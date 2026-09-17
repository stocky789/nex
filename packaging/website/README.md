<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Nex website hosting package

This package installs a static website service on a selected private listener. Pangolin
or another approved HTTPS proxy terminates browser TLS and forwards to that listener.
It does not configure DNS, certificates, firewall rules, SSH or proxy access policy.

Before installing nginx, inspect the selected listener with `ss -ltnp`. Do not stop an
unknown process to free a port; select the intended private listener or resolve its
ownership first. Mask the distribution service before package installation so its
default port-80 configuration cannot start, then install nginx, curl and the package
files. Create the dedicated account, render the configuration with a chosen unused
private `ADDRESS:PORT`, then validate it before starting the service:

```sh
sudo systemctl mask --runtime nginx.service
sudo apt install nginx curl
sudo systemctl mask nginx.service
sudo systemd-sysusers packaging/website/nex-website.sysusers
sudo install -D -m 0755 scripts/website-release.py /usr/local/lib/nex-website/website-release.py
sudo install -D -m 0755 scripts/website-health.sh /usr/local/lib/nex-website/website-health.sh
sudo install -D -m 0644 packaging/website/nginx.conf.template /etc/nex-website/nginx.conf
sudo sed -i 's/@NEX_WEBSITE_LISTEN@/PRIVATE_ADDRESS:PORT/' /etc/nex-website/nginx.conf
sudo install -D -m 0644 packaging/website/nex-website.service /etc/systemd/system/nex-website.service
sudo install -D -m 0644 packaging/website/nex-website-health.service /etc/systemd/system/nex-website-health.service
sudo install -D -m 0644 packaging/website/nex-website-health.timer /etc/systemd/system/nex-website-health.timer
sudo install -D -m 0644 packaging/website/nex-website.logrotate /etc/logrotate.d/nex-website
sudo install -d -o nex-website -g nex-website -m 0750 /run/nex-website /var/log/nex-website
```

Before starting it, create `/etc/nex-website/health.env` as root-owned mode 0600 with
`NEX_WEBSITE_URL=http://PRIVATE_ADDRESS:PORT/` and, if needed,
`NEX_WEBSITE_EXPECTED=Nex`.

For the first release only, bootstrap the root-owned release tree before the service
exists:

```sh
sudo /usr/local/lib/nex-website/website-release.py --no-health publish /path/to/approved-build RELEASE_ID
sudo systemctl daemon-reload
sudo -u nex-website nginx -t -c /etc/nex-website/nginx.conf
sudo systemctl enable --now nex-website.service nex-website-health.timer
```

Subsequent releases use the health-checked publish and rollback commands in the
[operator guide](../../docs/guide/operate-website.md). Verify every served file against
the approved build manifest after publishing or rollback; the health probe alone only
checks for a successful response containing the expected text. The health timer runs
every five minutes and records only to the journal. Logrotate runs daily, retains seven
rotations with older rotations compressed, and checks its 10 MiB `maxsize` threshold
when it runs; that threshold is not a disk quota.

## CI publish account

For the Release and Website workflows to publish without an interactive password, install
the publish wrapper and passwordless sudo for the same publishing account used by APT
bootstrap:

```sh
sudo NEX_PUBLISH_USER=<publishing-user> packaging/website/bootstrap-publish.sh
```

That installs `website-release.py`, `nex-website-publish`, and a sudoers rule so the
account may run only `sudo -n /usr/local/sbin/nex-website-publish`. Re-running is safe.
