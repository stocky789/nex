// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

const sanlockLeaseSize = 1 << 20

type SanlockLockspaceConfig struct {
	Lockspace   string
	LeasePath   string
	LocalHostID uint64
	HostIDs     []uint64
	IsMaster    bool
	Initialize  bool
	IOTimeout   uint32
}

type SanlockResource struct {
	Lockspace string
	Key       string
	Path      string
	Offset    uint64
}

type SanlockResourceConfig struct {
	SanlockResource
	Initialize       bool
	MasterAuthorized bool
}

const joinSanlockLockspace = `
import json, sys, sanlock
r=json.load(sys.stdin)
name=r["lockspace"].encode("ascii")
path=r["path"]
try:
  data=sanlock.read_lockspace(path, offset=0)
  if data.get("lockspace") != name:
    sys.exit(2)
except Exception:
  if not r["initialize"]:
    sys.exit(3)
  sanlock.write_lockspace(name, path, offset=0, max_hosts=2000,
                          iotimeout=r["io_timeout"], align=1048576, sector=512)
else:
  if r["initialize"]:
    sys.exit(4)
try:
  if sanlock.inq_lockspace(name, r["host_id"], path, offset=0) is not True:
    sanlock.add_lockspace(name, r["host_id"], path, offset=0,
                          iotimeout=r["io_timeout"], wait=True)
  if sanlock.inq_lockspace(name, r["host_id"], path, offset=0, wait=True) is not True:
    sys.exit(5)
  hosts=sanlock.get_hosts(name, r["host_id"])
except Exception:
  sys.exit(6)
if len(hosts) != 1 or hosts[0].get("host_id") != r["host_id"]:
  sys.exit(7)
h=hosts[0]
states={sanlock.HOST_UNKNOWN:"HOST_UNKNOWN", sanlock.HOST_FREE:"HOST_FREE",
        sanlock.HOST_LIVE:"HOST_LIVE", sanlock.HOST_FAIL:"HOST_FAIL", sanlock.HOST_DEAD:"HOST_DEAD"}
if h.get("flags") not in states or any(type(h.get(k)) is not int for k in
     ("host_id", "generation", "timestamp", "io_timeout", "flags")):
  sys.exit(8)
sys.stdout.write(json.dumps({"host_id":h["host_id"], "generation":h["generation"],
  "timestamp":h["timestamp"], "io_timeout":h["io_timeout"], "host_state":states[h["flags"]]}, separators=(",", ":")))
`

const ensureSanlockResource = `
import json, sys, sanlock
r=json.load(sys.stdin)
path=r["path"]
name=r["lockspace"].encode("ascii")
key=r["key"].encode("ascii")
try:
  data=sanlock.read_resource(path, offset=r["offset"], align=1048576, sector=512)
  if data.get("lockspace") != name or data.get("resource") != key:
    sys.exit(2)
except Exception:
  if not r["initialize"]:
    sys.exit(3)
  sanlock.write_resource(name, key, [(path, r["offset"])], align=1048576, sector=512)
  data=sanlock.read_resource(path, offset=r["offset"], align=1048576, sector=512)
  if data.get("lockspace") != name or data.get("resource") != key:
    sys.exit(4)
sys.stdout.write("ok")
`

// EnsureSanlockLockspace initializes a new lease file only when the caller is the master
// and explicitly sets Initialize. Every host joins through libsanlock and receives its
// exact local host record; import readiness must not rely on this call's intent alone.
func EnsureSanlockLockspace(ctx context.Context, cfg SanlockLockspaceConfig) (sanlockHostStatus, error) {
	var status sanlockHostStatus
	if !sanlockName(cfg.Lockspace) ||
		cfg.LocalHostID == 0 || cfg.LocalHostID > 2000 || cfg.IOTimeout == 0 || cfg.IOTimeout > 600 ||
		cfg.Initialize && !cfg.IsMaster ||
		!filepath.IsAbs(cfg.LeasePath) || filepath.Clean(cfg.LeasePath) != cfg.LeasePath ||
		filepath.Base(cfg.LeasePath) != "lockspace" || filepath.Base(filepath.Dir(cfg.LeasePath)) != ".nex-sanlock" {
		return status, errors.New("invalid sanlock lockspace configuration")
	}
	seen, local := map[uint64]bool{}, false
	for _, id := range cfg.HostIDs {
		if id == 0 || id > 2000 || seen[id] {
			return status, errors.New("invalid sanlock host ID set")
		}
		seen[id] = true
		local = local || id == cfg.LocalHostID
	}
	if len(cfg.HostIDs) == 0 || !local {
		return status, errors.New("sanlock host ID set omits this host")
	}

	dir := filepath.Dir(cfg.LeasePath)
	if cfg.Initialize {
		if _, err := os.Lstat(cfg.LeasePath); err == nil || !os.IsNotExist(err) {
			return status, errors.New("refusing to initialize an existing or ambiguous sanlock lease file")
		}
	}
	if info, err := os.Lstat(dir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return status, errors.New("sanlock lease directory is not a real directory")
		}
		if cfg.Initialize {
			if err := checkSanlockOwnership(dir, true); err != nil {
				return status, errors.New("existing sanlock lease directory ownership is invalid")
			}
		}
	} else if !os.IsNotExist(err) {
		return status, errors.New("cannot inspect sanlock lease directory")
	}
	if cfg.Initialize {
		if _, err := os.Lstat(dir); os.IsNotExist(err) {
			if err := os.Mkdir(dir, 0770); err != nil {
				return status, errors.New("cannot create sanlock lease directory")
			}
			if err := setSanlockOwnership(dir, true); err != nil {
				return status, errors.New("cannot prepare sanlock lease directory ownership")
			}
		}
		f, err := os.OpenFile(cfg.LeasePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return status, errors.New("refusing to initialize an existing sanlock lease file")
		}
		if err = f.Truncate(sanlockLeaseSize); err == nil {
			err = f.Sync()
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		} else {
			_ = closeErr
		}
		if err != nil {
			return status, errors.New("cannot size sanlock lease file")
		}
		if err := setSanlockOwnership(cfg.LeasePath, false); err != nil {
			return status, errors.New("cannot prepare sanlock lease file ownership")
		}
	} else {
		info, err := os.Lstat(cfg.LeasePath)
		if err != nil || !info.Mode().IsRegular() || info.Size() < sanlockLeaseSize {
			return status, errors.New("registered sanlock lockspace file is missing or invalid")
		}
		if err := checkSanlockOwnership(dir, true); err != nil {
			return status, errors.New("registered sanlock lease directory ownership is invalid")
		}
		if err := checkSanlockOwnership(cfg.LeasePath, false); err != nil {
			return status, errors.New("registered sanlock lease file ownership is invalid")
		}
	}
	request := struct {
		Lockspace  string `json:"lockspace"`
		Path       string `json:"path"`
		HostID     uint64 `json:"host_id"`
		IOTimeout  uint32 `json:"io_timeout"`
		Initialize bool   `json:"initialize"`
	}{cfg.Lockspace, cfg.LeasePath, cfg.LocalHostID, cfg.IOTimeout, cfg.Initialize}
	data, err := json.Marshal(request)
	if err == nil {
		var output bytes.Buffer
		cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", joinSanlockLockspace)
		cmd.Stdin = bytes.NewReader(data)
		cmd.Stdout = &output
		cmd.Stderr = io.Discard
		err = cmd.Run()
		if err == nil {
			status, err = decodeSanlockHostStatus(output.Bytes(), cfg.LocalHostID)
		}
	}
	if err != nil {
		// Once libsanlock has been invoked the new lease file may already be live.
		// Keep ambiguous state for reconciliation; removing it could split lockspace.
		return sanlockHostStatus{}, errors.New("sanlock lockspace registration failed")
	}
	if status.HostState != "HOST_LIVE" || status.IOTimeout != cfg.IOTimeout {
		return sanlockHostStatus{}, errors.New("local sanlock host is not live in the expected lockspace")
	}
	return status, nil
}

// EnsureSanlockResource initializes a deterministic resource file once, then validates
// its recorded lockspace/key on every retry. It never rewrites a live or ambiguous lease.
func EnsureSanlockResource(ctx context.Context, cfg SanlockResourceConfig) error {
	resource := cfg.SanlockResource
	if !validSanlockResource(resource) || cfg.Initialize && !cfg.MasterAuthorized {
		return errors.New("invalid sanlock resource configuration")
	}
	dir := filepath.Dir(resource.Path)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sanlock resource directory is unavailable")
	}
	if cfg.Initialize {
		created := false
		f, err := os.OpenFile(resource.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			created = true
			if err = f.Truncate(sanlockLeaseSize); err == nil {
				err = f.Sync()
			}
			if closeErr := f.Close(); err == nil {
				err = closeErr
			} else {
				_ = closeErr
			}
			if err != nil {
				return errors.New("cannot create sanlock resource file")
			}
			if err := setSanlockOwnership(resource.Path, false); err != nil {
				return errors.New("cannot prepare sanlock resource file ownership")
			}
		} else if !os.IsExist(err) {
			return errors.New("cannot create sanlock resource file")
		}
		cfg.Initialize = created
	}
	info, err := os.Lstat(resource.Path)
	if err != nil || !info.Mode().IsRegular() || info.Size() < sanlockLeaseSize {
		return errors.New("sanlock resource file is missing or invalid")
	}
	if err := checkSanlockOwnership(dir, true); err != nil {
		return errors.New("sanlock resource directory ownership is invalid")
	}
	if err := checkSanlockOwnership(resource.Path, false); err != nil {
		return errors.New("sanlock resource file ownership is invalid")
	}
	request := struct {
		Lockspace  string `json:"lockspace"`
		Key        string `json:"key"`
		Path       string `json:"path"`
		Offset     uint64 `json:"offset"`
		Initialize bool   `json:"initialize"`
	}{resource.Lockspace, resource.Key, resource.Path, resource.Offset, cfg.Initialize}
	data, err := json.Marshal(request)
	if err != nil {
		return errors.New("invalid sanlock resource request")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", ensureSanlockResource)
	cmd.Stdin = bytes.NewReader(data)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("sanlock resource initialization or validation failed")
	}
	return nil
}

func setSanlockOwnership(path string, directory bool) error {
	account, err := user.Lookup("sanlock")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if directory {
		mode = 0770
	}
	if err := os.Chown(path, uid, gid); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return checkSanlockOwnership(path, directory)
}

func checkSanlockOwnership(path string, directory bool) error {
	account, err := user.Lookup("sanlock")
	if err != nil {
		return err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("sanlock path is unavailable")
	}
	if directory && !info.IsDir() || !directory && !info.Mode().IsRegular() {
		return errors.New("sanlock path has an unexpected type")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid || int(stat.Gid) != gid {
		return errors.New("sanlock path has an unexpected owner")
	}
	wanted := os.FileMode(0600)
	if directory {
		wanted = 0770
	}
	if info.Mode().Perm() != wanted {
		return errors.New("sanlock path has unexpected permissions")
	}
	return nil
}

func validSanlockResource(resource SanlockResource) bool {
	return sanlockName(resource.Lockspace) && sanlockName(resource.Key) &&
		filepath.IsAbs(resource.Path) && filepath.Clean(resource.Path) == resource.Path &&
		len(resource.Path) <= 1023 && !strings.ContainsAny(resource.Path, ":\x00\r\n") &&
		filepath.Base(filepath.Dir(resource.Path)) == ".nex-sanlock" && resource.Offset == 0
}

func sanlockName(name string) bool {
	if name == "" || len(name) > 48 || strings.ContainsAny(name, ":/\x00\r\n") {
		return false
	}
	for _, r := range name {
		if r > 0x7f {
			return false
		}
	}
	return true
}
