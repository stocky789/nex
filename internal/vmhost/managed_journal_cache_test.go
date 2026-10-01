// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/protocol"
)

func newCacheTestEngine(t *testing.T) *ManagedEngine {
	t.Helper()
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func saveCacheTestJournal(t *testing.T, e *ManagedEngine, id string) {
	t.Helper()
	req := protocol.StorageRegistration{ID: "s-" + id, PoolID: "p", HostID: "h", Path: t.TempDir()}
	if err := e.save(managedJournal{Command: protocol.Command{OperationID: id, Kind: "storage-register", Storage: &req}, Result: protocol.Result{OperationID: id, Status: "succeeded"}, AppliedStorage: &req}); err != nil {
		t.Fatal(err)
	}
}

func TestSettledJournalsAreValidatedOnce(t *testing.T) {
	e := newCacheTestEngine(t)
	const n = 500
	for i := 0; i < n; i++ {
		saveCacheTestJournal(t, e, fmt.Sprintf("op-%03d", i))
	}
	before := journalValidations.Load()
	for pass := 0; pass < 5; pass++ {
		if js, bad := e.readable(); len(js) != n || len(bad) != 0 {
			t.Fatalf("pass %d: %d readable, bad %v", pass, len(js), bad)
		}
	}
	if got := journalValidations.Load() - before; got > n {
		t.Fatalf("%d validations over 5 passes of %d journals, want at most %d", got, n, n)
	}
}

func TestChangedJournalIsRevalidated(t *testing.T) {
	e := newCacheTestEngine(t)
	saveCacheTestJournal(t, e, "op-1")
	if _, bad := e.readable(); len(bad) != 0 {
		t.Fatal(bad)
	}
	path := filepath.Join(e.stateDir, "op-1.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	// Same size and mtime, different content: the result status becomes invalid.
	bad := []byte(string(b))
	i := indexOf(bad, `"succeeded"`)
	copy(bad[i:], `"succeedXd"`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	if js, bad := e.readable(); len(js) != 0 || len(bad) != 1 {
		t.Fatalf("rewritten journal accepted: %d readable, bad %v", len(js), bad)
	}
}

func indexOf(b []byte, s string) int {
	for i := range b {
		if string(b[i:min(i+len(s), len(b))]) == s {
			return i
		}
	}
	return -1
}
