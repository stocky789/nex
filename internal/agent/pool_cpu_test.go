// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func mockPoolCPU(t *testing.T) *int {
	t.Helper()
	oldHost, oldBaseline, oldCompare := poolHostCPU, poolNativeBaseline, poolNativeCompare
	t.Cleanup(func() { poolHostCPU, poolNativeBaseline, poolNativeCompare = oldHost, oldBaseline, oldCompare })
	calls := new(int)
	poolHostCPU = func(context.Context, string) (string, error) {
		return `<cpu><model>ExampleCPU-v1</model><vendor>Intel</vendor></cpu>`, nil
	}
	poolNativeBaseline = func(_ context.Context, cpus []string, machine string) (protocol.CPUBaseline, error) {
		*calls++
		return protocol.CPUBaseline{Model: "ExampleCPU-v1", Features: []protocol.CPUFeature{{Name: "sse4.2", Policy: "require"}}}, nil
	}
	poolNativeCompare = func(context.Context, string, []protocol.CPUFeature, string) error { return nil }
	return calls
}

func TestPoolCPUProbeCachesOnlySuccessfulCurrentMembership(t *testing.T) {
	calls := mockPoolCPU(t)
	master := peerStore(t, "cpu-master", "cpu-pool")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	probe := PoolCPUProbe(master)
	first, err := probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	baseline := first.(protocol.CPUBaseline)
	if baseline.Error != "" || baseline.MemberCount != 1 || baseline.Generation != 1 || baseline.ObservedAt.IsZero() {
		t.Fatalf("baseline: %+v", baseline)
	}
	baseline.Features[0].Name = "changed"
	second, _ := probe(context.Background())
	if *calls != 1 || second.(protocol.CPUBaseline).Features[0].Name != "sse4.2" {
		t.Fatal("cache was not isolated")
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(master)
	if err == nil {
		state.Membership.Generation++
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = state.Membership.Generation
		}
		err = savePoolPeer(master, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	poolNativeCompare = func(context.Context, string, []protocol.CPUFeature, string) error {
		return errors.New("runtime changed")
	}
	third, _ := probe(context.Background())
	if *calls != 2 || !strings.Contains(third.(protocol.CPUBaseline).Error, "runtime changed") {
		t.Fatal("stale CPU observation survived generation change")
	}
	poolNativeCompare = func(context.Context, string, []protocol.CPUFeature, string) error { return nil }
	fourth, _ := probe(context.Background())
	if *calls != 3 || fourth.(protocol.CPUBaseline).Error != "" {
		t.Fatal("failed observations were cached")
	}
}

func cpuPeerPair(t *testing.T) (*Store, *Store, *httptest.Server, *httptest.Server) {
	t.Helper()
	master, member := peerStore(t, "cpu-master", "cpu-pool"), peerStore(t, "cpu-member", "other-pool")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePoolMembership(member); err != nil {
		t.Fatal(err)
	}
	csr, err := PoolJoinCSR(member)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(PoolPeerHandler(master))
	server.TLS, err = PoolPeerTLSConfig(master)
	if err != nil {
		t.Fatal(err)
	}
	server.StartTLS()
	t.Cleanup(server.Close)
	ca, offer, err := CreatePoolJoinOffer(master, server.URL, member.State.HostID, csr)
	if err != nil {
		t.Fatal(err)
	}
	if err = JoinPoolPeer(context.Background(), member, server.URL, ca, offer); err != nil {
		t.Fatal(err)
	}
	memberServer := httptest.NewUnstartedServer(PoolPeerHandler(member))
	memberServer.TLS, err = PoolPeerTLSConfig(member)
	if err != nil {
		t.Fatal(err)
	}
	memberServer.StartTLS()
	t.Cleanup(memberServer.Close)
	if err = AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	return master, member, server, memberServer
}

func TestPoolCPUBaselineAuthenticatedPeersAndRuntimeChecks(t *testing.T) {
	mockPoolCPU(t)
	master, member, server, memberServer := cpuPeerPair(t)
	baseline, err := poolCPUBaseline(context.Background(), master, "q35")
	if err != nil || baseline.MemberCount != 2 {
		t.Fatalf("baseline: %+v, %v", baseline, err)
	}
	probe := PoolCPUProbe(member)
	observed, err := probe(context.Background())
	if err != nil || observed.(protocol.CPUBaseline).Error != "" || observed.(protocol.CPUBaseline).MemberCount != 2 {
		t.Fatalf("member result: %+v, %v", observed, err)
	}
	// Both endpoint directions require authenticated certificate-bound identities.
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer anonymous.CloseIdleConnections()
	for _, endpoint := range []string{server.URL + poolCPUPath + "baseline", memberServer.URL + poolCPUPath + "capabilities"} {
		response, err := anonymous.Get(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated CPU endpoint: %d", response.StatusCode)
		}
	}
	membership, _ := Membership(master)
	request := poolCPUComparison{Generation: membership.Generation - 1, Machine: "q35", Model: "ExampleCPU-v1"}
	var response poolCPUComparison
	if err := poolCPURequest(context.Background(), master, memberServer.URL, member.State.HostID, http.MethodPost, "compare", request, &response); err == nil {
		t.Fatal("stale comparison accepted")
	}
	request.Generation = membership.Generation
	request.Features = []protocol.CPUFeature{{Name: "sse4.2", Policy: "force"}}
	if err := poolCPURequest(context.Background(), master, memberServer.URL, member.State.HostID, http.MethodPost, "compare", request, &response); err == nil {
		t.Fatal("unbounded or unsupported feature policy accepted")
	}
	if err := poolCPURequest(context.Background(), master, memberServer.URL, member.State.HostID, http.MethodGet, "compare", nil, &response); err == nil {
		t.Fatal("incorrect comparison method accepted")
	}
	checks := 0
	poolNativeCompare = func(context.Context, string, []protocol.CPUFeature, string) error {
		checks++
		if checks == 2 {
			return errors.New("member runtime rejects CPU")
		}
		return nil
	}
	if _, err := poolCPUBaseline(context.Background(), master, "q35"); err == nil {
		t.Fatal("member runtime rejection ignored")
	}
}

func TestPoolCPUBaselineNeverOmitsUnknownMembers(t *testing.T) {
	mockPoolCPU(t)
	master, _, _, _ := cpuPeerPair(t)
	for _, status := range []string{"pending", "ready"} {
		poolPeerMu.Lock()
		state, err := loadPoolPeer(master)
		if err == nil {
			for i := range state.Membership.Members {
				if state.Membership.Members[i].HostID != master.State.HostID {
					state.Membership.Members[i].Status = status
					state.Membership.Members[i].PeerOrigin = ""
				}
			}
			err = savePoolPeer(master, state)
		}
		poolPeerMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := poolCPUBaseline(context.Background(), master, "q35"); err == nil {
			t.Fatalf("%s member omitted", status)
		}
	}
}

func TestPoolCPUBaselineRejectsMixedVendorsAndMembershipChange(t *testing.T) {
	mockPoolCPU(t)
	master, _, _, _ := cpuPeerPair(t)
	calls := 0
	poolHostCPU = func(context.Context, string) (string, error) {
		calls++
		vendor := "Intel"
		if calls == 2 {
			vendor = "AMD"
		}
		return `<cpu><model>ExampleCPU-v1</model><vendor>` + vendor + `</vendor></cpu>`, nil
	}
	if _, err := poolCPUBaseline(context.Background(), master, "q35"); err == nil || !strings.Contains(err.Error(), "vendors differ") {
		t.Fatalf("mixed vendor result: %v", err)
	}
	poolHostCPU = func(context.Context, string) (string, error) {
		return `<cpu><model>ExampleCPU-v1</model><vendor>Intel</vendor></cpu>`, nil
	}
	poolNativeCompare = func(context.Context, string, []protocol.CPUFeature, string) error {
		poolPeerMu.Lock()
		defer poolPeerMu.Unlock()
		state, err := loadPoolPeer(master)
		if err != nil {
			return err
		}
		state.Membership.Generation++
		for i := range state.Membership.Members {
			state.Membership.Members[i].Generation = state.Membership.Generation
		}
		return savePoolPeer(master, state)
	}
	if _, err := poolCPUBaseline(context.Background(), master, "q35"); err == nil {
		t.Fatal("changed membership accepted")
	}
}

func TestPoolCPUSharedCacheCoalescesPeerAndMonitorDiscovery(t *testing.T) {
	mockPoolCPU(t)
	master, member, server, _ := cpuPeerPair(t)
	started, release := make(chan struct{}), make(chan struct{})
	calls := 0
	poolNativeBaseline = func(_ context.Context, _ []string, _ string) (protocol.CPUBaseline, error) {
		calls++
		if calls == 1 {
			close(started)
		}
		<-release
		return protocol.CPUBaseline{Model: "ExampleCPU-v1", Features: []protocol.CPUFeature{{Name: "aes", Policy: "require"}}}, nil
	}
	results := make(chan error, 6)
	go func() { _, err := cachedPoolCPUBaseline(context.Background(), master, "q35"); results <- err }()
	<-started
	membership, _ := Membership(member)
	for i := 0; i < 5; i++ {
		go func() {
			var result protocol.CPUBaseline
			err := poolCPURequest(context.Background(), member, server.URL, "", http.MethodGet, "baseline?machine=q35&generation="+strconv.FormatUint(membership.Generation, 10), nil, &result)
			if err == nil && result.MemberCount != 2 {
				err = errors.New("missing verified members")
			}
			results <- err
		}()
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cachedPoolCPUBaseline(canceled, master, "q35"); err == nil {
		t.Fatal("canceled waiter succeeded")
	}
	close(release)
	for i := 0; i < 6; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("discovery repeated %d times", calls)
	}
}

func TestPoolCPUNeverPublishesEmptyOrHostModeBaseline(t *testing.T) {
	mockPoolCPU(t)
	master := peerStore(t, "cpu-master", "cpu-pool")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"ExampleCPU-v1", "host-model", "host-passthrough", "max"} {
		poolNativeBaseline = func(context.Context, []string, string) (protocol.CPUBaseline, error) {
			return protocol.CPUBaseline{Model: model}, nil
		}
		result, _ := PoolCPUProbe(master)(context.Background())
		if result.(protocol.CPUBaseline).Error == "" {
			t.Fatalf("empty %s baseline published", model)
		}
	}
}

func TestPoolCPUCancelledDiscoveryDoesNotPopulateCache(t *testing.T) {
	mockPoolCPU(t)
	master := peerStore(t, "cpu-master", "cpu-pool")
	if err := EnsurePoolMembership(master); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	poolNativeBaseline = func(context.Context, []string, string) (protocol.CPUBaseline, error) {
		cancel()
		return protocol.CPUBaseline{Model: "ExampleCPU-v1", Features: []protocol.CPUFeature{{Name: "aes", Policy: "require"}}}, nil
	}
	if _, err := cachedPoolCPUBaseline(ctx, master, "q35"); err == nil {
		t.Fatal("canceled discovery succeeded")
	}
	master.cpuBaseline.mu.Lock()
	defer master.cpuBaseline.mu.Unlock()
	if master.cpuBaseline.baseline.Model != "" {
		t.Fatal("canceled discovery populated cache")
	}
}
