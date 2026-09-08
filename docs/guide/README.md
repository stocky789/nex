# Nex user guide

This living guide covers implemented operator workflows. Current product: **Nex Core
0.1.0-alpha1**, a standalone Debian-derived host for local lab evaluation.

1. [Install Nex Core in a disposable VM](install-nex-core.md).
2. [Verify the installed Nex Core host](verify-nex-core.md).

Each procedure states its prerequisites, expected outcome, troubleshooting, and verification
limits. A documented procedure is not automatically a tested procedure: check its Verification
section for the exact evidence and outstanding checks.

Nex Controller, its web GUI, enrollment, pools, managed snapshots/backups/restores, migrations,
HA, and Docker Container Services are **planned and unavailable in this alpha**. Their intended
behavior belongs in the [roadmap](../plans/2026-09-08-nex-platform.md), not instructions for
features that do not exist. Add their guide pages as working workflows are implemented.

The [API reference](../API.md) tracks implemented Nex interfaces separately. Build developers
should use the [ISO build instructions](../../installer/README.md).

These are ordinary Markdown pages with relative links and stable filenames, ready for a future
documentation website. Keep this index current when adding or retiring a procedure; preserve
versioned release copies when publishing release documentation.
