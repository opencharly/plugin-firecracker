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

**What exists today.** `doctor` reports what a host can do. `launch` starts the VMM, writes a WHOLE
lease into it over the API — boot source, drive, machine config, network interface, MMDS v2 tree — and
starts the instance, gate-checking that the VMM survived the first moments; `status` reports a lease
from its record by asking the API whether it is still there; `stop` powers the guest down and waits for
the VMM to exit. The `vmm` check method proves the same client against a REAL VMM binary inside the R10
bed, which runs in a disposable guest.

**What does not exist yet, on purpose:** a **booted guest**. That needs a root drive that is a real
filesystem — a producer, filed as opencharly/plugin-firecracker#1 — and until it lands, `launch`
requires a rootfs you supply and nothing claims a guest came up. A verb that reported a boot before a
boot could happen is the fake fix this project refuses.

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
