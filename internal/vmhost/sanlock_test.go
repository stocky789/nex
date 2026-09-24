// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestEnsureVMLeaseDefinitionSkipsUnconfiguredVM(t *testing.T) {
	log := filepath.Join(t.TempDir(), "virsh.log")
	t.Setenv("VMHOST_LOG", log)
	fakeCommand(t, "virsh", `echo "$3" >> "$VMHOST_LOG"; exit 1`)
	vm := protocol.VMCreateSpec{ID: "11111111-2222-4333-a444-555555555555"}
	if err := ensureVMLeaseDefinition(context.Background(), nil, vm); err != nil {
		t.Fatalf("ordinary VM needs no storage lease verification: %v", err)
	}
	if calls, err := os.ReadFile(log); err == nil {
		t.Fatalf("ordinary VM called virsh: %q", calls)
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestDecodeSanlockHostStatusRequiresExactRecord(t *testing.T) {
	good := `{"host_id":103,"generation":1,"timestamp":17,"io_timeout":10,"host_state":"HOST_DEAD"}`
	status, err := decodeSanlockHostStatus([]byte(good), 103)
	if err != nil || status.HostState != sanlockHostDead || status.Generation != 1 {
		t.Fatalf("decode HOST_DEAD status = %+v, %v", status, err)
	}
	for _, state := range []string{"HOST_FREE", "HOST_FAIL"} {
		data := `{"host_id":103,"generation":1,"timestamp":17,"io_timeout":10,"host_state":"` + state + `"}`
		status, err := decodeSanlockHostStatus([]byte(data), 103)
		if err != nil || status.HostState == sanlockHostDead {
			t.Fatalf("decode %s status = %+v, %v", state, status, err)
		}
	}
	for name, raw := range map[string]string{
		"wrong host":      `{"host_id":102,"generation":1,"timestamp":17,"io_timeout":10,"host_state":"HOST_DEAD"}`,
		"zero generation": `{"host_id":103,"generation":0,"timestamp":17,"io_timeout":10,"host_state":"HOST_DEAD"}`,
		"zero timeout":    `{"host_id":103,"generation":1,"timestamp":17,"io_timeout":0,"host_state":"HOST_DEAD"}`,
		"unknown state":   `{"host_id":103,"generation":1,"timestamp":17,"io_timeout":10,"host_state":"HOST_GONE"}`,
		"unknown field":   `{"host_id":103,"generation":1,"timestamp":17,"io_timeout":10,"host_state":"HOST_DEAD","extra":1}`,
		"trailing record": good + `{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeSanlockHostStatus([]byte(raw), 103); err == nil {
				t.Fatal("unsafe or ambiguous sanlock result accepted")
			}
		})
	}
}

func TestReadSanlockHostStatusRejectsUntrustedQueryBeforeExec(t *testing.T) {
	for name, lockspace := range map[string]string{
		"empty":     "",
		"too long":  strings.Repeat("a", 49),
		"control":   "pool\x00repo",
		"non-ascii": "pool-é",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadSanlockHostStatus(context.Background(), lockspace, 1); err == nil {
				t.Fatal("invalid lockspace was sent to sanlock")
			}
		})
	}
	if _, err := ReadSanlockHostStatus(context.Background(), "pool-repo", 0); err == nil {
		t.Fatal("zero host ID was sent to sanlock")
	}
}
