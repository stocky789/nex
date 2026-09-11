# Storage implementation handover — paused for continuation on another machine

## Read this first

The project owner requested a pause so this working tree can be committed and continued at home.
**Resume implementation acceptance; do not start the feature again or call it complete.**
Terra 5.6 medium subagents implemented and reviewed the work. Continue using that model and
reasoning level for bounded independent work, with one owner per file and one owner of lab mutations.

The feature is single-host VM storage repositories: NFSv4, existing local OpenZFS datasets,
SMB3 shares, and dedicated single-initiator iSCSI filesystems. Core owns registrations,
identities, credentials, operations and recovery; Controller projects host inventory and asks
Core to act. No multi-host placement, migration, HA, ZFS pool creation or iSCSI formatting was added.

Read [the implementation plan](2026-09-11-vm-storage-repositories.md),
[QA results](../qa/storage-repositories/checks.md),
[the storage guide](../guide/storage-repositories.md), and
[BUG-0082 through BUG-0090](../BUG_LEDGER.md). The existing working tree also contains other
in-flight changes; preserve them. Agents have not committed or pushed anything.

## Safe pause checkpoint

The lab owner confirmed all test VMs are shut off, no Controller operations are pending, and
faulted exports/shares/mounts have been restored. Controller and the Core agent remain running
and healthy on the office machine. No temporary QEMU writer or package builder remains.
The isolated undersized-iSCSI residue was captured and removed without changing baseline storage.
The product cleanup fix and unit regressions are now in-tree; native re-verification remains open.

## How the work was tested

**We were running actual Nex Core and Controller in a disposable local lab to establish that
VMs can use each storage type, then testing failure and recovery paths.** This was not only
unit testing, mocked mounts or a UI demonstration.

- Core ran in a Debian 13 KVM cloud guest with a private 64 GiB qcow2 overlay, 6 GiB RAM,
  four vCPUs and QEMU user networking. Only loopback SSH forwarding was used on the workstation.
  No workstation libvirt service, mounts, physical disks or production storage were changed.
- Controller ran locally as a separate real process with its built React UI. Core was enrolled
  into it and used the shipped agent systemd protections, adapted to a private lab state path.
  Repository registration, VM creation and power operations went through Nex APIs.
- NFS server, Samba, OpenZFS file-vdev pools and Linux LIO iSCSI targets ran **inside the disposable
  Core guest**. iSCSI filesystems were prepared explicitly as lab fixtures, not formatted by Nex.
  This co-location makes storage failures/reboots different from independent physical servers.
- Real Debian guests booted from Nex-created qcow2 disks on each backend. Checks wrote 8 MiB
  using `conv=fsync`, recorded SHA-256, requested a Nex guest reboot, asserted a changed guest
  boot ID and checked the same hash. Nex graceful shutdown was also exercised.
- A separate NFS-backed guest installed Debian from an ISO uploaded through Nex, installed
  GRUB, ejected the ISO through Nex and booted the installed OS. Initial installer input used
  direct VNC. Later real Nex browser-console login and typed commands were visually verified.
- Chromium's eight deterministic storage checks use mocked API responses and are explicitly
  separate from these native checks. Real browser NFS registration/recheck/detach also passed.
- Failure tests used isolated disposable exports/shares/datasets/LUNs, exact resource/source/unit
  checks before mutation, and restoration afterward. Only one fault/API mutation lane ran at a
  time because Core's native operation worker is serialized.

Observed lab versions: Debian 13, kernel 6.12.107+deb13-cloud-amd64, libvirt 11.3.0,
QEMU 10.0.11, systemd 257.13-1~deb13u1, OpenZFS 2.3.9 and open-iscsi 2.1.11.
These are lab-profile results, not physical hardware, arbitrary NAS or multi-host certification.

## Passed and still open

| Area | Actual result at pause | Remaining work |
| --- | --- | --- |
| NFS | Earlier profile passed guest install, guest fsync/reboot, browser console and hard-server-outage recovery. Corrected kernel-FSID profile now registers a distinct export and passes missing-export and bind-replacement rejection/recovery plus root-squash VM-create rejection. | Corrected-profile guest baseline, quota/full rejection, interrupted registration, final host reboot/replacement and enforced-AppArmor hook read. |
| OpenZFS | Final Core-managed bind-alias guest passed fsync/reboot and hash after a Core reset. Isolated missing/renamed/locked/read-only/degraded/quota-full checks rejected use and recovered. | Final graceful Core reboot and Controller replacement on the final deployment; optional module delivery/kernel-upgrade gates remain separate. |
| SMB | Final durable-marker guest passed fsync/reboot and retained hash, boot ID and QEMU PID through a libvirtd restart. Isolated wrong credentials, permissions, missing share, thick rejection and server restart/reconnect passed. | Quota exhaustion, credential-rotation rollback, valid overlapping writer-lock test, final Core reboot/replacement. First writer test was invalid because writer one exited before writer two opened; do not claim a lock refusal. |
| iSCSI | Dedicated LUN guest passed fsync/reboot and hash after Core reset. Wrong WWID/UUID and duplicate claims rejected. Fixed actual portal parser completed a partial detach and fresh registration. | CHAP failure/rollback, link loss during guest fsync, full-filesystem behavior, boot ordering and final reboot/replacement. Undersized admission refusal is not a full-filesystem guest-write test. See open cleanup finding below. |
| Start guard | Adapted packaged hook passed healthy NFS start and missing/tmpfs-replaced mount refusal with agent and Controller stopped. Narrow AppArmor policy install/remove preserved administrator content; libvirtd restart preserved a running SMB guest. | Repeat with the new NFS kernel-table read. Real rebuilt `.deb` install/upgrade lifecycle is unexecuted. |
| Controller replacement | A fresh Controller recovered host/pool/storage IDs, names, sources, generations, credential presence and VM references; NFS stayed running and hash passed. | Repeat for final backend profiles with guest boot ID and hash captured both before and after. First attempt lacked a pre-replacement boot-ID capture. |
| Core reboot | A Core restart required QMP reset after shutdown stalled; NFS/ZFS/iSCSI hashes survived. | Successful graceful Core reboot with corrected fixture boot/shutdown order and saved LIO configuration, then final-profile hashes on all four backends. |

**Pause-audit finding:** an isolated undersized iSCSI registration returned
`insufficient free storage including reserve` but left its session, node and mount behind.
BUG-0090 is fixed and natively re-verified on a disposable undersized LUN: refusal returned
the capacity error and left no session, node, mount unit or ownership receipt. Unit
regressions cover capacity detach, uncertain cleanup, and engine owner-root prepare rollback.
This is still not a full-filesystem guest-write test.

## Corrections already made — do not undo them

- NFS `statfs.f_fsid` was zero for distinct filesystems. New identities use the actual NFSv4
  protocol FSID from `/proc/fs/nfsfs/volumes`, matched to `stat(path).Dev`, and namespaced by
  mounted server address/port. Valid protocol FSID `0:0` is accepted. Old statfs-only records
  remain unavailable without silent identity replacement. Do not weaken duplicate detection.
- CIFS filesystem IDs change across reconnect. New SMB registrations verify a durable
  `.nex-storage-marker`; missing/altered markers fail closed and are never recreated during
  read-only inspection. Old development records remain preserved under strict legacy checks.
- iSCSI `Current Portal` is `host:port,tpgt`, not an invented comma-separated host/port format.
  Ownership receipts belong outside the native node database. One repository owns a target/
  portal session; generated mount paths cannot distinguish duplicate claims.
- A native power error after an attempted effect is uncertain, not proof that a VM failed to
  start. Preserve diagnostics and reconcile the same command without repeating the effect.
- Hard NFS requests can remain pending through an outage and succeed after restoration.
  Unknown access retains its last verified timestamp; require fresh heartbeat and fail-closed
  readability/writability, not a fabricated fresh successful observation.
- Core's systemd namespace requires host-manager-owned mount units. QEMU probes need the
  explicitly retained `CAP_SETUID`. New managed directories need mode `0711` despite umask `0077`.
- AppArmor needs the exact packaged executable `rix` rule; preserve administrator policy.
  A newly installed libvirt hook requires restart of an active libvirtd. The hook ignores
  reconnect, and the daemon restart must preserve running guests. Never disable AppArmor.
- Recheck is read-only: restore the exact owned mount unit/session after fixture repairs;
  do not expect recheck to repair a failed systemd mount automatically.

## What Git transfers, and what it does not

Commit the implementation, tests, docs and this handover through the normal owner-controlled Git
workflow. **Do not force-add `.cache/`, `dist/`, passwords, keys, raw inventories, screenshots,
qcow2 images or unredacted logs.** Those contain local state, credentials and identifiers.
The handover deliberately uses roles/placeholders rather than private resource IDs.

The office lab's overlay, enrolled Core state, Controller database/CA, native fixture scripts,
raw results and compiled artifacts are ignored and **will not appear in a clone at home**.
A Git-only continuation must create a fresh disposable lab and generate new secrets and IDs.
Existing committed QA establishes what ran previously; it does not establish a run on the new machine.

Reusable committed tools:

- `tests/integration/storage_repositories_lab_boot.py`: creates the private Debian overlay,
  key, QMP socket and SSH forwarding; it does not finish service installation or enrollment.
- `tests/integration/storage_repositories_guest.py`: real guest write/reboot/hash verification.
- `tests/integration/storage_repositories_native.py`: inspect/heartbeat/recheck and guarded NFS
  outage with watchdog restoration. Pass the actual Controller URL and password file explicitly;
  defaults can refer to an obsolete local endpoint.
- `tests/integration/storage_repositories_browser.py`: deterministic browser regression.

The earlier lab used Controller loopback port 44444 after replacement, although runner defaults
are 44443. Do not copy stale IDs or assume either port is a running Controller at home.

## Resume sequence on the home machine

1. Read this handover and inspect `git status`; preserve other work. Use Terra 5.6 medium workers
   for independent code review, lab ownership and documentation, not concurrent fault injection.
2. Build one matching source/binary/UI snapshot:

   ```sh
   make build
   go test -race ./internal/vmhost ./internal/agent ./internal/protocol -count=1
   python3 -m unittest tests/test_api_contract.py -q
   ```

3. Prepare a new disposable lab with KVM/nested virtualization, QEMU, SSH, Docker and enough
   memory/disk. The boot helper expects the verified Debian cloud image at its `BASE` path;
   use the pinned image/checksum procedure in
   [the existing appliance QA](../qa/controller-desktop-rebuild/checks.md).
   Build its existing helper images if absent, then boot:

   ```sh
   docker build -t nex-appliance-builder appliance
   docker build -t nex-iso-builder installer
   python3 tests/integration/storage_repositories_lab_boot.py --prepare
   ```

   Retain the printed lab path privately. Install libvirt/QEMU, NFS, CIFS/Samba, OpenZFS and
   open-iscsi/LIO fixture prerequisites only in that disposable guest. The full storage fixture
   setup is not yet a one-command committed bootstrap; recreate isolated file-backed resources
   and record the setup without secrets. No real disks, exports or production targets are authorized.
4. Start the built Controller with an isolated data directory, generated TLS material and private
   password file. See `dist/nex-controller --help` and
   [manual enrollment](../guide/manage-controller.md). Copy matching Core binaries into the guest,
   enroll using a fresh token/CA, and run the agent with the shipped service protections adapted
   to the private lab state path. Keep mount/credential/state paths consistent.
5. Verify actual running executable hashes against the build, including `/proc/<MainPID>/exe`
   inside Core. A temporary `systemd-executor` is not the running agent. Never claim a new
   deployment because only the local build artifact changed.
6. First re-test the iSCSI capacity-failure cleanup fix with an isolated LUN and verify no
   session, node or mount remains. In a separate lane, create a new NFS registration under the
   corrected identity profile; do not rewrite any older journal identity to make a test pass.
7. Finish the open backend checks in the table, using exact API resource/source/mount-unit
   selection and restoration. Assert specific storage errors, not just any failure: memory
   reserve or an invalid VM payload does not prove disk-capacity rejection. A 512 MiB
   maximum-memory guest was usable without bypassing Core's 2 GiB host reserve.
8. Capture final guest data, complete graceful Core reboot and verify all four hashes. Then
   replace Controller with empty state/new credentials and recover the same Core-owned inventory;
   capture a running guest's boot ID/hash before and after. Eject any external cloud seed ISO
   through Nex before the hook check; unmanaged media is correctly refused.
9. Update QA/guide/bug ledger/change assessment, obtain independent review, and run:

   ```sh
   python3 scripts/check_docs.py --worktree
   python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
   git diff --check
   ```

   Run relevant behavior regressions, not repeated unrelated suites. Do not close acceptance
   while recovery, documentation or an applicable native gate is still unexecuted.

## Verification already run

Whole-tree Go race tests passed seven times during implementation. After the final NFS fix,
focused vmhost/agent/protocol race tests passed (8.877 s / 2.877 s / 1.021 s), and independent
full vmhost review passed. API contract tests passed 21 checks. Web typecheck, unit/build and
eight browser checks passed. Full Python discovery passed 154 tests with no skips in a disposable
Debian builder; later packaging checks passed 38 tests plus installer checks. These are local
results. No hosted CI run, release deployment or multi-host certification is claimed.

Last verified running source snapshot: `90c535fa73fd434c8ba1b056c68fbbf994f4697f47d0af706c6be7e549453a4d`.
Core agent: `fbe91894d8b0237da820a07bb459ef5ccabe6ae2181b32e4cf723315bdb37ace`.
Controller: `211e4c17717162de6429677bc7c7ca53c833e76c6a431cc0e0f7d5431b48165d`.
Core-local: `8e6a0c3e44b786fa10eb92d8fbf5e7d60509b19333f3d04d87f4c0536ef7c400`.

A later real Core-local `.deb` build completed and its contents were inspected, but it was not
installed. That ignored artifact and its signed appliance inputs do not transfer through Git;
rebuild through `packaging/core-local/build.py` when testing real install/upgrade lifecycle.
Optional ZFS redistribution, Secure Boot and kernel-upgrade/rollback remain separate delivery gates.

API impact: existing contract for this handover; it documents the implemented changed storage
contract without introducing an endpoint. Guide impact: none for the handover itself; operator
behavior is documented in the linked guide. Durable state owner: none for this contributor
handover; product storage state remains on Core as described above.
