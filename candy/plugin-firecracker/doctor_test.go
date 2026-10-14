package firecracker

// doctor_test.go — the gates for the read-only host probe.
//
// These are deterministic WITHOUT privilege and without a live VMM: they assert the DECISIONS the
// probe makes (which snapshotter can back a root drive, what counts as a problem, how the verdict is
// worded) and the report's internal consistency on whatever host runs them. The live end of the
// probe is the R10 bed, where the verb runs against a real host and its answer is the verdict.

import (
	"strings"
	"testing"
)

// TestRootDriveCapable is the load-bearing gate of this slice: the VMM is handed a BLOCK DEVICE or a
// FILE, never a directory tree, so a directory-mounting snapshotter can never work — the constraint
// that made the fork's snapshotter configurability necessary (ledger WI-15). Flip either arm and a
// pool reports healthy on a host that can never boot a guest.
func TestRootDriveCapable(t *testing.T) {
	capable := map[string]bool{
		"devmapper": true,  // an LVM thin volume → a block device
		"blockfile": true,  // one file per snapshot → a file
		"overlayfs": false, // a directory tree, which no Firecracker drive can be built from
		"native":    false, // likewise
		"":          false, // no snapshotter named is not a capability
		"something": false, // anything unknown is refused rather than assumed
	}

	for snapshotter, want := range capable {
		if got := rootDriveCapable(snapshotter); got != want {
			t.Errorf("rootDriveCapable(%q) = %v, want %v", snapshotter, got, want)
		}
	}
}

// TestDoctor_NamedSnapshotterThatCannotWork asserts the probe FAILS a configuration no VMM can use,
// and says why — the report is the product here, so its wording is part of the contract.
func TestDoctor_NamedSnapshotterThatCannotWork(t *testing.T) {
	rep := runDoctor(doctorRequest{Snapshotter: "overlayfs"})

	if rep.RootDriveCapable {
		t.Error("overlayfs reported as root-drive-capable")
	}

	if !hasProblem(rep, "cannot back a microVM root drive") {
		t.Errorf("no problem named for overlayfs; problems = %v", rep.Problems)
	}

	if !strings.Contains(rep.summary(), "CANNOT back a root drive") {
		t.Errorf("summary hides the incapability: %s", rep.summary())
	}
}

// TestDoctor_UnanswerableBinary asserts that a VMM which EXISTS but cannot answer is a problem —
// the distinction a plain LookPath would miss, and the reason the probe runs `--version`.
func TestDoctor_UnanswerableBinary(t *testing.T) {
	rep := runDoctor(doctorRequest{Binary: "/nonexistent/firecracker-" + t.Name()})

	if !hasProblem(rep, "does not answer --version") {
		t.Errorf("an unanswerable binary produced no problem; problems = %v", rep.Problems)
	}

	if rep.BinaryVersion != "" {
		t.Errorf("version %q reported for a binary that cannot answer", rep.BinaryVersion)
	}
}

// TestDoctor_MissingArtefacts asserts a named artefact that is not there is a problem that NAMES the
// path: a kernel and a root drive are both required, and one without the other is a boot that dies.
func TestDoctor_MissingArtefacts(t *testing.T) {
	rep := runDoctor(doctorRequest{
		Kernel: "/nonexistent/vmlinux-" + t.Name(),
		Rootfs: "/nonexistent/rootfs-" + t.Name(),
	})

	if !hasProblem(rep, "kernel artefact") {
		t.Errorf("missing kernel produced no problem; problems = %v", rep.Problems)
	}

	if !hasProblem(rep, "root drive artefact") {
		t.Errorf("missing root drive produced no problem; problems = %v", rep.Problems)
	}

	if !strings.Contains(rep.summary(), "PROBLEMS:") {
		t.Errorf("summary does not carry the problems: %s", rep.summary())
	}
}

// TestDoctor_ReportIsInternallyConsistent is the invariant that holds on ANY host: the report never
// claims KVM without the device that backs the claim, and it always names the path it inspected.
// A probe that can pass on a host it never looked at is the failure this asserts against.
func TestDoctor_ReportIsInternallyConsistent(t *testing.T) {
	rep := runDoctor(doctorRequest{})

	if rep.KVMPath != kvmDevice {
		t.Errorf("kvm path = %q, want %q", rep.KVMPath, kvmDevice)
	}

	if rep.KVM && rep.KVMMode == "" {
		t.Error("KVM reported present without a mode: the claim has no evidence behind it")
	}

	if !rep.KVM && !hasProblem(rep, "no /dev/kvm") {
		t.Errorf("KVM absent but not named as a problem; problems = %v", rep.Problems)
	}

	if rep.Snapshotter == "" {
		t.Error("no snapshotter reported: the default is part of the answer")
	}

	if rep.ContainerdSocket == "" {
		t.Error("no containerd socket reported: the default is part of the answer")
	}

	if rep.summary() == "" {
		t.Error("empty summary: a check step would have nothing to show")
	}
}

// TestDoctor_FactoryDefaultsAreTheUpstreamOnes pins the two defaults a pool inherits, because a
// silent change to either would move every deployment's behaviour.
func TestDoctor_FactoryDefaultsAreTheUpstreamOnes(t *testing.T) {
	rep := runDoctor(doctorRequest{})

	if rep.Snapshotter != "devmapper" {
		t.Errorf("default snapshotter = %q, want devmapper (the upstream fireactions default)", rep.Snapshotter)
	}

	if rep.ContainerdSocket != "/run/containerd/containerd.sock" {
		t.Errorf("default socket = %q", rep.ContainerdSocket)
	}
}

// TestFirstLine asserts the version line is taken from a binary's real output shape (a banner first,
// the version on the first non-empty line), not from the whole blob.
func TestFirstLine(t *testing.T) {
	got := firstLine("\n  Firecracker v1.17.0\nmore text\n")
	if got != "Firecracker v1.17.0" {
		t.Errorf("firstLine = %q", got)
	}

	if firstLine("   \n\n") != "" {
		t.Error("firstLine of blank input must be empty")
	}
}

func hasProblem(rep doctorReport, substr string) bool {
	for _, p := range rep.Problems {
		if strings.Contains(p, substr) {
			return true
		}
	}

	return false
}

// realCTRPluginsLS is the REAL output of `ctr plugins ls`, captured from a live daemon — not a
// hand-written approximation of it. That distinction is the whole point of this gate: the bug it
// guards against was a parser written against an ASSUMED column order, and an assumed sample would
// have agreed with it.
const realCTRPluginsLS = `TYPE                                      ID                       PLATFORMS      STATUS
io.containerd.content.v1                  content                  -              ok
io.containerd.event.v1                    exchange                 -              ok
io.containerd.mount-handler.v1            erofs                    linux/amd64    ok
io.containerd.snapshotter.v1              blockfile                linux/amd64    skip
io.containerd.snapshotter.v1              btrfs                    linux/amd64    skip
io.containerd.snapshotter.v1              devmapper                linux/amd64    skip
io.containerd.snapshotter.v1              erofs                    linux/amd64    skip
io.containerd.snapshotter.v1              native                   linux/amd64    ok
io.containerd.snapshotter.v1              overlayfs                linux/amd64    ok
`

// TestSnapshotterStateFromCTR_RealOutput pins the parse against that real text.
func TestSnapshotterStateFromCTR_RealOutput(t *testing.T) {
	cases := []struct {
		snapshotter string
		want        string
		found       bool
	}{
		{"devmapper", "skip", true},
		{"blockfile", "skip", true},
		{"overlayfs", "ok", true},
		{"native", "ok", true},
		{"zfs", "", false},
	}

	for _, c := range cases {
		state, found := snapshotterStateFromCTR(realCTRPluginsLS, c.snapshotter)
		if found != c.found || state != c.want {
			t.Errorf("snapshotterStateFromCTR(%q) = (%q, %v), want (%q, %v)", c.snapshotter, state, found, c.want, c.found)
		}
	}

	// THE REGRESSION this gate exists for: an earlier parse read the THIRD column, which is PLATFORMS —
	// so a healthy host was reported as `state "linux/amd64"` plus a FALSE problem, i.e. the doctor
	// failed a host that was fine. The platform must never appear as a state.
	for _, name := range []string{"devmapper", "overlayfs"} {
		if state, _ := snapshotterStateFromCTR(realCTRPluginsLS, name); state == "linux/amd64" {
			t.Fatalf("the PLATFORMS column was read as the state for %q — the exact bug this guards", name)
		}
	}
}

// TestSnapshotterStateFromCTR_ShortRowIsNotEvidence asserts a truncated row proves nothing: the state is
// only the last field when all four columns are present.
func TestSnapshotterStateFromCTR_ShortRowIsNotEvidence(t *testing.T) {
	const truncated = "io.containerd.snapshotter.v1  devmapper  skip\n"

	if _, found := snapshotterStateFromCTR(truncated, "devmapper"); found {
		t.Error("a three-column row was accepted as evidence of a state")
	}
}

// TestDoctor_UnconfiguredSnapshotterInventsNoProblem gates the false Problems entry: with no snapshotter
// configured, the probe appended 'the daemon does not serve a snapshotter named "" at all', failing a
// host that configured none. A doctor must not report a problem for something nobody asked about.
func TestSnapshotterStateFromCTR_EmptyNameMatchesNothing(t *testing.T) {
	if _, found := snapshotterStateFromCTR(realCTRPluginsLS, ""); found {
		t.Fatal("an empty snapshotter name matched a row — it must match nothing")
	}
}
