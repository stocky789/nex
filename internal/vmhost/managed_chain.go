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
	"slices"
	"sort"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

// QEMU 10's block graph parser has a bounded chain walk; keep host observations aligned with
// the 200-layer patched native limit and reject larger output before retaining it in state.
const maxManagedDiskChain = 200

type managedImageLayer struct {
	Path   string
	Format string
}

// observeManagedDiskChain returns the active qcow2 head followed by every backing image.
// qemu-img is the source of truth for the on-disk relationship; callers compare it with
// the journal before changing a managed VM or deleting any of its files.
func observeManagedDiskChain(ctx context.Context, head string) ([]string, error) {
	b, err := run(ctx, "qemu-img", "info", "--force-share", "--backing-chain", "--output=json", head)
	if err != nil {
		return nil, err
	}
	return parseManagedDiskChain(head, b)
}

func parseManagedDiskChain(head string, b []byte) ([]string, error) {
	layers, err := parseImageChain(head, b)
	if err != nil {
		return nil, err
	}
	paths := make([]string, len(layers))
	for i, layer := range layers {
		if layer.Format != "qcow2" {
			return nil, errors.New("managed VM backing chain contains an unsupported image format")
		}
		paths[i] = layer.Path
	}
	return paths, nil
}

func parseImageChain(head string, b []byte) ([]managedImageLayer, error) {
	var images []struct {
		Filename            string `json:"filename"`
		Format              string `json:"format"`
		BackingFilename     string `json:"backing-filename"`
		FullBackingFilename string `json:"full-backing-filename"`
	}
	if err := json.Unmarshal(b, &images); err != nil || len(images) == 0 || len(images) > maxManagedDiskChain {
		return nil, errors.New("qemu-img returned an invalid or oversized backing chain")
	}
	layers := make([]managedImageLayer, len(images))
	seen := make(map[string]bool, len(images))
	for i, image := range images {
		path, err := cleanImagePath(image.Filename)
		if err != nil || image.Format == "" || seen[path] {
			return nil, errors.New("qemu-img returned an invalid or repeated backing path")
		}
		layers[i] = managedImageLayer{Path: path, Format: image.Format}
		seen[path] = true
	}
	wantHead, err := cleanImagePath(head)
	if err != nil || layers[0].Path != wantHead {
		return nil, errors.New("qemu-img backing chain head changed")
	}
	for i, image := range images {
		backing := image.BackingFilename
		if i == len(images)-1 {
			if backing != "" || image.FullBackingFilename != "" {
				return nil, errors.New("qemu-img backing chain is truncated")
			}
			continue
		}
		if backing == "" {
			return nil, errors.New("qemu-img backing chain is truncated")
		}
		if image.FullBackingFilename != "" {
			backing = image.FullBackingFilename
		}
		if !filepath.IsAbs(backing) {
			backing = filepath.Join(filepath.Dir(layers[i].Path), backing)
		}
		backing, err = cleanImagePath(backing)
		if err != nil || backing != layers[i+1].Path {
			return nil, errors.New("qemu-img backing chain has inconsistent parent references")
		}
	}
	return layers, nil
}

func cleanImagePath(path string) (string, error) {
	if path == "" || !filepath.IsAbs(path) || strings.ContainsRune(path, '\x00') {
		return "", errors.New("invalid image path")
	}
	return filepath.Clean(path), nil
}

func managedDiskChains(j managedJournal) ([][]string, error) {
	if len(j.Disks) == 0 {
		return nil, nil
	}
	if len(j.DiskChains) == 0 { // A journal without recorded chains may own standalone disks only.
		chains := make([][]string, len(j.Disks))
		for i, path := range j.Disks {
			clean, err := cleanImagePath(path)
			if err != nil || clean != path {
				return nil, errors.New("VM disk journal contains an invalid path")
			}
			chains[i] = []string{path}
		}
		return chains, nil
	}
	if len(j.DiskChains) != len(j.Disks) {
		return nil, errors.New("VM backing-chain journal does not match its disk layout")
	}
	chains := make([][]string, len(j.DiskChains))
	for i, chain := range j.DiskChains {
		if len(chain) == 0 || len(chain) > maxManagedDiskChain || chain[0] != j.Disks[i] {
			return nil, errors.New("VM backing-chain journal does not match its disk head")
		}
		seen := make(map[string]bool, len(chain))
		for _, path := range chain {
			clean, err := cleanImagePath(path)
			if err != nil || clean != path || seen[path] {
				return nil, errors.New("VM backing-chain journal contains an invalid or repeated path")
			}
			seen[path] = true
		}
		chains[i] = append([]string(nil), chain...)
	}
	return chains, nil
}

func verifyManagedDiskHeads(domain managedDomain, targets, disks []string) error {
	if len(disks) == 0 || len(disks) != len(targets) {
		return errors.New("VM disk-chain evidence unavailable")
	}
	managedTargets := make(map[string]int, len(targets))
	for i, target := range targets {
		if target == "" || disks[i] == "" {
			return errors.New("VM disk-chain evidence unavailable")
		}
		if _, exists := managedTargets[target]; exists {
			return errors.New("VM disk journal contains duplicate targets")
		}
		managedTargets[target] = i
	}
	observedTargets := make(map[string]bool, len(managedTargets))
	observedSources := make(map[string]bool, len(managedTargets))
	for _, candidate := range domain.Devices.Disks {
		if candidate.Readonly != nil {
			continue
		}
		if candidate.Device != "disk" {
			return errors.New("VM has an unsupported writable disk device")
		}
		target := candidate.Target.Dev
		i, ok := managedTargets[target]
		if !ok || observedTargets[target] {
			return errors.New("VM has an unmanaged or duplicate writable disk target")
		}
		if candidate.Source == nil || candidate.Source.File == "" || candidate.Source.File != disks[i] {
			return errors.New("VM disk head differs from its managed journal")
		}
		if observedSources[candidate.Source.File] {
			return errors.New("VM has duplicate writable disk sources")
		}
		observedTargets[target] = true
		observedSources[candidate.Source.File] = true
	}
	if len(observedTargets) != len(managedTargets) {
		return errors.New("VM writable disk targets differ from its managed journal")
	}
	return nil
}

func verifyManagedDiskChains(ctx context.Context, js []managedJournal, j managedJournal) ([][]string, error) {
	if j.Command.VM == nil || len(j.Disks) != len(j.Targets) || len(j.Disks) != len(j.Command.VM.Disks) {
		return nil, errors.New("VM disk-chain evidence unavailable")
	}
	want, err := managedDiskChains(j)
	if err != nil {
		return nil, err
	}
	state, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil {
		return nil, errors.New("VM state cannot be established for disk-chain verification")
	}
	xmlBytes, err := managedVirsh(ctx, managedDiskXMLArgs(j.Command.VM.ID, strings.TrimSpace(string(state)))...)
	if err != nil {
		return nil, errors.New("cannot read VM disk heads for backing-chain verification")
	}
	var domain managedDomain
	if err := xml.Unmarshal(xmlBytes, &domain); err != nil {
		return nil, errors.New("invalid VM definition during backing-chain verification")
	}
	if err = verifyManagedDiskHeads(domain, j.Targets, j.Disks); err != nil {
		return nil, err
	}
	var allPaths []string
	for i, disk := range j.Disks {
		observed, err := observeManagedDiskChain(ctx, disk)
		if err != nil {
			return nil, err
		}
		if !slices.Equal(observed, want[i]) {
			return nil, errors.New("VM backing chain differs from its managed journal; inspect the VM on its host")
		}
		_, access, err := managedStorageByID(ctx, js, j.Command.VM.Disks[i].StorageID, 0)
		if err != nil {
			return nil, err
		}
		root := filepath.Join(access.Path, "nex-vms", j.Command.VM.ID) + string(os.PathSeparator)
		for _, path := range observed {
			if !strings.HasPrefix(path, root) {
				return nil, errors.New("VM backing image is outside its registered storage directory")
			}
			real, err := filepath.EvalSymlinks(path)
			info, statErr := os.Stat(path)
			var st *syscall.Stat_t
			ok := false
			if statErr == nil {
				st, ok = info.Sys().(*syscall.Stat_t)
			}
			if err != nil || real != path || statErr != nil || !info.Mode().IsRegular() || !ok || st.Nlink != 1 {
				return nil, errors.New("VM backing image is unavailable or has aliases")
			}
			if err := checkImageWithBacking(ctx, path, true); err != nil {
				return nil, err
			}
			allPaths = append(allPaths, path)
		}
	}
	if err := (&Engine{config: Config{VirshURI: "qemu:///system"}}).exclusiveDisks(ctx, journal{Operation: Operation{VMID: j.Command.VM.ID}}, allPaths); err != nil {
		return nil, err
	}
	return want, nil
}

func managedDiskXMLArgs(vmID, state string) []string {
	if state == "shut off" {
		return []string{"dumpxml", vmID, "--inactive"}
	}
	return []string{"dumpxml", vmID}
}

// PoolVMRecoveryEvidence returns a receipt only for an exact terminal reservation whose
// current full disk chains still match the managed journal. Partial and post-effect failures
// deliberately have no settlement evidence.
func (e *ManagedEngine) PoolVMRecoveryEvidence(ctx context.Context, reservation protocol.PoolVMRecoveryReservation) (protocol.PoolVMRecoveryEvidence, error) {
	if err := reservation.Valid(); err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	if reservation.Kind == "snapshot-revert" {
		return e.poolVMSnapshotRevertEvidence(ctx, reservation)
	}
	if reservation.Kind == "snapshot-delete" {
		return e.poolVMSnapshotDeleteEvidence(ctx, reservation)
	}
	path := reservation.OperationID + ".json"
	raw, err := readSmall(filepath.Join(e.stateDir, path), 1<<20)
	if err != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery journal is unavailable")
	}
	j, err := e.readJournal(path)
	if err != nil || j.RecoveryReservation == nil || *j.RecoveryReservation != reservation {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery reservation does not match the managed journal")
	}
	if j.Snapshot == nil || validateManagedSnapshotReservation(j.Command, *j.Snapshot, reservation) != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery intent differs from its master reservation")
	}
	if j.Command.VM == nil || !recoverySpecMatches(*j.Command.VM, reservation) {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery VM definition differs from its reservation")
	}
	if j.RecoverySettled || j.Command.Kind != "snapshot-create" || j.Snapshot == nil || j.Result.Status != "pending" {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery is settled or its native result is not awaiting master settlement")
	}
	var observation managedSnapshotObservation
	switch {
	case j.RecoveryStage == "completed" && j.RecoveryOutcome == "completed" && j.Snapshot.ObservedAfter != nil:
		before, beforeErr := snapshotObservationDigest(j.Snapshot.ExpectedAfter)
		after, afterErr := snapshotObservationDigest(*j.Snapshot.ObservedAfter)
		if beforeErr != nil || afterErr != nil || before != after || !lowerSnapshotHash(j.Snapshot.NativeXMLSHA256) {
			return protocol.PoolVMRecoveryEvidence{}, errors.New("completed snapshot observation differs from the reserved disk transition")
		}
		nativeDigest, err := managedSnapshotXMLDigest(ctx, j.Command.VM.ID, managedSnapshotNativeName(reservation.OperationID), *j.Snapshot)
		if err != nil || nativeDigest != j.Snapshot.NativeXMLSHA256 {
			return protocol.PoolVMRecoveryEvidence{}, errors.New("native snapshot metadata differs from its journal")
		}
		observation = *j.Snapshot.ObservedAfter
	case j.RecoveryStage == "failed-before-effects" && j.RecoveryOutcome == "failed-before-effects":
		absent, err := managedSnapshotAbsent(ctx, j.Command.VM.ID, managedSnapshotNativeName(reservation.OperationID))
		if err != nil || !absent || j.Snapshot.NativeXMLSHA256 != "" {
			return protocol.PoolVMRecoveryEvidence{}, errors.New("failed snapshot recovery may have left native metadata")
		}
		observation = j.Snapshot.Before
	default:
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery outcome is partial or unresolved")
	}
	js, err := e.journals()
	if err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	if err = e.verifySnapshotRecoveryObservation(ctx, js, j, observation); err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	latest, err := readSmall(filepath.Join(e.stateDir, path), 1<<20)
	if err != nil || !bytes.Equal(raw, latest) {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot recovery journal changed during evidence collection")
	}
	digest := sha256.Sum256(raw)
	evidence := protocol.PoolVMRecoveryEvidence{Reservation: reservation, Outcome: j.RecoveryOutcome, JournalSHA256: hex.EncodeToString(digest[:])}
	if j.RecoveryOutcome == "completed" {
		descriptor, err := managedSnapshotDescriptor(j.Command, reservation, *j.Snapshot)
		if err != nil {
			return protocol.PoolVMRecoveryEvidence{}, err
		}
		evidence.Snapshot = &descriptor
	}
	if err := evidence.Valid(); err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	return evidence, nil
}

func (e *ManagedEngine) poolVMSnapshotRevertEvidence(ctx context.Context, reservation protocol.PoolVMRecoveryReservation) (protocol.PoolVMRecoveryEvidence, error) {
	path := reservation.OperationID + ".json"
	raw, err := readSmall(filepath.Join(e.stateDir, path), managedSnapshotJournalLimit)
	if err != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot revert journal is unavailable")
	}
	j, err := e.readJournal(path)
	if err != nil || j.Command.Kind != "snapshot-revert" || j.RecoveryReservation == nil || *j.RecoveryReservation != reservation ||
		j.RecoverySettled || j.Result.Status != "pending" || j.SnapshotRevert == nil ||
		validateManagedSnapshotRevertReservation(j.Command, *j.SnapshotRevert, reservation) != nil {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot revert is not an exact unsettled terminal result")
	}
	switch {
	case j.RecoveryStage == "completed" && j.RecoveryOutcome == "completed":
		if err = e.verifySnapshotRevertCompleted(ctx, j); err != nil {
			return protocol.PoolVMRecoveryEvidence{}, err
		}
	case j.RecoveryStage == "failed-before-effects" && j.RecoveryOutcome == "failed-before-effects":
		if j.SnapshotRevert.ObservedTargetNativeXMLSHA256 != "" || j.SnapshotRevert.ObservedAfter != nil ||
			len(j.SnapshotRevert.ObservedDomain) != 0 || j.CurrentProjection != nil {
			return protocol.PoolVMRecoveryEvidence{}, errors.New("failed snapshot revert may have left native effects")
		}
	default:
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot revert outcome is partial or unresolved")
	}
	latest, err := readSmall(filepath.Join(e.stateDir, path), managedSnapshotJournalLimit)
	if err != nil || !bytes.Equal(raw, latest) {
		return protocol.PoolVMRecoveryEvidence{}, errors.New("snapshot revert journal changed during evidence collection")
	}
	digest := sha256.Sum256(raw)
	evidence := protocol.PoolVMRecoveryEvidence{
		Reservation: reservation, Outcome: j.RecoveryOutcome, JournalSHA256: hex.EncodeToString(digest[:]),
	}
	if j.RecoveryOutcome == "completed" {
		resultSpec := j.SnapshotRevert.ResultSpec
		evidence.ResultSpec = &resultSpec
	}
	if err = evidence.Valid(); err != nil {
		return protocol.PoolVMRecoveryEvidence{}, err
	}
	return evidence, nil
}

func (e *ManagedEngine) verifySnapshotRecoveryObservation(ctx context.Context, js []managedJournal, j managedJournal, observation managedSnapshotObservation) error {
	if validateManagedSnapshotObservation(observation) != nil || j.Command.VM == nil || len(observation.Disks) != len(j.Command.VM.Disks) {
		return errors.New("snapshot recovery disk observation is incomplete")
	}
	state, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil || strings.TrimSpace(string(state)) != observation.DomainState {
		return errors.New("snapshot recovery VM state differs from its journal")
	}
	info, err := managedVirsh(ctx, "dominfo", j.Command.VM.ID)
	if err != nil || !autostartDisabled(info) {
		return errors.New("snapshot recovery VM native autostart state is not confirmed disabled")
	}
	xmlBytes, err := managedVirsh(ctx, managedDiskXMLArgs(j.Command.VM.ID, observation.DomainState)...)
	if err != nil {
		return errors.New("snapshot recovery VM definition cannot be read")
	}
	definitionHash, err := snapshotDefinitionDigest(xmlBytes)
	if err != nil || definitionHash != observation.DefinitionSHA256 {
		return errors.New("snapshot recovery VM definition differs from its journal")
	}
	check := j
	check.Disks = make([]string, len(observation.Disks))
	check.Targets = make([]string, len(observation.Disks))
	check.DiskChains = make([][]string, len(observation.Disks))
	for i, disk := range observation.Disks {
		if disk.StorageID != j.Command.VM.Disks[i].StorageID {
			return errors.New("snapshot recovery disk order differs from the managed VM")
		}
		storage, access, err := managedStorageByID(ctx, js, disk.StorageID, 0)
		if err != nil {
			return errors.New("snapshot recovery storage cannot be verified")
		}
		identity, err := snapshotStorageIdentityDigest(storage, access)
		if err != nil || identity != disk.StorageIdentitySHA256 || effectiveStorageGeneration(storage) != disk.StorageGeneration {
			return errors.New("snapshot recovery storage identity or generation differs from its journal")
		}
		check.Disks[i], check.Targets[i] = disk.Source, disk.Target
		check.DiskChains[i] = append([]string(nil), disk.Layers...)
	}
	if _, err := e.diskChains(ctx, js, check); err != nil {
		return err
	}
	for _, firmware := range j.Snapshot.Firmware {
		source, err := managedSnapshotManifest(ctx, firmware.SourcePath)
		if err != nil || !snapshotManifestsEqual(source, firmware.SourceManifest) {
			return errors.New("snapshot recovery firmware source differs from its journal")
		}
		copy, err := managedSnapshotManifest(ctx, firmware.SnapshotPath)
		if j.RecoveryOutcome == "completed" {
			if err != nil || !snapshotManifestsEqual(copy, firmware.SnapshotManifest) || !snapshotManifestsEqual(copy, firmware.SourceManifest) {
				return errors.New("snapshot recovery firmware copy differs from its source")
			}
		} else if err != nil || copy.Exists {
			return errors.New("failed snapshot recovery left a firmware copy")
		}
	}
	return nil
}

// PoolVMRecoveryReservations lists only bindings already persisted in this Core's managed
// journal. The pool owner matches these to its own pending reservation before requesting proof.
func (e *ManagedEngine) PoolVMRecoveryReservations() ([]protocol.PoolVMRecoveryReservation, error) {
	info, err := os.Stat(e.stateDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, errors.New("managed VM state path is not a directory")
	}
	js, err := e.journals()
	if err != nil {
		return nil, err
	}
	reservations := make([]protocol.PoolVMRecoveryReservation, 0)
	for _, j := range js {
		if j.RecoveryReservation != nil && !j.RecoverySettled {
			reservations = append(reservations, *j.RecoveryReservation)
		}
	}
	sort.Slice(reservations, func(i, j int) bool { return reservations[i].OperationID < reservations[j].OperationID })
	return reservations, nil
}

func recoverySpecMatches(spec protocol.VMCreateSpec, reservation protocol.PoolVMRecoveryReservation) bool {
	if protocol.ValidateVMSpec(spec) != nil || spec.ID != reservation.VMID || spec.PoolID != reservation.PoolID || spec.HostID != reservation.OwnerHostID || spec.Revision != reservation.SpecRevision {
		return false
	}
	specBytes, err := json.Marshal(spec)
	if err != nil {
		return false
	}
	disksBytes, err := json.Marshal(spec.Disks)
	if err != nil {
		return false
	}
	specHash, disksHash := sha256.Sum256(specBytes), sha256.Sum256(disksBytes)
	return hex.EncodeToString(specHash[:]) == reservation.SpecSHA256 && hex.EncodeToString(disksHash[:]) == reservation.DiskIdentitySHA256
}
