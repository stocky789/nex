// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func bootProjection(t *testing.T, status string) (*Store, storageClusterOperation) {
	t.Helper()
	oldOwner, oldAncestor := clusterBootStateOwnership, clusterBootOwnership
	oldFenceHelper := validateClusterBootDLMFenceHelper
	clusterBootStateOwnership = func(uint32) bool { return true }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	validateClusterBootDLMFenceHelper = func(context.Context) error { return nil }
	t.Cleanup(func() {
		clusterBootStateOwnership, clusterBootOwnership = oldOwner, oldAncestor
		validateClusterBootDLMFenceHelper = oldFenceHelper
	})
	s := clusterRecoveryStore(t) // Deliberately remains locked during snapshot reads.
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	registration := protocol.StorageRegistration{ID: "repo-boot", PoolID: "pool-1", HostID: "master-1", Name: "shared", Path: "/var/lib/nex-storage/repo-boot", Kind: protocol.StorageKindISCSI, Generation: 1}
	registration.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:boot", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: registration.Path, Mode: "clustered_gfs2", ClusterID: "cluster-boot", LockTable: "cluster-boot:repo-boot", EligibleHostIDs: []string{"master-1", "member-1", "member-2"}}
	registration.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: registration.Source.ISCSI.TargetIQN, LUN: 1, WWID: registration.Source.ISCSI.WWID, FilesystemUUID: registration.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: "cluster-boot:repo-boot", GFS2Journals: 3}
	config := protocol.StorageClusterConfig{PoolID: "pool-1", RepositoryID: registration.ID, ClusterID: "cluster-boot", Generation: 1, LockTable: "cluster-boot:repo-boot", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: registration.Path}
	for i, host := range registration.Source.ISCSI.EligibleHostIDs {
		config.Nodes = append(config.Nodes, protocol.StorageClusterNode{HostID: host, CorosyncNodeID: uint32(i + 1), CorosyncAddress: "core" + string(rune('1'+i)) + ".example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-3333-4444-55555555555" + string(rune('1'+i)), CredentialRef: "credential" + string(rune('1'+i)), SSHHostKeyRef: "hostkey" + string(rune('1'+i)), SSHLogin: "root", SSHPort: 22, VirshURI: "qemu:///system"}})
	}
	for _, node := range config.Nodes {
		config.FenceProofs = append(config.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now().UTC(), ConfigGeneration: config.Generation, ConfigDigest: config.Digest()})
	}
	op := storageClusterOperation{RequestID: "boot-" + status, Digest: "durable-request", MembershipGeneration: 1, Registration: registration, Config: config, Status: status, UpdatedAt: time.Now().UTC()}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	return s, op
}

func TestClusterBootBlocksPacemakerGateWhenDLMFenceHelperInvalid(t *testing.T) {
	s, _ := bootProjection(t, "healthy")
	validateClusterBootDLMFenceHelper = func(context.Context) error { return errors.New("fixture ABI mismatch") }
	old := connectStorageCluster
	t.Cleanup(func() { connectStorageCluster = old })
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
		t.Fatalf("invalid DLM helper reached iSCSI readiness: calls=%d err=%v", calls, err)
	}
}

func TestClusterBootDoesNotConnectAfterLateDLMFenceHelperCancellation(t *testing.T) {
	s, _ := bootProjection(t, "healthy")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	validateClusterBootDLMFenceHelper = func(context.Context) error {
		cancel()
		return nil
	}
	old := connectStorageCluster
	t.Cleanup(func() { connectStorageCluster = old })
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if err := ClusterStorageBootReadyFromDir(ctx, s.dir); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("late-canceled DLM helper reached iSCSI readiness: calls=%d err=%v", calls, err)
	}
}

func TestClusterBootSnapshotRejectsNonRootOwnedProjection(t *testing.T) {
	s, _ := bootProjection(t, "healthy")
	clusterBootStateOwnership = func(uint32) bool { return false }
	old := connectStorageCluster
	defer func() { connectStorageCluster = old }()
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
		t.Fatalf("non-root projection reached connect: calls=%d err=%v", calls, err)
	}
}

func TestClusterBootReadyReadsSnapshotWhileAgentStoreLocked(t *testing.T) {
	for _, status := range []string{"applying", "healthy"} {
		t.Run(status, func(t *testing.T) {
			s, _ := bootProjection(t, status)
			old := connectStorageCluster
			defer func() { connectStorageCluster = old }()
			calls := 0
			connectStorageCluster = func(_ context.Context, gotDir string, _ protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
				calls++
				if gotDir != s.dir {
					t.Fatalf("state dir %q", gotDir)
				}
				return protocol.StorageRegistration{}, nil
			}
			if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err != nil || calls != 1 {
				t.Fatalf("locked snapshot gate: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestClusterBootUsesCompletedActivationDespiteLastDegradedObservation(t *testing.T) {
	for _, status := range []string{"degraded", "needs_attention"} {
		t.Run(status, func(t *testing.T) {
			s, op := bootProjection(t, status)
			op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID, Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: op.MembershipGeneration, CommittedAt: time.Now().UTC()}
			if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
				t.Fatal(err)
			}
			old := connectStorageCluster
			defer func() { connectStorageCluster = old }()
			calls := 0
			connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
				calls++
				return protocol.StorageRegistration{}, nil
			}
			if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err != nil || calls != 1 {
				t.Fatalf("completed activation did not reach boot readiness: calls=%d err=%v", calls, err)
			}
		})
	}

	s, op := bootProjection(t, "degraded")
	old := connectStorageCluster
	defer func() { connectStorageCluster = old }()
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
		t.Fatalf("degraded projection bypassed activation receipt: calls=%d err=%v op=%#v", calls, err, op)
	}
}

func TestClusterBootRejectsNewerOrUnsafeProjectionBeforeConnect(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Store, storageClusterOperation) error
	}{
		{name: "newer failed supersedes old healthy", change: func(s *Store, op storageClusterOperation) error {
			old := op
			old.RequestID, old.Status, old.UpdatedAt = "old", "healthy", time.Now().Add(-time.Minute)
			newer := op
			newer.RequestID, newer.Status, newer.Config.Generation, newer.Registration.Generation = "newer", "failed", 2, 2
			return saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{old.RequestID: old, newer.RequestID: newer}})
		}},
		{name: "ambiguous same generation", change: func(s *Store, op storageClusterOperation) error {
			other := op
			other.RequestID = "conflict"
			other.Config.LockTable = "other:table"
			return saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op, other.RequestID: other}})
		}},
		{name: "unverified proof", change: func(s *Store, op storageClusterOperation) error {
			op.Config.FenceProofs[0].Status = "unverified"
			return saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}})
		}},
		{name: "stale membership", change: func(s *Store, op storageClusterOperation) error {
			peer, err := loadPoolPeer(s)
			if err != nil {
				return err
			}
			peer.Membership.Generation++
			if err = savePoolPeer(s, peer); err != nil {
				return err
			}
			return saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, op := bootProjection(t, "healthy")
			if err := tc.change(s, op); err != nil {
				t.Fatal(err)
			}
			old := connectStorageCluster
			defer func() { connectStorageCluster = old }()
			calls := 0
			connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
				calls++
				return protocol.StorageRegistration{}, nil
			}
			if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
				t.Fatalf("unsafe boot projection connected: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestClusterBootRejectsHistoricalGenerationConflictBeforeSelectingNewer(t *testing.T) {
	s, op := bootProjection(t, "healthy")
	older := op
	older.RequestID, older.UpdatedAt = "older", time.Now().Add(-time.Minute)
	conflict := older
	conflict.RequestID, conflict.Config.LockTable = "older-conflict", "cluster-boot:other"
	newer := op
	newer.RequestID, newer.Config.Generation, newer.Registration.Generation = "newer", 2, 2
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{
		older.RequestID: older, conflict.RequestID: conflict, newer.RequestID: newer,
	}}); err != nil {
		t.Fatal(err)
	}
	state, err := loadStorageClusters(s)
	if err != nil {
		t.Fatal(err)
	}
	// Repeat because the old bug depended on Go map iteration order.
	for i := 0; i < 32; i++ {
		if _, err := clusterBootOperations(state); err == nil {
			t.Fatal("newer projection hid conflicting historical generation")
		}
	}
}

func TestClusterBootSnapshotRejectsWritableAncestor(t *testing.T) {
	s, _ := bootProjection(t, "healthy")
	clusterBootOwnership = func(info os.FileInfo) bool { return info.Mode().Perm()&0022 == 0 }
	old := connectStorageCluster
	t.Cleanup(func() { connectStorageCluster = old })
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	// The temporary-root ancestor is intentionally made writable only after the
	// private state leaf exists; a privileged gate must refuse that authority.
	if err := os.Chmod(filepath.Dir(s.dir), 0777); err != nil {
		t.Fatal(err)
	}
	if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
		t.Fatalf("writable ancestor reached connect: calls=%d err=%v", calls, err)
	}
}

func TestClusterBootValidatesEverySelectedProjectionBeforeAnyConnect(t *testing.T) {
	s, safe := bootProjection(t, "healthy")
	unsafe := safe
	unsafe.RequestID, unsafe.Config.RepositoryID, unsafe.Registration.ID, unsafe.Status = "other", "repo-other", "repo-other", "failed"
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{safe.RequestID: safe, unsafe.RequestID: unsafe}}); err != nil {
		t.Fatal(err)
	}
	old := connectStorageCluster
	defer func() { connectStorageCluster = old }()
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if err := ClusterStorageBootReadyFromDir(context.Background(), s.dir); err == nil || calls != 0 {
		t.Fatalf("invalid selected projection allowed an earlier login: calls=%d err=%v", calls, err)
	}
}

func TestClusterBootGateRefusesNoProjectionBeforeConnect(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	old := connectStorageCluster
	defer func() { connectStorageCluster = old }()
	calls := 0
	connectStorageCluster = func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
		calls++
		return protocol.StorageRegistration{}, nil
	}
	if ClusterStorageBootReadyFromDir(context.Background(), s.dir) == nil || calls != 0 {
		t.Fatal("invalid boot projection reached iSCSI connect")
	}
}

func TestClusterBootUnitRejectsForeignExecutableAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nex-core-local")
	if err := os.WriteFile(path, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := trustedClusterBootPath(path, true); err == nil {
		t.Fatal("non-root executable accepted")
	}
	for _, value := range []string{"relative", "/safe path", "/safe$path", "/safe\\path", "/safe%path"} {
		if _, err := clusterBootArg(value); err == nil {
			t.Fatalf("unsafe argument accepted: %q", value)
		}
	}
}

func TestBootUnitRefusesForeignContentAndSymlink(t *testing.T) {
	oldU, oldD, oldExec, oldOwner, oldReload := clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload
	t.Cleanup(func() {
		clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload = oldU, oldD, oldExec, oldOwner, oldReload
	})
	root := t.TempDir()
	clusterBootUnitPath, clusterBootDropinPath = filepath.Join(root, "unit"), filepath.Join(root, "dropin", "unit.conf")
	exe := filepath.Join(root, "nex-core-local")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	clusterBootExecutable = func() (string, error) { return exe, nil }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	clusterBootDaemonReload = func(context.Context) error { return nil }
	s, err := OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := os.WriteFile(clusterBootUnitPath, []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := InstallClusterStorageBootGate(s); err == nil {
		t.Fatal("foreign unit accepted")
	}
	if err := os.Remove(clusterBootUnitPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, clusterBootUnitPath); err != nil {
		t.Fatal(err)
	}
	if err := InstallClusterStorageBootGate(s); err == nil {
		t.Fatal("symlink unit accepted")
	}
}

func TestBootUnitPreflightsBothTargetsBeforeWriting(t *testing.T) {
	oldU, oldD, oldExec, oldOwner, oldReload := clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload
	t.Cleanup(func() {
		clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload = oldU, oldD, oldExec, oldOwner, oldReload
	})
	root := t.TempDir()
	clusterBootUnitPath, clusterBootDropinPath = filepath.Join(root, "unit"), filepath.Join(root, "dropin", "unit.conf")
	if err := os.Mkdir(filepath.Dir(clusterBootDropinPath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(clusterBootDropinPath, []byte("foreign"), 0644); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(root, "nex-core-local")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	clusterBootExecutable = func() (string, error) { return exe, nil }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	clusterBootDaemonReload = func(context.Context) error { return nil }
	s, err := OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = InstallClusterStorageBootGate(s); err == nil {
		t.Fatal("foreign second target accepted")
	}
	if _, err = os.Lstat(clusterBootUnitPath); !os.IsNotExist(err) {
		t.Fatalf("first target was written before second preflight: %v", err)
	}
}

func TestBootUnitRefusesUnsafeExistingParentBeforeCreatingDirectories(t *testing.T) {
	oldU, oldD, oldExec, oldOwner, oldReload := clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload
	t.Cleanup(func() {
		clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload = oldU, oldD, oldExec, oldOwner, oldReload
	})
	root := t.TempDir()
	clusterBootUnitPath = filepath.Join(root, "unit")
	unsafe, target := filepath.Join(root, "unsafe"), filepath.Join(root, "target")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, unsafe); err != nil {
		t.Fatal(err)
	}
	clusterBootDropinPath = filepath.Join(unsafe, "missing", "unit.conf")
	exe := filepath.Join(root, "nex-core-local")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	clusterBootExecutable = func() (string, error) { return exe, nil }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	clusterBootDaemonReload = func(context.Context) error { return nil }
	s, err := OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = InstallClusterStorageBootGate(s); err == nil {
		t.Fatal("symlink parent accepted")
	}
	if _, err = os.Lstat(clusterBootUnitPath); !os.IsNotExist(err) {
		t.Fatalf("unit written before unsafe parent rejection: %v", err)
	}
	if _, err = os.Lstat(filepath.Join(target, "missing")); !os.IsNotExist(err) {
		t.Fatalf("unsafe parent caused mkdir: %v", err)
	}
}

func TestBootUnitInstallsSafeContentAndBoundsReload(t *testing.T) {
	oldU, oldD, oldExec, oldOwner, oldReload := clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload
	t.Cleanup(func() {
		clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload = oldU, oldD, oldExec, oldOwner, oldReload
	})
	root := t.TempDir()
	clusterBootUnitPath, clusterBootDropinPath = filepath.Join(root, "unit"), filepath.Join(root, "dropin", "unit.conf")
	exe := filepath.Join(root, "nex-core-local")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	clusterBootExecutable = func() (string, error) { return exe, nil }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	reloads := 0
	clusterBootDaemonReload = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 {
			return errors.New("missing bounded daemon reload")
		}
		reloads++
		return nil
	}
	s, err := OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = InstallClusterStorageBootGate(s); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(clusterBootUnitPath)
	if err != nil {
		t.Fatal(err)
	}
	want := "ExecStart=" + exe + " cluster-storage-ready --state-dir=" + s.dir + "\n"
	if !strings.Contains(string(body), want) || reloads != 1 {
		t.Fatalf("unit=%q reloads=%d", body, reloads)
	}
	if err = InstallClusterStorageBootGate(s); err != nil {
		t.Fatal(err)
	}
	if reloads != 1 {
		t.Fatalf("unchanged unit caused daemon reloads=%d", reloads)
	}
}

func TestBootUnitRetriesReloadAfterPriorFailure(t *testing.T) {
	oldU, oldD, oldExec, oldOwner, oldReload, oldCache := clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload, clusterBootReloaded
	t.Cleanup(func() {
		clusterBootUnitPath, clusterBootDropinPath, clusterBootExecutable, clusterBootOwnership, clusterBootDaemonReload, clusterBootReloaded = oldU, oldD, oldExec, oldOwner, oldReload, oldCache
	})
	clusterBootReloaded = map[string]bool{}
	root := t.TempDir()
	clusterBootUnitPath, clusterBootDropinPath = filepath.Join(root, "unit"), filepath.Join(root, "dropin", "unit.conf")
	exe := filepath.Join(root, "nex-core-local")
	if err := os.WriteFile(exe, []byte("x"), 0755); err != nil {
		t.Fatal(err)
	}
	clusterBootExecutable = func() (string, error) { return exe, nil }
	clusterBootOwnership = func(os.FileInfo) bool { return true }
	attempts := 0
	clusterBootDaemonReload = func(context.Context) error {
		attempts++
		if attempts == 1 {
			return errors.New("fixture reload failed")
		}
		return nil
	}
	s, err := OpenStore(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = InstallClusterStorageBootGate(s); err == nil {
		t.Fatal("initial daemon reload failure was accepted")
	}
	if err = InstallClusterStorageBootGate(s); err != nil || attempts != 2 {
		t.Fatalf("same-content reload retry err=%v attempts=%d", err, attempts)
	}
}
