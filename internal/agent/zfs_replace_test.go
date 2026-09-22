// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func zfsReplaceCommand(master, member *Store, repositoryID string) protocol.Command {
	return protocol.Command{OperationID: "zfs-replace-op", Kind: "storage-zfs-replace", ZFSReplace: &protocol.ZFSReplaceRequest{
		OperationID: "zfs-replace-op", RepositoryID: repositoryID, PoolID: master.State.PoolID, HostID: member.State.HostID,
		// As a pool observation names it: the partition on the selected whole disk.
		OldDevice: "/dev/disk/by-id/" + member.State.HostID + "-part1",
		Device:    protocol.ZFSProvisionDevice{ByID: "/dev/disk/by-id/disk-new", Serial: "new-serial", SizeBytes: 1, Fingerprint: strings.Repeat("a", 64)}}}
}

// The master owns the repository record, so it decides whether the repository and the host
// are its own and then the host that holds the pool does the work.
func TestPoolZFSReplaceForwardsToTheMemberAndRecordsTheNewDisk(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	oldProvision, oldReplace := poolZFSExecuteLocal, poolZFSReplaceLocal
	defer func() { poolZFSExecuteLocal, poolZFSReplaceLocal = oldProvision, oldReplace }()
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Devices: host.Devices, Status: "succeeded"}
	}
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	provision := zfsPoolCommand(member.State.HostID)
	provision.ZFSProvision.PoolID = master.State.PoolID
	if result, err := ExecutePoolZFSProvision(context.Background(), master, provision); err != nil || result.Status != "succeeded" {
		t.Fatalf("provision: %+v %v", result, err)
	}
	repositoryID := zfsRepositoryID(provision.OperationID)

	var seen []protocol.ZFSReplaceRequest
	poolZFSReplaceLocal = func(_ context.Context, s *Store, request protocol.ZFSReplaceRequest) error {
		if s.State.HostID != member.State.HostID {
			t.Errorf("the replacement ran on %s, not on the host that holds the pool", s.State.HostID)
		}
		seen = append(seen, request)
		return nil
	}
	cmd := zfsReplaceCommand(master, member, repositoryID)
	if result, err := ExecutePoolZFSReplace(context.Background(), master, cmd); err != nil || result.Status != "succeeded" {
		t.Fatalf("replace: %+v %v", result, err)
	}
	if len(seen) != 1 || seen[0].Device.ByID != "/dev/disk/by-id/disk-new" {
		t.Fatalf("the member was asked for: %+v", seen)
	}
	poolZFSMu.Lock()
	state, err := loadPoolZFS(master)
	poolZFSMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	devices := state.Operations[provision.OperationID].Outcomes[member.State.HostID].Devices
	if len(devices) != 1 || devices[0].ByID != "/dev/disk/by-id/disk-new" {
		t.Fatalf("the repository record still names the old disk: %+v", devices)
	}

	// A repository this master does not own is refused without reaching any host.
	unowned := zfsReplaceCommand(master, member, "zfs-not-owned")
	if result, err := ExecutePoolZFSReplace(context.Background(), master, unowned); err != nil || result.Status != "failed" || len(seen) != 1 {
		t.Fatalf("unowned repository: %+v %v %+v", result, err, seen)
	}
	// Only the master may run the operation at all.
	if result, err := ExecutePoolZFSReplace(context.Background(), member, cmd); err != nil || result.Status != "failed" || !strings.Contains(result.Error, "only the pool master") {
		t.Fatalf("member as master: %+v %v", result, err)
	}

	// The member endpoint is mTLS master-only.
	anonymous := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	defer anonymous.CloseIdleConnections()
	body, _ := json.Marshal(cmd.ZFSReplace)
	request, _ := http.NewRequest(http.MethodPost, memberServer.URL+poolZFSReplacePath, bytes.NewReader(body))
	response, err := anonymous.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous peer status=%d", response.StatusCode)
	}
}

// A host that refuses the replacement is reported as it answered, and nothing rewrites the
// repository record on the strength of an attempt.
func TestPoolZFSReplaceReportsTheHostRefusal(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	oldProvision, oldReplace := poolZFSExecuteLocal, poolZFSReplaceLocal
	defer func() { poolZFSExecuteLocal, poolZFSReplaceLocal = oldProvision, oldReplace }()
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Devices: host.Devices, Status: "succeeded"}
	}
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	provision := zfsPoolCommand(member.State.HostID)
	provision.ZFSProvision.PoolID = master.State.PoolID
	if _, err := ExecutePoolZFSProvision(context.Background(), master, provision); err != nil {
		t.Fatal(err)
	}
	poolZFSReplaceLocal = func(context.Context, *Store, protocol.ZFSReplaceRequest) error {
		return errors.New("/dev/disk/by-id/disk-old is online, so there is nothing to replace")
	}
	cmd := zfsReplaceCommand(master, member, zfsRepositoryID(provision.OperationID))
	result, err := ExecutePoolZFSReplace(context.Background(), master, cmd)
	if err != nil || result.Status != "needs_attention" || !strings.Contains(result.Error, "is online") {
		t.Fatalf("refusal: %+v %v", result, err)
	}
	poolZFSMu.Lock()
	state, loadErr := loadPoolZFS(master)
	poolZFSMu.Unlock()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if devices := state.Operations[provision.OperationID].Outcomes[member.State.HostID].Devices; devices[0].ByID == "/dev/disk/by-id/disk-new" {
		t.Fatalf("a refused replacement rewrote the repository record: %+v", devices)
	}
}

// A ZFS repository command is addressed to the pool master, which names the host inside the
// request; the host-identity check that guards every other managed command must not reject it.
func TestPoolZFSCommandsAreAddressedToTheMaster(t *testing.T) {
	for _, kind := range []string{"storage-zfs-provision", "storage-zfs-replace"} {
		if !poolAddressedCommand(kind) || commandHost(protocol.Command{Kind: kind}) != "" {
			t.Fatalf("%s is not treated as a pool-addressed command", kind)
		}
	}
	if poolAddressedCommand("vm-create") {
		t.Fatal("an ordinary managed command was treated as pool-addressed")
	}
}

// A member that is not ready (pending or ejected) must never receive wipefs / zpool replace.
// Membership.Valid already requires every row share the pool generation, so a lagging
// generation cannot appear in a loadable membership; the ready-status check is what fires.
func TestPoolZFSReplaceRefusesAStaleOrJoiningMember(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	oldProvision, oldReplace := poolZFSExecuteLocal, poolZFSReplaceLocal
	defer func() { poolZFSExecuteLocal, poolZFSReplaceLocal = oldProvision, oldReplace }()
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Devices: host.Devices, Status: "succeeded"}
	}
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	provision := zfsPoolCommand(member.State.HostID)
	provision.ZFSProvision.PoolID = master.State.PoolID
	if _, err := ExecutePoolZFSProvision(context.Background(), master, provision); err != nil {
		t.Fatal(err)
	}
	var called atomic.Bool
	poolZFSReplaceLocal = func(context.Context, *Store, protocol.ZFSReplaceRequest) error {
		called.Store(true)
		return nil
	}
	cmd := zfsReplaceCommand(master, member, zfsRepositoryID(provision.OperationID))

	for _, status := range []string{"pending", "ejected"} {
		called.Store(false)
		poolPeerMu.Lock()
		state, err := loadPoolPeer(master)
		if err == nil {
			for i := range state.Membership.Members {
				if state.Membership.Members[i].HostID == member.State.HostID {
					state.Membership.Members[i].Status = status
					state.Membership.Members[i].Generation = state.Membership.Generation
				}
			}
			err = savePoolPeer(master, state)
		}
		poolPeerMu.Unlock()
		if err != nil {
			t.Fatal(err)
		}
		result, err := ExecutePoolZFSReplace(context.Background(), master, cmd)
		if err != nil || result.Status != "failed" || !strings.Contains(result.Error, "not a ready current member") || called.Load() {
			t.Fatalf("status %s: %+v %v called=%v", status, result, err, called.Load())
		}
	}
}

// The member peer route accepts only a request addressed to its own pool and host.
func TestPoolZFSReplacePeerRefusesAForeignPoolOrHost(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	client, err := PoolMemberPeerClient(master, memberServer.URL, member.State.HostID)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	var called atomic.Bool
	old := poolZFSReplaceLocal
	defer func() { poolZFSReplaceLocal = old }()
	poolZFSReplaceLocal = func(context.Context, *Store, protocol.ZFSReplaceRequest) error {
		called.Store(true)
		return nil
	}
	for _, request := range []protocol.ZFSReplaceRequest{
		{OperationID: "zfs-replace-op", RepositoryID: "zfs-repo", PoolID: "foreign-pool", HostID: member.State.HostID,
			OldDevice: "/dev/disk/by-id/old-part1", Device: protocol.ZFSProvisionDevice{ByID: "/dev/disk/by-id/disk-new", Serial: "new", SizeBytes: 1, Fingerprint: strings.Repeat("a", 64)}},
		{OperationID: "zfs-replace-op", RepositoryID: "zfs-repo", PoolID: member.State.PoolID, HostID: "other-host",
			OldDevice: "/dev/disk/by-id/old-part1", Device: protocol.ZFSProvisionDevice{ByID: "/dev/disk/by-id/disk-new", Serial: "new", SizeBytes: 1, Fingerprint: strings.Repeat("a", 64)}},
	} {
		called.Store(false)
		body, _ := json.Marshal(request)
		req, _ := http.NewRequest(http.MethodPost, memberServer.URL+poolZFSReplacePath, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest || called.Load() {
			t.Fatalf("foreign binding status=%d called=%v request=%+v", resp.StatusCode, called.Load(), request)
		}
	}
}

// Hot spares and widened layouts must not reach a member that lacks replace support.
func TestPoolZFSProvisionRefusesWidenedSelectionOnAnOldMember(t *testing.T) {
	master, member, _, memberServer := cpuPeerPair(t)
	oldProvision, oldProbe := poolZFSExecuteLocal, poolZFSMemberSupportsReplace
	defer func() { poolZFSExecuteLocal, poolZFSMemberSupportsReplace = oldProvision, oldProbe }()
	var called atomic.Bool
	poolZFSExecuteLocal = func(context.Context, *Store, string, string, protocol.ZFSProvisionRequest, protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		called.Store(true)
		return protocol.ZFSProvisionHostResult{Status: "succeeded"}
	}
	poolZFSMemberSupportsReplace = func(context.Context, *http.Client, string) (bool, error) {
		return false, nil
	}
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	fp := strings.Repeat("a", 64)
	cmd := protocol.Command{OperationID: "zfs-spare-op", Kind: "storage-zfs-provision", ZFSProvision: &protocol.ZFSProvisionRequest{
		PoolID: master.State.PoolID, Name: "fast", Scope: "pool",
		Hosts: []protocol.ZFSProvisionHost{{
			HostID: member.State.HostID, Layout: "mirror",
			Devices: []protocol.ZFSProvisionDevice{
				{ByID: "/dev/disk/by-id/d1", Serial: "d1", SizeBytes: 1, Fingerprint: fp},
				{ByID: "/dev/disk/by-id/d2", Serial: "d2", SizeBytes: 1, Fingerprint: fp},
			},
			Spares: []protocol.ZFSProvisionDevice{
				{ByID: "/dev/disk/by-id/s1", Serial: "s1", SizeBytes: 1, Fingerprint: fp, EraseConfirmation: "/dev/disk/by-id/s1"},
			},
		}},
	}}
	result, err := ExecutePoolZFSProvision(context.Background(), master, cmd)
	if err != nil || result.Status != "needs_attention" || called.Load() {
		t.Fatalf("old member spare provision: %+v %v called=%v", result, err, called.Load())
	}
	poolZFSMu.Lock()
	state, loadErr := loadPoolZFS(master)
	poolZFSMu.Unlock()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	outcome := state.Operations[cmd.OperationID].Outcomes[member.State.HostID]
	if !strings.Contains(outcome.Error, protocol.ZFSReplaceCapability) {
		t.Fatalf("upgrade sentence missing: %+v", outcome)
	}
}

// A cancelled command context must not abort a member replacement already in flight, and the
// peer client budget must cover a destructive pool operation rather than the default 15 s.
func TestPoolZFSReplaceDetachesFromCommandCancelAndUsesALongPeerTimeout(t *testing.T) {
	if poolZFSReplacePeerTimeout <= 15*time.Second {
		t.Fatalf("replace peer timeout %s is not longer than the default peer client budget", poolZFSReplacePeerTimeout)
	}
	master, member, _, memberServer := cpuPeerPair(t)
	oldProvision, oldReplace := poolZFSExecuteLocal, poolZFSReplaceLocal
	defer func() { poolZFSExecuteLocal, poolZFSReplaceLocal = oldProvision, oldReplace }()
	poolZFSExecuteLocal = func(_ context.Context, _ *Store, _ string, repositoryID string, request protocol.ZFSProvisionRequest, host protocol.ZFSProvisionHost) protocol.ZFSProvisionHostResult {
		return protocol.ZFSProvisionHostResult{RepositoryID: repositoryID, PoolID: request.PoolID, Name: request.Name, Scope: request.Scope, HostID: host.HostID, Devices: host.Devices, Status: "succeeded"}
	}
	if err := AdvertisePoolPeerOrigin(context.Background(), member, memberServer.URL); err != nil {
		t.Fatal(err)
	}
	provision := zfsPoolCommand(member.State.HostID)
	provision.ZFSProvision.PoolID = master.State.PoolID
	if _, err := ExecutePoolZFSProvision(context.Background(), master, provision); err != nil {
		t.Fatal(err)
	}
	poolZFSReplaceLocal = func(ctx context.Context, _ *Store, _ protocol.ZFSReplaceRequest) error {
		time.Sleep(40 * time.Millisecond)
		return ctx.Err()
	}
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	cmd := zfsReplaceCommand(master, member, zfsRepositoryID(provision.OperationID))
	result, err := ExecutePoolZFSReplace(parent, master, cmd)
	if err != nil || result.Status != "succeeded" {
		t.Fatalf("cancelled command aborted the replacement: %+v %v", result, err)
	}
}
