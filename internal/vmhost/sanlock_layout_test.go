// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/xml"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestSanlockLeasePathsAreStablePerVMAndRepository(t *testing.T) {
	root := t.TempDir()
	lockspace, err := SanlockLockspacePath(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".nex-sanlock", "lockspace"); lockspace != want {
		t.Fatalf("lockspace path = %q, want %q", lockspace, want)
	}
	vm1 := "12345678-1234-4234-8234-123456789012"
	first, err := SanlockVMResourcePath(root, vm1, "repo-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := SanlockVMResourcePath(root, vm1, "repo-a")
	if err != nil || first != second {
		t.Fatalf("resource path is not stable: %q, %q, %v", first, second, err)
	}
	otherRepo, err := SanlockVMResourcePath(root, vm1, "repo-b")
	if err != nil || first == otherRepo {
		t.Fatalf("different repository reused resource path: %q, %q, %v", first, otherRepo, err)
	}
	otherVM, err := SanlockVMResourcePath(root, "12345678-1234-4234-8234-123456789013", "repo-a")
	if err != nil || first == otherVM {
		t.Fatalf("different VM reused resource path: %q, %q, %v", first, otherVM, err)
	}
	if filepath.Dir(first) != filepath.Dir(lockspace) || first == lockspace {
		t.Fatalf("VM resource is not separate beside lockspace: %q, %q", first, lockspace)
	}
}

func TestManagedDomainXMLIncludesExplicitLeasesInOrder(t *testing.T) {
	vmID := "12345678-1234-4234-8234-123456789012"
	root := t.TempDir()
	repoA, err := SanlockVMResourcePath(root, vmID, "repo-a")
	if err != nil {
		t.Fatal(err)
	}
	repoB, err := SanlockVMResourcePath(root, vmID, "repo-b")
	if err != nil {
		t.Fatal(err)
	}
	v := protocol.VMCreateSpec{ID: vmID, Name: "lease-test", CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "maximum", SizeMiB: 512}, Boot: protocol.VMBoot{Firmware: "bios"}}
	leases := []managedLease{
		{Lockspace: "repository-a", Key: "vm-a", Target: sanlockLeaseTarget{Path: repoA}},
		{Lockspace: "repository-b", Key: "vm-b", Target: sanlockLeaseTarget{Path: repoB}},
	}
	body, err := managedDomainXML(v, nil, nil, nil, "sda", "", "sdb", "", leases)
	if err != nil {
		t.Fatal(err)
	}
	var domain managedDomain
	if err := xml.Unmarshal(body, &domain); err != nil {
		t.Fatal(err)
	}
	if len(domain.Leases) != 2 || domain.Leases[0] != leases[0] || domain.Leases[1] != leases[1] {
		t.Fatalf("domain leases = %#v, want %#v", domain.Leases, leases)
	}
	if strings.Index(string(body), "<lockspace>repository-a</lockspace>") > strings.Index(string(body), "<lockspace>repository-b</lockspace>") {
		t.Fatalf("lease acquisition order changed: %s", body)
	}
}

func TestSanlockLeasePathsRejectUntrustedComponents(t *testing.T) {
	for _, path := range []string{"", ".", "/", "relative", "/tmp/../repo"} {
		if _, err := SanlockLockspacePath(path); err == nil {
			t.Errorf("accepted repository path %q", path)
		}
	}
	for _, tc := range []struct{ vm, repo string }{
		{"not-a-uuid", "repo-a"},
		{"12345678-1234-4234-8234-123456789012", "../repo"},
	} {
		if _, err := SanlockVMResourcePath("/srv/repo", tc.vm, tc.repo); err == nil {
			t.Errorf("accepted VM/repository identity %q / %q", tc.vm, tc.repo)
		}
	}
}
