# Recovery S0 libvirt public consolidation evidence

The four-patch `11.3.0-3+deb13u3+nex3` candidate passed the matched source build,
session-mode TCG lifecycle checks, and enforcing-AppArmor system-guest public
consolidation runs with and without a libvirtd interruption. The public cases
use per-disk `blockcommit` without `--delete`, rewrite retained point-B snapshot metadata, and remove
point-A metadata only, scans references before unlinking the exact point-A files,
and checks reopen and retained-point reverts. A separate earlier `+nex3`
integrated snapshot-delete attempt failed during the two-disk parent-delete
operation; this public sequence does not make that integrated path safe. S0
acceptance remains open, and no Core host package was installed for this result.

## Candidate and source

The retained candidate contains four local patches: the readonly AppArmor
helper fix, UUID-based revert-overlay names, partial backing-chain profile
completion, and bounded QMP JSON parsing. The two integrated snapshot-delete
patches from the historical six-patch `+nex2` candidate are absent from this
recipe. The immutable build inputs identify upstream Debian
`11.3.0-3+deb13u3`, local version `11.3.0-3+deb13u3+nex3`, and recipe hash
`3bf950c83fa9279de22e80c736984304e275d77ffcecd0609893cc5466ac46a5`.

The build produced 73 binary packages and complete corresponding source
artifacts. All 90 entries in the retained `SHA256SUMS` verified; its digest is
`116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`. The
patched source package was retained with the original Debian source and notices.
The full Meson suite reported 158 pass, one expected failure, one skipped, and
zero failures; the upstream `virt-aa-helper-test` passed. The readonly policy
and partial-chain helper checks passed. These are build and helper results, not
native recovery acceptance by themselves.

## Disposable runtime checks

The matched candidate passed one- and two-disk `qemu:///session` rapid lifecycle
cases under TCG. The AppArmor case ran in a fresh disposable Debian guest under
`qemu:///system`, also nested under TCG. Its kernel reported AppArmor enabled,
the AppArmor service was active, and all 15 system candidate packages matched
`+nex3`.

The public two-disk case started and paused its synthetic BIOS domain, then ran
`virsh blockcommit` separately for `vda` and `vdb` from point A to the standalone
base. Both commands completed with clear native jobs and no `--delete` or
`--active` option. Before and after each commit, the same QEMU PID and domain
UUID had the expected `libvirt-<UUID> (enforce)` AppArmor context. The probe
redefined point B with the standalone base as its parent and verified the
point-A/point-B/point-C snapshot graph before issuing
`virsh snapshot-delete <domain> point-a --metadata`.

After stopping the domain, the probe scanned snapshot XML, inactive domain XML,
QMP block/job graphs, and current and saved-point backing chains. The scan found
no references to either point-A file. It then unlinked exactly those two paths
and verified both were absent. All saved/post markers and payloads for points B
and C, plus current data, read correctly on both disks. The domain reopened
under the same UUID with `query-block`, `query-named-block-nodes`,
`query-block-jobs`, and `query-jobs` succeeding. Native reverts to retained
points C and B each preserved the expected data.

The normal-run raw AppArmor result is a 630,265-byte ignored artifact with SHA256
`a55fabdd9d911d2d10b43c9208fa9dbb5142a0159d7a3956036b58cd772d6e64`. The
outer runner result has SHA256
`4b9f3bc8e1afe953288eb15242c1d170d7f2e8511bc3dc88fdcb99837fe186b4`. The
native result predates candidate-receipt support and does not itself embed the
candidate manifest digest. A separate ignored, owner-curated execution record
links the actual runner invocation, verified candidate manifest and selected
package hashes to both result hashes; that record is provenance metadata, not a
native capture. Its SHA256 is
`f5277202cd6372207d89fc1837ec4a00f74f20f52941ae2a82ee465b5916b4f6`. The runner
now adds a candidate receipt to outer reports on future executions. The tracked
validator rejects an integrated `--delete` flag, incomplete metadata or
reopen evidence, nonmatching unlink paths, missing retained-data checks, and
missing retained-point reverts. The focused unit suites have eight CI-validator
tests and seven shared consolidation-probe tests.

### Interrupted public consolidation

A separate fresh enforcing-AppArmor system guest ran the same two-disk fixture
with a libvirtd interruption during the first public blockcommit. The probe
observed vda at 25.39% copied (34,078,720 of 134,217,728 bytes), with one
non-READY QMP commit job. The VM was paused; its UUID, QEMU PID, and enforcing
AppArmor context remained unchanged while libvirtd's PID changed. The waiting
virsh client disconnected as expected. After libvirtd restarted, the same job
reconciled to terminal clear without resubmitting it. The vdb commit then
completed normally.

The probe continued through point-B metadata redefinition, metadata-only
point-A deletion, a successful scan of snapshot XML, inactive domain XML, QMP
graphs, and current/saved backing chains, and unlink of only the two exact
point-A files. Both disks' retained data passed, the domain reopened under the
same UUID with all four required QMP queries, and native reverts to retained
points C and B preserved their expected data. This run did not use an
integrated snapshot data-delete operation.

The interrupted-run system result is an ignored artifact with SHA256
`58cfa68aca5acd5609132f1503935a33a2bdd2715a5bf4b0d4cd250c1c08a464`; its outer
runner result is SHA256
`e4ce45f255b9f532ae56fb841f4e21b251d164dc281fde9b191525feab5ab1d1`. The outer
result embeds a candidate receipt with the +nex3 version, exact SHA256SUMS and
recipe digests, and hashes for all 15 selected packages. The system result
itself records installed package versions but does not embed those package
hashes. The matched candidate remained the same immutable build described
above.

## Failed path and limits

An earlier separate `+nex3` system-guest trial used libvirt's integrated
two-disk snapshot parent deletion. It ended with a monitor connection reset
during deletion and an uncertain result. The failed artifact is preserved
separately from the passing public-consolidation evidence. The CI control now
exercises the supported no-delete blockcommit, metadata-only update, reference
scan, and guarded unlink sequence; it does not claim that integrated
multi-disk snapshot deletion survives interruption.

Three disposable-runner attempts stopped before native recovery checks: one
guest regenerated an injected SSH host key, one staging path was too shallow
for the shared probe imports, and one system-only payload omitted a helper
used by the interruption campaign. The guest now preserves its injected host
key while strict SSH host-key checking stays enabled, scripts are staged under
their expected integration directory, and the generated system payload is
compiled and tested with both captured `Block Commit` output forms. The
corrected interruption run passed the native checks described above. These
harness failures did not alter a Core host or establish a libvirt failure.

No Core package installation, Core storage test, remote Forge execution,
installer rebuild, or full snapshot/backup/restore phase acceptance is claimed.
The result is a bounded disposable system-guest check of this candidate and
sequence, not a general storage backend or production interruption guarantee.
