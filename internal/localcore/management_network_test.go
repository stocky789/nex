// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// callLocalOn serves one local API request as a listener at local accepted it from remote. The
// addresses matter: whether a served call is evidence that a staged management network change
// carries traffic depends entirely on which socket carried it.
func callLocalOn(s *Service, local, remote, cookie, csrf string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://core.test/api/local/v1/host", bytes.NewReader(nil))
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("Origin", "https://core.test")
	r.Header.Set("X-CSRF-Token", csrf)
	r.RemoteAddr = remote
	if cookie != "" {
		r.AddCookie(&http.Cookie{Name: "__Host-nex_local_session", Value: cookie})
	}
	addr, err := net.ResolveTCPAddr("tcp", local)
	if err != nil {
		panic(err)
	}
	// What net/http itself puts on a served request's context for the accepting listener.
	r = r.WithContext(context.WithValue(r.Context(), http.LocalAddrContextKey, net.Addr(addr)))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

// A staged management network change can only be confirmed once the host has observed its own
// reachability over it. When the Controller is the thing that became unreachable, a served,
// authenticated local API call is the only observation left — but only when the listening
// socket that accepted it holds an address of the management bridge and the caller is somewhere
// else. An operator at the host console on 127.0.0.1 is exactly the person who can confirm a
// change that reaches nobody, and a call answered on a second, unchanged NIC says nothing about
// the configuration under test.
func TestAuthenticatedLocalCallRecordsManagementConnectivity(t *testing.T) {
	s, cfg := testService(t)
	if err := os.MkdirAll(cfg.AgentStateDir, 0700); err != nil {
		t.Fatal(err)
	}
	applied := time.Now().UTC().Add(-time.Minute)
	path := filepath.Join(cfg.AgentStateDir, "management-network.json")
	plant := func(t *testing.T) {
		t.Helper()
		b, err := json.Marshal(vmhost.ManagementNetworkRecord{
			OperationID: "op-1", Revision: 1, Stage: protocol.ManagementNetworkApplied,
			Spec:          protocol.ManagementNetworkSpec{Interfaces: []protocol.ManagementNIC{{Name: "nex0", MAC: "52:54:00:12:34:56", Mode: "static", Address: "192.0.2.10/24", Gateway: "192.0.2.1", Management: true}}},
			WindowSeconds: 600, AppliedAt: applied, Deadline: applied.Add(10 * time.Minute), UpdatedAt: applied,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}

	plant(t)
	// An unauthenticated call proves nothing about the operator's reachability.
	if w := callLocalOn(s, "192.0.2.10:8443", "192.0.2.200:40000", "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated host call: %d %s", w.Code, w.Body)
	}
	if read(t, path).ConnectivityKind != "" {
		t.Fatal("a rejected call was recorded as connectivity")
	}
	cookie, csrf := loginLocal(t, s)

	for name, tc := range map[string]struct {
		local, remote string
		counts        bool
	}{
		"the host console on loopback":             {"127.0.0.1:8443", "127.0.0.1:40000", false},
		"the management address, from itself":      {"192.0.2.10:8443", "127.0.0.1:40000", false},
		"a second, unchanged NIC":                  {"198.51.100.5:8443", "192.0.2.200:40000", false},
		"the management address, from the network": {"192.0.2.10:8443", "192.0.2.200:40000", true},
	} {
		t.Run(name, func(t *testing.T) {
			plant(t)
			if w := callLocalOn(s, tc.local, tc.remote, cookie, csrf); w.Code != 200 {
				t.Fatalf("host call: %d %s", w.Code, w.Body)
			}
			observed := read(t, path)
			if !tc.counts {
				if observed.ConnectivityKind != "" {
					t.Fatalf("a call that proves nothing was recorded as connectivity: %+v", observed)
				}
				return
			}
			if observed.ConnectivityKind != "local-api" || observed.ConnectivityAt.Before(applied) {
				t.Fatalf("the served call was not recorded as connectivity: %+v", observed)
			}
		})
	}
}

func read(t *testing.T, path string) vmhost.ManagementNetworkRecord {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rec vmhost.ManagementNetworkRecord
	if err = json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}
