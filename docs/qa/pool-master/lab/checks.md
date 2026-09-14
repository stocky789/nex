# Pool master model: real lab verification

Date: 2026-09-09. Lab: disposable nested-KVM `nex-fresh-lab` from the
[fresh install QA](../../fresh-install/checks.md) (Nex Core 0.1.0-alpha1, `nex-core-local 0.1.0+managed1`,
appliance at `10.0.2.20`, loopback forwards).

Build: uncommitted tree (HEAD `1fb9782` + pool-master + concurrent add-host-by-address) built in
`debian:13-slim` for the appliance's `x86-64-baseline` (workstation builds failed: `CPU ISA level is
lower than required`). UI, API reference and source archive rebuilt. Files replaced in place with
backups (`/root/pool-qa-backup-*`); no package rebuilt, so **package upgrade not covered**. Temporary
root SSH enabled via console, removed afterwards.

| # | Check | Result | Evidence |
|---|---|---|---|
| 1 | `bash tests/integration/phase1-debian.sh` (real `libvirtd`, real `nex-core-agent enroll`) | **pass**; first run failed on an inline fixture still creating a pool, fixed; asserts pool named after host, token site, `master_host_id` | `.cache/pool-master-debian.log` (not published) |
| 2 | In-place Controller upgrade | Before: one manual **Fresh install pool**, one host, two VMs, pools route existed (400). After: name kept, `master_host_id` = host; host `fresh`; appliance, stopped guest, storage, network, media retained; pools 404; `{pool_id}` token 400; unknown site 400; active re-enroll 409; `{site_id}` token 201 | [before-upgrade.json](before-upgrade.json), [after-upgrade.json](after-upgrade.json) |
| 3 | Browser on upgraded Controller and Core local UI | 15 checks pass. Inspected: pool page **Pool master nex-fresh-lab** and tree suffix; host **Pool master** badge `fresh`; **Add host to Fresh install site** form (with add-by-address fields); no **Create pool**; Core local deployment `ready`, no **Pool name** | [browser-results.json](browser-results.json) (`lab-controller-*.png`, `lab-core-local-overview.png`, local only) |
| 4a | Reset lab (VMs undefined, disks kept in `/var/lib/libvirt/images-pool-qa-old`, state backed up to `/root/pool-qa-backup-reset`) and GUI deploy via `fresh_install_browser.py --stage deploy` (site only) | Packaged appliance had the old Controller: handoff `needs_attention`, "controller rejected /api/v1/appliances/bootstrap (HTTP 400)" — version skew rejected, recoverable | [deploy/](deploy/), [deployment states](deploy/browser-deployment-state.json) |
| 4b | Replace appliance Controller (backup `/root/pool-qa-backup-image`), retry | First retry failed: UI replacement removed `appliance-ready.json`; restored marker, `POST /api/local/v1/deployments/{id}/retry` reached `ready` | [retry-states.json](deploy/retry-states.json) |
| 4c | Fresh bootstrap | One site, no pool from bootstrap; enrollment created pool `nex-fresh-lab` with host as master; appliance adopted; audit `appliance.bootstrap`, `host.enroll`, `pool.create`, `appliance.adopt`; pools 404 | [fresh-deploy.json](fresh-deploy.json) |
| 4d | Browser on fresh appliance | Same 15 checks pass; pool page shows master, suffix, one adopted `nex-controller` VM | [fresh/browser-results.json](fresh/browser-results.json) |

## Limits and cleanup

- Not covered: package upgrade, physical hardware, multi-host pools, add-host-by-address
  ([separate run](../../simple-host-enrollment/lab/checks.md)).
- The lab was handed to the add-host session, which replaced the Controller, revoked/re-enrolled the
  host twice (identity and pool kept) and left one revoked host with its empty pool; it removed the
  SSH drop-in and key (lab key rejected) and stopped the file-transfer `http.server`.
- A revoked host keeps its automatic pool so re-enrollment restores it; permanent removal belongs
  to Phase 4 eject.
