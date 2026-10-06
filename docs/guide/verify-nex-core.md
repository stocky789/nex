# Verify the installed Nex Core host

## Status

Applies to **Nex Core** (development alpha) after [installation](install-nex-core.md). These local
console checks inspect the standalone host, not a Nex Controller service. [Return to the guide](README.md).

## Prerequisites

- The disposable installed host, booted with its installation ISO ejected.
- Local console access as `root` with the password chosen during installation. The installer
  creates no ordinary user. Use the trusted local console for the initial SSH host key check.
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
   installed release's version and follows [software updates](software-updates.md); there is no separate agent
   version. The latter files retain Debian ancestry.
2. Inspect the installed services and root SSH policy:

   ```sh
   systemctl --no-pager status libvirtd ssh chrony
   sshd -T | grep -E '^(permitrootlogin|passwordauthentication) '
   ```

   Services should be active; SSH policy should report `permitrootlogin yes` and
   `passwordauthentication yes`. Compare the console's
   `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` output with the SSH client's host
   key prompt before signing in as `root` from the management network.
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

When the installed host includes OpenZFS (VM storage repositories), also inspect the module
only after the ordinary host checks pass:

```sh
dkms status
modprobe zfs
zpool version
zfs version
systemctl --no-pager is-enabled zfs-import-cache.service
systemctl --no-pager is-active zfs-mount.service
```

The internal `0.3.5~zfsqa4` image passed these checks after offline installation on first
boot and reboot; see the [ZFS verification record](../qa/2026-09-19-zfs-provisioning.md).
They do not certify Secure Boot, physical hardware, a published update, or external distribution.

## Troubleshooting

- **SSH permission denied:** confirm the installed policy with `sshd -T`, that the root
  account is unlocked, and that you are using the host's current root password. Keep
  local console access for recovery.
- **`/dev/kvm` is missing or hardware acceleration fails:** check firmware virtualization
  settings and the outer VM's nested-virtualization configuration. Software emulation is
  not proof of the KVM acceptance gate.
- **IOMMU or Secure Guest warnings:** nested labs commonly report these while KVM checks
  pass. They concern additional capabilities; do not infer passthrough or confidential-guest support from a working basic KVM host.
- **Libvirt cannot connect or a service failed:** inspect its journal, for example
  `journalctl -b -u libvirtd --no-pager`, and retain the relevant error. Do not reinstall
  or reset storage as a generic repair step.
- **Identity is Debian only:** confirm the intended image was installed and retain the
  installer logs. Do not hand-edit identity to mask an incomplete installation.
- **No network/remote access:** return to the local console and inspect the networking
  selected during installation; this offline readiness check does not validate SSH login.

## Verification

Steps 1–2 were checked again on `0.2.10~gate5` in the [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md)
(2026-09-17, disposable nested-KVM lab, by the installation check harness over an SSH session
pinned against the console host key): the OS release and installed package report the build
version, the Nex Core services are active, and the then-current `permitrootlogin no` was recorded before the test
key was added. `virt-host-validate qemu` (step 3) was not rerun on gate5; it last passed, apart
from optional IOMMU and secure-guest warnings, on `0.2.10~gate3`. The gate5 run rebooted the host
and confirmed its identity and VMs with its own harness, but Step 4 was not repeated from the
local console. The Step 1–3 commands were also rehearsed earlier on a disposable host. See the
[guide command rehearsal](../qa/guide-host-checks.md), the
[alpha1 QA report](../qa/iso/2026-09-08-alpha1.md) and the
[single Nex Core version checks](../qa/single-nex-core-version.md). These checks do not establish
guest OS installation, guest networking, SSH login, time synchronization or upgrades.
