// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestQEMUStorageAccessUsesSeparatePOSIXTests(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	var calls [][]string
	path := t.TempDir()
	nativeRun = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, append([]string{name}, args...))
		if name == "id" {
			return []byte(strconv.Itoa(os.Getuid())), nil
		}
		return nil, nil
	}
	if err := qemuStorageAccess(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"runuser", "-u", "libvirt-qemu", "--", "test", "-x", path},
		{"id", "-u", "libvirt-qemu"},
	}
	if len(calls) != 4 || !reflect.DeepEqual(calls[:2], want) || calls[2][0] != "runuser" || calls[2][5] != "-r" || calls[3][5] != "-w" || filepath.Dir(calls[2][6]) != path || calls[2][6] != calls[3][6] {
		t.Fatalf("calls = %#v, want %#v", calls, want)
	}
}

func TestCredentialFileUsesBoundCoreRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "receipt"), []byte("password=secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), storageCredentialRootKey{}, root)
	got, err := credentialFile(ctx, protocol.StorageRegistration{CredentialReceipt: "receipt"})
	if err != nil || got != filepath.Join(root, "receipt") {
		t.Fatalf("credentialFile() = %q, %v", got, err)
	}
}
