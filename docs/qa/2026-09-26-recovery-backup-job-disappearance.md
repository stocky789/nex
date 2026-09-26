# Recovery S0: backup job disappearance without terminal statistics

Date: 2026-09-26. Status: **the selected +nex3 native backup terminal-record dismissal case passed. This is a bounded libvirt observation, not a daemon-crash or lost in-progress-job test.**

## Scope and result

A fresh nested TCG system guest ran the selected libvirt `11.3.0-3+deb13u3+nex3` package set under an enforcing per-domain AppArmor profile. Inside it, a uniquely named two-disk synthetic `qemu:///system` domain ran QEMU as `type=qemu`. Its source disks were local 64 MiB qcow2 files with a 64 KiB marker write on each disk; it had no guest OS, NFS repository, or checkpoint.

`backup-begin --reuse-external` completed. With the current job view at `Job type: 0`, the completed view reported job type 3, `success=1`, `disk_total=134217728`, `disk_processed=134217728`, and `disk_remaining=0`. The existing [`completed_backup_result`](../../tests/integration/recovery_s0_backup.py) accepted those pre-dismissal views. Both standalone output images passed `qemu-img check`.

The probe then queried `domjobinfo --completed --anystats --rawstats` **without** `--keep-completed`. That command returned the completed statistics and dismissed the retained record. The subsequent current-job query and completed-job query both returned `Job type: 0`. Passing those exact views to `completed_backup_result` raised `native backup has no completed-job statistics`, as required. The output hashes remained unchanged across dismissal, and copies of both outputs were preserved privately. The archived runner source records the `backup-begin <domain> <backup.xml> --reuse-external` invocation; the raw JSON result does not duplicate that argv.

This verifies the safe boundary after an operator or caller explicitly consumes a completed record: disappearance alone is refused as proof of backup success. It does not show that an in-progress backup can disappear without terminal statistics, simulate daemon restart/crash, or exercise a Nex backup API or its `Needs attention` state. No output was published or used as a restore. The outer guest was shut down cleanly; its clone, result, and output copies remain in ignored local evidence. Earlier harness setup attempts failed before `backup-begin` (missing host `qemu-img` shim, fixture permissions, QMP socket path length, and unsupported graphics XML); they are not native backup failures.

## Reproduction and evidence

The private raw result is retained under `.cache/recovery-s0-job-disappearance-nex3/run7/`; its `result.json` SHA256 is `b8738cf3d15aa25102ee432f5783a79d746a2a90e3be20d4c806b464963fe5ba`. Each output had SHA256 `8d73df7ae6c13b0e9319274a81525a09c5d1661b4332d356a3c9b06948506f8b` both before and after dismissal. The execution used manifest SHA256 `116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e` and recipe SHA256 `3bf950c83fa9279de22e80c736984304e275d77ffcecd0609893cc5466ac46a5`. A private one-off wrapper cloned an already provisioned +nex3 guest so the candidate was not reinstalled; its source SHA256 is `e17112bf0882922e0fbcb2769ce837563c9d7e1a78e281fe0a86a2d5bcb1da81`, and the private guest runner SHA256 is `503224ecc470245385804bc635cede2c5fa11012f15b81aae86bf04faad40709`. The source provisioning guest remained byte-identical. These ignored artifacts are local evidence, not a portable test harness.

The archived private runner source records this native command sequence:

1. Define and start a fresh two-disk `qemu:///system` domain; verify its QEMU PID entered the exact enforcing AppArmor profile.
2. Run `virsh backup-begin <domain> <backup.xml> --reuse-external`; wait for current job type 0 and preserve the completed statistics.
3. Query the completed record with `virsh domjobinfo <domain> --completed --anystats --rawstats`, without `--keep-completed`.
4. Query current and completed views again, require both to report type 0, and require the existing result parser to refuse the missing terminal statistics.
5. Compare the private output hashes before and after, shut off the exact synthetic domain, and retain the isolated guest and outputs.

`python3 -m unittest tests/test_recovery_s0_backup.py` passed 22 tests, including the existing missing-terminal-statistics refusal. `python3 -m py_compile` passed for the private one-off runner and clone wrapper. These checks do not simulate a lost in-progress job or establish a product backup publication/recovery policy. This probe alone does not close S0; the collective feasibility decision is recorded in the [recovery plan](../plans/2026-09-25-recovery-snapshots-backups-restores.md). Core interruption/recovery and product API/UI gates remain open.
