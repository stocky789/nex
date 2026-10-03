// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func iscsiDiscoveryFixture(t *testing.T) protocol.StorageDiscoveryRequest {
	t.Helper()
	oldPath, oldHelper, oldRun, oldNative := iscsiInitiatorPath, iscsiInspectHelper, iscsiInspectRun, nativeRun
	t.Cleanup(func() {
		iscsiInitiatorPath, iscsiInspectHelper, iscsiInspectRun, nativeRun = oldPath, oldHelper, oldRun, oldNative
	})
	iscsiInitiatorPath = filepath.Join(t.TempDir(), "initiatorname.iscsi")
	if err := os.WriteFile(iscsiInitiatorPath, []byte("# existing host identity\nInitiatorName=iqn.2026-09.test:core-initiator\n"), 0600); err != nil {
		t.Fatal(err)
	}
	nativeRun = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("selected-target inspection invoked a kernel/session/storage command")
		return nil, errors.New("unexpected native mutation")
	}
	r := request(protocol.StorageKindISCSI)
	r.TargetIQN = "iqn.2026-09.test:selected"
	return r
}

func TestSelectedISCSIObservesActualLUNsAndDoesNotGuessIdentity(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	for _, test := range []struct {
		name   string
		output string
		want   []protocol.ISCSILUNDiscovery
	}{
		{"empty", "[]", []protocol.ISCSILUNDiscovery{}},
		{"zero", `[{"lun":0,"wwid":"5001020304050607","filesystem_uuid":"12345678-1234-4234-8234-123456789012","filesystem_type":"ext4","size_bytes":104857600}]`, []protocol.ISCSILUNDiscovery{{LUN: 0, WWID: "5001020304050607", FilesystemUUID: "12345678-1234-4234-8234-123456789012", FilesystemType: "ext4", SizeBytes: 104857600}}},
		{"multiple", `[{"lun":7,"filesystem_type":"xfs"},{"lun":0,"error":"No filesystem signature detected: blank or unsupported media"}]`, []protocol.ISCSILUNDiscovery{{LUN: 0, Error: "No filesystem signature detected: blank or unsupported media"}, {LUN: 7, FilesystemType: "xfs"}}},
		{"unsupported", `[{"lun":3,"filesystem_type":"crypto_LUKS","error":"Detected filesystem is unsupported for iSCSI repositories"}]`, []protocol.ISCSILUNDiscovery{{LUN: 3, FilesystemType: "crypto_LUKS", Error: "Detected filesystem is unsupported for iSCSI repositories"}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			iscsiInspectRun = func(ctx context.Context, input []byte, name string, args ...string) ([]byte, error) {
				if len(args) != 0 || name != iscsiInspectHelper || ctx.Err() != nil {
					t.Fatal("inspection did not use its single bounded native helper")
				}
				fields := strings.Split(string(input), "\n")
				if len(fields) != 6 || fields[0] != "iqn.2026-09.test:core-initiator" || fields[1] != "storage.example.test:3260" || fields[2] != r.TargetIQN || fields[3] != "" || fields[4] != "" || fields[5] != "" {
					t.Fatalf("wrong host/portal/selected-target handoff or unexpected CHAP: %q", fields)
				}
				return []byte(test.output), nil
			}
			got := DiscoverStorage(context.Background(), r)
			if got.Status != "succeeded" || len(got.Candidates) != 0 || !reflect.DeepEqual(got.ISCSILUNs, test.want) {
				t.Fatalf("observations = %+v, want %+v", got, test.want)
			}
		})
	}
}

func TestSelectedISCSISecretsAreTransientAndErrorsAreSafe(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	r.Credentials = &protocol.StorageCredentials{Username: "fixture-chap", Secret: "fixture-chap-secret"}
	var handed []byte
	iscsiInspectRun = func(_ context.Context, input []byte, _ string, args ...string) ([]byte, error) {
		fields := strings.Split(string(input), "\n")
		if len(args) != 0 || len(fields) != 6 || fields[3] != r.Credentials.Username || fields[4] != r.Credentials.Secret {
			t.Fatalf("CHAP was not passed privately over stdin as fields 3 and 4: %q", fields)
		}
		handed = input
		return nil, fmt.Errorf("native error contains %s", r.Credentials.Secret)
	}
	before, _ := os.ReadFile(iscsiInitiatorPath)
	got := DiscoverStorage(context.Background(), r)
	after, _ := os.ReadFile(iscsiInitiatorPath)
	if got.Status != "failed" || strings.Contains(got.Error, r.Credentials.Secret) || !reflect.DeepEqual(before, after) {
		t.Fatalf("unsafe or mutating inspection: %+v", got)
	}
	for _, b := range handed {
		if b != 0 {
			t.Fatal("private stdin bytes remained after inspection")
		}
	}
	if r.Credentials.Secret != "fixture-chap-secret" {
		t.Fatal("inspection mutated caller-owned credentials")
	}
}

func TestSelectedISCSIRejectsInvalidNativeResults(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	for _, output := range []string{
		`null`, `{}`, `[{}]`, `[{"lun":null}]`, `[{"lun":-1}]`, `[{"lun":0},{"lun":0}]`,
		`[{"lun":0,"credentials":"leak"}]`, `[{"lun":0,"error":"bad\nreason"}]`, `[] []`,
		"[" + strings.Repeat(`{"lun":1},`, discoveryLimit) + `{"lun":2}]`,
		`[{"lun":0,"wwid":"bad wwid"}]`, `[{"lun":0,"wwid":"` + strings.Repeat("a", 256) + `"}]`,
		`[{"lun":0,"filesystem_type":"ext\n4"}]`, `[{"lun":0,"filesystem_type":"` + strings.Repeat("a", 64) + `"}]`,
		`[{"lun":0,"error":"` + strings.Repeat("a", 512) + `"}]`,
	} {
		iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) { return []byte(output), nil }
		if got := DiscoverStorage(context.Background(), r); got.Status != "failed" || len(got.ISCSILUNs) != 0 {
			t.Fatalf("invalid native result was presented as discovery: %q: %+v", output, got)
		}
	}
}

func TestSelectedISCSIDowngradesOnlyTheLUNWithAnUnsupportedFilesystemUUID(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	for _, uuid := range []string{"not-a-uuid", "bad\\nuuid", strings.Repeat("a", 256)} {
		output := `[{"lun":0,"filesystem_uuid":"` + uuid + `","filesystem_type":"ext4"},{"lun":1,"filesystem_uuid":"12345678-1234-4234-8234-123456789012","filesystem_type":"xfs"}]`
		iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) { return []byte(output), nil }
		got := DiscoverStorage(context.Background(), r)
		if got.Status != "succeeded" || len(got.ISCSILUNs) != 2 || got.ISCSILUNs[0].FilesystemUUID != "" || got.ISCSILUNs[0].Error == "" ||
			got.ISCSILUNs[1].FilesystemUUID == "" || got.ISCSILUNs[1].Error != "" {
			t.Fatalf("uuid %q was not downgraded for its own LUN only: %+v", uuid, got)
		}
	}
}

func TestSelectedISCSIMissingHelperIsUnavailableAndHelperEnvironmentIsMinimal(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	iscsiInspectHelper = filepath.Join(t.TempDir(), "missing")
	iscsiInspectRun = runInspectHelper
	if got := DiscoverStorage(context.Background(), r); got.Status != "failed" || got.Error != "storage discovery is unavailable" || len(got.ISCSILUNs) != 0 {
		t.Fatalf("missing helper = %+v", got)
	}
	if ISCSIInspectionAvailable(context.Background()) {
		t.Fatal("missing helper advertised inspection")
	}
	env, err := exec.LookPath("env")
	if err != nil {
		t.Skip("env is unavailable")
	}
	t.Setenv("NEX_TEST_INHERITED", "must-not-reach-helper")
	output, err := runInspectHelper(context.Background(), nil, env)
	if err != nil || strings.TrimSpace(string(output)) != "LC_ALL=C" {
		t.Fatalf("helper environment = %q, %v", output, err)
	}
}

func TestSelectedISCSICancellationEndsOwnHelper(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	iscsiInspectHelper = filepath.Join(t.TempDir(), "inspect")
	if err := os.WriteFile(iscsiInspectHelper, []byte("#!/bin/sh\nexec sleep 30\n"), 0700); err != nil {
		t.Fatal(err)
	}
	iscsiInspectRun = runInspectHelper
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := DiscoverStorage(ctx, r)
	if got.Status != "cancelled" || len(got.ISCSILUNs) != 0 || time.Since(start) > 2*time.Second {
		t.Fatalf("cancelled helper did not close promptly: %+v", got)
	}
}

func TestISCSIInspectionCapabilityRequiresActualRuntimeAndInitiator(t *testing.T) {
	iscsiDiscoveryFixture(t)
	iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte(protocol.ISCSIInspectCapability + "\n"), nil
	}
	if !ISCSIInspectionAvailable(context.Background()) {
		t.Fatal("working runtime with host initiator not available")
	}
	iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) { return []byte("older-helper"), nil }
	if ISCSIInspectionAvailable(context.Background()) {
		t.Fatal("incompatible helper advertised inspection")
	}
	iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte(protocol.ISCSIInspectCapability), nil
	}
	for _, contents := range []string{"GenerateName=yes\n", "InitiatorName=bad\n", "InitiatorName=iqn.2026-09.test:first\nInitiatorName=iqn.2026-09.test:second\n"} {
		if err := os.WriteFile(iscsiInitiatorPath, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if ISCSIInspectionAvailable(context.Background()) {
			t.Fatal("missing/ambiguous initiator identity advertised inspection")
		}
	}
}

func TestSendTargetsOnlyTreatsTypedNoObjectsExitAsEmpty(t *testing.T) {
	old := nativeRun
	t.Cleanup(func() { nativeRun = old })
	noObjects := exec.Command("sh", "-c", "exit 21").Run()
	for _, test := range []struct {
		err    error
		status string
	}{
		{fmt.Errorf("native wrapped: %w", noObjects), "succeeded"},
		{errors.New("exit status 21"), "failed"},
		{exec.Command("sh", "-c", "exit 24").Run(), "failed"},
	} {
		nativeRun = func(context.Context, string, ...string) ([]byte, error) { return nil, test.err }
		got := DiscoverStorage(context.Background(), request(protocol.StorageKindISCSI))
		if got.Status != test.status || len(got.Candidates) != 0 {
			t.Fatalf("SendTargets classification: %+v, want %s", got, test.status)
		}
	}
}

func TestFlatAddressedDiscoveryLUNMatchesNativeRegistrationIdentity(t *testing.T) {
	r := iscsiDiscoveryFixture(t)
	const uuid = "12345678-1234-4234-8234-123456789012"
	const wwid = "60000000000000000e00000000010001"
	iscsiInspectRun = func(context.Context, []byte, string, ...string) ([]byte, error) {
		return []byte(`[{"lun":16640,"wwid":"` + wwid + `","filesystem_uuid":"` + uuid + `","filesystem_type":"ext4"}]`), nil
	}
	found := DiscoverStorage(context.Background(), r)
	if found.Status != "succeeded" || len(found.ISCSILUNs) != 1 || found.ISCSILUNs[0].LUN != 16640 {
		t.Fatalf("flat-addressed observation changed native LUN identity: %+v", found)
	}
	source := &protocol.ISCSIStorageSource{Portal: r.Server, TargetIQN: r.TargetIQN, LUN: found.ISCSILUNs[0].LUN, WWID: found.ISCSILUNs[0].WWID, FilesystemUUID: found.ISCSILUNs[0].FilesystemUUID, ExclusiveTarget: true}
	registration := protocol.StorageRegistration{ID: "repo-flat", Name: "Flat addressed", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: source}}
	if err := protocol.ValidateStorageRegistration(registration, false); err != nil {
		t.Fatalf("native LUN identity was not registerable: %v", err)
	}
	raw := []byte(`{"blockdevices":[{"path":"/dev/fixture","wwn":"0x` + wwid + `","uuid":"` + uuid + `","fstype":"ext4","hctl":"6:0:0:16640"}]}`)
	filesystem, err := iscsiFilesystemForLUNInfo(raw, source.FilesystemUUID, source.LUN)
	if err != nil || canonicalWWID(filesystem.WWID) != source.WWID {
		t.Fatalf("discovered identity disagreed with native HCTL/WWN: %+v %v", filesystem, err)
	}
	if _, err := iscsiFilesystemForLUNInfo(raw, source.FilesystemUUID, 256); err == nil {
		t.Fatal("discarded flat addressing bits selected the same registered LUN")
	}
}
