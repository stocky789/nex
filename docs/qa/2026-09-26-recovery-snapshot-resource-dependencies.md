# Recovery QA: retained VM snapshot resource dependencies

Date: 2026-09-26. Status: **partial disposable-Core API walkthrough; storage
detach did not isolate the snapshot guard, and the broader resource lease gate
remains unverified.** This is sanitized historical evidence from the local
Core2 disposable fixture; names and identifiers are aliases.

## Candidate and fixture

The selected package-based Debian 13 Core ran the r7 `nex-core-local` runtime
candidate, version `0.3.6~recovery.snapshot6`, binary SHA-256
`1435dc55ed49950f5ed9e3987f453752f9fb27b97e8b12159eab316a802d7ee7`. The
installed Debian package remained `0.3.6~recovery.s0`; the candidate was a
lab-only binary replacement. Controller B ran the matching r7 candidate,
binary SHA-256
`69bd3ac73371b8f26b63c4f904411c4b0f6cb5aae19644fef48851596d221af0`.
This was not an ISO-installed Core test.

The walkthrough used one isolated pool master, two local storage registrations,
two isolated VLAN registrations, and a generated nonbootable marker ISO. It
created one stopped BIOS VM with one 2 GiB disk. Root access was locked before
and after the authenticated local API calls. No guest OS was booted.

## Observed operations

The runner created a VM disk on storage A, wrote and read a `0x6a` marker, and
captured a snapshot through the Core API. The saved snapshot descriptor
contained storage A, network A, and the imported ISO as dependencies. The
runner's exact same-request retry assertion passed, and a same-operation retry
with a changed name passed its expected HTTP 409 assertion. The raw 409 response
was not separately serialized, so the assertion result is supported by the
runner advancing to later steps, not by a retained response body.

The VM then ejected the ISO and changed its current disk and network to storage
B and network B. Its current definition reached revision 2 and remained shut
off. Removal of the old network and ISO each failed with the exact retained
snapshot dependency error. The runner stopped at the storage detach assertion:
the operation failed, but its error cited the current VM, imported ISO, and
retained disk together. That mixed-use refusal does not establish that the
snapshot dependency alone blocked detach.

The original runner therefore ended `failed-preserved`; this was not a passing
end-to-end run. A separate continuation made no VM or snapshot changes and did
not retry any removal. It submitted the ordinary recheck for storage A once,
and the request succeeded while the registration remained present at
generation 1. The master
readback still reported the VM fresh and shut off, at revision 2 with storage B,
network B, and no media; the retained snapshot descriptor still named storage
A, network A, and the ISO.

Native read-only inspection confirmed that the snapshot's saved disk source is
the original storage A disk and that its `0x6a` marker still reads correctly.
The current storage B disk is a separate newly allocated replacement, and the
same `0x6a` marker does not match there. A separate 4096-byte read receipt did
not retain the command or expected pattern, so it does not establish a blank
value. No native snapshot operation, replay, cleanup, or retry was performed
during this read-only continuation.

## Evidence and limits

The ignored local evidence was inspected from separate files for the runner
progress, the master/resource/VM readback, the native snapshot XML and disk
reads, and the one storage recheck request and result. Their SHA-256 digests,
in that order, are:

- `3c658414b5be08a4053848b40ee168e1c6afdb0c174c6e3c12241f7cc2070e5c`
- `b77a5c9de75b6612485a924efc5a4763449eb31339cf90da33d7f667378041d3`
- `c72bfee9e407243b9697263f1174ee1e7015570decbe6a6c498e6bef20eb44d1`
- `d945f8daa253737bc29cecd52a1c102e9ac1c727c66fdca2b94b91773d55e7c1`

This evidence supports historical dependency capture and the specific
network/ISO removal refusals on one package-based Core candidate. It does not
prove a snapshot-only storage detach refusal, a storage lease or reservation
workflow, multi-host replication, guest boot, or ISO-install behavior. No
additional resource removal or VM mutation was attempted after the mixed-use
detach failure; the single ordinary recheck is recorded above.
