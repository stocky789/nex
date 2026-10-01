// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"
)

func managedCommand(kind string) bool {
	switch kind {
	case "storage-register", "storage-update", "storage-recheck", "storage-detach", "storage-credential-retire", "storage-zfs-provision", "storage-zfs-replace", "network-register", "network-remove", "network-update", "media-remove", "iso-import", "vm-create", "vm-update", "snapshot-create", "snapshot-revert", "snapshot-delete", "snapshot-metadata", "backup-now", "backup-restore-as-new", "backup-restore-replace", "backup-test-restore", "backup-repository-register", "backup-repository-test", "backup-repository-forget", "backup-orphan-cleanup", "backup-policy-upsert", "backup-policy-pause", "backup-policy-delete", "backup-policy-run-now", "backup-protect", "backup-catalog-import", "backup-catalog-rebuild", "backup-test-verify", "backup-test-cleanup", "vm-power", "vm-delete", "vm-media", "vm-release-retained", "pool-member-join", "pool-storage-reconcile", "pool-join", "pool-maintenance", "pool-transfer", "pool-promote", "pool-fence-profile", "pool-bmc-provisioning", "pool-host-fencing", "pool-update", "management-network", "host-system-disk-replace":
		return true
	case "host-image-update":
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

// startBundledSeed runs seed in the background unless a previous seed is still running.
func startBundledSeed(running *atomic.Bool, seed func()) {
	if !running.CompareAndSwap(false, true) {
		return
	}
	go func() {
		defer running.Store(false)
		seed()
	}()
}

type managedCompletion struct {
	result     protocol.Result
	err        error
	background bool
}

// defaultsInterval is how often the worker retries default storage/network registration
// (including when the default storage directory was missing at enroll) and looks for new uplinks.
const defaultsInterval = time.Minute

// This goroutine executes commands; the bundled media seed below is the one other mutator and
// journals only under the managed engine's operations lock. It owns no Store data: the heartbeat
// loop persists dispatch and completion, so long disk/download work cannot race
// identity persistence or prevent reports. It also owns the host's default storage and
// untagged networks: they are self-registered at start and whenever the host's bridges and
// their physical ports change, and the Controller only mirrors what the journal reports.
// Bundled installer media such as virtio-win is seeded on every tick until published, in its
// own goroutine so the first copy and hash never delay commands; its journal decision is
// retaken under the operations lock that command execution holds.
func managedWorker(ctx context.Context, dir string, identity State, commands <-chan protocol.Command, completed chan<- managedCompletion) {
	engine := vmhost.NewManaged(filepath.Join(dir, "managed-vms"))
	var seeding atomic.Bool
	defaults := func() {
		nics, err := HostNICs()
		if err != nil {
			log.Printf("default resources: %v", err)
			return
		}
		// Call every tick: RegisterDefaults is idempotent once journals hold the path or
		// bridge, and must retry when the default storage directory appears after enroll.
		if err := engine.RegisterDefaults(ctx, cmp.Or(identity.PoolID, identity.HostID), identity.HostID, nics); err != nil {
			log.Printf("default resources: %v", err)
			return
		}
		startBundledSeed(&seeding, func() {
			if err := engine.SeedBundledMedia(ctx, cmp.Or(identity.PoolID, identity.HostID), identity.HostID); err != nil && ctx.Err() == nil {
				log.Printf("bundled media: %v", err)
			}
		})
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
			// Pool ZFS provisioning survives Controller loss. Resume only the
			// master-owned durable record; its provisioner re-adopts tagged pools
			// and skips successful hosts, so this cannot repeat a wipe.
			if resumed, err := ResumePoolZFSOperations(ctx, memberStore); err != nil && ctx.Err() == nil {
				log.Printf("pool ZFS provisioning did not complete; inspect the Core journal")
			} else {
				for _, result := range resumed {
					select {
					case completed <- managedCompletion{result: result, background: true}:
					case <-ctx.Done():
						return
					}
				}
			}
			// Settle an unresolved remote-storage VM update from native observation on every
			// pool host, single-host pools included, before publishing: an applied revision
			// then reaches the master's commit through this round's inventory evidence.
			if _, err := Membership(memberStore); err == nil {
				if err := reconcileRemoteVMUpdate(ctx, memberStore, engine); err != nil {
					log.Printf("remote VM update reconciliation did not complete; inspect the Core journal")
				}
			}
			if completion, ready, err := reconcilePendingResourceRemoval(ctx, memberStore, engine); err != nil {
				log.Printf("pool resource removal reconciliation is waiting for authority")
			} else if ready {
				select {
				case completed <- completion:
				case <-ctx.Done():
					return
				}
			}
			_ = PublishPoolVMInventory(ctx, memberStore)
			_ = ReconcilePoolVMAutostart(ctx, memberStore)
			// Retry NFS/SMB mounts that failed at boot on every host, including joined pools.
			engine.StartRemoteMounts(ctx)
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
			if lifecycleResult, handled, lifecycleErr := ExecutePoolZFSLifecycle(ctx, &Store{dir: dir, State: identity}, cmd); handled {
				result = lifecycleResult
				err = lifecycleErr
			} else if (!poolAddressedCommand(cmd.Kind) && commandHost(cmd) != identity.HostID) || identity.HostID == "" {
				result.Error = "managed command host identity does not match enrolled host"
			} else {
				if cmd.Pool != nil {
					if cmd.Kind == "pool-maintenance" {
						// Master-owned drain intent: one durable membership field,
						// no native work, and a definite result either way.
						result = ExecutePoolMaintenance(&Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-update" {
						// The master's own multi-step job: a durable record the coordinator
						// drives with no Controller connection; this command only asks.
						result = ExecutePoolUpdate(ctx, &Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-promote" {
						// A fence-verified promotion, driven by the candidate itself:
						// the same durable, resumable machine, proved before it moves.
						result = ExecutePoolPromotion(ctx, &Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-host-fencing" {
						// One master-owned pool setting: whether Nex Core may arm
						// this pool's hosts to shut themselves out.
						result = ExecutePoolHostFencing(&Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-bmc-provisioning" {
						// One master-owned membership field: whether this host may
						// configure its own management controller for fencing.
						result = ExecutePoolBMCProvisioning(&Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-fence-profile" {
						// One master-owned membership field: how this pool would exclude
						// a host, recorded before it has to be excluded.
						result = ExecutePoolFenceProfile(&Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-transfer" {
						// A planned master change or an eject: a durable, resumable Core
						// state machine. This worker executes one command at a time, so
						// no other authority write of this host is in flight while it
						// freezes.
						result = ExecutePoolTransfer(ctx, &Store{dir: dir, State: identity}, cmd)
					} else if cmd.Kind == "pool-storage-reconcile" && cmd.Pool.Action == "storage-reconcile" {
						memberStore := &Store{dir: dir, State: identity}
						var masterURL string
						masterURL, err = PoolMasterURL(memberStore)
						if err == nil {
							err = ReconcilePoolStorage(ctx, memberStore, masterURL, cmd.Pool.RepositoryID)
						}
					} else {
						err = vmhost.HandlePoolCommand(ctx, dir, identity.HostID, cmd)
					}
					// Maintenance and a transfer already produced their own definite
					// result; every other pool command reports success by returning no
					// error.
					if err == nil && cmd.Kind != "pool-maintenance" && cmd.Kind != "pool-transfer" && cmd.Kind != "pool-promote" && cmd.Kind != "pool-fence-profile" && cmd.Kind != "pool-bmc-provisioning" && cmd.Kind != "pool-host-fencing" && cmd.Kind != "pool-update" {
						result = protocol.Result{OperationID: cmd.OperationID, Status: "succeeded", ObservedAt: time.Now().UTC()}
					}
				} else if cmd.Kind == "host-system-disk-replace" {
					// Host-local and pool-free: this host's own mirror, its own disks
					// and its own bootloader.
					result = ExecuteSystemDiskReplace(ctx, dir, cmd)
				} else if cmd.Kind == protocol.ImageUpdateCommandKind {
					// Host-scoped and pool-free: this host's own update engine owns the work and
					// the record; the result only says whether it accepted the request.
					result = ExecuteImageUpdate(ctx, cmd)
				} else if cmd.Kind == "management-network" {
					// Host-scoped and pool-free: this host's own /etc files and its
					// durable staged record, with no membership or storage fence.
					result = ExecuteManagementNetwork(ctx, dir, cmd)
				} else if cmd.Kind == "storage-zfs-provision" {
					result, err = ExecutePoolZFSProvision(ctx, &Store{dir: dir, State: identity}, cmd)
				} else if cmd.Kind == "storage-zfs-replace" {
					result, err = ExecutePoolZFSReplace(ctx, &Store{dir: dir, State: identity}, cmd)
				} else {
					memberStore := &Store{dir: dir, State: identity}
					// Core decides from its own journal how a media change is applied: on a
					// pool-shared VM one that rewrites the durable definition becomes the
					// master-reserved vm-update transition, before any reservation,
					// admission or storage fence sees the command.
					cmd = PoolVMMediaCommand(ctx, memberStore, cmd)
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
					var finishResourceMutation func(bool, bool)
					var finishVMExecution func(protocol.Result, error) error
					executed := false
					replayBlocked := false
					if err == nil {
						// One guard for every Controller-driven pool registry change this
						// host performs. VM commands are deliberately absent: they are
						// guarded inside the durable ownership record instead, so a stop,
						// a shutdown or a force-stop is never blocked by a master change.
						err = poolRegistryCommandWritable(memberStore, cmd)
					}
					if err == nil {
						finishMutation, err = BeginPoolStorageMutation(ctx, memberStore, cmd)
					}
					if err == nil {
						finishResourceMutation, err = BeginPoolResourceRemoval(ctx, memberStore, cmd)
					}
					if err == nil {
						finishVMExecution, err = AdmitPoolVMExecution(ctx, memberStore, cmd)
					}
					executionCtx := ctx
					if err == nil {
						executionCtx, err = poolVMCommandExecutionContext(ctx, memberStore, cmd)
					}
					if err == nil && cmd.Kind == "snapshot-metadata" {
						executed = true
						result, err = executeSnapshotMetadata(executionCtx, memberStore, cmd)
					}
					// A redelivered exact definition command may already have a durable
					// pending or terminal journal entry. Do not turn a fresh placement or
					// clustered-storage refusal into a new definite failure, but do not
					// call Engine.Execute with a bare context: that could reconcile a
					// native mutation without the fencing admission it still requires.
					if err != nil && (cmd.Kind == "vm-create" || cmd.Kind == "vm-update") {
						if journaled, ok := engine.JournaledCommand(cmd.OperationID); ok && reflect.DeepEqual(journaled, cmd) {
							result.Status, result.Error = "needs_attention", err.Error()
							replayBlocked, err = true, nil
						} else if !ok {
							// The master may have reserved this operation before its reply was
							// lost. Journal the refusal so the next inventory proves it failed
							// unapplied and the master releases that reservation.
							_ = engine.RecordRefusal(ctx, cmd, err.Error())
						}
					}
					if err == nil && !replayBlocked && cmd.Kind != "snapshot-metadata" {
						executed = true
						result, err = engine.Execute(executionCtx, cmd, mediaFetcher(identity))
					}
					if finishVMExecution != nil && !replayBlocked {
						if finishErr := finishVMExecution(result, err); finishErr != nil && err == nil {
							// The host-side journal may prove a local mutation or a
							// local failure, but the master did not confirm the matching
							// durable owner transition. Keep every such ambiguity visible
							// and fail closed rather than leaving a stuck reservation
							// hidden behind a terminal native result.
							result.Status, result.Error = "needs_attention", finishErr.Error()
						}
					}
					if finishMutation != nil && !replayBlocked {
						finishMutation(err == nil && (result.Status == "succeeded" || result.Status == "failed"))
					}
					if finishResourceMutation != nil && !replayBlocked {
						finishResourceMutation(executed, !executed || (err == nil && (result.Status == "succeeded" || result.Status == "failed")))
					}
					if reserved && !replayBlocked && (err != nil || result.Status != "succeeded") {
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
			case completed <- managedCompletion{result: result, err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

// reconcilePendingResourceRemoval resumes one exact Core journal entry after a
// restart or lost reply. A fresh master check and replica acknowledgement happen
// before Engine.Execute can replay native effects; ambiguous outcomes keep the
// replicated marker and the managed journal for another pass.
func reconcilePendingResourceRemoval(ctx context.Context, s *Store, engine *vmhost.ManagedEngine) (managedCompletion, bool, error) {
	membership, err := Membership(s)
	if err != nil || membership.MasterHostID != s.State.HostID {
		return managedCompletion{}, false, nil
	}
	pending, err := engine.PendingResourceRemoval(ctx, s.State.PoolID, s.State.HostID)
	if err != nil {
		return managedCompletion{}, false, err
	}
	if pending != nil {
		if err = validateLocalResourceRemovalOwner(s, membership, pending.Command); err != nil {
			return managedCompletion{}, false, err
		}
		finish, beginErr := BeginPoolResourceRemoval(ctx, s, pending.Command)
		if beginErr != nil {
			return managedCompletion{}, false, beginErr
		}
		result, executeErr := engine.Execute(ctx, pending.Command, mediaFetcher(s.State))
		definitive := executeErr == nil && (result.Status == "succeeded" || result.Status == "failed")
		finish(true, definitive)
		return managedCompletion{result: result, err: executeErr, background: true}, true, nil
	}

	// A process can exit after Engine.Execute durably records a terminal result
	// but before the pool marker is cleared. Inspect only operations named by an
	// exact persisted marker; terminal journal history without a marker is inert.
	poolStorageMu.Lock()
	storage, storageErr := loadPoolStorage(s)
	poolStorageMu.Unlock()
	if storageErr != nil {
		return managedCompletion{}, false, storageErr
	}
	keys := make([]string, 0, len(storage.Mutations))
	for key := range storage.Mutations {
		if strings.HasPrefix(key, poolResourceMutationPrefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		operationID := storage.Mutations[key]
		outcome, outcomeErr := engine.ResourceRemovalOutcome(ctx, operationID)
		if outcomeErr != nil {
			return managedCompletion{}, false, outcomeErr
		}
		if outcome == nil {
			continue
		}
		if err = validateLocalResourceRemovalOwner(s, membership, outcome.Command); err != nil {
			return managedCompletion{}, false, err
		}
		request := outcome.Command.ResourceLifecycle
		if key != poolResourceMutationKey(request.Resource, request.ResourceID) || operationID != outcome.Command.OperationID {
			return managedCompletion{}, false, errors.New("resource-removal marker does not match its terminal journal")
		}
		if err = settleTerminalPoolResourceMutation(s, key, operationID); err != nil {
			return managedCompletion{}, false, err
		}
		return managedCompletion{result: outcome.Result, background: true}, true, nil
	}
	return managedCompletion{}, false, nil
}

func validateLocalResourceRemovalOwner(s *Store, membership protocol.PoolMembership, cmd protocol.Command) error {
	request := cmd.ResourceLifecycle
	if request == nil || (cmd.Kind != "network-remove" && cmd.Kind != "media-remove") || request.Action != "remove" ||
		request.PoolID != s.State.PoolID || request.HostID != s.State.HostID || request.PoolID != membership.PoolID ||
		request.HostID != membership.MasterHostID || membership.MasterHostID != s.State.HostID {
		return errors.New("resource-removal journal does not belong to this pool master")
	}
	return nil
}

func settleTerminalPoolResourceMutation(s *Store, key, operationID string) error {
	poolVMMu.Lock()
	poolPeerMu.Lock()
	poolStorageMu.Lock()
	defer poolStorageMu.Unlock()
	defer poolPeerMu.Unlock()
	defer poolVMMu.Unlock()
	peer, err := loadPoolPeer(s)
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID || peer.Membership.PoolID != s.State.PoolID || poolAuthorityWritable(peer) != nil {
		return errors.New("pool authority is unavailable for terminal resource-removal reconciliation")
	}
	storage, err := loadPoolStorage(s)
	if err != nil || storage.Mutations[key] != operationID {
		return errors.New("resource-removal marker changed before terminal reconciliation")
	}
	delete(storage.Mutations, key)
	if err = savePoolStorage(s, storage); err != nil {
		return err
	}
	return nil
}

// osHostname is what this host calls itself, and what it reports as Inventory.Hostname; tests
// replace it to exercise the apply confirmation without renaming the machine.
var osHostname = os.Hostname

// ExecuteManagementNetwork performs one step of this host's staged management-network edit.
// Every refusal is a definite failed result carrying Core's own sentence, never a local
// execution error: a Controller maps a result whose error ends in
// protocol.ManagementNetworkConflict to 409 and anything else to a plain failure.
func ExecuteManagementNetwork(ctx context.Context, dir string, cmd protocol.Command) protocol.Result {
	result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", ObservedAt: time.Now().UTC()}
	v := cmd.ManagementNetwork
	if v == nil {
		result.Error = "management network command carries no request"
		return result
	}
	if v.OperationID != cmd.OperationID {
		result.Error = "management network request does not match its command"
		return result
	}
	if err := v.Valid(); err != nil {
		result.Error = err.Error()
		return result
	}
	var err error
	// Two identities, deliberately: the command's own OperationID is the managed journal's key,
	// so a redelivered command replays its recorded result instead of acting twice, while the
	// change the engine acts on is named by ChangeID. Staging creates the change, so there they
	// are the same; every later step names the change the host itself recorded and reported.
	switch v.Action {
	case "stage":
		if err = managementNetworkUnarmed(ctx); err == nil {
			_, err = vmhost.StageManagementNetwork(dir, v.Spec, v.OperationID, v.ExpectedRevision, time.Duration(v.WindowSeconds)*time.Second)
		}
	case "apply":
		// The one edit that can strand a host names its target explicitly, and the host
		// itself checks the name: a Controller mirror can be stale, the hostname cannot.
		if err = managementNetworkUnarmed(ctx); err == nil {
			if err = confirmsThisHost(v.Confirmation); err == nil {
				var rec *vmhost.ManagementNetworkRecord
				if rec, err = vmhost.ApplyManagementNetwork(ctx, dir, v.ChangeID); err == nil {
					armManagementRollback(dir, rec.Deadline)
				}
			}
		}
	case "confirm":
		_, err = vmhost.ConfirmManagementNetwork(dir, v.ChangeID)
	case "cancel":
		// Discards a staged change, or rolls an applied one back early at the operator's
		// request. The rollback bounces the management interface, so this result may not
		// reach the Controller until the host reports again on the previous configuration;
		// the managed journal keeps it until then.
		_, err = vmhost.CancelManagementNetwork(ctx, dir, v.ChangeID)
	}
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Status = "succeeded"
	return result
}

// managementNetworkUnarmed refuses a management-network edit on a host that is feeding a
// watchdog. The pool's Corosync ring runs on the management network, because that is where the
// pool's own traffic runs, so bouncing that interface is a partition — and an armed host on the
// minority side of a partition resets itself. That is planned work resetting a healthy host,
// which is the one thing watchdog fencing must never do.
//
// It asks systemd whether sbd is running rather than reading the pool's record, because the
// question is about this machine at this moment: a record can be stale, a running daemon
// holding a watchdog device cannot.
func managementNetworkUnarmed(ctx context.Context) error {
	armed, err := localWatchdogArmed(ctx)
	if err != nil {
		return errors.New("Nex Core could not read whether this host is feeding a watchdog, so it will not change the network its pool's cluster runs on")
	}
	if armed {
		return errors.New("this host is feeding a watchdog that resets it if it loses this pool's quorum, and this pool's cluster runs on the management network. Turn Host fencing off for this pool, or put this host in maintenance, before changing its management network")
	}
	return nil
}

func confirmsThisHost(confirmation string) error {
	name, err := osHostname()
	if err != nil || name == "" {
		return errors.New("this host cannot read its own name to check the confirmation")
	}
	if !strings.EqualFold(strings.TrimSpace(confirmation), name) {
		return errors.New("the confirmation does not name this host")
	}
	return nil
}

// armManagementRollback makes the deadline exact for as long as this process lives. It is an
// optimization, not the guarantee: the deadline is durable, and RecoverManagementNetwork runs
// at agent start and on every probe tick regardless. Re-arming is harmless because recovery
// takes the record lock and does nothing unless the change is still applied and out of time.
func armManagementRollback(dir string, deadline time.Time) {
	if deadline.IsZero() {
		return
	}
	time.AfterFunc(time.Until(deadline)+time.Second, func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = vmhost.RecoverManagementNetwork(ctx, dir)
	})
}

// BeginPoolResourceRemoval is the master-side, pool-wide refusal for a Controller-issued
// pool network or ISO removal or rename. Core's own dependency check sees only this host's
// VM definitions, so the master also reads the pool's durable VM inventory in pool-vms.json
// and refuses while any VM on any other host still references the resource. It fails closed
// on anything it cannot prove: no membership, no inventory, or a ready member that has not
// published at the current membership generation. Only the master accepts these commands;
// a member's own converge loop applies the master's decision through the engine directly
// and is deliberately not fenced here.
func BeginPoolResourceRemoval(ctx context.Context, s *Store, cmd protocol.Command) (func(bool, bool), error) {
	v := cmd.ResourceLifecycle
	if v == nil {
		return func(bool, bool) {}, nil
	}
	noun := "network"
	if v.Resource == "media" {
		noun = "ISO"
	}
	membership, err := Membership(s)
	if err != nil {
		return nil, errors.New("pool membership is unavailable for a pool " + noun + " change")
	}
	if membership.MasterHostID != s.State.HostID {
		return nil, errors.New("only the pool master may remove or rename a pool " + noun)
	}
	if v.Action != "remove" {
		// A rename moves a name and a generation; no VM definition references it.
		return func(bool, bool) {}, nil
	}
	// A master change leaves the new master holding ISO records it adopted from shared
	// storage while it was a member. Removal never unlinks an adopted file — the pool's
	// only copy belongs to the host that imported it — so removing one here would drop the
	// pool's record and leave the file behind with nothing to remove it. Say so instead.
	if v.Resource == "media" && vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).MediaAdoptedHere(v.ResourceID) {
		return nil, errors.New("this pool master adopted this ISO from shared storage rather than importing it; the file stays on shared storage, so remove it from the host that imported it")
	}
	incomplete := errors.New("pool VM inventory is incomplete; wait for every member to report before removing this " + noun)
	poolVMMu.Lock()
	state, err := loadPoolVMs(s)
	if err != nil {
		poolVMMu.Unlock()
		return nil, incomplete
	}
	for _, vm := range state.VMs {
		if validatePoolVMSnapshotCatalog(vm) != nil {
			poolVMMu.Unlock()
			return nil, errors.New("pool VM snapshot catalog is invalid")
		}
		if poolVMSnapshotUsesResource(vm, v.Resource, v.ResourceID) {
			poolVMMu.Unlock()
			return nil, errors.New("resource is required by a retained VM snapshot")
		}
		if poolVMUsesResource(vm.Spec, v.Resource, v.ResourceID) || (vm.PendingUpdate != nil && poolVMUsesResource(vm.PendingUpdate.Next, v.Resource, v.ResourceID)) {
			poolVMMu.Unlock()
			return nil, errors.New("resource is still referenced by a pool VM definition")
		}
	}
	for _, member := range membership.Members {
		if member.HostID == s.State.HostID {
			continue
		}
		if member.Status == "ejected" {
			continue
		}
		inventory, reported := state.Inventory[member.HostID]
		if !reported || inventory.HostID != member.HostID || (member.Status == "ready" && inventory.Generation != membership.Generation) {
			poolVMMu.Unlock()
			return nil, incomplete
		}
		for _, vm := range inventory.VMs {
			if poolVMUsesResource(vm, v.Resource, v.ResourceID) {
				poolVMMu.Unlock()
				if noun == "network" {
					return nil, errors.New("network cannot be removed while used by VM " + vm.ID + " on host " + member.HostID)
				}
				return nil, errors.New("ISO cannot be removed while attached to VM " + vm.ID + " on host " + member.HostID)
			}
		}
	}

	// VM admission, authority freeze, and resource removal use VM → peer → storage lock
	// order. The durable marker closes the gap after this check and before native removal.
	poolPeerMu.Lock()
	peer, peerErr := loadPoolPeer(s)
	if peerErr != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID || peer.Membership.Generation != membership.Generation {
		poolPeerMu.Unlock()
		poolVMMu.Unlock()
		return nil, errors.New("pool authority changed before resource removal could be reserved")
	}
	if peer.Transfer != nil && (peer.Transfer.Frozen || peer.Transfer.HandedOff) {
		poolPeerMu.Unlock()
		poolVMMu.Unlock()
		return nil, errors.New("pool authority is frozen for transfer")
	}
	poolStorageMu.Lock()
	storage, storageErr := loadPoolStorage(s)
	key := poolResourceMutationKey(v.Resource, v.ResourceID)
	newlyReserved := false
	if storageErr == nil && storage.Mutations[key] != "" && storage.Mutations[key] != cmd.OperationID {
		storageErr = errors.New("another resource removal is pending")
	}
	if storageErr == nil && storage.Mutations[key] == "" {
		storage.Mutations[key] = cmd.OperationID
		newlyReserved = true
		if _, validationErr := poolResourceMutationsFromStorage(storage); validationErr != nil {
			delete(storage.Mutations, key)
			storageErr = errors.New("pool resource mutation limit or record validation failed")
		} else {
			storageErr = savePoolStorage(s, storage)
		}
	}
	poolStorageMu.Unlock()
	poolPeerMu.Unlock()
	poolVMMu.Unlock()
	if storageErr != nil {
		return nil, errors.New("pool resource removal state is unavailable: " + storageErr.Error())
	}
	if err := confirmPoolResourceMutationReplica(ctx, s, membership, v.Resource, v.ResourceID, cmd.OperationID); err != nil {
		// This invocation has not entered the native engine, so a newly written marker
		// can be safely retired after an admission/replication refusal. A marker left by
		// an earlier ambiguous operation is never cleared by a failed retry.
		if newlyReserved {
			finishPoolStorageMutation(s, key, cmd.OperationID, true)
		}
		return nil, err
	}
	return func(executed, definitive bool) {
		finishPoolStorageMutation(s, key, cmd.OperationID, definitive && (executed || newlyReserved))
	}, nil
}

// confirmPoolResourceMutationReplica proves every ready member has stored the exact
// marker before the caller can begin native removal. Push is single-flight and may return
// early, so only a matching post-ACK is evidence. Locks are released during peer I/O, then
// the marker, authority and membership are rechecked before returning.
func confirmPoolResourceMutationReplica(ctx context.Context, s *Store, membership protocol.PoolMembership, resource, id, operation string) error {
	_ = PushPoolAuthorityReplicas(ctx, s, 20*time.Second)
	replica, digest, err := buildPoolAuthorityReplica(s)
	if err != nil {
		return errors.New("pool resource removal authority replica is unavailable")
	}
	poolPeerMu.Lock()
	peer, err := loadPoolPeer(s)
	poolPeerMu.Unlock()
	if err != nil || peer.Membership.Valid() != nil || peer.Membership.MasterHostID != s.State.HostID ||
		peer.Membership.Generation != membership.Generation || peer.AuthorityGeneration == 0 || peer.AuthorityDigest != digest {
		return errors.New("pool resource removal authority changed before members acknowledged it")
	}
	replica.Generation = peer.AuthorityGeneration
	for _, member := range peer.Membership.Members {
		if member.HostID == s.State.HostID || member.Status != "ready" {
			continue
		}
		if member.PeerOrigin == "" || !poolReplicaPostAckMatches(s, member.HostID, replica.Generation, digest) {
			return errors.New("every ready pool member must acknowledge the resource removal marker before native removal")
		}
	}

	poolVMMu.Lock()
	poolPeerMu.Lock()
	poolStorageMu.Lock()
	currentPeer, peerErr := loadPoolPeer(s)
	owners, ownerErr := loadPoolVMs(s)
	storage, storageErr := loadPoolStorage(s)
	currentMutations, mutationErr := poolResourceMutationsFromStorage(storage)
	key := poolResourceMutationKey(resource, id)
	vmMatches := false
	if ownerErr == nil {
		vmBytes, marshalErr := json.Marshal(owners.VMs)
		vmMatches = marshalErr == nil && string(vmBytes) == string(replica.VMs)
	}
	valid := peerErr == nil && ownerErr == nil && storageErr == nil && mutationErr == nil && vmMatches &&
		currentPeer.Membership.Valid() == nil && currentPeer.Membership.MasterHostID == s.State.HostID &&
		currentPeer.Membership.Generation == membership.Generation && currentPeer.AuthorityGeneration == replica.Generation &&
		currentPeer.AuthorityDigest == digest && poolAuthorityWritable(currentPeer) == nil &&
		storage.Mutations[key] == operation && reflect.DeepEqual(currentMutations, replica.ResourceMutations)
	poolStorageMu.Unlock()
	poolPeerMu.Unlock()
	poolVMMu.Unlock()
	if !valid {
		return errors.New("pool resource removal authority changed after member acknowledgement")
	}
	return nil
}

func poolVMUsesResource(vm protocol.VMCreateSpec, resource, id string) bool {
	if resource == "media" {
		return vm.MediaID == id || vm.DriversMediaID == id
	}
	for _, nic := range vm.NICs {
		if nic.NetworkID == id {
			return true
		}
	}
	return false
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

// reconcileRemoteVMUpdate revalidates this Core's identity and pool execution context for
// the update's VM before any observation. It deliberately does not use the define owner
// check: the master refuses it while the update it would settle is still pending.
func reconcileRemoteVMUpdate(ctx context.Context, s *Store, engine *vmhost.ManagedEngine) error {
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return engine.ReconcileRemoteVMUpdate(work, func(ctx context.Context, v protocol.VMCreateSpec) error {
		if v.ID == "" || v.PoolID != s.State.PoolID || v.HostID != s.State.HostID || protocol.ValidateVMSpec(v) != nil {
			return errors.New("remote VM definition no longer matches this Core identity")
		}
		_, err := poolVMExecutionContext(ctx, s, v)
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

// poolAddressedCommand names the commands the Controller sends to the pool master about
// the pool's repositories: the master names the host inside the request and reaches it
// itself. Every other managed command is addressed to the host that carries it out.
func poolAddressedCommand(kind string) bool {
	return kind == "storage-zfs-provision" || kind == "storage-zfs-replace"
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
	case "pool-member-join", "pool-storage-reconcile", "pool-maintenance", "pool-transfer", "pool-fence-profile", "pool-bmc-provisioning", "pool-host-fencing", "pool-update":
		// Maintenance, a master change, an eject and a fence profile are all performed by
		// the master about another host, so the command's target is the master, not that
		// host.
		if cmd.Pool != nil {
			return cmd.Pool.Membership.MasterHostID
		}
	case "pool-promote":
		// A promotion is the one transfer the master cannot perform: it is gone. The
		// command goes to the surviving member the operator selected.
		if cmd.Pool != nil {
			return cmd.Pool.MemberHostID
		}
	case "pool-join":
		// The only pool command addressed to a host that is not the master: the joining
		// host performs the whole exchange itself. Run executes it, not the worker,
		// because completing it rewrites the pool identity the worker snapshotted.
		if cmd.Pool != nil {
			return cmd.Pool.MemberHostID
		}
	case "management-network":
		if cmd.ManagementNetwork != nil {
			return cmd.ManagementNetwork.HostID
		}
	case protocol.ImageUpdateCommandKind:
		if cmd.ImageUpdate != nil {
			return cmd.ImageUpdate.HostID
		}
	case "host-system-disk-replace":
		if cmd.SystemDiskReplace != nil {
			return cmd.SystemDiskReplace.HostID
		}
	case "network-register":
		if cmd.Network != nil {
			return cmd.Network.HostID
		}
	case "network-remove", "network-update", "media-remove":
		if cmd.ResourceLifecycle != nil {
			return cmd.ResourceLifecycle.HostID
		}
	case "iso-import":
		if cmd.Media != nil {
			return cmd.Media.HostID
		}
	case "vm-create", "vm-update", "snapshot-create", "snapshot-revert", "snapshot-delete", "snapshot-metadata", "backup-now":
		if cmd.VM != nil {
			return cmd.VM.HostID
		}
	case "backup-restore-as-new":
		if cmd.Restore != nil {
			return "" // restore-as-new targets local host placement chosen later
		}
	case "backup-restore-replace", "backup-test-restore":
		return ""
	case "vm-power":
		if cmd.Power != nil {
			return cmd.Power.HostID
		}
	case "vm-delete":
		if cmd.Delete != nil {
			return cmd.Delete.HostID
		}
	case "vm-media":
		if cmd.VMMedia != nil {
			return cmd.VMMedia.HostID
		}
	case "vm-release-retained":
		if cmd.ReleaseRetained != nil {
			return cmd.ReleaseRetained.HostID
		}
	}
	return ""
}
