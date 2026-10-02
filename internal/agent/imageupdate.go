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
	var err error
	offline, hasOffline := engine.(OfflineUpdater)
	switch req.Action {
	case "settings", "media", "import", "fetch":
		if !hasOffline {
			result.Error = "unsupported_edition: this host does not run the image edition"
			return result
		}
	}
	action := nexupdate.ActionRequest{RequestID: req.RequestID, RunningVMs: req.RunningVMs}
	switch req.Action {
	case "check":
		_, err = engine.Check(ctx)
	case "apply":
		_, _, err = engine.Apply(ctx, nexupdate.ApplyRequest{RequestID: req.RequestID, Sequence: req.Sequence, Activate: req.Activate, RunningVMs: req.RunningVMs})
	case "activate":
		_, _, err = engine.Activate(ctx, action)
	case "rollback":
		_, _, err = engine.Rollback(ctx, action)
	case "cancel":
		_, _, err = engine.Cancel(ctx, action)
	case "settings":
		_, err = offline.UpdateSettings(ctx, nexupdate.Settings{Channel: req.Channel, BaseURL: req.BaseURL})
	case "media":
		// A rescan: the host records the list and reports it in Inventory.image.media.
		_, err = offline.Media(ctx)
	case "import":
		_, _, err = offline.Import(ctx, nexupdate.ImportRequest{RequestID: req.RequestID, Media: req.Media, Channel: req.Channel,
			Sequence: req.Sequence, Activate: req.Activate, RunningVMs: req.RunningVMs})
	case "fetch":
		_, _, err = offline.FetchComponent(ctx, req.Component, nexupdate.FetchRequest{RequestID: req.RequestID, Media: req.Media, Channel: req.Channel})
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
