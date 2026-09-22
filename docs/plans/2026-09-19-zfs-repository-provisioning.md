# ZFS repository provisioning: pick disks, Core does the rest

Status: implemented in the working tree with bounded native, browser, final-internal-ISO
and kernel-upgrade evidence. Phase 4 master transfer and external distribution acceptance
remain open. This extends the [storage repositories plan](2026-09-11-vm-storage-repositories.md).
Its original ZFS boundary admitted only an existing imported and mounted dataset; this slice
adds Core-owned zpool creation from explicitly selected devices.

## Decisions

Decision: project owner, 2026-09-19.

- A ZFS repository has a scope: **every host in the pool** or **one selected host**.
- The operator chooses physical devices; Core chooses and applies everything else.
- The wizard offers blank devices directly and devices holding old data only behind a typed
  erase confirmation. System, mounted and in-use devices are never offered.
- **OpenZFS is built into the Nex Core distribution.** This supersedes "OpenZFS remains an
  optional host prerequisite" in the storage plan. [Licensing](../LICENSING.md) still records
  exact-package notices and source for OpenZFS components. DKMS build, offline installation
  and known-good retained-kernel boot evidence passed in disposable VMs.

## Problem

Registration needs a zpool built over SSH and three typed fields (pool, dataset, mount
path). A repository is bound to one host. Core's `disks` inventory reports only a name and
size, so a safe device picker cannot be built from it.

## Operator workflow

Storage → Add storage → ZFS.

1. **Scope.** "Every host in the pool" or "One host" (select it).
2. **Disks.** Per host, Core lists devices with model, serial, size, rotational/SSD and a
   stable `/dev/disk/by-id` name. Blank devices are selectable. Devices with an old partition
   table or filesystem are selectable with a typed confirmation naming the device. The system
   disk and anything mounted, or a member of md, LVM or an imported zpool, is shown disabled
   with its reason.
3. **Name.** Prefilled. The confirmation screen lists exactly which devices are erased on
   which hosts.

Core-selected defaults, with one layout override:

| Devices on a host | Layout | Note |
|---|---|---|
| 1 | single | Warn: no redundancy |
| 2 | mirror | |
| 3 or more | raidz1 | Override: striped mirrors (even count), raidz2 (4 or more) |

Pool name `nex-<repository id>`, `ashift=12`, `compression=lz4`, `atime=off`,
`xattr=sa`, by-id vdev paths, one `vms` filesystem dataset holding qcow2 files and mounted
directly beneath Core's storage root. Never `sync=disabled`. A Core-owned mountpoint removes
the bind-mount alias that arbitrary dataset mountpoints need. Thick provisioning stays
refused for ZFS until reservation behaviour is validated, as the storage plan already says.

"Use an existing dataset" remains as an advanced path, filled from discovery, unchanged in
behaviour.

## Pool-wide is local-on-every-host

Each host gets an independent zpool under one repository identity and name. It is not shared
storage: a VM's disks exist on one host, capabilities report `shared: false`, and moving the
VM requires a storage copy (a later migration feature). The UI labels the scope "Local on
every host" and shows per-host status and capacity through per-host ZFS access observations
(`zfs_host_access`).

Hosts differ, so the wizard shows one device picker per host. A host without a spare device
is left "not configured" and is not eligible for that repository. A host that joins the pool
later shows "needs disks" until the operator selects devices for it. Core never erases a
device nobody selected, and never infers a device from another host's selection.

## Architecture (Core first)

1. **Core behaviour.** Device observation (`lsblk --json`, by-id links, zpool membership);
   provisioning (`wipefs`/label clear only for confirmed devices, `zpool create`,
   `zfs create`); admission reuses the existing `inspectZFS` identity and health checks.
2. **Durable state.** `state.owner: core`. The pool master's managed journal holds the
   pool-scoped record: identity, name, scope, layout and, per host, the selected by-id devices
   and the observed pool/dataset GUIDs. Each host's pool root filesystem carries the ZFS user property
   `nex:repository=<id>`, and its own journal holds its applied record. A fresh Controller
   recovers the repository from master inventory exactly as other repositories.
3. **Host API.** New contract, capability-gated (`storage-zfs-provision-v1`):
   - device discovery: extend `storage-discovery-v1` with a device-listing kind returning
     operator-safe device metadata and an eligibility reason; transient, never persisted;
   - provision: a durable master operation carrying scope, layout and per-host devices with
     their erase confirmations. The master runs it locally or forwards it to each member over
     the authenticated peer channel used for shared-storage reconciliation and records each
     host's result separately;
   - later "configure this host" for a pool-scoped repository reuses the same operation.
   Administrator session and CSRF at the Controller; pool-master peer authentication at
   members. Core revalidates every device at execution time: the confirmed by-id name,
   size, serial and discovery fingerprint (including observed signatures and topology)
   must still match, and eligibility is recomputed, not trusted from the request. Old Cores lacking the capability are refused with a clear reason.
4. **Outage and recovery.** Retries key on the operation ID. After an interruption Core
   inspects before acting: a zpool carrying this repository's property is adopted; confirmed
   devices that are still exactly as observed are created; anything else is
   `needs_attention` and never erased again. A partially successful pool-wide operation
   reports per-host outcomes; succeeded hosts are usable. Controller loss mid-operation does
   not stop the master.
5. **Controller UI and guide.** The three-step wizard above, per-host status, and
   `docs/guide/storage-repositories.md` updated in the same slice.

Detach keeps today's rule: it unregisters and never destroys the zpool. Destroying data is a
separate, explicitly confirmed operation and is out of this plan.

API impact: **new contract** — device discovery kind and ZFS provision operation
(`docs/API.md` and the machine-readable contract change in the implementing slices).

## Implemented slices and remaining gates

1. **Distribution — verified in the disposable lab.** OpenZFS packages are in the Core image;
   DKMS build/module load passed after offline install and reboot, and a separate native
   package upgrade booted both the upgraded and retained kernels. Licensing and Secure Boot
   distribution evidence remain outstanding.
2. **Device discovery — implemented and checked.** Core observation, API and contract checks
   cover authorization, invalid input and old-Core capability refusal.
3. **Single-host provision — implemented and checked.** The Core journal, API, wizard and
   guide cover VM lifecycle, interruption/retry, device refusal, reboot, degraded health and
   Controller replacement in the bounded disposable-host run.
4. **Pool-wide scope — implemented with bounded evidence.** Per-host pickers/outcomes,
   offline-member recovery and later-host configuration passed. Master-change record survival
   remains open because the authority-transfer capability is not implemented.

Each slice is proven through the host API before its UI counts, on disposable lab hosts with
disposable extra virtual disks only. No real disks are erased by this work.

### Open gates found in review, 2026-09-19

These are unresolved and block nothing else from being marked complete in their place.

- **A DKMS build failure aborts the whole installation.** `zfs-dkms` is installed with the rest
  of `installer/packages.txt` in one `apt` call inside `installer/late-install.sh`, which runs
  under `set -eu`. An in-target module build failure therefore fails Core installation on any
  host, including hosts that will never use ZFS. No fault injection has been run for a failed
  in-target build or a failed module rebuild during a later kernel upgrade.
- **No Secure Boot module-signing path.** Nothing in the image or installer enrolls a machine
  owner key or signs the built module. On a host with Secure Boot enabled the ZFS module will
  not load, so an existing pool is not imported at boot, and there is no boot-time capability
  signal that distinguishes this from a broken pool.
- **Pool network and media convergence is no longer add-only** (2026-09-19): the master removes and
  renames pool VLAN networks and removes imported ISOs, and members converge. What remains open
  there (no native two-host walkthrough, ejected-member cleanup, older members, unpruned pool VM
  owner records) is listed in the
  [pool master model plan](2026-09-09-pool-master-model.md#not-in-this-change-planned-phase-4).
- **A repository with a permanently failed host cannot be detached.** Lifecycle operations
  require every configured host to hold a successful provisioning outcome, and a terminal
  needs-attention outcome is deliberately never retried, so such a repository has no API removal
  path. Allowing a detach, which never touches a zpool, for a terminally failed host needs a
  project-owner decision.
- **Older hosts receive OpenZFS one update late, unverified.** The Core package recommends the
  OpenZFS packages and enables `contrib` on upgrade; no real upgrade from an earlier release has
  been run, and apt's handling of an unsatisfiable recommendation is reasoned, not observed.
- **This slice's QA captures are not reviewable from the repository.** All of them live under
  the gitignored `.cache/` directory, so the written reports are the only record a reviewer can
  inspect here.

## Out of scope

zvol/raw block VM disks, native ZFS snapshots and replication, encryption-key management,
adding or replacing vdevs, pool destroy, and cross-host VM moves between local zpools.

## Execution and acceptance record

Implementation completed in the working tree on 2026-09-19. The runtime API impact is **new contract** and
the guide impact is **updated**. All provisioning intent and outcomes belong to Core;
Controller remains a request relay and inventory projection.

Core discovery/provisioning, durable per-host reconciliation, Controller/API integration,
UI and guide are implemented. Native disposable-host checks passed VM lifecycle,
interrupted provisioning, device refusal, degraded health, reboot, offline-member
recovery, later host configuration, Controller replacement, rename/recheck and
non-destructive detach. The final internal ISO passed offline installation and both
ZFS boot checks; a separate disposable image passed native kernel package upgrade
and GRUB boot of the upgraded and retained kernels. Real and simulated browser checks passed. Exact commands, artifact
hashes, regression results and remaining acceptance limits are in the
[QA record](../qa/2026-09-19-zfs-provisioning.md).

The existing Phase 4 plan still marks master authority transfer and member replicas as
unimplemented. The master-change acceptance check above therefore depends on that
separate capability; preserving records on the current master is not evidence of a
successful handover. The recovery design was resolved with the project owner on 2026-09-19:
retain the pool/resource identities, start with operator-selected promotion and verified
fencing, and automatically rejoin the old host only after Core reconciles authority and
VM ownership. VM restart/return follows the existing recovery and HA policies. See the
[canonical authority-transfer decisions](2026-09-08-nex-platform.md#pool-authority-transfer-and-master-succession).
This records the agreed design; authority-transfer implementation and the ZFS master-change
acceptance check remain outstanding.

Acceptance must distinguish runnable simulated command/transport checks, browser checks,
native disposable-host checks and exact-image delivery checks. No real disks or existing
operator hosts are targets of this rollout.

Native implementation clarification, 2026-09-19: OpenZFS user properties belong to
filesystem datasets, not zpool properties. Create the repository marker atomically with
`zpool create -O nex:repository=<id>` on the pool's root dataset and read it with
`zfs get`. The disposable native run rejected the initial `-o` pool-property form before
creating a pool. This corrects the mechanism without changing ownership or recovery intent;
see [OpenZFS zpool-create](https://openzfs.github.io/openzfs-docs/man/master/8/zpool-create.8.html).

Implementation scope clarification: the Core-created `vms` dataset advertises VM-disk
capability only. The existing media import contract targets one registration host and
cannot select a member of a master-owned local-ZFS aggregate. Do not advertise ISO
capability until that flow can select and validate the actual storage host. Advanced
existing-dataset registration retains its existing media behavior. This slice does not
add a second media placement contract.
