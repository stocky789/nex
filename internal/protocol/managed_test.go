// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

func validSpec() VMCreateSpec {
	return VMCreateSpec{
		ID: "12345678-1234-4234-8234-123456789012", PoolID: "p", HostID: "h", Name: "guest", GuestOS: "linux",
		CPU:     VMCPU{Count: 4, Model: "host-passthrough"},
		Memory:  VMMemory{Mode: "dedicated", SizeMiB: 2048},
		Disks:   []VMDisk{{StorageID: "s", Name: "system", SizeGiB: 20, Bus: "virtio", BootOrder: 2}},
		NICs:    []VMNIC{{NetworkID: "n", Model: "virtio"}},
		Boot:    VMBoot{Firmware: "uefi", Machine: "q35", MediaBootOrder: 1},
		MediaID: "m",
	}
}

func specDisks(n int) []VMDisk {
	out := make([]VMDisk, n)
	for i := range out {
		out[i] = VMDisk{StorageID: "s", Name: fmt.Sprintf("disk%d", i), SizeGiB: 10, Bus: "virtio", BootOrder: i + 1}
	}
	return out
}

func specNICs(n int) []VMNIC {
	out := make([]VMNIC, n)
	for i := range out {
		out[i] = VMNIC{NetworkID: "n", Model: "virtio"}
	}
	return out
}

func TestValidateVMSpec(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*VMCreateSpec)
		valid bool
	}{
		{"unchanged", func(v *VMCreateSpec) {}, true},
		{"guest OS optional", func(v *VMCreateSpec) { v.GuestOS = "" }, true},
		{"guest OS windows", func(v *VMCreateSpec) { v.GuestOS = "windows" }, true},
		{"guest OS unknown", func(v *VMCreateSpec) { v.GuestOS = "plan9" }, false},
		{"description bounded", func(v *VMCreateSpec) { v.Description = strings.Repeat("a", 4096) }, true},
		{"description too long", func(v *VMCreateSpec) { v.Description = strings.Repeat("a", 4097) }, false},

		{"no vCPU", func(v *VMCreateSpec) { v.CPU.Count = 0 }, false},
		{"negative vCPU", func(v *VMCreateSpec) { v.CPU.Count = -1 }, false},
		{"topology matches", func(v *VMCreateSpec) { v.CPU.Sockets, v.CPU.Cores, v.CPU.Threads = 2, 2, 1 }, true},
		{"topology mismatch", func(v *VMCreateSpec) { v.CPU.Sockets, v.CPU.Cores, v.CPU.Threads = 2, 2, 2 }, false},
		{"topology partly set", func(v *VMCreateSpec) { v.CPU.Sockets = 4 }, false},
		{"topology zero factor", func(v *VMCreateSpec) { v.CPU.Sockets, v.CPU.Cores, v.CPU.Threads = 4, 1, 0 }, false},
		{"CPU model host-model", func(v *VMCreateSpec) { v.CPU.Model = "host-model" }, true},
		{"CPU model named", func(v *VMCreateSpec) { v.CPU.Model = "Skylake-Server" }, false},
		{"CPU model empty", func(v *VMCreateSpec) { v.CPU.Model = "" }, false},

		{"memory maximum", func(v *VMCreateSpec) { v.Memory.Mode = "maximum" }, true},
		{"memory mode unknown", func(v *VMCreateSpec) { v.Memory.Mode = "ballooned" }, false},
		{"memory mode empty", func(v *VMCreateSpec) { v.Memory.Mode = "" }, false},
		{"memory floor", func(v *VMCreateSpec) { v.Memory.SizeMiB = 512 }, true},
		{"memory below floor", func(v *VMCreateSpec) { v.Memory.SizeMiB = 511 }, false},

		{"firmware bios", func(v *VMCreateSpec) { v.Boot.Firmware = "bios" }, true},
		{"firmware unknown", func(v *VMCreateSpec) { v.Boot.Firmware = "coreboot" }, false},
		{"machine optional", func(v *VMCreateSpec) { v.Boot.Machine = "" }, true},
		{"machine unsafe", func(v *VMCreateSpec) { v.Boot.Machine = "q35 --enable-kvm" }, false},

		{"one disk", func(v *VMCreateSpec) { v.Disks, v.Boot.MediaBootOrder = specDisks(1), 2 }, true},
		{"eight disks", func(v *VMCreateSpec) { v.Disks, v.Boot.MediaBootOrder = specDisks(8), 0 }, true},
		{"no disks", func(v *VMCreateSpec) { v.Disks = nil }, false},
		{"nine disks", func(v *VMCreateSpec) { v.Disks, v.Boot.MediaBootOrder = specDisks(9), 0 }, false},
		{"disk without storage", func(v *VMCreateSpec) { v.Disks[0].StorageID = "" }, false},
		{"disk smallest", func(v *VMCreateSpec) { v.Disks[0].SizeGiB = 1 }, true},
		{"disk largest", func(v *VMCreateSpec) { v.Disks[0].SizeGiB = 2048 }, true},
		{"disk too small", func(v *VMCreateSpec) { v.Disks[0].SizeGiB = 0 }, false},
		{"disk too large", func(v *VMCreateSpec) { v.Disks[0].SizeGiB = 2049 }, false},
		{"disk thick", func(v *VMCreateSpec) { v.Disks[0].Thin = false }, true},
		{"disk thin", func(v *VMCreateSpec) { v.Disks[0].Thin = true }, true},
		{"disk bus sata", func(v *VMCreateSpec) { v.Disks[0].Bus = "sata" }, true},
		{"disk bus scsi", func(v *VMCreateSpec) { v.Disks[0].Bus = "scsi" }, true},
		{"disk bus unknown", func(v *VMCreateSpec) { v.Disks[0].Bus = "ide" }, false},
		{"disk name empty", func(v *VMCreateSpec) { v.Disks[0].Name = "" }, false},
		{"disk name path", func(v *VMCreateSpec) { v.Disks[0].Name = "sub/system" }, false},
		{"disk name traversal", func(v *VMCreateSpec) { v.Disks[0].Name = ".." }, false},
		{"disk name hidden", func(v *VMCreateSpec) { v.Disks[0].Name = ".system" }, false},
		{"disk name flag", func(v *VMCreateSpec) { v.Disks[0].Name = "-o" }, false},
		{"disk name space", func(v *VMCreateSpec) { v.Disks[0].Name = "system disk" }, false},
		{"disk name long", func(v *VMCreateSpec) { v.Disks[0].Name = strings.Repeat("d", 64) }, false},
		{"disk names duplicated", func(v *VMCreateSpec) {
			v.Disks = specDisks(2)
			v.Disks[1].Name, v.Boot.MediaBootOrder = v.Disks[0].Name, 3
		}, false},

		{"no NICs", func(v *VMCreateSpec) { v.NICs = nil }, true},
		{"eight NICs", func(v *VMCreateSpec) { v.NICs = specNICs(8) }, true},
		{"nine NICs", func(v *VMCreateSpec) { v.NICs = specNICs(9) }, false},
		{"NIC without network", func(v *VMCreateSpec) { v.NICs[0].NetworkID = "" }, false},
		{"NIC model e1000e", func(v *VMCreateSpec) { v.NICs[0].Model = "e1000e" }, true},
		// pcnet is a real QEMU model Nex deliberately does not offer: the list is an allowlist,
		// not a spell check.
		{"NIC model unsupported", func(v *VMCreateSpec) { v.NICs[0].Model = "pcnet" }, false},
		{"NIC model e1000", func(v *VMCreateSpec) { v.NICs[0].Model = "e1000" }, true},
		{"NIC model rtl8139", func(v *VMCreateSpec) { v.NICs[0].Model = "rtl8139" }, true},
		{"NIC model vmxnet3", func(v *VMCreateSpec) { v.NICs[0].Model = "vmxnet3" }, true},
		{"MTU on virtio", func(v *VMCreateSpec) { v.NICs[0].Model, v.NICs[0].MTU = "virtio", 9000 }, true},
		{"MTU on emulated model", func(v *VMCreateSpec) { v.NICs[0].Model, v.NICs[0].MTU = "e1000", 9000 }, false},
		{"MTU below IPv6 minimum", func(v *VMCreateSpec) { v.NICs[0].MTU = 1279 }, false},
		{"MTU above jumbo maximum", func(v *VMCreateSpec) { v.NICs[0].MTU = 9217 }, false},
		{"MTU unset", func(v *VMCreateSpec) { v.NICs[0].MTU = 0 }, true},
		{"NIC model empty", func(v *VMCreateSpec) { v.NICs[0].Model = "" }, false},

		{"MAC manual", func(v *VMCreateSpec) { v.NICs[0].MAC = "52:54:00:12:34:56" }, true},
		{"MAC derived", func(v *VMCreateSpec) { v.NICs[0].MAC = DerivedMAC(v.ID, 0) }, true},
		{"MAC multicast", func(v *VMCreateSpec) { v.NICs[0].MAC = "01:00:5e:00:00:01" }, false},
		{"MAC multicast odd octet", func(v *VMCreateSpec) { v.NICs[0].MAC = "53:54:00:12:34:56" }, false},
		{"MAC zero", func(v *VMCreateSpec) { v.NICs[0].MAC = "00:00:00:00:00:00" }, false},
		{"MAC upper case", func(v *VMCreateSpec) { v.NICs[0].MAC = "52:54:00:AB:CD:EF" }, false},
		{"MAC dashed", func(v *VMCreateSpec) { v.NICs[0].MAC = "52-54-00-12-34-56" }, false},
		{"MAC eight octets", func(v *VMCreateSpec) { v.NICs[0].MAC = "52:54:00:12:34:56:78:9a" }, false},
		{"MAC short", func(v *VMCreateSpec) { v.NICs[0].MAC = "52:54:00:12:34" }, false},
		{"MAC nonsense", func(v *VMCreateSpec) { v.NICs[0].MAC = "not-a-mac" }, false},
		{"MACs duplicated", func(v *VMCreateSpec) {
			v.NICs = specNICs(2)
			v.NICs[0].MAC, v.NICs[1].MAC = "52:54:00:12:34:56", "52:54:00:12:34:56"
		}, false},

		{"boot order from disk only", func(v *VMCreateSpec) { v.Disks[0].BootOrder, v.Boot.MediaBootOrder = 1, 0 }, true},
		{"boot order media only", func(v *VMCreateSpec) { v.Disks[0].BootOrder = 0 }, true},
		{"boot order duplicated", func(v *VMCreateSpec) { v.Disks[0].BootOrder = 1 }, false},
		{"boot order duplicated across disks", func(v *VMCreateSpec) {
			v.Disks, v.Boot.MediaBootOrder = specDisks(2), 0
			v.Disks[1].BootOrder = 1
		}, false},
		{"boot order gap", func(v *VMCreateSpec) { v.Disks[0].BootOrder = 3 }, false},
		{"boot order not from one", func(v *VMCreateSpec) { v.Disks[0].BootOrder, v.Boot.MediaBootOrder = 3, 2 }, false},
		{"boot order negative", func(v *VMCreateSpec) { v.Disks[0].BootOrder = -1 }, false},
		{"boot order absent", func(v *VMCreateSpec) { v.Disks[0].BootOrder, v.Boot.MediaBootOrder = 0, 0 }, false},
		{"media boot order without media", func(v *VMCreateSpec) { v.MediaID = "" }, false},
		{"no media without media boot order", func(v *VMCreateSpec) {
			v.MediaID, v.Boot.MediaBootOrder, v.Disks[0].BootOrder = "", 0, 1
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := validSpec()
			c.edit(&v)
			err := ValidateVMSpec(v)
			if c.valid && err != nil {
				t.Fatalf("rejected a valid specification: %v", err)
			}
			if !c.valid && err == nil {
				t.Fatal("accepted an invalid specification")
			}
		})
	}
}

func TestValidateVMSpecMemoryModes(t *testing.T) {
	for _, mode := range []string{"dedicated", "maximum"} {
		v := validSpec()
		v.Memory = VMMemory{Mode: mode, SizeMiB: 8192}
		v.CPU = VMCPU{Count: 8, Sockets: 2, Cores: 2, Threads: 2, Model: "host-model"}
		v.Disks = specDisks(3)
		v.Disks[1].Bus, v.Disks[1].Thin, v.Disks[2].Bus = "sata", true, "scsi"
		v.NICs = specNICs(3)
		v.NICs[1].Model, v.NICs[1].MAC = "e1000e", "52:54:00:aa:bb:cc"
		v.NICs[2].MAC = DerivedMAC(v.ID, 2)
		v.Boot.MediaBootOrder = 4
		if err := ValidateVMSpec(v); err != nil {
			t.Fatalf("%s: rejected a valid multi-disk multi-NIC specification: %v", mode, err)
		}
	}
}

func TestVMCPUTopology(t *testing.T) {
	cases := []struct {
		cpu      VMCPU
		s, c, th int
	}{
		{VMCPU{Count: 4}, 1, 4, 1},
		{VMCPU{Count: 1}, 1, 1, 1},
		{VMCPU{Count: 8, Sockets: 2, Cores: 2, Threads: 2}, 2, 2, 2},
		{VMCPU{Count: 6, Sockets: 1, Cores: 3, Threads: 2}, 1, 3, 2},
	}
	for _, c := range cases {
		s, co, th := c.cpu.Topology()
		if s != c.s || co != c.c || th != c.th {
			t.Fatalf("%+v: topology %d×%d×%d, want %d×%d×%d", c.cpu, s, co, th, c.s, c.c, c.th)
		}
		if s*co*th != c.cpu.Count {
			t.Fatalf("%+v: topology %d×%d×%d does not multiply to %d", c.cpu, s, co, th, c.cpu.Count)
		}
	}
}

func TestDerivedMAC(t *testing.T) {
	const id = "12345678-1234-4234-8234-123456789012"
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		m := DerivedMAC(id, i)
		if m != DerivedMAC(id, i) {
			t.Fatalf("index %d: derivation is not stable", i)
		}
		if seen[m] {
			t.Fatalf("index %d: %s repeats an earlier interface", i, m)
		}
		seen[m] = true
		hw, err := net.ParseMAC(m)
		if err != nil || len(hw) != 6 {
			t.Fatalf("index %d: %s is not a six-octet MAC: %v", i, m, err)
		}
		if hw[0]&1 != 0 {
			t.Fatalf("index %d: %s is multicast", i, m)
		}
		if hw[0]&2 == 0 {
			t.Fatalf("index %d: %s is not locally administered", i, m)
		}
		v := validSpec()
		v.NICs[0].MAC = m
		if err := ValidateVMSpec(v); err != nil {
			t.Fatalf("index %d: %s rejected by validation: %v", i, m, err)
		}
	}
	if DerivedMAC(id, 0) == DerivedMAC("87654321-1234-4234-8234-123456789012", 0) {
		t.Fatal("different VMs derive the same MAC")
	}
}
