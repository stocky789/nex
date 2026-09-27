# Root SSH defaults, source verification

Date: 2026-09-28. Environment: unreleased Nex source worktree on a Debian development
machine. No Nex Core ISO or Controller appliance was rebuilt or booted for this check.

- `bash installer/check.sh` checks the Core late-install SSH policy and installer inputs;
  it does not execute a fresh installation.
- `python3 -m unittest tests.test_netinst_network.NetinstNetwork.test_config_network_gate_and_preflight`
  executes the shared installer validator and checks that a one-character password is
  accepted while an empty password is rejected.
- `go test ./internal/vmhost -run 'TestDeploymentAcceptsShortPasswords|TestSeedInjectsUpdateHelperWhenPresent' -count=1`
  checks that deployment still accepts short passwords and the cloud-init seed enables root
  password SSH; it does not boot a Controller guest.
- `python3 -m unittest tests.test_appliance_build` checks the openssh-server package request
  and sshd policy in the image recipe; it does not build the image or prove sshd starts.
- `npm run typecheck` and `npm run build` in `web/` check the local UI. The browser form
  test still accepts nonempty short passwords; Playwright is unavailable here.
- `python3 scripts/generate_api.py --check`, `python3 scripts/check_docs.py --worktree`,
  and `git diff --check` check the API reference, documentation structure and patch format.

Fresh Core installation, appliance first boot, root password SSH from a management
client, host-key comparison, wrong-password rejection, and reboot persistence remain
unverified. Existing hosts and appliances were not changed.
