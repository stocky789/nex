# Bug ledger local acceptance

Date: 2026-09-12. Development working tree; scoped BUG-0093 through BUG-0096
acceptance completed. BUG-0097 nested UEFI is excluded by project owner.

## Environment

Two newly created Debian 13 BIOS/KVM guests use private overlays and an isolated
guest-to-guest network. A separate local Controller uses disposable state and TLS
credentials. Existing workstation guests and infrastructure are outside the test
targets. Initial Core verification stages the current binary and service unit;
this is not yet a rebuilt-package installation or an ISO installation test.

The native ISO/health walkthrough used Core binary SHA-256
`3de81caf291a4725aaf0f0a9dbd24582e2810deba531d8ba009f54d68bad0314`,
kernel `6.12.107+deb13-cloud-amd64`, systemd `257.13-1~deb13u1`, and
libvirt daemon/clients `11.3.0-3+deb13u2`.

## BUG-0093: automatic ISO registration

The primary independently executed the original Core-local service sandbox's
identity probe inside the disposable member candidate. With the original
properties, `runuser -u libvirt-qemu -- id -u` failed with `Operation not permitted`.
Adding only `AmbientCapabilities=CAP_SETUID` made the same probe succeed. All other
properties, including `NoNewPrivileges` and `RestrictSUIDSGID`, remained enabled.

After the worker started the staged Core-local service and enrolled it, the
primary queried the fresh Controller and observed one fresh host and one healthy
**ISO** registration at `/var/lib/libvirt/images`. No manual registration request
was issued. The worker separately matched the Core-generated storage identity in
the managed journal and Controller inventory; see [service evidence](bug-0093/packaged-service.md).

The primary rebuilt `nex-core-local_0.1.6~alpha1+bugledger1_amd64.deb` from the
working source archive. Its internal UI typecheck and build passed. The package
was then installed on a separate, new 3 GiB BIOS guest installed from the cached
`nex-core-0.1.6~alpha1-amd64.iso`, SHA-256
`1895f74a6f4e45564ad2384bbd1aa582753b5cc6701bace650feb1336061e370`.
The actual installer produced its Core provenance stamp; no stamp was fabricated.

Lab prerequisite limitations: the test-only SSH/network preseed extension did not
persist, so the worker configured those through the guest's BIOS console. The
installed baseline had an empty Debian mirror configuration and a broken
`start-stop-daemon` diversion. Before the package transaction, the primary added
explicit Debian lab mirror sources, removed that broken diversion and restored
the existing native daemon helper. This was a disposable-lab repair, not an
installer source fix or evidence that the cached ISO's upgrade path is sound.

After the package transaction completed, the primary authenticated through the
packaged Core local API using native PAM and enrolled it into the disposable
Controller. Core automatically registered healthy **ISO** storage; the Controller
adopted the same ID found in the Core journal. A Core service restart preserved
exactly one registration with that ID. No manual storage registration or service
override was used. This completes the BUG-0093 service/registration scenario;
unmodified ISO upgrade certification remains outside this evidence.

## BUG-0096: health reasons

The worker's full process/browser command,
`.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser`, exited 0
with 20 reported outcomes. Its simulated health cases cover compact healthy
state, degraded and failed reasons, multiple affected checks, stale and unknown
observations, revoked management access and detail navigation. The primary
inspected the multiple-check capture. This suite simulates libvirt responses.

The primary then exercised an independent native scenario with the staged Core:

1. Verified that the disposable host contained no libvirt guest definitions.
2. Stopped that host's libvirt service and sockets, retaining their initial state.
3. Waited for Core's fresh failed `libvirt` observation and verified its public
   reason was `local libvirt is unavailable`.
4. Signed into the actual Controller in Chromium, opened the pool and verified
   that both the pool summary and Hosts table displayed the failed observations,
   their count and the same Core reason.
5. Followed **View host health** and verified the detailed host view carried that
   reason. Inspected the native browser capture: all three affected checks were
   legible, including the CPU-model and guest-observation failures caused by the
   stopped libvirt service.
6. Restored the initially active libvirt units and waited for a fresh healthy
   Core observation.

Both native assertions passed. Service restoration ran in a `finally` block.
The focused Core regression also rejects arbitrary credential-bearing probe error
text while preserving explicitly public native causes. UI typecheck/build and
the focused agent test passed; independent read-only review accepted this slice.

## BUG-0095: native discovery and browser selection

The primary exercised the real Controller-to-Core discovery route, with native
Debian `showmount`, `nfs-ls`, `smbclient` and `iscsiadm` against the isolated fixture.
NFS exports, authenticated SMB shares and an existing iSCSI target were returned.
The fixture restricts the iSCSI LUN to one initiator; discovery did not log in.

The first native directory test exposed an actual root-squash failure: a root
`nfs-ls` could not read the QEMU-owned export. Using the real QEMU UID/GID in the
native client request preserved the reserved-port requirement and made discovery
succeed. The corrected parser returned the existing directory and excluded the
regular file present beside it. This has a focused native-adapter regression.

In Chromium against the real Controller and Core, the primary then:

1. Discovered an NFS export, selected it, browsed its directories and selected the
   exact subdirectory; the form retained that directory.
2. Discovered an authenticated SMB share and selected its exact subdirectory.
3. Supplied a deliberately invalid SMB credential and observed the safe visible
   error `storage discovery is unavailable`.
4. Cancelled discovery of an unreachable source and verified the form became
   usable again.
5. Discovered and selected the existing iSCSI target, verifying that discovery
   did not check the required exclusive-initiator confirmation on the operator's
   behalf.

These scenarios passed. The primary visually inspected the native NFS and iSCSI
selection captures. A separate native browser run verified empty ZFS discovery
with manual entry still available, and a stopped Core becoming stale and no
longer selectable for discovery; the Core service was restored afterward.

The primary subsequently submitted the exact selected directory for both NFS
and SMB through the browser and waited for healthy master registrations. Native
testing exposed and corrected a malformed subdirectory character check, remote
directories incorrectly subjected to local-root ownership rules, and a managed
mount root created without QEMU traversal under the packaged service's private
umask. Focused regressions cover these failures and failed-mount rollback.

The supported fixture permits both Core root and QEMU access. Its NFS export
retains root squashing while mapping the anonymous identity to the explicit
export owner. This is not evidence that registration works against an arbitrary
root-squashed export that denies Core write access. Master registration passed;
member convergence is assessed separately below. Independent discovery review
accepted the native flow and subsequent host/pool binding, credential-erasure
and URI-encoding regressions.

## BUG-0094: native execution-lock prerequisite

The primary mounted the same NFSv4 export on both disposable hosts and started a
paused native QEMU process with a writable QCOW2 disk. A second QEMU process on
the other host refused the same writable disk with a native lock error. After the
first QEMU was stopped, the second opened it successfully. The same sequence
passed on encrypted SMB 3.1.1 with strict caching and the QEMU UID/GID mount
profile. Both processes used BIOS/non-UEFI configuration. Test-only mounts were
unmounted afterward.

This proves held QEMU file-lock behavior for these fixtures, not Nex's distributed
admission, mutation safety or failure recovery. Those require the complete Core
pool walkthrough below before BUG-0094 can close.

## BUG-0094: primary native pool walkthrough

The primary authenticated to each Core's native PAM-backed local API and ran the
CSR, one-use offer and member-join sequence. Both hosts then reported the same
master-owned pool to the Controller. Actual Core resource reports removed the
obsolete automatic pool; no Controller-owned membership command was used.

With source-built Core SHA-256
`ce048e1a307ca709df2d79649db4739483e5fd42f645c6759ee9dd8219f018f6`,
both browser-created NFS and SMB repositories automatically mounted on the
member using the master's repository ID. Core reported matching durable backing
identity, QEMU write access and cross-host locking. Native tests caught invalid
internal operation IDs, a missing peer route, CIFS diagnostic IDs incorrectly
treated as durable identity, and an invalid successful health status; focused
regressions accompany their corrections.

The primary created and started a BIOS VM with a real 1 GiB QCOW2 disk on each
repository, assigned to the member. Native libvirt state was `running` and the
domain XML used the master-owned repository ID and selected directory. The
3 GiB lab host could not accommodate both 512 MiB guests alongside the product's
2 GiB reserve: attempted excess allocations failed explicitly. The NFS VM was
then stopped through the product API, retaining its disk/definition, and the SMB
VM ran in maximum-memory mode. These are blank-disk execution/storage checks,
not guest operating-system installation evidence.

A master detach request for the NFS repository failed definitively with
`pool repository is reserved by a member workload`; the retained member disk
remained protected even with its VM stopped.

The primary stopped only the master Core service. Its Controller connection
became stale while the member remained fresh and reported failed peer
reconciliation. Chromium showed the matching Core reason and the master’s stale
state; the primary inspected the capture. The SMB VM remained running throughout.
Restoring the master service recovered fresh, healthy reconciliation.

A third, unused NFS export tested lifecycle independently of VM disks:

1. Register on the master and wait for healthy automatic member access.
2. Remove export directory permissions; observe a failed member access result.
3. Restore permissions; observe recovery without another registration.
4. Stop the member Core service, update the master twice, then restart the member.
   It converged to generation three with the original repository ID.
5. Detach the unused repository on the master; verify it disappeared from adopted
   inventory and neither host retained its native mount.

All five lifecycle assertions passed. The isolated iSCSI fixture separately
refused a login from the unauthorized second initiator and left no session; the
temporary test node was removed. No shared writable iSCSI mount was enabled.

Fresh Controller replacement initially exposed a missing adoption path for member
VMs using master-owned storage. After correcting that path and adding a full-report
regression, the primary confirmed that an empty Controller database, new TLS
identity and ordinary Core enrollment recovered both hosts, the original pool,
four storage registrations and both successful VM definitions. Repository IDs,
names, source selections, generations and credential receipts matched; no storage
or credential was re-entered. VM IDs and all durable specification fields matched.
Core correctly cleared the completed one-shot `start_after_create` action.

Native execution state was unchanged: the previously stopped NFS VM stayed
stopped and the SMB VM stayed running. The primary signed into the replacement
Controller, verified storage/member access and VM inventory in Chromium, and
visually inspected both captures. A subsequent stage/restart used Core SHA-256
`1ebcee8a40cc1a274aa733796f4dd439ded0c5711aaf367633e153bd165cf821`.

## Acceptance boundary

Independent review accepted the four scoped bug slices after the final API/status
documentation corrections. No migration, HA, physical hardware, UEFI, external
distribution or hosted CI results are claimed by this report.

## Final automated checks

The primary reran these against the integrated working tree:

- `GOMAXPROCS=4 .cache/toolchains/go/bin/go test -race ./...`: passed all packages;
  the Controller package completed in 114.525 seconds.
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 159 tests passed.
  Existing HTTP/socket fixture resource warnings and an nginx hash-size warning
  were emitted; they did not skip or fail checks.
- `npm run typecheck && npm test && npm run build` in `web/`: passed.
- `.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser`:
  all 20 outcomes passed; this suite labels simulated libvirt/VM behavior.
- `.cache/phase1-qa-venv/bin/python tests/integration/storage_repositories_browser.py
  --evidence .cache/bug-ledger-storage-browser-final`: all nine mocked-API browser
  scenarios passed with no page errors. Native storage evidence is separate above.
- `python3 scripts/check_docs.py --worktree`, repository privacy checks and
  `git diff --check`: passed during closeout; semantic review remains separate.

Both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` run the Go,
Python, UI and browser suites on pushes and pull requests. No commit or push was
performed, and no hosted CI run is asserted.
