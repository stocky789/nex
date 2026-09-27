// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"log"
	"path/filepath"
	"reflect"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// Local observations remain durable while the master is unreachable. Only the
// master's VM record is authority; publication carries these facts to that record.
func retainLocalDetectedOS(s *Store, vms []protocol.VMCreateSpec, detected map[string]protocol.DetectedOS) (map[string]protocol.DetectedOS, error) {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return nil, err
	}
	kept := map[string]protocol.DetectedOS{}
	for _, vm := range vms {
		if vm.HostID != s.State.HostID || vm.PoolID != s.State.PoolID {
			continue
		}
		if previous, ok := state.LocalDetectedOS[vm.ID]; ok {
			kept[vm.ID] = previous
		}
		fresh, ok := detected[vm.ID]
		if !ok || fresh.Valid() != nil {
			continue
		}
		previous := kept[vm.ID]
		// Same OS facts must not rewrite ObservedAt: probes stamp "now" on every
		// successful guest-agent answer, and that churn would rewrite pool state forever.
		if previous.Valid() == nil && previous.OS == fresh.OS && previous.OSID == fresh.OSID {
			continue
		}
		if previous.Valid() != nil || fresh.ObservedAt.After(previous.ObservedAt) {
			kept[vm.ID] = fresh
		}
	}
	if !reflect.DeepEqual(state.LocalDetectedOS, kept) && (len(state.LocalDetectedOS) != 0 || len(kept) != 0) {
		state.LocalDetectedOS = kept
		if err := savePoolVMs(s, state); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

func recordGuestOS(ctx context.Context, s *Store, detected map[string]protocol.DetectedOS) {
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		log.Printf("detected guest OS could not be retained: managed inventory unavailable")
		return
	}
	if _, err := retainLocalDetectedOS(s, managed.VMs, detected); err != nil {
		log.Printf("detected guest OS could not be retained: %v", err)
	}
}

func poolDetectedOS(s *Store) (map[string]protocol.DetectedOS, error) {
	membership, err := Membership(s)
	if err != nil {
		return nil, err
	}
	if membership.MasterHostID != s.State.HostID {
		return nil, nil
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return nil, err
	}
	out := map[string]protocol.DetectedOS{}
	invalid := false
	for id, vm := range state.VMs {
		if vm.PoolID != membership.PoolID || vm.DetectedOS == nil {
			continue
		}
		if vm.DetectedOS.Valid() != nil {
			invalid = true
			continue
		}
		out[id] = *vm.DetectedOS
	}
	if invalid {
		return out, errors.New("invalid retained guest OS")
	}
	return out, nil
}
