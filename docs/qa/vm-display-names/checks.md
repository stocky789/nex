# VM display-name checks

Date: 2026-09-11. Development 0.1.0 working tree. Chromium with a real disposable HTTPS Controller and a
simulated mTLS host. No real domain creation, rename or guest execution.

## Results

- `go test ./...` passed. Names with spaces, case, dots and Unicode survived validation, creation, rename, replay
  and fresh-Controller adoption. Core round-trips XML escaping, reopens the journal, keeps the UUID and rejects
  space-padded duplicates.
- Regression proof: before the fix, adoption lost the VM and validation failed the new bounds. Restoring the old
  hostname guard fails all five name cases, and restoring the whitespace-stripping parser fails the duplicate test.
- `npm --prefix web run typecheck`/`test`/`build`: passed.
- `managed_vm_browser.py --simulated --evidence .cache/vm-display-names-browser`: passed. The mixed name was
  created; blank, slash and over-limit names were refused; keyboard flow, lost-response retry, hyphen rename, reload,
  desktop/360 px, stale/error and recovery passed. Captures were inspected but kept local, because they contain generated lab IDs.
- `generate_api.py --check` and 21 API contract tests passed.

Both phase1 workflows run these suites. No remote CI or live infrastructure.

## Limits

Native libvirt rename was source-reviewed only; KVM execution not exercised. The argv now ends options before
the name ([virsh parser](https://raw.githubusercontent.com/libvirt/libvirt/master/tools/vsh.c),
[libvirt name schema](https://raw.githubusercontent.com/libvirt/libvirt/master/src/conf/schemas/basictypes.rng)).
Core XML/journal checks use fake virsh. Older Core/Controller reject the expanded name syntax.
