# Update a pool

## Status

Development build, **unverified**. A pool of image-edition Nex Core hosts is updated by the pool master's Core as one durable
rollout: it stages the release on every host, swaps the Controller appliance first when the Controller runs on a pool host, then
updates one host at a time, members first and the master last. **Settings** → **Updates** → **Pool rollout** shows what the master
and the hosts report and sends your pause, resume, abort and roll-back requests. The rollout belongs to the master, so it carries on
if the Controller is stopped or replaced. No part of this page has been walked through on a running pool: see
[Verification](#verification). The rollout never forces a guest off: guests of a host wait, or, if you choose, are shut down
gracefully, and live migration does not exist yet. The one forced power-off belongs to the Controller swap and is not a guest of
the rollout: a new Controller image that never became ready is forced off while the swap rolls back, and the previous Controller
image is never forced off (see [Deploy the Controller appliance](deploy-controller-appliance.md)).

## Prerequisites

- Every host of the pool runs the image edition, is freshly connected and advertises pool rollouts; otherwise the panel says to
  upgrade Nex Core on the host that lacks them. A host that has not found the release (use **Check** under **Host images** first)
  blocks the preflight.
- A Controller administrator session. The pool master must be freshly connected.
- High availability is off. A rollout refuses while it is on.
- Decide what happens to running guests. **Refuse** (the default) waits, up to six hours per host, for the guests on that host to be
  stopped by you, then pauses and names them. **Shut down gracefully** stops each guest through its guest agent, then starts the
  same guests again on the same host afterwards: that is real guest downtime.
- If the Controller VM runs on a pool host in the two-disk profile and the release carries a Controller image, the Controller is
  restarted during the rollout; keep that in mind for your own session.

## Steps

1. Open **Settings** → **Updates** with the pool selected and find **Pool rollout**.
2. Enter the release sequence (it is prefilled when every host reports the same newest release) and choose how running guests are
   handled.
3. Select **Run preflight**. A passing preflight records nothing and says the pool is ready. A refusal shows a stable code and one
   message, for example `quorum_margin`, `running_vms`, `bridge_required`, `controller_too_old` or `host_busy`; fix it and run it again.
4. Select **Start rollout**. The table shows **Controller first** and one row per host in the order they will be updated.
5. Watch the rows. While a rollout runs you can **Pause** (it stops between hosts), and while it is paused you can **Resume**,
   **Abort** or **Roll back host** for a host that has activated the release. Abort puts maintenance mode back and starts again the
   guests the rollout stopped, on every host it had started on, and only becomes **Aborted** once every one of those hosts is out of
   maintenance mode and its guests are running; hosts that already committed the release stay on it. If a host cannot be put back
   yet (it is still rebooting, or a guest will not start), the rollout stays **Paused** with the code `abort_cleanup_pending` and the
   host's row says what is owed: see [`abort_cleanup_pending`](#troubleshooting). Roll back host is held to the same quorum and
   readiness checks as any other host stop.
6. If the rollout pauses, read the code and message. `needs_attention` with `controller_rolled_back` means the Controller's new image
   was not ready and its previous image and data were restored; no host was touched. Resume asks for a new swap; abort stops here.

## Expected result

Every host runs the release and has committed it, one host at a time, with at most one host in maintenance at once. Guests stopped by
the rollout run again on the host that ran them. When the last host commits, the rollout is **Finalized** and the release may now run
irreversible state migrations. A host that falls back to its previous release, or does not confirm the new one, pauses the rollout
instead of finalizing it.

## Troubleshooting

- **Run preflight is greyed out:** the pool master is not freshly connected, a member lacks pool rollouts, or no release sequence is
  known. The page says which.
- **`running_vms`:** a guest is running on the next host and the policy is Refuse. Stop it, or abort and start again with the
  graceful shutdown choice.
- **`quorum_margin` or `host_busy`:** taking the next host down would leave the pool without a majority, or that host is running
  another update operation. Fix the pool, then resume.
- **`needs_attention` with a code from a host** (for example `insufficient_space`, or a refusal to stop or start a guest): the host's own
  code and message are in the pause. Fix the cause and resume: a resume sends the step again as a new attempt instead of repeating the
  failed one. A guest that fails to shut down or start, or a guest list that libvirt cannot give (`vm_inventory_unavailable`, for
  example while libvirt is still starting after a reboot), is retried by itself for five minutes, as a new attempt each time; if it still
  fails the rollout pauses `needs_attention` with the code `vm_shutdown_failed`, `vm_start_failed` or `vm_inventory_unavailable` and the
  host's own message.
- **`abort_cleanup_pending`:** an abort could not yet put back a host: it is still rebooting, maintenance mode could not be cleared, or
  a guest the rollout stopped is not running. The rollout stays **Paused** (it is not Aborted, and a new rollout is refused with
  `rollout_in_progress`); the rows name the host and the guests. The master retries by itself for 30 minutes per host, then stops and
  says so. Fix the cause (bring the host back, start the guest, clear maintenance mode) and select **Abort** again: that retries at
  once and ends the rollout when everything is back. **Resume** and **Roll back host** are refused while an abort is pending. If you
  have put everything back by hand, or accept leaving it, send the abort with `"force": true` to the master's local control route
  (`POST /api/local/v1/pool/updates/rollouts/{id}/control`; the panel does not offer it): the rollout ends **Aborted** and each host's
  row keeps what was left (its error and the guests listed in `stopped_by_update`) so nothing is lost silently. `force` is refused
  with `abort_not_pending` on any rollout that is not in this state.
- **`step_timeout`:** a host did not finish staging, did not answer, or a wait ran past six hours. The message names the host.
  Resume starts a fresh wait.
- **A rollout was aborted and the release is still not on every host:** start a new rollout for the same release. Hosts that already
  run it are not asked to do anything again; they are counted as done. A host that booted into the release but has not committed it
  yet (or is armed for it) is refused with `unconfirmed` until its own update finishes; a host that never updated (it joined or was
  installed on this release) counts as running it.
- **`master_changed`:** the pool master changed during the rollout. The new master pauses the rollout; review the pool, then resume
  or abort.
- **`controller_needs_attention`:** the Controller could not be restored automatically and was left stopped: the previous image was
  never forced off, and a new image is forced off only when it never became ready (and then only during the rollback). Recover it
  from the host with the [host recovery export](recover-without-controller.md) and
  [deploy a replacement](deploy-controller-appliance.md) if needed; the rollout stays paused.
- **A host does not return:** nothing is restarted elsewhere and no guest is assumed stopped. Recover the host from its console;
  its boot entry has three tries and then falls back by itself.

## Verification

Not walked through. Nothing on this page has been run on a real pool, in a browser against a live Controller, or with a real
Controller appliance. The rollout coordinator, the controller_first step and the appliance swap are covered by automated tests
against fake peers and a fake libvirt only: see the [Phase I-4b results](../qa/image/phase4-appliance.md) and the
[Phase I-4a results](../qa/image/phase4-rollout.md), which list what remains open (a nested three-host lab, a real Controller swap
and a browser walkthrough).

The recovery behaviour described above (abort that stays open until clean-up is confirmed and its forced end, bounded retries of guest
steps, skipping hosts already on the release, step timeouts, the watchdog teardown wait on pools of four or five hosts) was added after
review and is covered by fake-peer unit tests only; it has not been run on a real pool.
