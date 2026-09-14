// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

type poolVMStartPermit struct {
	PoolID string `json:"pool_id"`
	HostID string `json:"host_id"`
	VMID   string `json:"vm_id"`
	Permit string `json:"permit"`
	Epoch  uint64 `json:"epoch"`
	BootID string `json:"boot_id"`
}

func poolVMStartPermitPath(stateDir, vmID string) string {
	return filepath.Join(stateDir, "pool-vm-permits", vmID+".json")
}
func poolVMAdmissionPath(stateDir, vmID string) string {
	return filepath.Join(stateDir, "pool-vm-admissions", vmID+".json")
}

// WritePoolVMStartPermit is called only after the pool master durably admits this
// host's start.  The hook consumes it once, so a direct libvirt/autostart attempt
// cannot reuse an old admission.
func WritePoolVMStartPermit(stateDir, vmID, permit string) error {
	return WritePoolVMStartPermitAdmission(stateDir, "local", "local", vmID, 1, permit)
}

// WritePoolVMStartPermitAdmission persists the exact master-issued execution
// epoch alongside the one-use permit.  It is private Core state, not a
// libvirt or client supplied token.
func WritePoolVMStartPermitAdmission(stateDir, poolID, hostID, vmID string, epoch uint64, permit string) error {
	if poolID == "" || hostID == "" || !uuidPattern.MatchString(vmID) || epoch == 0 || len(permit) != 64 {
		return errors.New("invalid pool VM start permit")
	}
	dir := filepath.Join(stateDir, "pool-vm-permits")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return err
	}
	bootID, err := localBootID()
	if err != nil {
		return err
	}
	b, err := json.Marshal(poolVMStartPermit{PoolID: poolID, HostID: hostID, VMID: vmID, Permit: permit, Epoch: epoch, BootID: bootID})
	if err != nil {
		return err
	}
	admissionDir := filepath.Join(stateDir, "pool-vm-admissions")
	if err := os.MkdirAll(admissionDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(admissionDir, 0700); err != nil {
		return err
	}
	// Write the durable current admission before making its consumable permit
	// visible.  A crash between these writes fails closed: an older permit cannot
	// match the newer epoch record.
	if err := atomicWrite(poolVMAdmissionPath(stateDir, vmID), b); err != nil {
		return err
	}
	return atomicWrite(poolVMStartPermitPath(stateDir, vmID), b)
}

func localBootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil || !uuidPattern.MatchString(strings.TrimSpace(string(b))) {
		return "", errors.New("local boot identity is unavailable")
	}
	return strings.TrimSpace(string(b)), nil
}
func RemovePoolVMStartPermit(stateDir, vmID string) error {
	if !uuidPattern.MatchString(vmID) {
		return errors.New("invalid pool VM identity")
	}
	err := os.Remove(poolVMStartPermitPath(stateDir, vmID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	err = os.Remove(poolVMAdmissionPath(stateDir, vmID))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func consumePoolVMStartPermit(stateDir, vmID string) error {
	path := poolVMStartPermitPath(stateDir, vmID)
	claimed := path + ".claimed"
	// rename is atomic: concurrent libvirt prepare hooks cannot both consume one permit.
	if err := os.Rename(path, claimed); err != nil {
		return errors.New("managed shared VM startup lacks a Core execution permit")
	}
	defer os.Remove(claimed)
	b, err := os.ReadFile(claimed)
	if err != nil {
		return errors.New("managed shared VM startup lacks a Core execution permit")
	}
	var p poolVMStartPermit
	bootID, bootErr := localBootID()
	if json.Unmarshal(b, &p) != nil || p.PoolID == "" || p.HostID == "" || p.VMID != vmID || p.Epoch == 0 || len(p.Permit) != 64 || bootErr != nil || p.BootID != bootID {
		return errors.New("managed shared VM execution permit is invalid")
	}
	current, err := os.ReadFile(poolVMAdmissionPath(stateDir, vmID))
	if err != nil {
		return errors.New("managed shared VM execution admission is unavailable")
	}
	var admission poolVMStartPermit
	if json.Unmarshal(current, &admission) != nil || admission.PoolID != p.PoolID || admission.HostID != p.HostID || admission.VMID != p.VMID || admission.Epoch != p.Epoch || admission.BootID != p.BootID {
		return errors.New("managed shared VM execution permit is stale")
	}
	return nil
}

// StorageGuard checks libvirt's prepare/begin XML without calling libvirt or taking
// the managed mutation lock: libvirt invokes it while the initiating operation
// already holds that lock. It never mounts, logs in, or repairs a repository.
func StorageGuard(ctx context.Context, stateDir string, input io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil || len(b) > 1<<20 {
		return errors.New("storage startup XML is unavailable or exceeds 1 MiB")
	}
	var domain domainXML
	if err := xml.Unmarshal(b, &domain); err != nil || domain.XMLName.Local != "domain" {
		return errors.New("invalid storage startup domain XML")
	}
	js, err := NewManaged(filepath.Join(stateDir, "managed-vms")).journals()
	if err != nil {
		if os.IsNotExist(err) && !hasManagedDisk(domain) {
			return nil // A fresh host can still boot its independent Controller appliance.
		}
		return errors.New("storage startup cannot read the Core repository journal")
	}
	shared := false
	err = checkStorageStartup(ctx, domain, js, func(ctx context.Context, s protocol.StorageRegistration) error {
		shared = shared || protocol.IsPoolSharedStorage(s)
		a, err := storageInspect(ctx, s)
		if err != nil {
			return err
		}
		return storageVerify(ctx, s, a)
	})
	if err != nil {
		return err
	}
	if shared && hasManagedDisk(domain) {
		if err := consumePoolVMStartPermit(stateDir, domain.UUID); err != nil {
			return err
		}
	}
	if hasManagedDisk(domain) {
		for _, disk := range domain.Devices.Disks {
			if disk.Source.File != "" {
				if err := startupDiskFile(disk.Source.File); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func startupDiskFile(path string) error {
	real, err := filepath.EvalSymlinks(path)
	if err != nil || real != path {
		return errors.New("managed VM startup disk is missing or replaced by a symlink")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("managed VM startup requires a regular disk file")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || st.Nlink != 1 {
		return errors.New("managed VM startup disk has hardlink aliases")
	}
	return nil
}

func hasManagedDisk(d domainXML) bool {
	for _, disk := range d.Devices.Disks {
		if strings.Contains(disk.Source.File, "/nex-vms/") {
			return true
		}
	}
	return false
}

func checkStorageStartup(ctx context.Context, d domainXML, js []managedJournal, verify func(context.Context, protocol.StorageRegistration) error) error {
	managed := hasManagedDisk(d)
	for _, j := range js {
		if j.Command.VM != nil && j.Command.VM.ID == d.UUID {
			managed = true
		}
	}
	if managed && !uuidPattern.MatchString(d.UUID) {
		return errors.New("managed VM startup requires a valid VM identity")
	}
	repositories := storageProjection(js)
	checked := map[string]bool{}
	for _, disk := range d.Devices.Disks {
		if disk.Device != "disk" && disk.Device != "cdrom" {
			continue
		}
		path := disk.Source.File
		if path == "" && disk.Device == "cdrom" {
			continue
		}
		if !managed {
			continue // This hook does not change ownership of unrelated guests.
		}
		if disk.Type != "file" || !filepath.IsAbs(path) || filepath.Clean(path) != path || disk.Source.Dev != "" || disk.Source.Protocol != "" {
			return errors.New("managed VM startup requires a registered filesystem disk")
		}
		found := false
		for _, s := range repositories {
			root := storageRegistrationPath(s)
			if !strings.HasPrefix(path, root+string(filepath.Separator)) {
				continue
			}
			if disk.Device == "disk" && !strings.HasPrefix(path, filepath.Join(root, "nex-vms", d.UUID)+string(filepath.Separator)) {
				return errors.New("managed VM startup disk does not belong to this VM")
			}
			if !checked[s.ID] {
				if err := verify(ctx, s); err != nil {
					return errors.New("managed VM startup blocked: repository is unavailable or its identity changed")
				}
				checked[s.ID] = true
			}
			found = true
			break
		}
		if !found {
			return errors.New("managed VM startup disk repository is missing or detached")
		}
	}
	return nil
}
