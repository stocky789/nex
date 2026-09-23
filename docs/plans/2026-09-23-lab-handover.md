# Lab handover — Phase 3 chain and placement fault/recovery, 2026-09-23

Owner: project owner. Scope, as the project owner requested on 2026-09-23:
(1) sign off hosted delivery, (2) run the Phase 3 integration tests in a local lab,
(3) run the native placement fault/recovery acceptance in the same lab.

## State at handover

- **Nothing is committed.** All changes are unstaged in the working tree.
- **Another session also has uncommitted edits in this tree:** `AGENTS.md`, two guide pages
  (`install-nex-core.md`, `replace-a-failed-system-disk.md`), their change record
  (`docs/changes/2026-09-23-system-disk-md-ext4-wording.json`), and one `CHANGELOG.md` line.
  `python3 scripts/check_docs.py --worktree` currently fails only on `AGENTS.md`, which has no
  assessment yet. That is the other session's to add; this work's record passes.
- **The lab is stopped.** No lab VMs, DHCP server or replacement Controller are running. The
  lab's disks and ignored evidence are kept under `.cache/lab-0923/` and
  `.cache/nex-fresh-install.*`. The lab bridge `nexlab0` and its tap devices disappear on reboot;
  recreate them as described under "Restarting the lab".
- **Memory rule:** earlier today the workstation froze with about 20 GB of lab guests plus an ISO
  build running. Keep lab guests at or below about 16 GB in total, and never build an ISO while
  hosts are running.

## Results

| Item | Status |
| --- | --- |
| 1. Hosted delivery acceptance | Signed off by the project owner; recorded in the [readiness handoff](2026-09-14-production-readiness-handoff.md). |
| 2. Phase 3 integration | **Done except one item.** Passed: offline install under enforcing Secure Boot and under BIOS, one-of-two and mirrored disks, no-link install, power cut mid-install then reinstall, eject/reboot, bundled Controller deploy, second-host enrollment and join, first managed VM, recovery access during a Controller outage, host reboot. **Open:** interrupted appliance deployment, and a fresh-install check of the BUG-0534 fix. |
| 3. Placement fault/recovery | **Closed for virtual-lab scope.** `lost-reply`, `shared-edit`, `storage-loss` (NFS stop and fsid swap), `membership` (both race orders plus eject-first), `interrupt` and `replacement` all pass on ISO-installed hosts. |

Full evidence: [QA record](../qa/2026-09-23-phase3-placement-lab.md).

## Bugs found and fixed (details in `docs/BUG_LEDGER.md`)

- **BUG-0533:** the ISO build failed because it fetched its pinned OVMF version from moving sid.
  It now uses a snapshot (`installer/build.sh`).
- **BUG-0534:** a fresh host could not upload ISOs to Local storage because `nex-media` was 0700.
  Fixed in `bundled_media.go`. Existing hosts are repaired with
  `chmod 0711 /var/lib/libvirt/images/nex-media`, which is now in the guide.
- **BUG-0535:** a joined host's untagged network stayed under its removed auto-pool on the
  Controller. Fixed in `controller/managed.go`.
- **BUG-0536, BUG-0537, BUG-0538:** interrupted or refused creates and updates left phantom VMs and
  stuck master reservations. Fixed in Core per the project owner's decisions (2026-09-23):
  - a create interrupted while validating settles as failed;
  - a member journals a refusal, and its inventory's new `aborted` evidence lets the master
    release the reservation;
  - a delete prunes a record even if its update never settled.

  This changes a Core-peer contract, documented in `docs/API.md`.

All fixes have regression tests, and `go test ./internal/agent ./internal/vmhost ./internal/controller`
passes. A `-race` run of the final code has not been done yet.

## Next steps (in order)

1. **Finish item 2 on a fresh host C.** Use the ISO already built with every fix:
   `dist/nex-core-0.4.5-amd64.iso`, SHA-256 `62e37dbb6bb9486e…bf5272f5f6a1262124811b3abeaee`.
   Run only host C (5 GB) and nothing else.
   1. Restart the lab bridge and DHCP (see below).
   2. Prepare a new lab:
      `python3 tests/integration/fresh_install_lab.py --prepare --disks 1 --memory-mib 5120 --vcpus 2 --evidence .cache/lab-0923/evidence-c2`.
   3. In its `config.json`, set `tap=nexlab-c`, `mac=52:54:00:91:20:17`, `address=10.0.2.17`
      and `hostname=nex-lab-c`.
   4. Boot the ISO, press Enter at the boot menu, then run
      `fresh_install_gui.py --stage configure`, then `--stage install`, then `--stage restart`.
   5. Boot with `--installed`, then run `--stage console-key`.
   6. Check the fix: after the host enrolls or deploys, `nex-media` must be `drwx--x--x`.
   7. Deploy a DHCP appliance through `POST /api/local/v1/deployments` with 2048 MiB of memory.
      Run `systemctl kill -s KILL nex-core-local` on C when the deploy stage is `copying_disk`.
   8. Expect the deploy to resume, or to end `failed` or `needs_attention` with a reason, with at
      most one appliance domain. **Retry deployment** should then finish it.
   9. Record the result in the QA record's "Interrupted bootstrap" row. Then mark item 2 closed in
      the readiness handoff and move the roadmap's open line
      ("A Controller deployment interrupted part-way…") to done.

   The old half-installed host C lab is still in `.cache/lab-0923/lab-c`. Delete that directory
   once the new one exists.
2. **Run** `go test -race ./internal/agent ./internal/vmhost ./internal/controller` and
   `python3 -m unittest discover -s tests`.
3. **Independent review** of the Core changes: `internal/vmhost/managed.go`
   (`RecordRefusal`, `NeverCreatedVMs`, the validating settle in `reconcile`/`promote`), and
   `internal/agent/{managed,pool_vm,pool_vm_inventory}.go`.
4. **Commit** once the other session's edits are settled. The suggested scopes are `installer:`
   (BUG-0533), `core:` (BUG-0534, BUG-0536 to BUG-0538), `controller:` (BUG-0535), and `tests:`
   (harnesses, `placement_faults.py`).

## Known limits and follow-ups

- The master-side lost-reply ordering (BUG-0537) was not re-observed in the lab after the fix;
  in two runs the kill landed after the reply. It is proven by unit tests only.
- The report-time `update-abort` in `agent/run.go` only covers VMs on pool-shared storage. A
  local-storage member VM with a stuck update was not tested.
- Cosmetic: the installer's Disk step keeps showing its red "Select the disk…" message after a
  disk is ticked.
- Not tested: physical Intel/AMD hardware, a real BMC or watchdog, and a package-upgrade path for
  these fixes (the lab used binary swaps).

## Restarting the lab

Helper scripts are in `.cache/lab-0923/tools/`: `lab.py` for QMP screen, keys and clicks, `h` for
root SSH to hosts a, b, c and s, and `capi.py`, a Controller API client. As root:

1. Create the bridge: `ip link add nexlab0 type bridge`, then add addresses `10.0.2.2/24` and
   `10.0.2.3/24`, then `ip link set nexlab0 up`.
2. Create one user-owned tap per guest (`nexlab-a`, `nexlab-b`, `nexlab-c`, `nexlab-s`), attach
   each to the bridge with `master nexlab0`, and bring it up.
3. Start `dnsmasq` bound to `nexlab0`:
   - DNS on `10.0.2.3`, DHCP range `10.0.2.100–150`, router `10.0.2.2`;
   - fixed leases for MACs `52:54:00:91:20:15`/`16`/`17`/`30` → `.15`/`.16`/`.17`/`.30`.

Hosts start with `fresh_install_lab.py --lab-dir <lab> --installed`; the lab directories are
listed in `.cache/lab-0923/lab-{a,b,c}`. Pool A/B now belongs to the workstation replacement
Controller (`.cache/lab-0923/replacement/`, listening on `10.0.2.2:8443`); the original appliance
on host A is shut off with autostart disabled.
