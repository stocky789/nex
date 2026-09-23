// SPDX-License-Identifier: AGPL-3.0-only
package agent

// Pool VM execution is deliberately a small, master-owned fence.  Repository
// reservations protect a storage configuration; they cannot say which host may
// run one particular guest.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolVMPath = "/api/v1/pool/vms/"

type poolVMRecord struct {
	PoolID    string `json:"pool_id"`
	VMID      string `json:"vm_id"`
	HostID    string `json:"host_id"`
	State     string `json:"state"` // defined, starting, running, stopped; never inferred from time.
	Autostart bool   `json:"autostart"`
	Epoch     uint64 `json:"epoch"`
	// Spec is the master-owned inventory copy used to re-adopt a pool VM after a
	// Controller replacement.  It is written only from a complete, generation
	// matched host inventory or a validated new vm-create request.
	Spec protocol.VMCreateSpec `json:"spec"`
	// PendingUpdate is a master-owned reservation for one exact next revision.
	// The committed Spec never changes until the owner proves the matching local
	// managed journal succeeded; a timeout therefore blocks rather than adopts
	// an arbitrary later inventory report.
	PendingUpdate *poolVMPendingUpdate `json:"pending_update,omitempty"`
	// LastAbortedUpdate is the single bounded replay receipt retained after an
	// exact failed update-abort response was lost.
	LastAbortedUpdate *poolVMPendingUpdate `json:"last_aborted_update,omitempty"`
}
type poolVMPendingUpdate struct {
	OperationID string                `json:"operation_id"`
	Next        protocol.VMCreateSpec `json:"next"`
}
type poolVMState struct {
	VMs       map[string]poolVMRecord    `json:"vms"`
	Inventory map[string]poolVMInventory `json:"inventory,omitempty"`
}
type poolVMRequest struct {
	VMID   string                 `json:"vm_id,omitempty"`
	Action string                 `json:"action,omitempty"`
	Permit string                 `json:"permit,omitempty"`
	Create bool                   `json:"create,omitempty"`
	VM     *protocol.VMCreateSpec `json:"vm,omitempty"`
	// SpecMatch is an explicit master acknowledgement for recovery admission. It
	// is deliberately absent from older peers, which a new recovery client must
	// treat as unsafe rather than silently accepting a 200 response.
	SpecMatch   bool                     `json:"spec_match,omitempty"`
	Epoch       uint64                   `json:"epoch,omitempty"`
	OperationID string                   `json:"operation_id,omitempty"`
	Previous    *protocol.VMCreateSpec   `json:"previous,omitempty"`
	Update      *vmhost.VMUpdateEvidence `json:"update,omitempty"`
	// UpdateAccepted is set only by the master after its durable record was
	// saved.  It prevents a newer member from mistaking a legacy empty 200 for
	// a definition-transition acknowledgement.
	UpdateAccepted bool `json:"update_accepted,omitempty"`
	UpdateRevision int  `json:"update_revision,omitempty"`
	// PlacementAccepted is an explicit acknowledgement from a current master
	// after its fresh target-host preflight and durable definition reservation.
	PlacementAccepted bool `json:"placement_accepted,omitempty"`
}

// poolVMRecordMatchesSpec binds an interrupted operation to the immutable
// master-owned definition rather than trusting the caller's copy. A later
// inventory observation may refresh a record, so recovery fails closed on any
// revision, disk, or other definition difference.
func poolVMRecordMatchesSpec(record poolVMRecord, want protocol.VMCreateSpec) bool {
	return record.PoolID == want.PoolID && record.VMID == want.ID && record.HostID == want.HostID && protocol.ValidateVMSpec(record.Spec) == nil && reflect.DeepEqual(record.Spec, want)
}

func validPoolVMUpdateOperation(id string) bool {
	return len(id) > 0 && len(id) <= 200 && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "/\\\x00")
}

func poolVMUpdateEvidenceMatches(record poolVMRecord, pending poolVMPendingUpdate, evidence vmhost.VMUpdateEvidence) bool {
	return evidence.Status == "succeeded" && evidence.OperationID == pending.OperationID && reflect.DeepEqual(evidence.Previous, record.Spec) && reflect.DeepEqual(evidence.Current, pending.Next) && evidence.Current.ID == record.VMID && evidence.Current.PoolID == record.PoolID && evidence.Current.HostID == record.HostID && evidence.Current.Revision == record.Spec.Revision+1 && protocol.ValidateVMSpec(evidence.Current) == nil
}

func reservePoolVMUpdate(record *poolVMRecord, owner, operationID string, next protocol.VMCreateSpec) error {
	if record.PoolID == "" || record.HostID != owner || (record.State != "defined" && record.State != "stopped" && record.State != "running") || !validPoolVMUpdateOperation(operationID) || next.ID != record.VMID || next.PoolID != record.PoolID || next.HostID != owner || next.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(next) != nil {
		return errors.New("VM update is not eligible for the durable pool owner record")
	}
	if record.PendingUpdate != nil {
		if record.PendingUpdate.OperationID == operationID && reflect.DeepEqual(record.PendingUpdate.Next, next) {
			return nil
		}
		return errors.New("VM update outcome is pending reconciliation")
	}
	record.PendingUpdate = &poolVMPendingUpdate{OperationID: operationID, Next: next}
	record.LastAbortedUpdate = nil
	return nil
}

// reserveOrSeedPoolVMUpdate creates a first pool owner record only from the
// authenticated member's current-generation inventory. The caller-supplied
// Previous is a comparison value, never an authority to adopt or transfer a
// local VM definition.
func reserveOrSeedPoolVMUpdate(state *poolVMState, membership protocol.PoolMembership, record *poolVMRecord, exists bool, owner, operationID string, previous, next *protocol.VMCreateSpec) error {
	if previous == nil || next == nil || previous.ID != next.ID || previous.PoolID != next.PoolID || previous.HostID != owner || next.HostID != owner || next.Revision != previous.Revision+1 || protocol.ValidateVMSpec(*previous) != nil {
		return errors.New("VM update previous definition is invalid")
	}
	if exists {
		if !poolVMRecordMatchesSpec(*record, *previous) {
			return errors.New("VM update previous definition does not match the durable pool owner record")
		}
		return reservePoolVMUpdate(record, owner, operationID, *next)
	}
	memberReady := false
	for _, member := range membership.Members {
		if member.HostID == owner && member.Status == "ready" && member.Generation == membership.Generation {
			memberReady = true
		}
	}
	inv, inventoryOK := state.Inventory[owner]
	if !memberReady || !inventoryOK || inv.Generation != membership.Generation || inv.HostID != owner || !reflect.DeepEqual(findPoolVMInventorySpec(inv, previous.ID), *previous) {
		return errors.New("VM update requires an exact current owner inventory")
	}
	observed := inv.Observed[previous.ID]
	if observed != "running" && observed != "shut off" {
		return errors.New("VM update owner runtime observation is unavailable")
	}
	stateName := "stopped"
	if observed == "running" {
		stateName = "running"
	}
	*record = poolVMRecord{PoolID: previous.PoolID, VMID: previous.ID, HostID: owner, State: stateName, Autostart: previous.Autostart, Epoch: 1, Spec: *previous}
	return reservePoolVMUpdate(record, owner, operationID, *next)
}

func findPoolVMInventorySpec(inv poolVMInventory, id string) protocol.VMCreateSpec {
	var found protocol.VMCreateSpec
	for _, vm := range inv.VMs {
		if vm.ID != id {
			continue
		}
		if found.ID != "" {
			return protocol.VMCreateSpec{}
		}
		found = vm
	}
	return found
}

func commitPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || record.PendingUpdate == nil || !poolVMUpdateEvidenceMatches(*record, *record.PendingUpdate, evidence) {
		return errors.New("VM update journal evidence does not match the durable pool owner record")
	}
	record.Spec, record.Autostart, record.PendingUpdate = evidence.Current, evidence.Current.Autostart, nil
	return nil
}

// abortPoolVMUpdate releases only a journal-proven failed, unapplied update.
// Timeouts, needs_attention, and any evidence of a newer definition deliberately
// leave the pending record in place: another execution could still have changed
// the guest and must be reconciled rather than retried as a new update.
func abortPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || evidence.Status != "failed" || !reflect.DeepEqual(evidence.Previous, record.Spec) || evidence.Current.ID != record.VMID || evidence.Current.PoolID != record.PoolID || evidence.Current.HostID != record.HostID || evidence.Current.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(evidence.Current) != nil {
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	if record.PendingUpdate == nil {
		// Retrying an exact authenticated abort after its response was lost is a
		// read/compare acknowledgement. It cannot release a later reservation.
		if record.LastAbortedUpdate != nil && evidence.OperationID == record.LastAbortedUpdate.OperationID && reflect.DeepEqual(evidence.Current, record.LastAbortedUpdate.Next) {
			return nil
		}
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	if evidence.OperationID != record.PendingUpdate.OperationID || !reflect.DeepEqual(evidence.Current, record.PendingUpdate.Next) {
		return errors.New("VM update failure evidence does not match the durable pool owner record")
	}
	record.LastAbortedUpdate = record.PendingUpdate
	record.PendingUpdate = nil
	return nil
}

// recoverPoolVMUpdate accepts only a journal-backed, one-revision successor
// from the already recorded owner. It is the bounded recovery path for an
// update applied by an older Core before update reservations existed; inventory
// alone never reaches this function.
func recoverPoolVMUpdate(record *poolVMRecord, owner string, evidence vmhost.VMUpdateEvidence) error {
	if record.PoolID == "" || record.HostID != owner || record.PendingUpdate != nil || evidence.Status != "succeeded" || !validPoolVMUpdateOperation(evidence.OperationID) || !reflect.DeepEqual(evidence.Previous, record.Spec) || evidence.Current.ID != record.VMID || evidence.Current.PoolID != record.PoolID || evidence.Current.HostID != record.HostID || evidence.Current.Revision != record.Spec.Revision+1 || protocol.ValidateVMSpec(evidence.Current) != nil {
		return errors.New("VM update journal evidence does not match the durable pool owner record")
	}
	record.Spec, record.Autostart = evidence.Current, evidence.Current.Autostart
	return nil
}

var poolVMMu sync.Mutex

const poolVMAutostartAttemptLimit = 4 << 10

type poolVMAutostartAttempt struct {
	VMID   string `json:"vm_id"`
	BootID string `json:"boot_id"`
}

var poolVMAutostartMu sync.Mutex
var poolVMAutostartBootID = func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	bootID := strings.TrimSpace(string(b))
	if err != nil || !validPoolVMAutostartID(bootID) {
		return "", errors.New("local boot identity is unavailable")
	}
	return bootID, nil
}
var poolVMAutostartOwner = func(uid uint32) bool {
	return uid == 0 || uid == uint32(os.Geteuid())
}

func validPoolVMAutostartID(id string) bool {
	return id == strings.ToLower(id) && uuidLike(id)
}

// ClaimPoolVMAutostartAttempt durably records one autostart reconciliation for
// vmID during the current OS boot. A failed claim is fail-closed: callers must
// not infer that a restart of the agent is permission to start a stopped VM.
func ClaimPoolVMAutostartAttempt(stateDir, vmID string) (bool, error) {
	if !validPoolVMAutostartID(vmID) {
		return false, errors.New("invalid pool VM identity")
	}
	bootID, err := poolVMAutostartBootID()
	if err != nil {
		return false, err
	}

	poolVMAutostartMu.Lock()
	defer poolVMAutostartMu.Unlock()
	dir, err := poolVMAutostartAttemptDir(stateDir)
	if err != nil {
		return false, err
	}
	path := filepath.Join(dir, vmID+"-"+bootID+".json")
	fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		if errors.Is(err, syscall.EEXIST) {
			return poolVMAutostartAttemptExists(path, vmID, bootID)
		}
		return false, err
	}
	f := os.NewFile(uintptr(fd), path)
	b, err := json.Marshal(poolVMAutostartAttempt{VMID: vmID, BootID: bootID})
	if err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		// Do not remove a partial claim: a later attempt must fail closed rather
		// than treat an interrupted durable write as permission to start.
		return false, err
	}
	d, err := os.Open(dir)
	if err != nil {
		return false, err
	}
	defer d.Close()
	if err = d.Sync(); err != nil {
		return false, err
	}
	return true, nil
}

func poolVMAutostartAttemptDir(stateDir string) (string, error) {
	if err := trustedPoolVMAutostartStateDir(stateDir); err != nil {
		return "", errors.New("Core state directory is unsafe")
	}
	dir := filepath.Join(stateDir, "pool-vm-autostart-attempts")
	if err := os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := trustedPoolVMAutostartDir(dir); err != nil {
		return "", errors.New("pool VM autostart state directory is unsafe")
	}
	return dir, nil
}

func poolVMAutostartAttemptExists(path, vmID, bootID string) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0600 || !poolVMAutostartOwned(info) {
		return false, errors.New("pool VM autostart attempt is unsafe")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return false, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0077 != 0 || !poolVMAutostartOwner(stat.Uid) {
		return false, errors.New("pool VM autostart attempt changed or is unsafe")
	}
	b, err := io.ReadAll(io.LimitReader(f, poolVMAutostartAttemptLimit+1))
	if err != nil || len(b) > poolVMAutostartAttemptLimit {
		return false, errors.New("pool VM autostart attempt is unreadable")
	}
	var attempt poolVMAutostartAttempt
	if json.Unmarshal(b, &attempt) != nil || attempt.VMID != vmID || attempt.BootID != bootID {
		return false, errors.New("pool VM autostart attempt is invalid")
	}
	return false, nil
}

func poolVMAutostartOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && poolVMAutostartOwner(stat.Uid)
}

func trustedPoolVMAutostartDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !poolVMAutostartOwned(info) {
		return errors.New("unsafe private directory")
	}
	return nil
}

func trustedPoolVMAutostartStateDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("unsafe Core state directory path")
	}
	if err := trustedPoolVMAutostartDir(path); err != nil {
		return err
	}
	// A writable ancestor may rename the state directory between validation and
	// creation. Sticky parents protect an entry owned by this Core; all other
	// ancestors must not be writable by group or other users.
	for parent := filepath.Dir(path); ; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe Core state parent")
		}
		if info.Mode().Perm()&0022 != 0 {
			if info.Mode()&os.ModeSticky == 0 || !poolVMAutostartOwned(info) {
				return errors.New("writable Core state parent is unsafe")
			}
		}
		if parent == "/" {
			return nil
		}
	}
}

// prunePoolVMOwners removes owner records for VMs their owner has provably deleted. The
// rule is positive evidence only, never absence: a record is removed when the host that
// just published its inventory is the record's owner, that inventory carries a succeeded
// vm-delete tombstone (or a create it proved failed before allocating) for the same pool
// and host, and no other host's stored inventory still defines that VM. A deleted VM ID
// can never be defined again on the same host, so the tombstone cannot prune a later VM,
// and a pending update for it can no longer take effect.
// Everything else is kept: an unreported, unreachable or mid-placement VM keeps its owner
// record, because a lost record is a lost fence. It returns the pruned IDs.
func prunePoolVMOwners(state *poolVMState, observedHost string) []string {
	inventory, ok := state.Inventory[observedHost]
	if !ok || len(inventory.Deleted) == 0 {
		return nil
	}
	defined := map[string]bool{}
	for host, inv := range state.Inventory {
		if host == observedHost {
			continue
		}
		for _, vm := range inv.VMs {
			defined[vm.ID] = true
		}
	}
	var pruned []string
	for _, id := range inventory.Deleted {
		record, exists := state.VMs[id]
		if !exists || record.HostID != observedHost || defined[id] {
			continue
		}
		delete(state.VMs, id)
		pruned = append(pruned, id)
	}
	return pruned
}

func poolVMFile(s *Store) string { return filepath.Join(s.dir, "pool-vms.json") }
func loadPoolVMs(s *Store) (poolVMState, error) {
	out := poolVMState{VMs: map[string]poolVMRecord{}, Inventory: map[string]poolVMInventory{}}
	b, err := os.ReadFile(poolVMFile(s))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	if json.Unmarshal(b, &out) != nil || out.VMs == nil {
		return out, errors.New("invalid pool VM state")
	}
	if out.Inventory == nil {
		out.Inventory = map[string]poolVMInventory{}
	}
	return out, nil
}
func savePoolVMs(s *Store, state poolVMState) error {
	b, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(poolVMFile(s), b, 0600)
}

func poolSharedVM(ctx context.Context, s *Store, cmd protocol.Command) (protocol.VMCreateSpec, bool, error) {
	var v protocol.VMCreateSpec
	if cmd.VM != nil {
		v = *cmd.VM
	} else if cmd.Power != nil {
		state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return v, false, errors.New("managed VM state is unavailable")
		}
		for _, candidate := range state.VMs {
			if candidate.ID == cmd.Power.VMID {
				v = candidate
				break
			}
		}
	} else if cmd.VMMedia != nil {
		state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return v, false, errors.New("managed VM state is unavailable")
		}
		for _, candidate := range state.VMs {
			if candidate.ID == cmd.VMMedia.VMID {
				v = candidate
				break
			}
		}
	}
	if v.ID == "" {
		return v, false, nil
	}
	state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return v, false, errors.New("managed storage state is unavailable")
	}
	definitions := []protocol.VMCreateSpec{v}
	if cmd.Kind == "vm-update" {
		for _, current := range state.VMs {
			if current.ID == v.ID {
				definitions = append(definitions, current)
				break
			}
		}
	}
	for _, definition := range definitions {
		for _, disk := range definition.Disks {
			for _, storage := range state.Storage {
				if storage.ID == disk.StorageID && isPoolSharedStorage(storage) {
					return v, true, nil
				}
			}
		}
	}
	// A VM which ever used a successfully registered pool-shared repository
	// remains pool-owned after an edit removes its final shared disk or after
	// that repository is detached. The proof is read-only managed history, not
	// a member policy cache.
	if cmd.Kind == "vm-update" || cmd.Power != nil || cmd.VMMedia != nil {
		used, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).VMHasPoolSharedStorageHistory(ctx, v.ID)
		if err != nil {
			return v, false, errors.New("managed VM shared-storage history is unavailable")
		}
		if used {
			return v, true, nil
		}
	}
	return v, false, nil
}

func poolVMUpdatePrevious(ctx context.Context, s *Store, id string) (*protocol.VMCreateSpec, error) {
	state, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	if !managedVMChecksReady(checks) {
		return nil, errors.New("managed VM state is unavailable")
	}
	for _, candidate := range state.VMs {
		if candidate.ID == id {
			previous := candidate
			return &previous, nil
		}
	}
	return nil, errors.New("managed VM definition is unavailable")
}

// ManagedEngine always reports observational checks.  Healthy observations are
// positive evidence; only failed or unknown journal/libvirt state blocks a
// shared-disk execution admission.
func managedVMChecksReady(checks []protocol.Check) bool {
	if len(checks) == 0 {
		return false
	}
	for _, check := range checks {
		if check.Status != "healthy" && check.Status != "passed" {
			return false
		}
	}
	return true
}

func isPoolSharedStorage(s protocol.StorageRegistration) bool {
	return protocol.IsPoolSharedStorage(s)
}

// poolVMExecutionContext binds a VM operation to the master-approved clustered
// storage profile.  It is deliberately derived from the local managed journal,
// never from the client command. ValidateClusteredISCSIAt is also a live native
// readiness gate before a start permit is written.
func poolVMExecutionContext(ctx context.Context, s *Store, v protocol.VMCreateSpec) (context.Context, error) {
	storage, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).AppliedStorage()
	if !managedVMChecksReady(checks) {
		return nil, errors.New("managed storage state is unavailable")
	}
	byID := map[string]protocol.StorageRegistration{}
	for _, storage := range storage {
		byID[storage.ID] = storage
	}
	var selected protocol.StorageClusterConfig
	for _, disk := range v.Disks {
		storage, ok := byID[disk.StorageID]
		if !ok || !protocol.IsClusteredISCSI(storage) {
			continue
		}
		config, err := StorageClusterConfigForRepository(s, storage.ID)
		if err != nil || config.ValidFor(storage) != nil || !config.FencingVerified() {
			return nil, errors.New("clustered VM storage configuration is unavailable or unsafe")
		}
		membership, err := Membership(s)
		if err != nil || !poolVMClusterOperationActivated(s, membership, config) {
			return nil, errors.New("clustered VM storage has not completed safe initial activation for the current pool membership")
		}
		if selected.RepositoryID != "" && selected.RepositoryID != config.RepositoryID {
			return nil, errors.New("a VM may not use multiple clustered iSCSI repositories")
		}
		if _, err := vmhost.ValidateClusteredISCSIAt(ctx, s.dir, storage, config); err != nil {
			return nil, errors.New("clustered VM storage is not ready")
		}
		selected = config
	}
	if selected.RepositoryID != "" {
		return vmhost.WithStorageClusterStateDir(vmhost.WithStorageClusterConfig(ctx, selected), s.dir), nil
	}
	return ctx, nil
}

// poolVMClusterOperationActivated distinguishes an initial, all-member Core
// admission from later runtime health. The native validator that follows this
// check remains authoritative for current local quorum/fencing readiness.
func poolVMClusterOperationActivated(s *Store, membership protocol.PoolMembership, config protocol.StorageClusterConfig) bool {
	if !clusterCurrentMember(s.State.HostID, membership, config) {
		return false
	}
	for _, op := range StorageClusterStatus(s) {
		if clusterOperationActivated(op, membership, config) {
			return true
		}
	}
	return false
}

func poolVMCommandExecutionContext(ctx context.Context, s *Store, cmd protocol.Command) (context.Context, error) {
	if cmd.Kind == "storage-recheck" && cmd.StorageLifecycle != nil {
		managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
		if !managedVMChecksReady(checks) {
			return ctx, errors.New("managed storage state is unavailable")
		}
		for _, storage := range managed.Storage {
			if storage.ID != cmd.StorageLifecycle.RepositoryID || !protocol.IsClusteredISCSI(storage) {
				continue
			}
			config, err := StorageClusterConfigForRepository(s, storage.ID)
			if err != nil || config.ValidFor(storage) != nil || !config.FencingVerified() {
				return ctx, errors.New("clustered storage recheck configuration is unavailable or unsafe")
			}
			membership, err := Membership(s)
			if err != nil || !poolVMClusterOperationActivated(s, membership, config) {
				return ctx, errors.New("clustered storage recheck has not completed safe initial activation for the current pool membership")
			}
			return vmhost.WithStorageClusterStateDir(vmhost.WithStorageClusterConfig(ctx, config), s.dir), nil
		}
	}
	v, shared, err := poolSharedVM(ctx, s, cmd)
	if err != nil || !shared {
		return ctx, err
	}
	if err := poolVMStopOnly(s, cmd, v); err == nil {
		// A stop only reduces the locally observed execution set.  It neither
		// opens the clustered filesystem nor writes a start permit, so a lost
		// quorum must not prevent an operator from stopping the exact local
		// owner.  All state-changing admissions remain below.
		return ctx, nil
	} else if isPoolVMStopOnly(cmd) {
		return ctx, err
	}
	return poolVMExecutionContext(ctx, s, v)
}

// isPoolVMStopOnly identifies the two reducing-only power requests.  Reboot and
// force-reboot deliberately remain execution admissions: they can resume a writable VM.
func isPoolVMStopOnly(cmd protocol.Command) bool {
	return cmd.Kind == "vm-power" && cmd.Power != nil && (cmd.Power.Action == "shutdown" || cmd.Power.Action == "force-stop")
}

// poolVMStartsExecution identifies the requests that begin a new execution on this host: a
// power start, the S3 wake AdmitPoolVMExecution turns a start into, and a create that asks
// for the guest to be started. A reboot or force-reboot needs a guest that is already
// running here, so it is not a new placement and maintenance never refuses it.
func poolVMStartsExecution(cmd protocol.Command) bool {
	if cmd.Kind == "vm-create" {
		return cmd.VM != nil && cmd.VM.StartAfterCreate
	}
	return cmd.Kind == "vm-power" && cmd.Power != nil && (cmd.Power.Action == "start" || cmd.Power.Action == "wake")
}

// poolVMStopOnly proves a stop request is for the exact locally journaled
// owner before allowing it to bypass live clustered-storage readiness.  It
// does not contact or alter the master record; a fresh authenticated inventory
// observation reconciles a confirmed stop after quorum returns.
func poolVMStopOnly(s *Store, cmd protocol.Command, v protocol.VMCreateSpec) error {
	if !isPoolVMStopOnly(cmd) {
		return errors.New("VM power action is not stop-only")
	}
	if err := vmhost.ValidateManagedCommand(cmd); err != nil {
		return err
	}
	p := cmd.Power
	if v.ID == "" || p.VMID != v.ID || p.PoolID != s.State.PoolID || p.HostID != s.State.HostID || v.PoolID != s.State.PoolID || v.HostID != s.State.HostID {
		return errors.New("stop request does not match the local pool VM owner")
	}
	return nil
}

func randomPermit() (string, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return hex.EncodeToString(b), err
}
func uuidLike(id string) bool {
	if len(id) != 36 || id[8] != '-' || id[13] != '-' || id[18] != '-' || id[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	return err == nil
}

// PoolVMHandler is mounted by PoolPeerHandler.  It accepts requests only from a ready member;
// the master writes the record before returning a start permit.  There is intentionally no
// expiry or timeout cleanup: unknown execution remains fenced.
func PoolVMHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		member, err := AuthenticatePoolPeer(s, r)
		if err != nil {
			http.Error(w, "peer rejected", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == poolVMPath+"inventory" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var inv poolVMInventory
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&inv) != nil || inv.HostID != member.HostID {
				http.Error(w, "invalid VM inventory", http.StatusBadRequest)
				return
			}
			membership, err := Membership(s)
			if err != nil {
				http.Error(w, "membership unavailable", http.StatusConflict)
				return
			}
			if err = recordPoolVMInventory(s, membership, inv); err != nil {
				if errors.Is(err, errPoolVMInventoryIncomplete) || errors.Is(err, errPoolVMOwnersDeferred) {
					// The snapshot is durable but cannot activate legacy owner
					// adoption until every current ready member has reported.
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if errors.Is(err, errPoolVMInventoryStale) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusConflict)
					_ = json.NewEncoder(w).Encode(poolVMInventoryReply{Membership: membership})
					return
				}
				http.Error(w, "VM inventory refused", http.StatusConflict)
				return
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, poolVMPath)
		if id == "" || strings.ContainsAny(id, "/\\") {
			http.NotFound(w, r)
			return
		}
		var req poolVMRequest
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !uuidLike(id) || json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req) != nil || req.VMID != id {
			http.Error(w, "invalid VM execution request", http.StatusBadRequest)
			return
		}
		req.PlacementAccepted, req.UpdateAccepted, req.SpecMatch = false, false, false
		// A definition placement is decided by the master for every VM, including
		// local-storage guests.  Do the bounded target RPC before the durable VM
		// lock; locks must never span a peer request.
		var placement poolPlacementReply
		if req.Action == "define" && req.Create || req.Action == "update-reserve" {
			membership, membershipErr := Membership(s)
			cmdKind := "vm-create"
			if req.Action == "update-reserve" {
				cmdKind = "vm-update"
			}
			command := protocol.Command{OperationID: req.OperationID, Kind: cmdKind, VM: req.VM}
			if membershipErr != nil || req.OperationID == "" || req.VM == nil {
				http.Error(w, "VM placement preflight is unavailable", http.StatusConflict)
				return
			}
			placement, membershipErr = poolVMPlacement(r.Context(), s, membership, member, command)
			if membershipErr == nil {
				membershipErr = poolVMPlacementStorageMatches(r.Context(), s, placement, *req.VM)
			}
			if membershipErr != nil {
				http.Error(w, "VM placement preflight refused: "+placementError(membershipErr), http.StatusConflict)
				return
			}
		}
		poolVMMu.Lock()
		defer poolVMMu.Unlock()
		// A frozen or handed-over pool authority refuses new ownership and new start
		// permits here, inside the same lock section that would record them, which is what
		// makes the freeze an ordering guarantee rather than a hint.
		if err := requirePoolVMAuthorityWritable(s, req.Action, req.Create); err != nil {
			http.Error(w, placementError(err), http.StatusConflict)
			return
		}
		// All placement admission paths take the established VM → peer lock order.
		// Keep the peer lock through savePoolVMs so membership cannot change between
		// the authenticated preflight and durable owner/reservation record.
		placementMutation := (req.Action == "define" && req.Create) || req.Action == "update-reserve"
		var currentMembership protocol.PoolMembership
		if placementMutation {
			poolPeerMu.Lock()
			defer poolPeerMu.Unlock()
			peer, peerErr := loadPoolPeer(s)
			if peerErr != nil || peer.Membership.Valid() != nil {
				http.Error(w, "VM placement membership is unavailable", http.StatusConflict)
				return
			}
			currentMembership = peer.Membership
		}
		state, err := loadPoolVMs(s)
		if err != nil {
			http.Error(w, "execution state unavailable", 500)
			return
		}
		if placementMutation && !placementMembershipMatches(s, currentMembership, placement.Generation, member) {
			http.Error(w, "VM placement membership changed before durable admission", http.StatusConflict)
			return
		}
		record, ok := state.VMs[id]
		if placement.Replay && (!ok || (req.Action == "update-reserve" && !placementReservationMatches(record, req.OperationID, req.VM))) {
			http.Error(w, "VM placement replay has no durable master record", http.StatusConflict)
			return
		}
		switch req.Action {
		case "update-reserve":
			if req.VM == nil || !placementMutation || vmhost.ValidateManagedCommand(protocol.Command{OperationID: req.OperationID, Kind: "vm-update", VM: req.VM}) != nil || reserveOrSeedPoolVMUpdate(&state, currentMembership, &record, ok, member.HostID, req.OperationID, req.Previous, req.VM) != nil {
				http.Error(w, "VM update is not eligible for the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil && record.PendingUpdate != nil {
				req.UpdateAccepted = true
				req.UpdateRevision = record.PendingUpdate.Next.Revision
				req.PlacementAccepted = true
			}
		case "update-commit":
			if !ok || req.Update == nil || commitPoolVMUpdate(&record, member.HostID, *req.Update) != nil {
				http.Error(w, "VM update journal evidence does not match the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.UpdateAccepted = true
				req.UpdateRevision = record.Spec.Revision
			}
		case "update-abort":
			if !ok || req.Update == nil || abortPoolVMUpdate(&record, member.HostID, *req.Update) != nil {
				http.Error(w, "VM update failure evidence does not match the durable pool owner record", http.StatusConflict)
				return
			}
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.UpdateAccepted = true
				req.UpdateRevision = req.Update.Current.Revision
			}
		case "define":
			if ok && (record.PendingUpdate != nil || record.PoolID != s.State.PoolID || record.HostID != member.HostID) {
				http.Error(w, "VM is owned by another host", http.StatusConflict)
				return
			}
			if ok && req.VM != nil && !poolVMRecordMatchesSpec(record, *req.VM) {
				http.Error(w, "VM definition does not match the durable pool owner record", http.StatusConflict)
				return
			}
			if !ok && !req.Create {
				http.Error(w, "VM has no durable pool execution record", http.StatusConflict)
				return
			}
			if !ok {
				if req.VM == nil || req.VM.ID != id || req.VM.PoolID != s.State.PoolID || req.VM.HostID != member.HostID || protocol.ValidateVMSpec(*req.VM) != nil {
					http.Error(w, "invalid VM definition", http.StatusBadRequest)
					return
				}
				record = poolVMRecord{PoolID: s.State.PoolID, VMID: id, HostID: member.HostID, State: "defined", Autostart: req.VM.Autostart, Epoch: 1, Spec: *req.VM}
				state.VMs[id] = record
				err = savePoolVMs(s, state)
			}
			if req.VM != nil && err == nil && poolVMRecordMatchesSpec(record, *req.VM) {
				req.SpecMatch = true
				if req.Create {
					req.PlacementAccepted = true
				}
			}
		case "start":
			// A drained host starts nothing new. Stop and the transitions that follow a
			// start already under way are deliberately not refused here.
			if err := poolHostInMaintenance(s, member.HostID); err != nil {
				http.Error(w, placementError(err), http.StatusConflict)
				return
			}
			if !ok || record.PendingUpdate != nil || record.PoolID != s.State.PoolID || record.HostID != member.HostID || (record.State != "defined" && record.State != "stopped") {
				http.Error(w, "VM execution is not eligible to start", http.StatusConflict)
				return
			}
			record.State, record.Epoch = "starting", record.Epoch+1
			state.VMs[id] = record
			err = savePoolVMs(s, state)
			if err == nil {
				req.Permit, err = randomPermit()
			}
		case "running":
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID || record.State != "starting" {
				http.Error(w, "VM execution transition refused", http.StatusConflict)
				return
			}
			record.State = "running"
			state.VMs[id] = record
			err = savePoolVMs(s, state)
		case "stop":
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID {
				http.Error(w, "VM execution owner mismatch", http.StatusConflict)
				return
			}
		case "stopped":
			// Only the recorded owner may prove a stop.  It retains placement; another host
			// cannot claim the VM merely because this host is quiet or unreachable.
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID {
				http.Error(w, "VM execution owner mismatch", http.StatusConflict)
				return
			}
			record.State = "stopped"
			state.VMs[id] = record
			err = savePoolVMs(s, state)
		case "reboot", "force-reboot", "wake", "media":
			// wake resumes a guest that put itself to sleep (ACPI S3): the domain is
			// still this owner's running execution, so the record keeps state and epoch.
			// It is nevertheless an operator pressing Start, so a drained host refuses it;
			// a reboot of a guest already executing there is not a new placement and is
			// deliberately allowed.
			if req.Action == "wake" {
				if err := poolHostInMaintenance(s, member.HostID); err != nil {
					http.Error(w, placementError(err), http.StatusConflict)
					return
				}
			}
			if !ok || record.PendingUpdate != nil || record.HostID != member.HostID || (req.Action != "media" && record.State != "running") {
				http.Error(w, "VM execution is not eligible for this action", http.StatusConflict)
				return
			}
		default:
			http.Error(w, "unsupported VM execution action", http.StatusBadRequest)
			return
		}
		if err != nil {
			http.Error(w, "execution state persistence failed", 500)
			return
		}
		req.Epoch = record.Epoch
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(req)
	})
}

// poolVMMediaChangesDefinition reports whether a media request would rewrite the durable VM
// definition the pool master owns.  Re-inserting the ISO that definition already names does
// not: it reconnects a tray, which is how an operator recovers a disc the guest ejected.
func poolVMMediaChangesDefinition(v protocol.VMCreateSpec, req protocol.VMMediaRequest) bool {
	return !reflect.DeepEqual(protocol.ApplyVMMedia(v, req.Action, req.Drive, req.MediaID), v)
}

// PoolVMMediaCommand expresses a shared VM's media change as the same durable prior→next
// definition transition vm-update uses, so the master reserves the exact next revision
// before this host applies it and its committed Spec cannot drift.  The operation keeps its
// identity, so the Controller still tracks the media task it queued. A media
// request that leaves the definition unchanged keeps the lighter vm-media path.
func PoolVMMediaCommand(ctx context.Context, s *Store, cmd protocol.Command) protocol.Command {
	if cmd.Kind != "vm-media" || cmd.VMMedia == nil {
		return cmd
	}
	// A redelivered operation is applied exactly as it was journaled.  The current
	// definition already carries the first attempt's change, so deriving the next revision
	// again would make one operation ID mean two different commands.
	if journaled, ok := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).JournaledCommand(cmd.OperationID); ok {
		if journaled.Kind == "vm-media" && reflect.DeepEqual(journaled, cmd) {
			return journaled
		}
		if journaled.Kind == "vm-update" && journaled.VM != nil && journaled.VM.ID == cmd.VMMedia.VMID && journaled.VM.PoolID == cmd.VMMedia.PoolID && journaled.VM.HostID == cmd.VMMedia.HostID && reflect.DeepEqual(protocol.ApplyVMMedia(*journaled.VM, cmd.VMMedia.Action, cmd.VMMedia.Drive, cmd.VMMedia.MediaID), *journaled.VM) {
			return journaled
		}
		return cmd
	}
	v, _, err := poolSharedVM(ctx, s, cmd)
	if err != nil || !poolVMMediaChangesDefinition(v, *cmd.VMMedia) {
		return cmd
	}
	next := protocol.ApplyVMMedia(v, cmd.VMMedia.Action, cmd.VMMedia.Drive, cmd.VMMedia.MediaID)
	next.Revision, next.StartAfterCreate = v.Revision+1, false
	update := protocol.Command{OperationID: cmd.OperationID, Kind: "vm-update", VM: &next}
	if vmhost.ValidateManagedCommand(update) != nil {
		return cmd
	}
	return update
}

// AdmitPoolVMExecution must wrap every managed create/start/stop.  Its returned callback
// records only a confirmed local observation; it never frees ownership after an error/timeout.
func AdmitPoolVMExecution(ctx context.Context, s *Store, cmd protocol.Command) (func(protocol.Result, error) error, error) {
	// A host that cannot prove it holds its pool's current authority starts no workload at
	// all and does not even ask its master for a permit. It is decided first, before any
	// storage or runtime observation, so a restricted host refuses in its own words rather
	// than failing for an unrelated reason, and it covers local-storage guests too: the
	// recovery decision is that a stale host blocks local and libvirt autostarts and other
	// workload starts until it has reconciled. Stop, shutdown and force-stop are never
	// blocked — a restricted host must always be able to put a guest down — and local
	// console and recovery access are untouched.
	if !isPoolVMStopOnly(cmd) {
		if err := requirePoolStartPath(s); err != nil {
			return nil, err
		}
	}
	// A drained host starts nothing new, for exactly the same reason and in exactly the
	// same place: the early return below is where every local-storage-only guest leaves
	// this function, before any master contact, so a maintenance guard downstream of that
	// round trip never sees the most common guest shape. Reboots and every reducing power
	// request are deliberately not refused here.
	if poolVMStartsExecution(cmd) {
		if err := poolHostInMaintenance(s, s.State.HostID); err != nil {
			return nil, err
		}
	}
	v, shared, err := poolSharedVM(ctx, s, cmd)
	if err != nil {
		return nil, err
	}
	definition := cmd.Kind == "vm-create" || cmd.Kind == "vm-update"
	// Placement ownership covers every VM definition. The shared-storage fence
	// below still covers only operations which could create a second writer.
	if !shared && !definition {
		return func(protocol.Result, error) error { return nil }, nil
	}
	stopErr := poolVMStopOnly(s, cmd, v)
	if shared && stopErr == nil {
		// Do not turn loss of quorum into an inability to stop a known local
		// writer.  In particular, do not write a start permit, call an
		// unreachable master, or infer a durable "stopped" transition from a
		// request timeout.  The Engine journals and observes the native request;
		// later authenticated inventory is the only reconciliation path.
		return func(protocol.Result, error) error { return nil }, nil
	} else if shared && isPoolVMStopOnly(cmd) {
		return nil, stopErr
	}
	if shared {
		if _, err := poolVMExecutionContext(ctx, s, v); err != nil {
			return nil, err
		}
	}
	if (shared || definition) && cmd.Kind != "vm-create" {
		if err := PublishPoolVMInventory(ctx, s); err != nil {
			return nil, err
		}
	}
	action := ""
	switch cmd.Kind {
	case "vm-create":
		action = "define"
	case "vm-update":
		action = "update-reserve"
	case "vm-power":
		action = cmd.Power.Action
	case "vm-media":
		// recordVMMedia changes the durable VM definition (MediaID and, on eject,
		// media boot order). A simple owner permit would let that new revision
		// diverge from the master's immutable Spec, so a media change that rewrites
		// the definition must arrive as the vm-update transition PoolVMMediaCommand
		// produces, and is fail-closed here. What remains cannot change the master's
		// Spec: it reconnects the tray to the ISO the definition already names, and
		// needs the owner admission every other execution action takes.
		if cmd.VMMedia == nil || poolVMMediaChangesDefinition(v, *cmd.VMMedia) {
			return nil, errors.New("shared VM media changes require a durable pool definition transition")
		}
		action = "media"
	default:
		return func(protocol.Result, error) error { return nil }, nil
	}
	if action != "define" && action != "update-reserve" && action != "start" && action != "shutdown" && action != "force-stop" && action != "reboot" && action != "force-reboot" && action != "media" {
		return func(protocol.Result, error) error { return nil }, nil
	}
	if cmd.Kind == "vm-power" && (action == "shutdown" || action == "force-stop") {
		action = "stop"
	}
	if action == "start" {
		// The engine wakes a guest asleep in ACPI S3 instead of starting it. The master
		// already records that execution as running, so it is asked for a wake, which
		// needs no permit; a second start permit would claim a second execution.
		asleep, err := vmhost.ManagedVMAsleep(ctx, v.ID)
		if err != nil {
			return nil, errors.New("managed VM runtime observation is unavailable")
		}
		if asleep {
			action = "wake"
		}
	}
	permit := ""
	epoch := uint64(0)
	if action == "update-reserve" {
		previous, priorErr := poolVMUpdatePrevious(ctx, s, v.ID)
		if cmd.VM == nil || priorErr != nil || vmhost.ValidateManagedCommand(cmd) != nil {
			return nil, errors.New("VM update definition is unavailable or invalid")
		}
		if err := poolVMUpdateRequestPeer(ctx, s, v.ID, action, cmd.OperationID, previous, cmd.VM, nil); err != nil {
			return nil, err
		}
	} else if action == "define" && cmd.Kind == "vm-create" {
		if err := poolVMDefineRequestPeer(ctx, s, *cmd.VM, cmd.OperationID); err != nil {
			return nil, err
		}
	} else if action != "start" {
		if err := poolVMRequestPeer(ctx, s, v.ID, action, nil, cmd.Kind == "vm-create", cmd.VM, nil); err != nil {
			return nil, err
		}
	}
	if shared && cmd.Kind == "vm-create" && v.StartAfterCreate {
		if err := poolVMRequestPeer(ctx, s, v.ID, "start", &permit, false, nil, &epoch); err != nil {
			return nil, err
		}
	} else if action == "start" {
		if err := poolVMRequestPeer(ctx, s, v.ID, action, &permit, false, nil, &epoch); err != nil {
			return nil, err
		}
	}
	if permit != "" {
		if err := vmhost.WritePoolVMStartPermitAdmission(s.dir, s.State.PoolID, s.State.HostID, v.ID, epoch, permit); err != nil {
			return nil, err
		}
	}
	return func(result protocol.Result, executeErr error) error {
		if cmd.Kind == "vm-update" && (executeErr != nil || result.Status != "succeeded") {
			settle, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			evidence, evidenceErr := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).FailedVMUpdateEvidence(settle, v.ID, cmd.OperationID, *cmd.VM)
			if evidenceErr != nil || poolVMUpdateRequestPeer(settle, s, v.ID, "update-abort", cmd.OperationID, nil, nil, &evidence) != nil {
				return errors.New("VM update outcome is pending durable failure reconciliation")
			}
			return nil
		}
		if executeErr != nil || result.Status != "succeeded" {
			if permit != "" {
				_ = vmhost.RemovePoolVMStartPermit(s.dir, v.ID)
			}
			return nil
		}
		if cmd.Kind == "vm-update" {
			settle, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			evidence, evidenceErr := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(settle, v.ID)
			if evidenceErr != nil || poolVMUpdateRequestPeer(settle, s, v.ID, "update-commit", cmd.OperationID, nil, nil, &evidence) != nil {
				return errors.New("local VM update succeeded but pool owner confirmation is pending")
			}
			return nil
		}
		if cmd.Kind == "vm-power" && (cmd.Power.Action == "reboot" || cmd.Power.Action == "force-reboot") {
			// Reboot has no stopped/running acknowledgement: an ambiguous guest
			// reboot must not be translated into a placement transition.
			return nil
		}
		if !shared || (cmd.Kind == "vm-create" && !v.StartAfterCreate) {
			return nil
		}
		state, err := vmhost.ManagedVMState(context.Background(), v.ID)
		if err != nil {
			return nil
		}
		if state == "running" {
			_ = poolVMRequestPeer(context.Background(), s, v.ID, "running", nil, false, nil, nil)
		}
		if state == "shut off" {
			_ = vmhost.RemovePoolVMStartPermit(s.dir, v.ID)
			_ = poolVMRequestPeer(context.Background(), s, v.ID, "stopped", nil, false, nil, nil)
		}
		return nil
	}, nil
}

func poolVMDefineRequestPeer(ctx context.Context, s *Store, vm protocol.VMCreateSpec, operationID string) error {
	membership, err := Membership(s)
	if err != nil || operationID == "" {
		return errors.New("VM placement membership or operation is unavailable")
	}
	if membership.MasterHostID == s.State.HostID {
		member := protocol.PoolMember{HostID: s.State.HostID, Generation: membership.Generation, Status: "ready"}
		placement, err := poolVMPlacement(ctx, s, membership, member, protocol.Command{OperationID: operationID, Kind: "vm-create", VM: &vm})
		if err != nil {
			return err
		}
		if err := poolVMPlacementStorageMatches(ctx, s, placement, vm); err != nil {
			return err
		}
		return poolVMLocalPlacement(s, vm.ID, "define", nil, true, &vm, nil, &placement)
	}
	master, err := PoolMasterURL(s)
	if err != nil {
		return err
	}
	client, err := PoolPeerClient(s, master)
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	body, _ := json.Marshal(poolVMRequest{VMID: vm.ID, Action: "define", Create: true, OperationID: operationID, VM: &vm})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+vm.ID, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := client.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		return errors.New("pool master refused VM placement: " + placementError(errors.New(string(body))))
	}
	var reply poolVMRequest
	if json.NewDecoder(response.Body).Decode(&reply) != nil || reply.VMID != vm.ID || reply.Action != "define" || reply.OperationID != operationID || !reply.PlacementAccepted || !reply.SpecMatch {
		return errors.New("pool master did not acknowledge VM placement")
	}
	return nil
}

func poolVMRequestPeer(ctx context.Context, s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	if create {
		return errors.New("VM creation requires exact operation placement admission")
	}
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		return poolVMLocal(s, id, action, permit, create, vm, epoch)
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
	body, _ := json.Marshal(poolVMRequest{VMID: id, Action: action, Create: create, VM: vm})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+id, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		// The master's own sentence, not a bare refusal: an operator whose start was
		// refused has to be told which check refused it and on which host.
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		if reason := placementError(errors.New(string(detail))); reason != "" {
			return errors.New("pool master refused VM execution: " + reason)
		}
		return errors.New("pool master refused VM execution")
	}
	var result poolVMRequest
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return errors.New("invalid pool VM execution response")
	}
	if action == "define" && !create && vm != nil && !result.SpecMatch {
		return errors.New("pool master did not confirm the durable VM definition")
	}
	if permit != nil {
		*permit = result.Permit
	}
	if epoch != nil {
		*epoch = result.Epoch
	}
	return nil
}

// poolVMUpdateRequestPeer is deliberately separate from execution permits: a
// definition revision has a two-step master record (reserve then journal-backed
// commit), while power permits are one-use runtime admissions.
func poolVMUpdateRequestPeer(ctx context.Context, s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	membership, err := Membership(s)
	if err != nil {
		return err
	}
	if membership.MasterHostID == s.State.HostID {
		if action == "update-reserve" {
			if next == nil {
				return errors.New("VM placement definition is unavailable")
			}
			member := protocol.PoolMember{HostID: s.State.HostID, Generation: membership.Generation, Status: "ready"}
			placement, preflightErr := poolVMPlacement(ctx, s, membership, member, protocol.Command{OperationID: operationID, Kind: "vm-update", VM: next})
			if preflightErr != nil {
				return preflightErr
			}
			if err := poolVMPlacementStorageMatches(ctx, s, placement, *next); err != nil {
				return err
			}
			return poolVMLocalUpdatePlacement(s, id, action, operationID, previous, next, evidence, &placement)
		}
		return poolVMLocalUpdate(s, id, action, operationID, previous, next, evidence)
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
	body, _ := json.Marshal(poolVMRequest{VMID: id, Action: action, OperationID: operationID, Previous: previous, VM: next, Update: evidence})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(master, "/")+poolVMPath+id, strings.NewReader(string(body)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return errors.New("pool master refused VM update transition: " + placementError(errors.New(string(body))))
	}
	var result poolVMRequest
	if json.NewDecoder(resp.Body).Decode(&result) != nil {
		return errors.New("invalid pool VM update transition response")
	}
	if result.VMID != id || result.Action != action || result.OperationID != operationID || !result.UpdateAccepted || (action == "update-reserve" && !result.PlacementAccepted) {
		return errors.New("pool master did not confirm the durable VM update transition")
	}
	wantRevision := 0
	if next != nil {
		wantRevision = next.Revision
	} else if evidence != nil {
		wantRevision = evidence.Current.Revision
	}
	if wantRevision < 1 || result.UpdateRevision != wantRevision {
		return errors.New("pool master returned a mismatched durable VM update transition")
	}
	return nil
}

// placementMembershipMatches validates the final admission snapshot while the
// caller holds poolPeerMu through the durable VM record write.
func placementMembershipMatches(s *Store, membership protocol.PoolMembership, generation uint64, selected protocol.PoolMember) bool {
	if membership.Valid() != nil || membership.PoolID != s.State.PoolID || membership.MasterHostID != s.State.HostID || membership.Generation != generation {
		return false
	}
	for _, member := range membership.Members {
		if member.HostID == selected.HostID && member.Status == "ready" && member.Generation == generation {
			return selected.CertificateSHA256 == "" || member.CertificateSHA256 == selected.CertificateSHA256
		}
	}
	return false
}

func placementReservationMatches(record poolVMRecord, operationID string, next *protocol.VMCreateSpec) bool {
	return next != nil && record.PendingUpdate != nil && record.PendingUpdate.OperationID == operationID && reflect.DeepEqual(record.PendingUpdate.Next, *next)
}

func poolVMLocalUpdate(s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence) error {
	return poolVMLocalUpdatePlacement(s, id, action, operationID, previous, next, evidence, nil)
}

func poolVMLocalUpdatePlacement(s *Store, id, action, operationID string, previous, next *protocol.VMCreateSpec, evidence *vmhost.VMUpdateEvidence, placement *poolPlacementReply) error {
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	if err := requirePoolVMAuthorityWritable(s, action, false); err != nil {
		return err
	}
	var membership protocol.PoolMembership
	if placement != nil {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, err := loadPoolPeer(s)
		membership = peer.Membership
		if err != nil || !placementMembershipMatches(s, membership, placement.Generation, protocol.PoolMember{HostID: s.State.HostID}) {
			return errors.New("pool membership changed before durable VM placement")
		}
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	record, ok := state.VMs[id]
	if placement != nil && placement.Replay && (!ok || !placementReservationMatches(record, operationID, next)) {
		return errors.New("VM placement replay has no matching durable master reservation")
	}
	if !ok && action != "update-reserve" {
		return errors.New("VM has no durable pool execution record")
	}
	switch action {
	case "update-reserve":
		valid := next != nil && vmhost.ValidateManagedCommand(protocol.Command{OperationID: operationID, Kind: "vm-update", VM: next}) == nil
		if valid && ok {
			valid = previous != nil && poolVMRecordMatchesSpec(record, *previous) && reservePoolVMUpdate(&record, s.State.HostID, operationID, *next) == nil
		} else if valid {
			var membershipErr error
			if placement == nil {
				membership, membershipErr = Membership(s)
			}
			valid = membershipErr == nil && reserveOrSeedPoolVMUpdate(&state, membership, &record, ok, s.State.HostID, operationID, previous, next) == nil
		}
		if !valid {
			return errors.New("VM update is not eligible for the durable pool owner record")
		}
	case "update-commit":
		if evidence == nil || commitPoolVMUpdate(&record, s.State.HostID, *evidence) != nil {
			return errors.New("VM update journal evidence does not match the durable pool owner record")
		}
	case "update-abort":
		if evidence == nil || abortPoolVMUpdate(&record, s.State.HostID, *evidence) != nil {
			return errors.New("VM update failure evidence does not match the durable pool owner record")
		}
	default:
		return errors.New("unsupported VM update transition")
	}
	state.VMs[id] = record
	return savePoolVMs(s, state)
}
func poolVMLocal(s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64) error {
	return poolVMLocalPlacement(s, id, action, permit, create, vm, epoch, nil)
}

func poolVMLocalPlacement(s *Store, id, action string, permit *string, create bool, vm *protocol.VMCreateSpec, epoch *uint64, placement *poolPlacementReply) error {
	// The master's own guests take this path instead of the peer handler. Read the drain
	// flag before any lock: a placement already carries its own refusal from
	// poolVMPlacement, and reading membership here would otherwise take the peer lock in
	// the wrong order behind the VM lock a placement already holds. A wake is a start an
	// operator asked for; a reboot of a guest already running here is not.
	if action == "start" || action == "wake" {
		if err := poolHostInMaintenance(s, s.State.HostID); err != nil {
			return err
		}
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	if err := requirePoolVMAuthorityWritable(s, action, create); err != nil {
		return err
	}
	if placement != nil {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		peer, err := loadPoolPeer(s)
		if err != nil || !placementMembershipMatches(s, peer.Membership, placement.Generation, protocol.PoolMember{HostID: s.State.HostID}) {
			return errors.New("pool membership changed before durable VM placement")
		}
	}
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	r, ok := state.VMs[id]
	if placement != nil && placement.Replay && !ok {
		return errors.New("VM placement replay has no durable master record")
	}
	switch action {
	case "define":
		if ok && (r.PendingUpdate != nil || r.HostID != s.State.HostID) {
			return errors.New("VM is owned by another host")
		}
		if ok && vm != nil && !poolVMRecordMatchesSpec(r, *vm) {
			return errors.New("VM definition does not match the durable pool owner record")
		}
		if !ok && !create {
			return errors.New("VM has no durable pool execution record")
		}
		if !ok {
			if vm == nil || vm.ID != id || vm.PoolID != s.State.PoolID || vm.HostID != s.State.HostID || protocol.ValidateVMSpec(*vm) != nil {
				return errors.New("invalid VM definition")
			}
			r = poolVMRecord{PoolID: s.State.PoolID, VMID: id, HostID: s.State.HostID, State: "defined", Autostart: vm.Autostart, Epoch: 1, Spec: *vm}
		}
	case "start":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID || (r.State != "defined" && r.State != "stopped") {
			return errors.New("VM execution is not eligible to start")
		}
		r.State, r.Epoch = "starting", r.Epoch+1
		if permit != nil {
			*permit, err = randomPermit()
			if err != nil {
				return err
			}
		}
	case "running":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID || r.State != "starting" {
			return errors.New("VM execution transition refused")
		}
		r.State = "running"
	case "stop":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID {
			return errors.New("VM execution owner mismatch")
		}
	case "stopped":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID {
			return errors.New("VM execution owner mismatch")
		}
		r.State = "stopped"
	case "reboot", "force-reboot", "wake", "media":
		if !ok || r.PendingUpdate != nil || r.HostID != s.State.HostID || (action != "media" && r.State != "running") {
			return errors.New("VM execution is not eligible for this action")
		}
	default:
		return errors.New("unsupported VM execution action")
	}
	state.VMs[id] = r
	if err := savePoolVMs(s, state); err != nil {
		return err
	}
	if epoch != nil {
		*epoch = r.Epoch
	}
	return nil
}

// ReconcilePoolVMAutostart is called from the serialized managed worker.  It starts only the
// already-recorded owner and only after a fresh master permit; it is not HA or failover. It
// returns the reason the pass did nothing, or "" when it ran.
func ReconcilePoolVMAutostart(ctx context.Context, s *Store) string {
	// A drained host does not autostart pool guests on boot. Say so once for the pass: the
	// master would refuse each start anyway, and a silent skip reads like a failed start.
	// The operator-visible reason is the maintenance flag this host reports in inventory.
	drained := poolHostInMaintenance(s, s.State.HostID)
	// A restricted host autostarts no pool guest: this pass runs at boot, which is exactly
	// when a host that was fenced and promoted away would otherwise start a second copy of
	// a guest that is already running on the host that took over.
	restricted := requirePoolStartPath(s)
	engine := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms"))
	managed, checks := engine.Applied(ctx)
	// Neither refusal above can stop libvirt: it starts its autostart domains at boot,
	// before this agent runs, and this pass is not on that path at all. So the drain has to
	// reach libvirt's own flag. While this host is drained or restricted every managed
	// domain's autostart is disabled, and it is restored from Nex's durable definition —
	// the managed journal, which is where the operator's choice lives and stays — as soon
	// as the host is neither. Nothing about the desired value is recorded here.
	//
	// The flag alone is not enough to bring a local-storage guest back, because libvirt's
	// one attempt happens before this host has confirmed its pool authority and the startup
	// hook refuses it then. The loop below is what actually starts those guests, once the
	// two gates above have passed.
	for _, vm := range managed.VMs {
		if err := engine.SetManagedVMAutostart(ctx, vm, drained != nil || restricted != nil); err != nil {
			log.Print("libvirt autostart could not be reconciled for a managed VM; inspect this host")
		}
	}
	if drained != nil {
		log.Print("pool VM autostart skipped: this host is in maintenance mode")
		return "pool VM autostart skipped: this host is in maintenance mode"
	}
	if restricted != nil {
		log.Print("pool VM autostart skipped: this host is restricted until its pool authority is reconciled")
		return "pool VM autostart skipped: this host is restricted until its pool authority is reconciled"
	}
	if !managedVMChecksReady(checks) {
		return "pool VM autostart skipped: managed VM checks are not ready"
	}
	for _, vm := range managed.VMs {
		if !vm.Autostart {
			continue
		}
		shared := poolVMOnSharedStorage(managed, vm)
		// Autostart is an on-boot intent, not a keepalive policy. Claim this boot
		// before native observation or master admission, so an agent restart after
		// a manual stop cannot start the VM again.
		claimed, err := ClaimPoolVMAutostartAttempt(s.dir, vm.ID)
		if err != nil || !claimed {
			continue
		}
		state, err := vmhost.ManagedVMState(ctx, vm.ID)
		if err != nil || state != "shut off" {
			continue
		}
		if !shared {
			startLocalAutostartVM(ctx, s, engine, vm)
			continue
		}
		_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
		// Older member definitions predate the master record.  The first owner to report
		// one is recorded; a competing host is refused before libvirt receives a start.
		if poolVMRequestPeer(ctx, s, vm.ID, "stopped", nil, false, nil, nil) != nil {
			continue
		}
		permit := ""
		epoch := uint64(0)
		executionCtx, err := poolVMExecutionContext(ctx, s, vm)
		if err != nil {
			continue
		}
		if poolVMRequestPeer(ctx, s, vm.ID, "start", &permit, false, nil, &epoch) != nil {
			continue
		}
		if vmhost.WritePoolVMStartPermitAdmission(s.dir, s.State.PoolID, s.State.HostID, vm.ID, epoch, permit) != nil {
			continue
		}
		op, err := randomPermit()
		if err != nil {
			continue
		}
		result, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Execute(executionCtx, protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}}, nil)
		if err != nil || result.Status != "succeeded" {
			_ = vmhost.RemovePoolVMStartPermit(s.dir, vm.ID)
			continue
		}
		if observed, err := vmhost.ManagedVMState(ctx, vm.ID); err == nil && observed == "running" {
			_ = poolVMRequestPeer(ctx, s, vm.ID, "running", nil, false, nil, nil)
		}
	}
	return ""
}

// poolVMOnSharedStorage answers whether any of this VM's disks sit on a repository the pool
// shares. It decides which half of the autostart pass a guest belongs to: a shared-storage
// guest needs the master's admission and a one-use execution permit, a local one cannot be
// opened from another host and needs neither.
func poolVMOnSharedStorage(managed *protocol.ManagedState, vm protocol.VMCreateSpec) bool {
	for _, disk := range vm.Disks {
		for _, storage := range managed.Storage {
			if disk.StorageID == storage.ID && isPoolSharedStorage(storage) {
				return true
			}
		}
	}
	return false
}

// startLocalAutostartVM brings back a local-storage guest whose autostart flag libvirt owns.
//
// libvirt tries that flag exactly once, at boot, and on a pool master with other ready
// members it tries it before this agent has confirmed this boot's pool authority — so the
// startup hook refuses it, and nothing retries it for the rest of that boot. Without this the
// guest simply never returns from a reboot, and an operator has to start it by hand.
//
// It is safe to start here for the same reason the hook could not be left to it: the gates
// this pass already passed are the whole decision. The host is neither drained nor
// restricted, and a local disk cannot be opened from another host, so no retry here can ever
// produce a second writer. No master permit or ownership transition is involved either — an
// API start of a local-storage guest takes exactly the same path (AdmitPoolVMExecution
// returns before any master round trip for one).
func startLocalAutostartVM(ctx context.Context, s *Store, engine *vmhost.ManagedEngine, vm protocol.VMCreateSpec) {
	executionCtx, err := poolVMExecutionContext(ctx, s, vm)
	if err != nil {
		return
	}
	op, err := randomPermit()
	if err != nil {
		return
	}
	result, err := engine.Execute(executionCtx, protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID, Action: "start"}}, nil)
	if err != nil || result.Status != "succeeded" {
		log.Print("pool VM autostart could not start a local-storage guest; inspect the VM on this host")
	}
}
