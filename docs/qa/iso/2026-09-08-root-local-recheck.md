# Local root-only installer recheck

Date: 2026-09-08. Nex Core 0.1.0-alpha1, committed source `f04721a`.

## Artifact and environment

Rebuilt an isolated `git archive` of the committed source under
`.cache/root-recheck-source/`. Another session edited `installer/build.sh` during the
initial shared-tree build, which failed while reading the changing script. No shared
source was changed by this recheck; the isolated build completed successfully.
This report does not certify the other session's uncommitted installer changes.

ISO, relative to that isolated source:
`dist/nex-core-0.1.0-alpha1-amd64.iso`. SHA256:
`cea731c75dc8e904c3e02b0b9d19a4aeeb394fedca2d3baf2195d197225650d8`.
The matching checksum check passed. Debian signature and base ISO checksum verification
passed during the build. ISO/cache artifacts remain local and ignored by Git.

Host: Linux `7.2.3-1-cachyos`, QEMU 11.1.1, KVM available. Each installation used
its own new 24 GiB regular-file disk, two vCPUs, and no virtual NIC. No physical
host disks were attached. Automated installation used 3 GiB RAM; interactive
installation used 2 GiB. OVMF/TCG was used only for UEFI boot-menu inspection.

## Account screens

The interactive VM booted the built image's Debian text installer kernel/initrd.
A test-only appended initrd answered locale, keyboard, network and clock questions;
shipping policy and hook were loaded from the ISO. Passwords and partitioning
remained interactive. Input was sent through QEMU keyboard events and captures
were inspected; this was a real installer in virtual hardware, not mocked UI.

1. [Root password prompt](root-local-recheck/root-prompt.png) asks for the existing
   root account's password and explicitly says no ordinary user will be created.
2. Blank password and blank confirmation produce [Empty password](root-local-recheck/blank-error.png).
   Continue returns to the root-password prompt.
3. Different password and confirmation produce [Password input error](root-local-recheck/mismatch-error.png).
   Continue returns to the root-password prompt.
4. Matching nonempty password and confirmation advance directly to
   [Partition disks](root-local-recheck/partition.png), with no ordinary-user questions.

An independent reviewer inspected the prompt, both errors, both retry captures and
partitioning capture and found no issue. Raw captures, test-only initrd, VM command
and disposable disk are under the isolated source's `.cache/nex-iso/recheck-interactive/`.

## Automated installation and reboot

From the isolated source, these commands passed:

```sh
./installer/check.sh
python3 tests/integration/root-account.py \
  .cache/nex-iso/stage/pool/main/u/user-setup/user-setup-udeb_1.107_all.udeb
python3 tests/integration/iso-smoke.py dist/nex-core-0.1.0-alpha1-amd64.iso \
  --output .cache/nex-iso/recheck-smoke \
  --kernel .cache/nex-iso/stage/install.amd/vmlinuz \
  --initrd .cache/nex-iso/stage/install.amd/initrd.gz \
  --uefi-code /usr/share/OVMF/OVMF_CODE_4M.fd --uefi-accel tcg
```

The dialog regression test uses the bundled Debian script with mocked debconf;
it reproduced Debian's original blank-password fallback and passed valid input,
blank/mismatch retries, initialization, repeat application and upstream-change rejection.

The full smoke invocation exited zero uninterrupted. Logs in
`.cache/nex-iso/recheck-smoke/` record offline installation, root password status `P`
and no ordinary UID 1000–60000 accounts on both boots, active services, Nex identity
surviving a base-files reinstall, nested libvirt KVM guest boot, and reboot persistence.
BIOS and UEFI boot-menu captures were inspected. No post-install test repair was needed.

## Interactive installed login

The same interactive VM completed installation after explicit selection and erase
confirmation of its sole disposable `vda` disk. GRUB was installed to that disk.
After the installer completed and shut down on its reboot request, the VM booted
from the installed disk with the ISO and test initrd removed.

Local console login as `root` succeeded using the unchanged password typed into
the installer. `id` returned `uid=0(root) gid=0(root) groups=0(root)`.
The [authenticated account inventory](root-local-recheck/accounts.png) shows the
successful login, `id`, and complete `/etc/passwd`: root plus Debian system/service
accounts, with no ordinary account. No password reset or account modification was
used. The VM was shut down with `poweroff`; both test installations are stopped.

The documentation structure check (`python3 scripts/check_docs.py --all`) passed.
Committed-range coverage was not run: this report is uncommitted and other work
is active in the shared checkout. No hosted CI result is claimed.

Independent final review checked this report, its assessment, the authenticated
account capture and installation/boot log markers; no findings remained.

## Scope

API impact: **none** — test rerun of ISO → Debian account setup → local OS credentials;
no Controller operation, Core agent command/result or client API is changed.
Guide impact: **none** — existing root-password and local-console instructions remain
unchanged; this report adds verification evidence only.

This verifies virtual hardware only. Physical hardware, UEFI installation, Secure
Boot, multiple-disk safety and full guest networking/data persistence remain outside
this recheck. Root-only means the only human administrator account; required Debian
system/service accounts remain. Root SSH remains disabled.
