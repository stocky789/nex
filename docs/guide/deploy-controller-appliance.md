# Deploy Nex Controller from a Core host

## Status

This is the **2026-09-08 Controller appliance development slice**, with the newer
`nex-core-local` package `0.1.0+managed1`, for explicitly selected
disposable lab hosts. The host-local web service deploys a prepared Debian 13 virtual
appliance and provides restricted local VM power recovery. No Controller installer ISO
or interactive Debian installation is involved; the guest configures itself on first boot.

Current Nex Core ISO builds include the local-host package. For an existing host, install
the updated package before using root login. Its service discovers the default-route
management IPv4 address and starts local access automatically. Controller deployment
uses `br0` created by current GUI installations when a management NIC is selected.
Older installations and package-only hosts still require a prepared management bridge.
The newer [web-security development changes](secure-web-access.md) alter login/session
and new-password rules; their actual rebuilt-package walkthrough is separately recorded.
The `0.1.0+usability1` signed appliance and matching host package include DHCP and
relaxed Controller password validation. Earlier artifacts do not contain these changes.
The `0.1.0+desktop1` appliance/package retains those behaviors and adds the
Controller desktop layout: resource tree, menus, detail views and bottom task drawer.
See the [desktop artifact rebuild](../qa/controller-desktop-rebuild/checks.md) for
its historical hashes and checks. The `0.1.0+managed1` appliance/package also includes
[managed VM creation](create-virtual-machine.md); the
[fresh local installation report](../qa/fresh-install/checks.md) records its matching
ISO, installation and deployment evidence. Rebuilding media does not
update an already deployed Controller VM; its existing UI remains until that guest
is deliberately updated through a separately verified procedure.
Use a private management network or VPN, with no direct Internet exposure.
General VM deletion, browser consoles, shared-storage control and automatic
Controller failover remain outside this slice. This page does not establish Phase 3 acceptance.

## Prerequisites

- A selected disposable Debian 13 amd64 host with working KVM/libvirt and root console access.
- A trusted `nex-core-local` development package containing a compatible signed appliance
  and the independently trusted release public key. Obtain matching source and retain the
  package's version; see the [development instructions](../development.md).
- An existing local storage directory with capacity for the appliance and a working Linux
  bridge connected to the management LAN. Selecting storage creates a guest disk; it does
  not format host disks. Do not select shared storage.
- A DHCP server on the selected bridge network, or a reserved, unused static IPv4
  address/prefix, gateway and one to three DNS servers. The host and browser must be able
  to reach Controller. A DHCP reservation keeps its address stable.
- At least the default guest allocation of **2 virtual CPUs, 4 GiB RAM and a 32 GiB disk**,
  plus capacity for the host and existing workloads. Preflight can reject insufficient capacity.
- A new Controller administrator password, separate from the host root password.
  Any nonempty password is accepted, up to 1024 UTF-8 bytes without line breaks or NUL.
  Keep both credentials in a password manager.

## Steps

1. On a current GUI-installed Core host, the package and management bridge are already
   configured; continue to step 2. On an older or package-only host, use the root console
   to install the supplied package. It starts local access
   using the default-route management IPv4 address and `/var/lib/libvirt/images`. It uses
   the default-route interface as a bridge only when that interface is already a Linux bridge.
   If needed, set the intended existing address, directory and bridge explicitly using the
   last two commands below. Replace capitalized values; configuration does not create a
   bridge or change networking.

   ```sh
   apt install ./nex-core-local_0.1.0+managed1_amd64.deb
   /usr/lib/nex/core-local-setup configure --address HOST_MANAGEMENT_IP --storage EXISTING_ABSOLUTE_DIRECTORY --bridge EXISTING_BRIDGE
   systemctl restart nex-core-local
   ```

2. After install, the console login banner shows only the local web UI URL,
   for example `https://192.0.2.5:9443`. Use HTTPS and the displayed port. Automatic
   setup detects an address on the default-route interface at service startup; explicitly
   configured hosts show their configured address. If setup cannot find an address or the
   service stops, the console shows an unavailable message. Recover the network/configuration
   locally and restart `nex-core-local`. After a management address change, restart the
   service to refresh automatic discovery.

   On ISO-installed hosts the local UI package is already present. On a package-only host,
   install it first as in step 1.

   Read the SHA-256 certificate fingerprint after root console login:

   ```sh
   openssl x509 -in /var/lib/nex/core-local/tls.crt -noout -fingerprint -sha256
   ```

   Open the displayed `https://HOST_MANAGEMENT_IP:9443` address. Compare its certificate
   fingerprint with the trusted console before accepting the self-signed certificate.
   Sign in as **root** using the password set during Nex Core installation. If you changed
   the host root password afterwards, use its current value. There is no separate local
   web password; Controller access still uses its own **admin** account.

3. In **Overview**, inspect host observations and any **Deployment needs attention** blockers.
   Select **Deploy Controller**. Keep the resource defaults unless the host can support a
   larger allocation, then explicitly select **Local storage** and **LAN bridge**.

4. Enter **Controller hostname** under **Appliance resources**. In **Network settings**,
   choose **Automatic (DHCP)** or **Static IPv4** under **IP configuration**.
   DHCP is selected by default and obtains its address, gateway and DNS automatically. For
   static addressing, enter **IPv4 address / prefix**, **IPv4 gateway** and **DNS servers**;
   separate DNS addresses with commas. Enter the first **Site name** and
   **Controller administrator password**. There is no pool name: this host becomes the
   pool master of a pool created automatically and named after its hostname. The Controller account is **admin**.

5. Select **Review deployment**, check the host, storage, address and resources, then select
   **Create VM & install Controller**. Follow **Deployment progress**. Progress is saved on
   the host; closing or refreshing the browser does not cancel deployment. Wait for readiness,
   including initialization, host enrollment and adoption of the already-created appliance.

6. When the operation is **ready**, select **Open Nex Controller** and sign in with the new
   Controller password. The host verifies the provisioned Controller TLS identity during
   handoff. Compare the browser certificate's SHA-256 fingerprint with the value shown
   beside the ready link before accepting its trust warning. DHCP uses a hostname certificate
   while the link uses its discovered IP, so the browser may also report a name mismatch.
   Check that the chosen Site, the automatically created pool showing this host as
   **Pool master**, and the enrolled host appear in Controller. The appliance
   remains visible in the host-local **Virtual machines** list and in Controller's
   inventory under its host. Select it in Controller to inspect its VM identity;
   adoption retains the existing appliance UUID instead of creating another VM.

7. For local power recovery, return to **Virtual machines** on the host. Where local control
   is allowed, select **Start**, **Shut down** or **Force stop**, then confirm the selected
   VM and action. No hostname typing is needed. Stopping Controller interrupts central management. Force stop cuts
   power immediately and can lose unwritten data; inspect observed state after the request.

## Expected result

One appliance VM boots from its own local disk and autostarts independently of Controller.
The saved deployment reaches **ready**, exposes the verified Controller URL, and enrolls
this host into the selected first Site as pool master of its automatic pool. The existing
appliance UUID is adopted; handoff
must not create a second VM. Local host access remains available if Controller is stopped.

A running VM alone is not completion. Initialization, TLS verification, enrollment and
adoption must also succeed. The local page shows resident VM observations; power actions apply only to the Controller
appliance created by this host. Unrelated resident VMs remain read-only. Unknown ownership
prevents local starts.

## Troubleshooting

- **Local sign-in rejected:** use **root** and the current host root password. The former
  local **admin** account is no longer accepted after upgrade. Locked or expired root
  accounts/passwords cannot sign in; resolve their OS account status through trusted console
  recovery. Change a known root password with `passwd` at the root console. New web logins
  use the new password; existing sessions remain until logout, expiry or service restart.

- **Missing bridge or capacity:** resolve the displayed preflight blocker on the selected
  lab host, then use **Refresh**. The deployment page does not change host networking or
  format disks. Do not bypass the blocker by choosing an unrelated interface or disk.
- **Invalid signature or incompatible appliance:** use the matching trusted package and
  release key. Do not replace the trusted key with one supplied by an untrusted image.
- **Stale observations or expired login:** reconnect/sign in again. Security builds expire
  sessions after 30 minutes without authenticated state-changing requests and eight hours
  absolutely; polling does not renew them. Sign in again after upgrading session cookies.
  Stale data does not mean
  a VM stopped; controls remain disabled until fresh observations arrive.
- **Deployment needs attention or unknown outcome:** inspect the saved operation and the
  existing VM before retrying. Do not erase local state, create another appliance or reuse
  its disk. Select **Retry deployment** on that saved operation to reconcile it. The service
  also resumes recorded work after restart. Root diagnosis uses `journalctl -u nex-core-local`.
- **Waiting for network:** allow the guest to boot and obtain its DHCP lease. If it cannot
  find one usable address within five minutes, fix the DHCP/bridge connection and retry
  the original operation; do not create another Controller VM.
- **Controller DHCP address changed:** restart `nex-core-local` from the host console to
  refresh the address and reconnect the existing host identity, including ready deployments.
  Retry the original operation if it still needs attention; do not create another VM.
  A DHCP reservation is recommended because leases are not monitored continuously.
- **Controller address unreachable:** verify the DHCP lease or reserved static address,
  bridge path and gateway. Do not mark deployment complete merely because the guest runs.
- **Local action refused:** preserve the ownership restriction. Controller outage does not
  authorize starting another copy or taking over shared writable storage.

Controller recovery exports contain database state, identity, TLS private keys and other
secrets. Store a protected copy outside the hosting server; a same-host VM snapshot is not
an independent recovery copy. Use the package's host-root recovery helper, which talks to
this appliance's guest agent; no guest SSH access or shared root password is needed.

1. At the hosting Core server's root console, select the appliance's exact VM UUID from
   `virsh list --all --uuid` and its recorded deployment. Replace `CONTROLLER_UUID` and
   `SELECTED_STORAGE_DIRECTORY` below. Export to an unused path in a protected directory:

   ```sh
   /usr/lib/nex/host-recovery export --vm CONTROLLER_UUID --archive /secure/controller-state.tar.gz --state-dir /var/lib/nex/core-local/vmhost --storage-dir SELECTED_STORAGE_DIRECTORY
   ```

   The helper verifies local ownership, stops Controller and initialization services,
   exports consistent state and transfers it to a mode-0600 file. Controller remains
   stopped after success. A failure before the stop may leave it running; inspect actual state.
   An interrupted command may leave a partial file; it is not a valid recovery copy.

2. Transfer the archive through a protected channel to storage outside the hosting server
   and encrypt it at rest. For example, use your independently verified recovery storage SSH
   account and an existing protected destination directory:

   ```sh
   scp /secure/controller-state.tar.gz recovery-admin@RECOVERY_STORAGE:/protected/nex/
   ```

   Keep the exact matching Nex source version and appliance build. For an export-only
   operation, restart the original Controller through the guest agent:

   ```sh
   virsh -c qemu:///system qemu-agent-command CONTROLLER_UUID '{"execute":"guest-exec","arguments":{"path":"/usr/bin/systemctl","arg":["start","nex-controller"],"capture-output":false}}'
   virsh -c qemu:///system qemu-agent-command CONTROLLER_UUID '{"execute":"guest-exec-status","arguments":{"pid":RETURNED_PID}}'
   ```

   Substitute the numeric `pid` returned by the first command for `RETURNED_PID`. Repeat
   the status query until `exited` is true and require `exitcode` zero; then verify the
   Controller URL. If replacing the original now, keep its Controller service stopped.

3. For replacement recovery, stop and permanently exclude the original Controller before
   preparing any replacement using its network or management identity. The restored copy
   must be the only Controller able to reach the managed hosts. Exclusion is an operator
   responsibility, not automatic fencing. Do not use a fresh normal deployment: that creates
   another Site, pool and host enrollment, making it an unsuitable restore target. If the
   original host is accessible, disable appliance autostart, request shutdown and verify
   observed power-off:

   ```sh
   virsh -c qemu:///system autostart ORIGINAL_UUID --disable
   virsh -c qemu:///system shutdown ORIGINAL_UUID
   virsh -c qemu:///system domstate ORIGINAL_UUID
   ```

   Wait for `shut off`; a shutdown request alone is insufficient. Prevent this original VM
   from being started again. If the host is unavailable, establish physical or network/storage
   exclusion through your recovery procedure; an unreachable host is not proof of power-off.

4. On a clean replacement host, install/configure the same signed appliance/source version
   using the package steps above. This host must have no enrolled agent identity or existing
   Controller deployment. Stop local management while preparing recovery:

   ```sh
   systemctl stop nex-core-local
   install -m 0600 /dev/null /secure/replacement.json
   sensible-editor /secure/replacement.json
   ```

   Enter the following JSON, replacing example network values with the **original Controller's
   address** on the reachable replacement network, and the storage/bridge with the replacement
   host's configured values. Choose a new temporary administrator password in the private editor;
   it will be overwritten by the restored original account. Do not enter the password in shell
   arguments or leave the placeholder. Omit the Site name.

   ```json
   {
     "request_id": "controller-recovery-20260908",
     "name": "nex-controller-recovery",
     "hostname": "nex-controller",
     "cpus": 2,
     "memory_mib": 4096,
     "disk_gib": 32,
     "address_cidr": "192.0.2.20/24",
     "gateway": "192.0.2.1",
     "dns": ["192.0.2.1"],
     "admin_password": "REPLACE_WITH_NEW_PRIVATE_PASSWORD",
     "bridge": "br0",
     "storage_dir": "/var/lib/libvirt/images"
   }
   ```

5. Prepare the empty replacement using this exact saved request:

   ```sh
   /usr/lib/nex/core-local-setup prepare-recovery --request-file /secure/replacement.json
   ```

   Require status `recovery_prepared` and stage `awaiting_manual_restore`; retain the returned
   `vm_id` as `REPLACEMENT_UUID`. This creates and initializes the appliance without enrolling
   a host or creating a Site or pool. Interrupted preparation must reuse the same request/file;
   do not edit the request or create another replacement. Keep local management stopped.
   Use this newly created independent disk; do not reassign or modify VM disks outside
   Nex between preparation and restore. The recovery helper checks recorded identity and
   disk paths, not arbitrary backing chains or hard-link exclusivity.

6. Copy the protected archive onto the replacement host. Restore into the verified empty
   replacement, selecting the exact original VM UUID retained from the failed hosting server:

   ```sh
   /usr/lib/nex/host-recovery restore --vm REPLACEMENT_UUID --original-vm ORIGINAL_UUID --original-external --original-stopped-and-excluded --replace-empty-controller --archive /secure/controller-state.tar.gz --state-dir /var/lib/nex/core-local/vmhost --storage-dir SELECTED_STORAGE_DIRECTORY
   ```

   `--original-external` is for this separate replacement host: it cannot observe or fence the
   original server. The exclusion attestation must be true before running the command. Restore
   rejects a populated replacement or mismatched source version. It transfers the archive through
   the guest agent, restores the original identity, and leaves Controller stopped.

7. Start Controller with the two guest-agent commands from step 2, using `REPLACEMENT_UUID`.
   Require successful command completion, then open the **original Controller URL** and compare
   its original certificate fingerprint. Sign in with the original administrator credential.
   Confirm the original Site, pool and host IDs and existing-host reconnection, with no second active
   Controller. Restart local access with `systemctl start nex-core-local`. Remove the private
   preparation request after successful recovery; it contains the temporary password.

Restoration does not copy guest networking, automatically enroll the replacement host, or
relocate the original appliance's inventory record. Verify actual placement and retain the
original exclusion. The tested manual recovery restored Controller identity and host
connectivity; it did not relocate the appliance inventory record or enroll the replacement host.

## Verification

**Pool master change verified in a real lab (2026-09-09):** the
[pool master lab run](../qa/pool-master/lab/checks.md) deployed a fresh appliance through this
GUI with the site name only (no **Pool name** field), reached `ready` after the Controller inside
the appliance was upgraded to the matching build, and recorded the automatic pool and adoption.
Deploying with a packaged appliance image that still carries the previous Controller stops at
**Needs attention** with a bootstrap rejection until the image is rebuilt. The recovery request
format without a pool name is covered by unit tests only; treat it as unverified in a real lab.

The [fresh local installation](../qa/fresh-install/checks.md) records the
`0.1.0+managed1` package and bundled appliance installed from one matching ISO.
Stock root browser login, deployment through the local GUI, ready state,
automatic host enrollment and protected appliance adoption passed on the new
disposable host. The report separates native results from remaining coverage.

[Desktop-layout rebuild verification](../qa/controller-desktop-rebuild/checks.md)
identifies the matching appliance, local package and ISO. Runtime and installation
limits are recorded there; earlier deployment and recovery evidence remains tied to
its original artifact version.

[Deployment usability checks](../qa/appliance/deployment-usability.md) record the current
DHCP/password/power UI changes, simulated browser checks and artifact availability limits.

[Root-login verification](../qa/appliance/root-auth.md) records the current root-authentication
package and browser checks, including password changes and account failure/recovery.
Earlier appliance deployment/reboot results below apply to their named artifacts.

The `0.1.0+security1` rebuild passed its [Debian package checks](../qa/security/package.md)
and [actual nested-KVM deployment checks](../qa/security/lab.md): ready state,
workspace enrollment/adoption, secret cleanup, Controller login, preserved VM identity,
Controller/local-service outages and guest shutdown/cold start. Its
[browser-security walkthrough](../qa/security/browser.md) separately exercises session/policy
behavior with simulated virtualization. Security1 also passed an actual outer-host reboot:
local management and the same appliance autostarted, and browser login, TLS/source/management
identity and fresh host reporting were retained. The complete manual replacement restore and
replacement-host reboot below remain historical `0.1.0+lab6.1` evidence; a separate-host
restoration was not rerun on security1.
The [appliance lab report](../qa/appliance/lab.md) separates early development fixes from
the final clean run. Package `0.1.0+lab6.1` and its bundled image reached **ready / 100%**
from a pristine disposable nested-KVM host without guest or VM-definition corrections.
The [final recorded results](../qa/appliance/final/results.json) cover actual browser
login/deployment, signature rejection, request authorization, preserved VM identity after
refresh, Controller login and API adoption, bootstrap-secret cleanup, Controller/local-service
outages, and confirmed guest shutdown/cold start. These checks used real HTTPS and libvirt;
they did not mock virtualization. Network access used the disposable lab's bridge and
loopback forwards, so physical management-LAN behavior is not established.

The shipped recovery helper [exported consistent state and verified all six file hashes
after copying it off-host](../qa/appliance/final/export.json). A separate clean host
[prepared an empty replacement](../qa/appliance/final/recovery-prepared.json), restored the
archive and passed original-credential browser login, original TLS fingerprint verification
and [existing-host reconnection with preserved IDs](../qa/appliance/final/restored-inventory.json).
The original VM remained shut off and excluded. The lab report describes the isolated
network preparation and the placement limitation above; these results do not establish
automatic failover or automatic relocation of the appliance's inventory record.

Rebooting the separate replacement host also passed native appliance autostart and
local-service startup. Post-reboot browser login, original TLS/source identity and fresh
existing-host reconnection passed while the original VM stayed shut off and excluded.
The report records the lab-only bridge-link startup correction and
[actual host reboot observations](../qa/appliance/final/replacement-host-reboot.txt).

Physical hardware, physical-LAN compatibility, complete
redistribution clearance, hosted CI and full Phase 2/3 acceptance remain outside these
results. Follow the report's remaining gates and independent review findings.

[Console URL verification](../qa/appliance/console-url.md) records native Debian login-banner
rendering and simulated management discovery, including remaining installed-boot limits.
