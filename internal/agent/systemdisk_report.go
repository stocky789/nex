// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"path/filepath"

	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/protocol"
)

// systemDiskReport is what this host says about its system disk when its hardware is asked for: the disk the
// running system started from and Core's record of the latest replacement, with the kernel's own progress of the
// nex-data mirror onto the replacement. Nothing here is kept apart from the record hostimage writes, so a fresh
// Controller sees an operation another one started.
func systemDiskReport(dir string, md protocol.HardwareMDArrays) *protocol.HardwareSystemDisk {
	report := &protocol.HardwareSystemDisk{}
	if disk, err := systemDiskHost().RunningDisk(); err == nil {
		report.BootedDisk = disk
	}
	if rec, err := hostimage.ReadReplaceRecord(systemDiskJournal(dir)); err == nil && rec != nil {
		r := &protocol.SystemDiskReplacement{RequestID: rec.RequestID, ByID: rec.ByID, Serial: rec.Serial, SizeBytes: rec.SizeBytes,
			Phase: rec.Phase, Status: rec.Status, Error: rec.Error, StartedAt: rec.StartedAt, FinishedAt: rec.FinishedAt}
		r.Resync = systemDiskResync(rec.ByID, md)
		report.Replacement = r
	}
	if report.BootedDisk == "" && report.Replacement == nil {
		return nil
	}
	return report
}

// systemDiskResolve names a device node by its stable path; tests replace it.
var systemDiskResolve = filepath.EvalSymlinks

// systemDiskResync finds the replacement disk's partition among the members of the nex-data array.
func systemDiskResync(byID string, md protocol.HardwareMDArrays) *protocol.SystemDiskResync {
	disk, err := systemDiskResolve(byID)
	if err != nil {
		return nil
	}
	member := filepath.Base(hostimage.NodeName(disk, hostimage.DataIndex))
	for _, array := range md.Arrays {
		for _, m := range array.Members {
			if m.Name != member {
				continue
			}
			return &protocol.SystemDiskResync{Array: array.Name, Member: m.Name, State: m.State, Action: array.SyncAction,
				Percent: array.SyncPercent, SpeedKBs: array.SyncSpeedKBs, Degraded: array.Degraded}
		}
	}
	return nil
}
