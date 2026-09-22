// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"log"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
)

// Temperatures come from the kernel's hwmon class, read by the same 5 s host sampler as
// everything else. hwmon index numbers are assigned in probe order and change across boots, so
// a series key is built from the chip name and the device's sysfs basename instead.
const (
	hwmonMinCelsius = -40
	hwmonMaxCelsius = 150 // some chips report a huge value for an unconnected input
)

var hwmonBudgetWarned sync.Once

// hwmonLastMaxAge is how long the last completed read may stand in for a live one. Two sample
// steps covers a chip that misses a tick; past that the reading is no longer a temperature the
// operator can act on, so the series takes a gap instead of a frozen line.
const hwmonLastMaxAge = 2 * usageSampleInterval

// hwmonLast holds the sensors of the last completed read, so a chip that stops answering costs
// one sample rather than the caller's whole deadline. root is remembered with them because the
// hardware report and the sampler can be pointed at different trees, and at because a stalled
// chip must not keep publishing the same number for ever.
var hwmonLast struct {
	mu      sync.Mutex
	reading bool
	root    string
	at      time.Time
	sensors []hwmonSensor
}

// hwmonSensorsWithin reads the sensors on a background goroutine and returns the last known
// values when a chip does not answer within wait. An nvme or drivetemp reading is a command to
// the device, not a procfs read: a controller in reset or a disk in error recovery blocks until
// the driver gives up, tens of seconds later. Only one read is ever in flight, so a wedged chip
// cannot leave a goroutine behind on every tick — and a caller that found one already in flight
// does not wait on it at all, because the tick it would burn buys nothing.
func hwmonSensorsWithin(root string, wait time.Duration) []hwmonSensor {
	var done chan []hwmonSensor
	hwmonLast.mu.Lock()
	if !hwmonLast.reading {
		hwmonLast.reading = true
		done = make(chan []hwmonSensor, 1)
		go func() {
			sensors := readHwmonSensors(root)
			hwmonLast.mu.Lock()
			hwmonLast.reading, hwmonLast.root, hwmonLast.at, hwmonLast.sensors = false, root, time.Now(), sensors
			hwmonLast.mu.Unlock()
			done <- sensors
		}()
	}
	hwmonLast.mu.Unlock()

	if done != nil {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case sensors := <-done:
			return sensors
		case <-timer.C:
		}
	}
	hwmonLast.mu.Lock()
	defer hwmonLast.mu.Unlock()
	if hwmonLast.root != root || time.Since(hwmonLast.at) > hwmonLastMaxAge {
		return nil
	}
	return hwmonLast.sensors
}

// hwmonSensor is one temperature input. Celsius is NaN when the reading is missing, unreadable
// or implausible, which records a gap rather than a zero. Max and Crit are the chip's own
// thresholds, nil when it publishes none; the hardware report serves them beside the same key.
type hwmonSensor struct {
	Key     string
	Chip    string
	Label   string
	Celsius float64
	Max     *float64
	Crit    *float64
}

// readHwmonSensors walks root (/sys/class/hwmon) and returns at most protocol.HardwareMaxSensors
// sensors, ordered by key. Over budget the lowest-priority chips are dropped first, so the same
// sensors survive from one boot to the next and a storage host's disks cannot crowd out its CPU.
func readHwmonSensors(root string) []hwmonSensor {
	dirs, _ := filepath.Glob(filepath.Join(root, "hwmon*"))
	var sensors []hwmonSensor
	for _, dir := range dirs {
		chip := readSysfs(filepath.Join(dir, "name"))
		if chip == "" {
			continue
		}
		device, devicePath := "", ""
		if target, err := filepath.EvalSymlinks(filepath.Join(dir, "device")); err == nil {
			device, devicePath = filepath.Base(target), target
		}
		inputs, _ := filepath.Glob(filepath.Join(dir, "temp*_input"))
		sort.Slice(inputs, func(i, j int) bool { return hwmonIndex(inputs[i]) < hwmonIndex(inputs[j]) })
		for _, input := range inputs {
			n := strconv.Itoa(hwmonIndex(input))
			prefix := strings.TrimSuffix(input, "_input")
			label, ok := hwmonLabel(dir, chip, device, devicePath, n, readSysfs(prefix+"_label"), len(inputs))
			if !ok {
				continue
			}
			key := "temp." + chip + "." + n
			if device != "" {
				key = "temp." + chip + "-" + device + "." + n
			}
			sensors = append(sensors, hwmonSensor{
				Key:     key,
				Chip:    chip,
				Label:   label,
				Celsius: hwmonCelsius(input),
				Max:     hwmonThreshold(prefix + "_max"),
				Crit:    hwmonThreshold(prefix + "_crit"),
			})
		}
	}
	sort.SliceStable(sensors, func(i, j int) bool { return sensors[i].Key < sensors[j].Key })
	// Two chips can report the same name and expose no device link — several acpitz zones are
	// the common case — which would give their inputs one key, one history accumulator and one
	// row in the hardware report. Number the repeats so each input keeps its own series.
	seen := map[string]int{}
	for i := range sensors {
		seen[sensors[i].Key]++
		if n := seen[sensors[i].Key]; n > 1 {
			sensors[i].Key += "-" + strconv.Itoa(n)
		}
	}
	if len(sensors) > protocol.HardwareMaxSensors {
		hwmonBudgetWarned.Do(func() {
			log.Printf("usage history: more than %d temperature sensors; the lowest-priority ones are not kept", protocol.HardwareMaxSensors)
		})
		sort.SliceStable(sensors, func(i, j int) bool { return hwmonRank(sensors[i].Chip) < hwmonRank(sensors[j].Chip) })
		sensors = sensors[:protocol.HardwareMaxSensors]
		sort.SliceStable(sensors, func(i, j int) bool { return sensors[i].Key < sensors[j].Key })
	}
	return sensors
}

// hwmonRank orders the budget cut: CPU packages first, then NVMe, then everything else (PCI
// devices and board zones), then the per-drive disk sensors, of which a storage host can have
// more than the whole budget.
func hwmonRank(chip string) int {
	switch chip {
	case "coretemp", "k10temp":
		return 0
	case "nvme":
		return 1
	case "drivetemp":
		return 3
	}
	return 2
}

// hwmonLabel names the component a sensor belongs to and reports whether it is worth a series.
// Per-core CPU readings are skipped: one series per package is what an operator acts on, and the
// cores would crowd out every other sensor on a large host.
func hwmonLabel(dir, chip, device, devicePath, n, label string, inputs int) (string, bool) {
	switch chip {
	case "coretemp":
		if !strings.HasPrefix(label, "Package id ") {
			return "", false
		}
		return "CPU " + strings.TrimPrefix(label, "Package id ") + " package", true
	case "k10temp":
		if label != "Tctl" && label != "Tdie" {
			return "", false
		}
		return "CPU package", true
	case "drivetemp":
		if disks, _ := filepath.Glob(filepath.Join(dir, "device", "block", "*")); len(disks) > 0 {
			return filepath.Base(disks[0]), true
		}
	case "nvme":
		name := device
		if model := readSysfs(filepath.Join(dir, "device", "model")); model != "" {
			name += " (" + model + ")"
		}
		if label != "" && label != "Composite" {
			name += " " + label
		}
		return name, true
	}
	// A GPU or NIC sensor hangs off a PCI device. The address identifies it without asking
	// lspci; the hardware report resolves the same key to a vendor and model.
	if strings.Contains(devicePath, "/devices/pci") {
		name := chip + " " + device
		if label != "" {
			name += " " + label
		}
		return name, true
	}
	if label != "" {
		return label, true
	}
	if inputs > 1 {
		return chip + " " + n, true
	}
	return chip, true
}

// hwmonIndex is the N in .../tempN_input.
func hwmonIndex(path string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSuffix(filepath.Base(path), "_input"), "temp"))
	return n
}

// hwmonCelsius reads a millidegree file, returning NaN for anything unreadable or implausible.
func hwmonCelsius(path string) float64 {
	v, err := strconv.ParseFloat(readSysfs(path), 64)
	if err != nil {
		return math.NaN()
	}
	c := v / 1000
	if c < hwmonMinCelsius || c > hwmonMaxCelsius {
		return math.NaN()
	}
	return c
}

func hwmonThreshold(path string) *float64 {
	// Chips that publish no threshold for an input still expose the file, reading 0.
	c := hwmonCelsius(path)
	if math.IsNaN(c) || c <= 0 {
		return nil
	}
	return &c
}

func readSysfs(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
