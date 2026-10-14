package firecracker

// cli.go — the `charly firecracker` CLI family.
//
// The command is the operator's half of the same capability the check verb serves: `doctor` reports
// what this host can do. It prints the report as a single line by default and as JSON on `--json`,
// so a person reads it and a script can parse it.
//
// The in-proc path (Invoke → runCommand) and the fork/exec path (CliMain → runCommand) run the SAME
// function, so the two placements cannot drift.

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// CliMain is the out-of-process CLI entry: charly fork/execs this binary with the pass-through
// tokens after `charly firecracker`.
func CliMain(args []string) int { return runCommand(args) }

// runCommand is the command's ONE effect, shared by both placements. It returns a process exit code:
// a doctor that found problems exits non-zero, because a caller who scripted it must see the failure
// rather than parse prose for it.
func runCommand(args []string) int {
	sub := "doctor"
	asJSON := false
	req := doctorRequest{}

	for i := 0; i < len(args); i++ {
		switch arg := args[i]; {
		case arg == "--json":
			asJSON = true
		case arg == "doctor":
			sub = "doctor"
		case arg == "--binary":
			if i+1 < len(args) {
				i++
				req.Binary = args[i]
			}
		case arg == "--kernel":
			if i+1 < len(args) {
				i++
				req.Kernel = args[i]
			}
		case arg == "--rootfs":
			if i+1 < len(args) {
				i++
				req.Rootfs = args[i]
			}
		case arg == "--snapshotter":
			if i+1 < len(args) {
				i++
				req.Snapshotter = args[i]
			}
		case arg == "--containerd-socket":
			if i+1 < len(args) {
				i++
				req.ContainerdSocket = args[i]
			}
		case strings.HasPrefix(arg, "-"):
			fmt.Fprintf(os.Stderr, "firecracker: unknown flag %q\n", arg)

			return 2
		default:
			fmt.Fprintf(os.Stderr, "firecracker: unknown subcommand %q (only \"doctor\")\n", arg)

			return 2
		}
	}

	if sub != "doctor" {
		fmt.Fprintf(os.Stderr, "firecracker: unknown subcommand %q (only \"doctor\")\n", sub)

		return 2
	}

	rep := runDoctor(req)

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")

		if err := enc.Encode(rep); err != nil {
			fmt.Fprintf(os.Stderr, "firecracker: encode report: %v\n", err)

			return 1
		}
	} else {
		fmt.Println(rep.summary())
	}

	if len(rep.Problems) > 0 {
		return 1
	}

	return 0
}
