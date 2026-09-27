// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

// replaceCutoverCapacity is the host capacity check used before exclusive replace cutover.
// Tests stub it; production keeps managedCapacity.
var replaceCutoverCapacity = managedCapacity

// replaceCutoverPrepareDirs ensures nex-vms / VM directories under a repository.
// Tests stub it; production keeps managedRepositorySubdir.
var replaceCutoverPrepareDirs = managedRepositorySubdir

var ErrRestorePlacementUncertain = errors.New("restore placement requires observation before retry")
var ErrRestoreManifestUnbound = errors.New("retained restore allocation lacks manifest binding")
var ErrRestoreAllocationJournalUnavailable = errors.New("restore allocation journal is unavailable")

// RestoreAsNewAllocating identifies an exact retained restore that can resume
// artifact placement. Defining and defined intents must be observed instead.
func (e *ManagedEngine) RestoreAsNewAllocating(operationID string, spec protocol.VMCreateSpec, manifestSHA string, artifacts []protocol.BackupDiskArtifact, firmware []protocol.BackupFirmwareArtifact) (bool, error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || !uuidPattern.MatchString(spec.ID) || !validRestoreManifestSHA(manifestSHA) {
		return false, errors.New("restore allocation identity is invalid")
	}
	js, err := e.journals()
	if err != nil {
		return false, err
	}
	for _, j := range js {
		if j.Command.OperationID != operationID {
			continue
		}
		if j.Command.Kind != "vm-create" || !j.BackupRestore || j.Command.VM == nil ||
			!reflect.DeepEqual(*j.Command.VM, spec) || !slices.Equal(j.BackupFirmware, firmware) {
			return false, errors.New("restore allocation intent differs from retained journal")
		}
		if j.Stage == "allocating" && j.Result.Status == "pending" && j.BackupManifestSHA256 == "" && len(j.BackupDisks) == 0 {
			return false, ErrRestoreManifestUnbound
		}
		if j.BackupManifestSHA256 != manifestSHA || !slices.Equal(j.BackupDisks, artifacts) {
			return false, errors.New("restore allocation point differs from retained journal")
		}
		switch j.Stage {
		case "allocating":
			if j.Result.Status == "pending" {
				return true, nil
			}
		case "defining", "defined":
			if j.Result.Status == "pending" || j.Result.Status == "succeeded" {
				return false, nil
			}
		}
		return false, errors.New("restore allocation stage is unresolved")
	}
	return false, ErrRestoreAllocationJournalUnavailable
}

func validRestoreManifestSHA(digest string) bool {
	decoded, err := hex.DecodeString(digest)
	return err == nil && len(decoded) == 32 && hex.EncodeToString(decoded) == digest
}

// BindLegacyRestoreAsNewAllocating upgrades an older allocating journal only
// when every local and placed artifact still proves the same immutable point.
// It never copies, replaces, or defines a VM.
func (e *ManagedEngine) BindLegacyRestoreAsNewAllocating(ctx context.Context, operationID string, spec protocol.VMCreateSpec, localDir, manifestSHA string, artifacts []protocol.BackupDiskArtifact, firmware []protocol.BackupFirmwareArtifact) error {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || !uuidPattern.MatchString(spec.ID) || !validRestoreManifestSHA(manifestSHA) ||
		localDir != filepath.Join(filepath.Dir(e.stateDir), "backup-restore", operationID) || len(artifacts) != len(spec.Disks) {
		return errors.New("legacy restore binding arguments are invalid")
	}
	if err := protocol.ValidateVMSpec(spec); err != nil {
		return err
	}
	if err := ValidateBackupFirmwareForRestore(protocol.BackupManifest{CapturedSpec: &spec, Firmware: firmware}); err != nil {
		return err
	}
	if !filepath.IsAbs(e.stateDir) || filepath.Clean(e.stateDir) == "/" {
		return errors.New("managed state requires an absolute non-root directory")
	}
	real, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil || real != e.stateDir {
		return errors.New("managed state must not use symlinks")
	}
	real, err = filepath.EvalSymlinks(localDir)
	if err != nil || real != localDir {
		return errors.New("legacy restore staging must not use symlinks")
	}
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return err
	}
	var journal *managedJournal
	for i := range js {
		if js[i].Command.OperationID == operationID {
			journal = &js[i]
		}
	}
	if journal == nil || journal.Command.Kind != "vm-create" || !journal.BackupRestore || journal.Command.VM == nil ||
		!reflect.DeepEqual(*journal.Command.VM, spec) || !slices.Equal(journal.BackupFirmware, firmware) ||
		journal.Stage != "allocating" || journal.Result.Status != "pending" || journal.BackupManifestSHA256 != "" ||
		len(journal.BackupDisks) != 0 || len(journal.Domain) != 0 || journal.BackupTPMManifestSHA256 != "" ||
		len(journal.Disks) != len(artifacts) || len(journal.Targets) != len(artifacts) {
		return errors.New("legacy restore allocation cannot be bound")
	}
	ids, err := managedVirsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return err
	}
	if slices.Contains(strings.Fields(string(ids)), spec.ID) {
		return errors.New("legacy restore VM is already defined")
	}
	if _, err := os.Lstat(filepath.Join(backupTPMStateRoot, spec.ID)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("legacy restore has unexpected TPM effects")
	}
	if _, err := os.Lstat(filepath.Join(e.stateDir, operationID+".xml")); !errors.Is(err, os.ErrNotExist) {
		return errors.New("legacy restore has an unobserved native definition file")
	}
	byTarget := make(map[string]protocol.BackupDiskArtifact, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.Valid() != nil || artifact.Target == "" {
			return errors.New("legacy restore disk descriptor is invalid")
		}
		if _, exists := byTarget[artifact.Target]; exists {
			return errors.New("legacy restore disk target is repeated")
		}
		byTarget[artifact.Target] = artifact
		if err := exactRestoreFile(filepath.Join(localDir, artifact.Filename), artifact.SHA256, artifact.Size); err != nil {
			return err
		}
	}
	for i, target := range journal.Targets {
		artifact, ok := byTarget[target]
		if !ok {
			return errors.New("legacy restore disk target differs")
		}
		if err := exactRestoreFile(journal.Disks[i], artifact.SHA256, artifact.Size); err != nil {
			return err
		}
	}
	for _, artifact := range firmware {
		if artifact.Valid() != nil {
			return errors.New("legacy restore firmware descriptor is invalid")
		}
		if err := exactRestoreFile(filepath.Join(localDir, artifact.Filename), artifact.SHA256, artifact.Size); err != nil {
			return err
		}
		if artifact.Kind == "nvram" {
			if err := verifyRestoreNVRAM(ctx, restoreNVRAMPath(spec, operationID, false), artifact); err != nil {
				return err
			}
		}
	}
	journal.BackupManifestSHA256 = manifestSHA
	journal.BackupDisks = append([]protocol.BackupDiskArtifact(nil), artifacts...)
	return e.save(*journal)
}

func exactRestoreFile(path, digest string, size int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("legacy restore artifact is missing or unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("legacy restore artifact has hardlink aliases")
	}
	sum, gotSize, err := fileSHA256(path)
	if err != nil || sum != digest || gotSize != size {
		return errors.New("legacy restore artifact differs from immutable point")
	}
	return nil
}

// ReconcileRestoredAsNew observes a retained registration after an uncertain
// define reply. It never allocates, copies, or defines a VM.
func (e *ManagedEngine) ReconcileRestoredAsNew(ctx context.Context, operationID string, expectedSpec protocol.VMCreateSpec) (protocol.Result, error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || !uuidPattern.MatchString(expectedSpec.ID) {
		return protocol.Result{}, errors.New("restore reconciliation arguments are invalid")
	}
	js, err := e.journals()
	if err != nil {
		return protocol.Result{}, err
	}
	for _, j := range js {
		if j.Command.OperationID != operationID {
			continue
		}
		if j.Command.Kind != "vm-create" || j.Command.VM == nil || !reflect.DeepEqual(*j.Command.VM, expectedSpec) {
			return protocol.Result{}, errors.New("restore journal intent does not match")
		}
		if j.Stage == "allocating" {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: "restore disk allocation is incomplete", ObservedAt: time.Now().UTC()}, nil
		}
		if j.Stage != "defining" && j.Stage != "defined" {
			return protocol.Result{}, errors.New("restore journal stage cannot be reconciled")
		}
		if err := e.observer()(ctx, j, "shut off"); err != nil {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: err.Error(), ObservedAt: time.Now().UTC()}, nil
		}
		if err := verifyJournalRestoreFirmware(ctx, j); err != nil {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: err.Error(), ObservedAt: time.Now().UTC()}, nil
		}
		j.Stage = "defined"
		j.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
		if err := e.save(j); err != nil {
			return protocol.Result{}, err
		}
		return j.Result, nil
	}
	return protocol.Result{}, errors.New("restore journal is unavailable")
}

// RestoreAsNewTargetAbsent proves that registration has no retained native intent,
// domain, or destination artifacts. A failed observation is not evidence of absence.
func (e *ManagedEngine) RestoreAsNewTargetAbsent(ctx context.Context, operationID string, spec protocol.VMCreateSpec) (bool, error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || !uuidPattern.MatchString(spec.ID) {
		return false, errors.New("restore absence arguments are invalid")
	}
	if err := protocol.ValidateVMSpec(spec); err != nil {
		return false, err
	}
	js, err := e.journals()
	if err != nil {
		return false, err
	}
	for _, j := range js {
		if j.Command.OperationID == operationID || j.Command.VM != nil && j.Command.VM.ID == spec.ID {
			return false, nil
		}
	}
	ids, err := managedVirsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return false, err
	}
	for _, id := range strings.Fields(string(ids)) {
		if id == spec.ID {
			return false, nil
		}
	}
	targets, _, err := managedDiskPlan(js, spec)
	if err != nil {
		return false, err
	}
	paths := make([]string, 0, len(targets)+2)
	for _, target := range targets {
		paths = append(paths, target.Dir)
	}
	if spec.Boot.Firmware == "uefi" {
		paths = append(paths, restoreNVRAMPath(spec, operationID, false))
	}
	if spec.Boot.TPMEnabled() {
		paths = append(paths, filepath.Join(backupTPMStateRoot, spec.ID))
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return false, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	return true, nil
}

// CleanupTestRestoredVM binds a test restore's native deletion to its exact
// restore journal while the managed operations lock is held. An absent master
// owner may supply only identity fields when the exact delete already succeeded.
func (e *ManagedEngine) CleanupTestRestoredVM(ctx context.Context, createOperationID string, expectedSpec protocol.VMCreateSpec, deleteOperationID string) (protocol.Result, error) {
	refused := protocol.Result{OperationID: deleteOperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if e == nil || !protocol.ValidSnapshotOperationID(createOperationID) || !protocol.ValidSnapshotOperationID(deleteOperationID) || createOperationID == deleteOperationID {
		refused.Error = "test restore cleanup identity is invalid"
		return refused, nil
	}
	providedSpec := protocol.ValidateVMSpec(expectedSpec) == nil
	if !providedSpec {
		if !uuidPattern.MatchString(expectedSpec.ID) || expectedSpec.PoolID == "" || expectedSpec.HostID == "" {
			refused.Error = "test restore replay identity is incomplete"
			return refused, nil
		}
		creator, err := e.readJournal(createOperationID + ".json")
		if err != nil || creator.Command.VM == nil {
			refused.Error = "test restore creator journal is unavailable"
			return refused, nil
		}
		if creator.Command.VM.ID != expectedSpec.ID || creator.Command.VM.PoolID != expectedSpec.PoolID || creator.Command.VM.HostID != expectedSpec.HostID {
			refused.Error = "test restore replay identity differs from creator"
			return refused, nil
		}
		expectedSpec = *creator.Command.VM
	}
	delete := protocol.VMDeleteRequest{VMID: expectedSpec.ID, PoolID: expectedSpec.PoolID, HostID: expectedSpec.HostID, DeleteDisks: true}
	command := protocol.Command{OperationID: deleteOperationID, Kind: "vm-delete", Delete: &delete}
	guard := func(ctx context.Context, js []managedJournal) error {
		var creator *managedJournal
		var priorDelete *managedJournal
		for i := range js {
			j := &js[i]
			if j.Command.OperationID == createOperationID {
				creator = j
			}
			if j.Command.OperationID == deleteOperationID {
				priorDelete = j
			}
			if j.Command.Kind == "vm-create" && j.Command.VM != nil && j.Command.VM.ID == expectedSpec.ID && j.Command.OperationID != createOperationID {
				return errors.New("test restore VM identity was reused by another create")
			}
		}
		if creator == nil || !creator.BackupRestore || creator.Command.Kind != "vm-create" || creator.Command.VM == nil ||
			!reflect.DeepEqual(*creator.Command.VM, expectedSpec) || creator.Result.Status != "succeeded" || creator.Stage != "defined" {
			return errors.New("test restore creator intent is unavailable or differs")
		}
		if priorDelete != nil {
			if priorDelete.Command.Kind != "vm-delete" || priorDelete.Command.Delete == nil || *priorDelete.Command.Delete != delete {
				return errors.New("test restore cleanup operation belongs to another intent")
			}
			if priorDelete.Result.Status == "succeeded" {
				return testRestoreNativeAbsent(ctx, *creator)
			}
			if !providedSpec || (priorDelete.Stage != "undefining" && priorDelete.Stage != "wiping") {
				return errors.New("test restore cleanup outcome needs native inspection")
			}
			return nil
		}
		if !providedSpec {
			return errors.New("test restore cleanup cannot begin without the exact owner specification")
		}
		current, err := managedCurrent(js, expectedSpec.ID)
		if err != nil || current.Command.OperationID != createOperationID || current.projectionView {
			return errors.New("test restore definition changed after creation")
		}
		return e.observer()(ctx, *creator, "shut off")
	}
	return e.execute(ctx, command, nil, nil, nil, guard)
}

// VerifyTestRestoredVM observes the exact successful test restore while holding
// the native operations lock. Firmware contents may change after a guest boots,
// so only its current safe state and assigned path are checked here.
func (e *ManagedEngine) VerifyTestRestoredVM(ctx context.Context, createOperationID string, expectedSpec protocol.VMCreateSpec, passed bool) error {
	if e == nil || !protocol.ValidSnapshotOperationID(createOperationID) || !uuidPattern.MatchString(expectedSpec.ID) {
		return errors.New("test restore operation identity is invalid")
	}
	if err := protocol.ValidateVMSpec(expectedSpec); err != nil {
		return err
	}
	if !filepath.IsAbs(e.stateDir) || filepath.Clean(e.stateDir) == "/" {
		return errors.New("managed state requires an absolute non-root directory")
	}
	real, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil || real != e.stateDir {
		return errors.New("managed state must not use symlinks")
	}
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return err
	}
	var creator *managedJournal
	for i := range js {
		j := &js[i]
		if j.Command.OperationID == createOperationID {
			creator = j
		}
		if j.Command.Kind == "vm-create" && j.Command.VM != nil && j.Command.VM.ID == expectedSpec.ID && j.Command.OperationID != createOperationID {
			return errors.New("test restore VM identity was reused by another create")
		}
	}
	if creator == nil || !creator.BackupRestore || creator.Command.Kind != "vm-create" || creator.Command.VM == nil ||
		!reflect.DeepEqual(*creator.Command.VM, expectedSpec) || creator.Result.Status != "succeeded" || creator.Stage != "defined" {
		return errors.New("test restore creator intent is unavailable or differs")
	}
	current, err := managedCurrent(js, expectedSpec.ID)
	if err != nil || current.Command.OperationID != createOperationID || current.projectionView {
		return errors.New("test restore definition changed after creation")
	}
	stateOutput, err := managedVirsh(ctx, "domstate", expectedSpec.ID)
	if err != nil {
		return errors.New("test restore VM state is unavailable")
	}
	state := strings.TrimSpace(string(stateOutput))
	if state != "shut off" && state != "running" {
		return errors.New("test restore VM is not running or shut off")
	}
	if !passed {
		definition, err := managedVirsh(ctx, "dumpxml", expectedSpec.ID, "--inactive")
		if err != nil || validateManagedDefinition(definition, creator.Domain) != nil {
			return errors.New("test restore native definition differs from its creator")
		}
		return nil
	}
	if err := e.observer()(ctx, *creator, state); err != nil {
		return err
	}
	if expectedSpec.Boot.Firmware == "uefi" {
		path := restoreNVRAMPath(expectedSpec, createOperationID, false)
		if err := firmwareVariablesFile(path); err != nil {
			return err
		}
		if err := singleLinkNVRAM(path); err != nil {
			return err
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			return errors.New("test restore NVRAM state is unavailable")
		}
	}
	if expectedSpec.Boot.TPMEnabled() {
		definition, err := managedVirsh(ctx, "dumpxml", expectedSpec.ID, "--inactive")
		var domain managedDomain
		if err != nil || xml.Unmarshal(definition, &domain) != nil {
			return errors.New("test restore TPM definition is unavailable")
		}
		if source, err := managedTPMStateSource(domain, expectedSpec.ID); err != nil || source != filepath.Join(backupTPMStateRoot, expectedSpec.ID) {
			return errors.New("test restore TPM source differs from its VM UUID")
		}
		manifest, err := managedSnapshotManifest(ctx, filepath.Join(backupTPMStateRoot, expectedSpec.ID))
		if err != nil || !manifest.Exists {
			return errors.New("test restore TPM state is unavailable")
		}
		stateFile := false
		for _, file := range manifest.Files {
			if file.RelativePath == "tpm2/tpm2-00.permall" && !file.Directory && file.Size > 0 {
				stateFile = true
			}
		}
		if !stateFile {
			return errors.New("test restore TPM state file is unavailable")
		}
	}
	return nil
}

func testRestoreNativeAbsent(ctx context.Context, creator managedJournal) error {
	vm := creator.Command.VM
	defined := domains(ctx)
	if defined == nil || defined[strings.ToLower(vm.ID)] {
		return errors.New("test restore domain absence is unproven")
	}
	paths := make([]string, 0, len(creator.Disks)+2)
	for _, disk := range creator.Disks {
		paths = append(paths, filepath.Dir(disk))
	}
	if vm.Boot.Firmware == "uefi" {
		paths = append(paths, restoreNVRAMPath(*vm, creator.Command.OperationID, false))
	}
	if vm.Boot.TPMEnabled() {
		paths = append(paths, filepath.Join(backupTPMStateRoot, vm.ID))
	}
	for _, path := range paths {
		if _, err := os.Lstat(path); err == nil {
			return errors.New("test restore native artifacts remain")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

var backupNVRAMOwner = func(ctx context.Context) (int, int, error) {
	uid, gid, err := libvirtQEMUIdentity(ctx)
	if err != nil {
		return 0, 0, err
	}
	u, err := strconv.Atoi(uid)
	if err != nil {
		return 0, 0, err
	}
	g, err := strconv.Atoi(gid)
	return u, g, err
}

// ReplaceCutoverError marks a failure after exclusive definition switch started.
// Callers settle needs_attention with Stage rather than a clean failed preflight.
type ReplaceCutoverError struct {
	Stage string
	Err   error
}

func (e *ReplaceCutoverError) Error() string {
	if e == nil {
		return "replace cutover failed"
	}
	if e.Stage == "" {
		return e.Err.Error()
	}
	return e.Stage + ": " + e.Err.Error()
}

func (e *ReplaceCutoverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsReplaceCutoverAttention reports whether err is a mid-cutover recoverable failure.
func IsReplaceCutoverAttention(err error) bool {
	var ce *ReplaceCutoverError
	return errors.As(err, &ce)
}

// ReconcileExclusiveReplaceCutover observes only a retained definition switch.
// It never copies disks or issues another define after an uncertain native reply.
func (e *ManagedEngine) ReconcileExclusiveReplaceCutover(ctx context.Context, operationID string) (protocol.Result, error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) {
		return protocol.Result{}, errors.New("replace reconciliation arguments are invalid")
	}
	js, err := e.journals()
	if err != nil {
		return protocol.Result{}, err
	}
	for _, j := range js {
		if j.Command.OperationID != operationID {
			continue
		}
		if j.Command.Kind != "vm-update" || j.Command.VM == nil {
			return protocol.Result{}, errors.New("replace journal intent does not match")
		}
		if j.Stage == "allocating" {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: "replace disk allocation is incomplete", ObservedAt: time.Now().UTC()}, nil
		}
		if j.Stage == "swapping-tpm" {
			if j.Command.VM == nil || j.BackupTPMPreviousSHA256 == "" || j.BackupTPMManifestSHA256 == "" {
				return protocol.Result{}, errors.New("replace TPM swap journal is incomplete")
			}
			if state, stateErr := ManagedVMState(ctx, j.Command.VM.ID); stateErr != nil || state != "shut off" {
				return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: "original VM state is uncertain after TPM swap reservation", ObservedAt: time.Now().UTC()}, nil
			}
			current, currentErr := managedSnapshotManifest(ctx, filepath.Join(backupTPMStateRoot, j.Command.VM.ID))
			staged, stagedErr := managedSnapshotManifest(ctx, restoreTPMPath(j.Command.VM.ID, operationID))
			if currentErr != nil || stagedErr != nil || !current.Exists || !staged.Exists {
				return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: "TPM state cannot be inspected after swap reservation", ObservedAt: time.Now().UTC()}, nil
			}
			errorText := "TPM swap outcome is ambiguous; original definition remains stopped"
			if current.SHA256 == j.BackupTPMManifestSHA256 && staged.SHA256 == j.BackupTPMPreviousSHA256 {
				errorText = "TPM swap landed before definition cutover; inspect retained original state"
			} else if current.SHA256 == j.BackupTPMPreviousSHA256 && staged.SHA256 == j.BackupTPMManifestSHA256 {
				errorText = "TPM swap did not land; original definition remains stopped"
			}
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: errorText, ObservedAt: time.Now().UTC()}, nil
		}
		if j.Stage != "defining" && j.Stage != "defined" {
			return protocol.Result{}, errors.New("replace journal stage cannot be reconciled")
		}
		if err := e.observer()(ctx, j, "shut off"); err != nil {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: err.Error(), ObservedAt: time.Now().UTC()}, nil
		}
		if err := verifyJournalRestoreFirmware(ctx, j); err != nil {
			return protocol.Result{OperationID: operationID, Status: "needs_attention", Error: err.Error(), ObservedAt: time.Now().UTC()}, nil
		}
		j.Stage = "defined"
		j.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
		if err := e.save(j); err != nil {
			return protocol.Result{}, err
		}
		return j.Result, nil
	}
	return protocol.Result{}, errors.New("replace journal is unavailable")
}

// ReplaceCutoverStage returns the durable cutover stage carried by err, if any.
func ReplaceCutoverStage(err error) string {
	var ce *ReplaceCutoverError
	if errors.As(err, &ce) {
		return ce.Stage
	}
	return ""
}

// BuildRestoreAsNewSpec reconstructs a stopped VM definition for restore-as-new:
// new identity, remapped storage, cleared media, and disconnected NICs (none attached).
func BuildRestoreAsNewSpec(captured protocol.VMCreateSpec, req protocol.RestoreAsNewRequest, poolID, hostID string) (protocol.VMCreateSpec, error) {
	if err := req.Valid(); err != nil {
		return protocol.VMCreateSpec{}, err
	}
	if !uuidPattern.MatchString(req.NewVMID) {
		return protocol.VMCreateSpec{}, errors.New("restored VM ID must be a UUID")
	}
	if req.HostID != "" && req.HostID != hostID {
		return protocol.VMCreateSpec{}, errors.New("restore target host does not match this host")
	}
	if captured.ID == "" || len(captured.Disks) == 0 {
		return protocol.VMCreateSpec{}, errors.New("captured VM specification is incomplete")
	}
	out := captured
	out.ID = req.NewVMID
	out.Name = req.NewVMName
	out.PoolID = poolID
	out.HostID = hostID
	out.Revision = 0
	out.StartAfterCreate = false
	out.Autostart = false
	out.MediaID = ""
	out.DriversMediaID = ""
	out.Boot.MediaBootOrder = 0
	// Disconnected by default: no live network attachment; operator reconnects after inspection.
	out.NICs = nil
	disks := make([]protocol.VMDisk, len(captured.Disks))
	hasBoot := false
	for i, d := range captured.Disks {
		d.StorageID = req.StorageID
		disks[i] = d
		if d.BootOrder > 0 {
			hasBoot = true
		}
	}
	if !hasBoot && len(disks) > 0 {
		disks[0].BootOrder = 1
	}
	out.Disks = disks
	if err := protocol.ValidateVMSpec(out); err != nil {
		return protocol.VMCreateSpec{}, err
	}
	return out, nil
}

// RegisterRestoredAsNew places local restore disk copies onto selected storage and registers
// a stopped managed VM through the same journal/define path as create, without starting it.
func (e *ManagedEngine) RegisterRestoredAsNew(ctx context.Context, operationID string, spec protocol.VMCreateSpec, localDir, manifestSHA string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) (retErr error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || localDir == "" || !uuidPattern.MatchString(spec.ID) || !validRestoreManifestSHA(manifestSHA) {
		return errors.New("restore registration arguments are invalid")
	}
	if err := protocol.ValidateVMSpec(spec); err != nil {
		return err
	}
	if len(artifacts) != len(spec.Disks) {
		return errors.New("restore disk artifacts do not match the reconstructed VM")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	var prior *managedJournal
	other := make([]managedJournal, 0, len(js))
	for i := range js {
		if js[i].Command.OperationID == operationID {
			prior = &js[i]
			continue
		}
		other = append(other, js[i])
	}
	if prior != nil {
		if prior.Command.Kind != "vm-create" || prior.Command.VM == nil || !reflect.DeepEqual(*prior.Command.VM, spec) ||
			prior.BackupManifestSHA256 != manifestSHA || !slices.Equal(prior.BackupDisks, artifacts) || !slices.Equal(prior.BackupFirmware, firmware) {
			return errors.New("restore operation ID is already reserved by another VM definition")
		}
		if prior.Stage == "defining" || prior.Stage == "defined" {
			if err := e.observer()(ctx, *prior, "shut off"); err != nil {
				return fmt.Errorf("%w: %v", ErrRestorePlacementUncertain, err)
			}
			if err := verifyJournalRestoreFirmware(ctx, *prior); err != nil {
				return fmt.Errorf("%w: %v", ErrRestorePlacementUncertain, err)
			}
			prior.Stage = "defined"
			prior.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
			return e.save(*prior)
		}
		if prior.Stage != "allocating" {
			return ErrRestorePlacementUncertain
		}
	}
	if err = vmConflict(other, spec); err != nil {
		return err
	}
	targets, disks, err := managedDiskPlan(js, spec)
	if err != nil {
		return err
	}
	for _, t := range targets {
		_, access, readyErr := managedStorageByID(ctx, js, t.ID, t.Bytes)
		if readyErr != nil || access.Path != t.Path {
			if readyErr != nil {
				return readyErr
			}
			return errors.New("storage mount identity changed before restore placement")
		}
		if err = managedRepositorySubdir(ctx, js, t.ID, filepath.Join(access.Path, "nex-vms")); err != nil {
			return err
		}
	}
	if err = managedCapacity(ctx, spec, js); err != nil {
		return err
	}
	ids, err := managedVirsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return err
	}
	names, err := managedVirsh(ctx, "list", "--all", "--name")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(ids)) {
		if id == spec.ID {
			return errors.New("VM UUID already exists")
		}
	}
	for _, name := range strings.Split(string(names), "\n") {
		if name == spec.Name {
			return errors.New("VM name " + spec.Name + " already exists on this host")
		}
	}
	rootAbsent := false
	if spec.Boot.TPMEnabled() && prior == nil {
		if _, statErr := os.Lstat(backupTPMStateRoot); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		} else {
			rootAbsent = errors.Is(statErr, os.ErrNotExist)
		}
		if _, statErr := os.Lstat(filepath.Join(backupTPMStateRoot, spec.ID)); !errors.Is(statErr, os.ErrNotExist) {
			if statErr == nil {
				return errors.New("target UUID already has libvirt TPM state")
			}
			return statErr
		}
	}
	diskTargets, mediaTarget, driversTarget := managedTargets(spec)
	byTarget := map[string]protocol.BackupDiskArtifact{}
	for _, a := range artifacts {
		byTarget[a.Target] = a
	}
	for i := range spec.Disks {
		if diskTargets[i] == "" {
			return errors.New("restore disk targets are incomplete")
		}
		if _, ok := byTarget[diskTargets[i]]; !ok {
			return fmt.Errorf("restore artifact for target %s is missing", diskTargets[i])
		}
	}
	j := managedJournal{
		Command:              protocol.Command{OperationID: operationID, Kind: "vm-create", VM: &spec},
		Result:               protocol.Result{OperationID: operationID, Status: "pending", ObservedAt: time.Now().UTC()},
		Stage:                "allocating",
		BackupRestore:        true,
		BackupManifestSHA256: manifestSHA,
		BackupDisks:          append([]protocol.BackupDiskArtifact(nil), artifacts...),
		BackupFirmware:       append([]protocol.BackupFirmwareArtifact(nil), firmware...),
	}
	if prior != nil {
		j.BackupTPMRootAbsent = prior.BackupTPMRootAbsent
	} else {
		j.BackupTPMRootAbsent = rootAbsent
	}
	j.Disks = disks
	j.Targets, j.MediaTarget, j.DriversTarget = diskTargets, mediaTarget, driversTarget
	if prior != nil {
		if !slices.Equal(prior.Disks, j.Disks) || !slices.Equal(prior.Targets, j.Targets) {
			return ErrRestorePlacementUncertain
		}
	} else if err := e.save(j); err != nil {
		return fmt.Errorf("%w: %v", ErrRestorePlacementUncertain, err)
	}
	defer func() {
		if retErr != nil && !errors.Is(retErr, ErrRestorePlacementUncertain) {
			retErr = fmt.Errorf("%w: %v", ErrRestorePlacementUncertain, retErr)
		}
	}()
	for _, t := range targets {
		if prior == nil {
			if err = createDiskDir(t.Dir); err != nil {
				return errors.New("VM directory exists or cannot be exclusively created; inspect before recovery")
			}
		} else if info, statErr := os.Lstat(t.Dir); statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		} else if statErr == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("restore VM directory is unsafe")
		} else if errors.Is(statErr, os.ErrNotExist) {
			if err = createDiskDir(t.Dir); err != nil {
				return err
			}
		}
		if err = syncManagedDir(filepath.Dir(t.Dir)); err != nil {
			return err
		}
	}
	j.DiskChains = make([][]string, len(disks))
	for i := range disks {
		art := byTarget[j.Targets[i]]
		src := filepath.Join(localDir, art.Filename)
		if err = copyRestoreDisk(src, disks[i], art); err != nil {
			return err
		}
		sum, size, sumErr := fileSHA256(disks[i])
		if sumErr != nil || sum != art.SHA256 || size != art.Size {
			return errors.New("restored disk digest mismatch after placement")
		}
		if err = checkImage(ctx, disks[i]); err != nil {
			return err
		}
		if err = os.Chmod(disks[i], 0600); err != nil {
			return err
		}
		j.DiskChains[i] = []string{disks[i]}
	}
	for _, t := range targets {
		if err = syncManagedDir(t.Dir); err != nil {
			return err
		}
	}
	nets := make([]protocol.NetworkRegistration, len(spec.NICs))
	for i, requested := range spec.NICs {
		if nets[i], err = managedNetwork(js, requested.NetworkID); err != nil {
			return err
		}
		if err = verifyNetwork(ctx, nets[i]); err != nil {
			return err
		}
	}
	leases, err := managedSanlockVMLeases(ctx, js, spec)
	if err != nil {
		return err
	}
	if err = ensureManagedSanlockVMResources(ctx, leases); err != nil {
		return err
	}
	nvramPath, err := installRestoreNVRAM(ctx, spec, operationID, localDir, firmware, prior != nil, false)
	if err != nil {
		return err
	}
	_, tpmDigest, err := installRestoreTPM(ctx, spec, operationID, localDir, firmware, prior != nil, false, prior != nil && j.BackupTPMRootAbsent)
	if err != nil {
		return err
	}
	if prior != nil && prior.BackupTPMManifestSHA256 != "" && prior.BackupTPMManifestSHA256 != tpmDigest {
		return errors.New("restore TPM tree differs from its reserved intent")
	}
	j.BackupTPMManifestSHA256 = tpmDigest
	j.Domain, err = managedDomainXML(spec, nets, disks, j.Targets, j.MediaTarget, "", j.DriversTarget, "", leases)
	if err != nil {
		return err
	}
	j.Domain, err = setRestoreFirmware(j.Domain, nvramPath)
	if err != nil {
		return err
	}
	domainPath := filepath.Join(e.stateDir, operationID+".xml")
	if err = atomicWrite(domainPath, j.Domain); err != nil {
		return err
	}
	j.Stage = "defining"
	if err = e.save(j); err != nil {
		return err
	}
	if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
		return errors.New("VM definition outcome uncertain; observation required before recovery")
	}
	if err = setAutostart(ctx, spec.ID); err != nil {
		return err
	}
	if err = e.observeCreated(ctx, j); err != nil {
		return err
	}
	if err = verifyJournalRestoreFirmware(ctx, j); err != nil {
		return err
	}
	j.Stage = "defined"
	j.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
	return e.save(j)
}

func copyRestoreDisk(source, destination string, artifact protocol.BackupDiskArtifact) error {
	if _, err := os.Lstat(destination); err == nil {
		if sum, size, checkErr := fileSHA256(destination); checkErr == nil && sum == artifact.SHA256 && size == artifact.Size {
			return nil
		}
		if err := os.Remove(destination); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return copyRegularFileExclusive(source, destination)
}

func restoreNVRAMPath(spec protocol.VMCreateSpec, operationID string, replace bool) string {
	if replace {
		return filepath.Join(firmwareVariablesDir, spec.ID+"-"+operationID+"_VARS.fd")
	}
	return firmwareVariablesPath(spec.ID)
}

func installRestoreNVRAM(ctx context.Context, spec protocol.VMCreateSpec, operationID, localDir string, firmware []protocol.BackupFirmwareArtifact, retry, replace bool) (string, error) {
	var artifact *protocol.BackupFirmwareArtifact
	for i := range firmware {
		if firmware[i].Kind == "nvram" {
			artifact = &firmware[i]
		}
	}
	if spec.Boot.Firmware != "uefi" {
		if artifact != nil {
			return "", errors.New("BIOS restore has unexpected firmware state")
		}
		return "", nil
	}
	if artifact == nil || artifact.Valid() != nil {
		return "", errors.New("UEFI restore requires one valid NVRAM artifact")
	}
	info, err := os.Lstat(firmwareVariablesDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("libvirt NVRAM directory is unavailable or unsafe")
	}
	path := restoreNVRAMPath(spec, operationID, replace)
	if err := firmwareVariablesFile(path); err != nil {
		return "", err
	}
	if err := singleLinkNVRAM(path); err != nil {
		return "", err
	}
	source := filepath.Join(localDir, artifact.Filename)
	sum, size, err := fileSHA256(source)
	if err != nil || sum != artifact.SHA256 || size != artifact.Size {
		return "", errors.New("restore NVRAM artifact digest mismatch")
	}
	if retry {
		err = copyRestoreDisk(source, path, protocol.BackupDiskArtifact{SHA256: sum, Size: size})
	} else {
		err = copyRegularFileExclusive(source, path)
	}
	if err != nil {
		return "", err
	}
	uid, gid, err := backupNVRAMOwner(ctx)
	if err != nil {
		return "", err
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return "", err
	}
	if err := syncManagedDir(firmwareVariablesDir); err != nil {
		return "", err
	}
	if err := verifyRestoreNVRAM(ctx, path, *artifact); err != nil {
		return "", err
	}
	return path, nil
}

func verifyRestoreNVRAM(ctx context.Context, path string, artifact protocol.BackupFirmwareArtifact) error {
	if err := firmwareVariablesFile(path); err != nil {
		return err
	}
	if err := singleLinkNVRAM(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		return errors.New("restored NVRAM has unsafe permissions")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	uid, gid, ownerErr := backupNVRAMOwner(ctx)
	if !ok || ownerErr != nil || stat.Uid != uint32(uid) || stat.Gid != uint32(gid) {
		return errors.New("restored NVRAM owner differs from libvirt QEMU")
	}
	sum, size, err := fileSHA256(path)
	if err != nil || sum != artifact.SHA256 || size != artifact.Size {
		return errors.New("restored NVRAM digest mismatch")
	}
	return nil
}

func singleLinkNVRAM(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("restored NVRAM has another filesystem link")
	}
	return nil
}

func setRestoreFirmware(domain []byte, nvramPath string) ([]byte, error) {
	if nvramPath == "" {
		return domain, nil
	}
	var parsed managedDomain
	if err := xml.Unmarshal(domain, &parsed); err != nil {
		return nil, errors.New("restored domain definition is invalid")
	}
	if nvramPath != "" {
		if parsed.OS.Firmware != "efi" || parsed.OS.NVRAM == nil {
			return nil, errors.New("restored domain has no UEFI variable store")
		}
		parsed.OS.NVRAM.Path = nvramPath
	}
	return xml.Marshal(parsed)
}

func verifyJournalRestoreNVRAM(ctx context.Context, j managedJournal) error {
	var artifact *protocol.BackupFirmwareArtifact
	for i := range j.BackupFirmware {
		if j.BackupFirmware[i].Kind == "nvram" {
			artifact = &j.BackupFirmware[i]
		}
	}
	if artifact == nil {
		return nil
	}
	var domain managedDomain
	if err := xml.Unmarshal(j.Domain, &domain); err != nil || domain.OS.NVRAM == nil {
		return errors.New("restore journal NVRAM definition is unavailable")
	}
	if j.Command.VM == nil || domain.OS.NVRAM.Path != restoreNVRAMPath(*j.Command.VM, j.Command.OperationID, j.Command.Kind == "vm-update") {
		return errors.New("restore journal NVRAM path differs from its reserved placement")
	}
	return verifyRestoreNVRAM(ctx, domain.OS.NVRAM.Path, *artifact)
}

func verifyJournalRestoreTPM(ctx context.Context, j managedJournal) error {
	if j.Command.VM == nil || !j.Command.VM.Boot.TPMEnabled() {
		return nil
	}
	if len(j.Disks) == 0 || j.BackupTPMManifestSHA256 == "" {
		return errors.New("restore journal TPM state is missing")
	}
	base := filepath.Join(backupTPMStateRoot, j.Command.VM.ID)
	manifest, err := managedSnapshotManifest(ctx, base)
	if err != nil || !manifest.Exists || manifest.SHA256 != j.BackupTPMManifestSHA256 {
		return errors.New("restored TPM tree differs from the reserved state")
	}
	var domain managedDomain
	if err := xml.Unmarshal(j.Domain, &domain); err != nil || domain.Devices.TPM == nil || domain.Devices.TPM.Backend.Source != nil {
		return errors.New("restore journal TPM must use libvirt's UUID state path")
	}
	if j.Command.Kind == "vm-update" {
		retained, retainedErr := managedSnapshotManifest(ctx, restoreTPMPath(j.Command.VM.ID, j.Command.OperationID))
		if retainedErr != nil || !retained.Exists || retained.SHA256 != j.BackupTPMPreviousSHA256 {
			return errors.New("original TPM state is not retained after replace cutover")
		}
	}
	return nil
}

func verifyJournalRestoreFirmware(ctx context.Context, j managedJournal) error {
	if err := verifyJournalRestoreNVRAM(ctx, j); err != nil {
		return err
	}
	return verifyJournalRestoreTPM(ctx, j)
}

// BuildRestoreReplaceSpec rebuilds the original VM identity from a captured recovery-point
// specification: same UUID/name/host, remapped storage, cleared media, disconnected NICs,
// and the next definition revision. The guest stays stopped after cutover.
func BuildRestoreReplaceSpec(current, captured protocol.VMCreateSpec, req protocol.RestoreReplaceRequest) (protocol.VMCreateSpec, error) {
	if err := req.Valid(); err != nil {
		return protocol.VMCreateSpec{}, err
	}
	if current.ID == "" || current.ID != req.OriginalVMID {
		return protocol.VMCreateSpec{}, errors.New("original VM identity does not match the replace request")
	}
	if captured.ID != current.ID || len(captured.Disks) == 0 {
		return protocol.VMCreateSpec{}, errors.New("captured VM specification is incomplete")
	}
	out := captured
	out.ID = current.ID
	out.Name = current.Name
	out.PoolID = current.PoolID
	out.HostID = current.HostID
	out.Revision = current.Revision + 1
	out.StartAfterCreate = false
	out.Autostart = false
	out.MediaID = ""
	out.DriversMediaID = ""
	out.Boot.MediaBootOrder = 0
	out.NICs = nil
	disks := make([]protocol.VMDisk, len(captured.Disks))
	hasBoot := false
	for i, d := range captured.Disks {
		d.StorageID = req.StorageID
		disks[i] = d
		if d.BootOrder > 0 {
			hasBoot = true
		}
	}
	if !hasBoot && len(disks) > 0 {
		disks[0].BootOrder = 1
	}
	out.Disks = disks
	if err := protocol.ValidateVMSpec(out); err != nil {
		return protocol.VMCreateSpec{}, err
	}
	return out, nil
}

// ApplyExclusiveReplaceCutover places restored disks beside the retained originals, switches
// the managed definition to those files, and leaves the guest shut off with disconnected NICs.
// Failures before virsh define leave originals as the live definition; failures after return
// ReplaceCutoverError with the exact stage.
func (e *ManagedEngine) ApplyExclusiveReplaceCutover(ctx context.Context, operationID string, spec protocol.VMCreateSpec, localDir string, artifacts []protocol.BackupDiskArtifact, firmware ...protocol.BackupFirmwareArtifact) (retErr error) {
	if e == nil || !protocol.ValidSnapshotOperationID(operationID) || localDir == "" || !uuidPattern.MatchString(spec.ID) {
		return errors.New("replace cutover arguments are invalid")
	}
	if err := protocol.ValidateVMSpec(spec); err != nil {
		return err
	}
	if len(artifacts) != len(spec.Disks) {
		return errors.New("restore disk artifacts do not match the reconstructed VM")
	}
	state, err := ManagedVMState(ctx, spec.ID)
	if err != nil {
		return err
	}
	if state != "shut off" {
		return errors.New("original VM must be shut off before replace; refuse unsafe in-place overwrite while powered")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	var retry *managedJournal
	for i := range js {
		prior := js[i]
		if prior.Command.OperationID != operationID {
			continue
		}
		if prior.Command.Kind != "vm-update" || prior.Command.VM == nil || !reflect.DeepEqual(*prior.Command.VM, spec) || !slices.Equal(prior.BackupFirmware, firmware) {
			return &ReplaceCutoverError{Stage: "journal", Err: errors.New("replace operation ID has a different retained intent")}
		}
		if prior.Stage == "allocating" {
			retry = &prior
			break
		}
		if prior.Stage != "defining" && prior.Stage != "defined" {
			return &ReplaceCutoverError{Stage: "journal", Err: errors.New("replace operation has an unknown retained stage")}
		}
		if err := e.observer()(ctx, prior, "shut off"); err != nil {
			return &ReplaceCutoverError{Stage: "observing", Err: err}
		}
		if err := verifyJournalRestoreFirmware(ctx, prior); err != nil {
			return &ReplaceCutoverError{Stage: "firmware", Err: err}
		}
		prior.Stage = "defined"
		prior.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
		if err := e.save(prior); err != nil {
			return &ReplaceCutoverError{Stage: "settling", Err: err}
		}
		return nil
	}
	cur, err := managedCurrent(js, spec.ID)
	if err != nil {
		return err
	}
	if cur.Command.VM == nil || cur.Command.VM.ID != spec.ID {
		return errors.New("original VM is not defined on this host")
	}
	if spec.Revision != cur.Command.VM.Revision+1 {
		return fmt.Errorf("VM definition revision %d is not current; refresh the VM and replace again", spec.Revision)
	}
	previousTPM := ""
	if spec.Boot.TPMEnabled() {
		if !cur.Command.VM.Boot.TPMEnabled() {
			return errors.New("original VM has no TPM state to retain before replace")
		}
		var currentDomain managedDomain
		if err := xml.Unmarshal(cur.Domain, &currentDomain); err != nil || currentDomain.Devices.TPM == nil {
			return errors.New("original VM TPM definition is unavailable before replace")
		}
		if source, sourceErr := managedTPMStateSource(currentDomain, spec.ID); sourceErr != nil || source != filepath.Join(backupTPMStateRoot, spec.ID) {
			return errors.New("original VM TPM state is outside libvirt's UUID directory")
		}
		previous, previousErr := managedSnapshotManifest(ctx, filepath.Join(backupTPMStateRoot, spec.ID))
		if previousErr != nil || !previous.Exists {
			return errors.New("original VM TPM state is unavailable before replace")
		}
		previousTPM = previous.SHA256
	}
	var targets []managedTarget
	var disks, retained []string
	if retry == nil {
		targets, disks, retained, err = replaceCutoverDiskPlan(js, cur, spec)
	} else {
		targets, _, err = managedDiskPlan(js, spec)
		retained = append([]string{}, cur.Disks...)
		if chains, chainErr := managedDiskChains(cur); chainErr == nil {
			retained = nil
			for _, chain := range chains {
				retained = append(retained, chain...)
			}
		}
		disks = retry.Disks
	}
	if err != nil {
		return err
	}
	if retry != nil {
		if len(disks) != len(spec.Disks) || !slices.Equal(retry.Retained, retained) || !slices.Equal(retry.BackupFirmware, firmware) || retry.BackupTPMPreviousSHA256 != previousTPM {
			return &ReplaceCutoverError{Stage: "allocating", Err: errors.New("replace placement changed since its reserved intent")}
		}
		dirs := map[string]string{}
		for _, target := range targets {
			dirs[target.ID] = target.Dir
		}
		for i, path := range retry.Disks {
			storageDir := dirs[spec.Disks[i].StorageID]
			if filepath.Dir(path) != storageDir || slices.Contains(retained, path) || slices.Contains(retry.Disks[:i], path) {
				return &ReplaceCutoverError{Stage: "allocating", Err: errors.New("replace placement is unsafe")}
			}
		}
	}
	for _, t := range targets {
		_, access, readyErr := managedStorageByID(ctx, js, t.ID, t.Bytes)
		if readyErr != nil || access.Path != t.Path {
			if readyErr != nil {
				return readyErr
			}
			return errors.New("storage mount identity changed before replace cutover")
		}
		if err = replaceCutoverPrepareDirs(ctx, js, t.ID, filepath.Join(access.Path, "nex-vms")); err != nil {
			return err
		}
		if err = replaceCutoverPrepareDirs(ctx, js, t.ID, t.Dir); err != nil {
			return err
		}
	}
	if err = replaceCutoverCapacity(ctx, spec, js); err != nil {
		return err
	}
	diskTargets, mediaTarget, driversTarget := managedTargets(spec)
	byTarget := map[string]protocol.BackupDiskArtifact{}
	for _, a := range artifacts {
		byTarget[a.Target] = a
	}
	for i := range spec.Disks {
		if diskTargets[i] == "" {
			return errors.New("restore disk targets are incomplete")
		}
		if _, ok := byTarget[diskTargets[i]]; !ok {
			return fmt.Errorf("restore artifact for target %s is missing", diskTargets[i])
		}
	}
	j := managedJournal{
		Command: protocol.Command{OperationID: operationID, Kind: "vm-update", VM: &spec},
		Result:  protocol.Result{OperationID: operationID, Status: "pending", ObservedAt: time.Now().UTC()},
		Stage:   "allocating", Disks: disks, Targets: diskTargets,
		MediaTarget: mediaTarget, DriversTarget: driversTarget, Retained: retained,
		BackupFirmware:          append([]protocol.BackupFirmwareArtifact(nil), firmware...),
		BackupTPMPreviousSHA256: previousTPM,
	}
	if retry == nil {
		if err := e.save(j); err != nil {
			return err
		}
	}
	defer func() {
		if retErr != nil && !IsReplaceCutoverAttention(retErr) {
			retErr = &ReplaceCutoverError{Stage: "allocating", Err: retErr}
		}
	}()
	for i := range disks {
		art := byTarget[diskTargets[i]]
		src := filepath.Join(localDir, art.Filename)
		if err = copyRestoreDisk(src, disks[i], art); err != nil {
			return err
		}
		sum, size, sumErr := fileSHA256(disks[i])
		if sumErr != nil || sum != art.SHA256 || size != art.Size {
			return errors.New("restored disk digest mismatch after cutover placement")
		}
		if err = checkImage(ctx, disks[i]); err != nil {
			return err
		}
		if err = os.Chmod(disks[i], 0600); err != nil {
			return err
		}
	}
	for _, t := range targets {
		if err = syncManagedDir(t.Dir); err != nil {
			return err
		}
	}
	leases, err := managedSanlockVMLeases(ctx, js, spec)
	if err != nil {
		return err
	}
	if err = ensureManagedSanlockVMResources(ctx, leases); err != nil {
		return err
	}
	nvramPath, err := installRestoreNVRAM(ctx, spec, operationID, localDir, firmware, retry != nil, true)
	if err != nil {
		return err
	}
	_, tpmDigest, err := installRestoreTPM(ctx, spec, operationID, localDir, firmware, retry != nil, true, false)
	if err != nil {
		return err
	}
	if retry != nil && retry.BackupTPMManifestSHA256 != "" && retry.BackupTPMManifestSHA256 != tpmDigest {
		return errors.New("replace TPM tree differs from its reserved intent")
	}
	j.BackupTPMManifestSHA256 = tpmDigest
	domain, err := managedDomainXML(spec, nil, disks, diskTargets, mediaTarget, "", driversTarget, "", leases)
	if err != nil {
		return err
	}
	domain, err = setRestoreFirmware(domain, nvramPath)
	if err != nil {
		return err
	}
	j.Stage = "defining"
	if spec.Boot.TPMEnabled() {
		j.Stage = "swapping-tpm"
	}
	j.Domain = domain
	j.DiskChains = make([][]string, len(disks))
	for i := range disks {
		j.DiskChains[i] = []string{disks[i]}
	}
	domainPath := filepath.Join(e.stateDir, operationID+".xml")
	if err = atomicWrite(domainPath, j.Domain); err != nil {
		return err
	}
	if err = e.save(j); err != nil {
		return err
	}
	if spec.Boot.TPMEnabled() {
		if err = exchangeReplaceTPM(ctx, spec.ID, operationID, previousTPM, tpmDigest); err != nil {
			return &ReplaceCutoverError{Stage: "swapping-tpm", Err: err}
		}
		j.Stage = "defining"
		if err = e.save(j); err != nil {
			return &ReplaceCutoverError{Stage: "swapping-tpm", Err: err}
		}
	}
	// Exclusive cutover begins: definition switch. Originals remain on disk via Retained.
	if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
		return &ReplaceCutoverError{Stage: "defining", Err: errors.New("VM definition outcome uncertain; observation required before recovery")}
	}
	if err = setAutostart(ctx, spec.ID); err != nil {
		return &ReplaceCutoverError{Stage: "autostart", Err: err}
	}
	if err = e.observer()(ctx, j, "shut off"); err != nil {
		return &ReplaceCutoverError{Stage: "observing", Err: err}
	}
	if err = verifyJournalRestoreFirmware(ctx, j); err != nil {
		return &ReplaceCutoverError{Stage: "firmware", Err: err}
	}
	j.Stage = "defined"
	j.Result = protocol.Result{OperationID: operationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
	if err = e.save(j); err != nil {
		return &ReplaceCutoverError{Stage: "settling", Err: err}
	}
	return nil
}

// replaceCutoverDiskPlan allocates new disk files beside retained originals under each
// storage target's VM directory. It never reuses an original live path.
func replaceCutoverDiskPlan(js []managedJournal, cur managedJournal, spec protocol.VMCreateSpec) ([]managedTarget, []string, []string, error) {
	retained := append([]string{}, cur.Disks...)
	if chains, err := managedDiskChains(cur); err == nil {
		retained = nil
		for _, chain := range chains {
			retained = append(retained, chain...)
		}
	}
	taken := map[string]bool{}
	for _, path := range retained {
		taken[path] = true
	}
	targets, at := []managedTarget{}, map[string]int{}
	disks := make([]string, len(spec.Disks))
	for i, d := range spec.Disks {
		index, known := at[d.StorageID]
		if !known {
			s, err := managedStorage(js, d.StorageID)
			if err != nil {
				return nil, nil, nil, err
			}
			caps := s.Capabilities
			if !caps.VMDisk {
				caps = storageCapabilities(s)
			}
			if (d.Thin && !caps.Thin) || (!d.Thin && !caps.Thick) {
				return nil, nil, nil, errors.New("storage does not support the selected disk provisioning mode")
			}
			index = len(targets)
			at[d.StorageID] = index
			path := storageRegistrationPath(s)
			targets = append(targets, managedTarget{ID: d.StorageID, Path: path, Dir: filepath.Join(path, "nex-vms", spec.ID), Bytes: 4 << 30})
		}
		targets[index].Bytes += uint64(d.SizeGiB) << 30
		dir := targets[index].Dir
		path := ""
		for n := 0; n < 64 && path == ""; n++ {
			candidate := filepath.Join(dir, fmt.Sprintf("disk-%d.qcow2", n))
			if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) && !taken[candidate] {
				path = candidate
			}
		}
		if path == "" {
			return nil, nil, nil, errors.New("the VM directory holds too many retained disk files; clean it up on the host first")
		}
		if taken[path] {
			return nil, nil, nil, errors.New("refuse unsafe in-place overwrite; restore replace retains originals until exclusive cutover")
		}
		taken[path] = true
		disks[i] = path
	}
	return targets, disks, retained, nil
}
