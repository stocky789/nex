# Product principles

Nex is a community-driven project for people building and operating virtualized
infrastructure. Nex Core provides the Debian-based KVM/QEMU and libvirt host;
Nex Controller provides centralized management through a web UI and API. It is replaceable:
each pool master keeps the durable copy of its pool's settings, and a new Controller re-adopts
them when the master is connected to it.
The [implementation plan](docs/plans/2026-09-08-nex-platform.md) owns feature scope,
build order and acceptance gates.

## Product model

- Organize infrastructure as Sites → Pools (one master host + members) → Hosts → VMs.
  Pools are created automatically: each enrolled host becomes the pool master of its
  own pool. VMs belong to pools with a separate host assignment, preserving identity
  across migration.
- Keep running workloads and local recovery independent of Controller availability.
- Build on Debian and established virtualization tools. Debian remains the source of packages; Nex hosts are
  signed, read-only images assembled from them, not systems an administrator maintains with `apt`
  (project owner decision, 2026-10-02; [plan](docs/plans/2026-10-01-immutable-nex-core.md)).
- Plan Docker application management through Controller, with Docker Swarm inside
  Nex-managed VMs. This follows the VM foundation.

## Interface principles

- Use clear, calm language and the approved [Nex identity](docs/brand/README.md).
- Make choices understandable and reviewable, especially destructive actions.
- Show failures and stale or unknown state honestly.
- Provide original Nex graphical installer screens and retain the minimal text path.
- Support keyboard navigation, visible focus, readable contrast, labeled controls
  and explicit errors. Never rely on color alone.

The [contributor contract](AGENTS.md) defines implementation and safety requirements;
the [user guide](docs/guide/README.md) describes available workflows and their limits.
