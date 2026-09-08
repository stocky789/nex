# Nex Controller desktop layout

Date: 2026-09-09
Status: Controller frontend slice implemented and verified locally with independent review.

## Authorized change

Matt redirected the next work from installer walkthroughs and Phase 2 expansion to
overhauling the Nex Controller web GUI. The requested visual target is the Windows
XenCenter application: make the layout and density closely match its desktop
management interface, including rebuilding the existing shell where needed.
This supersedes the earlier vCenter/XO 6 shell direction in the
[feature and interface review](2026-09-08-feature-and-ux-review.md).

Retain Nex identity and the Sites → Pools → Hosts → VMs product hierarchy. The
current implemented inventory ends at hosts; VM controls arrive with Phase 2.
This changes presentation and navigation of available Controller features, not
phase order, backend capability, or completion of any outstanding platform gate.
The Nex Core host-local UI and installer are outside this redesign.

## Screenshot references

The implementation workstream inspected downloaded Windows XenCenter screenshots
from [XenServer's host/VM documentation](https://docs.xenserver.com/en-us/xenserver/8/media/singlehost-singlevm-tools-not-installed-anno.png)
and [Oracle's XenCenter example](https://docs.oracle.com/cd/E59954_01/E59951/html/images/citrix_xencenter_mainwindow.png).
Both show a compact menu and icon toolbar, a left resource tree, a grey selected
resource header and horizontal tabs, and dense property content. These are the
visual references for the Nex redesign; no authenticated XenCenter environment
was operated and no vendor image assets are copied into the product.

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
  with unsupported XenCenter VM, storage, migration or HA controls.
- Keep keyboard access, visible focus, non-color status labels, overflow handling
  and usable narrow-screen navigation. Compact desktop styling must not hide
  target confirmation, enrollment-token handling, errors or recovery instructions.

Use existing React, CSS and native browser controls. Reuse the present API helper,
session handling, scoped inventory and operation records; a visual rewrite does
not require a new UI framework or a second state/API layer. XenCenter is a visual
reference; retain Nex assets instead of copying vendor branding or artwork.

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

- Compare inspected Windows XenCenter screenshots with actual Nex desktop
  captures for the menu/toolbar, tree, detail tabs, dense content and bottom area.
  Cite the reference sources and distinguish screenshot research from operating
  XenCenter itself.
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
