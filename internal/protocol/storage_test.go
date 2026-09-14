// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func storageRegistration(kind string) StorageRegistration {
	s := StorageRegistration{ID: "repo-1", PoolID: "pool-1", HostID: "host-1", Name: "VMs", Kind: kind, Generation: 1}
	switch kind {
	case StorageKindLocal:
		s.Path = "/var/lib/nex/vms"
	case StorageKindNFS:
		s.Source.NFS = &NFSStorageSource{Server: "nfs.example.test", Export: "/exports/vms"}
	case StorageKindZFS:
		s.Source.ZFS = &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vms"}
	case StorageKindSMB:
		s.Source.SMB = &SMBStorageSource{Server: "files.example.test", Share: "vms"}
	case StorageKindISCSI:
		s.Source.ISCSI = &ISCSIStorageSource{Portal: "iscsi.example.test:3260", TargetIQN: "iqn.2026-09.test:nex.vms", LUN: 0, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "11111111-2222-4333-8444-555555555555", ExclusiveTarget: true}
	}
	return s
}

func TestValidateStorageRegistration(t *testing.T) {
	for _, kind := range []string{StorageKindLocal, StorageKindNFS, StorageKindZFS, StorageKindSMB, StorageKindISCSI} {
		t.Run(kind, func(t *testing.T) {
			if err := ValidateStorageRegistration(storageRegistration(kind), false); err != nil {
				t.Fatalf("valid %s repository rejected: %v", kind, err)
			}
		})
	}
	legacy := StorageRegistration{ID: "old-local", Path: "/var/lib/libvirt/images", OperationID: "old-operation"}
	if legacy.StorageKind() != StorageKindLocal || ValidateStorageRegistration(legacy, false) != nil {
		t.Fatal("v1 path-only local journal was not accepted")
	}
	localWithSpace := legacy
	localWithSpace.Path = "/var/lib/libvirt/vm images"
	if err := ValidateStorageRegistration(localWithSpace, false); err != nil {
		t.Fatalf("ZFS mount validation changed local path behavior: %v", err)
	}
	stored := storageRegistration(StorageKindNFS)
	stored.Path = "/var/lib/nex-storage/repo-1"
	if ValidateStorageRegistration(stored, false) == nil || ValidateStoredStorageRegistration(stored, false) != nil {
		t.Fatal("resolved Core mount path did not stay separate from registration input")
	}
	storedISCSI := storageRegistration(StorageKindISCSI)
	storedISCSI.Source.ISCSI.MountPath = "/var/lib/nex-storage/repo-1"
	storedISCSI.Path = storedISCSI.Source.ISCSI.MountPath
	if ValidateStorageRegistration(storedISCSI, false) == nil || ValidateStoredStorageRegistration(storedISCSI, false) != nil {
		t.Fatal("iSCSI mount path was accepted from client or rejected from Core state")
	}
	cases := []struct {
		name string
		edit func(*StorageRegistration)
	}{
		{"unknown kind", func(s *StorageRegistration) { s.Kind = "ceph" }},
		{"local root", func(s *StorageRegistration) { s.Path = "/" }},
		{"two sources", func(s *StorageRegistration) {
			s.Source.NFS = &NFSStorageSource{Server: "nfs.example.test", Export: "/vms"}
		}},
		{"nfs traversal", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindNFS, "", StorageSource{NFS: &NFSStorageSource{Server: "nfs.example.test", Export: "/vms/../bad"}}
		}},
		{"smb shell fragment", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindSMB, "", StorageSource{SMB: &SMBStorageSource{Server: "files.example.test;bad", Share: "vms"}}
		}},
		{"iscsi missing filesystem", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindISCSI, "", StorageSource{ISCSI: &ISCSIStorageSource{Portal: "iscsi.example.test:3260", TargetIQN: "iqn.2026-09.test:vms", MountPath: "/mnt/vms"}}
		}},
		{"zfs unit percent", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/%vms"}}
		}},
		{"zfs unit backslash", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/\\vms"}}
		}},
		{"zfs unit whitespace", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vm store"}}
		}},
		{"zfs unit control", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vm\tstore"}}
		}},
		{"zfs unit escape", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vm\x1bstore"}}
		}},
		{"zfs unit vertical tab", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vm\vstore"}}
		}},
		{"zfs unit delete", func(s *StorageRegistration) {
			s.Kind, s.Path, s.Source = StorageKindZFS, "", StorageSource{ZFS: &ZFSStorageSource{Pool: "tank", Dataset: "tank/vms", MountPath: "/tank/vm\x7fstore"}}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := storageRegistration(StorageKindLocal)
			c.edit(&s)
			if err := ValidateStorageRegistration(s, false); err == nil {
				t.Fatal("invalid storage registration accepted")
			}
		})
	}
}

func TestStorageSubdirectoryValidationAllowsNamesAndRejectsTraversalControls(t *testing.T) {
	for _, directory := range []string{"discovered", "team/vms", "Team VMs/2026", "r-n-x-0"} {
		if !storageSubdirectory(directory) {
			t.Fatalf("safe directory %q rejected", directory)
		}
	}
	for _, directory := range []string{".", "..", "../x", "x/../y", "/x", "x\\y", "x\x00y", "x\ny", "x\ry", "x\ty"} {
		if storageSubdirectory(directory) {
			t.Fatalf("unsafe directory %q accepted", directory)
		}
	}
}

func TestStorageRegistrationAllowsSMBNamesWithSpacesAndHiddenSuffix(t *testing.T) {
	s := storageRegistration(StorageKindSMB)
	s.Source.SMB.Share = "VM Files$"
	s.Source.SMB.Directory = "Team VMs/2026"
	if err := ValidateStorageRegistration(s, false); err != nil {
		t.Fatalf("safe SMB name rejected: %v", err)
	}
	for _, share := range []string{"../vms", "vms/share", "vms\\share", "vms%share", "vms\nshare", " VM Files$", "VM Files$ ", `VM "Files$"`} {
		s.Source.SMB.Share = share
		if err := ValidateStorageRegistration(s, false); err == nil {
			t.Fatalf("unsafe SMB share accepted: %q", share)
		}
	}
}

func TestValidateStorageDiscoveryAllowsSelectedNamesWithSpaces(t *testing.T) {
	for _, v := range []StorageDiscoveryRequest{
		{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: StorageKindNFS, Server: "nfs.example.test", Path: "/Team Shares/VM Disks"},
		{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: StorageKindSMB, Server: "files.example.test", Path: "/Team Shares$/VM Disks"},
	} {
		if err := ValidateStorageDiscovery(v); err != nil {
			t.Fatalf("safe selected path rejected: %v", err)
		}
	}
}

func TestValidateStorageLifecycle(t *testing.T) {
	base := StorageLifecycleRequest{RepositoryID: "repo-1", PoolID: "pool-1", HostID: "host-1"}
	for _, c := range []struct {
		name string
		v    StorageLifecycleRequest
		ok   bool
	}{
		{"update", StorageLifecycleRequest{Action: "update", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID, ExpectedGeneration: 1, Registration: ptr(storageRegistration(StorageKindNFS))}, true},
		{"recheck", StorageLifecycleRequest{Action: "recheck", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID}, true},
		{"detach", StorageLifecycleRequest{Action: "detach", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID, ExpectedGeneration: 1}, true},
		{"recheck changes config", StorageLifecycleRequest{Action: "recheck", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID, Registration: ptr(storageRegistration(StorageKindNFS))}, false},
		{"detach no generation", StorageLifecycleRequest{Action: "detach", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID}, false},
		{"register is legacy command", StorageLifecycleRequest{Action: "register", RepositoryID: base.RepositoryID, PoolID: base.PoolID, HostID: base.HostID}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := ValidateStorageLifecycle(c.v)
			if (err == nil) != c.ok {
				t.Fatalf("valid=%t, err=%v", c.ok, err)
			}
		})
	}
}

func ptr[T any](v T) *T { return &v }

func TestStorageCredentialsAreNeverInCommand(t *testing.T) {
	handoff, err := json.Marshal(StorageCredentialHandoff{Credentials: StorageCredentials{Username: "operator", Secret: "secret"}})
	if err != nil || !strings.Contains(string(handoff), "secret") {
		t.Fatal("transient handoff did not preserve credential material")
	}
	command, err := json.Marshal(Command{Kind: "storage-register", Storage: &StorageRegistration{ID: "repo", CredentialReceipt: "receipt"}})
	if err != nil || strings.Contains(string(command), "secret") || !strings.Contains(string(command), "credential_receipt") {
		t.Fatalf("ordinary command exposes credential material: %s", command)
	}
}

func TestValidateStorageCredentialRetire(t *testing.T) {
	if err := ValidateStorageCredentialRetire(StorageCredentialRetireRequest{Receipt: "receipt-1", PoolID: "pool-1", HostID: "host-1"}); err != nil {
		t.Fatalf("valid credential retirement rejected: %v", err)
	}
	if err := ValidateStorageCredentialRetire(StorageCredentialRetireRequest{Receipt: "receipt/1", PoolID: "pool-1", HostID: "host-1"}); err == nil {
		t.Fatal("unsafe credential receipt accepted")
	}
	command, err := json.Marshal(Command{Kind: "storage-credential-retire", CredentialRetire: &StorageCredentialRetireRequest{Receipt: "receipt-1", PoolID: "pool-1", HostID: "host-1"}})
	if err != nil || strings.Contains(string(command), "secret") {
		t.Fatalf("credential retirement command carries secret material: %s", command)
	}
}

func TestStorageDiscoveryIsScopedAndNeverCarriesSecretsInResults(t *testing.T) {
	v := StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: StorageKindSMB, Server: "files.example.test", Credentials: &StorageCredentials{Username: "operator", Secret: "secret"}}
	if err := ValidateStorageDiscovery(v); err != nil {
		t.Fatal(err)
	}
	v.Server = "bad;server"
	if err := ValidateStorageDiscovery(v); err == nil {
		t.Fatal("unsafe discovery server accepted")
	}
	b, err := json.Marshal(StorageDiscoveryResult{RequestID: "discover-1", Status: "succeeded", Candidates: []StorageDiscoveryCandidate{{Kind: StorageKindSMB, Source: StorageSource{SMB: &SMBStorageSource{Server: "files.example.test", Share: "vms"}}}}})
	if err != nil || strings.Contains(string(b), "secret") {
		t.Fatalf("discovery result leaks a credential: %s", b)
	}
}

func TestCredentialReceiptRetirementInventoryIsRedacted(t *testing.T) {
	b, err := json.Marshal(StorageCredentialReceipt{Receipt: "receipt-1", RetireEligible: true})
	if err != nil || !strings.Contains(string(b), "retire_eligible") || strings.Contains(string(b), "secret") {
		t.Fatalf("receipt inventory is not safely redacted: %s", b)
	}
}

func TestSMBMarkerIdentityIsCoreObserved(t *testing.T) {
	stored := storageRegistration(StorageKindSMB)
	stored.Identity.SMB = &SMBStorageIdentity{Server: "files.example.test", Share: "vms"}
	if err := ValidateStoredStorageRegistration(stored, false); err != nil {
		t.Fatalf("legacy SMB identity without marker rejected: %v", err)
	}
	stored.Identity.SMB.MarkerID = "0123456789abcdef0123456789abcdef"
	if err := ValidateStoredStorageRegistration(stored, false); err != nil {
		t.Fatalf("valid Core SMB marker rejected: %v", err)
	}
	stored.Identity.SMB.MarkerID = "0123456789ABCDEF0123456789abcdef"
	if err := ValidateStoredStorageRegistration(stored, false); err == nil {
		t.Fatal("invalid SMB marker accepted")
	}
	stored.Identity.SMB.MarkerID = "0123456789abcdef0123456789abcdef"
	if err := ValidateStorageRegistration(stored, false); err == nil {
		t.Fatal("client registration supplied Core-observed SMB identity")
	}
}
