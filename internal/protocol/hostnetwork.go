// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"fmt"
	"time"
)

// ManagementNetworkCapability gates the staged management-network edit: the command kind, the
// request below and the inventory status.  A Core without it cannot be sent a network change,
// and the Controller says so instead of offering a control the host would refuse.
const ManagementNetworkCapability = "management-network-v1"

// ManagementNetworkConflict is the text every Core conflict refusal ends with (a revision
// mismatch, an action that does not match the recorded stage, or a confirmation the host
// cannot justify).  It is the single source of truth for that wording: Core's sentinel error
// carries it and the Controller maps a command result whose error carries it to HTTP 409,
// rather than matching sentence fragments of its own.
const ManagementNetworkConflict = "management network conflict"

// Stages of a host's management network record, as reported in ManagementNetworkStatus.Stage.
// A record is never deleted, so the last outcome stays readable: "rolled-back" is what the
// host reports (with a failing "management-network" check) after an unconfirmed change was
// automatically restored. "none" is reported when the host has no change record but still
// projects its on-disk configuration as Current (installer or last confirmed layout).
const (
	ManagementNetworkNone       = "none"
	ManagementNetworkStaged     = "staged"
	ManagementNetworkApplied    = "applied"
	ManagementNetworkConfirmed  = "confirmed"
	ManagementNetworkCancelled  = "cancelled"
	ManagementNetworkRolledBack = "rolled-back"
)

// ManagementNIC is one interface of a host's management configuration.  Name is the on-host
// interface name, MAC binds it, and exactly one NIC carries Management.
type ManagementNIC struct {
	Name       string `json:"name"`
	MAC        string `json:"mac"`
	Mode       string `json:"mode"` // dhcp | static | disabled
	VLAN       int    `json:"vlan,omitempty"`
	Address    string `json:"address,omitempty"` // IPv4 CIDR, static only
	Gateway    string `json:"gateway,omitempty"` // management NIC only
	Management bool   `json:"management,omitempty"`
}

// ManagementNetworkSpec is the whole desired management configuration of one host.  Core, not
// the Controller, holds the rules it must satisfy; only bounds are checked on the wire.
type ManagementNetworkSpec struct {
	Interfaces []ManagementNIC `json:"interfaces"`
	DNS        []string        `json:"dns,omitempty"`
}

// ManagementNetworkStatus is the host's own report of its management network: the durable
// record projected into inventory, so a replaced Controller adopts an in-flight change with
// its deadline instead of losing it.
type ManagementNetworkStatus struct {
	Revision    uint64                 `json:"revision"`
	Stage       string                 `json:"stage"`
	OperationID string                 `json:"operation_id,omitempty"`
	Spec        *ManagementNetworkSpec `json:"spec,omitempty"`
	// Current is what the host's Nex-managed /etc files say right now. It is independent of
	// Spec: Spec is the staged, applied or last change record; Current is parsed from disk so
	// a Controller can show the live addressing before any change has been staged. Absent when
	// the files are missing or were not written by Nex.
	Current  *ManagementNetworkSpec `json:"current,omitempty"`
	Deadline time.Time              `json:"deadline,omitempty"`
	// WindowSeconds is the rollback window the operator chose when the change was staged. It
	// is reported because "how long will I have" must survive a reload: the deadline only
	// exists once the change is applied, and the default is not the operator's choice.
	WindowSeconds    int    `json:"window_seconds,omitempty"`
	SecondsRemaining int    `json:"seconds_remaining,omitempty"`
	LastError        string `json:"last_error,omitempty"`
	// RequestedRollback marks a "rolled-back" record the operator asked for: a cancel of an
	// applied, unconfirmed change, which is an early undo rather than a failure. Without it a
	// deliberate undo is indistinguishable from an automatic rollback after a lost host, and
	// the host would report a failing "management-network" check for something nobody has to
	// fix.  It is only meaningful together with stage "rolled-back".
	RequestedRollback bool      `json:"requested_rollback,omitempty"`
	ObservedAt        time.Time `json:"observed_at"`
}

// ManagementNetworkRequest is one step of the staged edit, carried by the durable managed
// command path so it survives a Controller outage.  HostID names the target the way every
// other managed command does; the agent refuses a command addressed to another host.
type ManagementNetworkRequest struct {
	OperationID string `json:"operation_id"`
	HostID      string `json:"host_id"`
	Action      string `json:"action"` // stage | apply | confirm | cancel
	// ChangeID is the staged change this action targets: the OperationID the host recorded
	// when it staged it, which is what Core matches an apply, confirm or cancel against.
	// OperationID above stays unique per command, because it is the journal key that makes a
	// replayed command replay its recorded result instead of acting twice. "stage" creates
	// the change, so there it is the command's own OperationID and is left empty.
	ChangeID string                `json:"change_id,omitempty"`
	Spec     ManagementNetworkSpec `json:"spec,omitempty"`
	// ExpectedRevision is the revision the operator saw; staging against a stale one is a
	// conflict.  WindowSeconds is the rollback window, 0 for the host's default.
	ExpectedRevision uint64 `json:"expected_revision,omitempty"`
	WindowSeconds    int    `json:"window_seconds,omitempty"`
	// Confirmation must equal the host's own hostname for "apply".  The host checks it
	// itself: applying a management network change is the one edit that can strand a host,
	// so the target is named explicitly at the boundary that performs it.
	Confirmation string `json:"confirmation,omitempty"`
}

// Valid bounds the request.  The spec's own rules live in Core, which owns the render.
func (r ManagementNetworkRequest) Valid() error {
	for _, field := range []struct {
		value    string
		required bool
		emptyMsg string
		longMsg  string
	}{
		{r.OperationID, true, "management network request requires an operation ID", "management network operation ID is too long"},
		{r.HostID, true, "management network request requires a host", "management network host ID is too long"},
		{r.ChangeID, false, "", "management network change ID is too long"},
	} {
		if field.required {
			if err := requireNonEmptyBoundedID(field.value, field.emptyMsg); err != nil {
				return err
			}
		} else if err := requireBoundedIDMax(field.value, field.longMsg); err != nil {
			return err
		}
	}
	switch r.Action {
	case "stage":
		// Staging creates the change, so it names no other one: an id here would mean the
		// sender believes it is acting on an existing change.
		if r.ChangeID != "" && r.ChangeID != r.OperationID {
			return errors.New("staging a management network change names no other change")
		}
		if n := len(r.Spec.Interfaces); n == 0 || n > 64 {
			return fmt.Errorf("a management network spec needs 1-64 interfaces, %d given", n)
		}
		if len(r.Spec.DNS) > 8 {
			return errors.New("a management network spec allows at most 8 DNS servers")
		}
	case "apply":
		if r.Confirmation == "" || len(r.Confirmation) > 253 {
			return errors.New("applying a management network change requires the host name as confirmation")
		}
		fallthrough
	case "confirm", "cancel":
		// Core matches these against the staged change's own ID, which is not this command's:
		// each durable command keeps a unique operation ID so a replay replays its result.
		if r.ChangeID == "" {
			return errors.New("this management network action must name the staged change it targets")
		}
	default:
		return errors.New("invalid management network action")
	}
	if r.WindowSeconds < 0 || r.WindowSeconds > 3600 {
		return errors.New("invalid management network rollback window")
	}
	return nil
}
