// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

type applianceRequest struct {
	RequestID string `json:"request_id"`
	PoolID    string `json:"pool_id,omitempty"`
	HostID    string `json:"host_id"`
	VMID      string `json:"vm_id"`
	Name      string `json:"name"`
}

type managedAppliance struct {
	ID          string                 `json:"id"`
	PoolID      string                 `json:"pool_id"`
	HostID      string                 `json:"host_id"`
	Name        string                 `json:"name"`
	Role        string                 `json:"role"`
	Protected   bool                   `json:"protected"`
	State       string                 `json:"state"`
	Freshness   string                 `json:"freshness"`
	Spec        *protocol.VMCreateSpec `json:"spec,omitempty"`
	OperationID string                 `json:"operation_id,omitempty"`
	Status      string                 `json:"status,omitempty"`
	Error       string                 `json:"error,omitempty"`
	// Guest is the host's latest guest-agent and console-channel observation of a
	// running VM; absent when the VM is not running or the host predates it.
	Guest *protocol.GuestInfo `json:"guest,omitempty"`
}

func applianceUUID(s string) bool {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s != strings.ToLower(s) {
		return false
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 {
		return false
	}
	for _, v := range b {
		if v != 0 {
			return true
		}
	}
	return false
}

// A persisted VM state is history, never proof of its current power state.
func (c *Controller) applianceObservation(host, id string, revoked bool, raw sql.NullString) (string, string) {
	state, fresh, _ := c.observeVM(host, id, revoked, raw)
	return state, fresh
}

// observeVM is applianceObservation with the guest observation of a fresh running VM.
func (c *Controller) observeVM(host, id string, revoked bool, raw sql.NullString) (string, string, *protocol.GuestInfo) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revoked {
		return "unknown", "revoked", nil
	}
	fresh := c.observationFreshness(host + "/libvirt")
	if seen, ok := c.seen[host]; !ok {
		fresh = "unknown"
	} else if time.Since(seen) > c.cfg.StaleAfter {
		fresh = "stale"
	}
	if fresh != "fresh" {
		return "unknown", fresh, nil
	}
	var inv protocol.Inventory
	if !raw.Valid || json.Unmarshal([]byte(raw.String), &inv) != nil {
		return "unknown", "unknown", nil
	}
	healthy := false
	for _, check := range inv.Checks {
		if check.Name == "libvirt" && check.Status == "healthy" {
			healthy = true
		}
	}
	if !healthy {
		return "unknown", "unknown", nil
	}
	for _, vm := range inv.VMs {
		if strings.EqualFold(vm.ID, id) {
			return vm.State, "fresh", vm.Guest
		}
	}
	return "unknown", "fresh", nil
}

func (c *Controller) adoptAppliance(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v applianceRequest
	if !decode(w, r, &v) {
		return
	}
	if !applianceUUID(v.VMID) || !nameValid(v.RequestID) || !nameValid(v.Name) || !nameValid(v.HostID) || (v.PoolID != "" && !nameValid(v.PoolID)) {
		fail(w, 400, "valid lowercase VM UUID, name, host_id, optional pool_id and stable request_id required")
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	// The pool is the host's own; a supplied pool_id only confirms it.
	var pool string
	var revoked bool
	var inv sql.NullString
	if err = tx.QueryRow("SELECT pool_id,revoked,inventory FROM hosts WHERE id=?", v.HostID).Scan(&pool, &revoked, &inv); err == sql.ErrNoRows {
		fail(w, 404, "host not found")
		return
	} else if err != nil {
		fail(w, 500, "host lookup failed")
		return
	}
	if v.PoolID != "" && v.PoolID != pool {
		fail(w, 409, "pool_id does not match the host's pool")
		return
	}
	v.PoolID = pool
	var managedCount int
	if tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=?", v.VMID).Scan(&managedCount) != nil {
		fail(w, 500, "VM ownership lookup failed")
		return
	}
	if managedCount > 0 {
		fail(w, 409, "VM already belongs to a managed creation operation")
		return
	}
	var old applianceRequest
	err = tx.QueryRow("SELECT id,pool_id,host_id,name,request_id FROM appliances WHERE request_id=? OR id=?", v.RequestID, v.VMID).Scan(&old.VMID, &old.PoolID, &old.HostID, &old.Name, &old.RequestID)
	if err == nil {
		if old != v {
			fail(w, 409, "VM or request_id already has another adoption owner or payload")
			return
		}
		write(w, 200, map[string]string{"id": v.VMID, "pool_id": v.PoolID, "host_id": v.HostID})
		return
	}
	if err != sql.ErrNoRows {
		fail(w, 500, "adoption lookup failed")
		return
	}
	if revoked {
		fail(w, 409, "host must be active in its pool")
		return
	}
	state, fresh := c.applianceObservation(v.HostID, v.VMID, revoked, inv)
	if fresh != "fresh" || state == "unknown" || state == "" {
		fail(w, 409, "fresh successful host inventory must observe the VM before adoption")
		return
	}
	// Refuse a UUID observed on another host; registration is not a fencing decision.
	rows, err := tx.Query("SELECT inventory FROM hosts WHERE id<>?", v.HostID)
	if err != nil {
		fail(w, 500, "ownership lookup failed")
		return
	}
	conflict := false
	for rows.Next() {
		var raw sql.NullString
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if !raw.Valid {
			continue
		}
		var other protocol.Inventory
		if err = json.Unmarshal([]byte(raw.String), &other); err != nil {
			break
		}
		for _, vm := range other.VMs {
			if strings.EqualFold(vm.ID, v.VMID) {
				conflict = true
			}
		}
	}
	rowErr := rows.Err()
	rows.Close()
	if err != nil || rowErr != nil {
		fail(w, 500, "ownership inventory invalid")
		return
	}
	if conflict {
		fail(w, 409, "VM UUID is also reported by another host; reconcile ownership first")
		return
	}
	_, err = tx.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", v.VMID, v.PoolID, v.HostID, v.Name, v.RequestID)
	if err != nil || c.audit(tx, user, "appliance.adopt", v.VMID) != nil || tx.Commit() != nil {
		fail(w, 500, "adoption storage failed")
		return
	}
	write(w, 201, map[string]string{"id": v.VMID, "pool_id": v.PoolID, "host_id": v.HostID})
}

func (c *Controller) appliances(w http.ResponseWriter, r *http.Request, user, csrf string) {
	rows, err := c.db.Query("SELECT a.id,a.pool_id,a.host_id,a.name,h.revoked,h.inventory FROM appliances a JOIN hosts h ON h.id=a.host_id ORDER BY a.name,a.id")
	if err != nil {
		fail(w, 500, "VM lookup failed")
		return
	}
	defer rows.Close()
	out := []managedAppliance{}
	for rows.Next() {
		v := managedAppliance{Role: "controller", Protected: true}
		var revoked bool
		var raw sql.NullString
		if rows.Scan(&v.ID, &v.PoolID, &v.HostID, &v.Name, &revoked, &raw) != nil {
			fail(w, 500, "VM decode failed")
			return
		}
		v.State, v.Freshness, v.Guest = c.observeVM(v.HostID, v.ID, revoked, raw)
		out = append(out, v)
	}
	if rows.Err() != nil {
		fail(w, 500, "VM read failed")
		return
	}
	rows.Close()
	managed, err := c.db.Query("SELECT r.command,o.status,o.error,h.revoked,h.inventory FROM managed_resources r JOIN operations o ON o.id=r.operation_id JOIN hosts h ON h.id=r.host_id WHERE r.kind='vm-create' ORDER BY r.name,r.id")
	if err != nil {
		fail(w, 500, "VM lookup failed")
		return
	}
	defer managed.Close()
	for managed.Next() {
		var command string
		var v managedAppliance
		var raw sql.NullString
		var revoked bool
		if managed.Scan(&command, &v.Status, &v.Error, &revoked, &raw) != nil {
			fail(w, 500, "VM decode failed")
			return
		}
		var cmd protocol.Command
		if json.Unmarshal([]byte(command), &cmd) != nil || cmd.VM == nil {
			fail(w, 500, "VM decode failed")
			return
		}
		v.ID = cmd.VM.ID
		v.PoolID = cmd.VM.PoolID
		v.HostID = cmd.VM.HostID
		v.Name = cmd.VM.Name
		v.Role = "guest"
		// Interfaces are reported with their effective addresses so an edit can name them.
		spec := *cmd.VM
		spec.NICs = append([]protocol.VMNIC{}, spec.NICs...)
		for i := range spec.NICs {
			spec.NICs[i].MAC = protocol.NICMAC(spec.ID, i, spec.NICs[i])
		}
		v.Spec = &spec
		v.OperationID = cmd.OperationID
		v.State, v.Freshness, v.Guest = c.observeVM(v.HostID, v.ID, revoked, raw)
		out = append(out, v)
	}
	if managed.Err() != nil {
		fail(w, 500, "VM read failed")
		return
	}
	write(w, 200, out)
}

// The caller retains the random enrollment token before making this request.
// Only its digest is persisted here, preserving enrollment's existing secret handling.
func (c *Controller) bootstrapAppliance(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v struct {
		RequestID       string `json:"request_id"`
		SiteName        string `json:"site_name"`
		EnrollmentToken string `json:"enrollment_token"`
	}
	if !decode(w, r, &v) {
		return
	}
	tokenBytes, tokenErr := hex.DecodeString(v.EnrollmentToken)
	if !nameValid(v.RequestID) || !nameValid(v.SiteName) || tokenErr != nil || len(tokenBytes) != 32 || v.EnrollmentToken != strings.ToLower(v.EnrollmentToken) {
		fail(w, 400, "site_name, stable request_id and random 64 lowercase hexadecimal enrollment_token required")
		return
	}
	v.SiteName = strings.TrimSpace(v.SiteName)
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	hash := digest(v.EnrollmentToken)
	var siteName, oldHash, siteID string
	err = tx.QueryRow("SELECT site_name,token_hash,site_id FROM appliance_bootstraps WHERE request_id=?", v.RequestID).Scan(&siteName, &oldHash, &siteID)
	status := 200
	if err == nil {
		if siteName != v.SiteName || oldHash != hash {
			fail(w, 409, "request_id already used with another bootstrap payload")
			return
		}
	} else if err == sql.ErrNoRows {
		var count int
		if tx.QueryRow("SELECT count(*) FROM tokens WHERE hash=?", hash).Scan(&count) != nil {
			fail(w, 500, "token lookup failed")
			return
		}
		if count != 0 {
			fail(w, 409, "enrollment token already exists")
			return
		}
		siteID = random()
		// The appliance host becomes the master of a pool created at enrollment.
		if _, err = tx.Exec("INSERT INTO sites VALUES(?,?)", siteID, v.SiteName); err == nil {
			_, err = tx.Exec("INSERT INTO tokens(hash,site_id,host_id,expires) VALUES(?,?,?,?)", hash, siteID, "", time.Now().Add(10*time.Minute).Unix())
		}
		if err == nil {
			_, err = tx.Exec("INSERT INTO appliance_bootstraps VALUES(?,?,?,?)", v.RequestID, v.SiteName, hash, siteID)
		}
		if err == nil {
			err = c.audit(tx, user, "appliance.bootstrap", siteID)
		}
		if err != nil {
			fail(w, 500, "bootstrap storage failed")
			return
		}
		status = 201
	} else {
		fail(w, 500, "bootstrap lookup failed")
		return
	}
	var expires int64
	// Renew expiry without clearing a consumed token's CSR-bound response.
	if _, err = tx.Exec("UPDATE tokens SET expires=? WHERE hash=?", time.Now().Add(10*time.Minute).Unix(), hash); err != nil {
		fail(w, 500, "token renewal failed")
		return
	}
	if err = tx.QueryRow("SELECT expires FROM tokens WHERE hash=?", hash).Scan(&expires); err == sql.ErrNoRows {
		fail(w, 409, "bootstrap enrollment was revoked; enroll explicitly through recovery")
		return
	} else if err != nil {
		fail(w, 500, "token lookup failed")
		return
	}
	if tx.Commit() != nil {
		fail(w, 500, "bootstrap commit failed")
		return
	}
	write(w, status, map[string]any{"site_id": siteID, "expires_at": time.Unix(expires, 0).UTC()})
}
