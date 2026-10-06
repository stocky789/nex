// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "errors"

// ImageUpdateCommandKind is the Command.Kind a Controller relays to one host. The host's own
// update engine runs the operation and keeps its record; the command only asks and the result only says
// whether the host accepted the request.
const ImageUpdateCommandKind = "host-image-update"

// ImageUpdateRequest asks one host's update engine for one action. RequestID is the engine's idempotency
// key: an exact replay returns the original operation, a changed body under the same ID is refused.
type ImageUpdateRequest struct {
	HostID     string `json:"host_id"`
	Action     string `json:"action"` // check, apply, activate, rollback, cancel, settings, media, import, fetch
	RequestID  string `json:"request_id"`
	Sequence   int    `json:"sequence,omitempty"`    // apply, import
	Activate   string `json:"activate,omitempty"`    // apply, import: stage_only (default) or reboot
	RunningVMs string `json:"running_vms,omitempty"` // apply, import, activate, rollback, cancel: refuse (default) or shutdown
	Channel    string `json:"channel,omitempty"`     // settings (required); import and fetch: the bundle's channel
	BaseURL    string `json:"base_url,omitempty"`    // settings: the update server; empty clears it
	Media      string `json:"media,omitempty"`       // import (required); fetch: read the payload from this bundle
	Component  string `json:"component,omitempty"`   // fetch (required): the on_demand component id
}

// Valid checks only the shape. The engine owns every other judgment (URL rules, channel names, media ids)
// and answers with its own stable code.
func (r ImageUpdateRequest) Valid() error {
	if !storageOpaqueID.MatchString(r.HostID) {
		return errors.New("image update requires a host")
	}
	if !storageOpaqueID.MatchString(r.RequestID) {
		return errors.New("image update requires a stable request_id")
	}
	switch r.Action {
	case "check", "apply", "activate", "rollback", "cancel", "settings", "media", "import", "fetch":
	default:
		return errors.New("image update action must be check, apply, activate, rollback, cancel, settings, media, import or fetch")
	}
	staging := r.Action == "apply" || r.Action == "import"
	if !staging && (r.Sequence != 0 || r.Activate != "") {
		return errors.New("only apply and import take a sequence or an activation mode")
	}
	switch r.Action {
	case "check", "settings", "media", "fetch":
		if r.RunningVMs != "" {
			return errors.New(r.Action + " takes no running_vms policy")
		}
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
	if (r.Channel != "" && r.Action != "settings" && r.Action != "import" && r.Action != "fetch") || (r.Action == "settings" && r.Channel == "") {
		return errors.New("channel is for settings, import and fetch only; settings requires it")
	}
	if len(r.Channel) > 32 || len(r.BaseURL) > 2048 || len(r.Media) > 80 || len(r.Component) > 64 {
		return errors.New("a channel, base_url, media or component value is too long")
	}
	if (r.BaseURL != "" && r.Action != "settings") || (r.Media != "" && r.Action != "import" && r.Action != "fetch") ||
		(r.Component != "" && r.Action != "fetch") {
		return errors.New("base_url is for settings, media for import and fetch, component for fetch")
	}
	if (r.Action == "import" && r.Media == "") || (r.Action == "fetch" && r.Component == "") {
		return errors.New("import needs a media id and fetch needs a component")
	}
	return nil
}
