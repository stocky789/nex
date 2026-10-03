// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/nexupdate"
	"nex.local/nex/internal/protocol"
)

// stepEngine is a host engine whose status the test sets and whose calls it records.
type stepEngine struct {
	status    nexupdate.ImageStatus
	calls     []string
	requests  []string
	refuse    map[string]*nexupdate.Error
	afterAply func()
	scopes    []string             // scope of every apply
	applyOp   *nexupdate.Operation // what apply answers when set (a swap that is running, committed or failed)
}

func (e *stepEngine) Status(context.Context) (nexupdate.ImageStatus, error) { return e.status, nil }
func (e *stepEngine) call(name, id string) (nexupdate.Operation, bool, error) {
	e.calls, e.requests = append(e.calls, name), append(e.requests, id)
	// The engine refuses a request ID outside its rule; the fake must refuse it the same way.
	if name != "check" && !nexupdate.ValidRequestID(id) {
		return nexupdate.Operation{}, false, &nexupdate.Error{Status: 400, Code: "invalid_request", Message: "request_id must be 8 to 64 letters, digits, - or _"}
	}
	if err := e.refuse[name]; err != nil {
		return nexupdate.Operation{}, false, err
	}
	return nexupdate.Operation{ID: id}, false, nil
}
func (e *stepEngine) Check(context.Context) (nexupdate.ImageStatus, error) {
	_, _, err := e.call("check", "")
	if e.afterAply != nil {
		e.afterAply()
	}
	return e.status, err
}
func (e *stepEngine) Apply(_ context.Context, r nexupdate.ApplyRequest) (nexupdate.Operation, bool, error) {
	if r.Activate != "stage_only" {
		panic("a rollout stage must never arm the next boot")
	}
	e.scopes = append(e.scopes, r.Scope)
	op, replayed, err := e.call("apply", r.RequestID)
	if e.applyOp != nil && err == nil {
		op = *e.applyOp
		op.ID = r.RequestID
	}
	return op, replayed, err
}
func (e *stepEngine) Activate(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	if r.RunningVMs != "refuse" {
		panic("activate must keep the engine's refuse policy; the rollout shuts guests down itself")
	}
	return e.call("activate", r.RequestID)
}
func (e *stepEngine) Rollback(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	return e.call("rollback", r.RequestID)
}
func (e *stepEngine) Cancel(_ context.Context, r nexupdate.ActionRequest) (nexupdate.Operation, bool, error) {
	return e.call("cancel", r.RequestID)
}

type stepGuests struct {
	running  map[string]bool
	shutdown []string
	started  []string
	startErr error
	opIDs    []string
}

func (g *stepGuests) Running(context.Context) ([]string, error) {
	var out []string
	for id, on := range g.running {
		if on {
			out = append(out, id)
		}
	}
	return out, nil
}
func (g *stepGuests) Shutdown(_ context.Context, op, id string) error {
	g.shutdown, g.opIDs = append(g.shutdown, id), append(g.opIDs, op)
	return nil
}
func (g *stepGuests) Start(_ context.Context, op, id string) error {
	g.opIDs = append(g.opIDs, op)
	if g.startErr != nil {
		return g.startErr
	}
	g.started = append(g.started, id)
	return nil
}

func hostStepLab(t *testing.T) (*Store, *stepEngine, *stepGuests) {
	t.Helper()
	s := peerStore(t, "member-1", "pool-1")
	engine := &stepEngine{refuse: map[string]*nexupdate.Error{}, status: nexupdate.ImageStatus{
		Channel: "stable", Phase: nexupdate.PhaseCommitted, StateFormat: 2,
		Slots: []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}}}}
	guests := &stepGuests{running: map[string]bool{}}
	SetImageUpdater(engine)
	previous := poolUpdateGuestOps
	poolUpdateGuestOps = func(*Store) PoolUpdateGuests { return guests }
	t.Cleanup(func() { SetImageUpdater(nil); poolUpdateGuestOps = previous })
	return s, engine, guests
}

func step(action string, vms ...string) protocol.PoolUpdateStep {
	return protocol.PoolUpdateStep{RolloutID: "r1", StepID: poolUpdateStepID("r1", "member-1", action, 0), Action: action, Sequence: 2, VMIDs: vms}
}

func TestHostStatusReportsTheEngineAndGuests(t *testing.T) {
	s, engine, guests := hostStepLab(t)
	guests.running["vm-1"] = true
	engine.status.Running = true
	engine.status.Slots = append(engine.status.Slots, nexupdate.SlotStatus{Role: "staged", Sequence: 2})
	engine.status.Available = &nexupdate.Available{Sequence: 2, Compat: nexupdate.Compat{StateFormat: 2, MixedPool: true, MinControllerAPIEpoch: 1},
		Components: []nexupdate.ComponentInfo{{Bytes: 100, Required: true}, {Bytes: 900, Required: true, OnDemand: true}, {Bytes: 5}}}
	previousFree := poolUpdateFreeBytes
	poolUpdateFreeBytes = func() uint64 { return 4096 }
	t.Cleanup(func() { poolUpdateFreeBytes = previousFree })
	got, err := PoolUpdateHostStatusFor(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if got.RunningSequence != 1 || got.StagedSequence != 2 || !got.Busy || got.FreeBytes != 4096 || got.Channel != "stable" ||
		!reflect.DeepEqual(got.RunningVMs, []string{"vm-1"}) || got.Target == nil || got.Target.RequiredBytes != 100 ||
		got.Target.MinControllerAPIEpoch != 1 || len(got.Capabilities) != 1 {
		t.Fatalf("status = %+v target = %+v", got, got.Target)
	}
	SetImageUpdater(nil)
	if _, err = PoolUpdateHostStatusFor(context.Background(), s); err == nil {
		t.Fatal("a classic host answered with an image status")
	}
}

func TestHostStepsMapOntoTheEngine(t *testing.T) {
	s, engine, _ := hostStepLab(t)
	ctx := context.Background()
	// check: not offered yet, so the engine checks; still not offered is a stable refusal.
	if res := PoolUpdateHostStep(ctx, s, step("check")); res.Code != "release_unavailable" || engine.calls[len(engine.calls)-1] != "check" {
		t.Fatalf("check without the release = %+v calls %v", res, engine.calls)
	}
	engine.status.Available = &nexupdate.Available{Sequence: 2}
	engine.calls = nil
	if res := PoolUpdateHostStep(ctx, s, step("check")); !res.Done || len(engine.calls) != 0 {
		t.Fatalf("an already offered release re-checked: %+v %v", res, engine.calls)
	}
	// stage: starts the engine apply under the step ID, reports done only once staged.
	if res := PoolUpdateHostStep(ctx, s, step("stage")); res.Done || res.Code != "" || engine.requests[len(engine.requests)-1] != step("stage").StepID {
		t.Fatalf("stage in progress = %+v %v", res, engine.requests)
	}
	engine.status.Operation = &nexupdate.Operation{ID: step("stage").StepID, Error: &nexupdate.Error{Code: "state_format_downgrade", Message: "no"}}
	if res := PoolUpdateHostStep(ctx, s, step("stage")); res.Code != "state_format_downgrade" {
		t.Fatalf("a failed stage = %+v", res)
	}
	engine.status.Operation = nil
	engine.status.Slots = append(engine.status.Slots, nexupdate.SlotStatus{Role: "staged", Sequence: 2})
	engine.calls = nil
	if res := PoolUpdateHostStep(ctx, s, step("stage")); !res.Done || len(engine.calls) != 0 {
		t.Fatalf("a staged host re-staged: %+v %v", res, engine.calls)
	}
	// activate: the engine's own refusal is returned untouched; an armed host is not armed again.
	engine.refuse["activate"] = &nexupdate.Error{Code: "running_vms", Message: "guests"}
	if res := PoolUpdateHostStep(ctx, s, step("activate")); res.Code != "running_vms" {
		t.Fatalf("activate with guests = %+v", res)
	}
	delete(engine.refuse, "activate")
	engine.calls = nil
	if res := PoolUpdateHostStep(ctx, s, step("activate")); !res.Done || engine.calls[0] != "activate" {
		t.Fatalf("activate = %+v %v", res, engine.calls)
	}
	engine.status.Phase, engine.calls = nexupdate.PhaseArmed, nil
	engine.status.Slots[1].Role = "armed"
	if res := PoolUpdateHostStep(ctx, s, step("activate")); !res.Done || len(engine.calls) != 0 {
		t.Fatalf("a repeated activate re-armed: %+v %v", res, engine.calls)
	}
	// confirm: only a committed host on the target release confirms; a fallback is rolled_back.
	if res := PoolUpdateHostStep(ctx, s, step("confirm")); res.Done || res.Code != "" {
		t.Fatalf("confirm while armed = %+v", res)
	}
	engine.status.Slots = []nexupdate.SlotStatus{{Role: "booted", Sequence: 2}}
	engine.status.Phase = nexupdate.PhaseCommitted
	if res := PoolUpdateHostStep(ctx, s, step("confirm")); !res.Done {
		t.Fatalf("confirm committed = %+v", res)
	}
	// A host that runs the release without ever having updated (phase idle) has nothing to commit.
	engine.status.Phase = nexupdate.PhaseIdle
	if res := PoolUpdateHostStep(ctx, s, step("confirm")); !res.Done {
		t.Fatalf("confirm idle on the release = %+v", res)
	}
	engine.status.Phase = nexupdate.PhaseBooted
	if res := PoolUpdateHostStep(ctx, s, step("confirm")); res.Done || res.Code != "" {
		t.Fatalf("confirm booted but not committed = %+v", res)
	}
	engine.status.Slots, engine.status.Phase = []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}}, nexupdate.PhaseRolledBack
	if res := PoolUpdateHostStep(ctx, s, step("confirm")); res.Code != "rolled_back" {
		t.Fatalf("confirm after fallback = %+v", res)
	}
	if res := PoolUpdateHostStep(ctx, s, protocol.PoolUpdateStep{Action: "stage"}); res.Code != "invalid_step" {
		t.Fatalf("invalid step = %+v", res)
	}
	// A step ID the engine would refuse is refused before any engine call, for every action.
	engine.calls = nil
	for _, id := range []string{"r1/member-1/stage", "short", strings.Repeat("a", 65), "-leading-dash-id"} {
		bad := step("stage")
		bad.StepID = id
		if res := PoolUpdateHostStep(ctx, s, bad); res.Code != "invalid_step" || len(engine.calls) != 0 {
			t.Fatalf("step id %q = %+v calls %v", id, res, engine.calls)
		}
	}
}

// drain and undrain touch exactly the guests the master listed, never force anything, repeat the
// same operation ID, and a start that cannot happen yet is retried rather than failed.
func TestHostGuestStepsTouchOnlyTheListedGuests(t *testing.T) {
	s, _, guests := hostStepLab(t)
	ctx := context.Background()
	guests.running["listed"], guests.running["other"] = true, true
	res := PoolUpdateHostStep(ctx, s, step("drain", "listed"))
	if res.Done || !reflect.DeepEqual(guests.shutdown, []string{"listed"}) {
		t.Fatalf("drain = %+v shutdown %v", res, guests.shutdown)
	}
	guests.running["listed"] = false
	if res = PoolUpdateHostStep(ctx, s, step("drain", "listed")); !res.Done || len(guests.shutdown) != 1 {
		t.Fatalf("drained host = %+v shutdown %v", res, guests.shutdown)
	}
	guests.startErr = errors.New("the master refused admission")
	if res = PoolUpdateHostStep(ctx, s, step("undrain", "listed")); res.Done || res.Code != "" || res.Message == "" {
		t.Fatalf("an admission refusal must be retried, not failed: %+v", res)
	}
	guests.startErr = nil
	if res = PoolUpdateHostStep(ctx, s, step("undrain", "listed")); res.Done || !reflect.DeepEqual(guests.started, []string{"listed"}) {
		t.Fatalf("undrain = %+v started %v", res, guests.started)
	}
	guests.running["listed"] = true
	if res = PoolUpdateHostStep(ctx, s, step("undrain", "listed")); !res.Done || len(guests.started) != 1 {
		t.Fatalf("a running guest was started again: %+v %v", res, guests.started)
	}
	if !guests.running["other"] {
		t.Fatal("an unlisted guest was touched")
	}
	// opIDs: the drain's shutdown, the refused start, the start that worked. A repeated step repeats
	// its operation ID; the drain and the undrain are different steps, and so are different guests.
	if len(guests.opIDs) != 3 || guests.opIDs[1] != guests.opIDs[2] || guests.opIDs[0] == guests.opIDs[1] {
		t.Fatalf("operation IDs of the guest actions = %v", guests.opIDs)
	}
	if guests.opIDs[1] != poolUpdateOpID(step("undrain", "listed").StepID, "listed") {
		t.Fatal("the operation ID is not derived from the step and the guest")
	}
	if poolUpdateOpID("a", "b") == poolUpdateOpID("a", "c") {
		t.Fatal("two guests share an operation ID")
	}
	if poolUpdateOpID("a", "b") == poolUpdateOpID("d", "b") {
		t.Fatal("two steps share an operation ID for one guest")
	}
}

// The engine offers no release a host already runs (it clears "available" once the sequence is not
// newer), so the check of a host that is on the release is done without asking the engine: a
// rollout can still be started for the hosts that are not.
func TestHostCheckIsDoneForAReleaseTheHostAlreadyRuns(t *testing.T) {
	s, engine, _ := hostStepLab(t)
	engine.status.Slots = []nexupdate.SlotStatus{{Role: "booted", Sequence: 2}}
	if res := PoolUpdateHostStep(context.Background(), s, step("check")); !res.Done || res.Code != "" || len(engine.calls) != 0 {
		t.Fatalf("check on a host running the release = %+v calls %v", res, engine.calls)
	}
	engine.status.Slots = []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}}
	if res := PoolUpdateHostStep(context.Background(), s, step("check")); res.Code != "release_unavailable" {
		t.Fatalf("check on a host running an older release that is not offered = %+v", res)
	}
}

// Only a staged file whose content hashes to its name is served from the peer cache.
func TestPeerBlobCacheServesOnlyVerifiedContent(t *testing.T) {
	dir := t.TempDir()
	content := []byte("slot image bytes")
	sum := sha256.Sum256(content)
	name := hex.EncodeToString(sum[:])
	staging := filepath.Join(dir, "staging", "2")
	if err := os.MkdirAll(staging, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, name), content, 0600); err != nil {
		t.Fatal(err)
	}
	if path, ok := poolUpdateBlobFile(dir, name); !ok || filepath.Base(path) != name {
		t.Fatal("the verified blob was not found")
	}
	for _, bad := range []string{"", "../etc/passwd", name[:63], name + "0", "ZZ" + name[2:]} {
		if _, ok := poolUpdateBlobFile(dir, bad); ok {
			t.Fatalf("%q was served", bad)
		}
	}
	other := sha256.Sum256([]byte("different"))
	corrupt := hex.EncodeToString(other[:])
	if err := os.WriteFile(filepath.Join(staging, corrupt), content, 0600); err != nil {
		t.Fatal(err)
	}
	if _, ok := poolUpdateBlobFile(dir, corrupt); ok {
		t.Fatal("a file whose content does not match its name was served")
	}
}

// The controller step asks the host engine for a Controller swap (scope controller, never a host stage),
// reports it running until the operation ends, and passes the engine's own refusal code through.
func TestControllerStepAsksTheEngineForAScopedSwap(t *testing.T) {
	s, engine, _ := hostStepLab(t)
	ctx := context.Background()
	st := step("controller")
	if res := PoolUpdateHostStep(ctx, s, st); res.Done || res.Code != "" {
		t.Fatalf("a swap that has only started is still running: %+v", res)
	}
	if !reflect.DeepEqual(engine.scopes, []string{"controller"}) || engine.requests[0] != st.StepID {
		t.Fatalf("scopes %v requests %v", engine.scopes, engine.requests)
	}
	finished := time.Now()
	engine.applyOp = &nexupdate.Operation{Kind: "controller", Phase: nexupdate.PhaseCommitted, FinishedAt: &finished}
	if res := PoolUpdateHostStep(ctx, s, st); !res.Done {
		t.Fatalf("a committed swap is done: %+v", res)
	}
	engine.applyOp = &nexupdate.Operation{Kind: "controller", Phase: "failed", FinishedAt: &finished, Error: &nexupdate.Error{Code: "controller_rolled_back", Message: "restored"}}
	if res := PoolUpdateHostStep(ctx, s, st); res.Done || res.Code != "controller_rolled_back" {
		t.Fatalf("a failed swap reports the engine's code: %+v", res)
	}
	engine.applyOp = nil
	engine.refuse["apply"] = &nexupdate.Error{Status: 409, Code: "scope_unavailable", Message: "no appliance engine"}
	if res := PoolUpdateHostStep(ctx, s, st); res.Code != "scope_unavailable" {
		t.Fatalf("a refusal passes through: %+v", res)
	}
}

// A host tells the master it runs a swappable Controller (the two-disk profile in its deployment journal)
// and whether the release is controller_first with a Controller image; a single-disk appliance is not offered.
func TestHostStatusReportsASwappableControllerAndTheReleasesControllerImage(t *testing.T) {
	s, engine, _ := hostStepLab(t)
	engine.status.Available = &nexupdate.Available{Sequence: 2, Compat: nexupdate.Compat{ControllerFirst: true},
		Components: []nexupdate.ComponentInfo{{Kind: "slot-image", Required: true, Bytes: 1}, {Kind: "vm-image", OnDemand: true, Bytes: 9}}}
	dir := t.TempDir()
	previous := ApplianceJournalDir
	ApplianceJournalDir = dir
	t.Cleanup(func() { ApplianceJournalDir = previous })
	status := func() protocol.PoolUpdateHostStatus {
		got, err := PoolUpdateHostStatusFor(context.Background(), s)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := status(); got.ControllerVM || got.Target == nil || !got.Target.ControllerImage {
		t.Fatalf("no deployment: %+v", got)
	}
	journal := func(system string) {
		body := `{"id":"deploy-1","vm_id":"5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d","kind":"deploy","disk":"/x/controller.qcow2"` + system + `}`
		if err := os.WriteFile(filepath.Join(dir, "deploy-1.json"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	journal("")
	if got := status(); got.ControllerVM {
		t.Fatal("a single-disk appliance cannot be swapped and must not be offered")
	}
	journal(`,"system":"/x/system-a.img"`)
	if got := status(); !got.ControllerVM {
		t.Fatal("a two-disk deployment is swappable")
	}
	engine.status.Available.Compat.ControllerFirst = false
	if got := status(); got.Target.ControllerImage {
		t.Fatal("a release that is not controller_first must not claim a Controller step")
	}
}

// A journal the host cannot read must not look like "no Controller here": the master would skip the
// Controller step and update Core under a Controller that was never swapped.
func TestHostStatusFailsWhenTheControllerRecordCannotBeRead(t *testing.T) {
	s, _, _ := hostStepLab(t)
	dir := t.TempDir()
	previous := ApplianceJournalDir
	ApplianceJournalDir = dir
	t.Cleanup(func() { ApplianceJournalDir = previous })
	if err := os.WriteFile(filepath.Join(dir, "deploy-1.json"), []byte(`{"id":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := PoolUpdateHostStatusFor(context.Background(), s); err == nil || got.ControllerVM {
		t.Fatalf("an unreadable record must be an error: %+v %v", got, err)
	}
	ApplianceJournalDir = filepath.Join(dir, "missing")
	if got, err := PoolUpdateHostStatusFor(context.Background(), s); err != nil || got.ControllerVM {
		t.Fatalf("a host that never deployed a Controller reports none: %+v %v", got, err)
	}
}

func TestHostActivateRefusesADifferentStagedOrArmedSequence(t *testing.T) {
	for _, phase := range []string{nexupdate.PhaseStaged, nexupdate.PhaseArmed} {
		t.Run(phase, func(t *testing.T) {
			s, engine, _ := hostStepLab(t)
			role := "staged"
			if phase == nexupdate.PhaseArmed {
				role = "armed"
			}
			engine.status.Phase = phase
			engine.status.Slots = []nexupdate.SlotStatus{{Role: "booted", Sequence: 1}, {Role: role, Sequence: 3}}
			if phase == nexupdate.PhaseArmed {
				engine.status.Slots[0].Sequence = 2
			}
			got := PoolUpdateHostStep(context.Background(), s, step("activate"))
			if got.Done || got.Code != "sequence_mismatch" || len(engine.calls) != 0 {
				t.Fatalf("activate mismatched %s = %+v calls %v", role, got, engine.calls)
			}
			engine.status.Slots[1].Sequence = 2
			got = PoolUpdateHostStep(context.Background(), s, step("activate"))
			if !got.Done || got.Code != "" {
				t.Fatalf("activate matching %s = %+v", role, got)
			}
			wantCalls := 1
			if phase == nexupdate.PhaseArmed {
				wantCalls = 0
			}
			if len(engine.calls) != wantCalls {
				t.Fatalf("activate matching %s calls %v", role, engine.calls)
			}
		})
	}
}
