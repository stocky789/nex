# VM editing checks

Date: 2026-09-10. Scope: [edit slice](../../plans/2026-09-10-vm-edit.md). Status: Go, contract, UI and
simulated browser checks passed. The [real-host walkthrough](#real-host-walkthrough) passed every step
after two defects were fixed. No independent review yet.

## Environment

Arch Linux, kernel 7.2.3-1-cachyos; Go 1.27.1, Node 22, Python 3.14, Playwright 1.62 and Chromium 1234 in
`.cache/phase1-qa-venv`. Built on `6d38fbe`, then ported onto `c81a633` (Core owns name/MAC/memory
admission, journal outcomes/promotion, `vm-media`). The Go, contract, UI, docs-gate and simulated-browser checks
were rerun after the port. The real-host walkthrough ran pre-port; the engine code it exercised was ported unchanged apart from its
two fixes. No libvirt on the workstation.

## Executed checks

| Command | Result |
|---|---|
| `go build ./...`, `go vet ./...`, `gofmt -l` | exit 0, clean |
| `go test -race -count=1 ./...` | all `ok`; new: `TestNeedsStopClassifiesEdits`, `TestManagedUpdateLayoutKeepsExistingDisks`, `TestManagedCurrentAndAppliedFollowRevisions`, `TestManagedApplyLiveReconcilesDevices`, `TestUpdateVMValidatesFreezesMACsDispatchesAndRefreshes`, `TestUpdateVMRunningStateAndGates` |
| `python3 -m unittest tests.test_api_contract` | 21 OK |
| `python3 scripts/generate_api.py --check` | current |
| `npm run typecheck`, `npm run build` (in `web/`) | exit 0 |
| `python3 scripts/check_docs.py --worktree` | passed |
| `tests/integration/vm_edit_browser.py --evidence .cache/vm-edit-browser` | exit 0 |

Controller tests exposed two bugs, fixed before they passed: frozen MACs were written through a slice shared with the replay
payload, and `powerVM` did not count a pending edit as in flight.

## Browser walkthrough (simulated host)

Chromium ran against a real HTTPS Controller with a simulated mTLS agent that reports every vm-update as current. No libvirt.

- Stopped edit passed: rename, description, second disk and NIC saved; invalid boot order refused; disk size locked;
  first MAC kept; details follow the host report; 360 px editor has no overflow.
- Running edit passed: name, CPU, memory and firmware locked with the stop-first reason; SATA refused; boot-disk removal
  locked; interface removal saved as revision 2 while the VM kept running.
- A third edit queued and applied. No page errors.

Captures inspected in `.cache/vm-edit-browser/` (not retained): `vm-edit-details-before.png`,
`vm-edit-edit-stopped-360.png`, `vm-edit-edit-stopped.png`, `vm-edit-details-after-stopped-edit.png`,
`vm-edit-edit-running.png`. Layout, locked-field titles, the notice/task row and updated details were correct.
Limits: no redefine, hot-plug, media or disk allocation executed. Pending-edit 409 was tested only in Go.

## Real-host walkthrough

`tests/integration/vm_edit_lab.py --copy-from .cache/nex-appliance-lab.<redacted> --iso
.cache/nex-iso/debian-13.6.0-amd64-netinst.iso` (pre-port tree; results in [`lab-results.json`](lab-results.json)).
Copied disposable lab: Nex Core 0.1.0-alpha1 image, kernel 6.12.94+deb13, libvirt 11.3.0, QEMU 10.0.11,
AppArmor. The agent was built from this tree (sha256 `720af37f…`) and enrolled over loopback. The lab copy was destroyed afterwards.

| Step | Result |
|---|---|
| Agent capability | `vm-update-v1` reported |
| 1. Create (UEFI q35, 1 VirtIO disk, 1 NIC, ISO first) | `succeeded`; read-only SATA cdrom `sda`, 9 `pcie-root-port` behind `pcie-root` |
| 2. Stopped edit: rename, description, disk 2, NIC 2, autostart | rev 1; domain renamed, `disk-1.qcow2` as `vdb`, both MACs frozen, `Autostart: enable` |
| 3. Running edit: disk 3, NIC 3, eject ISO | rev 2, still running; live `vdc`, `sda -`, three interfaces; persistent matches |
| 4. CPU change while running | `409 stop the VM first to change the CPU configuration` |
| 5. Remove disk 3 and NIC 3 while running | rev 3; persistent has two disks/NICs, `disk-2.qcow2` retained. **Limit:** the guest sat at the installer menu and never released the devices, so the live domain kept them until restart |
| 6. Force-stop, remove disk 2 | rev 4 by redefine; `vda` only; `disk-1.qcow2` retained and listed under `retained` |

Defects found and fixed (Go tests cover both):

- libvirt rejected every define because the first PCI controller must be `pcie-root`. That controller is now emitted first.
- Hot-plug failed with `No more available PCI slots`. The definition now lists one root port per PCIe device plus four spare,
  and `run()` appends the last stderr line to its error.

Harness-only fixes: the ISO path, autostart whitespace, and a startup enrollment retry.

## Not run

- Cooperative guest hot-unplug; media insert on a running guest; `needs_attention` reconciliation;
  `domrename` name collision.
- `controller_replacement.py` with an edited VM (revision > 0 adoption).
- Independent implementation and documentation review.
