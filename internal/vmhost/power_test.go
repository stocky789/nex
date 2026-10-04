// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagedPowerDependencies(t *testing.T) {
	for _, tc := range []struct{ name, kind, status string }{
		{"create", "vm-create", "succeeded"},
		{"edit", "vm-update", "succeeded"},
		{"power", "vm-power", "succeeded"},
		{"media", "vm-media", "succeeded"},
		{"failed", "vm-update", "failed"},
		{"uncertain", "vm-update", "needs_attention"},
		{"pending", "vm-update", "pending"},
		{"missing", "vm-update", "succeeded"},
		{"wrong-vm", "vm-update", "succeeded"},
		{"not-vm", "storage-register", "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := managedTestEngine(t.TempDir())
			log := filepath.Join(t.TempDir(), "calls")
			t.Setenv("VMHOST_LOG", log)
			fakeCommand(t, "virsh", `echo "$3" >> "$VMHOST_LOG"; case "$3" in domstate) echo 'shut off';; start) exit 0;; *) exit 1;; esac`)
			v := managedTestVM()
			v.Revision = 1
			p := protocol.VMPowerRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "start", AfterOperationID: "before"}
			before := protocol.Command{OperationID: "before", Kind: tc.kind}
			switch tc.kind {
			case "vm-create":
				v.Revision = 0
				before.VM = &v
			case "vm-update":
				before.VM = &v
			case "vm-power":
				before.Power = &protocol.VMPowerRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "shutdown"}
			case "vm-media":
				before.VMMedia = &protocol.VMMediaRequest{VMID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Action: "eject"}
			case "storage-register":
				before.Storage = &protocol.StorageRegistration{ID: v.ID, PoolID: v.PoolID, HostID: v.HostID, Name: "Storage", Path: "/srv/vms"}
			}
			if tc.name == "wrong-vm" {
				v.ID = "11111111-2222-4333-a444-555555555555"
			}
			if tc.name != "missing" {
				if err := e.save(managedJournal{Command: before, Result: protocol.Result{OperationID: before.OperationID, Status: tc.status}}); err != nil {
					t.Fatal(err)
				}
			}
			c := protocol.Command{OperationID: "after", Kind: "vm-power", Power: &p}
			want := "failed"
			if tc.name == "create" || tc.name == "edit" || tc.name == "power" || tc.name == "media" {
				want = "succeeded"
			}
			for i := 0; i < 2; i++ {
				r, err := managedTestEngine(e.stateDir).Execute(context.Background(), c, nil)
				if err != nil || r.Status != want || (want == "failed" && r.Error == "") {
					t.Fatalf("attempt %d: %+v %v", i, r, err)
				}
			}
			calls, err := os.ReadFile(log)
			if want == "succeeded" {
				if err != nil || string(calls) != "domstate\nstart\n" {
					t.Fatalf("power must execute exactly once: %q %v", calls, err)
				}
			} else if len(calls) != 0 {
				t.Fatalf("failed dependency caused host effects: %q", calls)
			}
		})
	}
	for _, dependency := range []string{"../escape", strings.Repeat("x", 65), "self"} {
		c := protocol.Command{OperationID: "self", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: managedTestVM().ID, PoolID: "p", HostID: "h", Action: "start", AfterOperationID: dependency}}
		if validateManaged(c) == nil {
			t.Fatalf("invalid dependency accepted: %q", dependency)
		}
	}
}

func TestManagedPowerRequests(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	log := filepath.Join(t.TempDir(), "virsh.log")
	fakeCommand(t, "virsh", `echo "$3 $4" >> "$VMHOST_LOG"; case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; start|shutdown|reboot|destroy|reset) [ "$VMHOST_FAIL" = "$3" ] && exit 1; exit 0;; *) exit 2;; esac`)
	t.Setenv("VMHOST_LOG", log)
	e := managedTestEngine(dir)
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
	if r, _ := e.Execute(context.Background(), command("p3b", "force-reboot"), nil); r.Status != "succeeded" || calls() != "domstate "+vm+"\nreset "+vm+"\n" {
		t.Fatalf("force-reboot %+v", r)
	}
	t.Setenv("VMHOST_STATE", "shut off")
	if r, _ := e.Execute(context.Background(), command("p3c", "force-reboot"), nil); r.Status != "failed" || !strings.Contains(r.Error, "not running") {
		t.Fatalf("force-reboot when not running must refuse: %+v", r)
	}
	if got := calls(); got != "domstate "+vm+"\n" {
		t.Fatalf("refused force-reboot must not call reset: %q", got)
	}
	t.Setenv("VMHOST_STATE", "running")
	t.Setenv("VMHOST_FAIL", "shutdown")
	if r, _ := e.Execute(context.Background(), command("p4", "shutdown"), nil); r.Status != "needs_attention" || r.Error == "" {
		t.Fatalf("unconfirmed shutdown must remain uncertain: %+v", r)
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
	for _, tc := range []struct{ action, state, want string }{{"shutdown", "running", "needs_attention"}, {"shutdown", "shut off", "succeeded"}, {"reboot", "running", "needs_attention"}, {"force-reboot", "running", "needs_attention"}} {
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

func TestManagedPowerRequestCancellationSettlesFromObservation(t *testing.T) {
	for _, fromStage := range []string{"initial", "validating"} {
		t.Run(fromStage, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "state")
			state := filepath.Join(t.TempDir(), "state")
			log := filepath.Join(t.TempDir(), "virsh.log")
			if err := os.WriteFile(state, []byte("shut off\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("VMHOST_POWER_STATE", state)
			t.Setenv("VMHOST_POWER_LOG", log)
			fakeCommand(t, "virsh", `case "$3" in
domstate) cat "$VMHOST_POWER_STATE";;
start) echo running > "$VMHOST_POWER_STATE"; echo start >> "$VMHOST_POWER_LOG"; sleep 2;;
esac`)
			e := managedTestEngine(dir)
			c := protocol.Command{OperationID: "start-deadline", Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "11111111-2222-4333-a444-555555555555", PoolID: "pool1", HostID: "host1", Action: "start"}}
			if fromStage == "validating" {
				if err := os.MkdirAll(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if err := e.save(managedJournal{Command: c, Result: protocol.Result{OperationID: c.OperationID, Status: "pending"}, Stage: "validating"}); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan struct {
				r   protocol.Result
				err error
			}, 1)
			go func() {
				r, err := e.Execute(ctx, c, nil)
				result <- struct {
					r   protocol.Result
					err error
				}{r, err}
			}()
			until := time.After(time.Minute)
			for {
				if calls, _ := os.ReadFile(log); string(calls) == "start\n" {
					break
				}
				select {
				case <-until:
					t.Fatal("start request did not reach libvirt before cancellation")
				case <-time.After(5 * time.Millisecond):
				}
			}
			cancel()
			firstResult := <-result
			first, err := firstResult.r, firstResult.err
			if err != nil || first.Status != "needs_attention" || first.Error == "" {
				t.Fatalf("deadline start = %+v, %v", first, err)
			}
			second, err := e.Execute(context.Background(), c, nil)
			if err != nil || second.Status != "succeeded" {
				t.Fatalf("replayed observed start = %+v, %v", second, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || string(calls) != "start\n" {
				t.Fatalf("start must be issued once, calls=%q err=%v", calls, err)
			}
		})
	}
}

// A power command whose virsh call fails after the guest already acted is settled by observing
// the domain, not by the exit status. Whether that observation can prove anything depends on the
// action: reaching "shut off" proves a shutdown or force-stop took effect, while a guest still
// running proves nothing about a reboot, because observation cannot distinguish "the reset
// happened" from "the reset never arrived". So a stop settles on the spot and a reboot stays
// uncertain for an explicit reconcile. Reporting a completed shutdown as needing attention sent
// an operator to inspect a VM that had done exactly what was asked (BUG-0213).
func TestManagedPowerPostEffectFailureIsUncertain(t *testing.T) {
	for _, tc := range []struct {
		action, before, after, wantFirst, wantReplay string
	}{
		{"shutdown", "running", "shut off", "succeeded", "succeeded"},
		{"reboot", "running", "running", "needs_attention", "needs_attention"},
		{"force-reboot", "running", "running", "needs_attention", "needs_attention"},
		{"force-stop", "running", "shut off", "succeeded", "succeeded"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			state := filepath.Join(t.TempDir(), "state")
			if err := os.WriteFile(state, []byte(tc.before+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("VMHOST_POWER_STATE", state)
			log := filepath.Join(t.TempDir(), "virsh.log")
			t.Setenv("VMHOST_POWER_LOG", log)
			fakeCommand(t, "virsh", `case "$3" in domstate) cat "$VMHOST_POWER_STATE";; *) echo "$3" >> "$VMHOST_POWER_LOG"; echo "$VMHOST_POWER_AFTER" > "$VMHOST_POWER_STATE"; exit 1;; esac`)
			t.Setenv("VMHOST_POWER_AFTER", tc.after)
			c := protocol.Command{OperationID: "uncertain-" + tc.action, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: "11111111-2222-4333-a444-555555555555", PoolID: "pool1", HostID: "host1", Action: tc.action}}
			e := managedTestEngine(filepath.Join(t.TempDir(), "managed"))
			r, err := e.Execute(context.Background(), c, nil)
			if err != nil || r.Status != tc.wantFirst {
				t.Fatalf("post-effect %s = %+v, %v", tc.action, r, err)
			}
			replayed, err := e.Execute(context.Background(), c, nil)
			if err != nil || replayed.Status != tc.wantReplay {
				t.Fatalf("replayed %s = %+v, %v", tc.action, replayed, err)
			}
			calls, err := os.ReadFile(log)
			if err != nil || strings.Count(string(calls), "\n") != 1 {
				t.Fatalf("replayed %s issued a second action: %q, %v", tc.action, calls, err)
			}
		})
	}
}

// BUG-0273: a guest that put itself to sleep (ACPI S3, virsh domstate pmsuspended) is an
// active domain that runs nothing. Before the fix every power request refused it, the
// console refused it and, because the pool inventory could not observe it, every other VM
// on the host was blocked too. Start now wakes it, shutdown and reboot wake it first, and
// the force actions act on the QEMU process directly.
func TestManagedPowerWakesSleepingGuest(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	log := filepath.Join(t.TempDir(), "virsh.log")
	fakeCommand(t, "virsh", `echo "$3 $4" >> "$VMHOST_LOG"; case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; start|shutdown|reboot|destroy|reset|dompmwakeup) [ "$VMHOST_FAIL" = "$3" ] && exit 1; exit 0;; *) exit 2;; esac`)
	t.Setenv("VMHOST_LOG", log)
	t.Setenv("VMHOST_STATE", "pmsuspended")
	e := managedTestEngine(dir)
	vm := "11111111-2222-4333-a444-555555555555"
	command := func(op, action string) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "vm-power", Power: &protocol.VMPowerRequest{VMID: vm, PoolID: "pool1", HostID: "host1", Action: action}}
	}
	calls := func() string { b, _ := os.ReadFile(log); os.Remove(log); return string(b) }
	if state, err := ManagedVMState(context.Background(), vm); err != nil || state != "running" {
		t.Fatalf("a sleeping domain must be observed as a running owner: %q %v", state, err)
	}
	if asleep, err := ManagedVMAsleep(context.Background(), vm); err != nil || !asleep {
		t.Fatalf("ManagedVMAsleep = %v %v", asleep, err)
	}
	calls()
	for _, tc := range []struct{ action, want string }{
		{"start", "dompmwakeup"},
		{"shutdown", "dompmwakeup\nshutdown"},
		{"reboot", "dompmwakeup\nreboot"},
		{"force-reboot", "reset"},
		{"force-stop", "destroy"},
	} {
		want := "domstate " + vm + "\n"
		for _, c := range strings.Split(tc.want, "\n") {
			want += c + " " + vm + "\n"
		}
		if r, err := e.Execute(context.Background(), command("s-"+tc.action, tc.action), nil); err != nil || r.Status != "succeeded" {
			t.Fatalf("%s of a sleeping guest: %v %+v", tc.action, err, r)
		}
		if got := calls(); got != want {
			t.Fatalf("%s calls %q, want %q", tc.action, got, want)
		}
	}
	t.Setenv("VMHOST_FAIL", "dompmwakeup")
	if r, _ := e.Execute(context.Background(), command("s-fail", "shutdown"), nil); r.Status != "needs_attention" || !strings.Contains(calls(), "dompmwakeup") || strings.Contains(r.Error, "resolve that state") {
		t.Fatalf("a failed wake before an ACPI request must stay uncertain, not refused: %+v", r)
	}
	t.Setenv("VMHOST_STATE", "running")
	if r, _ := e.Execute(context.Background(), command("s-awake", "start"), nil); r.Status != "succeeded" || calls() != "domstate "+vm+"\n" {
		t.Fatalf("start of an awake VM must not wake it: %+v", r)
	}
}

// Every Nex definition removes ACPI S3/S4 from the guest firmware tables, so a guest never
// offers Sleep or Hibernate in the first place.
func TestManagedDomainXMLDisablesGuestSleep(t *testing.T) {
	v := managedTestVM()
	v.NICs = nil
	b, err := managedDomainXML(v, nil, []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `<suspend-to-mem enabled="no">`) || !strings.Contains(string(b), `<suspend-to-disk enabled="no">`) {
		t.Fatalf("definition does not disable guest sleep:\n%s", b)
	}
}
