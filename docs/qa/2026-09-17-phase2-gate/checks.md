# Phase 2 gate run on 0.2.10~gate5

Date: 2026-09-17 UTC (build 00:26Z to 00:36Z, lab run 00:40Z to 02:44Z). All dates and times in
this report are UTC.

**Result of record: the final gate run on `0.2.10~gate5`.** 15 of the 16 checklist rows,
COVERAGE-1 and both halves of G7 **passed** in a new disposable lab. **Row 11 failed** on its
running-VM interface probe, although its BUG-0254 checks a–c passed. The gate4 code-review "major" was
**not reproduced**, and the real defect found while checking it (BUG-0254) is **fixed in gate5
and confirmed on a real host**. **But a new major defect was found on gate5:** a network
interface added to a running VM can reuse the MAC of an interface the guest has not yet released,
leaving two interfaces with the same MAC on the bridge while the task reports succeeded
([open defect 1](#open-defects-after-gate5)). Code reading indicates the defect is also present
in released 0.2.9 (not reproduced in a lab on 0.2.9). gate5 is the last lab cycle of this effort, so the
defect is **not fixed in the gate5 build**; it was handed to the project owner with the
[triage below](#owner-triage-open-defect-1). After gate5 it was **fixed in the working tree**
(BUG-0256: Core refuses the add, the Controller stops refilling removed addresses) with unit
tests against a simulated libvirt only; it is **not lab-verified**. **The Phase 2 gate
stays open** until a new gated build reruns row 11 and the G7 rows, and the
[remaining requirements](#remaining-gate-requirements-outside-this-lab) are met.

G6 external Controller replacement **passed** with the gate5 package binary, but same-host
appliance redeploy is **blocked by the product** (open owner decision) and backup restore was
**not exercised**, so G6 stays **partial**. Several phase items are partial because a sub-clause
was not exercised (see [gate items](#gate-items)). This report has **not had independent
review**, hosted CI has not run on this tree, and the tree is not committed.

The gate1, gate3 and gate4 runs are kept as [run history](#run-history). gate4 is superseded by
this run.

All identifiers are sanitized. Host, pool, VM, operation and site IDs, machine IDs, boot IDs,
SSH and TLS fingerprints, MAC addresses derived from VM IDs, and lab addresses and ports are
replaced with the aliases below, with reserved documentation addresses, or with shortened
prefixes of artifact digests. `nex-fresh-lab` is the fictional hostname typed into the
installer, and `gate-vm-a` to `gate-vm-e` and `gate-kbd-overcap` are fictional VM names; they
appear in captures. Raw evidence stayed in the ignored lab cache and is not published.

| Alias | Meaning |
|---|---|
| host-1 | The nested Nex Core host (`nex-fresh-lab`), pool master |
| pool-1 | Its automatically created pool `nex-fresh-lab` |
| appliance-1 | The Controller appliance VM deployed from host-1 (`nex-controller`) |
| vm-a | `gate-vm-a`: 2 vCPU, 4 GiB dedicated, UEFI + Secure Boot, thin 20 GiB and thick 2 GiB disks, 2 NICs, ISO first, autostart on |
| vm-b | `gate-vm-b`: 1 vCPU, 1 GiB, 4 GiB thin, UEFI, no OS, Start after creation on |
| vm-c | `gate-vm-c`: UEFI + TPM 2.0, no OS, used for a wipe delete |
| vm-d | `gate-vm-d`: UEFI with the ISO attached, no OS, used for the interrupted delete |
| vm-e | `gate-vm-e`: 1 vCPU, 1 GiB, 4 GiB thin, 2 NICs, no OS, created through the API for the BUG-0254 checks |
| `xx:yy` | The VM-derived middle octets of an automatic MAC (`52:54:00:xx:yy:NN`) |
| 192.0.2.10 / .20 / .2 | host-1 management address / appliance-1 / workstation as seen from host-1 |
| `<replacement-port>` | Loopback lab port of the G6 replacement Controller |

## Build under test

Version **0.2.10~gate5**, built by `installer/build.sh` and `packaging/controller/build.py`
from 00:26Z to 00:36Z (ISO build time 00:29:58Z).

| Artifact | SHA-256 |
|---|---|
| `nex-core-0.2.10~gate5-amd64.iso` | `b2d2e72384914752940e2e935db7fff6013990fa17644e6efcd01562d5e84a5f` |
| `nex-core-0.2.10~gate5-source.tar.gz` | `80cb12b61bf6e3ec5c776f0fc53688d44a634f812a794c97d51a828722796788` |
| `nex-core-0.2.10~gate5-compliance-manifest.json` | `2ff93a2ee206cee49dad5c18acce459b80ccec525f7e366d5f090472c6903c5a` |
| `nex-source.tar.gz` (Nex source snapshot) | `46d744c98c4f1ae31bae582fb8d25df97e1cdba2adeeedbfd3f2332044479d46` |
| `nex-core-local_0.2.10~gate5_amd64.deb` | `32be4fb4d478a2fb395996c0d92e8bbbf164fca024dfaf7c4851c4d7925ec767` |
| `nex-controller_0.2.10~gate5_amd64.deb` | `51de586e8467706a0c615286f94172c8e6ab4ce505da2c674162afa390d15e88` |
| Appliance `controller.qcow2` | `a5f53f1f583cea52ed8e19f2ab2a818f629b57680295b75a67a24c3331e3e813` |
| Appliance `manifest.json` | `46d0fcf4a8935b0edc88141d27c94177fad4d0c905cda3c232cc3fb1ba63a249` |
| `nex-controller` binary inside the Controller deb | `11c5d4695b898e4ece3396c121d3c49b9f1377b1baecd32aa4d49e3529745896` |

- `sha256sum -c` passed for the ISO (and its sidecar), source bundle and compliance manifest.
- The deb inside the ISO equals the `dist/` deb (Version `0.2.10~gate5`). The appliance manifest
  reports version `0.2.10~gate5`, and its `image_sha256` matches `controller.qcow2` in `dist/`
  and inside the core-local deb. The source snapshot has one digest everywhere it appears: `dist/`,
  the ISO's product checksum list, both copies in the core-local deb, the appliance directory and
  the Controller deb.
- The Controller package binary reports `CGO_ENABLED=1` (go1.27.1). Smoke starts of a binary built
  from the tree and of the packaged binary with its own UI, each with a private data directory and
  temporary certificate, returned 200 for `/`, `/app.js` and `/source`. The packaged UI is
  identical to `web/dist`.
- Package versions come from the trixie repositories at build time; the build is not
  bit-reproducible.

**Tree identity.** The tree was **uncommitted** (agents do not commit). It is identified by
`HEAD ce0c83c3a33be422c8e74d1ede659db3bdbda5db` plus these fingerprints, identical before and
after the build:

| Fingerprint | Files | Value at build |
|---|---|---|
| Full tree | 1044 | `461010e486427f06c9d0d75b1d6e0e45109c6097edc00f607933ab82a57a8a16` |
| **Product paths** (`internal cmd web/src installer packaging appliance scripts`) | 302 | `43d9e7c016e308a57522f73bad943b48ff3f7f715406f3e6c7e16625998856ef` |

**Method change from gate4.** gate5 fingerprints are the SHA-256 of the sorted per-file
`sha256sum` list of every tracked and untracked, not-ignored file under the paths. gate4 hashed
`git diff HEAD` output and a separate untracked-file list. The gate4 helper was lost when the
workstation rebooted, so the method was rewritten; gate4 and gate5 values **cannot be compared**.
The per-file list at build time was kept with the build records.

### Tree drift after the gated build

At 02:48Z, after the lab run, the product-path fingerprint was recomputed with the same method:
`43d9e7c0…`, **identical to the build**. A per-file comparison with the build-time list found
**0 changed, 0 added and 0 removed** of the 302 product files. The two gate4 post-build changes
(`packaging/systemd/nex-core-agent.service`, `web/src/stats.tsx`) predate the gate5 build and are
part of this gated product.

Files outside the product paths did change after the build and are **not covered by the lab
run**: documentation (this report, guide Verification text, the bug ledger, `CHANGELOG.md`,
`docs/API.md`, plans, and a peer session's usage-history QA and guide pages) and
`website/src/routes/roadmap/+page.svelte` (the public website, not in the product path set).
`go.mod`, `go.sum` and `Makefile` are outside the fingerprint but unchanged since 2026-09-08.

### Tree checks before the build

Run by the build runner on the tree that was built; all exited 0:

- `go vet ./...`, `go test -race -count=1 ./...` (first run; the flaky console test did not fail),
  `bash installer/check.sh`, web `typecheck`, `test` and `build`,
  `python3 -m unittest tests.test_api_contract`, `python3 scripts/generate_api.py --check`,
  `python3 scripts/check_docs.py --worktree` and `--all`, `python3 -m unittest discover -s tests`
  (299 tests), `scripts/run-browser-qa.sh simulated` (6 scripts), `localcore_browser.py
  --blocked-only` and `site_selector_browser.py`. The last two first failed because the system
  Python lacks Playwright (setup, not product) and passed with the browser QA interpreter.

## Environment

- A disposable **nested KVM** lab on a developer workstation (Linux, `/dev/kvm`). host-1 ran in
  QEMU with 16 GiB RAM, 6 vCPU and a new 128 GiB sparse raw disk, **SeaBIOS** firmware, and QEMU
  user networking with loopback-only forwards. The lab's own `br0` exists only inside host-1.
  Older labs were not running after the workstation reboot and were not touched.
- Guests on host-1 used **UEFI** (OVMF with the Microsoft-enrolled variable store, Secure Boot on);
  vm-c used a TPM 2.0 (swtpm). The guest OS was the Debian 13.6 netinst ISO.
- The G6 replacement Controller ran on the workstation from the **gate5 `nex-controller` package
  binary**, listening on loopback.
- Browser: **headless Chromium** through Playwright (a persistent CDP-attached instance for some
  segments), at desktop (1440×1000) and 360 px widths.
- This is **virtual hardware, not physical**: no physical NIC, switch, disk or power-loss
  behaviour is established by this run, and nested timings do not predict physical timings.

## Method

The checklist has 16 rows plus COVERAGE-1, mapped to the Phase 2 gate items G1–G7, the phase
items P2-1 to P2-8, UX-1 and DOC-1. The run had five segments (S1 to S5), each continuing from
the previous segment's lab state. Each row recorded host-reported state (Core journal, `virsh`,
agent state, host files) next to the browser view. Each guide page was walked step by step where
it applies. Every capture cited as "inspected" was opened and looked at.

Harnesses, all in the repository:

- [`fresh_install_lab.py`](../../../tests/integration/fresh_install_lab.py) and
  [`fresh_install_gui.py`](../../../tests/integration/fresh_install_gui.py): lab creation and
  GUI installation.
- [`fresh_install_host.py`](../../../tests/integration/fresh_install_host.py): baseline and
  package/appliance file provenance (`--expected-version`, `--expected-controller`). Expected
  appliance hashes were read from the signed `controller.qcow2` with `virt-ls --checksum=sha256`.
- [`fresh_install_browser.py`](../../../tests/integration/fresh_install_browser.py): deploy,
  managed, create-vm, negative and offline stages.
- [`fresh_install_recovery.py`](../../../tests/integration/fresh_install_recovery.py):
  host reboot, verify-reboot and interrupt-delete.
- [`phase2_gate_guest.py`](../../../tests/integration/phase2_gate_guest.py): guest marker and
  1 s heartbeat read back through qemu-guest-agent.
- [`controller_replacement_debian.sh`](../../../tests/integration/controller_replacement_debian.sh)
  running [`controller_replacement.py`](../../../tests/integration/controller_replacement.py)
  with `--vm`: journal round-trip contract.
- Untracked scratch helpers for row-specific probes (storage table watcher, keyboard wizard pass,
  ISO refusals, queued-create sampler, BUG-0254 editor probes, power and delete dialogs, and a
  host-side inotify journal poller). They are not part of the repository.

**Inputs lost to a workstation reboot.** The session scratchpad that held the gate plan and
decision notes, the gate4 results file and the gate4 scratch helpers was cleared before this
run. Each segment reconstructed its procedure from the gate4 lab state, run log and evidence
layout, and rewrote the scratch helpers. Methods are equivalent to gate4, not identical.

**Harness fixes on gate5:** none. Harness-only fixes from earlier runs are listed in
[run history](#harness-only-fixes-across-earlier-runs).

**Test access.** After the stock root browser login, the harness added one lab SSH key and a
key-only drop-in through the console, for native inspection over a host key pinned against the
VGA console. Both were removed at 02:43Z, effective `PermitRootLogin no` was confirmed, and a
fresh key login without connection reuse was denied. (An earlier re-check had passed only
because it reused an open SSH control connection; that connection was closed.)

## Results

Status: **pass**, **fail**, **partial** or **not run**. Evidence files in this directory are
sanitized text excerpts. Cropped captures were inspected, but the repository ignores images under
`docs/qa`, so they are **not published**: each cited capture name links to its sanitized text
transcription in [captures-text-sanitized.txt](captures-text-sanitized.txt), and the images stayed
in the lab cache. Other references describe raw evidence that stayed in the lab cache.

| Row | Items | Status | Summary | Evidence |
|---|---|---|---|---|
| 1 | G7 (Core) | pass | The gate5 ISO checksum passed, and the GUI install reports `nex-core-local 0.2.10~gate5` and `Nex Core 0.2.10~gate5`; the boot menu reads Nex Core and `core-release` records the ISO installer. `dpkg --verify` is clean, and all 56 regular files match the gate5 deb. The appliance manifest, image and both source copies equal the build table. `fresh_install_host.py --expected-version` passed. The SSH host key on the console equals the scanned key, and the TLS certificate file equals the served one. Install step 8 matched apart from [open defect 2](#open-defects-after-gate5). | Provenance JSON and package hash list (lab cache); `installer-step-list-blank.png` ([text](captures-text-sanitized.txt)) (cropped, inspected) |
| G7 (Controller) | G7 | pass | After deploy, `fresh_install_host.py --expected-controller` passed: all 30 appliance files (listed by `virt-ls` from the signed image) and all 18 core-local service files match. The served Controller certificate equals the ready page fingerprint; the Core certificate is unchanged from row 1. | Provenance JSON (lab cache) |
| 2 | P2-1, P2-2, DOC-1 | pass | Deploy went queued → copying disk → initializing → cleanup → ready, followed by enrollment and appliance adoption; host-owned Local storage and `nex0 untagged` were reused. Two missing paths failed with `lstat …: no such file or directory`. A new directory went queued → healthy in 6 s, and a 75 s watch of the storage table saw one unchanged row set (UI-1). The registered bridge is not offered again, and a duplicate `br0` failed naming `nex0 untagged`. At 360 px the host-local VM table has 3 columns with **Open console** inside its scroll area (UI-12). Management bridge address and default route unchanged. | Captures, `ip addr` (lab cache) |
| 3 | P2-3, UX-1, DOC-1 | pass | Refused with reasons: a non-`.iso` picker name (alert, submit disabled); traversal, slash, `..` and non-`.iso` names (400 at the API); non-ISO9660 content (400, alert "ISO9660 image signature required. Your entries are retained. …"); a declared 8 TiB size and a 64 GiB sparse upload (507); a throttled upload cancelled at 2 % (no media record). Upload by file picker and by synthetic drag and drop both imported on host-1: files 0444 with the source hash, and Controller staging empty. | Captures, host import listing (lab cache) |
| 4 | G1, P2-3, P2-6, UX-1, DOC-1 | pass | vm-a was created through the desktop wizard exactly as specified (not started, autostart on). At 360 px, driven by keyboard, the slash name, topology, memory above the maximum, duplicate disk, invalid MAC and ISO retention checks refused in place and kept entries. A 2000 GiB create, sampled every ~0.15 s, showed **Creation queued**, then **dispatched**, then **Creation failed** ("insufficient free storage including reserve"), with 0 **Edit** or **Delete** buttons and never "Object not found", still failed at +30 s; no host artifacts. Domain: UEFI Secure Boot, locked dedicated memory, ISO boot 1, thin disk-0 (196 KiB used) and fully allocated disk-1. | Domain XML, samples JSON (lab cache); `queued-create-header-360.png` ([text](captures-text-sanitized.txt)) (cropped, inspected) |
| 5 | P2-5 | pass | No VNC or SPICE TCP listener on host-1; consoles use unix sockets. API refusals: no session 401; no cookie on the stream 401; missing or foreign Origin 403; reused, unknown, other-VM and expired tickets 404; stopped VM 409. A valid first use returned 101 with RFB 003.008. The appliance browser console reached its login prompt, and a signed-out popout shows sign-in. | [host-listeners-sanitized.txt](host-listeners-sanitized.txt) |
| 6 | G1, G2 | pass | **Start VM** on the Console toolbar; the console attached and showed the Debian 13.6 UEFI installer menu, but only 9.7 s after the host reported running ([open defect 4](#open-defects-after-gate5)). The install was driven through the browser console only (GRUB edit with canvas keys, preseed parameters with **Type into VM**). The guest's own eject showed "(ejected in the VM)". **Eject** → Empty before Continue; the host drive shows no source and keeps boot order 1; the guest booted shim and the installed GRUB to the login greeter with Secure Boot on. | `console-guest-ejected.png` ([text](captures-text-sanitized.txt)) (cropped, inspected) |
| 7 | P2-7, P2-8, DOC-1 | pass | guest-tools.md step 1 in the guest: both packages present, both services active, Secure Boot enabled. **Guest tools** panel: agent answering, OS, hostname, addresses, clipboard Ready, resize Available. VM usage fresh, Stats 10-minute charts, VM **Activity** lists only this VM's rows (4 audit rows by resource), Task history shows the actor. Host managed checks show a last success. No horizontal scroll at 360 px. With qemu-guest-agent stopped the API read clipboard waiting and resize false; after restart, ready and true. | Captures, audit API rows (lab cache) |
| 8 | G2, G3 | pass | A marker and a 1 s heartbeat were written through `guest()`, and a second marker through the console. The Controller **Reboot** (ACPI) settled succeeded; the domain ID stayed the same while the guest boot ID changed. Both markers re-read unchanged through `guest()` and the console. The one heartbeat gap (60.8 s) covers the reboot until the heartbeat restart. The "Reboot queued" notice cleared on tab change while the operation was still queued. | Marker and heartbeat JSON, captures (lab cache) |
| 9 | G4 | pass | The Controller service was stopped for 107 s. vm-a and appliance-1 stayed running (`virsh` sampled; the appliance answered guest-ping), and the heartbeat showed 146 samples, 0 gaps over 3 s, marker unchanged. The offline stage (host-local root login, running-VM inventory) passed. Host and VMs were fresh 25 s after restart. | Outage sequence and heartbeat JSON (lab cache) |
| 10 | G5 | pass | A vm-b create with Start after creation was submitted in the wizard, then a vm-a reboot was posted with a fixed `request_id` 43 ms before the Controller stopped. During the outage host-1 finished the create; no reboot entry reached the host. After restart the host ran the reboot once (one guest boot change), both tasks settled succeeded, and exactly one vm-b domain exists. Resubmitting the same `request_id` returned 200 with the original operation, no new host entry and no new boot. Task history showed one of each. | Host journal, `virsh`, guest boot list (lab cache) |
| 11 | P2-4b, DOC-1 | **fail** (open major defect 1) | **BUG-0254 checks** on a real host: (a) on never-edited vm-a, stopped, the editor removed interface 1 and the survivor kept its MAC (host XML matches); (b) on never-edited vm-e, stopped, removing an interface and adding an e1000e one in one save gave the new interface a MAC distinct from both originals; (c) on running vm-e, remove plus add of the same model was accepted and Controller and host definitions agree; (d) not checked (see [open defect 5](#open-defects-after-gate5)). vm-a: a rename collision was refused by host-1 ("VM name already exists"), a stopped 3 vCPU / 5120 MiB edit plus an added NIC succeeded, and on the running guest an ISO insert (seen in the guest), a NIC hot-unplug the guest released, and an eject succeeded. **Activity** lists every vm-update, including the refused rename. The extra probe after (c) (c2, an add-only save on the same running VM) **failed**: the hot-plugged interface duplicated a MAC still attached to the live domain while the task reported succeeded ([open defect 1](#open-defects-after-gate5)). needs_attention was not induced. | [nic-duplicate-mac-sanitized.txt](nic-duplicate-mac-sanitized.txt), `nic-single-replace-refusal.png` ([text](captures-text-sanitized.txt)) (cropped, inspected); XML, revisions, captures (lab cache) |
| 12 | G6, G7 | pass (external replacement) | See [G6 in detail](#g6-in-detail). host-1 was adopted by a new Controller running the gate5 package binary: same IDs, names, specs, revisions, XML and journal set, with nothing re-entered. A repeated **Add host** returned 409 with a persistent alert (UI-4). All three **Start** paths on the old appliance ask for confirmation. Stopped edit, live NIC add and two wizard creates succeeded. The repository contract script passed. Same-host redeploy is blocked by the product, and backup restore was not exercised. | [g6-readoption-sanitized.txt](g6-readoption-sanitized.txt), `replacement-protected-appliance.png` ([text](captures-text-sanitized.txt)) (cropped, inspected) |
| 13 | UX-1 | pass | **Negative stage:** anonymous 401, no CSRF 403, invalid create and ISO 400, unknown network 404, exact replay kept the same ID, changed replay 409, counts unchanged. **Offline stage** on the external Controller passed with host-local login; a deep link shows sign-in and a wrong password "invalid credentials". **Stale host:** with `nex-core-local` stopped, the vm-a page read "Current execution state is unknown. Last reported state: running at …" and disabled power, **Edit** and **Delete**, while `virsh` showed vm-a running (UI-6). Every VM icon in the tree turned grey with "State unknown: host not fresh" (BUG-0253). | `stale-vm-warning-360.png` ([text](captures-text-sanitized.txt)) (cropped; last-report time redacted because the browser showed workstation local time; inspected) |
| 14 | G3 | pass | host-1 rebooted, and its boot ID changed. libvirt-guests waited its full 300 s for the OS-less vm-b, then shut down vm-a in 2 s. verify-reboot: host ID, certificate, machine ID and pool unchanged; inactive XML and disk sizes identical for every domain; autostart flags unchanged; vm-a autostarted; the rest, including appliance-1, stayed shut off. Both markers re-read unchanged (one 38.1 s heartbeat gap after the restart). The external Controller saw host-1 fresh; the old appliance's **Start** still asks for confirmation. | Recovery JSON, captures (lab cache) |
| 15 | P2-4a, DOC-1 | pass | From the toolbar on the external Controller, vm-a ran Shut down (Escape first sends nothing), Start, Force stop (Escape first), Start, Reboot and Force reboot. Each sent exactly one request, showed "<Action> queued for gate-vm-a. Follow its progress in Recent tasks.", settled succeeded (2.1–3.1 s where timed) and matched the host journal and guest boot list. Confirm buttons: primary for ACPI actions, danger for force actions (UI-5). The header lags the API by up to the 10 s refresh. | `force-stop-dialog.png` ([text](captures-text-sanitized.txt)) (cropped, inspected) |
| 16 | P2-4c, DOC-1 | pass | **Delete VM** and **Delete…** were disabled with a reason, and API DELETE returned 409, for running vm-b and for appliance-1. vm-b (force-stopped through the API) was deleted with disks retained through the tree menu: empty, wrong and wrong-case names were refused, and the qcow2 was kept. vm-c and vm-e were deleted with their disks from General: disk directories, vm-c's NVRAM and swtpm state were removed. After vm-b's delete the page returned to the host with the notice; the checkbox sat 11 px from its label for vm-b, vm-c and vm-e. vm-a was untouched (XML hash equal). | Host listings, captures (lab cache) |
| COVERAGE-1 | P2-4c recovery | pass | A wipe delete of vm-d (through the API) was pending at the wiping stage when `nex-core-local` was killed (SIGKILL). After restart the entry read wiping pending, then succeeded 24 ms later. A host-side inotify poller read every write (5 events, 5 versions) and **never saw needs_attention**. The Controller operation settled succeeded; the domain and disk directory are gone (vm-d had no TPM and was never started, so it had no NVRAM or swtpm state to remove). The held (`--hold`) and cleaning-NVRAM interrupt variants were **not run**. | Journal versions and interrupt JSON (lab cache) |

### Gate items

| Item | Status on gate5 | Notes |
|---|---|---|
| G1 | pass | Rows 3, 4, 6: uploaded ISO, six-step wizard, Controller start, full browser-driven install. |
| G2 | pass | Rows 6, 8: ticketed console for the install and after it. Console attach lags the host's running report (open defect 4). |
| G3 | pass | Rows 8, 14: marker hashes unchanged across a guest reboot and a host reboot. Heartbeat continuity is proven only across the G4 outage. |
| G4 | pass | Row 9. |
| G5 | pass | Row 10. Limit: the variant with a task still running on the host at reconnect was not achieved, because the create settled about 2 s into the outage. |
| G6 | **partial** | External replacement passed with the gate5 package binary (row 12), and the contract script passed. **Same-host appliance redeploy is blocked by the product (owner decision).** **Backup restore was not exercised.** |
| G7 | pass | Rows 1, G7 (Controller) and 12: Core, appliance and replacement Controller all ran gate5 artifacts with verified hashes, and the product paths did not drift. Limit: the contract script built from the working tree, not the gate5 source snapshot. |
| P2-1, P2-2 | pass | Row 2. The DHCP default and the pre-deploy form at 360 px were not re-observed (static IPv4 deploy). |
| P2-3 | **partial** | Rows 3, 4. The host-side ISO free-space refusal was not reached, because Controller staging refuses first. Drag and drop used a synthetic DOM event. |
| P2-4a | pass | Row 15. The power queue behind pending tasks was not exercised in the lab. |
| P2-4b | **partial, major defect open** | Row 11 failed on probe c2; its other edits and the row 12 edits passed, and BUG-0254 is confirmed fixed within one save. **Open major defect 1:** an interface hot-plugged into a running VM can duplicate a MAC still attached to the live domain, and automatic MACs removed in earlier saves are recycled. Also open: single-interface model replacement returns 400 (open defect 5). needs_attention was not induced; UEFI to BIOS and a combined rename plus Secure Boot edit were not run. Plan item 7 is an owner decision. |
| P2-4c | pass | Row 16 and COVERAGE-1. Not exercised in the lab: the held needs_attention variant, the same-choice retry and the different-disk-choice refusal. |
| P2-5 | pass | Row 5. Not tested: the "not yours" refusal from a second administrator. |
| P2-6 | **partial** | Storage capacity refusal by the host (row 4) and the client memory maximum were shown. **Dedicated-memory admission and stale or unverified resource refusals were not induced.** |
| P2-7 | **partial** | Guest agent states shown (rows 6, 7, 8, 10). A pending-versus-current hardware indicator was not reviewed. The VM details page does not show a pending unplug (open defect 1). |
| P2-8 | **partial** | Inventory, summary, task history, freshness, usage and force-stop separation shown. Whether typed-name confirmation plus appliance protection counts as "deletion protection" (plan item 8) is an owner decision. |
| UX-1 | pass | Rows 3, 4, 13, 15, with the limits below. No pool with zero VMs was available for empty pool tables. |
| DOC-1 | **partial** | The listed pages were walked where they apply. Mismatches: install-nex-core.md step 8 (open defect 2); vm-console.md step 2 "connects as soon as the host reports the VM running" (open defect 4); edit-virtual-machine.md step 2 says a new automatic MAC never reuses one removed **in the same save**, which holds, but reuse across saves is not mentioned (open defect 1); the single-interface replacement 400 was undocumented. Guide Verification text was stale (open defect 6); it was updated after the run in this change, and two Troubleshooting entries were added, without a new walkthrough. verify-nex-core.md steps 3–4 were not walked. |

## G6 in detail

**External replacement: PASS**, following the owner-approved G6 procedure (reconstructed after
the reboot from the gate4 records) and [manage-controller.md](../../guide/manage-controller.md):

0. Snapshots of host-1 (agent state, journal, pool VM inventory, `virsh` XML hashes, disk and
   interface lists) and of the Controller API were taken.
1. appliance-1 was excluded: autostart disabled and shut down from a root shell on host-1 (over
   the lab SSH session, not the physical console) at 02:15Z. Its disk is kept, and the other VMs
   kept running.
2. A new Controller was started on the workstation from the **gate5 `nex-controller` deb**: binary
   sha256 `11c5d469…`, `CGO_ENABLED=1`. It used a new data directory and administrator password,
   and a chained `-tls-cert` file (server certificate followed by its issuing CA). It listened on
   loopback, and host-1 reached it as `https://192.0.2.2:<replacement-port>`.
3. Sign-in showed no resources. The operator created a site and used **Add host** through the
   lab's loopback forward to host-1. The fingerprint shown matched the Core certificate pinned at
   installation, and the host reported fresh at once. A repeated **Add host** returned 409, and
   its "Already enrolled…" alert stayed visible at 1, 12 and 25 s.
4. The diff ([g6-readoption-sanitized.txt](g6-readoption-sanitized.txt)) shows these
   **unchanged**: host, pool, storage, network, ISO and VM IDs and names; VM specs and revisions;
   inactive XML hashes; the host journal set. Only these differ:
   - Controller-scoped data: the site;
   - trust: the certificate and the Controller URL;
   - adopt operation and request IDs;
   - appliance-1's run state and autostart;
   - vm-b's one-shot `start_after_create`.

   Failed registrations came back only as failed tasks with their original errors.
   Non-succeeded operations are exactly the 5 original failures; nothing reads needs_attention,
   and the host's managed checks are healthy.
5. appliance-1 is shut off with **Edit** and **Delete** disabled; API DELETE returned 409.
   **Start** from the toolbar, the Console tab's **Start VM** and the tree context menu each opened
   the two-Controllers warning (`replacement-protected-appliance.png` ([text](captures-text-sanitized.txt))).
   Each was cancelled, no non-GET request was sent, and the appliance stayed shut off.
6. On the new Controller: a stopped memory edit on vm-e, a live NIC add on vm-a, and wizard
   creation of vm-c (UEFI + TPM 2.0) and vm-d (ISO attached) on the adopted storage, network and
   ISO all succeeded. vm-c was started once and force-stopped, leaving NVRAM and swtpm state for
   row 16.
7. appliance-1 was still shut off with autostart disabled at the end of row 12, after the host-1
   reboot in row 14 and at cleanup.
8. The **repository** `controller_replacement_debian.sh` with `--vm` **passed** both phases:
   Controller A enrolment and definition, then Controller B re-adoption plus new operations. It
   built from the working tree, not the gate5 source snapshot.

**Same-host appliance redeploy: BLOCKED BY PRODUCT (owner decision).** Host-local Deploy is
disabled once a deployment is recorded, the local deployment API refuses a host with agent
identity, and there is no supported appliance removal path. Found on gate1 and not attempted
since.

**Backup restore to a replacement Controller: NOT EXERCISED.**

## Fixes confirmed in lab across runs

| Finding | Ledger | First confirmed | gate5 evidence |
|---|---|---|---|
| UI-1: stale queued storage row | BUG-0236 | gate3 | Row 2, 75 s watch |
| UI-2: failed create became "Object not found" | BUG-0237 | gate3 | Row 4, failed panel at +5, +15, +30 s |
| UI-3: VM Activity omitted the VM's own events | BUG-0238 | gate3 | Rows 7, 11 |
| UI-4: Add host swallowed 409 | BUG-0239 | gate3 | Row 12 |
| UI-5: power confirmations lost danger/primary styling | BUG-0240 | gate3 | Row 15 |
| UI-6: stale warning said "Last reported state: unknown" | BUG-0241 | gate3 | Row 13 |
| UI-12: host-local VM table clipped Open console at 360 px | BUG-0242 | gate3 | Row 2 |
| CORE-1: clipboard/resize Ready with no guest OS | BUG-0243 | gate3 | Rows 6–8: firmware, GRUB, stopped agent, guest reboot |
| CORE-3: healthy managed checks showed no last success | BUG-0244 | gate3 | Row 7 |
| GM-10: no "Delete queued" notice | BUG-0245 | gate3 | Row 16 |
| CTRL-DEB-CGO: Controller package binary could not start | BUG-0246 | gate4 | Row 12: the gate5 package binary ran the whole replacement |
| Transient needs_attention on a resumed delete | BUG-0247 | gate4 | COVERAGE-1 |
| Queued create showed VM details with Edit and Delete | BUG-0248 | gate4 | Row 4: 89 samples, 0 details, **Edit** or **Delete** |
| "Start queued" notice stayed after success | BUG-0249 | gate4 | Rows 6, 8 |
| Guest tools badge "stale" on a fresh VM | BUG-0250 | gate4 | Rows 6, 8, 10: 40 samples, never stale |
| Unconfirmed Start of the old appliance | BUG-0251 | gate4 | Rows 12, 14 |
| Delete dialog checkbox far from its label | BUG-0252 | gate4 | Rows 2 (1440 and 360 px) and 16 |
| Running icon on a stale host | BUG-0253 | gate4 | Row 13 |
| **Editor remove plus add refused (400) or given the removed MAC** | **BUG-0254** | **gate5**: fixed within one save only | Row 11 checks (a)–(c); (d) not checked. Reuse of a MAC removed in an **earlier** save remains (probe c2 handed out `:01` one save after it was removed): open defect 1 |
| Guide and wording items GM-01, GM-02, UI-7, UI-8, UI-9, UI-10 | — | gate3 | Rows 2, 3, 4, 7 (UI-9 banner by its logged text) |

**The gate4 "major" was not reproduced.** The gate4 final code review reported that the editor
cannot remove or change a network interface on a never-edited VM. Checking it showed that
`GET /api/v1/vms` already returns each interface's effective MAC and the editor sends it back, so
removal or change alone was never refused; a Controller test and a simulated browser step now
cover that round trip. The real defect found while checking it is BUG-0254: a save that removed
one interface **and** added a new one was refused with 400 when the new interface differed from
the old one at that position, or otherwise could give the new interface the removed interface's
MAC, and the Controller ran its stop check on a different definition from the one sent to Core.
It was fixed before the gate5 build and walked on host-1 in row 11 (checks a–c). The fix holds
within one save only; reuse across saves remains part of open defect 1. Check d, single
interface replacement, is a known limitation, not part of the fix.

## Open defects after gate5

One **major** and the rest **minor**. None is fixed in the gated build.

1. **Major: an interface added to a running VM can reuse the MAC of an interface the guest has
   not released, putting a duplicate MAC on the bridge.**
   - **Symptom:** on a running VM whose guest has not yet released a removed interface, the next
     **Add network interface** gets the same automatic MAC. It is hot-plugged, so the live domain
     has two interfaces with one MAC on `br0` (the old one still pending unplug, and the new one).
     The task reports succeeded, and the Controller and VM details page show one interface fewer
     than the live domain, with no sign of the duplicate. Related: automatic MACs removed in
     earlier saves are recycled in general (vm-e's next new interface got `xx:yy:00`, removed in
     an earlier save; vm-a's re-added interface got its old `:00`).
   - **Repro:** create a VM with two automatic interfaces and no OS; edit it once while stopped so
     it holds `:01` and `:02`. Start it; the guest stays at firmware and never releases devices.
     In the editor remove `:01`, add a virtio interface and save (accepted; new MAC `:00`; the
     live domain keeps `:01`). Then **Add network interface** only and save. The API shows three
     interfaces `[:02, :00, :01]`; `virsh domiflist` shows `:01` twice on `br0`.
   - **Evidence:** [nic-duplicate-mac-sanitized.txt](nic-duplicate-mac-sanitized.txt) (probes c
     and c2 on vm-e, row 11); the vm-e details capture was inspected in the lab cache.
   - **Affected releases:** code reading indicates it is present in released 0.2.9 as well (same
     live attach and detach logic, and a Controller that recycles MACs even more readily). This was
     **not reproduced in a lab on 0.2.9**. BUG-0254 did not introduce it.
   - **Suspected cause (code reading, not yet confirmed by a fix):** primarily **Core**. The live
     attach and `validateManagedDefinition` read only the inactive definition, and a detach the guest
     never confirms is recorded as succeeded, so Core hot-plugs a MAC still attached to the live
     domain. The **Controller** aggravates it: `updateVM` picks the lowest derived MAC not in the
     request or current definition, so MACs removed in earlier saves are handed out again.
   - **Plausible, untested:** a **different VM** given the same MAC manually while the unplug is
     pending passes the conflict checks, which scan persistent definitions only, so the duplicate
     could span VMs.
   - **Not measured:** the effect on real guest networking (bridge MAC flapping).
   - **Handling:** not fixed in the gate5 build. See the
     [owner triage](#owner-triage-open-defect-1) below.
   - **Status after gate5: fixed in the tree, not lab-verified** (BUG-0256 in the
     [bug ledger](../../BUG_LEDGER.md)). Triage F1 without its cross-VM scan, plus F2: while
     validating a running guest's edit, before allocating disks or attaching anything, Core
     compares the live and persistent domains and reports the `vm-update` failed, changing
     nothing, when a requested interface's MAC is still on a live interface ("network interface MAC … is still
     attached to the running VM; wait for the guest to release it or stop the VM, then save
     again"); the Controller draws new automatic MACs above the highest index the current definition uses,
     falling back to the lowest free index. Evidence is
     fake-virsh and Controller unit tests only. Still required: a gated rebuild and a rerun of
     row 11 (including probe c2) and the G7 rows. Still open: the cross-VM manual-MAC variant
     (F1's scan of other running domains) and a pending-unplug indicator (P2-7). The
     [edit guide](../../guide/edit-virtual-machine.md#troubleshooting) now describes the refusal.
2. **The installer step list loses its current step and ticks between backend steps.** On gate5
   the progress panel showed "Preparing the next installation step…" with no current-step bullet
   and no ticks once for about 4 s and once for about **20–24 s** (frames every 4 s), then
   returned to the right step. gate4 saw 4–8 s. This contradicts install-nex-core.md step 8.
   `installer-step-list-blank.png` ([text](captures-text-sanitized.txt)) (cropped, inspected).
3. **Queued and failed create page residuals.** While queued, the page shows the VM name,
   "Observed unknown · fresh. The VM is unknown; resolve that state on its host before a power
   request." and the tabs above the **Creation** panel, and the tree lists the VM. After failure a
   "Your workspace" heading sits above **Creation failed**. The page kept showing **Creation
   dispatched** until +10.5 s although the host refused the create at +1.2 s (10 s refresh).
   `queued-create-header-360.png` ([text](captures-text-sanitized.txt)) (cropped, inspected).
4. **Console attach after Start VM waits for the 10 s UI refresh.** The host reported the VM
   running 4.6 s after **Start VM**, but the console began connecting at 13.3 s and connected at
   14.3 s, and the "Start queued" notice stayed until then. vm-console.md step 2 says the tab
   connects as soon as the host reports the VM running; a 5 s BIOS "Press any key" prompt could be
   missed (not testable on this UEFI guest). Same cause as the header lag below.
5. **Replacing a VM's only interface with a different adapter model in one save returns 400**
   "when removing, reordering or changing a network interface, give every existing interface its
   MAC address". The request names no current MAC, so it cannot be told apart from an unnamed
   model change. Workaround: add the new interface, save, then remove the old one. Now documented
   in the edit guide's Troubleshooting. `nic-single-replace-refusal.png` ([text](captures-text-sanitized.txt))
   (cropped, inspected).
6. **Guide Verification text was stale after gate5:** edit-virtual-machine.md said BUG-0254 had
   not been walked on a real host, manage-controller.md cited only gate4, and
   delete-virtual-machine.md listed only unit coverage. Corrected in this change (Verification
   text and two Troubleshooting entries only).

### Owner triage: open defect 1

Read-only triage by code reading on 2026-09-17 against the gate5 tree; no lab repro beyond row 11.
Line references are to the gate5 tree.

- **Root cause.** Controller and Core both judge "MAC in use" from the persistent definition only,
  while `virsh detach-device --persistent` drops the interface from that definition at once and the
  live removal waits for the guest.
  1. **Core (primary).** `applyLive` in `internal/vmhost/managed.go` reads only
     `dumpxml --inactive` and attaches every wanted MAC missing from that set, even when the live
     domain still carries it; QEMU and libvirt do not refuse a duplicate MAC.
     `validateManagedDefinition` and `vmConflict` also check persistent definitions only, so the
     operation records succeeded.
  2. **Controller (aggravating).** The `updateVM` freeze loop in `internal/controller/managed.go`
     picks the lowest `protocol.DerivedMAC(id, k)` not in the request or current definition, so a MAC
     dropped in an earlier revision is free again. At 0.2.9 it ignored current MACs too.
- **Simpler repro (code reading):** on a running VM with `[:00, :01]` whose guest does not
  acknowledge unplug, save 1 removes `:00`; save 2 adds an interface, which is given `:00` while it
  is still live.
- **Blast radius.** Same VM, same bridge (observed): two tap ports share one MAC, so the bridge
  forwarding table flaps if both transmit. Guests still using the old interface may lose return
  traffic or see duplicate DHCP identity. Other VMs: plausible, untested (above). Hidden from
  operators: the task succeeds and the UI shows one interface fewer than libvirt. Not persistent:
  the duplicate lasts until the guest releases the device or QEMU stops; no disk or data loss seen.
- **Smallest safe fix, Core first.**
  - **F1 (required, Core, existing contract with a new failure reason):** at the top of
    `applyLive`, read the live domain once and refuse, before any attach or detach, to attach a MAC
    that is still live but absent from the persistent definition ("still attached to the running
    guest waiting to be released; wait for release or stop the VM, then save again"). Extend the
    create and update MAC uniqueness scans to running domains' live XML to close the cross-VM
    variant. `state.owner: none`.
  - **F2 (recommended, Controller):** allocate new automatic MACs above the highest derived index in
    the current host-owned definition instead of the lowest free index. `state.owner: none`.
  - **F3 (owner decision):** never reuse any MAC a VM held, which needs a Core-side per-VM
    high-water mark reported through inventory (changed contract, `state.owner: core`). A
    Controller-only history is rejected: it would not survive Controller replacement.
- **Regression tests:** fake-virsh unit tests in `internal/vmhost/managed_test.go` (refuse attach
  of a MAC still live after detach with no libvirt calls; attach succeeds once released; removal-only
  still succeeds; cross-VM manual MAC refused), a Controller test for F2, and a lab walk of the
  two-save repro, the release-then-retry path, a Linux guest that releases at once, and the
  cross-VM variant.
- **Operator workaround until fixed:** after removing an interface from a running VM, stop the VM
  before adding another interface, or give the new interface a manual MAC this VM or another VM
  never used recently.
- **Gate impact:** blocks Phase 2 closure. F1 with its tests and a lab walk on a new gated build
  closes the safety issue; F2 and F3 are owner choices.

### Carried from earlier runs

7. **AGENT-1:** idle `runuser` PAM session flood from storage access probes, not re-measured on
   gate5. Waiting on an owner decision.
8. **A host reboot waits 300 s for an unresponsive guest** (libvirt-guests timeout; seen again in
   row 14).
9. **The VM header lags the API after an action settles**, until the next 10 s refresh (seen
   again in row 15).
10. **Cosmetic:** after the erase box is checked, the installer kept showing the red confirmation
    text from the unconfirmed attempt (gate3, gate4; not re-checked on gate5).

### Also observed on gate5, not triaged as defects

- At `nex-core-local` restart the journal logs "pool journal scope recovery: managed workload or
  remote storage journal cannot be rehomed", as on gate3 and gate4. The delete then completed.
- The interrupted delete's journal `retained` field was null in every version, where gate4 listed
  `disk-0`. Informational; the wipe was complete on the host.
- `BUG-0254` in the bug ledger still reads "not yet walked on a real host"; the ledger owner
  should update it with the row 11 result.

## Untested limits

- Physical hardware, physical LAN and switch VLANs, power loss, and UEFI or Xen host
  installation. The Core host used SeaBIOS only, and the minimal text installer was not run. The
  install progress panel was sampled every 4 s, which can miss shorter transitions.
- Files outside the product paths that changed after the build (see
  [tree drift](#tree-drift-after-the-gated-build)).
- Deploy with DHCP and the pre-deploy form at 360 px (static IPv4 deploy).
- Host-side ISO free-space refusal (Controller staging refused first). Drag and drop used a
  synthetic DOM event with a real file, and traversal names were tested at the API.
- The 360 px wizard was keyboard-driven with focus, typing, type-ahead and Enter, not strictly
  Tab-only; the desktop wizard was driven by the harness managed stage.
- The console "not yours" refusal from a second administrator. The expired-ticket message was not
  captured in the UI. No real-keyboard canvas typing beyond GRUB navigation keys. A BIOS 5 s
  "Press any key" prompt with **Start VM**.
- Clipboard paste delivery and **Resize guest to window** in a signed-in desktop session (Ready
  was observed at the login greeter). A guest that never had qemu-guest-agent was not observed;
  the missing-agent state came from stopping the service.
- P2-6 dedicated-memory admission and stale or unverified resource refusals.
- G5 with a task still running on host-1 at reconnect.
- P2-4b needs_attention, UEFI to BIOS, a combined rename plus Secure Boot edit, and the
  failed-create **Edit** refusal wording. BUG-0254 checks b and c used API-created vm-e with no
  OS; a Linux guest releasing a removed interface was covered only on vm-a. The duplicate-MAC
  defect's effect on guest networking was not measured.
- The UI-9 eject banner is evidenced by its logged text; its capture scrolled past it.
- G6 conditions: the replacement Controller bound to loopback, and headless Chromium ignored
  HTTPS errors. The host was added through a loopback forward, and the exclusion ran from an SSH
  shell. **Same-host redeploy and backup restore were not run.** The contract script built from
  the working tree.
- Interrupted-delete paths: the held needs_attention and cleaning-NVRAM variants, same-choice
  retry, different-disk-choice refusal and storage detach. Row 16 had no dedicated pre-delete host
  capture; the pre-state comes from earlier row records.
- Only one host reboot. Row 15 ACPI actions used a Debian GNOME guest only. The row 13 offline
  stage covers an external Controller process outage, not the appliance Controller.
- The power queue behind pending tasks. vm-b's retained qcow2 was not reattached or booted.
- Some actions were driven through the API rather than the browser, to control timing: vm-e's
  creation, vm-b's force stop before its delete, the vm-d interrupted delete, the G5 reboot and
  the console ticket refusals.
- Pool-level empty tables, because no pool had zero VMs.
- Heartbeat continuity is proven **only** across the G4 outage. The heartbeat was restarted after
  each guest reboot and the host reboot; persistence is shown by marker hashes.
- AGENT-1 PAM session rate not re-measured.
- Headless Chromium only.

## Open owner decisions

1. **G6 same-host retire:** a same-host Controller appliance redeploy or removal path, and whether
   G6 is met by external replacement without it and without a backup restore.
2. **AGENT-1:** fewer `runuser` PAM sessions from a storage access probe cache, versus slower
   detection of access loss.
3. **CORE-1 tradeoff:** a guest running spice-vdagent without qemu-guest-agent reads clipboard
   waiting and resize unavailable.
4. **Plan item 7:** whether refusing stop-first changes on a running VM, instead of queuing them,
   meets the edit requirement.
5. **Plan item 8:** whether typed-name confirmation plus appliance protection counts as "deletion
   protection".
6. **Published `nex-controller` packages 0.1.6~alpha1 to 0.2.9 (BUG-0246):** whether to pull or
   supersede them, and how to tell affected operators to recover.
7. **Core-side refusal of appliance Start:** the fix is a Controller UI confirmation only. Whether
   Nex Core should also refuse starting a Controller appliance that another Controller has
   replaced.
8. **Release workflow smoke start:** whether the release workflow should start the packaged
   Controller binary (and check `CGO_ENABLED=1`) before publishing, as the gate builds did by hand.
9. **Network interface model:** whether to replace the editor's single-interface model so a
   request can mark interfaces as new (open defect 5), and how open defect 1 should be fixed (see
   the [owner triage](#owner-triage-open-defect-1)).

## Remaining gate requirements outside this lab

- **Open major defect 1** fixed with a regression test, a **new gated build**, and a rerun of at
  least row 11 (including the running-VM remove, add and add-again sequence with a guest that does
  not release the device) and the G7 provenance rows.
- **Independent review** of this report, the API impact and `state.owner` decisions, guide accuracy
  and walkthrough evidence, by a reviewer who did not author the work. Findings must be resolved
  and verified.
- **Independent slice reviews** required by the gate plan on the committed tree: VM edit, VM
  delete, usage history and the Windows 11 batch.
- **Hosted CI** on both GitHub Actions and Gitea Actions after the owner pushes: phase1 (including
  the simulated browser jobs and the Controller replacement contract) and documentation, with job
  logs inspected for skips.
- **Owner commit** of the gated tree. Its product paths must match the gate5 product-path
  fingerprint (`43d9e7c0…`, fp method above), or the difference needs its own checks or a new
  gated build.
- **Flaky test:** `TestHostConsoleIncrementalUpdateOnlyOnChange` failed once before the gate4
  build and passed on rerun; it did not fail before gate5. It still needs a fix or a ledger entry
  before CI can be trusted to stay green.

## Run history

1. **gate1 (0.2.10~gate1).** Built from the uncommitted tree at the same HEAD with a different
   fingerprint. The full checklist ran in an earlier disposable lab. Rows 1, 12 and 15 were
   partial and the rest passed. The G6 substitute was a new Controller inside the same appliance
   VM, because same-host replacement is blocked. Findings fixed in round 2 are BUG-0236 to
   BUG-0245. AGENT-1 was deliberately not implemented, pending an owner decision.
2. **gate2 (0.2.10~gate2).** Built and verified (ISO
   `0aea2dd122c0885f17fed6f495bd549609021bda7a8ed6cd03d0b9419c38b88b`). The working tree changed
   afterwards, so gate2 was superseded without a lab run.
3. **gate3 (0.2.10~gate3).** ISO `81d8ac9abc77ef777c3e7df9ce36f857d82ed84a65fb16a54b57ecce8f40ed38`.
   The full checklist ran from a new blank lab. All 16 rows passed and COVERAGE-1 was partial. G6
   external replacement passed, but with a Controller rebuilt locally from source, because the
   gate3 package binary could not start. Its findings were fixed as BUG-0246 to BUG-0253.
4. **gate4 (0.2.10~gate4).** ISO `7371782ca6902e588555d7a6f6e34249398ab5c779df8cc9a4c8a13a1aa9cc85`,
   Controller deb `08995158875a50351410b9a832fdcbebc6d953e9c785653c0779aff427f0dd76`, product
   fingerprint (gate4 method) diff `5ca55c35…` and untracked `c135d119…`. Pre-build
   `go test -race` hit the flaky console test once. The full checklist (rows 1–16, COVERAGE-1)
   passed from a new blank lab, and G6 external replacement passed with the gate4 package binary.
   Two product files changed after that build (`nex-core-agent.service`, `stats.tsx`); both are
   in gate5. The final code review then reported a major editor defect (below), so gate4 could
   not close Phase 2.
5. **gate5 (0.2.10~gate5).** This run; the result of record.

### Open defects after gate4

Kept for history; other documents link to this list. The status after gate5 is in brackets.

1. **Major (code review): the VM editor cannot remove or change a network interface on a
   never-edited VM.** [**Not reproduced.** The listing already returns effective MACs. The real
   remove-plus-add defect is BUG-0254, fixed and confirmed in gate5 row 11.]
2. **The installer step list goes blank between backend steps** (4–8 s twice). [Open; up to about
   24 s on gate5, open defect 2.]
3. **The queued-create page shows the VM header and tabs above the Creation panel**, and a
   "Your workspace" heading above a failed create. [Open; open defect 3.]
4. **AGENT-1** idle `runuser` flood. [Open; owner decision.]
5. **A host reboot waits 300 s for an unresponsive guest.** [Open; seen again.]
6. **The VM header lags the API after an action settles.** [Open; seen again, and the same cause
   delays console attach, open defect 4.]
7. **Cosmetic:** installer red confirmation text after the erase box is checked. [Not re-checked.]

### Open defects found on gate3

Kept for history. The status after gate5 is in brackets.

1. **Major:** the `nex-controller` deb binary was built without cgo and exited at start
   (BUG-0246). [Fixed; confirmed on gate4 and gate5.]
2. **Minor:** AGENT-1 `runuser` PAM sessions. [Open; owner decision.]
3. **Minor:** a queued create showed VM details with Edit and Delete (BUG-0248). [Fixed; header
   and tabs residual open.]
4. **Minor:** the "Start queued" notice stayed after success (BUG-0249). [Fixed.]
5. **Minor:** the Guest tools badge read stale on a fresh VM (BUG-0250). [Fixed.]
6. **Minor:** a replacement Controller offered an unconfirmed Start for the old appliance
   (BUG-0251). [Fixed.]
7. **Blocker for CI (harness):** `controller_replacement_debian.sh` lacked `docker run --init`.
   [Fixed; the repository script passed on gate4 and gate5. Hosted CI has not run.]
8. **Minor:** an interrupted wipe delete briefly read needs_attention after restart (BUG-0247).
   [Fixed.]
9. **Minor:** the delete dialog checkbox rendered far from its label (BUG-0252). [Fixed.]
10. **Minor:** the tree showed shut-off VMs on a stale host with the running icon (BUG-0253).
    [Fixed.]
11. **Minor:** a host reboot waits the full 300 s libvirt-guests timeout. [Open.]
12. **Minor:** the VM header lags the API until the next refresh. [Open.]
13. **Minor:** a "Your workspace" heading above the failed-create panel. [Open.]
14. **Guide mismatches:** install-nex-core.md steps 8 and 10, vm-power.md step 5, and
    manage-controller.md step 1, trust description and old-appliance warning. [Corrected after
    gate3; step 8 still mismatches between backend steps. Step 1 is walked with the package binary
    rather than `make build`.]

### Harness-only fixes across earlier runs

No product code:

- **gate1:** `fresh_install_lab.py` `--memory-mib` and `--vcpus`. `fresh_install_host.py` and
  `fresh_install_recovery.py` check `nex-core-local` and `libvirtd.socket` instead of the
  socket-activated `libvirtd` service. `provenance()` takes the gate package path and an
  expected Controller hash file. `fresh_install_browser.py` gained gate VM names, reuse of
  host-owned registrations and imported ISOs, exact row matching, the TPM select lookup and a
  truly changed negative-stage replay. `fresh_install_recovery.py` tolerates an undefined domain
  after controller-stop.
- **gate3:** `fresh_install_browser.py` gained `--site-name` and preset `d`, and its offline stage
  accepts any running VM. `fresh_install_recovery.py`'s reboot stage accepts an excluded
  appliance, and verify-reboot asserts each domain's autostart flag.
- **After gate3:** `controller_replacement_debian.sh` runs its container with `docker run --init`.
  `tests/test_fresh_install_recovery.py` covers the gate3 reboot and verify-reboot changes.
- **gate4:** the `fresh_install_browser.py` create flow waits up to 300 s for VM details while the
  **Creation** panel shows.
- **Recorded, not fixed:** the `post-reboot` browser stage still assumes a running appliance.
  The `fresh_install_recovery.py` docstring still says interrupt-delete was not run in a lab.
