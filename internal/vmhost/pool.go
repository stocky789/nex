// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"

	"nex.local/nex/internal/protocol"
)

// RehomePoolScope changes only the Core-owned pool/host scope on eligible journal records.
// Resource IDs, generations, paths and definitions stay intact; callers preflight away VMs
// and remote storage before invoking it, so joining cannot retarget a writable workload.
func RehomePoolScope(stateDir, poolID, hostID string) error {
	e := NewManaged(stateDir)
	js, err := e.journals()
	if err != nil {
		return err
	}
	for _, j := range js {
		c := j.Command
		switch {
		case c.Storage != nil:
			c.Storage.PoolID, c.Storage.HostID = poolID, hostID
		case c.Network != nil:
			c.Network.PoolID, c.Network.HostID = poolID, hostID
		case c.Media != nil:
			c.Media.PoolID, c.Media.HostID = poolID, hostID
		case c.VM != nil || c.Power != nil || c.VMMedia != nil || c.StorageLifecycle != nil:
			return errors.New("managed workload or remote storage journal cannot be rehomed")
		default:
			continue
		}
		j.Command = c
		if err = e.save(j); err != nil {
			return err
		}
	}
	return nil
}

// HandlePoolCommand accepts only reconciliation after the peer endpoint durably admits it.
// A Controller command is never authority to create or replace membership.
func HandlePoolCommand(_ context.Context, _ string, hostID string, cmd protocol.Command) error {
	if cmd.Pool == nil || cmd.Pool.Valid() != nil {
		return errors.New("invalid pool command")
	}
	if cmd.Pool.Membership.MasterHostID != hostID {
		return errors.New("only the recorded pool master accepts pool commands")
	}
	if cmd.Pool.Action == "member-join" {
		return errors.New("pool membership is admitted only by the authenticated Core peer endpoint")
	}
	return nil
}
