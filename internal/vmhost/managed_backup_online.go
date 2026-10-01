// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

var (
	backupJobTypeRe          = regexp.MustCompile(`(?m)^Job type:\s*(\S+)\s*$`)
	backupStatLineRe         = regexp.MustCompile(`(?m)^(\w+):\s*(-?\d+)\s*$`)
	backupCapacityRe         = regexp.MustCompile(`(?m)^Capacity:\s*(\d+)`)
	errBackupNoStats         = errors.New("native backup has no completed-job statistics")
	errBackupSuccess0        = errors.New("native backup completed with success=0")
	ErrOnlineBackupUncertain = errors.New("online backup native job outcome is uncertain")
)

var onlineBackupLocalRoot = filepath.Join(DefaultStoragePath, ".nex-backup-work")

func OnlineBackupWorkDir(operationID string) string {
	return filepath.Join(onlineBackupLocalRoot, operationID)
}

// PrepareOnlineBackupWorkDir lets QEMU create and own its work directory.
// Core must not precreate it as root with an inaccessible mode.
func PrepareOnlineBackupWorkDir(ctx context.Context, path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("online backup work directory is invalid")
	}
	for parent := path; parent != "/"; parent = filepath.Dir(parent) {
		info, err := os.Lstat(parent)
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return errors.New("online backup work directory has an unsafe parent")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if strings.HasPrefix(path, onlineBackupLocalRoot+string(os.PathSeparator)) {
		if err := prepareLocalOnlineBackupRoot(ctx); err != nil {
			return err
		}
	}
	script := `umask 077; mkdir -p -- "$1"; test -w "$1" && test -x "$1"`
	if _, err := run(ctx, "runuser", "-u", "libvirt-qemu", "--", "sh", "-c", script, "sh", path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("online backup work directory is unsafe")
	}
	return nil
}

func prepareLocalOnlineBackupRoot(ctx context.Context) error {
	if err := os.Mkdir(onlineBackupLocalRoot, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	info, err := os.Lstat(onlineBackupLocalRoot)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("local online backup work directory is unsafe")
	}
	uidText, err := qemuID(ctx, "-u")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil {
		return err
	}
	gidText, err := qemuID(ctx, "-g")
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(gidText)
	if err != nil {
		return err
	}
	return os.Chown(onlineBackupLocalRoot, uid, gid)
}

// PrepareOnlineBackupTarget creates a reusable qcow2 target as libvirt-qemu mode 0600
// sized to the live disk capacity. Default backup-begin output creation is never used.
func PrepareOnlineBackupTarget(ctx context.Context, vmID, target, path string) error {
	if !uuidPattern.MatchString(vmID) || strings.TrimSpace(target) == "" ||
		!filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("online backup target preparation is invalid")
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return errors.New("online backup target already exists")
		}
		return err
	}
	info, err := managedVirsh(ctx, "domblkinfo", vmID, target)
	if err != nil {
		return err
	}
	match := backupCapacityRe.FindSubmatch(info)
	if match == nil {
		return errors.New("live disk capacity is unavailable for online backup")
	}
	capacity := string(match[1])
	if _, err := strconv.ParseInt(capacity, 10, 64); err != nil || capacity == "0" {
		return errors.New("live disk capacity is invalid for online backup")
	}
	if err := PrepareOnlineBackupWorkDir(ctx, filepath.Dir(path)); err != nil {
		return err
	}
	// umask 077 → mode 0600; run as libvirt-qemu so QEMU can write push-mode outputs.
	script := `umask 077; exec qemu-img create -f qcow2 "$1" "$2"`
	if _, err := run(ctx, "runuser", "-u", "libvirt-qemu", "--", "sh", "-c", script, "sh", path, capacity); err != nil {
		_ = os.Remove(path)
		return err
	}
	st, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if st.Mode()&0o777 != 0o600 {
		_ = os.Remove(path)
		return errors.New("online backup target is not owner-only mode 0600")
	}
	return nil
}

// OnlineBackupDiskXML builds domainbackup XML with explicit per-disk file targets.
func OnlineBackupDiskXML(disks []BackupDiskSource, targets map[string]string) (string, error) {
	if len(disks) == 0 || len(targets) != len(disks) {
		return "", errors.New("online backup disk set is incomplete")
	}
	var b strings.Builder
	b.WriteString("<domainbackup><disks>")
	for _, disk := range disks {
		path := targets[disk.Target]
		if path == "" || !filepath.IsAbs(path) {
			return "", errors.New("online backup target path is missing")
		}
		b.WriteString("<disk name='")
		b.WriteString(xmlEscapeAttr(disk.Target))
		b.WriteString("' type='file'><target file='")
		b.WriteString(xmlEscapeAttr(path))
		b.WriteString("'/><driver type='qcow2'/></disk>")
	}
	b.WriteString("</disks></domainbackup>")
	return b.String(), nil
}

func xmlEscapeAttr(s string) string {
	r := strings.NewReplacer(`&`, "&amp;", `'`, "&apos;", `"`, "&quot;", `<`, "&lt;", `>`, "&gt;")
	return r.Replace(s)
}

// BeginOnlineFullBackup starts virsh backup-begin --reuse-external with prepared targets.
func BeginOnlineFullBackup(ctx context.Context, vmID, backupXMLPath string) error {
	if !uuidPattern.MatchString(vmID) || !filepath.IsAbs(backupXMLPath) {
		return errors.New("online backup begin arguments are invalid")
	}
	_, err := managedVirsh(ctx, "backup-begin", vmID, backupXMLPath, "--reuse-external")
	return err
}

// OnlineBackupJobInactive proves QEMU is no longer writing backup outputs.
// A failed query is an uncertain outcome, never permission to unlink targets.
func OnlineBackupJobInactive(ctx context.Context, vmID string) error {
	if !uuidPattern.MatchString(vmID) {
		return errors.New("online backup VM ID is invalid")
	}
	raw, err := managedVirsh(ctx, "domjobinfo", vmID, "--rawstats")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOnlineBackupUncertain, err)
	}
	kind, err := backupJobType(string(raw))
	if err != nil || kind != "none" {
		return ErrOnlineBackupUncertain
	}
	return nil
}

// WaitOnlineBackupJob waits until the current domain job is idle, then returns completed raw stats.
// Job disappearance alone is never treated as success; callers must RequireCompletedBackupSuccess.
func WaitOnlineBackupJob(ctx context.Context, vmID string) (current, completed string, err error) {
	if !uuidPattern.MatchString(vmID) {
		return "", "", errors.New("online backup wait arguments are invalid")
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return "", "", err
		}
		raw, jobErr := managedVirsh(ctx, "domjobinfo", vmID, "--rawstats")
		current = string(raw)
		if jobErr != nil {
			if strings.Contains(strings.ToLower(jobErr.Error()), "domain is not running") {
				return current, "", errors.New("VM stopped during online backup")
			}
			return current, "", jobErr
		}
		kind, parseErr := backupJobType(current)
		if parseErr != nil {
			return current, "", parseErr
		}
		if kind == "none" {
			break
		}
		select {
		case <-ctx.Done():
			return current, "", ctx.Err()
		case <-ticker.C:
		}
	}
	raw, err := managedVirsh(ctx, "domjobinfo", vmID, "--completed", "--keep-completed", "--anystats", "--rawstats")
	if err != nil {
		return current, "", errBackupNoStats
	}
	return current, string(raw), nil
}

// RequireCompletedBackupSuccess refuses success=0 and missing completed statistics.
// Job type none / disappearance alone must never publish.
func RequireCompletedBackupSuccess(current, completed string) error {
	kind, err := backupJobType(current)
	if err != nil {
		return err
	}
	if kind != "none" {
		return errors.New("native backup still appears active")
	}
	completedKind, err := backupJobType(completed)
	if err != nil {
		return errBackupNoStats
	}
	stats := backupNumericStats(completed)
	success, ok := stats["success"]
	if !ok {
		return errBackupNoStats
	}
	if success != 1 {
		return errBackupSuccess0
	}
	if completedKind != "completed" {
		return errBackupNoStats
	}
	if stats["disk_processed"] <= 0 || stats["disk_remaining"] != 0 {
		return errors.New("native backup completed without successful full-disk statistics")
	}
	return nil
}

func backupJobType(output string) (string, error) {
	match := backupJobTypeRe.FindStringSubmatch(output)
	if match == nil {
		return "", errors.New("unrecognized domjobinfo result")
	}
	switch strings.ToLower(match[1]) {
	case "0":
		return "none", nil
	case "1":
		return "bounded", nil
	case "2":
		return "unbounded", nil
	case "3":
		return "completed", nil
	case "4":
		return "failed", nil
	case "5":
		return "cancelled", nil
	default:
		return strings.ToLower(match[1]), nil
	}
}

func backupNumericStats(output string) map[string]int64 {
	out := map[string]int64{}
	for _, match := range backupStatLineRe.FindAllStringSubmatch(output, -1) {
		v, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			continue
		}
		out[match[1]] = v
	}
	return out
}

// CaptureOnlineDisks runs push-mode full backup into prepared destination paths and validates
// completed-job success=1 before returning. Partial outputs must not be published.
func CaptureOnlineDisks(ctx context.Context, vmID string, disks []BackupDiskSource, destinations map[string]string) error {
	if len(disks) == 0 {
		return errors.New("online backup disk set is empty")
	}
	xmlBody, err := OnlineBackupDiskXML(disks, destinations)
	if err != nil {
		return err
	}
	workDir := filepath.Dir(destinations[disks[0].Target])
	xmlPath := filepath.Join(workDir, "backup.xml")
	if err := AtomicWrite(xmlPath, []byte(xmlBody), 0600); err != nil {
		return err
	}
	if err := BeginOnlineFullBackup(ctx, vmID, xmlPath); err != nil {
		return fmt.Errorf("%w: %v", ErrOnlineBackupUncertain, err)
	}
	current, completed, err := WaitOnlineBackupJob(ctx, vmID)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrOnlineBackupUncertain, err)
	}
	if err := RequireCompletedBackupSuccess(current, completed); err != nil {
		return err
	}
	for _, disk := range disks {
		path := destinations[disk.Target]
		chain, chainErr := observeManagedDiskChain(ctx, path)
		if chainErr != nil {
			return chainErr
		}
		if len(chain) != 1 || chain[0] != path {
			return fmt.Errorf("online backup image for %s retained a backing link", disk.Target)
		}
		if _, checkErr := run(ctx, "qemu-img", "check", path); checkErr != nil {
			return fmt.Errorf("online backup image for %s failed qemu-img check", disk.Target)
		}
	}
	return nil
}

// RefuseOnlineBackupAdmission returns a durable refusal reason, or empty when admitted.
func RefuseOnlineBackupAdmission(consistency string, vm protocol.VMCreateSpec, domainState string) string {
	switch consistency {
	case "", protocol.BackupCaptureStopped:
		if domainState != "shut off" {
			return "stopped backup requires a shut-off VM"
		}
		return ""
	case protocol.BackupCaptureQuiesced:
		return "quiesced online backup is not supported in this development slice"
	case protocol.BackupCaptureOnline:
		if domainState != "running" {
			return "online backup requires a running VM"
		}
		if vm.Boot.Firmware == "uefi" {
			return "online backup is not supported for UEFI guests in this development slice"
		}
		if vm.Boot.TPMEnabled() {
			return "online backup is not supported for guests with a TPM; shut the VM off and back it up stopped"
		}
		return ""
	default:
		return "backup consistency is invalid"
	}
}
