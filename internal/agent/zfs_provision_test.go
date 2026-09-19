// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestZFSProvisionUsesStableCoreRepositoryIdentity(t *testing.T) {
	request := &protocol.ZFSProvisionRequest{PoolID: "pool-1", Name: "fast", Scope: "host", Hosts: []protocol.ZFSProvisionHost{{HostID: "host-1", Devices: []protocol.ZFSProvisionDevice{{ByID: "/dev/disk/by-id/test", Serial: "test", SizeBytes: 1, Fingerprint: "0000000000000000000000000000000000000000000000000000000000000000"}}, Layout: "single"}}}
	command := protocol.Command{OperationID: "provision-1", Kind: "storage-zfs-provision", ZFSProvision: request}
	if got := commandHost(command); got != "" {
		t.Fatalf("pool-owned command target = %q", got)
	}
	if zfsRepositoryID(command.OperationID) != zfsRepositoryID(command.OperationID) || zfsRepositoryID(command.OperationID) == zfsRepositoryID("provision-2") {
		t.Fatal("repository allocation is not stable and unique per operation")
	}
}

func TestZFSProvisionDoesNotRouteMultiHostCommand(t *testing.T) {
	command := protocol.Command{Kind: "storage-zfs-provision", ZFSProvision: &protocol.ZFSProvisionRequest{Hosts: []protocol.ZFSProvisionHost{{HostID: "host-1"}, {HostID: "host-2"}}}}
	if got := commandHost(command); got != "" {
		t.Fatalf("multi-host command target = %q", got)
	}
}
