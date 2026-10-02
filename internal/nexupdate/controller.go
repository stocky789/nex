// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"errors"
	"time"

	"nex.local/nex/internal/vmhost"
)

// Appliance swaps the Controller appliance's system image on this host: the vmhost engine, whose
// journaled swap is resumable and restores the previous image and its data if the new one is not ready.
type Appliance interface {
	SwapAppliance(ctx context.Context, req vmhost.SwapRequest) (vmhost.Operation, error)
}

// controllerDeadline bounds the readiness probe of a swapped Controller (and of the restored one).
const controllerDeadline = 10 * time.Minute

// sideJob reports an operation kind that never moves the host's image lifecycle: a payload fetch and a
// Controller swap run beside a staged or armed release.
func sideJob(kind string) bool { return kind == "fetch" || kind == "controller" }

// controllerPipeline is apply with scope "controller": verify the release, stage its vm-image component
// under the payload directory (the digest in the signed manifest is the only integrity check), and hand it
// to the appliance swap under this operation's ID. Repeating the request_id after an interruption resumes
// the same journaled swap.
func (e *Engine) controllerPipeline(ctx context.Context, id string, req ApplyRequest) (string, error) {
	// A resumed swap goes on with the release it started with: the channel may have moved since, and the
	// vmhost journal of this request ID is keyed by that release's sequence and digest.
	e.mu.Lock()
	if req.Sequence == 0 {
		req.Sequence = e.recorded(id).Sequence
	}
	e.mu.Unlock()
	e.setPhase(id, PhaseChecking)
	src, channel, done, err := e.sourceFor(ctx, req)
	if err != nil {
		return "", err
	}
	defer done()
	v, err := e.fetchMetadataFrom(ctx, src, channel, req.Sequence)
	if err != nil {
		return "", err
	}
	m := v.manifest
	e.mu.Lock()
	e.updateOp(id, func(o *Operation) { o.Sequence = m.Sequence })
	e.mu.Unlock()
	var comp *Component
	for i, c := range m.Components {
		if c.Kind == "vm-image" {
			comp = &m.Components[i]
			break
		}
	}
	if comp == nil {
		return "", refuse(404, "no_controller_image", "the release has no Controller image")
	}
	path, err := e.stagePayload(ctx, id, src, comp)
	if err != nil {
		return "", err
	}
	e.setPhase(id, PhaseSwapping)
	_, err = e.cfg.Appliance.SwapAppliance(ctx, vmhost.SwapRequest{RequestID: id, Sequence: m.Sequence, SHA256: comp.SHA256, Source: path,
		Deadline: controllerDeadline, Probe: e.cfg.ControllerProbe})
	var swap *vmhost.SwapError
	switch {
	case err == nil:
		return PhaseCommitted, nil
	case errors.As(err, &swap):
		return "", refuse(409, "controller_"+swap.Code, swap.Message)
	}
	return "", err
}
