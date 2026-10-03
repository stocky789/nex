// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"time"

	"nex.local/nex/internal/vmhost"
)

const updateRestartFile = "update-restart.json"

// updateRestartRecord is the durable, Core-owned list of managed guests that were running
// when an update shut them down for a reboot. It is a one-shot boot intent for the boot
// pass beside each guest's own Autostart setting, never a second way to start a guest: the
// pass still takes the master's admission, maintenance and authority gates for every one.
type updateRestartRecord struct {
	// BootID is the boot the guests were shut down in. The pass honours the record only on a
	// different boot; empty means "restore now" after a reboot that did not happen.
	BootID string   `json:"boot_id"`
	VMIDs  []string `json:"vm_ids"`
}

func updateRestartPath(s *Store) string { return filepath.Join(s.dir, updateRestartFile) }

func saveUpdateRestart(s *Store, r updateRestartRecord) error {
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return vmhost.AtomicWrite(updateRestartPath(s), b, 0600)
}

// pendingUpdateRestart returns the guests to start after an update reboot, or nil when there
// is no record or the host has not rebooted since it was written.
func pendingUpdateRestart(s *Store) map[string]bool {
	b, err := os.ReadFile(updateRestartPath(s))
	if err != nil {
		return nil
	}
	var r updateRestartRecord
	if json.Unmarshal(b, &r) != nil {
		return nil
	}
	if r.BootID != "" {
		if boot, err := poolVMAutostartBootID(); err != nil || boot == r.BootID {
			return nil
		}
	}
	ids := make(map[string]bool, len(r.VMIDs))
	for _, id := range r.VMIDs {
		ids[id] = true
	}
	return ids
}

// PrepareUpdateReboot shuts every running guest down gracefully ahead of a host reboot and
// records which managed ones to bring back afterwards. It never forces a guest off: if any
// guest is still running after wait, the guests it stopped are started again and an error
// says the host must not be rebooted. On success the returned undo starts them again, for a
// reboot that then cannot be scheduled.
func PrepareUpdateReboot(ctx context.Context, s *Store, wait time.Duration) (undo func(context.Context), err error) {
	running, err := vmhost.RunningGuests(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not list running guests: %w", err)
	}
	managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	var managedRunning []string
	if managed != nil {
		for _, vm := range managed.VMs {
			if slices.Contains(running, vm.ID) {
				managedRunning = append(managedRunning, vm.ID)
			}
		}
	}
	boot, err := poolVMAutostartBootID()
	if err != nil {
		return nil, fmt.Errorf("could not identify this boot: %w", err)
	}
	// Recorded before anything is stopped: a crash mid-shutdown must not forget a guest.
	if err = saveUpdateRestart(s, updateRestartRecord{BootID: boot, VMIDs: managedRunning}); err != nil {
		return nil, fmt.Errorf("could not record running guests: %w", err)
	}
	remaining := vmhost.ShutdownGuests(ctx, running, wait)
	if len(remaining) == 0 {
		return func(ctx context.Context) { restoreGuests(ctx, s, running, managedRunning) }, nil
	}
	restoreGuests(ctx, s, running, managedRunning)
	return nil, fmt.Errorf("%d guest(s) did not shut down in time", len(remaining))
}

// restoreGuests returns the guests an abandoned update reboot stopped to running. Unmanaged
// guests (the Controller appliance) start directly; managed pool guests start through the
// boot pass, so they take the same master admission as any start.
func restoreGuests(ctx context.Context, s *Store, stopped, managedIDs []string) {
	if err := saveUpdateRestart(s, updateRestartRecord{VMIDs: managedIDs}); err != nil {
		log.Print("update reboot abandoned; guest restore could not be recorded")
	}
	boot, _ := poolVMAutostartBootID()
	for _, id := range stopped {
		if slices.Contains(managedIDs, id) {
			// This boot's autostart claim is spent; clear it so the pass may try again.
			if dir, err := poolVMAutostartAttemptDir(s.dir); err == nil {
				_ = os.Remove(filepath.Join(dir, id+"-"+boot+".json"))
			}
			continue
		}
		if state, err := vmhost.ManagedVMState(ctx, id); err == nil && state == "shut off" {
			_ = vmhost.StartGuest(ctx, id)
		}
	}
	ReconcilePoolVMAutostart(ctx, s)
}

// finishUpdateRestart drops the record once the pass has dealt with every guest in it.
func finishUpdateRestart(s *Store) { _ = os.Remove(updateRestartPath(s)) }
