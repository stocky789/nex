# Check and apply software updates

## Status

Available in Nex Core / Controller **0.1.0-alpha1** development builds that include
the Updates APIs and packaged installers. Lab package repos are assumed; signing-key
rollout for APT sources remains separate. GUI walkthrough evidence is not yet recorded.

## Prerequisites

- Administrator session on Controller, or root on the host-local UI.
- Reachable package repo serving `updates.json` and `.deb` files (default
  `https://nexvirtualization.com/apt`, overridable with `/etc/nex/updates.conf` or
  `NEX_UPDATE_REPO`).
- Packaged `nex-controller` or `nex-core-local` installs for apply to succeed.

## Steps

1. Open **Updates** (Controller: View menu or sidebar; host-local: aside **Updates**).
2. Select **Check**.
3. If an update is listed, select **Update**.
4. Watch the progress bar through download, install and restart; errors appear on the same page.
5. Sign in again if the session drops after restart.

## Expected result

Installed and available versions are shown. Apply schedules a package install and
service restart; the page reports that the service is restarting.

## Troubleshooting

- Repo errors appear beside the version fields; fix connectivity or `repo_url` and check again.
- Controller cannot run `apt` itself; the privileged update path unit must be enabled.
- Appliance images and new Core deploys include the update helper so **Update** works
  after deploy. Older Controller VMs without that helper need a redeploy (or a one-time
  `nex-controller` package install).

## Verification

Automated API tests cover check/apply scheduling. Browser walkthrough against a live
update repo is outstanding.
