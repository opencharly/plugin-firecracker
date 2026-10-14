package firecracker

// doctor.go — the read-only host probe: what can THIS host actually do with a microVM?
//
// Every check here answers a question the integration's RDD probes had to answer the hard way, and
// each one is stated as a PROBLEM rather than assumed away:
//
//   - /dev/kvm must exist and be usable. A host without it cannot run a KVM guest at all, however
//     many artefacts are present.
//   - the VMM binary must ANSWER, not merely exist: the probe runs `--version` and reports what it
//     says, because a binary on PATH that cannot start is indistinguishable from a missing one
//     until you ask it something.
//   - the kernel is a SEPARATE artefact from the root drive. The VMM boots both, so one without the
//     other is a boot that dies after the first instruction.
//   - the containerd snapshotter must be able to BACK A ROOT DRIVE. The VMM attaches a block device
//     (devmapper's thin volume) or a FILE (blockfile's per-snapshot file); a directory-mounting
//     snapshotter (overlayfs, native) hands back a directory that no Firecracker drive can be built
//     from. This is the constraint that made the fork's snapshotter configurability necessary
//     (ledger WI-15) — and it is a CONFIGURATION fact, so it is checked here without touching the
//     daemon, while the socket's reachability is checked separately.
//   - a socket file is not a service: reachability is a DIAL, not a stat (ledger probe P2).

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	// kvmDevice is the KVM character device every microVM needs.
	kvmDevice = "/dev/kvm"

	// defaultVMMBinary is the VMM looked up on PATH when the caller names none.
	defaultVMMBinary = "firecracker"

	// defaultContainerdSocket is the daemon socket reported on when the caller names none.
	defaultContainerdSocket = "/run/containerd/containerd.sock"

	// defaultSnapshotter is the snapshotter a pool gets when the caller names none: it is the
	// upstream fireactions default, and the one that needs an LVM thin pool on the host.
	defaultSnapshotter = "devmapper"

	// probeTimeout bounds every conversation this probe has with the host. A wedged binary or a
	// half-open socket must not hang a check — not answering is not an answer.
	probeTimeout = 5 * time.Second
)

// doctorRequest is what the probe needs. Every field is optional, so `doctor` answers with the
// defaults an operator would get.
type doctorRequest struct {
	Binary           string
	Kernel           string
	Rootfs           string
	Snapshotter      string
	ContainerdSocket string
}

// doctorReport is the probe's answer. `Problems` is the verdict: the verb FAILS when it is non-empty,
// so a bed cannot pass by reporting a host it never checked.
type doctorReport struct {
	KVM              bool     `json:"kvm"`
	KVMPath          string   `json:"kvm_path,omitempty"`
	KVMMode          string   `json:"kvm_mode,omitempty"`
	Binary           string   `json:"binary,omitempty"`
	BinaryVersion    string   `json:"binary_version,omitempty"`
	Kernel           string   `json:"kernel,omitempty"`
	Rootfs           string   `json:"rootfs,omitempty"`
	Snapshotter      string   `json:"snapshotter,omitempty"`
	RootDriveCapable bool     `json:"root_drive_capable"`
	ContainerdSocket string   `json:"containerd_socket,omitempty"`
	ContainerdUp     bool     `json:"containerd_reachable"`
	IPForward        string   `json:"ip_forward,omitempty"`
	Problems         []string `json:"problems,omitempty"`
}

// rootDriveCapable reports whether a containerd snapshotter can back a microVM root drive. Only the
// two block/file-producing snapshotters can: the VMM is handed a block device or a file, never a
// directory tree.
func rootDriveCapable(snapshotter string) bool {
	switch snapshotter {
	case "devmapper", "blockfile":
		return true
	default:
		return false
	}
}

// summary is the one-line verdict a check step shows.
func (r doctorReport) summary() string {
	parts := []string{
		fmt.Sprintf("kvm=%s", yesNo(r.KVM)),
		fmt.Sprintf("vmm=%s", orNone(r.BinaryVersion)),
		fmt.Sprintf("snapshotter=%s(%s)", r.Snapshotter, capableWord(r.RootDriveCapable)),
		fmt.Sprintf("containerd=%s", upWord(r.ContainerdUp)),
		fmt.Sprintf("forward=%s", orNone(r.IPForward)),
	}

	if r.Kernel != "" {
		parts = append(parts, "kernel="+r.Kernel)
	}

	line := "firecracker doctor: " + strings.Join(parts, " ")

	if len(r.Problems) > 0 {
		return line + " — PROBLEMS: " + strings.Join(r.Problems, "; ")
	}

	return line
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}

	return "no"
}

func upWord(b bool) string {
	if b {
		return "reachable"
	}

	return "unreachable"
}

func capableWord(b bool) string {
	if b {
		return "root-drive-capable"
	}

	return "CANNOT back a root drive"
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}

	return s
}

// runDoctor probes the host. It is read-only: it creates nothing, starts nothing, and needs no
// privilege beyond reading the paths it names.
func runDoctor(req doctorRequest) doctorReport {
	rep := doctorReport{
		Snapshotter:      req.Snapshotter,
		ContainerdSocket: req.ContainerdSocket,
	}

	if rep.Snapshotter == "" {
		rep.Snapshotter = defaultSnapshotter
	}

	if rep.ContainerdSocket == "" {
		rep.ContainerdSocket = defaultContainerdSocket
	}

	probeKVM(&rep)
	probeVMM(&rep, req.Binary)
	probeArtefact(&rep, "kernel", req.Kernel)
	probeArtefact(&rep, "root drive", req.Rootfs)
	probeSnapshotter(&rep)
	probeContainerd(&rep)
	probeForwarding(&rep)

	return rep
}

func probeKVM(rep *doctorReport) {
	rep.KVMPath = kvmDevice

	fi, err := os.Stat(kvmDevice)
	if err != nil {
		rep.Problems = append(rep.Problems,
			fmt.Sprintf("no %s: this host cannot run a KVM guest (enable virtualization / load the kvm module)", kvmDevice))

		return
	}

	rep.KVM = true
	rep.KVMMode = fi.Mode().String()
}

func probeVMM(rep *doctorReport, want string) {
	binary := want
	if binary == "" {
		found, err := exec.LookPath(defaultVMMBinary)
		if err != nil {
			rep.Problems = append(rep.Problems,
				fmt.Sprintf("no %s on PATH and none named: install a VMM, or name one with binary:", defaultVMMBinary))

			return
		}

		binary = found
	}

	rep.Binary = binary

	out, err := runBounded(binary, "--version")
	if err != nil {
		rep.Problems = append(rep.Problems,
			fmt.Sprintf("%s does not answer --version (%v): a VMM that cannot start is not a VMM", binary, err))

		return
	}

	rep.BinaryVersion = firstLine(out)
}

func probeArtefact(rep *doctorReport, what, path string) {
	if path == "" {
		return // nothing named to check; the report says so by leaving the field empty
	}

	if what == "kernel" {
		rep.Kernel = path
	} else {
		rep.Rootfs = path
	}

	if _, err := os.Stat(path); err != nil {
		rep.Problems = append(rep.Problems, fmt.Sprintf("%s artefact %s: %v", what, path, err))
	}
}

func probeSnapshotter(rep *doctorReport) {
	rep.RootDriveCapable = rootDriveCapable(rep.Snapshotter)

	if !rep.RootDriveCapable {
		rep.Problems = append(rep.Problems,
			fmt.Sprintf("snapshotter %q cannot back a microVM root drive: the VMM is handed a block device (devmapper) or a file (blockfile), never a directory tree", rep.Snapshotter))
	}
}

func probeContainerd(rep *doctorReport) {
	if _, err := os.Stat(rep.ContainerdSocket); err != nil {
		rep.Problems = append(rep.Problems, fmt.Sprintf("containerd socket %s: %v", rep.ContainerdSocket, err))

		return
	}

	// A socket FILE is not a service (probe P2): dial it, bounded.
	conn, err := net.DialTimeout("unix", rep.ContainerdSocket, probeTimeout)
	if err != nil {
		rep.Problems = append(rep.Problems,
			fmt.Sprintf("containerd socket %s exists but does not answer a dial (%v): a socket file is not a service", rep.ContainerdSocket, err))

		return
	}

	_ = conn.Close()

	rep.ContainerdUp = true
}

func probeForwarding(rep *doctorReport) {
	b, err := os.ReadFile("/proc/sys/net/ipv4/ip_forward")
	if err != nil {
		return // not every kernel exposes it; the report leaves it empty rather than inventing a value
	}

	rep.IPForward = strings.TrimSpace(string(b))

	if rep.IPForward == "0" {
		rep.Problems = append(rep.Problems,
			"ip_forward is 0: a TAP-backed guest cannot reach the network until forwarding is enabled")
	}
}

// runBounded runs a command with the probe's timeout and returns its combined output.
func runBounded(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)

	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), err
	}

	return string(out), nil
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(line); t != "" {
			return t
		}
	}

	return ""
}
