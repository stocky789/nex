# Console banner cleanup — 2026-09-08

Removed automatic certificate fingerprint and root-login instructions from the local
service startup notice, pre-login banner and MOTD. The successful notice contains
only the HTTPS web URL. TLS generation/validation and authentication are unchanged;
the guide retains the explicit command to inspect a certificate fingerprint when needed.

Executed `python3 -m unittest discover -s tests -p test_core_local_package.py`: nine
checks passed. The temporary-filesystem initialization check asserts the exact URL-only
notice and matching MOTD while exercising real certificate creation/preservation and
address-change renewal. Getty refresh is mocked; no installed console rendering or
full host boot is claimed. Existing banner atomic-write/permissions checks passed.

API impact: none — presentation of an existing local URL only; no Controller operation,
agent command, local session or client API changes. Guide impact: updated.
The source change is included in the rebuilt package and ISO recorded below.

Independent review passed nine package checks and identified the obsolete guide MOTD
command; it now reads the certificate directly with openssl. Documentation structure
and whitespace checks passed.

## Rebuilt artifacts

Built `nex-core-local_0.1.0+console1_amd64.deb` with SHA256
`683c378a00ad1a9b11819212b8f37b535cb4f88dd1a67b15fb2202006d67676d`.
Matching source SHA256: `11b1f207001fa733df34c9618ada12c1ce8d125934ed4f0411e37bab77290c4e`.
Independent disposable Debian package smoke passed (exit zero): actual initialization
printed the URL only; packaged setup matches current source. Root PAM, password rotation,
locked/expired/wrong-password rejection, TLS and removal/state preservation passed.

Rebuilt `dist/nex-core-0.1.0-alpha1-amd64.iso`, SHA256
`65f8d82dc986b82023be683bdcc0da41760e25a173c6f3ec045a6d826abe85ff`.
Checksum verification passed and the embedded console1 package was extracted for digest
comparison. Management bridge and root-login changes remain included. No new full
installation/boot walkthrough was run for this rebuild.
