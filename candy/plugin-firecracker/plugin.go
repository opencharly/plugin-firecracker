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

// NewCheckVerb returns the check-verb provider. The `firecracker:` verb dispatches IN-PROCESS via
// RunVerb, which is what keeps the live runner — the run mode and the deploy context an
// out-of-process Invoke cannot see.
func NewCheckVerb() kit.CheckVerbProvider { return verb{} }

// NewMeta advertises the TWO words this plugin serves, each with this plugin's OWN self-contained
// CUE schema (there is no schema-less plugin: the schema is served over Describe, spliced onto the
// base at the load gate, and used to validate every authored `firecracker:` step before the
// provider is invoked). `kind:firecracker` is deliberately NOT claimed here — the structural kind
// belongs to candy/plugin-substrate, and a second claimant would be the duplication R2 forbids.
func NewMeta() pb.PluginMetaServer {
	return sdk.NewMeta(calver,
		[]sdk.ProvidedCapability{
			{Class: "verb", Word: "firecracker", InputDef: "#FirecrackerInput"},
			{Class: "command", Word: "firecracker"},
		},
		schemaFS)
}

type provider struct{ pb.UnimplementedProviderServer }

// Invoke serves the COMPILED-IN (in-proc) command dispatch: decode the pass-through `{args}` and run
// the command effect in charly's own process. Out-of-process dispatch fork/execs the binary
// (CliMain instead), never this gRPC path.
func (provider) Invoke(_ context.Context, req *pb.InvokeRequest) (*pb.InvokeReply, error) {
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

type verb struct{}

// Reserved is the word this verb answers to.
func (verb) Reserved() string { return "firecracker" }

// RunVerb runs an authored `firecracker:` check step. Today it has exactly one method, and it is
// read-only: `doctor` reports what this host can do, and ANY missing precondition makes the step
// FAIL with the reason, because a bed that skipped the probe would report a green host it never
// checked.
func (verb) RunVerb(_ context.Context, cc kit.CheckContext, op *spec.Op) kit.Result {
	var in params.FirecrackerInput

	kit.DecodeInput(op.PluginInput, &in)

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
