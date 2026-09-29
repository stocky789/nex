// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// redeemStorageCredentials persists each secret before acknowledging it. A failed or lost
// acknowledgement is safe: the Controller never hands a delivered offer out again, so the
// operator's request gets no receipt and retries after the offer expires; that new offer's
// keyed replay check in SaveStorageCredential returns the original receipt.
//
// A refused offer belongs to one operator request, not to Core persistence. Log it and keep
// redeeming the rest, so one stale or conflicting offer cannot stop the agent or the commands
// delivered in the same report.
func redeemStorageCredentials(ctx context.Context, s *Store, offers []protocol.StorageCredentialOffer) {
	for _, offer := range offers {
		if err := redeemStorageCredential(ctx, s, offer); err != nil {
			log.Printf("storage credential request %q not redeemed: %v", offer.RequestID, err)
		}
	}
}

func redeemStorageCredential(ctx context.Context, s *Store, offer protocol.StorageCredentialOffer) error {
	if offer.Handle == "" || offer.HostID != s.State.HostID || offer.PoolID != s.State.PoolID || offer.RequestID == "" || (offer.RepositoryID == "" && offer.RegistrationRequestID == "") || time.Now().After(offer.ExpiresAt) {
		return errors.New("invalid storage credential offer")
	}
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	path := "/api/v1/agent/storage/credentials/" + url.PathEscape(offer.Handle)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.State.Controller, "/")+path, nil)
	if err != nil {
		c.CloseIdleConnections()
		return err
	}
	resp, err := c.Do(req)
	if err != nil {
		c.CloseIdleConnections()
		return fmt.Errorf("credential handoff failed: %w", withoutRequestURL(err))
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		c.CloseIdleConnections()
		return fmt.Errorf("credential handoff rejected (HTTP %d)", resp.StatusCode)
	}
	var handoff protocol.StorageCredentialHandoff
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&handoff)
	resp.Body.Close()
	c.CloseIdleConnections()
	if err != nil || handoff.Offer != offer {
		return errors.New("invalid storage credential handoff")
	}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		return fmt.Errorf("storage credential persistence failed: %w", err)
	}
	ackPath := path + "/ack"
	c, err = client(s.State, true)
	if err != nil {
		return err
	}
	err = post(ctx, c, s.State.Controller, ackPath, receipt, &protocol.StorageCredentialReceipt{})
	c.CloseIdleConnections()
	if err != nil {
		return fmt.Errorf("credential receipt acknowledgement failed: %w", withoutRequestURL(err))
	}
	return nil
}

// withoutRequestURL drops the request URL a transport error repeats; it carries the one-shot
// offer handle, which must not reach logs.
func withoutRequestURL(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}

func clusterCredentialReceipt(s *Store, handoff protocol.StorageCredentialHandoff) (ClusterSecretReceipt, bool) {
	const prefix = "cluster-credential:"
	binding := handoff.Offer.RegistrationRequestID
	if !strings.HasPrefix(binding, prefix) {
		return ClusterSecretReceipt{}, false
	}
	parts := strings.Split(strings.TrimPrefix(binding, prefix), ":")
	if len(parts) != 3 || handoff.Offer.RepositoryID != "" || (parts[1] != "chap" && parts[1] != "fence_bmc" && parts[1] != "fence_ssh" && parts[1] != "bmc_probe") {
		return ClusterSecretReceipt{}, false
	}
	clusterRequestID, clusterErr := hex.DecodeString(parts[0])
	recipientHostID, recipientErr := hex.DecodeString(parts[2])
	if clusterErr != nil || recipientErr != nil || !clusterSecretID(string(clusterRequestID)) || !clusterSecretID(string(recipientHostID)) {
		return ClusterSecretReceipt{}, false
	}
	r := ClusterSecretReceipt{RequestID: handoff.Offer.RequestID, ClusterRequestID: string(clusterRequestID), PoolID: handoff.Offer.PoolID, HostID: handoff.Offer.HostID, RecipientHostID: string(recipientHostID), Kind: parts[1]}
	if r.Kind != "chap" {
		r.RecipientHostID, r.FenceTargetHostID = handoff.Offer.HostID, string(recipientHostID)
	}
	return r, true
}

// saveCredentialHandoff is the sole transient handoff persistence seam. Cluster
// offers use their purpose-bound marker; ordinary storage retains the existing
// receipt path unchanged.
func saveCredentialHandoff(s *Store, handoff protocol.StorageCredentialHandoff) (protocol.StorageCredentialReceipt, error) {
	if strings.HasPrefix(handoff.Offer.RegistrationRequestID, "backup-credential") {
		return saveBackupCredential(s, handoff)
	}
	if cluster, ok := clusterCredentialReceipt(s, handoff); ok {
		material := ClusterSecretMaterial{SSHPrivateKey: handoff.Credentials.SSHPrivateKey, SSHKnownHosts: handoff.Credentials.SSHKnownHosts}
		if cluster.Kind != "fence_ssh" {
			material.Credentials = &protocol.StorageCredentials{Username: handoff.Credentials.Username, Secret: handoff.Credentials.Secret}
		}
		stored, err := SaveClusterSecret(s, cluster, material)
		if err != nil {
			return protocol.StorageCredentialReceipt{}, err
		}
		return protocol.StorageCredentialReceipt{Receipt: stored.Reference, RequestID: handoff.Offer.RequestID, PoolID: handoff.Offer.PoolID, HostID: handoff.Offer.HostID, RegistrationRequestID: handoff.Offer.RegistrationRequestID, Status: "ready", CreatedAt: stored.CreatedAt}, nil
	}
	if strings.HasPrefix(handoff.Offer.RegistrationRequestID, "cluster-credential:") {
		return protocol.StorageCredentialReceipt{}, errors.New("invalid cluster credential offer")
	}
	return vmhost.SaveStorageCredential(s.dir, handoff)
}
