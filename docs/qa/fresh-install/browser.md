# Fresh installed host browser walkthrough

Date: 2026-09-09. Chromium against the real packaged HTTPS services on the installed lab from the
[installation report](checks.md) (artifacts, environment, native checks and limits live there). No
mocked API or virtualization commands; no development binary replaced the package. Passwords came
from mode-0600 files and were masked.

## Method

Harness: [fresh_install_browser.py](../../../tests/integration/fresh_install_browser.py).

```sh
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage deploy --evidence docs/qa/fresh-install
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage managed --iso SELECTED_GUEST_ISO \
  --evidence docs/qa/fresh-install
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage negative --evidence docs/qa/fresh-install
# After stopping the Controller service inside its appliance:
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage offline --evidence docs/qa/fresh-install
# After restoring Controller, stopping the guest and rebooting Core:
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage post-reboot --evidence docs/qa/fresh-install
```

## Results (all five stages passed)

| Stage | Observed |
|---|---|
| [deploy](browser-deploy-results.json) | Keyboard root login before SSH setup; no VMs; ready preflight; selected `/var/lib/libvirt/images`, `br0`, static `10.0.2.20/24`, gateway `10.0.2.2`, DNS `10.0.2.3`, **Fresh install site**/**Fresh install pool**; one deployment `ready / 100%` without retries, kept after refresh |
| [managed](browser-managed-results.json) | Controller login; named site/pool, one fresh capable host, one adopted protected appliance; storage and bridge registered; Debian 13.6.0 netinst ISO uploaded; one `fresh-install-guest` (2 vCPU, 1024 MiB, 4 GiB, UEFI), task succeeded, observed `shut off`; no power action |
| [negative](browser-negative-results.json) | Anonymous 401; missing CSRF 403; invalid VM fields/ISO 400; unknown network 409; exact replay 200 with original UUID; changed replay 409; no resource or media changes. First harness run wrongly expected 202 on replay; fixed to documented 200/`vm_id`, no product change |
| [offline](browser-offline-results.json) | Controller API gave no response; fresh root login succeeded; appliance and guest shown running; no power action |
| [post-reboot](browser-post-reboot-results.json) | Root and Controller logins; host and both VM UUIDs persisted; appliance running/fresh; guest stopped/fresh with original config |
| Layout | 1440px and 360px deploy/VM review and VM details: no page overflow; keyboard sign-in, submit and stage heading focus passed; no page errors |

Inspected captures (legible, no material defect; narrow tabs/tables scroll in containers):
`browser-deploy-local-login-desktop.png` (not committed), `browser-deploy-review-360.png` (not committed),
`browser-deploy-ready.png` (not committed), `browser-managed-networking.png` (not committed),
`browser-managed-review-360.png` (not committed), `browser-managed-created-desktop.png` (not committed),
`browser-managed-created-360.png` (not committed), `browser-offline-local-login-360.png` (not committed),
`browser-post-reboot-central.png` (not committed).

## Review and limits

Independent read-only review found three harness evidence issues before execution (overwritten
progress captures, missing outage precondition, missing freshness assertion), all corrected; final
review of all stages and captures had no findings. Python compilation, capture-reference checks and
`check_docs.py --all` passed.

Invalid/stale/interrupted cases are in the [managed-VM report](../managed-vm/checks.md), not repeated.
No physical hardware, guest OS install, data-file persistence, central power/console or hosted CI.
