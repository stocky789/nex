# Working rules for Nex

This is the shared contributor contract for Nex Controller and Nex Core. Tool-specific files
reference this file; keep working rules here rather than maintaining competing copies.

## Read before working

- Read `docs/plans/2026-09-08-nex-platform.md` for the product model, build order, and acceptance gates.
- Read the relevant feature/UI research and current phase under `docs/plans/`; research is inspiration,
  not authorization to expand the current milestone.
- Inspect the actual code and working-tree changes before editing. Planned files and checks are not
  evidence of implementation. Create additional documentation only when it has something useful to record.
- Keep every project plan under `docs/plans/`. Record scope changes and decisions in the relevant plan.
  Resolve material product changes with Matt; use judgment for routine implementation choices.

## Communication

Keep replies concise by default, using at most these three short fields:

- **Need from you:** `Nothing` or one exact action/decision.
- **Done:** One or two sentences describing the outcome.
- **Attention:** Genuine limitations, risks, or follow-up only; omit when none.

Requested reports, plans, and explanations may be detailed. Intermediate updates are one sentence
when work starts, materially changes, blocks, or needs a progress update. Ask one direct question
when blocked; do not repeatedly ask permission for already authorized work.

## Agent workflow

- The primary owns intent, decomposition, coordination, user decisions, acceptance, and synthesis.
  It may implement work itself and remains responsible for the final result.
- Use subagents proactively for independent, bounded work, especially research, exploration, review,
  and test triage. Use useful concurrency; do not delegate trivial commands to inflate agent count.
- Assign each worker an objective, scope, relevant sources, acceptance criteria, required checks,
  and either a read-only lane or an exclusive write set. Give only the context its task needs.
- One writer owns a file at a time. Partition concurrent edits; sequence dependent changes. Preserve
  unrelated dirty work and report ownership conflicts before changing someone else's files.
- Hand off outcome, changed paths, actual checks/results, evidence, and unresolved decisions concisely.
- Have an independent reviewer check meaningful implementation/documentation batches against scope,
  behavior, safety, and real evidence. Return findings to the owner, then verify the corrections.
  If independent review is unavailable, disclose that limitation; do not claim self-review is independent.

## Product and architecture boundaries

- Nex Controller is the centralized management service/API and web UI; Nex Core is the Debian-based
  host distribution with KVM/QEMU, libvirt, and a local agent. Do not implement a new hypervisor.
- Use Go for controller/agent, React and TypeScript for the UI, and Debian packages/systemd plus
  installer automation for delivery. Keep the Debian base intact; prefer existing platform capabilities.
- Product hierarchy: **Sites → Pools (one master host + members) → Hosts → VMs**. Pool is the
  management grouping. Administrators never create pools: each enrolling host becomes the pool master
  of its own automatically created pool, named after its hostname; enrollment tokens bind to a site.
  Joining a member, changing the master and ejecting are Phase 4 work. HA is a later capability, not
  a prerequisite. Do not add a duplicate Cluster tier without an agreed requirement.
- VMs belong to pools and have separate host assignments. Preserve identity across migration.
- Storage registrations, backup repositories/policies, and logical networks belong to pools.
  Track actual access per host: pool-level registration never makes a local disk shared.
- **Nex Controller is replaceable; the pool master is the durable source of truth for its pool.**
  The web UI and API act on hosts, not on a private Controller model. Every pool-scoped setting
  (membership, networks, storage, media, VM definitions, policies, VM identity) must be persisted on
  the pool master's Nex Core and re-adopted, with identity and names, when a host (re-)enrolls with a
  new or rebuilt Controller. Replacing a failed Controller must need no more than installing a new one
  and connecting each pool master to it; nothing else is re-entered. Controller-scoped data (sites,
  administrator accounts/roles, audit history, enrollment tokens, its CA) is the only exception and
  needs Controller backup/restore. Agents/libvirt report observed state; show stale/unknown state
  honestly and reconcile ambiguous outcomes before retrying. Decision: Matt, 2026-09-09.
- Running VMs and local recovery access must survive a controller outage. Never interpret an unreachable
  host as proof its VMs stopped. Never permit duplicate writable execution after a timeout or partition.
- Snapshots, independent backups, verified restoration, and compatible shared-storage live migration
  are required phased features. Start shared storage with external NFS; Ceph integration comes later.
- A snapshot is not a backup. Protect complete disk/configuration recovery, backing chains, exclusive
  ownership, and restore integrity. A completed backup job is not proof of a working restore.
- Migration requires compatible CPU/runtime, networking, storage, and firmware, not just shared disks.
  HA requires proven quorum, fencing, and failure recovery before automatic restart is enabled.
- Use vCenter and XO6 as inspiration for operator workflows and navigation, with the simplest working
  UI first. Do not copy branding or present documentation-only research as inspected visual behavior.
- Container Services use Docker Swarm in Nex-managed node VMs, with the full lifecycle in Nex Controller;
  not LXC or Docker directly on Nex Core. Keep this after the VM foundation and preserve the agreed
  colorful application/service icons. Follow Phase 10 for storage ownership, recovery, and quorum rules.

## Licensing and redistribution

- Read [docs/LICENSING.md](docs/LICENSING.md) before changing dependencies, packaging, or release delivery.
  Original Nex code, build scripts, and configuration use AGPL-3.0-only; Debian and other third-party
  components retain their own licenses and notices. The ISO as a whole is not relicensed as AGPL.
- Separate Debian programs can ship alongside Nex. Review exact licenses before copying code, linking
  libraries, or adding plugins; aggregation does not establish compatibility for a combined program.
  Do not combine GPL-2.0-only code into AGPL Nex without appropriate additional permission.
- Before external ISO distribution, including alphas, verify a complete component manifest, retained
  matching corresponding source, preserved notices, and a tested source-delivery path. A Nex-only
  source archive or generic Debian mirror link is insufficient. Include base ISO/installer components
  and firmware in the review; resolve Nex artwork redistribution terms separately.
- License compatibility is not release certification. Record missing evidence as outstanding;
  future Nex network services must provide AGPL source access where required.

## Implementation and safety

- Basics first: finish the current phase's end-to-end acceptance gate before dependent work expands it.
  The entire future roadmap does not need to be built to complete the current phase.
- Trace the affected flow and callers before choosing a fix. Reuse existing code, stdlib, native
  platform features, and installed dependencies before adding code or dependencies.
- Prefer the smallest correct change. No speculative abstractions, plugin frameworks, empty scaffolding,
  or parallel sources of truth. Never simplify away validation, accessibility, security, or data safety.
- Authenticate and authorize control operations and consoles at the API boundary. Validate host identity,
  scope credentials narrowly, and keep secrets out of logs, source control, images, and diagnostic bundles.
- Disk erase, overwrite restore, VM/disk deletion, and management-network changes need explicit target
  selection and appropriate confirmation/recovery behavior in the product.
- A code or planning request does not authorize installing onto real disks, changing live infrastructure,
  rebooting hosts, or exercising destructive tests against production. Use explicitly selected disposable
  lab resources; obtain missing authorization before operating on live infrastructure.

## Required API impact check

- Every change, including UI, installer, configuration, documentation, and roadmap work, must assess
  whether it needs a new or changed API between Nex Controller and Nex Core. Trace the affected caller,
  controller operation, agent command, and observed-state/result flow; also assess the client-facing API.
- Record `API impact: none | existing contract | changed contract | new contract` with a brief reason
  in the change/PR summary and, for planned work, the relevant plan. Name affected operations/contracts.
  `none` requires a reason; it does not require inventing an endpoint or running unrelated API tests.
- Reuse an existing contract when it covers the behavior. When a contract must change, implement both
  sides and define authentication, authorization, validation, errors, asynchronous results, safe retries,
  outage/reconnect behavior, and Controller/Core version compatibility in the same slice.
- Every new pool-scoped setting must name its durable host-side record on the pool master and the
  inventory/adoption path a fresh Controller uses to recover it. A setting that exists only in the
  Controller database leaves the slice open; record it as such in the plan.
- Maintain [docs/API.md](docs/API.md), the living reference for all Nex API calls, functions/commands,
  events, and streams. Update the applicable machine-readable contracts and generated reference in the
  same change as API behavior; keep planned interfaces clearly separate from implemented ones.
- API-affecting work needs focused runnable contract/integration checks covering the changed behavior
  and relevant authorization, invalid input, and failure/retry paths. Check implementation/reference
  coverage and compatibility. Record actual results; a successful HTTP response alone is insufficient.
- Independent review and every phase acceptance gate must verify the API impact decision, required
  implementation, reference updates, and check evidence. Missing required API work leaves the slice open.

## Living user guide and documentation gate

- Apply [docs/DOCUMENTATION.md](docs/DOCUMENTATION.md) to every change. Assess guide impact as
  `updated` or `none` with a reason, alongside the required API impact decision. Internal refactors
  do not need invented user instructions; changed user behavior does need guide updates.
- Maintain the [user guide](docs/guide/README.md) in the same change as each implemented feature,
  including GUI workflows, CLI/installer behavior, configuration, permissions, errors, and recovery.
  Update or remove obsolete instructions when behavior changes or features are removed.
- Walk through the documented steps against the actual implementation in a disposable environment.
  Exercise applicable success, failure, and recovery paths; for GUI features use browser interaction
  and inspect captures. Record version/environment, method, actual results, evidence, and untested
  limits. Simulation must be labeled; it cannot establish real hardware or storage correctness.
  Never mark an unexecuted workflow verified. Missing applicable verification leaves acceptance open.
- Use portable Markdown under `docs/guide/`, stable descriptive filenames, relative links, and the
  required page sections/navigation. This is the future website's source, not a second manual to
  rewrite later. Do not expose contributor implementation details in operator workflows.
- Add a scoped `docs/changes/*.json` assessment covering changed paths. Applicable guide/API changes
  must name updated documentation and verification evidence; `none` requires a reviewable reason.
  Run the documentation checker and relevant behavior checks. Follow the documented CI procedure.
- Independent review and every phase gate check guide/API applicability, documentation accuracy,
  and actual walkthrough evidence. Automated structural checks cannot judge semantic completeness
  or prove that a walkthrough ran. No applicable documentation work is deferred to project end.

## Verification and bug memory

- Nontrivial logic ships with a focused runnable check. Use the repository's existing test tools when
  available. Test observable behavior and failure recovery; do not freeze known-bad prototype output.
- For bugs, reproduce the failure and add a failing regression check before fixing the root cause where
  practical. If the check cannot reproduce it, record the limitation and the alternative evidence.
- Every bug fix must include a regression test and ensure it runs in both GitHub Actions
  (`.github/workflows/`) and Gitea Actions (`.gitea/workflows/`) on pushes and pull requests.
  Reuse an existing test suite when both workflows already run it; otherwise update both workflow
  files in the same change. The test must detect the original bug, pass with the fix, and fail CI
  if the bug returns. Record the test command and results in `docs/BUG_LEDGER.md`; missing runner
  or disposable-lab prerequisites leave regression coverage outstanding, not silently skipped.
- Check relevant `docs/BUG_LEDGER.md` entries before fixing a previously affected system, if the ledger
  exists. Create it with the first bug fix; append ID, date, symptom, root cause, fix, regression evidence,
  and the wrong approach to avoid. Coordinate append ownership and check IDs for duplicates.
- Run checks appropriate to the change: backend tests, UI typecheck/build, browser behavior, or disposable
  host integration as applicable. A docs-only change needs link/scope review, not invented application tests.
- Green is not proof: inspect actual results/artifacts and confirm expected checks ran rather than skipped.
  Never claim unrun tests, hardware coverage, restores, migrations, or CI success.
- After an authorized push, inspect the actual CI run and required job results. Report failing, missing,
  pending, or skipped required checks honestly; a workflow file alone establishes no coverage.
- UI work needs real browser review: inspect captures and exercise relevant loading, empty, error,
  stale-data, keyboard, and destructive-action states. Fix and recapture material defects.
- A phase is complete only when its functionality, relevant safety/recovery checks, documentation,
  and UI review pass. Record remaining work explicitly; do not mark partial work complete.
- Update documentation in the same change as behavior. Keep changes coherent and commit messages
  imperative with an appropriate scope such as `controller:`, `agent:`, `ui:`, `installer:`, or `docs:`.
