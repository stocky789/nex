// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestApplianceDomainUsesDefaultEmulator(t *testing.T) {
	j := journal{Operation: Operation{VMID: "11111111-1111-4111-8111-111111111111"}}
	b, err := xml.Marshal(makeDomain(j, DeploySpec{CPUs: 2, MemoryMiB: 4096}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "<emulator>") {
		t.Fatalf("appliance overrides libvirt's standard emulator: %s", b)
	}
	if !strings.Contains(string(b), `type="vnc"`) || !strings.Contains(string(b), `<listen type="socket"`) {
		t.Fatalf("appliance lost its host-local VNC console: %s", b)
	}
}
