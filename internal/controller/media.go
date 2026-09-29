// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Staging is bounded by the free space it can actually claim, independently of
// Content-Length, keeping this reserve free for the rest of the Controller.
// Successful host imports release the staged bytes.
const mediaStagingReserve int64 = 256 << 20

func (c *Controller) initMedia() error {
	dir := filepath.Join(c.cfg.DataDir, "media")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("media staging must be a private real directory")
	}
	c.cleanMedia()
	return nil
}
func (c *Controller) cleanMedia() {
	if !c.mediaMu.TryLock() {
		return
	}
	defer c.mediaMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(c.cfg.DataDir, "media"))
	if err != nil {
		return
	}
	for _, entry := range entries {
		var status string
		err := c.db.QueryRow("SELECT o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='iso-import'", entry.Name()).Scan(&status)
		if err == sql.ErrNoRows || err == nil && (status == "succeeded" || status == "failed" || status == "needs_attention") {
			os.Remove(filepath.Join(c.cfg.DataDir, "media", entry.Name()))
		}
	}
}

type mediaRequest struct {
	RequestID string `json:"request_id"`
	StorageID string `json:"storage_id"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

// Media uses Core's current ISO capability, independently of VM-disk capability.
func (c *Controller) storageReadyForMedia(tx *sql.Tx, id, pool, host string) error {
	cmd, err := c.readyStorageForHost(tx, id, pool, host)
	if err != nil {
		return err
	}
	if cmd.Storage == nil {
		return fmt.Errorf("storage repository is unavailable")
	}
	s := cmd.Storage
	if s.ZFSProvisioned {
		if s.Tombstone || !s.Capabilities.ISO {
			return fmt.Errorf("storage repository is not writable for ISO media")
		}
		return nil
	}
	if s.Kind != "" && (s.Tombstone || !s.Access.Readable || !s.Access.Writable || !s.Capabilities.ISO) {
		return fmt.Errorf("storage repository is not writable for ISO media")
	}
	return nil
}

func (c *Controller) media(w http.ResponseWriter, r *http.Request, user, csrf string) {
	if r.Method == "GET" {
		c.listResources(w, "iso-import")
		return
	}
	v := mediaRequest{RequestID: r.URL.Query().Get("request_id"), StorageID: r.URL.Query().Get("storage_id"), Name: r.URL.Query().Get("name")}
	if !nameValid(v.RequestID) || !nameValid(v.StorageID) || !nameValid(v.Name) || filepath.Base(v.Name) != v.Name || !strings.HasSuffix(strings.ToLower(v.Name), ".iso") {
		fail(w, 400, "storage_id, stable request_id and .iso filename required")
		return
	}
	if !c.mediaMu.TryLock() {
		fail(w, 409, "another media transfer is active; retry shortly")
		return
	}
	defer c.mediaMu.Unlock()
	var pool, host, status string
	err := c.db.QueryRow("SELECT r.pool_id,r.host_id,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='storage-register'", v.StorageID).Scan(&pool, &host, &status)
	if err != nil || status != "succeeded" {
		fail(w, 409, "selected storage must be verified")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	err = c.managedHost(tx, pool, host)
	if err == nil {
		err = c.storageReadyForMedia(tx, v.StorageID, pool, host)
	}
	tx.Rollback()
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	dir := filepath.Join(c.cfg.DataDir, "media")
	entries, err := os.ReadDir(dir)
	if err != nil {
		fail(w, 500, "media staging unavailable")
		return
	}
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			fail(w, 500, "invalid staging entry")
			return
		}
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		fail(w, 500, "cannot check staging capacity")
		return
	}
	// Free space already excludes bytes held by existing staged files.
	budget := int64(fs.Bavail)*int64(fs.Bsize) - mediaStagingReserve
	if budget < 32774 || r.ContentLength > budget {
		fail(w, 507, "insufficient media staging space (256 MiB reserved for the Controller)")
		return
	}
	rc := http.NewResponseController(w)
	rc.SetReadDeadline(time.Now().Add(2 * time.Hour))
	rc.SetWriteDeadline(time.Now().Add(2 * time.Hour))
	f, err := os.CreateTemp(dir, "upload-")
	if err != nil {
		fail(w, 500, "media staging unavailable")
		return
	}
	path := f.Name()
	defer func() { f.Close(); os.Remove(path) }()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(r.Body, budget+1))
	if err != nil {
		fail(w, 400, "ISO upload interrupted; retry the same request_id")
		return
	}
	if n > budget {
		fail(w, 413, "ISO exceeds available media staging capacity")
		return
	}
	magic := make([]byte, 5)
	if n < 32774 {
		fail(w, 400, "invalid ISO image")
		return
	}
	if _, err = f.ReadAt(magic, 32769); err != nil || string(magic) != "CD001" {
		fail(w, 400, "ISO9660 image signature required")
		return
	}
	if err = f.Sync(); err != nil {
		fail(w, 500, "ISO staging sync failed")
		return
	}
	v.SizeBytes = n
	v.SHA256 = hex.EncodeToString(hash.Sum(nil))
	tx, err = c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "iso-import", v) {
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err = c.storageReadyForMedia(tx, v.StorageID, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	id, opID := random(), random()
	m := protocol.Media{ID: id, PoolID: pool, HostID: host, StorageID: v.StorageID, Name: v.Name, SizeBytes: n, SHA256: v.SHA256, OperationID: opID, Status: "queued"}
	cmd := protocol.Command{OperationID: opID, Kind: "iso-import", Media: &m}
	target := filepath.Join(dir, id)
	if err = os.Rename(path, target); err != nil {
		fail(w, 500, "ISO publish failed")
		return
	}
	committed := false
	defer func() {
		if !committed {
			os.Remove(target)
		}
	}()
	d, e := os.Open(dir)
	if e != nil {
		fail(w, 500, "ISO directory unavailable")
		return
	}
	e = d.Sync()
	d.Close()
	if e != nil {
		fail(w, 500, "ISO directory sync failed")
		return
	}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, v.Name, id, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "ISO operation storage failed")
		return
	}
	committed = true
	write(w, 201, resourceResponse(cmd, op))
}

// deleteMedia clears an import that never became pool content, or relays the removal of an
// imported one. A failed or abandoned import published nothing on the host, so the only thing
// left is this Controller's own request row; the host's journal outcome and the task and
// audit history stay untouched, and the call takes no body. An imported ISO belongs to its
// host: the file and the durable record live in that Core's journal, so removal is a relayed
// media-remove command taking a stable request_id, and the mirror row disappears only when
// the host reports the tombstone. Should a host report the ISO as applied after all,
// adoption recreates the entry from Core's record, which remains the only source of truth.
func (c *Controller) deleteMedia(w http.ResponseWriter, r *http.Request, user, csrf string) {
	id := r.PathValue("id")
	if !identifierValid(id) {
		fail(w, 404, "media not found")
		return
	}
	// Staging is shared with an active upload, which may be writing this entry right now.
	if !c.mediaMu.TryLock() {
		fail(w, 409, "another media transfer is active; retry shortly")
		return
	}
	defer c.mediaMu.Unlock()
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	var status, pool, host, raw string
	if tx.QueryRow("SELECT o.status,r.pool_id,r.host_id,r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='iso-import'", id).Scan(&status, &pool, &host, &raw) != nil {
		fail(w, 404, "media not found")
		return
	}
	switch status {
	case "failed", "needs_attention":
	case "succeeded":
		c.removeMedia(w, r, tx, user, id, pool, host, raw)
		return
	default:
		fail(w, 409, "this import is still running; wait for its task to finish")
		return
	}
	if _, err = tx.Exec("DELETE FROM managed_resources WHERE id=? AND kind='iso-import'", id); err != nil {
		fail(w, 500, "media removal failed")
		return
	}
	if c.audit(tx, user, "media.discard", id) != nil || tx.Commit() != nil {
		fail(w, 500, "media removal failed")
		return
	}
	// Controller-owned scratch space only: staged upload bytes of an import that failed.
	os.Remove(filepath.Join(c.cfg.DataDir, "media", id))
	write(w, 200, map[string]any{"id": id, "deleted": true})
}

// removeMedia relays an imported ISO's removal to the Core that stores it. Core unlinks the
// published file, journals the tombstone and propagates it; the Controller only asks, and its
// mirror row is dropped by the host's next report, not here.
func (c *Controller) removeMedia(w http.ResponseWriter, r *http.Request, tx *sql.Tx, user, id, pool, host, raw string) {
	var body struct {
		RequestID string `json:"request_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(body.RequestID) {
		fail(w, 400, "stable request_id required to remove an imported ISO")
		return
	}
	if c.replayResource(w, tx, body.RequestID, "media-remove", body) {
		return
	}
	if err := c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var registered protocol.Command
	if json.Unmarshal([]byte(raw), &registered) != nil || registered.Media == nil {
		fail(w, 404, "media not found")
		return
	}
	v := protocol.ResourceLifecycleRequest{Resource: "media", Action: "remove", ResourceID: id, PoolID: pool, HostID: host, ExpectedGeneration: protocol.MediaGeneration(*registered.Media)}
	if err := protocol.ValidateResourceLifecycle(v); err != nil {
		fail(w, 400, err.Error())
		return
	}
	opID := random()
	cmd := protocol.Command{OperationID: opID, Kind: "media-remove", ResourceLifecycle: &v}
	op, err := c.saveResource(tx, user, body.RequestID, pool, host, registered.Media.Name, "media-operation-"+opID, cmd, body)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "ISO removal request storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

func (c *Controller) agentMedia(w http.ResponseWriter, r *http.Request, host string) {
	id := r.PathValue("id")
	if len(id) != 64 {
		fail(w, 404, "media not found")
		return
	}
	if _, err := hex.DecodeString(id); err != nil {
		fail(w, 404, "media not found")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if !transactionIdentity(tx, r, host) {
		fail(w, 401, "host credential no longer valid")
		return
	}
	var status string
	if tx.QueryRow("SELECT o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.host_id=? AND r.kind='iso-import'", id, host).Scan(&status) != nil {
		fail(w, 404, "media not found")
		return
	}
	if status != "dispatched" {
		fail(w, 409, "media operation is not dispatched")
		return
	}
	tx.Rollback()
	f, err := os.Open(filepath.Join(c.cfg.DataDir, "media", id))
	if err != nil {
		fail(w, 410, "staged media unavailable; inspect import operation")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fail(w, 500, "invalid staged media")
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Now().Add(2 * time.Hour))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, id, info.ModTime(), f)
}

// URL transfers run on Core; Controller only queues the durable import command.
type mediaDownloadRequest struct {
	RequestID string `json:"request_id"`
	StorageID string `json:"storage_id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
}

func (c *Controller) downloadMedia(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v mediaDownloadRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.RequestID) || !nameValid(v.StorageID) || !nameValid(v.Name) || filepath.Base(v.Name) != v.Name || !strings.HasSuffix(strings.ToLower(v.Name), ".iso") {
		fail(w, 400, "storage_id, stable request_id and .iso filename required")
		return
	}
	if err := protocol.ValidateISOURL(v.URL); err != nil {
		fail(w, 400, err.Error())
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "iso-import", v) {
		return
	}
	var pool, host, status string
	if tx.QueryRow("SELECT r.pool_id,r.host_id,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind='storage-register'", v.StorageID).Scan(&pool, &host, &status) != nil || status != "succeeded" {
		fail(w, 409, "selected storage must be verified")
		return
	}
	if err = c.managedHost(tx, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err = c.storageReadyForMedia(tx, v.StorageID, pool, host); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var raw string
	var caps []string
	if tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &caps) != nil || !slices.Contains(caps, protocol.ISODownloadCapability) {
		fail(w, 409, "host must support iso-download-v1; update Nex Core before downloading an ISO")
		return
	}
	id, opID := random(), random()
	cmd := protocol.Command{OperationID: opID, Kind: "iso-import", Media: &protocol.Media{ID: id, PoolID: pool, HostID: host, StorageID: v.StorageID, Name: v.Name, URL: v.URL, OperationID: opID, Status: "queued"}}
	op, err := c.saveResource(tx, user, v.RequestID, pool, host, v.Name, id, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "ISO operation storage failed")
		return
	}
	write(w, 201, resourceResponse(cmd, op))
}

// Completed download metadata comes from the host's durable managed journal.
func mirrorDownloadedMedia(tx *sql.Tx, host string, md protocol.Media) error {
	var raw string
	err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.host_id=? AND r.kind='iso-import' AND o.status='succeeded'", md.ID, host).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cmd protocol.Command
	if err = json.Unmarshal([]byte(raw), &cmd); err != nil {
		return err
	}
	if cmd.Media == nil || cmd.Media.URL == "" {
		return nil
	}
	if md.StorageID != cmd.Media.StorageID || md.Name != cmd.Media.Name || md.SizeBytes < 32774 || !sha256Hex(md.SHA256) {
		return fmt.Errorf("invalid downloaded ISO metadata")
	}
	cmd.Media.SizeBytes, cmd.Media.SHA256 = md.SizeBytes, md.SHA256
	data, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE managed_resources SET command=? WHERE id=? AND host_id=?", string(data), md.ID, host)
	return err
}
