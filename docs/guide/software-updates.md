# Check and apply software updates

## Status

Available in Nex Core / Controller development builds (alpha). A Nex Core host does not use `apt`: its own
update engine checks signed release information, downloads and writes the new system image to its unused slot, and only
when you choose to reboot into it makes it the next boot. If the new release fails its health check after the restart, the host
returns to the previous release by itself. The steps below have **not** been walked through on a running Controller, a host
or a VM: only automated handler and component tests have run (see [Verification](#verification)). Navigation on this page
follows the development build 0.4.8 console layout; see [Navigate the Controller console](navigate-the-controller-console.md).
See also [Roll back a host update](roll-back-a-host-update.md).

## Prerequisites

- Administrator session on Controller, or root on the host-local UI.
- A host that is freshly connected and whose Nex Core and Controller are the same development build (the host offers
  `image-update-v1` and the Controller acknowledges it; until then the host is not listed).
- An update source: the host's base URL and channel, set under [Update channels](#update-channels). There is no built-in
  server: until an operator sets a base URL, a check fails with `source_unconfigured`.
- Free space on the host's data volume for the downloaded release (an update that would not fit is refused with
  `insufficient_space` before anything is downloaded).

## Steps

1. Open **Settings** → **Updates** (`#/settings/updates`; host-local: aside **Updates**) and find the host under **Host images**.
   The panel shows only what the host reports: the running release and slot, any staged and previous release, the newest
   verified release, the channel, the rollback floor, the phase and the latest operation or failure.
   The Hosts list, a host's panel, **Members** and the Overview placement cards also show a small **Update available**
   badge when the host reports a release newer than the one it runs, and **Update staged** or **Update pending reboot**
   once one is staged or armed. The badge is the host's own report, never a Controller check; a disconnected host's badge
   is marked **(last report)**.
2. Select **Check**. The host refreshes and verifies signed release information. It refuses a second check within 60 seconds.
3. If a newer release is listed, select **Download and stage**. The host downloads (and resumes after an interruption),
   verifies and writes the release to its unused system slot. **Nothing changes the next boot:** an unplanned reboot still
   boots the current release.
4. When the phase reads **Staged**, select **Reboot into update**. The host arms the new release with three tries and restarts
   now. Running guests stop the reboot unless you tick *Shut down running guests gracefully first*, which accepts their downtime.
5. After the restart the host checks its own health. If the new release passes, the phase becomes **Committed** and the
   previous release is kept for **Roll back** until the next update is staged. If it does not pass, the host returns to the
   previous release by itself and reports **Rolled back** with the failure `boot_failed`.

**Cancel** stops a running download or discards a staged release; the host refuses it once the release is armed. A refused
action appears as the task's error, for example `update_in_progress: another update operation is running`; the host keeps
running. Nothing is stored by the Controller: replace the Controller and the same state appears from the host's next report.

If activation was interrupted, preserve the original request ID and action fields. Recovery
requires an exact retry **against the host-local API**, for example
`POST /api/local/v1/updates/activate` with the same `request_id` and `running_vms`, using a
host-local root session, or the host's update CLI. Replaying the Controller API only returns
its stored task; it does not dispatch the request to Core again. Core's exact retry resumes
an armed request without rewriting its slot and can reschedule an interrupted delayed reboot;
once the target booted, it does not reboot again. A cancelled interrupted staged download
leaves the host ready for another update. `arm_incomplete` means not every partially published
boot entry could be removed: inspect the host's state and recovery error before retrying.

Updating several hosts as a pool, one at a time with members before the master, is described in [update a pool](update-a-pool.md) (unverified).
The Controller appliance's own system image is replaced by the Core that hosts it; see [Deploy the Controller appliance](deploy-controller-appliance.md).

## Expected result

The host shows the new release as running and **Committed**, and identifies as `Nex Core <new version>` in `/etc/os-release`,
the console banner and the **Nex Core** field on the host's **Overview** tab in Controller. Virtual machines are shut down
cleanly (never forced off) only if you chose that option; they start again after the host is back in service.

## Update channels

Nex is published on two channels. **Stable**, the default, follows the `main` branch. **Development** follows the `dev` branch:
every change merged for the next stable release is published as a pre-release numbered just below that release (for example
`0.4.19~~dev.231`), so it can be rough. Use Development on disposable hosts that should test early builds. A host installed from
a development ISO starts on Development.

A host follows one channel from one update server. Both are host settings
that the host keeps in `/etc/nex/updates.conf` and reports in its image status. Changing the channel or server clears the host's
available release and starts replay tracking for the new source; the host refuses the change while a check or an update
operation is running. Hosts verify signed channel information whatever the transport, so an HTTP mirror is acceptable.

The channel is kept on each host, so every host in a pool chooses its own, and a pool rolling update refuses a pool whose hosts
are on different channels (`channel_mismatch`): set the same channel on all of them first. To change it:

1. Nex Controller: **Settings** → **Updates**, find the host under **Host images**, choose **Update channel**
   and select **Change channel**. The host reports the new channel with its next report. The picker offers Stable and Development;
   a channel set through the API stays visible but is not offered again.
2. Nex Core host-local page: **Updates**, choose **Update channel** and select **Change channel**; the page checks again.
3. Select **Check** to read the release the new channel offers.

Moving a host from Development back to Stable never downgrades it: the update engine refuses a release below the host's
rollback floor, so nothing is offered until Stable publishes something newer than the host runs. This section is unverified:
the channel selectors have not been walked through in a browser or on a running host (see [Verification](#verification)).

## Troubleshooting

- **An update task failed or needs attention:** the host card shows its recorded error
  beside the task link. Open the task for details and check the running/staged image state
  before retrying; a failed check does not mean the host has no newer release.
- **`source_unconfigured` or `source_unreachable`:** the host has no update base URL, or cannot reach it. Set the source under
  [Update channels](#update-channels) and check again.
- **`insufficient_space`:** the data volume cannot hold the release; free space and start again.
- **A virtual machine did not start again after the reboot:** a machine starts again after an
  update only if it was running before it, and only after the host is back in service, so a host
  in maintenance mode, or a pool master still waiting for its members to confirm it, starts it
  later, not immediately. Start it from its page if you do not want to wait.
- **Upgrade order for 0.4.19 (hard cutover):** update the Controller first, then update **and
  restart** Nex Core on every host in the same window. An upgraded Controller refuses a Core
  master that still runs 0.4.18 with `invalid JSON request: unknown field <name>; upgrade or
  restart Nex Core on the reporting host`, and that host stays stale in Controller (no inventory
  or task updates) until its Core is restarted on the new build. A host that was not restarted
  also cannot join a pool: the master refuses it until it reports a valid hostname.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

Automated tests cover the Controller routes, acknowledgement that withholds
`inventory.image` from an older Controller, relay into the host engine and Host images panel
(`go test ./internal/controller ./internal/agent ./internal/localcore`, `web/src/image-updates.test.mjs`;
see [Phase I-3c results](../qa/image/phase3-controller.md)). The review-fix batch also exercised
terminal task failures, attention and recovery in Chromium with the actual panel/styles and a
**simulated API**, not a running Controller/host:
[review-fix verification](../qa/2026-10-03-immutable-review-fixes.md).
The full image update through Controller remains unverified. Historical engine proof used a VM's
command-line tool, not these routes ([Phase I-3b results](../qa/image/phase3-boot.md)).

Update channels: the channel setting (host-local route, refusals) and the channel picker's
request and rendering are covered by automated tests only (`go test ./internal/nexupdate ./internal/localcore`,
`web/src/channel.test.mjs`, `web/src/image-updates.test.mjs`). No channel-changing browser walkthrough,
host channel switch or published development release was exercised; the release side is in
[publish a release](publish-a-release.md#release-channels).
