// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain keeps this package's tests off the developer's real hypervisor. Several paths shell
// out to virsh without stubbing it. On CI there is no libvirt, so those calls already fail and
// the tests already tolerate it; on a workstation that has libvirt they reach qemu:///system for
// real, and each one raises a polkit authorisation prompt to the logged-in user. One run of the
// suite issued 28 such calls, including "virsh event --all --loop", which retries continuously
// and flooded a desktop with hundreds of prompts (BUG-0216). A stub early in PATH makes the
// failure deterministic on every machine. Tests that install their own fake with fakeCommand
// still win, because they prepend their own directory to PATH after this one.
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
