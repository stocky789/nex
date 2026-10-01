// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"archive/tar"
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"

	"nex.local/nex/internal/hostimage"
)

// Paths of the image edition. Tests replace them; a host never does.
var (
	OSReleasePath  = "/usr/lib/os-release"
	CmdlinePath    = "/proc/cmdline"
	UpdatesConfig  = "/etc/nex/updates.conf"
	ImageStateDir  = "/var/lib/nex/image"
	TrustStateDir  = "/var/lib/nex/update"
	PinnedTrustPth = "/usr/share/nex/update/trust.json"
)

// Edition is "image" on a host running the immutable image (its static os-release says IMAGE_ID=nex-core)
// and "classic" on a Debian host that updates through apt. The classic path stays until it is retired.
var Edition = func() string {
	if OSReleaseValue("IMAGE_ID") == "nex-core" {
		return "image"
	}
	return "classic"
}

// OSReleaseValue reads one key of the image's static os-release.
func OSReleaseValue(key string) string {
	f, err := os.Open(OSReleasePath)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if k, v, ok := strings.Cut(sc.Text(), "="); ok && k == key {
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

// HostConfig is the engine configuration of a running image host. RunningVMs is the one guest inventory
// of the host: the service and nex-update both use it, so neither can arm a reboot over guests the
// other would have seen.
func HostConfig() Config {
	return Config{Dir: ImageStateDir, TrustDir: TrustStateDir, PinnedTrust: PinnedTrustPth, Root: "/", RunningVMs: HostRunningVMs}
}

// HostRunningVMs names every libvirt domain that is not shut off (paused, crashed and in-shutdown
// domains count: they still hold guest state a reboot would destroy).
func HostRunningVMs(ctx context.Context) ([]string, error) {
	virsh := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		out, err := Command(ctx, "virsh", append([]string{"--connect", "qemu:///system"}, args...)...).Output()
		return strings.TrimSpace(string(out)), err
	}
	list, err := virsh("list", "--all", "--uuid")
	if err != nil {
		return nil, err
	}
	var running []string
	for _, id := range strings.Fields(list) {
		state, err := virsh("domstate", id)
		if err != nil {
			return nil, err
		}
		if state != "shut off" {
			name, err := virsh("domname", id)
			if err != nil {
				return nil, err
			}
			running = append(running, name)
		}
	}
	return running, nil
}

// ImageSettings reads the update base URL and channel: NEX_UPDATE_BASE_URL and NEX_UPDATE_CHANNEL,
// else base_url= and channel= in /etc/nex/updates.conf. There is no built-in base URL: it is
// configuration only. The channel defaults to stable.
func ImageSettings() (baseURL, channel string) {
	baseURL, channel = strings.TrimSpace(os.Getenv("NEX_UPDATE_BASE_URL")), strings.TrimSpace(os.Getenv("NEX_UPDATE_CHANNEL"))
	if b, err := os.ReadFile(UpdatesConfig); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if v, ok := strings.CutPrefix(line, "base_url="); ok && baseURL == "" {
				baseURL = strings.TrimSpace(v)
			}
			if v, ok := strings.CutPrefix(line, "channel="); ok && channel == "" {
				channel = strings.TrimSpace(v)
			}
		}
	}
	if channel == "" {
		channel = "stable"
	}
	return baseURL, channel
}

// hostPlatform is the real Platform: hostimage's slot writer and ESP code, the running root hash
// from the kernel command line and the image identity from os-release.
type hostPlatform struct{ host *hostimage.Host }

// HostPlatform returns the Platform of the running host.
func HostPlatform() Platform {
	return hostPlatform{host: &hostimage.Host{W: &hostimage.Writer{Runner: hostimage.ExecRunner{}}}}
}

// NewHostPlatform wraps explicit disks (lab harnesses and tests).
func NewHostPlatform(h *hostimage.Host) Platform { return hostPlatform{host: h} }

func (p hostPlatform) Booted(context.Context) (Booted, error) {
	cmdline, err := os.ReadFile(CmdlinePath)
	if err != nil {
		return Booted{}, err
	}
	b := Booted{StateFormat: osReleaseInt("NEX_STATE_FORMAT"), MinStateFormat: osReleaseInt("NEX_MIN_STATE_FORMAT"),
		MDGeneration: osReleaseInt("NEX_MD_GENERATION"), MDFallbackSafe: OSReleaseValue("NEX_MD_FALLBACK_SAFE") == "1"}
	for _, f := range strings.Fields(string(cmdline)) {
		if v, ok := strings.CutPrefix(f, "roothash="); ok {
			b.RootHash = v
		}
	}
	if b.RootHash == "" {
		return Booted{}, errors.New("the running system has no roothash= on its command line")
	}
	// BUILD_ID is <label>-<sequence>; the label may itself contain dashes.
	if id := OSReleaseValue("BUILD_ID"); id != "" {
		if i := strings.LastIndex(id, "-"); i > 0 {
			b.Label = id[:i]
			b.Sequence, _ = strconv.Atoi(id[i+1:])
		}
	}
	return b, nil
}

func osReleaseInt(key string) int {
	n, _ := strconv.Atoi(OSReleaseValue(key))
	return n
}

func (p hostPlatform) Verify(dir string) (*hostimage.Release, error) {
	return hostimage.ReadRelease(dir)
}

func (p hostPlatform) Stage(ctx context.Context, rel *hostimage.Release, b Booted) (string, error) {
	return p.host.StageSlot(ctx, rel, b.RootHash, b.Sequence)
}

func (p hostPlatform) Arm(ctx context.Context, rel *hostimage.Release) error {
	return p.host.Arm(ctx, rel)
}

func (p hostPlatform) Rollback(ctx context.Context, from, to ReleaseRecord) error {
	return p.host.Rollback(ctx, from.Sequence, to.Sequence, to.RootHash)
}

func (p hostPlatform) Entries(ctx context.Context) ([]hostimage.UKIEntry, error) {
	return p.host.Entries(ctx)
}

// SystemReboot is the production Config.Reboot.
func SystemReboot(ctx context.Context) error {
	return Command(ctx, "systemctl", "reboot").Run()
}

// extractRelease unpacks a slot-image blob (SlotEncoding: a zstd-compressed tar of the release directory,
// root.erofs, root.verity, roothash, the UKI, esp/ and manifest.json) into dir, decompressing as it
// writes: no decompressed tar is ever stored. Only regular files with clean
// relative names are accepted; the tree is built beside dir and renamed into place so a crash never
// leaves a half-extracted release that looks complete. The content is then authenticated by
// hostimage.ReadRelease, which trusts nothing the tar says.
func extractRelease(blob, dir string) error {
	f, err := os.Open(blob)
	if err != nil {
		return err
	}
	defer f.Close()
	tmp := dir + ".tmp"
	if err = os.RemoveAll(tmp); err != nil {
		return err
	}
	if err = os.RemoveAll(dir); err != nil {
		return err
	}
	if err = os.MkdirAll(tmp, 0o700); err != nil {
		return err
	}
	bad := func() error {
		_ = os.RemoveAll(tmp)
		return refuse(502, "image_invalid", "the image archive is malformed")
	}
	var total int64
	zr, err := zstd.NewReader(f, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true),
		zstd.WithDecoderMaxWindow(maxZstdWindow), zstd.WithDecoderMaxMemory(maxZstdMemory))
	if err != nil {
		return bad()
	}
	defer zr.Close()
	stream := &io.LimitedReader{R: zr, N: maxExtract + 1<<20}
	tr := tar.NewReader(stream)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return bad()
		}
		name := strings.TrimPrefix(h.Name, "./")
		if h.Typeflag == tar.TypeDir && name == "" {
			continue
		}
		if h.Typeflag == tar.TypeDir {
			if !fs.ValidPath(strings.TrimSuffix(name, "/")) {
				return bad()
			}
			continue
		}
		if h.Typeflag != tar.TypeReg || !fs.ValidPath(name) || h.Size < 0 {
			return bad()
		}
		if total += h.Size; total > maxExtract {
			return bad()
		}
		dst := filepath.Join(tmp, filepath.FromSlash(name))
		if err = os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return bad()
		}
		n, err := io.Copy(out, tr)
		if err == nil {
			err = out.Sync()
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil || n != h.Size {
			return bad()
		}
	}
	// Drain the rest of the stream: the tar reader stops at its end marker, but the zstd frame checksum is
	// only verified when the decoder reaches the end of the frame (a writer's record padding lies between).
	// The same cap bounds it.
	if _, err = io.Copy(io.Discard, stream); err != nil || stream.N <= 0 {
		return bad()
	}
	if d, err := os.Open(tmp); err == nil {
		_ = d.Sync()
		d.Close()
	}
	if err = os.Rename(tmp, dir); err != nil {
		return fmt.Errorf("extract release: %w", err)
	}
	return nil
}
