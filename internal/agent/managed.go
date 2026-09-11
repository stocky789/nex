// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"path/filepath"
	"strings"
	"time"
)

func managedCommand(kind string) bool {
	switch kind {
	case "storage-register", "storage-update", "storage-recheck", "storage-detach", "storage-credential-retire", "network-register", "iso-import", "vm-create", "vm-update", "vm-power", "vm-media":
		return true
	}
	return false
}

// Media stays on the enrolled, hostname-verified Controller connection. Neither
// command payloads nor HTTP redirects may choose another download destination.
func mediaFetcher(s State) func(context.Context, string) (io.ReadCloser, error) {
	return func(ctx context.Context, id string) (io.ReadCloser, error) {
		if id == "" || strings.ContainsAny(id, "/\\?#%") {
			return nil, errors.New("invalid media identity")
		}
		c, err := client(s, true)
		if err != nil {
			return nil, err
		}
		c.Timeout = 2 * time.Hour
		c.Transport.(*http.Transport).ResponseHeaderTimeout = 15 * time.Second
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(s.Controller, "/")+"/api/v1/agent/media/"+url.PathEscape(id), nil)
		if err != nil {
			c.CloseIdleConnections()
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			c.CloseIdleConnections()
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			c.CloseIdleConnections()
			return nil, fmt.Errorf("controller rejected media download (HTTP %d)", resp.StatusCode)
		}
		return &mediaBody{ReadCloser: resp.Body, client: c}, nil
	}
}

type mediaBody struct {
	io.ReadCloser
	client *http.Client
}

func (b *mediaBody) Close() error {
	err := b.ReadCloser.Close()
	b.client.CloseIdleConnections()
	return err
}

type managedCompletion struct {
	result protocol.Result
	err    error
}

// defaultsInterval is how often the worker looks for a new physical uplink to register.
const defaultsInterval = time.Minute

// Only this goroutine executes mutations. It owns no Store data: the heartbeat
// loop persists dispatch and completion, so long disk/download work cannot race
// identity persistence or prevent reports. It also owns the host's default storage and
// untagged networks: they are self-registered at start and whenever the host's bridges and
// their physical ports change, and the Controller only mirrors what the journal reports.
func managedWorker(ctx context.Context, dir string, identity State, commands <-chan protocol.Command, completed chan<- managedCompletion) {
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	seen := ""
	defaults := func() {
		nics, err := HostNICs()
		if err != nil {
			log.Printf("default resources: %v", err)
			return
		}
		key := fmt.Sprint(vmhost.DefaultNetworks(nics))
		if key == seen {
			return
		}
		if err := engine.RegisterDefaults(ctx, cmp.Or(identity.PoolID, identity.HostID), identity.HostID, nics); err != nil {
			log.Printf("default resources: %v", err)
			return
		}
		seen = key
	}
	defaults()
	ticker := time.NewTicker(defaultsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			defaults()
			if err := engine.ReconcileStorage(ctx); err != nil {
				// The retained Core journal carries the actionable detail; do not put
				// storage sources or credentials into the process log.
				log.Printf("storage reconciliation did not complete; inspect the Core journal")
			}
		case cmd := <-commands:
			result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
			var err error
			if commandHost(cmd) != identity.HostID || identity.HostID == "" {
				result.Error = "managed command host identity does not match enrolled host"
			} else {
				result, err = engine.Execute(ctx, cmd, mediaFetcher(identity))
			}
			select {
			case completed <- managedCompletion{result, err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func commandHost(cmd protocol.Command) string {
	switch cmd.Kind {
	case "storage-register":
		if cmd.Storage != nil {
			return cmd.Storage.HostID
		}
	case "storage-update", "storage-recheck", "storage-detach":
		if cmd.StorageLifecycle != nil {
			return cmd.StorageLifecycle.HostID
		}
	case "storage-credential-retire":
		if cmd.CredentialRetire != nil {
			return cmd.CredentialRetire.HostID
		}
	case "network-register":
		if cmd.Network != nil {
			return cmd.Network.HostID
		}
	case "iso-import":
		if cmd.Media != nil {
			return cmd.Media.HostID
		}
	case "vm-create", "vm-update":
		if cmd.VM != nil {
			return cmd.VM.HostID
		}
	case "vm-power":
		if cmd.Power != nil {
			return cmd.Power.HostID
		}
	case "vm-media":
		if cmd.VMMedia != nil {
			return cmd.VMMedia.HostID
		}
	}
	return ""
}
