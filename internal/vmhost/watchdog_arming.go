// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

// Arming a watchdog is the one thing in Nex that makes a host reset itself, so it is done by
// the native engine the rest of the cluster already uses rather than by anything of Nex's own.
//
// The engine is sbd in its diskless watchdog-only mode: a Debian package, one daemon, one
// device. sbd feeds the hardware watchdog while this host is in the pool's quorate partition;
// when it is not, it stops feeding it and the machine resets within SBD_WATCHDOG_TIMEOUT. Nex
// Core writes sbd's configuration and enables the service, and never touches the device itself.
//
// Diskless sbd reads that quorum through Pacemaker, and only self-fences when Pacemaker's own
// policy says an inquorate node should die. Both were measured in a nested lab, and both are
// ways for a host to end up armed on paper and unfenceable in fact:
//
//   - sbd's only sources of truth are a shared block device and Pacemaker (its -P option). With
//     neither it starts, holds the watchdog open and never expires it.
//   - With no-quorum-policy=freeze an inquorate node stays frozen and sbd keeps feeding the
//     watchdog: measured, a partitioned host was still running after two minutes. With
//     no-quorum-policy=suicide the same host reset itself about seven seconds after it left the
//     quorate partition, and the survivors did not.
//
// So Nex Core arms a host only where Pacemaker is running *and* its policy is suicide, and
// otherwise refuses with a sentence rather than reporting a protection that does not exist.
//
// Three rules make this safe to run on every host on every reconcile pass:
//
//   - One hardware watchdog device, one owner. sbd opens it; nothing else in Nex does, and a
//     device something else already holds is reported and left alone rather than taken.
//   - Nothing here restarts a running Corosync. Debian's sbd.service refuses a manual start
//     and exists only as a dependency of corosync.service, so a host whose cluster is already
//     up can begin feeding its watchdog only when that cluster next starts. Restarting
//     Corosync under a live clustered filesystem is precisely what must not happen by itself,
//     so that host is left alone and reports arming as pending.
//   - Disarming is a clean stop of a daemon that closes the device with the kernel's magic
//     character. Where the kernel was built with nowayout that close does not stop the timer,
//     so Nex Core does not pretend it did: it reports that this host stays armed until it is
//     rebooted, rather than leaving a machine that will reset itself for a reason nobody can
//     see any more.

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"nex.local/nex/internal/protocol"
)

var sbdConfigPath = "/etc/default/sbd"

// sbdWatchdogTimeout is how long after sbd stops feeding the device the machine resets. Five
// seconds is sbd's own default and is short enough that the quorate side's wait — twice this,
// see PoolWatchdogBound — stays inside a promotion's patience, and long enough that a loaded
// host is not reset by a scheduling delay.
const SBDWatchdogTimeout = 5

// ErrWatchdogNoCluster is a host that has no cluster to lose. Nex Core arms nothing there.
var ErrWatchdogNoCluster = errors.New("this host is not running this pool's Pacemaker cluster, so there is no quorum for it to lose and Nex Core arms no watchdog")

// ErrWatchdogArmingPending is arming that cannot be completed now without disrupting a
// cluster that is already running. sbd exists only as a dependency of corosync.service —
// Debian's unit refuses a manual start — so a host whose cluster is already up can only begin
// feeding its watchdog when that cluster next starts. Restarting Corosync and Pacemaker under
// a running clustered filesystem is exactly what must not happen automatically, so Nex Core
// enables sbd, changes nothing else, and reports this.
var ErrWatchdogArmingPending = errors.New("this host arms its watchdog the next time its cluster starts: put it in maintenance mode and restart it, because Nex Core will not restart a running cluster to arm a watchdog")

// ArmLocalWatchdog makes sbd own this host's watchdog device. It is idempotent: a host whose
// configuration already says this and whose sbd is running is left exactly as it is, so a
// reconcile pass neither rewrites a file nor disturbs a daemon holding a live watchdog.
func ArmLocalWatchdog(ctx context.Context, w protocol.PoolHostWatchdog) error {
	if !protocol.PoolWatchdogArmable(w.Kind) || w.Device == "" {
		return errors.New("only a hardware or emulated watchdog can be armed")
	}
	if err := sbdInstalled(ctx); err != nil {
		return err
	}
	// Diskless sbd reads quorum through Pacemaker and has no other source, so arming a
	// host whose Pacemaker is not running would leave it holding the watchdog open and
	// never expiring it — armed on paper and unfenceable in fact.
	if active, err := unitActive(ctx, "pacemaker.service"); err != nil {
		return err
	} else if !active {
		return ErrWatchdogNoCluster
	}
	if err := clusterSelfFences(ctx); err != nil {
		return err
	}
	rendered := renderSBDConfig(w.Device)
	current, readErr := os.ReadFile(sbdConfigPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	if string(current) != rendered {
		if err := AtomicWrite(sbdConfigPath, []byte(rendered), 0644); err != nil {
			return err
		}
	}
	running, err := sbdRunning(ctx)
	if err != nil {
		return err
	}
	if _, err = clusterRun(ctx, "systemctl", "enable", "sbd.service"); err != nil {
		return err
	}
	if running {
		return nil
	}
	// The cluster is already running, so sbd can only join it at its next start, and Nex
	// Core will not restart a live cluster to arm a watchdog.
	return ErrWatchdogArmingPending
}

// DisarmLocalWatchdog stops this host arming at its next cluster start, and does not touch a
// running one. Disabling the unit is measurably inert under a live cluster; signalling the
// daemon is not the same thing. sbd is `RequiredBy=corosync.service`, and the one measurement
// of a SIGTERM under a live cluster left the unit `failed` on a Requires= edge — with no
// clustered filesystem mounted, which is exactly the case that matters and the one that was
// not measured. So Nex Core mirrors its own arming rule in the other direction: the device is
// released at the next cluster start, and a host that is still feeding one reports it.
//
// The pool's durable record says "not armed" either way, which is what governs whether a
// survivor may prove an exclusion; this is only about the device.
func DisarmLocalWatchdog(ctx context.Context, w protocol.PoolHostWatchdog) error {
	// Debian's sbd package leaves the unit ENABLED on install (measured), so a host the pool
	// has not asked to arm must actively disable it: otherwise the next cluster start pulls
	// sbd in with the distribution's own configuration instead of Nex Core's, on a
	// RequiredBy= edge that decides whether corosync starts at all.
	enabled, err := unitEnabled(ctx, "sbd.service")
	if err != nil {
		return err
	}
	if enabled {
		if _, err = clusterRun(ctx, "systemctl", "disable", "sbd.service"); err != nil {
			return err
		}
	}
	running, err := sbdRunning(ctx)
	if err != nil || !running {
		return err
	}
	if w.NoWayOut {
		return ErrWatchdogNoWayOut
	}
	return ErrWatchdogDisarmPending
}

// ErrWatchdogDisarmPending is a host that has been told to stop arming but is still feeding a
// watchdog it can only put down when its cluster next stops.
var ErrWatchdogDisarmPending = errors.New("this host stops feeding its watchdog the next time its cluster stops: put it in maintenance mode and restart it, because Nex Core will not disturb a running cluster's services")

// ErrWatchdogNoWayOut is the same, made permanent by the kernel: with nowayout, closing the
// device does not stop the timer, so only a reboot puts it down.
var ErrWatchdogNoWayOut = errors.New("this host's watchdog driver was built with nowayout, so the timer does not stop when the device is closed; this host keeps resetting on quorum loss until it is rebooted")

// clusterSelfFences is the other half of the same question: whether this cluster would let an
// inquorate node die. no-quorum-policy=freeze keeps it frozen and alive, and sbd goes on
// feeding the watchdog — so a host armed on such a cluster is a false promise, not a fence.
func clusterSelfFences(ctx context.Context) error {
	out, err := clusterRun(ctx, "crm_attribute", "-t", "crm_config", "-n", "no-quorum-policy", "-G", "--quiet")
	if err != nil {
		return errors.New("Nex Core could not read this pool's cluster policy, so it will not claim this host is fenced by its watchdog")
	}
	if strings.TrimSpace(string(out)) != "suicide" {
		return ErrWatchdogClusterWillNotSelfFence
	}
	return nil
}

// ErrWatchdogClusterWillNotSelfFence is a cluster whose policy keeps an inquorate node running.
var ErrWatchdogClusterWillNotSelfFence = errors.New("this pool's cluster keeps an inquorate host running instead of letting it reset itself, so arming its watchdog would protect nothing; its no-quorum-policy must be suicide")

// unitEnabled is whether systemd would start this unit as a dependency at the next boot or
// cluster start. "enabled" and "enabled-runtime" both would.
func unitEnabled(ctx context.Context, unit string) (bool, error) {
	out, err := clusterRun(ctx, "systemctl", "show", "--property=UnitFileState", "--value", unit)
	if err != nil {
		return false, err
	}
	return strings.HasPrefix(strings.TrimSpace(string(out)), "enabled"), nil
}

func unitActive(ctx context.Context, unit string) (bool, error) {
	out, err := clusterRun(ctx, "systemctl", "show", "--property=ActiveState", "--value", unit)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "active", nil
}

// LocalWatchdogArmed is whether sbd is feeding this host's watchdog right now. It asks systemd,
// not the device: reading the device would mean opening it.
func LocalWatchdogArmed(ctx context.Context) (bool, error) { return sbdRunning(ctx) }

func sbdRunning(ctx context.Context) (bool, error) { return unitActive(ctx, "sbd.service") }

func sbdInstalled(ctx context.Context) error {
	out, err := clusterRun(ctx, "systemctl", "show", "--property=LoadState", "--value", "sbd.service")
	if err != nil || strings.TrimSpace(string(out)) != "loaded" {
		return errors.New("the sbd package is not installed on this host, so Nex Core cannot arm its watchdog")
	}
	return nil
}

// renderSBDConfig is sbd's whole configuration for watchdog-only mode. SBD_DEVICE is absent,
// which is what makes it diskless: there is no shared block device to poison, and the pool's
// own quorum is the only input.
//
// SBD_PACEMAKER is on because it is the only quorum source a diskless sbd has; with it off
// and no block device, sbd watches nothing and the watchdog is never allowed to expire.
//
// SBD_DELAY_START makes sbd wait out the cluster's own message timeout before it begins
// feeding, so a cluster that is still forming is not reset while it forms. It is a second
// line of defence and not the first: the first is that Nex Core never enables this pool's
// cluster at boot, and starts it unarmed, so no host ever comes up alone and armed.
func renderSBDConfig(device string) string {
	return sbdConfigMarker +
		"SBD_WATCHDOG_DEV=/dev/" + device + "\n" +
		"SBD_WATCHDOG_TIMEOUT=" + strconv.Itoa(SBDWatchdogTimeout) + "\n" +
		"SBD_PACEMAKER=yes\n" +
		"SBD_STARTMODE=always\n" +
		"SBD_DELAY_START=yes\n" +
		"SBD_OPTS=\n"
}

// sbdConfigMarker is what tells a package upgrade that this host's sbd belongs to a pool.
// packaging/core-local/postinst disables sbd.service on a host that has no Nex-managed
// configuration — Debian leaves the unit enabled on install and it is RequiredBy=corosync —
// and leaves an armed host's alone, so an ordinary upgrade does not silently disarm a pool.
const sbdConfigMarker = "# Managed by Nex Core; do not edit.\n"
