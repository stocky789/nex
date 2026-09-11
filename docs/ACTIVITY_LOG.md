# Activity log

## 2026-09-08 — Root-only ISO installation and handoff

### Current state

project owner requested that installation ask only for a root password and confirmation,
without creating an ordinary user. That implementation is complete. At project owner's
request, **all repository changes**, including concurrent documentation work,
were committed and pushed to `origin/main` as
`009a3dd4e7aca93e81418b5a9287184697d34861`.
The working tree was clean after that push. This log is a subsequent handoff update.

The current product remains the standalone Nex Core alpha. Controller and agent
implementation are deferred; do not expand the milestone based on this handoff.
Read [the platform plan](plans/2026-09-08-nex-platform.md) and `AGENTS.md` first.

### What changed

- `installer/preseed.cfg` enables root login and disables ordinary-user creation.
- `installer/root-account.sh` loads Debian's account component using native anna
  initialization, then changes its blank-password fallback into a retry. Debian's
  password-confirmation mismatch retry remains intact. The prompt explains root-only
  console access. The hook rejects an unexpected upstream script rather than silently
  skipping its adjustment.
- Boot-menu labels and installation instructions reflect the new account flow.
- **Root SSH remains disabled.** Use the local console. Debian service accounts,
  including `libvirt-qemu`, remain; root-only means no ordinary installer-created user.
- The pushed batch also includes the living operator guide, documentation assessment
  checker/tests, and GitHub/Gitea workflow definitions. See
  [documentation rules](DOCUMENTATION.md) and [guide navigation](guide/README.md).

### Verification already completed

The [root-only QA report](qa/iso/2026-09-08-root-account.md) is the detailed source
of evidence, including committed screenshots and limitations.

- ISO build and checksum verification passed.
- Actual QEMU keyboard interaction verified the root-password prompt, blank rejection
  and retry, mismatch rejection and retry, and matching confirmation advancing directly
  to partitioning without ordinary-user questions.
- Offline installation passed. The first post-boot assertion incorrectly counted
  `libvirt-qemu` UID 64055 as an ordinary user. The assertion was corrected to Debian's
  ordinary-user range, 1000–60000, in both the test source and injected disposable test
  script. The original installation was retained; no account password was changed.
- Replaying the corrected installed-host checks passed both boots, root password status,
  absence of ordinary users, active services, Nex identity, base-files identity retention,
  nested KVM guest boot, and host marker/VM-definition persistence.
- Actual local root login with the unchanged test password passed; `id` reported UID 0.
- Installer source checks and the bundled Debian account-dialog regression check passed.
  Documentation structure checks, all 13 checker tests, and actual committed-range
  coverage for all 36 changed paths passed.
- Independent implementation/documentation review found no outstanding issues.
  All verification VMs were shut down.

Do not describe the initially failing full smoke invocation as a zero-exit pass:
installation succeeded, then the corrected post-boot checks were replayed separately.
See [BUG-0005 and BUG-0006](BUG_LEDGER.md) before changing this flow again.

### Local artifacts

- ISO: `dist/nex-core-0.1.0-alpha1-amd64.iso`
- SHA256: `05a856f8162096d45eb4ede45463671e3b5aba9ff82471ca0272d6ef97e230b5`
- Build log: `.cache/nex-iso/build-root-account.log`
- Installation and corrected boot evidence: `.cache/nex-iso/qa-root-account-4/`
- Interactive password evidence: `.cache/nex-iso/qa-root-interactive/`

`dist/` and `.cache/` are ignored and were **not pushed to Git**. A different machine
must rebuild the ISO; it will not receive these local artifacts by cloning. Build
dependencies resolve from current Debian repositories, so rebuilding need not produce
the same digest. Preserve the exact tested image and its matching checksum locally.
Test-only credentials are confined to the test harness/artifacts, not the shipping ISO.

### Where the next agent should resume

1. Check the latest Git state before editing; this handoff follows the implementation
   commit above. The completed root-only change does not need to be reimplemented.
2. Verify hosted CI on Gitea for the pushed commits. The unauthenticated request to
   `/api/v1/repos/example-org/Nex/actions/runs?limit=5` returned HTTP 404. That does not
   establish whether Actions is disabled, inaccessible, unsupported at that route, or
   awaiting a runner. **No hosted CI success or branch-protection enforcement was verified.**
3. If a single uninterrupted smoke run is needed, use the command in the QA report with
   a fresh, nonexistent output directory and the corrected test source. Existing runtime
   evidence already covers installation and the corrected post-boot checks separately.
4. Keep the existing acceptance limits explicit: physical hardware, UEFI installation,
   Secure Boot, multi-disk safety, guest networking/data persistence, and the complete
   Phase 0/3 gates remain outstanding. External ISO redistribution still requires the
   [licensing/source-delivery gates](LICENSING.md); no ISO was published externally.

**API impact: none** — this handoff records completed local installer work; it adds no
Controller operation, Core command/result, or client-facing API. **Guide impact: none**
for this log: operator behavior and instructions are unchanged.
