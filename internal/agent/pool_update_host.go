// SPDX-License-Identifier: AGPL-3.0-only
package agent

// The host side of a pool rollout. The master's coordinator (pool_update.go) asks; this file
// answers for the host it runs on: a read-only status, and one idempotent step at a time. Every
// piece of image work is the host engine's own (internal/nexupdate keeps /var/lib/nex/image/
// state.json); this code only maps a step onto the engine and onto the guests the host runs.
// The master reaches itself through the same two functions the peer routes call, so a master's
// own step is no different from a member's.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const (
	poolUpdateStepPath   = "/api/v1/pool/updates/step"
	poolUpdateStatusPath = "/api/v1/pool/updates/status"
	poolUpdateBlobPath   = "/api/v1/pool/updates/blobs/"
	poolUpdateImageDir   = "/var/lib/nex/image"
)

// PoolUpdateGuests is what a rollout does to the guests of one host. The default goes through
// the same start/stop admission a Controller-sent power command takes; a lab replaces it.
type PoolUpdateGuests interface {
	// Running lists the IDs of guests that are not shut off.
	Running(ctx context.Context) ([]string, error)
	// Shutdown asks one guest to shut down gracefully; it never forces.
	Shutdown(ctx context.Context, opID, vmID string) error
	// Start starts one guest through the master's start admission.
	Start(ctx context.Context, opID, vmID string) error
}

var (
	poolUpdateGuestOps  = func(s *Store) PoolUpdateGuests { return localPoolUpdateGuests{s} }
	poolUpdateFreeBytes = func() uint64 {
		var st syscall.Statfs_t
		if syscall.Statfs(poolUpdateImageDir, &st) != nil {
			return 0
		}
		return st.Bavail * uint64(st.Bsize)
	}
)

type localPoolUpdateGuests struct{ s *Store }

func (g localPoolUpdateGuests) engine() *vmhost.ManagedEngine {
	return vmhost.NewManaged(filepath.Join(g.s.dir, "managed-vms"))
}

func (g localPoolUpdateGuests) Running(ctx context.Context) ([]string, error) {
	out, err := virsh(ctx, "list", "--uuid")
	if err != nil {
		return nil, err
	}
	return strings.Fields(out), nil
}

func (g localPoolUpdateGuests) power(ctx context.Context, opID, vmID, action string) error {
	cmd := protocol.Command{OperationID: opID, Kind: "vm-power", Power: &protocol.VMPowerRequest{
		VMID: vmID, PoolID: g.s.State.PoolID, HostID: g.s.State.HostID, Action: action}}
	finish, err := AdmitPoolVMExecution(ctx, g.s, cmd)
	if err != nil {
		return err
	}
	executionCtx, err := poolVMCommandExecutionContext(ctx, g.s, cmd)
	if err != nil {
		return err
	}
	result, execErr := g.engine().Execute(executionCtx, cmd, mediaFetcher(g.s.State))
	if finish != nil {
		if finishErr := finish(result, execErr); finishErr != nil && execErr == nil {
			return finishErr
		}
	}
	if execErr != nil {
		return execErr
	}
	if result.Status != "succeeded" {
		return errors.New(result.Error)
	}
	return nil
}

func (g localPoolUpdateGuests) Shutdown(ctx context.Context, opID, vmID string) error {
	return g.power(ctx, opID, vmID, "shutdown")
}

func (g localPoolUpdateGuests) Start(ctx context.Context, opID, vmID string) error {
	return g.power(ctx, opID, vmID, "start")
}

// poolUpdateOpID is the deterministic operation ID of one guest action inside one step, so a
// repeated step repeats the same journaled operation instead of starting a second one.
func poolUpdateOpID(stepID, vmID string) string {
	sum := sha256.Sum256([]byte(stepID + "\x00" + vmID))
	return hex.EncodeToString(sum[:16])
}

func poolUpdateEngineStatus(ctx context.Context) (nexupdate.ImageStatus, *protocol.PoolUpdateStepResult) {
	engine := currentImageUpdater()
	if engine == nil {
		return nexupdate.ImageStatus{}, &protocol.PoolUpdateStepResult{Code: "unsupported_edition", Message: "this host does not run the image edition"}
	}
	st, err := engine.Status(ctx)
	if err != nil {
		return st, poolUpdateRefusal(err)
	}
	return st, nil
}

func poolUpdateRefusal(err error) *protocol.PoolUpdateStepResult {
	var refusal *nexupdate.Error
	if errors.As(err, &refusal) {
		return &protocol.PoolUpdateStepResult{Code: refusal.Code, Message: refusal.Message}
	}
	return &protocol.PoolUpdateStepResult{Code: "internal", Message: "the update engine failed"}
}

func poolUpdateSlotSequence(st nexupdate.ImageStatus, roles ...string) uint64 {
	for _, slot := range st.Slots {
		for _, role := range roles {
			if slot.Role == role && slot.Sequence > 0 {
				return uint64(slot.Sequence)
			}
		}
	}
	return 0
}

// PoolUpdateHostStatusFor is GET /api/v1/pool/updates/status: this host's own observation of
// its image engine and its guests. It changes nothing.
func PoolUpdateHostStatusFor(ctx context.Context, s *Store) (protocol.PoolUpdateHostStatus, error) {
	out := protocol.PoolUpdateHostStatus{HostID: s.State.HostID, ObservedAt: time.Now().UTC()}
	st, refusal := poolUpdateEngineStatus(ctx)
	if refusal != nil {
		return out, errors.New(refusal.Code + ": " + refusal.Message)
	}
	out.Capabilities = []string{protocol.PoolUpdateRolloutCapability}
	out.Channel, out.Phase, out.StateFormat = st.Channel, st.Phase, st.StateFormat
	out.RunningSequence = poolUpdateSlotSequence(st, "booted")
	out.StagedSequence = poolUpdateSlotSequence(st, "armed", "staged")
	out.FreeBytes = poolUpdateFreeBytes()
	out.Busy = st.Running
	if st.Failure != nil {
		out.Failure = st.Failure.Code
	}
	if a := st.Available; a != nil {
		target := &protocol.PoolUpdateTarget{Sequence: uint64(a.Sequence), StateFormat: a.Compat.StateFormat,
			MinControllerAPIEpoch: a.Compat.MinControllerAPIEpoch, MixedPool: a.Compat.MixedPool}
		for _, c := range a.Components {
			if c.Required && !c.OnDemand && c.Bytes > 0 {
				target.RequiredBytes += uint64(c.Bytes)
			}
			if c.Kind == "vm-image" && a.Compat.ControllerFirst {
				target.ControllerImage = true
			}
		}
		out.Target = target
	}
	var err error
	if out.ControllerVM, err = vmhost.HasControllerProfile(ApplianceJournalDir); err != nil {
		return out, errors.New("controller_scan_unavailable: this host's Controller appliance record cannot be read")
	}
	running, err := poolUpdateGuestOps(s).Running(ctx)
	if err != nil {
		return out, errors.New("vm_inventory_unavailable: the running virtual machines cannot be listed")
	}
	out.RunningVMs = running
	return out, nil
}

// PoolUpdateHostStep runs one rollout step on this host. A step is a request to converge, so a
// repeat with the same StepID is answered from the host's current state and never runs twice.
func PoolUpdateHostStep(ctx context.Context, s *Store, step protocol.PoolUpdateStep) protocol.PoolUpdateStepResult {
	if err := step.Valid(); err != nil {
		return protocol.PoolUpdateStepResult{Code: "invalid_step", Message: err.Error()}
	}
	// The step ID is the engine's request ID for stage, activate and rollback; the engine would
	// refuse a malformed one only after the coordinator had already moved on.
	if !nexupdate.ValidRequestID(step.StepID) {
		return protocol.PoolUpdateStepResult{Code: "invalid_step", Message: "step_id must be 8 to 64 letters, digits, - or _"}
	}
	engine := currentImageUpdater()
	if engine == nil {
		return protocol.PoolUpdateStepResult{Code: "unsupported_edition", Message: "this host does not run the image edition"}
	}
	st, refusal := poolUpdateEngineStatus(ctx)
	if refusal != nil {
		return *refusal
	}
	running := poolUpdateSlotSequence(st, "booted")
	switch step.Action {
	case "check":
		// The engine offers no release a host already runs, so there is nothing left to verify.
		if running == step.Sequence {
			return protocol.PoolUpdateStepResult{Done: true}
		}
		if st.Available == nil || uint64(st.Available.Sequence) != step.Sequence {
			if _, err := engine.Check(ctx); err != nil {
				return *poolUpdateRefusal(err)
			}
			if st, refusal = poolUpdateEngineStatus(ctx); refusal != nil {
				return *refusal
			}
		}
		if st.Available == nil || uint64(st.Available.Sequence) != step.Sequence {
			return protocol.PoolUpdateStepResult{Code: "release_unavailable", Message: "this host's channel does not offer the release"}
		}
		return protocol.PoolUpdateStepResult{Done: true}
	case "stage":
		if poolUpdateSlotSequence(st, "armed", "staged") == step.Sequence || running == step.Sequence {
			return protocol.PoolUpdateStepResult{Done: true}
		}
		if _, _, err := engine.Apply(ctx, nexupdate.ApplyRequest{RequestID: step.StepID, Sequence: int(step.Sequence), Activate: "stage_only"}); err != nil {
			return *poolUpdateRefusal(err)
		}
		return poolUpdateOperationResult(ctx, step, false)
	case "controller":
		// controller_first: this host owns the Controller VM and replaces its system image, under the
		// step ID as the swap's operation ID. A repeat resumes or replays the same journaled swap.
		op, _, err := engine.Apply(ctx, nexupdate.ApplyRequest{RequestID: step.StepID, Sequence: int(step.Sequence), Scope: "controller", Activate: "stage_only"})
		if err != nil {
			return *poolUpdateRefusal(err)
		}
		switch {
		case op.Error != nil:
			return protocol.PoolUpdateStepResult{Code: op.Error.Code, Message: op.Error.Message}
		case op.FinishedAt != nil:
			return protocol.PoolUpdateStepResult{Done: true}
		}
		return protocol.PoolUpdateStepResult{}
	case "activate":
		if running == step.Sequence || st.Phase == nexupdate.PhaseArmed {
			return protocol.PoolUpdateStepResult{Done: true}
		}
		// Guests were shut down by the rollout before this step; the engine's own refuse policy
		// is the last guard, so a guest that appeared since blocks the reboot.
		if _, _, err := engine.Activate(ctx, nexupdate.ActionRequest{RequestID: step.StepID, RunningVMs: "refuse"}); err != nil {
			return *poolUpdateRefusal(err)
		}
		return protocol.PoolUpdateStepResult{Done: true}
	case "confirm":
		switch {
		case st.Phase == nexupdate.PhaseRolledBack:
			return protocol.PoolUpdateStepResult{Code: "rolled_back", Message: "the host fell back to its previous release"}
		case running == step.Sequence && !poolUpdateUnconfirmed(st.Phase):
			return protocol.PoolUpdateStepResult{Done: true}
		}
		return protocol.PoolUpdateStepResult{}
	case "rollback":
		if _, _, err := engine.Rollback(ctx, nexupdate.ActionRequest{RequestID: step.StepID, RunningVMs: "refuse"}); err != nil {
			return *poolUpdateRefusal(err)
		}
		return protocol.PoolUpdateStepResult{Done: true}
	case "drain", "undrain":
		return poolUpdateGuestStep(ctx, s, step)
	}
	return protocol.PoolUpdateStepResult{Code: "invalid_step", Message: "unknown step action"}
}

// poolUpdateOperationResult reports an engine operation this step started: still running, done,
// or failed with the engine's own code.
func poolUpdateOperationResult(ctx context.Context, step protocol.PoolUpdateStep, _ bool) protocol.PoolUpdateStepResult {
	st, refusal := poolUpdateEngineStatus(ctx)
	if refusal != nil {
		return *refusal
	}
	if poolUpdateSlotSequence(st, "armed", "staged") == step.Sequence {
		return protocol.PoolUpdateStepResult{Done: true}
	}
	if op := st.Operation; op != nil && op.ID == step.StepID && op.Error != nil {
		return protocol.PoolUpdateStepResult{Code: op.Error.Code, Message: op.Error.Message}
	}
	return protocol.PoolUpdateStepResult{}
}

// poolUpdateGuestStep shuts down (drain) or starts (undrain) exactly the listed guests, so the
// list the master recorded before it asked is the whole of what a step may touch.
func poolUpdateGuestStep(ctx context.Context, s *Store, step protocol.PoolUpdateStep) protocol.PoolUpdateStepResult {
	guests := poolUpdateGuestOps(s)
	running, err := guests.Running(ctx)
	if err != nil {
		return protocol.PoolUpdateStepResult{Code: "vm_inventory_unavailable", Message: "the running virtual machines cannot be listed"}
	}
	isRunning := map[string]bool{}
	for _, id := range running {
		isRunning[id] = true
	}
	done := true
	var last error
	for _, id := range step.VMIDs {
		switch {
		case step.Action == "drain" && isRunning[id]:
			done = false
			if err := guests.Shutdown(ctx, poolUpdateOpID(step.StepID, id), id); err != nil {
				last = err
			}
		case step.Action == "undrain" && !isRunning[id]:
			done = false
			if err := guests.Start(ctx, poolUpdateOpID(step.StepID, id), id); err != nil {
				last = err
			}
		}
	}
	result := protocol.PoolUpdateStepResult{Done: done}
	if last != nil {
		// Not a refusal code: a guest that cannot start yet (the master is rebooting, admission
		// refuses) is retried by the coordinator until its step deadline, under a new step
		// identity because the engine journals a failed operation under its ID.
		result.Message = last.Error()
	}
	return result
}

// poolUpdateBlobFile finds a staged, hash-named blob in the host engine's staging area. Only a
// file whose content hashes to its name is ever served.
func poolUpdateBlobFile(dir, sha string) (string, bool) {
	if len(sha) != 64 || strings.Trim(sha, "0123456789abcdef") != "" {
		return "", false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "staging", "*", sha))
	for _, path := range matches {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		h := sha256.New()
		_, err = io.Copy(h, f)
		f.Close()
		if err == nil && hex.EncodeToString(h.Sum(nil)) == sha {
			return path, true
		}
	}
	return "", false
}

// PoolUpdateHandler serves the three peer routes. step and status are the master's alone; a blob
// is the master's cache and any ready member may read it (it verifies the hash itself).
func PoolUpdateHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		isBlob := strings.HasPrefix(r.URL.Path, poolUpdateBlobPath)
		var err error
		if isBlob {
			_, err = AuthenticatePoolPeer(s, r)
		} else {
			_, err = AuthenticatePoolMaster(s, r)
		}
		if err != nil {
			http.Error(w, "update peer rejected", http.StatusUnauthorized)
			return
		}
		switch {
		case isBlob:
			sha := strings.TrimPrefix(r.URL.Path, poolUpdateBlobPath)
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			membership, err := Membership(s)
			if err != nil || membership.MasterHostID != s.State.HostID {
				http.Error(w, "only the pool master serves the update cache", http.StatusConflict)
				return
			}
			path, ok := poolUpdateBlobFile(poolUpdateImageDir, sha)
			if !ok {
				http.NotFound(w, r)
				return
			}
			f, err := os.Open(path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			http.ServeContent(w, r, "", time.Time{}, f)
		case r.URL.Path == poolUpdateStatusPath && r.Method == http.MethodGet:
			status, err := PoolUpdateHostStatusFor(r.Context(), s)
			if err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
			writePoolUpdateJSON(w, status)
		case r.URL.Path == poolUpdateStepPath && r.Method == http.MethodPost:
			var step protocol.PoolUpdateStep
			if decodePoolUpdateJSON(r.Body, &step) != nil {
				http.Error(w, "invalid update step", http.StatusBadRequest)
				return
			}
			writePoolUpdateJSON(w, PoolUpdateHostStep(r.Context(), s, step))
		default:
			http.NotFound(w, r)
		}
	})
}

func poolUpdatePeerPath(path string) bool {
	return path == poolUpdateStepPath || path == poolUpdateStatusPath || strings.HasPrefix(path, poolUpdateBlobPath)
}

var errPoolUpdateUnsupported = fmt.Errorf("this host does not support %s; upgrade it first", protocol.PoolUpdateRolloutCapability)

// poolUpdatePeerSource is a member's view of its master's blob cache, as an update Source. It has
// no metadata (a member's metadata comes from its channel or an import) and fails closed on
// anything that is not a blob, so nexupdate.PeerFirstSource falls back to the origin.
type poolUpdatePeerSource struct{ store func() *Store }

// PoolUpdatePeerSource returns the member-side source for the master's blob cache. store is called
// per request because the agent store may not exist when the engine is built.
func PoolUpdatePeerSource(store func() *Store) nexupdate.Source { return poolUpdatePeerSource{store} }

func (poolUpdatePeerSource) Fetch(context.Context, string, int64) ([]byte, error) {
	return nil, errors.New("the master's update cache serves blobs only")
}

func (p poolUpdatePeerSource) Blob(ctx context.Context, name string, offset int64, validator string) (io.ReadCloser, bool, string, error) {
	s := p.store()
	sha, ok := strings.CutPrefix(name, "blobs/sha256-")
	if s == nil || !ok || len(sha) != 64 {
		return nil, false, "", errors.New("not a blob of the master's update cache")
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || state.Membership.Valid() != nil || state.Membership.MasterHostID == s.State.HostID || state.MasterURL == "" {
		return nil, false, "", errors.New("this host has no pool master to read a blob from")
	}
	client, err := PoolPeerClient(s, state.MasterURL)
	if err != nil {
		return nil, false, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(state.MasterURL, "/")+poolUpdateBlobPath+sha, nil)
	if err != nil {
		return nil, false, "", err
	}
	if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false, "", err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		return nil, false, "", errors.New("the master's cache does not have this blob")
	}
	return resp.Body, resp.StatusCode == http.StatusPartialContent, resp.Header.Get("ETag"), nil
}
