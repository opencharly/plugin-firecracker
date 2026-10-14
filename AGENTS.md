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
- **A verb must not outrun the substrate.** The launch leg does not exist yet, so the verb is
  read-only and says so. A method that reported a booted guest before a guest could be booted would
  be the fake fix this project refuses.
- Keep the probe's verdicts FAILING when a precondition is missing: `Problems` non-empty fails the
  step, so a bed can never pass on a host it did not check.

## Landing

- PR-only; the org-required `charly/pr-validator` arms native auto-merge on PASS, and
  `tag-on-merge` mints the CalVer tag + writes `CHANGELOG/<CalVer>.md` from the PR body. Direct
  pushes to `main` are blocked.
- The authoritative rulebook is the umbrella `AGENTS.md` in `opencharly/opencharly`. Do not restate
  its rules here.
