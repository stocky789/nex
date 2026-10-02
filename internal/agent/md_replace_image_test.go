// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// System-disk replace rebuilds the data mirror only; it does not copy the image edition's root and verity
// slots, so an image host must not offer it even after the storage handshake, while the rest of the storage
// capabilities it shares that gate with stay on offer.
func TestImageEditionHostDoesNotAdvertiseSystemDiskReplace(t *testing.T) {
	SetImageUpdater(&fakeUpdater{})
	t.Cleanup(func() { SetImageUpdater(nil) })
	reports := make(chan protocol.ReportRequest, 1)
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
	s.State.StorageProtocolReady = true
	cfg := RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	got := <-reports
	if hasCapability(got.Capabilities, protocol.SystemDiskReplaceCapability) {
		t.Fatalf("an image-edition host advertised system-disk replace: %v", got.Capabilities)
	}
	for _, want := range []string{protocol.StorageDiscoveryCapability, protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability, protocol.ImageUpdateCapability} {
		if !hasCapability(got.Capabilities, want) {
			t.Fatalf("capabilities omit %s: %v", want, got.Capabilities)
		}
	}
}
