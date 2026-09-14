# Controller and agent development

These commands build and check development builds; they do not install or enable services.
Operator setup is described in the [user guide](guide/README.md).

## Build and check

Use the checksum-pinned Go 1.27.1 security-build toolchain with CGO enabled, a C compiler
and libc development headers, Node.js 20 or newer with npm, Python 3, GNU Make, and tar.
The SQLite driver compiles its pinned SQLite amalgamation; no separate SQLite development
library is needed. Initial dependency retrieval requires internet access.

```sh
make build
make test
```

Override tool locations with `make GO=/absolute/path/to/go build`. `make build`
snapshots current source, vendors the Go dependency, and retains integrity-checked
npm package tarballs from the lockfile. It compiles and typechecks that snapshot,
producing `dist/nex-controller`, `dist/nex-core-agent`, `dist/web/`, `dist/api/`, and the matching
`dist/nex-source.tar.gz` plus SHA-256 sidecar. The archive's `SOURCE-MANIFEST.json`
records each input's digest. Keep these outputs together when running a controller.
Do not edit source during snapshot creation.

For quick edit/test cycles, `make binaries`, `make ui`, and `make typecheck` use the
working tree. Before making a build available to users, rerun `make build` so the
served source matches the binaries and UI. `make source` refreshes only the source
archive; it must not replace the archive accompanying an older running build.
The archive includes the UI's approved `nex-brand.png` and its separate artwork
terms and uses an explicit source-path/type allowlist. When adding build inputs,
update `scripts/build-source.py` in the same change.

## Rebuild from the download

```sh
mkdir nex-rebuild
tar -xzf nex-source.tar.gz -C nex-rebuild --strip-components=1
cd nex-rebuild
go build -mod=vendor -trimpath -o nex-controller ./cmd/nex-controller
go build -mod=vendor -trimpath -o nex-core-agent ./cmd/nex-core-agent
for package in third_party/npm/*.tgz; do npm cache add "$package"; done
cd web
npm ci --offline
npm run typecheck
npm run build
```

The vendor tree retains the SQLite driver's C source and license; npm tarballs retain
upstream notices. This rebuild still needs the tools listed above and does not promise
byte-identical output across toolchain versions. The dependency review is in
[controller-dependencies.md](controller-dependencies.md).

## systemd examples

The units in `packaging/systemd/` expect binaries in `/usr/local/lib/nex/`, built UI
in `/usr/local/share/nex/web/`, API documentation (the compiled snapshot's `docs/api/`)
in `/usr/local/share/nex/api/`, and the matching source archive in
`/usr/local/share/nex/nex-source.tar.gz`. They are examples for an explicitly selected
disposable Debian host, not an automatic installation target.

The controller runs as a systemd dynamic user with private persistent
`/var/lib/nex-controller`. Supply a server certificate, private key, and first
administrator password in `/etc/nex-controller/{tls.crt,tls.key,admin-password}`;
keep private files root-owned mode 0600 and the directory mode 0700. `LoadCredential`
gives the service private copies without exposing secrets in argv. Use a certificate
whose subject alternative name matches the controller URL and configure client trust
explicitly. New passwords need at least 16 Unicode characters and at most 1024 UTF-8
bytes; there is no shared default password.

The inventory agent example runs as root to inspect KVM access and query the libvirt
system socket (`virsh` from Debian's `libvirt-clients`). It writes only its mode-0700
state directory, does not control VM power, and does not install libvirt or alter host
networking. Enroll before starting it, and stop it before credential rotation or
re-enrollment because the state directory is exclusively locked. Unit syntax checking
does not prove a service starts or that libvirt access works under the sandbox.

## Controller appliance and host-local package

The appliance is a prepared Debian 13 amd64 qcow2, delivered with a separate
`nex-core-local` Debian package. See the [appliance builder](../appliance/README.md)
for base-image provenance, signing-key handling and the build command. Debian
cloud-image checksums are fetched over verified HTTPS; do not assume a detached
upstream signature exists. The Nex deployment manifest is independently signed.

After building the appliance and a matching complete source snapshot, build the host
package with an independently trusted raw Ed25519 public key:

```sh
python3 packaging/core-local/build.py --source dist/nex-source.tar.gz --bundle dist/appliance --trusted-key /secure/nex-release-public-key.raw --version 0.1.0+security1
```

This uses a Debian Docker builder and writes
`dist/core-local/nex-core-local_0.1.0+security1_amd64.deb`. Both builders accept `--docker` for
an executable wrapper. The package includes the signed appliance, local UI/service and
source; dependency installation may still need Debian repository access, so do not claim
empty-host offline installation from this package alone.

Install only on an explicitly selected disposable Debian 13 host using the
[operator procedure](guide/deploy-controller-appliance.md). The local service binds port
9443 on the default-route management IPv4 and creates `/var/lib/libvirt/images` if needed,
without formatting disks. Only an existing default-route bridge is selected automatically;
an explicit setup override selects an existing address/directory/bridge without changing
host networking. State lives in `/var/lib/nex/core-local`, configuration in
`/etc/nex/core-local.env` (JSON despite the suffix). It embeds its own enrolled agent; do not
run a second agent using `/var/lib/nex-core-agent`.

The guest appliance serves HTTPS on port 8443 and defaults to 2 vCPU, 4096 MiB RAM and
32 GiB disk, with a private NoCloud seed for static IPv4 and unique bootstrap credentials.
The package also installs `/usr/lib/nex/host-recovery` and
`/usr/lib/nex/core-local-setup prepare-recovery --request-file /secure/replacement.json` for
replacement preparation; see the operator guide for request fields and limits. Lab results
are in [appliance evidence](qa/appliance/lab.md).

## Web-security behavior

Controller and host-local authentication use `__Host-` session cookies, 30-minute idle expiry
and an eight-hour absolute lifetime, capped at 64 active sessions per service; login admission
is rate-limited per network peer. The contract is in the [API reference](API.md) and the
operator workflow in [Secure web access](guide/secure-web-access.md). Evidence:
[package](qa/security/package.md), [compiler](qa/security/compiler.md),
[browser](qa/security/browser.md) and [lab](qa/security/lab.md).

The Controller adds `sessions.idle_expires` to an existing database; old sessions expire and
account hashes remain. The earlier binary's session INSERT is incompatible with the upgraded
table, so a binary-only downgrade is unsupported: restore the matching pre-upgrade state snapshot
with its matching build. Preserve a protected snapshot before upgrade.

Not implemented: common/compromised-password screening, MFA, administrator rotation/recovery,
roles, security-event retention, managed certificate renewal and HSTS. Restrict development
services to private management networks or a VPN.
