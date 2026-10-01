// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
)

// fakeUpdater records the engine calls a relayed command makes.
type fakeUpdater struct {
	calls []string
	apply nexupdate.ApplyRequest
	acts  nexupdate.ActionRequest
	err   error
}

func (f *fakeUpdater) Status(context.Context) (nexupdate.ImageStatus, error) {
	return nexupdate.ImageStatus{}, f.err
}

func (f *fakeUpdater) Check(context.Context) (nexupdate.ImageStatus, error) {
	f.calls = append(f.calls, "check")
	return nexupdate.ImageStatus{}, f.err
}
func (f *fakeUpdater) Apply(_ context.Context, r nexupdate.ApplyRequest) (nexupdate.Operation, bool, error) {
	f.calls, f.apply = append(f.calls, "apply"), r
	return nexupdate.Operation{}, false, f.err
}
func (f *fakeUpdater) act(name string, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	f.calls, f.acts = append(f.calls, name), r
	return nexupdate.Operation{}, false, f.err
}
func (f *fakeUpdater) Activate(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	return f.act("activate", r)
}
func (f *fakeUpdater) Rollback(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	return f.act("rollback", r)
}
func (f *fakeUpdater) Cancel(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	return f.act("cancel", r)
}

func imageCommand(action string, mutate func(*protocol.ImageUpdateRequest)) protocol.Command {
	req := protocol.ImageUpdateRequest{HostID: "host-1", Action: action, RequestID: "req-" + action + "-0001"}
	if mutate != nil {
		mutate(&req)
	}
	return protocol.Command{OperationID: "op-" + action, Kind: protocol.ImageUpdateCommandKind, ImageUpdate: &req}
}

func TestRelayedImageUpdateRunsOnTheHostsEngine(t *testing.T) {
	engine := &fakeUpdater{}
	SetImageUpdater(engine)
	t.Cleanup(func() { SetImageUpdater(nil) })

	for _, action := range []string{"check", "apply", "activate", "rollback", "cancel"} {
		cmd := imageCommand(action, func(r *protocol.ImageUpdateRequest) {
			if action == "apply" {
				r.Sequence, r.Activate, r.RunningVMs = 7, "reboot", "shutdown"
			}
		})
		if result := ExecuteImageUpdate(context.Background(), cmd); result.Status != "succeeded" || result.OperationID != "op-"+action {
			t.Fatalf("%s: %+v", action, result)
		}
	}
	if strings.Join(engine.calls, ",") != "check,apply,activate,rollback,cancel" {
		t.Fatalf("engine calls %v", engine.calls)
	}
	// The engine's idempotency key is the operator's request_id, not the Controller's operation ID.
	if engine.apply != (nexupdate.ApplyRequest{RequestID: "req-apply-0001", Sequence: 7, Activate: "reboot", RunningVMs: "shutdown"}) || engine.acts.RequestID != "req-cancel-0001" {
		t.Fatalf("request not carried: %+v %+v", engine.apply, engine.acts)
	}

	// A refusal is a definite failed result with the engine's stable code and its one literal message.
	engine.err = &nexupdate.Error{Status: 409, Code: "update_in_progress", Message: "another update operation is running"}
	result := ExecuteImageUpdate(context.Background(), imageCommand("apply", nil))
	if result.Status != "failed" || result.Error != "update_in_progress: another update operation is running" {
		t.Fatalf("refusal: %+v", result)
	}
	engine.err = context.DeadlineExceeded
	if result = ExecuteImageUpdate(context.Background(), imageCommand("check", nil)); result.Status != "failed" || strings.Contains(result.Error, "deadline") {
		t.Fatalf("an unclassified failure leaked its text: %+v", result)
	}

	// A request the engine could misread never reaches it.
	before := len(engine.calls)
	for name, cmd := range map[string]protocol.Command{
		"no request":         {OperationID: "op-x", Kind: protocol.ImageUpdateCommandKind},
		"unknown action":     imageCommand("reboot-now", nil),
		"sequence on cancel": imageCommand("cancel", func(r *protocol.ImageUpdateRequest) { r.Sequence = 3 }),
		"bad request id":     imageCommand("check", func(r *protocol.ImageUpdateRequest) { r.RequestID = "../x" }),
	} {
		if result = ExecuteImageUpdate(context.Background(), cmd); result.Status != "failed" {
			t.Fatalf("%s was executed: %+v", name, result)
		}
	}
	if len(engine.calls) != before {
		t.Fatalf("an invalid command reached the engine: %v", engine.calls[before:])
	}

	// A host without an engine (classic) refuses with the stable code instead of pretending.
	SetImageUpdater(nil)
	if result = ExecuteImageUpdate(context.Background(), imageCommand("check", nil)); result.Status != "failed" || !strings.HasPrefix(result.Error, "unsupported_edition: ") {
		t.Fatalf("classic host: %+v", result)
	}
	if got := commandHost(imageCommand("check", nil)); got != "host-1" || !managedCommand(protocol.ImageUpdateCommandKind) {
		t.Fatalf("command host %q", got)
	}
}

// Inventory.image and the capability stay hidden until the Controller acknowledges image-update-v1; a
// Controller that does not know the field never decodes it.
func TestInventoryImageRequiresControllerAcknowledgement(t *testing.T) {
	SetImageUpdater(&fakeUpdater{})
	t.Cleanup(func() { SetImageUpdater(nil) })
	started := time.Now().UTC()
	probes := map[string]Probe{"image": func(context.Context) (any, error) {
		return protocol.ImageStatus{Booted: protocol.ImageRelease{Label: "2026.10.2", Sequence: 2, Slot: "a"}, Phase: "committed",
			Operation: &protocol.ImageOperation{ID: "req-apply-0001", Kind: "apply", Phase: "committed", StartedAt: started}}, nil
	}}
	monitor := NewMonitor(time.Second, probes)
	monitor.Tick(context.Background())
	time.Sleep(50 * time.Millisecond)
	if monitor.Snapshot().Image == nil {
		t.Fatal("the image probe did not produce an inventory (test setup)")
	}

	reports := 0
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body bytes.Buffer
		body.ReadFrom(r.Body)
		var request protocol.ReportRequest
		if err := json.Unmarshal(body.Bytes(), &request); err != nil {
			t.Error(err)
			return
		}
		if !contains(request.Capabilities, protocol.ImageUpdateCapability) {
			t.Error("image-update-v1 not offered by a host with an engine")
		}
		if got := request.Inventory.Image != nil; got != (reports == 1) {
			t.Errorf("report %d carried image = %v", reports, got)
		}
		reports++
		ack := reports == 1
		json.NewEncoder(w).Encode(protocol.ReportResponse{ImageUpdate: &ack})
	}))
	s := testStore(t)
	s.State = identity
	for i := range 3 {
		if err := report(context.Background(), s, monitor, RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}); err != nil {
			t.Fatal(err)
		}
		if s.State.ImageUpdateReady != (i == 0) {
			t.Fatalf("report %d: acknowledgement state %v", i, s.State.ImageUpdateReady)
		}
	}
	s.State.ImageUpdateReady = true
	if err := resetReportExtensions(s); err != nil || s.State.ImageUpdateReady {
		t.Fatal("acknowledgement survived a Controller change")
	}

	// Without an engine the capability is never offered, even to a Controller that would acknowledge it.
	SetImageUpdater(nil)
	if imageUpdateAvailable() {
		t.Fatal("capability offered without an engine")
	}
}
