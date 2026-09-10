// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"

	"nex.local/nex/internal/protocol"
)

// UsableCPUModels returns the named custom CPU models libvirt marks usable=yes for
// this host in domain capabilities. That is the set Nex offers for guest CPU selection:
// virsh cpu-models lists every QEMU name, including baselines that cannot start here.
func UsableCPUModels(domcaps []byte) ([]string, error) {
	var caps struct {
		CPU struct {
			Modes []struct {
				Name   string `xml:"name,attr"`
				Models []struct {
					Usable string `xml:"usable,attr"`
					Name   string `xml:",chardata"`
				} `xml:"model"`
			} `xml:"mode"`
		} `xml:"cpu"`
	}
	if err := xml.Unmarshal(domcaps, &caps); err != nil {
		return nil, fmt.Errorf("invalid domain capabilities")
	}
	seen := map[string]bool{}
	for _, mode := range caps.CPU.Modes {
		if mode.Name != "custom" {
			continue
		}
		for _, model := range mode.Models {
			name := strings.TrimSpace(model.Name)
			if model.Usable != "yes" || name == "" {
				continue
			}
			if !protocol.ValidCPUModel(name) {
				return nil, fmt.Errorf("invalid CPU model from libvirt")
			}
			seen[name] = true
		}
	}
	if len(seen) > 256 {
		return nil, fmt.Errorf("CPU model limit exceeded")
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}
