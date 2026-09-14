# Local management console URL verification

Date: 2026-09-08. Development source, local package setup/service, ISO bundling.

## Checks executed

| Check | Result |
| --- | --- |
| `python3 -m unittest discover -s tests -p test_core_local_package.py`, `installer/check.sh` | Passed: console text, bundling hooks (`genisoimage`, issue escapes, ISO repository copy, package list, `nex-core-local.service` enablement, MOTD) |
| Real OpenSSL generation | Unchanged credentials and certificate rotation verified; mocked reload verified atomic issue output and refresh; failed discovery clears URL; service stop shows unavailable message |
| Disposable Debian 13 `nex-iso-builder` container, native `agetty --show-issue` on a pty | Default `/run/issue.d` fragment found without overrides, Debian banner kept; rendered `https://192.0.2.5:9443` then `https://[2001:db8::5]:9443` |

## Limits

Native terminal rendering and source-level installer checks, not a rebuilt ISO boot. No
already-waiting getty on a fresh install exercised; pre-login banner on an installed host remains
required for end-to-end acceptance.
