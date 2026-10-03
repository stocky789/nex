// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Update settings, offline bundles (import) and on_demand payloads (fetch). All three reuse the engine's one
// verification path: a bundle is read through DirSource by the same fetchMetadataFrom that checks an online
// source, so trust, expiry, replay, floor and blob hashes are judged identically.

// Paths of the update media. Tests replace them; a host never does.
var (
	MediaLabelDevice = "/dev/disk/by-label/NEXUPDATE"
	MountsPath       = "/proc/self/mounts"
)

// Settings is the host's update source: the channel it follows and the base URL it reads. There is no
// built-in base URL; an empty one means the host has no online source.
type Settings struct {
	Channel string `json:"channel"`
	BaseURL string `json:"base_url"`
}

var channelRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// ValidateSettings checks and normalises settings. https is the expectation, plain http is allowed because
// signatures and blob hashes, not the transport, carry integrity; credentials, queries and fragments are
// not (a base URL is stored in clear and reported in the inventory).
func ValidateSettings(s Settings) (Settings, error) {
	s.Channel, s.BaseURL = strings.TrimSpace(s.Channel), strings.TrimSpace(s.BaseURL)
	if !channelRE.MatchString(s.Channel) {
		return s, refuse(400, "invalid_channel", "channel must be 1 to 32 lower-case letters, digits or - and start with a letter")
	}
	if s.BaseURL == "" {
		return s, nil
	}
	u, err := url.Parse(s.BaseURL)
	if err != nil || len(s.BaseURL) > 2048 || strings.ContainsAny(s.BaseURL, " \t\r\n") || (u.Scheme != "http" && u.Scheme != "https") ||
		u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(s.BaseURL, "#") || strings.Contains(s.BaseURL, "?") {
		return s, refuse(400, "invalid_base_url", "base_url must be an http or https URL without credentials, query or fragment")
	}
	s.BaseURL = strings.TrimRight(s.BaseURL, "/")
	return s, nil
}

// SaveImageSettings writes channel= and base_url= into /etc/nex/updates.conf, keeping every other line.
// It refuses while an environment override is set, because the environment would silently win.
func SaveImageSettings(s Settings) error {
	if os.Getenv("NEX_UPDATE_BASE_URL") != "" || os.Getenv("NEX_UPDATE_CHANNEL") != "" {
		return refuse(409, "settings_overridden", "the update settings are fixed by the service environment and cannot be changed here")
	}
	var kept []string
	if b, err := os.ReadFile(UpdatesConfig); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			t := strings.TrimSpace(line)
			if !strings.HasPrefix(t, "base_url=") && !strings.HasPrefix(t, "channel=") && t != "" {
				kept = append(kept, line)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	kept = append(kept, "channel="+s.Channel)
	if s.BaseURL != "" {
		kept = append(kept, "base_url="+s.BaseURL)
	}
	if err := os.MkdirAll(filepath.Dir(UpdatesConfig), 0o755); err != nil {
		return err
	}
	return writeFileAtomic(UpdatesConfig, []byte(strings.Join(kept, "\n")+"\n"), 0o644)
}

// UpdateSettings is PUT /updates/settings. The settings take effect for the next check. The pointer history
// belongs to the source it came from (replayKey), so a changed source starts empty without any reset, while
// the rollback floor, which is about releases and not about a source, is kept.
func (e *Engine) UpdateSettings(ctx context.Context, s Settings) (Settings, error) {
	s, err := ValidateSettings(s)
	if err != nil {
		return s, err
	}
	if err = e.enter(lockWait); err != nil {
		return s, err
	}
	defer e.leave()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err = e.load(); err != nil {
		return s, err
	}
	if e.active != nil || e.checks > 0 {
		return s, refuse(409, "update_in_progress", "an update check or operation is running; change the settings when it has finished")
	}
	base, channel := e.cfg.Settings()
	if base == s.BaseURL && channel == s.Channel {
		return s, nil
	}
	save := e.cfg.SaveSettings
	if save == nil {
		save = SaveImageSettings
	}
	// The record goes first: a crash before the file is written leaves the old source with nothing
	// to show, and repeating the request finishes the change.
	available, shown := e.st.Available, e.st.Channel
	e.st.Available, e.st.Channel = nil, s.Channel
	if err = e.save(); err != nil {
		return s, err
	}
	if err = save(s); err != nil {
		e.st.Available, e.st.Channel = available, shown
		_ = e.save()
		return s, err
	}
	e.lastCheck = time.Time{}
	return s, nil
}

// ---- offline media ----

// MediaChannel is a channel found on media, read from the unverified channel.json for display only.
type MediaChannel struct {
	Name     string `json:"name"`
	Sequence int    `json:"sequence"`
}

// Media is one candidate bundle: the host's inbox, a directory in it, or the device labelled NEXUPDATE.
type Media struct {
	ID        string         `json:"id"` // inbox, inbox/<name> or label
	Source    string         `json:"source"`
	Mounted   bool           `json:"mounted"`
	Channels  []MediaChannel `json:"channels,omitempty"`
	Checksums bool           `json:"has_sha256sums"`
}

var mediaNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func (e *Engine) inboxDir() string {
	if e.cfg.InboxDir != "" {
		return e.cfg.InboxDir
	}
	return filepath.Join(e.cfg.Dir, "inbox")
}

// bundleChannels lists the channels of a directory in the online layout.
func bundleChannels(dir string) []MediaChannel {
	var out []MediaChannel
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil
	}
	defer root.Close()
	entries, _ := os.ReadDir(dir)
	for _, d := range entries {
		if !d.IsDir() || !channelRE.MatchString(d.Name()) {
			continue
		}
		b, err := readFileIn(root, d.Name()+"/channel.json", maxMetadata)
		if err != nil {
			continue
		}
		var c Channel
		if json.Unmarshal(b, &c) == nil {
			out = append(out, MediaChannel{Name: d.Name(), Sequence: c.Sequence})
		}
	}
	return out
}

func hasSums(root string) bool {
	st, err := os.Stat(filepath.Join(root, "SHA256SUMS"))
	return err == nil && st.Mode().IsRegular()
}

// labelledMount returns the device labelled NEXUPDATE and where it is mounted ("" when it is not).
func labelledMount() (device, dir string) {
	device, err := filepath.EvalSymlinks(MediaLabelDevice)
	if err != nil {
		return "", ""
	}
	b, _ := os.ReadFile(MountsPath)
	for _, line := range strings.Split(string(b), "\n") {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == device {
			return device, strings.ReplaceAll(f[1], `\040`, " ")
		}
	}
	return device, ""
}

func (e *Engine) scanMedia() []Media {
	var out []Media
	inbox := e.inboxDir()
	if ch := bundleChannels(inbox); len(ch) > 0 {
		out = append(out, Media{ID: "inbox", Source: "inbox", Mounted: true, Channels: ch, Checksums: hasSums(inbox)})
	}
	entries, _ := os.ReadDir(inbox)
	for _, d := range entries {
		root := filepath.Join(inbox, d.Name())
		if !d.IsDir() || !mediaNameRE.MatchString(d.Name()) {
			continue
		}
		if ch := bundleChannels(root); len(ch) > 0 {
			out = append(out, Media{ID: "inbox/" + d.Name(), Source: "inbox", Mounted: true, Channels: ch, Checksums: hasSums(root)})
		}
	}
	if dev, dir := labelledMount(); dev != "" {
		m := Media{ID: "label", Source: "label", Mounted: dir != ""}
		if dir != "" {
			m.Channels, m.Checksums = bundleChannels(dir), hasSums(dir)
		}
		out = append(out, m)
	}
	return out
}

// Media is GET /updates/media: the candidates, read-only. Nothing on the media is changed and nothing is
// mounted. The scan is remembered in the engine's record when no operation holds it, so the Controller
// reads it from Inventory.image.
func (e *Engine) Media(ctx context.Context) ([]Media, error) {
	media := e.scanMedia()
	if media == nil {
		media = []Media{}
	}
	if err := e.enter(0); err != nil {
		if isBusy(err) {
			return media, nil
		}
		return nil, err
	}
	defer e.leave()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := e.load(); err != nil {
		return nil, err
	}
	e.st.Media = media
	return media, e.save()
}

// openMedia resolves a media id to a directory the engine can read. Unmounted labelled media is mounted
// read-only for the duration of the operation and released by done.
func (e *Engine) openMedia(ctx context.Context, id string) (string, func(), error) {
	noop := func() {}
	unknown := refuse(404, "unknown_media", "no such update media")
	switch {
	case id == "inbox":
		return e.mediaDir(e.inboxDir(), unknown)
	case strings.HasPrefix(id, "inbox/") && mediaNameRE.MatchString(strings.TrimPrefix(id, "inbox/")):
		return e.mediaDir(filepath.Join(e.inboxDir(), strings.TrimPrefix(id, "inbox/")), unknown)
	case id == "label":
		dev, dir := labelledMount()
		if dev == "" {
			return "", noop, unknown
		}
		if dir != "" {
			return dir, noop, nil
		}
		mount := e.cfg.Mount
		if mount == nil {
			mount = hostMount
		}
		dir, done, err := mount(ctx, dev)
		if err != nil {
			return "", noop, refuse(503, "media_unreadable", "the update media cannot be mounted")
		}
		return dir, done, nil
	}
	return "", noop, unknown
}

func (e *Engine) mediaDir(dir string, unknown error) (string, func(), error) {
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return "", func() {}, unknown
	}
	return dir, func() {}, nil
}

// hostMount mounts a device read-only (no execution, no device nodes) in a private directory.
func hostMount(ctx context.Context, device string) (string, func(), error) {
	if err := os.MkdirAll("/run/nex", 0o755); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("/run/nex", "update-media-")
	if err != nil {
		return "", nil, err
	}
	if err = Command(ctx, "mount", "-o", "ro,nosuid,nodev,noexec", device, dir).Run(); err != nil {
		_ = os.Remove(dir)
		return "", nil, err
	}
	return dir, func() {
		_ = Command(context.Background(), "umount", dir).Run()
		_ = os.Remove(dir)
	}, nil
}

// sourceFor picks where an operation reads from: the configured update server, or the bundle it names.
// The returned release function must run when the operation ends.
func (e *Engine) sourceFor(ctx context.Context, req ApplyRequest) (Source, string, func(), error) {
	noop := func() {}
	_, configured := e.cfg.Settings()
	if req.media == "" {
		base, channel := e.cfg.Settings()
		if strings.TrimSpace(base) == "" {
			return nil, "", noop, refuse(503, "source_unconfigured", "no update base URL is configured")
		}
		return e.cfg.Source(base), channel, noop, nil
	}
	root, done, err := e.openMedia(ctx, req.media)
	if err != nil {
		return nil, "", noop, err
	}
	channels := bundleChannels(root)
	pick := func(name string) bool {
		for _, c := range channels {
			if c.Name == name {
				return true
			}
		}
		return false
	}
	channel := req.channel
	switch {
	case channel != "" && !pick(channel):
		done()
		return nil, "", noop, refuse(409, "channel_not_in_bundle", "the bundle does not carry that channel")
	case channel == "" && pick(configured):
		channel = configured
	case channel == "" && len(channels) == 1:
		channel = channels[0].Name
	case channel == "":
		done()
		return nil, "", noop, refuse(409, "channel_ambiguous", "the bundle carries several channels; name one")
	}
	return DirSource{Root: root}, channel, done, nil
}

// ---- import ----

// ImportRequest is POST /updates/import: stage a release from media. It is an apply whose source is the
// bundle, so it ends staged (or armed, with activate reboot) exactly as an online apply does.
type ImportRequest struct {
	RequestID  string `json:"request_id"`
	Media      string `json:"media"`
	Channel    string `json:"channel,omitempty"`
	Sequence   int    `json:"sequence,omitempty"`
	Activate   string `json:"activate,omitempty"`
	RunningVMs string `json:"running_vms,omitempty"`
}

func (e *Engine) Import(ctx context.Context, r ImportRequest) (Operation, bool, error) {
	if r.Media == "" || (r.Channel != "" && !channelRE.MatchString(r.Channel)) {
		return Operation{}, false, refuse(400, "invalid_request", "media is required and channel must be a valid channel name")
	}
	return e.startJob(ctx, "import", ApplyRequest{RequestID: r.RequestID, Sequence: r.Sequence, Activate: r.Activate,
		RunningVMs: r.RunningVMs, media: r.Media, channel: r.Channel})
}

// ---- on_demand payloads ----

// FetchRequest is POST /updates/components/{id}/fetch. Without Media the payload comes from the update
// server.
type FetchRequest struct {
	RequestID string `json:"request_id"`
	Media     string `json:"media,omitempty"`
	Channel   string `json:"channel,omitempty"`
}

var componentIDRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// FetchComponent downloads one on_demand component of the release the source offers into
// <payload dir>/<sha256>/<name>. The layers and the slot image are not payloads: they come with a stage.
func (e *Engine) FetchComponent(ctx context.Context, id string, r FetchRequest) (Operation, bool, error) {
	if !componentIDRE.MatchString(id) || (r.Channel != "" && !channelRE.MatchString(r.Channel)) {
		return Operation{}, false, refuse(400, "invalid_request", "the component id or channel is not valid")
	}
	return e.startJob(ctx, "fetch", ApplyRequest{RequestID: r.RequestID, media: r.Media, channel: r.Channel, component: id})
}

func (e *Engine) fetchPipeline(ctx context.Context, id string, req ApplyRequest) (string, error) {
	e.setPhase(id, PhaseChecking)
	src, channel, done, err := e.sourceFor(ctx, req)
	if err != nil {
		return "", err
	}
	defer done()
	v, err := e.fetchMetadataFrom(ctx, src, channel, 0)
	if err != nil {
		return "", err
	}
	var comp *Component
	for i, c := range v.manifest.Components {
		if c.ID == req.component {
			comp = &v.manifest.Components[i]
		}
	}
	switch {
	case comp == nil:
		return "", refuse(404, "unknown_component", "the release has no such component")
	case !comp.OnDemand || comp.Kind == "slot-image" || comp.Kind == "layer":
		return "", refuse(409, "not_on_demand", "only on_demand payloads are fetched this way; the image and its layers come with a stage")
	}
	if _, err = e.stagePayload(ctx, id, src, comp); err != nil {
		return "", err
	}
	return "fetched", nil
}

// stagePayload places an on_demand component at <payload dir>/<sha256>/<name> and returns that path. The
// download is verified against the signed manifest's SHA-256 by fetchBlob; a file already in place with the
// right digest is kept.
func (e *Engine) stagePayload(ctx context.Context, id string, src Source, comp *Component) (string, error) {
	name := comp.Name
	if name == "" {
		name = comp.ID
	}
	if !payloadNameRE.MatchString(name) {
		return "", refuse(502, "metadata_invalid", "the component has no valid file name")
	}
	dir := filepath.Join(e.cfg.PayloadDir, comp.SHA256)
	dst := filepath.Join(dir, name)
	if sum, err := fileSHA256(dst); err == nil && sum == comp.SHA256 {
		return dst, nil
	}
	e.setPhase(id, PhaseDownloading)
	blob, err := fetchBlob(ctx, src, blobName(*comp), *comp, filepath.Join(e.cfg.Dir, "payload-fetch"), func(done, total int64) {
		e.setProgress(id, OpProgress{Component: comp.ID, BytesDone: done, BytesTotal: total})
	})
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err = os.Rename(blob, dst); err != nil {
		return "", err
	}
	if d, derr := os.Open(dir); derr == nil {
		_ = d.Sync()
		d.Close()
	}
	return dst, nil
}
