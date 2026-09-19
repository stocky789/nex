<!-- SPDX-License-Identifier: AGPL-3.0-only -->

# Nex APT repository

Configured client URL: `https://nexvirtualization.com/apt`. Confirm that a specific
published package/version is present before treating the archive as available; an
unreleased working-tree version is not an APT candidate.

The tree is served from the website host (`/srv/nex-apt`) through nginx `/apt/`
and the Pangolin route for nexvirtualization.com.

## One-time bootstrap (website host, as root)

```sh
sudo NEX_PUBLISH_USER=publisher packaging/apt/bootstrap.sh
```

Creates `/srv/nex-apt`, signing key under `/etc/nex-apt/gnupg`, installs
`/usr/local/sbin/nex-apt-publish`, merges the nginx `/apt/` location, and allows
the account in `NEX_PUBLISH_USER` to run the publisher via sudoers.

## Publish packages

```sh
sudo nex-apt-publish /path/to/nex-controller_….deb \
  /path/to/nex-core-local_….deb
```

In-product updates default to `https://nexvirtualization.com/apt` via
`/etc/nex/updates.conf` (`repo_url=`) or `NEX_UPDATE_REPO`.

Optional APT sources entry:

```text
deb [signed-by=/usr/share/keyrings/nex-archive-keyring.gpg] https://nexvirtualization.com/apt nex main
```

Install the keyring from `https://nexvirtualization.com/apt/pubkey.gpg` (dearmor
into `/usr/share/keyrings/nex-archive-keyring.gpg`).
