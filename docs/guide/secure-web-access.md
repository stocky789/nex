# Secure Controller and host web access

## Status

This page covers the **2026-09-08 web-security development changes** for Nex Controller
and the Nex Core host-local interface. The changes harden existing authentication,
sessions and browser responses. The current host package is `0.1.0+usability1`; its root-login checks are recorded
in Verification. Earlier `0.1.0+security1` nested-KVM results apply to that artifact.
Earlier appliance results remain separate evidence for their named artifacts. Current
host-local builds now use the OS **root** password; the older security package used a
separate local account. Install matching updated service, web assets and authentication
components before following the root-login instructions.

Use a private management network or an established VPN. Direct Internet exposure is not
supported by this development slice. The assessment uses
[OWASP ASVS 5.0](https://owasp.org/www-project-application-security-verification-standard/)
and [NIST authentication guidance](https://pages.nist.gov/800-63-4/sp800-63b.html)
as references; Nex does not claim compliance with every requirement, an assurance level,
regulatory certification or unrestricted production readiness.

## Prerequisites

- Matching service binaries, web assets and source archive for the security development build.
  Preserve the existing private state and recoverable identity before replacing a running build.
- An authenticated management-network/VPN path and independent root console access to the host.
  A VPN does not replace the Nex administrator login or certificate verification.
- A trusted Controller certificate or a host/appliance certificate fingerprint obtained from
  its trusted console or authenticated deployment page. Compare it before accepting a
  self-signed certificate warning; do not disable TLS checks.
- For host-local access, the current OS **root** password, initially set during installation.
  No separate local web password is created.
- For Controller access, its **root** password. Controller passwords have no
  minimum length or complexity rule; use a nonempty value of at most 1024 UTF-8 bytes without line breaks or NUL. Existing Controller
  password hashes remain valid.

## Steps

1. Reach the appropriate HTTPS address through the private management network or VPN.
   Use the [Controller setup](manage-controller.md) or
   [host-local appliance procedure](deploy-controller-appliance.md) to establish certificate
   trust. Sign in to a Core host as **root** with its OS password, and to Controller as
   **root** with the Controller password.

2. After installing the security development build, sign in again in each interface.
   Old browser sessions are not reused: the protected session-cookie names changed.
   Controller and host-local access have separate sessions. Signing out of one does not
   sign out of the other.

3. Keep a password-manager copy of each credential, and paste it into the appropriate
   password field when needed. New Controller and replacement-appliance credentials follow
   the input limits above. The host uses its existing OS root password. To change a known
   root password, run `passwd` at its trusted root console; new web logins use the changed
   password. Existing web sessions remain valid until logout, expiry or service restart.
   Editing a Controller bootstrap password file does not rotate an existing Controller account.

4. Expect sign-in to expire after **30 minutes without an authenticated state-changing
   request**, or **eight hours after login**, whichever comes first. Background inventory
   polling, page refreshes and other read-only requests do not renew the idle deadline.
   A tab left open for monitoring will therefore require another sign-in. A submitted
   deployment continues on the host when the browser session expires; inspect its saved
   progress after signing in again before retrying.

5. Use **Sign out** when finished in each interface. Closing the tab alone does not revoke
   its session. Keep local recovery credentials accessible if Controller is unavailable.

## Expected result

Both interfaces require authenticated HTTPS access and reject expired sessions. Browser
cookies are host-scoped, Secure, HttpOnly and SameSite Strict; normal browser use needs
no cookie editing. The updated services use `__Host-nex_session` for Controller and
`__Host-nex_local_session` for local management. These names explain the upgrade sign-in
requirement; they are not credentials to copy between hosts.

Each service allows at most **64 active sessions**. Sign-in admits at most **five password
checks per network peer in a 60-second window**, including successful attempts. Separate
browser tabs and users behind the same NAT or reverse proxy can share that limit. The
service uses the actual connection peer, not a browser-supplied forwarding header.
Additional bounded server-wide admission can reject a login while password checks are busy.

## Troubleshooting

- **Session expired while the page was visible:** sign in again. Read-only monitoring does
  not count as activity, and no activity extends the eight-hour absolute deadline. Check
  saved operations before resubmitting; a browser timeout does not cancel accepted work.
- **Rate limited or busy / HTTP 429:** stop repeated submissions, wait up to a minute for
  the peer window to clear, then retry once. Other users behind the same NAT or proxy may
  share the limit. A busy-service refusal can clear sooner. No `Retry-After` countdown is
  provided. Do not switch identities or weaken validation to bypass admission controls.
- **Session limit reached / HTTP 429:** sign out of sessions you no longer need, or let
  their idle/absolute deadlines expire, then sign in again. This is a per-service limit.
- **An upgraded browser is sent back to login:** expected for old session cookies; use the
  current root password for local access or the existing Controller root credential.
  The former local admin credential is no longer accepted. Controller state is preserved.
- **Certificate warning, changed identity or expired certificate:** verify the address,
  certificate and host clock through independent access. Follow the selected deployment's
  trust setup; do not accept an unexplained fingerprint change. Managed certificate renewal
  is not implemented, and blanket browser HSTS is not enabled for this development workflow.
- **Rollback needed after upgrading:** do not replace only the executable with an older
  build. The Controller session-table migration is incompatible with the older writer.
  Rollback requires the matching pre-upgrade state/identity snapshot and its matching build,
  with the upgraded Controller stopped and excluded before the original identity resumes.
  Preserve newer state separately; follow the applicable quiesced recovery procedure.
- **Local password rejected:** locked or expired root accounts/passwords are refused.
  Resolve OS account status through trusted console recovery; authentication failures never
  fall back to the former local admin account.
- **Forgotten password or production access requirements:** use OS console recovery for
  host root access. This slice has no Controller password-reset/rotation workflow, MFA,
  finer-grained user roles, or screening against
  common/compromised passwords. Length checks and password hashing alone do not meet all
  NIST requirements; no breach-check service is implemented. Do not delete a working
  Controller database to reset access. Complete security-event retention, certificate lifecycle,
  operating-system patch/release processes and production penetration testing remain open.

## Verification

[Deployment usability checks](../qa/appliance/deployment-usability.md) record the current
DHCP/password/power UI changes, simulated browser checks and artifact availability limits.

[Root-login verification](../qa/appliance/root-auth.md) covers current root PAM login,
password changes, rejected/expired/locked account recovery and browser checks.

The [security check report](../qa/security/checks.md) records the reproduced login-admission
failure, regression/race/API/password checks, independent review and time-specific advisory
results. [Security package evidence](../qa/security/package.md) distinguishes the older
compiler findings from the rebuilt package's successful Debian-container smoke and
exact-binary advisory scan. The separately extracted Controller binary also passed its
[compiler/advisory checks](../qa/security/compiler.md). The [browser walkthrough](../qa/security/browser.md)
passed real HTTPS login/logout, cookie and browser-policy checks, throttling, password validation,
Controller expiry recovery and host-local session-loss recovery. Virtualization was simulated;
Controller idle expiry was accelerated by changing only a disposable session deadline. Local
idle expiry remains a focused Go check, not a thirty-minute browser walkthrough.

The security package also passed [real nested-KVM deployment and recovery-of-access checks](../qa/security/lab.md),
including first workspace enrollment, same-VM refresh, secret cleanup, Controller/local-service
outages and guest shutdown/cold start. [Actual cookie, policy and throttle responses](../qa/security/lab/security.json)
were recorded on both services. An actual outer-host reboot also passed native local-service
startup, same-appliance autostart, original-password login, preserved TLS/source/management
identity and fresh enrolled-agent reporting. This does not repeat the older artifact's full
replacement restore or establish physical-LAN/hardware behavior. These results do not
establish production-security acceptance. Historical
[appliance verification](../qa/appliance/lab.md) remains evidence for its named older artifacts,
not for the new session and password policies. The current Core ISO recipe includes the local package; a full updated-ISO installation
walkthrough remains separate from these container checks.
