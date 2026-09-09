# Package repository and in-product updates

Date: 2026-09-09  
Status: Lab APT is live on the website listener at `http://172.16.4.3:8080/apt/`
(`nex-controller` and `nex-core-local` `0.1.0+upd1`). Signing key under
`/etc/nex-apt/gnupg`; `matt` can republish with passwordless
`sudo nex-apt-publish`.

## Decision

Matt authorized a Nex APT repository on the existing website VM (`172.16.4.3`) and
in-GUI update checks/apply for Nex Core (host-local UI) and Nex Controller, so
development builds can be upgraded without full reinstall.

This is an early development update path, not Phase 8 populated-host upgrade
acceptance (drain, compatibility windows, rollback proof).

## Scope

1. Signed (when a signing key exists) APT tree under `/srv/nex-apt`, served at
   `http://172.16.4.3:8080/apt/` beside the static website.
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
