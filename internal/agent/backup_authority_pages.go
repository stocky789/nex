// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupPageDir = "pool-backup-replica-pages"
const poolBackupBlobDir = "pool-backup-page-blobs"

var errPoolBackupBlobCorrupt = errors.New("backup authority page blob is corrupt")
var poolBackupBlobMu sync.Mutex
var activePoolBackupStages = map[string]bool{}

var poolBackupBundleLimit = protocol.PoolBackupBundleLimit

type poolBackupPageUpload struct {
	Replica        *protocol.PoolAuthorityReplica `json:"replica,omitempty"`
	ManifestSHA256 string                         `json:"manifest_sha256,omitempty"`
	Index          int                            `json:"index"`
	Data           []byte                         `json:"data"`
	ReuseSHA256    string                         `json:"reuse_sha256,omitempty"`
}

type poolBackupPageProgress struct {
	NextIndex int `json:"next_index"`
}

func poolBackupPageFile(s *Store, digest string, index int) string {
	return filepath.Join(s.dir, poolBackupPageDir, digest, fmt.Sprintf("%04d", index))
}

func poolBackupBlobFile(s *Store, digest string) string {
	return filepath.Join(s.dir, poolBackupBlobDir, digest)
}

func newPoolBackupStage(s *Store) (string, error) {
	poolBackupBlobMu.Lock()
	defer poolBackupBlobMu.Unlock()
	parent := filepath.Join(s.dir, poolBackupPageDir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return "", err
	}
	removed, err := removeAbandonedPoolBackupStages(parent)
	if err != nil {
		return "", err
	}
	if removed {
		if err := gcPoolBackupOrphanBlobs(s); err != nil {
			return "", err
		}
	}
	stage, err := os.MkdirTemp(parent, ".stage-")
	if err == nil {
		activePoolBackupStages[stage] = true
	}
	return stage, err
}

func releasePoolBackupStage(path string) {
	poolBackupBlobMu.Lock()
	defer poolBackupBlobMu.Unlock()
	_ = os.RemoveAll(path)
	delete(activePoolBackupStages, path)
}

// Called under poolBackupBlobMu; only stages from a previous process are reaped.
func removeAbandonedPoolBackupStages(parent string) (bool, error) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return false, err
	}
	removed := false
	for _, entry := range entries {
		path := filepath.Join(parent, entry.Name())
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), ".stage-") || activePoolBackupStages[path] {
			continue
		}
		if err := os.RemoveAll(path); err != nil {
			return false, err
		}
		removed = true
	}
	if removed {
		return true, syncPoolBackupDirectory(parent)
	}
	return false, nil
}

func poolBackupHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func validPoolBackupHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && strings.ToLower(value) == value
}

func readBoundedPoolBackupFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, protocol.PoolBackupPageLimit+1))
}

func storePoolBackupBlob(s *Store, page []byte) (string, error) {
	if len(page) == 0 || len(page) > protocol.PoolBackupPageLimit {
		return "", errors.New("backup authority page size is invalid")
	}
	hash := poolBackupHash(page)
	path := poolBackupBlobFile(s, hash)
	data, err := readBoundedPoolBackupFile(path)
	if err == nil {
		if !bytes.Equal(data, page) {
			// Rewrite the corrupt inode itself so page links to it are repaired too.
			return hash, rewritePoolBackupBlob(path, page)
		}
		return hash, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return "", err
	}
	if err := vmhost.AtomicWrite(path, page, 0600); err != nil {
		return "", err
	}
	return hash, syncPoolBackupDirectory(filepath.Dir(path))
}

func rewritePoolBackupBlob(path string, page []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	if _, err := file.Write(page); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func linkPoolBackupBlob(s *Store, hash, path string) error {
	if !validPoolBackupHash(hash) {
		return errors.New("backup authority page blob digest is invalid")
	}
	blob := poolBackupBlobFile(s, hash)
	data, err := readBoundedPoolBackupFile(blob)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > protocol.PoolBackupPageLimit || poolBackupHash(data) != hash {
		return errPoolBackupBlobCorrupt
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if old, err := readBoundedPoolBackupFile(path); err == nil && poolBackupHash(old) == hash {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Link(blob, path); err != nil {
		return err
	}
	return syncPoolBackupDirectory(filepath.Dir(path))
}

// Keep the pushed or acknowledged root and the root of this host's stored
// replica, which is what a promotion of this host would activate. A running
// transfer can still need an older signed replica, so it defers GC.
func gcPoolBackupPages(s *Store, keep string) error {
	transfer, err := loadPoolTransfer(s)
	if err != nil || transfer != nil && !poolTransferTerminal(transfer.Stage) {
		return err
	}
	stored := ""
	poolReplicaMu.Lock()
	record, err := loadPoolReplicaUnchecked(s)
	poolReplicaMu.Unlock()
	if err != nil {
		return err
	}
	if record != nil && record.Replica.BackupManifest != nil {
		stored = record.Replica.BackupManifest.SHA256
	}
	poolBackupBlobMu.Lock()
	defer poolBackupBlobMu.Unlock()
	parent := filepath.Join(s.dir, poolBackupPageDir)
	if _, err := removeAbandonedPoolBackupStages(parent); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, err := os.ReadDir(parent)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep || entry.Name() == stored || strings.HasPrefix(entry.Name(), ".stage-") {
			continue
		}
		if err := os.RemoveAll(filepath.Join(parent, entry.Name())); err != nil {
			return err
		}
	}
	if err := syncPoolBackupDirectory(parent); err != nil {
		return err
	}
	return gcPoolBackupOrphanBlobs(s)
}

// The caller holds poolBackupBlobMu and has removed any temporary page links.
func gcPoolBackupOrphanBlobs(s *Store) error {
	blobs := filepath.Join(s.dir, poolBackupBlobDir)
	files, err := os.ReadDir(blobs)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range files {
		if entry.IsDir() || !validPoolBackupHash(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink == 1 {
			if err := os.Remove(filepath.Join(blobs, entry.Name())); err != nil {
				return err
			}
		}
	}
	return syncPoolBackupDirectory(blobs)
}

// Check an expanding authority record before it is committed. A master must not
// accept backup state that its members cannot hold in a signed replica. Writes
// that do not grow the authority, such as settling a reserved run, are never
// refused: their room was admitted with the reservation.
func checkPoolBackupAuthorityCapacity(s *Store, edit func(*protocol.PoolBackupAuthority)) error {
	peer, err := loadPoolPeer(s)
	if err != nil {
		return err
	}
	if s.State.HostID == "" || peer.Membership.MasterHostID != s.State.HostID {
		return nil // Member mirrors and promotion staging do not publish authority.
	}
	source, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil {
		return err
	}
	grows := false
	if source != nil && poolBackupNeedsV2(source) {
		// A v2 authority is not loaded whole; the edit grows it only by naming a
		// record the store neither holds nor reserved.
		auth := &protocol.PoolBackupAuthority{}
		edit(auth)
		grows = poolBackupAuthorityEditAdds(s, source, auth)
	} else {
		auth, err := PoolBackupAuthorityRefs(s)
		if err != nil {
			return err
		}
		if auth == nil {
			auth = &protocol.PoolBackupAuthority{}
		}
		before, _, err := poolBackupAuthorityReservedBytes(auth)
		if err != nil {
			return err
		}
		edit(auth)
		after, v2, err := poolBackupAuthorityReservedBytes(auth)
		if err != nil {
			return err
		}
		grows = after > before && (v2 || after > poolBackupBundleLimit)
	}
	// The adopting master imports its catalog before it re-points members, so
	// none of them can confirm its authority yet.
	if !grows || peer.Transfer != nil && peer.Transfer.Stage == "catalog-rebuild" {
		return nil
	}
	return requirePoolBackupMembersCurrent(s, peer.Membership)
}

// poolBackupAuthorityReservedBytes is the authority's encoded size plus the
// room every unresolved run and receipt needs for its terminal outcome.
func poolBackupAuthorityReservedBytes(auth *protocol.PoolBackupAuthority) (int, bool, error) {
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		return 0, false, fmt.Errorf("backup authority cannot be replicated: %w", err)
	}
	used := manifest.TotalBytes
	cataloged := make(map[string]bool, len(auth.Catalog))
	for _, point := range auth.Catalog {
		cataloged[point.ID] = true
	}
	for _, run := range auth.History {
		if run.Status != "pending" && run.Status != "needs_attention" {
			continue
		}
		body, _ := json.Marshal(run)
		terminal := run
		terminal.Status = "needs_attention"
		terminal.Stage = "authority_transfer"
		terminal.Error = strings.Repeat("\x00", 4096)
		maximum, _ := json.Marshal(terminal)
		if len(maximum)+5 > protocol.PoolBackupPageLimit {
			return 0, false, errors.New("backup run has no room for its terminal outcome")
		}
		used += protocol.PoolBackupPageLimit - len(body) - 5
		if (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) &&
			run.RecoveryPointID != "" && !cataloged[run.RecoveryPointID] {
			used += protocol.PoolBackupPageLimit
		}
	}
	for _, receipt := range auth.MetadataReceipts {
		if receipt.Status != "pending" && receipt.Status != "needs_attention" {
			continue
		}
		current, _ := json.Marshal(receipt)
		terminal := receipt
		terminal.Status = "needs_attention"
		terminal.Error = strings.Repeat("\x00", 4096)
		maximum, _ := json.Marshal(terminal)
		if len(maximum)+5 > protocol.PoolBackupPageLimit {
			return 0, false, errors.New("backup metadata receipt has no room for its terminal outcome")
		}
		if growth := len(maximum) - len(current) + 10; growth > 0 {
			used += growth
		}
	}
	return used, manifest.Version == 2, nil
}

// poolBackupAuthorityEditAdds reports whether records an edit wrote into an
// empty authority are new to the stored v2 authority. A run's reserved point
// is not new: its settlement was admitted when the run was reserved.
func poolBackupAuthorityEditAdds(s *Store, source *protocol.PoolBackupManifest, edit *protocol.PoolBackupAuthority) bool {
	stored := func(dir, id string) bool {
		_, err := os.Stat(filepath.Join(dir, id+".json"))
		return err == nil
	}
	reserved := map[string]bool{}
	for _, run := range edit.History {
		existing, err := loadPoolBackupRun(s, run.OperationID)
		if err != nil {
			return true
		}
		if existing.Status == "pending" || existing.Status == "needs_attention" {
			reserved[existing.RecoveryPointID] = true
		}
	}
	for _, point := range edit.Catalog {
		if !reserved[point.ID] && !stored(poolBackupCatalogPath(s), point.ID) {
			return true
		}
	}
	for _, receipt := range edit.MetadataReceipts {
		if !stored(poolBackupReceiptsPath(s), receipt.OperationID) {
			return true
		}
	}
	return len(edit.Repositories) > source.Repositories || len(edit.Definitions) > source.Definitions || len(edit.Pins) > source.Pins
}

func requirePoolBackupMembersCurrent(s *Store, membership protocol.PoolMembership) error {
	members := poolConfirmationPeers(membership, s.State.HostID)
	required := members[:0]
	for _, member := range members {
		if poolAuthorityReplicaAckRequired(member) {
			required = append(required, member)
		}
	}
	if len(required) == 0 {
		return nil
	}
	generation, digest, err := PoolAuthorityGeneration(s)
	if err != nil || generation == 0 || digest == "" {
		return errors.New("backup authority growth requires current signed pool authority")
	}
	for _, member := range required {
		status, err := refreshPoolBackupMemberStatus(s, member)
		if err != nil {
			log.Printf("backup authority growth refused: authenticated member replica status unavailable")
			return errors.New("backup authority growth requires every ready pool member to hold the current replica")
		}
		if status.Error != "" || !status.Verified || status.Generation != generation || status.Digest != digest {
			log.Printf("backup authority growth refused: member replica verified=%t error=%t generation_match=%t digest_match=%t",
				status.Verified, status.Error != "", status.Generation == generation, status.Digest == digest)
			return errors.New("backup authority growth requires every ready pool member to hold the current replica")
		}
	}
	currentGeneration, currentDigest, err := PoolAuthorityGeneration(s)
	if err != nil || currentGeneration != generation || currentDigest != digest {
		return errors.New("backup authority changed during growth admission")
	}
	currentMembership, err := Membership(s)
	if err != nil || currentMembership.Generation != membership.Generation || currentMembership.MasterHostID != membership.MasterHostID {
		return errors.New("pool membership changed during backup authority growth admission")
	}
	return nil
}

func refreshPoolBackupMemberStatus(s *Store, member protocol.PoolMember) (protocol.PoolReplicaStatus, error) {
	if member.PeerOrigin == "" {
		return protocol.PoolReplicaStatus{}, errors.New("pool member peer origin is unavailable")
	}
	client, err := PoolMemberPeerClient(s, member.PeerOrigin, member.HostID)
	if err != nil {
		return protocol.PoolReplicaStatus{}, err
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(member.PeerOrigin, "/")+poolReplicaPath, nil)
	if err != nil {
		return protocol.PoolReplicaStatus{}, err
	}
	response, err := client.Do(request)
	if err != nil {
		return protocol.PoolReplicaStatus{}, err
	}
	defer response.Body.Close()
	var status protocol.PoolReplicaStatus
	if response.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(response.Body, 1<<16)).Decode(&status) != nil ||
		status.MasterHostID != s.State.HostID {
		return protocol.PoolReplicaStatus{}, errors.New("pool member replica status is unavailable")
	}
	recordPoolReplicaObservation(s, member.HostID, status)
	poolReplicaMu.Lock()
	status = poolReplicaObservations[s.dir][member.HostID]
	poolReplicaMu.Unlock()
	return status, nil
}

// Each frame is a type byte, a four-byte big-endian JSON length, then exactly
// one record. No frame crosses a page; no record or page exceeds 256 KiB.
func encodePoolBackupAuthority(auth *protocol.PoolBackupAuthority, emit func([]byte) error) (*protocol.PoolBackupManifest, error) {
	if auth == nil {
		auth = &protocol.PoolBackupAuthority{}
	}
	if err := auth.Valid(); err != nil {
		return nil, err
	}
	m := &protocol.PoolBackupManifest{
		Repositories: len(auth.Repositories), Definitions: len(auth.Definitions), History: len(auth.History),
		Catalog: len(auth.Catalog), Pins: len(auth.Pins), MetadataReceipts: len(auth.MetadataReceipts),
	}
	var page bytes.Buffer
	whole := sha256.New()
	flush := func() error {
		if page.Len() == 0 {
			return nil
		}
		data := page.Bytes()
		m.TotalBytes += len(data)
		m.PageCount++
		if m.Version == 0 {
			m.PageBytes = append(m.PageBytes, len(data))
			m.PageSHA256 = append(m.PageSHA256, poolBackupHash(data))
			if m.TotalBytes > poolBackupBundleLimit || len(m.PageBytes) > 1024 {
				m.Version, m.PageBytes, m.PageSHA256 = 2, nil, nil
			}
		}
		_, _ = whole.Write(data)
		if emit != nil {
			if err := emit(data); err != nil {
				return err
			}
		}
		page.Reset()
		return nil
	}
	frame := func(kind byte, record any) error {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(body)+5 > protocol.PoolBackupPageLimit {
			return errors.New("backup authority record exceeds page limit")
		}
		if page.Len()+len(body)+5 > protocol.PoolBackupPageLimit {
			if err := flush(); err != nil {
				return err
			}
		}
		page.WriteByte(kind)
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(body)))
		_, _ = page.Write(size[:])
		_, _ = page.Write(body)
		return nil
	}
	for _, record := range auth.Repositories {
		if err := frame(1, record); err != nil {
			return nil, err
		}
	}
	for _, record := range auth.Definitions {
		if err := frame(2, record); err != nil {
			return nil, err
		}
	}
	for _, record := range auth.History {
		if err := frame(3, record); err != nil {
			return nil, err
		}
	}
	for _, record := range auth.Catalog {
		if err := frame(4, record); err != nil {
			return nil, err
		}
	}
	for _, record := range auth.Pins {
		if err := frame(5, record); err != nil {
			return nil, err
		}
	}
	for _, record := range auth.MetadataReceipts {
		if err := frame(6, record); err != nil {
			return nil, err
		}
	}
	if m.TotalBytes == 0 && page.Len() == 0 {
		_, _ = page.Write([]byte{0, 0, 0, 0, 0})
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if m.Version == 0 && (m.History > 100000 || m.Catalog > 100000 || m.Pins > 100000 || m.MetadataReceipts > 100000) {
		m.Version, m.PageBytes, m.PageSHA256 = 2, nil, nil
	}
	if m.Version == 0 {
		m.PageCount = 0
	}
	m.SHA256 = fmt.Sprintf("%x", whole.Sum(nil))
	return m, m.Valid()
}

func encodePoolBackupAuthorityFromStore(s *Store, emit func([]byte) error) (*protocol.PoolBackupManifest, error) {
	m := &protocol.PoolBackupManifest{Version: 2}
	var page bytes.Buffer
	whole := sha256.New()
	flush := func() error {
		if page.Len() == 0 {
			return nil
		}
		data := page.Bytes()
		m.PageCount++
		m.TotalBytes += len(data)
		_, _ = whole.Write(data)
		if emit != nil {
			if err := emit(data); err != nil {
				return err
			}
		}
		page.Reset()
		return nil
	}
	visit := func(kind byte, record any) error {
		body, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if len(body)+5 > protocol.PoolBackupPageLimit {
			return errors.New("backup authority record exceeds page limit")
		}
		if page.Len()+len(body)+5 > protocol.PoolBackupPageLimit {
			if err := flush(); err != nil {
				return err
			}
		}
		page.WriteByte(kind)
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(body)))
		_, _ = page.Write(size[:])
		_, _ = page.Write(body)
		// Records content decides where a page ends, so one changed or inserted
		// record leaves the other pages, and their reusable blobs, unchanged.
		if sha256.Sum256(body)[0] == 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		switch kind {
		case 1:
			m.Repositories++
		case 2:
			m.Definitions++
		case 3:
			m.History++
		case 4:
			m.Catalog++
		case 5:
			m.Pins++
		case 6:
			m.MetadataReceipts++
		default:
			return errors.New("backup authority record kind is invalid")
		}
		return nil
	}
	nonempty, err := walkPoolBackupAuthorityRecords(s, visit)
	if err != nil || !nonempty {
		return nil, err
	}
	if err := flush(); err != nil {
		return nil, err
	}
	m.SHA256 = fmt.Sprintf("%x", whole.Sum(nil))
	return m, m.Valid()
}

const poolBackupCapacityCheck = "pool-backup-authority-capacity"

type poolBackupAuthorityUsage struct {
	observedAt time.Time
	manifest   protocol.PoolBackupManifest
}

var poolBackupAuthorityUsages = map[string]poolBackupAuthorityUsage{}
var poolBackupAuthorityUsageMu sync.Mutex

// refreshPoolBackupAuthorityUsage measures the stored authority off the heartbeat
// path; PoolBackupAuthorityCapacityCheck reports the last measurement. Only the
// pool master measures: members hold a replica of what it limits.
func refreshPoolBackupAuthorityUsage(s *Store) error {
	if membership, err := Membership(s); err != nil || membership.MasterHostID != s.State.HostID {
		poolBackupAuthorityUsageMu.Lock()
		delete(poolBackupAuthorityUsages, s.dir)
		poolBackupAuthorityUsageMu.Unlock()
		return nil
	}
	m, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil {
		return err
	}
	usage := poolBackupAuthorityUsage{observedAt: time.Now().UTC()}
	if m != nil {
		usage.manifest = *m
	}
	poolBackupAuthorityUsageMu.Lock()
	poolBackupAuthorityUsages[s.dir] = usage
	poolBackupAuthorityUsageMu.Unlock()
	return nil
}

// PoolBackupAuthorityCapacityCheck warns once a limit Core refuses at, the number of
// repositories or policies, is 80% used. Sizes and record counts only pick the
// replication form and are never limits.
func PoolBackupAuthorityCapacityCheck(s *Store) []protocol.Check {
	poolBackupAuthorityUsageMu.Lock()
	usage, ok := poolBackupAuthorityUsages[s.dir]
	poolBackupAuthorityUsageMu.Unlock()
	if !ok {
		return nil
	}
	m := usage.manifest
	var near []string
	for _, limit := range []struct {
		name          string
		used, maximum int
	}{
		{"repositories", m.Repositories, protocol.PoolBackupMaxRepositories},
		{"policies", m.Definitions, protocol.PoolBackupMaxDefinitions},
	} {
		if limit.used*5 >= limit.maximum*4 {
			near = append(near, fmt.Sprintf("%s %d of %d", limit.name, limit.used, limit.maximum))
		}
	}
	check := protocol.Check{Name: poolBackupCapacityCheck, Status: "healthy", Freshness: "fresh", ObservedAt: usage.observedAt}
	if len(near) != 0 {
		check.Status = "degraded"
		check.Error = "backup authority is at 80% or more of its limits: " + strings.Join(near, ", ")
	}
	return []protocol.Check{check}
}

func poolBackupNeedsV2(m *protocol.PoolBackupManifest) bool {
	return m.TotalBytes > poolBackupBundleLimit || m.History > 100000 || m.Catalog > 100000 ||
		m.Pins > 100000 || m.MetadataReceipts > 100000
}

// stagePoolBackupAuthorityFromStore writes a v2 root's pages only when that
// root is not already staged; a v1-sized authority writes nothing.
func stagePoolBackupAuthorityFromStore(s *Store) (*protocol.PoolBackupManifest, error) {
	m, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil || m == nil || !poolBackupNeedsV2(m) || verifyPoolBackupAuthorityPages(s, m) == nil {
		return m, err
	}
	temporary, err := newPoolBackupStage(s)
	if err != nil {
		return nil, err
	}
	defer releasePoolBackupStage(temporary)
	index := 0
	m, err = encodePoolBackupAuthorityFromStore(s, func(page []byte) error {
		path := filepath.Join(temporary, fmt.Sprintf("%04d", index))
		index++
		poolBackupBlobMu.Lock()
		defer poolBackupBlobMu.Unlock()
		hash, err := storePoolBackupBlob(s, page)
		if err != nil {
			return err
		}
		return os.Link(poolBackupBlobFile(s, hash), path)
	})
	if err != nil || m == nil {
		return nil, err
	}
	if err := syncPoolBackupDirectory(temporary); err != nil {
		return nil, err
	}
	parent := filepath.Join(s.dir, poolBackupPageDir)
	poolBackupBlobMu.Lock()
	defer poolBackupBlobMu.Unlock()
	if err := os.RemoveAll(filepath.Join(parent, m.SHA256)); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, filepath.Join(parent, m.SHA256)); err != nil {
		return nil, err
	}
	return m, syncPoolBackupDirectory(parent)
}

// stagePoolBackupReplicaPages makes a built replica's backup pages readable for
// the push. The caller holds poolBackupAuthorityMu, so the store cannot move on.
func stagePoolBackupReplicaPages(s *Store, m *protocol.PoolBackupManifest) error {
	if m == nil || verifyPoolBackupAuthorityPages(s, m) == nil {
		return nil
	}
	var staged *protocol.PoolBackupManifest
	var err error
	if m.Version == 2 {
		staged, err = stagePoolBackupAuthorityFromStore(s)
	} else {
		var auth *protocol.PoolBackupAuthority
		if auth, err = PoolBackupAuthorityRefs(s); err == nil {
			staged, err = stagePoolBackupAuthority(s, auth)
		}
	}
	if err == nil && (staged == nil || staged.SHA256 != m.SHA256) {
		err = errors.New("backup authority changed while its pages were staged")
	}
	return err
}

func stagePoolBackupAuthority(s *Store, auth *protocol.PoolBackupAuthority) (*protocol.PoolBackupManifest, error) {
	if auth == nil {
		return nil, nil
	}
	parent := filepath.Join(s.dir, poolBackupPageDir)
	temporary, err := newPoolBackupStage(s)
	if err != nil {
		return nil, err
	}
	defer releasePoolBackupStage(temporary)
	index := 0
	m, err := encodePoolBackupAuthority(auth, func(page []byte) error {
		path := filepath.Join(temporary, fmt.Sprintf("%04d", index))
		index++
		return vmhost.AtomicWrite(path, page, 0600)
	})
	if err != nil {
		return nil, err
	}
	destination := filepath.Join(parent, m.SHA256)
	if err := verifyPoolBackupAuthorityPages(s, m); err == nil {
		return m, nil
	}
	if _, err := os.Stat(destination); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(temporary, destination); err != nil {
			return nil, err
		}
	} else {
		if err != nil {
			return nil, err
		}
		for index := 0; index < poolBackupPageCount(m); index++ {
			page, readErr := os.ReadFile(filepath.Join(temporary, fmt.Sprintf("%04d", index)))
			if readErr != nil {
				return nil, readErr
			}
			if err := vmhost.AtomicWrite(poolBackupPageFile(s, m.SHA256, index), page, 0600); err != nil {
				return nil, err
			}
		}
	}
	if err := syncPoolBackupDirectory(parent); err != nil {
		return nil, err
	}
	return m, nil
}

func readPoolBackupPage(s *Store, m *protocol.PoolBackupManifest, index int) ([]byte, error) {
	if m == nil || m.Valid() != nil || index < 0 || index >= poolBackupPageCount(m) {
		return nil, errors.New("backup authority page reference is invalid")
	}
	page, err := readBoundedPoolBackupFile(poolBackupPageFile(s, m.SHA256, index))
	if err != nil {
		return nil, err
	}
	if m.Version == 2 {
		if len(page) == 0 || len(page) > protocol.PoolBackupPageLimit {
			return nil, errors.New("backup authority page size is invalid")
		}
	} else if len(page) != m.PageBytes[index] || poolBackupHash(page) != m.PageSHA256[index] {
		return nil, errors.New("backup authority page digest does not match")
	}
	return page, nil
}

func poolBackupPageCount(m *protocol.PoolBackupManifest) int {
	if m.Version == 2 {
		return m.PageCount
	}
	return len(m.PageSHA256)
}

func decodePoolBackupPage(auth *protocol.PoolBackupAuthority, page []byte) error {
	for len(page) > 0 {
		if len(page) < 5 {
			return errors.New("backup authority frame is incomplete")
		}
		kind, size := page[0], int(binary.BigEndian.Uint32(page[1:5]))
		page = page[5:]
		if size > len(page) {
			return errors.New("backup authority frame is incomplete")
		}
		body := page[:size]
		page = page[size:]
		switch kind {
		case 0:
			if size != 0 || len(page) != 0 {
				return errors.New("backup authority empty marker is invalid")
			}
		case 1:
			var value protocol.BackupRepository
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup repository frame is invalid")
			}
			auth.Repositories = append(auth.Repositories, value)
		case 2:
			var value protocol.BackupPolicy
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup policy frame is invalid")
			}
			auth.Definitions = append(auth.Definitions, value)
		case 3:
			var value protocol.BackupRun
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup run frame is invalid")
			}
			auth.History = append(auth.History, value)
		case 4:
			var value protocol.BackupRecoveryPoint
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup catalog frame is invalid")
			}
			auth.Catalog = append(auth.Catalog, value)
		case 5:
			var value protocol.BackupRestorePin
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup pin frame is invalid")
			}
			auth.Pins = append(auth.Pins, value)
		case 6:
			var value protocol.BackupMetadataReceipt
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup receipt frame is invalid")
			}
			auth.MetadataReceipts = append(auth.MetadataReceipts, value)
		default:
			return errors.New("backup authority frame type is invalid")
		}
	}
	return nil
}

func readPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest, digest hash.Hash, auth *protocol.PoolBackupAuthority) error {
	total := 0
	for index := 0; index < poolBackupPageCount(m); index++ {
		page, err := readPoolBackupPage(s, m, index)
		if err != nil {
			return err
		}
		_, _ = digest.Write(page)
		total += len(page)
		if auth != nil {
			if err := decodePoolBackupPage(auth, page); err != nil {
				return err
			}
		}
	}
	if total != m.TotalBytes {
		return errors.New("backup authority bundle size does not match")
	}
	return nil
}

func verifyPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest) error {
	if m == nil || m.Valid() != nil {
		return errors.New("backup authority manifest is invalid")
	}
	digest := sha256.New()
	if err := readPoolBackupAuthorityPages(s, m, digest, nil); err != nil {
		return err
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != m.SHA256 {
		return errors.New("backup authority bundle digest does not match")
	}
	return nil
}

func validatePoolBackupAuthorityPagesV2(s *Store, m *protocol.PoolBackupManifest) error {
	if m == nil || m.Version != 2 || m.Valid() != nil {
		return errors.New("backup authority v2 manifest is invalid")
	}
	digest := sha256.New()
	counts := [6]int{}
	seen := [6]map[string]bool{}
	for i := range seen {
		seen[i] = map[string]bool{}
	}
	total := 0
	for index := 0; index < m.PageCount; index++ {
		page, err := readPoolBackupPage(s, m, index)
		if err != nil {
			return err
		}
		total += len(page)
		_, _ = digest.Write(page)
		auth := &protocol.PoolBackupAuthority{}
		if err := decodePoolBackupPage(auth, page); err != nil || auth.Valid() != nil {
			return errors.New("backup authority v2 page has invalid records")
		}
		for _, repo := range auth.Repositories {
			if seen[0][repo.ID] {
				return errors.New("backup authority v2 has duplicate repository")
			}
			seen[0][repo.ID] = true
			counts[0]++
		}
		for _, policy := range auth.Definitions {
			if seen[1][policy.ID] {
				return errors.New("backup authority v2 has duplicate policy")
			}
			seen[1][policy.ID] = true
			counts[1]++
		}
		for _, run := range auth.History {
			if seen[2][run.OperationID] {
				return errors.New("backup authority v2 has duplicate run")
			}
			seen[2][run.OperationID] = true
			counts[2]++
		}
		for _, point := range auth.Catalog {
			if seen[3][point.ID] {
				return errors.New("backup authority v2 has duplicate point")
			}
			seen[3][point.ID] = true
			counts[3]++
		}
		for _, pin := range auth.Pins {
			if seen[4][pin.OperationID] {
				return errors.New("backup authority v2 has duplicate pin")
			}
			seen[4][pin.OperationID] = true
			counts[4]++
		}
		for _, receipt := range auth.MetadataReceipts {
			if seen[5][receipt.OperationID] {
				return errors.New("backup authority v2 has duplicate receipt")
			}
			seen[5][receipt.OperationID] = true
			counts[5]++
		}
	}
	if total != m.TotalBytes || fmt.Sprintf("%x", digest.Sum(nil)) != m.SHA256 ||
		counts != [6]int{m.Repositories, m.Definitions, m.History, m.Catalog, m.Pins, m.MetadataReceipts} {
		return errors.New("backup authority v2 root or counts do not match")
	}
	return nil
}

func loadPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest) (*protocol.PoolBackupAuthority, error) {
	if m == nil || m.Valid() != nil {
		return nil, errors.New("backup authority manifest is invalid")
	}
	auth := &protocol.PoolBackupAuthority{}
	digest := sha256.New()
	if err := readPoolBackupAuthorityPages(s, m, digest, auth); err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != m.SHA256 || auth.Valid() != nil {
		return nil, errors.New("backup authority bundle is invalid")
	}
	if len(auth.Repositories) != m.Repositories || len(auth.Definitions) != m.Definitions ||
		len(auth.History) != m.History || len(auth.Catalog) != m.Catalog || len(auth.Pins) != m.Pins ||
		len(auth.MetadataReceipts) != m.MetadataReceipts {
		return nil, errors.New("backup authority bundle counts do not match")
	}
	return auth, nil
}
