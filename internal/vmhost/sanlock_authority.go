// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

type SanlockAuthorityState string

const (
	SanlockAuthorityHeld     SanlockAuthorityState = "held"
	SanlockAuthorityReleased SanlockAuthorityState = "released"
	SanlockAuthorityUnknown  SanlockAuthorityState = "unknown"
)

type SanlockAuthorityStatus struct {
	State    SanlockAuthorityState
	OwnerPID int
}

type SanlockWatchdogStatus struct {
	Device        string
	Kind          string
	OwnerPID      int
	DeviceOpen    bool
	ResetVerified bool
}

// CheckSanlockWatchdogReady confirms the expected physical watchdog is held by wdmd,
// sanlock is connected to wdmd, and SBD is not a competing owner. ResetVerified stays
// false: ownership and an open fd do not prove that hardware will reset this host.
func CheckSanlockWatchdogReady(ctx context.Context, expected protocol.PoolHostWatchdog) (SanlockWatchdogStatus, error) {
	status := SanlockWatchdogStatus{Device: expected.Device, Kind: expected.Kind}
	if expected.Kind != protocol.PoolWatchdogHardware || expected.Device == "" || expected.TimeoutSeconds <= 0 {
		return status, errors.New("storage-lease fencing requires a reported hardware watchdog")
	}
	actual := LocalWatchdog()
	if actual.Kind != protocol.PoolWatchdogHardware || actual.Device != expected.Device || actual.TimeoutSeconds != expected.TimeoutSeconds {
		return status, errors.New("local hardware watchdog does not match the recorded device and timeout")
	}
	sbd, err := systemdActive(ctx, "sbd.service")
	if err != nil || sbd {
		return status, errors.New("SBD is active or its state is unavailable; wdmd cannot own this watchdog")
	}
	wdmd, wdmdPID, err := systemdUnit(ctx, "wdmd.service")
	if err != nil || wdmd != "active" || wdmdPID <= 0 {
		return status, errors.New("wdmd is not active with a verifiable process")
	}
	sanlockState, _, err := systemdUnit(ctx, "sanlock.service")
	if err != nil || sanlockState != "active" {
		return status, errors.New("sanlock is not active")
	}
	wanted := filepath.Join("/dev", expected.Device)
	fds, err := os.ReadDir(fmt.Sprintf("/proc/%d/fd", wdmdPID))
	if err != nil {
		return status, errors.New("cannot inspect wdmd watchdog ownership")
	}
	for _, fd := range fds {
		target, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%s", wdmdPID, fd.Name()))
		if err == nil && target == wanted {
			status.OwnerPID = wdmdPID
			status.DeviceOpen = true
			return status, nil
		}
	}
	return status, errors.New("wdmd does not hold the recorded watchdog device")
}

// SetSanlockAuthorityLease supervises one non-restarting systemd service whose process
// lifetime owns the authority resource through sanlock. Callers must create the resource
// file in the master-authorized preparation operation before requesting hold.
func SetSanlockAuthorityLease(ctx context.Context, resource SanlockResource, hold bool) (SanlockAuthorityStatus, error) {
	status := SanlockAuthorityStatus{State: SanlockAuthorityUnknown}
	if !validSanlockResource(resource) {
		return status, errors.New("invalid sanlock authority resource")
	}
	unit := sanlockAuthorityUnit(resource)
	if !hold {
		state, _, err := systemdUnit(ctx, unit)
		if err != nil {
			return status, errors.New("authority lease service state is unavailable")
		}
		if state == "not-found" {
			status.State = SanlockAuthorityReleased
			return status, nil
		}
		if state == "failed" {
			return status, errors.New("authority lease service failed; lease state is unknown")
		}
		if state == "active" {
			if _, err := run(ctx, "systemctl", "stop", unit); err != nil {
				return status, errors.New("authority lease release did not complete")
			}
		}
		state, _, err = systemdUnit(ctx, unit)
		if err != nil || state != "inactive" && state != "not-found" {
			return status, errors.New("authority lease release is not confirmed")
		}
		status.State = SanlockAuthorityReleased
		return status, nil
	}
	if err := EnsureSanlockResource(ctx, SanlockResourceConfig{SanlockResource: resource}); err != nil {
		return status, err
	}
	watchdog, err := expectedLocalHardwareWatchdog()
	if err != nil {
		return status, err
	}
	watchdogStatus, readyErr := CheckSanlockWatchdogReady(ctx, watchdog)
	if readyErr != nil {
		return status, readyErr
	}
	if !watchdogStatus.ResetVerified {
		return status, errors.New("storage-lease authority is disabled until this hardware watchdog's reset path is independently verified")
	}
	state, pid, err := systemdUnit(ctx, unit)
	if err != nil {
		return status, errors.New("authority lease service state is unavailable")
	}
	if state == "active" {
		if !sanlockOwnerProcess(pid) {
			return status, errors.New("authority lease service is active without its sanlock owner process")
		}
		return SanlockAuthorityStatus{State: SanlockAuthorityHeld, OwnerPID: pid}, nil
	}
	if state == "failed" || state != "inactive" && state != "not-found" {
		return status, errors.New("authority lease service state is ambiguous")
	}
	resourceArg := fmt.Sprintf("%s:%s:%s:%d", resource.Lockspace, resource.Key, resource.Path, resource.Offset)
	if _, err := run(ctx, "systemd-run", "--unit="+unit, "--collect", "--no-block", "--property=Restart=no", "--property=KillMode=control-group", "/usr/sbin/sanlock", "client", "command", "-r", resourceArg, "-c", "/usr/bin/sleep", "infinity"); err != nil {
		return status, errors.New("authority lease owner could not be started")
	}
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		state, pid, err = systemdUnit(ctx, unit)
		if err == nil && state == "active" && sanlockOwnerProcess(pid) {
			return SanlockAuthorityStatus{State: SanlockAuthorityHeld, OwnerPID: pid}, nil
		}
		if err == nil && (state == "failed" || state == "inactive" || state == "not-found") {
			return status, errors.New("authority resource acquisition failed")
		}
		select {
		case <-ctx.Done():
			return status, errors.New("authority resource acquisition state is unknown")
		case <-deadline.C:
			return status, errors.New("authority resource acquisition was not confirmed")
		case <-ticker.C:
		}
	}
}

func expectedLocalHardwareWatchdog() (protocol.PoolHostWatchdog, error) {
	local := LocalWatchdog()
	if local.Kind != protocol.PoolWatchdogHardware {
		return local, errors.New("storage-lease fencing requires a physical hardware watchdog")
	}
	return local, nil
}

func sanlockAuthorityUnit(resource SanlockResource) string {
	digest := sha256.Sum256([]byte(resource.Lockspace + "\x00" + resource.Key + "\x00" + resource.Path + "\x00" + strconv.FormatUint(resource.Offset, 10)))
	return "nex-storage-authority-" + hex.EncodeToString(digest[:12]) + ".service"
}

func systemdUnit(ctx context.Context, unit string) (string, int, error) {
	output, err := run(ctx, "systemctl", "show", "--property=LoadState", "--property=ActiveState", "--property=MainPID", "--value", unit)
	if err != nil {
		return "", 0, err
	}
	fields := strings.Fields(string(output))
	if len(fields) != 3 {
		return "", 0, errors.New("systemd unit status is invalid")
	}
	if fields[0] == "not-found" {
		return "not-found", 0, nil
	}
	pid, err := strconv.Atoi(fields[2])
	if err != nil || pid < 0 {
		return "", 0, errors.New("systemd unit process ID is invalid")
	}
	return fields[1], pid, nil
}

func systemdActive(ctx context.Context, unit string) (bool, error) {
	state, _, err := systemdUnit(ctx, unit)
	return state == "active", err
}

func sanlockOwnerProcess(pid int) bool {
	if pid <= 0 {
		return false
	}
	comm, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	return err == nil && strings.TrimSpace(string(comm)) == "sleep"
}
