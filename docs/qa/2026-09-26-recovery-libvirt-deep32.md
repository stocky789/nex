# Recovery S0 deep-chain public consolidation evidence

The four-patch libvirt `11.3.0-3+deb13u3+nex3` candidate completed the native
32-point, two-disk public-consolidation lifecycle in a disposable Debian 13
system guest. The guest used `qemu:///system`, local qcow2 disks, nested TCG,
and enforcing AppArmor. This is a bounded deep-chain check; it does not close
S0 or establish behavior on physical KVM, remote storage, Core hosts, or across
a host reboot.

## Candidate and checks

The run used the immutable four-patch candidate with recipe SHA256
`3bf950c83fa9279de22e80c736984304e275d77ffcecd0609893cc5466ac46a5` and
`SHA256SUMS` digest
`116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`. The
candidate receipt in the result records the same digests and all 15 selected
package hashes. Before native execution, the generated 32-point payload was
compiled and independently reviewed. Final Python discovery ran 538 tests with
one skip; the focused system-runner suite reported 15 passed.

The probe created 32 external snapshot points on two qcow2 disks. Both public
`blockcommit` operations completed from point-00 to the standalone base without
`--delete` or `--active`. It explicitly redefined point-01's saved source to
the base, removed point-00 metadata, scanned domain/snapshot XML and backing
chains for references, then read saved points, post heads, and the current
head before unlinking. It recorded 31 saved-state reads and 31 post-head reads
per disk, plus each current head; all marker and payload checks passed. Point-01
saved data was read from the rewritten base after the point-00 commit. The
remaining 31-point metadata and chain graph passed, with 32 QMP image layers
per disk on reopen under the same UUID.

The preserved native result is an ignored artifact with SHA256
`dcd29f89d05c1866c64936eca159a9b2b10d6999958addf063392060c66365d5`. It records
both completed commits, the metadata-only deletion, zero-reference scan,
pre-unlink saved/post/current reads and chains, exact absence of the two
point-00 files after unlink, and same-UUID reopen with all four required QMP
queries. The successful-path code saved an intermediate passed status and
undefined the domain, then its final status print passed `flush` to
`json.dumps`, raising `TypeError`. The exception handler overwrote the final
raw report with `failed-preserved`. The original artifact remains unchanged
and is not reported as a raw passing run.

The raw result's `qemu-img` chain checkpoints also show sparse-file sizes for
this synthetic workload. Each checkpoint was recorded when its snapshot was
created, before the new post-head marker and payload were written. Each disk
has a 128 MiB virtual size; the byte counts below sum `actual-size` for every
image in that disk's backing chain, and the aggregate adds both independent
disks.

| Snapshot created | Images per disk | Chain actual-size per disk | Two-disk aggregate |
| --- | ---: | ---: | ---: |
| point-00 | 2 | 667,648 bytes | 1,335,296 bytes |
| point-15 | 17 | 7,610,368 bytes | 15,220,736 bytes |
| point-31 | 33 | 15,015,936 bytes | 30,031,872 bytes (about 28.6 MiB) |

The probe reached the existing 32-point catalog limit before consolidation.
After point-00 deletion, 31 snapshot records remained and reopen showed 32
QMP image layers per disk, including the base. These file-size observations
reflect only this sparse image layout and marker/payload pattern; they are not
a capacity estimate, storage headroom guarantee, or workload performance
claim.

For review, the captured fields were checked read-only against the expected
saved-source mapping and then passed through the current validator using an
in-memory projection of the old data-shape and terminal-status envelope. That
projection is a reviewer aid, not a rewritten capture or proof that the
original runner returned success. The runner now emits the validator's
target-oriented data shape and uses a tested JSON status helper; those latest
corrections passed the focused tests but were not rerun in a native guest.

The first two fresh +nex3 deep32 guest attempts are separately preserved. One
stopped at the expected-parent assertion before metadata removal. The second
failed in the retained-data observer after unlink because it read the wrong
post-point path; its raw result SHA256 is
`87a450d5803a08451f1681b2985b6173afa89951f99045ca2840e029b7fa487f`. A separate
read-only reconciliation of that preserved guest recorded all 31 saved and
31 post-head reads on each disk; current-head reads are in a companion receipt.
Their SHA256 digests are
`85bb65fbd83f47cb8e767c31872c17fc8e90193ba3fcb532319c9cadb5396660` and
`524c14f6a65275dd7ce17969cecb0a286ee1d58a87dace4c132d63577c65c1fb`. Those
read-only receipts confirmed data remained readable and do not replace the
later native run. The third run's raw result SHA256 is
`dcd29f89d05c1866c64936eca159a9b2b10d6999958addf063392060c66365d5`; it
performed the readback and unlink steps but its terminal status print failed
after undefining the domain. The first attempt's raw result SHA256 is
`f2c7561a15501e963d8b14b51a233c64fd22b154733abaf337877001ee148a52`. None of
these raw runner reports is labeled passing. No Core
package was installed, and no integrated snapshot `--delete` operation was
used. The separate integrated-delete failure remains an unsupported path.
