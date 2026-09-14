# Host guide command verification

Date: 2026-09-08. Scope: root-console commands in [`verify-nex-core.md`](../guide/verify-nex-core.md).

## Environment

- Nex Core `0.1.0-alpha1`, Debian `13.6`, kernel `6.12.94+deb13-amd64`.
- Disposable reflink clone of the installed disk from [ISO QA run 3](iso/2026-09-08-alpha1.md) (ISO
  SHA-256 `b02044bcac4205af247d52a30ff158ad4d73e7580382f666b37baf394538015f`); originals preserved.
- QEMU KVM, Intel nested, 2 vCPU, 4 GiB; no NIC, ISO or physical disks.
- One-boot GRUB `systemd.mask=nex-smoke.service` suppressed the fixture's auto checks/poweroff.
  Automated serial **root login** with the historical fixture's test-only credentials (not
  `init=/bin/sh`, not the new installer account prompts).

## Results: all eight commands returned 0 (output inspected)

| Command | Observed |
|---|---|
| `cat /etc/os-release` | Nex Core, `ID=nex-core`, `ID_LIKE=debian`, `VERSION_ID=0.1.0-alpha1` |
| `cat /usr/lib/os-release` | Debian GNU/Linux 13, trixie |
| `cat /etc/debian_version` | `13.6` |
| `systemctl --no-pager status libvirtd ssh chrony` | All active (running); some journal lines abbreviated |
| `sshd -T \| grep '^permitrootlogin '` | `permitrootlogin no` |
| `ls -l /dev/kvm` | root:kvm `crw-rw----` |
| `virt-host-validate qemu` | Virtualization, KVM, vhost-net, tun, cgroups PASS; **WARN** no DMAR/IOMMU and unknown Secure Guest (nested VM; KVM still available) |
| `virsh -c qemu:///system list --all` | Connected; `nex-smoke` shut off |

Local evidence (ignored, not published): `.cache/nex-iso/guide-checks-20260908/` — `serial.log`,
`results.json` (command, return code, output), QEMU command, replay script, cloned disk.

## Limits

Not verified: installer/root-account source changes, interactive prompts, sudo/non-root, remote SSH,
clock sync, guest disk/network, physical hardware, passthrough/confidential guests. `systemctl
poweroff` did not exit QEMU within 60 s; cleanup killed only the clone and the runner exited 1 (cause
unknown), so shutdown/reboot steps are **not certified** here (QA3 has separate two-boot evidence).
No QEMU process remains.
