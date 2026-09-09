<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Nex APT repository (private website host)

## Interim lab serve (no root)

Until nginx `/apt/` is bootstrapped, the maintainer can serve `~/nex-apt` on the
private host:

```sh
cd ~/nex-apt && python3 -m http.server 8081 --bind 172.16.4.3
```

Clients and in-product updates default to `http://172.16.4.3:8081` in this interim
layout. Publish with:

```sh
python3 scripts/apt-publish.py --repo-root ~/nex-apt --gpg-home ~/nex-apt-gnupg path/to/*.deb
```

## Website listener (`/apt/`)

One-time bootstrap on the website host as root (from a checkout that includes this tree):

```sh
sudo packaging/apt/bootstrap.sh
sudo rsync -a --delete /home/matt/nex-apt/ /srv/nex-apt/
sudo nex-apt-publish
```

That creates `/srv/nex-apt`, the signing key under `/etc/nex-apt/gnupg`, installs
`/usr/local/sbin/nex-apt-publish`, merges the nginx `/apt/` location when
`/etc/nex-website/nginx.conf` exists, and allows `matt` to run the publisher via
sudoers. After bootstrap, point clients at `http://172.16.4.3:8080/apt`.

Publish packages:

```sh
sudo nex-apt-publish /path/to/nex-controller_0.1.0+upd1_amd64.deb \
  /path/to/nex-core-local_0.1.0+upd1_amd64.deb
```

Client sources entry for the private listener (example):

```text
deb [signed-by=/usr/share/keyrings/nex-archive-keyring.gpg] http://172.16.4.3:8080/apt nex main
```

Install the keyring from `http://172.16.4.3:8080/apt/pubkey.gpg` (dearmor into
`/usr/share/keyrings/nex-archive-keyring.gpg`). Do not assume a public DNS name;
use the private host and port you actually serve.
