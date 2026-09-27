// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"strings"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolBackupPageDir = "pool-backup-replica-pages"

var poolBackupBundleLimit = protocol.PoolBackupBundleLimit

type poolBackupPageUpload struct {
	Replica protocol.PoolAuthorityReplica `json:"replica"`
	Index   int                           `json:"index"`
	Data    []byte                        `json:"data"`
}

type poolBackupPageProgress struct {
	NextIndex int `json:"next_index"`
}

func poolBackupPageFile(s *Store, digest string, index int) string {
	return filepath.Join(s.dir, poolBackupPageDir, digest, fmt.Sprintf("%04d", index))
}

func poolBackupHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

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
	auth, err := PoolBackupAuthorityRefs(s)
	if err != nil {
		return err
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
		if growth := len(maximum) - len(current) + 10; growth > 0 {
			remaining -= growth
		}
	}
	if remaining < 0 {
		return errors.New("backup authority has no room for retained operation outcomes")
	}
	return nil
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
		if m.TotalBytes+len(data) > poolBackupBundleLimit || len(m.PageBytes) == 1024 {
			return errors.New("backup authority exceeds the bounded transfer limit")
		}
		m.TotalBytes += len(data)
		m.PageBytes = append(m.PageBytes, len(data))
		m.PageSHA256 = append(m.PageSHA256, poolBackupHash(data))
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
	m.SHA256 = fmt.Sprintf("%x", whole.Sum(nil))
	return m, m.Valid()
}

func stagePoolBackupAuthority(s *Store, auth *protocol.PoolBackupAuthority) (*protocol.PoolBackupManifest, error) {
	if auth == nil {
		return nil, nil
	}
	parent := filepath.Join(s.dir, poolBackupPageDir)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	temporary, err := os.MkdirTemp(parent, ".stage-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(temporary)
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
		for index := range m.PageSHA256 {
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
	if m == nil || m.Valid() != nil || index < 0 || index >= len(m.PageSHA256) {
		return nil, errors.New("backup authority page reference is invalid")
	}
	page, err := os.ReadFile(poolBackupPageFile(s, m.SHA256, index))
	if err != nil {
		return nil, err
	}
	if len(page) != m.PageBytes[index] || poolBackupHash(page) != m.PageSHA256[index] {
		return nil, errors.New("backup authority page digest does not match")
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
		default:
			return errors.New("backup authority frame type is invalid")
		}
	}
	return nil
}

func readPoolBackupAuthorityPages(s *Store, m *protocol.PoolBackupManifest, digest hash.Hash, auth *protocol.PoolBackupAuthority) error {
	for index := range m.PageSHA256 {
		page, err := readPoolBackupPage(s, m, index)
		if err != nil {
			return err
		}
		_, _ = digest.Write(page)
		if auth != nil {
			if err := decodePoolBackupPage(auth, page); err != nil {
				return err
			}
		}
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
