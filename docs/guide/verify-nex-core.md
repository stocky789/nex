# Verify the installed Nex Core host

## Status

Applies to **Nex Core** (development alpha) after [installation](install-nex-core.md). These local
console checks inspect the standalone host, not a Nex Controller service. [Return to the guide](README.md).

## Prerequisites

- The disposable installed host, booted with its installation ISO ejected.
- Local console access as `root` with the password chosen during installation. The current
  installer creates no ordinary user; root SSH is disabled. Run the commands below directly
  in that local root console.
- Hardware virtualization enabled and exposed by the outer hypervisor for nested KVM.

## Steps

1. Inspect distribution identity:

   ```sh
   cat /etc/os-release
   cat /usr/lib/os-release
   cat /etc/debian_version
   ```

   The first file should identify Nex Core, with `ID=nex-core`, `ID_LIKE=debian`, and
   `PRETTY_NAME="Nex Core <version>"`. Nex Core has one version: `VERSION_ID` equals the
   installed `nex-core-local` package version (`dpkg-query -W nex-core-local`) and follows
   [software updates](software-updates.md); there is no separate agent version. The latter
   files retain Debian ancestry.
2. Inspect the installed services and root SSH policy:

   ```sh
   systemctl --no-pager status libvirtd ssh chrony
   sshd -T | grep '^permitrootlogin '
   ```

   Services should be active; SSH policy should report `permitrootlogin no`.
3. Check host virtualization readiness and libvirt connectivity:

   ```sh
   ls -l /dev/kvm
   virt-host-validate qemu
   virsh -c qemu:///system list --all
   ```

   Read every validation result in context. A warning about an optional capability is
   different from unavailable KVM acceleration. A successful VM list can be empty on a
   new host; it proves connectivity, not successful guest execution.
4. Record the image checksum, results, and any errors. If checking reboot behavior, shut
   down any disposable guests first and reboot only this selected lab VM, then repeat
   the identity/service checks from its local console.

## Expected result

Nex identity is present, libvirt/SSH/Chrony are running, `/dev/kvm` is available, and libvirt
responds. These are host readiness checks. Active SSH does not prove remote login works;
active Chrony does not prove clock synchronization, especially offline.

The alpha includes the host-local web UI (`nex-core-local` on port 9443). Central Controller
enrollment, which places the host in its own automatically created pool as pool master,
remains separate. No bridge/NAT network or persistent guest is
created by this procedure.

## Troubleshooting

- **Permission denied:** verify you are using the local root console; do not enable root SSH
  or weaken authentication to work around an access problem.
- **`/dev/kvm` is missing or hardware acceleration fails:** check firmware virtualization
  settings and the outer VM's nested-virtualization configuration. Software emulation is
  not proof of the KVM acceptance gate.
- **IOMMU or Secure Guest warnings:** nested labs commonly report these while KVM checks
  pass. They concern additional capabilities; do not infer passthrough or confidential-guest support from a working basic KVM host.
- **Libvirt cannot connect or a service failed:** inspect its journal, for example
  `journalctl -b -u libvirtd --no-pager`, and retain the relevant error. Do not reinstall
  or reset storage as a generic repair step.
- **Identity is Debian only:** confirm the intended image was installed and retain the
  installer logs. Do not hand-edit identity to mask an incomplete late-install step.
- **No network/remote access:** return to the local console and inspect the networking
  selected during installation; this offline readiness check does not validate SSH login.

## Verification

The Step 1–3 commands were rehearsed on a disposable host; Step 4 reboot was not. See the
[guide command rehearsal](../qa/guide-host-checks.md), the
[alpha1 QA report](../qa/iso/2026-09-08-alpha1.md) and the
[single Nex Core version checks](../qa/single-nex-core-version.md). These checks do not establish
guest OS installation, guest networking, SSH login, time synchronization or upgrades.

