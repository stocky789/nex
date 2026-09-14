# Controller deployment usability — 2026-09-08

## Scope and availability

Deployment drafts default to DHCP (static IPv4 under Network settings; hidden static fields cleared).
Sixteen-character password minimum removed across UI, host validation, guest firstboot and
Controller bootstrap. Local power uses named action/VM confirmation without typed host names.
Routine Observed timestamp badge removed; stale warnings and protections kept. Artifacts and native
guest checks: [rebuild verification](usability-rebuild.md). Old guest firstboot code still rejects
short passwords. No installed host changed.

## Executed browser walkthrough

TypeScript check and production build passed. `localcore_browser.py --blocked-only` against a
current binary with real HTTPS/root PAM in a disposable Debian 13 container passed: static and DHCP
payloads, hidden settings cleared, short/Unicode/space passwords, empty/oversized validation, three
power confirmations/cancel/payloads, timestamp removal, stale/session-loss/restart protections.
Deployment/power responses and VM commands were fixtures.

[Results and hashes](deployment-usability/results.json),
`deployment-usability/deployment-dhcp-desktop.png` (not committed),
`deployment-usability/deployment-static-desktop.png` (not committed),
`deployment-usability/deployment-dhcp-mobile.png` (not committed),
`deployment-usability/deployment-dhcp-review-mobile.png` (not committed),
`deployment-usability/deployment-submit-error-mobile.png` (not committed),
`deployment-usability/power-confirmation-mobile.png` (not committed).
Implementation and independent review inspected captures: readable, no overflow, passwords absent
from review, force-stop names target and consequence. Both phase1 workflows run this harness.

## Password and TLS checks

- Password regression reproduced the former rejection before the fix; Go policy, Controller/CLI and
  race checks passed; guest appliance Python 10/10. Controller test bootstraps a short password,
  rejects wrong, accepts configured. Single-character, Unicode, space pass; NUL/line endings and
  oversized rejected.
- Agent tests and race passed: hostname identity over IP connection, legacy empty server name,
  untrusted/wrong-name rejection, malformed names, persisted state, endpoint refresh keeping host
  identity. No `InsecureSkipVerify`.

## Research and limits

Uses [cloud-init network v2](https://docs.cloud-init.io/en/latest/reference/network-config-format-v2.html)
and the [libvirt guest-agent channel](https://www.libvirt.org/formatdomain). Mocks do not prove
native DHCP, guest boot or post-lease-change enrollment; the [native guest test](usability-rebuild.md)
covers DHCP/guest bootstrap. Full localcore deployment/enrollment, live lease change and hardware
remain open.

## Final backend and independent review

`go test ./...` passed; Python discovery 58/58 (API 17, appliance 10); focused localcore/vmhost/agent
race passed; API references current. Backend regressions: DHCP seed/validation, management MAC and
owned VM lookup, unusable/ambiguous/missing addresses, unavailable/canceled waits, hostname TLS at
IP, pending/ready DHCP startup ordering; startup or retry re-discovers the Controller and updates
its URL without new VM/enrollment. No periodic lease monitor. Typed power confirmation ignored for
legacy clients; requests without it still reach ownership checks.

Independent review found incorrect DHCP restart ordering, corrected and regression-tested; approved
source/API scope and captures with no blocking findings.
