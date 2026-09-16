# Verifying the Windows 11 UEFI fixes on real libvirt, QEMU and Windows

Date: 2026-09-16. Disposable lab on a developer workstation. This report verifies the fixes made
for [the Windows 11 UEFI lab findings](2026-09-16-windows-11-uefi-lab.md). It records what was
actually executed and what remains unverified.

## Environment

| Item | Value |
|---|---|
| Host | Arch Linux workstation, 32 threads, 31 GiB RAM, nested KVM available |
| Hypervisor | libvirt 12.7.0, QEMU 11.1.1, swtpm 0.10.2, edk2-ovmf 202605 |
| Firmware | `OVMF_CODE.secboot.4m.fd` plus a locally key-enrolled `OVMF_VARS.ms.4m.fd`, published as a firmware descriptor so the host advertises `enrolledKeys: yes` as Debian's `ovmf` package does |
| Core code | The Nex Core managed engine from this working tree, driven directly through `vmhost.NewManaged(...).Execute(...)`, `ObserveGuest` and `GuestFacts`. No reimplementation: the harness only builds commands |
| Guest media | `en-us_windows_11_iot_enterprise_ltsc_2024_x64_dvd_f6b14814.iso`, imported through the real `iso-import` path with SHA-256 verification; `virtio-win-0.1.302` |
| Guest | `win11-tpm-lab`: 4 vCPU host-passthrough, 8192 MiB maximum, 64 GiB SATA thin, e1000e, UEFI, Secure Boot default (on), TPM 2.0 |
| Method | Engine commands as root; `virsh` for observation; VNC over a unix-socket bridge for mouse/keyboard and screenshots |

**This is not Nex Core.** It is the Core engine running against a newer libvirt/QEMU than Core
ships, on Arch rather than Debian. It exercises the same code and the same libvirt contracts, but
it does not verify Core's packaging, installer or Controller.

## Verified

| Bug | Evidence |
|---|---|
| BUG-0196 TPM | `<tpm model='tpm-crb'><backend type='emulator' version='2.0'/>` in the live domain. swtpm manufactured an EK certificate and activated SHA-256 PCR banks; the guest wrote TPM state. Inside the guest, `Win32_Tpm` reported `IsActivated_InitialValue: TRUE`, `SpecVersion: 2.0`. **Windows 11 Enterprise LTSC — the non-IoT edition the findings say was refused — reached "Ready to install" with no system-requirements refusal, installed, and booted to a desktop.** Deleting a TPM VM removed `/var/lib/libvirt/swtpm/<uuid>` and its NVRAM |
| BUG-0197 boot prompt | Reproduced: starting without an attached console fell through to `BdsDxe: No bootable option or device was found`. Attaching first and pressing a key booted the installer, which is what the console Start control now makes possible |
| BUG-0198 driver disc | Two SATA CD drives in the live domain, aliases `ua-nex-cdrom-install` and `ua-nex-cdrom-drivers` preserved by libvirt across dumpxml. The guest saw `D:` (Windows) and `E:` (driver disc). A `drive: "drivers"` media request inserted into the second drive without disturbing the first, while the guest was running. virtio-win was delivered on that drive and its installer run from `E:` with the Windows ISO still mounted — the disc swap the findings describe was not needed. `drivers_media_id` persisted in the durable record |
| BUG-0199 Secure Boot | libvirt selected `OVMF_CODE.secboot.4m.fd` with the enrolled-keys VARS template, `secure='yes'`, `<smm state='on'/>`, from Nex's explicit `secure-boot` and `enrolled-keys` firmware features |
| BUG-0200 ISO URL | The virtio-win "stable" URL still answers `301` to plain `http://`. Nex reported `ISO download refused this server's redirect from HTTPS to plain HTTP; use a direct HTTPS link to the ISO` instead of the old generic connection error, with no URL text |
| BUG-0201 resize | After guest tools, `clipboard: ready` and `resize: true` together. Resize now follows the vdagent channel the guest opened, not the device model |
| BUG-0202 post-install media | Reproduced: after installing, the VM booted to `Press any key to boot from CD or DVD` again, because the ISO stays inserted ahead of the disk. This is the state the eject action addresses |
| BUG-0205 repository name | The engine registered `/var/lib/libvirt/images` as **Local storage** |
| BUG-0210 Windows defaults | `<clock offset='localtime'/>` and Hyper-V relaxed/vapic/spinlocks 8191 in the live domain. The guest clock read 2:11 PM against a host local time of 14:11; under the previous `utc` offset it would have read 4:11 AM |
| BUG-0211 Windows 11 naming | The guest agent reported `pretty-name: "Windows 10 Enterprise LTSC 2024"` with `kernel-release: 26100`. Nex reported `os: "Windows 11 Enterprise LTSC 2024"` |
| BUG-0212 typing | `E:\virtio-win-guest-tools.exe` typed into the guest arrived exactly, including the capital, `:` and `\`. The findings record this same string arriving as `d;\virtio-win-guest-tools.exe` |
| BUG-0213 shutdown | **A/B on the same VM with the guest agent connected.** Pre-fix binary (blanket 20 s cap): `wall=20.138s`, `needs_attention`, `virsh failed: signal: killed` — the recorded symptom. Fixed binary: `wall=1m0.073s`, `succeeded`. The guest reached `shut off` 17 s after the pre-fix run had already declared failure, confirming that report was false |

## Found while verifying

**BUG-0215 — a host without enrolled-keys firmware failed with an opaque libvirt error.** Before
the enrolled-keys firmware was published on this host, `vm-create` failed with libvirt's
`Unable to find 'efi' firmware that is compatible with the current configuration`. That is true
but never mentions Secure Boot, so an operator cannot tell which choice to change. Since
BUG-0199 makes Nex state the firmware features explicitly rather than letting libvirt choose,
Nex now asks the host first, the same way the TPM check does, and refuses in terms of the
setting. Fixed and covered by `TestHostSecureBootSupportRefusesWhatTheHostCannotBuild`, whose
data is the shape this host actually reported in both states.

## Not verified

- **Nex Core itself.** No Core ISO was built or installed. Packaging (including that `swtpm` and
  `swtpm-tools` ship), the installer and first boot are untested here.
- **Nex Controller.** No Controller ran, so BUG-0202 (the offered eject), BUG-0203 (media
  delete), BUG-0204 (upload banner), BUG-0206 (memory wording), BUG-0207 (console layout),
  BUG-0208 (login 401) and BUG-0212's actual console control were not exercised through the web
  UI. BUG-0212's key mapping was verified by typing the same string into the guest through an
  equivalent implementation, not through `web/src/console.tsx`.
- **BUG-0209 installer screens.** `nex-shell.c` is a fragment compiled inside the ISO builder;
  it was neither compiled nor run. Regression coverage remains outstanding.
- **Library versions.** libvirt 12.7/QEMU 11.1 here versus libvirt 11.3/QEMU 10.0 in Core. The
  Hyper-V enlightenments and the firmware-feature form were accepted by the newer stack only.
- **Secure Boot enforcement.** The firmware was selected and SMM enabled, but no attempt was made
  to boot unsigned media to prove enforcement, and the enrolled keys were locally generated
  rather than Microsoft's.
- The `win11-tpm-lab` guest was left defined and shut off on the workstation.

## Cross-reference for the consolidated lab session

A parallel session drafted a consolidated disposable-host verification plan covering the host
debts this batch left open. This run closes part of that plan. Mapping what was actually
executed here, so the remaining session is scoped to what is genuinely still open:

**Closed by this run**

- Virtual TPM observed on a host (BUG-0196): device in the live domain, swtpm manufacturing, and
  Windows reporting an activated TPM 2.0.
- Windows 11 accepting the VM (BUG-0196): **Windows 11 Enterprise LTSC**, the non-IoT edition the
  original finding named, installed and booted.
- A completed shutdown reported as succeeded (BUG-0213): A/B against a pre-fix binary on one
  guest with the guest agent connected.
- Delete removes per-VM state: swtpm directory and NVRAM both gone after `vm-delete`.
- Driver disc delivery (BUG-0198): two drives, alias-resolved `drive` routing, live insert into
  the second drive, virtio-win installed from it with the Windows ISO still mounted.

**Correction to that plan's media assumption.** It states a standard non-IoT Windows 11 ISO must
be staged because "IoT LTSC does not test the TPM requirement at all". The
`en-us_windows_11_iot_enterprise_ltsc_2024` ISO used here offers three images at Select Image:
**Windows 11 Enterprise LTSC**, Windows 11 IoT Enterprise LTSC, and Windows 11 IoT Enterprise
Subscription LTSC. The first is non-IoT and is what was installed, so that ISO does test the
requirement. A separate ISO may not be needed.

**Still open after this run** — none of the following was attempted here:

- The firmware/NVRAM probe series on stock libvirt that decides the variable-store implementation.
- Secure Boot genuinely changing inside a guest, and the silent no-op reproduction.
- A VirtIO *system disk* install using the driver disc during Setup. virtio-win was delivered on
  the second drive and installed after the fact, but this guest's system disk is SATA, so loading
  a storage driver at the disk-selection step was not exercised.
- Renaming a UEFI VM.
- BUG-0214: whether the NVRAM orphan actually occurs in practice.
- `undefine` without `--nvram`.
- Host reboot with a UEFI + TPM guest.
- The three browser debts (console Start control, offered eject, console layout), which need a
  running Controller.
