// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"encoding/json"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/protocol"
)

func poolTestStore(t *testing.T, dir, host, pool string) *agent.Store {
	t.Helper()
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	s, err := agent.OpenStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s.State.HostID, s.State.PoolID = host, pool
	if err = s.Save(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestJoinPoolAcceptsRealOneUseOffer(t *testing.T) {
	s, cfg := testService(t)
	member := poolTestStore(t, cfg.AgentStateDir, "member-1", "member-default")
	csr, err := agent.PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	if err = member.Close(); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(cfg.AgentStateDir, "managed-vms"), 0700); err != nil {
		t.Fatal(err)
	}
	master := poolTestStore(t, t.TempDir(), "master-1", "pool-1")
	defer master.Close()
	// The peer TLS configuration needs the master CA created before the listener starts.
	if _, _, err = agent.CreatePoolJoinOffer(master, "https://master.example.test", "member-1", csr); err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := agent.PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(agent.PoolPeerHandler(master))
	server.TLS = tlsConfig
	server.StartTLS()
	defer server.Close()
	ca, offer, err := agent.CreatePoolJoinOffer(master, server.URL, "member-1", csr)
	if err != nil {
		t.Fatal(err)
	}
	if len(offer) <= 256 {
		t.Fatalf("test offer unexpectedly small: %d bytes", len(offer))
	}
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "POST", "/api/local/v1/pool/join", map[string]string{"master_url": server.URL, "pool_ca_pem": ca, "offer": offer}, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("join rejected real offer (%d bytes): %d %s", len(offer), w.Code, w.Body)
	}
	handoffAgents.Lock()
	joined, err := getHandoffAgent(cfg.AgentStateDir)
	handoffAgents.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if joined.store.State.PoolID != "pool-1" || joined.store.State.HostID != "member-1" {
		t.Fatalf("joined state = %#v", joined.store.State)
	}
}

// The host-local pool view is what a recovery procedure reads when no Controller answers.
func TestLocalPoolViewRequiresSessionAndReportsMembership(t *testing.T) {
	s, cfg := testService(t)
	member := poolTestStore(t, cfg.AgentStateDir, "member-1", "member-pool")
	if err := agent.EnsurePoolMembership(member); err != nil {
		t.Fatal(err)
	}
	if err := member.Close(); err != nil {
		t.Fatal(err)
	}
	if w := callLocal(s, "GET", "/api/local/v1/pool", nil, "", "", "https://core.test"); w.Code != 401 {
		t.Fatalf("unauthenticated pool view = %d %s", w.Code, w.Body)
	}
	cookie, csrf := loginLocal(t, s)
	w := callLocal(s, "GET", "/api/local/v1/pool", nil, cookie, csrf, "https://core.test")
	if w.Code != 200 {
		t.Fatalf("pool view = %d %s", w.Code, w.Body)
	}
	var view struct {
		PoolID       string                   `json:"pool_id"`
		PoolName     string                   `json:"pool_name"`
		Role         string                   `json:"role"`
		MasterHostID string                   `json:"master_host_id"`
		Members      []protocol.PoolMember    `json:"members"`
		Join         *protocol.PoolJoinStatus `json:"join"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
		t.Fatal(err)
	}
	if view.PoolID != "member-pool" || view.Role != "master" || view.MasterHostID != "member-1" || len(view.Members) != 1 || view.Members[0].HostID != "member-1" {
		t.Fatalf("pool view = %+v", view)
	}
	if view.Join != nil {
		t.Fatalf("unstarted join reported: %+v", view.Join)
	}
}
