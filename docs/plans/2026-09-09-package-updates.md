# Package repository and in-product updates

Date: 2026-09-09  
Status: APT is live at `https://nexvirtualization.com/apt/`
(`nex-controller` `0.1.3`, `nex-core-local` `0.1.3`). Domain default; private HTTP
repo URLs are remapped to that default. Controller appliances ship `apply-update`
+ path unit so Updates Check/Update work after deploy. Signing key under
`/etc/nex-apt/gnupg`; `matt` can republish with passwordless `sudo nex-apt-publish`.
Disposable lab verified Check/Apply for both packages against the public repo.
Host backend remains `/srv/nex-apt` on the website VM.

## Decision

Matt authorized a Nex APT repository on the website host and in-GUI update
checks/apply for Nex Core (host-local UI) and Nex Controller, so development
builds can be upgraded without full reinstall.

This is an early development update path, not Phase 8 populated-host upgrade
acceptance (drain, compatibility windows, rollback proof).

## Scope

1. Signed (when a signing key exists) APT tree under `/srv/nex-apt`, served at
   `https://nexvirtualization.com/apt/` (nginx `/apt/` on the website host).
2. Publish tooling for `.deb` artifacts and machine-readable `updates.json`.
3. `nex-controller` Debian package matching appliance install paths so existing
   Controller VMs can `dpkg -i` upgrades.
4. Existing `nex-core-local` package remains the Core update unit (still embeds the
   appliance bundle until a later packaging split).
5. Authenticated update APIs and compact Updates UI in both web GUIs.
6. Controller apply uses a root systemd path unit (service is unprivileged);
   Core apply runs as root and schedules a service restart.

## Out of scope

- Public Internet publication / DNS changes
- Full Debian mirror
- Splitting the appliance qcow2 out of `nex-core-local` (follow-on)
- Standalone `nex-core-agent` package (agent remains in-process in core-local)
- Phase 8 maintenance drain / HA-safe rolling upgrades

## API impact

**new contract** — `GET`/`POST` update routes on Controller (`/api/v1/updates`) and
Core local (`/api/local/v1/updates`). Clients fetch `updates.json` from the APT
HTTP repo; no new agent protocol commands.

## Guide impact

**updated** — document checking/applying updates in Controller and Core local UIs,
and operator publish steps for the APT tree on the website host.
