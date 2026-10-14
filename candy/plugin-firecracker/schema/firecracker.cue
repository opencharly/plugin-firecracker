// plugin-firecracker's OWN self-contained CUE schema — the SINGLE SOURCE for this plugin's served
// declaration surface (there is no schema-less plugin: every plugin ships a non-empty schema over
// Describe) AND for the generated Go params of its check verb.
//
// SELF-CONTAINED and PACKAGE-LESS: it references no base def and carries no package clause, so it
// compiles STANDALONE (the serve-side compile) and splices onto the base at the load gate
// (base ++ plugin) — a self-contained schema that will not splice is a LOUD load failure.
//
// TWO CONSUMERS, like every verb plugin's schema:
//  1. GENERATE the Go param struct — `charly candy params plugin-firecracker` concatenates this
//     file under a `package params` header and writes ../params/cue_types_gen.go, so the provider
//     decodes plugin_input into a TYPED struct rather than a hand-parsed map.
//  2. VALIDATE authored input AT RUNTIME — an authored `firecracker:` step's plugin_input is
//     validated against #FirecrackerInput before the provider is invoked.
//
// The `contract:` fields document the words this plugin serves, which is what a reader (and the
// public docs surface) reads to learn the verb's shape without opening Go.

// #FirecrackerPlugin documents the THREE words this plugin serves and what each one is for.
#FirecrackerPlugin: {
	// kind — the substrate word. The STRUCTURAL half (the word itself, its declared traits and its
	// status row) is served by candy/plugin-substrate, which owns the substrate kinds; this plugin
	// owns the BEHAVIOUR behind the word.
	kind: "firecracker"

	// verb — the check verb an authored plan step uses: `firecracker:` + plugin_input.
	verb: "firecracker"

	// command — the CLI family: `charly firecracker <subcommand>`.
	command: "firecracker"

	// contract — one line per served word, in the public-docs voice.
	contract!: string & !=""
}

// #FirecrackerInput is the typed plugin_input of the `firecracker:` check verb (and, through the
// CLI, of `charly firecracker`).
//
// THE FIRST SLICE IS READ-ONLY ON PURPOSE. A microVM is booted by the substrate's own lifecycle
// (the launch leg), and a check verb that claimed to boot one before that leg existed would be
// exactly the kind of thing this ledger refuses: a capability that reports success without a
// guest behind it. `doctor` therefore answers what THIS HOST can do — the question every bed and
// every operator asks first — and says so in its report.
#FirecrackerInput: {
	// method selects what the verb does. Only the read-only host probe exists today.
	method: *"doctor" | "doctor-json" @go(Method)

	// binary is the VMM binary to inspect; empty means the one on PATH. A path that is present but
	// not executable is a PROBLEM the report names, not a silent pass.
	binary?: string @go(Binary)

	// kernel is the kernel artefact to check for (a host path, or empty for the configured
	// default). The kernel is a SEPARATE file from the root drive: the VMM boots both.
	kernel?: string @go(Kernel)

	// rootfs is the root-drive artefact to check for (a host path, or empty to skip).
	rootfs?: string @go(Rootfs)

	// snapshotter is the containerd snapshotter a pool would use. Only the two that can back a root
	// drive are legal — `devmapper` (an LVM thin pool) and `blockfile` (a file per snapshot, which
	// needs neither LVM nor a spare device); anything else can never work, because the VMM attaches
	// a block device or a FILE.
	snapshotter?: *"devmapper" | "blockfile" | "" @go(Snapshotter)

	// containerd_socket is the daemon socket to report on; empty means the engine default.
	containerd_socket?: string @go(ContainerdSocket)
}
