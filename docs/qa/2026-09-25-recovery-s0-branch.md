# Recovery S0 off-branch snapshot probe

Date: 2026-09-25. This is bounded native evidence for one public
block-commit/rebase sequence on an off-current snapshot branch. It does not
establish a Nex product operation or close the Recovery S0 gate.

## Environment and method

The probe ran on a package-based Debian 13 Core candidate with libvirt
`11.3.0-3+deb13u3+nex2` and QEMU `10.0.13`. AppArmor was enabled and the tested
QEMU process ran under its UUID-specific enforcing profile. The disposable
domain had no guest OS or network and used two 128 MiB qcow2 disks with known
4 KiB marker bytes. Marker reads and writes were made only while the domain was
shut off. QEMU was started paused only to prove that libvirt could reopen the
resulting current chain; no guest boot was attempted.

The snapshot tree was created as A→B→C, reverted to B, then extended with D,
leaving B with two children, C and D. Each saved point and current disk had a
distinct expected marker per target. Revert B restored B's markers. Libvirt's
saved C point remained readable from its saved-domain disk sources even though
the abandoned C post-point overlay files were absent after the revert; those
absent post-point paths were recorded as stale branch heads, not as proof of
lost retained data. The domain was started paused for the public block-commit
operations and again for the final chain-reopen checks.

Before changing the chain, the probe resolved A's disk source through its first
child B. C's saved-domain disk sources were the two off-current direct children:
each immediately backed onto A's resolved top. The parent base and each A top
were checked, and standalone logical copies of the A tops were created and
compared. All saved-point markers, current D markers, snapshot parents and
delete-in-progress flags were reconciled. The probe also checked every other
persistent domain's disk-source backing chain and snapshot XML; those sources
were inspectable and contained no references to the selected scratch files.

## Observed results

- Public `virsh blockcommit` completed separately for vda and vdb, with explicit
  A-top and parent-base arguments, `--wait --verbose`, and without `--delete`.
  Each operation reported `Commit complete`. After each commit the domain was
  stopped and the job output was checked. Both resulting base images passed
  `qemu-img check` and compared identical to their independent precommit A-top
  logical copies.
- The retained B, C and D saved-point markers, and current D markers, still read
  correctly after both commits. The domain's current branch remained D. The
  off-branch C saved-source children were then confirmed again to back directly
  onto the old A tops, while each new base compared identical to its former A
  top.
- With the domain shut off and no other domain referencing the selected files,
  `qemu-img rebase -u -F qcow2 -f qcow2 -b <new-base> <C-saved-source>` completed
  for each off-branch disk. Both new chains named the expected base, passed
  `qemu-img check`, compared identical to independent pre-rebase C-source copies,
  and retained C's expected marker.
- B's metadata was redefined through `virsh snapshot-create --redefine`: its
  parent A was removed and its saved-domain disk sources were changed to the
  proven-equivalent parent bases. B's point markers remained correct. C and D
  remained children of B, and D remained current. The metadata-only command
  `virsh snapshot-delete <domain> A --metadata` then succeeded. It removed A
  from the snapshot list without unlinking disk images.
- Before unlinking A's two external top files, the probe scanned all remaining
  snapshot XML, the current domain chain, every qcow2 header in the selected
  scratch directory, and every other persistent domain's disk-source chains
  and snapshot XML. All inspected sources were readable and no references to
  the exact A-top files remained. Only those two A-top files were unlinked.
- A pre-unlink `stat` check was run, but its stdout was not retained in the
  evidence record. This report does not independently substantiate the files'
  link counts.
- After A's metadata and top files were removed, reverting to C restored both
  C markers. A paused QEMU start opened both resulting disk chains under the
  UUID-specific enforcing AppArmor profile; after stopping, the markers and
  chains still checked correctly. Reverting to D then restored both D markers;
  another paused start opened the chains under the enforcing profile, and the
  stopped markers remained correct. Exact A-top paths were absent from both
  chains. B's saved point was also checked throughout, but this final sequence
  did not reopen B separately.

These observations establish this one native public sequence on this package
and local filesystem. The block-commit/rebase sequence was driven directly with
libvirt and qemu-img commands; it is not evidence that Nex Core currently
implements or safely journals this workflow. Raw command outputs and full
machine-readable observations remain in ignored local lab scratch, not in the
public repository.

## Setup and evidence corrections

Two earlier fresh fixtures were preserved and excluded from the result. The
first reached D but its private capture helper treated the expected absent C
post-point overlay as a fatal chain error before it checked C's saved point. The
second failed to start because the private scratch directory was mode 0700 and
not traversable by `libvirt-qemu`; this was a probe setup error, not a native
snapshot limitation. The successful fixture used the existing 0755 scratch
directory convention.

Two private postcondition assertions also needed correction: one assumed a
fixed blank-line position in `virsh` output, and another matched any filename
beginning with `branch-a-`, incorrectly including valid UUID-suffixed revert
overlays. Read-only reconciliation showed the revert had succeeded, the exact
deleted A-top filenames were absent, and retained markers were correct. Neither
assertion failure was counted as a native failure.

## Limits and next evidence

This probe does not test a native snapshot-delete operation for current, leaf,
or only snapshots; snapshot capture during guest writes; crash interruption;
job cancellation or disappearance; shutdown; storage-capacity exhaustion;
backup/checkpoint APIs; other storage backends; guest boot; or the Nex API,
agent, journal, authorization and safe-retry path. The earlier depth-limit,
serialized multi-disk deletion and interruption results remain separate. These
results do not close Recovery S0 or establish a product snapshot-count bound.

API impact: none. The probe uses existing libvirt/QEMU commands and changes no
Nex API contract. Guide impact: none. No operator-facing product behavior
changed. The existing [user guide](../guide/README.md) remains the operator
reference; this contributor probe adds no operator steps. State owner: none;
all tested domains and disks were disposable lab resources.
