# Nex bug ledger

Append resolved defects with their evidence and the approach to avoid.

## BUG-0001 — Incomplete isolated ISO build dependencies

- Date: 2026-09-08
- Symptom: first build exited 127 at `gpgv`; target HTTPS APT also needed an explicit CA-certificate dependency.
- Root cause: relying on recommended/transitive tools while installing with `--no-install-recommends`.
- Fix: add `gpgv` to `installer/Dockerfile` and `ca-certificates` to `installer/packages.txt`.
- Regression: `./installer/build.sh` completed signature verification and ISO build (alpha1 ISO SHA256 `b2862b1e31f8c784b54efd67a3496dcf66bc8252ce910109d59ea043853eb734`); manifest contains `ca-certificates`; offline install smoke exercises the closure. No physical-hardware or Secure Boot verification claimed.
- Avoid: assuming build-machine tools exist in a minimal installer target.

## BUG-0002 — Hybrid boot objects lost during ISO remaster

- Date: 2026-09-08
- Symptom: xorriso: `Cannot refer by isohybrid MBR to data outside of ISO 9660 filesystem`.
- Root cause: `-map` of the whole root invalidated boot-object references; boot replay patches `isolinux.bin` after media hashes are prepared.
- Fix: replay boot settings before `-update_r`; exclude boot catalog and patched `isolinux.bin` from media MD5 lists (`installer/build.sh`).
- Regression: independent review verified BIOS/UEFI hybrid metadata and checksums; `tests/integration/iso-smoke.py` boots both firmware modes.
- Avoid: rebuilding hybrid boot layout from copied filenames, or hashing bytes the ISO writer later patches.

## BUG-0003 — Package manifest used the wrong dpkg-deb operation

- Date: 2026-09-08
- Symptom: draft manifest treated its format expression as a field name instead of producing TSV rows.
- Root cause: `dpkg-deb -f` reads named control fields; it is not a formatted listing.
- Fix: `dpkg-deb --show --showformat` in `installer/build.sh`.
- Regression: independent final-image review checked populated TSV manifest with versions/architectures.
- Avoid: treating an output file's existence as proof of valid inventory.

## BUG-0004 — Installer debconf environment leaked into target package setup

- Date: 2026-09-08
- Symptom: offline install failed in CA-certificate configuration (bad debconf descriptors), later hung in PAM configuration.
- Root cause: raw `chroot` inherited Debian Installer frontend state without target environment setup.
- Fix: use `in-target /bin/sh` for the target configuration script (`installer/late-install.sh`).
- Regression: `installer/check.sh` guards `in-target`; fresh unattended run passed install, services, nested KVM and both boots (ISO SHA256 `b02044bcac4205af247d52a30ff158ad4d73e7580382f666b37baf394538015f`). Evidence: [QA evidence](qa/iso/2026-09-08-alpha1.md).
- Avoid: raw chroot instead of the installer's target helper; treating ISO creation as proof of installation.

## BUG-0005 — Root-account hook ran before installer component initialization

- Date: 2026-09-08
- Symptom: root-only draft early command failed: `/usr/bin/user-setup-ask` missing.
- Root cause: before component loading, `anna-install` only queues packages; setting the retriever alone leaves the index missing.
- Fix: run native `anna cdrom-retriever default` before installing the account component when its dialog is absent.
- Regression: `tests/integration/root-account.py` checks init order and root-only password handling. Evidence: [root-only installer QA report](qa/iso/2026-09-08-root-account.md).
- Avoid: mocking package install as unconditional success; assuming early commands run after component load.

## BUG-0006 — Root-only smoke check counted a Debian service account

- Date: 2026-09-08
- Symptom: install succeeded but ordinary-user assertion failed on first boot.
- Root cause: UID range included `libvirt-qemu` (64055).
- Fix: check Debian ordinary-user range 1000–60000.
- Regression: corrected installed-host check on the disposable install. Evidence: [root-only QA report](qa/iso/2026-09-08-root-account.md).
- Avoid: treating every UID above 1000 as an installer-created login.

## BUG-0007 — Installer header used placeholder branding

- Date: 2026-09-08
- Symptom: installer showed a generic blue letter tile and system-font name.
- Root cause: header recreated branding as text instead of approved artwork.
- Fix: crop/scale approved Core lockup with GTK image support; keep original artwork in the frontend source archive.
- Regression: initrd regression compares exact artwork bytes. Evidence: [actual 800×600 VM capture](qa/iso/2026-09-08-installer-logo.md).
- Avoid: typed text in place of an approved lockup.

## BUG-0008 — Initrd overlay omitted directories needed by new assets

- Date: 2026-09-08
- Symptom: logo rebuild booted to the plain-text fallback though the PNG was in the cpio listing.
- Root cause: overlay archived files only; kernel cannot extract into a missing parent directory.
- Fix: include directory entries before their files in the overlay archive.
- Regression: `tests/integration/installer-logo.py` checks parent-before-file ordering and artwork on the complete initrd. Evidence: [final VM capture](qa/iso/2026-09-08-installer-logo.md).
- Avoid: treating an archive listing as proof the kernel can extract it.

## BUG-P1-0001 — Controller startup and credential lifecycle regressions

- Date: 2026-09-08
- Symptom: Controller panicked before listening; in-flight old credentials or a second replacement token could bypass revocation/re-enrollment.
- Root cause: ambiguous ServeMux patterns; certificate checks outside the state-changing transaction.
- Fix: non-conflicting routes; revalidate exact certificate inside each transaction; consume replacement tokens only for a still-revoked host.
- Regression: `go test -race -count=1 ./...` and `python3 tests/integration/phase1.py --browser` (stale-auth, replacement-token checks). Both phase1 workflows; hosted execution not claimed. Evidence: [independent review](qa/phase1/review.md), [process/browser results](qa/phase1/acceptance.md).
- Avoid: treating compilation or pre-transaction authorization as proof a later operation is authorized.

## BUG-P1-0002 — Native inventory rejection and misleading probe freshness

- Date: 2026-09-08
- Symptom: real hosts got HTTP 400 (CPU flag list over assumed limit); stalled probes looked healthy, suppressed observations or lost samples after clock correction.
- Root cause: too-small CPU-feature bound; aggregate/wall-clock-only merging; unrelated CPU flags labeled virtualization.
- Fix: realistic bounds, virtualization-specific flags, per-probe merge by ordered reports and sample identity, preserve timeout/stale state.
- Regression: merge/freshness and virtualization-filter Go tests (filter failed before fix); `bash tests/integration/phase1-debian.sh` covers stalled/failed libvirt. Both phase1 workflows; hosted results unverified. Evidence: [Debian evidence](qa/phase1/debian.md).
- Avoid: heartbeat as fresh service check, ordering by host wall clocks, dev-machine CPU metadata bounds.

## BUG-P1-0003 — UI health and asynchronous scope races

- Date: 2026-09-08
- Symptom: healthy checks rendered degraded; stale reads overwrote revoked state; delayed token appeared under another pool; skip link changed the route.
- Root cause: mismatched status enum; async responses unguarded by request/session/scope; navigation hash reused for focus.
- Fix: render contract status/freshness, reject superseded reads, guard workflow changes during mutations, move focus without changing URL.
- Regression: `python3 tests/integration/phase1.py --browser` (keyboard, health, delayed-token, delayed-read/revocation, narrow screen). Both phase1 workflows; hosted CI not proven. Evidence: [browser evidence](qa/phase1/acceptance.md).
- Avoid: trusting promise completion order as intent; object-navigation fragment as skip link.

## BUG-0009 — Initrd overlay replaced the merged-usr library symlink

- Date: 2026-09-08
- Symptom: first standalone-wizard image panicked during init.
- Root cause: archiving a new /lib directory replaced Debian's /lib → usr/lib symlink.
- Fix: install hooks under usr/lib.
- Regression: installer-logo.py fails the broken initrd with "Overlay directory replaces base symlink: lib"; fixed artifact boots. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: creating overlay directories without checking whether the base path is a symlink.

## BUG-0010 — Preseed command failure did not stop automatic partitioning

- Date: 2026-09-08
- Symptom: acknowledging a failed disk recheck could let partitioning continue.
- Root cause: upstream preseed_command reports failure but returns success after its dialog.
- Fix: direct partman init hook whose nonzero exit aborts.
- Regression: installer-handoff.py simulates a failed recheck and expects nonzero exit. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: a diagnostic preseed early command as a fail-closed destructive-operation guard.

## BUG-0011 — Private staging permissions leaked into public host configuration

- Date: 2026-09-08
- Symptom: umask 0077 could stage /etc directories 0700 and resolver files 0600.
- Root cause: umask reduces requested modes; copied staged attributes propagate.
- Fix: explicit 0755 public directories / 0644 files; keep private staging root and credentials.
- Regression: installer-network.c under umask 0077 checks public/private modes. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: assuming requested mode is resulting mode under a restrictive umask.

## BUG-0012 — Selected keyboard did not match credential entry

- Date: 2026-09-08
- Symptom: non-US keys could produce a different password after reboot; restarting setup kept the old live layout.
- Root cause: wizard only saved a later preseed while Xorg kept its previous layout.
- Fix: apply layout at startup and on selection, clear both credential entries on change, block continuation on failure.
- Regression: installer-keyboard.py covers success/failure/clearing/init; VM German selection verified. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: collecting a password before applying layout; trusting dropdown default to reset Xorg.

## BUG-0013 — Wizard startup changed the installation engine umask

- Date: 2026-09-08
- Symptom: native installation would inherit umask 0077.
- Root cause: sourced private-handoff script did not restore caller's mask.
- Fix: save/restore the mask around the handoff.
- Regression: installer-handoff.py checks caller stays 0022. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: treating a sourced script's process settings as file-local.

## BUG-0014 — Review text and confirmation recovery were not fully visible

- Date: 2026-09-08
- Symptom: review showed only HOST; after wrong confirmation the correction field was hidden.
- Root cause: dynamic wrapping label failed in shipped GTK; wrapping error label shrank the viewport.
- Fix: read-only scrollable text view for summary; single-line error.
- Regression: wizard-review.py checks visible summary, error and correction input, disk unchanged; old capture fails. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: string construction as proof of visibility; checking only happy-path layout.

## BUG-0015 — Hardware preparation lacked a debconf protocol channel

- Date: 2026-09-08
- Symptom: preparation log contained raw PROGRESS commands.
- Root cause: anna invoked outside a debconf frontend after protocol descriptors were cleared.
- Fix: initialize noninteractive confmodule before redirecting output or calling anna.
- Regression: wizard-review.py rejects raw PROGRESS lines; complete-initrd check matches shipped script. Installer checks (`installer/check.sh`, installer-shell/handoff/keyboard.py, installer-network.c, installer-logo.py, wizard-review.py) run in both installer workflows; hosted runs unexecuted. Evidence: [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: redirecting installer protocol output to a log instead of establishing its channel.

## BUG-0016 — Wizard stayed at 800×600 on larger displays

- Date: 2026-09-08.
- Symptom: black margins at 1024×768.
- Root cause: fixed 800×600 relied on maximize; installer X has no window manager.
- Fix: size/position the undecorated window to the GDK screen and track size changes.
- Regression: `wizard-review.py --resolution 1024x768` checks corners (old ISO reproduced); both CI providers. Evidence: [QoL QA](qa/iso/2026-09-08-installer-qol.md).
- Avoid: relying on WM maximize/fullscreen hints in the installer.

## BUG-0017 — Timezone completion did not open in the native installer

- Date: 2026-09-08.
- Symptom: matching city typed but no popup in the real installer.
- Root cause: window not presented/focused in WM-free X; callback-only tests missed GTK popup activation.
- Fix: present the window after sizing; keep canonical timezone validation.
- Regression: `installer-qol.py` matching; `wizard-review.py` real popup selection at both resolutions, GitHub and Gitea. Evidence: [QoL QA](qa/iso/2026-09-08-installer-qol.md).
- Avoid: string-matching unit check as proof a GTK popup opens.

## BUG-AP-0001 — Appliance ownership must survive management revocation

- Date: 2026-09-08.
- Symptom: revoking another host could drop its observed VM UUID from adoption conflict checks.
- Root cause: conflict query filtered revoked hosts.
- Fix: include every other host's UUID observation; require fresh observations to clear; local power checks domain/disk identity, duplicate writable refs, hardlinks.
- Regression: `internal/controller/appliance_test.go` (revoked conflicts, restart freshness), `internal/vmhost/engine_test.go`; `go test -race -count=1 ./...` passed; both Phase 1 workflows.
- Avoid: treating revocation, timeout or stopped management as storage fencing.

## BUG-AP-0002 — Appliance recovery must preserve complete private identity

- Date: 2026-09-08.
- Symptom: partial recovery archive could omit host CA; extraction left private state root-owned.
- Root cause: path validation did not prove completeness, integrity, source, or ownership.
- Fix: require database, CA pair, TLS pair, init marker; verify file set/hashes before mutation; serialize against live state; restore service ownership.
- Regression: `tests/test_appliance_build.py` (missing-CA, tampered, traversal/link, ownership) via `python3 -m unittest discover -s tests -p 'test_*.py'`; both CI providers. Evidence: [appliance lab evidence](qa/appliance/lab.md).
- Avoid: reporting file extraction as working Controller recovery.

## BUG-AP-0003 — Guest package inventory format was expanded by the shell

- Date: 2026-09-08.
- Symptom: first appliance image build failed recording package/source inventory.
- Root cause: guest shell expanded dpkg-query's `${source:Package}`.
- Fix: preserve literal format variables through the guest shell.
- Regression: `tests/test_appliance_build.py` runs real `/bin/sh` quoting with fake dpkg-query (old fails); both CI providers.
- Avoid: assuming nested quoting preserves a tool's dollar syntax.

## BUG-AP-0004 — QEMU could not create the appliance console socket

- Date: 2026-09-08.
- Symptom: nested-KVM deploy defined the VM but QEMU could not bind `console.sock` in the private disk directory.
- Root cause: QEMU-created socket placed in a host-service-owned directory; simulated virsh hid it.
- Fix: let libvirt allocate its private UNIX socket path; keep restricted disk dir, no public VNC.
- Regression: `TestConsoleSocketUsesLibvirtRuntimeDirectory` (failed before) in `go test -race ./internal/vmhost`; both CI. Evidence: [Real failure and subsequent lab verification](qa/appliance/lab.md).
- Avoid: making the disk directory writable or exposing TCP VNC.

## BUG-AP-0005 — Package builder could not create a nested read-only mountpoint

- Date: 2026-09-08.
- Symptom: host-package Docker run failed: `/input/bundle` missing under read-only input mount.
- Root cause: expected Docker to create a nested mount point inside a read-only parent.
- Fix: create the bundle mountpoint in staged input before launching Docker.
- Regression: `tests/test_core_local_package.py` checks the staged mountpoint; both CI. Evidence: [package evidence](qa/appliance/package.md).
- Avoid: Dockerfile compilation as proof the mount layout works.

## BUG-AP-0006 — First-boot initialization had a systemd ordering cycle

- Date: 2026-09-08.
- Symptom: real Debian boot skipped appliance initialization.
- Root cause: wanted by `multi-user.target` but ordered after `cloud-final.service`, which runs after that target.
- Fix: enable under `cloud-init.target`.
- Regression: `test_firstboot_has_no_debian_cloud_final_ordering_cycle` runs `systemd-analyze verify` (old unit fails); both CI. Evidence: [the lab report](qa/appliance/lab.md).
- Avoid: valid unit syntax as proof the dependency graph can start.

## BUG-AP-0007 — Private build permissions made public Controller files unreadable

- Date: 2026-09-08.
- Symptom: API login worked but GUI returned 403 and corresponding source was unreadable.
- Root cause: private build umask put root-only modes on public UI/source files.
- Fix: readable/traversable modes on the public share only.
- Regression: `test_public_guest_assets_are_readable_by_service_user`; both CI. Evidence: [package](qa/appliance/package.md), [guest](qa/appliance/lab.md).
- Avoid: weakening credential/state permissions.

## BUG-AP-0008 — API success was mistaken for completed appliance installation

- Date: 2026-09-08.
- Symptom: build reported Ready while GUI returned 403 and first-boot credential cleanup was unfinished.
- Root cause: handoff proved enrollment/adoption, not first-boot completion; cleanup failures could be ignored.
- Fix: per-instance readiness marker only after GUI access and verified cleanup of persistent and runtime cloud-init caches; handoff requires versioned marker and GUI HTTP 200 over pinned HTTPS.
- Regression: handoff integration test (failed before); firstboot test injects cleanup failure and proves retry; Go race suites; both CI.
- Avoid: booted VM or API success as finished workflow; swallowing cleanup errors.

## BUG-AP-0009 — Virsh interactive framing broke recovery transfer parsing

- Date: 2026-09-08.
- Symptom: host-root recovery export failed parsing domain XML and guest-agent replies.
- Root cause: stdin-fed virsh adds prompts and can truncate/wrap echoed commands; parser expected plain output.
- Fix: validate short-command framing; require exactly one guest-agent return/error JSON; keep ambiguous output and secrets out of errors.
- Regression: `tests/test_host_recovery.py` (6 checks); real QGA export verified six file hashes; both CI.
- Avoid: archive contents in process arguments; assuming simulated subprocess framing matches the real tool.

## BUG-AP-0010 — The packaged service umask removed QEMU directory traversal

- Date: 2026-09-08.
- Symptom: systemd-packaged deploy made the per-VM directory 0700; QEMU could not open its disk.
- Root cause: `mkdir` 0711 reduced by `UMask=0077`; dev launch lacked that environment.
- Fix: exclusively create then chmod 0711; state/seed dirs 0700, disk/seed/credential files 0600.
- Regression: `TestApplianceDirectoryTraversableUnderPrivateUmask` (failed before); both CI. Evidence: [lab evidence](qa/appliance/lab.md).
- Avoid: weakening service umask; requested mode as resulting mode.

## BUG-0018 — Installer rejected valid qualified hostnames

- Date: 2026-09-08.
- Symptom: `nexcore.example.test` rejected on Host and Network.
- Root cause: GUI and backend separately enforced a single 63-char label; config assumed a short name.
- Fix: shared standards-based ASCII hostname validation with root-dot handling; split short hostname/domain, keep full/short hosts aliases.
- Regression: `installer-network.c`, `installer-hostname.py`, `wizard-review.py`; both installer workflows. Evidence: [hostname QA](qa/iso/2026-09-08-installer-hostname.md).
- Avoid: permitting dots in a UI regex while backend/config still assume one label.

## BUG-0019 — NIC status confused disabled interfaces with absent link

- Date: 2026-09-08.
- Symptom: connected virtual NIC showed `down` during setup.
- Root cause: GUI read raw operstate before admin activation, refreshed only on selection.
- Fix: link-only probe, carrier read with admin-state checks, refresh without reloading form values.
- Regression: `installer-link.py`; `wizard-review.py` QEMU disconnect/reconnect; both installer workflows. Evidence: [link QA](qa/iso/2026-09-08-installer-link.md).
- Avoid: renaming `down` to "No link" without probing carrier; reloading the editor to refresh.

## BUG-0020 — Installer offered Xen disks then rejected them

- Date: 2026-09-08.
- Symptom: `/dev/xvda` listed, then install failed "Select a supported physical installation disk."
- Root cause: discovery and backend used different name rules; backend omitted `xvd`.
- Fix: share the name policy including `xvd`; keep device safety checks.
- Regression: `tests/integration/installer-disk.py` (old validator fails, unsafe targets rejected); both installer workflows. Evidence: [QA](qa/iso/2026-09-08-installer-xen-disk.md).
- Avoid: bypassing disk validation or hiding a supported disk.

## BUG-SEC-0001 — Unauthenticated requests consumed shared login capacity

- Date: 2026-09-08.
- Symptom: two incomplete login bodies made a valid login get HTTP 429; one global cooldown coupled clients.
- Root cause: hashing slots reserved before reading untrusted bodies; admission ignored peer identity.
- Fix: validate bounded credentials/origin before reserving; 5 attempts/min per actual peer, bounded tracking, two global hashing slots; ignore forwarding headers.
- Regression: `TestIncompleteLoginDoesNotReserveHashCapacity` (429 before) plus peer/proxy isolation, `go test -race ./...`; both Phase 1 workflows push/PR and weekly. Evidence: [security checks](qa/security/checks.md).
- Avoid: removing expensive-work limits; a global cooldown one peer can trigger.

## BUG-SEC-0002 — Password byte length did not enforce the displayed character minimum

- Date: 2026-09-08.
- Status: minimum superseded 2026-09-08 — project owner removed the 16-character minimum; bootstrap accepts nonempty UTF-8 within the transport bound. Shared byte/encoding validation remains. Evidence: [deployment usability QA](qa/appliance/deployment-usability.md).
- Symptom: eight two-byte characters satisfied the advertised 16-character minimum.
- Root cause: backend counted UTF-8 bytes; guest firstboot and bootstrap had inconsistent limits.
- Fix: shared validation (then ≥16 code points), ≤1024 UTF-8 bytes; aligned guest and browser; existing hashes preserved.
- Regression: `TestPasswordMinimumCountsCharacters` (failed before) in vmhost race suite; browser checks; both CI push/PR and weekly; guest via Python discovery.
- Avoid: byte counts or UTF-16 code-unit length as character count.

## BUG-SEC-0003 — Child processes inherited ownership locks

- Date: 2026-09-08.
- Symptom: local service refused restart; a still-running child held the ownership lock.
- Root cause: three raw `syscall.Open` lock calls (local management, recovery prep, agent storage) lacked close-on-exec.
- Fix: atomic `O_CLOEXEC` on all three.
- Regression: subprocess tests in `internal/localcore/lock_test.go`, `internal/agent/lock_test.go` (failed before); `go test -race ./...` both CI push/PR and weekly.
- Avoid: waiting for children, removing locks, or setting close-on-exec via a separate racy call.

## BUG-SEC-0004 — Browser CI referenced a binary it never built

- Date: 2026-09-08.
- Symptom: workflows ran local browser QA against default binary path never built on a clean runner.
- Root cause: integration built private temp binaries, not `dist/nex-core-local`.
- Fix: build the local service before its browser step in both workflows.
- Regression: `python3 -m unittest discover -s tests -p test_ci_security.py` (failed before for both providers). No hosted CI execution claimed.
- Avoid: assuming an ignored workstation artifact exists on CI.

## BUG-0021 — Normal keyboard setup appeared as a startup error

- Date: 2026-09-08.
- Symptom: Welcome page showed red "Keyboard layout changed".
- Root cause: successful initial layout application used the error area.
- Fix: success clears the message; real errors and credential clearing remain.
- Regression: `installer-keyboard.py` silent-success assertion (failed before); both installer workflows.
- Avoid: removing initial layout application or credential clearing to hide it.

## BUG-0022 — Progress gaps left the installer panel empty

- Date: 2026-09-08.
- Symptom: main panel went blank between installation steps.
- Root cause: cdebconf destroys progress widgets at STOP; shell had no gap content.
- Fix: waiting panel between steps, replaced by real progress or prompts.
- Regression: `tests/integration/installer-progress.py` (failed on blank gap), includes error/log recovery and completion; both installer workflows. Evidence: [native progress QA](qa/iso/2026-09-08-installer-progress.md).
- Avoid: hiding recovery questions, fake percentages, showing completed task as live.

## BUG-0023 — Waiting fallback still swapped with the stock progress layout

- Date: 2026-09-08.
- Symptom: waiting text alternated with the differently positioned stock progress bar (flicker).
- Root cause: BUG-0022 kept two progress presentations and lifecycles.
- Fix: one persistent Nex panel receives step updates across component boundaries.
- Regression: `tests/integration/installer-progress.py` checks active/gap/resumed geometry; both installer workflows. Evidence: [continuous progress QA](qa/iso/2026-09-08-continuous-progress.md).
- Avoid: nonempty waiting screen as proof of smooth transitions.

## BUG-0024 — Installer-selected management NIC lacked a deployment bridge

- Date: 2026-09-08.
- Symptom: after GUI management networking, local Controller deployment demanded an existing management bridge.
- Root cause: installer addressed the NIC/VLAN directly; deployment requires a Linux bridge.
- Fix: stage native `br0` with the selected NIC/VLAN as port and addressing on the bridge; bundle bridge-utils.
- Regression: generated-config check (failed before); `bash tests/integration/installer-bridge.sh` native bringup/recovery. Evidence: [bridge QA](qa/iso/2026-09-08-management-bridge.md).
- Avoid: hiding preflight failure or changing live management networking from deployment.

## BUG-0025 — DHCP preview text leaked into static editing during development

- Date: 2026-09-09.
- Symptom: switching DHCP preview to static kept transient status text.
- Root cause: mode change cleared address only when leaving static.
- Fix: clear transient entries on every mode change; restore saved static settings only on NIC load; readonly DHCP feedback with normal contrast.
- Regression: `tests/integration/wizard-review.py --nics 2` asserts blank static address; found by source review, no failing-before run claimed. Evidence: [DHCP QA](qa/iso/2026-09-09-installer-dhcp.md).
- Avoid: storing status text in config; readonly feedback as disabled low-contrast text.

## BUG-0026 — Desktop Help menu clipped on narrow screens during development

- Date: 2026-09-09.
- Symptom: Help dropdown extended past the left edge at 360px.
- Root cause: mobile last-menu rule right-aligned a popup wider than its left offset.
- Fix: shared left alignment.
- Regression: bounds assertion in `tests/integration/phase1_browser.py` failed with `Help menu outside narrow viewport`; both phase1 workflows. Evidence: [desktop layout QA](qa/controller-desktop-layout/checks.md).
- Avoid: aligning menus by position without checking popup bounds.

## BUG-0027 — Website wordmark retained a grey raster background

- Date: 2026-09-09.
- Symptom: header/footer wordmarks showed a pale rectangle.
- Root cause: opaque pale brand-board backdrop; multiply blending kept it.
- Fix: shared CSS brightness filter before blending, header/footer/mobile.
- Regression: `tests/integration/website_browser.py` compares logo background pixels (failed before with `Logo raster background differs from the page`); both phase1 workflows. Evidence: [website QA](qa/website/checks.md).
- Avoid: assuming multiply makes nonwhite backgrounds transparent; fixing only the header.

## BUG-0028 — Website gallery reused personal workstation identity

- Date: 2026-09-09.
- Symptom: gallery screenshots showed a contributor workstation name and machine inventory.
- Root cause: reused internal captures with native telemetry instead of a neutral fixture.
- Fix: fresh disposable Controller, fictional host enrolled via TLS/mTLS, explicit demo inventory, real browser captures; no DOM substitution or image editing.
- Regression: `tests/integration/website_controller_captures.py` checks inventory/identity, rejects private label, asserts no credentials; both phase1 workflows. Original found by visual inspection. Evidence: [fresh-capture evidence](qa/website-controller-captures/results.json), [website walkthrough](qa/website/checks.md).
- Avoid: publishing internal QA screenshots unreviewed; treating no passwords as full privacy check.

## BUG-0029 — Website source README included private deployment details

- Date: 2026-09-09.
- Symptom: website README named a private deployment address, workstation account/home path, env-specific restart commands.
- Root cause: internal deployment handoff kept in prospective public source.
- Fix: generic build/deploy guidance; ignore local credentials/config; generated artifacts out of Git.
- Regression: `python3 -m unittest discover -s tests -p 'test_website_public_files.py'` (failed before) checks Git-visible files incl. force-added; both phase1 workflows. Evidence: [website source review](qa/website/public-source-review.md).
- Avoid: real deployment instructions in public docs; `.gitignore` as protection for tracked files.

## BUG-0030 — Installer logo included the textured reference-board background

- Date: 2026-09-09.
- Symptom: installer logo distorted with a pale rippled rectangle.
- Root cause: both GTK headers cropped the opaque textured brand board.
- Fix: dedicated transparent Nex Core lockup, proportional GDK scaling, original allocation.
- Regression: `tests/integration/wizard-review.py` logo-background pixel check (fails original); `installer-logo.py` packaging; both installer workflows. No full ISO rebuild or hosted CI claimed. Evidence: [before/after captures and executed commands](qa/iso/2026-09-09-logo-cleanup.md).
- Avoid: reference-board crop as production logo; shrinking allocation and shifting controls.

## BUG-0031 — Agent service sandbox blocked Linux network inventory

- Date: 2026-09-09.
- Symptom: agent reported `netlinkrib: address family not supported by protocol`; inventory missing, VM creation failed capacity check.
- Root cause: `RestrictAddressFamilies` omitted AF_NETLINK.
- Fix: allow AF_NETLINK.
- Regression: `python3 -m unittest discover -s tests -p 'test_agent_service.py' -v` (old unit fails `'AF_NETLINK' not found`); both phase1 workflows; static check is not runtime proof. Evidence: [native managed-VM run](qa/managed-vm/checks.md).
- Avoid: accepting missing inventory in placement; disabling all socket restrictions.

## BUG-0032 — Agent result backlog exceeded the Controller report limit

- Date: 2026-09-09.
- Symptom: large completed-operation backlog made every reconnect report fail HTTP 400.
- Root cause: agent sent all unacknowledged results; Controller accepts ≤100.
- Fix: send ≤100 durable results per report, drain as acknowledged, discard none.
- Regression: `TestResultBacklogDrainsInControllerSizedBatches` in `go test -race ./internal/agent` (failed before with `controller rejected request (HTTP 400)`); both phase1 workflows; hosted runs not triggered.
- Avoid: dropping results, clearing the journal, weakening Controller input bounds.

## BUG-0033 — VM details read configuration from the wrong response shape

- Date: 2026-09-09.
- Symptom: new guest details omitted CPU, memory, disk, firmware, creation task.
- Root cause: UI expected flat fields; API returns them in `spec`.
- Fix: read `vm.spec`; render creation task when an operation ID exists.
- Regression: `tests/integration/managed_vm_browser.py` (isolated reproduction failed CPU assertion); both phase1 workflows (simulated host). Evidence: [the reproduction and its limits](qa/managed-vm/browser-regression-results.json).
- Avoid: create response as proof settings are visible; guessing response shape.

## BUG-0034 — Oversized operation errors blocked agent reporting

- Date: 2026-09-09.
- Symptom: long filesystem error caused HTTP 400 on every report.
- Root cause: OS error over Controller's 4096-byte result-error limit forwarded unbounded.
- Fix: valid UTF-8 prefix ≤4096 bytes on the wire; full error kept in the private journal.
- Regression: `go test ./internal/agent -run TestOversizedResultErrorDoesNotBlockHeartbeat -count=1` (failed before with HTTP 400); both phase1 workflows. Evidence: [Native supplemental verification](qa/managed-vm/agent-reconnect.json).
- Avoid: dropping local diagnostics, invalid UTF-8 cuts, weakening Controller validation.

## BUG-0035 — Website preview hosting exposed listings and lacked browser policy

- Date: 2026-09-09.
- Symptom: static site listed asset directories; no CSP, framing, MIME, referrer or HTTPS policy headers.
- Root cause: temporary Python HTTP preview remained the deployed server.
- Fix: unprivileged nginx, read-only releases, no listings or asset symlinks, restrictive headers on all responses, relative redirects through the HTTPS proxy.
- Regression: `python3 -m unittest discover -s tests -p 'test_website_*.py' -v` with real nginx; `tests/integration/website_hardening_browser.py`; both phase1 workflows. Evidence: [hosting evidence](qa/website-security/hardening.md).
- Avoid: proxy TLS as proof of origin hardening; weakening CSP for tests; absolute redirects revealing the private port.

## BUG-0036 — Site dropdown retained an unintended keyboard Tab stop

- Date: 2026-09-09.
- Symptom: Tab from site dropdown search focused the option container instead of closing.
- Root cause: Chromium made the scrollable listbox implicitly focusable.
- Fix: `tabIndex={-1}` on the listbox; keep arrow keys and Escape restoration.
- Regression: `tests/integration/site_selector_browser.py` Tab-dismissal (original failed); both Phase 1 workflows. Evidence: [before/after browser record](qa/site-selector/popup-tab-regression.json).
- Avoid: removing the Tab assertion, trapping focus, disabling scrolling.

## BUG-0037 — Host-local enrollment raced the running embedded agent

- Date: 2026-09-09.
- Symptom: `go test -race` on `POST /api/local/v1/enrollment` re-enrollment: `agent.Enroll` mutated a Store the embedded `agent.Run` loop was using.
- Root cause: `nex-core-local` runs the agent in-process; handler called `agent.Enroll` without pausing it.
- Fix: per-agent stoppable context (`internal/localcore/handoff.go`); stop before enroll, restore prior controller/trust on failure, restart after.
- Regression: `TestEnrollmentReplaceHostSuccess`, `TestAddSiteHostReEnrollmentSuccess` (reproduced before) under `go test -race ./...`; both Phase 1 workflows. Evidence: [add host lab checks](qa/simple-host-enrollment/lab/checks.md).
- Avoid: mutating a Store while its `Run` loop is live; tearing down the registry entry to pause.

## BUG-0038 — Add host pushed the host-identity CA instead of the served certificate

- Date: 2026-09-09.
- Symptom: `POST /api/v1/sites/{id}/hosts` pushed the host-identity CA as trust, so real `agent.Enroll` against the self-signed HTTPS cert would fail `certificate signed by unknown authority`.
- Root cause: unit test listener signed with that CA masked the mismatch.
- Fix: `cmd/nex-controller/main.go` reads `-tls-cert` into `Config.ServerCertPEM` and pushes it; route returns 503 before host contact when absent.
- Impact: API contract and `docs/API.md` updated (503 when server certificate absent).
- Regression: `TestAddSiteHostSuccess` (self-signed listener, fails old), `TestAddSiteHostRequiresServerCertificate`; `go test -race ./...` both Phase 1 workflows. Evidence: [lab checks](qa/simple-host-enrollment/lab/checks.md).
- Avoid: test listeners chained to the host-identity CA; host CA as Controller HTTPS trust anchor.

## BUG-0039 — libvirt probe blocked on `virsh dominfo` for defined domains

- Date: 2026-09-09
- Symptom: on Debian 13 (libvirt 11.3.0) the libvirt observation of any host with a defined domain was `unknown` (`probe deadline exceeded; previous observation retained`), so adopted VMs never showed a state.
- Root cause: per-domain `virsh dominfo` prints its first fields and then blocks on that libvirt build; one blocking call stalled the whole observation.
- Fix: `internal/agent/probes.go` uses `virsh domstate` and `virsh domname` per domain.
- Regression: `tests/integration/controller_replacement_debian.sh` (both Phase 1 workflows) waits for a replacement Controller to observe the domain `shut off`; failed before, passes after. Evidence: [evidence](qa/pool-vlan-networks/controller-replacement-debian.json).
- Avoid: per-domain `dominfo` in bounded probes; prefer single-purpose queries.

## BUG-0040 — Update tests reached the public apt repository after the loopback rewrite

- Date: 2026-09-09
- Symptom: Gitea `phase1` failed `TestUpdatesAuthAndApply` and `TestLocalUpdatesAuthAndApply` (`RepoURL:https://nexvirtualization.com/apt ... Error:updates index HTTP 401`) on every push since `aeb32d4`; locally they returned the real published version instead of fixture `9.9.9`.
- Root cause: `NormalizeRepoURL` replaces plain-http loopback repo URLs with the public default, discarding the `httptest.NewServer` URL from `NEX_UPDATE_REPO`; only the `nexupdate` tests had moved to TLS (`8d587e8`).
- Fix: `internal/controller/updates_test.go` and `internal/localcore/updates_test.go` use `httptest.NewTLSServer` trusted via `nexupdate.HTTPClient`; regenerated stale `docs/api/index.html`. Same change: `tests/test_appliance_build.py` restores the `077` umask set by `firstboot.main()` (had failed `test_apt_publish` with `control directory has bad permissions 700`); `docs/changes/2026-09-09-iso-fresh-source.json` cites tracked scripts instead of an untracked ISO; `packaging/core-local/core-local-setup` reads the printed `openssl x509 -checkip` verdict (OpenSSL 3.0 on the Ubuntu 24.04 runner exits 0 on mismatch). `docs/changes/2026-09-09-core-update-progress-stuck.json` (test names after evidence paths) left unchanged as historical.
- Regression: `go test -race ./...` (both Phase 1 workflows) fails on the old tests, passes with fix; `python3 scripts/generate_api.py --check` covers the reference.
- Avoid: plain-http loopback update fixtures (use TLS with `HTTPClient` overridden); tests changing process-wide state without cleanup; relying on `openssl x509 -check*` exit codes; evidence entries that are not bare repository paths.

## BUG-0041 — Controller capture harness rejected its own fixture addresses

- Date: 2026-09-09
- Symptom: `tests/integration/website_controller_captures.py` exited before opening a browser with `AssertionError: Private IP in capture content`.
- Root cause: a privacy guard rejecting RFC 1918 addresses was added while the simulated inventory fixture still used private subnets.
- Fix: fixture uses documentation addresses (`198.51.100.10/24`, `203.0.113.10/24`), identifies as `Nex Core 0.1.4`, and asserts one `Version` row and no `Agent version`.
- Regression: the harness itself; failed at `HEAD`, passes with fix. Evidence: [evidence](qa/single-nex-core-version/controller-captures.json).
- Avoid: private addresses in any capture screened for publication.

## BUG-0042 — Fresh Nex Core install had no Nex package repository

- Date: 2026-09-10
- Symptom: after an ISO install, `apt update` listed only Debian repositories; `https://nexvirtualization.com/apt` was absent.
- Root cause: `nex-core-local` shipped only `nex.list.example` under `/usr/share/nex/core-local/`, with no apt source or signing key.
- Fix: package ships `/usr/share/keyrings/nex-archive-keyring.gpg` (verified against the live `InRelease`); postinst writes deb822 `/etc/apt/sources.list.d/nex.sources` (`URIs` from `repo_url` in `/etc/nex/updates.conf`); prerm removes it.
- Regression: `tests/test_core_local_package.py` (`test_postinst_owns_release_identity_from_package_version`, `test_apt_source_uri_follows_configured_repo_url`, `test_package_ships_archive_keyring_and_builds_the_apt_source`) via `python3 -m unittest discover -s tests -p 'test_*.py'` in both `phase1` workflows.
- Avoid: shipping required out-of-box configuration as an `.example` copied by hand.

## BUG-0043 — CI reported nothing for 18 of 23 checks because one privileged step failed first

- Date: 2026-09-10
- Symptom: every Gitea `phase1`/`installer` run failed since 2026-09-08. `phase1` stopped at the VLAN bridge namespace step (`unshare: unshare failed: Operation not permitted`), skipping 18 later steps (unit tests, `generate_api --check`, UI build, advisories, browser checks); `installer` failed with `bash: installer/check.sh: No such file or directory`.
- Root cause: `act_runner` v1.0.6 runs jobs in an unprivileged container: no `CAP_SYS_ADMIN` for namespaces, and `docker run -v "$PWD:/work"` names a job-container path that mounts empty on the host. Masked because the privileged step ran sixth. Second cause: this Gitea instance executes `.github/workflows/`, not `.gitea/workflows/`, so two pushes editing `.gitea/workflows/installer.yml` changed nothing, and `.gitea/workflows/release.yml` had no `.github` copy.
- Fix: unprivileged `phase1` steps run first; a single `hostcap` step probes namespace creation and a Docker bind mount, gating all five host-dependent steps (namespace, `localcore_browser.py` which failed with `cc1: fatal error: .../nex-root-auth.c: No such file`, two libvirt/KVM steps) with a `::warning::`. Gitea `installer.yml` (project owner decision, 2026-09-10) runs `installer/check.sh`, seven installer regressions and `docker build` in the job container; **four checks were deleted, not fixed** (native bridge, DHCP preview, ISO build/initramfs completeness, wizard review/disk confirmation/progress), and every run warns that no bootable ISO is established. GitHub `installer.yml` keeps the full matrix. Both installer files now identical, `release.yml` exists in both, each with a header naming the executed directory.
- Regression: none automated (a workflow file is not coverage). `bash installer/check.sh`, the seven `tests/integration/installer-*.py` and the `installer-network.c` compile-and-run passed outside any container; workflows parse, gating confirmed. After the third pass every ungated step passed; the five gated steps report as skipped.
- Update 2026-09-17: the `host` lane and its `hostcap` gate were removed from both `phase1` workflows because every gated step skipped on this runner, and `installer.yml` no longer builds the builder image (`release.yml` builds it). The five host-dependent checks remain uncovered in CI; restore them as a job only with a privileged or host-label runner.
- Avoid: a host-privileged step ahead of cheap unconditional checks; reading the resulting green as full coverage (skipped is not passing); assuming which workflow directory a forge executes without checking step names in a real run.

## BUG-0044 — Website hosting test asserted against 404s instead of the serving policy

- Date: 2026-09-10
- Symptom: `test_website_hosting.WebsiteHosting.test_serving_policy_and_headers` failed every assertion with `404 != 200`/`403`/`301` once unit tests ran in CI again.
- Root cause: the `tempfile` fixture directory was 0700; nginx's unprivileged workers got `EACCES`, which `try_files` renders as `=404`, so no policy was exercised.
- Fix: fixture made traversable (0755 dirs, 0644 files), matching production `/srv/nex-website/current`. `4da27f2`.
- Regression: `python3 -m unittest discover -s tests -p 'test_*.py'` in both phase1 workflows' `Check contracts and contributor tools` step (skipped since 2026-09-08 by BUG-0043); not yet confirmed green in CI.
- Avoid: asserting only status codes when the server can 404 for reasons unrelated to the policy under test.

## BUG-0045 — New maintainer script omitted from the corresponding-source archive

- Date: 2026-09-10
- Symptom: `packaging/core-local/preinst` was added to the package builder but not to the `scripts/build-source.py` allowlist, so the AGPL corresponding-source archive lacked it.
- Root cause: `packaging/core-local/build.py` builds from the extracted archive, so the builder's `install -m755 packaging/core-local/preinst` would fail; two lists must agree and only one was updated (a licensing defect, not only a build break).
- Fix: allowlist entry plus `test_installed_maintainer_scripts_are_in_the_allowlist`, cross-checking every packaging path a builder references.
- Regression: `python3 -m unittest discover -s tests -p 'test_build_source.py'`; fails without the entry (`referenced by a package builder but absent from the source archive: ['packaging/core-local/preinst']`), all 5 pass with it. Both phase1 workflows.
- Avoid: adding a file to a package builder without adding it to the source archive that builder is built from.

## BUG-0046 — Local service Close left the enrollment agent racing TempDir cleanup

- Date: 2026-09-10
- Symptom: Gitea `phase1` failed `TestEnrollmentWithReplacementControllerSucceeds` with `TempDir RemoveAll cleanup: unlinkat …/agent: directory not empty` after assertions passed.
- Root cause: `Service.Close` waited on `s.wg`, but the embedded handoff agent goroutine was not on it and could still write `state.json`.
- Fix: `Close` calls `releaseHandoffAgent` (waits for the goroutine or closes an unstarted store); the test removes the agent directory right after `Close`.
- Regression: `go test -race -count=10 ./internal/localcore -run TestEnrollmentWithReplacementControllerSucceeds` and `go test -race -count=1 ./internal/localcore` pass; `go test -race ./...` in both phase1 workflows.
- Avoid: dismissing TempDir cleanup failures as flakes when a background goroutine still owns files; stop the owner in Close.

## BUG-0047 — Create VM kept a blank "system" disk name instead of the derived default

- Date: 2026-09-10
- Symptom: Gitea `phase1` failed `managed_vm_browser.py` with `AssertionError: disk name 'system' does not derive from VM name 'browser-vm-…'`.
- Root cause: `VMWizard` discarded `autoDiskName` disks whenever `blank.disks` existed, which is always true after `{...blank, ...restored}`.
- Fix: keep restored disks only when the restore payload supplies them.
- Regression: the existing assertion in `tests/integration/managed_vm_browser.py` (both phase1 workflows); `npm run typecheck` passes. Full Playwright not re-run locally.
- Avoid: treating a spread of a non-empty blank draft as restored state; check the restore payload.

## BUG-0048 — Background ISO upload rendered as a panel above Recent tasks

- Date: 2026-09-10
- Symptom: an ISO upload showed as a separate "ISO upload to Controller" panel stacked above Recent tasks.
- Root cause: `UploadStatus` was a standalone `section.panel` beside `<Tasks>`.
- Fix: `UploadRow` rendered first in `Tasks` (kind `iso-upload`, progress/rate and Cancel in Status); upload state records `actor` and `updated_at`; table renders with only the upload.
- Regression: `tests/integration/managed_vm_browser.py` asserts a `tr` in `.activity-dock table tbody` with a progressbar and no upload `section` (both phase1 workflows). Local simulated run passed that assertion, then stopped on the unrelated ambiguous **Back** button.
- Avoid: rendering a transient item as a sibling panel of the list it belongs to.

## BUG-0049 — Create VM topology treated blank sockets as zero while showing "1"

- Date: 2026-09-10
- Symptom: vCPUs 4, sockets apparently 1, cores 2, threads 2 blocked with `0 × 2 × 2 does not`.
- Root cause: `value={n || ""}` with numeric placeholders; empty sockets stayed 0 while the placeholder looked filled.
- Fix: removed placeholders; filling the first field seeds the others (1 × count × 1); clearing one clears all; separate incomplete vs mismatch errors (`applyTopology`/`topologyIssue`).
- Regression: `tests/integration/managed_vm_browser.py` fills cores then threads with count 4 and asserts sockets `1`, no error (both phase1 workflows); `npm run typecheck` passes.
- Avoid: numeric placeholders on optional number fields stored as 0 when empty.

## BUG-0050 — Create VM dropdowns flickered closed on click

- Date: 2026-09-10
- Status: superseded — diagnosis wrong; see BUG-0064.
- Symptom: every native `<select>` in Create VM opened and immediately closed.
- Root cause (as recorded, wrong): `.panel { overflow: hidden }` with the desktop shell's nested overflow made Chromium dismiss the popup.
- Fix: stopped clipping `.panel` overflow. Had no effect on the native popup; dropdowns kept flickering.
- Regression: `tests/integration/form_select_overflow.py` never reproduced the flicker (headed run caught a fleeting popup); replaced by `form_select_picker.py`.
- Avoid: `overflow: hidden` on form panels in the desktop shell (original note); see BUG-0064 for the real lesson.

## BUG-0051 — Host console listener let any local uid connect and get an unauthenticated root shell

- Date: 2026-09-10
- Symptom: review found `HostConsoleSocket` listening on abstract UNIX socket `@nex-core-host-console` with RFB security None, spawning root `/bin/bash -l` for any local process (including QEMU guests).
- Root cause: built as a real RFB listener although its only caller is `internal/agent/console.go` in the same process.
- Fix: `HostConsoleSession() (net.Conn, error)` in `internal/vmhost/hostconsole.go` returns the client half of an in-process `net.Pipe`; `internal/agent/console.go` (`hostConsoleSession`) uses it directly.
- Regression: `go test ./internal/vmhost -run TestHostConsoleHasNoLocalListener` and `go test ./internal/agent -run TestServeConsoleUsesHostSessionDirectly` pass; `go test -race ./...` in both phase1 workflows.
- Avoid: giving an in-process-only consumer an OS listener; an abstract UNIX socket is reachable by every local uid.

## BUG-0052 — Host console streamed a full frame on every FramebufferUpdateRequest, ignoring incremental

- Date: 2026-09-10
- Symptom: `serveHostRFB` answered every request with a 1,024,000-byte raw frame; noVNC re-requests immediately, so an idle console streamed forever.
- Root cause: the 9-byte request body was discarded (`io.CopyN(io.Discard, c, 9)`) and `send()` always called; the PTY reader also sent a full frame per read chunk.
- Fix: `dirty`/`wantUpdate` tracking — non-incremental always answered, incremental only if changed, otherwise the PTY reader pushes the deferred frame; `sendFrame` clears `wantUpdate` under the same mutex (second review round: a satisfied deferred request otherwise caused one unsolicited frame).
- Regression: `go test -race -count=1 ./internal/vmhost -run TestHostConsoleIncrementalUpdateOnlyOnChange` over a real PTY: no reply to unchanged incremental, deferred frame after echo, no unsolicited frame after a satisfied request (fails with "output pushed an unsolicited frame after a satisfied incremental request" without the `wantUpdate = false` line). Both phase1 workflows.
- Avoid: conflating "send a frame" with "the client asked for one" when the protocol has an incremental flag.

## BUG-0053 — Host console KeyEvent handling dropped Ctrl+C/D/Z and arrow keys

- Date: 2026-09-10
- Symptom: `keysymByte` had an unreachable lowercase branch; Ctrl+C/D/Z arrived as `c`/`d`/`z` and arrow keys were dropped.
- Root cause: no Control state tracking (key-up events skipped entirely) and no arrow/Home/End cases.
- Fix: per-connection `keyState.ctrl` from Control_L/R (0xffe3/0xffe4); `keysymByte(sym, ctrl)` maps 0x40..0x5f and lowercase to `sym & 0x1f`, Ctrl+Space to NUL, adds Left/Up/Right/Down/Home/End sequences. Second round added non-letter controls (Ctrl+\ and Ctrl+] had reached the shell literally). Ctrl+C added to `Send key…` in `web/src/console.tsx`.
- Regression: `go test -race -count=1 ./internal/vmhost -run TestKeysymByteCtrlAndArrows` (covers Ctrl+C/D/Z, arrows/Home/End, Ctrl+\ 0x1c, Ctrl+[ 0x1b, Ctrl+@/Space 0x00, plain `\`); both phase1 workflows; `npm run typecheck` passes.
- Avoid: leaving an unreachable branch as "the intended hook" without wiring the modifier state it needs.

## BUG-0054 — Host console tab expansion emitted roughly half the intended spaces

- Date: 2026-09-10
- Symptom: `"\t"` from column 0 left `t.cur == 4` (expected 8); `"abc\t"` left 6.
- Root cause: loop bound `8-(t.cur%hostCols)%8` re-evaluated while `put()` advanced `t.cur`.
- Fix: compute the count once in `hostTerm.write` (case `'\t'`).
- Regression: `go test ./internal/vmhost -run TestHostTermTabStopsAtNextMultipleOfEight`; both phase1 workflows.
- Avoid: loop bounds over mutable state the body advances.

## BUG-0055 — HostConsoleSocket cached a one-time Listen failure for the process lifetime

- Date: 2026-09-10
- Status: moot — resolved by BUG-0051, no separate change.
- Symptom: `hostConsoleOnce sync.Once` would cache a first `net.Listen` failure forever until agent restart.
- Root cause/fix: BUG-0051 removed the listener and `sync.Once`; `HostConsoleSession()` starts a fresh `net.Pipe` and PTY per call.
- Regression: BUG-0051's `TestHostConsoleSessionRFBHandshake` and `TestHostConsoleHasNoLocalListener`.
- Avoid: n/a — recorded for traceability.

## BUG-0056 — openHostConsole let an old Core hang the browser instead of refusing it

- Date: 2026-09-10
- Symptom: `openHostConsole` gated on `console-v1`, which pre-host-console agents also report; the browser waited 20 s for a 504 instead of an immediate "upgrade Nex Core" 409.
- Root cause: host console reused the VM console capability constant.
- Fix: `protocol.HostConsoleCapability = "host-console-v1"` (`internal/protocol/console.go`), reported by `internal/agent/run.go`, gated in `internal/controller/console.go` and in `hostConsoleBlocker` (`web/src/main.tsx`, missed in first round). Host console 404/409 and ticket wording corrected from VM-console copy.
- Regression: `go test ./internal/controller -run TestHostConsoleTicket` (409 with only `console-v1`, success with both); both phase1 workflows. UI gate has no unit test (typecheck/build and inspection only; limit recorded in `docs/changes/2026-09-10-host-console-hardening.json`).
- Avoid: sharing a capability constant between two features when old deployments can advertise it without the new feature.

## BUG-0057 — Interrupted VM media insert could reconcile as succeeded with an empty tray

- Date: 2026-09-10
- Symptom: if the agent died before or during `virsh change-media` for an insert, reconciliation reported `succeeded` and mirrored the ISO as inserted while the tray was empty.
- Root cause: `apply()` persisted stage `changing-media` with empty `MediaPath`; `changeMedia` set the path only in memory, so `mediaObserved` matched an empty tray against an empty want.
- Fix: `changeMedia` saves stage `changing-media` and the resolved `MediaPath` in one journal write immediately before `virsh change-media` (insert and eject); `apply()` sets no stage, so a journal without it re-applies; removed the empty-path special case in `mediaObserved`.
- Regression: `go test -race -count=1 ./internal/vmhost -run TestManagedMediaChange` (m11: unresolvable ISO must not journal `changing-media`, then re-applies with `change-media ... --update`); hand-revert fails with `nothing was issued, so the journal must not claim a media change was in flight`.
- Avoid: keeping reconciliation-critical state only in memory before an interruptible side-effecting call.

## BUG-0058 — VM media eject zeroed the boot order without renumbering, orphaning boot-order-dependent VMs on re-adoption

- Date: 2026-09-10
- Status: fixed; relaxing boot-order contiguity awaits project owner confirmation.
- Symptom: a VM with ISO first (`media_boot_order=1`, disk `boot_order=2`) had its media ejected, leaving disk 2 and nothing at 1; `protocol.ValidateVMSpec` rejected it, so Core's `journals()` dropped it from `Applied()` and Controller `adoptManaged` skipped it — a replacement Controller would never re-adopt the running VM.
- Root cause: `recordVMMedia` (`internal/vmhost/managed.go`) and `mirrorVMMedia` (`internal/controller/vmmedia.go`) independently zeroed media fields without closing the gap. The first-round renumbering fix made the record lie, since `virsh change-media` never touches `bootindex`.
- Fix: `protocol.ApplyVMMedia` records only `media_id` (insert invents no boot order; eject clears `media_id` and `media_boot_order`, disks untouched); `ValidateVMSpec` requires unique positive boot orders but no longer contiguous (libvirt tolerates gaps; `web/src/vm-create.tsx` still submits 1..N). Rewriting domain bootindex values was rejected as more code for no operator gain.
- Regression: `go test -race -count=1 ./internal/protocol ./internal/vmhost ./internal/controller` — `TestApplyVMMediaKeepsBootOrder`, `TestManagedRecordVMMediaKeepsBootOrderOnEject`, `TestMirrorVMMediaKeepsBootOrderOnEject`, plus gap cases in `protocol/managed_test.go`; renumbering revert fails all three.
- Avoid: mutating a derived order-dependent field independently on Core and Controller, or without maintaining the invariant the rest of the system assumes.

## BUG-0059 — Ejecting an already-empty CD tray failed instead of succeeding, and could attach a tray just to fail ejecting from it

- Date: 2026-09-10
- Symptom: eject on an empty tray (retry, out-of-band eject, or stopped VM with no drive) failed with "libvirt refused the media change; inspect the VM on its host"; a driveless stopped VM first got an empty tray attached by `ensureCDROM`; stale `media_id` could not be cleared.
- Root cause: `changeMedia` always ran `ensureCDROM` then `virsh change-media ... --eject`; virsh refuses to eject a source-less cdrom even with `--force`. The first short-circuit read only the persistent definition and `<source file>`, so a running VM with out-of-band or `<source dev>` media was reported ejected.
- Fix: `cdromSource` takes `running`, reads the live definition for running domains (`dumpManagedDomain`), and treats `<source file>` or `<source dev>` as present; `mediaObserved` uses the same helper; one args builder for insert and eject.
- Regression: `go test -race -count=1 ./internal/vmhost -run 'TestManagedMediaChange|TestManagedMediaEjectReadsLiveDefinition'` (inactive empty, live `<source dev="/dev/sr0">` → expects `change-media <vm> sda --force --live --config --eject`); revert fails.
- Avoid: issuing side-effecting host commands unconditionally when a cheap read shows the operation is already satisfied.

## BUG-0060 — A VM media change broke idempotent replay of the original create request

- Date: 2026-09-10
- Symptom: after a media change, replaying the original create `request_id` returned 409 "request_id already used with another payload" instead of 200.
- Root cause: `mirrorVMMedia` (`internal/controller/vmmedia.go`) rewrote both `command` and `payload` of the `managed_resources` row; `replayResource` compares `payload` byte for byte.
- Fix: update only `command` (the live mirror); leave `payload` (replay input) untouched.
- Regression: `TestVMCreateReplayStaysIdempotentAfterMediaChange` in `internal/controller/vmmedia_test.go`; revert fails with `POST /api/v1/vms: got 409 want 200`. Runs under `go test -race` in both phase1 workflows.
- Avoid: treating a stored request payload as a live mirror of resource state.

## BUG-0061 — Playwright Back locator ambiguous against the new fixed toolbar

- Date: 2026-09-10
- Symptom: `tests/integration/managed_vm_browser.py` failed Playwright strict-mode resolution at the wizard step-back assertion (recorded in `docs/changes/2026-09-10-iso-upload-task-row.json`).
- Root cause: the new always-visible toolbar in `web/src/main.tsx` added a second button named exactly "Back" alongside the wizard's in `web/src/vm-create.tsx`.
- Fix: scope the locator to `get_by_role('region', name='Create VM', exact=True)`; renaming the toolbar button rejected because `tests/integration/phase1_browser.py` asserts its name.
- Regression: `tests/integration/managed_vm_browser.py --simulated` passes all 5 phases locally; both phase1 workflows run it.
- Avoid: unscoped `get_by_role(..., name='Back')` where the toolbar exists; scope to the landmark.

## BUG-0062 — Create VM draft restore replaced an explicit empty NIC list with one blank NIC

- Date: 2026-09-10
- Symptom: after a lost submit with 0 NICs, the reopened wizard showed one NIC with an empty required Network, and choosing one made `web/src/managed.tsx`'s retry throw on payload mismatch.
- Root cause: `web/src/vm-create.tsx` used `Array.isArray(r.nics) && r.nics.length ? r.nics : blank.nics`, treating `[]` as absent.
- Fix: exported pure `restoredNICs(nics)` falls back only when not an array; `VMWizard` uses it.
- Regression: new `web/src/vm-create.test.mjs` (`npm --prefix web run test`, esbuild transform with stubbed imports) asserts `[]` stays `[]`, non-arrays fall back, populated arrays preserved; fails on old logic. Added to both phase1 workflows after typecheck; `npm run typecheck && npm run build` pass.
- Avoid: truthiness/length checks to distinguish "never set" from "deliberately cleared".

## BUG-0063 — Deploy guide claimed guest root console login stays locked

- Date: 2026-09-10
- Symptom: `docs/guide/deploy-controller-appliance.md` said "guest root stays locked", contradicting the next sentence and `appliance/firstboot.py`.
- Root cause: guide not updated when `provision_console_login()` began unlocking root with the deploy password (`chpasswd -e`, `passwd -u`) and adding it to `sudo` where present.
- Fix: guide states first boot unlocks the root console with the deploy password, may grant `sudo`, and that the Controller admin password therefore gives a console root shell; SSH password login stays disabled (firstboot never touches sshd).
- Impact: guide updated (`docs/changes/2026-09-10-wizard-draft-back-locator.json`).
- Regression: docs accuracy, no automated check; re-read `appliance/firstboot.py` against every clause; `python3 scripts/check_docs.py --worktree` passes.
- Avoid: letting security claims in a guide drift from the provisioning script; grep the guide when `appliance/firstboot.py` changes.

## BUG-0064 — Create VM dropdowns still flickered closed: the OS popup was being dismissed

- Date: 2026-09-10
- Symptom: after BUG-0050, every native `<select>` in Create VM still opened and closed at once.
- Root cause: not the Controller — the compositor (Hyprland 0.56.2, Brave 152 on Wayland) dismisses the native picker popup when the pointer moves; reproduced with a page containing only a `<select>`, while the Create VM popup stayed open 6 s with a still pointer.
- Fix: `select, ::picker(select) { appearance: base-select }` with picker/option styling in `web/src/style.css` and `web/src/local.css`; Chromium 135+ renders the picker in the top layer; other browsers keep the native picker.
- Regression: `tests/integration/form_select_picker.py` (replaces `form_select_overflow.py`) requires `appearance: base-select`, `:open` after click and re-selection, picker below the control, ArrowUp + Enter choosing SATA; fails on the old stylesheet. Both phase1 workflows. Evidence: [QA record](qa/form-select/2026-09-10-base-select.md).
- Avoid: changing page CSS around a compositor-dismissed native popup; trusting one early screenshot; Playwright synthetic clicks do not move the real pointer — use real input or an in-page picker.

## BUG-0065 — One corrupt managed journal file emptied the host's reported pool state

- Date: 2026-09-10
- Symptom: one unreadable file in `<agent-state>/managed-vms/` made the report's `Inventory.managed` empty, so a fresh Controller adopted nothing, with no signal why.
- Root cause: `ManagedEngine.Applied` (`internal/vmhost/managed.go`) returned empty on any `journals()` error, and `journals()` aborted on the first bad file — right for `Execute`, wrong for reporting.
- Fix: reporting uses `readable()`, skipping and naming bad files; `Applied` adds a `managed-journal` check (`failed` with file names). `Execute` stays fail-closed. Same change: `Applied` reports failed/needs_attention outcomes, re-observes interrupted creations, flags VMs missing from libvirt; agent reports the appliance deploy journal; Controller adoption records unadoptable entries (`docs/changes/2026-09-10-adoption-completeness.json`).
- Regression: `go test ./internal/vmhost -run TestAppliedSurvivesCorruptJournalEntry` (strict read fails with `corrupt file emptied the applied state`); `go test -race ./...` in both phase1 workflows; `tests/integration/controller_replacement.py` seeds `corrupt.json` and asserts other entries adopt and the check names it.
- Avoid: sharing one fail-closed reader between mutation and read-only reporting paths.

## BUG-0066 — A failed VM edit reserved its revision, so every retry of that edit was refused

- Date: 2026-09-10
- Symptom: after a host-side VM edit failure, every later edit failed with *resource ID already reserved by another operation*; the Controller kept resending the same next revision.
- Root cause: `ManagedEngine.Execute` (`internal/vmhost/managed.go`) reserved `vm-update:<vm>:<revision>` for entries of any status, so a `failed` edit held a revision it never produced. `promote` only re-observed interrupted `vm-create` entries, and Controller `adoptManaged` never closed a settled `adopt-outcome:<host>:<op>` needs_attention row.
- Fix: skip `failed` `vm-update` entries in the reservation loop (`revision == current + 1` still serialises retries); `promote` re-observes interrupted `vm-update` entries via `observeDefinition`; `adoptManaged` marks adopt-outcome rows succeeded once absent from host outcomes.
- Regression: `go test ./internal/vmhost -run 'TestManagedFailedUpdateDoesNotReserveRevision|TestAppliedPromotesInterruptedEdit'` and `go test ./internal/controller -run TestAdoptedOutcomeClosesWhenHostSettlesIt` failed before, pass after; CI via `go test -race ./...` in both phase1 workflows.
- Avoid: keying a reservation on a value the operation has not yet produced; release it when nothing changed, or key it on the request.

## BUG-0067 — A needs_attention VM edit locked its revision; libvirt CPU check=none failed observation

- Date: 2026-09-11
- Symptom: after an edit ended *needs_attention* with *VM CPU or machine changed*, every later edit failed with *resource ID already reserved by another operation*.
- Root cause: `validateManagedDefinition` required an exact CPU `check` match, but libvirt injects `check='none' migratable='on'` on host-passthrough when omitted; BUG-0066 skipped only `failed`, not `needs_attention`, `vm-update` entries.
- Fix: write `check='none'` for host-passthrough/host-model, tolerate the injected value, and skip `needs_attention` `vm-update` entries in the reservation loop.
- Regression: `go test ./internal/vmhost -run 'TestManagedNeedsAttentionUpdateDoesNotReserveRevision|TestLibvirtCPUCheckNoneAccepted|TestManagedDefinitionTampering|TestManagedFailedUpdateDoesNotReserveRevision'` failed before, passes after; CI via `go test -race ./...` in both phase1 workflows.
- Avoid: treating `needs_attention` as a lasting reservation, or libvirt-injected CPU attributes as tampering.

## BUG-0068 — VNC lossy wrapper omitted from the corresponding-source archive

- Date: 2026-09-11
- Symptom: release build of `nex-core-local` failed: `install: cannot stat 'packaging/core-local/qemu-vnc-lossy': No such file or directory`.
- Root cause: same class as BUG-0045; the extensionless file matched no `packaging/core-local` TREES suffix and was absent from `scripts/build-source.py` FILES.
- Fix: add it to FILES; keep `MaintainerScriptRetentionTests` before `if __name__`.
- Regression: `python3 -m unittest tests.test_build_source -v` fails naming `qemu-vnc-lossy` without the entry, 5 pass with it; phase1 `Check contracts and contributor tools` in both workflows.
- Avoid: adding packaging paths via `build-inside.sh` without listing extensionless files in FILES.

## BUG-0069 — Console emulator wrapper blocked VM definition

- Date: 2026-09-11
- Status: fixed in code; deployment/recovery acceptance on the affected build remains open.
- Symptom: libvirt reported permission denied probing `/usr/lib/nex/qemu-vnc-lossy`, blocking appliance and managed VM definition.
- Root cause: the console quality change replaced libvirt's emulator with an executable outside confined-libvirt policy paths; mode 0755 grants no policy permission. AppArmor is consistent but the host audit log was not inspected.
- Fix: remove the override in both domain builders and the runtime installer; keep the packaged wrapper for existing definitions (not rewritten). Lossy JPEG stays unavailable.
- Regression: `TestApplianceDomainUsesDefaultEmulator`, `TestManagedDomainXMLUsesDefaultEmulator` failed before; `go test -race ./internal/vmhost ./internal/localcore` and `python3 -m unittest tests.test_build_source -v` pass; both phase1 workflows. Local checks only: no AppArmor host, browser walkthrough, rebuilt package or hosted CI.
- Avoid: replacing a hypervisor executable for a display preference, treating chmod as policy compatibility, or redeploying before the retained operation, definition and disk ownership are reconciled.

## BUG-0070 — Package-update policy test retained the removed upgrade-only assertion

- Date: 2026-09-11
- Symptom: CI failed in `Check contracts and contributor tools` after the first-install update change.
- Root cause: `test_core_updater_uses_apt_and_not_dpkg_install` still required `--only-upgrade`; `ApplyLocal` correctly uses plain `apt install`.
- Fix: assert plain apt install and reject the upgrade-only form.
- Regression: `python3 -m unittest tests.test_distribution_packaging.AptUpdatePathTests -v`; both phase1 workflows via unittest discovery.
- Avoid: keeping a source-text assertion after intentionally changing the update path.

## BUG-0071 — Website browser check asserted release copy the site no longer had

- Date: 2026-09-11
- Symptom: `tests/integration/website_browser.py` failed at `assert 'Not available yet' in page.locator('#releases').inner_text()` at `HEAD`.
- Root cause: the status section was reworded ("In development", "not a supported production release"); the assertion pinned retired wording instead of the claim boundary.
- Fix: assert the current wording and disclaimer; keep the no `.iso`/`.deb`/`.qcow2` link assertion.
- Regression: failed at `HEAD`, passes all 12 groups including JavaScript-disabled; both phase1 workflows. Evidence: [website roadmap QA](qa/website-roadmap/checks.md).
- Avoid: asserting exact marketing copy; pin the property (no public-release promise, no media links).

## BUG-0072 — Site header navigation could not wrap, so a sixth item overflowed at 360px

- Date: 2026-09-11
- Symptom: with the Roadmap link, `scrollWidth` 411 vs `innerWidth` 360; overflow from `nav` and `.nav-release`.
- Root cause: `.site-header nav` in `website/src/app.css` had `display: flex` without `flex-wrap` and a narrow `flex-basis: 100%`/`space-between` rule; the Gallery page's local `flex-wrap` hid it.
- Fix: allow wrapping with a row gap in the shared rule; left-align wrapped narrow rows.
- Regression: `scrollWidth <= innerWidth` assertion in `tests/integration/website_browser.py` failed at 360px before, passes after (now also on roadmap page); both phase1 workflows; 360/768px captures inspected.
- Avoid: fixing a shared layout defect in one page's scoped styles.

## BUG-0073 — Private identities appeared in public repository content

- Date: 2026-09-11
- Symptom: docs, QA evidence, installer examples and publishing defaults exposed personal names, private infrastructure and environment identifiers.
- Root cause: lab output kept without anonymization; privacy check covered website inputs only; APT bootstrap hard-coded the sudoers account.
- Fix: sanitize current files with role labels and anonymous aliases, require configured publishing accounts, add the AGENTS.md public-repository rule.
- Regression: `python3 -m unittest tests.test_repository_privacy tests.test_apt_bootstrap_privacy tests.test_apt_publish tests.test_website_public_files tests.test_ci_security tests.test_api_contract tests.test_build_source -v` — 36 passed; both phase1 workflows discover them; hosted CI not run. Full discovery: 135 tests, one nginx-unavailable setup error. Evidence: [privacy cleanup](qa/2026-09-11-public-repository-privacy.md).
- Avoid: private identities in fixtures, website-only scans as repository coverage, or claiming file cleanup removes Git history.

## BUG-0074 — Website browser check crashed against the hardened deployment's image policy

- Date: 2026-09-11
- Symptom: against the deployed backend, `website_browser.py` aborted with `Page.evaluate: EncodingError: The source image cannot be decoded`.
- Root cause: the logo backdrop check reloads a `data:image/png;base64,` capture; deployed nginx `img-src 'self'` correctly blocks `data:`.
- Fix: run the pixel assertion only when CSP `img-src` permits `data:`; otherwise record `logo backdrop SKIPPED (CSP img-src blocks data:; covered by build-hash match)`. Policy not weakened.
- Regression: deployed run aborted before, passes 12 groups with recorded skip after; local build still runs the pixel assertion, as does CI (`python3 -m http.server` on `127.0.0.1`).
- Avoid: relaxing security headers for a test, or reporting an unexecuted check as passed.

## BUG-0075 — Releases stopped publishing to the website and the pipeline stayed green

- Date: 2026-09-11
- Status: fix makes the failure visible; publishing still outstanding until secrets are configured.
- Symptom: the website's latest download stayed at 0.1.6~alpha1 while the changelog was at 0.1.8; downloads and APT roots on the host held only `0.1.6~alpha1` despite `--keep 3`.
- Root cause: `Check for deploy credentials` wrote `enabled=false` and continued when any deploy secret was unset, after `Create or replace the release` had run, so tags and releases were created green with the website skipped. Commit `0b1d12e` made `NEX_DEPLOY_USER` a fourth required secret; the workflow was unchanged between `v0.1.6-alpha1` and `v0.1.7-alpha1`.
- Fix: all four deploy secrets enable publishing; none skips with a notice; any partial set fails and names the missing secret.
- Regression: `python3 -m unittest tests.test_ci_security` — 5 OK, executing each workflow's extracted step shell; restoring the old logic fails 4 of 5. Both phase1 workflows via unittest discovery.
- Outstanding: configure `NEX_DEPLOY_USER` (and confirm the other three) in Gitea; 0.1.7 and 0.1.8 remain unpublished. Run logs were unreadable (no API token); skip-vs-failure rests on host-side evidence and step order.
- Avoid: gating publishing on a condition whose false branch only prints; distinguish "not configured" from "configured wrong" and fail the latter.

## BUG-0076 — VM display names were restricted to hostnames

- Date: 2026-09-11
- Symptom: create/rename rejected names with spaces, uppercase, dots or Unicode.
- Root cause: duplicated hostname regexes in UI, Controller create/update/adoption and Core validation, although UUIDs own paths and operations.
- Fix: shared bounded nonblank display-name validation, exact Core journal/libvirt retention, unchanged disk-label sanitization; preserve surrounding spaces in duplicate checks; terminate virsh options before rename arguments.
- Regression: `go test ./...`, `npm --prefix web test`, UI typecheck/build and `tests/integration/managed_vm_browser.py --simulated` pass; old guards fail new Core checks; both phase1 workflows. Evidence: [VM display names QA](qa/vm-display-names/checks.md).
- Avoid: a frontend-only alias or relaxed UI regex while host, update or adoption paths keep hostname checks.

## BUG-0077 — Console quality presets advertised unavailable image changes

- Date: 2026-09-11
- Symptom: Low bandwidth, Balanced and Best image looked identical.
- Root cause: noVNC encoding preferences reached a Core VNC server without lossy JPEG; the BUG-0069 wrapper was removed but the selector stayed; the lab assertion only checked frames appeared.
- Fix: remove the selector, saved-quality handling and private refresh hook; keep View and noVNC defaults.
- Regression: `tests/integration/console_controls_browser.py` failed on the old build, passes 4 groups after; both phase1 workflows. Evidence: [console quality QA](qa/console-quality/checks.md).
- Avoid: treating a successful setter or visible framebuffer as proof of quality change, or replacing the hypervisor executable.

## BUG-0078 — Start repeatedly reopened a busy-error confirmation

- Date: 2026-09-11
- Symptom: Start was refused while a power/edit task was pending; each confirmation repeated the busy error.
- Root cause: Controller enforced one pending action, Core picked persisted commands from an unordered map, and enrollment discarded pending work; removing the guard alone could start a VM before its edit finished.
- Fix: capability-gated power queue with Core-owned durable FIFO and same-VM successful prerequisites, retained across restart/replacement with passive task adoption; UI reports queued. Failures still need recovery, not retry.
- Regression: `go test ./...`, UI typecheck/test/build, 21 API contract tests and `tests/integration/phase1.py --browser` pass; prerequisite-rejection checks failed before; both phase1 workflows. Evidence: [power queue QA](qa/power-queue/checks.md).
- Avoid: Controller/browser retry timers, dropping the guard without ordered Core execution, or replaying uncertain power effects.

## BUG-0079 — Console ISO progress remained after successful media change

- Date: 2026-09-11
- Symptom: CD/DVD showed Empty after eject but Ejecting ISO… never cleared; insert likewise.
- Root cause: the UI wrote a progress string on API acceptance without following the operation or host media state.
- Fix: track the operation and requested media; clear on completion; retain failure/needs-attention reasons after task eviction.
- Regression: `tests/integration/console_controls_browser.py` reproduced the stuck message, passes completion/failure/eviction checks; UI build/typecheck pass; both phase1 workflows. Evidence: [console media status QA](qa/console-media-status/checks.md).
- Avoid: a timeout that hides work or errors, or hiding the string while an old queued state can reappear.

## BUG-0080 — Pool registration forms stayed open on unrelated tabs

- Date: 2026-09-11
- Symptom: **Register network**, **Upload ISO** or **Create VM** stayed above other pool views until **Close**.
- Root cause: forms rendered from one `form` state no view checked.
- Fix: hide each form outside the view that opens it, keeping it mounted so entries and wizard progress survive.
- Regression: `web/src/managed.test.mjs` fails pre-fix (`network form on Virtual machines: wrong visibility`), passes after via `npm --prefix web run test` in both phase1 workflows. Evidence: [pool tab forms QA](qa/pool-tab-forms/checks.md).
- Avoid: unmounting on tab change; the wizard's **Open Storage**/**Open Networking**/**Open ISO library** buttons switch tabs and would lose a part-completed VM.

## BUG-0081 — Dedicated VM memory was promised but not physically reserved

- Date: 2026-09-11
- Symptom: a VM with 4 GiB dedicated RAM consumed about 1 GiB on the host.
- Root cause: Core set `memory`/`currentMemory` and disabled ballooning but omitted libvirt memory backing, allowing on-demand allocation, swap and KSM.
- Fix: the shared definition builder preallocates, locks and disables page sharing; observation verifies backing. Legacy dedicated VMs need shutdown, stopped save and start; legacy live edits fail before device effects.
- Regression: `go test ./internal/vmhost -run 'TestManaged(DedicatedMemoryReservation|LegacyDedicatedLiveEditRequiresStop)$' -count=1` failed before, passes after; both phase1 workflows; no hosted run. Native `TestDedicatedMemoryLibvirt` needs `NEX_MEMORY_LIBVIRT_TEST=1` on a disposable host (skipped otherwise). Evidence: [dedicated memory QA](qa/dedicated-memory/checks.md).
- Avoid: treating equal memory values, balloon removal or admission arithmetic as physical reservation; guessing a QEMU hard limit that can kill a guest.

## BUG-0082 — Storage adoption and startup validation lost Core evidence

- Date: 2026-09-11
- Status: code fixed; packaged service and live backend acceptance remain open.
- Symptom: a successful native NFS registration stayed queued without its Core ID; source replacement and filesystem access lacked startup/freshness checks.
- Root cause: Controller matched a cleared projection operation ID instead of Core's outcome; event replay depended on filename order; filesystem-only observations did not prove QEMU access. The remote directory helper also created VM directories `0700` under the service's `0077` umask.
- Fix: correlate adoption via Core outcome, fold generations and tombstones, retain backend identity/capabilities, validate stored sources on adoption, bound QEMU access observations, and guard libvirt startup against missing/replaced repositories and aliased disks. The shared creator sets `0711` on new Core directories through a no-follow descriptor with inode checks; existing directories unchanged.
- Regression: `go test ./internal/vmhost -run 'TestStorageStartup|TestStorageGuard|TestStorageMonitor' -count=1`, `TestManagedDirectoryPreservesQEMUTraversalUnderPrivateUmask` and full `go test -race ./...` pass; `tests/integration/storage_repositories_browser.py`; both phase1 workflows. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: treating a mount or green mocked test as proof of source identity, VM write access, adoption or recovery.

## BUG-0083 — First console connection had a zero-sized Fit viewport

- Date: 2026-09-11
- Symptom: a guest showed a login prompt over direct VNC but the browser console was black until Actual size.
- Root cause: noVNC autoscaling ran before layout, leaving zero canvas CSS size.
- Fix: after connection and layout, reset scaling and reapply the view; ignore callbacks for replaced clients.
- Regression: `tests/integration/console_controls_browser.py` asserts a visible canvas; both phase1 workflows; disposable Debian guest Fit showed login at 1280×800. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: equating a connected WebSocket or nonempty framebuffer with a visible console.

## BUG-0084 — Timed-out VM power requests were reported as failed after taking effect

- Date: 2026-09-11
- Symptom: during an NFS outage test a start was reported failed but the VM had started (same request, per correlated timestamps).
- Root cause: the result classifier treated a libvirt error after the persisted power-effect boundary as definitive failure, in both execution and pre-effect replay paths; client timeout does not cancel an accepted request.
- Fix: classify errors from the actual power request as `needs_attention` in both paths, keep native diagnostics; preflight refusals stay failed; reconciliation observes without reissuing; reboot stays uncertain.
- Regression: `TestManagedPowerRequestCancellationSettlesFromObservation`, `TestManagedPowerPostEffectFailureIsUncertain` pass; both phase1 workflows via `go test -race ./...`. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: treating a timeout as proof of no side effect, or retrying the power effect for a success response.

## BUG-0085 — SMB repository identity changed after host restart

- Date: 2026-09-11
- Symptom: an unchanged SMB share and saved VM were refused after Core restart because the CIFS filesystem ID changed.
- Root cause: Linux CIFS `statfs` identity includes volume creation time, which Samba derives from the share root and is unstable.
- Fix: new registrations create/reuse `.nex-storage-marker` (bounded, regular, single-link, no-follow, synced), persist its ID in Core and verify with exact source/options; markers decide backing aliases; missing/altered markers fail closed; legacy records stay strict.
- Regression: `TestSMBMarkerDurableIdentity`, `TestSMBMarkerRejectsUnsafeFilesAndConcurrentCreate`, `TestSMBMarkerDirectorySyncPolicy`, `TestSMBMarkerLegacyFSIDRemainsStrict`, `TestSMBMarkerBackingConflictPrefersDurableMarker` pass; both phase1 workflows. Native acceptance: [storage QA](qa/storage-repositories/checks.md).
- Avoid: truncating an unstable identifier to match a journal, or auto-replacing identity after a mismatch.

## BUG-0086 — Managed libvirt observation could delay heartbeat reporting

- Date: 2026-09-11
- Symptom: report construction let a managed-VM libvirt list consume its full native timeout before the heartbeat.
- Root cause: the report path passed its transport context to managed observation without `ProbeTimeout`. Found during an outage attempt against an obsolete Controller endpoint, which does not prove runtime causality.
- Fix: bound report-time observation by `ProbeTimeout` in a cancelled child context; unverified definitions are unknown, not missing.
- Regression: `TestReportBoundsManagedLibvirtObservation` (100 ms budget still delivers report) passes; both phase1 workflows via `go test -race ./...`.
- Avoid: lengthening heartbeat test waits to hide synchronous work, or naming an outage cause without verifying the Controller endpoint and clock.

## BUG-0087 — Duplicate iSCSI registration could disturb an owned session

- Date: 2026-09-11
- Symptom: a second registration of the same test LUN reached login and its cleanup disturbed the first session; detach failed when the session was already absent.
- Root cause: source equality included the per-ID mount path; rollback/detach did not distinguish pre-existing sessions or leftover receipts; session parsing expected invented output instead of Debian `host:port,tpgt`, so detach deleted a node while logged in.
- Fix: compare canonical target/portal and WWID/filesystem claims before native effects (one repository per session); ignore generated paths; rollback logs out only sessions it created; detach verifies ownership and requires typed exit 21 for absence; strict `host:port,tpgt` parsing including bracketed IPv6.
- Regression: `TestISCSIRegistrationConflictPreventsSecondSessionPreparation`, `TestISCSISourceChangeIgnoresCoreMountPathButConflictsWithOtherRepository`, `TestISCSINoActiveSessionsErrorRequiresTypedExit21`, `TestISCSISessionPortalStrictGrammar`, `TestDetachISCSICleansOwnerAfterNodeDeleteCrash` pass; both phase1 workflows. Live duplicate rejected before initiator activity. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: using allocated mount paths to distinguish ownership of one LUN, or ignoring logout errors to fake idempotence.

## BUG-0088 — Debian confinement and hook discovery blocked storage startup protection

- Date: 2026-09-11
- Symptom: AppArmor denied libvirt's prepare hook executing the Core-local storage guard; a hook added to a running daemon was not discovered.
- Root cause: Debian's libvirtd profile did not permit the executable; `qemu.d` hooks need daemon restart, not unit reload.
- Fix: maintain one exact `rix` rule in the administrator local include without owning it (reject malformed blocks and non-regular files, preserve bytes/owner/mode); require a parser when reload is needed; restart only an active libvirtd; hook ignores reconnect events.
- Regression: `test_packaged_hook_preserves_admin_apparmor_policy` and seven storage packaging tests pass; both phase1 workflows via unittest discovery. Native reload/restart kept a running SMB guest. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: owning the administrator local include, disabling AppArmor, treating a static hook file as activation, or skipping activation while a guest runs.

## BUG-0089 — NFS statfs identifiers falsely collided across distinct filesystems

- Date: 2026-09-11
- Symptom: a separate NFS export was rejected as already registered; both `statfs` filesystem IDs were zero.
- Root cause: Linux NFS `statfs` gives no usable protocol identity; equal unavailable values implied common backing.
- Fix: bind the mounted path's device to exactly one NFSv4 kernel volume-table record; store protocol FSID with server address and port; missing/malformed/ambiguous identity blocks use; `0:0` protocol FSID is valid; legacy records stay unavailable.
- Regression: `TestNFSKernelFilesystemIdentity`, `TestNFSKernelFilesystemIdentityBindsPathDevice`, `TestNFSKernelFilesystemIdentityIPv6AndZeroProtocolFSID`, `TestNFSKernelFilesystemIdentityRejectsUnavailableOrAmbiguousRows`, `TestNFSKernelFilesystemIdentityChangesWhenServerOrFilesystemChanges`, `TestNFSIdentityRejectsLegacyStatfsAndChangedProtocolProfiles` pass; both phase1 workflows. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: treating zero statfs values as identity, dropping duplicate detection, comparing unnamespaced FSIDs across servers, or silently replacing stored identity.

## BUG-0091 — Concurrent SMB marker create failed closed on the loser

- Date: 2026-09-11
- Symptom: `TestSMBMarkerRejectsUnsafeFilesAndConcurrentCreate` failed under `go test -race ./...` with `SMB repository marker is unavailable` or `invalid`.
- Root cause: `smbMarkerAt` treated a lost `O_EXCL` create as fatal, and a reader seeing the winner's file before the 32-byte ID was written judged it permanently invalid.
- Fix: retry short reads after a lost create race; remove a marker that fails to persist.
- Regression: `go test -race ./internal/vmhost -run TestSMBMarker -count=200` passes (8 concurrent creators); both phase1 workflows.
- Avoid: returning on `EEXIST`, or treating a provisional zero-length marker as a durable identity failure without a short retry.

## BUG-0092 — nex-core-local postinst failed during ISO install on AppArmor hosts

- Date: 2026-09-11
- Status: fixed in packaging; full ISO reinstall on an AppArmor-capable host still required after the next Core package rebuild.
- Symptom: late configure failed with `nex-core-local package post-installation script subprocess returned error exit status 1` after `apparmor_parser` warned `unable to find a suitable fs in /proc/mounts`.
- Root cause: postinst took the live installer kernel's AppArmor-enabled flag as proof the target could reload; `apparmor_parser -r` failed under `set -e` in `in-target`.
- Fix: always write the libvirtd local include; reload only outside a chroot when `/sys/kernel/security/apparmor/.load` exists; first boot reloads.
- Regression: `test_apparmor_live_gate_skips_reload_in_chroot_or_without_load` and packaging assertions in `tests/test_storage_packaging.py`; both phase1 workflows via unittest discovery.
- Avoid: trusting `/sys/module/apparmor/parameters/enabled=Y` inside `in-target`, `[ -w .load ]` as root, or fatal `apparmor_parser` failures in a chroot.

## BUG-0090 — Undersized iSCSI registration left session and mount behind

- Date: 2026-09-11
- Symptom: an undersized iSCSI registration returned `insufficient free storage including reserve` but left session, node and mount.
- Root cause: native preparation marked itself complete before the readiness/capacity check, so refusal skipped detach.
- Fix: keep preparation provisional through capacity, conflict and writable-probe checks and detach on refusal; surface cleanup failure as uncertain; source-changing updates refused after replacement also detach and report uncertain rollback.
- Regression: `TestManagedStoragePrepareRollsBackCapacityRefusal`, `TestManagedStoragePrepareReportsUncertainCapacityCleanup`, `TestPrepareISCSIRollbackRemovesEngineOwnerReceipt` pass; both phase1 workflows. Disposable undersized LUN left no session, node, mount unit or receipt. Evidence: [storage QA](qa/storage-repositories/checks.md).
- Avoid: treating capacity refusal as cleanup, disarming rollback before admission, discarding detach errors, or rolling back with a bare context that drops the engine owner-root.

## Current bug work and unresolved issues

## BUG-0093 — Default ISO library cannot self-register on installed hosts

- Date: 2026-09-12
- Status: fixed; packaged Core and native Controller adoption verified in the disposable lab.
- Symptom: the default **ISO** library at `/var/lib/libvirt/images` was not adopted; manual registration failed with `storage is not traversable by libvirt-qemu: runuser: cannot set user id: Operation not permitted`.
- Root cause: `qemuStorageAccess` runs `runuser -u libvirt-qemu`, but `nex-core-local.service` lacked `CAP_SETUID`, blocking `RegisterDefaults`. (Not `RestrictSUIDSGID`, as first blamed.)
- Fix: add only `AmbientCapabilities=CAP_SETUID` to the packaged Core-local service; keep `NoNewPrivileges`, `RestrictSUIDSGID` and other confinement.
- Regression: `python3 -m unittest tests.test_core_local_package` passes; both Actions providers. Disposable BIOS host adopted the ISO library automatically with the same Core ID after restart. Evidence: [native acceptance](qa/2026-09-12-bug-ledger.md).
- Avoid: weakening the wrong service, removing SUID/SGID protections, or a Controller-owned fallback registration.

## BUG-0094 — Pool-wide remote repository access is not available

- Date: 2026-09-12
- Status: fixed for the scoped NFS/SMB multi-host slice; native acceptance and independent review passed. Local/ZFS/iSCSI remain single-host; Phase 4, migration, master change, ejection, HA and physical hardware not covered; BUG-0097 excluded.
- Symptom: NFS/SMB/ZFS/iSCSI registration bound to one `host_id`; other pool members got no registration, access check or mount/session.
- Root cause: intentional single-host limit; the storage plan deferred multi-host placement and shared-writer protection to Phase 4.
- Fix: authenticated Core-local join gives durable peer trust; members pull master-owned NFS/SMB registrations with IDs and generations and report backing identity, QEMU access and locking; durable reservations fence master mutations; Controller adopts member VMs; UI placement uses fresh member access. iSCSI never becomes multi-writer without a fencing/cluster design.
- Impact: new/changed Core contract; state owner core (pool master plus members), recovered via Core inventory/adoption.
- Regression: `go test -race ./...`, Python/API suites, UI and both browser suites pass; both Actions providers. Covers peer auth/replay, generation convergence, identity mismatch, reservations over mTLS, fresh Controller adoption. Native NFS/SMB walkthrough passed; second iSCSI initiator rejected. Evidence: [native walkthrough](qa/2026-09-12-bug-ledger.md).
- Avoid: copying desired pool state into the Controller, treating local storage as shared, trusting stale access results, or dropping a reservation on an ambiguous operation while a workload holds the repository.

## BUG-0095 — Storage registration lacks discovery and a guided workflow

- Date: 2026-09-12
- Status: fixed; native browser walkthrough and independent review passed.
- Symptom: operators hand-entered servers, exports/shares, paths and targets with no discovery or directory guidance.
- Root cause: no Core discovery contract; registration form was manual-only.
- Fix: bounded Core-native discovery of local/ZFS sources, NFS exports/directories, authenticated SMB shares/directories and iSCSI targets; guided form keeps manual entry and explicit exclusive-initiator confirmation; Controller erases relayed credentials and keeps no discovery inventory. UX correction: SMB browses from server/account/password, NFS from server alone, via dialogs that preserve edits on cancel; spaced and hidden SMB names retained.
- Impact: new Core discovery contracts; Controller retains neither results nor credentials.
- Regression: `go test ./internal/protocol ./internal/vmhost ./internal/agent ./internal/controller`, API contract suite, UI checks and `tests/integration/storage_repositories_browser.py` pass (failed on missing Browse exports before the picker); both Actions providers. Evidence: [native acceptance](qa/2026-09-12-bug-ledger.md), [picker evidence](qa/2026-09-12-network-storage-picker.md).
- Avoid: Controller-side probing, retaining credentials, treating export paths as URI query strings, assuming discovery proves safe shared execution, treating registered storage as destination discovery, requiring a share before browsing, or interpolating folder names into native commands.

## BUG-0096 — Host and pool health summaries omit the observed cause

- Date: 2026-09-12.
- Status: fixed; focused regression, full browser suite and primary native walkthrough passed.
- Symptom: the Hosts table showed only **degraded**; the failed check and reason needed the host detail page.
- Root cause: the summary reduced all non-healthy `checks` (which already carry status, freshness and `error`) to a bare badge.
- Fix: shared summaries show severity, affected-check count, each sanitized Core-reported reason and a detail link; healthy stays compact. Core publishes only explicitly public probe errors.
- Regression: `go test ./internal/agent`, web typecheck/build and `tests/integration/phase1.py --browser` pass (healthy, one/multiple failures, unknown/stale, revoked); both Actions providers run them. Native libvirt-stop walkthrough passed. Evidence: [native acceptance](qa/2026-09-12-bug-ledger.md).
- Impact: API existing contract; no new durable state (Core owns check results).
- Avoid: collapsing all problems to `degraded`, inventing Controller diagnoses or caching causes, or rendering arbitrary command output as a public reason.

## BUG-0097 — Starting a UEFI guest can crash a nested Nex Core lab host

- Date: 2026-09-12.
- Status: unresolved lab-critical defect; root cause not established.
- Symptom: starting a UEFI guest on Nex Core nested under XCP-ng crashes libvirt or the Core guest; BIOS is the comparison path; crash evidence not yet captured.
- Root cause: unknown. Nex emits `firmware='efi'` only when libvirt q35 domcapabilities advertise it; OVMF UEFI worked in a separate nested-KVM lab; earlier nested ISO QA saw OVMF stall for stock Debian too. Leading hypothesis: the partial/unsupported Xen nesting boundary (libvirt may pick a Secure-Boot OVMF with SMM/NVRAM).
- Fix: none yet. Required diagnostics (disposable lab only): domain XML, `virsh domcapabilities`, selected OVMF loader/VARS, QEMU/libvirt log, agent/kernel journals for this and previous boot, `coredumpctl`, package versions, XCP-ng/Xen logs; compare BIOS, default UEFI and stock libvirt UEFI, then a supported non-nested KVM host.
- Regression: none. Acceptance: Core rejects detectable incompatible firmware/runtime with host evidence and never crashes the host; UEFI coverage needed on nested XCP-ng and a supported non-nested host.
- Avoid: repeatedly starting the guest after a crash without collecting evidence, or treating a successful definition or boot menu elsewhere as safety.

## BUG-0098 — Shared VM starts lacked durable pool-master execution admission

- Date: 2026-09-12.
- Status: implementation and independent code review passed; native multi-host acceptance open.
- Symptom: repository reservations let hosts share storage but did not decide which host may execute a VM; native starts/autostart lacked Controller-request ownership protection.
- Root cause: repository access and VM execution treated as one exclusion boundary; no durable member-VM ownership record or epoch-bound admission on the master.
- Fix: peers publish generation-bound VM inventory; master admits ownership only after complete conflict-free observations; native starts consume a one-use pool/host/VM/epoch/boot-bound permit. Confirmed stop invalidates permits without moving placement; timeouts never release ownership.
- Regression: `go test ./internal/agent ./internal/vmhost` (incomplete inventory, owner retention, competing hosts, single-use permits, stale epochs) passed; both `phase1.yml` race jobs run it; no remote CI run claimed. Evidence: [concurrent storage evidence](qa/2026-09-12-concurrent-pool-storage.md).
- Avoid: first-caller ownership, expiring ownership on timeout, using repository locking as VM fencing, keeping a permit after confirmed stop, or re-starting an intentionally stopped VM as "autostart".

## BUG-0099 — Cluster setup relay discarded valid Core lifecycle states

- Date: 2026-09-12.
- Status: reproduced and fixed; complete native workflow acceptance open.
- Symptom: cluster status replies timed out when Core reported awaiting fencing, verifying a fence, or healthy.
- Root cause: the Controller result validator omitted those three states and dropped the whole reply.
- Fix: accept every implemented state; keep unknown-state, size and host/pool checks; no Controller-inferred health.
- Regression: `TestStorageClusterRelayDeliversCoreLifecycleStates` failed before, passes after (`go test ./internal/controller -run TestStorageCluster -count=1`); both `phase1.yml` race jobs; no remote CI run claimed.
- Avoid: adding a Core state without matching relay validation/reference coverage, or treating a transport ack as completed setup.

## BUG-0100 — Shared storage probed a different lock family from QEMU

- Date: 2026-09-12.
- Status: repaired and reviewed; native cross-host QEMU lock evidence passed for NFS, SMB and GFS2; broader outage/HA acceptance open.
- Symptom: a member could pass the lock probe without proving QEMU byte-range locks are exclusive across hosts.
- Root cause: probe used `flock`; QEMU uses OFD/POSIX byte-range locks.
- Fix: nonblocking OFD byte-range locks on both peers; reject unsupported locking; require conflicting acquisition to fail and acquisition after master release.
- Regression: `TestPoolProbeUsesCrossProcessOFDLockFamily` via `go test ./internal/agent -run 'TestPoolProbeUsesCrossProcessOFDLockFamily|TestPoolStorage' -count=1` passed; both `phase1.yml` race jobs. Native six-VM runs recorded QEMU write-lock refusal and release: [concurrent storage evidence](qa/2026-09-12-concurrent-pool-storage.md).
- Avoid: treating a local lock test, another lock family, or a successful mount as cross-host QEMU exclusion.

## BUG-0101 — Healthy managed observations blocked shared VM operations

- Date: 2026-09-12.
- Status: reproduced natively; regression and review passed; scoped multi-host workload passed; broader failure/outage/HA acceptance open.
- Symptom: shared VM creation or inventory publication refused a healthy host.
- Root cause: callers treated any returned observation as an error; the engine returns healthy observations on success.
- Fix: accept only healthy/passed observations; still reject unknown, empty-status and failed.
- Regression: `TestManagedVMChecksReady`, `TestSharedVMCreateAcceptsRealHealthyManagedObservations`, `TestPoolVMInventoryAcceptsRealHealthyManagedObservations` pass in `go test -race ./internal/agent`; both `phase1.yml` jobs.
- Avoid: using a nonempty observation list as a failure flag, or ignoring unhealthy checks.

## BUG-0102 — Stale member generation prevented complete VM inventory

- Date: 2026-09-12.
- Status: reproduced on three hosts; regression and review passed; native retry in progress.
- Symptom: starts failed with incomplete inventory after a third Core joined.
- Root cause: an existing member kept the old membership generation and the master rightly rejected its inventory; partial snapshots also needed retention.
- Fix: retain current-generation partial snapshots without activating owners; return authenticated newer membership on conflict; validate pinned membership; retry once.
- Regression: `TestPublishPoolVMInventoryRefreshesStaleMemberAndAcceptsPartialSnapshot` (real TLS conflict/refresh) and full Agent tests pass; both `phase1.yml` race jobs; no remote CI success claimed.
- Avoid: weakening completeness, accepting changed identity pins, discarding partial observations, or retrying indefinitely.

## BUG-0103 — Replaying unchanged iSCSI configuration requested credential rotation

- Date: 2026-09-12.
- Status: reproduced natively; regression and review passed; complete clustered bootstrap acceptance open.
- Symptom: retrying iSCSI setup demanded safe reconnect though credentials were unchanged.
- Root cause: node renderer accumulated newlines before its marker; byte equality failed when open-iscsi rewrote the node without the marker.
- Fix: idempotent rendering; matching effective auth is a no-op only after inline-marker or exact sidecar ownership validation. Real changes still require safe reconnect; no selected-LUN reconnect bypass.
- Regression: `TestNexISCSINodeConfigIsIdempotentForUnchangedCredentials`, `TestConfigureISCSICHAPActiveLegacyWhitespaceIsNotCredentialRotation`, `TestConfigureISCSICHAPActiveSidecarOwnedMatchingAuthIsNoop` pass (VM-host suite); both `phase1.yml` race jobs.
- Avoid: equating formatting with credential change, adopting unmanaged sessions, or reconnecting a target-wide session for one unmounted LUN.

## BUG-0104 — Fence UI omitted repository scope and offered premature verification

- Date: 2026-09-12.
- Status: repair, Core/browser regressions and review passed; native destructive walkthrough open.
- Symptom: fence verification omitted the repository ID; incomplete bootstrap exposed controls before Core resolved storage identity.
- Root cause: UI dropped the operation's full API scope; Core accepted intent without resolved bootstrap metadata.
- Fix: send the exact repository ID, disable premature controls, reject unresolved configuration before any durable fence request/proof.
- Regression: `TestStorageClusterFenceRejectsUnresolvedBootstrap` failed before, passes after (Agent race suite); cluster browser fixture asserts payload and confirmation/error behavior. Both `phase1.yml` run the fixture and Go checks; no remote CI run claimed.
- Avoid: accepting destructive intent for an unresolved candidate, guessing a repository from a host ID, or permissive mocks ignoring required fields.

## BUG-0105 — Cluster restart recovery could strand setup or repeat fence work

- Date: 2026-09-12.
- Status: regressions and review passed; three stopped-workload native Pacemaker restart/recovery checks passed; interrupted bootstrap and fence-replay acceptance open; final re-review of direct-proof receipt fix open.
- Symptom: interrupted bootstrap stayed unresolved, `applying` could skip native setup, delegated fence outcomes lacked restart-safe reconciliation.
- Root cause: replay rebuilt a different input digest; no durable native-apply completion marker; fence dispatch lacked persisted identity and selected executor.
- Fix: resume the accepted candidate and original digest; idempotently retry missing native receipts; persist fence intent before dispatch bound to one executor; never redispatch ambiguous actions. Expired peer reports are not readiness; partial fencing returns to awaiting proofs; direct proofs and terminal request status save together, keeping prior receipts.
- Regression: `TestClusterOriginalInputDigestSurvivesDurableRoundTrip`, `TestClusterRestartResumesOriginalCandidateWithoutReconstructedDigest`, `TestResumeApplyingClusterWithoutNativeReceiptReappliesDurableConfig`, `TestClusterMasterHealthRefreshReplacesExpiredOwnObservation`, `TestStorageClusterFenceRequestIsDurablyOneShot`, `TestClusterSurvivorFenceReplaySurvivesRestart`, `TestClusterFenceReducerPreservesPartialAndBadProofStates`, `TestClusterProofPollingCannotSkipQueuedBootstrap`, `TestStorageClusterDirectFenceProofRetainsPriorRequestReceipt` pass (Agent race); both `phase1.yml` race jobs.
- Avoid: reconstructing immutable accepted input, treating an `applying` label as native application, or trying another destructive executor after an uncertain reply.

## BUG-0106 — Default-port fence pins and derived secret files did not recover

- Date: 2026-09-12.
- Status: reproduced read-only natively; repair and review passed.
- Symptom: strict SSH rejected a `[host]:22` known-host entry; an existing master's derived credential files did not regenerate on reconciliation.
- Root cause: OpenSSH looks up unbracketed DNS/IPv4 for the default port; derived files were only written when storing/importing a secret.
- Fix: normalize only the unambiguous default-port token in private files, keeping signed receipt material; authenticated peer export revalidates the master record and regenerates derived files without new offers.
- Regression: `TestMaterializedKnownHostsOnlyNormalizesUnambiguousDefaultPort`, `TestClusterCredentialResolutionProjectsRecipientLocalRefs` pass; native strict-pinned SSH then authenticated; both `phase1.yml` race jobs.
- Avoid: disabling host-key verification, rewriting signed receipts, or requiring an expired Controller offer to recover a Core-owned file.

## BUG-0107 — Native virsh fencing used an unsupported selector and lost its prompt

- Date: 2026-09-12.
- Status: reproduced read-only; fixes and review passed; bounded destructive native fencing passed; failed-fence and broader outage acceptance open.
- Symptom: correctly configured targets failed identity/status checks despite working pinned SSH and matching UUIDs.
- Root cause: `virsh domuuid` rejected the UUID selector; overriding `fence_virsh` SSH options dropped its fixed terminal/prompt; libvirt URI not bound to the transport.
- Fix: inspect exact UUID via explicit system-connection `dominfo`; keep the agent's terminal/prompt with strict pins; validate URI scope; native proof and Pacemaker derive identical fence arguments.
- Regression: `TestVirshDOMInfoMatchesExactUUID`, `TestVerifyStorageClusterFenceRequiresActualVirshPowerOff`, `TestFenceCIBUsesProofCredentialAndPinnedHostKeyPaths`, `TestVirshURIIsBoundToPinnedSSHTransport` pass; read-only status succeeded for all nine executor/target pairs (not power exclusion); both `phase1.yml` race jobs.
- Avoid: assuming similar subcommands accept the same identifiers, replacing required agent defaults, or keeping an ignored connection-identity field.

## BUG-0108 — Controller write deadline hid an uncertain cluster response

- Date: 2026-09-12.
- Status: reproduced natively; deadline regression passed; reporting-path diagnosis, native recheck and final review open.
- Symptom: a member fence powered off its target and Core recorded a passed proof, but the browser got no timely response.
- Root cause: 30-second relay wait equalled the server write deadline, leaving no time for the uncertain-outcome response. Separately, the lab's older Controller rejected the new fence-proof request identity field (matching builds required; older-Controller compatibility not established).
- Fix: 45-second server write deadline with bounded relay; transport loss is unknown, reconcile Core status, never auto-resend destructive requests. Does not explain the delayed report.
- Regression: `TestClusterRelayWriteDeadlineLeavesResponseMargin` passes (`go test ./cmd/nex-controller -count=1`); browser fixture covers timeout and reset uncertainty; both `phase1.yml` race jobs.
- Avoid: treating a missing response as proof the target stayed on, or retrying a power action before reconciling the original intent and proof.

## BUG-0109 — Cluster proof updates were sent to an incompatible Controller

- Date: 2026-09-12.
- Status: reproduced; acknowledgement repair, matching-build rollout and bounded aggregate-status checks passed; Controller replacement and outage acceptance open.
- Symptom: an older Controller rejected updated fence-proof reports; status requests timed out while Core kept valid evidence.
- Root cause: a shared capability name admitted incompatible proof fields without acknowledgement from the strict decoder.
- Fix: negotiate the revised clustered-storage capability before sending results; reset readiness on startup, replacement and report failure; Core keeps durable work regardless; unsupported Controllers get no cluster envelope.
- Regression: `TestClusteredStoragePreflightProtectsStrictOlderControllerAndReconnect`, `TestClusteredStorageV2AcknowledgementDispatchesAndAcceptsProofRequestID` pass; both `phase1.yml` race jobs; not a native Controller-replacement run.
- Avoid: equating an advertised feature name with schema negotiation, or persisting readiness across a different Controller without a fresh handshake.

## BUG-0110 — Master-to-member peer calls expected the master's certificate

- Date: 2026-09-12.
- Status: reproduced; regressions, review and native delegated exclusion passed.
- Symptom: member power proofs passed but master fencing returned uncertain without reaching the survivor's fence handler.
- Root cause: master-to-member connection reused a client pinned to the master identity, rejecting the member certificate.
- Fix: master-only member client bound to the ready member's advertised origin, host identity and certificate fingerprint (CA validation, mTLS, redirect rejection kept); member verify/proof routes authenticate only the current pinned master; uncertain intent kept, new action needs confirmation.
- Regression: `go test ./internal/agent -run 'TestPool(MemberPeerClient|PeerJoin)' -count=1` incl. `TestPoolMemberPeerClientPinsCurrentMemberOriginAndCertificate`, plus `TestStorageClusterMemberVerifyPathsAcceptOnlyCurrentMaster` pass; both `phase1.yml` race jobs. Native GUI verification read a survivor receipt with the master confirmed off, then recovered it.
- Avoid: weakening TLS validation, accepting any pool certificate for any endpoint, or repeating an uncertain power request automatically.

## BUG-0111 — GFS2 could become startable before its DLM ordering constraint

- Date: 2026-09-12.
- Status: found in source review; regression and review passed; native activation verification open.
- Symptom: sequential CIB creation could expose a startable GFS2 resource before the DLM ordering constraint existed.
- Root cause: resource creation enabled the filesystem before the constraint command.
- Fix: create the clone disabled, reread exact DLM/resource/order state, then enable; existing enabled-but-unordered resources fail closed without disabling a possibly mounted filesystem; racing creation accepted only after exact reread.
- Regression: `TestGFS2EnableWaitsForDisabledResourceAndExactOrder`, `TestGFS2ApplyRefusesEnabledUnorderedExistingResource` pass (interrupted, failed-ordering, duplicate cases); both `phase1.yml` race jobs.
- Avoid: treating final desired CIB state as proof intermediate activation is safe, or force-stopping a filesystem to fix ownership mismatch.

## BUG-0112 — Rendered quorum configuration was rejected by Corosync

- Date: 2026-09-12.
- Status: reproduced on all three lab Cores; corrected renderer deployed; scoped three-Core runtime verification passed; broader failure/HA acceptance separate.
- Symptom: Corosync exited status 8 with an extra-characters-after-brace error, blocking Pacemaker and GFS2.
- Root cause: renderer put the quorum property on the section's opening line.
- Fix: render the quorum section and provider on separate lines.
- Regression: `TestRenderStorageClusterIsDeterministicAndAuthenticated` requires the multiline stanza; installed `corosync -t -c` rejects old and accepts corrected config; both `phase1.yml` race jobs.
- Avoid: substring tests as proof of parser compatibility, or fixing only the deployed file instead of the renderer.

## BUG-0113 — Native CIB reconciliation misread stopped clones and could panic

- Date: 2026-09-12.
- Status: reproduced; repair and review passed; scoped native mount and six-guest admission rerun passed.
- Symptom: reconciliation tried to recreate an existing disabled clone; health inspection panicked and restarted Core services on a non-default state root.
- Root cause: stopped state read from primitive metadata, not the clone; fence health assumed the default credential directory and its failure hit an unchecked slice.
- Fix: read clone-level metadata; reject malformed specs without panic; carry the real Core state root through health, pool validation and VM admission; missing context fails closed.
- Regression: `TestCIBResourceChecksArePanicFreeAndRecognizeDisabledCloneMeta`, `TestManagedClusteredAdmissionUsesContextStateRoot` pass in full Agent/VM-host suites; both `phase1.yml` race jobs.
- Avoid: fixing only the panic guard, assuming native XML matches a hand-written fixture, or silently falling back to another credential directory.

## BUG-0114 — Members raced the master when writing shared Pacemaker state

- Date: 2026-09-12.
- Status: observed natively; repair and review passed; native reconciliation rerun open.
- Symptom: member reconciliation also wrote shared resources/constraints, racing on the CIB.
- Root cause: local Corosync preparation and master-owned CIB reconciliation shared one apply operation.
- Fix: members only connect storage and start local services; only the master reconciles shared resources and constraints.
- Regression: `TestStartStorageClusterDoesNotWriteSharedCIB`, `TestMemberClusterReconcileStartsLocalServicesWithoutCIBApply` pass; both `phase1.yml` race jobs.
- Avoid: treating local service configuration as permission for members to author pool-wide state.

## BUG-0115 — Public cluster reports discarded per-host observation identity

- Date: 2026-09-12.
- Status: reproduced in live GUI; typed-contract regressions, review and matching-build walkthrough passed.
- Symptom: natively healthy cluster, but every GUI member said health not reported and workload preflight could not identify healthy members.
- Root cause: the public Core mapper flattened member reports into anonymous status objects; UI/runner expected host-bound envelopes.
- Fix: publish typed member reports with host identity, nested status, storage/membership generations, config digest, stage and receiver timestamp; Controller rejects duplicate/unrelated references. Matching Core/Controller builds required.
- Regression: `TestStorageClusterPublicMemberReportPreservesObservedIdentity` (real JSON) and `TestStorageClusterMemberReportsKeepThreeIdentitiesAndRejectMalformedReferences` pass; both `phase1.yml` race jobs.
- Avoid: frontend-only identity mapping by array order, treating private journal JSON as the public schema, or mocks of an uninspected API.

## BUG-0116 — Healthy GFS2 was rejected by ordinary repository inventory and member import

- Date: 2026-09-12.
- Status: repairs and source review passed; live six-guest admission, start and concurrent I/O passed; failed-fence, outage, adoption and HA acceptance open.
- Symptom: all hosts reported healthy GFS2 but inventory rejected the master's filesystem as non-ext4/XFS and members rejected a resolved mount path.
- Root cause: observation still used exclusive-iSCSI filesystem validation; member intent carried a Core-resolved path through public validation without the trusted path later; bootstrap journal omitted supported capabilities, so the repository never became selectable.
- Fix: GFS2 observation for the clustered profile (native lock table, journal count); sanitize member intent and restore the path only from matching Core-private config; Core derives capabilities for new records and projects them for older zero-capability ones without identity change. Quorum/fencing stays a separate VM-admission gate.
- Regression: `TestInspectISCSIObservesClusteredGFS2Identity`, `TestPoolMemberStorageIntentLetsCoreResolveClusteredISCSIMount`, `TestClusteredISCSIRegistrationRestoresOnlyTrustedConfigMountPath` pass in `go test ./internal/agent ./internal/vmhost -count=1`; both `phase1.yml` race jobs.
- Avoid: loosening public path validation, treating a mount as cluster safety, or reporting desired host count as observed journal count.

## BUG-0117 — Shared-storage admission and lifecycle still assumed NFS/SMB only

- Date: 2026-09-12.
- Status: fixes and source review passed; native member VM admission, start and concurrent I/O passed; failed-fence, outage, adoption and HA acceptance open; UI selection regressions part of batch acceptance.
- Symptom: GFS2 treated as host-exclusive by Controller member selection and the VM picker; member adoption could replace the master's storage projection; generic iSCSI lifecycle unsuitable for a Pacemaker-managed LUN.
- Root cause: NFS/SMB-only conditions not extended to every caller; generic updates advance repository generation without updating cluster config.
- Fix: explicit pool-shared predicate across Core/Controller admission and adoption with fresh access checks; UI shows member eligibility; refuse generic clustered update/detach before native mutation; carry Core-private config into read-only rechecks.
- Regression: `TestClusteredISCSISharedStorageRequiresMembershipAndMasterRegistration`, `TestClusteredISCSIUpdateRefusesBeforeNativePreparation`, `TestDetachISCSIRefusesClusteredProfileBeforeNativeMutation`, `TestPoolRemoteStoragePlacementUsesFreshCoreMemberObservation`, `TestStorageRecheckReceivesCoreOwnedClusterContext` pass; both `phase1.yml` run Go suites and the clustered browser workflow.
- Avoid: changing a native backend without tracing API and picker callers, treating ordinary iSCSI as shared, or exclusive detach/logout on GFS2.

## BUG-0118 — Disposable workload runner could not safely resume partial creation

- Date: 2026-09-12.
- Status: partial-resume repair, safety review and bounded native continuation passed; broader failure/outage acceptance open.
- Symptom: after one GFS2 VM creation then an HTTP refusal, resume required all six VMs; naive continuation could duplicate a host/ordinal or an accepted-but-uncheckpointed VM.
- Root cause: resume validation assumed only post-creation failures.
- Fix: validate retained VM/disk/host/ordinal identities, reject ambiguous or uncheckpointed slots, check capacity first, create only missing slots without recopying disks; wait for fresh matching per-host storage observations before mutation. Lab-only GFS2 exclusion may name a terminal failed creation after exact binding and proven absence on every Core; replacement gets a new identity; failed record preserved. Capture live QEMU PID before exit removes the pidfile.
- Regression: `python3 -m unittest tests.test_cluster_storage_workloads` passes 28 tests (one retained VM leads to exactly five creations; capacity/uncheckpointed refusal; stale-to-fresh, timeout, generation mismatch; replacement binding; PID evidence); both `phase1.yml` Python jobs; no remote CI run claimed.
- Avoid: deleting a partial lab for a green run, overwriting failure evidence, or treating a missing checkpoint as proof no VM was created.

## BUG-0119 — One repository deadline poisoned later member reconciliation

- Date: 2026-09-12.
- Status: reproduced; scoped deadline repair reviewed; matching-build lab verification open.
- Symptom: a member's GFS2 access observation went too old for VM admission while host and cluster were healthy.
- Root cause: one three-second context covered the whole sequential reconciliation pass; a slow repository expired later calls. Native filesystem calls outliving cancellation remain a separate, unfixed latency.
- Fix: independently bounded list, repository and orphan-cleanup operations keeping shutdown cancellation and single-flight; late successes are deadline failures, not healthy access.
- Regression: `TestMemberReconcilePostsLaterAccessAfterEarlierDeadline` plus cancellation/late-return checks pass in `go test ./internal/agent ./internal/vmhost -count=1`; both `phase1.yml` jobs; no remote run claimed.
- Avoid: raising the global deadline, relaxing freshness or fsync, or overlapping reconciliation while a filesystem call is stuck.

## BUG-0120 — Cancelled folder discovery could reopen from a late success

- Date: 2026-09-12.
- Status: source and browser regression review passed; updated live deployment walkthrough open.
- Symptom: a discovery response fulfilled after cancellation reopened the closed folder picker.
- Root cause: cancellation kept the active request reference; success callbacks did not reject aborted requests.
- Fix: Cancel and native dialog close clear the reference before aborting; success handling rejects an aborted signal.
- Regression: `tests/integration/cluster_storage_browser.py` (Cancel/Escape then deferred success) passes; `tests/integration/storage_repositories_browser.py`, storage UI checks and typecheck rerun; both `phase1.yml` run the browser regression; no remote CI run claimed.
- Avoid: assuming transport abort stops an already-fulfilled callback from updating the UI.

## BUG-0121 — Clustered iSCSI was not ready before Pacemaker on host boot

- Date: 2026-09-12.
- Status: fixed; isolated master cold-login reboot passed after publication repair.
- Symptom: Pacemaker permanently refused GFS2 start because its device was absent; the other two hosts kept quorum and mounts.
- Root cause: clustered connection did not persist iSCSI automatic node startup; Pacemaker had no Core-validated storage readiness dependency.
- Fix: before Pacemaker, validate local durable cluster intent, membership and fencing proofs, connect and verify exact GFS2 identity; needs no Controller/peer and issues no guest permit. Existing masters install the dependency without native reapply; unreadable receipts stay uncertain.
- Regression: agent/vmhost race and Core CLI checks pass (active-agent locks, unsafe ownership/ancestors, foreign units, reload retry, conflicting history, upgrade, receipts, session/device publication, post-command cancellation); both providers run the Go suites. First repeat boot failed before session/device appeared (warm-device CLI probe is not a pass); after repair, a master reboot with automatic login disabled restored login and passed the gate; all three hosts mounted the GFS2 UUID with quorum, no manual steps.
- Avoid: clearing failure counters to call a failed boot healthy, manual GFS2 mounts, accepting any available disk, or requiring a healthy mounted cluster before its own startup prerequisite.

## BUG-0122 — An uncertain remote VM creation could not settle by observation

- Date: 2026-09-12.
- Status: fixed; original stopped SMB creation settled in the disposable lab.
- Symptom: exact stopped domain and blank disk existed but creation stayed `needs_attention` after an ownership-inspection failure.
- Root cause: generic promotion skips remote-backed VMs and no Core authority-aware observer handled recovery; Controller replay must not redispatch.
- Fix: the serialized Core worker validates owner, master's durable VM spec and storage, observes the exact stopped definition under the journal lock, and settles the same operation without allocating, starting, deleting or replacing.
- Regression: agent spec-binding and vmhost reconciliation tests pass (changed revision/disk refusal, cancellation after autostart claim, legacy-success requires positive `spec_match` receipt); both providers run the Go suite. Native settlement confirmed via API, journal and libvirt; no replay, disk replacement or start.
- Avoid: editing journals, calling a start-capable replay to inspect state, deleting an uncertain disk, or making Controller stay alive to finish recovery.

## BUG-0123 — Autostart attempts were process-bound rather than boot-bound

- Date: 2026-09-12.
- Status: source repair accepted; native agent/OS-restart walkthrough passed.
- Symptom: restarting Core could retry autostart for a VM manually stopped during the same host boot.
- Root cause: attempted-VM map lived only in process memory though autostart is an on-host-boot policy.
- Fix: private boot-bound claim (exclusive create, file/dir sync) survives agent restart and allows one attempt per actual host boot; uncertain-definition recovery consumes the claim first.
- Regression: `go test ./internal/agent` (race) passes same-boot, new-boot, concurrent, malformed, symlink, foreign-ownership and unsafe-ancestor cases; both providers run it. Native: agent restart kept VM stopped; real OS reboot made one new-boot start on the assigned host; policy restored. Not HA, not a remote CI result.
- Avoid: resetting the marker on agent/Controller reconnect, treating a timeout as proof a VM stopped, or silently changing the pool autostart policy.

## BUG-0124 — Shared-storage lock fixture put its PID file in the datastore root

- Date: 2026-09-12.
- Status: fixed; native lock proof passed with all six GFS2 guests running; complete six-VM workload a separate gate.
- Symptom: six guests started but the lock holder could not create its PID file before the competing-write test.
- Root cause: fixture treated probe-disk write access as permission to create runtime files in a protected datastore root; a later attempt also dropped the QEMU account's datastore group by deriving credentials from disk ownership and clearing supplementary groups.
- Fix: PID/log files in a private host-local directory; resolve the real non-root QEMU account and full groups on both hosts, refusing mismatches. Datastore access not broadened.
- Regression: `python3 -m unittest discover -s tests -p 'test_cluster_storage_workloads.py'` passes 33 checks (runtime paths, process identity before signalling, artifact preservation, lock/refusal/release, QEMU identity); both providers run it. Native probe got second-host write-lock refusal then a 512-byte write after release; later failure recorded as BUG-0125.
- Avoid: treating a startup failure as a lock refusal, removing lock coverage, or changing repository permissions for test-only files.

## BUG-0125 — Fenced lab Core returned before the requested recovery step

- Date: 2026-09-13.
- Status: native policy upgrade and six-VM write-time controlled-return repeat passed.
- Symptom: Core recorded a passed power-off proof but the target was running again while survivor writes were active, before the runner's return step. No automatic VM recovery result claimed.
- Root cause: Core did not set `stonith-action=off`; Pacemaker's default reboot returned the target after Core's direct power-off test.
- Fix: configure power-off fencing and require the exact property in readiness; master validates intent, membership, native receipt and owned CIB before policy-only upgrade; matching is a no-op; stale/foreign state and cancellation fail closed without remount/recreate.
- Regression: `go test -race ./internal/agent ./internal/vmhost -count=1` passes (missing/wrong/duplicate policy, stale intent, foreign CIB, no-op, cancellation); both providers run it. Native upgrade added the policy on all three hosts without changing CIB hashes or mounts; attempt nine passed exclusion with target held off, return and checksums (see BUG-0134).
- Avoid: treating a past power-off observation as proof a host remains off, skipping the pre-return power-state check, or manually editing cluster properties for a pass.

## BUG-0126 — Lab admission compared volatile SMB filesystem identifiers

- Date: 2026-09-13.
- Status: fixed; review and six-guest SMB workload passed.
- Symptom: SMB workload stopped before creating a member VM despite fresh member access; two stopped VMs remain checkpointed on the master from that attempt.
- Root cause: runner compared full identity objects; CIFS filesystem IDs differ per host while server, share and durable marker match (Core's marker-aware comparison already handles this).
- Fix: fixture uses the existing backing-identity contract, keeping freshness/generation/host checks; reject changed markers/sources; markerless still strict; mixed marker/legacy refused more strictly than Core.
- Regression: fixture suite passes 34 checks (differing-FSID/same-marker, wrong marker/source, blank identity); SMB attempt three passed six-VM fsync writes, reboot checksums and cross-host QEMU lock refusal/release; both providers run the Python suite.
- Avoid: changing product identity reporting to satisfy a broken test, bypassing member readiness, or reading a client-local filesystem ID as a server UUID.

## BUG-0127 — Shared-storage monitor timeouts fenced every lab Core

- Date: 2026-09-13.
- Status: monitor-budget repair and native upgrade passed; initiating-stall investigation and interruption testing open.
- Symptom: after the SMB workload passed and guests stopped, all three Cores powered off before the next GFS2 workload's mutations.
- Root cause: partial. Journals show a transient iSCSI ping timeout, three GFS2 monitor timeouts and power-off fencing; Core left the default 20-second monitor despite the Filesystem agent's 40-second recommendation; a fence monitor also timed out. Transport stall cause unknown.
- Fix: explicit 40-second GFS2 monitor; guarded master upgrade recognizes only the exact legacy resource, applies a version-conditioned CIB patch and rechecks, preserving fencing, mounts and failure history.
- Regression: agent/vmhost race suites pass (strict legacy recognition, drift/conflict refusal, no broad reconciliation); both providers run them. Native read-only capture showed only the expected timeout attribute changed, stable on repeat; two-host DLM/GFS2 recovery passed. The failed preflight is not a pass.
- Avoid: disabling fencing, clearing failures to claim health, blaming host load without evidence, or treating a larger timeout as proof of transport reliability.

## BUG-0128 — DLM fencing helper requires an unavailable Pacemaker library

- Date: 2026-09-13.
- Status: prerequisite guard and matching package reviewed; reproduced two-host native recovery passed; write-time and interruption gates separate.
- Symptom: quorum and power exclusion succeed but GFS2 mount blocks joining the DLM lockspace, times out, and the host is fenced again.
- Root cause: Debian `dlm-controld` 4.3.0-3's helper requests `libstonithd.so.26`; Pacemaker 3.0.0-2 ships `.so.56` only (`kick_helper error -79`, exit 177). The Pacemaker 3 development header itself still defines `.so.26`, so a plain rebuild does not fix it.
- Fix: Core checks the helper's exact library name, architecture and exports without fencing; setup, health and pre-Pacemaker boot readiness fail closed (before iSCSI connect). A rebuilt Debian package keeps the upstream helper and corrects only the stale header constant via a reproducible build-local overlay; no library alias or new fencing implementation.
- Regression: agent/vmhost race suites pass (missing library, ambiguous candidates, late cancellation); both providers run them. Package-loader check passed; offline deployment to three disposable hosts; two-host startup excluded the third and recovered DLM/GFS2 without manual steps. Evidence: [concurrent storage evidence](qa/2026-09-12-concurrent-pool-storage.md).
- Avoid: trusting package installation as fencing compatibility, symlinking incompatible library versions, disabling DLM fencing, or manually acknowledging exclusion the integration cannot verify.

## BUG-0129 — Cluster lab inhibited the wrong DLM service name

- Date: 2026-09-13
- Status: fixture correction reviewed; corrected native boot order verified on all three hosts.
- Symptom: the lab's standalone `dlm.service` was enabled and active before Pacemaker, unlike the product installer's ordering.
- Root cause: lab provisioning inhibited `dlm_controld.service`, not Debian's actual `dlm.service`; successful package install hid the drift.
- Fix: use the actual Debian unit in the fixture and keep the standalone service disabled after upgrades, leaving Pacemaker's DLM resource in control; no executable masking or CIB ordering change.
- Regression: all six fixture tests pass (Python discovery, both CI providers); native check verified the disabled unit and readiness gate before Corosync/Pacemaker on all three hosts, with all GFS2 clones healthy.
- Avoid: treating a nonexistent unit's tolerated error as successful inhibition, or claiming lab boot ordering matches the product without checking.

## BUG-0130 — Healthy majority recovery was rejected as missing quorum

- Date: 2026-09-13
- Status: correction reviewed; native majority health, initial activation and degraded existing-VM admission passed.
- Symptom: Corosync reports a quorate two-of-three partition and DLM/GFS2 recovers on both survivors, but Core reports quorum unconfirmed and blocks storage admission.
- Root cause: health parsing equated live nodes with configured pool size; DLM/GFS2 checks required the full clone count; master admission required all hosts indefinitely; member readiness treated the registration's master owner as the local observer.
- Fix: distinguish configured membership from its live voting majority, keeping exact membership, vote policy, fencing and resource-identity checks; bind the observer to the native local membership row. Initial setup still needs every host's fresh evidence and a master-recorded, configuration-bound activation receipt before majority operation; observations distinguish degraded from healthy.
- Regression: focused agent/vmhost race suites (member-observed master registration, malformed membership, stale/mismatched activation, unsafe resource states), both CI providers; native survivor reports, third-host return and existing-VM restart with the third host off passed. Separate stale aggregate-health label split to BUG-0137.
- Avoid: accepting a bare `Quorate: Yes`, lowering configured votes to the live count, dropping fencing checks, or treating a configured pool as fully online.

## BUG-0131 — Shared VM policy update leaves the ownership definition stale

- Date: 2026-09-13
- Status: reviewed correction, native API recovery and full autostart walkthrough passed.
- Symptom: after starting a shared-storage VM and changing its autostart policy, the next graceful shutdown fails with a durable-inventory definition conflict.
- Root cause: the managed update changed the definition without updating the pool master's ownership record, so exact inventory reconciliation rejected it; the scan could also block operations on other VMs.
- Fix: keep authorized definition changes and the master-owned record consistent, including interrupted-operation recovery; reboot/media use owner admission; a local journal alone never reports an unconfirmed master transition as succeeded. Divergent member inventory and exclusive ownership stay strict.
- Regression: `GOMAXPROCS=2 go test -race ./internal/agent ./internal/vmhost -count=1` passed (reserve/commit/abort acks, replay, invalid input, master confirmation, journal recovery), both CI workflows; native journal recovery, shutdown and agent-restart/OS-reboot autostart walkthrough passed.
- Avoid: deleting the ownership journal, ignoring spec mismatches, manually rewriting state, or claiming the failed autostart walkthrough passed.

## BUG-0132 — Disk-placement changes lose the existing pool ownership path

- Date: 2026-09-13
- Status: corrected and independently reviewed; stopped local/NFS attachment transition verified; broader conversion acceptance open.
- Symptom: after removing a VM's last shared disk, a local-only edit or power action can bypass its master ownership record; adding the first shared disk to a local VM has no master record to reserve against.
- Root cause: admission classified only requested/current disks, not durable ownership history; initial inventory adoption skipped local definitions.
- Fix: preserve pool ownership through disk-placement changes using retained Core journal evidence; bind first-transition adoption to the authenticated prior owner definition, not client input. No second member policy store.
- Regression: `GOMAXPROCS=2 go test -race ./internal/agent ./internal/vmhost -count=1` (local-to-shared seeding via master-local and member paths, history after detach, local-edit bypass), both CI workflows; native stopped attach/detach 1 → 2 → 1 passed (not disk migration or running reconfiguration). Evidence: [current native evidence](qa/2026-09-14-cluster-storage-reliability.md).
- Avoid: treating the disappearance of a shared disk as permission to discard the master's VM identity or accept a new owner.

## BUG-0133 — Cached stopped inventory overwrites newer execution state

- Date: 2026-09-13
- Status: native mismatch reproduced; reviewed correction and native reboot checks passed.
- Symptom: the master ownership record says stopped while the assigned guest is running, blocking running-only reboot admission.
- Root cause: owner seeding replayed runtime observations from all cached member inventories; an older stopped observation overwrote a later start ack and later running inventory did not correct it.
- Fix: accept runtime transitions only from the relevant fresh owner observation, protecting newer execution epochs and in-flight starts; never infer stop from a timeout, foreign report or cached pre-start snapshot.
- Regression: fresh-owner and stale-foreign-inventory tests under `GOMAXPROCS=2 go test -race ./internal/agent ./internal/vmhost -count=1`, both CI workflows; native six guest reboots and survivor stop/start passed (foreign-report race covered only by the deterministic test).
- Avoid: replaying cached runtime state during global definition seeding, or changing the UI to hide a contradictory master record.

## BUG-0134 — External DLM fencing requests bypass the default off policy

- Date: 2026-09-13
- Status: native configuration upgrade and external-DLM controlled-return gate passed; stricter foreign-parameter guard deployed with native exact-CIB no-op verified.
- Symptom: DLM requests reboot fencing although Pacemaker's default fencing policy is off, so the default alone does not guarantee controlled host return.
- Root cause: DLM is an independent client of the Pacemaker fencing API and requests its own action; the cluster default governs only requests using it.
- Fix: render `pcmk_reboot_action=off` on every Core-owned fence resource; readiness requires one direct unconditional mapping with its deterministic CIB ID. Only a recognized otherwise-exact predecessor missing the mapping gets a version-checked narrow patch; conflicting/duplicate/conditional/inherited mappings and extra or duplicate fence parameters are refused before mutation.
- Regression: full agent/vmhost race checks (wrong-mapping no-write, legacy upgrade), both CI workflows; native rollout installed mappings with unchanged PIDs, and a DLM reboot request resolved to off with the target held off (on the pre-guard build). Foreign-parameter refusal is regression-tested only, not injected live. Remote CI results not established.
- Avoid: assuming `stonith-action=off` covers all fencing clients, disabling DLM fencing, or acknowledging an unresolved request to make a mount succeed.

## BUG-0135 — Initial cluster activation receipt disappears

- Date: 2026-09-13
- Status: reviewed correction and combined checks passed; selected native master fence/replay passed with the receipt retained. Other failure modes remain separate.
- Symptom: an initially qualified cluster returns to awaiting initial healthy member confirmation instead of its established degraded state.
- Root cause: a runtime-fault retry moved the operation to queued and reconstructed it for native replay without copying the completed activation receipt.
- Fix: preserve master-owned initial-activation evidence across reporting, restart and failure paths without inferring it from historical status.
- Regression: `TestResumeNeedsAttentionRetainsCompletedActivationReceipt` passes (pre-fix red not captured); combined agent/vmhost race checks, both CI workflows; native reboot and master fence/replay kept activation and registration byte-identical (final raw guest inventory not retained). Evidence: [reliability follow-up](qa/2026-09-14-cluster-storage-reliability.md).
- Avoid: manufacturing an activation receipt from a status label, bypassing initial qualification, or relabeling the UI without restoring durable behavior.

## BUG-0136 — Workload fixture rejects equivalent empty media defaults

- Date: 2026-09-13
- Status: fixture corrected and independently reviewed; native retry passed.
- Symptom: the six-VM GFS2 resume gate rejects otherwise matching retained VMs before any VM starts.
- Root cause: fixture input omits the optional media ID while Core reports an empty string; canonical comparison treated them as different.
- Fix: normalize only omitted/empty no-media defaults; actual media selections and all other fields stay strict.
- Regression: all 39 workload fixture tests pass (equivalent defaults, invalid media types, real media mismatch), Python suite in both CI providers; native resume of the six retained VMs passed.
- Avoid: removing spec comparison, accepting arbitrary media drift, or overwriting retained guest images to fake compatibility.

## BUG-0137 — Recent pre-fence reports keep aggregate storage falsely healthy

- Date: 2026-09-13
- Status: correction independently accepted; native degraded-state recapture passed.
- Symptom: a positively fenced-off host and aggregate storage still show healthy from recent pre-fence reports while the host connection shows offline.
- Root cause: the freshness window alone did not invalidate observations predating a new exclusion proof, and proof acceptance did not reduce persisted aggregate status.
- Fix: invalidate older target observations, require reports to acknowledge the current passed proof via the existing status contract, and recompute readiness on proof acceptance; keep the activation receipt and never infer health from exclusion.
- Regression: verifier test against persisted activated state proves immediate durable degradation; `go test -race ./internal/agent ./internal/vmhost -count=1` and 210 Python tests passed; Go packages in both CI providers. Native captures showed degraded after exclusion and healthy after controlled return.
- Avoid: shortening the freshness timeout, changing only the UI label, or treating a successful fence as proof the target returned healthy.

## BUG-0138 — Single-host iSCSI interruption is followed by all-host exclusion

- Date: 2026-09-13
- Status: native failure reproduced; reviewed correction passes source regressions and the stopped-VM native interruption repeat; live-write acceptance remains open.
- Symptom: a bounded 90-second TCP/3260 interruption on only the third Core powered off all three Core hosts (guests stopped; heartbeat/fencing paths not targeted).
- Root cause: GFS2 monitor-stall cascade: the isolated host's iSCSI NOP timeout preceded stalled monitors on all three hosts, all exceeding 40 seconds and scheduled for fencing before session recovery. Exact blocking syscall unproven; not DLM-monitor-first.
- Fix: monitor-only exact-session OCF resource ahead of each affected GFS2 clone, one transport identity per portal/IQN; never logs in/out or changes mounts. Activation and legacy upgrade need fresh live adapter/session readiness from every member, keep the activation receipt and version-check the narrow CIB patch; real health refresh precedes the legacy retry gate.
- Regression: parser, wrapper attestation, CLI, CIB and agent retry tests in `go test -mod=vendor ./...` plus agent/vmhost/protocol race tests, both CI workflows (remote CI not executed); native 90-second repeat with stopped VMs fenced only the target and survivors stayed healthy (28 observations). Live-write fault and final delivery open.
- Avoid: disabling fencing, clearing native failure state, raising every timeout without causal evidence, or claiming a baseline write pass proves resilience to storage-path loss.

## BUG-0139 — Offline storage retains current-looking healthy access

- Date: 2026-09-13
- Status: correction, browser regressions and native all-stale outage/recovery workflow passed.
- Symptom: with every Core disconnected, cached NFS/SMB rows still show green healthy/readable/writable access and enabled lifecycle actions.
- Root cause: the UI rendered the last access projection without the assigned Core's connection freshness; the media storage picker filtered cached access the same way.
- Fix: keep the last report as history, mark current access unknown, and gate actions/pickers on host freshness. Core stays authoritative; no Controller-owned state.
- Regression: `tests/integration/cluster_storage_browser.py` (stale NFS/SMB labels, disabled actions, withheld ISO destinations, stale update/detach, deferred SMB credential handoff; simulated API capture labelled) in both CI workflows, remote CI not executed; typecheck/build/UI unit tests passed; native all-stale workflow passed.
- Avoid: deleting useful last-known data, turning cached Core facts into invented current state, or treating a green cached badge as proof of writable storage.

## BUG-0140 — Source archive metadata retains the builder's account identity

- Date: 2026-09-13
- Status: source correction and regression pass; neutral ownership verified on all 396 members of a newly built private source archive.
- Symptom: candidate source files passed content privacy review, but tar member ownership retained the build machine's account and group names.
- Root cause: the packager used default `tarfile.add` metadata without a public ownership policy.
- Fix: normalize every member's uid/gid to zero and clear account/group names and inherited extended headers, keeping contents and notices.
- Regression: `test_archive_metadata_does_not_retain_builder_identity` failed before and passes after; all eight `test_build_source.py` tests pass; Python discovery in both CI workflows, remote CI not executed. Existing private archives, published history and distributed artifacts were not rewritten or audited.
- Avoid: checking only file contents, renaming the outer archive, or claiming distributed artifacts were sanitized by fixing the current builder.

## BUG-0141 — Cluster status times out after a master-agent outage during writes

- Date: 2026-09-13
- Status: mixed-version correction and independent source/documentation review passed; corrected native outage repeat pending.
- Symptom: after stopping and restoring only the master's management service during six GFS2 guest writers, clustered-storage status returned HTTP 504 (guest writes progressed meanwhile).
- Root cause: new Core sends `session_monitor_ready` in member status; the older Controller's strict decoder rejects the unknown field with 400 and the broad cluster capability does not negotiate it, so report handshake/reset cycles starve the broker until its 30-second expiry. Not a native storage stall.
- Fix: negotiate the report extension and omit it from the Controller-facing projection until acknowledged, keeping Core-native readiness state.
- Regression: test rejects the raw payload with the old decoder then accepts the negotiated projection (new/new, old/new, late base ack, reconnect, re-enrollment); `go test -race ./...` and 212 Python tests passed; Go suites in both CI workflows, remote CI not executed. No corrected native checksum/reboot or outage acceptance claimed.
- Avoid: hiding the timeout with unbounded retry, clearing native failures, treating a restarted process as complete recovery, or calling write progress alone a passed durability test.

## BUG-0142 — Lab cleanup can falsely report unknown guests stopped

- Date: 2026-09-13
- Status: reproduced in the disposable lab; correction and independent review passed; native management-outage repeat pending.
- Symptom: the management-outage helper recorded all six guests stopped, but native inspection found both master-host guests still running.
- Root cause: shared workload cleanup selected only guests initially `running` and verified that subset, silently excluding unknown/missing ones; the private caller asserted complete cleanup. The original run's exact mismatch is inconclusive; its false receipt is retained.
- Fix: require fresh known state for every requested VM before any cleanup request, verify the full set is freshly stopped, and keep the original error when cleanup is unproven; private verifier uses bounded fresh-report reconciliation after restart.
- Regression: 41 shared workload tests, 18 private outage checks and 214 Python discovery tests passed; discovery suite in both CI workflows, remote CI not executed.
- Avoid: interpreting unknown as stopped, checking only a convenient subset, or treating a helper's return as evidence every requested cleanup happened.

## BUG-0143 — Session-monitor fencing cascades during writes before fault injection

- Date: 2026-09-13
- Status: reproduced; diagnosis and resilience acceptance open. Bounded disposable IBLOCK scenario later passed; production, HA, migration, physical SAN, soak, strict continuous-writer handover with transport fault, and current-ISO acceptance remain open.
- Symptom: with six GFS2 guest writers, session monitoring fenced two members before the planned transport fault was installed and the master lost quorum; stopped-guest fault passes did not cover writes.
- Root cause: unresolved. The target logged unrecoverable iSCSI DataOut timeouts immediately before both monitor failures (return code 7, zero exec/queue time, not a monitor timeout); distinct initiator identities rule out cloned identity. CPU/backing-I/O pressure is a hypothesis; the OCF wrapper suppresses CLI diagnostics.
- Fix: none for the root cause. Lab work: BUG-0145 fixture correction (repeat still failed with master DataOut before injection and force-stops stuck `in shutdown`); switching lab target backing FILEIO → IBLOCK let a second full six-writer run pass with only the selected Core fenced, no DataOut, healthy survivors and matching 600 MiB hashes (first IBLOCK run failed on a `pending` fence history sample). No target timer or fencing change claimed.
- Regression: no fix or passed write-time repeat of the original failure; the required regression must detect the established cause, keep fail-closed persistent-loss behavior and run in both CI workflows. NFS/SMB six-VM write/reboot/lock repeats and strict Controller-adoption handover (C-to-D) passed separately; aggregate CPU-model observation and transient member-reconciliation warnings remain under investigation. Evidence: [reliability follow-up](qa/2026-09-14-cluster-storage-reliability.md).
- Avoid: disabling fencing, lowering the workload solely to pass, assuming a timeout without evidence, or extrapolating a stopped-guest fault pass to workload resilience.

## BUG-0144 — Lost-quorum storage admission blocks shared-VM stop requests

- Date: 2026-09-13
- Status: reproduced; narrow source/API/guide correction independently accepted. Native check passed only with master management down and storage healthy; lost-quorum and frozen-I/O completion remain open.
- Symptom: after the preceding cascade, API force-stop of a surviving VM failed with `clustered VM storage is not ready` before reaching libvirt.
- Root cause: shared execution admission required live clustered storage and peer bookkeeping for stop-only actions, though native stop does not need storage admission.
- Fix: allow only authenticated, scope-validated shutdown/force-stop of the locally journal-defined shared VM without a healthy cluster or reachable master; start, reboot, autostart, creation, updates, permits and ownership transfer stay fail-closed; no invented stopped state after error. During frozen GFS2 I/O native termination may still hang (`in shutdown`).
- Regression: stop-only/no-permit/unchanged-owner tests, full agent suite and race check passed; Go suites in both CI workflows, remote CI not executed.
- Impact: API and guide updated for stop-only admission.
- Avoid: granting start/reboot through a stop exception, transferring ownership on a timeout, or reporting a requested stop as observed completion.

## BUG-0145 — Disposable host definitions omit ACPI/APIC CPU enumeration

- Date: 2026-09-13
- Status: fixture correction and controlled native cold return verified; unchanged six-writer verification remains open under BUG-0143.
- Symptom: the storage target had two vCPUs but booted with one present CPU and SMP disabled; shutdown flushed filesystems but could not power off.
- Root cause: the outer libvirt fixture omitted ACPI/APIC features (no MADT or MP tables). Not proof of the BUG-0143 DataOut stall.
- Fix: render ACPI, APIC and an internal VGA device (no graphics endpoint), keeping identities, disks, memory, CPU count and networks. ACPI without VGA repeatedly reset on this QEMU version; ACPI with VGA booted two CPUs.
- Regression: fixture test failed before, all seven pass after; 215 Python discovery tests in both CI workflows, remote CI not run; native storage and three Cores booted with two CPUs and healthy quorum/DLM/GFS2 with no guests. Not a write-time resilience pass.
- Avoid: calling configured CPU count observed capacity, altering storage timers to hide a stall, or treating fixture tests as native boot evidence.

## BUG-0146 — Member reconciliation rewrites healthy native cluster state

- Date: 2026-09-13
- Status: source correction, independent review, Core rollout and post-fix native idle observation passed.
- Symptom: each five-second member reconciliation rewrote an unchanged Corosync configuration and reissued service enable/start while the cluster was already active (120 generator calls, 60 reloads per member in 30 s).
- Root cause: `StartStorageClusterAt` did not distinguish exact steady state from recovery.
- Fix: compare owned configuration byte-for-byte and check every required unit for persistent enablement and active state; exact healthy state is read-only, disabled/inactive keeps recovery, foreign configuration is refused.
- Regression: `TestStartStorageClusterIsPersistentServiceAndConfigNoop` reproduces the old starts/inode change and passes with the fix; vmhost race/full checks, both CI workflows; native 35 s no-VM observation showed zero generator calls or reloads. Steady-state idempotence only; no DataOut causality.
- Impact: existing setup/status/reconciliation contract, no schema change; state owner Core (existing master journal and native config).
- Avoid: treating every periodic pass as recovery, silently accepting foreign configuration, or making disabled/inactive recovery a no-op.

## BUG-0147 — Unknown master storage status appears as an empty configuration

- Date: 2026-09-13
- Status: source correction, independent review, UI publication and native outage/recovery workflow passed.
- Symptom: an unavailable or still-checking master storage status rendered as empty configuration, offering initial setup and hiding existing additional repositories.
- Root cause: the browser collapsed unknown, failed and confirmed-empty responses into one absence state.
- Fix: distinguish checking, unavailable, succeeded-empty and configured views; setup only after confirmed status and preserves additional repositories; a pool-key remount rejects late writes from a previous scope.
- Regression: source UI checks and fixture coverage pass; browser suites in both CI workflows; native outage/recovery workflow passed without browser errors.
- Impact: existing status GET contract; guide updated (storage guidance); state owner Core, no durable record change.
- Avoid: treating unavailable data as empty, enabling setup during a pending status read, or applying late replies after a pool-scope change.

## BUG-0148 — Replacement enrollment can leave credential recovery ambiguous

- Date: 2026-09-13
- Status: source correction and independent review accepted; focused normal/race checks and bounded three-member Core rollout (CIB, quorum, GFS mounts unchanged) passed. Replacement-enrollment failure paths not exercised natively; aggregate pool health, production readiness, HA and transport-fault recovery not established.
- Symptom: a rejected or lost replacement enrollment could make recovery ambiguous, and a failure after renaming the new state file could be misread as proof the replacement credential was inactive.
- Root cause: enrollment did not make explicit the boundary between pending credential, still-active credential, and a published but not durably confirmed state file.
- Fix: keep the replacement key/CSR in Core state until a validated response is atomically accepted; keep prior trust/key/certificate on rejection or lost response; after rename, retain published state even if directory fsync fails, return uncertain, and reconcile observed identity/inventory before retry.
- Regression: agent tests (byte-for-byte retry after lost response, old credential preserved, post-rename failure matches published state) and local-core handoff tests; `go test -race ./...` passed; Go suites in both CI workflows, remote CI not run.
- Impact: existing contract — `POST /api/v1/agent/enroll` and local handoff shapes unchanged; callers reconcile before retrying a post-rename uncertain error. Guide updated (enrollment recovery). State owner Core: agent `state.json` holds active and pending material; fresh Controller adopts via enrollment and reports.
- Avoid: deleting or cloning the state directory after a failed replacement, pairing a new key/trust with an old certificate, or retrying a post-rename uncertain result before reconciling.

## BUG-0151 — Controller restart mislabels clustered-storage preflight as unsupported

- Date: 2026-09-13
- Status: source correction and independent review accepted; bounded Controller-outage workload passed hash/reboot, off/return and cleanup, but the natural 503 branch did not occur (source/contract-only). Earlier failed outage receipts remain failures.
- Symptom: after Controller restart, a v2 Core withholding clustered capability during preflight got a permanent-looking 409 unsupported instead of a retryable state.
- Root cause: capability selection treated an offered preflight extension like a Core that never advertised clustered support.
- Fix: return 503 `clustered storage capability negotiation is pending` for the bounded v2 preflight; keep 409 for genuinely unsupported. No broker request, native work, credential handoff or legacy fallback while negotiating; callers retry after the acknowledged report.
- Regression: Controller capability tests (status and credential refusal without dispatch during preflight, relay after ack); workload consumer treats 503 as retryable. Bounded lab evidence only, not production or ISO acceptance.
- Impact: changed error contract for clustered-status, setup, credential and fence routes (negotiation 503, absent support 409); guide updated; no Controller state.
- Avoid: dispatching native work or credentials before acknowledgement, treating transient preflight as permanent unsupported, or adding a legacy fallback route.

## BUG-0152 — Adopted task history implies an unavailable original timestamp

- Date: 2026-09-13
- Status: source correction and independent review accepted; browser captures passed full adopted-history (688 outcomes) and recent-five views.
- Symptom: task history showed the Controller re-observation timestamp for Core-reported historical outcomes as the original outcome time.
- Fix: label only rows whose request ID is exactly `adopt-outcome:<host-id>:<operation-id>` as **Reobserved from Core history** and state the original time was not reported; all other tasks keep the updated timestamp.
- Regression: UI checks cover the exact identity and reject lookalike prefixes; typecheck, tests, build and privacy passed; browser capture passed (earlier locator failure retained).
- Impact: existing contract (existing request ID, no Core field); guide updated.
- Avoid: inferring a missing original timestamp, matching a loose request-ID prefix, or presenting a re-observation as a newly dispatched task.

## BUG-0149 — Fresh ISO management cannot start with unprovisioned agent state permissions

- Date: 2026-09-13
- Status: source regression, independent review and seventh-ISO clean-install BIOS verification passed; hosted CI and broader production gates remain open.
- Symptom: BIOS installation completed and booted, but local Core management repeatedly exited with an agent state-directory permission error.
- Root cause: the embedded agent uses `/var/lib/nex-core-agent` (root 0755 after install), but the local management unit provisioned only `/var/lib/nex/core-local`; strict store validation correctly rejected it.
- Fix: declare both directories in local management's `StateDirectory` with `StateDirectoryMode=0700`; validation unchanged.
- Regression: private-state-directory package test failed before, passes after; `python3 -m unittest discover -s tests -p 'test_core_local_package.py'` passed 15, in both CI workflows' Python discovery; seventh ISO clean install showed both dirs root 0700 and zero restarts across a reboot. Hosted CI has not run.
- Impact: existing contract (restores Core-local API startup); state owner Core (existing agent `state.json`).
- Avoid: weakening store permission checks, relying on an inactive standalone unit, or manually fixing one lab directory and calling the installer fixed.

## BUG-0150 — Fresh-install acceptance can pass with failed Core management

- Date: 2026-09-13
- Status: fixed; corrected seventh-ISO baseline/reboot walkthrough passed (not UEFI or physical hardware); hosted CI not run. Original false-positive output retained, not relabeled passed.
- Symptom: the fresh-install helper printed a baseline pass while Core local management was restart-looping.
- Root cause: multi-unit `systemctl is-active` succeeds if any unit is active; baseline and reboot checks trusted that aggregate.
- Fix: require each named service to be exactly `active`; validate the reboot UUID; stop labeling a version/service check as signature verification; check the BIOS fixture's active/autostart NAT prerequisite before creating its domain/disk.
- Regression: `python3 -m unittest discover -s tests -p 'test_fresh_install_host.py'` passed 12 (reproduces aggregate exit-zero); corrected check refused the unhealthy install and accepted the healthy one after a distinct reboot. CI: both providers run the `test_*.py` discovery suite.
- Avoid: treating a multi-unit exit code, a Type=simple service start, or a package version as proof of a healthy product or verified artifact.

## BUG-0153 — Disk-step explanatory text clips at 800×600

- Date: 2026-09-13
- Status: fixed; eighth-ISO native wizard checks passed at 800×600 and 1024×768 (not a completed eighth-ISO install); broader production gates open.
- Symptom: the disk-step introductory and empty-state labels extended beyond the visible page at 800×600 (presentation only; target review and erase confirmation still worked).
- Root cause: long GTK labels were not constrained to the page width by automatic wrapping.
- Fix: explicit line breaks at the two clipped clause boundaries, preserving wording, selected disk, erase review and confirmation.
- Regression: `python3 tests/integration/installer-disk.py` and `make installer-check` passed; native wizard walkthrough checks both continuation bands (seventh ISO failed it; a first eighth-ISO run failed on a wrong screenshot coordinate, retained). CI: both providers run these suites; hosted CI not run.
- Avoid: accepting clipped destructive-step explanations because the final confirmation works, or claiming a guessed label-width adjustment is verified.

## BUG-0154 — CPU capability observation uses a connection libvirt rejects

- Date: 2026-09-13
- Status: fixed; bounded three-Core native rollout returned 59 models and healthy CPU checks.
- Symptom: fresh hosts reported a failed CPU-model check and no usable model list.
- Root cause: the shared inventory runner adds `--readonly`, but libvirt rejects `virConnectGetDomainCapabilities` on that connection (not a nested-virtualization limit).
- Fix: omit read-only mode only for `domcapabilities`; keep it, cancellation, output bounds and stderr redaction for every other query.
- Regression: `go test ./internal/agent -count=1` plus CPU-probe race tests (reproduce refusal, keep other queries read-only, reject invalid models). CI: both providers; hosted CI not run.
- Impact: API existing contract (restores `inventory.cpu_models`).
- Avoid: labeling a deterministic query error a hardware limitation, or removing read-only protection from every inventory query.

## BUG-0155 — Member reconciliation failure has no actionable diagnostic stage

- Date: 2026-09-13
- Status: diagnostic deployed and reviewed; the underlying intermittent repository deadline failures remain open.
- Symptom: a fresh member health check told the operator to inspect Core, but neither the error nor the journal named the failing stage.
- Root cause: reconciliation kept only a final error with fixed public text; the background caller discarded the error.
- Fix: report a bounded stage and safe outcome in the existing check (unknown stages fall back to generic), preserving retries, deadlines and raw-error secrecy.
- Regression: focused normal/race agent checks (redaction, classifications, recovery; source-bound tags for native deadline branches, not executed). Native rollout exposed `pool storage reconciliation repository deadline` then fresh recovery. CI: both providers; hosted CI pending.
- Impact: API existing contract (`check.error` text only); guide updated.
- Avoid: treating fresh failed health as task history, hiding failure because mounts work, or publishing raw peer/command errors and secrets.

## BUG-0156 — Periodic repository validation repeats clustered setup

- Date: 2026-09-14
- Status: partial; deployed to three lab Cores, but a five-minute idle capture still had three fresh deadline episodes across two members; six-writer verification pending.
- Symptom: members repeatedly reported fresh repository deadlines with mounted storage and no running guests.
- Root cause: periodic iSCSI validation repeated full cluster reconciliation (2.985–3.316 s measured) inside its three-second child context while the cluster worker did the same work; cluster scheduling also sat below the repository busy check.
- Fix: validate against the refreshed, activated, membership-bound Core record; keep native identity/access/locking checks and explicit refresh; schedule cluster work before the busy check. Deadlines unchanged.
- Regression: `go test ./internal/agent -run '^TestPeriodicPoolStorageReconcileSkipsDuplicateClusterRefresh$' -count=1` failed on the restored old dispatch; `go test -race ./internal/agent -run '^TestPeriodic' -count=1` passed. Scheduling order: source review only. Evidence: [the follow-up](qa/2026-09-14-cluster-storage-reliability.md). CI: both Go race jobs; hosted CI not run.
- Avoid: increasing deadlines, treating an aggregate timing label as a specific syscall, hiding current failure behind healthy mounts, or caching healthy state.

## BUG-0157 — Outage harness waits for all writes before restoring management

- Date: 2026-09-14
- Status: fixed; bounded diagnostic native outage and independent payload rereads passed (audit limits in the reliability QA report).
- Symptom: a slow writer could outlast the completion wait or management restart watchdog, invalidating the bounded outage.
- Root cause: the harness awaited all 600 fsynced writes while master management stayed stopped.
- Fix: observe six-writer progress and admission refusal during the outage, restore management in `finally`, then wait on one bounded budget.
- Regression: `python3 -m unittest tests.test_cluster_storage_workloads.ClusterStorageWorkloadTests.test_master_agent_outage_accepts_real_api_shaped_admission_record -v` fails with the old order; focused suite passed 53. CI: both providers' Python discovery.
- Avoid: extending the management outage merely to wait for slow storage writes.

## BUG-0158 — Fresh member access reports synchronously rewrite durable pool state

- Date: 2026-09-14
- Status: reviewed; full race checks passed; clean-build idle verification still found two deadline episodes, so native acceptance is open.
- Symptom: fresh NFS and clustered-iSCSI reconciliation still hit the three-second deadline after duplicate setup was removed.
- Root cause: each member access observation rewrote and flushed the master's pool-state file and directory (~350 ms per flush when slow). Native access validation also consumes budget; not every latency source is fixed.
- Fix: keep current access observations in master Core memory only (unknown after restart); repository and reservation/mutation state stay durable with error, generation, identity, freshness and execution gates.
- Regression: `go test ./internal/agent -run '^TestMemberReconcilePostsLaterAccessAfterEarlierDeadline$' -count=1` fails with the durable handler; `GOMAXPROCS=2 go test -race ./...` passes. CI: both Go race jobs; hosted runs not executed.
- Impact: API existing contract; guide updated for post-restart access; state owner core (storage and reservation records stay on Core; adoption reads master inventory plus fresh member observations).
- Avoid: removing configuration/ownership durability, hiding a failed report, extending probe deadlines or substituting an old healthy result.

## BUG-0159 — Clustered repository wrappers repeat QEMU access proof

- Date: 2026-09-14
- Status: fixed and reviewed; native acceptance open.
- Symptom: periodic clustered-iSCSI validation spent budget repeating the disposable QEMU-account probe.
- Root cause: `ValidatePoolStorageAccess` repeated the proof already done by `ValidateClusteredISCSIAt`; the preparation wrapper overlapped `PrepareClusteredISCSI` the same way.
- Fix: omit only the second wrapper probe for clustered iSCSI; NFS/SMB keep theirs; identity, quorum, fencing, mount and cross-Core locking checks remain.
- Regression: `go test ./internal/vmhost -run '^TestValidatePoolStorageAccessRunsOneClusteredQEMUProof$' -count=1` failed (two proofs) on the old wrapper; `GOMAXPROCS=2 go test -race ./internal/agent ./internal/vmhost` passed. Success counts use a clustered-validator seam. CI: both providers; no native or hosted pass claimed.
- Avoid: removing the underlying proof or testing only a new boolean predicate without exercising the actual wrapper.

## BUG-0160 — Reused workload markers can reject or mismeasure a new writer

- Date: 2026-09-14
- Status: fixed and reviewed; native outage acceptance open.
- Symptom: a reused guest could expose the previous run's progress after a new start marker, or the poll rejected an old marker before the new writer reached its barrier.
- Root cause: the writer published its start before resetting progress; the poll treated an older marker as terminal failure.
- Fix: sync progress zero before publishing the start marker; keep waiting for this run's marker within the bounded timeout.
- Regression: old producer/poll failed all three targeted checks; `python3 -m unittest tests.test_cluster_storage_workloads` passed 55, full discovery 242. CI: both providers.
- Avoid: trusting a new marker paired with an old progress counter or accepting a stale marker as evidence of a current writer.

## BUG-0161 — Workload exclusion rejects an adopted failed-create outcome

- Date: 2026-09-14
- Status: fixed and reviewed; corrected diagnostic outage and payload rereads passed (limits in QA).
- Symptom: the retained six-VM workload stopped before writes because an old failed-create exclusion had no Controller VM row.
- Root cause: the harness expected the original Controller's failed VM and request ID; a replacement Controller correctly adopts the terminal Core operation with an adoption request ID and no VM.
- Fix: accept only the exact adopted terminal failed operation with absent VM, keeping checkpoint binding, fresh hosts, GFS identity and all-Core domain/disk-directory absence checks.
- Regression: `ClusterStorageWorkloadTests.test_failed_create_exclusion_requires_terminal_absent_exact_slot` failed on the old guard; focused 55 and discovery 242 passed. CI: both providers.
- Avoid: deleting the exclusion, inventing a failed VM row, or accepting uncertain execution/native artifacts merely to make a workload resume.

## BUG-0162 — Outage admission check rejects the actual master transport error

- Date: 2026-09-14
- Status: fixed and reviewed; corrected diagnostic outage and payload rereads passed (limits in QA).
- Symptom: a terminal failed start during master-agent outage was rejected by the harness despite reporting the unavailable master inventory endpoint.
- Root cause: the harness required the words "pool master"; the Core guard can return the inventory request's connection refusal.
- Fix: also accept the exact refusal for the selected master's inventory URL, keeping terminal-failure and stopped-VM identity checks.
- Regression: `ClusterStorageWorkloadTests.test_refused_start_accepts_only_exact_observed_master_inventory_refusal` failed on the old guard; `python3 -m unittest tests.test_cluster_storage_workloads` passed 56. CI: both providers.
- Avoid: accepting any failed start as proof of master-authority refusal.

## BUG-0163 — Simultaneous member lock proofs contend on the same byte

- Date: 2026-09-14
- Status: fixed and reviewed; native rollout passed but the five-minute idle reliability check still failed.
- Symptom: concurrent member probes reported the master could not hold, or did not recover, the shared-storage lock on filesystems that enforce locks correctly.
- Root cause: per-challenge leases all locked byte zero of the same probe file; the client also treated any lock error as exclusion.
- Fix: negotiate a challenge-derived nonzero range (byte zero for legacy/older masters); bind release to member and lease; reject unknown modes, malformed challenges and non-conflict errors; cap active leases at 64 (exact replay before capacity). Expiry is not VM fencing.
- Regression: `go test ./internal/agent -run '^TestPoolProbeDistinctChallengesDoNotConflict$' -count=1` failed on the old code; the authenticated two-member test got HTTP 409 with the old rule; `go test ./internal/agent` and `-race` passed. CI: both Go race jobs; no hosted run claimed.
- Impact: API changed Core-peer lock mode/acknowledgement contract (documented in API reference); Controller schemas unchanged.
- Avoid: sharing one byte among independent proofs, treating arbitrary I/O errors as exclusion, or assuming a new range without confirming peer support.

## BUG-0164 — Cluster lifecycle repeats connection preparation after native start

- Date: 2026-09-14
- Status: fixed and reviewed; native rollout passed but the latest idle reliability check still failed on both members (one deadline on Core2, two on Core3 in 150 samples).
- Symptom: cluster reconciliation added repeated native work alongside repository validation; remaining deadlines not fully attributed.
- Root cause: after Connect and native start/apply, the lifecycle called Prepare, repeating CHAP configuration, login, device publication and GFS preflight.
- Fix: keep Connect before start/apply, then use the full post-start validator (LUN identity, GFS metadata, CIB/quorum/mount, path and QEMU checks).
- Regression: overlay restoring the two old Prepare callsites failed both focused tests; `go test ./internal/agent -count=1` and `-race` passed. CI: both Go race jobs; no hosted result claimed.
- Avoid: removing full post-start validation, dropping distinct QEMU/path checks, or claiming duplicate calls explain every deadline without timing evidence.

## BUG-0165 — Storage reconciliation performs unrelated VM inventory work

- Date: 2026-09-14
- Status: fixed, reviewed and diagnostically deployed; clean idle/loaded reliability verification open.
- Symptom: managed-inventory processing (2.399–2.663 s aggregate) consumed repository budget before native storage validation.
- Root cause: two member reconciliation reads and four periodic master peer handlers used the full managed inventory path (VM promotion, media/credentials, libvirt domain listing) for storage only.
- Fix: a read-only durable storage projection for those six reads, reusing generation folding, detach tombstones and journal checks; pending entries excluded, unreadable ones reported.
- Regression: `TestMemberReconcileReadsStorageWithoutUnrelatedVMObservation` and the master-handler test failed on old-path overlays; `go test ./internal/vmhost ./internal/agent` and `-race` passed. CI: both Go race jobs; hosted CI not run.
- Avoid: globally replacing managed inventory reads that need VM/media state, or claiming this explains every native deadline.

## BUG-0166 — Concurrent checks repeat identical native GFS metadata queries

- Date: 2026-09-14
- Status: fixed, reviewed and diagnostically deployed; idle reliability still failed on Core3; no deadline elimination claimed.
- Symptom: overlapping repository, cluster-worker and storage-monitor GFS metadata queries took up to 2.611 s, exhausting the repository deadline.
- Root cause: identical simultaneous queries ran independently; gfs2-utils 3.6.1-1 journal inspection issues buffer-cache flushes on startup (not proven to explain all latency).
- Fix: share only a running query for the exact device string; drop it on completion; waiters keep their own deadlines and run their own read once after a failed leader.
- Regression: `TestGFS2MetadataCoalescesOnlyConcurrentSameDeviceReads` failed on the old path; `go test ./internal/vmhost -count=1`, `-race`, and `go test ./internal/agent ./internal/vmhost -count=1` passed. CI: both Go race jobs; no hosted result claimed.
- Avoid: retaining completed healthy results, extending another caller's context, serializing unrelated devices, or deleting journal verification.

## BUG-0167 — GFS2 access probes contend in one shared directory

- Date: 2026-09-14
- Status: fixed and deployed; native probes passed on three hosts; idle reliability still failed once on Core2; revised loaded checks open. An earlier pathname-based candidate silently fell back on the mode-2770 root (failed receipts retained).
- Symptom: QEMU access checks spent much of the budget creating/removing temp files in one shared GFS2 directory (a contributor, not the sole cause).
- Root cause: every Core mutated the same GFS2 directory.
- Fix: hold GFS root and per-host leaf by directory descriptors, validate held filesystem/mount and root-owned leaf mode, anchor create/cleanup and child working directory to them; root permissions and non-GFS behavior unchanged. Empty leaves are regenerable artifacts.
- Regression: old-root overlay failed the per-host placement assertion; `go test ./internal/vmhost -count=1`, `go test ./internal/agent -count=1` and `-race` passed. CI: both Go race jobs; hosted CI not run.
- Avoid: caching healthy access, removing fsync or permission checks, changing operator root permissions, or trusting a leaf pathname beneath a writable parent.

## BUG-0168 — NFS access probes contend in the export root

- Date: 2026-09-14
- Status: fixed; three-Core native NFS proof, six-guest NFS workload and 150-sample health check passed; the separate post-GFS/SMB-startup reliability investigation remains open.
- Symptom: under six GFS2 writers, member NFS reconciliation took 3.436 s and reported a repository deadline though access and payloads passed.
- Root cause: NFS still used the export-root temp-file probe (3.423 s proof: 1.894 s create, 850 ms remove); a contributor, not every deadline.
- Fix: extend the descriptor-anchored per-Core leaf (root-owned mode 0711) to NFS; a leaf-owner mismatch keeps the full-root proof for root-squash compatibility without trusting the leaf. SMB unchanged.
- Regression: `go test ./internal/vmhost -run '^TestQEMUStorageAccessNFSUsesPerMachineLeaf$' -count=1` failed on the GFS-only selector; full vmhost/agent normal and `-race` passed. Root-squash fallback is unit-tested only (lab export was no_root_squash, mode 0777, not changed). CI: both Go race jobs; hosted not executed.
- Avoid: accepting non-root probe leaves, changing an operator's export policy, increasing deadlines, omitting file sync or calling a passing payload a health pass.

## BUG-0169 — Concurrent same-host GFS2 checks repeat the full QEMU proof

- Date: 2026-09-14
- Status: fixed and reviewed; combined native acceptance pending.
- Symptom: a loaded clustered-iSCSI reconciliation exhausted its budget; a second full QEMU proof (1.217 s) overlapped on the same Core and repository.
- Root cause: monitor, reconciliation and admission callers repeated the full file proof concurrently in the same held per-Core directory.
- Fix: share only a running GFS2 proof with the same held mount/device/inode and QEMU UID; each caller keeps its own traversal, validation and context; no completed result retained; a live waiter reruns once after a failed leader. NFS keeps individual proofs.
- Regression: `go test ./internal/vmhost -run '^TestQEMUGFSAccessCoalescesConcurrentLeafProof$' -count=1` failed on the old source; full vmhost/agent normal and race passed (UID/leaf key checks are key-level, not concurrent native users). CI: both `go test -race ./...` jobs; hosted CI not run.
- Avoid: sharing by pathname alone or across QEMU identities, extending a waiter's deadline, caching a completed proof, suppressing cleanup failure, or claiming the loaded deadline is gone before a native rerun.

## BUG-0170 — SMB credential replay repeatedly syncs an unchanged mount file

- Date: 2026-09-14
- Status: fixed and reviewed; six-guest SMB workload passed; reliability open (seven failed observer samples from two member repository deadlines, unattributed).
- Symptom: loaded SMB reconciliation spent 567–1196 ms re-saving its existing credential.
- Root cause: replaying an accepted credential unconditionally created, synced and renamed identical bytes and synced the directory.
- Fix: reuse only an exact-content, bounded, regular, single-link, effective-user-owned mode-0600 credential; otherwise atomic repair; stat/read errors propagate. No cache.
- Regression: inode-retention check failed on the original via overlay; `GOMAXPROCS=2 go test ./internal/vmhost -count=1`, agent equivalent and `-race` passed. The wrong-owner test requires root and skips otherwise. CI: both Go race jobs; hosted CI not run.
- Avoid: caching a successful credential check, ignoring unsafe file metadata, weakening secret-file permissions or claiming latency improvement before measuring the repaired build.

## BUG-0171 — QEMU access proof changes an already exact file mode

- Date: 2026-09-14
- Status: fixed and reviewed; clean native acceptance open.
- Symptom: NFS file proofs spent hundreds of milliseconds on Chmod; a reconciliation reached 2.950 s of its 3 s budget (earlier deadlines not attributed).
- Root cause: unconditional Chmod after Chown even when the held file already had the exact mode (native: stat 3–6 µs vs Chmod 4.055–6.424 ms idle).
- Fix: skip Chmod only when the held descriptor proves a regular exact-0600 file with the QEMU owner; otherwise repair. Fsync, Close, access checks, cleanup and deadlines unchanged.
- Regression: `TestQEMUStorageAccessSkipsOnlyExactQEMUModeChmod` failed behaviorally on the original (one call, expected zero); vmhost/agent `GOMAXPROCS=2 go test -count=1` normal and `-race` passed. CI: both Go race jobs; hosted CI not run.
- Avoid: assuming creation mode survives umask/owner changes, comparing only permission bits, trusting a pathname instead of the held file, skipping the rest of the proof, or claiming intermittent deadlines closed before native acceptance.

## BUG-0172 — NFS monitor and reconciliation duplicate a live file proof

- Date: 2026-09-14
- Status: fixed and reviewed; clean native acceptance pending.
- Symptom: Core2 timing showed 12 overlapping monitor/reconciliation NFS file proofs (0.589–2.065 s) on the sole NFS repository; duplicate work, not the cause of every deadline.
- Root cause: the in-flight held-leaf proof selector admitted only GFS2.
- Fix: admit eligible held NFS leaves to the mount/device/inode/QEMU-UID key; per-caller validation and deadlines, failed-leader retry and independent root-squash fallback kept; no completed cache.
- Regression: `TestQEMUNFSAccessCoalescesConcurrentLeafProof` failed on the GFS-only selector; `GOMAXPROCS=2 go test -count=1 ./internal/vmhost`, `-race` and agent normal/race passed. CI: both Go race jobs; hosted CI not run.
- Avoid: sharing across hosts or different held leaves/UIDs, completed-health caching, bypassing preflight or cleanup, extending deadlines, or attributing the separate SMB lock-probe failure to this work.

## BUG-0173 — Closing one SMB probe lease releases another lease's lock

- Date: 2026-09-14
- Status: fixed; clean three-Core rollout, 150-sample idle observation and six-guest SMB, NFS and clustered-iSCSI GFS2 workloads passed (bounded no-fault runs; see the QA follow-up).
- Symptom: members intermittently reported "shared storage OFD lock is not exclusive across Cores" for SMB while the master held their challenge byte.
- Root cause: one open description per lease; on the lab SMB stack (Samba 4.22, Linux 6.12 cifs, SMB 3.1.1) closing one description released another's held OFD range (one shared description kept range B in 20 of 20 rounds).
- Fix: concurrent leases on one lock file share a reference-counted description closed after the last range unlocks; a second lease on an already held range is refused.
- Regression: `TestPoolProbeConcurrentLeasesShareOneDescription` failed on the per-lease-descriptor overlay; `go test -count=1 ./internal/agent` and `GOMAXPROCS=2 go test -race` passed. Asserts the invariant only (local filesystems cannot reproduce SMB close). CI: both Go race jobs; hosted CI not run.
- Avoid: `nobrl` or other options disabling byte-range locking, longer lease/probe deadlines, treating it as SMB-unsupported, or assuming it covers other same-host multi-descriptor SMB locking.

## BUG-0174 — Pool advertise returns 400 for a wrong HTTP method

- Date: 2026-09-14. Status: fixed; regression passed; hosted CI has not run.
- Symptom: a non-POST request to the Core peer route `/api/v1/pool/advertise` returned 400 "invalid peer origin" instead of 405.
- Root cause: the method check shared one condition and error path with body decoding.
- Fix: after peer authentication and before reading the body, return 405 with `Allow: POST`; `/api/v1/pool/join` already did.
- Regression: `TestPoolPeerExactRoutesRejectWrongMethodWith405` failed on the old handler (400, no `Allow`) and passes; `go test ./internal/agent` normal/race pass; both CI providers run this suite.
- Avoid: combining method and input validation in one error path, or checking the method before authentication.

## BUG-0175 — Empty storage-cluster proof list is served as plain-text null

- Date: 2026-09-14. Status: fixed; regression passed; hosted CI has not run.
- Symptom: `GET /api/v1/pool/storage-cluster/proof/{repository_id}` with no recorded proofs returned `null` as `text/plain; charset=utf-8`, contradicting the documented JSON array.
- Root cause: a nil slice was JSON-encoded without an explicit Content-Type, so net/http sniffed the type.
- Fix: start from an empty slice and set `Content-Type: application/json`. The only caller decodes `null` and `[]` identically, so mixed-version pools are unaffected.
- Regression: `TestStorageClusterEmptyProofListIsJSONArray` failed on the old handler and passes; `go test ./internal/agent` normal/race pass; both CI providers run this suite.
- Avoid: documenting accidental content sniffing, or making callers depend on `null` versus `[]`.

## BUG-0176 — ISO URL browser check selects storage hidden for a stale host

- Date: 2026-09-14. Status: fixed (test); passes locally; hosted CI rerun pending.
- Symptom: the controller-browser job timed out at `tests/integration/iso_url_browser.py` selecting **Destination storage** ("did not find some options").
- Root cause: ISO-ready storage is intentionally filtered to repositories whose owner Core is fresh (`isoReadyStorage` in `web/src/managed.tsx`, `storageOwnerFresh` in `web/src/storage.tsx`); the check's stale-host step still tried to select that hidden option.
- Fix: the stale-host step now asserts the option is absent, the current-access help is shown and **Download ISO** is disabled; product behavior unchanged.
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/iso_url_browser.py --evidence .cache/iso-url-browser` passes; web typecheck/tests/build pass; both GitHub and Gitea phase1 workflows run it.
- Avoid: re-offering storage from stale hosts to satisfy an outdated check; run browser checks whenever storage filters change.

## BUG-0177 — Member reconcile deadline test is flaky under CI race load

- Date: 2026-09-14. Status: fixed (test); stress passes; hosted CI rerun pending.
- Symptom: `TestMemberReconcilePostsLaterAccessAfterEarlierDeadline` intermittently failed under CI `-race` load with "expired first repository published healthy member access".
- Root cause: the test's 10ms budget also covered the master list request and each TLS access POST, so reconciliation could fail before the first repository's fake ran; the late master handler write also raced the test's read. Product ruled out: the HTTP transport refuses to send an already-expired request, so an expired repository cannot publish access.
- Fix: test budget raised to 1s with unchanged assertions; product code unchanged.
- Regression: `go test -race ./...` in both CI providers; stress (32 parallel race builds) failed 256/640 before and passed 1280/1280 after with zero races.
- Avoid: millisecond deadlines around real network test stages, or blaming product publish-after-expiry without checking the transport's up-front context check.

## BUG-0178 — Standalone pool master cannot register NFS or SMB storage

- Date: 2026-09-14. Status: fixed; focused and package tests pass locally; hosted CI rerun pending.
- Symptom: `storage-register` for NFS failed on a single-host pool with "pool membership is unavailable for remote storage operation".
- Root cause: the managed worker's fail-closed membership gate treated a missing pool peer record as unavailable membership. A standalone master has no peer record until it issues its first join offer, so every remote storage command was refused.
- Fix: superseded by BUG-0183. The interim `remoteStorageMembershipUnavailable` special case was removed; every Core now records itself as its pool's master at startup.
- Regression: `TestSingleHostCoreIsItsPoolMasterForRemoteStorage` in `internal/agent/managed_test.go`, run by `go test -race ./...` in both GitHub and Gitea phase1 workflows.
- Avoid: using `Membership` errors as proof of a joined host; distinguish "never joined" from "joined but unreadable".

## BUG-0179 — SMB registration has no domain field and hides existing folders

- Date: 2026-09-14. Status: fixed; focused tests pass locally; hosted CI rerun pending.
- Symptom: SMB credentials accepted only account and password, so workgroup/AD logins (for example a TrueNAS domain) could not be supplied. Nested share folders were typed into **Existing directory within share** instead of being marked in the folder picker.
- Root cause: `StorageCredentials` and the CIFS/smbclient auth file had no `domain` line; discovery listed folders without probing `.nex-storage-marker`.
- Fix: optional `domain` on the credential handoff, discovery request and mount auth file; member transfer copies it; the picker shows **E** when a marker is present and no longer offers a typed share subdirectory.
- Regression: `TestStorageCredentialMountFileIncludesDomain`, `TestPoolSMBCredentialPreservesDomain`, `TestDiscoverSMBWritesDomainAndLeavesNonMarkerFoldersUnmarked`, `tests/integration/storage_repositories_browser.py`; both phase1 workflows run those suites. Native NAS login not run.
- Avoid: stuffing `DOMAIN\\user` into Account, interpolating folder names into smbclient `-c`, or treating `existing` as proof of a healthy registration.

## BUG-0180 — Install ISO forced onto the same repository as VM disks

- Date: 2026-09-14. Status: fixed; focused tests pass locally; hosted CI rerun pending.
- Symptom: Create VM listed no ISO, or Controller returned 409 *the install ISO must be on one of the VM's disk storage targets*, whenever the ISO library was a different repository from the VM disks. Typical case: ISO on the default local library, disks on NFS or SMB.
- Root cause: UI, Controller create/update/insert-media, and Core create/update all required `media.storage_id` to match a VM disk `storage_id`. Libvirt only needs a readable ISO path on the placement host.
- Fix: attach any ISO imported and verified on the placement host; keep the Core check that the file is still at the host-published `nex-media/<id>.iso` path on the ISO's own storage.
- Regression: `TestCreateAndInsertISOFromSeparateStorage`, `TestPublishedMediaPathIndependentOfDiskStorage`, `TestManagedMediaInsertFromSeparateStorage`; `go test -race ./...` in both GitHub and Gitea phase1 workflows. Browser wizard not walked through.
- Avoid: treating the CDROM source path as a disk-repository constraint.

## BUG-0181 — ISO on SMB storage fails verification as writable

- Date: 2026-09-14. Status: fixed; focused and package tests pass locally; hosted CI rerun pending; not re-run on a lab host.
- Symptom: VM creation failed with "ISO size or immutable permissions changed" for an ISO imported to SMB storage.
- Root cause: SMB repositories mount with `file_mode=0600`, so the import's `chmod 0444` has no effect; `verifyISO` rejected any file with a write bit.
- Fix: `verifyISO` in `internal/vmhost/managed.go` no longer checks permission bits; regular file, size and SHA-256 still prove the content is unchanged.
- Regression: `TestReceiveISO…` case in `internal/vmhost/managed_test.go` accepts a 0600 ISO and refuses changed content; run by `go test -race ./...` in both GitHub and Gitea phase1 workflows.
- Avoid: using permission bits as an integrity check on remote filesystems whose mount options fix file modes.

## BUG-0182 — Shared-storage VM cannot start on a single-host pool

- Date: 2026-09-14. Status: fixed; focused and package tests pass locally; hosted CI rerun pending; not re-run on a lab host.
- Symptom: `vm-power` start of a VM on NFS/SMB storage failed with "pool membership is unavailable" on a pool with only its master.
- Root cause: `AdmitPoolVMExecution` publishes VM inventory before every non-create shared VM command, and `publishPoolVMInventory` returned the `Membership` error when the host had no pool peer record. A standalone master has none until it issues a join offer. Same gap as BUG-0178; `poolVMRequestPeer` already treated a missing record as standalone.
- Fix: superseded by BUG-0183. The interim skip in `publishPoolVMInventory` was removed; a single-host Core is now its pool's recorded master and publishes like any master.
- Regression: `TestSingleHostPoolMasterPublishesSharedVMInventory` in `internal/agent/pool_vm_test.go`, run by `go test -race ./...` in both GitHub and Gitea phase1 workflows.
- Avoid: calling `Membership` on standalone paths without first distinguishing "never joined" (no peer record) from "joined but unreadable"; check every shared-storage caller when fixing one.

## BUG-0183 — Single-host pool had no recorded pool master

- Date: 2026-09-14. Status: fixed; agent, local Core and Controller tests pass locally; hosted CI rerun pending; not run on a lab host.
- Symptom: a pool with one host failed pool operations with "pool membership is unavailable" (BUG-0178 NFS/SMB registration, BUG-0182 shared-VM start), although a single-host pool's host is its pool master.
- Root cause: Core created its pool CA and `PoolMembership` only when the master issued its first join offer. Until then there was no membership record, and pool paths each guessed what a missing record meant, some refusing and some treating the host as a separate standalone mode.
- Fix: `EnsurePoolMembership` (`internal/agent/peer.go`) runs at agent startup and records an enrolled Core with no record as the ready master of its own one-member pool, sharing `initPoolMaster` with `CreatePoolJoinOffer`. `JoinPoolPeer` may replace a record where the Core is still its pool's only member. The no-record branches in `managed.go`, `pool_vm.go` and `pool_vm_inventory.go` were removed, so a missing or invalid record always fails closed. The periodic worker still replays local storage operations unless the pool has other members. With a valid single-host membership, background agent work started reading host identity after the agent stopped, racing a re-enrollment (found by `go test -race ./internal/localcore`). `Run` now waits for its ticker and cluster goroutines, starts the member-only storage reconcile pass only on members and does not wait for it (a hung remote mount could outlive its deadline), and the local handoff (`internal/localcore/handoff.go`) waits for its pool scope recovery goroutine before reporting the agent stopped. Independent review found the earlier version, which also waited for the storage reconcile pass, could hang agent stop on a hung mount; corrected before acceptance.
- Regression: `TestSingleHostCoreIsItsPoolMasterForRemoteStorage`, `TestSingleHostPoolMasterPublishesSharedVMInventory`, `TestSingleHostSharedVMStillUsesDurableOwnerAndPermit`, and `TestPoolPeerJoinRecoversLostResponse` (single-host masters joining); run by `go test -race ./...` in both GitHub and Gitea phase1 workflows.
- Avoid: modelling a single-host pool as "standalone" or treating a missing membership record as a mode; create the record, then let every path use the one master rule.

## BUG-0184 — Guest installer eject empties the CD tray while Nex still shows the ISO inserted

- Date: 2026-09-15.
- Status: fixed in the working tree; Go race suite, focused regressions and the console browser check pass locally. Native guest-install walkthrough and hosted CI still outstanding, so acceptance stays open.
- Symptom: during a guest OS install (Debian netinst at **Select and install software**), the installer asks to reinsert the install disc even though the operator did not eject it in Nex. The live QEMU tray is empty; Nex still records the original ISO as inserted.
- Root cause: the guest issues an eject after the base system; QEMU opens the virtual tray and disconnects the ISO. Nex does not observe that out-of-band tray change, so the durable `media_id` and console CD/DVD label stay loaded. Re-inserting the same ISO is then a no-op in the UI. On pool-shared VMs this cannot be recovered from the console (BUG-0185).
- Fix: Core reports the live drive of every running VM as `GuestInfo.media` (`loaded`, `empty`, `no-drive`) from the dumpxml the guest probe already reads (`internal/vmhost/guest.go`), so no extra libvirt call and no durable state change. The console (`web/src/main.tsx`) labels the recorded ISO **(ejected in the VM)** when the tray is observed empty, keeps that ISO in the insert list and explains how to reconnect it; a re-insert stays pending until the host observes the disc back. `changeMedia` no longer records an empty tray while the persistent definition still names the ISO: with the live tray already empty it clears the persistent definition alone (`--config`, no `--live`, which real `virsh` refuses on an empty tray), and `mediaObserved` settles an interrupted running eject only when both are empty. `applyLive` changes each side only where it differs, so an edit can also reconnect a guest-ejected disc.
- Regression: `TestManagedMediaAfterGuestEject` and `TestObserveGuest`/`TestManagedApplyLiveReconcilesDevices` cases in `go test -race ./...`; `tests/integration/console_controls_browser.py` check *A guest-ejected tray is reported as ejected…*. Each failed with the fix reverted (engine: no `virsh` call at all; UI: `Locator expected to have text 'demo-installer.iso (ejected in the VM)'`). Both GitHub and Gitea `phase1` workflows run both. Evidence: [checks](qa/console-media-guest-eject/checks.md). Acceptance still needs a running install that guest-ejects the disc, re-presents the same library ISO from the console and continues in the installer; do not mark that verified from simulated runs.
- Avoid: telling the operator they ejected the ISO; requiring a stop; clearing `media_id` because the tray opened; or reading an empty live tray as proof an eject reached the persistent definition.

## BUG-0185 — Console CD/DVD eject and ISO-library insert fail on shared VMs

- Date: 2026-09-15.
- Status: fixed in the working tree; Go race suite and focused admission regressions pass locally. Native shared-storage walkthrough and hosted CI still outstanding, so acceptance stays open.
- Symptom: **CD/DVD** on the VM console cannot eject, and cannot insert an ISO that is already in the pool ISO library. The task fails with `shared VM media changes require a durable pool definition transition`. Observed on a VM whose disks use pool-shared storage.
- Root cause: `AdmitPoolVMExecution` fail-closes `vm-media` for any VM on the pool-shared path (`internal/agent/pool_vm.go`). Console insert/eject is `vm-media`; that command updates durable `media_id` without the master's prior→next journal transition used by `vm-update`. Edit VM can change the ISO; the console cannot.
- Fix: Core, not the Controller, decides how a media request is applied. `PoolVMMediaCommand` (`internal/agent/pool_vm.go`, called from the managed worker before any reservation or admission) rewrites a `vm-media` request that would change a pool-shared VM's definition into the `vm-update` at revision + 1 under the same operation ID, so the existing reserve/commit/abort transition, journal evidence and inventory settlement apply unchanged. A redelivered operation reuses the exact journaled command (`ManagedEngine.JournaledCommand`), because `Execute` compares the whole command for one operation ID. A request that changes nothing — re-inserting the recorded ISO to reconnect a tray the guest ejected (BUG-0184) — keeps the `vm-media` path under the `media` owner admission the master already implements. A raw `vm-media` that would rewrite a shared definition is still refused.
- Regression: `TestSharedVMMediaChangeBecomesDurableDefinitionUpdate`, `TestSharedVMMediaReinsertKeepsDefinitionAndIsAdmitted`, `TestSharedVMMediaRedeliveryReusesJournaledCommand` and the retained `TestSharedVMMediaUsesOwnerAdmissionBeforeExecution` in `go test -race ./...` (both GitHub and Gitea `phase1`); all three new tests failed with the rewrite and admission change disabled. Evidence: [checks](qa/console-media-guest-eject/checks.md). Acceptance still needs the native walkthrough: from a running shared-storage VM's console, eject succeeds, insert attaches a library ISO, and both survive re-adoption by a replacement Controller.
- Consequence to know: a definition-changing media request on a shared VM is now a revision, so it takes the same rules as an edit — a stopped VM's insert re-runs the host capacity check, and the Controller's mirror is one revision behind until the host's next report, which can refuse an immediate **Edit VM** with "refresh the VM and edit again". Both are existing `vm-update` behaviour, not new state.
- Avoid: applying `vm-media` on a shared VM without a master transition; deriving the next revision again for a redelivered operation; teaching the Controller which VMs are shared instead of letting Core decide from its journal; recording a media change on a shared VM without a revision (the next inventory publication then fails "shared VM definition conflicts with durable inventory" and wedges pool VM admission); documenting Edit as a substitute for the console control the UI already offers.

## BUG-0186 — ISO build needs BuildKit for one image and fails after the slow work

- Date: 2026-09-15. Status: fixed; the matched `dlm-controld` image builds on the legacy builder and `python3 -m unittest discover -s tests -p 'test_storage_packaging.py'` covers it.
- Symptom: `installer/build.sh` failed a local 0.2.3 build with `the --chmod option requires BuildKit`, then `Unable to find image 'nex-dlm-pacemaker-abi:latest' locally` and `matched dlm-controld build did not produce its pinned package` — after the Controller appliance and the Core package had already been rebuilt (the expensive part of the build).
- Root cause: `packaging/cluster-storage/Dockerfile` used `COPY --chmod=755`, which only the BuildKit builder understands. Docker 29 without the `buildx` component falls back to the deprecated legacy builder, which every other Nex Dockerfile builds on. CI runners have BuildKit by default, so the workflows never saw it.
- Fix: plain `COPY` plus `RUN chmod 755` in that Dockerfile; both builders accept it and the image is unchanged.
- Regression: `test_iso_rebuilds_dlm_against_the_pinned_pacemaker_fencing_abi` in `tests/test_storage_packaging.py` now rejects BuildKit-only `COPY|ADD|RUN --chmod|--chown|--link|--mount` in every tracked Dockerfile; it failed on the old file (`packaging/cluster-storage/Dockerfile needs BuildKit; the legacy builder cannot run it`) and passes with the fix. Both `phase1` workflows run `python3 -m unittest discover -s tests -p 'test_*.py'`. Evidence: [ISO build checks](qa/iso/2026-09-15-core-0.2.3-build.md).
- Avoid: BuildKit-only Dockerfile syntax in this repository, and ordering a cheap image build after the appliance and package rebuilds where a syntax failure costs the whole run.

## BUG-0187 — Storage downgrade guard refused every upgrade on a host with storage state

- Date: 2026-09-15. Status: fixed; reproduced and re-verified against real apt 3.0 on Debian 13, regression added. Not yet released: the published 0.2.3 package still carries the broken guard.
- Symptom: the built-in updater and `apt` could not upgrade Nex Core. After downloading the new package, the install failed with `nex-core-local: refusing APT downgrade while version-2 storage state is retained; upgrade to a storage-format-v2 package instead`, `E: Sub-process /usr/lib/nex/storage-downgrade-guard --core returned an error code (1)`, although the target package does contain `usr/share/nex/core-local/storage-format-v2` and is a newer version.
- Root cause: `target_archives` in `packaging/core-local/storage-downgrade-guard.py` read APT's `DPkg::Pre-Install-Pkgs` version-3 input as one line per package. APT sends one line per dpkg **action**: the unpack names the `.deb`, and the matching configure of that same package carries the pseudo-filename `**CONFIGURE**` (a removal carries `**REMOVE**`). The configure line has no field ending in `.deb`, so the guard yielded an empty archive and took its fail-closed branch. Any host with version-2 storage state was therefore blocked from every upgrade, Core and Controller alike, since both packages install this one script. The existing unit test fed a single hand-written line, so no check ever saw APT's real input.
- Fix: skip lines whose action is a known APT pseudo-filename; a line naming neither an archive nor a known pseudo-action still fails closed, and the archive line still decides.
- Regression: `test_apt_guard_refuses_legacy_archives_only_when_v2_state_requires_it` in `tests/test_storage_packaging.py` now drives the guard with APT's captured two-line payload; it failed before the fix and passes after. Both `phase1` workflows run `python3 -m unittest discover -s tests -p 'test_*.py'`. Evidence: [checks](qa/apt-downgrade-guard/checks.md), including a disposable Debian 13 container where real `apt-get` reproduced the refusal, upgraded cleanly after the fix, and still refused a package without the v2 marker.
- Operator recovery: the guard that runs is the installed one, so a fixed package cannot install past it. Move `/etc/apt/apt.conf.d/91nex-core-storage-downgrade` aside, upgrade, and confirm the new package wrote it back ([software updates](guide/software-updates.md)).
- Avoid: parsing an APT hook protocol from its documentation alone; asserting hook behaviour with a hand-written payload instead of one captured from the apt the product ships with; treating "cannot identify the archive" as the only fail-closed case without enumerating the actions APT actually sends.

## BUG-0188 — Ejecting a disc from a running VM was refused as a boot order change

- Date: 2026-09-15. Status: fixed; reproduced by the regression checks below, which fail with the fix reverted. Not yet released: 0.2.3 hosts still carry the broken record.
- Symptom: **CD/DVD → Eject** on the console of a running VM fails, and the `vm-media` task reports `stop the VM first to change the boot order`. Observed on a VM created with install media, whose disks are on pool-shared storage. Ejecting while the VM is stopped works, and inserting an ISO while it runs works.
- Root cause: `ApplyVMMedia` (`internal/protocol/vmmedia.go`) recorded an eject as `media_id` cleared **and** `boot.media_boot_order` reset to 0. Since BUG-0185 a pool-shared VM's media change is applied as the master-reserved `vm-update` transition, and `NeedsStop` lists a changed boot order as stop-first, so Core refused the whole eject before doing anything. Every VM created from the wizard with an ISO is install-media-first (`media_boot_order` 1), which is why it appeared as soon as media-first creation shipped. The record was also wrong on its own terms: `virsh change-media --eject` swaps only the drive's source and leaves each device's `bootindex` in place, the emptied drive's included, so the zeroed record described a domain no host has. The shared-media fixture in `internal/agent/pool_vm_test.go` had `media_boot_order` 0, so no check exercised an install-media-first eject.
- Fix: `ApplyVMMedia` records `media_id` only. The CD drive keeps its place in the boot order while empty (already how `VMBoot`, `ValidateVMSpec` and the published contract describe it), so an eject is not a boot order change, `NeedsStop` reports nothing, and the derived `vm-update` applies live. `docs/API.md` and the generated reference no longer claim an eject clears `media_boot_order` or that a non-zero order requires `media_id`.
- Regression: `TestApplyVMMediaNeedsNoStop` and `TestApplyVMMediaKeepsBootOrder` (`internal/protocol`), `TestSharedVMMediaChangeBecomesDurableDefinitionUpdate` with an install-media-first fixture and a `NeedsStop` assertion (`internal/agent`), `TestManagedRecordVMMediaKeepsBootOrderOnEject` and `TestManagedMediaChange` (`internal/vmhost`), `TestMirrorVMMediaKeepsBootOrderOnEject` and `TestMediaRequestsAreAuthorizedDurableAndDispatched` (`internal/controller`). Command: `go test -race ./...`; all seven failed with the one-line fix reverted (`media change classified stop-first: [stop the VM first to change the boot order]`). Both GitHub and Gitea `phase1` workflows run that suite. Acceptance still needs the native walkthrough: eject and re-insert a disc from the console of a running install-media-first VM on a real host.
- Consequence to know: a VM ejected by an older Core has `media_boot_order` 0 recorded while its domain still carries the drive's `bootindex`. `validateManagedDefinition` compares the CD drive's expected boot order against the host's persistent definition, so a live edit of such a VM is expected to fail observation with `VM disk assignment or boot order differs from requested ownership` (derived from that comparison; not reproduced in a lab). Stop the VM and use **Edit → Save changes** once, which redefines the domain from the record and is where the CD drive boot order can be set again. A later insert does not repair the record: an insert never invents a boot order. No automatic repair is implemented.
- Avoid: recording state a media change does not produce on the host; asserting media behaviour with a fixture whose boot order is 0; relaxing `NeedsStop` for media instead, which only moves the failure to `observeDefinition`, where the expected XML would then contradict the domain.

## BUG-0189 — A refused storage credential offer stopped the Core agent

- Date: 2026-09-15. Status: fixed; regression added, reproduced and re-verified natively in a disposable nested SMB lab; hosted CI not run.
- Symptom: after a storage credential request was retried, the Core agent exited and systemd restarted it in a loop (`persist storage credential handoff: storage credential persistence failed: storage credential replay conflicts with existing receipt`, then `persist storage credential handoff: credential handoff rejected (HTTP 404)` on each restart) until the Controller's 15-second offer expired. On a packaged host the local management service restarts with it.
- Root cause: `redeemStorageCredentials` (`internal/agent/storage_credentials.go`) returned on the first offer it could not redeem, and `reportStorage` (`internal/agent/run.go`) wrapped that as a `persistenceError`, which `Run` treats as fatal. A refused offer is a problem for one operator request, not a failure to persist Core state. The Controller keeps listing an offer that was delivered but never acknowledged, while refusing to hand it out again (404), so every restart hit the same offer and exited again. The same early return also skipped the rest of the offers and the commands in that report response.
- Fix: redeem each offer independently; log a refused offer (invalid offer, non-200 handoff, mismatched handoff, receipt replay conflict, local save or acknowledgement failure) and continue. The request that was refused receives no receipt, so the operator's credential call still returns 503 and can be retried; nothing is acknowledged that Core did not persist. The logged error omits the request URL, which carries the one-shot offer handle.
- Regression: `TestReportSkipsRejectedCredentialOffersWithoutStopping` in `internal/agent/storage_protocol_test.go` sends a 404 offer, a replay-conflict offer, a valid offer and a command in one report, and requires the report to finish, only the valid offer to be acknowledged and the command to be journaled; it failed before the fix (`a refused credential offer stopped the report: persist storage credential handoff: credential handoff rejected (HTTP 404)`) and passes after, with `TestReportRedeemsCredentialOfferBeforeCommandAdmission` unchanged. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Native: in the disposable lab the fixed agent received the same conflicting credential replay, logged the conflict and then one 404 per report until the offer expired, returned 503 to the retried request, and kept running (`NRestarts=0`, unchanged PID).
- Avoid: treating any per-request remote refusal as a persistence failure; exiting the agent on data the Controller can resend; re-fetching an already delivered offer as the recovery path for a lost acknowledgement.

## BUG-0190 — A remote NFS/SMB mount that failed at Core boot was never retried

- Date: 2026-09-15. Status: fixed; regression added; verified natively in a disposable lab.
- Symptom: after Core and its SMB server rebooted together, the Nex mount unit failed once (`mount error(111)`) because the server was not listening yet, stayed `failed`, and the repository was still `unavailable` five minutes later with the server up. **Recheck** failed with the misleading `storage mount source does not match registration`. A manual `systemctl start` of the unit fixed it.
- Root cause: every path that observes an already registered remote repository is read-only by design (storage monitor, periodic storage reconcile of unsettled journals only, member access validation, recheck, guest start); only registration and update start mount units, and the generated `.mount` unit has no retry. `verifyRemoteMount` reads `findmnt --target`, which returns the parent filesystem when nothing is mounted, so it reported a source mismatch.
- Fix: `startRemoteMount` (`internal/vmhost/storage_mounts.go`) starts the existing, unchanged Nex-marked NFS/SMB unit only when `systemctl is-active` reports it inactive; it never restarts, rewrites, reloads, enables or reads credentials, and the normal identity, marker and QEMU access checks still decide health. Core's one-minute worker calls it for every settled repository on masters and members with `--no-block`, so an unreachable server never holds the engine lock; **Recheck** starts it and waits (bounded) before verifying. An unmounted path now reports `remote storage is not mounted`.
- Regression: `TestStorageRecheckStartsInactiveRemoteMountUnit`, `TestStartRemoteMountsSkipsActiveBusyAndAdminUnits` and `TestVerifyRemoteMountReportsUnmountedParent` in `internal/vmhost/storage_mounts_test.go`; before the fix the package did not compile against them (`startRemoteMount` and `StartRemoteMounts` did not exist); with temporary empty stubs they failed with `recheck result = {... Error:storage mount source does not match registration}`, `calls = []string(nil)` and `unmounted path error = storage mount source does not match registration`. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. The worker call placement in `internal/agent/managed.go` is covered by code review and the native walkthrough, not a unit test. Native (disposable nested lab, single-host pool, HEAD agent): with the SMB server stopped, Core rebooted and the Nex mount unit failed at boot; the repository reported `remote storage is not mounted`; about 20 seconds after the server was started again the one-minute worker queued the unit start, systemd mounted it and the repository reported healthy, with no manual mount and no Recheck.
- Avoid: making read-only inspection, the storage guard or the monitor mount things; `restart` or unit rewrites on a possibly active mount; `findmnt`/`statfs` probes from the worker goroutine (they can hang on a hung hard NFS mount); blocking `systemctl start` while holding the engine lock; adding `Restart=`/automount options that existing hosts would never receive.

## BUG-0191 — Disk ownership check refused when another VM's repository was unmounted

- Date: 2026-09-15. Status: fixed; regression added; the unit regression reproduces the failure, and the native lab confirmed the affected VM then settled and operated (BUG-0192), but the unmounted-repository refusal itself was not re-created natively after the fix.
- Symptom: inserting an ISO into a stopped pool-shared VM on a healthy SMB repository ended `needs_attention` with `other domain disk cannot be inspected for exclusive ownership`. Another VM on the same host had its disk on an SMB repository whose mount failed at boot (BUG-0190), so that disk path did not exist.
- Root cause: `exclusiveDisk` (`internal/vmhost/engine.go`) stats every `<source file>` of every other domain and treated any stat error, including a missing file, as fatal. It runs inside `observeDefinition` after `virsh define`, so the refusal came after libvirt had already applied the update, which is why the entry was `needs_attention` rather than a clean failure (settled by BUG-0192).
- Fix: a missing other-domain source (`os.ErrNotExist`) is skipped: the owned disk exists with a single link, so a nonexistent path cannot be that file. Every other stat error (permission, I/O, not-a-directory, loops) still refuses. Duplicate backing across repositories remains refused at registration (`registrationConflict`, `storageSameBacking`).
- Regression: `TestExclusiveDiskSkipsMissingOtherDomainSources` (missing source allowed; ENOTDIR still refused; a missing source next to a real shared disk still refused) and the unchanged `TestExclusiveDiskRejectsOtherDomainsAndHardlinks` in `internal/vmhost/engine_test.go`. The new test failed before the fix (`missing source: err=other domain disk cannot be inspected for exclusive ownership`). Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: ignoring every stat error; moving the check before `define` and calling a post-define refusal `failed` (the definition had already changed); relying on this point-in-time check for cross-mount aliases, which it never detected.

## BUG-0192 — One stuck shared VM update disabled every shared-storage VM operation in the pool

- Date: 2026-09-15. Status: fixed; regressions added; verified natively in a disposable lab.
- Symptom: after an ISO insert on a stopped pool-shared VM ended `needs_attention` (BUG-0191), the task showed `VM update outcome is pending durable failure reconciliation`, and afterwards a retried insert on that VM and an eject and a start on a different, freshly created shared VM all failed with `managed VM update evidence is unavailable`, even with every repository healthy again. Only force stop worked, and nothing settled it.
- Root cause: (1) `localPoolVMInventory` (`internal/agent/pool_vm_inventory.go`) failed the whole pool inventory publication when any shared VM lacked exact update evidence, and every shared VM operation publishes inventory first. (2) No periodic path settled a remote-storage `vm-update`: `promote` skips remote storage, `reconcile` runs only on redelivery, and the remote create reconcile covers only `vm-create` on multi-member pools. (3) `FailedVMUpdateEvidence` accepted only a failure at `validating`, although this update had already been defined in libvirt. (4) `confirmPoolVMUpdateOutcomes` (`internal/agent/run.go`) required the agent journal command to be `vm-update`, but a console media change rewritten into an update (BUG-0185) is journaled as `vm-media`, so even a settled or aborted media update stayed `needs_attention`. (5) The same function reported that outcome as `vm-update`, but the Controller only applies a host outcome to an operation with the same ID, host and kind (`internal/controller/managed.go`), and the console request is a `vm-media` operation, so the task stayed `needs_attention` in Task history after Core had settled it.
- Fix: a VM without exact evidence is still published, without an `Updates` entry, so only that VM stays behind its pending-update guards. `ManagedEngine.ReconcileRemoteVMUpdate` runs in Core's one-minute worker on every pool host and only observes: a definition and state matching the update record `succeeded` and the master commits it from inventory; a shut-off VM whose inactive definition is exactly its single succeeded predecessor after allocation or define records `failed` with `unapplied_observed`, which is accepted as abort evidence; anything else (live attach, running guest, rename, other differences) stays `needs_attention`, and an entry already superseded by a succeeded create or update of the same VM at the same or a newer revision (for example an operator retry) is left untouched. `confirmPoolVMUpdateOutcomes` resolves a `vm-media` entry to the managed journal's `vm-update` for the same operation and VM, failing closed otherwise, without rewriting the agent journal. Its outcome is reported under the requested `vm-media` kind so the Controller's task settles too.
- Regression: `TestSharedVMUnresolvedUpdateDoesNotBlockPoolInventory` (failed before: `an unresolved update of one shared VM blocked pool inventory: managed VM update evidence is unavailable`), `TestSharedVMRewrittenMediaOutcomeConfirmsWithPoolOwner` (failed before: `settled media update was not confirmed with the pool owner … needs_attention` and `proven-unapplied media update was not aborted at the pool owner`; after the kind correction was added it also failed before that change with `settled media outcome must keep the requested kind the Controller tracks: [… Kind:vm-update …]`) in `internal/agent/pool_vm_test.go`; `TestReconcileRemoteVMUpdateSettlesAppliedDefinition` (including `entry superseded by a succeeded retry is left alone`, which failed before its guard with `superseded update observed`), `TestReconcileRemoteVMUpdateAbortsProvenUnappliedDefinition` and the `observed unapplied post-native stage is failed evidence` subtest in `internal/vmhost/managed_test.go` (did not compile before: the reconcile did not exist). Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Native (same lab, rebuilt agent, Core and SMB server rebooted together): the stuck ISO insert settled as applied without operator action, the pool master's pending update cleared and the recorded media became revision 2; start, force stop and eject then succeeded through Nex on both the formerly stuck VM and a second shared VM; after the outcome-kind correction was deployed the stale Task history entry for the original insert changed from `needs_attention` to `succeeded` within seconds of the agent restart. BUG-0191's missing-source case was not re-created natively after the fix (all repositories were mounted by then); its evidence is the unit regression.
- Known limit: when an update is proven unapplied after `allocating`, a new disk file it created is left in the VM directory (not referenced by the domain, not deleted); a retry allocates the next free disk name.
- Avoid: re-running define, attach, allocate or start to settle; calling a post-define observation failure `failed` without proving the predecessor definition is still in place; settling live attachments or renamed domains as unapplied; relabelling an attempt that a later succeeded revision already replaced; letting one VM's missing evidence fail the whole pool inventory; rewriting the agent journal command to make kinds match.

## BUG-0193 — One failed Controller GUI update wedged every later update

- Date: 2026-09-15. Status: fixed; regressions added; wedge reproduced and fix verified natively in a disposable lab (evidence under Regression).
- Symptom: on a Controller, **Updates** stayed on `Update requested…` with 0.2.4 installed and 0.2.5 available; nothing progressed, and no error appeared.
- Root cause: the Controller only writes `/var/lib/nex-controller/pending-update.json` and the progress text "Update requested"; the root helper `apply-update` is started by `nex-controller-update.path`, which fires on `PathExists=` as well as `PathChanged=`. Every failure path of the helper exited without removing the request (it was removed only after success), so systemd immediately re-ran the helper, which failed again (in the lab: "no newer nex-controller available" once the package had installed but the service restart failed), until `Start request repeated too quickly` left the path unit `failed` (`unit-start-limit-hit`). A failed path unit ignores new requests, and each new **Update** click only rewrote "Update requested", so the page waited indefinitely. `postinst` started the path unit without `reset-failed`, so even a package upgrade did not clear it. Separately, the helper wrote `update-progress.json` as root with mode 0600, which the Controller (running as `nex-controller`) cannot read, so failure errors and "Update complete" never reached the page.
- Fix: `apply-update` renames the request to `applying-update.json` before reading it, then reads it without following links or blocking (regular file only, size capped) and accepts only `package = nex-controller` and an `http(s)` `repo_url` without whitespace or control characters, because root copies both into the APT source and `apt-get` arguments (a newline could otherwise add `Trusted: yes`); any rejected, unreadable or planted request fails and is still consumed. removes the marker on success and failure, and removes a request queued during a successful apply. An unexpected exception restarts the service and reports the failure. Progress is written through a freshly created `O_EXCL|O_NOFOLLOW` file handed to the state directory's owner with `fchown`, so the Controller can read it and root never follows a path the Controller user could plant. `postinst` restarts the service unless the helper is actually running (`ActiveState` of `nex-controller-update.service`), clearing any stale request or marker (and its stale progress text) in that case, then runs `systemctl reset-failed` on both update units before starting the path unit. The software updates guide tells operators on 0.2.5 or earlier to clear the request and upgrade with `apt`, because the installed helper is the one that runs.
- Regression: `ControllerUpdateHelperRecoveryTests` in `tests/test_distribution_packaging.py` — `test_failed_apply_consumes_request_and_reports_readable_progress` (failed before the fix: `failed apply left its request to retrigger the path unit`), `test_package_upgrade_clears_a_wedged_update_path_unit` (failed before: `postinst never clears a path unit that hit its start limit`), `test_postinst_restarts_unless_the_helper_is_running_and_clears_stale_requests`, `test_progress_write_does_not_follow_a_planted_symlink` (failed before: `root followed a symlink the Controller user can plant`), `test_corrupt_request_is_consumed_and_reported`, `test_unexpected_error_releases_the_request_and_restarts_the_service` and `test_successful_apply_clears_its_marker_and_a_request_queued_meanwhile`, `test_request_fields_cannot_inject_apt_configuration_or_other_packages`, `test_planted_marker_directory_or_fifo_cannot_wedge_the_helper` and `test_postinst_clears_stale_progress_with_a_stale_request`; each failed against the unfixed helper or an earlier draft of this fix (two independent reviews found the later cases). Command: `python3 -m unittest discover -s tests -p 'test_*.py'`, run by both GitHub and Gitea `phase1` workflows. Native (disposable Debian 13 systemd VM; the published 0.2.5 package, then 0.2.6 packages built from the fixed tree): the 0.2.5 helper, after one failed apply, re-ran five times in five seconds and left the path unit `failed` (`unit-start-limit-hit`) with the request and its progress text still present. Installing the final 0.2.6 package over that state removed the stale request and progress and returned the path unit to `active`. A request whose `repo_url` carried a newline and `Trusted: yes` was rejected with `invalid pending update request: repo_url must be an http(s) URL without whitespace or control characters`, leaving the APT source and `updates.conf` unchanged. A normal failing request ran the helper exactly once, left the path unit active with no request files, and wrote progress readable by `nex-controller`. With the preceding 0.2.6 build of the same helper design, a symlink planted at `update-progress.tmp` was not followed (its root-only target unchanged) and a stale `applying-update.json` was removed by a package reinstall, which also issued the service restart.
- Avoid: deleting the request before apt runs while postinst gates its restart on that file (the restart would happen mid-dpkg); root writes, chmods or chowns by path in a directory an unprivileged user can write; copying request fields from an unprivileged writer into root's APT configuration without validation; dropping `PathExists=` (a request written before a reboot would never apply); treating the page's wait as a UI bug; relying on a package upgrade to repair a helper that cannot run the upgrade.

## BUG-0194 — BuildKit Dockerfile check scanned untracked scratch copies

- Date: 2026-09-15. Status: fixed; test-only, no product behaviour changed.
- Symptom: `python3 -m unittest discover -s tests -p 'test_*.py'` failed on a developer workstation in `test_iso_rebuilds_dlm_against_the_pinned_pacemaker_fencing_abi` with `.cache/iso-audit.*/src-media/nex-source/packaging/cluster-storage/Dockerfile needs BuildKit`, although the repository Dockerfile was fixed in BUG-0186.
- Root cause: the BUG-0186 regression globbed every `**/Dockerfile*` under the checkout, excluding only `node_modules`, so an old extracted source tree left under the ignored `.cache` directory was checked. CI checkouts have no such tree, so the check was green in CI and red locally for reasons unrelated to the change under test.
- Fix: skip Dockerfiles whose first path component is `.cache`, `.git`, `dist` or `vendor` (and `node_modules` anywhere). The check still reads the tree rather than `git ls-files`, so it works in a copied checkout without `.git`.
- Regression: the same test passes with the stale copy still present, and still fails when a tracked `packaging/…/Dockerfile` uses `COPY --chmod` (verified with a temporary probe file, then removed). Command: `python3 -m unittest discover -s tests -p 'test_*.py'`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: letting repository checks read ignored scratch directories; deleting local scratch to make a check pass.

## BUG-0195 — VM creation was refused when the host's free memory was low

- Date: 2026-09-15. Status: fixed; regressions added; verified natively in a disposable lab (see Regression).
- Symptom: creating a VM failed with `insufficient available memory including 2 GiB host reserve` (Task `9b57b3a1…`), although a VM that cannot start yet can still be defined; the same refusal applied to editing a stopped VM.
- Root cause: `managedCapacity` (`internal/vmhost/managed.go`), the preflight shared by `vm-create` and stopped `vm-update`, compared the VM's memory ceiling plus the 2 GiB host reserve against `MemAvailable`, the memory free at that instant. That is a start-time condition, not a property of a stopped definition. Meanwhile `powerVM`, used by every Core start (manual `vm-power` start, `start_after_create`, and pool autostart through `Execute`), checked no memory at all, so a start that could not fit failed later inside libvirt.
- Fix: `managedCapacity` keeps the CPU, KVM, machine-capability and total-memory dedicated-promise checks but no longer reads available memory. `powerVM` receives the VM's memory from its newest non-failed journaled definition (so an unresolved larger memory edit is not bypassed) and, before `virsh start` of a shut-off VM, refuses with `insufficient available memory to start this VM including 2 GiB host reserve` when `MemAvailable` is below the ceiling plus the reserve; a start of a running VM stays a no-op. A created VM whose automatic start is refused stays defined and stopped, reported as `VM created but not started: <reason>`. The Controller's check against the host's total memory is unchanged. Libvirt's own autostart replay at host boot for non-shared VMs is outside Core's control and is not covered.
- Regression: `TestManagedCapacityIgnoresCurrentlyAvailableMemory`, `TestManagedPowerStartChecksAvailableMemory`, `TestManagedStartAfterCreateChecksAvailableMemory` and `TestManagedPowerStartUsesUnresolvedLargerMemory` (added after independent review; it failed before its change) in `internal/vmhost/memory_start_test.go`, using test seams for `/proc/meminfo` and `/dev/kvm` (with only those seams added to the previous code they failed: `dedicated VM refused on low available memory: insufficient available memory including 2 GiB host reserve`; a low-memory start returned `Status:succeeded`; a refused automatic start still issued `virsh start`). Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Native (disposable nested lab Core, agent built from this tree, host with 7947 MiB total and 7508 MiB available): creating a 7691 MiB VM through the Controller with `start_after_create` succeeded; its automatic start was reported as a failed `:start` task `VM created but not started: insufficient available memory to start this VM including 2 GiB host reserve` and the domain stayed `shut off`; a manual start failed with the same message and the domain stayed `shut off`; a 512 MiB VM on the same host was created and started (`running`) and then force-stopped.
- Avoid: judging a stopped definition by memory free at that instant; dropping the check without adding it at start; relaxing the dedicated-memory promise, which is a durable reservation; sizing the start check from the last succeeded revision when an unresolved larger edit exists.

## BUG-0196 — No virtual TPM, so Windows 11 could not install

- Date: 2026-09-16. Status: fixed; regressions added; **not verified on a host** (see Regression).
- Symptom: Windows 11 Setup refused every non-IoT edition with "This PC doesn't currently meet Windows 11 system requirements". Only the IoT editions, which have relaxed requirements, installed. The guest had UEFI, Secure Boot, 4 vCPU, 8 GiB and 64 GiB, so the unmet requirement was the TPM.
- Root cause: Nex defined no `<tpm>` device anywhere, and `swtpm` was not installed on Core 0.2.7 (`dpkg -l swtpm` not installed; absent from `dist/package-manifest.tsv`). There was no per-VM option to ask for one, so a supported guest OS could not be installed at all.
- Fix: `protocol.VMBoot` gains `tpm` (`2.0`, `none` or omitted); omitted is no TPM, so a definition written before the field keeps exactly the devices it had. `managedDomainXML` emits `<tpm model='tpm-crb'><backend type='emulator' version='2.0'/></tpm>` when asked. Core ships `swtpm` and `swtpm-tools`. A TPM is refused when the host's own `virsh domcapabilities` does not advertise the emulator backend and the `tpm-crb` model, rather than defining a device that cannot start — the host's answer, not a guess from a package list. The Create VM wizard defaults it on for a Windows guest and off otherwise.
- Known libvirt limitation, and what actually closes it: `undefine --tpm` removes state only for TPM devices the persistent definition still carries — `qemuExtDevicesCleanupHost` iterates `def->tpms` — so a redefine that drops `<tpm>` orphans `/var/lib/libvirt/swtpm/<uuid>` with the endorsement key and everything the guest sealed, and no undefine flag can clean it afterwards. `NeedsStop` therefore refuses removing a TPM outright; that, not the flag, is what keeps host state and definition in step. Deleting a VM that still has its TPM passes `--tpm` and works. The limitation is from reading libvirt's source, not documented contract, and is unverified on a host. The same limitation applies to `--nvram` for a UEFI-to-BIOS edit and is logged as BUG-0214.
- Regression: `TestManagedDeleteRemovesStateAnEditDroppedLater` (`internal/vmhost/delete_test.go`) and `TestNeedsStopRefusesRemovingATPM` (`internal/protocol/managed_test.go`), plus the TPM cases in `internal/vmhost/firmware_test.go`. The delete test was proved to fail without its fix by reverting only that fix by hand: `undefine ... --nvram` carried no `--tpm` for a VM whose TPM an edit had dropped. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Verified natively 2026-09-16 on real libvirt/QEMU/swtpm: the TPM reached the live domain, swtpm manufactured an EK and the guest used it, Windows reported an activated TPM 2.0, **Windows 11 Enterprise LTSC (the non-IoT edition this entry is about) installed and booted**, and deleting a TPM VM removed its swtpm state and NVRAM. See [the verification report](qa/2026-09-16-windows-11-fix-verification.md). Nex Core packaging, the installer and the Controller were not exercised.
- Avoid: adding a TPM to every VM silently, since it changes measured boot and can bind BitLocker keys; deciding host TPM support from a package list instead of asking libvirt; assuming an undefine flag cleans state for a device the definition no longer has; treating the emitted XML as proof the guest installs.

## BUG-0197 — Opening the console after Start missed the boot prompt

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: starting a VM and then opening the Console missed the roughly 5 s "Press any key to boot from CD or DVD" prompt, so the guest fell through to "BdsDxe: No bootable option or device was found" or the OVMF front page. Reproduced after **Start** and after Setup's own reboot. "Start after creation" guaranteed the miss.
- Root cause: the console attaches when it is opened, and the only way to start a VM was from a different screen, so the operator could not be attached before the guest reached the prompt. The verified workaround — open the Console first, then press Start — existed only as operator folklore and was not in the guide.
- Fix: the console toolbar carries a Start control (`power?: {state, busy?, onStart}`), shown when the VM is not running, so the operator is attached before the guest boots. The workaround became the product behaviour rather than documentation for a trap.
- Regression: `web/src/console.test.mjs`. Command: `npm --prefix web run test`, run by both workflows. Chromium against deployed Controller/Core connected before boot and visibly captured the CD boot prompt.
- Avoid: documenting a timing workaround instead of removing the race; assuming an operator can predict when to open the console.

## BUG-0198 — One CD drive, so a driver disc could not be attached

- Date: 2026-09-16. Status: fixed; disposable VirtIO system-disk acceptance passed.
- Symptom: with a VirtIO disk, Windows Setup listed no disks. A VM had one CD drive, so the virtio-win driver ISO could not be attached alongside the install ISO. The only route to "Ready to install" was to swap the CD to virtio-win mid-Setup with the console toolbar, load the driver, then swap back.
- Root cause: `managedDomainXML` defined exactly one SATA CDROM, and the media contract had no way to name a drive.
- Fix: every VM defined by a host reporting `vm-media-drivers-drive-v1` carries two SATA CD drives. `VMCreateSpec.drivers_media_id` is the durable record of the driver disc, journaled on the pool master beside `media_id`; `VMMediaRequest.drive` selects `install` (or omitted) or `drivers`. Core resolves a drive through the libvirt user aliases `ua-nex-cdrom-install` and `ua-nex-cdrom-drivers`, so the target letters libvirt assigns are irrelevant. A domain defined by an older Core carries neither alias and exactly one cdrom, which is always the install drive. The wizard, edit form and console toolbar expose both drives. Windows device defaults were deliberately NOT changed to SATA/E1000E: that would trade VirtIO performance away permanently to work around a missing drive.
- Regression: drive and alias cases in `internal/vmhost/firmware_test.go`, `internal/vmhost/guest_test.go` and `internal/protocol/vmmedia_test.go`. A disposable Windows install loaded the VirtIO SCSI driver from the second drive, exposed the 64 GiB system disk and reached OOBE.
- Avoid: identifying a drive by the target letter libvirt happened to assign; assuming every domain has the new aliases; defaulting Windows to slow devices instead of supplying the driver disc.

## BUG-0199 — Secure Boot was silently on and could not be changed

- Date: 2026-09-16. Status: fixed; deployed-final-package lifecycle and guest state verified.
- Symptom: Secure Boot was on for every UEFI VM with no indication and no control. libvirt picked `OVMF_CODE_4M.ms.fd` with the enrolled-keys VARS and SMM; General showed only "UEFI". Unsigned boot media or drivers could not be used at all.
- Root cause: `firmware='efi'` was the only input to the domain (`managedDomainXML`), leaving the firmware build entirely to libvirt's own selection.
- Fix: `protocol.VMBoot` gains `secure_boot` (`on`, `off`, omitted), stated explicitly in the domain as `<firmware><feature enabled=... name='secure-boot'/><feature enabled=... name='enrolled-keys'/></firmware>`, with SMM when on. **Omitted means on with enrolled keys** — exactly what libvirt already selected — so an installed guest keeps the firmware it was installed with and nothing has its firmware pulled out from under it. Secure Boot requires UEFI; requesting it with BIOS is rejected. Shown and editable in the UI; changing it requires the VM to be stopped.
- Regression: Secure Boot cases in `internal/vmhost/firmware_test.go`, including omitted-field compatibility. The final package completed on → rename → off → on with NVRAM keys and boot entries retained; host SecureBootEnable changed `01` → `00` → `01`. A signed Debian guest reported EFI Secure Boot `01` on and no SecureBoot variable after off. The independent Windows TPM/Secure Boot matrix remains open.
- Avoid: letting libvirt choose a security-relevant firmware build implicitly; using a Go bool for a tri-state whose zero value would silently disable Secure Boot on every existing definition.

## BUG-0200 — An HTTPS-downgrade refusal was reported as a network fault

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: the official virtio-win "stable" URL failed in about 2 s with "ISO download failed: connection, redirect or timeout". The host had DNS and TCP 443 to the site; the direct HTTP URL then downloaded in 120 s. The operator saw a network problem where the answer was "this URL redirects to plain HTTP".
- Root cause: the server redirects HTTPS to HTTP and `publicISOClient`'s `CheckRedirect` correctly refuses the downgrade, but `fetchPublicISO` replaced every `client.Do` error with one generic message, so a deliberate policy refusal was indistinguishable from a timeout.
- Fix: policy refusals are their own error type (`isoPolicyError`) and are unwrapped from the `*url.Error` and reported verbatim, while genuine transport faults keep the generic text. URL text is still never echoed, because ISO URLs can be signed.
- Regression: `internal/vmhost/iso_download_test.go`, including an https-to-http redirect surfacing the downgrade reason and asserting no branch echoes the URL. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: collapsing distinct causes into one message to keep secrets out; a refusal and a fault need different operator actions.

## BUG-0201 — Display resize was offered when the guest could not resize

- Date: 2026-09-16. Status: fixed; regressions added.
- Symptom: before virtio-win drivers were installed, the console offered "Resize guest to window", General said "Display resize: Available · VirtIO display" and the API returned `guest.resize: true`, but the guest never resized: at an 1100 px viewport the framebuffer stayed 1280×800 after 12 s. It worked only after the driver installer finished.
- Root cause: `observeGuest` set `Resize` purely from the VirtIO video device in the domain XML. That is what QEMU would accept, not what the guest can do.
- Fix: `Resize` requires the VirtIO display **and** the guest to have opened the qemu-vdagent channel, which is the agent that applies the resolution. The console hides or disables the control with an honest reason when it is false, and the details page no longer blames a missing VirtIO display for a VM whose guest tools simply are not installed.
- Regression: `internal/vmhost/guest_test.go` covers VirtIO with and without the guest-side agent and a non-VirtIO display; the UI reason is covered in the web suites. Command: `go test -race ./...` and `npm --prefix web run test`, both run by the GitHub and Gitea `phase1` workflows.
- Avoid: advertising a capability from the device model rather than the guest; reporting absence of evidence as capability.

## BUG-0202 — Nothing offered to eject the install media after an install

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: after Windows installed, the ISO stayed inserted with the CD first in boot order, so every boot showed "Press any key to boot from CD or DVD" and a keypress restarted Setup. The operator had to find **Edit**.
- Root cause: nothing prompted an eject or reorder once a guest had been installed.
- Fix: the UI offers a one-click eject of the install media when a VM's install drive still holds media that is first in boot order. It is an offered action the operator confirms; Core does not guess that an install finished and does not silently rewrite the definition.
- Regression: covered in the web suites. Chromium offered and completed eject on a unique disposable Windows VM; fresh persisted install and driver media IDs were empty before fixture deletion.
- Avoid: inferring "the install finished" host-side and mutating a VM definition without the operator.

## BUG-0203 — A failed ISO download could not be removed or retried

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: a failed URL download left a permanent `virtio-win.iso · failed · 0 bytes` row in the ISO library with no delete and no retry.
- Root cause: Controller media routes were only `GET/POST /api/v1/media` and `POST /api/v1/media/download`; nothing could remove a record.
- Fix: `DELETE /api/v1/media/{id}` removes the library record of an `iso-import` whose operation is `failed` or `needs_attention`, audited as `media.discard`, and deletes only Controller-owned staging bytes. The ISO library now offers a confirmed **Discard failed import** action only for those rows; if a browser loses its DELETE response, it checks the library and treats an absent exact row as discarded before reloading. It returns 409 for an import still queued or dispatched, and **409 for one that succeeded**: the ISO then lives on its host, Core has no media-removal command, and Controller will not drop a record for bytes it cannot delete. Retry is `POST /api/v1/media/download` again under a new `request_id`; no second endpoint was added for what an existing one covers.
- Regression: `internal/controller/media_download_test.go` and `web/src/managed.test.mjs`. Chromium discarded a disposable HTTPS-downgrade failure; its row disappeared and task history remained.
- Avoid: making the Controller the only record of truth for host-held media; inventing a retry endpoint that duplicates an existing call; faking a deletion Core cannot perform.

## BUG-0204 — The upload banner still said "queued" after the transfer succeeded

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: after an ISO upload's host transfer succeeded, the banner still read "host transfer queued. Wait for a succeeded result before creating a VM", while the same page showed the `iso-import` task succeeded.
- Root cause: UI only. The API already returns a live `status` per media entry; the one-shot notice string interpolated the status as it was at POST time and was never refreshed.
- Fix: the banner follows the media entry's live status and distinguishes queued, succeeded and failed.
- Regression: covered in the web suites. A browser-uploaded disposable 64 MiB ISO changed from the dispatched notice to **ISO is on the host and can be used**.
- Avoid: rendering a captured status string as if it were live state.

## BUG-0205 — The default local repository was named "ISO" but held VM disks

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: the automatically registered local repository on a fresh host was named **ISO** although it also holds VM disks (`vm_disk` and `iso` capabilities), so Windows system disks landed on a repository called "ISO".
- Root cause: `DefaultStorageName` was the literal `"ISO"`, from when the directory was only used for ISOs.
- Fix: the default is `"Local storage"`. `RegisterDefaults` matches an existing registration on `DefaultStoragePath` and never on its name, so this changes only the name applied at first registration: a host that registered the directory under the earlier default keeps the name its pool master recorded, and an upgrade never renames, duplicates or re-registers it. The guide says so where it names the repository.
- Regression: `TestDefaultLocalStorageNameIsAccurate` in `internal/vmhost/pool_storage_test.go`, asserting against the `DefaultStorageName` symbol with a negative property (the name must not contain "iso", case-insensitively) so it catches the old default without pinning a wording; proved to fail against a `git archive HEAD` export with `default local repository name "ISO" does not describe storage that also holds VM disks`. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: renaming existing host-owned registrations to match a new default; keying "already registered" on a display name rather than the path.

## BUG-0206 — A new dedicated-memory VM showed an upgrade instruction meant for old ones

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: a VM created with dedicated memory on Core 0.2.7 showed "Existing VMs: upgrade Core, stop, then Edit → Save changes" on its details page. That sentence is for VMs defined by an older Core and is wrong for one whose dedicated memory is already applied.
- Root cause: the sentence was unconditional in the memory-mode description.
- Fix: shown only when it is actually true for the VM being viewed.
- Regression: covered in the web suites. Command: `npm --prefix web run test`, run by both workflows.
- Avoid: printing migration advice unconditionally in a details view.

## BUG-0207 — Console controls were clipped and covered at 1440×1000

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: at 1440×1000 the console's "Open in new window" link was clipped under the tab strip, and the Recent tasks drawer covered the lower console area. With Clipboard open only a thin strip of the guest display stayed visible, so text typed into the VM could not be watched.
- Root cause: console layout in `web/src/console.css`.
- Fix: the popout link is no longer clipped and the guest display stays usable at that viewport with the clipboard panel open, using relative layout rather than a fixed viewport assumption.
- Regression: Chromium at 1440×1000 kept Clipboard, both media controls, display selector, popout link, guest display and task history visible and usable without overlap.
- Avoid: claiming a layout fix without looking at it; hard-coding one viewport.

## BUG-0208 — The login page logged a 401 console error on every load

- Date: 2026-09-16. Status: fixed; deployed Controller/browser acceptance passed.
- Symptom: the login page requested `GET /api/v1/session` and got 401 before sign-in, logging a browser console error on every load.
- Root cause: the session probe treated the expected "not signed in" answer as an error.
- Fix: 401 before sign-in is handled as the expected answer rather than logged as a failure. Authentication is unchanged.
- Regression: covered in the web suites. Command: `npm --prefix web run test`, run by both workflows.
- Avoid: weakening the session check to silence a console message.

## BUG-0209 — Installer progress used a different step list and offered Go Back at the end

- Date: 2026-09-16. Status: fixed; visual installer acceptance passed on an earlier 0.2.8 build with unchanged installer code.
- Symptom: after **Erase disk and install**, progress and completion used a different sidebar (Region/Network/Access/Storage/Installation) from the wizard (Welcome/Host/Network/Disk/Review), and the completion page still offered **Go Back**.
- Root cause: the post-install screens are in `installer/gui/nex-shell.c`, a fragment included into cdebconf's `gtk/ui.c`, with its own `nex_stages[]` rail. The Go Back button is d-i's own action-box button from cdebconf's `go.c`, not a widget nex-shell.c creates.
- Fix: `nex_stages[]` follows the wizard's list plus the one step it does not have (Welcome/Host/Network/Disk/Review/Install), with completed steps in the wizard's style. nex-shell.c exports one predicate that `installer/build-gui.sh` gates both the `create_goback_button` call and the `is_action_box_filled` count on, as assert-guarded exact-text replacements like the rest of that file. Both had to move together: `is_action_box_filled()` assumes the Go Back button exists whenever `CAN_GO_BACK(fe)`, so merely hiding it would have made the count read "already filled" and cost the completion page its **Restart now** button. The same predicate replaces the three copies of the `finish-install/reboot_in_progress` literal.
- Regression: earlier 0.2.8 completion acceptance reached the prompt, remained open until **Restart now**, and then rebooted naturally. The final SHA passed unattended offline install, first boot and reboot, but its completion screen was not visually inspected. The build assertions remain the source-level guard.
- Avoid: removing a d-i button without the count that assumes it; editing `nex-wizard.c` for screens it does not render; claiming a build that was not run.

## BUG-0210 — A Windows guest got no Windows-specific device defaults

- Date: 2026-09-16. Status: fixed; regressions added; not verified on a host.
- Symptom: selecting Guest OS = Windows changed no devices. The clock was `offset='utc'` although Windows keeps local time in the RTC, and there were no Hyper-V enlightenments.
- Root cause: `GuestOS` was recorded but never consulted when building the domain.
- Fix: a Windows guest gets `<clock offset='localtime'>` and Hyper-V `relaxed`, `vapic` and `spinlocks retries='8191'`. Only those three: each is a long-standing default for Windows, while `vpindex`, `synic`, `stimer`, `reset`, `frequencies` and `tlbflush` each ask KVM for a capability a host or nested hypervisor can lack, and a domain that then refuses to start is worse than a missing enlightenment. Every other guest OS value leaves the domain exactly as it was.
- Regression: Windows clock and Hyper-V cases in `internal/vmhost/firmware_test.go`. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: enabling every available enlightenment because it is available; changing devices for guests that did not ask.

## BUG-0211 — A Windows 11 guest was reported as Windows 10

- Date: 2026-09-16. Status: fixed; regressions added.
- Symptom: after guest tools were installed, a Windows 11 guest was reported as "Windows 10 IoT Enterprise LTSC 2024" in `GET /api/v1/vms` `guest.os`.
- Root cause: the QEMU guest agent's product name comes from a registry value Windows 11 never renamed, and Nex reported it verbatim.
- Fix: when the product name says Windows 10 but the reported build is 22000 or above, the name is corrected. A missing or unparseable build reports what the guest said rather than guessing. Existing bounding of guest-reported strings is unchanged, so a hostile guest cannot inject text through this path.
- Regression: `internal/vmhost/guest_test.go` covers Windows 11 misreported as 10 (corrected), a genuine Windows 10 (unchanged), a missing or garbage build (unchanged) and a Linux guest (unchanged). Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: trusting a guest-reported product name as identity; rewriting it without a bound and a fallback.

## BUG-0212 — Typing into a Windows guest lost every shifted character

- Date: 2026-09-16. Status: fixed; exact deployed-console typing acceptance passed.
- Symptom: **Type into VM** sent `Nex-Test_Upper D:\Path\virtio-win-guest-tools.exe 123` and Notepad received `nex-test-upper d;\path\virtio-win-guest-tools.exe 123`: capitals became lowercase, `_` became `-`, `:` became `;`. Passwords, paths and commands could not be typed. The Run dialog then reported "cannot find d;\virtio-win-guest-tools.exe".
- Root cause: `typeToVM` sent each character as `client.sendKey(keysym(ch), null, …)` — a bare keysym with no KeyboardEvent code and no Shift press. A guest reads the scancode noVNC derives from the key code plus the modifier state it was sent, not the keysym, so every character arrived as whatever that physical key types unshifted.
- Fix: each character is typed as the physical US key that produces it, with Shift pressed around it when the character needs it and the KeyboardEvent code sent alongside the keysym. Characters outside the US layout keep the bare keysym they had.
- Regression: `web/src/console.test.mjs` decodes the emitted event sequence the way a guest does and asserts printable ASCII, `_`, `:`, the exact lab string and released Shift state. The final Windows console check passed the exact string including capitals and shifted characters.
- Avoid: sending a bare keysym and expecting a guest to infer the modifier; asserting the key table back at itself instead of decoding what the guest reads.

## BUG-0213 — A completed shutdown was reported as needing attention

- Date: 2026-09-16. Status: false-failure behavior fixed; historical and current timing remain unverified.
- Symptom: **Shut down** on a Windows guest with the guest agent connected powered the VM off, but the task ended `needs_attention` with "libvirt refused the shutdown request; inspect the VM on its host: virsh failed: signal: killed". The operator was sent to inspect a VM that had done exactly what was asked.
- Root cause: `Engine.virsh` capped every virsh call at 20 s. The retained A/B evidence proves the pre-fix client was killed at 20.138 s while the guest later shut off; killing that client did not establish that the host operation had failed. `powerVM` then turned the unobserved virsh failure straight into `powerRequestError`, which wraps `errPowerRequestUncertain` and is journaled as `needs_attention`, without asking the domain what happened. The historical ~215 s task gap and its intervening guest-agent timing are unrecoverable from retained evidence.
- Fix: `virshTimeout` bounds by whether the command takes libvirt's per-domain job. Every mutating subcommand Core actually issues — `shutdown`, `reboot`, `reset`, `start`, `destroy`, `define`, `undefine`, `attach-device`, `detach-device`, `change-media`, `domrename` — gets a bound that exceeds a real guest operation, because a job-taking call waits for whatever job is already running on that domain: `detach-device` already waits on the guest to release a device, and even a quick `define` blocks behind a long shutdown. Queries keep the short bound so a hung libvirt still fails fast instead of holding Core's shared engine lock; guest-agent calls stay short deliberately, since an unresponsive guest must not hold observation for minutes and they carry their own one-second libvirt timeout. `powerVM` then settles a failed call by observing the domain through `powerObserved`, which already holds the rules: reaching "shut off" proves a shutdown or force-stop took effect, while a guest still running proves nothing about a reboot, so a stop settles on the spot and a reboot stays uncertain for an explicit reconcile. `powerRequestError` now says "did not confirm" rather than "refused", because after observation the cause may be either a real refusal or a killed call, and libvirt's own stderr line still distinguishes them.
- Regression: `TestManagedPowerPostEffectFailureIsUncertain` (`internal/vmhost/power_test.go`) gained a first-attempt expectation and `TestVirshTimeoutSeparatesDomainJobsFromQueries` (`internal/vmhost/engine_test.go`) is new. Reverting only the observe-after-failure change reproduced the lab symptom exactly: shutdown and force-stop both reported `needs_attention` while the domain had reached shut off, and reboot and force-reboot stayed uncertain in both runs, confirming the uncertainty property was not weakened. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Native A/B evidence: the pre-fix binary returned `wall=20.138s`, `needs_attention`, `virsh failed: signal: killed`; the fixed binary returned `wall=1m0.073s`, `succeeded`; the guest reached `shut off` 17 s after the pre-fix run declared failure. A current final-package timing attempt submitted no graceful shutdown because guest-agent readiness did not arrive within bounded attempts. **Still open:** the historical ~215 s task gap has no recoverable cause, and current graceful timing is unverified.
- Avoid: one timeout for every libvirt call regardless of what it does; turning a transport failure into a verdict about the guest; killing a client to bound an operation the kill does not cancel; "fixing" this by treating every power failure as success.

## BUG-0214 — A UEFI-to-BIOS edit could orphan the VM's NVRAM file

- Date: 2026-09-16. Status: fixed; regression and disposable-host verification added.
- Symptom: a UEFI-to-BIOS edit could leave its firmware variable file because libvirt removes NVRAM only while the persistent definition still names UEFI.
- Root cause: Core redefined the BIOS domain before cleaning the UEFI state, leaving `undefine --nvram` with no firmware device to clean.
- Fix: new UEFI definitions use an explicit UUID-keyed NVRAM path. A UEFI-to-BIOS edit journals `cleaning-nvram`, observes the existing UEFI domain, records and validates its exact regular NVRAM path, undefines with `--nvram`, removes that exact path if libvirt left it behind, then defines the BIOS XML. Recovery observes a landed BIOS definition or an absent old domain before retrying; legacy UEFI edits retain their observed NVRAM path.
- Regression: `TestFirmwareVariablesStayWithVMIdentity`, `TestFinishFirmwareRemovalCleansNVRAMAndRetriesSafely`, and `TestReconcileRemoteVMUpdateFinishesFirmwareCleanupAfterValidation` in `internal/vmhost/firmware_test.go` cover UUID identity, legacy-path preservation, cleanup, retry and remote validation. Focused race checks passed. The disposable nested-lab probe accepted the template-free explicit path, preserved it through create/start/stop/rename, and removed it during simultaneous rename plus UEFI-to-BIOS cleanup. Evidence: [firmware variable-store verification](qa/2026-09-16-firmware-variable-store-verification.md).
- Avoid: redefining before the old firmware state is cleaned; trusting an arbitrary filesystem path; replaying an interrupted destructive libvirt operation without observation.

## BUG-0215 — A host without enrolled-keys firmware failed with an opaque libvirt error

- Date: 2026-09-16. Status: fixed; regression added; found while verifying BUG-0199 on real libvirt.
- Symptom: on a host whose OVMF build advertises `secure-boot` but not `enrolled-keys`, creating any UEFI VM failed at define with libvirt's `operation failed: Unable to find 'efi' firmware that is compatible with the current configuration`. Nex reported that verbatim as `VM definition outcome uncertain`. The message is true but never mentions Secure Boot, so the operator cannot tell which setting to change, and the VM directory is left retained for inspection.
- Root cause: BUG-0199 made Nex state `secure-boot` and `enrolled-keys` explicitly instead of letting libvirt pick a firmware build. That is correct — it is what keeps an installed guest on the firmware it was installed with — but it converts "this host has no such build" from something libvirt silently worked around into a hard define failure, and Nex had no preflight for it. The TPM path already asked the host through `virsh domcapabilities`; the firmware path did not.
- Fix: `hostSecureBootSupport` (`internal/vmhost/firmware.go`) reads the `firmwareFeatures` block libvirt publishes in `domcapabilities` — `secureBoot` and `enrolledKeys` enums — and `managedCapacity` refuses before defining, naming the setting: "this host's UEFI firmware has no enrolled Secure Boot keys, so a signed installer such as Windows would not boot: install the enrolled-keys OVMF build on Nex Core, or turn Secure Boot off for this VM". A libvirt that reports no `firmwareFeatures` block is not treated as evidence of a missing build, matching how the TPM `backendVersion` enum is handled. Debian, which Core is built on, ships the enrolled-keys build, so a compliant Core host is unaffected.
- Regression: `TestHostSecureBootSupportRefusesWhatTheHostCannotBuild` in `internal/vmhost/firmware_test.go`, using the two capability shapes this host actually reported (enrolledKeys "no" only, then "yes" and "no" after the enrolled build was published), plus an older-libvirt shape with no `firmwareFeatures` and a BIOS VM. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows. Verified natively: with only the secboot build present the define failed as described; after publishing an enrolled-keys descriptor the same VM defined, started, and libvirt selected `OVMF_CODE.secboot.4m.fd` with the enrolled VARS template. See [the verification report](qa/2026-09-16-windows-11-fix-verification.md).
- Avoid: stating a firmware requirement explicitly without asking the host whether it can meet it; passing libvirt's firmware-selection error through as the operator-facing reason; treating a silent capability block as a refusal.

## BUG-0216 — The Go test suite ran virsh against the developer's real hypervisor

- Date: 2026-09-16. Status: fixed; guard added; found by disrupting a real workstation.
- Symptom: on a workstation with libvirt installed, `go test ./internal/...` raised polkit authorisation dialogs at the logged-in user. One full run issued **28 real `virsh --connect qemu:///system` calls**, including `virsh event --all --loop`, which retries continuously; repeated runs produced 165 `domcapabilities` and 45 `list --all --uuid` authorisation requests in ten minutes. The desktop polkit agent was flooded, every dialog's authorisation cookie expired before it could be answered, and the operator's own correct password was rejected every time with a lockout backoff. Nothing legitimate needed authorising.
- Root cause: several test paths in `internal/vmhost`, `internal/agent` and `internal/localcore` reach code that shells out to `virsh` without installing the per-test PATH stub `fakeCommand` provides. On CI there is no libvirt, so those calls fail immediately and the tests already tolerate the failure; the defect is invisible there and only appears where libvirt is actually installed. Tests must never touch the host they run on.
- Fix: a `TestMain` in each of those three packages (`stubs_test.go`) puts a refusing `virsh` stub first on `PATH` for the whole package run, so an unstubbed path fails deterministically on every machine instead of reaching `qemu:///system`. Tests that install their own fake with `fakeCommand` are unaffected, because they prepend their own directory afterwards. No production code changed, and no test outcome changed: the calls already failed on CI.
- Recurrence guard: `tests/test_host_tool_isolation.py` fails if any `internal/` package that has tests and can reach virsh lacks the stub. It resolves imports by reading the sources, not by shelling out to `go list`: the contracts job that runs this suite installs no Go toolchain, so the first version failed there with `FileNotFoundError: 'go'` and broke CI. Making it skip when Go is absent was rejected — a guard that silently does not run is the exact failure mode this entry is about. It follows the **dependency graph**, not the text of each package: a literal `"virsh"` search would have produced a false positive on `internal/protocol` (whose `FenceVirsh = "virsh"` is a fencing-method identifier that runs nothing) and, far worse, would have missed `internal/localcore`, which never mentions virsh yet made 23 of the 28 calls through `internal/vmhost`. A package counts as executing virsh when it both names the binary and calls `exec.Command`, judged over the package rather than per line: excluding assignment lines also excluded `bin = "virsh"` followed by `exec.Command(bin, ...)`, an exec site written one line apart, which a probe package of exactly that shape confirmed went undetected before the rule changed. Proved to fail by deleting one guard: `nex.local/nex/internal/localcore has tests and can reach virsh, but installs no refusing stub`. A second case asserts the stub exits non-zero, so it surfaces a call instead of hiding it.
- Regression: verified by putting a logging `virsh` first on `PATH` and running the suite. Before the guard: 28 real calls (`list --all --uuid` 20, `domcapabilities` 4, `event --all --loop` 4) across vmhost 3, agent 4, localcore 23, controller 0. After the guard: **0**, with `go test -race ./internal/...` still green in every package. Command: `go test -race ./...`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: relying on "CI has no libvirt" to keep host-touching tests harmless; assuming a flood of authorisation prompts means a misconfigured polkit or a wrong password rather than a caller looping; blaming a concurrent agent for host activity before auditing your own test runs.

## BUG-0217 — A failed VM creation left a Controller-only VM row

- Date: 2026-09-16. Status: fixed; focused race regressions passed.
- Symptom: a Core `vm-create` rejection before libvirt, such as insufficient storage, left an unknown VM row in Controller while Core inventory had no VM. Deleting that row returned 404 because Core correctly had no resource.
- Root cause: Controller marked the dispatched create terminally failed but retained its `managed_resources` projection. Core `Applied` reports only succeeded VM definitions.
- Fix: the failed `vm-create` row remains as Controller task/audit and idempotent-replay data, but its managed-resource ID moves to the internal `failed-vm-create:<operation-id>` form so it cannot collide with a Core VM identity. VM-list, ownership, placement and adoption conflict queries exclude terminal failed rows. Historical adoption relocates a failed row with the same Core VM ID before storing Core's succeeded definition.
- Regression: `TestFailedVMCreateDoesNotLeaveControllerProjection` and `TestHistoricalFailedVMCreateStaysHiddenAndReplayable` in `internal/controller/delete_test.go`, plus `TestCreateVMSpecificationReferencesAndScope` in `internal/controller/managed_test.go`, which verifies a failed duplicate-MAC create is absent from `GET /api/v1/vms` while its error remains in `GET /api/v1/operations`. Focused same-ID race cases passed: `go test -race ./internal/controller -run 'Test(CreateVMSpecificationReferencesAndScope|FailedVMCreateDoesNotLeaveControllerProjection|HistoricalFailedVMCreateStaysHiddenAndReplayable)$' -count=1`. Full Controller race passed: `go test -race ./internal/controller -count=1` (`ok nex.local/nex/internal/controller 124.279s`). Both GitHub and Gitea `phase1` workflows run the existing Controller Go suite.
- Avoid: treating a Controller projection as proof that Core created a VM; deleting a failed request row and breaking its idempotent replay; letting terminal failures occupy VM ownership or name admission.

## BUG-0218 — Context-menu Delete closed the confirm dialog on mount

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: right-click **Delete…** on a VM in Resources closed the menu and nothing else appeared; General-tab Delete VM still worked.
- Root cause: `VMDelete` started with `open=false`, then its dialog sync effect called `onOpenChange(false)` on first paint. The context-menu host cleared `menuDeleteVM` and unmounted the dialog before `openSignal` could open it.
- Fix: seed `open` from `openSignal`, sync the `<dialog>` separately, and notify `onOpenChange` only after the first open/close transition. Menu polish: console/delete icons, aligned rows, disabled danger items no longer stay red.
- Regression: `web/src/managed.test.mjs` asserts `openSignal` seeds the confirm markup, closed without it, and the old sync+notify pattern is gone. Command: `npm --prefix web run test`, run by both GitHub and Gitea `phase1` workflows.
- Avoid: notifying a parent that unmounts on close from the initial closed mount of a signal-opened dialog.

## BUG-0219 — An interrupted UEFI-to-BIOS define left the VM with no domain

- Date: 2026-09-16. Status: fixed; regression added; not verified on a host. Follows BUG-0214.
- Symptom: a stopped UEFI VM was edited to BIOS, and the define that follows `undefine --nvram` was interrupted or refused. The VM then had no libvirt domain, recovery stayed `needs_attention`, and a new edit was refused with "VM not found or its state cannot be established on this host".
- Root cause: `finishFirmwareRemoval` journals stage `defining` before the define, but local `reconcile` and `ReconcileRemoteVMUpdate` re-entered it only at `cleaning-nvram`. At `defining` recovery only observed, and observation fails when the domain is absent.
- Fix: `firmwareRemovalPending` treats `defining` with a recorded NVRAM path like `cleaning-nvram` in both recovery paths: `reconcile` when the same operation is delivered again, and the periodic `ReconcileRemoteVMUpdate` for shared storage only. `finishFirmwareRemoval` confirms with `list --all --uuid` that the domain is absent, then defines the journaled BIOS XML. A UEFI domain found at `defining` is never undefined; recovery returns "VM definition changed during firmware cleanup; inspect the VM on its host".
- Regression: `TestReconcileFirmwareRemovalDefinesAfterInterruptedDefine` (`internal/vmhost/firmware_gate_test.go`). Failed with the pre-fix predicate (needs_attention, no define); a review mutation that removed the UEFI-at-`defining` guard also failed it. Passes now: `go test -race -count=1 ./internal/vmhost/ -run TestReconcileFirmwareRemovalDefinesAfterInterruptedDefine` ok. Runs in `go test -race ./...`, job `go`, in both GitHub and Gitea `phase1` workflows on push and pull request. Fake virsh only; no real-libvirt walkthrough.
- Avoid: journaling a recovery stage after a destructive step that recovery cannot resume from; replaying undefine; telling the operator to resubmit an edit for a VM whose domain no longer exists. Open limit: a local-storage edit already reported `needs_attention` at `defining` is not redelivered by Controller, and background `promote` only observes it, so it stays `needs_attention` until inspected on the host.

## BUG-0220 — A stopped edit that renamed a VM and changed Secure Boot failed after rewriting NVRAM

- Date: 2026-09-16. Status: fixed; regressions added; not verified on a host. Related: BUG-0199, BUG-0214.
- Symptom: one save that renamed a stopped UEFI VM and toggled Secure Boot ended `needs_attention` with "VM Secure Boot redefinition outcome uncertain". The variable store already had the new SecureBootEnable value while the definition did not, and every recovery pass retried the same failing define.
- Root cause: the Secure Boot define used XML with the new name for an existing UUID, and `domrename` ran only afterwards. libvirt refuses to define a known UUID under a different name; the existing fake virsh accepted it.
- Fix: the stopped branch moved to `ManagedEngine.applyStopped`. The read-only Secure Boot checks (NVRAM path, digest, enrolled keys when enabling, ownership metadata) run first in `secureBootSource`; then `domrename`; then the renamed XML is re-read into `SecureBootPreviousDomain` with the NVRAM path carried over; only then the variable edit and define. The later plain rename is skipped for this case. Review of the first version found that it renamed before those checks, so a refusal such as "no enrolled Secure Boot keys" left libvirt renamed while Nex recorded the edit as failed; the checks were moved ahead of the rename before this change landed.
- Regression: `TestStoppedRenameWithSecureBootToggleRenamesBeforeDefine` (fake virsh now refuses a define whose name differs for the UUID; failed with calls `dumpxml,define` before the fix) and `TestStoppedRenameWithRefusedSecureBootDoesNotRename` (failed with calls `domrename,dumpxml` before the check reorder), both in `internal/vmhost/firmware_gate_test.go`. `go test -race -count=1 ./internal/vmhost/ -run TestStoppedRename` ok. Job `go` in both `phase1` workflows.
- Avoid: fakes that accept what real libvirt rejects; testing rename and firmware changes only as separate edits; running a native mutation before validation that can still refuse the operation. Open limit: a stopped edit that fails after `domrename` landed is refused on resend with "VM name already exists".

## BUG-0221 — An interrupted VM delete could never finish or be retried

- Date: 2026-09-16. Status: fixed; regressions added; not verified on a host. Related: BUG-0196.
- Symptom: if the agent died after journaling `undefining`, or during the disk wipe, re-sending the same delete operation returned `needs_attention` every time ("disk file still present after an interrupted wipe" / "VM domain is still defined after an interrupted delete"), and a new delete was refused as "still unresolved". The VM definition and disks stayed.
- Root cause: `reconcile` only observed an interrupted `vm-delete` (`deleteObserved`); it never re-ran undefine or the wipe. `deleteVM` refused new deletes while that entry was unresolved, and `promote` does not cover `vm-delete`.
- Fix: `reconcile` resumes through `resumeDelete`/`finishDelete`: a still-defined domain must be shut off, undefine re-runs with the same flags, a requested wipe re-runs over the journaled paths (missing files ignored), and the entry succeeds. A new delete for the same VM settles the interrupted one only when both requests make the same `delete_disks` choice, then reports "VM is already deleted on this host: an earlier interrupted delete has now finished". Review of the first version found that a new retain delete wiped disks using the earlier request's `delete_disks=true`; a differing choice is now refused and tells the operator to resend with the earlier value or resolve it on the host.
- Regression: `TestInterruptedWipeDeleteResumesOnResend`, `TestNewDeleteSettlesInterruptedDelete`, `TestInterruptedDeleteRefusesRunningDomain`, `TestNewRetainDeleteRefusesInterruptedWipeDelete` in `internal/vmhost/delete_resume_test.go`. All failed before their fixes (the last: disk wiped and "already deleted"); `go test -race -count=1 ./internal/vmhost/ -run 'TestInterrupted|TestNewDelete|TestNewRetain'` ok. Job `go` in both `phase1` workflows.
- Avoid: treating a leftover disk or domain after an interrupted destructive step as needing manual work when the journal records intent and exact targets; letting a later request skip an unresolved entry; finishing an interrupted delete with a destructive disk choice the latest request did not make. Open decision: background `promote` still does not resume interrupted deletes, and a mismatched choice is refused in both directions. Limit: the new-delete path is reachable through Controller only while the domain is still defined and observed shut off; after undefine (every interruption during the wipe) Controller refuses the new delete with "stop the VM before deleting it", so recovery relies on redelivery of the original still-dispatched operation. No test covers the Controller path. Open decision for the project owner: whether Controller should accept a delete for a VM the host reports as missing but journaled as an interrupted delete.

## BUG-0222 — Wiped disks of a deleted VM blocked storage detach forever

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: after deleting a VM with "Permanently delete disks", detaching its storage repository was still refused with "retained disk for VM <id>" for disks that no longer existed.
- Root cause: `storageDependencies` counted every succeeded entry's `Retained` paths. A wipe delete cleared only its own list and left earlier `vm-update` lists that `managedDeleteDiskPaths` had already wiped.
- Fix: `storageDependencies` ignores `Retained` paths of VMs with a succeeded `vm-delete` that set `DeleteDisks`. A retain delete still blocks detach.
- Regression: `TestWipedVMRetainedDisksAreNotStorageDependencies` (`internal/vmhost/delete_resume_test.go`); failed before ("wiped disks still block detach"), passes now; a review mutation removing the skip failed it. Job `go` in both `phase1` workflows.
- Avoid: skipping paths that `os.Stat` reports missing — an unmounted remote repository would then allow detach while disks exist.

## BUG-0223 — The bundled virtio-win ISO was seeded onto a detached storage registration

- Date: 2026-09-16. Status: fixed; regressions added; not verified on a host.
- Symptom: if the default Local storage had been detached and registered again before the first seed, the bundled virtio-win ISO was journaled under the detached storage ID and the Controller showed it as needing attention ("storage ... is not adopted on this host").
- Root cause: `SeedBundledMedia` scanned raw journals in operation-ID filename order and took the first succeeded registration with the default path, ignoring the generation and detach-tombstone logic in `storageProjection`. After BUG-0224 moved the seed off the worker, a detach during the long copy could also land between the seed's read and its save.
- Fix: the target comes from `storageProjection` (live registration at the default path, lowest ID first); nothing is seeded while only a detached registration exists. The copy and hash run unlocked, then the seed takes the managed engine's operations lock, re-resolves the target and any existing virtio-win entry through `bundledMediaTarget`, and journals only if nothing changed, otherwise removes the copy.
- Regression: `TestSeedBundledMediaUsesLiveDefaultStorageAfterDetach` (failed before: published to "old-storage"), `TestSeedBundledMediaSkipsDetachedOnlyDefaultStorage`, `TestSeedBundledMediaDiscardsCopyWhenStorageDetachedMeanwhile` (failed with the lock step removed) in `internal/vmhost/bundled_media_test.go`. `go test -race -count=1 ./internal/vmhost/ -run TestSeedBundledMedia` ok. Job `go` in both `phase1` workflows. The lowest-ID choice itself is untested.
- Avoid: scanning raw storage journals to find a repository; moving a journal mutator off the serialized worker without re-deciding under the operations lock.

## BUG-0224 — The first bundled media seed blocked Controller commands on the agent

- Date: 2026-09-16. Status: fixed; regression added; not verified on a host.
- Symptom: on a new host with `/var` on a different filesystem from `/usr`, or a slow disk, the first tick copied and hashed an ISO of about 877 MB while `vm-create` and storage commands waited, and shutdown waited on a copy that could not be cancelled.
- Root cause: `managedWorker` called `SeedBundledMedia` synchronously in its command select loop, and `publishBundledISO`'s copy never checked the context.
- Fix: `startBundledSeed` runs the seed in a background goroutine, one at a time (`atomic.Bool`); the cross-filesystem copy reads through a context-checking reader. Journaling is serialized with commands as described in BUG-0223.
- Regression: `TestBundledMediaSeedRunsOffWorkerOneAtATime` (`internal/agent/managed_test.go`); before the fix the seed could only run synchronously (build failure against the new entry point). `go test -race -count=1 ./internal/agent/ -run TestBundledMediaSeedRunsOffWorkerOneAtATime` ok. Job `go` in both `phase1` workflows. Outstanding: the context-checking copy path has no unit test (a cross-filesystem copy cannot be forced reliably), and `verifyISO` still hashes to completion after shutdown.
- Avoid: multi-hundred-MB file work inline in the managed worker's command loop.

## BUG-0225 — A UEFI-to-BIOS edit deleted the variable store without warning

- Date: 2026-09-16. Status: fixed; regressions added. Related: BUG-0214, BUG-0219.
- Symptom: changing Firmware from UEFI to BIOS in the VM editor saved without any warning, and Core permanently deleted the VM's NVRAM (boot entries and Secure Boot variables).
- Root cause: the Firmware select in `web/src/vm-edit.tsx` had no destructive-change handling; the loss only happens in Core's firmware removal path.
- Fix: `firmwareIssue(current, next, acknowledged)` joins the editor's issue gate. A UEFI-to-BIOS draft shows an inline warning and Save stays disabled until "I understand the UEFI variable store will be deleted" is ticked.
- Regression: `web/src/vm-edit.test.mjs` (failed before: `firmwareIssue is not a function`), run by `npm --prefix web run test`, job `web`; `tests/integration/vm_edit_browser.py` checks the warning, disabled Save and enabled Save after acknowledgement, job `controller-browser`. Both jobs run in GitHub and Gitea `phase1` workflows on push and pull request. The browser capture was inspected in a simulated run.
- Avoid: relying on help text alone for an irreversible change, or blocking a legitimate firmware change outright.

## BUG-0226 — A VM edit that removed or swapped a NIC changed the wrong interface

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: `PUT /api/v1/vms/{id}` that kept only the second of two automatic-MAC interfaces with an empty MAC was accepted, and Core detached the second interface instead of the first (live when running). A same-count request that removed one NIC and added another applied the kept NIC's settings to the removed device.
- Root cause: `NeedsStop` and the Controller's MAC freeze loop resolve an empty MAC from its position in the request (`DerivedMAC(id, index)`); once an interface is removed or reordered, that position names another device. The first guard only checked for a shorter NIC list, which review showed a remove-plus-add bypasses.
- Fix: `updateVM` checks each index. An empty MAC at an existing index is accepted only when no interface is removed and its network, model and MTU match the current NIC at that index; only NICs appended past the current count may omit a MAC. Otherwise 400 "when removing, reordering or changing a network interface, give every existing interface its MAC address".
- Regression: `TestUpdateVMRemovingInterfaceRequiresNamedMACs` (`internal/controller/update_test.go`) covering removal and the swap case; failed before each fix (202, want 400), passes now. Job `go` in both `phase1` workflows.
- Avoid: guarding on NIC count; guessing the caller's intent from network or model; relying on the bundled UI sending effective MACs. Open decision: freezing derived MACs at creation would remove position matching entirely.
- Correction (2026-09-17, BUG-0254): the per-index guard assumed an empty MAC at an existing index always names the current interface there. The bundled editor sends every existing interface's effective MAC but sends new interfaces with an empty MAC, so an editor save that removed one interface and added another was refused with 400, and an accepted new interface could be frozen to the removed interface's address. The guard now applies only to requests that name no current interface by MAC; see BUG-0254.

## BUG-0227 — Retrying a VM delete after changing the wipe choice returned 409

- Date: 2026-09-16. Status: fixed; regression added. Related: BUG-0218.
- Symptom: after an uncertain delete response, ticking "Permanently delete disks" and retrying sent the old `request_id` with a different payload, and the Controller returned 409 "request_id already used with another payload".
- Root cause: `VMDelete` kept one request id, cleared only on success, regardless of payload.
- Fix: the id is keyed by the wipe choice (`request.current[String(wipe)]`), matching the VM editor's per-payload ids; all ids clear after success.
- Regression: `web/src/managed.test.mjs` source assertions (failed before), `npm --prefix web run test`, job `web` in both `phase1` workflows. The check is a source pattern, not a rendered behaviour test.
- Avoid: resetting the id on every dialog open, which loses safe retry of the same payload.

## BUG-0228 — The create wizard preselected a driver disc on hosts that cannot attach one

- Date: 2026-09-16. Status: fixed (UI and Controller); regressions added. Related: BUG-0235.
- Symptom: on a host without `vm-media-drivers-drive-v1` that had a virtio-win ISO, a Windows VM draft showed a disabled Driver disc holding virtio-win that could not be cleared; the review page said it would attach and the older Core ignored it. Directly through the API, `createVM` and `updateVM` accepted any `drivers_media_id` (unknown IDs, media on another host, hosts without the capability) and queued work Core could only fail or apply without the drive.
- Root cause: `chooseHost` and the Guest OS change called `defaultDriversMedia` without checking `driversDriveReady`. `createVM` and `updateVM` in `internal/controller/managed.go` resolved `media_id` with `readyResource(iso-import)` but never checked `drivers_media_id` or `DriversDriveCapability`; the adoption pass had the same gap.
- Fix: UI — `nextDriversMedia()` returns empty when the host is not ready, and the submitted definition and review page drop `drivers_media_id` for such hosts. Controller — when `drivers_media_id` is set, both handlers return 409 "the selected host agent does not support a driver disc drive; upgrade Nex Core" (edit: "the VM's host agent …") without the capability, then resolve it like the install ISO (404 "the driver disc ISO is not registered and verified on the selected host and pool"). Both checks run after the replay check, so replays stay idempotent. Adoption marks a VM needs attention when either ISO is not adopted on the host.
- Regression: `web/src/vm-create.test.mjs` (failed before: `nextDriversMedia is not a function`), job `web`; `tests/integration/managed_vm_browser.py --simulated` expects the disabled, empty Driver disc, job `controller-browser`. `go test ./internal/controller/ -run TestVMDriversMediaIsValidatedLikeInstallMedia` failed before the Controller fix (got 202 want 409; with only the ID check disabled, 202 want 404) and passes now; `go test -race ./internal/controller/...` ok. It runs in job `go` (`go test -race ./...`). All three jobs are in GitHub and Gitea `phase1` workflows on push and pull request. The adoption-pass check has no dedicated test.
- Avoid: only disabling a select, which still submits and displays its value; relying on Core or the web form to reject an unverifiable reference instead of refusing it at the Controller API before queuing.

## BUG-0229 — Switching Guest OS away from Windows kept the automatic virtio-win disc

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: choosing Windows, then Linux, created a Linux VM with virtio-win in its second CD drive.
- Root cause: the Guest OS change only filled an empty Driver disc and never removed the value it had chosen itself.
- Fix: `nextDriversMedia()` clears the Driver disc when it still holds the previous guest OS's automatic choice; an operator's own choice is kept.
- Regression: `web/src/vm-create.test.mjs` (failed before), `npm --prefix web run test`, job `web` in both `phase1` workflows. The browser run cannot cover this: the simulated host lacks the drivers-drive capability.
- Avoid: clearing the Driver disc on every Guest OS change.

## BUG-0230 — A stopped VM lost its last 1-year usage point

- Date: 2026-09-16. Status: fixed; regressions added.
- Symptom: an hour after a VM stopped, the agent dropped it from memory together with the open day step's average, so `range=1y` showed that day as unknown without any restart.
- Root cause: flush eviction checked only time since the last sample, not whether a tier still held memory-only accumulators for an unfinished step. The first fix kept such objects in memory until the day boundary, and review found they still counted toward the 256-VM cap, so stopped VMs could refuse history to newly started ones.
- Fix: evict only when idle for over an hour and `ringFile.pending()` is false; `record()` counts toward the cap only objects sampled in the last hour and re-checks the cap before an idle object resumes.
- Regression: `TestIdleObjectIsKeptUntilItsDayStepIsFinished` (failed before: day point unknown, want 50) and the extended `TestHistoryCapsSeriesAndRecordedVMs` (failed before: "stopped VMs still in memory blocked a newly running VM") in `internal/agent/usage_history_test.go`; both pass. Job `go` in both `phase1` workflows.
- Avoid: treating eviction as free because slots are on disk; treating "loaded in memory" as "actively recording".

## BUG-0231 — A backwards clock step wiped stored usage history

- Date: 2026-09-16. Status: fixed; regression added; open decision recorded.
- Symptom: a clock step back of more than one step (for example a boot before NTP with a slow RTC) reset the 24h/30d/1y rings and rewrote the file; in the repro 180 known 24h points dropped to 1.
- Root cause: `ringFile.add` reset every tier when a sample's step was behind the stored step, and the next flush persisted the wipe.
- Fix: stored tiers ignore samples behind their current step until the clock reaches it again and log once per object that stored history is ahead of the clock; only the memory-only 5 s tier restarts.
- Regression: `TestRingKeepsStoredTiersWhenTheClockGoesBack` (`internal/agent/usage_history_test.go`), replacing `TestRingRestartsATierWhenTheClockGoesBack`, which locked in the wipe; failed before ("a backwards clock step scheduled a whole-file rewrite"), passes now. Job `go` in both `phase1` workflows.
- Avoid: assuming the current clock is right and restarting a durable tier. Open decision for the project owner: a clock that ran ahead and is corrected now pauses stored tiers for as long as it was ahead (every boot on an RTC that runs ahead); options are ordering the agent unit after time sync, gating on kernel sync status, or accepting the gap.

## BUG-0232 — The stats relay accepted short series and its range error omitted live

- Date: 2026-09-16. Status: fixed; regression added.
- Symptom: an invalid range returned "range must be one of 10m, 24h, 30d or 1y" although `live` is accepted, and a host reply with fewer values than the tier's points was relayed as valid.
- Root cause: the message was not updated when `live` was added, and `validHistory` only rejected `len(Values) > tier.Points`.
- Fix: the message reads "range must be live, 10m, 24h, 30d or 1y" and `validHistory` requires exactly `tier.Points` values, returning 502 otherwise. Current agents always send full-length series.
- Regression: `TestStatsRelay` (`internal/controller/stats_test.go`); failed before on both assertions, passes now. Job `go` in both `phase1` workflows.
- Avoid: padding short series in the UI, which invents unknown data instead of refusing a reply that breaks the contract.

## BUG-0233 — Stacked usage charts drew hidden series

- Date: 2026-09-16. Status: fixed; regressions added.
- Symptom: with Stacked on, hiding vCPU 0 left vCPU 1's band offset by vCPU 0's share; hiding a middle series left the band above it unfilled.
- Root cause: `plotValues` ignored the hidden set and the chart never recomputed data on legend toggle; bands were fixed adjacent pairs, and uPlot does not fill a band whose lower series is hidden.
- Fix: `plotValues(series, stacked, hidden)` skips hidden series in the sums; `stackBands()` pairs each visible series with the nearest visible same-unit series below it; `StatsChart` rebuilds bands and re-runs `setData` on visibility change, keeping zoom.
- Regression: `web/src/stats.test.mjs` (failed before: "hidden vCPU 0 no longer offsets vCPU 1", then `stackBands` not exported), job `web`; `tests/integration/managed_vm_browser.py --simulated` counts fill pixels with vCPU 1 hidden (failed before: single-fill pixels 130 to 65; passes 130/129 with 0 overlap), job `controller-browser`. Both jobs in GitHub and Gitea `phase1` workflows. Simulated agent only.
- Avoid: trusting a pure data unit test for chart rendering; re-weighting by visible count; forcing a chart rebuild that resets zoom. Open limit: on hosts with more than 64 cores the CPU stack covers only the first 64 per-core series.

## BUG-0234 — The virtio-win notice described a GPL-bearing ISO as BSD driver source

- Date: 2026-09-16. Status: fixed for the staged notice, manifest and licensing record; release-artifact gap and source retention outstanding.
- Symptom: the staged virtio-win `NOTICE` and `docs/LICENSING.md` described the bundled virtio-win 0.1.302 ISO as BSD-3-Clause driver source, and its `manifest.tsv` had a single row. The ISO also ships GPL QEMU guest agent MSIs, SPICE agent and QXL driver MSIs inside the guest tools bundle, LGPL libraries and an MS-RL installer runtime, with no corresponding source retained.
- Root cause: the notice was written from the upstream driver repository's license, not from an inventory of the media.
- Fix: inventoried the checksum-verified ISO; `installer/build.sh` writes a NOTICE listing each component and license, and `manifest.tsv` gains license and `source_retained` columns with one row per embedded component; `docs/LICENSING.md` records the missing corresponding source, and that the compliance manifest and `SOURCE-DELIVERY.txt` do not yet cover the ISO, as outstanding before external distribution.
- Regression: `tests.test_distribution_packaging.VirtioWinNoticeTests` (failed before: 2 subtests, then 1 for the manifest); `python3 -m unittest tests.test_distribution_packaging.VirtioWinNoticeTests` OK. Runs through `unittest discover`, job `contracts`, in both `phase1` workflows. Outstanding: `scripts/build-source-bundle.py` still omits the ISO and its notice and claims complete source; retain source or stop shipping those components (project owner decision).
- Avoid: describing bundled media by its headline project's license without listing the media; treating a src.rpm or upstream link as corresponding source without inspecting it.

## BUG-0235 — Console driver disc insert and eject always failed with 400

- Date: 2026-09-16. Status: fixed; regression added. Not in the published 0.2.9 release (commits 4fb8570 and 76a28b6 lack the Controller `drive` field); listed under 0.3.0. Related: BUG-0228.
- Symptom: in the VM console, every insert or eject on the **Driver disc** drive failed with 400 "invalid JSON request"; the install drive kept working.
- Root cause: `changeVMMedia` in `web/src/main.tsx` sends `{"drive":"drivers"}` to `POST /api/v1/vms/{id}/media`, but the Controller's `mediaChangeRequest` (`internal/controller/vmmedia.go`) had no `drive` field and `decodeLimit` rejects unknown fields. Core already accepted `VMMediaRequest.Drive` and advertised `vm-media-drivers-drive-v1`; only the Controller contract was missing it.
- Fix: `mediaChangeRequest` gains an optional `drive`, validated as empty, `install` or `drivers` (400 "drive must be install or drivers"); `drivers` requires the VM's host to report `vm-media-drivers-drive-v1` (409 "the VM's host agent does not support a driver disc drive; upgrade Nex Core"). A non-empty drive joins the replay payload (an omitted drive is not stored, so older replays still match) and is passed to Core as `VMMediaRequest.Drive`.
- Regression: `go test ./internal/controller/ -run TestVMMediaDriversDrive -count=1 -v` failed before the fix (`vmmedia_test.go:308 invalid drive: {"error":"invalid JSON request"}`) and passes now (`--- PASS: TestVMMediaDriversDrive`); `go test -race ./internal/controller/...` ok. Job `go` (`go test -race ./...`) in both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` on push and pull request. No browser or lab run exercised the UI against a real host.
- Avoid: a UI that sends a field the Controller contract never accepted, with no contract test across UI and Controller; a UI unit test that only asserts the request body it builds proves nothing about acceptance.
- Follow-up (review, same day): with drives addressable, an insert could put the ISO already in the VM's other drive into this one; Core journals it and the Controller mirror then fails `ValidateVMSpec` ("the driver disc must differ from the install media") on later edits and marks the VM needs attention on re-adoption. `mediaVM` now refuses that insert with 400 using the VM's current `vm-create` mirror, and its 404 names the drive ("the driver disc ISO …" or "the install ISO …" on the VM's host and pool). `TestVMMediaDriversDrive` covers both directions and the unknown driver disc; with the guard disabled it failed (`got 202 want 400`). Hiding the other drive's ISO in the console list remains open.
- Core half (review follow-up, 2026-09-17): `changeMedia` in `internal/vmhost/managed.go` did not refuse that insert itself, so an older, third-party or stale Controller could still make Core journal a definition `ValidateVMSpec` rejects. It now compares the request with the VM's own journaled definition (`managedVM`) before any libvirt call and fails the operation with "that ISO is already in the install drive; …" or "that ISO is already in the driver disc drive; …"; re-inserting into the same drive stays allowed. Regression: the extended `TestManagedMediaDriversDrive` (`internal/vmhost/firmware_test.go`) failed before the fix (`firmware_test.go:1161: install insert of the driver disc ISO must be refused: {… Status:succeeded …}`) and passes now with `go test ./internal/vmhost/ -run TestManagedMediaDriversDrive -count=1`; `go test -race ./internal/vmhost/... ./internal/agent/...` ok. Job `go` (`go test -race ./...`) in both `phase1` workflows.

## BUG-0236 — A queued storage row stayed on screen after its registration became healthy

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, the storage table kept a stale "queued" row next to the healthy registration after the queued registration succeeded, until the page was reloaded.
- Root cause: the Controller returns queued, dispatched and failed registrations with `id ""`, and `web/src/storage.tsx` (and the network and ISO tables in `web/src/managed.tsx`) keyed rows by `id`. Duplicate empty keys made React reconcile the wrong row.
- Fix: rows use `key={s.id || \`op:${s.operation_id}\`}` in the storage, network and ISO tables.
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/storage_repositories_browser.py --evidence <dir>` pending-row case failed against the pre-fix build (5 rows where 3 were expected) and passes after; `npm --prefix web run test` (`storage.test.mjs`, `managed.test.mjs` key asserts) passes. CI: step "Verify storage repository browser workflows with simulated host" in job `controller-browser`, and job `web`, in both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml`. Simulated host only.
- Avoid: keying list rows by an ID that pending records do not have yet.

## BUG-0237 — A VM page opened after a create turned into "Object not found" when the create failed

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, an over-capacity create went to the new VM's page; once the create task failed, the next reload showed "Object not found" with no reason or link to the task.
- Root cause: the VM listing deliberately omits failed creates (no VM exists), but `create()` in `web/src/managed.tsx` dropped the `operation` from the 202 response and navigated to `#/vms/<vm_id>`, a route that only resolves from the listing.
- Fix: `create()` navigates to `#/vms/<vm_id>/<operation_id>`; `web/src/main.tsx` renders a "Creation <status>" panel with the task error and a **Task history** link when the VM is absent and the third segment is a `vm-create` operation. A VM route without the operation still shows Object not found.
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated --evidence <dir>` failed on the previous build (region "Creation task" not found) and passes after (13 PASS); `managed.test.mjs` source assert. CI: step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, and job `web`, in both `phase1` workflows. Simulated host only.
- Avoid: sending the operator to a route that only resolves from a listing that excludes the failure.

## BUG-0238 — The VM Activity tab showed pool and host rows but not the VM's own actions

- Date: 2026-09-17. Status: fixed; regressions added. Audit rows written before the fix keep their old resource IDs.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, the VM Activity tab omitted console.open, vm-create, vm-power, vm-media and vm-update rows and listed pool, site and host rows instead.
- Root cause: two causes. The Activity filter in `web/src/main.tsx` kept rows for the site, pool and pool hosts but never checked the VM ID. And `saveResourceStatus` in `internal/controller/managed.go` audited the managed_resources key, which for vm-power, vm-media, vm-update and vm-delete is an operation or row ID, not the VM ID.
- Fix: `saveResourceStatus` audits the name argument (the VM ID at every caller) for `vm-*` kinds other than `vm-create`; the Activity tab keeps only `resource_id === vm.id` when a VM is selected and shows VM names as targets.
- Regression: `go test ./internal/controller/ -run TestVMOperationAuditsNameTheVM` failed before (4 actions audited non-VM IDs) and passes after (rerun 2026-09-17: ok); `managed_vm_browser.py --simulated` with only the filter reverted gave 2 rows instead of 3 and passes after. CI: job `go` (`go test -race ./...`) and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: scoping VM activity by host or pool IDs; a UI-only `vm.id` filter that cannot match operation-keyed audit rows; adding a new `Operation.resource_id` contract field when the caller already has the VM ID.

## BUG-0239 — Add host silently swallowed a 409 "already enrolled"

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, adding a host that still trusted another Controller showed no error in three attempts.
- Root cause: `action()` in `web/src/main.tsx` wrote request errors into the inventory reload error state, which every successful 10 s poll cleared, and showed it outside the open form with a misleading Retry. A failed update apply used the same channel.
- Fix: a separate `actionError` state, set and cleared by `action()`, `open()`, Close, route changes and update apply, rendered with `role=alert` inside the open form panel (or as a page alert without Retry); `api()` keeps `host_id` so the 409 message names the host and the Re-enroll route.
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/site_selector_browser.py --evidence <dir>` (probe 200, add 409 with host_id, 12 s wait with an observed poll; and a failed update apply) failed on the previous build and passes after (9 PASS). The script previously never reached the workspace in CI because it lacked the signed-in init script; that is fixed too. CI: step "Verify searchable site selection" in job `controller-browser` in both `phase1` workflows.
- Avoid: routing operator request errors through the inventory reload error channel.

## BUG-0240 — Power confirmation buttons lost their danger and primary styling

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, **Confirm force stop** and **Confirm shut down** rendered as flat text identical to Cancel.
- Root cause: the power dialog sits inside `.toolbar`; `.toolbar button` in `web/src/style.css` has the same specificity as `button.danger`/`button.primary` and comes later, so it won.
- Fix: the toolbar rules (base, hover, disabled, disabled icon, mobile) apply only to `.toolbar-group > button` and `.toolbar > button`.
- Regression: `managed_vm_browser.py --simulated` computed-style check failed with the old CSS (`'UI-5 danger confirmation keeps its border', 'rgba(0, 0, 0, 0)'`) and passes after. CI: step "Verify managed VM browser workflows with simulated host" in job `controller-browser` in both `phase1` workflows.
- Avoid: descendant selectors on containers that host dialogs.

## BUG-0241 — The stale VM warning always said "Last reported state: unknown"

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, a VM on a stale host always showed "Last reported state: unknown" although the host had reported its state.
- Root cause: the VM listing deliberately reports `unknown` for a VM on a host that is not fresh, and the warning printed that listing field.
- Fix: the warning reads the VM's state and `observed_at` from the host inventory in `GET /api/v1/hosts`, which is still returned while the host is stale, and falls back to unknown.
- Regression: `npm --prefix web run test` (`managed.test.mjs` expects "Last reported state: running") failed before and passes after; `managed_vm_browser.py --simulated` stale case expects the host's last state. CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: changing the listing's `unknown` state, which correctly gates actions.

## BUG-0242 — The host-local VM table clipped Open console at 360 px

- Date: 2026-09-17. Status: fixed; regression added; CI coverage conditional.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, the host-local login page at 360 px cut off the fourth Console column, so Open console was reachable only by an unmarked sideways scroll.
- Root cause: four columns inside `.table-scroll{overflow:auto}`; the Local control cell's stacked buttons or long blocked reason pushed Console past the card edge.
- Fix: Open console (or "Console: start the VM first") moved into the Local control cell and the Console column was removed (`web/src/local.tsx`, `web/src/local.css`).
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py --blocked-only` new 360x800 case failed before (button right edge 400.5 px vs table edge 323 px) and passes after. CI: step "Verify local host browser authentication and preflight blockers" in job `host` in both `phase1` workflows, but that step runs only when the runner capability check passes; on a runner without it the regression is skipped, so CI coverage there is outstanding.
- Avoid: shrinking mobile padding or letter-spacing only, which still overflows with a long blocked reason.

## BUG-0243 — Console clipboard and resize showed ready with no guest OS running

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, a VM in firmware, in GRUB or with no OS reported clipboard "ready" and display resize available.
- Root cause: `observeGuest` in `internal/vmhost/guest.go` treated libvirt's `state='connected'` on the qemu-vdagent channel as proof of a running guest vdagent; that port reads connected even while the guest-agent channel on the same virtio-serial bus is disconnected.
- Fix: after the channel loop, report ready only when the vdagent channel is connected and the guest agent is not `disconnected`; resize follows. Tradeoff: a guest with spice-vdagent but no qemu-guest-agent now reads waiting.
- Regression: `go test -race ./internal/vmhost/ -run TestObserveGuest`, case "vdagent port connected without a guest OS", failed before (ready/true) and passes after (rerun 2026-09-17: ok). CI: job `go` (`go test -race ./...`) in both `phase1` workflows. Follow-up done 2026-09-17: the console waiting label now reads "Waiting for guest tools (qemu-guest-agent and spice-vdagent) in the guest", the resize reason names both agents, and `docs/guide/guest-tools.md`, `docs/guide/vm-console.md` and the `docs/API.md` guest observation paragraph state the stricter condition and tradeoff. `npm --prefix web run test` (`managed.test.mjs` label assert) failed with the old label and passes now (ok); CI: job `web` in both `phase1` workflows.
- Avoid: relying on the vdagent port state alone, or on the order of channels in the definition.

## BUG-0244 — Healthy managed checks showed "Last success Not observed"

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, Host Health showed managed-journal and managed-vms as healthy with "Last success Not observed".
- Root cause: Core built both checks as healthy without setting the optional `LastSuccess`.
- Fix: `managedJournalCheck` and `Applied` in `internal/vmhost/managed.go` set `LastSuccess` to the observation time when healthy; failed and unknown checks keep nil.
- Regression: `go test -race ./internal/vmhost/ -run 'TestAppliedSurvivesCorruptJournalEntry|TestAppliedFlagsVMsMissingFromLibvirt'` failed before (LastSuccess nil) and passes after (rerun 2026-09-17: ok). CI: job `go` in both `phase1` workflows.
- Avoid: filling `last_success` in the Controller, which passes checks through; the host owns the observation.

## BUG-0245 — Deleting a VM from its page showed no "Delete queued" notice

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate lab run on 0.2.10~gate1, deleting from the VM page returned to the host page with no confirmation notice.
- Root cause: `VMDelete.submit` in `web/src/managed.tsx` set the notice before changing the hash, and the hashchange handler in `web/src/main.tsx` clears notices.
- Fix: call `onNotice` after `await reload()`, so the navigation runs first.
- Regression: `managed_vm_browser.py --simulated` injected-delete case failed with the old call order ("Delete queued for" not visible on the host page) and passes after; `managed.test.mjs` source-order assert. CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: setting page notices before a navigation that resets them.

## BUG-0246 — Every nex-controller package from 0.1.6~alpha1 to 0.2.9 shipped a Controller that cannot start

- Date: 2026-09-17. Status: fixed; package smoke-start gate added 2026-09-18; hosted execution outstanding.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, the `nex-controller` deb binary exited at start with "Binary was compiled with CGO_ENABLED=0, go-sqlite3 requires cgo to work. This is a stub". `go version -m` on the packaged binaries reports `CGO_ENABLED=0` for 0.1.6~alpha1, 0.2.2, 0.2.5, 0.2.6, 0.2.7, 0.2.9 and 0.2.10~gate3, so every Controller package from 0.1.6~alpha1 through 0.2.9 is affected. A Controller installed from the package, including one upgraded through apt, gets a binary that cannot open its database, and the service keeps restarting; recovery needs a shell. Appliances built from the image keep their own working binary.
- Root cause: `packaging/controller/Dockerfile` installed no C toolchain (`gcc`, `libc6-dev`) and neither `packaging/controller/build-inside.sh` nor `appliance/build-inside.sh` set `CGO_ENABLED`, so `go build` silently fell back to `CGO_ENABLED=0` and linked the go-sqlite3 stub.
- Fix: the controller builder image installs `gcc` and `libc6-dev`, and both build scripts `export CGO_ENABLED=1` before `go build`, so a builder without a compiler fails the build instead of producing the stub.
- Regression: `python3 -m unittest tests.test_distribution_packaging.ControllerCgoBuildTests` failed before the fix (1 failure, 2 errors) and passes after; the full module ran 23 tests OK. Artifact check: a throwaway `0.2.10~cgocheck` package built with `packaging/controller/build.py` reports `CGO_ENABLED=1` in `go version -m`, and its binary started on 127.0.0.1 with an empty data directory, created `controller.db` and answered HTTPS 200. CI: step "Check contracts and contributor tools" (`unittest discover -s tests -p 'test_*.py'`) in job `contracts` in both `phase1` workflows.
- Follow-up (2026-09-18): both release workflows now extract and smoke-start the exact Controller package in the Debian 13 builder before creating/uploading a release. The gate requires a fresh SQLite database, packaged UI/source and an authenticated session. `python3 tests/integration/controller_package_smoke.py` passed locally: a real CGO-enabled Controller in a disposable package passed and a real CGO-disabled one failed with the original SQLite stub diagnostic. Both `phase1` workflows run this regression on pushes and pull requests; the later [exact 0.3.2 artifact lab](qa/2026-09-18-release-artifact-lab.md) passed real-package startup and installed service restart/crash/reinstall/reboot checks. Green hosted execution for the final source remains outstanding. See [package-gate evidence](qa/2026-09-18-controller-package-smoke.md). The [Phase 2 close](qa/2026-09-17-phase2-gate/CLOSED.md) records the owner decision to supersede the broken historical packages with 0.3.0+; this change does not remove published artifacts.
- Avoid: relying on Go's implicit cgo default in a builder image with no C compiler; it builds without error and fails only at runtime.

## BUG-0247 — A resumed interrupted delete briefly published needs_attention while it was still running

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, after `nex-core-local` was killed during a wipe delete, the journal entry read needs_attention after restart and then settled succeeded about 0.3 s later.
- Root cause: `reconcile` in `internal/vmhost/managed.go` set `Result.Status` to needs_attention in memory before resuming, and saves made part-way by the resume helpers (`finishDelete` at stage `wiping`, `startCreated`, `finishFirmwareRemoval`, `finishSecureBoot*`) wrote that provisional status to disk.
- Fix: `reconcile` keeps the result pending during the resume; only a result still unsettled at the end (or on an early error return) becomes needs_attention, keeping its reason or the retained-operation message. A resumed create records succeeded after its start step.
- Regression: `go test -race -count=1 -run TestResumedDeleteMidwaySaveStaysPending ./internal/vmhost/` failed before the fix (mid-way save read needs_attention) and passes after; the full `./internal/vmhost/... ./internal/agent/...` race run passed. CI: job `go` (`go test -race ./...`) in both `phase1` workflows.
- Avoid: setting a pessimistic final status before the side effects run; every save made part-way publishes it. Related: BUG-0221.

## BUG-0248 — A queued create showed full VM details with Edit and Delete

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, for about 1–2 s after submit a queued create showed the full VM details view with Edit and Delete instead of the "Creation queued" panel.
- Root cause: `GET /api/v1/vms` lists creates that have not failed, and `web/src/main.tsx` showed the Creation panel only when no VM row existed; `editBlocker` in `web/src/vm-edit.tsx` ignored the create task status (the server still refused the edit).
- Fix: `main.tsx` also shows the Creation panel while `vm.status` is not succeeded and the route's operation segment equals `vm.operation_id`; `editBlocker` returns "Wait for the creation task to finish." while queued or dispatched and "The creation task did not succeed; check it in Task history." for failed or needs_attention.
- Regression: `node web/src/vm-edit.test.mjs` failed with the fix reverted and passes after; `.cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated` queued-create route case failed with the `main.tsx` hunk reverted ("Creation queued" not found) and passes after (16 PASS lines). CI: job `web` (`npm --prefix web run test`) and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: treating the presence of a VM row as proof that its create succeeded.

## BUG-0249 — The "Start queued" notice stayed after its task succeeded

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, the "Start queued" notice stayed on the VM page long after the task succeeded, including across tab changes.
- Root cause: the notice in `web/src/main.tsx` had no task identity and was cleared only on hash change or by `action()`; tab changes keep the hash.
- Fix: power notices carry the operation ID from the 202 response (`VMPower` in `web/src/managed.tsx`, console Start, context menu and its dialog); an effect clears the notice once that operation leaves queued/dispatched, and detail-tab clicks clear it.
- Regression: `managed_vm_browser.py --simulated` notice case failed with the effect and tab clear reverted ("Locator expected to have count 0") and passes after. The `managed.tsx` source assertion in `managed.test.mjs` was not shown failing on its own. CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: page notices without a task identity that nothing expires.

## BUG-0250 — The Guest tools badge said "stale" on a fresh VM

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, the Guest tools badge read stale while the VM and host were fresh (during a guest reboot, and steadily on a VM with no OS).
- Root cause: `web/src/managed.tsx` mapped the guest agent's `disconnected`/`unresponsive` states onto freshness words.
- Fix: the badge keeps its colour class but shows the agent's own state, for example "disconnected".
- Regression: `node web/src/managed.test.mjs` ("guest agent badge must not say stale") failed with the fix reverted and passes after; `managed_vm_browser.py --simulated` disconnected-agent case failed against a bundle with the old badge (showed "stale") and passes after. CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: using freshness words for anything but report age.

## BUG-0251 — A replacement Controller started the old Controller appliance without confirmation

- Date: 2026-09-17. Status: fixed in the UI; regression added; Core-side refusal is an owner decision.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, a replacement Controller offered an enabled Start for the excluded old appliance, and Start was sent with no warning, which would run two Controllers.
- Root cause: Start has no confirm text, and the power dialog in `web/src/managed.tsx` opened only when an item had one; console Start and the context menu in `web/src/main.tsx` also sent Start directly. Nothing refuses it server-side.
- Fix: `powerConfirmText(item, vm)` gives Start on a protected VM the two-Controllers warning, and the toolbar, console and context-menu Start paths all open the confirmation dialog, which now opens modal, shows its own errors and reuses the retry-safe `request_id`.
- Regression: `managed_vm_browser.py --simulated` covers toolbar, context-menu and console Start and asserts no POST before Confirm start; it failed on context-menu Start with the `main.tsx` hunks reverted and passes after. The refused-Start case (error inside the dialog, same `request_id` on retry) failed against the previous bundle. The `managed.tsx` half in `node web/src/managed.test.mjs` was not shown failing on its own. CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: gating confirmation on per-action confirm text alone; protected VMs need it for Start too.

## BUG-0252 — The delete dialog's disk checkbox rendered far from its label

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, the delete dialog's wipe checkbox rendered far from its label.
- Root cause: the global `input { width: 100% }` rule in `web/src/style.css` stretched the checkbox inside the flex label.
- Fix: `input:where([type="checkbox"], [type="radio"]) { width: auto; }` placed after the global input width rule.
- Regression: `node web/src/managed.test.mjs` ("checkbox width rule must follow the input width rule") failed with the fix reverted and passes after; `managed_vm_browser.py --simulated` checks the checkbox is at most 24 px wide and within 16 px of its label, and failed with the rule reverted (`252.84375, 11`). CI: job `web` and step "Verify managed VM browser workflows with simulated host" in job `controller-browser`, in both `phase1` workflows.
- Avoid: placing the rule before the input width rule, where equal specificity makes it lose; and measuring only the gap, which passed without the fix.

## BUG-0253 — VMs on a stale host showed the running icon in the tree

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: in the Phase 2 gate3 lab run on 0.2.10~gate3, while host-1 was stale the Resources tree showed its shut-off VMs with the running icon.
- Root cause: `web/src/main.tsx` applied `vm-off` only to a fresh shut-off VM; a VM on a stale host reports state `unknown` and fell back to the default icon.
- Fix: tree VM links get class `vm-unknown` and the title "State unknown: host not fresh" when freshness is not fresh; `web/src/style.css` greys that icon without muting the text.
- Regression: `managed_vm_browser.py --simulated` stale case (class, title and grayscale filter) failed with the hunk reverted and passes after. CI: step "Verify managed VM browser workflows with simulated host" in job `controller-browser` in both `phase1` workflows.
- Avoid: inferring a running state from the absence of a fresh shut-off report.

## BUG-0254 — A VM edit that removed one interface and added another was refused or reused the removed MAC

- Date: 2026-09-17. Status: fixed; regression added; walked on a real host in the Phase 2 gate5 run (0.2.10~gate5, 2026-09-17 UTC, disposable nested-KVM lab): [gate report](qa/2026-09-17-phase2-gate/checks.md) row 11 checks (a) remove on a never-edited stopped VM keeps the survivor's MAC, (b) remove plus add of a different model gives a MAC distinct from both originals, and (c) remove plus add on a running VM is accepted with Controller and host definitions in agreement, all confirmed; (d) single-interface replacement was not checked (known limitation, open defect 5). The fix holds within one save only: reuse of a MAC removed in an earlier save is part of open major defect 1 ([owner triage](qa/2026-09-17-phase2-gate/checks.md#owner-triage-open-defect-1)), not this entry.
- Symptom: the final code review of the Phase 2 gate4 build (0.2.10~gate4) reported that the BUG-0226 guard in `updateVM` refuses the editor's NIC removal or change on a never-edited VM. Checking it showed removal or change alone works, because `GET /api/v1/vms` fills every interface's effective MAC and the editor sends it back. The real failures: an editor save that removed an interface and added a new one (which the editor sends with an empty MAC) whose settings differ from the current interface at that index was refused with 400 "when removing, reordering or changing a network interface, give every existing interface its MAC address"; when they matched it was accepted, but the new interface was frozen to the first derived MAC not in the request, often the MAC of the interface just removed. On a running VM the Controller ran its stop-first check before freezing, so it judged a different definition from the one Core checks.
- Root cause: BUG-0226 assumed that an empty MAC at an existing index names the current interface at that index, i.e. that the client sends no MACs at all. The bundled editor sends existing interfaces with their MACs and new ones without, and no test used the editor's real payload. The freeze loop also left current MACs out of the taken set and ran after `NeedsStop`.
- Fix: in `internal/controller/managed.go` `updateVM`, a request that names any current interface by its effective MAC (`protocol.NICMAC`) treats every empty-MAC interface as new and skips the positional guard; a request that names none keeps the 400 for removal, reorder or change, and an unchanged unnamed interface keeps the current MAC at its position. Automatic MACs are frozen before `NeedsStop`, skipping every requested and current effective MAC.
- Regression: `go test -race -run TestUpdateVM ./internal/controller/` passes (2026-09-17). `TestUpdateVMRemovePlusAddGetsFreshMAC` failed on a scratch copy with the fix reverted (reconstructed pre-fix guard): with the VM running, 409 "stop the VM first to change network interface" (the new interface took the removed d0); with it stopped, "new interface must get a fresh MAC". The 400 on a differing new interface was seen by the fixing session before its fix, not rerun here. `TestUpdateVMEditorRoundTripOnNeverEditedVM` (editor payload from the VM list; remove first interface, change first model) and `TestUpdateVMRemovingInterfaceRequiresNamedMACs` (MAC-less API client still gets 400) pass. `npm --prefix web run test` ("vm-edit firmware and interface check ok") passes. `tests/integration/vm_edit_browser.py` (simulated host) passes its never-edited step: remove interface 1 and add an rtl8139 interface in one save; revision 1 keeps the second MAC and gives the new interface a distinct MAC. CI: job `go` (`go test -race ./...`), job `web` (`npm --prefix web run test`) and step "Verify VM editing browser workflow with simulated host" (`vm_edit_browser.py`) in job `controller-browser`, in both `phase1` workflows.
- Avoid: a guard that assumes a client payload shape without a test that sends the real client's payload; reasoning about what the UI sends from `vmDefinition`/`createVM` without following the `GET /api/v1/vms` serializer; checking stop requirements on a definition other than the one dispatched.

## BUG-0255 — Realtime stats showed CPU and memory for a stopped VM

- Date: 2026-09-17. Status: fixed; regression added; verified on a disposable nested-KVM host.
- Symptom: in the usage-history lab run (uncommitted working tree), realtime (`range=live`) for a force-stopped VM returned HTTP 200 with 7 known points (`cpu.total` 0.5/0/0 %, `memory.assigned` 1 GiB) instead of all nulls, so the Realtime drawer drew small CPU rates and assigned memory for a VM that was not running.
- Root cause: on libvirt 11.3, `virsh domstats --raw --nowait --cpu-total --balloon <uuid>` still returns `cpu.time`, `cpu.user`, `cpu.system` and `balloon.*` for a shut-off domain, and a never-started domain returned the same increasing `cpu.time`, so the counter is not the domain's own. `liveViews.sample` in `internal/agent/usage_history_live.go` treated any block with `cpu.time` as a running domain. Stored history was unaffected because its sampler uses `--list-running`.
- Fix: the realtime `domstats` call adds `--state`; only `state.state=1` (running) is sampled. Shut-off, paused or state-less answers record nulls and reset the counters, so the first rate after a restart is unknown.
- Regression: `TestLiveSampleTreatsOnlyARunningStateAsRunning` (`internal/agent/usage_history_live_test.go`) uses the stopped-VM output captured on the lab host as its fixture; it failed before the fix (`sampled as running = true`) and passes after it, with shut-off, paused and running cases. `go test -race -count=1 ./internal/agent/` passes (2026-09-17). The final `tests/integration/usage_history_lab.py` run (2026-09-17) returned all nulls for the stopped VM while libvirt still reported the stale counters ([QA record](qa/2026-09-16-usage-history.md), L5). CI: job `go` (`go test -race ./...`) in both `phase1` workflows.
- Avoid: treating the presence of libvirt counters as proof a domain is running; ask for and check the domain state.

## BUG-0256 — An interface added to a running VM reused the MAC of an interface the guest had not released

- Date: 2026-09-17. Status: fixed; regression added; **lab-verified on published 0.3.1** (2026-09-17 disposable nested-KVM fresh install): remove+add then add-only on an OS-less running VM allocated `:02`/`:03` instead of reusing still-live `:00`; `virsh domiflist` had four distinct MACs and no duplicate. Evidence under `.cache/phase2-gate6-evidence-*-retry1/phase2-focused-probes.json`. Cross-VM manual-MAC refusal remains open.
- Symptom: on gate5 (0.2.10~gate5, disposable nested-KVM lab), a running VM at its firmware had an interface removed; a later add-only save froze the new interface to the removed interface's MAC and hot-plugged it. `virsh domiflist` showed that MAC twice on `br0` while the task reported succeeded and the Controller showed one interface fewer than libvirt.
- Root cause: `virsh detach-device --persistent` drops the interface from the persistent definition at once, but the live device stays until the guest acknowledges the unplug, which firmware, installer menus and guests without hotplug never do. Core's `applyLive` (`internal/vmhost/managed.go`) read only `dumpxml --inactive` and attached every wanted MAC missing from it; `validateManagedDefinition` also checks only the inactive XML, so the operation succeeded. The Controller's `updateVM` freeze loop (`internal/controller/managed.go`) picked the lowest `protocol.DerivedMAC(id, k)` not in the request or current definition, so a MAC removed in an earlier save was handed out again.
- Fix: `update` (`internal/vmhost/managed.go`), in its validating stage next to the dedicated-memory refusal and before any disk allocation or device operation, reads the persistent and live domains of a running guest and fails with "network interface MAC <mac> is still attached to the running VM; wait for the guest to release it or stop the VM, then save again" when a requested interface's MAC is live but absent from the persistent definition. Because no allocating or attaching stage was saved, `Execute` records `failed` (not `needs_attention`), background promotion leaves it alone, and the previous revision stays current. An independent review rejected a first version that ran the check inside `applyLive`: that runs after the journal stage becomes `attaching`, so the refusal became `needs_attention` after new disks were already allocated. `updateVM` draws new automatic MACs from the first derived index above the highest one the current definition uses (request MACs do not raise it), skipping taken addresses, and falls back to the lowest free index when none above is free. Known limits: the Controller has no live interface data, so after the highest-index interface is removed, or once index 255 is in use (for example a manual MAC equal to the last derived address), the next automatic MAC can be a recently removed one; Core refuses it while it is live. Not done: extending the create/update cross-VM MAC scans to other running domains' live XML (a manual MAC still live on another VM is not refused).
- Regression: `TestManagedUpdateRefusesMACStillLive` (`internal/vmhost/managed_update_test.go`) drives `Execute` with a fake virsh whose live XML holds a MAC the inactive XML lacks. With the check disabled it fails ("Status:needs_attention", after an attach); with the fix it asserts `failed`, the exact error text, no `attach-device`, `detach-device`, `change-media` or `autostart` call, and no `allocating` or `attaching` stage or domain saved. After release, the retry passes validation and attaches exactly once; it then stops at the final disk observation with "storage is not registered on this host", because a unit test cannot register a storage repository (root-owned mount, `libvirt-qemu` probe), so a fully `succeeded` retry is not proven here and needs the lab rerun. `TestUpdateVMAddAfterRunningRemovalSkipsRemovedMAC` (`internal/controller/update_test.go`, running VM `[:00, :01]`, save 1 removes `:00`, save 2 adds) failed before the fix ("new interface recycled a removed MAC") and passes after it (new MAC `:02`). `TestUpdateVMAutomaticMACHighestIndexFromCurrentDefinition` (current `[:01]`, request adds manual `:ff` and an automatic interface; then a further add) failed with the first version's request-MAC high-water ("a manual last derived address must not restart allocation at d0") and passes after it (`:02`, then fallback `:00`). `TestUpdateVMRemovePlusAddGetsFreshMAC`'s last step, which asserted the recycled `:00`, now expects `:03`. `go test -race ./internal/vmhost/... ./internal/controller/... ./internal/protocol/...` passes (2026-09-17); `go vet` on those packages is clean and `gofmt -l` on the changed Go files is empty. CI: job `go` (`go test -race ./...`) in both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml`.
- Avoid: judging whether a MAC is free only against the persistent definition (inactive XML, journal or Controller mirror); on a running domain a detached device can still be live. Do not track removed addresses in the Controller database: it cannot see the live domain and a Controller-only history would not survive replacement.

## BUG-0257 — A failed bundled-media import prevented a later seed retry

- Date: 2026-09-17. Status: fixed; regression added. The public 0.2.9 installer intentionally does not bundle virtio-win, so this affects only a future authorized bundled-media seed.
- Symptom: a failed `iso-import` journal entry with the bundled image's name or SHA-256 made later Core startup seeding return without retrying, leaving the image unavailable until manual journal intervention.
- Root cause: `bundledMediaTarget` treated any matching import journal as proof the image existed, without checking its result status.
- Fix: suppress a seed only for a matching `succeeded` import; failed imports are retried through the existing Core-owned `iso-import` journal and inventory-adoption flow. No API shape, guide workflow or durable record changed.
- Regression: `TestSeedBundledMediaRetriesFailedImport` starts with a failed matching import and now requires a later succeeded import. The focused test against the staged baseline, compiled through an isolated temporary Go overlay, failed exit 1 with `failed import blocked bundled media seeding`; it passes after the fix. `go test -race -count=1 ./internal/vmhost` passed in 12.430 s after the fix. CI: job `go` runs `go test -race ./...` in both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml`.
- Avoid: treating a journal entry's matching identity as completed work; a retry-safe seed must distinguish terminal success from a failed attempt.

## BUG-0258 — Website publish ran recursive chmod as root on a caller-supplied artifact

- Date: 2026-09-17. Status: fixed; regression added.
- Symptom: `nex-website-publish`, which a publishing account may run through sudo, recursively removed execute bits from every regular file in its supplied artifact before validation. A traversable root-owned artifact could therefore be changed by the wrapper.
- Root cause: the wrapper repaired artifact modes with `strip_execute_bits` instead of leaving validation to the release tool.
- Fix: remove that root-side mutation. `website-release.py` continues to reject executable files before its root-owned staging copy; the wrapper now fails and leaves the supplied artifact unchanged.
- Regression: `test_executable_artifact_is_rejected_without_mode_mutation` failed before the fix because the executable bit was cleared and downstream validation passed; it now requires wrapper failure and the unchanged executable bit. `python3 -m unittest discover -s tests -p 'test_nex_website_publish.py' -v` (5) and `python3 -m unittest discover -s tests -p 'test_website_release.py' -v` (7) pass. CI: both phase1 workflows run `python3 -m unittest discover -s tests -p 'test_*.py' -v`.
- Avoid: mutating a caller-controlled path as root to make invalid input acceptable; reject it before privileged staging.

## BUG-0259 — Stats treated stale hosts and unsupported Core versions as generic retry errors

- Date: 2026-09-17. Status: fixed; regression added; browser/component verification remains outstanding.
- Symptom: both an older Core without usage history and a stale host return existing HTTP 409 responses, but the Stats tab showed either as a generic alert with Retry. Operators were not told to upgrade an unsupported Core, and stale data was not labelled stale.
- Root cause: the UI retained only an undifferentiated error string.
- Fix: classify the existing 409 response text: an unsupported host shows **Upgrade Nex Core to see usage history** and clears old charts; a stale host preserves loaded history with an out-of-date warning and Retry. Other errors keep the existing alert behavior.
- Regression: `statsFailure` assertions in `web/src/stats.test.mjs` distinguish capability, stale and ordinary failures; `npm test`, typecheck and build pass. The proposed Stats module was not in HEAD or the index, so no repository baseline failure was executed or claimed. No browser or component runner was available; retained-chart and recovery behavior need browser/component coverage.
- Avoid: collapsing different safe operator actions behind one retry-only message when the existing error contract supplies the distinction.

## BUG-0260 — A same-named imported ISO suppressed the pinned bundled-media seed

- Date: 2026-09-17. Status: fixed; regression added. Public 0.2.9 remains unaffected because bundled virtio-win seeding is blocked.
- Symptom: a succeeded unrelated ISO named `virtio-win.iso` stopped a future authorized bundled-media seed, even when its digest did not match the pinned bundle.
- Root cause: Core considered either a matching name or SHA-256 to identify the bundle.
- Fix: only a succeeded `iso-import` with the pinned SHA-256 suppresses seeding. The existing Core-owned journal, media file and inventory adoption path are unchanged.
- Regression: `TestSeedBundledMediaDoesNotTreatSameNamedISOAsPinnedBundle` failed before the fix with `same-named ISO blocked the pinned bundled media seed`, then passed. `go test -race ./internal/vmhost -run 'TestSeedBundledMedia' -count=1` and `go test -race -count=1 ./internal/vmhost` (12.640 s) pass. CI: both phase1 workflows run `go test -race ./...`.
- Avoid: use the pinned digest, not a mutable display filename, as the identity of an exact bundled artifact.

## BUG-0261 — Installer CI asserted obsolete wizard stage indices

- Date: 2026-09-17. Status: fixed; regression corrected.
- Symptom: both reviewed Installer runs aborted in `installer-shell.py` on the language-stage assertion.
- Root cause: BUG-0209 changed the production rail to Welcome/Host/Network/Disk/Review/Install, but the test retained the older numeric indices.
- Fix: check the actual stage names and index bounds using the production stage array; preserve unknown/rescue checks. Production installer behavior is unchanged.
- Regression: original `python3 tests/integration/installer-shell.py` failed locally; corrected check and all installer source regressions in the workflow passed. An isolated mutation mapping language to Welcome failed the corrected check. Both `.github/workflows/installer.yml` and `.gitea/workflows/installer.yml` run it on pushes and pull requests. Independent review passed. See [workflow evidence](qa/ci/2026-09-17-workflows.md).
- Avoid: reverting correct operator behavior to satisfy stale positional test expectations.

## BUG-0262 — Browser fixtures and assertions lagged the delivered Controller UI

- Date: 2026-09-17. Status: fixed; existing browser regressions corrected.
- Symptom: clustered-storage browser checks timed out on navigation; executing previously blocked downstream checks also found stale console and enrollment assertions.
- Root cause: cluster and console fixtures supplied a session API response without the `nex-signed-in` browser marker required after BUG-0208. Cluster storage selected a control by position. Console media status and enrollment assertions used superseded wording.
- Fix: establish the same returning-browser marker used by the other simulated fixtures, select Repository type by its label, and assert the actual media/enrollment messages. Independent review also caught a loading check that could capture the login page after a transient initial render: it now proves fresh login makes no session probe, then holds an actual returning-session request before asserting loading. Its full browser rerun and independent capture inspection passed. No authentication or production UI behavior is changed.
- Regression: cluster and console failures were reproduced before their corrections; the phase1 browser walkthrough exposed the stale enrollment assertions. Commands and final outcomes are recorded in [workflow evidence](qa/ci/2026-09-17-workflows.md). Both phase1 workflows run these browser checks on pushes and pull requests.
- Avoid: authenticated API mocks without matching browser state, positional selectors for labeled controls, or dropping operation/error assertions to bypass stale text.

## BUG-0263 — Handoff regression used an operation deadline as its service lifetime

- Date: 2026-09-17. Status: fixed; existing regression corrected.
- Symptom: `TestHandoffAdoptsObservedApplianceAndResumesOffline` failed with `context deadline exceeded` under hosted race-test load.
- Root cause: a 15-second parent context covered readiness, enrollment, adoption and later recovery, although production passes a service-lifetime context and bounds individual operations separately.
- Fix: use a cancellable test-lifetime context and always cancel/release the embedded agent on test exit. Production readiness, adoption and HTTP deadlines are unchanged.
- Regression: a temporary Go overlay delaying the initial readiness reply by 16 seconds reproduced the original failure (16.71 s); the corrected test passed the same delayed case (23.420 s). `go test -race ./internal/localcore -count=3` passed (28.160 s), and independent focused race review passed. The delay overlay is a simulation, not a permanent CI sleep. Both phase1 workflows run the existing test through `go test -race ./...` on pushes and pull requests.
- Avoid: imposing one arbitrary short deadline on an entire service/recovery lifecycle, or merely replacing it with another arbitrary lifetime deadline.

## BUG-0264 — Website hosting fixture aborted on transient HTTP startup timeout

- Date: 2026-09-17. Status: fixed; regressions added.
- Symptom: the contracts lane failed in nginx fixture `setUpClass` while reading the first HTTP response.
- Root cause: readiness retried only refused TCP connections; HTTP timeout escaped setup, where normal class teardown does not run. Failed HTTP requests also left their connection unclosed. The historical log does not establish why that runner's nginx response was delayed.
- Fix: retry refused/timed-out requests within a monotonic readiness deadline, detect process exit, register process/temp cleanup before readiness, and close connections in `finally`.
- Regression: injected startup-timeout and connection-close checks failed before the fix; afterward the website suite passed 15 tests. Six focused hosting tests passed independent review, including a permanent-timeout deadline and cleanup check. Full Python discovery passed 307 tests. Both phase1 workflows run `python3 -m unittest discover -s tests -p 'test_*.py' -v` on pushes and pull requests.
- Avoid: equating a listening TCP socket with ready HTTP service, or leaking fixture resources when class setup fails.

## BUG-0265 — Identity rejection regression imposed a one-second startup benchmark

- Date: 2026-09-17. Status: fixed; existing regression corrected.
- Symptom: a Go lane later cancelled by a newer push had already failed `TestManagedWorkerRejectsDifferentHostWithoutEffects` with `worker blocked`.
- Root cause: the test's one-second guard included goroutine scheduling and native NIC observation, neither of which has a one-second security contract. Its fatal path cancelled but did not join the worker before temporary-directory cleanup. CI's exact scheduling/native-call delay was not captured.
- Fix: retain a bounded ten-second deadlock guard and always cancel/join the worker, preserving identity rejection and absence-of-journal-effects assertions. No production command handling changes.
- Regression: 100 original local race repetitions passed; a temporary 1.5-second worker-start delay reproduced the original one-second failure and passed after correction. Removed that simulated delay; 100 final race repetitions passed. Rejection, slow-command reporting/cancellation and nonblocking seed tests passed together with `-race -count=10` (14.837 s). Both phase1 workflows run `go test -race ./...` on pushes and pull requests.
- Avoid: making security assertions incidental startup benchmarks or letting fatal assertions race fixture cleanup.

## BUG-0266 — Persistence failure regression cancelled before observing the injected error

- Date: 2026-09-17. Status: fixed; existing regression corrected.
- Symptom: `TestRunStopsOnJournalPersistenceFailure` expected a fatal persistence error but received `nil` in hosted CI.
- Root cause: its one-second context included native agent initialization and the first TLS report that injects the disk failure; cancellation normally returns `nil`. The log does not establish the exact hosted delay or a production persistence defect.
- Fix: allow a bounded thirty-second test watchdog and count HTTP reports atomically; still require a typed `persistenceError` and exactly one report.
- Regression: temporary 1.5-second server latency reproduced the original nil-error failure (1.51 s) and passed after correction (2.579 s). Removed the temporary delay; 100 focused race repetitions passed, as did twenty repetitions with journal replay and worker identity checks. Independent review ran ten focused repetitions successfully. Both phase1 workflows run the regression on pushes and pull requests via `go test -race -timeout 20m ./...`.
- Avoid: cancelling the test before its fault injection can be observed, or accepting cancellation as proof that persistence errors stop the agent.

## BUG-0267 — Race suite exceeded Go's default package timeout on the shared runner

- Date: 2026-09-17. Status: workflow budget corrected; hosted rerun outstanding.
- Symptom: the Controller package panicked after 600.246 seconds, while the currently active test had run only three seconds.
- Root cause: the suite exhausted its cumulative ten-minute package budget. The sampled active work was full-strength PBKDF2 login under race instrumentation, not a blocked lock; the trace does not measure cumulative hashing cost.
- Fix: both phase1 workflows explicitly allow twenty minutes per package while retaining race detection, the full package suite and unchanged production hashing.
- Regression: `test_go_race_suite_has_an_explicit_bounded_ci_budget` failed against both original workflow copies and passes with the corrected command. Both forge contracts lanes discover this check on pushes and pull requests. Actual full-suite results and hosted limits are in [follow-up evidence](qa/ci/2026-09-17-runner-load.md).
- Avoid: weakening authentication hashing, dropping race detection or skipping test packages to satisfy an unsuitable CI runtime budget.

## BUG-0268 — Acceptance fixture used a subsecond libvirt deadline and fixed browser sleeps

- Date: 2026-09-17. Status: fixed; regression added to existing acceptance fixture.
- Symptom: hosted process/browser acceptance failed waiting for healthy libvirt after Controller and agent restarts.
- Root cause: the fixture forced a 150 ms probe deadline instead of production's three seconds, making a slow successful subprocess report unknown; related browser checks slept 700 ms rather than waiting for observed state. Hosted logs omitted the final inventory, so exact runner timing remains unproven.
- Fix: use the production probe deadline, retain a 250 ms successful simulated virsh reply to catch the original configuration, and wait for API-observed failed/unknown/healthy probe states before checking the UI.
- Regression: the delayed healthy shim fails with the original 150 ms override and passes after correction. Full `phase1.py --browser` passed, including restart recovery, durable refresh, stalled-probe isolation and re-enrollment; independent review inspected logs and local captures. Both phase1 workflows execute the fixture on pushes and pull requests. Simulated virsh is not native libvirt evidence; captures with local inventory remain unpublished.
- Avoid: imposing a test-only probe deadline too short for normal fork/exec under CI load, or substituting a longer fixed UI sleep for an observed-state wait.

## BUG-0269 — Media capacity regression raced changing filesystem free space

- Date: 2026-09-17. Status: fixed; existing regression corrected.
- Symptom: the local full race-suite rerun failed `TestManagedWorkflowScopeRetryAndMedia`: expected 507 for an oversized declared upload, received 400 for its empty invalid ISO body.
- Root cause: the test declared only one byte more than its own free-space snapshot; the handler's later snapshot could see more space after an unrelated file was removed and proceed to body validation.
- Fix: declare `1 << 62` bytes, matching the existing sibling body-limit regression, while preserving the insufficient-space skip and exact 507 assertion. Production capacity and ISO-body validation are unchanged.
- Regression: a temporary 32 MiB staging file created before the first snapshot and removed before the handler reproduced the original 400, then passed with the fixed declared length. Removed that temporary injection; focused and full-suite results are recorded in [follow-up evidence](qa/ci/2026-09-17-runner-load.md). Both phase1 workflows run the test on pushes and pull requests via `go test -race -timeout 20m ./...`.
- Avoid: assuming filesystem free space stays constant between a test and the operation it exercises.

## BUG-0270 — Workflow cleanup was committed without complete documentation assessment

- Date: 2026-09-17. Status: fixed locally; hosted rerun outstanding.
- Symptom: the Documentation workflow rejected four installer/website workflow paths as missing impact assessment.
- Root cause: the committed timing-fix record covered its own changes, while concurrent removal of the standalone installer builder-image step and external website homepage check was committed without its separate record.
- Fix: extend the existing assessment to all four workflow copies and accurately record removed CI coverage. This changes no workflow behavior, service API, operator procedure or durable state; it does not fix or explain the previously observed external HTTP 401.
- Regression: the documentation checker reproduced the exact failed commit-range error before correction. Its existing `test_missing_impact_record_fails` regression runs in both Documentation workflows on pushes and pull requests. After correction, the checker passes the failed commit's changed/added path sets against the corrected tree and the current worktree gate; no Git history changes or hosted rerun are claimed.
- Avoid: committing a combined diff with assessments covering only one contributor's subset; run the full worktree documentation gate immediately before committing.

## BUG-0271 — A binary-only Controller appliance could not install its first update

- Date: 2026-09-17. Status: fixed in the working tree; no rebuilt or published package is claimed.
- Symptom: an appliance running its bundled 0.2.9 Controller binary reported Installed 0.2.9 and Available 0.3.0, but **Update** ended with `no newer nex-controller available`.
- Root cause: appliance build copies the Controller binary without installing the Debian package. The root helper used `apt-get install --only-upgrade nex-controller`; apt exited successfully without installing an uninstalled package, leaving the before/after `dpkg-query` version empty.
- Fix: the helper uses plain `apt-get install nex-controller`, which both installs the first package and upgrades later package-managed appliances. The `GET`/`POST /api/v1/updates` contract is unchanged.
- Regression: `ControllerUpdateHelperRecoveryTests.test_binary_only_appliance_receives_its_first_controller_package` failed before the fix and passes after, simulating an empty initial `dpkg-query` and a successful first install. `python3 -m unittest tests/test_distribution_packaging.py -v` passed 24 tests. Both phase1 workflows run full Python unittest discovery on pushes and pull requests.
- Avoid: using `--only-upgrade` for a component that can start from a copied binary rather than an installed package.

## BUG-0272 — CPU selection could not preserve a native pool feature baseline

- Date: 2026-09-17. Status: implemented in the working tree; no live deployment or original installer-root-cause claim.
- Symptom: CPU selection offered only a host's model catalogue or host-dependent modes, without a fixed, explicitly verified common pool definition.
- Root cause: named-model usability was treated as the entire CPU contract; explicit native feature overrides and a pool-wide baseline had no persisted representation.
- Fix: Core obtains the native common model/features from all current members, compares the result on every member, and exposes the observation. Selecting it stores the model/features in the existing Core VM definition. Manual dynamically discovered models remain selectable; no OS-specific policy exists.
- Regression: protocol/controller/agent/vmhost CPU tests and simulated browser acceptance pass; see [scoped evidence](qa/2026-09-17-cpu-baseline.md) for commands, native-runtime results and limits. Both phase1 workflow copies run these existing Go/UI/browser suites on pushes and pull requests. Native hardware and hosted CI acceptance remain separate.
- Avoid: hardcoded guest-release rules, intersecting CPU model names instead of effective features, silently excluding unavailable members, or rewriting existing CPUs when membership changes.

## BUG-0273 — A guest that put itself to sleep locked up its VM and blocked every VM on its host

- Date: 2026-09-17. Status: fixed in the working tree; lab wake and browser walkthrough outstanding.
- Symptom: a Windows 11 VM on SMB storage became unresponsive in Controller: every power
  request and the console were refused, and a second VM on the same host could not start. Its
  power and media tasks failed with `managed VM runtime observation is unavailable`.
- Root cause: the guest entered ACPI S3 (libvirt `pmsuspended`). Nex handled only `running`
  and `shut off`: the power engine refused every action on any other state including force
  stop, the console required `running`, and the pool inventory observation errored on the
  state, so one sleeping guest failed the host's whole inventory publication and with it every
  admission on that host. Nothing prevented the guest from sleeping in the first place.
- Fix: every generated domain definition disables ACPI S3/S4 (`<pm>`), following Hyper-V's no-S3
  model and VMware's guidance to disable guest power management. A domain that still reports
  `pmsuspended` is observed as a running owner; `start` wakes it (`virsh dompmwakeup`, the
  libvirt equivalent of Proxmox's Resume), `shutdown`/`reboot` wake it first, force actions act
  directly; the master admits the wake as a new `wake` execution action with no permit or epoch
  change; console, edit and delete name Start as the way out; the UI shows "asleep" and offers
  Start. Raw observed state is passed through unchanged.
- Regression: `go test ./internal/vmhost -run 'TestManagedPowerWakesSleepingGuest|TestManagedDomainXMLDisablesGuestSleep|TestConsoleSocket'`
  and `go test ./internal/agent -run 'TestSharedVMAsleepIsAdmittedAsWake|TestPoolVMExecutionRetainsOwnerAcrossStopAndRejectsSecondHost'`
  fail on the previous engine (refused actions, inventory error) and pass now; `node web/src/managed.test.mjs`
  covers the toolbar. Full `./internal/vmhost`, `./internal/agent`, `./internal/controller`,
  `./internal/protocol` suites and the UI typecheck passed. Both phase1 workflow copies run
  these suites on pushes and pull requests. The originating host has not been woken with the
  fixed build.
- Avoid: treating libvirt's state set as `running`/`shut off` only, or letting one VM's
  unobservable state fail a host-wide inventory publication.

## BUG-0274 — Core shutdown abandoned a running update, and the page called it done early

- Date: 2026-09-18
- Symptom: `go test -race ./...` failed intermittently in CI with
  `TestLocalUpdatesAuthAndApply` ... `TempDir RemoveAll cleanup: unlinkat
  /tmp/TestLocalUpdatesAuthAndApply*/002/state: directory not empty`; repeated runs of the
  same package also failed with `update already in progress`.
- Root cause: two halves of the same mistake. `MergeProgress` repaired a stuck phase to
  `done` whenever apt already reported the new version, which is also true mid-apply because
  dpkg unpacks before the service restart, so a status read could report a finished update
  while the apply goroutine was still working. `Service.Close` never waited for that
  goroutine (it is not on `s.wg`), so the apply kept writing `progress.json` into the state
  directory after shutdown — racing the caller that was already removing it.
- Fix: `nexupdate.WaitApply` waits for the background apply, and `Service.Close` calls it
  beside the existing enrollment-agent wait; `MergeProgress` repairs a stuck phase only when
  this process is not the one applying.
- Regression: `go test -race ./internal/localcore -run TestCloseWaitsForTheBackgroundUpdate`
  fails without the wait ("Close returned while the apply was still writing to the state
  directory", with a detected race) and passes with it;
  `go test -race ./internal/nexupdate -run TestMergeProgressLeavesARunningApplyAlone` reports
  `done` without the guard and `installing` with it. Full `./internal/localcore` and
  `./internal/nexupdate` suites passed. Both phase1 workflow copies run these in the `go`
  lane on pushes and pull requests. No update was applied on a real host for this fix.
- Avoid: believing a status read over the goroutine that is still writing the status, or
  leaving a writer running past the Close that owns its directory.

## BUG-0275 — A dropped connection while attaching the ISO failed the whole release

- Date: 2026-09-18
- Symptom: the release workflow failed in "Create or replace the release" with
  `ssl.SSLEOFError: EOF occurred in violation of protocol` raised as
  `urllib.error.URLError` while posting the multi-gigabyte ISO, discarding a 37-minute build.
- Root cause: `scripts/gitea-release.py` sent each asset exactly once. A connection dropped
  mid-body had no retry, and a short body the server accepted would have published a
  truncated download.
- Fix: each asset is attached with up to three attempts; a retry discards whatever the failed
  attempt left registered under that name and resends from the start, a stored size that does
  not match the file is a failure, and a 4xx is not retried.
- Regression: `python3 -m unittest discover -s tests -p 'test_gitea_release.py'` — 6 tests
  passed; the dropped-upload test fails with `UPLOAD_ATTEMPTS = 1` (the previous behaviour)
  and passes with the retry. Both phase1 workflow copies run it in the contracts lane on
  pushes and pull requests. The fix has not yet been exercised against the real forge; the
  next release run is that evidence.
- Avoid: one-shot uploads of release-sized artifacts, or treating 201 as proof the server
  kept every byte.

## BUG-0276 — The browser lane read a busy runner as a dead Controller

- Date: 2026-09-18
- Symptom: `Capture Controller gallery with neutral demo inventory` failed with
  `AssertionError: controller startup; last transport error: <urlopen error [Errno 111]
  Connection refused>`, after which the Controller process did not stop within the harness's
  five seconds and had to be killed.
- Root cause: startup waits used `eventually`'s default 15-second window. Controller startup
  measured 0.10s on an idle machine, but the phase1 workflow runs six lanes on one host
  (including `go test -race ./...`), and the wait discarded the Controller's own log, so a
  slow start and a crashed start produced the same message.
- Fix: `Lab.started()` in `tests/integration/phase1.py` waits 60 seconds and prints the
  process log when it gives up; all eight Controller startup waits across the integration
  harnesses call it, not only the lane that failed.
- Regression: no automated check asserts a timeout, so regression coverage is outstanding;
  both paths were exercised by hand against the built Controller — a healthy start returned,
  and a Controller started without its source archive failed with
  `matching -source-archive required: stat dist/nex-source.tar.gz: no such file or directory`
  printed from its log instead of only "connection refused". The browser lanes in both
  workflow copies exercise the helper on every push and pull request.
- Avoid: sizing a CI wait to an idle machine, or discarding the log of the process being
  waited for.

## BUG-0277 — Agent timing checks measured the runner, not the behaviour

- Date: 2026-09-18
- Symptom: the `go` lane failed intermittently in `internal/agent` with
  `TestReportBoundsManagedLibvirtObservation`: `report waited for libvirt observation:
  2.361014647s` (and, in an earlier run, `blocking virsh was not invoked`), and
  `TestManagedSlowCommandKeepsReportingAndCancels`: `heartbeat blocked during command:
  0 reports`. The same package took 295s in the failing run against 128s in a passing one.
- Root cause: both checks asserted wall-clock numbers sized for an idle machine. The
  observation bound was 2s against a virsh that blocks for 30s, so any slow report failed even
  though it had not waited for libvirt; the slow-command check gave the whole run 5s to get a
  child process started and measured "cancelled promptly" from the start of the run rather
  than from the cancellation, and reported a command that never started as a blocked
  heartbeat.
- Fix: bound the observation at 10s, still far below the blocking virsh; give the slow-command
  run 20s to reach the command (it still ends on the third heartbeat seen during it), measure
  prompt cancellation from the cancellation, and fail separately when the command never
  started.
- Regression: `go test -race -run
  'TestManagedSlowCommandKeepsReportingAndCancels|TestReportBoundsManagedLibvirtObservation'
  -count=4 ./internal/agent` passed with all 16 cores saturated by busy loops, and `-count=3`
  passed idle; full `go test -race ./...` passed except `internal/vmhost`
  `TestRegisterDefaultsIsIdempotentAndRespectsExistingEntries`, which needs root for `runuser`
  and is green in CI. The failures are load-dependent, so no check reproduces the original
  timing on demand; the `go` lane in both workflow copies is the standing check.
- Avoid: asserting a wall-clock number that a busy build machine controls, or letting one
  assertion report two different failures.

## BUG-0278 — Default-resource test depended on the development host's libvirt directory

- Date: 2026-09-18
- Symptom: `TestRegisterDefaultsIsIdempotentAndRespectsExistingEntries` expected one
  network journal but found an additional storage attempt on workstations where
  `/var/lib/libvirt/images` exists; the native `runuser` check then failed without root.
- Root cause: the test assumed the default storage path did not exist rather than
  selecting an absent temporary path. The BUG-0277 note described the resulting
  permission error; running the test as root is not its actual requirement.
- Fix: point this test at an absent child of its temporary directory and restore the
  package default afterward. Production resource registration is unchanged.
- Regression: reproduced the original failure with the existing libvirt directory;
  `go test ./internal/vmhost -count=1` and focused placement/default-resource
  race checks passed after the repair. Both push/PR workflow copies run
  the test through `go test -race -timeout 20m ./...`.
- Avoid: depending on whether optional virtualization directories happen to exist on
  the machine running a unit test, or modifying real host directories to make it pass.

## BUG-0279 — Storage projection broke idempotent registration replay

- Date: 2026-09-18. Status: fixed in the working tree; native retry reproduced before the fix.
- Symptom: in the native standalone-Core lab, `POST /api/v1/storage` with the exact local
  registration payload and `request_id` `lab-local-0` returned 201 initially. After Core
  reported the completed repository, retrying that unchanged payload returned 409
  `request_id already used with another payload`.
- Root cause: `mirrorStorageProjection` rewrote an existing `managed_resources` row's
  `payload` with its synthetic adoption request and Core-resolved repository fields.
  `replayResource` correctly compares the saved input byte-for-byte, so the Controller
  subsequently compared the operator's original request with its own replacement payload.
- Fix: refresh only the Controller's disposable storage command projection, pool/host and
  name. The original request payload remains the idempotent replay record. Newly adopted
  repositories still receive their synthetic adoption payload because they have no original
  Controller request to replay.
- Regression: `TestStorageRepositoryIsCoreAllocatedAndAdopted` first failed with the exact
  409 after Core adoption, then passed after the fix by replaying the original request and
  requiring the same Core-allocated repository ID and operation. `go test ./internal/controller
  -run 'Storage' -count=1` passed. Both existing push/PR `phase1` workflows run the Go suite
  with `go test -race -timeout 20m ./...`.
- Avoid: updating a request payload while refreshing an observed resource mirror; payload is
  immutable replay input, while command is the current host-reported projection.

## BUG-0280 — VM startup could check a nested repository through its parent

- Date: 2026-09-18. Status: fixed in the working tree.
- Symptom: a managed VM disk beneath a nested local repository could be assigned to its
  broader parent registration at startup because repository selection stopped at the first
  matching path. Map iteration made that choice unstable, allowing the wrong repository's
  availability and identity check to decide whether the VM could start.
- Root cause: the startup guard used the first prefix match instead of resolving ownership by
  the longest registered storage root. It also needed to discard an equal-root ambiguity from
  a broader root once a more-specific root was found.
- Fix: select the longest matching root, reject equally specific roots with different IDs as
  ambiguous, and reset the ambiguity state whenever a longer root supersedes the prior choice.
  The disk remains required to be beneath that selected repository's `nex-vms/<vm-id>/` path.
- Regression: `TestStorageStartupGuardUsesMostSpecificRepository` covers parent and nested
  roots, an ambiguous parent superseded by an unambiguous nested root, equal-root refusal, the
  selected repository verification, foreign-VM refusal and unavailable nested storage. The
  focused guard regression and full vmhost test run passed locally. Both existing push/PR
  `phase1` workflows run the Go suite with `go test -race -timeout 20m ./...`.
- Avoid: using first-match filesystem-root ownership where nested repositories are valid, or
  retaining an ambiguity after a more-specific root has made it irrelevant.


## BUG-0281 — Failed release uploads exposed incomplete releases

- Date: 2026-09-18. Status: fixed in the PR; documentation assessment added locally.
- Symptom/root cause: the publisher created a public release before uploading its assets,
  so an exhausted retry or rejected upload left incomplete downloads visible.
- Fix: create a draft, publish only after all size checks pass, and delete the draft on
  handled upload failure. Replacement still removes the old release first; this is not
  an atomic rollback. A killed runner or failed cleanup can leave a draft.
- Regression: `python3 -m unittest discover -s tests -p 'test_gitea_release.py'` passed
  all six checks against a simulated HTTP forge. The updated checks fail three cases
  against the previous publisher, including rejected and truncated upload cleanup.
  Both providers' push/PR workflows discover this suite. Hosted publication remains untested.
- Avoid: making the release public before verifying all assets, or claiming a local forge
  simulation proves real release delivery.

## BUG-0282 — Storage browser navigation inherited a short control deadline

- Date: 2026-09-18. Status: fixed locally.
- Symptom: hosted clustered-storage acceptance timed out on its initial page load at 3s.
- Root cause: Playwright's default action timeout also sets navigation's deadline unless
  navigation is configured separately. The harness shortened both while intending quick
  control failures. The precise cause of hosted asset-loading delay was not established.
- Fix: keep 3s controls and explicitly restore the normal 30s navigation budget.
- Regression: `test_cluster_browser_timeouts.py` failed before and passed after the fix.
  A real Chromium experiment with a 4s delayed asset failed at 3.00s with the original
  setup and loaded in 4.02s with the fix. The full clustered-storage browser suite passed.
  Both providers' push/PR workflows run the unit regression and browser suite.
- Avoid: blind navigation retries or widening every control assertion.

## BUG-0283 — Freshness assertion expired before the next inventory poll

- Date: 2026-09-18. Status: fixed locally.
- Symptom: hosted enrollment browser acceptance saw a stale host on its initial visit
  and failed before the next refresh. The exact hosted agent-report delay is unknown.
- Root cause: the fixture permits only 2s before staleness, while the UI refreshes every
  10s and the assertion waited only 5s. One stale sample could therefore fail healthy recovery.
- Fix: allow that freshness assertion 15s; retain the fixture's staleness threshold and
  other deadlines. Inject one stale observation, then restore real API responses to prove
  automatic refresh recovery deterministically.
- Regression: the injected observation failed with the original 5s assertion and passed
  with 15s. Both providers' existing push/PR `phase1.py --browser` steps run this regression.
  The injection simulates stale inventory; it does not prove host scheduling under contention.
- Avoid: disabling stale-state checks or changing production freshness thresholds to pass CI.


## BUG-0284 — Native VM browser lab accessed simulator-only controls

- Date: 2026-09-18. Status: fixed in the PR; regression added locally.
- Symptom/root cause: placement fault scenarios accessed `config['control']` in native
  mode, whose configuration has no simulator control dictionary.
- Fix: run those injected capability/rejection scenarios only in simulated mode; keep
  common VM creation and UI checks in both modes.
- Regression: `test_managed_vm_browser_modes.py` passes against the corrected harness
  and rejects four unguarded accesses in the original. Both providers' existing push/PR
  unittest discovery runs it. This is a structural guard check; the full native lab
  was not rerun and native end-to-end acceptance remains outstanding.
- Avoid: invoking simulator fault controls in native execution or removing simulated
  rejection coverage to make the native harness work.

## BUG-0285 — Cancellation regression confused agent shutdown with worker latency

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: the Go race lane rejected a 3.23s shutdown against a three-second limit.
- Root cause: the assertion times all of `Run` teardown, including native probes, while
  claiming to measure only the worker. Its fake shell left a sleeping child holding output
  pipes; the two-second command could also finish naturally before the cancellation limit.
  The exact source of the hosted shutdown delay was not captured.
- Fix: use `exec sleep 30`, require three heartbeats without a terminal command result,
  and bound whole-agent teardown at ten seconds, below the uncancelled command's twenty-second
  query deadline. Production cancellation and deadlines are unchanged.
- Regression: `go test -race -timeout 20m ./internal/agent -count=1` passed;
  `go test -race -run '^TestManagedSlowCommandKeepsReportingAndCancels$' -count=50
  ./internal/agent` passed. A temporary mutation removing cancellation from the slow command
  failed at 19.81s after cancellation; it was restored before final checks. Both providers'
  existing push/PR Go suites run this regression. The full `go test -race -timeout 20m ./...`
  run also passed; independent review passed the final focused regression three times.
- Avoid: timing complete agent shutdown as an isolated worker benchmark, or letting a
  cancellation fixture finish naturally before its failure threshold.

## BUG-0286 — Storage browser input dispatch inherited a short action deadline

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: the clustered-storage browser lane timed out clicking Browse exports after
  actionability checks had passed, during input dispatch.
- Root cause: the fixture imposed a three-second action deadline on shared CI. The previous
  navigation-only correction did not cover clicks. The hosted renderer slowdown is unproven.
- Fix: restore Playwright's normal bounded thirty-second action/navigation defaults;
  retain five-second UI assertions and all discovery cancellation and safety checks.
- Regression: a one-shot simulated 3.5s renderer stall on pointerdown reproduced the original
  click timeout. With the fix, the complete `tests/integration/cluster_storage_browser.py`
  suite passed using the local Playwright environment and freshly built UI; the confirmation
  capture was visually inspected. `python3 -m unittest discover -s tests -p
  'test_cluster_browser_timeouts.py'` failed before and passed after the fix. Both providers'
  existing push/PR workflows run the unit check and browser scenario, including the stall.
- Limits: broader local Python discovery ran 314 tests but reported one failure and two
  errors because nginx is absent; the hosted contracts lane passed. Simulated API replies
  and input delays do not prove native storage or reproduce the actual runner slowdown.
- Avoid: using short action deadlines as functional assertions, retries that conceal failed
  input dispatch, or claiming the hosted root scheduling cause from a simulated delay.

## BUG-0287 — Repository browser retained a short inherited navigation deadline

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: the repository browser fixture failed its initial `page.goto` after 2500ms.
- Root cause: this sibling of the clustered-storage fixture still overrode the global
  Playwright action timeout, which navigation inherits. Earlier fixes covered only the
  clustered-storage fixture. The precise hosted asset-loading delay remains unknown.
- Fix: remove the last global short timeout override in the browser harnesses, restoring
  standard thirty-second action/navigation budgets while retaining five-second assertions.
- Regression: extend `test_cluster_browser_timeouts.py` to both storage fixtures.
  `python3 -m unittest discover -s tests -p 'test_cluster_browser_timeouts.py'` failed
  against the original repository fixture and passed after correction. A real Chromium
  experiment with a simulated 3.2s script response failed at 2.50s with the old setting
  and loaded at 3.21s with defaults. The full repository browser suite passed using
  `.cache/phase1-qa-venv/bin/python tests/integration/storage_repositories_browser.py
  --evidence .cache/storage-browser-timeout-fix`; its NFS picker capture was inspected.
  Both providers' existing push/PR workflows run the unit regression and browser suite.
- Limits: API replies and the delayed asset are simulations, not native storage or proof
  of the actual hosted slowdown. No production API, state or operator behavior changed.
- Avoid: correcting only one sibling fixture or coupling page-load deadlines to short
  interactive-action budgets.

## BUG-0288 — Escape and discovery replies left an empty modal blocking storage

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: the next Browse exports click timed out for 30 seconds because an empty
  open storage dialog intercepted input. A named-dialog hidden assertion missed it.
- Root cause: native Escape closed the dialog before its asynchronous close handler
  cancelled discovery; an intervening reply reopened it before the handler erased
  its content. An old close notification could also clear a newly reopened picker.
- Fix: abort discovery synchronously on cancel and ignore close notifications while
  the shared NFS/SMB dialog is open again.
- Regression: `tests/integration/storage_repositories_browser.py` now delivers discovery
  during Escape and an old close notification after reopening. The original built UI
  failed the first check; removing only the close guard failed the second. The fixed
  full browser suite passed 12 checks with no page errors. UI typecheck, tests and
  build passed. Both providers' existing push/PR foundation workflows run this script.
  Commands, inspected captures and simulation limits are recorded in
  [workflow evidence](qa/2026-09-18-storage-dialog-workflow.md).
- Avoid: increasing timeouts for a modal lifecycle race, checking only a dialog's
  accessible name, or letting a previous opening's close event clear current content.

## BUG-0289 — ISO staged redundant payloads and historical cached packages

- Date: 2026-09-18. Status: local packaging correction; full artifact checks recorded
  in [packaging verification](qa/2026-09-18-iso-packaging-efficiency.md).
- Symptom: the driver addition grew local media from roughly 2.06 GB to 3.54 GB.
- Root cause: the media carried an uncompressed complete driver ISO plus both the
  source RPM and its extracted payloads. APT staging copied every cached package,
  including versions unrelated to the current dependency resolution.
- Fix: gzip the complete driver ISO for transport, verify its restored bytes before
  use, retain the intact source RPM on media and the complete extracted release
  source tree separately, and stage packages from a fresh APT archive directory.
- Regression: `test_iso_payload_efficiency.py`, `test_source_bundle.py` and
  `test_distribution_packaging.py` cover transport/source/staging contracts;
  `installer/check.sh` checks both installer decoder paths. Both providers run the
  Python suites on push/PR; the installer workflows also run the payload regression
  explicitly. Actual command results, sizes and native-install limits are in QA.
- Avoid: removing driver architectures or required source to reduce size, accepting
  unrelated notices as source evidence, or copying a persistent download cache
  wholesale onto release media.

## BUG-0290 — Core package repeated the identical Nex source archive

- Date: 2026-09-18. Status: fixed locally.
- Symptom/root cause: independent Core and appliance copies stored the same roughly
  120 MiB archive twice, without requiring those two build inputs to match.
- Fix: require matching source hashes before starting the builder and preserve the
  Core source-serving path as a relative symlink to the appliance archive.
- Regression: `python3 -m unittest discover -s tests -p 'test_core_local_package.py'`
  passed 17 tests, including an actual package build/extract and differing-source
  refusal. An isolated original builder accepted the mismatched fixture (exit 0);
  the corrected builder rejected it before Docker (exit 2). Both providers' push/PR
  foundation workflows discover this suite. The appliance suite passed 12 tests.
- Avoid: removing source-serving paths, linking to files outside the installed
  package, or sharing archives without first proving the inputs identical.

## BUG-0291 — Storage browser check races SMB credential request dispatch

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: clustered storage browser CI reports `SMB credential handoff was not requested`
  immediately after the update form displays its disabled submitting button.
- Root cause: the browser harness equated React's busy/dialog rendering with the Python
  route callback having captured the outgoing request. These are separate events.
  The exact scheduling delay on the hosted runner is not established.
- Fix: a shared bounded wait pumps Playwright events until the deferred route is captured,
  before disconnecting the simulated Core or cancelling discovery. Apply it to SMB
  credentials, NFS export browsing and iSCSI target discovery. Product code is unchanged.
- Regression: inject a one-shot 1-second credential fetch dispatch delay in
  `tests/integration/cluster_storage_browser.py`. With that delay and the original
  immediate assertion, the full browser run reproduced the exact assertion failure.
  With the fix, `.cache/phase1-qa-venv/bin/python tests/integration/cluster_storage_browser.py`
  passed against freshly built UI (`npm --prefix web run build`), including the stale-Core
  rejection, unused-credential warning and absence of an SMB update after disconnection.
  `python3 -m unittest discover -s tests -p 'test_cluster_browser_timeouts.py'` passed.
  Both `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` already run this
  browser scenario on pushes and pull requests, including the new delay regression.
- Limits: simulated API replies and dispatch delay; no real SMB server, native storage,
  hosted CI rerun or publication exercised.
- Avoid: assuming a visible busy state proves network dispatch, sleeping for a fixed
  period before asserting, retrying the entire test, or weakening the stale-owner guard.

## BUG-0292 — Storage browser readiness used a shorter deadline than browser actions

- Date: 2026-09-18. Status: fixed locally; hosted rerun outstanding.
- Symptom: the hosted repository browser check failed waiting for `Open /exports`
  after reopening the NFS picker, while the final diagnostic snapshot already
  contained that button. The other foundation jobs passed; the browser gate failed.
- Root cause: actions/navigation allowed 30 seconds, but functional UI expectations
  still used five seconds. A loaded picker did not have a defined five-second
  performance requirement. Some request inspections also read the last Python
  capture before confirming the triggering request had arrived.
- Fix: give both storage browser suites a bounded 30-second expectation budget;
  bind root/refresh/up request assertions to Playwright request events. Reuse the
  deferred-route wait for both harnesses, and wait for fence results before reading
  their captured requests. Keep every cancellation, old-close, stale-owner and
  credential safety assertion; no product code changed.
- Regression: injecting a one-shot 5.5-second discovery response delivery delay at
  the failing reopen reproduced the original five-second locator failure. The fixed
  `storage_repositories_browser.py` completed all 12 scenario groups with no page
  errors; `cluster_storage_browser.py` also passed with its credential dispatch
  regression. The timeout policy unit test, documentation/privacy gates and diff
  check passed. Both providers already execute the browser scripts and unit suite
  on pushes and pull requests; no workflow edits are needed.
- Evidence: [commands, hosted observation and simulated-test limits](qa/2026-09-18-storage-ci-readiness.md).
- Avoid: retrying the whole suite, treating rendering as proof a request was captured,
  imposing an undocumented performance threshold on functional tests, or describing
  a simulated delay as proof of the hosted runner's exact scheduling cause.

## BUG-0293 — Healthy clustered browser fixture expired during the test

- Date: 2026-09-18. Status: fixed locally; hosted validation outstanding.
- Symptom: after the repository browser suite passed, the clustered suite's VM
  storage selector contained only `Select VM-ready storage` on the hosted runner.
- Root cause: its healthy member observation was stamped once at Python startup.
  By the VM-selection scenario it exceeded the real 30-second access freshness
  limit, so the UI correctly excluded it. The previous dispatch/readiness audit
  missed this separate wall-time dependency; increasing readiness waits cannot fix it.
- Fix: pair a deterministic observation with Playwright's fixed Date clock before
  navigation. Timers keep running for inventory polling and cancellation checks.
  Product freshness enforcement remains unchanged. Read the selector only after
  its expected option renders.
- Regression: ageing the original startup fixture by 31 seconds reproduced the
  exact placeholder-only assertion. The fixed full browser scenario passed, and
  now explicitly advances browser time by 31 seconds to verify exclusion before
  resetting the fixture clock and verifying availability again. Existing stale,
  errored, exclusive-storage and disconnected-host exclusions remain asserted.
  A second full run with an injected real 31-second delay before VM creation also
  passed; this verification-only sleep is not shipped in CI.
  `node web/src/storage.test.mjs` and the browser timeout unit check passed.
  Both providers' push/PR foundation workflows already run the browser and storage
  unit checks; no workflow edit is required.
- Evidence: [follow-up clock verification](qa/2026-09-18-storage-ci-readiness.md#follow-up-fixture-freshness).
- Avoid: refreshing fake observation timestamps on every read, extending the real
  freshness limit, placing observations in the future, or relying on fast machines
  to finish the test before its fixtures expire.

## BUG-0294 — Final release verification transfers huge files and closes its index pipe early

- Date: 2026-09-18. Status: corrected locally; hosted correction not run.
- Symptom/root cause: reachability used GET with a 60-second deadline on the entire
  source archive. Separately, `grep -q` stopped reading the package index as soon as
  the version matched; under `pipefail`, curl's resulting write failure reported a
  missing version even when it existed.
- Fix: use HEAD for reachability and consume the complete small Packages response
  with `grep -x` while discarding matching output. Keep non-200 and missing-version
  checks in both providers' workflows.
- Regression: `python3 -m unittest discover -s tests -p 'test_release_reachability.py'`
  executes both actual workflow shell blocks against a disposable HTTP server.
  Full-archive GET returns a simulated timeout response; a large matching index
  reproduced curl exit 23. Both failed before their fixes and pass after. Six cases
  cover success, HTTP 401 and missing version across both providers. Both push/PR
  foundation jobs discover this test automatically.
- Live evidence: the existing published 0.3.4 ISO/source/manifest and APT paths
  returned 200; both package names had version 0.3.4. The ISO checksum sidecar matched
  the manifest. Executing the corrected final workflow block against the documented
  public site passed without rebuilding or republishing. This did not hash the full
  remote payloads. The hosted failure was an earlier 401 at InRelease, not either
  newly reproduced bug; its configured URL/routing remains unresolved.
- API impact: none; release verification only. No product or pool state changes.
- Avoid: rebuilding published artifacts to test URL routing, ignoring auth failures,
  downloading gigabytes for a reachability check, or using an early-exit consumer
  in a pipe whose producer must complete successfully.

## BUG-0295 — Pool master refused VM creation on its own SMB/NFS repository

- Date: 2026-09-19. Status: corrected locally; not yet deployed to the affected lab host.
- Symptom: `vm-create` on a single-host pool with a Controller-registered SMB repository
  failed with `selected VM host returned an unregistered shared storage observation`,
  even when the VM's disks were on local storage.
- Root cause: Controller stamps its request state (`status: "queued"`, `operation_id`)
  into typed `storage-register` commands. Core copied the request into the durable
  applied record, and `storageProjection` echoed it. The master's own applied-storage
  list clears those fields, but the placement observation did not, so the master's
  shared-storage comparison saw status `queued` and refused. Members never hit it
  because replicated pool storage already strips the fields.
- Fix: `storageProjection` drops `OperationID`, `Status` and `Error`. Only succeeded
  journal entries are projected, so those fields are never an observation. The read-side
  fix also heals journals already written with `queued`.
- Regression: `go test ./internal/vmhost/ -run TestStorageProjectionDropsControllerRequestState`
  failed before (projection carried `Status:queued`) and passes after. `go test
  ./internal/vmhost/ ./internal/agent/ ./internal/controller/ ./internal/protocol/`
  passed. Both push/PR workflows run `go test -race ./...`.
- Live evidence: observed read-only through the lab Controller API (operation failed,
  one host, one SMB repository, no tombstones). The fix was not deployed or re-tested
  on that host.
- API impact: none; no request, response or journal format changes.
- Avoid: relaxing the master's shared-storage status check, or clearing the field only
  in new registrations, which leaves existing journals broken.


## BUG-0296 — Initial ZFS provisioning passed its ownership marker as a pool property

- Date: 2026-09-19; found during the unshipped provisioning implementation.
- Symptom: the disposable host API operation reached `needs_attention` with
  `property 'nex:repository' is not a valid pool or vdev property`; no pool was created.
- Root cause: the create command used `zpool create -o` for a ZFS user property and
  attempted to recover it through pool-property lookup. User properties belong to
  filesystem datasets.
- Fix: set `nex:repository` on the pool's root filesystem atomically through
  `zpool create -O`; recovery reads it with `zfs get` before adopting any native state.
- Regression: `go test ./internal/vmhost -run TestZFSProvisionCreatesCoreOwnedPoolOnce -count=1`
  passed. The test requires the exact `-O nex:repository=repo-1` argument and rejects
  the original `-o` form. Both GitHub and Gitea push/PR foundation workflows already
  run the vmhost tests through `go test -race -timeout 20m ./...`.
- Evidence: [native failure and rollout verification](qa/2026-09-19-zfs-provisioning.md).
  The initial native failure is reproduced; successful post-fix native provisioning
  and later recovery gates are recorded separately when executed.
- Avoid: treating a mocked subprocess success as proof that a native tool accepts
  an option, or moving the ownership marker to a separate post-create command that
  leaves an unmarked pool after interruption.

## BUG-0297 — ZFS recovery stopped after native pool creation

- Date: 2026-09-19; found during the unshipped provisioning implementation.
- Symptom: the disposable host created an ONLINE tagged pool, but a missing lab
  prerequisite failed registration and an identical Controller request only returned
  the stored attention result.
- Root cause: the normal worker dispatched pending commands, while the aggregate
  attention result had no Core-owned continuation for a completed native provision.
  The failed managed registration also retained its original failure.
- Fix: the Core worker resumes durable incomplete aggregates and retries registration
  only after the exact completed native receipt and live pool/dataset identities prove
  ownership. Failed-create intent alone never proves a healthy repository.
- Regression: `go test ./internal/agent ./internal/vmhost ./internal/controller -run 'TestResumePoolZFSOperations|TestZFSProvisionResumesPartitionedNVMePoolWithoutWipe|TestZFSProvisionRecoveryCompletesOriginalAttentionOperation|TestManagedWorkerBackgroundCompletion' -count=1`
  passes, covering recovery without Controller replay, refusal of an unproven failed
  create, and the partitioned-vdev, attention-completion and background-completion
  tests named below, which live in `internal/vmhost`, `internal/controller` and
  `internal/agent` respectively. Both GitHub and Gitea push/PR foundation workflows run
  these tests through `go test -race -timeout 20m ./...`.
- Native evidence: the retry retained the existing pool GUID and did not create a
  pool or managed registration for the earlier failed-create intent. Registration
  then correctly refused an unmounted dataset. After the lab mounted it and completed
  registration, a real reboot proved native import/mount with stable GUIDs; see the [rollout report](qa/2026-09-19-zfs-provisioning.md).
- Further fault injection interrupted an actual tagged pool before dataset creation.
  Recovery originally rejected OpenZFS partition vdevs for selected whole disks. It
  now resolves by-id paths and explicitly marked sysfs partitions to their parent
  disks, preserving whole NVMe namespaces, before completing only the missing dataset.
  `TestZFSProvisionResumesPartitionedNVMePoolWithoutWipe` exercises the full path and
  asserts neither erasure nor a second pool create occurs.
- Completed aggregate recovery now reconciles the original Core journal after a crash
  and republishes an acknowledged attention result under the same operation ID.
  Controller accepts that authoritative attention-to-success transition.
  `TestZFSProvisionRecoveryCompletesOriginalAttentionOperation` covers this report flow;
  native task readback remains part of the rollout verification.
- Background completion uses the heartbeat-owned journal rather than a shared worker
  map. `TestManagedWorkerBackgroundCompletionSendCancels` reaches the production send
  helper with a full channel; `TestManagedWorkerBackgroundCompletionPreservesForeground`
  verifies durable attribution, acknowledgement reset, duplicate suppression and the
  independent foreground completion. Both passed under `go test -race`.
- Avoid: resetting operation identity, selecting fresh disks to hide recovery failure,
  or equating a stored successful intent with current native access.

## BUG-0298 — Provisioned ZFS aggregates bypassed target-host storage checks

- Date: 2026-09-19; found during native and independent review of the unshipped slice.
- Symptom: a failed-create repository remained a successful Controller mirror after
  Core stopped reporting it; a host-scoped repository on a member could be mistaken
  for storage local to its canonical master.
- Root cause: adoption and readiness applied the new per-host access semantics only
  to pool scope, retained absent master rows, and allowed the ordinary same-host
  readiness shortcut. Media capability also advertised a member-targeting flow that
  the existing import API does not implement.
- Fix: all provisioned ZFS scopes use the master's record and fresh access for the
  exact target. A healthy master journal confirming absence marks a stale mirror unusable
  while retaining its adoption operation; a later Core registration restores the
  same mirror. Failed inventory grants no new access. New provisioned
  repositories advertise VM disks only; advanced existing datasets are unchanged.
- Regression: `go test ./internal/controller` passed, including
  `TestHostScopeZFSUsesMasterProjectionAndTargetAccess` and
  `TestMasterReportMarksStaleZFSProjectionUnusableUntilCoreReregisters`. Both GitHub and
  Gitea push/PR foundation workflows execute the package through the full Go race
  suite. Native post-fix observation is tracked separately in the
  [rollout report](qa/2026-09-19-zfs-provisioning.md).
- Avoid: deriving disk availability from the master row's host ID, treating the
  selected scope as state ownership, or erasing mirrors after a failed Core probe.

## BUG-0299 — ZFS lifecycle replay and later-host configuration could drift

- Date: 2026-09-19; found during review of the unshipped provisioning slice.
- Symptom: an interrupted rename could fail when replayed after its generation was
  persisted; a configure-host request could replace canonical name/scope or an
  existing host's selected devices.
- Root cause: rename incremented the generation before transport completion without
  recognizing the already-applied exact name; configure-host admission checked only
  repository existence before adding another operation record.
- Fix: permit an already-applied rename only at the immediately following generation
  with the exact persisted name; preserve canonical name/scope and refuse replacement
  device intent when configuring additional hosts.
- Regression: `go test ./internal/agent -run 'TestPoolZFSLifecycle|TestPoolZFSJournal' -count=1`
  passed. The checks cover rename replay, changed-source/stale-update rejection and
  configure-host input identity. Both GitHub and Gitea push/PR foundation workflows
  execute these tests in the full Go race suite. These are simulated journal/peer
  checks; native lifecycle evidence remains separate.
- Avoid: treating a new configure-host operation as a rename, or incrementing state
  without defining safe replay after a lost completion.

## BUG-0300 — ZFS wizard could retain another provisioning context

- Date: 2026-09-19; found in the expanded browser walkthrough.
- Symptom: opening a new ZFS workflow after configuring an existing repository could
  retain the prior repository context or race its unfinished close/reload. Scope
  radios also rendered above their labels and lacked native keyboard grouping.
- Root cause: toolbar actions did not clear configuration context or prevent
  overlapping wizard lifetimes; generic grid labels were used for radio choices.
- Fix: clear configuration context for fresh actions, key the wizard by repository,
  disable competing open actions until it closes, and use aligned labelled radios
  with a shared native name. Draft/retry identity remains preserved after uncertainty.
- Regression: `.cache/phase1-qa-venv/bin/python tests/integration/zfs_provisioning_browser.py --evidence .cache/zfs-provisioning-browser-complete`
  passed, where `--evidence` names the output directory the run writes its captures and
  results to; it selects no additional checks. The runner checks configure-to-new identity, toolbar locking, Space/arrow
  selection, typed erasure and lost-reply replay; it runs in both GitHub and Gitea
  push/PR foundation workflows. Captures were inspected. Replies are simulated.
- Avoid: reusing a mounted wizard across different repository intents or allowing a
  late response from one submission to close the next workflow.


## BUG-0301 — Controller replacement could reject retained Core reports

- Date: 2026-09-19; found in the disposable two-host replacement walkthrough.
- Symptom: replacement enrollment preserved host identity, but reports failed and
  repositories were absent from the fresh Controller.
- Root cause: Core recovery could republish former Controller operation IDs, which
  report validation rejected. Removing stale resource mirrors also left their unique
  adoption operations behind, so a later authoritative registration could not insert
  another adoption operation.
- Fix: acknowledge valid unknown results without creating state, retain cross-host
  rejection for known operations, and restore an orphan mirror only through its exact
  host/repository adoption operation using the current Core registration. Missing
  registrations keep an unusable mirror rather than deleting its adoption relation.
- Regression: `go test ./internal/controller -run 'TestReplacementControllerAcknowledgesFormerResultsAndAdoptsZFS|TestCoreStorageReportRepairsOrphanAdoptionProjection|TestMasterReportMarksStaleZFSProjectionUnusableUntilCoreReregisters' -count=1`
  passed. Both GitHub and Gitea push/PR foundation workflows run these through the
  full Go race suite. Native retesting uses the preserved failing database; its final
  result is recorded separately in the [rollout report](qa/2026-09-19-zfs-provisioning.md).
- Avoid: resetting the Controller database to hide an adoption failure, accepting
  another host's known operation, or deriving resource state from an unknown result.


## BUG-0302 — ZFS detach after rename mixed aggregate and local recovery state

- Date: 2026-09-19; reproduced through the disposable member-only repository API.
- Symptom: rename and recheck succeeded, but detach failed its member generation
  check. The failed lifecycle result then hid the successful repository registration.
  Review also found that a lost detach reply could leave an aggregate unrecoverable.
- Root cause: the master generation was forwarded as the member's local generation,
  lifecycle results overwrote provisioning outcomes, and replay required a live local
  registration even after successful detach had left its durable tombstone.
- Fix: retain the master generation fence and translate to the observed local
  generation; keep lifecycle receipts separately; replay only exact local detach
  receipts and retain a ready receipt across aggregate removal. Earlier development
  outcomes are repaired only against authenticated current matching native GUIDs.
- Regression: `go test -race ./internal/agent -run 'ZFSLifecycle|TestPoolZFSProjectionPublishesCompletedDetachTombstoneWithoutMasterDataset' -count=1`
  passed, covering aggregate generation two/local generation one, stale master
  rejection, failed-lifecycle isolation, matching/mismatched GUID repair, and crashes
  before and after aggregate removal. Canonical tombstone projection also removes
  detached master-only mirrors while preserving newer registration generations.
  Both GitHub and Gitea push/PR foundation
  workflows run these tests in the full Go race suite. Native terminal detach and
  unchanged-pool evidence are recorded in the [rollout report](qa/2026-09-19-zfs-provisioning.md).
- Avoid: weakening Controller adoption gates, rewriting immutable failed command
  receipts, treating missing registration as proof of successful detach, or reporting
  HTTP 202 as completed work.


## BUG-0303 — ZFS source labels repeated the pool name

- Date: 2026-09-19; found by inspecting the real disposable Controller storage page.
- Symptom: a Core-reported `tank/vms` dataset rendered as `tank/tank/vms`.
- Root cause: the shared source formatter always prepended the pool, assuming every
  dataset was relative even though Core can report a qualified dataset name.
- Fix: preserve an already qualified dataset or pool root; prepend the pool only to
  relative names. API impact: existing contract, presentation only; no state changes.
- Regression: `npm --prefix web test` failed before the fix and passed after it,
  covering relative, qualified and root datasets. Typecheck and build also passed.
  Both GitHub and Gitea push/PR foundation workflows execute the web test suite.
  The real-Controller recapture is recorded in the [rollout report](qa/2026-09-19-zfs-provisioning.md).
- Avoid: changing Core-reported identity to compensate for a display assumption.


## BUG-0304 — Adopting a shared ISO made a member's managed journal unreadable

- Date: 2026-09-19; found by independent review of the uncommitted 0.3.5 batch, before release.
- Symptom: after a member adopted one shared ISO, every later managed operation on that host
  (VM, storage, network) failed until the journal file was deleted by hand.
- Root cause: `AdoptSharedMedia` used `"pool-media-" + media.ID` as the operation ID. Media IDs
  are 64 hex characters, so the ID was 75 characters, beyond the 64-character identifier limit.
  `save` does not validate, `readJournal` does, and `journals` fails closed on the first bad file.
  The only test of the path stubbed the function out.
- Fix: derive a bounded deterministic operation ID from a digest of the media ID and run
  `validateManaged` on the constructed command before saving. API impact: none.
- Regression: `go test ./internal/vmhost -run TestAdoptSharedMediaKeepsTheJournalReadableForALongMediaID -count=1`
  failed before the fix and passed after it. Both GitHub and Gitea push/PR foundation workflows
  run it in the full Go race suite.
- Avoid: short fixture IDs, asserting only that adoption returned nil (the damage shows on the
  next journal read), or deleting an invalid entry at read time instead of never writing it.


## BUG-0305 — Members re-hashed every shared ISO on every probe pass

- Date: 2026-09-19; found by independent review before release.
- Symptom: each member read the whole shared ISO library from NFS/SMB every ten seconds; long
  passes skipped later passes, so storage access observations aged out and placement refused.
- Root cause: media convergence had no already-adopted short-circuit, and the already-published
  path still ran a full SHA-256, unlike network convergence.
- Fix: skip media whose journalled storage, digest and size match and whose published file is
  still a regular file of that size; hash only on first adoption. A same-size in-place
  replacement is therefore not re-detected by convergence.
- Regression: `go test ./internal/vmhost -run TestAdoptSharedMediaDoesNotRehashAnAdoptedISO -count=1`
  failed before the fix and passed after it; both workflows run it in the Go race suite.
- Avoid: a Controller- or process-side "last verified" cache for something the host journal
  already records.


## BUG-0306 — A hung shared mount stopped member convergence permanently

- Date: 2026-09-19; found by independent review before release.
- Symptom: on a member whose shared storage hung, network, media and storage convergence stopped
  and never resumed until restart.
- Root cause: `adoptPoolMediaNative` discarded its context and `verifyISO` hashed without
  cancellation, so the goroutine never returned and `poolReconcileRunning` stayed true. The
  convergence goroutine was also not registered with `runWork`.
- Fix: thread the context through `AdoptSharedMedia` into a context-checking reader, and register
  the goroutine with `runWork`. The initial open/stat on a hung mount remains uninterruptible.
- Regression: `go test ./internal/vmhost -run TestAdoptSharedMediaHonoursACancelledContext -count=1`
  failed before the fix and passed after it; both workflows run it. The `runWork` registration has
  no dedicated test.
- Avoid: clearing `poolReconcileRunning` on a timeout while the goroutine still runs; that lets a
  second pass mutate the same journal concurrently.


## BUG-0307 — An unreachable host poisoned the ZFS lifecycle journal

- Date: 2026-09-19; found by independent review before release.
- Symptom: after a ZFS recheck or detach against an unreachable host, lifecycle operations on any
  other provisioned repository failed with "ZFS lifecycle operation ID belongs to another
  repository", across restarts. The real cause was also replaced by a generic failure message.
- Root cause: the dispatch result was persisted before its error was tested, a failed dispatch
  returns an empty result, and the receipt was keyed on the result's empty operation ID.
- Fix: key receipts on the lifecycle command's operation ID, refuse an empty one, drop an existing
  empty-key entry on load, and wrap a failed dispatch as needs-attention with its real error.
- Regression: `go test ./internal/agent -run TestZFSLifecycleDispatchFailureKeepsCauseAndDoesNotPoisonJournal -count=1`
  failed before the fix and passed after it; both workflows run it in the Go race suite.
- Avoid: testing through detach with a stubbed dispatcher; detach fails in preflight, before the
  persist. Drive the real dispatcher against a host that is not a reachable member.


## BUG-0308 — A reopened ZFS wizard kept the disk-erase confirmation armed

- Date: 2026-09-19; found by independent review before release.
- Symptom: closing and reopening the wizard landed on the confirm step with the typed by-id
  confirmations restored, so **Provision and erase selected disks** was one click away. The
  browser check asserted this as correct.
- Root cause: the wizard serialised its whole selection state, including each confirmation and
  the current step, to `sessionStorage`, while the discovered devices were never restored.
- Fix: persist selections with blank confirmations, stop persisting the step so a reopened wizard
  starts at scope and asks Core again, and keep the request identity so an unchanged retry
  replays. API impact: none.
- Regression: `npm --prefix web test` and `tests/integration/zfs_provisioning_browser.py` failed
  before the fix and passed after it; both workflows run both.
- Avoid: clearing storage on close or dropping the stored request identity, which breaks safe
  retry of a submission whose reply was lost.


## BUG-0309 — "Configure this host" discarded the chosen host

- Date: 2026-09-19; found by independent review before release.
- Symptom: the per-host action opened the wizard with an empty host picker listing every eligible
  host, including hosts Core already reported as configured for that repository.
- Root cause: the wizard's `initialHostID` property existed but was never passed.
- Fix: carry the selected row's host into the wizard and exclude hosts that already have a
  Core-reported outcome for the repository.
- Regression: `npm --prefix web test` and `tests/integration/zfs_provisioning_browser.py` failed
  before the fix and passed after it; both workflows run both.
- Avoid: a Controller-side record of which host holds which repository; derive the exclusion from
  host-reported outcomes.


## BUG-0310 — A pool VLAN retry burned its request ID

- Date: 2026-09-19; found by independent review before release.
- Symptom: registering a VLAN with a member's `host_id` returned 201, and the identical retry
  returned 409 "request_id already used with another payload" although the network existed.
- Root cause: `registerResource` replaced the host with the pool master after the replay check and
  before storing the idempotency payload, so the stored payload was never what a client resends.
- Fix: store the request as received; the row and command still carry the master. API impact:
  existing contract, now documented.
- Regression: `go test ./internal/controller -run TestPoolVLANRequestedOnMemberReplays -count=1`
  failed before the fix and passed after it; both workflows run it.
- Avoid: relaxing the payload comparison or requiring clients to send the master's ID.


## BUG-0311 — Network adoption left attention open and one bad VLAN failed the host report

- Date: 2026-09-19; found by independent review before release.
- Symptom: a repaired network kept its needs-attention operation forever; separately, one
  master-reported VLAN failing validation made every report from that host return 500.
- Root cause: the network loop moved from the shared save helper to `mirrorNetworkProjection`,
  which never resolves attention and returns a hard error that the held branch propagated.
- Fix: resolve network attention after a successful mirror, and record attention and continue in
  the held branch instead of failing the report.
- Regression: `go test ./internal/controller -run 'TestRepairedNetworkClosesAdoptionAttention|TestInvalidMasterVLANDoesNotFailHostReport' -count=1`
  failed before the fix and passed after it; both workflows run it.
- Avoid: making the mirror swallow validation failures, or clearing attention on a timer.


## BUG-0312 — Updating an older host failed on unsatisfiable OpenZFS dependencies

- Date: 2026-09-19; found by independent review before OpenZFS packages shipped in Core updates.
- Symptom (by analysis, not observed): a host installed from 0.3.4 or earlier could not apply the
  update, because `zfs-dkms` and `zfsutils-linux` had no installation candidate.
- Root cause: the Core package declared them as hard dependencies, but they live in Debian
  `contrib`, which only a fresh ISO install enabled. The first upgrade is driven by the old
  installed updater, and apt resolves dependencies before any new package script runs.
- Fix: recommend the OpenZFS packages instead, have `postinst` idempotently add `contrib` to the
  Debian deb822 sources, and have the updater install still-missing recommendations after a
  successful Core upgrade. ZFS therefore arrives one update later on such hosts.
- Regression: `python3 -m unittest tests.test_storage_packaging` and
  `go test ./internal/nexupdate/... -count=1` failed before the fix and passed after it; both
  workflows run them. Outstanding: no real 0.3.4 to 0.3.5 upgrade was run; apt's handling of an
  unsatisfiable recommendation needs a disposable-lab install before field use.
- Avoid: fixing this only in new updater code, or rewriting an operator's sources file.


## BUG-0313 — The pre-wipe signature gate used a non-portable wipefs form

- Date: 2026-09-19; found by independent review before release.
- Symptom (by analysis): the gate immediately before a disk wipe called `wipefs` with a column
  list, the form device discovery had already abandoned for breaking blank-disk scans on a
  supported util-linux release. No test executed the wipe path at all.
- Root cause: a second, differently shaped `wipefs` invocation beside the existing JSON helper.
- Fix: reuse `zfsDeviceSignatures`; inspection errors still fail closed and old data is still
  refused without an exact confirmation. `wipe-started` is journalled only immediately before the
  first real wipe, the adopt branch verifies vdevs, and a provision intent requires a fingerprint.
- Regression: `go test ./internal/vmhost -run 'TestZFSProvisionRefusesDataFoundAfterDiscoveryWithoutEraseConfirmation|TestZFSProvisionWipesOnlySelectedDevicesAndNeverForcesPoolCreate' -count=1`
  failed before the fix and passed after it; both workflows run it.
- Avoid: a third call shape for the destructive path, or changing output columns to compensate.


## BUG-0314 — A blocked release printed a traceback

- Date: 2026-09-19; found by independent review.
- Symptom: `scripts/gitea-release.py` reading an invalid compliance manifest printed a Python
  traceback instead of its one-line reason. The exit status was already non-zero.
- Root cause: JSON parsing was unguarded, outside the exception types `main` reports.
- Fix: raise `GiteaError` for an invalid or non-object manifest; still fail closed before any
  release API call.
- Regression: `python3 -m unittest tests.test_gitea_release` failed before the fix and passed
  after it; both workflows run it.
- Avoid: broadening `main` to catch every exception, or letting an unreadable manifest pass.


## BUG-0315 — ZFS device discovery offered the disks of an imported pool as erasable

- Date: 2026-09-19; found natively in the disposable two-host ZFS lab, before release.
- Symptom: after a pool was provisioned on a disk, discovery on both hosts still reported that
  disk as `erase_required`, selectable for a new repository, while `zpool status -P` listed its
  first partition as an online member. A system disk with a stable by-id name and mounted
  partitions would have been offered the same way.
- Root cause: `lsblk --json` was called without `--tree`. Without a NAME column current
  util-linux prints a flat device list, so every disk had no children and the imported-pool,
  mounted, swap, RAID and LVM checks, which walk a disk's partitions, saw nothing. The
  pre-wipe revalidation reuses the same discovery, so it shared the blind spot. Unit fixtures
  hand-wrote nested children, so the tests passed.
- Fix: pass `--tree`, and refuse to offer any device when a partition appears at the top level
  of the reply, so a future change in `lsblk` output fails closed.
- Regression: `go test ./internal/vmhost -run 'TestDiscoverZFSDevices' -count=1` — the discovery
  test now requires `--tree` in the arguments and `TestDiscoverZFSDevicesRefusesAFlatDeviceList`
  feeds the flat shape seen in the lab; both failed before the fix and pass after it. Both GitHub
  and Gitea push/PR foundation workflows run them in the Go race suite. Native recheck, run by
  the lab session on 2026-09-19 in the same disposable two-host QEMU lab (Debian 13, util-linux
  2.41.5, OpenZFS 2.3.9, Core built from the uncommitted working tree): on both hosts the disk
  backing the imported online pool now reports `member of an imported zpool`, the spare disks
  stay `blank`, and the system disk stays ineligible. Limits: virtio disks, one pool per host;
  a real-hardware system disk with a stable by-id name was not exercised.
- Avoid: fixtures shaped by hand instead of captured from the tool, and trusting any in-use check
  whose input structure was never observed on a real host.


## BUG-0316 — A member's half of a pool-wide ZFS repository went stale after provisioning

- Date: 2026-09-19; found natively in the disposable two-host ZFS lab, before release.
- Symptom: about thirty seconds after provisioning a pool-scoped ZFS repository, the member's
  access entry stopped refreshing while both Cores and the peer path were healthy, so the
  storage page showed the member as stale and no VM could be placed on the member's repository.
- Root cause: the master pulls each member's access from the member's peer `/access` reply. That
  handler answered from the journalled registration, whose access and observation time are
  fixed at registration, while the host's own report overlays the live storage monitor.
- Fix: the agent shares its running storage monitor with the peer handler, which overlays the
  live observation before answering; an unobserved repository answers unknown, not writable.
- Regression: `go test -race ./internal/agent -run TestPoolZFSAccessProbeFencesMembersAndReleasesConnections -count=1`
  failed before the fix and passed after it; both GitHub and Gitea push/PR foundation workflows
  run it in the Go race suite. Native recheck, same lab and build as BUG-0315: both hosts'
  access observation times advanced between two samples forty seconds apart, writable and
  without error, where the member's had been frozen at provisioning time before the fix.
- Avoid: refreshing the observation time in the handler without observing, or having the
  Controller treat a member's row as fresh because the master's is.

## BUG-0317 — A member's failed pool VLAN registration reserved the network ID for ever

- Date: 2026-09-19; found by independent review of the working tree, before release.
- Symptom: a member whose first apply of a pool VLAN failed for a host reason — most often an
  uplink NIC name the member does not have, or a switch trunk that does not carry the tag —
  never converged afterwards. Every later attempt answered `resource ID already reserved by
  another operation` instead of retrying, so fixing the uplink changed nothing and the member's
  `pool-network-reconciliation` check reported a reservation message that named the wrong cause.
  The guide told operators to fix the uplink and wait, which could not work.
- Root cause: `Execute`'s reservation loop reserves `network:<id>` permanently for any journal
  entry, whatever its status. Its exception list covered only `vm-update` and a same-generation
  `storage-register`, so a `failed` `network-register` kept the ID even though it applied nothing
  durable.
- Fix: one more exception arm in the same loop — a `failed` `network-register` for the same pool
  VLAN network ID does not reserve it, so the registration may be retried under a later operation
  ID. Building the bridge is idempotent and a failed registration has no applied projection.
  `needs_attention` stays reserved, because native rollback is ambiguous there.
- Regression: `go test ./internal/vmhost -run TestFailedPoolVLANRegistrationCanBeRetried -count=1`
  passes on the fixed tree (`ok`, re-run by the documentation lane). The failure it reproduces was
  observed before the fix by the independent reviewer, against the real engine: hour one
  `status=failed err=uplink <nic> does not exist on this host`, hour two `status=failed
  err=resource ID already reserved by another operation`. Both the GitHub and Gitea foundation workflows run the test, because
  each runs `go test -race -timeout 20m ./...` on pushes and pull requests. No native two-host
  lab run: the member-side convergence this unblocks has in-process evidence only.
- Avoid: retrying under fresh operation IDs without releasing the failed reservation (the retry
  is refused by the reservation, not by the host condition), and releasing a `needs_attention`
  entry whose native state is ambiguous.

## BUG-0318 — A staged management network change could never be applied, confirmed or discarded

- Date: 2026-09-19; found in lane review of the unshipped staged management-network slice
  (S3), before any release.
- Symptom: every `apply`, `confirm` and `cancel` of a staged management-network edit failed
  with `no management network change is staged as <id>: management network conflict`. A
  staged change could be created and then neither applied nor discarded. Both unit suites
  passed, because neither crossed the Controller/Core boundary.
- Root cause: two distinct identities were being served by one field. The Controller minted a
  fresh `operation_id` per REST call — correct, it is the durable managed journal's replay key
  — while Nex Core matched the action against the *staged change's* own ID and additionally
  required `request.operation_id == command.operation_id`. The REST body carried no field for
  the staged change's ID.
- Fix: a separate `change_id` on `protocol.ManagementNetworkRequest`, required by `Valid()`
  for `apply`, `confirm` and `cancel` and refused on `stage`; Core matches on it; the
  Controller fills it from the host's own reported `management_network.operation_id`, never
  from Controller memory, and refuses a body `change_id` that names a different change.
  `ManagementNetworkStatus.window_seconds` was added in the same change so a staged change can
  still state its window after a page reload.
- Regression: `internal/controller/hostnetwork_e2e_test.go` —
  `go test -race -count=1 -timeout 30m ./internal/controller` passed (`ok
  nex.local/nex/internal/controller 32.453s` for the `TestHostNetwork|TestPoolJoin|TestPoolMembers`
  subset re-run by the documentation lane; `TestHostNetworkStagedEditEndToEnd` and
  `TestHostNetworkActionsNameTheReportedChange` both PASS). The test drives stage to apply to
  confirm and stage to cancel through the real Controller HTTP handlers into the real Nex Core
  engine on a scratch `/etc`, and was observed failing on the pre-fix code with the text above.
  Both the GitHub and Gitea foundation workflows run it, because each runs
  `go test -race -timeout 20m ./...` on pushes and pull requests. No native host run: the
  interface bounce is faked and `TestManagementNetworkNetns` has never executed.
- Avoid: collapsing the two identities — making the command ID the change ID breaks journal
  replay, and having Core ignore the ID lets a stale command act on a change the operator never
  saw. Equally, do not have the Controller remember the staged change's ID; that would put host
  state in the Controller. Do not trust two green unit suites either side of a boundary as
  evidence that the boundary works.

## BUG-0319 — A quiet pool master erased every member's peer origin

- Date: 2026-09-19; found in documentation-lane review of the unshipped Controller-driven
  pool-join slice (S1), before any release.
- Symptom: `GET /api/v1/pools/{id}/members` returned `peer_origin: ""` for every member
  whenever `authoritative` was false, so the Hosts tab's `master_url` prefill silently vanished
  exactly when an operator was likely to be diagnosing a quiet master.
- Root cause: `peer_origin` was assigned inside the `if out.Authoritative` block in
  `poolMembers`, alongside genuine observations (`status`, `error`, `checks`, access counts).
  It is not an observation: it is part of the master's durable membership record.
- Fix: assign it unconditionally, with a comment stating why.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolMembersKeepsPeerOriginWhenMasterIsNotFresh` passes (PASS, re-run by the documentation
  lane); the failure was observed first by reverting the fix in place. Both the GitHub and Gitea
  foundation workflows run it through `go test -race -timeout 20m ./...` on pushes and pull
  requests.
- Avoid: making `authoritative` true for a stale master, or caching the last-seen origin in the
  Controller — that would make the Controller the only holder of a host address.

## BUG-0320 — A quiet pool master's stale failure rows were presented as current

- Date: 2026-09-19; found in documentation-lane review of the unshipped S1 slice.
- Symptom: member rows carried `failures` entries mirrored from the master's resource
  registrations even when `authoritative` was false, contradicting the contract's rule that an
  unobserved value is shown as unknown.
- Root cause: the `uplinkFailures` append sat after the `if out.Authoritative` block instead of
  inside it; the neighbouring `memberAccess` failures were already inside it.
- Fix: guard the append with `out.Authoritative`.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolMembersReportsNoFailuresWhenMasterIsNotFresh` passes (PASS, re-run by the
  documentation lane); it also asserts the rows *do* appear while the master is fresh, so it
  proves withholding rather than an empty fixture. Both workflows run it as above.
- Avoid: blanket-blanking every field for a non-fresh master. `guests` comes from the member's
  own inventory, `last_maintenance` from the Controller's own operations table, and `replica`
  has its own staleness ladder. Check each field's owner before hiding it.

## BUG-0321 — A terse Core refusal produced an empty 409 body

- Date: 2026-09-19; found in documentation-lane review of the unshipped S1 slice.
- Symptom: a `failed` PoolResult carrying an empty `error` produced `409 {"error":""}`; the UI
  showed a refusal with no text.
- Root cause: `poolResultValid` permits an empty `Error` on a failure, and `addPoolMember`
  relayed `result.Error` straight into `fail`.
- Fix: fall back to `the pool master refused to issue a join invitation` when the relayed
  message is empty; a non-empty Core message is still relayed verbatim.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolJoinTerseCoreRefusalIsReadable` passes (PASS, re-run by the documentation lane); the
  pre-fix failure `terse Core refusal: 409 ""` was observed first. Both workflows run it as above.
- Avoid: tightening `poolResultValid` to reject empty-error failures instead — that turns an
  honest if terse refusal into a 504 broker timeout, which is a worse lie and makes the retry
  advice wrong.

## BUG-0322 — An abandoned pool-join request was answered with a bare 200

- Date: 2026-09-19; found in documentation-lane review of the unshipped S1 slice.
- Symptom: when the client disconnected while the handler waited on the broker, the handler
  returned having written nothing, so net/http emitted `200` with an empty body — a success
  status for an operation that never happened.
- Root cause: `case <-r.Context().Done(): return` in `relayPoolRequest`'s select wrote no
  response.
- Fix: write `499` with `the client disconnected before the pool master answered; retry with
  the same request_id`. The pre-existing `defer` already deletes the broker entry, so an exact
  retry with the same `request_id` is accepted rather than refused as already active.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolJoinAbandonedRequestIsNotSuccessAndReleasesBroker` passes (PASS, re-run by the
  documentation lane); the pre-fix failure `an abandoned join was answered with success: 200`
  was observed first. The test cancels the request context while the broker holds it, then
  retries the same `request_id` and expects 202. Both workflows run it as above.
- Avoid: deleting the broker entry only on the success and timeout paths, or dropping the
  `Context().Done()` case altogether — the handler would then hold a goroutine and a broker slot
  for the full join wait after the client left. `internal/controller/storage_cluster.go`'s
  `relayStorageCluster` carried the identical defect; another lane fixed it in the same working
  tree (`internal/controller/storage_cluster_abandoned_test.go`), which is where the "grep every
  caller of the shape, not only the one the report names" rule paid off.

## BUG-0323 — An expired pool-join invitation could outlive its window after a Controller restart

- Date: 2026-09-19; found in documentation-lane review of the unshipped S1 slice.
- Symptom: a single-use pool-join invitation and pool CA could sit in
  `managed_resources.command` past the 15-minute TTL in a Controller that restarted and then
  received no further pool traffic.
- Root cause: the scrub was armed only by an in-process `time.AfterFunc`, which dies with the
  process, and by two of the three pool-join routes; `poolJoinCandidates` never called it,
  although the sweep's own comment claimed every pool-join route did.
- Fix: call `scrubPoolJoinInvitations` from `Open` (immediately before the health monitor
  starts) and from `poolJoinCandidates`, and correct the comment.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolJoinInvitationScrubbedOnControllerStart` passes (PASS, re-run by the documentation
  lane); it backdates the queued operation past the TTL, closes and reopens the Controller and
  asserts the invitation and pool CA are gone with no HTTP request made. The pre-fix failure was
  observed first. Both workflows run it as above.
- Avoid: adding a background ticker goroutine for this — one query at start plus the route calls
  covers it without another lifecycle to stop and test. And do not trust a comment that claims a
  hook runs everywhere: grep the callers.

## BUG-0324 — Controller and host-local API disagreed on a trailing-slash origin

- Date: 2026-09-19; found in documentation-lane review of the unshipped S1 slice.
- Symptom: `POST /api/v1/pools/{id}/members` returned `400` for `https://host:9444/` while the
  host-local `POST /api/local/v1/pool/join` accepted the identical string, so the same operator
  input worked on one path and not the other. Only API clients hit it; the UI normalises first.
- Root cause: `bareHTTPSOrigin` required an empty path and an exact match against
  `https://`+host, whereas the host-local `validHTTPSOrigin` allows a single `/`.
- Fix: `normalizedHTTPSOrigin` accepts at most one trailing `/` and returns the canonical
  slash-less form; `bareHTTPSOrigin` delegates to it. The Controller normalises `peer_origin`
  and `master_url` *before* building the replay payload and before forwarding, and normalises a
  host-reported `peer_origin` too, so the two spellings are the same request for exact-retry
  replay. Paths, queries, fragments, userinfo and non-HTTPS schemes are still refused.
- Regression: `go test -race -count=1 ./internal/controller -run
  TestPoolJoinOriginAcceptsTrailingSlashLikeTheHostLocalAPI` passes (PASS, re-run by the
  documentation lane); it joins with slashed origins, asserts the forwarded command carries the
  normalised form, asserts the unslashed spelling of the same `request_id` replays with 200, and
  asserts a real path is still 400. The pre-fix failure was observed first. Both workflows run it
  as above.
- Avoid: relaxing only the validator and leaving the raw string in the payload — then the two
  spellings become different payloads and an exact retry turns into `request_id already used
  with another payload`. Normalise at the boundary, once, before the replay record is built.
  Equally, do not loosen the Controller further than the host-local rule; the point is parity,
  not permissiveness.

## BUG-0325 — A registration the pool master refused could never be discarded

- Date: 2026-09-19; found by the Phase 4 native acceptance campaign (lab check D1), before release.
- Symptom: a pool VLAN registration the master refused stayed in `GET /api/v1/networks` for ever
  with `status: failed`. `DELETE` and `PATCH /api/v1/networks/{id}` both answered `404 network is
  not registered on this pool's master`, so the row could not be removed or renamed and no action
  existed to clear it. The storage twin was worse: a `storage-register` the master rejected produced
  a row whose `id` was the empty string, so no route could address it at all. The failed ISO import
  already had the right model — a separate **Discard failed import** that asks no host to do
  anything.
- Root cause: `networkLifecycle` (`internal/controller/managed.go`) requires
  `operations.status = 'succeeded'` before relaying and 404s otherwise, with no discard branch of
  the kind `deleteMedia` has. Separately `registerStorage` (`internal/controller/storage.go`) stored
  a typed registration under the synthetic row key `pending-storage-<operation_id>` while leaving
  `cmd.Storage.ID` empty, because Core allocates the repository ID; the mirroring renames the row to
  Core's ID on success, so only refused registrations were left with an empty `id` in the response
  and the list, which key on the command, not on the row.
- Fix: one shared helper `discardFailedRegistration`, used by the network `DELETE` and by a new
  `DELETE /api/v1/storage/{id}`. It deletes only the Controller's `managed_resources` row, relays
  nothing, audits `network.discard` / `storage.discard`, keeps the operation and its audit history,
  and takes no body. Its guard is narrow: discardable only when the operation failed **and** the
  stored command is not one a host has reported as applied; a mirrored-applied row answers `409` and
  survives. `resourceRowResponse` fills the row key into the `id` of a *failed* storage registration
  only, so a pending one still honestly answers `id: ""` and the Controller still never invents a
  repository ID.
- Regression: `internal/controller/failed_registration_discard_test.go`,
  `TestFailedNetworkRegistrationIsDiscardable` and
  `TestFailedStorageRegistrationIsAddressableAndDiscardable`. Command:
  `go test -race -count=1 -run 'TestFailedNetworkRegistrationIsDiscardable|TestFailedStorageRegistrationIsAddressableAndDiscardable' ./internal/controller`
  — passes. Observed failing first: `DELETE /api/v1/networks/4cfa76a1…: got 400 want 200:
  {"error":"invalid JSON request"}` and `a storage registration answered with an empty id, so no
  route can address it`. Full package: `ok nex.local/nex/internal/controller 256.627s`. Both
  `.github/workflows/phase1.yml:29` and `.gitea/workflows/phase1.yml:29` run
  `go test -race -timeout 20m ./...` on pushes to `'**'` and on pull requests, so no workflow change
  was needed. UI coverage: `web/src/managed.test.mjs` and `web/src/storage.test.mjs`, run by
  `npm --prefix web run test` at line 70 of both workflow files. **No native two-host lab rerun of
  D1 against the fix.**
- Avoid: relaxing `networkLifecycle`'s `succeeded` requirement so a failed row can be relayed — the
  master has nothing to remove and would refuse again. Do not key the discard on the operation
  status alone: a failed operation's row can later become the mirror of a host-reported resource
  (the mirror passes rewrite that row's command while `operations.status` stays `failed`), and
  discarding it would delete a live pool resource's projection. Do not answer a pending typed
  registration with a Controller-minted repository ID; that is Core's to allocate.

## BUG-0326 — A placement against a member with stale shared-storage access sat silent

- Date: 2026-09-19; found by the Phase 4 native acceptance campaign (lab check A3, fault b), before
  release.
- Symptom: with NFS dropped from a member, the master's `member_access` row for that member froze.
  Three minutes later `POST /api/v1/vms` targeting that member on the shared repository still
  answered `202`, and the operation sat `dispatched` for the whole outage with no operator signal,
  then succeeded when NFS returned. Nothing unsafe happened — the master's preflight is the
  authority and the VM was only created once access returned — but an operator had no way to tell
  that request from a merely slow one.
- Root cause: `createVM` (`internal/controller/managed.go`) calls only `placementHost` and
  `cpuFeaturesSupported`. `readyStorageForHost` / `readyNetworkForHost` / `readyMediaForHost`, which
  carry the `-stale-after` gate, were used for the placement hint, media import and `vm-media` but
  never for `vm-create` — a deliberate "Core validates, Controller mirrors" choice — and nothing
  exposed the mirror's opinion anywhere an operator would see it.
- Fix: an honest non-blocking signal, no Controller veto and no change to acceptance semantics.
  `vmCreateWarnings` reuses the three `ready*ForHost` helpers as advisory only and returns one
  sentence per required repository, network or ISO the mirror does not show the selected host able
  to use. `createVM` adds them to the `202` body as `warnings` when non-empty; `operationWarnings`
  recomputes them for every still-`dispatched` `vm-create` in the operations listing, so the signal
  is live rather than a snapshot and nothing extra is stored.
- Regression: `internal/controller/placement_warning_test.go`,
  `TestStaleMemberAccessWarnsWithoutRefusingPlacement`. Command:
  `go test -race -count=1 -run TestStaleMemberAccessWarnsWithoutRefusingPlacement ./internal/controller`
  — passes. Observed failing first: `no warning for a create against a member whose repository
  access is stale`. It first proves a fresh member is **not** warned about, so the warning is not
  blanket. Both workflows run it as above (`phase1.yml:29`). UI coverage in
  `web/src/managed.test.mjs` via `npm --prefix web run test` (line 70 of both files). **No native
  lab rerun against the fix.**
- Avoid: turning this into a Controller-side placement veto. The mirror is a 30-second-old copy of
  the master's observation; refusing on it would make the Controller a second placement authority
  and would refuse placements the master would accept. Do not store the warnings with the request
  either — a stored warning goes stale the moment the host recovers, which is the failure this
  defect is about.

## BUG-0327 — "Roll back now" could not undo an applied management-network change

- Date: 2026-09-19; found in documentation-lane review of the unshipped S3 slice, before release.
- Symptom: on an applied, unconfirmed management network change the host network panel offers
  **Roll back now**, which submits action `cancel`. Nex Core refused it with `management network
  change <id> is applied and cannot be cancelled: management network conflict`, so an operator who
  could see that the new configuration was wrong, and still had connectivity, could not undo it
  early; the only way out was to wait for the rollback deadline.
- Root cause: `CancelManagementNetwork` (`internal/vmhost/management_network.go`) accepted only the
  `staged` stage, and no command action reached `RollbackManagementNetwork`. The REST contract and
  the UI both assumed `cancel` rolled an applied change back; all three layers passed their own unit
  suite while the flow could not work.
- Fix: `cancel` on an applied, unconfirmed change now runs the same `rollbackLocked` restore with
  the reason `rolled back at the operator's request`, and records
  `ManagementNetworkRecord.RequestedRollback` **before** the restore begins, so a rollback
  interrupted before the interface came back up resumes as the same operator rollback. It is
  reported as `management_network.requested_rollback`, and the agent's `management-network` health
  check is derived from stage `rolled-back` **and** `!requested_rollback`, so a rollback the
  operator asked for is an outcome, not a failure. Staged cancel (discard) and the
  confirmed/cancelled/rolled-back conflicts are unchanged.
- Regression: `TestHostNetworkOperatorRollbackEndToEnd`
  (`internal/controller/hostnetwork_e2e_test.go`, Controller → real `agent.ExecuteManagementNetwork`
  → real engine, with the result delivered on a *later* report to model the cut connection),
  `TestManagementNetworkOperatorRollback` (`internal/vmhost/management_network_test.go`,
  byte-identical restore of all three files) and the extended
  `TestReportRecordsManagementConnectivityAndProjectsStatus` (`internal/agent`). Command:
  `go test -race -count=1 -run 'TestHostNetworkOperatorRollbackEndToEnd' ./internal/controller` →
  `ok 14.128s`. Observed failing first: `the operator's early rollback was refused by the host:
  {Status:failed Error:management network change … is applied and cannot be cancelled: management
  network conflict}`. Both workflows run all three via `go test -race -timeout 20m ./...`
  (`phase1.yml:29`); `web/src/host-network.test.mjs` covers the new label and note and runs via
  `npm --prefix web run test` (line 70 of both files). **The namespace test that would bounce a
  real interface has still never executed**, so no real `ifup`/`ifdown` rollback has been observed.
- Avoid: do not special-case the UI to hide the button, and do not let the Controller drive the
  rollback. The rollback is Core's and must stay re-runnable and durable, because it can cut the
  very connection that asked for it. Do not distinguish an operator rollback from an automatic one
  by stage plus error text: only a durable flag can say which it was after a restart.

## BUG-0328 — A member retried a failed pool VLAN apply only on the clock hour, and re-posted the replayed failure as a fresh observation

- Date: 2026-09-19; found by the Phase 4 native acceptance campaign (lab checks A2/D3), before
  release.
- Symptom: after a member's pool VLAN apply failed because the uplink NIC did not exist, fixing the
  uplink did not converge the member. Convergence happened only when the UTC clock hour rolled over
  — anywhere from seconds to 60 minutes later — so `pool-network-reconciliation` stayed `failed` and
  the network row stayed unusable for placement. Meanwhile every 10 s converge tick re-posted the
  *replayed* journal error to the master with a brand-new `observed_at`, so the master's
  `member_access` row looked like a fresh observation of a condition that no longer existed.
- Root cause: `applyPoolNetworkNative` (`internal/agent/pool_network.go`) called
  `ManagedEngine.Execute` with an operation ID that hashes the current UTC clock hour. That hour
  pinning exists on purpose — it bounds journal growth from a persistently failing apply, because a
  salted ID would mint one entry per 10 s tick and reach the engine's 10000-entry cap within a day,
  after which every managed operation on the host fails — but it was the *only* retry gate on the
  apply path, so every tick inside the hour replayed the stored failure instead of re-evaluating the
  host precondition, and `reconcilePoolNetwork` stamped that replayed result with `time.Now()`
  before posting it. The removal path already ran `CheckNetworkRemoval` before `Execute`; the apply
  path had no precheck.
- Fix: (1) a side-effect-free precheck before `Execute` — now `vmhost.CheckNetworkApply`, beside
  the existing `CheckNetworkRemoval`, holding the six conditions the native apply refuses on
  (invalid registration, missing uplink, uplink is a bridge/VLAN/loopback, uplink is a bond member,
  VLAN port owned by another bridge, bridge name taken by a non-bridge). It reads only
  `/sys/class/net` and `/proc/net/vlan`, only ever refuses, journals nothing, and is re-evaluated
  every tick, so an operator's fix converges on the next pass. (2) `poolReplayedFailure`: a result
  whose `ObservedAt` predates this pass was replayed, not evaluated, and is **not** posted — the
  master's last real observation and its original `observed_at` stand. The hour-pinned ID stays as
  the backstop for failures no precheck can see, so the journal bound is unchanged at ≤ 24 entries
  per resource per day.
- Regression: in `internal/agent/pool_network_test.go` —
  `TestPoolNetworkApplyRefusalJournalsNothingAndRetriesWhenTheCauseIsFixed`,
  `TestPoolNetworkUnexpectedFailureJournalsAtMostOncePerHour` (a simulated day of 1440 ticks),
  `TestPoolNetworkReplayedFailureKeepsTheMastersObservation`, `TestPoolNetworkApplyPrecheckRefusals`;
  in `internal/agent/pool_media_test.go`,
  `TestPoolMediaAdoptionFailureIsReEvaluatedEveryTickAndJournalsNothing`; and in
  `internal/vmhost/vlan_test.go`, `TestCheckNetworkApplyAndTheNativeApplyRefuseAlike`, which proves
  the precheck and the native apply return byte-identical errors for each condition. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent ./internal/vmhost` →
  `ok nex.local/nex/internal/agent 16.452s`, `ok nex.local/nex/internal/vmhost 12.829s`. Observed
  failing first with a temporary reproduction on the unmodified tree (three passes returning the
  identical stored `observed_at`, one journal entry per refused apply) and, for the second half, by
  deleting the replay guard and re-running the kept test. Both workflows run them via
  `go test -race -timeout 20m ./...` (`phase1.yml:29`). **No native lab rerun against the fix.**
- Avoid: do **not** salt the converge operation ID (timestamp, counter, random) so every tick
  retries for real — that is exactly what the hour pinning prevents, and it reaches the engine's
  `managed journal full; manual recovery required` cap in a day, after which every managed operation
  on the host fails. Shortening the window is the same mistake more slowly. The retry gate must be a
  *precondition re-evaluated for free*. And do not re-stamp a replayed journal result with the
  current clock to keep a row looking fresh — that is what made the lab row lie.

## BUG-0329 — Re-adding a member before its master read as a broken host

- Date: 2026-09-19; found by the Phase 4 native acceptance campaign (lab check A6), before release.
- Symptom: adding a pool member to a replacement Controller before its master had reported it
  answered `502 {"error":"host could not enroll with the controller at …: controller rejected
  request (HTTP 409): pool identity is held; the pool master has not reported this ready member"}`.
  A retry 30 s later succeeded with the member's original host and pool IDs. The order is documented
  in `docs/guide/manage-controller.md`, but a 502 wrapping a nested 409 reads like a broken host
  rather than a normal wait.
- Root cause: `addSiteHost` (`internal/controller/addhost.go`) wrapped every non-200, non-409 answer
  from the host's local enrollment endpoint into one 502. The Controller's own refusal reaches it
  indirectly — the host relays it as its own error text — so a well-known refusal was flattened into
  a transport failure.
- Fix: the refusal sentence is a named constant `poolIdentityHeldRefusal` beside where `enroll`
  emits it (`internal/controller/identity.go`), and `addSiteHost` special-cases that one string: it
  answers `409` with plain guidance, `"retryable": true` (the marking `poolTransferOperation`
  already uses for a conflict an operator retries unchanged) and `host_error` carrying the host's
  own words. Every other host-side enrollment failure is still a 502.
- Regression: `internal/controller/readd_member_retryable_test.go`,
  `TestReaddingAMemberBeforeItsMasterIsARetryableConflict`. Command:
  `go test -race -count=1 -run TestReaddingAMemberBeforeItsMasterIsARetryableConflict ./internal/controller`
  — passes; it asserts the 409, the guidance, `retryable`, the preserved `host_error`, and that a
  different host-side failure is still 502. Observed failing first: `got 502 want 409`. Both
  workflows run it as above (`phase1.yml:29`); the UI state machine is covered in
  `web/src/managed.test.mjs` via `npm --prefix web run test` (line 70 of both files). **No native
  Controller-replacement rerun against the fix.**
- Avoid: mapping every nested 409 from a host to a Controller 409 — the enrollment endpoint has
  other 409s (`host identity already enrolled`, `presented pool name does not match the
  Core-reported pool`) that are not retryable waits. And do not have the Controller wait or poll for
  the master's report inside the add-host request: the master reports on its own schedule and the
  Controller must not drive a multi-step operation.

## BUG-0330 — An abandoned clustered-storage request was answered 200

- Date: 2026-09-19; found by independent review of the pool-join fixes, **in shipped code**.
- Symptom: when a client disconnected while `relayStorageCluster` waited on the storage-cluster
  broker, the handler returned having written nothing, so net/http emitted a bare `200` with an
  empty body — a success status for clustered-storage work no Core ever answered. It affected every
  route through that relay: `GET /api/v1/storage/cluster`, `POST /api/v1/storage/cluster/apply` and
  `POST /api/v1/storage/cluster/verify-fence`.
- Root cause: `case <-r.Context().Done(): return` in `relayStorageCluster`'s select
  (`internal/controller/storage_cluster.go`) wrote no response. Identical to the pool-join defect
  fixed in `relayPoolRequest`.
- Fix: write `499` (`statusClientClosedRequest`, already defined in `pooljoin.go`) with an
  explanatory body. The pre-existing `defer` already deletes the broker entry, so an exact retry
  with the same `request_id` is accepted rather than refused as already active.
- Regression: `internal/controller/storage_cluster_abandoned_test.go`,
  `TestAbandonedStorageClusterRequestIsNotSuccessAndReleasesBroker`. Command:
  `go test -race -count=1 -run TestAbandonedStorageClusterRequestIsNotSuccessAndReleasesBroker ./internal/controller`
  — passes; it parks an apply on the broker, cancels the request context, asserts the answer is not
  in the 2xx range, then retries the same `request_id`, lets Core answer it and expects 200.
  Observed failing first: `an abandoned clustered-storage request was answered with success: 200`.
  Both workflows run it via `go test -race -timeout 20m ./...` (`phase1.yml:29`).
- Avoid: dropping the `Context().Done()` case instead — the handler would then hold a goroutine and
  a broker slot for the full 30 s after the client left. Do not delete the broker entry only on the
  success and timeout paths. This is the second instance of the same shape; when adding a relay with
  a `select`, write a status on **every** branch.

## BUG-0331 — A cancel was accepted after an eject was already durable

- Date: 2026-09-19; found in documentation-lane review of the unshipped S5 slice, before release.
- Symptom: `POST /api/v1/pools/{id}/master/cancel` for an in-flight eject was accepted at stages
  `repoint` and `leave`, after the membership without that member had been written at the next
  generation. Core set `stage: aborted` without restoring the membership and stopped the
  re-point/leave loop, so the ejected host might never be told to leave and the remaining members
  converged only via the authority probe — an aborted transfer and a removed member at once. The UI
  never offered it, but the route did. A cancelled eject also recorded the *master change* sentence.
- Root cause: `cancelPoolTransfer` (`internal/agent/pool_transfer.go`) gated only on a terminal
  stage and on the target having activated — the master-change rule — with no rule for an eject's
  own irreversible step.
- Fix: one predicate `poolTransferCancellable`, enforced by `cancelPoolTransfer` **and** reported as
  `protocol.PoolTransferStatus.cancellable` (and on `PoolPromotionStatus`), so the reported flag and
  the accepted cancel cannot drift. Cancellable only before the first irreversible durable step:
  a master change until the signed hand-off, an eject until the membership write (stages `freeze`,
  `remove`), a promotion never. New refusals: `host <id> is already removed from this pool's
  membership; the eject can only go forward` and `this pool promotion has powered the old pool
  master off; it cannot be cancelled and can only go forward`. A cancelled eject now records `the
  pool member eject was cancelled by an operator`.
- Regression: `internal/agent/pool_transfer_test.go`,
  `TestPoolEjectCannotBeCancelledOnceTheMembershipIsWritten` (cancel refused, record untouched at
  `repoint`, the eject then completing on the next tick with exactly one master throughout) and
  `TestPoolTransferCancellableMatchesTheCancelAtEveryStage` (15 records over every stage of all
  three actions, asserting the reported flag equals whether `cancelPoolTransfer` actually accepts
  it, including a handed-off master change doing the real abort RPC). Command:
  `go test -race -count=1 ./internal/agent` → `ok 17.0s`. Observed failing first: `cancel after the
  membership removal = {Status:"failed", Error:"the pool master change was cancelled by an
  operator"}` and `master-change freeze: reported cancellable = false, want true`. Both workflows
  run them via `go test -race -timeout 20m ./...` (`phase1.yml:29`). **No native multi-host run of
  the refused-cancel path**; the evidence is the in-process lab over the real mTLS peer transport.
- Avoid: refusing every cancel once a transfer is frozen. A master change must stay cancellable
  through the freeze and the replicate, and the hand-off window must still be attemptable — the
  target's durable never-activate record decides it, not the stage name. Equally, do not leave the
  UI to infer cancellability from a hard-coded stage list: Core owns the predicate and reports it.

## BUG-0332 — The fence credential reached the master, but the candidate does the fencing

- Date: 2026-09-19; found by independent review of the unshipped S6 slice, before release.
- Symptom: a fence-verified promotion could never actually fence. The fence credential is delivered
  to the pool **master**, which records the profile, and `vmhost` reads the material locally from
  `<state-dir>/storage-cluster/fence-credentials/<ref>`. But the host that fences during a promotion
  is the **candidate**, and Core replicated the profile (with its reference) in the membership
  without the material, so the candidate never held the file. Every promotion test passed, because
  they all injected a fake fence at the verification seam; a real promotion would have failed at the
  one step the whole design exists for.
- Root cause: the credential handoff was designed for clustered storage, where the host that
  records a profile is the host that uses it. Nothing carried the material to the host that would
  need it *after* the recording host was gone — and it cannot be delivered then, by definition.
- Fix: each replica push reads the material once and seals it **per member** with the same primitive
  as the pool CA key, under a distinct context binding pool, member, generation, fenced host and
  reference (`protocol.PoolWrappedFenceCredential`, carried in `PoolAuthorityReplica.fence_credentials`).
  The sealed blob is inside `SigningBytes`, so tampering is detected, and excluded from
  `ContentDigest`, so one generation keeps one digest for every member. A member stores it sealed and
  opens it only while performing a promotion; the preflight opens it only to answer truthfully and
  keeps nothing. `ExecutePoolFenceProfile` refuses to record a profile unless the master actually
  holds the material, bounds it at 16 KiB, and keeps a private digest in its own `pool-peer.json`;
  only `PoolMember.fence_credential_version` is replicated, so rotated material advances the
  authority generation and identical material re-recorded moves nothing.
- Regression: `internal/agent/pool_fence_credential_test.go` (new) plus additions to
  `internal/agent/pool_promote_test.go`, `internal/protocol/pool_promotion_test.go` and
  `internal/vmhost/pool_fence_test.go`: a credential round trip master → replica → candidate with
  the fake fence asserting it received **the exact identity bytes** from the candidate's own
  directory; wrong member, wrong generation and tampered ciphertext all refused; the record-time
  refusal with no material; the version moving on rotation and not on an identical re-record; the
  oversize refusal; retire on clear and on replace, never retiring a reference another profile or a
  clustered-storage node still names; the preflight blocked structurally with no credential; two-
  and three-host promotions fencing with the unwrapped material and the new master re-wrapping for
  the remaining member; and log capture plus command result, verdict reasons and replica status
  asserted free of the key, the host-key string and the PEM header. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent ./internal/protocol ./internal/localcore ./internal/vmhost`
  → `ok agent 16.926s`, `ok protocol 1.051s`, `ok localcore 10.261s`, `ok vmhost 12.775s`. Both
  workflows run them via `go test -race -timeout 20m ./...` (`phase1.yml:29`), and
  `tests/test_api_contract.py::PoolPromotionContractTests` holds the wire shape, run by
  `python3 -m unittest discover -s tests -p 'test_*.py'` at `phase1.yml:53` in both files.
  **No real fence has ever run**: every test injects a double where Core calls the fence, so the
  credential's use against a real `virsh` endpoint is unproven.
- Avoid: do not "fix" this by delivering the credential to every host at record time over the
  Controller — the Controller must never hold fence material, and a host that joins later would
  still miss it. Do not give the candidate the material only when a promotion starts, either: the
  master it must fence is gone by then, which is the whole problem. And do not present the sealing
  as an access control — a ready member can open what is addressed to it whenever it likes, which is
  an owner-visible property of the design and is stated in the guide, not a lock.

## BUG-0333 — Two partitioned survivors could both be promoted, giving one pool two masters

- Date: 2026-09-19; found by an **independent review** of this campaign's unreleased Phase 4 work
  (authority review, `.cache/.../reports/review-authority.md`, finding F1), before release. Never
  present in a published build.
- Symptom: with the pool master dead and the two surviving ready members unable to reach each
  other, promoting each of them in turn succeeded. Both ended as pool master of the same pool. The
  reviewer's scenario, reproduced verbatim: `DUAL MASTER: [member-1 member-2]`.
- Root cause: `probePoolPromotionHosts` (`internal/agent/pool_promote.go`) let an **unreachable**
  host pass the `replica-supremacy` check silently — the rule was "any *reachable* host holding a
  higher generation blocks". An unreachable ready member that is not the old master is exactly the
  host that holds this pool's authority replica and could therefore be promoted the same way, so
  its silence is not evidence of anything. A second defect was found while fixing it:
  `poolPromotionRefusal(verdict, "old-master-exclusion")` skipped the exclusion check whatever its
  status, so a promotion aimed at a **reachable, healthy** master would have passed the preflight
  stage and fenced it. A third: nothing stopped two reachable hosts running concurrent promotions
  of the same pool.
- Fix: an unreachable ready non-master member now sets `Blocks` and puts `replica-supremacy` into
  `unknown` naming it, and passes only when this promotion has itself excluded it with a verified,
  promotion-bound fence proof. The exclusion became multi-host: Core computes `FenceHostIDs` (the
  old master plus every blocking host with a recorded profile, sorted), refuses when any of them
  cannot be fenced naming the per-host reason, and passes only when every one has a proof bound to
  pool + host + operation + generation and at most 15 minutes old. `poolTransferRecord.Fence`
  became `Fences []StorageClusterFenceStatus`, each persisted before the host is acted on. The
  exclusion refusal now tolerates only `unknown` ("nothing fenced yet"); `blocked` is a refusal, so
  a healthy master can never be fenced. A reachable host reporting a non-terminal promotion of its
  own blocks with `pool host <ids> is already performing a promotion of this pool; let it finish or
  cancel it before promoting another host`. And the whole set became **operator consent**: the
  command carries `fence_host_ids`, compared against Core's freshly computed list at the preflight
  stage and again inside the fence stage, so a pool that changed after the operator confirmed stops
  the promotion before anything is powered off.
- Regression: `internal/agent/pool_promote_partition_test.go` (new) —
  `TestPartitionedSurvivorsAreNotBothPromoted` (line 43) and
  `TestPromotionNeverFencesAReachableHealthyMaster` (line 198) — plus additions to
  `internal/agent/pool_promote_test.go` covering the per-host `blocks`/`fence_recorded`/
  `fence_usable` preflight rows, a promotion succeeding once both hosts are fenced, refusals for a
  stale / other-operation / other-host proof of the *second* host, four wrong host lists with the
  fence seam asserting it never ran, and concurrent reachable candidates. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok nex.local/nex/internal/agent 17.766s`.
  Cross-layer: `internal/controller/poolpromotion_e2e_test.go:47`
  `TestPromotionFenceConsentReachesCoreEndToEnd` drives preflight → promote through the real
  handlers into Core's own `agent.ExecutePoolPromotion`, and `TestPromoteFenceHostRefusals`
  (line 113) pins the route's refusals; command
  `go test -race -count=1 -timeout 30m ./internal/controller` → `ok … 285.920s`. Both
  `.github/workflows/phase1.yml:29` and `.gitea/workflows/phase1.yml:29` run
  `go test -race -timeout 20m ./...` on pushes to `'**'` and on pull requests, so no workflow change
  was needed. **No real fence has ever run**: every promotion test injects a double at the
  `poolFenceVerify` seam.
- Avoid: do not treat an unreachable host as "not in the way". Do not let the Controller compute,
  complete or correct the fence set — it is Core's, and Core must be able to refuse a confirmation
  that no longer matches. And do not skip the exclusion check on any status but `unknown`: that is
  what let a healthy master be fenced.

## BUG-0334 — The pool's virtual-machine owner records still moved while the authority was frozen

- Date: 2026-09-19; found by the same independent review (finding F3), before release. Never
  present in a published build.
- Symptom: while a master change held the authority freeze, the content the frozen generation names
  changed underneath it. Reproduced as `a frozen authority's content changed: 3f75b48d476a2477…
  then c8d739bcd8951ffc…`. The generation handed to the new master therefore did not identify what
  was handed over.
- Root cause: `recordPoolVMInventory` (`internal/agent/pool_vm_inventory.go`) ran
  `prunePoolVMOwners`/`seedPoolVMOwners` — writes to the replicated `vms` section — with no
  authority guard, while every neighbouring write path took one. An audit of every replicated
  section found two more unguarded entry points: the `/api/v1/pool/advertise` peer-origin write
  into `membership`, and `ResumeStorageClusterOperations` driving `storage_clusters` from the tick.
- Fix: `recordPoolVMInventory` stores the snapshot (an observation, never refused) and then takes
  `requirePoolAuthorityWritable` **inside the same `poolVMMu` section** before the prune and seed,
  returning the new sentinel `errPoolVMOwnersDeferred`, which the peer handler maps to the existing
  `202 Accepted`. Deferred, not dropped: the next publication settles it on whichever host is master
  then. The advertise write defers through the member's existing `PendingPeerOrigin` retry, and the
  clustered-storage resume is skipped while frozen, handed-off or restricted, so work already in
  flight is never interrupted.
- Regression: `internal/agent/pool_authority_freeze_test.go` (new) —
  `TestFrozenAuthorityContentDigestNeverMoves` (line 47), table-driven over placement, start, peer
  inventory, peer prune, own inventory, maintenance, fence profile, join invitation, every registry
  kind, advertise and clustered storage; and `TestOwnerRecordsSettleAfterTheFreezeEnds` (line 158).
  Command: `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 17.766s`; CI as
  BUG-0333.
- Avoid: do not refuse the *observation* — a member reporting its own inventory must always be
  accepted, or a member is punished for a master's transfer. Defer the derived write instead.

## BUG-0335 — A master change handed to a host that then died killed the pool permanently

- Date: 2026-09-19; found by the same independent review (finding F2), before release. Never
  present in a published build.
- Symptom: on a two-host pool, a master change that had signed the hand-off and whose target was
  then destroyed left the pool with no host that would act. The cancel path needs an RPC to the
  target, so it failed: `the fence-backed cancel = failed "pool peer member-1 did not answer this
  transfer step"`, `masters = []`. Nothing an operator could do recovered it.
- Root cause: `handed_off` correctly refuses every authority write on the source for good, and the
  only escape was a cancellation the dead target had to agree to. A malicious or merely unlucky
  Controller could therefore wedge a pool by issuing a master change to a host about to die.
- Fix: a fence-backed resume on the source (`resumeAfterFencedPoolTarget`,
  `internal/agent/pool_transfer.go`), reached through the existing cancel route carrying
  `fence_confirmed` + `fence_host_ids: ["<target>"]`. Preconditions, all enforced: the target is
  probed and unreachable; the operator named exactly that host; the source is still the recorded
  master, still holds the pool CA key and is **not restricted**; the target has a recorded profile;
  and Core's own fence produces a verified, digest-bound power-off. It resumes at authority
  generation **+ 2**, because a target that activated before it died minted exactly + 1 — so the
  resumed source supersedes it by the strictly-higher rule every host already applies, and the
  returning target (restricted by construction, since it was just fenced with its own profile)
  takes the superseded path. A host that was itself fenced and rebooted is restricted, and is
  refused here; that is what stops a fenced host resuming over the host that fenced it.
- Regression: `internal/agent/pool_transfer_lost_target_test.go` (new) —
  `TestHandedOffMasterResumesOnAFenceProofOfTheTarget` (line 69), covering activated-then-died and
  never-activated both resuming to exactly one master with the returning target converging, no
  proof leaving the pool handed off, and proofs of another host / stale / acknowledged-only / wrong
  host list all refused with the source still non-writable. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 17.766s`; CI as BUG-0333.
- Avoid: do not add a timeout-based or "declare host lost" escape. The only thing that makes this
  safe is an observed power-off bound to this operation and generation, and the restriction rule
  that refuses a host which was itself fenced.

## BUG-0336 — A member's self-claim could take the pool, and a member-first Controller replacement deadlocked

- Date: 2026-09-19; found by an **independent review** of this campaign's unreleased work
  (invariant review, finding C-1), before release. Never present in a published build.
- Symptom: a pool member reporting a membership that named itself master took the Controller's
  `pools.master_host_id` row and re-keyed every storage, network and media mirror row to it, with
  no evidence from the pool at all: `a self-claim with no evidence took the pool: master is
  c05c82aa…`. Separately, and traced while fixing it, a replacement Controller that a **member**
  reconnected to first was permanently stuck: the member created the pool row with itself recorded
  as master, and the real master's enrolment was then refused for ever with
  `409 pool identity is held; the pool master has not reported this ready member`.
- Root cause: in `adoptReportedMaster` (`internal/controller/pooltransfer.go:412-416`) the
  strictly-higher-generation guard sat inside `if mirrored != nil`, and `hostInventory` returns
  `(zero, nil)` for a `NULL hosts.inventory` — so with no mirrored membership there was no check at
  all. The enrolment half: `coreReportedPoolMember` (`internal/controller/identity.go:277`) required
  `membership.MasterHostID == master`, which the member-first ordering can never satisfy.
- Fix: the evidence check is unconditional and fails closed — an inventory read error is an error,
  and no mirrored membership for this pool means no adoption. Evidence is either a strictly higher
  generation **or** the recorded master's own mirrored membership already naming the claimant as
  master (the reconnect case, where both agree at one generation). `coreReportedPoolMember` accepts
  that same record, so the recorded master's report naming **the enrolling host** as pool master
  lifts the enrolment hold. Only the recorded master's authenticated report is ever read, and the
  host must still be listed `ready`, so no member can use either arm to take a pool.
- Regression: `internal/controller/pool_evidence_test.go` (new) —
  `TestSelfClaimedMasteryIsRefusedWithoutMirroredEvidence` (line 26) and
  `TestMemberFirstReconnectStillAdoptsTheRealMaster` (line 56, covering fresh-Controller adoption of
  an existing multi-host pool, member-first reconnect and an equal-generation rival afterwards).
  Observed failing first with the quoted line above and with enrolment answering
  `409 pool identity is held`. Command:
  `go test -race -count=1 -timeout 30m ./internal/controller` → `ok … 278.195s`; CI as BUG-0333.
- Avoid: do not fix this by adding the missing `mirrored == nil` condition alone. It closes the
  hole and silently leaves member-first Controller replacement deadlocked at enrolment for ever —
  the two halves are one change.

## BUG-0337 — A join invitation outlived its 15 minutes in the Controller database after a restart

- Date: 2026-09-19; found by the same invariant review (finding C-6), before release. Never present
  in a published build.
- Symptom: a single-use join invitation and the pool CA PEM stayed in `managed_resources.command`
  past their 15-minute TTL when the Controller restarted inside the window and was never asked
  about pools again: `a restarted Controller kept an expired invitation: …Invitation:lab-single-use-invitation-token`.
- Root cause: `pooljoin.go:351` deliberately skips a still-queued young row, and the only triggers
  were `Open`, the three pool routes and a process-local `time.AfterFunc` armed by the issuing
  request. A restart loses the timer and re-arms nothing.
- Fix: `healthTick` sweeps at most once per `poolJoinScrubInterval` (one minute, a new constant),
  gated by `c.joinScrubbed` under the existing `c.mu` — in memory only, no durable state added. The
  fail-open on an unparseable `created_at` was also inverted: such a row is now scrubbed.
- Regression: `internal/controller/pool_evidence_test.go:121`
  `TestPoolJoinInvitationScrubbedAfterARestartInsideTheTTL` (restart inside the TTL with no pool
  route ever called). Command: `go test -race -count=1 -timeout 30m ./internal/controller` →
  `ok … 278.195s`; CI as BUG-0333.
- Avoid: do not call the scrub from the 250 ms tick — it is a database write per tick for a
  one-minute-granularity rule.

## BUG-0338 — A scrubbed pool-join command was redelivered with an empty invitation

- Date: 2026-09-19; found by the same invariant review (finding C-7), before release. Never present
  in a published build.
- Symptom: `an uncollected expired join stayed deliverable: queued ""` — every report redelivered
  the `pool-join` command with empty `invitation` and `pool_ca_pem`, which the host must reject,
  risking a join legitimately in progress being marked failed.
- Root cause: `identity.go:701` redelivers every `queued`/`dispatched` operation and reloads the
  command payload from `managed_resources` (`identity.go:728`) after the scrub emptied it.
- Fix: split by what the row had actually done. A row scrubbed while still **queued** was never
  collected, so the operation is failed terminally with `the join invitation expired before the
  host collected it; start the join again`, visible on `GET /api/v1/operations`. A row scrubbed
  after **dispatch** is simply never re-sent — failing it would declare a healthy in-progress join
  dead, and that join's own result settles the operation.
- Regression: `internal/controller/pool_evidence_test.go:157`
  `TestExpiredPoolJoinFailsInsteadOfBeingRedeliveredEmpty`, covering both halves including the
  in-progress one staying `dispatched`. Command:
  `go test -race -count=1 -timeout 30m ./internal/controller` → `ok … 278.195s`; CI as BUG-0333.
- Avoid: do not fail every scrubbed row. That marks joins that are working as failed.

## BUG-0339 — A host could move itself out of its pool with no report from the master

- Date: 2026-09-19; found by the same invariant review (finding P-3), before release. Never present
  in a published build.
- Symptom: any enrolled host could leave its pool in the Controller's view by reporting a new
  single-host membership, leaving the old pool's mirror rows pointing at a non-member:
  `a member left its pool with no report from the master: pool-after-eject`.
- Root cause: `adoptReportedMaster` routes any self-claim carrying a different `PoolID` to
  `adoptReinitializedPool`, which only checked single-host shape and pool-ID collision. An eject is
  the **master's** record, not the leaving host's, and nothing required it.
- Fix: while another host is the old pool's recorded master, that master's own mirrored membership
  must exist and must no longer hold this host as a member (`maintenanceMember` already treats
  `ejected` as gone, which is what `nextPoolMembership` writes). A host that is its own pool's
  master is unaffected. Convergence is automatic: the leaver retries every heartbeat and is adopted
  on the first report after the master publishes the eject.
- Regression: `internal/controller/pool_evidence_test.go:93`
  `TestAMemberCannotLeaveItsPoolWithoutTheMastersReport`, plus
  `internal/controller/pooltransfer_test.go:422` `TestEjectedHostReinitializesIntoAnAdoptedPool`
  updated to the real flow (the master reports the eject first) with its assertions unchanged.
  Command: `go test -race -count=1 -timeout 30m ./internal/controller` → `ok … 278.195s`; CI as
  BUG-0333.
- Avoid: do not allow adoption when the old master has reported no membership at all. That is the
  same fail-open as BUG-0336.

## BUG-0340 — The management-network rollback used the wall clock, so it failed exactly when needed

- Date: 2026-09-19; found by the same invariant review (finding C-3), before release. Never present
  in a published build.
- Symptom: a host cut off by its own management-network change never rolled back after a power
  cycle. With the trigger mutated back to the clock path: `a wrong RTC after a reboot disabled the
  rollback: <nil> &{… Stage:applied …}`, plus a clock step rolling a live change back early and a
  backwards step postponing the rollback.
- Root cause: both triggers compared wall-clock values — `Deadline`, and `AppliedAt` against a boot
  time derived from `time.Now() - uptime`. A host that lost its time source with the change and
  rebooted with a wrong RTC read `time.Now()` behind `AppliedAt`, so neither arm fired. That is
  precisely the case the rollback exists for.
- Fix: apply stamps `BootID` (`/proc/sys/kernel/random/boot_id`) and `AppliedUptime`
  (`/proc/uptime`) into `<state-dir>/management-network.json`. "Rebooted" is a boot-id string
  inequality, never a timestamp; "window spent" is `uptime - AppliedUptime >= window` within the
  same boot. `Deadline` survives only as the operator-facing value, `ReadManagementNetworkStatus`
  computes `seconds_remaining` the same boot-relative way so the countdown cannot disagree with the
  trigger, and `ConfirmManagementNetwork` uses the same helper so a confirm can never be accepted
  for a change recovery is about to undo. A record written without the two fields falls back to the
  previous comparisons.
- Regression: `internal/vmhost/management_network_test.go:814`
  `TestManagementNetworkRollbackIsClockIndependent` — four subtests (wrong RTC after a reboot,
  clock forward, clock backward, legacy record) with an injectable boot id and uptime, each
  observed failing with the guard reverted. Command:
  `go test -race -count=1 -timeout 30m ./internal/vmhost` → `ok nex.local/nex/internal/vmhost 12.756s`;
  CI as BUG-0333.
- Avoid: never compare a persisted timestamp against `time.Now()` to decide a management-network
  rollback. The host that needs it is the host whose clock you cannot trust.

## BUG-0341 — Applying a management-network change destroyed the bridge and cut off every guest on it

- Date: 2026-09-19; found by the same invariant review (finding C-4), before release. Never present
  in a published build.
- Symptom: applying or rolling back a management-network change dropped networking for every guest
  on the untagged default network until its domain was restarted. With `ifdown br0` re-added:
  `a command that destroys br0 ran: "ifdown br0"`.
- Root cause: apply and rollback ran `ifdown br0; ifup br0`. ifupdown's bridge hook runs
  `brctl delbr br0`; guest taps are enslaved to `br0` itself and libvirt does not re-enslave them.
  The guide's claim that "a VM using a guest VLAN bridge is not affected at all" was true, but it
  quietly omitted every guest on the management bridge.
- Fix: `bounceManagement` is gone. `applyManagementBridge(ctx, want, have)` reconfigures `br0` in
  place with `ip(8)` — address, route, hardware address and DHCP client — and moves its uplink port
  only when the management interface or VLAN changes. `want`/`have` come from one parser,
  `parseManagementBridge`, run over the rendered and the on-disk `/etc/network/interfaces`, so
  rollback works identically and is correct after a crash before the file swap. `ifdown`, `brctl`,
  `ip link del` and `ip link set br0 down` are never run while the bridge exists, and no port the
  engine did not itself enslave is ever detached. The three files are still rendered byte-for-byte,
  so the next boot is unchanged. Where the uplink must move and the interfaces file records no
  `bridge_ports`, the engine refuses at stage time and lists the attached ports rather than
  rebuilding the bridge.
- Regression: `internal/vmhost/management_network_test.go:729`
  `TestManagementNetworkNeverDestroysTheBridge` (a `br0` with two `vnet` taps, across confirm,
  deadline rollback and operator rollback, asserting no destroying command, no guest port detached
  and the new uplink enslaved) and line 780
  `TestManagementNetworkKeepsTheUplinkWhenOnlyAddressingChanges`. Command:
  `go test -race -count=1 -timeout 30m ./internal/vmhost` → `ok … 12.756s`; CI as BUG-0333.
  **Scope of that evidence:** the scratch sysfs and `PATH` fakes prove *which* commands run and
  that the destroying ones never do. They prove nothing about the kernel's reaction, and
  `TestManagementNetworkNetns` has still never executed anywhere.
- Avoid: do not "fix" a bounce by re-adding the ports afterwards. The bridge index changes and the
  guests are already gone.

## BUG-0342 — A management-network change could be confirmed on evidence that never crossed it

- Date: 2026-09-19; found by the same invariant review (finding C-2), before release. Never present
  in a published build.
- Symptom: the confirmation gate — the thing that stops an operator keeping a configuration that
  has cut the host off — accepted an observation that proved nothing: a loopback local-API call, or
  a Controller report that left over a second, unchanged NIC. With the guard removed:
  `another NIC to controller: an observation that proves nothing confirmed the change`, plus
  `the host talking to itself` and `no address at all`.
- Root cause: `RecordManagementConnectivity` filtered only on a kind whitelist and a timestamp. It
  never saw which addresses the connection actually used.
- Fix: the signature became
  `RecordManagementConnectivity(dir, kind string, at time.Time, local, peer netip.Addr)`. It records
  only when the local address is one the applied spec puts on `br0` — the spec's own address for a
  static configuration, an address `br0` currently holds for DHCP — and the peer is valid and not
  loopback. The Controller report path captures the connection's addresses through
  `httptrace.ClientTrace.GotConn`, attached only to that POST's context; the host-local API path
  uses the accepting listener socket (`http.LocalAddrContextKey`) and `r.RemoteAddr`, recording
  nothing when there is no local address. The refusal now names the path that counts.
- Regression: `internal/vmhost/management_network_test.go:917`
  `TestManagementNetworkConnectivityMustTerminateOnTheBridge` (seven accept/refuse cases including
  the DHCP lease) and `internal/localcore/management_network_test.go:51`
  `TestAuthenticatedLocalCallRecordsManagementConnectivity` (four listener/peer cases); the agent
  test `internal/agent/management_network_test.go:137`
  `TestReportRecordsManagementConnectivityAndProjectsStatus` now asserts a loopback report is **not**
  recorded and that confirm is consequently refused. Command:
  `go test -race -count=1 -timeout 30m ./internal/vmhost ./internal/localcore ./internal/agent` →
  `ok vmhost 12.756s`, `ok localcore 10.216s`, `ok agent 17.911s`; CI as BUG-0333.
  **Honest limit:** an observation that *does* count cannot be produced in-process, because every
  in-process lab runs on loopback; what makes one count is proven in `internal/vmhost` alone.
- Avoid: do not treat "the report got an answer" as proof the management path works. The answer may
  have come back over the interface you did not change.

## BUG-0343 — A management-network rollback that could not complete reported no failure

- Date: 2026-09-19; found by the same invariant review (finding C-5), before release. Never present
  in a published build.
- Symptom: `a rollback that could not complete reported no management-network check` — a record
  stuck at stage `applied` with an error was indistinguishable, in the health checks, from a
  healthy applied change waiting to be confirmed.
- Root cause: `internal/agent/run.go` emitted the `management-network` check only for a
  `rolled-back` or unreadable record. The operator-requested rollback correctly emits none, and the
  stage alone cannot tell "restoring, and failing" from "applied, fine".
- Fix: a failed check is now also emitted for `Stage == applied && LastError != ""`, with its own
  sentence `restoring the previous management network configuration has not completed: <reason>`.
- Regression: an additional block in `internal/agent/management_network_test.go:137`
  `TestReportRecordsManagementConnectivityAndProjectsStatus`, using a record with
  `RequestedRollback: true` and an error still at `applied`. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 17.911s`; CI as BUG-0333.
- Avoid: do not report the operator's own successful rollback as a failure. An undo that was asked
  for is an outcome.

## BUG-0344 — A management-network spec was never checked against the host it was for

- Date: 2026-09-19; found by the same invariant review (finding C-12, with P-2), before release.
  Never present in a published build.
- Symptom: a spec naming an interface the host does not have, or the wrong card, or an interface a
  registered pool VLAN network is using, was staged and only failed at apply — with the rollback
  window as the safety net. The engine's own comment asserted the spec "names interfaces that
  already exist on the host"; nothing enforced it.
- Root cause: validation checked the spec's *shape*, never the machine.
- Fix: `checkManagementSpecAgainstHost` runs at stage time and reads
  `/sys/class/net/<name>/address` for every NIC in the spec —
  `interface <n> does not exist on this host`, `interface <n> has hardware address <a> on this
  host, not <b>; the spec names a different card`. `br0` is refused as a NIC name by the validator,
  because it would render two stanzas for one device. `guestVLANUplinks()` reads the host's own
  `/etc/network/interfaces.d/nex-*` stanzas — the durable record of a registered guest VLAN — and
  refuses a spec that disables such an interface or makes the management interface share its port.
  A new uplink already enslaved to another bridge is refused too.
- Regression: added cases in `internal/vmhost/management_network_test.go`, each observed failing
  with the guard removed. Command: `go test -race -count=1 -timeout 30m ./internal/vmhost` →
  `ok … 12.756s`; CI as BUG-0333.
- Known gap left open, deliberately: a spec that simply **omits** a NIC carrying guest VLANs is not
  refused — it loses its `iface … inet manual` stanza and the raw device is unconfigured at the
  next boot. Whether an omitted NIC means "leave alone" or "disabled" is a product decision and is
  recorded as open in the closure plan and in the guide.
- Avoid: do not rely on the rollback window as validation. The window exists for the things that
  cannot be checked in advance, like the switch port.

## BUG-0345 — Re-staging a change ID with a different configuration silently replayed the old one

- Date: 2026-09-19; found by the same invariant review (finding P-9), before release. Never present
  in a published build.
- Symptom: `stage` with the same `operation_id` and a **different** spec returned the record of the
  change already staged, so the operator was shown, and could apply, a configuration they had
  replaced.
- Root cause: stage was idempotent on the operation ID alone.
- Fix: an exact retry still replays; a `reflect.DeepEqual`-different spec is refused with
  `management network change <id> is already staged with a different configuration: management
  network conflict` — a conflict, so the Controller marks it as one and the page reloads.
- Regression: `internal/vmhost/management_network_test.go:1068`
  `TestManagementNetworkStageReplayWithADifferentSpecIsAConflict`. Command:
  `go test -race -count=1 -timeout 30m ./internal/vmhost` → `ok … 12.756s`; CI as BUG-0333.
- Avoid: do not widen the idempotency key to "same operation, latest spec wins". A stage that
  silently retargets is how an operator applies something they never reviewed.

## BUG-0346 — Maintenance mode did not block starting a local-storage guest at all

- Date: 2026-09-19; found by the same invariant review (finding C-8, with P-5), before release.
  Never present in a published build.
- Symptom: drain a host, start a virtual machine on its own **local** storage from Nex Controller,
  and it started: `a drained host admitted a local-storage start: <nil>`. Resuming a suspended
  guest on a drained host worked too: `a drained host woke a suspended guest: <nil>`. This is the
  commonest guest shape there is, so the feature did not do its job.
- Root cause: `AdmitPoolVMExecution` (`internal/agent/pool_vm.go:965-971`) returns a no-op callback
  for a non-shared, non-definition command **before any master contact**, and all five maintenance
  guards sat downstream of that round trip — while the neighbouring *restriction* guard was
  correctly placed before the same early return. No executing test drove the path.
- Fix: a new `poolVMStartsExecution(cmd)` (start, wake, and create with `StartAfterCreate`) plus
  `poolHostInMaintenance(s, s.State.HostID)` at the same choke point as `requirePoolStartPath`, i.e.
  before the early return. `wake` was also added to the pre-lock check in `poolVMLocalPlacement` and
  to the `reboot/force-reboot/wake/media` arm of `PoolVMHandler`, and `placement.go:53` now calls
  `poolHostInMaintenance` so the target-side preflight guard is live. Reboot and force-reboot stay
  allowed by decision — both require the guest to be `running` there already, so they are not a new
  placement — and every reducing power request, media change and stop is untouched. The host reads
  its own freshest **verified** membership copy (its own record on the master, the stored signed
  authority replica on a member), so there is no master round trip and local-only power keeps
  working with the master down.
- Regression: `internal/agent/pool_maintenance_test.go` —
  `TestMaintenanceRefusesStartingALocalStorageGuest` (line 237),
  `TestMaintenanceRefusesWakingAGuestOnADrainedHost` (line 270),
  `TestMaintenanceRefusesAMemberStartThroughThePeerHandler` (line 298),
  `TestMemberReadsItsDrainFlagFromTheAuthorityReplica` (line 338),
  `TestAutostartPassReturnsEarlyOnADrainedHost` (line 387) and
  `TestMaintenanceRefusesAnEjectedHost` (line 400). Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok nex.local/nex/internal/agent 18.225s`;
  CI as BUG-0333.
- Avoid: do not add the check after `poolSharedVM`. The early return **is** the defect, and a guard
  placed downstream of it looks correct and does nothing.

## BUG-0347 — libvirt autostart returned guests to a drained host after a reboot

- Date: 2026-09-19; found by the same invariant review (finding C-9), before release. Never present
  in a published build.
- Symptom: a drained host was rebooted and its managed guests came back up: `a drained host started
  a managed guest`, with the recorded `virsh` calls showing `autostart <id>` and never
  `--disable`. Rebooting is the workflow maintenance exists for.
- Root cause: `internal/vmhost/managed.go:5783-5786` gives a local-storage `Autostart` virtual
  machine libvirt `autostart=yes`, `ReconcilePoolVMAutostart` skipped non-shared virtual machines
  entirely, and libvirtd starts domains before Nex Core's agent runs at all.
- Fix, two layers. (1) A new `internal/vmhost/autostart.go` with
  `(*ManagedEngine).SetManagedVMAutostart(ctx, spec, disable)`; `ReconcilePoolVMAutostart` computes
  `drained`/`restricted` first and applies the flag to every managed domain before its early
  returns. The **desired** value is never rewritten — the suppression sits on top of the managed
  journal, which stays Nex's durable record, so leaving maintenance restores exactly what the
  operator asked for. (2) `poolHostDrained(stateDir)` in `internal/vmhost/storage_guard.go`, called
  from `StorageGuard` for any domain with a managed disk before any repository work. It reads only
  Nex Core's own `state.json` and `pool-peer.json`, so it holds with no Controller connected and
  fails closed on a present-but-unreadable record. It is exact on the pool **master**, including
  every single-host pool, and deliberately silent on a **member**, whose own membership copy never
  changes after join — a member's drain is enforced by layer 1.
- Regression: `internal/vmhost/storage_guard_maintenance_test.go` (new) —
  `TestPoolHostDrainedRefusesManagedStart` (line 34),
  `TestStorageGuardRefusesAManagedGuestOnADrainedHost` (line 69) and
  `TestSetManagedVMAutostartSuppressesWithoutRewritingTheDefinition` (line 84). Command:
  `go test -race -count=1 -timeout 30m ./internal/vmhost` → `ok nex.local/nex/internal/vmhost 12.841s`;
  CI as BUG-0333.
  **Untested by design of the environment:** the agent-side wiring from `Applied` to
  `SetManagedVMAutostart` needs a real libvirt, so the flag mapping and the hook refusal are each
  tested but not the two together on a host.
- Known limit, stated in the guide: a manual `virsh start` of a managed guest on a drained
  **member** is not refused by the hook; the suppressed flag is the enforcement there.
- Avoid: do not store a second "desired autostart" value anywhere to remember what to restore. The
  managed journal is the record; the suppression is a view on top of it.

## BUG-0348 — A join reported "failed" while the master already held a ready membership row

- Date: 2026-09-19; found by the same invariant review (finding C-10), before release. Never
  present in a published build.
- Symptom: an admitted host reported `Stage:joined Status:failed Error:pool master did not answer`,
  while the master's record said it was a ready member. Recovery was then shut in both directions —
  minting and redeeming each refuse an existing member — so a roughly two-minute network outage
  produced an unrecoverable split: `the join could not be completed after the reply was lost`.
- Root cause: the attempt budget that ends a join as `failed` applied to the **post-admission**
  stage as well as the pre-admission ones, and `acceptPeerJoin` refused the idempotent replay once
  the offer's TTL had passed.
- Fix: in `poolJoinStatus` (`internal/agent/pool_join.go`), stage `joined` with an exhausted budget
  is `needs_attention`, never `failed`; only `precheck` and `csr` — the stages before the master
  could have admitted the host — can end `failed`. In `acceptPeerJoin` (`internal/agent/peer.go`)
  the TTL now bounds only the **first** redemption: an offer that has already issued a certificate
  replays past its expiry, returning the same certificate to the same host and public key. It
  admits nothing new, because the CSR hash is bound and the member row already exists — so the
  truth stays learnable after a lost reply.
- Regression: `internal/agent/pool_join_test.go:416`
  `TestJoinNeverReportsFailedAfterTheMasterMayHaveAdmittedIt` (master admits, reply lost, record
  pinned at `joined` with the attempt budget exhausted and every offer expired; asserts not
  `failed`, then that `advancePoolJoin` completes to `ready`). Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 18.225s`; CI as BUG-0333. This is
  also what now makes the guide's sentence *"Join failed — the join stopped before any membership
  change"* true rather than merely intended.
- Avoid: do not "solve" it by widening the invitation TTL. The replay, not the clock, is what makes
  it recoverable.

## BUG-0349 — The host-local join path destroyed a Controller-driven join record

- Date: 2026-09-19; found by the same invariant review (finding P-7), before release. Never present
  in a published build.
- Symptom: `the host-local path discarded the Controller-driven join record: {OperationID: Stage:ready …}`
  — the operation ID was lost, so the Controller's operation could never be settled by the host.
- Root cause: `CompletePoolJoinStages` overwrote the stored record unconditionally.
- Fix: it now loads and **merges**. An in-progress record (not `succeeded`/`failed`) keeps its
  `OperationID`, is advanced to at least `rehomed` through a new `poolJoinStageIndex`, has its
  invitation and error cleared, and takes the new peer origin if one was given. Only an absent or
  terminal record is replaced.
- Regression: `internal/agent/pool_join_test.go:503`
  `TestCompletePoolJoinStagesKeepsAnExistingJoinRecord`. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 18.225s`; CI as BUG-0333.
- Avoid: do not treat the two join entry points as separate state machines. There is one durable
  record.

## BUG-0350 — The join invitation stayed at rest on the joining host after the join completed

- Date: 2026-09-19; found by the same invariant review (finding P-4), before release. Never present
  in a published build.
- Symptom: `the redeemed invitation is still stored on the joining host` — the single-use token
  remained in `pool-peer.json` indefinitely after it had been redeemed.
- Root cause: nothing cleared it; resume logic kept the whole pending record.
- Fix: `advancePoolJoin` clears `pending.Invitation` the moment the `joined` stage completes, and
  `CompletePoolJoinStages` clears it on merge. Resuming from any later stage needs only `MasterURL`
  and `CAPEM`.
- Regression: `internal/agent/pool_join_test.go:478` `TestJoinInvitationIsScrubbedOnceRedeemed`,
  which also greps the `pool-peer.json` bytes. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 18.225s`; CI as BUG-0333.
- Not done, deliberately: the agent journal entry for the `pool-join` command still contains the
  invitation. `applyResponse` rejects the whole report with *"controller reused command ID with
  different input"* on any byte difference between the journaled and a redelivered command, so
  scrubbing the host's copy while the Controller may still redeliver an unscrubbed one would break
  reporting. Recorded as open in the closure plan.
- Avoid: do not scrub the journal copy on the host alone.

## BUG-0351 — A clustered-storage apply could move the pool's records during an authority freeze

- Date: 2026-09-19; found while fixing BUG-0334 (the replicated-section write audit), before
  release. Never present in a published build.
- Symptom: `a frozen master answered a clustered-storage apply with {… Status:failed Error:cluster
  credentials are unavailable}` — that is, it passed the (absent) guard and entered the apply path
  while the authority was frozen for a master change.
- Root cause: `ExecuteStorageClusterRequest` (`internal/agent/storage_cluster.go`) had no authority
  guard, although `storage_clusters` is a replicated section like every registry kind.
- Fix: `requirePoolAuthorityWritable(s)` at the top of the function for every action but `status`,
  returning the existing retryable sentence `pool authority is frozen for a master change; retry
  when it completes`. `status` is a read and stays available. This is the entry point, not the
  resume, so no native operation in flight is interrupted.
- Regression: the `storage_clusters/apply` case in the table-driven
  `internal/agent/pool_authority_freeze_test.go:47` `TestFrozenAuthorityContentDigestNeverMoves`,
  asserting the frozen sentence for both write actions and that `status` still answers. Command:
  `go test -race -count=1 -timeout 30m ./internal/agent` → `ok … 18.225s`; CI as BUG-0333.
- Avoid: do not guard the resume instead of the entry point — that interrupts work already running
  on the host.

## BUG-0352 — The promote route could not drive a current Nex Core, and a successful cancel read as a failure

- Date: 2026-09-19; found by cross-layer review while closing the fence-consent work of BUG-0333,
  before release. Never present in a published build.
- Symptom: Nex Core refuses a `pool-promote` command carrying no confirmed fence set, and the
  Controller route neither accepted nor forwarded one. Reproduced end to end:
  `POST /api/v1/pools/…/master/promote: got 400 want 202: {"error":"invalid JSON request"}` (the
  route rejected `fence_host_ids` outright) and `got 202 want 400` (a promotion with no list was
  accepted and would then have been refused by Core). Separately, a cancellation Nex Core actually
  **performed** arrived as `status: "succeeded"` with its sentence in a field no client read, so the
  interface rendered a completed cancellation as a failed transfer; and `poolMemberFence` rebuilt
  three fields and dropped `fence_credential_version`, so an operator could not see whether rotated
  fence material had been replicated.
- Root cause: the multi-host fence landed on the Core side after the Controller and interface lanes
  had finished their slices.
- Fix: `PoolPromoteRequest` gains a **required**, non-empty `fence_host_ids`, normalised (sorted,
  blanks and duplicates refused) so the same set typed in any order is a replay; the Controller
  computes and corrects nothing and forwards it verbatim, refusing only a host its own mirror places
  in another pool. The cancel route gains optional `fence_confirmed` + `fence_host_ids`, accepted
  both or neither and forwarded verbatim; neither is accepted on the master-change or eject route,
  which now have their own body structs. `poolTransferOperation` gains `reason`, carrying Core's
  sentence on a succeeded operation with `error` empty. `poolMemberFence` projects
  `credential_version`.
- Regression: `internal/controller/poolpromotion_e2e_test.go` (new) —
  `TestPromotionFenceConsentReachesCoreEndToEnd` (line 47), which drives preflight → promote through
  the real handlers, takes the exact queued `protocol.Command` and executes it with Core's own
  exported `agent.ExecutePoolPromotion` against a real `agent.OpenStore` directory, asserting the
  command is not refused for missing consent and that the same command with the list removed **is**
  refused with Core's exact sentence — so the test fails if either side moves the gate; and
  `TestPromoteFenceHostRefusals` (line 113). Plus a new cancel test in
  `internal/controller/pooltransfer_test.go`. Command:
  `go test -race -count=1 -timeout 30m ./internal/controller` → `ok nex.local/nex/internal/controller 285.920s`.
  Contract coverage: `tests/test_api_contract.py::PoolPromotionContractTests` and
  `PoolTransferContractTests`, run by `python3 -m unittest discover -s tests -p 'test_*.py'` at line
  53 of both workflow files; UI coverage `web/src/pool-promotion.test.mjs`,
  `pool-authority.test.mjs` and `pool-members.test.mjs`, run by `npm --prefix web run test` at line
  70 of both workflow files. CI otherwise as BUG-0333.
- Avoid: do not let the Controller fill in a fence set it computed itself, and do not have a client
  infer a performed cancellation from the absence of an error.

## BUG-0353 — The resume offer keyed on a sentence, and the rollback timer died with the agent

- Date: 2026-09-19; found by review while closing BUG-0335 and BUG-0340 (review finding P-1),
  before release. Never present in a published build.
- Symptom: two small defects in the same final batch. (1) Nothing on the wire said a master change
  had been handed over: `HandedOff` existed only on the peer probe, not on the reported
  `PoolTransferStatus`, so the interface had to decide whether to offer **Resume on the current
  master…** by matching the text of one of Nex Core's refusal sentences. A wording change would have
  silently removed the only recovery from a dead hand-off target. (2) Both systemd units were
  `Restart=on-failure`, so an out-of-memory kill or any exit the unit treats as clean left nothing
  armed to perform a management-network rollback until someone started the service by hand — and
  every rollback trigger runs inside that process.
- Root cause: (1) the field was never projected; (2) the unit files predate the staged
  management-network work and its rollback window.
- Fix: (1) a new always-present `handed_off` boolean on `PoolTransferStatus` — like
  `cancellable` — filled from the durable record in `poolTransferStatus` and projected verbatim by
  the existing Controller view; the interface gate is now structural (`role === "source" &&
  handed_off`, plus the target row not being fresh). An older Nex Core that omits the field offers
  nothing, which is the safe direction. (2) both units are `Restart=always`, with a comment naming
  the rollback window. Checked before changing it: nothing in the repository relies on a clean exit
  staying down — `packaging/core-local/prerm` uses `deb-systemd-invoke stop` and
  `Conflicts=nex-core-agent.service` are stop jobs, which `Restart=always` does not affect.
- Regression: `internal/protocol/pool_test.go` (wire presence: `false` is emitted, `true` reaches
  the wire), the stage matrix in `internal/agent/pool_transfer_test.go` asserting reported
  `handed_off` per case, and `internal/controller/pool_reported_fields_test.go` asserting
  pass-through on the master's `transfer` (true) and the target's row (present, false). Units:
  `tests/test_agent_service.py:37` and `tests/test_core_local_package.py:404`, each
  `self.assertEqual(settings.get('Restart'), 'always')`. Commands:
  `go test -race -count=1 -timeout 40m ./...` → all packages ok (controller 291.025s), and
  `python3 -m unittest discover -s tests -p 'test_*.py'` → 373 tests. Both run in CI at
  `phase1.yml:29` and `phase1.yml:53` in `.github/workflows/` and `.gitea/workflows/`. UI:
  `web/src/pool-authority.test.mjs` and `pool-promotion.test.mjs` at line 70 of both files.
  `docs/qa/fresh-install/installed-host.txt:41` is a **historical capture** showing the old
  `Restart=on-failure` line and is deliberately left as recorded past evidence.
- Avoid: do not gate an interface affordance on the text of a server message, and do not assume a
  deliberate `systemctl stop` is covered by `Restart=always` — the stopped and masked cases remain
  open and are stated in the guide and the plan.

## BUG-0354 — A removed ISO blocked its repository's detach for ever

- Date: 2026-09-19; found by the S7 native acceptance lab before release.
- Symptom: a removed ISO still made `storage-detach` fail as in use.
- Root cause: the storage dependency check read succeeded imports without excluding media removal
  tombstones.
- Fix: one dependency predicate now serves both detach and its dry run, and ignores removed media.
- Regression: `TestRemovedISOIsNotAStorageDependency` and
  `TestCheckStorageDetachReportsTheDependencyWithoutJournalling` in
  `internal/vmhost/storage_lifecycle_test.go`; the reported pre-fix test failed and the S7 Core
  batch recorded `go test -race -count=1 -timeout 40m ./...` passing. Native rerun is open.
- API impact: none.
- Avoid: do not infer removal from a missing file; use the durable tombstone.

## BUG-0355 — A host with a detached remote repository could never join a pool

- Date: 2026-09-19; found by the S7 native acceptance lab before release.
- Symptom: Core refused a host whose only remote repository was already detached, while the
  Controller preview called it eligible.
- Root cause: a detach tombstone has no storage kind and was counted as remote storage by Core.
- Fix: Controller and Core now call `StorageBlocksPoolJoin`, which excludes tombstones.
- Regression: `TestPoolJoinPrecheckIgnoresADetachedRepository` in
  `internal/agent/pool_join_test.go`; the reported pre-fix test failed and the S7 Core batch
  recorded `go test -race -count=1 -timeout 40m ./...` passing. Native rerun is open.
- API impact: none.
- Avoid: do not duplicate the tombstone rule at each caller.

## BUG-0356 — An ejected member could remain stranded

- Date: 2026-09-19; found twice by the S7 native acceptance lab before release.
- Symptom: the master marked membership ejected while the host retained pool resources and could
  not rejoin.
- Root cause: leave replayed a stored detach failure, detach treated removed media as live, and the
  join-only rehome path refused normal leave records; its orphan loop could also grow the journal.
- Fix: leave removes adopted ISO metadata, repositories and VLANs in dependency order with dry-run
  checks and bounded retry IDs; a leave-specific rehome retains local VMs, and the master rejects
  an eject that would strand a local VM using a pool VLAN or ISO.
- Regression: `internal/agent/pool_leave_test.go` covers release order, named blockers, bounded
  journal growth, stranded-state recovery and the eject precheck. The reported pre-fix tests failed;
  the S7 Core batch recorded `go test -race -count=1 -timeout 40m ./...` passing. The lab host has
  not been upgraded and rerun.
- API impact: none.
- Avoid: master completion means membership removal only; use the ejected host's transfer report.

## BUG-0357 — A member's local repository retained its pre-join pool scope

- Date: 2026-09-19; found by the S7 native acceptance lab before release.
- Symptom: VM creation on a joined member's local storage failed because its retained projections
  still named the old pool.
- Root cause: rehome changed only command copies and stopped at workload entries, while projections
  read applied storage, lifecycle and downloaded-media copies.
- Fix: one idempotent pass rehomes every retained scope-bearing copy and skips workload entries;
  removal tombstones stay in the old pool.
- Regression: `TestRehome` in `internal/vmhost/pool_test.go` failed before and passed after.
  The reported full Go race run had one unrelated timing flake, which passed on rerun; native
  confirmation remains open.
- API impact: none.
- Avoid: do not change projection preference or rehome removal tombstones.

## BUG-0358 — An ejected membership row prevented shared VM ownership from settling

- Date: 2026-09-19; found by the S7 native acceptance lab before release.
- Symptom: any ejected row left a stopped shared VM recorded running and future starts refused.
- Root cause: inventory completeness required every retained membership row to be ready.
- Fix: completeness excludes only ejected rows; pending and failed rows still block, and obsolete
  snapshots are pruned.
- Regression: `TestPoolVMInventory*` and `TestPoolEject*` in
  `internal/agent/pool_vm_inventory_test.go` failed before and passed after. The reported full Go
  race run had one unrelated timing flake, which passed on rerun; native confirmation remains open.
- API impact: none.
- Avoid: do not relax the rule for pending or failed members.

## BUG-0359 — The real-ifupdown test used an obsolete rollback trigger

- Date: 2026-09-19; found on its first lab execution before release.
- Symptom: the test moved the old wall-clock deadline but the boot-relative rollback correctly did
  nothing.
- Root cause: a record with a boot ID uses elapsed uptime, not the deadline.
- Fix: the test now moves applied uptime past the window and retains deadline only as the fallback.
- Regression: it skips cleanly where `ifupdown` is unavailable; the corrected native re-run is
  outstanding.
- API impact: none.
- Avoid: do not reintroduce wall-clock triggering to make the test pass.

## BUG-0360 — A DHCP management-network change could remove resolver configuration

- Date: 2026-09-19; found during the S7 management-network work before release.
- Symptom: confirming a DHCP change with no DNS override could delete `/etc/resolv.conf`, leaving
  the host unable to resolve names after a supported transition.
- Root cause: the DHCP render used a nil resolver file and the common writer interpreted nil as a
  request to remove the snapshotted file before the DHCP hook had replaced it.
- Fix: apply retains the existing resolver configuration when DHCP has no DNS override; the DHCP
  hook can replace it, and rollback still restores a genuinely absent prior file.
- Regression: `TestManagementNetworkDHCPKeepsExistingResolvConf` in
  `internal/vmhost/management_network_test.go` failed before and passed after; the reported
  `go test -race -count=1 -timeout 20m ./internal/vmhost` passed. Native acceptance remains open.
- API impact: existing contract.
- Avoid: do not delete the resolver file before proving the DHCP hook will replace it.

## BUG-0361 — Omitted optional management-network revision was treated as zero

- Date: 2026-09-19; found during the S7 management-network work before release.
- Symptom: a client omitting documented-optional `expected_revision` could not stage a second
  change after a first confirmed change.
- Root cause: Core compared the omitted zero value with the current revision while Controller
  already treated zero as no optimistic-concurrency condition.
- Fix: Core compares expected revision only when it is nonzero.
- Regression: `TestManagementNetworkStageApplyConfirm` in
  `internal/vmhost/management_network_test.go` failed before and passed after; the reported
  `go test -race -count=1 -timeout 20m ./internal/vmhost` passed. Native acceptance remains open.
- API impact: existing contract.
- Avoid: do not treat a zero value from an optional field as an explicit revision.

## BUG-0362 — Adopted media kept a shared-storage reservation after convergence

- Date: 2026-09-19; found during S7 acceptance before release.
- Symptom: an otherwise converged member could not be ejected because adopted ISO metadata kept a
  storage reservation. The correction initially missed an attached drivers ISO, which was found
  and fixed before the final review.
- Fix: reservation release excludes adopted media while retaining imported, installation and
  drivers-media references.
- Regression: reported failing-first reservation-selection and authenticated peer release tests;
  final whole-tree race evidence is pending.
- API impact: existing contract.

## BUG-0363 — An ejected host could not rejoin its former pool

- Date: 2026-09-19; found during S7 acceptance before release.
- Symptom: an ejected host was refused as already a member by Controller and Core.
- Fix: only the historical `ejected` row can be replaced by a newly certified ready membership;
  active members remain refused. Controller also reprojects a retained local VM under the host's
  new pool.
- Regression: reported failing-first peer and Controller rejoin tests passed under focused race
  suites; an initial native same-identity rejoin is superseded by the later media correction.
- API impact: existing contract.

## BUG-0364 — Forward-only master-change cancellation named an eject

- Date: 2026-09-19; found during S7 acceptance before release.
- Symptom: an irreversible master-change cancel could return an eject sentence, or reject a
  demoted source or activated target before reading its durable receipt.
- Root cause: the first helper-only correction did not reach the ordinary command path: stale
  expected-version validation ran before the activated target or demoted source receipt was read.
- Fix: `ExecutePoolTransfer` now reads that irreversible receipt before ordinary version and role
  validation, then returns the documented forward-only master-change result from either participant.
- Regression: the final narrow guard applies only to activated targets and demoted sources, leaving
  ordinary cancellation version validation intact. Independent review cleared focused command,
  guard and candidate tests. The final whole-tree race result remains pending.
- API impact: existing contract.

## BUG-0365 — Eject could mishandle local VMs or hide a shared VM behind stale inventory

- Date: 2026-09-19; independent review finding during S7 acceptance before release.
- Symptom: generic old-pool records blocked verified local-only VMs, while absent or stale target
  inventory could hide a shared VM.
- Fix: eject requires current target inventory, retains every shared or unverifiable record, and
  retires only verified local-only ownership after authority freeze before rehoming that VM journal.
- Regression: reported stale-inventory and local-VM eject lifecycle tests passed; final whole-tree
  race and review recheck are pending.
- API impact: existing contract.

## BUG-0366 — A member ZFS tombstone left a stale Controller projection

- Date: 2026-09-19; found during S7 acceptance before release.
- Symptom: a member-reported ZFS detach tombstone did not clear its host-scoped mirror row.
- Fix: tombstones reach existing mirror cleanup while live master-owned records remain protected.
- Regression: `TestMemberZFSTombstoneDropsItsProjection` failed first and passed in the reported
  focused Controller race run; final whole-tree race evidence is pending.
- API impact: existing contract.

## BUG-0367 — Disabled pool transfer actions did not expose their shared block reason

- Date: 2026-09-19; found during Phase 4 UI review before release.
- Symptom: while another transfer blocked pool actions, **Make pool master** and **Eject from
  pool** were disabled without an accessible association to the displayed reason.
- Fix: each row action now has an instance-unique `useId` description for its own disabled reason.
  When a pool-wide transfer also blocks it, `aria-describedby` references both that local reason
  and the existing `pool-actions-blocked` status.
- Regression: `web/src/pool-members.test.mjs` and `web/src/pool-authority.test.mjs` assert both
  associations. Independent review cleared the tests; the reported focused test, typecheck and
  full web suite passed. Browser recapture after UI deployment is still open.
- API impact: existing contract.
- Avoid: do not duplicate the transfer reason in every row; one status and stable association
  preserves the single source of truth.

## BUG-0368 — A deleted VM could reappear after eject and same-pool rejoin

- Date: 2026-09-20; found during the Phase 4 native acceptance campaign before release.
- Symptom: after a host was ejected and rejoined its former pool, a VM deleted in that old pool
  could remain as a stale owner record because the host no longer published its delete tombstone.
- Root cause: leaving rehomed a succeeded `vm-delete` tombstone into the temporary single-host
  pool, although that tombstone is history of the old pool.
- Fix: leave retains a successful delete tombstone in its old pool scope while rehoming a live
  local VM definition; the rejoined host then publishes the deletion and the master prunes the
  stale owner record only from complete current inventory.
- Regression: `TestRehomeLeavingPoolScopeRehomesAppliedCopies` and
  `TestPoolVMInventoryPublishesOldPoolDeleteAfterEjectAndSamePoolRejoin` pass. The reports do
  not retain a failing-first command result for this follow-up. Independent review cleared focused,
  package and race checks. A clean native replay is
  pending; the existing lab state lacks the required provenance and will be archived and reset as
  an unreleased-fixture replay, not repaired in place.
- Native scope: the reset final campaign first refused eject until current target inventory matched
  the retained VM/spec, then ejected successfully while the same host, VM and running QEMU process
  remained. The VM was stopped and deleted only in its solo pool. This proves retained-local eject
  and solo deletion lifecycle; it does not by itself prove an old-pool tombstone-prune result.
- API impact: existing contract.
- Avoid: do not retarget a deletion tombstone merely because the host changes pools.

## BUG-0369 — Same-pool rejoin could leave shared resources permanently removed

- Date: 2026-09-20; found during the Phase 4 native acceptance campaign before release.
- Symptom: after eject and same-pool rejoin, a host could require manual remove-and-readd of a
  shared network, repository or ISO that its leave had removed locally.
- Root cause: a normal local removal and an eject-created cleanup removal were indistinguishable
  in the durable managed journal, so reapply correctly failed closed for both.
- Fix: a signed leave record marks only the successful eject-created cleanup entry. Reconciliation
  may reapply the same ID only with a newer membership generation and a current-enough desired
  resource generation; its successful journal entry consumes that one receipt. Unmarked or newer
  removals remain effective, and interrupted reapply retries through the existing lifecycle.
- Regression: `TestEjectCleanupReapplyConsumesOnlyTheMarkedRemoval` passes. The report does not
  retain a failing-first command result for this follow-up. Independent review cleared focused,
  package and race checks. A clean native replay is pending;
  the existing lab state will be archived and reset as an unreleased-fixture replay rather than
  repaired in place.
- Native scope: after same-ID Controller-driven rejoin reached ready, the original shared network,
  repository and ISO IDs reapplied on the returning member. This proves the same-ID convergence
  path; it does not close unrelated pool recovery or promotion cases.
- API impact: existing contract.
- Avoid: do not infer an eject cleanup grant from a removal, journal filename or operation ID.

## BUG-0370 — VM creation could offer a drained or unknown host as a placement target

- Date: 2026-09-20; found in Phase 4 UI review before release.
- Symptom: the VM form used host inventory alone, so it could offer a fresh host before it had the
  pool master's current maintenance state.
- Root cause: the form had not read the existing authoritative pool-members projection while open.
- Fix: the form reads that existing projection only while the VM form is open, clears it while
  loading, and disables placement for maintenance, missing, stale or failed reports. `readyHost`
  and non-placement resource eligibility remain unchanged; Core still makes the final decision.
- Regression: `web/src/vm-create.test.mjs` covers loading, maintenance, reported-ready and unknown
  state. Independent review cleared the source; reported focused tests, typecheck and full web
  suite passed. Simulated 1280 px and 360 px captures on the final UI were inspected for
  maintenance, missing, stale and unavailable reports; mutation routes were aborted. The retained
  Phase 1 browser command passed on the reviewed dirty working tree, but its new unconditional
  assertions exercise U7/U10 rather than this U6 fixture; it does not prove Core placement.
- API impact: existing contract.
- Avoid: do not infer placement authority from a host inventory observation.

## BUG-0371 — Hidden management-network labels caused narrow-page overflow

- Date: 2026-09-20; found in Phase 4 UI review before release.
- Symptom: visually hidden labels in the management-network form could place an absolute box
  outside its scroll area and widen a 360 px page.
- Root cause: the form's scroll container did not establish the positioning context for those
  labels.
- Fix: the management-network form's actual scroll container is positioned, keeping hidden labels
  available to assistive technology without contributing to document width.
- Regression: `tests/integration/phase1_browser.py` intercepts the existing host-network route for
  validation and requested rollback, asserts no 360 px overflow and captures both states. The
  isolated U7–U10 captures passed. The retained persistent Phase 1 browser command also passed on
  the reviewed dirty working tree; its unconditional assertions exercise the intercepted
  validation and requested-rollback UI states at 360 px, without a Core mutation.
- API impact: existing contract.
- Avoid: do not remove labels to suppress layout overflow.

## BUG-0372 — Accepted VM-create warning vanished on its task route

- Date: 2026-09-20; found in Phase 4 UI review before release.
- Symptom: an accepted VM creation could return a Core advisory warning, then immediately navigate
  to its task route and clear that warning before an operator could read it.
- Root cause: the hashchange handler cleared every notice, including the notice attached to the
  route it had just assigned.
- Fix: the shell records the route for a task-bound notice and preserves it through that matching
  route change. An ordinary navigation or terminal task state still clears it.
- Regression: `web/src/managed.test.mjs` covers the warning text, route guard and terminal-task
  projection. Independent review cleared isolated intercepted browser coverage at 1280 px and
  360 px: the warning survives the task-route change and clears on task completion or ordinary
  navigation. A current-source-only unconditional-clear reproduction failed at warning visibility;
  the restored fixture and persistent Phase 1 browser run then passed. This is browser fixture
  evidence, not a Core placement or native acceptance result.
- API impact: none.
- Avoid: do not clear a task-bound notice solely because the accepted request changed the route.

## BUG-0373 — A prior solo-pool VM owner could reappear after joining and promotion

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: a host's solo-pool VM ownership cache could survive joining another pool and later
  reappear through promotion, resurrecting an ownership record from the former pool.
- Root cause: the join rehome resume path cleared its durable pending-rehome marker before it had
  scrubbed old solo-pool VM ownership and inventory state.
- Fix: Core clears that old state before clearing `PendingRehome`. If cleanup fails, it retains the
  marker and uses the existing cancellation-aware retry backoff, so a repaired durable record can
  resume safely.
- Regression: the lifecycle test starts with a completed solo-pool VM-delete journal and stale
  solo-pool ownership state, then joins, resumes, receives a signed replica and completes a fenced
  promotion. It proves that only the destination authority remains and its VM record is
  byte-identical. A temporary disabled-scrub negative control failed with the stale authority
  still present. Cancellation with corrupt ownership state retains `PendingRehome`, and the retry
  completes after repair. Reported passes: focused `go test` and `go test -race` lifecycle suites,
  plus full `internal/agent` ordinary and race suites. The source test runs in the existing Go
  package coverage used by push and pull-request workflows. A clean native replay is pending; no
  already-corrupt replica was repaired in place.
- API impact: existing contract; join and resume behavior changed without a route or wire change.
- Avoid: do not retain or re-publish ownership records from a pool the host has left.

## BUG-0374 — A managed local VM could start while its host was restricted

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: a direct libvirt start of a managed VM with local storage bypassed the restricted-host
  gate, although the Core API correctly refused the same start and the recovery guide promises that
  restricted hosts start no VM.
- Root cause: the libvirt storage hook checked the restriction only inside its shared-storage
  permit branch, which local managed disks never enter.
- Fix: every managed disk now checks the existing durable Core restriction before repository work.
  Shared-storage permit consumption is unchanged, and unmanaged appliance domains remain outside
  this guard.
- Regression: `TestStorageGuardRefusesRestrictedLocalManagedGuestAndLeavesApplianceAlone` failed
  before the change and passes now; it proves a local managed VM is refused and an unmanaged
  appliance is admitted. The focused guard suite passed, as did `go test ./internal/vmhost`
  (8.602s) and `go test -race ./internal/vmhost` (12.767s). Independent review cleared the fix;
  the repository privacy test passed. Native proof on the later hook-only Core delta used the same
  valid stopped local managed VM: both the Core API and direct libvirt hook refused it while the
  host was `Restricted · unconfirmed`; it remained stopped, then started and stopped normally after
  recovery. This does not prove every start path, a bounded full restricted interval, duplicate-
  writer safety or permanently-dead-member recovery.
- API impact: existing contract; the existing Core restriction state governs existing start
  behavior without a new Controller contract.
- Avoid: do not apply the restriction gate only to shared-storage start paths.

## BUG-0375 — An expired promotion fence proof could leave activation retrying forever

- Date: 2026-09-20; found during Phase 4 acceptance before release.
- Symptom: when a fence proof expires while a promotion is at activation, retries can remain at
  that stage without returning to fencing for a fresh proof.
- Root cause: activation treated every failed live verdict as terminal, although the existing
  durable record had already advanced to activation and could no longer re-run fencing.
- Fix: only a fully bound proof set that was valid when recorded and has since expired moves the
  same durable candidate record from activation back to fencing. The retry replaces only expired
  proofs. Malformed, mismatched, unconfirmed or newly required proofs, and authority or ownership
  failures, remain refused.
- Regression: `TestPoolPromotionRefencesExpiredProofBeforeActivation` failed first with no fresh
  fence call and now passes. Three focused tests cover retaining a fresh proof while re-fencing an
  expired peer, malformed expired proof refusal, and higher-authority refusal. Focused tests,
  `go test ./internal/agent` (10.606s), and `go test -race ./internal/agent` (23.845s) passed;
  independent review cleared the fix. Native expiry validation held an aged, bound proof at
  activation while the old master was externally off, then replaced it for the same operation,
  host, target and configuration before promotion succeeded. A separate retained restart kept the
  same operation-bound fresh proof byte-identical across the candidate's new boot identity, with
  the old master still off; promotion then succeeded and cleanup returned that host unrestricted.
  Earlier incomplete fresh-proof attempts are superseded historical evidence.
- API impact: existing contract; the existing promotion status can again move from activation to
  fencing when its durable evidence expires.
- Avoid: do not retry activation with an expired fence proof or leave it in an unbounded retry
  loop.

## BUG-0376 — A historical ejection receipt could suppress a later missed-eject recovery

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: after an ejected host had rejoined, an old terminal ejection receipt could cause Core
  to ignore a later authoritative missed-eject observation, leaving the host out of convergence.
- Root cause: missed-eject recovery treated every historical terminal `ejected` role as current,
  instead of considering whether a newer authoritative membership state superseded it.
- Fix: Core preserves an active eject leave, but permits a terminal historical receipt to be
  replaced only after authenticated authority convergence reports the local caller ejected at a
  strictly newer membership generation. Existing master-certificate pinning, returned-host
  verification and newer-generation safeguards remain unchanged.
- Regression: `TestRejoinedHostLearnsLaterMissedEjectDespiteHistoricalTerminalReceipt` failed
  before the correction. It seeds the historical receipt, drives a later master eject while direct
  notice is unavailable, restores authenticated convergence, verifies replacement, and completes
  `ResumePoolLeave`. The seeded receipt is a limitation: it does not reproduce the preceding
  ejection/rejoin sequence. Package `go test ./internal/agent` (10.594s) and
  `go test -race ./internal/agent` (23.712s) passed; independent review cleared the fix. Native
  historical execution transcripts report recovery of the preserved fixture, but lack retained raw
  before/after provenance. They are not sufficient to elevate the native recovery claim; stronger
  raw evidence remains pending. A later whole-tree race passed after this correction.
- API impact: existing contract; this restores retry and recovery behavior within the existing Core
  membership lifecycle.
- Avoid: do not let a terminal receipt from an earlier membership generation suppress newer
  authoritative recovery state.

## BUG-0377 — Cleared Core checks could retain stale failure or pending status

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: a host could report `restricted: null` in fresh inventory while the pool-members view
  still showed its earlier `pool-restricted` health check as failed. The same producer pattern can
  also retain an old failed promotion check or a pending transfer check after Core has reached its
  corresponding clear or successful state.
- Root cause: Core omitted a fresh healthy replacement after a readable producer reached a clear,
  successful, terminal or not-applicable state. The Controller correctly retains omitted checks
  across partial inventory reports. The omission affected all seven fixed check producers.
- Status: source fixed and independently reviewed. The final combined whole-tree source gate and
  Python suite passed. Retained native Controller and members captures verify all seven fresh
  healthy check replacements on a fresh current master. Their contemporary API evidence is kept
  distinct from a later direct-shell observation rather than treating the two captures as one
  generation/provenance record.
- Fix and regression: a readable durable clear emits the existing `pool-restricted` check as
  healthy, fresh and observed now. An unreadable record emits no synthetic clear, and an active
  restriction keeps its existing failed fresh reason. The all-producer correction applies the
  same replacement rule to storage, network, media, authority-replica, promotion and transfer
  checks. The failing-first
  `TestPoolRestrictedCheckPublishesFreshHealthyClear` now proves failed to durably clear produces
  `restricted: null` plus an explicit healthy replacement; Controller coverage retains the
  failed-to-healthy merge and pool-members projection. Focused agent test (0.014s),
  `go test ./internal/agent` (10.693s), and `go test -race ./internal/agent` (23.838s) passed;
  independent review cleared the fix. Existing GitHub and Gitea Go race workflows cover the agent
  package. A later full `go test -race -count=1 -timeout 20m ./...` run also passed after the
  source freeze. Promotion success and transfer success now likewise emit explicit fresh healthy
  checks; a readable absent record now emits healthy, while an unreadable record emits no synthetic
  healthy replacement and a prior Controller-stored check can remain. The extension passed its package and race checks, and the final combined whole-tree source
  gate then passed. Retained native Controller and members captures show all seven current healthy
  replacements on a fresh current master; this is an observed producer outcome, not a Controller
  state repair.
- API impact: existing contract; this refreshes the status of an existing inventory
  check without adding a route or field.
- Avoid: do not make Controller infer a clear from a missing check or keep a second restriction
  state outside Core.

## BUG-0378 — A newer eject cleanup could remove resources after same-pool rejoin

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: after repeated eject cleanup at the same resource generation across later membership
  generations, rejoin could consume the older cleanup receipt. The newer removal then remained
  effective, leaving the shared network, repository and media unavailable even though the UI
  reported the host ready.
- Root cause: durable reapply provenance chose marked cleanup by resource generation alone. When
  generations tied, it could select the older membership cleanup rather than the newest applicable
  one.
- Status: source fixed, independently reviewed and natively verified for preserved-journal
  recovery. The multi-cycle regression covers newest
  membership selection, tied-generation fail-closed behavior and real media receipt projection;
  it failed under a resource-generation-only selector and passes with the correction. Focused,
  package and race checks passed. The preserved disposable fixture was not repaired in place. A
  fresh full eject and same-pool rejoin cycle remains beyond this bounded recovery proof.
- Native verification: ordinary reconciliation on the reviewed Core delta consumed the newest
  applicable cleanup provenance from the preserved journal. The shared repository mounted and the
  shared ISO reappeared; master-side repository access, media digest, API read/write, VM runtime
  and locking checks were healthy. A deliberately unmarked later network removal remained absent,
  as required. This is bounded preserved-journal recovery, not all-resource acceptance.
- API impact: existing contract; selection changes Core-private managed-journal provenance without
  a route, schema or Controller-owned record.
- Guide impact: none; this restores the documented rejoin behavior without changing its operator
  workflow.
- State: Core — existing managed-journal eject-cleanup provenance and reapply-consumption records
  remain authoritative and are reported through Core inventory to a replacement Controller.
- Avoid: do not choose receipts by journal filename or operation ID, and do not repair a
  previously damaged journal in place.

## BUG-0379 — A later eject cycle could reuse an earlier cleanup receipt

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: a second UI eject could leave a host pending when it still used shared media. Cleanup
  IDs tied only to resource generation were reused across membership generations, although the
  earlier removal had already been consumed by a prior rejoin.
- Root cause: leave-created cleanup provenance did not bind all resource IDs to the membership
  generation that created the cleanup, allowing an earlier consumption receipt to be matched again.
- Status: source fixed, independently reviewed and natively verified for the preserved repeated
  eject. A focused failing-first two-cycle regression
  drives a first cleanup and reapply, then a second signed eject through `ResumePoolLeave`; it
  proves distinct durable cleanup receipts for network, storage and media, with IDs stable across
  retries and time changes. The preserved native pending leave was resumed without repair: its
  original operation terminally completed, wrote distinct current-generation cleanup receipts for
  network, storage and media, and left all three removed while the ejected host became the sole
  ready master of its new pool with its runtime identity preserved. This does not establish a
  subsequent rejoin or resource convergence.
- API impact: existing contract; this is Core-private managed-journal identity selection with no
  route, schema or Controller-owned record.
- Guide impact: none; the visible eject and rejoin workflow is unchanged.
- State: Core — existing managed-journal cleanup and consumption receipts remain authoritative and
  are reported through Core inventory to a replacement Controller.
- Avoid: do not reuse a cleanup or consumption receipt across membership generations, and do not
  repair the preserved fixture in place.

## BUG-0380 — Reapplied VLAN could report healthy without materializing

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: after rejoin, fresh API state reported a VLAN healthy while direct native inspection
  found its device absent.
- Root cause: an hour-pinned reapply operation ID replayed a prior successful same-ID operation,
  so Core neither materialized the VLAN nor consumed the new cleanup receipt.
- Status: source fixed, independently reviewed and natively verified for the bounded preserved
  reconciliation. A retained failing-before Engine network replay
  test failed because the prior operation replayed instead of materializing a new cleanup/reapply
  attempt; the focused corrected test passes. Package and race suites for the affected Core
  packages passed. The regression covers two same-input signed network-cleanup cycles and stable
  retry identity. Its intentionally missing test NIC makes native validation fail, so it is not a
  successful real VLAN lifecycle. Shared-storage helper binding is source coverage only. The final
  whole-tree race and 374-test Python gate passed.
- Native verification: the reviewed candidate reconciled the preserved member without repair. A
  retained before/after comparison shows the expected VLAN bridge, its VLAN child and owned network
  stanza, alongside the shared repository and matching current API state. This is bounded
  preserved-reconciliation evidence only; it does not establish the active two-cycle G1 workflow.
- API impact: existing contract anticipated; the defect is in Core operation identity/replay, with
  no new route, schema or Controller-owned state proposed.
- Guide impact: none anticipated; the documented rejoin workflow is unchanged.
- State: Core — existing managed-journal reapply and cleanup-consumption records remain
  authoritative and are reported through Core inventory to a replacement Controller.
- Avoid: do not infer a materialized native VLAN from a fresh API health report alone.

## BUG-0381 — A returned master could autostart before its restriction persisted

- Date: 2026-09-20; found during Phase 4 native acceptance before release.
- Symptom: during a returned-master promotion scenario, Core API start was refused while the host
  was superseded, but its managed VM had already autostarted before Core recorded the restriction.
- Root cause: libvirt autostart could run before Core had persisted the returned master's
  restriction, while the local hook still read the preceding clear state.
- Fix: source correction records private current-boot `BootAuthority{PoolID, HostID, BootID}` in
  Core's existing pool-peer state. Before the agent resumes authority work, `StorageGuard` fails
  closed for a fence-exposed multi-member master whose current-boot confirmation is missing or
  stale. It leaves solo, member, unopted and unmanaged cases unchanged.
- Status: source fixed and independently reviewed. Focused agent (0.035s) and vmhost (0.008s),
  package vmhost (8.510s) and agent (10.441s), and race vmhost (12.881s) and agent (24.170s)
  checks passed. A labeled source-only negative control that bypassed only the new boot guard
  failed its regression (exit 1); restoring the exact fixed source passed (exit 0). This is not
  historical native evidence. The f714 whole-tree Go race gate (agent 24.311s, controller
  288.588s and vmhost 12.878s) and 374-test Python gate (13.130s) passed. Independently reviewed
  native S6-14 evidence then proved pre-publish API, installed-hook and authority-offer refusals,
  53.4-second autostart suppression, ordinary rejoin clearance, and the same VM's normal
  post-clear API start/QEMU observation and force-stop/final shutoff. This is bounded boot-window
  safety evidence, not a whole-S6 or permanent-failure result.
- API impact: existing contract — this is private Core pool-peer state and existing local start
  enforcement; no route, wire field, Controller state or job is added.
- Guide impact: updated in `docs/guide/pool-authority-and-recovery.md`. Clearing restriction
  restores the requested libvirt autostart flag, but does not retry a VM denied earlier in that
  boot; an operator may start it normally after clearance, and each boot revalidates authority.
- State: Core — existing `pool-peer.json` now includes the private current-boot authority
  confirmation alongside durable restriction and managed VM/autostart records. Core remains
  authoritative; a replacement Controller does not own or reconstruct this boot-local proof.
- Avoid: do not use the earlier scoped S6-18 result to infer this returned-master boot window is
  safe.

## BUG-0382 — Pool authority stayed writable between transfer record and freeze

- Date: 2026-09-20; found in staged Phase 4 correctness review.
- Symptom: between saving a new master-change/eject transfer record and completing freeze, joins,
  maintenance and registry edits could still mutate authority.
- Root cause: the initial transfer record was saved with `Frozen=false` until `freezePoolAuthority`
  ran.
- Fix: create the transfer record with `Frozen=true`; freeze remains idempotent.
- Status: source fixed. Regression `TestFrozenAuthorityAtTransferRecordCreation` plus
  `go test ./internal/agent/ -count=1` passed.
- API impact: existing contract; freeze timing only.
- Guide impact: none; operator steps unchanged.
- State: Core — existing pool transfer record on the master.
- Avoid: do not leave a recorded in-progress transfer writable until a later stage sets Frozen.

## BUG-0383 — Join preview blocked hosts with only failed VM creates

- Date: 2026-09-20; found in staged Phase 4 correctness review.
- Symptom: Add host to pool could refuse a candidate that Core would accept, because failed
  `vm-create` mirror rows (including `failed-vm-create:*`) counted as managed VMs.
- Root cause: `joinCandidateRefusal` counted every `vm-create` row without joining operations or
  filtering failures; storage-register similarly ignored failed ops.
- Fix: count only non-failed `vm-create` rows excluding `failed-vm-create:%`; storage-register
  skips failed operations before `StorageBlocksPoolJoin`.
- Status: source fixed with controller join tests; `go test ./internal/controller/ -count=1`
  passed.
- API impact: existing contract; stricter preview alignment with Core.
- Guide impact: none.
- State: none — Controller mirror projection only.
- Avoid: do not treat failed or retitled create rows as applied workloads.

## BUG-0384 — Cross-site pool join was accepted by the API

- Date: 2026-09-20; found in staged Phase 4 correctness review.
- Symptom: POST join could attempt a join the join-candidates UI never offered, across sites.
- Root cause: `poolJoinTargets` did not require matching `site_id`.
- Fix: require joining host pool and target pool share `site_id`.
- Status: source fixed with controller test coverage.
- API impact: existing contract; stricter validation on the join route.
- Guide impact: none; same-site join was already the documented workflow.
- State: none — Controller routing only.
- Avoid: do not rely on the UI alone to enforce site scope.

## BUG-0385 — Every failed promotion was projected as a conflict

- Date: 2026-09-20; found in staged Phase 4 correctness review.
- Symptom: UI always told operators to resolve and retry after any failed promotion.
- Root cause: `lastPoolPromotionOperation` set `Conflict=true` for every `failed` status.
- Fix: set conflict only for membership-generation / fence-list mismatch refusals, matching
  transfer conflict detection.
- Status: source fixed with promotion tests.
- API impact: existing contract; projection field semantics tightened.
- Guide impact: none.
- State: none — Controller projection of Core operation results.
- Avoid: do not treat every Core refusal as a generation conflict.

## BUG-0386 — Stale membership generation and restricted banner

- Date: 2026-09-20; found in staged Phase 4 correctness review.
- Symptom: maintenance/transfer could submit `expected_version: 0` and skip the generation gate;
  a cleared restriction could leave a host banner until navigation.
- Root cause: UI submitted before authoritative generation was known; restricted banner fetched
  once on mount.
- Fix: disable submit until authoritative generation > 0; re-fetch restricted status on focus
  and while still restricted.
- Status: source fixed; web unit suites passed.
- API impact: none; client gating only.
- Guide impact: none; same actions, safer submit.
- State: none.
- Avoid: do not send expected_version 0 as a way to skip freshness.

## BUG-0387 — Maintenance drain lagged on members and missed the libvirt hook

- Date: 2026-09-20; found in Phase 4 correctness follow-up.
- Symptom: after drain on the master, members could still accept starts until the next replica
  probe; the libvirt hook only read pool-peer.json, which members never update after join.
- Root cause: ExecutePoolMaintenance did not push replicas; poolHostDrained ignored verified
  pool-replica.json on members.
- Fix: push replicas after a durable maintenance change (needs_attention if push fails); hook
  reads verified replica membership for non-masters.
- Status: source fixed with maintenance and storage_guard regression tests; agent and vmhost
  packages passed.
- API impact: existing contract.
- Guide impact: none at fix time; drain behaviour already documented, now converges promptly.
- State: Core — master membership + replica.
- Avoid: do not enforce member drain from join-time peer membership alone.
- Docs (2026-09-20): guide and API reference caught up after native lab proof (Core binary sha256
  327b7ec9…) that a drained member refuses manual `virsh start` with the hook sentence; the prior
  member hook limit is closed in the guide, API and Phase 4 closure plan.

## BUG-0388 — Replica push short-circuited on member GET alone

- Date: 2026-09-20; found in Phase 4 correctness follow-up.
- Symptom: master skipped POST when GET reported matching generation/digest/verified, so a
  member without a durable wrap after corruption/rotation might never be re-pushed.
- Root cause: GET observation and successful POST ack shared one map; GET could justify skip.
- Fix: separate POST-ack map; short-circuit only on master's last successful POST ack.
- Status: source fixed with TestPoolReplicaPushShortCircuitsOnPostAckOnly.
- API impact: existing contract.
- Guide impact: none.
- State: Core — in-memory push ack; durable replica still on members.
- Avoid: do not trust peer GET alone to skip a replica POST.

## BUG-0389 — Join invitation rejected on Controller clock skew

- Date: 2026-09-20; found in Phase 4 correctness follow-up.
- Symptom: a still-valid invitation from the master could be dropped if Controller time was ahead.
- Root cause: ExpiresAt.After(time.Now()) with no skew grace.
- Fix: 2-minute collectable skew; Core still enforces real expiry on join.
- Status: source fixed with controller skew test.
- API impact: existing contract; looser Controller collectability only.
- Guide impact: none.
- State: none — Controller relay validation.
- Avoid: do not treat Controller wall clock as the invitation's authority.

## BUG-0390 — Returned master stayed restricted without naming unreachable peers

- Date: 2026-09-20; found in Phase 4 correctness follow-up (OD-6 territory).
- Symptom: one unreachable ready peer left a restarted master restricted indefinitely with no
  clear who blocked clearance; silence was correctly not treated as agreement.
- Root cause: confirmPoolAuthorityAfterRestart returned early with no unreachable list and no
  fence-evidence escape matching promotion discipline.
- Fix: record unreachable hosts in the restriction reason; clear only when every unreachable
  ready peer is already covered by a fresh verified fence proof on the durable transfer record
  and reachable peers confirm. No auto-fence on tick; OD-6 not loosened to clear on silence.
- Status: source fixed with promote restriction tests.
- API impact: existing contract; Error text only.
- Guide impact: none for this batch; OD-6 still unapproved as product policy.
- State: Core — pool-peer restriction + transfer fences.
- Avoid: do not clear unconfirmed authority because a peer is merely quiet.

## BUG-0391 — Failed master activation could leave adopted VM ownership

- Date: 2026-09-20; found in Phase 4 correctness follow-up.
- Symptom: adoptPoolAuthorityFromReplica wrote pool-vms.json before the atomic master peer save;
  a failed peer save left adopted ownership without master role.
- Root cause: intentional adopt-before-master ordering without rollback on peer-save failure.
- Fix: snapshot pool-vms before adopt; restore on peer-save failure. HandedOff-before-activate
  ordering kept (dual-writer rule).
- Status: source fixed with adopt rollback and HandedOff recovery-message tests.
- API impact: existing contract.
- Guide impact: none.
- State: Core — pool-vms.json + pool-peer.json.
- Avoid: do not leave adopted VM authority when master activation did not commit.

## BUG-0392 — A local-storage guest never came back from a pool master's reboot

- Date: 2026-09-20; found in Phase 4 native lab acceptance (S2-4, `s2-autostart`), where the
  master's local-storage guest stayed `shut off` for 303.6s after a reboot taken with maintenance
  already cleared and the libvirt flag restored to `enable`.
- Symptom: on a multi-host pool master, a managed guest on local storage with `autostart` set did
  not return after a normal (not drained, not restricted) reboot. A manual `virsh start` once the
  agent was up succeeded, so the definition and the flag were both correct.
- Root cause: libvirt attempts an autostart domain exactly once, at boot, before Core's agent
  runs. For a fence-exposed master with other ready members that is inside the window
  `EnsurePoolBootRestriction` opened, so `StorageGuard`'s `poolHostRestricted` refused the start —
  correctly. Nothing then retried it: `ReconcilePoolVMAutostart` skipped every non-shared VM
  (`if !shared { continue }`) on the grounds that libvirt owns those, which is only true for the
  one attempt libvirt has already spent.
- Fix: `ReconcilePoolVMAutostart` now takes the same once-per-boot attempt for a local-storage
  autostart guest, after — and only after — its existing drain and restriction gates have passed.
  The attempt is claimed by `ClaimPoolVMAutostartAttempt` exactly as the shared path claims it, so
  an agent restart or a later manual stop cannot start the guest again in that boot. It takes no
  master permit and no ownership transition, which is what an API start of a local-storage guest
  does too (`AdmitPoolVMExecution` returns before any master round trip for one). Nothing in the
  boot window changed: BUG-0381's refusal, and the `StorageGuard` gates behind it, are untouched.
- Status: source fixed. Regressions `TestAutostartPassAttemptsLocalStorageGuests` (asserts Core
  asks libvirt to start the guest, through a recording `virsh` stub) and
  `TestAutostartPassMakesNoLocalAttemptWhileDrainedOrRestricted` (asserts no attempt is made and
  no boot claim spent in either window). Negative control: with `startLocalAutostartVM` reverted
  to `continue`, the first test failed with `Core never asked libvirt to start the local-storage
  autostart guest: ["list --all --uuid" "autostart 1111..." "autostart --disable 2222..."
  "domstate 1111..."]`; restoring the fix passed. Commands and package results are in
  `docs/changes/2026-09-20-phase4-s2-autostart-and-drain-result.json`. The native S2-4 re-run is
  outstanding and is not claimed here.
- API impact: existing contract — no route, wire field or command kind changes. `docs/API.md`
  records that Core's once-per-boot attempt now covers non-shared guests, and that such a start
  goes through the same available-memory check every other start does.
- Guide impact: updated in `docs/guide/pool-authority-and-recovery.md` (clearing the restriction
  now also starts autostart guests that are still shut off, once for that boot) and
  `docs/guide/host-maintenance-mode.md` (the next reboot after exiting maintenance brings local
  guests back).
- State: Core — the managed journal remains the durable record of the autostart intent and the
  per-boot claim stays in private Core state under `pool-vm-autostart-attempts`. Nothing is added
  to Controller.
- Avoid: do not fix this by loosening `poolHostRestricted` or the `BootAuthority` gate to
  shared-storage disks only. The reason string says "starts no shared-storage guest", but the gate
  is also what stops a superseded master acting during a boot it has not accounted for; relaxing
  it to make one retry possible trades a bounded, recoverable delay for a permanent hole. Retrying
  after the gate has passed costs nothing and keeps BUG-0381's evidence valid. Also do not retry
  on a tick without claiming the boot — that turns autostart into a keepalive that undoes an
  operator's manual stop.

## BUG-0393 — A drain of a host that was down finished as needs_attention

- Date: 2026-09-20; found in Phase 4 native lab acceptance (S2-1, `s2-core`), where the check
  stops the member's Core before the drain and the operation settled `needs_attention` with
  `maintenance mode is recorded but has not yet been replicated to pool members`.
- Symptom: entering or leaving maintenance for a host whose agent was stopped, powered off or
  partitioned always reported `needs_attention`, asking an operator to act on something no action
  would change. The guide already promised the opposite: "the host being drained does not need to
  be reachable".
- Root cause: `ExecutePoolMaintenance` downgraded its own result when the prompt
  `PushPoolAuthorityReplicas` call failed. That made one authority write's result depend on a
  member's reachability; no other authority write does — VM ownership, networks, storage and
  master transfers all leave the catch-up to the probe tick at `run.go:846`.
- Fix: keep the prompt push (it is what makes a reachable member refuse within seconds instead of
  within a minute) and stop treating its failure as the operation's outcome. The drain is complete
  when the master's own `pool-peer.json` record is saved, which is the record every placement,
  definition and start is refused by; the member's replica is an early refusal on top of it, never
  the decision. The replication shortfall stays visible per member in the members projection's
  `replica` status and `connection`.
- Status: source fixed. Regression `TestMaintenanceSucceedsWhenAMemberIsUnreachableForThePush`
  (replaces `TestMaintenanceReplicaPushFailureNeedsAttention`) asserts the succeeded result, the
  durable master record, that the member did **not** receive a replica, and that the master itself
  refuses work on the drained host. Negative control: with the downgrade restored the test failed
  with `unreachable member push = {... Status:needs_attention ...}`.
  `TestMaintenanceDrainPushesReplicaPromptly` still passes, so the prompt push was not removed.
- API impact: existing contract — same route, request, command kind and asynchronous refusal set;
  one result status changes. `docs/API.md` records the change and the date.
- Guide impact: updated in `docs/guide/host-maintenance-mode.md` — the task succeeds once the
  master has recorded it, including when the host being drained is down.
- State: Core — the drain stays in the pool master's membership record in `pool-peer.json` and is
  re-adopted from the master's reported inventory. Nothing moves to Controller.
- Avoid: do not make this succeed by dropping the prompt push as well; a reachable member should
  still converge in seconds. And do not try to distinguish "member is dead" from "member is
  partitioned" to decide the status — that judgement is made over the same channel that just
  failed, so it cannot be trusted either way.

## BUG-0394 — Shared Gitea runner marked every Controller foundation lane failed at once
- Date: 2026-09-20
- Symptom: Gitea run 4168 failed every phase1 lane at one timestamp with truncated logs; Installer
  tests passed then the job was marked failed after `context deadline exceeded` removing the job
  container. UI lane steps had all succeeded.
- Root cause: Five parallel job containers plus Installer/Documentation on one act_runner Docker
  host overloaded cleanup; synchronized kill left no usable failure output. Separate from real
  test failures such as run 4162's browser assertion.
- Fix: Stagger phase1 to two concurrent containers (wave 1: go + controller-browser; wave 2:
  contracts + web; wave 3: website), add job timeouts and noninteractive apt, keep Installer and
  Documentation concurrency groups. Forge still executes `.github/workflows/` (BUG-0043).
- Regression: `tests.test_ci_security.BrowserArtifactTests.test_phase1_keeps_separate_lanes_with_runner_safe_stagger`
  asserts the stagger markers and wave-1 browser placement in both workflow copies. Hosted rerun
  after push is the remaining evidence.
- Avoid: Starting five apt/Playwright/Go-race containers on this runner at once, or treating a
  Docker cleanup timeout as a product test failure.

## BUG-0396 — Stalled-probe heartbeat check failed under Go-race CPU load
- Date: 2026-09-20
- Symptom: Gitea run 4171 controller-browser failed
  `other observations responsive during stalled probe` after libvirt unknown had already
  been observed; remaining browser steps were skipped by the job graph.
- Root cause: Wave 1 ran `go test -race` beside Playwright. Under that load, inventory and
  `last_seen` did not advance within the 15-second eventually budget while a stalled probe
  was timing out.
- Fix: Run controller-browser alone first; start go and web only after it finishes. Pass an
  explicit `--probe-timeout 3s` and raise the stalled-probe eventually budgets to 30s/45s.
- Regression: `tests.test_ci_security.BrowserArtifactTests.test_phase1_keeps_separate_lanes_with_runner_safe_stagger`
  asserts the browser job has no `needs` and precedes `go`. Hosted rerun after push is remaining
  evidence for the timing.
- Avoid: Running Go race and the phase1 agent lab on the same shared runner at once, or keeping
  a 15-second eventually budget for post-timeout inventory under CI load.

## BUG-0397 — Storage SMB secret still in page HTML during registration reload
- Date: 2026-09-20
- Symptom: Gitea PR run 4195 failed storage_repositories_browser asserting
  `not-for-display-qa-secret` was absent from page content after "Storage registration queued".
  The same commit passed on the push run.
- Root cause: Notice is shown before `reload()` finishes and `close()` clears the form, so the
  password input can still hold the draft value when the check runs under slower CI load.
- Fix: Clear the draft password as soon as the credential handoff payload is built; wait for the
  Register storage region to unmount before the privacy assertion.
- Regression: `tests/integration/storage_repositories_browser.py` waits for the form to close
  before asserting the secret is gone. Hosted PR/push rerun is remaining evidence.
- Avoid: Asserting DOM privacy against a still-mounted password field, or leaving credentials in
  React draft state across an awaited reload.

## BUG-0398 — Host ISO browser check failed when the host was briefly not fresh
- Date: 2026-09-20
- Symptom: Gitea PR run 4201 failed host_iso_browser expecting
  `Transfer succeeded · not rescanned`; the row showed
  `Availability unknown · host is not freshly connected`. Push run 4198 on the same
  commit passed.
- Root cause: The hosts route only applied explicit overrides, so a transient
  non-fresh connection from earlier controller-browser work on the shared runner
  changed the succeeded-ISO availability string.
- Fix: Default the fixture host to fresh, not revoked, and managed-vm-v1 before
  applying the stale/revoked/old-agent overrides the test already covers.
- Regression: `tests/integration/host_iso_browser.py` hosts route baseline. Hosted
  PR/push rerun is remaining evidence.
- Avoid: Asserting ISO availability wording against a live host connection that
  earlier lab steps may have left reconnecting.

## BUG-0399 — Push and PR Controller foundation labs ran together and starved cert rotation
- Date: 2026-09-20
- Symptom: Gitea PR run 4207 failed `rotated identity reports` while push run 4204 was
  still in progress on the same commit. Agent had reconnected after earlier controller
  restarts; libvirt never returned healthy within 15s.
- Root cause: phase1 concurrency was keyed by `github.ref`, so `refs/heads/dev` and
  `refs/pull/5/head` started two full controller-browser labs on one act_runner. Shared
  CPU delayed post-rotation inventory past the default eventually budget.
- Fix: One concurrency group across Controller foundation refs (later widened to
  `nex-shared-runner` with Release — see BUG-0400). Raise the rotated-identity
  eventually timeout to 45s.
- Regression: `tests.test_ci_security.BrowserArtifactTests` asserts the shared group in
  both workflow copies. Hosted rerun after push is remaining evidence.
- Avoid: Letting push and PR phase1 share a single Docker runner under different
  concurrency keys, or keeping a 15s budget for post-rotation mTLS reporting under load.

## BUG-0400 — Phase 1 browser lab starved by a concurrent Release ISO build

- Date: 2026-09-20
- Symptom: Gitea phase1 run 4222 failed `phase1.py --browser` at
  `Page.reload: Timeout 30000ms exceeded` on the pool health summary route after
  the toolbar power checks passed. Release run 4223 was downloading the ISO
  package closure on the same act_runner the whole time.
- Root cause: `phase1.yml` and `release.yml` used separate concurrency groups, so
  main-push started both. One Docker host ran Playwright plus a heavy ISO build;
  the SPA reload never reached load under that load.
- Fix: Shared concurrency group `nex-shared-runner` for Controller foundation and
  Release (both forge copies).
- Regression: `tests.test_ci_security.BrowserArtifactTests` asserts the shared
  group in phase1 and release workflows. Hosted rerun after push is remaining
  evidence.
- Avoid: Letting Release and Controller foundation overlap on a single act_runner,
  or treating a Playwright load timeout during an ISO build as an app defect.

## BUG-0401 — Release source bundle could not find live-security unbound

- Date: 2026-09-20
- Symptom: Gitea release run 4223 failed `scripts/build-source-bundle.py` with
  `Can not find version '1.26.1-0+deb13u1' of package 'unbound'`.
- Root cause: ISO binary packages came from live `deb.debian.org` (security update
  first seen 2026-09-19) while corresponding-source apt used pinned snapshots at
  `20260916T235959Z`, which lacked that unbound version.
- Fix: Default snapshots to `20260920T000000Z`, and download the offline binary
  closure through the same snapshot sources.list as the source bundle.
- Regression: `tests.test_source_bundle.SourceBundleTests.test_iso_build_exports_complete_release_evidence`
  asserts `snapshot-sources.list` and the bumped snapshot. Hosted release rerun is
  remaining evidence.
- Avoid: Resolving the offline package closure from live trixie while pinning
  corresponding source to an older snapshot.debian.org archive.

## BUG-0402 — ISO GUI rebuild lost deb-src after snapshot binary pin

- Date: 2026-09-20
- Symptom: Gitea release run 4228 failed in `installer/build-gui.sh` with
  `E: You must put some 'deb-src' URIs in your sources.list` while fetching
  `cdebconf=0.280`.
- Root cause: The BUG-0401 pin used an isolated binary-only sources.list for the
  offline package closure. `build-gui.sh` still called bare `apt-get source` and
  found no configured deb-src URIs in that build environment.
- Fix: Snapshot sources.list carries both `deb` and `deb-src`; export it as
  `NEX_SNAPSHOT_SOURCES` and fetch cdebconf through those pins.
- Regression: `tests.test_source_bundle.SourceBundleTests.test_iso_build_exports_complete_release_evidence`
  asserts `NEX_SNAPSHOT_SOURCES` in `build.sh` and `build-gui.sh`. Hosted release
  rerun is remaining evidence.
- Avoid: Pinning binary apt through an isolated sources.list without giving later
  `apt-get source` steps the matching deb-src URIs.

## BUG-0403 — docs/API.md claimed one maintenance refusal sentence where four exist

- Date: 2026-09-21
- Symptom: A native three-host lab placement onto a drained host returned
  `409 the selected host is in maintenance mode; …`, but `docs/API.md` stated that
  "every refusal carries the one sentence" `the selected VM host is in maintenance
  mode; …`. A reader of the API reference could reasonably match on the documented
  text and never see it on the commonest path.
- Root cause: A documentation defect only. The implementation is deliberate and
  correct: the master's authoritative refusal, the Controller's advisory
  pre-dispatch check, the drained host's libvirt-hook refusal and the
  autostart-skip log each name a different actor, and `docs/api/openapi.json`
  already documents the first two together on the three VM routes, spelling out
  that the Controller's is "presentation and early failure only" while "the
  authoritative refusal is the master's own". `docs/API.md` prose had simply not
  kept up with that.
- Fix: `docs/API.md` now scopes the one sentence to refusals the master itself
  decides and lists the other three with the actor each belongs to, plus an explicit
  note not to collapse them.
- Regression: `tests.test_api_contract.PoolMaintenanceContractTests`, which already
  pins every literal against the place it is documented, still passes; it is the
  check that would fail if any of these sentences moved.
- Avoid: "Fixing" this by making the Controller emit the master's sentence. That was
  tried first and reverted: it erases the advisory/authoritative distinction, breaks
  the three openapi.json descriptions that explain it, and leaves an operator unable
  to tell a Controller pre-check from the master's decision. Differing wording here
  is the design, not drift.

## BUG-0404 — Cluster lab guard aborted on the package set its own fixture seeds

- Date: 2026-09-21
- Symptom: `tests/integration/cluster_ha_lab.py --prepare` failed at
  `prepare_pristine_corosync()` for the first Core host, then `cleanup()` raised
  `refusing a reused or foreign PID` and left the remaining fixture VMs running.
- Root cause: The guard ran `virsh` and `pcs` unconditionally and stopped
  `nex-cluster-local.service`, none of which the same file's `seed()` installs, so
  under `set -eu` each ended `prepare()` before any check was evaluated. It also
  named all four units in one `systemctl stop` with corosync ahead of pacemaker;
  systemd declares no ordering between them, so they stopped concurrently and
  corosync went away under pacemaker, which then never finished stopping.
- Fix: Absence guards reach a tool only behind `command -v`, because an absent tool
  means the condition cannot be occurring; pacemaker resources fall back to the CIB
  when `pcs` is absent, since Debian's pacemaker does not depend on
  pacemaker-cli-utils. Units are stopped one at a time in cluster order (resource
  manager, lock manager, messaging), each bounded by `timeout` and confirmed down
  before the next. The existing is-active sweep still fails closed.
- Regression: `tests.test_cluster_ha_lab.PristineCorosyncGuardTests`, six checks
  against `pristine_corosync_command()`, which was extracted so the script's shape
  is testable without a lab. Four of the six fail against the original command;
  verified by running the suite against a reconstruction of it. Natively, the fixed
  guard completed on a Core host with no nested guests and correctly refused two
  hosts that were running guests, with `running-nested-vm`.
- Avoid: Treating "the tool errored" as "the dangerous condition is absent", and
  relying on systemd to order units that declare no dependency on each other.

## BUG-0405 — An invalid VM definition was refused without naming the field

- Date: 2026-09-21
- Symptom: `POST /api/v1/vms` carrying `revision: 7` (creation requires 0) was
  accepted with `202`, then failed asynchronously. On the master the operation read
  `VM placement preflight is invalid`; when the target was a member it read
  `pool master refused VM placement: VM placement preflight refused: selected VM
  host refused placement observation`. Neither named the field, and the member form
  did not even indicate that the cause was validation.
- Root cause: `placementCommand()` returned a bool, so every validation cause
  collapsed into one message. `PoolPlacementHandler` then answered an invalid
  command with a plain-text `http.Error`, which the master cannot decode into
  `poolPlacementReply.Error`, so it fell back to its generic sentence.
- Fix: `placementCommandError()` returns the underlying
  `vmhost.ValidateManagedCommand` error; `localPoolVMPlacement` wraps it; and every
  refusal `PoolPlacementHandler` makes, including auth and method, is emitted in the
  JSON shape the master already decodes.
- Regression: `internal/agent.TestPoolPlacementRefusalNamesTheInvalidField` drives a
  real master-to-member placement with `revision: 7` and requires the reason to
  survive the peer hop and to name the field. Against the pre-fix handler it fails
  with `member refusal lost its reason across the peer hop: selected VM host refused
  placement observation`; verified by reverting.
- Avoid: Answering a peer endpoint's error in a different content type from its
  success, and reducing a validator that knows the cause to a boolean.

## BUG-0406 — ISO install failed: nex-core-local depends on vlan, which the media never carried

- Date: 2026-09-21
- Symptom: Both installer modes failed in `in-target` package configuration with
  `nex-core-local : Depends: vlan but it is not installable` and
  `Unable to satisfy dependencies. Reached two conflicting decisions: ...
  nex-core-local:amd64 Depends vlan / but none of the choices are installable: [no choices]`.
- Root cause: `nex-core-local` gained `ifupdown, bridge-utils, vlan` dependencies for
  Core-owned management networking, but `installer/packages.txt` was not updated with
  `vlan`. The ISO's offline repository is downloaded from that list alone
  (`nex-core-local` itself is appended only after the closure is resolved), and nothing
  else in the list pulls `vlan` in. `late-install.sh` deliberately installs from the
  local repository only, so the missing package is unresolvable rather than fetched.
- Fix: Add `vlan` to `installer/packages.txt` so the offline closure carries it.
- Regression: `installer/check.sh` now parses `nex-core-local`'s `Depends:` field and
  asserts every entry is either in `installer/packages.txt` or in the explicit
  base-system set. It fails with
  `nex-core-local depends on packages the ISO repo never downloads: ['vlan']` when the
  entry is removed and passes with it; run 2026-09-21. CI already runs
  `bash installer/check.sh` on every push and pull request
  (`.github/workflows/installer.yml`, mirrored in `.gitea/workflows/installer.yml`).
  Booting a rebuilt ISO is remaining evidence.
- Avoid: Adding a dependency to `nex-core-local` without adding it to
  `installer/packages.txt`; the package is appended to the list after the closure is
  downloaded, so its own dependencies are never resolved by the ISO build.

## BUG-0407 — Text wizard stored an empty VLAN, so pressing Enter produced an invalid handoff

- Date: 2026-09-21
- Symptom: In **Minimal install - Nex Core (text)**, the VLAN question appeared blank instead of
  showing the untagged default. Accepting it with Enter wrote `vlan=` into
  `/run/nex-installer/config.ini`, and `nex-config` then rejected the handoff, so the fastest
  path through the wizard — answer nothing, accept every default — could not install.
- Root cause: `step_nic` ran `db_set nex-installer/vlan "$(getv "vlan_$key")"`, which on the
  first pass stores an empty string. cdebconf falls back to a template `Default:` only while a
  question has no value at all, not when its value is empty, so the template's `0` was never
  used and an empty answer was stored verbatim.
- Fix: `installer/gui/wizard-text.sh` prefills the previous answer or `0`
  (`db_set nex-installer/vlan "${previous:-0}"`) and stores an empty answer as `0`.
- Regression: `python3 tests/integration/installer-text-wizard.py`, scenario
  `default_answers`, answers every question with its prefilled value and asserts the decoded
  handoff has `vlan:eth0 == '0'` and that the real `nex_network_validate` accepts it. CI already
  runs this file on every push and pull request (`.github/workflows/installer.yml`, mirrored in
  `.gitea/workflows/installer.yml`).
- Avoid: Treating a debconf template `Default:` as a fallback for an empty stored value. Prefill
  the value you want the operator to be able to accept.

## BUG-0408 — French console layout was never applied, so the root password was typed on US

- Date: 2026-09-21
- Symptom: Choosing **French** in the text wizard left the console on the US layout. The root
  password was therefore typed, unseen, on a different layout from the one recorded in the
  handoff, and the installed host booted French — locking root out of its own console.
- Root cause: `step_keymap` ran `ckbcomp "$v_keymap" | loadkeys - || true`. The installer's
  console keymaps carry no bare `fr` map, only the `fr:latin9` variant, and `ckbcomp fr` exits
  `0` after printing its two header lines and no key definitions, so `loadkeys` loaded an empty
  map and the `|| true` swallowed the outcome. `us`, `gb`, `de` and `es` each produce a full map
  under their bare names, which is why only French failed. Confirmed directly against the
  extracted trixie graphical initrd, read-only: `ckbcomp fr` printed 0 `keycode` lines,
  `ckbcomp fr latin9` printed 252, and `us`/`gb`/`de`/`es` printed 106/283/284/284.
- Fix: `installer/gui/wizard-text.sh` maps `fr` to the `fr latin9` arguments (the other four keep
  their bare names) and treats the layout as applied only when `ckbcomp` exits 0, its output
  contains at least one line beginning with `keycode`, and `loadkeys` exits 0. Otherwise the new
  `nex-installer/keymap-error` template is shown and the layout question is asked again, so the
  password question is unreachable until a layout was applied — the same gate
  `nex-wizard.c`'s `keyboard_ready` already enforced graphically. The handoff still records the
  bare layout name. The map is loaded with `loadkeys -u`: `ckbcomp` emits UTF-8 keysyms, and
  `loadkeys` accepts `U+201c` and friends only in Unicode mode, which it otherwise infers from
  the console it opens. Since the gate now blocks the installation, leaving that to the console
  mode would have refused `gb`, `de` and `es` forever on a console that is not `K_UNICODE`;
  `kbd-mode` is absent from `install.amd/gtk/initrd.gz`, so nothing in the boot path sets that
  mode explicitly. Measured with kbd 2.10.0 against maps captured from the initrd's own
  `ckbcomp`: `gb`/`de`/`es` fail `loadkeys -b` (`unknown keysym
  'left_double_quotation_mark'`) and all five layouts pass `loadkeys -u -b`.
- Regression: `python3 tests/integration/installer-text-wizard.py`, scenarios
  `keymap_arguments` (asserts `fr` is requested as `fr latin9`, that `loadkeys` is invoked as
  `-u -`, and that the handoff still says
  `keymap=fr`), `keymap_empty_map_retried` (a layout that expands to no key definitions) and
  `keymap_load_failure_retried` (a layout `loadkeys` rejects); the last two assert the error is
  shown, the question is asked again, and the password question comes only after it. Run by CI
  as above.
- Avoid: `|| true` on a step whose failure changes what the operator is about to type, and
  trusting an exit status from a filter that can succeed while producing nothing. Check the
  output, not just the status.

## BUG-0409 — Padded sysfs values reached the handoff, so disk identity looked changed forever

- Date: 2026-09-21
- Symptom: On hardware whose sysfs serial or model is space padded — NVMe serials and many SCSI
  models are — a text-mode installation failed its identity recheck with `Disk serial changed;
  select the disk again.` and could not be completed, however often it was retried.
- Root cause: `read_attr` was `cat "$1"`, so the padding was kept, and GKeyFile preserves
  trailing spaces in a value. `nex-config` compares the handoff against a `g_strstrip()`ed read
  of the same sysfs attribute, so the two never matched. The graphical wizard reads through
  `read_sys()`, which strips, so only the text mode was affected.
- Fix: `read_attr` in `installer/gui/wizard-text.sh` strips leading and trailing whitespace from
  every sysfs value, in the one shared reader rather than at each call site, so MAC addresses and
  models are covered as well as serials.
- Regression: `python3 tests/integration/installer-text-wizard.py`, scenario
  `sysfs_values_trimmed`, builds a fixture whose serial, model and MAC are padded and asserts the
  handoff contains `disk_serial=TESTSERIAL1` and `mac=52:54:00:aa:bb:01` and that the review shows
  the trimmed serial. Run by CI as above.
- Avoid: Copying a sysfs attribute into a comparison key without stripping it, and fixing such a
  value at one call site when every caller goes through one reader.

## BUG-0410 — A host with no network adapter could not be installed in text mode at all

- Date: 2026-09-21
- Symptom: On a machine with no detected network adapter, the text wizard showed
  `No network adapter was detected. Nex Core needs one management adapter.` and exited. The
  shared setup loop in `start.sh` restarted it, so the same error reappeared indefinitely and the
  machine could not be installed, although the graphical wizard installs such a host offline.
- Root cause: `wizard-text.sh` treated an empty adapter list as fatal (`fail_note` then `exit 1`)
  and offered no offline choice at the management question, so it refused an installation the
  graphical wizard accepts. `start.sh` restarts the wizard on any non-zero exit, turning the
  refusal into a loop.
- Fix: The wizard shows the new `nex-installer/no-nic` note, skips the management question and
  installs offline with an empty `management=`, matching what `nex-wizard.c`'s `collect()` writes
  for the same choice. The management question also gained `Offline (no management network)` as
  its last choice, so an operator with adapters can choose an offline install as well; every
  adapter then offers `disabled`/`dhcp`/`static` and no gateway is asked, because only a
  management adapter may carry a default route.
- Regression: `python3 tests/integration/installer-text-wizard.py`, scenarios
  `no_network_adapter` (fixture with no adapter: the note is shown, the management question is
  not asked, the review reports `Offline`, the handoff matches the expected offline file and the
  real `nex_network_validate` accepts it) and `offline_management` (adapters present, offline
  chosen). Run by CI as above.
- Avoid: Letting a frontend refuse an installation the other frontend accepts, and exiting
  non-zero from a step a supervising loop will simply run again — a refusal that repeats forever
  is worse than the refusal.

## BUG-0411 — The same machine got different `nexN` adapter names in the two installer modes

- Date: 2026-09-21
- Symptom: Installing the same host graphically and in text mode could produce different
  `nex0`..`nexN` names for the same physical adapters, so the two modes did not install the same
  host and an operator comparing them found the names swapped.
- Root cause: `nex-network.c` assigns `nexN` by the position of each `[NIC:*]` group in the
  handoff. `wizard-text.sh` enumerates `/sys/class/net/*` in the shell's sorted glob order, while
  `nex-wizard.c`'s `discover()` used `g_dir_read_name()`, which returns raw readdir order — an
  arbitrary order that depends on the directory's on-disk layout, not on the adapter names.
- Fix: `discover()` collects the names first and sorts them with `strcmp` before building `nics`,
  so both frontends group the adapters in the same order.
- Regression: `bash installer/check.sh` asserts
  `g_ptr_array_sort(names, compare_names)` is present in `installer/gui/nex-wizard.c`, so
  reverting to readdir order fails the job CI already runs
  (`.github/workflows/installer.yml`, mirrored in `.gitea/workflows/installer.yml`). It is a
  source assertion, not a behavioural one: no automated check runs the graphical wizard.
- Avoid: Deriving a durable identity from a directory read order. If two code paths must agree on
  an ordering, both must impose one.

> BUG-0407 to BUG-0411 were found in pre-merge review of the unreleased text wizard, before any
> ISO carrying it was built. Their regression evidence is a scripted simulation: the scenarios
> above drive the wizard with a stand-in for cdebconf and stand-ins for `ckbcomp`, `loadkeys`,
> `prepare` and `nex-config`, under both `/bin/sh` and `busybox ash`. Red-to-green was recorded on
> 2026-09-21: `installer/gui/wizard-text.sh` and `installer/gui/wizard-text.templates` were
> restored from the pre-fix commit into a scratch tree and the committed
> `tests/integration/installer-text-wizard.py` was run against them unchanged. It exited 1 with
> `default_answers`, `erase_summary`, `sysfs_values_trimmed`, `keymap_arguments`,
> `keymap_empty_map_retried`, `keymap_load_failure_retried`, `rejected_settings_keep_answers`,
> `offline_management`, `no_network_adapter`, `prepare_runs_before_frontend` and `pure_ascii`
> failing, and only `navigation_and_handoff`, `prepare_failure_stops_setup`,
> `abandoned_setup_leaves_no_handoff` and `templates_exist` passing; against the fixed wizard all
> 28 scenarios pass. BUG-0411 is the exception: its check is a source assertion in
> `installer/check.sh`, because nothing runs the graphical wizard. Booting a rebuilt ISO and
> completing a text-mode installation, on real hardware and a real console, remains outstanding
> evidence for all five.

## BUG-0412 — Every pull request commit showed cancelled Controller foundation checks

- Date: 2026-09-21
- Symptom: Each commit pushed to a pull request branch carried six failed statuses,
  `Controller foundation / ... (push)` with "Has been cancelled", beside the passing
  `(pull_request)` statuses of the same jobs (push run 4263 and pull request run 4267 on one
  commit). The combined commit status was `failure`, so the pull request looked broken
  although every job that ran had passed.
- Root cause: `phase1.yml` ran on `push` to every branch and on `pull_request`, and both
  events evaluate to the one `nex-shared-runner` concurrency group with
  `cancel-in-progress: true` (BUG-0399, BUG-0400). The forge cancels earlier runs in a
  group when a newer one arrives, and cancels waiting or blocked ones even without
  `cancel-in-progress`, so the two runs of one commit always cancelled each other and the
  loser reported a failed status.
- Fix: `phase1.yml` runs on `push` to `main` only, in both workflow copies. A branch is
  covered by its pull request run, so one commit produces one Controller foundation run.
- Regression: `tests.test_ci_security.BrowserArtifactTests.test_phase1_runs_once_per_pull_request_commit`
  asserts the trigger block of both copies. With `'**'` restored in one copy it fails with
  `Lists differ: [... "'**'" ...] != [... 'main' ...]` and passes with the fix; run
  2026-09-21 with `python3 -m unittest tests.test_ci_security` (10 tests). Forge CI runs it
  in the Controller foundation contracts lane (`python3 -m unittest discover -s tests -p 'test_*.py'`).
  A hosted pull request run after the push is remaining evidence.
- Avoid: Triggering a workflow on both `push: '**'` and `pull_request` while its concurrency
  group is shared across refs; the group serializes by cancelling, not by queueing. Still
  open: on a push to `main` the Controller foundation and Release runs share that group and
  one cancels the other (runs 4255 and 4256); that needs a sequencing decision, not a
  trigger change.

## BUG-0413 — A disk from a previous RAID install stopped with "No root file system"

- Date: 2026-09-21
- Symptom: On a physical server whose selected disk was one member of a four-disk software RAID
  left by a previous hypervisor installation, the Disk step showed `No root file system is
  defined. Please correct this from the partitioning menu.` after the operator had confirmed
  the erase in the wizard.
- Root cause: The wizard preseeds only automatic partitioning. partman then finds the old
  array (and any LVM) on the disk and asks whether to remove it; when that question is not
  answered yes, `device_remove_md`/`device_remove_lvm` return non-zero, and
  `display.d/10initial_auto` ignores `autopartition`'s failure and exits 0. The preseeded
  `choose_partition finish` then reports no root file system. VM checks only ever used blank disks.
- Fix: The partman init hook `00nex-recheck` (`installer/gui/recheck.sh`), after the disk
  identity recheck and before partman assembles arrays (`25md-devices`), stops any array that
  holds the selected disk, zeroes the first and last MiB of each of its partitions and of the
  whole disk, records a marker so a partman restart does not clear it again, and marks
  partman's RAID/LVM scan as done so `25md-devices`/`26lvm-devices` do not reassemble the other
  disks' leftover degraded array (which would also install mdadm and write its `mdadm.conf`
  into the new host). Other member disks are not written. partman's own RAID removal is deliberately not preseeded: it zeroes
  the superblocks of every member, which would break "Other disks are left untouched".
  **View log** now includes `/var/log/partman`.
- Regression: `python3 tests/integration/installer-handoff.py` — fake sysfs, file-backed
  disks and an `mdadm` stub. With the selected disk in `md127` alongside `sdb3`, only `md127`
  is stopped, the selected disk's partitions and table are zeroed, `sdb` is unmodified, and a
  second run does not clear it again, and partman's scan markers are set. It fails on the old hook (`mdadm.log` never written) and
  passes with the fix. `bash installer/check.sh` asserts that RAID removal stays unanswered and
  that View log includes the partman log. Both run in `installer.yml` on the forge and hosted
  copies. Run locally under the host `sh` and under the installer initrd's own busybox; no
  VM install with a real multi-disk array or physical/iDRAC install has been exercised yet.
  Independent review found the reassembly problem, which is fixed as above.
- Avoid: Preseeding `partman-md/device_remove_md`: it clears RAID metadata from disks the
  operator did not select. Treating a blank-VM-disk install as coverage for previously used disks.

## BUG-0414 — A replaced Controller could not recover a pool whose master was gone

- Date: 2026-09-22
- Symptom: After Nex Controller was replaced while a pool's master was down for good, the first
  member to re-enroll showed as **Pool master**. Every other member's enrollment was held with
  `pool identity is held`, and promoting the survivor was refused with `this host is already the
  pool master`, so the pool could not be recovered through the product.
- Root cause: Enrollment into a pool unknown to the Controller records the enrolling host as
  master, on the assumption that the real master enrolls later and is adopted. The adoption rule
  only followed a host claiming mastery for itself, so the survivor's own report naming the dead
  master never corrected the row, and the enrollment and promotion checks trusted the row.
- Fix: `adoptReportedMaster` (`internal/controller/pooltransfer.go`) lets the recorded master's
  own report give the row away to the master it names, even one not enrolled here; mirror rows
  move only to an enrolled master (`moveRecordedMaster`). While the recorded master has never
  enrolled, `poolRecordNaming` supplies the evidence conservatively: the highest generation any
  enrolled host's membership naming it claims, and only the hosts all of them list with the same
  status. Enrollment (`coreReportedPoolMember`), the claim arm of adoption and
  `GET /api/v1/pools/{id}/members` use it. A recorded master that enrolls late takes the pool's
  mirror rows back at enrollment.
- Regression: `go test ./internal/controller/ -run TestReplacementControllerWithTheMasterGoneCanPromote`
  (`internal/controller/pool_evidence_test.go`) — fails on the old code at the first assertion
  (the survivor stays recorded as master) and passes with the fix. It also checks the second
  member enrolls, the Hosts view lists all three hosts non-authoritatively, promotion is not
  refused, the promoted member is adopted at the next generation, and an equal-generation rival is
  not. `TestAMemberCannotAdmitAHostThroughAGoneMastersRecord` (one member listing an extra host
  cannot admit it) and `TestAReturningMasterTakesItsMirrorsBack` (mirror rows move to the late
  master) were each confirmed to fail with their part of the fix disabled. All run in the Go test
  jobs of the forge and hosted workflows; `go test ./internal/controller/` passes. Independent
  review found the forged-evidence and orphaned-mirror gaps, fixed as above. Not exercised on a
  disposable multi-host lab.
- Avoid: Taking the newest member report as evidence: each is a payload its member controls.
  Letting any member's report move the row by claiming mastery for itself; only the
  recorded master may give the row away, and a claim still needs a strictly newer generation or
  the recorded master's pointer.

## BUG-0415 — Hardware tooling was missing from Core package delivery metadata

- Date: 2026-09-22
- Symptom: A package upgrade could leave memory-module and PCI inventory unavailable because
  `dmidecode` and `lspci` were only installed by the ISO recipe. The source archive also omitted
  the sensor module configuration installed by the package builder.
- Root cause: The hardware slice updated the ISO inputs and package installation script without
  updating Core's runtime dependencies and source-file allowlist together.
- Fix: Require `dmidecode` and `pciutils` in the Core package. Retain the existing `.conf`
  source-allowlist correction and check that the archive copies `nex-sensors.conf` unchanged.
- Regression: The extended `test_depends_cover_what_core_runs` failed for both missing packages
  before the dependency edit. The audit's `python3 -m unittest tests.test_build_source -v`
  failed with the missing sensor configuration before the allowlist correction; that correction
  was already present when cleanup implementation began. Afterward,
  `python3 -m unittest tests.test_build_source tests.test_core_local_package -v` passed 27 tests,
  and `bash installer/check.sh` passed. The existing push/PR Python discovery jobs in
  `.github/workflows/github.yml`, `.github/workflows/phase1.yml` and the forge copy execute these
  regressions. No package was installed or upgraded on a real host.
- Avoid: Treating an ISO package list as the dependency declaration for package upgrades, or
  assuming a file installed by the package builder is automatically retained in its source archive.

## BUG-0416 — Update comparison disagreed with Debian version ordering

- Date: 2026-09-22
- Symptom: Nex considered `0.1.0-dev` older than `0.1.0`, although Debian considers its explicit
  revision newer. Update availability and completion checks could disagree with APT.
- Root cause: A custom comparator implemented different revision rules and parsed arbitrary
  numeric components into machine integers instead of using Debian's version engine.
- Fix: Execute `dpkg --compare-versions` through the existing command seam, with caller
  cancellation and a five-second bound. Only exit status 1 means a false comparison; command
  failures do not report an available update or a successfully completed installation.
- Regression: In an isolated copy of the old code, changing the existing version test to the
  correct Debian expectation failed with `-1 want 1`. Read-only native `dpkg` checks confirmed
  revision, epoch, tilde and long numeric ordering. `go test -count=1 -race ./internal/nexupdate`
  passed with the fix, including comparison arguments/results, cancellation, missing-command
  and invalid-version errors, and update/progress failure handling. The existing push/PR Go
  jobs in `.github/workflows/github.yml`, `.github/workflows/phase1.yml` and the forge copy
  execute the regression. No real APT update or package installation was performed.
- Avoid: Reimplementing Debian version syntax or treating a subprocess failure as equality.

## BUG-0417 — Pool membership lookup error was absent from the API reference

- Date: 2026-09-22
- Symptom: The contract check rejected the members endpoint because its implemented
  `membership lookup failed` response was undocumented.
- Root cause: The recovery lookup added a handler error without its OpenAPI error description.
- Fix: Document the existing retry-safe HTTP 500 response in the OpenAPI contract and API guide,
  and regenerate the HTML reference; handler behavior is unchanged.
- Regression: The full Python suite first failed
  `PoolJoinContractTests.test_documented_refusals_are_the_literal_handler_messages` with the
  missing message. After the correction,
  `python3 -m unittest tests.test_api_contract.PoolJoinContractTests.test_documented_refusals_are_the_literal_handler_messages`
  passed. Existing push/PR Python discovery runs this test on the forge and hosted CI.
- Avoid: Adding a new error path without updating its machine-readable contract and reference.

## BUG-0418 — A host joining mid-rollout tore the pool's watchdog cluster down

- Date: 2026-09-22
- Symptom: During an arming rollout, a fourth host completing its CSR join (ready, `peer_origin`
  still empty until `/pool/advertise` lands) made every unarmed host stop Corosync and Pacemaker
  at once, leaving the one still-feeding host a minority of one; `no-quorum-policy=suicide` plus
  `sbd` then reset a healthy host.
- Root cause: `poolClusterIntent`'s "missing peer origin" branch returned `nil` for the nodelist —
  the only one of its five branches that discarded the nodes it had already collected — so
  `poolClusterMayLeave`'s `!poolClusterContains(nodes, hostID)` escape hatch, meant for drained and
  ejected hosts, fired for every host, and `poolWatchdogTeardownTurn` iterating the same nil slice
  never gave the armed host a disarm turn.
- Fix: Return `nodes` from that branch (`internal/agent/pool_cluster.go`); `want` stays false, so
  `applyPoolCluster` still never runs and no nodelist is rewritten.
- Regression: `TestAJoiningHostWithNoAddressYetDoesNotTearThePoolDown`
  (`internal/agent/pool_cluster_test.go`) fails with `a host enrolling reset [master-1]` on the old
  code. `go test -count=1 -run TestAJoiningHostWithNoAddressYet ./internal/agent/` →
  `ok nex.local/nex/internal/agent 0.026s`; `go test -count=1 ./internal/agent/ ./internal/vmhost/`
  → `ok` for both. Run by the existing Go jobs in `.github/workflows/` and the forge copies.
- Avoid: Special-casing an empty `peer_origin` at the join path, or guarding inside
  `poolClusterMayLeave`. The invariant is that `poolClusterIntent` always reports the running
  cluster's members even when it answers "no".

## BUG-0419 — Nothing released the pool's `corosync.conf`, so clustered storage could never be added

- Date: 2026-09-22
- Symptom: After a pool formed a watchdog cluster, turning Host fencing off and then registering a
  clustered-storage repository failed permanently on every host with `this host's Corosync
  configuration belongs to another Nex Core cluster on this pool; one host has one cluster` — the
  exact remedy both the refusal text and the guide tell the operator to use.
- Root cause: `LeavePoolCluster` only stopped Pacemaker and Corosync and left the pool-marker
  `/etc/corosync/corosync.conf` on disk; nothing in the tree ever removed `corosyncConfigPath`, and
  `writeCorosyncConfig` refuses any file whose marker differs from the caller's (the unconfigured
  vendor marker is deleted on first write).
- Fix: `LeavePoolCluster` (`internal/vmhost/pool_cluster.go`) removes the file it owns once stopped,
  on both the running and the already-stopped path, ignoring `os.ErrNotExist`. It is idempotent
  because the next pass sees `PoolClusterOwnsCorosync() == false`.
- Regression: `TestLeavingThePoolClusterReleasesItsCorosyncConfiguration`
  (`internal/vmhost/pool_cluster_test.go`) fails with `the pool's cluster configuration was left on
  disk after this host left it` with the `os.Remove` taken out.
  `go test -count=1 -run TestLeavingThePoolCluster ./internal/vmhost/` →
  `ok nex.local/nex/internal/vmhost 0.007s`; the full package run passes
  (`ok nex.local/nex/internal/vmhost 8.635s`).
- Avoid: Relaxing `writeCorosyncConfig`'s marker check, or re-creating the unconfigured marker so
  the other consumer can take the file over. The interlock is what keeps a
  `no-quorum-policy=freeze` cluster from landing under armed hosts; release the file where
  ownership ends instead. The symmetric storage-side release — freeing a storage-written
  `corosync.conf` when the last clustered repository is removed — is still outstanding.

## BUG-0420 — The IPMI fence ran over IPMI 1.5, not the session the identity was proved over

- Date: 2026-09-22
- Symptom: A host whose identity reads and every `PoolFenceCheck` succeeded over RMCP+ showed its
  fence device Ready, but the power-off at promotion failed on controllers with IPMI 1.5 disabled.
- Root cause: `fenceAgentArgs` built `fence_ipmilan` without `--lanplus`; the agent defaults to
  IPMI 1.5 and `--cipher` only applies to lanplus, so the proved cipher suite was silently dropped
  and the 1.5 path was first exercised at the fence itself.
- Fix: Pass `--lanplus` (`internal/vmhost/storage_fence.go`) and widen `fenceCIBCreateArgs`'
  valueless-flag case (`internal/vmhost/storage_cluster.go`) so the stonith resource renders
  `lanplus=1`.
- Regression: `TestIPMIFenceRunsOverTheSameRMCPSessionTheIdentityWasProvedOver`
  (`internal/vmhost/storage_fence_test.go`) asserts `--lanplus`/`--cipher=17` in the agent args and
  `lanplus=1` in the CIB args; without the first half it fails `IPMI fence arguments omit
  "--lanplus"`, without the second `invalid native fence argument`.
  `go test -count=1 ./internal/agent/ ./internal/vmhost/` → `ok` for both packages.
- Avoid: Adding `--lanplus` alone — `fenceCIBCreateArgs` rejects any valueless `--flag` with
  `invalid native fence argument`, so Pacemaker resource creation breaks.

## BUG-0421 — A timed-out fence-check pass erased the results it never reached

- Date: 2026-09-22
- Symptom: When a checking pass hit the two-minute pass timeout, targets it never reached flipped
  from their previous result to `no other host in this pool has checked this host's management
  controller yet`, and a previously recorded `credentials_rejected` stopped warning.
- Root cause: `recordPoolFenceChecks` replaced `poolFenceChecks[dir][checker]` wholesale,
  contradicting its own comment that unreached targets keep their previous result and go stale.
- Fix: Start the pass as a `maps.Clone` of the checker's existing entry so the pass merges into it
  (`internal/agent/pool_fence_check.go`).
- Regression: `TestATruncatedPassKeepsTheResultsItDidNotReach`
  (`internal/agent/pool_fence_check_test.go`) reports the `host-c` entry as zero-valued with the
  clone removed. `go test -count=1 ./internal/agent/ ./internal/vmhost/` → `ok` for both packages.
- Avoid: Adding pruning alongside the merge. `PoolFenceCheckMembership` already drops non-ready
  checkers and profile-stamp mismatches and recomputes `Stale`, so a carried-over entry cannot
  read as fresh or resurrect a removed device.

## BUG-0422 — A peer-offered fence password was not checked against the shared rule

- Date: 2026-09-22
- Symptom: A modified pool member could offer a fence password with leading or trailing whitespace.
  It authenticated every `ipmitool -E` readiness check and the row read Ready, while
  `fence_ipmilan`, which strips the script's output, would fail the one power-off that matters.
- Root cause: The peer boundary hand-rolled its own password clauses instead of applying
  `vmhost.FenceSecretUsable`, which the operator-entered boundary does apply.
- Fix: `AcceptProvisionedFence` (`internal/agent/pool_bmc.go`) applies `FenceSecretUsable` at the
  peer boundary; the redundant `password == ""` clause is deleted and the NUL/interior-CRLF clause
  kept.
- Regression: `TestAPaddedPasswordAPeerOffersIsRefused` (`internal/agent/pool_bmc_test.go`) fails
  with the old clause restored (the offer gets past the guard and fails later with a different
  message). `go test -count=1 ./internal/agent/ ./internal/vmhost/` → `ok` for both packages.
- Avoid: Replacing the whole clause set with `FenceSecretUsable` — it does not cover NUL or
  interior CR/LF.

## BUG-0423 — Provisioning left password scripts on the host that nothing named

- Date: 2026-09-22
- Symptom: Root-owned 0700 password scripts accumulated in
  `<state>/storage-cluster/fence-credentials/` with nothing naming them — one per interrupted
  provisioning, and one per withdraw/re-enable cycle after the master recorded a profile.
- Root cause: The only name for the file lived inside `record.Pending`, which is written after the
  file and cleared once the master records the profile.
- Fix: `poolLocalBMCRecord` gains `Ref` (`internal/agent/pool_bmc.go`), saved with the slot before
  the controller is touched, reused on later passes (retire, then write) and retired on every exit
  path — withdrawal, leaving the pool and re-provisioning. The script is deliberately not retired
  when the master records the profile: on a master `AcceptProvisionedFence` writes the same material
  under the same ref, so retiring there would delete the master's own live credential.
- Regression: `TestAnInterruptedProvisioningLeavesNoSecondPasswordFile` and the two-case
  `TestRemovingTheAccountRetiresThePasswordItOpened`
  (`internal/agent/pool_bmc_test.go`). Without ref reuse and the retire-before-write they fail with
  `this host holds 2 fence password scripts for one account`; with the old `record.Pending`-based
  retire, `the password of a removed account is still on this host`.
  `go test -count=1 ./internal/agent/ ./internal/vmhost/` → `ok` for both packages.
- Avoid: Saving `Pending` before writing the credential. That inverts the window into
  `Pending != nil` with no secret file, and `offerPendingFence` then fails
  `ReadFenceCredentialSecret` on every pass and reports `PoolBMCPendingMaster` for ever with no path
  back to re-provisioning.

## BUG-0424 — A management controller that refused configuration was rewritten every ten seconds

- Date: 2026-09-22
- Symptom: Firmware that accepts `user set name` and `user set password` but refuses `user priv` or
  `channel setaccess` caused about 8,640 name-and-password NVRAM writes per day, for ever.
- Root cause: The failure path left `{Created:true, Slot:n, Pending:nil, Recorded:false}`, which
  matches no exit arm, so the ten-second reconcile pass re-planned the same slot and generated a new
  password each time; the `Pending` guard only covers a configuration that already succeeded.
- Fix: `poolLocalBMCRecord` gains `RetryAfter` (`internal/agent/pool_bmc.go`), set to one check
  interval on a `configureLocalBMCAccount` failure and saved before the refusal is reported; a new
  switch arm skips the pass until it passes, after the `Pending` arm so withdrawal and a manual
  profile still take precedence, and the last report stands.
- Regression: `TestAControllerThatKeepsRefusingIsNotRewrittenEveryPass`
  (`internal/agent/pool_bmc_test.go`) runs five passes against a refusing controller and asserts
  exactly one in-band configure call; with the new arm removed it fails `a refusing controller was
  written to 5 times`. `go test -count=1 ./internal/agent/ ./internal/vmhost/` → `ok` for both
  packages.
- Avoid: Setting `BMCProvisioningDisabled` in response to a refusal — that is the operator's
  withdrawal decision and would also remove the account.

## BUG-0425 — An ejected row held a machine's identity for ever

- Date: 2026-09-22
- Symptom: After a host was ejected and its physical machine re-imaged and re-enrolled under a new
  host ID, the new host could never record its SMBIOS UUID, so `vmhost.PoolFenceUsable` refused
  every `redfish` or `ipmilan` profile for it and the operator was told to eject a host that is
  already ejected, with no in-product way to clear it.
- Root cause: `acceptPoolSystemUUID` scanned every membership row for a duplicate identity without
  skipping `Status == "ejected"`; `nextPoolMembership` keeps ejected rows verbatim, `SystemUUID`
  included, and nothing prunes them.
- Fix: Skip ejected rows in the duplicate check (`internal/agent/peer.go`), as the join, authority,
  CPU and promotion scans already do.
- Regression: `TestAnEjectedRowDoesNotHoldAMachineIdentityForEver`
  (`internal/agent/pool_system_uuid_test.go`) fails with the clause removed: `an ejected host still
  holds the machine's identity: "", conflict "member-1"`. `go test -count=1 -run
  'TestAnEjectedRowDoesNotHoldAMachineIdentityForEver|TestPoolJoinInvitationWorksOutMasterPeerOrigin'
  ./internal/agent/` → `ok nex.local/nex/internal/agent 0.025s`; full package run
  `ok nex.local/nex/internal/agent 12.513s`.
- Avoid: Pruning ejected rows, or letting an operator clear `SystemUUIDConflictHostID`. The ejected
  row is deliberate durable history, and a live duplicate must still be refused.

## BUG-0426 — A master on a non-default pool-peer listener recorded an unreachable origin

- Date: 2026-09-22
- Symptom: A pool master running a non-default `--pool-peer-listen` port, or bound to a NIC that is
  not the default-route one, wrote an unreachable origin into its own membership row on the first
  invitation. Every later join dialled the dead address, and the recorded value could not be moved:
  the UI sends no `master_url` and the Controller overwrites a supplied one with the reported value.
- Root cause: `ownPeerOrigin` hard-coded port 9444 and the default-route source address while the
  listener is operator-configurable, and the derived value is persisted before any join proves it.
- Fix: Derive both host and port from the configured listener (`agent.PoolPeerListen`, set straight
  from the flag in `cmd/nex-core-agent/main.go` and `cmd/nex-core-local/main.go`), keeping the
  default-route fallback only for a wildcard bind (`internal/agent/pool_join.go`,
  `internal/agent/run.go`). The now-unread `localcore.Config.PoolPeerListen` was deleted.
- Regression: `TestPoolJoinInvitationWorksOutMasterPeerOrigin`
  (`internal/agent/pool_join_test.go`) gained a `192.0.2.50:9500` listener case; with the literal
  `":9444"` restored it fails on the derived default-route origin. `go test -count=1 -run
  'TestAnEjectedRowDoesNotHoldAMachineIdentityForEver|TestPoolJoinInvitationWorksOutMasterPeerOrigin'
  ./internal/agent/` → `ok nex.local/nex/internal/agent 0.025s`.
- Avoid: Making the record non-durable by leaving the master row empty for a derived origin, or
  adding a Controller-side `master_url` fallback. The master's peer origin is host-owned durable
  state, and pinning it once recorded is the design.

## BUG-0427 — A pool master could hand its pool row to a host of another pool

- Date: 2026-09-22
- Symptom: A compromised or buggy pool master could point its pool's `master_host_id` and all of its
  storage, network and media mirror rows at an enrolled host of a different pool or site. The pool's
  page then rendered the other pool's membership, fence profiles and BMC addresses, and its own
  mirrors stopped being refreshed — a self-inflicted denial of service plus display corruption.
  Cross-pool command execution stayed blocked by `managedHost`.
- Root cause: The disclaim arm of `adoptReportedMaster` only checked `claim.PoolID` and
  `master == host`, never that `claim.MasterHostID` belongs to the pool, and `moveRecordedMaster`
  looked the host up with a pool-unscoped `count(*)`.
- Fix: Scope that lookup to the pool inside `moveRecordedMaster`
  (`internal/controller/pooltransfer.go`), the single writer both arms go through:
  `sql.ErrNoRows` keeps today's "master not enrolled here" behaviour, an enrolled host of another
  pool is left untouched, and otherwise the row is recorded and rekeyed as before.
- Regression: `TestADisclaimedMasteryStaysInsideThePool`
  (`internal/controller/pool_evidence_test.go`) fails with the pool check removed: `a master handed
  its pool row to a host of another pool`. `go test -count=1 -run
  TestADisclaimedMasteryStaysInsideThePool ./internal/controller/` →
  `ok nex.local/nex/internal/controller 0.144s`; full package run
  `ok nex.local/nex/internal/controller 31.472s`.
- Avoid: Adding the check only to the disclaim arm of `adoptReportedMaster`. The shared writer is
  where every caller routes, and it is also what keeps the "master that never enrolled here" path
  working.

## BUG-0428 — A stalled temperature sensor stopped all usage sampling

- Date: 2026-09-22
- Symptom: On a host with an NVMe controller in reset or a SATA disk in error recovery, all host and
  VM usage sampling stopped for tens of seconds per tick, history flushes were skipped, the usage
  probe reported nothing because the latest sample aged past `usageMaxAge`, and `GET /hardware`
  timed out past the Controller's `statsQueryWait` on every retry.
- Root cause: `readHwmonSensors` reaches NVMe and `drivetemp` chips with a plain `os.ReadFile` on
  `tempN_input`, which is a device command (NVMe Get Log Page, ATA/SCSI SMART), not a cheap procfs
  read. It was called synchronously from `sampleHost()` — which `run()` invokes with no context and
  no deadline on the single sampler goroutine — and from `readSensors`, outside the
  `hardwareReportWait` context.
- Fix: `hwmonSensorsWithin(root, wait)` (`internal/agent/usage_history_hwmon.go`) runs the read on a
  background goroutine with at most one read in flight and returns the last completed read's values
  when the bound expires; the sampler passes `usageSampleDeadline`
  (`internal/agent/usage_history_sample.go`) and the hardware report `hardwareToolWait`
  (`internal/agent/hardware.go`). A caller that finds a read already in flight does not wait on it
  at all and returns the cache at once, so a wedged chip costs the sampler one tick rather than its
  whole deadline on every tick; and the cache carries the time it completed, so past
  `hwmonLastMaxAge` (two sample steps) it is dropped and the series takes a gap instead of
  publishing the same temperature for ever.
- Regression: `TestHwmonSensorsWithinReturnsLastValuesWhenAChipStalls`
  (`internal/agent/usage_history_hwmon_test.go`) blocks a sensor with a fifo — a real blocking
  `open(2)` — and asserts each 200 ms-bounded read returns the last known values, that the reads
  after the one that stalls return in under a quarter of that bound, that a cache older than
  `hwmonLastMaxAge` is not served, and that three stalled ticks add at most one goroutine.
  Reverted to the synchronous read it fails `panic: test timed out after 20s` with the stack in
  `hwmonSensorsWithin`; with the nil-channel wait restored it fails `read 1 took 200.057442ms while
  a read was already in flight`; with the staleness bound removed it fails `a reading older than
  10s was still served`. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 12.006s`;
  `go test -race -count=3 -run 'Hwmon|PeerOrigin|WatchdogArming|SeriesCap' ./internal/agent/` →
  `ok nex.local/nex/internal/agent 1.774s`.
- Avoid: Adding a context to `readHwmonSensors` — `os.ReadFile` is not cancellable, so the read
  still blocks. And spawning a goroutine per tick without an in-flight guard, which leaks one
  goroutine every five seconds for as long as the chip is wedged.

## BUG-0429 — The sensor budget dropped CPU and NVMe temperatures alphabetically

- Date: 2026-09-22
- Symptom: On an AMD host with about 24 or more SATA/SAS drives, the CPU package temperature and
  every NVMe temperature were missing from both the usage-history chart and the hardware panel,
  while 24 individual disk temperatures were kept. The only trace was a one-shot log line naming
  nothing.
- Root cause: `readHwmonSensors` sorted purely by key and truncated to
  `protocol.HardwareMaxSensors`, and `drivetemp` sorts before `k10temp` and `nvme`. The
  `/etc/modules-load.d/nex-sensors.conf` the package installs makes every disk publish a sensor, so
  the alphabetical cut undid the per-core suppression `hwmonLabel` performs for exactly this reason.
- Fix: Over budget only, stable-sort by a new `hwmonRank` (CPU packages, then NVMe, then other
  devices, then per-drive sensors), cut, then re-sort the survivors by key so ordering and
  boot-to-boot stability are unchanged (`internal/agent/usage_history_hwmon.go`).
- Regression: `TestHwmonBudgetKeepsCPUAndNVMeAheadOfDisks`
  (`internal/agent/usage_history_hwmon_test.go`) — 26 `drivetemp` chips plus one `k10temp` and one
  `nvme` — asserts both the CPU and NVMe keys survive and the survivors stay key-sorted; with plain
  truncation the survivors are 24 `temp.drivetemp-*`. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.950s`.
- Avoid: Raising `HardwareMaxSensors` — the bound is a wire contract on both sides. And dropping the
  key sort entirely: hwmon indexes are probe-order and change across boots, so an unsorted cut keeps
  different sensors after every reboot.

## BUG-0430 — An upgraded host at the series cap never recorded a temperature

- Date: 2026-09-22
- Symptom: A host upgraded to the temperature build whose `host.rrd` was already at the 128-series
  cap — a 64-thread machine with about 14 disks or 10 guest bridges — never recorded a single
  `temp.*` series, so its temperature charts stayed permanently empty; only a "more than 128 series"
  warning appeared.
- Root cause: `ringFile.index()` refused any unknown key once `len(f.Series) >= historyMaxSeries`,
  `decodeRing` loads the full series list from the existing file and marks every series seen, and
  `compact()` only drops a series that is both idle for an hour and entirely NaN — a `cpu.core.*`
  series is sampled every cycle, so it never frees its slot. The new priority ordering in
  `hostHistoryPoints` only decides who wins in a file being filled from empty.
- Fix: At the cap, a non-`cpu.core.*` key reclaims the last `cpu.core.*` slot — series meta, `seen`,
  a fresh ring and a zeroed accumulator per tier (`internal/agent/usage_history.go`). A `cpu.core.*`
  key evicts nothing, so a displaced core stays out and there is no thrash, and genuinely
  unprioritised extras still get the existing warning.
- Regression: `TestRingFileAtSeriesCapEvictsACoreForATemperature` (`internal/agent/usage_history_hwmon_test.go`) fills a ring to exactly 128 with
  `cpu.total` and cores, adds a temperature point, and asserts it is admitted, `cpu.total` survives,
  and that the series list after a second identical sample is exactly the one after the first — the
  displaced core stays out and nothing else moves; with the old `return -1` it fails with 128
  series, all cores, no temperature. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 12.006s`.
- Avoid: Testing the ordering only from `newRing()` — the empty-file path passes while the upgrade
  path it is meant to protect fails. And loosening `compact()` to evict live series, which would
  drop real data.

## BUG-0431 — The Hardware panel listed zram, loop and optical devices as installed disks

- Date: 2026-09-22
- Symptom: The host Hardware panel listed `/dev/zram0` as a 64 GB disk with no model or serial, plus
  `/dev/sr0` and any active loop device, contradicting the disk-I/O series on the same page and
  eating into the 128-disk bound.
- Root cause: `readDisks` ran `lsblk -J -d -b` and appended every top-level block device
  unconditionally; `lsblk -d` lists zram, loop and optical devices.
- Fix: Extract the prefix classifier the package already had inside `wholeDisk` into
  `physicalDisk(name)` (`internal/agent/usage_history_sample.go`) and skip non-physical devices in
  `readDisks` (`internal/agent/hardware.go`), so both panels use one list.
- Regression: `fixtureLsblk` gained `zram0`, `sr0` and `loop0`, and
  `TestReadHardwareReportsEverySection` (`internal/agent/hardware_test.go`) asserts exactly two
  devices; without the filter it reports five. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.950s`.
- Avoid: Filtering on lsblk's `TYPE != "disk"` — `zram0` reports `TYPE=disk`, so it would drop `sr0`
  and `loop0` and keep the very device the report cited. And calling `wholeDisk` from `readDisks`:
  its `os.Stat("/sys/block/<name>")` would make the fixture test depend on the build machine really
  having those devices.

## BUG-0432 — A refused Host fencing toggle was shown as a failed membership read

- Date: 2026-09-22
- Symptom: Turning the pool's Host fencing switch on or off against a master that refuses rendered
  Core's sentence under "The pool master could not be asked for its membership:", although the
  membership had loaded and was still displayed below, and the false message persisted until the
  operator pressed Refresh.
- Root cause: `setHostFencing` was the one command handler writing its catch into `error`, the state
  owned by `refresh()`, and it neither cleared that state on entry nor refreshed on failure.
- Fix: Reuse the existing panel-level action alert (`bmcError` renamed `actionError`) in
  `web/src/pool-members.tsx`: cleared at the start of `setHostFencing` and written on refusal, so
  `error` belongs to `refresh()` alone.
- Regression: The source assertion added to `web/src/pool-members.test.mjs` fails
  `Core's refusal goes to the action alert` with `setError(` restored in the catch. From `web/`:
  `npx tsc --noEmit` → exit 0, no diagnostics; `node --test src/*.test.mjs` →
  `tests 11, pass 11, fail 0`.
- Avoid: Adding a third error state, or calling `refresh()` in the catch — the panel already had the
  right slot, and refreshing would have discarded Core's refusal.

## BUG-0433 — Chart series colours fell below the 3:1 non-text contrast minimum

- Date: 2026-09-22
- Symptom: On the Stats tabs the second to fifth plotted line of any chart — network rx/tx and disk
  read/write reach series 2 on every host — were drawn at 2 px on a near-white card in colours
  measuring 1.80:1 to 2.98:1, with the yellow line effectively invisible. The legend swatch used the
  same token, so it was no mitigation, and the numeric values were reachable only through a hover
  tooltip.
- Root cause: The staged palette change aliased `--series-N` directly to the vivid card and gauge
  accents, replacing a previous all-dark series palette that passed.
- Fix: `web/src/stats.css` gives series 2 to 5 their own darkened hues (`#0e7490`, `#1179b5`,
  `#0f8a5f`, `#9a6700`, measuring 4.36:1 to 4.87:1 on white) while the accents stay vivid for card
  tints, gauge arcs and pills; two comments asserting the old, false mitigation were corrected.
  Series 3 was darkened too, beyond the three the review named, because it also measured below 3:1.
- Regression: A new block in `web/src/stats.test.mjs` parses `stats.css`, resolves the
  `var(--accent-*)` aliases, derives the darkest 6% card tint and asserts every `--series-1..8` is
  at least 3:1 against it; reverting `--series-2` fails
  `--series-2 #00d1b2 is 1.80:1 on #fff3f5, below 3:1`. From `web/`: `npx tsc --noEmit` → exit 0;
  `node --test src/*.test.mjs` → `tests 11, pass 11, fail 0`.
- Avoid: Keeping the aliases and claiming the legend or tooltip compensates — both inherit the same
  token, and the tooltip is unreachable by keyboard.

## BUG-0434 — The release workflow pinned the website release id to the package version

- Date: 2026-09-22
- Symptom: With deploy secrets set, a second push to `main` at an unchanged CHANGELOG version, or
  any prerelease version, turned the Release run red at the last step — after the tag had moved and
  APT and the download tree had already published, leaving the site on the previous build.
- Root cause: The release workflow passed `NEX_WEBSITE_RELEASE_ID: ${{ steps.version.outputs.version }}`,
  the Debian version, bypassing `scripts/website-ci-deploy.sh`'s unique `${stamp}-${sha:0:12}`
  fallback. `website-release.py publish` refuses an existing `releases/<id>`, and the deploy
  script's `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` regex rejects the `~` a prerelease Debian version
  carries.
- Fix: Delete the env line from `.github/workflows/release.yml` and the `.gitea/workflows/release.yml`
  copy, so the script derives its own per-run id as the Website workflow already does.
- Regression: `tests/test_ci_security.py::ReleaseDeployCredentialTests::test_website_release_id_is_left_to_the_deploy_script`
  fails on both provider copies against the pre-fix workflows.
  `python3 -m unittest tests.test_ci_security tests.test_website_hosting tests.test_release_publish`
  → `Ran 33 tests in 0.413s` `OK`. The existing Python discovery jobs run it on the forge and hosted
  CI.
- Avoid: Substituting the tag form (`steps.version.outputs.tag`) — it dodges the `~` but keeps the
  collision on a same-version republish, which is the documented normal path.

## BUG-0435 — The website was built after the release tag had already moved

- Date: 2026-09-22
- Symptom: A TypeScript error under `website/src`, or a half-set `NEX_DEPLOY_*` secret, aborted the
  Release job after "Create or replace the release" had created the forge release and moved the
  version tag, so APT and `/downloads` stayed on the previous version and the reachability check was
  skipped too — a half-published release.
- Root cause: "Check for deploy credentials", "Set up Node.js for the website" and "Build the static
  website" were placed after the release-creation step although none of them consumes its output.
- Fix: Move all three directly before "Create or replace the release" in both workflow copies, and
  correct the credentials-step comment that asserted the old order.
- Regression: `tests/test_ci_security.py::ReleaseDeployCredentialTests::test_deploy_checks_and_website_build_run_before_the_tag_moves`
  fails on both provider copies against the pre-fix workflows; the stale opposite-order assertion in
  `test_publish_and_verify_steps_are_gated_on_the_same_result` was removed rather than duplicated.
  `python3 -m unittest tests.test_ci_security tests.test_website_hosting tests.test_release_publish`
  → `Ran 33 tests in 0.413s` `OK`.
- Avoid: Relying on the separate Website workflow to catch the bad build — it runs in a different
  job and cannot stop the Release job. And adding a post-failure cleanup or retry step instead of
  ordering the cheap checks first.

## BUG-0436 — A watchdog proof was never counted, so a pool without fence devices could not promote

- Date: 2026-09-22
- Symptom: In a pool with Host fencing on and no BMC profiles, promotion produced a valid watchdog
  proof, reached stage `activate`, then failed `this pool promotion is not proved` on every retry,
  leaving the pool with no master.
- Root cause: The proof-verification loops skipped members with no fence profile, so a watchdog proof
  was never counted.
- Fix: Drop the `member.Fence == nil` short-circuit from `evaluatePoolPromotion`,
  `promotionExpiredFenceOnly` and `poolRestartUnreachableExcluded`
  (`internal/agent/pool_promote.go`); `protocol.PoolPromotionFenceVerified` is the single decider,
  and the verifier already rejects a non-watchdog proof without a profile.
- Regression: `TestTheWatchdogProofAndReadinessAgreeThroughTheRealPromotionPath`
  (`internal/agent/pool_watchdog_proof_test.go`) extended into `activatePromotedPoolAuthority`; with
  the skip restored it fails `pool host master-1 has not been fenced for this promotion`.
  `go test -count=1 ./internal/agent/` → `ok nex.local/nex/internal/agent 11.854s`;
  `go test -count=1 -race ./internal/agent/` → `ok nex.local/nex/internal/agent 29.176s`.
- Avoid: Special-casing the watchdog at each call site, or relaxing
  `protocol.PoolPromotionFenceVerified`.

## BUG-0437 — The fence step chose a method the readiness preflight had not promised

- Date: 2026-09-22
- Symptom: A promotion the preflight approved through the watchdog aborted on confirm with the
  credential refusal, and repeated for ever.
- Root cause: The fence step re-derived the method from `vmhost.PoolFenceUsable` alone, ignoring the
  credential condition readiness applies.
- Fix: `fencePoolExcludedHosts` (`internal/agent/pool_promote.go`) loads peer state once and calls
  `poolPromotionFenceReadiness` per host, branching on the kind it returns; the duplicated "no
  recorded fence profile" refusal was removed, leaving one decider.
- Regression: `TestAPromotionFencesByTheMethodReadinessPromised`
  (`internal/agent/pool_watchdog_proof_test.go`) fails on the old predicate with `this host holds no
  fence credential for master-1`. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.854s`; `go test -count=1 -race ./internal/agent/` →
  `ok nex.local/nex/internal/agent 29.176s`.
- Avoid: Adding the credential check to `vmhost.PoolFenceUsable`, or duplicating the rule in the
  fence step.

## BUG-0438 — A watchdog-only master rebooted ungated after its own watchdog reset it

- Date: 2026-09-22
- Symptom: A watchdog-only master that its watchdog had reset, and that the pool had promoted over,
  rebooted with no boot restriction and autostarted the same shared-storage VMs the new master was
  running — two writable copies of the same disks.
- Root cause: Nothing on the arming path recorded self-fence exposure, so
  `EnsurePoolBootRestriction` returned early and the storage guard's skip applied. Only a recorded
  fence profile set it.
- Fix: New `poolSelfFenceExposed(member)` — a usable fence profile or `WatchdogArmed.Armed` — used by
  `poolSelfFencePromotable` and `recordPoolSelfFenceExposure`, and
  `applyPoolWatchdogArmedReport` records the exposure for this host before saving peer state
  (`internal/agent/pool_watchdog_arming.go`, `internal/agent/pool_promote.go`).
  `internal/vmhost/storage_guard.go` is unchanged.
- Regression: `TestAnArmedMasterIsRestrictedOnTheBootAfterItsWatchdogCouldHaveResetIt`
  (`internal/agent/pool_watchdog_arming_test.go`) fails `arming recorded no self-fence exposure`
  with either half reverted. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.854s`; `go test -count=1 -race ./internal/agent/` →
  `ok nex.local/nex/internal/agent 29.176s`.
- Avoid: Adding a watchdog branch to `poolHostRestricted` in `internal/vmhost/storage_guard.go` —
  the exposure record is what every consumer already reads.

## BUG-0439 — An armed host read its own `sbd` as another program holding the watchdog

- Date: 2026-09-22
- Symptom: On real or emulated hardware an armed host reported `another program on this host already
  holds its watchdog device` on the next pass, was recorded not armed with `sbd` disabled, held the
  arming turn for ever, and the pool never armed.
- Root cause: The sysfs state reads `active` as soon as the device is opened, which this host's own
  `sbd` does, and the in-use gate ran before the armed check.
- Fix: `poolWatchdogArmingIntent` (`internal/agent/pool_watchdog_arming.go`) refuses only a device
  another program holds: `if w.InUse` becomes `if w.InUse && !armed`. The armed short-circuit is
  deliberately not hoisted above the maintenance gate.
- Regression: `TestAnArmedHostDoesNotReadItsOwnSBDAsAnotherProgram`
  (`internal/agent/pool_watchdog_arming_test.go`) fails on the old gate with `an armed host stopped
  feeding its watchdog because its own sbd holds the device`. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.854s`; `go test -count=1 -race ./internal/agent/` →
  `ok nex.local/nex/internal/agent 29.176s`.
- Avoid: Hoisting the armed short-circuit above the maintenance gate, which would keep a host
  feeding its watchdog after an operator asked for maintenance.

## BUG-0440 — Draining two hosts of a four-host pool reset the two healthy survivors

- Date: 2026-09-22
- Symptom: A four-host pool with every host armed, two hosts put into maintenance, reset the two
  healthy hosts that stayed. It reproduces only when the drained hosts sort before the survivors by
  host ID, which is why the existing four-host drain test never saw it.
- Root cause: `poolClusterMayLeave` let a host leave the cluster whenever it was no longer in the
  derived nodelist, even while peers were still feeding watchdogs. That escape was written for a
  drain in a pool that keeps its cluster, where the survivors reload the nodelist and stop counting
  the departing vote. In a teardown (`want == false`) nobody reloads anything, because
  `applyPoolCluster` only runs on the wanted path: the two maintenance hosts stopped feeding, dropped
  out of the nodelist and left, and the two hosts still feeding were 2 live votes against an on-disk
  expectation of 4, went inquorate, and `sbd` reset them.
- Fix: `want` is threaded into `poolClusterMayLeave` (`internal/agent/pool_cluster.go`) and the
  escape becomes `feeding == 0 || (want && !poolClusterContains(nodes, hostID))`. During a teardown
  the master strips the quorate side's bound first and `poolWatchdogTeardownTurn` refuses a turn
  while it is set, so an unarmed node lingering on the cluster is no longer the case the escape
  guarded against.
- Regression: `TestDrainingTwoHostsOfFourTearsThePoolDownWithoutResettingAnybody`
  (`internal/agent/pool_cluster_test.go`) fails on the unfixed code with `draining two hosts of four
  reset [member-2 member-3]` and passes with the fix. `go test -count=1 ./internal/agent/` →
  `ok nex.local/nex/internal/agent 11.981s`; `go test -race -count=2 -run 'Cluster|Watchdog'
  ./internal/agent/` → `ok nex.local/nex/internal/agent 4.351s`.
- Avoid: Writing the regression with arbitrary drained hosts (draining `member-2` and `member-3`
  passes on the buggy code because teardown turns run in host-ID order), and fixing it by having
  teardown hosts reload a shrunken nodelist, which lowers expected votes under peers that are still
  feeding and is the same reset in the other direction.

## BUG-0441 — The release's final reachability check failed on the site's own post-publish 503

- Date: 2026-09-22
- Symptom: Release run 4319 built, published the release, packages and website, then failed with
  `/ returned 503` half a second after `website-ci-deploy: published`. The site answered 200 for
  every checked path moments later and its package index listed the version.
- Root cause: The reachability step issued one request per path with no retry, and curl treats a
  5xx as retryable only when `--retry` is given; the website host answers 503 briefly while a
  publish settles.
- Fix: Both `release.yml` copies pass `--retry 10 --retry-delay 3` to the reachability `curl`
  calls, so a transient 503 is retried for about thirty seconds while a persistent one, a 401 or
  a missing version still fail.
- Regression: `test_public_checks_use_headers_and_still_reject_unauthorized`
  (`tests/test_release_reachability.py`) gained a `settling=1` case whose fixture answers the first
  HEAD with 503; it fails on the unfixed workflow text with `/ returned 503` and passes with the
  fix. `python3 -m unittest tests/test_release_reachability.py` → OK.
- Avoid: Retrying inside the shell loop, which curl already does, or dropping the root path from
  the check.

> BUG-0418 to BUG-0440 were found in pre-merge review of unreleased working-tree work — the
> automatic host fencing slices, the host hardware and sensor slices, the Stats palette change and
> the release-publishes-website workflow change — plus one found by the independent review of
> those fixes (BUG-0440) — before any of it was committed or any ISO or
> package carrying it was built. Their regression evidence is the Go, Node and Python checks named
> in each entry, every one of them red-to-green: each fix was reverted in place, its test observed
> failing with the message quoted in the entry, and the fix restored. None of it is hardware
> evidence. No physical management controller was contacted, no host was powered off, no physical
> watchdog was read, armed or expired, and no release was published; the fencing measurements behind
> these entries come from a nested virtual laboratory, recorded in
> [QA evidence](qa/2026-09-22-automatic-host-fencing.md). The concurrent review runs also saw two
> transient failures in `internal/agent` (`TestAHostFinishesAnAccountAControllerRefusedHalfway` and
> `TestAnArmedMasterIsRestrictedOnTheBootAfterItsWatchdogCouldHaveResetIt`) while other agents were
> mid-edit in the same package. Both were then run eight times in a row and three times under the
> race detector on the settled tree (`go test -count=8 -run '<name>|<name>' ./internal/agent/`, then
> `-race -count=3`), all passing: the failures were build races between concurrent editors, not
> defects.

## BUG-0442 — The Updates page reported a failure while the service restarted mid-update

- Date: 2026-09-22. Status: fixed in the working tree; regression added; not exercised against a live update (see Regression).
- Symptom: selecting **Update** on the Controller's **Updates** page ended in an error (`Failed to fetch`) part-way through, with the browser console showing `/api/v1/updates` `ERR_CONNECTION_REFUSED`. The Core local page behaves the same way.
- Root cause: both pages polled `GET /api/v1/updates` once a second after the apply request and treated any rejected poll as the update failing. The privileged helper stops the service before `apt-get install` and starts it after, so the poll is refused for the whole install; the first refused fetch ended the loop with the browser's own error text, whatever the helper went on to report.
- Fix: one shared `followUpdate` (`web/src/updates.ts`) replaces the two copied loops. A poll that cannot reach the service is skipped and the loop carries on; only a reported `done` or `failed` phase, an HTTP error (a response with a status) or the ten-minute bound ends it. Net −21 lines across the two pages.
- Regression: `web/src/updates.test.mjs` — a poll sequence of two refused fetches, then `restarting`, then `done` must resolve `done`; the same sequence rejected the previous inline loop with `Failed to fetch` (checked with the old loop body in a scratch script). Also covers a reported failure, an HTTP error surfacing and the bound. Command: `npm --prefix web test` (with `npm --prefix web run typecheck`), run by both `phase1` workflow copies. Not verified against a live update: the lab Controller VM was unreachable during this fix, so the helper's actual outcome there is unknown.
- Avoid: ending a wait for a restart on the first lost connection; keeping two copies of the same poll loop.

## BUG-0444 — Minimal install walked every NIC after management selection

- Date: 2026-09-22. Status: fixed in the working tree; regression added; not exercised on a booted ISO (see Regression).
- Symptom: after choosing the management adapter in the text installer, the next screen was IPv4 configuration for the first detected NIC, which often was not the one just chosen. The wizard then asked the same questions for every remaining adapter, so a multi-NIC host felt like it had shown the wrong interface.
- Root cause: the text wizard always appended one `nic:` step per detected adapter and defaulted leftover adapters to `disabled` only after the operator answered each mode question. There was no bulk skip after the management choice.
- Fix: after management selection, ask **Configure more network adapters?** (default no). No leaves every leftover adapter disabled and skips their questions; yes walks them with choices **skip**, **dhcp**, **static** (skip stored as `disabled` in the handoff). The management adapter is still always configured when one was chosen.
- Regression: `tests/integration/installer-text-wizard.py` — default Enter path asks other-adapters then only the management NIC; `scenario_configure_other_adapter` walks leftovers with skip/dhcp/static; `scenario_management_not_first_nic` configures eth1 as management while eth0 stays disabled without a mode question. Command: `python3 tests/integration/installer-text-wizard.py`, run by the installer workflow copies. Untested on a real newt console / booted minimal ISO.
- Avoid: walking every adapter as a mandatory step when only the management NIC is required for a normal install.

## BUG-0443 — A mirror that lost one disk took the whole repository out of service

- Date: 2026-09-22. Status: fixed in the working tree; regression added; verified in the disposable ZFS lab (see Regression).
- Symptom: offlining one member of a provisioned two-disk ZFS mirror made the repository report unreadable and unwritable with `ZFS pool health is DEGRADED`, so VM creation, start and migration were refused on it, while ZFS itself was still serving every read and write from the surviving disk. The 2026-09-19 ZFS rollout QA observed this and recorded it as accepted behaviour.
- Root cause: `inspectZFS` (`internal/vmhost/storage_zfs.go`) treated every pool health other than `ONLINE` as an error, and `storageVerify` accepted only `Health == "healthy"`. Redundancy was therefore cosmetic: losing one disk of a mirror lost the repository until it was repaired, which is the exact failure the mirror exists to survive.
- Fix: `inspectZFS` maps `ONLINE` → `healthy` and `DEGRADED` → `degraded` with access unchanged; `FAULTED`, `UNAVAIL`, `SUSPENDED` and `REMOVED` still fail with the health in the error. `storageVerify` asks the new `storageServesData` instead of comparing to one literal, so every caller (VM start, storage guard, registration and update) accepts a degraded pool through the same check. Core also reports `zpool status --json` detail — pool state, ZFS's own status/action sentences, the scrub or resilver, and each leaf by id with its error counts — on the per-host ZFS observation; the Controller passes it through and stores none of it. Decided in [the software RAID plan](plans/2026-09-22-software-raid.md).
- Regression: `internal/vmhost/zfs_status_test.go` maps every pool health through `inspectZFS` (`DEGRADED` must stay readable, writable and acceptable to `storageServesData`; the four inaccessible states must still be refused) and decodes `zpool status --json` fixtures into the reported detail: two captured from a lab host (a mirror with one member offlined, and a resilver in progress) and one, `zpool-status-idle-derived.json`, derived from a capture of the same pool by removing what the running resilver added, because every capture taken of that pool had a resilver in flight. The derived file says so in its name and in the test comment. Commands: `go test ./internal/...` and `go vet ./internal/...`, both run by the `phase1` workflow copies; `npm --prefix web test` covers `zfsPoolSummary` and that a degraded pool stays placement eligible in the UI. Lab evidence, including the same offline-member check with a VM running and a second VM created on the degraded pool: [docs/qa/2026-09-22-zfs-degraded.md](qa/2026-09-22-zfs-degraded.md).
- Avoid: treating a ZFS health string as a binary healthy/failed test, and gating access on one literal in each caller instead of one shared predicate. Do not read pool detail with a hand-written parser of `zpool status` text; the JSON output is stable and already installed.

## BUG-0445 — A retried disk replacement would have wiped the disk the pool was resilvering onto

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped slice 2 code, so it never reached a host.
- Symptom: none observed. `ZFSReplacer.Replace` is meant to adopt a pool that is already resilvering onto the replacement, so a retry after an interruption does nothing. The adoption never matched a real pool, so a retry of an operation that was interrupted after `zpool replace` had started would have run `wipefs --all` on the replacement disk again — a disk the pool was at that moment resilvering onto, and the only copy of the data the failed member no longer held.
- Root cause: the adoption compared the replacement's whole-disk `/dev/disk/by-id/...` name against the pool's leaf name and path, and against the block device both resolve to. A provisioned pool names its leaves by the partition ZFS created on the selected disk (`...-part1`), so none of the three comparisons could ever match; the whole-disk-to-partition rule existed on the master (`internal/agent/zfs_replace.go`) but not in the host worker. With adoption missed, the failed leaf — still reported inside the `replacing-` vdev, which the observation flattens — looked like an ordinary non-`ONLINE` leaf and the destructive path continued.
- Fix: `internal/vmhost/zfs_replace.go` matches the replacement with a new `zfsPoolHoldsDisk`, which accepts the disk itself or any partition of it, before anything is decided about the failed leaf. `zfsPoolLeaf` keeps matching the failed leaf exactly as the observation reports it, which is what the operator picked, and its unreachable resolved-path comparison is gone.
- Regression: `internal/vmhost/zfs_replace_test.go` has two adoption checks that both fail without the fix — one against a hand-built observation whose leaves are `-part1` paths (it recorded `wipefs --all` on the live replacement), and one against `testdata/zpool-status-replacing-derived.json` decoded by the real decoder. Command: `go test ./internal/...`, run by the `phase1` workflow copies. The fixture is derived, not captured: the captured degraded output with its leaves renamed to by-id partition paths, the failed leaf and its replacement moved into a `replacing-0` vdev and the captured resilver's scan copied in. Re-running the interrupted-retry case on a host, with the interruption placed after `zpool replace` has started, is outstanding: [docs/qa/2026-09-22-zfs-replace.md](qa/2026-09-22-zfs-replace.md).
- Avoid: comparing a selected whole disk against a ZFS leaf name without the partition rule, and putting a safety check behind a comparison no real observation can satisfy. When two processes encode the same naming rule, share it or test both against the same captured output.

## BUG-0446 — md_arrays was never asserted in the host hardware report

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-007).
- Symptom: none observed. Deleting `hw.MDArrays = vmhost.ReadMDArrays(root)` left every host page with an empty md_arrays section and Replace disk… unreachable, while `go test ./...` stayed green.
- Root cause: `readHardware` wired ReadMDArrays with no agent test referencing MDArrays, so the wiring was invisible to CI.
- Fix: regression only — assert `hw.MDArrays.Unavailable` on an empty fixture root, and assert a fixture md0 appears in `TestReadHardwareReportsEverySection`.
- Regression: `TestReadHardwareWithoutSysfsOrProc` and `TestReadHardwareReportsEverySection` (`internal/agent/hardware_test.go`). Command: `go test ./internal/agent/ -count=1`. Removing the ReadMDArrays assignment fails both assertions.
- Avoid: shipping a host-report section without an agent test that fails when its wiring line is deleted.

## BUG-0447 — No agent-side test admitted or routed host-system-disk-replace

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-008).
- Symptom: none observed. Dropping `host-system-disk-replace` from `managedCommand` or its `commandHost` case left every replacement discarded or failing host-identity admission, while agent tests stayed green.
- Root cause: the agent admission, host addressing and ExecuteSystemDiskReplace guards had no tests, unlike the management-network precedent.
- Fix: regression only — assert managed admission and HostID routing, and that nil-request and mismatched-operation-ID refusals fail without writing under the system-disk-replace state dir.
- Regression: `TestSystemDiskReplaceCommandRoutingAndAdmission` and `TestSystemDiskReplaceRefusalsAreDefiniteResults` (`internal/agent/md_replace_test.go`). Command: `go test ./internal/agent/ -count=1`.
- Avoid: adding a managed host command without the same routing and admission checks the management-network tests already provide.

## BUG-0448 — System disk mirror rebuild and task polls never fired on the Stats tab

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-004, SR-084).
- Symptom: on a host Stats tab (default Last 10 minutes), a rebuilding mirror's percentage stayed frozen and a submitted Replace disk… task never showed its success or failure notice until Retry was pressed by hand.
- Root cause: Stats passed a fresh `onRetry={() => setHardwareAttempt(...)}` arrow on every render (every 5s from the history poll). SystemDiskMirror listed that callback (and `api`) in two `useEffect` dependency arrays, so the 10s rebuild interval and the 5s operations interval were cleared and recreated before they could fire.
- Fix: hold `onReplaced` and `api` in refs inside SystemDiskMirror and depend only on `[rebuilding]` / `[task?.id, task?.status]`; give Stats a stable `useCallback` for the hardware reread and pass that as `onRetry`.
- Regression: `web/src/md.test.mjs` asserts the intervals call `onReplacedRef.current` / `apiRef.current` and that the effect deps are only rebuilding and task id/status (not onReplaced/api). `web/src/stats.test.mjs` asserts `rereadHardware = useCallback(...)` is what HardwarePanel receives. Without the refs or with the old `[rebuilding, onReplaced]` deps, those asserts fail. Command: `npm --prefix web test` (md/stats); also `npm --prefix web run typecheck`.
- Avoid: putting parent-render identity callbacks in long-interval effect deps when a ref to the latest function is enough; pairing that with an unstable inline arrow from a frequently re-rendering parent.

## BUG-0449 — Empty Replace disk… picker always said every spare was too small

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-015, SR-005).
- Symptom: opening Replace disk… on a host where every disk was in use showed "Every spare disk on this host is smaller than the remaining system disk …" instead of "This host reports no disk that can be used …". The guide's distinction between the two cases was unreachable.
- Root cause: the empty-state ternary used `usable.every(small)` while the message rendered only when `usable.length === 0`. `Array.prototype.every` is vacuously true on an empty array, so the too-small branch always won; the too-small case that keeps a non-empty disabled list never showed that sentence either.
- Fix: when `usable.length === 0` show the no-disk sentence; when usable is non-empty and every candidate is small, show the too-small sentence alongside the disabled picker. Guide troubleshooting already matched the intended wording; the system-disk replace QA record was annotated that the walkthrough's empty-state quote did not match the buggy shipped branch.
- Regression: `web/src/md.test.mjs` asserts `usable.length === 0 ? "This host reports no disk…"` and that `const empty = usable.every(small)` is gone; without the length check the first pattern fails. Command: `npm --prefix web test`.
- Avoid: using `every` to choose between empty-list messages; an empty list is not "every item failed the predicate".

## BUG-0450 — System disk replacement journal was written without fsync

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped software-RAID code (SR-011), so it never reached a host.
- Symptom: none observed. After a wipe started, a power loss could leave the journal truncated or empty so every later resume was refused ("journal is unreadable" or fingerprint mismatch) while the disk was already wiped.
- Root cause: `saveSystemDiskJournal` hand-rolled temp+rename with `os.WriteFile` and no file or directory fsync. The package already had `atomicWrite` / `AtomicWrite`, which `saveZFSReplaceJournal` uses.
- Fix: `saveSystemDiskJournal` marshals then calls `atomicWrite(path, raw)` like the ZFS replace journal.
- Regression: `TestSaveSystemDiskJournalUsesAtomicWrite` in `internal/vmhost/md_replace_test.go` — the function body must call `atomicWrite` (and must not contain `WriteFile` / `.tmp`), and a round-trip read must recover the wipe-started journal. Command: `go test ./internal/vmhost/ -count=1 -run TestSaveSystemDiskJournalUsesAtomicWrite`.
- Avoid: a second temp+rename writer in the same package when `atomicWrite` already exists for durable journals.

## BUG-0451 — System disk replacement accepted OperationIDs that escape the journal directory

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped software-RAID code (SR-017), so it never reached a host.
- Symptom: none observed. An `operation_id` containing path separators would be joined into the journal path and could write outside the state directory.
- Root cause: `SystemDiskReplaceRequest.Valid` only required a non-empty bounded string for `OperationID`, not the opaque-ID charset used elsewhere for filename-safe identifiers.
- Fix: reject `OperationID` values that do not match `storageOpaqueID` in `SystemDiskReplaceRequest.Valid`.
- Regression: `TestSystemDiskReplaceRequestValidation` in `internal/protocol/systemdisk_test.go` — `../../../../etc/cron.d/nex` and `op/../evil` must be refused. Without the charset check those cases were accepted. Command: `go test ./internal/protocol/ -count=1 -run TestSystemDiskReplaceRequestValidation`.
- Avoid: building journal paths from request fields that are only length-checked.

## BUG-0452 — System disk replacement journal kept the previous failure after a successful retry

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped software-RAID code (SR-018), so it never reached a host.
- Symptom: none observed. After a failed replace succeeded on retry, the journal still carried the earlier error string beside status `succeeded`.
- Root cause: `SystemDiskReplacer.Replace` set status and ObservedAt on the success path but left `journal.Error` uncleared; `ZFSReplacer` already clears it.
- Fix: on success set `journal.Error` to `""` with status and ObservedAt, matching the ZFS replacer.
- Regression: `TestReplaceSystemDiskResumesAfterInterruptedWipe` in `internal/vmhost/md_replace_test.go` — after the successful retry the journal must be `succeeded` with an empty Error. Command: `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskResumesAfterInterruptedWipe`.
- Avoid: writing a succeeded journal without clearing the previous failure field.

## BUG-0453 — System disk UEFI-without-ESP refusal was not exercised

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped software-RAID code (SR-012), so it never reached a host.
- Symptom: none observed. `TestReplaceSystemDiskOnBIOSWithoutAnESP` set `noESP=true` on a BIOS firmware stub that returns before `lsblk`, so the no-ESP fixture branch was dead.
- Root cause: the test intended a missing-ESP case but left `mdFirmwareEFI` pointing at a non-existent path, so only the BIOS early return ran.
- Fix: replace that test with `TestReplaceSystemDiskRefusesUEFIWithoutAnESP`, which points `mdFirmwareEFI` at a real directory, sets `noESP`, and asserts the refusal plus a `member-added` journal after the member is already added.
- Regression: `TestReplaceSystemDiskRefusesUEFIWithoutAnESP` in `internal/vmhost/md_replace_test.go`. Command: `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskRefusesUEFIWithoutAnESP`.
- Avoid: stubbing a firmware/partition fixture that the code path under test never reads.

## BUG-0452 — ZFS disk replacement accepted OperationIDs that escape the journal directory

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by independent review of unshipped software-RAID code (SR-034), so it never reached a host.
- Symptom: none observed. An `operation_id` containing path separators would be joined into the ZFS replace journal path and could write outside the state directory.
- Root cause: `ZFSReplaceRequest.Valid` only required a non-empty bounded string for `OperationID`, not the opaque-ID charset used elsewhere for filename-safe identifiers (including the sibling system-disk replace validator).
- Fix: reject `OperationID` values that do not match `storageOpaqueID` in `ZFSReplaceRequest.Valid`.
- Regression: `TestZFSReplaceRequestValidation` in `internal/protocol/storage_test.go` — `../../../../etc/cron.d/nex` and `op/../evil` must be refused. Without the charset check those cases were accepted. Command: `go test ./internal/protocol/ -count=1 -run TestZFSReplaceRequestValidation`.
- Avoid: building journal paths from request fields that are only length-checked.

## BUG-0453 — managedKind listed host-system-disk-replace though no host reports that outcome

- Date: 2026-09-22. Status: fixed in the working tree; found by independent review of unshipped software-RAID code (SR-016), so it never reached a host.
- Symptom: none observed. The entry was unreachable: system disk replace settles via `protocol.Result` from `ExecuteSystemDiskReplace`, not via managed outcomes, so no host ever reports a `host-system-disk-replace` outcome for adoption to settle.
- Root cause: `managedKind` gained the kind when the command was added, mirroring command admission, without a matching managed-engine outcome path.
- Fix: drop `host-system-disk-replace` from `managedKind`. Outcome reporting would be the other correct fix if adoption via managed outcomes were required; the command already settles through results, so the dead filter entry was wrong rather than incomplete recovery.
- Regression: covered by existing controller/protocol suites remaining green after the removal; no separate assertion of an absent switch case. Command: `go test ./internal/controller/ -count=1`.
- Avoid: adding kinds to `managedKind` unless Core actually emits matching `ManagedOutcome` rows.

## BUG-0470 — Version light pulsed green for non-Nex-Core OS strings

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-079).
- Symptom: a host reporting plain Debian (or any digit-bearing PRETTY_NAME) showed a pulsing green "up to date" light beside Version when the archive major was 0. An apt candidate with an epoch or `-N` revision also mis-coloured up-to-date Nex Core hosts.
- Root cause: `versionLight` scraped bare digit runs from any installed string and never required "Nex Core"; it also treated epoch/revision digits as version segments.
- Fix: require a `Nex Core <version>` token before lighting; strip Debian epoch and package revision before the numeric compare.
- Regression: `web/src/updates.test.mjs` — Debian/Ubuntu strings return undefined; `Nex Core 0.4.3` against `1:0.4.3-1` and `0.4.3-1` is current. Command: `node web/src/updates.test.mjs`.
- Avoid: comparing host OS pretty-names as if they were Nex Core package versions.

## BUG-0471 — Pre-release hosts read green when the matching final was in the archive

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-080).
- Symptom: a host on `0.5.0~alpha1` showed green "up to date" against archive `0.5.0`; a final host against `0.4.3~rc1` showed orange as if an upgrade existed.
- Root cause: digit scraping treated `~alpha1` leftover digits as equal-or-newer once the archive's segments matched; the reverse advertised a pre-release candidate as behind.
- Fix: split pre-release on `~`, treat a pre-release host against a matching final as behind, leave a final host current against a pre-release candidate, and treat leftover base segments after a full archive match as behind.
- Regression: `web/src/updates.test.mjs` — `0.5.0~alpha1` vs `0.5.0` is behind; `0.4.3` vs `0.4.3~rc1` is current; `0.5.0.1` vs `0.5.0` is behind. Command: `node web/src/updates.test.mjs`.
- Avoid: treating extra digit runs from Debian pre-release suffixes as proof the host is current.

## BUG-0472 — Version light survived sign-out for a non-administrator

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-082).
- Symptom: after an administrator viewed a host (populating update state), signing out and signing in as a non-admin left the version light lit from the stale `core_available_version`.
- Root cause: sign-out and 401 handling cleared session/data/token but not `update`.
- Fix: `setUpdate(null)` beside the other clears on sign-out and on 401.
- Regression: covered by the clear sites in `web/src/main.tsx`; no separate UI unit assertion. Command: `node web/src/updates.test.mjs` (versionLight suite remains green).
- Avoid: leaving host-scoped fetch caches across account changes when the next role may not be allowed to refresh them.

## BUG-0473 — Replace disk offered and accepted during an active md rebuild

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-014).
- Symptom: none observed on a host. While recover/resync/reshape was running, the Stats panel still offered **Replace disk…** on a degraded array, and Core would accept a second replacement that fought the running copy.
- Root cause: the UI gated the button only on `degraded` and Core only checked level/degraded/size/eligibility; neither consulted `sync_action`.
- Fix: hide Replace while `sync_action` is recover, resync or reshape and say the rebuild must finish; Core refuses a fresh replace for those actions (adoption of an already-added member still proceeds so bootloader resume works).
- Regression: `web/src/md.test.mjs` asserts `mdArrayRebuilding` and the button gate; `TestReplaceSystemDiskRefusesRunningRebuild` in `internal/vmhost/md_replace_test.go` refuses recover/resync/reshape without wipe and still adopts during recover. Commands: `node --test web/src/md.test.mjs` (or `npm --prefix web test`); `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskRefusesRunningRebuild`.
- Avoid: offering or accepting a destructive md member add while the kernel is already copying.

## BUG-0474 — Every md array summarised as a mirror

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-021).
- Symptom: none observed on a host. A healthy non-raid1 array would still be described as "Mirror of N disks", and Replace disk… could appear on any degraded level if Core reported it.
- Root cause: `mdArraySummary` hard-coded "Mirror"; the Replace button gated only on degraded, not `level === "raid1"` (Core already refused non-raid1).
- Fix: summarise with `array.level`; offer Replace only for RAID1.
- Regression: `web/src/md.test.mjs` asserts `raid1 of …` / `raid0 of …` summaries and the `level === "raid1"` button gate. Command: `node --test web/src/md.test.mjs` (or `npm --prefix web test`).
- Avoid: operator copy that assumes every md array is a mirror.

## BUG-0482 — System-disk and ZFS replace lacked a real host lock

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-001, SR-031).
- Symptom: none observed on a host. Concurrent system-disk replace and ZFS replace (or a second peer replace) could both pass eligibility and wipe the same spare disk.
- Root cause: each replacer used a per-instance mutex on a fresh value, which serializes nothing; only ZFS provision took a flock, and that flock was scoped to its own journal directory.
- Fix: `withExclusiveDiskLock` on the host state directory (`.disk-ops.lock` beside `zfs-provision`, `zfs-replace` and `system-disk-replace`); drop the useless per-instance mutexes; provision uses the same helper.
- Regression: `TestDiskOpsShareExclusiveLockAcrossReplacers`, `TestZFSReplaceTakesHostDiskOpsLock`. Command: `go test ./internal/vmhost/ -count=1 -run 'TestDiskOpsShareExclusiveLockAcrossReplacers|TestZFSReplaceTakesHostDiskOpsLock'`.
- Avoid: guarding host-destructive work with a mutex on a value created per call.

## BUG-0483 — Bootloader resume refused forever once the member reached in_sync

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-002, SR-010).
- Symptom: none observed on a host. After mdadm --add succeeded and the bootloader step failed, once the kernel finished rebuilding the member became in_sync and every retry returned "already holds a working member".
- Root cause: the in_sync refusal ran before the journal stage was consulted; member-added/bootable stages did not drive adoption.
- Fix: adopt when the journal stage is member-added or bootable (or the live member is spare/rebuilding/replacement); keep the in_sync refusal only for a first attempt (empty stage).
- Regression: `TestReplaceSystemDiskResumesBootloaderAfterMemberInSync`. Command: `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskResumesBootloaderAfterMemberInSync`.
- Avoid: treating live sync state alone as proof the operator picked the surviving disk after this operation already added the member.

## BUG-0484 — Faulty attached member treated as an adopted replacement

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-009).
- Symptom: none observed on a host. Naming a disk that still carried a faulty md member skipped wipe/add and reported success after only the bootloader step.
- Root cause: any non-in_sync member on the chosen disk set adopted.
- Fix: adopt only spare/rebuilding/replacement (or journal-recorded stages); refuse a faulty member on the chosen disk with its own sentence.
- Regression: `TestReplaceSystemDiskRefusesFaultyMemberOnChosenDisk`. Command: `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskRefusesFaultyMemberOnChosenDisk`.
- Avoid: treating "not in_sync" as "already rebuilding onto this disk".

## BUG-0485 — UEFI ESP copy failed when /boot/efi was the failed disk

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-003).
- Symptom: none observed on a host. When fstab mounted /boot/efi from the failed disk, replace formatted the new ESP then failed at the copy with no usable source.
- Root cause: nex-sync-esp and the inline copy both required /boot/efi/EFI; neither fell back to the surviving member's ESP.
- Fix: when /boot/efi/EFI is absent, mount the surviving disk's ESP and copy from there; errors name the missing sources.
- Regression: `TestReplaceSystemDiskCopiesESPFromSurvivingMemberWhenBootEFIMissing`. Command: `go test ./internal/vmhost/ -count=1 -run TestReplaceSystemDiskCopiesESPFromSurvivingMemberWhenBootEFIMissing`.
- Avoid: assuming the failed disk's fstab ESP mount is still present after replacement starts.

## BUG-0475 — Temperature gauge hid Warm/Critical from assistive tech

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-085).
- Symptom: none observed on a host. A Critical or Warm temperature tile announced only the Celsius value; the state word lived inside the meter’s presentational subtree and the coloured bar was aria-hidden.
- Root cause: the state span was moved inside the `role="meter"` element without folding it into `aria-valuetext`.
- Fix: `gaugeValueText` appends Warm/Critical to `aria-valuetext`; the visible state span is `aria-hidden`.
- Regression: `web/src/stats.test.mjs` asserts `gaugeValueText` and the meter markup. Command: `cd web && node src/stats.test.mjs`.
- Avoid: putting the only non-visual threshold signal inside a meter without updating valuetext.

## BUG-0476 — md_arrays null and unavailable both read as “single disk”

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-086).
- Symptom: none observed on a host. An older Core (no md_arrays on the wire) and a current Core that set `unavailable` both showed “Single system disk, no redundancy”.
- Root cause: `reported={!!hardware.md_arrays}` is always true after Controller remarsal, and `unavailable`/`reason` were never read.
- Fix: `mdArraysView` treats `arrays:null` as unreported, `unavailable` as the host’s reason, and `arrays:[]` as the single-disk empty state.
- Regression: `web/src/stats.test.mjs` asserts each `mdArraysView` branch. Command: `cd web && node src/stats.test.mjs`.
- Avoid: treating a remarsalled zero-value section as proof the host reported arrays.

## BUG-0477 — Hardware table header lost its bottom border

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-088).
- Symptom: none observed on a host. Expanding Disks or PCI dropped the rule under the column headers.
- Root cause: `.stats-hw-table tr:last-child` matched the sole thead row as well as the last body row.
- Fix: scope to `tbody tr:last-child`.
- Regression: `web/src/stats.test.mjs` asserts the tbody-scoped rule. Command: `cd web && node src/stats.test.mjs`.
- Avoid: last-child rules on tables that have a thead.

## BUG-0478 — System disk block and Healthy contrast on the Stats canvas

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-089, SR-090).
- Symptom: none observed on a host. The md-arrays block sat uncarded on `#f7f8fa`, and `.md-healthy` was ~4.1:1 on that canvas.
- Root cause: restyle carded every hardware section except `.md-arrays`; Healthy kept the old green.
- Fix: give `.md-arrays` the same white card treatment; darken Healthy to `#166534`.
- Regression: `web/src/stats.test.mjs` asserts card background and ≥4.5:1 contrast. Command: `cd web && node src/stats.test.mjs`.
- Avoid: body text colours that only cleared 3:1 when the panel was white.

## BUG-0479 — Replace disk offered without storage discovery caps

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-091).
- Symptom: none observed on a host. A host with `host-system-disk-replace-v1` but without storage discovery still showed Replace disk…; opening it failed on `storage/discover`.
- Root cause: the button gated only on the system-disk capability.
- Fix: also require `storage-discovery-v1` and `storage-zfs-provision-v1` (`systemDiskReplaceSupported`).
- Regression: `web/src/stats.test.mjs` asserts the three-cap gate. Command: `cd web && node src/stats.test.mjs`.
- Avoid: offering a dialog whose first call needs capabilities the button did not check.

## BUG-0475 — Storage guide Verification claimed unobserved disk-replace coverage

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-022).
- Symptom: `docs/guide/storage-repositories.md` Verification said three-disk mirrors, hot spares and Replace a failed disk were walked through in the browser and through the API, named only two limits, and closed with "Everything else on this page about replacing a disk was observed", while `docs/qa/2026-09-22-zfs-replace.md` marks three-way/hot-spare as reported-not-evidence and lists outstanding browser and lab items.
- Root cause: guide Verification was written ahead of the QA record's limits and treated planned or reported checks as observed.
- Fix: state the actual limits — API mirror replacement observed with retained output; three-way mirror and hot-spare checks run without retained evidence; reworked disk tables, Spare column, layout fieldset, live summary, degraded host row and confirm button not seen in a browser; RAIDZ3, automatic spare engagement and post-`zpool replace` interrupted retry outstanding.
- Regression: docs accuracy only; re-read the guide Verification against `docs/qa/2026-09-22-zfs-replace.md` Outstanding and Untested limits. No automated behaviour check.
- Avoid: closing guide Verification with "everything else was observed" when the linked QA record still lists unexecuted or unevidenced items.

## BUG-0476 — Degraded ZFS QA presented a derived idle fixture as a lab capture

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-029).
- Symptom: `docs/qa/2026-09-22-zfs-degraded.md` and `docs/changes/2026-09-22-zfs-degraded.json` called all three `zpool status --json` fixtures captures from the lab host, including a healthy/idle mirror, while `zpool-status-idle-derived.json`, the test comment and BUG-0443 already label that file derived.
- Root cause: QA and change verification text treated the three fixture files as the same kind of evidence.
- Fix: say two fixtures are captured (degraded, resilvering) and the idle file is derived by removing an in-flight resilver; carry "capture a genuine idle ONLINE pool" into the QA limits.
- Regression: docs accuracy only; re-read against `internal/vmhost/zfs_status_test.go` fixture comment and the derived filename. No automated behaviour check.
- Avoid: describing a hand-edited or derived fixture as an untouched lab capture.

## BUG-0480 — System disk replace submit covered only by source regexes

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-013).
- Symptom: none observed on a host. The replace dialog's POST path, request_id replay and erase_confirmation gating were asserted only by matching `md.tsx` source text.
- Root cause: no stub-driven submit helper; follow timers lived only inside `useEffect`.
- Fix: export `mdReplaceSubmit`, `mdPollRebuild` and `mdPollOperation`; drive submit with a stub API and poll with injectable timers.
- Regression: `web/src/md.test.mjs` asserts path `hosts/<id>/system-disk`, request_id reuse after a rejected call, erase_confirmation only for erase_required, and visibility/status behaviour of the poll helpers. Command: `node web/src/md.test.mjs`.
- Avoid: treating source regexes as proof that the request body and retry identity are correct.

## BUG-0481 — System disk replace duplicated ZFS disk picker helpers

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-019, SR-020).
- Symptom: none observed on a host. Replace disk… kept its own size/shortName/dirty/BlockedDisks copies and a separate radio list instead of the shared ZFS table.
- Root cause: helpers lived as private copies in `md.tsx` instead of imports from `zfs.tsx`.
- Fix: export the shared picker helpers from `zfs.tsx`; system disk replace imports them and adds only the Fit column for the size requirement.
- Regression: `web/src/md.test.mjs` asserts the zfs import and Fit column; `web/src/zfs.test.mjs` asserts the shared exports. Commands: `node web/src/md.test.mjs`; `node web/src/zfs.test.mjs`.
- Avoid: copying disk-picker presentation between ZFS and system-disk dialogs.

## BUG-0486 — ZFS replace guide pointed at the wrong panel

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-027).
- Symptom: none observed on a host. The Replace a failed disk steps told operators to open the degraded repository row and select Replace disk there; that row has no such control.
- Root cause: guide step 2 described the Pool storage repositories table; Replace disk is rendered only in the ZFS storage by host Action column, and only when the host is fresh and reports storage-zfs-replace-v1 plus storage-discovery-v1.
- Fix: step 2 names ZFS storage by host, the Action column, and when the button is absent.
- Regression: docs accuracy against web/src/storage.tsx Replace disk gate; no automated behaviour check.
- Avoid: documenting a destructive action on a table that only shows pool status.

## BUG-0487 — ZFS replace dialog hid the by-id erase confirmation target

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-028).
- Symptom: none observed on a host. With a dirty replacement disk selected, the erase field required typing the by-id name but the dialog never printed that name beside the input (only a Serial cell title tooltip).
- Root cause: ZFSReplaceDialog omitted the mono by-id span the system-disk dialog already renders next to its confirmation field.
- Fix: show chosen.by_id beside the confirmation input in ZFSReplaceDialog.
- Regression: web/src/zfs.test.mjs asserts the confirmation label includes `{chosen.by_id}`. Command: `node --test web/src/zfs.test.mjs` (or `npm --prefix web test`).
- Avoid: requiring a typed exact string that is only available as a tooltip.

## BUG-0488 — Registered ZFS repository hid pool detail from access.zfs_pool

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-041).
- Symptom: none observed on a host. A registered (not Core-provisioned) ZFS dataset that went degraded showed only the status word; failed device, status/action and resilver from `access.zfs_pool` were unused.
- Root cause: the TypeScript `access` type omitted `zfs_pool`, and `storageHosts` only rendered pool detail from `zfs_host_access` (provisioned path).
- Fix: add `zfs_pool?: ZFSPoolStatus` to `access` and render `zfsPoolSummary` on `kind === "zfs"` rows with no `zfs_host_access`.
- Regression: `web/src/storage.test.mjs` asserts the managed type spelling and `access?.zfs_pool` render path. Command: `node web/src/storage.test.mjs`.
- Avoid: shipping a wire field that only the provisioned per-host path reads.

## BUG-0489 — Resilver percentage and parenthetical sizes disagreed

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-042, SR-043).
- Symptom: none observed on a host. A running resilver could read "49% (1.46G of 4.89G)" when the percentage came from issued (2.44G) while the parenthetical printed examined.
- Root cause: `zfsScanProgress` used `issued ?? examined` for the percentage but always printed `examined` in the same string; the unit test froze that mismatch.
- Fix: print the same quantity the percentage was derived from (`issued ?? examined`).
- Regression: `web/src/storage.test.mjs` expects `49% (2.44G of 4.89G)`. Command: `node web/src/storage.test.mjs`.
- Avoid: deriving a percentage from one ZFS counter and labelling it with another.

## BUG-0490 — ONLINE pool with leaf errors or status stayed "Pool online"

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-044).
- Symptom: none observed on a host. A pool ZFS reported ONLINE with non-zero checksum errors and a status sentence rendered as plain "Pool online" with no attention badge.
- Root cause: `zfsPoolSummary` keyed attention only on vdev state strings and re-derived the failed-leaf list instead of using `zfsFailedDevices`.
- Fix: treat non-zero read/write/checksum counts or a non-empty `pool.status` as attention; name the leaf with its counts; call `zfsFailedDevices`.
- Regression: `web/src/storage.test.mjs` asserts checksum and status-only attention cases and that `zfsPoolSummary` calls `zfsFailedDevices`. Command: `node web/src/storage.test.mjs`.
- Avoid: treating ONLINE leaf state as healthy when ZFS itself reports errors or a status sentence.

## BUG-0491 — Hot spares and widened ZFS layouts sent without replace capability

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-023).
- Symptom: none observed on a host. After a Controller-first upgrade, the wizard offered Spare and three-disk mirror / RAIDZ3 on a pool whose Core still lacked storage-zfs-replace-v1; Core could ignore spares or refuse the whole provision.
- Root cause: provisionZFSStorage and the wizard gated only on storage-zfs-provision-v1.
- Fix: require storage-zfs-replace-v1 on the pool master and every selected host when any host carries spares or a widened layout; gate the Spare column and those layout radios on the same capability.
- Regression: `go test ./internal/protocol ./internal/controller -run 'ZFSProvisionNeedsReplace|ZFSProvisionRefusesSpares'`; `node web/src/zfs.test.mjs` asserts the replace-capable Spare/layout gates.
- Avoid: treating an optional wire field as safe for older Core because omitempty hides it on empty requests only.

## BUG-0492 — zfs_pool / pool inventory fields broke older Controllers

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-024, SR-025).
- Symptom: none observed on a host. An upgraded Core reporting any ZFS repository included Access.zfs_pool and ZFSHostAccess.pool; a strict older Controller returned 400 on every report and the agent never downgraded (409-only fallback).
- Root cause: the new observation fields had no Controller acknowledgement gate; zfsLegacyManagedProjection also kept non-provisioned ZFS entries that still carried zfs_pool.
- Fix: acknowledge storage-zfs-replace-v1 through the existing storage_repositories support shape; strip zfs_pool and pool from reports until that ack (same pattern as storageClusterResultsForController).
- Regression: `go test ./internal/agent -run ZFSPoolObservation`; `go test ./internal/controller -run ZFSCoreReceivesCapability`.
- Avoid: adding inventory fields without a Controller-first acknowledgement and a strict-old-schema decode test.

## BUG-0493 — Flattened ZFS vdev tree hid in-flight replacements and counted spares as failures

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-026).
- Symptom: none observed on a host. During a replacing-0 resilver the Storage page still offered Replace disk for the offline leaf; a second submit wiped another disk then failed at zpool replace. A hot spare under the spares class container could also appear as a failed leaf.
- Root cause: zfsPoolLeaves flattened every vdev map entry and discarded replacing/spare parentage; class containers beside the root were walked as data vdevs.
- Fix: walk only the pool root; mark leaves under replacing/spare with parent; ignore class containers; drop empty name/state leaves; refuse OldDevice with that parent before wipe; UI shows being replaced / hides Replace.
- Regression: `go test ./internal/vmhost/ -count=1 -run 'TestDecodeZFSPoolStatus(Ignores|Marks)|TestZFSReplaceRefusesOldDevice'`; `node web/src/storage.test.mjs`; `node web/src/zfs.test.mjs`.
- Avoid: flattening a ZFS vdev tree without carrying the replacing/spare parent and without skipping class containers.

## BUG-0494 — Healthy pool member adopted as a successful ZFS replace

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-038).
- Symptom: none observed on a host. Naming a disk already in the pool as the replacement returned success with nothing executed, and the master could rewrite two failed slots onto one disk.
- Root cause: zfsPoolHoldsDisk alone adopted any held disk before checking the failed leaf or a running resilver.
- Fix: adopt only when OldDevice is absent or present with a SCANNING RESILVER; otherwise refuse "already part of the pool".
- Regression: `go test ./internal/vmhost/ -count=1 -run 'TestZFSReplace(AdoptsAPoolAlready|RefusesAHealthyMember)'`.
- Avoid: treating "disk is in the pool" as proof this replace already ran.

## BUG-0495 — Replace disk stayed enabled during a ZFS resilver

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-045).
- Symptom: none observed on a host. While a resilver ran the Action cell still offered Replace disk for the offline leaf.
- Root cause: the button gated only on non-ONLINE leaves, which remain offline until resilver finishes.
- Fix: show "resilvering" while scan.state is SCANNING; hide Replace for leaves with parent replacing/spare.
- Regression: `node web/src/storage.test.mjs` asserts the scanning / beingReplaced / replaceable Action cell.
- Avoid: offering Replace while the pool observation already shows a scan or replacing vdev.

## BUG-0496 — Undersized ZFS replacement wiped before size check

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-040).
- Symptom: none observed on a host. A too-small replacement was wipefs'd then refused by zpool replace.
- Root cause: no size comparison against the failed leaf before wipe.
- Fix: compare request.Device.SizeBytes to the failed (or ONLINE sibling) whole-disk size from discovery before wipe; carry zpool's reason in the replace error.
- Regression: `go test ./internal/vmhost/ -count=1 -run TestZFSReplaceRefusesAnUndersizedReplacement`.
- Avoid: running wipefs before every replace precondition that does not need a clear disk.

## BUG-0497 — wipe-started resume skipped erase confirmation

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-039).
- Symptom: none observed on a host. After wipe-started was journalled and wipefs failed, a redelivery with no erase confirmation could wipe a disk that had gained data.
- Root cause: wipe-started skipped fingerprint and also skipped RequiresErase; no test asserted wipe-started before wipefs.
- Fix: keep RequiresErase on the wipe-started resume path; record saveZFSReplaceJournal as a var and assert wipe-started precedes wipefs.
- Regression: `go test ./internal/vmhost/ -count=1 -run TestZFSReplaceJournalsWipeStartedBeforeWipeAndKeepsEraseCheck`.
- Avoid: relaxing every identity check when only the fingerprint cannot survive a wipe.

## BUG-0498 — Hot spare skipped erase/eligibility re-check in provision tests

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-035).
- Symptom: none observed on a host. Spares use the same selected() wipe and eligibility path as data disks, but no test covered a RequiresErase spare wipe or an ineligible spare refusal.
- Root cause: provisionHarness cases only exercised Devices; a refactor back to Devices-only would stay green.
- Fix: add harness cases that wipe a RequiresErase spare and refuse an ineligible spare before any destructive call.
- Regression: `go test ./internal/vmhost/ -count=1 -run 'TestZFSProvision(WipesARequiresEraseSpare|RefusesAnIneligibleSpare)'`.
- Avoid: treating hot spares as exempt from the data-disk erase and eligibility gates.

## BUG-0499 — ZFS provision did not assert autoreplace=on for hot spares

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-036).
- Symptom: none observed on a host. Provisioning already passes `-o autoreplace=on` with a spare vdev, but no runnable check inspected the zpool create argument list.
- Root cause: create success alone was enough for green tests; dropping autoreplace would still pass.
- Fix: assert the recorded create includes `-o autoreplace=on` and ends with `spare <by-id>`, and that a spare-free create omits the property.
- Regression: `go test ./internal/vmhost/ -count=1 -run 'TestZFSProvisionCreate(EnablesAutoreplaceWhenSparesArePresent|OmitsAutoreplaceWithoutSpares)'`.
- Avoid: shipping spare support without asserting the property that makes the spare engage.

## BUG-0500 — Stale ZFS pool detail survived probe timeout and enabled Replace

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-037).
- Symptom: none observed on a host. After a storage probe deadline, Status/Readable/Writable were downgraded but ZFSPool (failed disk, resilver %) stayed attached; the by-host UI rendered it as current with Replace enabled.
- Root cause: both deadline branches mutated access in place without clearing ZFSPool or refreshing ObservedAt; the UI gated Replace only on host.connection.
- Fix: clear ZFSPool and set ObservedAt on both deadline paths; prefix by-host pool lines with Last report when the host or observation is not fresh, and gate Replace on the same freshness (keep resilvering/being-replaced gates).
- Regression: `go test ./internal/vmhost/ -count=1 -run TestStorageMonitorDeadlineClearsPlacementAccess`; `node web/src/storage.test.mjs`.
- Avoid: leaving transient pool detail on an observation Core has already marked stale.

## BUG-0501 — Spare redundancy rule bypassed by unticking a data disk

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-046).
- Symptom: none observed on a host. Tick three disks, Spare on the third, untick a data disk — the wizard could submit a single-disk pool with a hot spare.
- Root cause: toggleSpare gated the tick, but toggle and review did not re-apply the rule; zfsProvisionHosts sent whatever spare flags remained.
- Fix: demote spares when unticking leaves fewer than two data disks; refuse the same case in review; drop spares in zfsProvisionHosts when data disks < 2. Guide states the three-data-disk Spare rule.
- Regression: `node web/src/zfs.test.mjs`.
- Avoid: enforcing a selection rule only on the control that sets a flag.

## BUG-0502 — ZFS status/action sentences were hover-only and the badge did not wrap

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-056).
- Symptom: none observed on a host. Pool status and action text lived only in a title attribute on a non-focusable badge; long degraded lines forced the table wide.
- Root cause: title-only exposure plus .badge { white-space: nowrap }.
- Fix: render status/action as a visible <small> under the summary; allow the summary badge to wrap.
- Regression: `node web/src/storage.test.mjs`.
- Avoid: putting operator instruction text only in title= on non-focusable elements.

## BUG-0503 — Replace dialog kept a stale pool snapshot

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-057).
- Symptom: none observed on a host. Opening Replace disk and leaving it open while the leaf returned ONLINE still listed the disk as failed; submit hit Core's "is online" refusal instead of the dialog empty state.
- Root cause: replaceZFS state captured pool once on click and never refreshed from inventory.
- Fix: keep only repositoryID/host in state; pass pool from the live storage prop; ignore a selected leaf the live pool no longer offers.
- Regression: `node web/src/storage.test.mjs`; `node web/src/zfs.test.mjs`.
- Avoid: snapshotting observation into dialog state when the parent already reloads it.

## BUG-0504 — Guide misspelled resilvering and swallowed the erase placeholder

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-049, SR-051).
- Symptom: none observed on a host. Guide said "resilving"; **Erase <disk> and replace** dropped the placeholder when rendered as HTML.
- Root cause: typo; raw <disk> treated as an HTML tag.
- Fix: resilvering; **Erase _disk_ and replace**.
- Regression: wording in docs/guide/storage-repositories.md; `python3 scripts/check_docs.py --worktree`.
- Avoid: angle-bracket placeholders in Markdown meant for HTML rendering.

## BUG-0505 — Guide said nothing was erased for post-wipe replace failures

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-052).
- Symptom: none observed on a host. Needs-attention recovery text covered only pre-wipe refusals, then said nothing was erased.
- Root cause: post-wipe messages (could not be cleared / could not be replaced with) were omitted.
- Fix: name those two outcomes and that the disk was wiped, the pool unchanged, and the same replacement finishes it.
- Regression: wording in docs/guide/storage-repositories.md; `python3 scripts/check_docs.py --worktree`.
- Avoid: a blanket "nothing was erased" after listing only the pre-wipe refusals.

## BUG-0510 — Master forwarded widened ZFS provision to members without replace support

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-030).
- Symptom: none observed on a host. In a mixed-version pool the master posted spares or a three-disk mirror / raidz3 to an older member; the member's strict peer decoder returned 400 and the operator saw only "selected ZFS host refused provisioning".
- Root cause: `dispatchPoolZFSHost` forwarded the selection verbatim with no check that the member implements `storage-zfs-replace-v1`.
- Fix: before posting a widened selection, probe the member's replace peer route; refuse with an upgrade sentence naming `storage-zfs-replace-v1` when the member answers as an older provision-only Core.
- Regression: `go test ./internal/agent/ -count=1 -run TestPoolZFSProvisionRefusesWidenedSelectionOnAnOldMember`.
- Avoid: treating Controller capability checks as the only gate once a command has reached the pool master.

## BUG-0511 — ZFS replace membership and peer binding refusals were untested

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-032).
- Symptom: none observed on a host. The API documents that replace requires a ready current member and that the peer route binds pool_id/host_id to the member itself; neither rule had a runnable check.
- Root cause: missing regression coverage for the master membership refusal and the member peer binding.
- Fix: assert pending/ejected members fail with "not a ready current member" without calling the local replacer, and assert a master-authenticated POST with a foreign pool_id or host_id returns 400 without running replace.
- Regression: `go test ./internal/agent/ -count=1 -run 'TestPoolZFSReplaceRefusesAStaleOrJoiningMember|TestPoolZFSReplacePeerRefusesAForeignPoolOrHost'`.
- Avoid: documenting authority rules for destructive peer routes without exercising them.

## BUG-0512 — ZFS replace peer call used the 15s timeout and HTTP request context

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-033).
- Symptom: none observed on a host. A slow `zpool replace` past 15 s made the master report the member unreachable, skip the repository-record update, and leave the member mid-wipe under a cancelled request context.
- Root cause: `dispatchPoolZFSReplace` used the default peer client timeout and request context; the member handler ran wipe/replace under `r.Context()`.
- Fix: give replace dispatch a 30-minute client budget detached from the command context, and run member-side work under a matching timeout detached from the HTTP request context.
- Regression: `go test ./internal/agent/ -count=1 -run TestPoolZFSReplaceDetachesFromCommandCancelAndUsesALongPeerTimeout`.
- Avoid: running destructive pool operations under the short generic peer RPC budget.

## BUG-0513 — poolZFSMember duplicated poolMemberByID

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-053).
- Symptom: none. `poolZFSMember` was a byte-identical copy of `poolMemberByID`.
- Root cause: a new helper was added beside the existing package lookup.
- Fix: delete `poolZFSMember` and call `poolMemberByID`.
- Regression: covered by existing replace membership tests that exercise the shared lookup.
- Avoid: copying a one-line package helper instead of reusing it.

## BUG-0514 — Mirror rules worded differently across installer frontends

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-067).
- Symptom: none observed on hardware. GTK summary, Continue refusal and text/nex-config each phrased the 10% rule differently.
- Root cause: four independent string literals for the same capacity rule.
- Fix: one shared sentence including the smallest-member reason in GTK summary, Continue, text Disk retry and nex-config.
- Regression: `bash installer/check.sh`; `python3 tests/integration/installer-disk.py`; `python3 tests/integration/installer-text-wizard.py`.
- Avoid: inventing a shorter summary phrasing that drifts from nex-config.

## BUG-0515 — Mirror summary called raw disk capacity usable

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-068).
- Symptom: none observed on hardware. Summary and guide said "128.0 GiB usable" for a full 128 GiB disk.
- Root cause: summary formatted the smallest selected disk's whole capacity with the word usable.
- Fix: say "smallest disk N GiB" and match the guide example.
- Regression: `python3 tests/integration/installer-disk.py` (disk_summary_text harness).
- Avoid: promising filesystem usable size without subtracting boot partitions and md metadata.

## BUG-0516 — GTK listed disks under 16 GiB then refused them

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-069).
- Symptom: none observed on hardware. An 8 GiB disk appeared in the graphical tick list but not in the text list.
- Root cause: GTK `discover()` lacked the text frontend's MIN_SECTORS / 16 GiB filter.
- Fix: skip disks below 16 GiB during discover so both frontends offer the same set.
- Regression: `bash installer/check.sh`; `python3 tests/integration/installer-disk.py`.
- Avoid: filtering eligibility only in the Continue path.

## BUG-0517 — Text frontend called Disabled "skip"

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-070).
- Symptom: none observed on hardware. Text mode offered "skip" while GTK offered "Disabled" for the same stored state.
- Root cause: text wizard invented a second operator-facing word for mode=disabled.
- Fix: use Disabled in the text choices, templates and guide; still store disabled in the handoff.
- Regression: `python3 tests/integration/installer-text-wizard.py`.
- Avoid: renaming a mode only in one frontend.

## BUG-0518 — Text disk choices omitted the serial

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-071).
- Symptom: none observed on hardware. Guide and README claimed both lists showed serial; text choices did not.
- Root cause: text choice line built name/capacity/type/model/contents only.
- Fix: append the serial to each text choice and assert it in check.sh and the text wizard fixture.
- Regression: `bash installer/check.sh`; `python3 tests/integration/installer-text-wizard.py`.
- Avoid: documenting shared facts without asserting both frontends emit them.

## BUG-0519 — nex-sync-esp EXIT trap leaked mounted member ESPs

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-075).
- Symptom: none observed on hardware. A failed ESP copy left the member ESP mounted; apt hooks could accumulate mounts.
- Root cause: EXIT trap only rmdir'd the mktemp mountpoint.
- Fix: umount then rmdir on EXIT, matching mdCopyESP.
- Regression: `bash installer/check.sh` (trap assertion + heredoc syntax).
- Avoid: mounting in a set -e script without unmounting in the EXIT trap.

## BUG-0520 — Fresh UEFI lab hardcoded Arch-only OVMF paths

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-076).
- Symptom: `FileNotFoundError` on Debian/Ubuntu when running `--uefi` without Arch edk2 paths.
- Root cause: absolute `/usr/share/edk2/x64/...` paths with no probe or override.
- Fix: probe the same OVMF candidates as iso-smoke, add `--uefi-code`, raise a clear missing-firmware error.
- Regression: `python3 -m unittest tests.test_fresh_install_lab`.
- Avoid: hardcoding one distribution's firmware path in a shared lab harness.

## BUG-0521 — Text empty disk selection retry was untested

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-072).
- Symptom: none. Empty multiselect answer path had no scenario.
- Root cause: installer-text-wizard covered mismatch only.
- Fix: script an empty disk answer, assert "Select at least one disk", then a valid handoff.
- Regression: `python3 tests/integration/installer-text-wizard.py` (`empty_disk_selection_retried`).
- Avoid: adding retry branches without an empty-input scenario.

## BUG-0522 — Text handoff did not escape GKeyFile list separators

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-073).
- Symptom: none observed on hardware. A serial containing `;` could split disk_serial and fail validation with an unactionable message.
- Root cause: `ini()` escaped backslashes only.
- Fix: `ini_list()` also escapes `;` for disk_serial and disk_major_minor.
- Regression: `python3 tests/integration/installer-text-wizard.py` (`disk_serial_list_escape`).
- Avoid: writing GKeyFile string lists without escaping the list separator.

## BUG-0523 — Offline other-adapters template assumed a management adapter

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-074).
- Symptom: none observed on hardware. Offline installs still said "A normal install only needs the management adapter" joined into one paragraph with the detail.
- Root cause: fixed template body after `${detail}` with no `.` separator; management sentence not in the substituted detail.
- Fix: move that sentence into the online detail; offline detail is accurate; add a debconf `.` separator.
- Regression: `python3 tests/integration/installer-text-wizard.py` (navigation + offline detail asserts).
- Avoid: putting branch-specific advice in a shared template body.

## BUG-0524 — GTK Disk summary arithmetic was only string-matched

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-077).
- Symptom: none. Changing smallest vs largest in the summary format would not fail CI.
- Root cause: installer-disk.py grepped literals only.
- Fix: extract `disk_summary_text` and compile a harness covering empty, one, equal, 5%-apart, over-10% and three-disk cases.
- Regression: `python3 tests/integration/installer-disk.py`.
- Avoid: treating source greps as proof of branching arithmetic.

## BUG-0525 — 10% mirror rule lacked near-boundary coverage

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-078).
- Symptom: none. Accepting only identical disks would still pass every test.
- Root cause: fixtures used equal capacities or a 2:1 mismatch only.
- Fix: ~5% accept and just-over-10% refuse cases in installer-disk.py; 5% accept in the text wizard.
- Regression: `python3 tests/integration/installer-disk.py`; `python3 tests/integration/installer-text-wizard.py`.
- Avoid: proving a tolerance with only equality cases.

## BUG-0514 — Changelog omitted 0.4.3 RAID/replace/version-light features

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-092).
- Symptom: none on a host. 0.4.3 notes named a Version-light bug fix but not mirrored install, disk replace, hot spares, or degraded ZFS staying in service (BUG-0443).
- Root cause: operator-visible slice work landed without matching Minor Feature / Bug Fixes bullets.
- Fix: add those bullets under 0.4.3 in CHANGELOG.md.
- Regression: wording review; `python3 scripts/check_docs.py --worktree`.
- Avoid: shipping a slice's UI/guide without a matching changelog entry in the same version heading.

## BUG-0515 — API.md claimed older Controllers tolerate zfs_pool and store none of it

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-093, SR-101).
- Symptom: none after capability gating (BUG-0492). Reference still said an older Controller sees an unknown status string for the new fields, and that the Controller stores none of the pool observation.
- Root cause: docs described the pre-gate failure mode and omitted the disposable projection row.
- Fix: document withhold-until-`storage-zfs-replace-v1` acknowledgement and Core-refreshed projection wording in docs/API.md.
- Regression: wording review against `zfsPoolObservationForController` / storage protocol tests.
- Avoid: documenting graceful unknown-field behaviour when DisallowUnknownFields is still in force.

## BUG-0516 — ZFS/system-disk API reference listed phantom refusal and wrong master 404

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-094, SR-102–105).
- Symptom: none on a host. docs/API.md and OpenAPI quoted `the replacement disk is not the one that was selected` (never emitted), omitted `cannot be safely observed` / surviving-member refusals / `system disk replacement requires a host`, claimed unknown pool master is 404 (code answers 409), used x-introduced-version 0.1.0, and lacked a machine-readable capability on host-system-disk-replace.
- Root cause: reference text drifted from vmhost/controller refusals and OpenAPI metadata defaults.
- Fix: align docs/API.md and docs/api/openapi.json / peer-openapi.json; regenerate HTML.
- Regression: `python3 scripts/generate_api.py --check`; string review against `internal/vmhost/{zfs,md}_replace.go` and `internal/controller/zfs_replace.go`.
- Avoid: copying refusal lists without grepping the tree for the exact sentence.

## BUG-0517 — md_arrays and BIOS ESP docs contradicted remarsal and installer layout

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-095, SR-096).
- Symptom: none on a host after UI mdArraysView fix. API.md said an older Core omits md_arrays; OpenAPI requires the field and Controller remarsals `arrays: null`. System-disk docs justified BIOS grub-install by claiming no ESP, while the installer writes an ESP on every member.
- Root cause: docs described intended optional field / layout reasons instead of shipped remarsal and firmware branch.
- Fix: document remarsal/`arrays: null` upgrade path and `/sys/firmware/efi` branch in docs/API.md and OpenAPI replaceSystemDisk description.
- Regression: wording review against `web/src/stats.tsx` mdArraysView and `mdInstallBootloader`.
- Avoid: documenting “omit the field” when the Go type has no omitempty.

## BUG-0518 — Install/usage/plan/QA wording for mirror layout and lab evidence

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-098–100, SR-106–110).
- Symptom: none on a host. Guide asserted unbooted mirror behaviour; platform plan said slices 2–3 had unit evidence only; software-RAID plan kept the superseded two-partition / grub-multi-install design; usage-history omitted System disk mirror; QA claimed BIOS ESP mkfs.vfat the shipped path cannot do. (NIC order and Erase disk(s) label were already correct.)
- Root cause: design drafts and early lab notes outlived the implemented recipe and QA status.
- Fix: soften install expected-result and link mirror QA; reword platform plan evidence line; update Slice 3 Layout/Boot; usage-history step 8; label QA BIOS observation.
- Regression: `python3 scripts/check_docs.py --worktree`; cross-read of plan status vs QA records.
- Avoid: stating boot-from-either-disk as fact while the QA record lists boot as outstanding.

## BUG-0519 — ESP nofail guide pages contradicted each other

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-006).
- Symptom: none on a host. install-nex-core said the ESP mount never holds up boot; replace-a-failed-system-disk told operators to add nofail by hand with no scope.
- Root cause: troubleshooting copied the pre-fixail emergency case without naming which installs already mount with nofail.
- Fix: scope troubleshooting to pre-this-build and hand-built mirrors; say this build mounts /boot/efi with nofail (unverified on a booted host); cross-link from install-nex-core.
- Regression: wording in the two guide pages; `python3 scripts/check_docs.py --worktree`.
- Avoid: absolute "never holds up boot" claims that only apply to one installer recipe.

## BUG-0520 — Guide said an ONLINE disk appears in the failed-disk list

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-050).
- Symptom: none on a host. Step 3 said a disk listed as online cannot be replaced, but the picker excludes ONLINE leaves.
- Root cause: guide described a UI state the filter cannot produce.
- Fix: say ONLINE disks are not offered; a missing disk means the pool does not consider it failed.
- Regression: wording in docs/guide/storage-repositories.md; `python3 scripts/check_docs.py --worktree`.
- Avoid: writing recovery steps for list states the UI filters out.

## BUG-0521 — ZFS replace host capability and membership checks were untested

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-054).
- Symptom: none observed on a host. Relay tests covered master-as-host old-Core only, not a joined member lacking replace or a foreign/revoked host_id.
- Root cause: missing cases for the per-host capability loop and managedHost refusal.
- Fix: TestZFSReplaceChecksSelectedHostCapabilityAndMembership joins a member without replace (409 naming the capability), a foreign-pool host, and a revoked member (409 freshness/membership).
- Regression: `go test ./internal/controller/ -count=1 -run TestZFSReplaceChecksSelectedHostCapabilityAndMembership`.
- Avoid: treating master-only old-Core coverage as proof both hosts in the capability loop are checked.

## BUG-0522 — Spare-apart-from-layout protocol test accepted a padded mirror

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-055).
- Symptom: none observed on a host. A two-disk mirror plus spare also passes if spares were counted toward the layout.
- Root cause: the named test never asserted the asymmetric short-data cases.
- Fix: refuse mirror with 1 data + 1 spare and raidz1 with 2 data + 1 spare.
- Regression: `go test ./internal/protocol/ -count=1 -run TestValidateZFSProvisionCountsSparesApartFromTheLayout`.
- Avoid: green-path-only tests for rules that fail only on asymmetric inputs.

## BUG-0523 — Unreachable archive made GET /api/v1/updates run apt-get update twice

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-081).
- Symptom: none observed on a host. An offline Controller selected a host; apt-get update timed out, then the nex-core-local check timed out again.
- Root cause: CheckWithCache wrote the refresh stamp only on success.
- Fix: stamp the attempt on failure too so the second package check skips apt-get update inside indexMaxAge.
- Regression: `go test ./internal/nexupdate/ -count=1 -run TestCheckWithCacheFailedRefreshStampsAttempt`.
- Avoid: pairing two CheckWithCache calls without a shared failed-refresh gate.

## BUG-0524 — Version light state needed a mouse

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-083).
- Symptom: none observed on a host. Host Version was an 8px colour-only dot with title/aria-label only.
- Root cause: state lived only in colour and hover text.
- Fix: show "· up to date / update available / major release behind" beside the version; keep the dot decorative (aria-hidden).
- Regression: `node web/src/updates.test.mjs`.
- Avoid: colour-only status on non-focusable empty spans.

## BUG-0525 — Stats design plan still documented deleted accent tokens

- Date: 2026-09-22. Status: fixed in the working tree; found by staged review (SR-087).
- Symptom: none on a host. Plan token table still listed --stats-bg, --accent-* and gauge arcs after plain-stats removed them.
- Root cause: plan not updated with the restyle change record.
- Fix: rewrite the Visual style token table and Gauges paragraph to match web/src/stats.css; note the restyle under settled differences.
- Regression: cross-read plan vs stats.css; `python3 scripts/check_docs.py --worktree`.
- Avoid: leaving design-plan tokens that the CSS no longer defines.

## BUG-0526 — host-system-disk-replace-v1 advertised before storage handshake

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-111).
- Symptom: none observed on a host after UI three-cap gate (BUG-0479). Agent still advertised SystemDiskReplaceCapability before StorageProtocolReady.
- Root cause: capability sat on the unconditional v2 list while discover needs the storage handshake.
- Fix: advertise it from the StorageProtocolReady block with discovery and ZFS provision/replace.
- Regression: `go test ./internal/agent/ -count=1 -run TestSystemDiskReplaceCapabilityFollowsStorageHandshake`.
- Avoid: advertising a capability whose UI path depends on another gated capability set.

## BUG-0527 — ZFS Blank state colour flipped when Spare column appeared

- Date: 2026-09-22. Status: fixed in the working tree; regression added; found by staged review (SR-112).
- Symptom: none observed on a host. Blank was body colour without Spare and muted with Spare; dead .zfs-layout label rule remained.
- Root cause: td:last-child { color: inherit } overrode .muted when Blank was last.
- Fix: delete .zfs-layout label; scope inherit to last-child:not(.muted):not(.zfs-old).
- Regression: `node web/src/zfs.test.mjs`.
- Avoid: last-child colour overrides that cancel intentional muted/state classes.

## BUG-0528 — Installer Disk step clipped on small BMC consoles

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found from operator BMC screenshot.
- Symptom: On ~1024×768 IPMI/iDRAC consoles the Disk step showed nested scrollbars, a horizontally scrolling disk table, and a vertically clipped RAID summary line.
- Root cause: The page scrolled both axes while the bare GtkTreeView requested full column widths; the summary label did not wrap to the allocated width.
- Fix: Page scroll is vertical-only; the disk list sits in its own scrolled window with ellipsized fixed columns; summary labels pin wrap width on allocate; chrome densifies at ≤1024×768.
- Regression: `python3 tests/integration/installer-disk.py` (plus installer-qol/keyboard/hostname/link). Native 800×600/1024×768 wizard capture against a rebuilt ISO remains outstanding.
- Avoid: packing an unconstrained tree view into a dual-axis scrolled viewport on the installer.


## BUG-0529 — Installer Disk list columns could not be resized and sat at minimum widths

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found from operator screenshot.
- Symptom: On the GUI Disk step the resize cursor appeared on every column divider but dragging changed nothing, and Device/Capacity/Type/Contents were squeezed to their minimum widths while Model and Serial took the rest.
- Root cause: BUG-0528 gave Model and Serial GTK2 `expand`. An expand column's share of the slack is re-added on top of whatever width a drag sets, so it can never be dragged narrower, and the stale slack made widening any other column overflow instead of reflow; the remaining columns had only minimum widths.
- Fix: No expand columns. The list's `size-allocate` handler shares the width by weight (Device 13 %, Capacity 12 %, Type 8 %, Model 25 %, Serial 22 %, Contents 20 % of the width after the 40 px Use column) as fixed widths, so GTK2's plain fixed-column drag applies and the last column absorbs slack.
- Regression: `python3 tests/integration/installer-disk.py` now allocates the real disk list under Xvfb, asserts the default spread fills the list without sideways scroll, and drags Model, Serial and Device dividers with XTest, asserting each width changes. The installer workflows install `libgtk2.0-dev xvfb` for it. Native BMC-console capture against a rebuilt ISO remains outstanding.
- Avoid: `gtk_tree_view_column_set_expand` on a GTK2 column the operator is meant to resize.

## BUG-0530 — Installer disk serial blank for SATA, SAS, SCSI and virtio disks

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; reported by the project owner from the GUI Disk step.
- Symptom: The Serial column, the text wizard's disk choices and erase review, and the recorded `disk_serial` were empty for every disk except NVMe, so the "Disk serial changed" guard compared blank with blank.
- Root cause: All three readers only read `device/serial`, which exists for NVMe controllers alone. virtio-blk reports `serial` on the block device, and SCSI-class disks (SATA, SAS, USB) only expose the unit-serial VPD page `device/vpd_pg80`, a 4-byte binary header followed by the serial. Xen virtual disks report none.
- Fix: One shared `nex_install_disk_serial` in the installer helper unit reads `device/serial`, then `serial`, then `device/vpd_pg80` past its header; the GTK wizard and nex-config call it and the text wizard's `disk_serial` mirrors the same order, so the recorded and re-read serials always agree.
- Regression: `python3 tests/integration/installer-disk.py` runs the helper against NVMe, virtio, VPD-only, empty-page and Xen sysfs trees; `python3 tests/integration/installer-text-wizard.py` lists and records a VPD-only mirror partner and a virtio-style disk. Real SATA/SAS hardware readout remains outstanding.
- Avoid: reading a single sysfs attribute for a fact that differs by bus; keep the readers on one shared helper.

## BUG-0531 — Add host failed against a DHCP-deployed appliance: certificate has no IP SANs

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; reported by the project owner while adding a second host.
- Symptom: **Add host** with the appliance's IP as the Controller address failed with `host could not enroll with the controller at https://<ip>:8443: ... x509: cannot validate certificate for <ip> because it doesn't contain any IP SANs`. The host that deployed the appliance had enrolled fine.
- Root cause: A bundled Controller deployed with DHCP mints its certificate before it has an address, so it names only its hostname (`instanceTLS` in `internal/localcore/deploy.go`). The deploying host is told that name to verify by (`ControllerServerName`), but `addSiteHost` pushed only `controller_url`, `ca_pem`, `token` and `replace_host`, and the manual `nex-core-agent enroll` had no way to give the name either, so every host added by IP verified the certificate against the IP.
- Fix: `POST /api/local/v1/enrollment` gains optional `server_name` and `nex-core-agent enroll` gains `--server-name`; `agent.Enroll` takes the name (the deployment handoff passes it instead of writing the store) and keeps it with the identity. `addSiteHost` parses its served certificate and sends its first DNS name only when the certificate does not name the Controller address, so older hosts still enroll whenever it does; a host-side 400 after sending it is answered with the certificate's name and the upgrade.
- Regression: `TestAddSiteHostSendsCertificateServerName` (hostname-only certificate, Controller address by IP; failed with the exact error above before the fix), `TestAddSiteHostOlderHostRejectsServerName`, `TestAddSiteHostSuccess` (no name sent when the certificate names the address) in `internal/controller`; `TestEnrollmentRecordsServerName` in `internal/localcore`. Run by `go test ./...` in every phase1 workflow and the github.com job. Adding a real host to a DHCP-deployed appliance was not re-run after the fix.
- Avoid: enrolling a host with fewer facts than the deploying host was given; the served certificate's names, not the address, decide what a host must verify by.

## BUG-0532 — Add host to pool read as the place to add a new host by address

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; reported by the project owner while adding a second host.
- Symptom: With one host enrolled, the operator opened the pool's **Add host to pool** to add a freshly installed host by IP. The dialog listed nothing, showed a **Peer address** field and a disabled confirm button, and the operator concluded the product was broken.
- Root cause: The dialog assumed the operator knew that enrollment is the site's **Add host** and that every enrolled host is already the master of its own pool. Its empty state named the toolbar button in prose only, and the address field and confirm button rendered before any host was chosen, so an empty dialog looked like a form for a new host's address.
- Fix: The dialog opens with one sentence stating the model; with nothing to list it shows **Enroll a new host…**, which closes the dialog and opens the site's Add host form; the peer address and confirm button render only after a host is selected; the enrollment success line says the join is done from the pool's Hosts tab. Label unchanged (decision: project owner, 2026-09-23).
- Regression: `node web/src/pool-members.test.mjs` (model sentence; no address field or confirm button before a selection; Enroll button only when the page provides the form) and `tests/integration/site_selector_browser.py` (empty dialog → Enroll a new host → Add host form; selection reveals the address and confirm), step "Verify searchable site selection" in both `phase1` workflows. Captures inspected under `.cache/site-selector-browser/join-dialog-*.png`.
- Avoid: rendering a dialog's inputs before the choice they depend on; an empty list needs an action, not a pointer to another button.

## BUG-0533 — ISO build fails: pinned ovmf-generic no longer in Debian sid

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found rebuilding the ISO for the Phase 3 lab.
- Symptom: `installer/build.sh` stopped in `stage_ovmf_generic` with `E: Version '2026.05-2' for 'ovmf-generic' was not found`; no ISO could be built from current source.
- Root cause: The exact `ovmf-generic`/`edk2` 2026.05-2 pin was fetched from the live `deb.debian.org` sid suite, which drops a version as soon as a newer one (2026.05-3, then 2026.08) replaces it. Every other build input already comes from immutable snapshots.
- Fix: The isolated sid APT root reads `snapshot.debian.org/archive/debian/20260920T000000Z/` (the same timestamp as the build's source snapshot, which carries 2026.05-2 for both binary and source), with `Check-Valid-Until=false` as the main snapshot APT already uses. The binary SHA-256 and `.dsc` checks are unchanged.
- Regression: `python3 -m unittest tests.test_storage_packaging` asserts the snapshot source and that no live sid source remains; it failed against the previous `build.sh` and passes with the fix. Run by the `tests/` suite in the phase1 workflows and the github.com job. A full ISO rebuild with the fix is recorded in the 2026-09-23 lab QA record.
- Avoid: pinning an exact version while fetching it from a moving suite; pin the archive, not just the version.

## BUG-0534 — Fresh host cannot import ISOs into its local storage

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found in the ISO-installed Phase 3 lab.
- Symptom: On a freshly installed 0.4.5 host, the first ISO upload to **Local storage** failed with `storage ancestors must be root-owned directories, executable by others for QEMU, without group or other write access`. `/var/lib/libvirt/images/nex-media` was mode 0700.
- Root cause: The bundled virtio-win seed (`internal/vmhost/bundled_media.go`) runs first on a new host and created `nex-media` with `os.MkdirAll(dir, 0755)`. The service runs with `UMask=0077`, so the directory became 0700; every later import to that storage then failed the traversal check, and QEMU could not reach the bundled ISO either. Present since 0.3.0.
- Fix: Create the directory with the existing `managedDirectory`, which sets 0711 through the descriptor regardless of umask, as every other Core-owned directory already does.
- Regression: `TestSeedBundledMediaCreatesTraversableDirectoryUnderServiceUmask` in `internal/vmhost` (sets umask 0077; failed with `-rwx------` before the fix). Run by `go test ./...` in the phase1 workflows and the github.com job. Hosts installed from 0.3.0–0.4.4 (before the fix shipped in the 0.4.5 tag) keep an existing 0700 directory; `chmod 0711 /var/lib/libvirt/images/nex-media` repairs one (applied in the lab).
- Avoid: `os.MkdirAll` with a literal mode for any directory QEMU must traverse under the service umask.

## BUG-0535 — A joined host's untagged network stays under its removed auto-pool

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found in the two-host placement lab.
- Symptom: After host B joined host A's pool, B's auto-registered `nex0 untagged` network was still returned by `GET /api/v1/networks` with the pool ID of B's deleted auto-pool, so the UI could not offer it for VMs placed on B in the joined pool. B's local storage moved correctly.
- Root cause: `adoptManaged` handled a held non-VLAN network by updating only the `pool_id`/`host_id` columns; the API serves the stored command JSON, which kept the old pool. Storage and VLAN networks go through their mirror projection, which rewrites both.
- Fix: Every held network goes through `mirrorNetworkProjection` (validation plus attention for an unusable row), deleting the column-only branch.
- Regression: `TestJoinedMemberNetworkProjectionFollowsJoinedPool` in `internal/controller`; failed with the stale pool ID before the fix. Run by `go test ./...` in the phase1 workflows and the github.com job.
- Avoid: updating projection columns without the stored command the API reads.

## BUG-0536 — An interrupted or refused VM create leaves a permanent phantom VM

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found by `tests/integration/placement_faults.py --case interrupt`.
- Symptom: `nex-core-local` on the target was killed while a create was still `validating`. The create stayed `needs_attention` for ever, the name stayed reserved, the Controller listed the VM as `unknown` (delete and power returned 404), the pool master kept a `defined` owner record, and ejecting the host was refused because it "still owns pool VM records".
- Root cause: Recovery treated every unfinished create as uncertain although create saves `allocating` before its first VM-specific effect, and the master only ever released an owner record from a delete tombstone, so no failed create could clear its admission record.
- Fix: A create at `validating` settles as `failed` (on exact retry in `reconcile`, and on its own through the periodic `promote` observation after a minute); `NeverCreatedVMs` lists a VM whose only create failed at `validating`, with no other create/update past validation, and the pool VM inventory adds it to `deleted`, which the master's existing prune consumes. Decision: project owner, 2026-09-23.
- Regression: `TestInterruptedCreateAtValidatingSettlesAsProvenFailure`, `TestInterruptedCreateAtValidatingSettlesWithoutRetry` (`internal/vmhost`), `TestProvenFailedCreateReleasesMasterRecord` (`internal/agent`); each failed before the fix. Lab: the two live phantoms cleared within a minute of deploying the fix.
- Avoid: reading "unfinished" as "uncertain" when the durable stage proves no effect; never infer absence without that proof.

## BUG-0537 — A lost master reply leaves an update reservation nothing can settle

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found by `placement_faults.py --case interrupt`.
- Symptom: The master was killed after saving a `pending_update`; the member saw `EOF`, the Controller task ended `failed`, and the master record kept `pending_update` indefinitely, blocking that VM's lifecycle.
- Root cause: The member refused the operation before executing it and journaled nothing, so neither the report-time abort nor inventory evidence could ever name the operation as failed.
- Fix: `RecordRefusal` journals a pre-execution refusal of a `vm-create`/`vm-update` as `failed` at `validating`; the pool VM inventory carries `aborted` (operation ID → `FailedVMUpdateEvidence`), and the master aborts a matching `pending_update` through `abortPoolVMUpdate`. Decision: project owner, 2026-09-23.
- Regression: `TestRefusedUpdateReleasesMasterReservation` (`internal/agent`; failed with the reservation retained when the master-side abort was removed) `TestRecordedRefusalIsReturnedOnRedelivery` (`internal/vmhost`), and `TestManagedWorkerJournalsRefusedUpdate` (`internal/agent`; the worker's own `RecordRefusal` call, added 2026-09-23 after review found the other tests call it directly).
- Avoid: reporting a definite failure to the Controller without a durable host record that the master can also read.

## BUG-0538 — A deleted VM keeps its master record while an update is unresolved

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found in the placement lab after BUG-0537.
- Symptom: A VM whose master record had a stuck `pending_update` was deleted successfully on its host, but the master kept the record, so a replacement Controller would re-adopt it and the host could not be ejected.
- Root cause: `prunePoolVMOwners` skipped any record with a pending update, even with the owner's delete tombstone.
- Fix: Drop that condition; a deleted VM ID can take no further update, and `deleteVM` already refuses while a change for the VM is unresolved on the host.
- Regression: `TestDeletedOwnerVMPrunesRecordWithUnresolvedUpdate` (failed before) and the updated `TestPrunePoolVMOwnersUsesDeleteTombstonesOnly` in `internal/agent`.
- Avoid: letting an unrelated guard block pruning on positive deletion evidence.

## BUG-0539 — A host with no managed-VM journal cannot publish VM inventory or journal a refusal

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found by `go test -race ./internal/agent` and independent review of the BUG-0536/BUG-0537 fixes.
- Symptom: Four existing agent tests failed with "managed VM journal is unavailable" (`TestReturningOldMasterRejoinsSafely`, `TestRestrictedHostKeepsReportingAndStopping`, `TestPublishPoolVMInventoryRefreshesStaleMemberAndAcceptsPartialSnapshot`, `TestSingleHostPoolMasterPublishesSharedVMInventory`). On a real host that has never run a managed operation, pool VM inventory publication failed, so the master refused the pool's inventory as incomplete; and a refused first create was not journaled, so its master reservation could never be released.
- Root cause: `NeverCreatedVMs` and `RecordRefusal` read the journal directory with `journals()`, which returns `ErrNotExist` when `managed-vms` does not exist yet; `Applied` and `Execute` already treat that directory as optional or create it.
- Fix: `NeverCreatedVMs` treats a missing directory as an empty journal, and `RecordRefusal` creates the directory before locking, as `execute` does (`internal/vmhost/managed.go`).
- Regression: the four agent tests above and `TestManagedWorkerJournalsRefusedUpdate` (`internal/agent`) failed before the fix and pass after it; `go test -race ./internal/agent ./internal/vmhost ./internal/controller` passes. CI runs them through the existing Go test jobs.
- Avoid: calling "the journal is unreadable" what is only "the journal is empty"; reuse the readers that already handle a fresh host.

## BUG-0540 — A Controller deployment interrupted while copying its disk can never finish

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found on a freshly installed lab host by SIGKILLing `nex-core-local` the moment the deploy journal reached `copying_disk` (the Phase 3 "interrupted bootstrap" gate).
- Symptom: After the automatic service restart the deployment showed `needs_attention` ("interrupted operation: ownership or VM observation uncertain; no effects replayed") with a 30 MiB partial `controller.qcow2` and no domain. **Retry deployment** returned 202 and changed nothing, and a new deployment was refused because the host already had one, so the host could never get its Controller without manual file removal.
- Root cause: `Engine.Deploy` sent every saved operation to `reconcile`, which only accepts an interrupted deploy whose domain it can prove it owns. A deploy killed at `copying_disk` or `creating_seed` has no domain yet, so ownership could never be established, although nothing from it could be running.
- Fix: `undefinedDeploy` recognises a deploy interrupted before `defining_vm` when a successful `virsh list` shows neither its UUID nor its name; `Deploy` then removes only that deploy's own disk directory and seed directory and runs again under the same UUID. Any observation failure, or a listed domain, still goes to `reconcile`, which never replays effects (`internal/vmhost/engine.go`).
- Regression: `TestRetryRestartsDeployInterruptedBeforeDefine` (`internal/vmhost`) failed before the fix (partial disk kept, operation left `needs_attention`) and passes after; `TestInterruptedRetryNeverRecreates` still passes. Lab: with the fixed `nex-core-local` swapped in on the same stuck host, the restarted service resumed the deployment to `ready` under the same UUID, with one domain, the Controller answering and the host enrolled as its pool master.
- Avoid: treating "not yet defined" as "ownership uncertain"; absence proven by a successful listing is evidence, an observation failure is not.

## BUG-0541 — A member never adopts a large shared ISO

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found on a two-host test pool.
- Symptom: The member reported `pool-media-reconciliation` `failed` with `pool-media-reconciliation deadline`, and the master listed a ~5 GiB Windows ISO for it as `awaiting member reconciliation` on every pass; a small ISO on the same storage was adopted.
- Root cause: First adoption hashed the whole ISO inside the converge pass's per-item deadline, which is the agent probe timeout (`--probe-timeout 3s`). A multi-GiB file on remote storage cannot be read in 3 s, so every pass timed out and nothing was journalled.
- Fix: `adoptSharedMedia` adopts when the file is a regular file at the recorded size (`sharedISOPresent`, also used for the already-adopted check); every attach still verifies the digest (`internal/vmhost/managed.go`).
- Regression: `TestAdoptSharedMediaDoesNotHashOnFirstAdoption` (`internal/vmhost`) failed before the fix and passes after; it replaces `TestAdoptSharedMediaHonoursACancelledContext`, which asserted the removed hash.
- Avoid: doing work proportional to file size inside a probe-timeout budget.

## BUG-0542 — A steady pool master reports its members' authority replica as stale

- Date: 2026-09-23. Status: fixed in the working tree; regression asserted; found on a two-host test pool.
- Symptom: The master's `pool-authority-replica` check went `unknown`/stale ("a pool member's authority replica acknowledgement is stale") about a minute after each push, while the member reported the current generation as verified.
- Root cause: Once a member had acknowledged a POST, later pushes skipped the POST after the status read but never refreshed the acknowledgement time that `PoolReplicaChecks` ages.
- Fix: When the POST is skipped, a status read that reports the current generation and digest as verified is recorded as the acknowledgement (`internal/agent/pool_authority.go`).
- Regression: `TestPoolReplicaSkippedPushKeepsTheMasterCheckFresh` (`internal/agent`) failed before the fix and passes after; `TestPoolReplicaPushShortCircuitsOnPostAckOnly` still passes.
- Avoid: an idempotency short-circuit that skips the bookkeeping a freshness check depends on.

## BUG-0543 — Management VLAN install left Networks empty

- Date: 2026-09-24. Status: fixed in the working tree; regression asserted.
- Symptom: A fresh pool whose installer management NIC used a VLAN tag showed **No networks reported yet** on the Controller Networks screen even after the master was enrolled, and Create VM offered no network to select.
- Root cause: `DefaultNetworks` only followed physical/bond bridge ports, so `br0` with port `nexN.<tag>` produced no default and nothing was journalled for Controller adoption. Default network registration also ran after local storage.
- Fix: `DefaultNetworks` treats an installer management VLAN slave (`nexN.<tag>` on `br0`) as the uplink via its parent NIC, while skipping Core guest VLAN bridges named `<uplink>v<tag>`; `RegisterDefaults` registers default networks before local storage (`internal/vmhost/managed.go`).
- Regression: `TestDefaultNetworksFollowManagementVLANOnBridge`, `TestRegisterDefaultsRegistersManagementVLANBridge`, and the existing `TestDefaultNetworksFollowBridgesOnPhysicalPorts` (`internal/vmhost`). Hand-revert of the VLAN branch fails the management-VLAN test.
- Avoid: assuming the management bridge always enslaves the raw NIC; the installer uses `nexN.<tag>` when a management VLAN is chosen.

## BUG-0544 — Host console showed all-caps text and raw ANSI junk

- Date: 2026-09-24. Status: fixed in the working tree; regression asserted.
- Symptom: Opening a host Console showed blocky all-caps text and literal colour codes such as `[32m` / `[0m` during apt and coloured prompts.
- Root cause: `hostConsoleFont` reused A–Z bitmaps for a–z, and `hostTerm.write` discarded only the ESC byte of CSI/OSC sequences so the rest printed as characters.
- Fix: Distinct lowercase glyphs in `internal/vmhost/hostconsole_font.go`; CSI/OSC/charset escape parsing in `hostTerm.write` (`internal/vmhost/hostconsole.go`).
- Regression: `TestHostConsoleFontLowercaseDistinct` and `TestHostTermStripsANSISequences` (`internal/vmhost`).
- Avoid: dropping only `0x1b` and assuming `TERM=dumb` means no CSI will arrive; never alias lowercase glyphs to uppercase.

## BUG-0545 — Host Networking form showed Not configured for live management addressing

- Date: 2026-09-24. Status: fixed in the working tree; regression asserted.
- Symptom: Opening a host's Networking tab Management network form showed Addressing *Not
  configured*, empty VLAN/address/gateway and empty DNS even when the host had a live
  installer or confirmed Nex management network.
- Root cause: The edit form seeded only from `management_network.spec`, which exists only when
  `<state-dir>/management-network.json` holds a change record. Installer-configured hosts had
  no record, so every interface defaulted to disabled. Observed NIC addresses alone cannot seed
  the form (address lives on `br0`; no DNS/VLAN/mode).
- Fix: Nex Core parses the Nex-managed `/etc` files into `ManagementNetworkStatus.current` and
  reports stage `none` with that Current when no change record exists; the UI prefills from
  `current` (falling back to `spec`).
- Regression: `TestManagementNetworkRender` round-trip and
  `TestReadManagementNetworkStatusReportsCurrentWithoutRecord` (`internal/vmhost`);
  `web/src/host-network.test.mjs` current-only prefill; HostNetworkContractTests stage enum /
  `current` property.
- Avoid: treating "no staged change" as "no configuration"; the durable files on the host are
  the configuration.

## BUG-0546 — Dell Redfish system Id System.Embedded.1 refused as fence target

- Date: 2026-09-24. Status: fixed in the working tree; regression asserted.
- Symptom: Setting BMC access with Redfish and Dell's ComputerSystem Id `System.Embedded.1`
  returned `invalid BMC fence profile`. IPMI over LAN on the same iDRAC often does not answer,
  so Redfish is the workable path and was blocked at validation.
- Root cause: `StorageClusterFence.Valid` required `target_id` to match `storageOpaqueID`
  (`[A-Za-z0-9_-]` only). DMTF DSP0266 §9.6.6 allows period in Id values; Dell and
  `fence_redfish` use `System.Embedded.1`.
- Fix: Validate BMC `target_id` with `bmcTargetID`, which allows `.` while still refusing
  RFC1738 unsafe / path-breaking characters (`internal/protocol/cluster.go`).
- Regression: `TestBMCTargetIDFollowsRedfishIdRules` (`internal/protocol`).
- Avoid: Reusing opaque storage/host ID character sets for Redfish resource Ids.

## BUG-0547 — BMC Detect left passwords in the Core credential store

- Date: 2026-09-24. Status: fixed in the working tree; regression asserted.
- Symptom: Every passworded Detect left a private JSON receipt containing the BMC password even
  after the probe answered. A handoff whose probe never ran kept both the receipt and the
  materialized password file indefinitely.
- Root cause: Detect reused the permanent `fence_bmc` credential purpose. Its cleanup removed
  only the materialized file, while the shared cluster-secret receipt retained the password and
  could recreate that file on replay. An undelivered probe had no Core-side expiry. Core also
  accepted an unrecorded permanent handoff by reference, and one damaged unrelated receipt
  stopped the expiry scan before it could reach later probes.
- Fix: Give Detect a distinct `bmc_probe` credential purpose. Nex Core removes both private
  copies after an answered probe, and sweeps unused probe receipts after ten minutes on its own
  tick, including after restart. Core checks the signed purpose, request and target before using
  the password, and sweeps past damaged receipts. Permanent fence and storage secrets are excluded.
- Regression: `TestABMCProbeAnswersFromTheMasterRecordsNothingAndRetiresItsCredential`,
  `TestABMCProbeRunsOnlyOnTheMaster`, and
  `TestUndeliveredBMCProbeCredentialExpiresWithoutTouchingRecordedFenceSecrets`,
  `TestABMCProbeRefusesAnUnrecordedPermanentCredential`,
  `TestABMCProbeCredentialIsBoundToItsRequestAndTarget`, and
  `TestExpiredBMCProbeCredentialSurvivesUnrelatedCorruptReceipt`
  (`go test -count=1 ./internal/agent -run 'TestABMCProbe|TestUndeliveredBMCProbeCredential|TestExpiredBMCProbe'`).
  `TestBMCProbeIsRelayedToTheMasterAndStoresNothing` checks the probe-only handoff purpose
  (`go test -count=1 ./internal/controller -run TestBMCProbe`). Both packages run in the forge's
  `.github/workflows/phase1.yml` Go suite and its `.gitea/workflows/phase1.yml` copy.
- Avoid: Treating deletion of a materialized credential as deletion of every persisted copy,
  using a permanent credential scope for a one-shot operation, or trusting a script path without
  validating the handoff's signed purpose and target.

## BUG-0548 — Installer regression harness failed to compile after log-tail signature change

- Date: 2026-09-24. Status: fixed in the working tree; forge rerun outstanding.
- Symptom: Forge run 4412, installer job 5582 stopped in `installer-shell.py` before the later
  installer regressions ran; its generated C called `nex_log_tail` with one argument.
- Root cause: The production log reader gained a required byte limit, but the source-extracting
  regression harness still used the old call shape.
- Fix: Pass the existing 128 KiB log limit in each harness call, preserving the test's bounded-tail
  and UTF-8 assertions. No installed behavior changes.
- Regression: `python3 tests/integration/installer-shell.py` reproduced the compiler error before
  the fix and passed afterward. The same script runs in the installer job on forge pushes and pull
  requests through `.github/workflows/installer.yml`; the `.gitea` copy is unchanged and in sync.
- Avoid: Changing a C helper signature without running source-extracting tests that compile it.

## BUG-0549 — Text installer confirmation regression expected obsolete layout wording

- Date: 2026-09-24. Status: fixed in the working tree; forge rerun outstanding.
- Symptom: After the compile failure in forge run 4412 is corrected, the installer job's
  `erase_summary` scenario fails locally at its layout assertion.
- Root cause: The text wizard now describes a single installation target as “one system disk,
  no redundancy,” but the regression still expected the older ext4-specific sentence.
- Fix: Match the actual confirmation wording without changing disk selection or erase behavior.
- Regression: `python3 tests/integration/installer-text-wizard.py` failed at `erase_summary` before
  correction and passed all `sh` scenarios afterward. The forge installer job runs this script
  on pushes and pull requests through `.github/workflows/installer.yml`.
- Avoid: Preserving a stale wording assertion after changing the operator-facing confirmation.

## BUG-0550 — GitHub checks failed on delayed host console shell startup

- Date: 2026-09-24. Status: local fix; GitHub rerun outstanding.
- Symptom: Four GitHub `GitHub checks` runs failed in `TestHostConsoleIncrementalUpdateOnlyOnChange` because an incremental request received a frame during shell startup. `Controller foundation` also showed cancelled runs beside deliberately skipped jobs.
- Root cause: The test assumed all shell startup output arrived within 200 ms; later real output legitimately changed the framebuffer. GitHub `Controller foundation` and `Release` also shared a cancelling concurrency group even though their jobs skip on github.com.
- Fix: The regression now compares any startup frame with the previous framebuffer and waits for a one-second idle interval, with a separate deadline for reading a full frame. The two GitHub workflow copies use separate concurrency groups on github.com while retaining the shared forge group.
- Regression: `go test ./internal/vmhost -run '^TestHostConsoleIncrementalUpdateOnlyOnChange$' -count=5` and `go test ./...` passed locally. The test runs in `.github/workflows/github.yml` on GitHub and in `.github/workflows/phase1.yml` on the forge. GitHub rerun requires a project-owner push.
- Avoid: Using a fixed short sleep to infer that a login shell is idle, or sharing a cancelling workflow group between jobs that should only skip.

## BUG-0551 — Native network installation could miss newly installed NIC drivers

- Date: 2026-09-24. Status: fixed in the working tree; clean early-hook network discovery verified, full installation acceptance separate.
- Symptom: A disposable serial net installation reported no network interfaces even though
  the matching VirtIO network module had been installed from the media.
- Root cause: udev retained its boot-time module index after anna installed additional
  driver packages. Debian's hardware detection regenerated indexes and replayed device
  events without reloading udev; the new driver's alias was not discovered.
- Fix: The shared installer account/media hook regenerates module indexes with `depmod`,
  reloads udev, then invokes native `update-dev`, after component loading. Both native serial installation and the Nex
  graphical/text preparation use this hook; no adapter-specific module list is added.
- Regression: In a fresh disposable QEMU failure, only loopback was present; running
  `udevadm control --reload` followed by `update-dev` discovered the VirtIO interface
  without manually loading its driver. The focused module-loading test in
  `python3 -m unittest discover -s tests -p test_netinst.py` fails against the original hook and passes with the fix for
  both initial and repeated preparation. All seven netinst tests passed in the Debian
  builder container, including real APT selection (`docker run --rm --entrypoint python3
  -v "$PWD:/work:ro" nex-iso-builder -m unittest discover -s tests -p test_netinst.py`).
  The native account integration test
  against `user-setup-udeb` 1.107 and `installer/check.sh` passed. The existing forge
  Python suite runs the new regression on pushes and pull requests; clean unattended
  installation of the corrected artifact remains a separate acceptance check. A fresh
  serial QEMU boot with the exact corrected hook overlaid reached DHCP, HTTPS source
  preflight and automatic partition/base installation without manual driver loading.
- Avoid: Adding an adapter-specific `modprobe` workaround, or assuming that installing
  module files and replaying udev events also refreshes udev's cached indexes.

## BUG-0552 — CI concurrency checks rejected the GitHub runner split

- Date: 2026-09-24. Status: fixed in the working tree; forge execution pending.
- Symptom: The Python suite failed two CI security checks despite the workflows retaining
  the shared forge runner group.
- Root cause: BUG-0550 made the GitHub copies select separate groups on github.com and
  the shared group on the forge, but the checks still required an unconditional literal.
- Fix: Both checks now require the complete GitHub conditional or the Gitea literal,
  according to the workflow copy. Production concurrency configuration is unchanged.
- Regression: `python3 -m unittest discover -s tests -p 'test_ci_security.py' -v`
  failed the two concurrency assertions before correction and passes all 13 checks
  afterward. Temporary copies with an incorrect shared group were rejected for both
  workflows and both providers (four expected failures). The existing Python discovery
  suite runs these checks in `.github/workflows/phase1.yml` and `github.yml` on pushes
  and pull requests; their forge/GitHub gates remain unchanged.
- Avoid: Weakening the assertion to merely search for a shared-group name somewhere in
  the file, or changing production scheduling to satisfy a stale test.

## BUG-0553 — Network installer lost APT's package download plan

- Date: 2026-09-24. Status: fixed; final netinst passed clean unattended BIOS installation, first boot and reboot.
- Symptom: A disposable native net installation created an empty download plan and fetched
  only the driver ISO. APT's required-package URI lines appeared in syslog instead of the
  redirected plan file, so the later local-only install could not find its packages.
- Root cause: Debian Installer's `in-target` wrapper routes command stdout to its logger
  by default. The new plan capture treated it as a plain command wrapper.
- Fix: Request native `in-target --pass-stdout` for the APT URI-plan command. Preserve
  native target setup and logging for the actual package installation. An empty plan
  remains valid when a retry finds all requested packages already installed.
- Regression: `python3 -m unittest discover -s tests -p test_netinst.py` passed all eight
  tests in the Debian builder. The new check executes the actual plan-capture shell block
  with the native wrapper's stdout behavior: the corrected call selects the required
  package, while removing `--pass-stdout` reproduces an empty plan. The real APT fixture
  separately verifies package selection, URI filename encoding and already-installed
  package exclusion. `installer/check.sh` passed. The forge Python suite runs these tests
  on pushes and pull requests; the complete corrected VM installation is tracked separately.
- Avoid: Replacing `in-target` with raw `chroot`, inventing an empty-plan failure for valid
  retries, or treating an APT plan visible in syslog as evidence that redirected output exists.

## BUG-0554 — VM creation leaves the result page blank until refresh

- Date: 2026-09-24. Status: fixed in the working tree.
- Symptom: accepting a VM creation navigates to its result route but leaves the page empty.
- Cause: the wizard cleared its local form while the shell kept `creating=true`, hiding the VM list.
- Fix: call the existing shell close callback on accepted creation before navigating.
- Regression: the accepted-create fixture in `tests/integration/phase1_browser.py` now checks
  visible task and VM details without refresh. The isolated browser block failed before the fix
  and passed afterward; UI tests, typecheck and build passed. Both forge Phase 1 workflow copies
  already execute this browser suite on pushes and pull requests; hosted CI was not run here.
  See [browser evidence and limits](qa/2026-09-24-vm-create-navigation.md).
- Avoid: forcing a full-page reload or clearing wizard drafts during Storage/Networks side trips.

## BUG-0555 — ISO smoke guest test failed with multiple installed kernels

- Date: 2026-09-24. Status: fixed in the working tree.
- Symptom: Network-installed Core passed initial boot checks but its nested-guest smoke
  test failed when copying the kernel.
- Root cause: The fixture expanded `/boot/vmlinuz-*` into multiple source files while
  its destination was a single file. Network installation can retain the base kernel
  alongside the newer pinned kernel.
- Fix: Copy the currently running kernel using `uname -r`.
- Regression: `python3 -m unittest discover -s tests -p test_iso_smoke.py` passes its
  shell execution with two kernel files; restoring the wildcard reproduces the failure.
  Existing forge Python discovery runs it on pushes and pull requests. Final native
  nested-guest checks are recorded in the network-installer QA report.
- Avoid: Deleting valid installed kernels to accommodate a test fixture's assumption.

## BUG-0556 — Disk validator fixture rejected the netinst media probe

- Date: 2026-09-24. Status: fixed locally; hosted rerun pending.
- Symptom: The installer workflow aborted on its first valid disk scenario with a
  filesystem-stub assertion after the netinst change.
- Root cause: The actual validator now checks the netinst media marker before disk
  validation, but the fixture only recognized partition-file existence checks.
- Fix: Model the exact netinst marker path and retain strict partition-path assertions.
  Add a netinst scenario proving that missing management networking is rejected.
- Regression: `python3 tests/integration/installer-disk.py` reproduced the original
  assertion in the Debian builder, then passed with the fix, including simulated disk
  safety scenarios and real GTK column dragging under Xvfb. The container needed
  `xvfb`, `xauth` and `xdotool` in addition to its existing build dependencies.
  Both installer workflow copies already run this script on pushes and pull requests;
  no workflow change is needed. Independent review found no outstanding issues.
- Avoid: Accepting arbitrary filesystem probes in the stub or removing production
  network validation to accommodate a stale test.

## BUG-0557 — Phase 1 browser gate waited for removed UI elements

- Date: 2026-09-24. Status: fixed locally; forge rerun pending.
- Symptom: PR #8 run 4437 failed in `controller-browser` while waiting for `a.activity`;
  the `phase1` gate then failed and dependent jobs were skipped.
- Root cause: the browser suite still targeted the former activity link and warning
  class after those controls became an activity disclosure and status banner.
- Fix: open the activity disclosure and its Tasks link, and assert the host notices
  through their status role. Keep the queued-task count assertion on the disclosure.
- Regression: the forge log establishes the original timeout at the removed link.
  A local Phase 1 browser run reached the next stale warning assertion after the
  first correction. After both corrections, the full
  `.cache/vm-create-venv/bin/python tests/integration/phase1.py --browser` run passed
  all 22 reported checks, including the accepted VM creation route. Both forge
  Phase 1 workflows already run this suite on pull requests. No forge rerun is claimed.
- Avoid: reviewing a changed browser fixture as passing based on focused unit tests
  while its required end-to-end gate is red.
