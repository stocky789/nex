// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// BUG-0536: create saves stage "allocating" before its first VM-specific effect (the exclusive disk
// directory), so a journal still at "validating" proves nothing VM-specific exists. An exact
// retry of such an interrupted create must not strand the VM ID and name as needs_attention
// for ever; it must settle as a definite failure that frees the name.
func TestInterruptedCreateAtValidatingSettlesAsProvenFailure(t *testing.T) {
	root := t.TempDir()
	e := NewManaged(filepath.Join(root, "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	c := protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &v}
	if err := e.save(managedJournal{Command: c, Result: protocol.Result{OperationID: c.OperationID, Status: "pending"}, Stage: "validating"}); err != nil {
		t.Fatal(err)
	}
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "failed" {
		t.Fatalf("interrupted create at validating = %+v, %v; want a definite failed result", r, err)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	other := managedTestVM()
	other.ID = "22222222-2222-4222-8222-222222222222"
	if err := vmConflict(js, other); err != nil {
		t.Fatalf("a create that provably did nothing still reserves its name: %v", err)
	}
}

// BUG-0537: a refusal journaled before execution is the operation's final answer; a
// redelivery returns it instead of executing, and a second record keeps the first.
func TestRecordedRefusalIsReturnedOnRedelivery(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	c := protocol.Command{OperationID: "create-refused", Kind: "vm-create", VM: &v}
	if err := e.RecordRefusal(context.Background(), c, "pool master request failed: EOF"); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordRefusal(context.Background(), c, "later reason"); err != nil {
		t.Fatal(err)
	}
	r, err := e.Execute(context.Background(), c, nil)
	if err != nil || r.Status != "failed" || r.Error != "pool master request failed: EOF" {
		t.Fatalf("redelivered refused create = %+v, %v", r, err)
	}
	state, _ := e.Applied(context.Background())
	if ids, err := e.NeverCreatedVMs(); len(state.VMs) != 0 || err != nil || !slices.Equal(ids, []string{v.ID}) {
		t.Fatalf("a refused create must be reported as never created: VMs %+v never created %v %v", state.VMs, ids, err)
	}
}

// BUG-0536: a replaced or restarted Controller never re-sends the interrupted create, so
// the periodic observation must settle it without a retry and report the VM as never created.
func TestInterruptedCreateAtValidatingSettlesWithoutRetry(t *testing.T) {
	e := NewManaged(filepath.Join(t.TempDir(), "state"))
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	c := protocol.Command{OperationID: "create-interrupted", Kind: "vm-create", VM: &v}
	old := time.Now().UTC().Add(-2 * time.Minute)
	if err := e.save(managedJournal{Command: c, Result: protocol.Result{OperationID: c.OperationID, Status: "needs_attention", ObservedAt: old}, Stage: "validating"}); err != nil {
		t.Fatal(err)
	}
	state, _ := e.Applied(context.Background())
	if ids, err := e.NeverCreatedVMs(); err != nil || !slices.Equal(ids, []string{v.ID}) {
		t.Fatalf("interrupted unallocated create was not settled as never created: %+v / %v %v", state.Outcomes, ids, err)
	}
	if r, _ := e.Execute(context.Background(), c, nil); r.Status != "failed" || r.Error != errCreateInterruptedUnallocated {
		t.Fatalf("settled create replays as %+v", r)
	}
}
