# Automatic VM disk and network I/O defaults

Date: 2026-09-24. Status: implemented; native lifecycle verified; production performance acceptance open.
Parent: [platform plan](2026-09-08-nex-platform.md).

## Scope and decisions

The project owner authorized automatic Core disk IOThreads and VirtIO network multiqueue,
with no frontend settings or features. The platform is not in production; apply the defaults
at creation and on ordinary stopped VM saves. Preserve attached running devices during live
edits; newly attached devices receive the defaults. No forced restart or background conversion.

- Use one shared IOThread, ID 1, for supported VirtIO disks and the VirtIO SCSI controller.
  SATA disks and CD drives remain unthreaded. Add a thread/controller before a hotplugged disk
  references it; observe live and persistent state separately after partial failures.
- Offer up to four queues per VirtIO NIC, bounded by the VM's vCPU count. Single-vCPU guests
  retain the ordinary single-queue configuration. Other NIC models are unchanged. Preserve
  libvirt's backend selection and do not change guest operating-system settings.
- Retain existing device assignments during live updates. Journal expected XML before effects
  and verify the resulting thread/queue configuration. Interrupted effects are observed before
  retry; an incomplete operation must not be reported as completed.
- Cache policy, discard, disk formats, CPU placement, host networking and storage mount options
  are outside this slice. Do not add a tuning daemon or user-facing configuration fields.

## API, state and guide impact

**API impact: existing contract.** The flow remains authenticated client VM create/update →
Controller authorization and existing command → Core validation, journal and libvirt → existing
operation results and managed inventory. Request/response schemas, permissions and resource
identities are unchanged. Older Core keeps its prior defaults; old journal XML remains readable.
Update [the API reference](../API.md) with the new Core behavior.

**State owner: core.** The pool master's existing `pool-vms` specification owns VM identity,
placement and requested hardware. The assigned Core's managed journal `Domain` and persistent
libvirt definition retain the exact applied thread/queue layout. Controller replacement adopts
the existing VM through managed inventory without rebuilding its devices or asking for settings.

**Guide impact: updated.** Explain automatic defaults, stopped-save adoption and the distinction
between offered queues and queues activated by guest drivers in the existing create/edit guides.

## Acceptance

- Focused Go tests cover generated XML, supported buses/models, queue limits, legacy live edits,
  hotplug order, partial failures, retry and observation mismatches. Existing forge Go jobs run them.
- Disposable native libvirt checks cover actual generated definitions and device changes,
  stopped saves, restart and interruption. Controller disconnection/replacement must preserve
  the journaled configuration. No existing lab or production workload is a test target.
- Compare each change separately and together against the original configuration using repeated
  disk and network workloads. Record throughput, CPU and latency; inspect actual IOThread and
  guest queue use. XML acceptance alone is not evidence of faster workloads.
- Require repeatable benefit for the intended workloads and investigate regressions. Missing
  native or benchmark evidence keeps performance acceptance open; do not invent speed claims.
- Run the applicable Go checks, API-generation check, documentation gate and repository-privacy
  check. Independently review code, documentation and actual evidence.

The four-arm nested lab run completed. IOThread results were promising, but four active network
channels increased receive-side TCP retransmissions substantially. The additional one/two/four
channel diagnostic could not boot its separate guest to networking and produced no samples.
Keep the capped-four policy classified as a development candidate; repeatable benefit and
physical network/storage qualification remain open. No platform acceptance gate is closed.

Results and limits: [validation report](../qa/2026-09-24-automatic-vm-io.md).
