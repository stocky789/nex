# Full VM creation system

Date: 2026-09-10
Status: Implemented and committed on 2026-09-10 (`4a60e45`, media/adapter follow-up `38aa856`);
independent review round completed with corrections verified. Phase 2 acceptance gate open: on
the final gate run, build 0.2.10~gate5 (lab run 2026-09-17 UTC, disposable nested-KVM lab, virtual
hardware only; [gate report](../qa/2026-09-17-phase2-gate/checks.md)), 15 of 16 checklist rows, COVERAGE-1 and both G7 halves passed,
but row 11 failed on a **major defect open in the gated product**: a network interface added to a
running VM can reuse the MAC of an interface the guest has not yet released, leaving a duplicate MAC
on the bridge while the task reports succeeded ([open defect 1](../qa/2026-09-17-phase2-gate/checks.md#owner-triage-open-defect-1); code
reading indicates released 0.2.9 is also affected, not lab-reproduced). gate5 was the last lab cycle
of this effort, so the defect is not fixed in the gated build; it went to the project owner with a
[triage](../qa/2026-09-17-phase2-gate/checks.md#owner-triage-open-defect-1) and was then fixed in the
uncommitted working tree as BUG-0256 with unit tests against a simulated libvirt only (**not
lab-verified**). The gate4 code-review NIC finding was not reproduced. The
gate stays open until that fix is proven on a new gated build that reruns row 11 and the G7 rows,
and until the project owner's code review and refactor of this work, independent review, the owner
commit, hosted CI on both workflows after the owner pushes and the open owner decisions are resolved. See [acceptance](#acceptance).
Parent: [platform plan](2026-09-08-nex-platform.md) [Phase 2](2026-09-08-nex-platform.md#2-one-host-vm-management),
items 3, 4, 6 and 7, and the Phase 2 acceptance gate.

## Scope and decisions

project owner redirected the remaining Phase 2 work: build the **complete VM creation system before
stopped-VM editing**, so that edit is written once against the final VM model instead of being
built against the single-disk, single-NIC shape the
[first managed VM slice](2026-09-09-first-managed-vm.md) delivered and then reworked.

The field set was reviewed against the creation form project owner supplied and the fields a VM
specification actually needs. Research is inspiration, not authorization to expand the
milestone; each item below is either in this slice or listed under follow-ups with the reason.

### In this slice

| Area | Fields |
|---|---|
| Identity | Name, description, guest OS hint |
| CPU | vCPU count, topology (sockets × cores × threads), fixed native pool baseline or manual CPU model |
| Memory | Mode (dedicated or maximum) and size |
| Disks | Repeatable: storage, name, size, bus, thin/thick, boot order |
| Networks | Repeatable: network, adapter model, automatic or manual MAC |
| Boot and media | Firmware (UEFI/BIOS), machine type, boot order, install ISO |
| Lifecycle | Start after creation, autostart on host boot |

### Deferred, with reasons

| Item | Reason |
|---|---|
| Templates, full clones, cloud-init | Already the "Next" follow-on in the platform plan, whose stated prerequisite is *reliable VM creation* — that is what this slice delivers |
| Multiple VMs (name pattern, first index) | Bulk actions are a separate "Next" follow-on; creation correctness comes first |
| CPU weight and cap (`cputune` shares/quota) | No operator problem demonstrated yet in a single-host pool |
| Affinity host | Requires Phase 4 multi-host pools; there is one host to place on today |
| ACLs, resource sets, sharing | Requires Phase 8 scoped operator/viewer roles |
| vTPM, Secure Boot enrolment | Platform plan lists Windows customisation and vTPM under "Later" |
| Automatic pressure-driven memory reclaim | Needs a host daemon; see the memory limits below. Not claimed by this slice |

### Decisions

- **No compatibility surface.** Nex is unreleased. `protocol.VMCreateSpec` is replaced with the
  shape below; there are no decode-only legacy fields, no `managed-vm-v2` capability gate and no
  journal normalisation. Existing lab VMs and managed journal entries are recreated.
  The Controller appliance is unaffected: it is built from `DeploySpec` through
  `makeDomain` (`internal/vmhost/engine.go`), a separate path from `VMCreateSpec`.
  Decision: project owner, 2026-09-10.
- **CPU default updated for 0.3.1, 2026-09-17:** new drafts use the Core-reported fixed
  pool baseline when available, saving its named model and explicit feature policies in the
  existing Core VM records. Core computes and compares the native definition across all
  current members; unavailable members prevent discovery. Manual dynamically discovered
  models and host modes remain selectable. On a capable host with no baseline, a new draft
  needs an explicit manual choice; older hosts retain the previous manual selection behavior.
  Existing VM definitions never change on discovery or membership refresh. This supersedes
  the original `host-passthrough` default; there are no guest-release CPU rules.
  **API impact: changed contract**, including new authenticated Core peer discovery routes;
  **guide impact: updated** in the creation/edit guides. See the
  [CPU plan](2026-09-17-cpu-baseline.md) and [checks and limits](../qa/2026-09-17-cpu-baseline.md).
  This is implemented in the working tree, not deployed or migration-accepted.
- **Creation still never starts a guest.** `StartAfterCreate` is satisfied by the Controller
  issuing the existing, proven `vm-power` `start` request after creation succeeds — not by adding
  a start path to the managed create engine. This preserves the engine's stated invariant
  (`internal/vmhost/managed.go`) and shows the operator two honest tasks rather than one.
  `Autostart` is a libvirt property (`virsh autostart`), not a start, and stays in the engine.
- **Both firmware paths remain supported.** UEFI is the default; BIOS is selectable. The existing
  UEFI preflight against `domcapabilities` carries over unchanged.

## Memory model

Two modes, matching the two behaviours project owner specified.

| Mode | Operator meaning | libvirt |
|---|---|---|
| `dedicated` | The running VM holds the full amount in physical RAM; a stopped VM retains its Nex capacity commitment. | `<memory>N</memory>`, `<currentMemory>N</currentMemory>`, `<memoryBacking><nosharepages/><locked/><allocation mode="immediate"/></memoryBacking>`, `<memballoon model='none'/>` |
| `maximum` | The VM may use up to this much but reserves nothing; it consumes what it touches and returns what it frees. | `<memory>N</memory>`, `<memballoon model='virtio' freePageReporting='on'/>` |

**Dedicated-memory correction, 2026-09-11:** project owner requested actual physical reservation
in libvirt after observing a 4 GiB dedicated VM consume around 1 GiB. Disabling ballooning alone
had left QEMU allocating on demand. Dedicated now uses immediate allocation, locked memory and
no KSM page sharing, all persisted in Core's libvirt definition. Physical RAM is held while QEMU
runs and released on full shutdown. Maximum remains on-demand with virtio free-page reporting.

[Libvirt memory backing](https://libvirt.org/formatdomain.html#memory-backing) defines these native
controls. Locking also covers QEMU overhead. No guessed `memtune.hard_limit` is introduced:
[libvirt memory tuning](https://libvirt.org/formatdomain.html#memory-tuning) warns that a limit
below variable QEMU requirements can kill the guest. The existing 2 GiB host reserve is a
preflight margin, not a per-process cap or a guarantee against all host memory pressure.
Deployment-specific overhead/limits require measurement; this fix does not add a tuning policy.

Existing definitions need a full shutdown and a stopped **Save changes** before starting.
Core does not rewrite or stop running guests during upgrade. Legacy dedicated live edits fail
before device effects with an instruction to stop and save. Autostart uses the persisted definition;
upgrading the agent or rebooting the guest alone cannot retrofit the reservation.

API impact: **existing contract** — `VMCreateSpec.memory`, `vm-create`, `vm-update`, `vm-power`
and `Inventory.managed.vms` retain their fields, authentication, validation and replay/result
paths. Updated Core enforces the existing dedicated promise; older Core versions do not. Upgrade
Core before relying on it. Guide impact: **updated** with lifecycle and existing-VM instructions.
State owner: **core** — the pool master's managed journal holds the mode/size and generated
libvirt definition; inventory/adoption restores the same VM identity and settings to a fresh
Controller. No Controller reservation table or background memory manager is added.
Verification and remaining acceptance limits: [dedicated-memory QA](../qa/dedicated-memory/checks.md).

**Limits to state honestly and not design around:**

- Reclaim requires the balloon driver *running in the guest*. Linux has it built in; Windows needs
  the virtio-win guest tools. A guest without it can drift to the
  ceiling and hold its touched pages, but its RAM is not preallocated or locked. Per-VM guest tools are already observed
  ([console features](2026-09-09-console-features.md)), so a per-VM reclaim-availability indicator
  is possible; **it is not built in this slice** and is recorded as a follow-up. The wizard says
  reclaim depends on guest tools; nothing yet reports whether a given VM actually has them working.
- There is **no pressure-driven balloon daemon** in this slice. `maximum` means the ceiling exists
  and free pages return; it does not mean Nex actively grows and shrinks guests under host pressure.
- Host capacity preflight uses the **ceiling** in both modes. A VM that cannot reach its maximum
  must not pass preflight. `dedicated` is additionally admission-controlled: Nex Core sums the
  memory already promised by this host's non-failed dedicated VMs and refuses a request that would
  exceed host RAM less a 2048 MiB host reserve (`dedicatedCommitted`/`dedicatedFits` in `internal/vmhost/managed.go`).
  Creation leaves the VM stopped, so observed host memory proves nothing about what is committed —
  the promise is counted from acceptance. `maximum` reserves nothing and may oversubscribe; an
  in-wizard oversubscription warning is a **follow-up, not built in this slice**.

## Contract

`protocol.VMCreateSpec` replaces the current flat shape:

```go
type VMCreateSpec struct {
    ID, PoolID, HostID string
    Name, Description  string
    GuestOS string            // "linux" | "windows" | "other"; recorded for display and guest-tools advice

    CPU     VMCPU             // Count, Sockets, Cores, Threads, Model, Features
    Memory  VMMemory          // Mode "dedicated"|"maximum", SizeMiB
    Disks   []VMDisk          // StorageID, Name, SizeGiB, Bus, Thin, BootOrder
    NICs    []VMNIC           // NetworkID, Model, MAC ("" derives from VM UUID + index)
    Boot    VMBoot            // Firmware "uefi"|"bios", Machine, MediaBootOrder
    MediaID string

    StartAfterCreate bool
    Autostart        bool
}
```

Bounds are validated identically on both sides from shared helpers in `internal/protocol`:
at least one disk and at most 8; at most 8 NICs; `Sockets × Cores × Threads == CPU.Count`;
memory at least 512 MiB; per-disk size 1–2048 GiB; unique boot orders; bus in
`virtio|sata|scsi`; NIC model in `virtio|e1000e`; manual MACs unicast (low bit of the first
octet clear) and unique within the pool.

**API impact: changed contract.** `POST /api/v1/vms` accepts the new specification, the `vm-create`
command payload carries it, and the VM list and report inventory expose the created shape.
Authentication, authorization, CSRF, operation persistence, replay by `request_id` and audit
attribution are the **existing contract** and do not change. Update `docs/API.md`, the OpenAPI
schemas under `docs/api/` and the generated reference in the same change.

**Every field is pool-scoped state.** The durable host-side record is the managed journal on the
pool master, which already persists the full command and generated domain XML and re-adopts by
resource ID (`Inventory.managed`). Journal entries gain the per-disk paths; a replaced Controller
recovers the complete VM definition, identity and name with no re-entry.

**Guide impact: updated.** [Create a virtual machine](../guide/create-virtual-machine.md) covers the
whole wizard, both memory modes and their limits, multiple disks and networks, boot order,
start-after-creation and autostart. [Install guest tools](../guide/guest-tools.md) gains the memory
reclaim dependency.

## Implementation

**Domain XML** (`internal/vmhost/managed.go`): one `<disk>` per entry with its bus and target device
(`vda…`, `sda…`), one `<interface>` per NIC with its model and MAC, per-device `<boot order='N'/>`
in place of the current `<os><boot dev>` list (libvirt forbids mixing the two forms), the memory
elements above, and the existing console devices unchanged.

**Creation** (`ManagedEngine.create`): resolve and free-space check each disk's storage target,
summing per target; keep the exclusive VM directory as the reservation and place disks inside it as
`disk-0.qcow2 …`; `qemu-img create` per disk, thick using `-o preallocation=falloc`; extend the
exclusive-ownership check to every disk; `virsh autostart` when requested. Uniqueness, fail-closed
listing, staged journal writes and the uncertain-outcome wording are unchanged.

**Controller** (`internal/controller/managed.go`): validate the specification, resolve storage,
network and media references, enforce pool/host scope and MAC uniqueness, dispatch, and — only after
creation succeeds — issue the follow-on `vm-power` `start` when `StartAfterCreate` is set.

**UI**: the wizard moves out of `web/src/managed.tsx` into a new `web/src/vm-create.tsx`; repeatable
disk and network rows make the existing file too large to hold both. Six steps: Identity →
CPU and memory → Disks → Networks → Boot and media → Review.

## Acceptance

Evidence for every ticked box: [QA checks](../qa/vm-creation/checks.md), run 2026-09-10 on the
uncommitted working tree. Unticked boxes were **not run**; none of them may be called verified.

- [x] Specification, bounds and topology validated identically on both sides (Go tests).
      One shared `protocol.ValidateVMSpec` called from `internal/controller/managed.go:630` and
      `internal/vmhost/managed.go:230`; `go test -race -count=1 ./...` exit 0.
- [x] Domain XML: multiple disks and NICs, per-device boot order, both memory modes and their
      balloon devices, both firmware paths, manual and derived MACs (Go tests).
- [x] Creation allocates every disk with correct provisioning, enforces exclusive ownership per
      disk, checks free space per storage target and sets autostart (Go tests). Unit level: the
      tests pin the `qemu-img` argv and the per-target plan, not a real allocation on a host.
- [x] Capacity preflight uses the memory ceiling in both modes and refuses what cannot be met.
- [x] Controller rejects unauthorized, wrong-host/pool, stale-resource, invalid-input, duplicate-MAC,
      over-capacity and replayed requests; `StartAfterCreate` dispatches the existing `vm-power`
      start only after creation succeeds (Go tests).
- [x] Journal round-trip: the full specification and per-disk paths persist on the pool master and a
      replaced Controller re-adopts the VM with identity, name and shape intact
      (`tests/integration/controller_replacement.py`).
      **Passed locally (gate5, 2026-09-17 UTC)**: the repository `controller_replacement_debian.sh`
      with `--vm` passed both phases, and row 12 re-adopted host-1's VMs with the same IDs, names,
      specs, revisions, inactive XML and journal set using the gate5 package binary
      ([gate report](../qa/2026-09-17-phase2-gate/checks.md#g6-in-detail), rows 12 and G7). Limits: the script built from the shared
      working tree, not the gate5 source snapshot, and hosted CI has not run it.
- [x] OpenAPI schemas match the protocol structs; generated reference current (contract tests).
      `python3 -m unittest tests.test_api_contract` 21 tests OK; `generate_api.py --check` exit 0.
- [ ] Browser walkthrough of all six steps including validation, empty, stale, failed and
      interrupted states, narrow-screen and keyboard operation; captures inspected.
      **Open**: the gate5 lab walked all six steps in headless Chromium at desktop and keyboard-driven
      at 360 px, with unauthorized, failed, stale and offline states captured ([gate report](../qa/2026-09-17-phase2-gate/checks.md),
      rows 4 and 13). BUG-0248 stays fixed, but the queued-create page still shows the VM name,
      unknown state and tabs above the Creation panel, and shows **Creation dispatched** for up to
      10 s after the host refused ([open defect 3](../qa/2026-09-17-phase2-gate/checks.md#open-defects-after-gate5)). It stays open until
      that is resolved and the simulated `phase1` browser jobs are green in hosted CI after the owner
      pushes.
- [ ] **Phase 2 acceptance gate**, folded into this slice by project owner's decision on 2026-09-10:
      create a VM through Nex Controller, install a real guest from an uploaded ISO using the
      browser console, reboot it, and verify written data survives guest and host reboot; stop the
      Controller and confirm the VM keeps running, then confirm reconnect refreshes inventory and
      resolves unfinished tasks.
      **Functional clauses passed on virtual hardware, gate open** (final gate run, build
      0.2.10~gate5, 2026-09-17 UTC, [gate report](../qa/2026-09-17-phase2-gate/checks.md)): create and Debian install from an uploaded
      ISO through the browser console (rows 3, 4, 6); marker data unchanged across a guest reboot and
      a host reboot (rows 8, 14); the VM kept running through a 107 s Controller outage (row 9);
      reconnect refreshed inventory and delivered the queued task once (row 10). Not ticked:
      - **major open defect 1** (row 11 failed: a network interface hot-plugged into a running VM can
        reuse the MAC of an interface the guest has not released, while the task reports succeeded;
        [open defect 1](../qa/2026-09-17-phase2-gate/checks.md#owner-triage-open-defect-1), [owner triage](../qa/2026-09-17-phase2-gate/checks.md#owner-triage-open-defect-1))
        is fixed in the working tree as BUG-0256 with unit regression tests only, not lab-verified; it
        needs a new gated build and a rerun of at least row 11 (including the running-VM remove, add
        and add-again sequence, probe c2) and the G7 provenance rows. The cross-VM manual-MAC variant
        and a pending-unplug indicator remain open;
      - the report has had no independent review, and the required slice reviews are outstanding;
      - hosted CI has not run on either forge;
      - the tree is not committed and awaits the project owner's code review and refactor; the
        BUG-0256 fix changed product files after the gate5 build, so the committed product paths no
        longer match the gate5 product-path fingerprint and need the new gated build;
      - G6 is partial: same-host appliance redeploy is blocked by the product and backup restore was
        not exercised, and these and the other [owner decisions](../qa/2026-09-17-phase2-gate/checks.md#open-owner-decisions) are open.
      See the [remaining requirements](../qa/2026-09-17-phase2-gate/checks.md#remaining-gate-requirements-outside-this-lab). No physical
      hardware was tested.
- [ ] Guide, API reference and scoped change assessment updated; documentation checker and both CI
      workflows run the new checks.
      `documentation.yml` is green on the committed slice (Gitea runs 3799, 3802) and
      `check_docs.py --all` passes at HEAD; the earlier range-mode failures were fixed in the
      review round and by `8103af4`. Neither workflow runs `go vet ./...` or `git diff --check`.
      **Open (gate5, DOC-1 partial)**: install-nex-core.md step 8 contradicts the installer step list
      that loses its current step for up to about 24 s ([open defect 2](../qa/2026-09-17-phase2-gate/checks.md#open-defects-after-gate5));
      vm-console.md step 2 overstates how soon the console attaches (open defect 4); the edit guide
      did not cover MAC reuse across saves (open defect 1) or the single-interface replacement 400
      (open defect 5), now added to its Troubleshooting without a new walkthrough; and hosted CI has
      not run on the gated tree.
- [x] Independent implementation and documentation review; findings resolved and corrections verified.
      See the [review round](../qa/vm-creation/checks.md#independent-review-round--2026-09-10):
      dedicated-memory admission control was the material defect, fixed with a mutation-checked
      regression test.

## Follow-ups

Stopped-VM editing and deletion with disk retention (the remaining Phase 2 work, and the upgrade
path for VMs created before this slice; both since implemented, see the [VM edit plan](2026-09-10-vm-edit.md)
and the [delete guide](../guide/delete-virtual-machine.md), and covered by the gate run above); templates, clones and cloud-init; bulk creation; CPU weight
and cap; pressure-driven memory reclaim.

Deferred from this slice during review, both recorded rather than silently dropped:

- **In-wizard oversubscription warning** when the sum of `maximum` ceilings on a host exceeds its
  RAM. The wizard already holds the VM list, so this is client-side work; the authoritative refusal
  (dedicated admission control) is server-side and is implemented.
- **Per-VM reclaim-availability indicator** in the VM detail and Guest tools panel, showing whether
  a `maximum` VM's balloon driver is actually running rather than assuming it.


### Normal VM display names — 2026-09-11

Accept normal names with spaces, case, dots, punctuation and Unicode when creating or editing
managed VMs. Keep a nonblank 200 UTF-8 byte limit and reject slash, control characters and
XML-invalid U+FFFE/U+FFFF. The existing Core managed-journal VMCreateSpec.name and libvirt
name are the durable record; Inventory.managed.VMs restores the exact name to a fresh Controller.
UUID identity and disk paths are unchanged; existing disk-label sanitization is reused.

API impact: changed contract — POST /api/v1/vms, PUT /api/v1/vms/{id}, vm-create/vm-update
and managed inventory adoption use the same expanded name validation. Payloads, authentication,
authorization, asynchronous results and replay behavior are unchanged. Upgrade Core and
Controller together; older versions reject the broader syntax. Guide impact: updated.
Verification and limits: [display-name checks](../qa/vm-display-names/checks.md).
