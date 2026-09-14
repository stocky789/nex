# VM storage repository checks

Date: 2026-09-11. Development working tree; implementation acceptance is open. See the
[handover](../../plans/2026-09-11-storage-handover.md). Gates live in the
[implementation plan](../../plans/2026-09-11-vm-storage-repositories.md); bug root causes and
fixes live in the [bug ledger](../../BUG_LEDGER.md).

## Environment and scope

Disposable Debian 13 KVM guest with a private writable overlay and local Controller; no
production disks, exports or targets. Observed versions: kernel 6.12.107+deb13-cloud-amd64,
libvirt 11.3.0, QEMU 10.0.11, OpenZFS 2.3.9 (DKMS, inside the guest), open-iscsi 2.1.11,
systemd 257.13-1~deb13u1. These identify the disposable profile, not certification of other
servers, kernels or physical hardware.

## Executed checks

| Check | Result |
| --- | --- |
| Controller, agent, protocol, vmhost focused suites | Passed after control-path corrections |
| Whole-tree `go test -race ./...` | Passed seven times; latest covers periodic Core recovery and Controller task projection (Controller 141.707 s, vmhost 8.849 s) |
| `go test -race ./internal/vmhost ./internal/agent ./internal/protocol -count=1` | Passed after parser/ownership corrections (9.429/2.974/1.021 s) and after NFS kernel-identity correction (8.877/2.877/1.021 s) |
| Web typecheck, unit, production build | Passed; root rerun passed |
| Full Python unittest discovery | 154 passed, no skips, 4.596 s, in a removed Debian 13 builder container (read-only checkout copy); three socket ResourceWarnings did not fail |
| Packaging after hook correction | 38 focused storage/agent-service/source-archive/Core-package/distribution tests and `bash installer/check.sh` passed |
| Chromium storage regression (`tests/integration/storage_repositories_browser.py`, deterministic API, not real storage) | Eight checks passed: empty state, stale-host exclusion, four backend forms and transient credentials/cleanup, recheck failure/retry, typed detach, VM eligibility/mobile layout, expired-session sign-in. Worker inspected captures; root inspected mobile VM picker and native detach capture |

The browser check runs in both GitHub and Gitea phase1 workflows; Go regressions via
`go test -race ./...` and Python packaging via unittest discovery run in both. No hosted CI run
was requested.

### Native backend results

| Backend | Result | Limits |
| --- | --- | --- |
| NFS | Login/overview, registration, Core-ID adoption, identity/capability/capacity, recheck passed ([BUG-0082](../../BUG_LEDGER.md)). VM creation and Core power start/stop/start passed after child-directory validation fix. Stopped-image `qemu-io` write/read passed. Cloud guest wrote a proof file, read it after Core-API reboot. Browser recheck and typed detach passed; the other NFS repository stayed healthy | `qemu-io` does not establish guest writes or flush durability; no server-outage flush guarantee |
| NFS installed guest | Debian 13 installed from an ISO uploaded through Nex (input via direct VNC), GRUB, media ejected through Nex, booted to login. 8 MiB `conv=fsync`, SHA-256, Nex reboot with changed boot ID, same hash on `/dev/vda1` | Login key recovered while stopped |
| Browser console | Zero-sized canvas on first load ([BUG-0083](../../BUG_LEDGER.md)); after fix canvas 1280×800 shows login prompt, no page errors; login and Type into VM produced visible proof line, kernel version and prompt. Worker and root inspected secret-free captures | |
| OpenZFS | Existing file-vdev dataset registered. Guest wrote 8 MiB fsynced, verified SHA-256, Core-API reboot changed boot ID, same hash. Final profile: separate dataset via Core-managed bind alias, product-created `0711` directory, same write/hash/reboot checks, graceful Nex shutdown; hash retained after the Core reset below and after agent restart. Unmounted dataset (VM stopped): start refused, reporting stayed fresh, dataset restored | Earlier direct-path fixture preserved; not evidence of packaged writable access outside the alias. Not a Core host reboot test |
| SMB3 | Manual authenticated fixture write passed. Registration, recheck, VM creation, stopped-image QEMU I/O passed after CIFS ownership-mapping fix. Credential rotation hit duplicate `findmnt` rows after restart ([BUG-0082](../../BUG_LEDGER.md)); Core periodic recovery then reconciled the original uncertain update to `succeeded`, generation 2, healthy; rebuilt Controller shows the original task succeeded. Guest 8 MiB fsync, SHA-256, Nex reboot with changed boot ID and same hash, graceful shutdown | Core host reboot and server interruption separate |
| iSCSI | Manual fixture write passed. Debian node-file assumptions, repeated-login exit 15 and scanned-directory ownership sidecar (exit 6) found and fixed ([BUG-0082](../../BUG_LEDGER.md)). Registration passed preserving/migrating prior ownership state; host and agent agreed on filesystem UUID, source device, type, target, writable mount. After detach and LUN enlargement Core rejected the fixture's stale UUID; recaptured identity registered and created a VM. Guest 8 MiB fsync, SHA-256, Nex reboot with changed boot ID and same hash, graceful shutdown. Initial start hit the packaged umask directory-mode bug ([BUG-0082](../../BUG_LEDGER.md)); fixture traversal corrected for this run; final ZFS guest later started from a fresh `0711` directory with no fixture correction | Successful fixture login alone not treated as acceptance |

A nameless detach tombstone initially made Controller reject all host reports
([BUG-0082](../../BUG_LEDGER.md)); after the fix reports are fresh.

## Core restart checks in progress

- First Core restart stalled shutting down the co-located NFS guest and iSCSI service; a QMP
  reset was required. The changed Core boot ID proves a restart, not a graceful shutdown.
  Installed NFS guest, final ZFS alias guest and iSCSI guest then started through Nex, kept
  their SHA-256 data, and shut down through Nex.
- iSCSI fixture had persisted an obsolete 2 GiB LIO configuration for an 8 GiB filesystem.
  Recovery restored the original backing file and target identity, recreated only the initiator
  session, and restarted the existing Core-owned UUID mount unit after source verification.
  Core correctly refused the stale device and absent mount. No filesystem resized, formatted or
  repaired; no repository identity changed.
- SMB refused startup: CIFS filesystem ID changed across restart
  ([BUG-0085](../../BUG_LEDGER.md); [Linux CIFS source](https://raw.githubusercontent.com/torvalds/linux/v6.12/fs/smb/client/cifsfs.c)).
  A fresh marker-backed repository and Nex-created 4 GiB VM passed 8 MiB fsync, SHA-256, Nex
  reboot with changed boot ID and same hash, media eject, graceful shutdown. Earlier fixture and
  journal preserved; graceful Core reboot and server interruption still open.
- The outage showed a start reported failed after libvirt had started the VM
  ([BUG-0084](../../BUG_LEDGER.md)); deterministic cancellation/replay regressions passed
  independent race review.

## Current matching deployment

Root verified running executables against build artifacts and compared affected sources with
the extracted build snapshot. An earlier deployment retained the old Core binary; its results
are excluded. A temporary systemd executor process was not counted as a running agent.

| Artifact | SHA-256 |
| --- | --- |
| Source archive | `90c535fa73fd434c8ba1b056c68fbbf994f4697f47d0af706c6be7e549453a4d` |
| Running Controller | `211e4c17717162de6429677bc7c7ca53c833e76c6a431cc0e0f7d5431b48165d` |
| Running Core agent | `fbe91894d8b0237da820a07bb459ef5ccabe6ae2181b32e4cf723315bdb37ace` |

| Check | Result | Limits |
| --- | --- | --- |
| Controller replacement | Fresh Controller recovered host/pool, storage identities/names/sources/generations, credential presence and VM disk references from Core. Installed NFS guest kept running; saved hash passed | No pre-replacement boot-ID capture; exact boot-ID continuity not claimed |
| NFS outage (corrected endpoint) | Service inactive, heartbeat advanced, access unknown/non-readable/non-writable, recheck pending through 20 s observation (hard-mount wait expected). Restore: same operation and Core journal succeeded, fresh recheck healthy/readable/writable; watchdog cancelled. Unknown access retains last verified time | No immediate healthy pre-outage sample; runner now captures it. An earlier test wrongly required terminal failure while offline |
| ZFS faults (mirrored encrypted file-vdev) | Missing dataset, renamed mount path, locked key, read-only dataset, degraded pool rejected; recheck succeeded after each restore. Filled to zero bytes: VM creation rejected at QEMU access probe before any disk file; removing fill file and restoring quota recovered. Deployed-build missing-dataset recheck failed; restore rechecked successfully | |
| iSCSI parser | Actual Debian `Current Portal: host:port,tpgt` handled ([BUG-0087](../../BUG_LEDGER.md)); Core completed a partial detach, fresh registration remounted the exact ext4 LUN with matching WWID/UUID and active session; root verified both journal results | CHAP, capacity and link-loss-during-write gates open |

## Packaged service checks

| Check | Result | Limits |
| --- | --- | --- |
| Shipped systemd protections | NFS/SMB usable; iSCSI needed writable initiator state/lock paths (added). Private mount namespace requires host systemd mount units ([systemd.exec](https://raw.githubusercontent.com/systemd/systemd/main/man/systemd.exec.xml)); iSCSI host/agent namespace visibility passed | Full packaged recovery under test |
| `CAP_SETUID` ([BUG-0093](../../BUG_LEDGER.md)) | `runuser` `setuid` returned `EPERM`; A/B with only `AmbientCapabilities=CAP_SETUID` made NFS recheck pass. Lab then installed the declaration from the shipped unit and removed temporary overrides | Cause of implicit startup capability loss not established; transient approximations did not reproduce. Build/source hashes only in ignored local evidence |
| libvirt `qemu.d` hook / AppArmor ([BUG-0088](../../BUG_LEDGER.md)) | Temporary exact `rix` rule: healthy NFS start, no new denial; ACPI shutdown did not finish, VM force-stopped; policy restored. Adapted lifecycle: idempotent admin-policy preservation, healthy NFS start, missing/tmpfs-replaced mount refused with agent and Controller stopped; removal preserved admin rule. Running SMB guest kept SHA-256, boot ID and QEMU PID across libvirtd restart | Packaged helper and equivalent native commands, not a rebuilt `.deb` or full postinst. Final NFS-profile confinement pending ([libvirt hooks](https://www.libvirt.org/hooks.html), [libvirtd](https://libvirt.org/manpages/libvirtd.html)) |
| NFS identity ([BUG-0089](../../BUG_LEDGER.md)) | On corrected deployment a distinct ZFS-backed export registered with nonzero protocol FSID; missing export and bind replacement rejected and recovered; root-squash VM creation failed at QEMU access probe before any disk or domain | Earlier NFS guest/outage evidence does not cover the corrected identity contract. Final-profile guest persistence, quota/interrupted-registration, replacement open |

## Review and remaining gates

Independent re-reviews passed: downgrade guard (APT candidates, protected removal,
credential-only state without journal directory, manual-dpkg limitation); control path
(credential offer/ACK/recovery, journal projection, persistent identities/capabilities,
tombstones); corrected native batch (scoped iSCSI ownership/detach, verified reconnect, SMB
rollback, duplicate mount rows). Full vmhost, focused Controller/protocol lifecycle checks and
the earlier four-backend guest baselines pass. Storage probes verify the QEMU account within
bounded workers.

Open: corrected NFS identity final guest baseline; all backend failure, guest durability and
reboot gates not recorded here. Simulation does not prove storage durability, kernel recovery,
physical hardware or multi-host writer exclusion. OpenZFS redistribution, Secure Boot and kernel
upgrade recovery are separate delivery gates.

## Pause audit

| Check | Result |
| --- | --- |
| SMB wrong credential, permission, missing share | Failed as expected, recovered |
| Thick provisioning | Rejected (unsupported) |
| SMB server restart/reconnect | Rechecked successfully |
| Concurrent writer | Invalid attempt: writer one exited before writer two opened |
| Quota, credential-rotation rollback, valid simultaneous-writer refusal | Unexecuted; share restored, no writer left |
| Undersized iSCSI ([BUG-0090](../../BUG_LEDGER.md)) | Original run left session, node, mount; lab owner removed only that fixture, valid repository preserved. Re-verification on fresh 200 MiB LUN failed with `insufficient free storage including reserve` and left no session, node, mount unit or receipt. Not a full-filesystem guest-write test |

## Continuation lab (home machine)

Fresh disposable Debian 13 Core overlay with matching rebuilt Controller/agent hashes. After
BUG-0090 native verification, corrected-profile repositories registered for NFSv4 (nonzero
protocol FSID), local OpenZFS dataset, SMB3 and an adequate iSCSI LUN. Guest fsync/reboot
baselines, remaining fault gates, graceful Core reboot and Controller replacement are open here.

At pause: all guests shut off, no pending Controller operations, lab Controller and agent
healthy, no temporary package builder left. A Core-local package was built and inspected;
install/upgrade not exercised.
