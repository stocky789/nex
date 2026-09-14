# Fresh local installation verification

Date: 2026-09-09. Result: installation and runtime walkthrough **passed**; independent
[review](review.md) has no unresolved findings.

## Artifacts and environment

| Item | Value |
|---|---|
| Artifacts | [artifacts.json](artifacts.json): `0.1.0+managed1` Controller appliance and Core local package in ISO recipe `0.1.0-alpha1`, one frozen source snapshot (later installer README edit is docs only) |
| ISO SHA-256 | `5ca79c902ce39cb11c15f710abbf6629d2785089eb17b641435f8240af5e1dff` |
| Lab | New 128 GiB sparse raw disk ([zero allocated blocks](blank-disk.json), [attached devices](attached-devices.json)), 4 vCPU, 12 GiB RAM, KVM, QEMU user networking with loopback forwards; `br0` exists only inside the guest |

## Results

| Step | Result | Evidence |
|---|---|---|
| Build | Appliance and Core package builds passed; signed image/source match manifest and retained development signer; ISO package bytes match; ISO checksum, hybrid boot metadata and initrd artwork regression passed; Debian package smoke (PAM, TLS, credential rejection, source/API, removal) passed in its own container | [artifacts.json](artifacts.json) |
| GUI install | Branded boot menu; keyboard GUI selection; logo without ripple; review named `/dev/vda` 128 GiB; unchecked confirmation blocked install (disk still zero blocks); confirmed install; DHCP on `br0`; completion; **Restart now**; disk-only console shows hostname and local HTTPS URL | `installer-boot-menu.png` (not committed), `installer-welcome.png` (not committed), `installer-progress.png` (not committed), `installer-confirmation-required.png` (not committed), `installer-complete.png` (not committed), `installed-console.png` (not committed) |
| Browser walkthrough | Root login before any SSH setup, Controller deploy to ready, enrollment and appliance adoption, storage/bridge registration, Debian ISO upload, stopped UEFI guest | [browser.md](browser.md) |
| Provenance | `nex-core-local` runs the embedded agent; installed binaries, UI and API match build; served TLS and agent trust verified; bootstrap credentials and seed media removed (empty CD-ROM device may remain) | [installed-host.txt](installed-host.txt), [installed-provenance.json](installed-provenance.json) |
| Guest boot | Guest (2 vCPU, 1024 MiB, 4 GiB qcow2, UEFI) started locally reached the Debian UEFI installer menu; boot only, no OS install | `guest-boot-uefi.png` (not committed) |
| Negative API | Anonymous/missing-CSRF, invalid creation/media and changed replay rejected; exact replay returned original UUID | [browser-negative-results.json](browser-negative-results.json) |
| Controller outage | Service stopped: both VMs kept running; fresh root login worked at 360px | [browser-offline-results.json](browser-offline-results.json) |
| Host reboot | Changed boot ID; identity/certificate, guest definition and disk capacity preserved; Core and appliance autostarted; both logins, fresh reporting, both VM IDs | [recovery-after.json](recovery-after.json), [browser-post-reboot-results.json](browser-post-reboot-results.json) |

Test access: after stock browser login, one lab SSH key and `PermitRootLogin prohibit-password` were
set via console for native inspection (pinned host key, loopback). Removed before handoff; effective
`PermitRootLogin no` restored ([retained-state.json](retained-state.json)).

## Reproduction

Helpers: [lab](../../../tests/integration/fresh_install_lab.py),
[GUI install](../../../tests/integration/fresh_install_gui.py),
[host checks](../../../tests/integration/fresh_install_host.py),
[browser](../../../tests/integration/fresh_install_browser.py),
[recovery](../../../tests/integration/fresh_install_recovery.py). Sequence: lab prep → ISO boot →
GUI install/restart → disk boot → browser login → console key → host inspection → guest boot,
outage, reboot → SSH cleanup. Target guards require a regular-file lab under the repo cache and check
the QMP raw disk; both [guard tests](../../../tests/test_fresh_install_lab.py) passed locally and run in
both Phase 1 workflows. The lab stayed running (loopback only); private `ACCESS.md` beside the disk
holds URLs and credential-file paths.

## Limits

Physical hardware/LAN, UEFI host install, Secure Boot, redistribution clearance, hosted CI, central
guest power/console and remaining Phase 2 lifecycle not covered. `check_docs.py --all` passed; scoped
coverage checked in an isolated snapshot. [native-results.json](native-results.json) summarizes checks.
