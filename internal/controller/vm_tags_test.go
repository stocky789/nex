// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"slices"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// createdTaggedLabVM creates a VM on a lab host and reports it applied.
func createdTaggedLabVM(t *testing.T, l *managedLab) (string, protocol.VMCreateSpec) {
	t.Helper()
	var created createdVM
	read(t, l.create("create-tags", l.spec("tagged"), 202), &created)
	commands := l.report().Commands
	applied := *commands[0].VM
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	return created.VMID, applied
}

func TestUpdateVMTagsValidateDedupeReplayAndRoundTrip(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.UpdateCapability, protocol.VMTagsCapability)
	id, applied := createdTaggedLabVM(t, l)
	next := applied
	for tags, want := range map[string]string{
		"Prod":                              `tag \"Prod\"`,
		"web_1":                             `tag \"web_1\"`,
		strings.Repeat("a", 33):             "1 to 32",
		"":                                  `tag \"\"`,
		"a,b,c,d,e,f,g,h,i,j,k,l,m,n,o,p,q": "at most 16 tags",
	} {
		next.Tags = strings.Split(tags, ",")
		if r := l.update("tags-bad", id, next, 400); !strings.Contains(r["error"].(string), want) {
			t.Fatalf("tags %q: %v", tags, r)
		}
	}
	// Seventeen entries that are sixteen distinct tags pass once de-duplicated.
	next.Tags = strings.Split("web,prod,web,a,b,c,d,e,f,g,h,i,j,k,l,m,n", ",")
	first := l.update("tags-1", id, next, 202)
	if replay := l.update("tags-1", id, next, 200); replay["operation"].(map[string]any)["id"] != first["operation"].(map[string]any)["id"] {
		t.Fatal("replay created a second operation")
	}
	commands := l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{applied}}).Commands
	sent := *commands[0].VM
	if want := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "prod", "web"}; !slices.Equal(sent.Tags, want) {
		t.Fatalf("dispatched tags %v", sent.Tags)
	}
	// The host reports the definition it applied; the VM list is that report.
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}, protocol.Result{OperationID: commands[0].OperationID, Status: "succeeded"})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || !slices.Equal(vms[0].Spec.Tags, sent.Tags) {
		t.Fatalf("listed %+v", vms)
	}
	// Tags are part of the complete definition: an edit without them clears them.
	cleared := sent
	cleared.Tags = nil
	l.update("tags-2", id, cleared, 202)
	if commands = l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{sent}}).Commands; len(commands[0].VM.Tags) != 0 {
		t.Fatalf("cleared tags dispatched %v", commands[0].VM.Tags)
	}
}

func TestUpdateVMTagsNeedSessionAndCSRF(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.UpdateCapability, protocol.VMTagsCapability)
	id, applied := createdTaggedLabVM(t, l)
	applied.Tags = []string{"web"}
	body := updateVMRequest{RequestID: "tags-auth", VMCreateSpec: applied}
	csrf := l.a.csrf
	l.a.csrf = ""
	l.a.call("PUT", "/api/v1/vms/"+id, body, nil, 403)
	l.a.csrf = csrf
	cookie := l.a.cookie
	l.a.cookie = nil
	l.a.call("PUT", "/api/v1/vms/"+id, body, nil, 401)
	l.a.cookie = cookie
}

// A host or master without vm-tags-v1 would drop tags, so a tagged edit is refused before
// anything is queued; untagged edits still work and its VMs list without tags.
func TestUpdateVMTagsRefusedByOlderCore(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.UpdateCapability)
	id, applied := createdTaggedLabVM(t, l)
	tagged := applied
	tagged.Tags = []string{"web"}
	if r := l.update("tags-old", id, tagged, 409); !strings.Contains(r["error"].(string), "does not support VM tags") {
		t.Fatal(r)
	}
	create := l.spec("tagged-new")
	create.Tags = []string{"web"}
	l.create("create-old", create, 409)
	l.update("edit-old", id, applied, 202)
	w := l.a.call("GET", "/api/v1/vms", nil, nil, 200)
	if strings.Contains(w.Body.String(), `"tags"`) {
		t.Fatalf("untagged VM listed tags: %s", w.Body)
	}
}

// A fresh Controller learns tags only from the host's report of its definition.
func TestFreshControllerAdoptsReportedVMTags(t *testing.T) {
	l := newManagedLab(t, protocol.ManagedVMCapability, protocol.UpdateCapability, protocol.VMTagsCapability)
	reported := l.spec("adopted")
	reported.ID, reported.Tags = "3d1c3b1e-6a55-4d4c-9f3e-2b7f1d0c9a11", []string{"db", "prod"}
	l.reportState("shut off", &protocol.ManagedState{VMs: []protocol.VMCreateSpec{reported}})
	var vms []managedAppliance
	read(t, l.a.call("GET", "/api/v1/vms", nil, nil, 200), &vms)
	if len(vms) != 1 || vms[0].ID != reported.ID || !slices.Equal(vms[0].Spec.Tags, reported.Tags) {
		t.Fatalf("adopted %+v", vms)
	}
}
