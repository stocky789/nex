# Backup repository destination candidate checks

Date: 2026-09-28. Candidate: uncommitted development worktree. This report
records checks reported for that candidate; it is not a release acceptance or
an untouched lab transcript. Names below are fictional aliases. Raw service
outputs, credentials, host identifiers and fixture paths are excluded.
The tracked Core now advertises NFS, SMB3, S3 and SFTP. Bounded native checks
passed stopped and online cross-host recovery, selected fault and retention
boundaries, fenced promotion and fresh Controller adoption for all four kinds.
The final enabled-build browser and real-clock missed-schedule gates remain
open, as do release and forge CI acceptance.

**2026-09-29:** every native result below predates the BUG-0716 to BUG-0736
review fixes and the no-legacy cleanup. Those changes have focused unit tests
only; no check in this report has been rerun on them. See the
[destination plan](../plans/2026-09-28-backup-repository-destinations.md#2026-09-29-review-and-fix-pass).

## Source checks

`go test ./internal/agent ./internal/controller ./internal/protocol ./internal/vmhost ./internal/localcore -count=1`
passed in all five packages (31.150s, 41.238s, 0.101s, 10.890s and 7.213s,
respectively). This exercises source-level behavior, including destination
admission, storage operations and authority transfer checks. It does not prove
that a selected server durably publishes a recovery point or that a restored VM
boots. The source check may need repeating after later worktree edits.

`go test ./internal/controller -run 'TestBackupDestinationRelay|TestBackupCredential|TestBackupRepositoryList' -count=1`
also passed. It focuses on Controller relay, credential and list behavior;
live Controller-to-Core credential delivery remains unproven.
Focused Core policy compare-and-set checks passed for matching, missing and
stale revisions; the request-only `expected_revision` is cleared before the
policy is stored. This is source coverage, not a live policy switch.
The full Controller suite passed after the policy compare-and-set and
uncertain-reply UI changes. This does not replace a live policy-switch check.
The paged backup inventory contract passed 80 API contract tests and
`python3 scripts/generate_api.py --check`. The new Core preserves its history
when an older Controller cannot browse it. A later native page read is
recorded below; it used an overlay before the latest page guards and needs
repetition on the corrected candidate.
After adding the authenticated Core peer repository-access POST and the
missing-operation GET 404 response, the full 81 API contract tests and
`python3 scripts/generate_api.py --check` passed. The member access probe
is process-local; repository identity remains master-owned and adopted
through existing inventory.
An independent reviewer measured a temporary Go overlay on the workspace:
the first page across about 1,080 synthetic runs and 69 MiB of JSON took
about 160 ms, and a 17 MiB test traversal took 0.61 s. These are source-side
synthetic timings, not native Core guest or product latency. The current
read/hash walk is proportional to all retained history under a lock; native
69 MiB page latency and longer-term scaling remain open.
After the Controller **Run now** collision fix, the actual Core Run-loop and
interrupted member-dispatch regressions passed, as did the full agent,
localcore and Controller suites. A native Controller-path rerun is pending.

Later integrated worktree checks were reported green: `go test ./... -count=1`;
`python3 -m unittest discover -s tests` (565 tests, one skipped); from `web/`,
`npm test`, `npm run typecheck` and `npm run build`; and
`python3 scripts/generate_api.py --check`. The documentation gate and
repository privacy test also passed. These are local source/build checks;
no forge or hosted CI run is claimed. Earlier package timings above belong
to an earlier worktree state.

In an earlier restricted continuation workspace, local socket creation returned
`Operation not permitted`, and the recorded disposable QEMU process IDs were
absent. `go test ./... -count=1 -timeout=10m` failed across packages at
`httptest` listener creation (`listen tcp6 [::1]:0: socket: operation not
permitted`). `python3 -m unittest discover -s tests` ran 565 tests, with two
failures, 15 errors and one skip; socket denial and systemd permissions
prevented the affected tests from completing. `go test ./... -run '^$'
-count=1` compiled all packages and passed, but ran no tests. In `web/`,
`npm test`, `npm run typecheck` and `npm run build` passed. API generation,
the documentation gate (94 paths), repository privacy and `git diff --check`
also passed. These checks did not establish native lab behavior.

In a later unrestricted continuation, `go test ./...` passed;
`python3 -m unittest discover -s tests` passed 568 tests with one skip;
from `web/`, `npm test`, `npm run typecheck` and `npm run build` passed;
`python3 scripts/generate_api.py --check` passed; and
`python3 scripts/check_docs.py --worktree` passed over 97 paths. Package
output warnings came from expected test fixtures. These are actual local
checks, not a forge CI run or native backup/restore acceptance.

At the BUG-0689 restore reconciliation checkpoint, this continuation ran
`go test ./...`, `python3 -m unittest discover -s tests` (569 tests, one
skip), web `npm test`, `npm run typecheck` and `npm run build`,
`python3 scripts/generate_api.py --check`, the documentation gate and the
repository privacy test; all passed. The Core packaging notice change came
after that checkpoint; its later disposable `.deb` inspection is recorded
below. The signed release package and ISO remain unreviewed. These source
checks do not close the destination lab matrix or establish CI success.

After BUG-0691 and BUG-0692, a two-CPU, low-priority `go test ./...
-count=1` run passed the agent and other packages but failed the unrelated
`TestConsoleAuthorizationPairingAndRelay` because a second tunnel upgraded
before the first was paired. That focused test passed immediately in isolation,
and the whole Controller package passed alone. A serial `go test -p 1 ./...
-count=1` then passed every package, including agent, Controller and vmhost.
`python3 -m unittest discover -s tests` passed 570 tests with one skip. In
`web/`, `npm test`, `npm run typecheck` and `npm run build` passed.
`python3 scripts/generate_api.py --check`, `python3 scripts/check_docs.py
--worktree` (108 uncommitted paths), the repository privacy test and
`git diff --check` passed. The expected Python test-fixture warnings did
not fail the suite. The serial result resolves the observed concurrent
console-test failure for this local source check; no forge CI run is claimed.
After the BUG-0704 zero-byte abort correction, BUG-0705 missing-point refusal
and BUG-0706 symlink-point guard, `GOMAXPROCS=2 go test -p 1 ./... -count=1`
passed every Go package. Focused race tests for SFTP abort/reconciliation and
the missing-point report loop passed. API generation, the documentation gate
(135 uncommitted paths), repository privacy and `git diff --check` passed
after the last documentation update. These are local checks; the Python and
web suites were then rerun: Python passed 571 tests with one skip, and web
`npm test`, `npm run typecheck` and `npm run build` all passed. These local
checks do not establish the remaining native gates or forge CI.
After BUG-0707 through BUG-0710 and the S3 per-artifact fence correction,
`GOMAXPROCS=2 go test -p 1 ./... -count=1` passed every Go package.
Focused S3 abort/member reconciliation race tests and both SFTP final-rename
interleavings passed. `python3 -m unittest discover -s tests` passed 571
tests with one skip; web `npm test`, `npm run typecheck` and `npm run build`
passed. API generation, documentation structure (140 uncommitted paths),
repository privacy and `git diff --check` passed after the documentation
updates. These are local checks on the current source, not forge CI or
native provider proof.
A separate read-only reviewer found no findings in the BUG-0691/0692 source
and evidence batch. That scoped review did not rerun checks or review the
entire destination candidate.
An independent read-only review of the broader candidate found BUG-0693:
terminal capture/upload failures left local staged disks behind. The focused
`TestFailedBackupRemovesPartialCapture` wrote a partial disk on conversion
failure, failed against the original cleanup condition and passed after Core
removed the work directory on terminal paths. Core preserves the work files
when it cannot confirm an online native job has stopped. This source check
does not prove cleanup after an interrupted remote upload in the lab. The
review also found that the change record's NFS limit still described online
capture as open; it was corrected to distinguish bounded proof from the
remaining fault matrix. No CI run or final full-suite rerun is claimed here.
After the correction, serial two-CPU `go test -p 1 ./... -count=1` passed every
package, the Python suite passed 570 tests with one skip, web tests/typecheck/
build passed, and generated API, documentation, privacy and diff checks passed.
The independent reviewer found no issue in the BUG-0693 correction and ran no
checks.

A subsequent **fault-injected disposable product check** booted only storage
and two Core guests under a two-CPU low-priority limit. Both Core services ran
the matched current-source capability overlay; the old service executable
digest mistake was avoided by checking the running Core binary. A temporary
Core-local `qemu-img` hook applied only to one NFS stopped-backup operation:
it wrote a partial staged disk and returned failure. The authenticated
Core-local request returned HTTP 202/pending, then the durable run settled
`failed` with the injected exit status. The exact replay returned HTTP
202/failed; the partial staging directory and destination point were absent.
This directly checks terminal conversion cleanup, not interrupted remote
upload or an active online QEMU job. The hook was removed, Core restarted,
and all fixture QEMU and switch processes were stopped; no nested VM remained
running. Raw operation receipts stay in the ignored fixture.

In a separate selected **Controller-absent schedule** check, the disposable
storage guest and two matched Core guests ran under the same two-CPU limit;
the Controller stayed stopped. The S3 service and pinned SFTP service were
available and both repositories reported online before dispatch. New Core-
owned daily policies selected the same shut-off two-disk VM, with separate UTC
slots and retention that left existing points alone. At their slots, Core
recorded one succeeded/published run and one catalog point with a manifest
digest for S3, and the same for SFTP. Authenticated Core-local backup status
returned HTTP 200 with exactly those run/point IDs. Both policies were paused
through Core-local APIs afterward; no nested VM or fixture process remained
running. This proves one scheduled dispatch per kind without Controller, not
missed-run behavior, interrupted dispatch, post-promotion schedules or
artifact restore from those particular points.

A follow-up **unavailable-destination schedule** check used fresh Core-owned
policies with separate UTC slots. The disposable S3 service was stopped before
its slot; Core reported that repository offline, settled one S3 run `failed`
with a connection error, and published no point. After S3 restarted, its
probe returned online. The selected pinned SFTP service was then stopped
before its slot; Core likewise reported it offline, settled one SFTP run
`failed` with a connection error, and published no point. Authenticated
Core-local backup status returned HTTP 200 with exactly one failed run and
zero points for each occurrence; the 19 existing catalog points remained.
Both services returned online after restart. The disposable fault policies
were paused, and all fixture processes stopped. This is one outage at the
start of each scheduled backup; missed slots during a Core outage, upload/
publication interruption, alert deduplication and post-promotion dispatch
remain unverified.

A later **four-kind test-restore lifecycle** check used the same matched
disposable Core build with Controller stopped. NFS, SMB3 and SFTP selected
their existing online two-disk points; S3 selected its Controller-absent
scheduled two-disk point. Core-local test restore for each created one stopped
VM with two disks and zero NICs. Each guest started in isolation; after
shutdown, read-only NBD mounts found both original marker hashes and a new
boot marker. The S3 guest did not finish ACPI shutdown within the selected
window and was force-stopped by exact disposable VM ID before readback; the
other three shut down normally. Authenticated Core-local test verification
recorded `passed` for each point. Core-local test cleanup succeeded for all
four, removed each test VM and disk directory, and retained one point per
kind with `last_test_result=passed` in HTTP 200 backup status. This does not
prove application-level recovery beyond the planted markers or interrupted
test cleanup. The SFTP native absence check was repeated and saved after an
independent reviewer found its receipt missing. All nested guests and fixture
processes were stopped.

A later **Controller-submitted S3 policy Run now** check used the same matched
disposable Core build and a fresh Controller. The authenticated POST returned
HTTP 202/queued; exact replay returned HTTP 200 for the same operation.
Controller operations settled `succeeded`, and its paged run and catalog APIs
each contained exactly one matching published run and point. A second request
sent the full authenticated HTTP body and deliberately discarded the reply;
exact replay returned HTTP 200 for the already dispatched operation, which
later succeeded with one matching run and point. Both used an existing paused
Core-owned policy and a shut-off two-disk VM. This proves a bounded final-
candidate Controller submission and lost-reply retry for S3, not the other
destinations or an interrupted storage upload. CPU-pinned low-priority fixture
and Controller processes were stopped afterward. Raw receipts remain ignored.

The same Controller/API path then passed policy Run now for NFS, SMB3 and
SFTP, each on a shut-off two-disk VM in sequence. Each authenticated submit
returned HTTP 202, immediate and settled exact replays returned HTTP 200,
and Controller operations settled `succeeded` with exactly one matching
published run and point in its paged inventories. The NFS and SMB3 policies
were created paused through the Controller; SFTP used an existing paused
Core-owned policy. This closes the bounded Controller Run now check for all
four kinds, while upload interruption and broader fault recovery remain open.

A live headless Chromium walkthrough against the same Controller selected
the existing SMB3 policy, showed both repositories' current online status,
confirmed a switch to the new SMB3 repository and submitted one PUT returning
HTTP 202. Core's reported policy then moved from revision 1 to 2 with the
new repository ID. The confirmation screenshot was visually inspected and
the browser reported no page errors. This proves a live UI policy switch;
an earlier disposable API check covered old-point restore separately.

The browser also submitted a new SFTP repository with a transient SSH key.
Credential handoff returned HTTP 201, registration was queued with HTTP 202,
then its Controller operation settled `failed` with Core's generic backup
unavailable error and no repository appeared. Read-only membership inspection
showed one ready member whose selected disposable guest was off. Source review
traced this to mandatory pre-commit delivery to every ready member. This first
walkthrough **failed**. The screenshot masks the disposable private key.

After BUG-0694, a matched Core build verified the master's private credential
before committing registration and treated delivery to an offline member as
retryable. A new live browser SFTP credential handoff returned HTTP 201 and
registration HTTP 202; the Controller operation succeeded and authenticated
inventory showed the new repository at generation 1. The online member
reported read/write access; the offline member reported a failed probe. The
online member was stopped before the other guest started. Within seconds the
reconnected member reported read/write access through Core's periodic probe;
its signed authority replica was verified at the master's generation and
contained the backup repository manifest. This is bounded registration and
automatic catch-up proof, not promotion, credential rotation or broad fault
acceptance. The screenshot masks the disposable private key. All fixture and
browser processes were stopped afterward.

## Controller-absent policy retention and catalog recovery

In a later two-Core disposable fixture with Controller stopped, paused
Core-owned policies for NFS, SMB3, S3 and SFTP each ran on a shut-off
two-disk VM through authenticated Core-local Run now. Each submission
returned HTTP 202, settled `succeeded`, and exact replay returned the
retained result. With `keep_latest=1`, each kind retained its protected
older point and its newest point. NFS and S3 pruned an intermediate new
point; SMB3 and SFTP pruned two eligible older points each. This is bounded
policy retention evidence, not interrupted prune proof.

The same fixture then simulated local catalog index loss for one selected
point per kind by saving its Core index file outside the active catalog and
removing the selected index. Authenticated NFS and SMB3 rebuilds settled
`succeeded`, restored the selected point and kept their protected points;
already pruned points stayed absent. The initial S3 rebuild exceeded the
30-second Core-local HTTP write window. Its receipt stayed `pending`, and
an exact retry returned `needs_attention` without scanning. BUG-0695 moved
the scan into a bounded Core worker and made pending receipts retryable. On
the matched fixed build, the **same S3 operation ID** returned HTTP
202/pending after a Core restart, settled `succeeded` after 19 two-second
polls, and the catalog retained 24 points. The S3 index had been restored
from a disposable rollback copy before this retry, so this proves
interrupted-request recovery and catalog preservation, not a fresh
missing-index S3 rebuild.

The SFTP selected index was removed again after S3 settled. Its exact
rebuild remained pending when the storage guest's dedicated SFTP listener
had not restarted. After restoring the listener and restarting the Core
service, Core recorded a conflict: the older SFTP fixture has incomplete
remote delete records from prior fault checks. Core refused to scan past
those records; the selected index was restored byte-for-byte from the
disposable rollback copy. A clean SFTP rebuild and prune reconciliation
remain open. No incomplete remote delete marker was manually removed.
Raw operation, catalog and guest receipts remain ignored. The fixture used
only storage and one Core guest for this retest, pinned to two workstation
CPU cores at low priority. All lab guests and the private switch were
stopped afterward.

A later **full-pool** continuation started the storage guest and all three
Core members on matched capability-only builds. A first fresh S3 rebuild
request returned HTTP 503 while one member was off. Two local catalog index
copies from the earlier fixture rollback were then found empty after abrupt
lab stop; the saved copies were restored and file-synced before all members
verified the master's current signed replica. With every ready member
current, a fresh selected S3 index was removed, the catalog directory synced,
and the new Core-local rebuild returned HTTP 202/pending. It settled
`succeeded` after ten two-second polls; the selected point returned
byte-for-byte, the protected old point remained, the previously pruned
intermediate point stayed absent, and exact replay returned HTTP
200/succeeded. This closes the bounded S3 missing-index check on the
selected disposable profile.

For a clean SFTP rebuild, the same pool used a separate registered SFTP
repository containing only its owner record. Core-local access test passed.
A stopped two-disk backup returned HTTP 202, settled `succeeded`, and
published a manifest plus two nonempty disk artifacts. After the selected
local index was removed with a synced rollback copy retained, Core-local
rebuild returned HTTP 202/pending, settled `succeeded`, and restored the
index byte-for-byte. Authenticated catalog inventory showed exactly one
point for that repository; exact replay returned HTTP 200/succeeded. The
older repository's incomplete remote delete records were left untouched.
This closes a bounded clean SFTP missing-index rebuild, not interrupted
prune reconciliation or broad destination fault acceptance. Raw receipts
remain ignored in the disposable fixture.

The clean SFTP repository then passed a **pinned host-key fault** check. The
disposable storage guest's dedicated SFTP listener was replaced with a
different Ed25519 host key on the same endpoint. Core-local **Test access**
returned HTTP 200 with the repository resource, but its `access.status` was
`failed` and reported the pinned-key mismatch; it did not claim readable or
writable access. After the original pinned-key listener was restored, a fresh
test returned HTTP 200 with `access.status=ok`, readable and writable true.
The rebuilt point's catalog index remained byte-identical. This checks one
explicit host-key change and recovery, not credential rotation, an upload
interruption or restore while the key is wrong.

The same clean SFTP point passed a selected **corrupt artifact refusal**.
After an acknowledged copy of one remote disk artifact was saved, one byte
was changed in the disposable store. Core-local test restore accepted the
request asynchronously, then settled `failed` with an artifact digest
mismatch. The requested VM was absent from libvirt and pool inventory, and
its restore staging directory was absent. Exact replay retained the failed
result without creating a VM. The original artifact was restored and compared
byte-for-byte with its saved copy. A fresh test restore then succeeded,
registering a stopped, zero-NIC, two-disk VM. Core-local cleanup succeeded;
the recovery point remained. This recovery VM was not booted or marked `passed`;
the earlier four-kind test restores cover isolated boot and verification.
Raw receipts remain in ignored fixture files.

The same selected point also tested catalog recovery against an altered disk.
After saving and removing its local index, the altered remote disk made
Core-local rebuild return `202/pending`; operation GET retained an artifact
digest error and did not report success. The remote disk was then restored
byte-for-byte from the acknowledged copy. Exact retry of the pending rebuild
settled `succeeded`, restored the index byte-for-byte from its saved copy,
and returned HTTP 200 on replay. This demonstrates refusal and repair for
one SFTP artifact; it does not establish interrupted prune recovery. One
immediate lab API login was rate-limited during the retry attempt; the later
exact replay and artifact/index comparisons were captured in ignored receipts.

Selected NFS and SMB3 points then passed the same **modified disk refusal**
check. For each, one acknowledged 384 KiB artifact was copied and synced in
the disposable storage guest, one byte was changed, and Core-local test
restore returned HTTP 202 before settling `failed` with a published disk
digest mismatch. Neither requested VM appeared in libvirt or active pool
inventory; no VM disk or restore staging directory remained. Each original
artifact was restored and compared byte-for-byte with its saved copy. Exact
NFS and SMB3 replay retained their failed receipts; one immediate SMB3
attempt was blocked by the lab API login rate limit before the later replay.
These selected faults do not establish missing-artifact, in-flight upload or
application recovery.

On this worktree, low-priority serial `go test -p 1 ./... -count=1` passed all
packages, and focused `-race` checks for the pending catalog receipt and
Core-local pending response passed. The full Python suite passed 570 tests
with one skip; web `npm test`, `npm run typecheck` and `npm run build` passed.
`generate_api.py --check`, `check_docs.py --worktree`, the repository privacy
test and `git diff --check` passed. The selected disposable checks above used
matched current-source Core overlays; no forge CI or signed ISO result exists.
The selected guests were synced and asked to power off. One Core QEMU process
remained after its guest connection closed; fixture-owned cleanup stopped it.
All selected Core, storage and private-switch processes were confirmed off.

A later attempt to prepare this full-destination fixture for fenced promotion
stopped before any Core guest booted. A fixture-labelled libvirt container
could reach the private VDE control socket, but its QEMU handshake advertised
a callback under the container's separate `/var/tmp`; the host-side switch
closed the connection and libvirt refused the first domain start. The separate
callback path is the likely cause; no pool
operation, fence or promotion was submitted. The labelled container was
stopped, the storage guest was synced and shut down, and all fixture QEMU and
switch processes were confirmed off. This is a **lab wiring blocker**, not
evidence of a Core destination failure or a promotion result.

## Four-destination fenced promotion and recovery

The same disposable three-Core fixture was later restarted with matched
current-source overlays. A fixture-only SSH fencer addressed the old master's
QEMU monitor directly; its identity and status probes passed before use. The
Controller registered the fence profile while the old master was reachable,
and both members verified the same complete signed authority generation.
After disconnecting only the old master's pool-facing NIC, a fresh Controller
preflight reported `ready_to_fence` for that host. The confirmed promotion
settled `succeeded`, the fencer stopped the old master's QEMU process, and the
candidate became master with the other member still online. The old master
remained off throughout the following checks. This later result supersedes the
earlier fixture wiring blocker; it did not boot the failed libvirt-container
domain.

Authenticated Core-local inventory before and after promotion retained the
same six repository IDs, 15 policy IDs, 25 point IDs and 77 run IDs. Four
pre-existing pending runs changed to `needs_attention` with an authority
transfer error; no old ID disappeared. Fresh NFS, SMB3, S3 and SFTP access
probes on the promoted master each reported online, readable and writable.

With the former master fenced off, one stopped, bootable two-disk VM on the
promoted master was backed up once to each destination. Every operation
settled `succeeded` with one published point. Two backup HTTP replies were
lost after Core had durably succeeded; exact operation lookup found their
success. The source disks were then made unavailable while four restore-as-new
operations ran. Each produced one stopped, zero-NIC, two-disk VM. Both restored
QCOW2 disks compared identical to the withheld source disks before boot. Each
restored VM subsequently booted in isolation and shut down; read-only mounts
found the expected system and data markers plus a fresh boot timestamp.
The original source disks were reinstated with their prior hashes unchanged.

Exact replay of all four backup and four restore requests returned the same
eight succeeded operations without another run or point. Final inventory had
the same six repositories and 15 policies, plus exactly four points and eight
runs. The surviving member verified the promoted master's final signed
generation and digest. Raw requests, receipts, disk comparisons and marker
checks remain in the ignored private fixture. This is bounded **same-host**
post-promotion payload recovery for all four kinds; at that checkpoint,
cross-host recovery after this promotion, mixed-version adoption,
interrupted publication/prune and the
broad fault and browser matrices remain open. SMB3, S3 and SFTP remained
enabled only through ignored capability overlays.

The same promoted fixture then restored each of those four new points on the
surviving **other member** through fresh Controller requests. That member had
healthy local storage with enough free space; the four point/repository pairs
were rechecked against the live catalog. The original source disks on the
promoted master were withheld throughout the restores. Each operation settled
`succeeded` with one stopped, zero-NIC, two-disk VM on the other member.
Each VM ran in isolation. Private screenshots showed a Debian login prompt for
NFS, S3 and SFTP; SMB3's first capture was mid-boot, and a longer second boot
reached the login prompt. Read-only mounts of both restored disks found the
expected system and data marker hashes for every kind and a fresh boot
timestamp for each. NFS did not complete ACPI shutdown within the selected
window and required an exact-ID forced stop; SMB3's first, shorter boot was
also force-stopped, then its longer second boot shut down normally. S3 and
SFTP shut down normally. This is boot and marker recovery proof, with that
shutdown limitation recorded rather than treated as a destination failure.

Exact Controller replay of each cross-host restore returned the same succeeded
operation without another VM or run. Final Controller inventory reported the
same 29 points and 89 runs, including exactly one new succeeded cross-host
restore run per kind. The source disks were restored to their original paths
and both original hashes matched. During final inventory, two unfiltered
Controller catalog-page requests returned HTTP 504 while Core-local catalog
reads returned all 29 points; those 504 responses were observed in the session
but were not saved as separate receipts. Filtered Controller reads and a later
unfiltered read returned HTTP 200 with all 29. This transient timeout remains
a latency limit for further investigation. Screenshots and retained receipts are private
and ignored. The promoted old master remained fenced off. The broader fault,
lifecycle, mixed-version, browser and release gates remain open.

A further **fresh Controller** check used a new empty data directory, new
Controller CA and administrator secret, without the previous Controller
database. The previous Controller and its tunnels were stopped. The promoted
master and surviving member each enrolled through a separate site token; the
fenced former master stayed off. Authenticated reads from the new Controller
found the original pool and the same six repository, 15 policy and 29 point
ID sets, plus an 89-run total. It then submitted one
stopped two-disk backup per destination, each yielding exactly one succeeded
run and published point. Exact replays reused the four succeeded operations.
The surviving member verified the promoted master's signed authority
generation and digest after those new runs.

Through the new Controller, a fresh NFS point was restored on the other member
while the original source disks were withheld. The stopped zero-NIC guest
booted alone to a login prompt and shut down cleanly. Read-only mounts found
both expected markers and a fresh boot timestamp. Exact replay reused the
same succeeded operation; final inventory had 33 points and 94 runs. A
complete two-page Controller read matched all 94 Core-local run IDs, and the
surviving member verified the latest signed authority generation and digest.
The source disks were returned with their original hashes unchanged. This
proves bounded Controller replacement and resumed backup/restore operation
for a promoted pool. The fenced former master was not re-enrolled, so its
Controller host record and offline presentation were not part of this check.
Mixed-version Controller upgrade, broader interruption and other per-kind
replacement restores remained open at this point.

Four additional daily policies were created through the fresh Controller at
staggered UTC minutes, one per destination, for the shut-off two-disk VM.
After all four policy operations succeeded, Controller and both of its
private tunnels were stopped before the first slot. With Controller absent,
NFS, SMB3, S3 and SFTP each produced exactly one succeeded Core-owned
`backup-scheduled` run and one catalog point. Authenticated Core-local final
inventory contained 37 points, 98 runs and 19 policies; the four test
policies were then paused through Core-local API. The surviving member
verified the promoted master's signed generation and digest after the runs
and pauses. A short burst of Core-local logins returned HTTP 429 during final
inventory collection; one later batched authenticated read returned the
complete counts and records above. This proves selected post-promotion,
Controller-absent scheduled dispatch, not missed slots while Core is down,
mid-upload interruption or alert deduplication.
The selected Core and storage guests were synced and asked to power off;
fixture-owned cleanup stopped the one Core QEMU process that remained. All
fixture-owned QEMU, Controller, tunnel and private-switch processes were then
confirmed off, and the labelled fixture fencer container was stopped. The
fenced former master was never booted during these later checks.

## Bounded online recovery on the selected disposable fixture

A later low-resource retest used package 0.4.12 with matched current-source
Core binary overlays on two disposable hosts, a disposable storage guest and
a replacement Controller. The outer guests were pinned to two workstation CPU
cores and tested sequentially; the third Core stayed off. SMB3, S3 and SFTP
were enabled only by an ignored capability overlay. This was not an installed
release ISO or an acceptance run against production hardware. Private raw
requests, operation receipts and disk-marker hashes remain ignored under the
fixture; this summary contains no environment identities or credentials.

A bootable Debian guest with two disks and no NICs supplied system, data and
boot markers. NFS online crash-consistent backup returned HTTP 202 and
settled one succeeded/published run and one point. Its exact replay returned
HTTP 200 without another run. After the source disks were made unavailable,
restore-as-new registered a stopped VM on the other Core host. Both markers
were read from its disks through read-only NBD mounts; an isolated start and
shutdown changed the boot marker while preserving both disk markers. The
original source disks were restored. A separate NFS request sent through an
authenticated Controller session lost its HTTP reply after submission; Core
reported success and exact replay returned HTTP 202 with one new run and one
point. This is bounded lost-reply evidence for NFS, not every fault path.

The first S3 online request exposed BUG-0691: QEMU could not stage below
Core's root-only state and the run remained pending with a terminal
`needs_attention` result. A subsequent fixture attempt accidentally ran the
old service binary, so it was not evidence for the fix. After checking the
actual service executable digest, a fresh disposable VM exercised the fixed
Core. S3, SFTP and SMB3 online crash-consistent two-disk backups each settled
one succeeded/published run and one point; exact Controller backup replay
returned HTTP 200/succeeded for each. A deliberate QEMU work-root mode fault
on the fixed binary returned one failed run, no point and HTTP 200/failed on
exact replay; the fixture mode was restored immediately. The original two
uncertain fixture operations were not retried as new captures.

With the fresh source VM shut off and both original disks unavailable, each
of those three online points restored as a new stopped VM on the other Core
host and a different storage registration. Each had two disks and zero NICs.
Read-only mounts found both exact source marker hashes. Each restored guest
was started in isolation, shut down, and read again: the boot marker changed
while both disk-marker hashes stayed equal to the source. The source disks
were restored after the three checks. SFTP and SMB3 master/member restore
runs settled succeeded/registered, and exact Controller replay returned HTTP
200/succeeded. S3's member had succeeded and the restored guest booted, but
the master initially kept its run pending and returned `needs_attention`:
BUG-0692 classified a timed-out full artifact readback as permanent. Its
focused test failed before and passed after the repair. Restarting the
matched fixed Core reconciled that **same** S3 operation to
succeeded/registered; exact Controller replay returned HTTP 200/succeeded
with one run and no second VM.

The source regressions `TestRemoteOnlineBackupUsesQEMUAccessibleWorkDir` and
`TestPeerS3RestoreVerificationTimeoutCanRetry` passed after their respective
red runs. This lab proves bounded online capture and booted cross-host
recovery for the selected services. It does not prove quiesced capture,
full/read-only destination behavior, every credential fault, interruption at
publication, promotion payload recovery for each kind, large-volume peer
verification or CI. All selected QEMU guests, Controller, tunnels and switch
were stopped after the checks.

## Disposable service smoke

The selected disposable fixture used two Core guests and a separate storage
guest. NFSv4 and SMB3 service checks passed two-client mount, exact source,
write, rename and independent read. A confined OpenSSH SFTP profile passed
pinned-key upload, rename and read. An earlier stop/relaunch receipt used a
fixture that tried graceful termination first, so it did not prove a crash.
In a fresh selected fixture, the harness validated the owned QEMU process ID,
start time, command and forwarded ports, then used immediate pidfd SIGKILL
without a graceful shutdown after SFTP file sync, rename and parent-directory
sync. After relaunch, a second Core guest read the exact acknowledged bytes.
The fresh profile also refused an existing target and passed concurrent
directory-claim and rename checks with one winner.
This proves that bounded service primitive on the selected server profile;
it does not prove a complete Nex recovery point or restore. These are
service-profile checks, not Nex backup or restore tests. They used
`tests/integration/backup_repository_destinations_lab.py` with `--lab-dir`
pointing at the selected disposable fixture; the SFTP runs used
`--transfer-services` and `--sftp-directory-profile`. The published command
omits the private fixture path. This one OpenSSH/ext4/qcow2 profile does not
establish resumable prune or full recovery-point durability.

A disposable SeaweedFS 4.47 S3 profile passed verified TLS, a bucket-scoped key
rejecting another bucket, conditional simple PUT (first success, repeat
precondition failure without changed bytes), conditional completion of an
object larger than 5 MiB through multipart upload, independent GET digest and
LIST, and two-client concurrent-writer winner checks. After the fixture stopped
and relaunched the storage guest with termination mode unverified, two freshly acknowledged
concurrent-write objects returned missing on GET and were absent from LIST.
This selected profile is **unsupported** because acknowledged publication did
not survive that stop/relaunch.

A separate, single-node archived MinIO community profile, built from tag
`RELEASE.2025-10-15T17-29-55Z` with `MINIO_FS_OSYNC=on`, passed the selected
disposable S3 service checks. Verified TLS and a scoped key passed; simple PUT
and multipart conditional create each succeeded once (200) and refused the
repeat (412). Two clients through one gateway had one concurrent winner and
read back the exact bytes. After the fixture stopped and relaunched the storage
guest with termination mode unverified, independent GET digests and LIST retained the
acknowledged simple and multipart objects. That earlier receipt did not prove
abrupt-crash durability. A fresh selected fixture validated the owned QEMU
process ID and used immediate pidfd SIGKILL without graceful shutdown after
simple and multipart conditional writes and concurrent two-client attempts.
On relaunch, independent GET hashes and LIST retained the acknowledged simple
and multipart objects, with one winner and exact bytes for each concurrent
name. With the whole data directory
temporarily unavailable in a separate disposable profile, GET and a
conditional simple write over an existing name returned 404, and conditional
multipart completion returned 503. No write was acknowledged; restoring the
directory preserved the exact prior bytes and LIST names. With one object's
`xl.meta` malformed while
the service was live, GET of that object and conditional simple and multipart
writes over its name returned 500 without acknowledgement. Restoring that
metadata and restarting the service preserved the prior bytes and LIST entry.
These are coarse whole-drive outage and malformed-metadata checks, not physical
per-object EIO injection or multi-node read-quorum proof. This establishes only the
selected single-node service profile. Neither this MinIO result nor the SFTP
product result proves full Nex recovery-point crash durability.
A degraded read-quorum fault and the full Nex S3 product and fault-recovery
matrix remain pending; a bounded stopped two-disk backup and restore passed
later in this report.
No cloud-provider or physical-hardware coverage is claimed.

## Browser review with simulated API

The built Controller UI was exercised in headless Brave against simulated API
responses. Private captures were inspected but are not attached to this public
report. Interactions covered loading and empty states, offline health, a stale
online report becoming unknown, S3 secret handoff and clearing after
acknowledgement, exact retry after a 503 response, a 403 permission response,
NFS-only controls without a destination capability, and a 390 px viewport.
The policy **Change repository** panel also showed old/new repository health,
required explicit confirmation, used exact retry after an uncertain 503, and
respected the SMB3 capability gate. A later simulated browser pass covered
`202` pending semantics and **Check task status** in the panel after an
uncertain reply; the exact request remained available for retry.
The final simulated Playwright pass also covered this flow; captures remain
private and do not prove live Controller/Core acceptance.
No page errors were observed. This verifies those UI interactions with
simulated responses only; it does not establish live Core behavior or delivery
and custody of credentials across Controller and Core.

## Live disposable Controller browser review

An ephemeral Playwright/Chromium session opened the disposable replacement
Controller with its fixture administrator login. The browser explicitly
accepted that lab's self-signed certificate. Captures were inspected in an
ignored private fixture directory and are not attached here because they
contain unsanitized lab identifiers. The Backups page displayed nine adopted
recovery points. Repositories displayed five adopted entries across NFS, S3,
SFTP and SMB3, with Core-reported online/offline health and unknown host
access; SFTP was offline in this observation. The create form offered exactly
NFS, SMB3, S3-compatible and SFTP with kind-specific fields. Policies showed
five adopted policies and an offline-repository warning. The **Change
repository** panel showed current and new destinations, required explicit
confirmation, kept **Save** disabled until selection, and closed on Escape.
Some early captures showed **Loading...** before the pages populated. No UI
mutation was submitted. This verifies live read and panel behavior only;
credential submission, switch settlement and recovery outcomes remain open.
The observed SFTP offline state followed the disposable OpenSSH service not
starting after guest reboot; after manual fixture service restart, a Core-local
access test returned HTTP 200 with `ok`. This is a fixture recovery check, not
a cross-host restore result.
Source review found that the browser's master host-access lamp ignored a
current Core-local `access` observation. The UI now uses that observation and
keeps member access unknown without current `member_access`; focused and full
web tests, typecheck and build passed. A live browser recapture after that
fix remains open.
Core also added an authenticated master-to-member repository access probe;
the member checks destination, generation and identity against its local
record, or transiently prepares the exact source when no local record exists,
before the bounded read/write/list check. The master reports only process-local
member observations, expires them after two minutes and reports peer failure
as offline. The master timestamps a result when received and rejects future
or older-than-two-minute observations, so member clock skew cannot make a
stale success current. `go test ./internal/agent -run '^TestBackupMemberAccessProbeOfflineExpiryAndRestart$' -count=1`
and the focused repository-health tests passed. The focused
`go test -race ./internal/agent -run 'TestBackupMemberAccessProbeOfflineExpiryAndRestart|TestBackupRepositoryHealth' -count=1`
passed after correcting a mutable repository closure found in the first race
run; the focused race check passed again after the receipt-timestamp edit.
The periodic master health probe now refreshes its own `access` observation
from the same write/read/list result. `TestBackupMasterAccessFollowsPeriodicProbe`
failed before and passed after that source fix, and the combined focused
member/master/health race command passed.
The full agent package passed in a concurrent run that started before that
last edit, so it does not verify that exact final candidate. Native member
probing and the combined UI recapture remain open. A temporary ignored
diagnostic overlay in a disposable fixture made NFS access fresh,
readable/writable on both members. S3 on both members and SFTP on one member
instead returned a stale credential-repository error while transiently
prepared; SFTP on the other member succeeded after its prior restore had
created a local row. Both SMB3 repositories returned a retained-work identity
conflict on both members despite the same share marker. These are failed
Core probe observations under that diagnostic overlay; the member-access
matrix is not accepted. Core now persists transiently prepared member-local
repository rows before credential-gated S3/SFTP probes and compares SMB
identity using stable server/share/marker fields rather than a per-client
filesystem ID. Focused S3 fake and SMB identity tests failed before and
passed after these source fixes, including race/privacy checks. Native member
retest and browser recapture remain required.

## Disposable Core product NFS check

Three disposable Core guests ran package 0.4.12 with a current-worktree binary
overlay; this was not an ISO installation or a released package. Controller A
adopted the pool. Through Core-local operations on Core A, NFS registration
and access test succeeded. A stopped two-disk backup published one recovery
point, and the manifest and both artifact hashes matched. After the original
disks were moved aside, restore-as-new succeeded on Core A. The restored VM
started isolated with zero NICs; a separate read-only Linux probe boot read
the exact planted marker from each restored disk. The marker disks contained
no guest OS, so this does not prove guest operating-system recovery.

After a Core restart, exact backup and restore replays retained their previous
successful outcomes; a changed restore intent returned conflict (409). The
catalog showed one recovery point and two runs, and Controller A inventory
retained the same resource identities. This check used one host for backup
and restore. Online capture, scheduled dispatch, restore to another host,
Controller replacement, master promotion and all new destination kinds were
not exercised in this run.

## Disposable Core product SFTP check

A selected disposable three-Core fixture used an ignored lab-only capability
overlay; the tracked SFTP capability remains disabled. Source and binary
digests were recorded privately, not included here with the fixture's private
identifiers. Controller registration and Core access test passed. A stopped
two-disk backup completed with a full published manifest and matching remote
artifact hashes. After the original disks were moved aside, restore-as-new
succeeded; both restored qcow2 files held the expected markers. The restored
Nex VM started and stopped with zero NICs, and a separate read-only Linux
probe boot read both markers inside a guest. Exact backup and restore replays
retained success; changed restore payload returned conflict (409).

Test restore verified both markers through an isolated read-only Linux boot;
Core reported `last_test_result=passed`, and cleanup removed the test VM and
disks. A protected original point survived catalog file removal and rebuild
and retention with `keep_latest=1`. A second policy run produced a newer point
and pruned an older unprotected payload; the newest point remained. Confirmed
replace on the captured source VM succeeded with the same identity, stopped
state, zero NICs and restored original markers. A wrong-ID replace request was
refused before cutover.

The policy **Run now** HTTP request returned 503 twice even though each
durable run later settled as published and succeeded. The policy API call did
not succeed from the operator's perspective in that run; see the corrected
binary retest below. This is a bounded lab result, not a supported
SFTP release. Corrupt or failed remote reads, retention/prune fault recovery,
cross-host restore, scheduled dispatch, master promotion and immediate
SIGKILL recovery-point durability remain untested for SFTP.

## Disposable Core product SMB3 check

A selected disposable fixture used an ignored capability-only overlay for
SMB3 and SFTP; the tracked SMB3 capability remains disabled. Core used a
dedicated Samba share subdirectory and private credential receipt. Repository
registration and access test passed. A stopped two-disk backup published and
succeeded with a complete manifest and matching hashes for both remote
artifacts. After the original disks were moved aside, restore-as-new
succeeded. Both restored disk markers matched through `qemu-io` and a separate
read-only Linux guest. The zero-NIC Nex VM started and stopped.

This was a same-host product check with a lab-only overlay, not released SMB3
support. The partial repository-switch and old-point restore check follows;
full credential-rotation acceptance, including failure recovery, cross-host
restore, promotion and the fault matrix, remains open. Retained fixture
receipts for a later new-point restore are reviewed below.

## Disposable SMB3 repository switch checkpoint

On one disposable three-Core fixture, a rebuilt Controller using the current
source kept its database and adopted the existing pool. A separate Samba
share and account backed a new SMB3 repository; Core access test passed.
The policy initially selected the old repository, and a Core-local **Run now**
published a two-disk point there. An authenticated Controller policy `PUT`
switched the repository from old to new at revision 1 to 2 while preserving
the schedule ID and rule, VM scope and retention. Exact policy `PUT` retry
returned `202` with a succeeded task; a stale-revision update queued and then
failed with a conflict. A Core-local **Run now** then published a point in
the new repository.

With the original source disk directory moved aside, Core restored the old
repository point as a new zero-NIC two-disk VM. Both distinct planted markers
matched, and the isolated libvirt VM started and shut off. The Controller
**Run now** route separately failed with a command-ID collision after run
reservation; the source fix and regressions passed, but its native rerun is
pending. After the old Samba account was
disabled and its share closed, the old repository's Core access test failed,
yet an old-point restore still succeeded through an existing CIFS mount or
cache. This does not prove immediate revocation of active mounts. Clean
unmount of only the old repository followed by a Core restart made old health
offline while the new repository stayed online. A fresh old-point restore
failed `remote storage is not mounted` before VM or staging creation; exact
same-ID retry retained that failure. Re-enabling the old account and share
and starting its existing mount unit restored old access, and a fresh
old-point restore read both markers. The source disks were later restored.
This proves the disconnect and re-enable sequence only for the selected
Samba profile. A later review of retained, ignored fixture receipts found
a Core-local new-point restore submitted while the old Samba account was
disabled: submission returned HTTP 202, its operation reported HTTP 200 and
`succeeded`, and the retained marker receipt matched both restored disk
markers. A new read of that same retained Core operation after guest reboot
still reported `succeeded`. This is retrospective inspection of private lab
receipts, not a newly run restore or isolated boot. Controller replacement
during the switch, promotion with backup payloads and fault recovery remain open.
This is a partial development lab result under an ignored
capability overlay, not released SMB3 rotation.

## Disposable Core product S3 check

On a selected disposable fixture, an ignored lab-only S3 capability overlay
used the pinned single-node MinIO profile; the tracked S3 capability remains
disabled. Initial repository registration and access test passed. A stopped
two-disk backup published and succeeded with a complete manifest and matching
remote artifact hashes. The first restore failed because the local staging
directory was missing; no target VM was registered. The source fix in S3
artifact copying was then applied. A fresh restore operation succeeded while
the original disks remained aside: both restored markers matched, the zero-NIC
Nex VM started and stopped in isolation, and a separate read-only Linux guest
read both markers.

Exact backup and restore replays retained succeeded outcomes with one run
each; a changed restore payload returned conflict (409). After an S3 artifact
was corrupted, restore failed before VM registration. The exact original
object hash was then restored. A protected recovery-point marker survived
deletion of its Core catalog entry and catalog rebuild. With retention
`keep_latest=1`, the protected original and newest point remained, while an
older unprotected point disappeared from both Core catalog and MinIO LIST.

This is a bounded product rerun after the recorded failure, not supported S3
backup/restore. The single-node profile has not passed promotion, scheduled
dispatch, Controller replacement, cross-host restore or fault/retry recovery.

## Disposable policy Run now regression retest

The original Core-local **Run now** attempt returned 503 with a pending
receipt even though its durable run settled as published and succeeded. After
the source repair, a new binary overlay in a disposable product lab returned
`202` pending for the first POST. The run published and succeeded; an exact
same-ID replay returned `202` succeeded, the durable receipt was succeeded,
and exactly one run existed. This confirms the corrected acknowledgement and
exact replay in that bounded lab. It does not establish scheduled dispatch,
Controller-path behavior or the full destination matrix.

## Native signed authority checkpoint

A fresh disposable three-Core fixture ran package 0.4.12 with the same
worktree binary overlay on each guest. After authenticated enrollment and
two local certificate requests, offers and joins, the master was directly
seeded with 1,025 retained failed-run records for a simulated 67.4 MB
serialized authority. Both members received and verified the signed v2
manifest with 1,025 pages and the complete history. After 27 more records
were directly seeded, both members verified the matching new signed root
with 1,052 pages and complete 69,211,080-byte serialized history. A separate
read-only decode of every member page found the exact 1,052 distinct seeded
run IDs on both replicas, matching the seed ID-set digest; the concatenated
pages matched the signed manifest root. The digest value and private receipt
are omitted from this public report.

Direct seeding is simulation, not proof that product APIs can create or
settle this volume of runs. Native fence-verified promotion and
post-promotion lookup remain pending. NFS repository registration returned
Core 503 before a metadata receipt even though the native NFS mount probe
passed. Source review traced that refusal to master-side member observations
older than two minutes despite verified v2 roots. Independent review also
found that a fresh cached observation could outlive an immediate member
disconnect, and a v2-capable member could lag the master's current signed
generation. The final source requires a live, bounded certificate-pinned mTLS
GET from every required member, verified v2 status at the exact current
generation and digest, and a generation/membership recheck before commit.
Focused regressions cover stale observations, immediate disconnect and lagging
members; the earlier native 503 remains unresolved until a retry with the
corrected candidate runs ([BUG-0679](../BUG_LEDGER.md#bug-0679--stale-member-observations-refused-large-backup-authority-writes)).
The master Core agent later reported a
Controller HTTP 400 `invalid JSON request`, and Controller marked that master
stale while Core remained active. Source review confirmed that Core embedded
the full retained `BackupRuns` history in each master report; the roughly
69 MB seeded history exceeded Controller's 16 MiB request decode limit. A
paged report fix was deployed as a matched current-worktree Core and
Controller binary overlay. Controller then reported fresh, authoritative
pool inventory with all three members despite 1,052 retained runs beyond
69 MiB. This proves the bounded report/adoption recovery on that overlay,
not an installed release. Exact retry of the earlier repository request is
pending; fence-verified promotion is deferred for independent freshness
review.
On that deployed overlay, authenticated Controller reverse-channel paging
returned 34 HTTP 200 pages containing all 1,052 unique retained run IDs and
69,214,398 JSON bytes. The slowest first page took 1.005 s; later pages took
0.264–0.760 s, with no 502 or 504 responses. This is native read/adoption
evidence beyond 64 MiB, but it predates the latest page guards and authority
admission repair; the later current-candidate and promotion checks are below.
The earlier restricted workspace could not repeat the native retry or promotion because
local sockets and QEMU/VDE socket creation were denied with `Operation not
permitted`; the recorded lab process IDs were no longer present. No new native
outcome is inferred from that environment failure.
Independent source review also found a window where a backup metadata commit
could occur between authority snapshot build and generation advance. A
deterministic paused-push test failed before and passed after the source fix,
including a focused `-race` run; the lock now covers build and numbering but
ends before network push. The test uses a 100 ms negative assertion and a
nonparallel global hook
([BUG-0681](../BUG_LEDGER.md#bug-0681--backup-authority-snapshot-could-be-numbered-after-a-newer-commit)).

In a resumed three-Core disposable fixture with corrected NFS setup and
matching current-source binaries, NFS repository registration returned HTTP
200 while 1,052 retained runs occupied more than 69 MB. Both members verified
the same signed v2 generation 6 root and digest: 1,054 pages, 69,211,984 bytes,
all 1,052 distinct run IDs, one NFS repository and its metadata receipt.
After a corrected immutable-UUID fence profile, both verified a matching
generation 8 root and the candidate preflight reported `ready_to_fence`.
These are pre-promotion checks. Exact final page recheck, actual fencing,
promotion and post-promotion history lookup were pending when reported.

The same current-candidate fixture then completed a bounded native promotion.
The three guests ran the same locally built Core binary (SHA-256
`1083409b5c1e85f782d41656d9c913d411a1722e943cc50934c465bb6087e93b`).
Before the corrected NFS request, authenticated Core-local repository GET
returned HTTP 200 with an empty list and the interrupted request's operation
GET returned 404, so the ambiguous earlier attempt was reconciled. The new
registration POST returned HTTP 200. Promotion preflight reported integrity,
supremacy and ownership checks passed, `ready_to_fence`, and only the old
master as a fence target. The disposable fencer verified the fixture guest's
immutable UUID and QEMU process, shut that guest off, and observed process
exit, refused SSH and absent QMP. Promotion POST returned HTTP 202. Its first
fence-identity attempt failed because the fixture SSH command filter omitted
the original command; after repairing that fixture configuration, an exact
retry of the same durable operation passed Core's exclusion proof and
promotion succeeded at membership generation 4. The new master retained the
same 1,052 run IDs and ID-set digest, the NFS repository and its metadata
receipt. Core repository GET and test returned HTTP 200; the test reported
readable and writable. Authenticated Controller paging then returned 34 HTTP
200 pages with all 1,052 distinct runs and 69,214,398 JSON bytes; the slowest
page took 0.727 s. The Controller pool view adopted the new master. Its
original promotion task row still showed `needs_attention` with the transient
fixture fence error after Core had durably succeeded, so operation UI
convergence remains unresolved. Exact promotion POST replay and Controller
operation GET kept that earlier result. Source tracing found the Core command
journal acknowledged at `needs_attention` before the pool transfer finished,
and Controller's late recovery path excluded pool promotion. The promoted
master's catalog GET returned HTTP 200 with an empty list; the run history
was directly seeded failed records. This check did not run a post-promotion
backup or payload restore.

A later exact-operation convergence retest reused that already-promoted
fixture. Rebuilt Core (SHA-256
`d75c70bb7e12c6fa90024447e2976ccba3e562e49f7d971796bda3245cd28f36`)
and Controller (SHA-256
`2dca908851e2d4fa2eeca66728b0c490ccf819b907220b7b467bcdff4a33bf87`)
were restarted on the selected promoted host and Controller; the old master
remained fenced, with its QEMU process exited and QMP absent. Core's existing
promotion journal reported `succeeded` and acknowledged against the same
durable `done/succeeded` transfer. Controller operation GET reported that
same operation succeeded without error, and an exact promotion POST replay
with the original request ID returned HTTP 200/`succeeded` without another
fence or transfer. Controller still showed the promoted master, and Core
repository GET returned HTTP 200 with its one repository. This closes the
bounded operation-row convergence defect in the fixture. It does not add
post-promotion backup/restore payload evidence. Independent review found a
mixed-version gap: an older Controller can acknowledge the recovered Core
result while leaving its task at `needs_attention`, preventing a later
Controller from seeing a republished success. A focused regression failed
before and passed after a Controller fix that can settle the exact existing
operation from authenticated durable candidate inventory; independent source
review passed. Focused Core and Controller `go test -race` promotion regressions
passed, including the old-Controller-acknowledged inventory case; three new
tests had failed before their fixes. The native retained-operation retest
above predates this passive inventory path. A later separate native
mixed-version replacement check is recorded below.
See [BUG-0684](../BUG_LEDGER.md#bug-0684--recovered-promotion-retained-an-acknowledged-attention-receipt).

A subsequent post-promotion NFS payload backup attempt published a valid
manifest and both artifacts, but a temporarily lagging member caused v2
authority admission to refuse catalog/run settlement. The durable run stayed
reserved/pending and the catalog had no new point. Exact same-request POST
still reported `needs_attention` after the member caught up; source work on
safe exact settlement retry and native reconciliation was required. Published
artifacts alone were not a completed recovery point at that checkpoint.

In the resumed disposable fixture, BUG-0687's focused exact-settlement retry
test failed before and passed after the source fix. The original published
manifest and both disk artifact SHA-256 hashes matched after retry; no second
publication was made. The exact Core-local backup POST returned HTTP
202/`succeeded`, the original journal and run settled, and exactly one catalog
point represented the backup. The original 1,052 directly seeded run IDs and
their ID-set digest remained intact. A surviving member verified the signed
v2 history after settlement. See [BUG-0687](../BUG_LEDGER.md#bug-0687--published-backup-could-not-settle-after-authority-admission-recovered).

The first post-promotion restore-as-new request returned HTTP 503 before a
run was created. A later exact old request registered a new, shut-off zero-NIC
VM but left its run pending/reserved and command journal at `needs_attention`
after authority admission refused settlement. BUG-0689's focused regression
failed before and passed after its pending-run reconciliation fix; independent
source review passed. Restarting the matched Core overlay automatically
settled that same operation without registering a second VM. The journal, run
and Core API reported `succeeded`, with the run at `registered`. An isolated
libvirt start reached running and was shut down. The two restored marker disks
have no guest OS; a separate network-free read-only Linux guest attached them
and read both original markers exactly. At the end, the master held 1,054
total runs, the original 1,052 seeded run-ID digest was unchanged, and the
surviving member verified signed generation 19 with the matching 1,054-run
history. The fenced former master stayed off. This is bounded post-promotion
NFS payload recovery, not the common destination acceptance matrix. See
[BUG-0689](../BUG_LEDGER.md#bug-0689--registered-restore-stayed-uncertain-with-a-pending-run).

## Final matched-overlay destination checkpoints

In the disposable `final2` fixture, S3, SFTP and SMB3 each passed a
Controller-submitted stopped two-disk backup with one backup run and one
catalog point. With the original disk files unavailable, each recovery point
restored to member Core 2 as a stopped zero-NIC VM. `qemu-io` read both disk
markers, and a separate network-free read-only Linux guest read the same
markers from the restored disks. Exact replay returned HTTP 200 without
another run. These kinds used lab-only capability overlays; the tracked
capabilities remain disabled.

NFS passed a Controller-submitted stopped two-disk backup and cross-host
restore in an earlier matched build, with one backup run and point and the
original disks unavailable. In the later disposable `final3` fixture, its
cross-host restore was repeated after the peer NFS identity patch: the
restored VM on member Core 2 had zero NICs, both markers were read with
`qemu-io` and inside a separate network-free read-only Linux guest, and
HTTP 200 exact replay made no additional run. After SMB3, S3 and SFTP service
outages in this fixture, all five
registered repositories reported current online, readable and writable access
on both members. SMB3 recovery was delayed before those final observations.
All destination fixture guests were stopped after these checks.

The separate promoted fixture restarted with the latest matched Core binary,
reported one NFS repository and 1,054 retained runs, and was stopped again.
That inventory check did not exercise a native NFS filesystem mismatch under
the latest peer patch. Full local `go test ./...` and the Python suite (570
tests, one skip) passed; web tests, typecheck and build, API generation,
documentation, privacy and diff checks also passed. No forge CI or complete
four-kind fault/recovery matrix is claimed.

A separate disposable Debian 13 Core package build ran the package's actual
build script with a source archive and placeholder appliance bundle. Its
`.deb` contained all 19 expected vendored Go dependency license/notice files;
each matched the source archive byte for byte. `go version -m` on its Core
binary listed the 15 expected Go modules. Nex, Go toolchain and PAM
notices were present. The build did not validate a signed release appliance,
complete ISO components or corresponding-source delivery.

The fresh Controller replacement revealed BUG-0696: the fenced former master
appeared in the Members API with its stable ID and unknown connection, but
its Members link opened a missing Hosts panel because that host had not
enrolled with the new Controller. Its older Core membership also held no
hostname. The source candidate now saves the host's name in the Core master
membership at pool creation/join, copies it through the signed authority, and
uses it in the Members response when no enrolled host row exists. The UI
leaves such a row unlinked. The new Core join regression, Controller offline
member projection check and UI link check pass; the UI link check failed
before the change. The existing native fixture cannot supply a name never
recorded by its prior Core, so a separate new join/adoption fixture was used.
After independent review, Core also gained authenticated legacy-name backfill
gated by every ready peer's live replica-format support and a Controller
capability acknowledgement before reporting the new field to an older strict
Controller. A lost-response legacy join replay regression failed before its
ordering fix and passed afterward. The reviewer found no further material
source issue. A separate newly prepared disposable pool then installed and
verified the current-source Core executable on two guests, enrolled both
with Controller A, and completed their native join. The master's durable
membership saved both hostnames; the member's signed authority replica was
verified and carried the same names. With the joining member shut off,
Controller A was retired and a fresh Controller B with empty data directory
and CA enrolled only the master. Authenticated Members returned the offline
member by Core name with unknown connection, while Hosts had no row for it.
Eight private assertions passed; all new fixture QEMU and switch processes
stopped. A later headless Playwright pass against that fresh Controller and
live Core master showed the offline member by its Core name with Connection
Unknown and no Hosts link; only the enrolled master linked. The repository
and policy pages loaded two online repositories, two paused policies and
five points. Unmatched search showed the empty state. Replace required a
typed VM name with Next disabled until supplied; Escape closed Replace and
Change repository panels. No mutating request was sent. Intercepted GET 503
and empty responses showed repository/policy error and empty states; those
are browser mocks, not a real Core outage. Private screenshots and DOM
receipts were inspected. All fixture guests, Controller, browser and private
switch were off after the pass. Native mixed-version upgrade remains open.

On the stopped-and-restarted promoted four-kind fixture, five new-ID negative
registrations queued through authenticated Controller C and settled `failed`:
NFS missing export, SMB3 wrong password, S3 wrong access key, SFTP wrong pinned
host key, and S3 TLS hostname mismatch. NFS/CIFS mount journals and S3/SFTP
Core errors identified the intended refusal. The temporary TLS test hostname
alias was restored byte-for-byte. Core-local retests of the existing NFS, SMB3,
S3 and SFTP repositories returned readable and writable; exact six repository,
37 point and 98 run ID sets survived with no failed new repository ID.
The four credential handoffs returned 201, but Controller's scoped receipt
lookups returned 503 and its eligible-retirement list was empty. Source review
found Core-private backup receipts are outside that generic retirement list.
BUG-0697 now reports and retires unused master-side backup receipts through
that existing API. Focused Core and race tests passed; independent source
review found no confirmed issue after tracing member adoption. No red baseline
was captured, and this older-binary lab did not retest cleanup or rotation.
No upload, publication, full
or read-only target, restore or browser state was tested in this slice. Private
receipts are under the ignored fixture's `fault-slice-*` paths. Core1 stayed
fenced off; every fixture-owned process was stopped after the check.

After that negative slice, the fresh Controller restored one retained
post-promotion SMB3, S3 and SFTP point each on the other member while the
original source disks were withheld. Each restore settled one succeeded
operation/run and one stopped two-disk VM with zero NICs. Each VM booted alone
for about 52 seconds and shut down cleanly; separate read-only NBD inspection
matched both expected SHA-256 disk markers and a boot timestamp later than the
restore request. Exact replay returned HTTP 200/`succeeded` for each without
another run or VM. Returned source disks matched their original hashes.
Authenticated final inventory retained the six repository and 37 point ID
sets and moved from 98 to 101 runs and 19 to 22 VMs. One unfiltered final
Controller inventory request transiently failed; its immediate full retry
returned HTTP 200 throughout. The former master stayed fenced off. Private
raw receipts are ignored under the fixture's `lab-controller-c-replacement-*`
paths. The Core binary was the earlier matched overlay, so these checks do
not establish BUG-0697 credential retirement on the final source. The storage,
Core and Controller guests, private switch, tunnels and fixture fencer were
all verified off after this slice; Core1 was never started.

For BUG-0697, the fixture was rebuilt with a current-source Core binary and
only the ignored SMB3/S3/SFTP capability flags changed. The installed
systemd executable on both live Core guests matched that build's digest;
the older `/usr/local/bin` copy was not used as a match. The fresh Controller
then reported the exact four earlier failed SMB3, S3, SFTP and S3-TLS handoff
receipts as `ready` and retirement-eligible. Each serial retire operation
succeeded, its scoped GET reported `retired`, and the eligible list fell from
four to zero. Exact original handoff replay returned HTTP 503 for each while
Core continued reporting `retired`; no secret was recreated. Private Core
inspection found four 0600 tombstones with empty credential fields and no
unused SMB side credential file. Five receipts for active repositories on
the promoted master stayed `ready`, ineligible and materially intact. All
four existing repository test operations succeeded with online/readable/
writable reports; six repository and 37 point ID sets remained unchanged,
and complete paged run IDs matched the earlier 98 plus the three replacement
restores, for 101 total. Raw credential and operation receipts stay ignored
under the disposable fixture. This selected check does not cover credential
rotation or arbitrary interrupted retirement. An active receipt was observed
as ineligible; an explicit active-retire command was not attempted. All
fixture guests, Controller, tunnels, switch and fencer were verified off;
Core1 stayed fenced throughout.

A later missed-slot preparation created one disposable NFS daily policy for
the shut-off two-disk VM. Core2 was authoritative, Core3 verified as member,
NFS access passed, and the 6/37/101 repository/point/run baseline and source
disk hashes matched. The one previously enabled SMB3 policy was paused, with
its prior state saved. All fixture guests, Controller, tunnels, switch and
fencer were confirmed off before the new policy's first 2026-09-28 18:47 UTC
slot. Its second slot is 2026-09-29 18:47 UTC; the fixture must stay off until
at least 18:50 UTC that day. No clock was changed or automatic resume set.
This is setup only: no missed or catch-up run was observed yet. Later source
edits require a new matched binary overlay before the final candidate is
claimed.

Source review subsequently found BUG-0698 in NFS/SMB3 filesystem retention:
if deletion stopped after one artifact but before catalog removal, exact
settlement and rebuild could remain blocked. A focused crash-image regression
failed before the fix on exact prune retry and passed afterward. A second
same-ID replacement regression failed before its correction and passed after
Core required any present manifest to match the durable delete intent. The
test also retains a protected control point and refuses an incomplete point
without an intent. Focused race/corrupt-point/retention checks, the serial
full Go suite, Python suite (571 tests, one skip) and privacy check passed.
Independent source review found the
replacement hazard and re-reviewed its correction with no further material
finding. A separate new disposable fixture then registered NFS and SMB3 on
the current-source capability overlay and published one protected control
and one unprotected old stopped two-disk point per kind. An ignored hook
scoped to the exact old point stopped Core during each retention prune. The
first NFS hook used `/root`, hidden by Core's systemd sandbox, and the normal
backup pruned successfully; a fresh old point and operation were used for
the corrected run. In the corrected NFS and SMB3 runs, private inspection
found matching durable intent/catalog digests, an old directory missing its
manifest and first disk artifact but retaining its second artifact, and an
old catalog row still present. New and protected points, both source disk
hashes and the shut-off VM remained intact. The selected stopped Core PID was
killed with automatic restart disabled; after MainPID stayed absent, the
clean current-source capability overlay restarted and resumed the retained
operation. Exact Controller replay returned HTTP 200 with the same operation
and no additional run or point IDs. Both old directories were gone, their
tombstones remained, and selected catalog rebuilds succeeded. Final inventory
had eight succeeded runs and five points across the two kinds. Fixture-owned
QEMU, Controller and switch processes were stopped. These are controlled
native fault injections with an ignored overlay; they do not establish a
spontaneous hardware crash or close the broader destination fault matrix.

## Native product acceptance still open

Independent review found that same-ID NFS source or observed filesystem
identity replacement could redirect retained points. Core now refuses a
source change before native preparation and an identity change under the
repository commit lock. `TestNFSRepositoryReplacementPreservesRetainedPoints`
failed before the fix (changed source reached mount preparation; changed
filesystem identity committed) and passed afterward, preserving the original
destination, generation and catalog point. A move requires a new repository
ID with the old export retained for old-point restores. Native refusal and
restore validation were open at that checkpoint. A later promoted-fixture
Core-local same-ID NFS source-change POST and exact retry both returned HTTP
503. The durable repository source, observed identity and generation were
unchanged. `TestPeerNFSAdoptionPinsMasterFilesystem` passed: the master identity stays in
the authenticated NFS request, and a member refuses a missing or different
filesystem identity before the owner-marker claim. In a later matched
disposable fixture, a temporary filesystem replaced the NFS export's backing
filesystem. Kernel NFSv4 FSIDs differed from the registered identity on the
master and online member. Member access reported failure; same-ID registration
and exact replay each returned HTTP 503. The original export was restored,
and Core's repository identity and generation stayed unchanged. A retained
old point then passed test restore, with both disk markers matching through
read-only mounts; verification and native test-VM/disk cleanup succeeded while
the catalog point remained. The fixture was stopped. This is bounded native
identity and old-point recovery evidence; mixed-version peer refusal remains open
([BUG-0690](../BUG_LEDGER.md#bug-0690--same-id-nfs-replacement-could-orphan-retained-points)).

Independent source review found that same-ID S3/SFTP destination or
credential-reference replacement could orphan retained points. Core
registration and commit now refuse such replacement; a new repository ID is
required for moves/rotation while the old destination and credentials remain
available for old-point restores. `TestRemoteRepositoryReplacementPreservesRetainedPoints`
failed before the fix for all four S3/SFTP source/reference cases and passed
after it; focused `go test -race` also passed with SMB replacement and S3
private-receipt checks. The retained catalog stayed intact. Independent
source review later passed; native final-candidate validation remains open,
so this is not an accepted S3/SFTP operator flow
([BUG-0686](../BUG_LEDGER.md#bug-0686--same-id-remote-repository-replacement-orphaned-retained-points)).

In a disposable Controller replacement check, a fresh Controller explicitly
enrolled all three existing Core identities and listed three fresh hosts in
the original pool, five repositories and five policies. Its initial older
binary returned HTTP 503 for catalog and run reads because it lacked the
current paging path. After rebuilding and restarting that Controller from
current source with the same new data directory and certificate,
authenticated catalog and run GETs returned HTTP 200 pages totaling nine
points and 30 runs. This supports bounded inventory adoption and version
compatibility behavior; it does not prove a restored VM, a complete-history
promotion or stable identity for every retained record.

In a disposable SFTP cross-host restore from the first Core guest to a second
guest with healthy local storage, Controller submission returned HTTP 202 but
Core then reported `needs_attention` with `member backup peer refused the
request`. The master run stayed pending/reserved; the member created no
restore run, staging files or VM. The member worker incorrectly sent the
adopted repository through new-registration preparation. A focused SFTP
regression failed before and passed after the source fix; the same test also
covers S3 retained remote identity. SMB3 credential scope was corrected by
source inspection without a separate red/green test. The native cross-host
retry was submitted with a fresh request ID and returned Controller HTTP 202,
but it remained pending/reserved on the master and had no member operation
after several minutes; no restore success is claimed. Source tracing found
that the earlier unresolved peer refusal held the master backup run loop
while it polled a missing member operation. The authenticated peer GET now
returns 404 for that exact absence and master reconciliation ends at
`needs_attention` without replaying the request or releasing the reserved
run/pin. `TestPeerBackupReconciliationStopsOnMissingExactOperation` failed
before and passed after the fix; focused peer checks passed with `-race`.
Native cross-host retest was pending at this checkpoint
([BUG-0683](../BUG_LEDGER.md#bug-0683--missing-member-restore-operation-held-the-masters-dispatch-slot)).
After guest restart, the old master run
reported `needs_attention` with the same peer refusal and the member still had
no restore run; that reconciles the earlier pending state as a failed request,
not a successful restore
([BUG-0680](../BUG_LEDGER.md#bug-0680--member-backup-requests-treated-adopted-repositories-as-new-registrations)).

A fresh native retest then passed on three disposable Core guests running the
same current-source capability-only SFTP overlay. The Core binary was built
with `go build -overlay <ignored capability overlay> ./cmd/nex-core-local`,
installed on the selected guests and verified to have the same digest before
service restart (SHA-256
`080c686de31a5dcf534c5fa4640528d2a75d0fd5596d8f1fdaea68cb90ae4b7d`).
This build included the tracked peer-reconciliation fix and the ignored
SFTP capability-only replacement; it predates the later member-access
candidate. The old operation stayed
`needs_attention` with an absent-member result and was not replayed. A new
authenticated Controller restore POST returned HTTP 202; Core-local operation
GETs on master and member both returned HTTP 200/`succeeded`. The restored
member VM was shut off with zero NICs. `qemu-io` read both restored disk
markers exactly; an isolated libvirt start reached running and was shut off.
A separate network-free, read-only Linux guest attached both restored disks,
read both exact markers inside the guest and powered down. Its first minimal
initramfs lacked the matching virtio block module and could not read the
disks; a corrected host-matching initramfs completed the final read. This
establishes a
bounded cross-host SFTP restore on the selected lab overlay, not tracked SFTP
availability or the broader interruption/fault matrix.

The NFS, SFTP, SMB3 and S3 checks above cover bounded stopped and online
backup and booted cross-host restore paths;
SFTP has test restore, rebuild, retention and replace evidence, and S3 has
replay, corruption refusal, rebuild and retention evidence within lab-only
overlays. The changed candidate has not completed the common
authenticated Core/Controller, cross-host and isolated guest OS recovery
matrix for NFS, SMB3, S3 and SFTP. Bounded Controller replacement and
complete-history member promotion passed as described above; a bounded NFS
payload backup/restore after promotion also passed. A later fixture passed
bounded same-host post-promotion payload recovery for the other three kinds.
The same fixture later passed bounded cross-host booted restores for all four;
broader lost-reply recovery and interruption remain open across the
destination matrix.
Broader corruption handling and SFTP/S3 prune fault/retry safety remain
unproven. Independent review found that an interrupted S3 prune could remove
the manifest while leaving a delete record invisible to catalog rebuild;
SFTP had the same gap after point-directory removal. Focused tests reproduced
both failures before the fix and passed after Core changed deletion-record
handling to retain incomplete operations for exact retry. The focused
`go test -race ./internal/agent -run 'Test(S3BackupDeleteResumesAfterLostManifestReply|SFTPDeleteResumesExactInterruptedPoint)$' -count=1`
passed, as did the broader `Test(S3Backup|SFTP)` source tests. These use
in-process protocol fakes. Native interruption and crash-durability retests
remain open, so those retention and rebuild gates remain open
([BUG-0682](../BUG_LEDGER.md#bug-0682--interrupted-remote-prune-vanished-from-catalog-rebuild)).
Follow-up review found that a partially written SFTP completion digest could
block an exact prune retry. An in-process prefix-injection check failed
before and passed after the retry repair; a wrong digest still conflicts and
scan still blocks until the full digest is durable. Focused race and broader
SFTP tests passed. Native remote interrupted-write durability and CI remain
unverified
([BUG-0685](../BUG_LEDGER.md#bug-0685--partial-sftp-prune-completion-marker-blocked-exact-retry)).
An earlier Controller-absent SMB3 scheduled run was observed
`pending/reserved` after more than 40 seconds. After the disposable Core guests
were restarted with matching current-source capability-only overlays, an
authenticated Core-local backup-status request returned HTTP 200 while
Controller was absent. The same scheduled run had settled succeeded/published
with exactly one matching catalog point. This is bounded native schedule and
settlement evidence for that selected lab profile; the tracked SMB3 capability
remains disabled. Independent source inspection found no deterministic fault
in the reservation and later-dispatch ticks.

The paged signed authority source and native fixture cover fence-verified
promotion with complete retained run history beyond 64 MiB and a bounded
post-promotion NFS backup/restore. Exact retained promotion-task UI
convergence passed in the later bounded fixture retest. Live
browser read/panel behavior was reviewed against authenticated Controller/Core
services; credential delivery, mutation settlement and recovery outcomes
remain open.
The 2026-09-28 SMB3 rotation decision uses a new repository ID and a
policy/job switch. The policy UI action was reviewed with simulated browser
responses, while the disposable Controller API switch and old-point restore
were exercised separately above. Retained fixture receipts additionally show
a bounded new-point restore while the old account was disabled; broader
old-account revocation, Controller replacement through an active switch, and
live browser mutation remain open. The tracked
SMB3 capability remains disabled.
## Server-side write denial and protected restore

In a separate disposable NFS/SMB3 fixture, the exact original NFS export and
Samba share configuration were saved before a server-side write denial. The
executing Core still read each protected point's unchanged manifest. The NFS
export returned `EROFS`, and the SMB3 share returned `EACCES` on new writes.
Each fresh stopped backup settled one failed run with no published point or
staging leak; exact request replay reused its operation. The protected NFS
point restored while writes were denied to a stopped, zero-NIC, two-disk VM.
Read-only disk inspection found both expected markers. The original export
was restored byte-for-byte and Core write/read/unlink plus repository test
recovered.

The first SMB3 protected-point restore under write denial failed before VM
creation: `VerifyBackupRepository` required a QEMU write probe for a read
operation. Core now checks the mounted source, owner and SMB share marker on
reads without the publication probes; writes and health checks retain them
([BUG-0699](../BUG_LEDGER.md#bug-0699--smb3-restore-required-a-write-probe-on-the-source-repository)).
The regression failed to compile before the read path, then passed, including
a check that writable verification still calls both probes. Focused race and
serial full Go checks passed. An independent source reviewer found no runtime
or safety issue and identified a test assertion gap, which was corrected.

A clean current-source Core binary with only an ignored SMB3 capability toggle
was installed on the selected master, and its active service digest matched
the built binary. With the same share again denying writes, Core returned
`EACCES` for new writes but read the unchanged old manifest. A fresh restore
of that protected point succeeded to a unique stopped, zero-NIC, two-disk
local VM. Read-only NBD inspection found both exact disk markers; exact replay
returned the same operation. The five retained point IDs and source disk
hashes were unchanged, and no NBD mount remained. The original Samba config
was restored byte-for-byte; Core write/read/unlink recovered. This tests a
mounted share whose server denies writes, not a locally read-only CIFS remount
or every full-destination profile. A final private process-state receipt
confirmed all fixture
guests, Controller, network switch and fencer containers off without a forced
shutdown.

Source tracing after this retest found an exact-replay corner: a previously
published manual backup without retention still ran SMB3 writable verification
before reading its intact point. An existing settlement test was extended to
deny writes; it failed before removing the redundant probe and passed after,
including an absent-point replay that remained unresolved. Focused race
passed. This is source regression coverage only; a native published-point
replay under write denial was not run. Independent review found that policy
settlement still needs write access when its retention prune remains due, so
that case must wait for destination recovery rather than claim success under
denial.

## Capped full-target faults on new NFS and SMB3 repositories

The separate disposable fixture used two new 256 MiB loop-backed ext4 targets
on its storage guest, one exported over NFS with a unique filesystem ID and
one exposed as a separate Samba share. They did not overlap the five retained
recovery points or their original repository paths. Before any fault backup,
the storage and Core guest roots had more than 27 GiB free, each capped target
had about 223 MiB free, and one small two-disk control backup published to
each new repository and was protected. A separate shut-off, zero-NIC source
VM had incompressible staged QCOW data allocating about 336 MiB, exceeding
each target's measured free space by about 113 MiB.

Each fresh stopped backup reached remote artifact publication and failed with
`ENOSPC`: NFS during write, SMB3 during file sync. Each operation retained
one failed run, published no new point and left no `.staging-*` directory.
Exact replay returned the same failed operation. Both protected control
points restored to separate shut-off, zero-NIC two-disk VMs, with both disk
markers matching in each restore. The original five point IDs, manifest
hashes and source disk hashes were unchanged. This proves bounded full
filesystem behavior on these two new targets; it does not prove full S3/SFTP
profiles or loss during final manifest publication.

During setup, the first registration of the new SMB3 share failed with
`BAD_NETWORK_NAME` before Samba restarted. Exact request replay retained that
failure, but a new request with the same repository ID and a new credential
also failed because Core had left its newly written mount unit behind. A new
repository ID succeeded after Samba restarted. Three failed handoff receipts
were reported eligible and retired; replay of the first handoff could not
restore its secret. The mount-unit failure is being handled as a separate
Core retry bug, not counted as a successful registration retry. The original
unit is preserved on the disposable guest for fixed-build retest.
Source tracing found that Core returned from the failed `systemctl enable
--now` before the caller installed its cleanup defer. `prepareRemote` now
detaches a newly created unit on preparation failure. Independent review
found that overlapping preparations could otherwise remove a successful
mount, so NFS/SMB preparation and rollback are serialized. The first-start
and overlap regressions each failed before their fix and passed afterward;
focused race and the full vmhost package passed. The reviewer found no further
concrete issue after the correction. A fresh
current-source Core capability overlay then retried the sequence on a new
absent SMB share and repository ID. The first registration failed, exact
replay retained the failure, and no new managed unit, CIFS mount or Core
repository appeared. Once the share was activated, a fresh credential and
request for the same repository ID succeeded; Core access reported readable
and writable. Scoped Core receipt GET showed the failed handoff had already
been retired by the successful same-ID registration, while the active receipt
was ready; the eligible list was empty. Original seven point IDs/manifest
hashes, source disk hashes and the separate pre-fix orphan unit remained
unchanged. The old orphan was preserved only as disposable evidence. Broader
interrupted-mount and forge CI checks remain open. All processes in this
fixture were stopped after the retest; the separate staged missed-slot
fixture remained off.

Keep the destination expansion and the parent recovery safety gate open until
these native checks and independent review pass.

## SFTP pending-prune read guard

Before a planned native interrupted-prune check, source review found that an
SFTP point with a durable delete record could still pass `readPoint` while its
manifest and artifacts were intact. The existing protection writer and catalog
scan already refused pending deletion. A focused in-process SFTP protocol
assertion reproduced the read failure before the fix (`<nil>` instead of
conflict). Core now checks the delete record in `readPoint`; the focused check
and its race run passed, and independent read-only review found no concrete
issue. Serial full Go and 82 API contract tests passed after the change;
the full Python suite passed 571 tests with one skip; the documentation,
privacy, generated API and diff checks also passed. Web tests, typecheck and
build passed on the same worktree. Exact delete retry still reads the recorded
manifest. In a matched native OpenSSH fixture, a directory permission fault
left that manifest intact after the delete record was durable. Restore and
catalog rebuild refused the pending point without creating a VM. Once write
access returned, exact prune retry and a fresh rebuild succeeded. This is
BUG-0701; the controlled permission fault does not prove an abrupt crash
boundary.

## S3 post-intent prune refusal and Core retry correction

The separate disposable four-repository fixture registered new isolated S3
and SFTP destinations. Each held one protected two-disk control point and one
unprotected old point; Core reported zero active pins on the selected prune
targets. Initial S3 registration failed because the lab-only scoped IAM policy
omitted multipart abort permission; a corrected isolated policy and fresh
request succeeded. This was fixture setup, not a product finding.

A temporary scoped S3 `DeleteObject` denial on only the old point's manifest
let the new policy Run-now backup publish one new point, then blocked retention
after `.nex-delete.json` was durable. The run reported `needs_attention`.
Restore of that pending-delete point and catalog rebuild both refused it. The
protected control and the original seven points' IDs and manifest hashes, as
well as the original source disk hashes, remained intact. Removing the denial
and restarting matched Core did not resume the prune: exact Controller replay
returned its retained `needs_attention` task. A later SFTP policy run for the
same VM stayed pending behind that unresolved S3 run, so no SFTP delete fault
was reached. Its old point's directory mode was restored before fixture
shutdown. All selected fixture processes and the separate staged missed-slot
fixture were off at the end of this failed gate.

BUG-0702 corrects the Core recovery path: a single periodic worker validates
the already-published point and retries retention under the same operation ID,
including scheduled and policy Run-now runs, without recapturing. A focused
Run-loop regression was red before the retry path and green after; it covers
write-denied Run now recovery, scheduled recovery, and a terminal run whose
journal completion was interrupted. Focused race, SFTP handshake cancellation,
and the full agent package passed. Independent read-only review found an
unbounded prune context; the corrected worker now passes its finite context
through retention, and review found no further concrete issue. On a matched
fixed-build Core restart with the scoped S3 denial removed, Core settled the
original operation before Controller started. Its journal, run, receipt and
Controller replay all reported success, with one run and one new point. A
fresh SFTP Run-now operation then published one new point and a durable delete
record before a directory permission fault stopped old-point deletion. The
pending old point refused restore and rebuild without creating a VM; exact
replay under the fault retained `needs_attention`. Restoring write access and
restarting matched Core settled that same operation automatically. Exact
Controller replay and a fresh catalog rebuild succeeded. Both protected
controls, the original seven point IDs and manifest hashes, and the original
two source disk hashes remained unchanged. The four retained S3/SFTP server
manifests matched the Core catalog. All selected fixture processes were
stopped afterward. These controlled permission faults do not establish
abrupt process-crash or power-loss durability during deletion.

## Native SFTP restore pin and retention

In the separate matched disposable fixture, a successful two-disk test restore
of an unprotected SFTP point left one exact Core-owned operation-to-point pin
while its zero-NIC test VM was shut off. A subsequent `keep_latest=1` policy
run published one new point and pruned an older unpinned point from the Core
catalog and SFTP server. The pinned point, new point and protected control
remained; exact replay reused the one run and point. Typed-ID test cleanup
removed the test VM and cleared the pin. The original seven catalog IDs and
manifest hashes and both source disk hashes were unchanged. All fixture
processes, including the separate staged missed-slot fixture, were off at the
end. This proves retention respects a durable pin during the test-restore
lifecycle; an in-flight artifact-copy pause and concurrent prune were not
exercised.

## Native remote read-only targets and SFTP error correction

In the separate matched disposable fixture, new isolated S3 and SFTP
repositories each held a protected stopped two-disk control. Scoped S3
Put/Delete/Abort denial on only the new prefix caused one terminal failed
backup with a `PutObject` refusal. A new SFTP root set to read-only let the
pinned-key account read but denied its write; its backup also produced one
terminal failed run. Each exact replay reused the failure; neither published
a point or left local staging. Under each denial, a test restore of the
protected control succeeded as a stopped zero-NIC VM; both markers matched,
verification passed, and typed cleanup removed its VM and pin. S3 and SFTP
health checks failed while writes were denied and passed after access
returned. The original point IDs and manifest hashes, the two new protected
control manifests, and the source disk hashes matched after recovery. All
fixture processes, including the separate staged missed-slot fixture, were
off afterward. A transient Controller catalog timeout after the S3 replay
cleared on an immediate read-only retry without inventory drift. Full-target
and service-loss faults remain unrun.

The pre-fix SFTP terminal run incorrectly said `backup request conflicts with
retained work` even though the server reported permission denied and no
conflicting file existed. BUG-0703 traced this to SFTP ambiguous-write
resolution discarding write errors when a follow-up read found no new owner
or intent; the same pattern affected resumed point creation and some read
errors. A focused in-process regression failed before the correction and
passed afterward for owner claim, first intent and exact intent retry under
a read-only root. Focused race and the full agent package passed; independent
source review found no remaining concrete issue. The controlled native
read-only check above used the pre-fix binary. A matched fixed-build OpenSSH
recheck then submitted a fresh SFTP backup under the same isolated read-only
root. Its single run failed with `permission denied`, exact replay retained
that error, and no point or local staging appeared. The protected control
again restored and verified both markers under denial; typed cleanup removed
the test VM and pin. After write access returned, health passed and the prior
catalog IDs, manifest hashes and source disk hashes remained unchanged. The
separate staged missed-slot fixture stayed off. This verifies the corrected
operator error on the selected native service profile, not service loss or
full-target behavior.

## SFTP full-target partial publication

A new isolated SFTP repository on a size-capped disposable filesystem reached
target exhaustion during a fresh two-disk backup. Core recorded one terminal
failed run, no catalog point and no duplicate on exact replay. The OpenSSH
server retained a partial disk artifact and publication intent without a
final manifest, occupying the capped target after the run was terminal; Core
had removed its local staging. A protected control point on that same full
target still test-restored to a stopped zero-NIC VM, both markers matched,
verification passed, and typed cleanup removed the test VM and pin. The
incomplete bytes and intent are preserved only in the disposable fixture for
diagnosis. This is a recovery defect, not a passed full-target gate: the
terminal operation could not resume or reclaim that exact remote partial
through the existing API. At that point, Core cleanup/reconciliation and a
matched native retest were required. No completed point was removed.

BUG-0704 now stages and syncs the existing SFTP manifest before artifact
upload and keeps an uncertain publication under the original Core
`needs_attention` run/journal. Core checks for the exact completed point
first; otherwise it uses a durable abort marker and the staged manifest to
remove only that operation's incomplete regular files, then settles the
original run failed. Focused protocol checks cover interrupted abort,
unknown/symlink/protected/final refusal and a malformed pre-artifact stage;
focused race and serial full Go tests passed. Independent read-only review
found no concrete safety defect. Legacy hash-only partial artifacts like the
pre-fix fixture remain inspection-required and were not silently removed.
The first matched native full-target retry reached one `needs_attention` run,
an uncatalogued partial disk and a zero-byte abort marker after the target
filled. An incomplete-point restore created no Core run, pin, VM or disk.
After capacity returned, restarting matched Core left the original backup in
`needs_attention`: `readPoint` treated the abort marker as a conflict before
reconciliation could resume `AbortIncomplete`. A new through-reconciliation
SFTP regression reproduced this zero-byte marker and passed after Core
distinguished an absent final manifest from a final-plus-abort conflict.
Focused race and full agent checks passed after this correction. A rebuilt
matched OpenSSH overlay restarted with the exact preserved partial disk,
staged manifest, intent and zero-byte abort marker. With capacity available,
Core removed those fault-point files autonomously and settled the original
backup as one terminal failed run. Controller agreed, exact replay returned
that failed operation, no fault point entered the catalog, and prior 16
catalog point hashes stayed unchanged; the new protected control remained.
BUG-0706's symlink-point guard was added after this native run. The later
guarded-build protected restore, health and hash checks are recorded below.
The historical hash-only partial in another ignored fixture remains
preserved for inspection.

An independent new S3 repository used a separate MinIO instance on a
size-capped filesystem with ample guest-root reserve. Its small protected
two-disk control was readable before the fault. The new large stopped backup
failed during multipart upload with HTTP 507 `XMinioStorageFull`: MinIO's
minimum-free threshold refused the part before the filesystem itself returned
`ENOSPC`. Core recorded one failed run, exact replay reused it, and no new
point or incomplete multipart upload appeared in the scoped bucket. While
the cap remained, the protected control test-restored to a stopped zero-NIC
VM; both markers matched, verification passed, and typed cleanup removed its
VM and pin. Removing only the cap filler restored repository health. The
prior catalog ID/hash pairs and both source VMs' disk hashes were unchanged;
only the two new protected S3/SFTP controls had been added. This passes the
selected MinIO capacity-refusal profile, not literal S3 filesystem `ENOSPC`
or every object-service implementation. All destination fixture and staged
missed-slot fixture processes were off afterward.

## Native NFS mixed-version peer and Core upgrade

A separate disposable two-Core pool paired a current master with a Core
member built from a verified historical source archive. After the NFS
export's real filesystem identity changed, a Controller backup request
kept its exact operation in `needs_attention` and the historical member
created no backup run, VM, owner marker or artifact on the changed export.
Exact replay reused the same operation. This is a safe bounded refusal,
not a terminal successful backup.

The reverse old-master/current-member request did not reach peer admission.
The historical master held its global backup slot while repeatedly polling
the absent earlier peer operation, the already recorded BUG-0683 behavior;
even a distinct VM request stayed `dispatched`. Restoring the original NFS
filesystem identity and upgrading the master to matched current Core released
that slot. The distinct VM's retained operation then succeeded once with one
run and point, and exact Controller replay returned the same result. The
first refused operation remained `needs_attention` with no member work, and
same-VM work remained serialized behind it. An old protected point also
test-restored and verified after the original export returned; its restored
disk compared byte-for-byte with source. The original export UUID, retained
point/owner hashes and source disk hashes matched baseline. This first
fixture did not prove reverse peer admission or old-Controller acknowledgement
recovery. All processes in this fixture were stopped afterward.

A second clean disposable pool tested the reverse direction before any
attention work existed. Its historical master sent a backup toward a current
member after the real NFS filesystem identity changed; the operation retained
`needs_attention` on exact replay, while the member created no fault run, VM,
owner marker or artifact. The changed export remained empty. After the
original identity returned and the master upgraded to current Core, the
protected old point test-restored to a stopped VM. Its disk compared
byte-for-byte with source, Core verification and typed cleanup succeeded,
and the original manifest, disk, owner and source hashes matched. The fault
master run remained pending/attention for manual reconciliation, preserving
uncertainty without starting a duplicate. This is bounded native fail-closed
evidence in both version directions, not an old-Controller acknowledgement
repair or a clean terminal failure. The second fixture was shut down after
the check.

## Incomplete-point restore refusal

In the size-capped OpenSSH fixture, submitting a restore for the partial,
uncatalogued point created no Core run, pin, VM or disk. The pre-fix Controller
task nevertheless remained `needs_attention` with `backup operation not
found` after exact replay. A focused Core report-loop regression reproduced
that definite pre-execution refusal as `needs_attention` before the fix and
passed after it became terminal `failed`. The final regression uses a
registered NFS repository with no selected catalog point and asserts no
run, pin or VM plus stable exact replay. Independent read-only review found
no source defect; it identified the missing registered-repository and
absence assertions, which were added. On a matched disposable OpenSSH build,
a fresh restore request for the now-absent incomplete point returned terminal
`failed`/`backup operation not found`; exact replay retained it and Core
created no run, pin, VM or disk. The older pre-fix task and Core journal
remained `needs_attention` after restart, with no native work. This retest
proves new admission behavior, not retroactive repair of prior terminal
attention results. Forge CI has not run.

An independent read-only review of the corrected SFTP abort found a separate
unsafe path: a symlink at the point directory could redirect cleanup into an
unrelated target. A focused test reproduced removal of that target's artifact
before the fix, then passed when Core rejected a non-directory or symlink
point before looking inside it. Focused race and serial full Go tests passed
after this guard; independent correction review found no further concrete
defect. A later matched guarded overlay restored the protected two-disk
control to a stopped zero-NIC VM, read both markers, verified and cleaned it,
and reported healthy repository access. The 17 catalog ID/hash pairs, control
manifest and four source disk hashes matched preflight; the failed point,
staging, intent and abort remained absent, with one failed original run. No
symlink was injected through a public Core operation; the focused regression
is the evidence for that refusal. Both selected fixtures were then off.

## Mixed-version promotion task repair

A separate clean two-Core fixture used a verified historical Controller
against current Core. With the fixture fencer temporarily unavailable, one
promotion reached `needs_attention` on the old Controller and retained its
exact Core journal. After the fencer recovered, Core completed that same
durable transfer and its journal was `succeeded` with the result acknowledged;
the historical Controller row remained `needs_attention`. Replacing only
Controller on its existing data and certificate settled the row to
`succeeded` from authenticated Core inventory, without another result.
Exact POST replay returned HTTP 200 with the same ID and status. Core
transfer and journal were byte-equivalent before/after Controller replacement,
the fencer command count was unchanged, and the old master remained off.
All selected fixture processes were shut down afterward. This is a bounded
native old-to-new Controller recovery check; no CI run is claimed.

## SFTP upload connection and service loss

A matched capability-only OpenSSH fixture used a new isolated repository and
protected two-disk control. During a new 336 MiB artifact upload, a
fixture-only hook identified the exact growing SFTP session and killed it at
about 9 MiB. Core recorded one terminal failed run with `connection lost`,
removed the partial upload and intent without manual deletion, and exact
replay returned the same failure. This is a measured mid-artifact connection
loss while the service remained available for cleanup.

A separate fresh operation cut off only the isolated SFTP service at about
9 MiB of its artifact, leaving the storage guest's management access intact.
Core restart while the service stayed down retained the same run and operation
as `needs_attention`; exact replay kept its ID. The remote partial disk,
staged manifest and intent remained, with no final manifest or point. Core
recorded one offline health transition. A later failed probe advanced its
observation time but kept the exact original transition time, proving
deduplication in this selected outage. After the service returned, Core
autonomously cleared the incomplete upload and settled the same run terminal
`failed` with `connection lost`; exact replay remained stable. Health recorded
a later recovered transition and reported online access.

The protected control then restored to a stopped zero-NIC VM. Both disk
markers matched, verification and typed cleanup passed, and the pin and test
VM were absent afterward. All 18 pre-fault catalog ID/manifest-hash pairs and
four source disk hashes were unchanged; the control's test metadata changed
as expected. This passes bounded SFTP mid-upload connection and service-loss
recovery plus offline/recovered transition deduplication on this OpenSSH
profile. Final-manifest interruption, other destinations, and forge CI remain
open.

## Member policy Run now routing regression

In a disposable two-Core pool, Controller Run now for a stopped, member-hosted
two-disk VM stayed queued and then failed with `source VM definition is
unavailable` on the master. The member received no operation. Source tracing
found a blank `HostID` in the durable master run even though Core resolved the
VM's member host for the command. The focused
`TestControllerPolicyRunNowMemberUsesOneDispatcher` regression failed before
the fix with the blank host and passed after Core bound the run host before
reservation. The check also covers legacy blank-host metadata replay and
changed-placement refusal; focused race and full agent tests passed. An
independent read-only reviewer found and verified a correction that preserves
legacy local-run replay while refusing an already transitioned blank-host
member run before native work. A frozen-source capability-only two-Core lab
then reran a fresh Controller policy request on the member VM. The master run
named the member and settled succeeded/published, the member journal and
Controller task settled succeeded, and exact replay retained one run and
point. The final manifest and two artifacts existed with no staging. The
prior 18 catalog manifest hashes, two source disk hashes and signed member
replica verified. The historical failed request stayed failed. This closes
the selected native routing regression; forge CI remains open.

## Native restore copy overlapping retention

A disposable two-Core OpenSSH fixture published protected controls P/Q and
unprotected points P2/Q2 for one stopped, two-disk member VM. A restore of P2
was paused after Core durably pinned it and its first disk copy had reached
24,707,072 of 102,432,768 bytes. While that exact copy remained paused, an
authenticated Core-local policy Run now dispatched a new backup to the member.
The member journal and master run settled succeeded; retention pruned
unpinned Q2 while retaining pinned P2, protected P/Q and newest R. This was
real concurrent Core work, not a sequential pin check.

After the copy resumed, restore succeeded, both disk markers matched, exact
restore and Run now replays retained their operations, verification and typed
cleanup succeeded, and the pin map was empty. The source disks and all 18
pre-fault catalog manifest hashes were unchanged; the surviving member's
signed replica verified. The temporary disposable Core-local password was
removed and the root account relocked. This closes the selected in-flight
restore copy versus retention gate for the SFTP profile, not the other three
destination kinds or an abrupt process crash.

## Live Controller browser review on member fixture

A read-only headless Chromium pass logged into the disposable live Controller
and inspected private captures. Actual pages showed loading states followed by
14 repository rows (including online/offline and per-host access differences),
21 recovery points and five policies, without page errors. The create form
focused Name and Cancel worked; Escape did not dismiss that inline form. An
unmatched recovery-point search displayed an empty state. The Replace panel
kept Next disabled without the typed name and closed on Escape; Change
repository kept Save disabled without a confirmed target and closed on
Escape. No destructive action was submitted.

Labeled browser response interception, not a live outage, produced a
repository 403 alert, empty rows, and old observation times rendered as
Unknown; restoring the response returned the 14 live rows. The 403 produced
one expected Chromium resource error and no other console errors were seen.
This pass did not exercise actual stale expiry, credential handoff, uncertain
reply, or a completed destructive workflow. Screenshots remain private under
the ignored disposable fixture, and the browser was closed.

## S3 final publication and member reconciliation source gates

Source review found that Core terminal-failed S3 `Publish` errors even when
the final manifest might have committed after a lost reply. It also left
uncataloged uploaded objects after a definite final-write denial. The new S3
path stages an exact intent and keeps an uncertain run in attention until an
exact published read succeeds or a durable abort wins a conditional write at
the same final manifest key before deleting matching artifacts. A first
cleanup version used only a separate abort key; independent review found that
a delayed final write could then race artifact deletion. The same-key
tombstone correction passed read-only re-review. A second independent review
found that a delayed artifact upload could still appear after cleanup, so
Core now fences each expected artifact key with its own permanent conditional
tombstone. Fake-S3 tests exercised denied and delayed final writes, delayed
single and multipart artifact writes, partial-fence retry, interrupted
cleanup, foreign or mutated objects, and a committed write with lost reply
plus failed read. Focused S3 race tests passed and independent re-review found
no concrete blocker; native MinIO semantics and end-to-end recovery remain
open.

A separate member predicate regression showed the periodic Core worker did
not revisit publication attention for accepted peer backups. It failed before
and passed after the worker admitted a retained member journal marked
`PeerDispatched` with a matching run and intent when retained; the unaccepted
member case stayed refused. Focused race passed and independent read-only
review found no concrete integration issue. This does not yet prove a native
member backup settles after interrupted publication.

## SFTP final rename safety and native admission boundary

Independent review found that an incomplete SFTP abort could delete artifacts
after checking the final path while a lost-reply final rename remained in
flight. Core now atomically claims the staged manifest source before artifact
cleanup. A fake-SFTP adversarial test forced final rename to win after the
first abort check: the pre-fix path removed an artifact; the corrected path
refused abort and preserved final and artifact bytes. Focused race and full
agent tests passed; independent read-only correction review found no
concrete defect. The symmetric abort-wins test then passed focused race:
after the claim, a delayed final rename failed, abort completed, and a
separate published control kept its manifest and artifact. Native
final-boundary fault remained open at this checkpoint; the later matched
native denial and recovery is recorded below.

A separate disposable OpenSSH service denied all SFTP renames after an
isolated repository had published a protected two-disk control. A fresh
Controller backup stayed master-reserved and pending, with no member journal
or remote operation files, for about three minutes. Restoring rename on only
that service let the **same** operation dispatch and publish within about
eight seconds. This proves the selected admission/health gate holds a run
until repository rename capability returns; it does not exercise a final
manifest rename refusal. No second operation was submitted during the fault.

## Matched final publication and provider limits

A later matched two-Core OpenSSH fixture reached the SFTP final manifest
boundary after both full disk artifacts were present. Fixture-only inotify
observed the staged manifest and the two 102,432,768-byte artifacts before
the point directory was made read-only for that final rename. Core retained
publication attention after permission denial, with no final manifest. After
access returned and the member restarted, the original operation settled
failed and exact Controller replay stayed failed; partial data, intent and
abort state cleared. The protected two-disk control restored and verified
both markers, typed cleanup removed the test VM/disks/pin, 21 earlier catalog
hashes and source hashes stayed stable, and a fresh backup succeeded. This
tests final-boundary denial and recovery, not a delayed rename after a lost
reply; the adversarial delayed-rename ordering has focused fake-SFTP proof.

The matched disposable MinIO fixture could not register a fresh S3 prefix.
A signed sentinel probe sent `DeleteObject If-Match` with a wrong ETag; the
server returned 204 and the following GET returned 404. The cached fixture
binaries all reported `DEVELOPMENT.GOGET` and had the same digest. Core
correctly refused registration because conditional delete was unproven. No
protected prefix was touched, and this fixture provides no native S3
incomplete-publication proof. A provider enforcing the precondition is needed.

A separate isolated, loopback-only S3Proxy 4.1.1 filesystem service was
limited to one CPU and 768 MiB and pinned by image digest in private receipts.
Signed protocol probes confirmed simple conditional create (repeat 412),
wrong-ETag conditional delete (412 with unchanged object), a same-key
tombstone blocking a delayed final PUT (412), and a 5 MiB-plus multipart
conditional upload whose retry failed without changing original bytes.
Conditional cleanup succeeded. At this initial checkpoint the evidence was
limited to provider protocol probes. The service
documents [conditional enforcement only within one S3Proxy process](https://github.com/gaul/s3proxy), so this
fixture cannot establish a multi-process or production provider guarantee.

The later matched two-Core fixture reached the provider over verified private
TLS. Core's own S3 registration/protocol probe passed. A stopped member-source
two-disk backup published a protected control; independent restore to a
shut-off, zero-NIC VM on the master matched both ext4 disk markers, and typed
cleanup succeeded. All 26 prior catalog ID/hash pairs were unchanged.

A fixture-only exact-ID transport gate then allowed both full fault artifacts
to upload and denied only that operation's final `manifest.json` PUT with
HTTP 403. Core and Controller retained `needs_attention` with AccessDenied;
the catalog remained at its 27 prior/control points, with remote artifacts
and durable intent/abort but no final point. Exact replay kept the same ID.
After the gate was removed and the member restarted, Core terminal-failed
that same run with the original 403 error. The remote final and two artifact
keys held exact small abort tombstones; intent/abort markers were gone, and
Core's `ReadPoint` treats the tombstone as absent. Catalog IDs/hashes and two
source disk hashes stayed unchanged, exact failed-operation replay returned
HTTP 200, and a fresh protected two-disk test restore/marker check and typed
cleanup passed while repository health was online/readable/writable. This
proves selected native S3 incomplete-publication cleanup on this one-process
service. The separate committed final case follows; multi-process safety is
not established.

A second fresh member operation exercised the opposite final-publication
outcome. The fixture proxy logged a successful upstream conditional final
manifest PUT after both full artifacts, returned HTTP 503 only for that
reply, and denied nine exact manifest readbacks. Core and Controller retained
the same run in `needs_attention`; a direct signed provider read showed a
complete 1642-byte manifest, two 102,432,768-byte artifacts and matching
intent, with no abort marker. After direct access returned, Core's periodic
member reconciliation autonomously settled that one run and catalog point
`succeeded/published` without recapture or restart. Controller exact replay
returned HTTP 200 succeeded; the manifest and both artifact SHA-256 values
matched before and after settlement, and all 27 earlier catalog ID/hash
pairs stayed unchanged. The exact intent remains as a small remote record;
this is not an uncataloged point or incomplete upload. The proxy's response
and read faults were fixture-only; single-process provider and abrupt crash
limits remain.

## S3 final publication versus abort marker ordering

After the S3 native final-denial and lost-reply checks, an independent source
review found another interleaving: a delayed final PUT could commit after
Core wrote a separate abort marker but before the final-key tombstone. The
valid point would then be rejected by readback and catalog rebuild because
the abort marker remained. A new fake-S3 test forced the final PUT to win at
the tombstone request; it failed before the correction with a retained-work
conflict on the valid point. Core now places/verifies the conditional final
tombstone before the abort marker. The same test then passed and the point
remained readable/adoptable. Existing abort-wins, delayed artifact and
partial-fence retry checks passed; focused race passed. An independent
read-only correction review found no concrete issue. A later matched two-Core
S3Proxy 4.1.1 fixture used a private transport gate on the latest source. Two
artifacts uploaded fully, then the gate held the final conditional PUT before
the provider, returned HTTP 503 on its retries and on three readbacks, and
allowed two genuine provider 404 reads while the final was absent. Core then
issued the exact-key abort tombstone. The gate released final first: provider
HTTP 200; the tombstone followed and got HTTP 412. The member run and
Controller task succeeded under their original ID; exact Controller replay
returned HTTP 200. Core cataloged one new point. Its final manifest and both
artifacts matched signed provider readback, with no abort/delete marker or
tombstone. All 28 older catalog ID/hash pairs, the protected old point's
manifest/artifact hashes and both source disk hashes stayed unchanged. The
gate did not inject a crash or prove a multi-process S3 provider; forge CI
remains open. Its raw timing receipt remains in the ignored private fixture.

A separate latest-source operation forced the symmetric order. After two
complete artifact uploads, the gate held the final PUT, returned three HTTP
503 readbacks and allowed two genuine provider 404 reads. The exact-key
tombstone then committed first (HTTP 200); the delayed final PUT was refused
(HTTP 412). Core and Controller recorded one terminal failed operation with
no new catalog point. Exact replay returned the same failure; the catalog
still contained exactly 30 prior ID/hash pairs. Signed provider readback
showed the final-key abort tombstone and no intent, while protected remote
hashes and both source disk hashes were unchanged. The provider is the same
single-process S3Proxy fixture and the timing gate is private; no abrupt
crash or multi-process provider behavior was tested.

## NFS and SMB3 uncertain publication correction

Source review found that a filesystem point could be renamed into place
before a parent sync error, while Core terminal-failed the run and omitted
the complete point from its catalog. The regression first reproduced that
failure with a real point followed by a synthetic lost sync reply. Core now
marks rename and later sync failures as uncertain, reads back the exact point
and settles only from proof. A second case forces a real missing staged
artifact before the rename and verifies one terminal failed run and no
published point. Focused agent/vmhost tests and serial full Go passed;
Python passed 571 tests with one skip, and web test/typecheck/build passed.
API generation, documentation structure (141 uncommitted paths), privacy and
diff checks passed. Independent read-only review of the correction found no
remaining concrete finding. A matched disposable two-Core NFS fixture then
ran an exact fresh member backup with a private source overlay that returned
an uncertain lost-reply error after the native final rename and successful
parent sync. Controller and Core reported one succeeded published run and
catalog point; exact replay returned HTTP 200 and the same result. The remote
point held a final manifest and two 102,432,768-byte artifacts with no
staging directory. All 24 prior catalog ID/hash pairs, two protected NFS
manifest hashes and the two source disk hashes stayed unchanged. Overlay
source diff, fingerprints and private run/catalog/replay/remote/hash receipts
were collected. This simulates the lost reply after a real rename and sync;
it does not simulate an actual NFS sync failure. Forge CI remains open.

In the same matched NFS fixture, a second fresh member backup used a private
overlay to return a post-sync lost reply and then an exact-ID published-point
read error. Controller first showed `needs_attention`. It was stopped before
the normal-source Core binaries restarted; while Controller remained off,
both member and master journal/run records autonomously settled
`succeeded/published`. A fresh Controller adopted the succeeded task and
exact replay returned HTTP 200 for the same operation and one point. All 25
prior catalog ID/hash pairs, protected manifest hashes and source disk hashes
remained unchanged; no fixture process remained after shutdown. This proves
the selected Core member publication reconciliation with a simulated
readback fault, not an actual NFS server read failure.

A later matched SMB3 member used an ignored exact-operation overlay on the
latest-source binary. The real SMB final directory rename and parent sync
succeeded, then the overlay returned an uncertain lost reply. Core read back
the exact point and settled one member run and catalog point as
`succeeded/published`; Controller succeeded and exact replay returned HTTP
200 with the same result. The SMB server held the final manifest and two
complete artifacts, with no staging directory; manifest SHA matched the
catalog. All 29 prior catalog ID/hash pairs, the protected SMB control's
manifest/artifact hashes and both source disk hashes stayed unchanged. This
simulates a lost reply after successful SMB sync, not an actual SMB server
sync failure. Exact replay still succeeded after restoring the normal binary
and restarting Core; the private overlay was removed, and every SMB fixture
process was stopped. Raw receipts remain in the ignored private fixture.

## Live browser completion on disposable S3 fixture

The real Controller browser showed Core2 access Unknown immediately and at
65 and 135 seconds after only Core2 stopped, while Core1 stayed Accessible.
After Core2 restarted, both hosts showed fresh Accessible timestamps on the
S3 repository row. The browser created one new policy on a designated stopped
VM (HTTP 202), set a future weekly schedule and immediately paused it (HTTP
202); no existing policy changed.

The browser registered one new S3 repository ID on an unused prefix using a
credential kept in a private fixture file. Core inventory showed exactly one
new generation-1 row; UI Test access returned 202, then health was online,
master access good and member access readable/writable. The browser's first
script sampled too early to capture the credential and registration response
codes; the secret was never printed or included in captures. A later read-only
reopen of Create → S3 showed blank Access key ID and Secret access key fields,
with no secret in that form's DOM; Cancel submitted nothing. This verifies a
new form after reload, not the immediate post-POST field state.

On the paused disposable policy, the browser dropped only the accepted policy
switch response after HTTP 202. It showed an uncertain task and Check task
status reported queued; Core revision advanced 2 to 3. A reverse switch with
the same response fault returned HTTP 200 on exact UI retry with identical
request ID/body; Core advanced exactly 3 to 4 and remained paused.

The browser test-restored a designated, unprotected S3 point (POST 202) to a
new stopped zero-NIC two-disk VM. Independent lab inspection found one Core
test-restore pin and both expected disk markers; source and prior catalog
hashes were unchanged. The cleanup panel disabled submission for blank or
wrong IDs and enabled it only for that exact new VM ID. Cleanup POST 202
settled succeeded. Core then reported zero pins, no test VM/libvirt entry or
two-disk directory; exact cleanup replay returned HTTP 200. The designated
recovery point, all 28 prior catalog ID/hash pairs and source hashes stayed
unchanged. Captures and the exact cleanup request remain private in the
ignored disposable fixture. No live page or console errors were observed.

Actual master health timestamp expiry, live 403 (only response interception
was tested earlier), destructive overwrite Replace, and lost-response restore
were not exercised. The expected browser network abort displayed uncertainty.

A later matched latest-source browser fixture exercised those four states on
disposable resources. Removing `X-CSRF-Token` from one safe Test access POST
returned a real Controller HTTP 403 with `CSRF token required`; the page
displayed the error and Core's operation count did not change. This checks
the CSRF boundary, not role-scoped permission; the build has only
administrator sessions.

The browser sent Restore as new for a fresh stopped zero-NIC two-disk target,
dropped the accepted HTTP 202 response, showed uncertainty, then retried the
same ID/body and received HTTP 200 for the same succeeded operation. An
independent read-only mount verified both expected markers on the new VM;
the original 30 catalog ID/hash pairs and four original source disk hashes
were unchanged. Only that new VM was backed up to NFS. Its new point added
one catalog entry; independent provider readback found the original markers
and all prior hashes intact. With the VM stopped, only its two marker files
were changed to distinct sentinels before Replace.

In the Replace panel, blank and wrong typed names disabled Next; the exact
name enabled it. The browser submitted only the designated target with
`confirm_replace:true` and received HTTP 202. Core and Controller settled
success; exact replay returned HTTP 200 for the same operation. Libvirt
switched both active disks to new files. Independent mounts of those active
disks recovered both original backup markers and artifact hashes; the old
sentinel files remained detached as retained originals. The VM stayed shut
off with zero NICs. All 31 catalog ID/hash pairs and four original source
disk hashes stayed unchanged. An initial readback of the detached original
paths showed the sentinels; tracing the live libvirt paths resolved this
verification error, and no product defect was found.

For master health expiry, Core1 was genuinely off for 152 seconds while
Core2, storage and Controller remained live. The browser showed all 17
repository health badges Unknown and NFS master access Unknown while Core2
was Accessible. After Core1 restarted on the same binary, NFS returned
Online and both hosts showed fresh Accessible access. Private screenshots
were inspected; there were no page exceptions, aside from the expected 403
network response. Final independent checks kept the 31 catalog and four
original source hashes unchanged, the active replacement markers intact and
the target stopped with two disks and zero NICs. Browser and all fixture
processes were stopped; raw captures and receipts remain in ignored private
storage.

## NFS service interruption at upload and publication

A matched latest-source disposable NFSv4 fixture used an ignored exact-ID
timing barrier for each of two new member backups. For the upload cutoff, the
server held exactly 1,048,576 bytes of staged vda, no final point existed,
and that byte count stayed unchanged for five seconds with only `nfs-server`
stopped. After service return, the hard-mounted native copy continued and
settled one succeeded run and one published point; the final manifest and
both full artifacts were present, with no staging directory.

For the separate publication cutoff, the barrier paused after staged
directory sync and before final rename. The server held a complete staged
manifest and two 102,432,768-byte artifacts whose SHA-256 values matched
the source, with no final point. The stage stayed unchanged while NFS was
off. After service return, the same operation published one point. Each
operation kept its exact replay and result across Core restart. An older
protected two-disk point restored independently with byte-identical disk
hashes and both markers; its disposable VM was removed. The final catalog
contained the 31 prior ID/hash pairs unchanged plus the two new points,
and source disk hashes remained unchanged. The clean latest-source binary
was restored before final replay, and all owned fixture processes were
stopped. The barriers controlled timing; the actual NFS server stop/restart
and native hard-mount continuation were observed. This does not test a
prolonged outage terminal result or an actual NFS server sync failure.

## SMB3 service interruption and absent-publication correction

A separate latest-source SMB3 member fixture paused an exact new backup
after the first 1 MiB of staged vda had synced, with no final point. Stopping
only `smbd` closed port 445; the staged byte count stayed fixed for five
seconds after the barrier released. Core and Controller recorded one
terminal failed run with `host is down`, no point or stage, and exact replay
kept that result after Core restart.

A second exact operation paused after a complete staged manifest and two
102,432,768-byte artifacts with source-matching SHA-256 values, before final
rename. With `smbd` off, the bytes and absent final stayed unchanged. The
rename returned `host is down`; after service return, stage and final were
both absent, yet the run and Controller task remained `needs_attention`
through exact replay and normal-binary restart. All 33 earlier catalog
ID/hash pairs, protected SMB manifest/artifact hashes and two source disk
hashes were unchanged. No new point was cataloged. The fixture was stopped
with the exact operation preserved; protected restore was withheld while
the recovery defect was investigated.

The source fix adds a durable exact-key filesystem abort marker only after
writable authority, catalog and stage checks. A focused absent-publication
regression failed before the fix with `needs_attention`, then passed with
terminal failure and a fence that rejects a late directory rename. The test
also checks catalog rebuild skips the complete marker, interrupted marker
completion, a valid published point, foreign stage, FIFO and restricted
host. Focused race passed on the first correction. Independent review found
stale-authority verification writes and a FIFO hang in that correction;
both were fixed. Final read-only correction review found no remaining
concrete blocker.

A fresh matched capability-only Core build then restarted the preserved SMB
fixture. Without a new backup request or recapture, periodic Core
reconciliation settled that same operation terminal failed. The final path
held a complete 65-byte `nex-backup-abort:<ID>` marker and no staging
directory. Authenticated catalog rebuild succeeded with exactly the prior
33 point ID/manifest-hash pairs and no new point. A fixture-only same-ID
directory rename against the marker failed ENOTDIR on SMB3; marker inode
and SHA stayed unchanged and only the test stage was removed. Exact replay
returned HTTP 200 for the same failed operation before and after Core
restart. An older protected SMB point restored to a stopped zero-NIC
two-disk test VM; both restored disk SHA-256 values and offline markers
matched the retained artifacts. Exact restore replay and typed-ID VM/disk
cleanup passed. Protected manifest/artifact and source disk hashes stayed
unchanged; all fixture processes were stopped. These checks used a private
timing barrier and short service stop, not an actual server-side fsync fault;
forge CI remains open.

## S3 service interruption at upload and final manifest

A matched latest-source disposable fixture used pinned S3Proxy 4.1.1 behind
private TLS and an exact-ID timing gate. Signed provider probes confirmed
conditional create, duplicate refusal, wrong-ETag delete refusal and a
tombstone blocking a delayed final write. The original 33 catalog ID/hash
pairs and protected/source hashes matched the preflight baseline.

For the upload fault, the gate forwarded 1,048,576 bytes of a multipart vda
part upstream before S3Proxy was actually stopped. Core recorded
`needs_attention` under provider HTTP 503; exact Controller replay returned
HTTP 200 for the same ID. After provider recovery and matched Core restart,
that run settled terminal failed with no catalog point. The provider retained
only small incomplete/fence metadata, not full qcow artifacts. The barrier
proves bytes sent upstream, not that the partial multipart part was durable.

For a separate final-manifest fault, both 102,432,768-byte artifacts were
durable at S3Proxy with SHA-256 values matching the source, while the final
manifest was absent. The provider stopped before the held final PUT was
released. Core retained `needs_attention` under HTTP 503 and exact replay
kept the same ID; provider recovery plus Core restart settled terminal
failed with no new point. Authenticated S3 catalog rebuild succeeded with
the exact 33 original ID/hash pairs. A protected two-disk point restored to
a stopped zero-NIC VM with both artifact/disk and offline marker hashes
matching; exact replay and typed VM/disk cleanup passed. A previously
accepted restore request for the upload-incomplete ID terminal-failed with
`backup operation not found`, leaving no Core run, pin, VM or disk. No
restore request was made for the final-incomplete ID. Final source/control
hashes stayed unchanged and all fixture processes, including S3Proxy, were
stopped. These results are bounded to the fixture-only timing gate and
single-process filesystem S3Proxy, not a multi-process or cloud provider.

## SFTP service loss before final rename

A matched latest-source disposable fixture used one ignored exact-ID pause
immediately before the native SFTP final-manifest rename. The selected
repository's isolated OpenSSH service was separate from management SSH. At
the pause, the staged manifest was 1,648 bytes and both 102,432,768-byte
artifacts had source-matching SHA-256 values; no final manifest existed.
The lab killed only that OpenSSH service cgroup, then released the pause.
The final remained absent; Controller and Core retained the same operation
in `needs_attention` with connection refused, and exact replay returned
HTTP 200. After service restoration and restart on the normal latest-source
Core, that same run terminal-failed with `connection lost`. The remote
partial directory, staged/final manifest and abort record were absent;
the catalog kept exactly the prior 33 ID/hash pairs.

An older protected SFTP point restored to a stopped zero-NIC two-disk VM.
Both restored disk hashes and offline marker hashes matched the retained
artifacts; exact replay and typed cleanup passed. An incomplete-point
restore probe terminal-failed with `backup operation not found` and left no
VM, disk directory, pin or durable Core run. Source/control hashes stayed
unchanged, repository access recovered online, and every fixture process
was stopped. The exact-ID pause controlled timing; the OpenSSH service kill
and recovery were real within the disposable fixture. This was not an
unassisted timing race.

## SMB3 and S3 explicit replacement on disposable targets

On the matched latest-source fixture, each kind separately restored a
protected point as a fresh stopped zero-NIC two-disk VM. Independent offline
readback verified both source markers. Each target was then backed up only
to its own SMB3 or S3 repository, creating one new point. While each VM was
off, its two disk markers were changed to distinct sentinels. Authenticated
exact-target `confirm_replace:true` requests succeeded through Controller
and Core. Libvirt switched each VM's active disk paths to new files whose
SHA-256 values and both markers matched that kind's new backup artifacts;
the old sentinel files were detached retained originals. Exact Replace
replay returned the same result, and typed-ID cleanup removed each test VM
and its disks. The original 33 catalog ID/manifest-hash pairs and source
disk hashes remained exact; the final catalog had 35 points, the two new
target backups, and zero restore pins. Protected SMB3 and S3 control hashes
were unchanged. All fixture processes and the pinned single-process
S3Proxy container were stopped. The target identity and confirmation were
checked in private API receipts; this slice did not repeat the browser's
typed-name interaction, which passed on NFS above.

## SFTP abrupt Core stop during partial prune

In an isolated SFTP repository with an older protected two-disk control, a
fresh unprotected point P published before policy Run now created Q with
`keep_latest=1`. A private exact-P hook paused the disposable Core after a
successful remote artifact unlink and directory sync. The server had a
durable `.nex-delete-P` record and intent, an empty but still present P
directory and intact Q. The hook reached a partial delete before directory
removal; file iteration had already removed the manifest and vdb before
the observed vda unlink. Core was then killed with its automatic restart
disabled and MainPID zero confirmed.

Restarting the normal latest-source Core autonomously settled that same Q
run and Controller task succeeded. P's directory and intent were gone, its
durable deleted record remained, and Q plus the protected control stayed.
Exact Run-now replay returned HTTP 200. Authenticated catalog rebuild
succeeded with all original 35 point ID/manifest-hash pairs unchanged and
one added Q point (36 total); pins were empty. A protected-control test
restore made a stopped zero-NIC two-disk VM whose qcow2 and offline marker
hashes matched the retained artifacts; typed cleanup passed. Source disks
stayed unchanged. All fixture processes were stopped. The private hook
selected the exact post-unlink boundary; the Core SIGKILL and restart were
real within the disposable fixture.

## S3 abrupt Core stop during partial prune

The pinned single-process S3Proxy fixture began with 36 unchanged catalog
ID/manifest-hash pairs and an older protected two-disk control. Fresh
unprotected point P published, then policy Run now published Q with
`keep_latest=1`. A private exact-P hook stopped the disposable Core after
deleting P's vda object. Signed provider readback at that boundary showed
the durable `.nex-delete.json` record (2,240 bytes) and remaining vdb,
while P's manifest and vda returned 404. Q's final manifest and both
artifacts were already present. Core was then SIGKILLed with automatic
restart disabled; MainPID zero and unchanged provider objects were checked.

The normal latest-source Core restarted and reconciled that same Q run and
Controller task to success. Signed listing showed P empty and Q intact;
exact replay returned HTTP 200 and authenticated catalog rebuild succeeded.
The original 36 ID/manifest-hash pairs stayed exact, with only Q added (37
total). A protected S3 point's signed manifest, artifact and protection
hashes stayed unchanged. Its test restore yielded a stopped zero-NIC
two-disk VM with both qcow2 hashes and offline markers matching; typed
cleanup passed and pins were empty. Source disks stayed unchanged and all
fixture processes and the S3Proxy container were stopped. The exact hook
selected the partial-delete boundary; the Core SIGKILL was real within the
disposable fixture. Provider evidence is limited to S3Proxy's single-process
filesystem backend.

## Interrupted S3 catalog rebuild

An isolated S3Proxy repository/prefix held one newly published protected
two-disk control. The original 37 catalog ID/manifest-hash pairs remained
untouched. Only the new point's Core catalog index was moved to a private
in-guest stash; signed provider reads showed the remote manifest, both
artifacts and protection record intact. An ignored exact-repository hook
paused Core immediately after rebuild scanned the point and durably saved
its catalog entry, before the operation settled. At the pause, that entry's
SHA-256 matched the stashed copy, the catalog contained 38 indexes and all
37 originals still matched. Core was SIGKILLed with automatic restart
disabled and MainPID zero confirmed.

Restarting the normal latest-source Core reconciled the same rebuild
operation to success; exact Controller replay returned HTTP 200. The catalog
still held exactly the original 37 entries plus the single control point,
with all 38 index hashes unchanged. Signed remote hashes were unchanged.
A stopped zero-NIC two-disk test restore matched both artifact hashes and
offline markers; typed cleanup cleared its pin. Source disks were unchanged,
the private stashed copy was removed from the guest after recovery, and all
fixture processes and S3Proxy were stopped. The exact post-import pause was
a fixture timing aid; the Core kill and normal restart were real within the
disposable lab.

## Interrupted SFTP catalog rebuild

A new isolated SFTP repository held one protected two-disk control point.
Only that point's Core index was moved to a private in-guest stash; the
original 38 index hashes and remote control files were unchanged. An ignored
exact-repository hook paused Core after the SFTP scan and durable index save,
before rebuild settled. The recreated index matched the stashed SHA-256 and
all 39 indexes matched at the pause. Core was SIGKILLed with automatic restart
disabled and MainPID zero confirmed.

The normal latest-source Core restarted and reconciled the same rebuild ID to
success; exact replay returned HTTP 200. Catalog inventory held the original
38 ID/manifest-hash pairs plus the control, with all 39 index hashes exact.
The protected test restore produced a stopped zero-NIC two-disk VM whose
artifact hashes and offline markers matched. Typed cleanup cleared the pin;
source disks and remote control hashes remained unchanged. The stash was
removed from the guest after recovery, and all owned fixture processes were
confirmed stopped. The hook was a fixture timing aid; the Core kill and
restart were real within the disposable lab.

## Interrupted NFS catalog rebuild

A new isolated NFS repository held one protected two-disk control point.
Only its Core index was stashed privately; the 39 older index hashes and
server control files stayed unchanged. An ignored exact-repository hook
paused Core after the NFS scan and durable index save, before the rebuild
settled. The recreated index matched its stashed SHA-256 and all 40 index
hashes matched at the pause. Core was SIGKILLed with automatic restart
disabled. The normal latest-source Core reconciled the same rebuild ID to
success after restart; exact replay returned HTTP 200.

The final catalog contained the original 39 ID/manifest-hash pairs plus the
control, with all 40 index hashes exact. A stopped zero-NIC two-disk test
restore matched both artifact hashes and offline markers. Typed cleanup
cleared its pin; source disks and remote control hashes stayed unchanged.
All owned fixture processes were confirmed stopped. The hook selected the
crash boundary; the Core kill and normal restart were real within the
disposable lab.

## Interrupted SMB3 catalog rebuild

An isolated SMB3 repository held a new protected two-disk control point;
the prior 40 catalog indexes matched their preflight hashes. Only the new
index was stashed privately. An ignored exact-repository hook paused Core
after scanning the share and durably saving that index, before the rebuild
settled. The index and protected remote files matched pre-crash hashes, as
did all 41 catalog indexes. Core was SIGKILLed with automatic restart
disabled and MainPID zero confirmed. The normal latest-source Core settled
the same rebuild ID to success after restart; exact replay returned HTTP
200. All 41 ID/manifest and index hashes remained exact.

The protected test restore produced a stopped zero-NIC two-disk VM with
both original offline markers. Typed cleanup cleared its pin, and source
disk hashes remained unchanged. The ignored hook selected the boundary;
the Core kill and normal restart were real within the disposable lab.

## NFS corrupt-artifact rebuild and retry gap

An isolated NFS repository held a protected two-disk control and a fresh
unprotected two-disk point. The prior 41 catalog ID/manifest hashes and
source disk hashes matched baseline. After saving the unprotected point's
artifact and index privately, the lab changed one byte in only its remote
vda artifact. Authenticated restore failed on a disk digest mismatch with no
VM or pin. Only that point's Core index was then stashed. Authenticated
catalog rebuild entered `needs_attention` for an unreadable/invalid point;
the corrupt point was absent from the 42-entry catalog, which retained the
protected control and all 41 prior entries.

The remote artifact was repaired byte-for-byte with its saved SHA-256.
Controller exact replay and a normal Core restart left the retained task in
`needs_attention`. An authenticated Core-local request using the **same**
rebuild ID returned pending then succeeded, importing exactly one point
(43 total). All 41 old ID/manifest hashes, source hashes and protected files
remained exact. A protected test restore recovered both disk markers in a
stopped zero-NIC VM; typed cleanup cleared the pin. Controller still reported
the original task as `needs_attention` after Core succeeded. This is a
Controller-visible Core outcome convergence defect, not a passed end-to-end
retry. All owned fixture processes were stopped. The byte mutation and index
stash were disposable fixture actions; no protected point was altered.

On the matched BUG-0714 fix, the preserved old operation's successful Core
receipt reconciled its stale journal and Controller task at startup. Exact
Controller replay returned HTTP 200 and all 43 then-existing catalog hashes
were unchanged. A fresh unprotected point was corrupted by one artifact byte:
restore failed on digest with no VM or pin, and rebuild stayed in attention
without importing it or changing the prior 43 entries. After byte-exact
repair, authenticated Core-local retry with the same rebuild ID returned
pending then succeeded. Without restarting Controller, its retained task and
Core's receipt and journal all became `succeeded`; Core and Controller exact
replays returned HTTP 200. The catalog added exactly that repaired point
(44 total). A protected two-disk test restore matched both markers and disk
hashes in a stopped zero-NIC VM; typed cleanup cleared pins. Source hashes
were unchanged, the temporary lab root account was relocked to its exact
prior entry, and all owned fixture processes were stopped. This is bounded
native NFS evidence, not SMB3/S3 corruption coverage.

## SMB3 corrupt-artifact rebuild refusal and repair

On the matched BUG-0714 build, an isolated SMB3 repository held a protected
two-disk control and a fresh unprotected two-disk point. One byte of the
unprotected vda artifact was changed after saving its exact bytes privately.
Authenticated restore failed on the disk digest without creating a VM or
pin. With only that point's Core index stashed, authenticated rebuild entered
`needs_attention` for the invalid point and retained exactly the 44 prior
catalog ID/manifest hashes; the corrupt point was absent.

The artifact was restored byte-for-byte. An authenticated Core-local retry
with the same rebuild ID returned pending and settled success; Core receipt
and journal plus the live Controller task converged without a Controller
restart. Core and Controller exact replays returned HTTP 200. The catalog
added one repaired point (45 total), while older hashes stayed exact. A
protected two-disk test restore matched artifact hashes and both offline
markers in a stopped zero-NIC VM; typed cleanup cleared pins. Source and
protected remote hashes stayed unchanged, the temporary lab root account
was relocked to its exact prior entry, and all owned fixture processes were
stopped. This is bounded native SMB3 evidence on the selected Samba profile.

## S3 corrupt-artifact rebuild refusal and repair

An isolated prefix on the pinned single-process S3Proxy 4.1.1 profile held
a protected two-disk control and a fresh unprotected two-disk point. A
fixture-only one-byte change to the unprotected point's backing object was
confirmed by signed GET with a changed SHA-256; protected objects were
untouched. Authenticated restore terminal-failed with a generic conflict
from S3's artifact size/digest check and created no VM or pin. Only that
point's Core index was stashed. Rebuild entered `needs_attention`, excluded
the corrupt point, and retained the prior 45 catalog ID/manifest hashes
plus the protected control.

After byte-exact repair, signed GET matched the saved original SHA-256.
Authenticated Core-local retry with the same rebuild ID returned pending
then succeeded; Core receipt, journal and live Controller task converged
without a Controller restart, and exact Core/Controller replays returned
HTTP 200. One repaired point was imported. Final catalog held the original
45 ID/manifest hashes plus the control and repaired point (47 total).
Signed provider manifest, artifact and protection hashes matched their
pre-corruption values. A protected test restore recovered both markers in a
stopped zero-NIC VM; typed cleanup cleared pins and source hashes stayed
unchanged. The direct backing-object byte mutation was a disposable fault
injection, not normal S3 client behavior. The selected S3Proxy profile does
not establish other providers' semantics; its generic conflict message is
less specific than the observed digest mismatch. All owned fixture processes,
the TLS proxy and S3Proxy container were confirmed stopped.

## Cross-pool NFS owner registration race

Two disposable masters with distinct pool and host IDs ran the same matched
Core binary. They submitted authenticated registrations with different
repository IDs against one new empty subdirectory of the disposable NFS
export within about 1.3 seconds. One registered; the other returned HTTP
503 and retained that refusal on exact-ID replay, with zero repository or
point. The losing master separately registered a fresh scratch NFS target,
proving its own admission path was usable. The contested root held exactly
the winner's owner record, whose SHA-256 stayed unchanged. The source error
was `backup repository is owned by another pool`; the local API presented a
generic unavailable result. The plan does not require HTTP 409 for this
conflict; the generic message is an operator-diagnostic limit.

The winner then published one protected stopped two-disk point and restored
it as a new shut-off zero-NIC VM. Both offline disk markers and full restored
artifact hashes matched. Typed Controller VM deletion and disk cleanup
passed. The 47 older catalog index hashes and both source disk hashes stayed
unchanged; the new catalog count was 48. This is a native Core cross-pool
owner gate on one NFS profile, not a claim about arbitrary external exports.
Both temporary root API accounts were relocked and all owned fixture
processes were confirmed stopped. The storage guest was stopped before the
Core guests during lab teardown, causing a bounded systemd mount-stop wait;
that fixture sequencing did not affect the completed backup/restore result.

## Cross-pool SMB3 owner registration race

The same two disposable independent pool masters submitted concurrent
Controller registrations using distinct repository IDs and fresh scoped
credential receipts against one new empty directory of the disposable SMB3
share. Both Controller requests were queued with HTTP 202; Core settled
only one registration. The loser reported `backup repository is owned by
another pool`, retained that failed task on exact Controller replay and had
no repository or point. The winner's sole owner record and SHA-256 remained
unchanged after replay. No protected older share directory was contested.

The winning pool created a stopped zero-NIC two-disk source through the
product API, saved distinct 4 KiB markers and source disk hashes, and
published one protected backup point. Independent restore-as-new produced
a shut-off zero-NIC VM with both full disk hashes and markers matching.
Typed cleanup removed the restored VM and disks. The original pool's 48
catalog indexes and source disks, plus the winning pool's source disks,
retained their preflight hashes. This proves the selected Samba share's
cross-pool owner fence and usable winner restore, not arbitrary SMB servers.
All owned fixture processes were confirmed stopped before S3 setup. The
disposable second Core guest required QMP quit after completed work because
Debian's unrelated pacemaker stop advertised a 30-minute timeout; its disk
and state were preserved.

## Cross-pool S3 owner registration race

Two disposable masters with distinct pool IDs and a matched Core binary
used separate scoped credential receipts to race repository registrations
against one fresh absent prefix of the pinned single-process S3Proxy 4.1.1
profile. One Core registration succeeded; the other settled failed with
`backup request conflicts with retained work`. Exact loser replay retained
the failure and its pool had no repository. Signed provider GET returned
the sole `.nex-owner.json` record naming the winner's pool and repository,
with its SHA-256 saved. The API error is generic; the provider owner record
and absence of loser registration establish the selected owner fence.

The winner published a protected stopped two-disk point and independently
restored it as a shut-off zero-NIC VM. Both offline markers and full restored
disk hashes matched the source. This is native Core product evidence against
one disposable S3Proxy profile; it does not certify other object providers.
Typed cleanup removed the restored VM and disks. The original pool's 48
catalog indexes, the winner's earlier SMB3 index, both source VMs' disk
hashes and the owner object SHA-256 stayed unchanged. All owned fixture
processes, the TLS proxy and S3Proxy container were confirmed stopped.

## Cross-pool SFTP owner registration race

The two disposable independent masters used separate new pool-scoped
credential receipts, the pinned OpenSSH host key and one new empty confined
root, separate from all retained SFTP repositories. Concurrent Controller
registrations were both queued; Core settled only one registration as
successful. The loser failed with a conflict, retained that failure on
exact request replay and had no repository. The SFTP owner marker named
the winning pool and kept its original SHA-256 through loser replay.

The winner published and protected one stopped two-disk point, then
restored it independently as a shut-off zero-NIC VM. Both 4 KiB markers
and full restored disk SHA-256 hashes matched the source. Typed VM/disk
cleanup passed. The original pool's 48 catalog hashes and both source
hashes, the winning pool's two older point hashes and source hashes, and
the isolated SFTP owner marker remained byte-identical. Only the winning
point appeared under that new remote root. This is native Core proof on
the selected confined OpenSSH profile. All owned fixture processes were
confirmed stopped. The race synchronized HTTP submissions; it cannot prove
both owner-file claims occurred at the same lock microsecond. The same
timing limit applies to the other three cross-pool races.

## NFS restore-copy pin versus member retention

An isolated NFS repository held a protected control and unprotected P and
Q from the same stopped two-disk VM on the pool member. A paused
`keep_latest=1` policy targeted that VM and repository. A private exact-P
timing hook paused the master restore after it had a durable P pin and
synced 4,096 bytes into its first local restored disk. While the copy
remained at that byte count, an authenticated Core-local Run-now dispatched
to the member, published R and succeeded. The master catalog then held the
protected control, pinned P and newest R; only older unpinned Q was pruned.
The member backup settled while the restore copy was genuinely paused.

After release, the original Controller restore task succeeded. Its shut-off
zero-NIC two-disk VM had both ext4 markers and both full 96 MiB payload
SHA-256 hashes matching the member source. Exact Core-local R replay and
Controller restore replay retained their succeeded operations. Typed test
cleanup removed the VM and disks and cleared pins. The 48 catalog hashes
preceding this isolated sequence and source qcow2 hashes stayed exact; Q
was the only point removed from its 51-point pre-overlap set, replaced by
R. The master was restored to the normal matched binary, temporary root
authentication was relocked, the timing hook removed, and all owned fixture
processes were stopped. The hook selected timing; the backup, pin, prune
and restore ran through native Core paths.

## SMB3 restore-copy pin versus member retention

An isolated SMB3 share directory held a protected control and unprotected
P and Q from the same stopped two-disk VM on the pool member. Both Cores
ran the matched build before the master-only private exact-P timing hook.
The hook paused a real restore copy after the durable P pin and 4,096 synced
bytes of its first local disk were present. During that pause, an
authenticated Core-local member Run-now published R and succeeded. The
master retained the protected control, pinned P and newest R and pruned
only older unpinned Q; the partial copy and pin stayed in place until
release.

The original Controller restore task then succeeded. Its shut-off zero-NIC
two-disk VM matched both ext4 markers and full 96 MiB source payload hashes.
Exact R and restore replays retained their results. Typed cleanup removed
the restored VM, disks and pin. The 51 catalog hashes preceding this
isolated SMB3 sequence, protected SMB3 control/P hashes and source qcow2
hashes stayed exact. The master returned to the normal matched binary,
temporary root authentication was relocked, the timing hook removed and all
owned fixture processes were stopped. The hook selected the timing; native
Core performed the member backup, prune, pin and restore.

## S3 restore-copy pin versus member retention

The first isolated S3Proxy attempt held a durable P pin and 4,096 synced
restore bytes while a member backup published R and pruned only unpinned Q.
After a long zero-byte pause in the fixture's GetObject response, the restore
failed with `unexpected EOF`. Exact replay retained that failure; Core cleared
the pin and left no VM or disk. This attempt does not prove a completed restore.

A fresh S3 prefix and API-created shut-off, zero-NIC two-disk source kept the
same native path but used sparse marker disks with about 1.7 MiB per artifact.
The private exact-P hook paused GetObject after a durable P pin and 4,096
synced local bytes. During the 20.3-second pause, an authenticated Core-local
member Run-now published R, pruned only Q, and retained protected control,
pinned P and newest R. Releasing the original copy let the Controller restore
succeed. Its shut-off zero-NIC VM had both expected disk markers; both full
restored qcow2 SHA-256 hashes matched the provider artifacts. Exact R and
restore replays stayed stable, and typed cleanup removed VM, disks and pin.
The original catalog hashes, both source VM disk sets and 12 old/new S3
artifacts matched their earlier hashes or signed manifest digests. Core
returned to the normal matched binary, temporary root access was relocked,
the hook was removed, and all selected fixture processes stopped. The hook
selected timing; native Core performed the copy, pin, backup and prune. The
selected single-process S3Proxy profile and short pause do not establish
behavior for a different provider or a continuously slow object stream.

## SFTP accepted Controller reply loss

On the matched two-Core build, a fresh confined OpenSSH repository held a
protected stopped two-disk control point. A private localhost relay forwarded
one new backup POST to authenticated Controller, observed HTTP 202 upstream,
then closed the caller connection without forwarding any response byte. The
caller observed a disconnect. Direct retry of the identical request body and
ID returned HTTP 200; Core and Controller retained one succeeded operation,
one backup run and one new point. A later exact replay remained stable.

The protected control restored to a shut-off zero-NIC two-disk VM with both
ext4 markers and full qcow2 SHA-256 hashes matching its remote artifacts.
Exact restore replay and typed VM/disk/pin cleanup passed. Sixty prior catalog
hashes and both source disk hashes stayed exact. All selected Core, storage,
Controller and switch processes were stopped. The relay selected only the
caller-visible reply boundary; Core backup and restore were native. This
does not exercise a lost server-side SFTP final-rename reply, which has
separate adversarial source-level coverage.

## SMB3 accepted Controller reply loss

A separate fresh SMB3 share directory held a protected stopped two-disk
control point on the matched Core build. The private localhost relay
observed HTTP 202 from Controller for one fresh backup POST, forwarded zero
response bytes and closed the caller connection; the caller observed a
disconnect. Exact retry with the same body and request ID returned HTTP 200,
as did terminal replay. Core retained exactly one succeeded run and point.
The protected control independently restored to a shut-off zero-NIC VM with
both ext4 markers and full qcow2 hashes matching the remote artifacts.
Typed cleanup removed the restored VM and disks. The 62 older catalog
ID/hash pairs and both source disk hashes stayed exact. This proves the
caller-visible API reply boundary on the selected Samba profile; the relay
did not alter Core publication or the server's SMB reply.

## Current-candidate interrupted SMB3 restore

The healthy SMB3 fixture then submitted one new restore of its protected
two-disk point. A private exact-operation hook paused the master after its
Core point pin was durable and 4,096 first-disk bytes were copied and
synced locally. The disposable master Core process, configured not to
restart automatically for this fault, was SIGKILLed and confirmed absent.
After the hook was removed and the normal matched binary restarted, the
same restore ID autonomously reached `registered/succeeded` in Core and
Controller before any replay POST. Exact replay returned HTTP 200 without
a second VM. The single shut-off zero-NIC VM had both disk markers and full
qcow2 hashes matching the protected SMB artifacts. Typed cleanup removed
VM, disks and pin. All 64 catalog ID/hash pairs and both source disk hashes
stayed exact. The ordinary service restart policy was restored and every
selected fixture process stopped. The hook selected the byte boundary; the
Core kill, native resume, Controller convergence and cleanup were real.

## Registration preparation authority fence

Independent source review found that asynchronous new-repository preparation
could create an SFTP owner marker from a member or after a transfer freeze,
before the later master-only inventory commit refused it. The corrected
shared Core path checks exact master membership and writable authority under
the backup authority lock through bounded native preparation, serializing
with transfer freeze for all four kinds. SFTP adoption with an established
remote identity now refuses a missing owner marker. Focused member,
freeze-race and missing-owner tests failed before their respective fixes and
passed afterward; focused SFTP/peer/registration tests and diff check passed.
Independent read-only correction review found no blocker. A fresh two-Core
fixture ran matched binary SHA-256
`af5f3ed6e16f5f410515d9ba30ac5ddf84fca816c975a1acabcff364e87b994c`.
Master registration created the exact owner marker; the member's periodic
probe reported readable and writable for generation 1. A protected stopped
two-disk SFTP point backed up and restored independently to a shut-off
zero-NIC VM with both disk markers and full artifact hashes matching.
Registration, backup, protect, restore and cleanup replays were stable;
cleanup removed VM, disks and pin. Sixty-four prior catalog hashes and both
source disk hashes stayed exact, and all five fixture processes stopped.

A Core-local member attempt to register a separate absent SFTP root returned
HTTP 503 on initial and exact replay without creating the root, owner/probe
files, receipt or run. Controller refused a member-scoped credential handoff
with HTTP 409, so the native member request used a mismatched master receipt;
this attempt does not prove a valid-credential member path. The focused
regression proves Core's master check precedes credential preparation. The
lock can delay freeze up to the preparation timeout; a same-pool owner
claimed before freeze can remain uncommitted if freeze wins before inventory
commit, with no point or rival-pool write. This selected OpenSSH profile does
not certify other SFTP servers.

After BUG-0715's shared registration correction, serial
`GOMAXPROCS=2 go test -p 1 ./... -count=1` passed all Go packages.
After BUG-0714, `python3 -m unittest discover -s tests` passed 571 tests
with one skip; web
`npm test`, `npm run typecheck` and `npm run build` passed. API generation,
documentation structure (144 uncommitted paths), repository privacy and
`git diff --check` passed then; documentation structure (145 uncommitted
paths), privacy and diff checks passed again after BUG-0715 documentation.
These are local source checks, not a forge CI run. Re-run the complete gate
after capability enablement and final documentation edits.

## Enabled capability and current session checks

The tracked Core now advertises SMB3, S3 and SFTP alongside NFS. A focused
capability test passed with the Go cache redirected to a writable temporary
directory. The guide and API reference were updated for all four kinds; an
independent read-only review found one stale historical SFTP sentence in the
handover, which was corrected. After the guide and roadmap edits,
`check_docs.py --worktree`, the repository privacy test, API generation check
and `git diff --check` passed. The website Svelte check and production build
passed using cached dependencies installed offline. The Controller web test,
typecheck and build had passed just before these documentation edits.

The current session denies even creation of a loopback socket with
`Operation not permitted`. A full Python test attempt ran 568 tests and ended
with 15 errors, two failures and one skip: socket-based fixtures, GPG agent
startup and systemd socket options could not run in this sandbox. Redirecting
the Go cache removed an unrelated read-only cache error but did not remove the
socket failures. The final enabled-build Core/Controller lab, real-clock missed
schedule, live browser walkthrough, and full socket-dependent suites have
therefore not run here. No fixture was started or altered by this attempt.
The final serial Go suite also failed before affected package tests could run:
`httptest` could not create loopback listeners in agent, Controller, Core-local,
VM host and websocket packages. Other Go packages reached their tests and
passed. `GOCACHE=/tmp/nex-backup-go-build GOMAXPROCS=2 go test -p 1 ./...
-run '^$'` then compiled every Go test package successfully without executing
tests. This is an environmental block, not a passing full-suite result.
