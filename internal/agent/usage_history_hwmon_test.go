// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// hwmonChip writes one fake hwmon directory. files are paths inside it, so "temp1_input" is a
// reading and "device/model" is a file on the device the `device` symlink points at.
func hwmonChip(t *testing.T, sys string, index int, name, device string, files map[string]string) {
	t.Helper()
	dir := filepath.Join(sys, "class", "hwmon", fmt.Sprintf("hwmon%d", index))
	write := func(path, content string) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(dir, "name"), name)
	if device != "" {
		target := filepath.Join(sys, device)
		if err := os.MkdirAll(target, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "device")); err != nil {
			t.Fatal(err)
		}
	}
	for path, content := range files {
		write(filepath.Join(dir, path), content)
	}
}

func hwmonRoot(sys string) string { return filepath.Join(sys, "class", "hwmon") }

func sensorKeys(sensors []hwmonSensor) []string {
	keys := make([]string, len(sensors))
	for i, s := range sensors {
		keys[i] = s.Key
	}
	return keys
}

// hwmon indexes are assigned in probe order, so the same chips must keep the same keys when the
// kernel numbers them differently after a reboot.
func TestHwmonKeysIgnoreTheHwmonIndex(t *testing.T) {
	build := func(coretemp, nvme int) []string {
		sys := t.TempDir()
		hwmonChip(t, sys, coretemp, "coretemp", "devices/platform/coretemp.0", map[string]string{
			"temp1_input": "45000", "temp1_label": "Package id 0",
		})
		hwmonChip(t, sys, nvme, "nvme", "devices/pci0000:00/nvme/nvme0", map[string]string{
			"temp1_input": "38000", "temp1_label": "Composite",
		})
		return sensorKeys(readHwmonSensors(hwmonRoot(sys)))
	}
	before, after := build(0, 3), build(4, 1)
	want := []string{"temp.coretemp-coretemp.0.1", "temp.nvme-nvme0.1"}
	if fmt.Sprint(before) != fmt.Sprint(want) || fmt.Sprint(after) != fmt.Sprint(want) {
		t.Fatalf("keys = %v then %v, want %v", before, after, want)
	}
}

func TestHwmonLabelsFollowTheDriver(t *testing.T) {
	sys := t.TempDir()
	hwmonChip(t, sys, 0, "coretemp", "devices/platform/coretemp.0", map[string]string{
		"temp1_input": "45000", "temp1_label": "Package id 0",
		"temp2_input": "44000", "temp2_label": "Core 0", // per-core: no series
	})
	hwmonChip(t, sys, 1, "k10temp", "devices/pci0000:00/0000:00:18.3", map[string]string{
		"temp1_input": "52000", "temp1_label": "Tctl",
		"temp2_input": "51000", "temp2_label": "Tccd1",
	})
	hwmonChip(t, sys, 2, "drivetemp", "devices/scsi/4:0:0:0", map[string]string{
		"temp1_input": "31000", "device/block/sda/uevent": "",
	})
	hwmonChip(t, sys, 3, "nvme", "devices/pci0000:00/nvme/nvme0", map[string]string{
		"temp1_input": "38000", "temp1_label": "Composite", "device/model": "EXAMPLE NVMe 1TB",
	})
	hwmonChip(t, sys, 4, "amdgpu", "devices/pci0000:00/0000:03:00.0", map[string]string{
		"temp1_input": "61000", "temp1_label": "edge",
	})
	hwmonChip(t, sys, 5, "acpitz", "", map[string]string{"temp1_input": "40000"})

	want := map[string]string{
		"temp.coretemp-coretemp.0.1":  "CPU 0 package",
		"temp.k10temp-0000:00:18.3.1": "CPU package",
		"temp.drivetemp-4:0:0:0.1":    "sda",
		"temp.nvme-nvme0.1":           "nvme0 (EXAMPLE NVMe 1TB)",
		"temp.amdgpu-0000:03:00.0.1":  "amdgpu 0000:03:00.0 edge",
		"temp.acpitz.1":               "acpitz",
	}
	got := map[string]string{}
	for _, s := range readHwmonSensors(hwmonRoot(sys)) {
		got[s.Key] = s.Label
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sensors = %v, want %v", got, want)
	}
}

// An unconnected or failing input must record nothing rather than a zero, and must not take its
// thresholds from the same bad numbers.
func TestHwmonImplausibleAndUnreadableReadingsAreGaps(t *testing.T) {
	sys := t.TempDir()
	hwmonChip(t, sys, 0, "nct6795", "devices/platform/nct6795.656", map[string]string{
		"temp1_input": "45000", "temp1_label": "SYSTIN", "temp1_max": "80000", "temp1_crit": "95000",
		"temp2_input": "-6000000", "temp2_label": "AUXTIN0", "temp2_crit": "0",
		"temp3_input": "not a number", "temp3_label": "AUXTIN1",
	})
	sensors := readHwmonSensors(hwmonRoot(sys))
	if len(sensors) != 3 {
		t.Fatalf("sensors = %v", sensors)
	}
	if sensors[0].Celsius != 45 || sensors[0].Max == nil || *sensors[0].Max != 80 || sensors[0].Crit == nil || *sensors[0].Crit != 95 {
		t.Fatalf("good sensor = %+v", sensors[0])
	}
	for _, s := range sensors[1:] {
		if !math.IsNaN(s.Celsius) || s.Crit != nil {
			t.Fatalf("%s = %+v, want a gap and no threshold", s.Key, s)
		}
	}
	// A gap reaches the history file as no sample at all.
	f := newRing()
	at := time.Now()
	f.add(at, []historyPoint{{seriesMeta{sensors[1].Key, "temperature", sensors[1].Label, "celsius"}, sensors[1].Celsius}})
	if f.Acc[0][0].N != 0 {
		t.Fatalf("recorded %v samples for an implausible reading", f.Acc[0][0].N)
	}
}

// Two chips of the same name with no device link would otherwise share a key, which sums two
// sensors into one history accumulator and lists one of them twice in the hardware report.
func TestHwmonKeysAreUniquePerInput(t *testing.T) {
	sys := t.TempDir()
	for i := 0; i < 3; i++ {
		hwmonChip(t, sys, i, "acpitz", "", map[string]string{"temp1_input": "40000"})
	}
	keys := sensorKeys(readHwmonSensors(hwmonRoot(sys)))
	want := []string{"temp.acpitz.1", "temp.acpitz.1-2", "temp.acpitz.1-3"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

// A big host with many sensors keeps the 24-sensor budget and every fixed series, and the
// series file overflows into the per-core CPU points rather than into the temperatures. The
// fixture is deliberately over the 128-series cap: with the temperatures behind the cores,
// the sensors would be the series that are dropped and this test fails.
func TestHostHistoryPointsKeepTemperaturesAheadOfCores(t *testing.T) {
	sys := t.TempDir()
	for i := 0; i < 30; i++ {
		hwmonChip(t, sys, i, fmt.Sprintf("chip%02d", i), fmt.Sprintf("devices/platform/chip%02d", i),
			map[string]string{"temp1_input": "40000"})
	}
	sensors := readHwmonSensors(hwmonRoot(sys))
	if len(sensors) != protocol.HardwareMaxSensors {
		t.Fatalf("sensors = %d, want %d", len(sensors), protocol.HardwareMaxSensors)
	}
	var stat strings.Builder
	stat.WriteString("cpu  1 0 0 1 0 0 0 0\n")
	for i := 0; i < 64; i++ {
		fmt.Fprintf(&stat, "cpu%d 1 0 0 1 0 0 0 0\n", i)
	}
	var diskstats strings.Builder
	for i := 0; i < 12; i++ {
		fmt.Fprintf(&diskstats, "   8 %d sd%c 1 2 3 4 5 6 7 8 9 10 11\n", i*16, 'a'+i)
	}
	_, points := hostHistoryPoints(stat.String(), "MemTotal: 1 kB\nMemAvailable: 1 kB\n", diskstats.String(), "0 0 0",
		[]string{"eno1"}, map[string][2]string{}, sensors, func(string) bool { return true }, time.Now(), counters{})
	f := newRing()
	if complete, _ := f.add(time.Now(), points); complete {
		t.Fatalf("%d host series fit the file cap; the fixture no longer forces a choice", len(points))
	}
	temps, cores := 0, 0
	for _, s := range f.Series {
		switch {
		case s.Group == "temperature":
			if s.Unit != "celsius" {
				t.Fatalf("%s unit = %s", s.Key, s.Unit)
			}
			temps++
		case strings.HasPrefix(s.Key, "cpu.core."):
			cores++
		}
	}
	keys := fmt.Sprint(f.Series)
	if temps != protocol.HardwareMaxSensors || !strings.Contains(keys, "memory.used") || !strings.Contains(keys, "load.1") || !strings.Contains(keys, "cpu.total") {
		t.Fatalf("temperature series = %d, series = %s", temps, keys)
	}
	if cores == 0 || cores >= usageMaxCPUSeries {
		t.Fatalf("core series = %d, want the dropped series to be cores", cores)
	}
}

// A wedged nvme or disk sensor must cost one sample, not the sampler: the read continues in the
// background, the caller gets the last known values back inside its bound, and no tick leaves a
// goroutine behind.
func TestHwmonSensorsWithinReturnsLastValuesWhenAChipStalls(t *testing.T) {
	sys := t.TempDir()
	hwmonChip(t, sys, 0, "nvme", "devices/pci0000:00/nvme/nvme0", map[string]string{"temp1_input": "38000"})
	root := hwmonRoot(sys)
	if got := hwmonSensorsWithin(root, time.Minute); len(got) != 1 || got[0].Celsius != 38 {
		t.Fatalf("first read = %+v", got)
	}

	// A fifo stands in for a device that stops answering: the read blocks in open(2) until
	// something opens the other end, which the cleanup below does once the checks are done.
	input := filepath.Join(root, "hwmon0", "temp1_input")
	if err := os.Remove(input); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(input, 0o644); err != nil {
		t.Fatal(err)
	}
	defer func() {
		w, err := os.OpenFile(input, os.O_WRONLY, 0)
		if err != nil {
			t.Error(err)
			return
		}
		w.Close()
		// Leave no read in flight, or the next test's read is the one that waits.
		for {
			hwmonLast.mu.Lock()
			reading := hwmonLast.reading
			hwmonLast.mu.Unlock()
			if !reading {
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	goroutines := runtime.NumGoroutine()
	const wait = 200 * time.Millisecond
	for i := 0; i < 3; i++ {
		start := time.Now()
		got := hwmonSensorsWithin(root, wait)
		elapsed := time.Since(start)
		// The first of these starts the read that then stalls, so it pays the bound once.
		// Every later one finds that read already in flight and must not wait on it at
		// all: waiting buys nothing and costs the sampler its whole deadline each tick.
		if i == 0 && elapsed > time.Second {
			t.Fatalf("the stalling read took %s, want the bound to cut it short", elapsed)
		}
		if i > 0 && elapsed > wait/4 {
			t.Fatalf("read %d took %s while a read was already in flight, want it to return at once", i, elapsed)
		}
		if len(got) != 1 || got[0].Celsius != 38 {
			t.Fatalf("read %d = %+v, want the last known values", i, got)
		}
	}
	if extra := runtime.NumGoroutine() - goroutines; extra > 1 {
		t.Fatalf("%d extra goroutines after three stalled reads, want one read in flight", extra)
	}

	// A chip that never comes back must produce a gap rather than the same reading for ever.
	hwmonLast.mu.Lock()
	hwmonLast.at = time.Now().Add(-hwmonLastMaxAge - time.Second)
	hwmonLast.mu.Unlock()
	if got := hwmonSensorsWithin(root, wait); got != nil {
		t.Fatalf("a reading older than %s was still served: %+v", hwmonLastMaxAge, got)
	}
}

// drivetemp gives a storage host one sensor per disk, more than the whole budget on its own.
// The CPU package and NVMe readings the feature exists for must survive the cut, and the
// sensors that do survive stay key-sorted so the same ones are kept from boot to boot.
func TestHwmonBudgetKeepsCPUAndNVMeAheadOfDisks(t *testing.T) {
	sys := t.TempDir()
	for i := 0; i < 26; i++ {
		hwmonChip(t, sys, i, "drivetemp", fmt.Sprintf("devices/scsi/%d:0:0:0", i), map[string]string{
			"temp1_input": "31000", fmt.Sprintf("device/block/sd%c/uevent", 'a'+i): "",
		})
	}
	hwmonChip(t, sys, 26, "k10temp", "devices/pci0000:00/0000:00:18.3", map[string]string{
		"temp1_input": "52000", "temp1_label": "Tctl",
	})
	hwmonChip(t, sys, 27, "nvme", "devices/pci0000:00/nvme/nvme0", map[string]string{
		"temp1_input": "38000", "temp1_label": "Composite",
	})
	keys := sensorKeys(readHwmonSensors(hwmonRoot(sys)))
	if len(keys) != protocol.HardwareMaxSensors {
		t.Fatalf("sensors = %d, want %d", len(keys), protocol.HardwareMaxSensors)
	}
	if !slices.Contains(keys, "temp.k10temp-0000:00:18.3.1") || !slices.Contains(keys, "temp.nvme-nvme0.1") {
		t.Fatalf("keys = %v, want the CPU package and NVMe sensors kept", keys)
	}
	if !slices.IsSorted(keys) {
		t.Fatalf("keys = %v, want the kept sensors ordered by key", keys)
	}
}

// A host upgraded to this build can already be at the series cap with per-core CPU series,
// which compact never frees because they are still sampled. A temperature series must still
// get in, or that host shows no temperature history at all.
func TestRingFileAtSeriesCapEvictsACoreForATemperature(t *testing.T) {
	at := time.Now()
	points := []historyPoint{point("cpu.total", 1)}
	for i := 0; len(points) < historyMaxSeries; i++ {
		points = append(points, historyPoint{seriesMeta{fmt.Sprintf("cpu.core.%d", i), "cpu", "CPU", "percent"}, 1})
	}
	f := newRing()
	if complete, _ := f.add(at, points); !complete || len(f.Series) != historyMaxSeries {
		t.Fatalf("fixture filled %d of %d series", len(f.Series), historyMaxSeries)
	}
	temp := historyPoint{seriesMeta{"temp.k10temp.1", "temperature", "CPU package", "celsius"}, 52}

	f.add(at, append(points, temp))

	keys := sensorMetaKeys(f.Series)
	if len(f.Series) != historyMaxSeries || !slices.Contains(keys, "temp.k10temp.1") {
		t.Fatalf("%d series, %v; want the temperature series in a full file", len(f.Series), keys)
	}
	if !slices.Contains(keys, "cpu.total") {
		t.Fatal("a fixed series was evicted instead of a core")
	}
	// The displaced core is offered again and must stay out rather than evict its neighbour.
	f.add(at, append(points, temp))
	if after := sensorMetaKeys(f.Series); !slices.Equal(after, keys) {
		t.Fatalf("a second sample moved the series: %v, want %v", after, keys)
	}
}

func sensorMetaKeys(series []seriesMeta) []string {
	keys := make([]string, len(series))
	for i, s := range series {
		keys[i] = s.Key
	}
	return keys
}
