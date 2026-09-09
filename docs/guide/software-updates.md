# Check and apply software updates

## Status

Available in Nex Core / Controller development builds (alpha) that include
the Updates APIs and packaged installers. Lab package repos are assumed; signing-key
rollout for APT sources remains separate. GUI walkthrough evidence is not yet recorded.

## Prerequisites

- Administrator session on Controller, or root on the host-local UI.
- Reachable package repo serving `updates.json` and `.deb` files (default
  `https://nexvirtualization.com/apt`, overridable with `/etc/nex/updates.conf` or
  `NEX_UPDATE_REPO`). A fresh Nex Core host also configures this repo as an APT source
  (`/etc/apt/sources.list.d/nex.sources`, signed by `/usr/share/keyrings/nex-archive-keyring.gpg`),
  so `apt update` lists it and `apt` can install Nex packages directly. The URL follows `repo_url`
  in `/etc/nex/updates.conf`; the in-product **Check**/**Update** flow is separate from `apt`.
- Packaged `nex-controller` or `nex-core-local` installs for apply to succeed.

## Steps

1. Open **Updates** (Controller: View menu or sidebar; host-local: aside **Updates**).
2. Select **Check**.
3. If an update is listed, select **Update**.
4. Watch the progress bar through download, install and restart; errors appear on the same page.
5. Sign in again if the session drops after restart.

## Expected result

Installed and available versions are shown. Apply schedules a package install and
service restart; the page reports that the service is restarting. After a Core update the
host identifies as `Nex Core <new version>` in `/etc/os-release`, the console banner and the
Controller host page **Version** field; that package version is the only Nex Core version.

## Troubleshooting

- Repo errors appear beside the version fields; fix connectivity or `repo_url` and check again.
- Controller cannot run `apt` itself; the privileged update path unit must be enabled.
- Appliance images and new Core deploys include the update helper so **Update** works
  after deploy. Older Controller VMs without that helper need a redeploy (or a one-time
  `nex-controller` package install).
- If Core shows Installing after the service is already back, refresh **Updates**; completed
  installs clear themselves once the installed version matches available.

## Verification

The [single Nex Core version checks](../qa/single-nex-core-version.md) record the package
identity path (real `dpkg` into a temporary root, labeled simulation) and the Controller host
page showing one **Version** row; no installed host has yet taken an update with this change.

Automated API tests cover check/apply scheduling. Browser walkthrough against a live
update repo is outstanding.
