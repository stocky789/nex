// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"nex.local/nex/internal/version"
)

const defaultRepoURL = "http://172.16.4.3:8080/apt"
const maxDebBytes = 4 << 30 // 4 GiB
const ProgressFileName = "update-progress.json"

type Status struct {
	Package          string `json:"package"`
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	RepoURL          string `json:"repo_url"`
	Phase            string `json:"phase,omitempty"`    // idle|downloading|installing|restarting|done|failed
	Progress         *int   `json:"progress,omitempty"` // 0-100 when known
	Message          string `json:"message,omitempty"`
	Error            string `json:"error,omitempty"`
}

type ApplyResult struct {
	Status    string `json:"status"` // scheduled|completed|failed
	Message   string `json:"message,omitempty"`
	Operation string `json:"operation,omitempty"`
}

type Progress struct {
	Phase   string `json:"phase"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

type packageInfo struct {
	Version  string `json:"version"`
	Filename string `json:"filename"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size,omitempty"`
}

type updatesIndex struct {
	Packages map[string]packageInfo `json:"packages"`
}

type pendingUpdate struct {
	Package     string `json:"package"`
	RepoURL     string `json:"repo_url"`
	RequestedAt string `json:"requested_at"`
}

// Overridable for tests.
var (
	LookPath = exec.LookPath
	Command  = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, name, args...)
	}
	HTTPClient = &http.Client{Timeout: 60 * time.Minute}
)

var applyMu sync.Mutex
var applyRunning bool

func DefaultRepoURL() string {
	if v := strings.TrimSpace(os.Getenv("NEX_UPDATE_REPO")); v != "" {
		return strings.TrimRight(v, "/")
	}
	if b, err := os.ReadFile("/etc/nex/updates.conf"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "repo_url=") {
				return strings.TrimRight(strings.TrimSpace(strings.TrimPrefix(line, "repo_url=")), "/")
			}
		}
	}
	return defaultRepoURL
}

func InstalledVersion(packageName string) string {
	if _, err := LookPath("dpkg-query"); err == nil {
		out, err := Command(context.Background(), "dpkg-query", "-W", "-f=${Version}", packageName).Output()
		if err == nil {
			if v := strings.TrimSpace(string(out)); v != "" {
				return v
			}
		}
	}
	return version.Version
}

func ProgressPath(stateDir string) string {
	return filepath.Join(stateDir, ProgressFileName)
}

func WriteProgress(stateDir string, p Progress) error {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	if p.Percent < 0 {
		p.Percent = 0
	}
	if p.Percent > 100 {
		p.Percent = 100
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	path := ProgressPath(stateDir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func ReadProgress(stateDir string) (Progress, bool) {
	b, err := os.ReadFile(ProgressPath(stateDir))
	if err != nil {
		return Progress{}, false
	}
	var p Progress
	if json.Unmarshal(b, &p) != nil || p.Phase == "" {
		return Progress{}, false
	}
	return p, true
}

func ClearProgress(stateDir string) {
	_ = os.Remove(ProgressPath(stateDir))
}

func MergeProgress(st *Status, stateDir string) {
	p, ok := ReadProgress(stateDir)
	if !ok {
		return
	}
	st.Phase = p.Phase
	pct := p.Percent
	st.Progress = &pct
	if p.Message != "" {
		st.Message = p.Message
	}
	if p.Error != "" {
		st.Error = p.Error
	}
}

func CompareVersions(a, b string) int {
	ea, ua, da := splitDebVersion(a)
	eb, ub, db := splitDebVersion(b)
	if ea != eb {
		if ea < eb {
			return -1
		}
		return 1
	}
	if c := compareDebPart(ua, ub); c != 0 {
		return c
	}
	if da == "" && db == "" {
		return 0
	}
	if da == "" {
		return 1
	}
	if db == "" {
		return -1
	}
	return compareDebPart(da, db)
}

func splitDebVersion(v string) (epoch int, upstream, debian string) {
	v = strings.TrimSpace(v)
	if i := strings.IndexByte(v, ':'); i >= 0 {
		fmt.Sscanf(v[:i], "%d", &epoch)
		v = v[i+1:]
	}
	if i := strings.IndexByte(v, '-'); i >= 0 {
		return epoch, v[:i], v[i+1:]
	}
	return epoch, v, ""
}

func compareDebPart(a, b string) int {
	for a != "" || b != "" {
		for len(a) > 0 && len(b) > 0 && !isDigit(a[0]) && !isDigit(b[0]) {
			ca, cb := orderChar(a[0]), orderChar(b[0])
			if ca != cb {
				if ca < cb {
					return -1
				}
				return 1
			}
			a, b = a[1:], b[1:]
		}
		for len(a) > 0 && !isDigit(a[0]) {
			if b == "" {
				if a[0] == '~' {
					return -1
				}
				return 1
			}
			if orderChar(a[0]) != 0 {
				return -1
			}
			a = a[1:]
		}
		for len(b) > 0 && !isDigit(b[0]) {
			if a == "" {
				if b[0] == '~' {
					return 1
				}
				return -1
			}
			if orderChar(b[0]) != 0 {
				return 1
			}
			b = b[1:]
		}
		na, ra := takeDigits(a)
		nb, rb := takeDigits(b)
		if na != nb {
			if na < nb {
				return -1
			}
			return 1
		}
		a, b = ra, rb
	}
	return 0
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func orderChar(c byte) int {
	if unicode.IsLetter(rune(c)) {
		return int(c)
	}
	if c == '~' {
		return -1
	}
	return int(c) + 256
}

func takeDigits(s string) (int, string) {
	i := 0
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	if i == 0 {
		return 0, s
	}
	n := 0
	fmt.Sscanf(s[:i], "%d", &n)
	return n, s[i:]
}

func fetchIndex(ctx context.Context, repoURL string) (updatesIndex, error) {
	base := strings.TrimRight(repoURL, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/updates.json", nil)
	if err != nil {
		return updatesIndex{}, err
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return updatesIndex{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return updatesIndex{}, fmt.Errorf("updates index HTTP %d", resp.StatusCode)
	}
	var idx updatesIndex
	dec := json.NewDecoder(io.LimitReader(resp.Body, 1<<20))
	if err := dec.Decode(&idx); err != nil {
		return updatesIndex{}, err
	}
	if idx.Packages == nil {
		idx.Packages = map[string]packageInfo{}
	}
	return idx, nil
}

func Check(ctx context.Context, repoURL, packageName string) (Status, error) {
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	repoURL = strings.TrimRight(repoURL, "/")
	st := Status{
		Package:          packageName,
		InstalledVersion: InstalledVersion(packageName),
		RepoURL:          repoURL,
	}
	idx, err := fetchIndex(ctx, repoURL)
	if err != nil {
		st.Error = err.Error()
		return st, nil
	}
	info, ok := idx.Packages[packageName]
	if !ok || info.Version == "" {
		st.Error = "package not listed in updates index"
		return st, nil
	}
	st.AvailableVersion = info.Version
	st.UpdateAvailable = CompareVersions(st.InstalledVersion, info.Version) < 0
	return st, nil
}

type progressWriter struct {
	w       io.Writer
	total   int64
	written int64
	last    int
	onPct   func(int)
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.written += int64(n)
	if p.onPct != nil && p.total > 0 {
		pct := int(p.written * 100 / p.total)
		if pct > 100 {
			pct = 100
		}
		if pct != p.last {
			p.last = pct
			p.onPct(pct)
		}
	}
	return n, err
}

func DownloadDeb(ctx context.Context, repoURL, packageName, destDir string, onProgress func(int)) (debPath, ver string, err error) {
	repoURL = strings.TrimRight(repoURL, "/")
	idx, err := fetchIndex(ctx, repoURL)
	if err != nil {
		return "", "", err
	}
	info, ok := idx.Packages[packageName]
	if !ok || info.Filename == "" || info.SHA256 == "" || info.Version == "" {
		return "", "", errors.New("package metadata incomplete in updates index")
	}
	if err := os.MkdirAll(destDir, 0700); err != nil {
		return "", "", err
	}
	url := repoURL + "/" + strings.TrimPrefix(info.Filename, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download HTTP %d", resp.StatusCode)
	}
	total := resp.ContentLength
	if total <= 0 && info.Size > 0 {
		total = info.Size
	}
	path := filepath.Join(destDir, filepath.Base(info.Filename))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|os.O_EXCL, 0600)
	if err != nil {
		return "", "", err
	}
	h := sha256.New()
	pw := &progressWriter{w: io.MultiWriter(f, h), total: total, onPct: onProgress}
	if _, err := io.Copy(pw, io.LimitReader(resp.Body, maxDebBytes)); err != nil {
		f.Close()
		os.Remove(path)
		return "", "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", "", err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, info.SHA256) {
		os.Remove(path)
		return "", "", fmt.Errorf("sha256 mismatch: got %s want %s", sum, info.SHA256)
	}
	if onProgress != nil {
		onProgress(100)
	}
	return path, info.Version, nil
}

func ApplyLocal(ctx context.Context, repoURL, packageName, stateDir string) (ApplyResult, error) {
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: 0, Message: "Downloading package"})
	dir, err := os.MkdirTemp("", "nex-update-*")
	if err != nil {
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 0, Error: err.Error()})
		return ApplyResult{Status: "failed", Message: err.Error()}, err
	}
	defer os.RemoveAll(dir)
	deb, _, err := DownloadDeb(ctx, repoURL, packageName, dir, func(pct int) {
		_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: pct, Message: "Downloading package"})
	})
	if err != nil {
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 0, Error: err.Error()})
		return ApplyResult{Status: "failed", Message: err.Error()}, err
	}
	_ = WriteProgress(stateDir, Progress{Phase: "installing", Percent: 90, Message: "Installing package"})
	cmd := Command(ctx, "dpkg", "-i", deb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 90, Error: msg})
		return ApplyResult{Status: "failed", Message: msg}, err
	}
	unit := "nex-core-local.service"
	if packageName == "nex-controller" {
		unit = "nex-controller.service"
	}
	_ = WriteProgress(stateDir, Progress{Phase: "restarting", Percent: 98, Message: "Restarting service"})
	restart := Command(ctx, "systemd-run", "--unit=nex-update-restart", "--no-block", "/bin/systemctl", "restart", unit)
	if out, err := restart.CombinedOutput(); err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 98, Error: "package installed but restart failed: " + msg})
		return ApplyResult{Status: "failed", Message: "package installed but restart failed: " + msg}, err
	}
	_ = WriteProgress(stateDir, Progress{Phase: "done", Percent: 100, Message: unit + " will restart shortly"})
	return ApplyResult{
		Status:    "scheduled",
		Message:   unit + " will restart shortly",
		Operation: "systemd-run restart",
	}, nil
}

// StartApplyLocal runs ApplyLocal in the background once. Concurrent applies are rejected.
func StartApplyLocal(repoURL, packageName, stateDir string) (ApplyResult, error) {
	applyMu.Lock()
	defer applyMu.Unlock()
	if applyRunning {
		return ApplyResult{Status: "failed", Message: "update already in progress"}, errors.New("update already in progress")
	}
	if p, ok := ReadProgress(stateDir); ok && (p.Phase == "downloading" || p.Phase == "installing" || p.Phase == "restarting") {
		return ApplyResult{Status: "failed", Message: "update already in progress"}, errors.New("update already in progress")
	}
	applyRunning = true
	_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: 0, Message: "Starting update"})
	go func() {
		defer func() {
			applyMu.Lock()
			applyRunning = false
			applyMu.Unlock()
		}()
		_, _ = ApplyLocal(context.Background(), repoURL, packageName, stateDir)
	}()
	return ApplyResult{Status: "scheduled", Message: "update started", Operation: "apply-local"}, nil
}

func RequestApply(stateDir, repoURL, packageName string) error {
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: 0, Message: "Update requested"})
	pending := pendingUpdate{
		Package:     packageName,
		RepoURL:     strings.TrimRight(repoURL, "/"),
		RequestedAt: time.Now().UTC().Format(time.RFC3339),
	}
	b, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	path := filepath.Join(stateDir, "pending-update.json")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
