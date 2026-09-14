# Bug ledger local acceptance

Date: 2026-09-12. Development working tree. Native evidence for BUG-0093 through BUG-0096;
symptoms, causes and fixes are in [the bug ledger](../BUG_LEDGER.md). BUG-0097 nested UEFI
is excluded by project owner.

## Environment and method

- Two new Debian 13 BIOS/KVM guests on private overlays and an isolated guest-to-guest
  network; a separate local Controller with disposable state and TLS credentials. Existing
  workstation guests and infrastructure were not targets.
- Initial Core verification staged the current binary and service unit (not a package or ISO
  install). Health/ISO walkthrough: Core SHA-256
  `3de81caf291a4725aaf0f0a9dbd24582e2810deba531d8ba009f54d68bad0314`, kernel
  `6.12.107+deb13-cloud-amd64`, systemd `257.13-1~deb13u1`, libvirt `11.3.0-3+deb13u2`.
- Primary executed native steps; browser checks used Chromium with inspected captures.

## BUG-0093: automatic ISO registration

- Original sandbox: `runuser -u libvirt-qemu -- id -u` failed `Operation not permitted`; adding
  only `AmbientCapabilities=CAP_SETUID` passed (other confinement, including `NoNewPrivileges`
  and `RestrictSUIDSGID`, unchanged).
- Staged service: fresh Controller showed one host and one healthy **ISO** registration at
  `/var/lib/libvirt/images`, no manual request; Core journal ID matched inventory
  ([service evidence](bug-0093/packaged-service.md)).
- Package: rebuilt `nex-core-local_0.1.6~alpha1+bugledger1_amd64.deb` (UI typecheck/build passed),
  installed on a new 3 GiB BIOS guest from cached `nex-core-0.1.6~alpha1-amd64.iso`
  (`1895f74a6f4e45564ad2384bbd1aa582753b5cc6701bace650feb1336061e370`; installer-produced
  provenance stamp). PAM login and enrollment passed; automatic healthy ISO registration adopted
  with the journal ID; exactly one registration with that ID survived service restart.
- Lab repairs (not installer fixes or upgrade-path evidence): SSH/network preseed did not persist
  (configured via BIOS console); added explicit Debian mirror sources; removed a broken
  `start-stop-daemon` diversion. Unmodified ISO upgrade certification is out of scope.

## BUG-0096: health reasons

- `.cache/phase1-qa-venv/bin/python tests/integration/phase1.py --browser`: exit 0, 20 outcomes
  (simulated libvirt: healthy, degraded/failed, multiple checks, stale/unknown, revoked, detail
  navigation); multiple-check capture inspected.
- Native: host with no guest definitions; libvirt service/sockets stopped; fresh failed `libvirt`
  observation with reason `local libvirt is unavailable` shown in pool summary, Hosts table
  (count and reason) and **View host health**; capture showed all three affected checks
  (libvirt, CPU model, guest observation). Units restored in `finally`; fresh healthy observation
  returned. Both assertions passed. Focused agent test, UI typecheck/build passed; independent
  review accepted.

## BUG-0095: native discovery and browser selection

- Real Controller-to-Core discovery with native `showmount`, `nfs-ls`, `smbclient`, `iscsiadm`
  against the isolated fixture returned NFS exports, authenticated SMB shares and an existing
  iSCSI target (single-initiator LUN; discovery did not log in).
- Defects found and fixed with focused regressions: root `nfs-ls` could not read the QEMU-owned
  root-squashed export (fixed by QEMU UID/GID, reserved port kept); directory parser now excludes
  regular files; malformed subdirectory character check; remote directories wrongly given
  local-root ownership rules; managed mount root lacked QEMU traversal under the service umask;
  failed-mount rollback.
- Chromium, all passed: NFS export → exact subdirectory retained; authenticated SMB subdirectory;
  invalid SMB credential → `storage discovery is unavailable`; cancelling an unreachable source
  restores the form; iSCSI selection left exclusive-initiator confirmation unchecked; empty ZFS
  discovery with manual entry; stopped Core stale and unselectable (restored). NFS/iSCSI captures
  inspected.
- Browser-submitted exact NFS and SMB directories reached healthy master registrations.
- Limit: fixture permits Core root and QEMU access (NFS root squash maps anonymous to the export
  owner); not evidence for exports denying Core write access. Independent review accepted flow and
  host/pool binding, credential-erasure and URI-encoding regressions.

## BUG-0094: native execution-lock prerequisite

- Same NFSv4 export mounted on both hosts: paused QEMU with writable QCOW2 held the disk; second
  host's QEMU refused with a lock error; opened after the first stopped. Same result on encrypted
  SMB 3.1.1 (strict caching, QEMU UID/GID mount). BIOS only; test mounts removed.
- Limit: fixture file-lock behavior only, not Nex admission or recovery.

## BUG-0094: primary native pool walkthrough

- Join: PAM local API, CSR, one-use offer and member join; both hosts reported the master-owned
  pool; obsolete automatic pool removed by Core reports (no Controller membership command).
- Core SHA-256 `ce048e1a307ca709df2d79649db4739483e5fd42f645c6759ee9dd8219f018f6`: browser-created
  NFS and SMB repositories auto-mounted on the member with the master's IDs; matching backing
  identity, QEMU write access and cross-host locking. Defects found and fixed with regressions:
  invalid internal operation IDs, missing peer route, CIFS diagnostic IDs treated as durable
  identity, invalid successful health status.
- VMs: one BIOS VM with a blank 1 GiB QCOW2 per repository on the member; libvirt `running`, domain
  XML used master repository ID and directory. 3 GiB host could not fit both 512 MiB guests plus
  the 2 GiB reserve (explicit failures); NFS VM stopped via API, SMB VM kept running. Not guest OS
  install evidence.
- In-use detach: master NFS detach refused `pool repository is reserved by a member workload`
  with the VM stopped.
- Master outage: only master Core stopped; master stale, member fresh with failed peer
  reconciliation and matching browser reason (capture inspected); SMB VM kept running; restart
  recovered healthy reconciliation.
- Unused third NFS export, all five passed: healthy member access; permission removal → failed;
  restore → recovered without re-registration; member stopped during two master updates →
  converged to generation 3 with original ID; unused detach removed inventory and both mounts.
- iSCSI fixture refused an unauthorized second initiator with no session; temporary node removed.
- Fresh Controller replacement (after fixing missing member-VM adoption with a full-report
  regression): empty database, new TLS identity, ordinary enrollment recovered both hosts, the
  pool, four storage registrations and both VM definitions; repository IDs, names, sources,
  generations, credential receipts, VM IDs and specs matched; nothing re-entered; one-shot
  `start_after_create` cleared. NFS VM stayed stopped, SMB VM running; storage and VM inventory
  captures inspected. Later stage/restart Core SHA-256
  `1ebcee8a40cc1a274aa733796f4dd439ded0c5711aaf367633e153bd165cf821`.

## Automated checks (integrated working tree)

- `GOMAXPROCS=4 .cache/toolchains/go/bin/go test -race ./...`: passed (Controller 114.525 s).
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 159 passed (resource and nginx
  hash-size warnings only).
- `npm run typecheck && npm test && npm run build` in `web/`: passed.
- `phase1.py --browser`: 20 outcomes passed (simulated libvirt/VM).
- `storage_repositories_browser.py --evidence .cache/bug-ledger-storage-browser-final`: nine
  mocked-API scenarios passed, no page errors.
- `python3 scripts/check_docs.py --worktree`, privacy checks, `git diff --check`: passed.
- Both `phase1.yml` workflows run these suites on push/PR; no commit, push or hosted CI run.

## Acceptance boundary

Independent review accepted the four scoped slices. No migration, HA, physical hardware, UEFI,
external distribution or hosted CI result is claimed. API/guide/state impact: see the ledger
entries and their `docs/changes/` records.
