# Root-only installer verification

Date: 2026-09-08. Nex Core 0.1.0-alpha1, rebuilt for root-only installation.

ISO: `dist/nex-core-0.1.0-alpha1-amd64.iso`  
SHA256: `05a856f8162096d45eb4ede45463671e3b5aba9ff82471ca0272d6ef97e230b5`

## Scope and checks

Account setup asks for a root password and confirmation, rejects blank passwords,
and retains Debian's mismatch retry. No ordinary user is created. Debian service
accounts remain; root SSH remains disabled, so administration starts at the local console.

**API impact: none** — this is boot media → Debian account setup → local OS
credentials. No Controller operation, Core agent command/result, or client API exists
in this flow. Guide impact: updated installation and root-console instructions.

- `./installer/check.sh`: passed shell syntax and shipped preseed safety checks.
- `python3 tests/integration/root-account.py .cache/nex-iso/stage/pool/main/u/user-setup/user-setup-udeb_1.107_all.udeb`:
  passed against the actual bundled Debian dialog. The test reproduces Debian's
  blank-password ordinary-user fallback before applying the hook, then checks valid
  input, blank/mismatch retries, component initialization, repeat application, and
  rejection of an unexpected upstream script. Debconf transport is mocked in this check.
- `./installer/build.sh`: passed, including Debian signature/base checksum verification.
- `sha256sum -c nex-core-0.1.0-alpha1-amd64.iso.sha256` from `dist`: passed.

## Disposable VM verification

The full offline smoke uses a newly created 24 GiB regular-file disk, QEMU/KVM,
two vCPUs, 3 GiB RAM and no virtual NIC. UEFI menu inspection uses OVMF under TCG.
The test-only initrd supplies disposable credentials and disk answers; the shipping
account policy and early hook are loaded from the actual ISO after media detection.
No credentials are embedded in the shipping ISO.

```sh
python3 tests/integration/iso-smoke.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/qa-root-account-4 \
  --kernel .cache/nex-iso/stage/install.amd/vmlinuz \
  --initrd .cache/nex-iso/stage/install.amd/initrd.gz \
  --uefi-code .cache/nex-iso/qa-firmware/usr/share/OVMF/OVMF_CODE_4M.fd \
  --uefi-accel tcg
```

Offline installation passed (`NEX_SMOKE_INSTALL_PASS`). Its first post-boot run
then failed an overly broad test UID range: root had password status `P`, while
Debian's required `libvirt-qemu` service account (UID 64055) was mistaken for an
ordinary user. The check now uses Debian's ordinary-user range, 1000–60000.
Only that assertion in the injected test script was corrected on the same disposable
disk. Replaying the existing `run_vm` checks passed both installed boots; no shipping
ISO code or installed account credentials were changed.

| Actual result | Evidence under `.cache/nex-iso/qa-root-account-4/` |
|---|---|
| Offline installation passed | `offline-install.serial.log`: `NEX_SMOKE_INSTALL_PASS` |
| Root has a password; no ordinary users, on both boots | `corrected-first-boot.serial.log`, `corrected-reboot.serial.log`: `NEX_SMOKE_ROOT_ONLY_PASS` |
| Installed services, Nex identity and base-files reinstall passed | `corrected-first-boot.serial.log`: three active services and two `NEX_SMOKE_IDENTITY_PASS` markers |
| Nested libvirt KVM guest boot passed | `corrected-first-boot.serial.log`: `NEX_NESTED_KVM_BOOT_PASS` |
| First boot and reboot persistence passed | `corrected-first-boot.serial.log`: `NEX_SMOKE_FIRST_BOOT_PASS`; `corrected-reboot.serial.log`: `NEX_SMOKE_REBOOT_PASS` |

The corrected replay used `tests/integration/iso-smoke.py`'s existing `run_vm`
helper against the original installed disk; exact QEMU command JSON and stderr logs
are beside each serial log. This is a successful installation plus corrected
post-boot replay, not a claim that the initially failing smoke command exited zero.

A separate interactive BIOS VM passed the actual keyboard-driven account flow using
QEMU/KVM, two vCPUs, 2 GiB RAM, no NIC and a fresh 24 GiB regular-file disk.
Its test initrd answered language, keyboard, network and clock questions only;
account and disk answers remained interactive. The following captures were inspected:

1. The [root password prompt](root-password.png) explains root-only local access.
2. Blank password plus blank confirmation displays [Empty password](root-password-empty.png)
   and Continue returns to the root-password prompt.
3. Different password/confirmation displays [Password input error](root-password-mismatch.png)
   and Continue returns to the root-password prompt.
4. Matching nonempty password/confirmation goes directly to [Partition disks](root-password-next.png)
   without ordinary-user questions. The VM was stopped before choosing a partition method.

Local raw captures and the exact run summary are under
`.cache/nex-iso/qa-root-interactive/`, including `result.json`. This is a real Debian
text-dialog walkthrough in virtual hardware, not a browser or a mocked UI.
Independent review of the hook, checks, installation instructions and API impact
found no outstanding issues.

Actual local root login also passed using the unchanged disposable test password:
`id` returned `uid=0(root) gid=0(root) groups=0(root)`. The [authenticated console
capture](root-login-authenticated.png) was inspected. The test-only smoke service
was masked after its two successful boots to permit console login; account settings
were unchanged. All verification VMs were shut down.

## Failure found during verification

The first hook called `anna-install` before Debian's component loader had initialized.
The VM reported an early-command failure because the account package was only queued
and its dialog script did not exist. Setting the retriever alone also failed because
its package index was absent. The corrected hook invokes the native media component
loader before installing the account component. The focused check now covers this
initialization order; failed images are not the artifact identified above.

## Limits

These checks do not establish physical-hardware support, UEFI installation, Secure
Boot, multi-disk selection safety, guest networking/data persistence, or the full
Phase 0/3 acceptance gates. Root-only account setup does not enable remote root SSH.
