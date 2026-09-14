// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

var clusterBootUnitPath = "/etc/systemd/system/nex-cluster-storage-ready.service"
var clusterBootDropinPath = "/etc/systemd/system/pacemaker.service.d/nex-cluster-storage.conf"

var clusterBootExecutable = os.Executable
var clusterBootOwnership = clusterBootOwned
var clusterBootStateOwnership = func(uid uint32) bool { return uid == 0 }
var clusterBootDaemonReload = func(ctx context.Context) error {
	return exec.CommandContext(ctx, "systemctl", "daemon-reload").Run()
}
var validateClusterBootDLMFenceHelper = vmhost.ValidateStorageClusterDLMFenceHelper

var clusterBootInstallMu sync.Mutex
var clusterBootReloaded = map[string]bool{}

// clusterBootArg intentionally accepts only a systemd-safe absolute path. The
// readiness unit has no operator-controlled arguments, endpoints, or secrets.
func clusterBootArg(s string) (string, error) {
	if !filepath.IsAbs(s) || filepath.Clean(s) != s || s == "/" || strings.ContainsAny(s, "\x00\r\n\t \\\"'$%") {
		return "", errors.New("unsafe systemd argument")
	}
	for _, r := range s {
		if !(r == '/' || r == '.' || r == '_' || r == '-' || r == ':' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return "", errors.New("unsafe systemd argument")
		}
	}
	return s, nil
}

func clusterBootOwned(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == 0 && info.Mode().Perm()&0022 == 0
}

func trustedClusterBootPath(path string, regular bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("cluster boot path is not absolute and clean")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !clusterBootOwnership(info) {
			return errors.New("cluster boot path is not root-owned and safe")
		}
		if current == path && regular {
			if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
				return errors.New("cluster readiness executable is unsafe")
			}
		} else if !info.IsDir() {
			return errors.New("cluster boot path parent is not a directory")
		}
		if current == "/" {
			return nil
		}
	}
}

// trustedClusterBootParent validates the first existing ancestor of path's
// parent before anything is created: MkdirAll follows symlinks, so it must never
// discover an unsafe systemd parent. With create, it then makes only the missing
// descendants; call that only after every unit passed preflight, so one foreign
// target cannot leave another target installed.
func trustedClusterBootParent(path string, create bool) error {
	missing := []string{}
	for current := filepath.Dir(path); ; current = filepath.Dir(current) {
		if _, err := os.Lstat(current); err == nil {
			if err := trustedClusterBootPath(current, false); err != nil {
				return err
			}
			break
		} else if !os.IsNotExist(err) {
			return err
		}
		if current == "/" {
			return errors.New("cluster boot parent is unavailable")
		}
		missing = append(missing, current)
	}
	for i := len(missing) - 1; create && i >= 0; i-- {
		if err := os.Mkdir(missing[i], 0755); err != nil && !os.IsExist(err) {
			return err
		}
		if err := trustedClusterBootPath(missing[i], false); err != nil {
			return err
		}
	}
	return nil
}

// InstallClusterStorageBootGate writes only exact Nex-owned unit content.
func InstallClusterStorageBootGate(s *Store) error {
	clusterBootInstallMu.Lock()
	defer clusterBootInstallMu.Unlock()
	exe, err := clusterBootExecutable()
	if err != nil {
		return err
	}
	exe, err = filepath.Abs(exe)
	if err != nil {
		return err
	}
	if err = trustedClusterBootPath(exe, true); err != nil {
		return err
	}
	exe, err = clusterBootArg(exe)
	if err != nil {
		return err
	}
	state, err := clusterBootArg(s.dir)
	if err != nil {
		return err
	}
	content := "# Managed by Nex Core; do not edit.\n[Unit]\nRequires=open-iscsi.service\nAfter=network-online.target open-iscsi.service\nBefore=pacemaker.service\n[Service]\nType=oneshot\nRemainAfterExit=yes\nTimeoutStartSec=2min\nExecStart=" + exe + " cluster-storage-ready --state-dir=" + state + "\n"
	dropin := "# Managed by Nex Core; do not edit.\n[Unit]\nRequires=nex-cluster-storage-ready.service\nAfter=nex-cluster-storage-ready.service\n"
	files := []struct{ path, body string }{{clusterBootUnitPath, content}, {clusterBootDropinPath, dropin}}
	changed := make([]bool, len(files))
	// Preflight every parent and target before creating a directory or writing
	// either file. A foreign drop-in must not leave a partial readiness unit.
	for i, f := range files {
		if err := trustedClusterBootParent(f.path, false); err != nil {
			return err
		}
		if old, err := os.Lstat(f.path); err == nil {
			if old.Mode()&os.ModeSymlink != 0 || !old.Mode().IsRegular() || !clusterBootOwnership(old) {
				return errors.New("cluster boot unit is not root-owned and safe")
			}
			body, readErr := os.ReadFile(f.path)
			if readErr != nil || string(body) != f.body {
				return fmt.Errorf("refusing foreign cluster boot unit")
			}
			continue
		} else if !os.IsNotExist(err) {
			return err
		}
		changed[i] = true
	}
	reloadKey := clusterBootUnitPath + "\x00" + clusterBootDropinPath
	if !changed[0] && !changed[1] && clusterBootReloaded[reloadKey] {
		return nil
	}
	for i, f := range files {
		if changed[i] {
			if err := trustedClusterBootParent(f.path, true); err != nil {
				return err
			}
		}
	}
	for i, f := range files {
		if !changed[i] {
			continue
		}
		if err := writePrivate(f.path, []byte(f.body), 0644); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := clusterBootDaemonReload(ctx); err != nil {
		return err
	}
	// This cache is intentionally process-local. It prevents ticker churn but
	// is forgotten on restart, when one daemon-reload is required to recover a
	// prior write that may have completed before its reload did.
	clusterBootReloaded[reloadKey] = true
	return nil
}

type clusterBootSnapshot struct {
	state      State
	membership protocol.PoolMembership
	clusters   storageClusterState
}

const clusterBootSnapshotLimit = 8 << 20

func readClusterBootPrivate(dir, name string) ([]byte, [32]byte, error) {
	var digest [32]byte
	path := filepath.Join(dir, name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, digest, errors.New("cluster boot state file is not private and regular")
	}
	before, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !clusterBootStateOwnership(before.Uid) {
		return nil, digest, errors.New("cluster boot state file is not private and regular")
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, digest, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	var stat syscall.Stat_t
	if err = syscall.Fstat(fd, &stat); err != nil || stat.Mode&syscall.S_IFMT != syscall.S_IFREG || stat.Mode&0077 != 0 || !clusterBootStateOwnership(stat.Uid) || stat.Dev != before.Dev || stat.Ino != before.Ino {
		return nil, digest, errors.New("cluster boot state file changed or is unsafe")
	}
	b, err := io.ReadAll(io.LimitReader(f, clusterBootSnapshotLimit+1))
	if err != nil || len(b) > clusterBootSnapshotLimit {
		return nil, digest, errors.New("cluster boot state file is unreadable")
	}
	return b, sha256.Sum256(b), nil
}

func readClusterBootSnapshot(dir string) (clusterBootSnapshot, error) {
	var out clusterBootSnapshot
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return out, errors.New("cluster boot state directory is unsafe")
	}
	resolved, resolveErr := filepath.EvalSymlinks(dir)
	if resolveErr != nil || resolved != dir {
		return out, errors.New("cluster boot state directory is unsafe")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return out, errors.New("cluster boot state directory is unsafe")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !clusterBootStateOwnership(stat.Uid) {
		return out, errors.New("cluster boot state directory is unsafe")
	}
	// The privileged gate treats this directory as authority for iSCSI login.
	// A safe leaf under a writable or foreign-owned ancestor is not safe.
	if trustedClusterBootPath(filepath.Dir(dir), false) != nil {
		return out, errors.New("cluster boot state directory is unsafe")
	}
	for attempt := 0; attempt < 3; attempt++ {
		stateRaw, stateDigest, err := readClusterBootPrivate(dir, "state.json")
		if err != nil {
			return out, err
		}
		peerRaw, peerDigest, err := readClusterBootPrivate(dir, poolPeerStateFile)
		if err != nil {
			return out, err
		}
		clusterRaw, clusterDigest, err := readClusterBootPrivate(dir, "pool-storage-cluster.json")
		if err != nil {
			return out, err
		}
		var state State
		var peer poolPeerState
		clusters := storageClusterState{Operations: map[string]storageClusterOperation{}, FenceRequests: map[string]storageFenceRequest{}}
		if json.Unmarshal(stateRaw, &state) != nil || json.Unmarshal(peerRaw, &peer) != nil || json.Unmarshal(clusterRaw, &clusters) != nil || clusters.Operations == nil {
			return out, errors.New("cluster boot projection is invalid")
		}
		_, stateAfter, e1 := readClusterBootPrivate(dir, "state.json")
		_, peerAfter, e2 := readClusterBootPrivate(dir, poolPeerStateFile)
		_, clusterAfter, e3 := readClusterBootPrivate(dir, "pool-storage-cluster.json")
		if e1 == nil && e2 == nil && e3 == nil && stateDigest == stateAfter && peerDigest == peerAfter && clusterDigest == clusterAfter {
			out.state, out.membership, out.clusters = state, peer.Membership, clusters
			return out, nil
		}
	}
	return out, errors.New("cluster boot projection changed during snapshot")
}

func validClusterBootMembership(hostID, poolID string, membership protocol.PoolMembership, config protocol.StorageClusterConfig) error {
	if hostID == "" || poolID == "" || membership.Valid() != nil || membership.PoolID != poolID || config.PoolID != poolID {
		return errors.New("cluster boot membership is not current")
	}
	if len(membership.Members) < 3 || len(membership.Members) > 5 {
		return errors.New("cluster boot membership is not current")
	}
	ready := map[string]bool{}
	for _, member := range membership.Members {
		if member.Status != "ready" || ready[member.HostID] {
			return errors.New("cluster boot membership is not current")
		}
		ready[member.HostID] = true
	}
	if !ready[hostID] || len(config.Nodes) != len(ready) {
		return errors.New("cluster boot membership is not current")
	}
	for _, node := range config.Nodes {
		if !ready[node.HostID] {
			return errors.New("cluster boot membership is not current")
		}
	}
	return nil
}

// validClusterBootGateIntent is deliberately narrower than generic storage
// state: installing a boot dependency is allowed for an already durable,
// fenced desired configuration even when a device is temporarily absent. The
// gate itself still performs the exact iSCSI/GFS2 Connect before Pacemaker.
func validClusterBootGateIntent(s *Store, membership protocol.PoolMembership, op storageClusterOperation, registration protocol.StorageRegistration) error {
	if op.RequestID == "" || op.Digest == "" || op.MembershipGeneration != membership.Generation || op.Config.ClusterID == "" || op.Config.ValidFor(registration) != nil || !op.Config.FencingVerified() {
		return errors.New("cluster boot gate intent is unavailable or unsafe")
	}
	switch op.Status {
	case "queued", "applying", "healthy", "failed", "needs_attention":
	default:
		// A completed initial admission remains eligible to establish the boot
		// dependency even when the last runtime report was degraded. The boot
		// command still performs exact Connect before Pacemaker can start.
		if !clusterOperationActivated(op, membership, op.Config) {
			return errors.New("cluster boot gate intent is unavailable or unsafe")
		}
	}
	return validClusterBootMembership(s.State.HostID, s.State.PoolID, membership, op.Config)
}

func clusterBootOperations(state storageClusterState) ([]storageClusterOperation, error) {
	// Reject divergent history at every generation before selecting the newest
	// record. Selecting while ranging the map would allow a newer entry to hide
	// conflicting older desired state depending on map iteration order.
	generationDigests := map[string]string{}
	for _, candidate := range state.Operations {
		repository := candidate.Config.RepositoryID
		if repository == "" {
			continue
		}
		key := repository + "\x00" + fmt.Sprintf("%020d", candidate.Config.Generation)
		if digest, ok := generationDigests[key]; ok && digest != candidate.Config.Digest() {
			return nil, errors.New("cluster boot projection is ambiguous")
		}
		generationDigests[key] = candidate.Config.Digest()
	}
	selected := map[string]storageClusterOperation{}
	selectedKey := map[string]string{}
	for key, candidate := range state.Operations {
		repository := candidate.Config.RepositoryID
		if repository == "" {
			continue
		}
		current, ok := selected[repository]
		if !ok || candidate.Config.Generation > current.Config.Generation || candidate.Config.Generation == current.Config.Generation && (candidate.UpdatedAt.After(current.UpdatedAt) || candidate.UpdatedAt.Equal(current.UpdatedAt) && key > selectedKey[repository]) {
			selected[repository], selectedKey[repository] = candidate, key
			continue
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("no approved clustered storage boot projection")
	}
	keys := make([]string, 0, len(selected))
	for key := range selected {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]storageClusterOperation, 0, len(keys))
	for _, key := range keys {
		out = append(out, selected[key])
	}
	return out, nil
}

func clusterStorageBootReady(ctx context.Context, dir string, snapshot clusterBootSnapshot) error {
	operations, err := clusterBootOperations(snapshot.clusters)
	if err != nil {
		return err
	}
	// Validate every selected projection before an iSCSI login can have side effects.
	for _, op := range operations {
		initialOrActivated := op.Status == "applying" || op.Status == "healthy" || clusterOperationActivated(op, snapshot.membership, op.Config)
		if !initialOrActivated || op.Config.RepositoryID == "" || op.Digest == "" || op.Config.ValidFor(op.Registration) != nil || !op.Config.FencingVerified() || op.Registration.Generation != op.Config.Generation || op.MembershipGeneration != snapshot.membership.Generation {
			return errors.New("cluster boot projection is invalid")
		}
		if err := validClusterBootMembership(snapshot.state.HostID, snapshot.state.PoolID, snapshot.membership, op.Config); err != nil {
			return err
		}
	}
	// This unit is ordered before Pacemaker. A successful iSCSI identity check
	// alone must not permit Pacemaker to start when DLM's runtime fencing ABI is
	// unavailable; that can otherwise deadlock GFS2 startup without fencing.
	if err := validateClusterBootDLMFenceHelper(ctx); err != nil {
		return errors.New("clustered storage boot fencing helper is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, op := range operations {
		if _, err := connectStorageCluster(ctx, dir, op.Registration, op.Config); err != nil {
			return errors.New("clustered iSCSI boot readiness failed")
		}
	}
	return nil
}

// ClusterStorageBootReady is the local, pre-Pacemaker boot gate. It consumes
// only a stable, durable Core snapshot; it never contacts a peer or writes CIB.
func ClusterStorageBootReady(ctx context.Context, s *Store) error {
	return ClusterStorageBootReadyFromDir(ctx, s.dir)
}

// ClusterStorageBootReadyFromDir is used by the short-lived systemd gate while
// the long-running agent owns the exclusive Store lock.
func ClusterStorageBootReadyFromDir(ctx context.Context, dir string) error {
	snapshot, err := readClusterBootSnapshot(dir)
	if err != nil {
		return err
	}
	return clusterStorageBootReady(ctx, dir, snapshot)
}
