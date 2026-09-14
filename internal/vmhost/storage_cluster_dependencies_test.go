// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"debug/elf"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStorageClusterFenceSONAMERequiresSingleNULTerminatedLibrary(t *testing.T) {
	if _, err := storageClusterFenceSONAME([]byte("not-a-library libstonithd.so.26\x00")); err == nil {
		t.Fatal("unterminated-prefix library name was accepted")
	}
	if got, err := storageClusterFenceSONAME([]byte("\x00libstonithd.so.26\x00")); err != nil || got != "libstonithd.so.26" {
		t.Fatalf("SONAME = %q, %v", got, err)
	}
	if _, err := storageClusterFenceSONAME([]byte("\x00libstonithd.so.26\x00\x00libstonithd.so.56\x00")); err == nil {
		t.Fatal("ambiguous helper library requirements were accepted")
	}
	if _, err := storageClusterFenceSONAME([]byte("\x00libstonithd.so.26\x00\x00libstonithd.so.26\x00")); err == nil {
		t.Fatal("duplicate helper library requirements were accepted")
	}
}

func TestValidateStorageClusterDLMFenceHelperFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cache   string
		exports map[string]bool
		wantErr string
	}{
		{name: "unavailable exact soname", cache: "libstonithd.so.56 (libc6,x86-64) => /lib/libstonithd.so.56\n", wantErr: "unavailable"},
		{name: "missing required API", cache: "libstonithd.so.26 (libc6,x86-64) => /lib/libstonithd.so.26\n", exports: map[string]bool{"stonith_api_time": true}, wantErr: "incompatible"},
		{name: "wrong elf architecture", cache: "libstonithd.so.26 (libc6,x86-64) => /lib/libstonithd.so.26\n", exports: map[string]bool{"stonith_api_time": true, "stonith_api_kick": true}, wantErr: "incompatible"},
		{name: "ambiguous loader candidates", cache: "libstonithd.so.26 (libc6,x86-64) => /lib/a/libstonithd.so.26\nlibstonithd.so.26 (libc6,x86-64) => /lib/b/libstonithd.so.26\n", exports: map[string]bool{"stonith_api_time": true, "stonith_api_kick": true}, wantErr: "ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			helper := storageClusterDependencyFixture(t)
			oldPath, oldCache, oldIdentity, oldExports := storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports
			t.Cleanup(func() {
				storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports = oldPath, oldCache, oldIdentity, oldExports
			})
			storageClusterFenceHelperPath = func() (string, error) { return helper, nil }
			storageClusterFenceLibraryCache = func(context.Context) ([]byte, error) { return []byte(tc.cache), nil }
			storageClusterELFIdentity = func(path string) (storageClusterELFInfo, error) {
				if path == helper {
					return storageClusterELFInfo{class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
				}
				if tc.name == "wrong elf architecture" {
					return storageClusterELFInfo{class: elf.ELFCLASS32, machine: elf.EM_386}, nil
				}
				return storageClusterELFInfo{class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
			}
			storageClusterELFExports = func(string) (map[string]bool, error) { return tc.exports, nil }
			if err := ValidateStorageClusterDLMFenceHelper(context.Background()); err == nil || !containsError(err, tc.wantErr) {
				t.Fatalf("compatibility check = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestValidateStorageClusterDLMFenceHelperRejectsLateCancellation(t *testing.T) {
	helper := storageClusterDependencyFixture(t)
	oldPath, oldCache, oldIdentity, oldExports := storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports
	t.Cleanup(func() {
		storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports = oldPath, oldCache, oldIdentity, oldExports
	})
	storageClusterFenceHelperPath = func() (string, error) { return helper, nil }
	storageClusterFenceLibraryCache = func(context.Context) ([]byte, error) {
		return []byte("libstonithd.so.26 (libc6,x86-64) => /lib/libstonithd.so.26\n"), nil
	}
	storageClusterELFIdentity = func(string) (storageClusterELFInfo, error) {
		return storageClusterELFInfo{class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	storageClusterELFExports = func(string) (map[string]bool, error) {
		cancel()
		return map[string]bool{"stonith_api_time": true, "stonith_api_kick": true}, nil
	}
	if err := ValidateStorageClusterDLMFenceHelper(ctx); err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
		t.Fatalf("late cancellation was accepted: %v", err)
	}
}

func TestValidateStorageClusterDLMFenceHelperAcceptsExactCompatibleLibrary(t *testing.T) {
	helper := storageClusterDependencyFixture(t)
	oldPath, oldCache, oldIdentity, oldExports := storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports
	t.Cleanup(func() {
		storageClusterFenceHelperPath, storageClusterFenceLibraryCache, storageClusterELFIdentity, storageClusterELFExports = oldPath, oldCache, oldIdentity, oldExports
	})
	storageClusterFenceHelperPath = func() (string, error) { return helper, nil }
	storageClusterFenceLibraryCache = func(context.Context) ([]byte, error) {
		return []byte("libstonithd.so.26 (libc6,x86-64) => /lib/libstonithd.so.26\n"), nil
	}
	storageClusterELFIdentity = func(string) (storageClusterELFInfo, error) {
		return storageClusterELFInfo{class: elf.ELFCLASS64, machine: elf.EM_X86_64}, nil
	}
	storageClusterELFExports = func(string) (map[string]bool, error) {
		return map[string]bool{"stonith_api_time": true, "stonith_api_kick": true}, nil
	}
	if err := ValidateStorageClusterDLMFenceHelper(context.Background()); err != nil {
		t.Fatalf("compatible helper was rejected: %v", err)
	}
}

func TestValidateStorageClusterSessionMonitorUsesOneExactBoundedMonitor(t *testing.T) {
	oldTrusted, oldExecute := storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute
	t.Cleanup(func() {
		storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute = oldTrusted, oldExecute
	})
	var paths []string
	storageClusterSessionMonitorTrustedFile = func(path string) error {
		paths = append(paths, path)
		return nil
	}
	called := 0
	const portal = "storage.example:3260"
	const target = "iqn.2026-09.example:shared"
	_, sessionID, err := StorageISCSISessionParameters(portal, target)
	if err != nil {
		t.Fatal(err)
	}
	storageClusterSessionMonitorExecute = func(ctx context.Context, path string, args, env []string) error {
		called++
		if path != storageClusterSessionMonitorWrapper || !reflect.DeepEqual(args, []string{"monitor"}) {
			t.Fatalf("monitor command = %q %#v", path, args)
		}
		if want := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "OCF_RESKEY_portal=" + portal, "OCF_RESKEY_target=" + target, "OCF_RESKEY_session_id=" + sessionID}; !reflect.DeepEqual(env, want) {
			t.Fatalf("monitor environment = %#v, want %#v", env, want)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 10*time.Second {
			t.Fatal("monitor is not bounded to the required deadline")
		}
		return nil
	}
	if err := ValidateStorageClusterSessionMonitor(context.Background(), portal, target, sessionID); err != nil {
		t.Fatalf("monitor attestation = %v", err)
	}
	if called != 1 || !reflect.DeepEqual(paths, []string{storageClusterSessionMonitorWrapper, storageClusterSessionMonitorCore}) {
		t.Fatalf("monitor calls=%d paths=%#v", called, paths)
	}
}

func TestValidateStorageClusterSessionMonitorFailsClosed(t *testing.T) {
	const portal = "private.example:3260"
	const target = "iqn.2026-09.example:secret"
	_, sessionID, err := StorageISCSISessionParameters(portal, target)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		failPath  string
		execError error
	}{
		{name: "missing wrapper", failPath: storageClusterSessionMonitorWrapper},
		{name: "foreign wrapper", failPath: storageClusterSessionMonitorWrapper},
		{name: "missing local core", failPath: storageClusterSessionMonitorCore},
		{name: "old local core", execError: errors.New("old CLI")},
		{name: "query failure", execError: errors.New("private iSCSI detail")},
		{name: "timeout", execError: context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldTrusted, oldExecute := storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute
			t.Cleanup(func() {
				storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute = oldTrusted, oldExecute
			})
			storageClusterSessionMonitorTrustedFile = func(path string) error {
				if path == tc.failPath {
					return errors.New("untrusted monitor component")
				}
				return nil
			}
			storageClusterSessionMonitorExecute = func(context.Context, string, []string, []string) error { return tc.execError }
			err := ValidateStorageClusterSessionMonitor(context.Background(), portal, target, sessionID)
			if err == nil || strings.Contains(err.Error(), "private.example") || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private iSCSI") {
				t.Fatalf("failure was accepted or leaked native detail: %v", err)
			}
		})
	}
}

func TestValidateStorageClusterSessionMonitorRejectsNoncanonicalTupleBeforeExecution(t *testing.T) {
	oldTrusted, oldExecute := storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute
	t.Cleanup(func() {
		storageClusterSessionMonitorTrustedFile, storageClusterSessionMonitorExecute = oldTrusted, oldExecute
	})
	called := false
	storageClusterSessionMonitorTrustedFile = func(string) error { called = true; return nil }
	storageClusterSessionMonitorExecute = func(context.Context, string, []string, []string) error { called = true; return nil }
	err := ValidateStorageClusterSessionMonitor(context.Background(), "Storage.Example", "iqn.2026-09.example:shared", "nex-iscsi-not-a-real-id")
	if err == nil || called || strings.Contains(err.Error(), "Storage.Example") {
		t.Fatalf("invalid tuple was executed or leaked: %v", err)
	}
}

func storageClusterDependencyFixture(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dlm_stonith")
	if err := os.WriteFile(path, []byte("\x7fELF\x00libstonithd.so.26\x00"), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func containsError(err error, want string) bool {
	return err != nil && want != "" && strings.Contains(err.Error(), want)
}
