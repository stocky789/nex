// SPDX-License-Identifier: AGPL-3.0-only

// Package nexupdate updates Nex Core.
//
// Two editions share it. The image edition (image.go, trust.go, source.go, platform.go) is the Core's
// own engine: it authenticates signed release metadata, downloads and stages a verity image into the
// inactive A/B slot of every disk, arms the boot entry on activate, and rolls back; installed hosts
// never run apt. The classic edition (this file) applies Nex Core and Nex Controller package updates
// from the signed Nex archive: apt owns download, GPG verification and dependency resolution against
// the archive its own installer configured, and this code only decides when to run apt and reports
// progress to the UI. The classic path stays until it is retired; Edition selects the path.
package nexupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/rootfs"
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
const versionCompareTimeout = 5 * time.Second

type Status struct {
	Package          string `json:"package"`
	InstalledVersion string `json:"installed_version"`
	AvailableVersion string `json:"available_version,omitempty"`
	UpdateAvailable  bool   `json:"update_available"`
	RepoURL          string `json:"repo_url"`
	Channel          string `json:"channel,omitempty"` // the host's own check only: stable, or dev when repo_url is the development subtree
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
var applyWG sync.WaitGroup

// WaitApply blocks until a background apply started by StartApplyLocal has
// finished writing. A caller that is shutting down (or removing the state
// directory) must not leave the apply writing progress behind it.
func WaitApply() { applyWG.Wait() }

func applying() bool {
	applyMu.Lock()
	defer applyMu.Unlock()
	return applyRunning
}

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
	path := UpdatesConfig
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
	if b, err := os.ReadFile(UpdatesConfig); err == nil {
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
	// even though the new package is already active. Only a restarted process can be
	// stuck: while this one is still applying, apt has already reported the new
	// version to Check before the apply has finished its own work.
	if !applying() &&
		(p.Phase == "downloading" || p.Phase == "installing" || p.Phase == "restarting") &&
		!st.UpdateAvailable && st.InstalledVersion != "" && st.AvailableVersion != "" {
		compare, err := CompareVersions(context.Background(), st.InstalledVersion, st.AvailableVersion)
		if err == nil && compare >= 0 {
			p = Progress{Phase: "done", Percent: 100, Message: "Update complete"}
			_ = WriteProgress(stateDir, p)
		}
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

func CompareVersions(ctx context.Context, a, b string) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, versionCompareTimeout)
	defer cancel()
	for _, comparison := range []struct {
		operator string
		result   int
	}{
		{"lt", -1},
		{"gt", 1},
	} {
		err := Command(ctx, "dpkg", "--compare-versions", a, comparison.operator, b).Run()
		if err == nil {
			return comparison.result, nil
		}
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			return 0, err
		}
	}
	return 0, nil
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

// CheckWithCache is for an unprivileged Controller service.  Apt's normal lists
// live under /var/lib/apt and need root, so the Controller keeps only its signed
// package-index cache under its own state directory.  The install still runs as
// root through apply-update and uses the system APT configuration.
func CheckWithCache(ctx context.Context, repoURL, packageName, cacheDir string) (Status, error) {
	repoURL = NormalizeRepoURL(repoURL)
	if repoURL == "" {
		repoURL = DefaultRepoURL()
	}
	repoURL = strings.TrimRight(repoURL, "/")
	sourceDir := filepath.Join(cacheDir, "sources.list.d")
	listsDir := filepath.Join(cacheDir, "lists")
	if err := os.MkdirAll(filepath.Join(listsDir, "partial"), 0700); err != nil {
		return Status{}, err
	}
	content := "Types: deb\nURIs: " + repoURL +
		"\nSuites: nex\nComponents: main\nArchitectures: amd64\nSigned-By: " + aptKeyringPath + "\n"
	if err := os.MkdirAll(sourceDir, 0700); err != nil {
		return Status{}, err
	}
	sourcePath := filepath.Join(sourceDir, "nex.sources")
	changed := true
	if old, err := os.ReadFile(sourcePath); err == nil && string(old) == content {
		changed = false
	}
	if err := os.WriteFile(sourcePath, []byte(content), 0600); err != nil {
		return Status{}, err
	}
	st := Status{Package: packageName, InstalledVersion: InstalledVersion(packageName), RepoURL: repoURL}
	extra := []string{"-o", "Dir::Etc::sourcelist=/dev/null", "-o", "Dir::Etc::sourceparts=" + sourceDir, "-o", "Dir::State::lists=" + listsDir}
	stamp := filepath.Join(cacheDir, "apt-refresh.stamp")
	if changed {
		_ = os.Remove(stamp)
	}
	if fi, err := os.Stat(stamp); err != nil || time.Since(fi.ModTime()) >= indexMaxAge {
		if out, err := apt(ctx, append([]string{"update"}, extra...)...).CombinedOutput(); err != nil {
			st.Error = "could not refresh package index: " + strings.TrimSpace(string(out))
			// Stamp the attempt either way so a second CheckWithCache in the same request
			// (Controller then nex-core-local) does not run the same failing apt-get update
			// again. The stamp ages out after indexMaxAge like a successful refresh.
			_ = os.WriteFile(stamp, nil, 0600)
			return st, nil
		}
		if err := os.WriteFile(stamp, nil, 0600); err != nil {
			return Status{}, err
		}
	}
	cmd := Command(ctx, "apt-cache", append([]string{"policy", packageName}, extra...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		st.Error = "could not read package index"
		return st, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Candidate:")
		if !ok {
			continue
		}
		if candidate := strings.TrimSpace(rest); candidate != "" && candidate != "(none)" {
			st.AvailableVersion = candidate
			compare, err := CompareVersions(ctx, st.InstalledVersion, candidate)
			if err != nil {
				st.Error = "could not compare package versions"
				return st, nil
			}
			st.UpdateAvailable = compare < 0
			return st, nil
		}
	}
	st.Error = "package not available from " + repoURL
	return st, nil
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
		Channel:          RepoChannel(repoURL),
	}
	candidate := AptCandidate(ctx, packageName)
	if candidate == "" {
		st.Error = "package not available from " + repoURL
		return st, nil
	}
	st.AvailableVersion = candidate
	compare, err := CompareVersions(ctx, st.InstalledVersion, candidate)
	if err != nil {
		st.Error = "could not compare package versions"
		return st, nil
	}
	st.UpdateAvailable = compare < 0
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
	// dpkg writes /usr and /boot, which a read-only-root host keeps read-only; the root is
	// writable only until this function returns.
	restoreRoot, err := rootfs.OpenWritable(ctx, func(ctx context.Context, name string, args ...string) ([]byte, error) {
		return Command(ctx, name, args...).CombinedOutput()
	})
	if err != nil {
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 50, Error: err.Error()})
		return ApplyResult{Status: "failed", Message: err.Error()}, err
	}
	defer restoreRoot()
	// Plain install both upgrades a packaged host and first-installs an appliance
	// that only had a binary under /usr/local (apt --only-upgrade skips those).
	out, err := apt(ctx, "install", "-y", packageName).CombinedOutput()
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
	compare, err := CompareVersions(ctx, before, InstalledVersion(packageName))
	if err != nil {
		msg := "could not compare package versions"
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 50, Error: msg})
		return ApplyResult{Status: "failed", Message: msg}, err
	}
	if compare >= 0 {
		msg := "no newer " + packageName + " available from " + repoURL
		_ = WriteProgress(stateDir, Progress{Phase: "failed", Percent: 50, Error: msg})
		return ApplyResult{Status: "failed", Message: msg}, errors.New(msg)
	}
	unit := "nex-core-local.service"
	if packageName == "nex-controller" {
		unit = "nex-controller.service"
	} else {
		installRecommended(ctx, stateDir)
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

// recommendedCorePackages are OpenZFS and the headers DKMS builds it against.
// nex-core-local recommends rather than depends on them: they live in Debian
// contrib, which a host installed before Nex enabled contrib cannot resolve, and
// a hard dependency there would make every in-product update fail permanently on
// exactly those hosts. The package's postinst enables contrib, so the first apply
// after that one can install them here without an operator shell step.
var recommendedCorePackages = []string{"linux-headers-amd64", "zfs-dkms", "zfsutils-linux"}

// packageInstalled reports whether dpkg has the package unpacked and configured.
// InstalledVersion is not usable here: it falls back to this binary's own version
// when dpkg-query reports nothing, so it never reports a package as absent.
func packageInstalled(ctx context.Context, name string) bool {
	out, err := Command(ctx, "dpkg-query", "-W", "-f=${db:Status-Status}", name).Output()
	return err == nil && strings.TrimSpace(string(out)) == "installed"
}

// installRecommended installs the recommended packages that are still missing.
// It is best effort: a host with no route to Debian contrib, or a DKMS build that
// fails on its kernel, loses ZFS support and reports discovery as unavailable,
// which must not fail an update that has already been applied.
func installRecommended(ctx context.Context, stateDir string) {
	var missing []string
	for _, name := range recommendedCorePackages {
		if !packageInstalled(ctx, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return
	}
	_ = WriteProgress(stateDir, Progress{Phase: "installing", Percent: 80, Message: "Installing ZFS support"})
	// The Core package's postinst may have just enabled Debian contrib; the index
	// fetched before the upgrade does not list it yet.
	_, _ = apt(ctx, "update").CombinedOutput()
	_, _ = apt(ctx, append([]string{"install", "-y"}, missing...)...).CombinedOutput()
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
	applyWG.Add(1)
	go func() {
		defer func() {
			applyMu.Lock()
			applyRunning = false
			applyMu.Unlock()
			applyWG.Done()
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
