# Local root-only installer recheck

2026-09-08, Nex Core 0.1.0-alpha1, committed source `f04721a`. Rerun of the
[root-only installer QA](2026-09-08-root-account.md) flow; adds verification evidence only.

## Artifact and environment

Built from an isolated `git archive` under `.cache/root-recheck-source/`: a first shared-tree build
failed because another session edited `installer/build.sh` mid-build. This does not certify that
session's uncommitted changes. ISO (relative to the isolated source) `dist/nex-core-0.1.0-alpha1-amd64.iso`,
SHA256 `cea731c75dc8e904c3e02b0b9d19a4aeeb394fedca2d3baf2195d197225650d8`; checksum, Debian signature
and base ISO checksum passed. Artifacts local and ignored by Git.

Host Linux `7.2.3-1-cachyos`, QEMU 11.1.1, KVM. Each install: new 24 GiB file disk, 2 vCPUs, no NIC,
no physical disks; automated 3 GiB RAM, interactive 2 GiB; OVMF/TCG only for UEFI menu inspection.

## Account screens

Built image's text installer kernel/initrd; test initrd answered locale, keyboard, network, clock;
passwords and partitioning interactive via QEMU keyboard events. Captures (not committed) under
`root-local-recheck/`; raw captures, test initrd, VM command and disk in the isolated source's
`.cache/nex-iso/recheck-interactive/`:

1. `root-prompt.png`: existing root account's password; says no ordinary user will be created.
2. Blank/blank → `blank-error.png` (**Empty password**); Continue returns to the prompt.
3. Mismatch → `mismatch-error.png` (**Password input error**); Continue returns to the prompt.
4. Matching → `partition.png` (**Partition disks**), no ordinary-user questions.

Independent reviewer inspected all captures: no issue.

## Automated installation and reboot

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

All passed. `root-account.py` reproduced the blank-password fallback and passed its cases. Smoke
exited zero uninterrupted with no post-install repair: logs in `.cache/nex-iso/recheck-smoke/` record
offline install, root status `P` and no UID 1000–60000 accounts on both boots, active services,
identity after base-files reinstall, nested KVM guest boot, reboot persistence. BIOS/UEFI menu captures inspected.

## Interactive installed login

The interactive VM installed after explicit selection and erase confirmation of its sole `vda` disk
(GRUB on it), then booted from disk with ISO and test initrd removed. Root console login with the
typed password succeeded; `id` returned `uid=0(root) gid=0(root) groups=0(root)`.
`root-local-recheck/accounts.png` (not committed) shows the login, `id` and full `/etc/passwd`: root plus
Debian system/service accounts, no ordinary account. No password reset or account change; both VMs powered off.

`check_docs.py --all` passed; range coverage and hosted CI not run. Independent final review of report,
assessment, account capture and log markers: no findings.

## Scope

Virtual hardware only. Not covered: physical hardware, UEFI installation, Secure Boot, multi-disk
safety, guest networking/data persistence. Root-only means the only human admin account; Debian
system/service accounts remain; root SSH disabled.
