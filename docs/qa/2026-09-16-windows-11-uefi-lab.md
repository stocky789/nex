# Windows 11 UEFI guest on a fresh Nex Core 0.2.7 lab

Date: 2026-09-16. Exploratory disposable-lab walkthrough. It records issues and does not accept
a feature. No product code changed.

## Environment

| Item | Value |
|---|---|
| Core | `nex-core-0.2.7-amd64.iso` (checksum sidecar OK), GUI install onto a new blank 128 GiB raw file, BIOS host firmware, nested KVM, 24 GiB RAM, 8 vCPU, QEMU user networking with loopback forwards |
| Controller | Deployed from the Core local UI (static 10.0.2.20/24), reached **ready**, pool master adopted |
| Guest media | `en-us_windows_11_iot_enterprise_ltsc_2024_x64_dvd_f6b14814.iso`, uploaded through **ISO library → Upload ISO** (5.14 GB in 50 s; SHA-256 matched the source); `virtio-win-0.1.302.iso` by URL download |
| Guest A | `win11-ltsc-uefi`: Windows, 4 vCPU host-passthrough, 8192 MiB dedicated, 64 GiB SATA thin, Intel E1000E on `br0`, UEFI, ISO first in boot order. Created through the Create VM wizard |
| Guest B | `win11-virtio-compare`: 2 vCPU, 4096 MiB maximum, 32 GiB VirtIO disk, VirtIO NIC, UEFI. Created through `POST /api/v1/vms` |
| Method | Playwright against the Controller web UI and VM console only (mouse, keyboard, typing, CD/DVD toolbar); host-side `virsh` reads for evidence |

## Result

Windows 11 IoT Enterprise LTSC installed to the desktop on guest A, under UEFI with Secure Boot
(Microsoft-enrolled OVMF, chosen automatically by libvirt). All Setup and first-run pages were
driven through the Controller console. Inbox drivers covered SATA and E1000E, and first-run
setup reached Windows Update over `br0`. The Core journal showed no failed units or libvirt errors.

The standard Windows 11 editions on the same ISO cannot install. VirtIO installs, the boot
prompt, display resize and several messages have the issues below.

## Issues

Severity: **blocker** stops a supported Windows workflow; **major** needs an undocumented
workaround; **minor** is cosmetic or wording.

| ID | Severity | Area | Issue | Evidence and cause |
|---|---|---|---|---|
| W1 | blocker | Core VM definition | Setup refuses **Windows 11 Enterprise LTSC** (and any non-IoT Windows 11 edition): "This PC doesn't currently meet Windows 11 system requirements". Only the IoT editions, which have relaxed requirements, install | Guest A had UEFI, Secure Boot, 4 vCPU, 8 GiB and 64 GiB, so the unmet requirement is almost certainly TPM 2.0 (inferred from the requirement list, not from Setup logs). Nex defines no `<tpm>` device anywhere, and `swtpm` is not installed on Core 0.2.7 (`dpkg -l swtpm` → not installed; absent from `dist/package-manifest.tsv`) |
| W2 | major | Console / boot | Starting the VM, then opening the Console, misses the ~5 s "Press any key to boot from CD or DVD" prompt. The guest falls through to the empty disk and stops at "BdsDxe: No bootable option or device was found" or the OVMF front page. Reproduced after **Start** and after Setup's own reboot | Workaround verified on guest B: open the Console first, press **Start** in the toolbar and hold a key. The operator otherwise has to navigate OVMF **Boot Manager → UEFI QEMU DVD-ROM** by hand. "Start after creation" guarantees the miss. The guide does not mention it |
| W3 | major | VM wizard / media | With a VirtIO disk, Setup lists no disks. A VM has one CD drive, so the virtio-win driver ISO cannot be attached alongside the install ISO | Workaround verified to **Ready to install**: in the running Setup, swap the CD to virtio-win with the console toolbar, **Load driver → D:\amd64\w11** (Red Hat VirtIO SCSI controller), swap back to the Windows ISO, then **Next**. The wizard still defaults new disks and NICs to VirtIO when Guest OS is Windows; the only guidance is help text |
| W4 | major | VM definition / UI | Secure Boot is silently on. libvirt picked `OVMF_CODE_4M.ms.fd` with the enrolled-keys VARS and SMM. Nex neither shows nor controls it: General shows only "UEFI". No option exists to turn it off for unsigned boot media or drivers | `firmware='efi'` is the only input (`internal/vmhost/managed.go` `managedDomainXML`) |
| W5 | major | ISO URL download | The official virtio-win "stable" link fails in about 2 s with "ISO download failed: connection, redirect or timeout" | The server redirects HTTPS to plain HTTP. Core refuses the downgrade by design (`internal/vmhost/iso_download.go` `CheckRedirect`), then `fetchPublicISO` replaces that error with a generic message, so a policy refusal looks like a network fault. The host had DNS and TCP 443 to the site. The direct HTTP URL then downloaded in 120 s |
| W6 | major | Console / VM details | Before virtio-win drivers are installed, "Resize guest to window" is offered, General says "Display resize: Available · VirtIO display" and the API returns `guest.resize: true`, but the guest never resizes | With the option selected at an 1100 px viewport, the guest framebuffer stayed 1280×800 after 12 s (canvas and host screenshot). After the virtio-win driver installer finished, it followed the console panel (836×400). The availability claim comes from the device model, not from a guest driver |
| W7 | minor | Post-install | After install the Windows ISO stays inserted with the CD first in boot order. Every boot shows "Press any key to boot from CD or DVD"; a keypress in the console restarts Setup | Nothing prompts an eject or reorder after first boot. The operator must use **Edit** |
| W8 | minor | ISO library | A failed URL download leaves a permanent `virtio-win.iso · failed · 0 bytes` row, with no delete or retry | Controller media routes are only `GET/POST /api/v1/media` and `POST /api/v1/media/download` |
| W9 | minor | ISO library | After the upload's host transfer succeeded, the banner still reads "host transfer queued. Wait for a succeeded result before creating a VM" | Same page shows the `iso-import` task succeeded |
| W10 | minor | Storage naming | The automatically registered local repository on a fresh host is named **ISO**, although it also holds VM disks (`vm_disk` and `iso` capabilities). Windows system disks land on "ISO" | Fresh `GET /api/v1/storage` |
| W11 | minor | VM details copy | A new dedicated-memory VM created on Core 0.2.7 shows "Existing VMs: upgrade Core, stop, then Edit → Save changes" | General tab |
| W12 | minor | Console layout | At 1440×1000 the console's "Open in new window" link is clipped under the tab strip, and the **Recent tasks** drawer covers the lower console area. With **Clipboard** open, only a thin strip of the guest display stays visible above the drawer, so text typed into the VM cannot be watched | Console tab captures |
| W13 | minor | Controller login | The login page requests `GET /api/v1/session` → 401 before sign-in, logging a browser console error on every load | Playwright response log |
| W14 | minor | Installer UI | After **Erase disk and install**, progress and completion use a different sidebar (Region/Network/Access/Storage/Installation) from the wizard (Welcome/Host/Network/Disk/Review), and the completion page still offers **Go Back** | Installer captures |
| W15 | note | Windows VM defaults | Guest OS = Windows changes no devices: clock `offset='utc'` (Windows keeps local time in the RTC), no Hyper-V enlightenments, VirtIO video with no inbox Windows driver | Domain XML of guest A |
| W16 | minor | Guest facts | After the guest tools were installed, a Windows 11 guest is reported as "Windows 10 IoT Enterprise LTSC 2024" | `GET /api/v1/vms` `guest.os`. The agent's product name comes from a registry value Windows 11 did not rename; Nex shows it verbatim instead of using the build number |
| W17 | major | Console **Type into VM** | Characters that need Shift arrive unshifted in a Windows guest. Sending `Nex-Test_Upper D:\Path\virtio-win-guest-tools.exe 123` produced `nex-test-upper d;\path\virtio-win-guest-tools.exe 123` in Notepad: capitals became lowercase, `_` became `-` and `:` became `;`. Passwords, paths and commands with capitals or symbols cannot be typed into Windows this way | Guest A, US keyboard, guest tools installed. The same loss made the Run dialog report "cannot find d;\virtio-win-guest-tools.exe". `typeToVM` in `web/src/console.tsx` sends each character as `client.sendKey(keysym(ch), null, …)`: a bare keysym with no key code and no Shift press, so the guest receives the unshifted key. **Paste into VM** (vdagent clipboard) delivered the same string exactly |
| W18 | major | VM power | **Shut down** on a Windows guest with the guest agent connected powered the VM off, but the task ended **needs_attention** with "libvirt refused the shutdown request; inspect the VM on its host: virsh failed: signal: killed". The details page kept the green "Shut down queued" banner | Task created 15:19:35Z. libvirt logged the machine terminated at 15:23:10Z, and the task was marked failed at 15:23:33Z. Meanwhile libvirt logged `cannot acquire state change lock (held by agent=remoteDispatchDomainShutdown)` for Nex's guest-agent polls at 37 s and 51 s. `powerVM` runs `virsh shutdown` through `Engine.virsh`, which caps every virsh call at 20 s (`internal/vmhost/engine.go`). libvirt's shutdown held the domain job while Windows shut down, so the call was killed and reported as "refused" instead of being reconciled from the observed shut-off state. Why the task took about 4 minutes to be marked failed is not established |

Follow-up on guest A: attaching `virtio-win-0.1.302.iso` with the console CD/DVD control and running
`virtio-win-guest-tools.exe` (UAC shown and answered through the console) installed the drivers,
guest agent and vdagent. The Controller then reported agent **connected**, hostname, IPv4
10.0.2.16 and clipboard **ready**. **Paste into VM** and **Latest text copied inside the VM** both
carried text exactly, and resize-to-window followed the console panel.

## Not verified

- Memory balloon behavior (guest A is dedicated; guest B was not installed).
- Guest B was not installed past **Ready to install**.
- UEFI host installation, physical hardware, Secure Boot with custom keys, BitLocker, and TPM
  (no TPM device exists).
- Root cause of W1 from Setup logs. It is inferred from the stated requirements and observed devices.

## Fix directions (for triage, not decisions)

- W1: ship `swtpm`/`swtpm-tools` on Core and add a per-VM TPM 2.0 option (emulated `tpm-crb`),
  default on for Windows. Its state under `/var/lib/libvirt/swtpm/<uuid>` then needs backup,
  migration and deletion handling, and its record on the pool master.
- W2/W7: a console that stays attached across start/reboot, a boot-menu timeout, or "start from
  media once". Offer an eject/reorder after install.
- W3/W15: Windows-aware defaults (SATA/E1000E, or a second driver CD drive), plus localtime clock
  and Hyper-V features for Windows.
- W4: show Secure Boot state and allow a choice.
- W5: keep URL text out of errors but return distinct policy messages.
- W6: derive resize availability from the guest, not the device model.

API impact: none. This report changes no API or behavior. Guide impact: none. It documents
lab findings; the guide gaps for W2, W3 and W7 are listed as issues. State owner: none.
