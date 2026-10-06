# Roll back a host update

## Status

Development build, **unverified**. A Nex Core host keeps the release it ran before an update (its previous slot and
boot entry) until the next update is staged. **Roll back** from the **Host images** panel under **Settings** → **Updates** asks
the host to boot that previous release again. The host also does this by itself when a new release fails its health check after
the restart. This page covers the operator's roll-back. Rolling a whole pool, and rolling back one finished host of a pool rollout,
is described in [update a pool](update-a-pool.md) (also unverified). No part of this page has been walked through on a running Controller or host: see
[Verification](#verification).

## Prerequisites

- A host that is freshly connected, with a Controller administrator session. The host's panel must show a
  **Previous** release and the phase **Committed** or **Booted**; otherwise **Roll back** is not offered.
- Running guests: the host refuses to reboot while any guest runs unless you accept their graceful shutdown.
- The target must be able to read the host's stored data. The host refuses a roll-back to a release whose state format is
  older than its stored VM or ZFS records need (`state_format_downgrade`), or whose kernel cannot assemble the data mirror
  (`md_generation_downgrade`).

## Steps

1. Open **Settings** → **Updates**, and find the host under **Host images**. Check that **Previous** names the release you
   want.
2. Select **Roll back**. Read the confirmation; it reboots the host now. Tick *Shut down running guests gracefully first* only
   if you accept their downtime.
3. Select **Roll back** in the confirmation. The task is accepted at once; the host arms the previous release and restarts.
4. Wait for the host to reconnect. The panel shows the previous release as **Running** and the release you left as **Previous**.

## Expected result

The host runs the previous release, with its settings, guests' disks and identity unchanged (they live on the separate data
volume, not in the release). The release you left stays on its slot as **Previous**, so you can roll forward again with
**Roll back** until a new update is staged and overwrites it.

## Troubleshooting

- **Roll back is greyed out:** the host reports no **Previous** release, a running operation, or a phase other than
  **Committed**/**Booted**. The tooltip says which. A host that has already returned to its old release by itself
  (**Rolled back**, failure `boot_failed`) is running that release already.
- **The task fails with `nothing_to_roll_back`, `update_in_progress`, `running_vms` or `state_format_downgrade`:** these are the
  host's own refusals, shown as the task error. Fix the cause (stop the guests, wait for the running operation) and select
  **Roll back** again.
- **The host does not return:** the Controller does not mark it failed or retry. The armed boot entry has three tries; wait
  for the host's next report before acting, and recover from the host's console if it stays away.

## Verification

Not walked through. The roll-back request path (Controller route, command relay, the host's engine accepting it) is covered
by automated tests only ([Phase I-3c results](../qa/image/phase3-controller.md)); the engine's roll-back, including the retained
entry being re-armed and the previous release booting again, ran once in a virtual machine through the host's command-line tool
([Phase I-3b results](../qa/image/phase3-boot.md)), not through the Controller and not on hardware.
