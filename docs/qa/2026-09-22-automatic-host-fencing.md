# Automatic host fencing verification

Date: 2026-09-22. Scope: slices 3, 4a, 4b and 4c of
[automatic host fencing](../plans/2026-09-22-automatic-host-fencing.md) — the continuous
non-destructive fence checks and the **Fencing** column, hardware watchdog detection and the pool's
**Host fencing** setting, the `sbd` watchdog engine with arming and the watchdog proof, and the
pool-owned Corosync/Pacemaker cluster.

**No physical hardware was involved at any point.** No physical management controller has been
contacted, no host has been powered off, and no physical watchdog of any model has been read, armed
or expired. Everything below is either a runnable check against fakes and fixtures, a browser review
driven from mocked API replies, or a **nested** lab of QEMU/KVM guests with an *emulated* watchdog
device. Every timing quoted is a nested-lab measurement and is not evidence about physical
hardware. The physical-hardware walkthrough is outstanding and this report is not acceptance of it.

## Build under test

Working tree on 2026-09-22 (uncommitted, alongside unrelated in-flight work from other sessions);
development product 0.4.3. Go toolchain and Node from the workstation's usual installs. The UI was
bundled with the repository's own esbuild options into untracked scratch directories, so `web/dist`
was never written. Lab guests were Debian 13 with corosync 3, pacemaker 3.0 and the distribution's
`sbd`.

## Executed checks

### Core, Controller and protocol

Run per slice and again for the whole feature at the end; the results below are the final run.

- `go build ./...` and `go vet ./internal/...` — passed.
- `go test -count=1 ./internal/...` — passed, all packages.
- `go test -race -count=1 ./internal/agent/ ./internal/vmhost/` — passed (28.0 s / 13.0 s).
- `gofmt -l internal/ web/src` — clean for every file this feature touched (the five it listed are
  other sessions' uncommitted files).
- `npx tsc --noEmit` in `web/`, and every `web/src/*.test.mjs` (11 suites) — passed.
- `python3 scripts/generate_api.py --check` — passed.
- `python3 scripts/check_docs.py --worktree` — passed ("181 uncommitted paths").
- `python3 -m unittest discover -s tests` — passed, 394 tests at the slice handoff (396 on the settled tree after the 2026-09-22 review fixes), including `test_api_contract.py`
  (74) and the repository-privacy suite.

What those Go and TypeScript checks cover, by slice:

- **Slice 3 — checks and the Fencing column.** A whole checking pass issues only reads (asserted per
  call and per pass: only `GET`s over Redfish, only `mc guid` and `chassis power status` over IPMI,
  and a laboratory `virsh` device never contacted at all); a member checks the master's controller
  with the credential unsealed in memory from its own signed replica and writes no credential to its
  own disk; the password never enters `argv`; each result is stamped with the profile it was taken
  against, so replacing a device drops the old device's successes; the pool's ready/total count is
  computed on the master and ignores ejected and joining hosts; a single-host pool reports that
  there is no other host to check from; a stale check is never readiness; twenty settled passes move
  neither the membership generation, the authority digest nor the durable record's bytes; a member
  cannot make itself ready over the real mutual-TLS peer route.
- **Slice 4a — detection and the setting.** Detection classifies seven device shapes from a fake
  sysfs tree and never opens a device node — asserted both behaviourally and by the absence of any
  `/dev` path literal in the detection source; a host's watchdog reaches the master's membership and
  twenty identical passes move neither generation nor digest; an invalid report is refused with 400
  and recorded nowhere; a member's report is never written onto another host's row; the **Host
  fencing** setting defaults to on, is recorded only by the master, is refused by a frozen
  authority, reaches the member's verified replica and is reported back; no host is ever ready
  through a watchdog in this slice; a two-host pool reports *not applicable*; the four prerequisite
  lines including an honestly unknown quorum; the Controller relays the command and mirrors the
  summary, checklist and watchdog verbatim, refuses an older master with 409 and makes no second row
  on retry; a fence credential is parsed rather than executed, with four shell-executable payloads
  refused.
- **Slice 4b — the engine, arming and the proof.** All three proof conditions and every refusal,
  driven through the real promotion path; the arming state machine including re-verification
  withdrawal, record-before-act in both directions, arming-pending, disarm-pending, a cluster that
  would not self-fence, a device another program holds, and a pool falling to two hosts; that an
  armed watchdog does not satisfy the fence-device prerequisite; the quorum-device column and the
  ring-change flap restarting the continuity clock; version skew both ways.
- **Slice 4c — the pool cluster.** The derived nodelist, node IDs and both cluster turns; the
  single-writer refusal in both directions; the unconditional cold-start disable; the settle gate;
  the rejoin rule and its agreement with readiness; the blocked-teardown report and its completion
  when the missing host returns; a hung `systemctl stop pacemaker` bounded at 20 s that never
  signals `sbd`; the foreign-bound gate; the four-host drain ordering; both clustered-storage
  interlocks; the pool-size ceiling; the management-network refusal; and the watchdog proof, its
  refusal before the bound and its grant after.

Revert checks were run for every regression test named in the slice handoffs: each fails with its
fix reverted and passes with it in place.

### Nested lab — slice 4b, the watchdog engine

Three Debian QEMU guests on a lab-local virtual switch, each with an emulated `i6300esb` watchdog
and `-watchdog-action reset`, running corosync, pacemaker and `sbd` with exactly the configuration
Nex Core renders. **The agent binary was never run in a guest**: the sequences are the commands Nex
Core issues, in its order, driven over SSH by hand. The code that decides *when* to issue them is
covered by Go tests only. All lab guests and directories were torn down afterwards.

| What | Real result |
|---|---|
| Debian package presets, captured after install and before anything disabled them | `sbd=enabled`, `corosync=enabled`, `pacemaker=enabled` — an unmanaged `sbd` would be pulled into the next cluster start, which is why the package's postinst now disables it |
| Detection against real guests | no driver link, identity `i6300ESB timer`, `nowayout=0`; classified as hardware by identity and then **emulated** by vendor, exactly as the Go code does |
| Arming three hosts | corosync, `sbd` and pacemaker active, watchdog active, **no boot ID changed** |
| Unrelated service restart on an armed host | **no reset** |
| Partition with `no-quorum-policy=freeze` | the minority host left the quorate partition and was **still running after two minutes** — `freeze` and a bounded self-reset are mutually exclusive |
| Partition with `no-quorum-policy=suicide` | left the partition at **8.6 s**, reset and rebooted by **15.7 s**; both survivors quorate and neither reset |
| Disarm by `systemctl disable` under a live armed cluster | **inert** — corosync, `sbd` and pacemaker all still active, every device still held |
| Disarm by `SIGTERM` under a live cluster | device released, host did **not** reset, cluster stayed quorate, but the unit ended `failed` on its `Requires=` edge. **No clustered filesystem was mounted**, which is the case that matters and the one not measured |
| Clean disarm run | three armed hosts released the device, none reset |

The lab also corrected two assumptions before any of the above: diskless `sbd` has no quorum source
without Pacemaker, and `sbd.service` is `RefuseManualStart` and exists only as a dependency of
`corosync.service` — so arming rides a cluster start and never a restart of a running cluster.

### Nested lab — slice 4c, the pool cluster

The same shape of lab, driven in the code's own order. All guests and directories were torn down.

| Scenario | Real result |
|---|---|
| Form three hosts, then arm one at a time | formed quorate with nothing armed; armed one host at a time; **no boot ID changed**; all three quorate at every step |
| Partition a member, no `stonith-watchdog-timeout` written anywhere | reset at **17.9 s**; both survivors quorate and unchanged; the host came back unarmed |
| Watchdog expiry with Pacemaker uninvolved | `SIGSTOP` on an armed host's `sbd` processes, pool quorate throughout so no `no-quorum-policy` could apply: the host **reset within 8 s** against a 5 s device timeout, survivors untouched, and its previous boot's journal ends mid-stream with no `emerg`, no panic and no shutdown trace — the signature of the device resetting the machine |
| A lone host starting the cluster with `suicide` already persisted and `sbd` disabled | **no panic**, boot ID unchanged over 165 s, inquorate with `WaitForAll`; the journal gives Pacemaker's own reason, *"Resetting no-quorum-policy to 'stop': cluster has never had quorum"*. Held with `stonith-enabled` both true and false |
| `stonith-enabled=false` | scheduler errors silenced, `have-watchdog` still follows `sbd`, and an armed host still self-fenced at **17.9 s**. Kept at `true` because every other partition measurement was taken under it |
| Host fencing turned off, and maintenance on one host of three | devices taken down one at a time, cluster up and quorate on all three hosts throughout, nobody reset |
| A host rebooting back into an armed pool | rejoined **unarmed**, nobody reset, re-armed afterwards |
| Whole-pool cold start; single-host reboot; a genuine three-host master partition | no reset loop; only the rebooting host's boot ID moved; the partitioned master reset at 17.9 s with two survivors quorate, no second reset |

### Browser review — mocked API replies

The real UI modules bundled with the project's esbuild and driven in headless Chromium through
Playwright against fixture data, with no Controller, agent or lab host contacted. Captures were
written to untracked scratch (`docs/qa/**/*.png` is gitignored, so they are described here rather
than committed) and every one was inspected. No console error in any run.

**Slice 3 — the Fencing column and the promotion advisory.** Five captures:

- *Mixed pool* — a seven-host pool's **Hosts** table with the new **Fencing** column between
  **Convergence** and **Actions**. A green `BMC · checked from 2 hosts` badge on the master with the
  fence device and *Management controller: configured by Nex Core at …* beneath it and an expanded
  *What each host found* disclosure listing each checking host and the time it last confirmed; an
  amber `BMC · stale` badge with *the last check from host-a is stale*; and four red *Not protected*
  rows each carrying Core's own sentence — the controller did not answer, the host has no local IPMI
  interface, an operator withdrew the device, and no other host has checked it yet. A grey
  `not reported` row for a master too old to report readiness. The pool line above and below the
  table reads *Fencing: ready for 1 of 7 hosts*, naming the rows that cannot be shut out.
- *Single-host pool* — a grey `BMC · no other host to check from` badge and the pool line
  *this pool has one host … so it needs no fence device until a second host joins*.
- *Recovery view* — master not reporting: grey `unknown` badges and the pool line
  *unknown — the pool master keeps this record and is not reporting. This does not mean these hosts
  cannot be shut out; it means nothing here knows.*
- *Promotion advisory* — the promotion verdict panel with an ok, a failing and a stale proving host.
  Each affected row carries two plain `small` lines, *Advisory: …* and *Nex Core proves the fence
  itself when the promotion runs; this stops nothing.*, with no callout box; the review script
  asserted the computed style has no padding, border or background. The yellow confirmation box
  below names every host the promotion powers off.
- *Fencing column, all states* — the same column with the loading and error states included.

Two defects were found this way in round one and fixed (a 64-character certificate fingerprint
overflowing the column; a duplicated sentence in the not-reporting cell), and one in round two (the
advisory's two clauses running together, split into the two lines described above). Each was
re-captured after the fix.

**Slice 4a — the watchdog line and the Host fencing setting.** Six captures:

- *Three-host pool* (five hosts, mixed) — each row's Fencing cell now carries a *Watchdog: …* line
  beneath the fence device: *this host's hardware watchdog is not armed: Nex Core arms watchdogs
  once this pool has quorum*, *this host has only the kernel's software watchdog, which a hung
  kernel cannot run, so it does not count*, *Nex Core does not recognise this host's watchdog driver
  …, so it does not count as hardware*, *this host reports no watchdog device*, and *this host does
  not report whether it has a watchdog; upgrade it*. Below the table, a **Host fencing** section with
  the checkbox *Let Nex Core arm this pool's hosts to shut themselves out*, ticked; the sentence
  *Turning this on does not arm anything by itself*; the yellow consequence box *Once watchdogs are
  armed, a host that loses this pool's quorum resets itself and its running virtual machines stop. A
  switch failure that splits a pool therefore reboots the minority side.*; and the four-line
  prerequisite checklist with fixed-width **met** / **unmet** / **unknown** badges (Hosts, Hardware
  watchdogs, Fence devices, Pool quorum), each naming the hosts that fail it.
- *Two-host pool* — the watchdog line reads *a watchdog needs three or more hosts to have a majority
  side; this pool is fenced through its management controllers*, and the Hosts checklist line is
  **unmet** with the same explanation. No pending language anywhere.
- *Turned off* — checkbox clear, every row reading *Host fencing is turned off for this pool, so Nex
  Core arms no watchdog*, and the consequence box still on the page.
- *Older Core* — no switch at all, and the section says *This pool master is too old to report the
  host fencing setting. Upgrade it to see and change it.*
- *Not reporting* — grey `unknown` everywhere and *This does not mean these hosts cannot be shut
  out; it means nothing here knows.*
- *Host fencing* — the full page capture.

One defect was found and fixed: the checklist's state badges were of different widths, so the four
line names did not align. Fixed with a fixed-width badge column and re-captured. The keyboard toggle
opens no dialog — turning the setting off needs no confirmation.

**Slice 4b — armed states.** Five captures:

- *Armed* — a green `Watchdog · armed` badge on a member with *This host feeds a watchdog that
  resets it 5 seconds after it loses this pool's quorum*, a `Watchdog · armed (emulated)` badge on a
  host whose device a hypervisor emulates, and *Watchdog: armed — this host also resets itself if it
  loses this pool's quorum* on a host that also has a confirmed controller. The pool line reads
  *ready for 3 of 3 hosts. Every host in this pool can be shut out — by another host, or by its own
  watchdog …*, and the Pool quorum checklist line is **met** with *this pool's Corosync cluster is
  quorate and every host is in it*.
- *Not armed yet*, *Checklist names* (a four-host pool whose checklist names the hosts failing each
  line, including *these hosts are not in this pool's quorate Corosync partition*), *Turned off*
  (with a row reading *this host stops feeding its watchdog the next time its cluster stops: put it
  in maintenance mode and restart it, because Nex Core will not disturb a running cluster's
  services*), and *Two-host pool*.

One defect was found and fixed: the pool summary read *"can be shut out by another"*, untrue of a
host protected by its own watchdog; it now reads *"by another host, or by its own watchdog"*.

**Slice 4c — no browser capture of the new states was taken.** Slice 4c's UI changes are covered by
the React unit suites only.

## Limits — what this report does not establish

- **No physical management controller has been contacted and no host has been powered off**, in any
  slice. All IPMI and Redfish behaviour is against fake runners and `httptest` servers. What real
  firmware does under a check every five minutes from every other host — its session table in
  particular — is unproven, and the cadence is a judgement, not a measurement. RMCP+ authentication
  by a Core-created account is untested and the Redfish Host Interface (SMBIOS type 42) is unbuilt.
- **No physical watchdog of any model has been read, armed or expired, on any machine, under any
  load.** The expiry path is proven only on QEMU's emulated `i6300esb`, and only by stopping the
  `sbd` daemon rather than by hanging a kernel: it shows the device expires and resets when nothing
  feeds it, not that a hung kernel reaches that state on real hardware.
- **Every promotion and every watchdog proof is test-only.** No proof and no promotion ran in a lab.
  What the lab measured is the precondition — partitions in which the target reset itself while the
  survivors stayed quorate.
- **The agent binary was never run in a lab guest.** Lab sequences are the commands Nex Core issues,
  in its order, driven over SSH; the code that decides *when* is covered by Go tests only.
- **All timings rest on nested runs.** `SBD_WATCHDOG_TIMEOUT=5`, the 17.9 s and 15.7 s resets, the
  8 s expiry and the 60 s proof bound all need re-measuring on physical hardware.
- **Pools that run clustered storage arm nothing.** They keep `no-quorum-policy=freeze` and report
  why. The `freeze` → `suicide` change under a mounted GFS2 filesystem, and whether DLM recovery
  waits out `stonith-watchdog-timeout`, are unmeasured and belong to that slice. No GFS2 or DLM was
  mounted in any measurement here, so neither the policy flip nor the disarm measurement covers a
  clustered filesystem.
- **Pools above five hosts arm nothing** and report it. That is this code's tested ceiling.
- **Two-host pools are fenced through management controllers only**, by owner decision; the
  follow-on work is in a separate plan.
- Not exercised in any lab: an agent restart or upgrade on an armed host, a package upgrade of an
  armed pool, a join, an eject, a master change, creating a clustered-storage repository while
  armed, maintenance in a pool of four or five hosts, and a teardown blocked by a lost host. All are
  covered by Go tests only.
- Uncharacterised: whether `sbd` treats a Pacemaker stopped under a live corosync as graceful; the
  cost of the per-bound re-confirmation at larger pool sizes; the `nowayout` branch; the packaging
  postinst line, which no upgrade test exercises.
- The browser harnesses, their bundles and their captures are untracked scratch, so this report is
  the only tracked evidence of the browser review and those runs are not reproducible from the
  repository. There is no browser capture of slice 4c's states at all.
- No whole Nex Controller was rebuilt against a live lab pool. Fresh-Controller recovery is proven
  only at the level each slice's Go tests own.
- The guide's own fencing steps have not been walked through as written against a running pool.
