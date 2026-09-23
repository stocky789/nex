# Put a pool host into maintenance

## Status

Development product 0.4.8. Maintenance mode is a drain flag the **pool master** records for one
host in the pool: while it is set, the master places no new virtual machine on that host and
refuses starting one there. Set it from Nex Controller: **Settings** → **Members** (with the
pool chosen in the top-bar scope menu) → **Actions** on the host's row → **Enter maintenance**.
The **Maintenance** quick action in a host's panel opens the same section.

What it does not do, in this release and by design:

- **It stops nothing.** Guests already running on the host keep running. Stopping, shutting down
  and force-stopping a guest are never blocked by maintenance — an operator draining a host is
  usually about to power guests off.
- **It does not move guests.** There is no live migration and no automatic evacuation. Move or
  stop the guests yourself.
- A placement the master already accepted before the flag was set finishes. Maintenance refuses
  *new* work.
- It is not a fence, and it does not protect against a host being used by something other than the
  pool master. It **is**, in a pool with **Host fencing** on, the supported way to make disruptive
  work safe: Nex Core puts this host's watchdog down *before* the host leaves the pool's cluster,
  which restarts that host's cluster services — one host at a time, a minute or two — and in a
  three-host pool it disarms the whole pool until the host returns. See
  [Host fencing, and the watchdog line](pool-authority-and-recovery.md#host-fencing-and-the-watchdog-line).
- There is no scheduled or automatic maintenance, and no maintenance window.

The flag is durable state of the **pool master**, not of Controller: it survives a Controller
restart, a Controller rebuild and the drained host being unreachable, and a replacement Controller
reads it back from the master with nothing to re-enter.

Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- An administrator session in Nex Controller, and the pool's **Settings** → **Members** section
  open.
- The pool master **connected and fresh**, and advertising the `pool-maintenance-v1` capability.
  If it does not, the row's **Details** → **Maintenance** entry shows *Maintenance mode needs
  pool-maintenance-v1 on the pool master;
  upgrade the pool master* and no control is offered — the refusal that makes the flag mean
  anything lives in the master, so Controller will not pretend to set it.
- The host must be a member of that pool as the master reports it, and not ejected.
- The host being drained does **not** need to be reachable. Entering and leaving maintenance never
  contacts it.
- A plan for the guests that are still on the host. Read the guest list in the confirmation dialog
  first.

A single-host pool's host is its own pool master. Draining it is allowed and works: the master's
record then refuses placements and starts on itself.

## Steps

1. Choose the pool in the top-bar scope menu, then select **Settings** in the rail and
   **Members** in the settings navigation (`#/settings/members`). From a host's panel
   (**Hosts** in the rail → the host's row), the **Maintenance** quick action opens the
   **Members** section of that host's pool directly.
2. Find the host's row and read its **Maintenance** check in the status strip under the host
   name. It shows **Available**, **In maintenance**, or **Unknown**. *Unknown* means the pool
   master is not fresh, so nothing can be said about the flag; it never means *Available*. Select
   **Details** on the row for the guests still on the host and, while in maintenance, since when.
3. Open **Actions** on the host's row and select **Enter maintenance**. A dialog opens with the
   host's name, what maintenance does and does not do, and the guests still on the host — for example *2 guests · 1 running · build, db*.
   If the host is not reporting, the dialog says *Guests unknown: this host is not reporting.*
   Treat that as unknown, not as an empty host.
4. Move or stop any guest you do not want running during the work. Do that before confirming;
   maintenance will not do it for you.
5. Select **Enter maintenance** in the dialog to confirm. There is no typed confirmation: nothing
   is stopped or destroyed by this change, so there is nothing to confirm against.
6. To release the host later, open **Actions** on the same row, select **Exit maintenance** and
   confirm.

## Expected result

- The row shows **in maintenance** with *Since <time>*, read back from what the pool master
  reports. Controller records no flag of its own. The task succeeds once the master has recorded
  it, including when the host being drained is down: the master's copy is the one every refusal is
  made from, and a host that was unreachable picks the change up when it returns.
- The pool master refuses a new virtual machine placed on that host, and refuses starting one
  there, with *the selected VM host is in maintenance mode; take it out of maintenance before
  placing or starting VMs on it*. Autostart-by-placement skips the host and records why.
- **With Host fencing on for the pool**, the **Host fencing** line reports the teardown while it
  runs and can say it is *blocked by* a host that is missing. Wait until the pool reports that no
  host is armed before you start work that interrupts a host's network or power.
- In the VM creation screen, the host is disabled as a placement target until the open form has a
  fresh authoritative pool-members report. A reported maintenance state, missing state, stale
  master report or request error shows its reason beside the host; this conservative UI check
  applies only to VM placement, and Nex Core still makes the final placement decision.
- Guests that were running are still running, and can still be stopped, shut down and force
  stopped.
- After **Exit maintenance**, the row returns to **available**. The host restores each managed
  guest's requested autostart setting, and the next host reboot brings those guests back —
  including ones on the host's own local storage. On a member, the next start can still be refused
  until the updated signed authority copy arrives; wait one master probe interval, then retry
  unchanged.

### What is refused and what is allowed while a host is drained

The host refuses starts itself, without asking the master, so this holds for a guest on **local**
storage and while the master is unreachable:

| You do this | While drained |
|---|---|
| Start a guest — local or shared storage | **refused** |
| Resume (wake) a suspended guest | **refused** |
| Create a guest with *start after create* | **refused** |
| Place or edit a guest onto the host | **refused** |
| Reboot or force-reboot a guest already running there | allowed — it is already running on the host, so this is not new work |
| Stop, shut down or force-stop | allowed, always |
| Change a guest's CD/DVD, with no definition change | allowed |
| Host reboot: guests that would normally start themselves | **blocked** — the host suppresses libvirt's autostart flag while drained |
| Manual `virsh start` of a managed guest on any pool host | **refused** by Nex Core's libvirt hook |

On a member the drain flag is read from the signed copy of the pool's records that the master
pushes to it, so it can lag: at most one master probe interval while the master is reachable, and
as old as the last successful push while it is not.

## Troubleshooting

- **The Maintenance check shows *Unknown* and the menu item is disabled.** The pool master is not fresh.
  Reconnect the master; nothing about the flag can be read or changed while it is quiet.
- **No menu item, with an upgrade message under Details.** The pool master does not advertise
  `pool-maintenance-v1`. Upgrade the master.
- **"The pool master reports membership generation *n*; reload the pool and retry."** The
  membership changed after the page was loaded. Select **Refresh** in the **Members** section and
  try again.
- **"This host is not a member of the selected pool."** The master's reported membership does not
  contain the host, or reports it as ejected.
- **A red message on the row after submitting, ending in "The pool changed since you loaded it —
  reload."** The pool master refused the change because its membership moved while the request was
  in flight, or because its membership was unavailable. This is a conflict, not a failure of the
  host: select **Refresh** in the **Members** section and decide again. It arrives on the row rather than as an error from the
  button, because the command is durable and the master answers it later.
- **"Needs attention: maintenance mode could not be recorded durably."** The master could not
  write the flag to its own state, and it may or may not have landed. Read the **Maintenance**
  check after the master's next report — that is the truth — and submit the same change again if
  needed; repeating it is safe.
- **The change seems to do nothing.** Maintenance never affects a running guest. Confirm what you
  expected against the table above: it blocks new placements, definitions, starts and resumes, and
  it blocks guests starting themselves after a host reboot. It does not block reboot or force-reboot
  of a guest that is already running there, and it never blocks stopping anything.
- **Other hosts restarted their cluster services, or the pool says it is disarmed.** Expected in a
  pool with **Host fencing** on. Putting this host's watchdog down happens before it leaves the
  pool's cluster and restarts cluster services one host at a time, and a three-host pool that loses
  one host has no majority side, so the whole pool disarms deliberately and arms again when the
  host returns. Nothing running on any host is affected. See
  [Host fencing, and the watchdog line](pool-authority-and-recovery.md#host-fencing-and-the-watchdog-line).
- **Controller was restarted or replaced.** The flag is unaffected. A new Controller shows it
  again as soon as the master reports.
- **Recovering without Controller.** The flag lives on the master. Sign in to the master's own
  local HTTPS service and read `GET /api/local/v1/pool` to see its membership record.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

- **G2 browser walkthrough.** On a disposable host, the rendered UI entered maintenance while a
  fresh VM on that same host remained running. After UI stop, UI Start reached Core's terminal
  maintenance refusal, and the VM-create option for that host was disabled with its displayed
  reason. After **Exit maintenance**, normal start and a final shutoff succeeded. Desktop and
  360 px captures were inspected. This proves the stated browser/API behavior for that host; it
  does not establish reboot or autostart behavior, every maintenance recovery or physical-hardware
  case.
- Covered by Go `-race` unit tests on Nex Core: flag persistence and reload from the master's own
  state, refusal of placement, `define`, `start` and autostart on a drained host, refusal of
  starting a **local-storage** guest and of waking a suspended one, refusal of a member's start
  through the peer handler, a member reading its drain flag from the signed authority replica,
  refusal of an ejected host, the autostart pass returning early, the libvirt flag being suppressed
  without rewriting the definition, the `StorageGuard` hook refusing a managed guest on a drained
  host, `stop` still permitted, idempotent enable and disable, a drained single-host pool, and
  membership validation unaffected by the flag. Controller tests cover the placement refusal and the
  full `400`/`404`/`409` matrix, and the UI is covered by server-rendered component tests.
- **Not covered by any test:** the agent-side wiring that applies the suppressed libvirt flag needs
  a real libvirt, so only the flag mapping and the hook refusal are tested, not the two together on
  a host.
- Automated tests are not a walkthrough: layout, keyboard operation, the 360 px view, the dialog's
  focus behaviour and the real conflict and needs-attention states on screen remain unverified, as
  does any behaviour on real hardware.
- Offline contract checks in `tests/test_api_contract.py` hold the route, its refusals, the
  command and the projection to the implementation; see the
  [API reference](../API.md#host-maintenance-mode-post-apiv1poolsidmembershost_idmaintenance).

[Back to the user guide](README.md)
