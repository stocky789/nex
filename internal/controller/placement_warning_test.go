// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// sharedPoolLab is a master and a member of one pool with one pool-shared NFS repository,
// one pool VLAN and one shared ISO, whose member access the test sets per case. It is the
// shape of D2: the master is the placement authority, the Controller only mirrors what the
// master last reported about the member's access.
type sharedPoolLab struct {
	a              *testClient
	t              *testing.T
	master, member protocol.EnrollResponse
	pool           string
	seq            uint64
}

func newSharedPoolLab(t *testing.T) *sharedPoolLab {
	t.Helper()
	a := newClient(t)
	site := a.site()
	l := &sharedPoolLab{a: a, t: t}
	l.master = a.enroll(a.token(site, ""), csr(t))
	l.member = a.enroll(a.token(site, ""), csr(t))
	l.pool = a.poolOf(l.master.HostID)
	return l
}

func (l *sharedPoolLab) membership() *protocol.PoolMembership {
	return &protocol.PoolMembership{PoolID: l.pool, MasterHostID: l.master.HostID, Generation: 1, Members: []protocol.PoolMember{
		{HostID: l.master.HostID, CertificateSHA256: strings.Repeat("a", 64), Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()},
		{HostID: l.member.HostID, CertificateSHA256: strings.Repeat("b", 64), Generation: 1, Status: "ready", ObservedAt: time.Now().UTC()},
	}}
}

// report sends the master's journal with the member's access observed at one moment, then
// the member's own report, so both hosts are fresh to the Controller.
func (l *sharedPoolLab) report(observed time.Time) {
	l.t.Helper()
	l.seq++
	caps := []string{protocol.ManagedVMCapability, protocol.PoolVMPlacementCapability, protocol.StorageRepositoryCapability}
	nics := []protocol.NIC{{Name: "nex0", Kind: "physical"}}
	master := &protocol.ManagedState{
		Membership: l.membership(),
		Storage: []protocol.StorageRegistration{{ID: "pool-nfs", PoolID: l.pool, HostID: l.master.HostID, Name: "Pool NFS", Kind: protocol.StorageKindNFS, Generation: 1,
			Source:       protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: "nfs.example.test", Export: "/guests"}},
			MemberAccess: []protocol.PoolStorageAccess{{RepositoryID: "pool-nfs", HostID: l.member.HostID, Generation: 1, Identity: "nfs-fsid", Readable: true, Writable: true, QEMUWritable: true, Locking: true, ObservedAt: observed}}}},
		Networks: []protocol.NetworkRegistration{{ID: "pool-vlan", PoolID: l.pool, HostID: l.master.HostID, Name: "Guests", Kind: "vlan", Uplink: "nex0", VLAN: 100,
			Source: protocol.GuestBridge("nex0", 100), Generation: 1,
			MemberAccess: []protocol.PoolNetworkAccess{{NetworkID: "pool-vlan", HostID: l.member.HostID, Generation: 1, UplinkPresent: true, Bridge: protocol.GuestBridge("nex0", 100), ObservedAt: time.Now().UTC()}}}},
	}
	l.a.report(certificate(l.t, l.master.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "master", CPUCount: 8, MemoryBytes: 16 << 30, NICs: nics, Managed: master}})
	l.a.report(certificate(l.t, l.member.CertificatePEM), protocol.ReportRequest{Version: protocol.Version, Sequence: l.seq, Capabilities: caps,
		Inventory: &protocol.Inventory{Hostname: "member", CPUCount: 8, MemoryBytes: 16 << 30, NICs: nics, Managed: &protocol.ManagedState{Membership: l.membership()}}})
}

func (l *sharedPoolLab) createOnMember(request string, want int) map[string]any {
	l.t.Helper()
	spec := vmSpec(l.pool, l.member.HostID, request, "pool-nfs", "pool-vlan", "")
	var out map[string]any
	read(l.t, l.a.call("POST", "/api/v1/vms", createVMRequest{RequestID: request, VMCreateSpec: spec}, nil, want), &out)
	return out
}

func warningsOf(t *testing.T, body map[string]any) []string {
	t.Helper()
	raw, ok := body["warnings"]
	if !ok {
		return nil
	}
	values, ok := raw.([]any)
	if !ok {
		t.Fatalf("warnings is not a list: %+v", raw)
	}
	var out []string
	for _, value := range values {
		out = append(out, value.(string))
	}
	return out
}

// TestStaleMemberAccessWarnsWithoutRefusingPlacement: Core stays the placement authority, so
// a create against a member whose shared-storage observation is far beyond -stale-after is
// still accepted. What must not happen is silence: the 202 and the operation projection have
// to say that this Controller's mirror does not show the target able to use the repository,
// so a long-dispatched request against a stale host reads differently from a slow one.
func TestStaleMemberAccessWarnsWithoutRefusingPlacement(t *testing.T) {
	l := newSharedPoolLab(t)

	// A fresh observation: accepted, and nothing to warn about.
	l.report(time.Now().UTC())
	if warnings := warningsOf(t, l.createOnMember("fresh-create", 202)); len(warnings) != 0 {
		t.Fatalf("a fresh member was warned about: %+v", warnings)
	}
	// The member's NFS mount hangs: the master's observation freezes well beyond the window.
	frozen := time.Now().Add(-l.a.c.cfg.StaleAfter - 3*time.Minute).UTC()
	l.report(frozen)
	body := l.createOnMember("stale-create", 202)
	warnings := warningsOf(t, body)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "pool-nfs") {
		t.Fatalf("no warning for a create against a member whose repository access is stale: %+v", body)
	}
	var listed []map[string]any
	read(t, l.a.call("GET", "/api/v1/operations", nil, nil, 200), &listed)
	for _, op := range listed {
		if _, ok := op["warnings"]; ok {
			t.Fatalf("operations listing recomputes create warnings: %+v", listed)
		}
	}
}

