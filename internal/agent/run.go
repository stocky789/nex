// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http/httptrace"
	"net/netip"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/version"
	"nex.local/nex/internal/vmhost"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type persistenceError struct{ error }

var peerBackupRequests sync.Map // *Store -> chan BackupRequest

type preparedBackupRequest struct {
	request    BackupRequest
	journalID  string
	repository *protocol.BackupRepository
	err        error
}

type completedBackupVerification struct {
	request   BackupRequest
	journalID string
	plan      *testRestoreVerificationPlan
	err       error
}

type completedBackupCleanup struct {
	request   BackupRequest
	journalID string
	plan      *testRestoreCleanupPlan
	err       error
}

type directPeerBackupCompletion struct {
	peer peerBackupCompletion
}

type peerCleanupCompletion struct {
	work   peerCleanupWork
	result protocol.Result
	err    error
}

func backupNeedsPreparation(request BackupRequest) bool {
	if request.PeerRepository != nil {
		return true
	}
	return request.Command != nil && (request.Command.Kind == "backup-repository-register" || request.Command.Kind == "backup-repository-test")
}

func terminalBackupMetadataResponse(s *Store, request BackupRequest) (BackupResponse, bool) {
	if request.Command == nil || request.Command.Kind != "backup-test-cleanup" && request.Command.Kind != "backup-test-verify" {
		return BackupResponse{}, false
	}
	receipt, err := loadPoolBackupMetadataReceipt(s, request.Command.OperationID)
	if err != nil {
		return BackupResponse{}, false
	}
	intent, err := backupMetadataIntent(*request.Command)
	if err != nil || receipt.Kind != request.Command.Kind || receipt.IntentSHA256 != intent {
		return BackupResponse{Err: ErrBackupConflict}, true
	}
	if receipt.Status == "pending" {
		return BackupResponse{}, false
	}
	return BackupResponse{Result: &protocol.Result{OperationID: receipt.OperationID, Status: receipt.Status, Error: receipt.Error, ObservedAt: receipt.ObservedAt}}, true
}

func peerBackupRequest(ctx context.Context, s *Store, request BackupRequest) (BackupResponse, error) {
	queue, ok := peerBackupRequests.Load(s)
	if !ok {
		return BackupResponse{}, ErrBackupUnavailable
	}
	request.Reply = make(chan BackupResponse, 1)
	select {
	case queue.(chan BackupRequest) <- request:
	case <-ctx.Done():
		return BackupResponse{}, ctx.Err()
	}
	select {
	case response := <-request.Reply:
		return response, nil
	case <-ctx.Done():
		return BackupResponse{}, ctx.Err()
	}
}

type transientResultQueue[T any] struct {
	sync.Mutex
	byStore map[*Store][]T
	limit   int
}

func newTransientResultQueue[T any](limit int) transientResultQueue[T] {
	return transientResultQueue[T]{byStore: map[*Store][]T{}, limit: limit}
}

func (q *transientResultQueue[T]) drain(s *Store) []T {
	q.Lock()
	defer q.Unlock()
	out := q.byStore[s]
	delete(q.byStore, s)
	return out
}

func (q *transientResultQueue[T]) append(s *Store, results []T) {
	q.Lock()
	defer q.Unlock()
	room := q.limit - len(q.byStore[s])
	if room <= 0 {
		return
	}
	if len(results) > room {
		results = results[:room]
	}
	q.byStore[s] = append(q.byStore[s], results...)
}

var transientStorageDiscoveries = newTransientResultQueue[protocol.StorageDiscoveryResult](128)
var transientStorageClusters = newTransientResultQueue[protocol.StorageClusterResult](128)

// transientPoolResults answers the Controller's brokered pool requests on the next report.
// Nothing here is durable: the master's own offer record is the authority.
var transientPoolResults = newTransientResultQueue[protocol.PoolResult](32)

func resetReportExtensions(s *Store) error {
	if !s.State.StorageClusterProtocolReady && !s.State.StorageClusterSessionMonitorProtocolReady && !s.State.ISODownloadProgressReady && !s.State.DetectedOSReady {
		return nil
	}
	s.State.StorageClusterProtocolReady = false
	s.State.StorageClusterSessionMonitorProtocolReady = false
	s.State.ISODownloadProgressReady = false
	s.State.DetectedOSReady = false
	return s.Save()
}

func managedExtensionsForController(managed *protocol.ManagedState, isoReady, osReady bool) *protocol.ManagedState {
	if managed == nil {
		return nil
	}
	isoOK := isoReady || len(managed.ISODownloads) == 0
	osOK := osReady || len(managed.DetectedOS) == 0
	if isoOK && osOK {
		return managed
	}
	copy := *managed
	if !isoReady {
		copy.ISODownloads = nil
	}
	if !osReady {
		copy.DetectedOS = nil
	}
	return &copy
}

// storageClusterResultsForController is a report-only compatibility
// projection. Native peer state and the Core's durable operation are never
// changed for an older Controller; it simply cannot decode the additive member
// session-monitor field or its monitor-stage report.
func storageClusterResultsForController(results []protocol.StorageClusterResult, sessionMonitorReady bool) []protocol.StorageClusterResult {
	out := make([]protocol.StorageClusterResult, 0, len(results))
	for _, result := range results {
		copyResult := result
		copyResult.Operations = make([]protocol.StorageClusterOperation, 0, len(result.Operations))
		for _, operation := range result.Operations {
			copyOperation := operation
			copyOperation.Members = make([]protocol.StorageClusterMemberReport, 0, len(operation.Members))
			for _, member := range operation.Members {
				if !sessionMonitorReady && member.Stage == "monitor" {
					continue
				}
				copyMember := member
				if !sessionMonitorReady {
					copyMember.Status.SessionMonitorReady = false
				}
				copyOperation.Members = append(copyOperation.Members, copyMember)
			}
			copyResult.Operations = append(copyResult.Operations, copyOperation)
		}
		out = append(out, copyResult)
	}
	return out
}

// zfsPoolObservationForController is a report-only compatibility projection.
// Native Core observations stay intact; an older Controller that has not
// acknowledged storage-zfs-replace-v1 simply never sees Access.zfs_pool or
// ZFSHostAccess.pool.
func zfsPoolObservationForController(managed *protocol.ManagedState, ready bool) *protocol.ManagedState {
	if managed == nil || ready {
		return managed
	}
	copy := *managed
	copy.Storage = append([]protocol.StorageRegistration(nil), copy.Storage...)
	for i := range copy.Storage {
		if copy.Storage[i].Access.ZFSPool != nil {
			access := copy.Storage[i].Access
			access.ZFSPool = nil
			copy.Storage[i].Access = access
		}
		if len(copy.Storage[i].ZFSHostAccess) == 0 {
			continue
		}
		hosts := append([]protocol.ZFSHostAccess(nil), copy.Storage[i].ZFSHostAccess...)
		for j := range hosts {
			hosts[j].Pool = nil
		}
		copy.Storage[i].ZFSHostAccess = hosts
	}
	return &copy
}

type RunConfig struct {
	Heartbeat, ProbeInterval, ProbeTimeout time.Duration
	SnapshotRequests                       <-chan SnapshotRequest
	BackupRequests                         <-chan BackupRequest
	history                                *usageHistory // set by Run; answers stats channels
}

// ApplianceJournalDir is the local appliance engine's deploy journal (nex-core-local's
// data directory plus /vmhost). Its deployments are reported as this host's appliances.
// The packaged default matches nex-core-local's default data directory; nex-core-local
// itself sets it from its configuration before starting the embedded agent.
var ApplianceJournalDir = "/var/lib/nex/core-local/vmhost"

// PoolPeerListen is the address this host's Core-to-Core peer listener is bound to, which
// is also the origin the master hands a joining host. The commands set it from their
// --pool-peer-listen flag before starting the agent.
var PoolPeerListen = ":" + strconv.Itoa(protocol.DefaultPoolPeerPort)

func completeJournal(s *Store, inv *protocol.Inventory, timeout time.Duration) {
	for id, e := range s.State.Journal {
		if e.Status != "pending" || (e.Command != nil && managedCommand(e.Command.Kind)) {
			continue
		}
		if inv != nil && inv.ObservedAt.After(e.RequestedAt) {
			e.Status = "succeeded"
			e.ObservedAt = inv.ObservedAt
		} else if time.Since(e.RequestedAt) > timeout {
			e.Status = "failed"
			e.Error = "fresh host inventory unavailable before probe deadline"
			e.ObservedAt = time.Now().UTC()
		}
		s.State.Journal[id] = e
	}
}
func applyResponse(s *Store, r protocol.ReportResponse) error {
	return applyResponseWithPeer(s, r, false)
}

func applyResponseWithPeer(s *Store, r protocol.ReportResponse, peer bool) error {
	var order uint64
	for _, entry := range s.State.Journal {
		order = max(order, entry.Order)
	}
	for _, id := range r.AcknowledgedResults {
		e, ok := s.State.Journal[id]
		if ok && e.Status != "pending" {
			e.Acknowledged = true
			s.State.Journal[id] = e
		}
	}
	for _, cmd := range r.Commands {
		if cmd.OperationID == "" || len(cmd.OperationID) > 128 {
			return errors.New("invalid controller command ID")
		}
		if storageRepositoryCommand(cmd) && !s.State.StorageProtocolReady {
			return errors.New("controller sent a storage repository command before protocol preflight completed")
		}
		if cmd.Kind == "storage-zfs-provision" && !s.State.ZFSProvisionProtocolReady {
			return errors.New("controller sent a ZFS provision command before ZFS protocol preflight completed")
		}
		if old, ok := s.State.Journal[cmd.OperationID]; ok {
			if old.Command != nil {
				a, _ := json.Marshal(old.Command)
				b, _ := json.Marshal(cmd)
				if string(a) != string(b) {
					return errors.New("controller reused command ID with different input")
				}
			} else if cmd.Kind != "refresh-inventory" {
				return errors.New("controller reused legacy command ID")
			}
			if peer {
				old.PeerDispatched = true
				s.State.Journal[cmd.OperationID] = old
			}
			// Exact Controller replay of a snapshot that needs attention re-enters
			// observation/settle without requiring the snapshot HTTP path forever.
			if old.Status == "needs_attention" && old.Command != nil && snapshotCommand(old.Command.Kind) {
				old.Status, old.Error = "pending", ""
				s.State.Journal[cmd.OperationID] = old
			}
			continue
		}
		// ponytail: retain command IDs for this identity; compact only with a negotiated replay horizon.
		if len(s.State.Journal) >= 10000 {
			return errors.New("local command journal full; manual recovery required")
		}
		if order == math.MaxUint64 {
			return errors.New("command admission order exhausted")
		}
		order++
		e := JournalEntry{Order: order, Status: "pending", RequestedAt: time.Now(), Command: &cmd, PeerDispatched: peer}
		if cmd.Power != nil {
			unsettledSnapshot := hasUnsettledSnapshot(s.State.Journal, cmd.Power.VMID)
			emergency := cmd.Power.Action == "shutdown" || cmd.Power.Action == "force-stop"
			if emergency && unsettledSnapshot {
				// Clear snapshot After-predecessors only. Keep a pending definition/delete
				// fence so emergency stop cannot race a same-VM vm-update.
				pred := cmd.Power.AfterOperationID
				if pred == "" {
					pred = pendingVMDefinitionCommand(s.State.Journal, cmd.Power.VMID)
				}
				if pred != "" {
					before := s.State.Journal[pred]
					if before.Command != nil && snapshotCommand(before.Command.Kind) {
						e.AfterOperationID = ""
					} else if before.Command != nil && vmDefinitionCommand(before.Command.Kind) {
						e.AfterOperationID = pred
					}
				}
			} else if cmd.Power.AfterOperationID != "" {
				e.AfterOperationID = cmd.Power.AfterOperationID
			} else {
				e.AfterOperationID = pendingVMCommand(s.State.Journal, cmd.Power.VMID)
			}
		}
		if cmd.Kind != "refresh-inventory" && !managedCommand(cmd.Kind) {
			e.Status = "failed"
			e.Error = "unsupported agent command"
			e.ObservedAt = e.RequestedAt
		}
		s.State.Journal[cmd.OperationID] = e
	}
	return s.Save()
}

func storageRepositoryCommand(cmd protocol.Command) bool {
	if cmd.StorageLifecycle != nil {
		return true
	}
	if cmd.CredentialRetire != nil {
		return true
	}
	return cmd.Kind == "storage-register" && cmd.Storage != nil && cmd.Storage.StorageKind() != protocol.StorageKindLocal
}
func report(ctx context.Context, s *Store, m *Monitor, cfg RunConfig) error {
	return reportStorage(ctx, s, m, nil, cfg)
}

func reportStorage(ctx context.Context, s *Store, m *Monitor, storage *vmhost.StorageMonitor, cfg RunConfig) error {
	if err := markLegacyPeerBackupJournal(s); err != nil {
		return err
	}
	inv := m.Snapshot()
	// Libvirt can stall while a backing repository is unavailable. Bound only this
	// observational read: the caller's report context must remain usable for the
	// outbound heartbeat that records the uncertainty.
	appliedCtx, cancelApplied := context.WithTimeout(ctx, cfg.ProbeTimeout)
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(appliedCtx)
	cancelApplied()
	managed.Storage = PoolStorageMemberAccess(s, managed.Storage)
	managed.Networks = PoolNetworkMemberAccess(s, managed.Networks)
	if check := PoolStorageReconcileCheck(s); check != nil {
		checks = append(checks, *check)
	}
	checks = append(checks, PoolConvergeChecks(s)...)
	// Membership is a master-owned Core record (with the member's authenticated copy).
	// Reporting it is the sole Controller adoption path; no Controller cache derives it.
	if membership, err := Membership(s); err == nil {
		// The master overlays what each member acknowledged of its signed authority
		// replica: an observation on a copy of the record, never written back to it.
		managed.Membership = PoolReplicaMembership(s, &membership)
		// And whether each host could actually be shut out, from the same master and in
		// the same manner: an observation overlaid on the copy it reports, never
		// written back into the record it owns.
		managed.Membership = PoolFenceCheckMembership(s, managed.Membership)
	}
	detectedOS, detectedOSErr := poolDetectedOS(s)
	managed.DetectedOS = detectedOS
	if detectedOSErr != nil {
		checks = append(checks, protocol.Check{Name: "guest-os-history", Status: "failed", Error: "retained guest OS is unavailable", ObservedAt: time.Now().UTC()})
	}
	// This host's own stored replica, and the honest freshness of it on both sides. The
	// replica is reported, never acted on, and no other check is derived from it.
	managed.Replica = PoolReplicaReport(s)
	checks = append(checks, PoolReplicaChecks(s, cfg.ProbeInterval)...)
	// The master's own durable authority number and the content it numbered. Without it a
	// member's reported replica generation has nothing to be compared against, and a
	// lagging replica reads as a current one.
	managed.Authority = PoolAuthorityReport(s)
	if membership, membershipErr := Membership(s); membershipErr == nil && membership.MasterHostID == s.State.HostID {
		if reservations, reported, recoveryErr := PoolVMRecoveryInventory(s); recoveryErr != nil {
			checks = append(checks, protocol.Check{Name: "pool-vm-recovery", Status: "failed", Error: "pool snapshot recovery reservations are unavailable", ObservedAt: time.Now().UTC()})
		} else if reported {
			managed.RecoveryReservations = reservations
			managed.RecoveryReservationsReported = true
		}
		if snapshots, reported, snapshotErr := PoolVMSnapshotInventory(s); snapshotErr != nil {
			checks = append(checks, protocol.Check{Name: "pool-vm-snapshots", Status: "failed", Error: "pool snapshot catalog is unavailable", ObservedAt: time.Now().UTC()})
		} else if reported {
			managed.PoolVMSnapshots = snapshots
			managed.PoolVMSnapshotsReported = true
		}
		if repos, catalog, runs, policies, reported, backupErr := PoolBackupInventory(s); backupErr != nil {
			checks = append(checks, protocol.Check{Name: "pool-backup", Status: "failed", Error: "pool backup state is unavailable", ObservedAt: time.Now().UTC()})
		} else if reported {
			managed.BackupRepositories = repos
			managed.BackupRepositoriesReported = true
			managed.BackupCatalog = catalog
			managed.BackupCatalogReported = true
			managed.BackupRuns = runs
			managed.BackupRunsReported = true
			managed.BackupPolicies = policies
			managed.BackupPoliciesReported = true
		}
	}
	// This host's own part of a master change or an eject, from every participant, so the
	// Controller assembles one honest picture instead of keeping a job of its own.
	managed.Transfer = PoolTransferReport(s)
	checks = append(checks, PoolTransferCheck(s)...)
	// A fence-verified promotion this host is performing, and this host's own durable
	// refusal to act on pool authority while it cannot prove it holds the current one.
	// Both are Core's records; the Controller displays them and enforces nothing.
	managed.Promotion = PoolPromotionReport(s)
	checks = append(checks, PoolPromotionCheck(s)...)
	managed.Restricted = PoolRestrictedReport(s)
	checks = append(checks, PoolRestrictedCheck(s)...)
	// The staged join is Core's own durable record. Reporting it is how a replaced
	// Controller adopts an interrupted join; the invitation is never part of it.
	managed.Join = PoolJoinReport(s)
	// This host's own peer address is an observation for the join dialog to offer; the
	// join records it durably on the master. A host with no route reports none.
	if origin, originErr := ownPeerOrigin(); originErr == nil {
		managed.PeerOrigin = origin.String()
	}
	if storage != nil {
		managed.Storage = storage.Overlay(managed.Storage)
	}
	// Pool-local ZFS access is a bounded fresh observation; never let an
	// unreachable member consume the outbound heartbeat's whole context.
	zfsAccessCtx, cancelZFSAccess := context.WithTimeout(ctx, cfg.ProbeTimeout)
	managed.Storage = PoolZFSStorageProjection(zfsAccessCtx, s, managed.Storage)
	cancelZFSAccess()
	managed.Media = PoolMediaMemberAccess(s, managed.Media, managed.Storage)
	credentialsReadable := true
	if _, err := vmhost.StorageCredentialReceipts(s.dir); err != nil {
		credentialsReadable = false
		checks = append(checks, protocol.Check{Name: "storage-credentials", Status: "failed", Error: "storage credential state is unavailable", ObservedAt: time.Now().UTC()})
	}
	managed.Appliances = vmhost.Deployments(ApplianceJournalDir)
	confirmCtx, cancelConfirm := context.WithTimeout(ctx, cfg.ProbeTimeout)
	managed.Outcomes = confirmPoolVMUpdateOutcomes(confirmCtx, s, commandOutcomes(s.State.Journal, managed.Outcomes), managed.Storage)
	cancelConfirm()
	managed.ZFSProvisions = PoolZFSInventory(s)
	// This host's own management network: the durable record projected for adoption, so a
	// replaced Controller sees a running rollback deadline instead of losing it. The check
	// stands failed while an automatic rollback is the last outcome, and disappears with the
	// next successful apply, which is how an operator clears it. A rollback the operator asked
	// for is not an automatic one: it is the outcome they chose, so it reports no check at all
	// and nobody is sent looking for a failure to fix.
	if status, statusErr := vmhost.ReadManagementNetworkStatus(s.dir); statusErr != nil {
		checks = append(checks, protocol.Check{Name: "management-network", Status: "failed", Error: "the management network record is unreadable", ObservedAt: time.Now().UTC()})
	} else if status != nil {
		inv.ManagementNetwork = status
		switch {
		case status.Stage == protocol.ManagementNetworkRolledBack && !status.RequestedRollback:
			checks = append(checks, protocol.Check{Name: "management-network", Status: "failed", Error: resultError(status.LastError), ObservedAt: status.ObservedAt})
		case status.Stage == protocol.ManagementNetworkApplied && status.LastError != "":
			// The host tried to restore itself and could not: the files are back but the
			// bridge is not on them yet, and recovery is still retrying. Without this the
			// one state that means "this host could not save itself" renders as an ordinary
			// applied change whose countdown has run out, whoever asked for the rollback.
			checks = append(checks, protocol.Check{Name: "management-network", Status: "failed",
				Error: "restoring the previous management network configuration has not completed: " + resultError(status.LastError), ObservedAt: status.ObservedAt})
		}
	}
	inv.Managed = managed
	inv.Checks = append(inv.Checks, checks...)
	completeJournal(s, inv, cfg.ProbeInterval+2*cfg.ProbeTimeout)
	if s.State.Sequence == math.MaxUint64 {
		return errors.New("report sequence exhausted")
	}
	s.State.Sequence++
	// A v2 report without the repository capability is the Controller-first
	// preflight. It carries no v2 storage data and cannot receive storage work.
	version := protocol.Version
	// DriversDriveCapability is deliberately absent from the legacy v1 list below: a host
	// defines the second CD drive either way, but the capability is the Controller's signal
	// that it may send drive="drivers", and a strict v1 decoder must not be handed v2 names.
	capabilities := []string{protocol.ManagedVMCapability, protocol.PoolVMPlacementCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.PowerQueueCapability, protocol.MediaCapability, protocol.ISODownloadCapability, protocol.UpdateCapability, protocol.VMTagsCapability, protocol.DeleteCapability, protocol.DriversDriveCapability, protocol.UsageHistoryCapability, protocol.CPUBaselineCapability, protocol.PoolResourceRemovalCapability, protocol.PoolJoinCapability, protocol.PoolMaintenanceCapability, protocol.ManagementNetworkCapability, protocol.PoolAuthorityReplicaCapability, protocol.PoolAuthorityTransferCapability, protocol.PoolBackupPagedAuthorityCapability, protocol.HostHardwareCapability, protocol.PoolHostFencingCapability, protocol.PoolWatchdogClusterCapability, protocol.PoolWitnessCapability, protocol.BackupCapability}
	if s.State.StorageProtocolLegacy {
		version = protocol.LegacyVersion
		capabilities = []string{protocol.ManagedVMCapability, protocol.PoolVMPlacementCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.MediaCapability, protocol.UpdateCapability, protocol.DeleteCapability, protocol.UsageHistoryCapability}
		// A strict v1 decoder must not see v2 managed storage/receipt fields.
		legacy := *inv
		legacy.Managed = nil
		legacy.CPUBaseline = nil
		legacy.ManagementNetwork = nil
		inv = &legacy
	}
	if inv.Managed != nil {
		copy := *inv
		copy.Managed = managedExtensionsForController(inv.Managed, s.State.ISODownloadProgressReady, s.State.DetectedOSReady)
		inv = &copy
	}
	if s.State.StorageProtocolReady {
		// System-disk replace opens storage/discover for the spare list, so it shares the
		// storage handshake gate with discovery and ZFS provision/replace.
		capabilities = append(capabilities, "storage-repositories-v1", protocol.StorageDiscoveryCapability, protocol.ZFSProvisionCapability, protocol.ZFSReplaceCapability, protocol.SystemDiskReplaceCapability)
		if inv.Managed != nil {
			managed := inv.Managed
			if !s.State.ZFSProvisionProtocolReady {
				// Strict older Controllers reject unknown managed and registration
				// fields. Advertise the optional extension, but omit every pool-local
				// ZFS record until this Controller explicitly acknowledges it; presenting
				// it as a legacy host-local registration would lose its pool semantics.
				managed = zfsLegacyManagedProjection(managed)
			}
			if !s.State.ZFSReplaceProtocolReady {
				managed = zfsPoolObservationForController(managed, false)
			}
			if managed != inv.Managed {
				copy := *inv
				copy.Managed = managed
				inv = &copy
			}
		}
	}
	if !s.State.BackupProtocolReady && inv.Managed != nil {
		copy := *inv
		managedCopy := *inv.Managed
		managedCopy.BackupRepositories, managedCopy.BackupCatalog = nil, nil
		managedCopy.BackupRuns, managedCopy.BackupPolicies = nil, nil
		managedCopy.BackupRepositoriesReported, managedCopy.BackupCatalogReported = false, false
		managedCopy.BackupRunsReported, managedCopy.BackupPoliciesReported = false, false
		copy.Managed = &managedCopy
		inv = &copy
	}
	if s.State.StorageClusterProtocolReady {
		capabilities = append(capabilities, protocol.StorageClusterCapability)
	}
	if version == protocol.Version {
		// Offer the additive extension during every v2 preflight. An older
		// Controller ignores it and acknowledges the base capability instead.
		capabilities = append(capabilities, protocol.StorageClusterSessionMonitorCapability, protocol.ISODownloadProgressCapability, protocol.DetectedOSCapability)
	}
	discoveries := transientStorageDiscoveries.drain(s)
	var clusters []protocol.StorageClusterResult
	if s.State.StorageClusterProtocolReady {
		clusters = transientStorageClusters.drain(s)
		clusters = storageClusterResultsForController(clusters, s.State.StorageClusterSessionMonitorProtocolReady)
	}
	req := protocol.ReportRequest{Capabilities: capabilities, Version: version, Sequence: s.State.Sequence, Inventory: inv, Results: []protocol.Result{}, StorageDiscoveries: discoveries, StorageClusters: clusters, PoolResults: transientPoolResults.drain(s)}
	for id, e := range s.State.Journal {
		if e.Status != "pending" && !e.Acknowledged && !e.PeerDispatched {
			req.Results = append(req.Results, protocol.Result{OperationID: id, Status: e.Status, Error: resultError(e.Error), ObservedAt: e.ObservedAt})
			if len(req.Results) == 100 {
				break
			}
		}
	}
	if err := s.Save(); err != nil {
		return persistenceError{fmt.Errorf("persist report: %w", err)}
	}
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	defer c.CloseIdleConnections()
	var response protocol.ReportResponse
	// Which socket carried the report decides whether it counts as evidence that the live
	// management configuration works. A report that went out over a second, unchanged NIC
	// proves nothing about the one under test, so the connection's own local address — not the
	// fact that a reply arrived — is what Core records. GotConn fires once the connection is
	// in hand, before the request is written.
	var reportLocal, reportPeer netip.Addr
	traced := httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) {
		reportLocal = vmhost.ConnectionAddr(info.Conn.LocalAddr().String())
		reportPeer = vmhost.ConnectionAddr(info.Conn.RemoteAddr().String())
	}})
	if err = post(traced, c, s.State.Controller, "/api/v1/agent/report", req, &response); err != nil {
		var status httpStatusError
		if version == protocol.Version && !s.State.StorageProtocolReady && errors.As(err, &status) && status.Status == 409 && legacyStorageSafe(s.State, managed, credentialsReadable) {
			s.State.StorageProtocolLegacy = true
			if saveErr := s.Save(); saveErr != nil {
				return persistenceError{fmt.Errorf("persist storage protocol fallback: %w", saveErr)}
			}
			return report(ctx, s, m, cfg)
		}
		return err
	}
	// An authenticated report answered by the enrolled Controller, over a socket whose local
	// address is one the applied spec puts on the management bridge, is this host proving its
	// connectivity over the configuration under test; a staged change cannot be confirmed
	// without such an observation. A no-op unless one is applied.
	_ = vmhost.RecordManagementConnectivity(s.dir, "controller-report", time.Now(), reportLocal, reportPeer)
	ready := response.ISODownloadProgress != nil && *response.ISODownloadProgress
	osReady := response.DetectedOS != nil && *response.DetectedOS
	if s.State.ISODownloadProgressReady != ready || s.State.DetectedOSReady != osReady {
		s.State.ISODownloadProgressReady = ready
		s.State.DetectedOSReady = osReady
		if err = s.Save(); err != nil {
			return persistenceError{fmt.Errorf("persist managed report extensions preflight: %w", err)}
		}
	}
	if response.StorageRepositories != nil && response.StorageRepositories.ProtocolVersion == protocol.Version && (response.StorageRepositories.Capability == protocol.StorageRepositoryCapability || response.StorageRepositories.Capability == protocol.ZFSProvisionCapability || response.StorageRepositories.Capability == protocol.ZFSReplaceCapability) {
		if !s.State.StorageProtocolReady {
			s.State.StorageProtocolReady = true
		}
		provisionReady := response.StorageRepositories.Capability == protocol.ZFSProvisionCapability || response.StorageRepositories.Capability == protocol.ZFSReplaceCapability
		replaceReady := response.StorageRepositories.Capability == protocol.ZFSReplaceCapability
		if s.State.ZFSProvisionProtocolReady != provisionReady || s.State.ZFSReplaceProtocolReady != replaceReady {
			s.State.ZFSProvisionProtocolReady = provisionReady
			s.State.ZFSReplaceProtocolReady = replaceReady
		}
		if err = s.Save(); err != nil {
			return persistenceError{fmt.Errorf("persist storage protocol preflight: %w", err)}
		}
	}
	if response.BackupSupport != nil && response.BackupSupport.Capability == protocol.BackupCapability && !s.State.BackupProtocolReady {
		s.State.BackupProtocolReady = true
		if err = s.Save(); err != nil {
			return persistenceError{fmt.Errorf("persist backup protocol preflight: %w", err)}
		}
	}
	if response.StorageClusters != nil && (response.StorageClusters.Capability == protocol.StorageClusterCapability || response.StorageClusters.Capability == protocol.StorageClusterSessionMonitorCapability) {
		changed := false
		if !s.State.StorageClusterProtocolReady {
			s.State.StorageClusterProtocolReady = true
			changed = true
		}
		sessionMonitorReady := response.StorageClusters.Capability == protocol.StorageClusterSessionMonitorCapability
		if s.State.StorageClusterSessionMonitorProtocolReady != sessionMonitorReady {
			s.State.StorageClusterSessionMonitorProtocolReady = sessionMonitorReady
			changed = true
		}
		if changed {
			if err = s.Save(); err != nil {
				return persistenceError{fmt.Errorf("persist clustered storage protocol preflight: %w", err)}
			}
		}
	}
	redeemStorageCredentials(ctx, s, response.CredentialOffers)
	if err = applyResponse(s, response); err != nil {
		return persistenceError{fmt.Errorf("persist commands: %w", err)}
	}
	if len(response.StorageDiscoveries) > 0 {
		go func(requests []protocol.StorageDiscoveryRequest) {
			results := make([]protocol.StorageDiscoveryResult, 0, len(requests))
			for _, request := range requests {
				if !storageDiscoveryScopeMatches(s.State, request) {
					continue
				}
				work, cancel := context.WithTimeout(ctx, 8*time.Second)
				if request.Kind == "zfs-device" {
					results = append(results, discoverZFSDevices(work, request))
				} else {
					results = append(results, vmhost.DiscoverStorage(work, request))
				}
				cancel()
			}
			transientStorageDiscoveries.append(s, results)
		}(append([]protocol.StorageDiscoveryRequest(nil), response.StorageDiscoveries...))
	}
	if len(response.StorageClusterRequests) > 0 {
		go func(requests []protocol.StorageClusterRequest) {
			results := make([]protocol.StorageClusterResult, 0, len(requests))
			for _, request := range requests {
				if !storageClusterScopeMatches(s.State, request) {
					continue
				}
				// A report ending cannot cancel Core-owned work. Execute validates and
				// persists it before any native setup continues independently.
				work, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				results = append(results, ExecuteStorageClusterRequest(work, s, request))
				cancel()
			}
			transientStorageClusters.append(s, results)
		}(append([]protocol.StorageClusterRequest(nil), response.StorageClusterRequests...))
	}
	if len(response.PoolRequests) > 0 {
		// The master answers from its own durable offer record. The reply rides the next
		// report, which is what the Controller's one-round-trip broker waits for.
		go func(requests []protocol.PoolRequest) {
			results := make([]protocol.PoolResult, 0, len(requests))
			for _, request := range requests {
				if request.Action == "promotion-preflight" {
					work, done := context.WithTimeout(context.Background(), time.Minute)
					results = append(results, ExecutePoolPromotionRequest(work, s, request, 20*time.Second))
					done()
					continue
				}
				if request.Action == "bmc-probe" {
					work, done := context.WithTimeout(context.Background(), 2*time.Minute)
					results = append(results, ExecutePoolBMCProbe(work, s, request))
					done()
					continue
				}
				results = append(results, ExecutePoolRequest(s, request))
			}
			transientPoolResults.append(s, results)
		}(append([]protocol.PoolRequest(nil), response.PoolRequests...))
	}

	for _, console := range response.Consoles {
		go func(req protocol.ConsoleRequest) {
			if err := serveConsole(ctx, s.State, req); err != nil {
				if req.VMID == "" {
					log.Printf("host console: %v", err)
				} else {
					log.Printf("console for VM %s: %v", req.VMID, err)
				}
			}
		}(console)
	}
	for _, channel := range response.StatsChannels {
		if cfg.history == nil {
			break
		}
		go func(req protocol.StatsChannelRequest) {
			if err := serveStatsChannel(ctx, s.State, req, statsChannelHandler(cfg.history)); err != nil {
				log.Printf("stats channel: %v", err)
			}
		}(channel)
	}
	if len(response.Commands) > 0 {
		m.Tick(ctx)
	}
	return nil
}

// Older members recorded authenticated peer backup commands as ordinary local
// commands. Their master owns the Controller operation, while the member keeps
// its journal for exact peer replay. Mark only runs retained on a current member;
// a former master keeps its own Controller results through a planned handoff.
func markLegacyPeerBackupJournal(s *Store) error {
	peer, err := loadPoolPeer(s)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if peer.Membership.MasterHostID == "" || peer.Membership.MasterHostID == s.State.HostID ||
		peer.Transfer != nil && peer.Transfer.Role == "source" {
		return nil
	}
	changed := false
	for id, entry := range s.State.Journal {
		if entry.PeerDispatched || entry.Acknowledged || entry.Command == nil || !backupCommand(entry.Command.Kind) {
			continue
		}
		run, err := loadPoolBackupRun(s, id)
		if err != nil || run.HostID != s.State.HostID || run.Kind != entry.Command.Kind {
			continue
		}
		entry.PeerDispatched = true
		s.State.Journal[id] = entry
		changed = true
	}
	if changed {
		return s.Save()
	}
	return nil
}

func zfsLegacyManagedProjection(managed *protocol.ManagedState) *protocol.ManagedState {
	copy := *managed
	copy.Storage = slices.DeleteFunc(append([]protocol.StorageRegistration(nil), copy.Storage...), func(entry protocol.StorageRegistration) bool {
		return entry.ZFSProvisioned
	})
	copy.ZFSProvisions = nil
	return &copy
}

// confirmPoolVMUpdateOutcomes prevents a successful local managed journal from
// becoming a Controller-visible successful shared-VM definition change before
// the pool master's exact durable record agrees. This covers both a lost
// update-commit reply and a crash after vmhost saved its local journal but
// before the managed worker recorded its completion. The existing define
// recovery request is read/compare only when create is false; it rejects a
// pending, foreign, or different master definition.
func confirmPoolVMUpdateOutcomes(ctx context.Context, s *Store, outcomes []protocol.ManagedOutcome, storage []protocol.StorageRegistration) []protocol.ManagedOutcome {
	// At most one current nonterminal revision per guest merits a peer round
	// trip. Older interrupted revisions cannot be confirmed against the current
	// master definition and remain honestly uncertain without delaying reports.
	candidates := make(map[string]int)
	specs := make(map[string]*protocol.VMCreateSpec)
	for i := range outcomes {
		outcome := &outcomes[i]
		if outcome.Kind != "vm-update" {
			continue
		}
		entry, ok := s.State.Journal[outcome.OperationID]
		if ok && entry.Command != nil && entry.Command.Kind == "vm-media" {
			// A console media change applied as an update (BUG-0185) is the Controller's
			// vm-media operation; it only accepts outcomes under that kind.
			outcome.Kind = "vm-media"
		}
		if outcome.Status != "succeeded" && outcome.Status != "failed" {
			continue
		}
		spec := poolVMUpdateSpec(s, entry)
		if !ok || spec == nil {
			outcome.Status = "needs_attention"
			outcome.Error = "local VM update requires pool owner confirmation"
			continue
		}
		specs[outcome.OperationID] = spec
		// A terminal local result was already reported only after this same
		// confirmation. Re-checking it would incorrectly turn an older completed
		// revision into uncertainty after a later legitimate update.
		if entry.Status == "succeeded" || entry.Status == "failed" {
			continue
		}
		// Local-only VM updates never have a pool owner record. Reuse the
		// already-read storage projection plus read-only update evidence to
		// recognize an update that removed its last shared disk without adding a
		// new native observation to the report path.
		shared := vmUsesPoolSharedStorage(*spec, storage)
		var failedEvidence *vmhost.VMUpdateEvidence
		if outcome.Status == "succeeded" {
			evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).SuccessfulVMUpdateEvidence(ctx, spec.ID)
			if err == nil && reflect.DeepEqual(evidence.Current, *spec) {
				shared = shared || vmUsesPoolSharedStorage(evidence.Previous, storage)
			}
		} else {
			evidence, err := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).FailedVMUpdateEvidence(ctx, spec.ID, outcome.OperationID, *spec)
			if err == nil {
				failedEvidence = &evidence
				shared = shared || vmUsesPoolSharedStorage(evidence.Previous, storage)
			}
		}
		if !shared {
			continue
		}
		if outcome.Status == "failed" {
			if failedEvidence == nil || poolVMUpdateRequestPeer(ctx, s, spec.ID, "update-abort", outcome.OperationID, nil, nil, failedEvidence) != nil {
				outcome.Status = "needs_attention"
				outcome.Error = "local VM update failure is pending pool owner confirmation"
				continue
			}
			entry.Status, entry.Error, entry.ObservedAt = "failed", outcome.Error, time.Now().UTC()
			s.State.Journal[outcome.OperationID] = entry
			continue
		}
		if prior, ok := candidates[spec.ID]; ok {
			if specs[outcomes[prior].OperationID].Revision >= spec.Revision {
				outcome.Status, outcome.Error = "needs_attention", "local VM update requires pool owner confirmation"
				continue
			}
			outcomes[prior].Status, outcomes[prior].Error = "needs_attention", "local VM update requires pool owner confirmation"
		}
		candidates[spec.ID] = i
	}
	for _, i := range candidates {
		outcome := &outcomes[i]
		entry, spec := s.State.Journal[outcome.OperationID], specs[outcome.OperationID]
		if err := poolVMRequestPeer(ctx, s, spec.ID, "define", nil, false, spec, nil); err != nil {
			outcome.Status = "needs_attention"
			outcome.Error = "local VM update succeeded but pool owner confirmation is pending"
			continue
		}
		entry.Status, entry.Error, entry.ObservedAt = "succeeded", "", time.Now().UTC()
		s.State.Journal[outcome.OperationID] = entry
	}
	return outcomes
}

// poolVMUpdateSpec returns the definition a local vm-update outcome must confirm. A shared
// VM media change is journaled here as vm-media but executed by the managed engine as a
// vm-update under the same operation ID (PoolVMMediaCommand); only that exact rewritten
// transition for the same VM is accepted, and the agent journal command is never rewritten.
func poolVMUpdateSpec(s *Store, entry JournalEntry) *protocol.VMCreateSpec {
	switch {
	case entry.Command == nil:
		return nil
	case entry.Command.Kind == "vm-update":
		return entry.Command.VM
	case entry.Command.Kind == "vm-media" && entry.Command.VMMedia != nil:
		managed, ok := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).JournaledCommand(entry.Command.OperationID)
		if ok && managed.Kind == "vm-update" && managed.VM != nil && managed.VM.ID == entry.Command.VMMedia.VMID {
			return managed.VM
		}
	}
	return nil
}

func vmUsesPoolSharedStorage(vm protocol.VMCreateSpec, storage []protocol.StorageRegistration) bool {
	for _, disk := range vm.Disks {
		for _, registration := range storage {
			if registration.ID == disk.StorageID && protocol.IsPoolSharedStorage(registration) {
				return true
			}
		}
	}
	return false
}

func storageDiscoveryScopeMatches(state State, request protocol.StorageDiscoveryRequest) bool {
	return request.HostID == state.HostID && request.PoolID == state.PoolID
}

func storageClusterScopeMatches(state State, request protocol.StorageClusterRequest) bool {
	return request.HostID == state.HostID && request.PoolID == state.PoolID && (request.Action == "apply" || request.Action == "status" || request.Action == "verify_fence")
}

func legacyStorageSafe(state State, managed *protocol.ManagedState, credentialsReadable bool) bool {
	if !credentialsReadable || state.StorageProtocolReady || len(managed.CredentialReceipts) != 0 {
		return false
	}
	for _, storage := range managed.Storage {
		if storage.StorageKind() != protocol.StorageKindLocal {
			return false
		}
	}
	for _, outcome := range managed.Outcomes {
		// A pool network/ISO removal or rename is v2-only history: a host that performed
		// one must not fall back to reporting as a legacy v1 host, which drops its whole
		// managed state and with it the tombstones the Controller mirror needs.
		if outcome.Kind == "storage-register" || outcome.Kind == "storage-update" || outcome.Kind == "storage-recheck" || outcome.Kind == "storage-detach" || outcome.Kind == "storage-credential-retire" || outcome.Kind == "network-remove" || outcome.Kind == "network-update" || outcome.Kind == "media-remove" {
			return false
		}
	}
	for _, entry := range state.Journal {
		if entry.Command != nil && storageRepositoryCommand(*entry.Command) {
			return false
		}
	}
	return true
}
func Run(ctx context.Context, s *Store, cfg RunConfig) error {
	if cfg.Heartbeat < 100*time.Millisecond || cfg.ProbeInterval < 100*time.Millisecond || cfg.ProbeTimeout < 10*time.Millisecond {
		return errors.New("heartbeat/probe interval must be >=100ms and timeout >=10ms")
	}
	peerRequests := make(chan BackupRequest)
	peerBackupRequests.Store(s, peerRequests)
	defer peerBackupRequests.Delete(s)
	// A single-host pool is still a pool: record this Core as its master before any
	// pool work. On failure pool operations stay refused; running VMs are unaffected.
	if err := EnsurePoolMembership(s); err != nil {
		log.Printf("pool membership could not be recorded; pool operations stay refused until it is")
	}
	// Before any start path is exposed: a pool master with other members cannot know, at
	// boot, that it was not fenced and promoted away while it was down. It restricts itself
	// until its members confirm it, and the restriction is durable, so it holds through a
	// reboot and with no Controller connected.
	if err := EnsurePoolBootRestriction(s); err != nil {
		log.Printf("pool authority restriction could not be recorded; this host starts no pool guest until it is")
	}
	// Before this host reports anything: an unconfirmed management network change whose
	// deadline passed, or that outlived a reboot, is restored now. A host that was cut off
	// by its own new configuration and power-cycled therefore comes back on the previous
	// one with no Controller involved at all.
	if _, err := vmhost.RecoverManagementNetwork(ctx, s.dir); err != nil {
		log.Printf("management network recovery did not complete; inspect the Core record")
	}
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	c.CloseIdleConnections()
	// A stored acknowledgement belongs to the prior Controller connection. Never
	// send additive report fields until this connection acknowledges
	// their exact capabilities again.
	if err := resetReportExtensions(s); err != nil {
		return err
	}
	// A restarted connection must receive the Controller's backup-v1 acknowledgement
	// before its first backup inventory envelope is sent.
	if s.State.BackupProtocolReady {
		s.State.BackupProtocolReady = false
		if err := s.Save(); err != nil {
			return err
		}
	}
	// Pending read-only refreshes reconcile against a new sample after restart.
	// Retain monotonic time in memory; serialized timestamps are history only.
	for id, e := range s.State.Journal {
		if e.Status == "pending" && (e.Command == nil || !managedCommand(e.Command.Kind)) {
			e.RequestedAt = time.Now()
			s.State.Journal[id] = e
		}
	}
	if err := s.Save(); err != nil {
		return err
	}
	// This is the only automatic stale-reservation cleanup. It runs before the
	// worker can reserve an operation, so it cannot erase the reserve→journal
	// admission window. Periodic reconciliation deliberately never performs it.
	if membership, membershipErr := Membership(s); membershipErr == nil && membership.MasterHostID != s.State.HostID {
		ReleaseUnusedPoolStorageReservations(ctx, s)
	}
	cfg.history = newUsageHistory(filepath.Join(s.dir, "usage"), s.State.HostID)
	sampler := newUsageSampler(cfg.history)
	probes := NativeProbes(version.Version, sampler)
	probes["cpu-baseline"] = PoolCPUProbe(s)
	probes["guest"] = recordingGuestProbe(func(ctx context.Context, detected map[string]protocol.DetectedOS) { recordGuestOS(ctx, s, detected) })
	m := NewMonitor(cfg.ProbeTimeout, probes)
	storage := vmhost.NewStorageMonitor(cfg.ProbeTimeout)
	s.storageMonitor.Store(storage)
	var poolReconcileRunning atomic.Bool
	var clusterReconcileRunning atomic.Bool
	var fenceCheckRunning atomic.Bool
	var quorumReadingRunning atomic.Bool
	var nextProbeSecretSweep time.Time
	// Deferred before cancel so it runs after it: Run must not return while its background
	// work still reads the store, because a caller may re-enroll and rewrite it next.
	var runWork sync.WaitGroup
	defer runWork.Wait()
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// Cluster work is Core-owned. Run it at startup and with the existing probe
	// cadence; a Controller report is only a transient trigger, never a worker.
	scheduleClusterReconcile := func() {
		if !clusterReconcileRunning.CompareAndSwap(false, true) {
			return
		}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer clusterReconcileRunning.Store(false)
			work, done := context.WithTimeout(probeCtx, 5*time.Minute)
			defer done()
			// A retained peer advertisement is Core recovery work; retry it with
			// the cluster worker, never from a Controller request.
			_ = RetryPoolPeerOrigin(work, s)
			// Which machine this host is, recorded on the master beside where it listens.
			_ = ReportPoolSystemUUID(work, s)
			// And the native generation of its live host lease, pinned to the authority
			// lockspace so a stale sanlock record cannot prove a later host's exclusion.
			_ = ReportPoolSanlockHostGeneration(work, s)
			// And whether that machine can reset itself: sysfs only, never opening
			// the watchdog device, because opening it is what arms it.
			_ = ReportPoolWatchdog(work, s)
			// And how that machine can be powered off: this host's own management
			// controller, configured from the inside with no operator input.
			_ = EnsureLocalBMCFence(work, s)
			membership, err := Membership(s)
			if err != nil {
				return
			}
			if membership.MasterHostID == s.State.HostID {
				// A clustered-storage operation's accepted configuration is a replicated
				// section too, so a frozen, handed-over or restricted authority starts no
				// new cluster work: it would move the content a pinned generation names.
				// Work already in flight is never interrupted, and the next tick resumes
				// this pass once the transfer is over.
				if requirePoolAuthorityWritable(s) != nil {
					return
				}
				ResumeStorageClusterOperations(work, s)
				return
			}
			_ = ReconcileStorageClustersFromMaster(work, s)
		}()
	}
	// Whether the fence devices the pool recorded still work: every other host's
	// management controller read without powering anything off. It rides the same tick but
	// runs on its own, because a pass against controllers that have gone away spends its
	// whole two-minute budget, and the storage-cluster reconcile must not wait for it. It
	// throttles itself to its own interval, so a tick that is not due costs nothing, and it
	// stops with probeCtx like every other worker here.
	// What this host can see of the pool's Corosync quorum, on its own tick. It is one local
	// command and it is the clock a watchdog exclusion proof is measured on, so it must not
	// ride behind the cluster reconcile: a pass there can spend minutes in a conversation
	// with a management controller, and a gap longer than a fraction of the proof's bound
	// makes continuity unknown and restarts the clock. Its own worker keeps the readings
	// prompt; single-flight so a slow corosync-quorumtool cannot pile up.
	scheduleQuorumReading := func() {
		if !quorumReadingRunning.CompareAndSwap(false, true) {
			return
		}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer quorumReadingRunning.Store(false)
			reading, done := context.WithTimeout(probeCtx, 20*time.Second)
			defer done()
			_ = ObservePoolQuorum(reading, s)
		}()
	}
	// Whether this host should be feeding a watchdog, and whether the pool's own
	// Corosync/Pacemaker cluster should exist, in that order and on their own tick.
	//
	// The order is the safety property. Arming runs first, so a host that must stop being
	// armed has the pool's record say so before its cluster goes down under it, and a host
	// that has just booted disables sbd *before* this worker starts its cluster — which is
	// what stops a host coming up armed and alone after a power cut and resetting itself in
	// a loop. The tick is its own because the pool's durable record over-claims for as long
	// as a host takes to notice it is no longer armed, and the cluster worker beside it can
	// spend minutes in a conversation with a management controller.
	var watchdogReconcileRunning atomic.Bool
	scheduleWatchdogReconcile := func() {
		if !watchdogReconcileRunning.CompareAndSwap(false, true) {
			return
		}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer watchdogReconcileRunning.Store(false)
			work, done := context.WithTimeout(probeCtx, time.Minute)
			defer done()
			_ = ReconcilePoolWitness(work, s)
			_ = ReconcilePoolWatchdogArming(work, s)
			_ = ReconcilePoolCluster(work, s)
		}()
	}
	scheduleFenceChecks := func() {
		if !fenceCheckRunning.CompareAndSwap(false, true) {
			return
		}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer fenceCheckRunning.Store(false)
			_ = RunPoolFenceChecks(probeCtx, s)
		}()
	}
	scheduleClusterReconcile()
	scheduleFenceChecks()
	scheduleQuorumReading()
	scheduleWatchdogReconcile()
	runWork.Add(1)
	go func() { defer runWork.Done(); sampler.run(probeCtx) }()
	go m.WatchLibvirt(probeCtx)
	backupTicks := make(chan time.Time, 1)
	preparedBackupDone := make(chan preparedBackupRequest, 2)
	verificationDone := make(chan completedBackupVerification, 2)
	cleanupDone := make(chan completedBackupCleanup, 1)
	backupPrepSlots := make(chan struct{}, 2)
	preparingBackupRepo := map[string]bool{}
	activeBackupCleanup := map[string]bool{}
	cleanupNextAttempt := map[string]time.Time{}
	peerBackupDone := make(chan peerBackupCompletion, 1)
	directPeerDone := make(chan directPeerBackupCompletion, 1)
	restoreReconcileDone := make(chan managedCompletion, 1)
	peerCleanupDone := make(chan peerCleanupCompletion, 1)
	peerCleanupReconcileActive := false
	deferredBackupRetry := make(chan string, 8)
	restoreReconcileActive := false
	directPeerReconcileActive := false
	peerBackupActive := false
	runWork.Add(1)
	go func() {
		defer runWork.Done()
		ticker := time.NewTicker(cfg.ProbeInterval)
		defer ticker.Stop()
		tickStorage := func() {
			now := time.Now()
			if !now.Before(nextProbeSecretSweep) {
				if err := retireExpiredProbeSecrets(s, now); err != nil {
					log.Printf("management controller probe credential cleanup failed")
				}
				nextProbeSecretSweep = now.Add(time.Minute)
			}
			scheduleClusterReconcile()
			scheduleFenceChecks()
			scheduleQuorumReading()
			scheduleWatchdogReconcile()
			select {
			case backupTicks <- time.Now().UTC():
			default:
			}
			// The probe tick is the durable deadline timer: an applied change that was
			// not confirmed in time is rolled back here even if the in-process timer
			// armed at apply is gone with a restarted agent.
			if _, err := vmhost.RecoverManagementNetwork(probeCtx, s.dir); err != nil {
				log.Printf("management network recovery did not complete; inspect the Core record")
			}
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(probeCtx)
			storage.Tick(probeCtx, managed.Storage)
			// Only members reconcile from a master. The pass is not awaited by Run because a
			// hung remote mount can outlive its deadline; a master never starts it.
			membership, err := Membership(s)
			if err != nil {
				return
			}
			// A master change and an eject are Core state machines: every stage resumes
			// here, on both hosts, with no Controller connection at all. A member also
			// converges its own view of which host is the master, from signed evidence
			// only, so one that missed a re-point catches up when it comes back.
			runWork.Add(1)
			go func() {
				defer runWork.Done()
				// A restricted host re-establishes where authority actually is, from
				// signed evidence and live probes only, before anything else.
				ReconcilePoolRestriction(probeCtx, s, cfg.ProbeTimeout)
				AdvancePoolAuthorityTransfer(probeCtx, s, cfg.ProbeTimeout)
				if membership.MasterHostID != s.State.HostID {
					ReconcileAutomaticPoolPromotion(probeCtx, s, cfg.ProbeTimeout)
					ConvergePoolAuthority(probeCtx, s, cfg.ProbeTimeout)
				}
			}()
			if membership.MasterHostID == s.State.HostID {
				// The master numbers and persists its authority generation here, then
				// pushes the signed replica to every ready member that is behind. It is
				// single-flight and never awaited: a member that cannot be reached is
				// caught up on a later tick, and nothing waits on a replica.
				runWork.Add(1)
				go func() {
					defer runWork.Done()
					if err := PushPoolAuthorityReplicas(probeCtx, s, cfg.ProbeTimeout); err != nil && probeCtx.Err() == nil {
						log.Printf("pool authority replica push did not complete for every member")
					}
				}()
				return
			}
			if !poolReconcileRunning.CompareAndSwap(false, true) {
				return
			}
			runWork.Add(1)
			go func() {
				defer runWork.Done()
				defer poolReconcileRunning.Store(false)
				// Each repository gets its own bounded deadline. A single shared
				// deadline would let one slow NFS/SMB/GFS2 access check skip every
				// later member-access report in this pass.
				_ = reconcilePoolStorageFromMaster(probeCtx, s, cfg.ProbeTimeout)
				// Networks and media ride the same member pull pass: master owns the
				// durable desired set; members materialize locally and report access.
				// Their outcome is recorded, not discarded: a member whose VLAN or
				// shared ISO never materialized must not report itself healthy.
				recordPoolConverge(s, poolNetworkConvergeCheck, reconcilePoolNetworksFromMaster(probeCtx, s, cfg.ProbeTimeout))
				recordPoolConverge(s, poolMediaConvergeCheck, reconcilePoolMediaFromMaster(probeCtx, s, cfg.ProbeTimeout))
			}()
		}
		m.Tick(probeCtx)
		tickStorage()
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				m.Tick(probeCtx)
				tickStorage()
			}
		}
	}()
	commands := make(chan protocol.Command, 1)
	completed := make(chan managedCompletion, 1)
	var stopWorker context.CancelFunc
	var workerDone chan struct{}
	// The worker snapshots this Core's identity once. A completed pool join rewrites that
	// identity, so the worker is restarted on the new pool exactly as the host-local join
	// path restarts the embedded agent; nothing else in Run changes the worker.
	startWorker := func() {
		workerCtx, cancel := context.WithCancel(ctx)
		done := make(chan struct{})
		stopWorker, workerDone = cancel, done
		identity := s.State
		go func() { defer close(done); managedWorker(workerCtx, s.dir, identity, commands, completed) }()
	}
	startWorker()
	defer func() { stopWorker(); <-workerDone }()
	restartWorker := func() {
		stopWorker()
		<-workerDone
		startWorker()
	}
	// A staged pool join runs on this goroutine, which owns the Store, under the
	// convergence single-flight guard so no member pass sees a half-changed identity. A
	// pass already running simply leaves the work for the next heartbeat.
	joinPool := func(id string, cmd protocol.Command) error {
		if !poolReconcileRunning.CompareAndSwap(false, true) {
			return nil
		}
		join, done := context.WithTimeout(ctx, 2*time.Minute)
		result, changed := ExecutePoolJoin(join, s, cmd)
		done()
		poolReconcileRunning.Store(false)
		if _, err := completeManagedCommand(s, id, managedCompletion{result: result}, cfg.history); err != nil {
			return err
		}
		if changed {
			restartWorker()
		}
		return nil
	}
	resumePoolJoin := func() {
		if !poolReconcileRunning.CompareAndSwap(false, true) {
			return
		}
		join, done := context.WithTimeout(ctx, 2*time.Minute)
		changed := ResumePoolJoin(join, s)
		// An ejected host re-initializes as the master of its own new pool here, on the
		// goroutine that owns the Store and under the same convergence guard, because it
		// rewrites this Core's pool identity exactly as a completed join does.
		changed = ResumePoolLeave(join, s) || changed
		done()
		poolReconcileRunning.Store(false)
		if changed {
			restartWorker()
		}
	}
	active := ""
	// ponytail: one background snapshot per host; add bounded per-VM concurrency
	// only if snapshot throughput requires it. Ordinary VM control stays available.
	activeSnapshot := ""
	startBackupPreparation := func(request BackupRequest, journalID string) error {
		if request.Command == nil {
			return ErrBackupInvalid
		}
		var repo protocol.BackupRepository
		if request.PeerRepository != nil {
			repo = *request.PeerRepository
		} else if request.Command.Kind == "backup-repository-register" && request.Command.BackupRepository != nil {
			repo = *request.Command.BackupRepository
			if repo.PoolID == "" {
				repo.PoolID = s.State.PoolID
			}
		} else if request.Command.Kind == "backup-repository-test" && request.Command.BackupRepository != nil {
			var err error
			repo, err = GetPoolBackupRepository(s, request.Command.BackupRepository.ID)
			if err != nil {
				return err
			}
		} else {
			return ErrBackupInvalid
		}
		select {
		case backupPrepSlots <- struct{}{}:
		default:
			return ErrBackupUnavailable
		}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer func() { <-backupPrepSlots }()
			work, cancel := context.WithTimeout(probeCtx, 30*time.Second)
			defer cancel()
			var prepared protocol.BackupRepository
			var err error
			if request.Command.Kind == "backup-repository-test" {
				prepared, err = checkBackupRepositoryAccess(work, repo)
			} else {
				prepared, err = prepareBackupRepository(work, repo)
			}
			completion := preparedBackupRequest{request: request, journalID: journalID, err: err}
			if err == nil {
				completion.repository = &prepared
			}
			select {
			case preparedBackupDone <- completion:
			case <-probeCtx.Done():
			}
		}()
		return nil
	}
	startBackupCleanup := func(request BackupRequest, journalID string) error {
		if request.Command == nil || request.Command.TestRestoreClean == nil {
			return ErrBackupInvalid
		}
		if activeBackupCleanup[request.Command.OperationID] {
			return ErrBackupConflict
		}
		if err := reserveBackupMetadataReceipt(s, *request.Command, request.PeerRepository != nil); err != nil {
			return err
		}
		plan, err := prepareTestRestoreCleanup(s, *request.Command.TestRestoreClean, request.PeerRepository != nil)
		if err != nil {
			return err
		}
		activeBackupCleanup[request.Command.OperationID] = true
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			work, cancel := context.WithTimeout(probeCtx, 5*time.Minute)
			defer cancel()
			err := executeTestRestoreCleanupNative(work, s.dir, plan)
			select {
			case cleanupDone <- completedBackupCleanup{request: request, journalID: journalID, plan: &plan, err: err}:
			case <-probeCtx.Done():
			}
		}()
		return nil
	}
	startBackupVerification := func(request BackupRequest, journalID string) error {
		if request.Command == nil || request.Command.TestRestoreVerify == nil || request.Command.TestRestoreVerify.Valid() != nil {
			return ErrBackupInvalid
		}
		select {
		case backupPrepSlots <- struct{}{}:
		default:
			return ErrBackupUnavailable
		}
		if err := reserveBackupMetadataReceipt(s, *request.Command, false); err != nil {
			<-backupPrepSlots
			return err
		}
		plan, err := prepareTestRestoreVerification(s, *request.Command.TestRestoreVerify)
		if err != nil {
			<-backupPrepSlots
			if settleErr := settleTestVerificationProofFailure(s, *request.Command, err); settleErr != nil {
				return settleErr
			}
			return err
		}
		worker := &Store{dir: s.dir, State: copyState(s.State)}
		runWork.Add(1)
		go func() {
			defer runWork.Done()
			defer func() { <-backupPrepSlots }()
			work, cancel := context.WithTimeout(probeCtx, 30*time.Second)
			defer cancel()
			err := verifyTestRestoreNative(work, worker, plan)
			if err == nil {
				plan.VerifiedAt = time.Now().UTC()
			}
			select {
			case verificationDone <- completedBackupVerification{request: request, journalID: journalID, plan: &plan, err: err}:
			case <-probeCtx.Done():
			}
		}()
		return nil
	}
	ticker := time.NewTicker(cfg.Heartbeat)
	defer ticker.Stop()
	disconnected := false
	for {
		joined := false
		if active == "" {
			if id := nextManagedCommand(s.State.Journal, activeSnapshot, preparingBackupRepo); id != "" {
				// applyResponse already persisted the full command and admission order.
				if cmd := dispatchCommand(s.State.Journal[id], s.State.Journal); snapshotCommand(cmd.Kind) {
					activeSnapshot = id
					identity := copyState(s.State)
					runWork.Add(1)
					go func() {
						defer runWork.Done()
						result, workErr := executeStoppedSnapshot(probeCtx, s.dir, identity, cmd)
						select {
						case completed <- managedCompletion{result: result, err: workErr, background: true}:
						case <-probeCtx.Done():
						}
					}()
				} else if backupCommand(cmd.Kind) {
					if reserveErr := ensureBackupRunReserved(s, cmd); reserveErr != nil {
						result := protocol.Result{OperationID: id, Status: "needs_attention", Error: reserveErr.Error(), ObservedAt: time.Now().UTC()}
						err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
						if err != nil {
							return err
						}
						continue
					}
					run, loadErr := loadPoolBackupRun(s, id)
					if loadErr != nil {
						return loadErr
					}
					activeSnapshot = id
					if run.HostID != "" && run.HostID != s.State.HostID {
						work, workErr := peerBackupWorkForCommand(s, cmd, run)
						if workErr != nil {
							activeSnapshot = ""
							if errors.Is(workErr, ErrBackupConflict) {
								run.Status, run.Stage, run.Error = "needs_attention", "catalog_verification", "master backup catalog cannot bind the selected restore point"
								if err := SettlePoolBackupRun(s, run, nil); err != nil {
									return err
								}
								if err := completeBackgroundManagedCommand(s, managedCompletion{result: protocol.Result{
									OperationID: id, Status: "needs_attention", Error: run.Error, ObservedAt: time.Now().UTC(),
								}, background: true}, cfg.history); err != nil {
									return err
								}
								continue
							}
							log.Printf("member backup dispatch will retry: %v", workErr)
						} else {
							runWork.Add(1)
							go func() {
								defer runWork.Done()
								completion := waitPeerBackupTerminal(probeCtx, work)
								select {
								case directPeerDone <- directPeerBackupCompletion{peer: completion}:
								case <-probeCtx.Done():
								}
							}()
						}
					} else {
						identity := copyState(s.State)
						runWork.Add(1)
						go func() {
							defer runWork.Done()
							result, workErr := executeBackupCommand(probeCtx, s.dir, identity, cmd)
							select {
							case completed <- managedCompletion{result: result, err: workErr, background: true}:
							case <-probeCtx.Done():
							}
						}()
					}
				} else if cmd.Kind == "backup-repository-register" || cmd.Kind == "backup-repository-test" ||
					cmd.Kind == "backup-policy-upsert" || cmd.Kind == "backup-policy-pause" || cmd.Kind == "backup-policy-run-now" ||
					cmd.Kind == "backup-protect" || cmd.Kind == "backup-catalog-import" || cmd.Kind == "backup-catalog-rebuild" ||
					cmd.Kind == "backup-test-verify" || cmd.Kind == "backup-test-cleanup" {
					if cmd.Kind == "backup-repository-register" || cmd.Kind == "backup-repository-test" {
						if prepErr := startBackupPreparation(BackupRequest{Command: &cmd}, id); prepErr == nil {
							preparingBackupRepo[id] = true
							continue
						} else {
							result := protocol.Result{OperationID: id, Status: "failed", Error: prepErr.Error(), ObservedAt: time.Now().UTC()}
							err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
							if err != nil {
								return err
							}
							continue
						}
					}
					if cmd.Kind == "backup-test-cleanup" {
						if receipt, receiptErr := loadPoolBackupMetadataReceipt(s, id); receiptErr == nil && receipt.Status != "pending" {
							result := protocol.Result{OperationID: id, Status: receipt.Status, Error: receipt.Error, ObservedAt: receipt.ObservedAt}
							err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
							if err != nil {
								return err
							}
							continue
						}
						work, remote, lookupErr := memberCleanupWork(s, cmd)
						if lookupErr == nil && remote {
							if err := reserveBackupMetadataReceipt(s, cmd, false); err != nil {
								return err
							}
							preparingBackupRepo[id] = true
							runWork.Add(1)
							go func() {
								defer runWork.Done()
								result, workErr := waitPeerCleanup(probeCtx, work)
								select {
								case peerCleanupDone <- peerCleanupCompletion{work: work, result: result, err: workErr}:
								case <-probeCtx.Done():
								}
							}()
							continue
						}
						if lookupErr != nil && !errors.Is(lookupErr, ErrBackupNotFound) {
							log.Printf("member cleanup dispatch will retry: %v", lookupErr)
							preparingBackupRepo[id] = true
							runWork.Add(1)
							go func() {
								defer runWork.Done()
								select {
								case <-time.After(2 * time.Second):
									select {
									case deferredBackupRetry <- id:
									case <-probeCtx.Done():
									}
								case <-probeCtx.Done():
								}
							}()
							continue
						}
						if cleanupErr := startBackupCleanup(BackupRequest{Command: &cmd}, id); cleanupErr == nil {
							preparingBackupRepo[id] = true
							continue
						} else {
							result := protocol.Result{OperationID: id, Status: "failed", Error: cleanupErr.Error(), ObservedAt: time.Now().UTC()}
							err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
							if err != nil {
								return err
							}
							continue
						}
					}
					if cmd.Kind == "backup-test-verify" {
						if replay, done := terminalBackupMetadataResponse(s, BackupRequest{Command: &cmd}); done {
							result := protocol.Result{OperationID: id, Status: "failed", Error: "backup test verification failed", ObservedAt: time.Now().UTC()}
							if replay.Result != nil {
								result = *replay.Result
							} else if replay.Err != nil {
								result.Error = replay.Err.Error()
							}
							err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
							if err != nil {
								return err
							}
							continue
						}
						if verifyErr := startBackupVerification(BackupRequest{Command: &cmd}, id); verifyErr == nil {
							preparingBackupRepo[id] = true
							continue
						} else {
							result := protocol.Result{OperationID: id, Status: "failed", Error: verifyErr.Error(), ObservedAt: time.Now().UTC()}
							if replay, done := terminalBackupMetadataResponse(s, BackupRequest{Command: &cmd}); done && replay.Result != nil {
								result = *replay.Result
							}
							err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
							if err != nil {
								return err
							}
							continue
						}
					}
					response := acceptBackupRequest(ctx, s, BackupRequest{Command: &cmd})
					result := protocol.Result{OperationID: cmd.OperationID, Status: "failed", Error: "backup request failed", ObservedAt: time.Now().UTC()}
					if response.Result != nil {
						result = *response.Result
					} else if response.Err != nil {
						result.Error = response.Err.Error()
					}
					active, err = completeManagedCommand(s, id, managedCompletion{result: result}, cfg.history)
					if err != nil {
						return err
					}
				} else if cmd.Kind == "pool-join" {
					if err = joinPool(id, cmd); err != nil {
						return err
					}
					joined = true
				} else {
					active = id
					commands <- cmd
				}
			}
		}
		// Re-attempt a persisted staged join after a restart, a reboot or a Controller
		// outage: the join finishes with no Controller connection at all.
		if !joined && !hasUnsettledSnapshot(s.State.Journal, "") && !hasUnsettledBackup(s.State.Journal, "") && activeSnapshot == "" {
			resumePoolJoin()
		}
		if disconnected {
			m.Tick(probeCtx)
		}
		if err = reportStorage(ctx, s, m, storage, cfg); err != nil && ctx.Err() == nil {
			var diskError persistenceError
			if errors.As(err, &diskError) {
				return err
			}
			if resetErr := resetReportExtensions(s); resetErr != nil {
				return persistenceError{resetErr}
			}
			log.Printf("agent report: %v", err)
			disconnected = true
		} else if err == nil && disconnected {
			m.Tick(probeCtx)
			disconnected = false
		}
		select {
		case <-ctx.Done():
			return nil
		case done := <-completed:
			wasSnapshot := done.background && done.result.OperationID == activeSnapshot
			active, err = completeManagedCommand(s, active, done, cfg.history)
			if err != nil {
				return err
			}
			if wasSnapshot {
				entry := s.State.Journal[done.result.OperationID]
				if entry.Status != "pending" && (entry.Status != "needs_attention" ||
					entry.Command != nil && backupCommand(entry.Command.Kind)) {
					activeSnapshot = ""
				}
			}
			m.Tick(probeCtx)
		case request := <-cfg.SnapshotRequests:
			response := acceptSnapshotRequest(ctx, s, request)
			var diskError persistenceError
			if errors.As(response.Err, &diskError) {
				return diskError
			}
			select {
			case request.Reply <- response:
			case <-ctx.Done():
				return nil
			}
		case request := <-cfg.BackupRequests:
			if request.Command != nil && request.Command.Kind == "backup-test-verify" {
				if replay, done := terminalBackupMetadataResponse(s, request); done {
					request.Reply <- replay
					break
				}
				if err := startBackupVerification(request, ""); err == nil {
					break
				} else {
					if replay, done := terminalBackupMetadataResponse(s, request); done {
						request.Reply <- replay
					} else {
						request.Reply <- BackupResponse{Err: err}
					}
					break
				}
			}
			if request.Command != nil && request.Command.Kind == "backup-test-cleanup" {
				if replay, done := terminalBackupMetadataResponse(s, request); done {
					request.Reply <- replay
					break
				}
				if err := startBackupCleanup(request, ""); err == nil {
					break
				} else {
					request.Reply <- BackupResponse{Err: err}
					break
				}
			}
			if backupNeedsPreparation(request) {
				if err := startBackupPreparation(request, ""); err == nil {
					break
				} else {
					request.Reply <- BackupResponse{Err: err}
					break
				}
			}
			response := acceptBackupRequest(ctx, s, request)
			var diskError persistenceError
			if errors.As(response.Err, &diskError) {
				return diskError
			}
			select {
			case request.Reply <- response:
			case <-ctx.Done():
				return nil
			}
		case request := <-peerRequests:
			if request.Command != nil && request.Command.Kind == "backup-test-verify" {
				if replay, done := terminalBackupMetadataResponse(s, request); done {
					request.Reply <- replay
					break
				}
				if err := startBackupVerification(request, ""); err == nil {
					break
				} else {
					if replay, done := terminalBackupMetadataResponse(s, request); done {
						request.Reply <- replay
					} else {
						request.Reply <- BackupResponse{Err: err}
					}
					break
				}
			}
			if request.Command != nil && request.Command.Kind == "backup-test-cleanup" {
				if replay, done := terminalBackupMetadataResponse(s, request); done {
					request.Reply <- replay
					break
				}
				if err := startBackupCleanup(request, ""); err == nil {
					break
				} else {
					request.Reply <- BackupResponse{Err: err}
					break
				}
			}
			if backupNeedsPreparation(request) {
				if err := startBackupPreparation(request, ""); err == nil {
					break
				} else {
					request.Reply <- BackupResponse{Err: err}
					break
				}
			}
			response := acceptBackupRequest(ctx, s, request)
			select {
			case request.Reply <- response:
			case <-ctx.Done():
				return nil
			}
		case completion := <-preparedBackupDone:
			completion.request.PreparedRepository = completion.repository
			response := BackupResponse{Err: completion.err}
			if completion.err == nil {
				response = acceptBackupRequest(ctx, s, completion.request)
			}
			if completion.journalID != "" {
				delete(preparingBackupRepo, completion.journalID)
				result := protocol.Result{OperationID: completion.journalID, Status: "failed", Error: "backup repository preparation failed", ObservedAt: time.Now().UTC()}
				if response.Result != nil {
					result = *response.Result
				} else if response.Err != nil {
					result.Error = response.Err.Error()
				}
				err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
				if err != nil {
					return err
				}
			} else {
				select {
				case completion.request.Reply <- response:
				case <-ctx.Done():
					return nil
				}
			}
		case completion := <-verificationDone:
			response := BackupResponse{Err: completion.err}
			if completion.err == nil {
				completion.request.PreparedVerification = completion.plan
				response = acceptBackupRequest(ctx, s, completion.request)
			} else if settleErr := settleTestVerificationProofFailure(s, *completion.request.Command, completion.err); settleErr != nil {
				response.Err = settleErr
			} else {
				receipt, loadErr := loadPoolBackupMetadataReceipt(s, completion.request.Command.OperationID)
				if loadErr != nil {
					response.Err = loadErr
				} else {
					response = BackupResponse{Result: &protocol.Result{OperationID: receipt.OperationID, Status: receipt.Status, Error: receipt.Error, ObservedAt: receipt.ObservedAt}}
				}
			}
			if completion.journalID != "" {
				delete(preparingBackupRepo, completion.journalID)
				result := protocol.Result{OperationID: completion.journalID, Status: "failed", Error: "backup test verification failed", ObservedAt: time.Now().UTC()}
				if response.Result != nil {
					result = *response.Result
				} else if response.Err != nil {
					result.Error = response.Err.Error()
				}
				err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
				if err != nil {
					return err
				}
			} else {
				select {
				case completion.request.Reply <- response:
				case <-ctx.Done():
					return nil
				}
			}
		case completion := <-cleanupDone:
			delete(activeBackupCleanup, completion.request.Command.OperationID)
			if completion.err != nil {
				cleanupNextAttempt[completion.request.Command.OperationID] = time.Now().Add(30 * time.Second)
			}
			response := BackupResponse{Err: completion.err}
			if completion.err == nil {
				completion.request.PreparedCleanup = completion.plan
				response = acceptBackupRequest(ctx, s, completion.request)
			}
			if completion.journalID != "" {
				delete(preparingBackupRepo, completion.journalID)
				result := protocol.Result{OperationID: completion.journalID, Status: "needs_attention", Error: "backup test cleanup needs reconciliation", ObservedAt: time.Now().UTC()}
				if response.Result != nil {
					result = *response.Result
				} else if response.Err != nil {
					result.Error = response.Err.Error()
				}
				err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
				if err != nil {
					return err
				}
			} else {
				select {
				case completion.request.Reply <- response:
				case <-ctx.Done():
					return nil
				}
			}
		case completion := <-peerCleanupDone:
			peerCleanupReconcileActive = false
			id := completion.work.command.OperationID
			delete(preparingBackupRepo, id)
			result := completion.result
			if completion.err != nil {
				var permanent permanentPeerBackupError
				if !errors.As(completion.err, &permanent) {
					log.Printf("member test cleanup remains pending: %v", completion.err)
					break
				}
				result = protocol.Result{OperationID: id, Status: "needs_attention", Error: completion.err.Error(), ObservedAt: time.Now().UTC()}
			}
			if result.Status == "succeeded" {
				if err := finishMemberTestRestoreCleanup(s, *completion.work.command.TestRestoreClean, completion.work.ownerRun); err != nil {
					result.Status, result.Error = "needs_attention", err.Error()
				}
			}
			if result.ObservedAt.IsZero() {
				result.ObservedAt = time.Now().UTC()
			}
			poolBackupAuthorityMu.Lock()
			receipt, receiptErr := loadPoolBackupMetadataReceipt(s, id)
			if receiptErr == nil && receipt.IntentSHA256 == completion.work.intent && receipt.Kind == "backup-test-cleanup" {
				receipt.Status, receipt.Error, receipt.ObservedAt = result.Status, result.Error, result.ObservedAt
				receiptErr = savePoolBackupMetadataReceipt(s, receipt)
			} else if receiptErr == nil {
				receiptErr = ErrBackupConflict
			}
			poolBackupAuthorityMu.Unlock()
			if receiptErr != nil {
				return receiptErr
			}
			err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
			if err != nil {
				return err
			}
		case id := <-deferredBackupRetry:
			delete(preparingBackupRepo, id)
		case now := <-backupTicks:
			if err := rehydratePendingTestCleanup(s); err != nil && ctx.Err() == nil {
				log.Printf("pending test cleanup could not be recovered: %v", err)
			}
			if err := reconcileTerminalBackupRestorePins(s); err != nil && ctx.Err() == nil {
				log.Printf("terminal backup restore pins could not be reconciled: %v", err)
			}
			if _, err := ApplyBackupScheduleTick(s, now); err != nil && ctx.Err() == nil {
				log.Printf("backup schedule tick did not complete")
			}
			if !peerCleanupReconcileActive {
				for _, entry := range s.State.Journal {
					if entry.Status != "needs_attention" || entry.Command == nil || entry.Command.Kind != "backup-test-cleanup" {
						continue
					}
					work, remote, workErr := memberCleanupWork(s, *entry.Command)
					if workErr != nil || !remote {
						continue
					}
					peerCleanupReconcileActive = true
					runWork.Add(1)
					go func() {
						defer runWork.Done()
						result, err := waitPeerCleanupReadOnly(probeCtx, work)
						select {
						case peerCleanupDone <- peerCleanupCompletion{work: work, result: result, err: err}:
						case <-probeCtx.Done():
						}
					}()
					break
				}
			}
			for _, entry := range s.State.Journal {
				if entry.Status != "needs_attention" || entry.Command == nil || entry.Command.Kind != "backup-test-cleanup" ||
					activeBackupCleanup[entry.Command.OperationID] || now.Before(cleanupNextAttempt[entry.Command.OperationID]) {
					continue
				}
				if replay, done := terminalBackupMetadataResponse(s, BackupRequest{Command: entry.Command}); done && replay.Result != nil &&
					(replay.Result.Status == "succeeded" || replay.Result.Status == "failed") {
					err = completeBackgroundManagedCommand(s, managedCompletion{result: *replay.Result, background: true}, cfg.history)
					if err != nil {
						return err
					}
					break
				}
				_, remote, targetErr := memberCleanupWork(s, *entry.Command)
				if targetErr != nil || remote {
					continue
				}
				if err := startBackupCleanup(BackupRequest{Command: entry.Command}, entry.Command.OperationID); err != nil {
					cleanupNextAttempt[entry.Command.OperationID] = now.Add(30 * time.Second)
				}
				break
			}
			if !restoreReconcileActive && activeSnapshot == "" {
				for _, entry := range s.State.Journal {
					if entry.Status != "needs_attention" || entry.Command == nil ||
						entry.Command.Kind != protocol.BackupKindRestoreReplace && entry.Command.Kind != protocol.BackupKindRestoreNew &&
							entry.Command.Kind != protocol.BackupKindTestRestore {
						continue
					}
					cmd := *entry.Command
					run, runErr := loadPoolBackupRun(s, cmd.OperationID)
					if runErr != nil || run.HostID != s.State.HostID {
						continue
					}
					restoreReconcileActive = true
					identity := copyState(s.State)
					runWork.Add(1)
					go func() {
						defer runWork.Done()
						var result protocol.Result
						var workErr error
						switch cmd.Kind {
						case protocol.BackupKindRestoreReplace:
							result, workErr = reconcileRestoreReplaceAttention(probeCtx, s.dir, identity, cmd)
						case protocol.BackupKindRestoreNew:
							result, workErr = reconcileRestoreAsNewAttention(probeCtx, s.dir, identity, cmd)
						case protocol.BackupKindTestRestore:
							result, workErr = reconcileTestRestoreAttention(probeCtx, s.dir, identity, cmd)
						}
						select {
						case restoreReconcileDone <- managedCompletion{result: result, err: workErr, background: true}:
						case <-probeCtx.Done():
						}
					}()
					break
				}
			}
			if !directPeerReconcileActive && activeSnapshot == "" {
				for _, entry := range s.State.Journal {
					if entry.Status != "needs_attention" || entry.Command == nil || !backupCommand(entry.Command.Kind) {
						continue
					}
					run, runErr := loadPoolBackupRun(s, entry.Command.OperationID)
					if runErr != nil || run.HostID == "" || run.HostID == s.State.HostID {
						continue
					}
					work, workErr := peerBackupWorkForCommand(s, *entry.Command, run)
					if workErr != nil {
						continue
					}
					directPeerReconcileActive = true
					activeSnapshot = run.OperationID
					runWork.Add(1)
					go func() {
						defer runWork.Done()
						completion := waitPeerBackupReconciled(probeCtx, work)
						select {
						case directPeerDone <- directPeerBackupCompletion{peer: completion}:
						case <-probeCtx.Done():
						}
					}()
					break
				}
			}
			if !peerBackupActive {
				work, ready, err := nextPendingPeerBackup(s)
				if err != nil && ctx.Err() == nil {
					log.Printf("member backup dispatch could not be prepared: %v", err)
				} else if ready {
					peerBackupActive = true
					runWork.Add(1)
					go func() {
						defer runWork.Done()
						completion := callPeerBackup(probeCtx, work)
						select {
						case peerBackupDone <- completion:
						case <-probeCtx.Done():
						}
					}()
				}
			}
		case completion := <-peerBackupDone:
			peerBackupActive = false
			if completion.err != nil {
				var permanent permanentPeerBackupError
				if errors.As(completion.err, &permanent) {
					run := completion.run
					run.Status, run.Stage, run.Error = "needs_attention", "peer_verification", completion.err.Error()
					if settleErr := SettlePoolBackupRun(s, run, nil); settleErr != nil {
						log.Printf("member backup occurrence remains pending: %v", settleErr)
					}
				}
				log.Printf("member backup occurrence remains pending: %v", completion.err)
				break
			}
			status := completion.reply.Result.Status
			if status != "succeeded" && status != "failed" && status != "needs_attention" {
				break
			}
			run := completion.run
			run.Status, run.Error = status, completion.reply.Result.Error
			if completion.reply.Run != nil {
				run.Stage = completion.reply.Run.Stage
			} else {
				run.Stage = status
			}
			if err := SettlePoolBackupRun(s, run, completion.reply.Point); err != nil {
				log.Printf("member backup occurrence remains pending: %v", err)
			}
		case done := <-directPeerDone:
			directPeerReconcileActive = false
			peer := done.peer
			if peer.err != nil {
				var permanent permanentPeerBackupError
				if !errors.As(peer.err, &permanent) {
					activeSnapshot = ""
					break
				}
			}
			result := peer.reply.Result
			if peer.err != nil {
				result = protocol.Result{OperationID: peer.run.OperationID, Status: "needs_attention", Error: peer.err.Error(), ObservedAt: time.Now().UTC()}
			}
			run := peer.run
			run.Status, run.Error = result.Status, result.Error
			if peer.err == nil && result.Status == "succeeded" {
				if proofErr := verifyMemberRestoreOwnerRecord(s, run); proofErr != nil {
					result.Status, result.Error = "needs_attention", proofErr.Error()
					run.Status, run.Error = result.Status, result.Error
				}
			}
			if peer.reply.Run != nil && peer.err == nil {
				run.Stage = peer.reply.Run.Stage
			} else {
				run.Stage = run.Status
			}
			if peer.err == nil {
				if settleErr := SettlePoolBackupRun(s, run, peer.reply.Point); settleErr != nil {
					result.Status, result.Error = "needs_attention", settleErr.Error()
				} else if clearPeerRestorePinAfterSettlement(run.Kind, result.Status) {
					if clearErr := clearBackupRestorePin(s, run.RecoveryPointID, run.OperationID); clearErr != nil {
						result.Status, result.Error = "needs_attention", clearErr.Error()
					}
				}
			}
			err = completeBackgroundManagedCommand(s, managedCompletion{result: result, background: true}, cfg.history)
			if err != nil {
				return err
			}
			activeSnapshot = ""
		case done := <-restoreReconcileDone:
			restoreReconcileActive = false
			err = completeBackgroundManagedCommand(s, done, cfg.history)
			if err != nil {
				return err
			}
		case <-ticker.C:
		}
	}
}

func clearPeerRestorePinAfterSettlement(kind, status string) bool {
	return kind != protocol.BackupKindBackupNow &&
		(status == "failed" || status == "succeeded" && kind != protocol.BackupKindTestRestore)
}

func verifyMemberRestoreOwnerRecord(s *Store, run protocol.BackupRun) error {
	if run.Kind != protocol.BackupKindRestoreNew && run.Kind != protocol.BackupKindTestRestore &&
		run.Kind != protocol.BackupKindRestoreReplace {
		return nil
	}
	poolVMMu.Lock()
	defer poolVMMu.Unlock()
	state, err := loadPoolVMs(s)
	if err != nil {
		return err
	}
	vmID := run.NewVMID
	if run.Kind == protocol.BackupKindRestoreReplace {
		vmID = run.VMID
	}
	record, ok := state.VMs[vmID]
	if !ok || record.VMID != vmID || record.PoolID != run.PoolID || record.HostID != run.HostID ||
		record.Spec.ID != vmID || record.Spec.HostID != run.HostID || record.PendingRecovery != nil || record.PendingUpdate != nil {
		return errors.New("member restore has no matching durable master VM record")
	}
	return nil
}

// completeManagedCommand runs only on the heartbeat owner of Store. Background
// recovery may finish while a different command is queued for the worker.
func completeBackgroundManagedCommand(s *Store, done managedCompletion, history *usageHistory) error {
	_, err := completeManagedCommand(s, "", done, history)
	return err
}

func completeManagedCommand(s *Store, active string, done managedCompletion, history *usageHistory) (string, error) {
	id := active
	if done.background {
		id = done.result.OperationID
	}
	if id == "" {
		return active, errors.New("invalid local managed command completion")
	}
	entry, found := s.State.Journal[id]
	if done.background && (!found || entry.Status == "succeeded") {
		// Completed aggregates outlive transport acknowledgements. Do not invent
		// a missing command or re-publish an already settled result every tick.
		return active, nil
	}
	if !found {
		return active, errors.New("local managed command completion has no journal entry")
	}
	if done.err != nil {
		entry.Status = "needs_attention"
		entry.Error = "local execution or persistence failed; inspect retained managed VM journal before recovery"
		entry.ObservedAt = time.Now().UTC()
	} else {
		if done.result.OperationID != id || (done.result.Status != "succeeded" && done.result.Status != "failed" && done.result.Status != "needs_attention") {
			return active, errors.New("invalid local managed command completion")
		}
		entry.Status, entry.Error, entry.ObservedAt = done.result.Status, done.result.Error, done.result.ObservedAt
		forgetDeletedVM(history, entry)
	}
	if done.background {
		entry.Acknowledged = false
	}
	s.State.Journal[id] = entry
	if err := s.Save(); err != nil {
		return active, persistenceError{err}
	}
	if !done.background && id == active {
		active = ""
	}
	return active, nil
}

// Controller bounds each error to 4096 UTF-8 bytes; keep full details in the journal.
func resultError(detail string) string {
	detail = strings.ToValidUTF8(detail, "�")
	if len(detail) > 4096 {
		detail = detail[:4096]
		for !utf8.ValidString(detail) {
			detail = detail[:len(detail)-1]
		}
	}
	return detail
}

// Admission order survives restarts and wall-clock adjustments. Older entries have no
// order, so drain those first using their original receipt time and a stable tie-break.
func nextManagedCommand(journal map[string]JournalEntry, activeSnapshot string, skip ...map[string]bool) string {
	oldest := ""
	oldestSnapshotHeal := ""
	blockJoin := activeSnapshot != "" || hasUnsettledSnapshot(journal, "") || hasUnsettledBackup(journal, "")
	for id, entry := range journal {
		if len(skip) != 0 && skip[0][id] {
			continue
		}
		if entry.Command == nil || !managedCommand(entry.Command.Kind) {
			continue
		}
		// A backup with an uncertain native outcome needs observation before any
		// retry. Re-entering its worker can write the same VM or point twice.
		snapshotHeal := entry.Status == "needs_attention" && snapshotCommand(entry.Command.Kind)
		if snapshotHeal {
			if activeSnapshot != "" && id != activeSnapshot {
				continue
			}
		} else if entry.Status != "pending" {
			continue
		}
		if (snapshotCommand(entry.Command.Kind) || backupCommand(entry.Command.Kind)) && activeSnapshot != "" {
			// Keep the in-flight snapshot/backup exclusive; only re-enter it when it needs attention.
			if id != activeSnapshot || entry.Status != "needs_attention" {
				continue
			}
		}
		if backupCommand(entry.Command.Kind) && hasEarlierUnsettledBackup(journal, id, entry) {
			continue
		}
		if entry.Command.Kind == "pool-join" && blockJoin {
			continue
		}
		if vmID := managedCommandVMID(entry.Command); vmID != "" {
			emergency := entry.Command.Power != nil && (entry.Command.Power.Action == "shutdown" || entry.Command.Power.Action == "force-stop")
			if !emergency && !snapshotCommand(entry.Command.Kind) && !backupCommand(entry.Command.Kind) && (hasUnsettledSnapshot(journal, vmID) || hasUnsettledBackup(journal, vmID)) {
				continue
			}
		}
		if entry.Command.Power != nil {
			power := entry.Command.Power
			emergency := power.Action == "shutdown" || power.Action == "force-stop"
			blocked := false
			for _, predecessorID := range []string{entry.AfterOperationID, power.AfterOperationID} {
				if predecessorID == "" {
					continue
				}
				before := journal[predecessorID]
				if before.Command == nil || before.Status != "pending" && before.Status != "needs_attention" {
					continue
				}
				if emergency && snapshotCommand(before.Command.Kind) {
					continue
				}
				blocked = true
				break
			}
			if blocked {
				continue
			}
		}
		if snapshotHeal {
			old := journal[oldestSnapshotHeal]
			if oldestSnapshotHeal == "" || entry.Order < old.Order || entry.Order == old.Order &&
				(entry.RequestedAt.Before(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id < oldestSnapshotHeal) {
				oldestSnapshotHeal = id
			}
			continue
		}
		old := journal[oldest]
		if oldest == "" || entry.Order < old.Order || entry.Order == old.Order &&
			(entry.RequestedAt.Before(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id < oldest) {
			oldest = id
		}
	}
	if oldest != "" {
		return oldest
	}
	return oldestSnapshotHeal
}

func hasEarlierUnsettledBackup(journal map[string]JournalEntry, id string, entry JournalEntry) bool {
	target := managedCommandVMID(entry.Command)
	if target == "" {
		return false
	}
	for otherID, other := range journal {
		if otherID == id || other.Command == nil || !backupCommand(other.Command.Kind) || managedCommandVMID(other.Command) != target {
			continue
		}
		if other.Status == "needs_attention" || other.Status == "pending" && (other.Order < entry.Order || other.Order == entry.Order && otherID < id) {
			return true
		}
	}
	return false
}

func managedCommandVMID(cmd *protocol.Command) string {
	if cmd == nil {
		return ""
	}
	switch {
	case cmd.VM != nil:
		return cmd.VM.ID
	case cmd.Restore != nil:
		return cmd.Restore.NewVMID
	case cmd.RestoreReplace != nil:
		return cmd.RestoreReplace.OriginalVMID
	case cmd.TestRestore != nil:
		return cmd.TestRestore.NewVMID
	case cmd.Power != nil:
		return cmd.Power.VMID
	case cmd.VMMedia != nil:
		return cmd.VMMedia.VMID
	case cmd.Delete != nil:
		return cmd.Delete.VMID
	default:
		return ""
	}
}

// The Core queue is inventory too: a replacement Controller can display accepted work
// without reconstructing commands or dispatching them again. Engine outcomes win when
// execution has settled but its completion has not yet reached the agent journal.
func commandOutcomes(journal map[string]JournalEntry, outcomes []protocol.ManagedOutcome) []protocol.ManagedOutcome {
	filtered := make([]protocol.ManagedOutcome, 0, len(outcomes))
	for _, outcome := range outcomes {
		entry, ok := journal[outcome.OperationID]
		if ok && entry.Command != nil && backupCommand(entry.Command.Kind) {
			// A native vm-create shares the restore operation ID. The pool backup
			// journal is its authoritative Controller-facing outcome.
			continue
		}
		filtered = append(filtered, outcome)
	}
	outcomes = filtered
	seen := make(map[string]bool, len(outcomes))
	for _, outcome := range outcomes {
		seen[outcome.OperationID] = true
	}
	ids := make([]string, 0, len(journal))
	for id := range journal {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		entry := journal[id]
		cmd := entry.Command
		if seen[id] || cmd == nil || entry.PeerDispatched || !managedCommand(cmd.Kind) {
			continue
		}
		outcome := protocol.ManagedOutcome{OperationID: id, Kind: cmd.Kind, Status: entry.Status, Error: resultError(entry.Error)}
		if entry.Status == "pending" {
			outcome.Status = "queued"
		}
		switch {
		case cmd.Storage != nil:
			outcome.ID, outcome.Name = cmd.Storage.ID, cmd.Storage.Name
		case cmd.StorageLifecycle != nil:
			outcome.ID = cmd.StorageLifecycle.RepositoryID
			if cmd.StorageLifecycle.Registration != nil {
				outcome.Name = cmd.StorageLifecycle.Registration.Name
			}
		case cmd.CredentialRetire != nil:
			outcome.ID = cmd.CredentialRetire.Receipt
		case cmd.Network != nil:
			outcome.ID, outcome.Name = cmd.Network.ID, cmd.Network.Name
		case cmd.Media != nil:
			outcome.ID, outcome.Name = cmd.Media.ID, cmd.Media.Name
		case cmd.BackupRepository != nil:
			outcome.ID, outcome.Name = cmd.BackupRepository.ID, cmd.BackupRepository.Name
		case cmd.BackupPolicy != nil:
			outcome.ID, outcome.Name = cmd.BackupPolicy.ID, cmd.BackupPolicy.Name
		case cmd.BackupProtect != nil:
			outcome.ID = cmd.BackupProtect.RecoveryPointID
		case cmd.CatalogImport != nil:
			outcome.ID = cmd.CatalogImport.RepositoryID
		case cmd.TestRestoreVerify != nil:
			outcome.ID = cmd.TestRestoreVerify.TestVMID
		case cmd.TestRestoreClean != nil:
			outcome.ID = cmd.TestRestoreClean.TestVMID
		case cmd.VM != nil:
			outcome.ID, outcome.Name = cmd.VM.ID, cmd.VM.Name
		case cmd.Restore != nil:
			outcome.ID, outcome.Name = cmd.Restore.NewVMID, cmd.Restore.NewVMName
		case cmd.RestoreReplace != nil:
			outcome.ID = cmd.RestoreReplace.OriginalVMID
		case cmd.TestRestore != nil:
			outcome.ID, outcome.Name = cmd.TestRestore.NewVMID, cmd.TestRestore.NewVMName
		case cmd.Power != nil:
			outcome.ID = cmd.Power.VMID
		case cmd.VMMedia != nil:
			outcome.ID = cmd.VMMedia.VMID
		default:
			continue
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes
}

// A newly adopted Controller may not know an older command's payload. Core keeps
// the dependency itself without changing the original transport replay identity.
func pendingVMCommand(journal map[string]JournalEntry, vmID string) string {
	latest := ""
	for id, entry := range journal {
		if entry.Command == nil || (entry.Status != "pending" && !(entry.Status == "needs_attention" && snapshotCommand(entry.Command.Kind))) {
			continue
		}
		target := managedCommandVMID(entry.Command)
		old := journal[latest]
		if target == vmID && (latest == "" || entry.Order > old.Order || entry.Order == old.Order &&
			(entry.RequestedAt.After(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id > latest)) {
			latest = id
		}
	}
	return latest
}

func pendingVMDefinitionCommand(journal map[string]JournalEntry, vmID string) string {
	latest := ""
	for id, entry := range journal {
		if entry.Command == nil || entry.Status != "pending" || !vmDefinitionCommand(entry.Command.Kind) {
			continue
		}
		if managedCommandVMID(entry.Command) != vmID {
			continue
		}
		old := journal[latest]
		if latest == "" || entry.Order > old.Order || entry.Order == old.Order &&
			(entry.RequestedAt.After(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id > latest) {
			latest = id
		}
	}
	return latest
}

func vmDefinitionCommand(kind string) bool {
	return kind == "vm-update" || kind == "vm-delete" || kind == "vm-media" || kind == "vm-create"
}

func dispatchCommand(entry JournalEntry, journal map[string]JournalEntry) protocol.Command {
	cmd := *entry.Command
	if cmd.Power != nil {
		power := *cmd.Power
		if power.AfterOperationID == "" {
			power.AfterOperationID = entry.AfterOperationID
		} else if power.Action == "shutdown" || power.Action == "force-stop" {
			if before := journal[power.AfterOperationID]; before.Command != nil && snapshotCommand(before.Command.Kind) {
				power.AfterOperationID = ""
			}
		}
		cmd.Power = &power
	}
	return cmd
}
