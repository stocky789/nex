# Nex bug ledger

Append resolved defects with their evidence and the approach to avoid.

## BUG-0001 — Incomplete isolated ISO build dependencies

- Date: 2026-09-08
- Symptom: the first build exited 127 at `gpgv`; review also found that the target's HTTPS APT configuration needed an explicit CA-certificate dependency.
- Root cause: relying on recommended/transitive tools while installing with `--no-install-recommends`.
- Fix: explicitly include `gpgv` in `installer/Dockerfile` and `ca-certificates` in `installer/packages.txt`.
- Regression evidence: `./installer/build.sh` completed Debian signature verification and the final ISO build; the embedded manifest contains `ca-certificates`. The offline install smoke additionally exercises the resulting dependency closure.
- Avoid: assuming a developer machine or build container's installed tools will also exist in a minimal installer target.

## BUG-0002 — Hybrid boot objects lost during ISO remaster

- Date: 2026-09-08
- Symptom: xorriso rejected the initial image with `Cannot refer by isohybrid MBR to data outside of ISO 9660 filesystem`.
- Root cause: replacing the imported ISO's entire root with `-map` invalidated boot-object references; boot replay can also modify the boot-information bytes in `isolinux.bin` after media hashes are prepared.
- Fix: replay boot settings before `-update_r` to preserve imported objects, and exclude the regenerated boot catalog and patched `isolinux.bin` from media MD5 lists (`installer/build.sh`).
- Regression evidence: final build succeeded; independent artifact review verified BIOS/UEFI hybrid metadata and every listed media checksum. `tests/integration/iso-smoke.py` boots the actual ISO in both firmware modes.
- Avoid: rebuilding a hybrid ISO's boot layout from copied filenames alone, or hashing bytes that the ISO writer will subsequently patch.

## BUG-0003 — Package manifest used the wrong dpkg-deb operation

- Date: 2026-09-08
- Symptom: the draft manifest command treated its formatting expression as a field name rather than producing TSV rows.
- Root cause: `dpkg-deb -f` reads named control fields; it is not the formatted listing operation.
- Fix: use `dpkg-deb --show --showformat` in `installer/build.sh`.
- Regression evidence: independent final-image review checked a populated TSV manifest with package versions/architectures and the explicit CA-certificate package.
- Avoid: treating an output file's existence as proof its contents are valid inventory.

Build evidence above refers to Nex Core 0.1.0-alpha1 ISO SHA256
`b2862b1e31f8c784b54efd67a3496dcf66bc8252ce910109d59ea043853eb734`.
Runtime results are recorded separately with the ISO QA report; this ledger does
not claim physical-hardware or Secure Boot verification.

## BUG-0004 — Installer debconf environment leaked into target package setup

- Date: 2026-09-08
- Symptom: the first offline installation failed during CA-certificate configuration with bad debconf descriptors and later hung in PAM configuration.
- Root cause: invoking target package setup through raw `chroot` inherited Debian Installer frontend state without the target environment setup its packages need.
- Fix: use Debian Installer's `in-target /bin/sh` for the shared target configuration script (`installer/late-install.sh`).
- Regression evidence: run 1 reproduced the failure; `installer/check.sh` guards use of `in-target`. Fresh unattended run 3 passed offline installation, installed services, nested KVM and both installed boots against SHA256 `b02044bcac4205af247d52a30ff158ad4d73e7580382f666b37baf394538015f`; see [QA evidence](qa/iso/2026-09-08-alpha1.md).
- Avoid: substituting a raw chroot for the installer's environment-aware target helper, or treating successful ISO creation as proof of successful installation.
