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
