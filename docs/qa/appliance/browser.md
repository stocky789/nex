# Local Core browser acceptance

Date: 2026-09-08. Development working tree; no release or hosted CI claim.

## Environment and method

Executed [the runnable browser check](../../../tests/integration/localcore_browser.py)
against an actual `nex-core-local` HTTPS subprocess and production-built React assets,
using Playwright Chromium from the existing disposable QA Python environment.
Private temporary service state, enrollment directory, signing keys and fixture images
were removed after each run. The browser ignored the disposable local TLS certificate;
this check does not verify an operator's certificate trust ceremony.

**VM execution is simulated.** Private PATH fixtures intercepted every `virsh`,
`qemu-img` and `genisoimage` command. A tiny signed file supplied real manifest/signature
validation, but was never opened by real QEMU. The fixture stored domain XML and power
state locally. No guest OS, cloud-init or Controller appliance boot ran in this test.
The actual host's existing bridge, KVM availability, memory and Btrfs free-space checks
were read only; no network, disk, service or VM on the host was changed.

The suite uses real HTTP responses, authentication, CSRF handling, validation,
operation journals and observed-state serialization. Browser interception only holds
the initial session request for a loading capture and discards a real deployment POST
response after the server handles it, to reproduce a lost response. VM inventory
failure is injected by making the simulated `virsh` process fail, not by inventing
an API error response.

Commands executed successfully:

```sh
.cache/toolchains/go/bin/go build -o dist/nex-core-local ./cmd/nex-core-local
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only --captures .cache/localcore-browser-blocked
```

Both runs printed `PASS`. Exact binary and built UI SHA-256 values and execution times
are retained in [full-mode results](browser-captures/results-full.json) and
[blocked-mode results](browser-captures/results-blocked.json).
Full mode requires an existing bridge, available KVM and supported local storage with
36 GiB free; missing prerequisites fail the check. Explicit `--blocked-only` mode is
portable and does not claim the full deployment or power workflow was exercised.

## Actual results and inspected captures

- [Loading](browser-captures/loading.png): held session read displays a labeled progress indicator.
- [Invalid login](browser-captures/invalid-login.png): wrong password is rejected; password input clears and retains keyboard access. Successful login follows the actual login-rate delay.
- [Empty desktop](browser-captures/empty-desktop.png): authenticated host defaults show 2 CPUs, 4 GiB RAM and 32 GiB disk, with empty VM and deployment states.
- [Missing bridge](browser-captures/missing-bridge.png): the real preflight reports the deliberately nonexistent bridge and disables deployment.
- [Deployment review](browser-captures/deployment-review-desktop.png): required empty selection blocks progression; explicit storage/bridge, resources, static IPv4 and workspace names reach review. The password is absent from review text and captures.
- [Durable progress](browser-captures/durable-progress.png): after losing the real committed POST response, browser reload finds exactly one stored operation. It advances from queued to a simulated VM start failure.
- [Start failure](browser-captures/deployment-start-failure.png): the existing operation retains its UUID and shows `needs attention`, an incomplete progress bar and a recovery action. Clicking Retry deployment retains one operation.
- [Power confirmation](browser-captures/power-confirmation.png): the real local API starts the simulated owned VM; wrong hostname keeps Force stop confirmation disabled. Correct hostname executes the simulated force stop, and refreshed inventory reports `shut off`.
- [360px overview](browser-captures/overview-360.png): observed VM state, recovery controls and operation history remain readable without page horizontal overflow.
- [Inventory failure](browser-captures/inventory-error-360.png): failing the simulated inventory subprocess produces the real API error, stale warning and disabled power/deployment controls. Restoring the subprocess and Retry connection clears the error.

All ten retained screenshot states were visually inspected. Text, focus outlines,
buttons, progress and warnings are readable; no clipped controls or material visual
defects were observed. Password entry/submit and focused sign-out were exercised by
keyboard. No JavaScript page errors were emitted. Reload preserves one deployment;
these runs do not claim a service-process restart walkthrough.

## Limits and impact

A genuine Debian appliance boot, successful enrollment/adoption, seed cleanup,
100% ready link, certificate fingerprint ceremony, guest persistence and hardware
correctness require separate real-lab evidence. This browser suite deliberately ends
at a failed simulated start plus explicit local power recovery; it never labels that
a successfully installed Controller. Server-side auth and ownership failure matrices
are covered by focused Go tests, not established solely by browser success responses.

API impact: **existing contract** — the check exercises local session, host, VM,
deployment, retry and power APIs without changing their runtime contracts.
Guide impact: **updated evidence** — this report records exactly which local UI steps
were executed and separates simulated VM outcomes from real appliance acceptance.

Current root-login harness note: the historical commands above describe their recorded
run. New full simulations require `--disposable-full`; the harness now runs host
authentication in disposable Debian containers. See [root-login checks](root-auth.md) for current evidence.
