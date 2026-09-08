# Nex

Nex Core is our Debian-derived KVM host distribution, with its own release
identity, installer, package selection, defaults, and branding. Nex Controller will provide centralized
management of sites, pools, hosts, VMs, storage, and applications.

The current implementation is the first **standalone Nex Core installer alpha**.
Controller, enrollment, pools, backups, HA, and Docker services remain planned.

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
- [Feature and interface plan](docs/plans/2026-09-08-feature-and-ux-review.md)
- [Living API reference and contract requirements](docs/API.md)
- [Brand identity](docs/brand/README.md)
- [Licensing and release requirements](docs/LICENSING.md)

Original Nex code and build scripts use [AGPL-3.0-only](LICENSE). Bundled upstream
software retains its own licenses; see [NOTICE](NOTICE).
