# Operate the private Nex website

## Status

This procedure operates the generated static Nex website as a private HTTPS service
behind Pangolin. The dedicated `nex-website` systemd service runs nginx as the
`nex-website` account and serves the release selected by `/srv/nex-website/current`.
The rendered nginx configuration binds the chosen private server address; Pangolin
serves browser HTTPS and forwards to this private HTTP backend. The website remains
separate from Nex Controller and Nex Core. Unrestricted public
publication, SSH hardening, host-firewall rules and access-allowlist testing are outside
this procedure.

Pushes to `main` that change `website/` run the Website workflow. When the same
`NEX_DEPLOY_*` secrets used for ISO/APT publication are set, that workflow builds the
static site and publishes it through `nex-website-publish` on this host. ISO and package
publication stays on the Release workflow; this page covers the static pages and manual
publish or rollback.

## Prerequisites

- Root access to the selected website server and the approved generated website artifact.
- The [website hosting package](../../packaging/website/README.md) installed for the
  selected private listener.
- For CI or passwordless publish, the publish account bootstrap once:

  ```sh
  sudo NEX_PUBLISH_USER=<publishing-user> packaging/website/bootstrap-publish.sh
  ```

- An existing Pangolin HTTPS route and the maintainer-approved private access policy.
- A prior verified release retained under `/srv/nex-website/releases` before replacing
  `current`. Use the installed release tool, not a source checkout on the server.
- Optional: root once to bootstrap the private APT tree (`packaging/apt/bootstrap.sh`) so
  `/apt/` is served beside the static site; see [software updates](software-updates.md).

## Steps

1. Publish the approved generated artifact. Prefer the publish wrapper, which reads the
   health URL from `/etc/nex-website/health.env`, rejects an artifact containing executable
   files without changing it, switches `current` atomically and prunes older releases beyond
   the newest ten:

   ```sh
   sudo /usr/local/sbin/nex-website-publish publish /path/to/approved-build RELEASE_ID
   ```

   The lower-level tool remains available when you must pass the health URL yourself.
   It rejects links, executable files, unsupported extensions and selected credential
   patterns; it checks required notices and restores the previous `current` target if
   its local health request fails:

   ```sh
   sudo /usr/local/lib/nex-website/website-release.py --health-url http://PRIVATE_BACKEND:PORT/ publish /path/to/approved-build RELEASE_ID
   ```

2. Check nginx and the health timer after publishing:

   ```sh
   sudo -u nex-website nginx -t -c /etc/nex-website/nginx.conf
   systemctl status nex-website --no-pager
   systemctl status nex-website-health.timer --no-pager
   ```

3. From an authorized client, load the Pangolin HTTPS hostname. Check the home page and
   gallery, an unknown path, and a directory path. Confirm the strict CSP, HSTS,
   `X-Content-Type-Options`, framing and no-referrer headers, and that the directory
   path does not expose a listing. Compare every served file hash with the approved
   build manifest; the release health check confirms a response containing `Nex`, not
   the identity of every selected file.
4. Monitor service health and requests after deployment:

   ```sh
   systemctl status nex-website --no-pager
   systemctl status nex-website-health.timer --no-pager
   journalctl -u nex-website -n 100 --no-pager
   journalctl -u nex-website-health -n 100 --no-pager
   sudo tail -n 100 /var/log/nex-website/error.log
   ```

5. If the new release is faulty, select the exact retained release and roll back:

   ```sh
   sudo /usr/local/sbin/nex-website-publish rollback RETAINED_RELEASE_ID
   ```

   Re-run the checks in steps 2 and 3 before removing any release.

## Expected result

The browser reaches the selected generated release over HTTPS through Pangolin. nginx
accepts only GET and HEAD, does not list directories or serve dotfiles, and the website
service has no write access to root-owned releases. The prior release remains available
for a fast rollback until pruned by a later publish (the wrapper keeps the newest ten
including `current`). The health timer checks the private backend every five minutes and
writes its result to the journal; it does not notify an external monitoring system.
Logrotate runs daily, retains seven rotations with older rotations compressed, and
checks the 10 MiB `maxsize` threshold when it runs; that threshold is not a disk-usage
limit.

## Troubleshooting

- **Publish or local health check fails:** the tool restores the previous `current`
  target. Inspect the error and retain both releases while correcting the artifact or
  local serving configuration.
- **The artifact contains an executable file:** correct the generated artifact so its regular
  files are not executable, then publish that newly approved artifact. The root wrapper never
  changes modes in a caller-supplied directory.
- **The route is unavailable:** verify the private Pangolin route and access policy, then
  inspect `nex-website` status and journal entries. Do not weaken the private access
  policy merely to test availability.
- **Unexpected content or headers:** roll back to the retained release and
  investigate the artifact or configuration before retrying.
- **A directory appears browsable:** remove it from the served release or correct nginx
  configuration, then verify the path returns no listing before accepting deployment.

## Verification

Publish, rollback, headers and listing checks passed on a private HTTPS deployment; router/NAT
exposure, firewall, allowlists, SSH and reboot recovery were not verified. See the
[website hardening evidence](../qa/website-security/hardening.md).
