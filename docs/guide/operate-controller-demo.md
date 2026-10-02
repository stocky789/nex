# Operate the public Controller demo

## Status

The demo is a separate, read-only service using the real Controller UI with fictional
API data and shared `demo` / `demo` login. It is not a production Controller/Core
installation. It never connects to hosts or executes VM, storage or backup operations.

The private backend listens on **8081** behind the website operator's HTTPS proxy.
A network-disabled container serves a Unix socket; a dedicated non-root nginx service
exposes only the configured private listener. Publication briefly restarts the demo and
invalidates sessions. Failed-candidate checks restore the prior image automatically.

## Prerequisites

- Authorization to administer the selected deployment server and its HTTPS proxy.
- Docker, nginx, Python 3, curl, systemd and sudo/visudo installed, with
  current host security updates. Obtain explicit authorization before rebooting a
  server shared with the main website.
- The reviewed [demo hosting package](../../packaging/demo/README.md) installed with
  a fixed HTTPS origin, selected private listener and exact allowed proxy peers.
- Default-deny ingress, key-only administration and firewall access only from the
  approved HTTPS proxies and administration/publishing sources. Never forward public
  traffic directly to the private HTTP port.
- All four `NEX_DEMO_DEPLOY_*` Gitea secrets configured for a dedicated publishing
  account and forced-command key, including the pinned SSH host key. The demo must
  not receive the website's administrative credentials. Pull-request runners must
  be isolated and receive no deployment secrets or reusable production state.
- A retained verified prior image before performing a rollback.

## Steps

1. Bootstrap the selected server using the explicit root installation commands in the
   [hosting package](../../packaging/demo/README.md#explicit-root-installation).
   Keep endpoint, account and trusted-peer values in private configuration. The
   installer does not configure firewall, SSH or the external TLS route for you.
2. Publish an approved revision through Gitea's **Demo** workflow. Every main push
   rebuilds the shared UI and demo executable and checks the exact image in a browser (the
   `demo` job); a separate `deploy` job then sends it through the installed healthchecked
   wrapper, in its own concurrency group that is never cancelled. Pull requests check
   without publishing. A manual workflow run can publish the selected main revision.
   Re-running the same revision is safe: replaying the running release keeps its rollback
   image, while a different rebuilt image under a retained non-current release tag is
   refused. The deploy job needs the verified image in the same runner's Docker daemon;
   a superseded verification run skips that revision's deploy, and the next push deploys.
   Missing deploy secrets or a failed check must fail publication, not count as success.
3. Configure the public demo hostname at the HTTPS proxy to forward to the private
   server on **8081**, preserving the public Host and browser Origin. If trusted-client
   rate limiting is enabled, the proxy must replace or append authentic client
   `X-Forwarded-For`. Do not trust forwarding headers from arbitrary network clients.
4. Check the runtime locally:

   ```sh
   sudo docker inspect nex-demo --format '{{.State.Health.Status}}'
   sudo curl --fail --unix-socket /run/nex-demo/demo.sock http://localhost/healthz
   systemctl status nex-demo-proxy.service --no-pager
   sudo cat /var/lib/nex-demo/releases.json
   ```

5. Open the public **HTTPS** address and follow the [browsing guide](explore-controller-demo.md).
   Verify the fictional/read-only notice, login, resource panels, backup jobs and
   recovery points, a refused management action, source download and logout. Check
   both themes and a phone-sized layout before advertising the address.
6. Review proxy error and container logs without publishing visitor addresses or cookies.
   Proxy access logging is disabled; its separate error journal is rate-limited, bounded
   to 8 MiB and discarded at reboot:

   ```sh
   sudo journalctl --namespace=nex-demo -u nex-demo-proxy.service --no-pager -n 50
   sudo docker logs --tail 50 nex-demo
   ```

7. To roll back, read the retained previous revision from `releases.json`, select it
   explicitly, and run as root:

   ```sh
   docker image save "nex-demo:$PREVIOUS_RELEASE" | gzip -1 | \
     /usr/local/sbin/nex-demo-deploy "$PREVIOUS_RELEASE"
   ```

   Verify health and browser behavior again. Do not prune either selected or retained
   image. If packaging changes the host security boundary, review and reinstall its
   root-owned files explicitly; normal UI publication does not replace host policy.

## Expected result

The public domain serves the current checked Controller UI with a coherent fictional
inventory. Updates to shared UI source are rebuilt automatically after publication to
the configured Git branch, with no copied demo pages to maintain. New client contract
requirements must still update the sample adapter and pass checks before deployment.

The container has no external network, real host data, secrets, Docker socket or
privileged capabilities. Its only writable host mount is bounded socket storage.
Unknown Hosts are refused, requests are bounded/rate-limited, and management actions
receive a read-only refusal. The previous verified image remains available after an
update; its matching source remains inside that image.

## Troubleshooting

- **DNS or certificate failure:** correct the external HTTPS route before public
  launch. The private listener is not a substitute for browser TLS.
- **Connection refused or wrong Host:** verify the exact proxy source allowlists,
  configured public Host, service state and selected listener. Do not open 8081 to
  everyone to work around a missing proxy rule.
- **HTTP 429:** a client exceeded request, login or source-download limits. If all
  visitors share a bucket, verify exact trusted-proxy settings and authentic client
  forwarding; never enable unrestricted trust of `X-Forwarded-For`.
- **Publication fails:** the prior image is restored when one exists. Inspect the
  workflow result and local health; correct the image or configuration before retrying.
  The wrapper also refuses an image whose layers inflate past 1 GiB in total, that has more
  than 4,096 archive members, or that loads larger than 1 GiB; none of these changes the
  running release.
- **The demo does not start after a crash:** a leftover socket nobody is listening on is
  removed automatically; a regular file at the socket path or a live server is refused.
- **Archive upload times out:** the wrapper refuses an upload idle for 30 seconds or
  exceeding 600 seconds overall, releases its deployment lock and removes temporary
  files before importing an image. The current release stays unchanged; restore the
  transport and retry the complete archive.
- **The demo fails after boot:** check the socket tmpfs mount, Docker and proxy service
  ordering. Recheck the runtime after host security updates or an authorized reboot.
- **Login stops working after an update:** sign in again. Restart intentionally loses
  disposable sessions, not pool data.
- **A submitted management action fails:** expected. No writable sandbox or real
  infrastructure is behind this public account.

## Verification

The [deployment and browser report](../qa/2026-10-01-public-controller-demo.md) records
actual artifact, isolation, publication, rollback and reboot checks. It distinguishes
private-backend verification from external domain/TLS routing and the first real Gitea
run after the project owner commits and pushes the workflow.
