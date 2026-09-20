// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Placement is a small, read-only master-to-member peer check. The master never
// trusts Controller inventory for a definition placement.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolPlacementPath = "/api/v1/pool/placement"
const poolPlacementLimit = 128 << 10

type poolPlacementRequest struct {
	Generation  uint64           `json:"generation"`
	OperationID string           `json:"operation_id"`
	Command     protocol.Command `json:"command"`
}
type poolPlacementReply struct {
	Generation  uint64                         `json:"generation"`
	OperationID string                         `json:"operation_id"`
	VMID        string                         `json:"vm_id"`
	VM          protocol.VMCreateSpec          `json:"vm"`
	Storage     []protocol.StorageRegistration `json:"storage"`
	Accepted    bool                           `json:"accepted"`
	Replay      bool                           `json:"replay,omitempty"`
	Error       string                         `json:"error,omitempty"`
}

func placementCommand(cmd protocol.Command) bool {
	return (cmd.Kind == "vm-create" || cmd.Kind == "vm-update") && cmd.VM != nil && vmhost.ValidateManagedCommand(cmd) == nil
}

func localPoolVMPlacement(ctx context.Context, s *Store, generation uint64, operationID string, cmd protocol.Command) (poolPlacementReply, error) {
	membership, err := Membership(s)
	if err != nil || membership.Generation != generation || !placementCommand(cmd) || cmd.OperationID != operationID || cmd.VM.HostID != s.State.HostID || cmd.VM.PoolID != s.State.PoolID {
		return poolPlacementReply{}, errors.New("VM placement preflight is invalid")
	}
	// This host's freshest verified copy of the master's drain intent — its own record on
	// the master, the stored authority replica on a member. The master refuses again from
	// its own record; refusing here keeps a drained member from doing the native work first.
	if err = poolHostInMaintenance(s, s.State.HostID); err != nil {
		return poolPlacementReply{}, err
	}
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	journaled, replay := engine.JournaledCommand(cmd.OperationID)
	if replay && !reflect.DeepEqual(journaled, cmd) {
		return poolPlacementReply{}, errors.New("VM placement operation ID has different input")
	}
	if !replay {
		ctx, err = poolVMExecutionContext(ctx, s, *cmd.VM)
		if err != nil {
			return poolPlacementReply{}, err
		}
	}
	storage, err := engine.CheckVMPlacement(ctx, cmd)
	if err != nil {
		return poolPlacementReply{}, err
	}
	current, err := Membership(s)
	if err != nil || current.Generation != membership.Generation {
		return poolPlacementReply{}, errors.New("pool membership changed during VM placement preflight")
	}
	return poolPlacementReply{Generation: generation, OperationID: operationID, VMID: cmd.VM.ID, VM: *cmd.VM, Storage: storage, Accepted: true, Replay: replay}, nil
}

// PoolPlacementHandler accepts only the current master and returns a fresh,
// bounded native observation tied to the exact command and membership generation.
func PoolPlacementHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(s, r); err != nil {
			http.Error(w, "placement peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request poolPlacementRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, poolPlacementLimit))
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !placementCommand(request.Command) {
			http.Error(w, "invalid placement preflight", http.StatusBadRequest)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		reply, err := localPoolVMPlacement(ctx, s, request.Generation, request.OperationID, request.Command)
		if err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(poolPlacementReply{Error: placementError(err)})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(reply)
	})
}

func placementError(err error) string {
	message := strings.TrimSpace(err.Error())
	if len(message) > 240 {
		message = message[:240]
	}
	if strings.ContainsAny(message, "\r\n\x00") {
		return "selected host placement check failed"
	}
	return message
}

func poolVMPlacement(ctx context.Context, s *Store, membership protocol.PoolMembership, member protocol.PoolMember, cmd protocol.Command) (poolPlacementReply, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// Every new placement and every definition transition passes here on the master, for a
	// member and for the master's own guests alike, so this is the one refusal that covers
	// them all. It is decided from the master's own membership, never from a mirror.
	if err := poolMaintenanceRefusal(membership, member.HostID); err != nil {
		return poolPlacementReply{}, err
	}
	if member.HostID == s.State.HostID {
		return localPoolVMPlacement(ctx, s, membership.Generation, cmd.OperationID, cmd)
	}
	if member.Status != "ready" || member.Generation != membership.Generation || member.PeerOrigin == "" {
		return poolPlacementReply{}, errors.New("selected VM host is not a ready current member")
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return poolPlacementReply{}, err
	}
	defer client.CloseIdleConnections()
	body, err := json.Marshal(poolPlacementRequest{Generation: membership.Generation, OperationID: cmd.OperationID, Command: cmd})
	if err != nil {
		return poolPlacementReply{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolPlacementPath, bytes.NewReader(body))
	if err != nil {
		return poolPlacementReply{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return poolPlacementReply{}, errors.New("selected VM host placement observation is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var refused poolPlacementReply
		if json.NewDecoder(io.LimitReader(response.Body, poolPlacementLimit+1)).Decode(&refused) == nil && refused.Error != "" {
			return poolPlacementReply{}, errors.New("selected VM host: " + placementError(errors.New(refused.Error)))
		}
		return poolPlacementReply{}, errors.New("selected VM host refused placement observation")
	}
	var reply poolPlacementReply
	if json.NewDecoder(io.LimitReader(response.Body, poolPlacementLimit+1)).Decode(&reply) != nil || !reply.Accepted || reply.Generation != membership.Generation || reply.OperationID != cmd.OperationID || cmd.VM == nil || reply.VMID != cmd.VM.ID || !reflect.DeepEqual(reply.VM, *cmd.VM) {
		return poolPlacementReply{}, errors.New("selected VM host did not acknowledge exact placement observation")
	}
	return reply, nil
}

func poolVMPlacementStorageMatches(ctx context.Context, s *Store, reply poolPlacementReply, vm protocol.VMCreateSpec) error {
	storage, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return errors.New("master storage observation is unavailable")
	}
	master := map[string]protocol.StorageRegistration{}
	for _, registration := range storage {
		if protocol.IsPoolSharedStorage(registration) {
			master[registration.ID] = registration
		}
	}
	target := map[string]protocol.StorageRegistration{}
	for _, storage := range reply.Storage {
		if storage.ID == "" || target[storage.ID].ID != "" {
			return errors.New("selected VM host returned ambiguous storage observation")
		}
		if protocol.IsPoolSharedStorage(storage) {
			expected, ok := master[storage.ID]
			if !ok || storage.Generation != expected.Generation || storage.Tombstone || (storage.Status != "" && storage.Status != "succeeded") || !vmhost.StorageIdentityMatchesExpected(expected.Identity, storage.Identity) {
				return errors.New("selected VM host returned an unregistered shared storage observation")
			}
			if protocol.IsClusteredISCSI(expected) {
				eligible := false
				for _, hostID := range expected.Source.ISCSI.EligibleHostIDs {
					eligible = eligible || hostID == vm.HostID
				}
				if !eligible {
					return errors.New("selected VM host is not eligible for clustered shared storage")
				}
			}
		}
		target[storage.ID] = storage
	}
	for _, disk := range vm.Disks {
		expected, shared := master[disk.StorageID]
		if !shared {
			continue
		}
		observed, ok := target[disk.StorageID]
		if !ok || !vmhost.StorageIdentityMatchesExpected(expected.Identity, observed.Identity) {
			return errors.New("selected VM host shared storage observation does not match master registration")
		}
	}
	return nil
}
