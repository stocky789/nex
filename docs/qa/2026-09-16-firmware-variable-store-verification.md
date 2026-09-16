# Firmware variable-store and Windows VirtIO disk verification

Date: 2026-09-16. This is a disposable nested-virtualization lab. Raw command output and
screenshots remain ignored local evidence; this tracked report records the inspected results only.

## Firmware variable store

The host accepted a UEFI domain using firmware auto-selection with an explicit NVRAM path and no
`template` attribute. The path was keyed by the VM UUID, rather than its display name.

The Core lifecycle probe then passed: create, start, stop and rename retained the same NVRAM path,
file size and post-stop SHA-256. A deployed-final-package retest then completed Secure Boot on →
rename → off → on: host SecureBootEnable was `01`, `00`, then `01`; keys and boot entries remained;
and delete removed the domain and NVRAM. A stopped edit that renamed the VM and changed UEFI to BIOS
in one request removed the exact old regular NVRAM file and left the BIOS definition. The focused
race checks also passed, including recovery of an interrupted `cleaning-nvram` journal stage.

This establishes the implementation shape used by the current Core: new UEFI definitions name a
UUID-keyed NVRAM file; stopped edits retain a legacy observed path; cleanup validates the exact
libvirt NVRAM directory and regular-file type before removal; and recovery observes before it
retries destructive work.

## Windows VirtIO system disk

A fresh Windows 11 IoT Enterprise LTSC VM used the install ISO and virtio-win 0.1.302 on the
second CD drive. Windows Setup
initially showed no disk. Loading the Red Hat VirtIO SCSI driver from the driver disc exposed the
64 GiB VirtIO disk, installation completed, and the guest rebooted to OOBE. The disposable VM was
then stopped and deleted with its disks.

## Limits

The renamed UEFI guest booted with its existing boot entries. A signed Debian guest reported EFI
Secure Boot `01` when enabled; after Core disabled it, the guest variable was absent and host
SecureBootEnable was `00`. The final Windows TPM/Secure Boot independence matrix is still open,
as are physical-hardware and redistribution checks.
