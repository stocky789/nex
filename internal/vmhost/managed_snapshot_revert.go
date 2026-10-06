// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"os/user"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

type managedSnapshotRevertFirmware struct {
	Kind           string                      `json:"kind"`
	SourcePath     string                      `json:"source_path"`
	TargetPath     string                      `json:"target_path"`
	TemporaryPath  string                      `json:"temporary_path"`
	PreviousPath   string                      `json:"previous_path"`
	BeforeManifest managedSnapshotFileManifest `json:"before_manifest"`
	TargetManifest managedSnapshotFileManifest `json:"target_manifest"`
}

type managedSnapshotRevertRecord struct {
	TargetSnapshotID              string                          `json:"target_snapshot_id"`
	TargetSnapshotSHA256          string                          `json:"target_snapshot_sha256"`
	TargetJournalSHA256           string                          `json:"target_journal_sha256"`
	TargetNativeXMLSHA256         string                          `json:"target_native_xml_sha256"`
	TargetNativeIdentity          *managedSnapshotNativeIdentity  `json:"target_native_identity,omitempty"`
	ObservedTargetNativeXMLSHA256 string                          `json:"observed_target_native_xml_sha256,omitempty"`
	CurrentSnapshotBefore         string                          `json:"current_snapshot_before,omitempty"`
	CreatedAt                     time.Time                       `json:"created_at"`
	ResultSpec                    protocol.VMCreateSpec           `json:"result_spec"`
	CapturedSpec                  protocol.VMCreateSpec           `json:"captured_spec"`
	BeforeDomain                  []byte                          `json:"before_domain"`
	TargetDomain                  []byte                          `json:"target_domain"`
	Before                        managedSnapshotObservation      `json:"before"`
	TargetBefore                  managedSnapshotObservation      `json:"target_before"`
	Target                        managedSnapshotObservation      `json:"target"`
	ObservedAfter                 *managedSnapshotObservation     `json:"observed_after,omitempty"`
	ObservedDomain                []byte                          `json:"observed_domain,omitempty"`
	Firmware                      []managedSnapshotRevertFirmware `json:"firmware,omitempty"`
}

type managedSnapshotNativeIdentity struct {
	Description  string `json:"description"`
	CreationTime string `json:"creation_time"`
	ParentName   string `json:"parent_name,omitempty"`
}

func validateManagedSnapshotRevertRequest(command protocol.Command) error {
	if command.Kind != "snapshot-revert" || command.VM == nil || command.Snapshot == nil ||
		!protocol.ValidSnapshotOperationID(command.OperationID) || command.Snapshot.ValidFor(command.Kind) != nil ||
		protocol.ValidateVMSpec(*command.VM) != nil || command.Storage != nil || command.Network != nil ||
		command.Media != nil || command.Power != nil || command.Delete != nil || command.VMMedia != nil ||
		command.StorageLifecycle != nil || command.ResourceLifecycle != nil || command.CredentialRetire != nil ||
		command.ZFSProvision != nil || command.ZFSReplace != nil || command.Pool != nil ||
		command.ManagementNetwork != nil {
		return errors.New("invalid stopped snapshot revert request")
	}
	if command.Snapshot.SnapshotID == command.OperationID || command.Snapshot.Target.VMID != command.VM.ID {
		return errors.New("snapshot revert target does not belong to this VM")
	}
	return nil
}

func managedSnapshotRevertIntentDigest(command protocol.Command, record managedSnapshotRevertRecord) (string, error) {
	if err := validateManagedSnapshotRevertRequest(command); err != nil ||
		record.TargetSnapshotID != command.Snapshot.SnapshotID || record.CreatedAt.IsZero() ||
		!lowerSnapshotHash(record.TargetSnapshotSHA256) || !lowerSnapshotHash(record.TargetJournalSHA256) ||
		!lowerSnapshotHash(record.TargetNativeXMLSHA256) || validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateSnapshotBeforeDomain(record.TargetDomain, record.TargetBefore) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil || validateManagedSnapshotObservation(record.TargetBefore) != nil || validateManagedSnapshotObservation(record.Target) != nil ||
		protocol.ValidateVMSpec(record.CapturedSpec) != nil || protocol.ValidateVMSpec(record.ResultSpec) != nil {
		return "", errors.New("snapshot revert intent is incomplete")
	}
	if record.TargetSnapshotSHA256 != managedSnapshotDescriptorDigest(*command.Snapshot.Target) {
		return "", errors.New("snapshot revert target descriptor differs from its intent")
	}
	result, err := protocol.PoolVMSnapshotRevertSpec(record.CapturedSpec, *command.VM)
	if err != nil || !reflect.DeepEqual(result, record.ResultSpec) {
		return "", errors.New("snapshot revert result specification differs from the captured point")
	}
	b, err := json.Marshal(struct {
		Command               protocol.Command                `json:"command"`
		TargetSnapshotSHA256  string                          `json:"target_snapshot_sha256"`
		TargetJournalSHA256   string                          `json:"target_journal_sha256"`
		TargetNativeXMLSHA256 string                          `json:"target_native_xml_sha256"`
		TargetNativeIdentity  *managedSnapshotNativeIdentity  `json:"target_native_identity,omitempty"`
		CurrentSnapshotBefore string                          `json:"current_snapshot_before,omitempty"`
		CreatedAt             time.Time                       `json:"created_at"`
		ResultSpec            protocol.VMCreateSpec           `json:"result_spec"`
		CapturedSpec          protocol.VMCreateSpec           `json:"captured_spec"`
		BeforeDomain          []byte                          `json:"before_domain"`
		TargetDomain          []byte                          `json:"target_domain"`
		Before                managedSnapshotObservation      `json:"before"`
		TargetBefore          managedSnapshotObservation      `json:"target_before"`
		Target                managedSnapshotObservation      `json:"target"`
		Firmware              []managedSnapshotRevertFirmware `json:"firmware,omitempty"`
	}{command, record.TargetSnapshotSHA256, record.TargetJournalSHA256, record.TargetNativeXMLSHA256, record.TargetNativeIdentity,
		record.CurrentSnapshotBefore, record.CreatedAt, record.ResultSpec, record.CapturedSpec, record.BeforeDomain,
		record.TargetDomain, record.Before, record.TargetBefore, record.Target, record.Firmware})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(b)
	return hex.EncodeToString(digest[:]), nil
}

func managedSnapshotDescriptorDigest(descriptor protocol.PoolVMSnapshot) string {
	digest, _ := protocol.PoolVMSnapshotDescriptorSHA256(descriptor)
	return digest
}

func validateManagedSnapshotRevertRecord(command protocol.Command, record managedSnapshotRevertRecord) error {
	if err := validateManagedSnapshotRevertRequest(command); err != nil ||
		record.TargetSnapshotID != command.Snapshot.SnapshotID || record.TargetSnapshotSHA256 == "" ||
		record.TargetJournalSHA256 == "" || record.TargetNativeXMLSHA256 == "" || record.CreatedAt.IsZero() ||
		record.TargetNativeIdentity != nil && record.TargetNativeIdentity.CreationTime == "" ||
		validateSnapshotBeforeDomain(record.BeforeDomain, record.Before) != nil ||
		validateSnapshotBeforeDomain(record.TargetDomain, record.TargetBefore) != nil ||
		validateManagedSnapshotObservation(record.Before) != nil || validateManagedSnapshotObservation(record.TargetBefore) != nil || validateManagedSnapshotObservation(record.Target) != nil ||
		protocol.ValidateVMSpec(record.CapturedSpec) != nil || protocol.ValidateVMSpec(record.ResultSpec) != nil {
		return errors.New("managed snapshot revert journal is invalid")
	}
	if record.ObservedTargetNativeXMLSHA256 != "" && !lowerSnapshotHash(record.ObservedTargetNativeXMLSHA256) {
		return errors.New("managed snapshot revert native metadata observation is invalid")
	}
	if record.ObservedAfter != nil && validateManagedSnapshotObservation(*record.ObservedAfter) != nil {
		return errors.New("managed snapshot revert observation is invalid")
	}
	if len(record.Firmware) > 2 {
		return errors.New("managed snapshot revert firmware state is invalid")
	}
	for _, artifact := range record.Firmware {
		if (artifact.Kind != "nvram" && artifact.Kind != "swtpm") || !filepath.IsAbs(artifact.SourcePath) ||
			filepath.Clean(artifact.SourcePath) != artifact.SourcePath || !filepath.IsAbs(artifact.TargetPath) ||
			filepath.Clean(artifact.TargetPath) != artifact.TargetPath || !filepath.IsAbs(artifact.TemporaryPath) ||
			filepath.Clean(artifact.TemporaryPath) != artifact.TemporaryPath || !filepath.IsAbs(artifact.PreviousPath) ||
			filepath.Clean(artifact.PreviousPath) != artifact.PreviousPath || !validManagedSnapshotManifest(artifact.BeforeManifest) ||
			!validManagedSnapshotManifest(artifact.TargetManifest) || !artifact.BeforeManifest.Exists || !artifact.TargetManifest.Exists {
			return errors.New("managed snapshot revert firmware state is invalid")
		}
	}
	if _, err := managedSnapshotRevertIntentDigest(command, record); err != nil {
		return err
	}
	return nil
}

func validateManagedSnapshotRevertReservation(command protocol.Command, record managedSnapshotRevertRecord, reservation protocol.PoolVMRecoveryReservation) error {
	if err := reservation.Valid(); err != nil || validateManagedSnapshotRevertRecord(command, record) != nil {
		return errors.New("snapshot revert reservation is invalid")
	}
	intent, err := managedSnapshotRevertIntentDigest(command, record)
	if err != nil {
		return err
	}
	targetDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(*command.Snapshot.Target)
	if err != nil {
		return err
	}
	resultBytes, err := json.Marshal(record.ResultSpec)
	if err != nil {
		return err
	}
	resultDigest := sha256.Sum256(resultBytes)
	currentBytes, err := json.Marshal(command.VM)
	if err != nil {
		return err
	}
	currentDiskBytes, err := json.Marshal(command.VM.Disks)
	if err != nil {
		return err
	}
	currentSpecDigest, currentDiskDigest := sha256.Sum256(currentBytes), sha256.Sum256(currentDiskBytes)
	if reservation.Kind != "snapshot-revert" || reservation.OperationID != command.OperationID ||
		reservation.PoolID != command.VM.PoolID || reservation.VMID != command.VM.ID ||
		reservation.OwnerHostID != command.VM.HostID || reservation.SpecRevision != command.VM.Revision ||
		reservation.SpecSHA256 != hex.EncodeToString(currentSpecDigest[:]) ||
		reservation.DiskIdentitySHA256 != hex.EncodeToString(currentDiskDigest[:]) || reservation.IntentSHA256 != intent ||
		reservation.TargetSnapshotID != record.TargetSnapshotID || reservation.TargetSnapshotSHA256 != targetDigest ||
		reservation.ResultSpecSHA256 != hex.EncodeToString(resultDigest[:]) {
		return errors.New("master snapshot revert reservation differs from the local intent")
	}
	return nil
}

func snapshotRevertCurrentName(ctx context.Context, vmID string) (string, error) {
	b, err := managedSnapshotProbe(ctx, "snapshot-current", vmID, "--name")
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(string(b))
	if name == "" || strings.ContainsAny(name, "\r\n\x00") {
		return "", errors.New("VM current snapshot identity is unavailable")
	}
	return name, nil
}

func snapshotRevertNativeXMLDigest(ctx context.Context, vmID, nativeName string) (string, error) {
	_, raw, err := readManagedNativeSnapshotXML(ctx, vmID, nativeName)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func (e *ManagedEngine) verifiedSnapshotRevertTargetDigest(ctx context.Context, js []managedJournal, target managedJournal) (string, error) {
	if target.Command.VM == nil || target.Snapshot == nil {
		return "", errors.New("selected snapshot capture journal is unavailable")
	}
	name := managedSnapshotNativeName(target.Command.OperationID)
	digest, err := snapshotRevertNativeXMLDigest(ctx, target.Command.VM.ID, name)
	if err != nil {
		return "", err
	}
	var latest *managedJournal
	for i := range js {
		candidate := &js[i]
		if candidate.Command.Kind != "snapshot-revert" || candidate.Command.Snapshot == nil ||
			candidate.Command.VM == nil || candidate.Command.VM.ID != target.Command.VM.ID ||
			candidate.Command.Snapshot.SnapshotID != target.Command.OperationID || !candidate.RecoverySettled ||
			candidate.RecoveryStage != "completed" || candidate.RecoveryOutcome != "completed" ||
			candidate.Result.Status != "succeeded" || candidate.SnapshotRevert == nil ||
			candidate.SnapshotRevert.ObservedTargetNativeXMLSHA256 == "" {
			continue
		}
		if candidate.RecoveryReservation == nil {
			continue
		}
		if latest == nil || candidate.RecoveryReservation.OwnerOperationOrder > latest.RecoveryReservation.OwnerOperationOrder {
			latest = candidate
		}
	}
	if latest != nil {
		if digest != latest.SnapshotRevert.ObservedTargetNativeXMLSHA256 {
			return "", errors.New("selected native snapshot metadata changed after its latest settled revert")
		}
		return digest, nil
	}
	initial, err := managedSnapshotXMLDigest(ctx, target.Command.VM.ID, name, *target.Snapshot)
	if err != nil || initial != target.Snapshot.NativeXMLSHA256 || digest != initial {
		return "", errors.New("selected native snapshot metadata differs from its captured state")
	}
	return digest, nil
}

func (e *ManagedEngine) snapshotRevertSource(js []managedJournal, target protocol.PoolVMSnapshot) (managedJournal, error) {
	var found managedJournal
	for _, candidate := range js {
		if candidate.Command.OperationID != target.ID || candidate.Command.Kind != "snapshot-create" ||
			candidate.Command.VM == nil || candidate.Command.VM.ID != target.VMID || candidate.Snapshot == nil {
			continue
		}
		if found.Command.OperationID != "" {
			return managedJournal{}, errors.New("snapshot target has duplicate native create journals")
		}
		found = candidate
	}
	if found.Command.OperationID == "" || !found.RecoverySettled || found.RecoveryStage != "completed" ||
		found.RecoveryOutcome != "completed" || found.Result.Status != "succeeded" || found.RecoveryReservation == nil ||
		validateManagedSnapshotRecord(target.ID, *found.Snapshot) != nil {
		return managedJournal{}, errors.New("snapshot target has no settled, validated local capture journal")
	}
	if found.RecoveryReservation.SpecSHA256 != target.SpecSHA256 || found.RecoveryReservation.DiskIdentitySHA256 != target.DiskIdentitySHA256 ||
		found.RecoveryReservation.SpecRevision != target.SpecRevision || found.Snapshot.Dependencies == nil || target.Dependencies == nil ||
		!reflect.DeepEqual(found.Snapshot.Dependencies, target.Dependencies) {
		return managedJournal{}, errors.New("snapshot target descriptor does not match its captured VM evidence")
	}
	return found, nil
}

// SnapshotCapturedSpec returns the exact execution definition retained by a settled
// snapshot-create journal. Display metadata may have changed in the pool catalog; the
// immutable VM and disk digests must still match.
func (e *ManagedEngine) SnapshotCapturedSpec(target protocol.PoolVMSnapshot) (protocol.VMCreateSpec, error) {
	if e == nil || target.Valid() != nil {
		return protocol.VMCreateSpec{}, errors.New("invalid snapshot descriptor")
	}
	js, err := e.journals()
	if err != nil {
		return protocol.VMCreateSpec{}, errors.New("managed snapshot journal is unavailable")
	}
	j, err := e.snapshotRevertSource(js, target)
	if err != nil || j.Command.VM == nil {
		return protocol.VMCreateSpec{}, errors.New("snapshot captured definition is unavailable")
	}
	return *j.Command.VM, nil
}

func (e *ManagedEngine) snapshotRevertIntent(ctx context.Context, command protocol.Command, js []managedJournal, current, targetJournal managedJournal) (managedSnapshotRevertRecord, error) {
	if current.Command.VM == nil || !reflect.DeepEqual(current.Command.VM, command.VM) || targetJournal.Command.VM == nil ||
		targetJournal.Snapshot == nil || targetJournal.Snapshot.ObservedAfter == nil {
		return managedSnapshotRevertRecord{}, errors.New("snapshot revert VM or capture evidence changed")
	}
	before, _, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	name, err := snapshotRevertCurrentName(ctx, command.VM.ID)
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	nativeDigest, err := e.verifiedSnapshotRevertTargetDigest(ctx, js, targetJournal)
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	nativeXML, _, err := readManagedNativeSnapshotXML(ctx, command.VM.ID, managedSnapshotNativeName(command.Snapshot.SnapshotID))
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	// Description is optional on create; libvirt omits the element when unset. CreationTime is required identity.
	if nativeXML.CreationTime == "" {
		return managedSnapshotRevertRecord{}, errors.New("selected native snapshot metadata cannot be read")
	}
	targetDescriptorDigest, err := protocol.PoolVMSnapshotDescriptorSHA256(*command.Snapshot.Target)
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	targetRaw, err := os.ReadFile(filepath.Join(e.stateDir, targetJournal.Command.OperationID+".json"))
	if err != nil {
		return managedSnapshotRevertRecord{}, errors.New("selected snapshot source journal is unavailable")
	}
	targetHash := sha256.Sum256(targetRaw)
	capturedSpec := *targetJournal.Command.VM
	resultSpec, err := protocol.PoolVMSnapshotRevertSpec(capturedSpec, *command.VM)
	if err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	var captured managedDomain
	if err = xml.Unmarshal(targetJournal.Snapshot.BeforeDomain, &captured); err != nil || captured.UUID != command.VM.ID {
		return managedSnapshotRevertRecord{}, errors.New("selected snapshot has no valid captured VM definition")
	}
	if (resultSpec.Boot.Firmware == "uefi") != (captured.OS.Firmware == "efi") ||
		resultSpec.Boot.TPMEnabled() != (captured.Devices.TPM != nil) {
		return managedSnapshotRevertRecord{}, errors.New("selected snapshot firmware profile differs from its captured VM configuration")
	}
	artifacts := make(map[string]managedSnapshotFirmwareArtifact, len(targetJournal.Snapshot.Firmware))
	for _, artifact := range targetJournal.Snapshot.Firmware {
		artifacts[artifact.Kind] = artifact
	}
	firmware := make([]managedSnapshotRevertFirmware, 0, len(artifacts))
	for _, kind := range []string{"nvram", "swtpm"} {
		needed := kind == "nvram" && captured.OS.Firmware == "efi" || kind == "swtpm" && captured.Devices.TPM != nil
		artifact, exists := artifacts[kind]
		if !needed {
			if exists {
				return managedSnapshotRevertRecord{}, errors.New("selected snapshot captures firmware not present in its saved VM definition")
			}
			continue
		}
		if !exists || !snapshotManifestsEqual(artifact.SourceManifest, artifact.SnapshotManifest) || !artifact.SnapshotManifest.Exists {
			return managedSnapshotRevertRecord{}, errors.New("selected snapshot firmware state is incomplete")
		}
		source := artifact.SourcePath
		if kind == "nvram" {
			if captured.OS.NVRAM == nil || captured.OS.NVRAM.Path != source || firmwareVariablesFile(source) != nil {
				return managedSnapshotRevertRecord{}, errors.New("selected snapshot NVRAM path is not a supported libvirt variable store")
			}
		} else {
			expected, sourceErr := managedTPMStateSource(captured, command.VM.ID)
			if sourceErr != nil || source != expected {
				return managedSnapshotRevertRecord{}, errors.New("selected snapshot TPM state path is not the VM's libvirt state directory")
			}
		}
		currentManifest, manifestErr := managedSnapshotManifest(ctx, source)
		if manifestErr != nil {
			return managedSnapshotRevertRecord{}, errors.New("current paired firmware state cannot be inspected")
		}
		temporary := source + ".nex-revert-" + command.OperationID + ".tmp"
		previous := source + ".nex-revert-" + command.OperationID + ".previous"
		firmware = append(firmware, managedSnapshotRevertFirmware{
			Kind: kind, SourcePath: source, TargetPath: artifact.SnapshotPath,
			TemporaryPath: temporary, PreviousPath: previous,
			BeforeManifest: currentManifest, TargetManifest: artifact.SnapshotManifest,
		})
	}
	record := managedSnapshotRevertRecord{
		TargetSnapshotID: command.Snapshot.SnapshotID, TargetSnapshotSHA256: targetDescriptorDigest,
		TargetJournalSHA256: hex.EncodeToString(targetHash[:]), TargetNativeXMLSHA256: nativeDigest,
		TargetNativeIdentity:  &managedSnapshotNativeIdentity{Description: nativeXML.Description, CreationTime: nativeXML.CreationTime, ParentName: nativeXML.Parent.Name},
		CurrentSnapshotBefore: name, CreatedAt: time.Now().UTC(), ResultSpec: resultSpec, CapturedSpec: capturedSpec,
		BeforeDomain: append([]byte(nil), raw...), TargetDomain: append([]byte(nil), targetJournal.Snapshot.BeforeDomain...),
		Before: before, TargetBefore: *cloneSnapshotObservation(&targetJournal.Snapshot.Before),
		Target: *cloneSnapshotObservation(&targetJournal.Snapshot.Before), Firmware: firmware,
	}
	if err := e.snapshotRevertArtifactPaths(record); err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	if _, err := managedSnapshotRevertIntentDigest(command, record); err != nil {
		return managedSnapshotRevertRecord{}, err
	}
	return record, nil
}

func runNativeSnapshotRevert(ctx context.Context, command protocol.Command) error {
	_, err := managedVirsh(ctx, "snapshot-revert", command.VM.ID, managedSnapshotNativeName(command.Snapshot.SnapshotID), "--force")
	return err
}

func nativeSnapshotUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for i, r := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

func libvirtGeneratedMediaOverlayOwner(uid, gid int) bool {
	if uid == os.Geteuid() && gid == os.Getegid() {
		return true
	}
	account, err := user.Lookup("libvirt-qemu")
	if err != nil {
		return false
	}
	ownerUID, err := strconv.Atoi(account.Uid)
	if err != nil || uid != ownerUID {
		return false
	}
	ownerGID, err := strconv.Atoi(account.Gid)
	if err == nil && gid == ownerGID {
		return true
	}
	group, err := user.LookupGroup("libvirt-qemu")
	if err != nil {
		return false
	}
	groupGID, err := strconv.Atoi(group.Gid)
	return err == nil && gid == groupGID
}

func verifyRevertMediaOverlay(ctx context.Context, js []managedJournal, spec protocol.VMCreateSpec, expected, actual managedDisk) error {
	if expected.Device != "cdrom" || expected.Readonly == nil || actual.Readonly == nil || expected.Target != actual.Target ||
		expected.Alias == nil || actual.Alias == nil || expected.Alias.Name != actual.Alias.Name {
		return errors.New("reverted read-only media identity changed")
	}
	mediaID := ""
	switch expected.Alias.Name {
	case cdromInstallAlias:
		mediaID = spec.MediaID
	case cdromDriversAlias:
		mediaID = spec.DriversMediaID
	default:
		return errors.New("reverted read-only media is not a managed CDROM")
	}
	if mediaID == "" {
		if expected.Source == nil {
			if actual.Source != nil {
				return errors.New("reverted empty media tray gained a source")
			}
			return nil
		}
		return errors.New("captured read-only media has no owning media identity")
	}
	media, iso, err := publishedMediaPath(js, mediaID)
	if err != nil || expected.Source == nil || expected.Source.File != iso || verifyISO(iso, media) != nil {
		return errors.New("captured read-only media source cannot be verified")
	}
	if actual.Source == nil {
		return errors.New("reverted read-only media source is missing")
	}
	if actual.Source.File == iso {
		return nil
	}
	path := actual.Source.File
	base := strings.TrimSuffix(filepath.Base(iso), filepath.Ext(iso)) + "."
	if filepath.Dir(path) != filepath.Dir(iso) || !strings.HasPrefix(filepath.Base(path), base) ||
		!nativeSnapshotUUID(strings.TrimPrefix(filepath.Base(path), base)) ||
		actual.Driver.Type != "qcow2" || expected.Driver.Type != "raw" {
		return errors.New("reverted read-only media overlay path or format is not libvirt-generated")
	}
	info, err := os.Lstat(path)
	stat, ok := (*syscall.Stat_t)(nil), false
	if err == nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Nlink != 1 ||
		!libvirtGeneratedMediaOverlayOwner(int(stat.Uid), int(stat.Gid)) {
		return errors.New("reverted read-only media overlay ownership is unsafe")
	}
	b, err := run(ctx, "qemu-img", "info", "--force-share", "--backing-chain", "--output=json", path)
	if err != nil {
		return err
	}
	var images []map[string]any
	if json.Unmarshal(b, &images) != nil || len(images) != 2 || unsupportedImageMetadata(images, true) {
		return errors.New("reverted read-only media overlay metadata is unsupported")
	}
	layers, err := parseImageChain(path, b)
	if err != nil || len(layers) != 2 || layers[0].Path != path || layers[0].Format != "qcow2" || layers[1].Path != iso || layers[1].Format != "raw" {
		return errors.New("reverted read-only media overlay does not back the captured ISO")
	}
	if _, err = run(ctx, "qemu-img", "check", path); err != nil {
		return errors.New("reverted read-only media overlay failed qemu-img check")
	}
	return nil
}

func (e *ManagedEngine) verifyRevertedNativeSnapshot(ctx context.Context, js []managedJournal, j managedJournal) (string, error) {
	record := j.SnapshotRevert
	if record == nil {
		return "", errors.New("snapshot revert record is unavailable")
	}
	observed, currentXML, err := e.observeSnapshotRevert(ctx, js, j)
	if err != nil {
		return "", err
	}
	currentDigest, err := snapshotDefinitionDigest(currentXML)
	if err != nil || currentDigest != observed.DefinitionSHA256 {
		return "", errors.New("reverted VM definition projection is inconsistent")
	}
	snapshot, raw, err := readManagedNativeSnapshotXML(ctx, j.Command.VM.ID, managedSnapshotNativeName(record.TargetSnapshotID))
	if err != nil || snapshot.Description != j.Command.Snapshot.Target.Description || snapshot.Name != managedSnapshotNativeName(record.TargetSnapshotID) {
		return "", errors.New("reverted native snapshot identity changed")
	}
	if record.TargetNativeIdentity != nil {
		if snapshot.Description != record.TargetNativeIdentity.Description || snapshot.CreationTime != record.TargetNativeIdentity.CreationTime ||
			snapshot.Parent.Name != record.TargetNativeIdentity.ParentName {
			return "", errors.New("revert changed immutable native snapshot metadata")
		}
	} else {
		seconds, secondsErr := strconv.ParseInt(snapshot.CreationTime, 10, 64)
		if secondsErr != nil || snapshot.Parent.Name != "" || snapshot.Description != j.Command.Snapshot.Target.Description ||
			absDuration(time.Unix(seconds, 0).Sub(j.Command.Snapshot.Target.CreatedAt)) > 2*time.Minute {
			return "", errors.New("revert target metadata cannot be bounded to its captured point")
		}
	}
	var saved managedDomain
	savedXML := []byte("<domain type=\"" + snapshot.Domain.Type + "\">" + snapshot.Domain.Inner + "</domain>")
	if snapshot.Domain.Type == "" || xml.Unmarshal(savedXML, &saved) != nil || saved.UUID != j.Command.VM.ID ||
		validateManagedDefinition(savedXML, record.TargetDomain) != nil {
		return "", errors.New("reverted snapshot saved definition differs from the captured target")
	}
	savedDigest, err := snapshotDefinitionDigest(savedXML)
	targetDigest, targetErr := snapshotDefinitionDigest(record.TargetDomain)
	if err != nil || targetErr != nil || savedDigest != targetDigest {
		return "", errors.New("reverted snapshot saved configuration changed")
	}
	var current managedDomain
	if xml.Unmarshal(currentXML, &current) != nil {
		return "", errors.New("reverted current definition is invalid")
	}
	writable := make(map[string]string, len(observed.Disks))
	for _, disk := range observed.Disks {
		writable[disk.Target] = disk.Source
	}
	currentByTarget := make(map[string]managedDisk, len(current.Devices.Disks))
	for _, disk := range current.Devices.Disks {
		currentByTarget[disk.Target.Dev] = disk
	}
	savedByTarget := make(map[string]managedDisk, len(saved.Devices.Disks))
	for _, disk := range saved.Devices.Disks {
		savedByTarget[disk.Target.Dev] = disk
	}
	metadata := make(map[string]struct {
		snapshot string
		source   string
		format   string
	}, len(snapshot.Disks))
	for _, disk := range snapshot.Disks {
		if _, exists := metadata[disk.Name]; exists {
			return "", errors.New("reverted native snapshot repeats a disk target")
		}
		metadata[disk.Name] = struct {
			snapshot string
			source   string
			format   string
		}{disk.Snapshot, disk.Source.File, disk.Driver.Type}
	}
	revertByTarget := make(map[string]struct {
		snapshot string
		source   string
		format   string
	}, len(snapshot.RevertDisks))
	for _, disk := range snapshot.RevertDisks {
		if _, exists := revertByTarget[disk.Name]; exists {
			return "", errors.New("reverted native snapshot repeats a revert-disk target")
		}
		revertByTarget[disk.Name] = struct {
			snapshot string
			source   string
			format   string
		}{disk.Snapshot, disk.Source.File, disk.Driver.Type}
	}
	if len(metadata) != len(savedByTarget) {
		return "", errors.New("reverted native snapshot disk set differs from the captured VM")
	}
	for target, want := range savedByTarget {
		got, exists := metadata[target]
		live, liveExists := currentByTarget[target]
		if !exists || !liveExists {
			return "", errors.New("reverted native snapshot omits a captured disk")
		}
		if want.Readonly == nil && want.Device != "cdrom" {
			expectedSource, expectedFormat := got.source, got.format
			if revert, ok := revertByTarget[target]; ok {
				// Libvirt records the post-revert writable tip under <revertDisks>; the
				// <disks> source stays the capture-time overlay name.
				if revert.snapshot != "external" || revert.format != "qcow2" || revert.source == "" {
					return "", errors.New("reverted native snapshot writable disk projection changed")
				}
				expectedSource, expectedFormat = revert.source, revert.format
			}
			if live.Source == nil || got.snapshot != "external" || expectedSource != live.Source.File || expectedFormat != "qcow2" || writable[target] != live.Source.File {
				return "", errors.New("reverted native snapshot writable disk projection changed")
			}
			if _, ok := revertByTarget[target]; ok {
				if err := verifyLibvirtRevertDiskOverlay(ctx, want, live.Source.File); err != nil {
					return "", err
				}
			}
			continue
		}
		if got.snapshot != "no" && got.snapshot != "external" {
			return "", errors.New("reverted native snapshot read-only disk mode changed")
		}
		if got.snapshot == "external" {
			if live.Source == nil || got.source != live.Source.File || verifyRevertMediaOverlay(ctx, js, record.CapturedSpec, want, live) != nil {
				return "", errors.New("reverted native snapshot has an unverified read-only media overlay")
			}
		}
	}
	if len(snapshot.RevertDisks) != 0 {
		for _, disk := range snapshot.RevertDisks {
			if disk.Snapshot != "external" {
				continue
			}
			want, ok := savedByTarget[disk.Name]
			if !ok || want.Readonly != nil || want.Device == "cdrom" || disk.Source.File == "" {
				return "", errors.New("reverted native snapshot has an unowned revert-disk reference")
			}
			live, liveExists := currentByTarget[disk.Name]
			if !liveExists || live.Source == nil || live.Source.File != disk.Source.File {
				return "", errors.New("reverted native snapshot revert-disk tip is not current")
			}
		}
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func verifyLibvirtRevertDiskOverlay(ctx context.Context, captured managedDisk, livePath string) error {
	if captured.Source == nil || captured.Source.File == "" || livePath == "" {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	base := captured.Source.File
	if filepath.Dir(livePath) != filepath.Dir(base) {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	stem := strings.TrimSuffix(filepath.Base(base), filepath.Ext(base)) + "."
	if !strings.HasPrefix(filepath.Base(livePath), stem) || !nativeSnapshotUUID(strings.TrimPrefix(filepath.Base(livePath), stem)) {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	info, err := os.Lstat(livePath)
	stat, ok := (*syscall.Stat_t)(nil), false
	if err == nil {
		stat, ok = info.Sys().(*syscall.Stat_t)
	}
	if err != nil || !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Nlink != 1 ||
		!libvirtGeneratedMediaOverlayOwner(int(stat.Uid), int(stat.Gid)) {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	b, err := run(ctx, "qemu-img", "info", "--force-share", "--backing-chain", "--output=json", livePath)
	if err != nil {
		return err
	}
	layers, err := parseImageChain(livePath, b)
	if err != nil || len(layers) < 2 || layers[0].Path != livePath || layers[0].Format != "qcow2" || layers[1].Path != base {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	if _, err = run(ctx, "qemu-img", "check", livePath); err != nil {
		return errors.New("reverted native snapshot writable disk projection changed")
	}
	return nil
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func (e *ManagedEngine) RevertStoppedSnapshot(
	ctx context.Context,
	command protocol.Command,
	reserve func(context.Context, protocol.Command, string, protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error),
	settle func(context.Context, protocol.PoolVMRecoveryEvidence) error,
) (protocol.Result, error) {
	result := protocol.Result{OperationID: command.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := validateManagedSnapshotRevertRequest(command); err != nil || reserve == nil || settle == nil || e == nil || e.stateDir == "" {
		return result, errors.New("invalid stopped snapshot revert operation")
	}
	return e.revertStoppedSnapshot(ctx, command, reserve, settle, result)
}

func (e *ManagedEngine) snapshotRevertArtifactPaths(record managedSnapshotRevertRecord) error {
	for _, artifact := range record.Firmware {
		for _, path := range []string{artifact.TemporaryPath, artifact.PreviousPath} {
			if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
				if err == nil {
					return errors.New("snapshot revert firmware staging path already exists")
				}
				return err
			}
		}
	}
	return nil
}

func (e *ManagedEngine) observeSnapshotRevert(ctx context.Context, js []managedJournal, j managedJournal) (managedSnapshotObservation, []byte, error) {
	record := j.SnapshotRevert
	if record == nil {
		return managedSnapshotObservation{}, nil, errors.New("snapshot revert intent is unavailable")
	}
	state, err := managedSnapshotProbe(ctx, "domstate", j.Command.VM.ID)
	if err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	if strings.TrimSpace(string(state)) != "shut off" {
		return managedSnapshotObservation{}, nil, errors.New("VM must remain stopped during snapshot revert")
	}
	info, err := managedSnapshotProbe(ctx, "dominfo", j.Command.VM.ID)
	if err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	if !autostartDisabled(info) {
		return managedSnapshotObservation{}, nil, errors.New("VM native autostart must remain disabled during snapshot revert")
	}
	raw, err := managedSnapshotProbe(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	var actual, expected managedDomain
	if xml.Unmarshal(raw, &actual) != nil || xml.Unmarshal(record.TargetDomain, &expected) != nil || actual.UUID != j.Command.VM.ID || expected.UUID != j.Command.VM.ID {
		return managedSnapshotObservation{}, nil, errors.New("snapshot revert VM definition is invalid")
	}
	expected.Name = record.ResultSpec.Name
	if err := verifyManagedDiskHeads(actual, managedSnapshotTargets(record.Target), managedSnapshotSources(actual, record.Target)); err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	sources := managedSnapshotSources(actual, record.Target)
	if len(sources) != len(record.Target.Disks) {
		return managedSnapshotObservation{}, nil, errors.New("reverted VM writable disk set differs from the retained point")
	}
	actualByTarget := make(map[string]managedDisk, len(actual.Devices.Disks))
	for _, disk := range actual.Devices.Disks {
		actualByTarget[disk.Target.Dev] = disk
	}
	for i := range expected.Devices.Disks {
		disk := &expected.Devices.Disks[i]
		if disk.Readonly != nil || disk.Device == "cdrom" {
			live, ok := actualByTarget[disk.Target.Dev]
			if !ok || verifyRevertMediaOverlay(ctx, js, record.CapturedSpec, *disk, live) != nil {
				return managedSnapshotObservation{}, nil, errors.New("reverted read-only media differs from the captured point")
			}
			if live.Source != nil {
				if disk.Source == nil {
					disk.Source = &sourceXML{}
				}
				disk.Source.File = live.Source.File
			} else {
				disk.Source = nil
			}
			disk.Driver.Type = live.Driver.Type
			continue
		}
		index := snapshotTargetIndex(record.Target, disk.Target.Dev)
		if index < 0 || disk.Source == nil {
			return managedSnapshotObservation{}, nil, errors.New("captured VM definition has an unmanaged writable disk")
		}
		disk.Source.File = sources[index]
	}
	expectedXML, err := xml.MarshalIndent(expected, "", "  ")
	if err != nil || validateManagedDefinition(raw, expectedXML) != nil {
		return managedSnapshotObservation{}, nil, errors.New("reverted VM definition differs from the captured point")
	}
	definitionDigest, err := snapshotDefinitionDigest(raw)
	if err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	observation := managedSnapshotObservation{DomainState: "shut off", DefinitionSHA256: definitionDigest}
	synthetic := j
	vm := record.ResultSpec
	synthetic.Command.VM = &vm
	synthetic.Command.Kind = "vm-update"
	synthetic.Targets = managedSnapshotTargets(record.Target)
	synthetic.Disks = sources
	synthetic.DiskChains = make([][]string, len(sources))
	for i, source := range sources {
		chain, chainErr := observeManagedDiskChain(ctx, source)
		if chainErr != nil || len(chain) != len(record.Target.Disks[i].Layers)+1 || !slices.Equal(chain[1:], record.Target.Disks[i].Layers) {
			return managedSnapshotObservation{}, nil, errors.New("reverted disk chain does not preserve the selected retained point")
		}
		synthetic.DiskChains[i] = chain
	}
	if _, err := verifyManagedDiskChains(ctx, js, synthetic); err != nil {
		return managedSnapshotObservation{}, nil, err
	}
	for i, target := range record.Target.Disks {
		storage, access, err := managedStorageByID(ctx, js, target.StorageID, 0)
		if err != nil {
			return managedSnapshotObservation{}, nil, errors.New("snapshot revert storage cannot be verified")
		}
		identity, err := snapshotStorageIdentityDigest(storage, access)
		if err != nil || identity != target.StorageIdentitySHA256 || effectiveStorageGeneration(storage) != target.StorageGeneration {
			return managedSnapshotObservation{}, nil, errors.New("snapshot revert storage identity or generation changed")
		}
		observation.Disks = append(observation.Disks, managedSnapshotDiskObservation{
			Target: target.Target, StorageID: target.StorageID, StorageGeneration: target.StorageGeneration,
			StorageIdentitySHA256: target.StorageIdentitySHA256, Source: sources[i], Layers: synthetic.DiskChains[i],
		})
	}
	return observation, raw, nil
}

func managedSnapshotTargets(observation managedSnapshotObservation) []string {
	targets := make([]string, len(observation.Disks))
	for i, disk := range observation.Disks {
		targets[i] = disk.Target
	}
	return targets
}

func managedSnapshotSources(domain managedDomain, observation managedSnapshotObservation) []string {
	sources := make([]string, 0, len(observation.Disks))
	for _, target := range managedSnapshotTargets(observation) {
		for _, disk := range domain.Devices.Disks {
			if disk.Target.Dev == target && disk.Readonly == nil && disk.Device != "cdrom" && disk.Source != nil {
				sources = append(sources, disk.Source.File)
				break
			}
		}
	}
	return sources
}

func snapshotTargetIndex(observation managedSnapshotObservation, target string) int {
	for i, disk := range observation.Disks {
		if disk.Target == target {
			return i
		}
	}
	return -1
}

func (e *ManagedEngine) verifySnapshotRevertBefore(ctx context.Context, js []managedJournal, j managedJournal) error {
	record := j.SnapshotRevert
	if record == nil || j.Command.VM == nil {
		return errors.New("snapshot revert before-state is unavailable")
	}
	current, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil || current.Command.VM == nil || !reflect.DeepEqual(current.Command.VM, j.Command.VM) {
		return errors.New("current VM definition changed after snapshot revert reservation")
	}
	observed, _, raw, err := e.observeManagedSnapshot(ctx, js, current, "")
	match, digestErr := snapshotObservationDigestsMatch(observed, record.Before)
	if err != nil || digestErr != nil || !match {
		return errors.New("current VM disk state changed after snapshot revert reservation")
	}
	if err := validateManagedDefinition(raw, record.BeforeDomain); err != nil {
		return errors.New("current VM definition changed after snapshot revert reservation")
	}
	name, err := snapshotRevertCurrentName(ctx, j.Command.VM.ID)
	if err != nil || name != record.CurrentSnapshotBefore {
		return errors.New("current native snapshot changed after snapshot revert reservation")
	}
	if err := e.verifySnapshotRevertTargetJournal(ctx, j.Command, *record); err != nil {
		return err
	}
	return e.verifySnapshotRevertFirmware(ctx, *record, false)
}

func (e *ManagedEngine) stageSnapshotRevertFirmware(ctx context.Context, record *managedSnapshotRevertRecord) error {
	for i := range record.Firmware {
		artifact := &record.Firmware[i]
		manifest, err := managedSnapshotManifest(ctx, artifact.TemporaryPath)
		if err == nil && manifest.Exists {
			if !snapshotManifestsEqual(manifest, artifact.TargetManifest) {
				return errors.New("snapshot revert firmware staging file conflicts with the retained point")
			}
			continue
		}
		if err != nil {
			return err
		}
		copied, err := copyManagedSnapshotFirmware(ctx, artifact.TargetPath, artifact.TemporaryPath)
		if err != nil || !snapshotManifestsEqual(copied, artifact.TargetManifest) {
			return errors.New("could not stage the retained firmware state")
		}
	}
	return nil
}

func (e *ManagedEngine) applySnapshotRevertFirmware(ctx context.Context, record *managedSnapshotRevertRecord) error {
	for i := range record.Firmware {
		artifact := &record.Firmware[i]
		source, sourceErr := managedSnapshotManifest(ctx, artifact.SourcePath)
		previous, previousErr := managedSnapshotManifest(ctx, artifact.PreviousPath)
		temporary, temporaryErr := managedSnapshotManifest(ctx, artifact.TemporaryPath)
		if sourceErr != nil || previousErr != nil || temporaryErr != nil {
			return errors.Join(sourceErr, previousErr, temporaryErr)
		}
		if source.Exists && snapshotManifestsEqual(source, artifact.TargetManifest) {
			if previous.Exists && !snapshotManifestsEqual(previous, artifact.BeforeManifest) {
				return errors.New("snapshot revert firmware predecessor cannot be verified")
			}
			continue
		}
		if source.Exists && snapshotManifestsEqual(source, artifact.BeforeManifest) && !previous.Exists {
			if err := os.Rename(artifact.SourcePath, artifact.PreviousPath); err != nil {
				return err
			}
			if err := syncManagedDir(filepath.Dir(artifact.SourcePath)); err != nil {
				return err
			}
			previous, previousErr = managedSnapshotManifest(ctx, artifact.PreviousPath)
			source, sourceErr = managedSnapshotManifest(ctx, artifact.SourcePath)
		}
		if sourceErr != nil || source.Exists || previousErr != nil || !previous.Exists || !snapshotManifestsEqual(previous, artifact.BeforeManifest) ||
			temporaryErr != nil || !temporary.Exists || !snapshotManifestsEqual(temporary, artifact.TargetManifest) {
			return errors.New("snapshot revert firmware state is mixed or ambiguous")
		}
		if err := os.Rename(artifact.TemporaryPath, artifact.SourcePath); err != nil {
			return err
		}
		if err := syncManagedDir(filepath.Dir(artifact.SourcePath)); err != nil {
			return err
		}
		restored, restoredErr := managedSnapshotManifest(ctx, artifact.SourcePath)
		if restoredErr != nil || !snapshotManifestsEqual(restored, artifact.TargetManifest) {
			return errors.New("restored firmware state differs from the selected point")
		}
	}
	return nil
}

func (e *ManagedEngine) revertStoppedSnapshot(
	ctx context.Context,
	command protocol.Command,
	reserve func(context.Context, protocol.Command, string, protocol.VMCreateSpec) (protocol.PoolVMRecoveryReservation, error),
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
		return result, errors.New("snapshot revert operation is already running")
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
			result.Error = "snapshot revert request does not match the current VM definition"
			return result, nil
		}
		now := time.Now().UTC()
		j = managedJournal{Command: command, Result: protocol.Result{OperationID: command.OperationID, Status: "pending", ObservedAt: now},
			Stage: "snapshot-revert-preflight", RecoveryStage: "preflight", SnapshotRevert: &managedSnapshotRevertRecord{TargetSnapshotID: command.Snapshot.SnapshotID, CreatedAt: now}}
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
				err = errors.New("snapshot revert current VM definition changed")
			} else {
				targetJournal, targetErr := e.snapshotRevertSource(js, *command.Snapshot.Target)
				if targetErr != nil {
					err = targetErr
				} else {
					record, intentErr := e.snapshotRevertIntent(ctx, command, js, current, targetJournal)
					err = intentErr
					if err == nil {
						j.SnapshotRevert = &record
						j.Domain = append([]byte(nil), current.Domain...)
						j.Disks, j.DiskChains = append([]string(nil), current.Disks...), cloneDiskChains(current.DiskChains)
						j.Targets, j.MediaTarget, j.DriversTarget = append([]string(nil), current.Targets...), current.MediaTarget, current.DriversTarget
					}
				}
			}
		}
		if err != nil {
			return e.failSnapshotPreflight(ctx, result, command.OperationID, err)
		}
		j.RecoveryStage, j.Stage = "local-intent", "snapshot-revert-local-intent"
		if err = e.saveSnapshotUnderLock(ctx, j, false); err != nil {
			return result, err
		}
	}
	if j.SnapshotRevert == nil || validateManagedSnapshotRevertRecord(command, *j.SnapshotRevert) != nil {
		return e.snapshotAttention(ctx, j, errors.New("snapshot revert journal intent is invalid"))
	}
	js, err = e.journals()
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if _, err := e.snapshotRevertSource(js, *command.Snapshot.Target); err != nil {
		return e.snapshotAttention(ctx, j, errors.New("selected snapshot capture journal changed"))
	}
	if j.RecoveryStage == "native" && j.SnapshotRevert.ObservedTargetNativeXMLSHA256 == "" {
		if err := e.verifySnapshotRevertTargetJournalBytes(command, *j.SnapshotRevert); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
	} else if err := e.verifySnapshotRevertTargetJournal(ctx, command, *j.SnapshotRevert); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if j.RecoveryStage == "completed" || j.RecoveryStage == "failed-before-effects" {
		if j.RecoveryStage == "completed" {
			if err := e.verifySnapshotRevertCompleted(ctx, j); err != nil {
				return e.snapshotAttention(ctx, j, err)
			}
		}
		return e.settleSnapshotRevert(ctx, j, settle)
	}

	intent, err := managedSnapshotRevertIntentDigest(command, *j.SnapshotRevert)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if j.RecoveryReservation == nil {
		captured := j.SnapshotRevert.CapturedSpec
		reservation, reserveErr := reserve(ctx, command, intent, captured)
		if reserveErr != nil {
			return e.abortSnapshotLocalIntent(ctx, j, reserveErr)
		}
		// Master reservation succeeded: never abort/delete the journal.
		if err := validateManagedSnapshotRevertReservation(command, *j.SnapshotRevert, reservation); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		j.RecoveryReservation, j.RecoveryStage, j.Stage = &reservation, "reserved", "snapshot-revert-reserved"
		j.Result.Status, j.Result.Error = "pending", ""
		if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
	} else if err := validateManagedSnapshotRevertReservation(command, *j.SnapshotRevert, *j.RecoveryReservation); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	if err = e.stageSnapshotRevertFirmware(ctx, j.SnapshotRevert); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	var nativeCommandErr error
	targetNative := managedSnapshotNativeName(command.Snapshot.SnapshotID)
	if j.RecoveryStage == "reserved" {
		js, err = e.journals()
		if err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		if err = e.verifySnapshotRevertBefore(ctx, js, j); err != nil {
			return e.snapshotAttention(ctx, j, err)
		}
		j.RecoveryStage, j.Stage = "native", "snapshot-revert-native"
		j.Result.Status, j.Result.Error = "pending", ""
		if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
			return result, err
		}
		nativeCommandErr = runNativeSnapshotRevert(ctx, command)
	} else if j.RecoveryStage == "native" && j.SnapshotRevert.ObservedTargetNativeXMLSHA256 == "" {
		// Effect missing: current is not yet the target. Re-run snapshot-revert once.
		// Already on target with empty observation is the BUG-0647 resume path — skip.
		currentName, nameErr := snapshotRevertCurrentName(ctx, command.VM.ID)
		if nameErr != nil {
			return e.snapshotAttention(ctx, j, nameErr)
		}
		if currentName != targetNative {
			nativeCommandErr = runNativeSnapshotRevert(ctx, command)
		}
	}
	js, err = e.journals()
	if err != nil {
		return e.snapshotAttention(ctx, j, errors.Join(nativeCommandErr, err))
	}
	currentName, nameErr := snapshotRevertCurrentName(ctx, command.VM.ID)
	nativeDigest, nativeErr := e.verifyRevertedNativeSnapshot(ctx, js, j)
	if nameErr != nil || nativeErr != nil || currentName != targetNative {
		cause := errors.Join(nativeCommandErr, nameErr, nativeErr, errors.New("native snapshot revert outcome is not the exact selected point"))
		if currentName != targetNative && j.SnapshotRevert.ObservedTargetNativeXMLSHA256 == "" {
			if failed, failErr := e.failSnapshotRevertBeforeEffects(ctx, j, settle, cause); failErr == nil {
				return failed, nil
			} else if !errors.Is(failErr, errSnapshotRevertEffectsPresent) {
				return failed, failErr
			}
		}
		return e.snapshotAttention(ctx, j, cause)
	}
	j.SnapshotRevert.ObservedTargetNativeXMLSHA256 = nativeDigest
	if err = e.applySnapshotRevertFirmware(ctx, j.SnapshotRevert); err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	observation, raw, err := e.observeSnapshotRevert(ctx, js, j)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	j.SnapshotRevert.ObservedAfter, j.SnapshotRevert.ObservedDomain = &observation, append([]byte(nil), raw...)
	j.Disks, j.DiskChains = snapshotObservationProjection(observation)
	j.Domain = append([]byte(nil), raw...)
	targets, mediaTarget, driversTarget := managedTargets(j.SnapshotRevert.ResultSpec)
	j.CurrentProjection = &managedSnapshotCurrentProjection{
		Spec: j.SnapshotRevert.ResultSpec, Disks: append([]string(nil), j.Disks...),
		DiskChains: cloneDiskChains(j.DiskChains), Domain: append([]byte(nil), raw...),
		Targets: targets, MediaTarget: mediaTarget, DriversTarget: driversTarget,
	}
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "completed", "completed", "snapshot-revert-completed"
	j.Result.Status, j.Result.Error = "pending", ""
	if err = e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return result, err
	}
	return e.settleSnapshotRevert(ctx, j, settle)
}

func (e *ManagedEngine) verifySnapshotRevertCompleted(ctx context.Context, j managedJournal) error {
	if j.SnapshotRevert == nil || j.SnapshotRevert.ObservedAfter == nil || j.SnapshotRevert.ObservedTargetNativeXMLSHA256 == "" {
		return errors.New("completed snapshot revert lacks native observation")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	if err := e.verifySnapshotRevertTargetJournal(ctx, j.Command, *j.SnapshotRevert); err != nil {
		return err
	}
	observed, raw, err := e.observeSnapshotRevert(ctx, js, j)
	match, digestErr := snapshotObservationDigestsMatch(observed, *j.SnapshotRevert.ObservedAfter)
	if err != nil || digestErr != nil || !match || !reflect.DeepEqual(raw, j.SnapshotRevert.ObservedDomain) {
		return errors.New("completed snapshot revert no longer matches its observed VM state")
	}
	name, err := snapshotRevertCurrentName(ctx, j.Command.VM.ID)
	if err != nil || name != managedSnapshotNativeName(j.Command.Snapshot.SnapshotID) {
		return errors.New("selected native snapshot is no longer current")
	}
	digest, err := e.verifyRevertedNativeSnapshot(ctx, js, j)
	if err != nil || digest != j.SnapshotRevert.ObservedTargetNativeXMLSHA256 {
		return errors.New("selected snapshot metadata changed after revert")
	}
	return e.verifySnapshotRevertFirmware(ctx, *j.SnapshotRevert, true)
}

func (e *ManagedEngine) verifySnapshotRevertTargetJournalBytes(command protocol.Command, record managedSnapshotRevertRecord) error {
	raw, err := readSmall(filepath.Join(e.stateDir, record.TargetSnapshotID+".json"), managedSnapshotJournalLimit)
	if err != nil {
		return errors.New("selected snapshot capture journal is unavailable")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != record.TargetJournalSHA256 {
		return errors.New("selected snapshot capture journal changed after revert intent")
	}
	if _, err := e.snapshotRevertSourceMust(command, record); err != nil {
		return err
	}
	return nil
}

func (e *ManagedEngine) verifySnapshotRevertTargetJournal(ctx context.Context, command protocol.Command, record managedSnapshotRevertRecord) error {
	if err := e.verifySnapshotRevertTargetJournalBytes(command, record); err != nil {
		return err
	}
	target, err := e.snapshotRevertSourceMust(command, record)
	if err != nil {
		return err
	}
	if record.ObservedTargetNativeXMLSHA256 != "" {
		live, err := snapshotRevertNativeXMLDigest(ctx, command.VM.ID, managedSnapshotNativeName(record.TargetSnapshotID))
		if err != nil || live != record.ObservedTargetNativeXMLSHA256 {
			return errors.New("selected native snapshot metadata changed after revert")
		}
		if record.TargetNativeIdentity == nil {
			return nil
		}
		snapshot, _, err := readManagedNativeSnapshotXML(ctx, command.VM.ID, managedSnapshotNativeName(record.TargetSnapshotID))
		if err != nil || snapshot.Description != record.TargetNativeIdentity.Description ||
			snapshot.CreationTime != record.TargetNativeIdentity.CreationTime ||
			snapshot.Parent.Name != record.TargetNativeIdentity.ParentName {
			return errors.New("immutable native snapshot identity changed after revert")
		}
		return nil
	}
	nativeDigest, err := managedSnapshotXMLDigest(ctx, command.VM.ID, managedSnapshotNativeName(record.TargetSnapshotID), *target.Snapshot)
	if err != nil || nativeDigest != record.TargetNativeXMLSHA256 {
		return errors.New("selected native snapshot metadata changed after revert intent")
	}
	return nil
}

func (e *ManagedEngine) snapshotRevertSourceMust(command protocol.Command, record managedSnapshotRevertRecord) (managedJournal, error) {
	js, err := e.journals()
	if err != nil {
		return managedJournal{}, err
	}
	return e.snapshotRevertSource(js, *command.Snapshot.Target)
}

func (e *ManagedEngine) verifySnapshotRevertFirmware(ctx context.Context, record managedSnapshotRevertRecord, target bool) error {
	for _, artifact := range record.Firmware {
		manifest, err := managedSnapshotManifest(ctx, artifact.SourcePath)
		want := artifact.BeforeManifest
		if target {
			want = artifact.TargetManifest
		}
		if err != nil || !snapshotManifestsEqual(manifest, want) {
			return errors.New("paired snapshot firmware does not match the recorded state")
		}
	}
	return nil
}

func (e *ManagedEngine) settleSnapshotRevert(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error) (protocol.Result, error) {
	if j.RecoveryReservation == nil || j.RecoveryStage != "completed" && j.RecoveryStage != "failed-before-effects" {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot revert has no terminal master reservation"}, nil
	}
	evidence, err := e.PoolVMRecoveryEvidence(ctx, *j.RecoveryReservation)
	if err != nil {
		return e.snapshotAttention(ctx, j, err)
	}
	raw, err := readSmall(filepath.Join(e.stateDir, j.Command.OperationID+".json"), managedSnapshotJournalLimit)
	digest := sha256.Sum256(raw)
	if err != nil || hex.EncodeToString(digest[:]) != evidence.JournalSHA256 {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot revert receipt differs from its immutable journal"}, nil
	}
	if err = settle(ctx, evidence); err != nil {
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "master snapshot revert settlement is not acknowledged"}, nil
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
		return protocol.Result{OperationID: j.Command.OperationID, Status: "needs_attention", Error: "snapshot revert journal changed before settlement acknowledgement"}, nil
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

var errSnapshotRevertEffectsPresent = errors.New("snapshot revert left native effects")

func (e *ManagedEngine) failSnapshotRevertBeforeEffects(ctx context.Context, j managedJournal, settle func(context.Context, protocol.PoolVMRecoveryEvidence) error, cause error) (protocol.Result, error) {
	if j.SnapshotRevert == nil || j.SnapshotRevert.ObservedTargetNativeXMLSHA256 != "" || j.SnapshotRevert.ObservedAfter != nil ||
		len(j.SnapshotRevert.ObservedDomain) != 0 || j.CurrentProjection != nil {
		return protocol.Result{}, errSnapshotRevertEffectsPresent
	}
	js, err := e.journals()
	if err != nil {
		return protocol.Result{}, err
	}
	current, err := managedCurrent(js, j.Command.VM.ID)
	if err != nil {
		return protocol.Result{}, err
	}
	observed, _, _, err := e.observeManagedSnapshot(ctx, js, current, "")
	if err != nil {
		return protocol.Result{}, err
	}
	match, digestErr := snapshotObservationDigestsMatch(observed, j.SnapshotRevert.Before)
	if digestErr != nil || !match {
		return protocol.Result{}, errSnapshotRevertEffectsPresent
	}
	j.SnapshotRevert.ObservedAfter = nil
	j.SnapshotRevert.ObservedDomain = nil
	j.SnapshotRevert.ObservedTargetNativeXMLSHA256 = ""
	j.CurrentProjection = nil
	j.RecoveryStage, j.RecoveryOutcome, j.Stage = "failed-before-effects", "failed-before-effects", "snapshot-revert-failed-before-effects"
	j.Result.Status, j.Result.Error = "pending", cause.Error()
	if err := e.saveSnapshotUnderLock(ctx, j, true); err != nil {
		return j.Result, err
	}
	return e.settleSnapshotRevert(ctx, j, settle)
}
