# Core local API snapshot acceptance

Date: 2026-09-26. Status: **the first disposable API operation failed before effects;
the preserved second operation later succeeded and was adopted after a Controller restart.
Snapshot acceptance and S0 remain open**.

## Setup and run

An ephemeral Controller ran on loopback with private state and a short-lived TLS
certificate. A selected disposable Core was enrolled through its local API using the
existing root PAM flow. Its root account was returned to the locked state after each
login and at the end of the run. The Controller created a managed BIOS VM through its
API with two sparse 1 GiB disks. Before the snapshot request, the fresh host report
showed the VM in `inventory.vms`, with the `managed-vms` check healthy and fresh. The
VM was shut off, and distinct 4 KiB markers were readable from both disk files.

The package installed on the disposable Core remained `0.3.6~recovery.s0`. For this
lab-only run, the service executable was replaced with the reviewed `0.3.6~recovery.snapshot1`
candidate (SHA-256 `9392fc6d0c9c1468cbb5b664745a8f6574ff38e89391c557da47d224f677e8b2`,
source manifest `1eed10d274acaee60e345fc511c313adc43923137e45ead0bb66da48da4bc00d`). The
original executable was retained in the private ignored lab. After settling past the
systemd wrapper's `execv`, the running process hash matched the candidate. The package
version and prepared VM UUID, shut-off state, XML hash, disk paths, inode numbers, and
sizes remained unchanged across the service restart.

The runner stopped the owned ephemeral Controller, authenticated to the Core, submitted
one stopped-snapshot request, replayed the exact same body and operation ID, and sent a
same-ID request with a changed name. The exact replay returned the same operation ID and
the changed body returned HTTP 409. The operation poll reached Core status `failed` with
`VM storage identity changed during snapshot preflight`. The runner restarted the same
Controller and relocked Core root. It did not send another snapshot request.

## Failure evidence

Read-only inspection of the Core's managed records found that the selected local-storage
registration succeeded and matched the storage ID referenced by both VM disks, but its
persisted typed `identity` was empty (`{}`). The snapshot preflight compares this value
with the current storage observation and refuses the request when the identity does not
match. This is consistent with the terminal error; the product owner is tracking the
Core-side correction separately.

Two private journal summaries disagree because the first projection used Go-style field
names (`AppliedStorage` and `Command`) against JSON whose actual tags are lower snake case
(`applied_storage` and `command`). Its lookups therefore defaulted to `storage_id_matches: false`
and an empty disk-ID list; those values are parser artifacts, not journal contents. The
follow-up projection parsed the actual tagged fields. Its storage-registration and VM-create
file SHA-256 values match those recorded in the first summary, while the correctly decoded
fields show the registration ID equals both VM disk storage IDs and the typed identity is
empty. The lower-snake-case projection is authoritative for those fields; the original
summary remains preserved as a superseded, incorrectly decoded inspection.

After failure, the same VM UUID remained shut off, native `snapshot-list` was empty, both
disk files retained their recorded inode and size, and both marker reads still returned
the full expected 4 KiB. The operation failed before native snapshot metadata or disk
changes were observed. No operation retry, snapshot delete, or cleanup was performed.

The run also corrected three test-runner assumptions before the actual API attempt:
native `virsh domuuid` lookup uses the fixture's exact domain name and verifies the
returned UUID; the selected VM appears in `inventory.vms` while `inventory.managed`
contains pool authority data; and the recovery S0 validator is the existing lab validator
for this fixture prefix. The runner now persists a private, password-free request
envelope before stopping Controller. Its focused unit suite passed after these fixes.

One additional bounded, read-only check after the failure read the selected VM's record
from the pool master's durable `pool-vms.json`. The record was present with `pending_recovery`
and `pending_snapshot` both null and zero snapshot entries. This corroborates the failed
preflight left no master-side snapshot reservation or catalog entry; it does not replace
the failed operation result or claim that snapshot creation succeeded.

## R3 follow-up attempt

After the Core-side identity fix was built and independently reviewed, the same prepared
VM was tested with a new operation ID and the r3 candidate executable. The binary SHA-256
was `de7986e54e439b3a44275d8a4219bb7331cb7313bd69217c5183c666e6d18019` and its source
manifest digest was `08077a288ab4319f210112dd527b13b63be21eed80f34265c7314b27cee16dbe`;
the installed Debian package remained `0.3.6~recovery.s0`. Candidate startup reached the
pinned HTTPS API endpoint, the package version was unchanged, and the prepared VM UUID,
shut-off state, XML hash, disk inodes and sizes were unchanged across replacement.

The request was accepted, its exact replay returned the same operation ID, and a changed
same-ID body returned HTTP 409. The operation then reported `needs_attention` with
`VM disk state differs from both the before and expected snapshot projections`. The Core
journal is at `snapshot-reserved` / recovery stage `reserved`, has an intent, and has no
`observed_after` projection. The master's durable VM record contains the matching
`pending_recovery` and `pending_snapshot` reservation while its settled snapshot list is
still empty. Unlike the r2 attempt, this reservation has not been cleared.

A bounded read-only storage check found the registered local path on ext4 and measured
the current filesystem ID. Recomputing the candidate's local identity digest from that
path and filesystem ID exactly matched the digest stored in the r3 before/expected
projections. This supports that the recorded local identity input still matches, but it
does not explain why the observed disk state was rejected; no root cause is claimed.

Read-only reconciliation found the original VM shut off, no native snapshot metadata or
current snapshot, and both disk markers still fully readable. Each current qcow2 chain
contains only its original base disk; the two operation-named expected overlay paths are
absent. These observations do not settle the Core reservation or prove a successful
snapshot. No retry, revert, delete, or cleanup was performed. The exact request, runner
failure output, journal projection and read-only state are preserved in the ignored
disposable lab. The first journal projection used incorrect field names for disk paths;
the preserved `r3-attempt-journal-projection-v2.json` uses the actual JSON `source` and
`layers` fields and supersedes that projection.

A separate compiled read-only diagnostic was also attempted against this reservation.
Its test-mode `managedVirsh` helper deliberately refused host `virsh` calls, so the
captured diagnostic had empty live XML/state and produced no valid projection comparison.
It does not establish the rejection cause. The diagnostic binary/source hashes and its
private output are retained; its storage inspection substituted a read-only inspect for
the production storage write probe.

After the validator unit fix, the r4 local candidate was installed on the same selected
Core while the Debian package version remained unchanged. The install helper's first
immediate MainPID check saw the service wrapper during its normal `execv`; its failure
receipt was preserved. A bounded read-only follow-up confirmed the active process was
the r4 binary by path and SHA-256, and the selected VM remained stopped with the same
two disk sources. The local TLS endpoint was reachable with its pinned certificate
(unauthenticated request returned 401).

The guarded resume runner first stopped before Controller disconnection or any Core
operation request. Its inventory predicate requires a fresh host connection, a healthy
and fresh `managed-vms` check, and the selected VM. The captured Controller report was
stale with check freshness unknown; the VM was present. The Core master file still
contained the VM and both pending reservation fields. The Core journal showed repeated
HTTP 400 `invalid JSON request` rejections for agent reports. Read-only source comparison
found the old ephemeral Controller decoder rejected newly added nested report fields.
The lab Controller binary and source archive were updated together from the reviewed
matching candidate while preserving its private database, CA, and listener; a fresh report
then included the selected VM. This was a disposable development-version alignment, not
a general Controller compatibility claim.

The exact preserved r3 request was resumed under the r4 Core candidate and reached
`needs_attention` with the same disk-projection message. A read-only reconciliation found
the original VM stopped, no native snapshot, unchanged disk markers, and the unresolved
master reservation. A second validator path had compared memory quantities represented
in different units; the Core-side fix was reviewed against the captured raw XML before
the next candidate was accepted.

The same original request and operation ID were then resumed under the r5 Core candidate
(binary SHA-256 `be91a6de2d275e42836801a430c8c6d24eb557034725f2545f855801e6d2700c`; installed
Debian package remained `0.3.6~recovery.s0`). It returned the existing succeeded operation;
the exact retry returned the same ID and the changed-body request returned HTTP 409. The
master catalog contained exactly one matching entry. Native snapshot metadata existed,
the VM remained shut off, both disk heads pointed to the operation overlays with their
original base disks, and both expected 4 KiB markers passed. While Controller A was
stopped, the Core service restarted with a new PID and the expected executable,
package version, and binary hash. The same exact request replayed successfully after that
restart, and the operation, catalog entry, native snapshot, chains, and markers remained
unchanged. Controller A then restarted, freshly reported the selected host, and adopted
the matching snapshot catalog entry. Core root was locked at completion.

Two harness-only defects were preserved separately: an earlier post-Core-restart poll
treated systemd's transient setup wrapper as a terminal identity mismatch, and the next
attempt's successful remote payload omitted an explicit `status: passed` field, so the
outer runner labeled complete Core evidence as failed. The bounded readiness loop now
waits for the exact candidate process and pinned HTTPS readiness; the remote result now
reports explicit success. A fresh-label rerun of the same request completed the full
acceptance flow. No new operation ID, extra snapshot, revert, delete, or cleanup was used.

## API and recovery boundary

The runs exercised the existing authenticated Core contracts documented in
[`docs/API.md`](../API.md): `POST /api/local/v1/pool/vms/{id}/snapshots`, exact retry,
same-ID conflict, and `GET /api/local/v1/pool/vms/{id}/snapshot-operations/{operation_id}`.
Core handled the exact retries and conflict while Controller was stopped, then the same
Controller restarted and adopted the successful entry. The r2 operation failed before
reservation and its subsequent master-record read showed no pending state. The r3/r4
attempts recorded intermediate validation failures and a needs-attention reservation;
that same operation was settled by the reviewed r5 validator fix and exact replay. These
are actual disposable API results, not simulations or a product-wide support claim.

## Limits and verification

This was a package-based cloud guest, not an ISO install or hardware test. It covers one
local-storage BIOS VM only. It does not establish restore/revert, firmware variants, other
storage backends. The later one-host replacement follow-up is recorded below. The intermediate failed and
needs-attention attempt records remain preserved alongside the successful fresh-label
completion; no stale attempt artifact was overwritten.

Checks run after runner corrections:

- `python3 -m unittest tests.test_core_local_snapshot_api -v` — 14 passed.
- `python3 -m py_compile tests/integration/core_local_snapshot_api.py` — passed.
- The reviewed r5 candidate completed the same preserved r3 request through Core restart,
  exact replay, and fresh Controller adoption. The full private result is retained in the
  ignored lab as `core-local-snapshot-api-r5-resume-final2-result.json`.

The runner accepts a private preserved request envelope for an explicitly selected resume
attempt, keeps each attempt's result/request files separate, waits through the service
wrapper-to-executable transition without relaxing identity checks, and verifies pinned
HTTPS readiness. The successful run exercised Core restart and exact replay while
Controller was offline, followed by fresh Controller adoption. The reservation is settled.

## Fresh Controller replacement follow-up

The same stopped BIOS/local-storage VM was subsequently adopted by a separate, initially
empty Controller B, with its own database and CA. Core retained the r5 executable above;
Controller B used the pinned matching development Controller binary (SHA256
`4713b3218d3fb1ce269fb2b9afb743f5e6687832c1e3445fc439dbf06c634972`). Controller A was
stopped and its files retained. Exactly one enrollment reached Core successfully.

The runner failed after that enrollment while interpreting its earlier marker evidence:
the producer stores a source and readback string, not separate offset/length fields.
A read-only continuation established the outcome without enrolling again. Authenticated
Controller B APIs reported the original host, pool and master identity, the exact snapshot
descriptor including specification and disk-identity digests, an explicitly reported empty
recovery-reservation view, and the succeeded host-scoped snapshot operation. Core remained
active with root locked, its managed snapshot journal succeeded and settled, and the VM
stopped. Both original disk markers passed native reads. The frozen continuation receipt
has SHA256 `f3e09ef17ef90d59659da93affb90cb4a3bfb80ed18ee90161fd2e053a65ebf9`.

This is a successful identity-bound continuation, not a successful uninterrupted invocation
of the original runner. Earlier setup/login failures are preserved separately. The runner's
17 focused tests now cover producer-shaped marker verification, the actual operations API
projection, cookie handling and root relocking. Syntax, privacy and documentation checks
passed. Independent review checked the frozen receipt, distinct Controller certificates,
source and focused tests. The receipt is a curated continuation summary, not a raw
post-enrollment API/SSH transcript; it does not include the journal comparison
before/after hashes or raw marker command output. Preserve that evidence distinction.

Controller A's pre-enrollment file fingerprint was held only in memory and lost when the
runner failed. Its files remain and their current full fingerprints are recorded, but no
byte-for-byte comparison against that missing baseline is claimed. Future runs persist the
baseline before enrollment. This follow-up proves one-host adoption only: it does not close
the mandatory three-host campaign, backup adoption, master-host loss or browser acceptance.

## R7 executable refresh and exact replay

The matched Core and Controller r7 candidates were built from one frozen source map. The
Core executable SHA-256 was `1435dc55ed49950f5ed9e3987f453752f9fb27b97e8b12159eab316a802d7ee7`,
and the Controller executable SHA-256 was
`69bd3ac73371b8f26b63c4f904411c4b0f6cb5aae19644fef48851596d221af0`; both record the
same source-map digest. The installed Debian Core package remained
`0.3.6~recovery.s0`; only the executable changed for this disposable candidate check.

The first Core file-replacement attempt exposed a private installer error: it passed a
filesystem path to `os.fchmod`, which requires an open descriptor. The failure occurred
before the atomic replacement; readback showed the original r6 executable still running
after service recovery. A shared integration helper now stages the executable beside its
destination, sets mode through the open file descriptor, fsyncs and checks its owner, mode
and digest before stopping Core, then verifies the old digest before same-directory atomic
replacement. A real temporary-filesystem test covers staging, replacement and refusal when
the installed target changed. Independent review cleared that helper before the next swap.

The second attempt installed and started the r7 executable. The selected VM remained shut
off with its single existing native snapshot, both original 4 KiB disk markers passed, and
Core root remained locked. A first replay harness attempt then received HTTP 401 on local
PAM session creation because that invocation omitted the outer temporary-password unlock;
it failed before submitting an API operation. That attempt and its request envelope remain
separate and immutable.

The next attempt used the existing locked-root PAM flow and the exact previously preserved
request body and operation ID. The exact replay returned the same operation, and the runner's
changed-body POST passed its expected HTTP 409 assertion. Per-request raw status/body was not
serialized separately. The existing operation remained succeeded in the Core API and master
catalog. The Core service restarted under the r7 executable; a second exact replay
after restart retained the single native snapshot, stopped VM, both backing chains and both
disk markers. Controller readback showed the host fresh and returned the same snapshot
descriptor. The Controller remained available for this r7 replay. No new operation ID,
snapshot, guest boot, revert, deletion or cleanup occurred. The private attempt records are
preserved in the ignored lab; the earlier r5 evidence above remains unchanged.

The focused Core API runner suite now passes 16 tests, including the new staging and atomic
replacement checks. The assembled private remote installer payload and tracked Python files
compile, the worktree documentation gate and repository privacy test pass. Full Python
discovery ran 530 tests with one skip and one unrelated error: the separate Deep32 recovery
payload test generated an `IndentationError` in its remote payload. That failure is outside
this Core API runner and remains visible rather than being counted as a green full suite.
