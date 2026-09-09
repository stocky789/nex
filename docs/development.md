# Controller and agent development

Phase 1 supplies development builds and example systemd units. Debian packaging and
installer delivery have separate gates; these commands do not install or enable services.
Operator setup is described in the [user guide](guide/README.md).

## Build and check

Use the checksum-pinned Go 1.27.1 security-build toolchain with CGO enabled, a C compiler
and libc development headers,
Node.js 20 or newer with npm, Python 3, GNU Make, and tar. The SQLite driver compiles
its pinned SQLite amalgamation; no separately installed SQLite development library is
needed. Initial dependency retrieval requires internet access.

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
The archive includes the UI’s approved `nex-brand.png` and its separate artwork
terms; it excludes unrelated installer assets, private runtime files, caches, and unrelated
working-tree files by an explicit source-path/type allowlist. When adding build inputs,
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

The vendor tree retains the SQLite driver's C source and license. npm tarballs retain
upstream package contents and notices. This rebuild still needs the compiler/runtime
tools listed above; it does not promise byte-identical output across toolchain versions.
The dependency review is in [controller-dependencies.md](controller-dependencies.md).

## systemd examples

The units in `packaging/systemd/` expect binaries in `/usr/local/lib/nex/`, built UI
in `/usr/local/share/nex/web/`, API documentation in `/usr/local/share/nex/api/`, and
the matching source archive in `/usr/local/share/nex/nex-source.tar.gz`. Copy the
compiled snapshot's `docs/api/` for the API documentation, not another revision.
These are deployment examples for an explicitly selected disposable Debian host,
not an automatic installation target.

The controller runs as a systemd dynamic user. systemd creates its private persistent
`/var/lib/nex-controller` directory. Supply a server certificate, private key, and first
administrator password in `/etc/nex-controller/{tls.crt,tls.key,admin-password}`;
keep private files root-owned mode 0600 and the directory mode 0700. `LoadCredential`
gives the service private readable copies without exposing secrets in argv. Use a
certificate whose subject alternative name matches the controller URL and configure
client trust explicitly. New passwords must contain at least 16 Unicode characters and at most 1024 UTF-8 bytes; there is no
shared default password. Preserve service state across restarts.

The inventory agent example runs as root to inspect KVM access and query the existing
libvirt system socket. It writes only its mode-0700 state directory, does not control
VM power, and does not install libvirt or alter host networking. Unprivileged agent
operation is not established by this example. Enroll before starting it, and stop it
before credential rotation or re-enrollment because the state directory is exclusively
locked. Its required `virsh` comes from Debian's `libvirt-clients`.

Review unit paths and local certificate trust before manual lab installation. Unit
syntax checking does not prove a service started or that libvirt access works under
the sandbox; actual systemd deployment remains a disposable-host acceptance check.

API impact: existing contract — build output supplies the controller's source-download
and API-documentation files; units invoke the Phase 1 controller and agent CLI contracts.
Guide impact: updated — the Phase 1 guide documents running and enrolling these builds.


## Controller appliance and host-local package

The appliance-first development slice has a prepared Debian 13 amd64 qcow2 and a
separate `nex-core-local` Debian package. It does not install software onto this
workstation or modify the existing Core ISO. See the [appliance builder](../appliance/README.md)
for base-image provenance, private signing-key handling and the exact build command.
Current Debian cloud-image checksums are fetched over verified HTTPS; do not assume
a detached upstream signature exists. The Nex deployment manifest is independently signed.

After building the appliance and a matching complete source snapshot, build the host
package with an independently trusted raw Ed25519 public key:

```sh
python3 packaging/core-local/build.py --source dist/nex-source.tar.gz --bundle dist/appliance --trusted-key /secure/nex-release-public-key.raw --version 0.1.0+security1
```

This uses a Debian Docker builder and writes
`dist/core-local/nex-core-local_0.1.0+security1_amd64.deb`. Both builders accept `--docker` for
an executable wrapper where the lab needs one. The package includes the signed
appliance, local UI/service and source; initial dependency installation may require
Debian repository access unless dependencies are already available locally. Do not
claim empty-host offline installation from this package alone.

Install/configure only on an explicitly selected disposable Debian 13 host using
the [operator procedure](guide/deploy-controller-appliance.md). The local service
autostarts on systemd targets and discovers the default-route management IPv4, binding
port 9443. It creates `/var/lib/libvirt/images` if needed, without formatting disks.
Only an existing default-route bridge is selected automatically; missing bridge blocks
deployment while retaining local access. An explicit setup override selects an existing
address/directory/bridge without changing host networking. Its state lives
in `/var/lib/nex/core-local`, with configuration in `/etc/nex/core-local.env`
(JSON despite the suffix). It supplies its own embedded enrolled agent; do not run
a second agent using `/var/lib/nex-core-agent`.

The guest appliance serves HTTPS on port 8443 and defaults to 2 vCPU, 4096 MiB RAM
and 32 GiB disk. A private NoCloud seed supplies static IPv4 settings and unique
bootstrap credentials. The host persists deployment intent before VM creation and
reconciles enrollment/adoption against the existing VM identity. Seed cleanup is
part of completion. Inspect [lab evidence](qa/appliance/lab.md) for actual checks,
including any outstanding systemd, offline boot, browser and recovery gates.

API impact: new contract — authenticated host-local setup/control, durable deployment
results and Controller bootstrap/adoption extend existing enrollment/observation.
Guide impact: updated — separate-package installation, deployment and local recovery
are documented with explicit development and verification limits.

The package also installs `/usr/lib/nex/host-recovery` for host-root guest-agent
export/import. Empty replacement preparation uses
`/usr/lib/nex/core-local-setup prepare-recovery --request-file /secure/replacement.json`
with local management stopped, an un-enrolled clean replacement host, and a private
0600 request. It creates no Site or enrollment (pools are only created when a host enrolls). See the operator guide for exact
request fields, external-original exclusion, source matching and verification limits.


## Web-security development changes

Controller and host-local authentication now use `__Host-` session cookies, 30-minute
server-side idle expiry and an eight-hour absolute lifetime. Only requests passing
authentication and CSRF checks with methods other than GET/HEAD refresh idle expiry;
background polling does not. Old sessions require reauthentication after upgrade, while
existing account hashes remain unchanged. Each service caps active sessions at 64.

Login admission validates request shape before acquiring bounded password-hash work.
It limits each actual network peer to five admitted checks in a 60-second window, with
a separate limit on concurrent expensive checks. Forwarding headers do not identify
peers; a NAT or reverse proxy can therefore share the limit across users. The API can
return 429 for peer, concurrency or session capacity and does not emit Retry-After.
Newly provisioned passwords require 16 Unicode characters and at most 1024 UTF-8 bytes.

Use the [operator security page](guide/secure-web-access.md) for the corresponding
upgrade/login workflow and deployment assumptions. Restrict these development services
to private management networks or a VPN; this slice is not direct-Internet support or
ASVS/NIST certification. Browser HSTS is deliberately absent pending a managed trusted
certificate/renewal/recovery path. Core ISO integration remains separate.

API impact: changed contract — session cookies, expiry/admission and bootstrap password
validation change on the existing Controller and local APIs. Guide impact: updated.
The [security package evidence](qa/security/package.md), [compiler checks](qa/security/compiler.md)
and [browser walkthrough](qa/security/browser.md) record actual security1 build/scanner/smoke
and authentication checks. [Native nested-KVM results](qa/security/lab.md) cover
clean deployment, enrollment, cleanup, service/guest recovery and actual outer-host reboot
with retained login/TLS/source/management identity. The older complete `0.1.0+lab6.1`
separate-host replacement-restore results are not security1 results.
Preserve matching artifact/source sets and identify exact toolchains in build evidence.

The Controller adds `sessions.idle_expires` to an existing database. Old session rows
expire immediately; account hashes remain. The earlier binary's four-column session
INSERT is incompatible with that upgraded table. A binary-only downgrade is unsupported:
restore the matching pre-upgrade state/identity snapshot with its matching build, excluding
the upgraded instance before resuming the original identity. Preserve a protected snapshot
before upgrade and keep newer state separately when rolling back.

Common/compromised-password screening is not implemented. Length validation and hashing
do not establish complete NIST alignment or an ASVS verification level. MFA, administrator
rotation/recovery, roles, security-event retention and managed certificate renewal remain
production requirements; no online breach-query mechanism is claimed.
