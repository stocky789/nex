// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// The post-update check treats LastReport as proof the Controller is receiving this host, so
// a refused report must not count.
func TestLastReportOnlyRecordsAcceptedReports(t *testing.T) {
	var accept atomic.Bool
	_, identity := testManagedIdentity(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !accept.Load() {
			http.Error(w, "refused", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(protocol.ReportResponse{})
	}))
	s := testStore(t)
	s.State = identity
	cfg := RunConfig{ProbeInterval: time.Second, ProbeTimeout: time.Second}
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err == nil {
		t.Fatal("a refused report succeeded")
	}
	if !s.LastReport().IsZero() {
		t.Fatal("a refused report was recorded as reported")
	}
	accept.Store(true)
	if err := report(context.Background(), s, NewMonitor(time.Second, nil), cfg); err != nil {
		t.Fatal(err)
	}
	if s.LastReport().IsZero() {
		t.Fatal("an accepted report was not recorded")
	}
}
