// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "errors"

// ImageUpdateCommandKind is the Command.Kind a Controller relays to one image-edition host. The host's own
// update engine runs the operation and keeps its record; the command only asks and the result only says
// whether the host accepted the request.
const ImageUpdateCommandKind = "host-image-update"

// ImageUpdateRequest asks one host's update engine for one action. RequestID is the engine's idempotency
// key: an exact replay returns the original operation, a changed body under the same ID is refused.
type ImageUpdateRequest struct {
	HostID     string `json:"host_id"`
	Action     string `json:"action"` // check, apply, activate, rollback, cancel
	RequestID  string `json:"request_id"`
	Sequence   int    `json:"sequence,omitempty"`    // apply
	Activate   string `json:"activate,omitempty"`    // apply: stage_only (default) or reboot
	RunningVMs string `json:"running_vms,omitempty"` // apply, activate, rollback: refuse (default) or shutdown
}

func (r ImageUpdateRequest) Valid() error {
	if !storageOpaqueID.MatchString(r.HostID) {
		return errors.New("image update requires a host")
	}
	if !storageOpaqueID.MatchString(r.RequestID) {
		return errors.New("image update requires a stable request_id")
	}
	switch r.Action {
	case "check", "apply", "activate", "rollback", "cancel":
	default:
		return errors.New("image update action must be check, apply, activate, rollback or cancel")
	}
	if r.Action != "apply" && (r.Sequence != 0 || r.Activate != "") {
		return errors.New("only apply takes a sequence or an activation mode")
	}
	if r.Action == "check" && r.RunningVMs != "" {
		return errors.New("check takes no running_vms policy")
	}
	if r.Sequence < 0 {
		return errors.New("sequence must not be negative")
	}
	switch r.Activate {
	case "", "stage_only", "reboot":
	default:
		return errors.New("activate must be stage_only or reboot")
	}
	switch r.RunningVMs {
	case "", "refuse", "shutdown":
	default:
		return errors.New("running_vms must be refuse or shutdown")
	}
	return nil
}
