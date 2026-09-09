# First managed VM development checks

Date: 2026-09-09. Status: bounded development slice passed native/browser checks and independent review.

## Scope

The [authorized slice](../../plans/2026-09-09-first-managed-vm.md) adds pool-owned
host-local storage, existing guest-network registration, bounded ISO transfer and
creation of a persistent stopped VM. General central power controls, browser
consoles, guest installation, editing/deletion and full Phase 2 acceptance remain
separate work. Website/release changes in the shared checkout are not this slice.

API impact: **new contract** — browser requests flow through authenticated
Controller operations, scoped outbound Core commands/media retrieval, local
libvirt execution and durable results/observations. The [API reference](../../API.md)
and machine-readable schemas describe the implemented boundary and version
compatibility. Guide impact: **updated** in the
[creation workflow](../../guide/create-virtual-machine.md) and guide navigation.

## Executed root checks

- `.cache/toolchains/go/bin/go test -race -count=1 ./...` passed every Go
  package on the final backend: agent 2.504s, Controller 36.728s, localcore 10.045s,
  vmhost 1.341s and websecurity 1.010s; command/protocol packages compiled without
  separate tests. This includes the final storage-traversal and bounded-error fixes.
- `python3 -m unittest discover -s tests -p 'test_*.py'` passed 62 checks.
- `npm --prefix web run typecheck` and `npm --prefix web run build` passed.
  The final browser results below record the exact tested UI bundle digest.
- `python3 scripts/generate_api.py --check` passed central/local references;
  the API owner separately passed all 18 contract checks after the schema changes.
- `GO=/home/matt/Projects/Nex/.cache/toolchains/go/bin/go .cache/phase1-qa-venv/bin/python
  tests/integration/phase1.py --browser` passed all 16 existing process/browser
  result groups, including enrollment, independent probe failure/stall, restart,
  stale status, credentials, authorization, mobile navigation and revocation.
  Its libvirt behavior is simulated, as in the existing suite. The initial attempt
  using the workstation Go package stopped before acceptance because its GOROOT
  lacked `LICENSE`; rerunning with the documented retained toolchain passed.
- Source-retention checks passed after adding the new integration harnesses and
  their shared helper to the existing source archive allowlist.
- `git diff --check` and `python3 scripts/check_docs.py --all` passed at the
  final source-check point. This structural check does not establish committed-range
  coverage or prove walkthrough execution.

## Native and browser evidence

Executed lab preparation and final provisioning commands (the retained source
lab was copied; only the newly created destination was operated):

```sh
python3 tests/integration/managed_vm_lab.py --copy-from .cache/nex-appliance-lab.2851um6a
GO="$PWD/.cache/toolchains/go/bin/go" python3 tests/integration/managed_vm.py --lab-dir .cache/nex-managed-vm-lab.sp09c20p --iso .cache/nex-iso/debian-13.6.0-amd64-netinst.iso --evidence docs/qa/managed-vm --keep-running
```

The [native results](results.json) record real Controller → enrolled agent → libvirt
execution on a private copied Debian 13 host with nested KVM, loopback-only QEMU
forwarding and disposable regular-file disks. The agent ran through the packaged
systemd unit with only disposable binary/state paths and timing overrides. Results
retain source/UI input and artifact hashes and report no input changes during the run.

Existing local directory and network verification, invalid/missing resources,
cross-pool placement, unsupported-agent capability and wrong-host credential checks,
invalid/interrupted/successful ISO transfer, capacity rejection, stopped definitions,
request replay, Controller outage and agent/Controller restart all passed. A secondary
mTLS identity's capability/inventory reports are explicitly simulated; the primary
storage/media/VM flow is native. The [original service failure](initial-systemd-inventory-failure.json)
exposed AF_NETLINK omission; the actual corrected unit produced usable inventory.

The [BIOS definition](created-domain.xml), [UEFI definition](created-domain-uefi.xml)
and [live definition verification](definition-verification.json) confirm exact CPU,
memory, disk virtual size, network, ISO hash, boot order, firmware and absence of a
backing file. Explicitly starting only the new disposable guests through local virsh
reached the Debian installer menus in [BIOS](guest-boot.png) and [UEFI](guest-boot-uefi.png).
Root and independent reviewers inspected both captures. This proves bootable
provisioning, not guest installation or central power/console functionality.

A final report-only agent correction followed the native provisioning run. The
[supplemental restart check](agent-reconnect.json) records its new binary/source
hashes and preserves enrolled identity. A real 4096-byte pathname produced a
4122-byte private journal error, a valid 4096-byte wire error, an accepted failed
operation and later fresh heartbeat. Full local diagnostics remain available.

The [native browser results](browser-native-results.json) and
[portable simulated-host results](browser-simulated-results.json) both passed using
real HTTPS Controller and Chromium. They exercise sign-in, resource/media workflows,
four-stage VM creation, selected settings and creation task, lost response followed
by page reload and restored request identity, one resulting stopped VM, desktop and
360px layouts, keyboard controls, stale/error presentation and recovery. Native
storage and network registration encountered resources from preceding successful
attempts and reused their verified rows after 409; those reuses are not new
successful registrations in the final pass. Browser failure/stale/response-loss presentations
are injected and labeled; native Controller outage is a separate process check.
Root inspected native desktop VM details and narrow-screen review, and the independent
UI reviewer inspected additional success/failure captures. The
[isolated original-shape regression](browser-regression-results.json) fails missing
VM configuration against the old flat-field conditional and passes the real current
bundle. It is a controlled reproduction, not a historical committed checkout.

Mid-create interruption and tampered ownership/configuration are exercised by
focused engine tests. Native replay/restart checks must be distinguished from
those controlled failure injections; neither establishes arbitrary physical-disk
or hardware-failure recovery.

## Independent review and CI

Separate read-only reviewers inspect backend safety/contracts and UI/documentation
accuracy. Corrections so far include registration retry after definite failure,
media cleanup after results without blocking enrollment/reporting, global request
identity conflicts, preventing protected-appliance adoption of an existing managed
guest, trusted/traversable storage ancestry, exclusive-disk/configuration checks
on reconciliation, and showing nested VM configuration and the creation task.
Both reviewers rechecked the corrections and actual evidence with no unresolved
scoped findings. The final browser fixture uses neutral enrolled host names, and
root inspected the recaptured narrow VM details. Both final browser reports match
the current built UI SHA-256; the [browser report](browser-checks.md) records the
exact methods and reuse limits. No image or DOM alteration concealed host identity.

The [final scoped documentation check](documentation.json) passes assessment
coverage, existing references and the modified guide pages' structure/navigation.
The combined CLI gate initially hit a concurrent website draft referencing a QA
file before its owner wrote it; an isolated snapshot reproduced that error.
After the website owner completed the file, root and the independent reviewer both
ran `python3 scripts/check_docs.py --all` successfully. No unrelated file was
stubbed or removed to obtain a pass. This structural result is distinct from
changed-file coverage and actual walkthrough evidence. The final clean isolated
Git snapshot then passed the ordinary range checker for all **93 changed paths**
covered by the managed-VM and logo assessments. The documentation JSON retains its
base/head identities and method: concurrent unrelated work was the baseline,
not part of this scoped change. No shared-checkout commit or push was made.

Both GitHub and Gitea phase1 workflows run Go race tests, Python discovery, API/UI
checks and `managed_vm_browser.py --simulated` on pushes and pull requests. The
browser mode uses a real Controller and authenticated simulated host; it does not
claim native storage correctness. Native nested-KVM execution remains a separately
invoked disposable-lab check. No push, hosted CI run, package/ISO rebuild or live
host upgrade was performed for this slice.

The [cleanup record](lab-shutdown.json) confirms both private Controller processes
and the new copied-host QEMU stopped. The orderly guest shutdown did not finish
within 45 seconds, so QA used only that verified instance's private QMP socket to
quit; original labs and retained source images were untouched.
