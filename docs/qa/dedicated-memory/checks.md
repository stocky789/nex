# Dedicated physical-memory reservation checks

Date: 2026-09-11. Development working tree. UI fixtures are fictional. Native measurements were
transcribed from disposable-lab output, with paths, identities and keys excluded.

Scope: dedicated VMs get immediate allocation, locked memory, no KSM sharing and no balloon; maximum
stays on demand. Older running dedicated VMs must be stopped and saved before a live edit.
API impact: existing contract. Sources: [libvirt memory backing](https://libvirt.org/formatdomain.html#memory-backing),
[memory tuning](https://libvirt.org/formatdomain.html#memory-tuning) (no speculative hard limit),
[QEMU invocation](https://www.qemu.org/docs/master/system/invocation.html).

## Executed automated checks

- `go test ./internal/vmhost -run 'TestManaged(DedicatedMemoryReservation|LegacyDedicatedLiveEditRequiresStop)$' -count=1`:
  failed before the fix and passed after.
- `go test -race ./internal/vmhost ./internal/agent ./internal/controller ./internal/protocol`: passed
  (privileged opt-in tests skipped).
- `generate_api.py --check`, 21 API contract tests, web typecheck/test/build: passed.
- Both phase1 workflows run the Go regressions. The privileged native test is opt-in and has no hosted run.

## Native libvirt measurements

Private Debian 13.6 cloud-image overlay under nested KVM; kernel `6.12.107+deb13-cloud-amd64`,
libvirt `11.3.0-3+deb13u2`, QEMU `10.0.11`. SHA-256 of `internal/vmhost/managed.go`
`0e93ce60f15a789bae6fac0e303ef06c15b466b94bbbb0e8fe35e279b0dea34a`, `memory_libvirt_test.go`
`a570d880af86803139da7059d7cb3e0363a384e7b522735041412e16587134f0`, test binary
`606aae48272f2da6871ee5e7aaed911be561256cefe80f78a42c122eb94d10a9`.

```sh
go test -c ./internal/vmhost   # copy to a disposable libvirt host, then as root:
NEX_MEMORY_LIBVIRT_TEST=1 ./vmhost.test -test.v -test.run='^TestDedicatedMemoryLibvirt$'
```

The test defines 512 MiB diskless VMs started `--paused`. It checks live backing XML, validates the
inactive XML with the production validator, and reads QEMU memory from `/proc`:

| Configuration | RSS (KiB) | Locked (KiB) | Swap (KiB) |
|---|---:|---:|---:|
| Original dedicated implementation | 44,140 | 0 | 0 |
| Fixed maximum | 44,564 | 0 | 0 |
| Fixed dedicated | 685,992 | 947,648 | 0 |

- The original binary failed on absent backing. The fixed binary passed all four subtests in 3.34 s.
- Stopping a domain released its memory. A QEMU process with 64 MiB RAM and an 8 MiB memlock limit failed with
  `mlockall: Cannot allocate memory`. No host-pressure/OOM test. No leftover domains.
- The fourth subtest ran the real `ManagedEngine` against a simulated pre-fix definition, with backing stripped from the journal and XML.
  `vm-update` rev 1 saved while stopped, kept the UUID and disk, passed validation and booted paused with 1,025,776 KiB locked.

## Browser review

Chromium rendered the real creation, details and edit components with fictional props, including
Maximum and mobile layouts. No page errors or overflow. Inspected captures (local only, gitignored):
`create-memory.png`, `details-mobile.png`, `editor-mobile.png`. Copy/layout only. The editor fixture
has no disks, so its save is disabled, and the existing-VM save workflow is unproven there.

## Limits

The browser save → Controller → Core upgrade was not exercised as one workflow. No live VM, deployment,
physical machine, long memory pressure, managed-save, migration or HA. The failure test covers QEMU,
not a browser failed-start walkthrough. The 2 GiB admission margin is not a measured overhead cap.
Phase 2 acceptance is open.

## Final review and repository gates

Independent review found one UI overclaim for old definitions, since corrected: the UI now says
"configured policy" and shows the upgrade prerequisite. Re-inspected, no further issues. `go test -race ./internal/vmhost`
passed after the native test was added. `check_docs.py --all` passed once, then `--all`/`--worktree` were
blocked by concurrent storage work (section order, missing assessments, a link to a not-yet-created QA
file). Privacy test and `git diff --check` passed. PNG chunks were IHDR/IDAT/IEND only.
