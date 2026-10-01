// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// After the master re-pins an SFTP host key, a member's next probe adopts the new pin and
// identity. A change to anything else about the destination stays a conflict.
func TestMemberAdoptsReplacedSFTPHostKeyPinOnly(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	old := testPeerSFTPRepository(t, master)
	membership, err := Membership(master)
	if err != nil {
		t.Fatal(err)
	}
	peer, _ := poolMemberByID(membership, member.State.HostID)
	if err := deliverPeerBackupCredential(context.Background(), master, peer, old); err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRepositories(member, poolBackupRepositoriesState{Repositories: map[string]protocol.BackupRepository{old.ID: old}}); err != nil {
		t.Fatal(err)
	}
	previous := prepareBackupRepositorySFTPNative
	t.Cleanup(func() { prepareBackupRepositorySFTPNative = previous })
	prepareBackupRepositorySFTPNative = func(_ context.Context, got protocol.BackupRepository, _ string, _ protocol.StorageCredentials) (protocol.BackupRepository, error) {
		return got, nil
	}

	moved := old
	moved.SFTPSource = &protocol.BackupSFTPSource{Host: "other.example.test", Root: old.SFTPSource.Root, HostKeySHA256: old.SFTPSource.HostKeySHA256}
	probePeerBackupRepository(context.Background(), member, moved)
	if got, err := GetPoolBackupRepository(member, old.ID); err != nil || got.SFTPSource.Host != old.SFTPSource.Host {
		t.Fatalf("member accepted a different destination: %+v %v", got.SFTPSource, err)
	}

	repinned := old
	repinned.SFTPSource = &protocol.BackupSFTPSource{Host: old.SFTPSource.Host, Root: old.SFTPSource.Root, HostKeySHA256: "SHA256:BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"}
	repinned.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("b", 64)}
	probePeerBackupRepository(context.Background(), member, repinned)
	got, err := GetPoolBackupRepository(member, old.ID)
	if err != nil || got.SFTPSource.HostKeySHA256 != repinned.SFTPSource.HostKeySHA256 || got.RemoteIdentity.Fingerprint != repinned.RemoteIdentity.Fingerprint {
		t.Fatalf("member did not adopt the new pin: %+v %v", got, err)
	}
}
