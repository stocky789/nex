# Pool removal and rename convergence (VLAN networks and imported ISOs)

Date: 2026-09-19

## Environment

Uncommitted working tree on top of commit `5b094ab`, on the developer workstation. No Nex Core
host, no Controller deployment, no pool, no lab VM and no physical network were involved: no
build artifact, ISO or installed product version was exercised, so no product version is
identified here beyond the changelog heading the tree carries (0.3.5). The working tree also
carries unrelated in-flight work (ZFS provisioning, storage projection), so the package test runs
below cover that code as well as this slice.

## What changed after the first drafts of this note

The slice went through two independent review rounds. The first found five safety defects and the
second re-verified the fixes and found one more. All are fixed in the tree these runs cover:

1. **A master could serve a short pool list from an incomplete managed journal.** `AppliedNetworks`
   and `AppliedMedia` now fail closed, and all six pool network/media peer operations answer `503`
   with the fixed body `pool resource list unavailable: managed journal is incomplete`. A member
   treats any non-200 as "remove nothing".
2. **A member could unlink the pool's single shared ISO file.** `media-remove` unlinks only what
   this host imported; an adopted record writes the tombstone and leaves the file.
3. **The bridge ownership proof matched a substring**, so a crafted network name could pass another
   network's proof. It is now an exact tail match.
4. **A tombstoned resource the master listed again retried for ever.** The member now refuses
   before `Execute` with `pool network|ISO <id> was removed on this host and is listed again by the
   master; remove it on the master and add it again`, and writes no journal entry.
5. **Unbounded journal growth** from per-tick operation IDs: converge operation IDs are now
   deterministic per action, resource, generation and UTC hour, and read-only removal dry runs
   (`CheckNetworkRemoval` / `CheckMediaRemoval`) catch predictable refusals without writing at all.
6. **A member without membership could hijack a Controller mirror row**: adoption now checks the
   row's recorded pool master.
7. **A failed pool VLAN registration reserved the network ID for ever** (BUG-0317), so a member
   whose uplink was wrong on the first pass never converged. A `failed` `network-register` for a
   pool VLAN no longer reserves the ID; `needs_attention` still does.

## Method

Checks re-run by the documentation author on this workstation after all of the above, with the
results recorded as observed:

- `/usr/bin/go test -race ./internal/protocol/... ./internal/vmhost/... ./internal/agent/... -count=1`
- `npm --prefix web test`
- `python3 -m unittest tests.test_api_contract`
- `python3 scripts/generate_api.py --check`
- `python3 scripts/check_docs.py --worktree`
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`

Checks recorded by the implementation lanes and the **independent reviewer** and **not** re-run
here (attributed, not reproduced):

- `/usr/bin/go build ./...` — exit 0 (fix lane and reviewer).
- `/usr/bin/go vet ./...` — exit 0, no output (reviewer).
- `/usr/bin/go test -race ./internal/controller/... -count=1` — `ok` 18.252 s (reviewer).
- `gofmt -l internal/` — names `internal/vmhost/hostconsole.go` and
  `internal/vmhost/hostconsole_font.go` only, both untouched by this slice and pre-existing
  (reviewer).
- `npx tsc --noEmit` in `web/` — exit 0, no output (reviewer).
- `TestGuestVLANNetns` under `unshare -Urnm sh -c 'mount -t sysfs none /sys && NEX_NETNS_TEST=1 …'`
  — **ran**, PASS 0.07 s, six real refusals logged (untagged NIC in ifupdown, missing uplink,
  bridge as uplink, VLAN device as uplink, VLAN owned by a host bridge, untagged NIC with an
  address). Developer workstation only (fix lane, re-run by the reviewer).
- The stubbed-API browser check (UI lane; see **Browser check** below).

The reviewer also ran two ad-hoc overlay tests outside the repository, since deleted: an
ownership-proof table over hostile network names (every name round-trips, none satisfies another
network's proof) — PASS; and a failed-then-retried `network-register`, which **failed**, producing
the BUG-0317 evidence (`hour one: status=failed err=uplink … does not exist on this host`,
`hour two: status=failed err=resource ID already reserved by another operation`). That defect is
fixed and covered by `TestFailedPoolVLANRegistrationCanBeRetried`.

## Results

- `/usr/bin/go test -race ./internal/protocol/... ./internal/vmhost/... ./internal/agent/... -count=1`
  — `ok` (protocol 1.037 s, vmhost 12.697 s, agent 8.539 s). These packages hold the tests
  `internal/protocol/managed_test.go` (`TestValidateResourceLifecycle`),
  `internal/vmhost/managed_removal_test.go` (tombstone projection, rebuild stopped, in-use
  refusals for a stopped VM, rename keeping source/uplink/VLAN, stale-generation refusal, media
  unlink and ENOENT, old-format journal, re-registration after removal, ownership proof) and
  `internal/agent/pool_removal_test.go` (member removes what the master no longer lists, a failed
  or foreign-pool list removes nothing, local-VM refusal with no journal write, stepwise rename,
  master pool-wide refusal and stale member inventory, single-host master, command routing).
  The fixes added, in the same packages:
  `TestAppliedListsFailClosedOnAnIncompleteJournal`,
  `TestIncompleteMasterJournalFailsThePeerListClosed` (drives the master's real peer handler over
  real peer TLS and asserts the member removes nothing),
  `TestMediaRemoveUnlinksOnlyWhatThisHostImported`,
  `TestOwnershipProofRejectsANameThatEmbedsAnotherNetwork`,
  `TestMemberRefusesAResourceItAlreadyRemoved`,
  `TestCheckRemovalReportsTheSameRefusalsAsTheOperation`,
  `TestRepeatedNativeFailureWritesOneJournalEntryPerHour` and
  `TestFailedPoolVLANRegistrationCanBeRetried` (BUG-0317). Each was recorded failing on the
  unfixed code by the lane that wrote it; the reviewer independently reproduced the ownership-proof
  and BUG-0317 failures.
- `/usr/bin/go test ./internal/vmhost -run TestFailedPoolVLANRegistrationCanBeRetried -count=1` —
  `ok` 0.009 s.
- `npm --prefix web test` — all seven suites pass, including `managed.test.mjs: ok` and
  `managed.test.mjs: pool resource removal ok`.
- `python3 -m unittest tests.test_api_contract` — 30 tests, OK, after this change added the two
  new routes, the `ResourceLifecycleRequest` schema, the three command kinds and the mirrored
  `NetworkRegistration`/`Media` fields to `docs/api/openapi.json` and
  `docs/api/peer-openapi.json`. The test was not edited. Before the reference was updated it
  failed with exactly the three assertions the Controller lane recorded (missing
  `delete`/`patch /api/v1/networks/{id}`, missing `ResourceLifecycleRequest`, missing
  `network-remove`/`network-update`/`media-remove` in `x-commands`). **This test asserts no error
  codes**, so it is not evidence for the new `503`: that was documented by hand on all six peer
  operations and checked by reading `internal/agent/pool_network.go` and `pool_media.go`.
- `python3 scripts/generate_api.py --check` — `index.html is current`, `local.html is current`,
  `peer.html is current` after regenerating with the same script. No HTML was hand-edited.
- `python3 scripts/check_docs.py --worktree` — passes over the whole uncommitted tree.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` — passes. Its
  known-pattern checks supplement manual review; they do not prove arbitrary data is safe.

### Network-namespace teardown test

`TestGuestVLANNetns` in `internal/vmhost/vlan_test.go` was extended with removal and, per the Core
lane's record, **ran** on this workstation under
`unshare -Urnm sh -c 'mount -t sysfs none /sys && NEX_NETNS_TEST=1 ./vmhost.test -test.run
TestGuestVLANNetns'`, covering: a bridge with a foreign port refused; removal with a second Nex
stanza present keeping the shared VLAN device; a second removal deleting it; a bridge with no Nex
stanza refused and left alone; and the uplink, an operator's own bridge, a bystander guest bridge
and `/etc/network/interfaces` left byte-identical. The independent reviewer re-ran it after the
fixes (PASS, 0.07 s, six real refusals logged). It was not repeated again for this note.

The skip guard was inspected and is unchanged: the test calls
`t.Skip("set NEX_NETNS_TEST=1 inside a disposable network namespace")` when `NEX_NETNS_TEST` is
empty, so it **skips** on CI runners that cannot `unshare`. The `-race` run above therefore did
not execute it. A namespace with dummy links is not a physical switch, a real uplink or an
installed Nex Core host.

### Browser check

The UI lane ran a throwaway Playwright script
(`.cache/pool-removal-convergence/ui-check.py`) against an esbuild build in
`.cache/pool-removal-convergence/dist`, with **every** `/api/v1/**` route stubbed — no
Controller, no Core, no pool. It asserted: Rename/Remove offered on a pool VLAN row and not on a
host bridge row; the member line rendering `ready` / `converging`; focus in the confirmation
field; the destructive button disabled until the name matches exactly; `Enter` in the field
sending nothing; a 409 rendered in the dialog alert with the row left in place; a repeat click
sending the **same** `request_id`; a 202 closing the dialog, showing the notice, leaving the row
and returning focus to the trigger; the rename PATCH body; `Escape` closing without a request;
`Remove ISO` only on a succeeded row with body `{request_id}`; and no page errors. Ten
screenshots were captured and inspected by that lane under
`.cache/pool-removal-convergence/ui/`.

`.cache/` is gitignored, so those captures and that script **are not in the repository and cannot
be re-inspected from a checkout**. This written record is the reviewable evidence. `web/dist` was
not rebuilt or modified.

## Limits

- **No native host.** Nothing ran on an installed Nex Core: no real guest bridge, 802.1Q
  sub-interface or `/etc/network/interfaces.d` stanza on a Nex Core installation was created or
  torn down, and no real ISO file was unlinked from real storage.
- **No two-host lab.** Member convergence, the master's pool-wide refusal and the stepwise rename
  are covered by in-process tests with a real peer handler and a real engine, not by two enrolled
  hosts over a network.
- **No version-skew test.** Older master, older Controller and older member behaviour is reasoned
  from the capability gate and from the peer contract, whose routes, methods and success shapes are
  unchanged (only the new `503` was added); no mixed-version pair was run.
- **The `503` was not exercised against two real hosts.** `TestIncompleteMasterJournalFailsThePeerListClosed`
  drives the real handler in process; no member on another machine has read it.
- **The browser check is simulated.** It proves request construction, gating, confirmation and
  keyboard/focus behaviour only — never Core teardown, member convergence or any host effect.
- **Controller package tests were not re-run here**; the Controller lane's `-race` run is quoted,
  not reproduced.
- The tree was not clean: unrelated uncommitted work is present and is covered by the same
  package runs.
