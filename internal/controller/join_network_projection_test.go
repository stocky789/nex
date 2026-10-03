package controller

import (
	"net/http"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// BUG-0535: a standalone host's auto-registered untagged network must follow the host into the pool it
// joins, exactly like its local storage: the API projection a UI filters by pool has to name
// the joined pool, not the deleted auto-pool.
func TestJoinedMemberNetworkProjectionFollowsJoinedPool(t *testing.T) {
	a := newClient(t)
	site := a.site()
	master := a.enroll(a.token(site, ""), csr(t))
	member := a.enroll(a.token(site, ""), csr(t))
	pool := a.poolOf(master.HostID)
	former := a.poolOf(member.HostID)
	memberCert := certificate(t, member.CertificatePEM)
	caps := []string{protocol.ManagedVMCapability}
	a.report(memberCert, protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps, Inventory: &protocol.Inventory{Managed: hostDefaults(member.HostID)}})

	membership := protocol.PoolMembership{PoolID: pool, MasterHostID: master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: master.HostID, Hostname: master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready"},
		{HostID: member.HostID, Hostname: member.HostID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready"},
	}}
	a.report(certificate(t, master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: 1, Capabilities: caps, Inventory: &protocol.Inventory{Managed: &protocol.ManagedState{Membership: &membership}}})
	joined := hostDefaults(member.HostID)
	joined.Membership = &membership
	a.report(memberCert, protocol.ReportRequest{Version: protocol.Version, Sequence: 2, Capabilities: caps, Inventory: &protocol.Inventory{Managed: joined}})

	var storage []protocol.StorageRegistration
	read(t, a.call(http.MethodGet, "/api/v1/storage", nil, nil, http.StatusOK), &storage)
	if len(storage) != 1 || storage[0].PoolID != pool {
		t.Fatalf("storage was not rehomed: %+v", storage)
	}
	var networks []protocol.NetworkRegistration
	read(t, a.call(http.MethodGet, "/api/v1/networks", nil, nil, http.StatusOK), &networks)
	if len(networks) != 1 || networks[0].ID != "host-untagged" || networks[0].HostID != member.HostID {
		t.Fatalf("member network missing: %+v", networks)
	}
	if networks[0].PoolID != pool {
		t.Fatalf("member network still projected under former pool %q (joined pool %q): %+v", former, pool, networks[0])
	}
}
