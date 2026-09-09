# Nex bug ledger

Append resolved defects with their evidence and the approach to avoid.

## BUG-0001 — Incomplete isolated ISO build dependencies

- Date: 2026-09-08
- Symptom: the first build exited 127 at `gpgv`; review also found that the target's HTTPS APT configuration needed an explicit CA-certificate dependency.
- Root cause: relying on recommended/transitive tools while installing with `--no-install-recommends`.
- Fix: explicitly include `gpgv` in `installer/Dockerfile` and `ca-certificates` in `installer/packages.txt`.
- Regression evidence: `./installer/build.sh` completed Debian signature verification and the final ISO build; the embedded manifest contains `ca-certificates`. The offline install smoke additionally exercises the resulting dependency closure.
- Avoid: assuming a developer machine or build container's installed tools will also exist in a minimal installer target.

## BUG-0002 — Hybrid boot objects lost during ISO remaster

- Date: 2026-09-08
- Symptom: xorriso rejected the initial image with `Cannot refer by isohybrid MBR to data outside of ISO 9660 filesystem`.
- Root cause: replacing the imported ISO's entire root with `-map` invalidated boot-object references; boot replay can also modify the boot-information bytes in `isolinux.bin` after media hashes are prepared.
- Fix: replay boot settings before `-update_r` to preserve imported objects, and exclude the regenerated boot catalog and patched `isolinux.bin` from media MD5 lists (`installer/build.sh`).
- Regression evidence: final build succeeded; independent artifact review verified BIOS/UEFI hybrid metadata and every listed media checksum. `tests/integration/iso-smoke.py` boots the actual ISO in both firmware modes.
- Avoid: rebuilding a hybrid ISO's boot layout from copied filenames alone, or hashing bytes that the ISO writer will subsequently patch.

## BUG-0003 — Package manifest used the wrong dpkg-deb operation

- Date: 2026-09-08
- Symptom: the draft manifest command treated its formatting expression as a field name rather than producing TSV rows.
- Root cause: `dpkg-deb -f` reads named control fields; it is not the formatted listing operation.
- Fix: use `dpkg-deb --show --showformat` in `installer/build.sh`.
- Regression evidence: independent final-image review checked a populated TSV manifest with package versions/architectures and the explicit CA-certificate package.
- Avoid: treating an output file's existence as proof its contents are valid inventory.

Build evidence above refers to Nex Core 0.1.0-alpha1 ISO SHA256
`b2862b1e31f8c784b54efd67a3496dcf66bc8252ce910109d59ea043853eb734`.
Runtime results are recorded separately with the ISO QA report; this ledger does
not claim physical-hardware or Secure Boot verification.

## BUG-0004 — Installer debconf environment leaked into target package setup

- Date: 2026-09-08
- Symptom: the first offline installation failed during CA-certificate configuration with bad debconf descriptors and later hung in PAM configuration.
- Root cause: invoking target package setup through raw `chroot` inherited Debian Installer frontend state without the target environment setup its packages need.
- Fix: use Debian Installer's `in-target /bin/sh` for the shared target configuration script (`installer/late-install.sh`).
- Regression evidence: run 1 reproduced the failure; `installer/check.sh` guards use of `in-target`. Fresh unattended run 3 passed offline installation, installed services, nested KVM and both installed boots against SHA256 `b02044bcac4205af247d52a30ff158ad4d73e7580382f666b37baf394538015f`; see [QA evidence](qa/iso/2026-09-08-alpha1.md).
- Avoid: substituting a raw chroot for the installer's environment-aware target helper, or treating successful ISO creation as proof of successful installation.

## BUG-0005 — Root-account hook ran before installer component initialization

- Date: 2026-09-08
- Symptom: the root-only installer draft failed its early command because `/usr/bin/user-setup-ask` was missing.
- Root cause: before component loading, `anna-install` queues packages instead of installing them. Setting its retriever alone still leaves the package index missing.
- Fix: initialize through the native `anna cdrom-retriever default` command before installing the account component when its dialog is absent.
- Regression evidence: disposable VM runs reproduced both failures; `tests/integration/root-account.py` checks initialization order as well as root-only password handling. See the [root-only installer QA report](qa/iso/2026-09-08-root-account.md) for the exact rebuilt artifact and runtime results.
- Avoid: mocking package installation as an unconditional success, or assuming a preseed early command runs after installer components are loaded.

## BUG-0006 — Root-only smoke check counted a Debian service account

- Date: 2026-09-08
- Symptom: offline installation succeeded, but the new ordinary-user assertion failed on first boot.
- Root cause: its UID range included Debian's `libvirt-qemu` service account (64055).
- Fix: check Debian's ordinary-user range (1000–60000), preserving its higher reserved service UIDs.
- Regression evidence: inspection showed root password status `P` and only `libvirt-qemu` in the old range. The corrected installed-host check runs on the same disposable installation; results are recorded in the [root-only QA report](qa/iso/2026-09-08-root-account.md).
- Avoid: interpreting every UID above 1000 as an installer-created login account; Debian service accounts remain necessary on a root-only host.

## BUG-0007 — Installer header used placeholder branding

- Date: 2026-09-08
- Symptom: custom installer showed a generic blue letter tile and system-font product name.
- Root cause: the header recreated branding as text instead of using the approved artwork.
- Fix: crop and scale the approved Core lockup with existing GTK image support; retain original artwork in the frontend source archive.
- Regression evidence: [actual 800×600 VM capture](qa/iso/2026-09-08-installer-logo.md) shows the complete approved logo; the initrd regression compares exact artwork bytes.
- Avoid: substituting typed text for an existing approved product lockup.

## BUG-0008 — Initrd overlay omitted directories needed by new assets

- Date: 2026-09-08
- Symptom: first logo rebuild booted into the plain-text fallback despite the PNG appearing in the cpio file list.
- Root cause: the overlay archived files only; the kernel could not extract artwork into a missing parent directory. The previous frontend library reused an existing directory.
- Fix: include directory entries in the shared overlay archive before their files.
- Regression evidence: `tests/integration/installer-logo.py` passes against the complete concatenated initrd and verifies parent-before-file ordering and artwork contents; [final VM capture](qa/iso/2026-09-08-installer-logo.md) confirms successful rendering.
- Avoid: treating an archive member listing as proof the kernel can extract its files.

## BUG-P1-0001 — Controller startup and credential lifecycle regressions

- Date: 2026-09-08
- Symptom: the initial Phase 1 Controller panicked before listening; review also found that in-flight old credentials or a second replacement token could bypass a completed host revocation/re-enrollment.
- Root cause: ambiguous Go ServeMux patterns and certificate checks outside the state-changing transaction.
- Fix: use non-conflicting routes; revalidate the exact certificate inside each transaction and consume replacement tokens only for a still-revoked host.
- Regression evidence: `go test -race -count=1 ./...` passed; `python3 tests/integration/phase1.py --browser` exercises actual startup and credential lifecycle. Dedicated stale-auth and replacement-token checks detect the race. See [independent review](qa/phase1/review.md) and [process/browser results](qa/phase1/acceptance.md).
- CI: `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` run the Go checks and process/browser suite on pushes and pull requests; hosted execution is not claimed.
- Avoid: treating successful compilation or pre-transaction authorization as proof a later operation remains authorized.

## BUG-P1-0002 — Native inventory rejection and misleading probe freshness

- Date: 2026-09-08
- Symptom: real hosts reported HTTP 400 because their CPU flag list exceeded an assumed limit; stalled probes could retain a healthy appearance, suppress independent observations or discard samples after a clock correction.
- Root cause: an unrealistically small CPU-feature bound and aggregate/wall-clock-only observation merging. The agent also labeled unrelated CPU flags as virtualization features.
- Fix: validate realistic bounded reports, publish virtualization-specific flags, merge probes independently using ordered reports and sample identity, and preserve timeout/stale state without renewing old successful observations.
- Regression evidence: controller merge/freshness and agent virtualization-filter tests pass under `go test -race -count=1 ./...`; the filter regression failed before correction. Process/browser and `bash tests/integration/phase1-debian.sh` checks exercise real reporting and stalled/failed libvirt recovery. See [Debian evidence](qa/phase1/debian.md) and [process/browser results](qa/phase1/acceptance.md) for exact tested builds and limits.
- CI: both Phase 1 workflows run these Go, browser/process and Debian checks on pushes and pull requests; actual hosted results remain unverified.
- Avoid: treating a heartbeat as a fresh successful service check, ordering hosts by their wall clocks, or assuming development-machine CPU metadata bounds.

## BUG-P1-0003 — UI health and asynchronous scope races

- Date: 2026-09-08
- Symptom: healthy checks initially rendered degraded; an older read could overwrite revoked state, a delayed enrollment token could appear under another pool, and the skip link changed the resource route.
- Root cause: a mismatched status enum and asynchronous responses unguarded by request/session/scope identity; the navigation hash was reused for focus movement.
- Fix: render contract status/freshness, reject superseded reads, guard workflow changes while mutations are pending, and move focus without changing the object URL.
- Regression evidence: `python3 tests/integration/phase1.py --browser` passes keyboard, health, delayed-token, delayed-read/revocation and narrow-screen checks with inspected captures in [browser evidence](qa/phase1/acceptance.md).
- CI: both Phase 1 workflows run the browser regression suite on pushes and pull requests; workflow configuration alone is not hosted CI proof.
- Avoid: trusting promise completion order as user intent, or using an object-navigation fragment for a skip link.

## BUG-0009 — Initrd overlay replaced the merged-usr library symlink

- Date: 2026-09-08
- Symptom: The first standalone-wizard image panicked during init.
- Root cause: Archiving a new /lib directory replaced Debian’s /lib → usr/lib symlink, hiding required startup files.
- Fix: Install new hooks under usr/lib while preserving the base symlink.
- Regression evidence: installer-logo.py failed on the broken complete initrd with “Overlay directory replaces base symlink: lib”; the corrected artifact passes and boots the native wizard. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Creating overlay directories without checking whether the base path is a symlink.

## BUG-0010 — Preseed command failure did not stop automatic partitioning

- Date: 2026-09-08
- Symptom: Review found that acknowledging a failed disk recheck could allow partitioning to continue.
- Root cause: The upstream preseed_command helper reports failure but returns success after its error dialog.
- Fix: Use a direct partman init hook whose nonzero exit aborts initialization.
- Regression evidence: installer-handoff.py simulates a failed recheck and observes nonzero hook exit; minimal mode remains unaffected. The final disposable install exercised the successful hook. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Using a diagnostic preseed early command as a fail-closed destructive-operation guard.

## BUG-0011 — Private staging permissions leaked into public host configuration

- Date: 2026-09-08
- Symptom: Review found that umask 0077 could stage /etc directories as 0700 and resolver files as 0600.
- Root cause: Requested mkdir/file modes are reduced by umask; copying staged directory attributes would propagate them to the installed system.
- Fix: Set public target directories to 0755 and files to 0644 explicitly while retaining private staging root and credential files.
- Regression evidence: installer-network.c runs under umask 0077 and checks public directory/file permissions plus private staging-root preservation; strict compilation and execution pass. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Assuming a requested mode is the resulting mode under a restrictive process umask.

## BUG-0012 — Selected keyboard did not match credential entry

- Date: 2026-09-08
- Symptom: Review found that non-US physical keys could produce a different password after reboot; restarting setup also retained an old live layout.
- Root cause: The original wizard only saved a later keyboard preseed while Xorg continued using its previous layout.
- Fix: Apply the displayed layout at startup and on selection, clear both credential entries on changes, and reject continuation when layout application fails.
- Regression evidence: installer-keyboard.py compiles the actual callback with simulated boundaries and covers success, failure, clearing and initialization. Actual VM selection of German made physical Y produce Z and cleared both fields. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Collecting a password before applying the input layout, or trusting a dropdown’s default to reset Xorg.

## BUG-0013 — Wizard startup changed the installation engine umask

- Date: 2026-09-08
- Symptom: Review found that native installation would inherit umask 0077.
- Root cause: The private-handoff script is sourced into the installer shell and did not restore its caller’s mask.
- Fix: Save and restore the incoming mask around the private handoff.
- Regression evidence: installer-handoff.py checks the caller remains at 0022 after the real startup script runs with simulated commands. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Treating a sourced shell script’s process settings as local to that file.

## BUG-0014 — Review text and confirmation recovery were not fully visible

- Date: 2026-09-08
- Symptom: The live review showed only HOST; after a wrong confirmation, the error layout hid the correction field below the visible area.
- Root cause: The dynamic multiline wrapping label failed to display the complete summary in the shipped GTK runtime; wrapping error-label sizing also shrank the review viewport.
- Fix: Render the summary in a read-only scrollable text view and keep the concise error line from expanding vertically.
- Regression evidence: wizard-review.py boots the actual GUI and checks visible network/disk summary regions, an error, visible correction input and an unchanged disposable disk. The earlier error capture has zero text pixels in the required correction region and fails the new assertion; final KVM check passes. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Accepting successful string construction as proof the operator can see the review, or checking only the happy-path layout.

## BUG-0015 — Hardware preparation lacked a debconf protocol channel

- Date: 2026-09-08
- Symptom: The preparation log contained raw PROGRESS protocol commands.
- Root cause: Native anna was invoked outside an established debconf frontend after its protocol descriptors had been cleared.
- Fix: Initialize the native noninteractive confmodule before redirecting diagnostic output or calling anna.
- Regression evidence: wizard-review.py adds a test-only success marker only after rejecting raw PROGRESS lines; complete-initrd checks require the shipped preparation script to match the tested source. See [wizard QA](qa/iso/2026-09-08-nex-wizard.md).
- Avoid: Redirecting native installer protocol output into a log instead of establishing its frontend channel.

These regressions run through both `.github/workflows/installer.yml` and
`.gitea/workflows/installer.yml` on pushes and pull requests: `bash installer/check.sh`,
`python3 tests/integration/installer-shell.py`, the compiled `installer-network.c` check,
`python3 tests/integration/installer-handoff.py`, `python3 tests/integration/installer-keyboard.py`,
the complete-initrd `installer-logo.py` check and the native `wizard-review.py` check.
Local results and artifact limits are in the QA report; hosted runs remain unexecuted.

## BUG-0016 — Wizard stayed at 800×600 on larger displays

- Date: 2026-09-08.
- Symptom: Black margins surrounded the installer at 1024×768.
- Root cause: A fixed 800×600 default relied on a maximize request, but Debian Installer runs X without a window manager to honor it.
- Fix: Size and position the undecorated native window against the actual GDK screen and handle screen size changes.
- Regression evidence: The old ISO reproduced the black margins in QEMU at 1024×768. `wizard-review.py --resolution 1024x768` checks all display corners; both CI providers also retain the 800×600 walkthrough. Final results are recorded in [QoL QA](qa/iso/2026-09-08-installer-qol.md).
- Avoid: Assuming a window-manager maximize/fullscreen hint resizes a window in the installer environment.

## BUG-0017 — Timezone completion did not open in the native installer

- Date: 2026-09-08.
- Symptom: A city matched the timezone data, but typing it produced no selectable popup in the actual installer.
- Root cause: The window had not been explicitly presented/focused in the installer’s window-manager-free X session; callback-only matching tests did not exercise GTK popup activation.
- Fix: Present the native wizard after showing and sizing it; retain canonical timezone validation after selecting a match.
- Regression evidence: Native QEMU reproduced the absent popup, then selected America/New_York and Australia/Sydney after the fix. `installer-qol.py` checks case-insensitive and space/underscore matching; `wizard-review.py` exercises actual popup selection and review at both resolutions in GitHub and Gitea CI. See [QoL QA](qa/iso/2026-09-08-installer-qol.md).
- Avoid: Treating a successful string-matching unit check as proof that a native GTK popup can be opened and selected.

## BUG-AP-0001 — Appliance ownership must survive management revocation

- Date: 2026-09-08.
- Symptom: Independent review found that revoking another host could remove its observed VM UUID from appliance-adoption conflict checks.
- Root cause: The conflict query filtered revoked hosts, confusing loss of management permission with exclusion of a running guest.
- Fix: Include every other host's conflicting UUID observation; require fresh observations to resolve the conflict. Local power also checks complete domain/disk identity, duplicate writable references and hardlinks.
- Regression evidence: `.cache/toolchains/go/bin/go test -race -count=1 ./...` passed; `internal/controller/appliance_test.go` covers revoked-host conflicts and restart freshness, and `internal/vmhost/engine_test.go` covers local ownership/duplicate disk rejection. Both GitHub and Gitea Phase 1 workflows run the Go suites on pushes and pull requests.
- Avoid: Treating revocation, a timeout, or a stopped management service as storage fencing.

## BUG-AP-0002 — Appliance recovery must preserve complete private identity

- Date: 2026-09-08.
- Symptom: Review found that a partial recovery archive could omit the host CA, and safe archive extraction could leave private state owned by root rather than the Controller service account.
- Root cause: Path validation alone did not prove archive completeness, integrity, matching source, or usable restored ownership.
- Fix: Require the database, CA pair, TLS pair and initialization marker; verify the recorded file set and hashes before mutation, serialize against live Controller state, and restore private service ownership.
- Regression evidence: `python3 -m unittest discover -s tests -p 'test_*.py'` passed 42 checks at this verification point, including missing-CA, tampered-file, traversal/link, and restored-ownership checks in `tests/test_appliance_build.py`. Both CI providers run this discovery command; real replacement recovery is tracked separately in [appliance lab evidence](qa/appliance/lab.md).
- Avoid: Reporting a successful file extraction as a working Controller recovery.

## BUG-AP-0003 — Guest package inventory format was expanded by the shell

- Date: 2026-09-08.
- Symptom: The first appliance image build failed while recording the installed package/source inventory.
- Root cause: The guest shell expanded dpkg-query's `${source:Package}` format expression and rejected it as shell substitution.
- Fix: Preserve literal dpkg-query format variables through the guest shell.
- Regression evidence: `tests/test_appliance_build.py` runs the actual `/bin/sh` quoting boundary with a fake dpkg-query and verifies the exact format argument; the original quoting fails and the corrected command passes. The full Python suite passes and is discovered on pushes and pull requests by both CI providers.
- Avoid: Assuming nested shell quotation preserves a downstream tool's dollar-prefixed syntax.

## BUG-AP-0004 — QEMU could not create the appliance console socket

- Date: 2026-09-08.
- Symptom: Real nested-KVM deployment retained its defined VM but failed to start when QEMU could not bind `console.sock` in the private disk directory.
- Root cause: Domain XML placed a QEMU-created socket inside a directory owned by the host service; a simulated virsh success did not exercise QEMU's filesystem permissions.
- Fix: Let libvirt allocate its native private UNIX socket path using a socket listener with no explicit pathname. Keep restricted disk-directory permissions and no public VNC listener.
- Regression evidence: `TestConsoleSocketUsesLibvirtRuntimeDirectory` failed before the XML fix and `go test -race ./internal/vmhost` passes afterward. Both GitHub and Gitea run this suite. [Real failure and subsequent lab verification](qa/appliance/lab.md) distinguish actual QEMU behavior from command simulation.
- Avoid: Making the disk directory generally writable or exposing TCP VNC to work around a private-socket ownership error.

## BUG-AP-0005 — Package builder could not create a nested read-only mountpoint

- Date: 2026-09-08.
- Symptom: The first real host-package Docker run failed before compilation because `/input/bundle` did not exist under the read-only input mount.
- Root cause: The builder expected Docker to create a nested mount destination inside an already read-only parent mount.
- Fix: Create the bundle mountpoint in the staged input before launching Docker.
- Regression evidence: `tests/test_core_local_package.py` exercises the valid signed-bundle orchestration and checks the staged mountpoint. The corrected package built and passed disposable Debian package installation/removal checks; see [package evidence](qa/appliance/package.md). Both CI providers discover the Python regression suite.
- Avoid: Treating Dockerfile compilation as proof the actual container mount layout works.

## BUG-AP-0006 — First-boot initialization had a systemd ordering cycle

- Date: 2026-09-08.
- Symptom: Real Debian boot skipped appliance initialization, although cloud-init configured the VM network.
- Root cause: The initializer was wanted by `multi-user.target` but ordered after Debian's `cloud-final.service`, which itself runs after that target.
- Fix: Enable initialization under `cloud-init.target`, retaining the prerequisite ordering without a cycle.
- Regression evidence: `test_firstboot_has_no_debian_cloud_final_ordering_cycle` runs actual `systemd-analyze verify` against the shipped unit and Debian's relevant target ordering. The old unit fails; the fixed test passes on the workstation and Debian 13 builder. Both CI workflows discover it; real boot evidence is recorded in [the lab report](qa/appliance/lab.md).
- Avoid: Treating valid unit syntax as proof that a dependency graph can start.

## BUG-AP-0007 — Private build permissions made public Controller files unreadable

- Date: 2026-09-08.
- Symptom: Controller API login worked in the actual guest, but the browser GUI returned 403 and corresponding source was unreadable.
- Root cause: The build's private umask propagated root-only modes onto public UI/source files served by the unprivileged Controller account.
- Fix: Apply readable/traversable modes to the public share only; retain private credentials and state permissions.
- Regression evidence: `test_public_guest_assets_are_readable_by_service_user` executes the shipped chmod boundary on private fixtures and verifies resulting access bits. Real GUI/source checks accompany [package](qa/appliance/package.md) and [guest](qa/appliance/lab.md) evidence. Both CI providers run the Python regression.
- Avoid: Weakening credential/state permissions to make public assets readable.

## BUG-AP-0008 — API success was mistaken for completed appliance installation

- Date: 2026-09-08.
- Symptom: The first real build reported Ready while its GUI returned 403 and first-boot credential cleanup had not finished.
- Root cause: Handoff proved enrollment/adoption but not the first-boot completion boundary; cleanup failures could also have been ignored.
- Fix: Publish a per-instance readiness marker only after successful GUI access and verified cleanup of both persistent and runtime cloud-init caches. Handoff requires that versioned marker and GUI HTTP 200 over pinned HTTPS before completion; completed handoff records retain readiness-version evidence.
- Regression evidence: The handoff integration test failed before the fix when API success completed without the marker/GUI. Focused and combined Go race suites pass afterward. The firstboot test injects cleanup failure, verifies no ready marker, and proves retry succeeds; both CI providers run these suites.
- Avoid: Treating a booted VM or a successful API call as a finished user workflow, or swallowing credential-cleanup errors.

## BUG-AP-0009 — Virsh interactive framing broke recovery transfer parsing

- Date: 2026-09-08.
- Symptom: Actual host-root recovery export failed while parsing domain XML and guest-agent replies.
- Root cause: Stdin keeps private recovery payloads out of process arguments, but virsh adds prompts and can truncate or wrap echoed commands; the original parser expected plain output.
- Fix: Validate short-command framing and require exactly one guest-agent return/error JSON response, withholding ambiguous output and secrets from errors.
- Regression evidence: `tests/test_host_recovery.py` covers prompt framing, ambiguous replies, transfer and secret transport; six checks pass. Real QGA export then transferred the archive off the host and verified its six required file hashes. Both CI providers discover this test file.
- Avoid: Exposing archive contents in process arguments or assuming a simulated subprocess returns the same framing as the real tool.

## BUG-AP-0010 — The packaged service umask removed QEMU directory traversal

- Date: 2026-09-08.
- Symptom: A clean deployment through the real systemd package failed because the per-VM directory was 0700, so QEMU could not open its disk.
- Root cause: `mkdir`'s requested 0711 was reduced by the service's required `UMask=0077`; the earlier directly launched development process did not reproduce that environment.
- Fix: Exclusively create the owned VM directory, then explicitly set 0711. Keep state/seed directories 0700 and disk, seed ISO, and credential files 0600.
- Regression evidence: `TestApplianceDirectoryTraversableUnderPrivateUmask` sets the actual process umask to 0077 and failed with mode 0700 before the correction. The full vmhost race suite passes afterward; both CI providers run it. The rejected package and subsequent clean package walkthrough are distinguished in [lab evidence](qa/appliance/lab.md).
- Avoid: Weakening the service umask, or treating a requested creation mode as the resulting filesystem mode.

## BUG-0018 — Installer rejected valid qualified hostnames

- Date: 2026-09-08.
- Symptom: `nexcore.matthome` was rejected on Host and Network.
- Root cause: GUI and backend separately enforced a single-label 63-character rule; generated configuration also assumed a short name.
- Fix: Shared standards-based ASCII hostname validation and root-dot handling; split Debian short hostname/domain and retain full/short hosts aliases.
- Regression evidence: User screenshot and prior validation reproduced rejection. `installer-network.c`, `installer-hostname.py` and native `wizard-review.py` cover syntax boundaries, generated configuration and qualified-name UI flow, invoked in both GitHub and Gitea installer workflows. Actual results and standards are in [hostname QA](qa/iso/2026-09-08-installer-hostname.md).
- Avoid: Merely permitting dots in a UI regex while backend validation or installed hostname configuration still assumes one label.

## BUG-0019 — NIC status confused disabled interfaces with absent link

- Date: 2026-09-08.
- Symptom: A connected virtual NIC displayed `down` during setup.
- Root cause: The GUI read raw operstate before administrative activation and only refreshed on selection.
- Fix: Perform a link-only probe, read carrier with administrative-state checks, and refresh status without reloading form values.
- Regression evidence: `installer-link.py` covers probe/status behavior; native `wizard-review.py` checks QEMU link disconnect/reconnect and preservation of input. Both GitHub and Gitea installer workflows run them. See [link QA](qa/iso/2026-09-08-installer-link.md) for actual results and limits.
- Avoid: Renaming every `down` state to “No link” without probing carrier, or refreshing the NIC by reloading its editor.

## BUG-0020 — Installer offered Xen disks then rejected them

- Date: 2026-09-08.
- Symptom: `/dev/xvda` appeared in Disk and Review, but installation failed with “Select a supported physical installation disk.”
- Root cause: discovery and backend used different disk-name eligibility rules; backend omitted Xen `xvd`.
- Fix: share the existing name policy and include `xvd`, preserving all subsequent device safety checks.
- Regression evidence: `tests/integration/installer-disk.py` fails the original validator and passes the fix with simulated device boundaries, including unsafe-target rejection; both installer CI workflows invoke it. See [QA](qa/iso/2026-09-08-installer-xen-disk.md).
- Avoid: bypassing disk validation or hiding a disk that the supported backend can install onto.

## BUG-SEC-0001 — Unauthenticated requests consumed shared login capacity

- Date: 2026-09-08.
- Symptom: Two incomplete Controller login bodies caused a separate valid login to receive HTTP 429; both services also coupled clients through one global cooldown.
- Root cause: Controller reserved password-work slots before reading untrusted bodies; admission state did not distinguish direct network peers.
- Fix: Finish bounded credential/origin validation before reserving hashing capacity; limit each actual peer to five attempts per minute, bound peer tracking and retain two global hashing slots. Ignore untrusted forwarding headers.
- Regression evidence: `TestIncompleteLoginDoesNotReserveHashCapacity` failed with 429 before the fix and passes afterward; peer/proxy isolation checks and full Go race suites pass. Both GitHub and Gitea Phase 1 workflows run `go test -race ./...` on push/PR and weekly. See [security checks](qa/security/checks.md).
- Avoid: Removing expensive-work limits or using a global cooldown that lets one unauthenticated peer block unrelated logins.

## BUG-SEC-0002 — Password byte length did not enforce the displayed character minimum

- Date: 2026-09-08.
- Symptom: Eight two-byte Unicode characters satisfied the appliance's advertised sixteen-character minimum.
- Root cause: Backend validation counted UTF-8 bytes instead of Unicode characters; guest firstboot and bootstrap callers also had inconsistent limits.
- Fix: Shared validation requires at least sixteen Unicode code points and no more than 1024 UTF-8 bytes for newly provisioned passwords; align guest validation and browser feedback while preserving existing password hashes.
- Regression evidence: `TestPasswordMinimumCountsCharacters` failed before the fix and passes with the full vmhost race suite. Actual browser checks reject eight supplementary characters and oversized UTF-8 input, and accept sixteen characters. Both CI providers execute the Go regression on push/PR and weekly; guest validation is covered by Python discovery.
- Avoid: Treating byte counts or JavaScript UTF-16 code-unit length as a Unicode character count.
- Policy supersession (2026-09-08): Matt explicitly removed the sixteen-character minimum.
  Current bootstrap accepts nonempty UTF-8 passwords within the transport bound; historical
  minimum-length evidence above describes the former policy. Shared validation still
  prevents inconsistent byte/encoding handling. See [deployment usability QA](qa/appliance/deployment-usability.md).

## BUG-SEC-0003 — Child processes inherited ownership locks

- Date: 2026-09-08.
- Symptom: Actual browser restart verification twice found the local service refusing startup after its original process exited; a still-running child retained the ownership lock.
- Root cause: Three raw `syscall.Open` lock calls in local management, recovery preparation and agent storage omitted close-on-exec. Go's `os.OpenFile` callers already set it.
- Fix: Set `O_CLOEXEC` atomically on all three raw lock openings; retain the locks' exclusive ownership semantics.
- Regression evidence: Real subprocess tests in `internal/localcore/lock_test.go` and `internal/agent/lock_test.go` failed before the fix with retained-lock errors. They now prove reacquisition while the child remains alive and after it exits; agent/localcore race suites and actual local browser restart pass. Both CI providers run these regressions through `go test -race ./...` on push/PR and weekly.
- Avoid: Delaying restart until children happen to finish, removing ownership locks, or setting close-on-exec later through a race-prone separate call.

## BUG-SEC-0004 — Browser CI referenced a binary it never built

- Date: 2026-09-08.
- Symptom: Both workflow definitions invoked host-local browser QA with its default binary path but did not build that artifact on a clean runner.
- Root cause: Controller/agent integration built private temporary binaries; that did not provide `dist/nex-core-local` for the independent local browser suite.
- Fix: Build the local service explicitly before its browser step in both workflows.
- Regression evidence: `python3 -m unittest discover -s tests -p test_ci_security.py` failed for both providers before the fix and passes afterward; both workflows run this test through Python discovery on push/PR and weekly. Local actual browser suites pass with explicitly built artifacts. No hosted CI execution is claimed.
- Avoid: Assuming an ignored workstation build artifact will exist on a clean CI runner.

## BUG-0021 — Normal keyboard setup appeared as a startup error

- Date: 2026-09-08.
- Symptom: initial Welcome page showed a red “Keyboard layout changed” notice.
- Root cause: successful initial layout application used the shared error-message area.
- Fix: successful keyboard application clears the message; real application errors and password clearing remain.
- Regression evidence: `installer-keyboard.py` silent-success assertion failed before the fix and passed afterward. Both installer CI workflows already execute this check.
- Avoid: removing initial keyboard application or credential clearing to hide the message.

## BUG-0022 — Progress gaps left the installer panel empty

- Date: 2026-09-08.
- Symptom: the main panel went blank between backend installation steps.
- Root cause: cdebconf destroys progress widgets at STOP and clears answered question widgets; the custom shell provided no content during the next-step gap.
- Fix: present a waiting panel between steps, replaced by real progress or interactive prompts.
- Regression evidence: `tests/integration/installer-progress.py` failed on the original blank gap and passed on the rebuilt ISO, including error/log recovery and completion acknowledgement. Both GitHub and Gitea installer workflows invoke it. See [native progress QA](qa/iso/2026-09-08-installer-progress.md) for actual checks and limits.
- Avoid: hiding recovery questions, fabricating completion percentages, or retaining a completed task as live progress.

## BUG-0023 — Waiting fallback still swapped with the stock progress layout

- Date: 2026-09-08.
- Symptom: “Preparing the next installation step” alternated with the differently positioned stock Debian progress bar, still looking like flicker.
- Root cause: BUG-0022 filled empty intervals but retained two distinct progress presentations and widget lifecycles.
- Fix: a persistent Nex panel receives actual step updates and remains in place at component boundaries.
- Regression evidence: native `tests/integration/installer-progress.py` checks active/gap/resumed geometry and failure/log/completion behavior; both installer CI workflows invoke it. Actual results are recorded in [continuous progress QA](qa/iso/2026-09-08-continuous-progress.md).
- Avoid: treating a nonempty waiting screen as proof of smooth transitions; inspect active and inactive layouts together.

## BUG-0024 — Installer-selected management NIC lacked a deployment bridge

- Date: 2026-09-08.
- Symptom: after choosing management networking in the GUI installer, local Controller
  deployment asked the operator to configure an existing management bridge.
- Root cause: the installer assigned host addressing directly to the NIC/VLAN while
  deployment requires a Linux bridge; automatic service discovery cannot invent one.
- Fix: stage native `br0` with the selected NIC/VLAN as its port and management addressing
  on the bridge, retaining secondary NIC and offline behavior. Bundle bridge-utils.
- Regression evidence: the generated-config check failed before the fix and passes now;
  `bash tests/integration/installer-bridge.sh` runs native bringup/recovery;
  [bridge QA](qa/iso/2026-09-08-management-bridge.md) records commands, results and limits.
- Avoid: hiding preflight failure or changing live management networking from deployment;
  build the selected network during fresh installation and verify actual native bringup.

## BUG-0025 — DHCP preview text leaked into static editing during development

- Date: 2026-09-09.
- Symptom: switching from DHCP preview to static mode retained transient status text.
- Root cause: mode changes only cleared the address when leaving static mode.
- Fix: clear transient entries on every mode change, then restore saved static settings
  only when loading a NIC. Keep DHCP feedback readonly with normal text contrast.
- Regression evidence: independent source review found the defect before delivery;
  `tests/integration/wizard-review.py --nics 2` now asserts the static address is blank
  after a real DHCP preview. [DHCP QA](qa/iso/2026-09-09-installer-dhcp.md) records
  executed commands/captures; no failing-before runtime run is claimed.
- Avoid: storing status text in configuration or treating readonly feedback as disabled
  low-contrast text.

## BUG-0026 — Desktop Help menu clipped on narrow screens during development

- Date: 2026-09-09.
- Symptom: the Controller's new Help dropdown extended beyond the left edge at 360px.
- Root cause: a mobile last-menu rule right-aligned a popup wider than the menu's
  distance from the left viewport edge.
- Fix: retain the shared left alignment used by the other menus.
- Regression evidence: the new bounds assertion in `tests/integration/phase1_browser.py`
  failed with `Help menu outside narrow viewport` before the CSS correction. The same
  complete browser command and final result are recorded in
  [desktop layout QA](qa/controller-desktop-layout/checks.md). Both
  `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml` already run
  `tests/integration/phase1.py --browser` on pushes and pull requests.
- Avoid: aligning menus by first/last position without checking the actual popup bounds.

## BUG-0027 — Website wordmark retained a grey raster background

- Date: 2026-09-09.
- Symptom: the header and footer wordmarks displayed a pale rectangular patch on
  the website canvas.
- Root cause: the approved raster brand board has an opaque pale backdrop; CSS
  multiply blending multiplied that backdrop into the page instead of removing it.
- Fix: whiten the pale backdrop with a shared CSS brightness filter before blending.
  Retain the original logo shapes and apply the same rule to header/footer and mobile.
- Regression evidence: `tests/integration/website_browser.py` now compares rendered
  logo background pixels with the page canvas at desktop/tablet/mobile sizes. Running
  it against the original VM build failed with `Logo raster background differs from
  the page` before the CSS correction. Final command/results and inspected captures
  are recorded in [website QA](qa/website/checks.md). Both GitHub and Gitea phase1
  workflows invoke this same browser check on pushes and pull requests.
- Avoid: assuming multiply blending makes a nonwhite raster background transparent,
  or fixing only the header while leaving the shared footer/mobile treatment broken.

## BUG-0028 — Website gallery reused personal workstation identity

- Date: 2026-09-09.
- Symptom: gallery screenshots displayed the contributor's workstation name and
  underlying machine inventory, unsuitable for intended public-facing images.
- Root cause: reusing internal acceptance captures whose agent collected native
  workstation telemetry, rather than a dedicated neutral demonstration fixture.
- Fix: run a fresh disposable Controller, enroll a fictional host through existing
  TLS/mTLS contracts, report explicit demo inventory and capture its actual browser
  views. No native probes, personal data, DOM substitutions or image editing are used.
- Regression evidence: `tests/integration/website_controller_captures.py` checks
  returned inventory and rendered identity before saving each image, rejects the
  original personal host label/name in negative guard checks, and asserts credentials
  are absent. Both GitHub and Gitea phase1 workflows run it on pushes and PRs.
  Original failure was identified by visual inspection, not OCR; see the
  [fresh-capture evidence](qa/website-controller-captures/results.json) and
  [website walkthrough](qa/website/checks.md) for executed results and limits.
- Avoid: publishing general internal QA screenshots without reviewing personal
  identity and telemetry, or treating absence of passwords as a complete privacy check.

## BUG-0029 — Website source README included private deployment details

- Date: 2026-09-09.
- Symptom: the website folder's contributor README named a private deployment
  address, workstation account/home path and environment-specific restart commands.
- Root cause: keeping an internal deployment handoff in prospective public source.
- Fix: use generic build/deploy guidance and explain reviewed website contributions;
  ignore local credentials/configuration and keep generated artifacts out of Git.
- Regression evidence: `python3 -m unittest discover -s tests -p
  'test_website_public_files.py'` failed on the original README, then passed both
  checks after cleanup. It checks Git-visible files (including force-added ignored
  files), private deployment patterns, credential markers and package download URLs.
  Both phase1 workflows already run this suite on pushes and PRs via `test_*.py`.
  See [website source review](qa/website/public-source-review.md) for scope and limits.
- Avoid: copying real deployment instructions into public contributor documentation
  or treating `.gitignore` as protection for files already tracked by Git.

## BUG-0030 — Installer logo included the textured reference-board background

- Date: 2026-09-09.
- Symptom: the installer logo looked distorted with a pale rippled rectangle.
- Root cause: both native GTK headers cropped the opaque textured brand board.
- Fix: use one dedicated transparent Nex Core lockup, proportional GDK scaling,
  and the original widget allocation to preserve the wizard and progress layout.
- Regression evidence: `tests/integration/wizard-review.py` fails the original
  initrd's logo-background pixel check and passes the rebuilt native header;
  `tests/integration/installer-logo.py` checks matching asset/source packaging.
  Both installer workflows run these tests on pushes and pull requests. See
  [before/after captures and executed commands](qa/iso/2026-09-09-logo-cleanup.md).
  No full ISO rebuild or hosted CI execution is claimed.
- Avoid: shipping a design-reference board crop as a production logo, or reducing
  the image allocation and shifting unrelated installer controls.

## BUG-0031 — Agent service sandbox blocked Linux network inventory

- Date: 2026-09-09.
- Symptom: the packaged agent reported `netlinkrib: address family not supported
  by protocol`; host CPU/memory inventory remained unavailable and valid VM
  creation failed its capacity check in the disposable native lab.
- Root cause: `RestrictAddressFamilies` omitted AF_NETLINK, which Go's Linux
  network-interface discovery requires.
- Fix: permit AF_NETLINK alongside the existing local/IP socket families.
- Regression evidence: `python3 -m unittest discover -s tests -p
  'test_agent_service.py' -v` passes; the same test run with the original HEAD
  service in an isolated temporary tree fails with `'AF_NETLINK' not found`.
  Both phase1 workflows run it via `test_*.py` on pushes and pull requests.
  The [native managed-VM run](qa/managed-vm/checks.md) also passed actual network
  inventory and VM creation under the corrected packaged systemd unit; the
  static check alone is not runtime proof.
- Avoid: weakening placement validation to accept missing inventory, or disabling
  all socket restrictions instead of allowing the required native family.

## BUG-0032 — Agent result backlog exceeded the Controller report limit

- Date: 2026-09-09.
- Symptom: a large backlog of completed operations made every reconnect report
  fail with HTTP 400, preventing acknowledgement and recovery.
- Root cause: the agent sent all unacknowledged results while Controller accepts
  at most 100 per report.
- Fix: send at most 100 durable results per report and continue draining only as
  Controller acknowledges them; do not discard unacknowledged results.
- Regression evidence: `go test -race ./internal/agent` passes with
  `TestResultBacklogDrainsInControllerSizedBatches`, which drains 150 results in
  100/50 batches and verifies all acknowledgements. The test failed without the
  batching fix with `controller rejected request (HTTP 400)`. Both phase1
  workflows run the Go suite on pushes and pull requests; hosted runs were not
  triggered by this unpushed change.
- Avoid: dropping old results, clearing the journal, or weakening Controller's
  bounded-input validation to accommodate an unbounded client report.

## BUG-0033 — VM details read configuration from the wrong response shape

- Date: 2026-09-09.
- Symptom: a newly created guest's details omitted CPU, memory, disk, firmware
  and its creation task.
- Root cause: the UI expected flat configuration fields, but the VM API returns
  the requested configuration in `spec`.
- Fix: read `vm.spec` and render the creation task independently when an operation
  ID exists.
- Regression evidence: `tests/integration/managed_vm_browser.py` checks exact
  selected resources and the successful creation task in real browser rendering.
  An isolated reproduction of the original flat-field conditional failed the CPU
  assertion; the actual current bundle passed all selected values and task state.
  See [the reproduction and its limits](qa/managed-vm/browser-regression-results.json).
  Both phase1 workflows invoke the simulated-host browser mode on pushes and PRs;
  native browser evidence is recorded separately.
- Avoid: treating a successful create response as proof that its settings and
  durable task are visible, or guessing a response shape instead of checking it.

## BUG-0034 — Oversized operation errors blocked agent reporting

- Date: 2026-09-09.
- Symptom: a long filesystem error caused HTTP 400 on every report, preventing
  acknowledgement and fresh host reporting.
- Root cause: an accepted long storage path produced an OS error longer than
  Controller's 4096-byte result-error limit; the agent forwarded it unbounded.
- Fix: limit the outbound error to a valid UTF-8 prefix within 4096 bytes while
  retaining the full error in the private durable journal.
- Regression evidence: `go test ./internal/agent -run
  TestOversizedResultErrorDoesNotBlockHeartbeat -count=1` failed before the fix
  with `controller rejected request (HTTP 400)`. The final
  `go test -race -count=1 ./internal/agent ./internal/vmhost` passed (2.481s/1.406s),
  including acknowledgement, continued reporting and retained local error detail.
  Both phase1 workflows run these Go tests on pushes and pull requests.
  [Native supplemental verification](qa/managed-vm/agent-reconnect.json) also
  exercised the actual systemd agent: a 4122-byte local error produced an accepted
  4096-byte report and subsequent fresh heartbeat after restart.
- Avoid: truncating away local recovery diagnostics, cutting invalid UTF-8 on
  the wire, or weakening the Controller's bounded-input validation.

## BUG-0035 — Website preview hosting exposed listings and lacked browser policy

- Date: 2026-09-09.
- Symptom: the deployed static website listed asset directories and returned pages
  without CSP, framing, MIME, referrer or HTTPS transport policy headers.
- Root cause: the temporary Python HTTP preview remained the deployed web server.
- Fix: use the dedicated unprivileged nginx service, read-only releases, disabled
  listings and asset symlinks, restrictive headers on success/error responses, and
  relative directory redirects through the HTTPS proxy.
- Regression evidence: `python3 -m unittest discover -s tests -p
  'test_website_*.py' -v` exercises actual nginx and reproduces the prior Python
  behavior in disposable fixtures. The live pre-cutover CSP assertion failed;
  post-cutover policy, HTTPS browser and all 11 asset-hash checks passed.
  Both GitHub and Gitea phase1 push/PR workflows install nginx and run the unit
  discovery plus `tests/integration/website_hardening_browser.py`.
  See [hosting evidence](qa/website-security/hardening.md) for exact results and limits.
- Avoid: treating valid proxy TLS as evidence that origin directory/response policy
  is hardened, weakening CSP to accommodate test-only injected resources, or using
  absolute origin redirects that reveal the private HTTP port.

## BUG-0036 — Site dropdown retained an unintended keyboard Tab stop

- Date: 2026-09-09.
- Symptom: Tab from the site dropdown's search field moved focus into its scrolling
  option container instead of closing the popup and moving to the next control.
- Root cause: Chromium made the scrollable listbox implicitly keyboard-focusable;
  the picker already manages option navigation through the search input.
- Fix: exclude the listbox container from sequential focus with `tabIndex={-1}`;
  retain arrow-key selection and Escape focus restoration.
- Regression evidence: [before/after browser record](qa/site-selector/popup-tab-regression.json).
  `tests/integration/site_selector_browser.py` opens the popup, presses Tab from
  search and asserts dismissal. Both GitHub and Gitea Phase 1 push/PR workflows
  run this check in Chromium. The original popup build failed; final execution
  and exact build identity are recorded in that evidence.
- Avoid: removing the Tab assertion, trapping keyboard focus, or disabling scrolling
  to hide browser-native focus behavior.

## BUG-0037 — Host-local enrollment raced the running embedded agent

- Date: 2026-09-09.
- Symptom: `go test -race` on the new `POST /api/local/v1/enrollment` re-enrollment test
  reported a data race: `agent.Enroll` mutated a Store whose embedded `agent.Run` loop
  (started by an earlier enrollment) was reading and writing the same in-memory state.
- Root cause: `nex-core-local` runs the enrolled agent in-process from the shared
  handoff registry, and the enrollment handler called `agent.Enroll` without pausing it.
- Fix: each running agent gets a stoppable per-agent context (`internal/localcore/handoff.go`);
  the enrollment handler stops it before `agent.Enroll`, restores the pre-attempt
  controller/trust on failure, and restarts it afterward.
- Regression evidence: `TestEnrollmentReplaceHostSuccess` (`internal/localcore/enroll_test.go`)
  and `TestAddSiteHostReEnrollmentSuccess` (`internal/controller/addhost_test.go`) reproduced
  the race before the fix and pass under `go test -race ./...`, which both GitHub and Gitea
  Phase 1 push/PR workflows run. The real-lab re-enrollment in
  [add host lab checks](qa/simple-host-enrollment/lab/checks.md) passed twice.
- Avoid: calling `agent.Enroll` or otherwise mutating a Store while its `Run` loop is live,
  or tearing down the registry entry when only pausing the agent.

## BUG-0038 — Add host pushed the host-identity CA instead of the served certificate

- Date: 2026-09-09.
- Symptom: found while preparing the lab: the appliance serves a self-signed HTTPS
  certificate, but `POST /api/v1/sites/{id}/hosts` pushed the Controller's host-identity CA
  as the host's trust, so `agent.Enroll` on a real host would fail with
  `certificate signed by unknown authority`.
- Root cause: the unit test signed its Controller listener with that CA, masking the
  mismatch between HTTPS server trust and host-identity issuance.
- Fix: `cmd/nex-controller/main.go` reads `-tls-cert` into `Config.ServerCertPEM`; the push
  sends that certificate, and the route answers 503 before any host contact when it is
  absent. Contract and `docs/API.md` updated.
- Regression evidence: `TestAddSiteHostSuccess` now serves a self-signed listener and fails
  on the old value; `TestAddSiteHostRequiresServerCertificate` covers the 503. Both run
  under `go test -race ./...` in the GitHub and Gitea Phase 1 workflows. Real enrollment
  through the fixed binary passed in the [lab checks](qa/simple-host-enrollment/lab/checks.md).
- Avoid: test listeners chained to the host-identity CA, or treating the host CA as the
  Controller's HTTPS trust anchor.

## BUG-0039 — libvirt probe blocked on `virsh dominfo` for defined domains

- Date: 2026-09-09.
- Symptom: found by the Controller-replacement check on Debian 13 (libvirt 11.3.0): once a
  domain was defined, the agent's libvirt observation reported `unknown` with
  `probe deadline exceeded; previous observation retained`, so adopted VMs never showed a state.
- Root cause: the probe called `virsh dominfo` per domain; on that libvirt build `dominfo`
  prints the first fields and then blocks (read-only and read-write connections alike),
  exceeding any probe deadline. One blocking call stalled the whole VM observation.
- Fix: `internal/agent/probes.go` reads `virsh domstate` and `virsh domname` per domain,
  which answer from the domain object only.
- Regression evidence: `tests/integration/controller_replacement_debian.sh` (both Phase 1
  workflows) defines a domain through the real agent in a Debian 13 libvirt container and
  waits for Controller B to observe it `shut off` with fresh libvirt data; it failed on the
  old probe and passes with the fix ([evidence](qa/pool-vlan-networks/controller-replacement-debian.json)).
- Avoid: per-domain `dominfo` in bounded probes; prefer single-purpose queries.

## BUG-0040 — Update tests reached the public apt repository after the loopback rewrite

- Date: 2026-09-09.
- Symptom: Gitea Actions `phase1` failed at "Test Go services and recovery" on every push since
  `aeb32d4`: `TestUpdatesAuthAndApply` and `TestLocalUpdatesAuthAndApply` reported
  `RepoURL:https://nexvirtualization.com/apt ... Error:updates index HTTP 401`; locally they
  returned the real published version instead of the test server's `9.9.9`.
- Root cause: `NormalizeRepoURL` now replaces plain-http private/loopback repository URLs with the
  public default, so the `httptest.NewServer` URL each test exported through `NEX_UPDATE_REPO`
  was discarded and the tests depended on network access to the public site. The `nexupdate`
  package tests were switched to TLS in `8d587e8`, but the Controller and Core local tests were not.
- Fix: `internal/controller/updates_test.go` and `internal/localcore/updates_test.go` serve the
  fake index over `httptest.NewTLSServer` and trust it through `nexupdate.HTTPClient`, matching
  the `nexupdate` tests. The same change regenerated the stale `docs/api/index.html`. The
  `documentation` workflow failure on `8d587e8` had a separate cause: evidence entries in
  `docs/changes/2026-09-09-core-update-progress-stuck.json` carried test names after the path,
  so they were not files; that record is historical and is left unchanged because the checker
  re-validates edited records against the current diff range.
- Regression evidence: `go test -race ./...` (both Phase 1 workflows) failed on the old tests
  whenever the public repository rejected or differed from the fixture and passes with the fix;
  `python3 scripts/generate_api.py --check` covers the reference.
- Also fixed in the same change: `tests/test_appliance_build.py` called `firstboot.main()`, which
  sets a process-wide `077` umask, and never restored it, so `test_apt_publish` failed with
  `control directory has bad permissions 700` under full discovery; the test now restores the
  umask. `docs/changes/2026-09-09-iso-fresh-source.json` cited an untracked ISO as evidence,
  which failed the `documentation` workflow on `76037a5`; it now cites the tracked scripts.
- Also fixed: `packaging/core-local/core-local-setup` decided whether the local TLS certificate
  matched the host address from the exit status of `openssl x509 -checkip`, which OpenSSL 3.0
  (the Ubuntu 24.04 runner image) returns as 0 on both match and mismatch; the script now reads
  the printed verdict. Nex Core itself runs Debian 13 with OpenSSL 3.5, where the exit status was
  already meaningful, so this failed `test_core_local_package` only in Actions.
- Avoid: plain-http loopback fixtures for update checks; tests that change process-wide state
  without a cleanup; relying on `openssl x509 -check*` exit codes; any `nexupdate` test server must be TLS
  with `HTTPClient` overridden, and verification evidence entries must be bare repository paths.

## BUG-0041 — Controller capture harness rejected its own fixture addresses

- Date: 2026-09-09.
- Symptom: `tests/integration/website_controller_captures.py` (Phase 1 workflows, "website
  controller captures" step) exited before opening a browser with
  `AssertionError: Private IP in capture content`, so no Controller UI evidence was produced.
- Root cause: `aeb32d4` added a privacy guard rejecting RFC 1918 addresses in capture content while
  the simulated inventory fixture still carried `192.168.50.10/24` and `192.168.60.10/24`.
- Fix: the fixture uses documentation addresses (`198.51.100.10/24`, `203.0.113.10/24`), identifies
  as `Nex Core 0.1.4`, and asserts the General view shows one `Version` row and no `Agent version`.
- Regression evidence: the harness itself; it failed at `HEAD` and passes with the fix
  ([evidence](qa/single-nex-core-version/controller-captures.json)).
- Avoid: fixtures with private addresses in any capture that is screened for publication.
