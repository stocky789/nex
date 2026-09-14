// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func TestPoolProbeUsesCrossProcessOFDLockFamily(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".nex-peer-lock")
	first, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := poolProbeOFDLockAt(first, syscall.F_WRLCK, 0); err != nil {
		t.Skipf("kernel/filesystem does not support OFD locks: %v", err)
	}
	defer poolProbeOFDLockAt(first, syscall.F_UNLCK, 0)
	if err := poolProbeOFDLockAt(second, syscall.F_WRLCK, 0); err == nil {
		t.Fatal("second open description acquired an OFD lock held by the first")
	}
	if err := poolProbeOFDLockAt(first, syscall.F_UNLCK, 0); err != nil {
		t.Fatal(err)
	}
	if err := poolProbeOFDLockAt(second, syscall.F_WRLCK, 0); err != nil {
		t.Fatalf("OFD lock did not recover after release: %v", err)
	}
	_ = poolProbeOFDLockAt(second, syscall.F_UNLCK, 0)
}

func TestPoolProbeOFDLockConflictsWithQEMUPOSIXLockFamily(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".nex-peer-lock")
	ofd, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer ofd.Close()
	posix, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer posix.Close()
	if err := poolProbeOFDLockAt(ofd, syscall.F_WRLCK, 7); err != nil {
		t.Skipf("kernel/filesystem does not support OFD locks: %v", err)
	}
	defer poolProbeOFDLockAt(ofd, syscall.F_UNLCK, 7)
	// QEMU falls back to POSIX byte-range locks on platforms without OFD support.
	// Linux requires those locks to conflict with an existing OFD lock.
	if err := syscall.FcntlFlock(posix.Fd(), syscall.F_SETLK, &syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 7, Len: 1}); err == nil {
		t.Fatal("POSIX image-style lock bypassed held OFD probe lock")
	}
}

func TestPoolProbeDistinctChallengesDoNotConflict(t *testing.T) {
	path := t.TempDir()
	firstChallenge, secondChallenge := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	firstOffset, err := poolProbeOffset(firstChallenge)
	if err != nil {
		t.Fatal(err)
	}
	secondOffset, err := poolProbeOffset(secondChallenge)
	if err != nil {
		t.Fatal(err)
	}
	if firstOffset == secondOffset || firstOffset == 0 || secondOffset == 0 {
		t.Fatalf("challenge offsets = %d, %d", firstOffset, secondOffset)
	}
	first := poolProbeLockKey("member-1", "repo-1", "1", firstChallenge, poolProbeModeChallengeByte)
	second := poolProbeLockKey("member-2", "repo-1", "1", secondChallenge, poolProbeModeChallengeByte)
	defer releasePoolProbeLock(first)
	defer releasePoolProbeLock(second)
	if err := holdPoolProbeLock(first, path, firstOffset); err != nil {
		t.Skipf("kernel/filesystem does not support OFD locks: %v", err)
	}
	if err := holdPoolProbeLock(second, path, secondOffset); err != nil {
		t.Fatalf("distinct peer challenges interfered through one probe byte: %v", err)
	}
	releasePoolProbeLock(first)
	third, err := os.OpenFile(filepath.Join(path, ".nex-peer-lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer third.Close()
	if err = poolProbeOFDLockAt(third, syscall.F_WRLCK, secondOffset); !poolProbeExpectedExclusion(err) {
		t.Fatalf("release of first challenge released the second challenge: %v", err)
	}
	releasePoolProbeLock(second)
	if err = poolProbeOFDLockAt(third, syscall.F_WRLCK, secondOffset); err != nil {
		t.Fatalf("second challenge did not recover after its release: %v", err)
	}
	_ = poolProbeOFDLockAt(third, syscall.F_UNLCK, secondOffset)
}

func TestPoolProbeConcurrentLeasesShareOneDescription(t *testing.T) {
	// BUG-0173: on SMB, closing one open description released a different
	// description's held range. Local filesystems keep per-description locks, so
	// assert the invariant directly: leases share one description that stays open
	// until the last range is released, and a held range cannot be leased twice.
	path := t.TempDir()
	defer releasePoolProbeLock("share-a")
	defer releasePoolProbeLock("share-b")
	if err := holdPoolProbeLock("share-a", path, 11); err != nil {
		t.Skipf("kernel/filesystem does not support OFD locks: %v", err)
	}
	if err := holdPoolProbeLock("share-b", path, 12); err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(path, ".nex-peer-lock")
	poolProbeLocks.Lock()
	shared, opened := poolProbeLocks.shared[name], len(poolProbeLocks.shared)
	poolProbeLocks.Unlock()
	if shared == nil || opened != 1 || len(shared.offsets) != 2 {
		t.Fatalf("concurrent leases opened %d descriptions, want one holding both ranges", opened)
	}
	if err := holdPoolProbeLock("share-dup", path, 12); err == nil {
		releasePoolProbeLock("share-dup")
		t.Fatal("second lease on an already held range was accepted")
	}
	releasePoolProbeLock("share-a")
	if _, err := shared.file.Stat(); err != nil {
		t.Fatalf("release of one lease closed the shared description: %v", err)
	}
	other, err := os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if err = poolProbeOFDLockAt(other, syscall.F_WRLCK, 12); !poolProbeExpectedExclusion(err) {
		t.Fatalf("remaining lease lost its range: %v", err)
	}
	releasePoolProbeLock("share-b")
	if _, err := shared.file.Stat(); err == nil {
		t.Fatal("description stayed open after its last lease was released")
	}
	if err = poolProbeOFDLockAt(other, syscall.F_WRLCK, 12); err != nil {
		t.Fatalf("range did not recover after the last release: %v", err)
	}
}

func TestPoolProbeLeaseCapRefusesNewLeaseUntilRelease(t *testing.T) {
	path := t.TempDir()
	const limit = 64
	keys := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		key := fmt.Sprintf("cap-%d", i)
		if err := holdPoolProbeLock(key, path, int64(i+1)); err != nil {
			t.Fatalf("lease %d = %v", i, err)
		}
		keys = append(keys, key)
	}
	defer func() {
		for _, key := range keys {
			releasePoolProbeLock(key)
		}
	}()
	if err := holdPoolProbeLock(keys[1], path, 2); err != nil {
		t.Fatalf("idempotent held lease was refused at capacity: %v", err)
	}
	if err := holdPoolProbeLock("cap-overflow", path, limit+1); err == nil {
		t.Fatal("unbounded new probe lease was accepted")
	}
	releasePoolProbeLock(keys[0])
	if err := holdPoolProbeLock("cap-overflow", path, limit+1); err != nil {
		t.Fatalf("released lease capacity did not recover: %v", err)
	}
	keys = append(keys, "cap-overflow")
}

func TestPoolProbeModeAndConflictValidationFailClosed(t *testing.T) {
	challenge := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	mode, offset, err := poolProbeModeOffset(challenge, poolProbeModeChallengeByte)
	if err != nil || mode != poolProbeModeChallengeByte || offset == 0 {
		t.Fatalf("challenge mode = %q, %d, %v", mode, offset, err)
	}
	mode, offset, err = poolProbeModeOffset(challenge, "")
	if err != nil || mode != poolProbeLegacyMode || offset != 0 {
		t.Fatalf("legacy mode = %q, %d, %v", mode, offset, err)
	}
	for _, bad := range []struct{ challenge, mode string }{{"", ""}, {"zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz", ""}, {challenge, "unknown"}} {
		if _, _, err := poolProbeModeOffset(bad.challenge, bad.mode); err == nil {
			t.Fatalf("invalid probe input accepted: %#v", bad)
		}
	}
	if !poolProbeExpectedExclusion(syscall.EAGAIN) || !poolProbeExpectedExclusion(syscall.EACCES) {
		t.Fatal("expected lock-conflict errno was rejected")
	}
	for _, err := range []error{syscall.EINTR, syscall.EINVAL, syscall.ENOLCK} {
		if poolProbeExpectedExclusion(err) {
			t.Fatalf("non-conflict errno accepted as peer exclusion: %v", err)
		}
	}
}

func writeAppliedPoolStorage(t *testing.T, s *Store, storage protocol.StorageRegistration) {
	t.Helper()
	dir := filepath.Join(s.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	intent := storage
	intent.Path = ""
	intent.Identity = protocol.StorageIdentity{}
	journal, err := json.Marshal(map[string]any{
		"command":         protocol.Command{OperationID: "storage-" + storage.ID, Kind: "storage-register", Storage: &intent},
		"result":          protocol.Result{OperationID: "storage-" + storage.ID, Status: "succeeded"},
		"applied_storage": storage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "storage-"+storage.ID+".json"), journal, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestMemberReconcileReadsStorageWithoutUnrelatedVMObservation(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Name: "shared", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/exports", FilesystemID: "nfs-fs-1"}}, Generation: 1}
	memberStorage := storage
	memberStorage.HostID = member.State.HostID
	writeAppliedPoolStorage(t, member, memberStorage)
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	accessPosts := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			transfer := storage
			transfer.HostID = member.State.HostID
			_ = json.NewEncoder(w).Encode(poolStorageTransfer{Storage: transfer})
		case http.MethodPost:
			accessPosts++
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(member)
	if err == nil {
		peer.MasterURL = server.URL
		err = savePoolPeer(member, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	unrelated := poolVMTestSpec("12345678-1234-4234-8234-123456789012", member.State.HostID)
	command := protocol.Command{OperationID: "unrelated-vm", Kind: "vm-create", VM: unrelated}
	if err := vmhost.ValidateManagedCommand(command); err != nil {
		t.Fatalf("unrelated VM fixture is invalid: %v", err)
	}
	vmJournal, err := json.Marshal(map[string]any{
		"command": command,
		"result":  protocol.Result{OperationID: "unrelated-vm", Status: "succeeded"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(member.dir, "managed-vms", "unrelated-vm.json"), vmJournal, 0600); err != nil {
		t.Fatal(err)
	}
	if _, checks := vmhost.NewManaged(filepath.Join(member.dir, "managed-vms")).AppliedStorage(); len(checks) != 1 || checks[0].Status != "healthy" {
		t.Fatalf("unrelated VM made the durable journal unreadable: %#v", checks)
	}
	marker := filepath.Join(t.TempDir(), "virsh-called")
	bin := t.TempDir()
	if err = os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\nprintf called > \"$NEX_TEST_VIRSH_MARKER\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEX_TEST_VIRSH_MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	oldValidate, oldLock := validatePoolStorageAccess, probePoolStorageLock
	t.Cleanup(func() { validatePoolStorageAccess, probePoolStorageLock = oldValidate, oldLock })
	validatePoolStorageAccess = func(_ context.Context, got protocol.StorageRegistration) (protocol.StorageRegistration, error) {
		return got, nil
	}
	probePoolStorageLock = func(context.Context, *Store, string, protocol.StorageRegistration) error { return nil }
	masterURL, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	if err = reconcilePoolStorageWithKnownCluster(context.Background(), member, masterURL, storage.ID); err != nil {
		t.Fatalf("storage reconciliation = %v", err)
	}
	if accessPosts != 1 {
		t.Fatalf("member access posts = %d, want 1", accessPosts)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("storage-only reconciliation observed unrelated VM: %v", err)
	}
}

func TestCrossCoreLockProbeSeparatesConcurrentAuthenticatedChallenges(t *testing.T) {
	master, first, second := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-1"), peerStore(t, "member-2", "other-2")
	joinPeerMember(t, master, first)
	joinPeerMember(t, master, second)
	path := t.TempDir()
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Generation: 1, Path: path, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/repo"}}}
	writeAppliedPoolStorage(t, master, storage)
	if applied, _ := vmhost.NewManaged(filepath.Join(master.dir, "managed-vms")).Applied(context.Background()); len(applied.Storage) != 1 {
		t.Fatalf("master storage fixture = %#v", applied.Storage)
	}
	firstURL, err := PoolMasterURL(first)
	if err != nil {
		t.Fatal(err)
	}
	secondURL, err := PoolMasterURL(second)
	if err != nil {
		t.Fatal(err)
	}
	firstClient, err := PoolPeerClient(first, firstURL)
	if err != nil {
		t.Fatal(err)
	}
	secondClient, err := PoolPeerClient(second, secondURL)
	if err != nil {
		t.Fatal(err)
	}
	defer firstClient.CloseIdleConnections()
	defer secondClient.CloseIdleConnections()
	challengeA, challengeB := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	post := func(client *http.Client, masterURL, challenge, generation string) string {
		t.Helper()
		lockURL := masterURL + poolStoragePath + storage.ID + "/lock?generation=" + generation + "&challenge=" + challenge + "&probe_mode=" + poolProbeModeChallengeByte
		req, err := http.NewRequest(http.MethodPost, lockURL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent || resp.Header.Get(poolProbeModeHeader) != poolProbeModeChallengeByte {
			t.Fatalf("authenticated challenge hold = %d mode=%q", resp.StatusCode, resp.Header.Get(poolProbeModeHeader))
		}
		return lockURL
	}
	firstLockURL := post(firstClient, firstURL, challengeA, "1")
	secondLockURL := post(secondClient, secondURL, challengeB, "1")
	defer func() {
		_ = releasePoolProbeLease(firstClient, firstLockURL)
		_ = releasePoolProbeLease(secondClient, secondLockURL)
	}()
	firstOffset, _ := poolProbeOffset(challengeA)
	secondOffset, _ := poolProbeOffset(challengeB)
	local, err := os.OpenFile(filepath.Join(path, ".nex-peer-lock"), os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	if err = poolProbeOFDLockAt(local, syscall.F_WRLCK, firstOffset); !poolProbeExpectedExclusion(err) {
		t.Fatalf("first held challenge did not exclude its member: %v", err)
	}
	if err = poolProbeOFDLockAt(local, syscall.F_WRLCK, secondOffset); !poolProbeExpectedExclusion(err) {
		t.Fatalf("second held challenge did not exclude its member: %v", err)
	}
	// A different authenticated member knows the URL but cannot release this member's lease.
	req, err := http.NewRequest(http.MethodDelete, firstLockURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := secondClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("cross-member delete status = %d", resp.StatusCode)
	}
	if err = poolProbeOFDLockAt(local, syscall.F_WRLCK, firstOffset); !poolProbeExpectedExclusion(err) {
		t.Fatalf("cross-member delete released another member's lease: %v", err)
	}
	if err = releasePoolProbeLease(firstClient, firstLockURL); err != nil {
		t.Fatal(err)
	}
	if err = poolProbeOFDLockAt(local, syscall.F_WRLCK, firstOffset); err != nil {
		t.Fatalf("first challenge did not recover after exact release: %v", err)
	}
	_ = poolProbeOFDLockAt(local, syscall.F_UNLCK, firstOffset)
	// The handler rejects both a changed generation and malformed challenge before holding a lease.
	for _, raw := range []string{firstURL + poolStoragePath + storage.ID + "/lock?generation=2&challenge=" + challengeA + "&probe_mode=" + poolProbeModeChallengeByte, firstURL + poolStoragePath + storage.ID + "/lock?generation=1&challenge=not-hex&probe_mode=" + poolProbeModeChallengeByte} {
		req, err = http.NewRequest(http.MethodPost, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err = firstClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusNoContent {
			t.Fatalf("invalid authenticated lock request was accepted: %s", raw)
		}
	}
	secondStorage := storage
	secondStorage.HostID = second.State.HostID
	if err = crossCoreLockProbe(context.Background(), second, secondURL, secondStorage); err != nil {
		t.Fatalf("full client probe did not recover after concurrent lease release: %v", err)
	}
}

func TestCrossCoreLockProbeUsesLegacyByteZeroWhenMasterOmitsModeEcho(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-1")
	joinPeerMember(t, master, member)
	path := t.TempDir()
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Generation: 1, Path: path, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/repo"}}}
	legacyKey := "legacy-probe"
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if err := holdPoolProbeLock(legacyKey, path, 0); err != nil {
				http.Error(w, "lock unavailable", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent) // Legacy masters do not echo a probe mode.
		case http.MethodDelete:
			releasePoolProbeLock(legacyKey)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(member)
	if err == nil {
		peer.MasterURL = server.URL
		err = savePoolPeer(member, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = crossCoreLockProbe(context.Background(), member, server.URL, storage); err != nil {
		t.Fatalf("new member did not safely use old master byte-zero lease: %v", err)
	}
}

func TestCrossCoreLockProbeRejectsUnexpectedModeEchoAndReleasesLease(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-1")
	joinPeerMember(t, master, member)
	path := t.TempDir()
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: member.State.HostID, Generation: 1, Path: path, Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/repo"}}}
	var released atomic.Bool
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if err := holdPoolProbeLock("unexpected-mode", path, 0); err != nil {
				http.Error(w, "lock unavailable", http.StatusConflict)
				return
			}
			w.Header().Set(poolProbeModeHeader, "unexpected")
			w.WriteHeader(http.StatusNoContent)
		case http.MethodDelete:
			releasePoolProbeLock("unexpected-mode")
			released.Store(true)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(member)
	if err == nil {
		peer.MasterURL = server.URL
		err = savePoolPeer(member, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if err = crossCoreLockProbe(context.Background(), member, server.URL, storage); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unexpected mode echo = %v", err)
	}
	if !released.Load() {
		t.Fatal("unexpected mode echo left the acquired peer lease held")
	}
}

func TestPoolStorageMutationFenceRejectsReservation(t *testing.T) {
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []protocol.PoolMember{
		{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 2, Status: "ready"},
		{HostID: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 2, Status: "ready"},
	}}
	state := poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}
	state.Reservations["repo-1"] = map[string]string{"member-1": "vm-1"}
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "update-1"); err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("durable member reservation did not fence mutation: %v", err)
	}
	delete(state.Reservations, "repo-1")
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "update-1"); err != nil {
		t.Fatalf("drained repository could not acquire mutation fence: %v", err)
	}
	if state.Mutations["repo-1"] != "update-1" {
		t.Fatalf("mutation was not retained: %#v", state.Mutations)
	}
}

func TestPoolStorageEligibilityRejectsExclusiveAndOutOfProfileClusteredISCSI(t *testing.T) {
	clustered := protocol.StorageRegistration{Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{"master-1", "member-1", "member-2"}}}}
	if !poolStorageEligibleForMember(clustered, "member-1") {
		t.Fatal("eligible clustered iSCSI member was rejected")
	}
	if poolStorageEligibleForMember(clustered, "member-3") {
		t.Fatal("ineligible clustered iSCSI member was accepted")
	}
	exclusive := clustered
	exclusive.Source.ISCSI = &protocol.ISCSIStorageSource{ExclusiveTarget: true}
	if poolStorageEligibleForMember(exclusive, "member-1") {
		t.Fatal("exclusive iSCSI was admitted to pool reconciliation")
	}
}

func TestPoolReservationKeepsLiveOperationWhenLaterOperationFails(t *testing.T) {
	state := poolStorageState{Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}
	if !recordPoolStorageReservation(&state, "repo-1", "member-1", "vm-live") {
		t.Fatal("initial reservation was not recorded")
	}
	if recordPoolStorageReservation(&state, "repo-1", "member-1", "vm-failed") {
		t.Fatal("later operation replaced the live reservation")
	}
	// This is the master DELETE rule for a failed second operation: only its own
	// reservation may be deleted, so vm-live remains durably fenced.
	if state.Reservations["repo-1"]["member-1"] == "vm-failed" {
		delete(state.Reservations["repo-1"], "member-1")
	}
	if state.Reservations["repo-1"]["member-1"] != "vm-live" {
		t.Fatalf("live reservation lost: %#v", state.Reservations)
	}
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "detach"); err == nil {
		t.Fatal("mutation ignored surviving live reservation")
	}
}

func TestPoolMasterDoesNotReconcileItsOwnStorage(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := ReconcilePoolStorageFromMaster(context.Background(), s); err != nil {
		t.Fatalf("master reconcile was not a no-op: %v", err)
	}
}

func TestPoolStorageReconcileGivesLaterRepositoryFreshDeadline(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	previous := reconcilePoolStorage
	t.Cleanup(func() { reconcilePoolStorage = previous })
	var calls []string
	reconcilePoolStorage = func(ctx context.Context, _ *Store, _ string, id string) error {
		calls = append(calls, id)
		if id == "first" {
			<-ctx.Done()
			return ctx.Err()
		}
		if ctx.Err() != nil {
			t.Fatalf("later repository inherited expired context: %v", ctx.Err())
		}
		return nil
	}
	storage := []protocol.StorageRegistration{
		{ID: "first", PoolID: "pool-1"},
		{ID: "later", PoolID: "pool-1"},
	}
	if _, err := reconcilePoolStorageEntries(context.Background(), s, "https://master.example.test", storage, 10*time.Millisecond); err == nil {
		t.Fatal("first deadline failure was hidden")
	}
	if got, want := strings.Join(calls, ","), "first,later"; got != want {
		t.Fatalf("later repository was not reconciled after earlier deadline: got %q want %q", got, want)
	}
}

func TestPeriodicPoolStorageReconcileSkipsDuplicateClusterRefresh(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Kind: protocol.StorageKindISCSI, Generation: 1, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{master.State.HostID, member.State.HostID}}}}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(poolStorageTransfer{Storage: storage})
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(member)
	if err == nil {
		peer.MasterURL = server.URL
		err = savePoolPeer(member, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	previous := reconcileStorageClusters
	t.Cleanup(func() { reconcileStorageClusters = previous })
	calls := 0
	reconcileStorageClusters = func(context.Context, *Store) error { calls++; return nil }
	masterURL, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reconcilePoolStorageEntries(context.Background(), member, masterURL, []protocol.StorageRegistration{storage}, time.Second); err == nil || !strings.Contains(err.Error(), "clustered iSCSI configuration is unavailable") {
		t.Fatalf("periodic reconciliation result = %v", err)
	}
	if calls != 0 {
		t.Fatalf("periodic reconciliation repeated cluster setup %d times", calls)
	}
	if err = ReconcilePoolStorage(context.Background(), member, masterURL, storage.ID); err == nil || !strings.Contains(err.Error(), "clustered iSCSI configuration is unavailable") {
		t.Fatalf("explicit reconciliation result = %v", err)
	}
	if calls != 1 {
		t.Fatalf("explicit reconciliation did not refresh the cluster projection: %d", calls)
	}
}

func TestPeriodicClusteredStorageRequiresCurrentDurableConfig(t *testing.T) {
	s, op := bootProjection(t, "healthy")
	s.State.HostID = "member-1"
	op.RequestID, op.Digest = "periodic-current", "durable-periodic-config"
	op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID, Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: op.MembershipGeneration, CommittedAt: time.Now().UTC()}
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	got, err := StorageClusterConfigForRepository(s, op.Config.RepositoryID)
	if err != nil || got.Digest() != op.Config.Digest() {
		t.Fatalf("current durable config = %#v, %v", got, err)
	}
	stale := op
	stale.Config.Generation++
	if err := saveStorageClusters(s, storageClusterState{Operations: map[string]storageClusterOperation{stale.RequestID: stale}}); err != nil {
		t.Fatal(err)
	}
	if _, err := StorageClusterConfigForRepository(s, stale.Config.RepositoryID); err == nil {
		t.Fatal("stale durable cluster config was accepted by periodic reconciliation")
	}
}

func TestPeriodicClusteredStorageUsesCurrentConfigBeforeValidation(t *testing.T) {
	master, member, other := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-1"), peerStore(t, "member-2", "other-2")
	joinPeerMember(t, master, other)
	joinPeerMember(t, master, member)
	_, op := bootProjection(t, "healthy")
	op.RequestID, op.Digest = "periodic-live", "durable-periodic-live"
	membership, err := Membership(member)
	if err != nil {
		t.Fatal(err)
	}
	op.MembershipGeneration = membership.Generation
	op.Activation = &storageClusterActivation{RequestID: op.RequestID, OperationDigest: op.Digest, RepositoryID: op.Config.RepositoryID, Generation: op.Config.Generation, ConfigDigest: op.Config.Digest(), MembershipGeneration: op.MembershipGeneration, CommittedAt: time.Now().UTC()}
	if err := saveStorageClusters(member, storageClusterState{Operations: map[string]storageClusterOperation{op.RequestID: op}}); err != nil {
		t.Fatal(err)
	}
	if _, err := StorageClusterConfigForRepository(member, op.Config.RepositoryID); err != nil {
		t.Fatalf("current member config unavailable: %v", err)
	}
	local := op.Registration
	local.HostID = member.State.HostID
	dir := filepath.Join(member.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := vmhost.CommitClusteredStorageRegistration(context.Background(), dir, local); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(poolStorageTransfer{Storage: op.Registration})
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(member)
	if err == nil {
		peer.MasterURL = server.URL
		err = savePoolPeer(member, peer)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	oldValidate, oldLock := validatePoolStorageAccess, probePoolStorageLock
	t.Cleanup(func() { validatePoolStorageAccess, probePoolStorageLock = oldValidate, oldLock })
	validations := 0
	validatePoolStorageAccess = func(ctx context.Context, got protocol.StorageRegistration) (protocol.StorageRegistration, error) {
		validations++
		if config, ok := vmhost.StorageClusterConfig(ctx); !ok || config.Digest() != op.Config.Digest() {
			t.Fatal("periodic validation did not receive the current durable config")
		}
		return got, nil
	}
	probePoolStorageLock = func(context.Context, *Store, string, protocol.StorageRegistration) error { return nil }
	masterURL, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = reconcilePoolStorageEntries(context.Background(), member, masterURL, []protocol.StorageRegistration{op.Registration}, time.Second); err != nil {
		t.Fatalf("current periodic config did not reach validation: %v", err)
	}
	stale := op
	stale.Config.Generation++
	if err = saveStorageClusters(member, storageClusterState{Operations: map[string]storageClusterOperation{stale.RequestID: stale}}); err != nil {
		t.Fatal(err)
	}
	if _, err = reconcilePoolStorageEntries(context.Background(), member, masterURL, []protocol.StorageRegistration{op.Registration}, time.Second); err == nil {
		t.Fatal("periodic reconciliation accepted stale durable config")
	}
	if validations != 1 {
		t.Fatalf("stale config reached native validation %d times", validations)
	}
}

func TestMemberReconcilePostsLaterAccessAfterEarlierDeadline(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storages := []protocol.StorageRegistration{
		{ID: "first", PoolID: "pool-1", HostID: "master-1", Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/first"}}},
		{ID: "later", PoolID: "pool-1", HostID: "master-1", Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/later"}}},
	}
	dir := filepath.Join(master.dir, "managed-vms")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, storage := range storages {
		journal, err := json.Marshal(map[string]any{"command": protocol.Command{OperationID: "storage-" + storage.ID, Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "storage-" + storage.ID, Status: "succeeded"}, "applied_storage": storage})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "storage-"+storage.ID+".json"), journal, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(poolStorageFile(master)); !os.IsNotExist(err) {
		t.Fatalf("access heartbeat unexpectedly created durable state: %v", err)
	}
	previous := reconcilePoolStorage
	t.Cleanup(func() { reconcilePoolStorage = previous })
	var firstPost error
	reconcilePoolStorage = func(ctx context.Context, s *Store, masterURL, id string) error {
		access := protocol.PoolStorageAccess{RepositoryID: id, HostID: s.State.HostID, Generation: 1, Identity: "observed", Readable: true, Writable: true, QEMUWritable: true, Locking: true}
		if id == "first" {
			<-ctx.Done()
			firstPost = postPoolStorageAccess(ctx, s, masterURL, id, access)
			return nil // A late native call may appear successful after cancellation.
		}
		return postPoolStorageAccess(ctx, s, masterURL, id, access)
	}
	// The budget also covers the master list and each TLS POST; 10ms expired those under CI load.
	if err := reconcilePoolStorageFromMaster(context.Background(), member, time.Second); err == nil {
		t.Fatal("first repository deadline failure was hidden")
	}
	if firstPost == nil {
		t.Fatal("expired first repository published healthy member access")
	}
	accesses := poolStorageAccess(master)
	if first := accesses["first"][member.State.HostID]; first.HostID != "" {
		t.Fatalf("expired first repository published access: %#v", first)
	}
	later := accesses["later"][member.State.HostID]
	if !later.Readable || !later.Writable || !later.QEMUWritable || !later.Locking || later.Error != "" || later.ObservedAt.IsZero() {
		t.Fatalf("later repository did not post fresh healthy access: %#v", later)
	}
	if _, err := os.Stat(poolStorageFile(master)); !os.IsNotExist(err) {
		t.Fatalf("authenticated access heartbeat wrote durable state: %v", err)
	}
	reported := PoolStorageMemberAccess(master, storages)
	if len(reported[1].MemberAccess) != 1 || reported[1].MemberAccess[0].HostID != member.State.HostID || reported[1].MemberAccess[0].Error != "" {
		t.Fatalf("current authenticated access was not reported: %#v", reported[1].MemberAccess)
	}
	masterURL, err := PoolMasterURL(member)
	if err != nil {
		t.Fatal(err)
	}
	if err := postPoolStorageAccess(context.Background(), member, masterURL, "later", protocol.PoolStorageAccess{RepositoryID: "later", HostID: member.State.HostID, Generation: 1, Error: "native validation failed"}); err != nil {
		t.Fatal(err)
	}
	current := poolStorageAccess(master)["later"][member.State.HostID]
	if current.Error == "" || current.Readable || current.Writable || current.QEMUWritable || current.Locking {
		t.Fatalf("current error did not replace healthy access: %#v", current)
	}
	if err := os.WriteFile(poolStorageFile(master), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := postPoolStorageAccess(context.Background(), member, masterURL, "later", protocol.PoolStorageAccess{RepositoryID: "later", HostID: member.State.HostID, Generation: 1, Identity: "new-healthy", Readable: true, Writable: true, QEMUWritable: true, Locking: true}); err == nil {
		t.Fatal("authenticated access accepted corrupt durable state")
	}
	if got := poolStorageAccess(master)["later"][member.State.HostID]; got.HostID != "" {
		t.Fatalf("durable-state failure retained an old observation: %#v", got)
	}
	reported = PoolStorageMemberAccess(master, storages)
	if len(reported[1].MemberAccess) != 1 || reported[1].MemberAccess[0].Error != "awaiting member reconciliation" {
		t.Fatalf("corrupt durable state exposed cached health: %#v", reported[1].MemberAccess)
	}
	if err := savePoolStorage(master, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	reported = PoolStorageMemberAccess(master, storages)
	if len(reported[1].MemberAccess) != 1 || reported[1].MemberAccess[0].Error != "awaiting member reconciliation" {
		t.Fatalf("repair resurrected an old observation without a new access report: %#v", reported[1].MemberAccess)
	}
	if err := postPoolStorageAccess(context.Background(), member, masterURL, "later", protocol.PoolStorageAccess{RepositoryID: "later", HostID: member.State.HostID, Generation: 1, Identity: "fresh-healthy", Readable: true, Writable: true, QEMUWritable: true, Locking: true}); err != nil {
		t.Fatal(err)
	}
	reported = PoolStorageMemberAccess(master, storages)
	if len(reported[1].MemberAccess) != 1 || reported[1].MemberAccess[0].Identity != "fresh-healthy" || reported[1].MemberAccess[0].Error != "" {
		t.Fatalf("new authenticated access was not projected after repair: %#v", reported[1].MemberAccess)
	}
}

func TestPoolStorageAccessNeverRehydratesLegacyHealthyRecord(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	legacy := poolStorageState{
		Storage:      map[string]protocol.StorageRegistration{},
		Access:       map[string]map[string]protocol.PoolStorageAccess{"repo-1": {member.State.HostID: {RepositoryID: "repo-1", HostID: member.State.HostID, Generation: 1, Identity: "old", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().UTC()}}},
		Reservations: map[string]map[string]string{"repo-1": {member.State.HostID: "live-operation"}},
		Mutations:    map[string]string{"repo-2": "pending-mutation"},
	}
	// Write a historical file directly: savePoolStorage intentionally strips access.
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(poolStorageFile(master), b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = master.Save(); err != nil {
		t.Fatal(err)
	}
	if err = master.Close(); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenStore(master.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Close()
	state, err := loadPoolStorage(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Access) != 0 || state.Reservations["repo-1"][member.State.HostID] != "live-operation" || state.Mutations["repo-2"] != "pending-mutation" {
		t.Fatalf("fresh state did not discard only legacy access: %#v", state)
	}
	storage := []protocol.StorageRegistration{{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/repo"}}, MemberAccess: []protocol.PoolStorageAccess{{RepositoryID: "repo-1", HostID: member.State.HostID, Generation: 1, Identity: "historical-healthy", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: time.Now().UTC()}}}}
	reported := PoolStorageMemberAccess(fresh, storage)
	if len(reported[0].MemberAccess) != 1 || reported[0].MemberAccess[0].HostID != member.State.HostID || reported[0].MemberAccess[0].Error != "awaiting member reconciliation" {
		t.Fatalf("fresh Core reported legacy healthy access: %#v", reported[0].MemberAccess)
	}
	if storage[0].MemberAccess[0].Identity != "historical-healthy" {
		t.Fatalf("report projection mutated caller-owned storage: %#v", storage[0].MemberAccess)
	}
}

func TestPoolStorageCorruptDurableStateIsNotMaskedByVolatileAccess(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	if err := os.WriteFile(poolStorageFile(s), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadPoolStorage(s); err == nil {
		t.Fatal("corrupt durable pool storage state was accepted")
	}
	before, err := os.ReadFile(poolStorageFile(s))
	if err != nil {
		t.Fatal(err)
	}
	_ = PoolStorageMemberAccess(s, []protocol.StorageRegistration{{ID: "repo-1", PoolID: "pool-1", Kind: protocol.StorageKindNFS}})
	after, err := os.ReadFile(poolStorageFile(s))
	if err != nil || string(after) != string(before) {
		t.Fatalf("volatile observation masked or rewrote corrupt durable state: %q, %v", after, err)
	}
}

func TestPoolStorageReconcileDoesNotStartLaterRepositoryAfterShutdown(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	previous := reconcilePoolStorage
	t.Cleanup(func() { reconcilePoolStorage = previous })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls []string
	reconcilePoolStorage = func(_ context.Context, _ *Store, _ string, id string) error {
		calls = append(calls, id)
		cancel()
		return context.Canceled
	}
	storage := []protocol.StorageRegistration{{ID: "first", PoolID: "pool-1"}, {ID: "later", PoolID: "pool-1"}}
	if _, err := reconcilePoolStorageEntries(ctx, s, "https://master.example.test", storage, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("shutdown cancellation was not returned: %v", err)
	}
	if got, want := strings.Join(calls, ","), "first"; got != want {
		t.Fatalf("reconciliation started a later repository after shutdown: got %q want %q", got, want)
	}
}

func TestMemberReconcileCheckReportsFailureThenFreshRecovery(t *testing.T) {
	s := peerStore(t, "member-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}, {HostID: "member-1", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "unknown" {
		t.Fatalf("initial check = %#v", check)
	}
	recordPoolReconcile(s, poolReconcileStage("master-list", errors.New("transport failed: secret-marker")))
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "failed" || check.Error != "pool storage reconciliation master-list failed" || strings.Contains(check.Error, "secret-marker") {
		t.Fatalf("failure check = %#v", check)
	}
	recordPoolReconcile(s, poolReconcileStage("repository", context.DeadlineExceeded))
	if check := PoolStorageReconcileCheck(s); check == nil || check.Error != "pool storage reconciliation repository deadline" {
		t.Fatalf("deadline check = %#v", check)
	}
	recordPoolReconcile(s, poolReconcileStage("secret-stage", errors.New("secret-marker")))
	if check := PoolStorageReconcileCheck(s); check == nil || check.Error != "pool storage reconciliation observation failed" || strings.Contains(check.Error, "secret") {
		t.Fatalf("allowlisted stage check = %#v", check)
	}
	recordPoolReconcile(s, nil)
	if check := PoolStorageReconcileCheck(s); check == nil || check.Status != "healthy" {
		t.Fatalf("recovery check = %#v", check)
	}
}

func TestPoolStorageReconcileSourceKeepsLocalAndOrphanStageTags(t *testing.T) {
	// Source-bound coverage for the two native/managed boundaries: exercising
	// them would require a real Applied timeout and detach command. This catches
	// removal of the safe stage tags while check behavior is covered above.
	source, err := os.ReadFile("pool_storage.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"result = poolReconcileStage(\"local-observation\", err)",
		"result = poolReconcileStage(\"orphan-detach\", err)",
		"last = poolReconcileStage(\"orphan-detach\", deadlineErr)",
	} {
		if !strings.Contains(string(source), want) {
			t.Fatalf("source-bound reconciliation stage tag missing: %q", want)
		}
	}
}

func TestPoolStorageOperationIsManagedCommandIdentifier(t *testing.T) {
	operation := poolStorageOperation("apply", "repo-1", 7)
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`).MatchString(operation) {
		t.Fatalf("invalid generated operation ID %q", operation)
	}
}

func TestUnusedReservationSelectionWaitsForJournalReferencesToDisappear(t *testing.T) {
	managed := &protocol.ManagedState{Storage: []protocol.StorageRegistration{{ID: "repo-1", Kind: protocol.StorageKindNFS}}, VMs: []protocol.VMCreateSpec{{ID: "vm-1", Disks: []protocol.VMDisk{{StorageID: "repo-1"}}}}}
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 0 {
		t.Fatalf("live disk released reservation: %v %v", ids, safe)
	}
	managed.VMs[0].Disks = nil // Durable vm-update removed the last disk.
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 1 || ids[0] != "repo-1" {
		t.Fatalf("removed disk did not release: %v %v", ids, safe)
	}
	managed.VMs = []protocol.VMCreateSpec{{ID: "vm-1", MediaID: "iso-1"}}
	managed.Media = []protocol.Media{{ID: "iso-1", StorageID: "repo-1"}}
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 0 {
		t.Fatalf("attached media released reservation: %v %v", ids, safe)
	}
	managed.VMs[0].MediaID = "" // Durable media-eject no longer refers to the ISO.
	managed.Media = nil
	if ids, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "healthy"}}); !safe || len(ids) != 1 {
		t.Fatalf("ejected media did not release: %v %v", ids, safe)
	}
	if _, safe := poolStorageUnusedIDs(managed, []protocol.Check{{Status: "unknown"}}); safe {
		t.Fatal("unhealthy journal observation released a reservation")
	}
}

func TestPoolReservationReleaseUsesAuthenticatedPeerDelete(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	if _, _, err = CreatePoolJoinOffer(master, "https://master.example.test", member.State.HostID, csr); err != nil {
		t.Fatal(err)
	}
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "member-1", Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}, Generation: 1}
	journal := map[string]any{"command": protocol.Command{OperationID: "memberstorage1", Kind: "storage-register", Storage: &storage}, "result": protocol.Result{OperationID: "memberstorage1", Status: "succeeded"}, "applied_storage": storage}
	b, err := json.Marshal(journal)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(member.dir, "managed-vms")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "memberstorage1.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	vmID := "12345678-1234-4234-8234-123456789012"
	if err = os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\necho "+vmID+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	vm := protocol.VMCreateSpec{ID: vmID, PoolID: "pool-1", HostID: "member-1", Disks: []protocol.VMDisk{{StorageID: "repo-1", Name: "disk", SizeGiB: 1}}}
	vmJournal := map[string]any{"command": protocol.Command{OperationID: "membervm001", Kind: "vm-create", VM: &vm}, "result": protocol.Result{OperationID: "membervm001", Status: "succeeded"}}
	b, err = json.Marshal(vmJournal)
	if err != nil {
		t.Fatal(err)
	}
	vmPath := filepath.Join(dir, "membervm001.json")
	if err = os.WriteFile(vmPath, b, 0600); err != nil {
		t.Fatal(err)
	}
	if err = savePoolStorage(master, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{"repo-1": {"member-1": "vm-live"}}, Mutations: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	// A durable VM disk reference suppresses the authenticated release.
	ReleaseUnusedPoolStorageReservations(context.Background(), member)
	state, err := loadPoolStorage(master)
	if err != nil {
		t.Fatal(err)
	}
	if state.Reservations["repo-1"]["member-1"] != "vm-live" {
		t.Fatalf("live journal reference was released: %#v", state.Reservations)
	}
	if err = os.Remove(vmPath); err != nil {
		t.Fatal(err)
	}
	// Actual durable journal projection plus the mTLS peer DELETE—not a helper-only path.
	ReleaseUnusedPoolStorageReservations(context.Background(), member)
	state, err = loadPoolStorage(master)
	if err != nil {
		t.Fatal(err)
	}
	if state.Reservations["repo-1"]["member-1"] != "" {
		t.Fatalf("mTLS release did not remove member reservation: %#v", state.Reservations)
	}
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	if err = beginPoolStorageMutationState(&state, membership, "master-1", "repo-1", "detach-1"); err != nil {
		t.Fatalf("released reservation still fenced master mutation: %v", err)
	}
}

func TestPoolMemberStorageIntentStripsMasterObservationsForEngineValidation(t *testing.T) {
	master := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Kind: protocol.StorageKindNFS, Generation: 1, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/export"}}, Path: "/var/lib/nex-storage/master", Identity: protocol.StorageIdentity{NFS: &protocol.NFSStorageIdentity{Server: "nfs.example.test", Export: "/export"}}, Access: protocol.StorageAccess{Status: "healthy"}}
	if err := protocol.ValidateStorageRegistration(master, false); err == nil {
		t.Fatal("master observation unexpectedly accepted as member command")
	}
	member := poolMemberStorageIntent(master)
	member.HostID = "member-1"
	if err := protocol.ValidateStorageRegistration(member, false); err != nil {
		t.Fatalf("client-shaped member intent rejected: %v", err)
	}
	if member.Path != "" || member.Identity.NFS != nil || member.Access.Status != "" {
		t.Fatalf("master observation leaked into intent: %#v", member)
	}
}

func TestPoolMemberStorageIntentLetsCoreResolveClusteredISCSIMount(t *testing.T) {
	master := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "master-1", Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{MountPath: "/var/lib/nex-storage/repo-1", Mode: "clustered_gfs2"}}}
	member := poolMemberStorageIntent(master)
	if member.Source.ISCSI.MountPath != "" {
		t.Fatalf("master iSCSI mount path leaked into member command: %#v", member.Source.ISCSI)
	}
}

func TestReportProjectsCoreMembershipForControllerAdoption(t *testing.T) {
	reports := make(chan protocol.ReportRequest, 1)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports <- request
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	s.State.PoolID = "pool-1"
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: identity.HostID, Generation: 1, Members: []protocol.PoolMember{{HostID: identity.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()}}}
	if err := savePoolPeer(s, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
		t.Fatal(err)
	}
	request := <-reports
	if request.Inventory.Managed == nil || request.Inventory.Managed.Membership == nil || request.Inventory.Managed.Membership.PoolID != "pool-1" {
		t.Fatalf("membership was not projected: %#v", request.Inventory.Managed)
	}
}

func TestPoolStorageMutationFenceRetainsUncertainOutcome(t *testing.T) {
	s := peerStore(t, "master-1", "pool-1")
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "master-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(s, poolPeerState{CAKeyPEM: "master-key", Membership: membership}); err != nil {
		t.Fatal(err)
	}
	if err := savePoolStorage(s, poolStorageState{Storage: map[string]protocol.StorageRegistration{}, Access: map[string]map[string]protocol.PoolStorageAccess{}, Reservations: map[string]map[string]string{}, Mutations: map[string]string{"repo-1": "detach-1"}}); err != nil {
		t.Fatal(err)
	}
	finishPoolStorageMutation(s, "repo-1", "detach-1", false)
	state, err := loadPoolStorage(s)
	if err != nil || state.Mutations["repo-1"] != "detach-1" {
		t.Fatalf("uncertain outcome cleared fence: %#v %v", state.Mutations, err)
	}
	finishPoolStorageMutation(s, "repo-1", "detach-1", true)
	state, err = loadPoolStorage(s)
	if err != nil || state.Mutations["repo-1"] != "" {
		t.Fatalf("definitive outcome retained fence: %#v %v", state.Mutations, err)
	}
}

func TestPoolStoragePeerPeriodicHandlersUseStorageProjectionOnly(t *testing.T) {
	master, member := peerStore(t, "master-1", "pool-1"), peerStore(t, "member-1", "other-pool")
	joinPeerMember(t, master, member)
	storage := protocol.StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: master.State.HostID, Name: "shared", Path: t.TempDir(), Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/exports"}}, Generation: 1}
	writeAppliedPoolStorage(t, master, storage)
	// A successful detach remains a tombstone even when unrelated readable journal
	// entries are present; peer list and transfer must never resurrect it.
	tombstone := protocol.Command{OperationID: "detach-old", Kind: "storage-detach", StorageLifecycle: &protocol.StorageLifecycleRequest{Action: "detach", RepositoryID: "old-repo", PoolID: master.State.PoolID, HostID: master.State.HostID, ExpectedGeneration: 1}}
	if err := vmhost.ValidateManagedCommand(tombstone); err != nil {
		t.Fatalf("tombstone fixture is invalid: %v", err)
	}
	tombstoneJournal, err := json.Marshal(map[string]any{"command": tombstone, "result": protocol.Result{OperationID: tombstone.OperationID, Status: "succeeded"}})
	if err != nil {
		t.Fatal(err)
	}
	managedDir := filepath.Join(master.dir, "managed-vms")
	if err = os.WriteFile(filepath.Join(managedDir, tombstone.OperationID+".json"), tombstoneJournal, 0600); err != nil {
		t.Fatal(err)
	}
	// Full Applied observes unrelated VM state. These peer handlers need only the
	// durable storage projection, so an otherwise valid VM journal must not make
	// a periodic peer request invoke virsh.
	unrelated := poolVMTestSpec("12345678-1234-4234-8234-123456789012", master.State.HostID)
	vmCommand := protocol.Command{OperationID: "unrelated-vm", Kind: "vm-create", VM: unrelated}
	if err = vmhost.ValidateManagedCommand(vmCommand); err != nil {
		t.Fatalf("unrelated VM fixture is invalid: %v", err)
	}
	vmJournal, err := json.Marshal(map[string]any{"command": vmCommand, "result": protocol.Result{OperationID: vmCommand.OperationID, Status: "succeeded"}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(managedDir, vmCommand.OperationID+".json"), vmJournal, 0600); err != nil {
		t.Fatal(err)
	}
	marker, bin := filepath.Join(t.TempDir(), "virsh-called"), t.TempDir()
	if err = os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\nprintf called > \"$NEX_TEST_VIRSH_MARKER\"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NEX_TEST_VIRSH_MARKER", marker)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	config, err := PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS = config
	server.StartTLS()
	t.Cleanup(server.Close)
	client, err := PoolPeerClient(member, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)
	request := func(method, path string, body string, want int) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != want {
			response.Body.Close()
			t.Fatalf("%s %s status = %d, want %d", method, path, response.StatusCode, want)
		}
		return response
	}

	response := request(http.MethodGet, strings.TrimSuffix(poolStoragePath, "/"), "", http.StatusOK)
	var listed []protocol.StorageRegistration
	if err = json.NewDecoder(response.Body).Decode(&listed); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed) != 1 || listed[0].ID != storage.ID || listed[0].Generation != storage.Generation {
		t.Fatalf("peer storage list = %#v", listed)
	}
	response = request(http.MethodGet, poolStoragePath+storage.ID, "", http.StatusOK)
	var transfer poolStorageTransfer
	if err = json.NewDecoder(response.Body).Decode(&transfer); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if transfer.Storage.ID != storage.ID || transfer.Storage.Generation != storage.Generation || transfer.Storage.HostID != member.State.HostID {
		t.Fatalf("peer storage transfer = %#v", transfer.Storage)
	}
	response = request(http.MethodGet, poolStoragePath+"old-repo", "", http.StatusNotFound)
	response.Body.Close()

	challenge := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	lockPath := poolStoragePath + storage.ID + "/lock?generation=1&challenge=" + challenge + "&probe_mode=" + poolProbeModeChallengeByte
	response = request(http.MethodPost, lockPath, "", http.StatusNoContent)
	if response.Header.Get(poolProbeModeHeader) != poolProbeModeChallengeByte {
		response.Body.Close()
		t.Fatalf("peer lock mode = %q", response.Header.Get(poolProbeModeHeader))
	}
	response.Body.Close()
	defer func() { _ = releasePoolProbeLease(client, server.URL+lockPath) }()
	response = request(http.MethodDelete, lockPath, "", http.StatusNoContent)
	response.Body.Close()
	response = request(http.MethodPost, poolStoragePath+storage.ID+"/lock?generation=2&challenge="+challenge+"&probe_mode="+poolProbeModeChallengeByte, "", http.StatusConflict)
	response.Body.Close()

	access := protocol.PoolStorageAccess{RepositoryID: storage.ID, HostID: member.State.HostID, Generation: storage.Generation, Readable: true, Writable: true, QEMUWritable: true, Locking: true}
	accessBody, err := json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodPost, poolStoragePath+storage.ID+"/access", string(accessBody), http.StatusNoContent)
	response.Body.Close()
	access.Generation++
	accessBody, err = json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodPost, poolStoragePath+storage.ID+"/access", string(accessBody), http.StatusConflict)
	response.Body.Close()

	// The projection preserves readable registrations when an unrelated journal is
	// corrupt, as Applied did; it must not turn that VM/media failure into storage
	// admission. The durable pool-storage file remains a separate fail-closed guard.
	if err = os.WriteFile(filepath.Join(managedDir, "corrupt.json"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodGet, strings.TrimSuffix(poolStoragePath, "/"), "", http.StatusOK)
	if err = json.NewDecoder(response.Body).Decode(&listed); err != nil {
		response.Body.Close()
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed) != 1 || listed[0].ID != storage.ID {
		t.Fatalf("corrupt unrelated journal changed peer storage list: %#v", listed)
	}
	access.Generation = storage.Generation
	accessBody, err = json.Marshal(access)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(poolStorageFile(master), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	response = request(http.MethodPost, poolStoragePath+storage.ID+"/access", string(accessBody), http.StatusInternalServerError)
	response.Body.Close()
	if got := poolStorageAccess(master)[storage.ID][member.State.HostID]; got.HostID != "" {
		t.Fatalf("corrupt pool storage retained member access: %#v", got)
	}
	if _, err = os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("periodic storage peer handler observed unrelated VM: %v", err)
	}
}
