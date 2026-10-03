// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// An ejected host becomes the master of a new single-host pool. Its old ejected
// membership row is history, so it must not prevent a subsequent join.
func TestEjectedHostCanRejoinItsFormerPool(t *testing.T) {
	l := newJoinLab(t)
	ejected := selfMembership(l.master.id, l.master.pool)
	ejected.Generation, ejected.Members[0].Generation = 2, 2
	ejected.Members = append(ejected.Members, protocol.PoolMember{HostID: l.member.id, Hostname: l.member.id, CertificateSHA256: labCertificateHash, Generation: 2, Status: "ejected", ObservedAt: time.Now().UTC()})
	l.master.inv.Managed.Membership = ejected
	l.send(&l.master, nil)

	l.member.inv.Managed.Membership = selfMembership(l.member.id, "pool-after-eject")
	l.send(&l.member, nil)
	if got := l.a.poolOf(l.member.id); got != "pool-after-eject" {
		t.Fatalf("ejected host was not adopted into its new pool: %s", got)
	}
	if w := l.addMember(l.joinRequest("rejoin-ejected"), labInvitation); w.Code != http.StatusAccepted {
		t.Fatalf("rejoin: %d %s", w.Code, w.Body.String())
	}
}

func TestEjectedHostRetainsItsLocalVMWhenAdoptedIntoANewPool(t *testing.T) {
	l := newTransferLab(t)
	l.adoptMemberVM()

	ejected := l.transferMembership(3, l.master.id)
	for i := range ejected.Members {
		if ejected.Members[i].HostID == l.member.id {
			ejected.Members[i].Status = "ejected"
		}
	}
	l.master.inv.Managed.Membership = ejected
	l.send(&l.master, nil)
	l.member.inv.Managed.Membership = selfMembership(l.member.id, "pool-after-eject")
	l.send(&l.member, nil)

	var raw string
	if err := l.a.c.db.QueryRow("SELECT command FROM managed_resources WHERE id=? AND kind='vm-create'", l.member.inv.Managed.VMs[0].ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var cmd protocol.Command
	if err := json.Unmarshal([]byte(raw), &cmd); err != nil {
		t.Fatal(err)
	}
	if cmd.VM == nil || cmd.VM.ID != l.member.inv.Managed.VMs[0].ID || cmd.VM.HostID != l.member.id || cmd.VM.PoolID != "pool-after-eject" {
		t.Fatalf("local VM was not preserved under the ejected host's new pool: %+v", cmd.VM)
	}
}
