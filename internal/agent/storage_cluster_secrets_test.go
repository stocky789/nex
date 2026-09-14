// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func clusterSecretStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.State.PoolID, s.State.HostID = "pool-1", "host-1"
	if err := savePoolPeer(s, poolPeerState{Membership: protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}}}}); err != nil {
		t.Fatal(err)
	}
	return s
}
func secretReceipt(kind string) ClusterSecretReceipt {
	r := ClusterSecretReceipt{RequestID: "credential-1", ClusterRequestID: "cluster-1", PoolID: "pool-1", HostID: "host-1", RecipientHostID: "host-1", Kind: kind}
	if kind != "chap" {
		r.FenceTargetHostID = "host-1"
	}
	return r
}

func TestClusterSecretScopesReplayAndRedacts(t *testing.T) {
	s := clusterSecretStore(t)
	defer s.Close()
	r, err := SaveClusterSecret(s, secretReceipt("chap"), ClusterSecretMaterial{Credentials: &protocol.StorageCredentials{Username: "chap", Secret: "do-not-log"}})
	if err != nil || r.Reference == "" || r.RepositoryID != "" {
		t.Fatalf("save = %#v, %v", r, err)
	}
	if _, err = SaveClusterSecret(s, secretReceipt("chap"), ClusterSecretMaterial{Credentials: &protocol.StorageCredentials{Username: "chap", Secret: "changed"}}); err == nil {
		t.Fatal("conflicting replay accepted")
	}
	if err = ValidateClusterSecretReceipt(s, r.Reference, "pool-1", "host-1", "credential-1", "cluster-1", "repo-1", "chap"); err == nil {
		t.Fatal("unbound receipt accepted")
	}
	if err = BindClusterSecretRepository(s, r.Reference, "pool-1", "host-1", "credential-1", "cluster-1", "repo-1"); err != nil {
		t.Fatal(err)
	}
	if replay, replayErr := SaveClusterSecret(s, secretReceipt("chap"), ClusterSecretMaterial{Credentials: &protocol.StorageCredentials{Username: "chap", Secret: "do-not-log"}}); replayErr != nil || replay.Reference != r.Reference || replay.RepositoryID != "repo-1" {
		t.Fatalf("bound original handoff replay = %#v, %v", replay, replayErr)
	}
	if err = ValidateClusterSecretReceipt(s, r.Reference, "pool-1", "host-1", "credential-1", "cluster-1", "repo-1", "chap"); err != nil {
		t.Fatal(err)
	}
	if err = BindClusterSecretRepository(s, r.Reference, "pool-1", "host-1", "credential-1", "cluster-1", "repo-2"); err == nil {
		t.Fatal("receipt rebound")
	}
	if b, e := os.ReadFile(filepath.Join(s.dir, "pool-storage-cluster.json")); e == nil && strings.Contains(string(b), "do-not-log") {
		t.Fatal("secret entered normal cluster state")
	}
}

func TestClusterSecretRejectsNonMemberRecipient(t *testing.T) {
	s := clusterSecretStore(t)
	defer s.Close()
	r := secretReceipt("chap")
	r.RecipientHostID = "host-2"
	if _, err := SaveClusterSecret(s, r, ClusterSecretMaterial{Credentials: &protocol.StorageCredentials{Secret: "secret"}}); err == nil {
		t.Fatal("non-member recipient accepted")
	}
}

func TestClusterCredentialHandoffUsesScopedSecretReceipt(t *testing.T) {
	s := clusterSecretStore(t)
	defer s.Close()
	binding := "cluster-credential:" + hex.EncodeToString([]byte("cluster-1")) + ":fence_ssh:" + hex.EncodeToString([]byte("host-1"))
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{Handle: "offer-1", RequestID: "credential-1", RegistrationRequestID: binding, PoolID: "pool-1", HostID: "host-1"}, Credentials: protocol.StorageCredentials{SSHPrivateKey: "private\nkey", SSHKnownHosts: "fence.example.test ssh-ed25519 pinned"}}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil || receipt.Receipt == "" || receipt.RegistrationRequestID != binding {
		t.Fatalf("cluster handoff = %#v, %v", receipt, err)
	}
	if _, _, err = ClusterFenceSecretPaths(s, receipt.Receipt, "fence_ssh"); err != nil {
		t.Fatalf("cluster fence material was not persisted: %v", err)
	}
	if _, err = saveCredentialHandoff(s, protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{RequestID: "credential-2", RegistrationRequestID: "cluster-credential:not-hex:chap:bad", PoolID: "pool-1", HostID: "host-1"}, Credentials: protocol.StorageCredentials{Secret: "secret"}}); err == nil {
		t.Fatal("ambiguous cluster marker accepted")
	}
}

func TestClusterCredentialResolutionProjectsRecipientLocalRefs(t *testing.T) {
	master := clusterSecretStore(t)
	defer master.Close()
	membership := protocol.PoolMembership{PoolID: "pool-1", MasterHostID: "host-1", Generation: 1, Members: []protocol.PoolMember{{HostID: "host-1", CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"}, {HostID: "host-2", CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"}, {HostID: "host-3", CertificateSHA256: strings.Repeat("c", 64), Generation: 1, Status: "ready"}}}
	if err := savePoolPeer(master, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	_, err := SaveClusterSecret(master, ClusterSecretReceipt{RequestID: "fence-request", ClusterRequestID: "cluster-1", PoolID: "pool-1", HostID: "host-1", RecipientHostID: "host-1", FenceTargetHostID: "host-2", Kind: "fence_ssh"}, ClusterSecretMaterial{SSHPrivateKey: "key", SSHKnownHosts: "host.example.test ssh-ed25519 pinned"})
	if err != nil {
		t.Fatal(err)
	}
	req := StorageClusterRequest{RequestID: "cluster-1", PoolID: "pool-1", HostID: "host-1", Registration: protocol.StorageRegistration{ID: "repo-1"}, Config: protocol.StorageClusterConfig{RepositoryID: "repo-1", Nodes: []protocol.StorageClusterNode{{HostID: "host-2"}}}, CredentialRequests: []protocol.StorageClusterCredentialRef{{RequestID: "fence-request", TargetHostID: "host-2", Kind: "fence_ssh"}}}
	if err = ResolveStorageClusterCredentials(master, &req); err != nil || req.Config.Nodes[0].Fence.CredentialRef == "" || req.Config.Nodes[0].Fence.SSHHostKeyRef != req.Config.Nodes[0].Fence.CredentialRef {
		t.Fatalf("resolve = %#v, %v", req, err)
	}
	masterKey := filepath.Join(master.dir, "storage-cluster", "fence-credentials", req.Config.Nodes[0].Fence.CredentialRef)
	if err = os.Remove(masterKey); err != nil {
		t.Fatal(err)
	}
	if err = master.Save(); err != nil {
		t.Fatal(err)
	}
	masterDir := master.dir
	if err = master.Close(); err != nil {
		t.Fatal(err)
	}
	master, err = OpenStore(masterDir)
	if err != nil {
		t.Fatal(err)
	}
	defer master.Close()
	projection, err := ExportStorageClusterSecrets(master, "pool-1", "cluster-1", "repo-1", "host-3")
	if err != nil || len(projection) != 1 {
		t.Fatalf("export = %#v, %v", projection, err)
	}
	if _, err = os.Stat(masterKey); err != nil {
		t.Fatalf("export did not rematerialize master fence file: %v", err)
	}
	selfProjection, err := ExportStorageClusterSecrets(master, "pool-1", "cluster-1", "repo-1", "host-2")
	if err != nil || len(selfProjection) != 1 || selfProjection[0].Receipt.FenceTargetHostID != "host-2" {
		t.Fatalf("self CIB profile export = %#v, %v", selfProjection, err)
	}
	member := peerStore(t, "host-3", "pool-1")
	if err = savePoolPeer(member, poolPeerState{Membership: membership}); err != nil {
		t.Fatal(err)
	}
	bindings, err := ImportStorageClusterSecrets(member, projection)
	local := bindings.FenceSSH["host-2"]
	if err != nil || local == "" || local != req.Config.Nodes[0].Fence.CredentialRef {
		t.Fatalf("import = %#v, %v", bindings, err)
	}
	if _, _, err = ClusterFenceSecretPaths(member, local, "fence_ssh"); err != nil {
		t.Fatal(err)
	}
}

func TestClusterFenceSecretsArePrivateAndRejectUnsafePaths(t *testing.T) {
	s := clusterSecretStore(t)
	defer s.Close()
	r, err := SaveClusterSecret(s, secretReceipt("fence_ssh"), ClusterSecretMaterial{SSHPrivateKey: "-----BEGIN PRIVATE KEY-----\nkey\n-----END PRIVATE KEY-----\n", SSHKnownHosts: "fence.example.test ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAI"})
	if err != nil {
		t.Fatal(err)
	}
	key, hosts, err := ClusterFenceSecretPaths(s, r.Reference, "fence_ssh")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{key, hosts} {
		info, e := os.Stat(p)
		if e != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private path %q = %v, %v", p, info, e)
		}
	}
	if err = os.Remove(key); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/tmp/not-a-key", key); err != nil {
		t.Fatal(err)
	}
	if _, _, err = ClusterFenceSecretPaths(s, r.Reference, "fence_ssh"); err == nil {
		t.Fatal("symlink key accepted")
	}
	if _, err = SaveClusterSecret(s, ClusterSecretReceipt{RequestID: "../bad", ClusterRequestID: "cluster-1", PoolID: "pool-1", HostID: "host-1", RecipientHostID: "host-1", Kind: "chap"}, ClusterSecretMaterial{Credentials: &protocol.StorageCredentials{Secret: "secret"}}); err == nil {
		t.Fatal("path traversal accepted")
	}
}

func TestClusterCorosyncAuthKeyIsRandomPrivateAndStable(t *testing.T) {
	s := clusterSecretStore(t)
	defer s.Close()
	first, err := ClusterCorosyncAuthKey(s, "pool-1")
	if err != nil || len(first) != 128 {
		t.Fatalf("key = %d bytes, %v", len(first), err)
	}
	second, err := ClusterCorosyncAuthKey(s, "pool-1")
	if err != nil || string(first) != string(second) {
		t.Fatalf("key changed: %v", err)
	}
	info, err := os.Stat(filepath.Join(s.dir, "storage-cluster", "corosync-authkey"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("key mode = %v, %v", info, err)
	}
}

func TestMaterializedKnownHostsOnlyNormalizesUnambiguousDefaultPort(t *testing.T) {
	in := "[fence.example.test]:22 ssh-ed25519 key-a\n[192.0.2.10]:22 ssh-rsa key-b # comment\n[fence.example.test]:2222 ssh-ed25519 key-c\n[2001:db8::1]:22 ssh-ed25519 key-d\n[fence.example.test]:22,alias.example.test ssh-ed25519 key-e\n|1|hashed|entry ssh-ed25519 key-f\n"
	want := "fence.example.test ssh-ed25519 key-a\n192.0.2.10 ssh-rsa key-b # comment\n[fence.example.test]:2222 ssh-ed25519 key-c\n[2001:db8::1]:22 ssh-ed25519 key-d\n[fence.example.test]:22,alias.example.test ssh-ed25519 key-e\n|1|hashed|entry ssh-ed25519 key-f\n"
	if got := materializedKnownHosts(in); got != want {
		t.Fatalf("known_hosts normalization changed trust scope")
	}
}
