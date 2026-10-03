# Public Controller demo deployment

This package runs the real Controller UI against a dedicated **read-only fictional API**.
It does not run the production Controller, Nex Core, a hypervisor, SSH host control or
backup engines. Shared `demo` / `demo` credentials authorize browsing only. The image
contains matching downloadable corresponding source and the real API reference;
production API documentation is not a promise of executable demo operations.

## Boundary and prerequisites

- A selected Linux deployment server with Docker, nginx, Python 3, curl, sudo/visudo
  and systemd. Docker is accessed only by the root deployment wrapper;
  neither the publisher nor `nex-demo` needs Docker-group membership.
- A fixed HTTPS public origin and a private IPv4 listener on **8081**. The selected
  TLS proxy must preserve browser Host and Origin. Do not expose private HTTP to the
  internet, forward public HTTP directly, or disable Secure cookies to make it work.
- Default-deny firewall rules allowing only the selected proxy peers to 8081 and
  known administration/publishing sources to SSH. Restrict any existing website
  listener separately without breaking its current proxy. This package never changes
  firewall, DNS, TLS certificates or SSH configuration automatically.
- Exact trusted TLS proxy addresses when enabling forwarded-client rate limiting.
  The trusted proxy must replace or append authentic client `X-Forwarded-For`.
  Other clients cannot select their rate-limit identity by spoofing that header.
- A dedicated SSH publishing account and four demo-specific Gitea secrets:
  `NEX_DEMO_DEPLOY_HOST`, `NEX_DEMO_DEPLOY_USER`, `NEX_DEMO_DEPLOY_KEY`,
  `NEX_DEMO_DEPLOY_KNOWN_HOSTS`. Keep values private and pin the SSH host key.
  The workflow maps these to the deployment script's `NEX_DEPLOY_*` environment.
  Isolate Docker-capable runners for untrusted pull requests; never provide production
  secrets or reusable production filesystem state to such jobs.

## Explicit root installation

Supply environment-specific values privately; the installer validates the private
listener, HTTPS origin, peer addresses and existing publishing account.

```sh
sudo python3 packaging/demo/install.py \
  --listen "$NEX_DEMO_LISTEN" --origin "$NEX_DEMO_ORIGIN" \
  --deploy-user "$NEX_DEMO_DEPLOY_USER" \
  --proxy-peer "$PROXY_ONE" --proxy-peer "$PROXY_TWO" \
  --trusted-proxy "$PROXY_ONE" --trusted-proxy "$PROXY_TWO"
sudo systemctl daemon-reload
sudo systemd-tmpfiles --create /etc/tmpfiles.d/nex-demo.conf
sudo systemctl start 'run-nex\x2ddemo.mount'
sudo runuser -u nex-demo -- nginx -t -c /etc/nex-demo/nginx.conf
sudo systemctl enable --now nex-demo-proxy.service
```

An approved direct verification address can be supplied with another `--proxy-peer`,
without `--trusted-proxy`. Remove that temporary allowance from the firewall and service
configuration after verification. The installer writes root-owned configuration and a
command-specific sudo rule; it does **not** revoke an account's pre-existing broader sudo.

Use a dedicated account with no broad sudo or Docker-group membership. Keep its
authorized key root-owned and restrict it to the exact publishing sources:

```text
from="<approved exact publishing addresses>",restrict,command="/usr/local/sbin/nex-demo-ssh-deploy" <publishing public key>
```

The forced command accepts only the deployment script's commit-tagged publication;
shell access, arbitrary sudo commands and SSH forwarding are unavailable to this key.
Keep existing website credentials separate so the demo job never receives them.

The socket directory is a separate 1 MiB, 128-inode tmpfs, owned by UID/GID 64050.
Its mount is required before Docker and the proxy start after boot. The proxy runs as
that same non-login account, with restricted peer addresses, binding and filesystem
access. The container runs as UID/GID 64050, has `--network none`, no published ports,
a read-only root, no capabilities or privilege escalation, and bounded CPU, memory,
PIDs, file descriptors, temporary space and logs. It mounts only the bounded socket
directory; it never receives the Docker socket, deployment secrets or host data.

## Build, check and publish

The Demo workflow runs on every main push, pull request and manual dispatch. Its `demo`
job checks the backend and current UI, builds the image, extracts its exact
binary/UI/source/docs, and exercises browser navigation against that artifact; it has
job-level concurrency in the shared runner group, so cancelling it is harmless. A separate
`deploy` job (`needs: demo`, main push or manual dispatch only) has its own
`nex-demo-deploy` concurrency group with cancel-in-progress disabled, so a newer
verification run never cancels a live deploy or its rollback, and only that job reads deploy
secrets. It relies on the verified image still being in the Docker daemon of the same runner;
that arrangement, and the Gitea runner's support for job-level `concurrency`, were not verified
(a workflow-level `nex-demo-<ref>` group remains as a fallback). A verification run that shares
a concurrency group with a later run can be superseded and skip that revision's deploy; the next
push deploys. Missing secrets fail publication instead of silently reporting success.
The GitHub workflow mirror skips on github.com, following the project's forge policy.

For a manually selected approved revision, build and run the same checks:

```sh
go test -race -timeout 5m ./internal/demo ./cmd/nex-demo
python3 -m unittest discover -s tests -p 'test_demo_archive.py'
npm --prefix web ci
npm --prefix web run typecheck
docker build --platform linux/amd64 -f packaging/demo/Dockerfile \
  --build-arg "NEX_DEMO_REVISION=$RELEASE" --tag "nex-demo:$RELEASE" .
python3 tests/integration/demo_deploy_image.py --image "nex-demo:$RELEASE"
# RELEASE is the selected immutable 40-character lowercase Git SHA.
# Run the exact-artifact browser smoke as shown in the Demo workflow, then:
NEX_DEPLOY_HOST="$NEX_DEMO_DEPLOY_HOST" \
NEX_DEPLOY_USER="$NEX_DEMO_DEPLOY_USER" \
NEX_DEPLOY_KEY="$NEX_DEMO_DEPLOY_KEY" \
NEX_DEPLOY_KNOWN_HOSTS="$NEX_DEMO_DEPLOY_KNOWN_HOSTS" \
GITHUB_SHA="$RELEASE" scripts/demo-ci-deploy.sh
```

Transport uses strict known-host checking and streams a compressed `docker save` archive
to the fixed root wrapper. It accepts only one immutable release argument, validates and
bounds the archive, refuses links/special files/extra tags/declared volumes or mutable
replacement, and repacks only the validated image before Docker imports it. Runtime
mounts, network, identity, entrypoint and origin come from installed root configuration,
not from image metadata or transport arguments.

The archive and its normalized output are limited to 768 MiB, including repeated layer
references and tar overhead, and to 4,096 tar members. Each layer is also stream-decompressed
during inspection against one 1 GiB inflation budget shared by all layers (gzip, xz and bz2 are
counted; zstd layers are refused), so a small archive cannot inflate past the disk it will be
loaded into. After `docker image load` the image size is checked against a 1 GiB cap, and an
oversized, unverifiable or mismatched image is removed. Stale `import-*` working directories are
swept once the deploy lock is held. Failed candidates are removed after successful rollback,
without deleting the selected or retained image. Replaying the release that is already running is
idempotent: the rollback image is kept, and a rebuilt image with a new config ID is kept and
restarted rather than raising an immutable-tag error. A different image under a retained
non-current release tag is still refused. Publish a new source revision when intentionally
replacing such an image. At start, `nex-demo` removes its socket path only when it is a socket
nobody is listening on; a regular file or a live server is still refused. The `/source` download has
a 20-minute per-response write window and a proxy limit of 10 requests per minute (burst 6); the
login limit counts only POST requests. Hashed `/chunks/*` files are cached immutably for a year while
the shell stays `no-store`, and the CSP no longer allows inline styles. The Docker drop-in uses
`Wants=`, not `Requires=`, so stopping the socket mount does not stop unrelated containers.

Publication is serialized. It imports the image before replacing the old container,
starts the candidate, checks its Unix `/healthz`, and restores the prior image on
startup error, health failure or handled interruption. There is **brief restart downtime**;
this is not a zero-downtime switch. The current and prior release images retain their
matching source archives. `/var/lib/nex-demo/releases.json` records the selection.

## Monitoring and rollback

```sh
sudo docker inspect nex-demo --format '{{.State.Health.Status}}'
sudo curl --fail --unix-socket /run/nex-demo/demo.sock http://localhost/healthz
systemctl status nex-demo-proxy.service --no-pager
sudo journalctl --namespace=nex-demo -u nex-demo-proxy.service --no-pager -n 50
sudo docker logs --tail 50 nex-demo
sudo cat /var/lib/nex-demo/releases.json
```

As root, select the retained previous revision and republish its existing image:

```sh
docker image save "nex-demo:$PREVIOUS_RELEASE" | gzip -1 | \
  /usr/local/sbin/nex-demo-deploy "$PREVIOUS_RELEASE"
```

Do not prune the selected or retained image. Authentication sessions are disposable;
publication and restart invalidate cookies, requiring visitors to sign in again.
Docker log rotation bounds container logs. Proxy access logging is disabled; errors
use a separate rate-limited, volatile journal bounded to 8 MiB, with no forwarding
into other host log files. Proxy diagnostics do not survive reboot.
Health status is local inspection, not an external alerting service.

The installed wrapper and proxy configuration are infrastructure code: normal image
publication does not silently replace them. Review and explicitly reinstall this
package when its host boundary changes. Shared UI and sample API changes require no
manual page copying; future contract changes still need corresponding fixture updates
and passing browsing checks.

See the [operator guide](../../docs/guide/operate-controller-demo.md),
[browsing guide](../../docs/guide/explore-controller-demo.md) and
[actual verification](../../docs/qa/2026-10-01-public-controller-demo.md).
