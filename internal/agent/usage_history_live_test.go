// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"nex.local/nex/internal/protocol"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// liveTest is a history with a fake virsh and a fast tick. domstats counts CPU time up by
// one vCPU-second per call, so every measured rate is 100%.
func liveTest(t *testing.T, tick time.Duration, domstats func(calls int64) (string, error)) (*usageHistory, *liveViews, string) {
	t.Helper()
	dir := t.TempDir()
	h := newUsageHistory(dir, "host-1")
	u := newUsageSampler(h)
	var calls atomic.Int64
	u.virsh = func(_ context.Context, args ...string) (string, error) {
		switch args[0] {
		case "domstate":
			if args[1] == vmID {
				return "running", nil
			}
			return "", errors.New("domain not found")
		case "domstats":
			return domstats(calls.Add(1))
		}
		return "", errors.New("unexpected virsh call")
	}
	ctx, cancel := context.WithCancel(context.Background())
	h.live.tick, h.live.lease, h.live.ctx = tick, 20*tick, ctx
	t.Cleanup(func() {
		cancel() // the agent's run ending stops every view
		h.live.running.Wait()
		if len(h.live.views) != 0 {
			t.Error("views survived the run context")
		}
	})
	return h, h.live, dir
}

func runningDomstats(calls int64) (string, error) {
	return fmt.Sprintf("Domain: 'web'\n  state.state=1\n  cpu.time=%d\n  vcpu.current=1\n  balloon.current=1024\n", calls*int64(100*time.Millisecond)), nil
}

func liveQuery(t *testing.T, h *usageHistory, target string) (int, protocol.StatsHistory) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	var out protocol.StatsHistory
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func TestLiveLeaseStartsRenewsAndExpires(t *testing.T) {
	h, l, _ := liveTest(t, 20*time.Millisecond, runningDomstats)
	target := "/stats?object=vm&id=" + vmID + "&range=live"
	if code, _ := liveQuery(t, h, target); code != 200 {
		t.Fatalf("live query = %d", code)
	}
	views := func() int { l.mu.Lock(); defer l.mu.Unlock(); return len(l.views) }
	// Renew for longer than one lease; the view must still be there.
	for i := 0; i < 4; i++ {
		time.Sleep(l.lease / 2)
		liveQuery(t, h, target)
		if views() != 1 {
			t.Fatal("renewed view expired")
		}
	}
	done := make(chan struct{})
	go func() { l.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sampling goroutine outlived its lease")
	}
	if views() != 0 {
		t.Fatal("expired view was not freed")
	}
}

func TestLiveCapsViewsPerHost(t *testing.T) {
	h, l, _ := liveTest(t, 20*time.Millisecond, runningDomstats)
	for i := 0; i < protocol.StatsLiveObjects; i++ {
		if _, _, err := l.query(fmt.Sprintf("vm-%d", i), vmID, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := l.query("vm-0", vmID, nil, time.Now()); err != nil {
		t.Fatalf("renewing an existing view was refused: %v", err)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats?object=host&range=live", nil))
	if rec.Code != 429 || !strings.Contains(rec.Body.String(), "too many realtime views on this host; close one and retry") {
		t.Fatalf("view beyond the cap = %d %s", rec.Code, rec.Body)
	}
}

func TestLiveAlignsStepsWithNullsAndNeverWritesToDisk(t *testing.T) {
	h, l, dir := liveTest(t, 100*time.Millisecond, runningDomstats) // one tick per 100 ms of CPU time
	target := "/stats?object=vm&id=" + vmID + "&range=live"
	code, first := liveQuery(t, h, target)
	if code != 200 || first.StepSeconds != 1 || first.Range != "live" || len(first.Series) != 0 {
		t.Fatalf("first live query = %d %+v", code, first)
	}
	var got protocol.StatsHistory
	waitFor(t, "three live samples", func() bool {
		_, got = liveQuery(t, h, target)
		for _, s := range got.Series {
			if s.Key == "cpu.total" {
				known := 0
				for _, v := range s.Values {
					if v != nil {
						known++
					}
				}
				return known >= 2
			}
		}
		return false
	})
	for _, s := range got.Series {
		if len(s.Values) != 120 {
			t.Fatalf("%s has %d values", s.Key, len(s.Values))
		}
	}
	values := seriesByKey(t, got.Series, "cpu.total")
	if values[0] != nil {
		t.Fatal("seconds before the view started must be null")
	}
	// The first sample has no previous counter; every later one measures 100% of one vCPU.
	measured := 0
	for _, v := range values {
		if v != nil {
			if *v < 50 || *v > 150 {
				t.Fatalf("live CPU = %v, want about 100", *v)
			}
			measured++
		}
	}
	if assigned := seriesByKey(t, got.Series, "memory.assigned"); assigned[119] == nil && assigned[118] == nil {
		t.Fatal("latest ticks carry no memory sample")
	}
	if got.Start.UnixMilli()%l.tick.Milliseconds() != 0 {
		t.Fatalf("start %v is not on a tick boundary", got.Start)
	}
	h.flush(time.Now())
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Fatalf("live mode wrote to disk: %v", entries)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.objects) != 0 {
		t.Fatal("live samples were merged into stored history")
	}
}

// A slow virsh must skip ticks, never run two samples of one view at once.
func TestLiveSlowVirshSkipsTicksWithoutOverlap(t *testing.T) {
	var active, peak atomic.Int64
	h, l, _ := liveTest(t, 20*time.Millisecond, func(calls int64) (string, error) {
		if n := active.Add(1); n > peak.Load() {
			peak.Store(n)
		}
		defer active.Add(-1)
		time.Sleep(70 * time.Millisecond)
		return runningDomstats(calls)
	})
	start := time.Now()
	liveQuery(t, h, "/stats?object=vm&id="+vmID+"&range=live")
	time.Sleep(400 * time.Millisecond)
	calls := 0
	_, got := liveQuery(t, h, "/stats?object=vm&id="+vmID+"&range=live")
	for _, s := range got.Series {
		if s.Key == "memory.assigned" {
			for _, v := range s.Values {
				if v != nil {
					calls++
				}
			}
		}
	}
	if peak.Load() > 1 {
		t.Fatalf("%d overlapping samples", peak.Load())
	}
	if max := int(time.Since(start)/(70*time.Millisecond)) + 1; calls == 0 || calls > max {
		t.Fatalf("%d samples in %v with a 70ms virsh", calls, time.Since(start))
	}
	_ = l
}

func TestLiveStoppedVMIsNullsAndUnknownVMIs404(t *testing.T) {
	h, _, _ := liveTest(t, 20*time.Millisecond, func(int64) (string, error) {
		return "Domain: 'web'\n  state.state=5\n", nil // defined, not running
	})
	h.record("vm-"+vmID, time.Now(), []historyPoint{point("cpu.total", 1)})
	target := "/stats?object=vm&id=" + vmID + "&range=live"
	liveQuery(t, h, target)
	time.Sleep(100 * time.Millisecond)
	code, got := liveQuery(t, h, target)
	values := seriesByKey(t, got.Series, "cpu.total")
	if code != 200 {
		t.Fatalf("stopped VM live query = %d", code)
	}
	for _, v := range values {
		if v != nil {
			t.Fatalf("stopped VM reported %v", *v)
		}
	}
	if code, _ := liveQuery(t, h, "/stats?object=vm&id="+otherVMID+"&range=live"); code != 404 {
		t.Fatalf("unknown VM live query = %d", code)
	}
}

// The window ends at the newest tick that can hold a sample, never at one still being taken.
func TestLiveWindowEndsAtTheNewestWrittenTick(t *testing.T) {
	_, l, _ := liveTest(t, time.Second, runningDomstats)
	s := time.Now().Add(time.Hour).Truncate(time.Second)
	v := &liveView{name: "host", slots: make([]liveSlot, 120), series: []seriesMeta{{"memory.used", "memory", "Used", "bytes"}}}
	for i, value := range []float64{2, 1} {
		b := s.Unix() - 1 + int64(i)
		v.slots[b%120] = liveSlot{bucket: b, values: map[string]float64{"memory.used": value}}
	}
	l.views["host"] = v // no goroutine: the test owns the ring
	for _, now := range []time.Time{s.Add(600 * time.Millisecond), s.Add(1050 * time.Millisecond)} {
		start, series, err := l.query("host", "", nil, now)
		if err != nil {
			t.Fatal(err)
		}
		values := series[0].Values
		if !start.Equal(s.Add(-119*time.Second).UTC()) || valueAt(values, 119) != "1" || valueAt(values, 118) != "2" {
			t.Fatalf("at %v: start %v, newest %s, before %s", now.Sub(s), start, valueAt(values, 119), valueAt(values, 118))
		}
	}
	delete(l.views, "host")
}

// Through the sampling loop: a counter that goes backwards and a VM that stops and starts again
// both give nulls, then a fresh rate from the next pair of samples.
func TestLiveCounterResetAndRestartThroughTheLoop(t *testing.T) {
	running := func(cpu int64) string {
		return fmt.Sprintf("Domain: 'web'\n  state.state=1\n  cpu.time=%d\n  vcpu.current=1\n  balloon.current=1024\n", cpu)
	}
	ms := int64(time.Millisecond)
	script := []string{
		running(0), running(20 * ms), running(40 * ms), // rate
		"Domain: 'web'\n  state.state=5\n", "Domain: 'web'\n  state.state=5\n", // stopped
		running(0), running(20 * ms), // restarted: unknown, then rate
		running(5 * ms), running(25 * ms), // counter went backwards: unknown, then rate
	}
	want := []string{"unknown", "rate", "rate", "empty", "empty", "unknown", "rate", "unknown", "rate"}
	_, l, _ := liveTest(t, 40*time.Millisecond, func(call int64) (string, error) {
		if int(call) > len(script) {
			return "Domain: 'web'\n  state.state=5\n", nil
		}
		return script[call-1], nil
	})
	if _, _, err := l.query("vm-"+vmID, vmID, nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	var slots []liveSlot
	waitFor(t, "the scripted samples", func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		slots = nil
		for _, slot := range l.views["vm-"+vmID].slots {
			if slot.bucket != 0 {
				slots = append(slots, slot)
			}
		}
		return len(slots) >= len(want)
	})
	sort.Slice(slots, func(i, j int) bool { return slots[i].bucket < slots[j].bucket })
	for i, w := range want {
		cpu, ok := slots[i].values["cpu.total"]
		got := "rate"
		switch {
		case !ok:
			got = "empty"
		case math.IsNaN(cpu):
			got = "unknown"
		}
		if got != w {
			t.Fatalf("sample %d = %s (%v), want %s", i, got, slots[i].values, w)
		}
	}
}

// A renewal racing expiry never leaves two views for one object, and an expired view frees its
// place for another object.
func TestLiveRenewRacingExpiryKeepsTheCapHonest(t *testing.T) {
	_, l, _ := liveTest(t, 5*time.Millisecond, runningDomstats)
	l.lease = 5 * time.Millisecond
	for i := 0; i < protocol.StatsLiveObjects; i++ {
		if _, _, err := l.query(fmt.Sprintf("vm-%d", i), vmID, nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 200; i++ {
		if _, _, err := l.query("vm-0", vmID, nil, time.Now()); err != nil && !errors.Is(err, errLiveCap) {
			t.Fatal(err)
		}
		l.mu.Lock()
		n := len(l.views)
		l.mu.Unlock()
		if n > protocol.StatsLiveObjects {
			t.Fatalf("%d views exceed the cap", n)
		}
		time.Sleep(time.Millisecond)
	}
	waitFor(t, "every view to expire", func() bool { l.mu.Lock(); defer l.mu.Unlock(); return len(l.views) == 0 })
	if _, _, err := l.query("vm-ninth", vmID, nil, time.Now()); err != nil {
		t.Fatalf("a freed place was not reused: %v", err)
	}
}

// Stored history for a VM libvirt no longer knows still answers with nulls, and the view expires
// normally.
func TestLiveVMGoneFromLibvirtIsNullsAndExpires(t *testing.T) {
	h, l, _ := liveTest(t, 20*time.Millisecond, func(int64) (string, error) { return "", errors.New("domain not found") })
	h.record("vm-"+otherVMID, time.Now(), []historyPoint{point("cpu.total", 1)})
	target := "/stats?object=vm&id=" + otherVMID + "&range=live"
	liveQuery(t, h, target)
	time.Sleep(100 * time.Millisecond)
	code, got := liveQuery(t, h, target)
	if code != 200 {
		t.Fatalf("live query = %d", code)
	}
	for _, v := range seriesByKey(t, got.Series, "cpu.total") {
		if v != nil {
			t.Fatalf("a VM libvirt does not know reported %v", *v)
		}
	}
	done := make(chan struct{})
	go func() { l.running.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("view did not expire")
	}
}

// BUG L5 (real host, libvirt 11.3): domstats answers cpu.time and balloon.* for a shut-off VM,
// even one never started, so a stopped VM's realtime view showed CPU and memory. Only a domain
// libvirt reports running (state.state=1) is sampled; anything else, or no state, is nulls.
func TestLiveSampleTreatsOnlyARunningStateAsRunning(t *testing.T) {
	// Captured from a shut-off VM on the lab host (sanitized name).
	const stopped = "Domain: 'usage-qa-<run>'\n  cpu.time=2830000000\n  cpu.user=1750000000\n  cpu.system=1080000000\n  balloon.current=1048576\n  balloon.maximum=1048576\n\n"
	withState := func(state string) string {
		return strings.Replace(stopped, "  cpu.time", "  state.state="+state+"\n  state.reason=1\n  vcpu.current=1\n  cpu.time", 1)
	}
	var args []string
	for _, c := range []struct {
		name, out string
		running   bool
	}{
		{"shut off, no state member (captured)", stopped, false},
		{"shut off", withState("5"), false},
		{"paused", withState("3"), false},
		{"running", withState("1"), true},
	} {
		_, l, _ := liveTest(t, time.Second, func(int64) (string, error) { return c.out, nil })
		wrapped := l.sampler.virsh
		l.sampler.virsh = func(ctx context.Context, a ...string) (string, error) { args = a; return wrapped(ctx, a...) }
		v := &liveView{name: "vm-" + vmID, vmID: vmID}
		previous := counters{at: time.Now().Add(-time.Second), values: map[string]float64{"cpu.total": 1}}
		current, points := l.sample(v, time.Now(), previous)
		if running := len(points) > 0; running != c.running {
			t.Fatalf("%s: sampled as running = %v (%v)", c.name, running, points)
		}
		if !c.running && current.values != nil {
			t.Fatalf("%s: counters kept for a VM that is not running", c.name)
		}
		if !strings.Contains(strings.Join(args, " "), "--state") || !strings.Contains(strings.Join(args, " "), "--nowait") {
			t.Fatalf("live domstats arguments = %v", args)
		}
	}
}
