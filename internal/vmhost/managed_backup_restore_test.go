// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestCleanupTestRestoredVMSettlesWhenNothingWasRegistered(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "managed"))
	if err := os.Mkdir(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	identity := protocol.VMCreateSpec{ID: "22222222-2222-4222-8222-222222222222", PoolID: "pool-1", HostID: "host-1"}
	fakeCommand(t, "virsh", `case "$3" in list) echo '`+identity.ID+`';; *) exit 1;; esac`)
	result, err := e.CleanupTestRestoredVM(context.Background(), "test-restore-1", identity, "test-clean-1")
	if err != nil || result.Status == "succeeded" {
		t.Fatalf("a missing journal must not settle while the domain still exists: %+v err=%v", result, err)
	}
	fakeCommand(t, "virsh", `case "$3" in list) exit 0;; *) exit 1;; esac`)
	result, err = e.CleanupTestRestoredVM(context.Background(), "test-restore-1", identity, "test-clean-1")
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("cleanup of a restore with no creator journal must settle: %+v err=%v", result, err)
	}
}
