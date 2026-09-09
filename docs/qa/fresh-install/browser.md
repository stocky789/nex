# Fresh installed host browser walkthrough

Date: 2026-09-09. This run used Chromium and the actual packaged HTTPS services on
an isolated, nested-KVM host installed onto a new blank disk through the shipped GUI.
No API responses or virtualization commands were mocked, and no development binary
replaced the installed package. The parent [installation report](checks.md) records
the installation, topology, native checks and remaining limits.

## Artifacts and method

The [artifact manifest](artifacts.json) identifies the `0.1.0+managed1` package and
signed Controller image. The installed ISO SHA-256 is
`5ca79c902ce39cb11c15f710abbf6629d2785089eb17b641435f8240af5e1dff`.
The browser result files also retain the selected artifact identities and browser version.
Local and central HTTPS were reached through loopback forwards into the isolated
lab. Passwords came from mode-0600 private files, were never printed, and password
fields were masked in captures.

The runnable harness is [fresh_install_browser.py](../../../tests/integration/fresh_install_browser.py).
Executed stages used the existing Playwright environment:

```sh
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage deploy --evidence docs/qa/fresh-install
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage managed --iso SELECTED_GUEST_ISO \
  --evidence docs/qa/fresh-install
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage negative --evidence docs/qa/fresh-install
# After the parent stopped the actual Controller service inside its appliance:
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage offline --evidence docs/qa/fresh-install
# After the parent restored Controller, stopped the selected guest and rebooted Core:
.cache/phase1-qa-venv/bin/python tests/integration/fresh_install_browser.py \
  --config PRIVATE_LAB_CONFIG --stage post-reboot --evidence docs/qa/fresh-install
```

## Actual results

- [Deployment results](browser-deploy-results.json): keyboard root sign-in succeeded
  before any lab-only SSH setup. The installed host had no VMs or deployments and
  reported ready preflight. The GUI selected existing `/var/lib/libvirt/images`,
  installer-created `br0`, static `10.0.2.20/24`, gateway `10.0.2.2`, DNS `10.0.2.3`,
  and the names **Fresh install site** / **Fresh install pool**. One deployment
  reached `ready / 100%` without fixes or retries; refresh retained it.
- [Managed-VM results](browser-managed-results.json): keyboard Controller sign-in
  verified that named site/pool, one freshly connected capable host and exactly one
  adopted protected Controller appliance existed. GUI registrations for the existing
  directory and bridge succeeded; the genuine Debian 13.6.0 netinst ISO uploaded and
  transferred successfully. Four creation pages produced exactly one
  `fresh-install-guest`, assigned to that pool/host, with 2 vCPUs, 1024 MiB memory,
  a 4 GiB disk and UEFI. Its creation task succeeded and fresh observations reported
  `shut off`. The harness issued no guest power action.
- [Focused native API checks](browser-negative-results.json): anonymous storage/media/VM
  access returned 401; missing CSRF returned 403; invalid VM fields and ISO content
  returned 400; a nonexistent network returned 409. Exact completed creation replay
  returned 200 with the original VM UUID; changed replay returned 409. All resource
  identity sets were unchanged and invalid media was not published. An initial
  harness assertion incorrectly expected creation status 202 on replay; correcting
  it to the documented 200/`vm_id` contract passed without product changes.
- [Actual outage/local recovery](browser-offline-results.json): while the parent
  had stopped Controller inside its appliance, its API returned no HTTP response.
  A fresh root browser login still succeeded and local inventory showed both the
  appliance and ordinary guest running. This was a service outage, not a mocked
  API response or stopped appliance. The browser issued no power action.
- [After actual host reboot](browser-post-reboot-results.json): fresh root and
  Controller sign-ins succeeded. The original enrolled host and both VM UUIDs
  persisted, the protected appliance was running/fresh, and the ordinary guest
  remained stopped/fresh with its original requested configuration. Native boot-ID,
  XML, disk and certificate checks are in the parent installation report.
- Desktop (1440px) and narrow (360px) deployment/VM reviews and VM details passed
  page-overflow checks. Keyboard sign-in, review submission and creation-stage
  heading focus passed. No browser page errors were observed.

Inspected captures: [initial host overview](browser-deploy-local-login-desktop.png),
[narrow deployment review](browser-deploy-review-360.png),
[deployment ready](browser-deploy-ready.png),
[network registration](browser-managed-networking.png),
[narrow VM review](browser-managed-review-360.png),
[desktop VM details](browser-managed-created-desktop.png), and
[narrow VM details](browser-managed-created-360.png), and
[narrow local recovery during Controller outage](browser-offline-local-login-360.png),
and [post-reboot central VM details](browser-post-reboot-central.png).
Labels, requested resources, actual task status, freshness and stopped state were
readable; no material visual defect was found in these inspected views. Narrow
resource tabs and tables use their existing scrollable containers.

## Impact and limits

API impact: **existing contract** — the harness exercises local root sessions and
appliance deployment, central sessions/inventory, storage/network registration,
ISO import and VM creation through the existing browser → Controller → Core command
and observed-result flow. No interface or runtime behavior changes.
Guide impact: **none** for this QA harness: the existing operator workflows were
executed; the parent documentation change attaches this evidence to the guide.

The separate earlier [managed-VM report](../managed-vm/checks.md) covers invalid,
stale, interrupted and recovery cases; this fresh-install run does not claim to
repeat those tests. Physical hardware, guest operating-system installation,
central power/console operations, hosted CI and external release clearance remain
outside these browser results. All five harness stages passed; these results do not establish guest OS installation or data-file persistence.

Independent read-only review checked the harness, result identities/artifact digests
and deployment/managed-VM captures. Three harness evidence issues (overwritten progress
captures, missing outage precondition and missing freshness assertion) were corrected
before execution; correction review found no remaining issues. Negative/post-reboot
branches also received independent review.
Final independent review also inspected the outage/reboot records and captures:
no findings; local running inventory, preserved identities/configuration and fresh
post-reboot states match the report. Native service-stop/reboot proof remains in
the parent report. Python compilation, result/capture-reference checks and
`python3 scripts/check_docs.py --all` passed; structural checking does not establish
changed-file assessment coverage or hosted CI execution.
