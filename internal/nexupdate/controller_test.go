// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/vmhost"
)

// fakeAppliance stands in for the vmhost engine: it records what the swap was asked and answers as told.
type fakeAppliance struct {
	mu       sync.Mutex
	requests []vmhost.SwapRequest
	err      error
	gate     chan struct{} // when set, a swap waits for it (or for its context) before answering
}

func (f *fakeAppliance) SwapAppliance(ctx context.Context, req vmhost.SwapRequest) (vmhost.Operation, error) {
	f.mu.Lock()
	f.requests = append(f.requests, req)
	gate, err := f.gate, f.err
	f.mu.Unlock()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return vmhost.Operation{}, ctx.Err()
		}
	}
	return vmhost.Operation{ID: req.RequestID, Kind: "swap", Status: "completed"}, err
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

// An interrupted swap is resumed by repeating its request_id; the vmhost journal of that ID is keyed by the
// release it started with, so a channel that moved in between must not change the release it goes on with.
func TestControllerSwapResumesWithTheSequenceItStartedWith(t *testing.T) {
	r, fake, _ := controllerRig(t)
	fake.gate = make(chan struct{})
	if _, _, err := r.applyController("req-ctl-0006"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		fake.mu.Lock()
		n := len(fake.requests)
		fake.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the swap never started")
		}
		time.Sleep(5 * time.Millisecond)
	}
	r.restart() // the interrupted swap: the first engine is closed while the appliance is mid-swap
	fake.mu.Lock()
	fake.gate = nil
	fake.mu.Unlock()
	r.pub.serial++
	r.pub.release(3, "v3", true) // the channel moves on
	if _, _, err := r.applyController("req-ctl-0006"); err != nil {
		t.Fatal(err)
	}
	if op := r.wait(); op.Error != nil || op.Phase != PhaseCommitted || op.Sequence != 2 {
		t.Fatalf("%+v", op)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.requests) != 2 || fake.requests[1].Sequence != 2 || fake.requests[1].SHA256 != fake.requests[0].SHA256 {
		t.Fatalf("the resumed swap asked for %+v after %+v", fake.requests[1:], fake.requests[:1])
	}
}
