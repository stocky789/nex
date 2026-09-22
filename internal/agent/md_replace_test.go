// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func TestSystemDiskReplaceCommandRoutingAndAdmission(t *testing.T) {
	if !managedCommand("host-system-disk-replace") {
		t.Fatal("host-system-disk-replace is not an admitted managed command")
	}
	cmd := protocol.Command{
		OperationID: "op-1", Kind: "host-system-disk-replace",
		SystemDiskReplace: &protocol.SystemDiskReplaceRequest{
			OperationID: "op-1", HostID: "host-9", Array: "md0",
		},
	}
	if host := commandHost(cmd); host != "host-9" {
		t.Fatalf("command host is %q, want host-9", host)
	}
	if host := commandHost(protocol.Command{OperationID: "op-1", Kind: "host-system-disk-replace"}); host != "" {
		t.Fatalf("a request-less command must address no host, got %q", host)
	}
}

// Admission refusals must fail before vmhost is reached: a missing or mismatched request
// never opens the system-disk-replace journal directory.
func TestSystemDiskReplaceRefusalsAreDefiniteResults(t *testing.T) {
	dir := t.TempDir()
	for name, tc := range map[string]struct {
		cmd      protocol.Command
		contains string
	}{
		"no request": {
			cmd:      protocol.Command{OperationID: "op-1", Kind: "host-system-disk-replace"},
			contains: "carries no request",
		},
		"request for another operation": {
			cmd: protocol.Command{
				OperationID: "op-1", Kind: "host-system-disk-replace",
				SystemDiskReplace: &protocol.SystemDiskReplaceRequest{
					OperationID: "op-2", HostID: "host-1", Array: "md0",
				},
			},
			contains: "does not match its command",
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := ExecuteSystemDiskReplace(context.Background(), dir, tc.cmd)
			if result.Status != "failed" || result.OperationID != "op-1" {
				t.Fatalf("result is %+v, want a failed op-1", result)
			}
			if !strings.Contains(result.Error, tc.contains) {
				t.Fatalf("error %q does not contain %q", result.Error, tc.contains)
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("admission refused but left state under %s: %v %v", dir, err, entries)
			}
		})
	}
}

// Replace disk… opens storage/discover, so the capability is only advertised once the
// storage handshake has completed — the same gate as discovery and ZFS provision.
func TestSystemDiskReplaceCapabilityFollowsStorageHandshake(t *testing.T) {
	reports := make(chan protocol.ReportRequest, 2)
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request protocol.ReportRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		reports <- request
		_ = json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	cfg := RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	before := <-reports
	if hasCapability(before.Capabilities, protocol.SystemDiskReplaceCapability) {
		t.Fatalf("system-disk replace advertised before storage handshake: %v", before.Capabilities)
	}
	s.State.StorageProtocolReady = true
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	after := <-reports
	if !hasCapability(after.Capabilities, protocol.SystemDiskReplaceCapability) {
		t.Fatalf("system-disk replace missing after storage handshake: %v", after.Capabilities)
	}
	for _, want := range []string{protocol.StorageDiscoveryCapability, protocol.ZFSProvisionCapability} {
		if !hasCapability(after.Capabilities, want) {
			t.Fatalf("capabilities omit %s after handshake: %v", want, after.Capabilities)
		}
	}
}
