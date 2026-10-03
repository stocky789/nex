// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func boundsRun(master *Store, id, hostID, status string) protocol.BackupRun {
	return protocol.BackupRun{OperationID: id, Kind: protocol.BackupKindScheduled, PoolID: master.State.PoolID, HostID: hostID,
		RepositoryID: "repo-1", VMID: "vm-" + id, Stage: "reserved", Status: status, CreatedAt: time.Now().UTC().Add(-3 * time.Hour)}
}

// Silence and missing owner access cannot prove execution failed. Preserve each
// reservation as needs_attention so returning members can settle it without replay.
func TestUnservableMemberRunsRemainUncertain(t *testing.T) {
	lab := newTransferLab(t, 1)
	master, member := lab.host("master-1"), lab.host("member-1")
	seedFlowRepository(t, master)
	now := time.Now()
	backoff := peerBackoff{}
	backoff["silent"] = peerBackoffEntry{answer: peerBackupUnreachable, since: now.Add(-2 * peerBackupSilentLimit)}
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	undialable := protocol.PoolMember{HostID: "undialable-1", Hostname: "undialable-1",
		CertificateSHA256: strings.Repeat("c", 64), Generation: state.Membership.Generation, Status: "failed"}
	state.Membership.Members = append(state.Membership.Members, undialable)
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	for _, run := range []protocol.BackupRun{
		boundsRun(master, "silent", member.State.HostID, "pending"),
		boundsRun(master, "self", master.State.HostID, "pending"),
		boundsRun(master, "self-attention", master.State.HostID, "needs_attention"),
		boundsRun(master, "gone", "ejected-host", "needs_attention"),
		boundsRun(master, "undialable", undialable.HostID, "pending"),
	} {
		if err := savePoolBackupRun(master, run); err != nil {
			t.Fatal(err)
		}
	}
	if _, ready := nextPendingPeerBackup(master, backoff, now); ready {
		t.Fatal("an unservable run was dispatched")
	}
	for _, id := range []string{"silent", "self", "self-attention", "gone", "undialable"} {
		run, err := loadPoolBackupRun(master, id)
		if err != nil || run.Status != "needs_attention" || run.Error == "" {
			t.Fatalf("run %s = %+v, %v", id, run, err)
		}
	}
	if work, ready := nextPendingPeerBackup(master, peerBackoff{}, now); !ready || work.run.OperationID != "silent" || !work.observe {
		t.Fatalf("a returning member must be observed without restarting it: ready=%v work=%+v", ready, work)
	}
}

// A pending run reserved on a member that is not ready, or on the member being ejected, must
// not refuse the transfer.
func TestPendingRunOfUnreadyOrEjectedMemberDoesNotBlockTransfer(t *testing.T) {
	lab := newTransferLab(t, 2)
	master := lab.host("master-1")
	if err := savePoolBackupRun(master, boundsRun(master, "on-member-1", "member-1", "pending")); err != nil {
		t.Fatal(err)
	}
	if err := unsettledBackupTransferRefusal(master, "promote", "member-2"); err == nil {
		t.Fatal("a pending run on a ready member must still block a transfer")
	}
	if err := unsettledBackupTransferRefusal(master, "eject", "member-1"); err != nil {
		t.Fatalf("pending run on the eject target blocked the eject: %v", err)
	}
	state, err := loadPoolPeer(master)
	if err != nil {
		t.Fatal(err)
	}
	for i := range state.Membership.Members {
		if state.Membership.Members[i].HostID == "member-1" {
			state.Membership.Members[i].Status = "failed"
		}
	}
	if err := savePoolPeer(master, state); err != nil {
		t.Fatal(err)
	}
	if err := unsettledBackupTransferRefusal(master, "promote", "member-2"); err != nil {
		t.Fatalf("pending run on a member that is not ready blocked the transfer: %v", err)
	}
}

// A missing pool record is not evidence that an admitted command never ran.
func TestMissingNeedsAttentionBackupStillExcludesNewRun(t *testing.T) {
	s := seedBackupMaster(t)
	vm := poolVMTestSpec("vm-1", s.State.HostID)
	backup := func(id string) protocol.Command {
		return protocol.Command{OperationID: id, Kind: protocol.BackupKindBackupNow, VM: vm, Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	}
	stale := backup("stale")
	journal := map[string]JournalEntry{
		"stale": {Command: &stale, Status: "needs_attention", Order: 1},
	}
	fresh := backup("fresh")
	journal["fresh"] = JournalEntry{Command: &fresh, Status: "pending", Order: 2}
	s.State.Journal = journal
	if next := nextManagedCommand(journal, ""); next != "" {
		t.Fatalf("precondition: needs_attention entry should block, got %q", next)
	}
	result, settled := journalBackupSettlement(s, journal["stale"])
	if settled {
		t.Fatalf("unknown outcome was terminalized: %+v", result)
	}
	if result := unreservedBackupResult(s, stale, time.Now().UTC(), ErrBackupNotFound); result.Status != "needs_attention" {
		t.Fatalf("missing admitted execution was failed: %+v", result)
	}
	if next := nextManagedCommand(journal, ""); next != "" {
		t.Fatalf("unknown execution released same-VM exclusion: %q", next)
	}
	journal["stale"] = JournalEntry{Command: &stale, Status: "pending", PeerDispatched: true, Order: 1}
	if result := unreservedBackupResult(s, stale, time.Now().UTC(), ErrBackupNotFound); result.Status != "needs_attention" {
		t.Fatalf("missing peer-admitted execution was failed: %+v", result)
	}
}

// A metadata request that fails because its policy or repository does not exist is terminal,
// so its receipt must not stay pending and refuse every later authority change.
func TestFailedMetadataRequestReceiptDoesNotBlockAuthorityChange(t *testing.T) {
	s := seedBackupMaster(t)
	cmd := protocol.Command{OperationID: "run-now-nopolicy", Kind: protocol.BackupKindPolicyRunNow,
		BackupPolicy: &protocol.BackupPolicy{ID: "nonexistent"}, VM: &protocol.VMCreateSpec{ID: "vm-1"}}
	response := acceptBackupRequest(context.Background(), s, BackupRequest{Command: &cmd})
	if !errors.Is(response.Err, ErrBackupPolicyNotFound) {
		t.Fatalf("response = %+v", response)
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, cmd.OperationID)
	if err != nil || receipt.Status != "failed" {
		t.Fatalf("receipt = %+v, %v", receipt, err)
	}
	if err := unsettledBackupTransferRefusal(s, "promote", "member-1"); err != nil {
		t.Fatalf("failed receipt blocked the transfer: %v", err)
	}
}

func TestBackupHealthErrorNamesSFTPHostKeyChange(t *testing.T) {
	err := errors.New("ssh: handshake failed: backup SFTP host key differs from pinned key")
	if got := backupHealthError(err); !strings.Contains(got, "host key") {
		t.Fatalf("host key change reason missing: %q", got)
	}
	if got := backupHealthError(errors.New("dial tcp 192.0.2.1:22: i/o timeout")); got != "backup repository health probe failed" {
		t.Fatalf("probe detail leaked into health: %q", got)
	}
}
