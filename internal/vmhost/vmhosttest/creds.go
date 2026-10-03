// SPDX-License-Identifier: AGPL-3.0-only

// Package vmhosttest holds test doubles for vmhost. Only test binaries import it.
package vmhosttest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
)

// FakeSystemdCreds seals with a visible prefix so tests can tell sealed files from plaintext.
// A file without the prefix is read as-is: some tests place a credentials file directly.
func FakeSystemdCreds(_ context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	if name != "systemd-creds" || len(args) < 4 {
		return nil, fmt.Errorf("unexpected command %s %v", name, args)
	}
	switch args[0] {
	case "encrypt":
		return append([]byte("sealed:"), stdin...), nil
	case "decrypt":
		b, err := os.ReadFile(args[len(args)-2])
		if err == nil && !bytes.HasPrefix(b, []byte("sealed:")) && !bytes.HasPrefix(b, []byte("username=")) && !bytes.HasPrefix(b, []byte("password=")) {
			err = errors.New("credential cannot be decrypted")
		}
		return bytes.TrimPrefix(b, []byte("sealed:")), err
	}
	return nil, fmt.Errorf("unexpected systemd-creds %v", args)
}
