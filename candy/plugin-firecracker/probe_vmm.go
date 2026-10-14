package firecracker

// probe_vmm.go — the `vmm` method: a self-test of the API client against the REAL VMM binary.
//
// It starts the VMM on a scratch API socket with the caller's kernel, writes a whole lease into it,
// reads the decisive documents back, and stops it. Nothing is booted: the drive is configured and the
// instance is NOT started, because a real boot needs a rootfs artefact and that is the producer leg's
// job (ledger WI-7). What this proves is the claim that matters here — the client speaks the VMM's own
// protocol, verified by the VMM's own answers.

import (
	"context"
	"os"
	"path/filepath"

	"github.com/opencharly/sdk/kit"

	"github.com/opencharly/plugin-firecracker/candy/plugin-firecracker/params"
)

func runVMMProbe(in params.FirecrackerInput) kit.Result {
	binary := in.Binary
	if binary == "" {
		found, err := lookPathVMM()
		if err != nil {
			return kit.Failf("vmm probe: %v", err)
		}

		binary = found
	}

	if in.Kernel == "" {
		return kit.Fail("vmm probe: kernel is required (the VMM will not start without one)")
	}

	if in.Rootfs == "" {
		return kit.Fail("vmm probe: rootfs is required (the drive is part of the lease)")
	}

	runDir, err := os.MkdirTemp("", "charly-fc-probe-")
	if err != nil {
		return kit.Failf("vmm probe: scratch dir: %v", err)
	}

	defer os.RemoveAll(runDir) //nolint:errcheck // scratch state of a probe

	lease := vmmLease{
		Name:       "probe",
		KernelPath: in.Kernel,
		RootDrive:  in.Rootfs,
		GuestIP:    in.GuestIP,
		TAPIP:      in.TAPIP,
		GuestMAC:   in.GuestMAC,
		TAP:        in.TAPName,
		MMDS:       map[string]any{"charly": map[string]any{"probe": "vmm"}},
	}

	ctx := context.Background()

	cmd, version, err := startVMM(ctx, binary, lease, runDir)
	if err != nil {
		return kit.Failf("vmm probe: %v", err)
	}

	defer stopVMM(cmd)

	client := newVMMClient(filepath.Join(runDir, "firecracker.sock"))

	if err := client.configure(ctx, lease); err != nil {
		return kit.Failf("vmm probe: the VMM refused a document: %v", err)
	}

	return kit.Passf("vmm probe: %s accepted the whole lease (boot source, drive, machine config, mmds) and read it back", version)
}
