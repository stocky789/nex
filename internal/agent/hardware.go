// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

// Hardware is read live from the host whenever the Controller asks for it. Nothing here is
// cached, sampled or persisted: a host is the only record of the machine it runs on, so a
// fresh Controller sees the same report as the one it replaced.
//
// Each section reads its own source and reports itself unavailable with a reason when that
// source is missing or fails, so one absent tool never empties the whole report.
// The whole report is bounded well inside the Controller's channel deadline, and each tool
// gets a shorter slice of that: a tool that hangs costs its own section a reason, not the
// whole request a timeout the operator can only retry into another timeout.
const (
	hardwareReportWait = 6 * time.Second
	hardwareToolWait   = 2 * time.Second
)

// readHardware observes this host. root is "/" in production and a fixture tree in tests;
// the external tools are always the ones on PATH.
func readHardware(ctx context.Context, root string) protocol.HostHardware {
	ctx, cancel := context.WithTimeout(ctx, hardwareReportWait)
	defer cancel()
	hw := protocol.HostHardware{ObservedAt: time.Now().UTC()}
	hw.System = readDMI(filepath.Join(root, "sys/class/dmi/id"))
	hw.CPU = readCPUInfo(filepath.Join(root, "proc/cpuinfo"))
	hw.Memory = readMemory(ctx, filepath.Join(root, "proc/meminfo"))
	hw.Disks = readDisks(ctx)
	hw.PCI = readPCI(ctx)
	hw.Sensors = readSensors(filepath.Join(root, "sys/class/hwmon"))
	return hw
}

// hardwareTool runs one inventory tool with a short deadline and bounded output. The tool's
// own error text is never returned: it can name paths, and the reason an operator needs is
// which tool failed.
func hardwareTool(ctx context.Context, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, hardwareToolWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = time.Second
	var out cappedOutput
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// toolReason says which tool could not be used. The tool's own error text is deliberately
// discarded: it can name paths, and which tool failed is what an operator acts on.
func toolReason(name string, err error) string {
	if errors.Is(err, exec.ErrNotFound) {
		return name + " is not installed"
	}
	return name + " failed"
}

// chassisTypes names the SMBIOS chassis types a server is likely to report; anything else
// keeps its number rather than inventing a name.
var chassisTypes = map[string]string{
	"1": "Other", "3": "Desktop", "4": "Low Profile Desktop", "6": "Mini Tower",
	"7": "Tower", "8": "Portable", "9": "Laptop", "10": "Notebook", "13": "All In One",
	"17": "Main Server Chassis", "23": "Rack Mount Chassis", "25": "Multi-system Chassis",
	"28": "Blade", "29": "Blade Enclosure", "32": "Detachable",
}

func readDMI(dir string) protocol.HardwareSystem {
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return protocol.HardwareSystem{Unavailable: true,
			Reason: "the kernel exposes no DMI information for this machine"}
	}
	field := func(name string) string { return readSysfs(filepath.Join(dir, name)) }
	chassis := field("chassis_type")
	if name, ok := chassisTypes[chassis]; ok {
		chassis = name
	}
	return protocol.HardwareSystem{
		Vendor: field("sys_vendor"), Model: field("product_name"),
		Version: field("product_version"), Serial: field("product_serial"),
		BoardVendor: field("board_vendor"), BoardName: field("board_name"),
		BoardVersion: field("board_version"), BoardSerial: field("board_serial"),
		ChassisVendor: field("chassis_vendor"), ChassisType: chassis,
		BIOSVendor: field("bios_vendor"), BIOSVersion: field("bios_version"),
		BIOSDate: field("bios_date"),
	}
}

// readCPUInfo counts sockets, cores and threads from /proc/cpuinfo. Physical ids are absent
// on some guests, where the host is reported as one socket of as many cores as it has threads.
func readCPUInfo(path string) protocol.HardwareCPU {
	text, err := os.ReadFile(path)
	if err != nil {
		return protocol.HardwareCPU{Unavailable: true, Reason: "/proc/cpuinfo could not be read"}
	}
	cpu := protocol.HardwareCPU{}
	sockets, perSocket := map[string]bool{}, 0
	for _, line := range strings.Split(string(text), "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		switch key {
		case "processor":
			cpu.Threads++
		case "model name":
			cpu.Model = value
		case "physical id":
			sockets[value] = true
		case "cpu cores":
			perSocket, _ = strconv.Atoi(value)
		}
	}
	cpu.Sockets = max(len(sockets), 1)
	cpu.Cores = perSocket * cpu.Sockets
	if cpu.Cores == 0 {
		cpu.Cores = cpu.Threads
	}
	return cpu
}

func readMemory(ctx context.Context, meminfo string) protocol.HardwareMemory {
	memory := protocol.HardwareMemory{Modules: []protocol.MemoryModule{}}
	if text, err := os.ReadFile(meminfo); err == nil {
		for _, line := range strings.Split(string(text), "\n") {
			if kb, ok := strings.CutPrefix(line, "MemTotal:"); ok && len(strings.Fields(kb)) > 0 {
				n, _ := strconv.ParseUint(strings.Fields(kb)[0], 10, 64)
				memory.TotalBytes = n * 1024
			}
		}
	}
	out, err := hardwareTool(ctx, "dmidecode", "-t", "memory")
	if err != nil {
		memory.Unavailable, memory.Reason = true, toolReason("dmidecode", err)
		return memory
	}
	var module protocol.MemoryModule
	inDevice := false
	flush := func() {
		if inDevice && len(memory.Modules) < protocol.HardwareMaxMemoryModules {
			memory.Modules = append(memory.Modules, module)
		}
		module, inDevice = protocol.MemoryModule{}, false
	}
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, "\t") {
			flush()
			inDevice = strings.TrimSpace(line) == "Memory Device"
			continue
		}
		key, value, ok := strings.Cut(strings.TrimSpace(line), ": ")
		if !inDevice || !ok {
			continue
		}
		switch key {
		case "Locator":
			module.Slot = value
		case "Size":
			module.SizeBytes = dmidecodeSize(value)
		case "Type":
			if value != "Unknown" {
				module.Type = value
			}
		case "Speed", "Configured Memory Speed":
			if fields := strings.Fields(value); len(fields) > 0 {
				if speed, err := strconv.Atoi(fields[0]); err == nil {
					module.SpeedMTS = speed
				}
			}
		case "Manufacturer":
			module.Manufacturer = value
		case "Part Number":
			module.Part = value
		}
	}
	flush()
	return memory
}

// dmidecodeSize converts "32 GB" or "No Module Installed" into bytes.
func dmidecodeSize(value string) uint64 {
	fields := strings.Fields(value)
	if len(fields) != 2 {
		return 0
	}
	n, err := strconv.ParseUint(fields[0], 10, 64)
	unit := map[string]uint64{"kB": 1 << 10, "MB": 1 << 20, "GB": 1 << 30, "TB": 1 << 40}[fields[1]]
	if err != nil || unit == 0 || n > ^uint64(0)/unit {
		return 0
	}
	return n * unit
}

// lsblkValue accepts a field lsblk emits as a JSON number in current util-linux and as a
// string in older releases, so the report does not depend on the host's lsblk version.
type lsblkValue string

func (v *lsblkValue) UnmarshalJSON(b []byte) error {
	*v = lsblkValue(strings.Trim(string(b), `"`))
	return nil
}

func readDisks(ctx context.Context) protocol.HardwareDisks {
	disks := protocol.HardwareDisks{Devices: []protocol.HardwareDisk{}}
	out, err := hardwareTool(ctx, "lsblk", "-J", "-d", "-b", "-o", "NAME,MODEL,SERIAL,SIZE,ROTA,TRAN")
	if err != nil {
		disks.Unavailable, disks.Reason = true, toolReason("lsblk", err)
		return disks
	}
	var reply struct {
		BlockDevices []struct {
			Name, Model, Serial, Tran string
			Size, Rota                lsblkValue
		} `json:"blockdevices"`
	}
	if err := json.Unmarshal([]byte(out), &reply); err != nil {
		disks.Unavailable, disks.Reason = true, "lsblk returned an unreadable device list"
		return disks
	}
	for _, d := range reply.BlockDevices {
		if !physicalDisk(d.Name) {
			continue // lsblk -d lists zram, loop and optical devices as top-level disks
		}
		if len(disks.Devices) == protocol.HardwareMaxDisks {
			break
		}
		size, _ := strconv.ParseUint(string(d.Size), 10, 64)
		disks.Devices = append(disks.Devices, protocol.HardwareDisk{
			Name: "/dev/" + d.Name, Model: d.Model, Serial: d.Serial,
			SizeBytes: size, Rotational: d.Rota == "1" || d.Rota == "true", Transport: d.Tran,
		})
	}
	return disks
}

func readPCI(ctx context.Context) protocol.HardwarePCI {
	pci := protocol.HardwarePCI{Devices: []protocol.PCIDevice{}}
	out, err := hardwareTool(ctx, "lspci", "-mm", "-nn", "-D")
	if err != nil {
		pci.Unavailable, pci.Reason = true, toolReason("lspci", err)
		return pci
	}
	for _, line := range strings.Split(out, "\n") {
		address, rest, ok := strings.Cut(line, " ")
		if !ok || len(pci.Devices) == protocol.HardwareMaxPCIDevices {
			continue
		}
		// -mm quotes the class, vendor and device names in that order, each ending in its
		// numeric id; the revision and subsystem fields after them are not reported.
		var quoted []string
		for len(quoted) < 3 {
			_, open, found := strings.Cut(rest, `"`)
			field, tail, closed := strings.Cut(open, `"`)
			if !found || !closed {
				break
			}
			quoted, rest = append(quoted, field), tail
		}
		if len(quoted) < 3 {
			continue
		}
		class, classID := splitPCIName(quoted[0])
		vendor, vendorID := splitPCIName(quoted[1])
		device, deviceID := splitPCIName(quoted[2])
		pci.Devices = append(pci.Devices, protocol.PCIDevice{
			Address: address, Class: class, ClassID: classID,
			Vendor: vendor, VendorID: vendorID, Device: device, DeviceID: deviceID,
		})
	}
	return pci
}

// splitPCIName separates `Example Graphics [10de]` into its name and its numeric id.
func splitPCIName(field string) (string, string) {
	open := strings.LastIndex(field, " [")
	if open < 0 || !strings.HasSuffix(field, "]") {
		return field, ""
	}
	return field[:open], field[open+2 : len(field)-1]
}

// readSensors reports each temperature input the usage-history sampler records, keyed the
// same way, with the chip's own thresholds for the gauge's scale and warning band.
func readSensors(root string) protocol.HardwareSensors {
	sensors := protocol.HardwareSensors{Temperatures: []protocol.TemperatureSensor{}}
	for _, s := range hwmonSensorsWithin(root, hardwareToolWait) {
		sensors.Temperatures = append(sensors.Temperatures, protocol.TemperatureSensor{
			Key: s.Key, Chip: s.Chip, Label: s.Label, MaxCelsius: s.Max, CritCelsius: s.Crit,
		})
	}
	return sensors
}
