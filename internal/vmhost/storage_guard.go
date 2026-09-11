// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"nex.local/nex/internal/protocol"
)

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
	err = checkStorageStartup(ctx, domain, js, func(ctx context.Context, s protocol.StorageRegistration) error {
		a, err := storageInspect(ctx, s)
		if err != nil {
			return err
		}
		return storageVerify(ctx, s, a)
	})
	if err != nil {
		return err
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
