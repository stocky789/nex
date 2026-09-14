# Local Core browser acceptance

Date: 2026-09-08. Development working tree; no release or hosted CI claim.

## Environment and method

[`tests/integration/localcore_browser.py`](../../../tests/integration/localcore_browser.py) against
an actual `nex-core-local` HTTPS subprocess and production React build, Playwright Chromium.
Temporary state, keys and fixtures removed after each run; TLS certificate ignored (no trust
ceremony verified).

**VM execution is simulated.** PATH fixtures intercept `virsh`, `qemu-img`, `genisoimage`; a tiny
signed file exercises real manifest/signature validation but no QEMU, guest, cloud-init or
appliance boot ran. Host bridge, KVM, memory and Btrfs free space were read only. Real HTTP, auth,
CSRF, validation, journals and serialization; browser interception only holds the session request
(loading capture) and discards one handled deployment POST response (lost response). Inventory
failure is injected by failing simulated `virsh`.

```sh
.cache/toolchains/go/bin/go build -o dist/nex-core-local ./cmd/nex-core-local
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only --captures .cache/localcore-browser-blocked
```

Both printed `PASS`; binary/UI SHA-256 and times in [full](browser-captures/results-full.json)
and [blocked](browser-captures/results-blocked.json) results. Full mode needs a bridge, KVM and
36 GiB free supported storage (missing prerequisites fail); `--blocked-only` is portable and does
not claim deployment/power coverage.

Historical commands; new full simulations require `--disposable-full` with host authentication in
disposable Debian containers — see [root-login checks](root-auth.md).

## Actual results and inspected captures

| Capture | Result |
| --- | --- |
| `browser-captures/loading.png` (not committed) | Labeled progress indicator |
| `browser-captures/invalid-login.png` (not committed) | Rejected; input clears, keyboard access kept; login follows rate delay |
| `browser-captures/empty-desktop.png` (not committed) | Defaults 2 CPU, 4 GiB, 32 GiB; empty VM/deployment states |
| `browser-captures/missing-bridge.png` (not committed) | Preflight reports nonexistent bridge, deployment disabled |
| `browser-captures/deployment-review-desktop.png` (not committed) | Empty selection blocks; storage/bridge, resources, static IPv4, names reach review; password absent |
| `browser-captures/durable-progress.png` (not committed) | After lost POST response, reload finds one operation; queued → simulated start failure |
| `browser-captures/deployment-start-failure.png` (not committed) | Same UUID, `needs attention`, recovery action; Retry keeps one operation |
| `browser-captures/power-confirmation.png` (not committed) | Simulated start; wrong hostname keeps Force stop disabled; correct executes, inventory `shut off` |
| `browser-captures/overview-360.png` (not committed) | Readable, no horizontal overflow |
| `browser-captures/inventory-error-360.png` (not committed) | Real API error, stale warning, controls disabled; Retry connection clears |

All ten captures inspected: no clipped controls or material defects. Password submit and sign-out
exercised by keyboard; no JS errors. Reload preserves one deployment; no service-restart claim.

## Limits

Real appliance boot, enrollment/adoption, seed cleanup, 100% ready, fingerprint ceremony, guest
persistence and hardware need [real-lab evidence](lab.md). Suite ends at a failed simulated start
plus local power recovery. Server-side auth/ownership matrices are Go tests.
