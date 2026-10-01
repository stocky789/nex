# Phase I-2 reproducibility gate (S9): double-build record

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), spike S9 and "Reproducible build"
(acceptance: identical root hash from two builds, `diffoscope` output attached on any difference).
Earlier failure: [phase0.md](phase0.md) S9 (SSH host keys in the image, roothash differed, `diffoscope` not run).
**Environment: simulation only.** Docker containers on one workstation; nothing was booted and no physical hardware was used.

- API impact: none. Guide impact: none. State owner: none for the build tooling; the one image change that touches host state
  (the iSCSI initiator name, below) is owned by `core` on the host (`/etc` overlay upper on `nex-data`).
- **Result: the base image is byte-identical across two clean builds** (final check, 2026-10-01): roothash, `root.erofs`,
  `root.verity`, kernel, initrd, the whole base tree, and the release set (UKI, ESP tree, manifest). Two earlier checks on the
  way found real differences; both are recorded below with their causes and fixes.
- **Not shown:** layers were not double-built, the Nex-built debs (`nex-core-local`, matched libvirt) were not in the build, and
  both builds ran on the same host within minutes of each other (see [Limits](#limits)).

## Method

`scripts/image-repro-check.sh` (run with `--report image/out/repro`, base and release steps only, no layers, builds sequential):

- The repository inputs (tracked and untracked, not ignored) are copied once; both runs build from that copy.
  Source digest of the final check: `964945a2da35...d599` (recorded in `inputs.txt`).
- Held equal: `SOURCE_DATE_EPOCH=1790000000`, `NEX_LABEL=repro`, `NEX_SEQUENCE=1`, the mkosi `Seed`, the package snapshot
  `20260929T000000Z` (no live archive: `image/build.sh` pins `deb.debian.org`/`security.debian.org` off), the Debian trixie tools
  container (base image pinned by digest, packages from the snapshot), Go `go1.27.1`.
- Each run has its own output directory, mkosi workspace, sandbox and package cache. The cache was empty, so run 2 downloaded
  every package again from the snapshot instead of reusing run 1's files.
- Comparison runs in the tools container as root (root-only files are read too). A tree is compared by a manifest: path,
  mode, owner, type, size, **mtime**, link target for every entry, plus a sha256 for every regular file (29,738 manifest lines
  for the base tree). Single files are compared by sha256. Any difference gets a manifest diff and `diffoscope` (text output)
  per differing file; the report lands in `image/out/repro/` (gitignored).
- Resource caps (shared workstation): every command under `systemd-run --user --scope -p CPUQuota=400% -p MemoryMax=8G
  -p MemorySwapMax=0 nice -n 19 ionice -c3`, containers `--cpus 4 --memory 8g`. One build at a time. Peak use was **not
  measured**; the caps held. Wall time per check 325-428 s. In the final check the base step took 167-171 s per run and the
  release step 3-18 s. About 2.6 GB of
  run trees were written to `image/out/repro/` and deleted after the final check.

## Check 1: first double build (before any fix) - NOT reproducible

| Artifact | Equal |
|---|---|
| `base.vmlinuz`, `base.initrd` | yes |
| `root.erofs`, `root.verity`, `roothash` | **no** |
| base tree | **no** |
| release (UKI, manifest) | **no** |

Every difference, from `diff -rq` of the two base trees and `diffoscope`:

| Path | Difference | Cause | Fix |
|---|---|---|---|
| `/etc/iscsi/initiatorname.iscsi` | `InitiatorName=iqn.1993-08.org.debian:01:<12 hex digits>` differs | `open-iscsi`'s postinst draws a random initiator name at install time | `image/mkosi.postinst.chroot` replaces the file with Debian's `GenerateName=yes` form; the first start of `iscsid` (`ExecStartPre=/usr/lib/open-iscsi/startup-checks.sh`, read from the built tree) writes a unique name into `/etc` on the host. One writer, so it cannot race the daemon. |
| `/var/cache/ldconfig/aux-cache` | binary content differs (inode numbers of the build file system) | `ldconfig` cache written during package installation | removed in `image/mkosi.postinst.chroot` (`/var/cache` is the data volume at run time and its contents are excluded from `root.erofs`) |

`diffoscope` of `root.erofs` showed exactly one hunk: the `InitiatorName=` line. The aux-cache difference was not in the erofs
(`/var/cache` contents are excluded), so the initiator name alone moved the roothash, and through it `root.verity`, the UKI (the
roothash is in its command line) and `manifest.json`. Nothing else differed in the trees. A manifest comparison that includes mtimes,
run afterwards over the same two trees, listed these two files only (every mtime was identical), so no
host key, machine id or timestamp difference remained: the host-key cause from [phase0.md](phase0.md) S9 is gone (the first
check's built `/etc/ssh` holds no `ssh_host_*` file; `nex-firstboot.service` creates them on the host).

## Check 2: after the two fixes - NOT reproducible (metadata only)

Roothash, `root.erofs`, `root.verity`, kernel, initrd and the base tree were **equal**. The release set differed in metadata
only: the mtime of the directories under `release/` and of `manifest.json` (wall-clock build time, e.g. `1790857561` vs
`1790857763`); every file's bytes, including the UKI, were identical. Cause: `image/release.py` clamped the mtimes of the
files it listed but not of the directories or the manifest it wrote last. Fix: it now clamps every entry under `release/` to
`SOURCE_DATE_EPOCH` after `manifest.json` is written (and the two earlier per-file clamps are gone).

## Check 3: final - reproducible

| Artifact | Digest (both runs) | Equal |
|---|---|---|
| `roothash` | `ababbee61bc14150b8c8ce3c5fdea8b53e70d6635574a78946f2384f09375da6` | yes |
| `root.erofs` (315,179,008 B) | `ff03013df0024800ac7859511bb25e1af3ae87fdd435d2cbbd8e29416b250b55` | yes |
| `root.verity` (2,494,464 B) | `30558ae1ebca69261f0236cb07eee66a4ed290d01f3bb103f5ac3c80b28d8ade` | yes |
| `base.vmlinuz` (12,142,528 B) | `2b2358b37674d2505350528875bb17afae2a36522a9e8a9417eaca65a7da0e08` | yes |
| `base.initrd` (38,979,632 B) | `084730dc97796b3f06e06d58e1cf1c2c257d4f5bfd5ccbebb0f7bb5c28d44a2f` | yes |
| base tree (manifest digest) | `787b4df489b335a00f3e1ef830098b12485fa1da18f4bd2626814186042b6079` | yes |
| release set: UKI `nex-core_repro_1.efi` (51,221,504 B), ESP tree, `manifest.json` (manifest digest) | `c74ddfeef2dd0608b770d921ebd5cd1e31729e9900a42a388b0a2e3c45a3749e` | yes |

Roothash of the image (`label repro`, sequence 1): `c7952d4f9a43a77cade65cb6e9321ff089289baffff4a63a2b99f0ba47db1edf`
(the `roothash` row above is the sha256 of that file). The script exited 0.

How the final table was produced: the two builds ran with the previous revision of the comparison step, which hashed a
single file by hashing its sha256 line. I then changed the script to print the plain file sha256 and re-ran **only the
comparison step** of the final script over the same, unmodified outputs; the table is that result, and the values match
`sha256sum` of `root.erofs` and the UKI and the entries in the release `manifest.json`. The tree digests are identical in
both revisions.

Spot checks on the final base tree (text files only): no match for the build day's date strings (`2026-10-01`, `20261001`,
`2026-09-30`, `1 Oct 2026`, `Oct  1 2026`) and none for the builder's home or work-tree path; a search for the builder's
login name followed by `@` matched only an upstream author address in the `adduser` package's own copyright and scripts.

## Tests

- `python3 -m unittest tests.test_image_composition`: 17 pass. The new `ReproducibilityTest` finalizes the same layer content
  created in opposite order with different mtimes and requires identical trees, modes, link targets, bytes, mtimes (all
  `SOURCE_DATE_EPOCH`) and an identical report. With `clamp` replaced by a no-op the test fails (checked once, by hand).
- The comparison step was also smoke-run on a synthetic pair of trees (changed file, mode change, dangling symlink, a file in
  one run only, single files that differ): the listing, the per-file `diffoscope` and the exit status behaved as described.

## Limits

- **Layers not double-built.** `scripts/image-repro-check.sh --layers nex-zfs` (or `--no-base --base-tree DIR --layers ...`)
  supports it, but the ZFS module build plus layer build twice did not fit the 30-minute time box, so no layer hash was
  compared. The plan's acceptance ("every layer") is **open**. Only the finalizer's invariants are unit-tested (above).
- **Nex-built debs absent.** `NEX_PACKAGE_DIRS` was empty, so `nex-core-local` and the matched libvirt were not installed: this
  is the locked Debian set from the snapshot, not the shipping image. Their own build reproducibility is not covered.
- **Same host, same day, minutes apart.** The plan asks for two independent runners. Differences that depend on the host
  (CPU count, kernel, Docker version), on the calendar day, or on a different network path were not exercised. The tools image
  ID changed between the three checks (`a17d433b...` at check 1, `39740e29...` at check 3 start, `1b90c36f...` afterwards);
  its content is pinned by base digest and snapshot, but I did not compare the contents of the different IDs. [INFERENCE] The
  creation time of the current image equals `SOURCE_DATE_EPOCH`, so a build with that variable exported rewrites timestamps.
- `scripts/image-lock.py check` against the built package list was **not re-run** in this session (the earlier session matched
  317 packages). The comparison shows the two builds got the same files, not that they match `packages.lock`.
- Unsigned UKI only: signing (`NEX_UKI_KEY`/`NEX_UKI_CERT`) and a signed module path were not part of the comparison.
- The image was **not booted** after the iSCSI change. `GenerateName=yes` and `startup-checks.sh` were read from the built tree,
  not exercised: a host first start that creates the name, and the persistence of that name across a reboot, are untested.
  Until `iscsid` starts, `/etc/iscsi/initiatorname.iscsi` holds `GenerateName=yes` and no name. The plan's table row says
  `nex-firstboot.service` generates it; the implemented mechanism is the one Debian ships.

## Reproduce

```
systemd-run --user --scope -q -p CPUQuota=400% -p MemoryMax=8G -p MemorySwapMax=0 nice -n 19 ionice -c3 \
  scripts/image-repro-check.sh --report image/out/repro
```

Report: `summary.tsv`, `inputs.txt`, `run1.log`, `run2.log`, `diff/` (only when something differs).

## Changed files

`image/mkosi.postinst.chroot`, `image/release.py`, `image/mkosi.images/build-layer.sh` (container caps only),
`scripts/image-repro-check.sh` (container caps, mtimes and metadata in the comparison, manifest-diff listing, plain file
digests), `tests/test_image_composition.py`, `docs/qa/image/phase2-installer.md` (outstanding items corrected).
