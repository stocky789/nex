// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
)

// ImageUpdater is the image edition's update engine. The engine, not the Controller, runs every
// phase and keeps the record (/var/lib/nex/image/state.json); a command only asks for an action and
// the result only says whether the host accepted it. Progress, slots and rollback state come back in
// Inventory.image.
type ImageUpdater interface {
	Check(ctx context.Context) (nexupdate.ImageStatus, error)
	Apply(ctx context.Context, req nexupdate.ApplyRequest) (nexupdate.Operation, bool, error)
	Activate(ctx context.Context, req nexupdate.ActionRequest) (nexupdate.Operation, bool, error)
	Rollback(ctx context.Context, req nexupdate.ActionRequest) (nexupdate.Operation, bool, error)
	Cancel(ctx context.Context, req nexupdate.ActionRequest) (nexupdate.Operation, bool, error)
	Status(ctx context.Context) (nexupdate.ImageStatus, error)
}

// OfflineUpdater is the rest of the engine's host-scoped surface: the update source, offline bundles and
// on_demand payloads. The real engine implements both interfaces.
type OfflineUpdater interface {
	UpdateSettings(ctx context.Context, s nexupdate.Settings) (nexupdate.Settings, error)
	Media(ctx context.Context) ([]nexupdate.Media, error)
	Import(ctx context.Context, req nexupdate.ImportRequest) (nexupdate.Operation, bool, error)
	FetchComponent(ctx context.Context, id string, req nexupdate.FetchRequest) (nexupdate.Operation, bool, error)
}

var (
	imageUpdaterMu sync.RWMutex
	imageUpdater   ImageUpdater
)

// SetImageUpdater installs the host's update engine. nex-core-local calls it once, before the agent
// runs, on an image-edition host; a host without an engine never advertises image-update-v1.
func SetImageUpdater(u ImageUpdater) {
	imageUpdaterMu.Lock()
	imageUpdater = u
	imageUpdaterMu.Unlock()
}

func currentImageUpdater() ImageUpdater {
	imageUpdaterMu.RLock()
	defer imageUpdaterMu.RUnlock()
	return imageUpdater
}

func imageUpdateAvailable() bool { return currentImageUpdater() != nil }

// How long the worker waits for one relayed engine call. A check reads a handful of small metadata files; the
// other actions take the update lock and write records or boot entries. They are variables so tests need not
// wait them out.
var (
	imageCheckDeadline  = 2 * time.Minute
	imageActionDeadline = 5 * time.Minute
)

// ExecuteImageUpdate runs one relayed update action on this host's engine. Refusals are definite
// failed results carrying the engine's stable code and its one literal message.
func ExecuteImageUpdate(ctx context.Context, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	req := cmd.ImageUpdate
	if req == nil {
		result.Error = "image update command carries no request"
		return result
	}
	if err := req.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	engine := currentImageUpdater()
	if engine == nil {
		result.Error = "unsupported_edition: this host does not run the image edition"
		return result
	}
	offline, hasOffline := engine.(OfflineUpdater)
	switch req.Action {
	case "settings", "media", "import", "fetch":
		if !hasOffline {
			result.Error = "unsupported_edition: this host does not run the image edition"
			return result
		}
	}
	action := nexupdate.ActionRequest{RequestID: req.RequestID, RunningVMs: req.RunningVMs}
	// The managed worker runs one command at a time, so an engine call that never returns (an update
	// server that accepts the connection and stalls, a media mount that hangs) would hold every other
	// command of this host behind it, including the settings change that could fix the URL. The call
	// runs under a deadline and the worker stops waiting when it expires, whether or not the engine
	// honours its context.
	limit := imageActionDeadline
	if req.Action == "check" {
		limit = imageCheckDeadline
	}
	// Only read-only calls are cancelled when the worker stops waiting. A state-changing call may be
	// midway through writing boot entries or records; cancelling it would leave that half done while
	// the Controller is told it failed. Those run under the engine's own lifetime instead, and the
	// deadline only bounds how long the worker waits for them.
	cancellable := req.Action == "check" || req.Action == "media"
	callCtx, cancel := ctx, context.CancelFunc(func() {})
	if cancellable {
		callCtx, cancel = context.WithTimeout(ctx, limit)
	} else {
		callCtx = context.WithoutCancel(ctx)
	}
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		var err error
		switch req.Action {
		case "check":
			_, err = engine.Check(callCtx)
		case "apply":
			_, _, err = engine.Apply(callCtx, nexupdate.ApplyRequest{RequestID: req.RequestID, Sequence: req.Sequence, Activate: req.Activate, RunningVMs: req.RunningVMs})
		case "activate":
			_, _, err = engine.Activate(callCtx, action)
		case "rollback":
			_, _, err = engine.Rollback(callCtx, action)
		case "cancel":
			_, _, err = engine.Cancel(callCtx, action)
		case "settings":
			_, err = offline.UpdateSettings(callCtx, nexupdate.Settings{Channel: req.Channel, BaseURL: req.BaseURL})
		case "media":
			// A rescan: the host records the list and reports it in Inventory.image.media.
			_, err = offline.Media(callCtx)
		case "import":
			_, _, err = offline.Import(callCtx, nexupdate.ImportRequest{RequestID: req.RequestID, Media: req.Media, Channel: req.Channel,
				Sequence: req.Sequence, Activate: req.Activate, RunningVMs: req.RunningVMs})
		case "fetch":
			_, _, err = offline.FetchComponent(callCtx, req.Component, nexupdate.FetchRequest{RequestID: req.RequestID, Media: req.Media, Channel: req.Channel})
		}
		finished <- err
	}()
	var err error
	if cancellable {
		select {
		case err = <-finished:
		case <-callCtx.Done():
			// The engine may have answered at the same moment: its answer wins.
			select {
			case err = <-finished:
			default:
				if ctx.Err() == nil {
					result.Error = "internal: the update engine did not answer in time"
					return result
				}
				err = ctx.Err()
			}
		}
	} else {
		timer := time.NewTimer(limit)
		defer timer.Stop()
		select {
		case err = <-finished:
		case <-timer.C:
			select {
			case err = <-finished:
			default:
				return imageStillRunning(result)
			}
		case <-ctx.Done():
			select {
			case err = <-finished:
			default:
				return imageStillRunning(result)
			}
		}
	}
	if err != nil {
		var refusal *nexupdate.Error
		if errors.As(err, &refusal) {
			result.Error = fmt.Sprintf("%s: %s", refusal.Code, refusal.Message)
		} else {
			result.Error = "internal: the update engine failed"
		}
		return result
	}
	result.Status, result.ObservedAt = "succeeded", time.Now().UTC()
	return result
}

// imageStillRunning is the result for a state-changing action that outlived the worker's wait: the
// engine keeps going and its record (Inventory.image) is the truth, so this is not a failure.
func imageStillRunning(result protocol.Result) protocol.Result {
	result.Status, result.ObservedAt = "needs_attention", time.Now().UTC()
	result.Error = "still_running: the update engine is still working on this action; check the update status"
	return result
}
