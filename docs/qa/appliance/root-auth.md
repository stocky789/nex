# Host-local root authentication — 2026-09-08

## Scope and artifacts

Host-local UI authenticates native root through Linux-PAM (installer-set password); generated local
admin credentials removed on initialization, no fallback; existing sessions expire normally. The
appliance admin account is unchanged.

| Artifact (Debian 13, Go 1.27.1) | SHA256 |
| --- | --- |
| `nex-core-local_0.1.0+rootauth1_amd64.deb` | `6b854abb6bff7aca839cf6fe22411b952d7bab972238a98f00f5c3932542d699` |
| Matching source (includes PAM helper and policy) | `c23b0985d0979ee6c931aecc600c230f799b87ff484285be19b7447481e6bb09` |
| `dist/nex-core-0.1.0-alpha1-amd64.iso` | `6c72f6d00d5b2c5d11ee805400cac888d6cdafdceda503d4c43a032e10383c67` |

Unchanged signed `appliance-security1` bundle verified by the package builder.

## Executed checks

| Check | Result |
| --- | --- |
| `go test ./...`; focused localcore race | Passed |
| `python3 -m unittest discover -s tests -p 'test_*.py'` | 57 passed |
| `python3 scripts/generate_api.py --check` | Current |
| Web typecheck and build (package builder) | Passed |
| Browser, Go service + native Debian 13 PAM in disposable Docker (VM commands fixtures) | Passed: readonly root username, installer-password instruction, wrong password, login, secure cookies/policy, preflight blockers, inventory errors, restart session loss, logout, throttling. [Results](root-auth/results.json) (binary/UI hashes), `root-auth/root-login.png` (not committed), `root-auth/invalid-login.png` (not committed), `root-auth/session-lost-login.png` (not committed); desktop/360 px inspected by implementation and review |
| Installed package smoke (below) | Exit zero |
| ISO | Checksum passed; manifest records `nex-core-local 0.1.0+rootauth1`; extracted package matches tested digest; BIOS/UEFI hybrid metadata; installer source/syntax checks passed |
| Independent review | No blocking issues; ran Go, package, API, source checks; inspected captures |

```sh
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py \
  --blocked-only --binary .cache/nex-root-pam-browser \
  --captures .cache/root-pam-browser --docker /tmp/nex-rootauth-docker
```

## Installed package walkthrough

```sh
bash tests/integration/core-local-package.sh \
  --deb dist/core-local/nex-core-local_0.1.0+rootauth1_amd64.deb \
  --docker /tmp/nex-rootauth-docker
```

Fresh Debian 13 container: root login/session, wrong password and old admin rejected, rotation (old
fails/new succeeds), locked root, expired account, expired password and recovery; TLS IP
verification, source/API routes, preflight. Seeded legacy credential files removed; no passwords in
logs or banners. Removal kept configuration and a storage sentinel. No host authentication files
mounted.

## Limits

Native root auth tested only in disposable containers; no workstation or host account changed.
Fresh ISO install root login, physical hardware and nested-KVM deploy/reboot not established.
Package account-policy smoke is local only (CI runs backend/unit/blocked-browser).
