# First managed VM development checks

Date: 2026-09-09. Scope: [first managed VM slice](../../plans/2026-09-09-first-managed-vm.md)
(API impact: new contract). Status: native/browser checks and independent review passed.

## Executed root checks

- `go test -race -count=1 ./...` (retained toolchain): all passed; agent 2.504s, Controller 36.728s,
  localcore 10.045s, vmhost 1.341s, websecurity 1.010s.
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 62 passed. The API owner also passed 18 contract checks.
- `npm --prefix web run typecheck`/`build`, `python3 scripts/generate_api.py --check`: passed.
- `tests/integration/phase1.py --browser`: 16 groups passed (libvirt simulated). The first attempt, with the
  workstation Go build, stopped because its GOROOT lacked `LICENSE`. A rerun with the retained toolchain passed.
- Source-retention checks passed after the new harnesses were added to the archive allowlist.
- `git diff --check`, `check_docs.py --all`: passed (structural only).

## Native and browser evidence

```sh
python3 tests/integration/managed_vm_lab.py --copy-from .cache/nex-appliance-lab.<redacted>
GO="$PWD/.cache/toolchains/go/bin/go" python3 tests/integration/managed_vm.py --lab-dir .cache/nex-managed-vm-lab.<redacted> --iso .cache/nex-iso/debian-13.6.0-amd64-netinst.iso --evidence docs/qa/managed-vm --keep-running
```

[Native results](results.json): real Controller → enrolled agent → libvirt on a copied Debian 13 host
with nested KVM, loopback-only forwards, disposable file disks, and the packaged systemd agent unit with only path/timing
overrides. Input hashes were unchanged during the run. Passed: local directory/network verification,
invalid/missing resources, cross-pool placement, unsupported capability, wrong-host credentials,
ISO transfer (invalid/interrupted/successful), capacity rejection, stopped definitions, replay,
Controller outage and agent/Controller restart. The secondary identity's reports are simulated.
The [original service failure](initial-systemd-inventory-failure.json) exposed a missing AF_NETLINK
permission; the corrected unit produced inventory.

The [BIOS definition](created-domain.xml), [UEFI definition](created-domain-uefi.xml) and
[definition verification](definition-verification.json) confirm CPU, memory, disk size, network, ISO
hash, boot order, firmware and no backing file. Starting the guests with local virsh reached the Debian
installer in BIOS and UEFI (captures `guest-boot.png`, `guest-boot-uefi.png`, local only, inspected
by root and the reviewer). This proves bootable provisioning, not installation, power or console.

[Supplemental restart check](agent-reconnect.json) (after a report-only agent fix): identity
preserved. A 4096-byte pathname produced a 4122-byte journal error and a valid 4096-byte wire error,
followed by a fresh heartbeat.

Browser walkthrough: [browser-checks.md](browser-checks.md). Mid-create interruption and tampering are
covered by engine tests only, not by physical-disk or hardware-failure recovery.

## Independent review and CI

Read-only backend and UI/documentation reviewers found issues. Corrections: registration retry after
definite failure, media cleanup, global request-identity conflicts, no protected-appliance adoption,
trusted storage ancestry, exclusive disk/configuration checks on reconciliation, and showing nested config and the
creation task. Both reviewers rechecked with no unresolved findings.

[Scoped documentation check](documentation.json): passed. A concurrent website draft first broke
`check_docs.py --all`, which passed once its owner finished. An isolated snapshot range check passed
93 changed paths.

Both phase1 workflows run Go race tests, discovery, API/UI checks and `managed_vm_browser.py --simulated`.
Native nested-KVM runs are manual. No push, hosted CI, package/ISO rebuild or live upgrade.

[Cleanup record](lab-shutdown.json): Controller processes and copied-host QEMU stopped. Guest
shutdown exceeded 45 s, so QA quit through that instance's private QMP socket. Original labs untouched.
