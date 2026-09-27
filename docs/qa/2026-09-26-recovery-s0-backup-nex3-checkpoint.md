# Recovery S0: checkpoint-present backup and independent restore on +nex3

Date: 2026-09-26. Status: **the checkpoint-present two-disk full backup and source-unavailable
local-copy restore passed on the selected disposable +nex3 Core guest. The first invocation also
ran inherited capacity and interruption probes outside this run's requested scope; see the
scope note below. This evidence does not close S0 or establish product support.**

## Candidate and method

The selected Debian 13 Core guest reported libvirt `11.3.0-3+deb13u3+nex3`. The full candidate
manifest SHA256 was
`116615ecd128c157d390d96ada07281d53b77d4afb5c61dc628a20ef6450588e`; all 15 selected package
hashes matched it, all package versions matched, and the preflight compared 42 installed runtime
files against the package checksums. The package closure includes both architecture-independent
`all` packages and `amd64` packages. The candidate was already installed; the run did not install
or modify packages.

The contributor harness created a dedicated NFSv4.2 read/write mount and a unique output child on
the existing disposable lab export. It captured `/etc/exports` and `exportfs -v` before and after;
the captured bytes were identical. A sentinel write/readback confirmed access to the selected
export. No export configuration or ACL was changed.

The raw result SHA256 is
`7d7a662bbba78aa3ece136b52c88571d23c95a710ee00636f90d7d6b3d25b815`. Raw output remains in the
ignored disposable-lab evidence directory and is not committed.

## Results

| Check | Result |
|---|---|
| Checkpointed full backup | The two-disk live full backup completed with native success status, all bytes processed, and zero remaining. Both outputs were standalone qcow2 files with no backing file. |
| Checkpoint remains available | The exact checkpoint name was present after checkpointed backup. A second full backup completed while that checkpoint remained; the checkpoint listing was identical before and after. |
| Output confidentiality | Both full-backup outputs were owned by `libvirt-qemu`, mode `0600`. The checkpoint-present outputs retained matching SHA256, owner, mode, and size across restore. |
| Independent restore | The original source disk tree was moved out of the path before the restore guest booted. The restore used local writable copies of the two checkpoint-present exports. Both disk markers matched the values present when that backup began, and the repository output hashes were unchanged after boot. |
| Structural rejection | Separate disposable truncated and header-corrupted copies were rejected by `qemu-img check`; the complete exports were not modified. |
| Cleanup and export state | Read-only post-run checks found the generated domains absent and source scratch removed. The export destination remained preserved. The Core guest's NFS mount remained exact and writable; its traffic-control qdisc and filters matched the pre-run capture. Storage export configuration bytes matched before and after. |

## Scope deviation and limits

The checkpoint restore mode inherited three probes that were unconditional in the shared harness:
a bounded-target capacity refusal, a targeted NFS traffic filter followed by `domjobabort`, and a
shutdown-during-backup attempt. They all recorded their expected terminal observations. The traffic
filter was removed in the harness `finally` path; a read-only check confirmed the original qdisc and
empty filter list. The storage export configuration was unchanged. These extra checks were not
needed for this assignment and are not counted as part of the normal backup/restore result. The
harness now skips the capacity and interruption probes in checkpoint-restore mode, with a focused
regression test.

This is one package-based synthetic Debian guest using the selected nested KVM lab and NFS export.
It does not establish application-consistent recovery, production storage support, installed-ISO
behavior, other repository backends, or Controller/Core product workflow support. No integrated
snapshot deletion was attempted. S0 remains open.
