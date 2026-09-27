# Nex

Nex is a community-driven virtualization project built on Debian, KVM/QEMU and libvirt.

- **Nex Core** is the host distribution, with a graphical installer, local management UI and agent.
- **Nex Controller** is the centralized management service, API and web UI for sites, pools and hosts.
  Each enrolled host becomes the pool master of its own automatically created pool.

## Development status

Nex is in early development. Current builds provide the Core installer, Controller
appliance deployment, administrator login, host enrollment, inventory and health
reporting, VM creation/editing/power/consoles, guest information and usage history.
The project owner has closed Phases 0–4. Phase 5, VM snapshots and rollback, is
next; independent backups and restores follow in Phase 6. New problems in the
closed phases are bugs, including the published 0.4.12 network installer's UEFI
failure and joins with differently named VLAN uplinks. The historical tests remain
bounded to their recorded environments. Fencing is the exception: its native and
physical safety acceptance remains open before automatic recovery can be claimed.
Migration, automatic VM HA and managed Docker applications follow later. Docker
services will run inside Nex-managed VMs.

Use disposable environments for evaluation. These are development builds, not a
production release; validation and redistribution gates remain open. The
[implementation plan](docs/plans/2026-09-08-nex-platform.md) tracks scope and acceptance,
and the [changelog](CHANGELOG.md) records delivered changes and verification limits.

## Build and use

- [User guide](docs/guide/README.md) — setup, operation and recovery.
- [Controller and agent development](docs/development.md) — prerequisites, builds and checks.
- [Core installer](installer/README.md) — ISO build and verification.
- [Controller appliance builder](appliance/README.md) — image preparation and signing.
- [Website](website/README.md) — SvelteKit source, local preview and contribution instructions.
- [API reference](docs/API.md) — implemented contracts and compatibility requirements.

## Contributing

Code, testing, documentation, design and website contributions are welcome.
Read the [contributor contract](AGENTS.md) and [documentation requirements](docs/DOCUMENTATION.md)
before making changes. Keep work focused on the current milestone and include the
relevant checks. The [product principles](PRODUCT.md) and [brand guide](docs/brand/README.md)
describe the shared direction.

## License

Original Nex code, build scripts and configuration use [AGPL-3.0-only](LICENSE).
Third-party software retains its own licenses; the ISO as a whole is not relicensed.
Nex names and artwork have separate terms. See [NOTICE](NOTICE) and the
[licensing and redistribution requirements](docs/LICENSING.md).
