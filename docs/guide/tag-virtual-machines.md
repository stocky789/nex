# Tag virtual machines

## Status

Development build 0.4.8 of Nex Controller and Nex Core. Tags are short labels, such as `web` or
`prod`, kept in the VM's definition. The pool master's Nex Core stores them with the VM, so they
survive a Controller replacement and return when a new Controller adopts the pool. Nex Controller
keeps no copy of its own. Tags label and filter VMs. They do not change placement, power,
hardware or anything inside the guest.

Tags are added to an existing VM from its panel; the create wizard does not set them. Navigation
follows the console layout described in
[Navigate the Controller console](navigate-the-controller-console.md).

## Prerequisites

- An administrator session in Nex Controller and a VM created through Controller (see
  [Register resources and create a virtual machine](create-virtual-machine.md)).
- **Nex Core 0.4.8 or later on the pool master and on the VM's host.** An older Core would
  drop tags, so Controller refuses to send them there.
- The VM's host freshly connected, with the same conditions as
  [editing a VM](edit-virtual-machine.md). The VM may be running or stopped: a tag change
  applies to a running VM without a restart.

## Steps

1. Select **VMs** in the rail and select the VM's row to open its panel.
2. Open the **Configuration** tab. The **Tags** card lists the saved tags, or **No tags**.
3. Type a tag in the field (*Add a tag, such as web*) and select **Add**, or press Enter. A tag is
   1 to 32 lowercase letters, digits or hyphens; capitals are lowered and a repeated tag is
   ignored. Add up to 16 tags. The list is kept sorted.
   Tags already used on other VMs in the pool appear below the field as coloured **+ tag** buttons;
   select one to add it. Each tag has its own colour, the same everywhere it is shown.
4. To remove a tag, select the **×** beside it (*Remove tag <tag>*).
5. Select **Save tags**. It is enabled only when the list differs from the saved one. The notice
   reads *Tags for <VM> requested. They show here once the pool master reports them.* Follow the
   `vm-update` task in **Tasks**.
6. To find tagged VMs, use the **VMs** filter sidebar: type a tag in *Search name, IP, tag*, or
   tick one or more values under the **Tags** facet. Set **Group by** to **Tag** to list VMs under a heading per tag (a VM
   with several tags appears under each; untagged VMs come last). The **Tags** column shows each VM's tags
   (hide or show it with **Customize columns**), and the VM panel's **Overview** tab lists them
   under **Tags**.

## Expected result

After the pool master reports the updated definition, the **Tags** card, the **Tags** column, the
**Tags** facet counts and the search all show the new tags. The VM's **Activity** tab records the
update. Editing the VM's hardware with **Edit hardware** keeps its tags.

Because the tags are part of the VM definition on the pool master, deleting the Controller and
adopting the pool master into a new Controller brings them back with the VM, with nothing to
re-enter (see **Replace a failed Controller** in
[Set up Nex Controller](manage-controller.md#troubleshooting)).

## Troubleshooting

- **The editor is disabled with *Requires Nex Core 0.4.8 or later on the pool master.* or
  *… on this VM's host.*:** update Nex Core on that host (see
  [Check and apply software updates](software-updates.md)). Until then the VM shows no tags.
- **The editor is disabled with another reason**, such as
  *Editing needs a freshly connected host with managed-VM support.* or
  *Controller inventory may be out of date. Refresh before changing tags.*: wait for a fresh host
  report, or select **Retry** on the inventory alert, then try again.
- ***A tag is 1 to 32 lowercase letters, digits or hyphens.*:** remove spaces, underscores and
  other punctuation, and keep the tag to 32 characters.
- ***A VM allows at most 16 tags.*:** remove a tag before adding another.
- **Save is refused with *the VM's host or its pool master does not support VM tags; upgrade Nex
  Core*:** one of those hosts no longer reports tag support, for example after a reinstall with an
  older build. Nothing was queued; update Nex Core and save again.
- **Save is refused with *tag "<tag>" must be 1 to 32 lowercase letters, digits or hyphens*:**
  the pool master rejected that tag. Correct it and save again.
- **The response was lost or the Controller disconnected:** select **Save tags** again with the
  same list. The browser reuses the same request, so the change is not applied twice. Check the
  task in **Tasks**.
- **A host shows as stale on an older Controller after its Nex Core was upgraded:** a
  Controller older than 0.4.8 rejects the whole report from a 0.4.8 Nex Core, so it cannot show
  that host or its tags. Nothing is lost on the host. Upgrade the Controller first, then Nex Core
  (see [Check and apply software updates](software-updates.md)).
- **Tags are missing after the pool master moved:** moving pool authority to a host whose Nex
  Core is older than 0.4.8 can drop tags from the new master's record of the VM. The VM's host
  keeps them. Upgrade every host in the pool before changing the master; this is a known limit.
- **The card shows the old tags:** the request is queued but the pool master has not reported the
  new definition yet, or the host is stale. A stale host's VM keeps its last reported tags.

## Verification

Written for the Controller console redesign against the development source (development build
0.4.8); the browser walkthrough is recorded in the
[console redesign checks](../qa/controller-console-redesign/checks.md). Tag validation, refusal by
older Core, replay with the same request and adoption are covered by the Controller and agent
tests listed in the [API reference](../API.md#vm-tags). The Controller-replacement lab check,
where the tags return after the pool master is re-adopted, is an acceptance item of the
[console redesign plan](../plans/2026-09-23-controller-console-redesign.md#acceptance). This page
does not claim that check has run.
