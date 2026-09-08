# Host-local root authentication — 2026-09-08

## Scope and artifacts

The host-local UI now authenticates the native root account through Linux-PAM using
its current password. The installer-set OS password is the same credential used for
new web logins. Generated local admin credentials are removed on package initialization;
there is no fallback. Existing sessions keep their normal expiry until logout/restart.
The Controller appliance's admin account is unchanged.

Built `nex-core-local_0.1.0+rootauth1_amd64.deb` with Debian 13 and Go 1.27.1:
SHA256 `6b854abb6bff7aca839cf6fe22411b952d7bab972238a98f00f5c3932542d699`.
Matching source snapshot SHA256
`c23b0985d0979ee6c931aecc600c230f799b87ff484285be19b7447481e6bb09`.
The unchanged signed `appliance-security1` Controller bundle was verified by the
package builder. Native PAM helper and policy are included in matching source.

## Executed checks

- `go test ./...`: passed all backend packages.
- Focused localcore race checks passed during backend implementation.
- `python3 -m unittest discover -s tests -p 'test_*.py'`: 57 passed,
  including package, API, source allowlist and documentation checks.
- `python3 scripts/generate_api.py --check`: both references current.
- Web TypeScript checking and production build passed in the package builder.
- Actual browser interaction against the Go service and native Debian 13 PAM in a
  disposable Docker container passed: readonly root username, installer-password
  instruction, wrong password, successful login, secure cookies/browser policy,
  preflight blockers, inventory errors, restart session loss, logout and throttling.
  See [results](root-auth/results.json), [login](root-auth/root-login.png),
  [invalid password](root-auth/invalid-login.png), and
  [narrow session recovery](root-auth/session-lost-login.png). Desktop and 360px
  captures were inspected by implementation and independent review.

Browser command (Docker executable can be a sudo wrapper):

```sh
.cache/phase1-qa-venv/bin/python tests/integration/localcore_browser.py \
  --blocked-only --binary .cache/nex-root-pam-browser \
  --captures .cache/root-pam-browser --docker /tmp/nex-rootauth-docker
```

The browser exercises real HTTPS/root PAM, but VM commands are fixtures; no real
VM deployment is claimed for this run. The browser binary/UI hashes are in results.

## Installed package walkthrough

Executed successfully against the artifact above:

```sh
bash tests/integration/core-local-package.sh \
  --deb dist/core-local/nex-core-local_0.1.0+rootauth1_amd64.deb \
  --docker /tmp/nex-rootauth-docker
```

A fresh Debian 13 container installed the actual package and exercised native PAM:
root login/session, wrong password and old admin rejection, password rotation
(old fails/new succeeds), locked root, expired account, expired password and recovery.
TLS IP verification, source/API routes and host preflight passed. Legacy credential
files were seeded before initialization and removed; no passwords appeared in service
logs or banners. Package removal retained configuration and a disposable storage
sentinel. The command exited zero. No host filesystem authentication files were mounted.

## Impact and review

API impact: changed contract — local POST/GET `/api/local/v1/session` authenticate
and report root; JSON shape and session protections remain stable. No Controller
operation or agent command changes. Guide impact: updated — login, password change,
upgrade and recovery instructions match native account behavior.
Independent source/integration review found no blocking issues and separately ran
Go, package, API and source checks. It inspected the browser captures and validated
the API/guide decision. Final package and ISO evidence follows below.

## Limits

No workstation or installed user host account was changed. Native root authentication
is tested in disposable Debian containers. Updated ISO full installation, physical
hardware and nested-KVM deployment/reboot are not established by these checks.
Historical deployment evidence remains tied to its named older artifacts. Hosted CI
was not run; workflows include backend/unit/blocked-browser checks, while the full
package account-policy smoke is currently a local runnable check. External release
source/artwork/compliance gates remain unchanged.

## ISO delivery

Rebuilt `dist/nex-core-0.1.0-alpha1-amd64.iso` with the explicitly selected rootauth1
package. SHA256: `6c72f6d00d5b2c5d11ee805400cac888d6cdafdceda503d4c43a032e10383c67`.
Checksum verification passed. The ISO manifest records `nex-core-local 0.1.0+rootauth1`;
its extracted package matches the tested package digest above and includes the PAM
helper and policy. The builder reports BIOS/UEFI hybrid boot metadata. Installer
source/syntax checks passed. This is artifact verification, not a new installed-VM
walkthrough; root login on a complete fresh ISO installation remains to be exercised.

Documentation structure/link checks and `git diff --check` passed. The documentation
checker used `--all`: it does not establish committed-range coverage or hosted CI.
