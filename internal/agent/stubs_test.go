// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/vmhost"
	"nex.local/nex/internal/vmhost/vmhosttest"
)

// TestMain keeps this package's tests off the developer's real hypervisor. Several paths shell
// out to virsh without stubbing it. On CI there is no libvirt, so those calls already fail and
// the tests already tolerate it; on a workstation that has libvirt they reach qemu:///system for
// real, and each one raises a polkit authorisation prompt to the logged-in user. One run of the
// suite issued 28 such calls, including "virsh event --all --loop", which retries continuously
// and flooded a desktop with hundreds of prompts (BUG-0216). A stub early in PATH makes the
// failure deterministic on every machine. Tests that install their own fake with fakeCommand
// still win, because they prepend their own directory to PATH after this one.
//
// Whether sbd feeds a watchdog is asked of systemd. A CI container has no systemctl, which the
// code rightly reads as "cannot tell", and a workstation running sbd would answer "armed"; the
// default is a host that is not armed, and the tests about arming install their own answer.
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
	// SMB credentials are sealed with systemd-creds, which prompts the desktop user for
	// authentication; vmhost refuses the real tool inside any test binary.
	vmhost.SetSystemdCredsRunner(vmhosttest.FakeSystemdCreds)
	localWatchdogArmed = func(context.Context) (bool, error) { return false, nil }
	// Guest bridge stanzas go to an empty scratch directory, never to the host's /etc/network:
	// as root a native apply there leaves a stanza behind that later runs read as foreign.
	network, err := os.MkdirTemp("", "nex-test-network")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(network)
	defer vmhost.HostNetworkDirForTests(network)()
	return m.Run(), nil
}
