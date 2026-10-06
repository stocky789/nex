// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"strings"
	"testing"
)

func TestSystemDiskReplaceRequestValidation(t *testing.T) {
	valid := SystemDiskReplaceRequest{HostID: "host-1", RequestID: "req-1", Device: ZFSProvisionDevice{
		ByID: "/dev/disk/by-id/ata-NEW", Serial: "NEW1", SizeBytes: 1 << 40, Fingerprint: strings.Repeat("a", 64)}}
	if err := valid.Valid(); err != nil {
		t.Fatal(err)
	}
	confirmed := valid
	confirmed.Device.EraseConfirmation = valid.Device.ByID
	if err := confirmed.Valid(); err != nil {
		t.Fatalf("a typed erase confirmation naming the disk was refused: %v", err)
	}
	for name, mutate := range map[string]func(*SystemDiskReplaceRequest){
		"no host":             func(r *SystemDiskReplaceRequest) { r.HostID = "" },
		"path host":           func(r *SystemDiskReplaceRequest) { r.HostID = "../host" },
		"no request":          func(r *SystemDiskReplaceRequest) { r.RequestID = "" },
		"path request":        func(r *SystemDiskReplaceRequest) { r.RequestID = "../../../../etc/cron.d/nex" },
		"slash request":       func(r *SystemDiskReplaceRequest) { r.RequestID = "req/../evil" },
		"not a by-id path":    func(r *SystemDiskReplaceRequest) { r.Device.ByID = "/dev/sdb" },
		"no serial":           func(r *SystemDiskReplaceRequest) { r.Device.Serial = "" },
		"no size":             func(r *SystemDiskReplaceRequest) { r.Device.SizeBytes = 0 },
		"no fingerprint":      func(r *SystemDiskReplaceRequest) { r.Device.Fingerprint = "" },
		"short fingerprint":   func(r *SystemDiskReplaceRequest) { r.Device.Fingerprint = "abc" },
		"wrong confirmation":  func(r *SystemDiskReplaceRequest) { r.Device.EraseConfirmation = "/dev/disk/by-id/other" },
		"newline in the path": func(r *SystemDiskReplaceRequest) { r.Device.ByID = "/dev/disk/by-id/ata-NEW\n" },
	} {
		r := valid
		mutate(&r)
		if err := r.Valid(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}
