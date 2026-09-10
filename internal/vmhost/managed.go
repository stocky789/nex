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
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

// ManagedEngine owns Controller-created guests separately from local appliance recovery.
// Its journal survives Controller disconnects. The only command that starts a guest is a
// vm-create asking for start_after_create, and it starts that guest once, after the definition
// is verified; every other command leaves power state to vm-power.
type ManagedEngine struct{ stateDir, networkDir string }

func NewManaged(stateDir string) *ManagedEngine {
	return &ManagedEngine{stateDir: stateDir, networkDir: "/etc/network"}
}

type managedJournal struct {
	Command   protocol.Command `json:"command"`
	Result    protocol.Result  `json:"result"`
	Stage     string           `json:"stage"`
	Disks     []string         `json:"disks,omitempty"`
	MediaPath string           `json:"media_path,omitempty"`
	Domain    []byte           `json:"domain,omitempty"`
	// Start records the outcome of start_after_create: "started", or why it did not start.
	// It is recorded in the same entry so a retried or reconciled creation never starts twice.
	Start string `json:"start,omitempty"`
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
		b, err := readSmall(filepath.Join(e.stateDir, entry.Name()), 1<<20)
		if err != nil {
			return nil, err
		}
		var j managedJournal
		if err = json.Unmarshal(b, &j); err != nil {
			return nil, err
		}
		if err = validateManaged(j.Command); err != nil || entry.Name() != j.Command.OperationID+".json" || j.Result.OperationID != j.Command.OperationID {
			return nil, errors.New("invalid managed journal identity or command")
		}
		switch j.Result.Status {
		case "pending", "succeeded", "failed", "needs_attention":
		default:
			return nil, errors.New("invalid managed journal result")
		}
		out = append(out, j)
	}
	return out, nil
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
	// Reserve every resource ID, even when its operation was interrupted or failed.
	for _, old := range js {
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
		if j.Stage == "allocating" || j.Stage == "defining" || j.Stage == "starting" {
			j.Result.Status = "needs_attention"
		}
		j.Result.Error = err.Error()
	}
	return j.Result, e.save(j)
}
func managedResourceID(c protocol.Command) string {
	switch c.Kind {
	case "storage-register":
		return "storage:" + c.Storage.ID
	case "network-register":
		return "network:" + c.Network.ID
	case "iso-import":
		return "media:" + c.Media.ID
	case "vm-create":
		return "vm:" + c.VM.ID
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
	for _, present := range []bool{c.Storage != nil, c.Network != nil, c.Media != nil, c.VM != nil, c.Power != nil, c.VMMedia != nil} {
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
		if !filepath.IsAbs(s.Path) || filepath.Clean(s.Path) != s.Path || s.Path == "/" {
			return errors.New("storage requires a canonical absolute non-root directory")
		}
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
		h, err := hex.DecodeString(m.SHA256)
		if !identifier.MatchString(m.StorageID) || m.SizeBytes < 32774 || err != nil || len(h) != 32 || strings.ToLower(m.SHA256) != m.SHA256 {
			return errors.New("invalid ISO storage, size or SHA-256")
		}
	case "vm-create":
		if c.VM == nil {
			return errors.New("VM payload required")
		}
		v := c.VM
		id, pool, host = v.ID, v.PoolID, v.HostID
		if !uuidPattern.MatchString(id) || !hostname.MatchString(v.Name) || v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
			return errors.New("invalid VM identity, vCPU count or memory")
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
	if !identifier.MatchString(id) || !identifier.MatchString(pool) || !identifier.MatchString(host) {
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
	for _, j := range js {
		if j.Result.Status == "succeeded" && j.Command.Storage != nil && j.Command.Storage.ID == id {
			return *j.Command.Storage, nil
		}
	}
	return protocol.StorageRegistration{}, errors.New("storage is not registered on this host")
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
			return *j.Command.Media, j.MediaPath, nil
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
			index = len(targets)
			at[d.StorageID] = index
			// Every target keeps the same headroom a single-disk VM required.
			targets = append(targets, managedTarget{ID: d.StorageID, Path: s.Path, Dir: filepath.Join(s.Path, "nex-vms", v.ID), Bytes: 4 << 30})
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

// Applied lists the pool-scoped configuration this host has durably applied; a replaced
// Controller mirrors it. Per-operation fields are cleared; journal errors yield an empty state.
func (e *ManagedEngine) Applied() *protocol.ManagedState {
	out := &protocol.ManagedState{}
	js, err := e.journals()
	if err != nil {
		return out
	}
	for _, j := range js {
		if j.Result.Status != "succeeded" {
			continue
		}
		switch c := j.Command; c.Kind {
		case "storage-register":
			s := *c.Storage
			s.OperationID, s.Status, s.Error = "", "", ""
			out.Storage = append(out.Storage, s)
		case "network-register":
			n := *c.Network
			n.OperationID, n.Status, n.Error = "", "", ""
			out.Networks = append(out.Networks, n)
		case "iso-import":
			m := *c.Media
			m.OperationID, m.Status, m.Error = "", "", ""
			out.Media = append(out.Media, m)
		case "vm-create":
			out.VMs = append(out.VMs, *c.VM)
		}
	}
	return out
}
func managedSubdir(path string) error {
	if err := trustedManagedPath(filepath.Dir(path)); err != nil {
		return err
	}
	err := os.Mkdir(path, 0711)
	if err == nil {
		return os.Chmod(path, 0711)
	}
	if !errors.Is(err, os.ErrExist) {
		return err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("managed directory uses a symlink")
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return errors.New("managed path is not a directory")
	}
	return trustedManagedPath(path)
}
func (e *ManagedEngine) apply(ctx context.Context, j *managedJournal, js []managedJournal, fetch func(context.Context, string) (io.ReadCloser, error)) error {
	c := j.Command
	switch c.Kind {
	case "vm-power":
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
	case "storage-register":
		if c.Storage.Path == e.stateDir || strings.HasPrefix(e.stateDir, c.Storage.Path+"/") || strings.HasPrefix(c.Storage.Path, e.stateDir+"/") {
			return errors.New("storage must be separate from agent state")
		}
		if err := localStorage(c.Storage.Path, 1<<30); err != nil {
			return err
		}
		if err := registrationConflict(js, c); err != nil {
			return err
		}
		f, err := os.CreateTemp(c.Storage.Path, ".nex-access-")
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
		return removeErr
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
		s, err := managedStorage(js, m.StorageID)
		if err != nil {
			return err
		}
		if err = localStorage(s.Path, uint64(m.SizeBytes)+(1<<30)); err != nil {
			return err
		}
		dir := filepath.Join(s.Path, "nex-media")
		if err = managedSubdir(dir); err != nil {
			return err
		}
		j.MediaPath = filepath.Join(dir, m.ID+".iso")
		j.Stage = "receiving"
		if err = e.save(*j); err != nil {
			return err
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
	}
	return errors.New("unsupported managed command")
}
func receiveISO(ctx context.Context, path string, m protocol.Media, body io.Reader) error {
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
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(&managedContextReader{ctx, body}, m.SizeBytes+1))
	if err != nil {
		return err
	}
	if n != m.SizeBytes || hex.EncodeToString(h.Sum(nil)) != m.SHA256 {
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
	Type      string            `xml:"type,attr"`
	Device    string            `xml:"device,attr"`
	Driver    driverXML         `xml:"driver"`
	Source    sourceXML         `xml:"source"`
	Target    targetXML         `xml:"target"`
	Readonly  *struct{}         `xml:"readonly,omitempty"`
	Shareable *struct{}         `xml:"shareable,omitempty"`
	Boot      *managedBootOrder `xml:"boot,omitempty"`
}
type managedController struct {
	Type  string `xml:"type,attr"`
	Index int    `xml:"index,attr"`
	Model string `xml:"model,attr,omitempty"`
}
type managedTopology struct {
	Sockets int `xml:"sockets,attr"`
	Cores   int `xml:"cores,attr"`
	Threads int `xml:"threads,attr"`
}
type managedCPU struct {
	Mode     string           `xml:"mode,attr"`
	Topology *managedTopology `xml:"topology,omitempty"`
}

// managedBalloon is what makes the memory mode real: "maximum" needs a virtio balloon reporting
// free guest pages back to the host, "dedicated" must not have one at all.
type managedBalloon struct {
	Model             string `xml:"model,attr"`
	FreePageReporting string `xml:"freePageReporting,attr,omitempty"`
}
type managedNIC struct {
	Type   string `xml:"type,attr"`
	Source struct {
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
	XMLName       xml.Name    `xml:"domain"`
	Type          string      `xml:"type,attr"`
	Name          string      `xml:"name"`
	UUID          string      `xml:"uuid"`
	Memory        memoryXML   `xml:"memory"`
	CurrentMemory *memoryXML  `xml:"currentMemory,omitempty"`
	VCPU          int         `xml:"vcpu"`
	OS            managedOS   `xml:"os"`
	Features      featuresXML `xml:"features"`
	CPU           managedCPU  `xml:"cpu"`
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

// managedDomainXML builds the persistent definition. nets and disks are positional: nets[i]
// carries NICs[i]'s registration and disks[i] the path allocated for Disks[i].
func managedDomainXML(v protocol.VMCreateSpec, nets []protocol.NetworkRegistration, disks []string, iso string) ([]byte, error) {
	if len(nets) != len(v.NICs) || len(disks) != len(v.Disks) {
		return nil, errors.New("domain devices do not match the requested VM")
	}
	d := managedDomain{Type: "kvm", Name: v.Name, UUID: v.ID, Memory: memoryXML{"MiB", v.Memory.SizeMiB}, VCPU: v.CPU.Count, OS: managedOS{Type: osTypeXML{"x86_64", managedMachine(v), "hvm"}}}
	if v.Boot.Firmware == "uefi" {
		d.OS.Firmware = "efi"
	}
	// dedicated holds the ceiling as the guest's allocation and removes the balloon entirely;
	// maximum only caps it and lets the guest hand free pages back.
	if v.Memory.Mode == "dedicated" {
		current := memoryXML{"MiB", v.Memory.SizeMiB}
		d.CurrentMemory = &current
		d.Devices.Memballoon = managedBalloon{Model: "none"}
	} else {
		d.Devices.Memballoon = managedBalloon{Model: "virtio", FreePageReporting: "on"}
	}
	sockets, cores, threads := v.CPU.Topology()
	d.CPU = managedCPU{Mode: v.CPU.Model, Topology: &managedTopology{sockets, cores, threads}}
	// virtio disks take vdX; SATA and SCSI disks share the sdX namespace with the install ISO.
	virtio, scsi, sd := 0, false, 0
	dev := func(bus string) string {
		if bus == "virtio" {
			virtio++
			return "vd" + string(rune('a'+virtio-1))
		}
		sd++
		return "sd" + string(rune('a'+sd-1))
	}
	for i, disk := range v.Disks {
		x := managedDisk{Type: "file", Device: "disk", Driver: driverXML{"qemu", "qcow2"}, Source: sourceXML{File: disks[i]}, Target: targetXML{Dev: dev(disk.Bus), Bus: disk.Bus}}
		if disk.BootOrder > 0 {
			x.Boot = &managedBootOrder{disk.BootOrder}
		}
		scsi = scsi || disk.Bus == "scsi"
		d.Devices.Disks = append(d.Devices.Disks, x)
	}
	if scsi {
		d.Devices.Controllers = []managedController{{Type: "scsi", Model: "virtio-scsi"}}
	}
	if iso != "" {
		cdrom := managedDisk{Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Source: sourceXML{File: iso}, Target: targetXML{Dev: dev("sata"), Bus: "sata"}, Readonly: &struct{}{}}
		if v.Boot.MediaBootOrder > 0 {
			cdrom.Boot = &managedBootOrder{v.Boot.MediaBootOrder}
		}
		d.Devices.Disks = append(d.Devices.Disks, cdrom)
	}
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
	for _, j := range js {
		if j.Command.Kind == "vm-create" && j.Command.VM != nil && j.Command.VM.ID != except && j.Result.Status != "failed" && j.Command.VM.Memory.Mode == "dedicated" {
			total += j.Command.VM.Memory.SizeMiB
		}
	}
	return total
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
func (e *ManagedEngine) create(ctx context.Context, j *managedJournal, js []managedJournal) error {
	v := *j.Command.VM
	targets, disks, err := managedDiskPlan(js, v)
	if err != nil {
		return err
	}
	for _, t := range targets {
		if err = localStorage(t.Path, t.Bytes); err != nil {
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
	for _, name := range strings.Split(strings.TrimSpace(string(names)), "\n") {
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
		if err = managedSubdir(filepath.Join(t.Path, "nex-vms")); err != nil {
			return err
		}
	}
	j.Disks = disks
	j.MediaPath = mediaPath
	j.Domain, err = managedDomainXML(v, nets, disks, mediaPath)
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
// start failure leaves the creation intact and reports needs_attention with the reason.
func (e *ManagedEngine) startCreated(ctx context.Context, j *managedJournal) error {
	if !j.Command.VM.StartAfterCreate || j.Start != "" {
		return nil
	}
	j.Stage = "starting"
	if err := e.save(*j); err != nil {
		return err
	}
	if err := powerVM(ctx, protocol.VMPowerRequest{VMID: j.Command.VM.ID, Action: "start"}); err != nil {
		j.Start = err.Error()
		return errors.New("VM created but not started: " + err.Error())
	}
	j.Start, j.Stage = "started", "started"
	return nil
}

// registrationConflict refuses a storage path, or a network source in its namespace, that
// another registration on this host still holds: pending, succeeded or uncertain. A failed
// registration releases its resource. VLAN and plain bridge registrations share the bridge
// namespace; libvirt networks have their own.
func registrationConflict(js []managedJournal, c protocol.Command) error {
	for _, old := range js {
		if old.Command.OperationID == c.OperationID || old.Result.Status == "failed" {
			continue
		}
		if c.Storage != nil && old.Command.Storage != nil && old.Command.Storage.Path == c.Storage.Path {
			return errors.New("storage path " + c.Storage.Path + " is already registered on this host as " + old.Command.Storage.Name)
		}
		if c.Network != nil && old.Command.Network != nil && old.Command.Network.Source == c.Network.Source && (old.Command.Network.Kind == "network") == (c.Network.Kind == "network") {
			return errors.New("network " + c.Network.Source + " is already registered on this host as " + old.Command.Network.Name)
		}
	}
	return nil
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
	for _, old := range js {
		if old.Command.Kind != "vm-create" || old.Command.VM == nil || old.Command.VM.ID == v.ID || old.Result.Status == "failed" {
			continue
		}
		if old.Command.VM.Name == v.Name {
			return errors.New("VM name " + v.Name + " is already used on this host")
		}
		for _, mac := range vmMACs(*old.Command.VM) {
			if slices.Contains(macs, mac) {
				return errors.New("MAC address " + mac + " is already used by VM " + old.Command.VM.Name + " on this host")
			}
		}
	}
	return nil
}
func (e *ManagedEngine) observeCreated(ctx context.Context, j managedJournal) error {
	if j.Command.VM == nil || len(j.Domain) == 0 || len(j.Disks) != len(j.Command.VM.Disks) {
		return errors.New("VM creation evidence unavailable")
	}
	// Ownership is observed once per disk: exclusiveDisk rescans every domain each call, so this
	// costs disks × domains virsh calls. Batch it there if hosts ever carry many domains.
	for i, disk := range j.Disks {
		if err := localStorage(filepath.Dir(disk), 0); err != nil {
			return err
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
	if j.Command.VM.Autostart {
		info, err := managedVirsh(ctx, "dominfo", j.Command.VM.ID)
		if err != nil || !autostartEnabled(info) {
			return errors.New("VM autostart is not confirmed enabled")
		}
	}
	b, err := managedVirsh(ctx, "dumpxml", j.Command.VM.ID, "--inactive")
	if err != nil {
		return errors.New("cannot observe persistent VM definition")
	}
	if err = validateManagedDefinition(b, j.Domain); err != nil {
		return err
	}
	state, err := managedVirsh(ctx, "domstate", j.Command.VM.ID)
	if err != nil || strings.TrimSpace(string(state)) != "shut off" {
		return errors.New("VM state is not confirmed stopped; no effects replayed")
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
	for i, disk := range got.Devices.Disks {
		expected := want.Devices.Disks[i]
		if disk.Type != expected.Type || disk.Device != expected.Device || disk.Source.File != expected.Source.File || disk.Driver.Type != expected.Driver.Type || (disk.Readonly != nil) != (expected.Readonly != nil) || disk.Shareable != nil || disk.Source.Dev != "" || disk.Source.Protocol != "" || disk.Target != expected.Target || bootOrder(disk.Boot) != bootOrder(expected.Boot) {
			return errors.New("VM disk assignment or boot order differs from requested ownership")
		}
	}
	// libvirt expands a machine alias such as q35 to its versioned form.
	if got.OS.Type.Arch != want.OS.Type.Arch || got.OS.Type.Value != want.OS.Type.Value || (got.OS.Type.Machine != want.OS.Type.Machine && !strings.HasPrefix(got.OS.Type.Machine, "pc-"+want.OS.Type.Machine+"-")) || got.CPU.Mode != want.CPU.Mode {
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
	// The balloon carries the memory mode: no device for dedicated, virtio free page reporting
	// for maximum. A libvirt that does not echo the attribute must not fail every such VM.
	if got.Devices.Memballoon.Model != want.Devices.Memballoon.Model || (got.Devices.Memballoon.FreePageReporting != "" && got.Devices.Memballoon.FreePageReporting != want.Devices.Memballoon.FreePageReporting) {
		return errors.New("VM memory mode changed")
	}
	var devices domainXML
	if xml.Unmarshal(b, &devices) != nil || len(devices.Devices.Hostdev) != 0 || len(devices.Devices.Filesystems) != 0 || len(got.Devices.Graphics) != 1 || got.Devices.Graphics[0].Type != "vnc" || got.Devices.Graphics[0].Listen.Type != "socket" {
		return errors.New("unsupported VM devices or exposed graphics")
	}
	for i, nic := range got.Devices.Interfaces {
		// Compared field by field rather than as whole structs: MTU is a pointer, so struct
		// equality would compare addresses and reject every adapter that carries one.
		w := want.Devices.Interfaces[i]
		if nic.Type != w.Type || nic.Source != w.Source || nic.MAC != w.MAC || nic.Model != w.Model || !sameMTU(nic.MTU, w.MTU) {
			return errors.New("VM network assignment changed")
		}
	}
	return nil
}

// sameMTU enforces a requested size and tolerates one we did not request, matching how the
// balloon attribute is treated: a libvirt that adds its own default must not fail every VM.
func sameMTU(got, want *mtuXML) bool {
	return want == nil || (got != nil && *got == *want)
}
func (e *ManagedEngine) reconcile(ctx context.Context, j managedJournal) (protocol.Result, error) {
	j.Result.Status = "needs_attention"
	j.Result.Error = "interrupted operation retained; inspect host resources before creating a new request; no effects replayed"
	if j.Command.Kind == "storage-register" || j.Command.Kind == "network-register" {
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
	}
	if j.Command.Kind == "vm-create" && (j.Stage == "defining" || j.Stage == "defined") {
		if err := e.observeCreated(ctx, j); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
			j.Stage = "defined"
			if err = e.startCreated(ctx, &j); err != nil {
				j.Result.Status = "needs_attention"
				j.Result.Error = err.Error()
			}
		}
	}
	if j.Command.Kind == "vm-create" && j.Stage == "starting" {
		// The start request was issued or interrupted; only observation says which.
		if err := powerObserved(ctx, protocol.VMPowerRequest{VMID: j.Command.VM.ID, Action: "start"}); err == nil {
			j.Result.Status = "succeeded"
			j.Result.Error = ""
			j.Start, j.Stage = "started", "started"
		} else if j.Start == "" {
			j.Result.Error = "VM created but not started: " + err.Error()
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
				j.Result.Status = "failed"
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
		if err := verifyISO(j.MediaPath, *j.Command.Media); err == nil {
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
		return errors.New("libvirt refused the " + p.Action + " request; inspect the VM on its host")
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
			src := disk.Source.File
			if src == "" {
				src = disk.Source.Dev
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
	for _, j := range js {
		if j.Result.Status == "succeeded" && j.Command.Kind == "vm-create" && j.Command.VM != nil && j.Command.VM.ID == id {
			return *j.Command.VM, nil
		}
	}
	return protocol.VMCreateSpec{}, errors.New("VM is not defined on this host")
}

// recordVMMedia updates the durable vm-create journal so Applied reports the current media.
func (e *ManagedEngine) recordVMMedia(req protocol.VMMediaRequest) error {
	js, err := e.journals()
	if err != nil {
		return err
	}
	for _, j := range js {
		if j.Result.Status != "succeeded" || j.Command.Kind != "vm-create" || j.Command.VM == nil || j.Command.VM.ID != req.VMID {
			continue
		}
		v := protocol.ApplyVMMedia(*j.Command.VM, req.Action, req.MediaID)
		j.Command.VM = &v
		return e.save(j)
	}
	return errors.New("VM is not defined on this host")
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
// state and whoever issued it, means nothing is added; the pool is assigned on adoption, so
// the host's own identity stands in for it. It is safe to call on every uplink change.
func (e *ManagedEngine) RegisterDefaults(ctx context.Context, host string, nics []protocol.NIC) error {
	if !identifier.MatchString(host) {
		return errors.New("defaults need the enrolled host identity")
	}
	js, err := e.journals()
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var commands []protocol.Command
	if !slices.ContainsFunc(js, func(j managedJournal) bool {
		return j.Command.Storage != nil && j.Command.Storage.Path == DefaultStoragePath
	}) {
		commands = append(commands, protocol.Command{OperationID: hostID(), Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: hostID(), PoolID: host, HostID: host, Name: DefaultStorageName, Path: DefaultStoragePath}})
	}
	for _, n := range DefaultNetworks(nics) {
		if slices.ContainsFunc(js, func(j managedJournal) bool {
			return j.Command.Network != nil && j.Command.Network.Kind != "network" && j.Command.Network.Source == n.Source
		}) {
			continue
		}
		n.ID, n.PoolID, n.HostID = hostID(), host, host
		commands = append(commands, protocol.Command{OperationID: hostID(), Kind: "network-register", Network: &n})
	}
	for _, c := range commands {
		if _, err := e.Execute(ctx, c, nil); err != nil {
			return err
		}
	}
	return nil
}
