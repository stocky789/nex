# VM storage repository checks

Date: 2026-09-11. Development working tree; implementation acceptance is open. Work is paused
for continuation on another machine; see the [handover](../../plans/2026-09-11-storage-handover.md).

## Environment and scope

Terra 5.6 medium workers implemented and independently reviewed the single-host contracts,
Core backends, Controller orchestration and web workflows. Native testing uses a disposable
Debian 13 KVM guest with a private writable overlay and local Controller. No production disks,
exports or targets are used. Verified build hashes and executed native results appear below;
remaining fault and packaged-start gates are explicit.

## Executed checks

- Controller, agent, protocol and vmhost focused suites pass after control-path corrections.
- Whole-tree `go test -race ./...` passed seven times; the latest run covers the periodic Core
  recovery and direct Controller task-projection changes (Controller 141.707 s, vmhost 8.849 s).
  The later native parser and ownership corrections passed
  `go test -race ./internal/vmhost ./internal/agent ./internal/protocol -count=1`
  (9.429 s, 2.974 s and 1.021 s respectively). The subsequent NFS kernel-identity
  correction passed the same race command (8.877 s, 2.877 s and 1.021 s).
- Web typecheck, unit checks and production build pass. A subsequent root typecheck/unit rerun also passed.
- Final full Python unittest discovery passed 154 tests in a disposable Debian 13 builder container
  with nginx, git, systemd and GnuPG installed only inside that container. The checkout was
  mounted read-only and copied into the removed container; no workstation services were installed.
  The final run excluded cache, build outputs and dependency directories, reported no skips,
  and completed in 4.596 s. Three socket ResourceWarnings did not fail the run.
- After the hook packaging correction, 38 focused storage, agent-service, source-archive,
  Core-package and distribution packaging tests passed, along with `bash installer/check.sh`.
- Chromium storage regression passed eight checks, including credential cleanup: empty state, stale-host exclusion, all four
  backend forms and transient credentials, recheck failure/retry, typed detach confirmation,
  VM eligibility/mobile layout, and expired-session sign-in. This uses the real built UI with
  deterministic API responses, not real storage. Captures were inspected by the browser worker;
  root inspected the mobile VM picker and actual native detach capture.
- Actual local Controller login and storage overview were inspected. Native NFS registration,
  Core-generated ID adoption, identity/capability/capacity reporting and recheck passed.
- NFS VM creation and Core power start/stop/start passed after correcting child-directory
  validation below a server-owned mount leaf. A stopped-image `qemu-io` pattern write/read
  passed on its NFS qcow2 disk; this does not establish installed-guest writes or flush durability.
- Optional OpenZFS 2.3.9 DKMS built and loaded inside the disposable Debian guest; an existing
  file-vdev dataset registered successfully through Core. Manual authenticated SMB3 and iSCSI
  filesystem fixture writes passed; they do not establish Nex VM workflows.
- A real Linux cloud guest on the NFS repository wrote a proof file and read it after a
  reboot requested through the Core power API. This is guest-OS I/O; it does not claim an
  installer walkthrough or a server-outage flush guarantee.
- A nameless detach tombstone initially caused Controller to reject all host reports; the
  validator and regression were corrected, and actual reports are fresh again.
- The ZFS-backed Debian 13 cloud guest (kernel 6.12.107+deb13-cloud-amd64) wrote 8 MiB
  with `conv=fsync`, verified SHA-256, rebooted through the Core API, changed guest boot ID,
  and verified the same hash. The final alias profile also retained its hash after the Core reset described below.
- Final ZFS profile: a separate existing dataset registered through the Core-managed bind
  alias and held a Nex-created VM with a product-created `0711` directory. That guest passed
  8 MiB fsynced writes, SHA verification, Nex reboot with changed boot ID and post-reboot hash,
  then graceful Nex shutdown. The earlier direct-path prototype fixture remains preserved;
  it is not evidence of packaged writable access outside the managed alias.
- Actual browser NFS recheck and typed detach passed after report validation was repaired;
  the selected repository disappeared while the existing NFS VM repository stayed healthy.
- The ZFS guest retained its hash after an agent service restart. With its dataset deliberately
  unmounted while the VM was stopped, start was refused and host reporting stayed fresh;
  the dataset was then restored. This is not a Core host reboot test.
- SMB registration, recheck, VM creation and stopped-image QEMU I/O passed after correcting
  CIFS ownership mapping. Credential rotation initially exposed a source-verification failure.
  Diagnosis found duplicate identical `findmnt` rows in the packaged agent namespace after
  restart; the parser correction has regression coverage and passed the native recovery below.
  With the corrected native path and explicit probe capability, Core's periodic recovery
  subsequently reconciled the original uncertain SMB update to `succeeded`, generation 2,
  and healthy access. Updating the original Controller task projection was separately fixed
  and tested; the matching rebuilt lab Controller now reports the original task as succeeded.
  The SMB-backed Debian guest subsequently wrote 8 MiB with `conv=fsync`, verified SHA-256,
  rebooted through Nex with a changed boot ID and verified the hash again, then shut down
  gracefully through Nex. Core host reboot and server interruption remain separate gates.
- Actual Debian iSCSI testing found incompatible node-file assumptions, repeated login against
  an existing session (exit 15), and an ownership sidecar incorrectly placed inside the directory
  scanned by `iscsiadm` (database failure, exit 6). Those issues were corrected and retested;
  successful fixture login alone was not treated as repository acceptance.
  A subsequent Nex registration passed while preserving and migrating its prior verified
  ownership state. Native login handles only session-exists exit 15 with immediate exact-session
  confirmation; strict LUN identity checks still apply. Host and agent showed the same
  filesystem UUID, source device, type, target and writable mount state.
  After clean detach and enlargement of the disposable LUN, Core correctly rejected a stale
  filesystem UUID retained by the fixture script. Recapturing the actual LUN identity produced
  a successful registration and Nex VM creation. The iSCSI-backed guest subsequently passed
  8 MiB fsynced writes, SHA verification and Nex reboot with a changed boot ID and matching
  post-reboot hash, followed by graceful Nex shutdown. Its initial start exposed the packaged
  umask directory-mode bug; the fixture traversal mode was corrected for this run, and the
  shared-helper fix now has a subprocess regression. The final ZFS guest subsequently started
  from a fresh product-created `0711` directory without a fixture permission correction.
- A separate NFS VM installed Debian 13 from an ISO uploaded through Nex, installed GRUB,
  ejected media through Nex and booted to the installed system's login prompt. Installation
  input used direct VNC. After recovering the disposable guest's login key while it was stopped,
  the installed system wrote 8 MiB with `conv=fsync`, verified SHA-256, rebooted through Nex,
  changed boot ID and verified the hash on its `/dev/vda1` root filesystem. Observed Nex
  browser-console interaction was subsequently verified separately.
- Actual browser comparison found a connected RFB stream with a zero-sized canvas while direct
  VNC showed the installed guest. Resetting the initial viewport after layout corrected first-load
  Fit mode: the browser canvas is 1280×800 and visibly shows the Debian login prompt with no
  page errors. Subsequent browser login and Type into VM produced a visible proof line, kernel
  version and guest prompt. Worker and root inspected both secret-free captures.

The simulated browser check is `tests/integration/storage_repositories_browser.py` and runs in
both GitHub and Gitea phase1 workflows. Go regression checks run in both via `go test -race ./...`;
Python packaging checks run in both through unittest discovery. No hosted CI run was requested.

## Core restart checks in progress

The first Core restart stalled during shutdown of the co-located NFS guest and iSCSI service
and required a QMP reset of the disposable Core. The changed Core boot ID is evidence of a
host restart, not a successful graceful shutdown. The installed NFS guest, final ZFS alias
guest and iSCSI guest subsequently started through Nex and retained their saved SHA-256 data;
each shut down through Nex afterward.

The iSCSI fixture had persisted an obsolete 2 GiB LIO configuration for an 8 GiB filesystem.
Recovery restored the original backing file and target identity, recreated only the initiator
session to refresh its cached capacity/VPD, and restarted the existing Core-owned UUID mount
unit after verifying its source. Core correctly refused the stale device and absent mount.
No filesystem was resized, formatted or repaired, and no repository identity was changed.

SMB refused startup because the CIFS filesystem identifier changed across restart despite an
unchanged share. Linux builds that identifier partly from server-reported volume creation time;
it is unsuitable as the sole durable repository identity. The new durable marker profile passed
implementation tests and independent review. A fresh marker-backed repository and Nex-created
4 GiB VM passed 8 MiB fsynced guest writes, SHA-256 verification, Nex reboot with a changed
guest boot ID and matching hash, media eject, and graceful Nex shutdown. The earlier fixture
and journal remain preserved; final graceful Core reboot and server interruption are still open.
See the [Linux CIFS implementation](https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/smb/client/cifsfs.c).

The outage also exposed a power outcome defect: libvirt had started the requested VM before
the operation was recorded failed. Post-request errors now become uncertain and preserve
native diagnostics; same-command reconciliation observes state without repeating the action.
Deterministic cancellation and replay regressions passed independent race review.

## Current matching deployment

Root independently verified the running Controller and Core executables against their build
artifacts and compared the affected production sources with the extracted build snapshot.
An earlier deployment incorrectly retained the old Core binary; results from that process
were excluded from final-build acceptance. A temporary systemd executor process was also
not counted as a running agent. Verification waited for the actual agent executable.

| Artifact | SHA-256 |
| --- | --- |
| Source archive | `90c535fa73fd434c8ba1b056c68fbbf994f4697f47d0af706c6be7e549453a4d` |
| Running Controller | `211e4c17717162de6429677bc7c7ca53c833e76c6a431cc0e0f7d5431b48165d` |
| Running Core agent | `fbe91894d8b0237da820a07bb459ef5ccabe6ae2181b32e4cf723315bdb37ace` |

Observed platform versions: Debian 13, kernel 6.12.107+deb13-cloud-amd64, libvirt 11.3.0,
QEMU 10.0.11, OpenZFS 2.3.9 and open-iscsi 2.1.11. These identify the disposable profile,
not certification of other servers, kernels or physical hardware.

The fresh Controller recovered the same host/pool, storage identities/names/sources/generations,
credential presence and VM disk references from Core. The installed NFS guest remained running
and its saved hash passed afterward. The first replacement lacked a retained pre-replacement
guest boot-ID capture; exact boot-ID continuity is not claimed for that attempt.

A corrected-endpoint NFS outage passed the safety and recovery check: the service was inactive,
heartbeat advanced, access was unknown/non-readable/non-writable, and the original recheck
remained pending during the bounded 20-second observation. Restore returned the service to
active; that same operation and its matching Core journal succeeded, followed by a fresh
successful recheck and authenticated healthy/readable/writable access observation. The
recovery watchdog was cancelled. Hard-mount waiting is expected; an earlier test incorrectly
required a terminal failure while offline. Unknown access retains its last verified observation
time. This run did not retain an immediate healthy pre-outage sample; the reusable runner now
captures that transition explicitly.

A separate mirrored encrypted file-vdev ZFS fixture passed missing dataset, renamed mount path,
locked encryption key, read-only dataset and degraded pool rejection, with a successful recheck
after each restoration. Incompressible filling reached zero available bytes; Nex rejected VM
creation at the QEMU access probe before creating any disk files. Removing only the known fill
file and restoring the quota recovered healthy access. A final deployed-build missing-dataset
recheck failed and the restored dataset rechecked successfully.

The final iSCSI parser handled actual Debian `Current Portal: host:port,tpgt` output. In the
isolated fixture, Core completed the previously partial detach, then a fresh registration
remounted the exact ext4 LUN and reported the matching WWID/UUID with an active session. Root
verified both successful Core journal results. This does not establish the remaining CHAP,
capacity and link-loss-during-write gates.

## Packaged service checks

The actual Core agent was switched from a manual process to the shipped systemd protections
in the disposable guest. NFS/SMB remained usable; iSCSI exposed missing writable initiator
state/lock paths, now added narrowly. Core's filesystem protection also implies a private mount
namespace, so mounts must be requested through host systemd mount units rather than created
inside the agent. This follows [systemd's mount-namespace documentation](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.exec.xml);
actual iSCSI host/agent namespace visibility passed. Full packaged recovery remains under test.

A fresh agent recheck also exposed an effective-capability failure: `runuser` could change group
but `setuid` returned `EPERM`. The agent's permitted/effective sets lacked `CAP_SETUID`, although
the bounding set allowed it; a pre-exec wrapper observed the same absence before Nex started.
No file capabilities, user-namespace mismatch or AppArmor denial explained it. A/B testing the
same unit with only `AmbientCapabilities=CAP_SETUID` added made a fresh NFS recheck pass.
The unit now explicitly retains this capability for its QEMU-account probes, keeping
`NoNewPrivileges` and all other hardening. The cause of the implicit startup capability loss
has not been established; isolated transient approximations did not reproduce it.
The lab subsequently installed the declaration directly from the shipped unit and removed
the temporary ambient-capability and tracing overrides. The observed systemd package is
257.13-1~deb13u1 on Debian 13. Matching build/source hashes are retained in ignored local evidence.

The native libvirt `qemu.d` hook was invoked during `prepare/begin`, but Debian's enforced
libvirtd AppArmor profile initially denied execution of the Core-local binary at both the
lab and packaged paths. A temporary exact `/usr/lib/nex/nex-core-local rix,` rule allowed a
healthy NFS-backed start with no new denial; it retains the existing libvirtd confinement.
The disposable VM did not finish the bounded ACPI shutdown and was force-stopped. The policy
and fixture were restored. A later adapted lifecycle run passed idempotent administrator-policy
preservation, healthy NFS start, and missing/tmpfs-replaced mount refusal while Core agent and
Controller were stopped. Removal preserved the administrator rule, and the original policy,
mount and services were restored. This exercised the packaged helper and equivalent native
lifecycle commands, not a rebuilt `.deb` installation or the complete postinst script.
Newly added hooks require daemon activation; systemd unit reload alone does not load them.
The package now restarts only an already-active libvirtd after installing its narrow policy.
The hook ignores reconnect events. A running final-profile SMB guest retained its SHA-256
data, exact boot ID and QEMU PID across that libvirtd restart; the helper also preserved an
administrator policy fixture across idempotent installation and profile reload. Remaining
final NFS-profile confinement checks are pending; [libvirt's hook reference](https://www.libvirt.org/hooks.html) and
[daemon manual](https://libvirt.org/manpages/libvirtd.html) describe the required lifecycle.

A separate NFS export exposed a false duplicate-backing rejection: both client `statfs` IDs
were zero, although one export used a different backing filesystem. The Linux NFS volume table
provides the actual protocol filesystem ID keyed by the mounted device. A strict namespaced
kernel-identity correction passed independent review and focused race tests; earlier NFS guest/outage evidence remains valid for
the earlier profile but does not establish the corrected identity contract. On the corrected deployment, a distinct ZFS-backed NFS export registered with a nonzero
protocol FSID despite the unavailable statfs ID. Missing export and bind replacement were
rejected and recovered; root-squash VM creation failed at the QEMU access probe before any
disk or domain was created. Final-profile guest persistence, quota/interrupted-registration
and replacement tests remain open.

## Review and remaining gates

Independent downgrade-guard re-review passed APT candidate checks, protected removal,
credential-only state with no journal directory, and the documented manual-dpkg limitation.
Independent control-path re-review passed credential offer/ACK/recovery wiring, deterministic
journal projection, persistent source identities/capabilities and tombstones. Native review
identified actual mounted-device identity and credential-rotation issues. The corrected native
batch passed independent re-review, including scoped iSCSI ownership/detach, verified reconnect
and SMB rollback, and duplicate mount-row parsing. Full vmhost and focused Controller/protocol
lifecycle checks and the earlier four-backend guest baselines pass. The corrected NFS identity
profile still needs its final deployed guest baseline; remaining fault/recovery gates stay open.
Storage probes verify the QEMU account
within bounded workers.

All backend failure, guest durability and reboot gates in the
[implementation plan](../../plans/2026-09-11-vm-storage-repositories.md) remain open unless an
executed result is recorded here. Simulation does not prove storage durability, kernel recovery,
physical hardware or multi-host writer exclusion. OpenZFS redistribution, Secure Boot and kernel
upgrade recovery remain separate delivery gates.

## Pause audit

Isolated SMB wrong-credential, permission and missing-share checks failed as expected and
recovered. Unsupported thick provisioning was rejected; server restart/reconnect rechecked
successfully. The concurrent-writer attempt was invalid because writer one exited before
writer two opened. Quota, credential-rotation rollback and valid simultaneous-writer refusal
remain unexecuted. The share was restored and no test writer remained.

An isolated undersized iSCSI registration failed with `insufficient free storage including
reserve`, but the pause audit found its session, node and mount remained. The lab owner
recorded the residue and removed only that transient fixture; the existing valid repository
was preserved. The product cleanup fix and unit regressions are now in-tree: preparation
detaches after readiness or capacity refusal, including uncertain cleanup reporting, and
failed iSCSI prepare rollback keeps the engine owner-root so ownership receipts are removed.
Native disposable-lab re-verification on a fresh 200 MiB LUN failed with
`insufficient free storage including reserve` and left no session, node, mount unit or
ownership receipt. This still does not establish a successful full-filesystem guest-write test.

## Continuation lab (home machine)

A fresh disposable Debian 13 Core overlay was booted with matching rebuilt Controller/agent
hashes. After BUG-0090 native verification, corrected-profile repositories registered
successfully for NFSv4 (nonzero protocol FSID), local OpenZFS dataset, SMB3 and an adequate
iSCSI LUN. Guest fsync/reboot baselines, remaining fault gates, graceful Core reboot and
Controller replacement are still open on this deployment.

At pause, all guest VMs were shut off, no Controller operations were pending, and the lab
Controller and Core agent were healthy. No temporary package builder remained. A real
Core-local package was built and its contents inspected, but install/upgrade was not exercised.
