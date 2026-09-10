# Full VM creation system

Date: 2026-09-10
Status: Authorized by Matt on 2026-09-10; implementation in progress.
Parent: [platform plan](2026-09-08-nex-platform.md) [Phase 2](2026-09-08-nex-platform.md#2-one-host-vm-management),
items 3, 4, 6 and 7, and the Phase 2 acceptance gate.

## Scope and decisions

Matt redirected the remaining Phase 2 work: build the **complete VM creation system before
stopped-VM editing**, so that edit is written once against the final VM model instead of being
built against the single-disk, single-NIC shape the
[first managed VM slice](2026-09-09-first-managed-vm.md) delivered and then reworked.

Reference products reviewed for the field set: Xen Orchestra with XCP-ng (the form Matt supplied),
Proxmox VE's creation wizard, and the vSphere New Virtual Machine wizard. Research is inspiration,
not authorization to expand the milestone; each item below is either in this slice or listed under
follow-ups with the reason.

### In this slice

| Area | Fields |
|---|---|
| Identity | Name, description, guest OS hint |
| CPU | vCPU count, topology (sockets × cores × threads), CPU model |
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
  Decision: Matt, 2026-09-10.
- **CPU model defaults to `host-passthrough`**, unchanged from today, with `host-model` selectable.
  `host-model` is what makes Phase 7 live migration between non-identical hosts possible, so the
  field exists now even though the default does not change. Named model lists are not offered.
- **Creation still never starts a guest.** `StartAfterCreate` is satisfied by the Controller
  issuing the existing, proven `vm-power` `start` request after creation succeeds — not by adding
  a start path to the managed create engine. This preserves the engine's stated invariant
  (`internal/vmhost/managed.go`) and shows the operator two honest tasks rather than one.
  `Autostart` is a libvirt property (`virsh autostart`), not a start, and stays in the engine.
- **Both firmware paths remain supported.** UEFI is the default; BIOS is selectable. The existing
  UEFI preflight against `domcapabilities` carries over unchanged.

## Memory model

Two modes, matching the two behaviours Matt specified.

| Mode | Operator meaning | libvirt |
|---|---|---|
| `dedicated` | The VM always has this much; Nex counts all of it against host capacity and never hands it to another VM. | `<memory>N</memory>`, `<currentMemory>N</currentMemory>`, `<memballoon model='none'/>` |
| `maximum` | The VM may use up to this much but reserves nothing; it consumes what it touches and returns what it frees. | `<memory>N</memory>`, `<memballoon model='virtio' freePageReporting='on'/>` |

Mechanism, because it is easy to ship a field that does nothing: QEMU does not claim guest RAM up
front — the host populates pages as the guest touches them. What guests do *not* do on their own is
give pages back, because Linux fills spare RAM with page cache; a guest with an 8 GiB ceiling
therefore drifts to 8 GiB of real host memory and stays there. The virtio balloon's **free page
reporting** is what returns them: the guest kernel continuously reports free pages and the host
reclaims them, with no daemon and no polling. libvirt has supported the attribute since 6.9.0 and
Nex Core ships libvirt 11.

**Limits to state honestly and not design around:**

- Reclaim requires the balloon driver *running in the guest*. Linux has it built in; Windows needs
  the virtio-win guest tools. A guest without it behaves like a dedicated VM — it drifts to the
  ceiling and holds. Per-VM guest tools are already observed
  ([console features](2026-09-09-console-features.md)), so a per-VM reclaim-availability indicator
  is possible; **it is not built in this slice** and is recorded as a follow-up. The wizard says
  reclaim depends on guest tools; nothing yet reports whether a given VM actually has them working.
- There is **no pressure-driven balloon daemon** in this slice. `maximum` means the ceiling exists
  and free pages return; it does not mean Nex actively grows and shrinks guests under host pressure.
- Host capacity preflight uses the **ceiling** in both modes. A VM that cannot reach its maximum
  must not pass preflight. `dedicated` is additionally admission-controlled: Controller sums the
  memory already promised by this host's non-failed dedicated VMs and refuses a request that would
  exceed host RAM less a 2048 MiB host reserve (`hostMemory` in `internal/controller/managed.go`).
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

    CPU     VMCPU             // Count, Sockets, Cores, Threads, Model
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
- [ ] Journal round-trip: the full specification and per-disk paths persist on the pool master and a
      replaced Controller re-adopts the VM with identity, name and shape intact
      (`tests/integration/controller_replacement.py`).
      **Not run**: updated for the new shape but never executed; no libvirt host available.
- [x] OpenAPI schemas match the protocol structs; generated reference current (contract tests).
      `python3 -m unittest tests.test_api_contract` 21 tests OK; `generate_api.py --check` exit 0.
- [ ] Browser walkthrough of all six steps including validation, empty, stale, failed and
      interrupted states, narrow-screen and keyboard operation; captures inspected.
      **Not run**: no Playwright environment; no captures exist. `managed_vm_browser.py` was updated
      to drive the six steps and runs in both workflows, but was not executed here, and
      `docs/qa/managed-vm/browser-simulated-results.json` is evidence for the old flat flow only.
- [ ] **Phase 2 acceptance gate**, folded into this slice by Matt's decision on 2026-09-10:
      create a VM through Nex Controller, install a real guest from an uploaded ISO using the
      browser console, reboot it, and verify written data survives guest and host reboot; stop the
      Controller and confirm the VM keeps running, then confirm reconnect refreshes inventory and
      resolves unfinished tasks.
      **Not run**: no guest installed, no reboot data check, no Controller-outage or reconnect test.
      Needs a disposable nested-KVM lab; it was not part of this implementation batch.
- [ ] Guide, API reference and scoped change assessment updated; documentation checker and both CI
      workflows run the new checks.
      Written, and `check_docs.py --all` passes, but the range mode both workflows actually run
      **fails** three ways: `docs/guide/README.md` and `docs/qa/vm-creation/checks.md` are in no
      record's `paths`, and the edited `docs/changes/2026-09-10-controller-console-login.json` names
      a `guide.pages` entry this range does not touch. Neither workflow runs `go vet ./...` or
      `git diff --check`.
- [ ] Independent implementation and documentation review; findings resolved and corrections verified.
      The QA pass above is not independent review.

## Follow-ups

Stopped-VM editing and deletion with disk retention (the remaining Phase 2 work, and the upgrade
path for VMs created before this slice); templates, clones and cloud-init; bulk creation; CPU weight
and cap; pressure-driven memory reclaim; named CPU model lists for migration compatibility.

Deferred from this slice during review, both recorded rather than silently dropped:

- **In-wizard oversubscription warning** when the sum of `maximum` ceilings on a host exceeds its
  RAM. The wizard already holds the VM list, so this is client-side work; the authoritative refusal
  (dedicated admission control) is server-side and is implemented.
- **Per-VM reclaim-availability indicator** in the VM detail and Guest tools panel, showing whether
  a `maximum` VM's balloon driver is actually running rather than assuming it.

Also outstanding, from the same review: `docs/changes/2026-09-10-controller-console-login.json` and
`2026-09-10-default-iso-storage.json` record `api.impact: "existing contract"`, which
`scripts/check_docs.py:120` does not accept — it allows only `updated` or `none`, conflicting with
the `none | existing contract | changed contract | new contract` vocabulary in `AGENTS.md`. Those
records predate this slice and correcting them requires touching their own guide pages, so the
conflict is reported here for its owners rather than patched from this slice.
