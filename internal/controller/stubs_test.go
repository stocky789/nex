// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps this package's tests off the developer's real hypervisor. The Controller imports
// code that can shell out to virsh; a refusing stub first on PATH makes any such call fail the same
// way on every machine instead of raising polkit prompts on a workstation that has libvirt.
func TestMain(m *testing.M) {
	code, err := runWithStubbedHostTools(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "test stub setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func runWithStubbedHostTools(m *testing.M) (int, error) {
	dir, err := os.MkdirTemp("", "nex-test-stubs")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	const refuse = "#!/bin/sh\necho \"refusing to run the host's $(basename $0) from a test\" >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "virsh"), []byte(refuse), 0o700); err != nil {
		return 0, err
	}
	if err := os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH")); err != nil {
		return 0, err
	}
	return m.Run(), nil
}
