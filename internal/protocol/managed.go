// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

const ManagedVMCapability = "managed-vm-v1"
const CPUBaselineCapability = "vm-cpu-baseline-v1"

// Appliance is a Controller appliance VM the host deployed through its local engine, reported
// from that engine's deploy journal so a replaced Controller re-adopts it as protected.
type Appliance struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	RequestID string `json:"request_id"`
	Role      string `json:"role"`
}

// ManagedOutcome reports queued work and retained outcomes, keyed by the host's operation ID.
type ManagedOutcome struct {
	OperationID string `json:"operation_id"`
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type StorageRegistration struct {
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	Name        string `json:"name"`
	Path        string `json:"path"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	// Kind is local, nfs, zfs, smb or iscsi. Empty Kind with Path remains a v1 local record.
	Kind                string              `json:"kind,omitempty"`
	Source              StorageSource       `json:"source,omitempty"`
	Identity            StorageIdentity     `json:"identity,omitempty"`
	Generation          uint64              `json:"generation,omitempty"`
	Tombstone           bool                `json:"tombstone,omitempty"`
	CredentialReceipt   string              `json:"credential_receipt,omitempty"`
	CredentialRequestID string              `json:"credential_request_id,omitempty"`
	CredentialPresent   bool                `json:"credential_present,omitempty"`
	Capabilities        StorageCapabilities `json:"capabilities,omitempty"`
	Access              StorageAccess       `json:"access,omitempty"`
	// MemberAccess is master-reported, per-host observed access for a pool remote
	// repository. It remains empty for host-local repositories and iSCSI single-writer use.
	MemberAccess []PoolStorageAccess `json:"member_access,omitempty"`
}
type NetworkRegistration struct {
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Uplink      string `json:"uplink,omitempty"`
	VLAN        int    `json:"vlan,omitempty"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

type Media struct {
	URL         string `json:"url,omitempty"`
	ID          string `json:"id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	StorageID   string `json:"storage_id"`
	Name        string `json:"name"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
	OperationID string `json:"operation_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
}

// CPUFeature pins a native feature requirement or exclusion in a named CPU definition.
type CPUFeature struct {
	Name   string `json:"name" xml:"name,attr"`
	Policy string `json:"policy" xml:"policy,attr"`
}

// VMCPU carries the vCPU count, optional topology and fixed CPU definition. A zero topology
// means one socket, Count cores, one thread.
type VMCPU struct {
	Count    int          `json:"count"`
	Sockets  int          `json:"sockets"`
	Cores    int          `json:"cores"`
	Threads  int          `json:"threads"`
	Model    string       `json:"model"`
	Features []CPUFeature `json:"features,omitempty"`
}

// VMMemory is the ceiling in both modes: dedicated preallocates and locks it while running;
// stopped dedicated VMs retain their Core capacity commitment. Maximum only caps it.
type VMMemory struct {
	Mode    string `json:"mode"`
	SizeMiB int    `json:"size_mib"`
}
type VMDisk struct {
	StorageID string `json:"storage_id"`
	Name      string `json:"name"`
	SizeGiB   int    `json:"size_gib"`
	Bus       string `json:"bus"`
	Thin      bool   `json:"thin"`
	BootOrder int    `json:"boot_order"`
}

// VMNIC's MAC is empty for a MAC derived from the VM ID and interface index. MTU is 0 for the
// network's own MTU; a value is only accepted on virtio, the one model that can tell the guest.
type VMNIC struct {
	NetworkID string `json:"network_id"`
	Model     string `json:"model"`
	MAC       string `json:"mac"`
	MTU       int    `json:"mtu,omitempty"`
}

// NICModels are the adapter models Core can define. virtio is paravirtualised and needs a guest
// driver; the rest emulate real cards so a guest without virtio drivers still gets a network.
// rtl8139 is a 100 Mbit part and vmxnet3 exists for guests carrying VMware drivers.
var NICModels = []string{"virtio", "e1000", "e1000e", "rtl8139", "vmxnet3"}

// MTU bounds: below 1280 breaks IPv6, and 9216 is the largest jumbo frame common NICs accept.
const (
	MinNICMTU = 1280
	MaxNICMTU = 9216
)

// VMBoot's MediaBootOrder is the CD drive's place in the boot order. Every VM has the drive,
// so the order is kept while it is empty and an ISO can be ejected from a running guest.
type VMBoot struct {
	Firmware       string `json:"firmware"`
	Machine        string `json:"machine"`
	MediaBootOrder int    `json:"media_boot_order"`
	// SecureBoot is "on" or "off" for UEFI. "" keeps the host default, which is on: that is
	// what libvirt already selected for every UEFI VM defined before this field existed, so
	// an older definition re-sent unchanged keeps the firmware it was installed with.
	SecureBoot string `json:"secure_boot,omitempty"`
	// TPM is "2.0" for an emulated TPM 2.0 device, "none" or "" for no TPM. "" is no TPM so
	// that a definition written before this field existed keeps exactly the devices it had.
	TPM string `json:"tpm,omitempty"`
}

// Secure Boot and TPM values accepted in VMBoot.
const (
	SecureBootDefault = ""
	SecureBootOn      = "on"
	SecureBootOff     = "off"
	TPMNone           = "none"
	TPM20             = "2.0"
)

// SecureBootOn reports whether this definition boots with Secure Boot: UEFI with the field
// unset or "on". BIOS never has it.
func (b VMBoot) SecureBootEnabled() bool {
	return b.Firmware == "uefi" && b.SecureBoot != SecureBootOff
}

// TPMEnabled reports whether this definition carries an emulated TPM device.
func (b VMBoot) TPMEnabled() bool { return b.TPM == TPM20 }

// VMCreateSpec is a VM's complete definition. It is the vm-create payload and, at a later
// Revision, the vm-update payload; the host journals every revision and reports the current one.
type VMCreateSpec struct {
	ID          string   `json:"id"`
	PoolID      string   `json:"pool_id"`
	HostID      string   `json:"host_id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	GuestOS     string   `json:"guest_os"`
	CPU         VMCPU    `json:"cpu"`
	Memory      VMMemory `json:"memory"`
	Disks       []VMDisk `json:"disks"`
	NICs        []VMNIC  `json:"nics"`
	Boot        VMBoot   `json:"boot"`
	MediaID     string   `json:"media_id"`
	// DriversMediaID is the ISO in the VM's second CD drive, for a driver disc an installer
	// needs alongside the install media (Windows and VirtIO storage, for example). Empty
	// leaves that drive empty; the drive itself always exists.
	DriversMediaID   string `json:"drivers_media_id,omitempty"`
	StartAfterCreate bool   `json:"start_after_create"`
	Autostart        bool   `json:"autostart"`
	// Revision is 0 at creation and current + 1 for each edit; Core refuses any other value.
	Revision int `json:"revision"`
}

// UpdateCapability is reported by agents that apply vm-update commands.
const UpdateCapability = "vm-update-v1"

// DeleteCapability is reported by agents that apply vm-delete commands.
const DeleteCapability = "vm-delete-v1"

// VMDeleteRequest removes one managed VM definition from its assigned host. DeleteDisks
// unlinks the guest disk files after undefine; when false the files are retained on storage.
type VMDeleteRequest struct {
	VMID        string `json:"vm_id"`
	PoolID      string `json:"pool_id"`
	HostID      string `json:"host_id"`
	DeleteDisks bool   `json:"delete_disks"`
}

// VMTombstone tells Controllers a managed VM was intentionally deleted and should leave the
// mirror. It is reported from Core's journal, not invented by Controller.
type VMTombstone struct {
	ID        string `json:"id"`
	PoolID    string `json:"pool_id"`
	HostID    string `json:"host_id"`
	Tombstone bool   `json:"tombstone"`
}

// VMChange names one difference between two revisions that a running guest cannot take;
// Core refuses the whole edit before any effect, and the form disables the field while running.
func (c VMChange) Error() string { return "stop the VM first to change " + string(c) }

type VMChange string

// NeedsStop lists what changing cur into next requires the VM to be shut off for, and refuses
// what no edit may change. Disks match by name and interfaces by MAC (derived ones resolved
// from the VM identity), so a reordered list is not a change. Additions and removals of disks
// (except a SATA disk, which QEMU cannot hot-plug) and interfaces, the install media, the
// description, the guest OS hint and autostart apply to a running guest.
func NeedsStop(cur, next VMCreateSpec) ([]VMChange, error) {
	var out []VMChange
	add := func(changed bool, what string) {
		if changed {
			out = append(out, VMChange(what))
		}
	}
	add(cur.Name != next.Name, "the name")
	add(cur.CPU.Count != next.CPU.Count || cur.CPU.Sockets != next.CPU.Sockets || cur.CPU.Cores != next.CPU.Cores || cur.CPU.Threads != next.CPU.Threads || cur.CPU.Model != next.CPU.Model || !slices.Equal(cur.CPU.Features, next.CPU.Features), "the CPU configuration")
	add(cur.Memory != next.Memory, "the memory")
	add(cur.Boot.Firmware != next.Boot.Firmware || cur.Boot.Machine != next.Boot.Machine, "the firmware or machine type")
	add(cur.Boot.SecureBootEnabled() != next.Boot.SecureBootEnabled(), "Secure Boot")
	// Adding a TPM to a stopped VM is an ordinary edit. REMOVING one is refused outright, for
	// two reasons that point the same way. It destroys whatever the guest sealed to that TPM —
	// a Windows guest stores its BitLocker key there, and dropping the device silently leaves an
	// unbootable guest. And libvirt cleans a domain's TPM state by iterating the devices the
	// persistent definition still carries, so a redefine without <tpm> orphans
	// /var/lib/libvirt/swtpm/<uuid>, holding the endorsement key and the sealed secrets, with
	// nothing left to clean it up at delete (BUG-0214). Refusing the transition keeps the
	// definition and the host state in step, so deletion always removes what the VM created.
	// An operator who genuinely wants no TPM deletes the VM and creates it again.
	if cur.Boot.TPMEnabled() && !next.Boot.TPMEnabled() {
		return nil, errors.New("a VM's TPM cannot be removed after creation: anything the guest sealed to it, including a Windows BitLocker key, would be lost. Delete the VM and create it without a TPM instead")
	}
	add(cur.Boot.TPMEnabled() != next.Boot.TPMEnabled(), "the TPM")
	add(cur.Boot.MediaBootOrder != next.Boot.MediaBootOrder, "the boot order")
	before := map[string]VMDisk{}
	for _, d := range cur.Disks {
		before[d.Name] = d
	}
	for _, d := range next.Disks {
		old, known := before[d.Name]
		if !known {
			add(d.Bus == "sata", "a SATA disk cannot be added while the VM runs: use VirtIO or SCSI, or stop the VM")
			continue
		}
		if old.StorageID != d.StorageID || old.SizeGiB != d.SizeGiB || old.Thin != d.Thin {
			return nil, fmt.Errorf("disk %s: storage, size and provisioning cannot be changed after creation", d.Name)
		}
		add(old.Bus != d.Bus, "disk "+d.Name+"'s bus")
		add(old.BootOrder != d.BootOrder, "the boot order")
	}
	macs := map[string]VMNIC{}
	for i, n := range cur.NICs {
		macs[NICMAC(cur.ID, i, n)] = n
	}
	for i, n := range next.NICs {
		mac := NICMAC(next.ID, i, n)
		old, known := macs[mac]
		if known && (old.NetworkID != n.NetworkID || old.Model != n.Model || old.MTU != n.MTU) {
			add(true, "network interface "+mac)
		}
	}
	return out, nil
}

// NICMAC is an interface's effective address: the manual one, or the one derived from the VM
// identity and interface index.
func NICMAC(vmID string, index int, n VMNIC) string {
	if n.MAC != "" {
		return n.MAC
	}
	return DerivedMAC(vmID, index)
}

var diskName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)
var machineType = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
var cpuModel = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func ValidCPUModel(model string) bool { return cpuModel.MatchString(model) }

// Topology resolves the sockets × cores × threads a domain uses, applying the documented
// default when the operator left the topology unset. ValidateVMSpec has already checked
// that a supplied topology multiplies to Count.
func (c VMCPU) Topology() (sockets, cores, threads int) {
	if c.Sockets == 0 && c.Cores == 0 && c.Threads == 0 {
		return 1, c.Count, 1
	}
	return c.Sockets, c.Cores, c.Threads
}

// DerivedMAC returns the stable MAC for a VM's interface index: the QEMU locally administered
// unicast prefix 52:54:00, two octets from the VM ID and the index. Indices differ by the last
// octet, so the at most 8 interfaces a VM may have never collide with each other.
func DerivedMAC(vmID string, index int) string {
	h := sha256.Sum256([]byte(vmID))
	return fmt.Sprintf("52:54:00:%02x:%02x:%02x", h[0], h[1], byte(index))
}

// ValidateCPUFeatures bounds the explicit native CPU definition, independently of guest OS.
func ValidateCPUFeatures(model string, features []CPUFeature) error {
	if len(features) > 512 {
		return errors.New("CPU feature limit exceeded")
	}
	if len(features) > 0 && (model == "host-model" || model == "host-passthrough" || model == "max" || !ValidCPUModel(model)) {
		return errors.New("explicit CPU features require a named CPU model")
	}
	seen := map[string]bool{}
	for _, f := range features {
		if !ValidCPUModel(f.Name) || seen[f.Name] || (f.Policy != "require" && f.Policy != "disable") {
			return errors.New("CPU features must have distinct valid names and require or disable policies")
		}
		seen[f.Name] = true
	}
	return nil
}

// ValidateVMSpec checks the bounds Controller and Core enforce identically on a VM
// specification. Identity, resource references, pool-wide MAC uniqueness and host capacity
// belong to their owners and are not checked here.
func ValidateVMSpec(v VMCreateSpec) error {
	if err := ValidateCPUFeatures(v.CPU.Model, v.CPU.Features); err != nil {
		return err
	}
	if !utf8.ValidString(v.Name) || len(v.Name) > 200 || strings.TrimSpace(v.Name) == "" || strings.ContainsFunc(v.Name, func(r rune) bool {
		return r == '/' || unicode.IsControl(r) || r == '\ufffe' || r == '\uffff'
	}) {
		return errors.New("VM name must be nonblank, at most 200 UTF-8 bytes, without slashes or control characters")
	}
	if v.GuestOS != "" && v.GuestOS != "linux" && v.GuestOS != "windows" && v.GuestOS != "other" {
		return errors.New("guest OS must be linux, windows or other")
	}
	if len(v.Description) > 4096 {
		return errors.New("description must be at most 4096 characters")
	}
	if v.CPU.Count < 1 {
		return errors.New("a VM needs at least 1 vCPU")
	}
	if v.CPU.Sockets != 0 || v.CPU.Cores != 0 || v.CPU.Threads != 0 {
		if v.CPU.Sockets < 1 || v.CPU.Cores < 1 || v.CPU.Threads < 1 || v.CPU.Sockets*v.CPU.Cores*v.CPU.Threads != v.CPU.Count {
			return errors.New("sockets × cores × threads must equal the vCPU count")
		}
	}
	if v.CPU.Model != "host-passthrough" && v.CPU.Model != "host-model" && !ValidCPUModel(v.CPU.Model) {
		return errors.New("invalid CPU model")
	}
	if v.Memory.Mode != "dedicated" && v.Memory.Mode != "maximum" {
		return errors.New("memory mode must be dedicated or maximum")
	}
	if v.Memory.SizeMiB < 512 {
		return errors.New("memory must be at least 512 MiB")
	}
	if v.Boot.Firmware != "uefi" && v.Boot.Firmware != "bios" {
		return errors.New("firmware must be uefi or bios")
	}
	if v.Boot.Machine != "" && !machineType.MatchString(v.Boot.Machine) {
		return errors.New("machine type must be a plain libvirt machine name such as q35")
	}
	switch v.Boot.SecureBoot {
	case SecureBootDefault, SecureBootOff:
	case SecureBootOn:
		if v.Boot.Firmware != "uefi" {
			return errors.New("Secure Boot needs UEFI firmware")
		}
	default:
		return errors.New("secure boot must be on or off")
	}
	switch v.Boot.TPM {
	case "", TPMNone, TPM20:
	default:
		return errors.New("TPM must be 2.0 or none")
	}
	if v.DriversMediaID != "" && v.DriversMediaID == v.MediaID {
		return errors.New("the driver disc must differ from the install media")
	}
	if len(v.Disks) < 1 || len(v.Disks) > 8 {
		return errors.New("a VM needs 1 to 8 disks")
	}
	if len(v.NICs) > 8 {
		return errors.New("a VM allows at most 8 network interfaces")
	}
	orders, names := map[int]bool{}, map[string]bool{}
	boot := func(o int) error {
		if o < 0 {
			return errors.New("boot order must not be negative")
		}
		if o > 0 {
			if orders[o] {
				return errors.New("boot order values must be unique across disks and the install media")
			}
			orders[o] = true
		}
		return nil
	}
	for _, d := range v.Disks {
		if d.StorageID == "" {
			return errors.New("each disk needs a storage target")
		}
		if !diskName.MatchString(d.Name) {
			return errors.New("each disk needs a name of letters, digits, dot, dash or underscore starting with a letter or digit")
		}
		if names[d.Name] {
			return errors.New("disk names must be unique within a VM")
		}
		names[d.Name] = true
		if d.SizeGiB < 1 || d.SizeGiB > 2048 {
			return errors.New("each disk must be 1 to 2048 GiB")
		}
		if d.Bus != "virtio" && d.Bus != "sata" && d.Bus != "scsi" {
			return errors.New("disk bus must be virtio, sata or scsi")
		}
		if err := boot(d.BootOrder); err != nil {
			return err
		}
	}
	if err := boot(v.Boot.MediaBootOrder); err != nil {
		return err
	}
	if len(orders) == 0 {
		return errors.New("at least one disk or the install media must have a boot order")
	}
	// Boot order values must be unique and positive, not contiguous: libvirt orders devices by
	// bootindex and tolerates gaps, and a host can hold one: an older Core cleared the CD
	// drive's order on eject, so its records carry a gap. Rejecting that gap would
	// make a re-adopting Controller discard the VM over a boot order the host still holds.
	macs := map[string]bool{}
	for _, n := range v.NICs {
		if n.NetworkID == "" {
			return errors.New("each network interface needs a network")
		}
		if !slices.Contains(NICModels, n.Model) {
			return errors.New("network adapter model must be one of " + strings.Join(NICModels, ", "))
		}
		// QEMU communicates an MTU to the guest through virtio's host_mtu feature. The emulated
		// models have no equivalent, so accepting an MTU there would set the host side while the
		// guest silently kept 1500. Refuse it rather than pretend.
		if n.MTU != 0 {
			if n.Model != "virtio" {
				return errors.New("an MTU can only be set on a virtio adapter; other models take their MTU from inside the guest")
			}
			if n.MTU < MinNICMTU || n.MTU > MaxNICMTU {
				return fmt.Errorf("a network adapter MTU must be between %d and %d", MinNICMTU, MaxNICMTU)
			}
		}
		if n.MAC == "" {
			continue
		}
		hw, err := net.ParseMAC(n.MAC)
		if err != nil || len(hw) != 6 || hw.String() != n.MAC || hw[0]&1 != 0 || (hw[0]|hw[1]|hw[2]|hw[3]|hw[4]|hw[5]) == 0 {
			return errors.New("a manual MAC must be a unicast six-octet address in lower case, such as 52:54:00:12:34:56")
		}
		if macs[n.MAC] {
			return errors.New("MAC addresses must be unique within a VM")
		}
		macs[n.MAC] = true
	}
	return nil
}

var uplinkName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,9}$`)

// GuestBridge derives the deterministic bridge name for a VLAN network: uplink name plus
// "v" and the tag. Names stay within IFNAMSIZ (15) because uplinks are limited to 10 characters.
func GuestBridge(uplink string, vlan int) string { return fmt.Sprintf("%sv%d", uplink, vlan) }

// ValidUplink reports whether name can be an uplink NIC: plain, at most 10 characters.
func ValidUplink(name string) bool { return uplinkName.MatchString(name) }

// ValidateVLAN checks a kind "vlan" registration's shape on both Controller and Core.
func ValidateVLAN(n NetworkRegistration) error {
	if n.Kind != "vlan" || !uplinkName.MatchString(n.Uplink) || n.VLAN < 0 || n.VLAN > 4094 || n.Source != GuestBridge(n.Uplink, n.VLAN) {
		return errors.New("VLAN networks need an uplink NIC name of at most 10 characters, a VLAN ID 0–4094 and the derived bridge name")
	}
	return nil
}
