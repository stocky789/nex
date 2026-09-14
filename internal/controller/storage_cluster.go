// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"nex.local/nex/internal/protocol"
	"strings"
	"sync"
	"time"
)

// Storage-cluster setup is a Core-owned durable operation. This broker only
// holds one report round-trip in memory, like storage discovery.
type storageClusterPending struct {
	request    protocol.StorageClusterRequest
	result     chan protocol.StorageClusterResult
	dispatched bool
}
type storageClusterBroker struct {
	mu      sync.Mutex
	pending map[string]storageClusterPending
}

func (c *Controller) clusterBroker() *storageClusterBroker {
	c.clusterOnce.Do(func() { c.clusters.pending = map[string]storageClusterPending{} })
	return &c.clusters
}

func (c *Controller) takeStorageClusters(host string) []protocol.StorageClusterRequest {
	b := c.clusterBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []protocol.StorageClusterRequest
	for id, pending := range b.pending {
		if pending.request.HostID != host || pending.dispatched {
			continue
		}
		out = append(out, pending.request)
		pending.dispatched = true
		b.pending[id] = pending
	}
	return out
}

func (c *Controller) completeStorageClusters(host string, results []protocol.StorageClusterResult) {
	b := c.clusterBroker()
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, result := range results {
		pending, ok := b.pending[result.RequestID]
		if !ok || result.HostID != host || result.HostID != pending.request.HostID || result.PoolID != pending.request.PoolID || !clusterResultValid(result) {
			continue
		}
		select {
		case pending.result <- result:
		default:
		}
		delete(b.pending, result.RequestID)
	}
}

func clusterResultValid(v protocol.StorageClusterResult) bool {
	if (v.Status != "accepted" && v.Status != "queued" && v.Status != "succeeded" && v.Status != "failed" && v.Status != "needs_attention") || len(v.Error) > 4096 || len(v.Operations) > 128 {
		return false
	}
	for _, op := range v.Operations {
		if op.RequestID == "" || len(op.Error) > 4096 || len(op.Members) > 5 {
			return false
		}
		switch op.Status {
		case "queued", "awaiting_fencing", "verifying_fence", "applying", "healthy", "degraded", "succeeded", "failed", "needs_attention":
		default:
			return false
		}
		seen := map[string]bool{}
		configured := map[string]bool{}
		for _, node := range op.Config.Nodes {
			configured[node.HostID] = true
		}
		for _, member := range op.Members {
			if !nameValid(member.HostID) || !configured[member.HostID] || seen[member.HostID] || member.Generation != op.Config.Generation || member.MembershipGeneration != op.MembershipGeneration || member.ClusterID != op.Config.ClusterID || member.ConfigDigest != op.Config.Digest() || (member.Stage != "health" && member.Stage != "monitor") || member.UpdatedAt.IsZero() {
				return false
			}
			seen[member.HostID] = true
		}
	}
	return true
}

func clusterResultHasSessionMonitor(v protocol.StorageClusterResult) bool {
	for _, operation := range v.Operations {
		for _, member := range operation.Members {
			if member.Stage == "monitor" || member.Status.SessionMonitorReady {
				return true
			}
		}
	}
	return false
}

type storageClusterAPIRequest struct {
	RequestID          string                                 `json:"request_id"`
	PoolID             string                                 `json:"pool_id"`
	ExpectedGeneration uint64                                 `json:"expected_generation"`
	CredentialRequests []protocol.StorageClusterCredentialRef `json:"credential_requests,omitempty"`
	Registration       protocol.StorageRegistration           `json:"registration"`
	Config             protocol.StorageClusterConfig          `json:"config"`
}

// storageClusterNegotiatingError means a v2 Core has made its safe first
// clustered-storage preflight but has not yet received the acknowledgement
// required before it may accept work. It is deliberately retryable, unlike a
// Core which never advertises the v2 preflight capability.
type storageClusterNegotiatingError struct{}

func (storageClusterNegotiatingError) Error() string {
	return "clustered storage capability negotiation is pending"
}

func storageClusterCapabilityStatus(err error) int {
	var pending storageClusterNegotiatingError
	if errors.As(err, &pending) {
		return http.StatusServiceUnavailable
	}
	return http.StatusConflict
}

func storageClusterCapability(values []string) error {
	if contains(values, protocol.StorageClusterCapability) {
		return nil
	}
	if contains(values, protocol.StorageClusterSessionMonitorCapability) {
		return storageClusterNegotiatingError{}
	}
	return fmt.Errorf("host does not support clustered storage")
}

func (c *Controller) storageClusterMaster(tx *sql.Tx, pool string) (string, error) {
	var host, caps string
	if err := tx.QueryRow(`SELECT p.master_host_id,coalesce(c.capabilities,'[]') FROM pools p LEFT JOIN host_capabilities c ON c.host_id=p.master_host_id WHERE p.id=?`, pool).Scan(&host, &caps); err != nil {
		return "", fmt.Errorf("pool not found")
	}
	if err := c.managedHost(tx, pool, host); err != nil {
		return "", err
	}
	var values []string
	if json.Unmarshal([]byte(caps), &values) != nil {
		return "", fmt.Errorf("pool master does not support clustered storage")
	}
	if err := storageClusterCapability(values); err != nil {
		if _, ok := err.(storageClusterNegotiatingError); ok {
			return "", err
		}
		return "", fmt.Errorf("pool master does not support clustered storage")
	}
	return host, nil
}

func (c *Controller) storageClusterHost(tx *sql.Tx, pool, host string) error {
	if err := c.managedHost(tx, pool, host); err != nil {
		return err
	}
	var caps string
	if err := tx.QueryRow("SELECT capabilities FROM host_capabilities WHERE host_id=?", host).Scan(&caps); err != nil {
		return fmt.Errorf("host does not support clustered storage")
	}
	var values []string
	if json.Unmarshal([]byte(caps), &values) != nil {
		return fmt.Errorf("host does not support clustered storage")
	}
	return storageClusterCapability(values)
}

func (c *Controller) relayStorageCluster(w http.ResponseWriter, r *http.Request, request protocol.StorageClusterRequest) {
	b := c.clusterBroker()
	result := make(chan protocol.StorageClusterResult, 1)
	b.mu.Lock()
	if _, exists := b.pending[request.RequestID]; exists {
		b.mu.Unlock()
		fail(w, http.StatusConflict, "cluster request is already active")
		return
	}
	b.pending[request.RequestID] = storageClusterPending{request: request, result: result}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, request.RequestID)
		b.mu.Unlock()
	}()
	select {
	case found := <-result:
		write(w, http.StatusOK, found)
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		write(w, http.StatusGatewayTimeout, protocol.StorageClusterResult{RequestID: request.RequestID, PoolID: request.PoolID, HostID: request.HostID, Status: "needs_attention", Error: "Core did not confirm clustered storage before the deadline", ObservedAt: time.Now().UTC()})
	}
}

func (c *Controller) applyStorageCluster(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body storageClusterAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(body.RequestID) || !nameValid(body.PoolID) || body.Config.PoolID != "" && body.Config.PoolID != body.PoolID || body.ExpectedGeneration != 0 && body.Config.Generation != body.ExpectedGeneration || body.Registration.PoolID != "" && body.Registration.PoolID != body.PoolID || !clusterCredentialRefsValid(body.CredentialRequests) {
		fail(w, http.StatusBadRequest, "stable request_id and pool_id with consistent optional generation required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	host, err := c.storageClusterMaster(tx, body.PoolID)
	if err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	// Core derives device and mount paths from its own validated iSCSI identity;
	// this transient relay never writes the registration or config to Controller state.
	body.Registration.Path = ""
	if body.Registration.Source.ISCSI != nil {
		body.Registration.Source.ISCSI.MountPath = ""
	}
	body.Config.DevicePath, body.Config.MountPath = "", ""
	body.Registration.PoolID, body.Registration.HostID = body.PoolID, host
	c.relayStorageCluster(w, r, protocol.StorageClusterRequest{RequestID: body.RequestID, PoolID: body.PoolID, HostID: host, Action: "apply", CredentialRequests: body.CredentialRequests, Registration: body.Registration, Config: body.Config, ExpectedGeneration: body.ExpectedGeneration})
}

func clusterCredentialRefsValid(refs []protocol.StorageClusterCredentialRef) bool {
	if len(refs) > 8 {
		return false
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		if !nameValid(ref.RequestID) || !nameValid(ref.TargetHostID) || (ref.Kind != "chap" && ref.Kind != "fence_bmc" && ref.Kind != "fence_ssh") || seen[ref.RequestID] {
			return false
		}
		seen[ref.RequestID] = true
	}
	return true
}

type storageClusterCredentialAPIRequest struct {
	RequestID        string                      `json:"request_id"`
	ClusterRequestID string                      `json:"cluster_request_id"`
	PoolID           string                      `json:"pool_id"`
	TargetHostID     string                      `json:"target_host_id"`
	Kind             string                      `json:"kind"`
	Credentials      protocol.StorageCredentials `json:"credentials"`
}

func clusterCredentialBinding(v storageClusterCredentialAPIRequest) string {
	return "cluster-credential:" + hex.EncodeToString([]byte(v.ClusterRequestID)) + ":" + v.Kind + ":" + hex.EncodeToString([]byte(v.TargetHostID))
}

// storageClusterCredentials uses the established one-time encrypted offer path.
// The response deliberately exposes no receipt: Core resolves the private receipt
// by request ID and creates its own opaque alias for a native fence profile.
func (c *Controller) storageClusterCredentials(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body storageClusterCredentialAPIRequest
	if !decodeLimit(w, r, &body, 1<<20) {
		return
	}
	binding := clusterCredentialBinding(body)
	ssh := body.Kind == "fence_ssh"
	if !nameValid(body.RequestID) || !nameValid(body.ClusterRequestID) || !nameValid(body.PoolID) || !nameValid(body.TargetHostID) || !nameValid(binding) || (body.Kind != "chap" && body.Kind != "fence_bmc" && body.Kind != "fence_ssh") || len(body.Credentials.Username) > 4096 || len(body.Credentials.Secret) > 65536 || len(body.Credentials.SSHPrivateKey) > 65536 || len(body.Credentials.SSHKnownHosts) > 65536 || strings.ContainsAny(body.Credentials.Username+body.Credentials.Secret+body.Credentials.SSHPrivateKey+body.Credentials.SSHKnownHosts, "\x00\r") || (!ssh && strings.ContainsAny(body.Credentials.Username+body.Credentials.Secret, "\n")) || (ssh && (body.Credentials.Secret != "" || body.Credentials.SSHPrivateKey == "" || body.Credentials.SSHKnownHosts == "")) || (!ssh && (body.Credentials.Secret == "" || body.Credentials.SSHPrivateKey != "" || body.Credentials.SSHKnownHosts != "")) {
		fail(w, http.StatusBadRequest, "valid scoped cluster credential required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	host, err := c.storageClusterMaster(tx, body.PoolID)
	if err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	if err = c.storageClusterHost(tx, body.PoolID, body.TargetHostID); err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	if _, err = c.credentials.offer(storageCredentialRequest{RequestID: body.RequestID, RegistrationRequestID: binding, PoolID: body.PoolID, HostID: host, Credentials: body.Credentials}); err != nil {
		fail(w, http.StatusConflict, err.Error())
		return
	}
	if _, ok := c.credentials.wait(body.RequestID); !ok {
		fail(w, http.StatusServiceUnavailable, "Core did not acknowledge cluster credential delivery; retry with the same request_id")
		return
	}
	write(w, http.StatusAccepted, map[string]string{"request_id": body.RequestID, "status": "accepted"})
}

func (c *Controller) storageCluster(w http.ResponseWriter, r *http.Request, user, csrf string) {
	pool := r.URL.Query().Get("pool_id")
	if !nameValid(pool) {
		fail(w, http.StatusBadRequest, "pool_id required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	host, err := c.storageClusterMaster(tx, pool)
	if err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	c.relayStorageCluster(w, r, protocol.StorageClusterRequest{RequestID: random(), PoolID: pool, HostID: host, Action: "status"})
}

type storageClusterFenceAPIRequest struct {
	RequestID         string `json:"request_id"`
	PoolID            string `json:"pool_id"`
	HostID            string `json:"host_id"`
	RepositoryID      string `json:"repository_id"`
	FenceConfirmation string `json:"fence_confirmation"`
}

func (c *Controller) verifyStorageClusterFence(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var body storageClusterFenceAPIRequest
	if !decode(w, r, &body) {
		return
	}
	if !nameValid(body.RequestID) || !nameValid(body.PoolID) || !nameValid(body.HostID) || !identifierValid(body.RepositoryID) || body.FenceConfirmation != "FENCE "+body.HostID {
		fail(w, http.StatusBadRequest, "stable request_id, pool_id, repository_id, host_id and exact fence confirmation required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if err = c.storageClusterHost(tx, body.PoolID, body.HostID); err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	master, err := c.storageClusterMaster(tx, body.PoolID)
	if err != nil {
		fail(w, storageClusterCapabilityStatus(err), err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, http.StatusInternalServerError, "storage unavailable")
		return
	}
	c.relayStorageCluster(w, r, protocol.StorageClusterRequest{RequestID: body.RequestID, PoolID: body.PoolID, HostID: master, FenceHostID: body.HostID, FenceConfirmation: body.FenceConfirmation, Action: "verify_fence", Config: protocol.StorageClusterConfig{RepositoryID: body.RepositoryID}})
}
