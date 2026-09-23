# Working rules for Nex

This is the shared contributor contract for Nex Controller and Nex Core. Keep working rules here rather than maintaining competing copies.

## Architecture invariant: Core does the work, Controller only asks

This is the foundation of the product and is not negotiable for implementation convenience.

- **Nex Core is the product.** VM, storage, network, media, backup, container, policy, and pool behavior lives on the host platform and is exposed through host APIs.
- **Nex Controller is an orchestrator and UI.** It authenticates operators, renders the UI, and calls host APIs. It must not become the authoritative owner of pool/resource state.
- **Every host capability must be possible through the API.** If Controller has to implement host behavior itself, the behavior belongs in Core.
- **Replacement test:** delete Controller, install a fresh one, reconnect pool masters, and all pool/resource identities, settings, relationships, and policies must be recoverable from the hosts without interrupting workloads.
- **Controller-owned durable data is limited to:** sites, administrator accounts/roles, audit history, enrollment tokens, Controller CA, and backup/restore job orchestration/history/catalog. Backup repository registrations remain pool-scoped on the master.
- Pool-scoped state belongs on the pool master. Agents/libvirt report observed state; Controller presents it and handles stale/unknown state honestly.
- Running VMs and local recovery must survive Controller loss. Never interpret an unreachable host as proof a VM stopped, and never permit duplicate writable execution after timeout/partition.

A change violates the invariant if Controller becomes the only place that:
- stores a pool/resource setting, identity, name, mapping, relationship, assignment, schedule, policy, or counter;
- derives or reconciles state a host can report;
- performs host behavior because adding a Core API is less convenient;
- drives a multi-step host operation that should survive Controller disconnection;
- stores operator-visible pool metadata that a fresh Controller could not reconstruct.

If a requirement genuinely conflicts with this invariant, stop and raise it with the project owner instead of inventing an exception.

## Delivery order

For pool capabilities, implement and prove work in this order:

**Core behavior → durable host state → host API → failure/recovery behavior → Controller UI**

- Core owns validation, placement decisions, policy enforcement, durable host operations, and pool state.
- Prove retry, interruption, reconnect, and Controller-replacement behavior through the host API before treating the slice as complete.
- UI and guide updates ship with the feature, but a working screen is not proof that Core behavior or recovery is correct.
- Reuse native platform engines. Do not create a second scheduler or equivalent subsystem when the platform already provides the required capability.
- Future roadmap items do not justify current scaffolding.

## Before working

- Read `docs/plans/2026-09-08-nex-platform.md` and the relevant current feature/phase plan.
- Inspect the actual code and current working tree before editing. Plans are not evidence of implementation.
- Keep plans under `docs/plans/`.
- Record material scope/product decisions in the relevant plan.
- Resolve product-level changes with the project owner; use judgment for routine implementation choices.

## Communication

Keep normal replies concise:

- **Need from you:** `Nothing` or one exact action/decision.
- **Done:** one or two sentences describing the outcome.
- **Attention:** only genuine limitations, risks, or follow-up.

Use detailed output when the task itself requires a report, plan, or explanation.

Ask only when an answer materially affects the work. Use the session's interactive question tool when available. Do not repeatedly request approval for already-authorized work.

## Agent workflow

The primary agent owns **intent, architecture, design, decomposition, integration, user decisions, acceptance, simplification, and synthesis**. It remains responsible for the final shape of the codebase.

### Delegation

- Use subagents when parallelism genuinely helps: research, exploration, review, test triage, isolated investigations, and genuinely independent implementation areas.
- Prefer **one implementation owner for tightly coupled feature work**. Do not split one coherent implementation across several writers merely because it can be decomposed.
- Subagents do not own architecture. They extend the design selected by the primary agent.
- Before delegating implementation, the primary should identify the intended extension points, relevant existing code, expected scope, and architectural constraints.
- Give workers only the context required for their task, with an objective, scope, acceptance criteria, required checks, and either a read-only lane or exclusive write set.
- One writer owns a file at a time. Sequence dependent edits and preserve unrelated dirty work.
- Agents never commit, amend, push, tag, rebase, merge, or otherwise change Git history unless explicitly authorized by the project owner.

### Subagent implementation discipline

Subagent output is provisional until the primary integrates and simplifies it.

Subagents must:
- reuse existing functions, types, services, handlers, components, data flows, and platform capabilities before creating new ones;
- extend the existing architecture rather than building a parallel one;
- prefer modifying an existing implementation over creating a sibling implementation;
- touch the minimum practical number of files;
- avoid broad cleanup or redesign outside the assigned task;
- avoid duplicating functionality just to keep their task self-contained;
- return architectural concerns to the primary instead of inventing a new pattern.

Do **not** introduce an interface, wrapper, adapter, factory, generic helper, framework, abstraction layer, or other indirection for a single caller or speculative future use.

Do not make new code "reusable" unless multiple concrete current callers need it. **Reuse existing code aggressively; do not build generic infrastructure merely in the name of reuse.**

A small amount of obvious local duplication can be preferable to a new abstraction whose only purpose is eliminating incidental duplication.

### Integration and anti-bloat review

After subagent work is integrated, review the result as **one implementation**, not as a collection of individually acceptable patches.

Check for:
- duplicate helpers, validation, state, transformations, and abstractions;
- parallel implementations of the same responsibility;
- unnecessary files, types, DTOs, wrappers, adapters, or interfaces;
- defensive checks already enforced elsewhere;
- speculative extensibility;
- integration scaffolding that can now be removed;
- code that can be replaced by an existing canonical path.

Unexpected code growth is a review signal, not a line-count quota. If a modest feature adds several times more code than comparable existing behavior, investigate why before accepting it.

For substantial multi-agent work, have a read-only reviewer inspect specifically for unnecessary complexity and duplication. Correctness alone is not acceptance.

Workers hand off:
- outcome;
- changed paths;
- actual checks/results;
- unresolved decisions;
- net line change where useful.

## Product boundaries

- Nex Controller: centralized management API/service and React/TypeScript web UI.
- Nex Core: Debian-based host distribution using KVM/QEMU, libvirt, and a local Go agent.
- Do not implement a new hypervisor.
- Use Go for Controller/agent, React + TypeScript for UI, Debian packages/systemd/installer automation for delivery.
- Preserve the Debian base and prefer existing platform capabilities.

Hierarchy:

**Sites → Pools → Hosts → VMs**

- Each enrolling host initially becomes the master of an automatically created pool named after its hostname.
- Enrollment tokens bind to a site.
- Pool membership/master-change/ejection follow their planned phase; do not introduce a duplicate Cluster tier.
- VMs belong to pools and have separate host assignments. Preserve VM identity across migration.
- Storage registrations, backup repositories/policies, and logical networks belong to pools.
- Pool-level storage registration does not imply every host has access to that storage.
- Membership, networks, storage, media, VM definitions/identity, and policies persist on the pool master's Core and must be recoverable by a rebuilt Controller.
- Snapshots are not backups.
- Backup design must protect complete recovery, backing chains, exclusive ownership, and restore integrity.
- Migration requires compatible CPU/runtime, networking, storage, and firmware.
- HA requires proven quorum, fencing, and recovery before automatic restart.
- Start shared storage with external NFS; Ceph integration comes later.
- Container Services use Docker Swarm inside Nex-managed node VMs, not LXC or Docker directly on Nex Core.

Follow established infrastructure-management conventions, but do not copy another product's branding, artwork, wording, or claim documentation research as observed behavior.

## Public repository privacy

Treat every tracked file as public.

Do not include:
- personal names/usernames/emails;
- private hostnames/domains/IPs;
- workstation/home paths;
- account identifiers;
- real environment/resource IDs;
- credentials or secrets;
- other confidential data.

Use roles such as `project owner` and fictional/reserved examples.

Sanitize evidence before tracking it, including image content and metadata. Preserve legally required third-party attribution and approved organizational signing identities.

Run:

```bash
python3 -m unittest discover -s tests -p 'test_repository_privacy.py'
```

A passing automated check does not prove all sensitive data is absent.

Removing a secret from the current tree does not remove it from Git history, releases, or caches. Report history exposure separately and do not rewrite history without authorization.

## Licensing and redistribution

Read `docs/LICENSING.md` before changing dependencies, packaging, or release delivery.

- Original Nex code/build scripts/configuration use AGPL-3.0-only.
- Debian and third-party components keep their own licenses/notices.
- The ISO as a whole is not relicensed as AGPL.
- Review exact licenses before copying code, linking libraries, or adding plugins.
- Do not combine GPL-2.0-only code into AGPL Nex without appropriate permission.
- Before external ISO distribution, verify the component manifest, matching source availability, notices, source-delivery path, base ISO/installer components, firmware, and artwork redistribution terms.
- License compatibility is not release certification.

## Implementation discipline

- Finish the current phase's end-to-end acceptance gate before expanding dependent work.
- Trace affected flows and callers before choosing a fix.
- Reuse existing code, standard library, native platform features, and installed dependencies before adding code or dependencies.
- Prefer the **smallest maintainable implementation left behind**, not merely the smallest textual diff.
- Delete or simplify before layering guards, flags, wrappers, fallbacks, or special cases over a root problem.
- Remove code made dead or redundant by the change after confirming callers.
- Do not retain compatibility paths, flags, helpers, or scaffolding "just in case"; Git history is the archive.
- Match surrounding style. Prefer plain names and straightforward control flow.
- Comment reasons that are not obvious; do not generate explanatory boilerplate that restates the code.
- Avoid defensive code for states that cannot occur.
- Do not create speculative abstractions, plugin frameworks, empty scaffolding, or parallel sources of truth.
- Do not simplify away validation, accessibility, security, recovery, or data safety.

A bug fix that only layers more code over the existing path should trigger another look at the root cause.

Report unrelated dead code rather than widening the current task.

## Security and destructive operations

- Authenticate and authorize control operations and consoles at the API boundary.
- Validate host identity.
- Scope credentials narrowly.
- Keep secrets out of logs, source, images, and diagnostic bundles.
- Disk erase, overwrite restore, VM/disk deletion, and management-network changes require explicit target selection and suitable confirmation/recovery behavior.
- A coding/planning task does not authorize installation onto real disks, live infrastructure changes, reboots, or destructive production testing.
- Use explicitly selected disposable lab resources; obtain authorization before touching live infrastructure.

## API impact gate

Every change must assess API impact between Controller/Core and, where relevant, the client-facing API.

Record:

`API impact: none | existing contract | changed contract | new contract`

with a brief reason and affected operations/contracts.

- Reuse existing contracts when they already cover the behavior.
- When a contract changes, implement both sides in the same slice and define auth, authorization, validation, errors, async result behavior, safe retry behavior, outage/reconnect behavior, and version compatibility as applicable.
- New pool-scoped settings must name their durable host-side record and how a fresh Controller re-adopts them.
- Maintain `docs/API.md` and applicable machine-readable/generated contracts alongside behavior changes.
- API-affecting work requires focused runnable checks for changed behavior and relevant failure/authorization/retry paths.
- Missing required API implementation or evidence leaves the slice open.

## Documentation gate

Apply `docs/DOCUMENTATION.md` to every change.

Record guide impact as:

`Guide impact: updated | none`

with a reason.

- Update `docs/guide/` in the same change when operator-visible behavior changes.
- Do not invent user-guide work for internal refactors.
- Keep docs aligned with actual implementation and remove obsolete instructions.
- Walk through applicable documented workflows against the implementation in a disposable environment.
- Record environment, method, actual result, evidence, and untested limits.
- Never claim an unexecuted workflow was verified.
- Simulation must be labeled as simulation.
- Keep portable Markdown, stable filenames, relative links, and contributor details out of operator-facing workflows.

Add a scoped `docs/changes/*.json` record covering changed paths and:
- API impact;
- guide impact;
- `state.owner: none | core | controller`;
- durable record/adoption path when state exists;
- verification evidence where applicable.

Controller-owned state is accepted only for the explicit Controller-scoped exceptions defined above.

Before commit, run:

```bash
python3 scripts/check_docs.py --worktree
```

Then run checks appropriate to the change.

## Verification and bug memory

- Nontrivial logic needs a focused runnable check using the repository's existing test tools where possible.
- Test observable behavior and relevant failure/recovery paths.
- For bugs, reproduce the failure and add a failing regression check before the fix where practical.
- Every bug fix requires a regression test and corresponding forge CI coverage. If prerequisites prevent this, record the limitation instead of pretending coverage exists.
- Record bug ID/date, symptom, root cause, fix, regression evidence, and failed approach in `docs/BUG_LEDGER.md`.
- Keep `.github/workflows/` and `.gitea/workflows/` copies in sync.
- GitHub-hosted CI remains intentionally minimal; the forge runs the full suite. Preserve the existing project decision documented in the workflows/plan.
- Run checks appropriate to the change: backend tests, UI typecheck/build, browser behavior, or disposable-host integration.
- UI work requires real browser review of relevant loading, empty, error, stale-data, keyboard, and destructive-action states.
- Green CI is not proof by itself: inspect actual executed jobs/results and never claim skipped or unrun coverage.
- After an authorized push, inspect the actual CI run and report required failures, skips, missing jobs, or pending results honestly.
- A phase is complete only when functionality, applicable safety/recovery checks, documentation, API requirements, and UI review pass.
- Update documentation in the same change as behavior.
