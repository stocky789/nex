// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"path/filepath"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// ExecuteSystemDiskReplace rebuilds this host's degraded system-disk mirror onto the
// replacement the operator selected. Every refusal is a definite failed result carrying
// Core's own sentence; the replacement itself is journaled on the host, keyed on the
// operation ID, so a redelivered or interrupted command never wipes a disk twice.
func ExecuteSystemDiskReplace(ctx context.Context, dir string, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	request := cmd.SystemDiskReplace
	if request == nil {
		result.Error = "system disk replacement command carries no request"
		return result
	}
	if request.OperationID != cmd.OperationID {
		result.Error = "system disk replacement request does not match its command"
		return result
	}
	if err := vmhost.NewSystemDiskReplacer(filepath.Join(dir, "system-disk-replace")).Replace(ctx, *request); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status, result.ObservedAt = "succeeded", time.Now().UTC()
	return result
}
