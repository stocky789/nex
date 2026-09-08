# Controller deployment usability — 2026-09-08

## Scope and availability

New deployment drafts use DHCP by default, with static IPv4 available under Network
settings. Static-only fields are hidden and cleared from DHCP submissions. Short
nonempty passwords are accepted; the sixteen-character minimum is removed across
UI, host validation, guest firstboot and Controller bootstrap. Existing transport
bounds/encoding validation remain. Local VM power controls use a named action/VM
confirmation without typed host names. The routine Observed timestamp badge is gone;
stale-data warnings and control protections remain.

The matched `0.1.0+usability1` appliance and local package have now been rebuilt;
see [rebuild verification](usability-rebuild.md) for artifact hashes and native guest
checks. Old guest firstboot code still rejects short passwords. No installed host
was changed.

API impact: changed contract — optional `network_mode` on local deployment/recovery,
omitted mode remains static for older clients; DHCP discovery updates operation URLs.
Agent trust retains certificate verification with an optional persisted TLS server name.
Controller bootstrap accepts short passwords. Typed `confirm_host` is no longer required
for local power; authentication, CSRF, target identity and ownership checks remain.
Guide impact: updated.

## Executed browser walkthrough

TypeScript checking and production UI build passed. The existing Chromium harness ran
against a current host-local binary with real HTTPS/root PAM in a disposable Debian13
container (`localcore_browser.py --blocked-only`). It passed both static and DHCP
request payload checks, cleared hidden settings, short/Unicode/space password acceptance,
empty/oversized validation, all three power confirmations/cancel/payloads, removal of
the timestamp, and existing stale/session-loss/restart protections. Deployment/power
responses and VM commands were fixtures, not real VM effects.

[Results and binary/UI hashes](deployment-usability/results.json),
[DHCP desktop](deployment-usability/deployment-dhcp-desktop.png),
[static settings](deployment-usability/deployment-static-desktop.png),
[DHCP mobile](deployment-usability/deployment-dhcp-mobile.png),
[mobile review](deployment-usability/deployment-dhcp-review-mobile.png),
[submission error](deployment-usability/deployment-submit-error-mobile.png), and
[force-stop confirmation](deployment-usability/power-confirmation-mobile.png).
Implementation and independent review inspected desktop/mobile captures: fields and
buttons are readable without horizontal overflow; passwords never appear in review;
force-stop still names its target and consequence. Both phase1 workflows execute this
expanded browser harness and existing Go/Python regression discovery on push/PR.
Hosted CI was not executed during this change.

## Password and TLS checks

Password regression reproduced the former rejection before the fix. Owned Go policy,
Controller/CLI and race checks passed; guest appliance Python suite passed10/10.
The actual Controller test bootstraps a short password, rejects a wrong password and
accepts the configured password. Single-character, Unicode and space handling pass;
NUL/line endings and oversized input remain rejected consistently.

Agent tests and race checks passed: verified hostname identity over an IP connection,
legacy empty server-name compatibility, untrusted/wrong-name certificate rejection,
malformed names, persisted state and safe endpoint refresh preserving host identity.
No `InsecureSkipVerify` bypass was added.

## Research and limits

The implementation uses existing platform behavior documented by
[cloud-init network v2](https://docs.cloud-init.io/en/latest/reference/network-config-format-v2.html)
and the [libvirt guest-agent channel](https://www.libvirt.org/formatdomain).
Mocked address responses and browser fixtures do not prove native DHCP lease assignment,
a complete Controller guest boot, physical networking or enrollment after an actual
lease change. The later [native guest test](usability-rebuild.md) verifies actual DHCP/guest bootstrap;
full localcore deployment/enrollment, live lease-change and hardware checks remain open.

## Final backend and independent review

`go test ./...` passed all packages; Python discovery passed58/58, including API17
and appliance10. Focused localcore/vmhost/agent race suites passed independently.
API generated references are current; documentation structure and whitespace checks
passed (`--all` does not establish committed-range coverage).

Backend regressions cover DHCP seed/validation, exact management MAC and owned VM
lookup, unusable/ambiguous/missing addresses, unavailable/canceled waits, real hostname
TLS at an IP connection, and both pending/ready DHCP startup ordering before the agent
runs. Startup or explicit retry re-discovers and verifies the existing Controller,
updates its saved URL without a new VM/enrollment, and preserves agent identity.
There is no periodic lease monitor. Typed power confirmation is optional/ignored for
legacy compatibility; tests prove requests without it still reach ownership checks.

Independent review caught incorrect DHCP restart ordering, which was corrected and
regression-tested before acceptance. It approved source/API scope and inspected browser
captures; no blocking implementation findings remained. Later native guest boot and artifact delivery are recorded in the rebuild report;
full localcore enrollment and hardware gates remain open.
