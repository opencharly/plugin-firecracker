#!/usr/bin/env node
// check-bed-subject-parity — R2 guard for plugin-firecracker#6's finding 3.
//
// candy/producer-bed-env writes a SUBJECT project inline into a disposable guest, because
// the guest has no repo to copy from. Its two manifests are copies of the SHIPPED
// definitions — box/check-firecracker-rootfs and candy/check-firecracker-guest — and two
// copies of one definition drift.
//
// This cannot make them one file: the bed runs in a guest that does not have the repo, so
// the content has to travel inside the bed's manifest. What it CAN do is make drift
// impossible to land: every non-trivial line of each shipped definition must still appear
// verbatim in the bed's writer. Change a shipped definition without updating the bed (or
// the reverse) and this fails naming the line.
//
// FAILS WITHOUT the parity it asserts: edit box/check-firecracker-rootfs/charly.yml and
// re-run — it exits 1 listing the line the bed no longer carries.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

// Lives in the candy dir so copy:/command: can reach it; climbs two levels to the repo root.
const root = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const bed = readFileSync(join(root, 'candy/producer-bed-env/charly.yml'), 'utf8');

const subjects = [
  ['box/check-firecracker-rootfs/charly.yml', 'box'],
  ['candy/check-firecracker-guest/charly.yml', 'candy'],
];

// A line is "significant" when it is not blank, not a comment, and not the definition's
// own top-level key — the bed renames the subject, and that key is the one line that
// legitimately differs.
function significantLines(src) {
  return src
    .split('\n')
    .map((l) => l.replace(/\s+$/, ''))
    .filter((l) => l.trim() !== '' && !l.trim().startsWith('#'))
    .filter((l) => !/^[A-Za-z0-9_-]+:\s*$/.test(l));
}

let missing = 0;
for (const [rel] of subjects) {
  const shipped = readFileSync(join(root, rel), 'utf8');
  const lines = significantLines(shipped);
  const gone = lines.filter((l) => !bed.includes(l.trim()));
  if (gone.length) {
    missing += gone.length;
    console.error(`check-bed-subject-parity: ${rel} has ${gone.length} line(s) the bed no longer carries:`);
    for (const l of gone) console.error(`  MISSING ${JSON.stringify(l.trim())}`);
  } else {
    console.log(`check-bed-subject-parity: ${rel} — all ${lines.length} significant lines present in the bed`);
  }
}
if (missing) process.exit(1);
console.log('check-bed-subject-parity: OK — the bed\'s subject copies track the shipped definitions');
