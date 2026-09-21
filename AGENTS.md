# Working rules for Nex

This is the shared contributor contract for Nex Controller and Nex Core. Tool-specific files
reference this file; keep working rules here rather than maintaining competing copies.

## The architecture invariant: Nex Core does the work, Nex Controller only asks

Read this before designing anything. It is the foundation of the product, and it is the rule
implementation work keeps quietly breaking. It is not negotiable by convenience.

- **Nex Core is the product.** Every capability — VMs, storage, networks, media, backup, containers
  — is implemented in the host distribution and exposed through its API. Each host is either its
  pool's **master** or a **member**; which one performs an operation depends on the feature, and the
  master keeps the pool's durable record of it.
- **Nex Controller is an orchestrator and nothing else.** It authenticates operators, renders the
  UI, and calls host APIs. It is a replaceable front end onto the hosts. It does not own, compute,
  or keep pool state; it asks a host and shows what the host reports.
- **Every function happens over the API.** If a feature needs Nex Controller to do the work itself,
  the feature is in the wrong process: implement it in Nex Core and give it an API call.
- **The replacement test — apply it to every design.** Delete the Controller. Install a fresh one
  tomorrow, connect the pool masters, and every pool setting, network, storage registration, VM,
  identity, name, and policy comes back from the hosts while the workloads never stopped. If your
  design would lose anything, or make an operator re-enter anything, the design is wrong. Fix the
  design; do not add a recovery path, export, or migration to compensate.
- **The only Controller-owned data** is Controller-scoped: sites, administrator accounts and roles,
  audit history, enrollment tokens, the Controller CA, and backup/restore jobs, which are backed
  up and restored separately. That list is closed. Adding to it is a product decision for project owner,
  not an implementation choice. Decision: project owner, 2026-09-10 — backup and restore orchestration is
  the one explicit exception to the no-jobs rule below: Nex Controller schedules and drives backup
  and restore jobs and keeps their run history and catalog; backup repository registrations stay
  pool-scoped on the master.

A change breaks the invariant if it:

- adds a Controller table, cache, or file that holds the only copy of anything — a setting, name,
  ID, mapping, host assignment, schedule, or counter;
- makes Nex Controller derive, remember, or reconcile state a host could report instead;
- implements host behavior in Controller code because that is quicker than adding a Core API;
- has Nex Controller schedule, poll, or drive a multi-step operation that a host should run and
  report — any job the Controller must stay alive to finish (backup/restore jobs excepted, above);
- gives a resource an identity, name, or relationship the host does not know, so a re-adopting
  Controller could not reproduce it;
- keeps operator-visible data about pool resources (tags, notes, custom names, layouts of pool
  objects) with no host-side record, on the grounds that it is "not real state".

If a requirement genuinely does not fit the invariant, stop and resolve it with project owner. Do not invent
an exception, and do not leave the loophole for a later phase to clean up. Every change declares
where its durable state lives in its `docs/changes/*.json` record (`state.owner`), and both the API
impact check and phase acceptance gates verify it.

## Core-first delivery and acceptance

Design and review each pool capability in this order: **Core behavior → durable host-side
state → host API → outage/recovery checks → Controller UI**. Nex Core, the Debian host
platform, owns validation, placement decisions, policy enforcement and durable operations;
Controller authenticates operators, submits requests and presents host-reported results.
The closed Controller-scoped responsibilities and backup/restore exception above still apply.

Prove the capability through the host API, including interrupted requests, safe retries and
Controller disconnection/replacement, before counting the slice as complete. Deliver the UI
and guide in the same slice, but never treat a working screen as evidence that Core behavior
or recovery works. Core operations must not depend on an open browser or Controller connection.
Reuse native platform engines; Swarm schedules container tasks, without a second Nex task scheduler.

Future product direction does not authorize implementation scaffolding or reorder delivery.
Keep the current Core safety, authority and recovery gates ahead of dependent features; use the
[platform direction](docs/plans/2026-09-08-nex-platform.md#core-first-product-direction) when
assessing placement policies and application-catalogue proposals.

## Read before working

- Read `docs/plans/2026-09-08-nex-platform.md` for the product model, build order, and acceptance gates.
- Read the relevant feature/UI research and current phase under `docs/plans/`; research is inspiration,
  not authorization to expand the current milestone.
- Inspect the actual code and working-tree changes before editing. Planned files and checks are not
  evidence of implementation. Create additional documentation only when it has something useful to record.
- Keep every project plan under `docs/plans/`. Record scope changes and decisions in the relevant plan.
  Resolve material product changes with project owner; use judgment for routine implementation choices.

## Communication

Keep replies concise by default, using at most these three short fields:

- **Need from you:** `Nothing` or one exact action/decision.
- **Done:** One or two sentences describing the outcome.
- **Attention:** Genuine limitations, risks, or follow-up only; omit when none.

Requested reports, plans, and explanations may be detailed. Intermediate updates are one sentence
when work starts, materially changes, blocks, or needs a progress update. Ask one direct question
when blocked; do not repeatedly ask permission for already authorized work.

Whenever asking the user a question, use the session's native interactive question tool
when available and permitted in the current mode, rather than burying it in prose. This
applies to clarifications, preferences, decisions, and necessary confirmations; respect
tool restrictions and use dedicated approval controls where required. Prefer two or three
clear choices with a recommended option and brief tradeoffs when useful; allow a custom
answer where supported, and use free text when choices would be artificial.

If no suitable interactive tool is available, put the question in a separate, clearly
labeled **Need from you:** message with numbered choices when appropriate. Never hide a
question in a progress update or outcome paragraph. Ask only when the answer matters;
continue independent work while awaiting a reply, and never treat silence or a preselected
option as approval. These instructions cannot enable a question UI the client lacks.

## Agent workflow

- The primary owns intent, decomposition, coordination, user decisions, acceptance, and synthesis.
  It may implement work itself and remains responsible for the final result.
- Agents must never create commits, amend commits, push, tag, rebase, merge, or otherwise change Git
  history. project owner alone handles all Git commits and remote operations; agents leave completed changes
  unstaged unless explicitly told otherwise.
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
- **Apply the [architecture invariant](#the-architecture-invariant-nex-core-does-the-work-nex-controller-only-asks)
  to every pool-scoped setting**: membership, networks, storage, media, VM definitions, VM identity
  and policies persist on the pool master's Nex Core and are re-adopted, with their identities and
  names, when a host (re-)enrolls with a new or rebuilt Controller. The web UI and API act on hosts,
  not on a private Controller model. Agents/libvirt report observed state; show stale/unknown state
  honestly and reconcile ambiguous outcomes before retrying. Decision: project owner, 2026-09-09.
- Running VMs and local recovery access must survive a controller outage. Never interpret an unreachable
  host as proof its VMs stopped. Never permit duplicate writable execution after a timeout or partition.
- Snapshots, independent backups, verified restoration, and compatible shared-storage live migration
  are required phased features. Start shared storage with external NFS; Ceph integration comes later.
- A snapshot is not a backup. Protect complete disk/configuration recovery, backing chains, exclusive
  ownership, and restore integrity. A completed backup job is not proof of a working restore.
- Migration requires compatible CPU/runtime, networking, storage, and firmware, not just shared disks.
  HA requires proven quorum, fencing, and failure recovery before automatic restart is enabled.
- Follow established infrastructure-management conventions for operator workflows and navigation,
  with the simplest working UI first. Never name another product as the source of a design in code,
  documentation, commit messages, or the UI; never copy another product's branding, artwork, or
  wording; and do not present documentation-only research as inspected visual behavior.
- Container Services use Docker Swarm in Nex-managed node VMs, with the full lifecycle in Nex Controller;
  not LXC or Docker directly on Nex Core. Keep this after the VM foundation and preserve the agreed
  colorful application/service icons. Follow Phase 10 for storage ownership, recovery, and quorum rules.

## Public repository privacy

- Treat every tracked file as public, including documentation, plans, change records, QA
  evidence, logs, screenshots, fixtures, configuration, and contributor instructions.
- Never include personal names, usernames, personal email addresses, home/workstation paths,
  private hostnames/domains/IP addresses, account identifiers, real environment/resource IDs,
  credentials, or other confidential information. Refer to people by role (for example,
  “project owner” or “operator”), not by name, including when recording user decisions.
- Use clearly fictional examples and reserved example domains/addresses. Supply deployment
  accounts, endpoints, and credentials through configuration or CI secrets; never hard-code
  an individual's account or private environment as a default.
- Sanitize evidence before adding it. Preserve relationships with consistent anonymous aliases,
  label sanitized historical evidence, and never imply redacted output is an untouched capture
  or a newly executed check. Inspect images visually and review attached artifact metadata too.
- Preserve legally required third-party attribution, public dependency identifiers, and approved
  organizational signing identities. These are not permission to add private project identities.
- Run `python3 -m unittest discover -s tests -p 'test_repository_privacy.py'` after changes.
  Its known-pattern checks supplement manual review; a pass does not prove arbitrary data is safe.
- Removing current-file content does not remove published Git history, releases, or cached copies.
  Report those separately and follow the Git-history ownership rule; never claim they were cleaned
  without actual evidence.

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
- Keep every code change as minimal as possible: the smallest correct diff, in the fewest files,
  reusing what exists before writing anything new. Cut code that is not required for the task.
  Minimal must still read clearly: plain names and straightforward control flow, never
  terseness or cleverness that makes the code harder for a person to follow. Write code the way
  an experienced human developer on this project would: match the surrounding style, comment
  only where the reason is not obvious, and avoid generated-looking boilerplate, restated
  comments, or defensive code for cases that cannot happen. No speculative
  abstractions, plugin frameworks, empty scaffolding, or parallel sources of truth. Never simplify away
  validation, accessibility, security, or data safety.
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
  inventory/adoption path a fresh Controller uses to recover it, in the change record's
  `state.record` and in the plan. A setting that exists only in the Controller database is a broken
  invariant, not a documentation gap: it leaves the slice open and needs a design fix.
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
  Every record also declares `state.owner` as `none`, `core`, or `controller` with a reason;
  anything but `none` names the durable record and how a fresh Controller recovers it. Reviewers
  reject a `controller` owner that is not one of the closed Controller-scoped exceptions.
  Run `python3 scripts/check_docs.py --worktree` before committing: it applies the same gate CI
  applies, to uncommitted and untracked work, so a missing assessment is caught locally instead of
  as a red build. Then run the relevant behavior checks and follow the documented CI procedure.
- Independent review and every phase gate check guide/API applicability, documentation accuracy,
  and actual walkthrough evidence. Automated structural checks cannot judge semantic completeness
  or prove that a walkthrough ran. No applicable documentation work is deferred to project end.

## Verification and bug memory

- Nontrivial logic ships with a focused runnable check. Use the repository's existing test tools when
  available. Test observable behavior and failure recovery; do not freeze known-bad prototype output.
- For bugs, reproduce the failure and add a failing regression check before fixing the root cause where
  practical. If the check cannot reproduce it, record the limitation and the alternative evidence.
- Every bug fix must include a regression test and ensure forge CI runs it on pushes and pull
  requests. The forge executes `.github/workflows/`; keep the `.gitea/workflows/` copies in step
  with it. Hosted github.com minutes are paid for and the forge runs the full suite for free, so
  every job in `.github/workflows/` is skipped on github.com except the single cheap job in
  `.github/workflows/github.yml` (Go tests without `-race`, `generate_api.py --check`, the `tests/`
  suite and the documentation gate, on main pushes and pull requests). Decision: project owner,
  2026-09-21. Reuse an existing test suite when a workflow already runs it; otherwise update the
  workflow in the same change. The test must detect the original bug, pass with the fix, and fail CI
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
