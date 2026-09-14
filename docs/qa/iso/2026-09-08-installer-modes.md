# GUI and minimal installer verification

2026-09-08, Nex Core 0.1.0-alpha1 local build. ISO `dist/nex-core-0.1.0-alpha1-amd64.iso`, SHA256
`8fc53ba1706d864dacd11d281575514a46c790f03dc216ed2b37ea079394706c`.

## Scope

Both firmware menus offer **GUI install - Nex Core** and **Minimal install - Nex Core (text)** plus
serial and rescue, waiting for selection. GUI uses Debian's bundled GTK installer, minimal its text
frontend; both install the same headless host through the same preseed and hooks. No custom shell,
desktop, dependency or modified upstream executable. Bundled `isolinux/gtk.cfg` confirmed shared
`/install.amd/vmlinuz`, `vga=788` and `/install.amd/gtk/initrd.gz`
([Debian installer reference](https://www.debian.org/releases/trixie/amd64/ch05s01.en.html#graphical)).

## Build and focused checks

| Check | Result |
|---|---|
| Debian container and ISO build (via `sudo -n docker`; output owned by invoking user) | Passed, including signature and pinned checksum |
| `./installer/check.sh` | Passed: menu parity, shared preseed, serial/rescue, disabled timeout, syntax, account/disk/network policy, offline packages |
| `tests/integration/root-account.py .cache/nex-iso/stage/pool/main/u/user-setup/user-setup-udeb_1.107_all.udeb` (mocked debconf) | Passed: valid/blank/mismatch, initialization, idempotency, changed-upstream rejection |
| ISO SHA256 sidecar; embedded `installer/` source archive vs working tree | Passed; byte-identical |

## Real menu and frontend interaction

```sh
python3 tests/integration/installer-modes.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/qa-modes-menus-2
```

Actual ISO, no disks or NICs, 2 vCPUs, 4 GiB. BIOS on KVM; UEFI on OVMF/TCG. QMP sends menu
selection and Enter at language. PPM captures converted losslessly to PNG; the script proves capture
execution, visual inspection establishes the screen. Command JSON and logs stay in the output directory.

| Run | Result |
|---|---|
| First local run, UEFI on KVM | Rejected: stalled firmware splash; script now uses TCG (lab firmware limit, no ISO change) |
| Final run | All four BIOS/UEFI selections reached the expected language screen; Enter advanced to location; command succeeded |

Captures (not committed): `modes-bios-menu.png`, `modes-uefi-menu.png`, `gui-language.png`, `minimal-language.png`.

## Interactive graphical account and disk checks

BIOS/KVM, one new 24 GiB file disk, 4 GiB, 2 vCPUs, no NIC. Test-only GTK initrd (via the
`iso-smoke.py` archive helper) supplies locale, keyboard, network-disabled and clock answers, then
loads the shipping preseed; root, partitioning and bootloader stay interactive. Kernel args
`auto=true priority=high vga=788`, no serial frontend. Local: `.cache/nex-iso/qa-modes-dialog-2/`.
QMP keyboard plus test USB mouse:

- Missing NIC: continue with **no ethernet card**.
- Root password and confirmation together, with root-only/local-console wording. Blank → **Empty password**, mismatch → **Password input error**, both retry. Matching values go to partitioning with no ordinary-user questions.
- Guided partitioning asks for an explicit disk with device and capacity. Final confirmation names disk/partitions and defaults to **No** (returns to summary); **Yes** installs on the selected disposable disk only.
- Keyboard and a mouse click exercised; inspected text readable. Not screen-reader certification.
- GRUB on `/dev/vda`; **Installation complete** after the shipping late hook, no NIC.
- Rebooted from installed disk alone: root login with chosen password, Nex Core identity, `systemctl is-active libvirtd ssh chrony` three active.

Captures (not committed): `gui-root-password.png`, `gui-blank-password.png`, `gui-mismatch-password.png`,
`gui-disk-selection.png`, `gui-disk-confirmation.png`, `gui-disk-cancelled.png`, `gui-install-complete.png`, `gui-installed-host.png`.

## Offline installation and installed-host checks

```sh
python3 tests/integration/iso-smoke.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/qa-modes-install \
  --kernel .cache/nex-iso/stage/install.amd/vmlinuz \
  --initrd .cache/nex-iso/stage/install.amd/gtk/initrd.gz
```

Passed, exit zero: offline install, media removal, first boot, reboot. Markers
`NEX_SMOKE_INSTALL_PASS`, `NEX_SMOKE_ROOT_ONLY_PASS`, `NEX_SMOKE_IDENTITY_PASS`,
`NEX_NESTED_KVM_BOOT_PASS`, `NEX_SMOKE_FIRST_BOOT_PASS`, `NEX_SMOKE_REBOOT_PASS` (services, root
password/no ordinary users, identity after base-files reinstall, nested KVM guest). Uses the GTK
payload with serial console and test answers: offline payload evidence, not a manual GUI install.
Its initial UEFI capture used KVM and is not accepted; the TCG menu check owns UEFI evidence.

## Documentation and review

`check_docs.py --all`, 13 checker unit tests and `git diff --check` passed. Independent review found
no scope, implementation or visual defects in menus, frontend screens and password/disk captures.
Git-range coverage and hosted CI not run.

## Limits

Open: physical hardware, Secure Boot, full UEFI installation, multiple-disk safety, screen readers,
complete manual language/network-to-login workflow, Phase 0/3 and external redistribution gates.
