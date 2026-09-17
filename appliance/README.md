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

Outputs: `controller.qcow2`, `manifest.json`, detached `manifest.sig`, `public-key.raw`,
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
hash. Firstboot also sets the guest console password for `root` to that same password
(SSH password auth stays off). The host must detach and
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

## Checks

`python3 -m unittest discover -s tests -p test_appliance_build.py` validates bootstrap
input and rejects unsafe archive paths/links. A successful build additionally runs the
Debian Go compiler, UI typecheck/build, libguestfs customization and `qemu-img check`.
Actual boot/restart/recovery evidence belongs in `docs/qa/`; unit tests do not establish
that appliance deployment or recovery worked on a real hypervisor.
