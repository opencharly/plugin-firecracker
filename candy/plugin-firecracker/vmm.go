package firecracker

// vmm.go — the Firecracker API client.
//
// The VMM exposes a REST surface on a UNIX SOCKET (`--api-sock`): a guest is configured by PUTting
// the boot source, the drives, the machine config, the network interfaces and the MMDS metadata, and
// STARTED with an action. NOTHING in this sequence is invented here — it is the one the integration's
// RDD proved end to end (ledger probe P2: a real guest with TAP + NAT + SSH + internet, reading the
// fireactions agent's own JIT config from MMDS v2 inside the guest):
//
//	PUT /boot-source                 {kernel_image_path, boot_args}
//	PUT /drives/<id>                 {drive_id, path_on_host, is_root_device, is_read_only}
//	PUT /machine-config              {vcpu_count, mem_size_mib, smt}
//	PUT /network-interfaces/<id>     {iface_id, guest_mac, host_dev_name}
//	PUT /mmds/config                 {network_interfaces, version: "V2", ipv4_address, imds_compat}
//	PUT /mmds                       {…the guest's metadata…}
//	PUT /actions                     {action_type: "InstanceStart"}
//
// Every request is checked against the VMM's OWN success signal (204 No Content) and every document
// that matters is READ BACK, because "the API accepted it" and "the VMM is configured that way" are
// two different claims and only the second one is the product.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/opencharly/spec/spec"
)

// The API's own paths. Named once so the client and its tests cannot drift apart.
const (
	apiVersionPath   = "/version"
	apiBootSource    = "/boot-source"
	apiMachineConfig = "/machine-config"
	apiMMDSConfig    = "/mmds/config"
	apiMMDS          = "/mmds"
	apiActions       = "/actions"
	apiDrivesPrefix  = "/drives/"
	apiIfacesPrefix  = "/network-interfaces/"
)

const (
	// vmmCallTimeout bounds ONE API call. A wedged VMM must not hang a check.
	vmmCallTimeout = 20 * time.Second

	// vmmReadyTimeout bounds the wait for a freshly launched VMM's socket to answer.
	vmmReadyTimeout = 10 * time.Second

	// mmdsGuestIP is the link-local address the VMM serves MMDS v2 on by default.
	mmdsGuestIP = "169.254.169.254"

	// defaultVCPU / defaultMemMiB are the machine shape a lease gets when the caller names none. Two
	// vCPU and 2 GiB is what the proven path used and what a GitHub runner needs to be useful.
	defaultVCPU   = 2
	defaultMemMiB = 2048
)

// vmmLease is everything the VMM must be told to run ONE microVM.
type vmmLease struct {
	// Name is the lease identity (the per-lease microVM name, never a shared entity name).
	Name string

	KernelPath string
	// GuestIP and TAPIP are the two ends of the /30 the guest's interface is configured with; they are
	// part of the kernel command line because a microVM has no DHCP and no cloud-init to ask.
	GuestIP string
	TAPIP   string
	// BootArgs overrides the computed command line when the caller has its own.
	BootArgs string

	RootDrive string
	ReadOnly  bool

	VCPU   int
	MemMiB int

	IfaceID  string
	GuestMAC string
	TAP      string

	// MMDS is the metadata tree the guest reads over MMDS v2 — the fireactions agent's
	// `runner_jit_config` lives here, which is the whole point of the integration.
	MMDS map[string]any
}

// socketPath is the API socket for a lease.
func (l vmmLease) socketPath(runDir string) string {
	return filepath.Join(runDir, "firecracker.sock")
}

// serialLogPath is where the guest's console output lands — the FIRST place to look when a guest does
// not come up, and the file a check greps for the kernel's own boot lines.
func (l vmmLease) serialLogPath(runDir string) string {
	return filepath.Join(runDir, "serial.log")
}

// bootArgs is the kernel command line. A microVM gets NO DHCP and NO cloud-init unless the image asks
// for it, so the guest's address is passed on the command line exactly as the proven path did.
func (l vmmLease) bootArgs() string {
	if l.BootArgs != "" {
		return l.BootArgs
	}

	args := "console=ttyS0 reboot=k panic=1 pci=off nomodules rw"

	if l.GuestIP != "" && l.TAPIP != "" {
		// ip=<guest>::<gateway>:<netmask>::<iface>:off — no DHCP, no autoconf.
		args += fmt.Sprintf(" ip=%s::%s:255.255.255.252::eth0:off", l.GuestIP, l.TAPIP)
	}

	return args
}

func (l vmmLease) vcpu() int {
	if l.VCPU > 0 {
		return l.VCPU
	}

	return defaultVCPU
}

func (l vmmLease) memMiB() int {
	if l.MemMiB > 0 {
		return l.MemMiB
	}

	return defaultMemMiB
}

func (l vmmLease) ifaceID() string {
	if l.IfaceID != "" {
		return l.IfaceID
	}

	return "eth0"
}

// vmmClient speaks to one VMM's API socket.
type vmmClient struct {
	socket string
	http   *http.Client
}

func newVMMClient(socket string) *vmmClient {
	return &vmmClient{
		socket: socket,
		http: &http.Client{
			Timeout: vmmCallTimeout,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer

					return d.DialContext(ctx, "unix", socket)
				},
			},
		},
	}
}

// call performs one API request. A 2xx means the VMM accepted it; anything else is an error carrying
// the VMM's OWN message, because its body is the only thing that says what it disliked.
func (c *vmmClient) call(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader

	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal %s body: %w", path, err)
		}

		reader = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, "http://localhost"+path, reader)
	if err != nil {
		return fmt.Errorf("build %s request: %w", path, err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // a read-only body close cannot fail usefully here

	payload, _ := io.ReadAll(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, strings.TrimSpace(string(payload)))
	}

	if out != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, out); err != nil {
			return fmt.Errorf("%s %s: decode reply: %w", method, path, err)
		}
	}

	return nil
}

// version asks the VMM what it is — the FIRST call of any conversation, and the one that proves the
// socket is a VMM's and not merely a socket that exists.
func (c *vmmClient) version(ctx context.Context) (string, error) {
	var v struct {
		FirecrackerVersion string `json:"firecracker_version"`
	}

	if err := c.call(ctx, http.MethodGet, apiVersionPath, nil, &v); err != nil {
		return "", err
	}

	return v.FirecrackerVersion, nil
}

// configure writes the WHOLE lease into a running VMM and reads the decisive documents back.
func (c *vmmClient) configure(ctx context.Context, l vmmLease) error {
	if err := c.call(ctx, http.MethodPut, apiBootSource, map[string]any{
		"kernel_image_path": l.KernelPath,
		"boot_args":         l.bootArgs(),
	}, nil); err != nil {
		return fmt.Errorf("boot source: %w", err)
	}

	if err := c.call(ctx, http.MethodPut, apiDrivesPrefix+"rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   l.RootDrive,
		"is_root_device": true,
		"is_read_only":   l.ReadOnly,
	}, nil); err != nil {
		return fmt.Errorf("root drive: %w", err)
	}

	if err := c.call(ctx, http.MethodPut, apiMachineConfig, map[string]any{
		"vcpu_count":   l.vcpu(),
		"mem_size_mib": l.memMiB(),
		"smt":          false,
	}, nil); err != nil {
		return fmt.Errorf("machine config: %w", err)
	}

	if l.TAP != "" {
		if err := c.call(ctx, http.MethodPut, apiIfacesPrefix+l.ifaceID(), map[string]any{
			"iface_id":      l.ifaceID(),
			"guest_mac":     l.GuestMAC,
			"host_dev_name": l.TAP,
		}, nil); err != nil {
			return fmt.Errorf("network interface %s: %w", l.ifaceID(), err)
		}
	}

	if len(l.MMDS) > 0 {
		// MMDS v2 needs the interface list AND a token TTL before the tree is written: the guest must
		// fetch a token first, which is what the fireactions agent does.
		if err := c.call(ctx, http.MethodPut, apiMMDSConfig, map[string]any{
			"network_interfaces": []string{l.ifaceID()},
			"version":            "V2",
			"ipv4_address":       mmdsGuestIP,
			"imds_compat":        false,
		}, nil); err != nil {
			return fmt.Errorf("mmds config: %w", err)
		}

		if err := c.call(ctx, http.MethodPut, apiMMDS, l.MMDS, nil); err != nil {
			return fmt.Errorf("mmds tree: %w", err)
		}
	}

	// READ BACK the two documents that decide what the guest is: "the API said 204" and "the VMM is
	// configured this way" are different claims.
	var boot struct {
		KernelImagePath string `json:"kernel_image_path"`
		BootArgs        string `json:"boot_args"`
	}

	if err := c.call(ctx, http.MethodGet, apiBootSource, nil, &boot); err != nil {
		return fmt.Errorf("read back boot source: %w", err)
	}

	if boot.KernelImagePath != l.KernelPath {
		return fmt.Errorf("boot source read back as %q, want %q", boot.KernelImagePath, l.KernelPath)
	}

	var machine struct {
		VCPUCount  int `json:"vcpu_count"`
		MemSizeMiB int `json:"mem_size_mib"`
	}

	if err := c.call(ctx, http.MethodGet, apiMachineConfig, nil, &machine); err != nil {
		return fmt.Errorf("read back machine config: %w", err)
	}

	if machine.VCPUCount != l.vcpu() || machine.MemSizeMiB != l.memMiB() {
		return fmt.Errorf("machine config read back as %d vcpu / %d MiB, want %d / %d",
			machine.VCPUCount, machine.MemSizeMiB, l.vcpu(), l.memMiB())
	}

	if len(l.MMDS) > 0 {
		var tree map[string]any
		if err := c.call(ctx, http.MethodGet, apiMMDS, nil, &tree); err != nil {
			return fmt.Errorf("read back mmds tree: %w", err)
		}

		// The invariant is EQUALITY, not the presence of one particular key. An earlier version
		// hard-required a `fireactions` key here — which contradicted this plugin's own CLI, because
		// `parseMMDS` adds that key only when a pool's JIT config is supplied: a legitimate bare lease
		// would have been rejected by the very client that built it, and the `vmm` probe (whose lease
		// carries only charly's identity) would have failed its own read-back. What the client must
		// prove is that the tree the VMM serves is the tree it wrote.
		if !reflect.DeepEqual(tree, l.MMDS) {
			return fmt.Errorf("the mmds tree read back does not match the tree written: got %v, wrote %v", tree, l.MMDS)
		}
	}

	return nil
}

// start tells the VMM to boot the guest.
func (c *vmmClient) start(ctx context.Context) error {
	return c.call(ctx, http.MethodPut, apiActions, map[string]any{"action_type": "InstanceStart"}, nil)
}

// shutdown asks the VMM to power the guest down. The VMM exits after the guest's poweroff, so the
// caller still reaps the process — the API call is the graceful path, not the whole teardown.
func (c *vmmClient) shutdown(ctx context.Context) error {
	return c.call(ctx, http.MethodPut, apiActions, map[string]any{"action_type": "SendCtrlAltDel"}, nil)
}

// waitReady polls the API until it answers or the deadline passes. A socket that EXISTS is not a
// service: the first call is the proof (ledger probe P2's lesson).
func (c *vmmClient) waitReady(ctx context.Context, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		if v, err := c.version(ctx); err == nil {
			return v, nil
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("the VMM API socket %s did not answer /version within %s", c.socket, timeout)
		}

		time.Sleep(100 * time.Millisecond)
	}
}

// lookPathVMM resolves the VMM binary. Shared by the probe and by launch so the two cannot disagree
// about which binary they mean.
func lookPathVMM() (string, error) {
	found, err := exec.LookPath(defaultVMMBinary)
	if err != nil {
		return "", fmt.Errorf("no %s on PATH: install a VMM, or name one with binary/--binary", defaultVMMBinary)
	}

	return found, nil
}

// startVMM launches the VMM binary with an API socket and a serial log, and waits until its API
// answers. The returned command is the caller's to stop: the VMM is a child process, not a daemon.
func startVMM(ctx context.Context, binary string, l vmmLease, runDir string) (*exec.Cmd, string, error) {
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, "", fmt.Errorf("create run dir %s: %w", runDir, err)
	}

	sock := l.socketPath(runDir)
	logPath := l.serialLogPath(runDir)

	// A stale socket from a previous run would make the client talk to nothing; the API socket is
	// per-run, so removing it before launch is the correct reset, not a retry.
	_ = os.Remove(sock)

	logFile, err := os.Create(logPath)
	if err != nil {
		return nil, "", fmt.Errorf("create serial log %s: %w", logPath, err)
	}
	defer logFile.Close() //nolint:errcheck // the child holds its own descriptor

	cmd := exec.CommandContext(ctx, binary,
		"--api-sock", sock,
		"--log-path", filepath.Join(runDir, "vmm.log"),
		"--level", "Info")
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start %s: %w", binary, err)
	}

	client := newVMMClient(sock)

	version, err := client.waitReady(ctx, vmmReadyTimeout)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()

		return nil, "", err
	}

	return cmd, version, nil
}

// bootGrace is how long a freshly started instance must keep the VMM alive before launch claims it.
const bootGrace = 2 * time.Second

// awaitVMMAlive asserts the VMM survived the first moments of the boot. A guest that dies takes the
// VMM with it, so this is the cheapest honest distinction between "the action was accepted" and "a
// guest is running" — and it names the serial log, which is where the reason is.
func awaitVMMAlive(ctx context.Context, c *vmmClient, serialLog string) error {
	deadline := time.Now().Add(bootGrace)

	for time.Now().Before(deadline) {
		if _, err := c.version(ctx); err != nil {
			return fmt.Errorf("the VMM exited within %s of InstanceStart, so the guest did not come up: %w (serial log: %s)", bootGrace, err, serialLog)
		}

		time.Sleep(100 * time.Millisecond)
	}

	return nil
}

// leaseDir is where a lease's record lives. It is charly-owned state under the charly config root,
// one file per lease, so a status/stop invocation in a NEW process can find the lease it did not
// create (a process-local handle would make those commands impossible).
func leaseDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home: %w", err)
	}

	return filepath.Join(home, ".config", "charly", "firecracker"), nil
}

// SaveLease persists a lease record. The record IS spec.FirecrackerDeployState — the contract's own
// shape — so the deploy path and these commands can never disagree about what a lease is.
func SaveLease(st spec.FirecrackerDeployState) (string, error) {
	if st.Name == "" {
		return "", fmt.Errorf("a lease record needs a name")
	}

	dir, err := leaseDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create lease dir %s: %w", dir, err)
	}

	path := filepath.Join(dir, st.Name+".json")

	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal lease %s: %w", st.Name, err)
	}

	if err := os.WriteFile(path, append(b, '\n'), 0o600); err != nil {
		return "", fmt.Errorf("write lease %s: %w", path, err)
	}

	return path, nil
}

// LoadLease reads a lease record by name.
func LoadLease(name string) (spec.FirecrackerDeployState, error) {
	var st spec.FirecrackerDeployState

	dir, err := leaseDir()
	if err != nil {
		return st, err
	}

	b, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if err != nil {
		return st, fmt.Errorf("read lease %s: %w", name, err)
	}

	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("decode lease %s: %w", name, err)
	}

	return st, nil
}

// RemoveLease forgets a lease record (the caller has already stopped the VMM).
func RemoveLease(name string) error {
	dir, err := leaseDir()
	if err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(dir, name+".json")); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove lease %s: %w", name, err)
	}

	return nil
}

// leaseRunDir is where a lease's sockets and logs live: under the charly config root, keyed by the
// lease name, so the record's paths and the artefacts on disk cannot drift.
func leaseRunDir(name string) (string, error) {
	dir, err := leaseDir()
	if err != nil {
		return "", err
	}

	return filepath.Join(dir, "run", name), nil
}

// parseInt is the small helper the CLI uses for its numeric flags, kept here so the parsing rules
// (and their errors) live with the state they fill.
func parseInt(flag, raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("%s must be a whole number, got %q", flag, raw)
	}

	return n, nil
}
