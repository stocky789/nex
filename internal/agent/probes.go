// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Probe func(context.Context) (any, error)

type ProbeResult struct {
	Name, Status, Message   string
	ObservedAt, LastSuccess time.Time
	Data                    any
}

// publicProbeError is an intentionally operator-safe probe reason. Probe errors otherwise
// stay local: OS and command errors may disclose private paths, endpoints or credentials.
type publicProbeError string

func (e publicProbeError) Error() string { return string(e) }

type probeWorker struct {
	probe    Probe
	busy     bool
	deadline time.Time
	started  time.Time
	cancel   context.CancelFunc
	result   ProbeResult
}

// Monitor retains at most one worker per probe, including probes that ignore
// cancellation. A stuck filesystem call cannot consume workers on later ticks.
type Monitor struct {
	mu      sync.Mutex
	timeout time.Duration
	workers map[string]*probeWorker
}

func NewMonitor(timeout time.Duration, probes map[string]Probe) *Monitor {
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	m := &Monitor{timeout: timeout, workers: make(map[string]*probeWorker)}
	for name, p := range probes {
		m.workers[name] = &probeWorker{probe: p, result: ProbeResult{Name: name, Status: "unknown", Message: "not yet observed"}}
	}
	return m
}

// Tick never waits for a probe. Call regularly independently of heartbeat I/O.
func (m *Monitor) Tick(ctx context.Context) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for _, w := range m.workers {
		if w.busy {
			if !now.Before(w.deadline) {
				w.cancel()
				w.result.Status, w.result.Message = "unknown", "probe deadline exceeded; previous observation retained"
			}
			continue
		}
		if ctx.Err() != nil {
			continue
		}
		w.busy = true
		w.started = now
		timeout := m.timeout
		if w.result.Name == "cpu-baseline" {
			timeout = max(timeout, 30*time.Second)
		}
		w.deadline = now.Add(timeout)
		probeCtx, cancel := context.WithDeadline(ctx, w.deadline)
		w.cancel = cancel
		go m.run(probeCtx, w)
	}
}

func (m *Monitor) run(ctx context.Context, w *probeWorker) {
	data, err := w.probe(ctx)
	m.mu.Lock()
	defer m.mu.Unlock()
	defer w.cancel()
	w.busy = false
	// Discard late observations even if an uncooperative probe reports success.
	if ctx.Err() != nil || !time.Now().Before(w.deadline) {
		w.result.Status, w.result.Message = "unknown", "probe deadline exceeded; previous observation retained"
		return
	}
	// Timestamp sampling start so a pre-command in-flight probe cannot
	// satisfy a request for fresh post-command inventory.
	w.result.ObservedAt = w.started
	if err != nil {
		w.result.Status, w.result.Message = "failed", probeReason(w.result.Name, err)
		if errors.Is(err, os.ErrNotExist) || errors.Is(err, exec.ErrNotFound) {
			w.result.Status = "unsupported"
		}
		return
	}
	w.result.Data, w.result.Status, w.result.Message = data, "healthy", ""
	w.result.LastSuccess = w.result.ObservedAt
}

func probeReason(name string, err error) string {
	var safe publicProbeError
	if errors.As(err, &safe) {
		return safe.Error()
	}
	switch name {
	case "inventory":
		return "host inventory observation failed"
	case "cpu-models":
		return "CPU model observation failed"
	case "block-disks":
		return "block device observation failed"
	case "disk":
		return "disk capacity observation failed"
	case "kvm":
		return "KVM is unavailable"
	case "guest":
		return "guest agent observation failed"
	case "usage":
		return "VM usage observation failed"
	case "libvirt":
		return "local libvirt is unavailable"
	default:
		return "probe observation failed"
	}
}

func (m *Monitor) Results() []ProbeResult {
	m.mu.Lock()
	defer m.mu.Unlock()
	results := make([]ProbeResult, 0, len(m.workers))
	for _, w := range m.workers {
		if w.busy && !time.Now().Before(w.deadline) {
			w.cancel()
			w.result.Status, w.result.Message = "unknown", "probe deadline exceeded; previous observation retained"
		}
		results = append(results, w.result)
	}
	sort.Slice(results, func(i, j int) bool { return results[i].Name < results[j].Name })
	return results
}

type NativeInventory struct {
	Hostname, OS, AgentVersion, Architecture, CPUModel string
	CPUs                                               int
	MemoryBytes                                        uint64
	CPUFlags                                           []string
	NICs                                               []NativeNIC
}

type NativeNIC struct {
	Name, MAC, State, Kind, Master, Parent string
	Addresses                              []string
}

// nicTopology reports the bridge/bond a device is enslaved to and, for VLAN devices, the
// raw device they sit on, so clients can present networks instead of plumbing.
func nicTopology(name string) (master, parent string) {
	if target, err := os.Readlink("/sys/class/net/" + name + "/master"); err == nil {
		master = filepath.Base(target)
	}
	if b, err := os.ReadFile("/proc/net/vlan/" + name); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(line, "Device:") {
				parent = strings.TrimSpace(strings.TrimPrefix(line, "Device:"))
			}
		}
	}
	return master, parent
}

// nicKind classifies a host interface for uplink selection: physical NICs and bonds
// can carry guest VLANs; bridges, VLAN devices, loopback and taps cannot.
func nicKind(name string, flags net.Flags) string {
	sys := "/sys/class/net/" + name
	switch {
	case flags&net.FlagLoopback != 0:
		return "loopback"
	case exists(sys + "/bridge"):
		return "bridge"
	case exists(sys + "/bonding"):
		return "bond"
	case exists("/proc/net/vlan/" + name):
		return "vlan"
	case exists(sys + "/device"):
		return "physical"
	}
	return "virtual"
}
func exists(path string) bool { _, err := os.Stat(path); return err == nil }

type DiskCapacity struct {
	Path                       string
	TotalBytes, AvailableBytes uint64
}

// NativeProbes only observes the local machine; it never changes VM or host state.
// usage may be nil, which omits the usage probe.
func NativeProbes(version string, usage *usageSampler) map[string]Probe {
	probes := map[string]Probe{
		"inventory": func(ctx context.Context) (any, error) { return readInventory(version) },
		"cpu-models": func(ctx context.Context) (any, error) {
			// Domain capabilities mark which named models can actually run here;
			// virsh cpu-models lists every QEMU name, including unusable baselines.
			caps, err := virsh(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", "q35")
			if err != nil {
				return nil, err
			}
			return vmhost.UsableCPUModels([]byte(caps))
		},
		"block-disks": func(ctx context.Context) (any, error) { return readBlockDisks(ctx, "/sys/block") },
		"disk": func(ctx context.Context) (any, error) {
			var stat syscall.Statfs_t
			if err := syscall.Statfs("/", &stat); err != nil {
				return nil, err
			}
			return DiskCapacity{"/", stat.Blocks * uint64(stat.Bsize), stat.Bavail * uint64(stat.Bsize)}, nil
		},
		"kvm": func(ctx context.Context) (any, error) {
			f, err := os.OpenFile("/dev/kvm", os.O_RDWR, 0)
			if err != nil {
				return nil, err
			}
			return nil, f.Close()
		},
		"guest": guestProbe(),
		"libvirt": func(ctx context.Context) (any, error) {
			ids, err := virsh(ctx, "list", "--all", "--uuid")
			if err != nil {
				return nil, err
			}
			names := strings.Fields(ids)
			if len(names) > 256 {
				return nil, publicProbeError("VM observation limit exceeded (256)")
			}
			vms := make([]protocol.VM, 0, len(names))
			for _, id := range names {
				// domstate/domname answer from the domain object only; dominfo can block on
				// libvirt 11 (Debian 13) querying the security model, stalling the probe.
				state, e := virsh(ctx, "domstate", id)
				if e != nil {
					return nil, e
				}
				name, e := virsh(ctx, "domname", id)
				if e != nil {
					return nil, e
				}
				vm := protocol.VM{ID: id, Name: strings.TrimSpace(name), State: strings.TrimSpace(state)}
				if vm.State == "" {
					vm.State = "unknown"
				}
				vms = append(vms, vm)
			}
			return vms, nil
		},
	}
	if usage != nil {
		probes["usage"] = usageProbe(usage)
	}
	return probes
}

// Guest facts are refreshed per running VM once a minute while the agent answers, and
// every ten seconds while its channel is attached but unanswered; channel states are
// read from libvirt on every probe. Agent questions run after every VM was observed,
// oldest cache first, and stop when the probe deadline is closer than one question could
// take, so a hung guest delays other guests' facts by a tick instead of losing them.
const (
	guestFactsTTL    = time.Minute
	guestFactsRetry  = 10 * time.Second
	guestAgentBudget = 1500 * time.Millisecond
)

type guestObservation struct {
	VMID  string
	Guest *protocol.GuestInfo
}

// guestProbe observes the console channels and guest agent of every running VM. It is
// separate from the libvirt probe so a slow guest never hides VM power states.
func guestProbe() Probe {
	// ponytail: facts are cached per VM in memory; a VM that leaves the running list is dropped.
	type cached struct {
		at    time.Time
		facts *protocol.GuestInfo
	}
	cache := map[string]cached{}
	return func(ctx context.Context) (any, error) {
		ids, err := virsh(ctx, "list", "--uuid")
		if err != nil {
			return nil, err
		}
		running := strings.Fields(ids)
		if len(running) > 256 {
			return nil, publicProbeError("VM observation limit exceeded (256)")
		}
		seen := map[string]bool{}
		out := make([]guestObservation, 0, len(running))
		var due []int // indexes into out whose agent should be asked this tick
		for _, id := range running {
			seen[id] = true
			g, err := vmhost.ObserveGuest(ctx, id)
			if err != nil {
				if ctx.Err() != nil {
					return nil, err
				}
				continue // the VM stopped between list and dumpxml; the libvirt probe reports its state
			}
			out = append(out, guestObservation{id, g})
			if g.Agent != "unresponsive" { // channel not attached: nothing to ask
				delete(cache, id)
				continue
			}
			c, ok := cache[id]
			ttl := guestFactsTTL
			if !ok || c.facts.Agent != "connected" {
				ttl = guestFactsRetry
			}
			if ok && time.Since(c.at) < ttl {
				if c.facts.Agent == "connected" {
					g.Agent, g.OS, g.Hostname, g.Addresses = c.facts.Agent, c.facts.OS, c.facts.Hostname, c.facts.Addresses
				}
				continue
			}
			due = append(due, len(out)-1)
		}
		sort.SliceStable(due, func(i, j int) bool { return cache[out[due[i]].VMID].at.Before(cache[out[due[j]].VMID].at) })
		for _, i := range due {
			if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < guestAgentBudget {
				break // asked on a later tick; the VM stays unresponsive until then
			}
			vmhost.GuestFacts(ctx, out[i].VMID, out[i].Guest)
			cache[out[i].VMID] = cached{time.Now(), out[i].Guest}
		}
		for id := range cache {
			if !seen[id] {
				delete(cache, id)
			}
		}
		return out, nil
	}
}

// Usage is sampled for every running VM with `domstats --raw --nowait` by the usage history
// sampler (usage_history_sample.go). --nowait is the whole reason that sampling is safe.
// Without it libvirt waits for the domain's job before reading anything that needs the QEMU
// monitor, and that job is held for the entire length of the operation in progress: a Windows
// guest held one shutdown for over three minutes while Nex's own polls logged "cannot acquire
// state change lock" (BUG-0213). With it, libvirt returns the counters it can read without the
// job — CPU time, balloon allocation, vCPU count — always, and the ones behind the monitor
// (host RSS, block sizes) only when no other job holds the domain. That is why those members
// of protocol.VMUsage are pointers: absent means libvirt was busy, which is not zero. dominfo
// is avoided here for the same reason the libvirt probe avoids it.
//
// The usage probe only reports the sampler's latest values, so a slow sample never hides VM
// power states, exactly as the guest probe is separate. The sampler keeps the previous raw CPU
// counter per VM because a rate needs two samples, and Core reports the rate rather than the
// counter: a Controller that divided two counters itself would be deriving pool state the host
// already knows.
//
// A failure to sample one VM is skipped, never reported as a probe failure: the web UI ranks
// any unhealthy check as a host problem, and a cosmetic counter libvirt declined to answer
// must not paint the host red. The probe fails only when the sampler cannot list active
// domains, which is the same condition the libvirt probe already fails on.
const (
	// A sample this old is dropped rather than re-reported, and is never used as the base of
	// a rate. It bounds how wrong a wedged sampler can make the display.
	usageMaxAge = 2 * time.Minute
	// Above this the arithmetic is reporting a broken counter, not a busy VM.
	usageMaxPercent = 1000
	// A denominator beyond this is a misparse, not a machine. It also keeps delta*100 inside
	// uint64 for every value that survives the clamp below.
	usageMaxVCPUs = 1024
)

type usageObservation struct {
	VMID  string
	Usage *protocol.VMUsage
}

// usageSample is the previous raw counter (for the next rate) and the last usage reported for
// this VM (so a VM libvirt could not answer for re-reports it unchanged instead of disappearing).
type usageSample struct {
	at    time.Time
	cpuNS uint64
	usage protocol.VMUsage
}

// usageProbe reports the sampler's latest CPU, memory and disk consumption of every active VM.
func usageProbe(sampler *usageSampler) Probe {
	return func(ctx context.Context) (any, error) { return sampler.observations() }
}

// usageRate turns two cumulative libvirt CPU counters into a whole-percent rate of this VM's
// own vCPUs. It refuses rather than guesses: a counter that went backwards is a domain that
// restarted between samples, and a window that is not positive, or older than usageMaxAge, is
// not a rate anyone should read. Refusing costs one probe of "measuring"; guessing publishes a
// number an operator would act on. The window is floored at 1ms so a rate is never reported
// with a window truncated to zero — the Controller rejects that pairing, and rejecting it
// would discard the whole host report.
func usageRate(previous usageSample, at time.Time, cpuNS, vcpus uint64) (uint16, uint32, bool) {
	window := at.Sub(previous.at)
	if window <= 0 || window > usageMaxAge || vcpus == 0 || vcpus > usageMaxVCPUs || cpuNS < previous.cpuNS {
		return 0, 0, false
	}
	ms := window.Milliseconds()
	if ms < 1 {
		ms = 1
	}
	delta := cpuNS - previous.cpuNS
	denominator := uint64(window.Nanoseconds()) * vcpus
	// ponytail: compare before multiplying. delta*100 wraps uint64 once delta passes ~1.8e17ns,
	// and a wrapped product can land on a plausible small number instead of an obvious one.
	if delta/100 > denominator/10 {
		return usageMaxPercent, uint32(ms), true
	}
	percent := (delta*100 + denominator/2) / denominator
	if percent > usageMaxPercent {
		percent = usageMaxPercent
	}
	return uint16(percent), uint32(ms), true
}

type domainStats struct {
	cpuNS             uint64
	vcpus             uint64
	balloonCurrentKiB uint64
	balloonMaximumKiB uint64
	balloonRSSKiB     *uint64
	diskUsedBytes     *uint64
	diskCapacityBytes *uint64
}

// parseDomainStats reads `virsh domstats` key=value output for one VM, reporting false when the
// sample is worthless. Only this VM's own disk images are counted: Nex writes every VM disk
// into a directory named after the VM — its nex-vms/<id>/ directory, or the appliance's
// <storage>/<id>/ — while ISOs live in the pool's nex-media directory, so the parent directory
// name separates a VM's disks from a mounted installer without a second libvirt call. An image
// outside that layout is left uncounted rather than guessed at. A missing value is left
// unknown; only a missing CPU counter makes the whole sample worthless, because without it
// there is no rate on the next tick either.
func parseDomainStats(vmID, out string) (domainStats, bool) {
	fields := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			fields[key] = value
		}
	}
	number := func(key string) (uint64, bool) {
		v, err := strconv.ParseUint(fields[key], 10, 64)
		return v, err == nil
	}
	var s domainStats
	var ok bool
	if s.cpuNS, ok = number("cpu.time"); !ok {
		return s, false
	}
	s.vcpus, _ = number("vcpu.current")
	s.balloonCurrentKiB, _ = number("balloon.current")
	s.balloonMaximumKiB, _ = number("balloon.maximum")
	if rss, present := number("balloon.rss"); present {
		s.balloonRSSKiB = &rss
	}
	count, _ := number("block.count")
	if count > 64 {
		count = 64
	}
	var used, capacity uint64
	counted := false
	for i := uint64(0); i < count; i++ {
		prefix := "block." + strconv.FormatUint(i, 10) + "."
		path := fields[prefix+"path"]
		if path == "" || !strings.EqualFold(filepath.Base(filepath.Dir(path)), vmID) {
			continue // a CD drive, or an image outside the layout Nex owns
		}
		physical, havePhysical := number(prefix + "physical")
		size, haveCapacity := number(prefix + "capacity")
		if !havePhysical || !haveCapacity {
			continue // libvirt answered this domain without the monitor
		}
		used, capacity, counted = used+physical, capacity+size, true
	}
	if counted {
		s.diskUsedBytes, s.diskCapacityBytes = &used, &capacity
	}
	return s, true
}

func kiBToBytes(v *uint64) *uint64 {
	if v == nil {
		return nil
	}
	bytes := *v * 1024
	return &bytes
}

func readInventory(version string) (NativeInventory, error) {
	v := NativeInventory{AgentVersion: version, Architecture: runtime.GOARCH, CPUs: runtime.NumCPU()}
	var err error
	v.Hostname, err = os.Hostname()
	if err != nil {
		return v, err
	}
	cpu, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return v, err
	}
	for _, line := range strings.Split(string(cpu), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "model name":
			if v.CPUModel == "" {
				v.CPUModel = strings.TrimSpace(value)
			}
		case "flags", "Features":
			if v.CPUFlags == nil {
				v.CPUFlags = strings.Fields(value)
			}
		}
	}
	mem, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return v, err
	}
	for _, line := range strings.Split(string(mem), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "MemTotal:" {
			kb, e := strconv.ParseUint(fields[1], 10, 64)
			if e != nil {
				return v, e
			}
			v.MemoryBytes = kb * 1024
		}
	}
	osRelease, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return v, err
	}
	for _, line := range strings.Split(string(osRelease), "\n") {
		if strings.HasPrefix(line, "PRETTY_NAME=") {
			v.OS = strings.Trim(strings.TrimPrefix(line, "PRETTY_NAME="), "\"'")
		}
	}
	v.NICs, err = readNICs()
	return v, err
}

func cpuModels(output string) ([]string, error) {
	seen := map[string]bool{}
	for _, model := range strings.Fields(output) {
		if !protocol.ValidCPUModel(model) {
			return nil, publicProbeError("invalid CPU model from libvirt")
		}
		seen[model] = true
	}
	if len(seen) > 256 {
		return nil, publicProbeError("CPU model limit exceeded")
	}
	models := make([]string, 0, len(seen))
	for model := range seen {
		models = append(models, model)
	}
	sort.Strings(models)
	return models, nil
}

// readNICs observes every host interface with its kind, topology, state and addresses.
func readNICs() ([]NativeNIC, error) {
	nics, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []NativeNIC
	for _, nic := range nics {
		n := NativeNIC{Name: nic.Name, MAC: nic.HardwareAddr.String(), State: "unknown", Kind: nicKind(nic.Name, nic.Flags)}
		n.Master, n.Parent = nicTopology(nic.Name)
		if state, e := os.ReadFile("/sys/class/net/" + nic.Name + "/operstate"); e == nil {
			n.State = strings.TrimSpace(string(state))
		}
		addresses, e := nic.Addrs()
		if e != nil {
			return nil, e
		}
		for _, address := range addresses {
			n.Addresses = append(n.Addresses, address.String())
		}
		out = append(out, n)
	}
	return out, nil
}

// reported is the NIC as the Controller and the managed engine see it.
func (n NativeNIC) reported() protocol.NIC {
	return protocol.NIC{Name: n.Name, MAC: n.MAC, Kind: n.Kind, Master: n.Master, Parent: n.Parent, State: n.State, Addresses: append([]string(nil), n.Addresses...)}
}

// HostNICs is the same interface observation the inventory probe reports, for callers that
// need the host's bridges and their physical ports outside the probe cycle.
func HostNICs() ([]protocol.NIC, error) {
	nics, err := readNICs()
	if err != nil {
		return nil, err
	}
	out := make([]protocol.NIC, len(nics))
	for i, n := range nics {
		out[i] = n.reported()
	}
	return out, nil
}

// cappedOutput bounds subprocess memory as well as its execution time.
type cappedOutput struct{ bytes.Buffer }

func (b *cappedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1024*1024 {
		return 0, publicProbeError("libvirt response exceeded 1 MiB")
	}
	return b.Buffer.Write(p)
}
func virsh(ctx context.Context, args ...string) (string, error) {
	// libvirt rejects virConnectGetDomainCapabilities on a readonly connection,
	// even though virsh domcapabilities only observes host capabilities. Keep
	// every other inventory query readonly.
	prefix := []string{"--readonly", "--connect", "qemu:///system"}
	if len(args) > 0 && args[0] == "domcapabilities" {
		prefix = []string{"--connect", "qemu:///system"}
	}
	cmd := exec.CommandContext(ctx, "virsh", append(prefix, args...)...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var output cappedOutput
	cmd.Stdout = &output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("libvirt query: %w", err)
	}
	return output.String(), nil
}

func (m *Monitor) Snapshot() *protocol.Inventory {
	out := &protocol.Inventory{}
	var guests []guestObservation
	var usage []usageObservation
	for _, r := range m.Results() {
		check := protocol.Check{Name: r.Name, Status: r.Status, ObservedAt: r.ObservedAt, Error: r.Message}
		if !r.LastSuccess.IsZero() {
			t := r.LastSuccess
			check.LastSuccess = &t
		}
		out.Checks = append(out.Checks, check)
		switch data := r.Data.(type) {
		case NativeInventory:
			out.ObservedAt = r.LastSuccess
			out.Hostname = data.Hostname
			out.CPUModel = data.CPUModel
			out.CPUCount = data.CPUs
			out.MemoryBytes = data.MemoryBytes
			for _, flag := range data.CPUFlags {
				switch flag {
				case "vmx", "svm", "tpr_shadow", "vnmi", "flexpriority", "ept", "ept_ad", "vpid",
					"npt", "lbrv", "svm_lock", "nrip_save", "tsc_scale", "vmcb_clean", "flushbyasid",
					"decodeassists", "pausefilter", "pfthreshold", "avic", "v_vmsave_vmload", "vgif", "x2avic", "v_spec_ctrl":
					out.Virtualization = append(out.Virtualization, flag)
				}
			}
			out.OSVersion = data.OS
			out.AgentVersion = data.AgentVersion
			for _, n := range data.NICs {
				out.NICs = append(out.NICs, n.reported())
			}
		case DiskCapacity:
			out.Disks = append(out.Disks, protocol.Disk{Name: data.Path, SizeBytes: data.TotalBytes, AvailableBytes: &data.AvailableBytes})
		case []protocol.Disk:
			out.Disks = append(out.Disks, data...)
		case []protocol.VM:
			out.VMs = append([]protocol.VM(nil), data...)
		case []string:
			if r.Status == "healthy" {
				out.CPUModels = append([]string(nil), data...)
			}
		case protocol.CPUBaseline:
			if r.Status == "healthy" {
				copy := data
				copy.Features = append([]protocol.CPUFeature(nil), data.Features...)
				out.CPUBaseline = &copy
			}
		case []guestObservation:
			// Retained data from a failed or overdue guest probe is not attached: facts must be current.
			if r.Status == "healthy" {
				guests = data
			}
		case []usageObservation:
			// Retained data from a failed or overdue usage probe is not attached: a rate
			// must describe the window it claims.
			if r.Status == "healthy" {
				usage = data
			}
		}
	}
	// Guest facts and usage describe running VMs only; a VM the libvirt probe no longer sees
	// running keeps neither.
	for i := range out.VMs {
		for _, g := range guests {
			if strings.EqualFold(g.VMID, out.VMs[i].ID) && out.VMs[i].State == "running" {
				out.VMs[i].Guest = g.Guest
			}
		}
		for _, u := range usage {
			if strings.EqualFold(u.VMID, out.VMs[i].ID) && out.VMs[i].State == "running" {
				out.VMs[i].Usage = u.Usage
			}
		}
	}
	return out
}

func readBlockDisks(ctx context.Context, root string) ([]protocol.Disk, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	if len(entries) > 256 {
		return nil, publicProbeError("block device observation limit exceeded (256)")
	}
	disks := make([]protocol.Disk, 0, len(entries))
	for _, entry := range entries {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		size, e := os.ReadFile(root + "/" + entry.Name() + "/size")
		if e != nil {
			return nil, e
		}
		sectors, e := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
		if e != nil {
			return nil, e
		}
		if sectors > ^uint64(0)/512 {
			return nil, publicProbeError("block device size overflow")
		}
		disks = append(disks, protocol.Disk{Name: "/dev/" + entry.Name(), SizeBytes: sectors * 512})
	}
	return disks, nil
}

// WatchLibvirt supplements periodic reconciliation with read-only libvirt events.
// Missing virsh or interrupted event streams leave periodic probes authoritative.
func (m *Monitor) WatchLibvirt(ctx context.Context) {
	for ctx.Err() == nil {
		cmd := exec.CommandContext(ctx, "virsh", "--readonly", "--connect", "qemu:///system", "event", "--all", "--loop")
		cmd.WaitDelay = time.Second
		cmd.Stderr = io.Discard
		cmd.Stdout = &eventOutput{ctx: ctx, monitor: m}
		_ = cmd.Run()
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Event payloads are discarded, so neither a long line nor an event storm grows
// memory. Reconciliation is coalesced to at most once per second.
type eventOutput struct {
	ctx     context.Context
	monitor *Monitor
	last    time.Time
}

func (w *eventOutput) Write(p []byte) (int, error) {
	if bytes.IndexByte(p, '\n') >= 0 && time.Since(w.last) >= time.Second {
		w.last = time.Now()
		w.monitor.Tick(w.ctx)
	}
	return len(p), nil
}
