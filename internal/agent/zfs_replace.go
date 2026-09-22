// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Replacing a failed disk in a provisioned ZFS pool. The master checks that the repository
// and host are its own, then the executing host does the work and journals it; the master
// only records which disk the repository now holds.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolZFSReplacePath = poolZFSPath + "/replace"

// poolZFSReplacePeerTimeout covers wipefs and zpool replace on a failing disk. The
// default peer client budget (15 s) is far too short for that destructive work.
const poolZFSReplacePeerTimeout = 30 * time.Minute

// poolZFSReplaceLocal is a seam for the peer and aggregate checks; the real worker remains
// the only code that touches a disk.
var poolZFSReplaceLocal = func(ctx context.Context, s *Store, request protocol.ZFSReplaceRequest) error {
	return vmhost.NewZFSReplacer(filepath.Join(s.dir, "zfs-replace")).Replace(ctx, request)
}

// ExecutePoolZFSReplace is the master-side aggregate for one disk replacement.
func ExecutePoolZFSReplace(ctx context.Context, s *Store, cmd protocol.Command) (protocol.Result, error) {
	poolZFSRunMu.Lock()
	defer poolZFSRunMu.Unlock()
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	request := cmd.ZFSReplace
	if request == nil || request.OperationID != cmd.OperationID {
		result.Error = "ZFS disk replacement command carries no matching request"
		return result, nil
	}
	if err := request.Valid(); err != nil {
		result.Error = err.Error()
		return result, nil
	}
	if request.PoolID != s.State.PoolID {
		result.Error = "ZFS disk replacement targets another pool"
		return result, nil
	}
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		result.Error = "only the pool master may replace a ZFS disk"
		return result, nil
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(s)
	poolZFSMu.Unlock()
	if err != nil {
		result.Error = "ZFS pool journal is unavailable"
		return result, err
	}
	if outcome, ok := foldPoolZFSOperations(state)[request.RepositoryID].Outcomes[request.HostID]; !ok || outcome.Status != "succeeded" {
		result.Error = "this pool master has no ZFS repository of that ID on that host"
		return result, nil
	}
	if request.HostID != s.State.HostID {
		member, ok := poolMemberByID(membership, request.HostID)
		if !ok || member.Status != "ready" || member.Generation != membership.Generation {
			result.Error = "the selected ZFS host is not a ready current member"
			return result, nil
		}
		err = dispatchPoolZFSReplace(ctx, s, member, *request)
	} else {
		err = poolZFSReplaceLocal(ctx, s, *request)
	}
	if err != nil {
		result.Status, result.Error = "needs_attention", err.Error()
		return result, nil
	}
	if err := recordPoolZFSReplacement(s, *request); err != nil {
		result.Status, result.Error = "needs_attention", "the disk was replaced but the repository record was not updated"
		return result, err
	}
	result.Status, result.ObservedAt = "succeeded", time.Now().UTC()
	return result, nil
}

// recordPoolZFSReplacement keeps the master's durable repository record naming the disks the
// pool actually holds, so a fresh Controller does not adopt a repository that still lists a
// disk ZFS has retired.
func recordPoolZFSReplacement(s *Store, request protocol.ZFSReplaceRequest) error {
	poolZFSMu.Lock()
	defer poolZFSMu.Unlock()
	state, err := loadPoolZFS(s)
	if err != nil {
		return err
	}
	changed := false
	for id, op := range state.Operations {
		outcome, ok := op.Outcomes[request.HostID]
		if !ok || op.RepositoryID != request.RepositoryID {
			continue
		}
		for i, device := range outcome.Devices {
			// The pool names its leaves by the partition ZFS created on the selected
			// whole disk, so the record's disk is the one the failed leaf sits on.
			if device.ByID != request.OldDevice && !strings.HasPrefix(request.OldDevice, device.ByID+"-part") {
				continue
			}
			outcome.Devices[i] = protocol.ZFSProvisionDevice{ByID: request.Device.ByID, Serial: request.Device.Serial, SizeBytes: request.Device.SizeBytes, Fingerprint: request.Device.Fingerprint}
			op.Outcomes[request.HostID] = outcome
			state.Operations[id] = op
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return savePoolZFS(s, state)
}

func dispatchPoolZFSReplace(ctx context.Context, s *Store, member protocol.PoolMember, request protocol.ZFSReplaceRequest) error {
	if member.PeerOrigin == "" {
		return errors.New("the selected ZFS host is unreachable")
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return errors.New("the selected ZFS host is unreachable")
	}
	defer client.CloseIdleConnections()
	client.Timeout = poolZFSReplacePeerTimeout
	// Detach from the command context: a Controller cancel must not abort a wipe or
	// zpool replace that has already been handed to the member.
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), poolZFSReplacePeerTimeout)
	defer cancel()
	body, _ := json.Marshal(request)
	req, err := http.NewRequestWithContext(work, http.MethodPost, strings.TrimRight(member.PeerOrigin, "/")+poolZFSReplacePath, bytes.NewReader(body))
	if err != nil {
		return errors.New("the selected ZFS host is unreachable")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("the selected ZFS host is unreachable")
	}
	defer resp.Body.Close()
	var outcome protocol.Result
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, poolZFSLimit)).Decode(&outcome) != nil || outcome.OperationID != request.OperationID {
		return errors.New("the selected ZFS host refused the disk replacement")
	}
	if outcome.Status != "succeeded" {
		if outcome.Error == "" {
			return errors.New("the selected ZFS host did not replace the disk")
		}
		return errors.New(outcome.Error)
	}
	return nil
}

// PoolZFSReplaceHandler is member-only and accepts only the current master certificate.
func PoolZFSReplaceHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := AuthenticatePoolMaster(s, r); err != nil {
			http.Error(w, "ZFS peer rejected", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var request protocol.ZFSReplaceRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, poolZFSLimit))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || request.Valid() != nil || request.PoolID != s.State.PoolID || request.HostID != s.State.HostID {
			http.Error(w, "invalid ZFS disk replacement", http.StatusBadRequest)
			return
		}
		result := protocol.Result{OperationID: request.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
		// Detach from the HTTP request context so a peer client timeout or disconnect
		// cannot cancel wipefs / zpool replace after they have started; the journal
		// records the outcome for the next report or retry.
		work, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), poolZFSReplacePeerTimeout)
		defer cancel()
		if err := poolZFSReplaceLocal(work, s, request); err != nil {
			result.Status, result.Error = "needs_attention", err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
