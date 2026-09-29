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
	Replica     protocol.PoolAuthorityReplica `json:"replica"`
	Index       int                           `json:"index"`
	Data        []byte                        `json:"data"`
	ReuseSHA256 string                        `json:"reuse_sha256,omitempty"`
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
			if err := vmhost.AtomicWrite(path, page, 0600); err != nil {
				return "", err
			}
			return hash, syncPoolBackupDirectory(filepath.Dir(path))
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

// Keep the acknowledged root and one predecessor for interrupted peer retries.
// A nonterminal transfer can still need an older signed replica, so it defers GC.
func gcPoolBackupPages(s *Store, keep string) error {
	transfer, err := loadPoolTransfer(s)
	if err != nil || transfer != nil && transfer.Status != "succeeded" {
		return err
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
	var previous string
	var previousTime time.Time
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep || strings.HasPrefix(entry.Name(), ".stage-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if keep != "" && (previous == "" || info.ModTime().After(previousTime)) {
			previous, previousTime = entry.Name(), info.ModTime()
		}
	}
	for _, entry := range entries {
		if !entry.IsDir() || entry.Name() == keep || entry.Name() == previous || strings.HasPrefix(entry.Name(), ".stage-") {
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
// accept backup state that its members cannot hold in a signed replica.
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
	needsV2 := source != nil && (source.TotalBytes > poolBackupBundleLimit ||
		source.History > 100000 || source.Catalog > 100000 || source.Pins > 100000 || source.MetadataReceipts > 100000)
	var auth *protocol.PoolBackupAuthority
	if !needsV2 {
		auth, err = PoolBackupAuthorityRefs(s)
		if err != nil {
			return err
		}
	}
	if auth == nil {
		auth = &protocol.PoolBackupAuthority{}
	}
	edit(auth)
	manifest, err := encodePoolBackupAuthority(auth, nil)
	if err != nil {
		return fmt.Errorf("backup authority cannot be replicated: %w", err)
	}
	remaining := poolBackupBundleLimit - manifest.TotalBytes
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
			return errors.New("backup run has no room for its terminal outcome")
		}
		remaining -= protocol.PoolBackupPageLimit - len(body) - 5
		if (run.Kind == protocol.BackupKindBackupNow || run.Kind == protocol.BackupKindScheduled || run.Kind == protocol.BackupKindPolicyRunNow) &&
			run.RecoveryPointID != "" && !cataloged[run.RecoveryPointID] {
			remaining -= protocol.PoolBackupPageLimit
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
			return errors.New("backup metadata receipt has no room for its terminal outcome")
		}
		if growth := len(maximum) - len(current) + 10; growth > 0 {
			remaining -= growth
		}
	}
	if needsV2 || manifest.Version == 2 || remaining < 0 {
		return requirePoolBackupV2Members(s, peer.Membership)
	}
	return nil
}

func requirePoolBackupV2Members(s *Store, membership protocol.PoolMembership) error {
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
		poolReplicaMu.Lock()
		status := poolReplicaObservations[s.dir][member.HostID]
		poolReplicaMu.Unlock()
		if status.BackupPageVersion < 2 && !status.ReceivedAt.IsZero() && time.Since(status.ReceivedAt) <= time.Minute {
			return errors.New("backup authority growth requires a current v2 replica-capable pool member")
		}
		status, err = refreshPoolBackupV2MemberStatus(s, member)
		if err != nil {
			log.Printf("backup authority growth refused: authenticated member replica status unavailable")
			return errors.New("backup authority growth requires a current v2 replica-capable pool member")
		}
		if status.BackupPageVersion < 2 || status.Error != "" ||
			!status.Verified || status.Generation != generation || status.Digest != digest {
			log.Printf("backup authority growth refused: member replica version=%d verified=%t error=%t generation_match=%t digest_match=%t",
				status.BackupPageVersion, status.Verified, status.Error != "", status.Generation == generation, status.Digest == digest)
			return errors.New("backup authority growth requires a current v2 replica-capable pool member")
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

func refreshPoolBackupV2MemberStatus(s *Store, member protocol.PoolMember) (protocol.PoolReplicaStatus, error) {
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
		if page.Len() > 0 {
			if err := flush(); err != nil {
				return err
			}
		}
		page.WriteByte(kind)
		var size [4]byte
		binary.BigEndian.PutUint32(size[:], uint32(len(body)))
		_, _ = page.Write(size[:])
		_, _ = page.Write(body)
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

func stagePoolBackupAuthorityFromStore(s *Store) (*protocol.PoolBackupManifest, error) {
	parent := filepath.Join(s.dir, poolBackupPageDir)
	temporary, err := newPoolBackupStage(s)
	if err != nil {
		return nil, err
	}
	defer releasePoolBackupStage(temporary)
	index := 0
	m, err := encodePoolBackupAuthorityFromStore(s, func(page []byte) error {
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
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, nil
	}
	if m.TotalBytes <= poolBackupBundleLimit && m.History <= 100000 && m.Catalog <= 100000 &&
		m.Pins <= 100000 && m.MetadataReceipts <= 100000 {
		// The caller will use the v1 projection. Drop transient blob links too.
		poolBackupBlobMu.Lock()
		removeErr := os.RemoveAll(temporary)
		if removeErr == nil {
			removeErr = gcPoolBackupOrphanBlobs(s)
		}
		poolBackupBlobMu.Unlock()
		return m, removeErr
	}
	destination := filepath.Join(parent, m.SHA256)
	if err := verifyPoolBackupAuthorityPages(s, m); err == nil {
		return m, nil
	}
	if err := os.RemoveAll(destination); err != nil {
		return nil, err
	}
	if err := syncPoolBackupDirectory(temporary); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, destination); err != nil {
		return nil, err
	}
	if err := syncPoolBackupDirectory(parent); err != nil {
		return nil, err
	}
	return m, nil
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
