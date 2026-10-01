// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/nexupdate"
)

func TestImageInventoryReportsWhatRunsAndWhatIsRetained(t *testing.T) {
	st := nexupdate.ImageStatus{
		Phase: nexupdate.PhaseArmed, Channel: "dev", RollbackFloor: 2, StateFormat: 2,
		Slots: []nexupdate.SlotStatus{
			{Slot: "a", Role: "booted", Label: "1.0", Sequence: 1, RootHash: "aa"},
			{Slot: "b", Role: "armed", Label: "1.1", Sequence: 2, RootHash: "bb"},
		},
		Failure: &nexupdate.Error{Code: "boot_failed", Message: "m"},
	}
	yes := true
	got := imageInventory(st, nexupdate.Booted{RootHash: "aa", Label: "1.0", Sequence: 1}, &yes)
	if got.Booted.Slot != "a" || got.Booted.Sequence != 1 || got.Staged == nil || got.Staged.Slot != "b" || got.Previous != nil {
		t.Fatalf("slots: %+v", got)
	}
	if got.RollbackFloor != 2 || got.StateFormat != 2 || got.Phase != "armed" || got.Failure.Code != "boot_failed" || got.SecureBoot == nil || !*got.SecureBoot {
		t.Fatalf("status: %+v", got)
	}
	// A record that lags a boot must not claim the booted slot: the release that really runs is reported
	// by hash, without a slot letter it cannot vouch for.
	got = imageInventory(st, nexupdate.Booted{RootHash: "bb", Label: "1.1", Sequence: 2}, nil)
	if got.Booted.Slot != "" || got.Booted.Sequence != 2 || got.SecureBoot != nil {
		t.Fatalf("lagging record: %+v", got)
	}
}

func TestReadSecureBootDistinguishesOffFromUnknown(t *testing.T) {
	dir := t.TempDir()
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if v := readSecureBoot(write("on", []byte{7, 0, 0, 0, 1})); v == nil || !*v {
		t.Fatalf("enforcing: %v", v)
	}
	if v := readSecureBoot(write("off", []byte{7, 0, 0, 0, 0})); v == nil || *v {
		t.Fatalf("disabled: %v", v)
	}
	if v := readSecureBoot(filepath.Join(dir, "missing")); v != nil {
		t.Fatalf("no EFI variable must be unknown, got %v", *v)
	}
}
