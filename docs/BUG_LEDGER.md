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

## BUG-0042 — Fresh Nex Core install had no Nex package repository

- Date: 2026-09-10.
- Symptom: after installing from the ISO, `apt update` on a Nex Core host listed only the Debian
  repositories; the Nex repo (`https://nexvirtualization.com/apt`) never appeared, so `apt` could
  not see or install Nex packages out of the box.
- Root cause: the `nex-core-local` package shipped `nex.list.example` only to
  `/usr/share/nex/core-local/` and never installed an apt source or the archive signing key, so the
  host had no configured Nex source.
- Fix: the package now ships the dearmored Nex archive key at
  `/usr/share/keyrings/nex-archive-keyring.gpg` and the postinst writes
  `/etc/apt/sources.list.d/nex.sources` (deb822, `Signed-By` the key, `URIs` from `repo_url` in
  `/etc/nex/updates.conf`); the prerm removes that source on package removal so no source is left
  without its key. The committed key was verified against the live repo's `InRelease`.
- Regression evidence: `tests/test_core_local_package.py`
  (`test_postinst_owns_release_identity_from_package_version`,
  `test_apt_source_uri_follows_configured_repo_url`,
  `test_package_ships_archive_keyring_and_builds_the_apt_source`), run by
  `python3 -m unittest discover -s tests -p 'test_*.py'` in both the GitHub and Gitea `phase1`
  workflows.
- Avoid: shipping required out-of-box configuration as an `.example` that must be copied by hand.

## BUG-0043 — CI reported nothing for 18 of 23 checks because one privileged step failed first

- Date: 2026-09-10.
- Symptom: every `phase1` and `installer` run on the Gitea runner had failed since 2026-09-08;
  only `documentation` ever passed. `phase1` stopped at *Verify guest VLAN bridge creation in a
  disposable network namespace* with `unshare: unshare failed: Operation not permitted`, and the
  18 steps after it — unit tests, `generate_api --check`, the UI typecheck and build, dependency
  advisories and every browser workflow — were skipped, so the job reported nothing about them.
  `installer` failed at *Run installer regressions* with `bash: installer/check.sh: No such file
  or directory`.
- Root cause: the runner (`act_runner` v1.0.6) executes jobs inside an unprivileged container
  (`docker.gitea.com/runner-images:ubuntu-latest`). Two consequences. Creating network namespaces
  needs `CAP_SYS_ADMIN`, which that container does not have. And `docker run -v "$PWD:/work"`
  names a path that exists only inside the job container, so Docker created it empty on the host
  and the builder image mounted nothing. Neither is a defect in the checks themselves; both were
  masked because the privileged step ran sixth, ahead of everything cheap and unconditional.
- Fix: reordered `phase1` so every unprivileged check runs before the namespace steps, and gated
  those two steps on a detected capability that emits a loud `::warning::` naming the missing
  coverage when it is absent. `.gitea/workflows/installer.yml` was then scoped to what this runner
  can actually do (Matt's decision, 2026-09-10): `installer/check.sh` and the seven installer
  regressions now run directly in the job container, which only ever needed a toolchain, plus a
  `docker build` that still catches a broken Dockerfile. **Four checks were deleted, not fixed** —
  native bridge verification, DHCP preview, ISO build with initramfs completeness, and the wizard
  review / disk confirmation / installer progress checks. Nothing in that job now establishes that
  a bootable, correct ISO can be produced, and it says so in a `::warning::` on every run.
  `.github/workflows/installer.yml` deliberately keeps the full matrix, since GitHub-hosted runners
  are VMs with the required privileges.
- Regression evidence: `bash installer/check.sh` plus all seven `tests/integration/installer-*.py`
  checks and the `installer-network.c` compile-and-run were executed directly on a workstation,
  outside any container, exactly as the scoped job invokes them — all pass, confirming they never
  needed the builder image. All seven workflow files parse; step order and `if` gating confirmed;
  no `${{ }}` interpolation appears inside any `run:` block. CI configuration otherwise carries no
  automated regression test: a workflow file establishes no coverage by itself. The real check is
  the next pushed run.
- Third pass: with the ordering fixed, phase1 reached 20 of 24 steps and failed at *Verify local
  host browser authentication and preflight blockers* with
  `cc1: fatal error: /workspace/stocky789/Nex/packaging/core-local/nex-root-auth.c: No such file`.
  That file exists and is tracked; `localcore_browser.py` runs `docker run -v {ROOT}:{ROOT}:ro`,
  so the same container-only workspace path mounted empty. The two libvirt steps after it need
  KVM and a running libvirtd, equally unavailable. Rather than discover these one run at a time,
  the two separate probes were replaced by a single `hostcap` step that tests **both** namespace
  creation and a Docker bind mount, and all five host-dependent steps are gated on it. Every
  remaining step passed unaided in run 3789, so the job should now complete with those five
  reported as skipped.
- Second cause, found after two failed attempts to fix the first: **Gitea on git.stockhome.com.au
  executes `.github/workflows/`, not `.gitea/workflows/`.** Two pushes that scoped
  `.gitea/workflows/installer.yml` changed nothing, because the job kept running the `.github`
  copy; the executed step names matched that file exactly, in order. `.gitea/workflows/release.yml`
  had no `.github` counterpart at all, so the tag-driven release workflow would never have fired.
  Both installer files are now identical and `release.yml` exists in both, with a header in each
  naming which directory actually runs.
- Avoid: putting a step that needs host privileges ahead of cheap unconditional checks. A single
  environment failure then hides the entire suite, and the job looks uniformly broken when almost
  all of it would have passed. Equally, do not read the resulting green as full coverage: the
  namespace checks are skipped, not passing, until the runner can create namespaces. And never
  assume which of the two workflow directories a forge executes — confirm it against the step
  names in a real run before concluding a workflow edit had any effect.

## BUG-0044 — Website hosting test asserted against 404s instead of the serving policy

- Date: 2026-09-10.
- Symptom: `test_website_hosting.WebsiteHosting.test_serving_policy_and_headers` failed every
  assertion with `404 != 200`, `404 != 403` and `404 != 301` once the unit tests were reachable
  in CI again.
- Root cause: the fixture directory came from `tempfile`, which creates it mode 0700. The nginx
  master starts as root but drops its workers to an unprivileged user, and that user cannot
  traverse a 0700 directory. `try_files` treats the resulting `EACCES` as "not found" and falls
  through to `=404`, so every path answered 404 and none of the serving policy, header or
  redirect behaviour under test was ever exercised.
- Fix: the fixture is made traversable (`0755` directories, `0644` files) before nginx starts,
  matching production, where `/srv/nex-website/current` is root-owned but readable by the same
  unprivileged worker. Fixed in `4da27f2`.
- Regression evidence: `python3 -m unittest discover -s tests -p 'test_*.py'`, run by the
  `Check contracts and contributor tools` step of both phase1 workflows. That step had been
  skipped on every run since 2026-09-08 because of BUG-0043, which is why this surfaced only
  after the step ordering was fixed; it has not yet been confirmed green in CI.
- Avoid: asserting only on status codes when the server can answer 404 for reasons unrelated to
  the policy under test. A permission error that `try_files` renders as "not found" makes the
  whole suite vacuous rather than failing loudly, so a fixture that nginx cannot read looks the
  same as a policy that does not match.

## BUG-0045 — New maintainer script omitted from the corresponding-source archive

- Date: 2026-09-10.
- Symptom: `packaging/core-local/preinst` was added to the package builder but not to the file
  allowlist in `scripts/build-source.py`, so it was absent from the generated AGPL
  corresponding-source archive.
- Root cause: `packaging/core-local/build.py` extracts that archive and runs `build-inside.sh`
  from the extracted copy, so the builder's `install -m755 packaging/core-local/preinst ...`
  would fail on a file the archive never contained. Two lists have to agree — what the package
  installs and what the source archive ships — and only one was updated. The second is also the
  corresponding source Nex is obliged to distribute, so an omission there is a licensing defect,
  not only a build break.
- Fix: add `packaging/core-local/preinst` to the allowlist, and add
  `test_installed_maintainer_scripts_are_in_the_allowlist`, which cross-checks every packaging
  path referenced by a package builder against the archive allowlist so the next omission fails
  a check instead of a build.
- Regression evidence: `python3 -m unittest discover -s tests -p 'test_build_source.py'` — with
  the allowlist entry removed the new case fails with
  `referenced by a package builder but absent from the source archive: ['packaging/core-local/preinst']`;
  with it restored all 5 pass. Run by the `Check contracts and contributor tools` step of both
  phase1 workflows.
- Avoid: adding a file to a package builder without adding it to the source archive that builder
  is itself built from. Neither an ISO build nor a package build was runnable in CI when this
  landed, so nothing would have caught it before a release attempt.

## BUG-0046 — Local service Close left the enrollment agent racing TempDir cleanup

- Date: 2026-09-10.
- Symptom: Gitea `phase1` failed `TestEnrollmentWithReplacementControllerSucceeds` with
  `TempDir RemoveAll cleanup: unlinkat …/agent: directory not empty` even though the enrollment
  assertions had already passed (run 3801 job 4366).
- Root cause: `Service.Close` cancelled the service context and waited on `s.wg`, but the embedded
  handoff agent goroutine is not on that WaitGroup. The agent could still be writing `state.json`
  when `testing.T.TempDir` deleted the tree, so RemoveAll failed on the busy agent directory.
- Fix: `Close` now calls `releaseHandoffAgent`, which waits for the agent goroutine (or closes a
  leftover store that was never started) before returning. The replacement-enrollment test removes
  the agent directory immediately after `Close` so a regression fails the test instead of only CI
  TempDir cleanup.
- Regression evidence: `go test -race -count=10 ./internal/localcore -run TestEnrollmentWithReplacementControllerSucceeds`
  and `go test -race -count=1 ./internal/localcore` pass. Both GitHub and Gitea phase1 workflows
  already run `go test -race ./...` on push/PR.
- Avoid: treating testing.TempDir cleanup failures as flakes when a background goroutine still owns
  files under that tree; shut the owner down in Close (or an explicit stop) before returning.

## BUG-0047 — Create VM kept a blank "system" disk name instead of the derived default

- Date: 2026-09-10.
- Symptom: Gitea `phase1` failed `managed_vm_browser.py` with
  `AssertionError: disk name 'system' does not derive from VM name 'browser-vm-…'`
  (run 3804 job 4369) after the disk-name default feature landed.
- Root cause: `VMWizard` built `started` disks with `autoDiskName`, then discarded them
  whenever `blank.disks` was present — which it always is after `{...blank, ...restored}`.
  Fresh wizards therefore kept the template name `system`, so renaming the VM never
  matched `autoDiskName(pool, "", i)` and never updated the field.
- Fix: only keep restored disks when the restore payload itself supplies them; otherwise
  start from `autoDiskName`. `managed_vm_browser.py` already asserts the derived default
  and the suffixed second disk.
- Regression evidence: the failing assertion in `tests/integration/managed_vm_browser.py`
  is the detector; it runs in both GitHub and Gitea phase1 workflows. `npm run typecheck`
  passes after the init fix. Full Playwright was not re-run on this workstation.
- Avoid: treating a spread of a non-empty blank draft as "restored state"; check the
  restore payload, not the merged draft, before skipping generated defaults.

## BUG-0048 — Background ISO upload rendered as a panel above Recent tasks

- Date: 2026-09-10.
- Symptom: while an ISO uploaded to the Controller, the activity dock showed a separate
  "ISO upload to Controller" panel stacked on top of the Recent tasks heading and table, so the
  upload looked like a second widget instead of a current task.
- Root cause: `UploadStatus` was a standalone `section.panel` rendered beside `<Tasks>` rather
  than inside its table.
- Fix: replaced it with `UploadRow`, a row that `Tasks` renders first (kind `iso-upload`,
  progress/rate and Cancel in the Status cell, actor and timestamp like any task). Upload state
  now records `actor` and `updated_at`; the table also renders when only the upload exists.
- Regression evidence: `tests/integration/managed_vm_browser.py` now asserts the upload appears
  as a `tr` inside `.activity-dock table tbody` with a progressbar and that no upload `section`
  exists; it runs in both phase1 workflows. Local simulated run passed that assertion and
  captured `browser-simulated-iso-upload-row.png`; the run then stopped on the uncommitted
  toolbar work's ambiguous **Back** button, which is outside this fix.
- Avoid: adding a transient client-side item as a sibling panel of a list it conceptually
  belongs to; render it as an entry of that list.

## BUG-0049 — Create VM topology treated blank sockets as zero while showing "1"

- Date: 2026-09-10.
- Symptom: with Virtual CPUs 4, Sockets looking like 1, Cores 2 and Threads 2, the wizard
  blocked with `0 × 2 × 2 does not`, even though 1×2×2 equals 4. The working 1×1×4 case
  looked identical except sockets had actually been typed.
- Root cause: blank topology fields used `value={n || ""}` with placeholders `"1"` /
  vCPU count / `"1"`. Empty sockets stayed 0 in state while the placeholder looked filled;
  filling only cores and threads tripped validation on the still-zero sockets.
- Fix: remove the misleading placeholders; filling the first topology field seeds the other
  two from the documented default (1 × count × 1); clearing any field clears all three;
  incomplete vs product-mismatch errors are distinct. `applyTopology` / `topologyIssue`
  own the behaviour.
- Regression evidence: `tests/integration/managed_vm_browser.py` fills cores then threads
  with count 4 and asserts sockets becomes `1` with no topology error before the existing
  invalid-product check; both phase1 workflows run it. `npm run typecheck` passes.
- Avoid: using numeric placeholders on optional number fields whose empty state is stored
  as 0 — operators cannot tell placeholder from value.

## BUG-0050 — Create VM dropdowns flickered closed on click

- Date: 2026-09-10.
- Symptom: every native `<select>` in Create VM (Bus, Storage, guest OS, and the rest)
  opened and immediately closed, so the menu could not be used.
- Root cause: `.panel { overflow: hidden }` combined with the desktop shell's nested
  overflow (`desktop-app` / `#main` / `workspace-content`) made Chromium dismiss the
  native select popup on the same click that opened it.
- Fix: stop clipping `.panel` overflow; table scrolling stays on `.table-scroll`.
- Regression evidence: `tests/integration/form_select_overflow.py` fails when `.panel`
  sets a clipping overflow and passes with the fix; headed run requires the Bus menu
  highlight below the control. Both GitHub and Gitea phase1 workflows run it.
  Local headed: Bus menu stayed open (834 highlight pixels).
- Avoid: putting `overflow: hidden` on form panels inside the desktop shell; use a
  dedicated scroll container when clipping is required.
- Superseded: the diagnosis was wrong. The static fixture never reproduced the flicker
  (its headed run captured a fleeting popup, not the shell's behaviour), the `.panel` rule
  had no effect on the native popup, and the dropdowns kept flickering. See BUG-0064 for
  the actual cause and fix; `form_select_overflow.py` was replaced by
  `form_select_picker.py`.

## BUG-0051 — Host console listener let any local uid connect and get an unauthenticated root shell

- Date: 2026-09-10.
- Symptom: independent code review of the staged host console found `HostConsoleSocket`
  listening on the abstract UNIX socket `@nex-core-host-console` with RFB security type
  None and spawning `/bin/bash -l` as root for every accepted connection. Any local
  process — including a QEMU guest process on the same host — could dial that address
  and get a root shell with no Controller ticket, no authentication of any kind.
- Root cause: the host console was built on the same real-listener shape as a normal RFB
  server, but its only legitimate caller is `internal/agent/console.go` in the very same
  process (it dials the returned address and pipes it straight to the Controller). A real
  listener therefore had no legitimate second caller and no reason to be reachable by
  anything else on the host.
- Fix: replaced `HostConsoleSocket() (string, error)` with
  `HostConsoleSession() (net.Conn, error)` in `internal/vmhost/hostconsole.go`, which
  starts the RFB/PTY session on the server half of an in-process `net.Pipe` and returns
  the client half directly — no socket, abstract or otherwise, is ever opened. Updated
  `internal/agent/console.go` (`hostConsoleSession` var) to use the returned `net.Conn`
  directly instead of dialing a socket path.
- Regression evidence: `go test ./internal/vmhost -run TestHostConsoleHasNoLocalListener`
  and `go test ./internal/agent -run TestServeConsoleUsesHostSessionDirectly` pass; the
  first asserts the old socket address is unreachable, the second confirms `serveConsole`
  relays the hook's `net.Conn` without dialing anything. Both run under `go test -race
  ./...` in `.github/workflows/phase1.yml` and `.gitea/workflows/phase1.yml`.
- Avoid: giving an in-process-only consumer a real OS-level listener "because that's how
  RFB servers are normally built" — a listener is reachable by every local uid unless
  something else (peer-credential checks, a network namespace) narrows that, and an
  abstract UNIX socket narrows nothing.

## BUG-0052 — Host console streamed a full frame on every FramebufferUpdateRequest, ignoring incremental

- Date: 2026-09-10.
- Symptom: code review found `serveHostRFB` answered every `FramebufferUpdateRequest`
  (encoding a fresh 1,024,000-byte raw frame) regardless of the request's incremental
  flag; noVNC re-requests immediately after each update, so an idle host console
  streamed full frames back-to-back forever.
- Root cause: the handler discarded the whole 9-byte request body (`io.CopyN(io.Discard,
  c, 9)`) instead of reading the incremental flag, and always called `send()`. The PTY
  reader goroutine made this worse by calling `send()` (a full frame) after every read
  chunk instead of just marking the terminal dirty.
- Fix: track `dirty`/`wantUpdate` in `serveHostRFB`; a non-incremental request always
  gets a frame, an incremental request only gets one immediately if the terminal
  changed since the last frame sent, and otherwise the PTY reader pushes the deferred
  frame itself the next time it marks the terminal dirty. Second review round: `sendFrame`
  now clears `wantUpdate` (under the same mutex as `dirty`), because a deferred
  incremental request later satisfied by a full request otherwise left the flag set and
  the PTY reader pushed one unsolicited full frame on the next output.
- Regression evidence: `go test -race -count=1 ./internal/vmhost -run
  TestHostConsoleIncrementalUpdateOnlyOnChange` drives the real RFB protocol against
  `HostConsoleSession()` (a real `/bin/bash -l` PTY): confirms an incremental request
  with nothing changed gets no reply within 300ms (asserts `os.ErrDeadlineExceeded`),
  then confirms typing a character (which the PTY echoes) causes the deferred frame to
  arrive without a further request. Extended in the second review round with a deferred
  incremental request satisfied by a full request, after which new PTY output must
  produce no unsolicited frame within 500ms; that assertion fails with
  "output pushed an unsolicited frame after a satisfied incremental request" when the
  `wantUpdate = false` line in `sendFrame` is removed, and passes with it. Runs under
  `go test -race ./...` in both phase1 workflows.
- Avoid: treating "send a frame" and "the client asked for one" as the same event when
  the protocol has an explicit incremental flag for exactly this case.

## BUG-0053 — Host console KeyEvent handling dropped Ctrl+C/D/Z and arrow keys

- Date: 2026-09-10.
- Symptom: code review found `keysymByte` had a literal dead branch
  (`if sym >= 0x61 && sym <= 0x7a { return nil }`) that could never execute because the
  `0x20..0x7e` Latin-1 range above it already covered lowercase letters and returned
  first. Ctrl+C/D/Z therefore arrived at the shell as plain `c`/`d`/`z`, and arrow keys
  (not in any handled range) were silently dropped.
- Root cause: the handler never tracked Control key state (it also skipped every
  key-up event outright with `if body[0] == 0 { continue }`, so a held Control_L could
  never be observed), and `keysymByte` had no case for arrow/Home/End keysyms.
- Fix: `serveHostRFB` now tracks a per-connection `keyState.ctrl` set by Control_L/R
  (0xffe3/0xffe4) down/up events; `keysymByte(sym, ctrl)` maps a held-Ctrl keysym in
  0x40..0x5f to `sym & 0x1f` (Ctrl+@ -> 0x00, Ctrl+A..Z -> 0x01..0x1a,
  Ctrl+`[`/`\`/`]`/`^`/`_` -> 0x1b..0x1f), lowercase letters the same way, Ctrl+Space to
  NUL, and adds Left/Up/Right/Down/Home/End escape sequences. The non-letter control
  codes were missed in the first round: Ctrl+\ (SIGQUIT) and Ctrl+] reached the shell as
  literal characters.
  
- Regression evidence: `go test -race -count=1 ./internal/vmhost -run
  TestKeysymByteCtrlAndArrows` (pure-function table test) passes; the table covers
  Ctrl+C/D/Z, the arrow/Home/End sequences, and — added in the second review round —
  Ctrl+`\` (0x1c), Ctrl+`[` (0x1b), Ctrl+@ and Ctrl+Space (0x00), plus plain `\` to show
  the unmodified path is unchanged. Runs under `go test -race ./...` in both phase1
  workflows. Optionally added a Ctrl+C entry to the `Send key…` menu in
  `web/src/console.tsx` (`npm run typecheck` passes) since the backend now honours it.
- Avoid: leaving an unreachable branch in review as "the intended hook" without wiring
  the modifier state that would make it reachable — dead code here was a working sign
  the feature was incomplete, not defensive coverage.

## BUG-0054 — Host console tab expansion emitted roughly half the intended spaces

- Date: 2026-09-10.
- Symptom: a scratch test confirmed `"\t"` from column 0 left `t.cur == 4` (expected 8),
  and `"abc\t"` left `t.cur == 6` (expected 8).
- Root cause: `for i := 0; i < 8-(t.cur%hostCols)%8; i++ { t.put(' ') }` re-evaluated the
  loop bound every iteration; `put()` advances `t.cur`, so the bound shrank as the loop
  ran and the loop stopped roughly halfway to the next tab stop.
- Fix: compute the space count once before the loop in `hostTerm.write` (case `'\t'`).
- Regression evidence: `go test ./internal/vmhost -run
  TestHostTermTabStopsAtNextMultipleOfEight` reproduces both cases from the scratch test
  and passes with the fix; runs under `go test -race ./...` in both phase1 workflows.
- Avoid: writing a `for` loop bound as an expression over mutable state the loop body
  itself advances; compute it once, or iterate to an absolute target instead of a count.

## BUG-0055 — HostConsoleSocket cached a one-time Listen failure for the process lifetime

- Date: 2026-09-10.
- Symptom: code review found `hostConsoleOnce sync.Once` meant a failed `net.Listen` on
  first use (e.g. address already in use, resource limits during startup) would return
  the same cached error forever, with no way to recover without restarting the agent.
- Root cause/fix: moot — BUG-0051's fix removed the listener (and the `sync.Once`/cached
  error with it) entirely in favor of `HostConsoleSession()`, which starts a fresh
  `net.Pipe` and PTY session on every call and has no persistent failure state to cache.
  No separate code change was needed beyond BUG-0051's.
- Regression evidence: covered by BUG-0051's tests (`TestHostConsoleSessionRFBHandshake`
  and `TestHostConsoleHasNoLocalListener` both call `HostConsoleSession()` fresh and pass
  repeatedly).
- Avoid: n/a — recorded for traceability since it was a separate finding in review, not
  because it needed its own fix.

## BUG-0056 — openHostConsole let an old Core hang the browser instead of refusing it

- Date: 2026-09-10.
- Symptom: code review found `openHostConsole` gated on `protocol.ConsoleCapability`
  (`console-v1`), the same capability every agent old enough to support VM consoles
  already reports — including one that predates the host console feature entirely. A new
  Controller would enable the host console tab, issue a ticket, and the browser would
  wait the full 20 seconds for a tunnel the old agent can never open before showing a
  504, instead of an immediate "upgrade Nex Core" refusal. The 409 branch for that
  message existed but was unreachable in this scenario.
- Root cause: host console reused the VM console's capability constant instead of
  advertising its own, so "reports console-v1" was not proof the agent's host console
  RFB endpoint existed.
- Fix: added `protocol.HostConsoleCapability = "host-console-v1"` in
  `internal/protocol/console.go`; `internal/agent/run.go` now reports it alongside
  `console-v1`; `internal/controller/console.go`'s `openHostConsole` gates on it instead
  of `console-v1`, and `hostConsoleBlocker` in `web/src/main.tsx` checks the same
  capability so the host Console tab is disabled (with the "upgrade Nex Core" reason)
  rather than auto-connecting into a 409 the console component retries forever — the UI
  half was missed in the first round and still checked `console-v1`. `docs/API.md` and
  `docs/api/openapi.json` document the new capability and compatibility behavior; the
  host console endpoints' 404/409 response wording and the ticket parameter description
  were also corrected from copy-pasted VM-console text ("VM not found" / "another VM" /
  "Ticket from openConsole") to host wording.
- Regression evidence: extended `TestHostConsoleTicket` in
  `internal/controller/console_test.go` to report only `console-v1` (no
  `host-console-v1`) and assert the 409 "does not support browser consoles" refusal,
  then report both and assert the ticket succeeds. `go test ./internal/controller -run
  TestHostConsoleTicket` passes; runs under `go test -race ./...` in both phase1
  workflows. The `web/src/main.tsx` gate has no unit test (the UI suite has no component
  harness); it was verified by `npm run typecheck` and `npm run build` and by inspection
  against `openHostConsole` — recorded as a coverage limit in
  `docs/changes/2026-09-10-host-console-hardening.json`.
- Avoid: reusing a capability constant across two features just because one grew out of
  the other's code path — if an old deployment can advertise the shared capability
  without supporting the new feature, gate on a capability that is actually proof of the
  new feature.

---

## BUG-0057 — Interrupted VM media insert could reconcile as succeeded with an empty tray

- Date: 2026-09-10.
- Symptom: if the agent died before or during `virsh change-media` for an insert, restarting and
  reconciling the operation reported `succeeded` and mirrored the ISO as inserted (Controller UI
  showed the media attached), while the VM's CD tray was actually still empty.
- Root cause: `apply()` persisted the vm-media journal at stage `changing-media` with
  `MediaPath` empty; `changeMedia` only set the resolved media path in memory
  (`j.MediaPath = path`) and never saved the journal again before calling `virsh change-media`.
  On restart, `mediaObserved` compared the domain's current tray (`got`, empty) against `want`,
  which it also computed as empty for an insert whose journaled `MediaPath` was empty — an empty
  tray therefore satisfied "empty want" and reconciliation reported success regardless of what
  actually happened.
- Fix: root cause was the stage bookkeeping, not the comparison. `apply()` persisted Stage
  `changing-media` before `changeMedia` had resolved anything, so a crash in the window before
  the virsh call (ISO verification, tray attach) looked like an issued change and reconciled as
  a permanent failure or, worse, matched an empty tray. `changeMedia` now writes Stage
  `changing-media` and the resolved `MediaPath` in one journal save immediately before the
  `virsh change-media` call (both insert and eject); `apply()` sets no stage. A pending journal
  without that stage issued nothing, so `reconcile`'s `Stage != "changing-media"` branch simply
  re-applies the request. The empty-path special case in `mediaObserved` is removed: an insert
  reaching observation always has its path.
- Regression evidence: `go test -race -count=1 ./internal/vmhost -run TestManagedMediaChange`.
  The m11 case executes an insert that cannot resolve its ISO (the same window a crash during
  ISO verification falls in), asserts the journal on disk does not claim Stage
  `changing-media`, then re-executes that journal as pending and asserts it re-applies and
  issues `change-media ... --update`. Hand-reverting the stage move (Stage set in `apply()`
  before the path is resolved, without `git`) fails it with
  `nothing was issued, so the journal must not claim a media change was in flight: ... Stage:changing-media`;
  restoring the fix passes.
- Avoid: persisting only in memory before an external side-effecting call (`virsh`) that can be
  interrupted; anything reconciliation depends on to tell "succeeded" from "empty" apart must be
  durable before that call, not just after it returns.

## BUG-0058 — VM media eject zeroed the boot order without renumbering, orphaning boot-order-dependent VMs on re-adoption

- Date: 2026-09-10.
- Symptom: a VM created with the install ISO first in boot order (`media_boot_order=1`,
  disk `boot_order=2`, the shape `vmSpec`/`managedTestVM` use when media is given) that then had
  its media ejected ended up with a disk boot order of 2 and nothing at 1.
  `protocol.ValidateVMSpec` rejects that shape ("boot order must number the boot devices from 1
  without gaps"), so on Core the VM's own managed journal failed its own re-validation in
  `journals()` and vanished from `Applied()`; on the Controller, `adoptManaged` `continue`d past
  it during host-report adoption. A replacement Controller reconnecting to the pool master would
  never re-adopt the VM, even though it was still running on the host.
- Root cause: both `recordVMMedia` (`internal/vmhost/managed.go`) and `mirrorVMMedia`
  (`internal/controller/vmmedia.go`) independently zeroed `MediaID`/`Boot.MediaBootOrder` on
  eject without shifting the remaining disk boot orders down to close the gap, and neither gave
  a re-inserted ISO a boot order.
- Fix: the round-one renumbering made the durable record lie. `virsh change-media` never
  touches `bootindex`: an inserted ISO gets no boot element, and an eject leaves the (now
  empty) CD drive and every disk with the bootindex they had. `protocol.ApplyVMMedia` therefore
  records only `media_id` — insert invents no media boot order, eject clears `media_id` with
  `media_boot_order` and leaves the disks' `boot_order` untouched. So that the gap an eject
  leaves cannot make a re-adopting Controller reject and silently skip a running VM
  (`internal/controller/managed.go` adoption drops any spec failing `ValidateVMSpec`),
  `ValidateVMSpec` now requires boot order values to be unique, positive and at least one, but
  no longer contiguous — libvirt orders devices by bootindex and tolerates gaps. **Decision for
  Matt to confirm:** relaxing the contiguity rule. Nothing promises 1..N numbering to
  operators: `web/src/vm-create.tsx` renumbers its own list to 1..N before submitting, the VM
  details page prints each device's own number, and `docs/guide/create-virtual-machine.md` says
  only "an explicit boot order". The alternative (rewriting the domain's bootindex values in the
  same virsh step) was rejected as far more code for no operator-visible gain.
- Regression evidence: `go test -race -count=1 ./internal/protocol ./internal/vmhost ./internal/controller`.
  New `TestApplyVMMediaKeepsBootOrder` (protocol) asserts eject keeps disk order 2, clears the
  media order, validates the ejected spec, and that a re-insert invents nothing;
  `TestManagedRecordVMMediaKeepsBootOrderOnEject` (vmhost) and
  `TestMirrorVMMediaKeepsBootOrderOnEject` (controller) assert the same for Core's journal and
  the Controller mirror, both still passing `ValidateVMSpec`; `protocol/managed_test.go` covers
  the accepted gap ("boot order gap", "boot order not from one", "boot order after eject").
  Hand-reverting `ApplyVMMedia` to the renumbering version fails all three
  (`eject must not renumber the disk boot order libvirt still holds: ... BootOrder:1`).
- Avoid: mutating a derived, order-dependent field (boot order) independently on two sides
  (Core and Controller) that must never diverge, and mutating it without maintaining the
  invariant (contiguous numbering from 1) the rest of the system assumes holds.

## BUG-0059 — Ejecting an already-empty CD tray failed instead of succeeding, and could attach a tray just to fail ejecting from it

- Date: 2026-09-10.
- Symptom: an eject request on a VM whose tray was already empty (a fresh eject retried after
  the mirror was already cleared, an eject following an out-of-band `virsh` eject on the host, or
  eject on a stopped VM with no CD drive defined at all) failed with "libvirt refused the media
  change; inspect the VM on its host". A stopped VM without a drive at all would first get an
  empty tray persistently attached by `ensureCDROM`, and the eject would then still fail on that
  freshly attached, already-empty tray. There was no way to clear a stale `media_id` mirror this
  way.
- Root cause: `changeMedia` always called `ensureCDROM` (which attaches an empty tray if none
  exists) and then unconditionally ran `virsh change-media ... --eject` for every eject request,
  without checking whether the drive already had no media. Real `virsh` refuses to eject a
  source-less cdrom ("doesn't have media"); `--force` does not bypass that.
- Fix: the short-circuit decided "already empty" from the persistent definition only and only
  looked at `<source file>`, so a running VM whose tray was changed outside Nex, or whose media
  is attached as `<source dev>`, was reported ejected while the guest still held the ISO.
  `cdromSource` now takes the `running` flag and reads the live definition for a running domain
  (the same `dumpManagedDomain` call `ensureCDROM` uses), and treats either `<source file>` or
  `<source dev>` as media present. `mediaObserved` uses the same helper instead of its own
  `dumpxml --inactive` + unmarshal + first-cdrom scan, and one args builder now serves both
  insert and eject (branching only on `--eject` vs `path --update`).
- Regression evidence: `go test -race -count=1 ./internal/vmhost -run 'TestManagedMediaChange|TestManagedMediaEjectReadsLiveDefinition'`.
  New `TestManagedMediaEjectReadsLiveDefinition` uses a fake virsh whose `dumpxml --inactive`
  shows an empty tray while the live `dumpxml` shows `<source dev="/dev/sr0">` on a running VM,
  and asserts the eject issues `change-media <vm> sda --force --live --config --eject`.
  Hand-reverting `cdromSource` to the inactive definition and `<source file>` only fails it:
  the virsh log holds `dumpxml ... --inactive` and no `change-media`.
- Avoid: calling a side-effecting host command unconditionally for an operation that may already
  be satisfied; read the current observed state first when the API allows a cheap read-only check.

## BUG-0060 — A VM media change broke idempotent replay of the original create request

- Date: 2026-09-10.
- Symptom: create a VM with request_id `R`; later insert or eject its media; then replay the
  original create request with the same request_id `R` (the documented idempotent-retry
  behavior) — the replay returned 409 "request_id already used with another payload" instead of
  200 with the original VM.
- Root cause: `mirrorVMMedia` (`internal/controller/vmmedia.go`) rewrote both the `command` and
  the `payload` columns of the vm-create `managed_resources` row after a successful media change.
  `replayResource` (`internal/controller/managed.go`) looks up a request solely by `request_id`
  and treats it as a replay only if the stored `payload` matches the replayed request byte for
  byte; once `mirrorVMMedia` had rewritten `payload` with the new `media_id`/boot order, the
  original create request's payload no longer matched, so the replay was rejected as "another
  payload" instead of recognized as the same request.
- Fix: `mirrorVMMedia` now updates only the `command` column (the live mirror of the VM's current
  state, already read from the `command` column by `poolMACs`, `hostMemory`, `adoptManaged`, and
  `listResources`) and leaves `payload` (the original request body, which only backs idempotent
  replay of that specific request_id) untouched.
- Regression evidence: `internal/controller/vmmedia_test.go`,
  `TestVMCreateReplayStaysIdempotentAfterMediaChange` (create a VM, eject its media, then replay
  the original create request_id and assert 200 with the same VM ID/operation ID). Manually
  reverted `mirrorVMMedia` back to also rewriting `payload` (not via git) and reran: failed with
  `POST /api/v1/vms: got 409 want 200: {"error":"request_id already used with another payload"}`
  — the exact symptom described above. Restored the fix and reran: passes. Runs in
  `go test`/`go test -race ./internal/controller/`, which both phase1 workflows already execute
  on push/PR.
- Avoid: treating a stored request payload (the input a request_id owns for idempotent replay)
  as if it were the same thing as a live mirror of current resource state; when both need to be
  derived from the same event, update only the one that is supposed to change.

## BUG-0061 — Playwright Back locator ambiguous against the new fixed toolbar

- Date: 2026-09-10
- Symptom: `tests/integration/managed_vm_browser.py` failed Playwright strict-mode resolution
  at the Create VM wizard's "back to review the last step" assertion; both phase1 CI
  workflows (`.github/workflows/phase1.yml`, `.gitea/workflows/phase1.yml`) that run this
  script would go red. `docs/changes/2026-09-10-iso-upload-task-row.json` already recorded
  the run failing on this ambiguity.
- Root cause: `web/src/main.tsx`'s new always-visible toolbar added a second button whose
  accessible name is exactly "Back" (browser history navigation, icon `aria-hidden`),
  alongside the Create VM wizard's own step-back "Back" button
  (`web/src/vm-create.tsx`). `page.get_by_role('button', name='Back', exact=True)` on the
  whole page then matched two elements.
- Fix: scoped the wizard's locator to the wizard's own landmark
  (`page.get_by_role('region', name='Create VM', exact=True).get_by_role('button',
  name='Back', exact=True)`) instead of renaming the toolbar button. Renaming the toolbar
  button's accessible name was rejected: `tests/integration/phase1_browser.py` (not in this
  agent's write set) already asserts `toolbar.get_by_role('button', name='Back',
  exact=True)` for that same button, so changing its name would have broken that file
  instead.
- Regression evidence: ran the real simulated Playwright acceptance end-to-end —
  `.cache/phase1-qa-venv/bin/python tests/integration/managed_vm_browser.py --simulated
  --evidence <scratchpad>/managed-vm-browser-run2` — all 5 phases PASS, including the
  six-step wizard's step-back assertion that previously threw in strict mode. Both phase1
  workflows already run this script.
- Avoid: adding an unscoped `page.get_by_role(..., name='Back')` locator anywhere the fixed
  toolbar is present; scope to the relevant landmark/region instead, since "Back" is a
  generic label likely to recur.

## BUG-0062 — Create VM draft restore replaced an explicit empty NIC list with one blank NIC

- Date: 2026-09-10
- Symptom: an operator removes every network interface in the Create VM wizard (the
  protocol allows 0 NICs) and submits. If the submission response is lost (uncertain
  outcome) the draft persists with `nics: []`. Reopening the wizard shows one NIC with an
  empty required Network select, blocking Next; choosing a network there then makes
  `web/src/managed.tsx`'s retry throw because the resubmitted payload no longer matches the
  original stored request.
- Root cause: `web/src/vm-create.tsx`'s draft-restore logic used
  `Array.isArray(r.nics) && r.nics.length ? r.nics : blank.nics`, so a legitimate empty
  array was treated the same as a missing/absent field and replaced by the one-NIC blank
  template.
- Fix: extracted the restore rule into a pure exported function,
  `restoredNICs(nics: unknown): NICRow[]`, that falls back to `blank.nics` only when the
  value is not an array at all (`Array.isArray(nics) ? nics : blank.nics`), preserving an
  explicit `[]`. `VMWizard`'s initial-draft `useState` now calls `restoredNICs(r.nics)`.
- Regression evidence: no unit-test harness existed in `web/` (no vitest/jest). Added
  `web/src/vm-create.test.mjs` as `npm --prefix web run test`, which both phase1 workflows
  (`.github/workflows/phase1.yml`, `.gitea/workflows/phase1.yml`) now run after typecheck:
  it uses the already-installed `esbuild` to transform-and-import `vm-create.tsx` directly
  (stubbing the `react` and `./managed` imports, which the pure functions under test do not
  need) and asserts `restoredNICs([])` stays `[]`, `restoredNICs(undefined)` and
  `restoredNICs('not-an-array')` fall back to the one-NIC template, and a populated array is
  preserved unchanged. Confirmed the check fails against the original
  `Array.isArray(r.nics) && r.nics.length` logic and passes after the fix. Run with:
  `node web/src/vm-create.test.mjs`. `cd web && npm run typecheck && npm run build` both
  pass.
- Avoid: treating "empty array" the same as "field absent" when restoring a persisted
  draft/form whose protocol allows a legitimately empty collection (here, 0 NICs); check
  `Array.isArray` alone, not truthiness/length, to distinguish "never set" from
  "deliberately cleared."

## BUG-0063 — Deploy guide claimed guest root console login stays locked

- Date: 2026-09-10
- Symptom: `docs/guide/deploy-controller-appliance.md` said "SSH password login stays
  disabled and guest root stays locked" in the recovery section, directly contradicted by
  the very next sentence on the same page ("The deploy password also unlocks the appliance
  console as root...") and by the staged `appliance/firstboot.py` behavior.
- Root cause: the guide was not updated when `firstboot.py`'s
  `provision_console_login()` was added/changed to unlock the guest `root` account with the
  deploy/admin password (`chpasswd -e` then `passwd -u`) and add it to the `sudo` group
  where one exists on the image; the "stays locked" clause went stale.
- Fix: read `appliance/firstboot.py` (read-only) to confirm current behavior, then
  corrected the guide paragraph to state that first boot unlocks the guest root console
  account with the deploy password and may grant it `sudo`, and to spell out the security
  implication (knowing the Controller administrator password gives a full root shell at the
  console, not only web access; treat it like host root). SSH password login staying
  disabled was left as-is (unaffected by `firstboot.py`, which never touches sshd config).
- Regression evidence: this is a documentation-accuracy fix, not executable logic; no
  automated regression check applies. Verification: re-read `appliance/firstboot.py` in
  full and confirmed every clause of the corrected paragraph matches its actual behavior
  (useradd/usermod into `sudo` when present, `openssl passwd -6` + `chpasswd -e` +
  `passwd -u` for the deploy password, no sshd changes). `python3 scripts/check_docs.py
  --worktree` passes with the updated page correctly declared in
  `docs/changes/2026-09-10-wizard-draft-back-locator.json` (guide impact: updated).
- Avoid: letting a guide correctness claim ("X stays locked/disabled") drift from a
  security-relevant script it describes; grep the guide for the changed behavior whenever
  `appliance/firstboot.py` (or any onboarding/bootstrap script) changes what it provisions.

## BUG-0064 — Create VM dropdowns still flickered closed: the OS popup was being dismissed

- Date: 2026-09-10.
- Symptom: after BUG-0050, every native `<select>` in Create VM (Bus, Storage, guest OS,
  Placement host and the rest) still opened and closed at once when clicked.
- Root cause: not the Controller. A native `<select>` picker is a separate compositor popup
  window. On the reporting desktop (Hyprland 0.56.2, Brave 152 on Wayland) that popup is
  dismissed as soon as the pointer moves after the click. Reproduced with a plain page that
  contains nothing but a `<select>`; in the real Create VM form under the same browser the
  popup stayed open for 6 s as long as the pointer was still, so no Controller code (React
  re-render, the 10 s inventory reload, focus handling or panel overflow) closes it.
- Fix: `select, ::picker(select) { appearance: base-select }` in `web/src/style.css` and
  `web/src/local.css`, with picker, option, hover and checked styling. Chromium 135+ then
  renders the picker in the page (top layer) where a compositor cannot dismiss it; browsers
  without base-select keep their native picker. Select semantics, keyboard use and
  `onChange` handling are unchanged.
- Regression evidence: `tests/integration/form_select_picker.py` (replaces
  `form_select_overflow.py`) clicks Bus under the Controller stylesheet in headless Chromium
  and requires computed `appearance: base-select`, `:open` after the click, `:open` after a
  controlled re-selection of the options, the picker drawn below the control in a page
  capture, and ArrowUp + Enter choosing SATA. It fails against the previous stylesheet
  (native picker: appearance `auto`). Both phase1 workflows run it. Real-app evidence:
  the Create VM Bus picker in Brave 152 on Hyprland stayed open for 6 s with the pointer
  moving across the desktop, and the picker appears in headless page captures of the built
  bundle. See [QA record](qa/form-select/2026-09-10-base-select.md).
- Avoid: "fixing" a native popup that the compositor closes by changing page CSS around it,
  and reading a single early screenshot as proof that a popup stayed open. Playwright's
  synthetic clicks do not move the real pointer, so a headed run on a Wayland desktop can
  keep a native popup open that a real click never does; use real input or an in-page picker.

## BUG-0065 — One corrupt managed journal file emptied the host's reported pool state

- Date: 2026-09-10
- Symptom: with one unreadable or invalid file in `<agent-state>/managed-vms/`, the host's
  report carried an empty `Inventory.managed`, so a fresh Controller adopted nothing from the
  pool and showed empty Storage, Networking, ISO and VM tabs, with no signal why.
- Root cause: `ManagedEngine.Applied` (`internal/vmhost/managed.go`) returned an empty state on
  any error from `journals()`, and `journals()` aborted on the first bad file. Failing closed is
  right for `Execute` (a mutation must not proceed over an unreadable journal) but wrong for
  read-only reporting, where it silently discarded every other entry.
- Fix: reporting reads through `readable()`, which skips a bad file and names it; `Applied`
  reports every other entry and returns a `managed-journal` check that is `failed` with the
  file names (healthy otherwise). `Execute` still fails closed through the strict `journals()`.
  In the same change, `Applied` reports failed/needs_attention outcomes, re-observes interrupted
  creations, flags VMs missing from libvirt, and the agent reports the appliance deploy journal;
  Controller adoption records every entry it cannot adopt (`docs/changes/2026-09-10-adoption-completeness.json`).
- Regression evidence: `go test ./internal/vmhost -run TestAppliedSurvivesCorruptJournalEntry`.
  Run against the previous strict read (Applied wired back to `journals()`) it failed with
  `corrupt file emptied the applied state: &{Storage:[] ...}`; with the fix it passes, and
  `go test ./...` passes for every package. It runs in CI through `go test -race ./...` in both
  `.github/workflows/phase1.yml` (line 31) and `.gitea/workflows/phase1.yml` (line 31). The
  namespace/no-VM Controller replacement run (`tests/integration/controller_replacement.py`,
  phase1.yml lines 80-90) now also seeds a corrupt journal file and asserts the other entries
  are adopted and the host's `managed-journal` check is `failed` naming `corrupt.json`.
- Avoid: sharing one fail-closed reader between a mutation path and a read-only reporting path.
  Reporting must degrade entry by entry and say what it skipped; never turn one bad record into
  "the host holds nothing".

## BUG-0066 — A failed VM edit reserved its revision, so every retry of that edit was refused

- Date: 2026-09-10
- Symptom: after a VM edit failed on the host (stop-first refusal, name clash, capacity, a
  transient virsh error), every later edit of that VM failed with *resource ID already
  reserved by another operation*. The host still reported the previous revision, so the
  Controller kept sending the same next revision, and Core kept refusing it.
- Root cause: `ManagedEngine.Execute` (`internal/vmhost/managed.go`) reserved every journal
  entry's resource key regardless of status, and a `vm-update` is keyed
  `vm-update:<vm>:<revision>`. The pending entry is saved before `update` runs, so an edit
  that failed inside `update` left a `failed` entry holding a revision it never produced.
  Related: `promote` only re-observed interrupted `vm-create` entries, so a `vm-update` a dead
  agent process left pending at `defining`/`attaching`/`defined` held its key until a re-sent
  operation, which a replaced Controller never sends; and Controller `adoptManaged` never closed
  an adopted `adopt-outcome:<host>:<op>` needs_attention row once the host settled the entry.
- Fix: the reservation loop skips `failed` `vm-update` entries (they changed nothing; `update`'s
  `revision == current + 1` check still serialises retries); `promote` re-observes interrupted
  `vm-update` entries with `observeDefinition` in the state the edit left the guest
  (`attaching` → running, else shut off); `adoptManaged` marks needs_attention adopt-outcome
  rows succeeded when their operation is absent from the host's current outcomes.
- Regression evidence: `go test ./internal/vmhost -run 'TestManagedFailedUpdateDoesNotReserveRevision|TestAppliedPromotesInterruptedEdit'`
  and `go test ./internal/controller -run TestAdoptedOutcomeClosesWhenHostSettlesIt`. Before the
  fix all three failed (`retry did not reach the edit: ... resource ID already reserved by
  another operation`; `interrupted edit not promoted`; `settled outcome still needs
  attention`); with the fix they pass, and `go test -race -count=1 ./...` passes for every
  package. They run in CI through `go test -race ./...` in both `.github/workflows/phase1.yml`
  (line 31) and `.gitea/workflows/phase1.yml` (line 31).
- Avoid: keying a reservation on a value the operation has not yet produced. A reservation
  must be released when the operation provably changed nothing, or it must be keyed on the
  request instead of the outcome.
