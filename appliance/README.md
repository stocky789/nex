# Controller virtual appliance builder

Development tooling: a prepared Debian 13 amd64 genericcloud qcow2, customized with
cloud-init, qemu-guest-agent, Nex Controller, its UI/API reference and matching source.
No Controller ISO or interactive Debian installer is used. This is a local build,
not a redistribution certification. Complete Debian corresponding sources, base-image
component checksums/origins, firmware and artwork review remain release gates.
Package copyright files remain inside the guest; `components.tsv` inventories installed
package and source versions but is not a complete source-delivery bundle.

## Build

Download a versioned Debian 13 **genericcloud** amd64 qcow2 and its `SHA512SUMS`
from [Debian's HTTPS cloud image server](https://cloud.debian.org/images/cloud/).
Current cloud checksums use TLS origin authentication; detached signatures are only
published for legacy OpenStack images. Verify the exact file against published SHA512
then compute its SHA256 and pass that pinned digest to the builder. Retain the
versioned URL and upstream checksum as build evidence. Never use an unchecked download.
Do not use Debian's `nocloud` image: it lacks the desired cloud-init configuration.

Generate a release signing key outside this repository and keep it private:

```sh
umask 077
openssl genpkey -algorithm ED25519 -out /secure/nex-appliance-signing.pem
python3 appliance/build.py --base-image /cache/debian-13-genericcloud-amd64.qcow2 \
  --base-sha256 VERIFIED_IMAGE_SHA256 --version DEVELOPMENT_VERSION \
  --signing-key /secure/nex-appliance-signing.pem --output dist/appliance
```

Docker needs permission to run a Debian builder. `--docker` can name a wrapper that
executes `sudo -n docker "$@"`. The private key is used only by host OpenSSL and is
never mounted in the container. Output must be empty. Build inside Debian to avoid
incompatible host glibc linkage; no workstation binary is copied into the image.

Outputs of the default `--profile single-disk`: `controller.qcow2`, `manifest.json`, detached `manifest.sig`, `public-key.raw`,
`nex-source.tar.gz`, `components.tsv`, `LICENSE`, `NOTICE`, `ARTWORK-LICENSE.md`, and
`build-inputs.json`.
Install the public key through a trusted release channel; the key accompanying an
untrusted download does not authenticate that download. Signature is Ed25519 over the
exact manifest bytes, raw 64-byte signature and raw 32-byte public key. Manifest keys
are `format_version`, `version`, `architecture`, `image_sha256`, `protocol_version`,
`format`, `minimum_vcpus`, `minimum_memory_mib`, `minimum_disk_gib`.

## Guest bootstrap contract

Host deployment prepares a unique disk copy and NoCloud seed, static IPv4 network-config,
and cloud-config `write_files` for root-only `/etc/nex-controller/bootstrap.json`:
`admin_password`, `tls_certificate`, `tls_private_key`, `hostname`, `address_cidr`,
`gateway`, `dns`. The TLS identity is unique and pinned by the deploying host. Default
minimum resources are 2 vCPU, 4096 MiB RAM and a 32 GiB virtual disk. Cloud-init handles
filesystem growth and networking; no package download occurs at guest bootstrap.

The firstboot service waits for cloud-final, installs private identity files, starts
Controller on HTTPS 8443, verifies its UI using the provisioned certificate, then removes
the administrator plaintext password, bootstrap JSON and cloud-init cache/logs. It disables
cloud-init to prevent reseeding on reboot. Controller stores the administrator password
hash. Firstboot also sets the guest console and SSH password for `root` to that same password.
The host must detach and
remove its seed after readiness; guest cache deletion cannot erase the host's seed CDROM.
Failed initialization retains root-only bootstrap material for diagnosis. Machine ID and
SSH host keys are reset in the image; no default administrator credential is embedded in
the pristine image.

## Manual recovery

The Core host package installs `/usr/lib/nex/host-recovery`. Run it as the selected
host's root administrator; it uses local libvirt/QEMU guest-agent access, so no shared
root guest password or SSH login is needed. Select the Controller VM's UUID, actual
local service state directory and actual selected VM storage directory:

```sh
sudo /usr/lib/nex/host-recovery export --vm CONTROLLER_UUID \
  --archive /secure/controller-state.tar.gz \
  --state-dir /var/lib/nex/core-local/vmhost --storage-dir /selected/vm/storage
```

The helper verifies the owned VM definition, stops its Controller and initialization
services, exports while holding the Controller's exclusive database lock, and transfers
the archive to an exclusively created mode-0600 host file. It leaves Controller stopped;
restart it explicitly through the host's guest-agent recovery access when appropriate.
Move the archive off the host using a protected channel and encrypt it at rest: it contains
host authority, TLS keys, credential hashes and sessions. Partial archives are not backups.

Restore onto a separate, explicitly selected replacement appliance of the exact matching
Nex source version. It must be uninitialized, or have only its initial administrator and
no sites, pools, hosts, enrollment, adoption, operations, or other management state.
The initialized empty case needs an additional `--replace-empty-controller` flag.
Stop and permanently exclude the original Controller from managed hosts first:

```sh
sudo /usr/lib/nex/host-recovery restore --vm REPLACEMENT_UUID \
  --original-vm ORIGINAL_UUID --original-stopped-and-excluded --original-external \
  --replace-empty-controller --archive /secure/controller-state.tar.gz \
  --state-dir /var/lib/nex/core-local/vmhost --storage-dir /selected/vm/storage
```

Prepare a fresh empty replacement using the [documented recovery preparation command](../docs/guide/deploy-controller-appliance.md). The replacement must have a matching deployment
journal on its host. `--original-external` explicitly attests that the selected original
on another host has been stopped and excluded; unreachable is never proof of this.
Without that flag, the original also needs a matching local journal and the helper
observes it shut off and disables its autostart. The attestation is not automated fencing. It validates the whole archive's paths, required
identity files, checksums and source version before overwriting a verified empty target.
Controller remains stopped after restore. Restore its original reachable IP/DNS identity
through the selected recovery network procedure before starting; networking is not in
the state archive. Never run two Controllers with the restored identity. The guest also
contains `/usr/local/lib/nex/controller-recovery` for direct quiesced recovery commands.
A usable replacement provisioning walkthrough and real restore evidence are acceptance
gates; the commands and unit tests alone do not prove successful recovery.

## Controller profile: read-only system image and data disk (Phase I-4b)

Status: the guest image is built from `image/controller/` (below) and the host side is unit-tested against a fake libvirt.
`docs/qa/image/phase4-controller-image.md` records what was built and booted and what was not.

### Build the system image

```sh
python3 appliance/build.py --profile controller --version DEVELOPMENT_VERSION --sequence N --output dist/controller-image
```

No Debian cloud image and no signing key are used. `build.py` compiles the Controller and its UI in the pinned builder container
(`build-controller-payload.sh`), then runs `image/build.sh controller`: the Debian trixie snapshot packages of
`image/controller/controller.packages` (the same packages `build-inside.sh` installs into the single-disk appliance, minus the
package manager state, plus systemd-boot) plus the payload tree become an erofs image on a dm-verity tree, a UKI whose command line
carries the root hash, and a raw GPT disk (ESP with systemd-boot and the UKI, root partition, verity partition; UEFI only, unsigned,
no Secure Boot). Outputs: `controller-system.img`, `vm-image.json` (digest, root hash, size, the exact `--payload` argument) and
the source and licence files. The image's digest goes into the release through
`scripts/update-publish.py publish --payload vm-image:controller=dist/controller-image/controller-system.img`; the release
manifest's root signature is the only signature (the single-disk `manifest.sig` is not produced for this profile).

Inside the guest: the root is read-only; `/etc` is the same persistent overlay the host image uses (upper layer on the data disk
under `/var/lib/nex/etc`, so `/etc/nex-controller` lives on the data disk and `controller-recovery` and `host-recovery` see the same
two paths); `/var` is `/dev/vdb`, made as ext4 by the initrd on first boot (`x-systemd.makefs`, never on an existing file system);
cloud-init reads the same NoCloud seed as before; `appliance-ready.json` is written through a link into `/var/lib/nex-controller`.
There is no `apt`, no `apply-update`, no `nex-controller-update.path`; Core replaces the whole image.

### Deploy

On an image-edition host, stage the `vm-image` component (it is an on-demand payload under
`/var/lib/nex/payload/<sha256>/controller-system.img`), then `core-local-setup configure --address A --storage D --controller-image-sha256 SHA256`
and restart `nex-core-local`: it starts with `--controller-system-image` and `--controller-system-image-sha256` instead of the
single-disk `--image/--manifest/--signature/--trusted-key`, and the local deploy page creates the two-disk profile. The domain pins UEFI
with Secure Boot off (the image is unsigned until phase I-5).

With `vmhost.Config.SystemImage` and `SystemImageSHA256` set, Nex Core deploys the Controller as two disks under the VM's directory,
keeping the VM UUID and management MAC for life:

- `system-<sha256>.img`, raw, attached **read-only** as `vda` with guest UEFI. The digest is the `vm-image` component's digest in the
  signed release manifest; the separate appliance signature of the single-disk image is not used for this profile.
- `controller-data.qcow2`, attached as `vdb`, mounted at `/var`, holding only what the recovery commands above already enumerate
  (`var/lib/nex-controller` and `etc/nex-controller`; the latter is a bind mount from the data disk). No new Controller-owned data.

Nex Core replaces the system image with a journaled, resumable swap under one operation ID
(`POST /api/local/v1/updates/apply` with `scope:"controller"`, or the pool rollout's controller_first step). Before it stops
anything it runs the ownership checks every other Controller action uses (the deployment's recorded paths and bridge, a plain qcow2
data disk on local ext4, XFS or Btrfs that no other libvirt domain references) and checks that the data checkpoint fits. The steps are
then: graceful guest-agent shutdown (the Controller is not forced off here; a shutdown already under way is waited for), a checkpoint of
`controller-data.qcow2`, the domain redefined with the new system disk, start, a readiness probe of the UI for 10 minutes, then commit,
keeping the old image and the checkpoint until the next update. The checkpoint is `cp --reflink=auto`: it shares extents on Btrfs (and on
XFS made with reflink), but on ext4 it is a full copy of the data disk's allocated bytes, and a restore copies it again, so twice that
size must be free; the swap refuses with `controller_insufficient_storage` before it stops the Controller when it is not. If the new
image is not ready, the failed guest is stopped gracefully and the checkpoint and the old image are restored and probed. A new image
that never booted far enough to run the guest agent cannot be stopped gracefully; because the checkpoint replaces everything it wrote it is
forced off (`virsh destroy`) once the graceful request was refused or timed out, and it is the only guest a swap ever forces. If the old
image does not come back either, the VM stays stopped (`needs_attention`). A service restart during a step interrupts the swap and
repeating the request resumes it. A guest that accepted the pre-swap shutdown request but had not stopped when the 10-minute wait ended
fails the swap (`controller_shutdown_failed`, nothing changed); the request cannot be withdrawn, so the guest may still power off
afterwards and is then started with Nex Core's local power control. If power is lost in the middle of a swap and libvirt autostarts the
previous image again, repeating the request stops it gracefully (never forced), refreshes the data checkpoint even if the old guest
already stopped after writing, and carries on. A proven failed new image that autostarted during rollback is stopped and the checkpoint
restored again; after the old image has been redefined, its subsequent writes are preserved.
`host-recovery export` and `restore` check both live and inactive definitions against the deployment and, for an alternate system image,
a matching durable running/needs-attention swap journal. They require the owned data disk and approved read-only system image, and a
reachable guest agent; they never start a stopped VM. `controller-recovery` works on the same two guest paths.

What the profile no longer receives: the cloud-init bootstrap of `apply-update`, `nex-controller-update.path`/`.service` and
`/etc/nex/updates.conf` (`appendUpdateBootstrap` is skipped when the deployment has a system image; `TestProfileSeedCarriesNoUpdateHelper`).
What stays, for the classic edition: the single-disk appliance (`build-inside.sh`, `controller.qcow2`, its signed manifest), the
helper files in `packaging/controller`, the Controller `.deb` and its `postinst`/`prerm`, `appendUpdateBootstrap` for the single-disk
seed and the Controller's own APT routes and Settings panel. They are removed together with the classic edition, not before.

## Checks

`python3 -m unittest discover -s tests -p test_appliance_build.py` validates bootstrap
input and rejects unsafe archive paths/links. A successful build additionally runs the
Debian Go compiler, UI typecheck/build, libguestfs customization and `qemu-img check`.
Actual boot/restart/recovery evidence belongs in `docs/qa/`; unit tests do not establish
that appliance deployment or recovery worked on a real hypervisor.
