# Root-only installer verification

2026-09-08, Nex Core 0.1.0-alpha1 rebuilt for root-only installation. ISO
`dist/nex-core-0.1.0-alpha1-amd64.iso`, SHA256 `05a856f8162096d45eb4ede45463671e3b5aba9ff82471ca0272d6ef97e230b5`.
Bugs: BUG-0005, BUG-0006 ([ledger](../../BUG_LEDGER.md)). Independently rerun on committed source in
[local root-only recheck](2026-09-08-root-local-recheck.md).

## Scope and checks

Root password and confirmation, blank rejected, Debian mismatch retry kept; no ordinary user; Debian
service accounts remain; root SSH disabled, so administration starts at the local console.

| Check | Result |
|---|---|
| `./installer/check.sh` | Passed: syntax and preseed safety |
| `python3 tests/integration/root-account.py .cache/nex-iso/stage/pool/main/u/user-setup/user-setup-udeb_1.107_all.udeb` (bundled Debian dialog, mocked debconf) | Passed: reproduces Debian's blank-password ordinary-user fallback before the hook; valid input, blank/mismatch retries, component initialization, repeat application, unexpected-upstream rejection |
| `./installer/build.sh` | Passed incl. Debian signature/base checksum |
| `sha256sum -c nex-core-0.1.0-alpha1-amd64.iso.sha256` in `dist` | Passed |
| First hook build | Failed: early-command failure, `anna-install` before component loader init (BUG-0005); failed images are not this artifact |

## Disposable VM verification

New 24 GiB file disk, QEMU/KVM, 2 vCPUs, 3 GiB, no NIC; UEFI menu on OVMF/TCG. Test initrd supplies
disposable credentials and disk answers; shipping account policy and early hook load from the ISO.

```sh
python3 tests/integration/iso-smoke.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/qa-root-account-4 \
  --kernel .cache/nex-iso/stage/install.amd/vmlinuz \
  --initrd .cache/nex-iso/stage/install.amd/initrd.gz \
  --uefi-code .cache/nex-iso/qa-firmware/usr/share/OVMF/OVMF_CODE_4M.fd \
  --uefi-accel tcg
```

Install passed; the first post-boot run failed its own UID-range assertion (`libvirt-qemu` UID 64055,
BUG-0006; root status `P`). Only that injected test assertion was corrected on the same disk and the
existing `run_vm` checks replayed; no ISO code or credentials changed. This is install plus corrected
replay, not a zero exit of the original command. QEMU command JSON and stderr sit beside each log.

| Actual result | Evidence under `.cache/nex-iso/qa-root-account-4/` |
|---|---|
| Offline installation passed | `offline-install.serial.log`: `NEX_SMOKE_INSTALL_PASS` |
| Root has a password; no ordinary users, on both boots | `corrected-first-boot.serial.log`, `corrected-reboot.serial.log`: `NEX_SMOKE_ROOT_ONLY_PASS` |
| Installed services, Nex identity and base-files reinstall passed | `corrected-first-boot.serial.log`: three active services and two `NEX_SMOKE_IDENTITY_PASS` markers |
| Nested libvirt KVM guest boot passed | `corrected-first-boot.serial.log`: `NEX_NESTED_KVM_BOOT_PASS` |
| First boot and reboot persistence passed | `corrected-first-boot.serial.log`: `NEX_SMOKE_FIRST_BOOT_PASS`; `corrected-reboot.serial.log`: `NEX_SMOKE_REBOOT_PASS` |

Interactive BIOS VM (QEMU/KVM, 2 vCPUs, 2 GiB, no NIC, fresh 24 GiB disk; test initrd answered only
language, keyboard, network, clock) passed the keyboard-driven account flow. Local captures and
`result.json` under `.cache/nex-iso/qa-root-interactive/`; PNGs below not committed:

1. `root-password.png`: prompt explains root-only local access.
2. Blank/blank → `root-password-empty.png` (**Empty password**), Continue returns to the prompt.
3. Mismatch → `root-password-mismatch.png` (**Password input error**), Continue returns to the prompt.
4. Matching → `root-password-next.png` (**Partition disks**), no ordinary-user questions; VM stopped before partitioning.

Local root login with the disposable password: `id` returned `uid=0(root) gid=0(root) groups=0(root)`
(`root-login-authenticated.png`). The test smoke service was masked after both boots to allow console
login; account settings unchanged. All VMs shut down. Independent review of hook, checks, instructions
and API impact found no issues.

## Limits

Not established: physical hardware, UEFI installation, Secure Boot, multi-disk safety, guest
networking/data persistence, Phase 0/3 gates. Root-only does not enable remote root SSH.
