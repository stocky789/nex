// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"nex.local/nex/internal/protocol"
	"time"
)

const AgentVersion = "0.1.0-dev"

type persistenceError struct{ error }

type RunConfig struct{ Heartbeat, ProbeInterval, ProbeTimeout time.Duration }

func completeJournal(s *Store, inv *protocol.Inventory, timeout time.Duration) {
	for id, e := range s.State.Journal {
		if e.Status != "pending" {
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
		if _, ok := s.State.Journal[cmd.OperationID]; ok {
			continue
		}
		// ponytail: retain command IDs for this identity; compact only with a negotiated replay horizon.
		if len(s.State.Journal) >= 10000 {
			return errors.New("local command journal full; manual recovery required")
		}
		e := JournalEntry{Status: "pending", RequestedAt: time.Now()}
		if cmd.Kind != "refresh-inventory" {
			e.Status = "failed"
			e.Error = "unsupported command; this agent only observes inventory"
			e.ObservedAt = e.RequestedAt
		}
		s.State.Journal[cmd.OperationID] = e
	}
	return s.Save()
}
func report(ctx context.Context, s *Store, m *Monitor, cfg RunConfig) error {
	inv := m.Snapshot()
	completeJournal(s, inv, cfg.ProbeInterval+2*cfg.ProbeTimeout)
	if s.State.Sequence == math.MaxUint64 {
		return errors.New("report sequence exhausted")
	}
	s.State.Sequence++
	req := protocol.ReportRequest{Version: protocol.Version, Sequence: s.State.Sequence, Inventory: inv, Results: []protocol.Result{}}
	for id, e := range s.State.Journal {
		if e.Status != "pending" && !e.Acknowledged {
			req.Results = append(req.Results, protocol.Result{OperationID: id, Status: e.Status, Error: e.Error, ObservedAt: e.ObservedAt})
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
	m := NewMonitor(cfg.ProbeTimeout, NativeProbes(AgentVersion))
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
	ticker := time.NewTicker(cfg.Heartbeat)
	defer ticker.Stop()
	disconnected := false
	for {
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
		case <-ticker.C:
		}
	}
}
