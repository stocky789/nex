// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

// HostHardwareCapability is offered by a Core that answers GET /hardware on a stats channel.
// Without it the Controller refuses the hardware route rather than opening a channel.
const HostHardwareCapability = "host-hardware-v1"

// Bounds the host enforces on its own report and the Controller re-checks, so one
// misbehaving host cannot hand a browser an unbounded document.
const (
	HardwareMaxMemoryModules = 64
	HardwareMaxDisks         = 128
	HardwareMaxPCIDevices    = 256
	HardwareMaxSensors       = 24
	HardwareMaxMDArrays      = 32
	HardwareMaxMDMembers     = 64
)

// HostHardware is what a host reads about itself on request. Nothing here is cached,
// persisted or reported in a heartbeat: every field is observed when it is asked for.
type HostHardware struct {
	HostID     string           `json:"host_id,omitempty"` // set by the Controller relay
	ObservedAt time.Time        `json:"observed_at"`
	System     HardwareSystem   `json:"system"`
	CPU        HardwareCPU      `json:"cpu"`
	Memory     HardwareMemory   `json:"memory"`
	Disks      HardwareDisks    `json:"disks"`
	PCI        HardwarePCI      `json:"pci"`
	Sensors    HardwareSensors  `json:"sensors"`
	MDArrays   HardwareMDArrays `json:"md_arrays"`
}

// HardwareSystem describes the machine, its board, chassis and firmware. Serial numbers are
// returned to authenticated administrators only; they are never logged.
//
// Every section carries the same two fields: a source that is missing or fails leaves its own
// section empty with a reason, and the rest of the report still returns.
type HardwareSystem struct {
	Unavailable   bool   `json:"unavailable,omitempty"`
	Reason        string `json:"reason,omitempty"`
	Vendor        string `json:"vendor,omitempty"`
	Model         string `json:"model,omitempty"`
	Version       string `json:"version,omitempty"`
	Serial        string `json:"serial,omitempty"`
	BoardVendor   string `json:"board_vendor,omitempty"`
	BoardName     string `json:"board_name,omitempty"`
	BoardVersion  string `json:"board_version,omitempty"`
	BoardSerial   string `json:"board_serial,omitempty"`
	ChassisVendor string `json:"chassis_vendor,omitempty"`
	ChassisType   string `json:"chassis_type,omitempty"`
	BIOSVendor    string `json:"bios_vendor,omitempty"`
	BIOSVersion   string `json:"bios_version,omitempty"`
	BIOSDate      string `json:"bios_date,omitempty"`
}

type HardwareCPU struct {
	Unavailable bool   `json:"unavailable,omitempty"`
	Reason      string `json:"reason,omitempty"`
	Model       string `json:"model,omitempty"`
	Sockets     int    `json:"sockets,omitempty"`
	Cores       int    `json:"cores,omitempty"`
	Threads     int    `json:"threads,omitempty"`
}

// HardwareMemory reports the installed total even when the module table is unavailable:
// the total comes from the kernel, the modules from dmidecode.
type HardwareMemory struct {
	Unavailable bool           `json:"unavailable,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	TotalBytes  uint64         `json:"total_bytes,omitempty"`
	Modules     []MemoryModule `json:"modules"`
}

// MemoryModule is one DIMM slot; an empty slot reports its locator and a zero size.
type MemoryModule struct {
	Slot         string `json:"slot"`
	SizeBytes    uint64 `json:"size_bytes"`
	Type         string `json:"type,omitempty"`
	SpeedMTS     int    `json:"speed_mts,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	Part         string `json:"part,omitempty"`
}

type HardwareDisks struct {
	Unavailable bool           `json:"unavailable,omitempty"`
	Reason      string         `json:"reason,omitempty"`
	Devices     []HardwareDisk `json:"devices"`
}

type HardwareDisk struct {
	Name       string `json:"name"` // "/dev/sda", like Disk.Name
	Model      string `json:"model,omitempty"`
	Serial     string `json:"serial,omitempty"`
	SizeBytes  uint64 `json:"size_bytes"`
	Rotational bool   `json:"rotational"`
	Transport  string `json:"transport,omitempty"` // nvme, sata, sas, usb
}

type HardwarePCI struct {
	Unavailable bool        `json:"unavailable,omitempty"`
	Reason      string      `json:"reason,omitempty"`
	Devices     []PCIDevice `json:"devices"`
}

// PCIDevice keeps the numeric class, vendor and device ids beside their names: they group
// GPUs, NICs and storage controllers without matching on translated text.
type PCIDevice struct {
	Address  string `json:"address"` // 0000:01:00.0
	Class    string `json:"class"`
	ClassID  string `json:"class_id"`
	Vendor   string `json:"vendor"`
	VendorID string `json:"vendor_id"`
	Device   string `json:"device"`
	DeviceID string `json:"device_id"`
}

type HardwareSensors struct {
	Unavailable  bool                `json:"unavailable,omitempty"`
	Reason       string              `json:"reason,omitempty"`
	Temperatures []TemperatureSensor `json:"temperatures"`
}

// TemperatureSensor carries the chip's own thresholds beside the key of the temperature
// series in the host's usage history, so a gauge takes its scale from the hardware.
type TemperatureSensor struct {
	Key         string   `json:"key"` // identical to StatsSeries.Key
	Chip        string   `json:"chip"`
	Label       string   `json:"label"`
	MaxCelsius  *float64 `json:"max_celsius,omitempty"`
	CritCelsius *float64 `json:"crit_celsius,omitempty"`
}

// HardwareMDArrays reports the Linux md arrays the kernel has assembled on this host, which
// is how a mirrored system disk and its rebuild are seen. Nothing is stored: the arrays and
// their members are read from sysfs when the report is asked for.
type HardwareMDArrays struct {
	Unavailable bool      `json:"unavailable,omitempty"`
	Reason      string    `json:"reason,omitempty"`
	Arrays      []MDArray `json:"arrays"`
}

// MDArray is one /sys/block/md*/md/ directory. SyncPercent is set only while the kernel
// reports a position in a running action, so an idle array has no invented progress.
type MDArray struct {
	Name        string   `json:"name"`   // md0
	Device      string   `json:"device"` // /dev/md0
	Level       string   `json:"level"`  // raid1
	RaidDisks   int      `json:"raid_disks"`
	Degraded    int      `json:"degraded"`
	SyncAction  string   `json:"sync_action,omitempty"` // idle, resync, recover, check
	SyncPercent *float64 `json:"sync_percent,omitempty"`
	// SyncSpeedKBs is the kernel's own current copy rate in KB/s while an action runs, so an
	// operator can see a rebuild moving rather than only how far it has come.
	SyncSpeedKBs int        `json:"sync_speed_kbs,omitempty"`
	Members      []MDMember `json:"members"`
}

// MDMember is one dev-* entry of an array: the partition, the slot it fills and the state
// the kernel reports for it (in_sync, spare, faulty, write_mostly, …).
type MDMember struct {
	Name   string `json:"name"`   // sda2
	Device string `json:"device"` // /dev/sda2
	Slot   string `json:"slot"`   // "0", or "none" for a spare or failed member
	State  string `json:"state"`
}
