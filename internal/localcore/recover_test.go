// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nex.local/nex/internal/vmhost"
)

func recoveryFixture(t *testing.T) (Config, DeployRequest, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{DataDir: filepath.Join(dir, "local"), AgentStateDir: filepath.Join(dir, "agent"), Engine: vmhost.Config{StateDir: filepath.Join(dir, "local", "vmhost"), StorageDir: filepath.Join(dir, "storage"), Bridge: "br0"}}
	v := DeployRequest{RequestID: "recovery-001", Name: "nex-replacement", Hostname: "nex-replacement", CPUs: 2, MemoryMiB: 4096, DiskGiB: 32, AddressCIDR: "192.168.1.11/24", Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}, AdminPassword: "temporary-replacement-password", Bridge: cfg.Engine.Bridge, StorageDir: cfg.Engine.StorageDir}
	path := filepath.Join(dir, "request.json")
	if err := savePrivate(path, v); err != nil {
		t.Fatal(err)
	}
	return cfg, v, path
}
func TestRecoveryRejectsUnsafeInputAndNormalBootstrap(t *testing.T) {
	cfg, v, path := recoveryFixture(t)
	os.Chmod(path, 0644)
	if _, err := prepareRecovery(context.Background(), cfg, path); err == nil {
		t.Fatal("public secret file accepted")
	}
	os.Chmod(path, 0600)
	v.SiteName = "must-not-be-created"
	savePrivate(path, v)
	if _, err := prepareRecovery(context.Background(), cfg, path); err == nil {
		t.Fatal("normal bootstrap accepted")
	}
	if _, err := os.Stat(cfg.DataDir); !os.IsNotExist(err) {
		t.Fatal("invalid input touched deployment state")
	}
}
func TestRecoveryRejectsExistingAgentAndServiceLock(t *testing.T) {
	cfg, _, path := recoveryFixture(t)
	os.MkdirAll(cfg.AgentStateDir, 0700)
	savePrivate(filepath.Join(cfg.AgentStateDir, "state.json"), map[string]string{"host_id": "existing"})
	if _, err := prepareRecovery(context.Background(), cfg, path); err == nil {
		t.Fatal("existing identity accepted")
	}
	os.Remove(filepath.Join(cfg.AgentStateDir, "state.json"))
	s, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err = prepareRecovery(context.Background(), cfg, path); err == nil {
		t.Fatal("running service lock ignored")
	}
}
func TestPreparedRecoveryRetryDoesNotHandoffOrCreateAnotherVM(t *testing.T) {
	cfg, v, path := recoveryFixture(t)
	os.MkdirAll(cfg.DataDir, 0700)
	b, _ := json.Marshal(v)
	sum := sha256.Sum256(b)
	d := deployment{Digest: hex.EncodeToString(sum[:]), Operation: vmhost.Operation{ID: v.RequestID, VMID: "replacement-identity", Status: "recovery_prepared", Stage: "awaiting_manual_restore"}}
	savePrivate(filepath.Join(cfg.DataDir, "recovery-preparation.json"), d)
	op, err := prepareRecovery(context.Background(), cfg, path)
	if err != nil || op.VMID != "replacement-identity" {
		t.Fatal(op, err)
	}
	for _, p := range []string{cfg.AgentStateDir, cfg.Engine.StateDir, filepath.Join(cfg.DataDir, "deployments.json")} {
		if _, err = os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("retry unexpectedly created %s", p)
		}
	}
	v.Name = "different"
	savePrivate(path, v)
	if _, err = prepareRecovery(context.Background(), cfg, path); err == nil {
		t.Fatal("changed retry accepted")
	}
}
func TestNormalDeploymentRejectsAgentOwnershipBeforePersistence(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	os.MkdirAll(s.cfg.AgentStateDir, 0700)
	savePrivate(filepath.Join(s.cfg.AgentStateDir, "state.json"), map[string]string{"host_id": "existing"})
	v := DeployRequest{RequestID: "request-001", Name: "nex-controller", Hostname: "controller", CPUs: 2, MemoryMiB: 4096, DiskGiB: 32, AddressCIDR: "192.168.1.10/24", Gateway: "192.168.1.1", DNS: []string{"192.168.1.1"}, AdminPassword: "controller-password", SiteName: "Site", PoolName: "Pool", Bridge: s.cfg.Engine.Bridge, StorageDir: s.cfg.Engine.StorageDir}
	w := callLocal(s, "POST", "/api/local/v1/deployments", v, cookie, csrf, "https://core.test")
	if w.Code != 409 || len(s.operations) != 0 {
		t.Fatal(w.Code, w.Body, s.operations)
	}
	entries, err := os.ReadDir(s.cfg.Engine.StorageDir)
	if err != nil || len(entries) != 0 {
		t.Fatal("ownership rejection mutated VM storage", err)
	}
}

func TestNormalDeploymentRejectsRecoveryPreparationBeforePersistence(t *testing.T) {
	s, _ := testService(t)
	cookie, csrf := loginLocal(t, s)
	savePrivate(filepath.Join(s.cfg.DataDir, "recovery-preparation.json"), deployment{})
	v := DeployRequest{RequestID: "request-001", SiteName: "Site", PoolName: "Pool", Bridge: s.cfg.Engine.Bridge, StorageDir: s.cfg.Engine.StorageDir}
	w := callLocal(s, "POST", "/api/local/v1/deployments", v, cookie, csrf, "https://core.test")
	if w.Code != 409 || len(s.operations) != 0 {
		t.Fatal(w.Code, w.Body, s.operations)
	}
}
