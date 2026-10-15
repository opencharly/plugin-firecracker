package firecracker

// cli.go — the `charly firecracker` CLI family.
//
// The command is the operator's half of the same capability the check verb serves:
//
//	charly firecracker doctor                     what can this host do?
//	charly firecracker launch --name … --kernel … --rootfs … --tap …
//	charly firecracker status --name …
//	charly firecracker stop   --name …
//
// The in-proc path (Invoke → runCommand) and the fork/exec path (CliMain → runCommand) run the SAME
// function, so the two placements cannot drift.
//
// A lease OUTLIVES this process on purpose: `launch` starts the VMM and records the lease, exactly as
// `charly start` brings a container up, and `status`/`stop` find it in a new process through the
// record. Supervising that process is the deploy lifecycle's job (the venue Ops), not this command's —
// and this comment is the boundary rather than a promise.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/opencharly/spec/spec"
)

// CliMain is the out-of-process CLI entry: charly fork/execs this binary with the pass-through tokens
// after `charly firecracker`.
func CliMain(args []string) int { return runCommand(args) }

// cliArgs is the parsed command line. One struct so every subcommand reads the same fields and a new
// flag lands in one place.
type cliArgs struct {
	// positional carries `image build <box>`'s box — the ONE positional any subcommand accepts.
	positional []string
	// outDir is where the producer writes the root drive; engine names the container engine to drive.
	outDir string
	engine string
	// The producer emit half: ref names the artefact image, image names the built box image whose tree
	// became the root drive, kernel describes the kernel artefact, the rest identify the box.
	charly    string
	ref       string
	fromImage string
	distro    string
	sshUser   string
	baseUser  string
	push      bool

	sub     string
	asJSON  bool
	name    string
	binary  string
	kernel  string
	rootfs  string
	tap     string
	guestIP string
	tapIP   string
	mac     string
	vcpu    string
	mem     string
	mmds    string
	image   string
	pool    string
	// the doctor's own flags, kept so its interface does not narrow when the family grows.
	snapshotter string
	socket      string
}

// runCommand is the command's ONE effect, shared by both placements. It returns a process exit code:
// a doctor or a launch that found problems exits non-zero, because a caller who scripted it must see
// the failure rather than parse prose for it.
func runCommand(args []string) int {
	parsed, err := parseArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker: %v\n", err)

		return 2
	}

	switch parsed.sub {
	case "doctor":
		return runDoctorCLI(parsed)
	case "launch":
		return runLaunchCLI(parsed)
	case "status":
		return runStatusCLI(parsed)
	case "stop":
		return runStopCLI(parsed)
	case "image":
		return runImageCLI(parsed)
	default:
		fmt.Fprintf(os.Stderr, "firecracker: unknown subcommand %q (doctor, launch, status, stop, image)\n", parsed.sub)

		return 2
	}
}

// valueFlags are the flags that take the NEXT token as their value.
var valueFlags = map[string]func(*cliArgs, string){
	"--charly":     func(a *cliArgs, v string) { a.charly = v },
	"--ref":        func(a *cliArgs, v string) { a.ref = v },
	"--from-image": func(a *cliArgs, v string) { a.fromImage = v },
	"--distro":     func(a *cliArgs, v string) { a.distro = v },
	"--ssh-user":   func(a *cliArgs, v string) { a.sshUser = v },
	"--base-user":  func(a *cliArgs, v string) { a.baseUser = v },
	"--out":        func(a *cliArgs, v string) { a.outDir = v },
	"--engine":     func(a *cliArgs, v string) { a.engine = v },
	"--name":       func(a *cliArgs, v string) { a.name = v },
	"--binary":     func(a *cliArgs, v string) { a.binary = v },
	"--kernel":     func(a *cliArgs, v string) { a.kernel = v },
	"--rootfs":     func(a *cliArgs, v string) { a.rootfs = v },
	"--tap":        func(a *cliArgs, v string) { a.tap = v },
	"--guest-ip":   func(a *cliArgs, v string) { a.guestIP = v },
	"--tap-ip":     func(a *cliArgs, v string) { a.tapIP = v },
	"--guest-mac": func(a *cliArgs, v string) {
		a.mac = v
	},
	"--vcpu": func(a *cliArgs, v string) { a.vcpu = v },
	"--mem":  func(a *cliArgs, v string) { a.mem = v },
	"--mmds": func(a *cliArgs, v string) { a.mmds = v },
	"--image": func(a *cliArgs, v string) {
		a.image = v
	},
	"--pool":        func(a *cliArgs, v string) { a.pool = v },
	"--snapshotter": func(a *cliArgs, v string) { a.snapshotter = v },
	"--socket":      func(a *cliArgs, v string) { a.socket = v },
}

var subcommands = map[string]bool{"doctor": true, "launch": true, "status": true, "stop": true, "image": true}

func parseArgs(args []string) (cliArgs, error) {
	out := cliArgs{sub: "doctor"}

	for i := 0; i < len(args); i++ {
		arg := args[i]

		switch {
		case arg == "--json":
			out.asJSON = true
		case arg == "--push":
			out.push = true
		case subcommands[arg]:
			out.sub = arg
		case out.sub == "image" && !strings.HasPrefix(arg, "-"):
			// `image build <box>` — the ONE subcommand that takes a positional: every other subcommand
			// keeps rejecting stray arguments.
			out.positional = append(out.positional, arg)
		case valueFlags[arg] != nil:
			if i+1 >= len(args) {
				return out, fmt.Errorf("%s needs a value", arg)
			}

			i++
			valueFlags[arg](&out, args[i])
		case strings.HasPrefix(arg, "-"):
			return out, fmt.Errorf("unknown flag %q", arg)
		default:
			return out, fmt.Errorf("unknown argument %q (subcommands: doctor, launch, status, stop, image)", arg)
		}
	}

	return out, nil
}

func runDoctorCLI(a cliArgs) int {
	rep := runDoctor(doctorRequest{
		Binary:           a.binary,
		Kernel:           a.kernel,
		Rootfs:           a.rootfs,
		Snapshotter:      a.snapshotter,
		ContainerdSocket: a.socket,
	})

	if a.asJSON {
		return printJSON(rep)
	}

	fmt.Println(rep.summary())

	if len(rep.Problems) > 0 {
		return 1
	}

	return 0
}

// runLaunchCLI boots ONE microVM: it starts the VMM, writes the whole lease into it over the API, starts
// the instance, and records the lease so a later process can find it.
func runLaunchCLI(a cliArgs) int {
	if a.name == "" {
		fmt.Fprintln(os.Stderr, "firecracker launch: --name is required (the per-lease microVM name)")

		return 2
	}

	if a.kernel == "" || a.rootfs == "" {
		fmt.Fprintln(os.Stderr, "firecracker launch: --kernel and --rootfs are required (the VMM boots both)")

		return 2
	}

	binary := a.binary
	if binary == "" {
		found, err := lookPathVMM()
		if err != nil {
			fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

			return 1
		}

		binary = found
	}

	vcpu, err := optionalInt("--vcpu", a.vcpu)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 2
	}

	mem, err := optionalInt("--mem", a.mem)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 2
	}

	mmds, err := parseMMDS(a.mmds, a.name, a.pool)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 2
	}

	lease := vmmLease{
		Name: a.name, KernelPath: a.kernel, RootDrive: a.rootfs,
		GuestIP: a.guestIP, TAPIP: a.tapIP, TAP: a.tap, GuestMAC: a.mac,
		VCPU: vcpu, MemMiB: mem, MMDS: mmds,
	}

	runDir, err := leaseRunDir(a.name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 1
	}

	ctx := context.Background()

	cmd, version, err := startVMM(ctx, binary, lease, runDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 1
	}

	client := newVMMClient(lease.socketPath(runDir))

	if err := client.configure(ctx, lease); err != nil {
		stopVMM(cmd)

		fmt.Fprintf(os.Stderr, "firecracker launch: configure: %v\n", err)

		return 1
	}

	if err := client.start(ctx); err != nil {
		stopVMM(cmd)

		fmt.Fprintf(os.Stderr, "firecracker launch: start: %v\n", err)

		return 1
	}

	// An ACCEPTED action is not a live instance. The VMM exits when its guest dies (a kernel that finds
	// no init panics and the process goes with it), so the minutes after InstanceStart are exactly when
	// a false "up" is most likely. The liveness gate is the honest minimum; GUEST readiness is a
	// check's job, and the message says where to look when it fails.
	if err := awaitVMMAlive(ctx, client, lease.serialLogPath(runDir)); err != nil {
		stopVMM(cmd)

		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 1
	}

	st := spec.FirecrackerDeployState{
		Host: "local", Name: a.name,
		APISock:   lease.socketPath(runDir),
		SerialLog: lease.serialLogPath(runDir),
		TAP:       a.tap,
		GuestIP:   a.guestIP,
		GuestMAC:  a.mac,
		ImageRef:  a.image,
		KernelRef: a.kernel,
		Pool:      a.pool,
	}

	path, err := SaveLease(st)
	if err != nil {
		stopVMM(cmd)

		fmt.Fprintf(os.Stderr, "firecracker launch: %v\n", err)

		return 1
	}

	if a.asJSON {
		return printJSON(map[string]any{"lease": st, "record": path, "vmm_version": version})
	}

	fmt.Printf("firecracker launch: lease %s up (vmm %s, api %s, serial %s, record %s)\n",
		a.name, version, st.APISock, st.SerialLog, path)

	return 0
}

// runStatusCLI reports a lease from its record, asking the API whether the VMM is still there.
func runStatusCLI(a cliArgs) int {
	if a.name == "" {
		fmt.Fprintln(os.Stderr, "firecracker status: --name is required")

		return 2
	}

	st, err := LoadLease(a.name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker status: %v\n", err)

		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), vmmCallTimeout)
	defer cancel()

	version, apiErr := newVMMClient(st.APISock).version(ctx)

	live := apiErr == nil

	if a.asJSON {
		return printJSON(map[string]any{"lease": st, "live": live, "vmm_version": version})
	}

	state := "down"
	if live {
		state = "up"
	}

	fmt.Printf("firecracker status: lease %s %s (api %s, serial %s)\n", st.Name, state, st.APISock, st.SerialLog)

	if !live {
		fmt.Fprintf(os.Stderr, "firecracker status: the API socket did not answer: %v\n", apiErr)

		return 1
	}

	return 0
}

// runStopCLI powers the guest down through the API and waits for the VMM to go away. Firecracker exits
// when its guest powers off, so the API call IS the teardown; the wait is what makes the claim honest
// rather than assumed.
func runStopCLI(a cliArgs) int {
	if a.name == "" {
		fmt.Fprintln(os.Stderr, "firecracker stop: --name is required")

		return 2
	}

	st, err := LoadLease(a.name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "firecracker stop: %v\n", err)

		return 1
	}

	ctx := context.Background()
	client := newVMMClient(st.APISock)

	if err := client.shutdown(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "firecracker stop: %v\n", err)

		return 1
	}

	deadline := time.Now().Add(vmmCallTimeout)
	for {
		if _, err := client.version(ctx); err != nil {
			break // the VMM is gone: the guest powered off, as asked
		}

		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr,
				"firecracker stop: the guest did not power off within %s; the VMM is still serving %s\n",
				vmmCallTimeout, st.APISock)

			return 1
		}

		time.Sleep(100 * time.Millisecond)
	}

	if err := RemoveLease(a.name); err != nil {
		fmt.Fprintf(os.Stderr, "firecracker stop: %v\n", err)

		return 1
	}

	fmt.Printf("firecracker stop: lease %s down\n", a.name)

	return 0
}

func printJSON(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	if err := enc.Encode(v); err != nil {
		fmt.Fprintf(os.Stderr, "firecracker: encode: %v\n", err)

		return 1
	}

	return 0
}

func optionalInt(flag, raw string) (int, error) {
	if raw == "" {
		return 0, nil
	}

	return parseInt(flag, raw)
}

// parseMMDS builds the metadata tree the guest reads. `fireactions.runner_jit_config` is the key the
// fireactions agent registers from — a lease launched for a pool carries it; a bare lease carries
// charly's own lease identity so a guest can always tell what it is.
func parseMMDS(raw, lease, pool string) (map[string]any, error) {
	tree := map[string]any{}

	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &tree); err != nil {
			return nil, fmt.Errorf("--mmds is not valid JSON: %w", err)
		}
	}

	charly, ok := tree["charly"].(map[string]any)
	if !ok {
		charly = map[string]any{}
		tree["charly"] = charly
	}

	charly["lease"] = lease

	if pool != "" {
		charly["pool"] = pool
	}

	return tree, nil
}

// stopVMM ends a VMM THIS process started: the failure path of launch (a refused document, a failed
// start). A half-configured VMM left running would hold an API socket and a TAP that nothing owns.
func stopVMM(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
