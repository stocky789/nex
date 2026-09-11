// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

// redeemStorageCredentials persists each secret before acknowledging it. A failed or lost
// acknowledgement is safe: Core retries the same offer and SaveStorageCredential returns its
// original receipt after a keyed replay check.
func redeemStorageCredentials(ctx context.Context, s *Store, offers []protocol.StorageCredentialOffer) error {
	for _, offer := range offers {
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
			return fmt.Errorf("credential handoff failed: %w", err)
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
		receipt, err := vmhost.SaveStorageCredential(s.dir, handoff)
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
			return fmt.Errorf("credential receipt acknowledgement failed: %w", err)
		}
	}
	return nil
}
