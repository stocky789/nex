// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"nex.local/nex/internal/protocol"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

func (c *Controller) initManaged() error {
	_, err := c.db.Exec(`CREATE TABLE IF NOT EXISTS host_capabilities(host_id TEXT PRIMARY KEY REFERENCES hosts(id),capabilities TEXT NOT NULL);
 CREATE TABLE IF NOT EXISTS managed_resources(id TEXT PRIMARY KEY,kind TEXT NOT NULL,pool_id TEXT NOT NULL REFERENCES pools(id),host_id TEXT NOT NULL REFERENCES hosts(id),name TEXT NOT NULL,request_id TEXT NOT NULL UNIQUE,payload TEXT NOT NULL,command TEXT NOT NULL,operation_id TEXT NOT NULL UNIQUE REFERENCES operations(id));`)
	if err != nil {
		return err
	}
	return c.initMedia()
}
func hasManagedCapability(caps []string) bool {
	for _, v := range caps {
		if v == protocol.ManagedVMCapability {
			return true
		}
	}
	return false
}
func (c *Controller) managedHost(tx *sql.Tx, pool, host string) error {
	var actual string
	var revoked bool
	var caps string
	if err := tx.QueryRow("SELECT h.pool_id,h.revoked,coalesce(c.capabilities,'[]') FROM hosts h LEFT JOIN host_capabilities c ON c.host_id=h.id WHERE h.id=?", host).Scan(&actual, &revoked, &caps); err != nil {
		return fmt.Errorf("host not found")
	}
	var capabilities []string
	json.Unmarshal([]byte(caps), &capabilities)
	c.mu.Lock()
	seen := c.seen[host]
	c.mu.Unlock()
	if actual != pool || revoked || time.Since(seen) > c.cfg.StaleAfter || !hasManagedCapability(capabilities) {
		return fmt.Errorf("host must be fresh, active, in the selected pool, and support managed-vm-v1")
	}
	return nil
}
func (c *Controller) saveResource(tx *sql.Tx, user, requestID, pool, host, name, id string, cmd protocol.Command, payload any) (protocol.Operation, error) {
	return c.saveResourceStatus(tx, user, requestID, pool, host, name, id, "queued", cmd, payload)
}
func (c *Controller) saveResourceStatus(tx *sql.Tx, user, requestID, pool, host, name, id, status string, cmd protocol.Command, payload any) (protocol.Operation, error) {
	now := time.Now().UTC()
	op := protocol.Operation{ID: cmd.OperationID, HostID: host, Kind: cmd.Kind, RequestID: requestID, Status: status, CreatedAt: now, UpdatedAt: now, Actor: user}
	b, err := json.Marshal(payload)
	if err != nil {
		return op, err
	}
	command, err := json.Marshal(cmd)
	if err != nil {
		return op, err
	}
	_, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", op.ID, host, op.Kind, requestID, op.Status, "", now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano), user)
	if err == nil {
		_, err = tx.Exec("INSERT INTO managed_resources VALUES(?,?,?,?,?,?,?,?,?)", id, cmd.Kind, pool, host, name, requestID, string(b), string(command), op.ID)
	}
	if err == nil {
		err = c.audit(tx, user, cmd.Kind, id)
	}
	return op, err
}
func (c *Controller) replayResource(w http.ResponseWriter, tx *sql.Tx, requestID, kind string, payload any) bool {
	var raw, command string
	var opID string
	err := tx.QueryRow("SELECT payload,command,operation_id FROM managed_resources WHERE request_id=?", requestID).Scan(&raw, &command, &opID)
	if err == sql.ErrNoRows {
		var existing int
		if e := tx.QueryRow("SELECT count(*) FROM operations WHERE request_id=?", requestID).Scan(&existing); e != nil {
			fail(w, 500, "operation lookup failed")
			return true
		}
		if existing > 0 {
			fail(w, 409, "request_id already used for another operation")
			return true
		}
		return false
	}
	if err != nil {
		fail(w, 500, "resource lookup failed")
		return true
	}
	b, _ := json.Marshal(payload)
	var cmd protocol.Command
	json.Unmarshal([]byte(command), &cmd)
	if raw != string(b) || cmd.Kind != kind {
		fail(w, 409, "request_id already used with another payload")
		return true
	}
	op, err := scanOperation(tx.QueryRow("SELECT * FROM operations WHERE id=?", opID))
	if err != nil {
		fail(w, 500, "operation lookup failed")
		return true
	}
	write(w, 200, resourceResponse(cmd, op))
	return true
}
func resourceResponse(cmd protocol.Command, op protocol.Operation) any {
	switch {
	case cmd.Storage != nil:
		v := *cmd.Storage
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Network != nil:
		v := *cmd.Network
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Media != nil:
		v := *cmd.Media
		v.Status = op.Status
		v.Error = op.Error
		return v
	case cmd.Power != nil:
		return map[string]any{"vm_id": cmd.Power.VMID, "action": cmd.Power.Action, "operation": op}
	case cmd.VMMedia != nil:
		return map[string]any{"vm_id": cmd.VMMedia.VMID, "action": cmd.VMMedia.Action, "media_id": cmd.VMMedia.MediaID, "operation": op}
	default:
		return map[string]any{"vm_id": cmd.VM.ID, "operation": op}
	}
}
func (c *Controller) listResources(w http.ResponseWriter, kind string) {
	rows, err := c.db.Query("SELECT r.command,o.id,o.host_id,o.kind,o.request_id,o.status,o.error,o.created_at,o.updated_at,o.actor FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind=? ORDER BY r.name,r.id", kind)
	if err != nil {
		fail(w, 500, "resource lookup failed")
		return
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var raw, created, updated string
		var op protocol.Operation
		if rows.Scan(&raw, &op.ID, &op.HostID, &op.Kind, &op.RequestID, &op.Status, &op.Error, &created, &updated, &op.Actor) != nil {
			fail(w, 500, "resource decode failed")
			return
		}
		var cmd protocol.Command
		if json.Unmarshal([]byte(raw), &cmd) != nil {
			fail(w, 500, "resource decode failed")
			return
		}
		out = append(out, resourceResponse(cmd, op))
	}
	if rows.Err() != nil {
		fail(w, 500, "resource read failed")
		return
	}
	write(w, 200, out)
}

type registrationRequest struct {
	RequestID string `json:"request_id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Kind      string `json:"kind,omitempty"`
	Source    string `json:"source,omitempty"`
	Uplink    string `json:"uplink,omitempty"`
	VLAN      int    `json:"vlan,omitempty"`
}

var vmName = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var networkSource = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,62}$`)
var identifierValid = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`).MatchString
var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString

func storagePathValid(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != "/" && len(path) <= 4096 && !strings.ContainsAny(path, "\x00\r\n")
}

// networkShapeValid accepts an applied registration: a VLAN with its derived bridge, or a
// bridge/libvirt network source with an optional uplink on bridges only.
func networkShapeValid(kind, source, uplink string, vlan int) bool {
	if kind == "vlan" {
		return protocol.ValidateVLAN(protocol.NetworkRegistration{Kind: "vlan", Uplink: uplink, VLAN: vlan, Source: source}) == nil
	}
	return (kind == "bridge" || kind == "network") && networkSource.MatchString(source) && vlan == 0 && (uplink == "" || (kind == "bridge" && protocol.ValidUplink(uplink)))
}

// networkKey names the host resource a registration claims; VLAN and plain bridge
// registrations share the bridge namespace.
func networkKey(kind, source string) string {
	if kind == "network" {
		return "network:" + source
	}
	return "bridge:" + source
}

func (c *Controller) storage(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.registerResource(w, r, user, "storage-register")
}
func (c *Controller) networks(w http.ResponseWriter, r *http.Request, user, csrf string) {
	c.registerResource(w, r, user, "network-register")
}
func (c *Controller) registerResource(w http.ResponseWriter, r *http.Request, user, kind string) {
	if r.Method == "GET" {
		c.listResources(w, kind)
		return
	}
	var v registrationRequest
	if !decode(w, r, &v) {
		return
	}
	if !nameValid(v.Name) || !nameValid(v.RequestID) || !nameValid(v.HostID) || !nameValid(v.PoolID) {
		fail(w, 400, "name, pool_id, host_id and stable request_id required")
		return
	}
	v.Name = strings.TrimSpace(v.Name)
	if kind == "storage-register" {
		if !storagePathValid(v.Path) || v.Kind != "" || v.Source != "" || v.Uplink != "" || v.VLAN != 0 {
			fail(w, 400, "clean absolute existing storage path required")
			return
		}
	} else if v.Kind == "vlan" {
		// The bridge name is derived, so identical requests replay identically.
		if v.Path != "" || v.Source != "" {
			fail(w, 400, "VLAN networks take an uplink NIC and VLAN ID; the bridge name is derived")
			return
		}
		v.Source = protocol.GuestBridge(v.Uplink, v.VLAN)
		if err := protocol.ValidateVLAN(protocol.NetworkRegistration{Kind: "vlan", Uplink: v.Uplink, VLAN: v.VLAN, Source: v.Source}); err != nil {
			fail(w, 400, err.Error())
			return
		}
	} else if v.Path != "" || v.Uplink != "" || v.VLAN != 0 || (v.Kind != "bridge" && v.Kind != "network") || !networkSource.MatchString(v.Source) {
		fail(w, 400, "existing bridge or libvirt network source required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, kind, v) {
		return
	}
	if err = c.managedHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if v.Kind == "vlan" {
		inv, err := hostInventory(tx, v.HostID)
		if err != nil {
			fail(w, 500, "inventory unavailable")
			return
		}
		if err = uplinkObserved(inv, v.Uplink); err != nil {
			fail(w, 409, err.Error())
			return
		}
	}
	id, opID := random(), random()
	cmd := protocol.Command{OperationID: opID, Kind: kind}
	if kind == "storage-register" {
		cmd.Storage = &protocol.StorageRegistration{ID: id, PoolID: v.PoolID, HostID: v.HostID, Name: v.Name, Path: v.Path, OperationID: opID, Status: "queued"}
	} else {
		cmd.Network = &protocol.NetworkRegistration{ID: id, PoolID: v.PoolID, HostID: v.HostID, Name: v.Name, Kind: v.Kind, Source: v.Source, Uplink: v.Uplink, VLAN: v.VLAN, OperationID: opID, Status: "queued"}
	}
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, id, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "registration storage failed")
		return
	}
	write(w, 201, resourceResponse(cmd, op))
}

// adoptManaged is the managed step of a host report, run inside the report transaction after
// its results have been applied. It mirrors the pool-scoped configuration the host reports as
// applied from its journal, so a replaced Controller recovers a pool by connecting its master.
// Adopted entries keep their IDs and names, get the current pool/host, and are recorded as
// succeeded without a command: re-running them on the host would fail as already registered.
// The host's Controller appliance deployments become appliances rows, and journal entries that
// ended failed or needs_attention become operations with that status. An entry that is already
// held is silently current; one that is invalid, conflicting, unresolved or missing from libvirt
// is recorded once as a needs_attention operation plus an audit entry naming the reason.
func (c *Controller) adoptManaged(tx *sql.Tx, host string, inv *protocol.Inventory) error {
	m := inv.Managed
	if m == nil {
		return nil
	}
	var pool string
	if err := tx.QueryRow("SELECT pool_id FROM hosts WHERE id=?", host).Scan(&pool); err != nil {
		return err
	}
	taken := map[string]bool{}
	rows, err := tx.Query("SELECT r.kind,r.name,r.payload FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE (r.host_id=? AND r.kind IN ('storage-register','network-register') AND o.status!='failed') OR (r.pool_id=? AND r.kind='vm-create')", host, pool)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, name, raw string
		if err = rows.Scan(&kind, &name, &raw); err != nil {
			rows.Close()
			return err
		}
		var old registrationRequest
		json.Unmarshal([]byte(raw), &old)
		switch kind {
		case "storage-register":
			taken["path:"+old.Path] = true
		case "network-register":
			taken[networkKey(old.Kind, old.Source)] = true
		default:
			taken["vm:"+name] = true
		}
	}
	rows.Close()
	count := func(query string, args ...any) (int, error) {
		var n int
		err := tx.QueryRow(query, args...).Scan(&n)
		return n, err
	}
	// held reports an entry this Controller already knows under the same ID and kind: the
	// steady state of every report after adoption, which needs no record.
	held := func(id, kind string) (bool, error) {
		n, err := count("SELECT count(*) FROM managed_resources WHERE id=? AND kind=?", id, kind)
		return n > 0, err
	}
	free := func(id, key string) (bool, error) {
		if !identifierValid(id) || taken[key] {
			return false, nil
		}
		n, err := count("SELECT count(*) FROM managed_resources WHERE id=?", id)
		if err != nil || n > 0 {
			return false, err
		}
		taken[key] = true
		return true, nil
	}
	// attention records, once per host and entry, why the entry was not adopted.
	attention := func(kind, opKind, id, name, reason string) error {
		requestID := clip("adopt-attention:"+host+":"+kind+":"+id, 200)
		n, err := count("SELECT count(*) FROM operations WHERE request_id=?", requestID)
		if err != nil || n > 0 {
			return err
		}
		now := stamp()
		if _, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", random(), host, opKind, requestID, "needs_attention", clip("not adopted from host report: "+reason, 4096), now, now, "system"); err != nil {
			return err
		}
		return auditDetail(tx, "adopt.attention", clip(id, 200), clip(opKind+" "+name+": "+reason, 4096))
	}
	// resolved closes the attention record of an entry that has since been adopted.
	resolved := func(kind, id string) error {
		_, err := tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE request_id=? AND status='needs_attention'", stamp(), clip("adopt-attention:"+host+":"+kind+":"+id, 200))
		return err
	}
	save := func(kind, name, id string, cmd protocol.Command, payload any) error {
		if _, err := c.saveResourceStatus(tx, "system", "adopt-"+kind+":"+host+":"+id, pool, host, name, id, "succeeded", cmd, payload); err != nil {
			return err
		}
		return resolved(kind, id)
	}
	for _, s := range m.Storage {
		if ok, err := held(s.ID, "storage-register"); err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		reason := ""
		if !nameValid(s.Name) || !storagePathValid(s.Path) {
			reason = "invalid storage name or path"
		} else if ok, err := free(s.ID, "path:"+s.Path); err != nil {
			return err
		} else if !ok {
			reason = "storage ID or path already registered"
		}
		if reason != "" {
			if err = attention("storage", "storage-register", s.ID, s.Name, reason); err != nil {
				return err
			}
			continue
		}
		v := registrationRequest{RequestID: "adopt-storage:" + host + ":" + s.ID, PoolID: pool, HostID: host, Name: strings.TrimSpace(s.Name), Path: s.Path}
		opID := random()
		cmd := protocol.Command{OperationID: opID, Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: s.ID, PoolID: pool, HostID: host, Name: v.Name, Path: s.Path, OperationID: opID, Status: "succeeded"}}
		if err = save("storage", v.Name, s.ID, cmd, v); err != nil {
			return err
		}
	}
	for _, n := range m.Networks {
		if ok, err := held(n.ID, "network-register"); err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		reason := ""
		if !nameValid(n.Name) || !networkShapeValid(n.Kind, n.Source, n.Uplink, n.VLAN) {
			reason = "invalid network name or shape"
		} else if ok, err := free(n.ID, networkKey(n.Kind, n.Source)); err != nil {
			return err
		} else if !ok {
			reason = "network ID or source already registered"
		}
		if reason != "" {
			if err = attention("network", "network-register", n.ID, n.Name, reason); err != nil {
				return err
			}
			continue
		}
		v := registrationRequest{RequestID: "adopt-network:" + host + ":" + n.ID, PoolID: pool, HostID: host, Name: strings.TrimSpace(n.Name), Kind: n.Kind, Source: n.Source, Uplink: n.Uplink, VLAN: n.VLAN}
		opID := random()
		cmd := protocol.Command{OperationID: opID, Kind: "network-register", Network: &protocol.NetworkRegistration{ID: n.ID, PoolID: pool, HostID: host, Name: v.Name, Kind: n.Kind, Source: n.Source, Uplink: n.Uplink, VLAN: n.VLAN, OperationID: opID, Status: "succeeded"}}
		if err = save("network", v.Name, n.ID, cmd, v); err != nil {
			return err
		}
	}
	for _, md := range m.Media {
		if ok, err := held(md.ID, "iso-import"); err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		reason := ""
		if !nameValid(md.Name) || filepath.Base(md.Name) != md.Name || !strings.HasSuffix(strings.ToLower(md.Name), ".iso") || md.SizeBytes < 32774 || !sha256Hex(md.SHA256) {
			reason = "invalid ISO name, size or SHA-256"
		} else if _, e := readyResource(tx, md.StorageID, "storage-register", pool, host); e != nil {
			reason = "storage " + clip(md.StorageID, 64) + " is not adopted on this host"
		} else if ok, err := free(md.ID, "id:"+md.ID); err != nil {
			return err
		} else if !ok {
			reason = "ISO ID already held by another resource"
		}
		if reason != "" {
			if err = attention("media", "iso-import", md.ID, md.Name, reason); err != nil {
				return err
			}
			continue
		}
		v := mediaRequest{RequestID: "adopt-media:" + host + ":" + md.ID, StorageID: md.StorageID, Name: md.Name, SizeBytes: md.SizeBytes, SHA256: md.SHA256}
		opID := random()
		cmd := protocol.Command{OperationID: opID, Kind: "iso-import", Media: &protocol.Media{ID: md.ID, PoolID: pool, HostID: host, StorageID: md.StorageID, Name: md.Name, SizeBytes: md.SizeBytes, SHA256: md.SHA256, OperationID: opID, Status: "succeeded"}}
		if err = save("media", md.Name, md.ID, cmd, v); err != nil {
			return err
		}
	}
	missing := map[string]bool{}
	for _, id := range m.MissingVMs {
		missing[id] = true
	}
	for _, s := range m.VMs {
		if ok, err := held(s.ID, "vm-create"); err != nil || ok {
			if err != nil {
				return err
			}
			// Held: the host's current definition is the truth, so the mirror follows it.
			if err = c.refreshVM(tx, pool, host, s); err != nil {
				return err
			}
			continue
		}
		reason := ""
		if !vmName.MatchString(s.Name) || protocol.ValidateVMSpec(s) != nil || s.CPU.Count > 64 || s.Memory.SizeMiB > 262144 {
			reason = "invalid VM name or specification"
		} else if missing[s.ID] {
			reason = "VM definition is missing from libvirt on the host"
		}
		for _, d := range s.Disks {
			if _, e := readyResource(tx, d.StorageID, "storage-register", pool, host); e != nil && reason == "" {
				reason = "storage " + clip(d.StorageID, 64) + " is not adopted on this host"
			}
		}
		for _, n := range s.NICs {
			if _, e := readyResource(tx, n.NetworkID, "network-register", pool, host); e != nil && reason == "" {
				reason = "network " + clip(n.NetworkID, 64) + " is not adopted on this host"
			}
		}
		if s.MediaID != "" {
			if _, e := readyResource(tx, s.MediaID, "iso-import", pool, host); e != nil && reason == "" {
				reason = "ISO " + clip(s.MediaID, 64) + " is not adopted on this host"
			}
		}
		if reason == "" {
			if ok, err := free(s.ID, "vm:"+s.Name); err != nil {
				return err
			} else if !ok {
				reason = "VM ID or name already held in the pool"
			}
		}
		if reason != "" {
			if err = attention("vm", "vm-create", s.ID, s.Name, reason); err != nil {
				return err
			}
			continue
		}
		spec := s
		spec.PoolID, spec.HostID = pool, host
		// Start after creation is a one-shot creation action, not part of the VM definition:
		// the creation it belonged to already finished, so a new Controller never repeats it.
		spec.StartAfterCreate = false
		payload := spec
		payload.ID = ""
		v := createVMRequest{RequestID: "adopt-vm:" + host + ":" + s.ID, VMCreateSpec: payload}
		cmd := protocol.Command{OperationID: random(), Kind: "vm-create", VM: &spec}
		if err = save("vm", s.Name, s.ID, cmd, v); err != nil {
			return err
		}
	}
	if err = c.adoptAppliances(tx, host, pool, inv, attention, resolved); err != nil {
		return err
	}
	// The host's operation ID is the Controller's when this Controller issued it; only a
	// replaced Controller lacks the row. One query resolves every reported outcome.
	// ponytail: two placeholders per outcome, so the 10000-entry journal ceiling stays under
	// SQLite's 32766 variables; chunk the IN lists if the journal ceiling ever rises.
	known := map[string]bool{}
	if len(m.Outcomes) > 0 {
		ids, requests := make([]any, 0, len(m.Outcomes)), make([]any, 0, len(m.Outcomes))
		for _, o := range m.Outcomes {
			ids, requests = append(ids, o.OperationID), append(requests, "adopt-outcome:"+host+":"+o.OperationID)
		}
		marks := strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")
		rows, err := tx.Query("SELECT id,request_id FROM operations WHERE id IN ("+marks+") OR request_id IN ("+marks+")", append(ids, requests...)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id, request string
			if err = rows.Scan(&id, &request); err != nil {
				rows.Close()
				return err
			}
			known[id], known[request] = true, true
		}
		rows.Close()
	}
	// An adopted needs_attention outcome the host no longer reports was settled there (an
	// interrupted entry re-observed, or a pending command the restarted agent finished).
	settled := []any{stamp(), host, "adopt-outcome:" + host + ":%"}
	for _, o := range m.Outcomes {
		settled = append(settled, "adopt-outcome:"+host+":"+o.OperationID)
	}
	if _, err := tx.Exec("UPDATE operations SET status='succeeded',error='',updated_at=? WHERE host_id=? AND status='needs_attention' AND request_id LIKE ? AND request_id NOT IN ("+strings.TrimSuffix(strings.Repeat("?,", len(m.Outcomes)), ",")+")", settled...); err != nil {
		return err
	}
	for _, o := range m.Outcomes {
		// A creation's automatic start reports as <operation>:start.
		if !identifierValid(strings.TrimSuffix(o.OperationID, ":start")) || !managedKind(o.Kind) || (o.Status != "failed" && o.Status != "needs_attention") {
			continue
		}
		if known[o.OperationID] || known["adopt-outcome:"+host+":"+o.OperationID] {
			continue
		}
		known[o.OperationID] = true
		now := stamp()
		if _, err = tx.Exec("INSERT INTO operations VALUES(?,?,?,?,?,?,?,?,?)", o.OperationID, host, o.Kind, "adopt-outcome:"+host+":"+o.OperationID, o.Status, clip(o.Error, 4096), now, now, "system"); err != nil {
			return err
		}
		if err = auditDetail(tx, "adopt.outcome", clip(o.ID, 200), clip(o.Kind+" "+o.Name+" "+o.Status+": "+o.Error, 4096)); err != nil {
			return err
		}
	}
	return nil
}

// adoptAppliances rebuilds the appliances rows from the host's deploy journal. A row is
// inserted when the host observes the VM through healthy libvirt; a row held by a revoked
// host moves to the reporting host. POST /api/v1/appliances/adopt stays the handoff's path
// and replays as already adopted once the report has inserted the row.
func (c *Controller) adoptAppliances(tx *sql.Tx, host, pool string, inv *protocol.Inventory, attention func(kind, opKind, id, name, reason string) error, resolved func(kind, id string) error) error {
	libvirt := false
	for _, check := range inv.Checks {
		if check.Name == "libvirt" && check.Status == "healthy" {
			libvirt = true
		}
	}
	observed := map[string]bool{}
	for _, vm := range inv.VMs {
		observed[strings.ToLower(vm.ID)] = true
	}
	for _, ap := range inv.Managed.Appliances {
		reason := ""
		if !applianceUUID(ap.ID) || !nameValid(ap.Name) || !nameValid(ap.RequestID) || ap.Role != "controller" {
			reason = "invalid appliance record"
		} else if !observed[ap.ID] {
			if !libvirt {
				continue // unknown, not absent: the next healthy observation decides
			}
			reason = "deploy record present but libvirt does not list the VM"
		} else if n := 0; tx.QueryRow("SELECT count(*) FROM managed_resources WHERE id=?", ap.ID).Scan(&n) == nil && n > 0 {
			reason = "VM belongs to a managed creation operation"
		}
		if reason != "" {
			if err := attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: "+reason); err != nil {
				return err
			}
			continue
		}
		var oldID, oldHost string
		var oldRevoked bool
		err := tx.QueryRow("SELECT a.id,a.host_id,h.revoked FROM appliances a JOIN hosts h ON h.id=a.host_id WHERE a.id=? OR a.request_id=? LIMIT 1", ap.ID, ap.RequestID).Scan(&oldID, &oldHost, &oldRevoked)
		switch {
		case err == sql.ErrNoRows:
			if _, err = tx.Exec("INSERT INTO appliances VALUES(?,?,?,?,?)", ap.ID, pool, host, strings.TrimSpace(ap.Name), ap.RequestID); err != nil {
				return err
			}
			if err = c.audit(tx, "system", "appliance.adopt", ap.ID); err != nil {
				return err
			}
		case err != nil:
			return err
		case oldID != ap.ID:
			if err = attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: request_id already belongs to another appliance"); err != nil {
				return err
			}
			continue
		case oldHost != host && !oldRevoked:
			if err = attention("appliance", "vm-create", ap.ID, ap.Name, "Controller appliance: owned by another active host"); err != nil {
				return err
			}
			continue
		case oldHost != host:
			if _, err = tx.Exec("UPDATE appliances SET pool_id=?,host_id=? WHERE id=?", pool, host, ap.ID); err != nil {
				return err
			}
			if err = c.audit(tx, "system", "appliance.adopt", ap.ID); err != nil {
				return err
			}
		}
		if err = resolved("appliance", ap.ID); err != nil {
			return err
		}
	}
	return nil
}

func managedKind(kind string) bool {
	switch kind {
	case "storage-register", "network-register", "iso-import", "vm-create", "vm-update", "vm-power", "vm-media":
		return true
	}
	return false
}

// clip bounds host-supplied text stored in Controller rows.
func clip(s string, n int) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	for len(s) > n {
		_, size := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-size]
	}
	return s
}
func auditDetail(tx *sql.Tx, action, id, detail string) error {
	_, err := tx.Exec("INSERT INTO audit(actor,action,resource_id,created_at,detail) VALUES('system',?,?,?,?)", action, id, stamp(), detail)
	return err
}

// hostInventory returns the last observed inventory; an absent report decodes as empty.
func hostInventory(tx *sql.Tx, host string) (protocol.Inventory, error) {
	var raw sql.NullString
	var inv protocol.Inventory
	if err := tx.QueryRow("SELECT inventory FROM hosts WHERE id=?", host).Scan(&raw); err != nil {
		return inv, err
	}
	if raw.Valid {
		json.Unmarshal([]byte(raw.String), &inv)
	}
	return inv, nil
}

// uplinkObserved refuses uplinks the host has not reported as a physical NIC or bond, so a
// typo fails immediately instead of as a queued host task.
func uplinkObserved(inv protocol.Inventory, uplink string) error {
	for _, nic := range inv.NICs {
		if nic.Name == uplink {
			if nic.Kind == "" || nic.Kind == "physical" || nic.Kind == "bond" {
				return nil
			}
			return fmt.Errorf("uplink %s is a %s interface; choose a physical NIC or bond", uplink, nic.Kind)
		}
	}
	return fmt.Errorf("uplink %s has not been observed on this host", uplink)
}
func readyResource(tx *sql.Tx, id, kind, pool, host string) (protocol.Command, error) {
	var raw, status string
	var cmd protocol.Command
	err := tx.QueryRow("SELECT r.command,o.status FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.id=? AND r.kind=? AND r.pool_id=? AND r.host_id=?", id, kind, pool, host).Scan(&raw, &status)
	if err != nil || status != "succeeded" {
		return cmd, fmt.Errorf("selected resource must be verified on the selected host and pool")
	}
	err = json.Unmarshal([]byte(raw), &cmd)
	return cmd, err
}

type createVMRequest struct {
	RequestID string `json:"request_id"`
	protocol.VMCreateSpec
}

func (c *Controller) createVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v createVMRequest
	if !decode(w, r, &v) {
		return
	}
	if v.ID != "" || !nameValid(v.RequestID) || !vmName.MatchString(v.Name) {
		fail(w, 400, "valid name and stable request_id required; the Controller assigns the VM identity")
		return
	}
	if err := protocol.ValidateVMSpec(v.VMCreateSpec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
		fail(w, 400, "a VM allows at most 64 vCPUs and 262144 MiB of memory")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "vm-create", v) {
		return
	}
	if err = c.managedHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	// Every reference must name a registration this host has verified in this pool.
	resolve := func(id, kind, what string) (protocol.Command, bool) {
		cmd, e := readyResource(tx, id, kind, v.PoolID, v.HostID)
		if e != nil {
			fail(w, 404, what+" is not registered and verified on the selected host and pool")
		}
		return cmd, e == nil
	}
	for _, d := range v.Disks {
		if _, ok := resolve(d.StorageID, "storage-register", "the disk storage target"); !ok {
			return
		}
	}
	for _, nic := range v.NICs {
		if _, ok := resolve(nic.NetworkID, "network-register", "the selected network"); !ok {
			return
		}
	}
	if v.MediaID != "" {
		media, ok := resolve(v.MediaID, "iso-import", "the install ISO")
		if !ok {
			return
		}
		// Nex Core attaches the ISO from one of the VM's own storage targets.
		on := false
		for _, d := range v.Disks {
			on = on || (media.Media != nil && media.Media.StorageID == d.StorageID)
		}
		if !on {
			fail(w, 409, "the install ISO must be on one of the VM's disk storage targets")
			return
		}
	}
	inv, err := hostInventory(tx, v.HostID)
	if err != nil {
		fail(w, 500, "inventory unavailable")
		return
	}
	// The memory ceiling is what the host must be able to meet, in both memory modes.
	if v.CPU.Count > inv.CPUCount || uint64(v.Memory.SizeMiB)*1024*1024 > inv.MemoryBytes {
		fail(w, 409, "requested CPU or RAM exceeds observed host capacity")
		return
	}
	// Name and MAC uniqueness, dedicated-memory admission and start_after_create are the
	// host's: Core checks them against its journal and libvirt and reports a failed or
	// needs_attention result the operator reads in Task history.
	spec := v.VMCreateSpec
	b := random()[:32]
	spec.ID = fmt.Sprintf("%s-%s-4%s-a%s-%s", b[:8], b[8:12], b[13:16], b[17:20], b[20:])
	cmd := protocol.Command{OperationID: random(), Kind: "vm-create", VM: &spec}
	op, err := c.saveResource(tx, user, v.RequestID, v.PoolID, v.HostID, v.Name, spec.ID, cmd, v)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "VM operation storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}

// refreshVM makes the Controller's mirror of a VM it already holds match the definition its
// host reports as current, so an edit's result is the host's word and a Controller that missed
// it converges on the next report. The row's payload keeps the original request for replay.
func (c *Controller) refreshVM(tx *sql.Tx, pool, host string, reported protocol.VMCreateSpec) error {
	if !vmName.MatchString(reported.Name) || protocol.ValidateVMSpec(reported) != nil {
		return nil
	}
	var raw string
	err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.id=? AND r.pool_id=? AND r.host_id=? AND o.status='succeeded'", reported.ID, pool, host).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var cmd protocol.Command
	if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.VM == nil {
		return nil
	}
	next := reported
	next.PoolID, next.HostID, next.StartAfterCreate = pool, host, false
	held := *cmd.VM
	held.StartAfterCreate = false
	a, _ := json.Marshal(held)
	b, _ := json.Marshal(next)
	if string(a) == string(b) {
		return nil
	}
	cmd.VM = &next
	command, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	_, err = tx.Exec("UPDATE managed_resources SET command=?,name=? WHERE id=? AND kind='vm-create'", string(command), next.Name, next.ID)
	return err
}

type updateVMRequest struct {
	RequestID string `json:"request_id"`
	protocol.VMCreateSpec
}

// vmDefinition returns the mirrored current definition of a successfully created guest VM.
func vmDefinition(tx *sql.Tx, id string) (protocol.VMCreateSpec, error) {
	var raw string
	var cmd protocol.Command
	err := tx.QueryRow("SELECT r.command FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind='vm-create' AND r.id=? AND o.status='succeeded'", id).Scan(&raw)
	if err != nil {
		return protocol.VMCreateSpec{}, err
	}
	if json.Unmarshal([]byte(raw), &cmd) != nil || cmd.VM == nil {
		return protocol.VMCreateSpec{}, fmt.Errorf("VM definition invalid")
	}
	return *cmd.VM, nil
}

// updateVM queues one revision of a VM's definition for its assigned host. It validates the
// shape the way creation does, refuses what no edit may change and what the observed state
// cannot take (so the operator hears it before a task is queued), and freezes every automatic
// MAC into the definition so interfaces keep their identity across later edits. Name and MAC
// uniqueness against other VMs and dedicated-memory admission are the host's, as for creation;
// Core's own observation of the guest remains the authority on what applies live.
func (c *Controller) updateVM(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v updateVMRequest
	if !decode(w, r, &v) {
		return
	}
	id := r.PathValue("id")
	if (v.ID != "" && v.ID != id) || !nameValid(v.RequestID) || !vmName.MatchString(v.Name) {
		fail(w, 400, "valid name and stable request_id required; id must match the path or be omitted")
		return
	}
	if v.StartAfterCreate {
		fail(w, 400, "start_after_create applies to creation only; use the power controls")
		return
	}
	if err := protocol.ValidateVMSpec(v.VMCreateSpec); err != nil {
		fail(w, 400, err.Error())
		return
	}
	if v.CPU.Count > 64 || v.Memory.SizeMiB > 262144 {
		fail(w, 400, "a VM allows at most 64 vCPUs and 262144 MiB of memory")
		return
	}
	payload := v
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if c.replayResource(w, tx, v.RequestID, "vm-update", payload) {
		return
	}
	var appliance int
	if tx.QueryRow("SELECT count(*) FROM appliances WHERE id=?", id).Scan(&appliance) == nil && appliance > 0 {
		fail(w, 409, "the Controller appliance is managed from its host's local page, not edited here")
		return
	}
	cur, err := vmDefinition(tx, id)
	if err != nil {
		fail(w, 404, "VM not found")
		return
	}
	if v.PoolID != cur.PoolID || v.HostID != cur.HostID {
		fail(w, 400, "editing does not move a VM to another pool or host")
		return
	}
	spec := v.VMCreateSpec
	// Frozen addresses go into the dispatched definition, never into the replay payload.
	spec.NICs = append([]protocol.VMNIC{}, spec.NICs...)
	spec.ID, spec.Revision = id, cur.Revision+1
	stops, err := protocol.NeedsStop(cur, spec)
	if err != nil {
		fail(w, 400, err.Error())
		return
	}
	if err = c.managedHost(tx, cur.PoolID, cur.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	var raw string
	var caps []string
	tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", cur.HostID).Scan(&raw)
	json.Unmarshal([]byte(raw), &caps)
	if !slices.Contains(caps, protocol.UpdateCapability) {
		fail(w, 409, "the VM's host agent does not support editing VMs; upgrade Nex Core")
		return
	}
	var pending int
	if err = tx.QueryRow("SELECT count(*) FROM managed_resources r JOIN operations o ON o.id=r.operation_id WHERE r.kind IN ('vm-update','vm-power','vm-media') AND r.name=? AND o.status IN ('queued','dispatched')", id).Scan(&pending); err != nil {
		fail(w, 500, "pending request lookup failed")
		return
	}
	if pending > 0 {
		fail(w, 409, "a change, power or media request for this VM is still pending; wait for its task")
		return
	}
	var revoked bool
	var inventory sql.NullString
	if tx.QueryRow("SELECT revoked,inventory FROM hosts WHERE id=?", cur.HostID).Scan(&revoked, &inventory) == nil {
		if state, _, _ := c.observeVM(cur.HostID, id, revoked, inventory); state == "running" && len(stops) > 0 {
			names := make([]string, len(stops))
			for i, s := range stops {
				names[i] = string(s)
			}
			fail(w, 409, "stop the VM first to change "+strings.Join(names, ", "))
			return
		}
	}
	resolve := func(id, kind, what string) (protocol.Command, bool) {
		cmd, e := readyResource(tx, id, kind, cur.PoolID, cur.HostID)
		if e != nil {
			fail(w, 404, what+" is not registered and verified on the VM's host and pool")
		}
		return cmd, e == nil
	}
	for _, d := range spec.Disks {
		if _, ok := resolve(d.StorageID, "storage-register", "the disk storage target"); !ok {
			return
		}
	}
	for _, nic := range spec.NICs {
		if _, ok := resolve(nic.NetworkID, "network-register", "the selected network"); !ok {
			return
		}
	}
	if spec.MediaID != "" {
		media, ok := resolve(spec.MediaID, "iso-import", "the install ISO")
		if !ok {
			return
		}
		on := false
		for _, d := range spec.Disks {
			on = on || (media.Media != nil && media.Media.StorageID == d.StorageID)
		}
		if !on {
			fail(w, 409, "the install ISO must be on one of the VM's disk storage targets")
			return
		}
	}
	inv, err := hostInventory(tx, cur.HostID)
	if err != nil {
		fail(w, 500, "inventory unavailable")
		return
	}
	if spec.CPU.Count > inv.CPUCount || uint64(spec.Memory.SizeMiB)*1024*1024 > inv.MemoryBytes {
		fail(w, 409, "requested CPU or RAM exceeds observed host capacity")
		return
	}
	// Interfaces keep their identity by address: every automatic MAC is frozen into the
	// definition here, drawn from the VM's derived range at the first index this VM does not
	// already use. Uniqueness against other VMs is the host's check.
	taken := map[string]bool{}
	for _, nic := range spec.NICs {
		taken[nic.MAC] = true
	}
	for i := range spec.NICs {
		if spec.NICs[i].MAC != "" {
			continue
		}
		for k := 0; k < 8; k++ {
			if mac := protocol.DerivedMAC(id, k); !taken[mac] {
				spec.NICs[i].MAC, taken[mac] = mac, true
				break
			}
		}
		if spec.NICs[i].MAC == "" {
			fail(w, 409, "could not derive a free MAC address for this VM; set it manually")
			return
		}
	}
	cmd := protocol.Command{OperationID: random(), Kind: "vm-update", VM: &spec}
	op, err := c.saveResource(tx, user, v.RequestID, cur.PoolID, cur.HostID, id, random(), cmd, payload)
	if err != nil || tx.Commit() != nil {
		fail(w, 500, "VM operation storage failed")
		return
	}
	write(w, 202, resourceResponse(cmd, op))
}
