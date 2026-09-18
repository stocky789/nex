// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestStorageStartupGuardRefusesUnavailableAndForeignDisks(t *testing.T) {
	var d domainXML
	if err := xml.Unmarshal([]byte(`<domain><uuid>11111111-1111-4111-8111-111111111111</uuid><devices><disk type="file" device="disk"><source file="/var/lib/storage/nex-vms/11111111-1111-4111-8111-111111111111/disk-0.qcow2"/></disk></devices></domain>`), &d); err != nil {
		t.Fatal(err)
	}
	s := protocol.StorageRegistration{ID: "repo-one", Kind: "local", Path: "/var/lib/storage", Generation: 1}
	js := []managedJournal{{Command: protocol.Command{Kind: "storage-register", Storage: &s}, Result: protocol.Result{Status: "succeeded"}}}
	calls := 0
	verify := func(_ context.Context, got protocol.StorageRegistration) error {
		calls++
		if got.ID != s.ID {
			t.Fatalf("wrong repository: %s", got.ID)
		}
		return nil
	}
	if err := checkStorageStartup(context.Background(), d, js, verify); err != nil || calls != 1 {
		t.Fatalf("ready repository: %v, probes %d", err, calls)
	}
	if err := checkStorageStartup(context.Background(), d, js, func(context.Context, protocol.StorageRegistration) error { return errors.New("mount missing") }); err == nil {
		t.Fatal("unavailable repository admitted at libvirt startup")
	}
	if err := checkStorageStartup(context.Background(), d, nil, verify); err == nil {
		t.Fatal("unregistered managed disk admitted")
	}
	d.UUID = "22222222-2222-4222-8222-222222222222"
	if err := checkStorageStartup(context.Background(), d, js, verify); err == nil {
		t.Fatal("another VM's disk admitted")
	}
	for _, invalid := range []string{"", "not-a-uuid", ".."} {
		d.UUID = invalid
		if err := checkStorageStartup(context.Background(), d, js, verify); err == nil {
			t.Fatal("disk admitted without valid VM identity")
		}
	}
	d.UUID = "11111111-1111-4111-8111-111111111111"
	d.Devices.Disks[0].Source.File = "/var/lib/storage-other/nex-vms/11111111-1111-4111-8111-111111111111/disk.qcow2"
	if err := checkStorageStartup(context.Background(), d, js, verify); err == nil {
		t.Fatal("repository path prefix collision admitted")
	}
	js = append(js, managedJournal{Command: protocol.Command{VM: &protocol.VMCreateSpec{ID: d.UUID}}})
	d.Devices.Disks[0].Source.File = "/var/lib/unmanaged/disk.qcow2"
	if err := checkStorageStartup(context.Background(), d, js, verify); err == nil {
		t.Fatal("managed VM bypassed checks by changing its disk path")
	}
}

func TestStorageStartupGuardUsesMostSpecificRepository(t *testing.T) {
	const vm = "11111111-1111-4111-8111-111111111111"
	parent := protocol.StorageRegistration{ID: "parent", Kind: "local", Path: "/var/lib/libvirt/images", Generation: 1}
	nested := protocol.StorageRegistration{ID: "nested", Kind: "local", Path: "/var/lib/libvirt/images/placement-lab", Generation: 1}
	js := []managedJournal{
		{Command: protocol.Command{Kind: "storage-register", Storage: &parent}, Result: protocol.Result{Status: "succeeded"}},
		{Command: protocol.Command{Kind: "storage-register", Storage: &nested}, Result: protocol.Result{Status: "succeeded"}},
	}
	path := "/var/lib/libvirt/images/placement-lab/nex-vms/" + vm + "/disk-0.qcow2"
	repository, err := storageStartupRepository(path, storageProjection(js))
	if err != nil || repository.ID != nested.ID {
		t.Fatalf("nested repository = %#v, %v", repository, err)
	}
	if repository, err = storageStartupRepository("/var/lib/libvirt/images/nex-vms/"+vm+"/disk-0.qcow2", storageProjection(js)); err != nil || repository.ID != parent.ID {
		t.Fatalf("parent repository = %#v, %v", repository, err)
	}
	if repository, err = storageStartupRepository(path, map[string]protocol.StorageRegistration{
		"parent-one": {ID: "parent-one", Path: parent.Path},
		"parent-two": {ID: "parent-two", Path: parent.Path},
		"nested":     nested,
	}); err != nil || repository.ID != nested.ID {
		t.Fatalf("nested repository with ambiguous parent = %#v, %v", repository, err)
	}
	if _, err = storageStartupRepository(path, map[string]protocol.StorageRegistration{
		"one": {ID: "one", Path: nested.Path},
		"two": {ID: "two", Path: nested.Path},
	}); err == nil {
		t.Fatal("ambiguous equal repository roots admitted")
	}

	var d domainXML
	if err = xml.Unmarshal([]byte(`<domain><uuid>`+vm+`</uuid><devices><disk type="file" device="disk"><source file="`+path+`"/></disk></devices></domain>`), &d); err != nil {
		t.Fatal(err)
	}
	verified := []string{}
	if err = checkStorageStartup(context.Background(), d, js, func(_ context.Context, got protocol.StorageRegistration) error {
		verified = append(verified, got.ID)
		return nil
	}); err != nil || len(verified) != 1 || verified[0] != nested.ID {
		t.Fatalf("nested startup = %v, verified %v", err, verified)
	}
	d.UUID = "22222222-2222-4222-8222-222222222222"
	if err = checkStorageStartup(context.Background(), d, js, func(context.Context, protocol.StorageRegistration) error { return nil }); err == nil {
		t.Fatal("nested repository admitted another VM disk")
	}
	d.UUID = vm
	if err = checkStorageStartup(context.Background(), d, js, func(_ context.Context, got protocol.StorageRegistration) error {
		if got.ID != nested.ID {
			t.Fatalf("unavailable check used parent repository %q", got.ID)
		}
		return errors.New("nested repository unavailable")
	}); err == nil {
		t.Fatal("unavailable nested repository admitted through its parent")
	}
}

func TestStorageStartupDiskRefusesAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(path, []byte("disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := startupDiskFile(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, path+"-symlink"); err != nil {
		t.Fatal(err)
	}
	if err := startupDiskFile(path + "-symlink"); err == nil {
		t.Fatal("symlink admitted")
	}
	if err := os.Link(path, path+"-hardlink"); err != nil {
		t.Fatal(err)
	}
	if err := startupDiskFile(path); err == nil {
		t.Fatal("hardlink alias admitted")
	}
}

func TestPoolVMStartPermitIsSingleUse(t *testing.T) {
	state := t.TempDir()
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := consumePoolVMStartPermit(state, vm); err == nil {
		t.Fatal("shared VM started without a Core permit")
	}
	if err := WritePoolVMStartPermit(state, vm, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if err := consumePoolVMStartPermit(state, vm); err != nil {
		t.Fatal(err)
	}
	if err := consumePoolVMStartPermit(state, vm); err == nil {
		t.Fatal("stale permit admitted a second local/autostart execution")
	}
	if err := WritePoolVMStartPermit(state, vm, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(poolVMStartPermitPath(state, vm), []byte(`{"VMID":"`+vm+`","Permit":"`+strings.Repeat("b", 64)+`","BootID":"11111111-1111-4111-8111-111111111111"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := consumePoolVMStartPermit(state, vm); err == nil {
		t.Fatal("permit from another boot was admitted")
	}
}

func TestPoolVMStartPermitRejectsSameBootStaleEpoch(t *testing.T) {
	state := t.TempDir()
	const vm = "11111111-1111-4111-8111-111111111111"
	if err := WritePoolVMStartPermitAdmission(state, "pool-1", "host-a", vm, 1, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	stale, err := os.ReadFile(poolVMStartPermitPath(state, vm))
	if err != nil {
		t.Fatal(err)
	}
	if err := WritePoolVMStartPermitAdmission(state, "pool-1", "host-a", vm, 2, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	// Simulate a same-boot process crash restoring an older private permit after
	// the master has issued a newer epoch.  The current admission record wins.
	if err := os.WriteFile(poolVMStartPermitPath(state, vm), stale, 0600); err != nil {
		t.Fatal(err)
	}
	if err := consumePoolVMStartPermit(state, vm); err == nil {
		t.Fatal("same-boot stale epoch permit was admitted")
	}
}

func TestStorageGuardPreservesIndependentApplianceWithoutJournal(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	if err := StorageGuard(ctx, state, strings.NewReader(`<domain><uuid>appliance</uuid><devices><disk type="file" device="disk"><source file="/var/lib/libvirt/images/appliance/controller.qcow2"/></disk></devices></domain>`)); err != nil {
		t.Fatal(err)
	}
	if err := StorageGuard(ctx, state, strings.NewReader(`<domain><uuid>11111111-1111-4111-8111-111111111111</uuid><devices><disk type="file" device="disk"><source file="/var/lib/storage/nex-vms/11111111-1111-4111-8111-111111111111/disk.qcow2"/></disk></devices></domain>`)); err == nil {
		t.Fatal("managed disk started after journal loss")
	}
	for _, input := range []string{`<domain`, `<other/>`, strings.Repeat("x", (1<<20)+1)} {
		if err := StorageGuard(ctx, state, strings.NewReader(input)); err == nil {
			t.Fatal("invalid domain input admitted")
		}
	}
}
