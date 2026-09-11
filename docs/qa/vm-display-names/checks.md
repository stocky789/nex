# VM display-name checks

Date: 2026-09-11. Development 0.1.0 working tree; Chromium with a real disposable HTTPS
Controller and simulated mTLS host. No real domain creation, rename or guest execution.

## Results

- `go test ./...` passed all Go packages. Shared name validation, Controller creation,
  rename/replay/report refresh and fresh-Controller adoption preserve names with spaces,
  case, dots and Unicode. Core tests round-trip XML escaping, reopen the managed journal,
  preserve UUID identity, and reject a duplicate name including surrounding spaces.
- Before implementation, the new adoption check failed (the host-reported VM disappeared),
  and shared validation failed its new invalid-name bounds. Restoring the original Core
  hostname guard through a temporary Go overlay makes all five human-name cases fail.
  Restoring the old whitespace-stripping list parser makes the padded-duplicate test fail.
- `npm --prefix web run typecheck`, `npm --prefix web test`, and
  `npm --prefix web run build` passed. Existing disk-label sanitization is unchanged.
- `.cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated
  --evidence .cache/vm-display-names-browser` passed. Typed and created a mixed-case name
  with spaces, a dot and Unicode; blank, slash and over-limit names were refused. The
  six-step keyboard flow, lost-response retry, exact name retention, rename with leading
  hyphens, reload, desktop/360px presentation, stale/error states and recovery passed.
  Inspected review, created narrow-view and renamed desktop captures. Captures/results
  remain local under `.cache/vm-display-names-browser` because they contain generated lab IDs.
- `python3 scripts/generate_api.py --check` and the 21 API contract tests passed.

Both GitHub and Gitea phase1 workflows run the Go packages, UI tests and simulated browser
suite on pushes and pull requests. No remote CI run or live infrastructure was invoked.

## Limits

The native libvirt rename call is source-reviewed; its full execution requires KVM and was
not exercised. The existing UUID-based argv call now terminates options before the name.
[Upstream virsh parser](https://raw.githubusercontent.com/libvirt/libvirt/master/tools/vsh.c)
supports that separator; [libvirt's name schema](https://raw.githubusercontent.com/libvirt/libvirt/master/src/conf/schemas/basictypes.rng)
allows spaces and Unicode while excluding slash/newline. Core XML and journal checks use
fake virsh output and do not establish native hypervisor acceptance. Upgrade Core and
Controller together; older versions reject the expanded name syntax.
