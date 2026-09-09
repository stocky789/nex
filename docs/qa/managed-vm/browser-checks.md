# Managed VM browser acceptance

Date: 2026-09-09. Development working tree; exact served UI bundle SHA-256,
requested VM specification, observed result and inspected capture names are recorded
in [native results](browser-native-results.json) and
[simulated results](browser-simulated-results.json).

## Environment and method

Python Playwright with headless Chromium exercised the real HTTPS Controller and
built React UI at 1440×1000 and 360×800. The native backend used the explicitly
selected disposable Debian 13 Nex Core copied-disk nested-KVM lab. The simulated
CI mode used a real Controller and enrolled mTLS identity, but deliberately simulated
agent observations, successful host operations and a synthetic ISO. Simulation does
not establish storage or libvirt correctness.

Commands run from the repository root, using the installed QA Python environment
and cached Go toolchain locally:

```sh
python tests/integration/managed_vm_browser.py --simulated --evidence docs/qa/managed-vm
python tests/integration/managed_vm_browser.py --config <disposable-lab>/browser.json --iso <debian-netinst.iso> --evidence docs/qa/managed-vm
```

The private native config selects the prepared lab Controller and host; its adjacent
password file is not captured or committed. Fresh simulated fixture host labels are
set through the enrollment API. The native secondary fixture host was renamed by
revocation and replacement enrollment, not image or DOM alteration.

## Observed results

- Loading, rejected sign-in and successful keyboard sign-in passed.
- Simulated GUI storage and network registrations each completed their real
  Controller task against the simulated agent. Native storage registration to the
  new browser subdirectory and existing `virbr0` bridge registration succeeded
  during preceding native walkthrough attempts. The final repeat deliberately
  encountered explicit HTTP 409 duplicate-registration errors and reused these
  validated registrations; the final repeat does not count those rejected
  submissions as new registration success.
- Invalid file-picker input was rejected. The simulated run then uploaded through
  the drop target; native uploaded the real Debian ISO through the file picker.
  Both waited for the recorded transfer task to succeed.
- Invalid uppercase/spaced VM names failed native browser pattern validation.
  Keyboard Next focused the next stage heading, Back preserved disk size, and
  review displayed the selected CPU, memory, disk, network and firmware.
- The server accepted the creation request before Playwright deliberately lost
  its response. The browser retained the draft, restored it after page reload,
  and replayed the original request. Exactly one VM matched the requested name.
- Details displayed the expected CPU count, memory, disk size, BIOS firmware and
  succeeded task, with observed `shut off`. These tests did not start that VM.
- Injected stale observations displayed unknown current execution with the last
  reported state. Injected Controller request failure displayed an error, and
  reload recovered. These injected presentation checks are not native outage
  evidence; native process-outage checks are recorded separately.
- Desktop and narrow review/details captures were inspected: fields remain legible,
  actions remain reachable, long identities wrap, and the page does not overflow
  horizontally. Resource tabs may scroll within their own navigation. No browser
  page errors occurred.

Representative inspected evidence: [native review](browser-native-review-desktop.png),
[narrow review](browser-native-review-360.png),
[lost reply](browser-native-interrupted-create.png),
[created VM](browser-native-created-desktop.png),
[narrow details](browser-native-created-360.png),
[stale state](browser-native-stale.png), and
[simulated empty pool](browser-simulated-empty.png).

## Flat-field rendering regression

The runnable browser test asserts actual values under the nested API `vm.spec`
contract, including CPU, memory, disk and firmware, and independently checks the
creation task. An isolated esbuild `onLoad` substitution reproduced the original
flat-field conditional (`vm.cpus !== undefined`) without changing working source
or the served bundle. Playwright intercepted only that test page's `app.js`.
The CPU assertion failed because the fields were absent; removing interception
made the same field assertions pass. This is an isolated reproduction of the
original rendering shape, not a claim to have checked out a historical revision.
See [failure/pass results](browser-regression-results.json) and
[missing-field capture](browser-regression-flat-fields.png).

## Limits and impact

No guest OS installation, browser console or power lifecycle is claimed. No
physical hardware was touched. Native BIOS/UEFI boot and outage evidence belongs
to the separate managed-VM integration report. Hosted CI results require inspection
after an authorized push; local passes alone do not establish hosted enforcement.

API impact: existing contract — this test exercises Controller session,
storage/network/media/VM operations and the agent enrollment/report contracts;
it adds no runtime API. Guide impact: updated in the encompassing managed-VM slice;
this report provides walkthrough evidence for the create-VM guide.
