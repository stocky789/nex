// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"reflect"
	"testing"
)

func TestUsableCPUModelsFiltersToYes(t *testing.T) {
	caps := []byte(`<domainCapabilities>
  <cpu>
    <mode name="host-passthrough" supported="yes"/>
    <mode name="host-model" supported="yes"/>
    <mode name="custom" supported="yes">
      <model usable="yes">Nehalem</model>
      <model usable="no">EPYC-Genoa</model>
      <model usable="yes">qemu64</model>
      <model usable="no">EPYC-Genoa</model>
      <model usable="yes">Nehalem</model>
    </mode>
  </cpu>
</domainCapabilities>`)
	got, err := UsableCPUModels(caps)
	if err != nil || !reflect.DeepEqual(got, []string{"Nehalem", "qemu64"}) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := UsableCPUModels([]byte(`<domainCapabilities><cpu><mode name="custom"><model usable="yes">bad/model</model></mode></cpu></domainCapabilities>`)); err == nil {
		t.Fatal("unsafe CPU model accepted")
	}
	if _, err := UsableCPUModels([]byte(`not-xml`)); err == nil {
		t.Fatal("invalid XML accepted")
	}
}
