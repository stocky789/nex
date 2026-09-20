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
	return rehomePoolScope(stateDir, poolID, hostID, false)
}

// RehomeLeavingPoolScope is the same re-scoping for a host that has been ejected and is
// becoming the master of its own new pool. It keeps its local VMs, so unlike a join it must
// carry their definitions across too. What it deliberately leaves at the old pool's scope is
// the tombstone history — the detaches and removals of the resources that stayed with the
// pool — because those records are what happened in the pool this host left, not in its new
// one, and the tombstones the Controller mirrors are read from them. An update of a
// repository the host keeps is not history: its scope is stamped onto the projection, so it
// is rehomed with the registration it updates.
func RehomeLeavingPoolScope(stateDir, poolID, hostID string) error {
	return rehomePoolScope(stateDir, poolID, hostID, true)
}

func rehomePoolScope(stateDir, poolID, hostID string, leaving bool) error {
	e := NewManaged(stateDir)
	js, err := e.journals()
	if err != nil {
		return err
	}
	for _, j := range js {
		c, changed := j.Command, false
		set := func(pool, host *string) {
			if *pool != poolID || *host != hostID {
				*pool, *host, changed = poolID, hostID, true
			}
		}
		// A resource that stays with this host is rehomed in every copy the journal keeps of
		// it, not only in the command as it arrived: storageProjection prefers AppliedStorage
		// and stamps a storage-update's own scope onto the result, and journalMedia prefers
		// DownloadedMedia. Rehoming one copy and not the others left a joined member's own
		// "Local storage" at its pre-join pool and every local placement on it refused with
		// "storage scope does not match the VM" (BUG-0357); for an ISO it would also make the
		// entry unreadable, because readJournal requires the two media copies to agree.
		switch {
		case c.Storage != nil:
			set(&c.Storage.PoolID, &c.Storage.HostID)
		case c.Network != nil:
			set(&c.Network.PoolID, &c.Network.HostID)
		case c.Media != nil:
			set(&c.Media.PoolID, &c.Media.HostID)
		case c.StorageLifecycle != nil && c.StorageLifecycle.Action == "update":
			set(&c.StorageLifecycle.PoolID, &c.StorageLifecycle.HostID)
		case leaving && c.VM != nil:
			set(&c.VM.PoolID, &c.VM.HostID)
		case leaving && c.Power != nil:
			set(&c.Power.PoolID, &c.Power.HostID)
		case leaving && c.VMMedia != nil:
			set(&c.VMMedia.PoolID, &c.VMMedia.HostID)
		}
		// Everything else is left exactly where it happened: a detach or removal tombstone
		// belongs to the pool it happened in, and a join never retargets a workload — the
		// join precheck refuses a host that holds one. Refusing the whole pass on a workload
		// entry instead made the recovery pass every agent start runs a no-op on any host
		// that has VMs, which is what left the lab's members stranded.
		if j.AppliedStorage != nil && (c.Storage != nil || (c.StorageLifecycle != nil && c.StorageLifecycle.Action == "update")) {
			set(&j.AppliedStorage.PoolID, &j.AppliedStorage.HostID)
		}
		if j.DownloadedMedia != nil && c.Media != nil {
			set(&j.DownloadedMedia.PoolID, &j.DownloadedMedia.HostID)
		}
		if !changed {
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
