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

const transientStorageDiscoveryLimit = 128

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

type RunConfig struct{ Heartbeat, ProbeInterval, ProbeTimeout time.Duration }

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
	managed.Outcomes = commandOutcomes(s.State.Journal, managed.Outcomes)
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
	capabilities := []string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.PowerQueueCapability, protocol.MediaCapability, protocol.ISODownloadCapability, protocol.UpdateCapability}
	if s.State.StorageProtocolLegacy {
		version = protocol.LegacyVersion
		capabilities = []string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.MediaCapability, protocol.UpdateCapability}
		// A strict v1 decoder must not see v2 managed storage/receipt fields.
		legacy := *inv
		legacy.Managed = nil
		inv = &legacy
	}
	if s.State.StorageProtocolReady {
		capabilities = append(capabilities, "storage-repositories-v1", protocol.StorageDiscoveryCapability)
	}
	discoveries := drainStorageDiscoveries(s)
	req := protocol.ReportRequest{Capabilities: capabilities, Version: version, Sequence: s.State.Sequence, Inventory: inv, Results: []protocol.Result{}, StorageDiscoveries: discoveries}
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
	if err = redeemStorageCredentials(ctx, s, response.CredentialOffers); err != nil {
		return persistenceError{fmt.Errorf("persist storage credential handoff: %w", err)}
	}
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
	if len(response.Commands) > 0 {
		m.Tick(ctx)
	}
	return nil
}

func storageDiscoveryScopeMatches(state State, request protocol.StorageDiscoveryRequest) bool {
	return request.HostID == state.HostID && request.PoolID == state.PoolID
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
	c, err := client(s.State, true)
	if err != nil {
		return err
	}
	c.CloseIdleConnections()
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
	m := NewMonitor(cfg.ProbeTimeout, NativeProbes(version.Version))
	storage := vmhost.NewStorageMonitor(cfg.ProbeTimeout)
	var poolReconcileRunning atomic.Bool
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.WatchLibvirt(probeCtx)
	go func() {
		ticker := time.NewTicker(cfg.ProbeInterval)
		defer ticker.Stop()
		tickStorage := func() {
			managed, _ := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(probeCtx)
			storage.Tick(probeCtx, managed.Storage)
			if !poolReconcileRunning.CompareAndSwap(false, true) {
				return
			}
			go func() {
				defer poolReconcileRunning.Store(false)
				work, done := context.WithTimeout(probeCtx, cfg.ProbeTimeout)
				defer done()
				_ = ReconcilePoolStorageFromMaster(work, s)
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
