// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"sync"
	"time"

	"nex.local/nex/internal/hostimage"
	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// Replacing a failed system disk is Core's work end to end. internal/hostimage lays the surviving disk's partition
// table on the replacement, copies the verified slots, writes the boot partition through the host's boot method and
// adds the nex-data partition to the mirror, keeping its own durable record. This file is the request side: it
// revalidates the operator's selection against a fresh observation of the host's disks, starts the work on a
// goroutine of its own so that it outlives the command, the Controller connection and a restart of the worker, and
// reports what the host recorded.

var (
	// systemDiskDiscover lists the host's disks with their identity, fingerprint and eligibility.
	systemDiskDiscover = vmhost.DiscoverZFSDevices
	// systemDiskHost is the host's disks and tools; tests give it a sandbox.
	systemDiskHost = func() *hostimage.Host {
		return &hostimage.Host{W: &hostimage.Writer{Runner: hostimage.ExecRunner{}}}
	}
	// systemDiskBooted is the root hash of the running system.
	systemDiskBooted = func(ctx context.Context) (string, error) {
		b, err := nexupdate.HostPlatform().Booted(ctx)
		return b.RootHash, err
	}
	// systemDiskWait is how long the worker waits for a replacement before it answers that the host is still working:
	// the replacement goes on and its record is the truth.
	systemDiskWait = 5 * time.Minute
)

func systemDiskJournal(dir string) string { return filepath.Join(dir, "system-disk", "replace.json") }

// systemDiskRun is the replacement running in this process.
type systemDiskRun struct {
	byID string
	done chan struct{}
	err  error // set before done closes
}

var systemDisks struct {
	sync.Mutex
	run     *systemDiskRun
	lastLog string
}

// ExecuteSystemDiskReplace starts or continues the replacement of this host's system disk and answers with its
// outcome when it finishes within systemDiskWait. Every refusal is a definite failed result carrying Core's own
// sentence. The record on the host is keyed on the request ID, so a redelivered command replays its outcome and a
// new request for the same disk continues an interrupted replacement; no destructive step is ever repeated.
func ExecuteSystemDiskReplace(ctx context.Context, dir string, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	request := cmd.SystemDiskReplace
	if request == nil {
		result.Error = "system disk replacement command carries no request"
		return result
	}
	if err := request.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	run, err := startSystemDiskReplace(ctx, dir, *request)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	timer := time.NewTimer(systemDiskWait)
	defer timer.Stop()
	select {
	case <-run.done:
	case <-timer.C:
		return systemDiskStillRunning(result)
	case <-ctx.Done():
		return systemDiskStillRunning(result)
	}
	if run.err != nil {
		result.Error = run.err.Error()
		return result
	}
	result.Status, result.ObservedAt = "succeeded", time.Now().UTC()
	return result
}

// systemDiskStillRunning is the result for a replacement that outlived the worker's wait: the host's record (its
// hardware report) is the truth, so this is not a failure.
func systemDiskStillRunning(result protocol.Result) protocol.Result {
	result.Status, result.ObservedAt = "needs_attention", time.Now().UTC()
	result.Error = "still_running: Nex Core is still replacing the system disk; its hardware report shows the progress"
	return result
}

func startSystemDiskReplace(ctx context.Context, dir string, req protocol.SystemDiskReplaceRequest) (*systemDiskRun, error) {
	systemDisks.Lock()
	defer systemDisks.Unlock()
	if run := systemDisks.run; run != nil {
		select {
		case <-run.done:
			systemDisks.run = nil
		default:
			if run.byID != req.Device.ByID {
				return nil, fmt.Errorf("a system disk replacement onto %s is still running", run.byID)
			}
			return run, nil
		}
	}
	journal := systemDiskJournal(dir)
	old, err := hostimage.ReadReplaceRecord(journal)
	if err != nil {
		return nil, errors.New("the system disk replacement record is unreadable")
	}
	if old != nil && old.RequestID == req.RequestID && old.Status != hostimage.ReplaceRunning {
		// A request the record already settled: its outcome is replayed, nothing is observed or written.
		run := &systemDiskRun{byID: old.ByID, done: make(chan struct{})}
		if old.Status == hostimage.ReplaceFailed {
			run.err = errors.New(old.Error)
		}
		close(run.done)
		return run, nil
	}
	// An unfinished replacement of a disk that has been pulled can never continue, and would keep every other
	// disk from being replaced: it is given up when its disk is no longer reported at all.
	if old != nil && old.Status == hostimage.ReplaceRunning && old.ByID != req.Device.ByID {
		if _, err := checkSystemDiskDevice(ctx, protocol.ZFSProvisionDevice{ByID: old.ByID, Serial: old.Serial, SizeBytes: old.SizeBytes}, false); errors.Is(err, errSystemDiskGone) {
			if err = hostimage.AbandonReplace(journal, "the replacement disk was removed before the replacement finished"); err != nil {
				return nil, err
			}
		}
	}
	// The fingerprint binds the whole observed disk and cannot match once an earlier attempt has laid it; the
	// identity that survives is checked instead, and the disk itself proves it is the one that attempt laid.
	resumed := old != nil && old.ByID == req.Device.ByID && old.Phase != hostimage.ReplaceAccepted
	target, err := checkSystemDiskDevice(ctx, req.Device, !resumed)
	if err != nil {
		return nil, err
	}
	return launchSystemDiskReplace(ctx, journal, hostimage.ReplaceRequest{
		RequestID: req.RequestID, ByID: req.Device.ByID, Serial: req.Device.Serial, SizeBytes: req.Device.SizeBytes,
		Target: target, Erase: req.Device.EraseConfirmation == req.Device.ByID,
	})
}

// launchSystemDiskReplace runs the replacement on a goroutine that does not depend on the command's context. The
// caller holds the lock.
func launchSystemDiskReplace(ctx context.Context, journal string, req hostimage.ReplaceRequest) (*systemDiskRun, error) {
	hash, err := systemDiskBooted(ctx)
	if err != nil {
		return nil, errors.New("Nex Core cannot tell which system image is running")
	}
	req.BootedRootHash = hash
	run := &systemDiskRun{byID: req.ByID, done: make(chan struct{})}
	systemDisks.run = run
	runCtx := context.WithoutCancel(ctx)
	go func() {
		defer close(run.done)
		run.err = systemDiskHost().ReplaceDisk(runCtx, journal, req)
	}()
	return run, nil
}

// ResumeSystemDiskReplace continues a replacement this process (or the one before it) started and did not finish:
// after a restart of Core nothing needs the Controller to ask again. A record that never got past its checks is
// left to the Controller's redelivered command, which still carries the operator's selection.
func ResumeSystemDiskReplace(ctx context.Context, dir string) {
	journal := systemDiskJournal(dir)
	rec, err := hostimage.ReadReplaceRecord(journal)
	if err != nil || rec == nil || rec.Status != hostimage.ReplaceRunning || rec.Phase == hostimage.ReplaceAccepted {
		return
	}
	systemDisks.Lock()
	defer systemDisks.Unlock()
	if run := systemDisks.run; run != nil {
		select {
		case <-run.done:
			systemDisks.run = nil
		default:
			return
		}
	}
	target, err := checkSystemDiskDevice(ctx, protocol.ZFSProvisionDevice{ByID: rec.ByID, Serial: rec.Serial, SizeBytes: rec.SizeBytes}, false)
	if err == nil {
		_, err = launchSystemDiskReplace(ctx, journal, hostimage.ReplaceRequest{
			RequestID: rec.RequestID, ByID: rec.ByID, Serial: rec.Serial, SizeBytes: rec.SizeBytes, Target: target, Erase: rec.Erase,
		})
	}
	if err != nil && err.Error() != systemDisks.lastLog {
		systemDisks.lastLog = err.Error()
		log.Printf("the unfinished system disk replacement cannot continue yet: %v", err)
	}
}

var errSystemDiskGone = errors.New("this host no longer reports the selected replacement disk")

// checkSystemDiskDevice repeats the disk discovery's own eligibility rules against a fresh observation, so a disk
// that became busy, changed or lost its typed erase confirmation since the operator picked it is refused rather than
// erased. It returns the disk's device node. With strict false only the identity is compared: that is what is left
// of a disk after an attempt has written to it.
func checkSystemDiskDevice(ctx context.Context, device protocol.ZFSProvisionDevice, strict bool) (string, error) {
	candidates, err := systemDiskDiscover(ctx)
	if err != nil {
		return "", errors.New("the replacement disk cannot be safely observed")
	}
	for _, candidate := range candidates {
		if candidate.ByID != device.ByID {
			continue
		}
		if candidate.Serial != device.Serial || candidate.SizeBytes != device.SizeBytes {
			return "", errors.New("the replacement disk changed since it was selected")
		}
		if strict {
			if !candidate.Eligible || candidate.Fingerprint != device.Fingerprint {
				return "", errors.New("the replacement disk changed since it was selected, or is in use")
			}
			if candidate.RequiresErase && device.EraseConfirmation != device.ByID {
				return "", errors.New("the replacement disk holds old data and was not confirmed for erasure")
			}
		}
		if candidate.Path == "" {
			return "", errors.New("the replacement disk has no device node")
		}
		return candidate.Path, nil
	}
	return "", errSystemDiskGone
}
