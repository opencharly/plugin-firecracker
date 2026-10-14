package firecracker

// image.go — the ARTEFACT PRODUCER (ledger WI-7): a charly-built OCI image becomes a Firecracker root
// drive.
//
// THE OWNERSHIP-FIDELITY LAW, measured the hard way (ledger §6.5), is the reason this file exists as
// an engine rather than as three shell lines:
//
//	A rootfs built by extracting an OCI export AS A NON-ROOT USER comes out entirely owned by that uid.
//	`mke2fs -d` faithfully copies the SOURCE TREE's ownership, and a uid-1000 /root plus a uid-1000
//	authorized_keys makes sshd reject root's key under StrictModes — measured: `Permission denied
//	(publickey,password)`. The artefact looks fine until a guest refuses the key, which is hours of
//	debugging for a rootfs that was wrong from the start.
//
// So everything that READS the extracted tree runs elevated — the untar that buys the ownership, the
// measurement of it, and mke2fs — while the build and the engine reads stay with the invoking user (whose
// store holds the image and whose engine resolves DNS). The bed that proves this artefact asserts
// OWNERSHIP of named paths, not that files exist.
//
// The recipe itself is measured, not invented: `mke2fs -t ext4 -F -d <tree> <file>` builds a real ext4
// from a tree with no loop device, no device mapper and no LVM (7 ms for a small tree locally), which is
// what makes this viable on hosts whose storage path cannot back a root drive (the WI-15 defect).

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/opencharly/sdk/deploykit"
	"github.com/opencharly/spec/container"
	"github.com/opencharly/spec/spec"
)

// producer is everything the artefact producer needs, gathered so each step is testable on its own.
type producer struct {
	// Engine is the container engine name as charly knows it (podman/nerdctl/docker); the binary comes
	// from spec/container.EngineBinary, the SAME helper the SDK's VM-box emitter uses — a plugin does
	// not hand-roll engine invocations.
	Engine string
	// Charly is the charly binary used to build the box. Looked up on PATH: this command must work with
	// nothing but charly installed (R4).
	Charly string
	// Box is the box to build, as authored in the project.
	Box string
	// OutDir is where the root drive is written before it is emitted.
	OutDir string
	// Distro / SSHUser / BaseUser are the box's identity, carried into the artefact label.
	Distro   string
	SSHUser  string
	BaseUser string
}

// BuildRootDrive produces the root drive for a box and returns its path. It is the first half of the
// producer: build, extract as root, format.
func (p producer) BuildRootDrive(ctx context.Context) (string, error) {
	if p.Box == "" {
		return "", fmt.Errorf("no box named: usage is `charly firecracker image build <box>`")
	}

	// TWO IDENTITIES, deliberately — and root is not required for the whole producer.
	//
	// The build and the engine reads run as the INVOKING user (their engine is the one whose store the
	// image is in, and whose network resolves DNS), while exactly two steps are elevated: the untar that
	// materialises the tree (ownership fidelity — the law) and mke2fs (which must READ root-only files
	// such as /etc/gshadow, 0400 root). Measured on this host: rootful podman cannot resolve DNS while
	// the user's engine can, so a producer that demanded root for everything could not build here at all.
	if _, err := exec.LookPath("sudo"); err != nil && os.Geteuid() != 0 {
		return "", fmt.Errorf("the artefact producer needs either root or sudo: the untar must preserve ownership (a non-root untar yields a uid-owned rootfs whose sshd refuses root's key under StrictModes) and mke2fs must read root-only files in the tree")
	}

	if p.Charly == "" {
		return "", fmt.Errorf("the charly binary is not on PATH: this command works with nothing but charly, so it must be run where charly is installed")
	}

	if err := os.MkdirAll(p.OutDir, 0o755); err != nil { //nolint:gosec // a build output directory
		return "", fmt.Errorf("create the output directory: %w", err)
	}

	// 1. BUILD the box with charly itself. The producer does not know how to build an image and must
	//    not learn: `charly box build` is the one builder.
	if err := p.run(ctx, p.Charly, "box", "build", p.Box); err != nil {
		return "", fmt.Errorf("build box %q: %w", p.Box, err)
	}

	tree := filepath.Join(p.OutDir, "tree")

	// THE RULE, COMPLETE: everything that touches the extracted tree is elevated. Clearing it counts, so
	// a previous run's tree is removed through the same elevation as the untar that created it.
	if err := p.run(ctx, "sh", "-c", p.sudoPrefix()+"rm -rf "+shellArg(tree)); err != nil {
		return "", fmt.Errorf("clear the previous tree: %w", err)
	}

	// 1b. RESOLVE the tag charly actually applied. A box build tags the image with a CalVer
	// (measured: localhost/check-firecracker-rootfs:2026.287.1825), so the bare box name is NOT a usable
	// ref — asking the engine is the honest source.
	ref, err := p.resolveImageRef(ctx, p.Box)
	if err != nil {
		return "", err
	}

	// 2. EXTRACT the image's filesystem WITH ITS OWNERSHIP (the law above).
	if err := p.ExtractTree(ctx, ref, tree); err != nil {
		return "", err
	}

	// 3. EMIT the root drive from the tree.
	drive := filepath.Join(p.OutDir, "rootfs.ext4")

	if err := p.MkfsTree(ctx, tree, drive); err != nil {
		return "", err
	}

	return drive, nil
}

// ExtractTree materialises an image's filesystem at dest, preserving the ownership the image records.
func (p producer) ExtractTree(ctx context.Context, image, dest string) error {
	engine := container.EngineBinary(p.Engine)

	if err := os.MkdirAll(dest, 0o755); err != nil { //nolint:gosec // a build output directory
		return fmt.Errorf("create the extraction directory: %w", err)
	}

	// A container is CREATED, not run: `export` reads a stopped container's filesystem, and nothing here
	// needs the image to execute. The id is removed on every path out.
	create := exec.CommandContext(ctx, engine, "create", image) //nolint:gosec // the engine from charly's own settings

	out, err := create.Output()
	if err != nil {
		return fmt.Errorf("engine create %q: %w", image, err)
	}

	id := strings.TrimSpace(string(out))

	defer func() {
		_ = exec.CommandContext(ctx, engine, "rm", "-f", id).Run() //nolint:errcheck,gosec // best-effort cleanup of a stopped container
	}()

	if id == "" {
		return fmt.Errorf("engine create %q returned no container id", image)
	}

	// The ENGINE side stays as the invoking user (the image is in that store, and this needs no network);
	// the UNTAR is elevated, because ownership fidelity is exactly what it buys. One shell so the pipe is
	// one command and its failure is the pipeline's failure.
	export := exec.CommandContext(ctx, "sh", "-c",
		fmt.Sprintf("%s export %s | %star -x -C %s", container.EngineBinary(p.Engine), shellArg(id), p.sudoPrefix(), shellArg(dest))) //nolint:gosec // every operand is quoted

	if out, err := export.CombinedOutput(); err != nil {
		return fmt.Errorf("extract %q: %w: %s", image, err, strings.TrimSpace(string(out)))
	}

	return nil
}

// MkfsTree builds an ext4 filesystem from a directory tree, with no loop device and no device mapper.
func (p producer) MkfsTree(ctx context.Context, tree, drive string) error {
	if _, err := os.Stat(filepath.Join(tree, "etc")); err != nil {
		return fmt.Errorf("the extracted tree at %s does not look like a root filesystem (no etc/): %w", tree, err)
	}

	size, err := p.treeSizeMiB(ctx, tree)
	if err != nil {
		return err
	}

	// A sparse file first, so mke2fs writes into an already-sized image rather than growing one.
	if err := p.run(ctx, "truncate", "-s", fmt.Sprintf("%dM", size), drive); err != nil {
		return fmt.Errorf("size the root drive: %w", err)
	}

	if err := p.run(ctx, "sh", "-c", p.sudoPrefix()+"mke2fs -q -t ext4 -F -d "+shellArg(tree)+" "+shellArg(drive)); err != nil {
		if strings.Contains(err.Error(), "Permission denied") {
			return fmt.Errorf("build the ext4 root drive from %s: %w — a permission failure here means the tree holds root-only files, which mke2fs must read", tree, err)
		}

		return fmt.Errorf("build the ext4 root drive from %s: %w", tree, err)
	}

	return nil
}

// treeSizeMiB sizes the drive from the tree, with headroom. The measurement is a THIRD narrow elevation,
// for the same reason as the untar and mke2fs: an extracted image tree is root-owned (measured:
// /etc/sudoers.d is 0700 root), so a Go walk as the invoking user fails with permission denied — and it
// did, in the run that proved the extraction works. One `du -sb` through the same elevation replaces the
// walk: fewer moving parts, and the same identity rule.
func (p producer) treeSizeMiB(ctx context.Context, tree string) (int64, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", p.sudoPrefix()+"du -sb "+shellArg(tree)) //nolint:gosec // the operand is quoted

	out, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("measure the tree at %s: %w", tree, err)
	}

	total, err := duBytes(string(out))
	if err != nil {
		return 0, fmt.Errorf("measure the tree at %s: %w", tree, err)
	}

	// A quota of a quarter of the content on top of a 64 MiB floor, in whole MiB: a rootfs that is
	// exactly its content has nowhere for a guest to write.
	return total/(1024*1024) + total/(4*1024*1024) + 64, nil
}

// duBytes reads the byte count out of `du -sb`'s single output line.
func duBytes(out string) (int64, error) {
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 0 {
		return 0, fmt.Errorf("du reported nothing")
	}

	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("du reported %q: %w", fields[0], err)
	}

	return n, nil
}

// run executes a producer step, returning the command's own output on failure.
func (p producer) run(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // fixed producers, no shell

	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}

	return nil
}

// shellArg single-quotes a value for the shell.
func shellArg(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

// charlyBinary resolves the charly that builds the box: R9 says the WORKTREE-LOCAL, CalVer-stamped build,
// never a shared install — so the default is the charly that INVOKED this plugin (this process's parent),
// with CHARLY_BIN / --charly as explicit overrides and PATH only as a last resort.
//
// Measured, and it cost a whole debugging round: resolving via PATH found /usr/bin/charly — an older
// INSTALL whose loader still enforced a schema gate the current SDK deleted — so the producer died inside
// its first step with an error that had nothing to do with the code under test.
func charlyBinary() string {
	if p := os.Getenv("CHARLY_BIN"); p != "" {
		return p
	}

	if exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", os.Getppid())); err == nil && exe != "" {
		return exe
	}

	if p, err := exec.LookPath("charly"); err == nil {
		return p
	}

	return ""
}

// runImageCLI is `charly firecracker image build <box> [--out DIR] [--engine podman]`: the artefact
// producer (ledger WI-7). It builds the box, extracts the image tree AS ROOT (the ownership law), and
// formats the root drive — then reports the artefact's path and size.
//
// Publishing the artefact IS this command: EmitBox fills the AI-opencharly-vm-box label contract and hands
// the root drive and the kernel to deploykit.EmitVmBoxMicroVM, optionally pushing the result. (The comment
// here used to say publishing was "the next step" and named a symbol that does not exist; both were wrong.)
func runImageCLI(a cliArgs) int {
	if len(a.positional) == 0 || a.positional[0] != "build" || len(a.positional) < 2 {
		fmt.Fprintln(os.Stderr, "firecracker image: usage is `charly firecracker image build <box> [--out DIR] [--engine podman]`")

		return 2
	}

	box := a.positional[1]

	out := a.outDir
	if out == "" {
		out = filepath.Join(os.TempDir(), "charly-firecracker-artefacts", box)
	}

	engine := a.engine
	if engine == "" {
		engine = "podman"
	}

	charly := a.charly
	if charly == "" {
		charly = charlyBinary()
	}

	p := producer{Engine: engine, Charly: charly, Box: box, OutDir: out}

	drive, err := p.BuildRootDrive(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker image build %s: %v\n", box, err)

		return 1
	}

	info, err := os.Stat(drive)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker image build %s: built %s but cannot stat it: %v\n", box, drive, err)

		return 1
	}

	fmt.Printf("firecracker image build %s: root drive at %s (%d MiB, raw ext4)\n", box, drive, info.Size()/(1024*1024))

	if a.ref == "" {
		fmt.Println("not published: pass --ref <image-ref> to emit the microVM box image (the root drive AND the kernel, with the label contract)")

		return 0
	}

	p.Distro, p.SSHUser, p.BaseUser = a.distro, a.sshUser, a.baseUser

	fromImage := a.fromImage
	if fromImage == "" {
		fromImage = box
	}

	if err := p.EmitBox(context.Background(), a.ref, fromImage, drive, a.kernel, a.push); err != nil {
		fmt.Fprintf(os.Stderr, "firecracker image build %s: %v\n", box, err)

		return 1
	}

	if a.push {
		fmt.Printf("firecracker image build %s: emitted and pushed %s\n", box, a.ref)
	} else {
		fmt.Printf("firecracker image build %s: emitted %s (not pushed)\n", box, a.ref)
	}

	return 0
}

// EmitBox publishes the produced artefacts as ONE microVM box image: the root drive the VMM attaches and
// the kernel it boots with, at the shipped microVM paths, with the provenance recorded in the box label.
//
// The emitter records the artefact LAYOUT only after its build succeeds — the label must never claim a
// path the layer does not carry — and the provenance is the CALLER's to fill, because only the caller
// knows where the artefacts came from. So this fills the rootfs arm with the fact it knows (the image
// whose tree was materialised) and the kernel arm with what it was told, never with a guess.
func (p producer) EmitBox(ctx context.Context, ref, fromImage, rootfs, kernel string, push bool) error {
	if ref == "" {
		return fmt.Errorf("no artefact ref: emitting needs `--ref <image-ref>` to name the box image")
	}

	if kernel == "" {
		return fmt.Errorf("the microVM box carries the kernel TOO (the VMM boots a root drive plus a separate kernel): pass `--kernel <path>`")
	}

	for _, f := range []string{rootfs, kernel} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("emit %q: %w", ref, err)
		}
	}

	meta := p.boxMetadata(fromImage)

	if err := deploykit.EmitVmBoxMicroVM(p.Engine, ref, &meta, rootfs, kernel); err != nil {
		return fmt.Errorf("emit the microVM box %q: %w", ref, err)
	}

	if push {
		if err := p.run(ctx, container.EngineBinary(p.Engine), "push", ref); err != nil {
			return fmt.Errorf("push %q: %w", ref, err)
		}
	}

	return nil
}

// boxVersion is the CalVer the box label carries. Empty on purpose: the artefact's version is the
// release's business, and a producer that invented one would put a version in a label that no release
// ever published.
func boxVersion() string { return "" }

// sudoPrefix returns "sudo -n " when this process is not root, and nothing when it is. Elevation is
// explicit and narrow: only the untar and mke2fs pass through it, never the build or the engine reads.
func (p producer) sudoPrefix() string {
	if os.Geteuid() == 0 {
		return ""
	}

	return "sudo -n "
}

// boxMetadata is the LABEL CONTRACT the emitted artefact carries, built as a pure function so a unit gate
// can pin it without an engine, a tree or a build. Every field is read back out of the built image.
func (p producer) boxMetadata(fromImage string) spec.VmBoxMetadata {
	return spec.VmBoxMetadata{
		Version:       boxVersion(),
		Arch:          "x86_64",
		Init:          "systemd",
		Distro:        p.Distro,
		SSHUser:       p.SSHUser,
		BaseUser:      p.BaseUser,
		Firmware:      "bios",
		CharlyInstall: "auto",
		Source:        spec.VmBoxSource{Kind: "rootfs", Image: fromImage},
	}
}

// pickImageRef chooses the ref a box build produced out of the engine's image list. Pure, because the
// SELECTION was wrong once: a build lands as localhost/<box>:<CalVer>, so a prefix match on the bare box
// name misses it, and a rebuild adds a new CalVer while leaving the previous one behind.
func pickImageRef(lines []string, box string) (string, bool) {
	var refs []string

	for _, line := range lines {
		if r := strings.TrimSpace(line); strings.Contains(r, "/"+box+":") || strings.HasPrefix(r, box+":") {
			refs = append(refs, r)
		}
	}

	if len(refs) == 0 {
		return "", false
	}

	sort.Strings(refs) // CalVer tags sort lexically by time

	return refs[len(refs)-1], true
}

// resolveImageRef asks the engine which ref the box build produced. The newest match wins: a rebuild
// adds a new CalVer tag and leaves the previous one in place.
func (p producer) resolveImageRef(ctx context.Context, box string) (string, error) {
	out, err := exec.CommandContext(ctx, container.EngineBinary(p.Engine), //nolint:gosec // the engine from charly settings
		"images", "--format", "{{.Repository}}:{{.Tag}}").Output()
	if err != nil {
		return "", fmt.Errorf("ask the engine which image %q built: %w", box, err)
	}

	ref, ok := pickImageRef(strings.Split(string(out), "\n"), box)
	if !ok {
		return "", fmt.Errorf("the box build produced no image matching %q: the engine has no such ref", box)
	}

	return ref, nil
}
