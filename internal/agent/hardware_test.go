// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// Every value below is fictional. No hardware was read: these fixtures prove the parsing,
// the bounds and the per-section failure behaviour, not what a real machine reports.
const (
	fixtureCPUInfo = `processor	: 0
model name	: Example CPU E-16
physical id	: 0
cpu cores	: 2

processor	: 1
model name	: Example CPU E-16
physical id	: 0
cpu cores	: 2

processor	: 2
model name	: Example CPU E-16
physical id	: 1
cpu cores	: 2

processor	: 3
model name	: Example CPU E-16
physical id	: 1
cpu cores	: 2
`
	fixtureMemInfo = "MemTotal:       16384 kB\nMemFree:         1024 kB\n"

	fixtureDmidecode = `# dmidecode 3.5
Handle 0x1100, DMI type 17, 92 bytes
Memory Device
	Locator: DIMM_A1
	Size: 32 GB
	Type: DDR5
	Speed: 4800 MT/s
	Manufacturer: Example Memory
	Part Number: EXM-32G-4800

Handle 0x1101, DMI type 17, 92 bytes
Memory Device
	Locator: DIMM_A2
	Size: No Module Installed
	Type: Unknown
`
	// Older util-linux quotes every value; the current one emits numbers. Both are read.
	// zram, loop and optical devices are top-level block devices lsblk -d lists as disks;
	// none of them is installed hardware.
	fixtureLsblk = `{"blockdevices":[
   {"name":"nvme0n1","model":"EXAMPLE NVMe 1TB","serial":"EXNVME0000001","size":1024209543168,"rota":false,"tran":"nvme"},
   {"name":"zram0","model":null,"serial":null,"size":67138224128,"rota":false,"tran":null},
   {"name":"sr0","model":"EXAMPLE DVD","serial":null,"size":1073741312,"rota":true,"tran":"sata"},
   {"name":"loop0","model":null,"serial":null,"size":134217728,"rota":false,"tran":null},
   {"name":"sda","model":"EXAMPLE HDD","serial":"EXHDD0000001","size":"2000398934016","rota":"1","tran":"sata"}
]}`
	fixtureLspci = `0000:00:1f.2 "SATA controller [0106]" "Example Chipsets [8086]" "EX-AHCI [2922]" -r02 "Example Chipsets [8086]" "Device [7270]"
0000:01:00.0 "VGA compatible controller [0300]" "Example Graphics [10de]" "EX-GPU 2000 [1c82]" -ra1 "Example Graphics [10de]" "Device [1234]"
`
)

// fixtureHost builds a sysfs/procfs tree and puts stub inventory tools on PATH. A tool
// whose output is empty exits 1, standing in for one that fails on this machine.
func fixtureHost(t *testing.T, tools map[string]string) string {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	dmi := filepath.Join(root, "sys/class/dmi/id")
	for name, value := range map[string]string{
		"sys_vendor": "Example Systems", "product_name": "EX-2200", "product_version": "R2",
		"product_serial": "EXAMPLE0000001", "board_vendor": "Example Systems",
		"board_name": "EX-MB-1", "board_serial": "EXAMPLEBOARD01", "chassis_type": "23",
		"bios_vendor": "Example BIOS", "bios_version": "2.1.4", "bios_date": "2026-01-15",
	} {
		write(filepath.Join(dmi, name), value+"\n")
	}
	write(filepath.Join(root, "proc/cpuinfo"), fixtureCPUInfo)
	write(filepath.Join(root, "proc/meminfo"), fixtureMemInfo)
	hwmonChip(t, filepath.Join(root, "sys"), 0, "coretemp", "devices/pci0000:00/0000:00:18.3",
		map[string]string{"temp1_input": "42000", "temp1_label": "Package id 0", "temp1_max": "95000", "temp1_crit": "105000"})

	// PATH holds only the stubs, so a tool with no stub is genuinely missing and nothing
	// reads this machine. The stubs print a file, because /bin is not on that PATH.
	bin := t.TempDir()
	for name, output := range tools {
		script := "#!/bin/sh\nexit 1\n"
		if output != "" {
			write(filepath.Join(bin, name+".out"), output)
			script = "#!/bin/sh\nexec /bin/cat " + filepath.Join(bin, name+".out") + "\n"
		}
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin)
	return root
}

// writeMDFixture builds the same /sys/block tree vmhost's md tests use, so the agent
// wiring of ReadMDArrays is checked against a real fixture rather than a stubbed return.
func writeMDFixture(t *testing.T, root, name string, array map[string]string, members map[string]map[string]string) {
	t.Helper()
	dir := filepath.Join(root, "sys/block", name, "md")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sys/block", "sda"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, value := range array {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for member, files := range members {
		if err := os.MkdirAll(filepath.Join(dir, "dev-"+member), 0o755); err != nil {
			t.Fatal(err)
		}
		for file, value := range files {
			if err := os.WriteFile(filepath.Join(dir, "dev-"+member, file), []byte(value+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestReadHardwareReportsEverySection(t *testing.T) {
	root := fixtureHost(t, map[string]string{
		"dmidecode": fixtureDmidecode, "lsblk": fixtureLsblk, "lspci": fixtureLspci})
	writeMDFixture(t, root, "md0",
		map[string]string{"level": "raid1", "raid_disks": "2", "degraded": "0", "sync_action": "idle", "sync_completed": "none"},
		map[string]map[string]string{
			"sda2": {"state": "in_sync", "slot": "0"},
			"sdb2": {"state": "in_sync", "slot": "1"},
		})
	hw := readHardware(context.Background(), root)

	if hw.ObservedAt.IsZero() {
		t.Fatal("no observation time")
	}
	if hw.System.Model != "EX-2200" || hw.System.Serial != "EXAMPLE0000001" ||
		hw.System.ChassisType != "Rack Mount Chassis" || hw.System.BIOSVersion != "2.1.4" {
		t.Fatalf("system %+v", hw.System)
	}
	if hw.CPU.Model != "Example CPU E-16" || hw.CPU.Sockets != 2 || hw.CPU.Cores != 4 || hw.CPU.Threads != 4 {
		t.Fatalf("cpu %+v", hw.CPU)
	}
	if hw.Memory.TotalBytes != 16384*1024 || len(hw.Memory.Modules) != 2 {
		t.Fatalf("memory %+v", hw.Memory)
	}
	if first := hw.Memory.Modules[0]; first.Slot != "DIMM_A1" || first.SizeBytes != 32<<30 ||
		first.Type != "DDR5" || first.SpeedMTS != 4800 || first.Part != "EXM-32G-4800" {
		t.Fatalf("module %+v", first)
	}
	if empty := hw.Memory.Modules[1]; empty.Slot != "DIMM_A2" || empty.SizeBytes != 0 || empty.Type != "" {
		t.Fatalf("empty slot %+v", empty)
	}
	// The zram, optical and loop devices lsblk also returned are not installed disks.
	if len(hw.Disks.Devices) != 2 {
		t.Fatalf("disks %+v", hw.Disks)
	}
	if nvme := hw.Disks.Devices[0]; nvme.Name != "/dev/nvme0n1" || nvme.SizeBytes != 1024209543168 ||
		nvme.Rotational || nvme.Transport != "nvme" || nvme.Serial != "EXNVME0000001" {
		t.Fatalf("nvme %+v", nvme)
	}
	// The quoted size and rotational flag of an older lsblk read the same as the numbers.
	if sda := hw.Disks.Devices[1]; sda.SizeBytes != 2000398934016 || !sda.Rotational {
		t.Fatalf("sata %+v", sda)
	}
	if len(hw.PCI.Devices) != 2 {
		t.Fatalf("pci %+v", hw.PCI)
	}
	gpu := hw.PCI.Devices[1]
	if gpu.Address != "0000:01:00.0" || gpu.ClassID != "0300" || gpu.Vendor != "Example Graphics" ||
		gpu.VendorID != "10de" || gpu.Device != "EX-GPU 2000" || gpu.DeviceID != "1c82" {
		t.Fatalf("gpu %+v", gpu)
	}
	if len(hw.Sensors.Temperatures) != 1 {
		t.Fatalf("sensors %+v", hw.Sensors)
	}
	sensor := hw.Sensors.Temperatures[0]
	if sensor.Key != "temp.coretemp-0000:00:18.3.1" || sensor.Label != "CPU 0 package" ||
		sensor.MaxCelsius == nil || *sensor.MaxCelsius != 95 || sensor.CritCelsius == nil || *sensor.CritCelsius != 105 {
		t.Fatalf("sensor %+v", sensor)
	}
	if hw.MDArrays.Unavailable || len(hw.MDArrays.Arrays) != 1 || hw.MDArrays.Arrays[0].Name != "md0" {
		t.Fatalf("md_arrays %+v", hw.MDArrays)
	}
}

// A missing or failing tool leaves its own section unavailable with a reason; every other
// section, including the memory total the kernel reports, still returns.
func TestReadHardwareSurvivesMissingAndFailingTools(t *testing.T) {
	root := fixtureHost(t, map[string]string{"lspci": ""}) // lspci exits 1; the rest are absent
	hw := readHardware(context.Background(), root)

	for name, reason := range map[string]string{
		"memory": hw.Memory.Reason, "disks": hw.Disks.Reason, "pci": hw.PCI.Reason,
	} {
		if reason == "" {
			t.Fatalf("%s section failed with no reason", name)
		}
	}
	if !hw.Memory.Unavailable || !hw.Disks.Unavailable || !hw.PCI.Unavailable {
		t.Fatalf("a failed section is not marked unavailable: %+v", hw)
	}
	if hw.Memory.Reason != "dmidecode is not installed" || hw.PCI.Reason != "lspci failed" {
		t.Fatalf("reasons %q %q", hw.Memory.Reason, hw.PCI.Reason)
	}
	if hw.Memory.TotalBytes != 16384*1024 || hw.System.Model != "EX-2200" ||
		hw.CPU.Threads != 4 || len(hw.Sensors.Temperatures) != 1 {
		t.Fatalf("a working section was lost: %+v", hw)
	}
	if hw.System.Unavailable || hw.CPU.Unavailable || hw.Sensors.Unavailable {
		t.Fatal("a section with no tool to fail was marked unavailable")
	}
}

// A machine with no DMI table and no /proc/cpuinfo still answers, section by section.
func TestReadHardwareWithoutSysfsOrProc(t *testing.T) {
	hw := readHardware(context.Background(), t.TempDir())
	if !hw.System.Unavailable || !hw.CPU.Unavailable {
		t.Fatalf("missing sources reported as present: %+v", hw)
	}
	if hw.Memory.TotalBytes != 0 || len(hw.Sensors.Temperatures) != 0 {
		t.Fatalf("invented values: %+v", hw)
	}
	if !hw.MDArrays.Unavailable || hw.MDArrays.Reason == "" {
		t.Fatalf("missing md sysfs reported as present: %+v", hw.MDArrays)
	}
}

// The stats channel carries both queries: usage history stays on /stats and the hardware
// report answers on /hardware, which is read on request and kept nowhere.
func TestStatsChannelServesHistoryAndHardware(t *testing.T) {
	h := newUsageHistory(t.TempDir(), "host-1")
	h.record("host", time.Now(), []historyPoint{point("cpu.total", 1)})
	handler := statsChannelHandler(h, nil)
	for path, want := range map[string]int{"/stats?object=host&range=10m": 200, "/hardware": 200, "/nope": 404} {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://host"+path, nil))
		if w.Code != want {
			t.Fatalf("%s = %d, want %d", path, w.Code, want)
		}
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "http://host/hardware", nil))
	var hw protocol.HostHardware
	if err := json.Unmarshal(w.Body.Bytes(), &hw); err != nil || hw.ObservedAt.IsZero() {
		t.Fatalf("hardware reply %v: %s", err, w.Body)
	}
}
