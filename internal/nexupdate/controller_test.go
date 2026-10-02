// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"nex.local/nex/internal/vmhost"
)

// fakeAppliance stands in for the vmhost engine: it records what the swap was asked and answers as told.
type fakeAppliance struct {
	mu       sync.Mutex
	requests []vmhost.SwapRequest
	err      error
}

func (f *fakeAppliance) SwapAppliance(_ context.Context, req vmhost.SwapRequest) (vmhost.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, req)
	return vmhost.Operation{ID: req.RequestID, Kind: "swap", Status: "completed"}, f.err
}

func controllerRig(t *testing.T) (*rig, *fakeAppliance, Component) {
	t.Helper()
	r, comp, _ := payloadRig(t)
	fake := &fakeAppliance{}
	r.cfg.Appliance = fake
	r.cfg.ControllerProbe = func(context.Context, string) error { return nil }
	r.restart()
	r.pub.release(2, "v2", true)
	return r, fake, comp
}

func (r *rig) applyController(id string) (Operation, bool, error) {
	return r.apply(id, func(q *ApplyRequest) { q.Scope = "controller" })
}

func TestControllerScopeHandsTheVerifiedImageToTheSwapAndLeavesTheHostAlone(t *testing.T) {
	r, fake, comp := controllerRig(t)
	if _, _, err := r.applyController("req-ctl-0001"); err != nil {
		t.Fatal(err)
	}
	op := r.wait()
	if op.Kind != "controller" || op.Phase != PhaseCommitted || op.Error != nil || op.Sequence != 2 {
		t.Fatalf("%+v", op)
	}
	if len(fake.requests) != 1 {
		t.Fatalf("swap calls: %d", len(fake.requests))
	}
	got := fake.requests[0]
	if got.RequestID != "req-ctl-0001" || got.Sequence != 2 || got.SHA256 != comp.SHA256 || got.Deadline != controllerDeadline {
		t.Fatalf("swap request: %+v", got)
	}
	if want := filepath.Join(r.cfg.PayloadDir, comp.SHA256, "controller.qcow2"); got.Source != want {
		t.Fatalf("component staged at %s, want %s", got.Source, want)
	}
	if _, err := os.Stat(got.Source); err != nil {
		t.Fatal(err)
	}
	if st := r.status(); st.Phase != PhaseIdle || st.Failure != nil {
		t.Fatalf("a Controller swap must not move the host's image lifecycle: %+v", st)
	}
	if _, replayed, err := r.applyController("req-ctl-0001"); err != nil || !replayed || len(fake.requests) != 1 {
		t.Fatalf("replay: %v %v %d", replayed, err, len(fake.requests))
	}
}

func TestControllerScopeReportsTheSwapsRefusalCode(t *testing.T) {
	r, fake, _ := controllerRig(t)
	fake.err = &vmhost.SwapError{Code: vmhost.SwapRolledBack, Message: "the new Controller image did not become ready"}
	if _, _, err := r.applyController("req-ctl-0002"); err != nil {
		t.Fatal(err)
	}
	op := r.wait()
	if op.Error == nil || op.Error.Code != "controller_rolled_back" || op.Phase != "failed" {
		t.Fatalf("%+v", op)
	}
	if st := r.status(); st.Phase != PhaseIdle {
		t.Fatalf("host phase %s", st.Phase)
	}
}

func TestControllerScopeRefusals(t *testing.T) {
	// No appliance engine on this host.
	r := newRig(t)
	r.pub.release(2, "v2", true)
	_, _, err := r.applyController("req-ctl-0003")
	wantCode(t, err, "scope_unavailable")

	// A swap has no activation step.
	r, _, _ = controllerRig(t)
	_, _, err = r.apply("req-ctl-0004", func(q *ApplyRequest) { q.Scope, q.Activate = "controller", "reboot" })
	wantCode(t, err, "invalid_request")

	// A release without a Controller image.
	r = newRig(t)
	r.cfg.Appliance, r.cfg.ControllerProbe = &fakeAppliance{}, func(context.Context, string) error { return nil }
	r.restart()
	r.pub.release(2, "v2", true)
	if _, _, err = r.applyController("req-ctl-0005"); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error == nil || op.Error.Code != "no_controller_image" {
		t.Fatalf("%+v", op)
	}
}
