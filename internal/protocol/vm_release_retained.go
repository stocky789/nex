// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "errors"

// VMReleaseRetainedRequest asks a VM's host to delete the disk files and vTPM state that
// restore replace and VM edits kept beside it. Confirmation is the VM's current name.
type VMReleaseRetainedRequest struct {
	VMID         string `json:"vm_id"`
	PoolID       string `json:"pool_id"`
	HostID       string `json:"host_id"`
	Confirmation string `json:"confirmation"`
}

func (r VMReleaseRetainedRequest) Valid() error {
	if r.VMID == "" || r.PoolID == "" || r.HostID == "" || r.Confirmation == "" || len(r.Confirmation) > 256 {
		return errors.New("release retained originals request is invalid")
	}
	return nil
}
