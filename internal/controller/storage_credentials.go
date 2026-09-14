// SPDX-License-Identifier: AGPL-3.0-only
package controller

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

const credentialWait = 15 * time.Second
const maxCredentialOffers = 128

type storageCredentialRequest struct {
	RequestID             string                      `json:"request_id"`
	RegistrationRequestID string                      `json:"registration_request_id,omitempty"`
	PoolID                string                      `json:"pool_id"`
	HostID                string                      `json:"host_id"`
	RepositoryID          string                      `json:"repository_id,omitempty"`
	Credentials           protocol.StorageCredentials `json:"credentials"`
}

type credentialBrokerEntry struct {
	offer       protocol.StorageCredentialOffer
	credentials protocol.StorageCredentials
	receipt     protocol.StorageCredentialReceipt
	proof       []byte
	done        chan struct{}
	delivered   bool
}

// credentialBroker deliberately has no persistence: it transports an encrypted request once
// to the addressed Core. Its only durable output is the Core receipt.
type credentialBroker struct {
	mu           sync.Mutex
	entries      map[string]*credentialBrokerEntry
	receipts     map[string]protocol.StorageCredentialReceipt
	receiptHosts map[string]time.Time
	key          [32]byte
}

func newCredentialBroker() *credentialBroker {
	b := &credentialBroker{entries: map[string]*credentialBrokerEntry{}, receipts: map[string]protocol.StorageCredentialReceipt{}, receiptHosts: map[string]time.Time{}}
	if _, err := rand.Read(b.key[:]); err != nil {
		panic("credential broker random source unavailable")
	}
	return b
}

func receiptKey(host, requestID string) string { return host + "\x00" + requestID }

// observeReceipts keeps a bounded live view of Core-reported opaque receipts. It has no
// credential material and is deliberately rebuilt after a Controller replacement.
func (b *credentialBroker) observeReceipts(host string, receipts []protocol.StorageCredentialReceipt) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.receiptHosts[host] = time.Now().UTC()
	// A Managed receipt list is an authoritative Core observation for this host, including an
	// explicit empty list after retirement. Replace only that host's disposable mirror.
	for key, receipt := range b.receipts {
		if receipt.HostID == host {
			delete(b.receipts, key)
		}
	}
	for _, receipt := range receipts {
		if receipt.HostID != host || receipt.RequestID == "" || receipt.Receipt == "" {
			continue
		}
		if len(b.receipts) >= 1024 {
			break
		}
		b.receipts[receiptKey(host, receipt.RequestID)] = receipt
	}
}

func (b *credentialBroker) eligibleReceipts(poolID, hostID string) ([]protocol.StorageCredentialReceipt, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, observed := b.receiptHosts[hostID]; !observed {
		return nil, false
	}
	out := []protocol.StorageCredentialReceipt{}
	for _, receipt := range b.receipts {
		if receipt.PoolID == poolID && receipt.HostID == hostID && receipt.RetireEligible {
			out = append(out, receipt)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Receipt < out[j].Receipt })
	return out, true
}

func (b *credentialBroker) receipt(host, requestID, poolID, repositoryID, registrationRequestID string) (protocol.StorageCredentialReceipt, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	receipt, ok := b.receipts[receiptKey(host, requestID)]
	return receipt, ok && receipt.PoolID == poolID && receipt.RepositoryID == repositoryID && receipt.RegistrationRequestID == registrationRequestID
}

func (b *credentialBroker) proof(v storageCredentialRequest) []byte {
	m := hmac.New(sha256.New, b.key[:])
	fields := []string{v.RequestID, v.RegistrationRequestID, v.PoolID, v.HostID, v.RepositoryID, v.Credentials.Username, v.Credentials.Secret}
	if v.Credentials.Domain != "" {
		fields = append(fields, v.Credentials.Domain)
	}
	for _, field := range fields {
		_, _ = m.Write([]byte(field))
		_, _ = m.Write([]byte{0})
	}
	return m.Sum(nil)
}

func (b *credentialBroker) expire(requestID, handle string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if entry := b.entries[requestID]; entry != nil && entry.offer.Handle == handle && time.Now().After(entry.offer.ExpiresAt) {
		entry.credentials = protocol.StorageCredentials{}
		delete(b.entries, requestID)
	}
}

func newCredentialHandle() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (b *credentialBroker) offer(v storageCredentialRequest) (protocol.StorageCredentialOffer, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for id, entry := range b.entries {
		if time.Now().After(entry.offer.ExpiresAt) {
			delete(b.entries, id)
		}
	}
	if old := b.entries[v.RequestID]; old != nil {
		if old.offer.PoolID != v.PoolID || old.offer.HostID != v.HostID || old.offer.RepositoryID != v.RepositoryID || old.offer.RegistrationRequestID != v.RegistrationRequestID || (old.receipt.Receipt == "" && old.credentials != v.Credentials) || (old.receipt.Receipt != "" && !hmac.Equal(old.proof, b.proof(v))) {
			return protocol.StorageCredentialOffer{}, errors.New("request_id already used with another credential payload")
		}
		return old.offer, nil
	}
	if len(b.entries) >= maxCredentialOffers {
		return protocol.StorageCredentialOffer{}, errors.New("credential delivery queue is full")
	}
	handle, err := newCredentialHandle()
	if err != nil {
		return protocol.StorageCredentialOffer{}, err
	}
	offer := protocol.StorageCredentialOffer{Handle: handle, RequestID: v.RequestID, RegistrationRequestID: v.RegistrationRequestID, PoolID: v.PoolID, HostID: v.HostID, RepositoryID: v.RepositoryID, ExpiresAt: time.Now().Add(credentialWait).UTC()}
	b.entries[v.RequestID] = &credentialBrokerEntry{offer: offer, credentials: v.Credentials, proof: b.proof(v), done: make(chan struct{})}
	time.AfterFunc(time.Until(offer.ExpiresAt), func() { b.expire(v.RequestID, offer.Handle) })
	return offer, nil
}

func (b *credentialBroker) redeem(handle, host string) (protocol.StorageCredentialHandoff, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.entries {
		if entry.offer.Handle != handle {
			continue
		}
		if entry.offer.HostID != host || entry.receipt.Receipt != "" || entry.delivered || time.Now().After(entry.offer.ExpiresAt) {
			return protocol.StorageCredentialHandoff{}, errors.New("credential offer is unavailable")
		}
		handoff := protocol.StorageCredentialHandoff{Offer: entry.offer, Credentials: entry.credentials}
		// Credential bytes are one-shot transport data, not retryable Controller
		// state. Core acknowledgement retains only the opaque receipt.
		entry.credentials = protocol.StorageCredentials{}
		entry.delivered = true
		return handoff, nil
	}
	return protocol.StorageCredentialHandoff{}, errors.New("credential offer is unavailable")
}

func (b *credentialBroker) offers(host string) []protocol.StorageCredentialOffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	out := []protocol.StorageCredentialOffer{}
	for id, entry := range b.entries {
		if now.After(entry.offer.ExpiresAt) {
			delete(b.entries, id)
			continue
		}
		if entry.offer.HostID == host && entry.receipt.Receipt == "" {
			out = append(out, entry.offer)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out
}

func (b *credentialBroker) acknowledge(handle, host string, receipt protocol.StorageCredentialReceipt) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, entry := range b.entries {
		if entry.offer.Handle != handle {
			continue
		}
		if entry.offer.HostID != host || receipt.RequestID != entry.offer.RequestID || receipt.PoolID != entry.offer.PoolID || receipt.HostID != entry.offer.HostID || receipt.RepositoryID != entry.offer.RepositoryID || receipt.RegistrationRequestID != entry.offer.RegistrationRequestID || receipt.Receipt == "" || receipt.Status != "ready" {
			return errors.New("credential receipt does not match offer")
		}
		if entry.receipt.Receipt == "" {
			entry.receipt = receipt
			entry.credentials = protocol.StorageCredentials{}
			close(entry.done)
		}
		return nil
	}
	return errors.New("credential offer is unavailable")
}

func (b *credentialBroker) wait(requestID string) (protocol.StorageCredentialReceipt, bool) {
	b.mu.Lock()
	entry := b.entries[requestID]
	b.mu.Unlock()
	if entry == nil {
		return protocol.StorageCredentialReceipt{}, false
	}
	select {
	case <-entry.done:
		return entry.receipt, true
	case <-time.After(time.Until(entry.offer.ExpiresAt)):
		return protocol.StorageCredentialReceipt{}, false
	}
}

func (c *Controller) storageCredentials(w http.ResponseWriter, r *http.Request, user, csrf string) {
	var v storageCredentialRequest
	if !decodeLimit(w, r, &v, 1<<20) {
		return
	}
	if !nameValid(v.RequestID) || !nameValid(v.PoolID) || !nameValid(v.HostID) || (v.RepositoryID != "" && !nameValid(v.RepositoryID)) || (v.RegistrationRequestID != "" && !nameValid(v.RegistrationRequestID)) || (v.RepositoryID == "" && v.RegistrationRequestID == "") || len(v.Credentials.Username) > 4096 || len(v.Credentials.Secret) == 0 || len(v.Credentials.Secret) > 65536 || len(v.Credentials.Domain) > 253 || strings.ContainsAny(v.Credentials.Domain, "/\\= \t;'\"`$&|<>()") || v.Credentials.SSHPrivateKey != "" || v.Credentials.SSHKnownHosts != "" || strings.ContainsAny(v.Credentials.Username+v.Credentials.Secret+v.Credentials.Domain, "\x00\r\n") {
		fail(w, 400, "valid request, pool, host and bounded credential required")
		return
	}
	tx, err := c.db.Begin()
	if err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	defer tx.Rollback()
	if err = c.storageRepositoryHost(tx, v.PoolID, v.HostID); err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err = tx.Commit(); err != nil {
		fail(w, 500, "storage unavailable")
		return
	}
	if _, err = c.credentials.offer(v); err != nil {
		fail(w, 409, err.Error())
		return
	}
	receipt, ok := c.credentials.wait(v.RequestID)
	if !ok {
		fail(w, 503, "Core did not acknowledge credential delivery; retry with the same request_id")
		return
	}
	write(w, http.StatusCreated, receipt)
}

func (c *Controller) storageCredentialStatus(w http.ResponseWriter, r *http.Request, user, csrf string) {
	requestID, poolID, hostID := r.PathValue("request_id"), r.URL.Query().Get("pool_id"), r.URL.Query().Get("host_id")
	repositoryID, registrationRequestID := r.URL.Query().Get("repository_id"), r.URL.Query().Get("registration_request_id")
	if !nameValid(requestID) || !nameValid(poolID) || !nameValid(hostID) || (repositoryID != "" && !nameValid(repositoryID)) || (registrationRequestID != "" && !nameValid(registrationRequestID)) {
		fail(w, 400, "valid request, pool and host required")
		return
	}
	if receipt, ok := c.credentials.receipt(hostID, requestID, poolID, repositoryID, registrationRequestID); ok {
		write(w, http.StatusOK, receipt)
		return
	}
	fail(w, 503, "Core receipt has not been observed; retry after its next report")
}

// storageCredentialReceipts is a live Core observation used only to choose an explicitly
// eligible cleanup target. It is empty until the addressed Core has reported after startup.
func (c *Controller) storageCredentialReceipts(w http.ResponseWriter, r *http.Request, user, csrf string) {
	poolID, hostID := r.URL.Query().Get("pool_id"), r.URL.Query().Get("host_id")
	if !nameValid(poolID) || !nameValid(hostID) {
		fail(w, 400, "valid pool and host required")
		return
	}
	receipts, observed := c.credentials.eligibleReceipts(poolID, hostID)
	if !observed {
		fail(w, 503, "Core receipt inventory has not been observed; retry after its next report")
		return
	}
	write(w, http.StatusOK, receipts)
}

// agentStorageCredential returns an offered secret only over the enrolled host's authenticated
// connection. It intentionally has no normal command or database path.
func (c *Controller) agentStorageCredential(w http.ResponseWriter, r *http.Request, host string) {
	handoff, err := c.credentials.redeem(r.PathValue("handle"), host)
	if err != nil {
		fail(w, 404, "credential offer unavailable")
		return
	}
	write(w, http.StatusOK, handoff)
}

func (c *Controller) acknowledgeStorageCredential(w http.ResponseWriter, r *http.Request, host string) {
	var receipt protocol.StorageCredentialReceipt
	if !decodeLimit(w, r, &receipt, 1<<20) {
		return
	}
	if err := c.credentials.acknowledge(r.PathValue("handle"), host, receipt); err != nil {
		fail(w, 409, err.Error())
		return
	}
	write(w, http.StatusOK, receipt)
}
