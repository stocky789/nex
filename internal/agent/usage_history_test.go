// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func point(key string, v float64) historyPoint {
	return historyPoint{seriesMeta{key, "cpu", key, "percent"}, v}
}

func seriesByKey(t *testing.T, series []protocol.StatsSeries, key string) []*float64 {
	t.Helper()
	for _, s := range series {
		if s.Key == key {
			return s.Values
		}
	}
	t.Fatalf("series %q missing", key)
	return nil
}

func valueAt(values []*float64, i int) string {
	if values[i] == nil {
		return "unknown"
	}
	return fmt.Sprint(*values[i])
}

// A rate needs two samples of the same counter over a positive window. A counter that went
// backwards is a restarted guest, and publishing the wrapped difference would be a lie.
func TestHistoryRateIsUnknownWithoutAUsablePreviousSample(t *testing.T) {
	base := time.Unix(1_800_000_000, 0)
	previous := counters{at: base, values: map[string]float64{"c": 1000}}
	for _, c := range []struct {
		name string
		cur  counters
		want float64
	}{
		{"rate", counters{at: base.Add(5 * time.Second), values: map[string]float64{"c": 6000}}, 1000},
		{"idle is zero, not unknown", counters{at: base.Add(5 * time.Second), values: map[string]float64{"c": 1000}}, 0},
		{"reset", counters{at: base.Add(5 * time.Second), values: map[string]float64{"c": 10}}, math.NaN()},
		{"missing now", counters{at: base.Add(5 * time.Second), values: map[string]float64{}}, math.NaN()},
		{"same instant", counters{at: base, values: map[string]float64{"c": 2000}}, math.NaN()},
		{"too old", counters{at: base.Add(3 * time.Minute), values: map[string]float64{"c": 2000}}, math.NaN()},
	} {
		got := c.cur.rate(previous, "c", 1)
		if !(got == c.want || math.IsNaN(got) && math.IsNaN(c.want)) {
			t.Fatalf("%s: rate = %v, want %v", c.name, got, c.want)
		}
	}
	if got := (counters{at: base.Add(time.Second), values: map[string]float64{"c": 1}}).rate(counters{}, "c", 1); !math.IsNaN(got) {
		t.Fatalf("first sample rate = %v, want unknown", got)
	}
}

const otherVMID = "1a2b3c4d-5e6f-4a1b-8c2d-3e4f5a6b7c8d"

func TestSplitDomainStatsSeparatesEveryDomain(t *testing.T) {
	out := "Domain: 'web'\n  state.state=1\n  cpu.time=10\n\nDomain: 'db one'\n  cpu.time=20\n"
	blocks := splitDomainStats(out)
	if len(blocks) != 2 || domainFields(blocks["web"])["cpu.time"] != "10" || domainFields(blocks["db one"])["cpu.time"] != "20" {
		t.Fatalf("blocks = %#v", blocks)
	}
}

// Every counter series is unknown on a VM's first sample and measured on the second; memory the
// guest's balloon driver has not reported stays unknown rather than zero.
func TestVMHistoryPointsMeasureRatesOnlyFromTwoSamples(t *testing.T) {
	block := func(cpu, vcpu0, rd, rx uint64) string {
		return strings.Join([]string{
			fmt.Sprintf("cpu.time=%d", cpu), "vcpu.current=2", fmt.Sprintf("vcpu.0.time=%d", vcpu0), "vcpu.1.time=0",
			"balloon.current=1048576", "block.count=2",
			"block.0.name=vda", "block.0.path=/srv/nex-vms/" + vmID + "/disk-0.qcow2", fmt.Sprintf("block.0.rd.bytes=%d", rd), "block.0.wr.bytes=0", "block.0.rd.reqs=0", "block.0.wr.reqs=0",
			"block.1.name=sda", "block.1.path=/srv/nex-media/installer.iso", "block.1.rd.bytes=999",
			"net.count=1", "net.0.name=vnet7", fmt.Sprintf("net.0.rx.bytes=%d", rx), "net.0.tx.bytes=0",
		}, "\n")
	}
	base := time.Unix(1_800_000_000, 0)
	first, points := vmHistoryPoints(vmID, block(0, 0, 0, 0), base, counters{})
	values := map[string]float64{}
	for _, p := range points {
		values[p.Key] = p.Value
	}
	if !math.IsNaN(values["cpu.total"]) || !math.IsNaN(values["disk.vda.read_bytes"]) || !math.IsNaN(values["net.0.rx"]) {
		t.Fatalf("first sample reported rates: %v", values)
	}
	if values["memory.assigned"] != 1048576*1024 || !math.IsNaN(values["memory.guest_used"]) || !math.IsNaN(values["memory.host"]) {
		t.Fatalf("memory = %v", values)
	}
	if _, ok := values["disk.sda.read_bytes"]; ok {
		t.Fatal("installer ISO charted as the VM's disk")
	}
	_, points = vmHistoryPoints(vmID, block(10e9, 10e9, 5000, 500), base.Add(10*time.Second), first)
	for _, p := range points {
		values[p.Key] = p.Value
	}
	want := map[string]float64{"cpu.total": 50, "cpu.vcpu.0": 100, "cpu.vcpu.1": 0, "disk.vda.read_bytes": 500, "disk.vda.write_bytes": 0, "net.0.rx": 50}
	for key, v := range want {
		if values[key] != v {
			t.Fatalf("%s = %v, want %v (all %v)", key, values[key], v, values)
		}
	}
}

func TestHostHistoryPointsMeasureCPUMemoryDiskAndLoad(t *testing.T) {
	stat := func(user, idle uint64) string {
		return fmt.Sprintf("cpu  %d 0 0 %d 0 0 0 0 0 0\ncpu0 %d 0 0 %d 0 0 0 0 0 0\n", user, idle, user, idle)
	}
	disk := func(sectors uint64) string {
		return fmt.Sprintf("   8       0 sda 10 0 %d 0 0 0 0 0 0\n   8       1 sda1 10 0 %d 0 0 0 0 0 0\n", sectors, sectors)
	}
	whole := func(name string) bool { return name == "sda" }
	meminfo := "MemTotal:       1000 kB\nMemAvailable:    250 kB\n"
	nics := []string{"eno1"}
	base := time.Unix(1_800_000_000, 0)
	first, _ := hostHistoryPoints(stat(100, 100), meminfo, disk(0), "0.5 1.0 1.5 1/100 42", nics, map[string][2]string{"eno1": {"0", "0"}}, whole, base, counters{})
	_, points := hostHistoryPoints(stat(175, 125), meminfo, disk(10), "0.5 1.0 1.5 1/100 42", nics, map[string][2]string{"eno1": {"1000\n", "0"}}, whole, base.Add(5*time.Second), first)
	values := map[string]float64{}
	for _, p := range points {
		values[p.Key] = p.Value
	}
	want := map[string]float64{"cpu.total": 75, "cpu.core.0": 75, "memory.used": 750 * 1024, "memory.total": 1000 * 1024, "disk.sda.read_bytes": 1024, "net.eno1.rx": 200, "net.eno1.tx": 0, "load.15": 1.5}
	for key, v := range want {
		if values[key] != v {
			t.Fatalf("%s = %v, want %v (all %v)", key, values[key], v, values)
		}
	}
	if _, ok := values["disk.sda1.read_bytes"]; ok {
		t.Fatal("partition charted alongside its disk")
	}
}

// Each step is the average of its known samples; a step with only unknown samples, and a step
// with no samples at all, is unknown; the step still in progress reports its running average.
func TestRingConsolidatesStepsAndAlignsQueries(t *testing.T) {
	f := newRing()
	base := time.Unix(1_800_000_000/60*60, 0) // on a minute boundary
	f.add(base, []historyPoint{point("a", 10)})
	f.add(base.Add(20*time.Second), []historyPoint{point("a", 30)})
	f.add(base.Add(40*time.Second), []historyPoint{point("a", math.NaN())})
	f.add(base.Add(time.Minute), []historyPoint{point("a", math.NaN())})
	f.add(base.Add(3*time.Minute), []historyPoint{point("a", 7), point("b", 1)})
	start, series := f.query("24h", base.Add(3*time.Minute+30*time.Second))
	if want := base.Add(3*time.Minute - 1439*time.Minute).UTC(); !start.Equal(want) {
		t.Fatalf("start = %v, want %v", start, want)
	}
	a := seriesByKey(t, series, "a")
	if len(a) != 1440 {
		t.Fatalf("points = %d", len(a))
	}
	n := len(a) - 1
	for i, want := range []string{"20", "unknown", "unknown", "7"} {
		if got := valueAt(a, n-3+i); got != want {
			t.Fatalf("step %d = %s, want %s", i, got, want)
		}
	}
	// A series that appears later has no invented history.
	b := seriesByKey(t, series, "b")
	if valueAt(b, n-1) != "unknown" || valueAt(b, n) != "1" {
		t.Fatalf("new series = %s, %s", valueAt(b, n-1), valueAt(b, n))
	}
	// Ten minutes later nothing was sampled: the old steps are still there and the new ones unknown.
	_, series = f.query("24h", base.Add(13*time.Minute))
	a = seriesByKey(t, series, "a")
	if valueAt(a, n-13) != "20" || valueAt(a, n) != "unknown" {
		t.Fatalf("after gap: %s, %s", valueAt(a, n-13), valueAt(a, n))
	}
	// A gap longer than the ring leaves no stale step visible.
	f.add(base.Add(48*time.Hour), []historyPoint{point("a", 1)})
	_, series = f.query("24h", base.Add(48*time.Hour))
	for i, v := range seriesByKey(t, series, "a")[:n] {
		if v != nil {
			t.Fatalf("stale step %d = %v after a two-day gap", i, *v)
		}
	}
}

func TestHistoryFlushWritesSlotsInPlaceAndReloads(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "host-1")
	now := time.Now().Truncate(time.Minute)
	path := filepath.Join(dir, "vm-"+vmID+".rrd")
	h.record("vm-"+vmID, now.Add(-3*time.Minute), []historyPoint{point("a", 4)})
	h.flush(now.Add(-3 * time.Minute)) // creates the file: a whole-layout write
	created, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	h.record("vm-"+vmID, now.Add(-2*time.Minute), []historyPoint{point("a", 6)})
	h.record("vm-"+vmID, now, []historyPoint{point("a", 8)})
	f := h.objects["vm-"+vmID]
	if f.layout || len(f.slots) == 0 {
		t.Fatalf("an unchanged series set must flush slots in place (layout=%v slots=%d)", f.layout, len(f.slots))
	}
	if err = os.WriteFile(filepath.Join(dir, ".tmp-left-by-a-crash"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	h.flush(now)
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != "vm-"+vmID+".rrd" {
		t.Fatalf("files = %v", entries)
	}
	if after, _ := os.Stat(path); !os.SameFile(created, after) {
		t.Fatal("slot flush replaced the file instead of writing in place")
	}
	reloaded := newUsageHistory(dir, "host-1")
	g := reloaded.load("vm-"+vmID, false)
	if g == nil {
		t.Fatal("history not reloaded")
	}
	_, series := g.query("24h", now)
	a := seriesByKey(t, series, "a")
	// The unfinished minute's running average is memory-only by design.
	if n := len(a) - 1; valueAt(a, n-3) != "4" || valueAt(a, n-2) != "6" || valueAt(a, n-1) != "unknown" || valueAt(a, n) != "unknown" {
		t.Fatalf("reloaded 24h = %s %s %s %s", valueAt(a, n-3), valueAt(a, n-2), valueAt(a, n-1), valueAt(a, n))
	}
	_, series = g.query("10m", now)
	for _, v := range seriesByKey(t, series, "a") {
		if v != nil {
			t.Fatal("the memory-only 5 s tier was persisted")
		}
	}
	// Truncated and foreign files are replaced, not fatal.
	whole, _ := os.ReadFile(path)
	for name, data := range map[string][]byte{"vm-" + otherVMID: whole[:len(whole)-3], "vm-00000000-0000-4000-8000-000000000000": []byte("garbage")} {
		if err = os.WriteFile(filepath.Join(dir, name+".rrd"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if reloaded.load(name, false) != nil || reloaded.load(name, true) == nil {
			t.Fatalf("damaged file %s was not replaced", name)
		}
	}
}

func TestDeletedVMCompletionRemovesItsHistory(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "")
	h.record("vm-"+vmID, time.Now(), []historyPoint{point("a", 1)})
	h.flush(time.Now())
	entry := JournalEntry{Status: "failed", Command: &protocol.Command{Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: strings.ToUpper(vmID)}}}
	forgetDeletedVM(h, entry)
	path := filepath.Join(dir, "vm-"+vmID+".rrd")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a failed delete removed history")
	}
	entry.Status = "succeeded"
	forgetDeletedVM(h, entry)
	if _, err := os.Stat(path); !os.IsNotExist(err) || h.objects["vm-"+vmID] != nil {
		t.Fatalf("deleted VM history remains: %v", err)
	}
}

func TestHistoryPrunesOldFilesButKeepsSampledSeries(t *testing.T) {
	dir := t.TempDir()
	old := filepath.Join(dir, "vm-"+otherVMID+".rrd")
	if err := os.WriteFile(old, nil, 0600); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-400 * 24 * time.Hour)
	os.Chtimes(old, stale, stale)
	h := newUsageHistory(dir, "")
	h.flush(time.Now())
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("year-old history file kept")
	}
	now := time.Now()
	f := newRing()
	f.add(now.Add(-2*time.Hour), []historyPoint{point("gone", math.NaN())})
	f.add(now, []historyPoint{point("kept", 1), point("unknown but sampled", math.NaN())})
	f.compact(now)
	if len(f.Series) != 2 || f.Series[0].Key != "kept" || f.Series[1].Key != "unknown but sampled" {
		t.Fatalf("series after compact = %v", f.Series)
	}
}

// BUG (review): a clock stepped back by more than a step (for example a boot before time sync
// with a slow RTC, then NTP) reset the stored tiers and rewrote the file, wiping up to a year of
// history. Stored tiers now ignore samples behind their current step; only the memory-only 5 s
// tier starts again.
func TestRingKeepsStoredTiersWhenTheClockGoesBack(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "")
	base := time.Unix(1_800_000_000/86400*86400, 0).Add(12 * time.Hour)
	for i := 0; i <= 180; i++ {
		h.record("host", base.Add(time.Duration(i)*time.Minute), []historyPoint{point("a", 1)})
	}
	h.flush(base.Add(180 * time.Minute))
	back := base.Add(177 * time.Minute)
	h.record("host", back, []historyPoint{point("a", 5)})
	if h.objects["host"].layout {
		t.Fatal("a backwards clock step scheduled a whole-file rewrite")
	}
	h.flush(back)
	f := newUsageHistory(dir, "").load("host", false)
	_, series := f.query("24h", base.Add(180*time.Minute))
	known := 0
	for _, v := range seriesByKey(t, series, "a") {
		if v != nil {
			known++
		}
	}
	if known < 179 {
		t.Fatalf("24h known points after a backwards clock step = %d, want at least 179", known)
	}
	_, series = h.objects["host"].query("10m", back)
	if a := seriesByKey(t, series, "a"); valueAt(a, len(a)-1) != "5" {
		t.Fatalf("5 s tier after the clock went back = %s", valueAt(a, len(a)-1))
	}
}

// BUG (review): flush dropped an idle object from memory an hour after its last sample while its
// day step was still open, so a stopped VM lost its last day point in the 1y tier.
func TestIdleObjectIsKeptUntilItsDayStepIsFinished(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "")
	start := time.Unix(1_800_000_000/86400*86400, 0).Add(20 * time.Hour)
	for m := 0; m <= 120; m++ {
		h.record("vm-"+vmID, start.Add(time.Duration(m)*time.Minute), []historyPoint{point("a", 50)})
	}
	for m := 120; m <= 7*60; m++ { // flushes keep running past midnight after the VM stops
		h.flush(start.Add(time.Duration(m) * time.Minute))
	}
	f := newUsageHistory(dir, "").load("vm-"+vmID, false)
	if f == nil {
		t.Fatal("history not reloaded")
	}
	_, series := f.query("1y", start.Add(7*time.Hour))
	if a := seriesByKey(t, series, "a"); valueAt(a, len(a)-2) != "50" {
		t.Fatalf("stopped VM's day point = %s, want 50", valueAt(a, len(a)-2))
	}
}

// Ticks a little early or late around 5 s boundaries must still fill every 5 s step.
func TestRingPlacesJitteredTicksInTheirOwnSteps(t *testing.T) {
	f := newRing()
	base := time.Unix(1_800_000_000, 0) // a 5 s boundary
	for i, jitter := range []time.Duration{-40 * time.Millisecond, 900 * time.Millisecond, -300 * time.Millisecond, 20 * time.Millisecond} {
		f.add(base.Add(time.Duration(i)*5*time.Second+jitter), []historyPoint{point("a", float64(i+1))})
	}
	_, series := f.query("10m", base.Add(15*time.Second+100*time.Millisecond))
	a := seriesByKey(t, series, "a")
	for i := 0; i < 4; i++ {
		if got := valueAt(a, len(a)-4+i); got != fmt.Sprint(i+1) {
			t.Fatalf("step %d = %s", i, got)
		}
	}
}

func TestHistoryCapsSeriesAndRecordedVMs(t *testing.T) {
	f := newRing()
	points := make([]historyPoint, historyMaxSeries+1)
	for i := range points {
		points[i] = point(fmt.Sprint(i), 1)
	}
	if complete, _ := f.add(time.Now(), points); complete || len(f.Series) != historyMaxSeries {
		t.Fatalf("series cap: %d kept", len(f.Series))
	}
	h := newUsageHistory(t.TempDir(), "")
	for i := 0; i < historyMaxVMs; i++ {
		h.objects[fmt.Sprintf("vm-query-%d", i)] = newRing() // loaded by queries, not recorded
	}
	h.record("vm-"+vmID, time.Now(), []historyPoint{point("a", 1)})
	if h.objects["vm-"+vmID] == nil {
		t.Fatal("objects loaded only for queries counted toward the recording cap")
	}
	for _, o := range h.objects {
		o.recording, o.updated = true, time.Now()
	}
	h.record("host", time.Now(), []historyPoint{point("a", 1)})
	h.record("vm-"+otherVMID, time.Now(), []historyPoint{point("a", 1)})
	if h.objects["host"] == nil || h.objects["vm-"+otherVMID] != nil {
		t.Fatal("recording cap: host must be kept and the extra VM refused")
	}
	// VMs that stopped over an hour ago stay in memory for their open day step, but no longer
	// count toward the cap, so they cannot block a VM that starts later.
	later := time.Now().Add(3 * time.Hour)
	h.record("vm-"+otherVMID, later, []historyPoint{point("a", 1)})
	if h.objects["vm-"+otherVMID] == nil {
		t.Fatal("stopped VMs still in memory blocked a newly running VM")
	}
}

// A host with more cores than the series cap must still record memory, network, disk and load.
func TestHostHistoryPointsCapCoresBehindOtherSeries(t *testing.T) {
	var stat strings.Builder
	stat.WriteString("cpu  1 0 0 1 0 0 0 0\n")
	for i := 0; i < 200; i++ {
		fmt.Fprintf(&stat, "cpu%d 1 0 0 1 0 0 0 0\n", i)
	}
	_, points := hostHistoryPoints(stat.String(), "MemTotal: 1 kB\nMemAvailable: 1 kB\n", "", "0 0 0", []string{"eno1"}, map[string][2]string{}, func(string) bool { return false }, time.Now(), counters{})
	f := newRing()
	if complete, _ := f.add(time.Now(), points); !complete {
		t.Fatalf("%d host series exceed the file cap", len(points))
	}
	cores := 0
	for _, s := range f.Series {
		if strings.HasPrefix(s.Key, "cpu.core.") {
			cores++
		}
	}
	keys := fmt.Sprint(f.Series)
	if cores != usageMaxCPUSeries || !strings.Contains(keys, "memory.used") || !strings.Contains(keys, "load.1") || !strings.Contains(keys, "net.eno1.rx") {
		t.Fatalf("cores = %d, series = %s", cores, keys)
	}
}

func fakeVirsh(domstats func() (string, error)) func(context.Context, ...string) (string, error) {
	return func(_ context.Context, args ...string) (string, error) {
		if args[0] == "list" {
			return vmID + " web\n", nil
		}
		return domstats()
	}
}

// Rates use the moment libvirt was asked, not when a slow virsh finished printing.
func TestSamplerTimestampsBeforeASlowDomstats(t *testing.T) {
	u := newUsageSampler(newUsageHistory(t.TempDir(), ""))
	var asked time.Time
	u.virsh = fakeVirsh(func() (string, error) {
		asked = time.Now()
		time.Sleep(50 * time.Millisecond)
		return "Domain: 'web'\n  cpu.time=1\n  vcpu.current=1\n", nil
	})
	u.sampleVMs(context.Background())
	if at := u.latest[vmID].at; at.IsZero() || at.After(asked) {
		t.Fatalf("sample time %v is after domstats was asked at %v", at, asked)
	}
}

// A domstats failure is not a failure to list VMs: usage stays reported until it ages out.
func TestSamplerKeepsUsageWhenDomstatsFails(t *testing.T) {
	u := newUsageSampler(newUsageHistory(t.TempDir(), ""))
	u.virsh = fakeVirsh(func() (string, error) { return "Domain: 'web'\n  cpu.time=1\n  vcpu.current=1\n", nil })
	u.sampleVMs(context.Background())
	u.virsh = fakeVirsh(func() (string, error) { return "", fmt.Errorf("libvirt busy") })
	u.sampleVMs(context.Background())
	if got, err := u.observations(); err != nil || len(got) != 1 {
		t.Fatalf("observations = %v, %v", got, err)
	}
	u.virsh = func(context.Context, ...string) (string, error) { return "", fmt.Errorf("libvirt down") }
	u.sampleVMs(context.Background())
	if _, err := u.observations(); err == nil {
		t.Fatal("a failed domain list must fail the usage check")
	}
}

func TestStatsHandlerValidatesQueries(t *testing.T) {
	h := newUsageHistory(t.TempDir(), "host-1")
	h.record("host", time.Now(), []historyPoint{point("cpu.total", 12)})
	for _, c := range []struct {
		target string
		status int
	}{
		{"/stats?object=host&range=10m", 200},
		{"/stats?object=host&range=2h", 400},
		{"/stats?object=pool&range=10m", 400},
		{"/stats?object=vm&id=../host&range=10m", 400},
		{"/stats?object=vm&id=" + vmID + "&range=1y", 404},
		{"/other", 404},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, c.target, nil))
		if rec.Code != c.status {
			t.Fatalf("%s = %d %s, want %d", c.target, rec.Code, rec.Body, c.status)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats?object=host&range=10m", nil))
	var got protocol.StatsHistory
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	values := seriesByKey(t, got.Series, "cpu.total")
	if got.ID != "host-1" || got.StepSeconds != 5 || len(values) != 120 || valueAt(values, 119) != "12" || values[0] != nil {
		t.Fatalf("history = %+v", got)
	}
	if got.Start.Unix()%5 != 0 {
		t.Fatalf("start %v is not on a step boundary", got.Start)
	}
}

// The Controller sends queries one after another on the connection the agent dialled.
func TestStatsChannelServesSequentialQueriesOnOneConnection(t *testing.T) {
	h := newUsageHistory(t.TempDir(), "host-1")
	h.record("host", time.Now(), []historyPoint{point("cpu.total", 1)})
	agentSide, controllerSide := net.Pipe()
	done := make(chan struct{})
	go func() { defer close(done); serveStatsConn(context.Background(), agentSide, h) }()
	r := bufio.NewReader(controllerSide)
	for _, target := range []string{"/stats?object=host&range=10m", "/stats?object=vm&id=" + vmID + "&range=24h", "/stats?object=host&range=1y"} {
		req, _ := http.NewRequest(http.MethodGet, "http://host"+target, nil)
		if err := req.Write(controllerSide); err != nil {
			t.Fatal(err)
		}
		resp, err := http.ReadResponse(r, req)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if want := map[bool]int{true: 404, false: 200}[strings.Contains(target, "object=vm")]; resp.StatusCode != want {
			t.Fatalf("%s = %d, want %d", target, resp.StatusCode, want)
		}
	}
	controllerSide.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("channel did not end when the Controller closed it")
	}
}

// Files are written outside the lock; a VM deleted meanwhile must not be recreated by the
// write that was already in flight.
func TestHistoryWholeFileWriteDoesNotRecreateADeletedVM(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "")
	h.record("vm-"+vmID, time.Now(), []historyPoint{point("a", 1)})
	h.mu.Lock()
	f := h.objects["vm-"+vmID]
	job := flushJob{name: "vm-" + vmID, f: f, whole: f.encode()}
	h.mu.Unlock()
	h.forget(vmID)
	path := filepath.Join(dir, "vm-"+vmID+".rrd")
	if err := h.replace(job, path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("in-flight write recreated a deleted VM's history")
	}
}

// BUG (lab): a VM that stopped never finished its last minute, hour and day, because steps were
// finished only by the next sample; an agent restart then discarded them.
func TestStoppedVMStepsAreFinishedAndSurviveARestart(t *testing.T) {
	dir := t.TempDir()
	h := newUsageHistory(dir, "")
	start := time.Now().Add(-3 * 24 * time.Hour).Truncate(24 * time.Hour).Add(time.Hour + 10*time.Second)
	h.record("vm-"+vmID, start, []historyPoint{point("a", 9)})
	h.flush(start)
	// The VM stops; nothing more is sampled, and flushes keep running past every step's end.
	h.flush(start.Add(2 * time.Minute))
	later := start.Add(2 * 24 * time.Hour)
	h.flush(later)
	h.flush(later.Add(time.Minute)) // a stopped VM with finished steps writes nothing more
	if f := h.objects["vm-"+vmID]; f != nil && len(f.slots) > 0 {
		t.Fatalf("stopped VM keeps writing slots: %d", len(f.slots))
	}
	reloaded := newUsageHistory(dir, "")
	f := reloaded.load("vm-"+vmID, false)
	if f == nil {
		t.Fatal("history not reloaded")
	}
	for _, c := range []struct {
		rangeName string
		now       time.Time
		back      int
	}{{"24h", start.Add(2 * time.Minute), 2}, {"30d", later, 48}, {"1y", later, 2}} {
		_, series := f.query(c.rangeName, c.now)
		a := seriesByKey(t, series, "a")
		if got := valueAt(a, len(a)-1-c.back); got != "9" {
			t.Fatalf("%s step after restart = %s, want 9", c.rangeName, got)
		}
	}
	// A read before any flush sees the finished step too.
	g := newRing()
	g.add(start, []historyPoint{point("a", 3)})
	_, series := g.query("24h", start.Add(5*time.Minute))
	if a := seriesByKey(t, series, "a"); valueAt(a, len(a)-6) != "3" {
		t.Fatalf("unflushed finished minute = %s", valueAt(a, len(a)-6))
	}
}
