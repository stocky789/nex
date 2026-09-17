// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"
	"nex.local/nex/internal/protocol"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Usage history is observed state kept on the host that sampled it: one file per object under
// <state-dir>/usage (host.rrd, vm-<uuid>.rrd), holding a fixed-length ring per series for each
// tier in protocol.StatsRanges. A Controller only relays queries; it keeps no copy, so a
// replacement Controller shows the same history.
//
// Each tier slot is the average of the raw 5 s samples that fell in its step, ignoring unknown
// (NaN) samples; a slot is unknown only when every sample was. The finest tier is memory-only,
// so a restart costs at most its ten minutes of 5 s detail.
//
// File layout: "NXU1", uint32 header length, header JSON (the series), then for tiers 1..3 an
// int64 current step index followed by each series' float32 ring, all little-endian. The whole
// file is written (temp file, fsync, rename) only when it is created or its series change.
// Otherwise a flush writes just the slots completed since the last one, then the step indexes,
// in place, without fsync. A crash between the two is not fully hidden: the old index hides the
// first new slot, but each later one sits where a step one ring-length earlier is still shown,
// so after restart those oldest visible steps show the newer values until the object's next
// sample marks the gap unknown (within seconds for the host and running VMs).
//
// ponytail: the running average of each unfinished step lives only in memory, so a crash or
// restart loses the unfinished hour and day steps' samples so far (an idle object is kept in
// memory until they finish). Persist accumulators if operators notice the dip.
//
// ponytail: averages only. A one-day point hides short spikes; add a max consolidation if
// operators need peak detection.
const (
	historyMaxVMs    = 256
	historyMaxSeries = 128
	historyRetention = 366 * 24 * time.Hour
	// A series with no data left that has not been sampled for this long is dropped when the
	// file's layout is next rewritten.
	historySeriesIdle = time.Hour
	historyMagic      = "NXU1"
)

// historyTiers is protocol.StatsRanges ordered finest first; tier 0 is never written to disk.
var historyTiers = [4]string{"10m", "24h", "30d", "1y"}

var historyVMID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type seriesMeta struct {
	Key   string `json:"key"`
	Group string `json:"group"`
	Label string `json:"label"`
	Unit  string `json:"unit"`
}

// historyPoint is one sampled value for one series; NaN is unknown.
type historyPoint struct {
	seriesMeta
	Value float64
}

type accumulator struct {
	Sum float64
	N   uint32
}

// ringFile is one object's history. rings[tier][series] holds Points slots indexed by step
// index modulo Points; Bucket[tier] is the step being accumulated in Acc.
type ringFile struct {
	Series    []seriesMeta
	Bucket    [4]int64
	Acc       [4][]accumulator
	rings     [4][][]float32
	seen      []time.Time // per series, when a point for it last arrived
	updated   time.Time
	recording bool              // sampled by this agent, so it counts toward historyMaxVMs
	layout    bool              // the whole file must be rewritten
	slots     map[[2]int64]bool // (tier, slot) completed since the last flush, tiers 1..3
	headerLen int64             // of the file as last written or read
}

func newRing() *ringFile {
	return &ringFile{layout: true, slots: map[[2]int64]bool{}}
}

func tierOf(t int) protocol.StatsTier { return protocol.StatsRanges[historyTiers[t]] }

// bucketOf is the step index holding a sample. The 5 s tier rounds to the nearest boundary, so
// a sampler aligned to 5 s boundaries lands one sample per step even when a tick runs a little
// early or late; coarser tiers take the step the sample falls in.
func bucketOf(at time.Time, t int) int64 {
	step := tierOf(t).Step.Milliseconds()
	ms := at.UnixMilli()
	if t == 0 {
		ms += step / 2
	}
	return ms / step
}

func nanRing(n int) []float32 {
	r := make([]float32, n)
	for i := range r {
		r[i] = float32(math.NaN())
	}
	return r
}

// index returns the series position for a key, adding it when there is room.
func (f *ringFile) index(p historyPoint, at time.Time) int {
	for i, s := range f.Series {
		if s.Key == p.Key {
			if s != p.seriesMeta {
				f.Series[i], f.layout = p.seriesMeta, true // labels may improve; the key is the identity
			}
			f.seen[i] = at
			return i
		}
	}
	if len(f.Series) >= historyMaxSeries {
		return -1
	}
	f.Series, f.seen, f.layout = append(f.Series, p.seriesMeta), append(f.seen, at), true
	for t := range f.rings {
		f.rings[t] = append(f.rings[t], nanRing(tierOf(t).Points))
		f.Acc[t] = append(f.Acc[t], accumulator{})
	}
	return len(f.Series) - 1
}

// add records one sample taken at `at`. It reports whether every series fit under the cap, and
// whether a stored tier ignored the sample because the clock is behind its current step.
func (f *ringFile) add(at time.Time, points []historyPoint) (complete, behind bool) {
	complete = true
	f.updated = at
	for t := range f.rings {
		bucket := bucketOf(at, t)
		switch {
		case f.Bucket[t] == 0:
			f.Bucket[t] = bucket
		case bucket == f.Bucket[t]-1:
			// A tick a moment behind the last; it is not accumulated below.
		case bucket < f.Bucket[t] && t > 0:
			// The clock went back by more than a step, as when a host samples before time sync
			// and is then corrected. Which clock was right is unknown, so stored history is never
			// wiped for it: the tier ignores samples (below) until time reaches its step again.
			// ponytail: a clock that was far ahead and is corrected pauses a tier for as long as
			// it was ahead; add an operator reset if that is ever seen.
			behind = true
		case bucket < f.Bucket[t]:
			f.reset(t, bucket) // the memory-only 5 s tier just starts again
		case bucket > f.Bucket[t]:
			f.finish(t, bucket)
		}
	}
	for _, p := range points {
		i := f.index(p, at)
		if i < 0 {
			complete = false
			continue
		}
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue
		}
		for t := range f.rings {
			if bucketOf(at, t) == f.Bucket[t] {
				f.Acc[t][i].Sum += p.Value
				f.Acc[t][i].N++
			}
		}
	}
	return complete, behind
}

func (f *ringFile) reset(t int, bucket int64) {
	for i := range f.Series {
		f.rings[t][i], f.Acc[t][i] = nanRing(tierOf(t).Points), accumulator{}
	}
	f.Bucket[t] = bucket
}

// finish writes the accumulated step into its slot, marks skipped steps unknown and starts next.
func (f *ringFile) finish(t int, next int64) {
	points := int64(tierOf(t).Points)
	mark := func(b int64) {
		if t > 0 {
			f.slots[[2]int64{int64(t), b % points}] = true
		}
	}
	for i := range f.Series {
		v := float32(math.NaN())
		if a := f.Acc[t][i]; a.N > 0 {
			v = float32(a.Sum / float64(a.N))
		}
		f.rings[t][i][f.Bucket[t]%points] = v
		for b := f.Bucket[t] + 1; b < next && b <= f.Bucket[t]+points; b++ {
			f.rings[t][i][b%points] = float32(math.NaN())
		}
		f.Acc[t][i] = accumulator{}
	}
	mark(f.Bucket[t])
	for b := f.Bucket[t] + 1; b < next && b <= f.Bucket[t]+points; b++ {
		mark(b)
	}
	f.Bucket[t] = next
}

// pending reports whether any tier has accumulated samples for its unfinished step.
func (f *ringFile) pending() bool {
	for t := range f.Acc {
		for _, a := range f.Acc[t] {
			if a.N > 0 {
				return true
			}
		}
	}
	return false
}

// advance finishes every step with samples whose end has passed, so a VM that stopped still
// gets its last minute, hour and day written. A tier with nothing accumulated is left alone:
// finishing it would only write unknown slots for a stopped VM every minute, and the query and
// the next sample already treat steps after Bucket as unknown.
func (f *ringFile) advance(now time.Time) {
	for t := range f.rings {
		pending := false
		for _, a := range f.Acc[t] {
			pending = pending || a.N > 0
		}
		if b := bucketOf(now, t); pending && f.Bucket[t] != 0 && b > f.Bucket[t] {
			f.finish(t, b)
		}
	}
}

// query returns the range ending with the step containing now; the current step is its
// running average.
func (f *ringFile) query(rangeName string, now time.Time) (time.Time, []protocol.StatsSeries) {
	t := 0
	for i, name := range historyTiers {
		if name == rangeName {
			t = i
		}
	}
	f.advance(now)
	tier := tierOf(t)
	points := int64(tier.Points)
	last := bucketOf(now, t)
	first := last - points + 1
	out := make([]protocol.StatsSeries, 0, len(f.Series))
	for i, s := range f.Series {
		values := make([]*float64, points)
		for b := first; b <= last; b++ {
			v := math.NaN()
			switch {
			case b == f.Bucket[t] && f.Acc[t][i].N > 0:
				v = f.Acc[t][i].Sum / float64(f.Acc[t][i].N)
			case b < f.Bucket[t] && b > f.Bucket[t]-points:
				v = float64(f.rings[t][i][b%points])
			}
			if !math.IsNaN(v) {
				v = math.Round(v*1000) / 1000
				values[b-first] = &v
			}
		}
		out = append(out, protocol.StatsSeries{Key: s.Key, Group: s.Group, Label: s.Label, Unit: s.Unit, Values: values})
	}
	return time.UnixMilli(first * tier.Step.Milliseconds()).UTC(), out
}

// compact drops series with nothing left in any tier that have not been sampled recently, such
// as a removed disk whose data has aged out, so series churn cannot fill the cap for good. A
// series still being sampled keeps its place even while all its values are unknown.
func (f *ringFile) compact(now time.Time) {
	keep := 0
	for i := range f.Series {
		empty := now.Sub(f.seen[i]) > historySeriesIdle
		for t := range f.rings {
			if f.Acc[t][i].N > 0 {
				empty = false
			}
			for _, v := range f.rings[t][i] {
				if !empty {
					break
				}
				empty = math.IsNaN(float64(v))
			}
		}
		if empty {
			f.layout = true
			continue
		}
		f.Series[keep], f.seen[keep] = f.Series[i], f.seen[i]
		for t := range f.rings {
			f.rings[t][keep], f.Acc[t][keep] = f.rings[t][i], f.Acc[t][i]
		}
		keep++
	}
	f.Series, f.seen = f.Series[:keep], f.seen[:keep]
	for t := range f.rings {
		f.rings[t], f.Acc[t] = f.rings[t][:keep], f.Acc[t][:keep]
	}
}

// tierOffset is where tier t's step index starts in the file.
func (f *ringFile) tierOffset(t int) int64 {
	off := f.headerLen
	for i := 1; i < t; i++ {
		off += 8 + int64(len(f.Series))*int64(tierOf(i).Points)*4
	}
	return off
}

// encode serializes the whole file and records its header length.
func (f *ringFile) encode() []byte {
	series, _ := json.Marshal(f.Series)
	var b bytes.Buffer
	b.WriteString(historyMagic)
	_ = binary.Write(&b, binary.LittleEndian, uint32(len(series)))
	b.Write(series)
	f.headerLen = int64(b.Len())
	for t := 1; t < len(f.rings); t++ {
		_ = binary.Write(&b, binary.LittleEndian, f.Bucket[t])
		for _, r := range f.rings[t] {
			_ = binary.Write(&b, binary.LittleEndian, r)
		}
	}
	return b.Bytes()
}

func decodeRing(data []byte, now time.Time) (*ringFile, error) {
	if len(data) < 8 || string(data[:4]) != historyMagic {
		return nil, errors.New("not a usage history file")
	}
	n := int64(binary.LittleEndian.Uint32(data[4:8]))
	if n > int64(len(data))-8 {
		return nil, errors.New("usage history header truncated")
	}
	f := newRing()
	f.layout = false
	if err := json.Unmarshal(data[8:8+n], &f.Series); err != nil {
		return nil, err
	}
	if len(f.Series) > historyMaxSeries {
		return nil, errors.New("usage history file has too many series")
	}
	f.headerLen = 8 + n
	size := f.tierOffset(len(f.rings))
	if int64(len(data)) != size {
		return nil, errors.New("usage history file has the wrong size")
	}
	f.seen = make([]time.Time, len(f.Series))
	for i := range f.seen {
		f.seen[i] = now
	}
	for t := range f.rings {
		f.Acc[t] = make([]accumulator, len(f.Series))
		f.rings[t] = make([][]float32, len(f.Series))
		if t == 0 {
			for i := range f.rings[t] {
				f.rings[t][i] = nanRing(tierOf(t).Points)
			}
			continue
		}
		r := bytes.NewReader(data[f.tierOffset(t):])
		_ = binary.Read(r, binary.LittleEndian, &f.Bucket[t])
		for i := range f.rings[t] {
			f.rings[t][i] = make([]float32, tierOf(t).Points)
			_ = binary.Read(r, binary.LittleEndian, f.rings[t][i])
		}
	}
	return f, nil
}

// usageHistory owns every object's rings on this host.
type usageHistory struct {
	mu      sync.Mutex
	dir     string
	hostID  string
	objects map[string]*ringFile // "host" or "vm-<uuid>"
	warned  map[string]bool      // one log line per refused object or series cap
	live    *liveViews           // realtime views; nil until a sampler is attached
}

func newUsageHistory(dir, hostID string) *usageHistory {
	return &usageHistory{dir: dir, hostID: hostID, objects: map[string]*ringFile{}, warned: map[string]bool{}}
}

func (h *usageHistory) warn(key, format string, args ...any) {
	if !h.warned[key] {
		h.warned[key] = true
		log.Printf(format, args...)
	}
}

// load returns an object's rings, reading its file on first use. create adds a new object
// when there is no usable file; without it such an object is nil.
func (h *usageHistory) load(name string, create bool) *ringFile {
	if f := h.objects[name]; f != nil {
		return f
	}
	data, err := os.ReadFile(filepath.Join(h.dir, name+".rrd"))
	var f *ringFile
	if err == nil {
		if f, err = decodeRing(data, time.Now()); err != nil {
			// A damaged file is replaced rather than blocking all future history for the object.
			log.Printf("usage history for %s is unreadable and starts again: %v", name, err)
		}
	}
	if f == nil {
		if !create {
			return nil
		}
		f = newRing()
	}
	h.objects[name] = f
	return f
}

// record adds one sample for an object. Only objects sampled in the last hour count toward the
// VM cap, so neither a query for a stopped VM nor a stopped VM kept in memory for its open day
// step can keep a running one from being recorded.
func (h *usageHistory) record(name string, at time.Time, points []historyPoint) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if f := h.objects[name]; name != "host" && (f == nil || !f.recording || at.Sub(f.updated) > time.Hour) {
		recording := 0
		for key, o := range h.objects {
			if key != "host" && o.recording && at.Sub(o.updated) <= time.Hour {
				recording++
			}
		}
		if recording >= historyMaxVMs {
			h.warn("vms", "usage history: more than %d VMs; history is not kept for the rest", historyMaxVMs)
			return
		}
	}
	f := h.load(name, true)
	f.recording = true
	complete, behind := f.add(at, points)
	if !complete {
		h.warn("series-"+name, "usage history for %s: more than %d series; the rest are not kept", name, historyMaxSeries)
	}
	if behind {
		h.warn("clock-"+name, "usage history for %s: stored history is ahead of the clock, as after a clock that ran ahead was corrected; the 24h, 30d and 1y ranges store nothing until time reaches it", name)
	}
}

// forgetDeletedVM removes the history of a VM whose delete command succeeded.
func forgetDeletedVM(h *usageHistory, entry JournalEntry) {
	if entry.Status == "succeeded" && entry.Command != nil && entry.Command.Kind == "vm-delete" && entry.Command.Delete != nil {
		h.forget(entry.Command.Delete.VMID)
	}
}

// forget removes a deleted VM's history.
func (h *usageHistory) forget(vmID string) {
	id := strings.ToLower(vmID)
	if !historyVMID.MatchString(id) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.objects, "vm-"+id)
	if err := os.Remove(filepath.Join(h.dir, "vm-"+id+".rrd")); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("usage history for deleted VM %s could not be removed: %v", vmID, err)
	}
}

type slotWrite struct {
	off  int64
	data []byte
}

type flushJob struct {
	name   string
	f      *ringFile
	whole  []byte
	writes []slotWrite
}

// flush saves what changed. Work is collected under the lock and written outside it, so a slow
// disk never holds up a query. It also drops objects no longer sampled, removes files untouched
// for longer than the longest tier (by then they hold no data) and leftover temp files.
func (h *usageHistory) flush(now time.Time) {
	if err := os.MkdirAll(h.dir, 0700); err != nil {
		h.mu.Lock()
		h.warn("dir", "usage history directory unavailable: %v", err)
		h.mu.Unlock()
		return
	}
	var jobs []flushJob
	h.mu.Lock()
	for name, f := range h.objects {
		f.advance(now)
		switch {
		case f.layout:
			f.compact(now)
			jobs = append(jobs, flushJob{name: name, f: f, whole: f.encode()})
		case len(f.slots) > 0:
			job := flushJob{name: name, f: f}
			tiers := map[int]bool{}
			for key := range f.slots {
				t, slot := int(key[0]), key[1]
				tiers[t] = true
				points := int64(tierOf(t).Points)
				for i := range f.Series {
					b := make([]byte, 4)
					binary.LittleEndian.PutUint32(b, math.Float32bits(f.rings[t][i][slot]))
					job.writes = append(job.writes, slotWrite{f.tierOffset(t) + 8 + int64(i)*points*4 + slot*4, b})
				}
			}
			for t := range tiers {
				b := make([]byte, 8)
				binary.LittleEndian.PutUint64(b, uint64(f.Bucket[t]))
				job.writes = append(job.writes, slotWrite{f.tierOffset(t), b})
			}
			jobs = append(jobs, job)
		case now.Sub(f.updated) > time.Hour && !f.pending():
			// Reread when queried or sampled again. An open step (a stopped VM's day) keeps the
			// object until advance finishes it, since accumulators are memory-only.
			delete(h.objects, name)
			continue
		}
		f.layout, f.slots = false, map[[2]int64]bool{}
	}
	h.mu.Unlock()
	for _, job := range jobs {
		path := filepath.Join(h.dir, job.name+".rrd")
		var err error
		if job.whole != nil {
			err = h.replace(job, path)
		} else {
			err = writeSlots(path, job.writes)
		}
		if err != nil {
			log.Printf("usage history for %s was not saved: %v", job.name, err)
			h.mu.Lock()
			job.f.layout = true // rewrite it whole next time
			h.mu.Unlock()
		}
	}
	entries, _ := os.ReadDir(h.dir)
	for _, e := range entries {
		info, err := e.Info()
		switch {
		case err != nil:
		case strings.HasPrefix(e.Name(), ".tmp-"):
			_ = os.Remove(filepath.Join(h.dir, e.Name())) // flush is the only writer
		case strings.HasPrefix(e.Name(), "vm-") && now.Sub(info.ModTime()) > historyRetention:
			h.mu.Lock()
			delete(h.objects, strings.TrimSuffix(e.Name(), ".rrd"))
			_ = os.Remove(filepath.Join(h.dir, e.Name()))
			h.mu.Unlock()
		}
	}
}

// replace writes a whole file atomically, unless the VM was deleted while it was being written.
func (h *usageHistory) replace(job flushJob, path string) error {
	tmp, err := os.CreateTemp(h.dir, ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = tmp.Write(job.whole)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.objects[job.name] != job.f {
		return nil
	}
	return os.Rename(tmp.Name(), path)
}

func writeSlots(path string, writes []slotWrite) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	for _, w := range writes {
		if _, err = file.WriteAt(w.data, w.off); err != nil {
			break
		}
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	return err
}

// ServeHTTP answers GET /stats?object=vm|host&id=<uuid>&range=<range>.
func (h *usageHistory) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fail := func(status int, message string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
	}
	if r.Method != http.MethodGet || r.URL.Path != "/stats" {
		fail(http.StatusNotFound, "not found")
		return
	}
	q := r.URL.Query()
	rangeName := q.Get("range")
	if _, ok := protocol.StatsRanges[rangeName]; !ok {
		fail(http.StatusBadRequest, "range must be live, 10m, 24h, 30d or 1y")
		return
	}
	history := protocol.StatsHistory{Object: q.Get("object"), Range: rangeName, StepSeconds: int(protocol.StatsRanges[rangeName].Step / time.Second)}
	name := "host"
	switch history.Object {
	case "host":
		history.ID = h.hostID
	case "vm":
		history.ID = strings.ToLower(q.Get("id"))
		if !historyVMID.MatchString(history.ID) {
			fail(http.StatusBadRequest, "id must be a VM UUID")
			return
		}
		name = "vm-" + history.ID
	default:
		fail(http.StatusBadRequest, "object must be vm or host")
		return
	}
	h.mu.Lock()
	f := h.load(name, false)
	if rangeName == "live" {
		var seed []seriesMeta
		if f != nil {
			seed = append(seed, f.Series...)
		}
		h.mu.Unlock()
		if h.live == nil || f == nil && name != "host" && !h.live.known(r.Context(), name, history.ID) {
			fail(http.StatusNotFound, "no usage history for this "+history.Object)
			return
		}
		var err error
		if history.Start, history.Series, err = h.live.query(name, history.ID, seed, time.Now()); err != nil {
			fail(http.StatusTooManyRequests, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(history)
		return
	}
	if f == nil {
		h.mu.Unlock()
		fail(http.StatusNotFound, "no usage history for this "+history.Object)
		return
	}
	history.Start, history.Series = f.query(rangeName, time.Now())
	h.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(history)
}
