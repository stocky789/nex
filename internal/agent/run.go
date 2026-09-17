// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/version"
	"nex.local/nex/internal/vmhost"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

type persistenceError struct{ error }

var transientStorageDiscoveries = struct {
	sync.Mutex
	byStore map[*Store][]protocol.StorageDiscoveryResult
}{byStore: map[*Store][]protocol.StorageDiscoveryResult{}}

var transientStorageClusters = struct {
	sync.Mutex
	byStore map[*Store][]protocol.StorageClusterResult
}{byStore: map[*Store][]protocol.StorageClusterResult{}}

const transientStorageDiscoveryLimit = 128
const transientStorageClusterLimit = 128

func drainStorageDiscoveries(s *Store) []protocol.StorageDiscoveryResult {
	transientStorageDiscoveries.Lock()
	defer transientStorageDiscoveries.Unlock()
	out := transientStorageDiscoveries.byStore[s]
	delete(transientStorageDiscoveries.byStore, s)
	return out
}
func appendStorageDiscoveries(s *Store, results []protocol.StorageDiscoveryResult) {
	transientStorageDiscoveries.Lock()
	defer transientStorageDiscoveries.Unlock()
	current := transientStorageDiscoveries.byStore[s]
	room := transientStorageDiscoveryLimit - len(current)
	if room > 0 {
		if len(results) > room {
			results = results[:room]
		}
		transientStorageDiscoveries.byStore[s] = append(current, results...)
	}
}

func drainStorageClusters(s *Store) []protocol.StorageClusterResult {
	transientStorageClusters.Lock()
	defer transientStorageClusters.Unlock()
	out := transientStorageClusters.byStore[s]
	delete(transientStorageClusters.byStore, s)
	return out
}
func appendStorageClusters(s *Store, results []protocol.StorageClusterResult) {
	transientStorageClusters.Lock()
	defer transientStorageClusters.Unlock()
	current := transientStorageClusters.byStore[s]
	room := transientStorageClusterLimit - len(current)
	if room > 0 {
		if len(results) > room {
			results = results[:room]
		}
		transientStorageClusters.byStore[s] = append(current, results...)
	}
}

func resetStorageClusterProtocol(s *Store) error {
	if !s.State.StorageClusterProtocolReady && !s.State.StorageClusterSessionMonitorProtocolReady {
		return nil
	}
	s.State.StorageClusterProtocolReady = false
	s.State.StorageClusterSessionMonitorProtocolReady = false
	return s.Save()
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

type RunConfig struct {
	Heartbeat, ProbeInterval, ProbeTimeout time.Duration
	history                                *usageHistory // set by Run; answers stats channels
}

// ApplianceJournalDir is the local appliance engine's deploy journal (nex-core-local's
// data directory plus /vmhost). Its deployments are reported as this host's appliances.
// The packaged default matches nex-core-local's default data directory; nex-core-local
// itself sets it from its configuration before starting the embedded agent.
var ApplianceJournalDir = "/var/lib/nex/core-local/vmhost"

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
		e := JournalEntry{Order: order, Status: "pending", RequestedAt: time.Now(), Command: &cmd}
		if cmd.Power != nil && cmd.Power.AfterOperationID == "" {
			e.AfterOperationID = pendingVMCommand(s.State.Journal, cmd.Power.VMID)
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
	inv := m.Snapshot()
	// Libvirt can stall while a backing repository is unavailable. Bound only this
	// observational read: the caller's report context must remain usable for the
	// outbound heartbeat that records the uncertainty.
	appliedCtx, cancelApplied := context.WithTimeout(ctx, cfg.ProbeTimeout)
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(appliedCtx)
	cancelApplied()
	managed.Storage = PoolStorageMemberAccess(s, managed.Storage)
	if check := PoolStorageReconcileCheck(s); check != nil {
		checks = append(checks, *check)
	}
	// Membership is a master-owned Core record (with the member's authenticated copy).
	// Reporting it is the sole Controller adoption path; no Controller cache derives it.
	if membership, err := Membership(s); err == nil {
		managed.Membership = &membership
	}
	if storage != nil {
		managed.Storage = storage.Overlay(managed.Storage)
	}
	credentialsReadable := true
	if _, err := vmhost.StorageCredentialReceipts(s.dir); err != nil {
		credentialsReadable = false
		checks = append(checks, protocol.Check{Name: "storage-credentials", Status: "failed", Error: "storage credential state is unavailable", ObservedAt: time.Now().UTC()})
	}
	managed.Appliances = vmhost.Deployments(ApplianceJournalDir)
	confirmCtx, cancelConfirm := context.WithTimeout(ctx, cfg.ProbeTimeout)
	managed.Outcomes = confirmPoolVMUpdateOutcomes(confirmCtx, s, commandOutcomes(s.State.Journal, managed.Outcomes), managed.Storage)
	cancelConfirm()
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
	capabilities := []string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.PowerQueueCapability, protocol.MediaCapability, protocol.ISODownloadCapability, protocol.UpdateCapability, protocol.DeleteCapability, protocol.DriversDriveCapability, protocol.UsageHistoryCapability, protocol.CPUBaselineCapability}
	if s.State.StorageProtocolLegacy {
		version = protocol.LegacyVersion
		capabilities = []string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.MediaCapability, protocol.UpdateCapability, protocol.DeleteCapability, protocol.UsageHistoryCapability}
		// A strict v1 decoder must not see v2 managed storage/receipt fields.
		legacy := *inv
		legacy.Managed = nil
		legacy.CPUBaseline = nil
		inv = &legacy
	}
	if s.State.StorageProtocolReady {
		capabilities = append(capabilities, "storage-repositories-v1", protocol.StorageDiscoveryCapability)
	}
	if s.State.StorageClusterProtocolReady {
		capabilities = append(capabilities, protocol.StorageClusterCapability)
	}
	if version == protocol.Version {
		// Offer the additive extension during every v2 preflight. An older
		// Controller ignores it and acknowledges the base capability instead.
		capabilities = append(capabilities, protocol.StorageClusterSessionMonitorCapability)
	}
	discoveries := drainStorageDiscoveries(s)
	var clusters []protocol.StorageClusterResult
	if s.State.StorageClusterProtocolReady {
		clusters = drainStorageClusters(s)
		clusters = storageClusterResultsForController(clusters, s.State.StorageClusterSessionMonitorProtocolReady)
	}
	req := protocol.ReportRequest{Capabilities: capabilities, Version: version, Sequence: s.State.Sequence, Inventory: inv, Results: []protocol.Result{}, StorageDiscoveries: discoveries, StorageClusters: clusters}
	for id, e := range s.State.Journal {
		if e.Status != "pending" && !e.Acknowledged {
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
	if err = post(ctx, c, s.State.Controller, "/api/v1/agent/report", req, &response); err != nil {
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
	if !s.State.StorageProtocolReady && response.StorageRepositories != nil && response.StorageRepositories.ProtocolVersion == protocol.Version && response.StorageRepositories.Capability == protocol.StorageRepositoryCapability {
		s.State.StorageProtocolReady = true
		if err = s.Save(); err != nil {
			return persistenceError{fmt.Errorf("persist storage protocol preflight: %w", err)}
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
				results = append(results, vmhost.DiscoverStorage(work, request))
				cancel()
			}
			appendStorageDiscoveries(s, results)
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
			appendStorageClusters(s, results)
		}(append([]protocol.StorageClusterRequest(nil), response.StorageClusterRequests...))
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
			if err := serveStatsChannel(ctx, s.State, req, cfg.history); err != nil {
				log.Printf("stats channel: %v", err)
			}
		}(channel)
	}
	if len(response.Commands) > 0 {
		m.Tick(ctx)
	}
	return nil
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
		if outcome.Kind == "storage-register" || outcome.Kind == "storage-update" || outcome.Kind == "storage-recheck" || outcome.Kind == "storage-detach" || outcome.Kind == "storage-credential-retire" {
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
	// A single-host pool is still a pool: record this Core as its master before any
	// pool work. On failure pool operations stay refused; running VMs are unaffected.
	if err := EnsurePoolMembership(s); err != nil {
		log.Printf("pool membership could not be recorded; pool operations stay refused until it is")
	}
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	c.CloseIdleConnections()
	// A stored acknowledgement belongs to the prior Controller connection. Never
	// send a newer clustered-storage envelope until this connection acknowledges
	// its exact capability again.
	if err := resetStorageClusterProtocol(s); err != nil {
		return err
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
	m := NewMonitor(cfg.ProbeTimeout, probes)
	storage := vmhost.NewStorageMonitor(cfg.ProbeTimeout)
	var poolReconcileRunning atomic.Bool
	var clusterReconcileRunning atomic.Bool
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
			membership, err := Membership(s)
			if err != nil {
				return
			}
			if membership.MasterHostID == s.State.HostID {
				ResumeStorageClusterOperations(work, s)
				return
			}
			_ = ReconcileStorageClustersFromMaster(work, s)
		}()
	}
	scheduleClusterReconcile()
	runWork.Add(1)
	go func() { defer runWork.Done(); sampler.run(probeCtx) }()
	go m.WatchLibvirt(probeCtx)
	runWork.Add(1)
	go func() {
		defer runWork.Done()
		ticker := time.NewTicker(cfg.ProbeInterval)
		defer ticker.Stop()
		tickStorage := func() {
			scheduleClusterReconcile()
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(probeCtx)
			storage.Tick(probeCtx, managed.Storage)
			// Only members reconcile from a master. The pass is not awaited by Run because a
			// hung remote mount can outlive its deadline; a master never starts it.
			if membership, err := Membership(s); err != nil || membership.MasterHostID == s.State.HostID {
				return
			}
			if !poolReconcileRunning.CompareAndSwap(false, true) {
				return
			}
			go func() {
				defer poolReconcileRunning.Store(false)
				// Each repository gets its own bounded deadline. A single shared
				// deadline would let one slow NFS/SMB/GFS2 access check skip every
				// later member-access report in this pass.
				_ = reconcilePoolStorageFromMaster(probeCtx, s, cfg.ProbeTimeout)
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
	workerCtx, stopWorker := context.WithCancel(ctx)
	workerDone := make(chan struct{})
	identity := s.State
	go func() { defer close(workerDone); managedWorker(workerCtx, s.dir, identity, commands, completed) }()
	defer func() { stopWorker(); <-workerDone }()
	active := ""
	ticker := time.NewTicker(cfg.Heartbeat)
	defer ticker.Stop()
	disconnected := false
	for {
		if active == "" {
			if id := nextManagedCommand(s.State.Journal); id != "" {
				// applyResponse already persisted the full command and admission order.
				active = id
				commands <- dispatchCommand(s.State.Journal[id])
			}
		}
		if disconnected {
			m.Tick(probeCtx)
		}
		if err = reportStorage(ctx, s, m, storage, cfg); err != nil && ctx.Err() == nil {
			var diskError persistenceError
			if errors.As(err, &diskError) {
				return err
			}
			if resetErr := resetStorageClusterProtocol(s); resetErr != nil {
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
			entry := s.State.Journal[active]
			if done.err != nil {
				entry.Status = "needs_attention"
				entry.Error = "local execution or persistence failed; inspect retained managed VM journal before recovery"
				entry.ObservedAt = time.Now().UTC()
			} else {
				entry.Status, entry.Error, entry.ObservedAt = done.result.Status, done.result.Error, done.result.ObservedAt
				if done.result.OperationID != active || (entry.Status != "succeeded" && entry.Status != "failed" && entry.Status != "needs_attention") {
					return errors.New("invalid local managed command completion")
				}
				forgetDeletedVM(cfg.history, entry)
			}
			s.State.Journal[active] = entry
			if err = s.Save(); err != nil {
				return persistenceError{err}
			}
			active = ""
			m.Tick(probeCtx)
		case <-ticker.C:
		}
	}
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
func nextManagedCommand(journal map[string]JournalEntry) string {
	oldest := ""
	for id, entry := range journal {
		if entry.Status != "pending" || entry.Command == nil || !managedCommand(entry.Command.Kind) {
			continue
		}
		old := journal[oldest]
		if oldest == "" || entry.Order < old.Order || entry.Order == old.Order &&
			(entry.RequestedAt.Before(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id < oldest) {
			oldest = id
		}
	}
	return oldest
}

// The Core queue is inventory too: a replacement Controller can display accepted work
// without reconstructing commands or dispatching them again. Engine outcomes win when
// execution has settled but its completion has not yet reached the agent journal.
func commandOutcomes(journal map[string]JournalEntry, outcomes []protocol.ManagedOutcome) []protocol.ManagedOutcome {
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
		if seen[id] || cmd == nil || !managedCommand(cmd.Kind) {
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
		case cmd.VM != nil:
			outcome.ID, outcome.Name = cmd.VM.ID, cmd.VM.Name
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
		if entry.Status != "pending" || entry.Command == nil {
			continue
		}
		cmd := entry.Command
		target := ""
		switch {
		case cmd.VM != nil:
			target = cmd.VM.ID
		case cmd.Power != nil:
			target = cmd.Power.VMID
		case cmd.VMMedia != nil:
			target = cmd.VMMedia.VMID
		}
		old := journal[latest]
		if target == vmID && (latest == "" || entry.Order > old.Order || entry.Order == old.Order &&
			(entry.RequestedAt.After(old.RequestedAt) || entry.RequestedAt.Equal(old.RequestedAt) && id > latest)) {
			latest = id
		}
	}
	return latest
}

func dispatchCommand(entry JournalEntry) protocol.Command {
	cmd := *entry.Command
	if cmd.Power != nil && cmd.Power.AfterOperationID == "" && entry.AfterOperationID != "" {
		power := *cmd.Power
		power.AfterOperationID = entry.AfterOperationID
		cmd.Power = &power
	}
	return cmd
}
