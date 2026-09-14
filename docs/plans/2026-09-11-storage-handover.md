# Storage implementation handover (superseded)

Status: superseded. This paused the 2026-09-11 single-host storage repository work
(NFSv4, local OpenZFS datasets, SMB3, exclusive-host iSCSI) for continuation on
another machine. That continuation has happened, and the pause-time lab no longer exists.

Current owners:

- Scope, design decisions and per-backend gates: [storage repositories plan](2026-09-11-vm-storage-repositories.md).
- Executed single-host results, pause audit and open backend checks: [storage QA](../qa/storage-repositories/checks.md).
- Native corrections from that work (NFS protocol FSID, SMB marker, iSCSI portal
  and session ownership, capacity cleanup): [BUG-0082 through BUG-0090](../BUG_LEDGER.md).
- Operator workflow: [storage guide](../guide/storage-repositories.md).
- Multi-host and production-readiness status: [production-readiness handoff](2026-09-14-production-readiness-handoff.md).

A clone does not carry the ignored lab state (`.cache/`, `dist/`, credentials,
overlays). A Git-only continuation needs a fresh disposable lab with new secrets and IDs.
