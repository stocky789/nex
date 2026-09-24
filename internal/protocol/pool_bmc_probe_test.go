// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "testing"

func TestPoolRequestBMCProbeValidation(t *testing.T) {
	valid := PoolRequest{RequestID: "probe-1", PoolID: "pool-1", HostID: "master-1", Action: "bmc-probe",
		MemberHostID: "member-1", Endpoint: "192.0.2.12", Username: "nex-fence", CredentialRef: "cred-1"}
	if err := valid.Valid(); err != nil {
		t.Fatal(err)
	}
	anonymous := valid
	anonymous.Username, anonymous.CredentialRef = "", ""
	if err := anonymous.Valid(); err != nil {
		t.Fatalf("a probe without an account was refused: %v", err)
	}
	for name, mutate := range map[string]func(*PoolRequest){
		"no member":             func(r *PoolRequest) { r.MemberHostID = "" },
		"no endpoint":           func(r *PoolRequest) { r.Endpoint = "" },
		"endpoint path":         func(r *PoolRequest) { r.Endpoint = "192.0.2.12/redfish" },
		"account without ref":   func(r *PoolRequest) { r.CredentialRef = "" },
		"ref without account":   func(r *PoolRequest) { r.Username = "" },
		"unsafe ref":            func(r *PoolRequest) { r.CredentialRef = "../cred" },
		"master url":            func(r *PoolRequest) { r.MasterURL = "https://master.example.test" },
		"unknown action":        func(r *PoolRequest) { r.Action = "bmc-scan" },
		"probe fields on other": func(r *PoolRequest) { r.Action = "promotion-preflight" },
		"long request id":       func(r *PoolRequest) { r.RequestID = string(make([]byte, 129)) },
	} {
		request := valid
		mutate(&request)
		if request.Valid() == nil {
			t.Errorf("%s: accepted %#v", name, request)
		}
	}
}

func TestPoolBMCProbeResultValidation(t *testing.T) {
	valid := PoolBMCProbeResult{
		Methods: []PoolBMCProbeMethod{
			{Kind: FenceRedfish, Reachable: true, TargetIDs: []string{"System.Embedded.1"}},
			{Kind: FenceIPMI, Detail: "the management controller did not answer IPMI over LAN with this account"},
		},
		Recommended: &PoolBMCProbeChoice{Kind: FenceRedfish, TargetID: "System.Embedded.1", Reason: "this system reports this host's hardware identity"},
	}
	if err := valid.Valid(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*PoolBMCProbeResult){
		"unknown kind":             func(r *PoolBMCProbeResult) { r.Methods[1].Kind = "virsh" },
		"duplicate kind":           func(r *PoolBMCProbeResult) { r.Methods[1].Kind = FenceRedfish },
		"bad target":               func(r *PoolBMCProbeResult) { r.Methods[0].TargetIDs = []string{"../1"} },
		"targets when unreachable": func(r *PoolBMCProbeResult) { r.Methods[1].TargetIDs = []string{"1"} },
		"too many targets":         func(r *PoolBMCProbeResult) { r.Methods[0].TargetIDs = make([]string, PoolBMCProbeMaxTargets+1) },
		"unreachable choice":       func(r *PoolBMCProbeResult) { r.Recommended.Kind = FenceIPMI },
		"no reason":                func(r *PoolBMCProbeResult) { r.Recommended.Reason = "" },
		"bad choice target":        func(r *PoolBMCProbeResult) { r.Recommended.TargetID = "" },
	} {
		result := PoolBMCProbeResult{Methods: append([]PoolBMCProbeMethod(nil), valid.Methods...)}
		choice := *valid.Recommended
		result.Recommended = &choice
		mutate(&result)
		if result.Valid() == nil {
			t.Errorf("%s: accepted %#v", name, result)
		}
	}
}
