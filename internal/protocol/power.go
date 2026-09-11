// SPDX-License-Identifier: AGPL-3.0-only
package protocol

// PowerCapability is reported by agents that execute central VM power requests.
const PowerCapability = "vm-power-v1"

// PowerQueueCapability promises durable FIFO execution and prerequisite checks on Core.
const PowerQueueCapability = "vm-power-queue-v1"

// VMPowerRequest asks the assigned host to change one VM's execution state. Shutdown and
// reboot are ACPI requests the guest may ignore; force-stop cuts power immediately.
type VMPowerRequest struct {
	VMID             string `json:"vm_id"`
	PoolID           string `json:"pool_id"`
	HostID           string `json:"host_id"`
	Action           string `json:"action"`
	AfterOperationID string `json:"after_operation_id,omitempty"`
}

// ValidPowerAction reports whether a is a supported power action.
func ValidPowerAction(a string) bool {
	switch a {
	case "start", "shutdown", "reboot", "force-stop":
		return true
	}
	return false
}
