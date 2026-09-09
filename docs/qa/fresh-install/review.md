# Independent fresh-install review

Date: 2026-09-09. Result: no unresolved findings within the
[fresh local installation scope](../../plans/2026-09-09-fresh-local-install.md).
The reviewer inspected source, artifacts, recorded execution and captures without
operating the lab or changing product code.

## Artifact and execution evidence

- Independently hashed every artifact in [the manifest](artifacts.json), checked
  the Ed25519 signature and equality with the previously trusted development
  public key, and compared 110 Controller/Core source files and 24 installer
  source files with the frozen snapshot. The exact package and graphical initrd
  bytes were independently located and hashed inside the final ISO.
- Inspected the package archive: its embedded Controller image and both runtime
  source archives match the signed build; its Core executable, UI and API hashes
  match [installed provenance](installed-provenance.json). That record also
  compares the installed Controller files, served certificates, enrolled trust,
  and removal of deployment seed/bootstrap credentials. The documented
  post-freeze installer README edit changes no compiled artifact.
- Reviewed exclusive blank raw-file creation, zero pre-install allocation, QMP
  attachments, explicit target/erase confirmation, completed GUI installation
  and disk-only console. Inspected the rendered clean installer logo, deployment
  ready screen and ordinary guest's Debian UEFI boot menu.
- Reviewed the browser and native records: the original protected appliance,
  fresh capable embedded agent, storage/network/ISO workflow and one stopped
  guest retain consistent host, pool and VM identities. Native rejection/replay
  checks, Controller service outage with both VMs still running, independent
  local login, and actual host reboot/persistent identity checks passed.
  [Retained-state evidence](retained-state.json) records removal of temporary QA
  SSH access and restoration of the shipped root SSH policy.

## Corrections and checks

The new harness initially expected the wrong successful VM replay response;
it now checks the implemented `200`/`vm_id` contract. Disposable-target guards
now reject outside-cache or symlink targets and check the running QEMU raw disk
before installer control. SSH uses an existing pinned host key with explicit
strict checking. The new harness corrections required no product change.

The deployment guide's remaining old package example was corrected to
`0.1.0+managed1`; the installation report now explains that temporary SSH access
was removed. Both corrections were rechecked.

Independently executed:

- `python3 tests/integration/installer-logo.py` against the extracted final
  graphical initrd: passed approved artwork and startup/recheck contents.
- `python3 -m unittest discover -s tests -p test_fresh_install_lab.py`: both
  offline target-safety checks passed, including wrong QMP file/format rejection.
- `python3 scripts/check_docs.py --all`: passed structural checks. This does not
  establish committed-range assessment coverage or hosted CI success.

## Applicability and limits

API impact: **existing contract** — the fresh-install test uses the implemented
local session/deployment, Controller enrollment/adoption, managed resource/VM
operations and embedded-agent results. No interface change was necessary.
Guide impact: **updated** — installation, deployment and VM-creation guides link
the actual packaged walkthrough and preserve historical evidence boundaries.

This review accepts the recorded disposable nested-KVM installation slice. It
does not establish physical hardware or LAN compatibility, UEFI host installation,
guest OS/data-file persistence, central guest lifecycle/console support, arbitrary
storage-failure recovery, external redistribution readiness or hosted CI.
An ordinary guest was deliberately stopped before host reboot; its preserved
definition/disk is not evidence of guest autostart or live guest reboot recovery.
