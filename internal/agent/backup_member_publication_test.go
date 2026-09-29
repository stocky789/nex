// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestMemberPeerPublicationAttentionReconcilesWithoutController(t *testing.T) {
	lab := newTransferLab(t, 1)
	member := lab.host("member-1")
	cmd := protocol.Command{OperationID: "member-publication", Kind: protocol.BackupKindBackupNow,
		VM: poolVMTestSpec("vm-1", member.State.HostID),
		Backup: &protocol.BackupNowRequest{RepositoryID: "repo-1"}}
	intent, err := backupIntentSHA256(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if err := savePoolBackupRun(member, protocol.BackupRun{OperationID: cmd.OperationID,
		Kind: protocol.BackupKindBackupNow, PoolID: member.State.PoolID, HostID: member.State.HostID,
		VMID: cmd.VM.ID, RepositoryID: "repo-1", RecoveryPointID: cmd.OperationID,
		IntentSHA256: intent, Stage: "publication", Status: "needs_attention", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	entry := JournalEntry{Status: "needs_attention", Command: &cmd, PeerDispatched: true}
	if !publishedLocalBackupNeedsReconcile(member, entry) {
		t.Fatal("member peer publication cannot reconcile without Controller")
	}
	entry.PeerDispatched = false
	if publishedLocalBackupNeedsReconcile(member, entry) {
		t.Fatal("unaccepted member command entered peer reconciliation")
	}
}
