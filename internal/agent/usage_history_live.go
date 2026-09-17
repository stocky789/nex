// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"errors"
	"math"
	"nex.local/nex/internal/protocol"
	"sync"
	"time"
)

// Realtime views sample one object every second into a memory-only ring while someone is
// looking. A live query starts the view or renews its lease; when the lease runs out the loop
// stops and the ring is freed. Nothing here is written to disk or merged into stored history:
// the 5 s sampler remains the only source of stored tiers.
//
// Each view has its own goroutine that samples on tick boundaries, so a slow virsh call delays
// only that view, and sampling is sequential: a call that overruns its deadline skips the
// seconds it missed rather than overlapping the next.
// Views end at lease expiry or when the agent's run context ends, whichever is first.

var errLiveCap = errors.New("too many realtime views on this host; close one and retry")

type liveSlot struct {
	bucket int64
	values map[string]float64
}

type liveView struct {
	name, vmID string
	expires    time.Time
	series     []seriesMeta
	slots      []liveSlot // indexed by bucket modulo len
	prev       counters
}

type liveViews struct {
	mu      sync.Mutex
	ctx     context.Context // the agent run context, set by the sampler; views stop with it
	sampler *usageSampler
	views   map[string]*liveView
	lease   time.Duration
	tick    time.Duration
	running sync.WaitGroup
}

func newLiveViews(u *usageSampler) *liveViews {
	return &liveViews{ctx: context.Background(), sampler: u, views: map[string]*liveView{}, lease: protocol.StatsLiveLease, tick: protocol.StatsRanges["live"].Step}
}

func (l *liveViews) bucket(at time.Time) int64 {
	return (at.UnixMilli() + l.tick.Milliseconds()/2) / l.tick.Milliseconds()
}

// known reports whether libvirt knows a VM that has no stored history, so a live query for a
// defined but never-started VM is answered with nulls and an unknown VM with 404.
func (l *liveViews) known(ctx context.Context, name, vmID string) bool {
	l.mu.Lock()
	_, viewing := l.views[name]
	l.mu.Unlock()
	if viewing {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := l.sampler.virsh(ctx, "domstate", vmID)
	return err == nil
}

// query starts or renews a view and returns its ring, ending with the current tick.
func (l *liveViews) query(name, vmID string, seed []seriesMeta, now time.Time) (time.Time, []protocol.StatsSeries, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	v := l.views[name]
	if v == nil {
		if len(l.views) >= protocol.StatsLiveObjects {
			return time.Time{}, nil, errLiveCap
		}
		v = &liveView{name: name, vmID: vmID, series: seed, slots: make([]liveSlot, protocol.StatsRanges["live"].Points)}
		l.views[name] = v
		l.running.Add(1)
		go l.run(l.ctx, v)
	}
	v.expires = now.Add(l.lease)
	points := int64(len(v.slots))
	// End at the newest tick that can have been written: the tick now falls in, or the one before
	// it while this tick's sample is still being taken. Rounding to the nearest tick would end
	// the window on a second nobody sampled yet and show the newest point as null.
	last := now.UnixMilli() / l.tick.Milliseconds()
	if v.slots[last%points].bucket != last {
		last--
	}
	first := last - points + 1
	out := make([]protocol.StatsSeries, 0, len(v.series))
	for _, s := range v.series {
		values := make([]*float64, points)
		for b := first; b <= last; b++ {
			slot := v.slots[b%points]
			if value, ok := slot.values[s.Key]; ok && slot.bucket == b && !math.IsNaN(value) {
				value = math.Round(value*1000) / 1000
				values[b-first] = &value
			}
		}
		out = append(out, protocol.StatsSeries{Key: s.Key, Group: s.Group, Label: s.Label, Unit: s.Unit, Values: values})
	}
	return time.UnixMilli(first * l.tick.Milliseconds()).UTC(), out, nil
}

func (l *liveViews) run(ctx context.Context, v *liveView) {
	defer l.running.Done()
	for {
		timer := time.NewTimer(time.Until(time.Now().Truncate(l.tick).Add(l.tick)))
		stopped := false
		select {
		case <-ctx.Done():
			timer.Stop()
			stopped = true
		case <-timer.C:
		}
		l.mu.Lock()
		if stopped || time.Now().After(v.expires) {
			if l.views[v.name] == v {
				delete(l.views, v.name)
			}
			l.mu.Unlock()
			return
		}
		prev := v.prev
		l.mu.Unlock()
		at := time.Now() // before the call; the counters describe this moment
		current, points := l.sample(v, at, prev)
		l.mu.Lock()
		v.prev = current
		slot := liveSlot{bucket: l.bucket(at), values: map[string]float64{}}
		for _, p := range points {
			known := false
			for _, s := range v.series {
				known = known || s.Key == p.Key
			}
			if !known && len(v.series) >= historyMaxSeries {
				continue
			}
			if !known {
				v.series = append(v.series, p.seriesMeta)
			}
			slot.values[p.Key] = p.Value
		}
		v.slots[slot.bucket%int64(len(v.slots))] = slot
		l.mu.Unlock()
	}
}

// sample reads one tick. Only a domain libvirt reports running (state.state=1,
// VIR_DOMAIN_RUNNING) is measured. libvirt 11 answers cpu.time and balloon values for a shut-off
// domain too (BUG L5), so their presence proves nothing; a paused domain is not doing work
// either. Anything else, a missing state, or a VM libvirt cannot answer for records an empty
// tick (nulls) and forgets its counters, so the next rate after it runs again is unknown.
func (l *liveViews) sample(v *liveView, at time.Time, prev counters) (counters, []historyPoint) {
	if v.vmID == "" || v.name == "host" {
		return readHostPoints(at, prev)
	}
	ctx, cancel := context.WithDeadline(context.Background(), at.Add(l.tick*9/10))
	defer cancel()
	text, err := l.sampler.virsh(ctx, "domstats", "--raw", "--nowait", "--state", "--cpu-total", "--vcpu", "--balloon", "--block", "--interface", v.vmID)
	if err != nil {
		return counters{}, nil
	}
	for _, block := range splitDomainStats(text) {
		if domainFields(block)["state.state"] == "1" {
			return vmHistoryPoints(v.vmID, block, at, prev)
		}
	}
	return counters{}, nil
}
