// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestManagementNetworkRequestValid(t *testing.T) {
	nic := ManagementNIC{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}
	stage := ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "stage", Spec: ManagementNetworkSpec{Interfaces: []ManagementNIC{nic}}, WindowSeconds: 600}
	if err := stage.Valid(); err != nil {
		t.Fatalf("a well-formed stage was refused: %v", err)
	}
	// Staging may repeat its own id, which is the change it creates, and nothing else.
	stage.ChangeID = stage.OperationID
	if err := stage.Valid(); err != nil {
		t.Fatalf("a stage naming its own change was refused: %v", err)
	}
	apply := ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: "apply", ChangeID: "staged-2", Confirmation: "core1.example.test"}
	if err := apply.Valid(); err != nil {
		t.Fatalf("a well-formed apply was refused: %v", err)
	}
	for _, action := range []string{"confirm", "cancel"} {
		if err := (ManagementNetworkRequest{OperationID: "op-1", HostID: "host-1", Action: action, ChangeID: "staged-2"}).Valid(); err != nil {
			t.Fatalf("%s was refused: %v", action, err)
		}
	}
	for name, bad := range map[string]ManagementNetworkRequest{
		"no operation":      {HostID: "host-1", Action: "confirm", ChangeID: "staged-2"},
		"long operation":    {OperationID: strings.Repeat("o", 129), HostID: "host-1", Action: "confirm", ChangeID: "staged-2"},
		"no host":           {OperationID: "op-1", Action: "confirm", ChangeID: "staged-2"},
		"unknown action":    {OperationID: "op-1", HostID: "host-1", Action: "restart"},
		"empty action":      {OperationID: "op-1", HostID: "host-1"},
		"stage without NIC": {OperationID: "op-1", HostID: "host-1", Action: "stage"},
		"stage with too many NICs": {OperationID: "op-1", HostID: "host-1", Action: "stage",
			Spec: ManagementNetworkSpec{Interfaces: make([]ManagementNIC, 65)}},
		"stage with too much DNS": {OperationID: "op-1", HostID: "host-1", Action: "stage",
			Spec: ManagementNetworkSpec{Interfaces: []ManagementNIC{nic}, DNS: make([]string, 9)}},
		"apply without confirmation": {OperationID: "op-1", HostID: "host-1", Action: "apply", ChangeID: "staged-2"},
		"negative window":            {OperationID: "op-1", HostID: "host-1", Action: "confirm", ChangeID: "staged-2", WindowSeconds: -1},
		"absurd window":              {OperationID: "op-1", HostID: "host-1", Action: "confirm", ChangeID: "staged-2", WindowSeconds: 3601},
		// Every step after staging must name the change it acts on: Core matches it against
		// the staged record, and the command's own id is a different, per-command identity.
		"apply naming no change":   {OperationID: "op-1", HostID: "host-1", Action: "apply", Confirmation: "core1.example.test"},
		"confirm naming no change": {OperationID: "op-1", HostID: "host-1", Action: "confirm"},
		"cancel naming no change":  {OperationID: "op-1", HostID: "host-1", Action: "cancel"},
		"long change":              {OperationID: "op-1", HostID: "host-1", Action: "cancel", ChangeID: strings.Repeat("c", 129)},
		"stage naming another change": {OperationID: "op-1", HostID: "host-1", Action: "stage", ChangeID: "staged-2",
			Spec: ManagementNetworkSpec{Interfaces: []ManagementNIC{nic}}},
	} {
		if err := bad.Valid(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The Controller, the UI and the host all decode these names; freeze them.
func TestManagementNetworkWireShape(t *testing.T) {
	command, err := json.Marshal(Command{OperationID: "op-1", Kind: "management-network", ManagementNetwork: &ManagementNetworkRequest{
		OperationID: "op-1", HostID: "host-1", Action: "stage", ExpectedRevision: 2, WindowSeconds: 600,
		Spec: ManagementNetworkSpec{Interfaces: []ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", VLAN: 10, Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}}, DNS: []string{"192.0.2.53"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"management_network":{"operation_id":"op-1","host_id":"host-1","action":"stage","spec":{"interfaces":[{"name":"nex0","mac":"52:54:00:12:34:56","mode":"static","vlan":10,"address":"192.0.2.10/24","gateway":"192.0.2.1","management":true}],"dns":["192.0.2.53"]},"expected_revision":2,"window_seconds":600},"operation_id":"op-1","kind":"management-network"}`
	if string(command) != want {
		t.Fatalf("command wire shape:\n got %s\nwant %s", command, want)
	}
	// Every step after staging carries two identities: its own operation id and the change it
	// acts on. Freeze both names.
	cancel := mustMarshal(t, ManagementNetworkRequest{OperationID: "op-2", HostID: "host-1", Action: "cancel", ChangeID: "op-1"})
	if !strings.Contains(string(cancel), `"operation_id":"op-2","host_id":"host-1","action":"cancel","change_id":"op-1"`) {
		t.Fatalf("cancel wire shape: %s", cancel)
	}
	observed := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	inventory, err := json.Marshal(Inventory{ManagementNetwork: &ManagementNetworkStatus{Revision: 2, Stage: ManagementNetworkApplied, OperationID: "op-1", Deadline: observed.Add(10 * time.Minute), WindowSeconds: 600, SecondsRemaining: 412, ObservedAt: observed}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory), `"management_network":{"revision":2,"stage":"applied","operation_id":"op-1","deadline":"2026-09-19T10:10:00Z","window_seconds":600,"seconds_remaining":412,"observed_at":"2026-09-19T10:00:00Z"}`) {
		t.Fatalf("inventory wire shape: %s", inventory)
	}
	// A host that reports no status at all must not emit the member: an older Controller
	// rejects unknown non-empty fields, and an absent record is not an empty one.
	if strings.Contains(string(mustMarshal(t, Inventory{})), "management_network") {
		t.Fatal("an absent management network record was serialized")
	}
}

func mustMarshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
