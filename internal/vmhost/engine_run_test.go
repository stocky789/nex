// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRunWithDirAnchorsChildRelativeAccessToHeldParentFile(t *testing.T) {
	root := t.TempDir()
	probe := filepath.Join(root, "probe")
	if err := os.WriteFile(probe, []byte("probe"), 0600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(root, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		t.Fatal(err)
	}
	dir := os.NewFile(uintptr(fd), root)
	defer dir.Close()
	// Replace the pathname after opening it. The child must use the still-open
	// directory, not the attacker-controlled replacement.
	moved := filepath.Join(t.TempDir(), "held")
	if err := os.Rename(root, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), root); err != nil {
		t.Fatal(err)
	}
	for _, option := range []string{"-r", "-w"} {
		if _, err := runWithDir(context.Background(), dir, "sh", "-c", "exec test \"$1\" \"$2\"", "sh", option, filepath.Base(probe)); err != nil {
			t.Fatalf("relative %s access through anchored child cwd: %v", option, err)
		}
	}
}
