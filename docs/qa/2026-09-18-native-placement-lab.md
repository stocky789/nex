# Native placement lab — 2026-09-18

## Environment and scope

Disposable two-node Debian 13 cloud guests, each with 4 virtual CPUs, 6 GiB RAM,
64 GiB overlay disk and nested KVM. Each guest runs actual libvirt/QEMU and current
Nex Core binaries; the Controller runs the current CGO build with SQLite and the
built UI. Core-to-Core traffic uses an isolated guest network and authenticated
pool peer APIs. The Controller and SSH forwards bind to workstation loopback.
No existing host VM, storage registration or physical disk was modified.

This is a development-runtime lab, not an installation of the Nex ISO. Guest setup
includes the packaged PAM helper, libvirt startup hook and AppArmor policy; a
lab-specific systemd service runs Core. The Debian base image is the pinned
`debian-13-genericcloud-amd64-20260831-2587.qcow2`, SHA-256
`85a969b7e99d7c817414136033df18c58d5c45ac8d27bb36e8ccb67173d2d4e3`.
The pool was joined through authenticated Core-local CSR/offer/join APIs.

The shared repository is a real kernel NFS export inside the disposable master
guest, mounted and independently validated by the member. VMs have blank disks;
observed native domain execution does not establish guest OS installation or guest
filesystem correctness. API operations used authenticated sessions and CSRF tokens.

## Identified defects

- **BUG-0279:** after Core reported a storage registration, an exact retry returned
  HTTP 409 because projection had overwritten the original request with generated
  storage paths. Preserve the submitted payload while refreshing observed data.
- **BUG-0280:** the startup guard sometimes selected the broad default repository
  before a valid nested repository, rejecting the disk as belonging to another VM.
  Select the most-specific registered root, reject ambiguity at that root and retain
  ownership/readiness checks. Broader duplicate roots cannot hide a unique nested root.

An earlier hook permission error was a lab setup omission: the packaged AppArmor
rule had not been installed. The rule was installed before retesting; this was not
counted as a product defect. Historical failed operations remain visible in Task
history rather than being rewritten as successful.

## Artifacts

All final executable artifacts were built from the same corresponding-source archive,
including both defect fixes. Private runtime IDs, addresses, credentials, raw logs and
browser captures remain in ignored local cache; this report is a sanitized summary.

| Artifact | SHA-256 |
| --- | --- |
| Corresponding source | `2745abe82823b89688c6de321ed2a7475d9c8d9e602f705b2b1fbffd7225878a` |
| CGO Controller | `08975f8393ef567d1b5258bf11715e10c2a5413227447bcf381f8be1ddd041ec` |
| Static Core agent | `2095d28a13f61c7b2e1d0caafb0b1d83ea701a4c4f5ee991e5c649135cf82b13` |
| Static Core local service | `2c15d591dff9610de0c8ec5d2f0f321a30ad4a7e1b76639004438eba188178d5` |
| Controller Debian package | `c2c6f7a7e39e8548990d2ca4914efa79257165724023234acc5b89bbe6b7f3d9` |

## Verification

| Executed check | Observed result |
| --- | --- |
| Local-storage create on master and member | Both succeeded; master durable records contain the exact host owner; one native domain per VM. |
| Exact VM create retry | Same VM and operation identity; no second domain. |
| Missing network and insufficient disk capacity | Core rejected both requests on both targets; no native domain created. |
| Stopped definition edit on both targets | Updated names persisted on the master with no pending update left. |
| Native start on both targets after fixes | Both nested-KVM domains running through Controller → Core → libvirt. |
| Storage registration retry after fresh Core reports | Same repository and operation returned; changed request with reused key rejected with HTTP 409. |
| Controller stopped for 25 seconds and restarted | Shared member VM stayed running; both hosts reconnected with their existing identities. |
| Both Core services restarted with rebuilt binaries | Shared member VM stayed running. |
| Master service unavailable | Running shared VM continued; member local-only force-stop/start succeeded. |
| Shared VM power while master unavailable | Force-stop succeeded; start failed closed and domain remained stopped. |
| Master restored | Shared VM start succeeded again, with one native domain and unchanged identity. |
| Exact rebuilt Controller Debian package | Extraction/start smoke passed inside Debian 13: SQLite, packaged UI, corresponding source and authenticated session. |

The native harness reused an already registered member network after an attempted
second registration correctly failed; that harness correction did not require a
product change. The package smoke extracts and runs the actual package; it does not
establish installed-systemd or fresh ISO acceptance.

Focused Controller storage race checks and the full native-host package race suite
passed:

```sh
go test -race -timeout 5m ./internal/controller -run Storage -count=1
go test -race -timeout 5m ./internal/vmhost -count=1
```

Independent code review approved both fixes and the final longest-root ambiguity
correction. Both existing push/PR workflow copies run these regressions through the
Go race suite; no hosted CI run is claimed.

## Browser and final state

An independent reviewer used authenticated Chromium against the actual lab, then
inspected desktop VM/task captures and a 360px task capture after the fixes and
outage recovery. Both hosts were fresh/healthy and all three VMs were fresh/running.
No horizontal overflow, unnamed inputs, browser console errors, page errors or failed
browser requests were found. Keyboard focus plus Enter opened Task history.
Historical setup/guard failures and the deliberately rejected shared start remained
visible alongside subsequent successful power tasks. This read-only browser review
does not claim that VM creation was performed through the GUI; creation/editing and
fault cases were exercised through the authenticated API.

Worktree documentation checks passed for the 58-path reviewed batch; repository
privacy and diff checks passed. A later whole-worktree rerun detected concurrent
changes to `docs/guide/publish-a-release.md` and `docs/guide/software-updates.md`
without matching assessments. Those unrelated edits were left untouched; their
assessment remains outstanding and the later whole-worktree gate is not green.
Independent evidence review approved the final documentation against the private
result records and build report.
The lab was left running for inspection; process records, credentials, captures and
a scoped stop helper remain in ignored local cache. No test worker is required to
keep the lab running.

## Remaining acceptance

No fresh Nex ISO install, physical hardware, Secure Boot, actual SAN fencing,
heterogeneous CPU compatibility, migration, snapshot, backup/restore or HA acceptance
is claimed. Controller replacement, native shared-storage identity loss, membership
changes during placement and interrupted multi-host definition recovery remain open.
The Phase 3 and Phase 4 gates remain open.

API impact: **existing contract** — storage registration retry and VM power/startup
now preserve their documented behavior. No endpoint, request schema, authentication
or version capability changed. Controller still relays VM power to Core and reports
its observed result; Core retains repository validation and VM execution authority.
Guide impact: **none** — these fixes restore the existing registration/retry and VM
start workflows; no new operator action is required.
