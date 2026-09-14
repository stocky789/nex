# Managed VM browser acceptance

Date: 2026-09-09. Served UI bundle SHA-256, requested VM spec, results and capture names:
[native results](browser-native-results.json), [simulated results](browser-simulated-results.json).

## Environment and method

Python Playwright with headless Chromium ran against the real HTTPS Controller and built UI at 1440×1000 and 360×800.
Native: disposable Debian 13 Nex Core copied-disk nested-KVM lab. Simulated (CI): real Controller and
enrolled mTLS identity, with simulated agent observations, host operations and ISO. Simulation does not establish storage or
libvirt correctness.

```sh
python tests/integration/managed_vm_browser.py --simulated --evidence docs/qa/managed-vm
python tests/integration/managed_vm_browser.py --config <disposable-lab>/browser.json --iso <debian-netinst.iso> --evidence docs/qa/managed-vm
```

The native secondary host was renamed by revoking and re-enrolling it. No image or DOM was altered.

## Observed results

- Loading, rejected sign-in and keyboard sign-in: passed.
- Simulated storage/network registration tasks completed. Native registration succeeded in earlier
  attempts; the final native repeat got HTTP 409 duplicates and reused those rows, which do not count as new successes.
- Invalid file-picker input rejected. The simulated run uploaded through the drop target and native uploaded the real
  Debian ISO. Both waited for the transfer task to succeed.
- Invalid VM names failed pattern validation. Keyboard Next/Back preserved values, and review showed
  CPU, memory, disk, network and firmware.
- Response deliberately lost after server acceptance: draft restored after reload, original request
  replayed, exactly one VM created.
- Details showed the expected spec, the succeeded task and `shut off` (not started).
- Injected stale observations and Controller failures displayed correctly, and reload recovered.
  These are presentation checks, not native outage evidence.
- Desktop/narrow captures inspected: legible, reachable, wrapping, no horizontal overflow (tabs
  scroll internally). No page errors.

Inspected captures (local only; `docs/qa/**/*.png` is gitignored): `browser-native-review-desktop.png`,
`browser-native-review-360.png`, `browser-native-interrupted-create.png`,
`browser-native-created-desktop.png`, `browser-native-created-360.png`, `browser-native-stale.png`,
`browser-simulated-empty.png`.

## Flat-field rendering regression

An isolated esbuild `onLoad` substitution reproduced the original flat-field conditional
(`vm.cpus !== undefined`). Playwright intercepted only that page's `app.js`. The CPU assertion failed with the
interception and passed without it. This is a controlled reproduction, not a historical checkout.
[Failure/pass results](browser-regression-results.json); capture `browser-regression-flat-fields.png` (local only).

## Limits

No guest install, browser console, power lifecycle or physical hardware. Native boot/outage evidence is
in [checks.md](checks.md). No hosted CI run. API impact: existing contract.
