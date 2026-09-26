# Running snapshot create modes (S3 lab)

This QA records Controller-relayed running snapshot create against the disposable three-host
recovery lab with a locally built Core candidate and the existing replacement Controller on
port 36704. Private host/VM identities stay in lab cache files; this report keeps only the
behavioural outcomes.

## Crash-consistent create (BIOS)

A running BIOS guest accepted `mode=crash` through `POST /api/v1/vms/{id}/snapshots`. The
operation reached terminal `succeeded`. The master's compact catalog listed the new point
under the request ID used as snapshot identity. The guest remained running.

## UEFI refusal

A running UEFI guest with `mode=crash` reached terminal `failed` with the recorded cause that
running snapshots are not supported for UEFI guests in this development slice. No catalog
point was added for that request ID.

## Quiesced without guest agent

An earlier running BIOS guest with `mode=quiesced` and no connected guest agent reached
terminal `failed` with the guest-agent not-connected cause. The failure did not silently
downgrade to crash-consistent capture.

## Quiesced create (BIOS with guest agent)

A disposable managed BIOS guest with a responding guest agent accepted `mode=quiesced`. The
operation reached terminal `succeeded` and the master's catalog listed `s3-quiesced-bios`
under the request ID. The guest remained running after capture.

## Native freeze/thaw control

On a separate raw BIOS cloud guest (not Core-managed), `guest-fsfreeze-freeze` /
`guest-fsfreeze-thaw` and `snapshot-create-as --quiesce --disk-only` completed with
`guest-fsfreeze-status` reporting `thawed` afterward. That platform control is not a Core
API acceptance by itself; it supports the quiesced mode path Core invokes through libvirt.

## Core kill while filesystem frozen

With the managed BIOS guest frozen via the guest agent, Core's main process was killed. The
guest was then thawed through the agent (`guest-fsfreeze-status` returned `thawed`). After
Core restarted and the owner host returned to a fresh connection, a new Controller-relayed
`mode=quiesced` create on the same VM reached terminal `succeeded`. This is a host-owned
thaw plus post-restart quiesced success, not a kill during an in-flight Core quiesced
journal mid-`virsh`.

## Core kill during in-flight quiesced capture

A Controller-relayed `mode=quiesced` create was accepted, then Core was killed on the owner
while `guest-fsfreeze-status` reported `frozen` and a `virsh` snapshot process was present.
After Core restarted, the same operation reached terminal `succeeded`, the catalog listed
`s3-inflight-quiesce`, and the guest reported `thawed`. Exact private receipts stay in the
lab cache.

## Limits

- A running write-workload marker check across revert is not part of this receipt set.
- Browser Snapshots-tab create of a crash point was accepted into the catalog; see
  [browser evidence](2026-09-26-recovery-snapshot-browser.md). Rename/revert/delete from the
  browser and independent review remain open.
- UEFI running capture remains refused; this does not certify that profile.

## Independent review

Not yet performed for this S3 running-mode receipt set.
