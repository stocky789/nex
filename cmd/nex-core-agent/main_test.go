// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/agent"
)

func TestClusterStorageReadyCLIReadsSnapshotWithoutStoreLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	old := os.Args
	os.Args = []string{"nex-core-agent", "cluster-storage-ready", "--state-dir", dir}
	t.Cleanup(func() { os.Args = old })
	err = run()
	if err == nil || strings.Contains(err.Error(), "another agent process owns") {
		t.Fatalf("cluster boot CLI contended for Store lock: %v", err)
	}
}

func TestClusterISCSISessionCLIIsRootOnlyAndBoundsReadOnlyProbe(t *testing.T) {
	const portal = "storage.example:3260"
	const target = "iqn.2026-09.example:shared"
	_, id, err := storageISCSISessionParameters(portal, target)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"--portal", portal, "--target", target, "--session-id", id}
	if err := runClusterISCSISession(args, 1000); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("non-root probe error = %v", err)
	}
	old := validateStorageISCSISession
	t.Cleanup(func() { validateStorageISCSISession = old })
	want := errors.New("read-only probe")
	called := 0
	validateStorageISCSISession = func(ctx context.Context, gotPortal, gotTarget, gotID string) error {
		called++
		if gotPortal != portal || gotTarget != target || gotID != id {
			t.Fatalf("probe arguments = %q, %q, %q", gotPortal, gotTarget, gotID)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 8*time.Second || time.Until(deadline) <= 0 {
			t.Fatalf("probe deadline is not bounded below OCF monitor timeout")
		}
		return want
	}
	if err := runClusterISCSISession(args, 0); !errors.Is(err, want) {
		t.Fatalf("probe error = %v, want %v", err, want)
	}
	if err := runClusterISCSISession(append(args, "--validate-only"), 0); err != nil {
		t.Fatalf("static validation queried or failed: %v", err)
	}
	if called != 1 {
		t.Fatalf("static validation invoked session query %d times", called)
	}
}

func TestRunDispatchesClusterISCSISessionFlagsBeforeGenericParser(t *testing.T) {
	const portal = "storage.example:3260"
	const target = "iqn.2026-09.example:shared"
	_, id, err := storageISCSISessionParameters(portal, target)
	if err != nil {
		t.Fatal(err)
	}
	oldArgs, oldEUID, oldValidate := os.Args, currentEUID, validateStorageISCSISession
	t.Cleanup(func() {
		os.Args, currentEUID, validateStorageISCSISession = oldArgs, oldEUID, oldValidate
	})
	os.Args = []string{"nex-core-agent", "cluster-iscsi-session", "--portal", portal, "--target", target, "--session-id", id}
	currentEUID = func() int { return 1000 }
	if err := run(); err == nil || !strings.Contains(err.Error(), "requires root") {
		t.Fatalf("non-root run error = %v, want root gate after flag dispatch", err)
	}
	called := false
	currentEUID = func() int { return 0 }
	validateStorageISCSISession = func(context.Context, string, string, string) error {
		called = true
		return nil
	}
	if err := run(); err != nil {
		t.Fatalf("root dispatched run = %v", err)
	}
	if !called {
		t.Fatal("root run did not invoke the iSCSI session validator")
	}
}
