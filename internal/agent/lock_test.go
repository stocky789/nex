// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"os"
	"os/exec"
	"testing"
)

func TestExecChildDoesNotRetainStateLock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	child := exec.Command("sleep", "30")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	s.Close()
	again, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("child retained agent state lock: %v", err)
	}
	again.Close()
	_ = child.Process.Kill()
	_ = child.Wait()
	afterExit, err := OpenStore(dir)
	if err != nil {
		t.Fatalf("lock unavailable after helper exit: %v", err)
	}
	afterExit.Close()
}
