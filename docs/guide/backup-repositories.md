# Set up backup repositories

## Status

Development Phase 6 / S4 Controller UI for registering an external NFS backup
destination on the pool master. Core owns the durable repository record; Controller
submits create and test requests and shows capacity and host access Core reports.
Policies and schedules are documented in [Set up backup policies and schedules](backup-policies.md).
Import recovery points: use **Import points** on a repository row, or
[Rebuild the backup catalog](catalog-rebuild.md) after catalog loss.

Controller routes under `/api/v1/pools/{id}/…` relay to Core. Matching Controller and Core
builds are required.

## Prerequisites

- Administrator access to Nex Controller and a scoped pool with a freshly connected
  pool master that reports backup inventory.
- An existing NFSv4 export suitable for independent backups (not the VM's disk
  storage). Core and QEMU accounts on intended hosts must be able to write it.
- Use disposable lab resources for development verification.

## Steps

1. Choose the pool in the top-bar scope menu.
2. Open **Settings** → **Backup repositories** (`#/settings/backups`), or **Overview**
   in Settings where the same section appears.
3. Select **Create**. Enter **Name**, **NFS server**, **Export path**, and optional
   **Directory within export**. Select **Save**.
4. On the repository row, select **Test access**. Follow the task until Core reports
   access or a refusal. Core checks the configured NFS source, mounted path,
   filesystem identity, and repository owner marker. Confirm usable backup and
   restore access with a disposable recovery point before relying on the destination.

## Expected result

The table lists the repository with its NFS target. **Capacity** and **Access** show
Core-reported values, or **Not reported** / **Not checked** when unknown. **Host access**
shows one light per pool host from `member_access`, never inventing access. A fresh
Controller obtains the same registrations from master inventory when
`backup_repositories_reported` is true.

## Troubleshooting

- Create or test fails with an unavailable catalog or master: wait for a fresh master
  report, or confirm enrollment and matching builds.
- Access stays limited or errored: fix NFS export permissions and identity; re-run
  **Test access**. Do not treat registration alone as proof of writable restore access.
- If the NFS mount disappears, stop backup requests and verify the mounted source
  before retrying. A local directory at the same path is not an independent backup.
- Open **Policies** on the repository page, or **Backups** → **Policies**, to create schedules.

## Verification

The [product NFS lab](../qa/2026-09-27-phase6-product-backup-lab.md) exercised the
repository with stopped backup and restore. The later [browser report](../qa/2026-09-27-phase6-backup-browser.md)
records the UI flow and its exact limits. The original S4 guide change did not
include that browser walkthrough.
