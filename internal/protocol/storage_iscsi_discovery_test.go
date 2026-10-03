// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelectedISCSIDiscoveryValidatesTypedTargetAndCHAP(t *testing.T) {
	base := StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: StorageKindISCSI, Server: "iscsi.example.test", TargetIQN: "iqn.2026-09.test:chosen"}
	if err := ValidateStorageDiscovery(base); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"iqn.2026-09.test:chosen", "eui.0011223344556677", "naa.5001020304050607"} {
		v := base
		v.TargetIQN = target
		if err := ValidateStorageDiscovery(v); err != nil {
			t.Fatalf("valid target %q rejected: %v", target, err)
		}
	}
	for _, target := range []string{"chosen", "iqn.bad/target", "iqn.bad\\target", "iqn.bad\ttarget", "iqn.bad\nvalue", "iqn." + strings.Repeat("a", 220), "iqn." + strings.Repeat("a", 220) + "\x7f"} {
		v := base
		v.TargetIQN = target
		if err := ValidateStorageDiscovery(v); err == nil {
			t.Fatalf("invalid target %q accepted", target)
		}
	}
	for _, length := range []int{223, 224} {
		v := base
		v.TargetIQN = "iqn." + strings.Repeat("a", length-4)
		if err := ValidateStorageDiscovery(v); (err == nil) != (length == 223) {
			t.Fatalf("IQN length boundary wrong at %d bytes: %v", length, err)
		}
	}
	v := base
	v.Kind = StorageKindNFS
	if err := ValidateStorageDiscovery(v); err == nil {
		t.Fatal("selected iSCSI target accepted for another backend")
	}
	v = base
	v.Credentials = &StorageCredentials{Username: "chap", Secret: "fixture-secret"}
	if err := ValidateStorageDiscovery(v); err != nil {
		t.Fatal(err)
	}
	for _, length := range []int{255, 256} {
		v.Credentials.Secret = strings.Repeat("s", length)
		if err := ValidateStorageDiscovery(v); (err == nil) != (length == 255) {
			t.Fatalf("CHAP native buffer bound incorrectly validated at %d bytes", length)
		}
	}
	listing := v
	listing.TargetIQN = ""
	if err := ValidateStorageDiscovery(listing); err == nil {
		t.Fatal("CHAP accepted for target listing, which never logs in")
	}
	listing.Credentials = nil
	if err := ValidateStorageDiscovery(listing); err != nil {
		t.Fatal("native inspection limit incorrectly constrained target-only discovery")
	}
	v.Kind = StorageKindSMB
	if err := ValidateStorageDiscovery(v); err == nil {
		t.Fatal("an iSCSI target was accepted on SMB")
	}
	v.TargetIQN = ""
	if err := ValidateStorageDiscovery(v); err != nil {
		t.Fatal("iSCSI CHAP limit incorrectly constrained SMB credentials")
	}
	v = base
	for _, credentials := range []StorageCredentials{
		{Username: "chap"}, {Secret: "fixture-secret"}, {Username: "chap", Secret: "fixture-secret", Domain: "domain"},
		{Username: "chap", Secret: "bad\nsecret"}, {Username: "bad\x00user", Secret: "fixture-secret"},
		{Username: "chap", Secret: strings.Repeat("s", 256)},
	} {
		v.Credentials = &credentials
		if err := ValidateStorageDiscovery(v); err == nil {
			t.Fatal("invalid iSCSI credentials accepted")
		}
	}
}

func TestISCSILUNDiscoveryIncludesZeroWithoutInventedIdentity(t *testing.T) {
	data, err := json.Marshal(StorageDiscoveryResult{ISCSILUNs: []ISCSILUNDiscovery{{LUN: 0, Error: "No filesystem signature detected: blank or unsupported media"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"lun":0`) || strings.Contains(string(data), `"wwid"`) || strings.Contains(string(data), `"filesystem_uuid"`) || strings.Contains(string(data), `"credentials"`) {
		t.Fatalf("zero LUN or missing identity misrepresented: %s", data)
	}
}

func TestDiscoveryPurposeIsOnlyForNFSAndSMB(t *testing.T) {
	for kind, want := range map[string]bool{StorageKindNFS: true, StorageKindSMB: true, StorageKindISCSI: false, StorageKindLocal: false, StorageKindZFS: false, "zfs-device": false} {
		v := StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: kind, Server: "storage.example.test", Purpose: StorageDiscoveryPurposeBackup}
		if err := ValidateStorageDiscovery(v); (err == nil) != want {
			t.Fatalf("purpose on %s: %v", kind, err)
		}
	}
	v := StorageDiscoveryRequest{RequestID: "discover-1", PoolID: "pool-1", HostID: "host-1", Kind: StorageKindNFS, Server: "storage.example.test", Purpose: "other"}
	if ValidateStorageDiscovery(v) == nil {
		t.Fatal("unknown purpose accepted")
	}
}

func TestValidateStorageDiscoveryResultBoundsISCSIObservations(t *testing.T) {
	ok := StorageDiscoveryResult{Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{LUN: 0, WWID: "5001", FilesystemUUID: "12345678-1234-4234-8234-123456789012", FilesystemType: "ext4", Error: "fine"}, {LUN: 1}}}
	if err := ValidateStorageDiscoveryResult(ok); err != nil {
		t.Fatal(err)
	}
	many := func(n int) []ISCSILUNDiscovery {
		luns := make([]ISCSILUNDiscovery, n)
		for i := range luns {
			luns[i].LUN = uint64(i)
		}
		return luns
	}
	if ValidateStorageDiscoveryResult(StorageDiscoveryResult{Status: "succeeded", ISCSILUNs: many(64)}) != nil || ValidateStorageDiscoveryResult(StorageDiscoveryResult{Status: "succeeded", ISCSILUNs: many(65)}) == nil {
		t.Fatal("LUN count bound is not exactly 64")
	}
	for name, v := range map[string]StorageDiscoveryResult{
		"failed with luns":   {Status: "failed", ISCSILUNs: many(1)},
		"duplicate lun":      {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{LUN: 2}, {LUN: 2}}},
		"long wwid":          {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{WWID: strings.Repeat("a", 256)}}},
		"long uuid":          {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{FilesystemUUID: strings.Repeat("a", 256)}}},
		"long type":          {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{FilesystemType: strings.Repeat("a", 64)}}},
		"long lun error":     {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{Error: strings.Repeat("a", 512)}}},
		"control in uuid":    {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{FilesystemUUID: "a\nb"}}},
		"non-ascii in error": {Status: "succeeded", ISCSILUNs: []ISCSILUNDiscovery{{Error: "caf\u00e9"}}},
		"long result error":  {Status: "failed", Error: strings.Repeat("a", 512)},
	} {
		if ValidateStorageDiscoveryResult(v) == nil {
			t.Fatalf("%s accepted", name)
		}
	}
}

func TestEmptyISCSILUNListIsOmittedOnTheWire(t *testing.T) {
	for _, luns := range [][]ISCSILUNDiscovery{nil, {}} {
		data, err := json.Marshal(StorageDiscoveryResult{Status: "succeeded", ISCSILUNs: luns})
		if err != nil || strings.Contains(string(data), "iscsi_luns") {
			t.Fatalf("empty LUN list marshaled as %s (%v)", data, err)
		}
	}
}
