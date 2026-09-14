// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	Updates    map[string]vmhost.VMUpdateEvidence `json:"updates,omitempty"`
	ObservedAt time.Time                          `json:"observed_at"`
}

type poolVMInventoryReply struct {
	Membership protocol.PoolMembership `json:"membership,omitempty"`
}

var errPoolVMInventoryIncomplete = errors.New("pool VM inventory is incomplete for current membership")
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
		shared := vmUsesInventorySharedStorage(vm, storage)
		// A local-only definition is not a pool ownership record. Requiring
		// its historical update chain here would let an unrelated local VM make
		// every pool inventory publication unavailable. A current local-only
		// definition can nevertheless be the exact successor to a shared owner
		// record. Ask the read-only journal helper in that case, but only require
		// evidence when either side actually references shared storage.
		if vm.Revision > 0 {
			evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(ctx, vm.ID)
			if err == nil && reflect.DeepEqual(evidence.Current, vm) && (shared || vmUsesInventorySharedStorage(evidence.Previous, storage)) {
				updates[vm.ID] = evidence
			} else if shared {
				return poolVMInventory{}, errors.New("managed VM update evidence is unavailable")
			}
		}
	}
	return poolVMInventory{HostID: s.State.HostID, Generation: generation, VMs: managed.VMs, Storage: storage, Observed: observed, Updates: updates, ObservedAt: time.Now().UTC()}, nil
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
	seedErr := seedPoolVMOwners(s, membership, &state, inv.HostID)
	// A partial current-generation snapshot is durable evidence, not an execution
	// admission.  Retain it so later ready members can complete the same generation;
	// owner records remain untouched until seedPoolVMOwners succeeds.
	if err = savePoolVMs(s, state); err != nil {
		return err
	}
	return seedErr
}
func seedPoolVMOwners(s *Store, membership protocol.PoolMembership, state *poolVMState, observedHost string) error {
	for _, member := range membership.Members {
		if member.Status != "ready" || state.Inventory[member.HostID].Generation != membership.Generation {
			return errPoolVMInventoryIncomplete
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
			if !shared && !exists {
				continue
			}
			if inv.Observed[vm.ID] != "running" && inv.Observed[vm.ID] != "shut off" {
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
				vmState := "stopped"
				if inv.Observed[vm.ID] == "running" {
					vmState = "running"
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
