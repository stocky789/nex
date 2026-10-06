// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPoolCommandRejectsStaleOrUnboundMember(t *testing.T) {
	m := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 2, Members: []PoolMember{{HostID: "master-1", Hostname: "master-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}, {HostID: "member-1", Hostname: "member-1", CertificateSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", Generation: 2, Status: "ready"}}}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 2}).Valid(); err != nil {
		t.Fatalf("valid pool command: %v", err)
	}
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m, ExpectedVersion: 1}).Valid(); err == nil {
		t.Fatal("stale membership generation accepted")
	}
	m.Members[1].CertificateSHA256 = "unbound"
	if err := (PoolCommand{RequestID: "join-1", Action: "member-join", Membership: m}).Valid(); err == nil {
		t.Fatal("unbound member certificate accepted")
	}
}

func TestReplicaAckExclusionIsBackwardCompatibleAndSigned(t *testing.T) {
	member := PoolMember{HostID: "member-1", Status: "ready"}
	encoded, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "replica_ack_excluded") {
		t.Fatalf("false value changed canonical JSON: %s", encoded)
	}
	var decoded PoolMember
	if err = json.Unmarshal([]byte(`{"host_id":"member-1","status":"ready"}`), &decoded); err != nil || decoded.ReplicaAckExcluded {
		t.Fatalf("member without the field did not default to mandatory ACK: %#v, %v", decoded, err)
	}

	base := PoolAuthorityReplica{Membership: PoolMembership{PoolID: "pool-1", MasterHostID: "member-2", Generation: 4,
		Members: []PoolMember{{HostID: "member-1", Status: "ready"}, {HostID: "member-2", Status: "ready"}}}}
	before, err := base.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	base.Membership.Members[0].ReplicaAckExcluded = true
	after, err := base.ContentDigest()
	if err != nil {
		t.Fatal(err)
	}
	if before == after {
		t.Fatal("authority replica digest did not bind replica ACK exclusion")
	}
}

func TestSnapshotAdmissionRefusalIsBoundedAndCannotComplete(t *testing.T) {
	reservation := PoolVMRecoveryReservation{
		OperationID: "snapshot_1", Kind: "snapshot-create", OwnerOperationOrder: 1, PoolID: "pool-1", VMID: "11111111-1111-4111-8111-111111111111",
		OwnerHostID: "host-a", Epoch: 1, SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		IntentSHA256: strings.Repeat("c", 64), AdmissionRefusal: "name-in-use",
	}
	if err := reservation.Valid(); err != nil {
		t.Fatalf("valid name denial: %v", err)
	}
	evidence := PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("d", 64)}
	if err := evidence.Valid(); err != nil {
		t.Fatalf("valid no-effect denial evidence: %v", err)
	}
	evidence.Outcome = "completed"
	if err := evidence.Valid(); err == nil {
		t.Fatal("denied snapshot admission accepted completed native evidence")
	}
	for _, mutate := range []func(*PoolVMRecoveryReservation){
		func(r *PoolVMRecoveryReservation) { r.AdmissionRefusal = "unknown" },
		func(r *PoolVMRecoveryReservation) { r.Kind = "snapshot-revert" },
	} {
		invalid := reservation
		mutate(&invalid)
		if err := invalid.Valid(); err == nil {
			t.Fatalf("invalid refusal reservation accepted: %#v", invalid)
		}
	}
}

func TestPoolVMSnapshotDependenciesAreCanonicalAndBounded(t *testing.T) {
	spec := validSpec()
	spec.Disks = append(spec.Disks, VMDisk{StorageID: "storage-z", Name: "data", SizeGiB: 4, Bus: "virtio", BootOrder: 3})
	spec.NICs = append(spec.NICs, VMNIC{NetworkID: "network-a", Model: "virtio"})
	spec.DriversMediaID = "media-b"
	got, err := PoolVMSnapshotDependenciesForSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	want := PoolVMSnapshotDependencies{
		StorageIDs: []string{"s", "storage-z"},
		NetworkIDs: []string{"n", "network-a"},
		MediaIDs:   []string{"m", "media-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("dependencies = %#v, want %#v", got, want)
	}
	empty := spec
	empty.MediaID, empty.DriversMediaID, empty.NICs = "", "", nil
	got, err = PoolVMSnapshotDependenciesForSpec(empty)
	if err != nil || got.MediaIDs == nil || got.NetworkIDs == nil || len(got.MediaIDs) != 0 || len(got.NetworkIDs) != 0 {
		t.Fatalf("empty dependency sets must remain explicit: %#v (%v)", got, err)
	}

	for _, tc := range []struct {
		deps  PoolVMSnapshotDependencies
		valid bool
	}{
		{PoolVMSnapshotDependencies{StorageIDs: []string{"z", "a"}}, false},
		{PoolVMSnapshotDependencies{NetworkIDs: []string{"n", "n"}}, false},
		{PoolVMSnapshotDependencies{MediaIDs: []string{"bad/id"}}, false},
		{PoolVMSnapshotDependencies{StorageIDs: []string{}, NetworkIDs: nil, MediaIDs: []string{}}, false},
		{PoolVMSnapshotDependencies{StorageIDs: []string{"repo-1"}, NetworkIDs: []string{}, MediaIDs: []string{}}, true},
	} {
		if (tc.deps.Valid() == nil) != tc.valid {
			t.Fatalf("dependency validity mismatch: %#v", tc.deps)
		}
	}
}

func TestPoolJoinCommandRequiresTheWholeInvitation(t *testing.T) {
	join := PoolCommand{RequestID: "join-1", Action: "join", MemberHostID: "member-1", MasterURL: "https://master.example.test", PoolCAPEM: "-----BEGIN CERTIFICATE-----", Invitation: "invitation", PeerOrigin: "https://member.example.test"}
	// A join carries no membership record: the joining host is not a member yet.
	if err := join.Valid(); err != nil {
		t.Fatalf("valid pool join: %v", err)
	}
	for name, mutate := range map[string]func(*PoolCommand){
		"no request":      func(c *PoolCommand) { c.RequestID = "" },
		"no member":       func(c *PoolCommand) { c.MemberHostID = "" },
		"long member":     func(c *PoolCommand) { c.MemberHostID = string(make([]byte, 129)) },
		"no master":       func(c *PoolCommand) { c.MasterURL = "" },
		"no CA":           func(c *PoolCommand) { c.PoolCAPEM = "" },
		"no invitation":   func(c *PoolCommand) { c.Invitation = "" },
		"long invitation": func(c *PoolCommand) { c.Invitation = string(make([]byte, 8193)) },
		"long origin":     func(c *PoolCommand) { c.PeerOrigin = string(make([]byte, 513)) },
		"unknown action":  func(c *PoolCommand) { c.Action = "leave" },
	} {
		invalid := join
		mutate(&invalid)
		if err := invalid.Valid(); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestPoolStorageLeaseRequiresStableDistinctHostIDsAndExactNFSIdentity(t *testing.T) {
	identity := StorageIdentity{NFS: &NFSStorageIdentity{Server: "nfs.example.test", Export: "/exports/vms", FilesystemID: "0:42"}}
	lease := &PoolStorageLeaseConfig{
		Repositories: []PoolStorageLeaseRepository{{RepositoryID: "repo-1", RepositoryGeneration: 3, RepositoryIdentity: identity,
			Lockspace: PoolStorageLeaseLockspaceName("pool-1", "repo-1")}},
		AuthorityRepositoryID: "repo-1", IOTimeoutSeconds: 10, HostDeadSeconds: 60, FenceTimeoutSeconds: 120,
	}
	membership := PoolMembership{PoolID: "pool-1", MasterHostID: "master-1", Generation: 4,
		StorageLease: lease,
		Members: []PoolMember{
			{HostID: "master-1", Hostname: "master-1", SanlockHostID: 1, CertificateSHA256: strings.Repeat("a", 64), Generation: 4, Status: "ready"},
			{HostID: "member-1", Hostname: "member-1", SanlockHostID: 2, CertificateSHA256: strings.Repeat("b", 64), Generation: 4, Status: "ready"},
		},
	}
	if err := membership.Valid(); err != nil {
		t.Fatalf("valid lease membership rejected: %v", err)
	}
	for name, change := range map[string]func(*PoolMembership){
		"duplicate host lease ID":    func(m *PoolMembership) { m.Members[1].SanlockHostID = 1 },
		"out of range host lease ID": func(m *PoolMembership) { m.Members[1].SanlockHostID = PoolSanlockMaxHostID + 1 },
		"missing host lease ID":      func(m *PoolMembership) { m.Members[1].SanlockHostID = 0 },
		"non-NFS repository": func(m *PoolMembership) {
			m.StorageLease.Repositories[0].RepositoryIdentity = StorageIdentity{SMB: &SMBStorageIdentity{Server: "files.example.test", Share: "vms", FilesystemID: "1"}}
		},
		"wrong lockspace": func(m *PoolMembership) { m.StorageLease.Repositories[0].Lockspace = "client-path" },
		"unsorted repositories": func(m *PoolMembership) {
			m.StorageLease.Repositories = append(m.StorageLease.Repositories,
				PoolStorageLeaseRepository{RepositoryID: "repo-0", RepositoryGeneration: 1, RepositoryIdentity: identity, Lockspace: PoolStorageLeaseLockspaceName("pool-1", "repo-0")})
		},
		"bound shorter than dead interval": func(m *PoolMembership) { m.StorageLease.FenceTimeoutSeconds = 59 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := membership
			bad.Members = append([]PoolMember(nil), membership.Members...)
			copyLease := *lease
			bad.StorageLease = &copyLease
			change(&bad)
			if bad.Valid() == nil {
				t.Fatal("unsafe lease membership accepted")
			}
		})
	}
}

// A caller must be able to tell "this Core says the authority is not handed off" from "this
// Core does not say", so handed_off is on the wire even when false, like cancellable.
func TestPoolTransferStatusAlwaysReportsHandedOff(t *testing.T) {
	body, err := json.Marshal(PoolTransferStatus{OperationID: "op-1", Action: "promote", Role: "source"})
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]any
	if err = json.Unmarshal(body, &wire); err != nil {
		t.Fatal(err)
	}
	if got, ok := wire["handed_off"]; !ok || got != false {
		t.Fatalf("handed_off = %v (present %v), want false and present: %s", got, ok, body)
	}
	if body, err = json.Marshal(PoolTransferStatus{HandedOff: true}); err != nil || !strings.Contains(string(body), `"handed_off":true`) {
		t.Fatalf("handed_off true did not reach the wire: %s (%v)", body, err)
	}
}

// A member whose Core stamps no profile on its results. Refusing the whole report
// would lose every other host's result in it for the sake of one field the master would drop
// anyway, so an unstamped result is accepted and dropped where stamps are compared.
// (Reported by independent review.)
func TestAnUnstampedFenceCheckIsAcceptedAndAMalformedOneIsNot(t *testing.T) {
	older := PoolFenceCheckRequest{Checks: []PoolFenceCheck{
		{HostID: "host-a", Result: PoolFenceCheckOK},
		{HostID: "host-b", Result: PoolFenceCheckUnreachable, Detail: "no route"},
	}}
	if err := older.Valid(); err != nil {
		t.Fatalf("a pass from a Core that does not stamp was refused: %v", err)
	}
	malformed := PoolFenceCheckRequest{Checks: []PoolFenceCheck{{HostID: "host-a", Result: PoolFenceCheckOK, Profile: "zz"}}}
	if malformed.Valid() == nil {
		t.Fatal("a malformed profile stamp was accepted")
	}
}

// Every host's watchdog report crosses the pool's own mTLS, so the master bounds it before it
// records it: the classification is a closed set and the strings are bounded.
func TestAWatchdogReportIsBounded(t *testing.T) {
	if err := (PoolHostWatchdog{Kind: PoolWatchdogHardware, Device: "watchdog0", TimeoutSeconds: 30}).Valid(); err != nil {
		t.Fatalf("a valid watchdog report was refused: %v", err)
	}
	for name, report := range map[string]PoolHostWatchdog{
		"an unsupported kind":  {Kind: "titanium"},
		"an empty kind":        {},
		"a newline in a field": {Kind: PoolWatchdogHardware, Driver: "iTCO\nwdt"},
		"an absurd timeout":    {Kind: PoolWatchdogHardware, TimeoutSeconds: 99999},
	} {
		if report.Valid() == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

func TestVMSnapshotRequestAndCatalogDescriptorAreBounded(t *testing.T) {
	request := VMSnapshotRequest{Name: "before-upgrade", Description: "pre-upgrade checkpoint"}
	if err := request.Valid(); err != nil {
		t.Fatalf("valid snapshot request refused: %v", err)
	}
	for name, bad := range map[string]VMSnapshotRequest{
		"empty name":       {},
		"padded name":      {Name: " snapshot"},
		"long name":        {Name: strings.Repeat("x", 65)},
		"long description": {Name: "valid", Description: strings.Repeat("x", 513)},
		"NUL name":         {Name: "bad\x00name"},
	} {
		t.Run(name, func(t *testing.T) {
			if bad.Valid() == nil {
				t.Fatal("invalid snapshot metadata was accepted")
			}
		})
	}
	descriptor := PoolVMSnapshot{
		ID: "operation-1", VMID: "11111111-1111-4111-8111-111111111111", Name: request.Name,
		Description: request.Description, CreatedAt: time.Now().UTC(), SpecRevision: 2,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	if err := descriptor.Valid(); err != nil {
		t.Fatalf("valid snapshot descriptor refused: %v", err)
	}
	badDescriptor := descriptor
	badDescriptor.ID = "../snapshot"
	if badDescriptor.Valid() == nil {
		t.Fatal("path-like snapshot ID was accepted")
	}
}

func TestPoolVMSnapshotRevertSpecPreservesCurrentIdentityAndMetadata(t *testing.T) {
	captured := validSpec()
	captured.Name = "old-name"
	captured.Description = "old description"
	captured.Tags = []string{"old"}
	captured.Autostart = true
	captured.CPU.Count = 2
	captured.Memory.SizeMiB = 4096
	captured.Revision = 1

	current := validSpec()
	current.Name = "renamed"
	current.Description = "current description"
	current.Tags = []string{"current", "operator"}
	current.Autostart = false
	current.StartAfterCreate = true
	current.CPU.Count = 4
	current.Memory.SizeMiB = 8192
	current.Revision = 3

	got, err := PoolVMSnapshotRevertSpec(captured, current)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != current.ID || got.PoolID != current.PoolID || got.HostID != current.HostID || got.Name != current.Name ||
		got.Description != current.Description || !reflect.DeepEqual(got.Tags, current.Tags) || got.Autostart != current.Autostart ||
		got.StartAfterCreate != current.StartAfterCreate || got.Revision != current.Revision+1 {
		t.Fatalf("revert changed current identity/presentation fields: %#v", got)
	}
	if !reflect.DeepEqual(got.CPU, captured.CPU) || got.Memory != captured.Memory || !reflect.DeepEqual(got.Disks, captured.Disks) ||
		!reflect.DeepEqual(got.NICs, captured.NICs) || got.Boot != captured.Boot || got.GuestOS != captured.GuestOS ||
		got.MediaID != captured.MediaID || got.DriversMediaID != captured.DriversMediaID {
		t.Fatalf("revert did not restore captured execution configuration: %#v", got)
	}
	if _, err := PoolVMSnapshotRevertSpec(captured, func() VMCreateSpec { v := current; v.ID = "other"; return v }()); err == nil {
		t.Fatal("revert accepted a captured spec for another VM")
	}
}

func TestVMSnapshotRevertRequestBindsCurrentTargetDescriptor(t *testing.T) {
	descriptor := PoolVMSnapshot{
		ID: "snap-old", VMID: "11111111-1111-4111-8111-111111111111", Name: "renamed-point",
		CreatedAt: time.Now().UTC(), SpecRevision: 2, SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
		Dependencies: &PoolVMSnapshotDependencies{StorageIDs: []string{"repo-1"}, NetworkIDs: []string{}, MediaIDs: []string{}},
	}
	request := VMSnapshotRequest{SnapshotID: descriptor.ID, Target: &descriptor}
	if err := request.ValidFor("snapshot-revert"); err != nil {
		t.Fatalf("valid revert target refused: %v", err)
	}
	if err := request.ValidFor("snapshot-delete"); err != nil {
		t.Fatalf("valid delete target refused: %v", err)
	}
	bad := request
	bad.Target = nil
	if bad.ValidFor("snapshot-revert") == nil {
		t.Fatal("revert target without its current catalog descriptor accepted")
	}
	if bad.ValidFor("snapshot-delete") == nil {
		t.Fatal("delete target without its current catalog descriptor accepted")
	}
	bad = request
	changed := descriptor
	changed.ID = "snap-other"
	bad.Target = &changed
	if bad.ValidFor("snapshot-revert") == nil {
		t.Fatal("descriptor ID mismatch accepted")
	}
	if request.Valid() == nil {
		t.Fatal("revert payload accepted as a create request")
	}
	withMode := request
	withMode.Mode = "crash"
	if withMode.ValidFor("snapshot-revert") == nil {
		t.Fatal("revert accepted a create mode")
	}
	if withMode.ValidFor("snapshot-delete") == nil {
		t.Fatal("delete accepted a create mode")
	}
	metadata := VMSnapshotRequest{SnapshotID: descriptor.ID, Name: "renamed-point", Description: "catalog note"}
	if err := metadata.ValidFor("snapshot-metadata"); err != nil {
		t.Fatalf("valid metadata update refused: %v", err)
	}
	metadata.Mode = "stopped"
	if metadata.ValidFor("snapshot-metadata") == nil {
		t.Fatal("metadata update accepted a create mode")
	}
}

func TestPoolVMRecoveryRevertReservationAndEvidenceAreBound(t *testing.T) {
	reservation := PoolVMRecoveryReservation{
		OperationID: "revert-op", Kind: "snapshot-revert", OwnerOperationOrder: 8, PoolID: "pool-1",
		VMID: "11111111-1111-4111-8111-111111111111", OwnerHostID: "host-a", Epoch: 2, SpecRevision: 3,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64), IntentSHA256: strings.Repeat("c", 64),
		TargetSnapshotID: "snap-old", TargetSnapshotSHA256: strings.Repeat("d", 64), ResultSpecSHA256: strings.Repeat("e", 64),
	}
	if err := reservation.Valid(); err != nil {
		t.Fatalf("valid revert reservation refused: %v", err)
	}
	result := validSpec()
	result.ID = reservation.VMID
	result.PoolID = reservation.PoolID
	result.HostID = reservation.OwnerHostID
	result.Revision = 4
	specSHA, _, err := snapshotSpecDigests(result)
	if err != nil {
		t.Fatal(err)
	}
	reservation.ResultSpecSHA256 = specSHA
	evidence := PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("f", 64), ResultSpec: &result}
	if err := evidence.Valid(); err != nil {
		t.Fatalf("valid revert result refused: %v", err)
	}
	changed := result
	changed.Name = "unrequested name"
	evidence.ResultSpec = &changed
	if evidence.Valid() == nil {
		t.Fatal("result spec differing from the reserved digest was accepted")
	}
	evidence = PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("f", 64)}
	if err := evidence.Valid(); err != nil {
		t.Fatalf("valid no-effect revert evidence refused: %v", err)
	}
	for name, mutate := range map[string]func(*PoolVMRecoveryReservation){
		"missing target":        func(r *PoolVMRecoveryReservation) { r.TargetSnapshotID = "" },
		"missing target digest": func(r *PoolVMRecoveryReservation) { r.TargetSnapshotSHA256 = "" },
		"missing result digest": func(r *PoolVMRecoveryReservation) { r.ResultSpecSHA256 = "" },
		"zero owner order":      func(r *PoolVMRecoveryReservation) { r.OwnerOperationOrder = 0 },
	} {
		bad := reservation
		mutate(&bad)
		if bad.Valid() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestPoolVMRecoveryDeleteReservationAndEvidenceAreBound(t *testing.T) {
	reservation := PoolVMRecoveryReservation{
		OperationID: "delete-op", Kind: "snapshot-delete", OwnerOperationOrder: 9, PoolID: "pool-1",
		VMID: "11111111-1111-4111-8111-111111111111", OwnerHostID: "host-a", Epoch: 2, SpecRevision: 3,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64), IntentSHA256: strings.Repeat("c", 64),
		TargetSnapshotID: "snap-old", TargetSnapshotSHA256: strings.Repeat("d", 64),
	}
	if err := reservation.Valid(); err != nil {
		t.Fatalf("valid delete reservation refused: %v", err)
	}
	evidence := PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "completed", JournalSHA256: strings.Repeat("f", 64)}
	if err := evidence.Valid(); err != nil {
		t.Fatalf("valid completed delete evidence refused: %v", err)
	}
	withSnapshot := evidence
	withSnapshot.Snapshot = &PoolVMSnapshot{
		ID: "snap-old", VMID: reservation.VMID, Name: "point", CreatedAt: time.Now().UTC(),
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64),
	}
	if withSnapshot.Valid() == nil {
		t.Fatal("completed delete accepted a catalog descriptor")
	}
	withResult := evidence
	result := validSpec()
	withResult.ResultSpec = &result
	if withResult.Valid() == nil {
		t.Fatal("completed delete accepted a result VM definition")
	}
	evidence = PoolVMRecoveryEvidence{Reservation: reservation, Outcome: "failed-before-effects", JournalSHA256: strings.Repeat("f", 64)}
	if err := evidence.Valid(); err != nil {
		t.Fatalf("valid no-effect delete evidence refused: %v", err)
	}
	for name, mutate := range map[string]func(*PoolVMRecoveryReservation){
		"missing target":        func(r *PoolVMRecoveryReservation) { r.TargetSnapshotID = "" },
		"missing target digest": func(r *PoolVMRecoveryReservation) { r.TargetSnapshotSHA256 = "" },
		"result digest present": func(r *PoolVMRecoveryReservation) { r.ResultSpecSHA256 = strings.Repeat("e", 64) },
		"zero owner order":      func(r *PoolVMRecoveryReservation) { r.OwnerOperationOrder = 0 },
		"same operation ID":     func(r *PoolVMRecoveryReservation) { r.OperationID = r.TargetSnapshotID },
	} {
		bad := reservation
		mutate(&bad)
		if bad.Valid() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func snapshotSpecDigests(spec VMCreateSpec) (string, string, error) {
	if err := ValidateVMSpec(spec); err != nil {
		return "", "", err
	}
	b, err := json.Marshal(spec)
	if err != nil {
		return "", "", err
	}
	d, err := json.Marshal(spec.Disks)
	if err != nil {
		return "", "", err
	}
	specHash, diskHash := sha256.Sum256(b), sha256.Sum256(d)
	return hex.EncodeToString(specHash[:]), hex.EncodeToString(diskHash[:]), nil
}
