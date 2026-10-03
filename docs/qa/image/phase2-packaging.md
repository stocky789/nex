# Phase I-2 packaging, wizard and docs closure: record

Date: 2026-10-01. Plan: [Immutable Nex Core](../../plans/2026-10-01-immutable-nex-core.md), I-2. Siblings:
[build](phase2-build.md), [layers](phase2-layers.md), [installer](phase2-installer.md).
**Environment: simulation only / script level.** No VM was booted, no package or ISO was built, and nothing was run on
physical hardware.

API impact: new on-host command contract (`nex-core-local layers`, `LayerInfo`, `layer-unavailable`); no HTTP route,
report command or Inventory field. State owner: `core` (`/var/lib/nex/layers/enabled.json`;
`/var/lib/nex/payload/<sha256>/` for the Controller system image only). Change record: `docs/changes/2026-10-01-image-layers-packaging.json`.

## What changed

1. **`nex-layers.service` is installed.** `packaging/core-local/build-inside.sh` installs the unit and a vendor
   `sysinit.target.wants/nex-layers.service` link in `/usr/lib/systemd/system`, and
   `30-nex-core-local.preset` (installed to `/usr/lib/systemd/system-preset/`) says `enable nex-layers.service`.
   Enablement ships in `/usr`, not in `postinst`, so every slot of a verified image carries it. The unit's
   `ConditionPathExists=/var/lib/nex/layers/enabled.json` keeps it inert on any host, including every Classic host,
   that never enabled a layer. `cmd/nex-core-local/main.go` already routes `layers` to `layers.go`.
2. **Graphical wizard Repair image.** `installer/gui/nex-wizard.c` now mirrors the text wizard: image edition only
   (media `edition` file, not `nex.edition=classic`), asked only when `nex-image-install.sh probe` reports a complete data
   volume, ticks exactly the disks it names and cannot be changed, skips the host and disk pages, writes
   `Install.mode=repair`, shows its own review and confirmation text. The disk floor is 24 GiB on the image edition
   and 16 GiB on Classic. `installer/check.sh` pins the shared contract.
3. **Appliance split (removed).** This slice added `build.py --appliance-packaging split`, which wrote the appliance beside the
   deb. It was removed again in the review fixes: the package always carries the appliance, and the image edition takes the
   Controller image from the release's `vm-image` component. `core-local-setup` reads
   `/var/lib/nex/payload/<sha256>/` only for `configure --controller-image-sha256` (the image edition's Controller system image),
   not as a fallback for the appliance; a digest that is not a SHA-256 is ignored, so no path leaves the payload store.
4. **Docs.** Layer contract in `docs/API.md` and `docs/api/local-openapi.json` (`x-commands` entry, `LayerInfo`);
   `docs/api/local.html` regenerated; guide page `docs/guide/optional-host-features.md`; CHANGELOG `[Unreleased]`.
5. **Integration pins.** `tests/integration/installer-handoff.py` now asserts the image-edition hook: preflight, then
   `install` with no disk cleared by the hook (the image installer verifies first, then clears), Repair clears nothing
   and runs `repair`, classic media never runs the
   image installer and keeps its partman markers, and a net installer asks for the `image/ payload/` rows only.
   `tests/integration/installer-disk.py` follows the edition floor (`disk_floor()`), and its harness covers 24 GiB,
   UEFI refusal and Repair (previous pass).

## Checks actually run (2026-10-01)

- `python3 scripts/generate_api.py`, then `--check`: all three references current. `tests/test_api_contract.py`: 74 tests OK.
- `python3 -m unittest tests.test_storage_packaging tests.test_core_local_package`: 43 tests OK
  (when run, this included the since-removed appliance split tests).
- `sh installer/check.sh`: all PASS. `bash -n`/`sh -n` on `build-inside.sh` and `recheck.sh`.
- Smoke of the `build-inside.sh` appliance block against a stub bundle (when it still had a `split` mode, now removed).
- `tests/integration/installer-handoff.py`: all PASS, including the new image-edition case.
- Throwaway `debian:trixie-slim` container (apt `gcc pkg-config libglib2.0-dev libgtk2.0-dev xvfb xdotool`):
  `installer/gui/nex-wizard.c` and `nex-config.c` compile with the project's flags and no warnings, and
  `tests/integration/installer-disk.py` passes under Xvfb, including the image scenarios.

## Not run / limits

- The wizard's Repair choice was compiled and pinned by text, never drawn or clicked: no GUI walk-through exists.
- No `nex-core-local` deb was built (Docker build of the package not run), so the vendor wants link in a real package is
  checked by script and tests only. `installer/build.sh` reads the appliance manifest from the deb for the classic edition.
- `nex-layers.service` was shown to merge and verify in the lab VM in [layers](phase2-layers.md); the packaged
  unit, preset and wants link were not booted.
- Nothing calls `layers ensure` from a ZFS or cluster operation, and nothing places layer files on a host.
