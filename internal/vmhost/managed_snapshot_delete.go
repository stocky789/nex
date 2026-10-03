// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

type managedSnapshotDeleteRecord struct {
	TargetSnapshotID      string                         `json:"target_snapshot_id"`
	TargetSnapshotSHA256  string                         `json:"target_snapshot_sha256"`
	TargetNativeXMLSHA256 string                         `json:"target_native_xml_sha256"`
	TargetNativeIdentity  *managedSnapshotNativeIdentity `json:"target_native_identity,omitempty"`
	CurrentSnapshotBefore string                         `json:"current_snapshot_before,omitempty"`
	WasCurrent            bool                           `json:"was_current"`
	CreatedAt             time.Time                      `json:"created_at"`
	BeforeDomain          []byte                         `json:"before_domain"`
	Before                managedSnapshotObservation     `json:"before"`
	TargetOverlays        []managedSnapshotDeleteOverlay `json:"target_overlays"`
	ParentSources         []managedSnapshotDeleteOverlay `json:"parent_sources,omitempty"`
	MediaOverlays         []managedSnapshotDeleteOverlay `json:"media_overlays,omitempty"`
	MetadataDeleted       bool                           `json:"metadata_deleted,omitempty"`
	UnlinkedPaths         []string                       `json:"unlinked_paths,omitempty"`
	ObservedAfter         *managedSnapshotObservation    `json:"observed_after,omitempty"`
	ObservedDomain        []byte                         `json:"observed_domain,omitempty"`
}

type managedSnapshotDeleteOverlay struct {
	Target string `json:"target"`
	Path   string `json:"path"`
	Parent string `json:"parent,omitempty"`
}

func validateManagedSnapshotDeleteRequest(command protocol.Command) error {
	if command.Kind != "snapshot-delete" || command.VM == nil || command.Snapshot == nil ||
		!protocol.ValidSnapshotOperationID(command.OperationID) || command.Snapshot.ValidFor(command.Kind) != nil ||
		protocol.ValidateVMSpec(*command.VM) != nil || command.Storage != nil || command.Network != nil ||
		command.Media != nil || command.Power != nil || command.Delete != nil || command.VMMedia != nil ||
		command.StorageLifecycle != nil || command.ResourceLifecycle != nil || command.CredentialRetire != nil ||
		command.ZFSProvision != nil || command.ZFSReplace != nil || command.Pool != nil ||
		command.ManagementNetwork != nil || command.SystemDiskReplace != nil {
		return errors.New("invalid stopped snapshot delete request")
	}
	if command.Snapshot.SnapshotID == command.OperationID || command.Snapshot.Target.VMID != command.VM.ID {
		return errors.New("snapshot delete target does not belong to this VM")
	}
	return nil
}

func managedSnapshotDeleteIntentDigest(command protocol.Command, record managedSnapshotDeleteRecord) (string, error) {
	if err := validateManagedSnapshotDeleteRequest(command); err != nil ||
		record.TargetSnapshotID != command.Snapshot.SnapshotID || record.CreatedAt.IsZero() ||
		!lowerSnapshotHash(record.TargetSnapshotSHA256) || !lowerSnapshotHash(record.TargetNativeXMLSHA256) ||
		validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil || len(record.TargetOverlays) == 0 {
		return "", errors.New("snapshot delete intent is incomplete")
	}
	if record.TargetSnapshotSHA256 != managedSnapshotDescriptorDigest(*command.Snapshot.Target) {
		return "", errors.New("snapshot delete target descriptor differs from its intent")
	}
	b, err := json.Marshal(struct {
		Command               protocol.Command               `json:"command"`
		TargetSnapshotSHA256  string                         `json:"target_snapshot_sha256"`
		TargetNativeXMLSHA256 string                         `json:"target_native_xml_sha256"`
		TargetNativeIdentity  *managedSnapshotNativeIdentity `json:"target_native_identity,omitempty"`
		CurrentSnapshotBefore string                         `json:"current_snapshot_before,omitempty"`
		WasCurrent            bool                           `json:"was_current"`
		CreatedAt             time.Time                      `json:"created_at"`
		BeforeDomain          []byte                         `json:"before_domain"`
		Before                managedSnapshotObservation     `json:"before"`
		TargetOverlays        []managedSnapshotDeleteOverlay `json:"target_overlays"`
		ParentSources         []managedSnapshotDeleteOverlay `json:"parent_sources,omitempty"`
		MediaOverlays         []managedSnapshotDeleteOverlay `json:"media_overlays,omitempty"`
	}{command, record.TargetSnapshotSHA256, record.TargetNativeXMLSHA256, record.TargetNativeIdentity,
		record.CurrentSnapshotBefore, record.WasCurrent, record.CreatedAt, record.BeforeDomain, record.Before,
		record.TargetOverlays, record.ParentSources, record.MediaOverlays})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func validateManagedSnapshotDeleteRecord(command protocol.Command, record managedSnapshotDeleteRecord) error {
	if err := validateManagedSnapshotDeleteRequest(command); err != nil ||
		record.TargetSnapshotID != command.Snapshot.SnapshotID || record.TargetSnapshotSHA256 == "" ||
		record.TargetNativeXMLSHA256 == "" || record.CreatedAt.IsZero() ||
		record.TargetNativeIdentity != nil && record.TargetNativeIdentity.CreationTime == "" ||
		validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil || len(record.TargetOverlays) == 0 {
		return errors.New("managed snapshot delete journal is invalid")
	}
	if record.ObservedAfter != nil && validateManagedSnapshotObservation(*record.ObservedAfter) != nil {
		return errors.New("managed snapshot delete observation is invalid")
	}
	if _, err := managedSnapshotDeleteIntentDigest(command, record); err != nil {
		return err
	}
	return nil
}

func validateManagedSnapshotDeleteReservation(command protocol.Command, record managedSnapshotDeleteRecord, reservation protocol.PoolVMRecoveryReservation) error {
	if err := reservation.Valid(); err != nil || validateManagedSnapshotDeleteRecord(command, record) != nil {
		return errors.New("snapshot delete reservation is invalid")
	}
	intent, err := managedSnapshotDeleteIntentDigest(command, record)
	if err != nil {
		return err
	}
	targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(*command.Snapshot.Target)
	if err != nil {
		return err
	}
	currentBytes, err := json.Marshal(command.VM)
	if err != nil {
		return err
	}
	currentDiskBytes, err := json.Marshal(command.VM.Disks)
	if err != nil {
		return err
	}
	currentSpecDigest, currentDiskDigest := sha256.Sum256(currentBytes), sha256.Sum256(currentDiskBytes)
	if reservation.Kind != "snapshot-delete" || reservation.OperationID != command.OperationID ||
		reservation.PoolID != command.VM.PoolID || reservation.VMID != command.VM.ID ||
		reservation.OwnerHostID != command.VM.HostID || reservation.SpecRevision != command.VM.Revision ||
		reservation.SpecSHA256 != hex.EncodeToString(currentSpecDigest[:]) ||
		reservation.DiskIdentitySHA256 != hex.EncodeToString(currentDiskDigest[:]) || reservation.IntentSHA256 != intent ||
		reservation.TargetSnapshotID != record.TargetSnapshotID || reservation.TargetSnapshotSHA256 != targetDigest ||
		reservation.ResultSpecSHA256 != "" {
		return errors.New("master snapshot delete reservation differs from the local intent")
	}
	return nil
}

func (e *ManagedEngine) DeleteStoppedSnapshot(
	ctx context.Context,
	command protocol.Command,
	reserve func(context.Context, protocol.Command, string) (protocol.PoolVMRecoveryReservation, error),
	settle func(context.Context, protocol.PoolVMRecoveryEvidence) error,
) (protocol.Result, error) {
	result := protocol.Result{OperationID: command.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := validateManagedSnapshotDeleteRequest(command); err != nil || reserve == nil || settle == nil || e == nil || e.stateDir == "" {
		return result, errors.New("invalid stopped snapshot delete operation")
	}
	return e.deleteStoppedSnapshot(ctx, command, reserve, settle, result)
}

func (e *ManagedEngine) deleteStoppedSnapshot(
	ctx context.Context,
	command protocol.Command,
	reserve func(context.Context, protocol.Command, string) (protocol.PoolVMRecoveryReservation, error),
	settle func(context.Context, protocol.PoolVMRecoveryEvidence) error,
	result protocol.Result,
) (protocol.Result, error) {
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		return result, err
	}
	real, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil || real != e.stateDir {
		return result, errors.New("managed state must not use symlinks")
	}
	vmLock, err := managedSnapshotOperationLock(ctx, e.stateDir, command.VM.ID)
	if err != nil {
		return result, err
	}
	defer managedSnapshotUnlock(vmLock)
	inFlight.Lock()
	if inFlight.ids[command.OperationID] {
		inFlight.Unlock()
		return result, errors.New("snapshot delete operation is already running")
	}
	inFlight.ids[command.OperationID] = true
	inFlight.Unlock()
	defer func() { inFlight.Lock(); delete(inFlight.ids, command.OperationID); inFlight.Unlock() }()

	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return result, err
	}
	js, err := e.journals()
	if err != nil {
		unlock(global)
		return result, err
	}
	j, exists := managedSnapshotPoints(js, command.OperationID)
	if exists {
		old, _ := json.Marshal(j.Command)
		want, _ := json.Marshal(command)
		if !reflect.DeepEqual(old, want) {
			unlock(global)
			result.Error = "operation ID already belongs to different input"
			return result, nil
		}
		if j.RecoverySettled {
			unlock(global)
			return j.Result, nil
		}
	} else {
		if err := recoveryReservationRefusal(js, command); err != nil {
			unlock(global)
			result.Error = err.Error()
			return result, nil
		}
		current, currentErr := managedCurrent(js, command.VM.ID)
		if currentErr != nil || !reflect.DeepEqual(current.Command.VM, command.VM) {
			unlock(global)
			result.Error = "snapshot delete request does not match the current VM definition"
			return result, nil
		}
		now := time.Now().UTC()
		j = managedJournal{Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: now},
			Stage: "snapshot-delete-preflight", RecoveryStage: "preflight", SnapshotDelete: &managedSnapshotDeleteRecord{TargetSnapshotID: command.Snapshot.SnapshotID, CreatedAt: now}}
		if err := e.saveSnapshotJournal(j); err != nil {
			unlock(global)
			return result, err
		}
	}
	unlock(global)

	if j.RecoveryStage == "preflight" {
		js, err = e.journals()
		if err == nil {
			current, currentErr := managedCurrent(js, command.VM.ID)
			if currentErr != nil || !reflect.DeepEqual(current.Command.VM, command.VM) {
				err = errors.New("snapshot delete current VM definition changed")
			} else {
				record, intentErr := e.snapshotDeleteIntent(ctx, command, js, current)
				err = intentErr
				if err == nil {
					j.SnapshotDelete = &record
					j.Domain = append([]byte(nil), current.Domain...)
					j.Disks, j.DiskChains = append([]string(nil), current.Disks...), cloneDiskChains(current.DiskChains)
					j.Targets, j.MediaTarget, j.DriversTarget = append([]string(nil), current.Targets...), current.MediaTarget, current.DriversTarget
				}
			}
		}
		if err != nil {
			return e.failSnapshotPreflight(ctx, result, command.OperationID, err)
		}
		j.RecoveryStage, j.Stage = "local-intent", "snapshot-delete-local-intent"
		if err = e.saveSnapshotUnderLock(ctx, j, false); err != nil {
			return result, err
		}
	}
	if j.SnapshotDelete == nil || validateManagedSnapshotDeleteRecord(command, *j.SnapshotDelete) != nil {
		return e.snapshotAttention(ctx, j, errors.New("snapshot delete journal intent is invalid"))
	}
	if j.RecoveryStage == "completed" || j.RecoveryStage == "failed-before-effects" {
		if j.RecoveryStage == "completed" {
			if err := e.verifySnapshotDeleteCompleted(ctx, j); err != nil {
				return e.snapshotAttention(ctx, j, err)
			}
		}
		return e.settleSnapshotDelete(ctx, j, settle)
	}

	intent, err := managedSnapshotDeleteIntentDigest(command, *j.SnapshotDelete)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if j.RecoveryReservation == nil {
		reservation, reserveErr := reserve(ctx, command, intent)
		if reserveErr != nil {
			return e.abortSnapshotLocalIntent(ctx, j, reserveErr)
		}
		// Master reservation succeeded: never abort/delete the journal.
		if err := validateManagedSnapshotDeleteReservation(command, *j.SnapshotDelete, reservation); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		j.RecoveryReservation, j.RecoveryStage, j.Stage = &reservation, "reserved", "snapshot-delete-reserved"
		j.Result.Status, j.Result.Error = "pending", ""
		if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
	} else if err := validateManagedSnapshotDeleteReservation(command, *j.SnapshotDelete, *j.RecoveryReservation); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}

	var nativeCommandErr error
	if j.RecoveryStage == "reserved" {
		js, err = e.journals()
		if err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		if err = e.verifySnapshotDeleteBefore(ctx, js, j); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		j.RecoveryStage, j.Stage = "native", "snapshot-delete-native"
		j.Result.Status, j.Result.Error = "pending", ""
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
		nativeCommandErr = e.runNativeSnapshotDelete(ctx, command, j.SnapshotDelete)
	} else if j.RecoveryStage == "native" && !j.SnapshotDelete.MetadataDeleted {
		nativeCommandErr = e.runNativeSnapshotDelete(ctx, command, j.SnapshotDelete)
	}
	// Checkpoint MetadataDeleted under lock before unlink so a crash mid-finish
	// resumes past metadata delete without re-running consolidation.
	if nativeCommandErr == nil && j.SnapshotDelete.MetadataDeleted && j.RecoveryStage == "native" {
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
	}
	if err := e.finishNativeSnapshotDelete(ctx, command, &j); err != nil {
		cause := errors.Join(nativeCommandErr, err)
		if !j.SnapshotDelete.MetadataDeleted && len(j.SnapshotDelete.UnlinkedPaths) == 0 && !errors.Is(cause, ErrOwnershipUnconfirmed) {
			if failed, failErr := e.failSnapshotDeleteBeforeEffects(ctx, j, settle, cause); failErr == nil {
				return failed, nil
			} else if !errors.Is(failErr, errSnapshotDeleteEffectsPresent) {
				return failed, failErr
			}
		}
		return e.snapshotAttention(ctx, j, cause)
	}
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "completed", "completed", "snapshot-delete-completed"
	j.Result.Status, j.Result.Error = "pending", ""
	if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return result, err
	}
	return e.settleSnapshotDelete(ctx, j, settle)
}

func (e *ManagedEngine) snapshotDeleteIntent(ctx context.Context, command protocol.Command, js []managedJournal, current managedJournal) (managedSnapshotDeleteRecord, error) {
	observed, domain, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	if err != nil {
		return managedSnapshotDeleteRecord{}, err
	}
	nativeName := managedSnapshotNativeName(command.Snapshot.SnapshotID)
	if err := refuseSnapshotDeleteWithChildren(ctx, command.VM.ID, nativeName); err != nil {
		return managedSnapshotDeleteRecord{}, err
	}
	snapshot, nativeRaw, err := readManagedNativeSnapshotXML(ctx, command.VM.ID, nativeName)
	if err != nil {
		return managedSnapshotDeleteRecord{}, err
	}
	nativeDigest := sha256.Sum256(nativeRaw)
	currentName, err := snapshotRevertCurrentName(ctx, command.VM.ID)
	if err != nil {
		return managedSnapshotDeleteRecord{}, err
	}
	wasCurrent := currentName == nativeName
	overlays := make([]managedSnapshotDeleteOverlay, 0, len(observed.Disks))
	parents := make([]managedSnapshotDeleteOverlay, 0, len(observed.Disks))
	media := make([]managedSnapshotDeleteOverlay, 0)
	domainByTarget := make(map[string]managedDisk, len(domain.Devices.Disks))
	for _, disk := range domain.Devices.Disks {
		domainByTarget[disk.Target.Dev] = disk
	}
	for _, disk := range snapshot.Disks {
		if disk.Snapshot != "external" || disk.Source.File == "" {
			continue
		}
		index := snapshotTargetIndex(observed, disk.Name)
		if index >= 0 {
			layers := observed.Disks[index].Layers
			parent := ""
			if len(layers) > 1 {
				parent = layers[1]
			}
			overlays = append(overlays, managedSnapshotDeleteOverlay{Target: disk.Name, Path: disk.Source.File, Parent: parent})
			if wasCurrent {
				if observed.Disks[index].Source != disk.Source.File {
					return managedSnapshotDeleteRecord{}, errors.New("current snapshot disk head differs from native metadata")
				}
				if parent == "" {
					return managedSnapshotDeleteRecord{}, errors.New("current leaf snapshot has no consolidatable parent")
				}
				parents = append(parents, managedSnapshotDeleteOverlay{Target: disk.Name, Path: parent})
			}
			continue
		}
		live, ok := domainByTarget[disk.Name]
		if !ok || current.Command.VM == nil {
			return managedSnapshotDeleteRecord{}, errors.New("selected snapshot names an unmanaged writable disk")
		}
		iso, err := snapshotDeleteMediaOverlayParent(ctx, js, *current.Command.VM, live, disk.Source.File)
		if err != nil {
			return managedSnapshotDeleteRecord{}, err
		}
		if wasCurrent && (live.Source == nil || live.Source.File != disk.Source.File) {
			return managedSnapshotDeleteRecord{}, errors.New("current snapshot disk head differs from native metadata")
		}
		media = append(media, managedSnapshotDeleteOverlay{Target: disk.Name, Path: disk.Source.File, Parent: iso})
	}
	if len(overlays) == 0 {
		return managedSnapshotDeleteRecord{}, errors.New("selected snapshot has no external disk overlays")
	}
	record := managedSnapshotDeleteRecord{
		TargetSnapshotID: command.Snapshot.SnapshotID, TargetSnapshotSHA256: managedSnapshotDescriptorDigest(*command.Snapshot.Target),
		TargetNativeXMLSHA256: hex.EncodeToString(nativeDigest[:]),
		TargetNativeIdentity: &managedSnapshotNativeIdentity{
			Description: snapshot.Description, CreationTime: snapshot.CreationTime, ParentName: snapshot.Parent.Name,
		},
		CurrentSnapshotBefore: currentName, WasCurrent: wasCurrent, CreatedAt: time.Now().UTC(),
		BeforeDomain: append([]byte(nil), raw...), Before: observed, TargetOverlays: overlays, ParentSources: parents,
		MediaOverlays: media,
	}
	if _, err := managedSnapshotDeleteIntentDigest(command, record); err != nil {
		return managedSnapshotDeleteRecord{}, err
	}
	return record, nil
}

func snapshotDeleteMediaOverlayParent(ctx context.Context, js []managedJournal, spec protocol.VMCreateSpec, live managedDisk, overlay string) (string, error) {
	if live.Device != "cdrom" || live.Readonly == nil || live.Alias == nil {
		return "", errors.New("selected snapshot names an unmanaged writable disk")
	}
	mediaID := ""
	switch live.Alias.Name {
	case cdromInstallAlias:
		mediaID = spec.MediaID
	case cdromDriversAlias:
		mediaID = spec.DriversMediaID
	default:
		return "", errors.New("selected snapshot names an unmanaged writable disk")
	}
	if mediaID == "" {
		return "", errors.New("selected snapshot names an unmanaged writable disk")
	}
	media, iso, err := publishedMediaPath(js, mediaID)
	if err != nil {
		return "", errors.New("selected snapshot media overlay cannot be verified")
	}
	if verifyISO(iso, media) != nil {
		return "", errors.New("selected snapshot media overlay cannot be verified")
	}
	probe := live
	if probe.Source == nil || probe.Source.File != overlay {
		probe.Source = &sourceXML{File: overlay}
	}
	if probe.Driver.Type == "" {
		probe.Driver.Type = "qcow2"
	}
	expected := probe
	expected.Source = &sourceXML{File: iso}
	expected.Driver.Type = "raw"
	if err := verifyRevertMediaOverlay(ctx, js, spec, expected, probe); err != nil {
		return "", err
	}
	return iso, nil
}

func refuseSnapshotDeleteWithChildren(ctx context.Context, vmID, nativeName string) error {
	b, err := managedSnapshotProbe(ctx, "snapshot-list", vmID, "--name")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(b)) {
		if name == "" || name == nativeName {
			continue
		}
		parent, err := snapshotParentName(ctx, vmID, name)
		if err != nil {
			return err
		}
		if parent == nativeName {
			return errors.New("snapshot delete refuses points that still have native children")
		}
	}
	return nil
}

func snapshotParentName(ctx context.Context, vmID, name string) (string, error) {
	b, err := managedSnapshotProbe(ctx, "snapshot-dumpxml", vmID, name)
	if err != nil {
		return "", err
	}
	var snapshot struct {
		Name   string `xml:"name"`
		Parent struct {
			Name string `xml:"name"`
		} `xml:"parent"`
	}
	if xml.Unmarshal(b, &snapshot) != nil || snapshot.Name != name {
		return "", errors.New("native snapshot identity is unavailable")
	}
	return snapshot.Parent.Name, nil
}

func (e *ManagedEngine) verifySnapshotDeleteBefore(ctx context.Context, js []managedJournal, j managedJournal) error {
	record := j.SnapshotDelete
	if record == nil || j.Command.VM == nil {
		return errors.New("snapshot delete before-state is unavailable")
	}
	current, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil || current.Command.VM == nil || !reflect.DeepEqual(current.Command.VM, j.Command.VM) {
		return errors.New("current VM definition changed after snapshot delete reservation")
	}
	observed, _, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	match, digestErr := snapshotObservationDigestsMatch(observed, record.Before)
	if err != nil || digestErr != nil || !match {
		return errors.New("current VM disk state changed after snapshot delete reservation")
	}
	if err := validateManagedDefinition(raw, record.BeforeDomain); err != nil {
		return errors.New("current VM definition changed after snapshot delete reservation")
	}
	name, err := snapshotRevertCurrentName(ctx, j.Command.VM.ID)
	if err != nil || name != record.CurrentSnapshotBefore {
		return errors.New("current native snapshot changed after snapshot delete reservation")
	}
	if err := refuseSnapshotDeleteWithChildren(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.Snapshot.SnapshotID)); err != nil {
		return err
	}
	digest, err := snapshotRevertNativeXMLDigest(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.Snapshot.SnapshotID))
	if err != nil || digest != record.TargetNativeXMLSHA256 {
		return errors.New("selected native snapshot metadata changed after reservation")
	}
	return nil
}

func (e *ManagedEngine) runNativeSnapshotDelete(ctx context.Context, command protocol.Command, record *managedSnapshotDeleteRecord) error {
	if record == nil {
		return errors.New("snapshot delete intent is unavailable")
	}
	nativeName := managedSnapshotNativeName(command.Snapshot.SnapshotID)
	absent, err := managedSnapshotAbsent(ctx, command.VM.ID, nativeName)
	if err != nil {
		return err
	}
	if !absent {
		if record.WasCurrent {
			state, err := managedSnapshotProbe(ctx, "domstate", command.VM.ID)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(state)) != "shut off" {
				return errors.New("VM must remain stopped for snapshot consolidation")
			}
			// Resume after redefine: sources already on parents — skip commit/redefine.
			alreadyParents := ensureInactiveSourcesAreParents(ctx, command.VM.ID, record.ParentSources) == nil
			if !alreadyParents {
				for _, disk := range record.TargetOverlays {
					if disk.Parent == "" {
						return errors.New("current leaf snapshot has no consolidatable parent")
					}
					// Offline public equivalent of no-delete consolidation: commit into the
					// backing file and leave the overlay for the later reference-scan unlink.
					// Active blockcommit cannot start while the storage guard fences this intent.
					if _, err := run(ctx, "qemu-img", "commit", "-f", "qcow2", disk.Path); err != nil {
						return err
					}
				}
				if err := redefineInactiveSourcesToParents(ctx, command.VM.ID, record.TargetOverlays); err != nil {
					return err
				}
				if err := ensureInactiveSourcesAreParents(ctx, command.VM.ID, record.ParentSources); err != nil {
					return err
				}
			}
			// Always restore media when consolidating current leaf; already-on-parent is OK.
			if err := restoreSnapshotDeleteMediaOverlays(ctx, command.VM.ID, record.MediaOverlays); err != nil {
				return err
			}
		}
		if _, err := managedVirsh(ctx, "snapshot-delete", command.VM.ID, nativeName, "--metadata"); err != nil {
			return err
		}
	}
	record.MetadataDeleted = true
	return nil
}

func redefineInactiveSourcesToParents(ctx context.Context, vmID string, overlays []managedSnapshotDeleteOverlay) error {
	if len(overlays) == 0 {
		return errors.New("current leaf snapshot has no consolidatable parent")
	}
	parents := make([]managedSnapshotDeleteOverlay, 0, len(overlays))
	for _, overlay := range overlays {
		if overlay.Path == "" || overlay.Parent == "" {
			return errors.New("current leaf snapshot has no consolidatable parent")
		}
		parents = append(parents, managedSnapshotDeleteOverlay{Target: overlay.Target, Path: overlay.Parent})
	}
	if ensureInactiveSourcesAreParents(ctx, vmID, parents) == nil {
		return nil
	}
	raw, err := managedVirsh(ctx, "dumpxml", vmID, "--inactive")
	if err != nil {
		return errors.New("persistent VM definition is unavailable before snapshot consolidation")
	}
	var domain managedDomain
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != vmID {
		return errors.New("persistent VM definition is invalid before snapshot consolidation")
	}
	updated := string(raw)
	for _, overlay := range overlays {
		fromSingle := "file='" + overlay.Path + "'"
		toSingle := "file='" + overlay.Parent + "'"
		fromDouble := `file="` + overlay.Path + `"`
		toDouble := `file="` + overlay.Parent + `"`
		switch {
		case strings.Count(updated, fromSingle) == 1:
			updated = strings.Replace(updated, fromSingle, toSingle, 1)
		case strings.Count(updated, fromDouble) == 1:
			updated = strings.Replace(updated, fromDouble, toDouble, 1)
		default:
			return errors.New("persistent VM definition does not name the snapshot overlay exactly once")
		}
	}
	if updated == string(raw) {
		return errors.New("persistent VM definition did not change after snapshot consolidation")
	}
	tmp, err := os.CreateTemp("", "nex-snapshot-delete-*.xml")
	if err != nil {
		return err
	}
	path := tmp.Name()
	defer os.Remove(path)
	if _, err = tmp.WriteString(updated); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if _, err = managedVirsh(ctx, "define", path); err != nil {
		return errors.New("persistent VM definition could not be updated after snapshot consolidation")
	}
	return nil
}

func restoreSnapshotDeleteMediaOverlays(ctx context.Context, vmID string, overlays []managedSnapshotDeleteOverlay) error {
	if len(overlays) == 0 {
		return nil
	}
	raw, err := managedVirsh(ctx, "dumpxml", vmID, "--inactive")
	if err != nil {
		return errors.New("persistent VM definition is unavailable after media overlay restore")
	}
	var domain managedDomain
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != vmID {
		return errors.New("persistent VM definition is invalid after media overlay restore")
	}
	byTarget := make(map[string]string, len(domain.Devices.Disks))
	for _, disk := range domain.Devices.Disks {
		if disk.Device != "cdrom" || disk.Source == nil {
			continue
		}
		byTarget[disk.Target.Dev] = disk.Source.File
	}
	for _, overlay := range overlays {
		if overlay.Target == "" || overlay.Parent == "" {
			return errors.New("current leaf snapshot media overlay cannot be restored")
		}
		if byTarget[overlay.Target] == overlay.Parent {
			continue
		}
		if _, err := managedVirsh(ctx, "change-media", vmID, overlay.Target, "--force", "--config", overlay.Parent, "--update"); err != nil {
			return errors.New("current leaf snapshot media overlay cannot be restored")
		}
	}
	raw, err = managedVirsh(ctx, "dumpxml", vmID, "--inactive")
	if err != nil {
		return errors.New("persistent VM definition is unavailable after media overlay restore")
	}
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != vmID {
		return errors.New("persistent VM definition is invalid after media overlay restore")
	}
	byTarget = make(map[string]string, len(domain.Devices.Disks))
	for _, disk := range domain.Devices.Disks {
		if disk.Device != "cdrom" || disk.Source == nil {
			continue
		}
		byTarget[disk.Target.Dev] = disk.Source.File
	}
	for _, overlay := range overlays {
		if byTarget[overlay.Target] != overlay.Parent {
			return errors.New("inactive media sources did not settle on the captured ISO")
		}
	}
	return nil
}

func ensureInactiveSourcesAreParents(ctx context.Context, vmID string, parents []managedSnapshotDeleteOverlay) error {
	raw, err := managedVirsh(ctx, "dumpxml", vmID, "--inactive")
	if err != nil {
		return errors.New("persistent VM definition is unavailable after snapshot consolidation")
	}
	var domain managedDomain
	if xml.Unmarshal(raw, &domain) != nil || domain.UUID != vmID {
		return errors.New("persistent VM definition is invalid after snapshot consolidation")
	}
	byTarget := make(map[string]string, len(domain.Devices.Disks))
	for _, disk := range domain.Devices.Disks {
		if disk.Readonly != nil || disk.Device == "cdrom" || disk.Source == nil {
			continue
		}
		byTarget[disk.Target.Dev] = disk.Source.File
	}
	for _, parent := range parents {
		if byTarget[parent.Target] != parent.Path {
			return errors.New("inactive domain sources did not settle on the consolidated parents")
		}
	}
	return nil
}

func (e *ManagedEngine) finishNativeSnapshotDelete(ctx context.Context, command protocol.Command, j *managedJournal) error {
	record := j.SnapshotDelete
	if record == nil {
		return errors.New("snapshot delete intent is unavailable")
	}
	nativeName := managedSnapshotNativeName(command.Snapshot.SnapshotID)
	absent, err := managedSnapshotAbsent(ctx, command.VM.ID, nativeName)
	if err != nil || !absent {
		return errors.New("native snapshot metadata remains after delete")
	}
	record.MetadataDeleted = true
	if err := e.unlinkUnreferencedSnapshotOverlays(ctx, command.VM.ID, record); err != nil {
		return err
	}
	if record.WasCurrent {
		if err := e.publishSnapshotDeleteCurrent(ctx, *j); err != nil {
			return err
		}
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	current, err := managedCurrent(js, command.VM.ID)
	if err != nil {
		return err
	}
	observation, _, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	if err != nil {
		return err
	}
	record.ObservedAfter, record.ObservedDomain = &observation, append([]byte(nil), raw...)
	j.Disks, j.DiskChains = snapshotObservationProjection(observation)
	j.Domain = append([]byte(nil), raw...)
	return nil
}

func (e *ManagedEngine) publishSnapshotDeleteCurrent(ctx context.Context, j managedJournal) error {
	if j.Command.VM == nil || j.SnapshotDelete == nil || !j.SnapshotDelete.WasCurrent || len(j.SnapshotDelete.ParentSources) == 0 {
		return errors.New("completed current-leaf delete has no parent disk projection")
	}
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(global)
	js, err := e.journals()
	if err != nil {
		return err
	}
	if err = recoveryReservationRefusal(js, j.Command); err != nil {
		return err
	}
	cur, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil || !reflect.DeepEqual(cur.Command.VM, j.Command.VM) {
		return errors.New("managed VM current definition changed during snapshot delete")
	}
	byTarget := make(map[string]string, len(j.SnapshotDelete.ParentSources))
	for _, parent := range j.SnapshotDelete.ParentSources {
		byTarget[parent.Target] = parent.Path
	}
	if len(cur.Targets) != len(cur.Disks) || len(cur.Targets) != len(j.SnapshotDelete.ParentSources) {
		return errors.New("current VM disk set differs from the deleted leaf")
	}
	disks := make([]string, len(cur.Targets))
	chains := make([][]string, len(cur.Targets))
	for i, target := range cur.Targets {
		parent, ok := byTarget[target]
		if !ok {
			return errors.New("deleted leaf is missing a parent for a current disk")
		}
		chain, err := observeManagedDiskChain(ctx, parent)
		if err != nil {
			return err
		}
		disks[i], chains[i] = parent, chain
	}
	raw, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err != nil {
		return errors.New("persistent VM definition is unavailable after snapshot delete")
	}
	cur.Disks, cur.DiskChains, cur.Domain = disks, chains, append([]byte(nil), raw...)
	return e.save(cur)
}

func (e *ManagedEngine) unlinkUnreferencedSnapshotOverlays(ctx context.Context, vmID string, record *managedSnapshotDeleteRecord) error {
	referenced, err := collectManagedSnapshotReferences(ctx, vmID)
	if err != nil {
		return err
	}
	unlinked := append([]string(nil), record.UnlinkedPaths...)
	candidates := append(append([]managedSnapshotDeleteOverlay(nil), record.TargetOverlays...), record.MediaOverlays...)
	for _, overlay := range candidates {
		if slices.Contains(unlinked, overlay.Path) {
			continue
		}
		if referenced[overlay.Path] {
			continue
		}
		info, err := os.Lstat(overlay.Path)
		if errors.Is(err, os.ErrNotExist) {
			unlinked = append(unlinked, overlay.Path)
			continue
		}
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || !ok || st.Nlink != 1 {
			return errors.New("snapshot overlay is not an exact single-link unreferenced file")
		}
		real, err := filepath.EvalSymlinks(overlay.Path)
		if err != nil || real != overlay.Path {
			return errors.New("snapshot overlay path is not a regular unmanaged file")
		}
		if err := os.Remove(overlay.Path); err != nil {
			return err
		}
		unlinked = append(unlinked, overlay.Path)
	}
	record.UnlinkedPaths = unlinked
	return nil
}

func collectManagedSnapshotReferences(ctx context.Context, vmID string) (map[string]bool, error) {
	referenced := map[string]bool{}
	addDomain := func(raw []byte) error {
		var domain managedDomain
		if xml.Unmarshal(raw, &domain) != nil {
			return errors.New("domain XML reference scan failed")
		}
		for _, disk := range domain.Devices.Disks {
			if disk.Source == nil || disk.Source.File == "" {
				continue
			}
			referenced[disk.Source.File] = true
			if disk.Device == "cdrom" || disk.Readonly != nil {
				continue
			}
			chain, err := observeManagedDiskChain(ctx, disk.Source.File)
			if err != nil {
				return err
			}
			for _, path := range chain {
				referenced[path] = true
			}
		}
		return nil
	}
	for _, args := range [][]string{{"dumpxml", vmID, "--inactive"}, {"dumpxml", vmID}} {
		raw, err := managedSnapshotProbe(ctx, args...)
		if err != nil {
			if len(args) == 2 {
				continue
			}
			return nil, err
		}
		if err := addDomain(raw); err != nil {
			return nil, err
		}
	}
	names, err := managedSnapshotProbe(ctx, "snapshot-list", vmID, "--name")
	if err != nil {
		return nil, err
	}
	for _, name := range strings.Fields(string(names)) {
		if name == "" {
			continue
		}
		snapshot, raw, err := readManagedNativeSnapshotXML(ctx, vmID, name)
		if err != nil {
			return nil, err
		}
		_ = raw
		for _, disk := range snapshot.Disks {
			if disk.Source.File != "" {
				referenced[disk.Source.File] = true
				chain, chainErr := observeManagedDiskChain(ctx, disk.Source.File)
				if chainErr != nil {
					return nil, chainErr
				}
				for _, path := range chain {
					referenced[path] = true
				}
			}
		}
		if snapshot.Domain.Type != "" && snapshot.Domain.Inner != "" {
			if err := addDomain([]byte("<domain type=\"" + snapshot.Domain.Type + "\">" + snapshot.Domain.Inner + "</domain>")); err != nil {
				return nil, err
			}
		}
	}
	return referenced, nil
}

func (e *ManagedEngine) verifySnapshotDeleteCompleted(ctx context.Context, j managedJournal) error {
	if j.SnapshotDelete == nil || j.SnapshotDelete.ObservedAfter == nil || !j.SnapshotDelete.MetadataDeleted {
		return errors.New("completed snapshot delete lacks native observation")
	}
	absent, err := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.Snapshot.SnapshotID))
	if err != nil || !absent {
		return errors.New("deleted snapshot metadata is still present")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	current, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil {
		return err
	}
	observed, _, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	match, digestErr := snapshotObservationDigestsMatch(observed, *j.SnapshotDelete.ObservedAfter)
	if err != nil || digestErr != nil || !match || !reflect.DeepEqual(raw, j.SnapshotDelete.ObservedDomain) {
		return errors.New("completed snapshot delete observation changed")
	}
	return nil
}

func (e *ManagedEngine) settleSnapshotDelete(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error) (protocol.Result, error) {
	if j.RecoveryReservation == nil || j.RecoveryStage != "completed" && j.RecoveryStage != "failed-before-effects" {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot delete has no terminal master reservation"}, nil
	}
	evidence, err := e.PoolVMRecoveryEvidence(ctx, *j.RecoveryReservation)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	raw, err := readSmall(filepath.Join(e.stateDir, j.Command.OperationID+".json"), managedSnapshotJournalLimit)
	digest := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(digest[:]) != evidence.JournalSHA256 {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot delete receipt differs from its immutable journal"}, nil
	}
	if err = settle(ctx, evidence); err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "master snapshot delete settlement is not acknowledged"}, nil
	}
	global, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	defer unlock(global)
	js, err := e.journals()
	if err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	latest, ok := managedSnapshotPoints(js, j.Command.OperationID)
	if !ok || latest.RecoveryReservation == nil || *latest.RecoveryReservation != *j.RecoveryReservation || latest.RecoveryStage != j.RecoveryStage {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot delete journal changed before settlement acknowledgement"}, nil
	}
	latest.RecoverySettled = true
	latest.Result.Status, latest.Result.ObservedAt = "succeeded", time.Now().UTC()
	if latest.RecoveryOutcome == "failed-before-effects" {
		latest.Result.Status = "failed"
	} else {
		latest.Result.Error = ""
	}
	if err = e.saveSnapshotJournal(latest); err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: err.Error()}, nil
	}
	return latest.Result, nil
}

func (e *ManagedEngine) poolVMSnapshotDeleteEvidence(ctx context.Context, reservation protocol.PoolVMRecoveryReservation) (protocol.PoolVMRecoveryEvidence, error) {
	path := reservation.OperationID + ".json"
	raw, err := readSmall(filepath.Join(e.stateDir, path), managedSnapshotJournalLimit)
	if err != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot delete journal is unavailable")
	}
	j, err := e.readJournal(path)
	if err != nil || j.Command.Kind != "snapshot-delete" || j.RecoveryReservation == nil || *j.RecoveryReservation != reservation ||
		j.RecoverySettled || j.Result.Status != "pending" || j.SnapshotDelete == nil ||
		validateManagedSnapshotDeleteReservation(j.Command, *j.SnapshotDelete, reservation) != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot delete is not an exact unsettled terminal result")
	}
	switch {
	case j.RecoveryStage == "completed" && j.RecoveryOutcome == "completed":
		if err = e.verifySnapshotDeleteCompleted(ctx, j); err != nil {
			return protocol.PoolVMRecoveryEvidence{}, err
		}
	case j.RecoveryStage == "failed-before-effects" && j.RecoveryOutcome == "failed-before-effects":
		absent, absentErr := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.Snapshot.SnapshotID))
		if absentErr != nil || absent || j.SnapshotDelete.MetadataDeleted || len(j.SnapshotDelete.UnlinkedPaths) != 0 {
			return protocol.PoolVMRecoveryEvidence{}, errors.New("failed snapshot delete may have left native effects")
		}
	default:
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot delete outcome is partial or unresolved")
	}
	latest, err := readSmall(filepath.Join(e.stateDir, path), managedSnapshotJournalLimit)
	if err != nil || !bytes.Equal(raw, latest) {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot delete journal changed during evidence collection")
	}
	digest := sha256.Sum256(raw)
	evidence := protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: j.RecoveryOutcome, JournalSHA256: hex.EncodeToString(digest[:]),
	}
	if err = evidence.Valid(); err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	return evidence, nil
}

var errSnapshotDeleteEffectsPresent = errors.New("snapshot delete left native effects")

func (e *ManagedEngine) failSnapshotDeleteBeforeEffects(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error, cause error) (protocol.Result, error) {
	if j.SnapshotDelete == nil || j.SnapshotDelete.MetadataDeleted || len(j.SnapshotDelete.UnlinkedPaths) != 0 {
		return protocol.Result{}, errSnapshotDeleteEffectsPresent
	}
	absent, err := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(j.Command.Snapshot.SnapshotID))
	if err != nil || absent {
		return protocol.Result{}, errSnapshotDeleteEffectsPresent
	}
	j.SnapshotDelete.ObservedAfter = nil
	j.SnapshotDelete.ObservedDomain = nil
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "failed-before-effects", "failed-before-effects", "snapshot-delete-failed-before-effects"
	j.Result.Status, j.Result.Error = "pending", cause.Error()
	if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return j.Result, err
	}
	return e.settleSnapshotDelete(ctx, j, settle)
}
