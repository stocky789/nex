# Phase I-3b re-proof on the post-review-fix tree: results (the clean single run is "Run 19" at the end; the earlier sections are the partial runs that led to it)

Date: 2026-10-02, 09:19-10:15 local, hard time-box 60 minutes. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md).
Re-proves [phase3-boot.md](phase3-boot.md) on the tree after the independent-review fixes ([phase3-engine.md](phase3-engine.md)) and
closes two of its recorded deviations.

**Environment: simulation only.** One QEMU/KVM guest at a time (`-smp 2 -m 2048`, OVMF, Secure Boot off, one 14 GiB virtio disk, user-mode
networking), release images built by `image/build.sh` in the pinned tools container, local HTTP server with Range/If-Range, throwaway
keys in `.cache/`. Resource rules honoured: builds and VMs under `systemd-run --user --scope` (`CPUQuota=200%`/`300%`, `MemoryMax=4G`/`3G`, no
swap, `nice -n 19 ionice -c3`), containers `--cpus 2 --memory 4g`, `go test -p 1` on one package. All VMs were killed (`pgrep qemu` empty at the
end); the lab disk, both build trees and the package cache were deleted. Releases stayed local.

- API impact: none (no route, request or response changed). Guide impact: none (no operator workflow). State owner: Core.
- Release 1 `2026.10.1` seq 1 was built with `NEX_STATE_FORMAT=2` (roothash `0431f74a...`) and release 2 `2026.10.2` seq 2 with 3 (roothash
  `87c2ef63...`), so release 1 cannot read state that needs format 3 (for the state-guard proof). The publisher is the current zstd
  `slot-image` one (blob 299,121,054 bytes).

## What ran and passed (harness `tests/integration/image_update.py`, 44 checks passed, 0 failed in the runs that completed)

| Area | Observed |
|---|---|
| Boot 1 wiring (12 checks) | `/` read-only erofs; `trust.json` pinned; engine idle; boot check + state guard units present; **ESP not mounted at `/boot` or `/efi`** (`boot.mount`/`efi.mount` `LoadState=masked`, `findmnt` empty); `nex-boot-check` wired to `boot-complete.target` and not after chrony-wait/time-sync/network-online; state guard passes on fresh state; `nex-update-reconcile.service` `Result=success` |
| `check` | release 2 offered |
| Tampered blob | `blob_mismatch`, nothing staged, release 1 kept |
| Dropped download + Range resume | `.part` kept; next apply `GET ... Range: bytes=125829120-` |
| `stage_only` | release 2 in the inactive slot, no release 2 UKI on the ESP |
| **Failing boot check over the counted tries** | three counted boots of release 2 (`+3` -> `+2-1` -> `+1-2` -> `+0-3`): boots 1 and 2 logged "N boot tries left, rebooting to try again" and rebooted without marking bad, boot 3 marked bad; release 1 ran again after 72 s; `rolled_back` / `boot_failed`; ESP `...2_2+0-3.efi`; `/etc`, `/var`, SSH host key, machine-id unchanged |
| `apply -activate reboot` (fault removed) | 19 s to the new version; running roothash equals the signed manifest's; `systemd-bless-boot` "Marked boot as 'good'"; entry lost its counter; record `committed`, release 1 `previous`; **the record was already `committed` on disk before any CLI call** (`nex-update-reconcile.service` alone, `Result=success`, `ExecMainStatus=0`); data intact; `/` read-only erofs |
| `rollback` | release 1 booted from its retained UKI; data intact |
| Power cut during the slot write (`kill -9` of QEMU once the record said `writing`) | release 1 booted coherently (read-only erofs, `/etc` and `/var` data intact); record left `writing`; resumed by repeating the `request_id` |
| CLI exclusion | a second `nex-update apply` while one ran: `update_in_progress: another process is updating this host`; `systemctl start nex-update-reconcile.service` during an apply: success (it only reads) |

## Bugs found and fixed

1. **Boot check marked bad on the first failure.** `nex-boot-check` now reads the loader's `LoaderBootCountPath`, reboots without marking while tries are
   left and marks bad on the last one (item 3 of the assignment; proved above).
2. **My first version of that fix could not read the variable** (`tail -c +5` on efivarfs: "Illegal seek", so the tries logic silently never ran). Fixed with `cat | tail`. Found only in the VM.
3. **Masking the ESP automount broke blessing.** With no ESP at `/boot`, `systemd-bless-boot good` (stock service) and `bad` (boot check) fail with
   "Couldn't find $BOOT partition": nothing was ever blessed. Fixed with `image/mkosi.extra/usr/libexec/nex/nex-bless-boot` (mounts the ESP the
   loader booted from, `LoaderDevicePartUUID`, transiently under `/run/nex/esp/<node>`, reuses a mount Core already holds) and a
   `systemd-bless-boot.service` drop-in. Proved above (bless "good", counter dropped). Item 4 of the assignment (ESP not at `/boot`) done.
4. **`finish()` saved `state.json` after giving the update lock back** (`internal/nexupdate/image.go`): a second process could write at the same moment
   (seen as `rename state.json.tmp: no such file`). Now saved first. `go test -p 1 ./internal/nexupdate` passes; **not re-proved in a VM**, no regression test added.
5. Harness: lab shell stop delayed every reboot by 90 s (`KillSignal`/`TimeoutStopSec` set); ESP helpers reuse Core's transient mount; libvirtd restarted after the fault is
   removed because the engine (correctly) refuses to reboot a host whose guest inventory it cannot read (`vm_inventory_unavailable`); command output kept for refusals.

## Open defect (not fixed)

- **`nex-update cancel` from a second process while another process's apply runs cancels that apply's record and removes its staging** (the engine
  only knows an operation in its own process; the lock is not held for the whole apply). It left an unfinished operation that then refused every new
  apply (`update_in_progress: an interrupted update must be resumed with its request_id or cancelled first`) until a lone `cancel`. Seen once, in a VM;
  the VM test no longer issues the cancel. Needs an engine decision (hold the lock for the whole operation or record a pid/owner) and a regression test.

## Not run (time-box reached; say so plainly)

- Power cut after the write and before arming; with a half-written `.efi.new` on the ESP (real window and injected file); after arming before the bless
  (QMP `quit`) and in the counted trial boot. The harness code for these (section 6b-6d) exists but **was never executed**.
- **State guard in a VM** (release 1 over retained state needing format 3: engine refusal of the rollback, guard failure at boot, fallback after the counted
  boots). Section 7 of the harness exists but **was never executed**.
- Complete single run: the proof was assembled from three runs on one disk (steps 1-3 in one, then `--resume` for steps 4-5 and the first power cut). A
  further resume (run 12) failed only because the harness reused request id `lab-apply-0002`, which the engine replays idempotently without acting
  (harness defect; request ids must be fresh per run).
- Not changed from phase3-boot.md: Secure Boot, RAID mirror, `nex-core-local` HTTP layer, Controller acknowledgement, counter exhaustion from panic/hang.

## Progress log

- 09:19-09:30 Read the plan, QA records, harness and `image/build.sh`. Changes made before the builds: `nex-boot-check` now reboots
  without marking bad while the boot counter has tries left and marks bad only on the last try; `boot.mount`, `boot.automount`,
  `efi.mount`, `efi.automount` masked in the image (`/etc/systemd/system/*` -> `/dev/null`) so gpt-auto never mounts the ESP.
  Harness extended (power cuts, state guard, exclusion, reconcile). Release 1 (state format 2) and release 2 (state format 3) build started,
  sequentially, from an empty package cache.
- 09:30 Both releases built with the current publisher inputs (release 1 `2026.10.1` seq 1 state format 2, roothash `4ff52c36...`;
  release 2 `2026.10.2` seq 2 state format 3, roothash `22f818cf...`), full run 1 started. Boot 1 checks (units, ESP not at
  `/boot`/`/efi`, reconcile service ran, state guard fresh) passed.
- 09:35 Run 1 (against the first rebuild): steps 1-3 passed (tamper, Range resume at 125,829,120, stage_only, three counted failing boots
  ending on release 1). It found two **real defects**, both introduced by the boot changes, and I stopped the run:
  (1) `tail -c +5` cannot read an efivarfs file ("Illegal seek"), so the tries-left logic never ran; (2) with the ESP no longer
  mounted at `/boot`, `systemd-bless-boot` (both `good` from the stock service and `bad` from the boot check) fails with "Couldn't find
  $BOOT partition", so nothing was ever blessed and nothing marked bad. Fix: `nex-bless-boot` wrapper that mounts the ESP the loader
  booted from (`LoaderDevicePartUUID`) transiently under `/run/nex/esp/<node>`, a `systemd-bless-boot.service` drop-in that uses it, and
  `cat | tail`. The harness also needed: ESP helpers that reuse Core's transient mount, and `KillSignal=SIGKILL` on the lab shell
  (each reboot waited 90 s for it). Both releases rebuilt (09:45).
- 09:55 Run 2 (rebuilt images): steps 1-3 passed, including the corrected counted-boot behaviour: three counted boots of release 2
  (`+3` -> `+2-1` -> `+1-2` -> `+0-3`), the first two rebooted with "N boot tries left", the last marked bad, release 1 ran again
  in 72 s, `rolled_back` / `boot_failed`. Step 4 then hung 8 minutes: `apply -activate reboot` was **refused** with
  `vm_inventory_unavailable` because the lab fault (libvirtd masked) was still in force on the running release 1 (the engine fails
  closed when it cannot list guests: correct behaviour; the harness now restarts libvirtd after removing the fault, writes the
  command output to a file and reports it when no reboot comes). The rest of the run is resumed on the same disk (`--resume`).
- 10:00 Resumed run: update (committed, blessed, reconcile service alone made the record current), rollback and power cut in the slot
  write passed. Exclusion test found two defects: (a) `finish()` in `internal/nexupdate/image.go` saved `state.json` after handing the
  update lock back (fixed: save first, then release; unit tests pass, not re-proved in a VM); (b) a `cancel` from a second process while
  another process's apply is running does not see the apply (no in-memory operation there) and cancels its record and removes its
  staging (**open, not fixed**). The test no longer issues the cancel; the run was resumed once more (10:02).
- 10:15 Time-box reached. Hand-off: results above; sections 6b-6d and 7 of the harness unrun.

## Hand-off 2 (10:14-10:30, stopped early at the owner's request: the PC had to be restarted)

Nothing from the proof tasks below is complete. Exactly what finished, what was mid-run and what is unproven:

**Finished and proven (unit tests, `go test -p 1 -count=1 ./internal/nexupdate`: ok)**

- **The open "cancel from a second process" defect has one cause, and it is fixed.** The lock *is* held for the whole apply (an `Apply` hands its
  flock to the running operation; `TestTwoProcessesNeverWriteTheRecordTogether` already showed a second process's `cancel` refused with
  `update_in_progress` while an apply runs). What the VM saw was the end of an apply: `finish()` gave the lock back *before* saving the final
  record, so a waiting `cancel` read the half-way record (apply still unfinished), took the "interrupted apply" branch, cancelled the apply and
  removed its staging, and the apply's late save then collided with it (`rename state.json.tmp: no such file`, the same error the VM run printed).
  The fix is the `finish()` reorder already in this tree (save, then release). **A second path with the same defect was found and fixed:** an
  `apply -activate reboot` refused for running guests released the lock before dropping its just-written record (`internal/nexupdate/image.go`, `Apply`).
- Regression tests (`internal/nexupdate/image_fixes_test.go`): `TestTheFinishedRecordIsSavedBeforeTheLockIsGivenBack` and
  `TestARefusedApplyLeavesNoRecordForTheNextProcess`. Each makes the save slow (a 48 MiB foreign subtree of `state.json`), parks a second engine's
  `Cancel` on the lock and releases the first operation. **Both fail on the old order** (the first 3 of 3 runs: the finished apply's record was
  rewritten by the second process; the second: `rename ... state.json.tmp: no such file or directory`, the VM's error) and pass on the fix
  (`-count=3` ok). This covers items 1 and 2 of the assignment.

**Harness (`tests/integration/image_update.py`), changed, partly run**

- Request ids now carry a per-run token (`RUN`); the cancel from a second process is issued again (concurrently with a second apply) and must be
  refused with `update_in_progress` without cancelling the resumed apply; the "after-write-before-arm" check no longer trips over the bad release-2
  entry a rollback leaves on the ESP; the half-written `.efi.new` is injected deterministically (the real copy window is milliseconds wide);
  `wait_for` is bounded; `status` parsing ignores the shell's job-control lines.
- Run 13 (fresh install, single run, rebuilt images with the finish fix): **45 checks passed, 0 failed** through boot 1, tamper, Range resume,
  stage_only, the failing check over three counted boots, the update, the rollback, the power cut in the slot write, and the exclusion block: a
  second apply -> `update_in_progress`, **a cancel from a second process -> `update_in_progress`**, the reconcile service coexists. It then stopped
  with a *harness* error (`json.loads` on `nex-update status` output that contained `[2] + Done ...` job-control text: fixed, see `status()`).
- Run 14 (the fix applied, fresh install): 20 checks passed (boot 1 wiring, tamper, Range resume, stage_only), then the owner's stop; I killed the harness
  and QEMU (`pgrep qemu` empty), no container of mine was running.

**Not run / unproven (still)**

- Power cut after the write before arming (6b), half-written `.efi.new` on the ESP (6c), after arm before the bless with QMP `quit` and in the counted
  trial boot (6d), and the state-guard section (7): written and corrected in the harness, **never executed**, so no result for any of them.
- A complete single run through the end. Runs 13 and 14 each stopped early for the reasons above.
- The new engine ordering was **proved in a VM only as far as run 13**: the cancel was refused while the apply ran and the resumed apply was
  allowed to finish; the check that the resumed apply ended `staged` and not `cancelled` (after the status-parsing fix) did not run.

**Left over (cleanup incomplete)**

- `.cache/image-update/n1`, `n2` (about 1.9 GB of release build trees, some root-owned files that `rm` could not remove as my user), `build*.log` and
  `run*.log`. The lab disk (`.cache/image-update/lab`) was deleted. No VM, no container of mine is running. Remove the build trees with the same
  tools container that created them (or sudo) when convenient; they are under the git-ignored `.cache`.
- The release images in `n1`/`n2` contain the engine with both fixes (built 10:18-10:21), so a later run needs no rebuild.
- Half-done edits: none in source; `CHANGELOG.md` has one new Unreleased bullet on the update lock ordering. The progress log above (09:19-10:15)
  was not otherwise changed.

## Run 15 (10:54-, hard time-box 50 minutes, one clean single-disk run): progress log

- 10:54-10:57 Read the plan notes, harness and the two release trees (`.cache/image-update/n1`, `n2`: readable, built 10:18-10:22 from the tree that
  is still current: no file under `internal`, `cmd`, `image`, `scripts` is newer than that build except one test file), so no rebuild. `nex-hostimage`
  (the install tool, not the one in the image) was stale (21:57 the day before) and was rebuilt first. Harness change: section 8 (rollback again,
  a failing check with fallback, then the happy path, all after the power cuts and the state guard in the same run). Run started 10:57.
- 11:02 First launch of run 15 died in the failing-trial step with a **harness defect that is a real finding about the lab, not the product**: OVMF merges
  both serial ports into its console input, and the harness' boot-id "knock" (`echo BOOTID=...`, sent every 3 s to the control port while waiting for a
  boot) was typed into sd-boot's menu (hotkeys `c`, `e`, `j`/`k`...), which selected release 1 over the armed release 2 (menu on the console log:
  "Console mode changed", "Entry does not support editing the command line", release 1 booted with release 2's entry still `+3`). Earlier
  runs that passed were lucky about the 3 s menu window. Fix: knocks are only sent after the kernel of the current boot printed its banner
  (`VM.kernel_running`). (The same launch was also cut at 300 s by my tool timeout; relaunched fully detached.) Run 16 started 11:04.
- 11:15 Run 16 (with the knock fix, fresh disk): **0 FAIL**, 55 PASS through boot 1, tamper, Range resume, stage_only, the failing check (boots seen
  `2.. 2.. 2.. then release 1`, three counted tries logged "2 boot tries left", "1 boot tries left", then marked bad), the update, rollback, the power cut
  in the slot write (record `writing`, resumed with the same request id, ended `staged`, not cancelled; second apply and second-process cancel refused with
  `update_in_progress`), the cut after the write before arm, and the torn `.efi.new` (loader ignored it). At the QMP cut after arm the next boot was
  release 1 with release 2's entry still `+3` (the cut came before the reboot the engine had scheduled), and the harness then waited for a reboot that never
  comes; I killed it. Harness fixed: reboot into the counted trial boot by hand and cut power in it. Run 17 (all of it again, fresh disk) started 11:15.
- 11:23 Run 17 (full run, fresh disk, 11:15-11:23) reached the QMP cut after arm and found the same thing: the host booted release 1 and the menu
  listed only release 1 although `nex-core_2026.10.2_2+3.efi` was in the ESP directory (the cut falls between the rename into place and the
  engine's directory flush and byte verification). Old booted coherently, `/etc` and `/var` intact, and the engine's record was `staged` with the
  `activate` operation in phase `checking`. I stopped the run at that point (the harness waited for a reboot that cannot come) and added `--from-6d`
  (continue on the same disk at section 6d, same request-id token, `--run`).
- 11:25-11:29 Run 18 (`--from-6d`, same disk, same `RUN` token): see results below.

## Results of the run 16-18 series (the single clean run could not be completed inside the time-box)

Honest summary: **no single run went from install to the end.** The proof is run 17 (fresh disk: install through the arm cut, 54 PASS, 1 FAIL that was a
harness timing artefact) continued by run 18 (same disk, `--from-6d`: 6d through the first part of the state guard). Section 8 (rollback again, a
failing check again, the happy path again) **never ran**, and the harness' own checks of the state-guard boots never ran (details below).

| Step | Result |
|---|---|
| Boot 1 wiring, check, tamper -> `blob_mismatch`, Range resume (`bytes=125829120-`), `stage_only` | PASS (runs 16 and 17) |
| Failing check over three counted boots (`+3 -> +2-1 -> +1-2 -> +0-3`, "2 boot tries left", "1 boot tries left", then marked bad) -> release 1, `rolled_back`/`boot_failed`, data intact | PASS (run 16; in run 17 the same step's checks passed) |
| Update with the fault removed: 25 s later release 2, roothash = signed manifest's, entry blessed, record `committed` before any CLI call, reconcile service `Result=success` | PASS (runs 16, 17). One check (journal line "Marked boot as 'good'") **FAILED in run 17**: the journal was read before the bless line; the entry had already lost its counter and the record was `committed`. Harness now polls for the line; **not re-run** |
| Rollback to release 1 | PASS |
| Power cut (`kill -9`) during the slot write (record `writing`) | release 1 booted coherently (erofs ro, `/etc`, `/var` intact); resumed with the same request id: **ended `staged`, not `cancelled`**; a second apply and a `cancel` from a second process during the resume were both refused with `update_in_progress`; `nex-update-reconcile.service` coexisted. PASS (runs 16, 17) |
| Power cut after the write, before arm (`kill -9`) | release 1 booted; `staged` survives; nothing armed, no partial file. PASS (runs 16, 17) |
| Torn `.efi.new` on the ESP (5,000,000 bytes, injected), `kill -9` | release 1 booted, the loader ignored the file, record still `staged`. PASS (runs 16, 17) |
| Cut after arm, before the bless (QMP `quit`, as soon as the `+3` entry exists) | release 1 booted coherently, data intact; the `+3` file is in the ESP directory but the loader does not list it (the cut was inside the arm). Record `staged` + `activate` operation `checking`. The same request id **finished the arm and rebooted into release 2** (PASS, run 18). **Not a defect found**: old-or-new held; but the arm is not durable until the engine's flush, and a repeat of the same request id is what completes it. Recorded as a lab observation |
| Cut in the counted trial boot (release 2, before the bless, `kill -9`) | the next boot was release 2 again (counter used), it passed its check, was blessed, record `committed`, `/etc`, `/var`, host key, machine-id unchanged. PASS (run 18) |
| State guard: passes on release 2 over state needing format 3 | PASS (run 18) |
| State guard: the engine refuses `rollback` to release 1 (reads format 2) over state that needs 3 | **Refused** with `state_format_downgrade: the target image cannot read this host's retained state`. The harness reported FAIL (run 18) because it read the exit status of `| tail`, not the refusal; fixed to match the error code. The refusal itself is the observed engine output |
| State guard at boot (release 1 armed by hand over the format-3 state file) | **Not observed by the harness** (it crashed reading a guest that rebooted under it; fixed, **not re-run**). Afterwards I booted the same disk once without the harness and read what the guest kept: the journal shows `nex-state-guard.service` failed on release 1, `nex-boot-check: retained state needs a newer state_format than this image reads; libvirtd does not answer` and the boot check exiting 1; the ESP then held release 1 as `nex-core_2026.10.1_1+0-3.efi` (all tries used) and release 2 as the plain, blessed entry; release 2 booted, its guard `Finished`, `/etc` and `/var` data intact. That is the fallback, seen after the fact, not asserted live |
| Section 8: rollback again, failing check again, happy path again, in the same run | **NOT RUN.** The code is in the harness (reviewed only by running its syntax check) |

**Bugs found.** (1) Harness (and lab-only): OVMF feeds both serial ports to sd-boot, so the boot-id knock selected the wrong boot entry; fixed (`VM.kernel_running`). That also means an
earlier green run could have passed by luck of the 3 s window. (2) Harness: the step after the QMP cut waited for a reboot that could not come; the cut-in-arm path now resumes the same
request id; the settle loop no longer waits for a reboot that is not coming; section 7 reads the guard and the refusal correctly. **No product defect was found and none was changed in
`internal/nexupdate`, `internal/hostimage` or `image/mkosi.extra`, so there is no new regression test** (the earlier lock-ordering regression tests stand). `CHANGELOG.md` needed no new entry.

**State at the end.** No QEMU process and no container of mine is running (`pgrep qemu` empty). The lab disk and workdir were deleted. Still in git-ignored `.cache/image-update`: the two release build trees
(about 1.9 GB, some files root-owned), `run*.log` (including `run16.log`, `run17.log`, `run18.log`), `build*.log`, and a copy of `base.manifest` I placed in `n1/` (the harness reads it as the licence stand-in).
Nothing was committed, staged or published.

**To finish the proof (one run, about 25 minutes):** run the harness without `--from-6d` on a fresh disk (it now goes install -> section 8) with these checks fixed; expected new evidence is
the state-guard boot checks and section 8. Do that before calling Phase I-3 proven. *Done: see Run 19.*

## Run 19 (2026-10-02, 11:37-11:48 local): one clean single-disk run, install to the end

**Result: 84 checks PASS, 0 FAIL, exit 0, in one invocation of `tests/integration/image_update.py` on a fresh disk** (no `--resume`, no `--from-6d`; install of release 1, then
sections 1-8 in order). 11 minutes. Simulation only (QEMU/KVM + OVMF, Secure Boot off, user-mode networking, one VM at a time).

- Resources: one VM `-smp 2 -m 2048` under `systemd-run --user --scope` (`CPUQuota=300%`, `MemoryMax=3G`, no swap, `nice -n 19 ionice -c3`); every wait bounded (8 min); the 14 GiB
  sparse lab disk used 2.7 GB on disk and was deleted afterwards; `pgrep qemu` empty at the end. No subagents.
- Images: the release trees built at 10:18-10:22 for runs 13-18 (release 1 `2026.10.1` seq 1, `NEX_STATE_FORMAT=2`; release 2 `2026.10.2` seq 2, format 3). No file under `internal`, `cmd`,
  `scripts`, `web/src` or the image inputs is newer than that build, so they were reused, not rebuilt. **No product code changed in this task** (`internal/nexupdate`, `internal/hostimage`,
  `image/mkosi.extra` untouched), so there is no new regression test and `CHANGELOG.md` needed no entry.
- Harness changes before the run (the only edits): (a) in section 7 the lab boot check waits `NEX_BOOT_CHECK_WAIT=45` instead of 3 seconds before it fails the boot, so the
  harness can read release 1's failed guard **live** on the booted guest (3 s was shorter than the time to the control shell, which is why run 18 only saw the refusal after the fact);
  (b) the live check now requires `nex-state-guard.service` to be `failed` and libvirtd/nex-core-local not active; (c) a separate check reads the retained journal after the fallback
  for `nex-boot-check: retained state needs a newer state_format ...` (that one is **by design an after-the-fact check**); (d) the docstring lists section 8. The docstring edit was made
  while the run was already going (not executed by that process).

| Step | Result (all PASS) |
|---|---|
| Boot 1 wiring (12 checks): release 1, read-only erofs, engine idle, pinned trust, units present, ESP not mounted at `/boot`/`/efi` (readable only by explicit mount), boot check wired to `boot-complete.target` and not behind chrony/time-sync/network-online, state guard passes on fresh state, update server reachable, `nex-update-reconcile.service` succeeded | PASS |
| `check` offers release 2; tampered blob -> `blob_mismatch`, nothing staged, release 1 kept | PASS |
| Dropped download (cut at 120 MiB) kept `.part`; next apply sent `GET ... Range: bytes=125829120-`; `stage_only` wrote release 2 to the inactive slot, no release 2 UKI on the ESP | PASS |
| Failing check over the counted tries: boots `2.. 2.. 2.. then 1` in 65 s; journal "2 boot tries left", "1 boot tries left", then marked bad; `rolled_back` / `boot_failed`; entry `...2_2+0-3`; data, host key and machine-id unchanged | PASS |
| Update after removing the fault: new version 22 s after the command; record `committed` on disk before any CLI call; running roothash = the signed manifest's; release 1 `previous`; entry lost its counter; journal "Marked boot as 'good'"; reconcile service `success`; `/` read-only erofs | PASS |
| Rollback to release 1 | PASS |
| Power cut (`kill -9`) during the slot write: release 1 booted coherently (record `writing`); same request id resumed it; a second apply and a `cancel` from a second process during the resume were refused `update_in_progress`; reconcile service coexisted; resumed apply ended `staged`, not `cancelled` | PASS |
| Cut after the write, before arm: release 1 booted, `staged` kept, nothing armed, no partial file | PASS |
| Injected torn `.efi.new` (5,000,000 bytes), `kill -9`: release 1 booted, loader ignored it, record `staged` | PASS |
| Cut after arm (QMP `quit`, as soon as the `+3` entry exists): release 1 booted (the loader did not list the entry, the cut was inside the arm); record `staged` + `activate` `checking`; repeating the same request id finished the arm and rebooted into release 2 | PASS |
| Cut in the counted trial boot of release 2 (`kill -9`): release 2 booted coherently afterwards, passed its check and was blessed (`2_2.efi`, no counter), record `committed`, torn `.efi.new` gone, data intact | PASS |
| State guard (retained state needs format 3): passes on release 2; **the engine refuses `rollback` to release 1 with `state_format_downgrade`** | PASS |
| State guard at boot, **observed live**: release 1 armed by hand (`+3`), release 2 `+0-3`; boots `1, 1, 1, 2` in 197 s; on the first release 1 boot the harness read `nex-state-guard.service` = `failed` with libvirtd and nex-core-local `inactive` (`Failed to start nex-state-guard.service - Nex Core state format guard`); ESP afterwards `...1_1+0-3.efi` + plain blessed release 2; release 2's guard passes; data intact | PASS |
| State guard, from the retained journal **after the fact**: `nex-boot-check` failed the release 1 boot because of the retained-state guard | PASS (after the fact, by design) |
| Section 8, in the same run after all of the above: rollback to release 1 again (accepted once the state needs no newer format); failing check over three counted boots (`2.. 2.. 2.. then 1` in 67 s, `rolled_back` / `boot_failed`, `+0-3`); fault removed, `apply -activate reboot`: release 2 with the manifest's roothash, committed and blessed; data, host key, machine-id unchanged each time | PASS |

**Observed only after the fact (not asserted live):** the boot check's refusal message for the state guard (journal check above). The cut in the counted trial boot: the harness
asserts a coherent release 2 after the cut but the entry had already lost its counter by the time it was listed, so it does not show which boot (the cut one or the next counted one) was blessed.
The cut-after-arm path again showed that the arm is not durable until the engine's flush; the same request id completes it (lab observation, as in run 18, not a defect).

**Not covered (unchanged):** Secure Boot, RAID mirror, `nex-core-local` HTTP layer, Controller acknowledgement, counter exhaustion from panic/hang, real hardware.

**State at the end.** No QEMU process of mine remained; the lab disk and workdir were deleted. Still in git-ignored `.cache/image-update`: the two release build trees (some files
root-owned), `run*.log` (this run: `run19.log`), `build*.log`. Nothing was committed, staged or published.
