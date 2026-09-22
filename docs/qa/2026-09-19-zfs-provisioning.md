# ZFS provisioning rollout verification

Date: 2026-09-19. Status: implementation and verification in progress; this report
is not acceptance or release clearance.

## Scope and environment

The work follows [ZFS repository provisioning](../plans/2026-09-19-zfs-repository-provisioning.md).
Verification separates native disposable-host behavior from simulated command, transport
and browser tests. Native tests use new virtual host overlays and explicitly selected
extra virtual disks; existing operator infrastructure and workstation disks are excluded.
No external artifact publication or Git history changes are part of this rollout.

## Executed checks

The distribution worker executed these checks against the working tree:

- `installer/check.sh`: passed.
- `python3 -m unittest tests.test_storage_packaging tests.test_iso_payload_efficiency -v`:
  16 tests passed. These inspect delivery configuration; they do not prove a DKMS build
  or an offline installation.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed at that
  point. The final combined change still needs its own run and manual privacy review.

Additional distribution verification:

- The installer builder image completed. Debian source metadata resolves the exact
  `zfs-linux` source version and its source archive entries through `contrib`.
- APT simulation of the installer package closure selected and configured
  `zfs-dkms`/`zfsutils-linux` `2.3.9-0+deb13u1` and the `linux-image-amd64` /
  `linux-headers-amd64` metapackages `6.12.107-1`. This is dependency resolution,
  not an offline installation or DKMS execution.
- Debian package inspection found native module-load, cache-import and mount services;
  Core does not weaken its service sandbox to load kernel modules.
- Storage packaging and release-publisher test groups: 35 tests passed.
- Independent distribution review found no implementation blocker; its packaging
  suite rerun passed 14 tests. Native image acceptance remains separate.

## Exact-image offline checks

The internal `0.3.4~zfsqa2` ISO and retained corresponding-source bundle built
successfully. Its SHA-256 was verified and the compliance manifest includes
`zfs-dkms` / `zfsutils-linux` `2.3.9-0+deb13u1`, the matching `zfs-linux` source
and notices. This snapshot predates final runtime corrections and is delivery
evidence, not the final application artifact.

`tests/integration/iso-smoke.py` installed that ISO offline onto a new disposable
24 GiB disk. The installation, first-boot and reboot markers passed. The runner now
checks ZFS whenever the package is installed; the executed first boot and reboot
each emitted `NEX_SMOKE_ZFS_PASS` after DKMS inspection, `modprobe zfs`, native tool
version checks, enabled cache-import service and active mount service. Evidence is
under `.cache/zfs-image-qa/offline-zfs-smoke/`. These checks do not establish Secure
Boot, physical-hardware support or a distinct-version kernel upgrade.

The internal `0.3.5~zfsqa3` image subsequently built with the final Core recovery
fixes; it preceded the last Controller source-label correction.
Its ISO SHA-256 is
`ccecd18c2e58edd19a4f11a980f48e4e22fb81114440b02ac7269356295a8f1f`
and its corresponding-source bundle SHA-256 is
`feb0ddf53794f99bd6c53006623713a6fb0d85e7adc6fef18a9242c1ae15833b`.
The earlier QA3 attempt was stopped before producing an ISO when the tombstone
reporting defect was found. The completed build includes that Core fix; isolated offline installation and both
ZFS boot checks passed. QA4 includes the later Controller label correction. Full build output is retained at
`.cache/zfs-image-qa/build-qa3-final.log`.

The final implementation image is `dist/nex-core-0.3.5~zfsqa4-amd64.iso`, with
corresponding-source bundle and compliance manifest beside it. The primary independently
verified the manifest's ISO SHA-256
`9e51afea96d30f12639113e11f6a0be66042e53f5ec9de1a37d026188ff9391b`
and source-bundle SHA-256
`57025f3b5fa331eedd583915e1fb24b6064015f6f60322af41ea1fbf7cea96c3`.
The primary compared
all 235 Go, SQL, TypeScript and CSS implementation/test files under `cmd`, `internal`
and `web/src` against the retained Nex source archive: all matched the working tree.
That product source archive has SHA-256
`a1d95f414bd817ff3656341901186eb9160c76cc77f5e95b8f2814ae341aea50`.
VCS status/diff digests retained in the ignored build directory are bookkeeping,
not source-archive hashes or proof of untracked file contents. QA4 passed fresh
offline installation with `-nic none`, first boot and reboot. Both boots loaded
`6.12.107+deb13-amd64`, reported ZFS module/tools `2.3.9-0+deb13u1`, verified
cache-import/mount services and emitted exactly one `NEX_SMOKE_ZFS_PASS`. The
primary inspected the serial evidence under
`.cache/zfs-image-qa/offline-zfs-qa4-final/`; all owned smoke guests are stopped.

## Kernel package upgrade and retained-kernel boot

A separate owned copy of the QA2 installed disk exercised an actual offline package
transition while running `6.12.94+deb13-amd64`. The fixture removed the preinstalled
107 image/header/meta packages, retained the working 94 kernel, then installed the
staged official `6.12.107-1` image/header/kbuild closure. Temporary dependency overrides
were fixture preparation only, not a supported operator upgrade procedure.

Earlier fixture attempts selected the wrong GRUB entry, repeated removal during
recovery, or exited before reporting package completion. Their logs remain retained;
they are not passing evidence. The primary corrected the test-only recovery branch
and saved command output to a regular file instead of the reset serial terminal.
Boot 15 used the installed 94 kernel/initramfs through QEMU's explicit kernel boot
option to resume `dpkg --configure -a`. Native package hooks built, signed and installed
ZFS for 107, ran depmod, generated its initramfs and updated GRUB successfully.

Boots 16 and 17 then used **normal disk/GRUB boot**, with no QEMU kernel override.
The fixture used a flat generated GRUB menu to select 107 and then retained 94.
Each boot asserted its actual `uname -r`, installed DKMS status, successful
`modprobe zfs` and matching ZFS tool/module version `2.3.9-0+deb13u1`.
`package-upgrade.log` contains all three pass markers:

- `KERNEL_107_OFFLINE_PACKAGE_UPGRADE_PASS`
- `KERNEL_107_DKMS_ZFS_AFTER_PACKAGE_UPGRADE_PASS`
- `KERNEL_94_FALLBACK_DKMS_ZFS_AFTER_PACKAGE_UPGRADE_PASS`

Evidence is under `.cache/zfs-image-qa/kernel-upgrade/`, including
`dpkg-final.log`, `package-upgrade.log`, `boot-16-grub107.serial.log` and
`boot-17-grub94-fallback.serial.log`. The fixture reached phase `complete` and
powered off; no owned kernel-test guest remains. This proves native package hooks
and both kernel boots in the disposable VM. It does not certify Secure Boot,
physical hardware, an interactive operator recovery walkthrough or a failed-kernel
fault injection. This older QA2 fixture isolates distribution behavior; final
application-source image evidence is the separate QA4 check above. Independent
review inspected the package transaction, all three pass markers, both GRUB boot
logs and the stated limitations, and found no blocker.

## Native disposable-host preparation

The native worker booted two new QEMU overlays with dedicated 8 GiB virtual data disks
and stable virtual serial numbers. The system disk is a separate 64 GiB overlay; no
workstation block device is passed through. An explicit `virtio-blk-pci` boot device
with `bootindex=1` corrected the fixture's initial firmware boot failure. A guest reboot
was verified by a changed boot ID.

On the first disposable guest, Debian package installation and native DKMS completed.
The cloud base initially booted its cloud kernel, while the installed `linux-image-amd64`
and `linux-headers-amd64` selected the generic kernel. After selecting and rebooting into
`6.12.107+deb13-amd64`, the worker verified DKMS reported ZFS `2.3.9` installed for that
kernel, `modprobe zfs` succeeded, and `zpool version` / `zfs version` reported
`2.3.9-0+deb13u1`. This proves a native module build/load on that disposable guest;
it is not an exact Nex ISO installation, Secure Boot or kernel-upgrade test.

## Native host API findings

A real Controller and Core agent enrolled in the first disposable host. Authenticated
`POST /api/v1/storage/discover` returned the system disk disabled and the extra disks
with by-id paths, serials, sizes and observation fingerprints. A provisioning request
without the required fingerprint was refused; supplying the observed fingerprints
returned a queued durable operation.

The first native create reached `needs_attention` with
`property 'nex:repository' is not a valid pool or vdev property`. No zpool was created.
The draft incorrectly passed the marker as a pool property. The correction uses the
root filesystem property (`zpool create -O` and `zfs get`), which can be set atomically
during pool creation. See the [upstream command reference](https://openzfs.github.io/openzfs-docs/man/master/8/zpool-create.8.html).
A subsequent native run created an ONLINE mirror and the `vms` dataset with the
root-filesystem ownership marker. Registration then exposed a missing lab libvirt
account and a missing Core background retry path. Core now checks a completed native
receipt and the live tagged pool/dataset before retrying registration; failed create
intent alone cannot create a healthy repository projection. Native retry retained the real pool GUID and did not provision the earlier failed-create
intent. After correcting the lab mount state, registration succeeded; a subsequent
real reboot proved native cache import and dataset mounting with unchanged GUIDs.
The native ZFS services were enabled; the earlier boot log predated pool creation.
After rebuilding Controller and Core, the native storage API no longer returned a
phantom successful mirror of the earlier failed create. Subsequent replacement testing
exposed an orphaned adoption-operation problem in deletion of absent mirrors. The
current reconciliation retains those rows as unusable until Core reports registration
again; unavailable inventory grants no new access. The replacement initially failed
with orphaned adoption operations and former-Controller results. After the scoped
repair, the same preserved database accepted the master report and retained eight Core
provisioning outcomes. After member re-enrollment, the storage API preserved canonical
repository IDs and names, reported `shared: false`, returned two host-access entries
for the master-first repository and one for each member-only repository.

The managed VM API created a VM with a thin qcow2 disk on the new dataset, and its
start operation succeeded. With the VM stopped, the worker wrote and flushed a 4 KiB
pattern at offset 1 MiB using `qemu-io`, rebooted Core, then read the same pattern.
The pool/dataset GUIDs were unchanged, the pool ONLINE, the dataset mounted and the
VM definition retained. This is **disk-layer persistence**, not a booted guest
filesystem test. The intentionally blank guest could not handle ACPI shutdown; later
reboots used the stopped VM rather than treating a shutdown timeout as success.

A request carrying a changed discovery fingerprint over blank disposable devices was
accepted asynchronously, then rejected by Core as `needs_attention`. Native checks
confirmed only the original pool existed and the selected devices remained blank.
The asynchronous HTTP response alone was not treated as proof.

With the VM stopped, the worker offlined one selected mirror device. Native state
changed to DEGRADED and the fresh Controller report made storage unreadable and
unwritable with `ZFS pool health is DEGRADED`. Onlining the same device completed
resilvering and restored ONLINE with no known data errors.

The full managed VM lifecycle passed create, start, force-stop and disk deletion.
Deletion removed the selected domain and qcow2 while leaving its repository ONLINE.

An explicitly labelled lab fault injection paused after actual tagged pool creation
before the dataset, then interrupted the owned agent. The same operation recovered
the missing dataset and registration with the original pool GUID and selected vdevs,
without another pool or erase. This exposed the real whole-disk/partition-vdev mismatch
and an acknowledged-result propagation gap; regression checks now cover both. The final Core journal now records the original operation as succeeded. The previous
Controller database retains its historical attention outcome after replacement; native
proof that it received the later result before disconnection is unavailable.

Two disposable Core-local services completed the authenticated CSR, master offer and
member join flow over isolated lab tunnels. An offline-member provisioning request
remained durable and retried successfully after reconnect (the Core retry interval is
one minute), creating a member-only repository with no corresponding master dataset.
A later request selecting that member's now-in-use devices was refused without changing
the existing pool or dataset. This proves native in-use rejection; swap/md/LVM branches
also have simulated regression checks rather than hardware walkthrough evidence.

A separate master-first pool repository then exercised partial success: the master
remained usable while the selected member was offline, and the member later completed
under the same repository identity after reconnect. This does not make the disks shared.

The native old-data workflow discovered a filesystem-bearing selected disk as
`erase_required`, required its exact by-id confirmation and provisioned a host-scoped
single-disk repository on the member. A separate unselected disk's full-device SHA-256
sentinel was unchanged. An initially unavailable peer path reported attention, and
Core safely completed the same operation after connectivity returned.

The managed VM API placed a VM on the new member-only repository, and an explicit
start reached `running` on that member. A request using a network absent from the
member was correctly refused; the successful placement used a VM without a NIC.
The same VM then completed force-stop and explicit deletion with disk removal.
Controller operation history and the native journal confirmed those requested actions.
This proves native VM process/disk placement and lifecycle, not a booted guest filesystem.

Native repository rename and recheck then reached `succeeded`. Detach safely refused
a member generation mismatch after rename: the canonical master's generation had
advanced while the member's local registration generation had not. The native pool
remained intact. After the Core generation and receipt fixes, a new explicit detach
request reached `succeeded`, the master aggregate was removed and the native pool GUID
was unchanged. That earlier development receipt lacked canonical tombstone metadata. A final
new-repository run with the corrected receipt schema removed the Controller row
as well; the earlier fixture history was not edited to manufacture a pass.

## Final native recovery and detach proof

A fresh pair of owned virtual disks on the member was discovered and selected through
the real Controller API. A labelled lab wrapper paused immediately after actual tagged
pool creation, before dataset creation. The worker interrupted only that Core process,
restored the native tool and restarted the latest Core. The original Controller task
changed from `needs_attention` to `succeeded` after Core recovery. The pool GUID was
unchanged, and the member dataset and registration became healthy under the same
repository identity. This closes the earlier original-task readback gap without
rewriting the prior Controller's history or reusing its disks.

The same new repository then completed rename and recheck, followed by detach using
the current canonical master and generation. The task reached `succeeded`, the
Controller storage API excluded the repository, the master aggregate was removed,
and the member's durable detach receipt was succeeded. The master lifecycle receipt
retained generation three with `detach_ready` and `completed` true. The native pool
GUID remained unchanged; both disposable hosts had no remaining test VMs.

The ignored lab `runtime` evidence includes `final-data78-discovery.json`,
`final-recovery-provision-request.json`, `final-recovery-operations.json`,
`final-recovery-native-zfs.txt`, `final-post-rename-storage.json`,
`final-detach-operations.json`, `final-detach-storage.json`,
`final-master-zfs-lifecycle.json` and `final-member-detach-receipt.json`. These contain
actual disposable resource IDs and remain outside public documentation.

## Browser and contract checks

The UI worker ran `npm --prefix web test`, `npm --prefix web run typecheck`, and
`npm --prefix web run build`: passed. Chromium ran
`.cache/phase1-qa-venv/bin/python tests/integration/zfs_provisioning_browser.py --evidence .cache/zfs-provisioning-browser-complete`
against the built UI with **simulated Core replies**. It exercised per-host device
selection, disabled system devices, exact old-data confirmation, fingerprint forwarding,
configuring another host under an existing repository ID, and a lost-reply close/reopen
retry that retains the request ID. The primary inspected the disk-selection and
confirmation captures; a duplicate step-number rendering was corrected and recaptured.
The final browser pass also exercised loading, empty and stale replies, keyboard
opening, grouped radio Space/arrow selection, and configure-to-new wizard identity.
A competing toolbar action is disabled while the wizard is open. The primary inspected
the final confirmation and stale-state captures; aligned scope controls were recaptured
and independently inspected. These checks do not prove native erasure or ZFS recovery.

A separate **real-Controller** read-only browser walkthrough then used the disposable
pool's actual Core inventory. It inspected canonical ZFS rows, host access, capacity,
healthy/readable/writable indicators, deliberately stale historical rows and tasks.
That capture exposed a duplicate pool prefix in qualified ZFS dataset labels. The
formatter regression failed before correction and passed afterward. After rebuilding
and restarting only the disposable Controller with the same database, the browser
asserted one pool prefix and recaptured the rows. The primary inspected the final
`native-storage-rows.png`; evidence and results are in
`.cache/zfs-provisioning-native-browser/`. The UI conveys local host access; the
explicit `shared: false` assertion belongs to the native API checks, not a UI badge.
No browser mutation or erasure was performed in this read-only walkthrough. After
verification, both owned native-lab guests and their Controller/tunnel processes
were stopped; disks, state and evidence were retained.

Controller and protocol worker checks passed: `go test ./internal/controller`,
`go test ./internal/protocol`, `python3 -m unittest tests.test_api_contract`, and
`python3 scripts/generate_api.py --check`. The API checks cover the new client and peer
contracts, composed peer paths and schema/runtime field coverage. Subsequent recovery tests cover the actual NVMe by-id partition-parent path and the
production completion handoff, including a blocked send canceled during shutdown and
a recovered operation completing while another command remains active. Independent
review and race checks passed for these paths.

The primary reran `go test -race -timeout 20m ./...` after the Core and Controller
corrections: all packages passed (unchanged package results reused from the prior
passing run; the final agent, Controller and localcore packages passed in 7.870,
158.985 and 10.205 seconds after the lifecycle and tombstone corrections). The log is `.cache/zfs-rollout-go-tests-final.log`.

The primary ran `python3 -m unittest discover -s tests -p 'test_*.py'`: all 325
tests passed again (13.464 seconds), including API, packaging, release and privacy checks.
The log is retained under `.cache/zfs-rollout-python-tests-final.log`. The primary also
inspected the final `confirmation-top.png`, including the full destructive review.

Independent review checked safety revalidation, retry identity, Core ownership/adoption,
per-host placement, lifecycle routing and immutable-source validation. Its focused ZFS
Go, API contract, docs, privacy, UI and diff checks passed; this does not replace the
remaining native and image tests. A subsequent independent review passed the
Controller orphan-adoption repair and its focused regular/race and full Controller
tests; conflicting retained records fail the transaction rather than being rebound.

The primary ran the repository privacy check successfully and the worktree documentation
gate passed after adding the implementation assessment. The final documentation gate passed for all 97 uncommitted paths; the privacy test,
generated API reference check and `git diff --check` also passed. Independent final
semantic review found no remaining documentation blocker. These are structural/review
checks, not a substitute for native acceptance.

## Reproduction entry points

The isolated host fixture is prepared with:

```sh
python3 tests/integration/zfs_provisioning_lab.py --prepare --hosts 2
```

It prints its newly owned lab directory. Pass that exact directory to `--lab-dir`,
with `--start`, `--host core1 --reboot`, or `--host core2 --command 'zpool status'`
as appropriate. The fixture creates virtual disks only and guards its directory
identity. It prepares hosts; it does not automate the whole acceptance sequence.
Native provisioning, peer join and lifecycle checks above used authenticated API
requests and explicit selected virtual disks, with request/response artifacts in
the ignored lab's `runtime` directory. Credentials and actual resource IDs are
excluded from this public report.

For lifecycle reproduction, fetch the current `GET /api/v1/storage` row and pass
its canonical `host_id`, `pool_id` and `generation` as `expected_generation` to
`POST /api/v1/storage/{id}/update`, `/recheck` or `/detach`. Member-local storage
still uses the canonical master for these requests. Poll the returned task to a
terminal result, then verify fresh Core inventory and the native pool GUID; HTTP
202 alone does not prove completion. The [API reference](../API.md) defines the
request schemas and authentication, and the [guide](../guide/storage-repositories.md)
defines the operator workflow and erasure confirmation.

## Open acceptance evidence

The following remain unproven until an executed result replaces the corresponding item:

- Hosted GitHub/Gitea CI has not run because no push was performed. Local full and
  focused checks, final documentation/privacy gates and independent review passed
  with the scope recorded above.
- Master change with the repository record intact depends on the unimplemented
  [Phase 4 authority-transfer capability](../plans/2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession).
- External redistribution claims still require the exact OpenZFS component/source licence,
  notice, offline dependency, supported-kernel, Secure Boot and recovery evidence in
  [licensing](../LICENSING.md). A passing code check is not that evidence.
