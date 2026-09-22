# ISO packaging efficiency

Date: 2026-09-18. Status: historical 0.3.3 packaging result; hosted CI/publication
were unexecuted for that slice. See [artifact and offline-install evidence](../qa/2026-09-18-iso-packaging-efficiency.md).

## Scope

Project owner authorized reducing ISO packaging overhead without removing working
code, Windows drivers, offline installation, the Controller appliance or recovery
capabilities. Preserve complete matching source and notices. Do not claim an ISO
size target until an actual artifact is measured.

- Keep the intact VirtIO source RPM on the media; validate extracted payloads
  outside the final media tree and retain the required release source bundle.
- Compress the complete Windows driver ISO for transport and restore identical,
  checksum-verified bytes during installation.
- Stage only the current resolved APT dependency closure, excluding unrelated or
  superseded files left in a build cache.
- Deduplicate identical packaged Nex source archives while preserving all source
  access and appliance deployment paths; measure appliance compaction before
  retaining any additional build step.

Outcome: the locally built ISO decreased from 3,536,846,848 bytes (0.3.2) to
2,399,469,568 bytes (0.3.3), a 32.16% reduction. Additional appliance compression
was measured and excluded because it increased the final Debian package size.
All driver content and the requested installation package list are retained.

No live infrastructure changes, publication, Git history changes, guest driver
selection changes or reduced hardware support are authorized by this work.

This plan does not establish the status of the later working `0.3.5` tree.

## Acceptance

Run focused packaging, source-retention and corruption/failure regressions in both
providers' push/PR workflows. Build the complete ISO, inspect its payloads, compare
actual size and verify matching source/manifest bindings. Exercise offline
installation in a newly created disposable VM disk, check the restored driver ISO
and packaged source paths, then boot/reboot. Record actual checks and any limits;
source-only CI does not prove installation or hardware behavior. Independently
review the final implementation and evidence.

## API, state and guide impact

API impact: none. Packaging and installer transport do not change Controller/Core
operations, authentication, host commands, observed results or client APIs. The
installed media and source-serving paths remain compatible. State owner: none;
no new durable product settings or records are introduced. Guide impact: updated
installation/release packaging and troubleshooting details with measured QA.
No product phase or release acceptance gate is closed by this plan.
