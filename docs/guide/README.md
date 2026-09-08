# Nex user guide

This living guide covers implemented operator workflows: **Nex Core 0.1.0-alpha1**, a
standalone Debian-derived host, and the separate **Phase 1 Controller/agent development
build**. Both are for local lab evaluation; the installer does not yet deliver agent enrollment.

1. [Install Nex Core in a disposable VM](install-nex-core.md).
2. [Verify the installed Nex Core host](verify-nex-core.md).
3. [Set up Nex Controller and enroll a host](manage-controller.md).
4. [Deploy the Controller appliance from a Core host](deploy-controller-appliance.md)
   (separate development package).
5. [Secure Controller and host web access](secure-web-access.md)
   (security development changes and verification limits).

Each procedure states its prerequisites, expected outcome, troubleshooting, and verification
limits. A documented procedure is not automatically a tested procedure: check its Verification
section for the exact evidence and outstanding checks.

Controller's development build includes its web GUI, administrator login, sites/pools,
enrollment, observed host health and inventory, and read-only inventory refresh tasks.
The separate appliance slice adds local deployment and restricted VM power recovery.
General VM management, consoles, managed snapshots/backups/restores, migrations, HA, and Docker
Container Services remain **planned and unavailable**. Their intended behavior belongs in the
[roadmap](../plans/2026-09-08-nex-platform.md).

The [API reference](../API.md) tracks implemented Nex interfaces separately. Build developers
should use the [ISO build instructions](../../installer/README.md) or
[Controller development instructions](../development.md).

These are ordinary Markdown pages with relative links and stable filenames, ready for a future
documentation website. Keep this index current when adding or retiring a procedure; preserve
versioned release copies when publishing release documentation.
