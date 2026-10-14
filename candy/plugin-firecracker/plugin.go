// Package firecracker is the charly plugin for the `firecracker` microVM substrate.
//
// A microVM is booted directly under KVM from a ROOT DRIVE artefact plus a SEPARATE kernel: no
// libvirt, no domain, no host arbiter lease. Its STRUCTURAL half — the `kind:firecracker` word, its
// declared traits and its status row — is served by candy/plugin-substrate, which owns the
// substrate kinds and declares venue "ssh" WITHOUT an exclusive venue (the absence that keeps the
// kernel from misrouting a deploy into `charly vm build`/`charly vm create`). This plugin owns the
// BEHAVIOUR behind the word: `verb:firecracker` for an authored check step and `command:firecracker`
// for the CLI family.
//
// THIS SLICE IS READ-ONLY, deliberately. `firecracker: {method: doctor}` reports what the target
// host can actually do — KVM and its mode, the VMM binary and the version it reports, the kernel and
// root-drive artefacts, the containerd socket and whether the configured snapshotter can back a root
// drive at all, and the forwarding posture — naming every missing precondition as a problem. The
// launch leg (boot-source, drives, machine-config, NIC, vsock, MMDS, serial, rate limiters,
// snapshots, `firecracker_state`) and the R10 bed that boots a real guest follow; a verb must not
// claim a capability the substrate cannot yet honour.
package firecracker

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/opencharly/sdk"
	"github.com/opencharly/sdk/kit"
	pb "github.com/opencharly/spec/proto"
	"github.com/opencharly/spec/spec"

	"github.com/opencharly/plugin-firecracker/candy/plugin-firecracker/params"
)

//go:embed schema/*.cue
var schemaFS embed.FS

// calver is this plugin's declaration stamp, reported over Describe like every plugin's.
const calver = "2026.287.0001"

// NewProvider returns the provider for the gRPC (out-of-process) placement.
func NewProvider() pb.ProviderServer { return &provider{} }

// NewCheckVerb returns the check-verb provider — the SAME type NewProvider returns, deliberately.
//
// The SDK hands the provider to its server and asks it whether it also implements
// kit.CheckVerbProvider: if it does, `firecracker:` steps are routed to RunVerb; if it does NOT, they
// fall through to Invoke, which serves the COMMAND class and runs the CLI. That is exactly what
// happened here, and it failed invisibly: every `firecracker:` check step in every bed executed the
// command path instead of the verb and reported `firecracker: command exited 1`, so no check of this
// plugin ever ran until the venue-aware probe made the failure visible.
func NewCheckVerb() kit.CheckVerbProvider { return &provider{} }

// NewMeta advertises the TWO words this plugin serves, each with this plugin's OWN self-contained
// CUE schema (there is no schema-less plugin: the schema is served over Describe, spliced onto the
// base at the load gate, and used to validate every authored `firecracker:` step before the
// provider is invoked). `kind:firecracker` is deliberately NOT claimed here — the structural kind
// belongs to candy/plugin-substrate, and a second claimant would be the duplication R2 forbids.
func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver,
		[]sdk.ProvidedCapability{
			{Class: "verb", Word: "firecracker", InputDef: "#FirecrackerInput", Primary: "method"},
			{Class: "command", Word: "firecracker"},
		},
		schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

// Invoke serves BOTH classes this plugin declares, and it MUST dispatch on the class.
//
// An earlier version never looked at req.GetClass() and ran the command effect unconditionally. Every
// `firecracker:` CHECK step therefore ran the CLI, exited 1, and was reported as
// `firecracker: command exited 1` — with the check verb never invoked at all. That is the defect that
// made this plugin's green meaningless: no check of it had ever executed.
func (p *provider) Invoke(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	switch req.GetClass() {
	case classCommand:
		return invokeCommand(req)
	case classVerb:
		return p.invokeVerb(ctx, req)
	default:
		return nil, fmt.Errorf("firecracker: unsupported class %q", req.GetClass())
	}
}

// classCommand / classVerb are the two provider classes this plugin declares.
const (
	classCommand = "command"
	classVerb    = "verb"
)

// invokeCommand serves the COMPILED-IN (in-proc) command dispatch: decode the pass-through `{args}`
// and run the command effect in charly's own process. Out-of-process command dispatch fork/execs the
// binary (CliMain instead), never this gRPC path.
func invokeCommand(req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	if req.GetOp() != sdk.OpRun {
		return nil, fmt.Errorf("firecracker: unsupported op %q (only %q)", req.GetOp(), sdk.OpRun)
	}

	var in struct {
		Args []string `json:"args"`
	}

	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &in); err != nil {
			return nil, fmt.Errorf("firecracker: decode args: %w", err)
		}
	}

	if code := runCommand(in.Args); code != 0 {
		return nil, fmt.Errorf("firecracker: command exited %d", code)
	}

	return &pb.InvokeReply{}, nil
}

// invokeVerb runs one `firecracker:` check operation OUT OF PROCESS: the same core RunVerb executes,
// with the CheckContext reconstructed from the host's reverse channel — sdk.NewCheckContext carries
// the venue executor (ExecutorService), which is exactly what the venue-aware probe drives. Placement
// is invisible to the verb (R2: one implementation, two placements).
func (p *provider) invokeVerb(ctx context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
	var op spec.Op

	if len(req.GetParamsJson()) > 0 {
		if err := json.Unmarshal(req.GetParamsJson(), &op); err != nil {
			return sdk.ResultJSON("fail", "firecracker: decode op: "+err.Error())
		}
	}

	var in params.FirecrackerInput

	kit.DecodeInput(op.PluginInput, &in)

	cc, err := sdk.NewCheckContext(req.GetExecutorBrokerId(), req.GetEnvJson())
	if err != nil {
		return sdk.ResultJSON("fail", fmt.Sprintf("firecracker: %s: no venue context: %v", in.Method, err))
	}

	res := p.RunVerb(ctx, cc, &op)

	return sdk.ResultJSON(statusWord(res.Status), res.Message)
}

// statusWord renders the typed status as the wire word the host grades.
func statusWord(s spec.Status) string {
	switch s {
	case spec.StatusPass:
		return "pass"
	case spec.StatusSkip:
		return "skip"
	default:
		return "fail"
	}
}

// Reserved is the word this verb answers to. It is on the SAME type as Invoke so one object serves
// both classes (the shape every two-class plugin in the org uses).
func (p *provider) Reserved() string { return "firecracker" }

// RunVerb runs an authored `firecracker:` check step. `doctor` reports what this host can do and
// FAILS on any missing precondition; `vmm` proves the API client against the real VMM binary. Neither
// method claims a guest was booted: the boot needs a rootfs from the producer leg (ledger WI-7), and a
// verb that reported one before that existed would be a capability with nothing behind it.
func (p *provider) RunVerb(ctx context.Context, cc kit.CheckContext, op *spec.Op) kit.Result {
	var in params.FirecrackerInput

	kit.DecodeInput(op.PluginInput, &in)

	if in.Method == "vmm" {
		return runVMMProbe(ctx, cc, in)
	}

	rep := runDoctor(doctorRequest{
		Binary:           in.Binary,
		Kernel:           in.Kernel,
		Rootfs:           in.Rootfs,
		Snapshotter:      in.Snapshotter,
		ContainerdSocket: in.ContainerdSocket,
	})

	if len(rep.Problems) > 0 {
		return kit.Failf("%s (mode=%s)", rep.summary(), cc.Mode())
	}

	return kit.Passf("%s (mode=%s)", rep.summary(), cc.Mode())
}
