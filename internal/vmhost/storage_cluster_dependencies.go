// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"debug/elf"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// dlm_stonith loads libstonithd at runtime instead of declaring it as an ELF
// dependency. Validate that private loader contract before DLM is configured:
// `ldd` and an ELF DT_NEEDED inspection cannot discover this dependency.
const storageClusterFenceHelperMaximumBytes = 16 << 20
const storageClusterFenceLibraryCacheMaximumBytes = 16 << 20

var storageClusterFenceHelperPath = func() (string, error) {
	return exec.LookPath("dlm_stonith")
}

var storageClusterFenceLibraryCache = func(ctx context.Context) ([]byte, error) {
	return clusterRun(ctx, "ldconfig", "-p")
}

const (
	storageClusterSessionMonitorWrapper = "/usr/lib/ocf/resource.d/nex/cluster-iscsi-session"
	storageClusterSessionMonitorCore    = "/usr/lib/nex/nex-core-local"
)

var storageClusterSessionMonitorTrustedFile = trustedStorageClusterSessionMonitorFile
var storageClusterSessionMonitorExecute = func(ctx context.Context, path string, args, env []string) error {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = env
	return cmd.Run()
}

// ValidateStorageClusterSessionMonitor proves that this Core has the exact
// packaged iSCSI-session monitor and that it currently accepts one strict
// read-only session observation. It never invokes iscsiadm itself, changes a
// CIB, or reports native command output.
func ValidateStorageClusterSessionMonitor(ctx context.Context, portal, target, sessionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	canonicalPortal, wantID, err := StorageISCSISessionParameters(portal, target)
	if err != nil || portal != canonicalPortal || sessionID != wantID {
		return errors.New("cluster iSCSI session monitor parameters are invalid")
	}
	if err := storageClusterSessionMonitorTrustedFile(storageClusterSessionMonitorWrapper); err != nil {
		return errors.New("cluster iSCSI session monitor is unavailable")
	}
	if err := storageClusterSessionMonitorTrustedFile(storageClusterSessionMonitorCore); err != nil {
		return errors.New("cluster iSCSI session monitor is unavailable")
	}
	probe, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	env := []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "OCF_RESKEY_portal=" + portal, "OCF_RESKEY_target=" + target, "OCF_RESKEY_session_id=" + sessionID}
	if err := storageClusterSessionMonitorExecute(probe, storageClusterSessionMonitorWrapper, []string{"monitor"}, env); err != nil {
		return errors.New("cluster iSCSI session monitor did not confirm readiness")
	}
	if err := probe.Err(); err != nil {
		return err
	}
	return nil
}

func trustedStorageClusterSessionMonitorFile(path string) error {
	if err := trustedManagedPath(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0755 {
		return errors.New("not a trusted monitor file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 {
		return errors.New("not a Core-owned monitor file")
	}
	return nil
}

type storageClusterELFInfo struct {
	class   elf.Class
	machine elf.Machine
}

var storageClusterELFIdentity = func(path string) (storageClusterELFInfo, error) {
	f, err := elf.Open(path)
	if err != nil {
		return storageClusterELFInfo{}, err
	}
	defer f.Close()
	return storageClusterELFInfo{class: f.Class, machine: f.Machine}, nil
}

var storageClusterELFExports = func(path string) (map[string]bool, error) {
	f, err := elf.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	syms, err := f.DynamicSymbols()
	if err != nil {
		return nil, err
	}
	exports := make(map[string]bool, len(syms))
	for _, sym := range syms {
		if sym.Section != elf.SHN_UNDEF && (elf.ST_BIND(sym.Info) == elf.STB_GLOBAL || elf.ST_BIND(sym.Info) == elf.STB_WEAK) {
			exports[sym.Name] = true
		}
	}
	return exports, nil
}

// ValidateStorageClusterDLMFenceHelper confirms the helper's exact requested
// libstonithd SONAME has one architecture-matching loader-cache candidate with
// the APIs DLM needs. It does not execute dlm_stonith or any fence agent. In
// particular, no helper check may fence a host merely to establish whether
// clustered storage is admissible; native recovery remains the final proof.
func ValidateStorageClusterDLMFenceHelper(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	helper, err := storageClusterFenceHelperPath()
	if err != nil {
		return errors.New("DLM fencing helper is unavailable")
	}
	helperIdentity, err := storageClusterELFIdentity(helper)
	if err != nil {
		return errors.New("DLM fencing helper is not a readable ELF executable")
	}
	soname, err := storageClusterFenceHelperSONAME(helper)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	cache, err := storageClusterFenceLibraryCache(ctx)
	if err != nil || len(cache) > storageClusterFenceLibraryCacheMaximumBytes {
		return errors.New("native library cache is unavailable for DLM fencing")
	}
	paths := storageClusterLibraryCachePaths(cache, soname)
	if len(paths) == 0 {
		return errors.New("DLM fencing helper's exact stonith library is unavailable")
	}
	compatiblePath := ""
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return err
		}
		identity, err := storageClusterELFIdentity(path)
		if err != nil || identity != helperIdentity {
			continue
		}
		if compatiblePath != "" {
			return errors.New("DLM fencing helper's exact stonith library is ambiguous")
		}
		compatiblePath = path
	}
	if compatiblePath == "" {
		return errors.New("DLM fencing helper's exact stonith library is incompatible")
	}
	exports, err := storageClusterELFExports(compatiblePath)
	if err != nil || !exports["stonith_api_time"] || !exports["stonith_api_kick"] {
		return errors.New("DLM fencing helper's exact stonith library is incompatible")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func storageClusterFenceHelperSONAME(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("DLM fencing helper is unreadable")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, storageClusterFenceHelperMaximumBytes+1))
	if err != nil || len(raw) > storageClusterFenceHelperMaximumBytes {
		return "", errors.New("DLM fencing helper cannot be inspected safely")
	}
	return storageClusterFenceSONAME(raw)
}

func storageClusterFenceSONAME(raw []byte) (string, error) {
	const prefix = "libstonithd.so."
	var found string
	for start := 0; start < len(raw); {
		i := bytes.Index(raw[start:], []byte(prefix))
		if i < 0 {
			break
		}
		i += start
		if i == 0 || raw[i-1] == 0 {
			end := i + len(prefix)
			for end < len(raw) && raw[end] >= '0' && raw[end] <= '9' {
				end++
			}
			if end > i+len(prefix) && end < len(raw) && raw[end] == 0 {
				candidate := string(raw[i:end])
				if found != "" {
					return "", errors.New("DLM fencing helper has ambiguous stonith library requirements")
				}
				found = candidate
			}
		}
		start = i + len(prefix)
	}
	if found == "" {
		return "", errors.New("DLM fencing helper does not declare an inspectable stonith library")
	}
	return found, nil
}

func storageClusterLibraryCachePaths(raw []byte, soname string) []string {
	paths := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		before, path, ok := strings.Cut(line, " => ")
		fields := strings.Fields(before)
		if !ok || len(fields) == 0 || fields[0] != soname || !filepath.IsAbs(path) {
			continue
		}
		paths[path] = true
	}
	out := make([]string, 0, len(paths))
	for path := range paths {
		out = append(out, path)
	}
	return out
}
