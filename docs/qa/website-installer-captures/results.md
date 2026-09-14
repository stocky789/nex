# Website installer gallery captures

Captured 2026-09-14 for the website gallery's installer section.

## Method

- Media: `nex-core-0.1.9-amd64.iso`, SHA-256
  `2485052ba79c769cdac9b80e93d613d69691af61127b56e6eec980b771f6733f`.
- Disposable QEMU/KVM VM on a contributor workstation: 2 vCPUs, 4 GiB RAM, sparse 32 GiB
  raw file disk (deleted afterwards), two user-mode virtio NICs, 1024x768 VGA.
- Boot menu: UEFI (OVMF) boot of the ISO; GRUB draws the menu at 640x480.
- Wizard: direct kernel boot of the ISO's GUI kernel and `gtk/initrd.gz` with `vga=791`
  (the menu entry's parameters at 1024x768 instead of 800x600). The initrd was not modified.
- Input through QMP keyboard/tablet events; images are QEMU `screendump` output converted to
  PNG with no editing or cropping. The pointer arrow is visible in the progress and completion
  captures.
- Example values only: hostname `nex-core-01.example.com`, `192.0.2.0/24` documentation
  addresses. The DHCP lease preview shows QEMU's built-in user-network address `10.0.2.15`.

## Results

| Capture | Result |
| --- | --- |
| Boot menu, Welcome, Host, Network (static), Network (DHCP preview), Disk, Review | Captured while stepping through the real wizard |
| Progress | Captured during "Installing the base system" |
| Complete | Installer reached "Nex Core is ready" and waited; Restart now exited QEMU |
| First boot | Installed disk booted to the console showing Nex Core 0.1.9, `nex-core-01` and the static management address |
| `npm run check` / `npm run build` | 0 errors, 0 warnings; static build written |
| `website_browser.py` against a local serve of the build | Pass, all 13 groups (1440/768/360px gallery images, alt text, keyboard, JS disabled). One earlier run stopped at the 1440px roadmap hover group before reaching the gallery; the immediate rerun passed |

## Limits

Not a hardware install. The live website deployment was not updated; publishing remains the
maintainer's release step.
