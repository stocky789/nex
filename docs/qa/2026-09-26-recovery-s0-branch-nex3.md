# Recovery S0: public off-branch consolidation on +nex3

Date: 2026-09-26. Status: **one bounded two-disk branch sequence passed in a fresh
disposable +nex3 system guest under AppArmor. This is public blockcommit plus
explicit offline metadata repair; integrated snapshot data-delete remains outside
this evidence, and S0 remains open.**

## Candidate and test environment

The guest ran the package-based Debian 13 candidate `11.3.0-3+deb13u3+nex3`
with QEMU `10.0.13`, `qemu:///system`, AppArmor enabled in the kernel, and the
AppArmor service active. It was nested under host TCG. All 15 selected libvirt
packages matched the candidate version; the verified candidate
`SHA256SUMS` digest is
`116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e` and the
recipe hash is
`3bf950c83fa9279de22e80c736984304e275d77ffcecd0609893cc5466ac46a5`.
The synthetic domain had two 128 MiB qcow2 disks and no guest OS.

The run used the system-guest launcher and shared consolidation probe. The
ignored launcher copy used a relative local script path so it could stage its
siblings from the workspace; its SHA256 was
`a7eedb16ce8be3a90689c3105d7a4a9546783f41662846402f7232df7e419271`. The
probe source SHA256 was
`15186687db36b01b0ded8a95c09d0d48a3a3134dc4687398bb489ad965565896`. The
launcher embedded the candidate manifest and hashes for the 15 selected
packages in its ignored execution receipt.

## Observed sequence

The probe created snapshots A→B→C, reverted B, then created D, making C and D
off-current children of B. It checked each saved marker and payload before
consolidation and recorded the actual saved disk sources, current heads, chains,
and snapshot metadata. QEMU started paused under its UUID-specific enforcing
AppArmor profile.

The probe issued public `virsh blockcommit` separately for `vda` and `vdb`,
from each A top to its original base, with `--wait --verbose` and without
`--delete` or `--active`. Both commands returned success, reported completion,
and had clear native jobs. Standalone logical copies made before the commits
matched the resulting base images. After QEMU stopped, it identified the two C
saved-source files whose immediate backing was an A top, verified each target
was a unique regular file in the fixture, rebased each offline to its proven
equivalent base, and compared the result with a pre-rebase logical copy.

The probe explicitly rewrote B's parent and saved-source references while
retaining C and D as children of B. It verified all retained markers, current
data, disk chains, and deletion flags before running
`virsh snapshot-delete <domain> point-a --metadata`. It then scanned the
fixture's current, saved, and revert sources and backing chains, plus foreign
domain active/default and inactive XML and saved snapshot XML. The fresh guest
had no foreign domains. The scan passed before the probe unlinked only the two
exact, now-unreferenced A top files.

Finally, native reverts to C and D each restored the expected marker and
payload on both disks. For each point, QEMU reopened paused under the same UUID
with the exact enforcing AppArmor profile and successful QMP block query; it
was then stopped. The branch result reports every step passed. Its SHA256 is
`92d57937c12af5f2549f08bfd8e95ea974a637c584ff4c3b68cca496bd037956`; the
separate ignored execution receipt binds that result to the verified candidate
manifest and selected package hashes.

## Harness failures and limits

Two earlier clean-guest attempts are preserved but excluded from the passing
result. One stopped before any commit because the observer treated a present
chain record as if it had an `absent` state field. The next completed both
commits, offline rebases, metadata-only A deletion, and A-top unlink, but its
first C reopen failed in the probe's post-reopen inspection: plain `qemu-img
info` could not share a lock with the paused QEMU. The probe now classifies
present and absent chain observations separately and uses the existing
`--force-share` observer while QEMU is open. The successful result came from a
new unique guest and domain; no commit, rebase, or unlink was replayed on either
failed fixture.

This is a synthetic, local-storage nested-TCG test of one two-disk off-branch
sequence. It does not test guest boot, an interrupted commit/delete, integrated
libvirt snapshot data deletion, other storage backends, backup/restore, or a
Core host installed from ISO. The explicit metadata redefinition, offline
rebase, reference scan, and unlink steps are probe actions, not a shipped Nex
operation or an operator workflow. Earlier +nex2 and CoreB partial evidence is
separate and is not counted here.
