// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"nex.local/nex/internal/nexupdate"
)

// scheduled records the systemctl actions ApplyLocal asks systemd-run to schedule. A failing
// action is reported as a failed command, as a broken systemd would.
type scheduled struct {
	mu      sync.Mutex
	actions []string
}

func recordSchedule(t *testing.T, failing string) *scheduled {
	t.Helper()
	stubApt(t, "0.1.0", "9.9.9", nil)
	rec := &scheduled{}
	previous := nexupdate.Command
	nexupdate.Command = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if name != "systemd-run" {
			return previous(ctx, name, args...)
		}
		// systemd-run --unit=… --no-block /bin/systemctl <action…>
		action := strings.Join(args[3:], " ")
		rec.mu.Lock()
		rec.actions = append(rec.actions, action)
		rec.mu.Unlock()
		if action == failing {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "true")
	}
	t.Cleanup(func() { nexupdate.Command = previous })
	return rec
}

func (r *scheduled) last() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.actions) == 0 {
		return ""
	}
	return r.actions[len(r.actions)-1]
}

func applyCore(t *testing.T) (nexupdate.ApplyResult, string, error) {
	t.Helper()
	dir := t.TempDir()
	result, err := nexupdate.ApplyLocal(context.Background(), "https://mirror.test/apt", "nex-core-local", dir)
	return result, dir, err
}

func setPrepareReboot(t *testing.T, f func(context.Context) (func(context.Context), error)) {
	t.Helper()
	nexupdate.PrepareReboot = f
	t.Cleanup(func() { nexupdate.PrepareReboot = nil })
}

// Once guests are down, the update ends in a host reboot, and the restarted service still
// owes the report check.
func TestCoreUpdateRebootsAfterGuestsAreShutDown(t *testing.T) {
	rec := recordSchedule(t, "")
	setPrepareReboot(t, func(context.Context) (func(context.Context), error) { return func(context.Context) {}, nil })
	result, dir, err := applyCore(t)
	if err != nil || result.Status != "scheduled" {
		t.Fatalf("apply = %+v, %v", result, err)
	}
	if rec.last() != "reboot" {
		t.Fatalf("scheduled %q, want a reboot", rec.last())
	}
	if !nexupdate.PostUpdateCheckPending(dir) {
		t.Fatal("a rebooting update lost its report check")
	}
}

// A guest that will not shut down must leave the host up: the update is installed, the
// service restarts, and the operator is told why nothing rebooted.
func TestCoreUpdateDoesNotRebootWhenGuestsStayRunning(t *testing.T) {
	rec := recordSchedule(t, "")
	setPrepareReboot(t, func(context.Context) (func(context.Context), error) {
		return nil, errors.New("1 guest(s) did not shut down in time")
	})
	result, dir, err := applyCore(t)
	if err != nil || result.Status != "scheduled" {
		t.Fatalf("apply = %+v, %v", result, err)
	}
	if rec.last() != "restart nex-core-local.service" {
		t.Fatalf("scheduled %q, want a service restart", rec.last())
	}
	// The restarted service resolves the check; its outcome must carry the reason.
	if b, err := os.ReadFile(filepath.Join(dir, "post-update-check.json")); err != nil || !strings.Contains(string(b), "did not shut down") {
		t.Fatalf("the reason was not kept for the operator: %s %v", b, err)
	}
}

// If the reboot cannot be scheduled after guests were stopped, they come back and the
// service restarts instead; guests are never left stopped by an update that did not reboot.
func TestCoreUpdateRestoresGuestsWhenRebootCannotBeScheduled(t *testing.T) {
	rec := recordSchedule(t, "reboot")
	restored := false
	setPrepareReboot(t, func(context.Context) (func(context.Context), error) {
		return func(context.Context) { restored = true }, nil
	})
	result, _, err := applyCore(t)
	if err != nil || result.Status != "scheduled" {
		t.Fatalf("apply = %+v, %v", result, err)
	}
	if !restored {
		t.Fatal("guests stopped for a reboot that was not scheduled were left stopped")
	}
	if rec.last() != "restart nex-core-local.service" {
		t.Fatalf("scheduled %q, want the fallback service restart", rec.last())
	}
}

// The bundled Controller appliance reboots after its update; a Controller installed on a
// host of its own only restarts its service, since nothing here says that machine may reboot.
func TestControllerUpdateRebootsOnlyTheAppliance(t *testing.T) {
	for name, want := range map[string]string{"appliance": "reboot", "standalone": "restart nex-controller.service"} {
		t.Run(name, func(t *testing.T) {
			rec := recordSchedule(t, "")
			dir := t.TempDir()
			if name == "appliance" {
				if err := os.WriteFile(filepath.Join(dir, "appliance-initialized"), []byte("1\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := nexupdate.ApplyLocal(context.Background(), "https://mirror.test/apt", "nex-controller", dir); err != nil {
				t.Fatal(err)
			}
			if rec.last() != want {
				t.Fatalf("scheduled %q, want %q", rec.last(), want)
			}
		})
	}
}
