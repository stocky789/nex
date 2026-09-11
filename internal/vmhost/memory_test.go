// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestManagedDedicatedMemoryReservation(t *testing.T) {
	v := managedTestVM()
	v.Memory = protocol.VMMemory{Mode: "dedicated", SizeMiB: 4096}
	wanted, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var backing struct {
		Backing *struct {
			Locked     *struct{} `xml:"locked"`
			NoShare    *struct{} `xml:"nosharepages"`
			Allocation struct {
				Mode string `xml:"mode,attr"`
			} `xml:"allocation"`
		} `xml:"memoryBacking"`
	}
	if err = xml.Unmarshal(wanted, &backing); err != nil || backing.Backing == nil || backing.Backing.Locked == nil || backing.Backing.NoShare == nil || backing.Backing.Allocation.Mode != "immediate" {
		t.Fatalf("dedicated RAM must be preallocated, locked and unshared: %s (%v)", wanted, err)
	}
	for _, tc := range []struct{ name, old, replacement string }{
		{"lock removed", "<locked></locked>", ""},
		{"sharing enabled", "<nosharepages></nosharepages>", ""},
		{"lazy allocation", `mode="immediate"`, `mode="ondemand"`},
		{"allocation omitted", `<allocation mode="immediate"></allocation>`, ""},
		{"guest allocation reduced", `<currentMemory unit="MiB">4096</currentMemory>`, `<currentMemory unit="MiB">1024</currentMemory>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := strings.Replace(string(wanted), tc.old, tc.replacement, 1)
			if changed == string(wanted) {
				t.Fatal("fixture did not mutate")
			}
			if validateManagedDefinition([]byte(changed), wanted) == nil {
				t.Fatal("weakened reservation accepted")
			}
		})
	}
	normalized := strings.ReplaceAll(string(wanted), `unit="MiB">4096`, `unit="KiB">4194304`)
	if err = validateManagedDefinition([]byte(normalized), wanted); err != nil {
		t.Fatalf("libvirt normalization rejected: %v", err)
	}
	v.Memory.Mode = "maximum"
	maximum, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk"}, "")
	if err != nil || strings.Contains(string(maximum), "memoryBacking") {
		t.Fatalf("maximum RAM must remain reclaimable: %s (%v)", maximum, err)
	}
}

func TestManagedLegacyDedicatedLiveEditRequiresStop(t *testing.T) {
	v := managedTestVM()
	v.Memory.Mode = "dedicated"
	old, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	// This is a definition written before physical reservation was implemented.
	legacy := string(old)
	if start := strings.Index(legacy, "<memoryBacking>"); start >= 0 {
		end := strings.Index(legacy, "</memoryBacking>") + len("</memoryBacking>")
		legacy = legacy[:start] + legacy[end:]
	}
	dir := t.TempDir()
	path, calls := filepath.Join(dir, "domain.xml"), filepath.Join(dir, "calls")
	if err = os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MEMORY_DOMAIN", path)
	t.Setenv("MEMORY_CALLS", calls)
	fakeCommand(t, "virsh", `echo "$3" >> "$MEMORY_CALLS"; case "$3" in domstate) echo running;; dumpxml) cat "$MEMORY_DOMAIN";; *) exit 1;; esac`)
	e := NewManaged(dir)
	cur := managedJournal{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v}, Result: protocol.Result{Status: "succeeded"}, Domain: []byte(legacy), Disks: []string{"/disk"}}
	next := v
	next.Revision++
	next.Description = "Updated description"
	j := managedJournal{Command: protocol.Command{OperationID: "edit", Kind: "vm-update", VM: &next}}
	err = e.update(context.Background(), &j, []managedJournal{cur})
	if err == nil || !strings.Contains(err.Error(), "stop the VM and save") {
		t.Fatalf("legacy live edit must request stopped save before effects: %v", err)
	}
	got, _ := os.ReadFile(calls)
	if string(got) != "domstate\ndumpxml\n" {
		t.Fatalf("legacy live edit caused unexpected calls: %s", got)
	}
}
