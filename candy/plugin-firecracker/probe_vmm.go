package firecracker

// probe_vmm.go — the `vmm` method, VENUE-AWARE.
//
// A check verb is dispatched inside CHARLY'S PROCESS, so a probe that shells out locally reaches the
// machine charly runs on — never the guest a `vm:` step points at, which is precisely where the VMM and
// the kernel were installed by the pinned layer the bed composes. Every command therefore goes through
// cc.Exec(), which runs on the STEP'S VENUE ("host", "container", "image" or "vm").
//
// The conversation is not invented: the integration's RDD drove the VMM's API exactly this way (spike
// P2 booted a real guest with TAP + NAT + SSH and read the fireactions agent's JIT config out of MMDS
// v2), and curl-over-the-unix-socket is the same conversation in shell form.
//
// Readiness is a PROBE, never a socket file — the lesson probe P2 recorded. Nothing is booted: the
// drive is configured and the instance is NOT started, because a real boot needs a root drive that is a
// real filesystem, and that producer is a separate leg (ledger WI-7). What this proves is what the check
// claims: the client speaks the VMM's own protocol, verified by the VMM's own answers.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/opencharly/sdk/kit"

	"github.com/opencharly/plugin-firecracker/candy/plugin-firecracker/params"
)

// Probe constants. The scratch state lives under /tmp in the VENUE, because it is a probe: nothing
// about it should survive the step.
const (
	probeRunDir    = "/tmp/charly-fc-probe"
	probeVCU       = 2
	probeMemMiB    = 2048
	probeMMDSPath  = "/mmds"
	probeSockName  = "firecracker.sock"
	probeAPIPrefix = "http://localhost"
)

type probe struct {
	binary string
	kernel string
	rootfs string
	tap    string
	mac    string
	sock   string
	runDir string
}

func newProbe(in params.FirecrackerInput) probe {
	p := probe{
		binary: in.Binary, kernel: in.Kernel, rootfs: in.Rootfs,
		tap: in.TAPName, mac: in.GuestMAC,
		runDir: probeRunDir,
	}

	p.sock = probeRunDir + "/" + probeSockName

	return p
}

func (p probe) curl(path string) string {
	return fmt.Sprintf("curl -fsS --unix-socket %s %s%s", sq(p.sock), probeAPIPrefix, path)
}

// runVMMProbe proves the API client against the REAL VMM binary ON THE STEP'S VENUE.
func runVMMProbe(ctx context.Context, cc kit.CheckContext, in params.FirecrackerInput) kit.Result {
	if cc == nil || cc.Exec() == nil {
		return kit.Fail("vmm probe: no venue executor — the probe must run where its subject lives")
	}

	if in.Kernel == "" {
		return kit.Fail("vmm probe: kernel is required (the VMM will not start without one)")
	}

	if in.Rootfs == "" {
		return kit.Fail("vmm probe: rootfs is required (the drive is part of the lease)")
	}

	venue := cc.Exec().Kind()
	p := newProbe(in)

	if out, errOut, code, err := cc.Exec().RunCapture(ctx, p.artefactScript()); err != nil || code != 0 {
		return kit.Failf("vmm probe on venue %s: %s%s", venue, venueWords(out, errOut), execNote(err))
	}

	if out, errOut, code, err := cc.Exec().RunCapture(ctx, p.startScript()); err != nil || code != 0 {
		return kit.Failf("vmm probe on venue %s: the VMM did not come up: %s%s", venue, venueWords(out, errOut), execNote(err))
	}

	// Teardown is asserted, not hoped for: the probe ends with the venue as it found it, and a teardown
	// that cannot confirm itself is reported rather than skipped.
	defer func() {
		_, _, _, _ = cc.Exec().RunCapture(ctx, p.stopScript()) //nolint:errcheck // best-effort teardown; the probe's verdict is already decided
	}()

	if out, errOut, code, err := cc.Exec().RunCapture(ctx, p.configureScript()); err != nil || code != 0 {
		return kit.Failf("vmm probe on venue %s: the VMM refused the lease: %s%s", venue, venueWords(out, errOut), execNote(err))
	}

	version, _, _, _ := cc.Exec().RunCapture(ctx, p.curl("/version"))

	return kit.Passf("vmm probe on venue %s: Firecracker %s accepted the whole lease (boot source, scratch root drive at %s, machine config%s) and served it back",
		venue, strings.TrimSpace(version), p.rootfs, p.mmdsWord())
}

// mmdsWord names the metadata half only when it is carried: a guest reads metadata THROUGH a network
// interface, so the probe never writes an MMDS tree without one.
func (p probe) mmdsWord() string {
	if p.tap == "" {
		return ""
	}

	return ", network interface, mmds v2 tree"
}

func execNote(err error) string {
	if err == nil {
		return ""
	}

	return " (" + err.Error() + ")"
}

// artefactScript asserts the probe's subjects exist IN THE VENUE. A missing artefact is a named
// failure, never an assumption: the pinned layer installs these, and if it did not, that IS the finding.
// An empty binary resolves through the venue's own PATH, because the VMM may live anywhere the layer
// put it.
func (p probe) artefactScript() string {
	bin := sq(p.binary)

	if p.binary == "" {
		bin = `"$(command -v firecracker || true)"`
	}

	return fmt.Sprintf(`set -eu
mkdir -p %s
B=%s
[ -n "$B" ] && [ -x "$B" ] || { echo "no executable VMM in this venue (looked for %s)"; exit 1; }
[ -f %s ] || { echo "no kernel at %s"; exit 1; }
# The root drive is the probe's own SCRATCH fixture, created here when it is absent, and MEASURED —
# never assumed. It has to be created here rather than by a run: step in the bed's plan because
# "charly check live" is verify-only by design and marks every mutating step "skipped — verify-only
# mode (mutating step)": a fixture declared as a plan step can therefore NEVER satisfy a check in that
# same plan (measured: the same SKIP in a run against a freshly created, empty target). What this check
# proves is the CLIENT's protocol conversation, for which any real filesystem will do; proving a real
# root drive BOOTS is the producer leg's job (opencharly/plugin-firecracker#1), and that check will
# assert a guest observable, not a socket.
R=%s
if [ ! -f "$R" ]; then
  mkdir -p "$(dirname "$R")"
  truncate -s 64M "$R"
  mke2fs -q -t ext4 -F "$R" >/dev/null 2>&1 || { echo "could not build the scratch root drive at $R"; exit 1; }
fi
[ -s "$R" ] || { echo "the root drive at $R is empty"; exit 1; }
blkid -o value -s TYPE "$R" 2>/dev/null | grep -q ext4 || { echo "the root drive at $R is not an ext4 filesystem, which the VMM will refuse"; exit 1; }
command -v curl >/dev/null 2>&1 || { echo "curl is required to speak the VMM's API socket in this venue"; exit 1; }
echo "$B" > %s/binary
echo ready`, p.runDir, bin, probeBinaryHint(p.binary), sq(p.kernel), sq(p.kernel), sq(p.rootfs), p.runDir)
}

func probeBinaryHint(binary string) string {
	if binary == "" {
		return "firecracker on PATH"
	}

	return binary
}

// startScript starts the VMM in the venue and waits for the API to ANSWER. A socket file is not a
// service.
func (p probe) startScript() string {
	return fmt.Sprintf(`set -eu
mkdir -p %s
rm -f %s
B=$(cat %s/binary)
nohup "$B" --api-sock %s --log-path %s/vmm.log --level Info > %s/serial.log 2>&1 &
echo $! > %s/vmm.pid
i=0
while [ "$i" -lt 100 ]; do
  if %s >/dev/null 2>&1; then echo "answered"; exit 0; fi
  i=$((i+1))
  sleep 0.1
done
echo "the VMM did not answer /version within 10s; last lines of %s/serial.log: $(tail -n 3 %s/serial.log | tr '\n' ' ')"
exit 1`, p.runDir, sq(p.sock), p.runDir, sq(p.sock), p.runDir, p.runDir, p.runDir, p.curl("/version"), p.runDir, p.runDir)
}

// configureScript writes the WHOLE lease over the API and READS BACK what this API actually SERVES.
//
// Measured against a real Firecracker v1.17 (the probe's own generated script, run locally): GET / and
// GET /machine-config answer, and GET /mmds returns the tree; GET /boot-source, GET /drives/<id> and
// GET /network-interfaces/<id> are 400 "Invalid request method and/or path". An earlier version asserted
// a boot-source read-back the API does not implement, so a perfectly healthy VMM failed every run.
//
// Every document is therefore proven by its ACCEPTED WRITE — put() prints the document, the HTTP code
// and the VMM's own fault body, and refuses anything that is not 204 — and the configuration by the
// document the VMM serves back, as a SUBSET, because the VMM adds fields of its own.
//
// The MMDS half is written ONLY when the lease carries a network interface: metadata is read THROUGH
// one, so a tree written without it is a configuration that can never work.
func (p probe) configureScript() string {
	var b strings.Builder

	b.WriteString("set -eu\n")
	fmt.Fprintf(&b, "SOCK=%s\n", sq(p.sock))
	b.WriteString(`BODY=/tmp/charly-fc-probe/put-body.json
mkdir -p /tmp/charly-fc-probe
put() {
  code=$(curl -sS --unix-socket "$SOCK" -H 'Content-Type: application/json' -X PUT -d "$2" -o "$BODY" -w '%{http_code}' "http://localhost$1")
  if [ "$code" != "204" ]; then echo "PUT $1 was refused: HTTP $code $(cat "$BODY" 2>/dev/null)"; exit 1; fi
}
get() { curl -fsS --unix-socket "$SOCK" "http://localhost$1"; }
`)
	fmt.Fprintf(&b, "put /boot-source %s\n", sq(jsonDoc(map[string]any{
		"kernel_image_path": p.kernel, "boot_args": probeBootArgs,
	})))
	fmt.Fprintf(&b, "put /drives/rootfs %s\n", sq(jsonDoc(map[string]any{
		"drive_id": "rootfs", "path_on_host": p.rootfs, "is_root_device": true, "is_read_only": false,
	})))
	fmt.Fprintf(&b, "put /machine-config %s\n", sq(jsonDoc(map[string]any{
		"vcpu_count": probeVCU, "mem_size_mib": probeMemMiB, "smt": false,
	})))

	if p.tap != "" {
		fmt.Fprintf(&b, "put /network-interfaces/eth0 %s\n", sq(jsonDoc(map[string]any{
			"iface_id": "eth0", "guest_mac": p.mac, "host_dev_name": p.tap,
		})))
		fmt.Fprintf(&b, "put /mmds/config %s\n", sq(jsonDoc(map[string]any{
			"network_interfaces": []string{"eth0"}, "version": "V2",
			"ipv4_address": probeMMDSAddress, "imds_compat": false,
		})))
		fmt.Fprintf(&b, "put %s %s\n", probeMMDSPath, sq(jsonDoc(map[string]any{
			"charly": map[string]any{"probe": "vmm"},
		})))
	}

	fmt.Fprintf(&b, `GOT=$(get /)
echo "$GOT" | grep -q '"state"' || { echo "GET / returned: $GOT"; exit 1; }
GOT=$(get /machine-config)
echo "$GOT" | grep -q %s || { echo "machine config read back as: $GOT"; exit 1; }
echo "$GOT" | grep -q %s || { echo "machine config read back as: $GOT"; exit 1; }
`,
		sq(fmt.Sprintf(`"vcpu_count":%d`, probeVCU)),
		sq(fmt.Sprintf(`"mem_size_mib":%d`, probeMemMiB)))

	if p.tap != "" {
		fmt.Fprintf(&b, `GOT=$(get %s)
echo "$GOT" | grep -q %s || { echo "the mmds tree read back as: $GOT"; exit 1; }
`, probeMMDSPath, sq(`"probe":"vmm"`))
	}

	b.WriteString("echo configured\n")

	return b.String()
}

// stopScript tears the probe's VMM down and ASSERTS it: the socket must stop answering. A probe that
// leaves a process and a socket behind has not finished.
func (p probe) stopScript() string {
	return fmt.Sprintf(`set -eu
if [ -f %s/vmm.pid ]; then kill "$(cat %s/vmm.pid)" 2>/dev/null || true; fi
i=0
while [ "$i" -lt 100 ]; do
  if ! %s >/dev/null 2>&1; then rm -rf %s; echo stopped; exit 0; fi
  i=$((i+1))
  sleep 0.1
done
echo "the VMM is still serving %s after teardown; not leaving it behind silently"
exit 1`, p.runDir, p.runDir, p.curl("/version"), sq(p.runDir), sq(p.sock))
}

// probeBootArgs keeps the probe's lease shaped like a real one without pretending to boot: a console on
// ttyS0 so a would-be guest's output is visible in the venue's log, and metadata disabled, because the
// probe writes its MMDS tree explicitly rather than relying on the kernel's default.
const probeBootArgs = "console=ttyS0 reboot=k panic=1 pci=off"

// probeMMDSAddress is the link-local address the guest would read metadata from (MMDS v2).
const probeMMDSAddress = "169.254.169.254"

// sq single-quotes a value for the venue's shell: a document written here is the document the VMM
// receives, byte for byte.
func sq(v string) string { return "'" + strings.ReplaceAll(v, "'", `'\''`) + "'" }

func jsonDoc(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}

	return string(b)
}

// venueWords renders what the venue said, from BOTH streams: over the reverse channel a command
// complaint arrives on stderr while stdout is empty, so a message interpolating only stdout reads as
// nothing after its colon. Measured exactly that: vmm probe on venue ssh: with an empty tail.
func venueWords(out, errOut string) string {
	if s := firstLine(out); s != "" {
		return s
	}

	if s := firstLine(errOut); s != "" {
		return s
	}

	return "the venue reported no output"
}
