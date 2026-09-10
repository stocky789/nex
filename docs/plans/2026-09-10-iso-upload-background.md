# Background ISO upload (activity dock)

## Goal

Operators can dismiss the Upload ISO form while a browser→Controller transfer continues,
track progress (percent + rate) and cancel from Recent tasks / activity dock, and are not
capped at 8 GiB per ISO. Host `iso-import` after Controller accept stays in Task history.

## Scope

- Shell-level upload session (UI must stay open; no resume after refresh)
- Activity dock row: name, %, rate, Cancel → success/fail
- Remove per-ISO 8 GiB cap; capacity = staging free space minus 256 MiB reserve (drop fixed 16 GiB staging ceiling)
- Out of scope: URL pull, host-transfer rate in the dock

## API impact

`changed contract`: `POST /api/v1/media` no longer rejects solely for body > 8 GiB; staging budget is free-disk based. Update OpenAPI/`docs/API.md` and focused media tests.

## Guide impact

`updated`: create-virtual-machine (ISO upload steps / size wording).
