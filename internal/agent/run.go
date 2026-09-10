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
	"strings"
	"time"
	"unicode/utf8"
)

type persistenceError struct{ error }

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
		e := JournalEntry{Status: "pending", RequestedAt: time.Now(), Command: &cmd}
		if cmd.Kind != "refresh-inventory" && !managedCommand(cmd.Kind) {
			e.Status = "failed"
			e.Error = "unsupported agent command"
			e.ObservedAt = e.RequestedAt
		}
		s.State.Journal[cmd.OperationID] = e
	}
	return s.Save()
}
func report(ctx context.Context, s *Store, m *Monitor, cfg RunConfig) error {
	inv := m.Snapshot()
	managed, checks := vmhost.NewManaged(filepath.Join(s.dir, "managed-vms")).Applied(ctx)
	managed.Appliances = vmhost.Deployments(ApplianceJournalDir)
	inv.Managed = managed
	inv.Checks = append(inv.Checks, checks...)
	completeJournal(s, inv, cfg.ProbeInterval+2*cfg.ProbeTimeout)
	if s.State.Sequence == math.MaxUint64 {
		return errors.New("report sequence exhausted")
	}
	s.State.Sequence++
	req := protocol.ReportRequest{Capabilities: []string{protocol.ManagedVMCapability, protocol.ConsoleCapability, protocol.HostConsoleCapability, protocol.PowerCapability, protocol.MediaCapability, protocol.UpdateCapability}, Version: protocol.Version, Sequence: s.State.Sequence, Inventory: inv, Results: []protocol.Result{}}
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
		return err
	}
	if err = applyResponse(s, response); err != nil {
		return persistenceError{fmt.Errorf("persist commands: %w", err)}
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
		if e.Status == "pending" {
			e.RequestedAt = time.Now()
			s.State.Journal[id] = e
		}
	}
	if err := s.Save(); err != nil {
		return err
	}
	m := NewMonitor(cfg.ProbeTimeout, NativeProbes(version.Version))
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go m.WatchLibvirt(probeCtx)
	go func() {
		ticker := time.NewTicker(cfg.ProbeInterval)
		defer ticker.Stop()
		m.Tick(probeCtx)
		for {
			select {
			case <-probeCtx.Done():
				return
			case <-ticker.C:
				m.Tick(probeCtx)
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
			for id, entry := range s.State.Journal {
				if entry.Status == "pending" && entry.Command != nil && managedCommand(entry.Command.Kind) {
					// applyResponse already persisted the full command before dispatch.
					active = id
					commands <- *entry.Command
					break
				}
			}
		}
		if disconnected {
			m.Tick(probeCtx)
		}
		if err = report(ctx, s, m, cfg); err != nil && ctx.Err() == nil {
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
