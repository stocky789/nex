# Product

## Register

product

## Users

Virtualization operators installing Nex Core hosts and managing them through Nex Controller.
See the [platform model](docs/plans/2026-09-08-nex-platform.md).

## Product Purpose

Install a recoverable Debian-based KVM/libvirt host, then manage Sites, Pools, Hosts and VMs.
The installer must make its target and destructive consequences clear before writing disks.

## Brand Personality

Clear, calm and practical. Preserve the approved [Nex identity](docs/brand/README.md).

## Anti-references

The stock Debian graphical installer is not the custom Nex GUI requested by Matt.
Use Proxmox's guided installation as workflow inspiration without copying branding.

## Design Principles

- Keep installation choices understandable and reviewable.
- Use Nex's own screens throughout the graphical workflow.
- Preserve proven platform behavior and honest failure reporting.
- Keep the minimal text installation path available.

## Accessibility & Inclusion

Keyboard navigation, visible focus, readable contrast, labeled controls and explicit error
messages are required by the [contributor contract](AGENTS.md). Do not rely on color alone.
