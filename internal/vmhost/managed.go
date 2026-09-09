// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
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
	"strconv"
	"strings"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
)

// ManagedEngine owns Controller-created guests separately from local appliance recovery.
// Its journal survives Controller disconnects; no command ever starts a guest.
type ManagedEngine struct{ stateDir, networkDir string }

func NewManaged(stateDir string) *ManagedEngine {
	return &ManagedEngine{stateDir: stateDir, networkDir: "/etc/network"}
}

type managedJournal struct {
	Command   protocol.Command `json:"command"`
	Result    protocol.Result  `json:"result"`
	Stage     string           `json:"stage"`
	Disk      string           `json:"disk,omitempty"`
	MediaPath string           `json:"media_path,omitempty"`
	Domain    []byte           `json:"domain,omitempty"`
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
		if j.Stage == "allocating" || j.Stage == "defining" {
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
	}
	return ""
}

var managedSource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`)

func validateManaged(c protocol.Command) error {
	if !identifier.MatchString(c.OperationID) {
		return errors.New("invalid operation ID")
	}
	count := 0
	for _, present := range []bool{c.Storage != nil, c.Network != nil, c.Media != nil, c.VM != nil, c.Power != nil} {
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
		if !identifier.MatchString(m.StorageID) || m.SizeBytes < 32774 || m.SizeBytes > protocol.MaxISOBytes || err != nil || len(h) != 32 || strings.ToLower(m.SHA256) != m.SHA256 {
			return errors.New("invalid ISO storage, size or SHA-256")
		}
	case "vm-create":
		if c.VM == nil {
			return errors.New("VM payload required")
		}
		v := c.VM
		id, pool, host = v.ID, v.PoolID, v.HostID
		if !uuidPattern.MatchString(id) || !hostname.MatchString(v.Name) || v.CPUs < 1 || v.CPUs > 64 || v.MemoryMiB < 512 || v.MemoryMiB > 262144 || v.DiskGiB < 1 || v.DiskGiB > 2048 || (v.Firmware != "bios" && v.Firmware != "uefi") || !identifier.MatchString(v.StorageID) || !identifier.MatchString(v.NetworkID) || !identifier.MatchString(v.MediaID) {
			return errors.New("invalid VM identity, resources, firmware or references")
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

// managedVMRefs resolves a VM's network and ISO from succeeded journal entries; the ISO must
// live on the VM's storage.
func managedVMRefs(js []managedJournal, v protocol.VMCreateSpec) (n protocol.NetworkRegistration, m protocol.Media, mediaPath string, err error) {
	for _, old := range js {
		if old.Result.Status != "succeeded" {
			continue
		}
		if c := old.Command.Network; c != nil && c.ID == v.NetworkID {
			n = *c
		}
		if c := old.Command.Media; c != nil && c.ID == v.MediaID && c.StorageID == v.StorageID {
			m = *c
			mediaPath = old.MediaPath
		}
	}
	if n.ID == "" || m.ID == "" {
		err = errors.New("network and ISO must be ready on the selected storage")
	}
	return n, m, mediaPath, err
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
	case "storage-register":
		if c.Storage.Path == e.stateDir || strings.HasPrefix(e.stateDir, c.Storage.Path+"/") || strings.HasPrefix(c.Storage.Path, e.stateDir+"/") {
			return errors.New("storage must be separate from agent state")
		}
		if err := localStorage(c.Storage.Path, 1<<30); err != nil {
			return err
		}
		for _, old := range js {
			if old.Command.Storage != nil && old.Result.Status == "succeeded" && old.Command.Storage.Path == c.Storage.Path {
				return errors.New("storage path is already registered")
			}
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
}
type managedDomain struct {
	XMLName  xml.Name    `xml:"domain"`
	Type     string      `xml:"type,attr"`
	Name     string      `xml:"name"`
	UUID     string      `xml:"uuid"`
	Memory   memoryXML   `xml:"memory"`
	VCPU     int         `xml:"vcpu"`
	OS       managedOS   `xml:"os"`
	Features featuresXML `xml:"features"`
	CPU      struct {
		Mode string `xml:"mode,attr"`
	} `xml:"cpu"`
	Devices struct {
		Disks      []diskXML     `xml:"disk"`
		Interfaces []managedNIC  `xml:"interface"`
		Channels   []channelXML  `xml:"channel"`
		Inputs     []inputXML    `xml:"input"`
		Graphics   []graphicsXML `xml:"graphics"`
		Video      []videoXML    `xml:"video"`
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

func managedDomainXML(v protocol.VMCreateSpec, n protocol.NetworkRegistration, disk, iso string) ([]byte, error) {
	d := managedDomain{Type: "kvm", Name: v.Name, UUID: v.ID, Memory: memoryXML{"MiB", v.MemoryMiB}, VCPU: v.CPUs, OS: managedOS{Type: osTypeXML{"x86_64", "q35", "hvm"}, Boot: []managedBoot{{"cdrom"}, {"hd"}}}}
	if v.Firmware == "uefi" {
		d.OS.Firmware = "efi"
	}
	d.CPU.Mode = "host-passthrough"
	d.Devices.Disks = []diskXML{{Type: "file", Device: "disk", Driver: driverXML{"qemu", "qcow2"}, Source: sourceXML{File: disk}, Target: targetXML{Dev: "vda", Bus: "virtio"}}, {Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Source: sourceXML{File: iso}, Target: targetXML{Dev: "sda", Bus: "sata"}, Readonly: &struct{}{}}}
	nic := managedNIC{Type: "bridge"}
	if n.Kind == "network" {
		nic.Type = "network"
		nic.Source.Network = n.Source
	} else {
		nic.Source.Bridge = n.Source
	}
	nic.MAC.Address = mac(v.ID)
	nic.Model.Type = "virtio"
	d.Devices.Interfaces = []managedNIC{nic}
	consoleDevices(&d)
	g := graphicsXML{Type: "vnc", Autoport: "no"}
	g.Listen.Type = "socket"
	d.Devices.Graphics = []graphicsXML{g}
	return xml.MarshalIndent(d, "", "  ")
}
func managedCapacity(ctx context.Context, v protocol.VMCreateSpec) error {
	if runtime.GOARCH != "amd64" || v.CPUs > runtime.NumCPU() {
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
	available := uint64(0)
	for _, line := range strings.Split(string(mem), "\n") {
		fields := strings.Fields(line)
		if len(fields) > 1 && fields[0] == "MemAvailable:" {
			available, _ = strconv.ParseUint(fields[1], 10, 64)
		}
	}
	if available < uint64(v.MemoryMiB+2048)*1024 {
		return errors.New("insufficient available memory including 2 GiB host reserve")
	}
	b, err := managedVirsh(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", "q35")
	if err != nil {
		return errors.New("cannot verify KVM q35 capabilities")
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
		return errors.New("unsupported KVM q35 capabilities")
	}
	if v.Firmware == "uefi" {
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
	s, err := managedStorage(js, v.StorageID)
	if err != nil {
		return err
	}
	if err = localStorage(s.Path, uint64(v.DiskGiB+4)<<30); err != nil {
		return err
	}
	n, m, mediaPath, err := managedVMRefs(js, v)
	if err != nil {
		return err
	}
	if mediaPath != filepath.Join(s.Path, "nex-media", m.ID+".iso") {
		return errors.New("persisted ISO path is invalid")
	}
	if err = verifyNetwork(ctx, n); err != nil {
		return err
	}
	if err = verifyISO(mediaPath, m); err != nil {
		return err
	}
	if err = managedCapacity(ctx, v); err != nil {
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
			return errors.New("VM name already exists")
		}
	}
	dir := filepath.Join(s.Path, "nex-vms")
	if err = managedSubdir(dir); err != nil {
		return err
	}
	dir = filepath.Join(dir, v.ID)
	j.Disk = filepath.Join(dir, "disk.qcow2")
	j.MediaPath = mediaPath
	j.Domain, err = managedDomainXML(v, n, j.Disk, mediaPath)
	if err != nil {
		return err
	}
	j.Stage = "allocating"
	if err = e.save(*j); err != nil {
		return err
	}
	if err = createDiskDir(dir); err != nil {
		return errors.New("VM directory exists or cannot be exclusively created; inspect before recovery")
	}
	if err = syncManagedDir(filepath.Dir(dir)); err != nil {
		return err
	}
	// The exclusive directory is the reservation; never overwrite or reuse a disk.
	if _, err = run(ctx, "qemu-img", "create", "-f", "qcow2", j.Disk, fmt.Sprintf("%dG", v.DiskGiB)); err != nil {
		return errors.New("disk allocation outcome uncertain; inspect retained VM directory")
	}
	if err = checkImage(ctx, j.Disk); err != nil {
		return err
	}
	if err = os.Chmod(j.Disk, 0600); err != nil {
		return err
	}
	f, err := os.Open(j.Disk)
	if err != nil {
		return err
	}
	err = f.Sync()
	f.Close()
	if err != nil {
		return err
	}
	if err = syncManagedDir(dir); err != nil {
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
		return errors.New("VM definition outcome uncertain; observation required before recovery")
	}
	if err = e.observeCreated(ctx, *j); err != nil {
		return err
	}
	j.Stage = "defined"
	return nil
}
func (e *ManagedEngine) observeCreated(ctx context.Context, j managedJournal) error {
	if j.Command.VM == nil || len(j.Domain) == 0 {
		return errors.New("VM creation evidence unavailable")
	}
	if err := localStorage(filepath.Dir(j.Disk), 0); err != nil {
		return err
	}
	real, err := filepath.EvalSymlinks(j.Disk)
	if err != nil || real != j.Disk {
		return errors.New("created disk missing or replaced by symlink")
	}
	info, err := os.Stat(j.Disk)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("created disk unavailable")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return errors.New("created disk has hardlink aliases")
	}
	if err = checkImage(ctx, j.Disk); err != nil {
		return err
	}
	image, err := run(ctx, "qemu-img", "info", "--force-share", "--output=json", j.Disk)
	if err != nil {
		return err
	}
	var imageInfo struct {
		Size int64 `json:"virtual-size"`
	}
	if json.Unmarshal(image, &imageInfo) != nil || imageInfo.Size != int64(j.Command.VM.DiskGiB)<<30 {
		return errors.New("VM virtual disk size changed")
	}
	if err = (&Engine{config: Config{VirshURI: "qemu:///system"}}).exclusiveDisk(ctx, journal{Operation: Operation{VMID: j.Command.VM.ID}, Disk: j.Disk}); err != nil {
		return err
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
func validateManagedDefinition(b, expected []byte) error {
	var got, want managedDomain
	if xml.Unmarshal(b, &got) != nil || xml.Unmarshal(expected, &want) != nil {
		return errors.New("invalid persistent VM definition")
	}
	memory := got.Memory.Value
	if got.Memory.Unit == "KiB" || got.Memory.Unit == "k" {
		memory /= 1024
	}
	if got.UUID != want.UUID || got.Name != want.Name || got.Type != "kvm" || got.VCPU != want.VCPU || memory != want.Memory.Value || len(got.Devices.Disks) != 2 || len(got.Devices.Interfaces) != 1 {
		return errors.New("VM definition differs from requested ownership or resources")
	}
	for i, disk := range got.Devices.Disks {
		expected := want.Devices.Disks[i]
		if disk.Type != expected.Type || disk.Device != expected.Device || disk.Source.File != expected.Source.File || disk.Driver.Type != expected.Driver.Type || (disk.Readonly != nil) != (expected.Readonly != nil) || disk.Shareable != nil || disk.Source.Dev != "" || disk.Source.Protocol != "" || disk.Target != expected.Target {
			return errors.New("VM disk assignment differs from requested ownership")
		}
	}
	if got.OS.Type.Arch != want.OS.Type.Arch || got.OS.Type.Value != want.OS.Type.Value || (got.OS.Type.Machine != "q35" && !strings.HasPrefix(got.OS.Type.Machine, "pc-q35-")) || got.CPU.Mode != want.CPU.Mode {
		return errors.New("VM CPU or machine changed")
	}
	if (want.OS.Firmware == "efi" && got.OS.Firmware != "efi") || (want.OS.Firmware == "" && got.OS.Firmware != "" && got.OS.Firmware != "bios") {
		return errors.New("VM firmware changed")
	}
	if len(got.OS.Boot) != len(want.OS.Boot) {
		return errors.New("VM boot order changed")
	}
	for i := range want.OS.Boot {
		if got.OS.Boot[i] != want.OS.Boot[i] {
			return errors.New("VM boot order changed")
		}
	}
	var devices domainXML
	if xml.Unmarshal(b, &devices) != nil || len(devices.Devices.Hostdev) != 0 || len(devices.Devices.Filesystems) != 0 || len(got.Devices.Graphics) != 1 || got.Devices.Graphics[0].Type != "vnc" || got.Devices.Graphics[0].Listen.Type != "socket" {
		return errors.New("unsupported VM devices or exposed graphics")
	}
	if got.Devices.Interfaces[0] != want.Devices.Interfaces[0] {
		return errors.New("VM network assignment changed")
	}
	return nil
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
