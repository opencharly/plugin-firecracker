package firecracker

import (
	"os"
	"testing"
)

// TestElevationIsNarrowAndExplicit gates the producer's identity contract: the build and the engine reads
// run as the INVOKING user (their store holds the image, and their engine resolves DNS), while exactly
// four steps are elevated — the untar that preserves ownership, the du -sb measurement, the cleanup of a
// previous run's tree, and mke2fs, which must read root-only files.
//
// The LAW itself is asserted POSITIVELY, and it cannot be asserted here: proving that an extracted image
// tree comes out root-owned needs a real image and a real elevation, which is a bed's job, not a unit
// gate's. What this gate pins is the rule that decides when elevation happens at all.
func TestElevationIsNarrowAndExplicit(t *testing.T) {
	p := producer{Engine: "podman"}

	if os.Geteuid() == 0 {
		if p.sudoPrefix() != "" {
			t.Errorf("running as root: sudoPrefix() = %q, want empty (no elevation needed)", p.sudoPrefix())
		}
	} else if p.sudoPrefix() != "sudo -n " {
		t.Errorf("running unprivileged: sudoPrefix() = %q, want %q", p.sudoPrefix(), "sudo -n ")
	}
}

// TestRunImageCLI_UsageIsExplicit: the producer's argument contract, at the boundary where it is honest
// (no box, no build, nothing written).
func TestRunImageCLI_UsageIsExplicit(t *testing.T) {
	cases := []struct {
		name string
		args cliArgs
	}{
		{"no positional at all", cliArgs{}},
		{"image with no verb", cliArgs{positional: []string{}}},
		{"image build with no box", cliArgs{positional: []string{"build"}}},
		{"image with an unknown verb", cliArgs{positional: []string{"publish", "box"}}},
	}

	for _, c := range cases {
		if got := runImageCLI(c.args); got != 2 {
			t.Errorf("%s: exit %d, want 2 (usage)", c.name, got)
		}
	}
}

// TestDuBytes parses what the elevated measurement reports. The sizing itself now runs through the same
// elevation as the untar and mke2fs, so what a unit gate can pin is the parse and the arithmetic.
func TestDuBytes(t *testing.T) {
	if n, err := duBytes("8388608\t/tmp/tree\n"); err != nil || n != 8388608 {
		t.Errorf("duBytes = (%d, %v), want (8388608, nil)", n, err)
	}

	if _, err := duBytes(""); err == nil {
		t.Error("empty du output was accepted")
	}

	if _, err := duBytes("notanumber\t/tmp/tree"); err == nil {
		t.Error("unparseable du output was accepted")
	}
}

// TestParseArgs_OnlyImageTakesAPositional: every other subcommand keeps rejecting stray arguments, so
// the extension for the producer cannot quietly loosen the CLI's contract.
func TestParseArgs_OnlyImageTakesAPositional(t *testing.T) {
	if _, err := parseArgs([]string{"doctor", "stray"}); err == nil {
		t.Error("doctor accepted a positional argument")
	}

	a, err := parseArgs([]string{"image", "build", "some-box"})
	if err != nil {
		t.Fatalf("image build some-box was rejected: %v", err)
	}

	if a.sub != "image" || len(a.positional) != 2 || a.positional[1] != "some-box" {
		t.Errorf("parsed %q with positional %v", a.sub, a.positional)
	}
}

// TestBoxMetadataIsTheLabelContract pins the artefact's label contract as a pure value: what the emitted
// image must say about itself, and where its root drive came from.
func TestBoxMetadataIsTheLabelContract(t *testing.T) {
	p := producer{Distro: "fedora", SSHUser: "root", BaseUser: "user"}

	m := p.boxMetadata("check-firecracker-rootfs")

	if m.Arch != "x86_64" || m.Init != "systemd" || m.Firmware != "bios" || m.CharlyInstall != "auto" {
		t.Errorf("the microVM shape is wrong: arch=%q init=%q firmware=%q charly_install=%q", m.Arch, m.Init, m.Firmware, m.CharlyInstall)
	}

	if m.Distro != "fedora" || m.SSHUser != "root" || m.BaseUser != "user" {
		t.Errorf("the caller's box facts did not reach the label: %+v", m)
	}

	if m.Source.Kind != "rootfs" || m.Source.Image != "check-firecracker-rootfs" {
		t.Errorf("the provenance is wrong: %+v", m.Source)
	}

	// boxVersion returns empty and says so: this build injects no CalVer stamp, so the artefact version
	// field is empty ON PURPOSE. The gate pins that the metadata carries exactly what boxVersion reports
	// - no invention between the two - and that an empty version is never filled with a guess.
	if m.Version != boxVersion() {
		t.Errorf("the artefact's version %q is not the build's stamp %q", m.Version, boxVersion())
	}
}

// TestPickImageRef pins the selection rule that was wrong once: a box build lands as
// localhost/<box>:<CalVer> (so a prefix match on the bare name misses it) and a rebuild adds a new CalVer
// while leaving the previous one behind (so the newest must win).
func TestPickImageRef(t *testing.T) {
	lines := []string{
		"quay.io/fedora/fedora:43",
		"localhost/check-firecracker-rootfs:2026.287.1825",
		"localhost/check-firecracker-rootfs:2026.287.1840",
		"localhost/check-other:2026.299.0000",
	}

	got, ok := pickImageRef(lines, "check-firecracker-rootfs")
	if !ok || got != "localhost/check-firecracker-rootfs:2026.287.1840" {
		t.Errorf("pickImageRef = (%q, %v), want the NEWEST match of the named box", got, ok)
	}

	if _, ok := pickImageRef(lines, "check-absent"); ok {
		t.Error("a box with no image was reported as found")
	}

	if _, ok := pickImageRef([]string{"localhost/check-firecracker-rootfs-extra:1"}, "check-firecracker-rootfs"); ok {
		t.Error("a sibling whose name merely contains the box name was accepted")
	}
}
