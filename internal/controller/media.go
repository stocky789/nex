// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Staging is bounded independently of Content-Length. Successful host imports release it.
const maxStagingBytes int64 = 16 << 30

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
	if r.ContentLength > protocol.MaxISOBytes {
		fail(w, 413, "ISO exceeds 8 GiB limit")
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
	var used int64
	for _, entry := range entries {
		info, e := entry.Info()
		if e != nil || !info.Mode().IsRegular() {
			fail(w, 500, "invalid staging entry")
			return
		}
		used += info.Size()
	}
	budget := protocol.MaxISOBytes
	if remaining := maxStagingBytes - used; remaining < budget {
		budget = remaining
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(dir, &fs) != nil {
		fail(w, 500, "cannot check staging capacity")
		return
	}
	free := int64(fs.Bavail)*int64(fs.Bsize) - (256 << 20)
	if free < budget {
		budget = free
	}
	if budget < 32774 || r.ContentLength > budget {
		fail(w, 507, "insufficient media staging space (16 GiB total; 256 MiB reserved)")
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
		fail(w, 413, "ISO exceeds upload or staging capacity limit")
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
