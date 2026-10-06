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

	settings  nexupdate.Settings
	imported  nexupdate.ImportRequest
	fetched   nexupdate.FetchRequest
	component string
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
func (f *fakeUpdater) UpdateSettings(_ context.Context, s nexupdate.Settings) (nexupdate.Settings, error) {
	f.calls, f.settings = append(f.calls, "settings"), s
	return s, f.err
}
func (f *fakeUpdater) Media(context.Context) ([]nexupdate.Media, error) {
	f.calls = append(f.calls, "media")
	return nil, f.err
}
func (f *fakeUpdater) Import(_ context.Context, r nexupdate.ImportRequest) (nexupdate.Operation, bool, error) {
	f.calls, f.imported = append(f.calls, "import"), r
	return nexupdate.Operation{}, false, f.err
}
func (f *fakeUpdater) FetchComponent(_ context.Context, id string, r nexupdate.FetchRequest) (nexupdate.Operation, bool, error) {
	f.calls, f.fetched, f.component = append(f.calls, "fetch"), r, id
	return nexupdate.Operation{}, false, f.err
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

	// A host without an engine refuses with the stable code instead of pretending.
	SetImageUpdater(nil)
	if result = ExecuteImageUpdate(context.Background(), imageCommand("check", nil)); result.Status != "failed" || !strings.HasPrefix(result.Error, "no_image_update_engine: ") {
		t.Fatalf("host without an engine: %+v", result)
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

func TestRelayedSettingsMediaImportAndFetchRunOnTheHostsEngine(t *testing.T) {
	engine := &fakeUpdater{}
	SetImageUpdater(engine)
	t.Cleanup(func() { SetImageUpdater(nil) })
	run := func(action string, mutate func(*protocol.ImageUpdateRequest)) protocol.Result {
		return ExecuteImageUpdate(context.Background(), imageCommand(action, mutate))
	}
	for action, mutate := range map[string]func(*protocol.ImageUpdateRequest){
		"settings": func(r *protocol.ImageUpdateRequest) { r.Channel, r.BaseURL = "dev", "https://updates.example.test" },
		"media":    nil,
		"import": func(r *protocol.ImageUpdateRequest) {
			r.Media, r.Channel, r.Sequence, r.Activate = "inbox/spring", "stable", 4, "reboot"
		},
		"fetch": func(r *protocol.ImageUpdateRequest) { r.Component, r.Media = "controller", "label" },
	} {
		if result := run(action, mutate); result.Status != "succeeded" {
			t.Fatalf("%s: %+v", action, result)
		}
	}
	if engine.settings != (nexupdate.Settings{Channel: "dev", BaseURL: "https://updates.example.test"}) ||
		engine.imported != (nexupdate.ImportRequest{RequestID: "req-import-0001", Media: "inbox/spring", Channel: "stable", Sequence: 4, Activate: "reboot"}) ||
		engine.fetched != (nexupdate.FetchRequest{RequestID: "req-fetch-0001", Media: "label"}) || engine.component != "controller" {
		t.Fatalf("request not carried: %+v %+v %+v %q", engine.settings, engine.imported, engine.fetched, engine.component)
	}
	engine.err = &nexupdate.Error{Status: 400, Code: "invalid_base_url", Message: "base_url must be an http or https URL without credentials, query or fragment"}
	if result := run("settings", func(r *protocol.ImageUpdateRequest) { r.Channel = "dev" }); result.Status != "failed" || !strings.HasPrefix(result.Error, "invalid_base_url: ") {
		t.Fatalf("%+v", result)
	}
	engine.err = nil
	before := len(engine.calls)
	for name, mutate := range map[string]func(*protocol.ImageUpdateRequest){
		"settings without a channel": func(r *protocol.ImageUpdateRequest) { r.Action = "settings"; r.Channel = "" },
		"import without media":       func(r *protocol.ImageUpdateRequest) { r.Action = "import"; r.Media = "" },
		"fetch without a component":  func(r *protocol.ImageUpdateRequest) { r.Action = "fetch"; r.Component = "" },
		"base_url on apply":          func(r *protocol.ImageUpdateRequest) { r.Action = "apply"; r.BaseURL = "https://updates.example.test" },
		"sequence on settings":       func(r *protocol.ImageUpdateRequest) { r.Action = "settings"; r.Channel = "dev"; r.Sequence = 2 },
		"running_vms on fetch":       func(r *protocol.ImageUpdateRequest) { r.Action = "fetch"; r.Component = "c"; r.RunningVMs = "shutdown" },
	} {
		if result := run("check", mutate); result.Status != "failed" {
			t.Fatalf("%s was executed: %+v", name, result)
		}
	}
	if len(engine.calls) != before {
		t.Fatalf("a malformed request reached the engine: %v", engine.calls[before:])
	}
}

func TestInventoryImageReportsTheSourceAndMedia(t *testing.T) {
	st := nexupdate.ImageStatus{BaseURL: "https://updates.example.test", Channel: "dev", Phase: "idle", Media: []nexupdate.Media{
		{ID: "inbox/spring", Source: "inbox", Mounted: true, Checksums: true, Channels: []nexupdate.MediaChannel{{Name: "stable", Sequence: 4}}}}}
	got := imageInventory(st, nexupdate.Booted{Label: "x", Sequence: 1}, nil)
	if got.BaseURL != "https://updates.example.test" || len(got.Media) != 1 || got.Media[0].ID != "inbox/spring" ||
		got.Media[0].Channels[0].Sequence != 4 || !got.Media[0].Checksums {
		t.Fatalf("%+v", got)
	}
}

// stalledUpdater never answers a check until released, the way an update server that accepts the
// connection and then says nothing does; it ignores its context on purpose.
type stalledUpdater struct {
	fakeUpdater
	release chan struct{}
}

func (s *stalledUpdater) Check(context.Context) (nexupdate.ImageStatus, error) {
	<-s.release
	return nexupdate.ImageStatus{}, nil
}

// One stalled engine call must not hold the managed worker, which runs every command of the host one at a
// time: the worker gets a definite failed result at the deadline and goes on.
func TestRelayedImageUpdateGivesUpOnAStalledEngine(t *testing.T) {
	engine := &stalledUpdater{release: make(chan struct{})}
	defer close(engine.release)
	SetImageUpdater(engine)
	t.Cleanup(func() { SetImageUpdater(nil) })
	prev := imageCheckDeadline
	imageCheckDeadline = 50 * time.Millisecond
	t.Cleanup(func() { imageCheckDeadline = prev })

	started := time.Now()
	result := ExecuteImageUpdate(context.Background(), imageCommand("check", nil))
	if result.Status != "failed" || !strings.HasPrefix(result.Error, "internal: ") {
		t.Fatalf("a stalled check = %+v", result)
	}
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("the worker waited %v for a stalled engine", took)
	}
	// The next command runs: the settings change that could fix the URL is not stuck behind the check.
	if result = ExecuteImageUpdate(context.Background(), imageCommand("settings", func(r *protocol.ImageUpdateRequest) {
		r.Channel, r.BaseURL = "dev", "https://updates.example.test"
	})); result.Status != "succeeded" {
		t.Fatalf("the command after a stalled check: %+v", result)
	}
}

// ctxUpdater blocks every call until released and reports the context error it saw, so a test can tell a
// cancelled engine call from one left to finish.
type ctxUpdater struct {
	fakeUpdater
	release chan struct{}
	seen    chan error
}

func (c *ctxUpdater) wait(ctx context.Context) {
	<-c.release
	c.seen <- ctx.Err()
}
func (c *ctxUpdater) Check(ctx context.Context) (nexupdate.ImageStatus, error) {
	c.wait(ctx)
	return nexupdate.ImageStatus{}, nil
}
func (c *ctxUpdater) Activate(ctx context.Context, _ nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	c.wait(ctx)
	return nexupdate.Operation{}, false, nil
}
func (c *ctxUpdater) Rollback(ctx context.Context, _ nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	c.wait(ctx)
	return nexupdate.Operation{}, false, nil
}

// A relayed activate or rollback that outlives the worker's wait is reported as still running, not failed,
// and the engine call is not cancelled: cancelling it mid-arm would leave the ESPs half written. A check
// is read-only and is cancelled.
func TestRelayedImageUpdateDoesNotCancelStateChangingActions(t *testing.T) {
	prevAction, prevCheck := imageActionDeadline, imageCheckDeadline
	imageActionDeadline, imageCheckDeadline = 50*time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { imageActionDeadline, imageCheckDeadline = prevAction, prevCheck })
	t.Cleanup(func() { SetImageUpdater(nil) })

	for _, action := range []string{"activate", "rollback"} {
		engine := &ctxUpdater{release: make(chan struct{}), seen: make(chan error, 1)}
		SetImageUpdater(engine)
		started := time.Now()
		result := ExecuteImageUpdate(context.Background(), imageCommand(action, nil))
		if took := time.Since(started); took > 5*time.Second {
			t.Fatalf("%s: the worker waited %v", action, took)
		}
		if result.Status != "needs_attention" || !strings.HasPrefix(result.Error, "still_running: ") {
			t.Fatalf("%s past the wait = %+v", action, result)
		}
		time.Sleep(50 * time.Millisecond) // let any (wrong) cancellation propagate
		close(engine.release)
		if err := <-engine.seen; err != nil {
			t.Fatalf("%s: the engine call was cancelled by the relay: %v", action, err)
		}
	}

	engine := &ctxUpdater{release: make(chan struct{}), seen: make(chan error, 1)}
	SetImageUpdater(engine)
	result := ExecuteImageUpdate(context.Background(), imageCommand("check", nil))
	if result.Status != "failed" {
		t.Fatalf("a stalled check = %+v", result)
	}
	close(engine.release)
	if err := <-engine.seen; err == nil {
		t.Fatal("the stalled check kept running under a live context")
	}
}
