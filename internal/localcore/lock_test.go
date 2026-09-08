// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"os/exec"
	"testing"
)

func TestExecChildDoesNotRetainServiceLock(t *testing.T) {
	s, cfg := testService(t)
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = child.Process.Kill(); _ = child.Wait() }()
	// The external helper must not keep the service lock alive after shutdown.
	s.Close()
	again, err := Open(cfg)
	if err != nil {
		t.Fatalf("child retained service lock after owner closed: %v", err)
	}
	again.Close()
	_ = child.Process.Kill()
	_ = child.Wait()
	afterExit, err := Open(cfg)
	if err != nil {
		t.Fatalf("lock unavailable after helper exit: %v", err)
	}
	afterExit.Close()
}
