# Native pool CPU baseline

Date: 2026-09-17  
Status: Published in the 0.3.1 website/APT release; feature acceptance evidence and limits are recorded in
[the scoped QA report](../qa/2026-09-17-cpu-baseline.md). No migration acceptance is claimed. Publication checks are recorded in the
[release report](../qa/2026-09-17-nex-core-0.3.1-release.md).

## Scope and decision

CPU discovery and compatibility are independent of the guest operating system. There are no
new OS profiles, per-OS CPU requirements or CPU-name allowlists. Core discovers host-usable
models dynamically and offers all of them for manual selection, including `qemu64`.

For the default, use the installed platform's native baseline calculation across every
current pool member. Selecting that observation freezes its named model, explicit native
features and machine type in the VM; future observations never rewrite an existing guest.
The baseline is not a minimum OS requirement and is not inferred from model-name order.

## Core behavior and durable state

The pool master gathers native guest CPU XML from every non-ejected member, using libvirt
host-model capabilities and their explicit overrides. All members must be ready, have known
peer endpoints and respond under the same membership generation. Discovery supports at most
64 membership records and refuses mixed CPU vendors. It computes the common definition with
`hypervisor-cpu-baseline --features --migratable`, then verifies the exact result against every
member's runtime using `hypervisor-cpu-compare --error`. A membership change invalidates the
observation. A single-host pool uses the same flow.

Discovery is bounded, read-only and safe to retry; an unavailable member never becomes an
implicitly smaller pool. The observation cache is disposable and belongs to Core. Selecting
a baseline persists its model and features in the existing managed journal and master
`pool-vms` record `Spec`. Fresh Controller adoption recovers this definition through existing
inventory. **State owner: core**; there is no new Controller-owned policy or resource identity.

Optional `VMCPU.features` contains at most 512 unique native feature names with `require` or
`disable` policies. Nonempty features require a named model; host modes and `max` cannot carry
them. Omitting the field preserves the previous model-only semantics. Core compares explicit
CPU definitions against the actual VM machine before creation, stopped edits and
Core-mediated starts. CPU changes require a stop. Native libvirt autostart does not execute
this Nex preflight.

## API and operator delivery

**API impact: changed contract and new peer contract.** Add `vm-cpu-baseline-v1`, optional
`VMCPU.features`, and optional `Inventory.cpu_baseline` containing `model`, `features`,
`machine`, `generation`, `member_count`, `observed_at` and optional `error`. New Core peer
routes expose CPU capabilities, comparison and baseline observation under existing pool
certificate authorization. Machine and membership generation are explicit. Comparison JSON
is bounded to 128 KiB; each request has a 30-second deadline. Authentication failures,
malformed input, wrong methods and incompatible/changed observations have explicit errors.

Controller submits the resolved definition and presents Core's report; it computes no CPU
intersection. Nonempty feature requests require the capability after durable request replay;
older Core is refused with upgrade guidance. Upgrade Controller first, then every Core.
Existing operation authentication, authorization, replay and uncertain-result behavior remain.

**Guide impact: updated.** New drafts default to the verified fixed pool baseline when
available. If new Core cannot establish one, the operator must explicitly choose a CPU model
or restore member availability. Manual choices remain available. Existing saved CPU definitions
are preserved in the editor, including while discovery is unavailable. Selecting a manual
model clears the explicit feature list; selecting a baseline copies its current fixed values.

## Acceptance and limits

- Check native capability parsing, feature validation, intersection/comparison, member failure,
  generation changes, request authorization, API replay, durable round trips and startup refusal.
- Exercise browser defaults, manual selection, saved-definition preservation, unavailable
  observations and recovery. Label synthetic peer/CPU evidence as simulated.
- Test native commands against the actual libvirt/QEMU version. Mocked output cannot prove
  native command semantics or hardware support.
- Keep guest OS requirements separate: host/pool CPU compatibility does not prove an OS or
  application will run on the selected CPU.
- Current members only: future hosts can be incompatible with saved VMs. The observation does
  not add migration execution or replace destination/runtime/storage/network validation.
- Controller outage does not stop workloads. No CPU contract is changed on a running VM;
  existing definitions remain unchanged after membership or discovery changes.
- Native libvirt autostart bypass remains explicit; no claim covers every boot path.

## Native references

- [Libvirt domain capabilities](https://libvirt.org/formatdomaincaps.html)
- [Libvirt hypervisor CPU baseline and comparison](https://www.libvirt.org/manpages/virsh.html#hypervisor-cpu-baseline)
- [QEMU CPU model and migration constraints](https://www.qemu.org/docs/master/system/qemu-cpu-models.html)
