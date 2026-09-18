# Current release-artifact lab

Date: 2026-09-18. Local development build, version 0.3.2, from the working tree
based on `3db55e1a08b056c5f039565fc6fd1195ab507da4`, including uncommitted changes.
This is not a published release or phase closure. Results below are sanitized
summaries of actual execution, not untouched logs.

Other work changed `internal/controller/storage.go`, its test, and
`internal/vmhost/storage_guard.go` and its test after the source snapshot was
captured. These artifact results exclude those later changes; the archive hash,
not the latest shared working tree, identifies the tested code.

## Artifact identity and build

The real `installer/build.sh` and `packaging/controller/build.py` builders were
used. The final Controller package was rebuilt from the same source archive as
the appliance/Core build. The appliance uses a disposable lab signing key.

| Artifact | SHA-256 |
| --- | --- |
| `nex-source.tar.gz` | `60f0cf674b4ec19ab76917c9b339cca1e240bb6c9ead29491dd92e37d8e277b6` |
| `nex-controller_0.3.2_amd64.deb` | `15544e197ba5708845fa9cc6d28308ac3e92950d29813107d516bbfcbe95817e` |
| `nex-core-local_0.3.2_amd64.deb` | `abe34eacb225d622cf46c9364419cf34c1212f19270edd2dfb977fe550ade35d` |
| `controller.qcow2` | `a2ee10cbb8899c591eb0067a4af6147ad65ba4cafeddbdd73005523caccf341e` |
| `nex-core-0.3.2-amd64.iso` | `ddb3887af6c5202c768e8a270e96d698a216b97371e87c2bbabcd34007cac6d5` |
| `nex-core-0.3.2-source.tar.gz` | `f575a2f88d5ded6ae2dd42a6d6d31bf05868c10e5874af07eed2918667f1daa6` |
| `nex-core-0.3.2-compliance-manifest.json` | `9babcb05680f6d7d33807cb0a04b7a5c505aed28fa7093b726a94ead76747bbf` |

Both packages declare version 0.3.2. The Controller binary reports CGO enabled.
Its embedded source archive equals the appliance's archive and the recorded
source digest. The appliance image hash matches its manifest, and OpenSSL
verified that manifest's signature against its bundled public key.

The complete ISO build exited successfully. Its compliance manifest binds the
actual ISO/source-bundle hashes and lists 1,525 components and 705 notices.
Extracting the Core package from the finished ISO reproduced the tested package
hash. All 406 product-source files matched the ISO's expanded source archive,
which additionally contains the installer recipe. These structural checks do
not replace a complete redistribution/license review or hosted source delivery.

## Exact Controller package: passed

The release startup check ran against the actual package inside the Debian 13
Controller builder:

```sh
python3 scripts/check-controller-package.py dist/controller/nex-controller_0.3.2_amd64.deb
```

It passed fresh SQLite initialization, HTTPS UI/source delivery, administrator
login and authenticated session checks. This command ran inside the builder
with repository/package paths mapped into that container.

A separate fresh Debian 13.6 QEMU/KVM guest had real systemd PID 1, kernel
6.12.107, systemd 257.13 and libc 2.41. Only a new qcow2 overlay and loopback SSH
forward were used. Actual apt installation and maintainer scripts passed.

- Before TLS configuration, the service condition prevented startup and no
  database was created. Disposable TLS/admin configuration then enabled startup.
- Certificate-verified HTTPS rejected anonymous access and a wrong password;
  correct login, authenticated sessions and API documentation passed. Served UI
  and source bytes matched the installed files.
- A site created through the API retained its ID/name through service restart,
  forced process termination and automatic systemd recovery, same-version apt
  reinstall, and an actual VM reboot with a changed boot ID.
- Controller and update-path units were enabled and active after reboot. The
  packaged unit ran as its service user with `ProtectSystem=strict`,
  `PrivateDevices=yes` and `NoNewPrivileges=yes`. Private file permissions passed;
  the generated administrator password did not appear in the service journal.

Same-version reinstall does not prove a cross-version upgrade. HTTP checks do
not establish interactive browser behavior. The current guide documents binary
startup; this lab used the packaged wrapper's configuration paths and does not
claim a standalone package-install guide walkthrough.

## Exact appliance image: passed

`tests/integration/appliance-dhcp-boot.py` booted the recorded image with a new
overlay, two virtual CPUs, 4 GiB RAM and QEMU user networking. It verified DHCP
on the specified management NIC, the pinned-hostname TLS readiness marker,
wrong-password rejection and correct bootstrap login, active Controller/guest
agent services, and removal of bootstrap credentials and sensitive cloud-init
cache. No host bridge or live network was changed.

The first attempt stopped before boot because the ISO builder image used for
seed generation was not yet available. The successful rerun used the installed
host xorriso through a lab-only adapter. This is appliance boot evidence, not
host-local deployment, enrollment or real-LAN acceptance.

## Exact Core package: passed with simulated installer preparation

An unstamped Debian guest correctly rejected a fresh
Core package installation. A subsequent attempt to reuse the Controller guest
without removing its package failed safely on shared file ownership at
`/etc/nex/updates.conf`. No forced overwrite was used. The lab then separated the
roles by removing Controller before testing Core; this was a lab sequencing
correction, not evidence of supported package coexistence.

After an explicitly simulated installer stamp and normal removal of Controller,
apt installed Core and its real dependencies. Maintainer scripts, service
enablement and startup, guest OS identity setup and `host-recovery --help` passed.

- Management-IP TLS verification, anonymous/wrong-password rejection,
  PAM-backed root login, authenticated sessions, API docs and exact packaged
  UI/source delivery passed.
- The unbridged guest correctly reported a missing management bridge as an
  appliance deployment blocker; the lab changed no networking.
- Ordinary restart, forced process termination with automatic systemd recovery,
  and VM reboot passed. Boot ID changed, the service returned automatically, and
  configuration/certificate identity persisted.
- State directories were mode 0700; configuration/key files were mode 0600.
  The loaded packaged service retained its required `cap_setuid` capability.
- Stopping Core changed the console notice to an unavailable state. Restarting
  restored the HTTPS notice and API. The generated root password appeared in
  neither service journal nor console notice.

The disposable package-lab guest was powered off and QEMU exit verified. This
is installed-package evidence with simulated installer preparation, not an ISO
installation. Core reinstall, appliance deployment and native storage operations
were not exercised in this lane.

## Exact ISO: passed bounded installation lab

The existing `tests/integration/iso-smoke.py` harness used a new 24 GiB regular
disk file and no NIC. BIOS/KVM and UEFI/TCG menu captures were inspected: GUI,
text, serial and rescue entries were present and unclipped. The unattended
offline **BIOS/KVM** installation completed using the exact ISO's kernel/initrd
plus test-only automation. The shipping ISO was not modified.

First boot and a subsequent boot from the installed disk passed:

- Installed libvirt, SSH and chrony services; required package presence; root-only
  administrator setup; Nex identity including preservation across Debian
  `base-files` reinstall.
- Packaged OVMF 2023 certificate/firmware checks. These do not constitute a
  Secure Boot installation test.
- A nested KVM guest executed successfully on first boot. A persistent marker
  and its libvirt domain definition survived the installed-host reboot.
- A lab-only wrapper used packaged recovery configuration to set Core's
  management address to guest loopback because the VM intentionally had no NIC.
  Core was enabled, became active, served HTTPS and survived a service restart
  on both boots. This probe bypassed certificate verification; the separate
  native package lab above verified management-IP TLS correctly.

The serial logs contain `NEX_SMOKE_INSTALL_PASS`,
`NEX_NESTED_KVM_BOOT_PASS`, `NEX_SMOKE_FIRST_BOOT_PASS`,
`NEX_SMOKE_REBOOT_PASS`, and Core service/restart markers on both boots, without
the harness failure marker. Captures and marker extraction are retained in
`.cache/release-lab/iso-test/`; the wrapper is in
`.cache/release-lab/iso-inputs/run-smoke.py`. All test VMs stopped.

UEFI coverage is the boot menu only. Automatic management networking, UEFI
installation, install-failure injection/recovery, enrollment and first managed
VM remain separate acceptance checks. A successful serial unattended install
does not verify the graphical installer workflow.

## Hosted CI: inspected, not green

Authenticated read-only inspection found six Gitea runs for committed HEAD:

| Workflow | Push | Pull request |
| --- | --- | --- |
| Documentation | Passed | Passed |
| Installer source/regressions | Passed | Passed |
| Controller foundation | Failed | Failed |

The pull-request Go job failed
`TestManagedSlowCommandKeepsReportingAndCancels` with “cancelled command was
incorrectly completed”. Its browser job failed the simulated-libvirt healthy
state assertion. The push browser job failed with `StopIteration` while finding
the destination-storage option. Subsequent browser checks and the pull-request
Go advisory scan were skipped. Contracts, web and website jobs passed on both
triggers; push Go passed.

There was no Release run for this HEAD. No GitHub remote was configured, and
the corresponding repository API query returned 404, so GitHub evidence is
unavailable. These hosted results cover the committed revision, not the dirty
tree or this build. No push, release, tag or other remote mutation was performed.

The focused cancellation test passed three local race-enabled repetitions:
`go test -race ./internal/agent -run '^TestManagedSlowCommandKeepsReportingAndCancels$' -count=3`.
That does not explain or close the earlier hosted failure.

## Additional checks and limits

The good/bad fixture regression `python3 tests/integration/controller_package_smoke.py`
passed: the working package initialized SQLite and authenticated, while the
CGO-disabled package was rejected with the expected diagnostic. Package-gate
wiring tests passed (2), and distribution packaging tests passed (24).

Independent review checked completed package/appliance claims, exact hashes,
snapshot drift, privacy and impact assessments against the actual private
scripts/logs and found no issues. Final independent ISO review checked boot
captures, commands, raw markers, source/package identity and stated limits,
also with no findings. `python3 scripts/check_docs.py --worktree` passed for 61 uncommitted
paths; `--all`, the repository privacy unittest and `git diff --check` passed.
An earlier worktree check failed while the separate storage work lacked its
assessment; that documentation coverage was present by the final check.

No physical-hardware, Secure Boot host installation, full Phase 3
failure/recovery, enrollment/first-VM, native multi-host placement, cross-version
upgrade or external redistribution certification is claimed. Private raw logs
and lab scripts are retained under the ignored `.cache/release-lab/` directory;
this report records their relevant results without private machine details.

API impact: **none** — verification calls existing APIs and changes no contract.
Guide impact: **none** — no product behavior or operator workflow changed.
State owner: **none** — disposable test data adds no product durable record.

## Documentation and roadmap sign-off

The project owner requested recording these results and closing the related
acceptance items. The [platform checklist](../plans/2026-09-08-nex-platform.md#artifact-lab-sign-off--2026-09-18),
[appliance acceptance](../plans/2026-09-08-controller-appliance.md#acceptance-evidence)
and [readiness handoff](../plans/2026-09-14-production-readiness-handoff.md#recommended-next-work)
now sign off the bounded artifact checks for this snapshot. Full Phase 3 and
Phase 8 remain open; existing Phase 2 closure and other phases are unchanged.
Guide verification sections and BUG-0246 follow-up link the actual evidence.

The public roadmap keeps the installable-host milestone **In testing**, labels
new accomplishments as a local development snapshot, and lists integrated
installation/bootstrap/recovery and hosted release acceptance as outstanding.
Its release-build wording now follows the actual common-version checks rather
than implying that tagging triggers the build.

`npm run check` passed with zero errors/warnings and `npm run build` passed.
Focused headless Chromium checks on the built roadmap passed at 1440, 768 and
360 pixels: status and done/open text, keyboard disclosure, no horizontal
overflow, and content with JavaScript disabled. Desktop/mobile captures were
visually inspected and were readable and unclipped. There are no dynamic
loading, resource-error or destructive-action states on this static page.
The initial browser assertion expected mixed-case status text despite its CSS
uppercase rendering; a case-normalized assertion passed without a product fix.
Private captures/results are in `.cache/release-lab/roadmap-signoff/`.
No new product or infrastructure walkthrough was run for this documentation pass.

For this documentation follow-up, guide impact is **updated** (verification
sections only); API impact remains **none**, and state owner remains **none**.
Independent review found no scope, acceptance or visual defects.
The documentation worktree gate passed for 65 uncommitted paths; the repository
privacy unittest and `git diff --check` also passed for this follow-up.
