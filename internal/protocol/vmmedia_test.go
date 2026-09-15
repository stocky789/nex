// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

// A media change never renumbers boot order, because virsh change-media does not: it swaps the
// drive's source and leaves every bootindex alone, the drive's own included. The record must
// describe the values the domain still holds, so only media_id changes.
func TestApplyVMMediaKeepsBootOrder(t *testing.T) {
	v := validSpec() // disk boot order 2, media boot order 1
	got := ApplyVMMedia(v, "eject", "")
	if got.MediaID != "" || got.Boot.MediaBootOrder != 1 || got.Disks[0].BootOrder != 2 {
		t.Fatalf("eject: %+v %+v", got.Boot, got.Disks)
	}
	if err := ValidateVMSpec(got); err != nil {
		t.Fatalf("ejected spec must stay adoptable: %v", err)
	}
	back := ApplyVMMedia(got, "insert", "m2")
	if back.MediaID != "m2" || back.Boot.MediaBootOrder != 1 || back.Disks[0].BootOrder != 2 {
		t.Fatalf("insert: %+v %+v", back.Boot, back.Disks)
	}
	v.MediaID, v.Boot.MediaBootOrder, v.Disks[0].BootOrder = "", 0, 1
	if ins := ApplyVMMedia(v, "insert", "m"); ins.Boot.MediaBootOrder != 0 || ins.Disks[0].BootOrder != 1 {
		t.Fatalf("insert into a VM created without media must not invent a boot device: %+v", ins.Boot)
	}
}

// BUG-0188: an eject is not a boot order change, so a running guest takes it. A pool-shared VM
// applies its media change as a vm-update (BUG-0185), where NeedsStop decides whether the VM
// must be stopped first; recording the empty drive out of the boot order made every eject of a
// running install-media-first VM fail with "stop the VM first to change the boot order".
func TestApplyVMMediaNeedsNoStop(t *testing.T) {
	v := validSpec() // disk boot order 2, media boot order 1
	for _, c := range []struct {
		name, action, media string
	}{{"eject", "eject", ""}, {"swap", "insert", "m2"}} {
		t.Run(c.name, func(t *testing.T) {
			stops, err := NeedsStop(v, ApplyVMMedia(v, c.action, c.media))
			if err != nil || len(stops) != 0 {
				t.Fatalf("media change classified stop-first: %v %v", stops, err)
			}
		})
	}
}
