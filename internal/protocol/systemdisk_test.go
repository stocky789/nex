// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

func TestSystemDiskReplaceRequestValidation(t *testing.T) {
	valid := SystemDiskReplaceRequest{
		OperationID: "op-1", HostID: "host-1", Array: "md0",
		Device:      testZFSProvisionDevice("disk-9"),
	}
	if err := valid.Valid(); err != nil {
		t.Fatalf("a well-formed replacement was refused: %v", err)
	}
	for name, mutate := range map[string]func(*SystemDiskReplaceRequest){
		"no operation":       func(r *SystemDiskReplaceRequest) { r.OperationID = "" },
		"path operation":     func(r *SystemDiskReplaceRequest) { r.OperationID = "../../../../etc/cron.d/nex" },
		"slash operation":    func(r *SystemDiskReplaceRequest) { r.OperationID = "op/../evil" },
		"no host":            func(r *SystemDiskReplaceRequest) { r.HostID = "" },
		"bad array":          func(r *SystemDiskReplaceRequest) { r.Array = "raid0" },
		"no fingerprint":     func(r *SystemDiskReplaceRequest) { r.Device.Fingerprint = "" },
		"wrong confirmation": func(r *SystemDiskReplaceRequest) { r.Device.EraseConfirmation = "/dev/disk/by-id/other" },
	} {
		request := valid
		mutate(&request)
		if request.Valid() == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}
