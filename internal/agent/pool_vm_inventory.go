// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"reflect"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

type poolVMInventory struct {
	HostID     string                              `json:"host_id"`
	Generation uint64                              `json:"generation"`
	VMs        []protocol.VMCreateSpec             `json:"vms"`
	Storage    map[string]protocol.StorageIdentity `json:"storage"`
	Observed   map[string]string                   `json:"observed"`
	// Updates contains only exact, successful local managed-journal evidence
	// for a current revision. It lets the master settle a pre-existing pending
	// transition after a restart, never makes inventory itself authoritative.
	Updates map[string]vmhost.VMUpdateEvidence `json:"updates,omitempty"`
	// Aborted maps an operation ID to journal proof that this exact next-revision update
	// failed unapplied, so the master can release a reservation whose outcome it never saw.
	Aborted map[string]vmhost.VMUpdateEvidence `json:"aborted,omitempty"`
	// Deleted names the VMs this host durably deleted: succeeded vm-delete tombstones
	// from its own journal. It is the positive evidence the master prunes owner records
	// with; absence from VMs above is never read as a deletion.
	Deleted    []string  `json:"deleted,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

type poolVMInventoryReply struct {
	Membership protocol.PoolMembership `json:"membership,omitempty"`
}

var errPoolVMInventoryIncomplete = errors.New("pool VM inventory is incomplete for current membership")

// errPoolVMOwnersDeferred reports that the snapshot is durable but the owner records it would
// settle were not written, because this host's pool authority is frozen or handed over. The
// projection is deferred, never dropped: the next publication after the transfer settles it on
// whichever host is master then.
var errPoolVMOwnersDeferred = errors.New("pool VM owner records are deferred while pool authority is frozen")
var errPoolVMInventoryStale = errors.New("stale pool VM inventory")

func settlePoolVMInventoryDefinition(record *poolVMRecord, owner string, observed protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	if record.Spec.ID == "" || reflect.DeepEqual(record.Spec, observed) {
		return nil
	}
	if evidence == nil {
		return errors.New("shared VM definition conflicts with durable inventory")
	}
	if record.PendingUpdate != nil {
		if commitPoolVMUpdate(record, owner, *evidence) != nil {
			return errors.New("shared VM update evidence conflicts with durable inventory")
		}
	} else if recoverPoolVMUpdate(record, owner, *evidence) != nil {
		return errors.New("shared VM definition conflicts with durable inventory")
	}
	if !reflect.DeepEqual(record.Spec, observed) {
		return errors.New("shared VM update evidence conflicts with durable inventory")
	}
	return nil
}

// PublishPoolVMInventory sends only durable local facts to the master.  The master
// rejects incomplete/current-generation snapshots rather than electing the first caller.
func PublishPoolVMInventory(ctx context.Context, s *Store) error {
	return publishPoolVMInventory(ctx, s, false)
}

func publishPoolVMInventory(ctx context.Context, s *Store, refreshed bool) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	inv, err := localPoolVMInventory(ctx, s, membership.Generation)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return recordPoolVMInventory(s, membership, inv)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	c, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	b, _ := json.Marshal(inv)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, master+poolVMPath+"inventory", bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusAccepted {
		return nil // Accepted means master durably retained a current partial snapshot.
	}
	if resp.StatusCode == http.StatusConflict && !refreshed {
		var reply poolVMInventoryReply
		if json.NewDecoder(resp.Body).Decode(&reply) == nil && updatePoolVMMembership(s, reply.Membership) == nil {
			return publishPoolVMInventory(ctx, s, true)
		}
	}
	return errors.New("pool master refused VM inventory")
}

func updatePoolVMMembership(s *Store, membership protocol.PoolMembership) error {
	if membership.Valid() != nil || membership.PoolID != s.State.PoolID {
		return errors.New("pool membership refresh is invalid")
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if state.Membership.Valid() != nil || membership.MasterHostID != state.Membership.MasterHostID || membership.Generation <= state.Membership.Generation {
		return errors.New("pool membership refresh is stale")
	}
	var current, updated protocol.PoolMember
	for _, member := range state.Membership.Members {
		if member.HostID == s.State.HostID {
			current = member
		}
	}
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID {
			updated = member
		}
	}
	if current.HostID == "" || updated.HostID == "" || current.CertificateSHA256 != updated.CertificateSHA256 {
		return errors.New("pool membership refresh changes local peer identity")
	}
	state.Membership = membership
	return savePoolPeer(s, state)
}

func localPoolVMInventory(ctx context.Context, s *Store, generation uint64) (poolVMInventory, error) {
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return poolVMInventory{}, errors.New("managed VM inventory is unavailable")
	}
	storage := map[string]protocol.StorageIdentity{}
	for _, r := range managed.Storage {
		if protocol.IsPoolSharedStorage(r) {
			storage[r.ID] = r.Identity
		}
	}
	observed := make(map[string]string, len(managed.VMs))
	updates := make(map[string]vmhost.VMUpdateEvidence)
	for _, vm := range managed.VMs {
		state, err := vmhost.ManagedVMState(ctx, vm.ID)
		if err != nil {
			return poolVMInventory{}, errors.New("managed VM runtime observation is unavailable")
		}
		observed[vm.ID] = state
		if state == "shut off" {
			// A locally confirmed owner stop invalidates any unconsumed admission;
			// ownership itself remains on the master record.
			_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
		}
		// Every definition now has a master-owned placement record. The journal
		// evidence remains read-only and only settles an exact next revision.
		// A VM without exact evidence (for example an unresolved newer update) is
		// still published, without an Updates entry, at its highest succeeded
		// revision, which the master already records; that VM's pending-update
		// guards keep it blocked. If a published definition ever differed from the
		// master record without evidence, the master would still refuse the whole
		// publication, as before.
		if vm.Revision > 0 {
			evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(ctx, vm.ID)
			if err == nil && reflect.DeepEqual(evidence.Current, vm) {
				updates[vm.ID] = evidence
			}
		}
	}
	aborted := make(map[string]vmhost.VMUpdateEvidence)
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	for _, outcome := range managed.Outcomes {
		if outcome.Kind != "vm-update" || outcome.Status != "failed" {
			continue
		}
		cmd, ok := engine.JournaledCommand(outcome.OperationID)
		if !ok || cmd.VM == nil {
			continue
		}
		if evidence, err := engine.FailedVMUpdateEvidence(ctx, cmd.VM.ID, outcome.OperationID, *cmd.VM); err == nil {
			aborted[outcome.OperationID] = evidence
		}
	}
	deleted := make([]string, 0, len(managed.DeletedVMs))
	for _, tombstone := range managed.DeletedVMs {
		if tombstone.ID != "" && tombstone.HostID == s.State.HostID && tombstone.PoolID == s.State.PoolID {
			deleted = append(deleted, tombstone.ID)
		}
	}
	// A create proven never to have run here releases its admission record the same way.
	neverCreated, err := engine.NeverCreatedVMs()
	if err != nil {
		return poolVMInventory{}, errors.New("managed VM journal is unavailable")
	}
	deleted = append(deleted, neverCreated...)
	return poolVMInventory{HostID: s.State.HostID, Generation: generation, VMs: managed.VMs, Storage: storage, Observed: observed, Updates: updates, Aborted: aborted, Deleted: deleted, ObservedAt: time.Now().UTC()}, nil
}

func vmUsesInventorySharedStorage(vm protocol.VMCreateSpec, storage map[string]protocol.StorageIdentity) bool {
	for _, disk := range vm.Disks {
		if _, ok := storage[disk.StorageID]; ok {
			return true
		}
	}
	return false
}
func recordPoolVMInventory(s *Store, membership protocol.PoolMembership, inv poolVMInventory) error {
	if inv.HostID == "" || inv.Generation != membership.Generation {
		return errPoolVMInventoryStale
	}
	ready := false
	for _, m := range membership.Members {
		ready = ready || (m.HostID == inv.HostID && m.Status == "ready")
	}
	if !ready {
		return errors.New("VM inventory host is not ready")
	}
	// The receiving master timestamps its own accepted snapshot; a member-provided
	// time cannot make an old generation appear current.
	inv.ObservedAt = time.Now().UTC()
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	if state.Inventory == nil {
		state.Inventory = map[string]poolVMInventory{}
	}
	state.Inventory[inv.HostID] = inv
	// A host that is no longer part of the pool is no longer part of its picture. Its row
	// stays in membership for ever, so its snapshot is dropped here instead: otherwise it
	// keeps holding pool networks and ISOs against removal, and pool-vms.json grows without
	// bound. Dropping an observation removes no authority; owner records are untouched, and
	// the eject that produced the row already proved the host owned none.
	for host := range state.Inventory {
		member, known := poolMemberByID(membership, host)
		if !known || !poolVMInventoryMember(member) {
			delete(state.Inventory, host)
		}
	}
	// The snapshot above is an observation and is always stored; the owner records below are
	// the replicated `vms` section, so they are an authority write and take the same guard
	// every other authority write takes, inside the same lock section that performs it.
	// Without this a routine inventory publication could change the content a frozen
	// generation names, and one generation would stop identifying one authority.
	if refusal := requirePoolAuthorityWritable(s); refusal != nil {
		if err = savePoolVMs(s, state); err != nil {
			return err
		}
		return errPoolVMOwnersDeferred
	}
	// Owner records are pruned from this host's own delete tombstones before the seed
	// pass, so a deleted guest stops holding a pool fence and pool-vms.json stops growing
	// without bound. The pool-wide network/ISO dependency check keeps reading the
	// published inventory snapshots, which this never touches.
	prunePoolVMOwners(&state, inv.HostID)
	seedErr := seedPoolVMOwners(s, membership, &state, inv.HostID)
	// A partial current-generation snapshot is durable evidence, not an execution
	// admission.  Retain it so later ready members can complete the same generation;
	// owner records remain untouched until seedPoolVMOwners succeeds.
	if err = savePoolVMs(s, state); err != nil {
		return err
	}
	return seedErr
}

// poolVMInventoryMember reports whether a membership row can own or run a pool VM now, which
// is what the completeness rule is a question about. Every status but "ejected" counts: a
// pending or failed member may have been ready a moment ago and may still be running guests,
// so a picture without it is genuinely incomplete and nothing may be concluded from it. An
// ejected host is the one row that cannot hold a pool VM record by construction — the eject
// is refused from the master's own records while the target owns one (poolEjectRefusal) — and
// its row is kept for ever, so counting it made every inventory permanently incomplete and no
// shared VM could ever be settled as stopped or started again (BUG-0358).
func poolVMInventoryMember(member protocol.PoolMember) bool { return member.Status != "ejected" }

func seedPoolVMOwners(s *Store, membership protocol.PoolMembership, state *poolVMState, observedHost string) error {
	members := map[string]bool{}
	for _, member := range membership.Members {
		if !poolVMInventoryMember(member) {
			continue
		}
		members[member.HostID] = true
		if member.Status != "ready" || state.Inventory[member.HostID].Generation != membership.Generation {
			return fmt.Errorf("%w: host %s (%s) has not reported at generation %d", errPoolVMInventoryIncomplete, member.HostID, member.Status, membership.Generation)
		}
	}
	masterManaged, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(context.Background())
	if !managedVMChecksReady(checks) {
		return errors.New("master storage inventory is unavailable")
	}
	masterStorage := map[string]protocol.StorageRegistration{}
	for _, storage := range masterManaged.Storage {
		if protocol.IsPoolSharedStorage(storage) {
			masterStorage[storage.ID] = storage
		}
	}
	seen := map[string]string{}
	for host, inv := range state.Inventory {
		if !members[host] {
			continue // A host that left the pool seeds nothing; its VMs left with it.
		}
		for _, vm := range inv.VMs {
			if vm.ID == "" || vm.PoolID != s.State.PoolID || vm.HostID != host || protocol.ValidateVMSpec(vm) != nil {
				return errors.New("invalid VM definition in pool inventory")
			}
			shared := false
			for _, disk := range vm.Disks {
				if observed, ok := inv.Storage[disk.StorageID]; ok {
					expected, registered := masterStorage[disk.StorageID]
					if !registered || !vmhost.StorageIdentityMatchesExpected(expected.Identity, observed) {
						return errors.New("shared VM inventory storage identity does not match master registration")
					}
					shared = true
				}
			}
			old, exists := state.VMs[vm.ID]
			if shared && inv.Observed[vm.ID] != "running" && inv.Observed[vm.ID] != "shut off" {
				return errors.New("shared VM runtime observation is invalid")
			}
			if prior := seen[vm.ID]; prior != "" && prior != host {
				return errors.New("duplicate shared VM identity in pool inventory")
			}
			seen[vm.ID] = host
			if exists && old.HostID != host {
				return errors.New("shared VM owner conflicts with current inventory")
			}
			if !exists {
				vmState := "defined"
				if inv.Observed[vm.ID] == "running" {
					vmState = "running"
				} else if inv.Observed[vm.ID] == "shut off" {
					vmState = "stopped"
				}
				state.VMs[vm.ID] = poolVMRecord{PoolID: s.State.PoolID, VMID: vm.ID, HostID: host, State: vmState, Autostart: vm.Autostart, Epoch: 1, Spec: vm}
			} else if old.Spec.ID != "" && !reflect.DeepEqual(old.Spec, vm) {
				evidence, proven := inv.Updates[vm.ID]
				if !proven {
					return errors.New("shared VM definition conflicts with durable inventory")
				}
				if err := settlePoolVMInventoryDefinition(&old, host, vm, &evidence); err != nil {
					return err
				}
				state.VMs[vm.ID] = old
			} else if old.PendingUpdate != nil && host == observedHost && abortPoolVMUpdate(&old, host, inv.Aborted[old.PendingUpdate.OperationID]) == nil {
				// The owner proved the reserved update failed unapplied; release the reservation.
				state.VMs[vm.ID] = old
			} else if old.HostID == host && host == observedHost && old.State != "starting" {
				// Only the just-authenticated owner's fresh native observation may
				// reconcile execution state. Replaying another host's cached
				// inventory must never demote or promote this VM.
				switch inv.Observed[vm.ID] {
				case "running":
					if old.State != "running" {
						old.State = "running"
						state.VMs[vm.ID] = old
					}
				case "shut off":
					if old.State != "stopped" {
						old.State = "stopped"
						state.VMs[vm.ID] = old
					}
				}
			}
		}
	}
	return nil
}
