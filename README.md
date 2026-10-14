# plugin-firecracker

The charly plugin for the **Firecracker microVM substrate** — a guest booted directly under KVM from
a **root drive artefact plus a separate kernel**. No libvirt, no domain, no host arbiter lease.

```console
$ charly firecracker doctor
firecracker doctor: kvm=yes vmm=Firecracker v1.17.0 snapshotter=devmapper(root-drive-capable) containerd=reachable forward=1
```

`doctor` answers the question every operator and every bed asks first: **what can this host actually
do?** It reports

- `/dev/kvm` and the mode it is exposed with,
- the VMM binary and **the version it reports** (a binary that cannot answer is not a VMM),
- the kernel and root-drive artefacts you name,
- the containerd socket, and whether the **configured snapshotter can back a root drive at all** —
  the VMM is handed a block device (`devmapper`) or a file (`blockfile`), never a directory tree, so
  a directory-mounting snapshotter can never work,
- the host's forwarding posture.

Every missing precondition is reported as a **problem** and makes the command exit non-zero, so a
script sees the failure instead of parsing prose. `--json` prints the same report for machines.

## Status

**Read-only today, on purpose.** The launch leg — boot-source, drives, machine-config, NIC, vsock,
MMDS, serial, rate limiters, snapshots, per-lease artefact materialisation and `firecracker_state` —
plus the R10 bed that boots a real guest and runs an in-guest plan, are the next slices. The verb
reports what exists rather than claiming a capability the substrate cannot yet honour.

The structural half of the substrate (`kind:firecracker`, its declared traits and its status row) is
served by [opencharly/plugin-substrate](https://github.com/opencharly/plugin-substrate); this repo
owns the behaviour behind the word.

## Using it

An authored check step:

```yaml
plan:
    - check: this host can boot a microVM
      firecracker:
          method: doctor
```

and the CLI:

```console
$ charly firecracker doctor --snapshotter blockfile --json
```
