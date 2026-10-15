# AGENTS.md — plugin-firecracker

Standalone plugin repo for the **`firecracker` microVM substrate**: a guest booted directly under
KVM from a **root drive artefact plus a separate kernel** — no libvirt, no domain, no host arbiter
lease.

## Where the substrate is split, and why

- **The structural half lives in `plugin-substrate`.** The `kind:firecracker` word, its declared
  `#DeployTraits` and its status fan-out row are served by `candy/plugin-substrate`, which owns the
  substrate kinds. Its row is `{Venue: "ssh", ImageBacked, BedTarget, SupportsEphemeral}` with **no
  `ExclusiveVenue`** — the absence that keeps the kernel from misrouting a deploy into
  `charly vm build` / `charly vm create`, because `IsVmVenue` is `ssh ∧ exclusive_venue`.
- **The behaviour lives here.** `verb:firecracker` (an authored plan step) and `command:firecracker`
  (`charly firecracker …`). This plugin must NOT claim `kind:firecracker` as well: two claimants for
  one word is the duplication R2 forbids.

## Canonical files

- `candy/plugin-firecracker/charly.yml` — the `plugin-firecracker:` candy entity (`plugin:` block,
  `plan:`).
- `candy/plugin-firecracker/plugin.go` — `NewProvider`/`NewMeta`/`NewCheckVerb`.
- `candy/plugin-firecracker/doctor.go` — the read-only host probe.
- `candy/plugin-firecracker/cli.go` — the `charly firecracker` command.
- `candy/plugin-firecracker/schema/firecracker.cue` — the self-contained served schema.
- `candy/plugin-firecracker/params/cue_types_gen.go` — **generated**; never hand-edit.
- `charly.yml` — the project manifest + the `check-firecracker-host` witness bed.
- `.github/workflows/tag-on-merge.yml` — CalVer tag + `CHANGELOG/` on merge.

## Load these skills first (R0)

- `/charly-internals:plugin` — the `plugin:` block, the Provider model, the per-plugin CUE-schema
  contract, placement. Load before touching the provider or the schema.
- `/charly-check:check` — the bed / R10 run sequence, before running or authoring a bed.
- `/charly-internals:go` — before Go source work; `/charly-internals:git-workflow` before any
  git/PR action.
- `/charly-crabbox:crabbox-deploy` and `/charly-internals:vm-deploy-target` — before the launch and
  lifecycle legs (the guest SSH executor and the venue Ops).

## Build / validate / test

- `go build ./...` and `go test ./...` in `candy/plugin-firecracker/`.
- `charly candy params plugin-firecracker` — regenerates `params/cue_types_gen.go` from
  `schema/firecracker.cue`. This is the ONLY supported way to regenerate it.
- `charly box validate` at the repo root — the structural check (the candy, the `plugin:` block, the
  CUE schema, the witness bed).
- The merge gate is the **org-wide** `charly/pr-validator` (required check `validate / validate`).
  There is no per-repo candy gate.
- R10 witness: `charly check run check-firecracker-host` (`disposable: true`).

## Modify this repo

- **Schema first (SDD).** Change `schema/firecracker.cue`, then regenerate the params — never hand-
  transcribe a struct, and never hand-edit the generated file.
- **One provider must serve BOTH classes.** This plugin declares `verb:firecracker` and
  `command:firecracker`, and both must come from the SAME type (the reference is `plugin-qdrant`):
  the SDK asks the provider it is handed whether it also implements `kit.CheckVerbProvider`, and
  `Invoke` must dispatch on `req.GetClass()`. Get either wrong and every `firecracker:` check step
  silently runs the CLI instead of the verb — reporting `firecracker: command exited 1` while no check
  ever executes. `plugin_test.go` gates both halves; do not remove them.
- **A check runs where its subject lives.** `RunVerb` is dispatched in charly's process, so a probe
  that shells out locally probes the wrong machine: drive the step's venue through `cc.Exec()`, and
  never assert a read-back an API does not serve — Firecracker answers `GET /` and
  `GET /machine-config` and returns `400 Invalid request method and/or path` for `/boot-source`,
  `/drives/<id>` and `/network-interfaces/<id>` (measured on v1.17).
- **A verb must not outrun the substrate.** `doctor` is a read-only probe; `vmm` proves the API client
  by driving the REAL VMM binary (start, configure, read back, stop). Neither claims a booted guest. A
  method that reported a boot before a boot could happen would be the fake fix this project refuses, and
  `launch`'s liveness gate exists for the same reason: an accepted action is not a live instance.
- **The producer exists, and its artefact has been booted.** `charly firecracker image build <box>` builds
  a box, extracts its image tree with ownership fidelity, formats a raw ext4 root drive and emits a
  microVM box image carrying the drive and the kernel. Measured end to end: a real box → a 426 MiB raw
  ext4 → the VMM starting it, the ext4 mounting as `vda`, and `systemd` coming up as PID 1. It runs as an
  ordinary user: the build and the engine reads use the invoking user's engine (whose store holds the
  image and whose network resolves), and **everything that touches the extracted tree is elevated**
  through one explicit `sudo -n` — the untar that buys the ownership, the `du -sb` measurement, the
  cleanup of a previous run's tree, and `mke2fs`, which must read root-only files such as `/etc/gshadow`.
  The disposable-target bed EXISTS and is RED, and saying so is part of the contract. `check-firecracker-
  producer-vm` is the disposable GUEST (a VM, because a pod cannot host an engine and the nesting candy's
  builder stage needs a precondition no consumer can supply) whose step is meant to assert the emitted microVM
  box image at its boundary. It does not pass, and the reason is outside this repository: its step reads
  `[producer-artefact-in-guest] exit=2 want 0 (stderr: plugin env: unset)` — a deploy provides the guest charly
  and NO candy corpus, so a plugin verb there has no plugin to reach (opencharly/charly#916, reproduced by this
  bed). The producer path itself therefore never executed live in this bed, and no bed of this repo is green.
  Do not read this paragraph as a shipped proof: R10 is OWED for the producer path until that capability lands,
  and opencharly/plugin-firecracker#5 is scoped to the POD bed alone, whose image cannot build at all.

  The boot, executed (a VMM driven over its API socket; no TAP, no root, no network needed):

  ```
  $ firecracker-v1.17.0-x86_64 --api-sock …   (PUT /boot-source, /drives/rootfs, /machine-config → 204)
  $ curl … -d '{"action_type":"InstanceStart"}' /actions                    → 204
  [    0.000000] Linux version 6.1.155+ …
  [    2.184632] EXT4-fs (vda): mounted filesystem with ordered data mode. Quota mode: none.
  [    3.094703] systemd[1]: systemd 258.11-1.fc43 running in system mode (+PAM +AUDIT +SELINUX …)
  [    3.124802] systemd[1]: Detected virtualization kvm.
  Welcome to Fedora Linux 43 (Container Image)!
  [    3.158291] systemd[1]: Hostname set to <fedora>.
  ```

  This boot is also the CONTROL that catches a root drive with no userspace at all: before the subject
  box carried its `distro: [fedora]` tag, systemd was never installed, and this ends in `No working init`.
- Keep the probe's verdicts FAILING when a precondition is missing: `Problems` non-empty fails the
  step, so a bed can never pass on a host it did not check.

## Landing

- PR-only; the org-required `charly/pr-validator` arms native auto-merge on PASS, and
  `tag-on-merge` mints the CalVer tag + writes `CHANGELOG/<CalVer>.md` from the PR body. Direct
  pushes to `main` are blocked.
- The authoritative rulebook is the umbrella `AGENTS.md` in `opencharly/opencharly`. Do not restate
  its rules here.
