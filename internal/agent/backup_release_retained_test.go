// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"testing"

	"nex.local/nex/internal/protocol"
)

// A local release request names only the VM; Core journals it for this host and answers a retry
// of the same request_id with the journaled operation instead of a second one.
func TestReleaseRetainedRequestIsJournaledForThisHostAndIdempotent(t *testing.T) {
	s := seedBackupMaster(t)
	request := func(confirmation string) BackupRequest {
		return BackupRequest{Command: &protocol.Command{OperationID: "release-1", Kind: "vm-release-retained",
			ReleaseRetained: &protocol.VMReleaseRetainedRequest{VMID: "12345678-1234-4234-8234-123456789012", PoolID: "-", HostID: "-", Confirmation: confirmation}}}
	}
	first := acceptBackupRequest(context.Background(), s, request("guest"))
	if first.Err != nil || first.Result == nil || first.Result.Status != "pending" {
		t.Fatalf("accepted: %+v", first)
	}
	entry := s.State.Journal["release-1"]
	if entry.Command == nil || entry.Command.ReleaseRetained.HostID != s.State.HostID || entry.Command.ReleaseRetained.PoolID != s.State.PoolID {
		t.Fatalf("journaled command %+v", entry.Command)
	}
	if again := acceptBackupRequest(context.Background(), s, request("guest")); again.Err != nil || again.Result.Status != "pending" {
		t.Fatalf("retry: %+v", again)
	}
	if changed := acceptBackupRequest(context.Background(), s, request("other")); !errors.Is(changed.Err, ErrBackupConflict) {
		t.Fatalf("changed confirmation under the same request_id: %+v", changed)
	}
}
