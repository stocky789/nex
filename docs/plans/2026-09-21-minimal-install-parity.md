# Minimal installation parity with the graphical wizard

## Problem

The ISO offered two installation modes that produced two different hosts.

**GUI install** booted `install.amd/gtk/initrd.gz`, which carries the Nex overlay: the
GTK wizard, `nex-config`, the hardware `prepare` step, the partman identity recheck, the
bundled timezone list and an embedded safe seed. **Minimal install** booted the untouched
Debian `install.amd/initrd.gz` with `preseed/file=/cdrom/preseed.cfg`. That preseed only
sets the root-only account policy and the shared late command, so minimal was Debian's own
text installation with the Nex package payload applied afterwards.

Everything the wizard owns was therefore missing from minimal:

| | GUI | Minimal (before) |
|---|---|---|
| Configuration | Nex wizard | Debian `netcfg`, `partman`, `grub-installer` questions |
| Network | per-NIC IPv4 and one VLAN, MAC-bound `nexN`, `br0` management bridge | Debian networking, kernel interface names, no bridge |
| Disk | writable nonremovable whole disk ≥ 16 GiB, never the media, named erase confirmation, identity recheck before partitioning | raw `partman`, any disk |
| Root password | ≥ 8 characters with confirmation | blank rejected only |

`installer/late-install.sh` copies the staged `/etc` only when `/run/nex-installer/active`
exists, which only the wizard creates, so a minimal installation silently produced a host
with different networking from the one the documentation described.

Decision: project owner, 2026-09-21 — minimal installation runs the same wizard in a text
interface and must install the same host. Branding-only, gate-only and removing the mode
were the rejected alternatives.

## Approach

`nex-config` is already frontend-independent: it links glib and libcrypt only, reads
`/run/nex-installer/config.ini` and emits both the staged `/etc` and the debconf answers.
The GTK wizard is only one producer of that file, so a text producer gives parity without a
second validator or a second definition of the installed host.

Two facts about the pinned Debian 13 installer decided the shape:

- `install.amd/gtk/initrd.gz` contains `newt.so` and `text.so` as well as `gtk.so`, and
  `/usr/lib/debian-installer.d/S62Xorg` starts Xorg only when the frontend is `gtk`. Booting
  that initrd with `DEBIAN_FRONTEND=newt` therefore gives a text installation with every Nex
  file and library already present.
- `install.amd/initrd.gz` has no glib, so `nex-config` could not run there without bundling
  libraries into the initrd and carrying their corresponding source separately.

Minimal install therefore boots the same initrd as the GUI with `DEBIAN_FRONTEND=newt`.

## Delivered

- `installer/gui/wizard-text.sh` and `installer/gui/wizard-text.templates`: the text wizard,
  installed as `/usr/lib/nex-installer/wizard-text`. It runs the shared `prepare` step, asks
  host name, keyboard, timezone area and zone, root password and confirmation, management
  adapter, each adapter's IPv4 mode, VLAN, address and gateway, DNS, and the installation
  disk, then a review and erase confirmation that defaults to no. It supports **Go Back**,
  offers only supported disks, and writes the same `config.ini`.
- `installer/gui/start.sh`: one setup loop drives whichever frontend is running; validation,
  staging and the seed import are unchanged and shared.
- `installer/build-gui.sh`: installs the two new files into the overlay and fails the build
  if the graphical initrd stops carrying the text frontend.
- `installer/build.sh`: the minimal BIOS and UEFI entries boot `install.amd/gtk/initrd.gz`
  with `DEBIAN_FRONTEND=newt` and the initrd seed instead of the media file seed.
- `installer/check.sh` and `tests/integration/installer-text-wizard.py`: the source-level
  contract and a fixture-driven run of the wizard.

## Scope held back

- **Serial and rescue keep the untouched Debian initrd and the media seed.** The serial entry
  still runs Debian's own questions. The text wizard would work over a serial console, but
  that combination is untested here and changing it is a separate decision.
- **No review-and-edit screen.** The text wizard reviews every setting on the confirmation
  question and relies on **Go Back** for corrections, rather than reproducing the GUI's
  free navigation between pages.
- **Per-NIC DHCP lease preview is graphical only.** `dhcp-preview` stays with the GTK wizard;
  the text wizard reports link state when choosing the management adapter.

## Verification

`installer/check.sh`, the installer regression suite and
`tests/integration/installer-text-wizard.py` pass, and the generated handoff was decoded with
GKeyFile exactly as `nex-config` reads it. All of that is source-level.

Outstanding: a built ISO booted through the minimal entry, an installation completed from it,
and the installed `br0`, `nexN` names, VLAN and DNS compared against a graphical installation
of the same settings. Until that runs, parity is implemented and checked, not demonstrated.

API impact: none. No Controller/Core contract is involved; the installer produces a host, and
the wizard handoff is a private file consumed by `nex-config` in the same initrd.
