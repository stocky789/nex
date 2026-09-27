// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var (
	ErrSnapshotInvalid        = errors.New("invalid snapshot request")
	ErrSnapshotConflict       = errors.New("snapshot request conflicts with retained work")
	ErrSnapshotNotFound       = errors.New("snapshot operation not found for this VM")
	ErrSnapshotTargetNotFound = errors.New("snapshot target not found for this VM")
	ErrSnapshotUnavailable    = errors.New("snapshot state is unavailable on this host")
)

// SnapshotRequest enters the same Store owner as Controller commands. A nil
// Command reads an operation, or the catalog when OperationID is empty.
type SnapshotRequest struct {
	VMID        string
	Command     *protocol.Command
	OperationID string
	Reply       chan SnapshotResponse // buffered once; a disconnected caller cannot block Run
}

type SnapshotResponse struct {
	Result    *protocol.Result
	Snapshots []protocol.PoolVMSnapshot
	Err       error
}

func snapshotResult(id string, entry JournalEntry) *protocol.Result {
	return &protocol.Result{OperationID: id, Status: entry.Status, Error: entry.Error, ObservedAt: entry.ObservedAt}
}

func hasUnsettledSnapshot(journal map[string]JournalEntry, vmID string) bool {
	for _, entry := range journal {
		if entry.Command != nil && snapshotCommand(entry.Command.Kind) && (vmID == "" || entry.Command.VM != nil && entry.Command.VM.ID == vmID) && (entry.Status == "pending" || entry.Status == "needs_attention") {
			return true
		}
	}
	return false
}

func snapshotCommand(kind string) bool {
	return kind == "snapshot-create" || kind == "snapshot-revert" || kind == "snapshot-delete"
}

func snapshotTargetCommand(kind string) bool {
	return kind == "snapshot-revert" || kind == "snapshot-delete"
}

func executeStoppedSnapshot(ctx context.Context, dir string, identity State, cmd protocol.Command) (protocol.Result, error) {
	if cmd.VM == nil || identity.HostID == "" || cmd.VM.HostID != identity.HostID || cmd.VM.PoolID != identity.PoolID {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "snapshot command does not address this Core host and pool", ObservedAt: time.Now().UTC()}, nil
	}
	entry := identity.Journal[cmd.OperationID]
	if entry.Order == 0 || entry.Command == nil || !reflect.DeepEqual(*entry.Command, cmd) {
		return protocol.Result{OperationID: cmd.OperationID}, errors.New("snapshot command has no matching durable admission order")
	}
	store := &Store{dir: dir, State: identity}
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	if snapshotTargetCommand(cmd.Kind) && (cmd.Snapshot == nil || cmd.Snapshot.Target == nil) {
		if cmd.Snapshot == nil || !protocol.ValidSnapshotOperationID(cmd.Snapshot.SnapshotID) {
			return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "snapshot target is unavailable", ObservedAt: time.Now().UTC()}, nil
		}
		snapshots, listErr := ListPoolVMSnapshots(ctx, store, cmd.VM.ID)
		if listErr != nil {
			return protocol.Result{OperationID: cmd.OperationID}, listErr
		}
		found := false
		for _, snapshot := range snapshots {
			if snapshot.ID == cmd.Snapshot.SnapshotID {
				descriptor := snapshot
				metadata := *cmd.Snapshot
				metadata.Target = &descriptor
				cmd.Snapshot = &metadata
				found = true
				break
			}
		}
		if !found {
			return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: ErrSnapshotTargetNotFound.Error(), ObservedAt: time.Now().UTC()}, nil
		}
		if cmd.Snapshot.ValidFor(cmd.Kind) != nil {
			return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "snapshot target binding is invalid", ObservedAt: time.Now().UTC()}, nil
		}
	}
	if local, localErr := engine.VMDefinition(cmd.VM.ID); localErr == nil && local.HostID == identity.HostID && local.PoolID == identity.PoolID {
		cmd.VM = &local
	}
	executionCtx, err := snapshotExecutionContext(ctx, store, engine, cmd)
	if err != nil {
		return protocol.Result{OperationID: cmd.OperationID}, err
	}
	var result protocol.Result
	switch cmd.Kind {
	case "snapshot-create":
		result, err = engine.CreateStoppedSnapshot(executionCtx, cmd,
			func(ctx context.Context, command protocol.Command, intent string, created time.Time) (protocol.PoolVMRecoveryReservation, error) {
				return ReservePoolVMSnapshot(ctx, store, command, entry.Order, intent, created)
			},
			func(ctx context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
				return SettlePoolVMSnapshot(ctx, store, evidence)
			})
	case "snapshot-revert":
		result, err = engine.RevertStoppedSnapshot(executionCtx, cmd,
			func(ctx context.Context, command protocol.Command, intent string, captured protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error) {
				return ReservePoolVMSnapshotRevert(ctx, store, command, entry.Order, intent, captured)
			},
			func(ctx context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
				return SettlePoolVMSnapshot(ctx, store, evidence)
			})
	case "snapshot-delete":
		result, err = engine.DeleteStoppedSnapshot(executionCtx, cmd,
			func(ctx context.Context, command protocol.Command, intent string) (protocol.PoolVMRecoveryReservation, error) {
				return ReservePoolVMSnapshotDelete(ctx, store, command, entry.Order, intent)
			},
			func(ctx context.Context, evidence protocol.PoolVMRecoveryEvidence) error {
				return SettlePoolVMSnapshot(ctx, store, evidence)
			})
	default:
		return protocol.Result{OperationID: cmd.OperationID}, errors.New("unsupported snapshot operation")
	}
	if result.OperationID != cmd.OperationID {
		return protocol.Result{OperationID: cmd.OperationID}, errors.New("snapshot executor returned a mismatched operation identity")
	}
	return result, err
}

func executeSnapshotMetadata(ctx context.Context, store *Store, cmd protocol.Command) (protocol.Result, error) {
	observed := time.Now().UTC()
	if cmd.VM == nil || cmd.Snapshot == nil || cmd.Snapshot.ValidFor("snapshot-metadata") != nil {
		return protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "invalid snapshot metadata update", ObservedAt: observed}, nil
	}
	_, err := UpdatePoolVMSnapshotMetadata(ctx, store, cmd.VM.ID, cmd.Snapshot.SnapshotID, cmd.Snapshot.Name, cmd.Snapshot.Description)
	if err != nil {
		status := "failed"
		if errors.Is(err, ErrSnapshotMetadataPending) {
			status = "needs_attention"
		}
		return protocol.Result{OperationID: cmd.OperationID, Status: status, Error: err.Error(), ObservedAt: observed}, nil
	}
	return protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: observed}, nil
}

func snapshotExecutionContext(ctx context.Context, store *Store, engine *vmhost.ManagedEngine, command protocol.Command) (context.Context, error) {
	if command.Kind != "snapshot-revert" {
		return poolVMExecutionContext(ctx, store, *command.VM)
	}
	if command.Snapshot == nil || command.Snapshot.Target == nil {
		return nil, errors.New("snapshot revert target is unavailable")
	}
	captured, err := engine.SnapshotCapturedSpec(*command.Snapshot.Target)
	if err != nil {
		return nil, err
	}
	storage, checks := engine.AppliedStorage()
	if !managedVMChecksReady(checks) {
		return nil, errors.New("managed storage state is unavailable")
	}
	byID := make(map[string]protocol.StorageRegistration, len(storage))
	for _, registration := range storage {
		byID[registration.ID] = registration
	}
	clustered := ""
	for _, spec := range []protocol.VMCreateSpec{*command.VM, captured} {
		for _, disk := range spec.Disks {
			registration, ok := byID[disk.StorageID]
			if !ok {
				return nil, errors.New("snapshot revert storage dependency is unavailable")
			}
			if protocol.IsClusteredISCSI(registration) {
				if clustered != "" && clustered != registration.ID {
					return nil, errors.New("snapshot revert spans distinct clustered storage repositories")
				}
				clustered = registration.ID
			}
		}
	}
	ctx, err = poolVMExecutionContext(ctx, store, *command.VM)
	if err != nil {
		return nil, err
	}
	return poolVMExecutionContext(ctx, store, captured)
}

// acceptSnapshotRequest runs only on Run's Store-owning goroutine. Accepted
// commands outlive the HTTP connection and reuse the existing durable queue.
func acceptSnapshotRequest(ctx context.Context, s *Store, request SnapshotRequest) SnapshotResponse {
	if !uuidLike(request.VMID) {
		return SnapshotResponse{Err: ErrSnapshotInvalid}
	}
	if request.Command == nil {
		if request.OperationID == "" {
			work, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			snapshots, err := ListPoolVMSnapshots(work, s, request.VMID)
			if err != nil {
				return SnapshotResponse{Err: ErrSnapshotUnavailable}
			}
			return SnapshotResponse{Snapshots: snapshots}
		}
		entry, ok := s.State.Journal[request.OperationID]
		if !ok || entry.Command == nil || !snapshotCommand(entry.Command.Kind) || entry.Command.VM == nil || entry.Command.VM.ID != request.VMID {
			return SnapshotResponse{Err: ErrSnapshotNotFound}
		}
		return SnapshotResponse{Result: snapshotResult(request.OperationID, entry)}
	}
	cmd := request.Command
	if request.OperationID != "" || !snapshotCommand(cmd.Kind) || !protocol.ValidSnapshotOperationID(cmd.OperationID) || !validSnapshotAdmissionRequest(cmd) {
		return SnapshotResponse{Err: ErrSnapshotInvalid}
	}
	if old, ok := s.State.Journal[cmd.OperationID]; ok {
		if old.Command == nil || old.Command.Kind != cmd.Kind || old.Command.VM == nil || old.Command.VM.ID != request.VMID || !sameSnapshotRequest(old.Command.Snapshot, cmd.Snapshot, cmd.Kind) {
			return SnapshotResponse{Err: ErrSnapshotConflict}
		}
		if old.Status == "needs_attention" {
			// Re-enter the exact retained native journal for observation. The
			// executor may settle evidence, but must never repeat an uncertain effect.
			old.Status, old.Error = "pending", ""
			s.State.Journal[cmd.OperationID] = old
			if err := s.Save(); err != nil {
				return SnapshotResponse{Err: persistenceError{err}}
			}
		}
		return SnapshotResponse{Result: snapshotResult(cmd.OperationID, old)}
	}
	if snapshotTargetCommand(cmd.Kind) && cmd.Snapshot.SnapshotID == cmd.OperationID {
		return SnapshotResponse{Err: ErrSnapshotInvalid}
	}
	if pendingVMCommand(s.State.Journal, request.VMID) != "" || hasUnsettledSnapshot(s.State.Journal, request.VMID) {
		return SnapshotResponse{Err: ErrSnapshotConflict}
	}
	metadata := *cmd.Snapshot
	if snapshotTargetCommand(cmd.Kind) {
		work, cancel := context.WithTimeout(ctx, 10*time.Second)
		snapshots, err := ListPoolVMSnapshots(work, s, request.VMID)
		cancel()
		if err != nil {
			return SnapshotResponse{Err: ErrSnapshotUnavailable}
		}
		found := false
		for _, snapshot := range snapshots {
			if snapshot.ID == metadata.SnapshotID {
				descriptor := snapshot
				metadata.Target = &descriptor
				found = true
				break
			}
		}
		if !found {
			return SnapshotResponse{Err: ErrSnapshotTargetNotFound}
		}
		if metadata.ValidFor(cmd.Kind) != nil {
			return SnapshotResponse{Err: ErrSnapshotInvalid}
		}
	}
	vm, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMDefinition(request.VMID)
	if err != nil || vm.HostID != s.State.HostID || vm.PoolID != s.State.PoolID || s.State.HostID == "" {
		return SnapshotResponse{Err: ErrSnapshotUnavailable}
	}
	if cmd.Kind == "snapshot-create" && cmd.Snapshot != nil {
		if gate := snapshotAdmissionPowerGate(s, request.VMID, cmd.Snapshot.Mode); gate != nil {
			return SnapshotResponse{Err: gate}
		}
	}
	accepted := protocol.Command{OperationID: cmd.OperationID, Kind: cmd.Kind, VM: &vm, Snapshot: &metadata}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{accepted}}); err != nil {
		return SnapshotResponse{Err: persistenceError{err}}
	}
	return SnapshotResponse{Result: snapshotResult(cmd.OperationID, s.State.Journal[cmd.OperationID])}
}

// snapshotAdmissionPowerGate refuses create modes that conflict with the durable
// pool owner power state when that record is available on this host.
func snapshotAdmissionPowerGate(s *Store, vmID, mode string) error {
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	poolVMMu.Unlock()
	if err != nil {
		return nil // Feasible only when durable owner state is readable.
	}
	record, ok := state.VMs[vmID]
	if !ok {
		return nil
	}
	running := protocol.SnapshotModeRunning(mode)
	switch {
	case running && record.State == "running":
		return nil
	case !running && (record.State == "defined" || record.State == "stopped"):
		return nil
	case running:
		return ErrSnapshotConflict
	default:
		return ErrSnapshotConflict
	}
}

func sameSnapshotRequest(stored, incoming *protocol.VMSnapshotRequest, kind string) bool {
	if stored == nil || incoming == nil {
		return false
	}
	if snapshotTargetCommand(kind) {
		return stored.SnapshotID == incoming.SnapshotID
	}
	return reflect.DeepEqual(stored, incoming)
}

func validSnapshotAdmissionRequest(command *protocol.Command) bool {
	if command == nil || command.Snapshot == nil {
		return false
	}
	if snapshotTargetCommand(command.Kind) {
		return protocol.ValidSnapshotOperationID(command.Snapshot.SnapshotID) && command.Snapshot.Target == nil &&
			command.Snapshot.Name == "" && command.Snapshot.Description == ""
	}
	return command.Snapshot.ValidFor(command.Kind) == nil
}
