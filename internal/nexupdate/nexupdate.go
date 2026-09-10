// SPDX-License-Identifier: AGPL-3.0-only

// Package nexupdate applies Nex Core and Nex Controller package updates from the
// signed Nex archive. Nex Core is a distribution rather than a set of add-on
// packages, so apt owns download, GPG verification and dependency resolution
// against the archive its own installer configured; this package only decides
// when to run apt and reports progress to the UI.
package nexupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"nex.local/nex/internal/version"
)

const defaultRepoURL = "https://nexvirtualization.com/apt"
const ProgressFileName = "update-progress.json"

// The archive source and its keyring. nex-core-local's postinst writes the source
// at install time; Check and ApplyLocal rewrite it so a repo URL changed after
// install takes effect without reinstalling the package.
const aptSourcePath = "/etc/apt/sources.list.d/nex.sources"
const aptKeyringPath = "/usr/share/keyrings/nex-archive-keyring.gpg"

// indexMaxAge bounds how often Check refreshes the package index. The UI polls
// Check while an update applies, so refreshing on every call would re-fetch the
// index for nothing and contend with the running install for the archive lock.
const indexMaxAge = 5 * time.Minute

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
	ApplyUpdateBinary = "/usr/local/lib/nex/apply-update"
	UpdatePathUnits   = []string{
		"/usr/lib/systemd/system/nex-controller-update.path",
		"/etc/systemd/system/nex-controller-update.path",
	}
	// AptSourcePath and IndexStampPath are variables so tests can redirect them
	// away from the real system paths.
	AptSourcePath  = aptSourcePath
	IndexStampPath = "/var/lib/nex/apt-refresh.stamp"
)

var applyMu sync.Mutex
var applyRunning bool

func NormalizeRepoURL(repoURL string) string {
	repoURL = strings.TrimRight(strings.TrimSpace(repoURL), "/")
	if repoURL == "" || isPrivateHTTPRepoURL(repoURL) {
		return defaultRepoURL
	}
	return repoURL
}

func isPrivateHTTPRepoURL(repoURL string) bool {
	parsed, err := url.Parse(strings.TrimSpace(repoURL))
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" {
		return false
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}

func rewriteUpdatesConf(repoURL string) {
	path := "/etc/nex/updates.conf"
	content := "repo_url=" + strings.TrimRight(repoURL, "/") + "\n"
	tmp := path + ".tmp"
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	if err := os.WriteFile(tmp, []byte(content), 0644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

func DefaultRepoURL() string {
	if v := strings.TrimSpace(os.Getenv("NEX_UPDATE_REPO")); v != "" {
		return NormalizeRepoURL(v)
	}
	if b, err := os.ReadFile("/etc/nex/updates.conf"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "repo_url=") {
				configured := strings.TrimSpace(strings.TrimPrefix(line, "repo_url="))
				repo := NormalizeRepoURL(configured)
				if isPrivateHTTPRepoURL(configured) {
					rewriteUpdatesConf(repo)
				}
				return repo
			}
		}
	}
	return defaultRepoURL
}

// ControllerApplyReady reports whether the privileged Controllers update helper
// and path unit are present so Update does not hang after scheduling.
func ControllerApplyReady() error {
	if _, err := os.Stat(ApplyUpdateBinary); err != nil {
		return errors.New("update helper is missing; redeploy the Controller appliance or install the nex-controller package")
	}
	for _, path := range UpdatePathUnits {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	return errors.New("update path unit is missing; redeploy the Controller appliance or install the nex-controller package")
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
	// If postinst restarted the service mid-dpkg, progress can stick on installing
	// even though the new package is already active.
	if (p.Phase == "downloading" || p.Phase == "installing" || p.Phase == "restarting") &&
		!st.UpdateAvailable && st.InstalledVersion != "" && st.AvailableVersion != "" &&
		CompareVersions(st.InstalledVersion, st.AvailableVersion) >= 0 {
		p = Progress{Phase: "done", Percent: 100, Message: "Update complete"}
		_ = WriteProgress(stateDir, p)
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

// apt runs apt-get non-interactively, keeping existing config files on upgrade and
// waiting briefly for the archive lock so a concurrent apt-daily run does not fail
// an operator-initiated update. LC_ALL is pinned because callers parse the output.
func apt(ctx context.Context, args ...string) *exec.Cmd {
	cmd := Command(ctx, "apt-get", append([]string{
		"-q",
		"-o", "Dpkg::Options::=--force-confold",
		"-o", "DPkg::Lock::Timeout=60",
	}, args...)...)
	cmd.Env = append(os.Environ(), "DEBIAN_FRONTEND=noninteractive", "LC_ALL=C")
	return cmd
}

// writeAptSource keeps the signed Nex archive source aligned with the configured
// repo URL. nex-core-local's postinst writes the same file at install time; keeping
// it current here makes a repo change take effect without reinstalling the package.
func writeAptSource(repoURL string) {
	content := "Types: deb\nURIs: " + strings.TrimRight(repoURL, "/") +
		"\nSuites: nex\nComponents: main\nArchitectures: amd64\n" +
		"Signed-By: " + aptKeyringPath + "\n"
	if b, err := os.ReadFile(AptSourcePath); err == nil && string(b) == content {
		return
	}
	if os.MkdirAll(filepath.Dir(AptSourcePath), 0755) != nil {
		return
	}
	tmp := AptSourcePath + ".tmp"
	if os.WriteFile(tmp, []byte(content), 0644) != nil {
		return
	}
	_ = os.Rename(tmp, AptSourcePath)
}

// refreshIndex updates the package lists at most once per indexMaxAge. A failure is
// not fatal: apt keeps the previous lists, so a check still reports the last known
// candidate instead of nothing.
func refreshIndex(ctx context.Context) {
	if fi, err := os.Stat(IndexStampPath); err == nil && time.Since(fi.ModTime()) < indexMaxAge {
		return
	}
	if _, err := apt(ctx, "update").CombinedOutput(); err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(IndexStampPath), 0755) != nil {
		return
	}
	if f, err := os.OpenFile(IndexStampPath, os.O_CREATE|os.O_WRONLY, 0644); err == nil {
		_ = f.Close()
	}
	now := time.Now()
	_ = os.Chtimes(IndexStampPath, now, now)
}

// AptCandidate reports the version apt would install for packageName, or "" when no
// configured source offers it.
func AptCandidate(ctx context.Context, packageName string) string {
	cmd := Command(ctx, "apt-cache", "policy", packageName)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:")
		if !ok {
			continue
		}
		if v := strings.TrimSpace(rest); v != "" && v != "(none)" {
			return v
		}
		return ""
	}
	return ""
}

func Check(ctx context.Context, repoURL, packageName string) (Status, error) {
	repoURL = NormalizeRepoURL(repoURL)
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	repoURL = strings.TrimRight(repoURL, "/")
	writeAptSource(repoURL)
	refreshIndex(ctx)
	st := Status{
		Package:          packageName,
		InstalledVersion: InstalledVersion(packageName),
		RepoURL:          repoURL,
	}
	candidate := AptCandidate(ctx, packageName)
	if candidate == "" {
		st.Error = "package not available from " + repoURL
		return st, nil
	}
	st.AvailableVersion = candidate
	st.UpdateAvailable = CompareVersions(st.InstalledVersion, candidate) < 0
	return st, nil
}

// ApplyLocal upgrades packageName from the signed Nex archive. apt resolves the
// package's dependencies against the host's configured sources, so a release that
// adds a dependency installs cleanly instead of leaving dpkg half-configured.
//
// ponytail: progress is reported in fixed steps. apt can emit machine-readable
// download progress via -o APT::Status-Fd=N if a real percentage is wanted back.
func ApplyLocal(ctx context.Context, repoURL, packageName, stateDir string) (ApplyResult, error) {
	repoURL = NormalizeRepoURL(repoURL)
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	writeAptSource(repoURL)
	before := InstalledVersion(packageName)

	_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: 10, Message: "Refreshing package index"})
	// A partially failing refresh still leaves usable lists; the install below is
	// the authority on whether a newer version was actually available.
	_, _ = apt(ctx, "update").CombinedOutput()

	_ = WriteProgress(stateDir, Progress{Phase: "installing", Percent: 50, Message: "Installing package"})
	out, err := apt(ctx, "install", "-y", "--only-upgrade", packageName).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 50, Error: msg})
		return ApplyResult{Status: "failed", Message: msg}, err
	}
	// apt exits 0 when the installed version is already the candidate, so compare
	// rather than trusting the exit status to mean an upgrade happened.
	if CompareVersions(before, InstalledVersion(packageName)) >= 0 {
		msg := "no newer " + packageName + " available from " + repoURL
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 50, Error: msg})
		return ApplyResult{Status: "failed", Message: msg}, errors.New(msg)
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
	repoURL = NormalizeRepoURL(repoURL)
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return err
	}
	_ = WriteProgress(stateDir, Progress{Phase: "downloading", Percent: 0, Message: "Update requested"})
	pending := pendingUpdate{
		Package:     packageName,
		RepoURL:     repoURL,
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
