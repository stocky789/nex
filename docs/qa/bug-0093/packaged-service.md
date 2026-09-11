# BUG-0093 packaged Core service probe

Date: 2026-09-12  
Environment: disposable Debian 13 KVM guest with `libvirt-daemon-system` and
`libvirt-clients`; no existing Nex service, pool, Controller, guest, or storage data was used.

## Result

The `nex-core-local` sandbox directives reproduced the ISO-registration failure before the
fix. With `NoNewPrivileges=yes` and the retained filesystem, kernel and SUID/SGID protections,
the process lacked `CAP_SETUID` in its permitted and effective sets and
`runuser -u libvirt-qemu -- id` returned `cannot set user id: Operation not permitted`.

Adding only `AmbientCapabilities=CAP_SETUID` put that capability in the permitted, effective and
ambient sets. The same probe then returned the `libvirt-qemu` identity successfully. AppArmor
was enforcing for libvirtd and reported no denial during either probe. `RestrictSUIDSGID=yes`
was retained; it controls SUID/SGID file-mode bits rather than identity-change syscalls.

The current Core binary and its `nex-core-local.service` unit were then staged on a second
fresh disposable guest. After ordinary agent enrollment, the service started its embedded
agent, completed all three QEMU-account probes, and wrote one succeeded `storage-register`
entry named **ISO** for `/var/lib/libvirt/images` to the Core managed journal. The separately
started fresh Controller adopted that same journal record through `Inventory.managed`; its
storage list contained the original Core-generated storage ID and no manual storage request was
made.

## Limits

The Core binary and unit were staged directly rather than installed from a newly rebuilt `.deb`.
The service boundary, embedded journal write, and Controller adoption were exercised; package
rebuild and installation remain release-delivery verification.
