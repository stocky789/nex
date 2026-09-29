// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

// runSystemdCreds is the only place Core execs systemd-creds. Inside a test binary the real
// tool is refused: on a workstation it raises a polkit prompt for the logged-in user. A test
// that reaches it must install FakeSystemdCreds through SetSystemdCredsRunner.
var runSystemdCreds = realSystemdCreds

func realSystemdCreds(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if testing.Testing() {
		return nil, errors.New("refusing to run the real systemd-creds from a test; install vmhost.FakeSystemdCreds in TestMain")
	}
	return runInput(ctx, stdin, name, args...)
}

// SetSystemdCredsRunner replaces the systemd-creds runner. Test packages call it from TestMain.
func SetSystemdCredsRunner(run func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)) {
	runSystemdCreds = run
}

// FakeSystemdCreds seals with a visible prefix so tests can tell sealed files from plaintext.
// A file without the prefix is read as-is: some tests place a credentials file directly.
func FakeSystemdCreds(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if name != "systemd-creds" || len(args) < 5 || args[0] != "--no-ask-password" {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	args = args[1:]
	switch args[0] {
	case "encrypt":
		return nil, os.WriteFile(args[len(args)-1], append([]byte("sealed:"), stdin...), 0o600)
	case "decrypt":
		b, err := os.ReadFile(args[len(args)-2])
		if err == nil && !bytes.HasPrefix(b, []byte("sealed:")) && !bytes.HasPrefix(b, []byte("username=")) && !bytes.HasPrefix(b, []byte("password=")) {
			err = errors.New("credential cannot be decrypted")
		}
		return bytes.TrimPrefix(b, []byte("sealed:")), err
	}
	return nil, fmt.Errorf("unexpected systemd-creds %v", args)
}
