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

## Pre-merge review round, 2026-09-21

Review of the delivered text wizard found fourteen defects (four of them in the wizard's own
behaviour, recorded as BUG-0407 to BUG-0410 in [the bug ledger](../BUG_LEDGER.md)). Fixing them
settled these scope decisions; none of them changes the invariant, the API surface or where
state lives.

- **Offline parity in text mode.** The management question now lists the adapters first and
  `Offline (no management network)` last, exactly as the graphical wizard offers it, and writes
  the same empty `management=` handoff. A machine with no adapter shows a note and installs
  offline instead of the wizard exiting, which previously left `start.sh` restarting it forever.
  The text mode is not allowed to refuse an installation the graphical mode accepts.
- **The wizard validates before it finishes.** After the erase confirmation the wizard writes
  `config.ini` and has `nex-config` validate it (a single `CONFIG` argument validates only).
  A rejection removes the file, shows `nex-config`'s own reason and returns to the questions
  with every answer retained, instead of surfacing one fixed string from `start.sh` and
  restarting the wizard with everything cleared. `nex-config --apply` stays solely in
  `start.sh`, which now also reports `nex-config`'s own message rather than the fixed string.
  This adds no second validator: `nex-config` remains the only definition of a valid host.
- **Hardware preparation runs before the debconf frontend.** `prepare` drives `cdrom-detect`,
  `anna` and `hw-detect`, which own the screen themselves. It therefore runs on the wizard's
  first pass, before `confmodule` re-executes the script under cdebconf, so a backend question
  can no longer appear on top of a Nex question. The Nex progress bar around it is gone.
- **Sorted adapter order in both frontends.** `nex0..nexN` are assigned by the order adapters
  appear in the handoff, so the graphical wizard now sorts `/sys/class/net` the way the text
  wizard's glob already did. The same machine gets the same interface names in either mode.
- **The keyboard layout is a gate, not a best effort.** The console keymaps carry no bare `fr`
  map, and `ckbcomp` exits 0 after printing only its headers, so the previous best-effort apply
  left the console on US while the root password was typed. The layout now counts as applied
  only when the map expands, contains key definitions and loads; otherwise the wizard reports it
  and asks again, matching the graphical wizard's keyboard gate. The handoff still records the
  bare layout name.

API impact: none. No Controller/Core contract, agent command, event or stream is involved;
every one of these is installer-local behaviour between the wizard, `nex-config` and the
Debian installer seed. Durable state owner: none — the handoff lives in tmpfs and is consumed
during the installation, and the installed host's own records remain the authority.

## Scope held back

- **Serial keeps the untouched Debian initrd and the media seed; rescue keeps neither.** The
  serial entry still runs Debian's own questions from `preseed/file=/cdrom/preseed.cfg`. The
  rescue entry boots the same untouched initrd with no preseed at all, so it reaches a shell
  without installation answers preloaded. The text wizard would work over a serial console, but
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

After the review round, `python3 tests/integration/installer-text-wizard.py` runs 28 scenarios
(13 wizard scenarios under both `/bin/sh` and `busybox ash`, plus a template cross-check and an
ASCII check) and passed on 2026-09-21, with the handoff decoded and validated by the real
`nex-network.c` in the four scenarios that complete a handoff worth validating
(`default_answers`, `navigation_and_handoff`, `offline_management`, `no_network_adapter`); the
other scenarios compare the handoff text only. Its question system, keyboard tools and validator are
scripted stand-ins, so this is a simulation of the wizard's logic, not of the installer console.
The two new `installer/check.sh` boot-menu assertions were mutation-checked: seeding the BIOS
rescue entry fails with `rescue must boot Debian unseeded`, and removing the seed from the BIOS
serial entry fails with `the serial install needs the media seed`; the previous unscoped
assertion passed both mutations. The graphical wizard's sorted enumeration was checked by
compiling it with the build's own `cc` line and comparing the comparator's order with a C-locale
glob of the same names, and `installer/check.sh` now asserts that sort is present (BUG-0411) so a
revert fails CI; `tests/integration/installer-network.c` still passes.

The scenarios were also run red: with `wizard-text.sh` and `wizard-text.templates` restored from
the pre-fix commit into a scratch tree, the committed check exits 1 and fails eleven of the
fifteen names it reports, including every defect the review round fixed. `busybox` was added to
both CI workflow copies so the `busybox ash` lane cannot silently skip on a runner without it.

Outstanding: a built ISO booted through the minimal entry, an installation completed from it,
and the installed `br0`, `nexN` names, VLAN and DNS compared against a graphical installation
of the same settings. Until that runs, parity is implemented and checked, not demonstrated.
Specifically untested by the simulation: newt's real rendering of the questions and of the
`Adapters:` review line, `ckbcomp`/`loadkeys` against a real virtual console (only the initrd's
`ckbcomp` was run, read-only, confirming `fr` alone yields no key definitions and `fr latin9`
yields 252), `loadkeys` against a console that is not `K_UNICODE` — the reason the map is loaded
with `-u` was read from `loadkeys`' own behaviour, not measured on a real VT — real NVMe and SCSI
hardware behind the trimmed sysfs values, and the graphical wizard, for which only a source
assertion covers `discover()`'s ordering and nothing runs it. The `start.sh` apply-failure
branch is covered by `tests/integration/installer-handoff.py`: a stubbed `nex-config` that rejects
once must have its own reason shown on the next wizard pass, kept in the log, and leave no
capture file; the scenario fails against the previous `start.sh`.

API impact: none. No Controller/Core contract is involved; the installer produces a host, and
the wizard handoff is a private file consumed by `nex-config` in the same initrd.
