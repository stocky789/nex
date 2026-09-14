# Custom Nex installer GUI verification

2026-09-08, Nex Core 0.1.0-alpha1 local build. ISO `dist/nex-core-0.1.0-alpha1-amd64.iso`, SHA256
`3f4f7aabb2ef37cf4cb6d8b20da7c87d259be985c99818b253e0f956fa93e921`.

The earlier GUI option exposed Debian's stock interface and did not meet project owner's request.
This build replaces the graphical shell with original Nex code (header, section rail, page
heading/purpose, question pane, actions, safety footer). Debian's cdebconf widgets (ABI pinned to
0.280) and installer engine remain; minimal text and serial modes remain.

## Build and focused checks

| Check | Result |
|---|---|
| Debian builder and ISO rebuild | Passed; GTK module uses libraries already in the graphical initrd |
| `installer/build-gui.sh` | cdebconf 0.280 from authenticated APT source metadata, BSD notices kept, initrd ABI checked. `dpkg-source` warned it cannot check the maintainer signature (key not installed); no maintainer-signature claim |
| `tests/integration/installer-shell.py` | 12 checks on the compiled C classifier (namespaces, password/disk errors, blank/unknown tags, rescue); unknown questions stay visible without an invented section |
| GTK module with upstream warning/error flags | Compiled |
| ISO source bundle `nex-gui-source.tar.gz` | Exact upstream and modified source/config, checksum and BSD notice included; does not close whole-image [redistribution gates](../../LICENSING.md) |

## Firmware and console interaction

```sh
python3 tests/integration/installer-modes.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/qa-custom-menus
```

Diskless VMs, 2 vCPUs, 4 GiB RAM, no NIC; BIOS on KVM, UEFI on OVMF/TCG; QMP input and
screenshots of native GTK. GUI and minimal selections reached language; Enter advanced to
location. The custom shell persisted after media/component loading.

800×600: separate diskless KVM VM with the shipped kernel/initrd and preseed and
`-vga none -device VGA,xres=800,yres=600`; language, location and keyboard screens fit (header,
rail, scrolling questions, buttons). A first attempt with only `video=800x600` stayed at 1280×800
and is not small-screen proof. Local: `.cache/nex-iso/qa-custom-small/`.

Captures (not committed): `custom/keyboard-800.png`, `custom/uefi-gui.png`.

## Account and disk safety walkthrough

BIOS/KVM, 2 vCPUs, 4 GiB RAM, no NIC, new 24 GiB file disk. Test-only answers (via the
`iso-smoke.py` archive helper) skip initial locale/network/time; the shipping preseed loads from
the ISO; root, disk and bootloader questions are interactive. Local: `.cache/nex-iso/qa-custom-dialog/`.
Observed with real keyboard input and a test USB mouse:

- No NIC: continue with **no ethernet card**.
- Root password and confirmation in the Nex Access section. Blank → **Empty password**; mismatch → **Password input error**; both keep the fields for retry. Matching values advance to partitioning.
- Explicit target shows `vda`, 25.8 GB decimal.
- Final confirmation names the disk and ext4/swap formatting, defaults to **No**, and returns to the partition editor when declined; **Yes** writes only the selected disposable disk.
- Custom shell stays through progress and GRUB target selection.

Captures (not committed): `custom/root.png`, `blank.png`, `mismatch.png`, `disk.png`, `confirm.png`, `cancel.png`.

## Installation result and documentation checks

- Reached **Installation complete** (GRUB on `/dev/vda`, shipping late hook succeeded).
- Rebooted from the installed disk only: root login succeeded, Nex identity visible, `systemctl is-active libvirtd ssh chrony` returned three active, installed `nex-gui-source.tar.gz.sha256` check `OK`. Initial locale/network answers were test-supplied.
- ISO checksum passed; embedded recipe and frontend source byte-identical to working files; BSD notice retained; `installer/check.sh`, 13 docs-checker unit tests and `check_docs.py --all` passed. Git-range coverage and hosted CI not run.

Captures (not committed): `custom/complete.png`, `custom/installed.png`.

## Review and limits

Independent source review: no safety/ABI blockers. Independent inspection of the 800×600 keyboard,
password, disk and progress captures: no material clipping, overlap or missing controls (reviewer
inspected; root performed interactions).

Unverified: full UEFI installation, physical hardware/Secure Boot, multiple disks, all translations,
screen readers, a fully manual network-to-login workflow. Section names/purpose text are English
only. Phase 0/3 and external-release gates remain open.
