# Package repository and in-product updates

Date: 2026-09-09  
Status: APT is live at `https://nexvirtualization.com/apt/`
(`nex-controller` `0.1.3`, `nex-core-local` `0.1.4` at the last verified publication).
The next release must publish a version newer than `0.1.5~alpha1`; domain default; private HTTP
repo URLs are remapped to that default. Controller appliances ship `apply-update`
+ path unit so Updates Check/Update work after deploy. Signing key under
`/etc/nex-apt/gnupg`; the configured publishing account can republish with passwordless
`sudo nex-apt-publish`.
Disposable lab verified Check/Apply for both packages against the public repo.
Host backend remains `/srv/nex-apt` on the website VM.

## Decision

project owner authorized a Nex APT repository on the website host and in-GUI update
checks/apply for Nex Core (host-local UI) and Nex Controller, so development
builds can be upgraded without full reinstall.

This is an early development update path, not Phase 8 populated-host upgrade
acceptance (drain, compatibility windows, rollback proof).

## Scope

1. Signed (when a signing key exists) APT tree under `/srv/nex-apt`, served at
   `https://nexvirtualization.com/apt/` (nginx `/apt/` on the website host).
2. Publish tooling for `.deb` artifacts. ~~and machine-readable `updates.json`~~ —
   withdrawn 2026-09-10, see the scope change below.
3. `nex-controller` Debian package matching appliance install paths so existing
   Controller VMs can upgrade in place.
4. Existing `nex-core-local` package remains the Core update unit (still embeds the
   appliance bundle until a later packaging split).
5. Authenticated update APIs and compact Updates UI in both web GUIs.
6. Controller check uses a signed per-service APT cache (service is unprivileged) and apply uses a root systemd path unit;
   Core apply runs as root and schedules a service restart.

## Out of scope

- Public Internet publication / DNS changes
- Full Debian mirror
- Splitting the appliance qcow2 out of `nex-core-local` (follow-on)
- Standalone `nex-core-agent` package (agent remains in-process in core-local)
- Phase 8 maintenance drain / HA-safe rolling upgrades

## Scope change 2026-09-10: apt-native updates and the distribution boundary

project owner asked for updates to run through `apt`/`dist-upgrade` rather than a bespoke
downloader, and for Nex Core to be treated as a distribution rather than a set of
packages installable on any Debian system.

Both update paths now run `apt-get update` against the signed Nex archive. Core then runs
plain `apt-get install nex-core-local`, allowing an appliance with only its initial binary to
receive its first package; Controller's privileged helper runs
`apt-get install --only-upgrade nex-controller`. The
previous path fetched `{repo}/updates.json`, verified a SHA-256 taken from that same
index, and ran `dpkg -i` — which cannot satisfy dependencies, so any release adding one
would have failed mid-install and left the package unconfigured. The signed archive and
its keyring already existed and were unused by the product; apt now provides dependency
resolution and GPG verification from that existing infrastructure.

Consequences accepted:

- Clients no longer read `updates.json`; apt reads the signed `Release`/`Packages`
  index instead. **The publisher still writes `updates.json`**, and must keep doing so
  until no supported host reads it. Every host deployed before this change polls that
  file to discover releases, so freezing it would hide the very release that moves them
  onto apt and strand them on their installed version with no in-product upgrade. The
  live repo currently advertises `nex-core-local` 0.1.4 and `nex-controller` 0.1.3
  through it. Remove the shim only once no host at or below the switch release remains.
- The available version is apt's candidate. Checks refresh the index at most once every
  five minutes rather than reading a live index, because the UI polls the check endpoint
  while an apply runs.
- Progress reports fixed steps rather than download percentage. `APT::Status-Fd` can
  restore a real percentage if that matters later.
- `nex-controller` now ships the archive keyring and writes `nex.sources`; it previously
  shipped neither, so the appliance had no Nex apt source at all.

Separately, `nex-core-local`'s `preinst` refuses a *fresh* install unless
`/etc/nex/core-release` exists, which the Nex Core installer writes before installing
any package. Upgrades and reinstalls are never blocked, so hosts provisioned before this
check keep upgrading and `postinst` backfills the stamp. This is a product boundary, not
a security control: root can write the stamp. It exists because proper dependency
resolution would otherwise have made `apt install nex-core-local` on plain Debian work
*better* than before, producing something that resembles a Nex Core host without being
one.

Still open: no on-host upgrade has been run against the live archive, and no upgrade has
yet exercised a release that genuinely adds a dependency — the case this change exists
to fix. Immutable/image-based host updates (dm-verity, A/B via `systemd-sysupdate`)
were discussed and are **not** in scope here; they would replace this apt path entirely
and need their own plan and decision.

## API impact

**changed contract** (2026-09-10; originally **new contract**) — `GET`/`POST` update
routes on Controller (`/api/v1/updates`) and Core local (`/api/local/v1/updates`).
Paths, methods, auth, status codes and the `UpdateStatus`/`UpdateApplyResult` schemas
are unchanged. Semantics changed: `available_version` is apt's candidate, the
`updates.json` index is withdrawn, unavailable-package error text changed, apply fails
when the installed version did not advance, and `progress` reports fixed steps. Both
sides ship together; no new agent protocol commands. No new pool-scoped setting — the
repo URL stays host-side in `/etc/nex/updates.conf`, mirrored to
`/etc/apt/sources.list.d/nex.sources` on the same host, so a rebuilt Controller
re-adopts nothing new.

## Guide impact

**updated** — document checking/applying updates in Controller and Core local UIs,
and operator publish steps for the APT tree on the website host. From 2026-09-10 also
document that Check/Update *is* apt, the five-minute index refresh bound, the two new
error messages, and that Nex Core is installed from its ISO rather than by installing
`nex-core-local` onto an existing Debian system.
