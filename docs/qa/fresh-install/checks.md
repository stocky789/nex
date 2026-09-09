# Fresh local installation verification

Date: 2026-09-09
Status: Fresh installation and runtime walkthrough passed with independent review.

## Artifacts and environment

The [artifact record](artifacts.json) identifies the matching `0.1.0+managed1`
Controller appliance and Core local package, embedded in ISO recipe `0.1.0-alpha1`.
The ISO SHA-256 is
`5ca79c902ce39cb11c15f710abbf6629d2785089eb17b641435f8240af5e1dff`.
Its compiled runtime, UI and cleaned installer artwork came from one frozen
current-source snapshot. The later installer README correction is documentation
only. Existing shared release outputs were preserved.

This run uses a newly created 128 GiB sparse raw disk, four virtual CPUs and
12 GiB RAM under KVM. [Blank-disk evidence](blank-disk.json) records zero allocated
blocks before boot; [attached-device evidence](attached-devices.json) identifies
the fresh disk and read-only ISO. QEMU user networking and loopback forwards
isolate the lab; its management bridge exists inside the disposable host only.
No previous installed host disk or workstation bridge is reused.

## Executed build checks

Appliance and Core package builds passed. The signed appliance image and source
archive match their manifest; the signer matches the retained development trust
identity. Extracted ISO package bytes match the selected new package. The ISO
checksum, hybrid boot metadata and graphical-initrd artwork regression passed.
Debian package smoke checks exercised PAM login, TLS, credential rejection,
source/API access and package removal in their separate disposable environment.
These build checks do not substitute for installed-system tests below.

## Installation and runtime

The actual [branded boot menu](installer-boot-menu.png) was inspected and GUI
installation selected by keyboard. The [Welcome screen](installer-welcome.png)
and [installation progress](installer-progress.png) show the corrected logo
without the textured ripple. Review named the new `/dev/vda` 128 GiB disk;
an [unchecked confirmation attempt](installer-confirmation-required.png) blocked
installation while the disk still had zero allocated blocks. Explicit confirmation
then permitted installation. DHCP management networking selected `br0` inside
the guest. The installer reached its actual [completion page](installer-complete.png),
and **Restart now** was selected. The [disk-only boot console](installed-console.png)
then displayed the configured hostname and automatic local HTTPS URL.

The [browser walkthrough](browser.md) records successful stock root login before
any QA SSH setup, bundled Controller deployment to ready, automatic enrollment and
protected appliance adoption, first storage/bridge registrations, genuine Debian
ISO upload and stopped UEFI guest creation. The [installed-host record](installed-host.txt)
confirms `nex-core-local` runs the embedded agent; no standalone replacement agent
or development binaries were installed. [Provenance checks](installed-provenance.json)
match both installed binaries, UI and API files to the build artifacts, verify
served TLS identities and agent trust, and confirm bootstrap credentials and seed
media were removed. An empty virtual CD-ROM device may remain without a source.

The selected guest has two vCPUs, 1024 MiB RAM, a 4 GiB independent qcow2 disk and
UEFI firmware. Starting only that disposable guest locally reached the actual
[Debian UEFI installer menu](guest-boot-uefi.png); this is boot evidence, not a
guest OS installation or central console feature. [Native negative checks](browser-negative-results.json)
rejected anonymous/missing-CSRF requests, invalid creation/media and changed replay;
exact completed replay returned the original UUID without new resources.

During a real Controller service outage, both VMs remained running and a fresh
root login to Core still worked, including at 360 pixels. See
[outage browser results](browser-offline-results.json). [Host-reboot checks](recovery-after.json)
observed a changed boot ID, preserved enrolled identity/certificate, identical
persistent guest definition and unchanged disk capacity. Core returned automatically
and the same Controller appliance autostarted. [Post-reboot browser checks](browser-post-reboot-results.json)
verified both logins, fresh host reporting and both original VM identities. The
ordinary guest remains stopped after boot verification; no guest OS was installed.

For native inspection only, root console access installed one lab SSH public key
and changed effective root SSH policy from `no` to `prohibit-password` inside this
isolated VM. Stock browser login was verified first. SSH uses a loopback forward
and pinned host key; private credentials are excluded from evidence. This is an
explicit test-access adjustment, not the shipped SSH default.
Before handoff, the temporary SSH key and override were removed and effective
`PermitRootLogin no` restored; [retained-state evidence](retained-state.json)
records the final running appliance, stopped guest and loopback-only access.

## Reproduction and local access

The retained helpers are [lab creation/control](../../../tests/integration/fresh_install_lab.py),
[graphical installation](../../../tests/integration/fresh_install_gui.py),
[host/native checks](../../../tests/integration/fresh_install_host.py) and the
[browser workflow](../../../tests/integration/fresh_install_browser.py).
The [recovery helper](../../../tests/integration/fresh_install_recovery.py) exercised
guest boot, Controller stop/start and host reboot/verification. The native sequence
was lab preparation → ISO boot → GUI configure/install/restart → installed-disk
boot → stock browser login → temporary console-key setup → host inspection →
guest boot and outage/reboot checks → SSH cleanup. The browser report records
its executed commands. Target guards require a disposable
regular-file lab under the repository cache and check the actual QMP raw disk
before installer control. Two offline [target guard checks](../../../tests/test_fresh_install_lab.py)
passed locally and independently; both existing Phase 1 CI workflows discover
this test file on pushes and pull requests. Hosted CI was not run here.

The fresh lab remains running with loopback-only access for Matt's inspection.
Its private `ACCESS.md` beside the new disk records URLs, credential-file locations
and the validated graceful-poweroff command. No password is in this report.

## Scope and review

API impact: **existing contract** — installation/deployment and managed-VM tests
exercise shipped browser, Controller and embedded-agent interfaces. No new API
behavior is introduced by this verification batch.
Guide impact: **updated** — correct stale setup descriptions and link executed
walkthrough evidence from the applicable guide pages.

Physical hardware, physical management LANs, UEFI installation, Secure Boot,
external redistribution clearance and hosted CI are outside this run. Central
guest power/console operations and the remaining Phase 2 lifecycle are separate
work. Independent artifact, installer, runtime and harness review is recorded
in [the review](review.md), with no unresolved findings. The
[native result summary](native-results.json) records executed checks and limits.

`python3 scripts/check_docs.py --all` passed. Scoped assessment coverage is checked
in an isolated Git snapshot containing this batch, with concurrent unrelated
working changes treated as its baseline; this does not commit or publish the
shared checkout. The local range command/results are retained under the repository
cache. Neither structural nor range checking substitutes for the walkthrough above.
