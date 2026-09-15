# Check and apply software updates

## Status

Available in Nex Core / Controller development builds (alpha) that include
the Updates APIs and packaged installers. Both packages ship the Nex archive signing
key and configure the signed APT source, so updates are signature-verified.

## Prerequisites

- Administrator session on Controller, or root on the host-local UI.
- Reachable Nex APT archive (default `https://nexvirtualization.com/apt`, overridable
  with `/etc/nex/updates.conf` or `NEX_UPDATE_REPO`). Both Nex Core and the Controller
  appliance configure it as an APT source (`/etc/apt/sources.list.d/nex.sources`, signed
  by `/usr/share/keyrings/nex-archive-keyring.gpg`), rewritten from `repo_url` whenever
  you check or apply, so changing the URL takes effect without reinstalling.
- The in-product **Check**/**Update** flow *is* `apt`: it runs `apt-get update` and
  installs the selected package, so dependencies resolve and package signatures are verified.
  Core uses plain `apt-get install nex-core-local` so a binary-only appliance can receive its
  first package; Controller uses `apt-get install --only-upgrade nex-controller` through its
  privileged helper. Running the matching command yourself does the same thing.
- Packaged `nex-controller` or `nex-core-local` installs for apply to succeed.

## Steps

1. Open **Updates** (Controller: View menu or sidebar; host-local: aside **Updates**).
2. Select **Check**. The package index is refreshed at most once every five minutes,
   so a release published moments ago can take that long to appear.
3. If an update is listed, select **Update**.
4. Watch the progress through refresh, install and restart; errors appear on the same
   page and include apt's own message. Progress moves in steps, not by download percentage.
5. Sign in again if the session drops after restart.

## Expected result

Installed and available versions are shown. Apply schedules a package install and
service restart; the page reports that the service is restarting. After a Core update the
host identifies as `Nex Core <new version>` in `/etc/os-release`, the console banner and the
Controller host page **Version** field; that package version is the only Nex Core version.

## Troubleshooting

- Repo errors appear beside the version fields; a failed package-index refresh now says so directly. Fix connectivity or `repo_url` and check again.
- "Package not available from …" means no configured APT source offers the package.
  Check `repo_url`, then `apt policy nex-core-local` on the host.
- "No newer … available" means apt found nothing to upgrade; the installed version is
  already the candidate.
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
- If the page reports *Still applying after 10 minutes; the result is unknown*, the page stopped
  following the update; it has not completed or failed as far as the page knows. Select
  **Check** to read the host's current state.
- If Core shows Installing after the service is already back, refresh **Updates**; completed
  installs clear themselves once the installed version matches available.
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

Automated API tests cover check/apply and apt failures; package identity was checked in a
labeled simulation. A browser walkthrough and an on-host upgrade pulling a new dependency are
outstanding. See the [single Nex Core version checks](../qa/single-nex-core-version.md).

