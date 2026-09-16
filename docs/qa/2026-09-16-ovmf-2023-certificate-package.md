# OVMF 2023 certificate package verification

## Scope

This verifies the exact Debian package selected for the Core ISO build and its final development
ISO staging/installation checks. It does not certify external redistribution or the independent
Windows TPM/Secure Boot matrix.

## Method and result

On 2026-09-16, ignored local Debian sid retrieval evidence was inspected and
the builder's isolated APT commands were rerun in the pinned ISO-builder
container. That run used only a temporary source list containing Debian sid
`deb` and `deb-src` entries, downloaded `ovmf-generic=2026.05-2` and
`edk2=2026.05-2`, and passed the binary digest check. `dpkg-deb -f` reported
`ovmf-generic` version `2026.05-2`, source `edk2`; its SHA-256 was
`0f2d4de080ebeaf0972c5e212253c28dd2d90aa641542f42f37c6d6ecd4c67eb`.
After extracting that exact package, libvirt's enrolled firmware descriptor
`/usr/share/qemu/firmware/40-edk2-x86_64-secure-enrolled.json` named the
standard enrolled-template paths `/usr/share/OVMF/OVMF_CODE_4M.ms.fd` and
`/usr/share/OVMF/OVMF_VARS_4M.ms.fd` existed. The build-time screen uses offline
byte searches for `Microsoft UEFI CA 2023` and `Windows UEFI CA 2023`; that is
only a package-staging screen, not certificate parsing evidence.

The installed-host acceptance gate uses a parsed database check. It uses the
installed `python3-virt-firmware` command to extract certificates from the
enrolled variables template, accepts the two required certificates only from
`db-*.pem` files, matches SHA-256 fingerprints
`F6:12:4E:34:12:5B:EE:3F:E6:D7:9A:57:4E:AA:7B:91:C0:E7:BD:9D:92:9C:1A:32:11:78:EF:D6:11:DA:D9:01`
for Microsoft UEFI CA 2023 and
`07:6F:1F:EA:90:AC:29:15:5E:BF:77:C1:76:82:F7:5F:1F:DD:1B:E1:96:DA:30:2D:C8:46:1E:35:0A:9A:E3:30`
for Windows UEFI CA 2023, and passes `openssl x509 -checkend 0` for both.

The matching signed source descriptor identified `edk2` `2026.05-2` and listed
the retained upstream tarball SHA-256
`b2495b2473d620240b2c8ece10f2c8b75771a8f9cab58573b74de8d4d01c6851` and Debian
patch tarball SHA-256
`460a8c66db4c18e070612ea51f15a1cf71679653ccb9bf4414f56259d1107870`.

`installer/build.sh` retains the package, source and offline byte-string staging
checks. The ISO/fresh-host integration checks now use the installed
`python3-virt-firmware` command and OpenSSL to parse the extracted EFI `db`
certificates, validate their exact fingerprints and ensure they have not
expired.

## Final ISO result and limits

The exact-tree final development ISO SHA-256
`403dc489fc8fb7d948fe3d27a561c4203a54642c27ac18141dae6ede4c84267c` passed static package,
source and parsed-certificate inspection, then disposable nested-virtualization installation
acceptance: `INSTALL_PASS`, `OVMF_2023_CERTIFICATES_PASS`, `ROOT_ONLY_PASS`, `FIRST_BOOT_PASS`
and `REBOOT_PASS`. The guest Secure Boot lifecycle is recorded separately in [final ISO and
Controller acceptance](2026-09-16-final-iso-controller-acceptance.md).

The staged source directory is a delivery input, not proof of public release
compliance. The full ISO source-delivery bundle, firmware redistribution review,
physical-hardware coverage and the independent Windows TPM/Secure Boot matrix remain open.
