// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

func clusterTestConfig() protocol.StorageClusterConfig {
	return protocol.StorageClusterConfig{ClusterID: "pool-1", PoolID: "pool-1", RepositoryID: "repo-1", Generation: 1, LockTable: "pool-1:repo-1", DevicePath: "/dev/disk/by-id/scsi-3600508b400105e210000900000490000", MountPath: "/var/lib/nex-storage/repo-1", Nodes: []protocol.StorageClusterNode{{HostID: "host-1", CorosyncNodeID: 2, CorosyncAddress: "core1.example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: "bmc1.example.test", TargetID: "system1", CredentialRef: "fence1"}}, {HostID: "host-2", CorosyncNodeID: 1, CorosyncAddress: "core2.example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: "bmc2.example.test", TargetID: "system2", CredentialRef: "fence2"}}, {HostID: "host-3", CorosyncNodeID: 3, CorosyncAddress: "core3.example.test", Fence: protocol.StorageClusterFence{Kind: protocol.FenceRedfish, Endpoint: "bmc3.example.test", TargetID: "system3", CredentialRef: "fence3"}}}}
}

func clusterTestRegistration(c protocol.StorageClusterConfig) protocol.StorageRegistration {
	return protocol.StorageRegistration{ID: c.RepositoryID, PoolID: c.PoolID, HostID: "host-1", Generation: c.Generation, Kind: protocol.StorageKindISCSI, Path: c.MountPath,
		Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared", LUN: 1, WWID: "3600508b400105e210000900000490000", FilesystemUUID: "12345678-1234-1234-1234-123456789abc", MountPath: c.MountPath, Mode: "clustered_gfs2", ClusterID: c.ClusterID, LockTable: c.LockTable, EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}}}
}

// setForTest replaces a package test seam for the duration of t.
func setForTest[T any](t *testing.T, p *T, v T) {
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func replaceCIB(raw []byte, old, with string) []byte {
	return []byte(strings.Replace(string(raw), old, with, 1))
}

// provenClusterConfig carries passed, exclusion-verified fence proofs for every node.
func provenClusterConfig() protocol.StorageClusterConfig {
	c := clusterTestConfig()
	for _, node := range c.Nodes {
		c.FenceProofs = append(c.FenceProofs, protocol.StorageClusterFenceStatus{HostID: node.HostID, TargetID: node.Fence.TargetID, Status: "passed", ExclusionVerified: true, ObservedAt: time.Now().UTC(), ConfigGeneration: c.Generation})
	}
	digest := c.Digest()
	for i := range c.FenceProofs {
		c.FenceProofs[i].ConfigDigest = digest
	}
	return c
}

// startClusterFixture isolates Corosync config and the DLM helper check.
func startClusterFixture(t *testing.T) protocol.StorageClusterConfig {
	setForTest(t, &corosyncConfigPath, filepath.Join(t.TempDir(), "corosync.conf"))
	setForTest(t, &validateStorageClusterDLMFenceHelper, func(context.Context) error { return nil })
	return provenClusterConfig()
}

func writeCorosyncMarker(t *testing.T, state string, config []byte) {
	marker := corosyncUnconfiguredMarkerPath(state)
	if err := os.MkdirAll(filepath.Dir(marker), 0700); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(config)
	if err := os.WriteFile(marker, []byte(hex.EncodeToString(digest[:])+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

// fakeClusterServices permits only systemd service observation and start.
func fakeClusterServices(t *testing.T, enabled, active string) *int {
	starts := new(int)
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name + " " + strings.Join(args, " ") {
		case "systemctl enable --now corosync.service pacemaker.service":
			*starts++
			return nil, nil
		case "systemctl show --property=UnitFileState --value corosync.service", "systemctl show --property=UnitFileState --value pacemaker.service":
			return []byte(enabled + "\n"), nil
		case "systemctl show --property=ActiveState --value corosync.service", "systemctl show --property=ActiveState --value pacemaker.service":
			return []byte(active + "\n"), nil
		}
		t.Fatalf("unexpected cluster command %s %v", name, args)
		return nil, nil
	})
	return starts
}

func gfs2LSBLK(path, wwn string) []byte {
	return []byte(`{"blockdevices":[{"path":"` + path + `","type":"disk","uuid":"12345678-1234-1234-1234-123456789abc","wwn":"` + wwn + `","fstype":"gfs2","hctl":"0:0:0:1"}]}`)
}

// fakeClusteredISCSI stubs open-iscsi, udev and lsblk for s. lsblk receives its
// 1-based call number; onStartup runs when node.startup is set to automatic.
func fakeClusteredISCSI(t *testing.T, s protocol.StorageRegistration, lsblk func(call int) []byte, onStartup func()) {
	setForTest(t, &iscsiNodeRoot, t.TempDir())
	setForTest(t, &iscsiOwnerRoot, t.TempDir())
	setForTest(t, &clusterRun, clusterRun)
	calls := 0
	setForTest(t, &nativeRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch name {
		case "iscsiadm":
			if strings.Contains(joined, "--op new") {
				if err := os.MkdirAll(filepath.Dir(iscsiNodePath(s.Source.ISCSI)), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(iscsiNodePath(s.Source.ISCSI), []byte("node.startup = manual\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if strings.Contains(joined, "--op update -n node.startup -v automatic") && onStartup != nil {
				onStartup()
			}
			return nil, nil
		case "udevadm":
			if joined != "settle --timeout=2" {
				t.Fatalf("unexpected udev wait %v", args)
			}
			return nil, nil
		case "lsblk":
			calls++
			return lsblk(calls), nil
		}
		t.Fatalf("unexpected native command %s %v", name, args)
		return nil, nil
	})
}

// ownedClusterFixture writes fence credentials for c and renders the exact
// current Core-owned CIB.
func ownedClusterFixture(t *testing.T, c protocol.StorageClusterConfig) (string, []byte) {
	state := t.TempDir()
	for _, node := range c.Nodes {
		path := filepath.Join(state, "storage-cluster", "fence-credentials", node.Fence.CredentialRef)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	transport, err := storageClusterTransportFor(clusterTestRegistration(c), c)
	if err != nil {
		t.Fatal(err)
	}
	resources := []string{testCIBResource(dlmResourceSpec()), testTransportCIBResource(transport), testCIBResource(gfs2ResourceSpec(c))}
	for _, node := range c.Nodes {
		fence, err := fenceCIBCreateArgs(state, node)
		if err != nil {
			t.Fatal(err)
		}
		resources = append(resources, testCIBResource(fence))
	}
	return state, []byte(`<cib admin_epoch="0" epoch="1" num_updates="1"><configuration><crm_config><cluster_property_set>` +
		`<nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-action" value="off"/><nvpair name="startup-fencing" value="true"/><nvpair name="no-quorum-policy" value="freeze"/>` +
		`</cluster_property_set></crm_config><resources>` + strings.Join(resources, "") + `</resources><constraints><rsc_order id="nex-dlm-before-` + html.EscapeString(c.RepositoryID) + `" first="nex-dlm-clone" then="nex-gfs2-` + html.EscapeString(c.RepositoryID) + `-clone" kind="Mandatory"/>` + testTransportOrderCIB(transport, c) + `</constraints></configuration></cib>`)
}

func testTransportOrderCIB(t storageClusterTransport, c protocol.StorageClusterConfig) string {
	e := html.EscapeString
	return `<rsc_order id="` + e(storageTransportOrderID(t, c)) + `" first="` + e(t.resource) + `-clone" then="nex-gfs2-` + e(c.RepositoryID) + `-clone" kind="Mandatory" first-action="start" then-action="start"/>`
}

func testTransportCIBResource(t storageClusterTransport) string {
	e := html.EscapeString
	return `<clone id="` + e(t.resource) + `-clone"><meta_attributes><nvpair name="interleave" value="true"/><nvpair name="ordered" value="true"/></meta_attributes><primitive id="` + e(t.resource) + `" class="ocf" provider="nex" type="cluster-iscsi-session"><instance_attributes><nvpair name="portal" value="` + e(t.portal) + `"/><nvpair name="target" value="` + e(t.target) + `"/><nvpair name="session_id" value="` + e(t.resource) + `"/></instance_attributes><operations><op id="` + e(t.resource) + `-start-interval-0s" name="start" interval="0s" timeout="10s" on-fail="fence"/><op id="` + e(t.resource) + `-stop-interval-0s" name="stop" interval="0s" timeout="10s"/><op id="` + e(t.resource) + `-monitor-interval-5s" name="monitor" interval="5s" timeout="10s" on-fail="fence"/></operations></primitive></clone>`
}

func TestRenderStorageClusterIsDeterministicAndAuthenticated(t *testing.T) {
	text, err := RenderStorageCluster(clusterTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Managed by Nex Core", "secauth: on", "crypto_cipher: aes256", "crypto_hash: sha256", "ring0_addr: core2.example.test\n    name: host-2\n    nodeid: 1", "quorum {\n  provider: corosync_votequorum\n}"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in %s", want, text)
		}
	}
}

func TestStartStorageClusterDoesNotWriteSharedCIB(t *testing.T) {
	c := startClusterFixture(t)
	starts := fakeClusterServices(t, "", "")
	state := t.TempDir()
	stock := []byte("totem {\n  version: 2\n}\n")
	if err := os.WriteFile(corosyncConfigPath, stock, 0600); err != nil {
		t.Fatal(err)
	}
	writeCorosyncMarker(t, state, stock)
	if err := StartStorageClusterAt(context.Background(), state, c); err != nil || *starts != 1 {
		t.Fatalf("marked vendor start = starts %d, %v", *starts, err)
	}
}

// A first start always writes config and starts services; a repeat start with
// current config restarts only an inactive or non-persistent service.
func TestStartStorageClusterIsConfigNoopAndRecoversInactiveOrNonpersistentService(t *testing.T) {
	for _, tc := range []struct {
		name, enabled, active string
		wantStarts            int
	}{
		{"persistent", "enabled", "active", 1},
		{"inactive", "enabled", "inactive", 2},
		{"nonpersistent", "disabled", "active", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := startClusterFixture(t)
			starts := fakeClusterServices(t, tc.enabled, tc.active)
			state := t.TempDir()
			if err := StartStorageClusterAt(context.Background(), state, c); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(corosyncConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if err := StartStorageClusterAt(context.Background(), state, c); err != nil {
				t.Fatal(err)
			}
			after, err := os.Stat(corosyncConfigPath)
			if err != nil {
				t.Fatal(err)
			}
			if *starts != tc.wantStarts || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
				t.Fatalf("repeat start: starts=%d want %d same=%t before=%v after=%v", *starts, tc.wantStarts, os.SameFile(before, after), before.ModTime(), after.ModTime())
			}
		})
	}
}

func TestStartStorageClusterRefusesForeignOrEditedVendorConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		marked bool
	}{{"unmarked foreign", false}, {"edited after marker", true}} {
		t.Run(tc.name, func(t *testing.T) {
			c := startClusterFixture(t)
			setForTest(t, &clusterRun, func(context.Context, string, ...string) ([]byte, error) {
				t.Fatal("foreign configuration reached systemctl")
				return nil, nil
			})
			state := t.TempDir()
			if tc.marked {
				writeCorosyncMarker(t, state, []byte("totem { cluster_name: debian }\n"))
			}
			edited := []byte("totem { cluster_name: locally-edited }\n")
			if err := os.WriteFile(corosyncConfigPath, edited, 0600); err != nil {
				t.Fatal(err)
			}
			if err := StartStorageClusterAt(context.Background(), state, c); err == nil || !strings.Contains(err.Error(), "administrator Corosync") {
				t.Fatalf("foreign configuration error = %v", err)
			}
			if got, err := os.ReadFile(corosyncConfigPath); err != nil || string(got) != string(edited) {
				t.Fatalf("foreign config changed after refused start: %q, %v", got, err)
			}
		})
	}
}

func TestStartStorageClusterRefusesDLMFenceHelperBeforeLocalMutation(t *testing.T) {
	c := startClusterFixture(t)
	validateStorageClusterDLMFenceHelper = func(context.Context) error { return errors.New("fixture ABI mismatch") }
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("helper failure reached native command %s %v", name, args)
		return nil, nil
	})
	if err := StartStorageClusterAt(context.Background(), t.TempDir(), c); err == nil || !strings.Contains(err.Error(), "DLM fencing helper") {
		t.Fatalf("DLM helper failure was not fail-closed: %v", err)
	}
	if _, err := os.Stat(corosyncConfigPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("helper failure wrote Corosync configuration: %v", err)
	}
}

func TestStorageClusterHealthBlocksBeforeObservationWhenDLMFenceHelperInvalid(t *testing.T) {
	setForTest(t, &validateStorageClusterDLMFenceHelper, func(context.Context) error { return errors.New("fixture DLM helper ABI mismatch") })
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("helper failure reached cluster observation %s %v", name, args)
		return nil, nil
	})
	c := clusterTestConfig()
	status, err := StorageClusterHealthAt(context.Background(), t.TempDir(), clusterTestRegistration(c), c)
	if err != nil || status.Healthy || !strings.Contains(status.Error, "DLM helper") {
		t.Fatalf("helper-invalid health = %#v, %v", status, err)
	}
}

func TestStorageClusterHealthUsesNativeLocalQuorumMemberNotRegistrationOwner(t *testing.T) {
	setForTest(t, &clusterRun, clusterRun)
	setForTest(t, &validateStorageClusterDLMFenceHelper, func(context.Context) error { return nil })
	setForTest(t, &nativeRun, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("not mounted in quorum parser fixture")
	})
	c := provenClusterConfig()
	state, owned := ownedClusterFixture(t, c)
	runtime := runtimeCloneStatusForMembers(c, "host-1", "host-2")
	registration := clusterTestRegistration(c) // Durable pool registration remains owned by host-1.
	twoOfThree := quorumToolOutput(2, 2, 3, 3, 2, "Yes", "Quorate", 1)
	for _, tc := range []struct {
		name    string
		quorum  []byte
		runtime []byte
		quorate bool
	}{
		{name: "member two observes two of three", quorum: twoOfThree, runtime: runtime, quorate: true},
		{name: "member two observes three of three", quorum: quorumToolOutput(3, 3, 3, 3, 2, "Yes", "Quorate", 1), runtime: runtimeCloneStatusForMembers(c, "host-1", "host-2", "host-3"), quorate: true},
		{name: "registration owner absent from current majority", quorum: replaceCIB(twoOfThree, "2 1 host-1\n", "3 1 host-3\n"), runtime: runtimeCloneStatusForMembers(c, "host-2", "host-3"), quorate: true},
		{name: "wrong local marker", quorum: replaceCIB(twoOfThree, "host-2 (local)", "host-1 (local)"), runtime: runtime},
		{name: "foreign local member", quorum: replaceCIB(twoOfThree, "host-2 (local)", "foreign (local)"), runtime: runtime},
		{name: "wrong local node ID", quorum: replaceCIB(twoOfThree, "Node ID: 1", "Node ID: 2"), runtime: runtime},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clusterRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
				switch name {
				case "corosync-quorumtool":
					return tc.quorum, nil
				case "cibadmin":
					return owned, nil
				case "pcs":
					return tc.runtime, nil
				}
				t.Fatalf("unexpected cluster command %q", name)
				return nil, nil
			}
			status, err := StorageClusterHealthAt(context.Background(), state, registration, c)
			if err != nil || status.Quorate != tc.quorate || status.FencingVerified != tc.quorate {
				t.Fatalf("health quorum = %#v, %v", status, err)
			}
		})
	}
}

func TestConnectClusteredISCSIEnablesAutomaticStartupOnlyAfterVerifiedMetadata(t *testing.T) {
	c := clusterTestConfig()
	s := clusterTestRegistration(c)
	var automatic, metadataVerified bool
	var cancelAfterStartup func()
	fakeClusteredISCSI(t, s, func(int) []byte { return gfs2LSBLK("/dev/fake", "3600508b400105e210000900000490000") }, func() {
		if !metadataVerified {
			t.Fatal("automatic iSCSI startup configured before GFS2 metadata validation")
		}
		automatic = true
		if cancelAfterStartup != nil {
			cancelAfterStartup()
		}
	})
	clusterRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gfs2_edit" {
			metadataVerified = true
		} else if name != "tunegfs2" {
			t.Fatalf("unexpected cluster command %s", name)
		}
		return gfs2MetadataOutput(name), nil
	}
	if _, err := ConnectClusteredISCSI(context.Background(), t.TempDir(), s, c); err != nil {
		t.Fatal(err)
	}
	if !automatic {
		t.Fatal("verified clustered session was not configured for automatic startup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelAfterStartup = cancel
	if _, err := ConnectClusteredISCSI(ctx, t.TempDir(), s, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("startup-time cancellation was accepted: %v", err)
	}
}

func TestConnectClusteredISCSIWaitsForDelayedGFS2Publication(t *testing.T) {
	c, s := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
	lsblkCalls, startupCalls, metadata := 0, 0, false
	fakeClusteredISCSI(t, s, func(call int) []byte {
		lsblkCalls = call
		if call == 1 {
			return []byte(`{"blockdevices":[]}`)
		}
		return gfs2LSBLK("/dev/fake", "3600508b400105e210000900000490000")
	}, func() {
		startupCalls++
		if !metadata {
			t.Fatal("startup set before delayed GFS2 metadata")
		}
	})
	clusterRun = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gfs2_edit" {
			metadata = true
		} else if name != "tunegfs2" {
			t.Fatalf("unexpected cluster command %s", name)
		}
		return gfs2MetadataOutput(name), nil
	}
	if _, err := ConnectClusteredISCSI(context.Background(), t.TempDir(), s, c); err != nil {
		t.Fatal(err)
	}
	if lsblkCalls != 2 || startupCalls != 1 {
		t.Fatalf("publication wait lsblk=%d startup=%d", lsblkCalls, startupCalls)
	}
}

func TestConnectClusteredISCSIRejectsWrongWWIDBeforeMetadataOrStartup(t *testing.T) {
	c, s := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
	metadata, startup := 0, 0
	fakeClusteredISCSI(t, s, func(int) []byte { return gfs2LSBLK("/dev/wrong", "3600508b400105e210000900000490001") }, func() { startup++ })
	clusterRun = func(context.Context, string, ...string) ([]byte, error) { metadata++; return nil, nil }
	if _, err := ConnectClusteredISCSI(context.Background(), t.TempDir(), s, c); err == nil {
		t.Fatal("wrong WWID was accepted")
	}
	if metadata != 0 || startup != 0 {
		t.Fatalf("wrong WWID reached metadata=%d startup=%d", metadata, startup)
	}
}

func TestConnectClusteredISCSIHonorsCanceledContextBeforeMutation(t *testing.T) {
	c, s := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
	setForTest(t, &nativeRun, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("canceled connect ran a native command")
		return nil, nil
	})
	setForTest(t, &clusterRun, func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("canceled connect read metadata")
		return nil, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ConnectClusteredISCSI(ctx, t.TempDir(), s, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled connect error=%v", err)
	}
}

func clusteredISCSICallers(t *testing.T) map[string]func(context.Context, protocol.StorageRegistration, protocol.StorageClusterConfig) error {
	return map[string]func(context.Context, protocol.StorageRegistration, protocol.StorageClusterConfig) error{
		"connect": func(ctx context.Context, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
			_, err := ConnectClusteredISCSI(ctx, t.TempDir(), s, c)
			return err
		},
		"prepare": func(ctx context.Context, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
			_, err := PrepareClusteredISCSI(ctx, t.TempDir(), s, c)
			return err
		},
		"bootstrap": func(ctx context.Context, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
			s.Path, s.Identity.ISCSI, s.Source.ISCSI.MountPath = "", nil, ""
			c.DevicePath, c.MountPath = "", ""
			_, _, err := BootstrapClusteredISCSI(ctx, t.TempDir(), s, c)
			return err
		},
	}
}

func TestClusteredISCSICallersWaitForPublicationThenRejectWrongWWID(t *testing.T) {
	for name, call := range clusteredISCSICallers(t) {
		t.Run(name, func(t *testing.T) {
			c, s := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
			lsblkCalls, metadata, startup := 0, 0, 0
			fakeClusteredISCSI(t, s, func(n int) []byte {
				lsblkCalls = n
				if n == 1 {
					return []byte(`{"blockdevices":[]}`)
				}
				return gfs2LSBLK("/dev/wrong", "3600508b400105e210000900000490001")
			}, func() { startup++ })
			clusterRun = func(context.Context, string, ...string) ([]byte, error) { metadata++; return nil, nil }
			if err := call(context.Background(), s, c); err == nil {
				t.Fatal("wrong WWID accepted")
			}
			if lsblkCalls != 2 || metadata != 0 || startup != 0 {
				t.Fatalf("publication/identity calls lsblk=%d metadata=%d startup=%d", lsblkCalls, metadata, startup)
			}
		})
	}
}

func TestClusteredISCSICallersHonorCancellationAfterLSBLK(t *testing.T) {
	for name, call := range clusteredISCSICallers(t) {
		t.Run(name, func(t *testing.T) {
			c, s := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			metadata, startup := 0, 0
			fakeClusteredISCSI(t, s, func(int) []byte {
				cancel()
				return gfs2LSBLK("/dev/fake", "3600508b400105e210000900000490000")
			}, func() { startup++ })
			clusterRun = func(context.Context, string, ...string) ([]byte, error) { metadata++; return nil, nil }
			if err := call(ctx, s, c); !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled call error=%v", err)
			}
			if metadata != 0 || startup != 0 {
				t.Fatalf("canceled call reached metadata=%d startup=%d", metadata, startup)
			}
		})
	}
}

func TestClusteredLoginWaitsForDelayedSessionWithoutRetryingLogin(t *testing.T) {
	v := &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared"}
	logins, sessions := 0, 0
	setForTest(t, &nativeRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "iscsiadm" {
			t.Fatalf("unexpected command %s", name)
		}
		if strings.Contains(strings.Join(args, " "), "--login") {
			logins++
			return nil, errors.New("iscsid starting")
		}
		if strings.Join(args, " ") == "-m session -P 1" {
			sessions++
			if sessions == 1 {
				return []byte("Target: iqn.other\nCurrent Portal: san.example.test:3260,1\n"), nil
			}
			return []byte("Target: iqn.2026-01.test:shared\nCurrent Portal: san.example.test:3260,1\n"), nil
		}
		t.Fatalf("unexpected iscsiadm args %v", args)
		return nil, nil
	})
	if err := loginClusteredISCSI(context.Background(), v); err != nil || logins != 1 || sessions < 2 {
		t.Fatalf("delayed session err=%v logins=%d sessions=%d", err, logins, sessions)
	}
}

func TestClusteredLoginRejectsCancellationAfterSuccessfulCommand(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	setForTest(t, &nativeRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name != "iscsiadm" || !strings.Contains(strings.Join(args, " "), "--login") {
			t.Fatalf("unexpected native command %s %v", name, args)
		}
		cancel()
		return nil, nil
	})
	v := &protocol.ISCSIStorageSource{Portal: "san.example.test", TargetIQN: "iqn.2026-01.test:shared"}
	if err := loginClusteredISCSI(ctx, v); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled successful login error=%v", err)
	}
}

func TestManagedClusteredAdmissionUsesContextStateRoot(t *testing.T) {
	c := clusterTestConfig()
	c.MountPath = t.TempDir()
	s := protocol.StorageRegistration{
		ID: "repo-1", PoolID: "pool-1", Generation: 1, Kind: protocol.StorageKindISCSI, Path: c.MountPath,
		Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{
			Mode: "clustered_gfs2", ClusterID: c.ClusterID, LockTable: c.LockTable,
			MountPath: c.MountPath, EligibleHostIDs: []string{"host-1", "host-2", "host-3"},
			TargetIQN: "iqn.2026-09.example.test:nex", LUN: 0, WWID: "3600140509f6f9e020ef432c841051db1", FilesystemUUID: "11111111-2222-3333-4444-555555555555",
		}},
	}
	setForTest(t, &nativeRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "lsblk" {
			t.Fatalf("unexpected native command before health: %s", name)
		}
		return []byte(`{"blockdevices":[{"path":"/dev/fake","type":"disk","uuid":"11111111-2222-3333-4444-555555555555","wwn":"3600140509f6f9e020ef432c841051db1","fstype":"gfs2","hctl":"0:0:0:0"}]}`), nil
	})
	setForTest(t, &clusterRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "tunegfs2" && name != "gfs2_edit" {
			t.Fatalf("unexpected cluster command before health: %s", name)
		}
		return gfs2MetadataOutput(name), nil
	})
	wantRoot, gotRoot := t.TempDir(), ""
	setForTest(t, &clusterHealthyAt, func(_ context.Context, stateDir string, _ protocol.StorageRegistration, _ protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
		gotRoot = stateDir
		return protocol.StorageClusterStatus{}, nil
	})
	ctx := WithStorageClusterStateDir(WithStorageClusterConfig(context.Background(), c), wantRoot)
	if _, err := managedStorageReady(ctx, s, 0); err == nil || !strings.Contains(err.Error(), "not fenced") {
		t.Fatalf("managed admission did not reach health: %v", err)
	}
	if gotRoot != wantRoot {
		t.Fatalf("managed admission used state root %q, want %q", gotRoot, wantRoot)
	}
	if _, err := managedStorageReady(WithStorageClusterConfig(context.Background(), c), s, 0); err == nil || !strings.Contains(err.Error(), "local Core state directory") {
		t.Fatalf("managed admission accepted absent state root: %v", err)
	}
}

func TestVerifyGFS2RequiresLockDLMAndEnoughJournals(t *testing.T) {
	protocolLine := "Lock protocol: lock_dlm" // Observed Debian 13 gfs2-utils output uses "Lock protocol".
	setForTest(t, &clusterRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name == "gfs2_edit" {
			return []byte("journal0: 0x1 128MB clean.\njournal1: 0x2 128MB clean.\njournal2: 0x3 128MB clean.\n"), nil
		}
		return []byte(protocolLine + "\nLock table: pool-1:repo-1\n"), nil
	})
	if err := verifyGFS2(context.Background(), "/dev/fake", "pool-1:repo-1", 3); err != nil {
		t.Fatal(err)
	}
	if err := verifyGFS2(context.Background(), "/dev/fake", "pool-1:repo-1", 4); err == nil {
		t.Fatal("undersized journal count accepted")
	}
	protocolLine = "Locking protocol: lock_nolock"
	if err := verifyGFS2(context.Background(), "/dev/fake", "pool-1:repo-1", 3); err == nil {
		t.Fatal("lock_nolock accepted")
	}
}

func TestClusteredBootstrapCopyNeverMutatesPathFreeRequest(t *testing.T) {
	request := protocol.StorageRegistration{Path: "", Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Mode: "clustered_gfs2", EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}}}
	for _, scenario := range []string{"successful-derived-registration", "later-native-failure"} {
		t.Run(scenario, func(t *testing.T) {
			resolved := cloneClusteredISCSIRegistration(request)
			resolved.Path = "/var/lib/nex-storage/repo-1"
			resolved.Source.ISCSI.MountPath = resolved.Path
			resolved.Source.ISCSI.EligibleHostIDs[0] = "changed-only-in-derived-copy"
			if request.Path != "" || request.Source.ISCSI.MountPath != "" || request.Source.ISCSI.EligibleHostIDs[0] != "host-1" {
				t.Fatalf("%s mutated caller request: %#v", scenario, request)
			}
		})
	}
}

func TestStableSCSIDevicePathUsesObservedUdevAlias(t *testing.T) {
	setForTest(t, &diskByIDPath, t.TempDir())
	device := filepath.Join(t.TempDir(), "sda")
	if err := os.WriteFile(device, nil, 0600); err != nil {
		t.Fatal(err)
	}
	// Mirrors observed Debian behavior: lsblk can report 0x600..., while udev
	// names the same device scsi-3600.... The alias must be observed, not made.
	alias := filepath.Join(diskByIDPath, "scsi-3600140509f6f9e020ef432c841051db1")
	if err := os.Symlink(device, alias); err != nil {
		t.Fatal(err)
	}
	got, err := stableSCSIDevicePath(device)
	if err != nil || got != alias {
		t.Fatalf("stable alias = %q, %v", got, err)
	}
	if err := os.Symlink(device, filepath.Join(diskByIDPath, "scsi-duplicate")); err != nil {
		t.Fatal(err)
	}
	if _, err := stableSCSIDevicePath(device); err == nil {
		t.Fatal("ambiguous stable aliases accepted")
	}
}

func TestClusterQuorumAndCIBChecksFailClosed(t *testing.T) {
	c := clusterTestConfig()
	if _, ok := clusterQuorumStatus(quorumToolOutput(3, 3, 3, 3, 2, "No", "Quorate", 2), c); ok {
		t.Fatal("non-quorate partition accepted")
	}
	q, ok := clusterQuorumStatus(quorumToolOutput(2, 2, 3, 3, 2, "Yes", "Quorate", 2), c)
	if !ok || q.totalVotes != 2 || q.localHost != "host-1" {
		t.Fatalf("realistic fenced 2/3 quorum rejected: %#v, %v", q, ok)
	}
	five := c
	five.Nodes = append(five.Nodes, protocol.StorageClusterNode{HostID: "host-4", CorosyncNodeID: 4}, protocol.StorageClusterNode{HostID: "host-5", CorosyncNodeID: 5})
	if q, ok := clusterQuorumStatus(quorumToolOutput(3, 3, 5, 5, 3, "Yes", "Quorate", 2), five); !ok || q.totalVotes != 3 {
		t.Fatalf("ordinary 3/5 majority rejected: %#v, %v", q, ok)
	}
	twoOfThree := quorumToolOutput(2, 2, 3, 3, 2, "Yes", "Quorate", 2)
	for _, raw := range [][]byte{
		quorumToolOutput(1, 1, 3, 3, 2, "Yes", "Quorate", 2),
		quorumToolOutput(2, 2, 3, 3, 1, "Yes", "Quorate", 2),
		quorumToolOutput(2, 2, 3, 3, 2, "Yes", "Quorate Qdevice", 2),
		[]byte(string(twoOfThree) + "Nodes: 2\n"),
		[]byte(string(twoOfThree) + "1 1 host-2\n"),
		replaceCIB(twoOfThree, "host-2", "unknown"),
		replaceCIB(twoOfThree, "Node ID: 2", "Node ID: 4294967296"),
	} {
		if _, ok := clusterQuorumStatus(raw, c); ok {
			t.Fatalf("unsafe quorum observation accepted: %q", raw)
		}
	}
	create := []string{"resource", "create", "nex-gfs2-repo-1", "ocf:heartbeat:Filesystem", "device=/dev/disk/by-id/scsi-disk", "directory=/var/lib/nex-storage/repo-1", "fstype=gfs2"}
	good := []byte(`<cib><configuration><resources><clone id="clone"><primitive id="nex-gfs2-repo-1" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="device" value="/dev/disk/by-id/scsi-disk"/><nvpair name="directory" value="/var/lib/nex-storage/repo-1"/><nvpair name="fstype" value="gfs2"/></instance_attributes></primitive></clone></resources><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/></cluster_property_set></crm_config></configuration></cib>`)
	if !cibHasResource(good, "nex-gfs2-repo-1", create) || !cibHasProperty(good, "stonith-enabled", "true") {
		t.Fatal("exact CIB spec rejected")
	}
	if cibHasResource(replaceCIB(good, "scsi-disk", "scsi-other"), "nex-gfs2-repo-1", create) {
		t.Fatal("stale device CIB resource accepted")
	}
	clonedCreate := append(append([]string{}, create...), "op", "monitor", "interval=20s", "on-fail=fence", "clone", "interleave=true", "ordered=true")
	cloned := []byte(`<cib><configuration><resources><clone id="clone"><meta_attributes><nvpair name="interleave" value="true"/><nvpair name="ordered" value="true"/></meta_attributes><primitive id="nex-gfs2-repo-1" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="device" value="/dev/disk/by-id/scsi-disk"/><nvpair name="directory" value="/var/lib/nex-storage/repo-1"/><nvpair name="fstype" value="gfs2"/></instance_attributes><operations><op id="nex-gfs2-repo-1-monitor-interval-20s" name="monitor" interval="20s" timeout="40s" on-fail="fence"/></operations></primitive></clone></resources></configuration></cib>`)
	if !cibHasResource(cloned, "nex-gfs2-repo-1", clonedCreate) {
		t.Fatal("exact cloned resource rejected")
	}
	if cibHasResource(replaceCIB(cloned, `on-fail="fence"`, `on-fail="restart"`), "nex-gfs2-repo-1", clonedCreate) {
		t.Fatal("wrong monitor failure policy accepted")
	}
}

func TestDLMOrderAndCloneRuntimeBindsCurrentVotingMembers(t *testing.T) {
	cib := []byte(`<cib><configuration><constraints><rsc_order id="nex-dlm-before-repo-1" first="nex-dlm-clone" then="nex-gfs2-repo-1-clone" kind="Mandatory"/></constraints></configuration></cib>`)
	if !cibHasOrder(cib, "nex-dlm-before-repo-1", "nex-dlm-clone", "nex-gfs2-repo-1-clone") {
		t.Fatal("exact DLM before GFS2 constraint rejected")
	}
	if cibHasOrder(cib, "nex-dlm-before-repo-1", "nex-gfs2-repo-1-clone", "nex-dlm-clone") {
		t.Fatal("reversed ordering constraint accepted")
	}
	c := clusterTestConfig()
	runtime := runtimeCloneStatusFixture([]byte(`<pacemaker-result><resources><clone id="nex-dlm-clone"><resource id="nex-dlm" active="true" role="Started" nodes_running_on="1"><node name="host-1" id="2"/></resource><resource id="nex-dlm" active="true" role="Started" nodes_running_on="1"><node name="host-2" id="1"/></resource><resource id="nex-dlm" active="false" role="Stopped" nodes_running_on="0"/></clone><clone id="nex-gfs2-repo-1-clone"><resource id="nex-gfs2-repo-1" active="true" role="Started" nodes_running_on="1"><node name="host-1" id="2"/></resource><resource id="nex-gfs2-repo-1" active="true" role="Started" nodes_running_on="1"><node name="host-2" id="1"/></resource><resource id="nex-gfs2-repo-1" active="false" role="Stopped" nodes_running_on="0"/></clone></resources></pacemaker-result>`))
	dlm, dlmOK := runtimeCloneActiveMembers(runtime, "nex-dlm", c, 2)
	gfs, gfsOK := runtimeCloneActiveMembers(runtime, "nex-gfs2-repo-1", c, 2)
	if !dlmOK || !gfsOK || !reflect.DeepEqual(dlm, gfs) || !dlm["host-1"] {
		t.Fatalf("current two-member clone set rejected: dlm=%#v gfs=%#v", dlm, gfs)
	}
	for name, raw := range map[string][]byte{
		"duplicate active clone member":  replaceCIB(runtime, `name="host-2" id="1"`, `name="host-1" id="2"`),
		"unknown active clone member":    replaceCIB(runtime, `name="host-2" id="1"`, `name="unknown" id="1"`),
		"failed expected clone instance": replaceCIB(runtime, `active="false" role="Stopped" nodes_running_on="0"`, `active="false" role="Stopped" nodes_running_on="0" failed="true"`),
		"missing clone safety flag":      replaceCIB(runtime, `managed="true"`, ``),
		"wrong DLM resource agent":       replaceCIB(runtime, `resource_agent="ocf:pacemaker:controld"`, `resource_agent="ocf:invalid:controld"`),
		"duplicate active state":         replaceCIB(runtime, `active="true" role="Started"`, `active="true" active="false" role="Started"`),
		"duplicate role":                 replaceCIB(runtime, `active="true" role="Started"`, `active="true" role="Started" role="Stopped"`),
		"duplicate node ID":              replaceCIB(runtime, `name="host-1" id="2"`, `name="host-1" id="2" id="1"`),
		"duplicate clone ID":             replaceCIB(runtime, `id="nex-dlm-clone"`, `id="nex-dlm-clone" id="other"`),
		"unexpected clone child":         replaceCIB(runtime, `<resource resource_agent="ocf:pacemaker:controld"`, `<foreign/><resource resource_agent="ocf:pacemaker:controld"`),
		"unexpected resource child":      replaceCIB(runtime, `<node name="host-1" id="2"/>`, `<foreign/><node name="host-1" id="2"/>`),
		"foreign clone resource":         replaceCIB(runtime, `</clone><clone `, `<resource id="foreign" active="false" role="Stopped" nodes_running_on="0"/></clone><clone `),
	} {
		if _, ok := runtimeCloneActiveMembers(raw, "nex-dlm", c, 2); ok {
			t.Fatalf("%s accepted", name)
		}
	}
	quorum, ok := clusterQuorumStatus(quorumToolOutput(2, 2, 3, 3, 2, "Yes", "Quorate", 2), c)
	mismatched := []byte(strings.ReplaceAll(string(runtime), `name="host-2" id="1"`, `name="host-3" id="3"`))
	mismatchedDLM, mismatchedDLMOK := runtimeCloneActiveMembers(mismatched, "nex-dlm", c, 2)
	mismatchedGFS, mismatchedGFSOK := runtimeCloneActiveMembers(mismatched, "nex-gfs2-repo-1", c, 2)
	if !ok || !mismatchedDLMOK || !mismatchedGFSOK || reflect.DeepEqual(mismatchedDLM, quorum.members) || !reflect.DeepEqual(mismatchedDLM, mismatchedGFS) {
		t.Fatal("clone membership mismatch against accepted quorum was not detected")
	}
}

func runtimeCloneStatusFixture(raw []byte) []byte {
	text := strings.ReplaceAll(string(raw), `<clone id="`, `<clone multi_state="false" unique="false" maintenance="false" managed="true" disabled="false" failed="false" failure_ignored="false" id="`)
	text = strings.ReplaceAll(text, `<resource id="nex-dlm"`, `<resource resource_agent="ocf:pacemaker:controld" orphaned="false" blocked="false" maintenance="false" managed="true" failed="false" failure_ignored="false" id="nex-dlm"`)
	text = strings.ReplaceAll(text, `<resource id="nex-gfs2-repo-1"`, `<resource resource_agent="ocf:heartbeat:Filesystem" orphaned="false" blocked="false" maintenance="false" managed="true" failed="false" failure_ignored="false" id="nex-gfs2-repo-1"`)
	return []byte(text)
}

// runtimeCloneStatusForMembers renders DLM, transport and GFS2 clones active on hosts.
func runtimeCloneStatusForMembers(c protocol.StorageClusterConfig, hosts ...string) []byte {
	member := map[string]bool{}
	for _, host := range hosts {
		member[host] = true
	}
	resource := func(id string) string {
		out := ""
		for _, node := range c.Nodes {
			if member[node.HostID] {
				out += fmt.Sprintf(`<resource id="%s" active="true" role="Started" nodes_running_on="1"><node name="%s" id="%d"/></resource>`, id, node.HostID, node.CorosyncNodeID)
			} else {
				out += fmt.Sprintf(`<resource id="%s" active="false" role="Stopped" nodes_running_on="0"/>`, id)
			}
		}
		return out
	}
	transport, err := storageClusterTransportFor(clusterTestRegistration(c), c)
	if err != nil {
		panic(err)
	}
	raw := runtimeCloneStatusFixture([]byte(fmt.Sprintf(`<pacemaker-result><resources><clone id="nex-dlm-clone">%s</clone><clone id="%s-clone">%s</clone><clone id="nex-gfs2-%s-clone">%s</clone></resources></pacemaker-result>`, resource("nex-dlm"), transport.resource, resource(transport.resource), c.RepositoryID, resource("nex-gfs2-"+c.RepositoryID))))
	return []byte(strings.ReplaceAll(string(raw), `<resource id="`+transport.resource+`"`, `<resource resource_agent="ocf:nex:cluster-iscsi-session" orphaned="false" blocked="false" maintenance="false" managed="true" failed="false" failure_ignored="false" id="`+transport.resource+`"`))
}

func quorumToolOutput(nodes, total, expected, highest, quorum int, quorate, flags string, localID int) []byte {
	hosts := []struct {
		id   int
		host string
	}{{1, "host-2"}, {2, "host-1"}, {3, "host-3"}, {4, "host-4"}, {5, "host-5"}}
	out := fmt.Sprintf("Quorum provider: corosync_votequorum\nNodes: %d\nNode ID: %d\nQuorate: %s\nExpected votes: %d\nHighest expected: %d\nTotal votes: %d\nQuorum: %d\nFlags: %s\nMembership information\nNodeid Votes Name\n", nodes, localID, quorate, expected, highest, total, quorum, flags)
	for i := 0; i < total && i < len(hosts); i++ {
		local := ""
		if hosts[i].id == localID {
			local = " (local)"
		}
		out += fmt.Sprintf("%d 1 %s%s\n", hosts[i].id, hosts[i].host, local)
	}
	return []byte(out)
}

func TestCIBSafetyPropertiesAndResourceDriftFailClosed(t *testing.T) {
	// A resource nvpair must never impersonate a cluster-wide crm_config property.
	wrongScope := []byte(`<cib><configuration><resources><primitive id="x"><instance_attributes><nvpair name="no-quorum-policy" value="freeze"/></instance_attributes></primitive></resources><crm_config><cluster_property_set><nvpair name="no-quorum-policy" value="ignore"/></cluster_property_set></crm_config></configuration></cib>`)
	if cibHasProperty(wrongScope, "no-quorum-policy", "freeze") {
		t.Fatal("resource property or conflicting crm_config property accepted")
	}
	duplicate := []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-enabled" value="true"/></cluster_property_set></crm_config></configuration></cib>`)
	if cibHasProperty(duplicate, "stonith-enabled", "true") {
		t.Fatal("ambiguous duplicate safety property accepted")
	}
	exact := `<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-enabled" value="true"/><nvpair name="stonith-action" value="off"/><nvpair name="startup-fencing" value="true"/><nvpair name="no-quorum-policy" value="freeze"/></cluster_property_set></crm_config></configuration></cib>`
	for _, tc := range []struct {
		name string
		cib  string
		want bool
	}{
		{"exact", exact, true},
		{"missing action defaults unsafe", strings.Replace(exact, `<nvpair name="stonith-action" value="off"/>`, "", 1), false},
		{"reboot action", strings.Replace(exact, `"stonith-action" value="off"`, `"stonith-action" value="reboot"`, 1), false},
		{"duplicate action", strings.Replace(exact, `<nvpair name="stonith-action" value="off"/>`, `<nvpair name="stonith-action" value="off"/><nvpair name="stonith-action" value="off"/>`, 1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storageClusterFencingPropertiesMatch([]byte(tc.cib)); got != tc.want {
				t.Fatalf("fencing property safety = %v, want %v", got, tc.want)
			}
		})
	}
	c := clusterTestConfig()
	create := gfs2ResourceSpec(c)
	good := []byte(`<cib><configuration><resources><clone id="clone"><meta_attributes><nvpair name="interleave" value="true"/><nvpair name="ordered" value="true"/></meta_attributes><primitive id="nex-gfs2-repo-1" class="ocf" provider="heartbeat" type="Filesystem"><instance_attributes><nvpair name="device" value="/dev/disk/by-id/scsi-3600508b400105e210000900000490000"/><nvpair name="directory" value="/var/lib/nex-storage/repo-1"/><nvpair name="fstype" value="gfs2"/></instance_attributes><operations><op id="nex-gfs2-repo-1-monitor-interval-20s" name="monitor" interval="20s" timeout="40s" on-fail="fence"/></operations></primitive></clone></resources></configuration></cib>`)
	if !cibHasResource(good, "nex-gfs2-repo-1", create) {
		t.Fatal("complete desired GFS2 resource rejected")
	}
	if cibHasResource(replaceCIB(good, `on-fail="fence"`, `on-fail="restart"`), "nex-gfs2-repo-1", create) {
		t.Fatal("resource with unsafe monitor action accepted")
	}
}

func TestReconcileStorageClusterFencingRepairsPropertiesOfCurrentCIBWithExplicitOffAction(t *testing.T) {
	c := clusterTestConfig()
	state, owned := ownedClusterFixture(t, c)
	unsafe := replaceCIB(owned, `"stonith-action" value="off"`, `"stonith-action" value="reboot"`)
	var got []string
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "cibadmin" && strings.Join(args, " ") == "--query" {
			return unsafe, nil
		}
		if name != "pcs" {
			t.Fatalf("unexpected command %s %v", name, args)
		}
		got = append(got, strings.Join(args, " "))
		return nil, nil
	})
	if err := ReconcileStorageClusterFencing(context.Background(), state, clusterTestRegistration(c), c); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"property set stonith-enabled=true",
		"property set stonith-action=off",
		"property set startup-fencing=true",
		"property set no-quorum-policy=freeze",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("fencing policy commands = %#v, want %#v", got, want)
	}
}

// Both reconcilers accept only the exact current CIB. A foreign or former
// layout is refused after one query, with no CIB or property mutation.
func TestReconcileStorageClusterAcceptsOnlyCurrentCIBWithoutMutation(t *testing.T) {
	c := clusterTestConfig()
	state, owned := ownedClusterFixture(t, c)
	registration := clusterTestRegistration(c)
	transport, err := storageClusterTransportFor(registration, c)
	if err != nil {
		t.Fatal(err)
	}
	setForTest(t, &validateStorageClusterSessionMonitor, func(context.Context, string, string, string) error { return nil })
	fence := c.Nodes[0].HostID
	mapping := `<nvpair id="` + fenceCIBRebootActionID(fence) + `" name="pcmk_reboot_action" value="off"/>`
	fenceAttributes := func(replacement string) []byte {
		text := string(owned)
		at := strings.Index(text, `id="nex-fence-`+fence+`"`)
		at += strings.Index(text[at:], `<instance_attributes>`)
		return []byte(text[:at] + replacement + text[at+len(`<instance_attributes>`):])
	}
	withoutRebootMappings := owned
	for _, node := range c.Nodes {
		withoutRebootMappings = replaceCIB(withoutRebootMappings, `<nvpair id="`+fenceCIBRebootActionID(node.HostID)+`" name="pcmk_reboot_action" value="off"/>`, "")
	}
	for _, tc := range []struct {
		name string
		cib  []byte
		ok   bool
	}{
		{"current", owned, true},
		{"unrelated", []byte(`<cib><configuration><crm_config><cluster_property_set><nvpair name="stonith-action" value="reboot"/></cluster_property_set></crm_config></configuration></cib>`), false},
		{"foreign monitor timeout", replaceCIB(owned, `timeout="40s"`, `timeout="60s"`), false},
		{"former implicit monitor timeout", replaceCIB(owned, ` timeout="40s"`, ``), false},
		{"former layout without transport", replaceCIB(replaceCIB(owned, testTransportCIBResource(transport), ""), testTransportOrderCIB(transport, c), ""), false},
		{"foreign transport target", replaceCIB(owned, `name="target" value="`+transport.target+`"`, `name="target" value="iqn.2026-01.test:foreign"`), false},
		{"transport order without start actions", replaceCIB(owned, ` first-action="start" then-action="start"`, ""), false},
		{"former layout without reboot mappings", withoutRebootMappings, false},
		{"one missing reboot mapping", replaceCIB(owned, mapping, ""), false},
		{"wrong reboot mapping", replaceCIB(owned, `pcmk_reboot_action" value="off`, `pcmk_reboot_action" value="reboot`), false},
		{"unidentified reboot mapping", replaceCIB(owned, ` id="`+fenceCIBRebootActionID(fence)+`"`, ""), false},
		{"duplicate reboot mapping", replaceCIB(owned, mapping, mapping+mapping), false},
		{"conditional reboot mapping", replaceCIB(owned, mapping, mapping+`<rule><nvpair name="pcmk_reboot_action" value="off"/></rule>`), false},
		{"inherited fence attributes", fenceAttributes(`<instance_attributes id-ref="foreign">`), false},
		{"foreign fence parameter", fenceAttributes(`<instance_attributes><nvpair id="foreign-fence-parameter" name="pcmk_off_action" value="reboot"/>`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := storageClusterCIBMatchesConfig(tc.cib, state, registration, c); got != tc.ok {
				t.Fatalf("CIB acceptance = %v, want %v", got, tc.ok)
			}
			for name, reconcile := range map[string]func(context.Context, string, protocol.StorageRegistration, protocol.StorageClusterConfig) error{"fencing": ReconcileStorageClusterFencing, "transport": ReconcileStorageClusterTransport} {
				var calls []string
				setForTest(t, &clusterRun, func(_ context.Context, command string, args ...string) ([]byte, error) {
					calls = append(calls, command+" "+strings.Join(args, " "))
					if command == "cibadmin" && strings.Join(args, " ") == "--query" {
						return tc.cib, nil
					}
					t.Fatalf("%s reconcile reached mutation %s %v", name, command, args)
					return nil, nil
				})
				if err := reconcile(context.Background(), state, registration, c); (err == nil) != tc.ok || !reflect.DeepEqual(calls, []string{"cibadmin --query"}) {
					t.Fatalf("%s reconcile err=%v calls=%#v", name, err, calls)
				}
			}
		})
	}
}

func TestGFS2MonitorAcceptsOnlyCurrentCIB(t *testing.T) {
	c := clusterTestConfig()
	state, owned := ownedClusterFixture(t, c)
	monitor := `<op id="` + gfs2MonitorID(c) + `" name="monitor" interval="20s" timeout="40s" on-fail="fence"/>`
	lifecycle := `<op id="nex-gfs2-repo-1-start-interval-0s" name="start" interval="0s" timeout="60s"/><op id="nex-gfs2-repo-1-stop-interval-0s" name="stop" interval="0s" timeout="60s"/>`
	for _, tc := range []struct {
		name, op string
		want     bool
	}{
		{"current", monitor, true},
		{"current with canonical lifecycle operations", monitor + lifecycle, true},
		{"implicit timeout", strings.Replace(monitor, ` timeout="40s"`, ``, 1), false},
		{"twenty-second timeout", strings.Replace(monitor, `timeout="40s"`, `timeout="20s"`, 1), false},
		{"millisecond timeout", strings.Replace(monitor, `timeout="40s"`, `timeout="20000ms"`, 1), false},
		{"arbitrary timeout", strings.Replace(monitor, `timeout="40s"`, `timeout="60s"`, 1), false},
		{"wrong failure action", strings.Replace(monitor, `on-fail="fence"`, `on-fail="restart"`, 1), false},
		{"explicit depth", strings.Replace(monitor, `on-fail="fence"`, `on-fail="fence" depth="0"`, 1), false},
		{"unknown attribute", strings.Replace(monitor, `on-fail="fence"`, `on-fail="fence" enabled="true"`, 1), false},
		{"duplicate monitor", monitor + monitor, false},
		{"extra operation", monitor + `<op id="nex-gfs2-repo-1-start-0" name="start" interval="0" timeout="60s"/>`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := replaceCIB(owned, monitor, tc.op)
			if gfs2MonitorCurrent(raw, c) != tc.want || storageClusterCIBMatchesConfig(raw, state, clusterTestRegistration(c), c) != tc.want {
				t.Fatalf("monitor acceptance != %v", tc.want)
			}
		})
	}
}

// Initial activation stages the transport clone and then its order only
// through version-guarded CIB patches, never pcs mutations.
func TestStageStorageTransportAddsOnlyVersionedCloneThenOrder(t *testing.T) {
	c := clusterTestConfig()
	state, owned := ownedClusterFixture(t, c)
	registration := clusterTestRegistration(c)
	transport, err := storageClusterTransportFor(registration, c)
	if err != nil {
		t.Fatal(err)
	}
	current := replaceCIB(replaceCIB(owned, testTransportCIBResource(transport), ""), testTransportOrderCIB(transport, c), "")
	if storageClusterCIBMatchesConfig(current, state, registration, c) {
		t.Fatal("CIB without transport was treated as current")
	}
	var next []byte
	patches := 0
	setForTest(t, &clusterDiff, func(_ context.Context, args ...string) ([]byte, int, error) {
		for i := range args {
			if args[i] == "--new-string" && i+1 < len(args) {
				next = []byte(args[i+1])
			}
		}
		return []byte(`<diff format="2"/>`), 1, nil
	})
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "cibadmin" && strings.Join(args, " ") == "--query":
			return current, nil
		case name == "cibadmin" && len(args) > 0 && args[0] == "--patch":
			patches++
			if next == nil {
				t.Fatal("patch had no versioned replacement")
			}
			current, next = next, nil
			return nil, nil
		case name == "pcs" && strings.Join(args, " ") == "status xml":
			return runtimeCloneStatusForMembers(c, "host-1", "host-2", "host-3"), nil
		}
		t.Fatalf("unexpected command %s %v", name, args)
		return nil, nil
	})
	ctx := context.Background()
	if err := stageStorageTransportClone(ctx, transport); err != nil {
		t.Fatal(err)
	}
	if err := waitStorageTransportClone(ctx, transport, c); err != nil {
		t.Fatal(err)
	}
	if err := stageStorageTransportOrder(ctx, transport, c); err != nil {
		t.Fatal(err)
	}
	if patches != 2 || !storageClusterCIBMatchesConfig(current, state, registration, c) {
		t.Fatalf("transport staging patches=%d current=%v", patches, storageClusterCIBMatchesConfig(current, state, registration, c))
	}
}

func TestGFS2ApplyRefusesTransportOrderWithoutCanonicalActions(t *testing.T) {
	c := clusterTestConfig()
	_, owned := ownedClusterFixture(t, c)
	omitted := replaceCIB(owned, ` first-action="start" then-action="start"`, "")
	enables := 0
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch {
		case name == "cibadmin" && strings.Join(args, " ") == "--query":
			return omitted, nil
		case name == "pcs" && strings.Join(args, " ") == "status xml":
			return runtimeCloneStatusForMembers(c, "host-1", "host-2", "host-3"), nil
		case name == "pcs" && len(args) >= 3 && args[0] == "resource" && args[1] == "enable":
			enables++
			return nil, nil
		}
		t.Fatalf("unexpected native command %s %v", name, args)
		return nil, nil
	})
	if err := applyGFS2ClusterResources(context.Background(), clusterTestRegistration(c), c); err == nil || enables != 0 {
		t.Fatalf("GFS2 apply accepted transport order without canonical start actions: enables=%d err=%v", enables, err)
	}
}

func TestApplyStorageClusterWithRegistrationRejectsUnavailableSessionMonitorBeforeMutation(t *testing.T) {
	c, registration := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
	setForTest(t, &validateStorageClusterSessionMonitor, func(context.Context, string, string, string) error {
		return errors.New("fixture monitor unavailable")
	})
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		t.Fatalf("unavailable monitor reached native mutation %s %v", name, args)
		return nil, nil
	})
	if err := ApplyStorageClusterWithRegistrationAt(context.Background(), t.TempDir(), registration, c); err == nil || !strings.Contains(err.Error(), "session monitor") {
		t.Fatalf("unavailable monitor did not fail closed: %v", err)
	}
}

func TestStorageClusterTransportReusesSessionAcrossLUNs(t *testing.T) {
	c1, s1 := clusterTestConfig(), clusterTestRegistration(clusterTestConfig())
	c2 := c1
	c2.RepositoryID, c2.LockTable, c2.DevicePath, c2.MountPath = "repo-2", "pool-1:repo-2", "/dev/disk/by-id/scsi-3600508b400105e210000900000490001", "/var/lib/nex-storage/repo-2"
	s2 := s1
	s2.ID, s2.Path = c2.RepositoryID, c2.MountPath
	s2.Source.ISCSI = &protocol.ISCSIStorageSource{Portal: s1.Source.ISCSI.Portal, TargetIQN: s1.Source.ISCSI.TargetIQN, LUN: 2, WWID: "3600508b400105e210000900000490001", FilesystemUUID: "12345678-1234-1234-1234-123456789abd", MountPath: c2.MountPath, Mode: "clustered_gfs2", ClusterID: c2.ClusterID, LockTable: c2.LockTable, EligibleHostIDs: []string{"host-1", "host-2", "host-3"}}
	t1, err := storageClusterTransportFor(s1, c1)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := storageClusterTransportFor(s2, c2)
	if err != nil {
		t.Fatal(err)
	}
	if t1 != t2 {
		t.Fatalf("same portal/IQN produced distinct transport sessions: %#v %#v", t1, t2)
	}
	if storageTransportOrderID(t1, c1) == storageTransportOrderID(t2, c2) {
		t.Fatal("two GFS2 repositories shared a transport ordering identity")
	}
}

func testCIBResource(spec []string) string {
	id, agent := spec[2], spec[3]
	parts := strings.Split(agent, ":")
	attrs := ` id="` + html.EscapeString(id) + `"`
	if len(parts) == 3 {
		attrs += ` class="` + html.EscapeString(parts[0]) + `" provider="` + html.EscapeString(parts[1]) + `" type="` + html.EscapeString(parts[2]) + `"`
	} else {
		attrs += ` type="` + html.EscapeString(agent) + `"`
	}
	params, opName, opAttrs, cloneAttrs := []string{}, "", []string{}, []string{}
	for i := 4; i < len(spec); i++ {
		switch spec[i] {
		case "op":
			if i+1 < len(spec) {
				opName, i = spec[i+1], i+1
				for i+1 < len(spec) && spec[i+1] != "clone" {
					i++
					opAttrs = append(opAttrs, spec[i])
				}
			}
		case "clone":
			cloneAttrs = append(cloneAttrs, spec[i+1:]...)
			i = len(spec)
		default:
			params = append(params, spec[i])
		}
	}
	nvpairs := ""
	for _, pair := range params {
		if key, value, ok := strings.Cut(pair, "="); ok {
			pairID := ""
			if key == "pcmk_reboot_action" {
				pairID = ` id="` + html.EscapeString(fenceCIBRebootActionID(strings.TrimPrefix(id, "nex-fence-"))) + `"`
			}
			nvpairs += `<nvpair` + pairID + ` name="` + html.EscapeString(key) + `" value="` + html.EscapeString(value) + `"/>`
		}
	}
	primitive := `<primitive` + attrs + `><instance_attributes>` + nvpairs + `</instance_attributes>`
	if opName != "" {
		primitive += `<operations><op id="` + html.EscapeString(id) + `-` + html.EscapeString(opName) + `-interval-20s" name="` + html.EscapeString(opName) + `"`
		for _, pair := range opAttrs {
			if key, value, ok := strings.Cut(pair, "="); ok {
				primitive += ` ` + html.EscapeString(key) + `="` + html.EscapeString(value) + `"`
			}
		}
		primitive += `/></operations>`
	}
	primitive += `</primitive>`
	if len(cloneAttrs) == 0 {
		return primitive
	}
	meta := ""
	for _, pair := range cloneAttrs {
		if key, value, ok := strings.Cut(pair, "="); ok {
			meta += `<nvpair name="` + html.EscapeString(key) + `" value="` + html.EscapeString(value) + `"/>`
		}
	}
	return `<clone id="clone-` + html.EscapeString(id) + `"><meta_attributes>` + meta + `</meta_attributes>` + primitive + `</clone>`
}

func TestApplyClusterOrderIsIdempotent(t *testing.T) {
	var creates int
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "cibadmin" {
			if creates == 0 {
				return []byte(`<cib/>`), nil
			}
			return []byte(`<cib><configuration><constraints><rsc_order id="nex-dlm-before-repo-1" first="nex-dlm-clone" then="nex-gfs2-repo-1-clone" kind="Mandatory"/></constraints></configuration></cib>`), nil
		}
		if name == "pcs" && len(args) > 2 && args[0] == "constraint" && args[1] == "order" {
			creates++
		}
		return nil, nil
	})
	for range 2 {
		if err := applyClusterOrder(context.Background(), "nex-dlm-before-repo-1", "nex-dlm-clone", "nex-gfs2-repo-1-clone"); err != nil {
			t.Fatal(err)
		}
	}
	if creates != 1 {
		t.Fatalf("created %d ordering constraints, want exactly one", creates)
	}
}

func TestGFS2EnableWaitsForDisabledResourceAndExactOrder(t *testing.T) {
	for _, tc := range []struct {
		name                string
		startDLM, startGFS  bool
		startOrder, failDLM bool
		failOrder           bool
		wantEnable, wantErr bool
	}{
		// A fixture with no versioned CIB/transport clone cannot activate GFS2:
		// the transport monitor is deliberately a prerequisite.
		{"fresh", false, false, false, false, false, false, true},
		{"dlm failure", false, false, false, true, false, false, true},
		{"order failure", true, true, false, false, true, false, true},
		{"interrupted resume", true, true, false, false, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := clusterTestConfig()
			dlm, gfs, order := tc.startDLM, tc.startGFS, tc.startOrder
			enables := 0
			setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
				if name == "cibadmin" {
					return clusterCIBFixture(c, dlm, gfs, order), nil
				}
				if name != "pcs" {
					return nil, nil
				}
				switch {
				case len(args) >= 3 && args[0] == "resource" && args[1] == "create":
					if args[2] != "nex-dlm" {
						gfs = true
					} else if tc.failDLM {
						return nil, fmt.Errorf("dlm create failed")
					} else {
						dlm = true
					}
				case len(args) >= 2 && args[0] == "constraint" && args[1] == "order":
					if tc.failOrder {
						return nil, fmt.Errorf("order failed")
					}
					order = true
				case len(args) >= 3 && args[0] == "resource" && args[1] == "enable":
					enables++
					gfs = false
				}
				return nil, nil
			})
			err := applyGFS2ClusterResources(context.Background(), clusterTestRegistration(c), c)
			if (err != nil) != tc.wantErr || (enables == 1) != tc.wantEnable {
				t.Fatalf("error=%v wantErr=%v enable calls=%d wantEnable=%v", err, tc.wantErr, enables, tc.wantEnable)
			}
		})
	}
}

func TestGFS2ApplyRefusesEnabledUnorderedExistingResource(t *testing.T) {
	c := clusterTestConfig()
	active := replaceCIB(clusterCIBFixture(c, true, true, false), `<meta_attributes><nvpair name="target-role" value="Stopped"/></meta_attributes>`, "")
	enables := 0
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "cibadmin" {
			return active, nil
		}
		if name == "pcs" && len(args) >= 3 && args[0] == "resource" && args[1] == "enable" {
			enables++
		}
		return nil, nil
	})
	if err := applyGFS2ClusterResources(context.Background(), clusterTestRegistration(c), c); err == nil || enables != 0 {
		t.Fatalf("enabled unordered GFS2 resource accepted: enables=%d err=%v", enables, err)
	}
}

func TestClusterResourceConflictAcceptsOnlyExactPeerCreation(t *testing.T) {
	calls := 0
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "cibadmin" {
			calls++
			if calls == 1 {
				return []byte(`<cib/>`), nil
			}
			return clusterCIBFixture(clusterTestConfig(), true, false, false), nil
		}
		if name == "pcs" {
			return nil, fmt.Errorf("already exists")
		}
		return nil, nil
	})
	if err := applyClusterResource(context.Background(), "nex-dlm", dlmResourceSpec()); err != nil {
		t.Fatalf("exact peer creation was not reconciled: %v", err)
	}
}

func TestCIBResourceChecksArePanicFreeAndRecognizeDisabledCloneMeta(t *testing.T) {
	c := clusterTestConfig()
	fixture := clusterCIBFixture(c, false, true, false)
	if cibHasResource(fixture, "nex-gfs2-"+c.RepositoryID, gfs2ResourceSpec(c)) {
		t.Fatal("disabled clone accepted as enabled")
	}
	if !cibHasResource(fixture, "nex-gfs2-"+c.RepositoryID, gfs2DisabledResourceSpec(c)) {
		t.Fatal("Pacemaker clone-level target-role was not recognized")
	}
	// A fence render failure previously produced this empty create slice and
	// crashed health inspection with create[4:]. It must be an unhealthy miss.
	if cibHasResource([]byte(`<cib/>`), "nex-fence-host-1", nil) {
		t.Fatal("empty resource spec accepted")
	}
	text := string(fixture)
	start, end := strings.Index(text, `<clone `), strings.Index(text, `</clone>`)
	if start < 0 || end < start {
		t.Fatal("test fixture has no clone")
	}
	if cibHasResource(replaceCIB(fixture, `</resources>`, text[start:end+len(`</clone>`)]+`</resources>`), "nex-gfs2-"+c.RepositoryID, gfs2DisabledResourceSpec(c)) {
		t.Fatal("duplicate primitive identity accepted")
	}
}

func clusterCIBFixture(c protocol.StorageClusterConfig, dlm, disabledGFS, order bool) []byte {
	resources := ""
	if dlm {
		resources += `<clone id="nex-dlm-clone"><meta_attributes><nvpair name="interleave" value="true"/><nvpair name="ordered" value="true"/></meta_attributes><primitive id="nex-dlm" class="ocf" provider="pacemaker" type="controld"><operations><op name="monitor" interval="30s" on-fail="fence"/></operations></primitive></clone>`
	}
	if disabledGFS {
		resources += fmt.Sprintf(`<clone id="nex-gfs2-%s-clone"><meta_attributes><nvpair name="interleave" value="true"/><nvpair name="ordered" value="true"/></meta_attributes><primitive id="nex-gfs2-%s" class="ocf" provider="heartbeat" type="Filesystem"><meta_attributes><nvpair name="target-role" value="Stopped"/></meta_attributes><instance_attributes><nvpair name="device" value="%s"/><nvpair name="directory" value="%s"/><nvpair name="fstype" value="gfs2"/></instance_attributes><operations><op id="nex-gfs2-%s-monitor-interval-20s" name="monitor" interval="20s" timeout="40s" on-fail="fence"/></operations></primitive></clone>`, c.RepositoryID, c.RepositoryID, c.DevicePath, c.MountPath, c.RepositoryID)
	}
	constraints := ""
	if order {
		constraints = fmt.Sprintf(`<rsc_order id="nex-dlm-before-%s" first="nex-dlm-clone" then="nex-gfs2-%s-clone" kind="Mandatory"/>`, c.RepositoryID, c.RepositoryID)
	}
	return []byte(`<cib><configuration><resources>` + resources + `</resources><constraints>` + constraints + `</constraints></configuration></cib>`)
}

func TestFenceCIBUsesProofCredentialAndPinnedHostKeyPaths(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, "storage-cluster", "fence-credentials")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"identity", "hostkey.known_hosts"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	node := protocol.StorageClusterNode{HostID: "host-1", Fence: protocol.StorageClusterFence{Kind: protocol.FenceVirsh, Endpoint: "fence.example.test", TargetID: "11111111-2222-3333-4444-555555555555", CredentialRef: "identity", SSHLogin: "fence", SSHPort: 22, SSHHostKeyRef: "hostkey"}}
	args, err := fenceCIBCreateArgs(state, node)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, "\n")
	for _, want := range []string{"fence_virsh", "ip=fence.example.test", "username=fence", "ssh=1", "identity_file=" + filepath.Join(dir, "identity"), "ssh_options=-oUserKnownHostsFile=" + filepath.Join(dir, "hostkey.known_hosts"), "-oStrictHostKeyChecking=yes", "-oGlobalKnownHostsFile=/dev/null", fenceVirshPromptOption, "pcmk_host_map=host-1:11111111-2222-3333-4444-555555555555", "pcmk_reboot_action=off", "pcmk_host_list=host-1"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("CIB fence arguments omit %q: %v", want, args)
		}
	}
	if strings.Contains(joined, "plug=11111111") {
		t.Fatalf("static virsh plug overrides pcmk_host_map: %v", args)
	}
	if strings.Contains(joined, "login=") {
		t.Fatalf("deprecated/unsupported virsh login parameter emitted: %v", args)
	}
}

func TestCorosyncUnconfiguredMarkerIsExplicitAndEmpty(t *testing.T) {
	state := t.TempDir()
	stock := []byte("totem { cluster_name: debian }\n")
	if corosyncUnconfiguredMarker(state, stock) {
		t.Fatal("missing marker accepted")
	}
	writeCorosyncMarker(t, state, stock)
	if !corosyncUnconfiguredMarker(state, stock) {
		t.Fatal("matching pristine marker rejected")
	}
	if corosyncUnconfiguredMarker(state, []byte("totem { cluster_name: edited }\n")) {
		t.Fatal("marker accepted after config edit")
	}
}

// The image edition has no packaging script to write the marker: Debian's file arrives from the layer's read-only
// factory copy, which is the only thing that vouches for it. An edited file is still an administrator's.
func TestCorosyncUnconfiguredMarkerAcceptsOnlyTheLayersFactoryCopy(t *testing.T) {
	state := t.TempDir()
	factory := filepath.Join(t.TempDir(), "corosync.conf")
	stock := []byte("totem { cluster_name: debian }\n")
	if err := os.WriteFile(factory, stock, 0o644); err != nil {
		t.Fatal(err)
	}
	previous := corosyncFactoryConfigPath
	corosyncFactoryConfigPath = factory
	t.Cleanup(func() { corosyncFactoryConfigPath = previous })
	if !corosyncUnconfiguredMarker(state, stock) {
		t.Fatal("a file identical to the layer's factory copy was refused as administrator configuration")
	}
	if corosyncUnconfiguredMarker(state, []byte("totem { cluster_name: edited }\n")) {
		t.Fatal("an edited file was accepted against the factory copy")
	}
	corosyncFactoryConfigPath = filepath.Join(t.TempDir(), "absent")
	if corosyncUnconfiguredMarker(state, stock) {
		t.Fatal("a host without a factory copy and without a marker accepted the file")
	}
}

func resetGFS2MetadataFlights(t *testing.T) {
	t.Helper()
	gfs2MetadataFlights.Lock()
	gfs2MetadataFlights.byDevice = map[string]*gfs2MetadataFlight{}
	gfs2MetadataFlights.Unlock()
	t.Cleanup(func() {
		gfs2MetadataFlights.Lock()
		defer gfs2MetadataFlights.Unlock()
		if len(gfs2MetadataFlights.byDevice) != 0 {
			t.Fatalf("GFS2 metadata flights leaked: %d", len(gfs2MetadataFlights.byDevice))
		}
	})
}

func gfs2MetadataOutput(name string) []byte {
	if name == "tunegfs2" {
		return []byte("Lock protocol: lock_dlm\nLock table: pool-1:repo-1\n")
	}
	return []byte("journal0: clean\njournal1: clean\njournal2: clean\n")
}

func TestGFS2MetadataCoalescesOnlyConcurrentSameDeviceReads(t *testing.T) {
	resetGFS2MetadataFlights(t)
	var mu sync.Mutex
	calls := 0
	firstStarted := make(chan struct{})
	secondNative := make(chan struct{})
	release := make(chan struct{})
	setForTest(t, &clusterRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-release
		} else if call == 2 {
			select {
			case secondNative <- struct{}{}:
			default:
			}
		}
		return gfs2MetadataOutput(name), nil
	})
	result := make(chan error, 2)
	go func() { _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); result <- err }()
	<-firstStarted
	go func() { _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); result <- err }()
	select {
	case <-secondNative:
		t.Fatal("concurrent same-device follower started a duplicate native read")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 2 {
		t.Fatalf("same-device native calls = %d, want tunegfs2 and gfs2_edit once", got)
	}
	if _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	got = calls
	mu.Unlock()
	if got != 4 {
		t.Fatalf("post-completion read reused a result: native calls = %d", got)
	}
}

func TestGFS2MetadataKeepsDifferentDevicesIndependent(t *testing.T) {
	resetGFS2MetadataFlights(t)
	var mu sync.Mutex
	calls := map[string]int{}
	entered := make(chan string, 2)
	release := make(chan struct{})
	setForTest(t, &clusterRun, func(_ context.Context, name string, args ...string) ([]byte, error) {
		device := args[len(args)-1]
		mu.Lock()
		calls[device]++
		call := calls[device]
		mu.Unlock()
		if call == 1 {
			entered <- device
			<-release
		}
		return gfs2MetadataOutput(name), nil
	})
	result := make(chan error, 2)
	for _, device := range []string{"/dev/one", "/dev/two"} {
		go func(device string) { _, _, err := gfs2Metadata(context.Background(), device); result <- err }(device)
	}
	seen := map[string]bool{}
	for range 2 {
		seen[<-entered] = true
	}
	if !seen["/dev/one"] || !seen["/dev/two"] {
		t.Fatalf("different-device native reads did not both enter: %#v", seen)
	}
	close(release)
	for range 2 {
		if err := <-result; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if calls["/dev/one"] != 2 || calls["/dev/two"] != 2 {
		t.Fatalf("different-device calls = %#v", calls)
	}
}

func TestGFS2MetadataFollowerUsesOwnContextError(t *testing.T) {
	for _, test := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    error
	}{
		{
			name: "canceled",
			context: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, func() {}
			},
			want: context.Canceled,
		},
		{
			name: "deadline exceeded",
			context: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			want: context.DeadlineExceeded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetGFS2MetadataFlights(t)
			firstStarted := make(chan struct{})
			release := make(chan struct{})
			setForTest(t, &clusterRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
				select {
				case <-firstStarted:
				default:
					close(firstStarted)
					<-release
				}
				return gfs2MetadataOutput(name), nil
			})
			leader := make(chan error, 1)
			go func() { _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); leader <- err }()
			<-firstStarted
			ctx, cancel := test.context()
			defer cancel()
			if _, _, err := gfs2Metadata(ctx, "/dev/fake"); !errors.Is(err, test.want) {
				t.Fatalf("follower error = %v, want %v", err, test.want)
			}
			close(release)
			if err := <-leader; err != nil {
				t.Fatalf("leader error = %v", err)
			}
		})
	}
}

func TestGFS2MetadataFailedLeaderLetsLiveFollowerReadOnce(t *testing.T) {
	resetGFS2MetadataFlights(t)
	var mu sync.Mutex
	calls := 0
	firstStarted := make(chan struct{})
	secondNative := make(chan struct{})
	release := make(chan struct{})
	setForTest(t, &clusterRun, func(_ context.Context, name string, _ ...string) ([]byte, error) {
		mu.Lock()
		calls++
		call := calls
		mu.Unlock()
		if call == 1 {
			close(firstStarted)
			<-release
			return nil, errors.New("leader read failed")
		}
		if call == 2 {
			select {
			case secondNative <- struct{}{}:
			default:
			}
		}
		return gfs2MetadataOutput(name), nil
	})
	leader := make(chan error, 1)
	follower := make(chan error, 1)
	go func() { _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); leader <- err }()
	<-firstStarted
	go func() { _, _, err := gfs2Metadata(context.Background(), "/dev/fake"); follower <- err }()
	select {
	case <-secondNative:
		t.Fatal("follower bypassed failed in-flight leader")
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-leader; err == nil {
		t.Fatal("leader failure was accepted")
	}
	if err := <-follower; err != nil {
		t.Fatalf("live follower did not perform one fresh read: %v", err)
	}
	mu.Lock()
	got := calls
	mu.Unlock()
	if got != 3 {
		t.Fatalf("native calls after failed leader = %d, want one failed leader call plus follower pair", got)
	}
}
