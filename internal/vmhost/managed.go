// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

// ManagedEngine owns Controller-created guests separately from local appliance recovery.
// Its journal survives Controller disconnects. The only command that starts a guest is a
// vm-create asking for start_after_create, and it starts that guest once, after the definition
// is verified; every other command leaves power state to vm-power.
type ManagedEngine struct {
	stateDir, networkDir string
	// observe verifies an interrupted creation or edit in the given libvirt state; nil selects
	// observeDefinition (tests stub it).
	observe func(context.Context, managedJournal, string) error
}

// VMUpdateEvidence is read-only proof that a particular successful vm-update
// advanced one locally journaled VM definition.  Pool ownership code uses the
// exact predecessor and successor definitions to settle a previously approved
// update without accepting an untrusted inventory rewrite.
type VMUpdateEvidence struct {
	OperationID string
	Status      string
	Previous    protocol.VMCreateSpec
	Current     protocol.VMCreateSpec
}

func NewManaged(stateDir string) *ManagedEngine {
	return &ManagedEngine{stateDir: stateDir, networkDir: "/etc/network"}
}

// inFlight holds the operation IDs this process is executing, so a report can tell a pending
// entry that is in progress from one an earlier process left behind. One agent process owns a
// host and its report path opens its own engine, so the set is process-wide.
var inFlight = struct {
	sync.Mutex
	ids map[string]bool
}{ids: map[string]bool{}}

func (e *ManagedEngine) observer() func(context.Context, managedJournal, string) error {
	if e.observe != nil {
		return e.observe
	}
	return e.observeDefinition
}

// startInterrupted is the recorded start outcome when the process died between persisting
// stage "starting" and recording libvirt's answer, and the domain is not running now.
const startInterrupted = "interrupted before the automatic start completed"

// errPowerRequestUncertain marks a failure after libvirt received a power request.
// Preflight state validation stays terminal; after the request crosses the process
// boundary only a subsequent observation can prove whether it took effect.
var errPowerRequestUncertain = errors.New("power request outcome is uncertain")

type powerRequestError struct {
	action string
	cause  error
}

// "did not confirm" rather than "refused": powerVM only builds this after observing the domain
// and finding the request unproven, so the cause may be a genuine libvirt refusal or a call that
// was killed while libvirt still held the domain job. Claiming a refusal for the second sent an
// operator to inspect a VM that had done exactly what was asked (BUG-0213). The cause carries
// libvirt's own last stderr line, which distinguishes them when it knows.
func (e powerRequestError) Error() string {
	return "libvirt did not confirm the " + e.action + " request; inspect the VM on its host: " + e.cause.Error()
}

func (e powerRequestError) Unwrap() []error { return []error{errPowerRequestUncertain, e.cause} }

type managedJournal struct {
	Command         protocol.Command `json:"command"`
	Result          protocol.Result  `json:"result"`
	Stage           string           `json:"stage"`
	Disks           []string         `json:"disks,omitempty"`
	MediaPath       string           `json:"media_path,omitempty"`
	DownloadedMedia *protocol.Media  `json:"downloaded_media,omitempty"`
	// AppliedStorage is Core's verified projection of a storage command. Command remains the
	// original request so idempotent replay compares exactly what the caller sent.
	AppliedStorage *protocol.StorageRegistration `json:"applied_storage,omitempty"`
	Domain         []byte                        `json:"domain,omitempty"`
	// Start records the outcome of start_after_create: "started", or why it did not start.
	// It is recorded in the same entry so a retried or reconciled creation never starts twice.
	Start string `json:"start,omitempty"`
	// Targets are the disks' libvirt target names, positional with Disks; they are assigned
	// once and kept across edits so a guest never sees its disks renamed. MediaTarget and
	// DriversTarget are the install and driver-disc CD drives every VM carries. An entry
	// written before the second drive existed has no DriversTarget; managedCurrent places it
	// beside the targets that entry already holds, so adding it renames nothing.
	Targets       []string `json:"targets,omitempty"`
	MediaTarget   string   `json:"media_target,omitempty"`
	DriversTarget string   `json:"drivers_target,omitempty"`
	// DriversPath is the resolved host path of the driver disc, recorded with MediaPath.
	DriversPath string `json:"drivers_path,omitempty"`
	// NVRAMPath is the observed UEFI variable-store path retained across the UEFI-to-BIOS
	// cleanup boundary. It is host state, not Controller state, and lets recovery remove the
	// exact file after libvirt has removed the definition.
	NVRAMPath string `json:"nvram_path,omitempty"`
	// SecureBootNVRAM is an in-place logical update of the UEFI store.  The staged hashes make
	// an interrupted native edit observable: Core only retries while the original store remains,
	// and only accepts the result after the exact desired value is present.
	SecureBootNVRAM     string `json:"secure_boot_nvram,omitempty"`
	SecureBootTemporary string `json:"secure_boot_temporary,omitempty"`
	SecureBootBefore    string `json:"secure_boot_before,omitempty"`
	SecureBootAfter     string `json:"secure_boot_after,omitempty"`
	SecureBootEnabled   bool   `json:"secure_boot_enabled,omitempty"`
	SecureBootMode      uint32 `json:"secure_boot_mode,omitempty"`
	SecureBootUID       uint32 `json:"secure_boot_uid,omitempty"`
	SecureBootGID       uint32 `json:"secure_boot_gid,omitempty"`
	// SecureBootPreviousDomain is the exact inactive definition observed before the native
	// transition. It makes a combined rename and Secure Boot change recoverable without
	// treating a guessed XML predecessor as authority.
	SecureBootPreviousDomain []byte `json:"secure_boot_previous_domain,omitempty"`
	// Retained lists disk files an edit detached from the VM; edits never delete data.
	Retained []string `json:"retained,omitempty"`
	// UnappliedObserved marks a failed vm-update recorded after allocation or define only
	// because the stopped VM's persistent definition was observed to still be exactly its
	// predecessor (see ReconcileRemoteVMUpdate).
	UnappliedObserved bool `json:"unapplied_observed,omitempty"`
	// EjectCleanup is written only by the local pool-leave state machine before it
	// removes a member projection.  It is not a wire field: it binds the exact
	// successful cleanup to the signed membership generation that caused it.
	EjectCleanup *ejectCleanup `json:"eject_cleanup,omitempty"`
	// EjectCleanupReapply consumes EjectCleanup logically only once this entry has
	// succeeded.  Keeping the receipt in the successful reapply makes a crash before
	// success fail closed without rewriting historical removal evidence.
	EjectCleanupReapply *ejectCleanupReapply `json:"eject_cleanup_reapply,omitempty"`
}

type ejectCleanup struct {
	PoolID, HostID, Resource, ResourceID string
	MembershipGeneration                 uint64
}

type ejectCleanupReapply struct {
	RemovalOperationID   string
	MembershipGeneration uint64
}

func (e *ManagedEngine) save(j managedJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(e.stateDir, j.Command.OperationID+".json"), b)
}
func (e *ManagedEngine) journals() ([]managedJournal, error) {
	entries, err := os.ReadDir(e.stateDir)
	if err != nil {
		return nil, err
	}
	out := []managedJournal{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(out) >= 10000 {
			return nil, errors.New("managed journal capacity exceeded")
		}
		j, err := e.readJournal(entry.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// SuccessfulVMUpdateEvidence returns the newest succeeded vm-update and its
// exact succeeded predecessor. It never changes journal or native state. An
// incomplete, duplicate, or unreadable history is not evidence for changing a
// master-owned VM definition.
func (e *ManagedEngine) SuccessfulVMUpdateEvidence(ctx context.Context, vmID string) (VMUpdateEvidence, error) {
	if vmID == "" {
		return VMUpdateEvidence{}, errors.New("VM identity is required")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	js, err := e.journals()
	if err != nil {
		return VMUpdateEvidence{}, errors.New("managed VM journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	var current *managedJournal
	for i := range js {
		if err := ctx.Err(); err != nil {
			return VMUpdateEvidence{}, err
		}
		j := &js[i]
		if j.Result.Status != "succeeded" || j.Command.Kind != "vm-update" || j.Command.VM == nil || j.Command.VM.ID != vmID {
			continue
		}
		if current == nil || j.Command.VM.Revision > current.Command.VM.Revision {
			current = j
			continue
		}
		if j.Command.VM.Revision == current.Command.VM.Revision {
			return VMUpdateEvidence{}, errors.New("managed VM update evidence is ambiguous")
		}
	}
	if current == nil {
		return VMUpdateEvidence{}, errors.New("no succeeded VM update evidence")
	}
	// A pending or needs-attention definition can have crossed a native boundary
	// without a conclusive result. Never settle an older update while that newer
	// definition remains unresolved. Failed updates are intentionally excluded:
	// the managed journal's existing revision semantics do not treat them as
	// current definitions.
	for i := range js {
		if err := ctx.Err(); err != nil {
			return VMUpdateEvidence{}, err
		}
		j := &js[i]
		if j.Command.VM == nil || j.Command.VM.ID != vmID || (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") || (j.Result.Status != "pending" && j.Result.Status != "needs_attention") {
			continue
		}
		if j.Command.VM.Revision >= current.Command.VM.Revision {
			return VMUpdateEvidence{}, errors.New("managed VM update evidence is superseded or unresolved")
		}
	}
	// Keep this evidence aligned with the journal's authoritative definition
	// projection. This is intentionally journal-only: the succeeded result was
	// produced after the normal native definition observation, while this helper
	// must not cause another native effect during master recovery.
	authoritative, ok := currentDefinitions(js)[vmID]
	if !ok || !reflect.DeepEqual(authoritative, *current.Command.VM) {
		return VMUpdateEvidence{}, errors.New("managed VM update evidence is not the current definition")
	}
	var previous *managedJournal
	for i := range js {
		if err := ctx.Err(); err != nil {
			return VMUpdateEvidence{}, err
		}
		j := &js[i]
		if j.Result.Status != "succeeded" || (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") || j.Command.VM == nil || j.Command.VM.ID != vmID || j.Command.VM.Revision != current.Command.VM.Revision-1 {
			continue
		}
		if previous != nil {
			return VMUpdateEvidence{}, errors.New("managed VM update predecessor is ambiguous")
		}
		previous = j
	}
	if previous == nil {
		return VMUpdateEvidence{}, errors.New("managed VM update predecessor is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	return VMUpdateEvidence{OperationID: current.Command.OperationID, Status: current.Result.Status, Previous: *previous.Command.VM, Current: *current.Command.VM}, nil
}

// VMHasPoolSharedStorageHistory reports whether a successful durable VM
// definition ever referenced a repository that was successfully registered as
// pool-shared. It intentionally retains old registration journals after a
// later detach: a formerly shared VM must not silently lose its master-owned
// execution fence merely because its newest definition has local disks.
// This is journal-only and performs no libvirt or storage observation.
func (e *ManagedEngine) VMHasPoolSharedStorageHistory(ctx context.Context, vmID string) (bool, error) {
	if vmID == "" {
		return false, errors.New("VM identity is required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	js, err := e.journals()
	if err != nil {
		return false, errors.New("managed VM journal is unavailable")
	}
	shared := map[string]bool{}
	for i := range js {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		j := js[i]
		if j.Result.Status == "succeeded" && j.Command.Kind == "storage-register" && j.Command.Storage != nil && protocol.IsPoolSharedStorage(*j.Command.Storage) {
			shared[j.Command.Storage.ID] = true
		}
	}
	for i := range js {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		j := js[i]
		if j.Result.Status != "succeeded" || (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") || j.Command.VM == nil || j.Command.VM.ID != vmID {
			continue
		}
		for _, disk := range j.Command.VM.Disks {
			if shared[disk.StorageID] {
				return true, nil
			}
		}
	}
	return false, nil
}

// JournaledCommand returns the command a previous execution durably recorded under
// operationID. A redelivered request must be applied exactly as it was journaled: Execute
// compares the whole command, so a caller that derives part of it from state the first
// attempt already changed has to reuse what that attempt wrote instead of recomputing it.
func (e *ManagedEngine) JournaledCommand(operationID string) (protocol.Command, bool) {
	if operationID == "" {
		return protocol.Command{}, false
	}
	js, err := e.journals()
	if err != nil {
		return protocol.Command{}, false
	}
	for i := range js {
		if js[i].Command.OperationID == operationID {
			return js[i].Command, true
		}
	}
	return protocol.Command{}, false
}

// FailedVMUpdateEvidence proves that one exact vm-update failed without
// replacing its predecessor. It derives that predecessor from the local
// journal, so a member never needs a replicated master VM definition. A failed
// result has the existing managed-journal meaning that no definition became
// current; pending and needs-attention results are not safe failure evidence.
func (e *ManagedEngine) FailedVMUpdateEvidence(ctx context.Context, vmID, operationID string, attempted protocol.VMCreateSpec) (VMUpdateEvidence, error) {
	if vmID == "" || operationID == "" || attempted.ID != vmID || attempted.Revision < 1 || protocol.ValidateVMSpec(attempted) != nil {
		return VMUpdateEvidence{}, errors.New("invalid VM update failure evidence request")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	js, err := e.journals()
	if err != nil {
		return VMUpdateEvidence{}, errors.New("managed VM journal is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	failed := false
	var previous *protocol.VMCreateSpec
	for i := range js {
		if err := ctx.Err(); err != nil {
			return VMUpdateEvidence{}, err
		}
		j := &js[i]
		if j.Command.VM == nil || j.Command.VM.ID != vmID || (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") {
			continue
		}
		v := *j.Command.VM
		if j.Command.Kind == "vm-update" && j.Command.OperationID == operationID {
			// Execute records a failed update only while it remains in validating;
			// once allocation, define, or live attachment may have crossed a
			// native boundary it records needs_attention instead. Require that
			// durable stage rather than trusting a failed label alone. The one
			// post-native exception is an update later observed still unapplied.
			if j.Result.Status != "failed" || (j.Stage != "validating" && !j.UnappliedObserved) || !reflect.DeepEqual(v, attempted) {
				return VMUpdateEvidence{}, errors.New("managed VM update failure evidence does not match the attempted definition")
			}
			failed = true
			continue
		}
		if (j.Result.Status == "pending" || j.Result.Status == "needs_attention") && v.Revision >= attempted.Revision-1 {
			return VMUpdateEvidence{}, errors.New("managed VM update failure evidence is superseded or unresolved")
		}
		if j.Result.Status == "succeeded" {
			if v.Revision >= attempted.Revision {
				return VMUpdateEvidence{}, errors.New("managed VM update failure evidence is superseded")
			}
			if v.Revision == attempted.Revision-1 {
				if previous != nil {
					return VMUpdateEvidence{}, errors.New("managed VM update predecessor is ambiguous")
				}
				candidate := v
				previous = &candidate
			}
		}
	}
	if !failed {
		return VMUpdateEvidence{}, errors.New("no matching failed VM update evidence")
	}
	if previous == nil {
		return VMUpdateEvidence{}, errors.New("managed VM update predecessor is ambiguous or unavailable")
	}
	authoritative, ok := currentDefinitions(js)[vmID]
	if !ok || !reflect.DeepEqual(authoritative, *previous) {
		return VMUpdateEvidence{}, errors.New("managed VM update failure evidence is not the current definition")
	}
	if err := ctx.Err(); err != nil {
		return VMUpdateEvidence{}, err
	}
	// For failed evidence Current names the exact attempted, unapplied definition;
	// Previous is the still-current definition. This preserves one evidence type
	// while making the abort binding explicit to the pool owner.
	return VMUpdateEvidence{OperationID: operationID, Status: "failed", Previous: *previous, Current: attempted}, nil
}
func (e *ManagedEngine) readJournal(name string) (managedJournal, error) {
	var j managedJournal
	b, err := readSmall(filepath.Join(e.stateDir, name), 1<<20)
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal(b, &j); err != nil {
		return j, err
	}
	if err = validateManaged(j.Command); err != nil || name != j.Command.OperationID+".json" || j.Result.OperationID != j.Command.OperationID {
		return j, errors.New("invalid managed journal identity or command")
	}
	if j.DownloadedMedia != nil {
		if j.Command.Kind != "iso-import" || j.Command.Media.URL == "" {
			return j, errors.New("unexpected downloaded ISO metadata")
		}
		resolved := *j.Command.Media
		resolved.URL, resolved.SizeBytes, resolved.SHA256 = "", j.DownloadedMedia.SizeBytes, j.DownloadedMedia.SHA256
		cmd := j.Command
		cmd.Media = &resolved
		if !reflect.DeepEqual(resolved, *j.DownloadedMedia) || validateManaged(cmd) != nil {
			return j, errors.New("invalid downloaded ISO metadata")
		}
	}
	switch j.Result.Status {
	case "pending", "succeeded", "failed", "needs_attention":
	default:
		return j, errors.New("invalid managed journal result")
	}
	return j, nil
}

// readable lists every journal entry that can be read and names the files that cannot.
// Execute fails closed on one corrupt file; reporting must not, or a replaced Controller
// would adopt nothing from a pool over one bad file.
func (e *ManagedEngine) readable() ([]managedJournal, []string) {
	entries, err := os.ReadDir(e.stateDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, []string{err.Error()}
	}
	var out []managedJournal
	var bad []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(out) >= 10000 {
			bad = append(bad, "managed journal capacity exceeded")
			break
		}
		j, err := e.readJournal(entry.Name())
		if err != nil {
			bad = append(bad, entry.Name()+": "+err.Error())
			continue
		}
		out = append(out, j)
	}
	return out, bad
}

// Execute records intent before effects. Repeated IDs return the durable result;
// interrupted mutations are observed, never replayed. Errors indicate persistence
// failure; callers must retain the command and report an uncertain outcome.
func (e *ManagedEngine) Execute(ctx context.Context, c protocol.Command, fetch func(context.Context, string) (io.ReadCloser, error)) (protocol.Result, error) {
	return e.execute(ctx, c, fetch, nil, nil)
}

// ExecuteEjectCleanup is the leave state machine's private removal path. The marker is
// persisted with intent before native removal, so an interrupted leave cannot be mistaken
// for a deliberate removal or silently reopened on a later join.
func (e *ManagedEngine) ExecuteEjectCleanup(ctx context.Context, c protocol.Command, poolID, hostID string, membershipGeneration uint64) (protocol.Result, error) {
	resource, id, generation, ok := lifecycleResource(c)
	if !ok || poolID == "" || hostID == "" || membershipGeneration == 0 || generation == 0 {
		return protocol.Result{OperationID: c.OperationID, Status: "failed", Error: "invalid eject cleanup", ObservedAt: time.Now().UTC()}, nil
	}
	return e.execute(ctx, c, nil, &ejectCleanup{PoolID: poolID, HostID: hostID, Resource: resource, ResourceID: id, MembershipGeneration: membershipGeneration}, nil)
}

// ExecuteEjectCleanupReapply is the member's private, authenticated rejoin path. It grants
// the one matching cleanup tombstone only to a later membership and records consumption in
// the normal successful registration journal entry.
func (e *ManagedEngine) ExecuteEjectCleanupReapply(ctx context.Context, c protocol.Command, poolID, hostID string, membershipGeneration uint64) (protocol.Result, error) {
	if poolID == "" || hostID == "" || membershipGeneration == 0 {
		return protocol.Result{OperationID: c.OperationID, Status: "failed", Error: "invalid eject cleanup reapply", ObservedAt: time.Now().UTC()}, nil
	}
	js, bad := e.readable()
	if len(bad) != 0 {
		return protocol.Result{OperationID: c.OperationID, Status: "failed", Error: ErrManagedJournalIncomplete.Error(), ObservedAt: time.Now().UTC()}, nil
	}
	removal, err := eligibleEjectCleanup(js, c, poolID, hostID, membershipGeneration)
	if err != nil {
		return protocol.Result{OperationID: c.OperationID, Status: "failed", Error: err.Error(), ObservedAt: time.Now().UTC()}, nil
	}
	// A member reconcile command can intentionally be stable for an unchanged master
	// resource. It cannot also name every later eject cleanup: Execute would return the
	// earlier completed receipt before applying or recording consumption of this removal.
	// Bind the internal receipt to the exact selected cleanup instead. The result is stable
	// for retries of this cleanup and distinct for the next signed eject of the same ID.
	c.OperationID = ejectCleanupReapplyOperation(c.OperationID, removal.Command.OperationID)
	return e.execute(ctx, c, nil, nil, &ejectCleanupReapply{RemovalOperationID: removal.Command.OperationID, MembershipGeneration: membershipGeneration})
}

func ejectCleanupReapplyOperation(commandOperation, removalOperation string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte("eject-cleanup-reapply\x00"+commandOperation+"\x00"+removalOperation)))
}

// EjectCleanupReapplyAvailable tells a local reconciler whether an old tombstone names this
// resource and, if it does, whether its one safe reapply grant is currently valid.
func (e *ManagedEngine) EjectCleanupReapplyAvailable(c protocol.Command, poolID, hostID string, membershipGeneration uint64) (bool, error) {
	js, bad := e.readable()
	if len(bad) != 0 {
		return false, ErrManagedJournalIncomplete
	}
	resource, id, _, _, _, ok := cleanupResource(c)
	if !ok {
		return false, nil
	}
	found, marked := false, false
	for _, j := range js {
		kind, removedID, _, removed := lifecycleResource(j.Command)
		if removed && j.Result.Status == "succeeded" && kind == resource && removedID == id {
			found = true
			marked = marked || j.EjectCleanup != nil
		}
	}
	if !found {
		return false, nil
	}
	if !marked {
		return false, nil
	}
	_, err := eligibleEjectCleanup(js, c, poolID, hostID, membershipGeneration)
	return err == nil, err
}

func (e *ManagedEngine) execute(ctx context.Context, c protocol.Command, fetch func(context.Context, string) (io.ReadCloser, error), cleanup *ejectCleanup, reapply *ejectCleanupReapply) (protocol.Result, error) {
	result := protocol.Result{OperationID: c.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := validateManaged(c); err != nil {
		result.Error = err.Error()
		return result, nil
	}
	inFlight.Lock()
	inFlight.ids[c.OperationID] = true
	inFlight.Unlock()
	defer func() { inFlight.Lock(); delete(inFlight.ids, c.OperationID); inFlight.Unlock() }()
	if !filepath.IsAbs(e.stateDir) || filepath.Clean(e.stateDir) == "/" {
		return result, errors.New("managed state requires an absolute non-root directory")
	}
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		return result, err
	}
	real, err := filepath.EvalSymlinks(e.stateDir)
	if err != nil || real != e.stateDir {
		return result, errors.New("managed state must not use symlinks")
	}
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return result, err
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return result, err
	}
	if reapply != nil {
		_, _, poolID, hostID, _, _ := cleanupResource(c)
		removal, checkErr := eligibleEjectCleanup(js, c, poolID, hostID, reapply.MembershipGeneration)
		if checkErr != nil || removal.Command.OperationID != reapply.RemovalOperationID {
			if checkErr != nil {
				result.Error = checkErr.Error()
			} else {
				result.Error = "eject cleanup reapply no longer matches its removal"
			}
			return result, nil
		}
	}
	want, _ := json.Marshal(c)
	for _, old := range js {
		if old.Command.OperationID == c.OperationID {
			got, _ := json.Marshal(old.Command)
			if string(got) != string(want) {
				result.Error = "operation ID already belongs to different input"
				return result, nil
			}
			if old.Result.Status == "pending" || old.Result.Status == "needs_attention" {
				return e.reconcile(ctx, old)
			}
			// A completed ZFS pool may need its non-destructive local registration
			// retried after a host prerequisite (for example libvirt's storage
			// driver) is repaired. The pool provision receipt remains the authority;
			// retry only the exact failed registration, never the disk operation.
			if old.Result.Status == "failed" && c.Kind == "storage-register" && c.Storage != nil && c.Storage.ZFSProvisioned && zfsRegistrationRetryAllowed(ctx) {
				return e.retryFailedZFSRegistration(ctx, old, js, fetch)
			}
			return old.Result, nil
		}
	}
	if len(js) >= 10000 {
		return result, errors.New("managed journal full; manual recovery required")
	}
	j := managedJournal{Command: c, Result: result, Stage: "validating", EjectCleanup: cleanup, EjectCleanupReapply: reapply}
	if c.Kind == "storage-register" && c.Storage.ID == "" {
		allocated := *c.Storage
		allocated.ID, allocated.Generation = hostID(), 1
		j.AppliedStorage = &allocated
	}
	// Reserve every resource ID, even when its operation was interrupted or failed. A failed
	// or needs_attention vm-update is the exception: Applied does not treat either as the
	// current definition (only succeeded revisions are), so the revision they named is still
	// the next one the Controller sends; update's revision check serialises the retries.
	for _, old := range js {
		if c.StorageLifecycle != nil && old.Command.StorageLifecycle != nil && old.Command.StorageLifecycle.RepositoryID == c.StorageLifecycle.RepositoryID && (old.Result.Status == "pending" || old.Result.Status == "needs_attention") {
			j.Result.Error = "storage lifecycle operation is unresolved; inspect or reconcile it before retrying"
			return j.Result, e.save(j)
		}
		if c.ResourceLifecycle != nil && old.Command.ResourceLifecycle != nil && old.Command.ResourceLifecycle.Resource == c.ResourceLifecycle.Resource && old.Command.ResourceLifecycle.ResourceID == c.ResourceLifecycle.ResourceID && (old.Result.Status == "pending" || old.Result.Status == "needs_attention") {
			j.Result.Error = c.ResourceLifecycle.Resource + " lifecycle operation is unresolved; inspect or reconcile it before retrying"
			return j.Result, e.save(j)
		}
		if c.CredentialRetire != nil && old.Command.CredentialRetire != nil && old.Command.CredentialRetire.Receipt == c.CredentialRetire.Receipt && (old.Result.Status == "pending" || old.Result.Status == "needs_attention") {
			j.Result.Error = "storage credential retirement is unresolved; inspect or reconcile it before retrying"
			return j.Result, e.save(j)
		}
		if old.Command.Kind == "vm-update" && (old.Result.Status == "failed" || old.Result.Status == "needs_attention") {
			continue
		}
		// A failed remote storage preparation has no applied projection; a later Core
		// reconciliation may retry the identical repository after its server recovers.
		// needs_attention remains reserved because native rollback is ambiguous.
		if c.Kind == "storage-register" && old.Command.Kind == "storage-register" && old.Result.Status == "failed" && old.Command.Storage != nil && c.Storage != nil && old.Command.Storage.ID == c.Storage.ID && old.Command.Storage.Generation == c.Storage.Generation {
			continue
		}
		// A failed guest VLAN apply left no applied projection, and building the bridge is
		// idempotent, so a member may retry the same pool network once its uplink or trunk is
		// fixed. needs_attention stays reserved, as it does for storage.
		if c.Kind == "network-register" && old.Command.Kind == "network-register" && old.Result.Status == "failed" && old.Command.Network != nil && c.Network != nil && old.Command.Network.ID == c.Network.ID && protocol.IsPoolSharedNetwork(*c.Network) && protocol.IsPoolSharedNetwork(*old.Command.Network) {
			continue
		}
		if managedResourceID(old.Command) == managedResourceID(c) && !(reapply != nil && (old.Result.Status == "succeeded" || old.Result.Status == "failed")) {
			j.Result.Error = "resource ID already reserved by another operation"
			return j.Result, e.save(j)
		}
	}
	j.Result.Status = "pending"
	if err = e.save(j); err != nil {
		return result, err
	}
	err = e.apply(ctx, &j, js, fetch)
	j.Result.ObservedAt = time.Now().UTC()
	if err == nil {
		j.Result.Status = "succeeded"
		j.Result.Error = ""
	} else {
		j.Result.Status = "failed"
		if errors.Is(err, ErrStorageRollbackUncertain) || errors.Is(err, errPowerRequestUncertain) || j.Stage == "allocating" || j.Stage == "defining" || j.Stage == "starting" || j.Stage == "attaching" || j.Stage == "undefining" || j.Stage == "wiping" {
			j.Result.Status = "needs_attention"
		}
		j.Result.Error = err.Error()
	}
	return j.Result, e.save(j)
}

type zfsRegistrationRetryContextKey struct{}

// WithZFSProvisionRegistrationRetry authorizes one retry of a failed local ZFS
// registration. Callers may use it only after ZFSProvisioner.Provision returned
// succeeded for the same operation, which re-reads the tagged pool/dataset and
// GUIDs. Ordinary storage reconciliation must never infer that proof from a
// registration request alone.
func WithZFSProvisionRegistrationRetry(ctx context.Context) context.Context {
	return context.WithValue(ctx, zfsRegistrationRetryContextKey{}, true)
}

func zfsRegistrationRetryAllowed(ctx context.Context) bool {
	allowed, _ := ctx.Value(zfsRegistrationRetryContextKey{}).(bool)
	return allowed
}

func (e *ManagedEngine) retryFailedZFSRegistration(ctx context.Context, j managedJournal, js []managedJournal, fetch func(context.Context, string) (io.ReadCloser, error)) (protocol.Result, error) {
	j.Result = protocol.Result{OperationID: j.Command.OperationID, Status: "pending", ObservedAt: time.Now().UTC()}
	j.Stage = "validating"
	if err := e.save(j); err != nil {
		return j.Result, err
	}
	err := e.apply(ctx, &j, js, fetch)
	j.Result.ObservedAt = time.Now().UTC()
	if err == nil {
		j.Result.Status, j.Result.Error = "succeeded", ""
	} else {
		j.Result.Status, j.Result.Error = "failed", err.Error()
		if errors.Is(err, ErrStorageRollbackUncertain) || j.Stage == "attaching" {
			j.Result.Status = "needs_attention"
		}
	}
	return j.Result, e.save(j)
}

// CommitClusteredStorageRegistration records a Core-observed clustered GFS2
// registration after bootstrap identity verification, before Pacemaker mounts it.
// Normal storage-register prepares/mounts and is therefore intentionally unsuitable here.
func CommitClusteredStorageRegistration(ctx context.Context, stateDir string, storage protocol.StorageRegistration) error {
	if !protocol.IsClusteredISCSI(storage) || storage.ID == "" || storage.Generation == 0 || storage.Path == "" || storage.Identity.ISCSI == nil {
		return errors.New("invalid clustered storage registration")
	}
	storage.Capabilities = storageCapabilities(storage)
	e := NewManaged(stateDir)
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		return err
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
	for _, old := range js {
		if old.Command.Storage != nil && old.Command.Storage.ID == storage.ID {
			if old.Command.Storage.Generation != storage.Generation {
				return errors.New("clustered storage registration generation conflicts with journal")
			}
			if old.Result.Status == "succeeded" && old.AppliedStorage != nil {
				applied := *old.AppliedStorage
				normalizeStorageCapabilities(&applied)
				if reflect.DeepEqual(applied, storage) {
					return nil
				}
			}
			return errors.New("clustered storage registration conflicts with journal")
		}
	}
	request := storage
	request.Path = ""
	request.Identity = protocol.StorageIdentity{}
	source := *request.Source.ISCSI
	source.MountPath = ""
	request.Source.ISCSI = &source
	command := protocol.Command{OperationID: "cluster-storage-" + storage.ID + "-" + strconv.FormatUint(storage.Generation, 10), Kind: "storage-register", Storage: &request}
	if err := validateManaged(command); err != nil {
		return err
	}
	return e.save(managedJournal{Command: command, AppliedStorage: &storage, Result: protocol.Result{OperationID: command.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}, Stage: "clustered-bootstrap"})
}

// ClusteredStorageReceipt reads the committed clustered registration without
// promotion, observation, mounting, or any other native effect. Callers use
// it only as a crash-recovery receipt; unreadable journal state is distinct
// from an absent registration and must remain fail-closed.
func (e *ManagedEngine) ClusteredStorageReceipt(id string) (protocol.StorageRegistration, bool, error) {
	if id == "" {
		return protocol.StorageRegistration{}, false, errors.New("clustered storage receipt ID is required")
	}
	js, bad := e.readable()
	if len(bad) != 0 {
		return protocol.StorageRegistration{}, false, errors.New("managed storage receipt is unreadable")
	}
	storage, ok := storageProjection(js)[id]
	if !ok {
		return protocol.StorageRegistration{}, false, nil
	}
	if !protocol.IsClusteredISCSI(storage) {
		return protocol.StorageRegistration{}, false, errors.New("managed storage receipt is not clustered iSCSI")
	}
	return storage, true, nil
}

// AppliedStorage lists the durable storage registrations this host has applied.
// It is deliberately a journal projection only: unlike Applied, it does not
// promote interrupted VM or media work, inspect credentials, or query libvirt.
// Callers which need those observations must continue to use Applied.
func (e *ManagedEngine) AppliedStorage() ([]protocol.StorageRegistration, []protocol.Check) {
	js, bad := e.readable()
	return appliedStorageProjection(js, bad)
}

// ErrManagedJournalIncomplete reports that this host's journal could not be read whole:
// an entry is unparseable or the retained-entry cap truncated the listing. A projection built
// from it is a subset of what this host actually holds, so a peer must never read absence from
// it as removal. The text is fixed and names no file, so it is safe to return over the wire.
var ErrManagedJournalIncomplete = errors.New("managed journal is incomplete")

// AppliedNetworks lists succeeded network registrations from the journal without
// promoting interrupted work or querying libvirt. Peer pool list handlers use this, and a
// member deletes what an authoritative list omits, so an incomplete journal is an error here
// rather than a short list: the host's own inventory report keeps using Applied, which
// surfaces the same condition as the managed-journal check instead of failing.
func (e *ManagedEngine) AppliedNetworks() ([]protocol.NetworkRegistration, error) {
	js, bad := e.readable()
	if len(bad) != 0 {
		return nil, ErrManagedJournalIncomplete
	}
	out := appliedNetworkProjection(js)
	for i := range out {
		out[i].MemberAccess = nil
	}
	return out, nil
}

// NetworkRemoved reports whether this host durably removed the network, which is permanent:
// Execute reserves the resource ID for good, so the same ID can never be registered again here.
func (e *ManagedEngine) NetworkRemoved(id string) (bool, error) {
	js, bad := e.readable()
	if len(bad) != 0 {
		return false, ErrManagedJournalIncomplete
	}
	_, ok := networkTombstones(js)[id]
	return ok, nil
}

// MediaRemoved is the same permanent-removal question for an imported ISO.
func (e *ManagedEngine) MediaRemoved(id string) (bool, error) {
	js, bad := e.readable()
	if len(bad) != 0 {
		return false, ErrManagedJournalIncomplete
	}
	_, ok := mediaTombstones(js)[id]
	return ok, nil
}

func lifecycleResource(c protocol.Command) (string, string, uint64, bool) {
	if v := c.ResourceLifecycle; v != nil && v.Action == "remove" {
		return v.Resource, v.ResourceID, v.ExpectedGeneration, true
	}
	if v := c.StorageLifecycle; v != nil && v.Action == "detach" {
		return "storage", v.RepositoryID, v.ExpectedGeneration, true
	}
	return "", "", 0, false
}

func cleanupResource(c protocol.Command) (resource, id, poolID, hostID string, generation uint64, ok bool) {
	if c.Network != nil {
		return "network", c.Network.ID, c.Network.PoolID, c.Network.HostID, protocol.NetworkGeneration(*c.Network), true
	}
	if c.Storage != nil {
		return "storage", c.Storage.ID, c.Storage.PoolID, c.Storage.HostID, c.Storage.Generation, true
	}
	if c.Media != nil {
		return "media", c.Media.ID, c.Media.PoolID, c.Media.HostID, protocol.MediaGeneration(*c.Media), true
	}
	return "", "", "", "", 0, false
}

// eligibleEjectCleanup intentionally decides from generations and explicit provenance,
// never journal filenames or operation IDs. Any later unmarked removal is authoritative.
func eligibleEjectCleanup(js []managedJournal, c protocol.Command, poolID, hostID string, membershipGeneration uint64) (managedJournal, error) {
	resource, id, commandPool, commandHost, generation, ok := cleanupResource(c)
	if !ok || resource == "" || id == "" || commandPool != poolID || commandHost != hostID {
		return managedJournal{}, errors.New("eject cleanup reapply does not match this resource")
	}
	var found *managedJournal
	for i := range js {
		j := &js[i]
		kind, removedID, removedGeneration, removed := lifecycleResource(j.Command)
		if !removed || j.Result.Status != "succeeded" || kind != resource || removedID != id {
			continue
		}
		if j.EjectCleanup == nil || j.EjectCleanup.PoolID != poolID || j.EjectCleanup.HostID != hostID || j.EjectCleanup.Resource != resource || j.EjectCleanup.ResourceID != id {
			continue
		}
		// Eject cleanup is member-local. The master retained this resource at the
		// removal's expected generation, so rejoin normally reapplies that same
		// generation; only an older desired revision is stale.
		if membershipGeneration <= j.EjectCleanup.MembershipGeneration || generation < removedGeneration {
			continue
		}
		// A member can be ejected and rejoined more than once without the master
		// changing this resource's generation. Membership generation is therefore
		// the durable cleanup sequence; resource generation only breaks a tie.
		if found == nil || j.EjectCleanup.MembershipGeneration > found.EjectCleanup.MembershipGeneration ||
			(j.EjectCleanup.MembershipGeneration == found.EjectCleanup.MembershipGeneration && removedGeneration > lifecycleGeneration(found.Command)) {
			copy := *j
			found = &copy
			continue
		}
		if j.EjectCleanup.MembershipGeneration == found.EjectCleanup.MembershipGeneration && removedGeneration == lifecycleGeneration(found.Command) && j.Command.OperationID != found.Command.OperationID {
			return managedJournal{}, errors.New("eject cleanup provenance is ambiguous")
		}
	}
	if found == nil {
		return managedJournal{}, errors.New("eject cleanup reapply has stale membership or resource generation")
	}
	if ejectCleanupConsumed(js, found) {
		return managedJournal{}, errors.New("eject cleanup reapply was already consumed")
	}
	// A deliberate removal with at least the cleanup's generation is later in the only
	// durable resource sequence available. Equal generations are ambiguous and fail closed.
	base := lifecycleGeneration(found.Command)
	for _, j := range js {
		kind, removedID, removedGeneration, removed := lifecycleResource(j.Command)
		if removed && j.Result.Status == "succeeded" && kind == resource && removedID == id && j.EjectCleanup == nil && removedGeneration >= base {
			return managedJournal{}, errors.New("resource has a deliberate removal after eject cleanup")
		}
	}
	return *found, nil
}

func lifecycleGeneration(c protocol.Command) uint64 {
	_, _, generation, _ := lifecycleResource(c)
	return generation
}

// appliedNetworkProjection lists the networks this host still holds, in journal order, with
// renames applied, followed by a tombstone row for each network it removed.
func appliedNetworkProjection(js []managedJournal) []protocol.NetworkRegistration {
	live, removed := networkProjection(js), networkTombstones(js)
	out, seen := make([]protocol.NetworkRegistration, 0, len(js)), map[string]bool{}
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.Kind != "network-register" || j.Command.Network == nil {
			continue
		}
		n, ok := live[j.Command.Network.ID]
		if !ok || seen[n.ID] {
			continue
		}
		seen[n.ID] = true
		out = append(out, n)
	}
	for _, id := range slices.Sorted(maps.Keys(removed)) {
		out = append(out, removed[id])
	}
	return out
}

// appliedMediaProjection is the same for imported ISOs, which have no rename.
func appliedMediaProjection(js []managedJournal) []protocol.Media {
	removed := mediaTombstones(js)
	out, seen := make([]protocol.Media, 0, len(js)), map[string]bool{}
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.Kind != "iso-import" || j.Command.Media == nil {
			continue
		}
		m := journalMedia(j)
		if _, gone := removed[m.ID]; gone || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		m.OperationID, m.Status, m.Error = "", "", ""
		out = append(out, m)
	}
	for _, id := range slices.Sorted(maps.Keys(removed)) {
		out = append(out, removed[id])
	}
	return out
}

// AppliedMedia lists succeeded ISO imports from the journal without downloading or
// promoting interrupted transfers. Peer pool media list handlers use this.
func (e *ManagedEngine) AppliedMedia() ([]protocol.Media, error) {
	js, bad := e.readable()
	if len(bad) != 0 {
		return nil, ErrManagedJournalIncomplete
	}
	out := appliedMediaProjection(js)
	for i := range out {
		out[i].URL, out[i].MemberAccess = "", nil
	}
	return out, nil
}

// sharedMediaAdoptionOperation names the journal entry AdoptSharedMedia writes for an ISO a
// member found already present on pool-shared storage. It is deterministic so a repeated
// adoption reuses the file, and it is what distinguishes an adopted copy from an import this
// host performed itself. It must stay inside identifier's 64 characters: a pool media ID is
// already up to 64 long, so it cannot be a prefix; a bounded digest of it can.
func sharedMediaAdoptionOperation(mediaID string) string {
	digest := sha256.Sum256([]byte("pool-media\x00" + mediaID))
	return "media-" + hex.EncodeToString(digest[:])[:32]
}

// mediaAdopted reports whether this host's live record of the ISO is an adoption of a file
// another host imported, rather than an import this host performed. On pool-shared storage
// that file is the pool's only copy, so only the importing host may unlink it.
func mediaAdopted(js []managedJournal, id string) bool {
	for _, j := range js {
		if j.Result.Status == "succeeded" && j.Command.Kind == "iso-import" && j.Command.Media != nil && j.Command.Media.ID == id {
			return j.Command.OperationID == sharedMediaAdoptionOperation(id)
		}
	}
	return false
}

// MediaAdoptedHere reports whether this host's live record of an ISO is an adoption of a
// file another host imported onto pool-shared storage, rather than an import this host
// performed. Removal already refuses to unlink an adopted file — the pool's only copy
// belongs to the host that imported it — so a pool master that holds only an adopted record
// must say so instead of silently dropping the record and leaving the file behind.
func (e *ManagedEngine) MediaAdoptedHere(id string) bool {
	js, _ := e.readable()
	return mediaAdopted(js, id)
}

// AdoptSharedMedia journals a succeeded iso-import for an ISO already present on
// pool-shared storage after member pull. It verifies size and digest and never downloads.
func (e *ManagedEngine) AdoptSharedMedia(ctx context.Context, media protocol.Media) error {
	return e.adoptSharedMedia(ctx, media, nil)
}

// AdoptSharedMediaEjectCleanupReapply is the non-wire rejoin counterpart of
// ExecuteEjectCleanupReapply for metadata-only shared ISO adoption.
func (e *ManagedEngine) AdoptSharedMediaEjectCleanupReapply(ctx context.Context, media protocol.Media, membershipGeneration uint64) error {
	js, bad := e.readable()
	if len(bad) != 0 {
		return ErrManagedJournalIncomplete
	}
	removal, err := eligibleEjectCleanup(js, protocol.Command{Kind: "iso-import", Media: &media}, media.PoolID, media.HostID, membershipGeneration)
	if err != nil {
		return err
	}
	return e.adoptSharedMedia(ctx, media, &ejectCleanupReapply{RemovalOperationID: removal.Command.OperationID, MembershipGeneration: membershipGeneration})
}

func (e *ManagedEngine) adoptSharedMedia(ctx context.Context, media protocol.Media, reapply *ejectCleanupReapply) error {
	if media.ID == "" || media.StorageID == "" || media.SHA256 == "" || media.SizeBytes < 32774 {
		return errors.New("invalid shared media adoption")
	}
	js, _ := e.readable()
	if published, path, err := publishedMediaPath(js, media.ID); err == nil {
		// Already adopted: prove the published file is still present at its recorded
		// size rather than re-hashing every shared ISO on every converge pass. The
		// digest is verified on first adoption and again whenever a VM attaches it.
		if published.StorageID == media.StorageID && published.SHA256 == media.SHA256 && published.SizeBytes == media.SizeBytes {
			if info, statErr := os.Lstat(path); statErr == nil && info.Mode().IsRegular() && info.Size() == media.SizeBytes {
				return nil
			}
		}
		return verifyISOContext(ctx, path, media)
	}
	storage, err := managedStorage(js, media.StorageID)
	if err != nil {
		return err
	}
	if storage.Path == "" {
		return errors.New("shared media storage path is unavailable")
	}
	path := filepath.Join(storage.Path, "nex-media", media.ID+".iso")
	if err = verifyISOContext(ctx, path, media); err != nil {
		return err
	}
	op := sharedMediaAdoptionOperation(media.ID)
	media.URL, media.OperationID, media.Status, media.Error, media.MemberAccess = "", op, "succeeded", "", nil
	command := protocol.Command{OperationID: op, Kind: "iso-import", Media: &media}
	// save() does not validate, and an entry readJournal rejects fails every later
	// managed operation on this host. Apply the same validation Execute applies.
	if err = validateManaged(command); err != nil {
		return err
	}
	return e.save(managedJournal{
		Command:             command,
		Result:              protocol.Result{OperationID: op, Status: "succeeded", ObservedAt: time.Now().UTC()},
		MediaPath:           path,
		EjectCleanupReapply: reapply,
	})
}

// EnsureAppliedNetwork observes a network this host has already journalled and, for a VLAN
// guest bridge removed out of band, re-applies the unchanged registration. Execute cannot:
// it reserves the network's resource ID permanently, so pool converge would otherwise
// report a journal match as success forever without ever observing the bridge.
func (e *ManagedEngine) EnsureAppliedNetwork(ctx context.Context, id string) error {
	js, err := e.journals()
	if err != nil {
		return err
	}
	n, err := managedNetwork(js, id)
	if err != nil {
		return err
	}
	if err = verifyNetwork(ctx, n); err == nil || n.Kind != "vlan" {
		return err
	}
	// Rebuilding rewrites host network configuration, so it takes the lock Execute holds.
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lock)
	return ensureGuestBridge(ctx, n, e.networkDir)
}

func appliedStorageProjection(js []managedJournal, bad []string) ([]protocol.StorageRegistration, []protocol.Check) {
	storage := make([]protocol.StorageRegistration, 0, len(js))
	for _, s := range storageProjection(js) {
		s.OperationID, s.Status, s.Error = "", "", ""
		storage = append(storage, s)
	}
	for _, s := range storageTombstones(js) {
		storage = append(storage, s)
	}
	journal := managedJournalCheck(bad, time.Now().UTC())
	return storage, []protocol.Check{journal}
}

func managedJournalCheck(bad []string, observedAt time.Time) protocol.Check {
	journal := protocol.Check{Name: "managed-journal", Status: "healthy", ObservedAt: observedAt}
	if len(bad) > 0 {
		journal.Status, journal.Error = "failed", "unreadable managed journal entries skipped: "+strings.Join(bad, "; ")
	} else {
		journal.LastSuccess = &observedAt
	}
	return journal
}
func managedResourceID(c protocol.Command) string {
	switch c.Kind {
	case "storage-register":
		if c.Storage.ID == "" {
			return "storage-op:" + c.OperationID
		}
		return "storage:" + c.Storage.ID
	case "storage-update", "storage-recheck", "storage-detach":
		return "storage-lifecycle:" + c.StorageLifecycle.RepositoryID + ":" + c.OperationID
	case "storage-credential-retire":
		return "storage-credential-retire:" + c.CredentialRetire.Receipt + ":" + c.OperationID
	case "network-remove", "network-update", "media-remove":
		// A lifecycle change cannot reuse the permanent network:/media: reservation the
		// registration holds, so each one gets its own slot.
		return c.ResourceLifecycle.Resource + "-lifecycle:" + c.ResourceLifecycle.ResourceID + ":" + c.OperationID
	case "network-register":
		return "network:" + c.Network.ID
	case "iso-import":
		return "media:" + c.Media.ID
	case "vm-create":
		return "vm:" + c.VM.ID
	case "vm-update":
		return fmt.Sprintf("vm-update:%s:%d", c.VM.ID, c.VM.Revision)
	case "vm-power":
		return "power:" + c.OperationID
	case "vm-delete":
		return "vm-delete:" + c.OperationID
	case "vm-media":
		return "vm-media:" + c.OperationID
	}
	return ""
}

var managedSource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func validateManaged(c protocol.Command) error {
	if !identifier.MatchString(c.OperationID) {
		return errors.New("invalid operation ID")
	}
	count := 0
	for _, present := range []bool{c.Storage != nil, c.Network != nil, c.Media != nil, c.VM != nil, c.Power != nil, c.Delete != nil, c.VMMedia != nil, c.StorageLifecycle != nil, c.ResourceLifecycle != nil, c.CredentialRetire != nil, c.ZFSProvision != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("exactly one command payload is required")
	}
	var id, pool, host string
	switch c.Kind {
	case "storage-zfs-provision":
		if c.ZFSProvision == nil {
			return errors.New("ZFS provision payload required")
		}
		if err := protocol.ValidateZFSProvision(*c.ZFSProvision); err != nil {
			return err
		}
		id, pool = c.OperationID, c.ZFSProvision.PoolID
	case "storage-register":
		if c.Storage == nil {
			return errors.New("storage payload required")
		}
		s := c.Storage
		id, pool, host = s.ID, s.PoolID, s.HostID
		if err := protocol.ValidateStorageRegistration(*s, true); err != nil {
			return err
		}
	case "storage-update", "storage-recheck", "storage-detach":
		if c.StorageLifecycle == nil || c.StorageLifecycle.Action != strings.TrimPrefix(c.Kind, "storage-") {
			return errors.New("storage lifecycle payload does not match command")
		}
		if err := protocol.ValidateStorageLifecycle(*c.StorageLifecycle); err != nil {
			return err
		}
		id, pool, host = c.StorageLifecycle.RepositoryID, c.StorageLifecycle.PoolID, c.StorageLifecycle.HostID
	case "network-remove", "network-update", "media-remove":
		if c.ResourceLifecycle == nil || c.Kind != c.ResourceLifecycle.Resource+"-"+c.ResourceLifecycle.Action {
			return errors.New("resource lifecycle payload does not match command")
		}
		if err := protocol.ValidateResourceLifecycle(*c.ResourceLifecycle); err != nil {
			return err
		}
		id, pool, host = c.ResourceLifecycle.ResourceID, c.ResourceLifecycle.PoolID, c.ResourceLifecycle.HostID
	case "storage-credential-retire":
		if c.CredentialRetire == nil {
			return errors.New("storage credential retirement payload required")
		}
		if err := protocol.ValidateStorageCredentialRetire(*c.CredentialRetire); err != nil {
			return err
		}
		id, pool, host = c.CredentialRetire.Receipt, c.CredentialRetire.PoolID, c.CredentialRetire.HostID
	case "network-register":
		if c.Network == nil {
			return errors.New("network payload required")
		}
		n := c.Network
		id, pool, host = n.ID, n.PoolID, n.HostID
		if n.Kind == "vlan" {
			if err := protocol.ValidateVLAN(*n); err != nil {
				return err
			}
		} else if (n.Kind != "bridge" && n.Kind != "network") || !managedSource.MatchString(n.Source) || n.VLAN != 0 || (n.Uplink != "" && (n.Kind != "bridge" || !protocol.ValidUplink(n.Uplink))) {
			return errors.New("select an existing bridge or libvirt network")
		}
	case "iso-import":
		if c.Media == nil {
			return errors.New("media payload required")
		}
		m := c.Media
		id, pool, host = m.ID, m.PoolID, m.HostID
		if m.URL != "" {
			if err := protocol.ValidateISOURL(m.URL); err != nil {
				return err
			}
			if !identifier.MatchString(m.StorageID) || m.SizeBytes != 0 || m.SHA256 != "" {
				return errors.New("URL ISO requires storage and host-computed size and SHA-256")
			}
			break
		}
		h, err := hex.DecodeString(m.SHA256)
		if !identifier.MatchString(m.StorageID) || m.SizeBytes < 32774 || err != nil || len(h) != 32 || strings.ToLower(m.SHA256) != m.SHA256 {
			return errors.New("invalid ISO storage, size or SHA-256")
		}
	case "vm-create", "vm-update":
		if c.VM == nil {
			return errors.New("VM payload required")
		}
		v := c.VM
		id, pool, host = v.ID, v.PoolID, v.HostID
		if !uuidPattern.MatchString(id) || v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
			return errors.New("invalid VM identity, vCPU count or memory")
		}
		// Creation is revision 0; an edit names the revision it produces and never starts a guest.
		if (c.Kind == "vm-create" && v.Revision != 0) || (c.Kind == "vm-update" && (v.Revision < 1 || v.StartAfterCreate)) {
			return errors.New("invalid VM definition revision")
		}
		// Shape and bounds come from the specification both sides enforce; only host-side
		// reference syntax is checked here.
		if err := protocol.ValidateVMSpec(*v); err != nil {
			return err
		}
		for _, d := range v.Disks {
			if !identifier.MatchString(d.StorageID) {
				return errors.New("invalid disk storage reference")
			}
		}
		for _, n := range v.NICs {
			if !identifier.MatchString(n.NetworkID) {
				return errors.New("invalid network reference")
			}
		}
		if v.MediaID != "" && !identifier.MatchString(v.MediaID) {
			return errors.New("invalid install media reference")
		}
		if v.DriversMediaID != "" && !identifier.MatchString(v.DriversMediaID) {
			return errors.New("invalid driver disc reference")
		}
	case "vm-power":
		if c.Power == nil {
			return errors.New("power payload required")
		}
		p := c.Power
		id, pool, host = p.VMID, p.PoolID, p.HostID
		if !uuidPattern.MatchString(p.VMID) || !protocol.ValidPowerAction(p.Action) {
			return errors.New("invalid VM identity or power action")
		}
		if p.AfterOperationID != "" && (!identifier.MatchString(p.AfterOperationID) || p.AfterOperationID == c.OperationID) {
			return errors.New("invalid predecessor operation ID")
		}
	case "vm-delete":
		if c.Delete == nil {
			return errors.New("delete payload required")
		}
		d := c.Delete
		id, pool, host = d.VMID, d.PoolID, d.HostID
		if !uuidPattern.MatchString(d.VMID) {
			return errors.New("invalid VM identity")
		}
	case "vm-media":
		if c.VMMedia == nil {
			return errors.New("media-change payload required")
		}
		m := c.VMMedia
		id, pool, host = m.VMID, m.PoolID, m.HostID
		if !uuidPattern.MatchString(m.VMID) || !protocol.ValidMediaAction(m.Action) {
			return errors.New("invalid VM identity or media action")
		}
		if !protocol.ValidMediaDrive(m.Drive) {
			return errors.New("invalid CD drive")
		}
		if m.Action == "insert" && !identifier.MatchString(m.MediaID) {
			return errors.New("insert requires a media identity")
		}
		if m.Action == "eject" && m.MediaID != "" {
			return errors.New("eject must not name media")
		}
	default:
		return errors.New("unsupported managed command")
	}
	if ((c.Kind != "storage-register" || id != "") && !identifier.MatchString(id)) || !identifier.MatchString(pool) || (c.Kind != "storage-zfs-provision" && !identifier.MatchString(host)) {
		return errors.New("invalid resource scope")
	}
	return nil
}

// ValidateManagedCommand applies the complete no-side-effect managed-command
// validation used by Execute. Admission code uses it before reserving a remote
// pool transition, so invalid resource references cannot leave a master-owned
// pending update without a native journal from which to reconcile it.
func ValidateManagedCommand(c protocol.Command) error {
	return validateManaged(c)
}

// Privileged writes must never traverse a directory replaceable by an ordinary user.
// Require ordinary execute permission so QEMU can traverse without special ACLs.
func managedStoragePermissions(uid uint32, mode os.FileMode) bool {
	return uid == 0 && mode.Perm()&0022 == 0 && mode.Perm()&0001 != 0
}
func trustedManagedPath(path string) error {
	for p := path; ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !ok || !managedStoragePermissions(st.Uid, info.Mode()) {
			return errors.New("storage ancestors must be root-owned directories, executable by others for QEMU, without group or other write access")
		}
		if p == "/" {
			return nil
		}
	}
}
func localStorage(path string, required uint64) error {
	if err := trustedManagedPath(path); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("storage is unavailable or uses symlinks")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("storage must be an existing directory")
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(path, &stat); err != nil {
		return err
	}
	switch uint64(stat.Type) {
	case 0xef53, 0x58465342, 0x9123683e:
	default:
		return errors.New("storage must use local ext4, XFS or Btrfs")
	}
	if stat.Bavail*uint64(stat.Bsize) < required {
		return errors.New("insufficient free storage including reserve")
	}
	return nil
}

// managedStorageReady is the sole path from a durable repository record to a
// writable directory.  In particular, remote storage may not silently fall
// back to an empty mountpoint after a mount/session is lost.
func managedStorageReady(ctx context.Context, s protocol.StorageRegistration, required uint64) (storageAccess, error) {
	if protocol.IsClusteredISCSI(s) {
		config, ok := StorageClusterConfig(ctx)
		if !ok {
			return storageAccess{}, errors.New("clustered iSCSI requires Core cluster validation before VM use")
		}
		stateDir, ok := storageClusterStateDir(ctx)
		if !ok {
			return storageAccess{}, errors.New("clustered iSCSI requires the local Core state directory before VM use")
		}
		a, err := ValidateClusteredISCSIAt(ctx, stateDir, s, config)
		if err != nil {
			return storageAccess{}, err
		}
		if a.ReadOnly || a.Available < required {
			return storageAccess{}, errors.New("clustered iSCSI is read-only or lacks reserve capacity")
		}
		return a, nil
	}
	a, err := storageInspect(ctx, s)
	if err != nil {
		return storageAccess{}, err
	}
	if a.ReadOnly {
		return storageAccess{}, errors.New("storage is read-only")
	}
	if a.Path == "" || (s.Path != "" && filepath.Clean(a.Path) != s.Path) {
		return storageAccess{}, errors.New("storage mount identity does not match its registered path")
	}
	if err = storageVerify(ctx, s, a); err != nil {
		return storageAccess{}, err
	}
	if a.Available < required {
		return storageAccess{}, errors.New("insufficient free storage including reserve")
	}
	return a, nil
}

// Test hooks keep admission/rollback coverage free of a full native prepare. Production
// keeps the real prepare and readiness helpers.
var (
	storagePrepareAtFn    = storagePrepareAt
	managedStorageReadyFn = managedStorageReady
)

type expectedStorageIdentityContextKey struct{}
type storageClusterConfigContextKey struct{}
type poolStorageExpectation struct {
	identity   protocol.StorageIdentity
	generation uint64
}

// WithExpectedStorageIdentity carries a trusted peer master's observation into the
// private storage transaction. It is not a Controller command field: public commands
// remain unable to supply Core-observed mount identities.
func WithExpectedStorageIdentity(ctx context.Context, identity protocol.StorageIdentity) context.Context {
	return WithPoolStorageExpectation(ctx, identity, 0)
}

// WithPoolStorageExpectation additionally carries a trusted master generation for a
// first member import. Public storage-register commands still start at generation one.
func WithPoolStorageExpectation(ctx context.Context, identity protocol.StorageIdentity, generation uint64) context.Context {
	return context.WithValue(ctx, expectedStorageIdentityContextKey{}, poolStorageExpectation{identity: identity, generation: generation})
}

// managedStoragePrepare is reserved for registration and configuration changes. Ordinary
// lifecycle reads must inspect an existing mount/session and never replay login or mount work.
// Native preparation is provisional until readiness and capacity succeed; otherwise Core
// tears the attachment down so a refused registration cannot leave sessions or mounts behind.
func (e *ManagedEngine) managedStoragePrepare(ctx context.Context, s protocol.StorageRegistration, required uint64) (storageAccess, error) {
	if protocol.IsClusteredISCSI(s) {
		// The normal iSCSI path has a single-host ownership model.  Do not mount a
		// GFS2 LUN until the cluster layer supplies its validated Core-owned config.
		config, ok := StorageClusterConfig(ctx)
		if !ok {
			return storageAccess{}, errors.New("clustered iSCSI requires Core cluster preparation")
		}
		a, err := PrepareClusteredISCSI(ctx, filepath.Dir(e.stateDir), s, config)
		if err != nil {
			return storageAccess{}, err
		}
		if a.ReadOnly || a.Available < required {
			return storageAccess{}, errors.New("clustered iSCSI is read-only or lacks reserve capacity")
		}
		return a, nil
	}
	credentialRoot := filepath.Join(filepath.Dir(e.stateDir), credentialDirectory)
	prepared, err := storagePrepareAtFn(ctx, s, credentialRoot)
	if err != nil {
		return storageAccess{}, err
	}
	verified := s
	verified.Path, verified.Identity = prepared.Path, prepared.Observed
	if expectation, ok := ctx.Value(expectedStorageIdentityContextKey{}).(poolStorageExpectation); ok && !StorageIdentityMatchesExpected(expectation.identity, prepared.Observed) {
		if cleanupErr := storageDetachAt(context.Background(), verified, credentialRoot); cleanupErr != nil {
			return storageAccess{}, errors.Join(errors.New("prepared repository identity does not match master observation"), fmt.Errorf("%w: clean up rejected storage preparation: %v", ErrStorageRollbackUncertain, cleanupErr))
		}
		return storageAccess{}, errors.New("prepared repository identity does not match master observation")
	}
	access, err := managedStorageReadyFn(ctx, verified, required)
	if err == nil {
		return access, nil
	}
	if cleanupErr := storageDetachAt(context.Background(), verified, credentialRoot); cleanupErr != nil {
		return storageAccess{}, errors.Join(err, fmt.Errorf("%w: clean up rejected storage preparation: %v", ErrStorageRollbackUncertain, cleanupErr))
	}
	return storageAccess{}, err
}

// StorageIdentityMatchesExpected compares Core observations using the durable backing
// identity. In particular, a marker-backed SMB share deliberately ignores CIFS's
// reconnect-varying filesystem diagnostic while requiring its stable marker.
func StorageIdentityMatchesExpected(expected, actual protocol.StorageIdentity) bool {
	return storageSameBacking(expected, actual)
}

// registerStorage keeps a successful native prepare provisional until post-prepare
// conflict checks and the writable probe succeed. Cleanup failures become uncertain.
func (e *ManagedEngine) registerStorage(ctx context.Context, j *managedJournal, js []managedJournal, c protocol.Command) (retErr error) {
	requested := *c.Storage
	if j.AppliedStorage != nil {
		requested.ID = j.AppliedStorage.ID
	}
	storageCorePaths(&requested)
	if err := restoreClusteredRegistrationPath(ctx, &requested); err != nil {
		return err
	}
	if requested.CredentialReceipt != "" {
		if err := bindCredentialReceipt(filepath.Dir(e.stateDir), requested.CredentialReceipt, requested.ID, requested.PoolID, requested.HostID, requested.CredentialRequestID); err != nil {
			return err
		}
		requested.CredentialPresent = true
	}
	if requested.Path != "" && (requested.Path == e.stateDir || strings.HasPrefix(e.stateDir, requested.Path+"/") || strings.HasPrefix(requested.Path, e.stateDir+"/")) {
		return errors.New("storage must be separate from agent state")
	}
	conflict := c
	conflict.Storage = &requested
	if err := registrationConflict(js, conflict); err != nil {
		return err
	}
	access, err := e.managedStoragePrepare(ctx, requested, 1<<30)
	if err != nil {
		return err
	}
	// Source aliases can only be proven after Core observes the mounted backing
	// identity. Keep the attachment provisional until every post-prepare check and
	// the writable probe succeed; otherwise tear it down before returning.
	observed := requested
	observed.Path, observed.Identity = access.Path, access.Observed
	credentialRoot := filepath.Join(filepath.Dir(e.stateDir), credentialDirectory)
	committed := false
	defer func() {
		if committed {
			return
		}
		if cleanupErr := storageDetachAt(context.Background(), observed, credentialRoot); cleanupErr != nil {
			retErr = errors.Join(retErr, fmt.Errorf("%w: clean up rejected storage registration: %v", ErrStorageRollbackUncertain, cleanupErr))
		}
	}()
	postPrepare := c
	postPrepare.Storage = &observed
	if err := registrationConflict(js, postPrepare); err != nil {
		return err
	}
	if access.Path == e.stateDir || strings.HasPrefix(e.stateDir, access.Path+"/") || strings.HasPrefix(access.Path, e.stateDir+"/") {
		return errors.New("storage must be separate from agent state")
	}
	f, err := os.CreateTemp(access.Path, ".nex-access-")
	if err != nil {
		return errors.New("storage is not writable")
	}
	name := f.Name()
	err = f.Sync()
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	removeErr := os.Remove(name)
	if err != nil {
		return err
	}
	if removeErr != nil {
		return removeErr
	}
	applied := requested
	applied.Path = access.Path
	if expectation, ok := ctx.Value(expectedStorageIdentityContextKey{}).(poolStorageExpectation); ok && expectation.generation != 0 {
		applied.Generation = expectation.generation
	} else {
		applied.Generation = 1
	}
	applied.Identity = access.Observed
	applied.Capabilities = storageCapabilities(applied)
	applied.Access = protocol.StorageAccess{Status: access.Health, Readable: true, Writable: !access.ReadOnly, TotalBytes: access.Total, AvailableBytes: &access.Available, ObservedAt: time.Now().UTC()}
	j.AppliedStorage = &applied
	committed = true
	return nil
}

// restoreClusteredRegistrationPath is the sole exception to public path
// sanitization: members recover the GFS2 mount path only from the validated,
// Core-private cluster profile carried in context.
func restoreClusteredRegistrationPath(ctx context.Context, s *protocol.StorageRegistration) error {
	if !protocol.IsClusteredISCSI(*s) {
		return nil
	}
	config, ok := StorageClusterConfig(ctx)
	if !ok || config.ValidFor(*s) != nil || s.Source.ISCSI == nil || s.Source.ISCSI.MountPath != config.MountPath {
		return errors.New("clustered iSCSI needs the matching Core cluster configuration")
	}
	s.Path = config.MountPath
	return nil
}

func storageRegistrationPath(s protocol.StorageRegistration) string {
	if s.Path != "" {
		return s.Path
	}
	if s.Source.ZFS != nil {
		return s.Source.ZFS.MountPath
	}
	if s.Source.ISCSI != nil {
		return s.Source.ISCSI.MountPath
	}
	return filepath.Join(storageMountRoot, s.ID)
}

// storageCorePaths fills paths Core owns rather than accepting them from a client. The command
// journal keeps the original request; callers use this execution copy for native work.
func storageCorePaths(s *protocol.StorageRegistration) {
	if s.Source.ISCSI != nil && s.Source.ISCSI.MountPath == "" {
		source := *s.Source.ISCSI
		source.MountPath = filepath.Join(storageMountRoot, s.ID)
		s.Source.ISCSI = &source
	}
}

func storageCapabilities(s protocol.StorageRegistration) protocol.StorageCapabilities {
	thick := s.StorageKind() == protocol.StorageKindLocal || s.StorageKind() == protocol.StorageKindNFS || s.StorageKind() == protocol.StorageKindISCSI
	caps := protocol.StorageCapabilities{VMDisk: true, ISO: true, Thin: true, Thick: thick, EligibleHostIDs: []string{s.HostID}}
	if s.ZFSProvisioned {
		// Pool-local ZFS has per-host paths. This slice has no host-targeted
		// media placement contract, so advertising ISO would promise a source
		// that may only exist on another host.
		caps.ISO = false
	}
	if protocol.IsClusteredISCSI(s) {
		caps.Shared = true
		caps.EligibleHostIDs = append([]string(nil), s.Source.ISCSI.EligibleHostIDs...)
	}
	return caps
}

func normalizeStorageCapabilities(s *protocol.StorageRegistration) {
	if protocol.IsClusteredISCSI(*s) && !s.Capabilities.VMDisk {
		s.Capabilities = storageCapabilities(*s)
	}
}

func poolSharedVMStorage(s protocol.StorageRegistration) bool {
	return protocol.IsPoolSharedStorage(s)
}
func managedUsesPoolSharedStorage(js []managedJournal, v protocol.VMCreateSpec) bool {
	for _, disk := range v.Disks {
		storage, err := managedStorage(js, disk.StorageID)
		if err == nil && poolSharedVMStorage(storage) {
			return true
		}
	}
	return false
}

func managedStorageByID(ctx context.Context, js []managedJournal, id string, required uint64) (protocol.StorageRegistration, storageAccess, error) {
	s, err := managedStorage(js, id)
	if err != nil {
		return protocol.StorageRegistration{}, storageAccess{}, err
	}
	a, err := managedStorageReadyFn(ctx, s, required)
	return s, a, err
}

// ensureVMStorage rechecks every repository immediately before a guest can
// start. A controller report is only an observation; libvirt must never start
// a guest against a replaced or missing mount.
func ensureVMStorage(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec) error {
	seen := map[string]bool{}
	for _, d := range v.Disks {
		if seen[d.StorageID] {
			continue
		}
		seen[d.StorageID] = true
		if _, err := managedStorage(js, d.StorageID); err != nil {
			// Older host-local domains predate repository records. They are guarded by
			// libvirt's existing local path checks and are not a storage repository.
			continue
		}
		if _, _, err := managedStorageByID(ctx, js, d.StorageID, 0); err != nil {
			return err
		}
	}
	return nil
}
func managedVirsh(ctx context.Context, args ...string) ([]byte, error) {
	return (&Engine{config: Config{VirshURI: "qemu:///system"}}).virsh(ctx, args...)
}

// ManagedVMState is a narrow local observation used to confirm a stop or start to
// the pool master.  It does not authorize anything and treats an unavailable domain as unknown.
func ManagedVMState(ctx context.Context, id string) (string, error) {
	if !uuidPattern.MatchString(id) {
		return "", errors.New("invalid VM identity")
	}
	b, err := managedVirsh(ctx, "domstate", id)
	if err != nil {
		return "", err
	}
	state := strings.TrimSpace(string(b))
	if state == domainAsleep {
		// The guest put itself to sleep (ACPI S3). Its QEMU process is alive and holds
		// the disks, so for ownership and inventory it is running; powerVM wakes it.
		return "running", nil
	}
	if state != "running" && state != "shut off" {
		return "", errors.New("VM state is not confirmable")
	}
	return state, nil
}

// domainAsleep is the virsh domstate of a guest that entered ACPI S3 by itself.
const domainAsleep = "pmsuspended"

// ManagedVMAsleep reports whether the domain is observed pmsuspended. Callers that admit a
// start use it to ask the pool master for a wake instead of a second execution.
func ManagedVMAsleep(ctx context.Context, id string) (bool, error) {
	if !uuidPattern.MatchString(id) {
		return false, errors.New("invalid VM identity")
	}
	b, err := managedVirsh(ctx, "domstate", id)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(b)) == domainAsleep, nil
}
func verifyNetwork(ctx context.Context, n protocol.NetworkRegistration) error {
	if n.Kind == "vlan" {
		return verifyGuestBridge(n)
	}
	if n.Kind == "bridge" {
		if _, err := os.Stat(filepath.Join("/sys/class/net", n.Source, "bridge")); err != nil {
			return errors.New("selected bridge does not exist; configure networking separately")
		}
		return nil
	}
	b, err := managedVirsh(ctx, "net-list", "--name")
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(b)) {
		if name == n.Source {
			return nil
		}
	}
	return errors.New("selected libvirt network is not active")
}

// Host-side references match by resource ID only: pool and host IDs in the journal belong
// to whichever Controller issued the command and change when the host is re-enrolled.
func managedStorage(js []managedJournal, id string) (protocol.StorageRegistration, error) {
	if s, ok := storageProjection(js)[id]; ok {
		return s, nil
	}
	return protocol.StorageRegistration{}, errors.New("storage is not registered on this host")
}

func storageWasPrepared(js []managedJournal, id string) bool {
	for _, j := range js {
		// New repository registrations keep the caller's empty ID in Command and
		// place Core's allocated ID in AppliedStorage.  Match that projection first
		// so media and VM checks re-inspect the actual repository instead of treating
		// it as an old unprepared local record.
		if j.AppliedStorage != nil && j.AppliedStorage.ID == id {
			return true
		}
		if j.Command.Storage != nil && j.Command.Storage.ID == id && j.AppliedStorage != nil {
			return true
		}
	}
	return false
}

// storageProjection folds successful journal events by generation.  A detach is a tombstone,
// so an older successful registration cannot reappear after Controller replacement.
func storageProjection(js []managedJournal) map[string]protocol.StorageRegistration {
	out := map[string]protocol.StorageRegistration{}
	tombstone := map[string]uint64{}
	for _, j := range js {
		if j.Result.Status != "succeeded" {
			continue
		}
		if s := j.Command.Storage; s != nil {
			copy := *s
			if j.AppliedStorage != nil {
				copy = *j.AppliedStorage
			}
			if copy.Generation == 0 {
				copy.Generation = 1
			}
			// The command carries the Controller's request state ("queued"); only
			// succeeded entries reach here, so it is never an observation.
			copy.OperationID, copy.Status, copy.Error = "", "", ""
			normalizeStorageCapabilities(&copy)
			if copy.Generation > tombstone[copy.ID] {
				if prior, ok := out[copy.ID]; !ok || copy.Generation >= prior.Generation {
					out[copy.ID] = copy
				}
			}
		}
	}
	// Journal filenames are operation IDs, not an event sequence. Repeated passes apply the
	// generation chain independently of directory order; each successful pass advances one
	// repository and the bounded loop cannot spin on malformed history.
	for pass := 0; pass < len(js); pass++ {
		changed := false
		for _, j := range js {
			if j.Result.Status != "succeeded" || j.Command.StorageLifecycle == nil || ejectCleanupConsumed(js, &j) || ejectCleanupSuperseded(js, &j) {
				continue
			}
			v := j.Command.StorageLifecycle
			s, known := out[v.RepositoryID]
			if !known || s.Generation != v.ExpectedGeneration {
				continue
			}
			switch v.Action {
			case "update":
				if v.Registration == nil {
					continue
				}
				next := *v.Registration
				if j.AppliedStorage != nil {
					next = *j.AppliedStorage
				}
				next.ID, next.PoolID, next.HostID, next.Generation = v.RepositoryID, v.PoolID, v.HostID, s.Generation+1
				if v.CredentialReceipt != "" {
					next.CredentialReceipt, next.CredentialPresent = v.CredentialReceipt, true
				}
				out[v.RepositoryID], changed = next, true
			case "detach":
				delete(out, v.RepositoryID)
				tombstone[v.RepositoryID], changed = s.Generation+1, true
			}
		}
		if !changed {
			break
		}
	}
	return out
}

func storageTombstones(js []managedJournal) map[string]protocol.StorageRegistration {
	out := map[string]protocol.StorageRegistration{}
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.StorageLifecycle == nil || j.Command.StorageLifecycle.Action != "detach" || ejectCleanupConsumed(js, &j) || ejectCleanupSuperseded(js, &j) {
			continue
		}
		v := j.Command.StorageLifecycle
		if prior, ok := out[v.RepositoryID]; ok && prior.Generation >= v.ExpectedGeneration+1 {
			continue
		}
		out[v.RepositoryID] = protocol.StorageRegistration{ID: v.RepositoryID, PoolID: v.PoolID, HostID: v.HostID, Generation: v.ExpectedGeneration + 1, Tombstone: true}
	}
	return out
}

func storageCredentialAttached(js []managedJournal, receipt string) bool {
	if receipt == "" {
		return false
	}
	for _, s := range storageProjection(js) {
		if s.CredentialReceipt == receipt {
			return true
		}
	}
	for _, j := range js {
		if j.Result.Status != "pending" && j.Result.Status != "needs_attention" {
			continue
		}
		if j.Command.Storage != nil && j.Command.Storage.CredentialReceipt == receipt || j.Command.StorageLifecycle != nil && j.Command.StorageLifecycle.CredentialReceipt == receipt {
			return true
		}
	}
	return false
}

// storageDependencies names durable references which make detach unsafe.  Retained disks
// count too: an edit must never turn unregister into data loss by hiding an old disk.
func storageDependencies(js []managedJournal, id string) []string {
	var out []string
	for vmID, v := range currentDefinitions(js) {
		if slices.ContainsFunc(v.Disks, func(d protocol.VMDisk) bool { return d.StorageID == id }) {
			out = append(out, "VM "+vmID)
		}
	}
	// A succeeded wipe delete removed every retained path of its VM (managedDeleteDiskPaths).
	wiped := map[string]bool{}
	for _, j := range js {
		if j.Command.Kind == "vm-delete" && j.Result.Status == "succeeded" && j.Command.Delete != nil && j.Command.Delete.DeleteDisks {
			wiped[j.Command.Delete.VMID] = true
		}
	}
	// A removed ISO is not a reference any more. Its import entry stays succeeded for ever,
	// so without the tombstones one ISO ever imported onto a repository would block its
	// detach permanently — the same shape as the wiped retained disks above.
	removedMedia := mediaTombstones(js)
	for _, j := range js {
		if j.Result.Status != "succeeded" {
			continue
		}
		if j.Command.Media != nil && j.Command.Media.StorageID == id {
			if _, removed := removedMedia[j.Command.Media.ID]; !removed {
				out = append(out, "ISO "+j.Command.Media.ID)
			}
		}
		if len(j.Retained) == 0 {
			continue
		}
		vmID := ""
		switch {
		case j.Command.VM != nil:
			vmID = j.Command.VM.ID
		case j.Command.Delete != nil:
			vmID = j.Command.Delete.VMID
		}
		if vmID == "" || wiped[vmID] {
			continue
		}
		s, err := managedStorage(js, id)
		if err == nil {
			for _, path := range j.Retained {
				if path == s.Path || strings.HasPrefix(path, s.Path+"/") {
					out = append(out, "retained disk for VM "+vmID)
					break
				}
			}
		}
	}
	return out
}

func credentialRotationSafe(ctx context.Context, js []managedJournal, id string) error {
	for vmID, v := range currentDefinitions(js) {
		if !slices.ContainsFunc(v.Disks, func(d protocol.VMDisk) bool { return d.StorageID == id }) {
			continue
		}
		state, err := managedVirsh(ctx, "domstate", vmID)
		if err != nil || strings.TrimSpace(string(state)) != "shut off" {
			return errors.New("stop every VM using this storage before rotating credentials")
		}
	}
	for _, j := range js {
		if j.Result.Status != "pending" && j.Result.Status != "needs_attention" {
			continue
		}
		if j.Command.VM != nil && (j.Command.Kind == "vm-create" || j.Command.Kind == "vm-update") && slices.ContainsFunc(j.Command.VM.Disks, func(d protocol.VMDisk) bool { return d.StorageID == id }) {
			return errors.New("storage has a pending VM disk operation")
		}
		if j.Command.VMMedia != nil {
			return errors.New("storage has a pending media operation")
		}
		if j.Command.Media != nil && j.Command.Media.StorageID == id {
			return errors.New("storage has a pending ISO operation")
		}
	}
	return nil
}

// networkProjection folds successful network events by generation: a rename advances the
// generation and changes only the name, a removal is a tombstone, so neither an older
// registration nor a pre-rename name can reappear after Controller replacement.
func networkProjection(js []managedJournal) map[string]protocol.NetworkRegistration {
	out := map[string]protocol.NetworkRegistration{}
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.Kind != "network-register" || j.Command.Network == nil {
			continue
		}
		n := *j.Command.Network
		n.OperationID, n.Status, n.Error = "", "", ""
		if prior, ok := out[n.ID]; !ok || protocol.NetworkGeneration(n) >= protocol.NetworkGeneration(prior) {
			out[n.ID] = n
		}
	}
	// Journal filenames are operation IDs, not an event sequence. Repeated passes apply the
	// generation chain independently of directory order; each pass advances one network and
	// the bounded loop cannot spin on malformed history.
	for pass := 0; pass < len(js); pass++ {
		changed := false
		for _, j := range js {
			if j.Result.Status != "succeeded" || j.Command.ResourceLifecycle == nil || j.Command.ResourceLifecycle.Resource != "network" || ejectCleanupConsumed(js, &j) || ejectCleanupSuperseded(js, &j) {
				continue
			}
			v := j.Command.ResourceLifecycle
			n, known := out[v.ResourceID]
			if !known || protocol.NetworkGeneration(n) != v.ExpectedGeneration {
				continue
			}
			switch v.Action {
			case "update":
				n.Name, n.Generation = v.Name, v.ExpectedGeneration+1
				out[v.ResourceID], changed = n, true
			case "remove":
				delete(out, v.ResourceID)
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	return out
}

// networkTombstones names the networks this host durably removed, as Controller mirror rows.
func networkTombstones(js []managedJournal) map[string]protocol.NetworkRegistration {
	return resourceTombstones(js, "network", func(v protocol.ResourceLifecycleRequest) protocol.NetworkRegistration {
		return protocol.NetworkRegistration{ID: v.ResourceID, PoolID: v.PoolID, HostID: v.HostID, Generation: v.ExpectedGeneration + 1, Tombstone: true}
	})
}

// mediaTombstones names the imported ISOs this host durably removed.
func mediaTombstones(js []managedJournal) map[string]protocol.Media {
	return resourceTombstones(js, "media", func(v protocol.ResourceLifecycleRequest) protocol.Media {
		return protocol.Media{ID: v.ResourceID, PoolID: v.PoolID, HostID: v.HostID, Generation: v.ExpectedGeneration + 1, Tombstone: true}
	})
}

func resourceTombstones[T any](js []managedJournal, resource string, row func(protocol.ResourceLifecycleRequest) T) map[string]T {
	out, at := map[string]T{}, map[string]uint64{}
	for _, j := range js {
		v := j.Command.ResourceLifecycle
		if j.Result.Status != "succeeded" || v == nil || v.Resource != resource || v.Action != "remove" || ejectCleanupConsumed(js, &j) || ejectCleanupSuperseded(js, &j) {
			continue
		}
		if prior, ok := at[v.ResourceID]; ok && prior >= v.ExpectedGeneration+1 {
			continue
		}
		out[v.ResourceID], at[v.ResourceID] = row(*v), v.ExpectedGeneration+1
	}
	return out
}

func ejectCleanupConsumed(js []managedJournal, removal *managedJournal) bool {
	if removal.EjectCleanup == nil || removal.Result.Status != "succeeded" {
		return false
	}
	for _, candidate := range js {
		if candidate.Result.Status != "succeeded" || candidate.EjectCleanupReapply == nil || candidate.EjectCleanupReapply.RemovalOperationID != removal.Command.OperationID || candidate.EjectCleanupReapply.MembershipGeneration <= removal.EjectCleanup.MembershipGeneration {
			continue
		}
		resource, id, poolID, hostID, generation, ok := cleanupResource(candidate.Command)
		removedResource, removedID, removedGeneration, removed := lifecycleResource(removal.Command)
		if ok && removed && resource == removedResource && id == removedID && poolID == removal.EjectCleanup.PoolID && hostID == removal.EjectCleanup.HostID && generation >= removedGeneration {
			return true
		}
	}
	return false
}

// A later signed eject is a later lifecycle event even when the master retained the
// resource's generation. Its cleanup supersedes an older cleanup on this host; otherwise a
// deterministic media-adoption receipt rewritten for the newer cycle would resurrect the old
// tombstone in projections.
func ejectCleanupSuperseded(js []managedJournal, removal *managedJournal) bool {
	if removal.EjectCleanup == nil || removal.Result.Status != "succeeded" {
		return false
	}
	resource, id, generation, ok := lifecycleResource(removal.Command)
	if !ok {
		return false
	}
	for _, candidate := range js {
		otherResource, otherID, otherGeneration, removed := lifecycleResource(candidate.Command)
		if !removed || candidate.Result.Status != "succeeded" || candidate.EjectCleanup == nil || otherResource != resource || otherID != id {
			continue
		}
		if candidate.EjectCleanup.PoolID == removal.EjectCleanup.PoolID && candidate.EjectCleanup.HostID == removal.EjectCleanup.HostID &&
			(candidate.EjectCleanup.MembershipGeneration > removal.EjectCleanup.MembershipGeneration ||
				(candidate.EjectCleanup.MembershipGeneration == removal.EjectCleanup.MembershipGeneration && otherGeneration > generation)) {
			return true
		}
	}
	return false
}

// networkDependencies names the VM definitions this host holds that attach the network.
func networkDependencies(js []managedJournal, id string) []string {
	var out []string
	for vmID, v := range currentDefinitions(js) {
		if slices.ContainsFunc(v.NICs, func(n protocol.VMNIC) bool { return n.NetworkID == id }) {
			out = append(out, vmID)
		}
	}
	slices.Sort(out)
	return out
}

// mediaDependencies names the VM definitions this host holds that hold the ISO in a CD drive.
func mediaDependencies(js []managedJournal, id string) []string {
	var out []string
	for vmID, v := range currentDefinitions(js) {
		if v.MediaID == id || v.DriversMediaID == id {
			out = append(out, vmID)
		}
	}
	slices.Sort(out)
	return out
}

// NetworkDependencies reports local VM definitions that would refuse this network's removal.
// Pool convergence calls it as a precheck, so a persistent refusal writes no journal entry.
func (e *ManagedEngine) NetworkDependencies(id string) []string {
	js, _ := e.readable()
	return networkDependencies(js, id)
}

// MediaDependencies is the same precheck for an imported ISO.
func (e *ManagedEngine) MediaDependencies(id string) []string {
	js, _ := e.readable()
	return mediaDependencies(js, id)
}

// networkRemovalRefusal is the whole refusal set of a network-remove for a network this host
// holds: the VM dependency and every native precondition of the teardown. apply calls it
// immediately before the teardown and CheckNetworkRemoval calls it with no effect at all, so
// the dry run and the real thing cannot drift.
func (e *ManagedEngine) networkRemovalRefusal(js []managedJournal, n protocol.NetworkRegistration) error {
	if !protocol.IsPoolSharedNetwork(n) {
		// Every host re-registers its automatic untagged networks once a minute
		// (RegisterDefaults), so removing one would only flap.
		return errors.New("only pool VLAN networks can be removed")
	}
	if deps := networkDependencies(js, n.ID); len(deps) > 0 {
		return errors.New("network cannot be removed while used by VM " + strings.Join(deps, ", "))
	}
	return checkGuestBridgeRemovable(n, e.networkDir)
}

// CheckNetworkRemoval reports why this host would refuse to remove the pool network, without
// journalling, locking or changing anything. Pool convergence calls it before Execute: a
// removal that is refused on every probe tick would otherwise mint a journal entry per tick
// and reach the retained-entry cap, after which every managed operation on the host fails.
func (e *ManagedEngine) CheckNetworkRemoval(ctx context.Context, id string) error {
	js, bad := e.readable()
	if len(bad) != 0 {
		return ErrManagedJournalIncomplete
	}
	n, err := managedNetwork(js, id)
	if err != nil {
		return err
	}
	return e.networkRemovalRefusal(js, n)
}

// mediaRemovalRefusal is the same for an imported ISO. The unlink itself has no precondition
// beyond the dependency: an absent file is a completed removal.
func mediaRemovalRefusal(js []managedJournal, id string) error {
	if deps := mediaDependencies(js, id); len(deps) > 0 {
		return errors.New("ISO cannot be removed while attached to VM " + strings.Join(deps, ", "))
	}
	return nil
}

// storageDetachRefusal is the whole durable-dependency refusal of a storage-detach. The
// detach itself calls it and CheckStorageDetach calls it with no effect at all, so the dry
// run and the real thing cannot drift.
func storageDetachRefusal(js []managedJournal, id string) error {
	if deps := storageDependencies(js, id); len(deps) > 0 {
		return errors.New("storage cannot detach while used by " + strings.Join(deps, ", "))
	}
	return nil
}

// CheckStorageDetach is CheckNetworkRemoval for a repository: it reports why this host would
// refuse to detach it, without journalling, locking or changing anything. A leaving host and
// a member's orphan pass call it before Execute, so a detach that is refused on every probe
// tick mints no journal entry at all and is re-evaluated for free on the next one.
func (e *ManagedEngine) CheckStorageDetach(ctx context.Context, id string) error {
	js, bad := e.readable()
	if len(bad) != 0 {
		return ErrManagedJournalIncomplete
	}
	current, err := managedStorage(js, id)
	if err != nil {
		return err
	}
	return storageDetachRefusal(js, current.ID)
}

// CheckMediaRemoval is CheckNetworkRemoval for an imported ISO.
func (e *ManagedEngine) CheckMediaRemoval(ctx context.Context, id string) error {
	js, bad := e.readable()
	if len(bad) != 0 {
		return ErrManagedJournalIncomplete
	}
	if _, _, err := managedMedia(js, id); err != nil {
		return err
	}
	return mediaRemovalRefusal(js, id)
}

func managedNetwork(js []managedJournal, id string) (protocol.NetworkRegistration, error) {
	if n, ok := networkProjection(js)[id]; ok {
		return n, nil
	}
	return protocol.NetworkRegistration{}, errors.New("network is not registered on this host")
}

// managedMedia resolves an imported ISO and the path this host published it to.
func managedMedia(js []managedJournal, id string) (protocol.Media, string, error) {
	if _, removed := mediaTombstones(js)[id]; !removed {
		for _, j := range js {
			if j.Result.Status == "succeeded" && j.Command.Kind == "iso-import" && j.Command.Media != nil && j.Command.Media.ID == id {
				return journalMedia(j), j.MediaPath, nil
			}
		}
	}
	return protocol.Media{}, "", errors.New("ISO is not imported on this host")
}

// publishedMediaPath is the file this host wrote for an imported ISO. The CDROM only needs a
// readable path on this host; it is independent of any VM disk repository.
func publishedMediaPath(js []managedJournal, mediaID string) (protocol.Media, string, error) {
	m, path, err := managedMedia(js, mediaID)
	if err != nil {
		return protocol.Media{}, "", err
	}
	s, err := managedStorage(js, m.StorageID)
	if err != nil {
		return protocol.Media{}, "", err
	}
	if path != filepath.Join(storageRegistrationPath(s), "nex-media", m.ID+".iso") {
		return protocol.Media{}, "", errors.New("the install ISO is not at the path this host published")
	}
	return m, path, nil
}

// managedTarget is one storage registration a VM's disks land on: the space that target must
// provide for them, and the exclusive per-VM directory that reserves it.
type managedTarget struct {
	ID    string
	Path  string
	Dir   string
	Bytes uint64
}

// managedDiskPlan resolves each disk's storage target once, sums what every target must provide
// so free space is checked per target rather than per disk, and names each disk inside its
// target's exclusive VM directory. Disk index keeps names stable across targets.
func managedDiskPlan(js []managedJournal, v protocol.VMCreateSpec) ([]managedTarget, []string, error) {
	targets, at, disks := []managedTarget{}, map[string]int{}, make([]string, len(v.Disks))
	for i, d := range v.Disks {
		index, known := at[d.StorageID]
		if !known {
			s, err := managedStorage(js, d.StorageID)
			if err != nil {
				return nil, nil, err
			}
			caps := s.Capabilities
			if !caps.VMDisk { // v1 records have no explicit capabilities.
				caps = storageCapabilities(s)
			}
			if (d.Thin && !caps.Thin) || (!d.Thin && !caps.Thick) {
				return nil, nil, errors.New("storage does not support the selected disk provisioning mode")
			}
			index = len(targets)
			at[d.StorageID] = index
			// Every target keeps the same headroom a single-disk VM required.
			path := storageRegistrationPath(s)
			targets = append(targets, managedTarget{ID: d.StorageID, Path: path, Dir: filepath.Join(path, "nex-vms", v.ID), Bytes: 4 << 30})
		}
		targets[index].Bytes += uint64(d.SizeGiB) << 30
		disks[i] = filepath.Join(targets[index].Dir, fmt.Sprintf("disk-%d.qcow2", i))
	}
	return targets, disks, nil
}

// qemuCreateArgs builds one allocation. A thick disk is preallocated so the guest cannot meet
// ENOSPC on a write the host already reported as complete.
func qemuCreateArgs(path string, d protocol.VMDisk) []string {
	args := []string{"create", "-f", "qcow2"}
	if !d.Thin {
		args = append(args, "-o", "preallocation=falloc")
	}
	return append(args, path, fmt.Sprintf("%dG", d.SizeGiB))
}

// Applied lists the pool-scoped configuration this host has durably applied, for a replaced
// Controller to mirror, with the checks that qualify it. Per-operation fields are cleared.
// Terminal entries are reported as outcomes, as is a pending entry
// no longer executing in this process (an earlier process died with it) and a creation whose
// automatic start failed (outcome <operation>:start, kind vm-power, while the VM itself stays
// a normal managed VM); interrupted creations are re-observed first (see promote); VMs
// libvirt no longer defines are listed as missing.
func (e *ManagedEngine) Applied(ctx context.Context) (*protocol.ManagedState, []protocol.Check) {
	out := &protocol.ManagedState{}
	// Read and snapshot together: an entry pending on disk while its ID is in flight is
	// executing; Execute registers the ID before the pending save and drops it after the final one.
	inFlight.Lock()
	js, bad := e.readable()
	running := maps.Clone(inFlight.ids)
	inFlight.Unlock()
	// Preserve local interrupted-operation recovery, but never let heartbeat reporting probe a
	// remote repository. Remote entries remain uncertain until an explicit retry reconciles them.
	js = e.promote(ctx, js, running)
	deleted := managedDeletedVMs(js)
	for _, j := range js {
		c := j.Command
		id, name := managedIdentity(c)
		if c.Kind == "storage-register" && j.AppliedStorage != nil {
			id, name = j.AppliedStorage.ID, j.AppliedStorage.Name
		}
		outcome := protocol.ManagedOutcome{OperationID: c.OperationID, Kind: c.Kind, ID: id, Name: name, Status: j.Result.Status, Error: j.Result.Error}
		switch j.Result.Status {
		case "succeeded":
			out.Outcomes = append(out.Outcomes, outcome)
		case "failed", "needs_attention":
			out.Outcomes = append(out.Outcomes, outcome)
			continue
		case "pending":
			if !running[c.OperationID] {
				outcome.Status, outcome.Error = "needs_attention", "interrupted before completion; retry from a new request"
				out.Outcomes = append(out.Outcomes, outcome)
			}
			continue
		}
		if c.Kind == "vm-create" && c.VM.StartAfterCreate && j.Start != "" && j.Start != "started" {
			out.Outcomes = append(out.Outcomes, protocol.ManagedOutcome{OperationID: c.OperationID + ":start", Kind: "vm-power", ID: id, Name: name, Status: "failed", Error: "VM created but not started: " + j.Start})
		}
		switch c.Kind {
		// Storage, networks and media are projections: registrations folded with the
		// rename and removal events that followed them, appended after this loop.
		case "storage-register", "storage-update", "storage-recheck", "storage-detach",
			"network-register", "network-remove", "network-update", "iso-import", "media-remove":
		case "vm-create", "vm-update":
			// The current definition is the highest succeeded revision; the create entry is
			// revision 0. Succeeded deletes remove the VM from the reported inventory.
			if deleted[c.VM.ID] {
				break
			}
			replaced := false
			for i, existing := range out.VMs {
				if existing.ID == c.VM.ID {
					replaced = true
					if c.VM.Revision > existing.Revision {
						out.VMs[i] = *c.VM
					}
				}
			}
			if !replaced {
				out.VMs = append(out.VMs, *c.VM)
			}
		case "vm-delete":
			if j.Result.Status == "succeeded" && c.Delete != nil {
				out.DeletedVMs = append(out.DeletedVMs, protocol.VMTombstone{ID: c.Delete.VMID, PoolID: c.Delete.PoolID, HostID: c.Delete.HostID, Tombstone: true})
			}
		}
	}
	out.Storage, _ = appliedStorageProjection(js, nil)
	out.Networks, out.Media = appliedNetworkProjection(js), appliedMediaProjection(js)
	// Overlap the libvirt domain list with credential projection when VMs need verifying.
	// Report bounds Applied with ProbeTimeout; doing list only after all local file work meant
	// a tight deadline could expire without starting virsh (TestReportBoundsManagedLibvirtObservation).
	// Only start when out.VMs is non-empty so a tombstoned create does not re-list (delete resume).
	var domainsCh chan map[string]bool
	if len(out.VMs) > 0 {
		domainsCh = make(chan map[string]bool, 1)
		go func() { domainsCh <- domains(ctx) }()
	}
	if receipts, err := StorageCredentialReceipts(filepath.Dir(e.stateDir)); err == nil {
		for i := range receipts {
			receipts[i].RetireEligible = receipts[i].Status == "ready" && !storageCredentialAttached(js, receipts[i].Receipt)
		}
		out.CredentialReceipts = receipts
	}
	now := time.Now().UTC()
	journal := managedJournalCheck(bad, now)
	vms := protocol.Check{Name: "managed-vms", Status: "healthy", ObservedAt: now}
	if len(out.VMs) > 0 {
		defined := <-domainsCh
		if defined == nil {
			vms.Status, vms.Error = "unknown", "libvirt did not list domains; VM definitions unverified"
		} else {
			for _, v := range out.VMs {
				if !defined[strings.ToLower(v.ID)] {
					out.MissingVMs = append(out.MissingVMs, v.ID)
				}
			}
			if len(out.MissingVMs) > 0 {
				vms.Status, vms.Error = "failed", "journaled VM definitions missing from libvirt: "+strings.Join(out.MissingVMs, ", ")
			}
		}
	}
	if vms.Status == "healthy" {
		vms.LastSuccess = &now
	}
	return out, []protocol.Check{journal, vms}
}

// ReconcileStorage retries one durable unresolved storage operation using its original command.
// The managed worker calls this from its periodic lane, never from heartbeat reporting.
func (e *ManagedEngine) ReconcileStorage(ctx context.Context) error {
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return err
	}
	due := nextStorageReconcile(js, time.Now().UTC())
	if due != nil {
		_, err = e.reconcile(ctx, *due)
		return err
	}
	return nil
}

func nextStorageReconcile(js []managedJournal, now time.Time) *managedJournal {
	var due *managedJournal
	for i := range js {
		j := &js[i]
		if (j.Result.Status != "pending" && j.Result.Status != "needs_attention") || !strings.HasPrefix(j.Command.Kind, "storage-") || now.Sub(j.Result.ObservedAt) < time.Minute {
			continue
		}
		if due == nil || j.Result.ObservedAt.Before(due.Result.ObservedAt) || (j.Result.ObservedAt.Equal(due.Result.ObservedAt) && j.Command.OperationID < due.Command.OperationID) {
			due = j
		}
	}
	return due
}

// ReconcileRemoteVMCreateStopped settles one interrupted remote-storage creation only
// after the caller has revalidated its pool execution admission.  It observes the durable
// stopped definition; it never recreates disks, starts a guest, or changes pool permits.
// Remote storage is deliberately excluded from Applied's generic promotion because that
// report path cannot make the required current pool-owner and storage-readiness checks.
func (e *ManagedEngine) ReconcileRemoteVMCreateStopped(ctx context.Context, validate func(context.Context, protocol.VMCreateSpec) error, beforeSettle func(protocol.VMCreateSpec) error) error {
	if err := ctx.Err(); err != nil {
		return err
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
	due := nextRemoteVMCreateReconcile(js, time.Now().UTC())
	if due == nil {
		return nil
	}
	// Re-read under the journal lock. A concurrent command may have settled it
	// after selection, and a stale snapshot must never be promoted.
	current, err := e.journals()
	if err != nil {
		return err
	}
	due = nextRemoteVMCreateReconcile(current, time.Now().UTC())
	if due == nil {
		return nil
	}
	j := *due
	if err = validate(ctx, *j.Command.VM); err == nil {
		if err = ctx.Err(); err != nil {
			// Do not let a validation seam that returned after its deadline turn an
			// ambiguous definition into a healthy outcome.
			j.Result.Status, j.Result.Error = "needs_attention", err.Error()
			j.Result.ObservedAt = time.Now().UTC()
			return e.save(j)
		}
		err = e.observer()(ctx, j, "shut off")
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && beforeSettle != nil {
		err = beforeSettle(*j.Command.VM)
		if err == nil {
			err = ctx.Err()
		}
	}
	if err == nil {
		j.Result.Status, j.Result.Error, j.Stage = "succeeded", "", "defined"
		if j.Command.VM.StartAfterCreate && j.Start == "" {
			j.Start = startInterrupted
		}
	} else {
		j.Result.Status, j.Result.Error = "needs_attention", err.Error()
	}
	j.Result.ObservedAt = time.Now().UTC()
	return e.save(j)
}

func nextRemoteVMCreateReconcile(js []managedJournal, now time.Time) *managedJournal {
	var due *managedJournal
	for i := range js {
		j := &js[i]
		if j.Command.Kind != "vm-create" || j.Command.VM == nil || (j.Stage != "defining" && j.Stage != "defined") || (j.Result.Status != "pending" && j.Result.Status != "needs_attention") || !promotionUsesRemoteStorage(js, *j) || now.Sub(j.Result.ObservedAt) < time.Minute {
			continue
		}
		if due == nil || j.Result.ObservedAt.Before(due.Result.ObservedAt) || (j.Result.ObservedAt.Equal(due.Result.ObservedAt) && j.Command.OperationID < due.Command.OperationID) {
			due = j
		}
	}
	return due
}

// ReconcileRemoteVMUpdate settles one unresolved remote-storage vm-update after the caller has
// revalidated its pool execution identity. It normally observes only; cleaning-nvram is the one
// journaled native recovery stage, which resumes only after that validation. The update succeeds
// when the persistent definition and state match it; it fails only when a stopped VM's persistent
// definition is still exactly its predecessor after allocation or define (proven unapplied);
// anything else stays needs_attention. Like ReconcileRemoteVMCreateStopped, remote storage is
// excluded from promote because the report path cannot make these checks.
func (e *ManagedEngine) ReconcileRemoteVMUpdate(ctx context.Context, validate func(context.Context, protocol.VMCreateSpec) error) error {
	if err := ctx.Err(); err != nil {
		return err
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
	due := nextRemoteVMUpdateReconcile(js, time.Now().UTC())
	if due == nil {
		return nil
	}
	j := *due
	err = validate(ctx, *j.Command.VM)
	validated := err == nil
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && firmwareRemovalPending(j) {
		err = e.finishFirmwareRemoval(ctx, &j)
	}
	if err == nil && j.Stage == secureBootVariablesStage {
		err = e.finishSecureBootVariables(ctx, &j)
	}
	if err == nil && j.Stage == secureBootDefiningStage {
		err = e.finishSecureBootDefinition(ctx, &j)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		err = e.observer()(ctx, j, updateState(j))
	}
	if err == nil {
		err = ctx.Err()
	}
	switch {
	case err == nil:
		j.Result.Status, j.Result.Error, j.Stage = "succeeded", "", "defined"
	case validated && ctx.Err() == nil && updateProvenUnapplied(ctx, js, j):
		j.Result.Status, j.UnappliedObserved = "failed", true
		j.Result.Error = "VM update was not applied; the VM keeps its previous definition: " + err.Error()
	default:
		j.Result.Status, j.Result.Error = "needs_attention", err.Error()
	}
	j.Result.ObservedAt = time.Now().UTC()
	return e.save(j)
}

func nextRemoteVMUpdateReconcile(js []managedJournal, now time.Time) *managedJournal {
	var due *managedJournal
	for i := range js {
		j := &js[i]
		if j.Command.Kind != "vm-update" || j.Command.VM == nil || (!updateObservable(j.Stage) && j.Stage != "allocating" && j.Stage != "cleaning-nvram" && j.Stage != secureBootVariablesStage && j.Stage != secureBootDefiningStage) || (j.Result.Status != "pending" && j.Result.Status != "needs_attention") || !promotionUsesRemoteStorage(js, *j) || now.Sub(j.Result.ObservedAt) < time.Minute {
			continue
		}
		// A later success of the same or a newer revision (for example an operator retry) owns
		// the definition now; observing it would relabel this attempt's history.
		superseded := false
		for k := range js {
			p := &js[k]
			if p.Result.Status == "succeeded" && (p.Command.Kind == "vm-create" || p.Command.Kind == "vm-update") && p.Command.VM != nil && p.Command.VM.ID == j.Command.VM.ID && p.Command.VM.Revision >= j.Command.VM.Revision {
				superseded = true
				break
			}
		}
		if superseded {
			continue
		}
		if due == nil || j.Result.ObservedAt.Before(due.Result.ObservedAt) || (j.Result.ObservedAt.Equal(due.Result.ObservedAt) && j.Command.OperationID < due.Command.OperationID) {
			due = j
		}
	}
	return due
}

// updateProvenUnapplied reports whether an update interrupted at allocation or define left
// the shut-off VM exactly on its single succeeded predecessor definition. A live attachment,
// a running guest, a rename or any other difference stays ambiguous.
func updateProvenUnapplied(ctx context.Context, js []managedJournal, j managedJournal) bool {
	if (j.Stage != "allocating" && j.Stage != "defining") || len(j.Domain) == 0 {
		return false
	}
	var previous *managedJournal
	for i := range js {
		p := &js[i]
		if p.Result.Status != "succeeded" || (p.Command.Kind != "vm-create" && p.Command.Kind != "vm-update") || p.Command.VM == nil || p.Command.VM.ID != j.Command.VM.ID || p.Command.VM.Revision != j.Command.VM.Revision-1 {
			continue
		}
		if previous != nil {
			return false
		}
		previous = p
	}
	if previous == nil || len(previous.Domain) == 0 {
		return false
	}
	state, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil || strings.TrimSpace(string(state)) != "shut off" {
		return false
	}
	b, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	return err == nil && validateManagedDefinition(b, previous.Domain) == nil && validateManagedDefinition(b, j.Domain) != nil
}

// managedIdentity is the resource ID and operator name a command addresses.
func managedIdentity(c protocol.Command) (string, string) {
	switch c.Kind {
	case "storage-register":
		return c.Storage.ID, c.Storage.Name
	case "storage-update", "storage-recheck", "storage-detach":
		if c.StorageLifecycle != nil {
			return c.StorageLifecycle.RepositoryID, ""
		}
	case "network-remove", "network-update", "media-remove":
		if c.ResourceLifecycle != nil {
			return c.ResourceLifecycle.ResourceID, c.ResourceLifecycle.Name
		}
	case "storage-credential-retire":
		if c.CredentialRetire != nil {
			return c.CredentialRetire.Receipt, ""
		}
	case "network-register":
		return c.Network.ID, c.Network.Name
	case "iso-import":
		return c.Media.ID, c.Media.Name
	case "vm-create", "vm-update":
		return c.VM.ID, c.VM.Name
	case "vm-power":
		return c.Power.VMID, ""
	case "vm-delete":
		return c.Delete.VMID, ""
	case "vm-media":
		return c.VMMedia.VMID, ""
	}
	return "", ""
}

// domains lists the persistent domains libvirt holds; nil when it cannot answer, which
// proves nothing about any VM.
func domains(ctx context.Context) map[string]bool {
	b, err := managedVirsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return nil
	}
	out := map[string]bool{}
	for _, id := range strings.Fields(string(b)) {
		out[strings.ToLower(id)] = true
	}
	return out
}

// promote settles completed ISO publications and vm-create/vm-update entries left pending or
// needs_attention after the definition step, and none of them is executing here: when the
// persistent domain matches the journaled definition the operation succeeded (an edit in the
// state it left the guest), and a creation that died at the start step is settled from
// libvirt's state (see settleStart). reconcile does the same for a re-sent operation, which a
// replaced Controller never sends. No guest is started here: a start the interruption skipped
// is recorded as not started. Each entry is re-observed at most once a minute; a running
// operation holds the journal lock, in which case this round is skipped.
func (e *ManagedEngine) promote(ctx context.Context, js []managedJournal, running map[string]bool) []managedJournal {
	var due []int
	for i, j := range js {
		if promotionUsesRemoteStorage(js, j) {
			continue
		}
		observable := (j.Command.Kind == "iso-import" && j.Command.Media.URL != "" && j.MediaPath != "") || (j.Command.Kind == "vm-create" && createObservable(j.Stage)) || (j.Command.Kind == "vm-update" && updateObservable(j.Stage))
		if observable && (j.Result.Status == "needs_attention" || j.Result.Status == "pending") && !running[j.Command.OperationID] && time.Since(j.Result.ObservedAt) >= time.Minute {
			due = append(due, i)
		}
	}
	if len(due) == 0 {
		return js
	}
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(lockCtx)
	if err != nil {
		return js
	}
	defer unlock(lock)
	observe := e.observer()
	for _, i := range due {
		// Re-read under the lock: the entry may have moved on since it was listed.
		j, err := e.readJournal(js[i].Command.OperationID + ".json")
		if err != nil || (j.Result.Status != "needs_attention" && j.Result.Status != "pending") {
			continue
		}
		if j.Command.Kind == "iso-import" {
			err = verifyISO(j.MediaPath, journalMedia(j))
			if err != nil {
				_ = os.Remove(j.MediaPath + ".partial")
			}
		} else if j.Stage == "starting" || j.Stage == "started" {
			err = settleStart(ctx, &j)
		} else if err = observe(ctx, j, updateState(j)); err == nil {
			j.Stage = "defined"
			if j.Command.Kind == "vm-create" && j.Command.VM.StartAfterCreate && j.Start == "" {
				j.Start = startInterrupted
			}
		}
		if err == nil {
			j.Result.Status, j.Result.Error = "succeeded", ""
		} else {
			j.Result.Status, j.Result.Error = "needs_attention", err.Error()
		}
		j.Result.ObservedAt = time.Now().UTC()
		if e.save(j) == nil {
			js[i] = j
		}
	}
	return js
}

func promotionUsesRemoteStorage(js []managedJournal, j managedJournal) bool {
	if j.Command.VM == nil {
		return false
	}
	for _, d := range j.Command.VM.Disks {
		s, err := managedStorage(js, d.StorageID)
		if err == nil && s.StorageKind() != protocol.StorageKindLocal {
			return true
		}
	}
	return false
}

// createObservable reports whether a vm-create stage has a definition to observe.
func createObservable(stage string) bool {
	return stage == "defining" || stage == "defined" || stage == "starting" || stage == "started"
}

// updateObservable reports whether a vm-update stage has a definition to observe.
func updateObservable(stage string) bool {
	return stage == "defining" || stage == secureBootDefiningStage || stage == "attaching" || stage == "defined"
}

// updateState is the libvirt state an interrupted operation left the guest in: an edit
// applied live (stage attaching) kept it running; everything else defined it shut off.
func updateState(j managedJournal) string {
	if j.Command.Kind == "vm-update" && j.Stage == "attaching" {
		return "running"
	}
	return "shut off"
}

// settleStart resolves an automatic start whose outcome the journal never recorded: the
// single request was issued or interrupted, and libvirt's state says which. A recorded
// outcome, started or the failure reason, is kept.
func settleStart(ctx context.Context, j *managedJournal) error {
	if j.Start != "" {
		j.Stage = "started"
		return nil
	}
	state, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil {
		return errors.New("VM state cannot be established after an interrupted start")
	}
	j.Start, j.Stage = startInterrupted, "started"
	if strings.TrimSpace(string(state)) == "running" {
		j.Start = "started"
	}
	return nil
}
func managedSubdir(path string) error {
	if err := trustedManagedPath(filepath.Dir(path)); err != nil {
		return err
	}
	info, created, err := managedDirectory(path)
	if err != nil {
		return err
	}
	if created {
		return nil
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed path is not a directory")
	}
	return trustedManagedPath(path)
}

// managedDirectory creates a Core-owned directory with the intended traversal mode even
// when the agent service runs with UMask=0077. Existing directories are never chmodded here:
// on remote repositories their ownership and mode can be controlled by the storage server.
func managedDirectory(path string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if err = os.Mkdir(path, 0711); err != nil {
			return nil, false, err
		}
		// The remote mount leaf may be writable by the storage server. Never chmod by
		// pathname after mkdir: an attacker could replace it with a symlink between
		// those calls. Open the newly created directory without following links, set
		// its mode through that descriptor, then prove the pathname still names it.
		fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		if err != nil {
			return nil, false, err
		}
		f := os.NewFile(uintptr(fd), path)
		defer f.Close()
		if err = f.Chmod(0711); err != nil {
			return nil, false, err
		}
		info, err = f.Stat()
		if err != nil {
			return nil, false, err
		}
		atPath, err := os.Lstat(path)
		if err != nil || atPath.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, atPath) {
			return nil, false, errors.New("managed directory changed during creation")
		}
		return info, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("managed directory uses a symlink")
	}
	return info, false, nil
}

// managedRepositorySubdir keeps local storage's strict root-owned ancestry rule. Remote
// mounts are different: the verified mount leaf may be owned/mapped by the file server for
// libvirt-qemu, so only its trusted local parent and every no-symlink component are required.
func managedRepositorySubdir(ctx context.Context, js []managedJournal, id, path string) error {
	s, _, err := managedStorageByID(ctx, js, id, 0)
	if err != nil {
		return err
	}
	if s.StorageKind() == protocol.StorageKindLocal {
		return managedSubdir(path)
	}
	root := remoteMountPath(s)
	// A Core-provisioned ZFS dataset mounts directly beneath storageMountRoot;
	// it has no bind alias. Existing imported ZFS records keep their legacy path.
	if s.StorageKind() == protocol.StorageKindZFS && s.Source.ZFS != nil && s.Path == s.Source.ZFS.MountPath {
		root = s.Path
	}
	if !strings.HasPrefix(path, root+"/") || filepath.Clean(path) != path {
		return errors.New("managed storage directory escapes its repository mount")
	}
	if err := trustedManagedPath(filepath.Dir(root)); err != nil {
		return err
	}
	check := func(p string) error {
		info, _, err := managedDirectory(p)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("managed storage directory is unavailable or uses a symlink")
		}
		return nil
	}
	if err := check(root); err != nil {
		return err
	}
	p := root
	for _, part := range strings.Split(strings.TrimPrefix(path, root+"/"), "/") {
		p = filepath.Join(p, part)
		if err := check(p); err != nil {
			return err
		}
	}
	return nil
}
func (e *ManagedEngine) apply(ctx context.Context, j *managedJournal, js []managedJournal, fetch func(context.Context, string) (io.ReadCloser, error)) error {
	c := j.Command
	switch c.Kind {
	case "vm-power":
		if c.Power.AfterOperationID != "" {
			ready := false
			for _, before := range js {
				if before.Command.OperationID != c.Power.AfterOperationID {
					continue
				}
				switch before.Command.Kind {
				case "vm-create", "vm-update", "vm-power", "vm-media":
					id, _ := managedIdentity(before.Command)
					if id != c.Power.VMID {
						return errors.New("predecessor operation belongs to another VM")
					}
					if before.Result.Status != "succeeded" {
						return fmt.Errorf("predecessor operation did not succeed (%s); inspect it before requesting power again", before.Result.Status)
					}
					ready = true
				}
				break
			}
			if !ready {
				return errors.New("predecessor VM operation not found on this host")
			}
		}
		var spec *protocol.VMCreateSpec
		if c.Power.Action == "start" {
			v, currentErr := managedCurrent(js, c.Power.VMID)
			if currentErr == nil {
				spec = v.Command.VM
			}
			// An unresolved newer definition (for example a memory edit libvirt may already
			// hold) is what could boot, so check its ceiling rather than the last success.
			if newest, ok := currentDefinitions(js)[c.Power.VMID]; ok {
				spec = &newest
			}
			if currentErr == nil && len(v.Disks) > 0 {
				if err := ensureVMStorage(ctx, js, *v.Command.VM); err != nil {
					return err
				}
			}
		}
		// Stage "powering" is persisted before the single virsh call so an interruption is
		// reconciled by observation instead of a second request.
		j.Stage = "powering"
		if err := e.save(*j); err != nil {
			return err
		}
		return powerVM(ctx, *c.Power, spec)
	case "vm-media":
		// changeMedia persists Stage "changing-media" itself, immediately before its virsh
		// call: an interruption before that point issued nothing, so reconcile re-runs it.
		return e.changeMedia(ctx, j, js)
	case "storage-credential-retire":
		return retireStorageCredential(filepath.Dir(e.stateDir), c.CredentialRetire.Receipt, c.CredentialRetire.PoolID, c.CredentialRetire.HostID, storageCredentialAttached(js, c.CredentialRetire.Receipt))
	case "storage-register":
		return e.registerStorage(ctx, j, js, c)
	case "storage-update", "storage-recheck", "storage-detach":
		v := c.StorageLifecycle
		current, err := managedStorage(js, v.RepositoryID)
		if err != nil {
			return err
		}
		if v.Action != "recheck" && current.Generation != v.ExpectedGeneration {
			return errors.New("storage generation is no longer current; refresh storage and retry")
		}
		switch v.Action {
		case "recheck":
			if err := repairStaleRemoteMount(ctx, js, current); err != nil {
				return err
			}
			startRemoteMount(ctx, current)
			_, err := managedStorageReady(ctx, current, 0)
			return err
		case "detach":
			if err := storageDetachRefusal(js, current.ID); err != nil {
				return err
			}
			credentialRoot := filepath.Join(filepath.Dir(e.stateDir), credentialDirectory)
			if err := storageDetachAt(ctx, current, credentialRoot); err != nil {
				return err
			}
			// Same lifecycle as registering a password with a repository: detach removes the
			// Core-local secret for that repository. Mid-flight leftovers with no repository
			// are left alone.
			return releaseStorageCredential(filepath.Dir(e.stateDir), current)
		case "update":
			if protocol.IsClusteredISCSI(current) {
				return errors.New("clustered iSCSI updates require a coordinated Core cluster operation")
			}
			next := *v.Registration
			next.ID, next.PoolID, next.HostID, next.Generation = current.ID, current.PoolID, current.HostID, current.Generation+1
			storageCorePaths(&next)
			sourceChanged := storageSourceChanged(next, current) || (next.StorageKind() == protocol.StorageKindLocal && next.Path != current.Path)
			credentialChanged := v.CredentialReceipt != ""
			transportChanged := sourceChanged || credentialChanged
			if !transportChanged {
				// A metadata-only rename does not disturb an active mount/session.
				next.Path, next.Identity, next.Capabilities, next.Access = current.Path, current.Identity, current.Capabilities, current.Access
				next.CredentialReceipt, next.CredentialPresent, next.CredentialRequestID = current.CredentialReceipt, current.CredentialPresent, current.CredentialRequestID
				j.AppliedStorage = &next
				return nil
			}
			if sourceChanged {
				if err := registrationConflict(js, protocol.Command{OperationID: c.OperationID, Kind: "storage-register", Storage: &next}); err != nil {
					return err
				}
				if deps := storageDependencies(js, current.ID); len(deps) > 0 {
					return errors.New("storage cannot update while used by " + strings.Join(deps, ", "))
				}
			} else if credentialChanged {
				if err := credentialRotationSafe(ctx, js, current.ID); err != nil {
					return err
				}
			}
			if v.CredentialReceipt != "" {
				if err := credentialReceiptValid(filepath.Dir(e.stateDir), v.CredentialReceipt, next.ID, next.PoolID, next.HostID); err != nil {
					return err
				}
				next.CredentialReceipt, next.CredentialPresent = v.CredentialReceipt, true
			} else {
				next.CredentialReceipt, next.CredentialPresent = current.CredentialReceipt, current.CredentialPresent
			}
			credentialRoot := filepath.Join(filepath.Dir(e.stateDir), credentialDirectory)
			access, err := storagePrepareWithPrevious(allowStorageRemount(ctx), next, credentialRoot, current)
			if err != nil {
				return err
			}
			next.Path, next.Identity = access.Path, access.Observed
			if err = storageVerify(allowStorageRemount(ctx), next, access); err != nil || access.ReadOnly || access.Available < 1<<30 {
				fail := err
				if fail == nil {
					fail = errors.New("storage is unavailable, read-only, or lacks reserve capacity")
				}
				// A source change already replaced the previous attachment. Tear down the
				// refused new one and mark the outcome uncertain: automatic restore of the
				// prior source is not proven here.
				if sourceChanged {
					if cleanupErr := storageDetachAt(context.Background(), next, credentialRoot); cleanupErr != nil {
						return errors.Join(fail, fmt.Errorf("%w: clean up rejected storage update: %v", ErrStorageRollbackUncertain, cleanupErr))
					}
					return errors.Join(fail, fmt.Errorf("%w: previous storage was replaced and was not restored after rejected update", ErrStorageRollbackUncertain))
				}
				return fail
			}
			next.Path = access.Path
			next.Identity = access.Observed
			next.Capabilities = storageCapabilities(next)
			next.Access = protocol.StorageAccess{Status: access.Health, Readable: true, Writable: !access.ReadOnly, TotalBytes: access.Total, AvailableBytes: &access.Available, ObservedAt: time.Now().UTC()}
			j.AppliedStorage = &next
			if credentialChanged && current.CredentialReceipt != "" && current.CredentialReceipt != next.CredentialReceipt {
				if err := releaseStorageCredential(filepath.Dir(e.stateDir), protocol.StorageRegistration{CredentialReceipt: current.CredentialReceipt, PoolID: current.PoolID, HostID: current.HostID}); err != nil {
					return err
				}
			}
			return nil
		}
	case "network-remove", "network-update":
		v := c.ResourceLifecycle
		current, err := managedNetwork(js, v.ResourceID)
		if err != nil {
			return err
		}
		if !protocol.IsPoolSharedNetwork(current) {
			return errors.New("only pool VLAN networks can be removed")
		}
		if protocol.NetworkGeneration(current) != v.ExpectedGeneration {
			return errors.New("network generation is no longer current; refresh and retry")
		}
		if c.Kind == "network-update" {
			// Rename only: the payload cannot carry an uplink, VLAN or source, and the
			// projection copies only the name, so no link is ever touched.
			current.Name = v.Name
			return writeGuestBridgeFile(current, e.networkDir)
		}
		// The same refusals CheckNetworkRemoval reports, so a converge precheck that let
		// this operation through and the operation itself cannot disagree.
		if err = e.networkRemovalRefusal(js, current); err != nil {
			return err
		}
		j.Stage = "removing"
		if err = e.save(*j); err != nil {
			return err
		}
		return removeGuestBridge(ctx, current, e.networkDir)
	case "media-remove":
		v := c.ResourceLifecycle
		current, path, err := managedMedia(js, v.ResourceID)
		if err != nil {
			return err
		}
		if protocol.MediaGeneration(current) != v.ExpectedGeneration {
			return errors.New("ISO generation is no longer current; refresh and retry")
		}
		if err = mediaRemovalRefusal(js, v.ResourceID); err != nil {
			return err
		}
		j.Stage = "removing"
		if err = e.save(*j); err != nil {
			return err
		}
		// On pool-shared storage the published file is the pool's only copy, so only the
		// host that imported it unlinks it. A member's record is an adoption of that same
		// file: it drops its own journal record and leaves the file to the importing host,
		// because one member reaching a wrong conclusion must not destroy the ISO for
		// everyone. The distinction comes from the journal, not from the caller.
		if path == "" || mediaAdopted(js, v.ResourceID) {
			return nil
		}
		if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	case "network-register":
		if err := registrationConflict(js, c); err != nil {
			return err
		}
		if c.Network.Kind == "vlan" {
			return ensureGuestBridge(ctx, *c.Network, e.networkDir)
		}
		return verifyNetwork(ctx, *c.Network)
	case "iso-import":
		m := c.Media
		_, access, err := managedStorageByID(ctx, js, m.StorageID, uint64(m.SizeBytes)+(1<<30))
		if err != nil {
			return err
		}
		dir := filepath.Join(access.Path, "nex-media")
		if err = managedRepositorySubdir(ctx, js, m.StorageID, dir); err != nil {
			return err
		}
		j.MediaPath = filepath.Join(dir, m.ID+".iso")
		j.Stage = "receiving"
		if err = e.save(*j); err != nil {
			return err
		}
		if m.URL != "" {
			var stat syscall.Statfs_t
			if err := syscall.Statfs(access.Path, &stat); err != nil {
				return err
			}
			limit := int64(stat.Bavail)*stat.Bsize - (1 << 30)
			if limit < 32774 {
				return errors.New("insufficient free storage including reserve")
			}
			client := publicISOClient()
			defer client.CloseIdleConnections()
			return e.downloadISO(ctx, j, client, limit)
		}
		if fetch == nil {
			return errors.New("ISO transfer unavailable")
		}
		body, err := fetch(ctx, m.ID)
		if err != nil {
			return err
		}
		defer body.Close()
		return receiveISO(ctx, j.MediaPath, *m, body)
	case "vm-create":
		return e.create(ctx, j, js)
	case "vm-update":
		return e.update(ctx, j, js)
	case "vm-delete":
		return e.deleteVM(ctx, j, js)
	}
	return errors.New("unsupported managed command")
}
func journalMedia(j managedJournal) protocol.Media {
	if j.DownloadedMedia != nil {
		return *j.DownloadedMedia
	}
	return *j.Command.Media
}

func receiveISO(ctx context.Context, path string, m protocol.Media, body io.Reader) error {
	return receiveISOContent(ctx, path, m, body, m.SizeBytes, nil)
}

func receiveISOContent(ctx context.Context, path string, m protocol.Media, body io.Reader, limit int64, record func(protocol.Media) error) error {
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("ISO destination already exists or cannot be inspected")
	}
	f, err := os.OpenFile(path+".partial", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&managedContextReader{ctx, body}, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return errors.New("ISO exceeds available storage including reserve")
	}
	if m.URL == "" && (n != m.SizeBytes || hex.EncodeToString(h.Sum(nil)) != m.SHA256) {
		return errors.New("ISO transfer length or SHA-256 mismatch")
	}
	marker := make([]byte, 5)
	if _, err = f.ReadAt(marker, 32769); err != nil || string(marker) != "CD001" {
		return errors.New("media does not contain an ISO-9660 volume descriptor")
	}
	if err = f.Chmod(0444); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	// Save computed metadata before publication so a crash can verify the final file
	// without fetching the URL again; the original command remains immutable.
	if record != nil {
		m.SizeBytes, m.SHA256, m.URL = n, hex.EncodeToString(h.Sum(nil)), ""
		if err := record(m); err != nil {
			return err
		}
	}
	// Link publishes without overwriting an existing destination, unlike Rename.
	if err = os.Link(path+".partial", path); err != nil {
		return err
	}
	if err = os.Remove(path + ".partial"); err != nil {
		return err
	}
	return syncManagedDir(filepath.Dir(path))
}

type managedContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *managedContextReader) Read(b []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(b)
}
func syncManagedDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func verifyISO(path string, m protocol.Media) error {
	return verifyISOContext(context.Background(), path, m)
}

// verifyISOContext is verifyISO with a cancellable digest read: a shared ISO on a hung
// remote mount must not pin the caller's goroutine past its deadline.
func verifyISOContext(ctx context.Context, path string, m protocol.Media) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("ISO missing or replaced by a symlink")
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	// Permission bits are not checked: SMB mounts force file_mode=0600, so the import's
	// chmod 0444 cannot stick there. Size and SHA-256 prove the content is unchanged.
	if err != nil || !info.Mode().IsRegular() || info.Size() != m.SizeBytes {
		return errors.New("ISO size changed or is not a regular file")
	}
	h := sha256.New()
	if _, err = io.Copy(h, &managedContextReader{ctx: ctx, r: io.LimitReader(f, m.SizeBytes+1)}); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return errors.New("ISO SHA-256 changed")
	}
	return nil
}

type managedOS struct {
	Firmware string    `xml:"firmware,attr,omitempty"`
	Type     osTypeXML `xml:"type"`
	// FirmwareFeatures states Secure Boot and the enrolled keys instead of leaving libvirt to
	// pick a firmware build from the rest of the domain (see applyFirmware).
	FirmwareFeatures *firmwareXML  `xml:"firmware,omitempty"`
	NVRAM            *nvramXML     `xml:"nvram,omitempty"`
	Boot             []managedBoot `xml:"boot"`
}
type managedBoot struct {
	Dev string `xml:"dev,attr"`
}

// managedBootOrder is the per-device boot entry. libvirt forbids mixing it with an <os> boot
// list, so the domain carries one form only.
type managedBootOrder struct {
	Order int `xml:"order,attr"`
}
type managedDisk struct {
	XMLName   xml.Name          `xml:"disk"`
	Type      string            `xml:"type,attr"`
	Device    string            `xml:"device,attr"`
	Driver    driverXML         `xml:"driver"`
	Source    *sourceXML        `xml:"source,omitempty"`
	Target    targetXML         `xml:"target"`
	Readonly  *struct{}         `xml:"readonly,omitempty"`
	Shareable *struct{}         `xml:"shareable,omitempty"`
	Boot      *managedBootOrder `xml:"boot,omitempty"`
	// Alias names a CD drive across definitions; only the two drive aliases are written.
	Alias *aliasXML `xml:"alias,omitempty"`
}
type managedController struct {
	Type  string `xml:"type,attr"`
	Index *int   `xml:"index,attr,omitempty"`
	Model string `xml:"model,attr,omitempty"`
}
type managedTopology struct {
	Sockets int `xml:"sockets,attr"`
	Cores   int `xml:"cores,attr"`
	Threads int `xml:"threads,attr"`
}
type managedCPU struct {
	Mode     string           `xml:"mode,attr"`
	Match    string           `xml:"match,attr,omitempty"`
	Check    string           `xml:"check,attr,omitempty"`
	Model    *cpuModelXML     `xml:"model,omitempty"`
	Topology *managedTopology `xml:"topology,omitempty"`
	Features []cpuFeatureXML  `xml:"feature"`
}
type cpuModelXML struct {
	Fallback string `xml:"fallback,attr,omitempty"`
	Value    string `xml:",chardata"`
}

// Maximum uses a virtio balloon to report free guest pages; dedicated has no balloon
// and uses physical memory backing below. The stats period lets the guest's balloon driver
// report memory used inside the guest for usage history; it is never compared on
// validation, because it changes nothing the guest can be tricked by.
type managedBalloon struct {
	Model             string               `xml:"model,attr"`
	FreePageReporting string               `xml:"freePageReporting,attr,omitempty"`
	Stats             *managedBalloonStats `xml:"stats,omitempty"`
}
type managedBalloonStats struct {
	Period int `xml:"period,attr"`
}
type managedMemoryBacking struct {
	NoSharePages *struct{} `xml:"nosharepages,omitempty"`
	Locked       *struct{} `xml:"locked,omitempty"`
	Allocation   struct {
		Mode string `xml:"mode,attr"`
	} `xml:"allocation"`
}

func dedicatedMemoryBacking() *managedMemoryBacking {
	b := &managedMemoryBacking{NoSharePages: &struct{}{}, Locked: &struct{}{}}
	b.Allocation.Mode = "immediate"
	return b
}

type managedNIC struct {
	XMLName xml.Name `xml:"interface"`
	Type    string   `xml:"type,attr"`
	Source  struct {
		Bridge  string `xml:"bridge,attr,omitempty"`
		Network string `xml:"network,attr,omitempty"`
	} `xml:"source"`
	MAC struct {
		Address string `xml:"address,attr"`
	} `xml:"mac"`
	Model struct {
		Type string `xml:"type,attr"`
	} `xml:"model"`
	// Only emitted for virtio: QEMU passes it to the guest through host_mtu, and libvirt sizes
	// the host tap to match. The emulated models have no way to tell the guest.
	MTU *mtuXML `xml:"mtu,omitempty"`
}
type mtuXML struct {
	Size int `xml:"size,attr"`
}

// managedPM removes ACPI S3 and S4 from the guest's firmware tables, so the guest never
// offers Sleep or Hibernate. A server VM that sleeps holds its disks and its owner slot
// while doing nothing, and every guest OS ships a sleep timer that eventually fires
// (BUG-0273). Hyper-V exposes no S3 at all; the other hypervisors advise disabling guest
// power management. libvirt notes a guest can still try to circumvent this; powerVM and
// ManagedVMState handle a domain that nevertheless reports pmsuspended.
type managedPM struct {
	Mem  pmStateXML `xml:"suspend-to-mem"`
	Disk pmStateXML `xml:"suspend-to-disk"`
}
type pmStateXML struct {
	Enabled string `xml:"enabled,attr"`
}

type managedDomain struct {
	XMLName       xml.Name              `xml:"domain"`
	Type          string                `xml:"type,attr"`
	Name          string                `xml:"name"`
	UUID          string                `xml:"uuid"`
	Memory        memoryXML             `xml:"memory"`
	CurrentMemory *memoryXML            `xml:"currentMemory,omitempty"`
	MemoryBacking *managedMemoryBacking `xml:"memoryBacking,omitempty"`
	VCPU          int                   `xml:"vcpu"`
	OS            managedOS             `xml:"os"`
	Features      managedFeatures       `xml:"features"`
	CPU           managedCPU            `xml:"cpu"`
	Clock         *clockXML             `xml:"clock,omitempty"`
	PM            managedPM             `xml:"pm"`
	Devices       struct {
		Disks       []managedDisk       `xml:"disk"`
		Controllers []managedController `xml:"controller"`
		Interfaces  []managedNIC        `xml:"interface"`
		Channels    []channelXML        `xml:"channel"`
		Inputs      []inputXML          `xml:"input"`
		Graphics    []graphicsXML       `xml:"graphics"`
		Video       []videoXML          `xml:"video"`
		TPM         *managedTPM         `xml:"tpm,omitempty"`
		Memballoon  managedBalloon      `xml:"memballoon"`
	} `xml:"devices"`
}

// consoleDevices are the guest devices behind the browser console features: the guest
// agent channel (facts, graceful operations), the qemu-vdagent channel (bidirectional
// clipboard once spice-vdagent runs in the guest), a USB tablet (absolute pointer in any
// guest without drivers) and a VirtIO display (guest resolution follows the browser).
func consoleDevices(d *managedDomain) {
	agent := channelXML{Type: "unix", Target: targetXML{Type: "virtio", Name: "org.qemu.guest_agent.0"}}
	vdagent := channelXML{Type: "qemu-vdagent", Source: &channelSourceXML{}, Target: targetXML{Type: "virtio", Name: "com.redhat.spice.0"}}
	vdagent.Source.Clipboard.Copypaste = "yes"
	vdagent.Source.Mouse.Mode = "server"
	d.Devices.Channels = []channelXML{agent, vdagent}
	d.Devices.Inputs = []inputXML{{Type: "tablet", Bus: "usb"}}
	d.Devices.Video = []videoXML{{}}
	d.Devices.Video[0].Model.Type = "virtio"
}

func managedMachine(v protocol.VMCreateSpec) string {
	if v.Boot.Machine == "" {
		return "q35"
	}
	return v.Boot.Machine
}

// nextTarget assigns the first free libvirt target name for a bus: virtio disks take vdX;
// SATA and SCSI disks share sdX with the CD drive.
func nextTarget(bus string, used map[string]bool) (string, error) {
	prefix := "sd"
	if bus == "virtio" {
		prefix = "vd"
	}
	for letter := 'a'; letter <= 'z'; letter++ {
		dev := prefix + string(letter)
		if !used[dev] {
			used[dev] = true
			return dev, nil
		}
	}
	return "", errors.New("no free disk target names remain on this bus")
}

// managedTargets assigns creation-time target names positionally, plus both CD drives'.
func managedTargets(v protocol.VMCreateSpec) (targets []string, media, drivers string) {
	used := map[string]bool{}
	targets = make([]string, len(v.Disks))
	for i, d := range v.Disks {
		targets[i], _ = nextTarget(d.Bus, used)
	}
	media, _ = nextTarget("sata", used)
	drivers, _ = nextTarget("sata", used)
	return targets, media, drivers
}

// driversTarget places the drivers CD drive on a VM whose disk and install-media targets were
// assigned before that drive existed, so adding it never renames a device the guest already has.
func driversTarget(targets []string, media string) string {
	used := map[string]bool{media: true}
	for _, t := range targets {
		used[t] = true
	}
	dev, _ := nextTarget("sata", used)
	return dev
}

// q35Machine reports whether a machine type is the PCIe q35 family, which takes root ports.
func q35Machine(machine string) bool {
	return machine == "q35" || strings.HasPrefix(machine, "pc-q35-")
}

// managedDomainXML builds the persistent definition. nets, disks and targets are positional:
// nets[i] carries NICs[i]'s registration, disks[i] the path allocated for Disks[i] and
// targets[i] its libvirt target. Every VM carries two SATA CD drives, so an ISO can be inserted
// or ejected later without hot-plugging a SATA device: the install drive at mediaTarget holding
// iso, and the driver-disc drive at driversTarget holding driversISO. Either is empty when its
// path is. Each carries the user alias that names it in a media request.
func managedDomainXML(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks, targets []string, mediaTarget, iso, driversTarget, driversISO string) ([]byte, error) {
	if len(nets) != len(v.NICs) || len(disks) != len(v.Disks) || len(targets) != len(v.Disks) || mediaTarget == "" || driversTarget == "" || mediaTarget == driversTarget {
		return nil, errors.New("domain devices do not match the requested VM")
	}
	d := managedDomain{Type: "kvm", Name: v.Name, UUID: v.ID, Memory: memoryXML{"MiB", v.Memory.SizeMiB}, VCPU: v.CPU.Count, OS: managedOS{Type: osTypeXML{"x86_64", managedMachine(v), "hvm"}}}
	d.PM = managedPM{Mem: pmStateXML{"no"}, Disk: pmStateXML{"no"}}
	applyFirmware(&d, v.Boot)
	applyGuestOSDefaults(&d, v.GuestOS)
	if v.Boot.TPMEnabled() {
		d.Devices.TPM = emulatedTPM()
	}
	// Dedicated claims physical RAM before guest execution, prevents swapping/KSM and removes
	// the balloon. A stopped domain retains only its admission commitment. Do not guess a QEMU
	// hard_limit: it includes variable process overhead and an undersized limit kills the VM.
	if v.Memory.Mode == "dedicated" {
		current := memoryXML{"MiB", v.Memory.SizeMiB}
		d.CurrentMemory = &current
		d.MemoryBacking = dedicatedMemoryBacking()
		d.Devices.Memballoon = managedBalloon{Model: "none"}
	} else {
		d.Devices.Memballoon = managedBalloon{Model: "virtio", FreePageReporting: "on", Stats: &managedBalloonStats{Period: 5}}
	}
	sockets, cores, threads := v.CPU.Topology()
	d.CPU = managedCPU{Mode: v.CPU.Model, Check: "none", Topology: &managedTopology{sockets, cores, threads}}
	if v.CPU.Model == "max" {
		d.CPU.Mode = "maximum"
		d.CPU.Check = ""
	} else if v.CPU.Model != "host-passthrough" && v.CPU.Model != "host-model" {
		// check=partial: KVM always exposes extras (hypervisor, x2apic, …) beyond a
		// named baseline, so check=full refuses even models libvirt marks usable=yes.
		d.CPU = managedCPU{Mode: "custom", Match: "exact", Check: "partial", Model: &cpuModelXML{Fallback: "forbid", Value: v.CPU.Model}, Topology: &managedTopology{sockets, cores, threads}}
	}
	d.CPU.Features = specCPUFeatures(v.CPU.Features)
	scsi := false
	for i, disk := range v.Disks {
		x := managedDisk{Type: "file", Device: "disk", Driver: driverXML{"qemu", "qcow2"}, Source: &sourceXML{File: disks[i]}, Target: targetXML{Dev: targets[i], Bus: disk.Bus}}
		if disk.BootOrder > 0 {
			x.Boot = &managedBootOrder{disk.BootOrder}
		}
		scsi = scsi || disk.Bus == "scsi"
		d.Devices.Disks = append(d.Devices.Disks, x)
	}
	if scsi {
		d.Devices.Controllers = []managedController{{Type: "scsi", Model: "virtio-scsi"}}
	}
	// Explicit PCIe root ports are consumed by the defined devices first, and libvirt adds no
	// spare beside them (observed: "No more available PCI slots" on hot-plug with four listed
	// ports), so enough are defined for every PCIe device plus four free ones for hot-plug.
	// Only the q35 family has root ports, and once any PCI controller is listed libvirt
	// requires the first (index 0) to be the pcie-root itself.
	if q35Machine(managedMachine(v)) {
		ports := 4 + len(v.NICs) + 3 // video, virtio-serial and balloon
		for _, disk := range v.Disks {
			if disk.Bus == "virtio" {
				ports++
			}
		}
		if scsi {
			ports++
		}
		d.Devices.Controllers = append(d.Devices.Controllers, managedController{Type: "pci", Model: "pcie-root"})
		for range ports {
			d.Devices.Controllers = append(d.Devices.Controllers, managedController{Type: "pci", Model: "pcie-root-port"})
		}
	}
	cdrom := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Target: targetXML{Dev: mediaTarget, Bus: "sata"}, Readonly: &struct{}{}, Alias: &aliasXML{Name: cdromInstallAlias}}
	if iso != "" {
		cdrom.Source = &sourceXML{File: iso}
	}
	if v.Boot.MediaBootOrder > 0 {
		cdrom.Boot = &managedBootOrder{v.Boot.MediaBootOrder}
	}
	d.Devices.Disks = append(d.Devices.Disks, cdrom)
	// The driver disc never takes a boot order: it holds storage and network drivers an
	// installer loads from inside Setup, and giving it one would let the VM boot from it.
	drivers := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Target: targetXML{Dev: driversTarget, Bus: "sata"}, Readonly: &struct{}{}, Alias: &aliasXML{Name: cdromDriversAlias}}
	if driversISO != "" {
		drivers.Source = &sourceXML{File: driversISO}
	}
	d.Devices.Disks = append(d.Devices.Disks, drivers)
	for i, requested := range v.NICs {
		nic := managedNIC{Type: "bridge"}
		if nets[i].Kind == "network" {
			nic.Type = "network"
			nic.Source.Network = nets[i].Source
		} else {
			nic.Source.Bridge = nets[i].Source
		}
		nic.MAC.Address = requested.MAC
		if nic.MAC.Address == "" {
			nic.MAC.Address = protocol.DerivedMAC(v.ID, i)
		}
		nic.Model.Type = requested.Model
		if requested.MTU != 0 {
			nic.MTU = &mtuXML{Size: requested.MTU}
		}
		d.Devices.Interfaces = append(d.Devices.Interfaces, nic)
	}
	consoleDevices(&d)
	g := graphicsXML{Type: "vnc", Autoport: "no"}
	g.Listen.Type = "socket"
	d.Devices.Graphics = []graphicsXML{g}
	return xml.MarshalIndent(d, "", "  ")
}

// managedMemoryFits applies the ceiling in both memory modes: a "maximum" VM reserves nothing
// but must still be able to reach its ceiling, so a start refuses what the host cannot meet now.
func managedMemoryFits(availableKiB uint64, m protocol.VMMemory) bool {
	return availableKiB >= uint64(m.SizeMiB+2048)*1024
}

// dedicatedCommitted sums the dedicated memory this host has already promised, in MiB: every
// other vm-create the journal still holds, including pending and uncertain ones, because a
// stopped dedicated VM consumes nothing yet and MemAvailable proves nothing about it.
func dedicatedCommitted(js []managedJournal, except string) int {
	total := 0
	for _, v := range currentDefinitions(js) {
		if v.ID != except && v.Memory.Mode == "dedicated" {
			total += v.Memory.SizeMiB
		}
	}
	return total
}

// currentDefinitions is every VM's current definition the journal still holds: the highest
// revision among its non-failed vm-create and vm-update entries, pending and uncertain ones
// included, because what is promised or reserved does not wait for a result. A succeeded
// vm-delete removes the VM from the map.
func currentDefinitions(js []managedJournal) map[string]protocol.VMCreateSpec {
	out := map[string]protocol.VMCreateSpec{}
	deleted := managedDeletedVMs(js)
	for _, j := range js {
		if (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") || j.Command.VM == nil || j.Result.Status == "failed" {
			continue
		}
		if deleted[j.Command.VM.ID] {
			continue
		}
		if cur, ok := out[j.Command.VM.ID]; !ok || j.Command.VM.Revision > cur.Revision {
			out[j.Command.VM.ID] = *j.Command.VM
		}
	}
	return out
}

// managedDeletedVMs lists VMs with a succeeded vm-delete entry.
func managedDeletedVMs(js []managedJournal) map[string]bool {
	out := map[string]bool{}
	for _, j := range js {
		if j.Command.Kind == "vm-delete" && j.Result.Status == "succeeded" && j.Command.Delete != nil {
			out[j.Command.Delete.VMID] = true
		}
	}
	return out
}

// dedicatedFits admission-controls a dedicated VM against total host memory: what is already
// promised, this VM and the 2 GiB host reserve must fit. Maximum mode reserves nothing.
func dedicatedFits(totalKiB uint64, committedMiB int, m protocol.VMMemory) bool {
	return m.Mode != "dedicated" || uint64(committedMiB+m.SizeMiB+2048)*1024 <= totalKiB
}

// managedKVMDevice and managedMeminfo are the host files capacity and start checks read;
// tests point them at fakes.
var managedKVMDevice, managedMeminfo = "/dev/kvm", "/proc/meminfo"

// hostMemory reads MemAvailable and MemTotal, in KiB.
func hostMemory() (available, total uint64, err error) {
	mem, err := os.ReadFile(managedMeminfo)
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(mem), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "MemAvailable:" {
			available, _ = strconv.ParseUint(fields[1], 10, 64)
		}
		if len(fields) > 1 && fields[0] == "MemTotal:" {
			total, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	return available, total, nil
}

func managedCapacity(ctx context.Context, v protocol.VMCreateSpec, js []managedJournal) error {
	if len(v.CPU.Features) > 0 {
		if err := NativeCPUCompare(ctx, v.CPU.Model, v.CPU.Features, managedMachine(v)); err != nil {
			return err
		}
	}
	if runtime.GOARCH != "amd64" || v.CPU.Count > runtime.NumCPU() {
		return errors.New("requires amd64 with sufficient host CPUs")
	}
	f, err := os.OpenFile(managedKVMDevice, os.O_RDWR, 0)
	if err != nil {
		return errors.New("KVM acceleration unavailable")
	}
	f.Close()
	// Memory free right now is a start-time check (powerVM): a VM that cannot start yet can
	// still be created or edited while stopped. The durable dedicated promise stays here.
	_, total, err := hostMemory()
	if err != nil {
		return err
	}
	if !dedicatedFits(total, dedicatedCommitted(js, v.ID), v.Memory) {
		return errors.New("dedicated memory exceeds what this host has left to promise; free a dedicated VM or use the maximum memory mode")
	}
	b, err := managedVirsh(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", managedMachine(v))
	if err != nil {
		return errors.New("cannot verify KVM capabilities for this machine type")
	}
	if len(v.CPU.Features) == 0 && v.CPU.Model != "host-passthrough" && v.CPU.Model != "host-model" {
		if models, e := UsableCPUModels(b); e != nil || !slices.Contains(models, v.CPU.Model) {
			return fmt.Errorf("CPU model %s is not available on this host", v.CPU.Model)
		}
	}
	var caps domainCapabilities
	if xml.Unmarshal(b, &caps) != nil || caps.Domain != "kvm" || caps.Arch != "x86_64" || caps.OS.Supported != "yes" {
		return errors.New("unsupported KVM capabilities for this machine type")
	}
	if v.Boot.Firmware == "uefi" {
		found := false
		for _, enum := range caps.OS.Enums {
			if enum.Name == "firmware" {
				for _, value := range enum.Values {
					found = found || value == "efi"
				}
			}
		}
		if !found {
			return errors.New("UEFI firmware is not available on this host")
		}
	}
	// A TPM the host cannot actually run is worse than no TPM: libvirt accepts the definition
	// and every start then fails, after the operator was told the VM has one. Refuse here,
	// naming the host support that is missing, rather than defining it and hoping.
	if v.Boot.TPMEnabled() {
		if err = hostTPMSupport(caps); err != nil {
			return err
		}
	}
	// Same reasoning for the firmware: since Secure Boot is stated explicitly rather than left
	// to libvirt, a host without a matching OVMF build fails the define with an error that
	// never mentions Secure Boot. Refuse here in terms of the setting instead (BUG-0215).
	if err = hostSecureBootSupport(caps, v.Boot); err != nil {
		return err
	}
	return nil
}

// availableCPUModel reports whether model is marked usable on this host for the
// default q35 machine. Tests inject query; production uses managedVirsh.
func availableCPUModel(ctx context.Context, model string, query func(context.Context, ...string) ([]byte, error)) bool {
	b, err := query(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", "q35")
	if err != nil {
		return false
	}
	models, err := UsableCPUModels(b)
	return err == nil && slices.Contains(models, model)
}
func (e *ManagedEngine) create(ctx context.Context, j *managedJournal, js []managedJournal) error {
	v := *j.Command.VM
	targets, disks, err := managedDiskPlan(js, v)
	if err != nil {
		return err
	}
	for _, t := range targets {
		_, access, readyErr := managedStorageByID(ctx, js, t.ID, t.Bytes)
		if readyErr != nil || access.Path != t.Path {
			if readyErr != nil {
				return readyErr
			}
			return errors.New("storage mount identity changed before disk allocation")
		}
		if err = managedRepositorySubdir(ctx, js, t.ID, filepath.Join(access.Path, "nex-vms")); err != nil {
			return err
		}
	}
	nets := make([]protocol.NetworkRegistration, len(v.NICs))
	for i, requested := range v.NICs {
		if nets[i], err = managedNetwork(js, requested.NetworkID); err != nil {
			return err
		}
		if err = verifyNetwork(ctx, nets[i]); err != nil {
			return err
		}
	}
	mediaPath, driversPath := "", ""
	for _, resolve := range []struct {
		id   string
		into *string
	}{{v.MediaID, &mediaPath}, {v.DriversMediaID, &driversPath}} {
		if resolve.id == "" {
			continue
		}
		m, path, err := publishedMediaPath(js, resolve.id)
		if err != nil {
			return err
		}
		if storageWasPrepared(js, m.StorageID) {
			if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
				return err
			}
		}
		if err = verifyISO(path, m); err != nil {
			return err
		}
		*resolve.into = path
	}
	if err = managedCapacity(ctx, v, js); err != nil {
		return err
	}
	if err = vmConflict(js, v); err != nil {
		return err
	}
	// A failed lookup is not evidence that a domain is absent: list first and fail closed.
	ids, err := managedVirsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return err
	}
	names, err := managedVirsh(ctx, "list", "--all", "--name")
	if err != nil {
		return err
	}
	for _, id := range strings.Fields(string(ids)) {
		if id == v.ID {
			return errors.New("VM UUID already exists")
		}
	}
	for _, name := range strings.Split(string(names), "\n") {
		if name == v.Name {
			return errors.New("VM name " + v.Name + " already exists on this host")
		}
	}
	// ponytail: one dumpxml per domain; batch through a single libvirt query if hosts carry hundreds.
	for _, id := range strings.Fields(string(ids)) {
		b, err := managedVirsh(ctx, "dumpxml", id, "--inactive")
		if err != nil {
			return errors.New("cannot read an existing VM definition to check MAC uniqueness")
		}
		var d managedDomain
		if xml.Unmarshal(b, &d) != nil {
			continue
		}
		for _, nic := range d.Devices.Interfaces {
			if slices.Contains(vmMACs(v), strings.ToLower(nic.MAC.Address)) {
				return errors.New("MAC address " + nic.MAC.Address + " is already used by VM " + d.Name + " on this host")
			}
		}
	}
	for _, t := range targets {
		if err = managedRepositorySubdir(ctx, js, t.ID, filepath.Join(t.Path, "nex-vms")); err != nil {
			return err
		}
	}
	j.Disks = disks
	j.Targets, j.MediaTarget, j.DriversTarget = managedTargets(v)
	j.MediaPath, j.DriversPath = mediaPath, driversPath
	j.Domain, err = managedDomainXML(v, nets, disks, j.Targets, j.MediaTarget, mediaPath, j.DriversTarget, driversPath)
	if err != nil {
		return err
	}
	j.Stage = "allocating"
	if err = e.save(*j); err != nil {
		return err
	}
	// The exclusive directory on each target is the reservation; never overwrite or reuse a disk.
	for _, t := range targets {
		if err = createDiskDir(t.Dir); err != nil {
			return errors.New("VM directory exists or cannot be exclusively created; inspect before recovery")
		}
		if err = syncManagedDir(filepath.Dir(t.Dir)); err != nil {
			return err
		}
	}
	for i, d := range v.Disks {
		if _, err = run(ctx, "qemu-img", qemuCreateArgs(disks[i], d)...); err != nil {
			return errors.New("disk allocation outcome uncertain; inspect retained VM directory")
		}
		if err = checkImage(ctx, disks[i]); err != nil {
			return err
		}
		if err = os.Chmod(disks[i], 0600); err != nil {
			return err
		}
		f, err := os.Open(disks[i])
		if err != nil {
			return err
		}
		err = f.Sync()
		f.Close()
		if err != nil {
			return err
		}
	}
	for _, t := range targets {
		if err = syncManagedDir(t.Dir); err != nil {
			return err
		}
	}
	domainPath := filepath.Join(e.stateDir, j.Command.OperationID+".xml")
	if err = atomicWrite(domainPath, j.Domain); err != nil {
		return err
	}
	j.Stage = "defining"
	if err = e.save(*j); err != nil {
		return err
	}
	if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
		return errors.New("VM definition outcome uncertain; observation required before recovery")
	}
	// Shared storage autostart is Core-coordinated: libvirt's own boot replay cannot
	// prove current pool ownership.  The member worker later requests a fresh permit.
	if v.Autostart && !managedUsesPoolSharedStorage(js, v) {
		if _, err = managedVirsh(ctx, "autostart", v.ID); err != nil {
			return errors.New("VM defined but autostart could not be enabled; inspect the VM on its host")
		}
	}
	if err = e.observeCreated(ctx, *j); err != nil {
		return err
	}
	j.Stage = "defined"
	return e.startCreated(ctx, j)
}

// startCreated honours start_after_create once the definition is verified. Stage "starting" is
// persisted before the single libvirt request so an interruption is reconciled by observation,
// and the outcome lands in the same journal entry so a retry never starts the guest again. A
// start failure never fails the creation: the VM is defined and verified, so the creation
// succeeds and Start carries the reason, which Applied reports as a failed <operation>:start
// outcome. The only error is a persistence failure.
func (e *ManagedEngine) startCreated(ctx context.Context, j *managedJournal) error {
	if !j.Command.VM.StartAfterCreate || j.Start != "" {
		return nil
	}
	j.Stage = "starting"
	js, err := e.journals()
	if err != nil {
		return err
	}
	if len(j.Disks) > 0 {
		if err = ensureVMStorage(ctx, js, *j.Command.VM); err != nil {
			return err
		}
	}
	if err := e.save(*j); err != nil {
		return err
	}
	j.Start, j.Stage = "started", "started"
	if err := powerVM(ctx, protocol.VMPowerRequest{VMID: j.Command.VM.ID, Action: "start"}, j.Command.VM); err != nil {
		// The create journal has historically exposed a stable concise start outcome;
		// direct vm-power results retain the bounded native diagnostic for recovery.
		var powerErr powerRequestError
		if errors.As(err, &powerErr) {
			j.Start = "libvirt did not confirm the start request; inspect the VM on its host"
		} else {
			j.Start = err.Error()
		}
	}
	return nil
}

// registrationConflict refuses a storage path, or a network source in its namespace, that
// another registration on this host still holds: pending, succeeded or uncertain. A failed
// registration releases its resource. VLAN and plain bridge registrations share the bridge
// namespace; libvirt networks have their own.
func registrationConflict(js []managedJournal, c protocol.Command) error {
	if c.Storage != nil {
		for _, old := range storageProjection(js) {
			if old.ID == c.Storage.ID || old.ID == c.OperationID {
				continue
			}
			sameSource := storageSourcesConflict(old, *c.Storage)
			sameIdentity := storageSameBacking(old.Identity, c.Storage.Identity)
			if sameSource || sameIdentity || storageRegistrationPath(old) == storageRegistrationPath(*c.Storage) {
				if old.Path != "" && old.Path == c.Storage.Path {
					return errors.New("storage path " + c.Storage.Path + " is already registered on this host as " + old.Name)
				}
				return errors.New("storage source is already registered on this host as " + old.Name)
			}
		}
	}
	removedNetworks := map[string]bool{}
	if c.Network != nil {
		for id := range networkTombstones(js) {
			removedNetworks[id] = true
		}
	}
	for _, old := range js {
		if old.Command.OperationID == c.OperationID || old.Result.Status == "failed" {
			continue
		}
		if c.Storage != nil && old.Command.Storage != nil {
			// Successful registrations are represented by storageProjection above,
			// including detach tombstones. This pass only reserves unresolved native
			// work, so an old source cannot block its own update or a later detach and
			// re-registration forever.
			if old.Result.Status != "pending" && old.Result.Status != "needs_attention" {
				continue
			}
			sameSource := storageSourcesConflict(*old.Command.Storage, *c.Storage)
			oldIdentity := old.Command.Storage.Identity
			if old.AppliedStorage != nil {
				oldIdentity = old.AppliedStorage.Identity
			}
			if sameSource || storageSameBacking(oldIdentity, c.Storage.Identity) || (c.Storage.Path != "" && old.Command.Storage.Path == c.Storage.Path) {
				return errors.New("storage source is already registered on this host as " + old.Command.Storage.Name)
			}
		}
		// A removed network released its bridge name: its registration is still in the
		// journal, but the host tore the bridge down, so it must not hold the namespace
		// against a fresh registration of the same uplink and VLAN.
		if c.Network != nil && old.Command.Network != nil && removedNetworks[old.Command.Network.ID] {
			continue
		}
		if c.Network != nil && old.Command.Network != nil && old.Command.Network.Source == c.Network.Source && (old.Command.Network.Kind == "network") == (c.Network.Kind == "network") {
			// A host that was master of its own pool keeps that pool's registration for
			// the same uplink and VLAN. Re-registering the identical network under the
			// joined pool's ID configures nothing new, so refusing it would leave the
			// member unable to converge, and unable to resolve the pool network for a
			// VM NIC, for as long as it stays in the pool. Only a different definition,
			// or a duplicate inside one pool, still holds the bridge namespace.
			if old.Command.Network.PoolID == c.Network.PoolID || !protocol.IsPoolSharedNetwork(*c.Network) || old.Command.Network.Kind != c.Network.Kind ||
				old.Command.Network.Uplink != c.Network.Uplink || old.Command.Network.VLAN != c.Network.VLAN {
				return errors.New("network " + c.Network.Source + " is already registered on this host as " + old.Command.Network.Name)
			}
		}
	}
	return nil
}

// storageSourcesConflict compares ownership-bearing connection identity. iSCSI mount paths
// are Core-generated per repository and cannot distinguish a second claimant. One target and
// portal session belongs to one repository; matching WWID/filesystem UUID also rejects portal
// aliases before another login can affect the first session.
func storageSourcesConflict(a, b protocol.StorageRegistration) bool {
	if a.StorageKind() == protocol.StorageKindLocal || a.StorageKind() != b.StorageKind() {
		return false
	}
	if a.StorageKind() != protocol.StorageKindISCSI {
		return reflect.DeepEqual(a.Source, b.Source)
	}
	av, bv := a.Source.ISCSI, b.Source.ISCSI
	if av == nil || bv == nil {
		return false
	}
	sameSession := av.TargetIQN == bv.TargetIQN && strings.EqualFold(iscsiPortalAddress(av.Portal), iscsiPortalAddress(bv.Portal))
	sameBacking := av.WWID != "" && canonicalWWID(av.WWID) == canonicalWWID(bv.WWID) && av.FilesystemUUID != "" && strings.EqualFold(av.FilesystemUUID, bv.FilesystemUUID)
	return sameSession || sameBacking
}

// storageSourceChanged compares actual update settings while ignoring only Core's iSCSI
// mount path. It deliberately differs from storageSourcesConflict: a changed LUN is a source
// mutation even though a second repository may not share a session.
func storageSourceChanged(a, b protocol.StorageRegistration) bool {
	if a.StorageKind() != b.StorageKind() {
		return true
	}
	if a.StorageKind() != protocol.StorageKindISCSI {
		return !reflect.DeepEqual(a.Source, b.Source)
	}
	av, bv := a.Source.ISCSI, b.Source.ISCSI
	if av == nil || bv == nil {
		return av != bv
	}
	return av.TargetIQN != bv.TargetIQN || !strings.EqualFold(iscsiPortalAddress(av.Portal), iscsiPortalAddress(bv.Portal)) || av.LUN != bv.LUN || canonicalWWID(av.WWID) != canonicalWWID(bv.WWID) || !strings.EqualFold(av.FilesystemUUID, bv.FilesystemUUID) || av.ExclusiveTarget != bv.ExclusiveTarget
}

func storageSameBacking(a, b protocol.StorageIdentity) bool {
	if a.NFS != nil && b.NFS != nil && a.NFS.FilesystemID != "" && a.NFS.FilesystemID == b.NFS.FilesystemID {
		return true
	}
	if a.SMB != nil && b.SMB != nil {
		// CIFS filesystem IDs are reconnect diagnostics, not durable identity. Once
		// both registrations carry Core's marker, only that marker may establish an
		// alias; two distinct markers must never collide just because statfs reused
		// an ID. Mixed marker/legacy records retain the older strict nonempty
		// filesystem-ID comparison; exact source equality is checked independently
		// by registrationConflict.
		if a.SMB.MarkerID != "" && b.SMB.MarkerID != "" {
			return a.SMB.MarkerID == b.SMB.MarkerID
		}
		if a.SMB.FilesystemID != "" && a.SMB.FilesystemID == b.SMB.FilesystemID {
			return true
		}
	}
	if a.ISCSI != nil && b.ISCSI != nil && a.ISCSI.WWID != "" && canonicalWWID(a.ISCSI.WWID) == canonicalWWID(b.ISCSI.WWID) && a.ISCSI.FilesystemUUID != "" && strings.EqualFold(a.ISCSI.FilesystemUUID, b.ISCSI.FilesystemUUID) {
		return true
	}
	return !reflect.DeepEqual(a, protocol.StorageIdentity{}) && reflect.DeepEqual(a, b)
}

// vmMACs lists a VM's effective interface addresses: the supplied MAC, or the one derived
// from the VM identity and interface index.
func vmMACs(v protocol.VMCreateSpec) []string {
	out := make([]string, len(v.NICs))
	for i, n := range v.NICs {
		out[i] = n.MAC
		if n.MAC == "" {
			out[i] = protocol.DerivedMAC(v.ID, i)
		}
	}
	return out
}

// vmConflict refuses a VM whose name or any effective MAC another vm-create in the journal
// still holds; a failed creation holds nothing. libvirt's own domains are checked separately.
func vmConflict(js []managedJournal, v protocol.VMCreateSpec) error {
	macs := vmMACs(v)
	for id, old := range currentDefinitions(js) {
		if id == v.ID {
			continue
		}
		if old.Name == v.Name {
			return errors.New("VM name " + v.Name + " is already used on this host")
		}
		for _, mac := range vmMACs(old) {
			if slices.Contains(macs, mac) {
				return errors.New("MAC address " + mac + " is already used by VM " + old.Name + " on this host")
			}
		}
	}
	return nil
}
func (e *ManagedEngine) observeCreated(ctx context.Context, j managedJournal) error {
	return e.observeDefinition(ctx, j, "shut off")
}

// observeDefinition proves a creation or edit landed: every disk is exclusively owned and the
// right size, autostart matches, the persistent definition matches the expected one and the VM
// is in the state the operation left it (an edit of a running guest keeps it running).
func (e *ManagedEngine) observeDefinition(ctx context.Context, j managedJournal, state string) error {
	if j.Command.VM == nil || len(j.Domain) == 0 || len(j.Disks) != len(j.Command.VM.Disks) {
		return errors.New("VM definition evidence unavailable")
	}
	js, err := e.journals()
	if err != nil {
		return err
	}
	// Ownership is observed once per disk: exclusiveDisk rescans every domain each call, so this
	// costs disks × domains virsh calls. Batch it there if hosts ever carry many domains.
	for i, disk := range j.Disks {
		s, access, err := managedStorageByID(ctx, js, j.Command.VM.Disks[i].StorageID, 0)
		if err != nil {
			return err
		}
		if !strings.HasPrefix(disk, filepath.Join(access.Path, "nex-vms", j.Command.VM.ID)+"/") || s.ID != j.Command.VM.Disks[i].StorageID {
			return errors.New("created disk is outside its registered storage mount")
		}
		real, err := filepath.EvalSymlinks(disk)
		if err != nil || real != disk {
			return errors.New("created disk missing or replaced by symlink")
		}
		info, err := os.Stat(disk)
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("created disk unavailable")
		}
		if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
			return errors.New("created disk has hardlink aliases")
		}
		if err = checkImage(ctx, disk); err != nil {
			return err
		}
		image, err := run(ctx, "qemu-img", "info", "--force-share", "--output=json", disk)
		if err != nil {
			return err
		}
		var imageInfo struct {
			Size int64 `json:"virtual-size"`
		}
		if json.Unmarshal(image, &imageInfo) != nil || imageInfo.Size != int64(j.Command.VM.Disks[i].SizeGiB)<<30 {
			return errors.New("VM virtual disk size changed")
		}
		if err = (&Engine{config: Config{VirshURI: "qemu:///system"}}).exclusiveDisk(ctx, journal{Operation: Operation{VMID: j.Command.VM.ID}, Disk: disk}); err != nil {
			return err
		}
	}
	info, err := managedVirsh(ctx, "dominfo", j.Command.VM.ID)
	wantAutostart := j.Command.VM.Autostart && !managedUsesPoolSharedStorage(js, *j.Command.VM)
	if err != nil || autostartEnabled(info) != wantAutostart {
		return errors.New("VM autostart is not confirmed as requested")
	}
	b, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err != nil {
		return errors.New("cannot observe persistent VM definition")
	}
	if err = validateManagedDefinition(b, j.Domain); err != nil {
		return err
	}
	observed, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil || strings.TrimSpace(string(observed)) != state {
		return errors.New("VM state is not confirmed " + state + "; no effects replayed")
	}
	return nil
}

// autostartEnabled reads the Autostart line of virsh dominfo; run sets LC_ALL=C.
func autostartEnabled(b []byte) bool {
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) == 2 && f[0] == "Autostart:" {
			return f[1] == "enable"
		}
	}
	return false
}
func bootOrder(b *managedBootOrder) int {
	if b == nil {
		return 0
	}
	return b.Order
}
func validateManagedDefinition(b, expected []byte) error {
	var got, want managedDomain
	if xml.Unmarshal(b, &got) != nil || xml.Unmarshal(expected, &want) != nil {
		return errors.New("invalid persistent VM definition")
	}
	memory := got.Memory.Value
	if got.Memory.Unit == "KiB" || got.Memory.Unit == "k" {
		memory /= 1024
	}
	if got.UUID != want.UUID || got.Name != want.Name || got.Type != "kvm" || got.VCPU != want.VCPU || memory != want.Memory.Value || len(got.Devices.Disks) != len(want.Devices.Disks) || len(got.Devices.Interfaces) != len(want.Devices.Interfaces) {
		return errors.New("VM definition differs from requested ownership or resources")
	}
	// Devices are matched by identity (disk target, interface MAC), not position: libvirt
	// orders hot-plugged devices itself, and a renamed target or address is a real change.
	disks := map[string]managedDisk{}
	for _, disk := range got.Devices.Disks {
		if _, dup := disks[disk.Target.Dev]; dup {
			return errors.New("VM definition repeats a disk target")
		}
		disks[disk.Target.Dev] = disk
	}
	for _, expected := range want.Devices.Disks {
		disk, ok := disks[expected.Target.Dev]
		if !ok || disk.Type != expected.Type || disk.Device != expected.Device || diskFile(disk) != diskFile(expected) || disk.Driver.Type != expected.Driver.Type || (disk.Readonly != nil) != (expected.Readonly != nil) || disk.Shareable != nil || (disk.Source != nil && (disk.Source.Dev != "" || disk.Source.Protocol != "")) || disk.Target != expected.Target || bootOrder(disk.Boot) != bootOrder(expected.Boot) {
			return errors.New("VM disk assignment or boot order differs from requested ownership")
		}
	}
	// libvirt expands a machine alias such as q35 to its versioned form, and injects
	// check="none" (and migratable) on host-passthrough when the domain omitted check.
	if got.OS.Type.Arch != want.OS.Type.Arch || got.OS.Type.Value != want.OS.Type.Value || (got.OS.Type.Machine != want.OS.Type.Machine && !strings.HasPrefix(got.OS.Type.Machine, "pc-"+want.OS.Type.Machine+"-")) || (len(want.CPU.Features) > 0 && !sameCPUFeatures(got.CPU.Features, want.CPU.Features)) || got.CPU.Mode != want.CPU.Mode || got.CPU.Match != want.CPU.Match || !sameCPUCheck(got.CPU.Check, want.CPU.Check) || (got.CPU.Model == nil) != (want.CPU.Model == nil) || (got.CPU.Model != nil && *got.CPU.Model != *want.CPU.Model) {
		return errors.New("VM CPU or machine changed")
	}
	if want.CPU.Topology != nil && (got.CPU.Topology == nil || *got.CPU.Topology != *want.CPU.Topology) {
		return errors.New("VM CPU topology changed")
	}
	if (want.OS.Firmware == "efi" && got.OS.Firmware != "efi") || (want.OS.Firmware == "" && got.OS.Firmware != "" && got.OS.Firmware != "bios") {
		return errors.New("VM firmware changed")
	}
	if want.OS.NVRAM != nil && (got.OS.NVRAM == nil || got.OS.NVRAM.Path != want.OS.NVRAM.Path) {
		return errors.New("VM firmware variable store changed")
	}
	if err := checkFirmwareAndTPM(got, want); err != nil {
		return err
	}
	// Per-device boot order and an <os> boot list cannot coexist; only the former is requested.
	if len(got.OS.Boot) != 0 {
		return errors.New("VM boot order changed")
	}
	// The balloon distinguishes the modes: no device for dedicated, virtio free page reporting
	// for maximum. A libvirt that does not echo the attribute must not fail every such VM.
	if got.Devices.Memballoon.Model != want.Devices.Memballoon.Model || (got.Devices.Memballoon.FreePageReporting != "" && got.Devices.Memballoon.FreePageReporting != want.Devices.Memballoon.FreePageReporting) {
		return errors.New("VM memory mode changed")
	}
	if !reflect.DeepEqual(got.MemoryBacking, want.MemoryBacking) {
		return errors.New("VM physical memory reservation differs from requested configuration")
	}
	if want.CurrentMemory != nil {
		current := got.Memory // libvirt defaults an omitted currentMemory to memory.
		if got.CurrentMemory != nil {
			current = *got.CurrentMemory
		}
		if current.Unit == "KiB" || current.Unit == "k" {
			if current.Value != want.CurrentMemory.Value*1024 {
				return errors.New("VM current memory differs from requested allocation")
			}
		} else if current.Unit != "MiB" || current.Value != want.CurrentMemory.Value {
			return errors.New("VM current memory differs from requested allocation")
		}
	}
	var devices domainXML
	if xml.Unmarshal(b, &devices) != nil || len(devices.Devices.Hostdev) != 0 || len(devices.Devices.Filesystems) != 0 || len(got.Devices.Graphics) != 1 || got.Devices.Graphics[0].Type != "vnc" || got.Devices.Graphics[0].Listen.Type != "socket" {
		return errors.New("unsupported VM devices or exposed graphics")
	}
	nics := map[string]managedNIC{}
	for _, nic := range got.Devices.Interfaces {
		if _, dup := nics[nic.MAC.Address]; dup {
			return errors.New("VM definition repeats a MAC address")
		}
		nics[nic.MAC.Address] = nic
	}
	for _, w := range want.Devices.Interfaces {
		// Compared field by field rather than as whole structs: MTU is a pointer, so struct
		// equality would compare addresses and reject every adapter that carries one.
		nic, ok := nics[w.MAC.Address]
		if !ok || nic.Type != w.Type || nic.Source != w.Source || nic.Model != w.Model || !sameMTU(nic.MTU, w.MTU) {
			return errors.New("VM network assignment changed")
		}
	}
	return nil
}

// sameCPUCheck tolerates libvirt injecting check="none" when the domain omitted the attribute.
func sameCPUCheck(got, want string) bool {
	return got == want || (want == "" && got == "none")
}

// diskFile is a disk's backing file, empty for an empty CD drive.
func diskFile(d managedDisk) string {
	if d.Source == nil {
		return ""
	}
	return d.Source.File
}

// sameMTU enforces a requested size and tolerates one we did not request, matching how the
// balloon attribute is treated: a libvirt that adds its own default must not fail every VM.
func sameMTU(got, want *mtuXML) bool {
	return want == nil || (got != nil && *got == *want)
}

// carryFirmwareVariables keeps an existing UEFI store during an edit. Older Nex
// definitions let libvirt choose a name-derived path; moving them to the new UUID path would
// discard boot entries and Secure Boot variables. New definitions already carry the UUID path.
func carryFirmwareVariables(expected, current []byte) ([]byte, error) {
	var want, got managedDomain
	if xml.Unmarshal(expected, &want) != nil || xml.Unmarshal(current, &got) != nil {
		return nil, errors.New("cannot read VM firmware variable store")
	}
	if want.OS.Firmware == "efi" && got.OS.Firmware == "efi" {
		if got.OS.NVRAM == nil || got.OS.NVRAM.Path == "" {
			return nil, errors.New("existing UEFI VM has no firmware variable store; inspect it on the host before editing")
		}
		want.OS.NVRAM = &nvramXML{Path: got.OS.NVRAM.Path}
	}
	return xml.Marshal(want)
}

func firmwareRemoved(before, after protocol.VMCreateSpec) bool {
	return before.Boot.Firmware == "uefi" && after.Boot.Firmware != "uefi"
}

// firmwareVariablesFile accepts only the exact libvirt-owned NVRAM directory and one regular
// file directly within it. A missing file is valid for a UEFI domain that has never started;
// it also makes cleanup retries idempotent. Never follow a symlink from a privileged cleanup.
func firmwareVariablesFile(path string) error {
	if filepath.Clean(path) != path || filepath.Dir(path) != firmwareVariablesDir || filepath.Base(path) == "." {
		return errors.New("VM firmware variable store is outside libvirt's NVRAM directory")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("VM firmware variable store is not a regular file")
	}
	return nil
}

func removeFirmwareVariables(path string) error {
	if err := firmwareVariablesFile(path); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

const (
	secureBootVariablesStage = "updating-secure-boot-vars"
	secureBootDefiningStage  = "defining-secure-boot"
)

// secureBootChanged is deliberately separate from the XML feature change: libvirt chooses the
// firmware image, but OVMF enforces Secure Boot from this per-guest variable store.
func secureBootChanged(before, after protocol.VMCreateSpec) bool {
	return before.Boot.Firmware == "uefi" && after.Boot.Firmware == "uefi" && before.Boot.SecureBootEnabled() != after.Boot.SecureBootEnabled()
}

type firmwareVariablesJSON struct {
	Variables []struct {
		Name string `json:"name"`
		GUID string `json:"guid"`
		Data string `json:"data"`
	} `json:"variables"`
}

const (
	efiGlobalVariableGUID          = "8be4df61-93ca-11d2-aa0d-00e098032b8c"
	efiSecureBootEnableDisableGUID = "f0a30bc7-af08-4556-99c4-001009c93a44"
	efiCustomModeEnableGUID        = "c076ec0c-7028-4399-a072-71ee5c448b9f"
	efiImageSecurityDatabaseGUID   = "d719b2cb-3d3a-4596-a3bc-dad00e67656f"
)

// firmwareVariablesState uses the supported Debian virt-fw-vars JSON output, rather than
// parsing its human-readable print format. A Secure Boot enable requires an already enrolled
// PK and, when the offline store exposes it, SetupMode false; inventing or replacing keys belongs
// to a later explicit policy.
func firmwareVariablesState(ctx context.Context, path string) (enabled, enrolled bool, err error) {
	vars, err := readFirmwareVariables(ctx, path)
	if err != nil {
		return false, false, err
	}
	values := map[string]string{}
	for _, variable := range vars.Variables {
		if variable.Name == "SecureBootEnable" || variable.Name == "SetupMode" || variable.Name == "PK" {
			wantGUID := efiGlobalVariableGUID
			if variable.Name == "SecureBootEnable" {
				wantGUID = efiSecureBootEnableDisableGUID
			}
			if strings.ToLower(variable.GUID) != wantGUID {
				return false, false, errors.New("VM firmware variable has an unexpected EFI GUID")
			}
			if _, duplicate := values[variable.Name]; duplicate {
				return false, false, errors.New("VM firmware variable state is ambiguous")
			}
		}
		values[variable.Name] = variable.Data
	}
	secureBoot, ok := values["SecureBootEnable"]
	if !ok || (secureBoot != "00" && secureBoot != "01") {
		return false, false, errors.New("VM firmware SecureBootEnable state is invalid")
	}
	if setupMode, ok := values["SetupMode"]; ok && setupMode != "00" && setupMode != "01" {
		return false, false, errors.New("VM firmware SetupMode state is invalid")
	}
	enabled = secureBoot == "01"
	// SetupMode is a runtime-derived variable and OVMF's offline VARS templates commonly omit
	// it. A present setup-mode value is evidence; its absence is not. PK is the durable proof
	// that a previously Secure-Boot-on store remains enrolled.
	return enabled, values["PK"] != "" && values["SetupMode"] != "01", nil
}

func readFirmwareVariables(ctx context.Context, path string) (firmwareVariablesJSON, error) {
	b, err := run(ctx, "virt-fw-vars", "--input", path, "--output-json", "-")
	if err != nil {
		return firmwareVariablesJSON{}, errors.New("cannot inspect VM firmware variables: " + err.Error())
	}
	var vars firmwareVariablesJSON
	if err = json.Unmarshal(b, &vars); err != nil {
		return firmwareVariablesJSON{}, errors.New("cannot read VM firmware variable state")
	}
	seen := map[string]bool{}
	for _, variable := range vars.Variables {
		identity := variable.Name + "\x00" + strings.ToLower(variable.GUID)
		if seen[identity] {
			return firmwareVariablesJSON{}, errors.New("VM firmware variable store repeats an EFI variable")
		}
		seen[identity] = true
	}
	return vars, nil
}

// secureBootVariablesPreserved makes the native tool's intended logical in-place behavior an
// observed requirement: every existing variable except the documented Secure Boot controls must
// be byte-for-byte unchanged. This protects Boot####, BootOrder and operator-provided keys.
func secureBootVariablesPreserved(ctx context.Context, source, output string, enabled bool) error {
	before, err := readFirmwareVariables(ctx, source)
	if err != nil {
		return err
	}
	after, err := readFirmwareVariables(ctx, output)
	if err != nil {
		return err
	}
	key := func(name, guid string) string { return name + "\x00" + strings.ToLower(guid) }
	values := func(vars firmwareVariablesJSON) map[string]string {
		out := map[string]string{}
		for _, variable := range vars.Variables {
			out[key(variable.Name, variable.GUID)] = variable.Data
		}
		return out
	}
	allowed := func(k string) bool {
		return k == key("SecureBootEnable", efiSecureBootEnableDisableGUID) ||
			(enabled && (k == key("CustomMode", efiCustomModeEnableGUID) || k == key("dbx", efiImageSecurityDatabaseGUID)))
	}
	left, right := values(before), values(after)
	for k, value := range left {
		if !allowed(k) && right[k] != value {
			return errors.New("VM firmware tool changed a guest variable outside Secure Boot")
		}
	}
	for k := range right {
		if !allowed(k) {
			if _, exists := left[k]; !exists {
				return errors.New("VM firmware tool added an unexpected guest variable")
			}
		}
	}
	return nil
}

func firmwareVariablesDigest(path string) (string, os.FileInfo, error) {
	if err := firmwareVariablesFile(path); err != nil {
		return "", nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil, errors.New("VM firmware variable store does not exist; start the VM once or recreate it before changing Secure Boot")
		}
		return "", nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(h.Sum(nil)), info, nil
}

func secureBootMetadata(info os.FileInfo) (mode, uid, gid uint32, err error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0, errors.New("cannot read VM firmware variable store ownership")
	}
	return uint32(info.Mode().Perm()), stat.Uid, stat.Gid, nil
}

func preserveFirmwareXattrs(source, target string) error {
	n, err := syscall.Listxattr(source, nil)
	if err != nil {
		if errors.Is(err, syscall.ENOTSUP) {
			return nil
		}
		return err
	}
	if n == 0 {
		return nil
	}
	names := make([]byte, n)
	if _, err = syscall.Listxattr(source, names); err != nil {
		return err
	}
	for _, name := range strings.Split(strings.TrimSuffix(string(names), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		size, err := syscall.Getxattr(source, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		if _, err = syscall.Getxattr(source, name, value); err != nil {
			return err
		}
		if err = syscall.Setxattr(target, name, value, 0); err != nil {
			return err
		}
	}
	return nil
}

func secureBootTemporary(path string, mode, uid, gid uint32) (string, error) {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".secure-boot-")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err = f.Chmod(os.FileMode(mode)); err == nil {
		err = f.Chown(int(uid), int(gid))
	}
	if err == nil {
		err = preserveFirmwareXattrs(path, name)
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

func removeSecureBootTemporary(path string) error {
	if path == "" {
		return nil
	}
	if filepath.Clean(path) != path || filepath.Dir(path) != firmwareVariablesDir {
		return errors.New("VM firmware temporary file is outside libvirt's NVRAM directory")
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("VM firmware temporary file is not a regular file")
	}
	return os.Remove(path)
}

func syncFirmwareVariables(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return syncManagedDir(filepath.Dir(path))
}

func secureBootTemporaryValid(ctx context.Context, path string, j managedJournal) error {
	digest, info, err := firmwareVariablesDigest(path)
	if err != nil {
		return err
	}
	mode, uid, gid, err := secureBootMetadata(info)
	if err != nil || mode != j.SecureBootMode || uid != j.SecureBootUID || gid != j.SecureBootGID {
		return errors.New("VM firmware temporary file ownership changed")
	}
	if info.Size() == 0 {
		return errors.New("VM firmware temporary file is empty")
	}
	if j.SecureBootAfter != "" && digest != j.SecureBootAfter {
		return errors.New("VM firmware temporary file changed during Secure Boot update")
	}
	enabled, enrolled, err := firmwareVariablesState(ctx, path)
	if err != nil || enabled != j.SecureBootEnabled || (j.SecureBootEnabled && !enrolled) {
		return errors.New("VM firmware temporary file does not contain the requested Secure Boot state")
	}
	return nil
}

// prepareSecureBootVariables records the immutable source identity before any native mutation.
func (e *ManagedEngine) prepareSecureBootVariables(ctx context.Context, j *managedJournal) error {
	if j.SecureBootNVRAM != "" {
		return nil
	}
	if len(j.SecureBootPreviousDomain) == 0 {
		return errors.New("VM Secure Boot predecessor was not recorded; inspect the VM on its host")
	}
	path, digest, mode, uid, gid, err := secureBootSource(ctx, j.Domain, j.SecureBootEnabled)
	if err != nil {
		return err
	}
	j.SecureBootNVRAM, j.SecureBootBefore = path, digest
	j.SecureBootMode, j.SecureBootUID, j.SecureBootGID = mode, uid, gid
	return e.save(*j)
}

// secureBootSource reads, without changing anything, the variable store a Secure Boot edit
// would modify and refuses the edit when the store cannot carry the requested state.
func secureBootSource(ctx context.Context, domainXML []byte, enabled bool) (path, digest string, mode, uid, gid uint32, err error) {
	var domain managedDomain
	if xml.Unmarshal(domainXML, &domain) != nil || domain.OS.NVRAM == nil || domain.OS.NVRAM.Path == "" {
		return "", "", 0, 0, 0, errors.New("cannot read VM firmware variable store before changing Secure Boot")
	}
	path = domain.OS.NVRAM.Path
	digest, info, err := firmwareVariablesDigest(path)
	if err != nil {
		return "", "", 0, 0, 0, err
	}
	if enabled {
		_, enrolled, err := firmwareVariablesState(ctx, path)
		if err != nil {
			return "", "", 0, 0, 0, err
		}
		if !enrolled {
			return "", "", 0, 0, 0, errors.New("this VM's firmware variables have no enrolled Secure Boot keys; recreate the VM with Secure Boot on or wait for a Secure Boot enrollment policy")
		}
	}
	mode, uid, gid, err = secureBootMetadata(info)
	return path, digest, mode, uid, gid, err
}

// finishSecureBootVariables is safe after a process death: source and temporary hashes decide
// whether it may resume, and the desired firmware variable is observed before an atomic rename.
func (e *ManagedEngine) finishSecureBootVariables(ctx context.Context, j *managedJournal) error {
	if j.SecureBootNVRAM == "" || j.SecureBootBefore == "" {
		return errors.New("VM Secure Boot update was not journaled; inspect the VM on its host")
	}
	digest, info, err := firmwareVariablesDigest(j.SecureBootNVRAM)
	if err != nil {
		return err
	}
	mode, uid, gid, metaErr := secureBootMetadata(info)
	if metaErr != nil || mode != j.SecureBootMode || uid != j.SecureBootUID || gid != j.SecureBootGID {
		return errors.New("VM firmware variable store ownership changed during Secure Boot update")
	}
	if digest == j.SecureBootAfter && j.SecureBootAfter != "" {
		enabled, enrolled, err := firmwareVariablesState(ctx, j.SecureBootNVRAM)
		if err != nil || enabled != j.SecureBootEnabled || (j.SecureBootEnabled && !enrolled) {
			return errors.New("VM firmware variable store does not contain the requested Secure Boot state")
		}
		if err = removeSecureBootTemporary(j.SecureBootTemporary); err != nil {
			return err
		}
		j.Stage = secureBootDefiningStage
		return e.save(*j)
	}
	if digest != j.SecureBootBefore {
		return errors.New("VM firmware variable store changed during Secure Boot update; inspect the VM on its host")
	}
	if j.SecureBootTemporary == "" {
		j.SecureBootTemporary, err = secureBootTemporary(j.SecureBootNVRAM, j.SecureBootMode, j.SecureBootUID, j.SecureBootGID)
		if err != nil {
			return err
		}
		if err = e.save(*j); err != nil {
			return err
		}
	}
	if err = secureBootTemporaryValid(ctx, j.SecureBootTemporary, *j); err != nil {
		// A pre-created or partial output is never trusted. It is safe to replace only after the
		// source digest above proved the original store is still present.
		if j.SecureBootAfter != "" {
			j.SecureBootAfter = ""
			if saveErr := e.save(*j); saveErr != nil {
				return saveErr
			}
		}
		if err = removeSecureBootTemporary(j.SecureBootTemporary); err != nil {
			return err
		}
		j.SecureBootTemporary, err = secureBootTemporary(j.SecureBootNVRAM, j.SecureBootMode, j.SecureBootUID, j.SecureBootGID)
		if err != nil {
			return err
		}
		if err = e.save(*j); err != nil {
			return err
		}
		args := []string{"--input", j.SecureBootNVRAM, "--output", j.SecureBootTemporary}
		if j.SecureBootEnabled {
			args = append(args, "--secure-boot")
		} else {
			args = append(args, "--set-false", "SecureBootEnable")
		}
		if _, err = run(ctx, "virt-fw-vars", args...); err != nil {
			return errors.New("cannot update VM Secure Boot variables: " + err.Error())
		}
	}
	if err = secureBootTemporaryValid(ctx, j.SecureBootTemporary, *j); err != nil {
		return err
	}
	if err = secureBootVariablesPreserved(ctx, j.SecureBootNVRAM, j.SecureBootTemporary, j.SecureBootEnabled); err != nil {
		return err
	}
	if err = syncFirmwareVariables(j.SecureBootTemporary); err != nil {
		return err
	}
	j.SecureBootAfter, _, err = firmwareVariablesDigest(j.SecureBootTemporary)
	if err != nil {
		return err
	}
	if err = e.save(*j); err != nil {
		return err
	}
	if err = os.Rename(j.SecureBootTemporary, j.SecureBootNVRAM); err != nil {
		return err
	}
	if err = syncManagedDir(filepath.Dir(j.SecureBootNVRAM)); err != nil {
		return err
	}
	j.Stage = secureBootDefiningStage
	return e.save(*j)
}

func strictSecureBootPredecessor(got, expected []byte) bool {
	// Both values come from virsh dumpxml. Do not re-parse and project them through managedDomain:
	// that would silently ignore XML Core does not understand and then overwrite it. Whitespace at
	// the edges is the one harmless normalization accepted here.
	return bytes.Equal(bytes.TrimSpace(got), bytes.TrimSpace(expected))
}

// finishSecureBootDefinition resumes only the define known not to have been sent yet (the
// predecessor is still persistent), or accepts the desired definition after a crash. It never
// redefines an unrecognised domain.
func (e *ManagedEngine) finishSecureBootDefinition(ctx context.Context, j *managedJournal) error {
	b, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err != nil {
		return errors.New("cannot observe VM after Secure Boot update; inspect the VM on its host")
	}
	if validateManagedDefinition(b, j.Domain) == nil {
		j.Stage = "defined"
		return e.save(*j)
	}
	if len(j.SecureBootPreviousDomain) == 0 || !strictSecureBootPredecessor(b, j.SecureBootPreviousDomain) {
		return errors.New("VM definition changed during Secure Boot update; inspect the VM on its host")
	}
	domainPath := filepath.Join(e.stateDir, j.Command.OperationID+".xml")
	if err = atomicWrite(domainPath, j.Domain); err != nil {
		return err
	}
	if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
		return errors.New("VM Secure Boot redefinition outcome uncertain; observation required before recovery")
	}
	return nil
}

// firmwareRemovalPending reports a UEFI-to-BIOS edit whose BIOS define is not yet observed:
// either the undefine boundary (cleaning-nvram) or the define after it (defining, which only
// firmware removal enters with NVRAMPath recorded). Both resume through finishFirmwareRemoval,
// so an interrupted or refused define after undefine is retried instead of stranding the VM.
func firmwareRemovalPending(j managedJournal) bool {
	return j.Stage == "cleaning-nvram" || (j.Stage == "defining" && j.NVRAMPath != "")
}

// finishFirmwareRemoval is safe to retry after the journal records cleaning-nvram. A UEFI
// definition proves the BIOS redefine landed; an absent domain proves undefine landed. In both
// cases it removes the exact journaled file after path validation, because libvirt can report a
// successful --nvram undefine while leaving that file behind.
func (e *ManagedEngine) finishFirmwareRemoval(ctx context.Context, j *managedJournal) error {
	b, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err == nil {
		if validateManagedDefinition(b, j.Domain) == nil {
			if j.NVRAMPath == "" {
				return errors.New("VM firmware cleanup path was not recorded; inspect the VM on its host")
			}
			if err = removeFirmwareVariables(j.NVRAMPath); err != nil {
				return err
			}
			j.Stage = "defined"
			return nil
		}
		var d managedDomain
		if xml.Unmarshal(b, &d) != nil || d.OS.Firmware != "efi" {
			return errors.New("VM firmware cleanup state is not recognisable; inspect the VM on its host")
		}
		if j.Stage == "defining" {
			// Undefine already landed before this stage was saved; a UEFI domain now is not ours.
			return errors.New("VM definition changed during firmware cleanup; inspect the VM on its host")
		}
		if d.OS.NVRAM == nil || d.OS.NVRAM.Path == "" {
			return errors.New("existing UEFI VM has no firmware variable store; inspect it on the host before editing")
		}
		if j.NVRAMPath == "" {
			if err = firmwareVariablesFile(d.OS.NVRAM.Path); err != nil {
				return err
			}
			j.NVRAMPath = d.OS.NVRAM.Path
			if err = e.save(*j); err != nil {
				return err
			}
		} else if j.NVRAMPath != d.OS.NVRAM.Path {
			return errors.New("VM firmware variable store changed during cleanup; inspect the VM on its host")
		}
		if _, err = managedVirsh(ctx, "undefine", j.Command.VM.ID, "--nvram"); err != nil {
			return errors.New("VM firmware variable cleanup outcome uncertain; observation required before recovery")
		}
	} else {
		// A successful undefine leaves no persistent domain. Define is idempotent only after
		// that observation, never blindly replayed after an interrupted libvirt request.
		ids, listErr := managedVirsh(ctx, "list", "--all", "--uuid")
		if listErr != nil || slices.Contains(strings.Fields(string(ids)), j.Command.VM.ID) {
			return errors.New("VM firmware cleanup outcome uncertain; observation required before recovery")
		}
	}
	if j.NVRAMPath == "" {
		return errors.New("VM firmware cleanup path was not recorded; inspect the VM on its host")
	}
	if err = removeFirmwareVariables(j.NVRAMPath); err != nil {
		return err
	}
	domainPath := filepath.Join(e.stateDir, j.Command.OperationID+".xml")
	if err = atomicWrite(domainPath, j.Domain); err != nil {
		return err
	}
	j.Stage = "defining"
	if err = e.save(*j); err != nil {
		return err
	}
	if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
		return errors.New("VM redefinition outcome uncertain; observation required before recovery")
	}
	return nil
}

// reconcile resumes or observes an interrupted operation. The entry stays pending while a
// resume runs, so a helper's mid-way save never persists a provisional needs_attention; only
// a result still unsettled at the end becomes needs_attention, keeping its reason.
func (e *ManagedEngine) reconcile(ctx context.Context, j managedJournal) (r protocol.Result, err error) {
	j.Result.Status, j.Result.Error = "pending", ""
	defer func() { r = unsettledNeedsAttention(r) }()
	// Network and media removal teardown is idempotent (absent bridge, absent file, absent
	// ISO all mean done), so an interrupted removal settles by re-running it.
	if slices.Contains([]string{"storage-register", "storage-update", "storage-recheck", "storage-detach", "storage-credential-retire", "network-register", "network-remove", "network-update", "media-remove"}, j.Command.Kind) {
		js, err := e.journals()
		if err != nil {
			return j.Result, err
		}
		if err = e.apply(ctx, &j, js, nil); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
		} else {
			if errors.Is(err, ErrStorageRollbackUncertain) {
				j.Result.Status = "needs_attention"
			} else {
				j.Result.Status = "failed"
			}
			j.Result.Error = err.Error()
		}
	}
	if j.Command.Kind == "vm-create" && (j.Stage == "defining" || j.Stage == "defined") {
		// The start step was never reached, so the re-sent creation runs it once now.
		if err := e.observer()(ctx, j, "shut off"); err == nil {
			j.Stage = "defined"
			if err = e.startCreated(ctx, &j); err != nil {
				return j.Result, err
			}
			j.Result.Status, j.Result.Error = "succeeded", ""
		}
	}
	// An interrupted edit is proven the same way, in whichever state it was applied.
	if j.Command.Kind == "vm-update" && firmwareRemovalPending(j) {
		if err := e.finishFirmwareRemoval(ctx, &j); err == nil {
			if err = e.observer()(ctx, j, "shut off"); err == nil {
				j.Result.Status, j.Result.Error, j.Stage = "succeeded", "", "defined"
			} else {
				j.Result.Error = "interrupted edit: " + err.Error() + "; inspect the VM on its host, then stop it and submit the edit again"
			}
		} else {
			j.Result.Error = "interrupted edit: " + err.Error() + "; inspect the VM on its host, then stop it and submit the edit again"
		}
	}
	if j.Command.Kind == "vm-update" && j.Stage == secureBootVariablesStage {
		if err := e.finishSecureBootVariables(ctx, &j); err != nil {
			j.Result.Error = "interrupted edit: " + err.Error() + "; inspect the VM on its host, then stop it and submit the edit again"
		}
	}
	if j.Command.Kind == "vm-update" && j.Stage == secureBootDefiningStage {
		if err := e.finishSecureBootDefinition(ctx, &j); err != nil {
			j.Result.Error = "interrupted edit: " + err.Error() + "; inspect the VM on its host, then stop it and submit the edit again"
		}
	}
	if j.Command.Kind == "vm-update" && updateObservable(j.Stage) {
		if err := e.observer()(ctx, j, updateState(j)); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
			j.Stage = "defined"
		} else {
			j.Result.Error = "interrupted edit: " + err.Error() + "; inspect the VM on its host, then stop it and submit the edit again"
		}
	}
	if j.Command.Kind == "vm-create" && (j.Stage == "starting" || j.Stage == "started") {
		// The start request was issued or interrupted; libvirt's state says which, and a
		// recorded outcome stands. Either way the creation succeeded.
		if err := settleStart(ctx, &j); err == nil {
			j.Result.Status, j.Result.Error = "succeeded", ""
		} else {
			j.Result.Error = err.Error()
		}
	}
	if j.Command.Kind == "vm-power" {
		if j.Stage != "powering" {
			// Nothing was issued yet: run the request once now.
			js, err := e.journals()
			if err != nil {
				return j.Result, err
			}
			if err = e.apply(ctx, &j, js, nil); err == nil {
				j.Result.Status = "succeeded"
				j.Result.Error = ""
			} else {
				if errors.Is(err, errPowerRequestUncertain) {
					j.Result.Status = "needs_attention"
				} else {
					j.Result.Status = "failed"
				}
				j.Result.Error = err.Error()
			}
		} else if err := powerObserved(ctx, *j.Command.Power); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
		} else {
			j.Result.Error = err.Error()
		}
	}
	if j.Command.Kind == "vm-media" {
		if j.Stage != "changing-media" {
			js, err := e.journals()
			if err != nil {
				return j.Result, err
			}
			if err = e.apply(ctx, &j, js, nil); err == nil {
				j.Result.Status = "succeeded"
				j.Result.Error = ""
			} else {
				j.Result.Status = "failed"
				j.Result.Error = err.Error()
			}
		} else if err := mediaObserved(ctx, *j.Command.VMMedia, j.MediaPath); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
			if err := e.recordVMMedia(*j.Command.VMMedia); err != nil {
				return j.Result, err
			}
		} else {
			j.Result.Error = err.Error()
		}
	}
	if j.Command.Kind == "vm-delete" {
		if j.Stage != "undefining" && j.Stage != "wiping" {
			js, err := e.journals()
			if err != nil {
				return j.Result, err
			}
			if err = e.apply(ctx, &j, js, nil); err == nil {
				j.Result.Status = "succeeded"
				j.Result.Error = ""
			} else {
				j.Result.Status = "failed"
				if j.Stage == "undefining" || j.Stage == "wiping" {
					j.Result.Status = "needs_attention"
				}
				j.Result.Error = err.Error()
			}
		} else if err := e.resumeDelete(ctx, &j); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
		} else {
			j.Result.Error = err.Error()
		}
	}
	if j.Command.Kind == "iso-import" && j.MediaPath != "" {
		if err := verifyISO(j.MediaPath, journalMedia(j)); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
		} else {
			_ = os.Remove(j.MediaPath + ".partial")
		}
	}
	j.Result = unsettledNeedsAttention(j.Result)
	j.Result.ObservedAt = time.Now().UTC()
	return j.Result, e.save(j)
}

func unsettledNeedsAttention(r protocol.Result) protocol.Result {
	if r.Status == "pending" {
		r.Status = "needs_attention"
		if r.Error == "" {
			r.Error = "interrupted operation retained; inspect host resources before creating a new request; no effects replayed"
		}
	}
	return r
}

// vmEverHad reports whether any journaled definition of this VM, at any revision and whatever
// its result, satisfied want. Deletion of per-VM host state uses it rather than the current
// definition, because an edit that drops a device does not remove the state that device left.
func vmEverHad(js []managedJournal, vmID string, want func(protocol.VMCreateSpec) bool) bool {
	for _, j := range js {
		if j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update" {
			continue
		}
		if j.Command.VM != nil && j.Command.VM.ID == vmID && want(*j.Command.VM) {
			return true
		}
	}
	return false
}

// deleteVM removes a stopped managed VM from libvirt. Disk files are retained unless
// DeleteDisks is set; retained paths stay in the journal so storage detach still sees them.
func (e *ManagedEngine) deleteVM(ctx context.Context, j *managedJournal, js []managedJournal) error {
	req := j.Command.Delete
	if managedDeletedVMs(js)[req.VMID] {
		return errors.New("VM is already deleted on this host")
	}
	cur, err := managedCurrent(js, req.VMID)
	if err != nil {
		return err
	}
	if cur.Command.VM.PoolID != req.PoolID || cur.Command.VM.HostID != req.HostID {
		return errors.New("VM pool or host does not match this host's record")
	}
	for _, old := range js {
		if old.Command.OperationID == j.Command.OperationID || (old.Result.Status != "pending" && old.Result.Status != "needs_attention") {
			continue
		}
		id, _ := managedIdentity(old.Command)
		if id == req.VMID && old.Command.Kind == "vm-delete" && (old.Stage == "undefining" || old.Stage == "wiping") {
			// A replaced Controller never re-sends the interrupted delete, so a new delete for
			// the same VM finishes it, but only when both requests make the same disk choice:
			// a new request never wipes disks it asked to keep, nor settles a wipe as a retain.
			if old.Command.Delete.DeleteDisks != req.DeleteDisks {
				return errors.New("an earlier interrupted delete for this VM made a different disk choice; re-send the delete with delete_disks=" + strconv.FormatBool(old.Command.Delete.DeleteDisks) + " to finish it, or resolve it on the host")
			}
			r, err := e.reconcile(ctx, old)
			if err != nil {
				return err
			}
			if r.Status == "succeeded" {
				return errors.New("VM is already deleted on this host: an earlier interrupted delete has now finished")
			}
			return errors.New("an earlier interrupted delete for this VM is still unresolved: " + r.Error)
		}
		if id == req.VMID && (old.Command.Kind == "vm-create" || old.Command.Kind == "vm-update" || old.Command.Kind == "vm-power" || old.Command.Kind == "vm-media" || old.Command.Kind == "vm-delete") {
			return errors.New("a change, power, media or delete request for this VM is still unresolved")
		}
	}
	state, err := managedVirsh(ctx, "domstate", req.VMID)
	if err != nil {
		return errors.New("VM not found or its state cannot be established on this host")
	}
	if observed := strings.TrimSpace(string(state)); observed != "shut off" {
		return errors.New("stop the VM before deleting it; observed state is " + observed)
	}
	paths := managedDeleteDiskPaths(js, cur)
	j.Retained = append([]string{}, paths...)
	j.Stage = "undefining"
	if err = e.save(*j); err != nil {
		return err
	}
	return e.finishDelete(ctx, j, js, true)
}

// finishDelete runs the effects of a delete whose disk list is already journaled: undefine
// (when the domain is still defined) and, if requested, the wipe. A resumed delete re-runs
// whichever of them the interruption left undone; both are safe to repeat.
func (e *ManagedEngine) finishDelete(ctx context.Context, j *managedJournal, js []managedJournal, undefine bool) error {
	req := j.Command.Delete
	// The flags come from every revision the journal holds, failed ones included, but libvirt
	// removes NVRAM and swtpm state only for devices the persistent definition still carries
	// (BUG-0196, BUG-0214). Asking every revision does not recover state an earlier edit
	// dropped; NeedsStop refusing TPM removal and the firmware-removal cleanup are what keep
	// host state and definition in step. The flags matter only for devices still defined.
	args := []string{req.VMID}
	if vmEverHad(js, req.VMID, func(v protocol.VMCreateSpec) bool { return v.Boot.Firmware == "uefi" }) {
		args = append(args, "--nvram")
	}
	if vmEverHad(js, req.VMID, func(v protocol.VMCreateSpec) bool { return v.Boot.TPMEnabled() }) {
		args = append(args, "--tpm")
	}
	if undefine {
		if _, err := managedVirsh(ctx, append([]string{"undefine"}, args...)...); err != nil {
			return err
		}
	}
	if req.DeleteDisks {
		j.Stage = "wiping"
		if err := e.save(*j); err != nil {
			return err
		}
		if err := wipeManagedDisks(j.Retained); err != nil {
			return err
		}
		j.Retained = nil
	}
	defined := domains(ctx)
	if defined == nil {
		return errors.New("libvirt did not list domains after undefine; inspect the host before retrying")
	}
	if defined[strings.ToLower(req.VMID)] {
		return errors.New("VM domain is still defined after undefine")
	}
	return nil
}

// resumeDelete finishes a delete interrupted after its disk list was journaled. A domain that
// is still defined must be shut off before it is undefined; nothing else is re-decided.
func (e *ManagedEngine) resumeDelete(ctx context.Context, j *managedJournal) error {
	js, err := e.journals()
	if err != nil {
		return err
	}
	defined := domains(ctx)
	if defined == nil {
		return errors.New("libvirt did not list domains after an interrupted delete")
	}
	still := defined[strings.ToLower(j.Command.Delete.VMID)]
	if still {
		state, err := managedVirsh(ctx, "domstate", j.Command.Delete.VMID)
		if err != nil {
			return errors.New("VM state cannot be established after an interrupted delete")
		}
		if observed := strings.TrimSpace(string(state)); observed != "shut off" {
			return errors.New("stop the VM so its interrupted delete can finish; observed state is " + observed)
		}
	}
	return e.finishDelete(ctx, j, js, still)
}

func managedDeleteDiskPaths(js []managedJournal, cur managedJournal) []string {
	seen := map[string]bool{}
	var out []string
	add := func(path string) {
		if path == "" || seen[path] {
			return
		}
		seen[path] = true
		out = append(out, path)
	}
	for _, path := range cur.Disks {
		add(path)
	}
	for _, j := range js {
		if j.Result.Status != "succeeded" {
			continue
		}
		switch {
		case j.Command.VM != nil && j.Command.VM.ID == cur.Command.VM.ID:
			for _, path := range j.Retained {
				add(path)
			}
		case j.Command.Delete != nil && j.Command.Delete.VMID == cur.Command.VM.ID:
			for _, path := range j.Retained {
				add(path)
			}
		}
	}
	return out
}

func wipeManagedDisks(paths []string) error {
	dirs := map[string]bool{}
	for _, path := range paths {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("delete disk %s: %w", path, err)
		}
		dirs[filepath.Dir(path)] = true
	}
	for dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) > 0 {
			continue
		}
		_ = os.Remove(dir)
	}
	return nil
}

// powerVM issues one libvirt power request. Start and stop requests are idempotent against
// the observed state; shutdown and reboot are ACPI requests the guest decides how to honor;
// force-reboot issues virsh reset and requires a running guest. A start that would boot a
// shut-off guest rechecks explicit CPU features and available memory against the recorded
// spec. The spec is nil only when the managed journal holds no definition for the VM.
func powerVM(ctx context.Context, p protocol.VMPowerRequest, spec *protocol.VMCreateSpec) error {
	state, err := managedVirsh(ctx, "domstate", p.VMID)
	if err != nil {
		return errors.New("VM not found or its state cannot be established on this host")
	}
	current := strings.TrimSpace(string(state))
	// A guest asleep in ACPI S3 is an active domain that runs nothing: it cannot take an
	// ACPI request until woken. Start wakes it; shutdown and reboot wake it first and then
	// ask; the force actions act on the QEMU process and need no guest cooperation.
	asleep := current == domainAsleep
	if asleep {
		current = "running"
	}
	command := ""
	switch p.Action {
	case "start":
		if asleep {
			command = "dompmwakeup"
		} else if current != "running" {
			command = "start"
		}
	case "shutdown":
		if current != "shut off" {
			command = "shutdown"
		}
	case "reboot":
		if current != "running" {
			return errors.New("VM is not running; reboot needs a running guest")
		}
		command = "reboot"
	case "force-reboot":
		if current != "running" {
			return errors.New("VM is not running; force-reboot needs a running guest")
		}
		command = "reset"
	case "force-stop":
		if current != "shut off" {
			command = "destroy"
		}
	}
	if command == "" {
		return nil
	}
	if current != "running" && current != "shut off" {
		return errors.New("VM is " + current + "; resolve that state before a power request")
	}
	if asleep && (command == "shutdown" || command == "reboot") {
		if _, err = managedVirsh(ctx, "dompmwakeup", p.VMID); err != nil {
			return powerRequestError{action: p.Action, cause: err}
		}
	}
	if command == "start" && spec != nil {
		if len(spec.CPU.Features) > 0 {
			if err := NativeCPUCompare(ctx, spec.CPU.Model, spec.CPU.Features, managedMachine(*spec)); err != nil {
				return err
			}
		}
		available, _, err := hostMemory()
		if err != nil {
			return err
		}
		if !managedMemoryFits(available, spec.Memory) {
			return errors.New("insufficient available memory to start this VM including 2 GiB host reserve")
		}
	}
	if _, err = managedVirsh(ctx, command, p.VMID); err != nil {
		// The request crossed into libvirt before this failed, so the error says nothing about
		// what the guest did. Ask the domain. A shutdown whose client was killed while libvirt
		// held the domain job had already stopped the guest, and reporting that as a refusal
		// sent the operator to inspect a VM that had done exactly what was asked (BUG-0213).
		// powerObserved holds the rules for what an observation can prove: only shut off settles
		// a shutdown or force-stop, and a reboot still running stays uncertain, so this can
		// promote a request to succeeded but never invents one.
		if powerObserved(ctx, p) == nil {
			return nil
		}
		return powerRequestError{action: p.Action, cause: err}
	}
	return nil
}

// powerObserved reconciles an interrupted power request from the current state alone.
// Reboot and force-reboot stay uncertain while still running: observation cannot prove the
// reset completed. Force-stop and shutdown succeed only when the guest is shut off.
func powerObserved(ctx context.Context, p protocol.VMPowerRequest) error {
	state, err := managedVirsh(ctx, "domstate", p.VMID)
	if err != nil {
		return errors.New("VM state cannot be established after an interrupted power request")
	}
	current := strings.TrimSpace(string(state))
	switch p.Action {
	case "start":
		if current == "running" {
			return nil
		}
	case "force-stop", "shutdown":
		if current == "shut off" {
			return nil
		}
	}
	// An ACPI or reset request may or may not have taken effect; only the guest's state can tell.
	return errors.New("power request was interrupted; VM is " + current + "; observe the guest and submit a new request")
}

// changeMedia inserts or ejects ISO media on a managed VM. An empty SATA tray is attached
// when the domain has none; QEMU cannot hot-plug SATA, so a running VM without a tray is refused.
func (e *ManagedEngine) changeMedia(ctx context.Context, j *managedJournal, js []managedJournal) error {
	req := *j.Command.VMMedia
	vm, err := managedVM(js, req.VMID)
	if err != nil {
		return err
	}
	path := ""
	if req.Action == "insert" {
		// The recorded definition must keep the two drives on different ISOs (ValidateVMSpec),
		// whatever the requesting Controller believes; refuse before touching libvirt.
		if driveName(req.Drive) == protocol.DriversDrive && req.MediaID == vm.MediaID {
			return errors.New("that ISO is already in the install drive; the driver disc must differ from the install media")
		}
		if driveName(req.Drive) == protocol.InstallDrive && req.MediaID == vm.DriversMediaID {
			return errors.New("that ISO is already in the driver disc drive; the install media must differ from the driver disc")
		}
		m, mediaPath, err := managedMedia(js, req.MediaID)
		if err != nil {
			return err
		}
		if storageWasPrepared(js, m.StorageID) {
			if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
				return err
			}
		}
		if err = verifyISO(mediaPath, m); err != nil {
			return err
		}
		path = mediaPath
	}
	running, err := managedRunning(ctx, req.VMID)
	if err != nil {
		return err
	}
	dev := ""
	live := running
	if req.Action == "eject" {
		source, tray, err := cdromSource(ctx, req.VMID, req.Drive, running)
		if err != nil {
			return err
		}
		if running && source == "" {
			// The guest opened its own tray: the running domain is already empty, but the
			// persistent definition still names the ISO and would re-attach it at the next
			// start. Clear that one, and only that one: virsh refuses an eject on a tray
			// that already holds no media.
			if source, tray, err = cdromSource(ctx, req.VMID, req.Drive, false); err != nil {
				return err
			}
			live = false
		}
		if tray == "" || source == "" {
			// Nothing to eject: do not attach a tray, or call virsh, just to remove from it.
			return e.recordVMMedia(req)
		}
		dev = tray
	} else if dev, err = ensureCDROM(ctx, e.stateDir, req.VMID, req.Drive, running); err != nil {
		return err
	}
	// Persisted immediately before the virsh call, with the resolved media path, so an
	// interruption after this point reconciles by observing the tray and one before it is
	// simply re-applied.
	j.Stage, j.MediaPath = "changing-media", path
	if err = e.save(*j); err != nil {
		return err
	}
	args := []string{"change-media", req.VMID, dev, "--force"}
	if live {
		args = append(args, "--live")
	}
	args = append(args, "--config")
	if req.Action == "eject" {
		args = append(args, "--eject")
	} else {
		args = append(args, path, "--update")
	}
	if _, err = managedVirsh(ctx, args...); err != nil {
		return errors.New("libvirt refused the media change; inspect the VM on its host")
	}
	return e.recordVMMedia(req)
}

// managedRunning reports whether the domain is running, so callers read the live definition
// rather than a persistent one the guest may no longer match.
func managedRunning(ctx context.Context, id string) (bool, error) {
	state, err := managedVirsh(ctx, "domstate", id)
	if err != nil {
		return false, errors.New("VM not found or its state cannot be established on this host")
	}
	observed := strings.TrimSpace(string(state))
	return observed == "running" || observed == domainAsleep, nil
}

// dumpManagedDomain reads a domain's definition: the live one while running, otherwise the
// persistent one. Both ensureCDROM and cdromSource read the CD drive from this same parse.
func dumpManagedDomain(ctx context.Context, id string, running bool) (managedDomain, error) {
	args := []string{"dumpxml", id}
	if !running {
		args = append(args, "--inactive")
	}
	b, err := managedVirsh(ctx, args...)
	if err != nil {
		return managedDomain{}, errors.New("VM definition cannot be read on this host")
	}
	var d managedDomain
	if err = xml.Unmarshal(b, &d); err != nil {
		return managedDomain{}, err
	}
	return d, nil
}

// cdromMedia is what a tray holds: a file, or a host device (<source dev>) attached outside
// Nex, which counts as present the same as a file.
func cdromMedia(disk managedDisk) string {
	if src := diskFile(disk); src != "" {
		return src
	}
	if disk.Source != nil {
		return disk.Source.Dev
	}
	return ""
}

// cdromDrives resolves a domain's CD drives by drive name. The user aliases Nex writes are
// authoritative. A domain defined before the driver-disc drive existed carries neither alias,
// so the first cdrom it holds is the install drive; the drivers drive of such a domain is
// reported absent rather than guessed, because inserting a disc into the wrong tray of a
// running guest is not a recoverable mistake.
func cdromDrives(d managedDomain) map[string]*managedDisk {
	out := map[string]*managedDisk{}
	var unaliased *managedDisk
	for i := range d.Devices.Disks {
		disk := &d.Devices.Disks[i]
		if disk.Device != "cdrom" {
			continue
		}
		switch {
		case disk.Alias != nil && disk.Alias.Name == cdromInstallAlias:
			out[protocol.InstallDrive] = disk
		case disk.Alias != nil && disk.Alias.Name == cdromDriversAlias:
			out[protocol.DriversDrive] = disk
		case unaliased == nil && (disk.Alias == nil || !strings.HasPrefix(disk.Alias.Name, "ua-nex-")):
			unaliased = disk
		}
	}
	if out[protocol.InstallDrive] == nil && unaliased != nil {
		out[protocol.InstallDrive] = unaliased
	}
	return out
}

// cdromSource reports the named CD drive's target, empty when the domain has no such drive, and
// the media its tray holds. It is a read-only check so an eject on a VM with no drive, or an
// already-empty one, is recognized as already satisfied instead of failing a virsh call with
// nothing to eject. A running domain is read live: its tray may have been changed outside Nex,
// and only the live definition proves what the guest currently sees.
func cdromSource(ctx context.Context, id, drive string, running bool) (source, dev string, err error) {
	d, err := dumpManagedDomain(ctx, id, running)
	if err != nil {
		return "", "", err
	}
	tray := cdromDrives(d)[driveName(drive)]
	if tray == nil {
		return "", "", nil
	}
	return cdromMedia(*tray), tray.Target.Dev, nil
}

// driveName resolves the drive a request names; "" is the install drive, which is what every
// Controller sent before the second drive existed.
func driveName(drive string) string {
	if drive == "" {
		return protocol.InstallDrive
	}
	return drive
}

// ensureCDROM returns the named SATA cdrom target, attaching an empty tray when the domain has
// none. SATA cannot hot-plug, so a running domain missing that drive must be stopped first.
func ensureCDROM(ctx context.Context, stateDir, id, drive string, running bool) (string, error) {
	d, err := dumpManagedDomain(ctx, id, running)
	if err != nil {
		return "", err
	}
	alias, err := driveAlias(drive)
	if err != nil {
		return "", err
	}
	if tray := cdromDrives(d)[driveName(drive)]; tray != nil {
		return tray.Target.Dev, nil
	}
	if running {
		if driveName(drive) == protocol.DriversDrive {
			return "", errors.New("the VM has no driver disc drive while running; shut it down, then insert the driver disc to add one")
		}
		return "", errors.New("the VM has no CD drive while running; shut it down, then insert or eject media to add one")
	}
	used := map[string]bool{}
	for _, disk := range d.Devices.Disks {
		used[disk.Target.Dev] = true
	}
	dev := "sda"
	for i := 0; used[dev]; i++ {
		dev = "sd" + string(rune('a'+i+1))
		if i > 24 {
			return "", errors.New("no free SATA target for a CD drive")
		}
	}
	tray := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Target: targetXML{Dev: dev, Bus: "sata"}, Readonly: &struct{}{}, Alias: &aliasXML{Name: alias}}
	xmlBytes, err := xml.Marshal(tray)
	if err != nil {
		return "", err
	}
	path := filepath.Join(stateDir, "cdrom-"+id+".xml")
	if err = os.WriteFile(path, xmlBytes, 0600); err != nil {
		return "", err
	}
	defer os.Remove(path)
	if _, err = managedVirsh(ctx, "attach-device", id, path, "--config"); err != nil {
		return "", errors.New("libvirt refused to add an empty CD drive; inspect the VM on its host")
	}
	return dev, nil
}

func managedVM(js []managedJournal, id string) (protocol.VMCreateSpec, error) {
	cur, err := managedCurrent(js, id)
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	return *cur.Command.VM, nil
}

// managedCurrent returns a VM's current definition and the journal entry carrying its disk
// layout: the highest succeeded revision, where creation is revision 0.
func managedCurrent(js []managedJournal, id string) (managedJournal, error) {
	var cur managedJournal
	found := false
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.VM == nil || j.Command.VM.ID != id || (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") {
			continue
		}
		if !found || j.Command.VM.Revision > cur.Command.VM.Revision {
			cur, found = j, true
		}
	}
	if !found {
		return cur, errors.New("VM is not defined on this host")
	}
	if len(cur.Targets) != len(cur.Command.VM.Disks) || cur.MediaTarget == "" {
		cur.Targets, cur.MediaTarget, cur.DriversTarget = managedTargets(*cur.Command.VM)
	}
	if cur.DriversTarget == "" {
		cur.DriversTarget = driversTarget(cur.Targets, cur.MediaTarget)
	}
	return cur, nil
}

// recordVMMedia updates the VM's current journal entry so Applied reports the current media.
func (e *ManagedEngine) recordVMMedia(req protocol.VMMediaRequest) error {
	js, err := e.journals()
	if err != nil {
		return err
	}
	cur, err := managedCurrent(js, req.VMID)
	if err != nil {
		return err
	}
	v := protocol.ApplyVMMedia(*cur.Command.VM, req.Action, req.Drive, req.MediaID)
	cur.Command.VM = &v
	return e.save(cur)
}

// mediaObserved reconciles an interrupted media change by observing the CD drive. The journal
// records Stage "changing-media" and the resolved media path together, immediately before the
// virsh call, so an insert reaching here always knows the path it was to install.
func mediaObserved(ctx context.Context, req protocol.VMMediaRequest, path string) error {
	running, err := managedRunning(ctx, req.VMID)
	if err != nil {
		return errors.New("VM definition cannot be established after an interrupted media change")
	}
	got, _, err := cdromSource(ctx, req.VMID, req.Drive, running)
	if err != nil {
		return errors.New("VM definition cannot be established after an interrupted media change")
	}
	want := ""
	if req.Action == "insert" {
		want = path
	}
	if got == want {
		if req.Action == "eject" && running {
			// A guest can empty its own tray, so a live-empty drive alone does not prove
			// the eject reached the persistent definition, which would otherwise re-attach
			// the ISO at the next start.
			persistent, _, err := cdromSource(ctx, req.VMID, req.Drive, false)
			if err != nil || persistent != "" {
				return errors.New("media change was interrupted; observe the CD drive and submit a new request")
			}
		}
		return nil
	}
	return errors.New("media change was interrupted; observe the CD drive and submit a new request")
}

// Default resources every host owns without operator action: its local libvirt image
// directory as storage named "Local storage", and one untagged network per host bridge that
// sits on a physical NIC or bond, named "<port> untagged" with the port recorded as the uplink.
//
// The name is only the default applied the first time this path is registered; RegisterDefaults
// matches an existing registration on DefaultStoragePath, never on its name, so a host that
// registered this directory under an earlier default keeps the name its pool master recorded
// and is not renamed, duplicated or re-registered by an upgrade (BUG-0205).
const DefaultStorageName = "Local storage"

// DefaultStoragePath is the host directory RegisterDefaults publishes as local storage.
// Tests may point it at a temporary tree.
var DefaultStoragePath = "/var/lib/libvirt/images"

// DefaultNetworks derives the untagged default network of every bridge on a physical port.
func DefaultNetworks(nics []protocol.NIC) []protocol.NetworkRegistration {
	var out []protocol.NetworkRegistration
	for _, bridge := range nics {
		if bridge.Kind != "bridge" || !managedSource.MatchString(bridge.Name) {
			continue
		}
		for _, port := range bridge.Ports(nics) {
			if protocol.ValidUplink(port) {
				out = append(out, protocol.NetworkRegistration{Name: port + " untagged", Kind: "bridge", Source: bridge.Name, Uplink: port})
				break
			}
		}
	}
	return out
}

// hostID mints a host-generated resource or operation identity.
func hostID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// RegisterDefaults self-registers the host's default storage and untagged networks in the
// managed journal, through the same verification a Controller-issued registration gets, so a
// Controller only mirrors them. A journal entry already holding the path or bridge, in any
// state and whoever issued it, means nothing is added. pool is the host's own pool identity;
// a legacy host that has none passes its host identity, since a Controller assigns the pool on
// adoption and matches by resource ID only. It is safe to call on every uplink change.
func (e *ManagedEngine) RegisterDefaults(ctx context.Context, pool, host string, nics []protocol.NIC) error {
	if !identifier.MatchString(host) || !identifier.MatchString(pool) {
		return errors.New("defaults need the enrolled host and pool identity")
	}
	js, err := e.journals()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var commands []protocol.Command
	if !slices.ContainsFunc(js, func(j managedJournal) bool {
		return j.Command.Storage != nil && j.Command.Storage.Path == DefaultStoragePath
	}) {
		// Skip while the directory is absent so a lab host or a Core install that has not
		// created the path yet does not journal a failed registration that blocks a later
		// successful default. Unexpected lstat errors still surface.
		if _, err := os.Lstat(DefaultStoragePath); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			commands = append(commands, protocol.Command{OperationID: hostID(), Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: hostID(), PoolID: pool, HostID: host, Name: DefaultStorageName, Path: DefaultStoragePath}})
		}
	}
	for _, n := range DefaultNetworks(nics) {
		if slices.ContainsFunc(js, func(j managedJournal) bool {
			return j.Command.Network != nil && j.Command.Network.Kind != "network" && j.Command.Network.Source == n.Source
		}) {
			continue
		}
		n.ID, n.PoolID, n.HostID = hostID(), pool, host
		commands = append(commands, protocol.Command{OperationID: hostID(), Kind: "network-register", Network: &n})
	}
	for _, c := range commands {
		if _, err := e.Execute(ctx, c, nil); err != nil {
			return err
		}
	}
	return nil
}

// managedLayout is an edit's resolved device plan: the path and target of every disk in the
// new definition (existing disks keep theirs, matched by name), the disks to allocate, the
// files of removed disks to retain, and the space each storage target must provide.
type managedLayout struct {
	Disks, Targets             []string
	MediaTarget, DriversTarget string
	New                        []int
	Retained                   []string
	Targets_                   []managedTarget
}

// managedUpdateLayout keeps every existing disk where it is and places each new disk in the
// VM's directory on its storage target at the first unused disk-<n>.qcow2, so a retained or
// half-allocated file is never reused. Target names come from the journal; new disks take the
// next free letter on their bus.
func managedUpdateLayout(js []managedJournal, cur managedJournal, v protocol.VMCreateSpec) (managedLayout, error) {
	l := managedLayout{Disks: make([]string, len(v.Disks)), Targets: make([]string, len(v.Disks)), MediaTarget: cur.MediaTarget, DriversTarget: cur.DriversTarget}
	known := map[string]int{}
	used := map[string]bool{cur.MediaTarget: true, cur.DriversTarget: true}
	for i, d := range cur.Command.VM.Disks {
		known[d.Name] = i
		used[cur.Targets[i]] = true
	}
	kept := map[string]bool{}
	at := map[string]int{}
	for i, d := range v.Disks {
		if old, ok := known[d.Name]; ok {
			l.Disks[i], l.Targets[i] = cur.Disks[old], cur.Targets[old]
			kept[d.Name] = true
			continue
		}
		s, err := managedStorage(js, d.StorageID)
		if err != nil {
			return l, err
		}
		index, ok := at[d.StorageID]
		if !ok {
			index = len(l.Targets_)
			at[d.StorageID] = index
			path := storageRegistrationPath(s)
			l.Targets_ = append(l.Targets_, managedTarget{ID: d.StorageID, Path: path, Dir: filepath.Join(path, "nex-vms", v.ID), Bytes: 4 << 30})
		}
		l.Targets_[index].Bytes += uint64(d.SizeGiB) << 30
		dir := l.Targets_[index].Dir
		taken := map[string]bool{}
		for _, path := range append(append([]string{}, cur.Disks...), l.Disks...) {
			taken[path] = true
		}
		path := ""
		for n := 0; n < 64 && path == ""; n++ {
			candidate := filepath.Join(dir, fmt.Sprintf("disk-%d.qcow2", n))
			if _, err := os.Lstat(candidate); errors.Is(err, os.ErrNotExist) && !taken[candidate] {
				path = candidate
			}
		}
		if path == "" {
			return l, errors.New("the VM directory holds too many retained disk files; clean it up on the host first")
		}
		target, err := nextTarget(d.Bus, used)
		if err != nil {
			return l, err
		}
		l.Disks[i], l.Targets[i] = path, target
		l.New = append(l.New, i)
	}
	for i, d := range cur.Command.VM.Disks {
		if !kept[d.Name] {
			l.Retained = append(l.Retained, cur.Disks[i])
		}
	}
	return l, nil
}

// update applies one revision of a VM's definition. A shut-off guest is redefined from the
// regenerated XML after any new disks exist and a rename; a running guest only takes the
// device additions, removals and media changes NeedsStop allows, applied live and persisted.
// Either way the persistent definition is then observed against the expected one.
func (e *ManagedEngine) update(ctx context.Context, j *managedJournal, js []managedJournal) error {
	v := *j.Command.VM
	cur, err := managedCurrent(js, v.ID)
	if err != nil {
		return err
	}
	if v.Revision != cur.Command.VM.Revision+1 {
		return fmt.Errorf("VM definition revision %d is not current; refresh the VM and edit again", v.Revision)
	}
	stops, err := protocol.NeedsStop(*cur.Command.VM, v)
	if err != nil {
		return err
	}
	state, err := managedVirsh(ctx, "domstate", v.ID)
	if err != nil {
		return errors.New("VM not found or its state cannot be established on this host")
	}
	observed := strings.TrimSpace(string(state))
	if observed == domainAsleep {
		return errors.New("VM is asleep; use Start to wake it before editing it")
	}
	if observed != "running" && observed != "shut off" {
		return errors.New("VM is " + observed + "; resolve that state before editing it")
	}
	running := observed == "running"
	if running && len(stops) > 0 {
		names := make([]string, len(stops))
		for i, s := range stops {
			names[i] = string(s)
		}
		return errors.New("stop the VM first to change " + strings.Join(names, ", "))
	}
	if running && v.Memory.Mode == "dedicated" {
		// Live device edits cannot retrofit memory backing. Refuse before allocating or
		// attaching anything, so an older guest can be upgraded with a stopped save.
		b, err := managedVirsh(ctx, "dumpxml", v.ID, "--inactive")
		var domain managedDomain
		if err != nil || xml.Unmarshal(b, &domain) != nil {
			return errors.New("cannot verify dedicated memory reservation before editing")
		}
		if !reflect.DeepEqual(domain.MemoryBacking, dedicatedMemoryBacking()) {
			return errors.New("stop the VM and save its settings to apply the dedicated memory reservation before live editing")
		}
	}
	if running {
		// A detach drops an interface from the persistent definition at once, but the live device
		// stays until the guest acknowledges the unplug, and firmware or guests without hotplug
		// never do. Attaching a new interface with that address would put one MAC on the bridge
		// twice, so it is refused here, before allocating or attaching anything.
		persistent, err := dumpManagedDomain(ctx, v.ID, false)
		if err != nil {
			return err
		}
		live, err := dumpManagedDomain(ctx, v.ID, true)
		if err != nil {
			return err
		}
		held := map[string]bool{}
		for _, n := range live.Devices.Interfaces {
			held[n.MAC.Address] = true
		}
		for _, n := range persistent.Devices.Interfaces {
			delete(held, n.MAC.Address)
		}
		for i, n := range v.NICs {
			if mac := protocol.NICMAC(v.ID, i, n); held[mac] {
				return fmt.Errorf("network interface MAC %s is still attached to the running VM; wait for the guest to release it or stop the VM, then save again", mac)
			}
		}
	}
	if v.Name != cur.Command.VM.Name {
		names, err := managedVirsh(ctx, "list", "--all", "--name")
		if err != nil {
			return err
		}
		for _, name := range strings.Split(string(names), "\n") {
			if name == v.Name {
				return errors.New("VM name already exists")
			}
		}
	}
	layout, err := managedUpdateLayout(js, cur, v)
	if err != nil {
		return err
	}
	for _, t := range layout.Targets_ {
		_, access, readyErr := managedStorageByID(ctx, js, t.ID, t.Bytes)
		if readyErr != nil {
			return readyErr
		}
		if access.Path != t.Path {
			return errors.New("storage mount identity changed before disk allocation")
		}
	}
	nets := make([]protocol.NetworkRegistration, len(v.NICs))
	for i, requested := range v.NICs {
		if nets[i], err = managedNetwork(js, requested.NetworkID); err != nil {
			return err
		}
		if err = verifyNetwork(ctx, nets[i]); err != nil {
			return err
		}
	}
	mediaPath, driversPath := "", ""
	for _, resolve := range []struct {
		id   string
		into *string
	}{{v.MediaID, &mediaPath}, {v.DriversMediaID, &driversPath}} {
		if resolve.id == "" {
			continue
		}
		m, path, err := publishedMediaPath(js, resolve.id)
		if err != nil {
			return err
		}
		if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
			return err
		}
		if err = verifyISO(path, m); err != nil {
			return err
		}
		*resolve.into = path
	}
	if !running {
		if err = managedCapacity(ctx, v, js); err != nil {
			return err
		}
	}
	j.Disks, j.Targets, j.MediaTarget, j.DriversTarget = layout.Disks, layout.Targets, layout.MediaTarget, layout.DriversTarget
	j.MediaPath, j.DriversPath, j.Retained = mediaPath, driversPath, layout.Retained
	if j.Domain, err = managedDomainXML(v, nets, j.Disks, j.Targets, j.MediaTarget, mediaPath, j.DriversTarget, driversPath); err != nil {
		return err
	}
	var current []byte
	if cur.Command.VM.Boot.Firmware == "uefi" && v.Boot.Firmware == "uefi" {
		// A journal written before explicit NVRAM paths cannot tell us libvirt's generated
		// path. Read the stopped definition before a rename and retain that path, so an
		// upgrade does not replace an installed guest's firmware variables.
		var readErr error
		current, readErr = managedVirsh(ctx, "dumpxml", v.ID, "--inactive")
		if readErr != nil {
			return errors.New("cannot read VM firmware variable store before editing")
		}
		if j.Domain, err = carryFirmwareVariables(j.Domain, current); err != nil {
			return err
		}
		if secureBootChanged(*cur.Command.VM, v) {
			j.SecureBootPreviousDomain = current
		}
	}
	j.Stage = "allocating"
	if err = e.save(*j); err != nil {
		return err
	}
	for _, t := range layout.Targets_ {
		if err = managedRepositorySubdir(ctx, js, t.ID, filepath.Join(t.Path, "nex-vms")); err != nil {
			return err
		}
		if err = managedRepositorySubdir(ctx, js, t.ID, t.Dir); err != nil {
			return err
		}
	}
	for _, i := range layout.New {
		if err = allocateDisk(ctx, j.Disks[i], v.Disks[i]); err != nil {
			return err
		}
	}
	for _, t := range layout.Targets_ {
		if err = syncManagedDir(t.Dir); err != nil {
			return err
		}
	}
	if running {
		j.Stage = "attaching"
		if err = e.save(*j); err != nil {
			return err
		}
		if err = e.applyLive(ctx, v.ID, j.Domain, mediaPath, driversPath); err != nil {
			return err
		}
	} else if err = e.applyStopped(ctx, j, *cur.Command.VM); err != nil {
		return err
	}
	if err = setAutostart(ctx, v, js); err != nil {
		return err
	}
	if err = e.observeDefinition(ctx, *j, observed); err != nil {
		return err
	}
	j.Stage = "defined"
	return nil
}

// applyStopped writes a stopped VM's new definition: Secure Boot variables, UEFI-to-BIOS
// cleanup, rename and define, each journaled so recovery observes before any retry.
func (e *ManagedEngine) applyStopped(ctx context.Context, j *managedJournal, prev protocol.VMCreateSpec) error {
	v := *j.Command.VM
	var err error
	removeFirmware := firmwareRemoved(prev, v)
	changingSecureBoot := secureBootChanged(prev, v)
	if changingSecureBoot {
		if v.Name != prev.Name && j.SecureBootNVRAM == "" {
			// Decide every read-only refusal before the rename, so a refused edit leaves
			// libvirt untouched rather than renamed under a failed Nex record.
			if _, _, _, _, _, err = secureBootSource(ctx, j.Domain, v.Boot.SecureBootEnabled()); err != nil {
				return err
			}
		}
		if v.Name != prev.Name {
			// libvirt refuses to define an existing UUID under another name, so rename
			// before touching the variable store, then re-observe the renamed predecessor
			// (a legacy name-derived NVRAM path may move with it).
			if _, err = managedVirsh(ctx, "domrename", v.ID, "--", v.Name); err != nil {
				return fmt.Errorf("libvirt refused the rename (%v); inspect the VM on its host", err)
			}
			current, readErr := managedVirsh(ctx, "dumpxml", v.ID, "--inactive")
			if readErr != nil {
				return errors.New("cannot read VM firmware variable store after rename")
			}
			if j.Domain, err = carryFirmwareVariables(j.Domain, current); err != nil {
				return err
			}
			j.SecureBootPreviousDomain = current
		}
		// The XML selects the matching OVMF build, but the existing variable store is
		// authoritative for enforcement and guest boot entries. Journal its native edit
		// before invoking the tool; recovery never replaces it with a template.
		j.SecureBootEnabled = v.Boot.SecureBootEnabled()
		j.Stage = secureBootVariablesStage
		if err = e.prepareSecureBootVariables(ctx, j); err != nil {
			return err
		}
		if err = e.finishSecureBootVariables(ctx, j); err != nil {
			return err
		}
		if err = e.finishSecureBootDefinition(ctx, j); err != nil {
			return err
		}
	}
	if removeFirmware {
		// Libvirt can remove NVRAM only while the persistent definition still describes
		// UEFI. Persist this boundary first; finishFirmwareRemoval observes before any
		// retry, so an interruption cannot replay an ambiguous undefine.
		j.Stage = "cleaning-nvram"
		if err = e.save(*j); err != nil {
			return err
		}
		if err = e.finishFirmwareRemoval(ctx, j); err != nil {
			return err
		}
	}
	if v.Name != prev.Name && !removeFirmware && !changingSecureBoot {
		if _, err = managedVirsh(ctx, "domrename", v.ID, "--", v.Name); err != nil {
			return fmt.Errorf("libvirt refused the rename (%v); inspect the VM on its host", err)
		}
	}
	if !removeFirmware && !changingSecureBoot {
		domainPath := filepath.Join(e.stateDir, j.Command.OperationID+".xml")
		if err = atomicWrite(domainPath, j.Domain); err != nil {
			return err
		}
		j.Stage = "defining"
		if err = e.save(*j); err != nil {
			return err
		}
		if _, err = managedVirsh(ctx, "define", domainPath); err != nil {
			return errors.New("VM redefinition outcome uncertain; observation required before recovery")
		}
	}
	return nil
}

// allocateDisk creates one new qcow2 the way creation does and proves it before use.
func allocateDisk(ctx context.Context, path string, d protocol.VMDisk) error {
	if _, err := run(ctx, "qemu-img", qemuCreateArgs(path, d)...); err != nil {
		return errors.New("disk allocation outcome uncertain; inspect retained VM directory")
	}
	if err := checkImage(ctx, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0600); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	return err
}

// SetManagedVMAutostart makes libvirt's own autostart flag for one managed domain match what
// this host is currently allowed to do. libvirt starts its autostart domains at boot, before
// Core's agent runs, so no agent-side admission can stop them; disabling the flag is the only
// way a drained or restricted host stays empty across a reboot.
//
// disable is a suppression, never a new desired value: the operator's choice stays in the
// managed journal, which is Nex's own durable record of the definition, so clearing the
// suppression restores exactly what was asked for with no operator action and nothing to
// migrate. It never starts or stops a guest.
func (e *ManagedEngine) SetManagedVMAutostart(ctx context.Context, v protocol.VMCreateSpec, disable bool) error {
	js, err := e.journals()
	if err != nil {
		return err
	}
	if disable {
		v.Autostart = false
	}
	return setAutostart(ctx, v, js)
}

// setAutostart makes libvirt's autostart flag match the definition; it never starts a guest.
func setAutostart(ctx context.Context, v protocol.VMCreateSpec, js []managedJournal) error {
	args := []string{"autostart", v.ID}
	if !v.Autostart || managedUsesPoolSharedStorage(js, v) {
		args = []string{"autostart", "--disable", v.ID}
	}
	if _, err := managedVirsh(ctx, args...); err != nil {
		return errors.New("VM autostart could not be set; inspect the VM on its host")
	}
	return nil
}

// applyLive reconciles a running guest's persistent definition towards the expected one with
// device operations only: attach the disks and interfaces it lacks, detach the ones it should
// not have, and change each CD drive's media. Comparing against what libvirt actually holds,
// rather than the journal, makes a re-submitted edit after an interruption converge.
func (e *ManagedEngine) applyLive(ctx context.Context, id string, expected []byte, mediaPath, driversPath string) error {
	b, err := managedVirsh(ctx, "dumpxml", id, "--inactive")
	if err != nil {
		return errors.New("cannot read the VM's persistent definition")
	}
	var got, want managedDomain
	if xml.Unmarshal(b, &got) != nil || xml.Unmarshal(expected, &want) != nil {
		return errors.New("invalid persistent VM definition")
	}
	device := func(v any) (string, error) {
		fragment, err := xml.Marshal(v)
		if err != nil {
			return "", err
		}
		path := filepath.Join(e.stateDir, id+".device.xml")
		return path, atomicWrite(path, fragment)
	}
	op := func(action string, v any) error {
		path, err := device(v)
		if err != nil {
			return err
		}
		defer os.Remove(path)
		if _, err = managedVirsh(ctx, action, id, path, "--persistent"); err != nil {
			return fmt.Errorf("libvirt refused to %s a device on the running VM (%v); the guest may need to release it, or stop the VM and edit again", strings.TrimSuffix(action, "-device"), err)
		}
		return nil
	}
	have := map[string]managedDisk{}
	var cdrom *managedDisk
	for i, d := range got.Devices.Disks {
		if d.Device == "cdrom" {
			cdrom = &got.Devices.Disks[i]
			continue
		}
		have[d.Target.Dev] = d
	}
	wantDisks := map[string]bool{}
	for _, d := range want.Devices.Disks {
		if d.Device == "cdrom" {
			continue
		}
		wantDisks[d.Target.Dev] = true
		if _, ok := have[d.Target.Dev]; !ok {
			if err = op("attach-device", d); err != nil {
				return err
			}
		}
	}
	for dev, d := range have {
		if !wantDisks[dev] {
			if err = op("detach-device", d); err != nil {
				return err
			}
		}
	}
	nics := map[string]managedNIC{}
	for _, n := range got.Devices.Interfaces {
		nics[n.MAC.Address] = n
	}
	wantNICs := map[string]bool{}
	for _, n := range want.Devices.Interfaces {
		wantNICs[n.MAC.Address] = true
		if _, ok := nics[n.MAC.Address]; !ok {
			if err = op("attach-device", n); err != nil {
				return err
			}
		}
	}
	for mac, n := range nics {
		if !wantNICs[mac] {
			if err = op("detach-device", n); err != nil {
				return err
			}
		}
	}
	if cdrom == nil {
		return errors.New("the VM has no CD drive; stop it and edit again to add one")
	}
	// A VM defined before the driver-disc drive existed has only the install drive. SATA cannot
	// hot-plug, so the second drive is added to the persistent definition alone and appears at
	// the next start; without it the definition would never match the expected one and every
	// live edit of such a VM would fail its observation.
	if cdromDrives(got)[protocol.DriversDrive] == nil {
		if extra := cdromDrives(want)[protocol.DriversDrive]; extra != nil {
			path, err := device(*extra)
			if err != nil {
				return err
			}
			defer os.Remove(path)
			if _, err = managedVirsh(ctx, "attach-device", id, path, "--config"); err != nil {
				return fmt.Errorf("libvirt refused to add the driver disc drive (%v); stop the VM and save its settings", err)
			}
			if got, err = dumpManagedDomain(ctx, id, false); err != nil {
				return err
			}
		}
	}
	// The guest owns its trays while it runs: it can eject the disc the persistent definition
	// still names, and it keeps one this definition no longer does. Each side is changed only
	// where it differs, because virsh refuses an eject on a tray that is already empty.
	liveDomain, err := dumpManagedDomain(ctx, id, true)
	if err != nil {
		return errors.New("cannot read the running VM's CD drive")
	}
	persistent, live := cdromDrives(got), cdromDrives(liveDomain)
	for _, drive := range []struct {
		name, path string
	}{{protocol.InstallDrive, mediaPath}, {protocol.DriversDrive, driversPath}} {
		tray := persistent[drive.name]
		if tray == nil {
			continue
		}
		liveMedia := ""
		if held := live[drive.name]; held != nil {
			liveMedia = cdromMedia(*held)
		}
		if diskFile(*tray) == drive.path && liveMedia == drive.path {
			continue
		}
		args := []string{"change-media", id, tray.Target.Dev, "--force"}
		if liveMedia != drive.path {
			args = append(args, "--live")
		}
		if diskFile(*tray) != drive.path {
			args = append(args, "--config")
		}
		if drive.path == "" {
			args = append(args, "--eject")
		} else {
			args = append(args, drive.path, "--update")
		}
		if _, err = managedVirsh(ctx, args...); err != nil {
			return fmt.Errorf("libvirt refused the media change on the running VM (%v); inspect the VM on its host", err)
		}
	}
	return nil
}
