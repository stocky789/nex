# Host guide command verification

Date: 2026-09-08. Scope: the root-console commands in
[`verify-nex-core.md`](../guide/verify-nex-core.md), not the complete installation walkthrough.

## Tested environment

- Nex Core `0.1.0-alpha1`, Debian ancestry `13.6`, kernel `6.12.94+deb13-amd64`.
- Source: a fresh sparse/reflink clone of the installed disk from
  [ISO QA run 3](iso/2026-09-08-alpha1.md). That historical ISO's SHA256 is
  `b02044bcac4205af247d52a30ff158ad4d73e7580382f666b37baf394538015f`.
- QEMU KVM, Intel nested virtualization, 2 vCPUs, 4 GiB RAM; no NIC, ISO, or physical disks.
  The original QA3 disk and logs were preserved. Only the new disposable clone was booted.
- The runner entered a one-boot GRUB command with `systemd.mask=nex-smoke.service` to suppress
  the existing fixture's automatic checks/poweroff. The installed system otherwise booted normally.
  It then performed an actual local serial **root login** using the historical fixture's test-only
  credentials and replayed the documented root commands. This was automated console operation,
  not an `init=/bin/sh` shell or a test of the new installer account prompts. No default password
  is prescribed by this guide or copied into shipping media.

## Observed command results

All eight commands below returned **0**. Their actual output was inspected, not inferred from
command completion.

| Command | Observed result |
|---|---|
| `cat /etc/os-release` | Nex Core, `ID=nex-core`, `ID_LIKE=debian`, `VERSION_ID=0.1.0-alpha1` |
| `cat /usr/lib/os-release` | Debian GNU/Linux 13, trixie; upstream metadata retained |
| `cat /etc/debian_version` | `13.6` |
| `systemctl --no-pager status libvirtd ssh chrony` | All three services active (running) |
| `sshd -T \| grep '^permitrootlogin '` | `permitrootlogin no` |
| `ls -l /dev/kvm` | Character device present, root:kvm, mode `crw-rw----` |
| `virt-host-validate qemu` | Virtualization, KVM accessibility, vhost-net, tun and listed cgroup checks PASS; two optional warnings below |
| `virsh -c qemu:///system list --all` | Connected successfully; historical `nex-smoke` fixture guest listed as shut off |

`virt-host-validate` reported **WARN** for missing ACPI DMAR/IOMMU support and unknown Secure Guest
support in this nested VM. Those warnings did not mean KVM acceleration was unavailable. The
services' status output abbreviated some journal lines; the guide's journal troubleshooting command
remains appropriate when fuller diagnostics are needed.

Local evidence: `.cache/nex-iso/guide-checks-20260908/` contains `serial.log`, `results.json`, the
QEMU command, temporary replay script and cloned raw disk. These are ignored local lab artifacts,
not published downloads. Result JSON preserves each command, its return code and actual output.

## Limits and cleanup

This verifies the listed **root commands** on the historical installed image. It does not validate
new source changes to installer/root-account policy, the interactive install prompts, sudo or
non-root administration, remote SSH login, clock synchronization, guest disk/network behavior,
physical hardware or optional passthrough/confidential-guest capabilities.

After the commands completed, the temporary runner sent `systemctl poweroff` but did not observe
QEMU exit within 60 seconds. Its cleanup terminated **only the cloned VM**; the runner therefore
exited 1 even though all eight command results were 0. The cause of that cleanup timeout was not
established. This run does **not** certify the guide's optional shutdown/reboot procedure. The
separate QA3 report retains its independently observed unattended two-boot evidence.

No QEMU process from this walkthrough remains. Keep the installation guide's complete interactive
procedure and the new account flow explicitly unverified until separately exercised.
