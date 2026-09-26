# Recovery S0 libvirt backport lab evidence

Status: the four-patch `+nex1` candidate and a separate six-patch `+nex2`
candidate have both been built with corresponding source packages. The six-patch
build, source reconstruction, Meson suite, and helper checks passed under a
resource-capped disposable Docker container. Core A now has the exact six-patch
closure installed; the installed helper policy check and native two-disk
32-point chain stress passed, including parent deletion, full marker/chain
reconciliation, newest-first cleanup, and QEMU reopen. This does not validate
an interrupted-delete restart. Core B's four-patch installation had no native
check recorded before its current campaign. Core C has the three-patch candidate
installed and a curated reopen summary, while a logged source-reopen recheck
remains pending. The AppArmor-enabled guest CI runner has not been run. No
remote Forge run has occurred. This record does not establish installer
integration or complete snapshot, backup, or restore support.

## Scope

The four-patch lab candidate combines Debian libvirt `11.3.0-3+deb13u3` with the
upstream readonly AppArmor helper fix `7b889af2d235d3fadfc172f371f02df0b74e40f2`,
a local UUID-based temporary revert-overlay name fix, and the local
partial-backing-chain metadata fix, plus the local serialized external snapshot
delete fix. The local package version is `11.3.0-3+deb13u3+nex1`; each cohort
is identified by its recipe and checksum manifest, not version alone. The
upstream helper permission flag `R`
removes a generated explicit deny; it does not grant write access. A separate
write rule remains necessary for a writable path. The local revert change only
changes the transient generated overlay name and retains target validation.

The four-patch APT-locked recipe completed end to end using Debian and Debian security snapshot
metadata dated `20260920T000000Z`, bounded parallelism and no
`DEB_BUILD_OPTIONS=nocheck`. APT verified its snapshot metadata; Debian pool
archive bytes were cached only after SHA256 matched the expected values from the
pinned APT package indexes. The full Meson run completed with 158 tests passed,
one expected failure, one skipped and no unexpected failures; the upstream
`virt-aa-helper-test` passed. The build produced 73 version-matched binary
packages at `11.3.0-3+deb13u3+nex1`.

The four-patch retained bundle contains the untouched source artifacts, complete
patched source package, Debian copyright notice, build inputs, exact recipe,
binary manifest and logs. All 90 `SHA256SUMS` entries verified; the checksum
manifest hash is
`a359ec053cbad8a789d082f63de51aefd45d78ba6e03df287499c58dad51080a`. The build
input manifest records recipe hash
`a38a06cdb52a26ae08dc2e18538ee2253c11d763f3321aeb1baf39c82bdf5034`. The
source package was reconstructed from its `.dsc`; a no-follow tree comparison
against the compiled source passed, excluding only Debian's generated
`debian/.pc` and `debian/files` state. The patched `.dsc` hash is
`3289e58eb09953e749050aa594ccd921afb5e2fc99fe7d7442cceb12b666f83e`; its
Debian tar hash is `7e6b42c5bcb4cd4770da6dd1d9e347d2a683f4a26d2a675d811b8e5c7deb3f8a`.

An initial source-package attempt rejected the AppArmor patch because its hunk
needed fuzz after Debian's existing patches. The context was refreshed against
the exact Debian-patched source. The before/after SHA256 of both changed C files
was unchanged; the refreshed patch applied without fuzz. The final end-to-end
recipe then rebuilt the candidate and generated the matching source package.

## Six-patch `+nex2` build

A separate six-patch candidate was built from the pinned Debian Trixie
`20260920T000000Z` source/security snapshots in one fresh `--init` container
using the pinned `debian:trixie-slim` image. The container was limited to 0.8
CPU, 3 GiB memory and swap, and blkio weight 100; the builder completed with
exit code 0 and was not OOM-killed. The immutable recipe hash is
`351d6faf4560879e2e442a3ba98c8874f418c54f638a2683f92a055253d91f35`. It built
73 unique binary packages at `11.3.0-3+deb13u3+nex2`; all 92 retained SHA256
entries verified. The manifest hash is
`926cf76145b10555bc535d68d2df38581dace88efc154ae70d3a37990d1a4f60`. The
patched `.dsc` hash is
`56dc146870e0eff8b737bb4d99aa03ae83bb88082054d6d47327557d281a6a08`; the
Debian tar hash is
`e84bdff49ff93192551a1178f776b55f95d89e25cd6ca7572d99b9371a3c7238`.

Reconstruction from the `.dsc` succeeded. The nine C files touched by the six
patches match the compiled source tree. Meson reported 158 passed, one expected
failure, one skipped, and zero failures; `qemumonitorjsontest` and
`virt-aa-helper-test` passed. The patched helper policy regression and partial
backing-chain checks passed. These are build/source/helper results only: the
`+nex2` candidate has not been installed on a Core host, and no AppArmor system
guest or native recovery case was run.

## Helper policy regression

`tests/integration/libvirt_readonly_policy.py` runs the actual
`virt-aa-helper --dryrun -c` against a domain with a readonly top-level disk, a
readonly backing file, a writable overlay, and a separate writable disk. It
checks both readonly branches retain `rk`, writable paths retain `rwk`, and
readonly paths have no explicit write-deny in the patched helper output.

The original Core A helper was tested through the selected disposable lab's
authenticated SSH path. The normal patched-policy expectation failed because
the original helper emitted the explicit readonly backing-file deny. The
`--expect-legacy-deny` comparison passed and confirmed the expected deny for
both readonly branches while retaining `rk` and `rwk` for the respective
readonly and writable paths. This was a helper dry-run; no VM, disk, service,
package, or AppArmor policy was changed. The built candidate helper passed the
normal policy expectation: readonly paths retained `rk`, writable paths
retained `rwk`, and no readonly path had an explicit write-deny. The helper test
used the candidate `virt-aa-helper` and matching `libvirt0` libraries.

A follow-up helper-source patch, `packaging/libvirt/partial-backing-chain.patch`,
and an expanded regression case cover XML that declares only a prefix of a
disk's backing chain, plus an explicit empty-chain terminator. The retained
pre-patch helper failed this case because its profile omitted the deepest base
file's read grant while retaining grants for the top and intermediate files.
The patched helper passed the same test, including the explicit terminator
case. The regression creates an actual three-level qcow2 chain; it asserts
`rwk` for the top, `rk` for both implicitly discovered backing files, and no
grant for the base when an explicit empty backingStore terminates traversal.
The code preserves the 200-entry total chain-depth limit and does not add a
write grant. This helper-only red/green result is now included in the latest
matched package build; by itself it does not establish native snapshot deletion
or reopen behavior.

## Native lifecycle evidence

The native S0 baseline on the selected disposable Core candidate reproduced a
stopped external snapshot-delete block-commit permission refusal while the
backing file was DAC-writable. The refusal preserved the domain and snapshot
artifacts. This establishes the observed failure and motivates the targeted
helper-policy test; it does not by itself establish AppArmor as the only cause.

The preceding two-patch package was installed on the disposable Debian 13 Core
candidate. Its patched native probe passed four fresh cases: one- and two-disk
immediate reverts, and one- and two-disk paced reverts. It checked point markers
and backing chains, asserted UUID-suffixed revert overlay paths, deleted points
C/B/A, then successfully reopened each domain in QEMU. The cases did not boot a
guest OS.

The three preserved pre-patch delete failures were reconciled against their
saved domains, current heads and backing chains, then the exact selected native
delete was retried after installing the canonical package. All three retries
passed and cleared the parent in-progress flag while retaining the expected
markers and chains. No snapshot metadata was edited. This does not simulate a
process crash during deletion.

The four-patch package closure was installed on Core B, but no result from its
current six-patch campaign is included here. Core A's exact six-patch `+nex2`
closure was staged from the retained native-install manifest; all 15 local
package hashes matched. APT simulation showed 15 upgrades, no installs or
removals, and the install completed successfully. The post-install audit was
clean, libvirtd was active, all 15 package versions were `+nex2`, and all eight
pre-existing recovery domains remained shut off. The installed
`virt-aa-helper` passed the readonly policy and partial-chain/terminator checks.

On that exact Core A cohort, `recovery_s0.py --stress-point-chain 32` passed a
two-disk chain with 32 retained points. It deleted parent `stress-08`, verified
all retained markers and backing chains with no deletion-in-progress flags,
deleted the remaining 31 points newest-first with reconciliation after each,
and reopened the final chain in QEMU (`running`; no guest OS was booted). The
run exited 0 in 144.17 seconds. Raw ignored evidence is
`.cache/recovery-s0-native/six-patch-corea-deep32-8c9a0c8cbd/raw/result.json`;
the package manifest, pre/post-install state, helper output, and APT transcript
are in the sibling evidence directory. This normal native chain test does not
exercise a process interruption/restart or the enforcing-AppArmor CI path.
Core C received the three-patch candidate; a curated source-reopen summary is
recorded in the [S0 backup QA note](2026-09-25-recovery-s0-backup.md), but it
has no raw post-install command log and a logged recheck remains pending. The
earlier two-patch native results do not establish behavior of the later package
cohorts.

## Disposable CI regression

The earlier three-patch version of `python3 tests/integration/libvirt_recovery_ci.py`
passed in fresh `--init` Docker containers: it ran the helper policy regression
and one/two-disk rapid `qemu:///session` TCG lifecycle with UUID-overlay, marker,
chain, deletion, and reopen checks. That result is scoped to the three-patch
artifact and runner revision at that time. The runner has since been extended
to boot a checksum-pinned Debian cloud guest under TCG, install AppArmor and the
matched native package closure, then run the existing one/two-disk parent-delete
control through `qemu:///system`. The guest path uses a pinned ephemeral SSH
host key, waits for cloud-init completion, and retains the guest overlay and
logs on failure. This new system-mode path has received source and validator
tests but has not been run. Its validator requires the domain
QEMU process to be observed in its enforcing AppArmor profile, successful
parent deletion, reconciled markers/chains and clear deletion flags, both
remaining snapshot deletions in newest-first order, and a successful final QEMU
reopen. The enforcing QEMU observation is required for the initial parent
merge, which is the race under test; later cleanup deletions are checked for
successful commands and reconciled state without requiring another transient
QEMU process. Focused tests cover missing cleanup and missing reopen evidence, and exercise
selection of the mixed `amd64`/`all` package closure. The selector also chose all
15 expected package artifacts from the retained four-patch output using exact
package, version and architecture metadata; this checks selector compatibility,
not the six-patch system-guest run. The earlier session-mode run does not establish
AppArmor behavior. No remote Forge run has occurred.

## Bounded QMP parser depth proposal

`packaging/libvirt/qmp-json-depth.patch` adds a private JSON parser entry point
with an explicit maximum depth, keeps the existing parser at JSON-C's default,
and uses the higher chain-derived limit only for QEMU monitor JSON replies. Its
source diff and monitor regression were independently reviewed. An earlier
patch revision's focused monitor regression passed before its parser wrapper
was simplified; the patch author also reported a direct JSON-C boundary probe.
The six-patch `+nex2` build compiled the current patch hash; its
`qemumonitorjsontest` passed as part of the Meson suite. The patch is outside
the four-patch candidate and is not included in its checksums or native results.

## Interrupted external-delete recovery marker

`packaging/libvirt/snapshot-delete-recovery.patch` moves persistence of the
`snapshotDelete` job marker to the common external-delete preparation path,
including for an offline VM. The intent is for libvirt reconnect recovery to
recognize and cancel an interrupted external block commit. The patch author
reports a clean dry-run against the retained four-patch source. The six-patch
`+nex2` recipe applies and compiles the patch. The native
[interruption campaign](2026-09-25-recovery-s0-interruption.md) captured the
marker and verified native cancellation after restart. It also exposed BUG-0590:
the first completed disk image was already removed while a retained snapshot
still referenced it. This is a failed recovery gate, not a passing deletion
regression. The current Meson suite does not exercise that stopped multi-disk
interruption boundary.

## Limits

This is a disposable-lab packaging experiment, not an installer or release
integration. The six-patch candidate has passed a native local-filesystem
32-point deletion and reopen case on Core A, but the enforcing system-mode
AppArmor guest runner has not run, and the interrupted-delete/restart
regression failed retained-point integrity; remote Forge execution remains
unverified. Earlier native and
three-patch TCG results are tied to their own artifact cohorts. No claim is
made that the marker change by itself resolves process interruption or recovery
from an interrupted delete. The available cases do not establish shared
storage, a guest OS boot, installer delivery, or full backup and restore. The
S0 plan remains open. No Controller/Core API changed, no operator workflow
changed, and no Nex durable state was added.
