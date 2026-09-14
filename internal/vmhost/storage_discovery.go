// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bufio"
	"context"
	"errors"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"nex.local/nex/internal/protocol"
)

const discoveryLimit = 64

var (
	discoveryMounts       = func() (*os.File, error) { return os.Open("/proc/mounts") }
	discoveryNFSLine      = regexp.MustCompile(`^d[-rwxstST]{9}\s+\d+\s+\d+\s+\d+\s+\d+\s+(.+)$`)
	discoverySMBLine      = regexp.MustCompile(`^\s*(.+?)\s+([A-Z]+)\s+\d+\s+`)
	discoverySMBShareName = regexp.MustCompile(`^[\pL\pN][\pL\pN ._$:()\-]{0,253}$`)
	discoveryLocalSafe    = localStorage
)

// DiscoverStorage performs transient observations only: it does not mount, log in or persist data.
func DiscoverStorage(ctx context.Context, r protocol.StorageDiscoveryRequest) protocol.StorageDiscoveryResult {
	result := protocol.StorageDiscoveryResult{RequestID: r.RequestID, PoolID: r.PoolID, HostID: r.HostID, Status: "failed", ObservedAt: time.Now().UTC()}
	if err := protocol.ValidateStorageDiscovery(r); err != nil {
		result.Error = err.Error()
		return result
	}
	if ctx.Err() != nil {
		result.Status = "cancelled"
		result.Error = "discovery cancelled"
		return result
	}
	var candidates []protocol.StorageDiscoveryCandidate
	var err error
	switch r.Kind {
	case protocol.StorageKindLocal:
		candidates, err = discoverDirectories(ctx, r.Path)
	case protocol.StorageKindZFS:
		candidates, err = discoverMounted(ctx, "zfs", r.Path, "")
	case protocol.StorageKindNFS:
		candidates, err = discoverNFS(ctx, r)
	case protocol.StorageKindSMB:
		candidates, err = discoverSMB(ctx, r)
	case protocol.StorageKindISCSI:
		candidates, err = discoverISCSI(ctx, r)
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			result.Status = "cancelled"
			result.Error = "discovery cancelled"
		} else {
			result.Error = "storage discovery is unavailable"
		}
		return result
	}
	result.Status = "succeeded"
	result.Candidates = candidates
	return result
}

func discoverDirectories(ctx context.Context, root string) ([]protocol.StorageDiscoveryCandidate, error) {
	if root == "" {
		root = "/var/lib/libvirt/images"
	}
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("unsafe directory")
	}
	i, err := os.Lstat(root)
	if err != nil || i.Mode()&os.ModeSymlink != 0 || !i.IsDir() {
		return nil, errors.New("directory is unavailable")
	}
	if err := discoveryLocalSafe(root, 0); err != nil {
		return nil, errors.New("directory is not a Core-safe storage path")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, errors.New("directory cannot be listed")
	}
	out := []protocol.StorageDiscoveryCandidate{{Kind: protocol.StorageKindLocal, Path: root, Browsable: true}}
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(out) == discoveryLimit {
			break
		}
		p := filepath.Join(root, e.Name())
		i, err := os.Lstat(p)
		if err == nil && i.IsDir() && i.Mode()&os.ModeSymlink == 0 && discoveryLocalSafe(p, 0) == nil {
			out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindLocal, Path: p, Browsable: true})
		}
	}
	return out, nil
}

func discoverNFS(ctx context.Context, r protocol.StorageDiscoveryRequest) ([]protocol.StorageDiscoveryCandidate, error) {
	if r.Path != "" {
		// libnfs-utils lists an explicitly selected export without creating a kernel mount.
		uid, gid, err := libvirtQEMUIdentity(ctx)
		if err != nil {
			return nil, err
		}
		u := url.URL{Scheme: "nfs", Host: r.Server, Path: r.Path, RawQuery: "uid=" + url.QueryEscape(uid) + "&gid=" + url.QueryEscape(gid)}
		b, err := nativeRun(ctx, "nfs-ls", u.String())
		if err != nil {
			return nil, err
		}
		var out []protocol.StorageDiscoveryCandidate
		for _, line := range strings.Split(string(b), "\n") {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			directory, ok := discoveryNFSDirectory(line)
			if !ok {
				continue
			}
			out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: r.Server, Export: r.Path, Directory: directory}}, Path: r.Path + "/" + directory, Browsable: true})
			if len(out) == discoveryLimit {
				break
			}
		}
		return out, nil
	}
	b, err := nativeRun(ctx, "showmount", "-e", r.Server)
	if err != nil {
		return nil, err
	}
	var out []protocol.StorageDiscoveryCandidate
	for _, line := range strings.Split(string(b), "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f := strings.Fields(line)
		if len(f) == 0 || !strings.HasPrefix(f[0], "/") || (r.Path != "" && f[0] != r.Path) {
			continue
		}
		out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindNFS, Source: protocol.StorageSource{NFS: &protocol.NFSStorageSource{Server: r.Server, Export: f[0]}}, Path: f[0], Browsable: true})
		if len(out) == discoveryLimit {
			break
		}
	}
	return out, nil
}

func discoverSMB(ctx context.Context, r protocol.StorageDiscoveryRequest) ([]protocol.StorageDiscoveryCandidate, error) {
	f, err := os.CreateTemp("", "nex-smb-discovery-")
	if err != nil {
		return nil, err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err != nil {
		f.Close()
		return nil, err
	}
	text := "password=\n"
	if r.Credentials != nil {
		text = "username=" + r.Credentials.Username + "\npassword=" + r.Credentials.Secret + "\n"
	}
	if _, err = f.WriteString(text); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	args := []string{"-g", "-A", name}
	if r.Path == "" {
		args = append(args, "-L", r.Server)
		b, err := nativeRun(ctx, "smbclient", args...)
		if err != nil {
			return nil, err
		}
		return parseSMBShares(ctx, r.Server, string(b)), nil
	}
	share, dir, ok := discoverySMBPath(r.Path)
	if !ok {
		return nil, errors.New("unsafe SMB path")
	}
	args = append(args, "//"+r.Server+"/"+share, "-D", dir, "-c", "ls")
	b, err := nativeRun(ctx, "smbclient", args...)
	if err != nil {
		return nil, err
	}
	return parseSMBDirectories(ctx, r.Server, share, dir, string(b)), nil
}
func discoverySMBPath(v string) (string, string, bool) {
	p := strings.Split(strings.TrimPrefix(path.Clean(v), "/"), "/")
	if len(p) == 0 {
		return "", "", false
	}
	for i, x := range p {
		if !discoveryComponent(x) || (i == 0 && !discoverySMBShare(x)) {
			return "", "", false
		}
	}
	return p[0], strings.Join(p[1:], "/"), true
}
func parseSMBShares(ctx context.Context, server, text string) []protocol.StorageDiscoveryCandidate {
	var out []protocol.StorageDiscoveryCandidate
	for _, line := range strings.Split(text, "\n") {
		if ctx.Err() != nil || len(out) == discoveryLimit {
			break
		}
		p := strings.Split(line, "|")
		if len(p) < 2 || p[0] != "Disk" || !discoverySMBShare(p[1]) {
			continue
		}
		out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: server, Share: p[1]}}, Path: "/" + p[1], Browsable: true})
	}
	return out
}
func parseSMBDirectories(ctx context.Context, server, share, dir, text string) []protocol.StorageDiscoveryCandidate {
	var out []protocol.StorageDiscoveryCandidate
	for _, line := range strings.Split(text, "\n") {
		if ctx.Err() != nil || len(out) == discoveryLimit {
			break
		}
		p := discoverySMBLine.FindStringSubmatch(line)
		if len(p) != 3 || !strings.Contains(p[2], "D") || !discoveryComponent(p[1]) {
			continue
		}
		out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindSMB, Source: protocol.StorageSource{SMB: &protocol.SMBStorageSource{Server: server, Share: share}}, Path: "/" + share + "/" + strings.TrimPrefix(dir+"/"+p[1], "/"), Browsable: true})
	}
	return out
}

func discoveryNFSDirectory(line string) (string, bool) {
	if p := discoveryNFSLine.FindStringSubmatch(line); len(p) == 2 && discoveryComponent(p[1]) {
		return p[1], true
	}
	p := strings.Fields(line)
	if len(p) == 2 && strings.HasPrefix(p[0], "d") && discoveryComponent(p[1]) {
		return p[1], true
	}
	return "", false
}

func discoverySMBShare(v string) bool {
	return strings.TrimSpace(v) == v && discoverySMBShareName.MatchString(v)
}

func discoveryComponent(v string) bool {
	return v != "" && v != "." && v != ".." && len(v) <= 253 && !strings.ContainsAny(v, "/\\\x00\r\n") && !strings.ContainsFunc(v, unicode.IsControl)
}

func discoverISCSI(ctx context.Context, r protocol.StorageDiscoveryRequest) ([]protocol.StorageDiscoveryCandidate, error) {
	// nonpersistent SendTargets writes no discovery database, node or session; no login is issued.
	b, err := nativeRun(ctx, "iscsiadm", "-m", "discoverydb", "-t", "sendtargets", "-p", r.Server, "-o", "nonpersistent", "--discover")
	if err != nil {
		return nil, err
	}
	var out []protocol.StorageDiscoveryCandidate
	for _, line := range strings.Split(string(b), "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		p := strings.Fields(line)
		if len(p) < 2 || !strings.HasPrefix(p[1], "iqn.") || len(p[1]) > 223 || strings.ContainsAny(p[1], " \t\r\n") {
			continue
		}
		out = append(out, protocol.StorageDiscoveryCandidate{Kind: protocol.StorageKindISCSI, Source: protocol.StorageSource{ISCSI: &protocol.ISCSIStorageSource{Portal: r.Server, TargetIQN: p[1]}}})
		if len(out) == discoveryLimit {
			break
		}
	}
	return out, nil
}

func discoverMounted(ctx context.Context, fstype, root, server string) ([]protocol.StorageDiscoveryCandidate, error) {
	f, err := discoveryMounts()
	if err != nil {
		return nil, errors.New("mounted storage cannot be inspected")
	}
	defer f.Close()
	var out []protocol.StorageDiscoveryCandidate
	s := bufio.NewScanner(f)
	for s.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v := strings.Fields(s.Text())
		if len(v) < 3 || (fstype != "" && v[2] != fstype && !(fstype == "nfs" && v[2] == "nfs4")) {
			continue
		}
		source, mount := v[0], v[1]
		if root != "" && mount != root {
			continue
		}
		if server != "" && !strings.HasPrefix(source, server+":") && !strings.HasPrefix(source, "//"+server+"/") {
			continue
		}
		c := protocol.StorageDiscoveryCandidate{Path: mount, Browsable: true}
		switch v[2] {
		case "nfs", "nfs4":
			p := strings.SplitN(source, ":", 2)
			if len(p) != 2 {
				continue
			}
			c.Kind = protocol.StorageKindNFS
			c.Source.NFS = &protocol.NFSStorageSource{Server: p[0], Export: p[1]}
		case "cifs":
			p := strings.Split(strings.TrimPrefix(source, "//"), "/")
			if len(p) != 2 {
				continue
			}
			c.Kind = protocol.StorageKindSMB
			c.Source.SMB = &protocol.SMBStorageSource{Server: p[0], Share: p[1]}
		case "zfs":
			p := strings.SplitN(source, "/", 2)
			if len(p) != 2 {
				continue
			}
			c.Kind = protocol.StorageKindZFS
			c.Source.ZFS = &protocol.ZFSStorageSource{Pool: p[0], Dataset: source, MountPath: mount}
		default:
			continue
		}
		out = append(out, c)
		if len(out) == discoveryLimit {
			break
		}
	}
	if s.Err() != nil {
		return nil, errors.New("mounted storage cannot be inspected")
	}
	return out, nil
}
