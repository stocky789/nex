# Console banner cleanup — 2026-09-08

Startup notice, pre-login banner and MOTD now show only the HTTPS URL (no fingerprint or
root-login text).

| Check | Result |
| --- | --- |
| `python3 -m unittest discover -s tests -p test_core_local_package.py` | 9 passed: exact URL-only notice/MOTD with real certificate creation/preservation and address renewal; banner atomic-write/permissions. Getty refresh mocked |
| Independent review | 9 package checks passed; obsolete guide MOTD command found and replaced with openssl |

## Rebuilt artifacts

| Artifact | SHA256 |
| --- | --- |
| `nex-core-local_0.1.0+console1_amd64.deb` | `683c378a00ad1a9b11819212b8f37b535cb4f88dd1a67b15fb2202006d67676d` |
| Matching source | `11b1f207001fa733df34c9618ada12c1ce8d125934ed4f0411e37bab77290c4e` |
| `dist/nex-core-0.1.0-alpha1-amd64.iso` | `65f8d82dc986b82023be683bdcc0da41760e25a173c6f3ec045a6d826abe85ff` |

Independent disposable Debian package smoke exited zero: URL-only initialization, setup matches
source, root PAM, password rotation, locked/expired/wrong-password rejection, TLS,
removal/state preservation. ISO checksum passed; embedded console1 package extracted and digest
compared; management bridge and root-login changes included.

Limits: no installed console rendering, full host boot or ISO installation walkthrough.
