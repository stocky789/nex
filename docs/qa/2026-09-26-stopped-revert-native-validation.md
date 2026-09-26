# Recovery QA: stopped revert native validation

Date: 2026-09-26. Status: **first native walkthrough failed (BUG-0647); corrected
observe-only recovery passed on the same preserved operation**. Sanitized historical
evidence from a disposable local lab. Resource identities and private paths are omitted.

## Candidate and method

Three Debian 13 Core hosts and Controller B initially ran the matched r9 candidate,
`0.3.6~recovery.snapshot8`. The installed Core package remained
`0.3.6~recovery.s0`; only the matched runtime and source were replaced.
Core binary SHA-256 (r9):
`089052254a70c60bff423a2eaebae3204bce521a4aeb01128d3e55afb2f1d581`.
Controller binary SHA-256:
`5284699969d872b528105c9709e6dde60468298c02c0ab955a85f922988b51a3`.
Native stack: libvirt `11.3.0-3+deb13u3+nex3`, QEMU 10.0.13, swtpm 0.7.1, OVMF 2025.02.

The owner was a pool member with a separate master. Its guest used two writable
disks, UEFI, Secure Boot, a TPM 2.0 emulator, one attached read-only ISO and an
empty CD-ROM.

Through authenticated APIs, the walkthrough stopped the guest and captured
point A. It then booted the guest, changed both disk markers, added EFI/TPM
state, stopped it, and changed its name, CPU topology and maximum memory.
The corrected definition update succeeded at revision 2. One stopped revert
to A was then submitted through the owner-local Core API, with a wrapper
counting only that exact native revert and refusing a duplicate.

## Observed failure (r9)

The native revert ran once. Both fresh writable heads backed onto A's original
disk layers. The inactive definition retained the current name and restored
A's CPU and memory, but Core returned `needs_attention` for definition and
native metadata mismatches. The journal remained at
`snapshot-revert-native`, with its recovery reservation retained.

Libvirt rewrote A's external snapshot metadata to the fresh heads. It also
created a qcow2 head for the read-only ISO and changed the CD-ROM source and
driver while retaining its read-only property. Core's validators expected
the original capture metadata and original ISO source.

Firmware installation was not reached during the failed attempt. The guest
remained stopped. No successful guest restore was established by that attempt.

## Corrected observe-only recovery

A later Core-only binary candidate (`0.3.6~recovery.snapshot9`, SHA-256
`c87a10fc2494fd9a49b00c9bbf118d51d10ab3ef82f477ff852265649159bbdf`) was
installed on the owner host without changing libvirt (`+nex3` retained). The
exact same operation ID was posted again. Native counter remained `1` with no
duplicate marker. The journal settled `completed`/`succeeded`, the master cleared
`pending_recovery` and advanced the catalog to revision 3, and a subsequent guest
boot reported the point-A disk markers with Secure Boot enabled.

Focused Go regressions for snapshot revert and settled media projection passed.
Deletion, metadata editing, Controller snapshot UI, running capture and Phase 5/6
closeout remain open.
