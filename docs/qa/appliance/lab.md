# Controller appliance disposable KVM verification

Date: 2026-09-08. Status: clean package deployment, restart/outage checks, separate-host restore
with existing-agent reconnect, replacement-host autostart and post-reboot login/reconnect passed.
Physical hardware, release-distribution clearance and hosted CI were not verified.

## Environment and isolation

- Workstation QEMU 11.1.1, KVM, nested Intel virtualization. Outer hosts are reflink/sparse
  copies of an earlier disposable Nex Core 0.1.0-alpha1 (Debian 13) wizard disk, 4 vCPU,
  8 GiB RAM; the original disk was never booted. No physical disks, workstation network or
  unrelated Docker services changed.
- Preparation mounted only the copy's first partition to add a test-only SSH key and disable
  password SSH. QEMU user networking with loopback-only forwards; NIC MACs match the copied
  host's interfaces.
- New 80 GiB regular-file disk hotplugged; guest verified exact size and virtio serial
  `NEX-APPLIANCE-QA-ONL` (QEMU truncates longer serials) before formatting ext4 at
  `/var/lib/nex/appliances` (75 GiB available). Guest libvirt default NAT `virbr0`
  `192.168.122.1/24` (inside the disposable VM). `genisoimage` installed only in the guest.

Preparation results: `systemctl is-system-running` `running`; `/dev/kvm` present; `virsh list
--all` empty; `qemu-img`/`genisoimage` installed; SSH key login via forward succeeded; libvirt
network start/autostart succeeded. Preparation artifacts (QEMU command, serial, known-host,
baseline, storage/network output) stay in the private ignored lab directory; private SSH keys are
not evidence.

## Host-local service and browser checks

Development direct-binary `nex-core-local` under systemd with actual HTTPS (not the Debian
package), source snapshot SHA256 `004d39cf2b7dce24dc850a286361a27914c7a4b9bc99c7a2bc81315ff6f40944`:

```sh
.cache/phase1-qa-venv/bin/python tests/integration/appliance-lab.py \
  --lab-dir .cache/nex-appliance-lab.<redacted> --evidence docs/qa/appliance
```

No HTTP routes or virtualization mocked. Incorrect credentials rejected, keyboard sign-in
succeeded; anonymous inventory, missing Origin, missing CSRF and invalid deployment input returned
401, 403, 403, 400. Initial empty state showed host identity and the `appliance manifest
unavailable` blocker with deployment disabled (historical; later reruns replaced overview captures).
No JavaScript errors. 1440 px and 390 px captures inspected: readable, no horizontal overflow.
Did not verify the wizard or appliance creation. Results: [results.json](results.json),
[host-preflight.json](host-preflight.json).

`local-overview.png` (not committed)
`local-invalid-login.png` (not committed)
`local-overview-narrow.png` (not committed)

## Development appliance run and reproduced failures

Image `appliance-lab2`, source digest `e71508e7979543e9024b92947373c936cc91a509acff2bbf058578ace7293753`.
Development recovery results, **not a seamless clean deployment claim**.

| Failure | Bug | Development workaround / evidence |
| --- | --- | --- |
| QEMU could not create VNC socket; API kept one stopped VM, `needs_attention` at 80%, no second disk | [BUG-AP-0004](../../BUG_LEDGER.md) | Removed only the two socket attributes, then GUI Start and Retry. [Failure](initial-start-failure.json), [libvirt output](initial-start-libvirt.txt), `initial-start-failure.png` (not committed) |
| `nex-appliance-init` ordering cycle; systemd deleted the job | [BUG-AP-0006](../../BUG_LEDGER.md) | Init started manually after cloud-init |
| Public web directory root-owned 0700; GET `/` 403 while APIs worked; readiness reported early | [BUG-AP-0007](../../BUG_LEDGER.md), [BUG-AP-0008](../../BUG_LEDGER.md) | Manual permission correction and init restart. [Observed permissions](initial-public-permissions.txt) |
| `virsh` stdin echo broke recovery XML parsing | [BUG-AP-0009](../../BUG_LEDGER.md) | Subsequent actual export succeeded |

After these corrections: browser showed one Site, Pool, enrolled host and adopted VM UUID.
Controller service outage kept local HTTPS and the running VM. Wrong-host shutdown confirmation
rejected; graceful shutdown and cold start restored guest, static network and Controller without
bootstrap seed. Host-local service restart kept the running VM and one ready deployment. Guest
observed 2 vCPUs, 3922 MiB RAM, 32 GiB root (30 GiB free), `192.168.122.10/24` on `nex0`. Browser
used a loopback SSH forward (not physical-LAN routing).

`controller-ready.png` (not committed)

Recovery helper exported identity/state through QEMU guest agent (no guest SSH); archive copied
off-host mode 0600, six file hashes checked, not committed; [export evidence](export-verification.json).
Not valid for restore to the final source version. Guest and outer host shut down, disks preserved.

## Clean final package deployment

Third fresh outer host (own disk copy, new 80 GiB storage file, internal bridge). The second
host's package attempt failed on `UMask=0077` directory mode ([BUG-AP-0010](../../BUG_LEDGER.md));
preserved and stopped. [Retained package failure](package-umask/libvirt-failure.txt).

Installed `nex-core-local_0.1.0+lab6.1_amd64.deb` via APT (artifact hashes in
[package evidence](package.md)), then `core-local-setup configure` with management address
`10.0.2.15`, `/var/lib/nex/appliances`, `virbr0`. Service kept `UMask=0077`. No executable,
guest file, XML or permission fix injected. [Installed artifact checks](final/installed-artifacts.txt),
[package hash](final/artifact.json).

```sh
.cache/phase1-qa-venv/bin/python tests/integration/appliance-lab.py \
  --lab-dir .cache/nex-appliance-lab.<redacted> \
  --evidence docs/qa/appliance/final --deploy --packaged
```

| Check | Result |
| --- | --- |
| Browser deployment (storage/bridge, static address, DNS, resources, private password, review, Create VM & install Controller) | 0% → 10% → 92% → 98% → 100% `ready`; one VM UUID `private-id-033`. [Original progress](final/clean-first-deployment.json) kept separate from reruns |
| Invalid/keyboard login, unauthorized/Origin/CSRF/invalid input, signed-manifest tamper, refresh with one UUID | Passed |
| Controller login | Exactly one Site/Pool/enrolled host/adopted VM |
| Bootstrap cleanup before first reboot | Password/configuration, `/var/lib/cloud` user data, `/run/cloud-init/instance-data-sensitive.json` removed |
| Controller service stopped | VM running, local HTTPS available |
| Typed shutdown (wrong target rejected), graceful shutdown, cold start | Static networking persisted, no seed/credentials, same identity; root FS 33,603,391,488 bytes, 2 vCPU, 4,016,552 kB MemTotal ([guest output](final/controller-cold-start.txt)) |
| Local management stopped | Failed-fetch/stale display, power controls disabled, Controller HTTPS available; restart kept one running VM and one operation. Harness reload raced Type=simple startup; harness now polls `/healthz` (no app change) |
| Captures | Controller, ready deployment, desktop/narrow local UI, stale controls inspected; no 390 px overflow, no uncaught JS errors |
| Export | Shipped helper exported to new private archive; off-host copy matched six manifest hashes; source hash matches image 6. Original VM autostart disabled, `shut off` observed. [Export and exclusion](final/export.json) |

[Complete check results](final/results.json).

`final/deployment-ready.png` (not committed)
`final/controller-ready.png` (not committed)
`final/local-service-offline.png` (not committed)

## Separate-host recovery with the shipped package

Fourth copied host with package 6.1 and image 6. Original VM `private-id-033` stayed shut off,
autostart disabled; original host stayed on only to prove agent reconnect. No VM or journal
fabricated or relocated. With local management stopped, `core-local-setup prepare-recovery
--request-file /root/replacement.json` created `private-id-027` and returned
[`recovery_prepared`](final/recovery-prepared.json) (empty Controller, no new site/pool/host).

```sh
/usr/lib/nex/host-recovery restore \
  --vm private-id-027 \
  --original-vm private-id-033 \
  --original-external --original-stopped-and-excluded \
  --replace-empty-controller --archive /root/controller-final.tar.gz \
  --state-dir /var/lib/nex/core-local/vmhost \
  --storage-dir /var/lib/nex/appliances
```

Completed leaving Controller stopped ([output](final/restore.txt)); QEMU guest agent started it.
No appliance SSH, database edit or guest patch. Preparation request removed, local management
restarted.

Lab topology (not installer behavior or physical LAN proof): QEMU socket Ethernet joined the guest
bridges; original bridge MAC `52:54:00:99:01:01`, `192.168.122.2/24`; replacement kept
`192.168.122.1/24` and Controller `192.168.122.10`. Identical copied bridge IDs made STP block the
link until the unique MAC; a lab-only unit reattaches the NIC after reboot. Browser via loopback
SSH forward.

Results: original administrator password accepted; site/pool/host/VM IDs matched; certificate
SHA-256 `64ee325ae0fa7f758611ebde4904e15f4f4c11eb6a289950b66e7fb157696283` equals the original
pin; `/source` returned the exact image 6 archive; original agent reconnected with its identity and
freshly reported the original VM **shut off**. [Restored inventory](final/restored-inventory.json),
[check results](final/recovery-results.json).

`final/restored-controller.png` (not committed)

Limits: manual explicit workflow. Restored database keeps the Controller VM placed on the original
host; replacement host not enrolled, replacement VM not adopted. Not automatic relocation,
cross-version restore, shared-storage recovery, HA or fencing.

Replacement host reboot (original excluded): first attempt the lab link unit ran before the
bridge existed (`virbr0: Device does not exist`); with a bounded wait, the next reboot autostarted
the restored VM with local management and lab networking active.
[Reboot and exclusion evidence](final/replacement-host-reboot.txt). Browser then repeated
password, TLS/source hash, ID and stopped-VM checks successfully. Controller shut down gracefully,
outer hosts powered off; [process checks](final/lab-shutdown.json) show all four outer QEMU
processes exited and every host disk preserved.
