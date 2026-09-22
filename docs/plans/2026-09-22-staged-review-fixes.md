# Staged review fixes — software RAID, installer mirror, host version light, stats restyle

Status: implementation complete (code), 2026-09-22. Owner: project owner. All 112 items marked DONE in this plan. Lab gates still open where noted in slice QA (real disks, ESP-detached boot, etc.). No commit yet — worktree is dirty from parallel fix lanes; BUG_LEDGER IDs may need a collision pass before commit.

## Scope

This plan lists every defect a multi-agent review reported against the uncommitted batch staged on
top of commit b6b8536 (96 files: the three software RAID slices from
[the software RAID plan](2026-09-22-software-raid.md), the installer mirror and minimal-install NIC
skip, the host version light and the Stats tab restyle). Each item names the file and line it was
reported against, the verifier outcome, the defect and the fix the reviewer proposed. Items are
ranked by severity inside each area; fix the high items first.

## Review method and its limits

- Round one ran 34 lens-specific reviewers over nine work packages, one merge step per package,
  then three independent verifiers per merged finding (refute, reproduce, impact). A finding is
  *confirmed* when at least two verifiers called it real. Baseline checks before the review were
  all green: `go build`, `go vet`, `go test ./internal/...`, `-race` on agent, vmhost, controller and
  protocol, `npm run typecheck`, `npm test`, the `tests/` suite, `check_docs.py --worktree`,
  `generate_api.py --check` and the privacy test.
- The run was stopped early for budget reasons. The installer package and part of the
  documentation package never reached verification; their findings are listed as *unverified*
  and need a human or a single reviewer to confirm each before it is fixed. The planned fresh-eyes
  round and completeness critic did not run, so this list is not proof of absence elsewhere.
- Refuted findings (73) are not listed. The raw material is kept outside the repository.

## Acceptance for this plan

- Every fixed item gets a regression check that fails without the fix, a `docs/BUG_LEDGER.md`
  entry, and an entry in the owning slice's change record; guide and API reference updates ship in
  the same change. Run the baseline checks above plus `python3 tests/integration/installer-hostname.py`
  and `installer/check.sh` for installer items.
- Items marked unverified are triaged first: confirm or refute each with evidence, then fix or
  strike it with the reason recorded here.
- The software RAID plan's outstanding lab acceptance (real disks, both firmwares, an actual
  failed-member replacement) remains a separate gate and is not closed by this plan.

API impact: none for the plan itself; several items below change documented behaviour and the
owning fix records its own impact. Guide impact: none for the plan itself.


## System disk replacement

### SR-001 [high] DONE `internal/vmhost/md_replace.go:36` — confirmed 2/3

**Neither new replacer has a real lock: the per-instance mutex never serializes, and there is no cross-lane device claim**

Defect: ZFSReplacer and SystemDiskReplacer guard their destructive work with a sync.Mutex that is a field of an instance every caller creates fresh (`vmhost.NewZFSReplacer(...).Replace(...)`, `vmhost.NewSystemDiskReplacer(...).Replace(...)`), so it excludes nothing; unlike ZFSProvisioner, which takes an exclusive flock on its state dir for the whole run, neither takes any lock, and the two destructive lanes on a host (the peer HTTP goroutine that runs ZFS provision/replace and the managedWorker goroutine that runs host-system-disk-replace) can therefore wipe and claim the same disk at the same time.

Failure: Host M has one blank spare disk fitted and two failures: a degraded system mirror and a failed leaf in its ZFS repository. An operator submits POST /api/v1/hosts/M/system-disk (executed on M's managedWorker goroutine via ExecuteSystemDiskReplace) and, within the same minute, POST /api/v1/storage/zfs/replace, which the pool master relays to M's peer route POST /api/v1/pool/zfs-provision/replace (executed on M's peer HTTP goroutine). Both name the same by-id disk. Both call checkSystemDiskDevice, which sees the disk eligible in each case because neither has claimed it yet, and both then run `wipefs --all <disk>`; one adds it to md0 with `mdadm --add` while the other hands it to `zpool replace`, so the disk is simultaneously an md rebuild target and a ZFS resilver target and both rebuilds write over each other. The same absence of a lock lets a second peer replace request (after the master's 15s peer-client timeout at internal/agent/peer.go:510 abandons the first, or after the master agent restarts and re-executes the still-pending journal entry) start a second Replace against a disk whose first `systemd-run --wait` unit may still be running.

Fix: Give both replacers the same exclusive flock the provisioner uses, over one shared per-host state directory (or one lock file covering all destructive device work: zfs-provision, zfs-replace and system-disk-replace), and drop the per-instance mutex that cannot serialize anything.

### SR-002 [high] DONE `internal/vmhost/md_replace.go:123` — confirmed 3/3

**Once the rebuild reaches in_sync, a resume is refused forever, so a replacement that failed at the bootloader step can never be made bootable**

Defect: The "already holds a working member" guard fires unconditionally and before the journal's stage is consulted, so adoption is only possible while the replacement member is not yet in sync. After any bootloader failure, retries work only until the kernel finishes the resync; after that the operation is permanently refused and the replacement disk stays unbootable.

Failure: The agent or host dies after mdadm --add and the member-added journal write but before mdInstallBootloader returns (or grub-install fails transiently). The kernel finishes rebuilding the mirror — minutes on a small or fast disk — and the member reaches in_sync. When the command is redelivered, or the operator follows the guide and resubmits, rebuild returns "/dev/disk/by-id/X already holds a working member of /dev/md0" before the bootloader step, on every future attempt. The mirror is healthy but the new disk has no GRUB (BIOS) or an empty ESP (UEFI), and the product offers no way to finish; the only route left is the host console.

Fix: Treat a member on the replacement disk as adopted when this operation's journal already records stage member-added / bootable, regardless of its sync state, and let the idempotent bootloader step run. Keep the refusal only for a first attempt (empty stage), which is the "operator picked the surviving disk" case it is meant to catch.

### SR-003 [high] DONE `internal/vmhost/md_replace.go:212` — confirmed 3/3

**UEFI replacement can never be made bootable when the failed disk is the one fstab mounts at /boot/efi**

Defect: Both the preferred copier (the installer's nex-sync-esp) and the inline mdCopyESP fallback read the boot files from /boot/efi, which is one named ESP on one specific disk mounted `nofail`. When that disk is the one being replaced there is no source, so the operation reports failure after mdadm --add has already succeeded, and no retry can ever succeed.

Failure: UEFI host, two-disk mirror, /etc/fstab mounts /boot/efi from vda2. vda fails and the host is rebooted, so /boot/efi is an empty directory on the root filesystem. The operator replaces vda: revalidation, wipefs, sgdisk --replicate, sgdisk -G, udevadm settle and mdadm --add all succeed and the kernel starts rebuilding, then mdInstallBootloader runs mkfs.vfat on the new ESP, nex-sync-esp exits non-zero and mdCopyESP exits 1 at `[ -d /boot/efi/EFI ]`. Replace returns "the replacement disk's EFI system partition could not be made bootable"; the task shows failed although the mirror is rebuilding, every retry fails identically, and the new disk is left with a freshly formatted, empty ESP — the host boots only from the surviving disk, which defeats the mirror.

Fix: When /boot/efi/EFI is absent, mount the surviving member disk's own ESP (mdESPPartition already returns its partition number) at a temporary mountpoint and copy its EFI directory onto the new ESP; at minimum, report a reason that names the missing source.

### SR-004 [high] DONE `web/src/md.tsx:79` — confirmed 3/3; confirmed 3/3

**Both polling intervals are destroyed before they can fire, so the panel never follows a rebuild or a submitted replacement**

**System disk panel's rebuild and task polling intervals are torn down faster than they fire, so neither auto-refresh happens**

Defect: `onReplaced` is the inline arrow `onRetry={() => setHardwareAttempt(a => a + 1)}` created fresh on every `Stats` render (stats.tsx:491) and forwarded as `onReplaced` (stats.tsx:322). Both effects in SystemDiskMirror list it in their dependency array, so each Stats re-render tears down and recreates their setInterval. On the default 10m range Stats re-renders every 5 s, so the 10 000 ms rebuild timer and the 5 000 ms task timer are cleared before their deadline.

Failure: Operator opens host -> Stats on the default 10m range with a degraded mirror and submits a replacement. Stats re-renders 5 s later, clearing the task timer before it fires, and again every cycle; `task` never advances past `queued`, so neither "The replacement disk was added…" (md.tsx:104) nor "The host could not replace the disk: …" (md.tsx:103) is ever rendered — a failed replacement reports nothing in this panel. The array likewise stays frozen at its last-read "Rebuilding · 43.8%" because the 10 s re-read is cleared at ~5 s each time. Only the manual Retry button updates the panel. Switching to the 24h range masks it.

Also: Both useEffect timers in SystemDiskMirror depend on `onReplaced` (and `api`), which are new function objects on every render of Stats/App. Stats re-renders every 5 s from its own history poll, so a 10 s and a 5 s interval are cleared and recreated before they can fire.

Fix: Give `onRetry` a stable identity in Stats (`const rereadHardware = useCallback(() => setHardwareAttempt(a => a + 1), []);`) and pass that; same for `api`, or hold `onReplaced` in a ref inside SystemDiskMirror and depend only on `[rebuilding]` / `[task?.id, task?.status]`. Wrap the Stats retry callback in useCallback (`const reloadHardware = useCallback(() => setHardwareAttempt(a => a + 1), [])`) and keep `api` out of the effect deps (hold it in a ref), or store the latest callbacks in refs inside SystemDiskMirror so the intervals depend only on `rebuilding` and `task.id`/`task.status`.

### SR-005 [medium] DONE `docs/guide/replace-a-failed-system-disk.md:83` — confirmed 2/2

**"The dialog says which case it is" is false: the empty state always shows the wrong message**

Defect: The troubleshooting entry promises the dialog distinguishes "every disk in use" from "every spare disk too small". The code picks between those messages with `usable.every(small)` but renders the message only when `usable` is empty, where `every` is vacuously true — so the "too small" message always wins and the "every disk is in use" text is unreachable.

Failure: A host where every disk is mounted, in swap, in an md array or in a zpool is opened in Replace disk…. The dialog says "Every spare disk on this host is smaller than the remaining system disk (64 GiB). Fit a larger disk, then refresh." The operator fits a larger disk, which changes nothing, because size was never the problem.

Fix: Fix the branch in web/src/md.tsx (`usable.length && usable.every(small)` for the too-small text, and render the message in the all-disabled case too), then the guide sentence is correct; also annotate the QA record line, which no longer matches shipped behaviour.

### SR-006 [medium] DONE `docs/guide/replace-a-failed-system-disk.md:96` — unverified

**Two guide pages in this batch contradict each other about the ESP mount and nofail**

Defect: install-nex-core.md says the per-disk EFI partition "is mounted in a way that never holds up the boot", while replace-a-failed-system-disk.md tells the operator the host stops in emergency mode and that they must add `nofail` by hand from the console. Neither page says which hosts its statement applies to.

Failure: An operator whose host was installed by this build reads the troubleshooting entry, opens the console expecting a missing `nofail`, finds it already present and has no guidance for the actual cause; a reader of install-nex-core.md is told the boot can never be held up, which is only true for hosts installed by this build.

Fix: Scope the troubleshooting entry to mirrored hosts installed before this build and to hand-built mirrors, say installs from this build already mount `/boot/efi` with `nofail`, and note that the installer-side fix has not been seen on a booted host.

### SR-007 [medium] DONE `internal/agent/hardware.go:46` — confirmed 2/3

**md_arrays is never asserted to be in the hardware report**

Defect: The one line that wires ReadMDArrays into the host hardware report has no test; no Go test in the repository references MDArrays at all, so removing the wiring is invisible to CI.

Failure: Delete internal/agent/hardware.go:46. The report serialises with md_arrays zero-valued, every host page shows the no-mirror empty state and Replace disk… is unreachable everywhere, while `go test ./...`, the api-contract suite (which validates the openapi schema against the Go struct, not the population) and `npm test` all pass.

Fix: In TestReadHardwareWithoutSysfsOrProc add the positive case using the same fixture tree md_test.go's writeMDFixture builds and assert `readHardware(ctx, root).MDArrays.Arrays` names it; at minimum assert `hw.MDArrays.Unavailable` for the empty-tempdir root.

### SR-008 [medium] DONE `internal/agent/md_replace.go:17` — confirmed 2/3

**No agent-side test admits or routes host-system-disk-replace**

Defect: The agent half of the new operation — command admission (managed.go:26), host addressing (managed.go:649), the dispatch branch (managed.go:227) and ExecuteSystemDiskReplace's own two guards — has no test at all, although the repo has an exact precedent for the analogous host-scoped command.

Failure: Delete `case "host-system-disk-replace":` from internal/agent/managed.go:649. `commandHost` then returns "", so managedWorker's guard fails for every host and every replacement comes back as "managed command host identity does not match enrolled host" — the feature is dead on every host, while `go test ./...`, `-race`, `npm test` and the tests/ suite all stay green (internal/controller/md_replace_test.go only proves the Controller queued the command). The same holds for dropping the kind from `managedCommand` (managed.go:26), which makes the command silently discarded.

Fix: Add the two-function equivalent of TestManagementNetworkCommandRoutingAndAdmission in internal/agent: assert `managedCommand("host-system-disk-replace")`, that `commandHost` returns `cmd.SystemDiskReplace.HostID` and "" for a request-less command, and drive ExecuteSystemDiskReplace's nil-request and mismatched-operation-ID refusals (neither reaches vmhost).

### SR-009 [medium] DONE `internal/vmhost/md_replace.go:128` — confirmed 3/3; confirmed 3/3

**A faulty-but-still-attached member's own disk is treated as an adopted replacement, so the task reports success having repaired nothing**

**A still-attached faulty member on the chosen disk is treated as an adopted replacement, so the command reports success without repairing the array**

Defect: `adopted` is set from "this disk hosts any member that is not in_sync", which is true for a faulty member the kernel has kicked but not removed. All revalidation, the wipe, the partition copy and mdadm --add are skipped; only grub-install runs, and Replace returns nil.

Failure: md0 has sda3 in_sync and sdb3 faulty (what `mdadm --fail` alone, or a disk the kernel kicked without the device disappearing, produces). A POST to /api/v1/hosts/{id}/system-disk naming sdb's by-id passes SystemDiskReplaceRequest.Valid (string shapes only) and the Controller's checks (internal/controller/md_replace.go:38-42); rebuild sets adopted=true, nothing is revalidated, nothing is added, grub-install writes to the failing disk and the task reports succeeded with the banner "The replacement disk was added". The array is still degraded and the operator believes it is being repaired. Not reachable from the dialog — discovery marks a disk carrying a linux_raid_member partition ineligible (zfs_device_discovery.go:179) — but reachable from the API, which is the contract Core is supposed to validate.

Also: rebuild() decides the replacement was already added whenever the array has any member sitting on the chosen disk that is not in_sync. A faulty member of the failed disk itself satisfies that test, so picking the failed disk skips the device revalidation, the wipe and `mdadm --add`, runs only the bootloader step, and returns a succeeded result on an array that is still degraded and untouched.

Fix: Only adopt a member in a spare/rebuilding/replacement state (or one this operation's journal records as added), and refuse a disk that currently carries a faulty member of the same array with its own sentence, the way :123 already refuses an in-sync one. Adopt only a member the kernel is actually rebuilding onto — e.g. require the member state to be "spare"/recovering rather than merely not in_sync — and refuse a faulty member on the chosen disk with its own sentence.

### SR-010 [medium] DONE `internal/vmhost/md_replace.go:176` — confirmed 2/3

**The member-added journal write can fail a replacement that has already succeeded in the kernel**

Defect: Only `wipe-started` is compared against (md_replace.go:151); the member-added and bootable stages exist to record progress but drive no resume decision — resume after mdadm --add is decided from the array itself via the `adopted` branch. The member-added write costs an extra failure point after the destructive work is done.

Failure: mdadm --add succeeds, then the journal write at :177 fails (full or read-only /var). Replace returns "the system disk replacement journal could not be written" and the operator is shown a failed replacement, although the member is in the array and the kernel is rebuilding; the bootloader step was skipped, so the new disk is in the mirror but not bootable until the operator retries.

Fix: Either delete :176-179 and :186 (the adopt path already covers resuming after the add), or — preferably, per the in-sync-guard finding — make the stages actually drive adoption so the write earns its failure mode.

### SR-011 [medium] DONE `internal/vmhost/md_replace.go:345` — confirmed 2/3

**saveSystemDiskJournal hand-rolls a non-fsynced write instead of the package's atomicWrite, so the wipe-started boundary can be lost or truncated**

Defect: The journal writer re-implements temp-file+rename by hand with no fsync of the file or the directory, while the same package already has atomicWrite (engine.go:481) which the sibling saveZFSReplaceJournal uses. The journal's only job is to survive an interruption at a destructive boundary.

Failure: The journal is written with stage wipe-started, wipefs erases the replacement disk's signatures, and the host loses power before the data reach stable storage. On ext4 data=ordered the rename can be durable while the contents are not, so the file comes back zero length: readSystemDiskJournal's json.Unmarshal fails, the error is not os.ErrNotExist, and Replace returns "the system disk replacement journal is unreadable" (md_replace.go:74-75) for every future attempt — the operator is left with a wiped disk and a degraded mirror the product will not finish until someone deletes the file over SSH. If instead the file is simply absent, the resume takes the requireFingerprint=true path at md_replace.go:151 and is refused with "the replacement disk changed since it was selected, or is in use", because the wipe changed the fingerprint. Either way the guide's documented resume path does not work.

Fix: Delete md_replace.go:345-355 and call atomicWrite(path, raw) as saveZFSReplaceJournal does; readSystemDiskJournal and readZFSReplaceJournal are also the same ten lines twice, so one can go too.

### SR-012 [medium] DONE `internal/vmhost/md_replace_test.go:90` — confirmed 3/3

**The noESP fixture branch is dead; no test exercises a host with no ESP to copy**

Defect: TestReplaceSystemDiskOnBIOSWithoutAnESP sets noESP=true but runs on BIOS firmware, where mdInstallBootloader returns before lsblk is ever called — so the ESP-less partition-table fixture is never evaluated, and the UEFI-host-with-no-ESP path it was written for stays untested.

Failure: On a UEFI host, mdESPPartition returns "the remaining system disk has no EFI system partition to copy" at md_replace.go:204-207 — i.e. after wipefs, sgdisk and mdadm --add have already succeeded and the journal reached member-added: the operator sees a failed task while the kernel is in fact rebuilding the mirror correctly. No test covers that ordering, and deleting the whole `if noESP {…}` fixture branch changes no test result.

Fix: Either delete the dead noESP global and fixture branch (the BIOS test's real assertion stands on its own), or point mdFirmwareEFI at an existing directory in that test so the UEFI-with-no-ESP refusal is actually exercised, asserting the journal stage when the bootloader step fails after the member was added.

### SR-013 [medium] DONE `web/src/md.test.mjs:59` — confirmed 3/3

**The replace dialog is checked by source regexes, not behaviour; the submitted payload and the (broken) polling are uncovered**

Defect: Ten of md.test.mjs's assertions match the text of md.tsx rather than running it. The one thing with a contract on the other side — the POST body submit() sends to hosts/{id}/system-disk — is never executed, and the two assertions that claim to cover "the panel follows a rebuild and the submitted task" pass on code whose intervals never fire (see the polling finding).

Failure: Drop `fingerprint: device.fingerprint` from md.tsx:169 (or send erase_confirmation unconditionally): `node web/src/md.test.mjs` still prints ok, and every replacement then fails at internal/protocol/systemdisk.go:38 with HTTP 400 "system disk replacement device is invalid". Conversely, reformatting the setInterval at md.tsx:79 breaks CI without changing behaviour, while the actual polling regression is invisible. (Source-regex assertions are house style — zfs.test.mjs has 15 — so the gap specific to this slice is that the cross-process payload is among the untested parts.)

Fix: Drive submit() with a stub api and assert the path `hosts/<id>/system-disk`, that request_id is reused across a retry after a rejected call, and that erase_confirmation appears only for `erase_required`; that lets the four src regexes at :59-64 be deleted. Extract the follow logic so the polling can be asserted with fake timers.

### SR-014 [medium] DONE `web/src/md.tsx:108` — confirmed 2/3

**Replace disk… stays offered during an active rebuild, and Core accepts a second replacement onto a third disk**

Defect: The button's condition is `array.degraded > 0` alone, but md keeps `degraded` at 1 for the whole recover, and Core's only concurrency guard is the same field, so a second submission during a rebuild passes every check and wipes another disk, adding it as an extra spare.

Failure: md0 is rebuilding onto sdb (sda3 in_sync, sdb3 spare; degraded still 1, sync_action recover). The operator — or a second administrator who cannot see the first operator's in-flight task, which is component-local state — clicks Replace disk… again, picks the spare sdc and types the erase confirmation. Discovery lists sdc as eligible, `adopted` is false, Degraded is 1, so Core runs `wipefs --all` on sdc, replicates the table and `mdadm --add /dev/md0 <sdc>-part3`: a disk the operator did not intend to lose is erased and joins as an idle hot spare while the first rebuild is still running, and the UI reports "The replacement disk was added". The guide's "one disk at a time" limit is enforced nowhere.

Fix: Gate the button on `!actions[array.sync_action || ""]` as well (the `rebuilding` flag already exists) with a summary line explaining the rebuild must finish first, and refuse in Core when the array reports a running recover/resync/reshape.

### SR-015 [medium] DONE `web/src/md.tsx:179` — confirmed 3/3

**Empty-disk-picker message always claims every spare disk is too small**

Defect: `usable.every(small)` is evaluated only when `usable.length === 0` (md.tsx:186), where `Array.prototype.every` is vacuously true, so the ternary always takes the "too small" branch and the "no disk that can be used" sentence is dead code — while the too-small case it was written for keeps `usable` non-empty and never renders it.

Failure: Host with a degraded mirror where every remaining disk is mounted, in swap, in a ZFS pool or in another array, so `usable` is empty. The dialog says "Every spare disk on this host is smaller than the remaining system disk (Size not reported). Fit a larger disk, then refresh." The operator fits a larger disk and gets the same message, because the real reason is that no disk is eligible at all. docs/guide/replace-a-failed-system-disk.md promises "The dialog says which case it is", and docs/qa/2026-09-22-system-disk-replace.md:117 records observing the other wording in exactly this situation — so the recorded browser evidence no longer matches the code.

Fix: Branch on the candidate set instead: show "This host reports no disk that can be used…" whenever `usable.length === 0`, and compute the too-small wording from the disks that exist (or drop it, since a too-small disk is always listed rather than hidden).

### SR-016 [low] DONE `internal/controller/managed.go:1054` — confirmed 2/3

**managedKind gained a kind that can never appear in a host's managed outcomes**

Defect: "host-system-disk-replace" was added to managedKind, the filter that decides which host-reported managed outcomes may settle a Controller operation row, but the command never goes through the managed engine, so no outcome of that kind is ever produced.

Failure: No runtime failure; the entry is unreachable. It does mean the slice's stated recovery story is thinner than it looks: a replacement that completes while the agent's own state journal entry is lost is not re-settled from host-reported outcomes, unlike the storage/VM kinds this list was written for. (The pre-existing "management-network" entry is vacuous for the same reason.)

Fix: Either drop the entry, or report the replacement as a managed outcome so the adoption path can actually settle it.

### SR-017 [low] DONE `internal/protocol/systemdisk.go:28` — confirmed 2/3

**OperationID is used unescaped as the journal filename with no character validation**

Defect: SystemDiskReplaceRequest.Valid checks only that the operation ID is non-empty and bounded, and the journal path is built by string concatenation, so an ID containing path separators escapes the state directory.

Failure: A compromised or buggy Controller sends operation_id "../../../../etc/cron.d/nex"; Core writes a root-owned 0600 JSON file at that path (and a .tmp beside it), clobbering whatever was there. The Controller is already trusted to order disk wipes, so this is boundary hardening rather than new privilege, and it matches the pre-existing ZFS provisioning pattern — but systemdisk.go is the new validator and the cheapest place to fix it.

Fix: Validate OperationID with the existing storageOpaqueID regexp in SystemDiskReplaceRequest.Valid.

### SR-018 [low] DONE `internal/vmhost/md_replace.go:85` — confirmed 3/3

**Journal keeps the previous failure's error text after a successful retry**

Defect: `journal.Error` is set on failure but never cleared on the success path, so a resumed operation that finally succeeds is recorded as `"status":"succeeded"` carrying the earlier failure's sentence. The sibling ZFS replacer clears it.

Failure: A replacement fails at mdadm --add and is recorded needs_attention with `"error":"… could not be added to /dev/md0"`. The operator resubmits the same operation ID and it completes. The journal file then reads `{"status":"succeeded","error":"… could not be added to /dev/md0"}` — a self-contradicting record for anyone diagnosing the host during a support call. No effect on the API result.

Fix: Assign both fields on the success path: `journal.Status, journal.Error, journal.ObservedAt = "succeeded", "", time.Now().UTC()`.

### SR-019 [low] DONE `web/src/md.tsx:16` — confirmed 2/3

**The system disk dialog re-implements the disk picker the ZFS lane factored into shared helpers in the same batch**

Defect: zfs.tsx gained DiskCells, diskColumns, BlockedDisks, zfsDiskOrder, zfsBlockedSummary and the .zfs-disk-table markup so "the same disk columns in the wizard and the replace dialog, in the same order". md.tsx, written concurrently, copies size/shortName/dirty/zfsDeviceUsable and keeps the pre-batch <ul className="zfs-disks"> markup, so the two replacement dialogs now look and sort differently.

Failure: An operator replaces a ZFS pool disk (a table, smallest disk first, tick column, State column) and then a system disk on the same host (a radio list, largest disk first, different wording) and sees two unrelated pickers for the same act; a later change to the shared columns silently leaves the system disk dialog behind.

Fix: Import DiskCells, diskColumns, BlockedDisks, zfsDeviceUsable and zfsDiskOrder from zfs.tsx in md.tsx and delete the copies, keeping only the size-requirement column the system disk case adds.

### SR-020 [low] DONE `web/src/md.tsx:17` — confirmed 2/3

**md.tsx re-declares the device helpers, types and eligibility rule that zfs.tsx already exports**

Defect: Five helpers and two types are copied verbatim from web/src/zfs.tsx, including mdDeviceUsable, which restates the already-exported zfsDeviceUsable, and the inline erase-confirmation body, which restates deviceBody.

Failure: Core adds or renames an eligibility value (say "erase_required" splits into "erase_required"/"foreign") and the ZFS path is updated at zfs.tsx:35 and :70. The system-disk dialog keeps its own copy, so it either hides a disk the host says is usable or submits a device body without erase_confirmation for a disk that now needs one, and the operator gets Core's bare refusal instead of the typed-confirmation field. No test ties the two copies together.

Fix: Export size/shortName/dirty/deviceBody and the Device/Discovery types from zfs.tsx and import them in md.tsx; delete md.tsx:13-14,17-20 and replace the md.tsx:169 literal with deviceBody(device, confirmation). No import cycle: md -> zfs -> managed.

### SR-021 [low] DONE `web/src/md.tsx:44` — confirmed 2/3

**Every md array is described as a mirror, whatever its level**

Defect: mdArraySummary hard-codes "Mirror of N disks" and the Replace disk… button ignores `array.level`, although ReadMDArrays reports every assembled md array and Core only rebuilds raid1.

Failure: A host with an operator-created raid5 data array beside its system disk. Stats reports it under "System disk mirror" as "Mirror of 4 disks, all in sync." When that array degrades, Replace disk… is offered; the operator picks a disk, confirms erasure, and the task comes back failed with "/dev/md1 is a raid5 array; only a RAID1 mirror is rebuilt this way" after the round trip.

Fix: Use `array.level` in the sentence and offer Replace disk… only for `level === "raid1"`, saying so for other levels.

## ZFS: degraded pools, replacement and spares

### SR-022 [high] DONE `docs/guide/storage-repositories.md:494` — confirmed 3/3

**Verification section claims browser + lab coverage the QA record explicitly denies**

Defect: The new Verification paragraph says three-disk mirrors, hot spares and Replace a failed disk were walked through "in the browser and through the API", names only two limits, and closes "Everything else on this page about replacing a disk was observed" — but docs/qa/2026-09-22-zfs-replace.md labels the three-way-mirror/hot-spare section as reported-not-evidence and lists five outstanding items, including that the reworked screens this page describes have never been rendered.

Failure: A reviewer or the project owner reads this Verification section to decide whether slice 2 can be accepted, sees two narrow limits and "Everything else … was observed", and does not open the QA record. The Spare column, layout fieldset, live summary, degraded host row and renamed confirm button have never been seen running, section 3 retained no output, and the BUG-0445 adoption path was never re-proved on a pool — so the slice is closed on a verification claim that did not happen, which AGENTS.md forbids ("Never mark an unexecuted workflow verified").

Fix: Replace "Everything else on this page about replacing a disk was observed." with the record's actual limits: the API path of the mirror replacement was observed with output retained; the three-way mirror and hot-spare checks were run but retained no evidence; the reworked disk tables, Spare column, layout fieldset, live summary, degraded host row and confirm button have not been seen in a browser; RAIDZ3, automatic spare engagement and the post-`zpool replace` interrupted retry are outstanding.

### SR-023 [high] DONE `internal/controller/storage.go:328` — confirmed 3/3

**Hot spares and the widened layouts ride on the old storage-zfs-provision-v1 capability, so an older Core silently drops the spare (or refuses the whole provision)**

Defect: provisionZFSStorage checks only protocol.ZFSProvisionCapability on the master and each selected host; nothing requires storage-zfs-replace-v1 before sending the new `spares` array or a layout the old ValidZFSLayout rejected, and the wizard gates the Spare column on the old capability too — contradicting docs/API.md's claim that an older Core never receives the field.

Failure: Documented upgrade order is Controller first. Upgrade the Controller, leave the pool master on the previous Core. The wizard offers the Spare column; the operator ticks 3 data disks + 1 spare and confirms erasure of all four. The old master ignores `spares`: it creates a 3-disk RAIDZ1 with no `-o autoreplace=on` and no spare vdev, never erases the fourth disk, and reports `succeeded`. The operator believes a hot spare will resilver automatically and it never will. Variant: master upgraded, member older — the master forwards the whole request including `spares`, the member's strict decoder returns 400 and the operator sees the generic 'selected ZFS host refused provisioning'. The widened layouts (mirror at 3, raidz3 at 5+) have the same hole.

Fix: Require protocol.ZFSReplaceCapability on the master and on every selected host in provisionZFSStorage whenever any host carries spares or a layout the old rule rejected, and gate the wizard's Spare column and new layout radios on the same capability.

### SR-024 [high] DONE `internal/protocol/storage.go:137` — confirmed 2/3

**New inventory fields zfs_pool / pool are not capability-gated, so an older Controller 400s every report from an upgraded Core**

Defect: StorageAccess.ZFSPool (storage.go:137) and ZFSHostAccess.Pool (managed.go:147) are added to the host→Controller inventory with no capability gate and no legacy projection, but the Controller decodes host reports with DisallowUnknownFields, so a Controller that predates this slice rejects the whole report.

Failure: A pool has a Core-provisioned ZFS repository and the Controller already acknowledges storage-zfs-provision-v1 (ZFSProvisionProtocolReady true, no legacy projection). The operator upgrades a Core host package before the Controller. The agent posts POST /api/v1/agent/report with `"access":{...,"zfs_pool":{...}}` and `"zfs_host_access":[{...,"pool":{...}}]`. The old Controller's strict decoder fails with `json: unknown field "zfs_pool"` and returns 400 "invalid JSON request". The agent's downgrade path (internal/agent/run.go:373) triggers on 409, not 400, so every subsequent report fails identically: the host goes stale, receives no commands, and management of its VMs is lost until the Controller is upgraded. A manually registered (non-provisioned) ZFS repository breaks the same way even on a Controller that only acknowledges storage-repositories-v1, because zfsLegacyManagedProjection keeps non-provisioned entries.

Fix: Gate the two new fields the way storage cluster results are gated (internal/agent/run.go:338 `storageClusterResultsForController(clusters, s.State.StorageClusterSessionMonitorProtocolReady)`): advertise a capability marker and clear Access.ZFSPool / ZFSHostAccess.Pool in reportStorage until the Controller acknowledges it. Then correct the compatibility claim in the change record and docs/API.md.

### SR-025 [high] DONE `internal/vmhost/storage_zfs.go:111` — confirmed 2/3

**New access.zfs_pool field is reported unconditionally and a strict older Controller rejects the whole report**

Defect: inspectZFS now attaches a ZFSPool observation to StorageAccess for every ZFS repository, and StorageRegistration.Access is part of the managed inventory in POST /api/v1/agent/report. The Controller decodes that report with DisallowUnknownFields, so a Controller built before this batch answers 400 for every report from an upgraded Core that has any ZFS repository.

Failure: An operator updates nex-core-local on a host to this build while the Controller is still one release behind (the new version light on the host page actively prompts this order). The host has any ZFS repository, provisioned or registered. Its next report body contains `"access":{...,"zfs_pool":{...}}`; the old Controller's json.Decoder rejects the unknown field and returns 400 "invalid JSON request". The agent's only report fallback is on 409 (internal/agent/run.go:376), so it never downgrades: the host stops reporting, goes stale in the UI and accepts no managed commands until the Controller is upgraded.

Fix: Gate the new observation the same way ZFS provisioning is gated: add a capability the Controller must acknowledge (or reuse ZFSProvisionProtocolReady) and strip Access.ZFSPool from every reported registration until it is acknowledged, in the same place as zfsLegacyManagedProjection. Add a strict-old-schema decode test alongside internal/agent/storage_protocol_test.go.

### SR-026 [high] DONE `internal/vmhost/zfs_status.go:75` — confirmed 2/6; confirmed 2/6

**Flattened vdev tree hides an in-flight replacement, so a second "Replace disk" is offered and wipes a disk before zpool replace fails**

**zfsPoolLeaves treats zpool status --json class containers (spares/logs/l2cache) as vdevs, so a healthy hot spare reads as a failure**

Defect: zfsPoolLeaves() flattens replacing-/spare- vdevs into a flat leaf list, discarding the fact that the failed leaf is already being replaced. Nothing downstream can tell a resilver-in-progress leaf from an untouched failed one, so the UI keeps offering "Replace disk" for it and Core accepts a second, different replacement: it wipes the newly chosen disk and only then fails at zpool replace.

Failure: Mirror nex-repo-1 = {DATA-5, DATA-6}. DATA-5 fails; the operator replaces it with DATA-4. The pool is DEGRADED with replacing-0 {DATA-5-part1 OFFLINE, DATA-4-part1 ONLINE} and a running resilver that takes hours. The flat device list is [DATA-4-part1 ONLINE, DATA-5-part1 OFFLINE, DATA-6-part1 ONLINE], so the Storage page still reads "Pool degraded · …DATA-5-part1 offline · resilver in progress · 1% (4.88G of 4.89G)" with an enabled Replace disk button listing DATA-5-part1 as failed. Believing the first submission did not take, the operator picks blank disk DATA-7 and confirms. A new operation_id means a new journal, zfsPoolHoldsDisk(DATA-7) is false, zfsPoolLeaf(DATA-5-part1) is OFFLINE, so Core runs `wipefs --all /dev/disk/by-id/DATA-7` and then `zpool replace nex-repo-1 DATA-5-part1 DATA-7`, which ZFS refuses ("already in replacing/spare config"). DATA-7 has been erased for nothing and the operation ends in Needs attention with a raw sentence that does not say a resilver is already running.

Also: zfsPoolLeaves assumes every entry in a pool's `vdevs` map is a vdev with name/state, but `zpool status --json` also places class containers (`spares`, `logs`, `l2cache`, `dedup`, `special`) in that map. Either the idle spare (AVAIL) or a bare phantom entry poisons the leaf list the whole degraded/replace UI keys off — and this same batch adds hot-spare provisioning.

Fix: Keep the parent vdev type on each leaf (a `replacing`/`spare` marker or the parent name) in ZFSPoolDevice when flattening, and use it twice: ZFSReplacer.replace() refuses an OldDevice inside a replacing-/spare- vdev ("already being replaced; wait for the resilver") BEFORE the wipe, and web/src/storage.tsx shows "being replaced" instead of offering Replace disk. A cheaper interim guard is suppressing the action while `pool.scan?.state === "SCANNING"`. Descend only into the pool's own root vdev (key matching the pool name, or vdev_type root) and ignore the other class containers, or decode them and carry a `class` on ZFSPoolDevice so spares/log/cache leaves are never counted as failures; drop a leaf with an empty name/state either way. Capture one `zpool status --json` from a pool with a spare into testdata (already on the QA outstanding list) and assert what zfsPoolLeaves does with it.

Note: Two distinct findings share this line, so their verifier votes were pooled (2 of 6 each); re-check both before fixing.

### SR-027 [medium] DONE `docs/guide/storage-repositories.md:249` — unverified

**Step 2 points at the repository table row; Replace disk only exists in the "ZFS storage by host" panel**

Defect: The steps say to "open the degraded repository and find the host row that reports Pool degraded. Select Replace disk on that row" — but the repositories table has no expand action and no Replace disk button; the button is rendered only in the separate "ZFS storage by host" section further down.

Failure: An operator with a degraded pool follows the numbered steps, scans the repositories table for a Replace disk control, finds none, and concludes the feature is missing or the host unsupported. DOCUMENTATION.md requires Steps to give actual UI labels/locations.

Fix: Change step 2 to name the panel and Action column, and add a line saying the button is absent when the host's Nex Core lacks storage-zfs-replace-v1/storage-discovery-v1 or the host is not freshly connected.

### SR-028 [medium] DONE `docs/guide/storage-repositories.md:258` — unverified

**Step 5 tells the operator to type the "exact displayed by-id name", which the replace dialog never displays**

Defect: ZFSReplaceDialog renders the erase-confirmation field with no by-id name beside it — unlike the provisioning wizard's review step and the system-disk dialog, which both print it. The by-id path is only reachable as a `title` tooltip on the Serial cell.

Failure: An operator replacing a disk that holds old data looks for the by-id name the guide says is displayed and finds only the serial. Submit stays disabled (`ready` requires `confirmation === chosen.by_id`), so they must guess the path or hover the Serial cell — impossible for a keyboard or touch user — and a destructive-confirmation step is a dead end.

Fix: Render the chosen device's `by_id` beside the confirmation input in ZFSReplaceDialog, as the wizard and the system-disk dialog already do; then the guide sentence is correct as written.

### SR-029 [medium] DONE `docs/qa/2026-09-22-zfs-degraded.md:27` — confirmed 3/3; confirmed 3/3

**QA record and change record present a derived fixture as captured lab output**

**QA record calls the derived idle fixture a capture**

Defect: Both docs/qa/2026-09-22-zfs-degraded.md and docs/changes/2026-09-22-zfs-degraded.json say all three zpool-status fixtures are captures from the lab host, but the healthy/idle one is zpool-status-idle-derived.json, which the test file, BUG-0443 and the zfs-replace change record all label derived — every capture of that pool had a resilver in flight.

Failure: A reviewer or phase gate reads the QA and change records, concludes the ONLINE-health mapping is proved against untouched host output, and closes slice 1's evidence. In fact a real idle pool's JSON shape (absent scan_stats, different fields) has never been decoded, and AGENTS.md forbids implying edited evidence is an untouched capture.

Also: The QA evidence describes all three `zpool status --json` fixtures as captured from the lab host, including "a healthy mirror", while the test file, the change record and the bug ledger all state that the idle fixture is derived by hand from a resilvering capture.

Fix: Amend docs/qa/2026-09-22-zfs-degraded.md:26-29 and docs/changes/2026-09-22-zfs-degraded.json:63 to say two fixtures are captured (degraded, resilvering) and zpool-status-idle-derived.json is derived from a capture, with the reason, and carry the "capture a genuine idle ONLINE pool" item into this record's limits. Reword the bullet at docs/qa/2026-09-22-zfs-degraded.md:27-30 to match the test comment and the ledger: two captured from a file-backed fixture pool on the lab host, one derived from a capture by removing the in-flight resilver.

### SR-030 [medium] DONE `internal/agent/zfs_pool.go:661` — confirmed 2/3

**ZFSProvisionHost.spares and the widened layouts reach a member's strict peer decoder with no capability gate**

Defect: The master forwards the operator's selection verbatim to the member over POST /api/v1/pool/zfs-provision, which decodes with DisallowUnknownFields and revalidates with the member's own ValidateZFSProvision. A member running an older Core rejects spares, a three-disk mirror and raidz3 with an opaque 400.

Failure: In a pool part-way through a Core upgrade, the master is new and a member is one release older. The operator ticks three disks with one as a hot spare on that member. The master dispatches, the member's strict decoder rejects `spares` and the master surfaces "the selected ZFS host refused" / "invalid ZFS provision" with nothing that names the version mismatch.

Fix: Either advertise a capability for the widened selection and have the Controller refuse a host without it (as it already does for storage-zfs-replace-v1), or have the master check the member's reported capabilities before dispatching and return a sentence naming the upgrade.

### SR-031 [medium] DONE `internal/agent/zfs_replace.go:27` — confirmed 2/3

**The replacer's "one at a time" mutex is per-instance and the member peer route takes no other lock, so two deliveries can both wipe**

Defect: poolZFSReplaceLocal allocates a fresh vmhost.ZFSReplacer per call, so ZFSReplacer.mu serialises nothing; PoolZFSReplaceHandler calls it straight from the HTTP handler with neither poolZFSRunMu nor the flock the provisioner takes, so two concurrent replacements on one host can both pass the adopt check and both run `wipefs --all`.

Failure: The master dispatches to a member whose wipefs stalls on a flaky disk; the master's 15 s peer call times out and the operation ends needs_attention. The Controller redelivers (or the operator retries from the dialog, which mints a new request_id and therefore a new operation ID, so the journal keys differ). The master forwards again while the first run is still inside `zpool replace`; the second goroutine sees the pool does not yet hold the replacement, passes the adopt check, and its `wipefs --all` lands on the disk the first run just handed to zpool — the pool resilvers onto a disk whose labels are being erased. That is the outcome BUG-0445 exists to prevent, reached through concurrency instead of retry.

Fix: Give ZFSReplacer the same exclusive flock on its state directory that the provisioner takes (or hold poolZFSRunMu in PoolZFSReplaceHandler), and drop the per-instance mutex that cannot work.

### SR-032 [medium] DONE `internal/agent/zfs_replace.go:67` — confirmed 3/3

**The master's stale-member refusal and the peer route's pool/host binding have no checks**

Defect: Two authority rules the API reference documents for this operation — the target must be a ready member at the current membership generation, and a member accepts only a request naming its own pool and host — are not exercised by any test.

Failure: A later change drops or inverts the generation comparison. A host that was ejected and re-enrolled at a newer generation, or one still 'joining', is dispatched a `zpool replace` for a repository it no longer holds; the member's own binding is the last line of defence and it too has no check, so losing `request.HostID != s.State.HostID` lets a member run a replacement addressed to a different host with no test failing.

Fix: Add a case that bumps the member's status/generation and asserts failed with 'not a ready current member' and that poolZFSReplaceLocal was not called; and POST to the member's replace path with the master client but a foreign pool_id/host_id, asserting 400.

### SR-033 [medium] DONE `internal/agent/zfs_replace.go:183` — confirmed 2/3

**A destructive member replacement runs under the 15 s peer client timeout and the HTTP request context**

Defect: The member executes wipefs and zpool replace inside the peer handler using r.Context(), while the master's peer client gives the whole request 15 seconds; past that the master abandons a destructive operation in progress, reports the host unreachable and skips the repository-record update.

Failure: A RAIDZ1 pool has a disk failing with I/O timeouts — the situation this feature exists for. `zpool replace` blocks on the config sync past 15 s. The master's client.Do times out, the operator is told 'the selected ZFS host is unreachable' although the replacement disk has already been wiped and the replace may have committed. Because the error path is taken, the master's zfs-pool.json still names the retired disk, so its durable record and the pool disagree; the member is left at stage wipe-started and a retry under a new operation ID re-requires the fingerprint of a disk this host just wiped, refusing with 'the replacement disk changed since it was selected, or is in use'.

Fix: Give the replace dispatch its own client timeout sized for a destructive pool operation, and run the member-side work detached from the HTTP request context, reporting the outcome through its journal / next report.

### SR-034 [medium] DONE `internal/protocol/storage.go:264` — confirmed 3/3

**ZFSReplaceRequest.OperationID is not charset-validated, so the journal filename escapes the state directory**

Defect: Valid() bounds the operation ID's length only, and ZFSReplacer.Replace builds its durable journal path by concatenating it, so an ID containing '../' writes and reads a root-owned JSON file outside the journal directory — the sibling ZFS provisioner validates the same field with a strict identifier regex.

Failure: A request reaching the peer route POST /api/v1/pool/zfs-provision/replace (authenticated by the master certificate) or a managed command carrying operation_id="../managed-vms/<op>" or "../../../../etc/cron.d/nex" passes Valid(), and the host writes its journal outside <dir>/zfs-replace. The read side escapes too, so a pre-existing file at the escaped path is parsed as this operation's journal — and the journal is the only thing stopping a disk being erased twice. Reachability needs a hostile or buggy Controller/master (the Controller route mints its own ID), so this is a boundary-validation gap rather than a remote break.

Fix: Validate OperationID with the same identifier/storageOpaqueID pattern the rest of the ZFS path uses, in ZFSReplaceRequest.Valid and again in ZFSReplacer.Replace before joining the path.

### SR-035 [medium] DONE `internal/vmhost/provision_zfs.go:230` — confirmed 3/3

**Spares being re-checked and erased like data disks is untested**

Defect: request.selected() at lines 230 and 237 is what makes a hot spare pass the same eligibility/fingerprint re-check and the same wipe as a data disk — the rule the change record, API reference and guide all state — and reverting both to request.Devices breaks no test.

Failure: The re-check is lost in a refactor. An operator ticks a spare that another host has since claimed or that carries an old ZFS label. The data disks are wiped first, then `zpool create … spare /dev/disk/by-id/old-labelled` fails because the code deliberately never passes -f, so the run ends needs_attention with the data disks already erased; in the ineligible case a disk in use elsewhere is handed to the pool without the refusal data disks get.

Fix: Add one provisionHarness case with a spare reported RequiresErase (assert it appears in the recorded wipefs calls) and one reported ineligible (assert refusal before any destructive call).

### SR-036 [medium] DONE `internal/vmhost/provision_zfs.go:266` — confirmed 3/3

**`autoreplace=on` — the only thing that makes a hot spare engage — is asserted nowhere**

Defect: The spare feature's headline behaviour is the `-o autoreplace=on` property added when spares are present; no runnable check inspects the `zpool create` argument list for it, and the QA record says automatic engagement was never observed in the lab either.

Failure: The property is dropped or misspelled in a later change. Provisioning still succeeds, the pool still shows the spare AVAIL, every test stays green — and when a data disk fails months later ZFS never resilvers onto the spare, so the operator's redundancy plan silently does nothing.

Fix: Run provision() through provisionHarness with one spare and assert the recorded `zpool create` contains `-o autoreplace=on` and ends with `spare <spare by-id>`, and that a spare-free request does not carry the property.

### SR-037 [medium] DONE `internal/vmhost/storage_monitor.go:131` — confirmed 2/3

**Stale zpool detail survives a probe timeout, and the UI renders it — with a live Replace disk button — as current**

Defect: Both probe-deadline branches downgrade Status/Readable/Writable/Error in place but leave the previous probe's ZFSPool pointer (and ObservedAt) attached, so an observation Core has just declared stale still carries pool state, a failed device and a resilver percentage. The ZFS-storage-by-host table renders that with no freshness gate at all and gates the destructive action on it.

Failure: A host's storage probe starts timing out (wedged findmnt, blocking zpool call, loaded host), or the host loses its Controller connection shortly after its last healthy report. The ZFS-storage-by-host row keeps showing "Pool degraded · /dev/disk/by-id/X offline · resilver in progress · 63%" — or "Pool online" for a disk that failed after the last report — with no timestamp and no stale marker, and an enabled Replace disk button. The operator reads a percentage that stopped advancing hours ago as live progress and launches a destructive replacement from an observation Core itself no longer trusts. Core revalidates both disks against a fresh observation before acting, so no wrong disk is erased; the defect is that the displayed health is wrong and invites the action.

Fix: Clear entry.access.ZFSPool (and set ObservedAt) in both deadline branches — it is transient detail, not identity — and qualify the by-host cell the way the repositories table does: suppress or prefix it with "Last report" when `host.connection !== "fresh"` or `!storageObservationFresh(...)`, and gate the Replace disk button on the same freshness.

### SR-038 [medium] DONE `internal/vmhost/zfs_replace.go:84` — confirmed 3/3

**A replacement disk already anywhere in the pool is adopted as success, skipping every check, and the master then corrupts its durable record**

Defect: zfsPoolHoldsDisk is consulted before the failed leaf is located and before checkSystemDiskDevice runs, so a request naming a disk that is already an ordinary healthy member of the same pool returns nil with nothing executed; the master treats that as success and rewrites the failed leaf's entry in zfs-pool.json to name that disk.

Failure: A RAIDZ2 pool loses DATA-1 and DATA-2 and one blank disk DATA-9 is fitted. The operator replaces DATA-1 with DATA-9, then immediately reopens the dialog (the stale discovery listing still shows DATA-9 as usable) and submits DATA-2 + DATA-9. By the time the second command runs the pool holds DATA-9-part1, so replace() returns at :84 with nothing executed: the Controller reports 'succeeded', DATA-2 is still failed, and the master's zfs-pool.json now names DATA-9 for both DATA-1 and DATA-2. A fresh Controller re-adopts a repository whose device list does not match the pool.

Fix: Adopt only when the pool state corresponds to this request: require that request.OldDevice is absent (ZFS already retired it) or that it is present with a running RESILVER scan alongside the replacement; otherwise fall through and refuse with 'this disk is already part of the pool'.

### SR-039 [medium] DONE `internal/vmhost/zfs_replace.go:103` — confirmed 4/6; confirmed 4/6

**The 'wipe-started' stage is journalled before wipefs runs, and the resume path it unlocks skips the erase-confirmation check**

**Nothing tests that the wipe is journalled before the disk is erased**

Defect: journal.Stage is set to wipe-started and persisted before the wipe is attempted, so a run that never erased anything still takes the relaxed resume path on redelivery; that path returns as soon as by-id, serial, size and eligibility match and never re-checks RequiresErase, so a disk that acquired data since it was selected is erased with no operator confirmation.

Failure: The operator selects a blank disk (eligibility 'blank', so the request carries no erase confirmation). Core journals wipe-started, then `systemd-run wipefs` fails (device busy) and the operation ends needs_attention with nothing erased. Before redelivery the disk is put to use on the host (a filesystem written and unmounted). On redelivery journal.Stage == "wipe-started", requireFingerprint is false, checkSystemDiskDevice returns at the first branch, and `wipefs --all` destroys that data although the request carries no confirmation for a disk that now requires one.

Also: The durability guarantee this slice advertises — an agent interrupted after the wipe resumes from the wipe-started stage — is enforced only by the ordering of the journal write before `wipefs --all`, and no check asserts that ordering.

Fix: Record the wipe boundary only when a wipe is actually about to be issued (as the provisioner does), or keep the RequiresErase/EraseConfirmation check in the resume path — it is the fingerprint alone that cannot survive a wipe, not the confirmation rule. Give the replacer the provisioner's stage recorder (provision_zfs_test.go:389 recordZFSProvisionStages): make saveZFSReplaceJournal a var, record each stage into the same call list, and assert the wipe-started index is lower than the first wipefs index.

### SR-040 [medium] DONE `internal/vmhost/zfs_replace.go:107` — confirmed 3/3; confirmed 2/3

**The replacement disk is erased before anything checks it is large enough for the failed leaf**

**The ZFS replacement disk is erased before anything checks it is large enough, and zpool's refusal is discarded**

Defect: Neither the Controller, the master nor the executing host compares the replacement's size with the disk it replaces; `wipefs --all` runs first and `zpool replace` is what discovers 'device is too small', leaving an erased good disk and a still-degraded pool. The guide and the UI state the rule the product does not enforce.

Failure: A 2 TB mirror member fails. The host has a leftover 500 GB SSD holding an old filesystem; it is the first row in the dialog (smallest first). The operator selects it, types the by-id erase confirmation and submits. Core revalidates identity and eligibility (both pass), runs `wipefs --all` on the 500 GB disk destroying its contents, then `zpool replace` fails; the operation ends needs_attention with '<old> could not be replaced with <new>'. A disk was destroyed for an operation that could never have succeeded.

Also: The ZFS replace worker wipes the chosen disk and only then runs `zpool replace`; nothing compares its size with the leaf being replaced, and the error from zpool is replaced by a generic sentence that names neither the reason nor the command output. The sibling system-disk path does check the size before the wipe (md_replace.go:140-144), and the Controller/UI check nothing for ZFS either.

Fix: Compare request.Device.SizeBytes against the failed disk's size before the wipe — the master already holds it in its repository record (internal/agent/zfs_replace.go:113-118 matches outcome.Devices on OldDevice) — or carry the leaf size in ZFSPoolDevice so the dialog can hide undersized disks. Compare the replacement's size against the failed leaf's device (from the same discovery listing or `zpool list -v`) before the wipe, as the md path does, and carry zpool's own reason into the result error.

### SR-041 [medium] DONE `web/src/managed.tsx:16` — confirmed 2/3; confirmed 2/3

**StorageAccess.zfs_pool is produced, transported and documented but has no consumer, so a registered ZFS repository shows "degraded" with no device detail**

**The TypeScript Storage.access type omits the new zfs_pool field**

Defect: inspectZFS attaches the pool observation to every ZFS repository and it is carried in protocol, both OpenAPI documents and docs/API.md, but the TypeScript Storage["access"] type does not declare it and nothing in web/src reads it; only zfs_host_access[].pool is rendered, which exists solely for Core-provisioned repositories.

Failure: An operator registered an existing ZFS dataset (the advanced path, not the provisioning wizard) and one of its mirror members fails. The Storage row turns amber with the single word `degraded` and "Readable · Writable"; the failed by-id device, the status/action sentences and the resilver Core did send are dropped on the floor, and the troubleshooting page points at a host row that does not exist for this repository. Meanwhile every provisioned repository's report carries the identical pool status twice, only one copy of which is ever read.

Also: StorageAccess gained zfs_pool on the Go and OpenAPI side, but the UI's `access` type stops at `error`, so a registered (not Core-provisioned) ZFS dataset never shows the pool detail the new contract carries.

Fix: Either add `zfs_pool?: ZFSPoolStatus` to the access type and render `zfsPoolSummary(s.access.zfs_pool)` for `kind === "zfs"` rows with no zfs_host_access, or drop StorageAccess.ZFSPool from the wire contract, keep the value as an internal field feeding zfsHostAccess, and correct docs/API.md, both OpenAPI documents and the guide bullet. Add `zfs_pool?: ZFSPoolStatus` to the access type and render zfsPoolSummary(s.access.zfs_pool) on the repository row, or state in docs/API.md that StorageAccess.zfs_pool is reported but not yet surfaced for registered datasets.

### SR-042 [medium] DONE `web/src/storage.tsx:123` — confirmed 2/3

**Resilver percentage is computed from `issued` but printed beside the `examined` sizes, so the line contradicts itself**

Defect: zfsScanProgress derives the percentage from scan.issued but always renders scan.examined / scan.to_examine in the same string, so a running resilver reads as "1% (4.88G of 4.89G)"; the new unit test freezes the mismatch as expected output.

Failure: A mirror is resilvering. The host row reads "resilver in progress · 1% (4.88G of 4.89G)": the operator is told simultaneously that the repair is 1% done and that 4.88 of 4.89 GiB are through, and the natural reading (almost finished) is the wrong one. They either wait on a repair that has barely started or escalate a healthy one, while deciding when it is safe to touch the pool. The lab walkthrough never saw this (its resilver finished in one second, docs/qa/2026-09-22-zfs-degraded.md step 8), so the unit test is the only check and it asserts the mismatch.

Fix: Print the quantity the percentage came from: `(${scan.issued ?? scan.examined} of ${scan.to_examine})`, or label the two figures the way zpool status does ("49% issued · 1.46G examined of 4.89G"), and update the assertion to the corrected pairing.

### SR-043 [medium] DONE `web/src/storage.tsx:125` — confirmed 2/3

**The resilver percentage is computed from `issued` but printed beside `examined`, and a new test freezes the contradiction**

Defect: zfsScanProgress takes the numerator from `scan.issued ?? scan.examined` but always prints `scan.examined` in the parenthetical, so the two halves of the same sentence disagree — on this batch's own captured lab output it reads '1%' beside numbers that look complete.

Failure: An operator replaces a failed mirror member and watches the row the guide tells them to watch. It reads "resilver in progress · 1% (4.88G of 4.89G)": the percentage says the repair has barely started, the figures beside it say it is all but done. The guide promises 'the percentage it has finished and how much of the pool it has examined', and the line is self-contradictory. The test locks it in, so a fix looks like a regression.

Fix: Print the quantity the percentage came from — `(${scan.issued ?? scan.examined ?? "?"} of ${scan.to_examine ?? "?"})` — or label both figures as ZFS does, and update the assertion to `49% (2.44G of 4.89G)`.

### SR-044 [medium] DONE `web/src/storage.tsx:130` — confirmed 2/3; confirmed 2/3

**Per-device error counts and ZFS's own status/action sentences never raise attention; a pool with data errors reads "Pool online"**

**zfsPoolSummary re-implements zfsFailedDevices, which the same file already imports**

Defect: zfsPoolSummary() decides attention purely from vdev state strings and ignores the read/write/checksum counters and the status/action sentences Core now collects, so a pool ZFS reports as ONLINE while carrying unrecoverable errors renders as plain "Pool online" with no badge — the exact signal the slice exists to surface.

Failure: A mirror develops checksum errors on one member that ZFS repairs from the other. zpool status reports state ONLINE, every leaf ONLINE, cksum 47 on one leaf, and `status: One or more devices has experienced an unrecoverable error…` with `action: Determine if the device needs to be replaced…`. The Storage page renders the plain text "Pool online", no amber badge, and the action sentence only inside a title tooltip. The operator sees a healthy-looking repository and never learns a disk is failing until it goes DEGRADED or the pool loses data.

Also: zfsPoolSummary re-derives the failed-leaf list and the path-or-name naming rule that zfs.tsx already exports as zfsFailedDevices and that storage.tsx imports at line 4 and uses at line 453 — two copies of "which leaf counts as failed".

Fix: Treat a non-zero read/write/checksum count on any leaf, or a non-empty pool.status, as attention, and render the offending device with its counts (e.g. `…DATA-5-part1 · 47 checksum errors`) in the summary line rather than only in the tooltip. Use `const failed = zfsFailedDevices(pool);` in zfsPoolSummary and hoist the double call; note web/src/storage.test.mjs:129 pins the current spelling with a source regex and would need updating.

### SR-045 [medium] DONE `web/src/storage.tsx:453` — confirmed 3/3

**Replace disk stays enabled while the replacement resilver is running, and a second submission wipes another disk**

Defect: The button is gated only on the old leaf still being non-ONLINE, which is exactly its state during the resilver the previous replacement started; a second submission with a different disk falls past Core's adopt check and reaches wipefs before ZFS refuses the nested replace.

Failure: An operator submits a replacement, sees the row still offering 'Replace disk' (the old leaf is OFFLINE until the resilver ends), assumes the first attempt did not take, and replaces the same failed disk with a second spare disk. Core wipes that second disk and only then runs `zpool replace`, which ZFS rejects because the leaf is already inside a replacing vdev. needs_attention, one disk erased for nothing.

Fix: Disable or replace the button while the pool observation reports a scan in progress on that host (zfsPoolSummary already decodes pool.scan) and show 'resilvering' in the action cell instead.

### SR-046 [medium] DONE `web/src/zfs.tsx:175` — confirmed 3/3

**The wizard's "a spare must leave a redundant pool" rule is bypassed by unticking a data disk afterwards, and nothing downstream re-applies it**

Defect: toggleSpare refuses a spare below three data disks, but toggle() removes data disks with no re-check, review() only rejects spares with zero data disks, and ValidateZFSProvision counts layout on len(host.Devices) alone — so a single-disk pool with a hot spare is submitted and created.

Failure: On a host with three blank disks: tick all three, tick Spare on the third (allowed at that moment), then untick one of the two remaining data disks. The summary reads "1 data disk + 1 hot spare → Single disk … survives no disk failures" and the wizard submits it. Core erases both disks and creates a non-redundant single-disk pool with a hot spare ZFS can never resilver onto — the exact outcome the wizard's own error message says the rule prevents.

Fix: Apply the rule where the selection is read, not where the tick is set: in review()/zfsProvisionHosts reject (or drop) spares on a host with fewer than two data disks, and mirror it in ValidateZFSProvision by rejecting Spares when the resolved layout is "single".

### SR-047 [low] DONE `docs/changes/2026-09-22-zfs-replace.json:78` — unverified

**zfs-replace change record records contract-test failures that do not exist in the staged tree**

Defect: The record's verification method says test_api_contract still fails on lane D's SystemDiskReplaceCommand/SystemDiskReplaceRequest schema and a missing host-system-disk-replace kind enum entry. Those files are staged in the same batch and the suite passes.

Failure: A reviewer checking the batch's evidence sees a change record asserting failing contract tests, cannot reproduce them, and has to work out whether the record is stale or the tree differs from what was reviewed.

Fix: State that the suite passes in the combined tree (the lane D schemas landed in the same batch), keeping the note that those schemas belong to the system-disk record.

### SR-048 [low] DONE `docs/guide/storage-repositories.md:169` — unverified

**"Any number of spares can be ticked" overstates what toggleSpare allows**

Defect: Each Spare tick is refused unless at least three data disks are still ticked on that host, so the number of spares is bounded by the disks fitted, not unlimited.

Failure: An operator with four disks ticks two spares fine, then tries a third and gets the "Keep at least two data disks" error the guide never mentions, unable to tell a rule from a bug.

Fix: State the actual rule — each spare tick needs three data disks still ticked — quoting the error the wizard shows.

### SR-049 [low] DONE `docs/guide/storage-repositories.md:237` — confirmed 3/3; confirmed 3/3

**Typo "resilving" in the hot-spare paragraph of the operator guide**

**Typo "resilving" in the hot-spare paragraph**

Defect: The new "When a disk in a provisioned ZFS repository fails" section misspells resilvering.

Failure: An operator searching the guide for "resilvering" while a pool is repairing itself misses this paragraph, the only place the guide explains that a hot spare engages by itself and that the failed disk should still be replaced.

Also: The guide's new hot-spare paragraph misspells "resilvering"; it is the only occurrence in the tree.

Fix: "resilvering onto the spare". resilving -> resilvering.

### SR-050 [low] DONE `docs/guide/storage-repositories.md:253` — unverified

**Step 3 describes an ONLINE disk appearing in the failed-disk list, which cannot happen**

Defect: The failed-disk picker is built from `zfsFailedDevices`, which filters out every device whose state is ONLINE, so a disk "listed as online" is impossible in that list.

Failure: An operator whose expected disk is still ONLINE opens the dialog, does not see it listed at all, and finds no guidance — the sentence written for exactly this case describes a listing state the UI cannot produce.

Fix: Say a disk ZFS still reports as ONLINE is not offered at all, so a missing disk means the pool does not consider it failed; keep the ONLINE refusal with the other needs_attention messages.

### SR-051 [low] DONE `docs/guide/storage-repositories.md:260` — unverified

**Literal <disk> placeholder will be swallowed as an HTML tag when the page is rendered**

Defect: The confirm button is written as `**Erase <disk> and replace**`; Markdown passes `<disk>` through as raw HTML, so a renderer drops it and the sentence reads "Erase  and replace".

Failure: On the rendered guide site the instruction for the step that arms a destructive erase reads "Erase  and replace", with a gap where the placeholder was; check_docs checks structure and links, not rendered HTML.

Fix: Use `**Erase _disk_ and replace**` or backtick the literal string.

### SR-052 [low] DONE `docs/guide/storage-repositories.md:276` — confirmed 2/3

**The guide's "nothing was erased" reassurance omits the two failures that happen after the wipe**

Defect: The recovery paragraph lists only the pre-wipe refusals and then states 'Nothing was erased in those cases'; the two messages that can only occur after `wipefs --all` has run are not mentioned, so an operator reads the reassurance as covering every needs_attention outcome.

Failure: A replacement fails with 'could not be replaced with' (for example the disk was too small — see the pre-wipe size finding). The operator reads this paragraph, concludes the disk was not erased, and puts it back into service or hands it on as if its previous contents were intact.

Fix: Add the two post-wipe messages and what they mean for the disk: it has been cleared, the pool is unchanged, and retrying the same replacement is what finishes it.

### SR-053 [low] DONE `internal/agent/zfs_replace.go:87` — confirmed 2/3

**poolZFSMember is a byte-identical copy of the existing poolMemberByID**

Defect: The new helper duplicates poolMemberByID, which already exists in the same package with the same signature and body.

Failure: No runtime failure. A future change to member lookup (for example skipping ejected members the way pool_vm_inventory.go:275 does) has to be found in three places, and only one will be updated.

Fix: Delete internal/agent/zfs_replace.go:87-94 and call poolMemberByID(membership, request.HostID) at :66. Net -8 lines.

### SR-054 [low] DONE `internal/controller/zfs_replace.go:59` — confirmed 3/3

**The Controller's per-host capability check and host freshness check are untested**

Defect: Neither the fact that both the pool master and the selected host are capability-checked, nor the managedHost freshness/pool-membership refusal, is detected by the relay tests.

Failure: The loop is narrowed to the master in a cleanup. An upgraded master in a pool with a not-yet-upgraded member accepts the request and forwards it over the peer route to a Core that does not implement it; the operator gets an opaque late failure instead of the documented 409 naming storage-zfs-replace-v1. Losing managedHost similarly lets a revoked or foreign-pool host be named as the target.

Fix: Give the lab a second capable host in the master's pool, then submit with that host lacking storage-zfs-replace-v1 (assert 409) and with a revoked/foreign host id (assert the 'fresh, active and in the selected pool' 409).

### SR-055 [low] DONE `internal/protocol/storage_test.go:263` — confirmed 2/3

**The "spares count apart from the layout" test does not test that**

Defect: TestValidateZFSProvisionCountsSparesApartFromTheLayout asserts a mirror of two data disks plus one spare is accepted, but since this same slice widened mirror to accept three devices, that case passes just as well if spares were counted toward the layout.

Failure: A refactor counts spares in the layout check. A request for mirror with one data disk and one spare is then accepted by the Controller and reaches Core, which builds a single-disk vdev plus a spare while the operator was told they were getting a mirror — and the protocol test named for this exact rule stays green.

Fix: Add the asymmetric cases the rule is about: mirror with 1 device + 1 spare and raidz1 with 2 devices + 1 spare must both be refused.

### SR-056 [low] DONE `web/src/storage.tsx:138` — confirmed 4/6; confirmed 4/6; confirmed 2/3

**ZFS's status and action sentences are reachable only as a mouse-hover title attribute**

**The pool summary is put in a `.badge`, which is white-space: nowrap, so a degraded line forces the storage table wide**

**ZFS's status/action sentences are hover-only on a non-focusable element, and the nowrap badge overflows**

Defect: The only place the UI shows ZFS's own explanation and repair instruction is a `title=` attribute on a non-focusable <small>. A title tooltip is not reachable by keyboard and is inconsistently exposed by screen readers, so a keyboard-only operator gets the state word and nothing else.

Failure: A keyboard-only or screen-reader operator opens the Storage page for a repository reporting "Pool degraded · /dev/disk/by-id/…-part1 offline". Tabbing through the page never reaches the <small> and no accessible name carries the text, so the sentences ZFS printed — what happened and what to do about it ("replace the device with 'zpool replace'") — are unreachable. They get a state word for a redundancy failure and no instruction.

Also: A degraded summary is easily 100+ characters (state, by-id device path, resilver, percentage, sizes) and the badge class forbids wrapping.

Also: The pool's status and action sentences — what happened and what to do — are exposed only through a `title` on a `<small>`, unreachable by keyboard or touch; the same element uses `.badge`, which is white-space: nowrap, while the summary text is long.

Fix: Render the status/action sentences as visible text under the summary line (or in a <details>/expandable row) rather than as a title attribute only. Add `white-space: normal` for this badge (a `.zfs-pool-summary` class next to the existing `.pool-fencing-cell > .badge` rule), or drop the badge class and colour the text instead. Render the status/action sentences as visible text (a `<small>` under the badge, or a `<details>` like BlockedDisks) and let the badge wrap in a table cell.

### SR-057 [low] DONE `web/src/storage.tsx:454` — confirmed 2/3

**The replace dialog keeps the pool snapshot it was opened with, making its "no failed disk any more" empty state unreachable**

Defect: replaceZFS.pool is captured once on click and never updated by the inventory reload, so the dialog's failed-disk list cannot change while it is open.

Failure: The operator opens Replace disk and leaves it open while someone runs `zpool online` on the host (or a hot spare engages). The dialog keeps listing the now-ONLINE disk as failed; submitting is refused by Core with "…is online, so there is nothing to replace" (internal/vmhost/zfs_replace.go:95) and the operator gets an unexplained Needs-attention task instead of the empty-state sentence written for exactly this case.

Fix: Look the pool up from the live `storage` prop inside the render (keep only repositoryID/host in state), so the dialog re-renders from each reload and the written empty state actually appears.

## Installer mirror and minimal-install NIC skip

### SR-058 [high] DONE `installer/late-install.sh:124` — unverified

**fstab pins /boot/efi to one member's ESP, so replacing that disk leaves the UEFI mirror with no maintainable ESP and fails the replace operation**

Defect: The mirrored install mounts /boot/efi from exactly one member's ESP by UUID and only adds `nofail`; nothing ever re-points that entry, so once the disk whose ESP fstab names is gone the mount never returns, `nex-sync-esp` refuses to run, and the Controller's "Replace a failed system disk" path reports the replacement as not bootable.

Failure: Two-disk UEFI mirror; fstab has `UUID=<ESP-on-disk-A> /boot/efi vfat umask=0077,nofail 0 1`. Disk A fails, the operator powers off to swap it and boots from disk B's copied ESP. /boot/efi is now unmounted and is an empty directory on the root md filesystem. Replace a failed system disk: mdInstallBootloader mkfs.vfat's the new ESP, runs /usr/local/sbin/nex-sync-esp, which exits 1 at `[ -d /boot/efi/EFI ]`; the mdCopyESP fallback exits 1 on the same guard; the operation fails at the `bootable` stage with the new disk carrying an empty ESP. Worse, every later grub-efi-amd64 upgrade writes into the plain /boot/efi directory on the root filesystem and the APT hook's nex-sync-esp silently exits 1, so no ESP on the host is ever updated again, eventually pairing an old grubx64.efi with newer /boot/grub modules.

Fix: Do not pin /boot/efi to one member. Either drop the fstab entry and have nex-sync-esp mount each member's ESP on demand, or mount whichever member ESP is present and have the replace path rewrite the fstab UUID to the ESP it just created. While touching the line, add the short `x-systemd.device-timeout` lane D's own evidence (docs/qa/2026-09-22-system-disk-replace.md:96-97) recorded as part of the working fix, and add a lab check that boots with the ESP-owning disk detached.

### SR-059 [high] DONE `tests/integration/installer-hostname.py:12` — unverified

**This batch breaks installer-hostname.py: the installer CI job fails and the preseed renderer loses its only executable test**

Defect: nex-config.c now declares `disks`/`disk` above the line installer-hostname.py splits on, so the extracted render block references undeclared identifiers and the harness no longer compiles. It runs in the installer CI job under `set -eu`, so the job goes red and the only test that executes the seed renderer stops running.

Failure: Push this batch to the forge. The `installer` job reaches `python3 tests/integration/installer-hostname.py`, cc exits 1, `set -eu` aborts the step, and the remaining steps (installer-link.py, installer-disk.py) never run. Until fixed, no test executes nex-config's answers.cfg renderer at all, so the new RAID recipe, member device names and grub-installer seeding are covered only by substring greps of the C source.

Fix: Move `gsize disks=0; char **disk=disk_list(config,"disk",&disks);` below the `char *hostname=value(config,"hostname"),...` line in nex-config.c:164-166 so the existing split anchor captures them, and change installer-hostname.py:34 `g_free(disk)` to `g_strfreev(disk)`. Then add two- and three-disk mirror cases to its `check()` asserting the generated `partman-auto-raid/recipe`, `partman-auto/disk` and `grub-installer/bootdev` lines.

Note: Verified inline after the review: the harness fails to compile against the staged nex-config.c (`disks` undeclared), so the installer CI job is red.

### SR-060 [medium] DONE `installer/check.sh:129` — unverified

**The mirror preseed is checked by grepping nex-config.c's source, not by generating it**

Defect: Every assertion for the new RAID recipe (check.sh:118-138) is a literal substring match against the text of nex-config.c, including the format strings themselves. Nothing runs the generator, so any bug in how the format strings are filled - member device names, disk count, target list - passes every check in the repository.

Failure: Invert the NVMe suffix test at nex-config.c:190 to `g_ascii_isdigit(...) ? "" : "p"`. `bash installer/check.sh`, installer-disk.py, installer-text-wizard.py and the whole installer CI job still pass. A two-NVMe mirror then seeds `d-i partman-auto-raid/recipe string 1 2 0 ext4 / /dev/nvme0n13#/dev/nvme1n13 .`; partman-auto-raid cannot find those devices and the install stops with no root filesystem. The same holds for the disk count (`%u`) and the `partman-auto/disk` target list.

Fix: After repairing installer-hostname.py, drive `render()` with one-, two- and three-disk `disk` lists covering /dev/sda, /dev/nvme0n1 and /dev/vda and assert the exact generated lines (`partman-auto/disk string ...`, `partman-auto-raid/recipe string 1 2 0 ext4 / /dev/sda3#/dev/nvme0n1p3 .`, presence/absence of `grub-installer/bootdev`, and that the single-disk case still emits `partman-auto/method string regular`). Then drop the substring assertions.

### SR-061 [medium] DONE `installer/check.sh:179` — unverified

**The /boot/efi nofail rewrite is asserted by two disconnected substrings, so any sed passes**

Defect: The fstab rewrite that stops a mirrored host dropping into emergency mode after a disk failure is guarded only by `assert ',nofail' in late and '/boot/efi' in late`. Those two strings appear in unrelated places in the file, so the assertion holds for essentially any sed expression. The change record itself says the rewrite was exercised once by hand against a sample fstab.

Failure: Someone tightens or retypes the expression - drops the `vfat` group, anchors on `\<vfat\>` in a way that misses partman's actual spacing, or the ESP is generated with a different fs type spelling. `bash installer/check.sh` still passes because `,nofail` and `/boot/efi` are still somewhere in the file. The installed host then has a bare `/boot/efi` entry; when a mirror disk is replaced or fails, systemd's local-fs.target fails on the missing ESP and the host drops to emergency mode while the RAID1 root is perfectly healthy.

Fix: Turn the manual probe into six lines in installer/check.sh: extract the sed expression from late-install.sh, apply it with `subprocess.run(['sed', expr], input=<fixture fstab>)`, and assert the /boot/efi line gained exactly `,nofail` while the root and cdrom lines and the `# /boot/efi was on ...` comment are byte-identical.

### SR-062 [medium] DONE `installer/gui/wizard-text.sh:371` — unverified

**Text frontend never preselects a lone eligible disk, although the guide states the preselection for both installers**

Defect: `step_disk` never calls `db_set nex-installer/disk`, so the multiselect opens with nothing ticked; on a single-disk machine pressing Enter produces the "Select at least one disk" error and re-asks. The GTK wizard ticks a lone eligible disk, and the guide claims the behaviour for both installers.

Failure: Minimal (text) install on a machine with one eligible disk. The operator reaches "Installation disks:", sees the single disk unticked, presses Enter as they do for every other question, and gets "Installation disks not accepted - Select at least one disk: one to install on, or two or more to mirror." before the question is asked again. The guide told them the disk would already be ticked.

Fix: Preselect a lone eligible disk in step_disk (`[ $(set -- $disks; echo $#) -eq 1 ] && db_set nex-installer/disk "$choices"` before the first ask) to match the GUI, or scope the guide/README sentence to the graphical wizard.

### SR-063 [medium] DONE `installer/late-install.sh:86` — unverified

**Mirror block is gated on any md array in the installer, not on the target root being a mirror**

Defect: `if grep -qs "^md" /proc/mdstat; then` fires whenever the d-i kernel has any array assembled, so a single-disk install on a host that still carries an old mirror on unselected disks writes those foreign arrays into the installed /etc/mdadm/mdadm.conf and bakes them into the initramfs.

Failure: Host has sda (install target) plus sdb+sdc carrying a previous Nex mirror. The operator ticks only sda; recheck.sh wipes sda and leaves the sdb/sdc array alone, so /proc/mdstat still shows `md127 : active raid1 sdb3[0] sdc3[1]`. The guard is true even though the new root is /dev/sda3 (plain ext4): mkconf writes `ARRAY /dev/md/127 ... UUID=<old array>` into the installed /etc/mdadm/mdadm.conf, update-initramfs bakes it in, the nex-sync-esp APT hook is installed on a host with no mirror, and the installed host assembles (and can resync, i.e. write to) the operator's unselected disks on every boot.

Fix: Gate on the target root and emit only that array:

    root=$(findmnt -no SOURCE /)
    case $root in /dev/md*)
        { /usr/share/mdadm/mkconf | grep -v '^ARRAY'; mdadm --detail --scan "$root"; } > /etc/mdadm/mdadm.conf
        update-initramfs -u
        ...
    esac

(`findmnt -no SOURCE /` in the chroot already reports the real root device, which is what nex-sync-esp itself relies on at :96.)

### SR-064 [medium] DONE `installer/late-install.sh:89` — unverified

**nex-sync-esp is never parsed or executed by any check - sh -n cannot see inside the heredoc**

Defect: The new nex-sync-esp copier is written into late-install.sh as heredoc data. installer/check.sh:8 runs `sh -n installer/late-install.sh`, which treats the heredoc body as text, so the script is not even syntax-checked; the only checks are four substring greps. It runs on every mirrored host at install time and after every APT transaction, and the QA record lists all booted-host behaviour as outstanding.

Failure: Any future edit inside the heredoc that breaks shell syntax, or gets a flag wrong (`lsblk -nro PATH,PARTTYPE`, `mdadm --detail --export`, the `MD_DEVICE_[^=]*_DEV=` sed), passes `bash installer/check.sh` and the whole installer CI job. It then fails silently on the installed host: :121 invokes it as `/usr/local/sbin/nex-sync-esp || true` and the APT hook is also `|| true`, so nothing surfaces. The other members' ESPs are never written, and the first time the operator pulls the primary disk the mirror is healthy but the firmware has nothing to boot - the precise scenario the feature exists for.

Fix: Extract the embedded scripts in installer/check.sh and syntax-check them, e.g. `awk '/<<.ESP.$/,/^ESP$/' installer/late-install.sh | sed '1d;$d' | sh -n` (and the same for the TARGET heredoc). Better still, ship nex-sync-esp as a real file under installer/ that late-install.sh copies, so `sh -n` covers it directly and a fixture test can run it against fake findmnt/mdadm/lsblk/mount on PATH - the pattern installer-handoff.py already uses for recheck.sh.

### SR-065 [medium] DONE `tests/integration/fresh_install_gui.py:44` — unverified

**GUI lab harness still Ctrl-clicks the Disk rows, but the shipping Disk step is a tick list where row selection is inert**

Defect: The `--mirror` path (and the plain disk click) drive the pre-rework multi-selection UI. nex-wizard.c no longer has a disk selection handler, so clicking a row at x=300 ticks nothing; the lab silently proceeds without ever reaching Review, and can never produce the two-disk install the outstanding QA items need.

Failure: Run the outstanding acceptance `fresh_install_lab.py --prepare --disks 2` then `fresh_install_gui.py --stage configure --mirror`. Neither click reaches the toggle cell, `disks_changed()` is never invoked, `selected_disks` stays 0, and `collect()` (nex-wizard.c:577) blocks Continue with "Select the disk to erase and install Nex Core onto, or two or more disks for a mirror." `shot('installer-review')` and `shot('installer-confirmation-required')` both capture the Disk page, the blank-target assertions pass, the stage reports success, and the `install` stage then presses Alt+E on the Disk page. QA items 1-3 of docs/qa/iso/2026-09-22-installer-mirror.md can never be closed by this harness. The single-disk path only passes by accident: nex-wizard.c:464 `if (disks->len == 1)` pre-ticks the lone disk.

Fix: Click inside the "Use" column (around x=180), or focus `disk_list` and drive it from the keyboard with down/space per row, which `disk_key()` handles. Drop the Ctrl modifier, its coordinates and the stale comment.

### SR-066 [medium] DONE `tests/integration/installer-handoff.py:47` — unverified

**recheck.sh's new multi-disk wipe loop is never exercised; the BUG-0413 regression still uses a one-disk handoff**

Defect: recheck.sh is the destructive step - it stops arrays and zeroes the first and last MiB of every partition and of each whole disk. This change turned it into a loop over a `;`-separated list, but the only behavioural test of it still writes a single-disk handoff, so the split, the per-disk marker and the second disk's wipe are unverified.

Failure: The handoff separator or the loop changes (e.g. the wizard moves to a comma-separated list, or the marker is hoisted out of the loop so only the first disk is wiped). `bash installer/check.sh` needs only its one literal string updated and installer-handoff.py keeps passing, because it never selects two disks. On a real two-disk install the second member keeps its previous RAID/LVM superblock, partman-auto stops silently with "No root file system" - exactly the BUG-0413 failure this script exists to prevent - and no automated check saw it.

Fix: Add a second scenario alongside the BUG-0413 one: `disk=/dev/sda;/dev/sdb;`, an mdstat where sda3 and sdb3 are in different arrays plus a third array on sdc; assert both md devices were stopped (and not the third), that sda/sda3 and sdb/sdb3 are zeroed at both ends and untouched in the middle, that sdc is byte-identical, and that a second run creates no new writes.

### SR-067 [low] DONE `installer/gui/nex-wizard.c:331` — unverified

**The same two rules are worded four different ways across the frontends**

Defect: The GTK live summary, the GTK Continue error, the text frontend and nex-config each phrase the 16 GiB and 10% rules differently, so the operator sees the rule change wording as they move through the step.

Failure: A GTK operator ticks two mismatched disks, reads "Mirror disks must be within 10% of the smallest ticked disk", presses Continue and is told "Every mirror disk must be within 10% of the smallest selected disk's capacity" - a second, differently worded message for the same refusal, with no explanation of why a mirror is bounded by its smallest member (which only the text frontend gives).

Fix: Use one sentence per rule everywhere, preferably the text frontend's, which is the only one that states the reason; add the similar-disk tip to the nex-installer/disk template description.

### SR-068 [low] DONE `installer/gui/nex-wizard.c:341` — unverified

**The mirror summary reports the whole smallest disk as "usable", ignoring the two boot partitions and md metadata**

Defect: `disks_changed()` prints the smallest member's full disk capacity as the mirror's usable size; the root filesystem is that minus the 1 MiB BIOS boot partition, the 538 MB ESP and the md 1.2 superblock. The guide repeats the inflated example.

Failure: Two 128 GiB disks are ticked. The summary and the guide both promise "128.0 GiB usable"; the installed root filesystem is about 127.4 GiB. An operator sizing VM storage from that line is short by roughly half a gibibyte per host.

Fix: Either drop the word "usable" (say "smallest disk 128.0 GiB"), or subtract the fixed 539 MB of boot partitions before formatting the number, and match the guide example.

### SR-069 [low] DONE `installer/gui/nex-wizard.c:440` — unverified

**GTK offers disks under 16 GiB and then refuses them; the text frontend never lists them**

Defect: `discover()` filters on supported name, `partition`, `ro`, `removable`, media and non-zero size, but not capacity, so a 2 GiB disk appears in the tick list. The text wizard drops anything below MIN_SECTORS at discovery.

Failure: A machine with one 500 GiB disk and one 8 GiB boot SSD. In the graphical wizard the 8 GiB disk is listed; ticking it turns the summary red with "Every disk must be at least 16 GiB." In the minimal install on the same machine it is not offered at all. The guide's "the same rules apply as in the graphical wizard" covers the outcome but not the different set of disks each frontend shows.

Fix: Skip disks below 16 GiB in `discover()` as the text wizard does, so both frontends offer the same set and the 16 GiB branch in `disks_changed()`/`collect()` becomes unreachable and can go.

### SR-070 [low] DONE `installer/gui/wizard-text.sh:312` — unverified

**Text frontend renames the disabled NIC mode to "skip" while the graphical wizard still says "Disabled"**

Defect: The same stored state (`mode=disabled`) is presented as **skip** in the text frontend and **Disabled** in the GTK combo, and the guide uses both words in the same walkthrough.

Failure: An operator who installed one host through the minimal frontend, where an adapter is left "skip", then installs a second host through the GUI and looks for "skip" in the IPv4 mode list. There is none; the equivalent is "Disabled", and nothing tells them the two are the same state.

Fix: Pick one operator-facing word for the unconfigured state and use it in both frontends and throughout the guide. (Note: this wording belongs to the sibling minimal-nic-skip record but lands in an installer-mirror scope file.)

### SR-071 [low] DONE `installer/gui/wizard-text.sh:377` — unverified

**Text disk choices omit the serial, while README and the guide claim both frontends show the same facts including serial**

Defect: The text choice line is name, capacity, type, model, contents. The GTK list has a dedicated Serial column. Both docs state the serial is in both lists.

Failure: Two identical-model disks in a machine installed through the minimal (text) frontend. The operator was told by the guide that each row names the serial and wants to tick the two disks whose serials are on the caddy labels. The choices show only `sda - 932 GiB - HDD - ST1000NM0033 - blank` and the same for sdb; the serials appear only after the selection is made, on the erase confirmation.

Fix: Append `${serial:+ - $serial}` to the text choice entry (and add a `wizard` assertion in installer/check.sh), or correct installer/README.md:106 and docs/guide/install-nex-core.md:114 to say the serial is shown in the graphical list and on the text review.

### SR-072 [low] DONE `installer/gui/wizard-text.sh:387` — unverified

**The text wizard's empty-selection retry is untested**

Defect: step_disk gained a retry loop with two failure reasons. installer-text-wizard.py covers the capacity-mismatch reason but never unticks everything, so the empty-selection branch and the awk parse of an empty multiselect answer are unexercised.

Failure: The operator unticks every disk on the Disk step. If the awk expression or the `-z` guard regresses (for example a future edit makes awk emit a bare space for an empty answer, so `[ -z "$v_disks" ]` is false), the wizard writes `disk=` with empty size/serial/major-minor lists and hands off to nex-config, which refuses with the generic "Select the disk or disks to erase and install Nex Core onto." from the apply step instead of re-asking the Disk question in place. Nothing in CI notices the step stopped retrying.

Fix: Add a one-line scenario: script an empty `nex-installer/disk` answer, then `nex-installer/disk-error <note>`, then DISK_ONE, and assert the error detail contains "Select at least one disk" and the handoff still ends up as `disk=/dev/sda;`.

### SR-073 [low] DONE `installer/gui/wizard-text.sh:473` — unverified

**Text handoff does not escape the GKeyFile list separator, so a semicolon in a disk serial silently splits the list**

Defect: `ini()` escapes only backslashes, but disk/disk_size/disk_serial/disk_major_minor became `;`-separated GKeyFile lists; a serial containing `;` yields more list entries than disks and nex-config rejects the handoff with an unactionable message.

Failure: A disk whose sysfs `device/serial` contains a semicolon (some USB/SAS bridges expose vendor strings verbatim) is selected through the minimal (text) frontend: the wizard writes `disk_serial=AB;CD;`, nex-config counts 1 disk against 2 serials and refuses with "Select the disk or disks to erase and install Nex Core onto." The operator has selected a disk, so the message is unactionable and the wizard loops.

Fix: Add a list-value variant used for the four disk keys, e.g. `ini_list() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/;/\\;/g'; }`, matching what `g_key_file_set_string_list` writes.

### SR-074 [low] DONE `installer/gui/wizard-text.templates:80` — confirmed 2/3

**"Configure more network adapters?" body assumes a management adapter exists and runs into ${detail}**

Defect: The question is also asked on an offline install, where there is no management adapter, but the fixed body still reads "A normal install only needs the management adapter." There is also no `.` separator line after `${detail}`, so debconf joins the machine-generated detail and that sentence into one paragraph.

Failure: Operator chooses "Offline (no management network)" on a two-NIC host. The screen reads, as one wrapped paragraph: "Offline install. Adapters (2): eth0, eth1. A normal install only needs the management adapter. Answer no to leave every other adapter unconfigured and continue..." — referring to a management adapter the operator just declined. Pressing Enter accepts the default no and installs a host with zero configured interfaces.

Fix: Add a ` .` separator line after ` ${detail}`, and move the "A normal install only needs the management adapter" sentence into the substituted `$detail` so the offline branch can say something accurate instead.

### SR-075 [low] DONE `installer/late-install.sh:100` — unverified

**nex-sync-esp's EXIT trap does not umount, so a failed copy leaks a mounted member ESP on every apt run**

Defect: The installed helper mounts a member ESP on a mktemp dir but its EXIT trap only rmdirs; with `set -eu`, any failure between `mount` and `umount` leaves the ESP mounted, and the APT DPkg::Post-Invoke hook repeats it after every dpkg invocation.

Failure: A member's ESP is full, read-only (a vfat remounted ro after an unclean shutdown) or returns an I/O error: `cp -a` fails, `set -e` exits the script, the trap's `rmdir` fails on the non-empty mountpoint (suppressed by `|| true`), and /dev/sdb2 stays mounted on a stray <tmp>/tmp.XXXX. Nothing is reported; every subsequent apt operation adds another leaked mount of the same ESP, and a later disk replacement mkfs/partitions a partition that is still mounted.

Fix: Use the same trap as mdCopyESP: `trap 'umount "$copy" 2>/dev/null || true; rmdir "$copy" 2>/dev/null || true' EXIT`. Better still, have late-install.sh write the helper from the same text the Controller already carries instead of keeping two hand-maintained copies.

### SR-076 [low] DONE `tests/integration/fresh_install_lab.py:89` — unverified

**The new --uefi lab hardcodes one distribution's OVMF paths with no fallback or override**

Defect: The UEFI lab mode - the harness the QA record names for the outstanding mirror boot and disk-detach runs - resolves OVMF from absolute Arch-specific paths, so it raises FileNotFoundError on a Debian/Ubuntu workstation rather than reporting a missing firmware prerequisite.

Failure: Anyone but this workstation runs `python3 tests/integration/fresh_install_lab.py --uefi ...` to close outstanding acceptance item 1; `OVMF_VARS.read_bytes()` raises FileNotFoundError: '/usr/share/edk2/x64/OVMF_VARS.4m.fd' before QEMU starts, with no indication that the fix is to install or point at the distribution's OVMF package.

Fix: Reuse iso-smoke.py's probe list (/usr/share/OVMF/OVMF_{CODE,VARS}_4M.fd, /usr/share/edk2/x64/OVMF_{CODE,VARS}.4m.fd) or add a --uefi-code argument like installer-modes.py, and raise the same explicit 'OVMF 4M firmware not found; UEFI has not been tested' error when none is present.

### SR-077 [low] DONE `tests/integration/installer-disk.py:16` — unverified

**The reworked GTK Disk step is only string-matched, though the repo already has harnesses for compiled wizard fragments**

Defect: All of the new GTK logic - the tick list, the live summary, the 16 GiB and 10% inline reasons, the lone-disk preselect, disk_kind/has_partitions - is verified by grepping nex-wizard.c for its own literals, so the arithmetic and branching are untested.

Failure: Change nex-wizard.c:341 to report `largest` instead of `smallest` in the mirror summary. Every assertion in installer-disk.py and check.sh still passes (the format string is unchanged), and `collect()` still refuses genuinely mismatched pairs, so the only symptom is that an operator mirroring a 500 GB and a 540 GB disk is told 540 GiB is usable and is short 40 GB after install. Nothing in CI detects it.

Fix: Follow the installer-link.py pattern: extract `disks_changed`/`similar_disks` into a compiled harness with a stub GtkListStore (or refactor the size arithmetic and summary-text choice into a pure helper taking an array of sizes), and assert the summary text for one disk, two equal disks, two 5%-apart disks, a 12%-apart pair, a sub-16 GiB pick and the empty selection with and without a similar pair present.

### SR-078 [low] DONE `tests/integration/installer-disk.py:133` — unverified

**The 10% rule is only tested with identical disks and a half-size disk, so the accept boundary is unpinned**

Defect: Both frontends and nex-config implement `largest <= smallest + smallest/10`, but every passing case in every test uses byte-identical capacities and every failing case uses a 2:1 mismatch. Nothing distinguishes "within 10%" from "exactly equal", and no test selects more than two disks.

Failure: Change nex-config.c:126 to `> smallest` (dropping the 10% tolerance). Every test still passes, because the only accepted pairs are identical. An operator mirroring two nominally-500 GB disks from different vendors - 500107862016 vs 500105249280 bytes, a routine 0.0005% difference - is refused at the last step with "Every mirror disk must be within 10% of the smallest selected disk's capacity" and cannot install.

Fix: Add a scenario to installer-disk.py where the partner is ~5% smaller and must be accepted, one at just over 10% that must be refused, and one three-disk selection; mirror the 5% accept case in installer-text-wizard.py with a third fixture disk.

## Host version light

### SR-079 [medium] DONE `web/src/updates.ts:31` — confirmed 2/6; confirmed 2/6

**A host that is not running Nex Core shows a pulsing green "up to date" light**

**A Debian revision or epoch in the archive candidate mis-colours every host**

Defect: versionLight scrapes bare digit runs out of the host's PRETTY_NAME and never checks the string is a Nex Core version, so any os_version whose first number exceeds the archive's major reads as "current" — including strings that are not Nex Core at all.

Failure: A lab or dev host runs the agent on plain Debian 13 and reports PRETTY_NAME "Debian GNU/Linux 13 (trixie)" while the archive offers nex-core-local 0.4.3. have=[13], want=[0,4,3]; i stays 0 and 13 > 0, so the host page renders a pulsing green light titled "Up to date: Nex Core 0.4.3 is the newest release." beside a version string that is plain Debian — a confident, wrong statement about a host with no Nex Core. While the archive major is 0, every non-Nex-Core PRETTY_NAME containing a digit reads green. docs/guide/manage-controller.md:156-157 documents no light only for an unreadable archive or a non-admin account.

Also: core_available_version is apt's Candidate, a full Debian version string, but versionLight splits it on digit runs; a `-1` revision makes up-to-date hosts orange and an epoch makes them red.

Fix: Return undefined unless the installed string is recognisably a Nex Core version (e.g. require the numeric run to follow "Nex Core"), so an unrecognised OS string produces no light instead of a green one. Compare on the Controller with nexupdate.CompareVersions, or at minimum strip the epoch and Debian revision from the candidate before the numeric compare.

### SR-080 [medium] DONE `web/src/updates.ts:35` — confirmed 3/3

**Pre-release hosts read green when the matching final release is in the archive**

Defect: When the host version has extra digits after the archive's version (any `~`/`-` pre-release suffix), the loop reaches i === want.length and returns "current", so a host that dpkg orders strictly *below* the candidate is shown as up to date.

Failure: The archive promotes 0.5.0~alpha1 to 0.5.0. A host still running 0.5.0~alpha1 gives have=[0,5,0,1], want=[0,5,0]; the loop consumes all of want, so the operator sees a pulsing green light and "Up to date: Nex Core 0.5.0 is the newest release." for a host that needs the update. The reverse also misfires: host 0.4.3 against candidate 0.4.3~rc1 returns "behind", advertising a downgrade as an update.

Fix: Let the Controller decide with nexupdate.CompareVersions (dpkg --compare-versions) and send the level, or treat a leftover non-numeric tail on `installed` after the numeric prefix matched all of `want` as behind. If the shortcut stays, record the limitation in the change record and guide so green is not read as authoritative.

### SR-081 [low] DONE `internal/controller/updates.go:26` — confirmed 3/3

**An unreachable archive makes GET /api/v1/updates run apt-get update twice**

Defect: CheckWithCache only writes the refresh stamp on success, so when the index refresh fails the second (nex-core-local) call repeats the same failing apt-get update, doubling the endpoint's latency — and the endpoint is now hit on every host selection.

Failure: On an air-gapped or offline Controller an operator selects a host; apt-get update against the unreachable archive times out, no stamp is written, and the handler immediately runs the identical apt-get update again for nex-core-local before replying. The request blocks for roughly twice the previous timeout, now on host selection rather than only on the Updates page.

Fix: Write the stamp (or otherwise mark the attempt) even when the refresh fails, or fetch both candidates from a single CheckWithCache pass.

### SR-082 [low] DONE `web/src/main.tsx:681` — confirmed 2/6; confirmed 2/6

**The version light survives signing out and signing in as a non-administrator**

**The archive version behind the light is fetched once per host selection and never refreshed**

Defect: Sign-out clears session, data and token but not `update`, so the stale core_available_version keeps driving the light for the next account, contradicting the guide's "No light appears ... when the account is not an administrator".

Failure: An administrator opens a host page (update populated), signs out in the same tab, and an operator without the admin role signs in and opens a host. The refetch returns 403 and is discarded, so the operator still sees the light rendered from the previous administrator's data.

Also: The new effect depends on [session, host?.id], so core_available_version is read only when the selected host changes, while the rest of the host view reloads every 10 s. A host page left open does not pick up a release published in the meantime.

Fix: Clear `update` alongside `data`/`token` on sign-out, or `setUpdate(null)` in the effect before the fetch. Fold the read into the existing 10 s reload, or accept it and say so — the hover text asserts "is the newest release" with no indication of when the archive was last consulted.

### SR-083 [low] DONE `web/src/main.tsx:1810` — confirmed 3/3

**Version state is conveyed by an 8px colour dot whose explanation needs a mouse**

Defect: The light is a non-focusable empty <span> carrying only a background colour plus title/aria-label. Keyboard and touch users cannot read the explanation, colour is the only visual difference between the three states, the greens/oranges sit below the 3:1 non-text contrast threshold on the white panel, and the pulse drops the green to opacity .3.

Failure: A keyboard-only or touch operator, or one with red/green colour deficiency, opens a host's General tab, sees a small dot next to "Nex Core 0.4.3", and cannot tab to it or hover it, so they cannot tell green (up to date) from orange (update available) or red (major release behind) — the distinction the light exists to make. Under forced-colors the three states collapse to one colour for everyone.

Fix: Put the state in text beside the version (e.g. "Nex Core 0.4.3 · update available") and keep the dot decorative, or make the dot focusable (button/tabindex=0 with :focus-visible). .connection-dot shares the pattern, so a shared fix is cheapest.

## Stats tab restyle and system disk panel

### SR-084 [high] DONE `web/src/stats.tsx:491` — confirmed 3/3

**Inline onRetry restarts the mirror's polling intervals every render, so they never fire**

Defect: Stats passes a fresh arrow function as onRetry on every render; md.tsx keeps that function (as onReplaced) in two useEffect dependency arrays, so both setInterval timers are torn down and recreated faster than their 10s/5s periods and effectively never fire.

Failure: Operator opens a host's Stats tab (default range 10m, so the history poll runs every 5s) with a degraded/rebuilding system mirror. Every 5s poll re-renders Stats, creates a new onRetry closure, and md.tsx's effects clear and recreate their intervals, so the 10s rebuild refresh never reaches 10s: "Rebuilding · 12.3%" stays frozen until the operator presses Retry by hand. After submitting a replacement the 5s operations poll is reset on the same cadence, so `task.status` stays "queued" and neither the "The replacement disk was added" notice nor the "could not replace the disk" alert ever appears — the operator gets no outcome for a destructive, disk-erasing operation.

Fix: Give Stats a stable callback: `const retryHardware = useCallback(() => setHardwareAttempt(a => a + 1), []);` and pass that to HardwarePanel (and on through to SystemDiskMirror). Alternatively keep onReplaced in a ref inside md.tsx and drop it from both dependency arrays.

### SR-085 [medium] DONE `web/src/stats.tsx:171` — confirmed 3/3

**Warm/Critical word moved inside role="meter", where assistive tech does not expose it**

Defect: The state word used to be a sibling of the meter element; it is now a child of the element carrying role="meter", aria-label and aria-valuetext, so screen-reader users lose the only non-visual signal that a sensor is Warm or Critical.

Failure: A host's CPU package sensor reads 96 °C against a 95 °C crit, so the tile renders "96 °C Critical" with a red bar. A screen-reader user arrowing through the gauges hears "CPU Package, 96 °C" — the word Critical is inside the meter's presentational subtree and the coloured bar is aria-hidden, so nothing tells them the reading is past its threshold. Before this change the pill was a separate span and was announced.

Fix: Fold the state into the announced value, e.g. `aria-valuetext={gauge.state === "normal" ? text : `${text} ${word}`}`, or render the state span as a sibling of the meter div as before.

### SR-086 [medium] DONE `web/src/stats.tsx:322` — confirmed 3/3

**The panel's "Core too old" state is unreachable and md_arrays.unavailable is ignored, so both cases render "Single system disk, no redundancy"**

Defect: Two angles on one wrong state derivation in SystemDiskMirror. (a) `reported={!!hardware.md_arrays}` can never be false, because protocol.HostHardware.MDArrays has no `omitempty` and the Controller re-marshals the decoded struct, so md.tsx:93's "does not report its RAID arrays" branch is dead. (b) The section's `unavailable`/`reason` — produced by Core, relayed, and typed in stats.tsx:207 — are never read by any component. Both cases therefore fall through to md.tsx:97's positive claim that the host has one unmirrored system disk.

Failure: (a) A host still running the Core from commit 59c93d2 advertises host-hardware-v1 but sends no md_arrays; the Controller fills the zero value and relays `md_arrays:{arrays:null}`, so a host installed on a RAID1 mirror — possibly a degraded one — is shown "Single system disk, no redundancy. A mirrored system disk is chosen when Nex Core is installed." with no prompt to upgrade Core. (b) A current Core that cannot read /sys/block reports `unavailable:true` with its reason and gets the identical false sentence, hiding the reason. docs/API.md:4443 and docs/guide/replace-a-failed-system-disk.md both describe behaviour that cannot occur, and the change record's claim that such hosts "are all told so rather than shown nothing" is not met.

Fix: Pass the whole section into SystemDiskMirror and derive the three states from the payload: render `md_arrays.reason`/a generic unavailable notice when `unavailable` is set; distinguish an old Core either by making the field `*HardwareMDArrays` with `omitempty` (and dropping it from the openapi required list) or by keying on the host's capability list; keep "Single system disk" only for `unavailable !== true && arrays.length === 0`.

### SR-087 [low] DONE `web/src/stats.css:1` — confirmed 3/3

**The stats design plan still documents the tokens and gauge arc this diff deleted**

Defect: The CSS rewrite removed --stats-bg, --stats-card, --stats-track and every --accent-* alias, plus the .stats-pill, .stats-gauge-arc and .stats-hardware rules, but the plan that specifies them was not updated in the same change.

Failure: A contributor implementing the next Stats panel follows the plan's token table, writes `var(--accent-sky)` and gets an unresolved custom property with no fallback, or reproduces the gauge arc the diff just removed.

Fix: Update the token table and the Gauges paragraph in docs/plans/2026-09-22-host-hardware-and-sensors.md in the same change as docs/changes/2026-09-22-plain-stats-styling.json.

### SR-088 [low] DONE `web/src/stats.css:66` — confirmed 3/3

**tr:last-child rule also strips the column-header underline**

Defect: `.stats-hw-table tr:last-child > *` matches the last row of every row group, and <thead> contains exactly one <tr>, so the header row loses its bottom border and runs into the first data row.

Failure: Expanding Disks or PCI devices shows the "Device / Model / Size…" header with no rule under it, sitting flush against the first device row, while every other row in the card keeps its separator — the intended "no trailing rule at the card's bottom edge" effect also erases the header separator.

Fix: Scope it to the body: `.stats-hw-table tbody tr:last-child > * { border-bottom: 0; }`.

### SR-089 [low] DONE `web/src/stats.css:111` — confirmed 2/3

**System disk block is the only panel content not restyled as a card**

Defect: The restyle put every block in the Hardware panel on a white card over the #f7f8fa canvas, but the md-arrays block added in the same batch keeps bare canvas and the old 10px padding, so it reads as unstyled content between two carded groups.

Failure: On a host whose Core does not report md arrays, the Hardware panel shows the summary line, then "System disk" plus an explanatory paragraph floating directly on the grey canvas between two white card groups — it reads as unstyled content rather than part of the restyled panel.

Fix: Give .md-arrays the same card treatment as .stats-hw-section (white background, 1px var(--stats-border), 6px radius, 12px padding) or place it inside the .stats-hw-sections grid.

### SR-090 [low] DONE `web/src/stats.css:121` — confirmed 3/3

**.md-healthy green is about 4.1:1 on the new panel background, below the 4.5:1 text minimum**

Defect: The panel canvas changed to #f7f8fa in the same diff, and .md-healthy (#0f8a5f) is 13 px body text at weight 600 — not WCAG "large text" — so it needs 4.5:1 and reaches roughly 4.1:1.

Failure: A low-vision operator reading the "Healthy" state word on the host Stats tab gets sub-4.5:1 contrast; the word is the only non-colour signal of array health, so it is the one string on the panel that must be legible.

Fix: Darken to e.g. #0b6e4b or #166534, matching the .notice colour in style.css.

### SR-091 [low] DONE `web/src/stats.tsx:492` — confirmed 2/3

**System disk Replace button is gated only on host-system-disk-replace-v1, but its dialog needs the storage discovery capabilities**

Defect: The ZFS replace action requires storage-zfs-replace-v1 and storage-discovery-v1 before it offers a button; the system disk action checks only its own capability, although the dialog's first call is storage/discover, which the Controller refuses without storage-discovery-v1 and storage-zfs-provision-v1.

Failure: A host reports v2 but the Controller has not yet acknowledged storage-repositories-v1 (the preflight round, or a Core whose StorageProtocolReady was reset). Its capability list contains host-system-disk-replace-v1 and host-hardware-v1 but not storage-discovery-v1. The Stats tab offers Replace disk on a degraded array; opening it immediately shows "host does not support storage discovery" with no usable disks and no explanation of what to upgrade, instead of the panel's own "Update Nex Core, then replace the failed disk from here" message.

Fix: Gate `replaceSupported` on storage-discovery-v1 and storage-zfs-provision-v1 as well as host-system-disk-replace-v1, matching web/src/storage.tsx:453.

## Documentation, API reference and records

### SR-092 [medium] DONE `CHANGELOG.md:89` — unverified

**The batch's operator-visible features have no changelog entry**

Defect: The staged CHANGELOG diff adds exactly one bullet (BUG-0444). The mirrored system-disk install, system-disk replacement, degraded ZFS repositories staying in service (BUG-0443), ZFS disk replacement with hot spares and widened layouts, and the host version light are all absent from the 0.4.3 section.

Failure: 0.4.3 is cut from the newest numbered changelog heading. Its release notes tell operators about a minimal-install NIC question but not that they can install onto a mirrored system disk, replace a failed system or pool disk from the Controller, or that a degraded ZFS repository now stays in service — the last reversing previously accepted behaviour (BUG-0443).

Fix: Add Minor Feature bullets under 0.4.3 for the mirrored system disk, both replace workflows with hot spares, and the version light, plus a Bug Fixes entry for BUG-0443 — or state the batch-level convention (entries land when a slice closes with lab evidence) and treat BUG-0444 the same way.

### SR-093 [medium] DONE `docs/API.md:668` — unverified

**"An older Controller sees an unknown status string" is false: the new zfs_pool field makes an older Controller reject the whole report**

Defect: The changed-contract entry for the degraded-ZFS slice claims graceful degradation for an older Controller, but the same slice adds `zfs_pool` to StorageAccess and the Controller decodes agent reports with DisallowUnknownFields, so a pre-change Controller returns 400 for the entire report.

Failure: A Core host upgraded ahead of its Controller — the exact case the sentence describes — reports a ZFS repository. The old Controller's strict decoder hits the unknown `zfs_pool` member and answers 400, so the host's entire report (inventory, VMs, results, journal outcomes) is rejected on every pass, not just the new field.

Fix: State the real behaviour (Controller must be upgraded before Core), or gate the new fields behind a capability acknowledgement the way zfsLegacyManagedProjection already gates ZFS provisioning records, and document that gate.

### SR-094 [medium] DONE `docs/API.md:754` — confirmed 3/3; confirmed 3/3

**API.md documents a Core refusal sentence that no code produces, and omits one that exists**

**A documented Core refusal sentence that no code produces**

Defect: The ZFS-replace "Core refusals" paragraph quotes `the replacement disk is not the one that was selected` as one of the executing host's `needs_attention` sentences, but that string exists nowhere in the tree, while the real refusal `the replacement disk cannot be safely observed` is documented nowhere.

Failure: An operator hitting a needs_attention result searches the reference for the sentence and cannot find it (Core never emits the documented one), or a client matches on the documented string and never matches; an operator who hits "the replacement disk cannot be safely observed" finds no explanation in either reference.

Also: The reference lists `the replacement disk is not the one that was selected` among the executing host's refusals; that string exists nowhere in the repository outside docs/API.md.

Fix: Delete the phantom sentence from docs/API.md:754 and add `the replacement disk cannot be safely observed` to both docs/API.md and the openapi x-commands.storage-zfs-replace semantics. Delete the phantom sentence and add 'the replacement disk cannot be safely observed'.

### SR-095 [medium] DONE `docs/API.md:4441` — confirmed 2/3

**API.md claims an older Core omits md_arrays, but the contract marks it required and the Controller always emits it**

Defect: The staged compatibility note says "an older Core omits it and the host page shows no mirror", while openapi.json lists md_arrays in HostHardware.required and the Go type has no omitempty, so the Controller always re-marshals the field — making the documented "Nex Core is too old to report its arrays" state unreachable. The three references (API.md, openapi required list, web/src/stats.tsx optional type) disagree about whether the field is guaranteed.

Failure: A host on the pre-slice Nex Core is opened on its Stats tab. The Controller emits md_arrays:{arrays:null}, the UI treats it as reported with zero arrays, and the operator is told "Single system disk, no redundancy" even on a host whose system disk is in fact mirrored; the documented too-old message never appears. Separately, a client generated from openapi.json treats a genuine pre-0.4.3 hardware response as schema-invalid.

Fix: Either make the field genuinely optional (pointer/omitempty on protocol.HostHardware.MDArrays, drop md_arrays from HostHardware.required, regenerate) so the documented old-Core state is real, or correct docs/API.md:4441 and docs/guide/replace-a-failed-system-disk.md:15-16 and gate the panel on a capability instead.

### SR-096 [medium] DONE `docs/API.md:4495` — confirmed 3/3

**System-disk docs claim a BIOS mirror has no EFI system partition, contradicting this batch's own installer layout**

Defect: Both docs/API.md and the openapi description justify the BIOS bootloader path by asserting a BIOS mirror has no ESP at all, but the installer shipped in the same batch writes a 512 MiB ESP onto every mirror member regardless of firmware.

Failure: A contributor changing mdInstallBootloader (internal/vmhost/md_replace.go:184-206) reads the stated reason, concludes no ESP exists on a BIOS mirror, and branches on the partition's absence rather than on /sys/firmware/efi. On an installer-produced BIOS host the replacement disk does carry a replicated-but-unformatted ESP, so a change made on that reasoning leaves it unformatted after the host is switched to UEFI boot.

Fix: Restate the reason as it is — the branch is on the host's firmware (/sys/firmware/efi), not on whether an ESP partition exists — in docs/API.md:4494-4497 and the openapi replaceSystemDisk description.

### SR-097 [medium] DONE `docs/api/openapi.json:3015` — unverified

**The three new operations claim x-introduced-version 0.1.0 although they are new in this 0.4.3 development version**

Defect: POST /api/v1/storage/zfs/replace, POST /api/v1/hosts/{id}/system-disk and the peer route POST /api/v1/pool/zfs-provision/replace all declare `x-introduced-version: 0.1.0` and "Implemented in development product 0.1.0", while the comparable operations added immediately before them carry the real development version.

Failure: A client or operator reading the generated reference concludes /api/v1/hosts/{id}/system-disk exists on any 0.1.0-or-later Controller and does not gate the call on the Controller version; against a 0.4.2 Controller the route is simply absent (404).

Fix: Set `x-introduced-version` to 0.4.3 on all three new operations and change the "Implemented in development product 0.1.0" sentences to 0.4.3.

### SR-098 [medium] DONE `docs/guide/install-nex-core.md:156` — confirmed 2/3; unverified

**Guide states the wrong question order for the "yes" path, and BUG-0444's symptom survives there**

**Documented text-wizard question order is wrong when the management adapter is not the first NIC**

Defect: The guide says the management adapter's IPv4 questions come straight after the Yes/No, and that yes "also walks each remaining adapter". In the yes path the wizard still walks adapters in sysfs discovery order, so a leftover adapter that sorts before the management NIC is asked first — exactly the symptom BUG-0444 records. Both a docs-accuracy defect and an incomplete fix for the bug this work package claims to close.

Failure: Host has eth0 and eth1; operator picks eth1 as the management adapter, then answers yes to "Configure more network adapters?". The next screen is "IPv4 configuration for eth0:" with choices skip/dhcp/static — not the adapter just chosen — and eth1's question comes after it. The two new regression scenarios miss this: scenario_configure_other_adapter uses eth0 (the first NIC) as management, and scenario_management_not_first_nic answers no.

Also: The guide says the Yes/No question is followed by "the management adapter's IPv4 mode, VLAN ID, address and gateway" and that yes "also walks each remaining adapter", but the wizard walks adapters in sysfs order, so any leftover adapter that sorts before the management one is asked first.

Fix: Either order the nic steps management-first (build `steps` with $v_management ahead of the extras, which is what the guide already promises), or reword docs/guide/install-nex-core.md:156-159 to say that when you answer yes the adapters are walked in detection order, and add a regression scenario covering management-not-first with other-adapters=true. Reword to "then each detected adapter in turn, including the management one", or order the nic steps management-first in the step list.

### SR-099 [medium] DONE `docs/guide/install-nex-core.md:205` — confirmed 3/3

**Mirrored-install workflow documented as working, with no verification limit and no link to its QA record**

Defect: The new mirror paragraph asserts runtime behaviour as fact (assembled at boot, GRUB on every selected disk, boots from whichever survives, VMs uninterrupted, ESP mounted so it never holds up the boot), but the page's Verification section never mentions the mirror and never links docs/qa/iso/2026-09-22-installer-mirror.md, which records every one of those checks as OUTSTANDING.

Failure: An operator follows step 6, ticks two disks and installs a mirrored host believing — as the page states flatly and nothing contradicts — that it boots from either disk and survives a disk failure. No mirrored host produced by the current three-partition GPT recipe has ever been booted, GRUB on the second member has never been exercised, and the run that got furthest needed an `nofail` fstab fix to avoid emergency mode.

Fix: Add a Verification paragraph linking ../qa/iso/2026-09-22-installer-mirror.md stating that both wizards' Disk steps and two-disk installs reached the base system but no mirrored host has been booted, detached or checked for leftover arrays, and that the current GPT/BIOS-boot/ESP layout has never been installed; soften lines 205-212 to match.

### SR-100 [medium] DONE `docs/plans/2026-09-08-nex-platform.md:535` — confirmed 3/3

**Platform plan says slices 2 and 3 have unit/contract evidence only, contradicting both QA records and the software RAID plan**

Defect: The status line added to the phase plan understates the evidence: both slices were exercised on disposable hosts, as their own plan sections and QA records state, and not "every lab acceptance check" is outstanding.

Failure: A reviewer running the phase acceptance gate reads the platform plan (the canonical read-before-working document), believes no lab evidence exists for the ZFS replace or system-disk replace slices, and either re-runs work already done or marks the phase blocked on evidence that is recorded in docs/qa/; or mistrusts the slice statuses because the two plans disagree.

Fix: Reword line 535 to match the slice statuses: slice 1 lab-verified; slice 2 lab-walked with named outstanding reruns; slice 3 Core/Controller half lab-walked, installer half source-checked with lab acceptance outstanding.

### SR-101 [low] DONE `docs/API.md:667` — confirmed 2/3

**"The Controller passes it through and stores none of it" is not true of the pool observation**

Defect: The new zfs_pool / pool observation is written into the Controller's managed_resources.command projection row with the rest of the mirrored StorageRegistration and re-served from there between reports; the UI also renders it without the staleness gate the neighbouring access line has.

Failure: A host stops reporting. Its repository row keeps rendering the last stored zpool status — for example 'resilver in progress · 42%' — from the Controller database for as long as the projection is not refreshed, with no staleness treatment. The invariant itself holds (the row is a disposable Core-refreshed projection), but the reference's claim is wrong and the operator is shown stale pool health as current.

Fix: Reword docs/API.md:667 to say the Controller keeps it only in its disposable Core-refreshed projection and derives nothing from it, and gate the pool line on storageObservationFresh(access.observed_at) the way the access line is.

### SR-102 [low] DONE `docs/API.md:735` — confirmed 3/3

**The documented 404 for an unknown pool master is actually answered 409**

Defect: The reference and the OpenAPI 404 description both claim the pool master being unknown yields 404; the master is resolved through zfsProvisionMaster, whose errors are all returned as 409.

Failure: A pool whose master_host_id no longer resolves to a host (master ejected or rebuilt) is used for a replace request: the client receives 409 {"error":"host not found"} where the published contract promises 404, so a client branching on status treats an unknown master as a retryable conflict.

Fix: Narrow docs/API.md:735 and the openapi 404 description to the selected host, and list 'host not found' under 409 for the master.

### SR-103 [low] DONE `docs/API.md:4487` — unverified

**System-disk refusal list omits the "already holds a working member" refusal that protects the surviving disk**

Defect: Neither docs/API.md's Core-refusal sentence nor the guide's troubleshooting list names the refusal an operator hits when they pick the surviving system disk, though it has its own unit test and was exercised in the lab.

Failure: An operator confuses the two disks, picks the surviving one, and gets a refusal sentence that appears in no documentation; they cannot tell from the guide whether the request was rejected safely or something was written.

Fix: Add "<disk> already holds a working member of <array>" and the unknown-array / unreadable-arrays refusals to docs/API.md:4485-4493 and the troubleshooting list in docs/guide/replace-a-failed-system-disk.md.

### SR-104 [low] DONE `docs/api/openapi.json:7844` — unverified

**System-disk 400 description omits the "system disk replacement requires a host" refusal**

Defect: The 400 response lists three validation sentences but not the one protocol.SystemDiskReplaceRequest.Valid emits for a host ID that passes nameValid yet is not an opaque ID; docs/API.md:4480-4483 omits it too.

Failure: A client posts to /api/v1/hosts/some%20host/system-disk; the Controller answers 400 with a sentence neither reference documents, so error-mapping built from the contract falls through to a generic message.

Fix: Add the sentence to the 400 description in openapi.json and to the Refusals paragraph in docs/API.md.

### SR-105 [low] DONE `docs/api/openapi.json:19341` — unverified

**x-commands entry for host-system-disk-replace omits the machine-readable capability field**

Defect: Every other capability-gated command in x-commands carries a `capability` key; host-system-disk-replace states its capability only inside the prose `semantics`.

Failure: A compatibility tool or reviewer enumerating x-commands to list capability-gated commands misses host-system-disk-replace and reports it as ungated, so an upgrade check built from the contract would not require host-system-disk-replace-v1 on the host.

Fix: Add `"capability": "host-system-disk-replace-v1"` to the x-commands entry and drop the leading prose sentence, matching storage-zfs-replace.

### SR-106 [low] DONE `docs/guide/install-nex-core.md:133` — unverified

**Guide names the single-disk primary button label for a mirror install**

Defect: The Review step was updated to say "the targets are certain" but still quotes the button as **Erase disk and install Nex Core**; with two or more disks ticked the button reads "Erase disks and install Nex Core".

Failure: An operator following the guide through a two-disk mirror install reaches Review and looks for a control labelled "Erase disk and install Nex Core"; the wizard shows "Erase disks and install Nex Core" - a mismatch on the one screen where the guide is telling them to match the wording before a destructive action.

Fix: Write the label as **Erase disk(s) and install Nex Core**, or name both forms.

### SR-107 [low] DONE `docs/guide/usage-history.md:79` — unverified

**The Hardware panel description was not updated when the System disk mirror panel moved into it**

Defect: SystemDiskMirror now renders inside HardwarePanel, above the hardware sections, but usage-history.md step 8 still describes the panel as a summary line plus six expandable sections, and the system-disk change record does not list this page.

Failure: An operator reading the Stats-tab page sees a System disk mirror block the page does not describe, with no pointer to the workflow that acts on it, and no mention of the row counts now shown on collapsed sections; the page's own verification note now covers a panel whose contents changed after that check.

Fix: Add a sentence to step 8 naming the System disk mirror block and linking replace-a-failed-system-disk.md, mention the row count, and list docs/guide/usage-history.md in the system-disk change record's guide.pages.

### SR-108 [low] DONE `docs/plans/2026-09-22-software-raid.md:190` — unverified

**Plan still specifies the superseded two-partition layout and claims grub-efi-amd64 multi-ESP support the batch itself disproved**

Defect: The Slice 3 design body describes a 2-partition layout with no BIOS boot partition and no seeded GPT label, and states Debian's grub-efi-amd64 keeps several ESPs in step; the implemented recipe and the plan's own status paragraph say otherwise.

Failure: A contributor picking up the outstanding Slice 3 acceptance reads the plan's design section, expects two partitions per disk with the md member as partition 2 and expects GRUB updates to need no Nex code, and writes a boot check or a replace procedure against a layout the installer no longer produces.

Fix: Update the Layout and Boot paragraphs to the three-partition GPT layout (1 MiB biosgrub, 538 MB ESP, md member as partition 3) and to the ESP-copy approach, as AGENTS.md requires plan scope changes to be recorded in the plan.

### SR-109 [low] DONE `docs/plans/2026-09-22-software-raid.md:200` — unverified

**Plan keeps a superseded Boot paragraph that says GRUB needs no Nex code**

Defect: The slice 3 design body still describes multi-ESP handling via Debian's grub-efi/install_devices and grub-multi-install, which the same slice's status paragraph records as nonexistent in Debian 13 and which the implementation replaces with the nex-sync-esp copier and an APT hook.

Failure: A contributor picking up the installer boot work reads the design body, concludes no Nex code maintains the second ESP, and removes nex-sync-esp or its APT hook as redundant — leaving the surviving disk's ESP stale after the next GRUB update, so the replaced disk cannot boot.

Fix: Mark the superseded Boot and partman sentences as replaced by the 2026-09-22 findings above, or rewrite them to the implemented design.

### SR-110 [low] DONE `docs/qa/2026-09-22-system-disk-replace.md:84` — unverified

**QA record reports an ESP formatted vfat on a BIOS lab host, which the shipped BIOS code path cannot do**

Defect: The disposable-host bullet records that the replacement disk's ESP was formatted vfat, but the run was on BIOS firmware and mdInstallBootloader never reaches mkfs.vfat on BIOS.

Failure: A reviewer citing this record as evidence that the replacement makes the new disk's ESP usable relies on an observation the current code cannot reproduce on BIOS; the shipped BIOS path leaves the replicated ESP unformatted and the UEFI path remains untested.

Fix: Label that observation as made against the pre-rewrite bootloader code, or drop the vfat claim and state that on BIOS only grub-install --target=i386-pc runs.

### SR-111 [low] DONE `internal/agent/run.go:306` — confirmed 2/3

**host-system-disk-replace-v1 is advertised unconditionally, but the replace dialog needs the storage-discovery capabilities**

Defect: The Replace disk button is gated on SystemDiskReplaceCapability alone, while the dialog's only way to list devices is POST /api/v1/storage/discover, which the Controller refuses unless the host also advertises StorageDiscoveryCapability and ZFSProvisionCapability — both gated behind s.State.StorageProtocolReady.

Failure: In the window between enrolment (or a protocol reset) and the first accepted non-legacy report, a host with a degraded mirror shows Replace disk…; opening it returns "host does not support ZFS provisioning; upgrade Nex Core" even though its Core is the current build, with no indication that the real answer is to wait for the storage handshake. Self-heals on the next report interval.

Fix: Advertise SystemDiskReplaceCapability from the same StorageProtocolReady block as StorageDiscoveryCapability/ZFSProvisionCapability, since the operation's device selection depends on them.

### SR-112 [low] DONE `web/src/style.css:445` — confirmed 3/3

**`.zfs-layout label` matches nothing, and `.zfs-disk-table td:last-child` cancels the muted "Blank" state**

Defect: The QoL pass replaced the inline `<label>Layout<select>` with the `.zfs-layouts` radio fieldset, leaving `.zfs-layout label` with nothing to match; separately the new `td:last-child { color: inherit }` override makes the same 'Blank' cell muted or not depending on whether the Spare column happens to be showing.

Failure: No failure from the dead rule. The override means the 'Blank' state cell renders in body colour when no Spare column is present and muted grey when it is, so the same column changes colour as soon as a third disk is ticked.

Fix: Delete `.zfs-layout label`, and scope the last-child override to the state cell (give it its own class) so `.muted` keeps applying.
