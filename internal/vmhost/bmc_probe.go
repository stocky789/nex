// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Discovery answers the questions Set BMC access would otherwise put to an operator — does
// this management controller speak Redfish or IPMI, and which system on it is this host — from
// the pool master, the host that will fence through it. It records nothing: no profile, no
// certificate pin, no membership. Set BMC access that follows pins and proves the controller
// exactly as it always has.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"

	"nex.local/nex/internal/protocol"
)

// ProbeBMC asks one management controller how it answers and recommends the profile to
// record. Every method is reported; the error, when nothing can be recommended, is the one
// sentence an operator acts on.
func ProbeBMC(ctx context.Context, endpoint, username, secret, memberSystemUUID string) (protocol.PoolBMCProbeResult, error) {
	redfish, choice := probeRedfish(ctx, endpoint, username, secret, NormalizedSystemUUID(memberSystemUUID))
	ipmi := probeIPMI(ctx, endpoint, username, secret)
	result := protocol.PoolBMCProbeResult{Methods: []protocol.PoolBMCProbeMethod{redfish, ipmi}}
	switch {
	case choice != nil:
		// Redfish wins whenever it names the system: its identity read is exact, and it is
		// the method whose certificate Set BMC access pins.
		result.Recommended = choice
	case ipmi.Reachable:
		result.Recommended = &protocol.PoolBMCProbeChoice{Kind: protocol.FenceIPMI, TargetID: "1",
			Reason: "the management controller answered IPMI over LAN with this account"}
	case redfish.Reachable && secret == "":
		return result, errors.New("the management controller answered; enter its account and password so Nex Core can find which system is this host")
	case redfish.Reachable:
		return result, errors.New(redfish.Detail)
	case secret != "":
		return result, errors.New("the management controller did not answer Redfish or IPMI over LAN with this account; check the address, the account, and that network access is turned on in the controller")
	default:
		return result, errors.New("no management controller answered Redfish at this address; check the address, or enter the controller's account and password to try IPMI over LAN")
	}
	return result, nil
}

// probeRedfish reads the service root without an account, which is enough to know Redfish
// answers, and with one lists the systems and reads each one's identity. Every read goes over
// one connection pinned to the certificate presented first, so the listing and the identities
// all come from the same controller.
func probeRedfish(ctx context.Context, endpoint, username, secret, want string) (protocol.PoolBMCProbeMethod, *protocol.PoolBMCProbeChoice) {
	method := protocol.PoolBMCProbeMethod{Kind: protocol.FenceRedfish}
	fingerprint, err := BMCCertificateFingerprint(ctx, endpoint)
	if err != nil {
		method.Detail = "nothing answered HTTPS at this address"
		return method, nil
	}
	b, err := openBMCSession(protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: endpoint, Username: username, TLSFingerprint: fingerprint}, secret)
	if err != nil {
		method.Detail = err.Error()
		return method, nil
	}
	defer b.close()
	if !redfishServiceRoot(ctx, b.client, endpoint) {
		method.Detail = "this address answers HTTPS, but not as a Redfish service"
		return method, nil
	}
	method.Reachable = true
	if secret == "" {
		method.Detail = "Redfish answered; its account is needed to list the systems it manages"
		return method, nil
	}
	ids, err := redfishSystemIDs(ctx, b)
	if errors.Is(err, errBMCRejected) {
		method.Detail = "the management controller rejected this account and password"
		return method, nil
	}
	if err != nil {
		method.Detail = err.Error()
		return method, nil
	}
	method.TargetIDs = ids
	if len(ids) == 0 {
		method.Detail = "the management controller lists no system Nex Core can fence"
		return method, nil
	}
	var reported string
	for _, id := range ids {
		b.fence.TargetID = id
		if reported, _, err = b.system(ctx); err == nil && want != "" && reported == want {
			return method, &protocol.PoolBMCProbeChoice{Kind: protocol.FenceRedfish, TargetID: id,
				Reason: "this system reports this host's hardware identity"}
		}
	}
	switch {
	case len(ids) > 1:
		method.Detail = "the management controller manages several systems and none reports this host's hardware identity"
	case err == nil && want != "":
		method.Detail = "the one system this management controller manages reports a different machine than this host"
	default:
		return method, &protocol.PoolBMCProbeChoice{Kind: protocol.FenceRedfish, TargetID: ids[0],
			Reason: "this is the one system the management controller manages"}
	}
	return method, nil
}

// redfishServiceRoot is the unauthenticated read every Redfish service must answer.
func redfishServiceRoot(ctx context.Context, client *http.Client, endpoint string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, (&url.URL{Scheme: "https", Host: endpoint, Path: "/redfish/v1"}).String(), nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var root struct {
		RedfishVersion string `json:"RedfishVersion"`
	}
	return resp.StatusCode == http.StatusOK && json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&root) == nil && root.RedfishVersion != ""
}

// redfishSystemIDs lists the ComputerSystem Ids a fence profile could name. A member that is
// not a direct child of the Systems collection, or an Id the profile rule refuses, is skipped
// rather than offered.
func redfishSystemIDs(ctx context.Context, b *bmcSession) ([]string, error) {
	resp, err := b.request(ctx, http.MethodGet, "/redfish/v1/Systems", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var systems struct {
		Members []struct {
			ID string `json:"@odata.id"`
		} `json:"Members"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&systems) != nil {
		return nil, errBMCNoReport
	}
	ids := []string{}
	for _, member := range systems.Members {
		id, ok := strings.CutPrefix(strings.TrimSuffix(member.ID, "/"), "/redfish/v1/Systems/")
		if ok && protocol.BMCTargetIDValid(id) && len(ids) < protocol.PoolBMCProbeMaxTargets {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// probeIPMI asks for the chassis power state, the least an IPMI controller answers an
// authenticated session, over the cipher suites Set BMC access would select from.
func probeIPMI(ctx context.Context, endpoint, username, secret string) protocol.PoolBMCProbeMethod {
	method := protocol.PoolBMCProbeMethod{Kind: protocol.FenceIPMI}
	if secret == "" {
		method.Detail = "IPMI over LAN answers only an authenticated session; enter the controller's account and password to try it"
		return method
	}
	if _, err := exec.LookPath("ipmitool"); err != nil {
		method.Detail = errNoIPMITool.Error()
		return method
	}
	for _, cipher := range []uint8{17, 3} {
		b := bmcSession{fence: protocol.StorageClusterFence{Kind: protocol.FenceIPMI, Endpoint: endpoint, Username: username, Cipher: cipher}, secret: secret}
		if power, err := b.powerState(ctx); err == nil && power != "" {
			method.Reachable = true
			return method
		}
	}
	method.Detail = "the management controller did not answer IPMI over LAN with this account; IPMI over LAN may be turned off in the controller"
	return method
}
