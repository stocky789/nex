# Deploy Nex Controller from a Core host

## Status

Development workflow for explicitly selected disposable lab hosts. The host-local web service
(`nex-core-local`, included in every Nex Core ISO) deploys a prepared Debian 13 virtual appliance
that configures itself on first boot, and provides restricted local power recovery for that
appliance. Take the current Core update before using root login. Deployment uses the `br0`
bridge created by current GUI installations; hosts installed by an older ISO need a prepared
management bridge. Login/session rules are described in [secure web access](secure-web-access.md).

Rebuilding media does not update an already deployed Controller VM. Use a private management
network or VPN, with no direct Internet exposure. Automatic Controller failover is not provided. Navigation on this page follows the development build 0.4.8 console layout; see
[Navigate the Controller console](navigate-the-controller-console.md).

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
- A Controller password for username **root**. Any nonempty password is accepted, up to
  1024 UTF-8 bytes without line breaks or NUL. Same password for the web UI and the
  appliance console. You can reuse this host's root password if you want.

## Steps

1. On a Nex Core host the package and management bridge are already configured;
   continue to step 2. Nex Core is a distribution, so the host must have been installed
   from the Nex Core ISO — installing `nex-core-local` onto an arbitrary Debian system
   is refused, because the package alone does not establish the storage layout,
   networking, console identity and recovery paths a supported host needs.

   Local access uses the default-route management IPv4 address and
   `/var/lib/libvirt/images`, and treats the default-route interface as a bridge only
   when that interface is already a Linux bridge. If you need a different existing
   address, directory or bridge, set them explicitly from the root console. Replace
   capitalized values; configuration does not create a bridge or change networking.

   ```sh
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

   The local UI package is present on every Nex Core host; the ISO installs it.

   Read the SHA-256 certificate fingerprint after root console login:

   ```sh
   openssl x509 -in /var/lib/nex/core-local/tls.crt -noout -fingerprint -sha256
   ```

   Open the displayed `https://HOST_MANAGEMENT_IP:9443` address. Compare its certificate
   fingerprint with the trusted console before accepting the self-signed certificate.
   Sign in as **root** using the password set during Nex Core installation. If you changed
   the host root password afterwards, use its current value. There is no separate local
   web password; Controller access still uses its own **root** account.

3. In **Overview**, inspect host observations and any **Deployment needs attention** blockers.
   Select **Deploy Controller**. Keep the resource defaults unless the host can support a
   larger allocation. **Local storage** is fixed to the host path; select **LAN bridge**.

4. Under **Appliance resources**, keep or change **Controller hostname** (prefilled
   `nex-controller`). In **Network settings**,
   choose **Automatic (DHCP)** or **Static IPv4** under **IP configuration**.
   DHCP is selected by default and obtains its address, gateway and DNS automatically. For
   static addressing, enter **IPv4 address / prefix**, **IPv4 gateway** and **DNS servers**;
   separate DNS addresses with commas. Enter the first **Site name** and
   **Controller administrator password**. There is no pool name: this host becomes the
   pool master of a pool created automatically and named after its hostname. The account is
   **root** for both the Controller web UI and the appliance console.

5. Select **Review deployment**, check the host, storage, address and resources, then select
   **Create VM & install Controller**. Follow **Deployment progress**. Progress is saved on
   the host; closing or refreshing the browser does not cancel deployment. Wait for readiness,
   including initialization, host enrollment and adoption of the already-created appliance.

6. When the operation is **ready**, select **Open Nex Controller** and sign in as **root**
   with the password you set. The same credentials work at the appliance VM console.
   The host verifies the provisioned Controller TLS identity during
   handoff. Compare the browser certificate's SHA-256 fingerprint with the value shown
   beside the ready link before accepting its trust warning. DHCP uses a hostname certificate
   while the link uses its discovered IP, so the browser may also report a name mismatch.
   Check that the chosen Site and the automatically created pool appear in Controller's scope
   menu, and that the enrolled host appears under **Hosts** with the **Master** role. The
   appliance remains visible in the host-local **Virtual machines** list and in Controller's
   **VMs** list and host panel. Select its row in **VMs** to inspect its VM identity;
   adoption retains the existing appliance UUID instead of creating another VM.

7. For local power recovery, return to **Virtual machines** on the host. The appliance this
   page deployed is labelled **Controller appliance**; every other VM libvirt reports,
   including VMs created through Controller, is labelled **Other VM on this host** and shows
   *Local power control is only available for the Controller appliance deployed from this
   page* instead of power buttons. Where local control is allowed, select **Start**,
   **Shut down** or **Force stop**, then confirm the selected VM and action. No hostname
   typing is needed. Stopping Controller interrupts central management. Force stop cuts
   power immediately and can lose unwritten data; inspect observed state after the request.

## Expected result

One appliance VM boots from its own local disk and autostarts independently of Controller.
The saved deployment reaches **ready**, exposes the verified Controller URL, and enrolls
this host into the selected first Site as pool master of its automatic pool. The existing
appliance UUID is adopted; handoff
must not create a second VM. Local host access remains available if Controller is stopped.

A running VM alone is not completion. Initialization, TLS verification, enrollment and
adoption must also succeed. The local page shows resident VM observations; power actions apply only to the Controller
appliance created by this host. Other VMs on this host, including Controller-created ones,
remain read-only here; manage them from Controller. Unknown ownership prevents local starts.

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
- **Permission denied probing `qemu-vnc-lossy`:** an updated Core build fixes new definitions
  but does not repair an existing failed deployment. Keep its operation and disks; inspect the
  recorded VM UUID, saved definition and actual libvirt state before recovering that same
  appliance. **Retry deployment** cannot recreate a VM whose definition failed. Do not disable
  host security or create a second appliance to bypass this error.
- **Controller DHCP address changed:** restart `nex-core-local` from the host console to
  refresh the address and reconnect the existing host identity, including ready deployments.
  Retry the original operation if it still needs attention; do not create another VM.
  A DHCP reservation is recommended because leases are not monitored continuously.
- **Controller address unreachable:** verify the DHCP lease or reserved static address,
  bridge path and gateway. Do not mark deployment complete merely because the guest runs.
- **Local action refused:** preserve the ownership restriction. Controller outage does not
  authorize starting another copy or taking over shared writable storage. This build refuses a
  new appliance deployment on a host that is enrolled or already has an appliance deployment,
  and has no way to remove the old appliance there, so a failed appliance cannot be redeployed
  on its own host. To replace it, exclude the old appliance (autostart disabled and observed
  `shut off`, as in the replacement recovery steps below) and install a new Controller elsewhere:
  the development setup, or an appliance on a separate clean Nex Core host that is not enrolled
  (not yet walked through for replacement), as in [Replace a failed Controller](manage-controller.md#troubleshooting).

Controller recovery exports contain database state, identity, TLS private keys and other
secrets. Store a protected copy outside the hosting server; a same-host VM snapshot is not
an independent recovery copy. Use the package's host-root recovery helper, which talks to
this appliance's guest agent; SSH password login stays disabled. First boot unlocks the guest
**root** console account with the deploy password and, where a `sudo` group exists on the
image, adds it to that group, so anyone who knows the Controller administrator password (or
reads it from the bootstrap record before first boot) has a full root shell at the appliance
console, not only Controller web access; treat that password and console/hypervisor access
with the same care as host root.

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
original exclusion.

## Verification

The GUI navigation on this page was rewritten for the Controller console redesign (development
build 0.4.8); the walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Evidence below was
captured on the earlier layout and still applies to the behaviour it describes.

The [0.3.2 local artifact lab](../qa/2026-09-18-release-artifact-lab.md) booted
the exact bundled appliance separately in a disposable VM and verified DHCP,
pinned TLS, bootstrap login and secret cleanup. It did not repeat this guide's
host-local deployment, enrollment or adoption workflow on the new ISO; that
integrated acceptance remains open.

Recorded checks and their limits:

- [Phase 2 gate run](../qa/2026-09-17-phase2-gate/checks.md) — `0.2.10~gate5`, 2026-09-17,
  disposable nested-KVM lab, headless Chromium. Steps 2–6 in the browser from root login to a
  ready deployment (queued, copying disk, initializing, cleanup, ready) with enrollment and
  appliance adoption. The served Controller certificate matched the ready page fingerprint, and
  the running appliance files matched the signed image. The management bridge and default route
  were unchanged. The deployment used a static address; the DHCP default and the form at 360 px
  were not re-observed in this run (they were in the gate4 run). The appliance's browser console
  reached its login prompt; no console login was made.

- [Fresh local installation](../qa/fresh-install/checks.md) — ISO install, root login, deployment,
  enrollment and appliance adoption.
- [Pool master lab run](../qa/pool-master/lab/checks.md) — deployment without a pool name; the
  recovery request without a pool name is unit-tested only.
- [Appliance lab report](../qa/appliance/lab.md) with [final results](../qa/appliance/final/results.json),
  including [export](../qa/appliance/final/export.json),
  [replacement preparation](../qa/appliance/final/recovery-prepared.json),
  [restored inventory](../qa/appliance/final/restored-inventory.json) and
  [replacement-host reboot](../qa/appliance/final/replacement-host-reboot.txt) (historical
  `0.1.0+lab6.1` artifacts).
- Security build: [package](../qa/security/package.md), [nested-KVM lab](../qa/security/lab.md) and
  [browser](../qa/security/browser.md) checks; separate-host restore was not rerun.
- [Desktop rebuild](../qa/controller-desktop-rebuild/checks.md),
  [deployment usability](../qa/appliance/deployment-usability.md),
  [root login](../qa/appliance/root-auth.md) and [console URL](../qa/appliance/console-url.md).

A live appliance console login after deploy is not recorded. Physical hardware, physical-LAN
behavior and redistribution clearance are not established.
