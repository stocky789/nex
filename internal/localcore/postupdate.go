// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"nex.local/nex/internal/agent"
	"nex.local/nex/internal/nexupdate"
)

// postUpdateReportWait bounds the check. It must outlast DHCP rediscovery of the Controller
// (dhcpControllerReadyWait) plus the first report.
var postUpdateReportWait = dhcpControllerReadyWait + time.Minute

// WatchPostUpdate resolves an update that restarted this service: it is complete only once
// the embedded agent has a report accepted by the Controller. Without one in time the
// outcome is "attention", never a silent "done". It runs entirely in Core and survives a
// Controller outage; the agent keeps retrying whatever the outcome.
func (s *Service) WatchPostUpdate() {
	if !nexupdate.PostUpdateCheckPending(s.cfg.DataDir) {
		return
	}
	go func() {
		deadline := time.NewTimer(postUpdateReportWait)
		defer deadline.Stop()
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return // the marker stays; the next start repeats the check
			case <-deadline.C:
				_ = nexupdate.FinishPostUpdateCheck(s.cfg.DataDir, nexupdate.Progress{
					Phase: nexupdate.PhaseAttention, Percent: 100,
					Error: "The update is installed, but this host has not reported to its Controller since it restarted. Check that the Controller is running and reachable; the host keeps retrying.",
				})
				return
			case <-tick.C:
				if !s.hostEnrolled() {
					_ = nexupdate.FinishPostUpdateCheck(s.cfg.DataDir, nexupdate.Progress{Phase: "done", Percent: 100, Message: "Update complete"})
					return
				}
				if s.agentHasReported() {
					_ = nexupdate.FinishPostUpdateCheck(s.cfg.DataDir, nexupdate.Progress{Phase: "done", Percent: 100, Message: "Update complete; this host is reporting to its Controller"})
					return
				}
			}
		}
	}()
}

// hostEnrolled reads the persisted identity, not the running agent: an unenrolled host has
// no agent to wait for. An unreadable record counts as enrolled so the check never passes
// by default.
func (s *Service) hostEnrolled() bool {
	b, err := os.ReadFile(filepath.Join(s.cfg.AgentStateDir, "state.json"))
	if err != nil {
		return !os.IsNotExist(err)
	}
	var state struct {
		HostID string `json:"host_id"`
	}
	return json.Unmarshal(b, &state) != nil || state.HostID != ""
}

func (s *Service) agentHasReported() bool {
	key, err := filepath.Abs(s.cfg.AgentStateDir)
	if err != nil {
		return false
	}
	handoffAgents.Lock()
	defer handoffAgents.Unlock()
	a := handoffAgents.entries[key]
	return a != nil && a.started && !a.store.LastReport().IsZero()
}

// updateGuestShutdownWait is how long an update waits for guests to shut down before it
// gives up on the reboot. Guests are never forced off.
var updateGuestShutdownWait = 5 * time.Minute

// PrepareUpdateReboot is the service's nexupdate.PrepareReboot: the embedded agent shuts the
// guests down and records the managed ones for the boot pass. Without a running agent there
// is no record to keep, so the update restarts the service instead of rebooting.
func (s *Service) PrepareUpdateReboot(ctx context.Context) (func(context.Context), error) {
	key, err := filepath.Abs(s.cfg.AgentStateDir)
	if err != nil {
		return nil, err
	}
	handoffAgents.Lock()
	var store *agent.Store
	if a := handoffAgents.entries[key]; a != nil && a.started {
		store = a.store
	}
	handoffAgents.Unlock()
	if store == nil {
		return nil, errors.New("this host's Core agent is not running")
	}
	return agent.PrepareUpdateReboot(ctx, store, updateGuestShutdownWait)
}
