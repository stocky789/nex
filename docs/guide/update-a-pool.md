# Update a pool

## Status

Development build, **unverified**. A pool of image-edition Nex Core hosts is updated by the pool master's Core as one durable
rollout: it stages the release on every host, swaps the Controller appliance first when the Controller runs on a pool host, then
updates one host at a time, members first and the master last. **Settings** → **Updates** → **Pool rollout** shows what the master
and the hosts report and sends your pause, resume, abort and roll-back requests. The rollout belongs to the master, so it carries on
if the Controller is stopped or replaced. No part of this page has been walked through on a running pool: see
[Verification](#verification). Guests are never forced off; live migration does not exist yet, so hosts with running guests wait
or, if you choose, shut them down.

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
   **Abort** (hosts that already committed the release stay on it) or **Roll back host** for a host that has activated the release.
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
- **`master_changed`:** the pool master changed during the rollout. The new master pauses the rollout; review the pool, then resume
  or abort.
- **`controller_needs_attention`:** the Controller could not be restored automatically and was left stopped (it is never forced off).
  Recover it from the host with the [host recovery export](recover-without-controller.md) and
  [deploy a replacement](deploy-controller-appliance.md) if needed; the rollout stays paused.
- **A host does not return:** nothing is restarted elsewhere and no guest is assumed stopped. Recover the host from its console;
  its boot entry has three tries and then falls back by itself.

## Verification

Not walked through. Nothing on this page has been run on a real pool, in a browser against a live Controller, or with a real
Controller appliance. The rollout coordinator, the controller_first step and the appliance swap are covered by automated tests
against fake peers and a fake libvirt only: see the [Phase I-4b results](../qa/image/phase4-appliance.md) and the
[Phase I-4a results](../qa/image/phase4-rollout.md), which list what remains open (a nested three-host lab, a real Controller swap
and a browser walkthrough).
