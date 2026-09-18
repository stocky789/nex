// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

// CheckVMPlacement performs the native, no-effect part of vm-create or vm-update
// admission.  The operations lock makes its journal snapshot and native observations
// coherent with Execute; Execute repeats these checks before it changes anything.
func (e *ManagedEngine) CheckVMPlacement(ctx context.Context, cmd protocol.Command) ([]protocol.StorageRegistration, error) {
	if (cmd.Kind != "vm-create" && cmd.Kind != "vm-update") || validateManaged(cmd) != nil {
		return nil, errors.New("VM placement requires a valid vm-create or vm-update command")
	}
	if !filepath.IsAbs(e.stateDir) || filepath.Clean(e.stateDir) == "/" {
		return nil, errors.New("managed state requires an absolute non-root directory")
	}
	lock, err := (&Engine{config: Config{StateDir: e.stateDir}}).lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock(lock)
	js, err := e.journals()
	if err != nil {
		return nil, err
	}
	for _, old := range js {
		if old.Command.OperationID != cmd.OperationID {
			continue
		}
		if !sameManagedCommand(old.Command, cmd) {
			return nil, errors.New("operation ID already belongs to different input")
		}
		// Execute owns replay and reconciliation of a journaled operation.  Do not
		// turn its pending, failed, or uncertain result into a fresh admission
		// refusal: the caller must reach Execute so it reports the durable outcome.
		return placementRecordedStorage(js, *cmd.VM), nil
	}
	if cmd.Kind == "vm-create" {
		return e.checkVMCreatePlacement(ctx, js, *cmd.VM)
	}
	return e.checkVMUpdatePlacement(ctx, js, *cmd.VM)
}

func placementRecordedStorage(js []managedJournal, v protocol.VMCreateSpec) []protocol.StorageRegistration {
	ids := []string{}
	for _, disk := range v.Disks {
		if !slices.Contains(ids, disk.StorageID) {
			ids = append(ids, disk.StorageID)
		}
	}
	for _, id := range []string{v.MediaID, v.DriversMediaID} {
		if media, _, err := publishedMediaPath(js, id); err == nil {
			if !slices.Contains(ids, media.StorageID) {
				ids = append(ids, media.StorageID)
			}
		}
	}
	out := make([]protocol.StorageRegistration, 0, len(ids))
	for _, id := range ids {
		if storage, err := managedStorage(js, id); err == nil {
			out = append(out, storage)
		}
	}
	return out
}

func sameManagedCommand(a, b protocol.Command) bool {
	got, _ := json.Marshal(a)
	want, _ := json.Marshal(b)
	return string(got) == string(want)
}

func (e *ManagedEngine) checkVMCreatePlacement(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec) ([]protocol.StorageRegistration, error) {
	targets, _, err := managedDiskPlan(js, v)
	if err != nil {
		return nil, err
	}
	storage, err := placementStorage(ctx, js, v, targets)
	if err != nil {
		return nil, err
	}
	if err = placementNetworks(ctx, js, v); err != nil {
		return nil, err
	}
	if err = placementMedia(ctx, js, v, false); err != nil {
		return nil, err
	}
	if err = managedCapacity(ctx, v, js); err != nil {
		return nil, err
	}
	if err = vmConflict(js, v); err != nil {
		return nil, err
	}
	if err = placementLibvirtConflict(ctx, v); err != nil {
		return nil, err
	}
	return storage, nil
}

func (e *ManagedEngine) checkVMUpdatePlacement(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec) ([]protocol.StorageRegistration, error) {
	cur, err := managedCurrent(js, v.ID)
	if err != nil {
		return nil, err
	}
	if v.Revision != cur.Command.VM.Revision+1 {
		return nil, fmt.Errorf("VM definition revision %d is not current; refresh the VM and edit again", v.Revision)
	}
	stops, err := protocol.NeedsStop(*cur.Command.VM, v)
	if err != nil {
		return nil, err
	}
	state, err := managedVirsh(ctx, "domstate", v.ID)
	if err != nil {
		return nil, errors.New("VM not found or its state cannot be established on this host")
	}
	observed := strings.TrimSpace(string(state))
	if observed == domainAsleep {
		return nil, errors.New("VM is asleep; use Start to wake it before editing it")
	}
	if observed != "running" && observed != "shut off" {
		return nil, errors.New("VM is " + observed + "; resolve that state before editing it")
	}
	running := observed == "running"
	if running && len(stops) != 0 {
		names := make([]string, len(stops))
		for i, stop := range stops {
			names[i] = string(stop)
		}
		return nil, errors.New("stop the VM first to change " + strings.Join(names, ", "))
	}
	if running {
		if err = placementLiveUpdate(ctx, v); err != nil {
			return nil, err
		}
	}
	if v.Name != cur.Command.VM.Name {
		names, err := managedVirsh(ctx, "list", "--all", "--name")
		if err != nil {
			return nil, err
		}
		for _, name := range strings.Split(string(names), "\n") {
			if name == v.Name {
				return nil, errors.New("VM name already exists")
			}
		}
	}
	if len(cur.Disks) != len(cur.Command.VM.Disks) || len(cur.Targets) != len(cur.Command.VM.Disks) {
		return nil, errors.New("current VM disk layout is incomplete; inspect the managed journal before editing")
	}
	layout, err := managedUpdateLayout(js, cur, v)
	if err != nil {
		return nil, err
	}
	storage, err := placementStorage(ctx, js, v, layout.Targets_)
	if err != nil {
		return nil, err
	}
	if err = placementNetworks(ctx, js, v); err != nil {
		return nil, err
	}
	if err = placementMedia(ctx, js, v, true); err != nil {
		return nil, err
	}
	if !running {
		if err = managedCapacity(ctx, v, js); err != nil {
			return nil, err
		}
	}
	return storage, nil
}

func placementStorage(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec, targets []managedTarget) ([]protocol.StorageRegistration, error) {
	required := map[string]uint64{}
	for _, target := range targets {
		required[target.ID] = target.Bytes
	}
	ids := make([]string, 0, len(v.Disks)+2)
	for _, disk := range v.Disks {
		if !slices.Contains(ids, disk.StorageID) {
			ids = append(ids, disk.StorageID)
		}
	}
	for _, id := range []string{v.MediaID, v.DriversMediaID} {
		if id == "" {
			continue
		}
		media, _, err := publishedMediaPath(js, id)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(ids, media.StorageID) {
			ids = append(ids, media.StorageID)
		}
	}
	out := make([]protocol.StorageRegistration, 0, len(ids))
	for _, id := range ids {
		s, access, err := managedStorageByID(ctx, js, id, required[id])
		if err != nil {
			return nil, err
		}
		if s.PoolID != v.PoolID || s.HostID != v.HostID {
			return nil, errors.New("storage scope does not match the VM")
		}
		s.Access = protocol.StorageAccess{Status: access.Health, Readable: true, Writable: !access.ReadOnly, TotalBytes: access.Total, AvailableBytes: &access.Available, ObservedAt: time.Now().UTC()}
		out = append(out, s)
	}
	return out, nil
}

func placementNetworks(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec) error {
	for _, nic := range v.NICs {
		n, err := managedNetwork(js, nic.NetworkID)
		if err != nil {
			return err
		}
		if n.PoolID != v.PoolID || n.HostID != v.HostID {
			return errors.New("network scope does not match the VM")
		}
		if err = verifyNetwork(ctx, n); err != nil {
			return err
		}
	}
	return nil
}

func placementMedia(ctx context.Context, js []managedJournal, v protocol.VMCreateSpec, requireStorage bool) error {
	for _, id := range []string{v.MediaID, v.DriversMediaID} {
		if id == "" {
			continue
		}
		m, path, err := publishedMediaPath(js, id)
		if err != nil {
			return err
		}
		if m.PoolID != v.PoolID || m.HostID != v.HostID {
			return errors.New("media scope does not match the VM")
		}
		if requireStorage || storageWasPrepared(js, m.StorageID) {
			if _, _, err = managedStorageByID(ctx, js, m.StorageID, 0); err != nil {
				return err
			}
		}
		if err = verifyISO(path, m); err != nil {
			return err
		}
	}
	return nil
}

func placementLiveUpdate(ctx context.Context, v protocol.VMCreateSpec) error {
	if v.Memory.Mode == "dedicated" {
		b, err := managedVirsh(ctx, "dumpxml", v.ID, "--inactive")
		var domain managedDomain
		if err != nil || xml.Unmarshal(b, &domain) != nil {
			return errors.New("cannot verify dedicated memory reservation before editing")
		}
		if !reflect.DeepEqual(domain.MemoryBacking, dedicatedMemoryBacking()) {
			return errors.New("stop the VM and save its settings to apply the dedicated memory reservation before live editing")
		}
	}
	persistent, err := dumpManagedDomain(ctx, v.ID, false)
	if err != nil {
		return err
	}
	live, err := dumpManagedDomain(ctx, v.ID, true)
	if err != nil {
		return err
	}
	held := map[string]bool{}
	for _, nic := range live.Devices.Interfaces {
		held[nic.MAC.Address] = true
	}
	for _, nic := range persistent.Devices.Interfaces {
		delete(held, nic.MAC.Address)
	}
	for i, nic := range v.NICs {
		if mac := protocol.NICMAC(v.ID, i, nic); held[mac] {
			return fmt.Errorf("network interface MAC %s is still attached to the running VM; wait for the guest to release it or stop the VM, then save again", mac)
		}
	}
	return nil
}

func placementLibvirtConflict(ctx context.Context, v protocol.VMCreateSpec) error {
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
	for _, id := range strings.Fields(string(ids)) {
		b, err := managedVirsh(ctx, "dumpxml", id, "--inactive")
		if err != nil {
			return errors.New("cannot read an existing VM definition to check MAC uniqueness")
		}
		var domain managedDomain
		if xml.Unmarshal(b, &domain) != nil {
			continue
		}
		for _, nic := range domain.Devices.Interfaces {
			if slices.Contains(vmMACs(v), strings.ToLower(nic.MAC.Address)) {
				return errors.New("MAC address " + nic.MAC.Address + " is already used by VM " + domain.Name + " on this host")
			}
		}
	}
	return nil
}
