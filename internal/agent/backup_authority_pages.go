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
	// The authority is never loaded whole: the edit lists only the records it
	// writes, and it grows the stored authority only by naming one the store
	// neither holds nor reserved.
	auth := &protocol.PoolBackupAuthority{}
	edit(auth)
	written, err := checkPoolBackupTerminalRoom(auth)
	if err != nil {
		return err
	}
	source, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil {
		return err
	}
	if source == nil {
		source = &protocol.PoolBackupManifest{}
	}
	// A small authority is replicated in full with the next push. The adopting
	// master imports its catalog before it re-points members, so none of them can
	// confirm its authority yet.
	if !poolBackupAuthorityLarge(source, written) || !poolBackupAuthorityEditAdds(s, source, auth) ||
		peer.Transfer != nil && peer.Transfer.Stage == "catalog-rebuild" {
		return nil
	}
	return requirePoolBackupMembersCurrent(s, peer.Membership)
}

// checkPoolBackupTerminalRoom refuses records that could not be replicated, and
// pending records whose largest terminal outcome would not fit a page: a run or
// receipt that cannot be settled would stay unresolved forever.
func checkPoolBackupTerminalRoom(auth *protocol.PoolBackupAuthority) (*protocol.PoolBackupManifest, error) {
	written, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		return nil, fmt.Errorf("backup authority cannot be replicated: %w", err)
	}
	for _, run := range auth.History {
		if run.Status == "pending" || run.Status == "needs_attention" {
			run.Status, run.Stage, run.Error = "needs_attention", "authority_transfer", strings.Repeat("\x00", 4096)
			if maximum, _ := json.Marshal(run); len(maximum)+5 > protocol.PoolBackupPageLimit {
				return nil, errors.New("backup run has no room for its terminal outcome")
			}
		}
	}
	for _, receipt := range auth.MetadataReceipts {
		if receipt.Status == "pending" || receipt.Status == "needs_attention" {
			receipt.Status, receipt.Error = "needs_attention", strings.Repeat("\x00", 4096)
			if maximum, _ := json.Marshal(receipt); len(maximum)+5 > protocol.PoolBackupPageLimit {
				return nil, errors.New("backup metadata receipt has no room for its terminal outcome")
			}
		}
	}
	return written, nil
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
type poolBackupEncoder struct {
	m     protocol.PoolBackupManifest
	page  bytes.Buffer
	whole hash.Hash
	emit  func([]byte) error
}

func (e *poolBackupEncoder) flush() error {
	if e.page.Len() == 0 {
		return nil
	}
	data := e.page.Bytes()
	e.m.PageCount++
	e.m.TotalBytes += len(data)
	_, _ = e.whole.Write(data)
	if e.emit != nil {
		if err := e.emit(data); err != nil {
			return err
		}
	}
	e.page.Reset()
	return nil
}

func (e *poolBackupEncoder) add(kind byte, record any) error {
	body, err := json.Marshal(record)
	if err != nil {
		return err
	}
	if len(body)+5 > protocol.PoolBackupPageLimit {
		return errors.New("backup authority record exceeds page limit")
	}
	if e.page.Len()+len(body)+5 > protocol.PoolBackupPageLimit {
		if err := e.flush(); err != nil {
			return err
		}
	}
	e.page.WriteByte(kind)
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(body)))
	_, _ = e.page.Write(size[:])
	_, _ = e.page.Write(body)
	switch kind {
	case 1:
		e.m.Repositories++
	case 2:
		e.m.Definitions++
	case 3:
		e.m.History++
	case 4:
		e.m.Catalog++
	case 5:
		e.m.Pins++
	case 6:
		e.m.MetadataReceipts++
	case 7:
		e.m.OrphanCleanups++
	default:
		return errors.New("backup authority record kind is invalid")
	}
	// Records content decides where a page ends, so one changed or inserted
	// record leaves the other pages, and their reusable blobs, unchanged.
	if sha256.Sum256(body)[0] == 0 {
		return e.flush()
	}
	return nil
}

func (e *poolBackupEncoder) finish() (*protocol.PoolBackupManifest, error) {
	if err := e.flush(); err != nil {
		return nil, err
	}
	e.m.SHA256 = fmt.Sprintf("%x", e.whole.Sum(nil))
	return &e.m, e.m.Valid()
}

func addPoolBackupRecords[T any](e *poolBackupEncoder, kind byte, records []T) error {
	for _, record := range records {
		if err := e.add(kind, record); err != nil {
			return err
		}
	}
	return nil
}

// encodePoolBackupAuthority frames an authority held in memory; an empty one is
// a single marker page.
func encodePoolBackupAuthority(auth *protocol.PoolBackupAuthority, emit func([]byte) error) (*protocol.PoolBackupManifest, error) {
	if err := auth.Valid(); err != nil {
		return nil, err
	}
	e := &poolBackupEncoder{whole: sha256.New(), emit: emit}
	if err := errors.Join(
		addPoolBackupRecords(e, 1, auth.Repositories), addPoolBackupRecords(e, 2, auth.Definitions),
		addPoolBackupRecords(e, 3, auth.History), addPoolBackupRecords(e, 4, auth.Catalog),
		addPoolBackupRecords(e, 5, auth.Pins), addPoolBackupRecords(e, 6, auth.MetadataReceipts),
		addPoolBackupRecords(e, 7, auth.OrphanCleanups),
	); err != nil {
		return nil, err
	}
	if e.m.TotalBytes == 0 && e.page.Len() == 0 {
		_, _ = e.page.Write([]byte{0, 0, 0, 0, 0})
	}
	return e.finish()
}

func encodePoolBackupAuthorityFromStore(s *Store, emit func([]byte) error) (*protocol.PoolBackupManifest, error) {
	e := &poolBackupEncoder{whole: sha256.New(), emit: emit}
	nonempty, err := walkPoolBackupAuthorityRecords(s, e.add)
	if err != nil || !nonempty {
		return nil, err
	}
	return e.finish()
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

// An authority is large once the stored records plus an edit pass the bundle
// size or a record count at which growth must first be confirmed by every ready
// member. The edit may repeat stored records, so this overestimates.
func poolBackupAuthorityLarge(stored, edit *protocol.PoolBackupManifest) bool {
	return stored.TotalBytes+edit.TotalBytes > poolBackupBundleLimit || stored.History+edit.History > 100000 ||
		stored.Catalog+edit.Catalog > 100000 || stored.Pins+edit.Pins > 100000 ||
		stored.MetadataReceipts+edit.MetadataReceipts > 100000
}

// stagePoolBackupAuthorityFromStore writes a root's pages only when that root
// is not already staged.
func stagePoolBackupAuthorityFromStore(s *Store) (*protocol.PoolBackupManifest, error) {
	m, err := encodePoolBackupAuthorityFromStore(s, nil)
	if err != nil || m == nil || verifyPoolBackupAuthorityPages(s, m) == nil {
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
	staged, err := stagePoolBackupAuthorityFromStore(s)
	if err == nil && (staged == nil || staged.SHA256 != m.SHA256) {
		err = errors.New("backup authority changed while its pages were staged")
	}
	return err
}

func readPoolBackupPage(s *Store, m *protocol.PoolBackupManifest, index int) ([]byte, error) {
	if m == nil || m.Valid() != nil || index < 0 || index >= m.PageCount {
		return nil, errors.New("backup authority page reference is invalid")
	}
	page, err := readBoundedPoolBackupFile(poolBackupPageFile(s, m.SHA256, index))
	if err != nil {
		return nil, err
	}
	if len(page) == 0 || len(page) > protocol.PoolBackupPageLimit {
		return nil, errors.New("backup authority page size is invalid")
	}
	return page, nil
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
		case 7:
			var value protocol.BackupOrphanCleanup
			if json.Unmarshal(body, &value) != nil {
				return errors.New("backup orphan cleanup frame is invalid")
			}
			auth.OrphanCleanups = append(auth.OrphanCleanups, value)
		default:
			return errors.New("backup authority frame type is invalid")
		}
	}
	return nil
}

func verifyPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest) error {
	if m == nil || m.Valid() != nil {
		return errors.New("backup authority manifest is invalid")
	}
	digest := sha256.New()
	total := 0
	for index := 0; index < m.PageCount; index++ {
		page, err := readPoolBackupPage(s, m, index)
		if err != nil {
			return err
		}
		_, _ = digest.Write(page)
		total += len(page)
	}
	if total != m.TotalBytes {
		return errors.New("backup authority bundle size does not match")
	}
	if fmt.Sprintf("%x", digest.Sum(nil)) != m.SHA256 {
		return errors.New("backup authority bundle digest does not match")
	}
	return nil
}

// walkPoolBackupAuthorityPages reads a staged root one page at a time, refusing
// invalid or duplicate records, and hands each decoded page to visit. Only after
// the last page do the root digest, size and record counts have to match, so a
// caller must discard whatever visit built when it returns an error.
func walkPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest, visit func(*protocol.PoolBackupAuthority) error) error {
	if m == nil || m.Valid() != nil {
		return errors.New("backup authority manifest is invalid")
	}
	digest := sha256.New()
	total := 0
	var counts [7]int
	seen := map[string]bool{}
	note := func(kind int, id string) error {
		key := fmt.Sprint(kind, id)
		if seen[key] {
			return errors.New("backup authority has a duplicate record")
		}
		seen[key] = true
		counts[kind]++
		return nil
	}
	for index := 0; index < m.PageCount; index++ {
		page, err := readPoolBackupPage(s, m, index)
		if err != nil {
			return err
		}
		total += len(page)
		_, _ = digest.Write(page)
		auth := &protocol.PoolBackupAuthority{}
		if err := decodePoolBackupPage(auth, page); err != nil || auth.Valid() != nil {
			return errors.New("backup authority page has invalid records")
		}
		for _, repo := range auth.Repositories {
			if err := note(0, repo.ID); err != nil {
				return err
			}
		}
		for _, policy := range auth.Definitions {
			if err := note(1, policy.ID); err != nil {
				return err
			}
		}
		for _, run := range auth.History {
			if err := note(2, run.OperationID); err != nil {
				return err
			}
		}
		for _, point := range auth.Catalog {
			if err := note(3, point.ID); err != nil {
				return err
			}
		}
		for _, pin := range auth.Pins {
			if err := note(4, pin.OperationID); err != nil {
				return err
			}
		}
		for _, receipt := range auth.MetadataReceipts {
			if err := note(5, receipt.OperationID); err != nil {
				return err
			}
		}
		for _, cleanup := range auth.OrphanCleanups {
			if err := note(6, cleanup.OperationID); err != nil {
				return err
			}
		}
		if visit != nil {
			if err := visit(auth); err != nil {
				return err
			}
		}
	}
	if total != m.TotalBytes || fmt.Sprintf("%x", digest.Sum(nil)) != m.SHA256 ||
		counts != [7]int{m.Repositories, m.Definitions, m.History, m.Catalog, m.Pins, m.MetadataReceipts, m.OrphanCleanups} {
		return errors.New("backup authority root or counts do not match")
	}
	return nil
}
