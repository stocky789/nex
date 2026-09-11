// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

func queuedPower(id string) protocol.Command {
	return protocol.Command{OperationID: id, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "guest", PoolID: "pool", HostID: "host", Action: "start"}}
}

func TestManagedQueueAdmissionReplayAndRestart(t *testing.T) {
	s := testStore(t)
	commands := []protocol.Command{queuedPower("z-first"), queuedPower("a-second"), queuedPower("m-third")}
	response := protocol.ReportResponse{Commands: commands}
	if err := applyResponse(s, response); err != nil {
		t.Fatal(err)
	}
	before := s.State.Journal["z-first"]
	if err := applyResponse(s, response); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, s.State.Journal["z-first"]) {
		t.Fatal("replay changed queue admission")
	}
	// Simulate a clock adjustment: durable admission order, not timestamps, decides FIFO.
	e := s.State.Journal["a-second"]
	e.RequestedAt = before.RequestedAt.Add(-time.Hour)
	s.State.Journal["a-second"] = e
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	dir := s.dir
	s.Close()
	reopened, err := OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, cmd := range commands {
		for i := 0; i < 100; i++ {
			if got := nextManagedCommand(reopened.State.Journal); got != cmd.OperationID {
				t.Fatalf("next %q, want %q", got, cmd.OperationID)
			}
		}
		e := reopened.State.Journal[cmd.OperationID]
		e.Status = "succeeded"
		reopened.State.Journal[cmd.OperationID] = e
	}
	if nextManagedCommand(reopened.State.Journal) != "" {
		t.Fatal("terminal work dispatched")
	}
}

func TestManagedQueueLegacyOrderAndOverflow(t *testing.T) {
	now := time.Now()
	journal := map[string]JournalEntry{}
	for _, id := range []string{"a", "b", "c"} {
		cmd := queuedPower(id)
		journal[id] = JournalEntry{Command: &cmd, Status: "pending", RequestedAt: now}
	}
	e := journal["c"]
	e.Order = 1
	e.RequestedAt = now.Add(-time.Hour)
	journal["c"] = e
	if got := nextManagedCommand(journal); got != "a" {
		t.Fatalf("legacy stable tie-break: %q", got)
	}
	e = journal["b"]
	e.RequestedAt = now.Add(-time.Minute)
	journal["b"] = e
	if got := nextManagedCommand(journal); got != "b" {
		t.Fatalf("legacy receipt order: %q", got)
	}
	s := testStore(t)
	s.State.Journal["full"] = JournalEntry{Order: math.MaxUint64}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{queuedPower("new")}}); err == nil {
		t.Fatal("queue sequence wrapped")
	}
	if _, ok := s.State.Journal["new"]; ok {
		t.Fatal("overflow admitted command")
	}
}

func TestManagedQueueSurvivesControllerReplacement(t *testing.T) {
	first, firstTrust := issuingController(t, "")
	second, secondTrust := issuingController(t, "")
	s := testStore(t)
	if err := Enroll(context.Background(), s, first.URL, firstTrust, "token", ""); err != nil {
		t.Fatal(err)
	}
	cmds := []protocol.Command{queuedPower("pending"), queuedPower("done"), {OperationID: "refresh", Kind: "refresh-inventory"}}
	if err := applyResponse(s, protocol.ReportResponse{Commands: cmds}); err != nil {
		t.Fatal(err)
	}
	e := s.State.Journal["done"]
	e.Status = "succeeded"
	s.State.Journal["done"] = e
	before := s.State.Journal["pending"]
	if err := Enroll(context.Background(), s, second.URL, secondTrust, "token", ""); err != nil {
		t.Fatal(err)
	}
	if len(s.State.Journal) != 2 {
		t.Fatalf("retained entries: %+v", s.State.Journal)
	}
	pending := s.State.Journal["pending"]
	if pending.Order != before.Order || pending.Status != "pending" || !pending.Acknowledged || !s.State.Journal["done"].Acknowledged {
		t.Fatal("replacement lost queue or retained stale transport results")
	}
	if nextManagedCommand(s.State.Journal) != "pending" {
		t.Fatal("replacement lost runnable work")
	}
	outcomes := commandOutcomes(s.State.Journal, nil)
	if len(outcomes) != 2 || outcomes[0].OperationID != "done" || outcomes[0].Status != "succeeded" || outcomes[1].Status != "queued" {
		t.Fatalf("adoption outcomes %+v", outcomes)
	}
}

func TestManagedQueueOutcomesPreferEngineAndRemainStable(t *testing.T) {
	journal := map[string]JournalEntry{}
	for _, id := range []string{"b", "a"} {
		cmd := queuedPower(id)
		journal[id] = JournalEntry{Command: &cmd, Status: "pending"}
	}
	first := commandOutcomes(journal, nil)
	for i := 0; i < 100; i++ {
		if !reflect.DeepEqual(first, commandOutcomes(journal, nil)) {
			t.Fatal("unstable queue inventory")
		}
	}
	engine := []protocol.ManagedOutcome{{OperationID: "b", Kind: "vm-power", ID: "guest", Status: "failed", Error: "refused"}}
	outcomes := commandOutcomes(journal, engine)
	if len(outcomes) != 2 || outcomes[0].Status != "failed" || outcomes[1].OperationID != "a" || outcomes[1].Status != "queued" {
		t.Fatalf("engine outcome replaced or duplicated: %+v", outcomes)
	}
	// Non-power completions also remain visible after a new Controller adopted their queue.
	cmd := protocol.Command{OperationID: "edit", Kind: "vm-update", VM: &protocol.VMCreateSpec{ID: "guest", Name: "A guest"}}
	journal["edit"] = JournalEntry{Command: &cmd, Status: "succeeded", Acknowledged: true}
	outcomes = commandOutcomes(journal, nil)
	if len(outcomes) != 3 || outcomes[2].Kind != "vm-update" || outcomes[2].Status != "succeeded" || outcomes[2].Name != "A guest" {
		t.Fatalf("edit completion not recoverable: %+v", outcomes)
	}
}

func TestManagedQueueLocalDependencyDoesNotChangeReplay(t *testing.T) {
	s := testStore(t)
	edit := protocol.Command{OperationID: "edit", Kind: "vm-update", VM: &protocol.VMCreateSpec{ID: "guest"}}
	start := queuedPower("start")
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{edit, start}}); err != nil {
		t.Fatal(err)
	}
	entry := s.State.Journal["start"]
	if entry.AfterOperationID != "edit" || entry.Command.Power.AfterOperationID != "" {
		t.Fatal("local dependency missing or transport input mutated")
	}
	dispatched := dispatchCommand(entry)
	if dispatched.Power.AfterOperationID != "edit" || entry.Command.Power.AfterOperationID != "" || start.Power.AfterOperationID != "" {
		t.Fatal("dispatch lost dependency or mutated transport command")
	}
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{start}}); err != nil {
		t.Fatal("replay refused:", err)
	}
	explicit := queuedPower("explicit")
	explicit.Power.AfterOperationID = "edit"
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{explicit}}); err != nil {
		t.Fatal(err)
	}
	if got := dispatchCommand(s.State.Journal["explicit"]).Power.AfterOperationID; got != "edit" {
		t.Fatalf("explicit prerequisite changed: %q", got)
	}
	other := queuedPower("other")
	other.Power.VMID = "other-guest"
	if err := applyResponse(s, protocol.ReportResponse{Commands: []protocol.Command{other}}); err != nil {
		t.Fatal(err)
	}
	if s.State.Journal["other"].AfterOperationID != "" {
		t.Fatal("unrelated VM became a dependency")
	}
}

// Exercise the real engine's promotion of an interrupted creation with a durable
// successful start record. Only the read-only domain listing is simulated.
func TestManagedQueueOutcomesUsePromotedEngineResult(t *testing.T) {
	v := protocol.VMCreateSpec{ID: "12345678-1234-4234-8234-123456789012", PoolID: "pool", HostID: "host", Name: "Guest",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "storage", Name: "system", SizeGiB: 1, Bus: "virtio", BootOrder: 1}},
		Boot:  protocol.VMBoot{Firmware: "bios"}, StartAfterCreate: true}
	cmd := protocol.Command{OperationID: "created", Kind: "vm-create", VM: &v}
	dir := t.TempDir()
	body, err := json.Marshal(map[string]any{"command": cmd, "result": protocol.Result{OperationID: cmd.OperationID, Status: "needs_attention", Error: "interrupted", ObservedAt: time.Now().Add(-2 * time.Minute)}, "stage": "starting", "start": "started"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "created.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "virsh"), []byte("#!/bin/sh\ncase \" $* \" in *' list --all --uuid '*) printf '%s\\n' '"+v.ID+"';; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	engine := vmhost.NewManaged(dir)
	state, checks := engine.Applied(context.Background())
	for _, check := range checks {
		if check.Status != "healthy" {
			t.Fatalf("promotion check: %+v", check)
		}
	}
	if len(state.VMs) != 1 {
		t.Fatalf("engine did not promote interrupted creation: %+v", state)
	}
	journal := map[string]JournalEntry{cmd.OperationID: {Command: &cmd, Status: "needs_attention", Error: "stale transport failure", Acknowledged: true}}
	outcomes := commandOutcomes(journal, state.Outcomes)
	if len(outcomes) != 1 || outcomes[0].Status != "succeeded" || outcomes[0].Error != "" {
		t.Fatalf("stale agent result overrode engine promotion: %+v", outcomes)
	}
	// Missing engine evidence never turns an uncertain operation into success.
	if got := commandOutcomes(journal, nil); len(got) != 1 || got[0].Status != "needs_attention" {
		t.Fatalf("uncertainty discarded without engine evidence: %+v", got)
	}
}
