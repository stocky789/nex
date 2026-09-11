# VM editing checks

Date: 2026-09-10. Status: Go, contract, UI and simulated browser checks passed on the workstation
for the [edit slice](../../plans/2026-09-10-vm-edit.md), and the
[real-host walkthrough](#real-host-walkthrough) below passed every step on a disposable Nex Core
host with libvirt 11.3 after two defects it found were fixed. No independent review has happened
yet. This is a check-and-evidence pass by the implementing session.

## Environment

Arch Linux workstation, kernel 7.2.3-1-cachyos; Go 1.27.1 (nodwarf5), Node 22, Python 3.14 with
Playwright 1.62 and Chromium 1234 in `.cache/phase1-qa-venv`. The slice was first built against `6d38fbe`
and then ported onto main at `c81a633`, where the Controller no longer decides name, MAC or memory
admission (Core does), the journal report gained outcomes and promotion, and a `vm-media` command
exists; the Go, contract, UI, docs-gate and simulated-browser checks were re-run on main. The
real-host walkthrough below ran from the pre-port tree; the engine code it exercised was ported
unchanged apart from the two fixes it produced. No libvirt on the workstation.

## Executed checks

| Command | Result |
|---|---|
| `go build ./...`, `go vet ./...`, `gofmt -l` | exit 0, no diagnostics, nothing to format |
| `go test -race -count=1 ./...` | all `ok`; new tests `TestNeedsStopClassifiesEdits`, `TestManagedUpdateLayoutKeepsExistingDisks`, `TestManagedCurrentAndAppliedFollowRevisions`, `TestManagedApplyLiveReconcilesDevices` (`internal/vmhost`), `TestUpdateVMValidatesFreezesMACsDispatchesAndRefreshes`, `TestUpdateVMRunningStateAndGates` (`internal/controller`) |
| `python3 -m unittest tests.test_api_contract` | 21 tests OK (routes, schemas ↔ structs, command kinds, generated reference current) |
| `python3 scripts/generate_api.py --check` | `index.html is current` |
| `npm run typecheck`, `npm run build` (in `web/`) | exit 0 |
| `python3 scripts/check_docs.py --worktree` | passed (main) |
| `.cache/phase1-qa-venv/bin/python tests/integration/vm_edit_browser.py --evidence .cache/vm-edit-browser` | exit 0; see below |

Two findings surfaced by the Controller tests and fixed before they passed: the frozen MACs were
written through a slice shared with the replay payload (replay then reported "another payload"),
and `powerVM` did not yet count a pending edit as an in-flight request for the VM.

## Browser walkthrough (simulated host)

Chromium; real HTTPS Controller; simulated mTLS agent that applies every vm-update and reports it as current; no libvirt execution. The VM is created through the API; the browser exercises only the editor.

- Edit while stopped from General: rename, description, second disk out of the boot order and second interface saved; invalid boot order refused in place; existing disk size locked; first interface keeps its MAC; vm-update succeeded and details follow the host report; 360px editor without overflow
- Edit while running: name, CPU, memory and firmware locked with the stop-first reason, SATA refused for a new disk, boot disk removal locked, description open; interface removal saved as revision 2 while the VM stayed running
- Third edit queued and applied; no browser page errors across the walkthrough

Captures under `.cache/vm-edit-browser/`: `vm-edit-details-before.png`, `vm-edit-edit-stopped-360.png`, `vm-edit-edit-stopped.png`, `vm-edit-details-after-stopped-edit.png`, `vm-edit-edit-running.png`. Inspected: the Edit button sits in the
details heading beside the freshness badge; the form keeps the page's field grid at 1440 px and
stacks to one column at 360 px without horizontal overflow; locked fields are greyed and carry the
"Stop the VM to change this." title; the notice and the `vm-update` task row appear after Save;
the details show the new name, description, second disk and both interfaces with their MACs after
the host report.

Limits: Simulated host only: no libvirt redefine, hot-plug, media change or disk allocation executed; Pending-edit refusal (409) exercised in Go tests, not in the browser.

## Real-host walkthrough

`tests/integration/vm_edit_lab.py --copy-from .cache/nex-appliance-lab.<redacted> --iso
.cache/nex-iso/debian-13.6.0-amd64-netinst.iso` (run with `.cache/phase1-qa-venv/bin/python` and
`GO=.cache/toolchains/go/bin/go` from the pre-port tree; log in `.cache/vm-edit-lab.log`, results in
[`lab-results.json`](lab-results.json)). It copies the retained disposable lab (Nex Core 0.1.0-alpha1
image, kernel 6.12.94+deb13, libvirt 11.3.0, QEMU 10.0.11, AppArmor), builds the Controller and agent
from this tree (agent sha256 `720af37f…`), enrolls the copy against a workstation Controller over
loopback, uploads the Debian 13.6 netinst ISO, and drives the API while checking libvirt on the host.
The lab copy was destroyed afterwards; nothing outside `.cache` changed.

| Step | Result |
|---|---|
| Agent capability | `vm-update-v1` reported by the built agent |
| 1. Create (UEFI q35, 1 VirtIO disk, 1 NIC, ISO first in boot order) | `succeeded`; `virsh dumpxml --inactive` shows the read-only SATA cdrom `sda` with the ISO and 9 `pcie-root-port` controllers behind `pcie-root`; libvirt accepted the define |
| 2. Edit while stopped: rename, description, second VirtIO disk (boot order 0), second NIC, autostart on | `succeeded`, revision 1; domain renamed (`<name>…-ed</name>`), `disk-1.qcow2` allocated in the VM directory and listed as `vdb`, both NIC MACs frozen (`52:54:00:xx:xx:00/01`) and present in the definition, `virsh dominfo` `Autostart: enable` |
| 3. Start, then edit while running: third VirtIO disk, third NIC, eject ISO | `succeeded`, revision 2, VM still `running`; `virsh domblklist` (live) shows `vdc` = `disk-2.qcow2` and `sda -` (ejected), `virsh domiflist` (live) shows three virtio interfaces; the persistent definition matches |
| 4. CPU count change while running | Controller `409 stop the VM first to change the CPU configuration` |
| 5. Remove the third disk and NIC while running | `succeeded`, revision 3; persistent definition lists two disks and two interfaces, `disk-2.qcow2` retained on disk. **Limit:** the guest was sitting at the Debian installer boot menu and never released the devices, so the live domain kept `vdc` and the third interface until restart; libvirt's `detach-device --persistent` accepts the request and completes the live part only when the guest cooperates |
| 6. Force-stop, then remove the second disk while stopped | `succeeded`, revision 4 by redefine; `domblklist --inactive` lists `vda` only, `disk-1.qcow2` still in the VM directory and named in the journal entry's `retained` |

Two defects the first runs found, both fixed in this tree and covered by the Go tests:

- **libvirt rejected every new definition**: `The PCI controller with index='0' must be model='pcie-root'
  for this machine type, but model='pcie-root-port' was found instead`. Once any PCI controller is
  listed, the first must be the `pcie-root`; `managedDomainXML` now emits it before the root ports.
- **Hot-plug failed with `No more available PCI slots`** even with four spare root ports: libvirt
  assigns the defined devices to the listed ports first and adds no spare beside them. The
  definition now lists one port per PCIe device (VirtIO disks, NICs, video, virtio-serial, balloon,
  SCSI controller) plus four free ones, and `run()` now appends the last stderr line of a failed
  command to its error so the task reports libvirt's reason instead of a generic refusal.

Harness-only fixes on the way: an ISO path that resolved under the source tree, an autostart
assertion with the wrong whitespace, and one transient enrollment failure at Controller startup
(now retried three times).

## Not run

- A guest that cooperates with hot-unplug (Linux with VirtIO drivers) releasing a device live;
  media insert (only eject) on a running guest; a `needs_attention` edit reconciled by observation;
  `domrename` when the new name collides with a defined domain.
- `tests/integration/controller_replacement.py` with an edited VM (adoption of a revision > 0).
- Independent implementation and documentation review.
