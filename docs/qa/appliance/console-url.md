# Local management console URL verification

Date: 2026-09-08. Development source, local UI package setup/service, ISO bundling.

API impact: none. Existing address configuration and HTTPS listener supply console text;
no Controller operation, agent command/result, or client API changes. Guide impact: updated.

## Checks executed

`python3 -m unittest discover -s tests -p test_core_local_package.py` and `installer/check.sh`
cover package console text and installer bundling assertions. Real OpenSSL generation verified
unchanged credentials and certificate rotation; temporary files and mocked reload calls verified
public atomic issue output and refresh invocation. Failed discovery clears an earlier URL; the
service stop action replaces the URL with an unavailable message.

A fresh disposable Debian 13 `nex-iso-builder` container previously loaded the setup source
read-only and called initialize with documentation-only IPv4/IPv6 addresses. Native
`agetty --show-issue` with a pseudo-terminal on stdin found the default `/run/issue.d`
fragment without explicit file overrides, preserving the original Debian banner. Inspected
rendered output included `https://192.0.2.5:9443` and then `https://[2001:db8::5]:9443`.

Installer sources now copy `nex-core-local` into the offline ISO repository, append it to the
install package list, enable `nex-core-local.service`, and replace the old “no Controller”
MOTD. Source checks assert `genisoimage`, console issue escapes, and bundling hooks.

## Limits

This is native terminal rendering and source-level installer checks, not a rebuilt ISO boot.
Live prompt reload was invoked but no already-waiting getty was exercised on a fresh ISO
install. Rebuilding the ISO and verifying the pre-login banner on a disposable installed host
remains required before claiming end-to-end installer acceptance. No workstation network or
installed-host configuration was changed.
