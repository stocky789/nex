// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

// ZFSProvisionDevice is the operator-selected, stable device identity.  The
// serial and size are rechecked immediately before any destructive command.
type zfsProvisionDevice struct {
	ByID           string `json:"by_id"`
	Serial         string `json:"serial"`
	SizeBytes      uint64 `json:"size_bytes"`
	EraseConfirmed bool   `json:"erase_confirmed,omitempty"`
	Fingerprint    string `json:"fingerprint"`
}

// ZFSProvisionRequest is deliberately host-local. Pool-wide dispatch and the
// master record belong to the pool owner; this worker never chooses a disk.
type zfsProvisionIntent struct {
	OperationID  string               `json:"operation_id"`
	RepositoryID string               `json:"repository_id"`
	Pool         string               `json:"pool"`
	Dataset      string               `json:"dataset"`
	MountPath    string               `json:"mount_path"`
	Layout       string               `json:"layout"`
	Devices      []zfsProvisionDevice `json:"devices"`
	Spares       []zfsProvisionDevice `json:"spares,omitempty"`
}

type zfsProvisionResult struct {
	OperationID string    `json:"operation_id"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	PoolGUID    string    `json:"pool_guid,omitempty"`
	DatasetGUID string    `json:"dataset_guid,omitempty"`
	ObservedAt  time.Time `json:"observed_at"`
}

type zfsProvisionJournal struct {
	Request zfsProvisionIntent `json:"request"`
	Result  zfsProvisionResult `json:"result"`
	Stage   string             `json:"stage"`
}

// ZFSProvisioner keeps the host's durable receipt separate from VM journals.
// A new Controller can replay the same operation ID without repeating a wipe.
type ZFSProvisioner struct {
	stateDir string
}

func NewZFSProvisioner(stateDir string) *ZFSProvisioner { return &ZFSProvisioner{stateDir: stateDir} }

// withExclusiveDiskLock serializes destructive disk work on one host. Callers pass their
// journal directory (zfs-provision or zfs-replace); the lock file sits
// beside those siblings on the host state directory so concurrent lanes cannot wipe the same
// disk. lockedErr is returned when the lock cannot be taken.
func withExclusiveDiskLock(stateDir string, lockedErr error) (unlock func(), err error) {
	hostDir := filepath.Dir(filepath.Clean(stateDir))
	if hostDir == "." || hostDir == string(filepath.Separator) {
		return nil, lockedErr
	}
	if err := os.MkdirAll(hostDir, 0700); err != nil {
		return nil, lockedErr
	}
	lock, err := os.OpenFile(filepath.Join(hostDir, ".disk-ops.lock"), os.O_CREATE|os.O_RDWR|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, lockedErr
	}
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		_ = lock.Close()
		return nil, lockedErr
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

// ZFSProvisionStorage returns the client-shaped registration for a dataset Core
// has just created. The ordinary managed storage path still performs the native
// identity and access observation before this becomes inventory.
func ZFSProvisionStorage(repositoryID, poolID, hostID, name string) protocol.StorageRegistration {
	pool := "nex-" + repositoryID
	mount := filepath.Join(storageMountRoot, repositoryID)
	return protocol.StorageRegistration{ID: repositoryID, PoolID: poolID, HostID: hostID, Name: name, Kind: protocol.StorageKindZFS, ZFSProvisioned: true, ZFSScope: "pool", Source: protocol.StorageSource{ZFS: &protocol.ZFSStorageSource{Pool: pool, Dataset: pool + "/vms", MountPath: mount}}}
}

// zfsProvisionFingerprint matches the device fingerprint the client-facing
// contract already requires; the selection is only as safe as this binding.
var zfsProvisionFingerprint = regexp.MustCompile(`^[0-9a-f]{64}$`)

// zfsProvisionRun executes in a transient system-manager service. The agent
// itself has ProtectSystem=strict, whose private mount namespace must never
// own a repository mount that libvirt cannot see.
var zfsProvisionRun = func(ctx context.Context, name string, args ...string) ([]byte, error) {
	command := []string{"--quiet", "--wait", "--collect", "--pipe", "--service-type=exec", "--", name}
	command = append(command, args...)
	return nativeRun(ctx, "systemd-run", command...)
}
var zfsProvisionSysBlockRoot = "/sys/class/block"
var zfsProvisionReadFile = os.ReadFile
var zfsProvisionEvalSymlinks = filepath.EvalSymlinks

// Provision applies one host's selected devices. The caller owns pool-wide
// dispatch; this method journals before every destructive boundary.
func (p *ZFSProvisioner) Provision(ctx context.Context, operationID, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) (protocol.ZFSProvisionHostResult, error) {
	intent := zfsProvisionIntentFor(operationID, repositoryID, host)
	result, err := p.provision(ctx, intent)
	return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Layout: intent.Layout, Devices: host.Devices, Status: result.Status, Error: result.Error, PoolGUID: result.PoolGUID, DatasetGUID: result.DatasetGUID}, err
}

// HasSucceededZFSProvision proves that this host's retained receipt and the
// currently imported tagged pool/dataset agree. It is deliberately stronger
// than a failed registration record: that record alone must never manufacture
// a repository after an unsuccessful create.
func (p *ZFSProvisioner) HasSucceededZFSProvision(ctx context.Context, operationID, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) bool {
	intent := zfsProvisionIntentFor(operationID, repositoryID, host)
	journal, err := readZFSProvision(filepath.Join(p.stateDir, operationID+".json"))
	if err != nil || !sameZFSProvision(journal.Request, intent) || journal.Result.Status != "succeeded" || (journal.Stage != "dataset" && journal.Stage != "adopted") {
		return false
	}
	adopted, _, err := zfsProvisionAdopt(ctx, intent)
	if !adopted || err != nil {
		return false
	}
	_, _, err = zfsProvisionIdentity(ctx, intent)
	return err == nil
}

// CanResumeZFSProvision permits only the non-destructive half-created-pool
// recovery path. A tagged pool with a durable creating/pool-created receipt is
// rechecked by Provision before it creates the missing dataset; pre-wipe,
// wipe-started and terminal failed/attention receipts are never resumed here.
func (p *ZFSProvisioner) CanResumeZFSProvision(ctx context.Context, operationID, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) bool {
	if p.HasSucceededZFSProvision(ctx, operationID, repositoryID, request, host) {
		return true
	}
	intent := zfsProvisionIntentFor(operationID, repositoryID, host)
	journal, err := readZFSProvision(filepath.Join(p.stateDir, operationID+".json"))
	if err != nil || !sameZFSProvision(journal.Request, intent) || (journal.Result.Status != "pending" && journal.Result.Status != "needs_attention") || (journal.Stage != "creating" && journal.Stage != "pool-created") {
		return false
	}
	_, owned, err := zfsProvisionAdopt(ctx, intent)
	return err == nil && owned
}

// zfsProvisionIntentFor is the one place a selected host becomes this worker's durable
// intent, so the journal comparison that guards a retry cannot drift between callers.
func zfsProvisionIntentFor(operationID, repositoryID string, host protocol.ZFSProvisionHost) zfsProvisionIntent {
	intent := zfsProvisionIntent{OperationID: operationID, RepositoryID: repositoryID, Pool: "nex-" + repositoryID, Dataset: "vms", MountPath: filepath.Join(storageMountRoot, repositoryID), Layout: protocol.ZFSLayout(host.Layout, len(host.Devices))}
	for _, d := range host.Devices {
		intent.Devices = append(intent.Devices, zfsProvisionDevice{ByID: d.ByID, Serial: d.Serial, SizeBytes: d.SizeBytes, EraseConfirmed: d.EraseConfirmation == d.ByID, Fingerprint: d.Fingerprint})
	}
	for _, d := range host.Spares {
		intent.Spares = append(intent.Spares, zfsProvisionDevice{ByID: d.ByID, Serial: d.Serial, SizeBytes: d.SizeBytes, EraseConfirmed: d.EraseConfirmation == d.ByID, Fingerprint: d.Fingerprint})
	}
	return intent
}

func (p *ZFSProvisioner) provision(ctx context.Context, request zfsProvisionIntent) (zfsProvisionResult, error) {
	if err := validateZFSProvision(request); err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	if !filepath.IsAbs(p.stateDir) || filepath.Clean(p.stateDir) != p.stateDir {
		return failedZFSProvision(request.OperationID, errors.New("unsafe ZFS provision journal directory")), errors.New("unsafe ZFS provision journal directory")
	}
	if err := os.MkdirAll(p.stateDir, 0700); err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	info, err := os.Lstat(p.stateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return failedZFSProvision(request.OperationID, errors.New("unsafe ZFS provision journal directory")), errors.New("unsafe ZFS provision journal directory")
	}
	unlock, err := withExclusiveDiskLock(p.stateDir, errors.New("ZFS provision journal is locked"))
	if err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	defer unlock()
	path := filepath.Join(p.stateDir, request.OperationID+".json")
	j, err := readZFSProvision(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return failedZFSProvision(request.OperationID, errors.New("ZFS provision journal is unreadable")), errors.New("ZFS provision journal is unreadable")
	}
	if errors.Is(err, os.ErrNotExist) {
		j = zfsProvisionJournal{Request: request, Result: zfsProvisionResult{OperationID: request.OperationID, Status: "pending", ObservedAt: time.Now().UTC()}, Stage: "inspecting"}
		if err := saveZFSProvision(path, j); err != nil {
			return failedZFSProvision(request.OperationID, err), err
		}
	} else if !sameZFSProvision(j.Request, request) {
		return failedZFSProvision(request.OperationID, errors.New("operation ID was already used with a different ZFS provision request")), errors.New("operation ID was already used with a different ZFS provision request")
	} else if j.Result.Status == "succeeded" {
		return j.Result, nil
	}

	if adopted, ownedPool, err := zfsProvisionAdopt(ctx, request); err != nil {
		return p.zfsProvisionAttention(path, j, err)
	} else if adopted {
		if err := verifyZFSProvisionVdevs(ctx, request); err != nil {
			return p.zfsProvisionAttention(path, j, err)
		}
		poolGUID, datasetGUID, identityErr := zfsProvisionIdentity(ctx, request)
		if identityErr != nil {
			return p.zfsProvisionAttention(path, j, identityErr)
		}
		j.Stage, j.Result = "adopted", succeededZFSProvision(request.OperationID)
		j.Result.PoolGUID, j.Result.DatasetGUID = poolGUID, datasetGUID
		return j.Result, saveZFSProvision(path, j)
	} else if ownedPool {
		if j.Stage != "creating" && j.Stage != "pool-created" && j.Stage != "wipe-started" {
			return p.zfsProvisionAttention(path, j, errors.New("owned ZFS pool is missing its dataset outside a recoverable create stage"))
		}
		if err := verifyZFSProvisionVdevs(ctx, request); err != nil {
			return p.zfsProvisionAttention(path, j, err)
		}
		if _, err := zfsProvisionRun(ctx, "zfs", "create", "-o", "compression=lz4", "-o", "atime=off", "-o", "xattr=sa", "-o", "mountpoint="+request.MountPath, request.Pool+"/"+request.Dataset); err != nil {
			return p.zfsProvisionAttention(path, j, fmt.Errorf("resume ZFS dataset create: %w", err))
		}
		poolGUID, datasetGUID, err := zfsProvisionIdentity(ctx, request)
		if err != nil {
			return p.zfsProvisionAttention(path, j, err)
		}
		j.Stage, j.Result = "dataset", succeededZFSProvision(request.OperationID)
		j.Result.PoolGUID, j.Result.DatasetGUID = poolGUID, datasetGUID
		return j.Result, saveZFSProvision(path, j)
	}
	if j.Result.Status == "needs_attention" || j.Result.Status == "failed" {
		return j.Result, nil
	}
	if j.Stage == "wipe-started" {
		return p.zfsProvisionAttention(path, j, errors.New("interrupted after selected devices were wiped; inspect the ZFS pool before retrying"))
	}
	if j.Stage == "pool-created" || j.Stage == "dataset" {
		return p.zfsProvisionAttention(path, j, errors.New("ZFS pool was changed after an interrupted provision"))
	}
	if err := checkZFSProvisionDevices(ctx, request.selected()); err != nil {
		return p.zfsProvisionAttention(path, j, err)
	}
	j.Stage = "pre-wipe"
	if err := saveZFSProvision(path, j); err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	for _, device := range request.selected() {
		// Same read-only inspection discovery uses: a column list is not portable
		// across the supported util-linux releases, and an empty signature list is
		// the authoritative blank-disk answer.
		signatures, err := zfsDeviceSignatures(ctx, device.ByID)
		if err != nil {
			return p.zfsProvisionAttention(path, j, errors.New("cannot inspect selected device signatures"))
		}
		if len(signatures) == 0 {
			continue
		}
		if !device.EraseConfirmed {
			return p.zfsProvisionAttention(path, j, errors.New("selected device contains old data without erase confirmation"))
		}
		// Durable before the first destructive command, and only when one is due:
		// a blank-disk run must never leave a receipt claiming a disk was wiped.
		if j.Stage != "wipe-started" {
			j.Stage = "wipe-started"
			if err := saveZFSProvision(path, j); err != nil {
				return failedZFSProvision(request.OperationID, err), err
			}
		}
		if _, err := zfsProvisionRun(ctx, "wipefs", "--all", device.ByID); err != nil {
			return p.zfsProvisionAttention(path, j, fmt.Errorf("wipe selected device: %w", err))
		}
	}
	args := []string{"create", "-o", "ashift=12", "-O", "nex:repository=" + request.RepositoryID, "-m", "none"}
	if len(request.Spares) > 0 {
		// Without autoreplace a hot spare sits idle until an operator runs zpool replace.
		args = append(args, "-o", "autoreplace=on")
	}
	args = append(args, request.Pool)
	args = append(args, zfsVdevArgs(request.Layout, request.Devices, request.Spares)...)
	j.Stage = "creating"
	if err := saveZFSProvision(path, j); err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	if _, err := zfsProvisionRun(ctx, "zpool", args...); err != nil {
		return p.zfsProvisionAttention(path, j, fmt.Errorf("create ZFS pool: %w", err))
	}
	j.Stage = "pool-created"
	if err := saveZFSProvision(path, j); err != nil {
		return failedZFSProvision(request.OperationID, err), err
	}
	if _, err := zfsProvisionRun(ctx, "zfs", "create", "-o", "compression=lz4", "-o", "atime=off", "-o", "xattr=sa", "-o", "mountpoint="+request.MountPath, request.Pool+"/"+request.Dataset); err != nil {
		return p.zfsProvisionAttention(path, j, fmt.Errorf("create ZFS dataset: %w", err))
	}
	poolGUID, datasetGUID, err := zfsProvisionIdentity(ctx, request)
	if err != nil {
		return p.zfsProvisionAttention(path, j, err)
	}
	j.Stage, j.Result = "dataset", succeededZFSProvision(request.OperationID)
	j.Result.PoolGUID, j.Result.DatasetGUID = poolGUID, datasetGUID
	return j.Result, saveZFSProvision(path, j)
}

func (p *ZFSProvisioner) zfsProvisionAttention(path string, j zfsProvisionJournal, cause error) (zfsProvisionResult, error) {
	j.Result = zfsProvisionResult{OperationID: j.Request.OperationID, Status: "needs_attention", Error: cause.Error(), ObservedAt: time.Now().UTC()}
	_ = saveZFSProvision(path, j)
	return j.Result, cause
}

func validateZFSProvision(v zfsProvisionIntent) error {
	if !identifier.MatchString(v.OperationID) || !identifier.MatchString(v.RepositoryID) || !nativeZFSName.MatchString(v.Pool) || !identifier.MatchString(v.Dataset) || !protocolStoragePath(v.MountPath) || len(v.Devices) == 0 {
		return errors.New("invalid ZFS provision request")
	}
	if !strings.HasPrefix(v.Pool, "nex-") || filepath.Dir(v.MountPath) != storageMountRoot || filepath.Base(v.MountPath) != v.RepositoryID {
		return errors.New("ZFS provision must use Core-owned pool and mount names")
	}
	if v.Dataset != "vms" {
		return errors.New("ZFS provision dataset must be vms")
	}
	// The worker never applies the Core default here: the caller resolved the
	// layout before journalling it, so an empty layout is a broken request.
	if !protocol.ValidZFSLayout(v.Layout, len(v.Devices)) {
		return errors.New("invalid ZFS provision layout")
	}
	seen := map[string]bool{}
	for _, d := range v.selected() {
		if !strings.HasPrefix(d.ByID, "/dev/disk/by-id/") || d.Serial == "" || d.SizeBytes == 0 || !zfsProvisionFingerprint.MatchString(d.Fingerprint) || seen[d.ByID] {
			return errors.New("invalid selected ZFS device")
		}
		seen[d.ByID] = true
	}
	return nil
}

// selected is every disk this request claims: the data disks and the hot spares. Both are
// erased and handed to zpool create; only the data disks satisfy the layout.
func (v zfsProvisionIntent) selected() []zfsProvisionDevice {
	return append(append([]zfsProvisionDevice{}, v.Devices...), v.Spares...)
}

func zfsVdevArgs(layout string, devices, spares []zfsProvisionDevice) []string {
	paths := make([]string, len(devices))
	for i := range devices {
		paths[i] = devices[i].ByID
	}
	var out []string
	switch layout {
	case "single":
		out = paths
	case "mirror":
		out = append([]string{"mirror"}, paths...)
	case "raidz1", "raidz2", "raidz3":
		out = append([]string{layout}, paths...)
	default:
		for i := 0; i < len(paths); i += 2 {
			out = append(out, "mirror", paths[i], paths[i+1])
		}
	}
	if len(spares) > 0 {
		out = append(out, "spare")
		for _, spare := range spares {
			out = append(out, spare.ByID)
		}
	}
	return out
}

var zfsProvisionDiscover = DiscoverZFSDevices

func checkZFSProvisionDevices(ctx context.Context, devices []zfsProvisionDevice) error {
	candidates, err := zfsProvisionDiscover(ctx)
	if err != nil {
		return errors.New("selected devices cannot be safely observed")
	}
	for _, d := range devices {
		candidate := ZFSDeviceCandidate{}
		for _, c := range candidates {
			if c.ByID == d.ByID {
				candidate = c
				break
			}
		}
		if !candidate.Eligible || candidate.Serial != d.Serial || candidate.SizeBytes != d.SizeBytes || candidate.Fingerprint != d.Fingerprint {
			return errors.New("selected device changed or is in use")
		}
		if candidate.RequiresErase && !d.EraseConfirmed {
			return errors.New("selected device contains old data without an exact erase confirmation")
		}
	}
	return nil
}

func zfsProvisionAdopt(ctx context.Context, request zfsProvisionIntent) (bool, bool, error) {
	pools, err := zfsProvisionRun(ctx, "zpool", "list", "-H", "-o", "name")
	if err != nil {
		return false, false, errors.New("cannot inspect existing ZFS pools")
	}
	found := false
	for _, pool := range strings.Fields(string(pools)) {
		if pool == request.Pool {
			found = true
			break
		}
	}
	if !found {
		return false, false, nil
	}
	owner, err := zfsProvisionRun(ctx, "zfs", "get", "-H", "-o", "value", "nex:repository", request.Pool)
	if err != nil || strings.TrimSpace(string(owner)) != request.RepositoryID {
		return false, false, errors.New("ZFS pool name is already in use")
	}
	dataset, err := zfsProvisionRun(ctx, "zfs", "get", "-H", "-o", "value", "mountpoint", request.Pool+"/"+request.Dataset)
	if err != nil || strings.TrimSpace(string(dataset)) != request.MountPath {
		if err != nil {
			return false, true, nil
		}
		return false, true, errors.New("existing ZFS repository dataset does not match provision request")
	}
	mounted, err := zfsProvisionRun(ctx, "zfs", "get", "-H", "-o", "value", "mounted", request.Pool+"/"+request.Dataset)
	if err != nil || strings.TrimSpace(string(mounted)) != "yes" {
		if _, mountErr := zfsProvisionRun(ctx, "zfs", "mount", request.Pool+"/"+request.Dataset); mountErr != nil {
			return false, true, errors.New("cannot mount existing ZFS repository dataset")
		}
		mounted, err = zfsProvisionRun(ctx, "zfs", "get", "-H", "-o", "value", "mounted", request.Pool+"/"+request.Dataset)
		if err != nil || strings.TrimSpace(string(mounted)) != "yes" {
			return false, true, errors.New("ZFS repository dataset is not mounted")
		}
	}
	return true, true, nil
}

func zfsProvisionIdentity(ctx context.Context, request zfsProvisionIntent) (string, string, error) {
	poolGUID, err := zfsProvisionRun(ctx, "zpool", "get", "-H", "-o", "value", "guid", request.Pool)
	if err != nil || strings.TrimSpace(string(poolGUID)) == "" {
		return "", "", errors.New("cannot verify ZFS pool identity")
	}
	datasetGUID, err := zfsProvisionRun(ctx, "zfs", "get", "-H", "-o", "value", "guid", request.Pool+"/"+request.Dataset)
	if err != nil || strings.TrimSpace(string(datasetGUID)) == "" {
		return "", "", errors.New("cannot verify ZFS dataset identity")
	}
	health, err := zfsProvisionRun(ctx, "zpool", "list", "-H", "-o", "health", request.Pool)
	if err != nil || strings.TrimSpace(string(health)) != "ONLINE" {
		return "", "", errors.New("ZFS pool is not ONLINE")
	}
	return strings.TrimSpace(string(poolGUID)), strings.TrimSpace(string(datasetGUID)), nil
}

// verifyZFSProvisionVdevs accepts only the exact selected physical disks and
// requested topology before a tagged, half-created pool may be completed.
func verifyZFSProvisionVdevs(ctx context.Context, request zfsProvisionIntent) error {
	out, err := zfsProvisionRun(ctx, "zpool", "status", "-P", request.Pool)
	if err != nil {
		return errors.New("cannot verify tagged ZFS pool vdevs")
	}
	actual, mirrors, raidz := []string{}, 0, ""
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		name := fields[0]
		if strings.HasPrefix(name, "/dev/") {
			actual = append(actual, resolvedDevicePath(name))
			continue
		}
		if strings.HasPrefix(name, "mirror") {
			mirrors++
		}
		if strings.HasPrefix(name, "raidz1") {
			raidz = "raidz1"
		}
		if strings.HasPrefix(name, "raidz2") {
			raidz = "raidz2"
		}
		if strings.HasPrefix(name, "raidz3") {
			raidz = "raidz3"
		}
	}
	selected := request.selected()
	expected := make([]string, len(selected))
	for i, device := range selected {
		expected[i] = resolvedDevicePath(device.ByID)
	}
	for i := range actual {
		actual[i] = zfsVdevWholeDisk(actual[i])
	}
	slices.Sort(actual)
	slices.Sort(expected)
	if !slices.Equal(actual, expected) {
		return errors.New("tagged ZFS pool vdevs do not match the selected devices")
	}
	switch request.Layout {
	case "single":
		if mirrors != 0 || raidz != "" {
			return errors.New("tagged ZFS pool layout does not match request")
		}
	case "mirror":
		if mirrors != 1 || raidz != "" {
			return errors.New("tagged ZFS pool layout does not match request")
		}
	case "striped_mirrors":
		if mirrors != len(request.Devices)/2 || raidz != "" {
			return errors.New("tagged ZFS pool layout does not match request")
		}
	case "raidz1", "raidz2", "raidz3":
		if raidz != request.Layout || mirrors != 0 {
			return errors.New("tagged ZFS pool layout does not match request")
		}
	}
	return nil
}

func zfsVdevWholeDisk(path string) string {
	base := filepath.Base(path)
	partition, err := zfsProvisionReadFile(filepath.Join(zfsProvisionSysBlockRoot, base, "partition"))
	if err != nil || strings.TrimSpace(string(partition)) == "" {
		return path
	}
	resolved, err := zfsProvisionEvalSymlinks(filepath.Join(zfsProvisionSysBlockRoot, base))
	if err != nil {
		return path
	}
	parent := filepath.Base(filepath.Dir(resolved))
	if parent == "block" || parent == base {
		return path
	}
	return filepath.Join("/dev", parent)
}

func sameZFSProvision(a, b zfsProvisionIntent) bool {
	aa, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return bytes.Equal(aa, bb)
}
func readZFSProvision(path string) (zfsProvisionJournal, error) {
	var j zfsProvisionJournal
	b, err := os.ReadFile(path)
	if err != nil {
		return j, err
	}
	if err = json.Unmarshal(b, &j); err != nil {
		return j, err
	}
	if validateZFSProvision(j.Request) != nil || j.Result.OperationID != j.Request.OperationID {
		return j, errors.New("invalid ZFS provision journal")
	}
	return j, nil
}

// saveZFSProvision is a var so a test can observe the exact order of durable
// stages: which stage is on disk before each destructive command matters more
// than the stage that survives the run.
var saveZFSProvision = func(path string, j zfsProvisionJournal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
}

func succeededZFSProvision(id string) zfsProvisionResult {
	return zfsProvisionResult{OperationID: id, Status: "succeeded", ObservedAt: time.Now().UTC()}
}
func failedZFSProvision(id string, err error) zfsProvisionResult {
	return zfsProvisionResult{OperationID: id, Status: "failed", Error: err.Error(), ObservedAt: time.Now().UTC()}
}
