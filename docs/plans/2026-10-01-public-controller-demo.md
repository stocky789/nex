# Public Controller browsing demo

Date: 2026-10-01

## Scope and decision

The project owner requested a containerized, automatically updated public Controller
demo with fictional VM, storage and backup inventory and `demo` / `demo` login.
Use the current Controller React entry and components, not a separately maintained UI.
A dedicated demo executable implements the read-only client API over fictional data;
it is not a Nex Core implementation and never connects to hosts or runs operations.
Management submissions are refused rather than reported as successful simulations.
This is a browsing demo, not proof of VM, backup or recovery behavior.

The demo is separate from the production Controller binary. It introduces no production
Controller flag, pool record, enrollment path or alternate source of real pool state.
Deleting the demo loses no pool data. Its immutable sample inventory is build input,
not operator-owned durable state. API impact: **existing contract** — the real UI reads
a restricted subset of the existing Controller HTTP contracts; production Controller,
Core command and observed-result contracts do not change. The separate hosting health
probe is not a pool capability. Guide impact: **updated** — document public login,
read-only limits, deployment and rollback. State owner: **none** — no durable product
state is added; disposable sessions and fictional fixtures are not real pool records.

## Deployment boundary

- Build the same UI from Git with a demo-only notice and credentials guidance.
- Run the dedicated non-root demo process in a read-only, capability-free container
  with no network interface beyond loopback, no published ports and no Docker socket.
- Expose a Unix socket to a separate non-root host nginx service. It binds a selected
  private HTTP listener; the external domain route must terminate HTTPS.
- Accept only the configured public Host at nginx, bound request sizes, rate-limit
  requests and login attempts, and keep secure, HttpOnly, SameSite session cookies.
- Apply default-deny host ingress; permit the known HTTPS proxy sources to the
  website/demo listeners and known administration/publishing sources to SSH.
  Docker publishes no port, so it cannot bypass the host firewall for the demo.
- Keep account and deployment endpoints in private host configuration or existing
  Gitea deployment secrets, never in tracked files. Use strict SSH host-key checking.
- Build and check on pull requests without deployment credentials. Main pushes and
  manual runs publish via dedicated `NEX_DEMO_DEPLOY_*` secrets, a command-restricted
  SSH key and a publishing account with no broad sudo or Docker access. Existing
  website credentials remain untouched. Git history and pushes remain the project
  owner's responsibility.
- A failed candidate health check restores the prior image. Publication is serialized;
  a brief restart interruption is acceptable, but a broken release is not retained as
  the selected release. Retain matching downloadable corresponding source.

## Acceptance

1. Log in with `demo` / `demo`; reject other credentials without locking out all
   visitors. Verify cookie security, expiry, origin and CSRF boundaries.
2. Browse real Overview, VM/host detail, Storage, Networks, backup jobs/repositories,
   recovery-point details, Tasks, Events and Settings with coherent fictional data.
   Check filters, pool scope, dark/light rendering and phone layout in a browser.
3. Refuse management mutations, enrollment, console streams, uploads and arbitrary
   outbound requests at the server boundary, not just in the UI.
4. Verify actual container runtime restrictions, private listener, firewall and
   key-only SSH; preserve the existing website route.
5. Publish the built artifact, download matching source, restart the deployment,
   exercise failed-candidate rollback and inspect the resulting version/health.
6. Run focused backend checks, UI typecheck/build, repository privacy and documentation
   checks. Independently review the implementation and security boundary.
7. Record actual deployment and browser evidence separately from remaining external
   DNS/HTTPS routing and post-push Gitea execution. Do not claim either unrun gate.
