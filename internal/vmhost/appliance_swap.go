// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// The Controller appliance is updated by replacing its read-only system image. The Controller's own
// state lives on controller-data.qcow2 and is never rewritten by a swap; it is checkpointed first so a
// failed boot of the new image restores exactly what the old image last saw.
//
// One journal under the caller's operation ID records the next step to run. Every step observes the
// real domain and files first, so a crash at any point resumes by repeating the request: nothing is
// replayed blindly and nothing is ever forced off.

// Swap stages, in the order they run. The journal's Stage is the step still to do.
const (
	swapStage      = "swap_stage"       // verify the component and place it beside the VM
	swapShutdown   = "swap_shutdown"    // graceful guest-agent shutdown
	swapCheckpoint = "swap_checkpoint"  // reflink checkpoint of the data disk
	swapDefine     = "swap_define"      // redefine the domain with the new system disk
	swapStart      = "swap_start"       // start it
	swapProbe      = "swap_probe"       // wait for readiness
	swapCommit     = "swap_commit"      // record the new image, prune older ones
	swapRestore    = "swap_restore"     // failure: put the data disk back
	swapRedefine   = "swap_redefine"    // failure: redefine the old system disk
	swapRestart    = "swap_restart"     // failure: start the old image
	swapReprobe    = "swap_reprobe"     // failure: wait for readiness of the old image
	swapCommitted  = "swap_committed"   // terminal, Status completed
	swapRolledBack = "swap_rolled_back" // terminal, Status rolled_back
)

// Codes of a SwapError; nexupdate publishes them as controller_<code>.
const (
	SwapNoAppliance    = "no_appliance"
	SwapNotProfile     = "not_profile"
	SwapBusy           = "swap_in_progress"
	SwapDigestMismatch = "digest_mismatch"
	SwapShutdownFailed = "shutdown_failed"
	SwapRolledBack     = "rolled_back"
	SwapNeedsAttention = "needs_attention"
)

// SwapError is a refusal or failure with a stable code and one literal message.
type SwapError struct{ Code, Message string }

func (e *SwapError) Error() string { return e.Message }

// SwapRequest asks for the Controller's system image to be replaced by a staged release component.
type SwapRequest struct {
	// RequestID is the operation ID; repeating it resumes or replays the same swap.
	RequestID string
	Sequence  int
	// SHA256 is the component's digest from the verified release manifest; Source is the staged file
	// under the payload directory (<payload>/<sha256>/<name>).
	SHA256 string
	Source string
	// Deadline bounds each readiness probe (controller_deadline); zero is 10 minutes.
	Deadline time.Duration
	// Probe checks the Controller's readiness path at its management URL (the pinned-TLS request
	// deployment uses). It returns nil when the Controller answers.
	Probe func(ctx context.Context, url string) error
}

// swapState is the swap's own durable record inside its journal.
type swapState struct {
	Sequence   int    `json:"sequence"`
	SHA256     string `json:"sha256"`
	Old        string `json:"old"`
	New        string `json:"new"`
	Checkpoint string `json:"checkpoint"`
	// Started records that the new image booted: only then can the data disk have changed.
	Started bool   `json:"started,omitempty"`
	Cause   string `json:"cause,omitempty"`
}

const (
	defaultControllerDeadline = 10 * time.Minute
	defaultShutdownWait       = 10 * time.Minute
)

func (e *Engine) swapStep(j journal, stage string) (journal, error) {
	j.Stage = stage
	if err := e.save(j); err != nil {
		return j, err
	}
	if e.swapHook != nil {
		return j, e.swapHook(stage)
	}
	return j, nil
}

func (e *Engine) pollEvery() time.Duration {
	if e.swapPoll > 0 {
		return e.swapPoll
	}
	return 2 * time.Second
}

// SwapAppliance runs, resumes or replays one swap. It returns when the swap reached a terminal state
// (the returned error is then the failure: a *SwapError) or when it was interrupted, in which case the
// journal stays running and repeating the request continues it.
func (e *Engine) SwapAppliance(ctx context.Context, req SwapRequest) (Operation, error) {
	if !identifier.MatchString(req.RequestID) || !layerHex.MatchString(req.SHA256) || req.Sequence < 1 || req.Probe == nil {
		return Operation{}, errors.New("invalid swap request")
	}
	lock, err := e.lock(ctx)
	if err != nil {
		return Operation{}, err
	}
	defer unlock(lock)
	digest := fmt.Sprintf("swap:%d:%s", req.Sequence, req.SHA256)
	js, err := e.journals()
	if err != nil {
		return Operation{}, err
	}
	var dep, cur *journal
	for i := range js {
		if js[i].Kind == "deploy" {
			dep = &js[i]
		}
		if js[i].ID == req.RequestID {
			cur = &js[i]
		} else if js[i].Kind == "swap" && js[i].Status == "running" {
			return Operation{}, &SwapError{SwapBusy, "another Controller swap is unfinished; repeat its request_id to resume it"}
		}
	}
	if cur != nil {
		if cur.Kind != "swap" || cur.Digest != digest {
			return Operation{}, errors.New("request ID already used with different input")
		}
		if cur.Status != "running" {
			return cur.Operation, terminalError(*cur)
		}
		return e.swapRun(ctx, *cur, req)
	}
	if dep == nil {
		return Operation{}, &SwapError{SwapNoAppliance, "this host has no Controller appliance"}
	}
	if dep.System == "" {
		return Operation{}, &SwapError{SwapNotProfile, "the Controller appliance was deployed from a single-disk image and cannot be swapped; redeploy it"}
	}
	if dep.System == swapImagePath(*dep, req.SHA256) {
		// Already the running image (an earlier request, or the image it was deployed with): nothing to do,
		// and no second checkpoint.
		return Operation{ID: req.RequestID, VMID: dep.VMID, Kind: "swap", Status: "completed", Stage: swapCommitted, Progress: 100}, nil
	}
	if err = e.ownedDomain(ctx, *dep); err != nil {
		return Operation{}, err
	}
	// The data disk is replaced as a whole file at restore, so no other domain, running or defined, may
	// reference it, and no hardlink may alias it.
	if err = e.exclusiveDisk(ctx, *dep); err != nil {
		return Operation{}, err
	}
	if err = e.powerState(ctx, dep.VMID); err != nil {
		return Operation{}, err
	}
	next := swapImagePath(*dep, req.SHA256)
	j := *dep
	j.Operation = Operation{ID: req.RequestID, VMID: dep.VMID, Kind: "swap", Status: "running", Stage: swapStage, Progress: 5}
	j.Digest = digest
	j.Swap = &swapState{Sequence: req.Sequence, SHA256: req.SHA256, Old: dep.System, New: next, Checkpoint: dep.Disk + ".pre-" + strconv.Itoa(req.Sequence)}
	j.PreviousSystem = dep.PreviousSystem
	if j, err = e.swapStep(j, swapStage); err != nil {
		return j.Operation, err
	}
	return e.swapRun(ctx, j, req)
}

func swapImagePath(j journal, sha string) string {
	return filepath.Join(filepath.Dir(j.Disk), "system-"+sha+".img")
}

func terminalError(j journal) error {
	switch j.Status {
	case "rolled_back":
		return &SwapError{SwapRolledBack, "the new Controller image did not become ready; the previous image and its data were restored: " + j.Error}
	case "needs_attention":
		return &SwapError{SwapNeedsAttention, "the Controller could not be restored automatically; it was not forced off and needs an operator: " + j.Error}
	case "failed":
		return &SwapError{SwapShutdownFailed, j.Error}
	}
	return nil
}

// swapRun executes the remaining steps of j. A step that must be undone moves the journal to the
// restore steps; an interruption (context, hook) returns without touching the journal.
func (e *Engine) swapRun(ctx context.Context, j journal, req SwapRequest) (Operation, error) {
	s := j.Swap
	deadline := req.Deadline
	if deadline <= 0 {
		deadline = defaultControllerDeadline
	}
	// fail moves a forward step's failure to the restore path.
	fail := func(cause error) (journal, error) {
		if s.Cause == "" {
			s.Cause = cause.Error()
		}
		j.Error = s.Cause
		return e.swapStep(j, swapRestore)
	}
	var err error
	for {
		if err = ctx.Err(); err != nil {
			return j.Operation, err
		}
		switch j.Stage {
		case swapStage:
			// Nothing has changed yet, so a checkpoint on disk is a leftover of an earlier attempt: never reuse it.
			if err = os.Remove(s.Checkpoint); err != nil && !errors.Is(err, os.ErrNotExist) {
				return j.Operation, err
			}
			if err = e.swapStageImage(j, req); err != nil {
				j.Status, j.Error = "failed", err.Error()
				return j.Operation, e.finishSwap(j, err)
			}
			j.Progress = 15
			j, err = e.swapStep(j, swapShutdown)
		case swapShutdown:
			if err = e.swapShutdown(ctx, j.VMID); err != nil {
				if ctx.Err() != nil {
					return j.Operation, ctx.Err()
				}
				// Nothing was changed and the guest was never forced: the Controller keeps running its image.
				j.Status, j.Error = "failed", err.Error()
				return j.Operation, e.finishSwap(j, err)
			}
			j.Progress = 30
			j, err = e.swapStep(j, swapCheckpoint)
		case swapCheckpoint:
			if err = e.requireOff(ctx, j.VMID); err == nil {
				if err = e.exclusiveDisk(ctx, j); err == nil {
					err = reflinkCopy(ctx, j.Disk, s.Checkpoint)
				}
			}
			if err != nil {
				j, err = fail(err)
				break
			}
			j.Progress = 45
			j, err = e.swapStep(j, swapDefine)
		case swapDefine:
			if err = e.requireOff(ctx, j.VMID); err == nil {
				err = e.redefineSystem(ctx, j, s.Old, s.New)
			}
			if err != nil {
				j, err = fail(err)
				break
			}
			j.Progress = 55
			j, err = e.swapStep(j, swapStart)
		case swapStart:
			s.Started = true
			if err = e.save(j); err == nil {
				err = e.startDomain(ctx, j.VMID)
			}
			if err != nil {
				j, err = fail(err)
				break
			}
			j.Progress = 65
			j, err = e.swapStep(j, swapProbe)
		case swapProbe:
			if err = e.swapProbe(ctx, &j, req, s.New, deadline); err != nil {
				if ctx.Err() != nil {
					return j.Operation, ctx.Err()
				}
				j, err = fail(fmt.Errorf("the new Controller image was not ready within %s: %w", deadline, err))
				break
			}
			j.Progress = 90
			j, err = e.swapStep(j, swapCommit)
		case swapCommit:
			if err = e.commitSwap(j); err != nil {
				return j.Operation, err
			}
			j.Status, j.Stage, j.Progress, j.Error = "completed", swapCommitted, 100, ""
			return j.Operation, e.save(j)
		case swapRestore:
			// The failed image may still be running on the data disk: stop it gracefully first. A guest
			// that will not stop is never forced; the swap then needs an operator.
			if err = e.swapShutdown(ctx, j.VMID); err != nil {
				if ctx.Err() != nil {
					return j.Operation, ctx.Err()
				}
				return e.needsAttention(ctx, j, err)
			}
			// Only a started new image could have changed the data disk.
			if s.Started {
				if err = e.exclusiveDisk(ctx, j); err == nil {
					err = restoreCopy(ctx, s.Checkpoint, j.Disk)
				}
				if err != nil {
					return e.needsAttention(ctx, j, err)
				}
			}
			j, err = e.swapStep(j, swapRedefine)
		case swapRedefine:
			if err = e.requireOff(ctx, j.VMID); err == nil {
				err = e.redefineSystem(ctx, j, s.New, s.Old)
			}
			if err != nil {
				return e.needsAttention(ctx, j, err)
			}
			j, err = e.swapStep(j, swapRestart)
		case swapRestart:
			if err = e.startDomain(ctx, j.VMID); err != nil {
				return e.needsAttention(ctx, j, err)
			}
			j, err = e.swapStep(j, swapReprobe)
		case swapReprobe:
			if err = e.swapProbe(ctx, &j, req, s.Old, deadline); err != nil {
				if ctx.Err() != nil {
					return j.Operation, ctx.Err()
				}
				return e.needsAttention(ctx, j, err)
			}
			j.Status, j.Stage = "rolled_back", swapRolledBack
			j.Progress = 100
			if err = e.save(j); err != nil {
				return j.Operation, err
			}
			return j.Operation, terminalError(j)
		default:
			return j.Operation, errors.New("unknown swap stage " + j.Stage)
		}
		if err != nil {
			return j.Operation, err
		}
	}
}

// finishSwap records a terminal failure that changed nothing.
func (e *Engine) finishSwap(j journal, cause error) error {
	if err := e.save(j); err != nil {
		return err
	}
	if _, ok := cause.(*SwapError); ok {
		return cause
	}
	return terminalError(j)
}

// needsAttention is the last resort: the Controller could not be brought back, so it is left stopped
// (asked to shut down gracefully, never forced) and the operator recovers it with host-recovery.
func (e *Engine) needsAttention(ctx context.Context, j journal, cause error) (Operation, error) {
	if state, err := e.virsh(ctx, "domstate", j.VMID); err == nil && strings.TrimSpace(string(state)) == "running" {
		_, _ = e.virsh(ctx, "shutdown", j.VMID, "--mode", "agent")
	}
	j.Status = "needs_attention"
	j.Error = j.Swap.Cause + "; restore failed: " + cause.Error()
	if err := e.save(j); err != nil {
		return j.Operation, err
	}
	return j.Operation, terminalError(j)
}

// swapStageImage verifies the component against the manifest digest and places it beside the VM,
// read-only. An image already in place with the right digest is kept.
func (e *Engine) swapStageImage(j journal, req SwapRequest) error {
	s := j.Swap
	if sum, _, err := fileSHA256(s.New); err == nil && sum == s.SHA256 {
		return nil
	}
	sum, _, err := fileSHA256(req.Source)
	if err != nil {
		return &SwapError{SwapDigestMismatch, "the Controller image is not staged; fetch the component first"}
	}
	if sum != s.SHA256 {
		return &SwapError{SwapDigestMismatch, "the staged Controller image does not match the digest in the signed release manifest"}
	}
	tmp := s.New + ".part"
	if err = reflinkCopyFile(req.Source, tmp); err != nil {
		return err
	}
	if sum, _, err = fileSHA256(tmp); err != nil || sum != s.SHA256 {
		_ = os.Remove(tmp)
		return &SwapError{SwapDigestMismatch, "the Controller image changed while it was copied"}
	}
	if err = os.Chmod(tmp, 0o444); err != nil {
		return err
	}
	return os.Rename(tmp, s.New)
}

// swapShutdown asks the guest agent for a clean shutdown and waits; it never destroys the domain.
func (e *Engine) swapShutdown(ctx context.Context, id string) error {
	state, err := e.domState(ctx, id)
	if err != nil || state == "shut off" {
		return err
	}
	if state != "running" {
		return fmt.Errorf("the Controller VM is %s; it must be running or shut off", state)
	}
	if _, err = e.virsh(ctx, "shutdown", id, "--mode", "agent"); err != nil {
		return errors.New("the Controller guest agent did not accept a graceful shutdown; nothing was changed: " + err.Error())
	}
	wait := e.shutdownWait
	if wait <= 0 {
		wait = defaultShutdownWait
	}
	timeout := time.After(wait)
	for {
		if state, err = e.domState(ctx, id); err != nil {
			return err
		}
		if state == "shut off" {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return errors.New("the Controller did not shut down gracefully in time; it was not forced off and nothing was changed")
		case <-time.After(e.pollEvery()):
		}
	}
}

func (e *Engine) domState(ctx context.Context, id string) (string, error) {
	b, err := e.virsh(ctx, "domstate", id)
	return strings.TrimSpace(string(b)), err
}

func (e *Engine) requireOff(ctx context.Context, id string) error {
	state, err := e.domState(ctx, id)
	if err != nil {
		return err
	}
	if state != "shut off" {
		return errors.New("the Controller VM is not shut off")
	}
	return nil
}

func (e *Engine) startDomain(ctx context.Context, id string) error {
	state, err := e.domState(ctx, id)
	if err != nil || state == "running" {
		return err
	}
	_, err = e.virsh(ctx, "start", id)
	return err
}

// redefineSystem swaps the system disk's path in the persistent definition, keeping every other
// element (UUID, MAC, firmware state, devices). Repeating it after the swap is a no-op.
func (e *Engine) redefineSystem(ctx context.Context, j journal, from, to string) error {
	b, err := e.virsh(ctx, "dumpxml", j.VMID, "--inactive")
	if err != nil {
		return err
	}
	text := string(b)
	if strings.Contains(text, to) && !strings.Contains(text, from) {
		return e.checkSystem(ctx, j, to)
	}
	if strings.Count(text, from) != 1 {
		return errors.New("the domain does not reference the expected system image exactly once")
	}
	path := filepath.Join(filepath.Dir(j.Disk), "domain-"+filepath.Base(to)+".xml")
	if err = atomicWrite(path, []byte(strings.Replace(text, from, to, 1))); err != nil {
		return err
	}
	if _, err = e.virsh(ctx, "define", path); err != nil {
		return err
	}
	return e.checkSystem(ctx, j, to)
}

// checkSystem confirms the persistent definition is the owned layout with the given system image.
func (e *Engine) checkSystem(ctx context.Context, j journal, system string) error {
	b, err := e.virsh(ctx, "dumpxml", j.VMID, "--inactive")
	if err != nil {
		return err
	}
	j.System = system
	return checkDomain(b, j)
}

// swapProbe waits until the Controller of the image at system answers. The running domain must be the
// owned one booted from exactly that image, so a Controller still serving the other image, or a domain
// that never picked up the new definition, is not ready however well it answers.
func (e *Engine) swapProbe(ctx context.Context, j *journal, req SwapRequest, system string, deadline time.Duration) error {
	wait, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	var last error
	for {
		dep, err := e.deployment(j.VMID)
		if err != nil {
			return err
		}
		url := dep.ControllerURL
		if found, err := e.discoverURL(wait, dep); err == nil {
			url = found
		}
		if url == "" {
			last = errors.New("the Controller has no management address yet")
		} else if last = e.runningSystem(wait, *j, system); last == nil {
			last = req.Probe(wait, url)
		}
		if last == nil {
			return nil
		}
		select {
		case <-wait.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return last
		case <-time.After(e.pollEvery()):
		}
	}
}

// commitSwap makes the new image the deployment's and prunes everything older than the previous one.
// It is idempotent: a crash after the deployment was rewritten repeats harmlessly.
func (e *Engine) commitSwap(j journal) error {
	s := j.Swap
	deploys, err := e.journals()
	if err != nil {
		return err
	}
	for _, d := range deploys {
		if d.Kind != "deploy" || d.VMID != j.VMID {
			continue
		}
		d.System, d.PreviousSystem = s.New, s.Old
		d.Swap = &swapState{Sequence: s.Sequence, SHA256: s.SHA256, New: s.New, Old: s.Old, Checkpoint: s.Checkpoint}
		if err = e.save(d); err != nil {
			return err
		}
	}
	dir := filepath.Dir(j.Disk)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		old := (strings.HasPrefix(entry.Name(), "system-") && strings.HasSuffix(entry.Name(), ".img") && path != s.New && path != s.Old) ||
			(strings.HasPrefix(entry.Name(), filepath.Base(j.Disk)+".pre-") && path != s.Checkpoint) ||
			(strings.HasPrefix(entry.Name(), "domain-system-") && entry.Name() != "domain-"+filepath.Base(s.New)+".xml" && entry.Name() != "domain-"+filepath.Base(s.Old)+".xml")
		if old {
			if err = os.Remove(path); err != nil {
				return err
			}
		}
	}
	return nil
}

// runningSystem confirms the running definition of the domain is the owned layout booted from system.
func (e *Engine) runningSystem(ctx context.Context, j journal, system string) error {
	b, err := e.virsh(ctx, "dumpxml", j.VMID)
	if err != nil {
		return err
	}
	j.System = system
	if err = checkDomain(b, j); err != nil {
		return errors.New("the running Controller VM is not booted from the expected system image: " + err.Error())
	}
	return nil
}

// reflinkCopy writes a complete, durable copy of src at dst atomically, sharing extents where the
// filesystem can. The copy is read back against src before it is renamed into place.
func reflinkCopy(ctx context.Context, src, dst string) error {
	if sameContent(src, dst) == nil {
		return nil // an earlier run of this step finished it: the VM was off the whole time
	}
	return copyVerified(ctx, src, dst)
}

// restoreCopy puts a checkpoint back over the live data disk through a temporary file, so the disk is
// always either the failed boot's or the checkpoint, never half of each. The caller has just confirmed
// no other domain references the disk, so replacing the file cannot split it from a second user.
func restoreCopy(ctx context.Context, checkpoint, disk string) error {
	if _, err := os.Stat(checkpoint); err != nil {
		return fmt.Errorf("the data checkpoint is missing: %w", err)
	}
	return copyVerified(ctx, checkpoint, disk)
}

// copyVerified copies src to dst through dst+".part": fsynced and compared with src before the rename,
// then the directory is fsynced, so a crash leaves dst either absent/old or complete.
func copyVerified(ctx context.Context, src, dst string) error {
	tmp := dst + ".part"
	_ = os.Remove(tmp)
	if _, err := run(ctx, "cp", "--reflink=auto", "--sparse=always", "--", src, tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	err := syncFile(tmp)
	if err == nil {
		err = sameContent(src, tmp)
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncFile(filepath.Dir(dst))
}

func syncFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// sameContent reports an error unless both files exist and hold the same bytes.
func sameContent(a, b string) error {
	sumA, _, err := fileSHA256(a)
	if err != nil {
		return err
	}
	sumB, _, err := fileSHA256(b)
	if err != nil {
		return err
	}
	if sumA != sumB {
		return errors.New("a copy of the Controller data disk differs from its source")
	}
	return nil
}

func reflinkCopyFile(src, dst string) error {
	_ = os.Remove(dst)
	_, err := run(context.Background(), "cp", "--reflink=auto", "--", src, dst)
	return err
}

// HasControllerProfile reports whether this host runs a Controller appliance in the two-disk profile,
// the only form whose system image a swap can replace. It reads the deployment journal under stateDir.
// A journal that cannot be read is an error, not "no appliance": the caller would otherwise skip the
// Controller. A host that never deployed one has no state directory yet and answers false.
func HasControllerProfile(stateDir string) (bool, error) {
	e := &Engine{config: Config{StateDir: stateDir}}
	js, err := e.journals()
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, j := range js {
		if j.Kind == "deploy" && j.System != "" {
			return true, nil
		}
	}
	return false, nil
}
