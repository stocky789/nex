// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

// A media change never renumbers boot order, because virsh change-media does not: the record
// must describe the bootindex values the domain still holds, and the gap an eject leaves has
// to stay valid so a re-adopting Controller keeps the VM.
func TestApplyVMMediaKeepsBootOrder(t *testing.T) {
	v := validSpec() // disk boot order 2, media boot order 1
	got := ApplyVMMedia(v, "eject", "")
	if got.MediaID != "" || got.Boot.MediaBootOrder != 0 || got.Disks[0].BootOrder != 2 {
		t.Fatalf("eject: %+v %+v", got.Boot, got.Disks)
	}
	if err := ValidateVMSpec(got); err != nil {
		t.Fatalf("ejected spec must stay adoptable: %v", err)
	}
	back := ApplyVMMedia(got, "insert", "m2")
	if back.MediaID != "m2" || back.Boot.MediaBootOrder != 0 || back.Disks[0].BootOrder != 2 {
		t.Fatalf("insert: %+v %+v", back.Boot, back.Disks)
	}
	v.MediaID, v.Boot.MediaBootOrder, v.Disks[0].BootOrder = "", 0, 1
	if ins := ApplyVMMedia(v, "insert", "m"); ins.Boot.MediaBootOrder != 0 || ins.Disks[0].BootOrder != 1 {
		t.Fatalf("insert into a VM created without media must not invent a boot device: %+v", ins.Boot)
	}
}
