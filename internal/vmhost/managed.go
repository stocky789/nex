// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
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

func (e powerRequestError) Error() string {
	return "libvirt refused the " + e.action + " request; inspect the VM on its host: " + e.cause.Error()
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
	// once and kept across edits so a guest never sees its disks renamed. MediaTarget is the
	// CD drive every VM carries.
	Targets     []string `json:"targets,omitempty"`
	MediaTarget string   `json:"media_target,omitempty"`
	// Retained lists disk files an edit detached from the VM; edits never delete data.
	Retained []string `json:"retained,omitempty"`
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
		if resolved != *j.DownloadedMedia || validateManaged(cmd) != nil {
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
			return old.Result, nil
		}
	}
	if len(js) >= 10000 {
		return result, errors.New("managed journal full; manual recovery required")
	}
	j := managedJournal{Command: c, Result: result, Stage: "validating"}
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
		if c.CredentialRetire != nil && old.Command.CredentialRetire != nil && old.Command.CredentialRetire.Receipt == c.CredentialRetire.Receipt && (old.Result.Status == "pending" || old.Result.Status == "needs_attention") {
			j.Result.Error = "storage credential retirement is unresolved; inspect or reconcile it before retrying"
			return j.Result, e.save(j)
		}
		if old.Command.Kind == "vm-update" && (old.Result.Status == "failed" || old.Result.Status == "needs_attention") {
			continue
		}
		if managedResourceID(old.Command) == managedResourceID(c) {
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
		if errors.Is(err, ErrStorageRollbackUncertain) || errors.Is(err, errPowerRequestUncertain) || j.Stage == "allocating" || j.Stage == "defining" || j.Stage == "starting" || j.Stage == "attaching" {
			j.Result.Status = "needs_attention"
		}
		j.Result.Error = err.Error()
	}
	return j.Result, e.save(j)
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
	for _, present := range []bool{c.Storage != nil, c.Network != nil, c.Media != nil, c.VM != nil, c.Power != nil, c.VMMedia != nil, c.StorageLifecycle != nil, c.CredentialRetire != nil} {
		if present {
			count++
		}
	}
	if count != 1 {
		return errors.New("exactly one command payload is required")
	}
	var id, pool, host string
	switch c.Kind {
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
	case "vm-media":
		if c.VMMedia == nil {
			return errors.New("media-change payload required")
		}
		m := c.VMMedia
		id, pool, host = m.VMID, m.PoolID, m.HostID
		if !uuidPattern.MatchString(m.VMID) || !protocol.ValidMediaAction(m.Action) {
			return errors.New("invalid VM identity or media action")
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
	if ((c.Kind != "storage-register" || id != "") && !identifier.MatchString(id)) || !identifier.MatchString(pool) || !identifier.MatchString(host) {
		return errors.New("invalid resource scope")
	}
	return nil
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

// managedStoragePrepare is reserved for registration and configuration changes. Ordinary
// lifecycle reads must inspect an existing mount/session and never replay login or mount work.
// Native preparation is provisional until readiness and capacity succeed; otherwise Core
// tears the attachment down so a refused registration cannot leave sessions or mounts behind.
func (e *ManagedEngine) managedStoragePrepare(ctx context.Context, s protocol.StorageRegistration, required uint64) (storageAccess, error) {
	credentialRoot := filepath.Join(filepath.Dir(e.stateDir), credentialDirectory)
	prepared, err := storagePrepareAtFn(ctx, s, credentialRoot)
	if err != nil {
		return storageAccess{}, err
	}
	verified := s
	verified.Path, verified.Identity = prepared.Path, prepared.Observed
	access, err := managedStorageReadyFn(ctx, verified, required)
	if err == nil {
		return access, nil
	}
	if cleanupErr := storageDetachAt(context.Background(), verified, credentialRoot); cleanupErr != nil {
		return storageAccess{}, errors.Join(err, fmt.Errorf("%w: clean up rejected storage preparation: %v", ErrStorageRollbackUncertain, cleanupErr))
	}
	return storageAccess{}, err
}

// registerStorage keeps a successful native prepare provisional until post-prepare
// conflict checks and the writable probe succeed. Cleanup failures become uncertain.
func (e *ManagedEngine) registerStorage(ctx context.Context, j *managedJournal, js []managedJournal, c protocol.Command) (retErr error) {
	requested := *c.Storage
	if j.AppliedStorage != nil {
		requested.ID = j.AppliedStorage.ID
	}
	storageCorePaths(&requested)
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
	applied.Path, applied.Generation = access.Path, 1
	applied.Identity = access.Observed
	applied.Capabilities = storageCapabilities(applied)
	applied.Access = protocol.StorageAccess{Status: access.Health, Readable: true, Writable: !access.ReadOnly, TotalBytes: access.Total, AvailableBytes: &access.Available, ObservedAt: time.Now().UTC()}
	j.AppliedStorage = &applied
	committed = true
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
	return protocol.StorageCapabilities{VMDisk: true, ISO: true, Thin: true, Thick: thick, EligibleHostIDs: []string{s.HostID}}
}

func managedStorageByID(ctx context.Context, js []managedJournal, id string, required uint64) (protocol.StorageRegistration, storageAccess, error) {
	s, err := managedStorage(js, id)
	if err != nil {
		return protocol.StorageRegistration{}, storageAccess{}, err
	}
	a, err := managedStorageReady(ctx, s, required)
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
			if j.Result.Status != "succeeded" || j.Command.StorageLifecycle == nil {
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
		if j.Result.Status != "succeeded" || j.Command.StorageLifecycle == nil || j.Command.StorageLifecycle.Action != "detach" {
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
	for _, j := range js {
		if j.Result.Status != "succeeded" {
			continue
		}
		if j.Command.Media != nil && j.Command.Media.StorageID == id {
			out = append(out, "ISO "+j.Command.Media.ID)
		}
		if len(j.Retained) == 0 || j.Command.VM == nil {
			continue
		}
		s, err := managedStorage(js, id)
		if err == nil {
			for _, path := range j.Retained {
				if path == s.Path || strings.HasPrefix(path, s.Path+"/") {
					out = append(out, "retained disk for VM "+j.Command.VM.ID)
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

func managedNetwork(js []managedJournal, id string) (protocol.NetworkRegistration, error) {
	for _, j := range js {
		if j.Result.Status == "succeeded" && j.Command.Network != nil && j.Command.Network.ID == id {
			return *j.Command.Network, nil
		}
	}
	return protocol.NetworkRegistration{}, errors.New("network is not registered on this host")
}

// managedMedia resolves an imported ISO and the path this host published it to.
func managedMedia(js []managedJournal, id string) (protocol.Media, string, error) {
	for _, j := range js {
		if j.Result.Status == "succeeded" && j.Command.Media != nil && j.Command.Media.ID == id {
			return journalMedia(j), j.MediaPath, nil
		}
	}
	return protocol.Media{}, "", errors.New("ISO is not imported on this host")
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
		case "storage-register", "storage-update", "storage-recheck", "storage-detach":
		case "network-register":
			n := *c.Network
			n.OperationID, n.Status, n.Error = "", "", ""
			out.Networks = append(out.Networks, n)
		case "iso-import":
			m := journalMedia(j)
			m.OperationID, m.Status, m.Error = "", "", ""
			out.Media = append(out.Media, m)
		case "vm-create", "vm-update":
			// The current definition is the highest succeeded revision; the create entry is
			// revision 0.
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
		}
	}
	for _, s := range storageProjection(js) {
		s.OperationID, s.Status, s.Error = "", "", ""
		out.Storage = append(out.Storage, s)
	}
	for _, s := range storageTombstones(js) {
		out.Storage = append(out.Storage, s)
	}
	if receipts, err := StorageCredentialReceipts(filepath.Dir(e.stateDir)); err == nil {
		for i := range receipts {
			receipts[i].RetireEligible = receipts[i].Status == "ready" && !storageCredentialAttached(js, receipts[i].Receipt)
		}
		out.CredentialReceipts = receipts
	}
	now := time.Now().UTC()
	journal := protocol.Check{Name: "managed-journal", Status: "healthy", ObservedAt: now}
	if len(bad) > 0 {
		journal.Status, journal.Error = "failed", "unreadable managed journal entries skipped: "+strings.Join(bad, "; ")
	}
	vms := protocol.Check{Name: "managed-vms", Status: "healthy", ObservedAt: now}
	if len(out.VMs) > 0 {
		if defined := domains(ctx); defined == nil {
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

// managedIdentity is the resource ID and operator name a command addresses.
func managedIdentity(c protocol.Command) (string, string) {
	switch c.Kind {
	case "storage-register":
		return c.Storage.ID, c.Storage.Name
	case "storage-update", "storage-recheck", "storage-detach":
		if c.StorageLifecycle != nil {
			return c.StorageLifecycle.RepositoryID, ""
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
	return stage == "defining" || stage == "attaching" || stage == "defined"
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
	s, access, err := managedStorageByID(ctx, js, id, 0)
	if err != nil {
		return err
	}
	if s.StorageKind() == protocol.StorageKindLocal {
		return managedSubdir(path)
	}
	root := access.Path
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
		if c.Power.Action == "start" {
			v, currentErr := managedCurrent(js, c.Power.VMID)
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
		return powerVM(ctx, *c.Power)
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
			_, err := managedStorageReady(ctx, current, 0)
			return err
		case "detach":
			if deps := storageDependencies(js, current.ID); len(deps) > 0 {
				return errors.New("storage cannot detach while used by " + strings.Join(deps, ", "))
			}
			return storageDetachAt(ctx, current, filepath.Join(filepath.Dir(e.stateDir), credentialDirectory))
		case "update":
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
			return nil
		}
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
	if err != nil || !info.Mode().IsRegular() || info.Size() != m.SizeBytes || info.Mode().Perm()&0222 != 0 {
		return errors.New("ISO size or immutable permissions changed")
	}
	h := sha256.New()
	if _, err = io.Copy(h, io.LimitReader(f, m.SizeBytes+1)); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
		return errors.New("ISO SHA-256 changed")
	}
	return nil
}

type managedOS struct {
	Firmware string        `xml:"firmware,attr,omitempty"`
	Type     osTypeXML     `xml:"type"`
	Boot     []managedBoot `xml:"boot"`
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
}
type cpuModelXML struct {
	Fallback string `xml:"fallback,attr,omitempty"`
	Value    string `xml:",chardata"`
}

// Maximum uses a virtio balloon to report free guest pages; dedicated has no balloon
// and uses physical memory backing below.
type managedBalloon struct {
	Model             string `xml:"model,attr"`
	FreePageReporting string `xml:"freePageReporting,attr,omitempty"`
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
	Features      featuresXML           `xml:"features"`
	CPU           managedCPU            `xml:"cpu"`
	Devices       struct {
		Disks       []managedDisk       `xml:"disk"`
		Controllers []managedController `xml:"controller"`
		Interfaces  []managedNIC        `xml:"interface"`
		Channels    []channelXML        `xml:"channel"`
		Inputs      []inputXML          `xml:"input"`
		Graphics    []graphicsXML       `xml:"graphics"`
		Video       []videoXML          `xml:"video"`
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

// managedTargets assigns creation-time target names positionally, plus the CD drive's.
func managedTargets(v protocol.VMCreateSpec) ([]string, string) {
	used := map[string]bool{}
	targets := make([]string, len(v.Disks))
	for i, d := range v.Disks {
		targets[i], _ = nextTarget(d.Bus, used)
	}
	media, _ := nextTarget("sata", used)
	return targets, media
}

// q35Machine reports whether a machine type is the PCIe q35 family, which takes root ports.
func q35Machine(machine string) bool {
	return machine == "q35" || strings.HasPrefix(machine, "pc-q35-")
}

// managedDomainXML builds the persistent definition. nets, disks and targets are positional:
// nets[i] carries NICs[i]'s registration, disks[i] the path allocated for Disks[i] and
// targets[i] its libvirt target. Every VM carries one SATA CD drive at mediaTarget so an ISO
// can be inserted or ejected later without hot-plugging a SATA device; it is empty when iso is.
func managedDomainXML(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks, targets []string, mediaTarget, iso string) ([]byte, error) {
	if len(nets) != len(v.NICs) || len(disks) != len(v.Disks) || len(targets) != len(v.Disks) || mediaTarget == "" {
		return nil, errors.New("domain devices do not match the requested VM")
	}
	d := managedDomain{Type: "kvm", Name: v.Name, UUID: v.ID, Memory: memoryXML{"MiB", v.Memory.SizeMiB}, VCPU: v.CPU.Count, OS: managedOS{Type: osTypeXML{"x86_64", managedMachine(v), "hvm"}}}
	if v.Boot.Firmware == "uefi" {
		d.OS.Firmware = "efi"
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
		d.Devices.Memballoon = managedBalloon{Model: "virtio", FreePageReporting: "on"}
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
	cdrom := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Target: targetXML{Dev: mediaTarget, Bus: "sata"}, Readonly: &struct{}{}}
	if iso != "" {
		cdrom.Source = &sourceXML{File: iso}
	}
	if v.Boot.MediaBootOrder > 0 {
		cdrom.Boot = &managedBootOrder{v.Boot.MediaBootOrder}
	}
	d.Devices.Disks = append(d.Devices.Disks, cdrom)
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
// but must still be able to reach its ceiling, so preflight refuses what the host cannot meet.
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
// included, because what is promised or reserved does not wait for a result.
func currentDefinitions(js []managedJournal) map[string]protocol.VMCreateSpec {
	out := map[string]protocol.VMCreateSpec{}
	for _, j := range js {
		if (j.Command.Kind != "vm-create" && j.Command.Kind != "vm-update") || j.Command.VM == nil || j.Result.Status == "failed" {
			continue
		}
		if cur, ok := out[j.Command.VM.ID]; !ok || j.Command.VM.Revision > cur.Revision {
			out[j.Command.VM.ID] = *j.Command.VM
		}
	}
	return out
}

// dedicatedFits admission-controls a dedicated VM against total host memory: what is already
// promised, this VM and the 2 GiB host reserve must fit. Maximum mode reserves nothing.
func dedicatedFits(totalKiB uint64, committedMiB int, m protocol.VMMemory) bool {
	return m.Mode != "dedicated" || uint64(committedMiB+m.SizeMiB+2048)*1024 <= totalKiB
}
func managedCapacity(ctx context.Context, v protocol.VMCreateSpec, js []managedJournal) error {
	if runtime.GOARCH != "amd64" || v.CPU.Count > runtime.NumCPU() {
		return errors.New("requires amd64 with sufficient host CPUs")
	}
	f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
	if err != nil {
		return errors.New("KVM acceleration unavailable")
	}
	f.Close()
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return err
	}
	available, total := uint64(0), uint64(0)
	for _, line := range strings.Split(string(mem), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "MemAvailable:" {
			available, _ = strconv.ParseUint(fields[1], 10, 64)
		}
		if len(fields) > 1 && fields[0] == "MemTotal:" {
			total, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if !managedMemoryFits(available, v.Memory) {
		return errors.New("insufficient available memory including 2 GiB host reserve")
	}
	if !dedicatedFits(total, dedicatedCommitted(js, v.ID), v.Memory) {
		return errors.New("dedicated memory exceeds what this host has left to promise; free a dedicated VM or use the maximum memory mode")
	}
	b, err := managedVirsh(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", managedMachine(v))
	if err != nil {
		return errors.New("cannot verify KVM capabilities for this machine type")
	}
	if v.CPU.Model != "host-passthrough" && v.CPU.Model != "host-model" {
		if models, e := UsableCPUModels(b); e != nil || !slices.Contains(models, v.CPU.Model) {
			return fmt.Errorf("CPU model %s is not available on this host", v.CPU.Model)
		}
	}
	var caps struct {
		Domain string `xml:"domain"`
		Arch   string `xml:"arch"`
		OS     struct {
			Supported string `xml:"supported,attr"`
			Enums     []struct {
				Name   string   `xml:"name,attr"`
				Values []string `xml:"value"`
			} `xml:"enum"`
		} `xml:"os"`
	}
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
	mediaPath := ""
	if v.MediaID != "" {
		m, path, err := managedMedia(js, v.MediaID)
		if err != nil {
			return err
		}
		// The ISO must sit on one of the VM's own storage targets, at the path this host published.
		on := ""
		for _, t := range targets {
			if t.ID == m.StorageID {
				on = t.Path
			}
		}
		if on == "" || path != filepath.Join(on, "nex-media", m.ID+".iso") {
			return errors.New("the install ISO must be published on one of the VM's storage targets")
		}
		if storageWasPrepared(js, m.StorageID) {
			if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
				return err
			}
		}
		if err = verifyISO(path, m); err != nil {
			return err
		}
		mediaPath = path
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
	j.Targets, j.MediaTarget = managedTargets(v)
	j.MediaPath = mediaPath
	j.Domain, err = managedDomainXML(v, nets, disks, j.Targets, j.MediaTarget, mediaPath)
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
	// Autostart is a libvirt property of the defined domain, not a start: nothing here runs a guest.
	if v.Autostart {
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
	if err := powerVM(ctx, protocol.VMPowerRequest{VMID: j.Command.VM.ID, Action: "start"}); err != nil {
		// The create journal has historically exposed a stable concise start outcome;
		// direct vm-power results retain the bounded native diagnostic for recovery.
		var powerErr powerRequestError
		if errors.As(err, &powerErr) {
			j.Start = "libvirt refused the start request; inspect the VM on its host"
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
		if c.Network != nil && old.Command.Network != nil && old.Command.Network.Source == c.Network.Source && (old.Command.Network.Kind == "network") == (c.Network.Kind == "network") {
			return errors.New("network " + c.Network.Source + " is already registered on this host as " + old.Command.Network.Name)
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
	if err != nil || autostartEnabled(info) != j.Command.VM.Autostart {
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
	if got.OS.Type.Arch != want.OS.Type.Arch || got.OS.Type.Value != want.OS.Type.Value || (got.OS.Type.Machine != want.OS.Type.Machine && !strings.HasPrefix(got.OS.Type.Machine, "pc-"+want.OS.Type.Machine+"-")) || got.CPU.Mode != want.CPU.Mode || got.CPU.Match != want.CPU.Match || !sameCPUCheck(got.CPU.Check, want.CPU.Check) || (got.CPU.Model == nil) != (want.CPU.Model == nil) || (got.CPU.Model != nil && *got.CPU.Model != *want.CPU.Model) {
		return errors.New("VM CPU or machine changed")
	}
	if want.CPU.Topology != nil && (got.CPU.Topology == nil || *got.CPU.Topology != *want.CPU.Topology) {
		return errors.New("VM CPU topology changed")
	}
	if (want.OS.Firmware == "efi" && got.OS.Firmware != "efi") || (want.OS.Firmware == "" && got.OS.Firmware != "" && got.OS.Firmware != "bios") {
		return errors.New("VM firmware changed")
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
func (e *ManagedEngine) reconcile(ctx context.Context, j managedJournal) (protocol.Result, error) {
	j.Result.Status = "needs_attention"
	j.Result.Error = "interrupted operation retained; inspect host resources before creating a new request; no effects replayed"
	if j.Command.Kind == "storage-register" || j.Command.Kind == "storage-update" || j.Command.Kind == "storage-recheck" || j.Command.Kind == "storage-detach" || j.Command.Kind == "storage-credential-retire" || j.Command.Kind == "network-register" {
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
			j.Result.Status, j.Result.Error, j.Stage = "succeeded", "", "defined"
			if err = e.startCreated(ctx, &j); err != nil {
				return j.Result, err
			}
		}
	}
	// An interrupted edit is proven the same way, in whichever state it was applied.
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
	if j.Command.Kind == "iso-import" && j.MediaPath != "" {
		if err := verifyISO(j.MediaPath, journalMedia(j)); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
		} else {
			_ = os.Remove(j.MediaPath + ".partial")
		}
	}
	j.Result.ObservedAt = time.Now().UTC()
	return j.Result, e.save(j)
}

// powerVM issues one libvirt power request. Start and stop requests are idempotent against
// the observed state; shutdown and reboot are ACPI requests the guest decides how to honor.
func powerVM(ctx context.Context, p protocol.VMPowerRequest) error {
	state, err := managedVirsh(ctx, "domstate", p.VMID)
	if err != nil {
		return errors.New("VM not found or its state cannot be established on this host")
	}
	current := strings.TrimSpace(string(state))
	command := ""
	switch p.Action {
	case "start":
		if current != "running" {
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
	if _, err = managedVirsh(ctx, command, p.VMID); err != nil {
		return powerRequestError{action: p.Action, cause: err}
	}
	return nil
}

// powerObserved reconciles an interrupted power request from the current state alone.
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
	// An ACPI request may or may not have reached the guest; only the guest's state can tell.
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
		m, mediaPath, err := managedMedia(js, req.MediaID)
		if err != nil {
			return err
		}
		on := false
		for _, d := range vm.Disks {
			on = on || d.StorageID == m.StorageID
		}
		if !on {
			return errors.New("the install ISO must be on one of the VM's disk storage targets")
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
	if req.Action == "eject" {
		source, tray, err := cdromSource(ctx, req.VMID, running)
		if err != nil {
			return err
		}
		if tray == "" || source == "" {
			// Nothing to eject: do not attach a tray, or call virsh, just to remove from it.
			return e.recordVMMedia(req)
		}
		dev = tray
	} else if dev, err = ensureCDROM(ctx, e.stateDir, req.VMID, running); err != nil {
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
	if running {
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
	return strings.TrimSpace(string(state)) == "running", nil
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

// cdromSource reports the domain's CD drive target, empty when it has none, and the media the
// tray holds. It is a read-only check so an eject on a VM with no drive, or an already-empty
// one, is recognized as already satisfied instead of failing a virsh call with nothing to
// eject. A running domain is read live: its tray may have been changed outside Nex, and only
// the live definition proves what the guest currently sees. Media attached as a host device
// (<source dev>) counts as present, the same as a file.
func cdromSource(ctx context.Context, id string, running bool) (source, dev string, err error) {
	d, err := dumpManagedDomain(ctx, id, running)
	if err != nil {
		return "", "", err
	}
	for _, disk := range d.Devices.Disks {
		if disk.Device == "cdrom" {
			src := diskFile(disk)
			if src == "" {
				if disk.Source != nil {
					src = disk.Source.Dev
				}
			}
			return src, disk.Target.Dev, nil
		}
	}
	return "", "", nil
}

// ensureCDROM returns the SATA cdrom target, attaching an empty tray when missing.
// ponytail: SATA cannot hot-plug; a running domain without a tray must be stopped first.
func ensureCDROM(ctx context.Context, stateDir, id string, running bool) (string, error) {
	d, err := dumpManagedDomain(ctx, id, running)
	if err != nil {
		return "", err
	}
	used := map[string]bool{}
	for _, disk := range d.Devices.Disks {
		used[disk.Target.Dev] = true
		if disk.Device == "cdrom" {
			return disk.Target.Dev, nil
		}
	}
	if running {
		return "", errors.New("the VM has no CD drive while running; shut it down, then insert or eject media to add one")
	}
	dev := "sda"
	for i := 0; used[dev]; i++ {
		dev = "sd" + string(rune('a'+i+1))
		if i > 24 {
			return "", errors.New("no free SATA target for a CD drive")
		}
	}
	tray := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Target: targetXML{Dev: dev, Bus: "sata"}, Readonly: &struct{}{}}
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
		cur.Targets, cur.MediaTarget = managedTargets(*cur.Command.VM)
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
	v := protocol.ApplyVMMedia(*cur.Command.VM, req.Action, req.MediaID)
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
	got, _, err := cdromSource(ctx, req.VMID, running)
	if err != nil {
		return errors.New("VM definition cannot be established after an interrupted media change")
	}
	want := ""
	if req.Action == "insert" {
		want = path
	}
	if got == want {
		return nil
	}
	return errors.New("media change was interrupted; observe the CD drive and submit a new request")
}

// Default resources every host owns without operator action: its local libvirt image
// directory as storage named ISO, and one untagged network per host bridge that sits on a
// physical NIC or bond, named "<port> untagged" with the port recorded as the uplink.
const (
	DefaultStorageName = "ISO"
	DefaultStoragePath = "/var/lib/libvirt/images"
)

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
		commands = append(commands, protocol.Command{OperationID: hostID(), Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: hostID(), PoolID: pool, HostID: host, Name: DefaultStorageName, Path: DefaultStoragePath}})
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
	Disks, Targets []string
	MediaTarget    string
	New            []int
	Retained       []string
	Targets_       []managedTarget
}

// managedUpdateLayout keeps every existing disk where it is and places each new disk in the
// VM's directory on its storage target at the first unused disk-<n>.qcow2, so a retained or
// half-allocated file is never reused. Target names come from the journal; new disks take the
// next free letter on their bus.
func managedUpdateLayout(js []managedJournal, cur managedJournal, v protocol.VMCreateSpec) (managedLayout, error) {
	l := managedLayout{Disks: make([]string, len(v.Disks)), Targets: make([]string, len(v.Disks)), MediaTarget: cur.MediaTarget}
	known := map[string]int{}
	used := map[string]bool{cur.MediaTarget: true}
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
	mediaPath := ""
	if v.MediaID != "" {
		m, path, err := managedMedia(js, v.MediaID)
		if err != nil {
			return err
		}
		on := ""
		for _, d := range v.Disks {
			if d.StorageID == m.StorageID {
				if s, err := managedStorage(js, d.StorageID); err == nil {
					on = storageRegistrationPath(s)
				}
			}
		}
		if on == "" || path != filepath.Join(on, "nex-media", m.ID+".iso") {
			return errors.New("the install ISO must be published on one of the VM's storage targets")
		}
		if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
			return err
		}
		if err = verifyISO(path, m); err != nil {
			return err
		}
		mediaPath = path
	}
	if !running {
		if err = managedCapacity(ctx, v, js); err != nil {
			return err
		}
	}
	j.Disks, j.Targets, j.MediaTarget, j.MediaPath, j.Retained = layout.Disks, layout.Targets, layout.MediaTarget, mediaPath, layout.Retained
	if j.Domain, err = managedDomainXML(v, nets, j.Disks, j.Targets, j.MediaTarget, mediaPath); err != nil {
		return err
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
		if err = e.applyLive(ctx, v.ID, j.Domain, mediaPath); err != nil {
			return err
		}
	} else {
		if v.Name != cur.Command.VM.Name {
			if _, err = managedVirsh(ctx, "domrename", v.ID, "--", v.Name); err != nil {
				return fmt.Errorf("libvirt refused the rename (%v); inspect the VM on its host", err)
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
			return errors.New("VM redefinition outcome uncertain; observation required before recovery")
		}
	}
	if err = setAutostart(ctx, v); err != nil {
		return err
	}
	if err = e.observeDefinition(ctx, *j, observed); err != nil {
		return err
	}
	j.Stage = "defined"
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

// setAutostart makes libvirt's autostart flag match the definition; it never starts a guest.
func setAutostart(ctx context.Context, v protocol.VMCreateSpec) error {
	args := []string{"autostart", v.ID}
	if !v.Autostart {
		args = []string{"autostart", "--disable", v.ID}
	}
	if _, err := managedVirsh(ctx, args...); err != nil {
		return errors.New("VM autostart could not be set; inspect the VM on its host")
	}
	return nil
}

// applyLive reconciles a running guest's persistent definition towards the expected one with
// device operations only: attach the disks and interfaces it lacks, detach the ones it should
// not have, and change the CD drive's media. Comparing against what libvirt actually holds,
// rather than the journal, makes a re-submitted edit after an interruption converge.
func (e *ManagedEngine) applyLive(ctx context.Context, id string, expected []byte, mediaPath string) error {
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
	if diskFile(*cdrom) != mediaPath {
		args := []string{"change-media", id, cdrom.Target.Dev, "--live", "--config", "--force"}
		if mediaPath == "" {
			args = append(args, "--eject")
		} else {
			args = append(args, mediaPath, "--update")
		}
		if _, err = managedVirsh(ctx, args...); err != nil {
			return fmt.Errorf("libvirt refused the media change on the running VM (%v); inspect the VM on its host", err)
		}
	}
	return nil
}
