// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"fmt"
	"time"

	"nex.local/nex/internal/protocol"
)

// deletePoolBackupPolicy removes only the policy definition from the master, so the
// scheduler stops for it. Recovery points, run history and repository data stay; retention
// no longer applies to the points the policy made.
func deletePoolBackupPolicy(s *Store, req protocol.DeleteBackupPolicyRequest) error {
	poolBackupAuthorityMu.Lock()
	defer poolBackupAuthorityMu.Unlock()
	if err := masterBackupPool(s, s.State.PoolID); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(s); err != nil {
		return err
	}
	state, err := loadPoolBackupPolicies(s)
	if err != nil {
		return err
	}
	policy, ok := state.Policies[req.PolicyID]
	if !ok {
		return ErrBackupPolicyNotFound
	}
	if req.Confirmation != policy.Name {
		return backupRefusal(fmt.Sprintf("type the backup job name %q to confirm", policy.Name))
	}
	runs, err := listPoolBackupRuns(s)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.PolicyID == policy.ID && run.Status == "pending" {
			return backupRefusal(fmt.Sprintf("backup run %s of this job is still running; wait for it to finish", run.OperationID))
		}
	}
	delete(state.Policies, policy.ID)
	return savePoolBackupPolicies(s, state)
}

// retryPolicyDelete settles a delete whose receipt a crash left pending: the policy being
// gone already means the earlier attempt removed it.
func retryPolicyDelete(ctx context.Context, s *Store, cmd protocol.Command, receipt protocol.BackupMetadataReceipt) BackupResponse {
	response := acceptBackupRequestUnrecorded(ctx, s, BackupRequest{Command: &cmd})
	if response.Err == ErrBackupPolicyNotFound {
		response = BackupResponse{Result: &protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}}
	}
	return settleBackupMetadataResponse(s, cmd, receipt, response)
}
