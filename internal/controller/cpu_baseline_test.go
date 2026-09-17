// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestExplicitCPUFeaturesRequireCapableCoreAndReplayUnchanged(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability)
	v := l.spec("fixed-cpu")
	v.CPU.Model = "FutureCPU-v1"
	v.CPU.Features = []protocol.CPUFeature{{Name: "future-feature", Policy: "require"}}
	if body := l.create("cpu-old-core", v, 409).Body.String(); !strings.Contains(body, "fixed CPU features") {
		t.Fatal(body)
	}
	l.caps = append(l.caps, protocol.CPUBaselineCapability)
	l.report()
	// Model need not be in the unmodified named-model catalogue: Core compares
	// the explicit feature definition to its native runtime before any effect.
	var created createdVM
	read(t, l.create("cpu-create", v, 202), &created)
	cmds := l.report().Commands
	if len(cmds) != 1 || cmds[0].VM.CPU.Features[0].Name != "future-feature" {
		t.Fatalf("fixed CPU definition lost: %+v", cmds)
	}
	l.caps = []string{protocol.ManagedVMCapability}
	l.report()
	var replay createdVM
	read(t, l.create("cpu-create", v, 200), &replay)
	if replay.VMID != created.VMID || replay.Operation.ID != created.Operation.ID {
		t.Fatal("replay changed fixed definition")
	}
	v.CPU.Features[0].Policy = "disable"
	l.create("cpu-create", v, 409)
}

func TestCPUBaselineInventoryAndIndependentMerge(t *testing.T) {
	baseline := &protocol.CPUBaseline{Model: "FutureCPU-v1", Features: []protocol.CPUFeature{{Name: "f", Policy: "require"}}, Machine: "q35", Generation: 1, MemberCount: 1, ObservedAt: time.Now()}
	old := &protocol.Inventory{CPUBaseline: baseline}
	if err := validateInventory(old); err != nil {
		t.Fatal(err)
	}
	baseline.Features = append(baseline.Features, baseline.Features[0])
	if validateInventory(old) == nil {
		t.Fatal("duplicate CPU feature accepted")
	}
	baseline.Features = baseline.Features[:1]
	if mergeInventory(old, &protocol.Inventory{}).CPUBaseline != nil {
		t.Fatal("failed independent probe retained stale baseline")
	}
	if mergeInventory(&protocol.Inventory{}, old).CPUBaseline == nil {
		t.Fatal("new independent baseline omitted")
	}
}

func TestExplicitCPUUpdateRequiresStoppedCapableCore(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.PowerCapability, protocol.UpdateCapability)
	spec := l.spec("cpu-update")
	l.create("initial", spec, 202)
	commands := l.report().Commands
	applied := *commands[0].VM
	state := &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}
	l.reportState("shut off", state, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	next := applied
	next.CPU.Model = "FutureCPU-v1"
	next.CPU.Features = []protocol.CPUFeature{{Name: "feature", Policy: "require"}}
	if result := l.update("cpu-edit", applied.ID, next, 409); !strings.Contains(result["error"].(string), "fixed CPU features") {
		t.Fatal(result)
	}
	l.caps = append(l.caps, protocol.CPUBaselineCapability)
	l.reportState("running", state)
	l.update("cpu-edit", applied.ID, next, 409)
	l.reportState("shut off", state)
	l.update("cpu-edit", applied.ID, next, 202)
	commands = l.reportState("shut off", state).Commands
	if len(commands) != 1 || commands[0].Kind != "vm-update" || len(commands[0].VM.CPU.Features) != 1 {
		t.Fatalf("CPU update lost: %+v", commands)
	}
}
