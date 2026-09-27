// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

func TestPreflightRestoreReplaceRefusesUnsafeInPlace(t *testing.T) {
	req := RestoreReplaceRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		OriginalVMID: "vm-1", StorageID: "storage-1", ConfirmReplace: true,
	}
	if reason := PreflightRestoreReplaceReason(req, "running", true, false); reason == "" {
		t.Fatal("running original must be refused")
	}
	if reason := PreflightRestoreReplaceReason(req, "shut off", true, true); reason == "" {
		t.Fatal("in-place overwrite must be refused")
	}
	noConfirm := req
	noConfirm.ConfirmReplace = false
	if reason := PreflightRestoreReplaceReason(noConfirm, "shut off", true, false); reason == "" {
		t.Fatal("missing confirm_replace must be refused")
	}
	if reason := PreflightRestoreReplaceReason(req, "shut off", false, false); reason == "" {
		t.Fatal("missing original must be refused")
	}
	if reason := PreflightRestoreReplaceReason(req, "shut off", true, false); reason != "" {
		t.Fatalf("safe replace refused: %s", reason)
	}
}

func TestProtectAndTestRestoreRequestsAreBounded(t *testing.T) {
	if err := (ProtectRecoveryPointRequest{RepositoryID: "repo-1", RecoveryPointID: "point-1", Protected: true}).Valid(); err != nil {
		t.Fatal(err)
	}
	test := TestRestoreRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1",
		NewVMID: "22222222-2222-4222-8222-222222222222", NewVMName: "test-restore", StorageID: "storage-1",
	}
	if err := test.Valid(); err != nil {
		t.Fatal(err)
	}
	verify := TestRestoreVerifyRequest{
		RepositoryID: "repo-1", RecoveryPointID: "point-1", TestVMID: test.NewVMID, Result: BackupTestResultPassed,
	}
	if err := verify.Valid(); err != nil {
		t.Fatal(err)
	}
	verify.Result = "ok"
	if err := verify.Valid(); err == nil {
		t.Fatal("invalid verify result must be refused")
	}
	if err := (CatalogImportRequest{RepositoryID: "repo-1"}).Valid(); err != nil {
		t.Fatal(err)
	}
}
