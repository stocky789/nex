// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"os"
	"testing"
)

const (
	bootBeforeUpdate = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	bootAfterUpdate  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
)

func updateRestartFixture(t *testing.T, recordedBoot string) (s *Store, recorded, other string) {
	t.Helper()
	s = peerStore(t, "host-a", "pool-1")
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", false))
	// Neither guest asks for autostart: only the update's record may bring one back.
	local, shared := autostartFixture(t, s, false, false)
	autostartVirsh(t, local.ID, shared.ID)
	if err := saveUpdateRestart(s, updateRestartRecord{BootID: recordedBoot, VMIDs: []string{local.ID}}); err != nil {
		t.Fatal(err)
	}
	return s, local.ID, shared.ID
}

// A guest that was running before the update returns on the next boot even without
// Autostart, and the record is spent so a later boot does not repeat it.
func TestBootPassRestartsGuestRecordedByUpdate(t *testing.T) {
	s, recorded, other := updateRestartFixture(t, bootBeforeUpdate)
	setPoolVMAutostartBootID(t, bootAfterUpdate)
	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason != "" {
		t.Fatalf("the pass did not run: %q", reason)
	}
	if !autostartAttempted(t, s, recorded, bootAfterUpdate) {
		t.Fatal("a guest running before the update was not brought back")
	}
	if autostartAttempted(t, s, other, bootAfterUpdate) {
		t.Fatal("a guest that was not running was started by the update record")
	}
	if _, err := os.Stat(updateRestartPath(s)); !os.IsNotExist(err) {
		t.Fatalf("the spent record remains: %v", err)
	}
}

// Before the reboot has happened the record is only a note: a pass in the same boot must not
// start the guests the update has just shut down.
func TestBootPassIgnoresUpdateRecordFromTheSameBoot(t *testing.T) {
	s, recorded, _ := updateRestartFixture(t, bootBeforeUpdate)
	setPoolVMAutostartBootID(t, bootBeforeUpdate)
	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason != "" {
		t.Fatalf("the pass did not run: %q", reason)
	}
	if autostartAttempted(t, s, recorded, bootBeforeUpdate) {
		t.Fatal("a guest was started in the boot that was still shutting it down")
	}
	if _, err := os.Stat(updateRestartPath(s)); err != nil {
		t.Fatalf("the record was dropped before the reboot: %v", err)
	}
}

// A host in maintenance mode starts nothing at boot; the update record does not override
// that, and it survives to be honoured once the host is back in service.
func TestUpdateRecordDoesNotOverrideMaintenanceMode(t *testing.T) {
	s, recorded, _ := updateRestartFixture(t, bootBeforeUpdate)
	maintenanceMembership(t, s, "host-a", maintenanceMember("host-a", true))
	setPoolVMAutostartBootID(t, bootAfterUpdate)
	if reason := ReconcilePoolVMAutostart(context.Background(), s); reason == "" {
		t.Fatal("the pass ran on a drained host")
	}
	if autostartAttempted(t, s, recorded, bootAfterUpdate) {
		t.Fatal("a drained host started a guest because of the update record")
	}
	if _, err := os.Stat(updateRestartPath(s)); err != nil {
		t.Fatalf("the record was lost while the host was drained: %v", err)
	}
}
