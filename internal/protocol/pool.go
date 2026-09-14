// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"encoding/hex"
	"errors"
	"time"
)

// PoolMember is a Core-master-owned membership record.  The Controller mirrors it from
// inventory; it must not infer membership from which hosts happen to be connected.
type PoolMember struct {
	HostID            string    `json:"host_id"`
	CertificateSHA256 string    `json:"certificate_sha256"`
	Generation        uint64    `json:"generation"`
	Status            string    `json:"status"`
	ObservedAt        time.Time `json:"observed_at"`
	Error             string    `json:"error,omitempty"`
	PeerOrigin        string    `json:"peer_origin,omitempty"`
}

// PoolMembership is retained by the master and copied to a member only after the member
// proves the master's certificate-bound identity.  Generation prevents an old peer request
// from resurrecting an ejected member.
type PoolMembership struct {
	PoolID       string       `json:"pool_id"`
	MasterHostID string       `json:"master_host_id"`
	Generation   uint64       `json:"generation"`
	Members      []PoolMember `json:"members"`
}

// PoolCommand is sent only to the pool master.  The Controller is a transport for this
// request and never persists membership intent as authoritative state.
type PoolCommand struct {
	RequestID       string         `json:"request_id"`
	Action          string         `json:"action"`
	Membership      PoolMembership `json:"membership"`
	MemberEndpoint  string         `json:"member_endpoint,omitempty"`
	RepositoryID    string         `json:"repository_id,omitempty"`
	ExpectedVersion uint64         `json:"expected_version,omitempty"`
}

func (c PoolCommand) Valid() error {
	if c.RequestID == "" || c.Action != "member-join" && c.Action != "storage-reconcile" {
		return errors.New("invalid pool command action")
	}
	if err := c.Membership.Valid(); err != nil {
		return err
	}
	if c.ExpectedVersion != 0 && c.ExpectedVersion != c.Membership.Generation {
		return errors.New("pool command membership generation changed")
	}
	if c.Action == "storage-reconcile" && c.RepositoryID == "" {
		return errors.New("pool storage reconciliation requires a repository")
	}
	return nil
}

func (m PoolMembership) Valid() error {
	if m.PoolID == "" || m.MasterHostID == "" || m.Generation == 0 {
		return errors.New("pool membership requires pool, master and generation")
	}
	seen, master := map[string]bool{}, false
	for _, member := range m.Members {
		_, hexErr := hex.DecodeString(member.CertificateSHA256)
		if member.HostID == "" || seen[member.HostID] || len(member.CertificateSHA256) != 64 || hexErr != nil || member.Generation != m.Generation || (member.Status != "ready" && member.Status != "pending" && member.Status != "failed" && member.Status != "ejected") {
			return errors.New("invalid pool member record")
		}
		seen[member.HostID] = true
		master = master || member.HostID == m.MasterHostID && member.Status == "ready"
	}
	if !master {
		return errors.New("pool membership omits ready master")
	}
	return nil
}

// PoolStorageAccess is a member's observed access to a master-owned repository.  It is not
// a placement grant: Core checks current access again before every VM write or start.
type PoolStorageAccess struct {
	RepositoryID string    `json:"repository_id"`
	HostID       string    `json:"host_id"`
	Generation   uint64    `json:"generation"`
	Identity     string    `json:"identity"`
	Readable     bool      `json:"readable"`
	Writable     bool      `json:"writable"`
	QEMUWritable bool      `json:"qemu_writable"`
	Locking      bool      `json:"locking"`
	ObservedAt   time.Time `json:"observed_at"`
	Error        string    `json:"error,omitempty"`
}
