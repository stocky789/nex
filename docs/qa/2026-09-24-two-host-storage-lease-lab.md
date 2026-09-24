# Native sanlock and libvirt storage-lease lab

Date: 2026-09-24. This is a disposable nested-QEMU simulation for the storage-lease
slice in the [two-host pool fencing plan](../plans/2026-09-22-two-host-pool-fencing.md).
It records native Debian sanlock/libvirt behavior, not physical watchdog or production
storage evidence. The Nex storage-lease acceptance gate remains open.

## Environment and safety

Prepared a fresh `tests/integration/cluster_ha_lab.py` fixture with three Debian 13 Core
guests and one storage guest on its private VDE network. QMP-hotplugged an emulated
i6300ESB watchdog into all Core guests and verified its active sysfs state and 60-second
timeout. SBD stayed inactive throughout; before starting wdmd, there was no other watchdog
owner. The fixture used two new, separate NFS exports: one for sanlock lease files and one
for the shared VM image. Every Core saw matching sentinel/image inode and content hashes.
The lease directory was writable by the Debian sanlock account on all Core guests. No live
hosts or external storage were contacted.

Installed inside the disposable guests only: Debian Trixie `sanlock` and `python3-sanlock`
3.9.5-1, plus `libvirt-daemon-plugin-sanlock`, `libvirt-daemon-system`,
`libvirt-daemon-driver-qemu`, and `libvirt-clients` 11.3.0-3+deb13u3. The native Python
`sanlock.get_hosts()` API supplied host status; no CLI text was parsed as proof.

## Libvirt plugin configuration observed

Configured the system QEMU driver with `lock_manager = "sanlock"` in `qemu.conf`, and
these values in `qemu-sanlock.conf`:

```ini
host_id=101                 # unique per host; the other guests used 102 and 103
auto_disk_leases=1
require_lease_for_disks=0
disk_lease_dir="/var/lib/libvirt/sanlock"
user="sanlock"
group="sanlock"
```

With auto disk leases enabled, libvirt created the `__LIBVIRT__DISKS__` lockspace and one
resource lease named by a hash of each disk's canonical path; the tested domain XML did not
need a per-domain `<lease>` element. Local-only disks also received a lease without changing
their successful start behavior. `require_lease_for_disks=0` kept the plugin from requiring
explicit leases on disks outside its managed shared-lease path. The lease directory must be
accessible to the configured sanlock user and group; an initial root-only NFS directory
failed with `EACCES`, then worked after setting the server-side ownership to the guest's
sanlock UID/GID and mode 0770. NFS operation was demonstrated in this fixture.

For the authority-resource test, initialized distinct 1 MiB files in that dedicated lease
directory: one lockspace file and one `authority` resource file. These were separate from
the repository, VM image, and libvirt disk-lease files; no repository path or offset-zero
lease was used. The three IDs were 101, 102, and 103.

## Results

- **Local-only VM:** a Core guest started a local QCOW2 disk with no explicit `<lease>`.
  `sanlock client status -o s` showed the generated resource lease held by QEMU. Destroying
  the VM released its active lease; the VM definition and test disk were removed.
- **Shared disk duplicate start:** the same shared QCOW2 path was defined on two Core
  guests. While Core 1 ran it, Core 2's start was refused and remained shut off. After Core
  1 stopped it, Core 2 started successfully. This first refusal is consistent with both
  libvirt sanlock and NFS write-lock enforcement, so it is not by itself isolated sanlock
  proof.
- **Authority resource contention:** Core 1 held the dedicated sanlock `authority`
  resource. Core 2's acquisition returned `acquire done -243` and did not become an owner.
  After the known Core 1 holder exited, Core 2 acquired and released the same resource.
  This isolated the native sanlock exclusive-resource contention path.
- **Watchdog failure and exact dead status:** with only a throwaway VM holding the shared
  disk lease on Core 3, froze the two recorded sanlock daemon processes. wdmd's log showed
  failed sanlock renewals and a countdown to watchdog expiry; the emulated watchdog reset
  the guest and its boot ID changed. SBD remained inactive. Core 1's native host-status
  query first reported host ID 103, generation 1, `HOST_FAIL` (4), and only later
  `HOST_DEAD` (5), still generation 1. During `HOST_FAIL`, no peer retry was attempted.
  Only after the exact `HOST_DEAD` result was asserted did Core 1 start the shared-disk VM;
  its lease version advanced from 3 to 4. This is simulated QEMU watchdog behavior.
- **Free is not dead; generation changes on rejoin:** orderly `rem_lockspace` of host ID
  102 in the separate authority lockspace produced `HOST_FREE` (2), timestamp zero, and
  generation 1 after the sanlock bitmap expiry interval. Rejoining that host ID then
  produced `HOST_LIVE` (3) with generation 2. The independent libvirt lockspace was not
  removed for this check. Thus `FREE` is not a substitute for `DEAD`, and old-generation
  observations cannot be reused after a rejoin.
- **Cleanup:** destroyed and undefined test VMs, removed custom lockspace memberships from
  the active guests, then ran the fixture's ownership-checked cleanup. All four recorded
  QEMU processes stopped; no fixture QEMU or VDE process remained. Fixture artifacts remain
  in the ignored local cache only. Packages and configuration existed only inside discarded
  guest overlays.

The observed status flags were Debian `python3-sanlock` constants:
`HOST_UNKNOWN=1`, `HOST_FREE=2`, `HOST_LIVE=3`, `HOST_FAIL=4`, `HOST_DEAD=5`. Observed
lockspace records included host ID, generation, timestamp, and `io_timeout=10`. The status
transition took longer than the watchdog reset itself: reset alone did not qualify as
exclusion; Core 1 continued to reject action while the status was `HOST_FAIL`.

## Limits and acceptance

This validates packaged libvirt/sanlock behavior in a disposable Debian nested-guest
simulation. It does not prove real watchdog hardware, production NFS server behavior under
network partitions, lease-file persistence/durability on target storage, Nex Core API
integration, durable membership adoption, promotion reconciliation, authorization, or
controller-disconnection recovery. An unavailable-storage operation and a production
failure matrix were not exercised. The Nex runtime must consume status for the exact
lockspace, host ID, owner/fencing generation and operation, preserve monotonic continuity,
and refuse `FREE`, `FAIL`, unknown, stale, unavailable, or mismatched evidence. The broader
HA acceptance gate remains open.

## Explicit leases across two repositories

Date: 2026-09-24. This additional trial reused a fresh disposable fixture at
`tests/integration/cluster_ha_lab.py` and ran Debian 13.6 guests. Core 1 and Core 2 each ran
sanlock 3.9.5-1, libvirt 11.3.0-3+deb13u3 and QEMU 10.0.13+ds-0+deb13u1. It tested explicit
libvirt `<lease>` entries, with `auto_disk_leases=0`, against two separate NFSv4 exports from
the fixture's private storage guest. Both exports used `rw,sync,no_subtree_check,no_root_squash`;
sanlock-owned lease files were mode 0660. Core 1 and Core 2 used sanlock host IDs 1 and 2.
The guests' watchdog was software-emulated; this trial did not test fencing or reset behavior.

The per-host QEMU configuration used `lock_manager = "sanlock"`. `qemu-sanlock.conf` set the
host-specific `host_id`, `auto_disk_leases=0`, `require_lease_for_disks=1`, and `user="sanlock"`
and `group="sanlock"`. Both hosts mounted the exports at `/srv/repo-a` and `/srv/repo-b`.
Their lockspaces were `repoa` at `/srv/repo-a/lsfinal:0` and `repob` at
`/srv/repo-b/lsfinal:0`, with both host IDs live in each. The VM's explicit lease elements, in
acquisition order, were:

```xml
<lease>
  <lockspace>repoa</lockspace>
  <key>vm-a</key>
  <target path="/srv/repo-a/lsfinal" offset="1048576"/>
</lease>
<lease>
  <lockspace>repob</lockspace>
  <key>vm-b</key>
  <target path="/srv/repo-b/lsfinal" offset="1048576"/>
</lease>
```

With `auto_disk_leases=0`, libvirt accepted and started the VM with one raw disk on each
repository and these two different lockspaces. `sanlock client log_dump` recorded one two-resource
acquire request in the XML order (`repoa:vm-a`, then `repob:vm-b`). The live VM's QEMU PID held
both resources. A second throwaway VM on Repository A also started while the first VM remained
running, using a separate resource file `/srv/repo-a/lease-vm2:0` and resource key `vm-a2`.
`sanlock client status -o p` showed both QEMU PIDs and their distinct Repository A lease records
at the same time.

The raw test disks had libvirt's `<shareable/>` marker so QEMU's NFS advisory file lock would not
refuse a duplicate before sanlock was reached. This isolates sanlock's explicit lease enforcement;
it does not validate a production disk-sharing policy. With the first VM running on Core 1, Core 2
attempted to start the same domain and was refused with `resource busy: Failed to acquire lock:
Lease is held by another host`. Sanlock's log showed `-243 lease owned by other host` on
`repoa:vm-a`, and Core 2 remained shut off.

Rollback and retry were exercised by stopping the first VM, then making Core 2 hold only
`repob:vm-b` using a throwaway `sanlock client command` process. Core 1's next VM start acquired
`repoa:vm-a` first, failed to acquire `repob:vm-b` with `-243`, and released `repoa:vm-a`; its
post-failure `sanlock client status -o p` had no VM resource leases. After stopping the throwaway
holder, retrying the VM start on Core 1 succeeded and both resource leases were visible under its
QEMU PID. Resource lease versions advanced to 6 on retry.

The commands used, from the repository root, were:

```sh
python3 tests/integration/cluster_ha_lab.py --prepare
python3 tests/integration/cluster_ha_lab.py --resume-storage .cache/nex-cluster-ha-lab.<fixture-id>
```

The fixture was then booted using its ownership-recording `launch()` helper for `core1` and
`core2`, and its `ssh()` helper for guest commands. Inside the guests, the trial installed
`sanlock libvirt-daemon-system libvirt-daemon-driver-qemu libvirt-daemon-plugin-sanlock
libvirt-clients qemu-utils`, mounted the two NFSv4 exports, and initialized/registered the two
lockspaces and resource records with `sanlock direct init` and `sanlock client add_lockspace`.
Core 1 initialized the lockspaces and first VM resources with these commands; Core 2 registered
host ID 2 in both lockspaces. The second VM used a distinct resource file. On Core 1, the existing `ReadSanlockHostStatus`
implementation was also called through a temporary disposable Go caller while the leases were
live; it reported `repoa/1 HOST_LIVE generation 1`, `repob/1 HOST_LIVE generation 1`, and
`repob/2 HOST_LIVE generation 1`, each with `io_timeout=10`. The caller was removed after the
check. The temporary caller is not part of this change and the result does not establish an API
contract or authorization behavior.

```sh
truncate -s 4M /srv/repo-a/lsfinal
chown sanlock:sanlock /srv/repo-a/lsfinal
sanlock direct init -s repoa:1:/srv/repo-a/lsfinal:0 -Z 512 -A 1M
sanlock direct init -r repoa:vm-a:/srv/repo-a/lsfinal:1048576 -Z 512 -A 1M
truncate -s 4M /srv/repo-b/lsfinal
chown sanlock:sanlock /srv/repo-b/lsfinal
sanlock direct init -s repob:1:/srv/repo-b/lsfinal:0 -Z 512 -A 1M
sanlock direct init -r repob:vm-b:/srv/repo-b/lsfinal:1048576 -Z 512 -A 1M
truncate -s 1M /srv/repo-a/lease-vm2
chown sanlock:sanlock /srv/repo-a/lease-vm2
sanlock direct init -r repoa:vm-a2:/srv/repo-a/lease-vm2:0
```

The mount and sanlock membership commands were:

```sh
mount -t nfs -o vers=4 198.18.90.10:/srv/nex-repo-a /srv/repo-a
mount -t nfs -o vers=4 198.18.90.10:/srv/nex-repo-b /srv/repo-b
sanlock client add_lockspace -s repoa:1:/srv/repo-a/lsfinal:0  # Core 1
sanlock client add_lockspace -s repoa:2:/srv/repo-a/lsfinal:0  # Core 2
sanlock client add_lockspace -s repob:1:/srv/repo-b/lsfinal:0  # Core 1
sanlock client add_lockspace -s repob:2:/srv/repo-b/lsfinal:0  # Core 2
```

The decisive contention and rollback commands were:

```sh
virsh start lease-two-repos
systemd-run --unit=nex-lab-lease-holder --collect \
  /usr/sbin/sanlock client command \
  -r repob:vm-b:/srv/repo-b/lsfinal:1048576 -c /usr/bin/sleep 300
virsh start lease-two-repos
sanlock client status -o p
sanlock client log_dump
systemctl stop nex-lab-lease-holder.service
virsh start lease-two-repos
```

The first and second `virsh start` calls above correspond to the duplicate-host refusal and the
second-resource rollback attempt; between trials, the VM was stopped and its lease holder state
was confirmed from sanlock's native status and log. `python3 tests/integration/cluster_ha_lab.py
--cleanup .cache/nex-cluster-ha-lab.<fixture-id>` stopped only the fixture-owned QEMU and VDE
processes after evidence capture. All package and configuration changes were confined to the
disposable guest overlays. The initial `--cleanup` invocation rejected the two Core QEMU
processes because the wrapper had recorded repository-relative socket and serial paths while
the fixture's safety check requires absolute paths. The exact recorded PID, start time, process
group and command were revalidated with the fixture's `owned_process()` check, the recorded Core
QEMU and VDE processes were stopped, the labelled outer libvirt container was removed, and a
process listing confirmed no fixture QEMU or VDE remained. Ignored fixture files remain in the
local `.cache` directory. The optional outer libvirt-container start path also failed on the VDE
plug with `Could not open vde: Invalid argument`; guest-local libvirt was used for the successful
lease trial.

This demonstrates that native libvirt XML can name multiple explicit sanlock lockspaces for one
VM and that two repositories can acquire in a stable order with sanlock rollback on a later
resource conflict. Therefore the plan's one-lockspace-per-repository shape is feasible at the
native engine layer; `auto_disk_leases` and its single global `disk_lease_dir` cannot express it.
This does not implement or validate Nex Core API integration, host-side durable repository
registration, production NFS durability/partition behavior, fencing, promotion safety, or
Controller recovery. The acceptance gate remains open.
