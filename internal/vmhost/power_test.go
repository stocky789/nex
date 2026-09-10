// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedPowerRequests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	log := filepath.Join(t.TempDir(), "virsh.log")
	fakeCommand(t, "virsh", `echo "$3 $4" >> "$VMHOST_LOG"; case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; start|shutdown|reboot|destroy) [ "$VMHOST_FAIL" = "$3" ] && exit 1; exit 0;; *) exit 2;; esac`)
	t.Setenv("VMHOST_LOG", log)
	e := NewManaged(dir)
	vm := "11111111-2222-4333-a444-555555555555"
	command := func(op, action string) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm, PoolID: "pool1", HostID: "host1", Action: action}}
	}
	calls := func() string { b, _ := os.ReadFile(log); os.Remove(log); return string(b) }
	t.Setenv("VMHOST_STATE", "shut off")
	if r, err := e.Execute(context.Background(), command("p1", "start"), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("start: %v %+v", err, r)
	}
	if got := calls(); got != "domstate "+vm+"\nstart "+vm+"\n" {
		t.Fatalf("start calls %q", got)
	}
	t.Setenv("VMHOST_STATE", "running")
	if r, _ := e.Execute(context.Background(), command("p2", "start"), nil); r.Status != "succeeded" || calls() != "domstate "+vm+"\n" {
		t.Fatalf("start of a running VM must not call start: %+v", r)
	}
	if r, _ := e.Execute(context.Background(), command("p3", "reboot"), nil); r.Status != "succeeded" || calls() != "domstate "+vm+"\nreboot "+vm+"\n" {
		t.Fatalf("reboot %+v", r)
	}
	t.Setenv("VMHOST_FAIL", "shutdown")
	if r, _ := e.Execute(context.Background(), command("p4", "shutdown"), nil); r.Status != "failed" || r.Error == "" {
		t.Fatalf("refused shutdown must fail honestly: %+v", r)
	}
	t.Setenv("VMHOST_FAIL", "")
	if r, _ := e.Execute(context.Background(), command("p5", "explode"), nil); r.Status != "failed" {
		t.Fatalf("invalid action %+v", r)
	}
	t.Setenv("VMHOST_STATE", "paused")
	if r, _ := e.Execute(context.Background(), command("p6", "force-stop"), nil); r.Status != "failed" {
		t.Fatalf("paused VM must be refused: %+v", r)
	}
	// Interrupted after the request was issued: reconcile by observation only.
	j := managedJournal{Command: command("p7", "force-stop"), Result: protocol.Result{OperationID: "p7", Status: "pending"}, Stage: "powering"}
	b, _ := json.Marshal(j)
	os.WriteFile(filepath.Join(dir, "p7.json"), b, 0600)
	t.Setenv("VMHOST_STATE", "running")
	calls()
	if r, _ := e.Execute(context.Background(), command("p7", "force-stop"), nil); r.Status != "needs_attention" || calls() != "domstate "+vm+"\n" {
		t.Fatalf("interrupted force-stop with a running VM: %+v", r)
	}
	t.Setenv("VMHOST_STATE", "shut off")
	if r, _ := e.Execute(context.Background(), command("p7", "force-stop"), nil); r.Status != "succeeded" {
		t.Fatalf("interrupted force-stop with a stopped VM: %+v", r)
	}
	for _, tc := range []struct{ action, state, want string }{{"shutdown", "running", "needs_attention"}, {"shutdown", "shut off", "succeeded"}, {"reboot", "running", "needs_attention"}} {
		j := managedJournal{Command: command("p9-"+tc.action+tc.state[:3], tc.action), Result: protocol.Result{OperationID: "p9-" + tc.action + tc.state[:3], Status: "pending"}, Stage: "powering"}
		b, _ := json.Marshal(j)
		os.WriteFile(filepath.Join(dir, j.Command.OperationID+".json"), b, 0600)
		t.Setenv("VMHOST_STATE", tc.state)
		calls()
		if r, _ := e.Execute(context.Background(), j.Command, nil); r.Status != tc.want || calls() != "domstate "+vm+"\n" {
			t.Fatalf("interrupted %s with %s: %+v", tc.action, tc.state, r)
		}
	}
	// Interrupted before anything was issued: run once now.
	t.Setenv("VMHOST_STATE", "shut off")
	j = managedJournal{Command: command("p8", "start"), Result: protocol.Result{OperationID: "p8", Status: "pending"}, Stage: "validating"}
	b, _ = json.Marshal(j)
	os.WriteFile(filepath.Join(dir, "p8.json"), b, 0600)
	calls()
	if r, _ := e.Execute(context.Background(), command("p8", "start"), nil); r.Status != "succeeded" || calls() != "domstate "+vm+"\nstart "+vm+"\n" {
		t.Fatalf("interrupted-before-issue start: %+v", r)
	}
	if applied, _ := e.Applied(context.Background()); len(applied.VMs) != 0 {
		t.Fatal("power requests must not appear as applied settings")
	}
}
