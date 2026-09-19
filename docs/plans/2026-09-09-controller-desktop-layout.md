# Nex Controller desktop layout

Date: 2026-09-09
Status: Controller frontend slice implemented and verified locally with independent review.

## Authorized change

project owner redirected the next work from installer walkthroughs and Phase 2 expansion to
overhauling the Nex Controller web GUI. The requested visual target is a Windows
desktop management application: a compact menu row and icon toolbar, a left
resource tree, a grey selected-resource header above horizontal detail tabs, and
dense property content, including rebuilding the existing shell where needed.
This supersedes the earlier browser-shell direction in the
[feature and interface review](2026-09-08-feature-and-ux-review.md).

Retain Nex identity and the Sites → Pools → Hosts → VMs product hierarchy. At the
time of this frontend slice, the implemented inventory ended at hosts and VM controls
were Phase 2 work.
This changes presentation and navigation of available Controller features, not
phase order, backend capability, or completion of any outstanding platform gate.
The Nex Core host-local UI and installer are outside this redesign.

**Note, 2026-09-09:** the site/pool creation actions mentioned below were later reduced
to site creation by the [pool master model correction](2026-09-09-pool-master-model.md);
**Create pool** no longer exists and each enrolling host becomes the master of its
automatically created pool. The checklists remain as recorded for this slice.

**Superseded scope:** Phase 2 subsequently delivered VM creation, console, power and
editing controls. Their behavior and evidence belong to their own plans; this layout
slice's recorded browser evidence does not verify those later workflows.

## Layout and behavior

- Use a compact Windows-like menu row and action toolbar, light grey surfaces,
  thin borders, small readable system typography, and blue selection accents.
- Keep a persistent left infrastructure tree with site, pool and host nesting,
  recognizable resource icons, and a clear selected item.
- Place the selected resource name and status above a horizontal detail-tab strip.
  Use dense property rows and tables for inventory, health, networking and disks.
- Keep recent operations/activity and connection information in a bottom area;
  distinguish recorded outcomes from current observations and stale reachability.
- Preserve working site/pool creation, enrollment, inventory refresh, revocation,
  re-enrollment, authentication, tasks, audit history and source access. Menus and
  tabs must lead to available actions or information; do not populate the shell
  with unsupported VM, storage, migration or HA controls.
- Keep keyboard access, visible focus, non-color status labels, overflow handling
  and usable narrow-screen navigation. Compact desktop styling must not hide
  target confirmation, enrollment-token handling, errors or recovery instructions.

Use existing React, CSS and native browser controls. Reuse the present API helper,
session handling, scoped inventory and operation records; a visual rewrite does
not require a new UI framework or a second state/API layer. Retain Nex assets;
never copy another product's branding, artwork or image assets into Nex.

## API and guide impact

API impact: **existing contract** — browser session/login/logout, site and pool
creation, inventory reads, enrollment/re-enrollment, host revocation, refresh
operations and audit reads retain their implemented client-facing contracts.
The flow remains authenticated UI → Controller membership/operation or authorized
read → existing Core observation/refresh-command/result handling → inventory,
durable operation and audit records displayed by the UI. No Core command,
authentication policy, request schema, retry behavior or version contract changes
are planned. The [API reference](../API.md) remains authoritative.

Guide impact: **updated** — revise the
[Controller guide](../guide/manage-controller.md) for actual navigation and labels in
the delivered interface, with a scoped change assessment and browser walkthrough
evidence. Record API documentation impact as none if implementation confirms
that existing contracts and semantics remain unchanged.

## Acceptance

- Check actual Nex desktop captures against the layout described above: the
  menu/toolbar, tree, detail tabs, dense content and bottom area.
- Exercise existing Controller workflows against disposable state: sign in,
  create site/pool, enroll/select a host, inspect observations, refresh inventory,
  inspect task/activity outcomes and confirm scoped revocation/re-enrollment.
- Check loading, empty, invalid-object, error, unauthorized/session-expiry, stale
  and confirmation states, keyboard operation and a narrow viewport. Keep failed
  operation details and safe retry behavior intact; label any simulation.
- Run the existing UI typecheck/build and applicable focused browser/API checks,
  inspect captures, walk the revised guide and run the documentation checker.
- Obtain independent review of behavior, scope, API/guide applicability and the
  actual evidence. Record remaining limits honestly; a build or visual mockup
  alone does not pass this slice or the next platform phase.

## Delivered evidence

The shell now supplies compact menus/toolbars, a searchable collapsible resource
tree, General/Networking/Storage/Activity views, dense properties and a task drawer.
Existing forms, authentication, confirmations and stale/error recovery remain.
The [executed walkthrough](../qa/controller-desktop-layout/checks.md) records the
passing UI build/typecheck, all 16 browser/process result groups, inspected captures,
mobile menu correction/regression and independent review. API impact remains
**existing contract**; guide impact is **updated** in the Controller guide.

This accepts the bounded frontend slice only. No backend/VM phase, installed image,
real hardware or hosted CI gate is closed by these UI checks.

## ISO delivery rebuild — 2026-09-09

project owner requested rebuilding the ISO to include this Controller layout. Rebuild a
matching signed appliance and local-host package before assembling the ISO; keep
existing development trust and source snapshots. API impact: **none** for artifact
assembly, which delivers the existing contracts. Guide impact: **updated** for
artifact availability and verification evidence. Record actual results and limits
in the [rebuild report](../qa/controller-desktop-rebuild/checks.md). This is not an
upgrade of already deployed Controller VMs or acceptance of full ISO installation.

## Site selection at scale — 2026-09-09

project owner requested a searchable site selector directly below **Resources**, replacing
the all-sites tree that becomes unwieldy at 100–500 sites. Show only the selected
site's **Pools → Hosts → VMs** hierarchy. Retain accessible keyboard navigation,
separate resource search within the selected site, deep links that select the
correct site, and clear empty/no-match states. Site creation and global task
history remain reachable. Use existing controls and dependencies.

After viewing the local update, project owner refined the design: show one closed site
dropdown, with its search field inside the opened menu rather than a second box
above it. Keep a bounded scrolling result list and keyboard/focus/escape behavior;
reverify and update the selected local Controller with the final interaction.

API impact: **existing contract** — authenticated site/pool/host/VM lists already
contain the parent IDs needed for selection and filtering; creation requests and
Controller/Core command/results remain unchanged. This is a navigation change,
not a permissions boundary or backend pagination claim. Guide impact: **updated**
for selection, search and pool-root navigation.

Acceptance requires typecheck/build, focused browser checks including a labeled
500-site fixture, deep-link and keyboard/narrow behavior, independent review, and
verification in the selected disposable local Controller alongside host ISO
visibility. No global resource hierarchy or API schema change is required.

The requested host ISO view and final single-dropdown site selector are now
implemented and [verified in the selected local Controller](../qa/host-isos/checks.md).
Final browser checks cover transfer/status isolation, the searchable 500-site
fixture, keyboard focus and eight selector groups; all 16 existing Controller
walkthrough groups also passed. The local UI/source update preserves the backend
binary and is not a rebuilt installation/release image.

## Fixed command toolbar — 2026-09-10

project owner asked for the menu/toolbar band to work like a desktop management console's:
a fixed toolbar whose buttons stay in place and grey out when they do not apply,
with VM power control directly in the bar. Delivered layout, left to right:
**Back**/**Forward** (browser history), **Create site**/**Add host**/**New VM**,
**Start**/**Shut down**/**Reboot**/**Force stop** for the selected VM, and
**Refresh view** at the right end. Greyed buttons carry their reason as a tooltip; the
VM page repeats the observed state and blocker under its name. Power confirmations use a
native modal dialog so they work from the toolbar; a rejected request keeps the dialog
open with its error. **New VM** from a pool, host or VM opens the pool's wizard. Host
actions moved out of the toolbar into a new **Host** menu (**Refresh inventory**,
**Re-enroll host**, **Revoke access**) so the bar stays static; **File** keeps the three
creation actions. Not added: a VM menu duplicating the power group, storage/network
creation buttons, and icon-only narrow layouts (the bar wraps at phone width).

API impact: **existing contract** — the same site, enrollment, refresh-inventory, revoke
and VM power requests, identities and results. Guide impact: **updated**. Evidence:
[toolbar QA report](../qa/controller-toolbar/checks.md).
