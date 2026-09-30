// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"testing"
)

// runSystemdCreds is the only place Core execs systemd-creds. Inside a test binary the real
// tool is refused: on a workstation it raises a polkit prompt for the logged-in user. A test
// that reaches it must install vmhosttest.FakeSystemdCreds through SetSystemdCredsRunner.
var runSystemdCreds = realSystemdCreds

func realSystemdCreds(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if testing.Testing() {
		return nil, errors.New("refusing to run the real systemd-creds from a test; install vmhosttest.FakeSystemdCreds in TestMain")
	}
	return runInput(ctx, stdin, name, args...)
}

// SetSystemdCredsRunner replaces the systemd-creds runner. Test packages call it from TestMain.
func SetSystemdCredsRunner(run func(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error)) {
	runSystemdCreds = run
}
