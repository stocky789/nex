// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"math"
	"net"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The sampler observes every running VM and the host every 5 s, independently of the health
// probes, and is the only place usage is read: the usage probe reports its latest VM values.
// VMs are read with one `virsh domstats --nowait` call for all running domains (see the usage
// comment in probes.go for why --nowait is mandatory), plus `virsh list` to map domain names to
// UUIDs, because domstats prints only names.
const (
	usageSampleInterval = 5 * time.Second
	usageSampleDeadline = 4 * time.Second
	usageFlushInterval  = time.Minute
	// Per-vCPU and per-core series are capped so a large host or VM cannot fill the file's
	// series cap before memory, network, disk and load are recorded.
	usageMaxCPUSeries = 64
)

// counters holds cumulative raw values from one sample, for rates on the next.
type counters struct {
	at     time.Time
	values map[string]float64
}

// rate is the per-second change of a counter between two samples, or NaN when either sample
// lacks it, the window is not positive, or the counter went backwards (a restart).
func (c counters) rate(previous counters, key string, scale float64) float64 {
	cur, ok := c.values[key]
	old, had := previous.values[key]
	window := c.at.Sub(previous.at).Seconds()
	if !ok || !had || window <= 0 || cur < old || c.at.Sub(previous.at) > usageMaxAge {
		return math.NaN()
	}
	return (cur - old) / window * scale
}

type usageSampler struct {
	history *usageHistory
	virsh   func(context.Context, ...string) (string, error) // replaced by tests
	mu      sync.Mutex
	latest  map[string]usageSample // VM UUID → last usage reported by the probe
	listErr error                  // last failure to list domains; domstats failures are not one
	vms     map[string]counters
	host    counters
}

func newUsageSampler(history *usageHistory) *usageSampler {
	u := &usageSampler{history: history, virsh: virsh, latest: map[string]usageSample{}, vms: map[string]counters{}}
	history.live = newLiveViews(u)
	return u
}

// run samples on wall-clock 5 s boundaries until ctx ends, then saves history once more.
// Aligned ticks put one sample in each 5 s step; a free-running ticker would drift across
// step edges and leave some steps empty.
func (u *usageSampler) run(ctx context.Context) {
	u.history.live.mu.Lock()
	u.history.live.ctx = ctx
	u.history.live.mu.Unlock()
	lastFlush := time.Now()
	for {
		work, cancel := context.WithTimeout(ctx, usageSampleDeadline)
		u.sampleVMs(work)
		cancel()
		u.sampleHost()
		if time.Since(lastFlush) >= usageFlushInterval {
			u.history.flush(time.Now())
			lastFlush = time.Now()
		}
		timer := time.NewTimer(time.Until(time.Now().Truncate(usageSampleInterval).Add(usageSampleInterval)))
		select {
		case <-ctx.Done():
			timer.Stop()
			u.history.flush(time.Now())
			return
		case <-timer.C:
		}
	}
}

func (u *usageSampler) sampleVMs(ctx context.Context) {
	list, err := u.virsh(ctx, "list", "--uuid", "--name")
	if err != nil {
		u.mu.Lock()
		u.listErr = err
		u.mu.Unlock()
		return
	}
	// One timestamp, taken before the call, for every domain: the counters describe the moment
	// libvirt was asked, however long virsh then takes to print them.
	at := time.Now()
	text, err := u.virsh(ctx, "domstats", "--raw", "--nowait", "--list-running", "--cpu-total", "--vcpu", "--balloon", "--block", "--interface")
	u.mu.Lock()
	defer u.mu.Unlock()
	u.listErr = nil
	if err != nil {
		return // latest samples keep being reported until usageMaxAge
	}
	ids := map[string]string{}
	for _, line := range strings.Split(list, "\n") {
		if id, name, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			ids[strings.TrimSpace(name)] = strings.ToLower(id)
		}
	}
	seen := map[string]bool{}
	for name, block := range splitDomainStats(text) {
		id := ids[name]
		if id == "" {
			continue // started after the list; sampled next tick
		}
		seen[id] = true
		stats, ok := parseDomainStats(id, block)
		if !ok {
			continue // the previous usage stays until usageMaxAge
		}
		usage := protocol.VMUsage{
			ObservedAt:          at.UTC(),
			MemoryAssignedBytes: stats.balloonCurrentKiB * 1024,
			MemoryMaximumBytes:  stats.balloonMaximumKiB * 1024,
			MemoryHostBytes:     kiBToBytes(stats.balloonRSSKiB),
			DiskUsedBytes:       stats.diskUsedBytes,
			DiskCapacityBytes:   stats.diskCapacityBytes,
		}
		if previous, known := u.latest[id]; known {
			if percent, window, ok := usageRate(previous, at, stats.cpuNS, stats.vcpus); ok {
				usage.CPUPercent, usage.SampleWindowMS = &percent, window
			}
		}
		u.latest[id] = usageSample{at: at, cpuNS: stats.cpuNS, usage: usage}
		current, points := vmHistoryPoints(id, block, at, u.vms[id])
		u.vms[id] = current
		u.history.record("vm-"+id, at, points)
	}
	for id := range u.latest {
		if !seen[id] && time.Since(u.latest[id].at) > usageMaxAge {
			delete(u.latest, id)
		}
	}
	for id := range u.vms {
		if !seen[id] {
			delete(u.vms, id) // a stopped VM leaves a gap, and its next start begins a new rate
		}
	}
}

// observations is what the usage probe reports: the latest sample of every VM still recent.
func (u *usageSampler) observations() ([]usageObservation, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.listErr != nil {
		return nil, u.listErr
	}
	out := make([]usageObservation, 0, len(u.latest))
	for id, s := range u.latest {
		if time.Since(s.at) <= usageMaxAge {
			usage := s.usage
			out = append(out, usageObservation{id, &usage})
		}
	}
	return out, nil
}

// splitDomainStats splits multi-domain `domstats --raw` output into each domain's key=value
// lines, keyed by domain name.
func splitDomainStats(out string) map[string]string {
	blocks := map[string]string{}
	name := ""
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if rest, ok := strings.CutPrefix(line, "Domain: '"); ok {
			name = strings.TrimSuffix(rest, "'")
			blocks[name] = ""
			continue
		}
		if name != "" && line != "" {
			blocks[name] += line + "\n"
		}
	}
	return blocks
}

func domainFields(block string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(block, "\n") {
		if key, value, ok := strings.Cut(line, "="); ok {
			fields[key] = value
		}
	}
	return fields
}

// vmHistoryPoints turns one domain's stats into history points. Counters become rates against
// the previous sample; a counter with no usable previous value is unknown, never zero.
func vmHistoryPoints(vmID, block string, at time.Time, previous counters) (counters, []historyPoint) {
	fields := domainFields(block)
	num := func(key string) (float64, bool) {
		v, err := strconv.ParseUint(fields[key], 10, 64)
		return float64(v), err == nil
	}
	current := counters{at: at, values: map[string]float64{}}
	var points []historyPoint
	gauge := func(key, group, label, unit string, v float64, ok bool) {
		if !ok {
			v = math.NaN()
		}
		points = append(points, historyPoint{seriesMeta{key, group, label, unit}, v})
	}
	counter := func(key, group, label, unit, field string, scale float64) {
		if v, ok := num(field); ok {
			current.values[key] = v
		}
		points = append(points, historyPoint{seriesMeta{key, group, label, unit}, current.rate(previous, key, scale)})
	}
	vcpus, _ := num("vcpu.current")
	if vcpus > 0 && vcpus <= usageMaxVCPUs {
		counter("cpu.total", "cpu", "CPU", "percent", "cpu.time", 100/(vcpus*1e9))
	}
	assigned, assignedOK := num("balloon.current")
	gauge("memory.assigned", "memory", "Assigned", "bytes", assigned*1024, assignedOK)
	rss, rssOK := num("balloon.rss")
	gauge("memory.host", "memory", "Host resident", "bytes", rss*1024, rssOK)
	available, availableOK := num("balloon.available")
	usable, usableOK := num("balloon.usable")
	if !usableOK {
		usable, usableOK = num("balloon.unused")
	}
	gauge("memory.guest_used", "memory", "Guest used", "bytes", (available-usable)*1024, availableOK && usableOK && usable <= available)
	blocks, _ := num("block.count")
	for i := 0; i < int(math.Min(blocks, 64)); i++ {
		prefix := "block." + strconv.Itoa(i) + "."
		dev := fields[prefix+"name"]
		path := fields[prefix+"path"]
		if dev == "" || path == "" || !strings.EqualFold(filepath.Base(filepath.Dir(path)), vmID) {
			continue // a CD drive, or an image outside the layout Nex owns
		}
		counter("disk."+dev+".read_bytes", "disk", dev+" read", "bytes_per_second", prefix+"rd.bytes", 1)
		counter("disk."+dev+".write_bytes", "disk", dev+" write", "bytes_per_second", prefix+"wr.bytes", 1)
		counter("disk."+dev+".read_iops", "disk", dev+" read IOPS", "iops", prefix+"rd.reqs", 1)
		counter("disk."+dev+".write_iops", "disk", dev+" write IOPS", "iops", prefix+"wr.reqs", 1)
	}
	// Tap names (vnetN) change on every start; the interface's position in the definition
	// does not, so it is the series identity.
	nets, _ := num("net.count")
	for i := 0; i < int(math.Min(nets, 64)); i++ {
		n := strconv.Itoa(i)
		counter("net."+n+".rx", "network", "NIC "+n+" rx", "bytes_per_second", "net."+n+".rx.bytes", 1)
		counter("net."+n+".tx", "network", "NIC "+n+" tx", "bytes_per_second", "net."+n+".tx.bytes", 1)
	}
	if vcpus > 0 && vcpus <= usageMaxVCPUs {
		for i := 0; i < int(math.Min(vcpus, usageMaxCPUSeries)); i++ {
			n := strconv.Itoa(i)
			counter("cpu.vcpu."+n, "cpu", "vCPU "+n, "percent", "vcpu."+n+".time", 100/1e9)
		}
	}
	return current, points
}

func (u *usageSampler) sampleHost() {
	at := time.Now() // before any read, for the same reason as sampleVMs
	var points []historyPoint
	u.host, points = readHostPoints(at, u.host) // u.host is only used on the sampler goroutine
	u.history.record("host", at, points)
}

// readHostPoints reads the host's counters and turns them into points against previous.
func readHostPoints(at time.Time, previous counters) (counters, []historyPoint) {
	read := func(path string) string { b, _ := os.ReadFile(path); return string(b) }
	var nics []string
	if ifaces, err := net.Interfaces(); err == nil {
		for _, i := range ifaces {
			switch nicKind(i.Name, i.Flags) {
			case "physical", "bond", "bridge":
				nics = append(nics, i.Name)
			}
		}
	}
	netStats := map[string][2]string{}
	for _, name := range nics {
		dir := "/sys/class/net/" + name + "/statistics/"
		netStats[name] = [2]string{read(dir + "rx_bytes"), read(dir + "tx_bytes")}
	}
	return hostHistoryPoints(read("/proc/stat"), read("/proc/meminfo"), read("/proc/diskstats"), read("/proc/loadavg"), nics, netStats, wholeDisk, at, previous)
}

// wholeDisk reports whether a diskstats name is a whole block device worth charting. Partitions
// and device-mapper volumes would count the same I/O twice.
func wholeDisk(name string) bool {
	for _, prefix := range []string{"loop", "ram", "zram", "dm-", "sr", "fd", "nbd", "md"} {
		if strings.HasPrefix(name, prefix) {
			return false
		}
	}
	_, err := os.Stat("/sys/block/" + name)
	return err == nil
}

// hostHistoryPoints turns host counters into history points, as vmHistoryPoints does for a VM.
func hostHistoryPoints(stat, meminfo, diskstats, loadavg string, nics []string, netStats map[string][2]string, whole func(string) bool, at time.Time, previous counters) (counters, []historyPoint) {
	current := counters{at: at, values: map[string]float64{}}
	var points []historyPoint
	add := func(key, group, label, unit string, v float64) {
		points = append(points, historyPoint{seriesMeta{key, group, label, unit}, v})
	}
	// CPU percent is busy jiffies over total jiffies between samples, so it needs no clock.
	// The total comes first and cores last, capped, so cores never crowd out other series.
	var cores []historyPoint
	cpu := func(line string) {
		f := strings.Fields(line)
		if len(f) < 9 || !strings.HasPrefix(f[0], "cpu") {
			return
		}
		var total, idle float64
		for i, s := range f[1:9] {
			v, _ := strconv.ParseFloat(s, 64)
			total += v
			if i == 3 || i == 4 { // idle, iowait
				idle += v
			}
		}
		key, label := "cpu.total", "CPU"
		core := strings.TrimPrefix(f[0], "cpu")
		if core != "" {
			if n, err := strconv.Atoi(core); err != nil || n >= usageMaxCPUSeries {
				return
			}
			key, label = "cpu.core."+core, "Core "+core
		}
		current.values[key+".total"], current.values[key+".idle"] = total, idle
		v := math.NaN()
		oldTotal, ok1 := previous.values[key+".total"]
		oldIdle, ok2 := previous.values[key+".idle"]
		if ok1 && ok2 && total > oldTotal && idle >= oldIdle {
			v = 100 * (1 - (idle-oldIdle)/(total-oldTotal))
		}
		p := historyPoint{seriesMeta{key, "cpu", label, "percent"}, v}
		if core != "" {
			cores = append(cores, p)
		} else {
			points = append(points, p)
		}
	}
	for _, line := range strings.Split(stat, "\n") {
		cpu(line)
	}
	mem := map[string]float64{}
	for _, line := range strings.Split(meminfo, "\n") {
		if f := strings.Fields(line); len(f) >= 2 {
			v, err := strconv.ParseFloat(f[1], 64)
			if err == nil {
				mem[strings.TrimSuffix(f[0], ":")] = v * 1024
			}
		}
	}
	used, total := math.NaN(), math.NaN()
	if v, ok := mem["MemTotal"]; ok {
		total = v
		if available, ok := mem["MemAvailable"]; ok {
			used = total - available
		}
	}
	add("memory.used", "memory", "Used", "bytes", used)
	add("memory.total", "memory", "Total", "bytes", total)
	for _, name := range nics {
		for i, dir := range []string{"rx", "tx"} {
			key := "net." + name + "." + dir
			if v, err := strconv.ParseFloat(strings.TrimSpace(netStats[name][i]), 64); err == nil {
				current.values[key] = v
			}
			add(key, "network", name+" "+dir, "bytes_per_second", current.rate(previous, key, 1))
		}
	}
	for _, line := range strings.Split(diskstats, "\n") {
		f := strings.Fields(line)
		if len(f) < 10 || !whole(f[2]) {
			continue
		}
		dev := f[2]
		for _, c := range []struct {
			suffix, label, unit string
			field               int
			scale               float64
		}{
			{"read_bytes", " read", "bytes_per_second", 5, 512},
			{"write_bytes", " write", "bytes_per_second", 9, 512},
			{"read_iops", " read IOPS", "iops", 3, 1},
			{"write_iops", " write IOPS", "iops", 7, 1},
		} {
			key := "disk." + dev + "." + c.suffix
			if v, err := strconv.ParseFloat(f[c.field], 64); err == nil {
				current.values[key] = v
			}
			add(key, "disk", dev+c.label, c.unit, current.rate(previous, key, c.scale))
		}
	}
	if f := strings.Fields(loadavg); len(f) >= 3 {
		for i, name := range []string{"1", "5", "15"} {
			v, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				v = math.NaN()
			}
			add("load."+name, "load", "Load "+name+"m", "load", v)
		}
	}
	return current, append(points, cores...)
}
