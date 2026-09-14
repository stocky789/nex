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
	case "storage-register", "storage-update", "storage-recheck", "storage-detach", "storage-credential-retire", "network-register", "iso-import", "vm-create", "vm-update", "vm-power", "vm-media", "pool-member-join", "pool-storage-reconcile":
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
			memberStore := &Store{dir: dir, State: identity}
			_ = PublishPoolVMInventory(ctx, memberStore)
			ReconcilePoolVMAutostart(ctx, memberStore)
			// Joined pools reconcile remote repositories through the master-owned desired
			// projection. Generic journal replay could otherwise bypass the peer mutation fence.
			if membership, err := Membership(memberStore); err == nil && len(membership.Members) > 1 {
				if err := reconcileRemoteVMCreateStopped(ctx, memberStore, engine); err != nil {
					log.Printf("remote VM definition reconciliation did not complete; inspect the Core journal")
				}
				continue
			}
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
				if cmd.Pool != nil {
					if cmd.Kind == "pool-storage-reconcile" && cmd.Pool.Action == "storage-reconcile" {
						memberStore := &Store{dir: dir, State: identity}
						var masterURL string
						masterURL, err = PoolMasterURL(memberStore)
						if err == nil {
							err = ReconcilePoolStorage(ctx, memberStore, masterURL, cmd.Pool.RepositoryID)
						}
					} else {
						err = vmhost.HandlePoolCommand(ctx, dir, identity.HostID, cmd)
					}
					if err == nil {
						result = protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
					}
				} else {
					memberStore := &Store{dir: dir, State: identity}
					reserved := false
					membership, membershipErr := Membership(memberStore)
					if membershipErr != nil && commandReferencesRemoteStorage(ctx, memberStore, cmd) {
						err = errors.New("pool membership is unavailable for remote storage operation")
					} else if membershipErr == nil && membership.MasterHostID != identity.HostID {
						if poolMemberStorageRegistrationDenied(membership, identity.HostID, cmd.Storage) {
							err = errors.New("only the pool master may register remote storage; wait for Core reconciliation")
						} else if err = ReservePoolStorage(ctx, memberStore, cmd); err == nil {
							reserved = true
						}
					}
					var finishMutation func(bool)
					var finishVMExecution func(protocol.Result, error) error
					executed := false
					if err == nil {
						finishMutation, err = BeginPoolStorageMutation(ctx, memberStore, cmd)
					}
					if err == nil {
						finishVMExecution, err = AdmitPoolVMExecution(ctx, memberStore, cmd)
					}
					executionCtx := ctx
					if err == nil {
						executionCtx, err = poolVMCommandExecutionContext(ctx, memberStore, cmd)
					}
					if err == nil {
						executed = true
						result, err = engine.Execute(executionCtx, cmd, mediaFetcher(identity))
					}
					if finishVMExecution != nil {
						if finishErr := finishVMExecution(result, err); finishErr != nil && err == nil {
							// The host-side journal may prove a local mutation or a
							// local failure, but the master did not confirm the matching
							// durable owner transition. Keep every such ambiguity visible
							// and fail closed rather than leaving a stuck reservation
							// hidden behind a terminal native result.
							result.Status, result.Error = "needs_attention", finishErr.Error()
						}
					}
					if finishMutation != nil {
						finishMutation(err == nil && (result.Status == "succeeded" || result.Status == "failed"))
					}
					if reserved && (err != nil || result.Status != "succeeded") {
						ReleasePoolStorageReservations(ctx, memberStore, cmd)
					}
					// A VM update can remove its last remote disk and a media eject has
					// no new MediaID to reserve. After any durable member mutation, derive
					// the remaining references before leaving an old fence behind.
					if shouldReleaseUnusedReservation(membership, membershipErr, identity.HostID, executed, err, result.Status) {
						ReleaseUnusedPoolStorageReservations(ctx, memberStore)
					}
					if !executed && err != nil {
						// Admission/fence refusal occurs before any Engine side effect or
						// journal mutation; report it as a definite failed command.
						result.Error, err = err.Error(), nil
					}
				}
			}
			select {
			case completed <- managedCompletion{result, err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// reconcileRemoteVMCreateStopped is intentionally a Core worker path, not a Controller
// retry. A remote-backed create that died while defining has already reserved a master-owned
// VM identity; only a current owner and storage validation may let its stopped definition be
// observed and settled. The vmhost method has no create/start/delete path.
func reconcileRemoteVMCreateStopped(ctx context.Context, s *Store, engine *vmhost.ManagedEngine) error {
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return engine.ReconcileRemoteVMCreateStopped(work, func(ctx context.Context, v protocol.VMCreateSpec) error {
		if v.ID == "" || v.PoolID != s.State.PoolID || v.HostID != s.State.HostID || protocol.ValidateVMSpec(v) != nil {
			return errors.New("remote VM definition no longer matches this Core identity")
		}
		observed, shared, err := poolSharedVM(ctx, s, protocol.Command{Kind: "vm-create", VM: &v})
		if err != nil {
			return err
		}
		if !shared || observed.ID != v.ID || observed.Revision != v.Revision || len(observed.Disks) != len(v.Disks) {
			return errors.New("remote VM storage is not currently eligible")
		}
		// define with create=false is the established idempotent owner check. It refuses
		// an absent or foreign master record and does not issue a permit or native action.
		if err = poolVMRequestPeer(ctx, s, v.ID, "define", nil, false, &v, nil); err != nil {
			return errors.New("remote VM is not currently owned by this Core")
		}
		_, err = poolVMExecutionContext(ctx, s, v)
		return err
	}, func(v protocol.VMCreateSpec) error {
		if !v.Autostart {
			return nil
		}
		// Suppress a later periodic autostart in this boot before exposing a recovered
		// stopped VM to Applied. A prior claim is already the desired suppression.
		_, err := ClaimPoolVMAutostartAttempt(s.dir, v.ID)
		return err
	})
}

func shouldReleaseUnusedReservation(membership protocol.PoolMembership, membershipErr error, hostID string, executed bool, executionErr error, status string) bool {
	return membershipErr == nil && membership.MasterHostID != hostID && executed && executionErr == nil && status == "succeeded"
}

func poolMemberStorageRegistrationDenied(membership protocol.PoolMembership, hostID string, storage *protocol.StorageRegistration) bool {
	return membership.MasterHostID != hostID && storage != nil && protocol.IsPoolSharedStorage(*storage)
}

func commandReferencesRemoteStorage(ctx context.Context, s *Store, cmd protocol.Command) bool {
	if cmd.Storage != nil && protocol.IsPoolSharedStorage(*cmd.Storage) {
		return true
	}
	ids := poolStorageCommandIDs(ctx, s, cmd)
	if len(ids) == 0 {
		return false
	}
	managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	for _, storage := range managed.Storage {
		for _, id := range ids {
			if storage.ID == id && protocol.IsPoolSharedStorage(storage) {
				return true
			}
		}
	}
	return false
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
	case "pool-member-join", "pool-storage-reconcile":
		if cmd.Pool != nil {
			return cmd.Pool.Membership.MasterHostID
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
