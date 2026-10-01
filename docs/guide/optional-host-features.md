# Check and switch optional host features (image edition)

## Status

Development build, **image edition only**, operated from the host's own root shell. Optional features such as
OpenZFS storage (`nex-zfs`) and the cluster stack (`nex-cluster`) are separate signed **layers**: the system image
stays small and a host that never uses a feature does not carry it. The layer manager is implemented in Nex Core
and exercised in a disposable QEMU/KVM guest; it has not run on a booted installed image, on Secure Boot, or on
physical hardware. The Classic edition has no layers and ignores this page.

Not available yet, so do not expect it: a Controller screen or API for layers, automatic enabling when you create a
ZFS pool, and any installer or update step that places the layer files on the host. The `nex-zfs` layer is for internal
lab use only until the project owner decides on redistributing prebuilt OpenZFS modules (see
[Licensing](../LICENSING.md)). `nex-cluster` has been written but not built.

## Prerequisites

- An image-edition host you can log in to as root (console or SSH).
- The layer file already in the host's layer store, `/var/lib/nex/layers/store/sha256-<digest>.raw`, next to the signed
  release manifest for the running image under `/var/lib/nex/layers/releases/<build id>/`. Nothing places them
  automatically yet; a lab operator copies them.
- No virtual machine or storage pool depends on the feature you intend to switch off.

## Steps

1. List what the host knows about:

   ```sh
   /usr/lib/nex/nex-core-local layers list
   ```

   Each entry reports `enabled` (wanted), `merged` (active now), `available` (signed and present in the store) and
   `verified` (re-checked at the last boot), plus a `reason` when something is wrong.
2. Enable a feature, for example OpenZFS:

   ```sh
   /usr/lib/nex/nex-core-local layers ensure nex-zfs
   ```

   Core re-checks the file against the signed manifest, records the choice, merges the layer and starts its units.
   Running it again changes nothing. It normally takes effect at once; if it answers `reboot_required` (another layer
   holds the overlay and `/usr` is busy), restart the host and the layer merges early in boot.
3. Switch a feature off when nothing uses it:

   ```sh
   /usr/lib/nex/nex-core-local layers disable nex-zfs
   ```

   The choice is recorded at once and the layer's units stop; the files leave `/usr` at the next reboot.
4. After a reboot, run `layers list` again. An enabled layer is re-verified by `nex-layers.service` before the merge; a
   layer that fails the check is not linked for it, is reported in `/run/nex/layers/status.json`, and never blocks boot.

## Expected result

`layers list` shows the layer `enabled` and `merged` after step 2, and `enabled: false` after step 3 (still `merged`
until the reboot). The choice survives reboots and a rollback of the system image, because it is kept on the host's data
volume in `/var/lib/nex/layers/enabled.json`; nothing is stored by a Controller, so a replacement Controller changes
nothing here.

## Troubleshooting

- **`layer-unavailable: NAME: not_in_store`.** The layer file is not on this host. Place it, then run `ensure` again.
- **`base_mismatch`.** The layer was built for a different system image build. A layer only works with the exact build
  it was made for; use the layer that belongs to the running image. After a rollback this is expected for the newer
  base's layer.
- **`digest_mismatch` or `signature_invalid`.** The stored file is not the signed one, or the release manifest is missing
  or not signed by the release key. The layer is refused and not linked for the next merge; a layer that is already merged
  stays until the host restarts, and `layers list` shows it as not verified. Replace the file; do not work around the check.
- **`in_use`.** Something still uses the feature, for example an imported ZFS pool or a loaded module. Export or stop
  that first. `disable` is refused rather than pulling files from under a running service.
- **`merge_failed`.** `systemd-sysext` refused the layer or a unit did not start; the detail follows the code.
  Check `systemd-sysext status` and `journalctl -u nex-layers.service`.
- **A ZFS pool is not imported after a reboot.** The layer merged and the modules loaded in the lab, but automatic pool
  import at boot was not shown to work. Import the pool by hand (`zpool import`).

## Verification

Exercised in a disposable guest only (direct kernel boot, no Secure Boot): `ensure nex-zfs` merged the layer and loaded
the modules, `disable` was refused while a pool existed, a reboot re-verified and merged the layer, and a flipped byte in
the stored file was refused as `digest_mismatch` by `layers activate`. In that test the layer, merged earlier in the same
boot, stayed merged and `layers list` showed it as `merged` but not `verified`: the check protects the next merge, it does
not remove a running layer. Not exercised: a reboot with a tampered file, an installed image edition, Secure Boot module
enforcement, the `nex-cluster` layer, double-build reproducibility, and the steps above on a real host. See the
[layers QA record](../qa/image/phase2-layers.md) and the [command contract](../API.md#optional-feature-layers-nex-core-local-layers).
