package firecracker

// vmm_test.go — the gates for the Firecracker API client.
//
// The client is proved against a FAKE VMM THAT SPEAKS THE REAL PROTOCOL over a real UNIX SOCKET: the
// server records every request the client sends and answers exactly like the VMM does (204 for a PUT,
// JSON for the read-backs). That makes the assertions about the WIRE — the paths, the method, the
// field names — which is where a client can be wrong while still compiling and "passing" against a
// mock of its own shape.
//
// The live half (a real VMM binary accepting the same documents) is the repo's R10 bed, which
// installs the VMM through layer-firecracker; this file is the deterministic gate that fails the
// moment the protocol drifts.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencharly/spec/spec"
)

// recorded is one request the client made.
type recorded struct {
	Method string
	Path   string
	Body   map[string]any
}

// fakeVMM is a VMM-shaped API on a unix socket.
type fakeVMM struct {
	mu       sync.Mutex
	requests []recorded
	boot     map[string]any
	machine  map[string]any
	mmds     map[string]any
	// reject is a path answered with 400 instead of 204 — the control that proves a rejected document
	// is reported rather than swallowed.
	reject string
	// mutateMMDS, when set, is served for GET /mmds INSTEAD of the tree that was written — the control
	// for the read-back equality check.
	mutateMMDS map[string]any
	// version is what /version answers; a test can set it to prove the client reads it.
	version string
	socket  string
	srv     *http.Server
}

func newFakeVMM(t *testing.T) *fakeVMM {
	t.Helper()

	dir := t.TempDir()
	socket := filepath.Join(dir, "fc.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen on %s: %v", socket, err)
	}

	f := &fakeVMM{socket: socket, version: "1.17.0"}

	mux := http.NewServeMux()
	mux.HandleFunc("/", f.handle)

	f.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}

	go func() { _ = f.srv.Serve(ln) }()

	t.Cleanup(func() { _ = f.srv.Close() })

	return f
}

func (f *fakeVMM) handle(w http.ResponseWriter, r *http.Request) {
	var body map[string]any

	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}

	f.mu.Lock()
	f.requests = append(f.requests, recorded{Method: r.Method, Path: r.URL.Path, Body: body})

	switch r.URL.Path {
	case apiBootSource:
		if r.Method == http.MethodPut {
			f.boot = body
		}
	case apiMachineConfig:
		if r.Method == http.MethodPut {
			f.machine = body
		}
	case apiMMDS:
		if r.Method == http.MethodPut {
			f.mmds = body
		}
	}

	reject := f.reject
	mutate := f.mutateMMDS
	version := f.version
	boot := f.boot
	machine := f.machine
	mmds := f.mmds
	f.mu.Unlock()

	if reject != "" && r.URL.Path == reject {
		http.Error(w, `{"fault_message":"rejected by the control"}`, http.StatusBadRequest)

		return
	}

	switch {
	case r.URL.Path == apiVersionPath:
		writeJSON(w, map[string]any{"firecracker_version": version})
	case r.URL.Path == apiBootSource && r.Method == http.MethodGet:
		if boot == nil {
			http.Error(w, `{"fault_message":"no boot source"}`, http.StatusBadRequest)

			return
		}

		writeJSON(w, boot)
	case r.URL.Path == apiMachineConfig && r.Method == http.MethodGet:
		if machine == nil {
			http.Error(w, `{"fault_message":"no machine config"}`, http.StatusBadRequest)

			return
		}

		writeJSON(w, machine)
	case r.URL.Path == apiMMDS && r.Method == http.MethodGet:
		if mutate != nil {
			writeJSON(w, mutate)

			return
		}

		writeJSON(w, mmds)
	case r.Method == http.MethodPut:
		w.WriteHeader(http.StatusNoContent)
	default:
		writeJSON(w, map[string]any{})
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// paths returns the request paths in order.
func (f *fakeVMM) paths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.requests))
	for _, r := range f.requests {
		out = append(out, r.Method+" "+r.Path)
	}

	return out
}

func testLease() vmmLease {
	return vmmLease{
		Name:       "fc-test-01",
		KernelPath: "/var/lib/firecracker/kernels/5.10/vmlinux",
		RootDrive:  "/var/lib/firecracker/leases/fc-test-01/rootfs.ext4",
		GuestIP:    "172.16.0.2",
		TAPIP:      "172.16.0.1",
		TAP:        "fc-tap-01",
		GuestMAC:   "06:00:AC:10:00:02",
		VCPU:       2,
		MemMiB:     2048,
		MMDS: map[string]any{
			"fireactions": map[string]any{"runner_jit_config": "JIT", "hostname": "fc-test-01"},
			"charly":      map[string]any{"lease": "fc-test-01"},
		},
	}
}

// TestVMMClient_ConfigureSendsTheProvenSequence is the protocol gate: the exact paths, in the exact
// order the integration's RDD proved against a real VMM, with the field names the VMM expects.
func TestVMMClient_ConfigureSendsTheProvenSequence(t *testing.T) {
	f := newFakeVMM(t)
	c := newVMMClient(f.socket)

	if err := c.configure(context.Background(), testLease()); err != nil {
		t.Fatalf("configure against a VMM-shaped API: %v", err)
	}

	want := []string{
		"PUT " + apiBootSource,
		"PUT " + apiDrivesPrefix + "rootfs",
		"PUT " + apiMachineConfig,
		"PUT " + apiIfacesPrefix + "eth0",
		"PUT " + apiMMDSConfig,
		"PUT " + apiMMDS,
		"GET " + apiBootSource,
		"GET " + apiMachineConfig,
		"GET " + apiMMDS,
	}

	got := f.paths()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("request sequence:\n got %v\nwant %v", got, want)
	}

	// The documents themselves: a client can send the right paths with the wrong fields and still look
	// healthy on its own mock.
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.boot["kernel_image_path"] != testLease().KernelPath {
		t.Errorf("boot source kernel_image_path = %v", f.boot["kernel_image_path"])
	}

	if !strings.Contains(fmt.Sprint(f.boot["boot_args"]), "ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off") {
		t.Errorf("boot args carry no static address, so the guest would have no network: %v", f.boot["boot_args"])
	}

	if f.machine["vcpu_count"] != float64(2) || f.machine["mem_size_mib"] != float64(2048) {
		t.Errorf("machine config = %v", f.machine)
	}

	fa, ok := f.mmds["fireactions"].(map[string]any)
	if !ok || fa["runner_jit_config"] != "JIT" {
		t.Errorf("the mmds tree lost the fireactions JIT config the guest agent registers with: %v", f.mmds)
	}
}

// TestVMMClient_MMDSReadBackMustMatchWhatWasWritten asserts the invariant that replaced a hard-coded
// key requirement. The old form demanded a `fireactions` key unconditionally — contradicting this
// plugin's own CLI, which adds that key only when a pool's JIT config is supplied, so a legitimate bare
// lease was rejected by the client that built it, and the `vmm` probe (charly's identity only) could
// never pass its own read-back.
func TestVMMClient_MMDSReadBackMustMatchWhatWasWritten(t *testing.T) {
	// A lease with no metadata at all, and no interface: nothing to couple, nothing to refuse.
	f := newFakeVMM(t)
	bare := vmmLease{Name: "bare", KernelPath: "/k", RootDrive: "/r"}

	if err := newVMMClient(f.socket).configure(context.Background(), bare); err != nil {
		t.Fatalf("a lease with no mmds tree was rejected: %v", err)
	}

	// A lease WITH metadata and a NIC configures cleanly: the tree must have a way to be read.
	n := newFakeVMM(t)
	withTap := vmmLease{
		Name: "pool", KernelPath: "/k", RootDrive: "/r", TAP: "fc-tap0", GuestMAC: "06:00:AC:10:00:02",
		MMDS: map[string]any{"charly": map[string]any{"lease": "pool"}},
	}

	if err := newVMMClient(n.socket).configure(context.Background(), withTap); err != nil {
		t.Fatalf("a lease with mmds AND a tap was rejected: %v", err)
	}

	// …and WITHOUT one it is refused, by name: a guest reads metadata through an interface.
	if err := newVMMClient(f.socket).configure(context.Background(), vmmLease{
		Name: "broken", KernelPath: "/k", RootDrive: "/r",
		MMDS: map[string]any{"charly": map[string]any{"lease": "broken"}},
	}); err == nil {
		t.Fatal("a lease carrying mmds metadata with no network interface was accepted")
	}

	// And a VMM that serves a DIFFERENT tree than it was given must fail the read-back: equality is the
	// claim, so a mismatch is a real error rather than a tolerated difference.
	g := newFakeVMM(t)
	g.mutateMMDS = map[string]any{"charly": map[string]any{"lease": "SOMETHING-ELSE"}}

	if err := newVMMClient(g.socket).configure(context.Background(), withTap); err == nil {
		t.Fatal("a mmds tree that did not match what was written was accepted")
	}
}

// TestVMMClient_RejectedDocumentIsReported is the control: when the VMM refuses a document, the client
// must fail naming it — not proceed and report a configured guest.
func TestVMMClient_RejectedDocumentIsReported(t *testing.T) {
	f := newFakeVMM(t)
	f.reject = apiBootSource
	c := newVMMClient(f.socket)

	err := c.configure(context.Background(), testLease())
	if err == nil {
		t.Fatal("a refused boot source was accepted")
	}

	if !strings.Contains(err.Error(), "boot source") {
		t.Errorf("the error does not name the refused document: %v", err)
	}

	// Nothing after the refusal may have been sent: a half-configured VMM booted is worse than none.
	if got := f.paths(); len(got) > 2 {
		t.Errorf("the client kept going after a refusal: %v", got)
	}
}

// TestVMMClient_WrongSocketIsNotAVMM asserts the "a socket is not a service" lesson on the client
// side: a socket that exists but answers nothing usable must not be mistaken for a VMM.
func TestVMMClient_WrongSocketIsNotAVMM(t *testing.T) {
	dir := t.TempDir()
	socket := filepath.Join(dir, "not-a-vmm.sock")

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	defer ln.Close() //nolint:errcheck // test listener

	// A listener that never answers: exactly the shape of a wedged VMM.
	go func() {
		for {
			conn, aerr := ln.Accept()
			if aerr != nil {
				return
			}

			_ = conn // hold it open, answer nothing
		}
	}()

	c := newVMMClient(socket)
	if _, err := c.waitReady(context.Background(), 300*time.Millisecond); err == nil {
		t.Fatal("a socket that never answers was accepted as a ready VMM")
	}
}

// TestBootArgs asserts the kernel command line: the static address is present when both ends are known
// (a microVM has no DHCP), and an explicit override wins outright.
func TestBootArgs(t *testing.T) {
	got := vmmLease{GuestIP: "172.16.0.2", TAPIP: "172.16.0.1"}.bootArgs()
	if !strings.Contains(got, "ip=172.16.0.2::172.16.0.1:255.255.255.252::eth0:off") {
		t.Errorf("boot args = %q", got)
	}

	if !strings.Contains(got, "console=ttyS0") {
		t.Errorf("boot args lost the serial console, so a failed boot would be invisible: %q", got)
	}

	if override := (vmmLease{GuestIP: "a", TAPIP: "b", BootArgs: "console=ttyS0 root=/dev/vda"}).bootArgs(); override != "console=ttyS0 root=/dev/vda" {
		t.Errorf("override ignored: %q", override)
	}

	if bare := (vmmLease{}).bootArgs(); strings.Contains(bare, "ip=") {
		t.Errorf("an address was invented from nothing: %q", bare)
	}
}

// TestLease_RoundTrip asserts the lease record survives a process boundary — the reason it is a FILE
// and not a handle: `status` and `stop` run in a new process and must find the lease they did not
// create.
func TestLease_RoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	in := spec.FirecrackerDeployState{
		Host: "local", Name: "fc-test-01", APISock: "/run/fc.sock", TAP: "fc-tap-01",
		GuestIP: "172.16.0.2", GuestMAC: "06:00:AC:10:00:02", SSHPort: 2200,
		ImageRef: "ghcr.io/opencharly/fc-runner:v1", KernelRef: "fireactions/5.10", Pool: "pool-1",
	}

	path, err := SaveLease(in)
	if err != nil {
		t.Fatalf("SaveLease: %v", err)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the record was not written to %s: %v", path, err)
	}

	out, err := LoadLease("fc-test-01")
	if err != nil {
		t.Fatalf("LoadLease: %v", err)
	}

	if !reflect.DeepEqual(out, in) {
		t.Errorf("round trip changed the record:\n got %+v\nwant %+v", out, in)
	}

	if err := RemoveLease("fc-test-01"); err != nil {
		t.Fatalf("RemoveLease: %v", err)
	}

	if _, err := LoadLease("fc-test-01"); err == nil {
		t.Error("the record survived RemoveLease")
	}

	// Removing an absent lease is not an error: stop is idempotent.
	if err := RemoveLease("fc-never-existed"); err != nil {
		t.Errorf("removing an absent lease errored: %v", err)
	}
}

// TestLeaseRunDir asserts the per-lease run directory is keyed by the lease name, so two leases on one
// host cannot share a socket, a TAP or a serial log.
func TestLeaseRunDir(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	a, err := leaseRunDir("fc-a")
	if err != nil {
		t.Fatalf("leaseRunDir: %v", err)
	}

	b, _ := leaseRunDir("fc-b")
	if a == b {
		t.Fatalf("two leases share a run dir: %s", a)
	}

	if !strings.HasSuffix(a, filepath.Join("run", "fc-a")) {
		t.Errorf("run dir %s is not keyed by the lease", a)
	}
}

// TestParseInt asserts the numeric flags report a bad value instead of silently becoming zero (which
// would mean "use the default" — the wrong answer to a typo).
func TestParseInt(t *testing.T) {
	if n, err := parseInt("--vcpu", " 4 "); err != nil || n != 4 {
		t.Errorf("parseInt(4) = %d, %v", n, err)
	}

	if _, err := parseInt("--vcpu", "four"); err == nil {
		t.Error("a bad number was accepted")
	}
}
