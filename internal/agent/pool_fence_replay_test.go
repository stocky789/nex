// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"testing"
)

// The agent keeps every Controller command in its journal and compares a replay of the same
// operation ID with it byte for byte; a difference stops the agent ("controller reused command ID
// with different input"). Recording a management controller pins its certificate into the profile,
// so executing the command must not write that pin into the command the journal holds. The pool
// lab found a master whose agent stopped at its first restart after a fence profile was recorded.
func TestRecordingAManagementControllerLeavesTheJournaledCommandUntouched(t *testing.T) {
	lab := newTransferLab(t, 1)
	master := lab.host("master-1")
	server := labRedfishBMC(t, "4c4c4544-0037-3810-8057-b4c04f435331")
	bmc := labBMCProfile(server, "lab-bmc")
	writeLabBMCCredential(t, master, "lab-bmc")
	labSystemUUIDs(t, map[string]string{"master-1": "4c4c4544-0037-3810-8057-b4c04f435331"})
	if err := ReportPoolSystemUUID(context.Background(), master); err != nil {
		t.Fatal(err)
	}
	cmd := fenceProfileCommand("fp-replay", "master-1", "master-1", bmc)
	before, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if result := ExecutePoolFenceProfile(master, cmd); result.Status != "succeeded" {
		t.Fatalf("recording a management controller = %#v", result)
	}
	after, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("executing the command rewrote it, so its replay no longer matches the journal:\nbefore %s\nafter  %s", before, after)
	}
}
