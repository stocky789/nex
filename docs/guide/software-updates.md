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
- Appliance images and new Core deploys include the update helper so **Update** works
  after deploy. Older Controller VMs without that helper need a redeploy (or a one-time
  `nex-controller` package install).
- If the page reports *Still applying after 10 minutes; the result is unknown*, the page stopped
  following the update; it has not completed or failed as far as the page knows. Select
  **Check** to read the host's current state.
- If Core shows Installing after the service is already back, refresh **Updates**; completed
  installs clear themselves once the installed version matches available.

## Verification

Automated API tests cover check/apply and apt failures; package identity was checked in a
labeled simulation. A browser walkthrough and an on-host upgrade pulling a new dependency are
outstanding. See the [single Nex Core version checks](../qa/single-nex-core-version.md).

