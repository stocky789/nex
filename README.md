# Nex

Nex Core is our Debian-derived KVM host distribution, with its own release
identity, installer, package selection, defaults, and branding. Nex Controller provides centralized
management of sites, pools, and hosts; VM, storage, and application management follow later phases.

The **standalone Nex Core installer alpha** and **Phase 1 Controller/agent development
build** are separate deliverables. Controller supports administrator login, sites/pools,
secure host enrollment, inventory, health, and durable read-only refresh tasks. Installer
integration, VM management, backups, HA, and Docker services remain later work.

The new **Controller appliance development slice** adds a lightweight host-local
web UI, signed Debian cloud-image deployment, persistent progress, initial
enrollment/adoption, and guarded local appliance power controls. Controller needs
no installer ISO. See the [appliance setup guide](docs/guide/deploy-controller-appliance.md)
and its verification limits; this does not mark general VM management or the
complete host-installer integration phase finished.

## Build Controller and agent

With Go 1.24+, a C compiler/libc headers, Node.js 20+, npm, Python 3, Make, and tar:

```sh
make build
```

Keep the resulting binaries, web assets, API reference, and matching source archive together.
Use the [development instructions](docs/development.md) for build/check commands and the
[Controller operator guide](docs/guide/manage-controller.md) for HTTPS setup and enrollment.
This development build is for disposable lab evaluation; see each guide's verification limits.

## Build the ISO

On an amd64 Linux machine with Docker:

```sh
./installer/build.sh
```

The image is written to `dist/nex-core-0.1.0-alpha1-amd64.iso` with its checksum
and build inventory. Test in a disposable VM before using physical hardware.
Disk selection and the root password remain interactive; no ordinary user account is created.

- [Installer build, install, and verification instructions](installer/README.md)
- [User guide](docs/guide/README.md)
- [Documentation contribution and CI checks](docs/DOCUMENTATION.md)
- [Alpha ISO lab results and coverage limits](docs/qa/iso/2026-09-08-alpha1.md)
- [Implementation plan](docs/plans/2026-09-08-nex-platform.md)
- [Phase 1 scope and acceptance](docs/plans/2026-09-08-controller-foundation.md)
- [Feature and interface plan](docs/plans/2026-09-08-feature-and-ux-review.md)
- [Living API reference and contract requirements](docs/API.md)
- [Brand identity](docs/brand/README.md)
- [Licensing and release requirements](docs/LICENSING.md)

Original Nex code and build scripts use [AGPL-3.0-only](LICENSE). Bundled upstream
software retains its own licenses; see [NOTICE](NOTICE).
