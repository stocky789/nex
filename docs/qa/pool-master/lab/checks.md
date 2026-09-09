# Pool master model: real lab verification

Date: 2026-09-09. Environment: the disposable nested-KVM lab `nex-fresh-lab` retained from the
[fresh install QA](../../fresh-install/checks.md) (Nex Core 0.1.0-alpha1 installed from the shipped
ISO, `nex-core-local 0.1.0+managed1` package, real Controller appliance at `10.0.2.20`, loopback
forwards only). Nothing here touched workstation disks, bridges or production systems.

Binaries were built from the **uncommitted working tree** (git HEAD `1fb9782` plus the pool-master
change and the concurrent add-host-by-address change) inside a `debian:13-slim` container so they
match the appliance's `x86-64-baseline` CPU model; workstation-built binaries were rejected by the
appliance loader (`CPU ISA level is lower than required`). The UI bundle, API reference and
matching source archive were rebuilt from the same tree. Files were replaced in place with
backups (`/root/pool-qa-backup-*` on the host and inside the original appliance); no package
was rebuilt, so package upgrade behaviour is **not** covered.

Temporary root SSH access was re-enabled on the lab host for this session through the console
(QMP keystrokes, existing lab QA key) and removed afterwards; see the end of this page.

## 1. Real agent enrollment creates the pool (Debian 13 + real libvirt)

`bash tests/integration/phase1-debian.sh` (Docker `debian:13-slim`, real `libvirtd`, real
`nex-core-agent enroll`, host-built binaries): **pass**, exit 0. The first run failed because the
script's inline fixture still created a pool by hand; the fixture was corrected in the same change
and now asserts that the enrollment-created pool is named after the host, belongs to the token's
site and lists the host as `master_host_id`. Log: `.cache/pool-master-debian.log` (not published).

## 2. In-place Controller upgrade migrates a manual pool

Before the upgrade the appliance database held one hand-created pool, **Fresh install pool**, one
enrolled host and two VMs ([before-upgrade.json](before-upgrade.json); `POST /api/v1/pools`
returned 400 for an invalid body, proving the old route existed).

After replacing the Controller binary/UI/API/source and restarting the service
([after-upgrade.json](after-upgrade.json), all checks pass):

- the pool kept its name and gained `master_host_id` equal to the only enrolled host;
- the host reconnected `fresh`; the protected Controller appliance, the stopped guest VM, storage,
  network and media records were retained;
- `POST /api/v1/pools` now returns 404; a `{pool_id}` token body returns 400; an unknown site
  returns 400; re-enrolling the active host returns 409; a `{site_id}` token is issued (201).

## 3. Browser walkthrough of the upgraded Controller and Core local UI

`browser-results.json` records 15 passing checks with real Chromium against the appliance and
the upgraded `nex-core-local`. Captures inspected:

- `lab-controller-pool-page.png`: pool General panel shows **Pool master nex-fresh-lab**, the
  subtitle "The pool master controls the pool", and the tree entry `nex-fresh-lab · master`.
- `lab-controller-host-page.png`: host header shows the **Pool master** badge and `fresh`.
- `lab-controller-add-host-form.png`: **Add host to Fresh install site** with the help text
  "The new host becomes the master of a new pool in this site…"; the form also carries the
  concurrent add-host-by-address fields (address, root password, Controller address).
- `lab-controller-file-menu.png` / `lab-controller-home.png`: no **Create pool** anywhere.
- `lab-core-local-overview.png`: existing deployment `ready`; no **Pool name** label.

## 4. Fresh deployment: bootstrap without a pool name, automatic pool, adoption

The lab was then reset (both VMs undefined, their disks kept under
`/var/lib/libvirt/images-pool-qa-old`, local deployment/agent state backed up to
`/root/pool-qa-backup-reset` and cleared) and a new Controller was deployed through the real
Core local GUI with `fresh_install_browser.py --stage deploy` (site name only; captures under
[deploy/](deploy/)).

1. **Version skew is rejected safely.** The packaged appliance image still ships the previous
   Controller, so the upgraded `nex-core-local` handoff stopped at `needs_attention` with
   "controller rejected /api/v1/appliances/bootstrap (HTTP 400)" ([deploy log states](deploy/browser-deployment-state.json)).
   This is the documented Controller/Core compatibility rule; the deployment stayed recoverable.
2. The Controller inside the new appliance was replaced with the working-tree build (backup
   `/root/pool-qa-backup-image` in the guest). A first retry failed because replacing the UI
   directory had removed `appliance-ready.json`; restoring the marker and retrying through
   `POST /api/local/v1/deployments/{id}/retry` completed the handoff to `ready`
   ([retry-states.json](deploy/retry-states.json)).
3. [fresh-deploy.json](fresh-deploy.json): all checks pass. The bootstrap created exactly one site,
   **Fresh install site**, and no pool; enrollment created exactly one pool named `nex-fresh-lab`
   in that site with the host as `master_host_id`; the appliance was adopted into that pool and
   host; the audit log records `appliance.bootstrap`, `host.enroll`, `pool.create` and
   `appliance.adopt`; `POST /api/v1/pools` is 404.
4. [fresh/browser-results.json](fresh/browser-results.json): the same 15 browser checks pass on
   the fresh appliance. `fresh/lab-controller-pool-page.png` was inspected: pool **nex-fresh-lab**,
   **Pool master nex-fresh-lab**, tree `nex-fresh-lab · master`, one adopted `nex-controller` VM.

## Limits and cleanup

- Not covered: package upgrade paths, real physical hardware, multi-host pools (join/master
  change/eject are not implemented), and the add-host-by-address feature (verified separately).
- The lab was then handed to the concurrent add-host-by-address session, which replaced the
  appliance Controller with its own working-tree build, revoked and re-enrolled the real host twice
  (identity and pool preserved), and left one extra revoked host record with its empty automatic
  pool. That session removed the temporary root SSH drop-in and key and restarted `ssh`; the lab
  key is rejected again. The `http.server` used for file transfer was stopped.
- Observation: a revoked host keeps its automatic pool (master = the revoked host) so that
  re-enrollment restores it. Permanently removing a host and its empty pool is not implemented;
  it belongs with the Phase 4 host removal/eject work.
