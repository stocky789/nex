# Automatic VM I/O defaults validation

Date: 2026-09-24. Development working tree; implementation and native lifecycle verified.
Scope: [automatic disk and network defaults](../plans/2026-09-24-automatic-vm-io.md).

## Method and environment

Real Controller/agent processes exercised the existing API against libvirt/QEMU in a disposable
nested Debian 13.6 KVM host. The lab owned its guest disks, bridge and network namespace.
An initial container-only attempt could define domains but could not start networked guests:
the surrounding host's TUN node returned `No such device`. The nested Debian kernel supplied
working KVM/TUN without modifying the surrounding host. No production workload was changed.

Outer kernel: `6.12.107+deb13-cloud-amd64`; libvirt: `11.3.0-3+deb13u3`;
QEMU: `1:10.0.13+ds-0+deb13u1`. Inner Debian guest: the same kernel, `fio-3.39`,
`iperf 3.18` and `ethtool 6.14.2`. The public Debian genericcloud base was verified against
Debian's published SHA-512 before use; its SHA-256 is
`85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`.

Executed binary SHA-256 values:

- Core agent: `a5eb669e9f937cf5117074c73c76c6640f509e4b2fda7a87dcbb55ce45cb9db3`.
- Controller: `a74d2d1ea71fd0117547aadd5e0e4b30a1547244e5468fd186470812fe344ed2`.

The native runner is `tests/integration/vm_defaults_native.py`, with `NEX_BIN` pointing to
locally built binaries/source and `NEX_LAB_DIR=/tmp`. The shell wrapper prepares an isolated
container on hosts with working KVM/TUN; this execution instead prepared the nested host and
ran the same Python runner there. Output is saved after each measurement arm. Harness fixes
and a later simplification of the fixed 4K/QD32 workload do not change the tested Core binary.

## Functional results

All of these passed with real libvirt/QEMU:

- Core creation with one disk IOThread, VirtIO disk assignment and four offered NIC queues.
- Start of a simulated historical definition with no IOThread and one NIC queue. Both the
  test-owned libvirt definition and Core journal were changed consistently for this fixture;
  it was not a deployed historical Core binary.
- Live addition of IOThread 1 before a tuned VirtIO disk, a new VirtIO-SCSI controller/disk,
  and a VirtIO NIC. Existing attached devices kept their original tuning.
- Deletion of Controller A's database, creation of an empty Controller B, and re-enrollment of
  the same Core. The running VM retained its identity, revision and both live/persistent tuning.
- Stopped save applying defaults to existing disks/NICs, followed by actual Debian guest boot
  and SSH access.
- Restoration of Core XML after benchmark fixtures and live removal of the last SCSI disk.

Native hotplug caught an incorrectly named standalone SCSI controller fragment. It was fixed
and rerun successfully; the Go regression now inspects the exact attachment XML. See BUG-0569
in the [bug ledger](../BUG_LEDGER.md). Independent review also caught libvirt's extra resolved
bridge on network-backed NICs; logical network identity is now compared correctly.

## Workload measurements

The VM retained four vCPUs, 1 GiB RAM and identical devices throughout baseline, disk-only,
NIC-only and combined arms. Benchmark variants were direct libvirt XML changes while the Core
agent was stopped; its exact definition was restored afterward. These are measurement fixtures,
not new supported tuning APIs.

Three trials per case used direct `fio` libaio 4K random read/write, queue depth 32, a 256 MiB
prewritten file and three-second measured intervals. `iperf3` used one/four streams for three
seconds in both directions. Twenty idle pings followed each network trial. fio reports completion
p99, not whole-request latency; ping's sample p99 is the maximum of twenty idle samples, not
latency under network load. CPU fields are guest fio/iperf CPU, not host CPU cost.

The earlier full matrix also executed sequential I/O and depth-one cases successfully, but its
terminal output truncated the numeric comparison. Only the focused rerun's complete numeric
capture is retained here: [72 individual trial records](2026-09-24-automatic-vm-io-trials.csv).

Values below are median [minimum–maximum] of three trials. MB/s and Gb/s use decimal units.

| Workload | Baseline | Disk only | NIC only | Both |
| --- | --- | --- | --- | --- |
| 4K random read, MB/s | 662.8 [648.3–784.9] | 1330.6 [1244.4–1394.9] | 907.6 [753.9–915.3] | 1235.8 [1186.2–1297.4] |
| 4K random write, MB/s | 607.6 [590.7–652.9] | 983.7 [920.2–1083.2] | 622.6 [584.4–699.2] | 757.0 [418.2–1100.3] |
| Guest → host, 1 stream, Gb/s | 46.5 [43.9–47.1] | 53.7 [50.6–54.8] | 59.2 [59.1–66.5] | 54.5 [53.6–60.3] |
| Guest → host, 4 streams, Gb/s | 45.2 [43.1–45.9] | 45.2 [45.0–45.9] | 64.0 [58.8–68.6] | 58.0 [42.0–60.5] |
| Host → guest, 1 stream, Gb/s | 56.1 [40.0–58.7] | 53.9 [53.0–56.3] | 55.6 [55.4–58.9] | 57.4 [57.1–64.9] |
| Host → guest, 4 streams, Gb/s | 35.6 [32.4–40.0] | 40.0 [36.9–41.5] | 54.2 [48.2–55.5] | 55.2 [49.6–55.8] |

QEMU IOThread 1 accumulated 1654 CPU ticks in the disk-only arm and 1544 in the combined arm.
The Debian guest automatically reported four current combined channels in the NIC arms and one
in the other arms. The harness also explicitly set the intended guest channel count with
`ethtool -L` and asserted it before measurements; other guests may require that activation.

The four-channel receive path also had a material cost: four-stream TCP retransmissions were
[2, 1, 1] in baseline, [71767, 68576, 75022] in NIC-only and [70252, 51142, 80906] in combined
three-second trials. Tuned guest CPU during these receive trials was about 123–144%, versus
93–109% untuned. The guest/domain was cleaned up before diagnostic interface counters were
retained, so the cause remains uncertain. Higher throughput alone does not establish an
unqualified improvement. A bounded one/two/four-channel follow-up was attempted with a separate disposable guest,
but it did not reach cloud-init networking/SSH after the final setup correction. No channel-limit
samples were collected, so the two-channel alternative remains unmeasured.

## Automated checks and review

- `go test -race -timeout 20m ./...`: passed across all Go packages. After the final source
  normalization/SCSI fixes, `go test -race ./internal/vmhost -count=1` also passed.
- `python3 -m unittest tests.test_api_contract`: passed, 74 tests.
- `python3 scripts/generate_api.py --check`: passed; all three references current.
- `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'`: passed.
- Native Python compilation, shell syntax and `git diff --check`: passed.
- `python3 scripts/check_docs.py --worktree`: passed for all 16 changed paths, including retained trial data.
- The original broad Python run executed 399 tests with three failures. The queue threshold
  confused a CPU-ceiling source assertion and was corrected in this slice. Two stale workflow
  assertions remained; the 2026-09-25 PR merge-readiness follow-up corrected them, then the
  broad discovery suite passed all 399 tests. See BUG-0570 in the bug ledger.
- Independent review found no remaining confirmed Core defect after corrections. Independent numeric evidence review verified all 72 CSV records against the saved JSON
  and recomputed every displayed median and range. No remote CI run or production deployment is claimed.

## Acceptance limits

The exploratory nested measurements use a fixed arm order and very short trials. Host caching,
qcow2 allocation, time drift and shared-host scheduling can confound differences. They establish
executed workloads and native compatibility, not repeatable production speedups. The higher
retransmission counts need qualification before treating four queues as a production performance
win. Physical storage, NFS, physical network throughput, Windows guest behavior and browser
walkthroughs were not measured in this slice; no frontend code changed.

Native partial-operation fault injection remains untested. Go fake-virsh regressions cover partial
IOThread addition/retry and reject persistent-only completion. Controller replacement was tested
separately from mutations and does not prove interruption at every native effect boundary.
