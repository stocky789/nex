// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Clustered iSCSI is deliberately a thin adapter around Debian's cluster stack.
// Corosync, DLM and Pacemaker make membership, locking and fencing decisions; Nex
// validates the accepted Core-owned intent and never implements its own election or lock.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

var clusterRun = run
var clusterDiff = runClusterDiff
var validateStorageClusterDLMFenceHelper = ValidateStorageClusterDLMFenceHelper
var validateStorageClusterSessionMonitor = ValidateStorageClusterSessionMonitor
var corosyncConfigPath = "/etc/corosync/corosync.conf"
var clusterHealthyAt = StorageClusterHealthAt
var diskByIDPath = "/dev/disk/by-id"

// BootstrapClusteredISCSI turns a master-approved path-free request into Core
// observed identity/device/mount configuration. It accepts neither client mount
// paths nor fencing proof, and it never formats or mounts the GFS2 filesystem.
func BootstrapClusteredISCSI(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) (protocol.StorageRegistration, protocol.StorageClusterConfig, error) {
	if !protocol.IsClusteredISCSI(s) || s.Source.ISCSI == nil || s.Path != "" || s.Source.ISCSI.MountPath != "" || len(c.FenceProofs) != 0 {
		return s, c, errors.New("cluster bootstrap needs a path-free clustered iSCSI request without fencing proof")
	}
	if (c.ClusterID != "" && s.Source.ISCSI.ClusterID != "" && c.ClusterID != s.Source.ISCSI.ClusterID) || (c.LockTable != "" && s.Source.ISCSI.LockTable != "" && c.LockTable != s.Source.ISCSI.LockTable) || c.PoolID != s.PoolID || c.RepositoryID != s.ID || c.Generation != s.Generation {
		return s, c, errors.New("cluster bootstrap configuration does not match registration")
	}
	if err := ctx.Err(); err != nil {
		return s, c, err
	}
	resolved := cloneClusteredISCSIRegistration(s)
	resolved.Source.ISCSI.MountPath, resolved.Path = remoteMountPath(s), remoteMountPath(s)
	if _, err := configureISCSICHAP(context.WithValue(ctx, storageOwnerRootKey{}, filepath.Join(stateDir, "iscsi-owners")), resolved, resolved.Source.ISCSI); err != nil {
		return s, c, err
	}
	if err := loginClusteredISCSI(ctx, resolved.Source.ISCSI); err != nil {
		return s, c, err
	}
	info, err := waitClusteredISCSIInfo(ctx, resolved)
	if err != nil {
		return s, c, err
	}
	lockTable, journals, err := gfs2Metadata(ctx, info.Path)
	if err != nil {
		return s, c, err
	}
	parts := strings.SplitN(lockTable, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || journals < len(resolved.Source.ISCSI.EligibleHostIDs) {
		return s, c, errors.New("existing GFS2 lock table or journal count is incompatible")
	}
	if (resolved.Source.ISCSI.LockTable != "" && resolved.Source.ISCSI.LockTable != lockTable) || (resolved.Source.ISCSI.ClusterID != "" && resolved.Source.ISCSI.ClusterID != parts[0]) {
		return s, c, errors.New("requested clustered iSCSI identity does not match existing GFS2")
	}
	resolved.Source.ISCSI.LockTable, resolved.Source.ISCSI.ClusterID = lockTable, parts[0]
	c.LockTable, c.ClusterID = lockTable, parts[0]
	devicePath, err := stableSCSIDevicePath(info.Path)
	if err != nil {
		return s, c, err
	}
	c.DevicePath, c.MountPath = devicePath, resolved.Path
	if uint64(journals) > uint64(^uint32(0)) {
		return s, c, errors.New("GFS2 journal count overflows storage identity")
	}
	resolved.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: resolved.Source.ISCSI.TargetIQN, LUN: resolved.Source.ISCSI.LUN, WWID: info.WWID, FilesystemUUID: resolved.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: resolved.Source.ISCSI.LockTable, GFS2Journals: uint32(journals)}
	if err := c.ValidFor(resolved); err != nil {
		return s, c, err
	}
	return resolved, c, nil
}

// stableSCSIDevicePath returns the actual udev scsi-* alias that resolves to
// the already WWID/HCTL-validated LUN. Do not synthesize it from lsblk's WWN:
// Debian can report 0x600... while udev names that same device scsi-3600....
func stableSCSIDevicePath(device string) (string, error) {
	want, err := filepath.EvalSymlinks(device)
	if err != nil {
		return "", fmt.Errorf("resolve validated iSCSI device: %w", err)
	}
	candidates, err := filepath.Glob(filepath.Join(diskByIDPath, "scsi-*"))
	if err != nil {
		return "", err
	}
	var found string
	for _, candidate := range candidates {
		info, err := os.Lstat(candidate)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || resolved != want {
			continue
		}
		if found != "" {
			return "", errors.New("validated iSCSI device has ambiguous stable scsi aliases")
		}
		found = candidate
	}
	if found == "" {
		return "", errors.New("validated iSCSI device has no stable scsi by-id alias")
	}
	return found, nil
}

// cloneClusteredISCSIRegistration prevents bootstrap-derived fields from
// escaping into the caller's path-free request when a later native stage fails.
func cloneClusteredISCSIRegistration(s protocol.StorageRegistration) protocol.StorageRegistration {
	resolved := s
	if s.Source.ISCSI != nil {
		iscsi := *s.Source.ISCSI
		iscsi.EligibleHostIDs = append([]string(nil), s.Source.ISCSI.EligibleHostIDs...)
		resolved.Source.ISCSI = &iscsi
	}
	return resolved
}

// PrepareClusteredISCSI creates or verifies only Nex-owned iSCSI session state,
// observes the existing GFS2 superblock, and then proves Pacemaker owns its mount.
// It never calls mkfs, mount, or creates an ordinary systemd mount unit.
func PrepareClusteredISCSI(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) (storageAccess, error) {
	if err := c.ValidFor(s); err != nil {
		return storageAccess{}, err
	}
	if err := validateClusteredRegistration(s, c); err != nil {
		return storageAccess{}, err
	}
	if err := ctx.Err(); err != nil {
		return storageAccess{}, err
	}
	v := s.Source.ISCSI
	if _, err := configureISCSICHAP(context.WithValue(ctx, storageOwnerRootKey{}, filepath.Join(stateDir, "iscsi-owners")), s, v); err != nil {
		return storageAccess{}, err
	}
	if err := loginClusteredISCSI(ctx, v); err != nil {
		return storageAccess{}, err
	}
	info, err := waitClusteredISCSIInfo(ctx, s)
	if err != nil {
		return storageAccess{}, err
	}
	if err := verifyGFS2(ctx, info.Path, v.LockTable, len(v.EligibleHostIDs)); err != nil {
		return storageAccess{}, err
	}
	return ValidateClusteredISCSIAt(ctx, stateDir, s, c)
}

// ConnectClusteredISCSI is the pre-Pacemaker member stage: establish only the
// owned iSCSI session and verify the exact existing GFS2 superblock. It never
// mounts, starts Corosync, or touches CIB resources.
func ConnectClusteredISCSI(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) (protocol.StorageRegistration, error) {
	if err := c.ValidFor(s); err != nil {
		return s, err
	}
	if err := validateClusteredRegistration(s, c); err != nil {
		return s, err
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	v := s.Source.ISCSI
	if _, err := configureISCSICHAP(context.WithValue(ctx, storageOwnerRootKey{}, filepath.Join(stateDir, "iscsi-owners")), s, v); err != nil {
		return s, err
	}
	if err := loginClusteredISCSI(ctx, v); err != nil {
		return s, err
	}
	info, err := waitClusteredISCSIInfo(ctx, s)
	if err != nil {
		return s, err
	}
	lockTable, journals, err := gfs2Metadata(ctx, info.Path)
	if err != nil || lockTable != v.LockTable || journals < len(v.EligibleHostIDs) {
		return s, errors.New("GFS2 lock protocol, lock table or journals do not match registration")
	}
	if uint64(journals) > uint64(^uint32(0)) {
		return s, errors.New("GFS2 journal count overflows storage identity")
	}
	// Only an exact, already authenticated and metadata-verified Nex-owned
	// target may be resumed by open-iscsi on a later boot.
	if _, err := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--op", "update", "-n", "node.startup", "-v", "automatic"); err != nil {
		return s, fmt.Errorf("configure clustered iSCSI startup: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return s, err
	}
	s.Identity.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: v.TargetIQN, LUN: v.LUN, WWID: info.WWID, FilesystemUUID: v.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: v.LockTable, GFS2Journals: uint32(journals)}
	return s, nil
}

// ValidateClusteredISCSIAt is the no-mutation clustered-storage admission
// check for a specific Core state root. Callers that have an agent state
// directory must use it so health checks render the same fence secret paths
// as the active CIB.
func ValidateClusteredISCSIAt(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) (storageAccess, error) {
	if err := c.ValidFor(s); err != nil {
		return storageAccess{}, err
	}
	if err := validateClusteredRegistration(s, c); err != nil {
		return storageAccess{}, err
	}
	info, err := clusteredISCSIInfo(ctx, s)
	if err != nil {
		return storageAccess{}, err
	}
	lockTable, journals, err := gfs2Metadata(ctx, info.Path)
	if err != nil || lockTable != s.Source.ISCSI.LockTable || journals < len(s.Source.ISCSI.EligibleHostIDs) {
		return storageAccess{}, errors.New("GFS2 lock protocol, lock table or journals do not match registration")
	}
	if uint64(journals) > uint64(^uint32(0)) {
		return storageAccess{}, errors.New("GFS2 journal count overflows storage identity")
	}
	status, err := clusterHealthyAt(ctx, stateDir, s, c)
	if err != nil || !status.Healthy || !status.Quorate || !status.Mounted || !status.FencingVerified {
		return storageAccess{}, errors.New("clustered storage is not fenced, quorate and mounted by Pacemaker")
	}
	a, err := storagePathAccess(c.MountPath, "gfs2:"+s.Source.ISCSI.LockTable+":"+info.WWID, false)
	if err != nil {
		return storageAccess{}, err
	}
	a.Observed.ISCSI = &protocol.ISCSIStorageIdentity{TargetIQN: s.Source.ISCSI.TargetIQN, LUN: s.Source.ISCSI.LUN, WWID: info.WWID, FilesystemUUID: s.Source.ISCSI.FilesystemUUID, FilesystemType: "gfs2", GFS2LockTable: s.Source.ISCSI.LockTable, GFS2Journals: uint32(journals)}
	return a, qemuStorageAccess(ctx, c.MountPath)
}

func validateClusteredRegistration(s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
	if !protocol.IsClusteredISCSI(s) || s.Source.ISCSI == nil || s.Source.ISCSI.MountPath != c.MountPath || s.Path != c.MountPath {
		return errors.New("clustered iSCSI needs a resolved Core mount path")
	}
	if s.Identity.ISCSI != nil && (s.Identity.ISCSI.FilesystemType != "gfs2" || s.Identity.ISCSI.GFS2LockTable != s.Source.ISCSI.LockTable || s.Identity.ISCSI.GFS2Journals < uint32(len(s.Source.ISCSI.EligibleHostIDs))) {
		return errors.New("clustered iSCSI identity is not a matching GFS2 filesystem")
	}
	return nil
}

// loginClusteredISCSI issues at most one login. Some iscsid startup paths
// return from that command before the session publication is visible; after a
// failed login only, wait observationally for that exact session rather than
// issuing a second login or treating a delayed device as an identity match.
func loginClusteredISCSI(ctx context.Context, v *protocol.ISCSIStorageSource) error {
	_, loginErr := nativeRun(ctx, "iscsiadm", "-m", "node", "-T", v.TargetIQN, "-p", v.Portal, "--login")
	if err := ctx.Err(); err != nil {
		return err
	}
	if loginErr == nil {
		return nil
	}
	for attempt := 0; attempt < 12; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		active, err := iscsiSessionActive(ctx, v)
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}
		if err == nil && active {
			return nil
		}
		if attempt < 11 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(200 * time.Millisecond):
			}
		}
	}
	return loginErr
}

func waitClusteredISCSIInfo(ctx context.Context, s protocol.StorageRegistration) (iscsiFilesystemInfo, error) {
	info, err := waitISCSIFilesystemWith(ctx, s.Source.ISCSI, iscsiGFS2ForLUN)
	if err != nil {
		return iscsiFilesystemInfo{}, err
	}
	if canonicalWWID(info.WWID) != canonicalWWID(s.Source.ISCSI.WWID) || (s.Identity.ISCSI != nil && canonicalWWID(info.WWID) != canonicalWWID(s.Identity.ISCSI.WWID)) {
		return iscsiFilesystemInfo{}, errors.New("clustered iSCSI LUN WWID does not match registered identity")
	}
	return info, nil
}

func clusteredISCSIInfo(ctx context.Context, s protocol.StorageRegistration) (iscsiFilesystemInfo, error) {
	out, err := nativeRun(ctx, "lsblk", "--json", "--output", "PATH,TYPE,UUID,WWN,FSTYPE,HCTL")
	if err != nil {
		return iscsiFilesystemInfo{}, err
	}
	info, err := iscsiGFS2ForLUN(out, s.Source.ISCSI.FilesystemUUID, s.Source.ISCSI.LUN)
	if err != nil {
		return info, err
	}
	if canonicalWWID(info.WWID) != canonicalWWID(s.Source.ISCSI.WWID) || (s.Identity.ISCSI != nil && canonicalWWID(info.WWID) != canonicalWWID(s.Identity.ISCSI.WWID)) {
		return iscsiFilesystemInfo{}, errors.New("clustered iSCSI LUN WWID does not match registered identity")
	}
	return info, nil
}

func iscsiGFS2ForLUN(raw []byte, uuid string, lun uint64) (iscsiFilesystemInfo, error) {
	var list iscsiBlockList
	if json.Unmarshal(raw, &list) != nil {
		return iscsiFilesystemInfo{}, errors.New("invalid lsblk output")
	}
	var found []iscsiFilesystemInfo
	var walk func(iscsiBlock, string, string)
	walk = func(b iscsiBlock, parentWWN, parentHCTL string) {
		wwid, hctl := b.WWN, b.HCTL
		if wwid == "" {
			wwid = parentWWN
		}
		if hctl == "" {
			hctl = parentHCTL
		}
		if b.UUID == uuid && b.Path != "" && b.FSType == "gfs2" && iscsiWWID.MatchString(wwid) && iscsiHCTLMatches(hctl, lun) {
			found = append(found, iscsiFilesystemInfo{Path: b.Path, WWID: wwid, FSType: b.FSType})
		}
		for _, child := range b.Children {
			walk(child, wwid, hctl)
		}
	}
	for _, b := range list.BlockDevices {
		walk(b, "", "")
	}
	if len(found) != 1 {
		return iscsiFilesystemInfo{}, errors.New("clustered iSCSI UUID must identify exactly one GFS2 LUN")
	}
	return found[0], nil
}

// Debian gfs2-utils prints "Lock protocol" (not "Locking protocol") in
// tunegfs2 -l output. Keep the historical spelling accepted for compatible
// releases, while requiring the value itself to be exactly lock_dlm.
var gfs2ProtocolLine = regexp.MustCompile(`(?im)^\s*lock(?:ing)? protocol\s*:\s*lock_dlm\s*$`)
var gfs2LockLine = regexp.MustCompile(`(?im)^\s*lock table\s*:\s*([^\s]+)\s*$`)
var gfs2JournalLine = regexp.MustCompile(`(?im)^\s*journal[0-9]+:\s+`)

func verifyGFS2(ctx context.Context, device, lockTable string, members int) error {
	got, journals, err := gfs2Metadata(ctx, device)
	if err != nil || got != lockTable {
		return errors.New("GFS2 lock protocol, lock table or journals do not match registration")
	}
	if journals < members {
		return errors.New("GFS2 has fewer journals than eligible hosts")
	}
	return nil
}

type gfs2MetadataFlight struct {
	done     chan struct{}
	lock     string
	journals int
	err      error
}

var gfs2MetadataFlights = struct {
	sync.Mutex
	byDevice map[string]*gfs2MetadataFlight
}{byDevice: map[string]*gfs2MetadataFlight{}}

// gfs2Metadata coalesces only concurrent reads of the same exact device. It keeps no
// completed result: the leader removes its flight before followers receive its result.
func gfs2Metadata(ctx context.Context, device string) (string, int, error) {
	gfs2MetadataFlights.Lock()
	flight := gfs2MetadataFlights.byDevice[device]
	if flight == nil {
		flight = &gfs2MetadataFlight{done: make(chan struct{})}
		gfs2MetadataFlights.byDevice[device] = flight
		gfs2MetadataFlights.Unlock()

		flight.lock, flight.journals, flight.err = gfs2MetadataRead(ctx, device)

		gfs2MetadataFlights.Lock()
		delete(gfs2MetadataFlights.byDevice, device)
		close(flight.done)
		gfs2MetadataFlights.Unlock()
		return flight.lock, flight.journals, flight.err
	}
	gfs2MetadataFlights.Unlock()

	select {
	case <-ctx.Done():
		return "", 0, ctx.Err()
	case <-flight.done:
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if flight.err == nil {
		return flight.lock, flight.journals, nil
	}
	// A failed leader is never a shared result. A live follower performs its own
	// original read once, with its own existing context and no retry loop.
	return gfs2MetadataRead(ctx, device)
}

func gfs2MetadataRead(ctx context.Context, device string) (string, int, error) {
	out, err := clusterRun(ctx, "tunegfs2", "-l", device)
	if err != nil {
		return "", 0, fmt.Errorf("inspect existing GFS2 filesystem: %w", err)
	}
	lock := gfs2LockLine.FindStringSubmatch(string(out))
	if !gfs2ProtocolLine.Match(out) || len(lock) != 2 {
		return "", 0, errors.New("GFS2 lock protocol, lock table or journals are unavailable")
	}
	journalOutput, err := clusterRun(ctx, "gfs2_edit", "-p", "journals", device)
	if err != nil {
		return "", 0, fmt.Errorf("inspect GFS2 journals: %w", err)
	}
	n := len(gfs2JournalLine.FindAll(journalOutput, -1))
	if n < 1 {
		return "", 0, errors.New("GFS2 journal count is invalid")
	}
	return lock[1], n, nil
}

// RenderStorageCluster returns the Corosync file clustered storage asks for. A host has one
// corosync.conf and therefore one cluster, so this and the pool's own watchdog cluster
// (RenderPoolCluster) are two callers of one renderer and one writer, never two writers.
// It contains no secret: the auth key is separately generated/distributed by the Core peer
// layer.
func RenderStorageCluster(c protocol.StorageClusterConfig) (string, error) {
	if len(c.Nodes) < 3 || len(c.Nodes) > 5 {
		return "", errors.New("cluster needs three to five nodes")
	}
	nodes := make([]corosyncNode, 0, len(c.Nodes))
	for _, node := range c.SortedNodes() {
		nodes = append(nodes, corosyncNode{address: node.CorosyncAddress, name: node.HostID, id: node.CorosyncNodeID})
	}
	return renderCorosyncConfig("", c.ClusterID, nodes, false), nil
}

// corosyncNode is one nodelist entry. The name is deliberately the durable Nex Core host ID:
// Pacemaker addresses STONITH requests by the Corosync node name and pcmk_host_* already
// matches it, and a watchdog proof reads the same names back out of corosync-quorumtool.
type corosyncNode struct {
	address string
	name    string
	id      uint32
}

// renderCorosyncConfig is the only rendering of /etc/corosync/corosync.conf in Nex Core.
// marker is the owning consumer's extra header line, so a host can tell which of the two
// wrote the file it already has and refuse to take it over.
func renderCorosyncConfig(marker, clusterName string, nodes []corosyncNode, waitForAll bool, witnessAddress ...string) string {
	var b strings.Builder
	b.WriteString("# Managed by Nex Core; do not edit.\n" + marker)
	b.WriteString("totem {\n  version: 2\n  cluster_name: " + clusterName + "\n  transport: knet\n  secauth: on\n  crypto_cipher: aes256\n  crypto_hash: sha256\n}\n")
	b.WriteString("nodelist {\n")
	for _, node := range nodes {
		fmt.Fprintf(&b, "  node {\n    ring0_addr: %s\n    name: %s\n    nodeid: %d\n  }\n", node.address, node.name, node.id)
	}
	// Corosync's brace syntax requires stanza entries on a subsequent line.
	// Keeping quorum as an inline stanza makes the daemon reject the whole
	// configuration before it can form a safe quorum.
	b.WriteString("}\nquorum {\n  provider: corosync_votequorum\n")
	if waitForAll {
		// Two properties, and the second one is why this must not be removed.
		//
		// A partition that has never seen the whole pool must not decide it is the
		// majority: without this, a pool coming back one host at a time can form a
		// quorum of survivors while the rest of it is still booting.
		//
		// And it is what holds no-quorum-policy=suicide back on a cold start. Measured:
		// a host that starts this cluster alone, inquorate, with suicide already in its
		// persisted CIB, does NOT panic, because Pacemaker refuses to apply the policy to
		// a cluster that has never had quorum and says so — "Resetting no-quorum-policy
		// to 'stop': cluster has never had quorum". wait_for_all is what makes a lone
		// host's membership never-quorate, so it is load-bearing for the guarantee that
		// nothing resets a host that boots with its peers still down.
		b.WriteString("  wait_for_all: 1\n")
	}
	if len(witnessAddress) > 0 && witnessAddress[0] != "" {
		host, port, _ := net.SplitHostPort(witnessAddress[0])
		b.WriteString("  device {\n    model: net\n    votes: 1\n    net {\n")
		fmt.Fprintf(&b, "      host: %s\n      port: %s\n", host, port)
		b.WriteString("      algorithm: ffsplit\n      tie_breaker: lowest\n      tls: required\n    }\n  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

// writeCorosyncConfig is the one place Nex Core writes /etc/corosync/corosync.conf. Both
// consumers go through it, so a host can never end up with two writers of one cluster: a file
// this Core wrote for the other consumer is refused rather than replaced, and an
// administrator's own file is refused unless packaging's marker still binds it to an untouched
// vendor file. It reports whether the bytes on disk changed.
func writeCorosyncConfig(stateDir, rendered, marker string) (bool, error) {
	old, readErr := os.ReadFile(corosyncConfigPath)
	switch {
	case readErr != nil && !errors.Is(readErr, os.ErrNotExist):
		return false, readErr
	case readErr == nil && strings.HasPrefix(string(old), "# Managed by Nex Core;"):
		if corosyncConfigMarker(old) != marker {
			return false, errors.New("this host's Corosync configuration belongs to another Nex Core cluster on this pool; one host has one cluster")
		}
	case readErr == nil:
		if !corosyncUnconfiguredMarker(stateDir, old) {
			return false, errors.New("refusing to replace administrator Corosync configuration")
		}
		// Packaging creates the marker only for a recognized untouched vendor
		// file. Preserve that file before its one permitted replacement.
		if err := os.MkdirAll(filepath.Join(stateDir, "storage-cluster"), 0700); err != nil {
			return false, err
		}
		if err := atomicWrite(filepath.Join(stateDir, "storage-cluster", "corosync-precluster.conf"), old); err != nil {
			return false, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(corosyncConfigPath), 0700); err != nil {
		return false, err
	}
	changed := old == nil || !bytes.Equal(old, []byte(rendered))
	if changed {
		if err := atomicWrite(corosyncConfigPath, []byte(rendered)); err != nil {
			return false, err
		}
	}
	if err := os.Remove(corosyncUnconfiguredMarkerPath(stateDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return changed, nil
}

// corosyncConfigMarker is the owning consumer's header line, empty for clustered storage,
// which wrote this file before the pool's own cluster existed.
func corosyncConfigMarker(config []byte) string {
	for _, line := range strings.SplitN(string(config), "\n", 3) {
		if strings.HasPrefix(line, "# Nex ") {
			return line + "\n"
		}
	}
	return ""
}

// StartStorageClusterAt applies only the local Corosync projection and starts
// its local services. Every admitted member must complete this before the
// pool master writes shared CIB state. It deliberately performs no pcs or
// cibadmin operation: concurrent CIB writers can create invalid conflicting
// updates even when their desired configuration is identical.
func StartStorageClusterAt(ctx context.Context, stateDir string, c protocol.StorageClusterConfig) error {
	if !c.FencingVerified() {
		return errors.New("every cluster node needs recorded successful fencing and exclusion evidence")
	}
	if err := validateStorageClusterDLMFenceHelper(ctx); err != nil {
		return fmt.Errorf("validate DLM fencing helper: %w", err)
	}
	rendered, err := RenderStorageCluster(c)
	if err != nil {
		return err
	}
	changed, err := writeCorosyncConfig(stateDir, rendered, "")
	if err != nil {
		return err
	}
	if !changed {
		ready, err := storageClusterServicesReady(ctx)
		if err != nil {
			return err
		}
		if ready {
			return nil
		}
	}
	_, err = clusterRun(ctx, "systemctl", "enable", "--now", "corosync.service", "pacemaker.service")
	return err
}

// storageClusterServicesReady checks each service independently. systemctl's
// multi-unit is-active/is-enabled forms succeed when any unit matches, which
// would let a stopped or non-persistent cluster service evade reconciliation.
func storageClusterServicesReady(ctx context.Context) (bool, error) {
	for _, unit := range []string{"corosync.service", "pacemaker.service"} {
		enabled, err := clusterRun(ctx, "systemctl", "show", "--property=UnitFileState", "--value", unit)
		if err != nil {
			return false, err
		}
		active, err := clusterRun(ctx, "systemctl", "show", "--property=ActiveState", "--value", unit)
		if err != nil {
			return false, err
		}
		if strings.TrimSpace(string(enabled)) != "enabled" || strings.TrimSpace(string(active)) != "active" {
			return false, nil
		}
	}
	return true, nil
}

// ApplyStorageClusterWithRegistrationAt performs the master-only CIB
// reconciliation once every member has independently established the exact
// Core-owned iSCSI transport. The registration is required to render the
// transport monitor; it is never supplied by a Controller or CIB parameter.
func ApplyStorageClusterWithRegistrationAt(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
	if err := c.ValidFor(s); err != nil {
		return err
	}
	transport, err := storageClusterTransportFor(s, c)
	if err != nil {
		return err
	}
	if err := validateStorageClusterSessionMonitor(ctx, transport.portal, transport.target, transport.resource); err != nil {
		return errors.New("cluster iSCSI session monitor is unavailable")
	}
	if err := StartStorageClusterAt(ctx, stateDir, c); err != nil {
		return err
	}
	if err := applyStorageClusterFencingProperties(ctx); err != nil {
		return err
	}
	for _, node := range c.SortedNodes() {
		if err := applyFenceResourceAt(ctx, stateDir, node); err != nil {
			return err
		}
	}
	if err := applyGFS2ClusterResources(ctx, s, c); err != nil {
		return err
	}
	return nil
}

// ReconcileStorageClusterFencing repairs the fixed Core-owned cluster-wide
// fencing policy only when the queried CIB exactly matches the current Nex
// cluster configuration. Pacemaker's default fencing action is reboot,
// whereas a Nex proof deliberately powers a host off and requires it to remain
// excluded. Any other CIB is refused without mutation, and no GFS2 resource or
// mount is touched merely to repair this safety property.
func ReconcileStorageClusterFencing(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
	if err := c.ValidFor(s); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !storageClusterCIBMatchesConfig(current, stateDir, s, c) {
		return errors.New("owned storage cluster CIB configuration is unavailable")
	}
	if storageClusterFencingPropertiesMatch(current) {
		return nil
	}
	return applyStorageClusterFencingProperties(ctx)
}

// ReconcileStorageClusterTransport verifies, without mutation, that an
// already applied GFS2 configuration carries the exact Core-owned iSCSI
// transport clone and its mandatory order. Initial activation creates them;
// any other CIB is refused.
func ReconcileStorageClusterTransport(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
	if err := c.ValidFor(s); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	t, err := storageClusterTransportFor(s, c)
	if err != nil {
		return err
	}
	if err := validateStorageClusterSessionMonitor(ctx, t.portal, t.target, t.resource); err != nil {
		return errors.New("cluster iSCSI session monitor is unavailable")
	}
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	if !storageClusterCIBMatchesConfig(current, stateDir, s, c) {
		return errors.New("iSCSI transport monitor CIB configuration is unavailable")
	}
	return nil
}

func gfs2MonitorID(c protocol.StorageClusterConfig) string {
	return "nex-gfs2-" + c.RepositoryID + "-monitor-interval-20s"
}

// gfs2MonitorCurrent accepts only the exact Nex 40s monitor. Any extra
// monitor, changed failure action or timeout, depth, or arbitrary operation is
// foreign configuration.
func gfs2MonitorCurrent(raw []byte, c protocol.StorageClusterConfig) bool {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	resource := "nex-gfs2-" + c.RepositoryID
	var primitive cibNode
	primitiveCount := 0
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local == "primitive" && n.attr("id") == resource {
			primitive = n
			primitiveCount++
		}
		return false
	})
	if primitiveCount != 1 {
		return false
	}
	var monitor cibNode
	monitorCount, startCount, stopCount := 0, 0, 0
	primitive.walk(func(n cibNode) bool {
		if n.XMLName.Local != "op" {
			return false
		}
		switch n.attr("name") {
		case "monitor":
			monitor, monitorCount = n, monitorCount+1
		case "start":
			if !gfs2LifecycleOperation(n, resource, "start") {
				startCount = 2
			} else {
				startCount++
			}
		case "stop":
			if !gfs2LifecycleOperation(n, resource, "stop") {
				stopCount = 2
			} else {
				stopCount++
			}
		default:
			monitorCount = 2
		}
		return false
	})
	// pcs renders the default start/stop pair on some supported releases. It
	// is accepted only as that exact pair.
	if monitorCount != 1 || startCount > 1 || stopCount > 1 || startCount != stopCount {
		return false
	}
	op := monitor
	if op.attr("id") != gfs2MonitorID(c) || op.attr("interval") != "20s" || op.attr("timeout") != "40s" || op.attr("on-fail") != "fence" {
		return false
	}
	for _, attr := range op.Attr {
		if attr.Name.Local != "id" && attr.Name.Local != "name" && attr.Name.Local != "interval" && attr.Name.Local != "on-fail" && attr.Name.Local != "timeout" {
			return false
		}
	}
	return true
}

func gfs2LifecycleOperation(op cibNode, resource, name string) bool {
	if op.attr("id") != resource+"-"+name+"-interval-0s" || op.attr("interval") != "0s" || op.attr("timeout") != "60s" || op.attr("on-fail") != "" || op.attr("depth") != "" {
		return false
	}
	for _, attr := range op.Attr {
		if attr.Name.Local != "id" && attr.Name.Local != "name" && attr.Name.Local != "interval" && attr.Name.Local != "timeout" {
			return false
		}
	}
	return true
}

func runClusterDiff(ctx context.Context, args ...string) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "crm_diff", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var out, stderr cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return out.Bytes(), exitErr.ExitCode(), nil
	}
	return nil, 0, fmt.Errorf("crm_diff failed: %w", err)
}

// withCIBNextUpdate requires the full versioned CIB returned by cibadmin and
// advances only num_updates. crm_diff --cib carries these versions into its
// patch, making a concurrent CIB change a rejected patch rather than an
// overwrite.
func withCIBNextUpdate(raw []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	for {
		token, err := decoder.Token()
		if err != nil {
			return nil, err
		}
		start, ok := token.(xml.StartElement)
		if !ok || start.Name.Local != "cib" {
			continue
		}
		if xmlAttr(start.Attr, "admin_epoch") == "" || xmlAttr(start.Attr, "epoch") == "" || xmlAttr(start.Attr, "num_updates") == "" {
			return nil, errors.New("CIB version attributes are unavailable")
		}
		n, err := strconv.ParseUint(xmlAttr(start.Attr, "num_updates"), 10, 64)
		if err != nil || n == ^uint64(0) {
			return nil, errors.New("CIB update version is invalid")
		}
		end := int(decoder.InputOffset())
		begin := bytes.LastIndex(raw[:end], []byte("<cib"))
		if begin < 0 || end-begin < 5 || !bytes.HasSuffix(raw[begin:end], []byte(">")) {
			return nil, errors.New("CIB root element is malformed")
		}
		tag := append([]byte(nil), raw[begin:end]...)
		old := []byte(`num_updates="` + xmlAttr(start.Attr, "num_updates") + `"`)
		if bytes.Count(tag, old) != 1 {
			return nil, errors.New("CIB update version is ambiguous")
		}
		tag = bytes.Replace(tag, old, []byte(`num_updates="`+strconv.FormatUint(n+1, 10)+`"`), 1)
		updated := append([]byte(nil), raw[:begin]...)
		updated = append(updated, tag...)
		updated = append(updated, raw[end:]...)
		return updated, nil
	}
}

func xmlAttr(attrs []xml.Attr, name string) string {
	for _, attr := range attrs {
		if attr.Name.Local == name {
			return attr.Value
		}
	}
	return ""
}

func applyStorageClusterFencingProperties(ctx context.Context) error {
	for _, args := range storageClusterFencingProperties() {
		if _, err := clusterRun(ctx, "pcs", args...); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// fenceCIBRebootActionCurrent requires exactly one direct, unconditional
// pcmk_reboot_action=off mapping. A missing, conflicting or duplicated mapping
// is never accepted.
func fenceCIBRebootActionCurrent(raw []byte, hostID string) bool {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	resource, resourceCount := "nex-fence-"+hostID, 0
	var primitive cibNode
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local != "primitive" || n.attr("id") != resource {
			return false
		}
		resourceCount++
		primitive = n
		return false
	})
	if resourceCount != 1 {
		return false
	}
	all, direct := 0, []cibNode{}
	attributes := 0
	primitive.walk(func(n cibNode) bool {
		if n.XMLName.Local == "nvpair" && n.attr("name") == "pcmk_reboot_action" {
			all++
		}
		return false
	})
	for _, child := range primitive.Child {
		if child.XMLName.Local != "instance_attributes" {
			continue
		}
		attributes++
		if child.attr("id-ref") != "" {
			return false
		}
		for _, pair := range child.Child {
			if pair.XMLName.Local != "nvpair" {
				return false
			}
			if pair.attr("name") == "pcmk_reboot_action" {
				direct = append(direct, pair)
			}
		}
	}
	return attributes == 1 && all == 1 && len(direct) == 1 && direct[0].attr("value") == "off" && direct[0].attr("id") == fenceCIBRebootActionID(hostID)
}

func fenceCIBRebootActionID(hostID string) string {
	return "nex-fence-" + hostID + "-instance_attributes-pcmk_reboot_action"
}

// fenceCIBInstanceParametersMatch is stricter than cibHasResource: it requires
// precisely the Core-rendered, unconditional device parameters, not merely
// their presence alongside foreign fence behavior.
func fenceCIBInstanceParametersMatch(raw []byte, hostID string, create []string) bool {
	if len(create) < 4 {
		return false
	}
	want := make(map[string]string)
	for _, arg := range create[4:] {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" || value == "" || want[key] != "" {
			return false
		}
		want[key] = value
	}
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	resource, found := "nex-fence-"+hostID, []cibNode{}
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local == "primitive" && n.attr("id") == resource {
			found = append(found, n)
		}
		return false
	})
	if len(found) != 1 {
		return false
	}
	attributes := []cibNode{}
	for _, child := range found[0].Child {
		if child.XMLName.Local == "instance_attributes" {
			attributes = append(attributes, child)
		}
	}
	if len(attributes) != 1 || attributes[0].attr("id-ref") != "" {
		return false
	}
	got := make(map[string]string)
	for _, pair := range attributes[0].Child {
		if pair.XMLName.Local != "nvpair" {
			return false
		}
		name, value := pair.attr("name"), pair.attr("value")
		if name == "" || value == "" || got[name] != "" {
			return false
		}
		got[name] = value
	}
	if len(got) != len(want) {
		return false
	}
	for name, value := range want {
		if got[name] != value {
			return false
		}
	}
	return true
}

func storageClusterCIBMatchesConfig(raw []byte, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) bool {
	transport, err := storageClusterTransportFor(s, c)
	if err != nil || c.ClusterID == "" || c.RepositoryID == "" || len(c.Nodes) < 3 || len(c.Nodes) > 5 || !gfs2MonitorCurrent(raw, c) || !cibHasResource(raw, "nex-dlm", dlmResourceSpec()) || !cibHasResource(raw, "nex-gfs2-"+c.RepositoryID, gfs2ResourceBaseSpec(c)) || !cibHasOrder(raw, "nex-dlm-before-"+c.RepositoryID, "nex-dlm-clone", "nex-gfs2-"+c.RepositoryID+"-clone") || !transportCIBResourceMatches(raw, transport) || storageTransportOrderState(raw, transport, c) != transportOrderCurrent {
		return false
	}
	for _, node := range c.Nodes {
		fence, err := fenceCIBCreateArgs(stateDir, node)
		if err != nil || !cibHasResource(raw, "nex-fence-"+node.HostID, fence) || !fenceCIBInstanceParametersMatch(raw, node.HostID, fence) || !fenceCIBRebootActionCurrent(raw, node.HostID) {
			return false
		}
	}
	return true
}

func storageClusterFencingProperties() [][]string {
	return [][]string{
		{"property", "set", "stonith-enabled=true"},
		{"property", "set", "stonith-action=off"},
		{"property", "set", "startup-fencing=true"},
		{"property", "set", "no-quorum-policy=freeze"},
	}
}

func storageClusterFencingPropertiesMatch(pcs []byte) bool {
	for _, args := range storageClusterFencingProperties() {
		name, value, ok := strings.Cut(args[2], "=")
		if !ok || !cibHasProperty(pcs, name, value) {
			return false
		}
	}
	return true
}

func dlmResourceSpec() []string {
	return []string{"resource", "create", "nex-dlm", "ocf:pacemaker:controld", "op", "monitor", "interval=30s", "on-fail=fence", "clone", "interleave=true", "ordered=true"}
}

func gfs2ResourceSpec(c protocol.StorageClusterConfig) []string {
	resource := "nex-gfs2-" + c.RepositoryID
	return []string{"resource", "create", resource, "ocf:heartbeat:Filesystem", "device=" + c.DevicePath, "directory=" + c.MountPath, "fstype=gfs2", "op", "monitor", "interval=20s", "timeout=40s", "on-fail=fence", "clone", "interleave=true", "ordered=true"}
}

func gfs2ResourceBaseSpec(c protocol.StorageClusterConfig) []string {
	resource := "nex-gfs2-" + c.RepositoryID
	return []string{"resource", "create", resource, "ocf:heartbeat:Filesystem", "device=" + c.DevicePath, "directory=" + c.MountPath, "fstype=gfs2", "clone", "interleave=true", "ordered=true"}
}

func gfs2DisabledResourceSpec(c protocol.StorageClusterConfig) []string {
	return append(gfs2ResourceSpec(c), "--disabled")
}

// storageClusterTransport describes one iSCSI login session.  Open-iSCSI
// exposes all LUNs from a target/portal through that one session, so its CIB
// identity must deliberately not include a LUN, WWID, or repository ID.
// The resource agent receives only these non-secret, validated endpoint
// values; it performs a read-only session-state check and never logs out.
type storageClusterTransport struct {
	resource string
	portal   string
	target   string
}

func storageClusterTransportFor(s protocol.StorageRegistration, c protocol.StorageClusterConfig) (storageClusterTransport, error) {
	if err := c.ValidFor(s); err != nil || s.Source.ISCSI == nil {
		return storageClusterTransport{}, errors.New("cluster transport registration is invalid")
	}
	portal, resource, err := StorageISCSISessionParameters(s.Source.ISCSI.Portal, s.Source.ISCSI.TargetIQN)
	if err != nil {
		return storageClusterTransport{}, err
	}
	return storageClusterTransport{resource: resource, portal: portal, target: s.Source.ISCSI.TargetIQN}, nil
}

func storageTransportResourceSpec(t storageClusterTransport) []string {
	return []string{"resource", "create", t.resource, "ocf:nex:cluster-iscsi-session", "portal=" + t.portal, "target=" + t.target, "session_id=" + t.resource,
		"op", "start", "id=" + t.resource + "-start-interval-0s", "interval=0s", "timeout=10s", "on-fail=fence",
		"op", "stop", "id=" + t.resource + "-stop-interval-0s", "interval=0s", "timeout=10s",
		"op", "monitor", "id=" + t.resource + "-monitor-interval-5s", "interval=5s", "timeout=10s", "on-fail=fence",
		"clone", "interleave=true", "ordered=true"}
}

func storageTransportOrderID(t storageClusterTransport, c protocol.StorageClusterConfig) string {
	return t.resource + "-before-" + c.RepositoryID
}

func transportCIBResourceMatches(raw []byte, t storageClusterTransport) bool {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	var primitive *cibNode
	primitives := 0
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local == "primitive" && n.attr("id") == t.resource {
			copy := n
			primitive, primitives = &copy, primitives+1
		}
		return false
	})
	if primitives != 1 || primitive == nil || primitive.attr("class") != "ocf" || primitive.attr("provider") != "nex" || primitive.attr("type") != "cluster-iscsi-session" {
		return false
	}
	params := map[string]string{}
	containers := 0
	for _, child := range primitive.Child {
		if child.XMLName.Local != "instance_attributes" {
			continue
		}
		containers++
		for _, nv := range child.Child {
			if nv.XMLName.Local != "nvpair" || nv.attr("name") == "" || nv.attr("value") == "" {
				return false
			}
			if _, duplicate := params[nv.attr("name")]; duplicate {
				return false
			}
			params[nv.attr("name")] = nv.attr("value")
		}
	}
	if containers != 1 || len(params) != 3 || params["portal"] != t.portal || params["target"] != t.target || params["session_id"] != t.resource {
		return false
	}
	wantOps := map[string]map[string]string{
		"start":   {"id": t.resource + "-start-interval-0s", "name": "start", "interval": "0s", "timeout": "10s", "on-fail": "fence"},
		"stop":    {"id": t.resource + "-stop-interval-0s", "name": "stop", "interval": "0s", "timeout": "10s"},
		"monitor": {"id": t.resource + "-monitor-interval-5s", "name": "monitor", "interval": "5s", "timeout": "10s", "on-fail": "fence"},
	}
	opsContainers, ops := 0, map[string]cibNode{}
	for _, child := range primitive.Child {
		if child.XMLName.Local != "operations" {
			continue
		}
		opsContainers++
		for _, op := range child.Child {
			name := op.attr("name")
			if op.XMLName.Local != "op" || name == "" {
				return false
			}
			if _, duplicate := ops[name]; duplicate {
				return false
			}
			ops[name] = op
		}
	}
	if opsContainers != 1 || len(ops) != len(wantOps) {
		return false
	}
	for name, attrs := range wantOps {
		op, ok := ops[name]
		if !ok || len(op.Attr) != len(attrs) {
			return false
		}
		for key, value := range attrs {
			if op.attr(key) != value {
				return false
			}
		}
	}
	clones := 0
	var clone *cibNode
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local == "clone" && n.attr("id") == t.resource+"-clone" {
			copy := n
			clone, clones = &copy, clones+1
		}
		return false
	})
	if clones != 1 || clone == nil || !cloneContainsDirectPrimitive(*clone, t.resource) || !cloneHasExactMeta(*clone, map[string]string{"interleave": "true", "ordered": "true"}) {
		return false
	}
	stopped, unique := cibResourceDisabled(root, t.resource)
	return unique && !stopped
}

func cloneContainsDirectPrimitive(clone cibNode, id string) bool {
	count := 0
	for _, child := range clone.Child {
		if child.XMLName.Local == "primitive" && child.attr("id") == id {
			count++
		}
	}
	return count == 1
}

func cloneHasExactMeta(clone cibNode, want map[string]string) bool {
	meta := map[string]string{}
	containers := 0
	for _, child := range clone.Child {
		if child.XMLName.Local != "meta_attributes" {
			continue
		}
		containers++
		for _, nv := range child.Child {
			if nv.XMLName.Local != "nvpair" || nv.attr("name") == "" || nv.attr("value") == "" {
				return false
			}
			if _, duplicate := meta[nv.attr("name")]; duplicate {
				return false
			}
			meta[nv.attr("name")] = nv.attr("value")
		}
	}
	return containers == 1 && maps.Equal(meta, want)
}

func waitStorageTransportClone(ctx context.Context, t storageClusterTransport, c protocol.StorageClusterConfig) error {
	for attempt := 0; attempt < 20; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		runtime, err := clusterRun(ctx, "pcs", "status", "xml")
		if err == nil {
			members, active := runtimeCloneActiveMembers(runtime, t.resource, c, len(c.Nodes))
			if active && len(members) == len(c.Nodes) {
				return nil
			}
		}
		if attempt == 19 {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return errors.New("iSCSI transport monitor is not active on every configured Core")
}

// stageStorageTransportClone adds only the Nex transport clone through a
// version-guarded CIB patch. It never modifies a pre-existing primitive: a
// concurrent or administrator-owned resource with the same identity is an
// unsafe conflict. The clone is enabled immediately because its start action
// is a read-only session check; callers wait for every configured member
// before adding an order that could affect an already-running GFS clone.
func stageStorageTransportClone(ctx context.Context, t storageClusterTransport) error {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	if transportCIBResourceMatches(current, t) {
		return nil
	}
	if cibHasNamedPrimitive(current, t.resource) {
		return errors.New("iSCSI transport monitor CIB resource is foreign or conflicting")
	}
	updated, err := withStorageTransportClone(current, t)
	if err != nil {
		return err
	}
	if err := applyStorageClusterCIBPatch(ctx, current, updated); err != nil {
		return err
	}
	current, err = clusterRun(ctx, "cibadmin", "--query")
	if err != nil || !transportCIBResourceMatches(current, t) {
		return errors.New("iSCSI transport monitor CIB changed during creation")
	}
	return nil
}

func stageStorageTransportOrder(ctx context.Context, t storageClusterTransport, c protocol.StorageClusterConfig) error {
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	if storageTransportOrderState(current, t, c) == transportOrderCurrent {
		return nil
	}
	if storageTransportOrderState(current, t, c) != transportOrderAbsent || !transportCIBResourceMatches(current, t) {
		return errors.New("iSCSI transport monitor order is foreign or conflicting")
	}
	updated, err := withStorageTransportOrder(current, t, c)
	if err != nil {
		return err
	}
	if err := applyStorageClusterCIBPatch(ctx, current, updated); err != nil {
		return err
	}
	current, err = clusterRun(ctx, "cibadmin", "--query")
	if err != nil || storageTransportOrderState(current, t, c) != transportOrderCurrent {
		return errors.New("iSCSI transport monitor order changed during creation")
	}
	return nil
}

const (
	transportOrderInvalid = iota
	transportOrderAbsent
	transportOrderCurrent
)

func storageTransportOrderState(raw []byte, t storageClusterTransport, c protocol.StorageClusterConfig) int {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return transportOrderInvalid
	}
	count := 0
	valid := false
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local != "rsc_order" || n.attr("id") != storageTransportOrderID(t, c) {
			return false
		}
		count++
		// pcs 0.12/Pacemaker serializes the start actions explicitly. Requiring
		// them avoids treating a similarly named but differently triggered order
		// as Core-owned after the CIB normalizes our versioned patch.
		valid = n.attr("first") == t.resource+"-clone" && n.attr("then") == "nex-gfs2-"+c.RepositoryID+"-clone" && n.attr("kind") == "Mandatory" && n.attr("first-action") == "start" && n.attr("then-action") == "start" && len(n.Attr) == 6
		return false
	})
	if count == 0 {
		return transportOrderAbsent
	}
	if count == 1 && valid {
		return transportOrderCurrent
	}
	return transportOrderInvalid
}

func applyStorageClusterCIBPatch(ctx context.Context, current, updated []byte) error {
	updated, err := withCIBNextUpdate(updated)
	if err != nil {
		return err
	}
	patch, exitCode, err := clusterDiff(ctx, "--cib", "--original-string", string(current), "--new-string", string(updated))
	if err != nil || exitCode != 1 {
		if err != nil {
			return err
		}
		return fmt.Errorf("crm_diff returned unexpected exit status %d", exitCode)
	}
	if len(bytes.TrimSpace(patch)) == 0 {
		return errors.New("storage transport CIB patch is empty")
	}
	_, err = clusterRun(ctx, "cibadmin", "--patch", "--sync-call", "--xml-text", string(patch))
	return err
}

func withStorageTransportClone(raw []byte, t storageClusterTransport) ([]byte, error) {
	if cibHasNamedPrimitive(raw, t.resource) {
		return nil, errors.New("iSCSI transport monitor CIB resource already exists")
	}
	fragment := `<clone id="` + xmlEscaped(t.resource) + `-clone"><meta_attributes id="` + xmlEscaped(t.resource) + `-clone-meta"><nvpair id="` + xmlEscaped(t.resource) + `-clone-interleave" name="interleave" value="true"/><nvpair id="` + xmlEscaped(t.resource) + `-clone-ordered" name="ordered" value="true"/></meta_attributes><primitive id="` + xmlEscaped(t.resource) + `" class="ocf" provider="nex" type="cluster-iscsi-session"><instance_attributes id="` + xmlEscaped(t.resource) + `-params"><nvpair id="` + xmlEscaped(t.resource) + `-portal" name="portal" value="` + xmlEscaped(t.portal) + `"/><nvpair id="` + xmlEscaped(t.resource) + `-target" name="target" value="` + xmlEscaped(t.target) + `"/><nvpair id="` + xmlEscaped(t.resource) + `-session-id" name="session_id" value="` + xmlEscaped(t.resource) + `"/></instance_attributes><operations><op id="` + xmlEscaped(t.resource) + `-start-interval-0s" name="start" interval="0s" timeout="10s" on-fail="fence"/><op id="` + xmlEscaped(t.resource) + `-stop-interval-0s" name="stop" interval="0s" timeout="10s"/><op id="` + xmlEscaped(t.resource) + `-monitor-interval-5s" name="monitor" interval="5s" timeout="10s" on-fail="fence"/></operations></primitive></clone>`
	return insertCIBConfigurationChild(raw, "resources", []byte(fragment))
}

func withStorageTransportOrder(raw []byte, t storageClusterTransport, c protocol.StorageClusterConfig) ([]byte, error) {
	if storageTransportOrderState(raw, t, c) != transportOrderAbsent {
		return nil, errors.New("iSCSI transport monitor order already exists")
	}
	fragment := `<rsc_order id="` + xmlEscaped(storageTransportOrderID(t, c)) + `" first="` + xmlEscaped(t.resource) + `-clone" then="nex-gfs2-` + xmlEscaped(c.RepositoryID) + `-clone" kind="Mandatory" first-action="start" then-action="start"/>`
	return insertCIBConfigurationChild(raw, "constraints", []byte(fragment))
}

func xmlEscaped(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

// insertCIBConfigurationChild locates one direct configuration child using
// the XML token stream, then appends a fully rendered fragment before its end
// tag. Callers have already rejected an ambiguous resource/order; this helper
// additionally refuses duplicate or nested containers rather than applying a
// broad string replacement.
func insertCIBConfigurationChild(raw []byte, name string, fragment []byte) ([]byte, error) {
	decoder := xml.NewDecoder(bytes.NewReader(raw))
	depth, configurationDepth, matches, offset := 0, -1, 0, -1
	configurationSeen := false
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch token := token.(type) {
		case xml.StartElement:
			depth++
			if token.Name.Local == "configuration" {
				if configurationSeen {
					return nil, errors.New("CIB configuration is ambiguous")
				}
				configurationSeen = true
				configurationDepth = depth
			}
		case xml.EndElement:
			if token.Name.Local == name && configurationDepth >= 0 && depth == configurationDepth+1 {
				matches++
				offset = int(decoder.InputOffset()) - len("</"+name+">")
			}
			if token.Name.Local == "configuration" && depth == configurationDepth {
				configurationDepth = -2
			}
			depth--
		}
	}
	if configurationDepth != -2 || !configurationSeen || matches != 1 || offset < 0 || offset > len(raw) {
		return nil, errors.New("CIB configuration container is unavailable")
	}
	updated := make([]byte, 0, len(raw)+len(fragment))
	updated = append(updated, raw[:offset]...)
	updated = append(updated, fragment...)
	updated = append(updated, raw[offset:]...)
	return updated, nil
}

// applyGFS2ClusterResources never permits the Filesystem clone to start until
// the exact DLM clone and mandatory order constraint have both reached CIB.
// A peer racing the same desired generation is reconciled only by an exact CIB
// reread; an already enabled but unordered resource is left untouched.
func applyGFS2ClusterResources(ctx context.Context, s protocol.StorageRegistration, c protocol.StorageClusterConfig) error {
	transport, err := storageClusterTransportFor(s, c)
	if err != nil {
		return err
	}
	if err := applyClusterResource(ctx, "nex-dlm", dlmResourceSpec()); err != nil {
		return err
	}
	// Stage the transport independently before creating the GFS order. A
	// monitor resource is check-only, but an unavailable session must still
	// stop activation rather than letting Filesystem touch a failing GFS mount.
	if err := stageStorageTransportClone(ctx, transport); err != nil {
		return err
	}
	if err := waitStorageTransportClone(ctx, transport, c); err != nil {
		return err
	}
	resource, orderID := "nex-gfs2-"+c.RepositoryID, "nex-dlm-before-"+c.RepositoryID
	active, disabled := gfs2ResourceSpec(c), gfs2DisabledResourceSpec(c)
	current, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		return err
	}
	if cibHasResource(current, resource, active) {
		if !cibHasOrder(current, orderID, "nex-dlm-clone", resource+"-clone") || !transportCIBResourceMatches(current, transport) || storageTransportOrderState(current, transport, c) != transportOrderCurrent {
			return errors.New("refusing enabled GFS2 resource without exact DLM and transport ordering")
		}
		return nil
	}
	if !cibHasResource(current, resource, disabled) {
		if err := applyClusterResource(ctx, resource, disabled); err != nil {
			return err
		}
	}
	if err := applyClusterOrder(ctx, orderID, "nex-dlm-clone", resource+"-clone"); err != nil {
		return err
	}
	if err := stageStorageTransportOrder(ctx, transport, c); err != nil {
		return err
	}
	current, err = clusterRun(ctx, "cibadmin", "--query")
	if err != nil || !cibHasResource(current, "nex-dlm", dlmResourceSpec()) || !transportCIBResourceMatches(current, transport) || !cibHasResource(current, resource, disabled) || !cibHasOrder(current, orderID, "nex-dlm-clone", resource+"-clone") || storageTransportOrderState(current, transport, c) != transportOrderCurrent {
		return errors.New("DLM, disabled GFS2 resource, or mandatory ordering is not exactly configured")
	}
	_, err = clusterRun(ctx, "pcs", "resource", "enable", resource)
	return err
}

func cibHasNamedPrimitive(raw []byte, id string) bool {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	found := false
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local == "primitive" && n.attr("id") == id {
			found = true
		}
		return false
	})
	return found
}

func corosyncUnconfiguredMarkerPath(stateDir string) string {
	return filepath.Join(stateDir, "storage-cluster", "corosync-unconfigured")
}

// corosyncUnconfiguredMarker authorizes one vendor-file replacement only when
// packaging's root-owned marker exactly binds to the bytes currently on disk.
// An administrator edit after package installation therefore fails closed.
func corosyncUnconfiguredMarker(stateDir string, config []byte) bool {
	info, err := os.Lstat(corosyncUnconfiguredMarkerPath(stateDir))
	if err != nil || !info.Mode().IsRegular() || info.Size() != sha256.Size*2+1 {
		return false
	}
	marker, err := os.ReadFile(corosyncUnconfiguredMarkerPath(stateDir))
	if err != nil {
		return false
	}
	digest := sha256.Sum256(config)
	return string(marker) == fmt.Sprintf("%x\n", digest)
}

func applyClusterOrder(ctx context.Context, id, first, then string) error {
	if out, err := clusterRun(ctx, "cibadmin", "--query"); err == nil && cibHasOrder(out, id, first, then) {
		return nil
	}
	_, err := clusterRun(ctx, "pcs", "constraint", "order", "start", first, "then", "start", then, "kind=Mandatory", "id="+id)
	if err == nil {
		return nil
	}
	if out, queryErr := clusterRun(ctx, "cibadmin", "--query"); queryErr == nil && cibHasOrder(out, id, first, then) {
		return nil
	}
	return err
}

func applyClusterResource(ctx context.Context, name string, create []string) error {
	if out, err := clusterRun(ctx, "cibadmin", "--query"); err == nil && cibHasResource(out, name, create) {
		return nil
	}
	_, err := clusterRun(ctx, "pcs", create...)
	if err == nil {
		return nil
	}
	if out, queryErr := clusterRun(ctx, "cibadmin", "--query"); queryErr == nil && cibHasResource(out, name, create) {
		return nil
	}
	return err
}

func applyFenceResourceAt(ctx context.Context, stateDir string, node protocol.StorageClusterNode) error {
	name := "nex-fence-" + node.HostID
	args, err := fenceCIBCreateArgs(stateDir, node)
	if err != nil {
		return err
	}
	return applyClusterResource(ctx, name, args)
}

// StorageClusterHealthAt observes CIB state using the same Core-owned
// credential directory that rendered the fence resources.  A custom agent
// state root must never silently fall back to the production default.
func StorageClusterHealthAt(ctx context.Context, stateDir string, s protocol.StorageRegistration, c protocol.StorageClusterConfig) (protocol.StorageClusterStatus, error) {
	status := protocol.StorageClusterStatus{ClusterID: c.ClusterID, RepositoryID: c.RepositoryID, Status: "blocked"}
	if err := c.ValidFor(s); err != nil {
		status.Error = err.Error()
		return status, err
	}
	if err := validateStorageClusterDLMFenceHelper(ctx); err != nil {
		status.Error = err.Error()
		return status, nil
	}
	q, err := clusterRun(ctx, "corosync-quorumtool", "-s")
	if err != nil {
		status.Error = err.Error()
		return status, nil
	}
	quorum, quorate := clusterQuorumStatus(q, c)
	status.Quorate = quorate
	pcs, err := clusterRun(ctx, "cibadmin", "--query")
	if err != nil {
		status.Error = err.Error()
		return status, nil
	}
	transport, transportErr := storageClusterTransportFor(s, c)
	status.Fences = append([]protocol.StorageClusterFenceStatus(nil), c.FenceProofs...)
	status.FencingVerified = transportErr == nil && c.FencingVerified() && storageClusterFencingPropertiesMatch(pcs) && storageClusterCIBMatchesConfig(pcs, stateDir, s, c)
	runtime, runtimeErr := clusterRun(ctx, "pcs", "status", "xml")
	if runtimeErr == nil && status.Quorate {
		dlmMembers, dlmActive := runtimeCloneActiveMembers(runtime, "nex-dlm", c, quorum.totalVotes)
		gfsMembers, gfsActive := runtimeCloneActiveMembers(runtime, "nex-gfs2-"+c.RepositoryID, c, quorum.totalVotes)
		transportMembers, transportActive := runtimeCloneActiveMembers(runtime, transport.resource, c, quorum.totalVotes)
		status.FencingVerified = status.FencingVerified && cibHasOrder(pcs, "nex-dlm-before-"+c.RepositoryID, "nex-dlm-clone", "nex-gfs2-"+c.RepositoryID+"-clone") && dlmActive && gfsActive && transportActive && maps.Equal(dlmMembers, gfsMembers) && maps.Equal(transportMembers, gfsMembers) && maps.Equal(dlmMembers, quorum.members) && dlmMembers[quorum.localHost]
	} else {
		status.FencingVerified = false
	}
	m, err := nativeRun(ctx, "findmnt", "-n", "-o", "SOURCE,FSTYPE,TARGET", "--target", c.MountPath)
	status.Mounted = matchingGFS2Mount(m, err, c.DevicePath, c.MountPath)
	status.Healthy = status.Quorate && status.Mounted && status.FencingVerified
	if status.Healthy {
		status.Status = "healthy"
	}
	return status, nil
}

func runtimeCloneActiveMembers(raw []byte, id string, c protocol.StorageClusterConfig, expected int) (map[string]bool, bool) {
	var root cibNode
	if expected < 1 || expected > len(c.Nodes) || xml.Unmarshal(raw, &root) != nil {
		return nil, false
	}
	var clone *cibNode
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local != "clone" || n.attr("id") != id+"-clone" {
			return false
		}
		if clone != nil {
			clone = nil
			return true
		}
		copy := n
		clone = &copy
		return false
	})
	if clone == nil {
		return nil, false
	}
	if !runtimeCloneSafe(*clone, id) {
		return nil, false
	}
	configured := map[string]bool{}
	for _, node := range c.Nodes {
		configured[strconv.FormatUint(uint64(node.CorosyncNodeID), 10)+"\x00"+node.HostID] = true
	}
	active := map[string]bool{}
	instances := 0
	for _, instance := range clone.Child {
		if instance.XMLName.Local != "resource" {
			return nil, false
		}
		if instance.attr("id") != id || !runtimeCloneResourceSafe(instance, id) {
			return nil, false
		}
		instances++
		activeValue, activeOK := runtimeAttribute(instance, "active")
		role, roleOK := runtimeAttribute(instance, "role")
		running, runningOK := runtimeAttribute(instance, "nodes_running_on")
		if !activeOK || !roleOK || !runningOK {
			return nil, false
		}
		if activeValue == "false" && role == "Stopped" && running == "0" {
			if len(instance.Child) != 0 {
				return nil, false
			}
			continue
		}
		if activeValue != "true" || role != "Started" || running != "1" {
			return nil, false
		}
		var member *cibNode
		for _, child := range instance.Child {
			if child.XMLName.Local != "node" {
				return nil, false
			}
			if member != nil {
				return nil, false
			}
			copy := child
			member = &copy
		}
		if member == nil {
			return nil, false
		}
		memberID, memberIDOK := runtimeAttribute(*member, "id")
		memberName, memberNameOK := runtimeAttribute(*member, "name")
		if !memberIDOK || !memberNameOK {
			return nil, false
		}
		key := memberID + "\x00" + memberName
		if !configured[key] || active[memberName] {
			return nil, false
		}
		active[memberName] = true
	}
	return active, instances == len(c.Nodes) && len(active) == expected
}

func runtimeCloneSafe(clone cibNode, id string) bool {
	return runtimeAttributesMatch(clone, map[string]string{"id": id + "-clone", "multi_state": "false", "unique": "false", "maintenance": "false", "managed": "true", "disabled": "false", "failed": "false", "failure_ignored": "false"})
}

func runtimeCloneResourceSafe(instance cibNode, id string) bool {
	agent := "ocf:heartbeat:Filesystem"
	if id == "nex-dlm" {
		agent = "ocf:pacemaker:controld"
	} else if strings.HasPrefix(id, "nex-iscsi-") {
		agent = "ocf:nex:cluster-iscsi-session"
	}
	return runtimeAttributesMatch(instance, map[string]string{"id": id, "resource_agent": agent, "orphaned": "false", "blocked": "false", "maintenance": "false", "managed": "true", "failed": "false", "failure_ignored": "false"})
}

func runtimeAttributesMatch(node cibNode, required map[string]string) bool {
	seen := map[string]bool{}
	for _, attr := range node.Attr {
		want, found := required[attr.Name.Local]
		if !found {
			continue
		}
		if seen[attr.Name.Local] || attr.Value != want {
			return false
		}
		seen[attr.Name.Local] = true
	}
	return len(seen) == len(required)
}

func runtimeAttribute(node cibNode, name string) (string, bool) {
	var value string
	found := false
	for _, attr := range node.Attr {
		if attr.Name.Local != name {
			continue
		}
		if found {
			return "", false
		}
		value, found = attr.Value, true
	}
	return value, found
}

func matchingGFS2Mount(raw []byte, err error, device, target string) bool {
	if err != nil {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(string(raw)))
	if len(fields) != 3 || fields[1] != "gfs2" || fields[2] != target {
		return false
	}
	configured, e1 := filepath.EvalSymlinks(device)
	source, e2 := filepath.EvalSymlinks(fields[0])
	return e1 == nil && e2 == nil && configured == source
}

type clusterQuorum struct {
	totalVotes int
	members    map[string]bool
	localHost  string
}

// clusterQuorumStatus accepts only the ordinary one-vote votequorum policy
// rendered for every selected Core. Nodes is the live voting membership, not
// the configured cluster size: a fenced 2/3 or 3/5 majority remains safe.
func clusterQuorumStatus(raw []byte, c protocol.StorageClusterConfig) (clusterQuorum, bool) {
	want := map[string]string{}
	membership := map[string]bool{}
	membershipStarted, membershipFound, localFound := false, false, false
	var localHost string
	var localMemberID uint32
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "Membership information" {
			if membershipStarted {
				return clusterQuorum{}, false
			}
			membershipStarted = true
			continue
		}
		fields := strings.Fields(line)
		membershipHeader := len(fields) == 3 && fields[0] == "Nodeid" && fields[1] == "Votes" && fields[2] == "Name"
		if membershipStarted && line != "" && !membershipHeader && !strings.HasPrefix(line, "---") {
			if len(fields) < 3 || len(fields) > 4 || (len(fields) == 4 && fields[3] != "(local)") {
				return clusterQuorum{}, false
			}
			nodeID, nodeErr := strconv.ParseUint(fields[0], 10, 32)
			votes, voteErr := strconv.Atoi(fields[1])
			if nodeErr != nil || voteErr != nil || votes != 1 || nodeID == 0 || membership[fields[2]] {
				return clusterQuorum{}, false
			}
			var configured bool
			for _, node := range c.Nodes {
				if node.HostID == fields[2] && node.CorosyncNodeID == uint32(nodeID) {
					configured = true
				}
			}
			if !configured {
				return clusterQuorum{}, false
			}
			membership[fields[2]], membershipFound = true, true
			if len(fields) == 4 {
				if localFound {
					return clusterQuorum{}, false
				}
				localFound = true
				localHost, localMemberID = fields[2], uint32(nodeID)
			}
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "Quorum provider", "Nodes", "Node ID", "Quorate", "Expected votes", "Highest expected", "Total votes", "Quorum", "Flags":
			if value == "" || want[key] != "" {
				return clusterQuorum{}, false
			}
			want[key] = value
		}
	}
	parse := func(key string) (int, bool) {
		value, err := strconv.Atoi(want[key])
		return value, err == nil && value > 0
	}
	nodes, nodesOK := parse("Nodes")
	localID, localErr := strconv.ParseUint(want["Node ID"], 10, 32)
	localOK := localErr == nil && localID > 0
	expected, expectedOK := parse("Expected votes")
	highest, highestOK := parse("Highest expected")
	total, totalOK := parse("Total votes")
	quorum, quorumOK := parse("Quorum")
	if !nodesOK || !localOK || !expectedOK || !highestOK || !totalOK || !quorumOK || !membershipStarted || !membershipFound || !localFound || want["Quorum provider"] != "corosync_votequorum" || want["Quorate"] != "Yes" || want["Flags"] != "Quorate" || expected != len(c.Nodes) || highest != expected || nodes != total || quorum != expected/2+1 || total < quorum || total > expected || len(membership) != total {
		return clusterQuorum{}, false
	}
	if uint64(localMemberID) != localID || !membership[localHost] {
		return clusterQuorum{}, false
	}
	for _, node := range c.Nodes {
		if node.HostID == localHost && node.CorosyncNodeID == localMemberID {
			return clusterQuorum{totalVotes: total, members: membership, localHost: localHost}, true
		}
	}
	return clusterQuorum{}, false
}

type cibNode struct {
	XMLName xml.Name
	Attr    []xml.Attr `xml:",any,attr"`
	Child   []cibNode  `xml:",any"`
}

func (n cibNode) attr(name string) string {
	for _, a := range n.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}
func (n cibNode) walk(fn func(cibNode) bool) bool {
	if fn(n) {
		return true
	}
	for _, child := range n.Child {
		if child.walk(fn) {
			return true
		}
	}
	return false
}
func cibHasProperty(raw []byte, name, value string) bool {
	var root cibNode
	if xml.Unmarshal(raw, &root) != nil {
		return false
	}
	var configuration *cibNode
	if root.XMLName.Local == "configuration" {
		configuration = &root
	} else {
		for i := range root.Child {
			if root.Child[i].XMLName.Local == "configuration" {
				if configuration != nil { // ambiguous CIB is never safe to interpret.
					return false
				}
				configuration = &root.Child[i]
			}
		}
	}
	if configuration == nil {
		return false
	}
	var crmConfig *cibNode
	for i := range configuration.Child {
		if configuration.Child[i].XMLName.Local == "crm_config" {
			if crmConfig != nil {
				return false
			}
			crmConfig = &configuration.Child[i]
		}
	}
	if crmConfig == nil {
		return false
	}
	values := []string{}
	crmConfig.walk(func(child cibNode) bool {
		if child.XMLName.Local == "nvpair" && child.attr("name") == name {
			values = append(values, child.attr("value"))
		}
		return false
	})
	return len(values) == 1 && values[0] == value
}

// cibHasPropertyName is whether a cluster property is set at all, whatever its value.
func cibHasPropertyName(raw []byte, name string) bool {
	var root cibNode
	return xml.Unmarshal(raw, &root) == nil && root.walk(func(n cibNode) bool {
		return n.XMLName.Local == "nvpair" && n.attr("name") == name
	})
}

func cibHasOrder(raw []byte, id, first, then string) bool {
	var root cibNode
	return xml.Unmarshal(raw, &root) == nil && root.walk(func(n cibNode) bool {
		return n.XMLName.Local == "rsc_order" && n.attr("id") == id && n.attr("first") == first && n.attr("then") == then && n.attr("kind") == "Mandatory"
	})
}
func cibHasResource(raw []byte, id string, create []string) bool {
	var root cibNode
	// All callers provide a pcs `resource create` or `stonith create` spec.
	// Malformed/internal failed render input must be an unhealthy mismatch, not
	// a process crash while inspecting a CIB.
	if xml.Unmarshal(raw, &root) != nil || len(create) < 4 || (create[0] != "resource" && create[0] != "stonith") || create[1] != "create" || create[2] != id || create[3] == "" {
		return false
	}
	agent := create[3]
	parts := strings.Split(agent, ":")
	disabled := false
	for _, value := range create {
		if value == "--disabled" {
			disabled = true
		}
	}
	params := create[4:]
	for i, value := range params {
		if value == "op" || value == "clone" {
			params = params[:i]
			break
		}
	}
	found := root.walk(func(n cibNode) bool {
		if n.attr("id") != id || (n.XMLName.Local != "primitive" && n.XMLName.Local != "stonith") {
			return false
		}
		if len(parts) == 3 && (n.attr("class") != parts[0] || n.attr("provider") != parts[1] || n.attr("type") != parts[2]) {
			return false
		}
		if len(parts) == 1 && n.attr("type") != parts[0] {
			return false
		}
		for _, param := range params {
			if !strings.Contains(param, "=") {
				continue
			}
			kv := strings.SplitN(param, "=", 2)
			if !n.walk(func(x cibNode) bool {
				return x.XMLName.Local == "nvpair" && x.attr("name") == kv[0] && x.attr("value") == kv[1]
			}) {
				return false
			}
		}
		return resourceOpsMatch(n, create)
	})
	resourceDisabled, unique := cibResourceDisabled(root, id)
	if !found || !unique || resourceDisabled != disabled {
		return false
	}
	return cloneResourceMatch(root, id, create)
}

// cibResourceDisabled follows Pacemaker's actual representation: --disabled
// on a cloned resource stores target-role=Stopped in the clone's
// meta_attributes, not under the primitive.  A primitive-level target-role is
// accepted too for compatible CIBs. Multiple matching containers are unsafe.
func cibResourceDisabled(root cibNode, id string) (stopped, unique bool) {
	seen, stopped := 0, false
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local != "primitive" || n.attr("id") != id {
			return false
		}
		seen++
		if n.walk(func(child cibNode) bool {
			return child.XMLName.Local == "nvpair" && child.attr("name") == "target-role" && child.attr("value") == "Stopped"
		}) {
			stopped = true
		}
		return false
	})
	root.walk(func(n cibNode) bool {
		if n.XMLName.Local != "clone" || !n.walk(func(child cibNode) bool {
			return child.XMLName.Local == "primitive" && child.attr("id") == id
		}) {
			return false
		}
		if n.walk(func(child cibNode) bool {
			return child.XMLName.Local == "nvpair" && child.attr("name") == "target-role" && child.attr("value") == "Stopped"
		}) {
			stopped = true
		}
		return false
	})
	return stopped, seen == 1
}

func resourceOpsMatch(n cibNode, create []string) bool {
	i := -1
	for x, value := range create {
		if value == "op" {
			i = x
			break
		}
	}
	if i < 0 {
		return true
	}
	if i+1 >= len(create) {
		return false
	}
	name := create[i+1]
	want := create[i+2:]
	for _, value := range want {
		if value == "clone" {
			break
		}
	}
	return n.walk(func(op cibNode) bool {
		if op.XMLName.Local != "op" || op.attr("name") != name {
			return false
		}
		for _, value := range want {
			if value == "clone" {
				break
			}
			if strings.Contains(value, "=") {
				kv := strings.SplitN(value, "=", 2)
				if op.attr(kv[0]) != kv[1] {
					return false
				}
			}
		}
		return true
	})
}

func cloneResourceMatch(root cibNode, id string, create []string) bool {
	i := -1
	for x, value := range create {
		if value == "clone" {
			i = x
			break
		}
	}
	if i < 0 {
		return true
	}
	want := create[i+1:]
	return root.walk(func(clone cibNode) bool {
		if clone.XMLName.Local != "clone" || !clone.walk(func(n cibNode) bool { return n.XMLName.Local == "primitive" && n.attr("id") == id }) {
			return false
		}
		for _, value := range want {
			if !strings.Contains(value, "=") {
				continue
			}
			kv := strings.SplitN(value, "=", 2)
			if !clone.walk(func(n cibNode) bool {
				return n.XMLName.Local == "nvpair" && n.attr("name") == kv[0] && n.attr("value") == kv[1]
			}) {
				return false
			}
		}
		return true
	})
}

// fenceCIBCreateArgs derives Pacemaker parameters from the same pinned native
// fence command used to create proof.  This keeps secret paths and SSH host-key
// pinning identical instead of maintaining a second, divergent renderer.
func fenceCIBCreateArgs(stateDir string, node protocol.StorageClusterNode) ([]string, error) {
	name, f := "nex-fence-"+node.HostID, node.Fence
	agent, native, err := fenceAgentArgs(stateDir, f, "off")
	if err != nil {
		return nil, err
	}
	args := []string{"stonith", "create", name, agent}
	for _, arg := range native {
		if strings.HasPrefix(arg, "--action=") {
			continue
		}
		if !strings.HasPrefix(arg, "--") {
			return nil, errors.New("unexpected native fence argument")
		}
		kv := strings.TrimPrefix(arg, "--")
		key, value, ok := strings.Cut(kv, "=")
		if !ok && (key == "ssh" || key == "lanplus") {
			value, ok = "1", true
		}
		if !ok || value == "" {
			return nil, errors.New("invalid native fence argument")
		}
		switch key {
		case "system-id":
			key = "system_id"
		case "password-script":
			key = "password_script"
		case "identity-file":
			key = "identity_file"
		case "ssh-options":
			key = "ssh_options"
		}
		// Pacemaker supplies the target port from pcmk_host_map. A static
		// fence_virsh plug is proof-only and must not override that mapping.
		if f.Kind == protocol.FenceVirsh && key == "plug" {
			continue
		}
		args = append(args, key+"="+value)
	}
	if f.Kind == protocol.FenceVirsh {
		args = append(args, "pcmk_host_map="+node.HostID+":"+f.TargetID)
	}
	return append(args, "pcmk_reboot_action=off", "pcmk_host_list="+node.HostID), nil
}
