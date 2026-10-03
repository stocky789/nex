# Check and apply software updates

## Status

Available in Nex Core / Controller development builds (alpha) that include
the Updates APIs and packaged installers. Both packages ship the Nex archive signing
key and configure the signed APT source, so an available package is signature-verified.
The working `0.4.19` version is not evidence of a published candidate. Updates appear once
the Release workflow has published packages to the signed APT archive. Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

**Image-edition hosts (development, unverified).** A host installed from the image edition does not use `apt`: its own
update engine checks signed release information, downloads and writes the new system image to its unused slot, and only
when you choose to reboot into it makes it the next boot. Those hosts are listed under **Host images** on the same
**Updates** page; the rest of this page describes the classic package flow. The image-edition steps below have **not**
been walked through on a running Controller, a host or a VM: only automated handler and component tests have run (see
[Verification](#verification)). See also [Roll back a host update](roll-back-a-host-update.md).

## Prerequisites

- Administrator session on Controller, or root on the host-local UI.
- Reachable Nex APT archive (default `https://nexvirtualization.com/apt`, overridable
  with `/etc/nex/updates.conf` or `NEX_UPDATE_REPO`). Both Nex Core and the Controller
  appliance configure it as an APT source (`/etc/apt/sources.list.d/nex.sources`, signed
  by `/usr/share/keyrings/nex-archive-keyring.gpg`), rewritten from `repo_url` whenever
  you check or apply, so changing the URL takes effect without reinstalling.
- The in-product **Check**/**Update** flow *is* `apt`: it runs `apt-get update` and
  installs the selected package, so dependencies resolve and package signatures are verified.
  Core and Controller use plain `apt-get install` through their respective privileged paths, so
  an appliance with only its bundled binary can receive its first package. Running the matching
  command yourself does the same thing.
- A classic host with a separate `/var` entry in `/etc/fstab` adopts the read-only root
  layout at its first boot after installing a build that includes it, even if the host was
  installed earlier; see [Install Nex Core](install-nex-core.md).
  **Update** opens a writable window when needed and restores the original root mount
  state afterwards. Before running `apt` yourself, check `findmnt -no OPTIONS /`.
  If it reports `ro`, open the same window as root and close it when the command finishes:

  ```sh
  mount -o remount,rw /
  apt update && apt install --only-upgrade nex-core-local
  mount -o remount,ro /
  ```

  A host without a separate `/var` keeps its writable root and needs no window.

## Steps

1. Open **Updates** (Controller: **Settings** → **Updates**, `#/settings/updates`; the old
   `#/updates` link redirects there. Host-local: aside **Updates**).
2. Select **Check**. The package index is refreshed at most once every five minutes,
   so a release published moments ago can take that long to appear.
3. If an update is listed, select **Update**.
4. Watch the progress through refresh, install, shutdown of virtual machines, reboot and
   report check; errors appear on the same page and include apt's own message. Progress moves in
   steps, not by download percentage. A host reboot takes several minutes.
5. Sign in again if the session drops after the restart. Before you start, expect the host (or,
   for the Controller, the appliance) to reboot: machines on a Core host are stopped for the
   reboot, so schedule the update accordingly.

## Expected result

Installed and available versions are shown. Apply installs the package and restarts the
service. On a Core host the update ends with a reboot: virtual machines are shut down cleanly
(never forced off), the host reboots, and the machines that were running start again. The
Controller appliance reboots its guest after its own update. The progress then reads *waiting for
this host to report to its Controller* and becomes **Update complete** only after the Controller
has received a report from the restarted host. If a machine does not shut down within five
minutes, the host is not rebooted, stays up with its machines running, and the page says so.
After a Core update the
host identifies as `Nex Core <new version>` in `/etc/os-release`, the console banner and the
**Nex Core** field on the host's **Overview** tab in Controller; that package version is the only Nex Core version.

## Update an image-edition host (unverified)

Requires an image-edition host that is freshly connected and whose Nex Core and Controller are the same development build
(the host offers `image-update-v1` and the Controller acknowledges it; until then the host is not listed).

1. Open **Settings** → **Updates** and find the host under **Host images**. The panel shows only what the host reports: the
   running release and slot, any staged and previous release, the newest verified release, the channel, the rollback floor,
   the phase and the latest operation or failure.
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

## Update channels

Nex is published on two channels. **Stable**, the default, follows the `main` branch. **Development** follows the `dev` branch:
every change merged for the next stable release is published as a pre-release numbered just below that release (for example
`0.4.19~~dev.231`), so it can be rough. Use Development on disposable hosts that should test early builds. A host installed from
a development ISO starts on Development.

An image-edition host follows one channel from one update server. Both are host settings
that the host keeps in `/etc/nex/updates.conf` and reports in its image status. There is no built-in server: until an
operator sets a base URL, a check fails with `source_unconfigured`. Changing the channel or server clears the host's
available release and starts replay tracking for the new source; the host refuses the change while a check or an update
operation is running. Hosts verify signed channel information whatever the transport, so an HTTP mirror is acceptable.
A package-edition host reads its channel from the archive it uses: Stable is the archive root and Development its `dev` subdirectory.

The channel is kept on each host, so every host in a pool chooses its own, and a pool rolling update refuses a pool whose hosts
are on different channels (`channel_mismatch`): set the same channel on all of them first. To change it:

1. Nex Controller, image-edition host: **Settings** → **Updates**, find the host under **Host images**, choose **Update channel**
   and select **Change channel**. The host reports the new channel with its next report. The picker offers Stable and Development;
   a channel set through the API stays visible but is not offered again.
2. Nex Core host-local page: **Updates**, choose **Update channel** and select **Change channel**; the page checks again.
3. Select **Check** to read the release the new channel offers.

Moving a host from Development back to Stable never downgrades it: a package host's `apt` does not install an older version by
itself, and the image engine refuses a release below the host's rollback floor, so nothing is offered until Stable publishes
something newer than the host runs. A package-edition Controller (the appliance's own package updater) has no selector: it follows
the archive in `repo_url` in `/etc/nex/updates.conf`; to move it, set `repo_url` to the archive root plus `/dev`
(`https://<site>/apt/dev`) on the Controller and select **Check**. This section is unverified: the channel selectors have not
been walked through in a browser or on a running host (see [Verification](#verification)).

## Troubleshooting

- Repo errors appear beside the version fields; a failed package-index refresh now says so directly. Fix connectivity or `repo_url` and check again.
- **The update says it needs attention after the restart:** the package is installed, but the host
  has not reported to its Controller since it restarted, so the update is not counted as
  complete. Check that the Controller is running and reachable from the host, then select **Check**.
  The host keeps retrying on its own. If the page also says the host was not rebooted, a virtual
  machine did not shut down in time: shut it down from its console and update again.
- **A virtual machine did not start again after the reboot:** a machine starts again after an
  update only if it was running before it, and only after the host is back in service, so a host
  in maintenance mode, or a pool master still waiting for its members to confirm it, starts it
  later, not immediately. Start it from its page if you do not want to wait.
- **An image update task failed or needs attention:** the host card shows its recorded error
  beside the task link. Open the task for details and check the running/staged image state
  before retrying; a failed check does not mean the host has no newer release.
- **Upgrade order for 0.4.19 (hard cutover):** update the Controller first, then update **and
  restart** Nex Core on every host in the same window. An upgraded Controller refuses a Core
  master that still runs 0.4.18 with `invalid JSON request: unknown field <name>; upgrade or
  restart Nex Core on the reporting host`, and that host stays stale in Controller (no inventory
  or task updates) until its Core is restarted on the new build. A host that was not restarted
  also cannot join a pool: the master refuses it until it reports a valid hostname.
- "Package not available from …" means no configured APT source offers the package.
  Check `repo_url`, then `apt policy nex-core-local` on the host.
- **ZFS storage reports "ZFS device discovery is unavailable" after updating a host installed
  from 0.3.4 or earlier:** those hosts did not have Debian's `contrib` component enabled, which
  carries the OpenZFS packages. The Core package only recommends them, so the update itself
  succeeds; it then enables `contrib` in `/etc/apt/sources.list.d/debian.sources`, and the next
  **Update** installs the OpenZFS packages when a Debian mirror offering `contrib` is reachable.
  Without such a mirror, updates still apply and ZFS provisioning stays unavailable on that host.
- "No newer … available" means apt found nothing to upgrade; the installed version is
  already the candidate.
- **Controller shows Installed 0.2.9, Available 0.3.0, then says no newer package is
  available:** its appliance still runs the bundled Controller binary, so the 0.2.9 helper's
  `--only-upgrade` request installs nothing. From the appliance's local root shell, install the
  published package directly (without `--only-upgrade`), then check again:

  ```sh
  sudo apt update && sudo apt install nex-controller
  ```

  This is a recovery for the affected 0.2.9 appliance, not evidence that the local helper fix
  below has been rebuilt or published.
- Controller applies updates through a privileged helper; its update path unit must be enabled.
- **Controller stays on `Update requested…`:** the update helper is not being started. On a
  Controller running 0.2.5 or earlier, one failed update left its request behind and the retries
  stopped the update trigger for good. That helper is also the one that runs your next update,
  so upgrade the package directly on the Controller VM instead of selecting **Update** again:

  ```sh
  sudo rm -f /var/lib/nex-controller/pending-update.json
  sudo systemctl reset-failed nex-controller-update.path nex-controller-update.service
  sudo apt update && sudo apt install --only-upgrade nex-controller
  ```

  From 0.2.6 a failed update releases its request, reports apt's message on the page and never
  stops the trigger, and installing the package clears a stuck request or trigger by itself.
- Appliance images and new Core deploys include the update helper so **Update** works
  after deploy. Older Controller VMs without that helper need a redeploy (or a one-time
  `nex-controller` package install).
- The Controller and Core services stop while their package installs and start again
  afterwards, so the page loses its connection for part of every update. It keeps following the
  update through that gap and reports the result once the service is back; a browser console may
  show refused requests to `/api/v1/updates` in the meantime, which is expected.
- If the page reports *Still applying after 10 minutes; the result is unknown*, the page stopped
  following the update; it has not completed or failed as far as the page knows. Select
  **Check** to read the host's current state.
- If Core shows Installing after the service is already back, refresh **Updates**; completed
  installs clear themselves once the installed version matches available. While the same Core is
  still applying, the page keeps showing its real step: apt reports the new version as soon as it
  unpacks, which is before the restart the update still has to do.
- Stopping or restarting Nex Core during an apply waits for that install to finish instead of
  interrupting it, so the stop takes as long as apt does. Let it finish; a killed apt is what
  leaves a half-configured package behind.
- **"refusing APT downgrade while version-2 storage state is retained"** on a host that is
  upgrading, not downgrading: that host still has the release whose downgrade guard read APT's
  configure step as an unidentified package. The guard that runs is the installed one, so the
  fixed package cannot install past it. On the host, move the hook aside, upgrade, and let the
  new package restore it:

  ```sh
  sudo mv /etc/apt/apt.conf.d/91nex-core-storage-downgrade /root/
  sudo apt update && sudo apt install --only-upgrade nex-core-local
  ls /etc/apt/apt.conf.d/91nex-core-storage-downgrade
  ```

  The last command must list the file again: the new package writes it back, so the protection
  against a real downgrade returns. A Controller VM shows the same message for
  `nex-controller`; move `/etc/apt/apt.conf.d/91nex-controller-storage-downgrade` instead.
  Do not remove `nex-core-local` to work around it: the guard deliberately refuses removal while
  storage state exists.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

Automated API tests cover check/apply and apt failures; package identity was checked in a
labeled simulation. The binary-only appliance helper regression is covered in a simulated helper
test; no rebuilt package, published artifact, browser walkthrough or on-host upgrade is claimed.
See the [single Nex Core version checks](../qa/single-nex-core-version.md).

Image-edition hosts: automated tests cover the Controller routes, acknowledgement that withholds
`inventory.image` from an older Controller, relay into the host engine and Host images panel
(`go test ./internal/controller ./internal/agent ./internal/localcore`, `web/src/image-updates.test.mjs`;
see [Phase I-3c results](../qa/image/phase3-controller.md)). The review-fix batch also exercised
terminal task failures, attention and recovery in Chromium with the actual panel/styles and a
**simulated API**, not a running Controller/host:
[review-fix verification](../qa/2026-10-03-immutable-review-fixes.md).
The full image update through Controller remains unverified. Historical engine proof used a VM's
command-line tool, not these routes ([Phase I-3b results](../qa/image/phase3-boot.md)).

Update channels: the package edition's channel setting (archive subtree, host-local route, refusals) and the channel picker's
request and rendering are covered by automated tests only (`go test ./internal/nexupdate ./internal/localcore`,
`web/src/channel.test.mjs`, `web/src/image-updates.test.mjs`). No channel-changing browser walkthrough,
host channel switch or published development archive was exercised; the release side is in
[publish a release](publish-a-release.md#release-channels).
