package firecracker

// probe_vmm_test.go — the gate that makes the `vmm` probe's VENUE routing a proven claim.
//
// The defect this file exists for: the probe used to call exec.Command directly, so it ran on the
// machine charly runs on — never the guest a `vm:` step points at, and never where the pinned layer
// installed the VMM. A test double that only stubs the VMM cannot catch that; a double for the VENUE
// can, because the probe must reach its subject THROUGH it. These gates fail if the probe ever goes
// back to running anything locally.

import (
	"context"
	"strings"
	"testing"

	"github.com/opencharly/sdk/kit"
	"github.com/opencharly/spec/spec"

	"github.com/opencharly/plugin-firecracker/candy/plugin-firecracker/params"
)

// fakeVenue is the venue the probe is told to run on: it records every script, and answers the API
// readiness poll as a real VMM would.
type fakeVenue struct {
	kind    string
	scripts []string
	failOn  string // a script containing this text exits non-zero with failOut
	failOut string
}

func (f *fakeVenue) Kind() string { return f.kind }

func (f *fakeVenue) RunCapture(_ context.Context, script string) (string, string, int, error) {
	f.scripts = append(f.scripts, script)

	if f.failOn != "" && strings.Contains(script, f.failOn) {
		return f.failOut, "", 1, nil
	}

	if strings.Contains(script, "/version") {
		return `{"firecracker_version":"1.17.0"}`, "", 0, nil
	}

	return "", "", 0, nil
}

// fakeCheck is the CheckContext whose Exec routes to that venue. The interface is EMBEDDED so this
// double cannot drift out of step with it: the probe only ever calls Exec(), and an unstubbed method
// would panic loudly rather than silently returning a zero value.
type fakeCheck struct {
	kit.CheckContext
	venue *fakeVenue
}

func (f fakeCheck) Exec() kit.Executor {
	if f.venue == nil {
		return nil
	}

	return f.venue
}

// passed reports the probe's verdict without depending on how the SDK spells it.
func passed(res kit.Result) bool { return res.Status == spec.StatusPass }

var _ kit.CheckContext = fakeCheck{}

func probeInput() params.FirecrackerInput {
	return params.FirecrackerInput{
		Kernel: "/var/lib/firecracker/kernels/5.10/vmlinux",
		Rootfs: "/tmp/charly-fc-probe-rootfs.ext4",
	}
}

func joined(f *fakeVenue) string { return strings.Join(f.scripts, "\n===\n") }

// TestVMMProbe_DrivesItsSubjectThroughTheStepsVenue is the regression gate: everything the probe does
// must reach the venue, and the whole lease must be written and read back there.
func TestVMMProbe_DrivesItsSubjectThroughTheStepsVenue(t *testing.T) {
	v := &fakeVenue{kind: "vm"}

	res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput())
	if !passed(res) {
		t.Fatalf("the probe failed on a venue that answers: %s", res.Message)
	}

	all := joined(v)

	for _, want := range []string{
		"--api-sock", "/boot-source", "/drives/rootfs", "/machine-config", "/version", "kill",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("the probe never reached the venue with %q — it ran somewhere else", want)
		}
	}

	// The venue it names is the venue it used: a guest probe must SAY guest.
	if !strings.Contains(res.Message, "venue vm") {
		t.Errorf("the probe did not report the venue it ran on: %s", res.Message)
	}

	// The ready-wait must be a PROBE of the API, never a stat of the socket file.
	if !strings.Contains(all, "curl") || strings.Contains(all, "[ -S ") {
		t.Error("readiness was not probed through the API")
	}
}

// TestVMMProbe_NoNetworkInterfaceMeansNoMetadata asserts the coupling a guest actually has: metadata is
// read THROUGH an interface, so a probe with no tap must not write an mmds tree — the review's
// secondary finding, as a gate.
func TestVMMProbe_NoNetworkInterfaceMeansNoMetadata(t *testing.T) {
	v := &fakeVenue{kind: "vm"}

	if res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput()); !passed(res) {
		t.Fatalf("the probe failed: %s", res.Message)
	}

	if all := joined(v); strings.Contains(all, "/mmds/config") || strings.Contains(all, "/mmds ") {
		t.Error("an mmds tree was written without a network interface — a guest could never read it")
	}
}

// TestVMMProbe_WithATAPWritesTheMetadataHalf is the other half: given an interface, the metadata half is
// written and asserted.
func TestVMMProbe_WithATAPWritesTheMetadataHalf(t *testing.T) {
	v := &fakeVenue{kind: "vm"}
	in := probeInput()
	in.TAPName = "fc-tap0"
	in.GuestMAC = "06:00:AC:10:00:02"

	res := runVMMProbe(context.Background(), fakeCheck{venue: v}, in)
	if !passed(res) {
		t.Fatalf("the probe failed with a tap configured: %s", res.Message)
	}

	all := joined(v)

	for _, want := range []string{"/network-interfaces/eth0", "/mmds/config", "/mmds "} {
		if !strings.Contains(all, want) {
			t.Errorf("a tap was configured but %q was never written", want)
		}
	}
}

// TestVMMProbe_NoVenueIsANamedFailure: with no executor there is nowhere honest to run, so the probe
// refuses rather than silently probing charly's own host.
func TestVMMProbe_NoVenueIsANamedFailure(t *testing.T) {
	res := runVMMProbe(context.Background(), fakeCheck{}, probeInput())
	if passed(res) {
		t.Fatal("the probe passed with no venue executor — it must refuse")
	}

	if !strings.Contains(res.Message, "venue") {
		t.Errorf("the refusal does not name the venue: %s", res.Message)
	}
}

// TestVMMProbe_MissingArtefactIsReportedInTheVenue: the layer installs the VMM and the kernel; if it did
// not, that IS the finding, and the venue's own words carry it.
func TestVMMProbe_MissingArtefactIsReportedInTheVenue(t *testing.T) {
	v := &fakeVenue{kind: "vm", failOn: "no executable VMM", failOut: "no executable VMM in this venue (looked for /usr/local/bin/firecracker)"}

	res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput())
	if passed(res) {
		t.Fatal("the probe passed although the venue has no VMM")
	}

	if !strings.Contains(res.Message, "no executable VMM") {
		t.Errorf("the venue's own words were lost: %s", res.Message)
	}
}

// TestVMMProbe_UnreadyVMMIsANamedFailure: a venue that never answers /version must fail with the
// venue's message, not hang and not pass.
func TestVMMProbe_UnreadyVMMIsANamedFailure(t *testing.T) {
	v := &fakeVenue{kind: "vm", failOn: "did not answer /version", failOut: "the VMM did not answer /version within 10s"}

	res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput())
	if passed(res) {
		t.Fatal("the probe passed although the VMM never answered")
	}

	if !strings.Contains(res.Message, "did not come up") {
		t.Errorf("the failure does not say the VMM did not come up: %s", res.Message)
	}
}

// TestVMMProbe_OwnsItsScratchFixture pins the fix for the SKIP that made this bed red for a dishonest
// reason: "charly check live" is verify-only, so a run: step in the bed's plan is marked "skipped —
// verify-only mode (mutating step)" and can never satisfy a check in that same plan. The probe must
// therefore create its own scratch root drive IN THE VENUE and MEASURE it, rather than assume one.
func TestVMMProbe_OwnsItsScratchFixture(t *testing.T) {
	v := &fakeVenue{kind: "vm"}

	res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput())
	if !passed(res) {
		t.Fatalf("the probe failed: %s", res.Message)
	}

	artefact := v.scripts[0]

	for _, want := range []string{"mkdir -p", "truncate -s", "mke2fs", "blkid", "not an ext4 filesystem"} {
		if !strings.Contains(artefact, want) {
			t.Errorf("the fixture script does not %q — a fixture the probe does not measure is an assumption", want)
		}
	}

	if !strings.Contains(res.Message, probeRunDir) {
		t.Error("the verdict does not name the fixture it configured")
	}
}

// TestVMMProbe_ReadsBackOnlyWhatTheAPIServes gates the defect that made a healthy VMM fail every run:
// the probe asserted a read-back Firecracker does not implement. Measured on a real v1.17:
// GET /boot-source, GET /drives/<id> and GET /network-interfaces/<id> are 400 "Invalid request method
// and/or path"; GET / and GET /machine-config answer, and GET /mmds returns the tree. Documents with no
// readable endpoint are proven by their ACCEPTED WRITE (204, asserted per document), never by a GET.
func TestVMMProbe_ReadsBackOnlyWhatTheAPIServes(t *testing.T) {
	v := &fakeVenue{kind: "vm"}

	if res := runVMMProbe(context.Background(), fakeCheck{venue: v}, probeInput()); !passed(res) {
		t.Fatalf("the probe failed: %s", res.Message)
	}

	var cfg string

	for _, script := range v.scripts {
		if strings.Contains(script, "put /boot-source") {
			cfg = script
		}
	}

	if cfg == "" {
		t.Fatal("the configure script was not run")
	}

	for _, forbidden := range []string{"get /boot-source", "get /drives", "get /network-interfaces"} {
		if strings.Contains(cfg, forbidden) {
			t.Errorf("the configure script asserts %q, which Firecracker answers 400: it is unreadable by design", forbidden)
		}
	}

	for _, want := range []string{"get /", "get /machine-config", `'%{http_code}'`, "was refused"} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the configure script does not %q", want)
		}
	}
}
