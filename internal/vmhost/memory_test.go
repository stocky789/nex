// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/xml"
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
