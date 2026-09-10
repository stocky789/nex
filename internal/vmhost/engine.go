// SPDX-License-Identifier: AGPL-3.0-only
// Package vmhost owns the narrowly scoped, local-disk Controller appliance lifecycle.
package vmhost

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/websecurity"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Config struct{ StateDir, StorageDir, Bridge, ImagePath, ManifestPath, SignaturePath, PublicKeyPath, VirshURI string }
type Engine struct{ config Config }
type DeploySpec struct {
	RequestID      string   `json:"request_id"`
	Name           string   `json:"name"`
	Hostname       string   `json:"hostname"`
	CPUs           int      `json:"cpus"`
	MemoryMiB      int      `json:"memory_mib"`
	DiskGiB        int      `json:"disk_gib"`
	NetworkMode    string   `json:"network_mode,omitempty"`
	AddressCIDR    string   `json:"address_cidr"`
	Gateway        string   `json:"gateway"`
	DNS            []string `json:"dns"`
	AdminPassword  string   `json:"admin_password"`
	TLSCertificate string   `json:"tls_certificate"`
	TLSPrivateKey  string   `json:"tls_private_key"`
}
type Operation struct {
	ID                   string `json:"id"`
	VMID                 string `json:"vm_id"`
	Kind                 string `json:"kind"`
	Status               string `json:"status"`
	Stage                string `json:"stage"`
	Progress             int    `json:"progress"`
	Error                string `json:"error,omitempty"`
	ControllerURL        string `json:"controller_url,omitempty"`
	ControllerCertSHA256 string `json:"controller_cert_sha256,omitempty"`
}
type VM struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	State         string `json:"state"`
	Managed       bool   `json:"managed"`
	PowerAllowed  bool   `json:"power_allowed"`
	BlockedReason string `json:"blocked_reason,omitempty"`
}
type Preflight struct {
	ImageVerification string   `json:"image_verification"`
	Ready             bool     `json:"ready"`
	Blockers          []string `json:"blockers"`
	CPUs              int      `json:"cpus"`
	MemoryMiB         int      `json:"memory_mib"`
	DiskGiB           int      `json:"disk_gib"`
	Bridge            string   `json:"bridge"`
	StorageDir        string   `json:"storage_dir"`
}
type manifest struct {
	FormatVersion    int    `json:"format_version"`
	Version          string `json:"version"`
	Architecture     string `json:"architecture"`
	ProtocolVersion  int    `json:"protocol_version"`
	Format           string `json:"format"`
	ImageSHA256      string `json:"image_sha256"`
	MinimumVCPUs     int    `json:"minimum_vcpus"`
	MinimumMemoryMiB int    `json:"minimum_memory_mib"`
	MinimumDiskGiB   int    `json:"minimum_disk_gib"`
}
type journal struct {
	Operation
	Digest string `json:"digest"`
	Name   string `json:"name"`
	Disk   string `json:"disk"`
	Bridge string `json:"bridge"`
}

var identifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
var hostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func New(c Config) (*Engine, error) {
	for _, p := range []string{c.StateDir, c.StorageDir} {
		if !filepath.IsAbs(p) || filepath.Clean(p) == "/" {
			return nil, errors.New("state and local storage require absolute non-root paths")
		}
		if err := os.MkdirAll(p, 0700); err != nil {
			return nil, err
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil || real != filepath.Clean(p) {
			return nil, errors.New("state and storage paths must not use symlinks")
		}
	}
	if c.StateDir == c.StorageDir {
		return nil, errors.New("state and VM storage must be separate directories")
	}
	if err := os.Chmod(c.StateDir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(c.StorageDir, 0711); err != nil {
		return nil, err
	}
	if c.VirshURI == "" {
		c.VirshURI = "qemu:///system"
	}
	if c.VirshURI != "qemu:///system" && c.VirshURI != "qemu:///session" {
		return nil, errors.New("only local libvirt connections supported")
	}
	return &Engine{config: c}, nil
}
func defaults(s DeploySpec) DeploySpec {
	if s.CPUs == 0 {
		s.CPUs = 2
	}
	if s.MemoryMiB == 0 {
		s.MemoryMiB = 4096
	}
	if s.DiskGiB == 0 {
		s.DiskGiB = 32
	}
	if s.Name == "" {
		s.Name = "nex-controller"
	}
	if s.Hostname == "" {
		s.Hostname = s.Name
	}
	return s
}
func Validate(s DeploySpec) error { return validate(defaults(s)) }
func validate(s DeploySpec) error {
	if !identifier.MatchString(s.RequestID) || !hostname.MatchString(s.Name) || !hostname.MatchString(s.Hostname) {
		return errors.New("invalid request ID, VM name or hostname")
	}
	if s.CPUs < 2 || s.CPUs > 64 || s.MemoryMiB < 4096 || s.MemoryMiB > 262144 || s.DiskGiB < 32 || s.DiskGiB > 2048 {
		return errors.New("resources outside supported range: 2–64 CPUs, 4096–262144 MiB RAM, 32–2048 GiB disk")
	}
	if s.NetworkMode != "" && s.NetworkMode != "static" && s.NetworkMode != "dhcp" {
		return errors.New("network_mode must be static or dhcp")
	}
	if s.NetworkMode == "dhcp" {
		if s.AddressCIDR != "" || s.Gateway != "" || len(s.DNS) != 0 {
			return errors.New("DHCP network settings must omit static address, gateway and DNS")
		}
	} else {
		prefix, err := netip.ParsePrefix(s.AddressCIDR)
		if err != nil || !prefix.Addr().Is4() || !prefix.Addr().IsGlobalUnicast() || prefix.Bits() > 30 || prefix.Bits() < 8 || prefix.Addr() == prefix.Masked().Addr() {
			return errors.New("require usable static IPv4 address/CIDR (/8 through /30)")
		}
		gateway, err := netip.ParseAddr(s.Gateway)
		if err != nil || !gateway.Is4() || !prefix.Contains(gateway) || gateway == prefix.Addr() || gateway == prefix.Masked().Addr() {
			return errors.New("gateway must be another usable address in the subnet")
		}
		last := prefix.Masked().Addr().As4()
		bits := uint32(last[0])<<24 | uint32(last[1])<<16 | uint32(last[2])<<8 | uint32(last[3])
		bits |= uint32(1)<<(32-prefix.Bits()) - 1
		broadcast := netip.AddrFrom4([4]byte{byte(bits >> 24), byte(bits >> 16), byte(bits >> 8), byte(bits)})
		if gateway == broadcast || prefix.Addr() == broadcast {
			return errors.New("broadcast address cannot be used")
		}
		if len(s.DNS) == 0 || len(s.DNS) > 3 {
			return errors.New("supply one to three DNS addresses")
		}
		for _, d := range s.DNS {
			a, e := netip.ParseAddr(d)
			if e != nil || !a.Is4() || !a.IsGlobalUnicast() {
				return errors.New("invalid IPv4 DNS address")
			}
		}
	}
	if !websecurity.ValidNewPassword(s.AdminPassword) || strings.ContainsAny(s.AdminPassword, "\x00\r\n") {
		return errors.New("administrator password is required (maximum 1024 UTF-8 bytes, without NUL or line breaks)")
	}
	if _, err := tls.X509KeyPair([]byte(s.TLSCertificate), []byte(s.TLSPrivateKey)); err != nil {
		return errors.New("invalid Controller TLS certificate/key pair")
	}
	return nil
}
func (e *Engine) Preflight(ctx context.Context, s DeploySpec) (Preflight, error) {
	s = defaults(s)
	p := Preflight{ImageVerification: "Signed manifest checked; full image integrity and format verification runs during deployment", CPUs: s.CPUs, MemoryMiB: s.MemoryMiB, DiskGiB: s.DiskGiB, Bridge: e.config.Bridge, StorageDir: e.config.StorageDir, Blockers: []string{}}
	if s.RequestID != "" {
		if err := validate(s); err != nil {
			p.Blockers = append(p.Blockers, err.Error())
		}
	}
	if !identifier.MatchString(e.config.Bridge) {
		p.Blockers = append(p.Blockers, "configure an existing management bridge")
	} else if _, err := os.Stat("/sys/class/net/" + e.config.Bridge + "/bridge"); err != nil {
		p.Blockers = append(p.Blockers, "configured management bridge does not exist; do not alter live networking during deployment")
	}
	if f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0); err != nil {
		p.Blockers = append(p.Blockers, "KVM is unavailable")
	} else {
		f.Close()
	}
	if runtime.GOARCH != "amd64" {
		p.Blockers = append(p.Blockers, "appliance requires amd64")
	}
	if s.CPUs > runtime.NumCPU() {
		p.Blockers = append(p.Blockers, "insufficient host CPUs")
	}
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return p, err
	}
	available := uint64(0)
	for _, line := range strings.Split(string(mem), "\n") {
		f := strings.Fields(line)
		if len(f) > 1 && f[0] == "MemAvailable:" {
			available, _ = strconv.ParseUint(f[1], 10, 64)
		}
	}
	if available < uint64(s.MemoryMiB+2048)*1024 {
		p.Blockers = append(p.Blockers, "insufficient available RAM including 2 GiB host reserve")
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(e.config.StorageDir, &stat); err != nil {
		return p, err
	}
	// Only known local filesystems: pool registration never establishes shared-disk ownership.
	switch uint64(stat.Type) {
	case 0xef53, 0x58465342, 0x9123683e:
	default:
		p.Blockers = append(p.Blockers, "appliance storage must be local ext4, XFS or Btrfs")
	}
	if stat.Bavail*uint64(stat.Bsize) < uint64(s.DiskGiB+4)<<30 {
		p.Blockers = append(p.Blockers, "insufficient storage including full virtual disk and 4 GiB reserve")
	}
	for _, bin := range []string{"virsh", "qemu-img", "genisoimage"} {
		if _, err = exec.LookPath(bin); err != nil {
			p.Blockers = append(p.Blockers, bin+" is not installed")
		}
	}
	if _, err = e.virsh(ctx, "list", "--all", "--uuid"); err != nil {
		p.Blockers = append(p.Blockers, "local libvirt unavailable")
	}
	if _, err = e.verifyManifest(); err != nil {
		p.Blockers = append(p.Blockers, err.Error())
	}
	p.Ready = len(p.Blockers) == 0
	return p, nil
}

type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("command output too large")
	}
	return b.Buffer.Write(p)
}
func run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var out, stderr cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// The last stderr line is the reason (libvirt prints "error: ..."); it carries no secrets.
		lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
		if reason := strings.TrimSpace(lines[len(lines)-1]); reason != "" {
			return nil, fmt.Errorf("%s failed: %w: %s", name, err, reason)
		}
		return nil, fmt.Errorf("%s failed: %w", name, err)
	}
	return out.Bytes(), nil
}
func (e *Engine) virsh(ctx context.Context, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return run(ctx, "virsh", append([]string{"--connect", e.config.VirshURI}, args...)...)
}
func readSmall(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if len(b) > int(limit) {
		return nil, errors.New("file too large")
	}
	return b, err
}
func (e *Engine) verifyManifest() (manifest, error) {
	var m manifest
	b, err := readSmall(e.config.ManifestPath, 65536)
	if err != nil {
		return m, errors.New("appliance manifest unavailable")
	}
	sig, err := readSmall(e.config.SignaturePath, 64)
	if err != nil {
		return m, errors.New("appliance signature unavailable")
	}
	key, err := readSmall(e.config.PublicKeyPath, 32)
	if err != nil || len(key) != 32 || !ed25519.Verify(key, b, sig) {
		return m, errors.New("appliance signature verification failed")
	}
	if json.Unmarshal(b, &m) != nil || m.FormatVersion != 1 || m.ProtocolVersion != 1 || m.Architecture != "amd64" || m.Format != "qcow2" || len(m.ImageSHA256) != 64 {
		return m, errors.New("unsupported appliance manifest")
	}
	st, err := os.Stat(e.config.ImagePath)
	if err != nil || !st.Mode().IsRegular() {
		return m, errors.New("appliance image unavailable or not a regular file")
	}
	return m, nil
}

// Deployment always hashes image bytes; polling preflight checks only signed metadata.
func (e *Engine) verifyImage(ctx context.Context) (manifest, error) {
	m, err := e.verifyManifest()
	if err != nil {
		return m, err
	}
	f, err := os.Open(e.config.ImagePath)
	if err != nil {
		return m, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return m, errors.New("appliance must be a regular file")
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			return m, ctx.Err()
		}
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return m, rerr
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != m.ImageSHA256 {
		return m, errors.New("appliance image digest mismatch")
	}
	if err = checkImage(ctx, e.config.ImagePath); err != nil {
		return m, err
	}
	return m, nil
}
func checkImage(ctx context.Context, path string) error {
	b, err := run(ctx, "qemu-img", "info", "--force-share", "--output=json", path)
	if err != nil {
		return err
	}
	var v map[string]any
	if json.Unmarshal(b, &v) != nil || v["format"] != "qcow2" {
		return errors.New("image must be qcow2")
	}
	var unsafe func(any) bool
	unsafe = func(x any) bool {
		switch v := x.(type) {
		case map[string]any:
			for k, x := range v {
				if (strings.Contains(k, "backing") || k == "data-file" || k == "encrypted") && x != nil && x != "" && x != false {
					return true
				}
				if unsafe(x) {
					return true
				}
			}
		case []any:
			for _, x := range v {
				if unsafe(x) {
					return true
				}
			}
		}
		return false
	}
	if unsafe(v) {
		return errors.New("backing chains, external data files and encrypted images are unsupported")
	}
	return nil
}

func (e *Engine) lock(ctx context.Context) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(e.config.StateDir, "operations.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
func unlock(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".write-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if ce := f.Close(); err == nil {
		err = ce
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func (e *Engine) save(j journal) error {
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(e.config.StateDir, j.ID+".json"), b)
}
func (e *Engine) load(id string) (journal, error) {
	var j journal
	if !identifier.MatchString(id) {
		return j, errors.New("invalid operation ID")
	}
	b, err := readSmall(filepath.Join(e.config.StateDir, id+".json"), 65536)
	if err != nil {
		return j, err
	}
	err = json.Unmarshal(b, &j)
	return j, err
}
func (e *Engine) Operation(ctx context.Context, id string) (Operation, error) {
	j, err := e.load(id)
	return j.Operation, err
}
func (e *Engine) journals() ([]journal, error) {
	entries, err := os.ReadDir(e.config.StateDir)
	if err != nil {
		return nil, err
	}
	var out []journal
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			j, err := e.load(strings.TrimSuffix(entry.Name(), ".json"))
			if err != nil {
				return nil, err
			}
			out = append(out, j)
		}
	}
	return out, nil
}
func newUUID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
}

// Deploy executes once. Ambiguous or interrupted requests retain their disk and UUID;
// retries observe the existing operation and never replay an uncertain side effect.
func (e *Engine) Deploy(ctx context.Context, s DeploySpec) (Operation, error) {
	s = defaults(s)
	if err := validate(s); err != nil {
		return Operation{}, err
	}
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	digest := hex.EncodeToString(sum[:])
	lock, err := e.lock(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer unlock(lock)
	if j, err := e.load(s.RequestID); err == nil {
		if j.Digest != digest || j.Kind != "deploy" {
			return Operation{}, errors.New("request ID already used with different input")
		}
		return e.reconcile(ctx, j)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	entries, err := e.journals()
	if err != nil {
		return Operation{}, err
	}
	for _, j := range entries {
		if j.Kind == "deploy" {
			return Operation{}, errors.New("this host already has an appliance deployment; inspect its existing operation")
		}
	}
	p, err := e.Preflight(ctx, s)
	if err != nil {
		return Operation{}, err
	}
	if !p.Ready {
		return Operation{}, fmt.Errorf("preflight blocked: %s", strings.Join(p.Blockers, "; "))
	}
	m, err := e.verifyImage(ctx)
	if err != nil {
		return Operation{}, err
	}
	if s.CPUs < m.MinimumVCPUs || s.MemoryMiB < m.MinimumMemoryMiB || s.DiskGiB < m.MinimumDiskGiB {
		return Operation{}, errors.New("selected resources below signed appliance requirements")
	}
	vms, err := e.List(ctx)
	if err != nil {
		return Operation{}, err
	}
	for _, v := range vms {
		if v.Name == s.Name {
			return Operation{}, errors.New("VM name already exists")
		}
	}
	id, err := newUUID()
	if err != nil {
		return Operation{}, err
	}
	dir := filepath.Join(e.config.StorageDir, id)
	j := journal{Operation: Operation{ID: s.RequestID, VMID: id, Kind: "deploy", Status: "running", Stage: "copying_disk", Progress: 10, ControllerURL: staticURL(s)}, Digest: digest, Name: s.Name, Disk: filepath.Join(dir, "controller.qcow2"), Bridge: e.config.Bridge}
	if err = e.save(j); err != nil {
		return j.Operation, err
	}
	fail := func(cause error) (Operation, error) {
		j.Status = "needs_attention"
		j.Error = cause.Error()
		if err := e.save(j); err != nil {
			return j.Operation, err
		}
		return j.Operation, cause
	}
	if err = createDiskDir(dir); err != nil {
		return fail(err)
	}
	// Independent copy, verified before qemu-img can inspect or change it. Never an overlay.
	src, err := os.Open(e.config.ImagePath)
	if err != nil {
		return fail(err)
	}
	dst, err := os.OpenFile(j.Disk, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		src.Close()
		return fail(err)
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if ctx.Err() != nil {
			err = ctx.Err()
			break
		}
		n, re := src.Read(buf)
		if n > 0 {
			_, err = io.MultiWriter(dst, h).Write(buf[:n])
			if err != nil {
				break
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			err = re
			break
		}
	}
	src.Close()
	if err == nil {
		err = dst.Sync()
	}
	ce := dst.Close()
	if err == nil {
		err = ce
	}
	if err != nil {
		return fail(err)
	}
	if hex.EncodeToString(h.Sum(nil)) != m.ImageSHA256 {
		return fail(errors.New("copied appliance digest mismatch"))
	}
	if err = checkImage(ctx, j.Disk); err != nil {
		return fail(err)
	}
	if _, err = run(ctx, "qemu-img", "resize", j.Disk, strconv.Itoa(s.DiskGiB)+"G"); err != nil {
		return fail(err)
	}
	j.Stage = "creating_seed"
	j.Progress = 40
	if err = e.save(j); err != nil {
		return fail(err)
	}
	if err = e.seed(ctx, j, s); err != nil {
		return fail(err)
	}
	domain := makeDomain(j, s)
	xmlBytes, err := xml.MarshalIndent(domain, "", "  ")
	if err != nil {
		return fail(err)
	}
	domainPath := filepath.Join(dir, "domain.xml")
	if err = atomicWrite(domainPath, xmlBytes); err != nil {
		return fail(err)
	}
	j.Stage = "defining_vm"
	j.Progress = 65
	if err = e.save(j); err != nil {
		return fail(err)
	}
	if _, err = e.virsh(ctx, "define", domainPath); err != nil {
		return fail(err)
	}
	if err = e.owned(ctx, j); err != nil {
		return fail(err)
	}
	if _, err = e.virsh(ctx, "autostart", id); err != nil {
		return fail(err)
	}
	j.Stage = "starting_vm"
	j.Progress = 80
	if err = e.save(j); err != nil {
		return fail(err)
	}
	if _, err = e.virsh(ctx, "start", id); err != nil {
		return fail(err)
	}
	state, err := e.virsh(ctx, "domstate", id)
	if err != nil || strings.TrimSpace(string(state)) != "running" {
		return fail(errors.New("VM start outcome unknown; inspect existing VM before recovery"))
	}
	j.Status = "booted"
	j.Stage = "vm_running"
	j.Progress = 90
	j.ControllerURL = staticURL(s)
	err = e.save(j)
	return j.Operation, err
}
func staticURL(s DeploySpec) string {
	if s.NetworkMode == "dhcp" {
		return ""
	}
	return "https://" + strings.Split(s.AddressCIDR, "/")[0] + ":8443"
}

// DiscoverControllerURL only trusts the guest agent of the recorded, owned VM,
// and only addresses reported for its management MAC. Never use ARP guesses.
func (e *Engine) DiscoverControllerURL(ctx context.Context, id string) (string, error) {
	lock, err := e.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlock(lock)
	j, err := e.load(id)
	if err != nil {
		return "", err
	}
	if err = e.owned(ctx, j); err != nil {
		return "", err
	}
	data, err := e.virsh(ctx, "qemu-agent-command", j.VMID, `{"execute":"guest-network-get-interfaces"}`)
	if err != nil {
		return "", errors.New("waiting for the Controller guest agent and DHCP address")
	}
	address, err := managementAddress(data, mac(j.VMID))
	if err != nil {
		return "", err
	}
	j.ControllerURL = "https://" + address + ":8443"
	if err = e.save(j); err != nil {
		return "", err
	}
	return j.ControllerURL, nil
}

// WaitControllerURL bounds DHCP discovery and is safe to repeat for the same VM.
func (e *Engine) WaitControllerURL(ctx context.Context, id string) (string, error) {
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		address, err := e.DiscoverControllerURL(wait, id)
		if err == nil {
			return address, nil
		}
		select {
		case <-wait.Done():
			return "", fmt.Errorf("Controller network is not ready: %v; check DHCP and retry this deployment", err)
		case <-time.After(time.Second):
		}
	}
}

func managementAddress(data []byte, managementMAC string) (string, error) {
	var result struct {
		Interfaces []struct {
			MAC       string `json:"hardware-address"`
			Addresses []struct {
				Address string `json:"ip-address"`
				Type    string `json:"ip-address-type"`
				Prefix  int    `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"return"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", errors.New("invalid Controller guest-agent network response")
	}
	found := ""
	for _, nic := range result.Interfaces {
		if !strings.EqualFold(nic.MAC, managementMAC) {
			continue
		}
		for _, ip := range nic.Addresses {
			a, err := netip.ParseAddr(ip.Address)
			if err != nil || ip.Type != "ipv4" || !a.Is4() || !a.IsGlobalUnicast() || ip.Prefix < 1 || ip.Prefix > 32 {
				continue
			}
			if ip.Prefix < 31 {
				p := netip.PrefixFrom(a, ip.Prefix)
				if a == p.Masked().Addr() || !p.Contains(a.Next()) {
					continue
				}
			}
			if found != "" && found != a.String() {
				return "", errors.New("Controller management interface has multiple IPv4 addresses; reconcile its DHCP lease")
			}
			found = a.String()
		}
	}
	if found == "" {
		return "", errors.New("waiting for a usable DHCP IPv4 address on the Controller management interface")
	}
	return found, nil
}

func (e *Engine) reconcile(ctx context.Context, j journal) (Operation, error) {
	if j.Status != "running" && j.Status != "needs_attention" {
		return j.Operation, nil
	}
	if err := e.owned(ctx, j); err != nil {
		j.Status = "needs_attention"
		j.Error = "interrupted operation: ownership or VM observation uncertain; no effects replayed"
	} else {
		state, err := e.virsh(ctx, "domstate", j.VMID)
		if err == nil && strings.TrimSpace(string(state)) == "running" && j.Kind == "deploy" {
			j.Status = "booted"
			j.Stage = "vm_running"
			j.Progress = 90
			j.Error = ""
		} else {
			j.Status = "needs_attention"
			j.Error = "interrupted operation retained; inspect VM state and use an explicit recovery action"
		}
	}
	err := e.save(j)
	return j.Operation, err
}
func (e *Engine) Operations(ctx context.Context) ([]Operation, error) {
	js, err := e.journals()
	if err != nil {
		return nil, err
	}
	ops := make([]Operation, 0, len(js))
	for _, j := range js {
		ops = append(ops, j.Operation)
	}
	return ops, nil
}

func (e *Engine) seed(ctx context.Context, j journal, s DeploySpec) error {
	seedDir := filepath.Join(e.config.StateDir, j.VMID+"-seed")
	if err := os.Mkdir(seedDir, 0700); err != nil {
		return err
	}
	data, _ := json.Marshal(map[string]any{"admin_password": s.AdminPassword, "tls_certificate": s.TLSCertificate, "tls_private_key": s.TLSPrivateKey, "hostname": s.Hostname, "network_mode": s.NetworkMode, "address_cidr": s.AddressCIDR, "gateway": s.Gateway, "dns": s.DNS})
	var b strings.Builder
	b.WriteString("#cloud-config\nhostname: " + s.Hostname + "\nmanage_etc_hosts: true\nssh_pwauth: false\ndisable_root: true\nwrite_files:\n")
	b.WriteString("  - path: /etc/nex-controller/bootstrap.json\n    owner: root:root\n    permissions: '0600'\n    encoding: b64\n    content: " + base64.StdEncoding.EncodeToString(data) + "\n")
	if err := e.appendUpdateBootstrap(&b); err != nil {
		return err
	}
	userData := b.String()
	meta := "instance-id: " + j.VMID + "\nlocal-hostname: " + s.Hostname + "\n"
	network := fmt.Sprintf("version: 2\nethernets:\n  management:\n    match:\n      macaddress: '%s'\n    set-name: nex0\n    dhcp4: false\n    addresses: [%s]\n    routes:\n      - to: default\n        via: %s\n    nameservers:\n      addresses: [%s]\n", mac(j.VMID), s.AddressCIDR, s.Gateway, strings.Join(s.DNS, ", "))
	if s.NetworkMode == "dhcp" {
		network = fmt.Sprintf("version: 2\nethernets:\n  management:\n    match:\n      macaddress: '%s'\n    set-name: nex0\n    dhcp4: true\n", mac(j.VMID))
	}
	for name, data := range map[string]string{"user-data": userData, "meta-data": meta, "network-config": network} {
		if err := atomicWrite(filepath.Join(seedDir, name), []byte(data)); err != nil {
			return err
		}
	}
	isoPath := filepath.Join(filepath.Dir(j.Disk), "seed.iso")
	iso, err := os.OpenFile(isoPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if err = iso.Close(); err != nil {
		return err
	}
	_, err = run(ctx, "genisoimage", "-quiet", "-output", isoPath, "-volid", "cidata", "-joliet", "-rock", filepath.Join(seedDir, "user-data"), filepath.Join(seedDir, "meta-data"), filepath.Join(seedDir, "network-config"))
	return err
}

// appendUpdateBootstrap writes guest helpers into cloud-init so newly deployed
// appliances get current update tooling and firstboot even when the qcow predates them.
func (e *Engine) appendUpdateBootstrap(b *strings.Builder) error {
	dir := filepath.Join(filepath.Dir(e.config.ImagePath), "update")
	files := []struct {
		name string
		dest string
		mode string
	}{
		{"apply-update", "/usr/local/lib/nex/apply-update", "0755"},
		{"appliance-firstboot", "/usr/local/lib/nex/appliance-firstboot", "0755"},
		{"nex-controller-update.path", "/etc/systemd/system/nex-controller-update.path", "0644"},
		{"nex-controller-update.service", "/etc/systemd/system/nex-controller-update.service", "0644"},
		{"updates.conf", "/etc/nex/updates.conf", "0644"},
	}
	missing := false
	for _, f := range files {
		if _, err := os.Stat(filepath.Join(dir, f.name)); err != nil {
			missing = true
			break
		}
	}
	if missing {
		return nil
	}
	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "  - path: %s\n    owner: root:root\n    permissions: '%s'\n    encoding: b64\n    content: %s\n",
			f.dest, f.mode, base64.StdEncoding.EncodeToString(raw))
	}
	b.WriteString("runcmd:\n")
	b.WriteString("  - [mkdir, -p, /etc/nex, /usr/local/lib/nex]\n")
	b.WriteString("  - [systemctl, daemon-reload]\n")
	b.WriteString("  - [systemctl, enable, --now, nex-controller-update.path]\n")
	return nil
}
func mac(id string) string {
	h := strings.ReplaceAll(id, "-", "")
	return "52:54:00:" + h[0:2] + ":" + h[2:4] + ":" + h[4:6]
}

type domainXML struct {
	XMLName  xml.Name    `xml:"domain"`
	Type     string      `xml:"type,attr"`
	Name     string      `xml:"name"`
	UUID     string      `xml:"uuid"`
	Memory   memoryXML   `xml:"memory"`
	VCPU     int         `xml:"vcpu"`
	CPU      cpuXML      `xml:"cpu"`
	OS       osXML       `xml:"os"`
	Features featuresXML `xml:"features"`
	Devices  devicesXML  `xml:"devices"`
}
type cpuXML struct {
	Mode  string `xml:"mode,attr"`
	Check string `xml:"check,attr,omitempty"`
}
type memoryXML struct {
	Unit  string `xml:"unit,attr"`
	Value int    `xml:",chardata"`
}
type osXML struct {
	Type osTypeXML `xml:"type"`
}
type osTypeXML struct {
	Arch    string `xml:"arch,attr"`
	Machine string `xml:"machine,attr"`
	Value   string `xml:",chardata"`
}
type featuresXML struct {
	ACPI struct{} `xml:"acpi"`
	APIC struct{} `xml:"apic"`
}
type devicesXML struct {
	Disks       []diskXML      `xml:"disk"`
	Interfaces  []interfaceXML `xml:"interface"`
	Channels    []channelXML   `xml:"channel"`
	Graphics    []graphicsXML  `xml:"graphics"`
	Hostdev     []struct{}     `xml:"hostdev"`
	Filesystems []struct{}     `xml:"filesystem"`
}
type diskXML struct {
	Type      string    `xml:"type,attr"`
	Device    string    `xml:"device,attr"`
	Driver    driverXML `xml:"driver"`
	Source    sourceXML `xml:"source"`
	Target    targetXML `xml:"target"`
	Readonly  *struct{} `xml:"readonly,omitempty"`
	Shareable *struct{} `xml:"shareable,omitempty"`
	Backing   *struct{} `xml:"backingStore,omitempty"`
}
type driverXML struct {
	Name string `xml:"name,attr"`
	Type string `xml:"type,attr"`
}
type sourceXML struct {
	File     string `xml:"file,attr,omitempty"`
	Bridge   string `xml:"bridge,attr,omitempty"`
	Path     string `xml:"path,attr,omitempty"`
	Mode     string `xml:"mode,attr,omitempty"`
	Dev      string `xml:"dev,attr,omitempty"`
	Protocol string `xml:"protocol,attr,omitempty"`
}
type targetXML struct {
	Dev  string `xml:"dev,attr,omitempty"`
	Bus  string `xml:"bus,attr,omitempty"`
	Type string `xml:"type,attr,omitempty"`
	Name string `xml:"name,attr,omitempty"`
}
type interfaceXML struct {
	Type   string    `xml:"type,attr"`
	Source sourceXML `xml:"source"`
	MAC    struct {
		Address string `xml:"address,attr"`
	} `xml:"mac"`
	Model struct {
		Type string `xml:"type,attr"`
	} `xml:"model"`
}
type channelXML struct {
	Type   string            `xml:"type,attr"`
	Source *channelSourceXML `xml:"source,omitempty"`
	Target targetXML         `xml:"target"`
}

// channelSourceXML carries the qemu-vdagent options: clipboard relay between the VNC
// client and a spice-vdagent in the guest; mouse stays with the tablet device.
type channelSourceXML struct {
	Clipboard struct {
		Copypaste string `xml:"copypaste,attr"`
	} `xml:"clipboard"`
	Mouse struct {
		Mode string `xml:"mode,attr"`
	} `xml:"mouse"`
}
type inputXML struct {
	Type string `xml:"type,attr"`
	Bus  string `xml:"bus,attr"`
}
type videoXML struct {
	Model struct {
		Type string `xml:"type,attr"`
	} `xml:"model"`
}
type graphicsXML struct {
	Type     string `xml:"type,attr"`
	Socket   string `xml:"socket,attr,omitempty"`
	Autoport string `xml:"autoport,attr"`
	Listen   struct {
		Type   string `xml:"type,attr"`
		Socket string `xml:"socket,attr,omitempty"`
	} `xml:"listen"`
}

func makeDomain(j journal, s DeploySpec) domainXML {
	d := domainXML{Type: "kvm", Name: j.Name, UUID: j.VMID, Memory: memoryXML{"MiB", s.MemoryMiB}, VCPU: s.CPUs, CPU: cpuXML{Mode: "host-passthrough", Check: "none"}, OS: osXML{osTypeXML{"x86_64", "q35", "hvm"}}}
	d.Devices.Disks = []diskXML{{Type: "file", Device: "disk", Driver: driverXML{"qemu", "qcow2"}, Source: sourceXML{File: j.Disk}, Target: targetXML{Dev: "vda", Bus: "virtio"}}, {Type: "file", Device: "cdrom", Driver: driverXML{"qemu", "raw"}, Source: sourceXML{File: filepath.Join(filepath.Dir(j.Disk), "seed.iso")}, Target: targetXML{Dev: "sda", Bus: "sata"}, Readonly: &struct{}{}}}
	nic := interfaceXML{Type: "bridge", Source: sourceXML{Bridge: j.Bridge}}
	nic.MAC.Address = mac(j.VMID)
	nic.Model.Type = "virtio"
	d.Devices.Interfaces = []interfaceXML{nic}
	d.Devices.Channels = []channelXML{{Type: "unix", Target: targetXML{Type: "virtio", Name: "org.qemu.guest_agent.0"}}}
	g := graphicsXML{Type: "vnc", Autoport: "no"}
	g.Listen.Type = "socket"
	d.Devices.Graphics = []graphicsXML{g}
	return d
}

// Deployments lists the Controller appliance VMs the local engine deployed under stateDir, the
// host's durable ownership record the agent reports so a replaced Controller re-adopts them.
// Unreadable entries are skipped; a missing directory means no deployments.
func Deployments(stateDir string) []protocol.Appliance {
	e := &Engine{config: Config{StateDir: stateDir}}
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return nil
	}
	var out []protocol.Appliance
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		j, err := e.load(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil || j.Kind != "deploy" || !uuidPattern.MatchString(j.VMID) {
			continue
		}
		out = append(out, protocol.Appliance{ID: j.VMID, Name: j.Name, RequestID: j.ID, Role: "controller"})
	}
	return out
}
func (e *Engine) deployment(id string) (journal, error) {
	js, err := e.journals()
	if err != nil {
		return journal{}, err
	}
	for _, j := range js {
		if j.Kind == "deploy" && j.VMID == id {
			return j, nil
		}
	}
	return journal{}, errors.New("Local power control is only available for the Controller appliance deployed from this page")
}
func (e *Engine) owned(ctx context.Context, j journal) error {
	if !uuidPattern.MatchString(j.VMID) || j.Disk != filepath.Join(e.config.StorageDir, j.VMID, "controller.qcow2") || j.Bridge != e.config.Bridge {
		return errors.New("persisted ownership is not valid for this host configuration")
	}
	real, err := filepath.EvalSymlinks(j.Disk)
	if err != nil || real != j.Disk {
		return errors.New("owned disk missing or replaced by a symlink")
	}
	if err = e.exclusiveDisk(ctx, j); err != nil {
		return err
	}
	var stat syscall.Statfs_t
	if err = syscall.Statfs(j.Disk, &stat); err != nil {
		return err
	}
	switch uint64(stat.Type) {
	case 0xef53, 0x58465342, 0x9123683e:
	default:
		return errors.New("owned disk is no longer on supported local storage")
	}
	for _, args := range [][]string{{"dumpxml", j.VMID}, {"dumpxml", j.VMID, "--inactive"}} {
		b, err := e.virsh(ctx, args...)
		if err != nil {
			return errors.New("cannot establish current and persistent VM ownership")
		}
		var d domainXML
		if xml.Unmarshal(b, &d) != nil || d.UUID != j.VMID || d.Name != j.Name || d.Type != "kvm" {
			return errors.New("domain identity does not match persisted ownership")
		}
		if len(d.Devices.Disks) < 1 || len(d.Devices.Disks) > 2 || len(d.Devices.Interfaces) != 1 || len(d.Devices.Hostdev) > 0 || len(d.Devices.Filesystems) > 0 {
			return errors.New("unsupported domain devices; local power blocked")
		}
		writable := 0
		for _, disk := range d.Devices.Disks {
			if disk.Device == "disk" {
				writable++
				if disk.Type != "file" || disk.Source.File != j.Disk || disk.Driver.Type != "qcow2" || disk.Readonly != nil || disk.Shareable != nil || disk.Source.Dev != "" || disk.Source.Protocol != "" {
					return errors.New("VM writable disk ownership changed")
				}
			} else if disk.Device != "cdrom" || disk.Readonly == nil || (disk.Source.File != "" && disk.Source.File != filepath.Join(filepath.Dir(j.Disk), "seed.iso")) {
				return errors.New("unsupported VM media")
			}
		}
		if writable != 1 || d.Devices.Interfaces[0].Type != "bridge" || d.Devices.Interfaces[0].Source.Bridge != j.Bridge || !strings.EqualFold(d.Devices.Interfaces[0].MAC.Address, mac(j.VMID)) {
			return errors.New("VM disk/network assignment changed")
		}
	}
	// Inspect backing metadata too: a local pathname alone cannot establish exclusive storage.
	if err = checkImage(ctx, j.Disk); err != nil {
		return err
	}
	return e.powerState(ctx, j.VMID)
}
func (e *Engine) powerState(ctx context.Context, id string) error {
	state, err := e.virsh(ctx, "domstate", id)
	if err != nil {
		return errors.New("cannot establish VM execution state")
	}
	if strings.TrimSpace(string(state)) == "shut off" {
		return nil
	}
	if strings.TrimSpace(string(state)) != "running" {
		return errors.New("VM execution state is not eligible for local control")
	}
	info, err := e.virsh(ctx, "domjobinfo", id)
	if err != nil {
		return errors.New("cannot rule out an active migration or other domain job")
	}
	if !strings.Contains(string(info), "None") {
		return errors.New("VM has an active or unknown domain job")
	}
	return nil
}
func (e *Engine) List(ctx context.Context) ([]VM, error) {
	b, err := e.virsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return nil, err
	}
	ids := strings.Fields(string(b))
	if len(ids) > 256 {
		return nil, errors.New("VM observation limit exceeded (256)")
	}
	out := make([]VM, 0, len(ids))
	for _, id := range ids {
		if !uuidPattern.MatchString(id) {
			return nil, errors.New("invalid observed VM identity")
		}
		vm := VM{ID: id, State: "unknown"}
		name, err := e.virsh(ctx, "domname", id)
		if err != nil {
			return nil, err
		}
		vm.Name = strings.TrimSpace(string(name))
		state, err := e.virsh(ctx, "domstate", id)
		if err != nil {
			return nil, err
		}
		vm.State = strings.TrimSpace(string(state))
		j, err := e.deployment(id)
		if err == nil {
			vm.Managed = true
			err = e.owned(ctx, j)
			vm.PowerAllowed = err == nil
		}
		if err != nil {
			vm.BlockedReason = err.Error()
		}
		out = append(out, vm)
	}
	return out, nil
}
func (e *Engine) Power(ctx context.Context, requestID, id, action string) (Operation, error) {
	if !identifier.MatchString(requestID) || !uuidPattern.MatchString(id) {
		return Operation{}, errors.New("invalid request or VM ID")
	}
	switch action {
	case "start", "shutdown", "force-stop":
	default:
		return Operation{}, errors.New("unsupported power action")
	}
	lock, err := e.lock(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer unlock(lock)
	digest := id + ":" + action
	if old, err := e.load(requestID); err == nil {
		if old.Kind != "power" || old.Digest != digest {
			return Operation{}, errors.New("request ID already used with different input")
		}
		return e.reconcile(ctx, old)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Operation{}, err
	}
	owner, err := e.deployment(id)
	if err != nil {
		return Operation{}, err
	}
	if err = e.owned(ctx, owner); err != nil {
		return Operation{}, err
	}
	state, err := e.virsh(ctx, "domstate", id)
	if err != nil {
		return Operation{}, err
	}
	current := strings.TrimSpace(string(state))
	if current != "running" && current != "shut off" {
		return Operation{}, errors.New("VM state is not eligible for local power recovery")
	}
	j := owner
	j.Operation = Operation{ID: requestID, VMID: id, Kind: "power", Status: "running", Stage: action, Progress: 20}
	j.Digest = digest
	if err = e.save(j); err != nil {
		return j.Operation, err
	}
	if (action == "start" && current == "shut off") || (action != "start" && current == "running") {
		command := action
		if command == "force-stop" {
			command = "destroy"
		}
		_, err = e.virsh(ctx, command, id)
	}
	if err != nil {
		j.Status = "needs_attention"
		j.Error = "power outcome uncertain; refresh observation before a new recovery request"
	} else {
		j.Status = "completed"
		j.Progress = 100
		if action == "shutdown" && current == "running" {
			j.Stage = "shutdown_requested"
		} else {
			j.Stage = "command_completed"
		}
	}
	saveErr := e.save(j)
	if saveErr != nil {
		return j.Operation, saveErr
	}
	return j.Operation, err
}

// RemoveSeed is called only after the caller verifies Controller readiness. It
// removes bootstrap secrets, never the VM's writable disk. Repeated calls are safe.
func (e *Engine) RemoveSeed(ctx context.Context, id string) error {
	lock, err := e.lock(ctx)
	if err != nil {
		return err
	}
	defer unlock(lock)
	j, err := e.deployment(id)
	if err != nil {
		return err
	}
	if err = e.owned(ctx, j); err != nil {
		return err
	}
	for _, live := range []bool{false, true} {
		args := []string{"dumpxml", id}
		if !live {
			args = append(args, "--inactive")
		}
		b, err := e.virsh(ctx, args...)
		if err != nil {
			return err
		}
		var d domainXML
		if err = xml.Unmarshal(b, &d); err != nil {
			return err
		}
		for _, disk := range d.Devices.Disks {
			if disk.Device == "cdrom" && disk.Source.File != "" {
				args = []string{"change-media", id, disk.Target.Dev, "--eject"}
				if live {
					state, err := e.virsh(ctx, "domstate", id)
					if err != nil {
						return err
					}
					if strings.TrimSpace(string(state)) == "shut off" {
						continue
					}
					args = append(args, "--live")
				} else {
					args = append(args, "--config")
				}
				if _, err = e.virsh(ctx, args...); err != nil {
					return err
				}
			}
		}
	}
	path := filepath.Join(filepath.Dir(j.Disk), "seed.iso")
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	seedDir := filepath.Join(e.config.StateDir, id+"-seed")
	for _, name := range []string{"user-data", "meta-data", "network-config"} {
		if err = os.Remove(filepath.Join(seedDir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err = os.Remove(seedDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Check both running and persistent assignments; a pathname is not proof that
// another libvirt domain has not been attached to the same writable image.
func (e *Engine) exclusiveDisk(ctx context.Context, j journal) error {
	owned, err := os.Stat(j.Disk)
	if err != nil {
		return err
	}
	if st, ok := owned.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return errors.New("owned disk has unsupported hardlink aliases")
	}
	ids, err := e.virsh(ctx, "list", "--all", "--uuid")
	if err != nil {
		return err
	}
	if len(strings.Fields(string(ids))) > 256 {
		return errors.New("VM ownership observation limit exceeded")
	}
	for _, id := range strings.Fields(string(ids)) {
		if id == j.VMID {
			continue
		}
		if !uuidPattern.MatchString(id) {
			return errors.New("invalid observed VM identity")
		}
		for _, args := range [][]string{{"dumpxml", id}, {"dumpxml", id, "--inactive"}} {
			b, err := e.virsh(ctx, args...)
			if err != nil {
				return errors.New("cannot establish other domain storage ownership")
			}
			decoder := xml.NewDecoder(bytes.NewReader(b))
			for {
				token, err := decoder.Token()
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
				element, ok := token.(xml.StartElement)
				if !ok || element.Name.Local != "source" {
					continue
				}
				for _, attr := range element.Attr {
					if attr.Name.Local != "file" {
						continue
					}
					other, err := os.Stat(attr.Value)
					if err != nil {
						return errors.New("other domain disk cannot be inspected for exclusive ownership")
					}
					if os.SameFile(owned, other) {
						return errors.New("another domain references the appliance disk; local control blocked")
					}
				}
			}
		}
	}
	return nil
}

func createDiskDir(path string) error {
	if err := os.Mkdir(path, 0711); err != nil {
		return err
	}
	// The service's UMask=0077 must not remove QEMU's directory traversal.
	return os.Chmod(path, 0711)
}
