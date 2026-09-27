// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"errors"
	"path/filepath"
	"slices"

	"nex.local/nex/internal/protocol"
)

// User aliases naming a VM's two CD drives, declared once for the package. libvirt requires the
// "ua-" prefix on an alias the definition supplies, keeps it across dumpxml and never reassigns
// it, so a media request can name a drive without depending on the target letters libvirt
// happened to assign. A domain defined before the second drive existed carries neither alias and
// has exactly one cdrom; that one is always the install drive (see observeGuest and cdromDrives).
const (
	cdromInstallAlias = "ua-nex-cdrom-install"
	cdromDriversAlias = "ua-nex-cdrom-drivers"
)

// driveAlias is the user alias for a drive name. "" is the install drive: that is what every
// Controller sent before the drivers drive existed.
func driveAlias(drive string) (string, error) {
	switch drive {
	case "", protocol.InstallDrive:
		return cdromInstallAlias, nil
	case protocol.DriversDrive:
		return cdromDriversAlias, nil
	}
	return "", errors.New("unknown CD drive " + drive)
}

// aliasXML is a libvirt device alias. Only the user aliases above are written; libvirt assigns
// its own (sata0-0-0 and the like) to every device it echoes back.
type aliasXML struct {
	Name string `xml:"name,attr"`
}

// firmwareFeatureXML states one UEFI firmware feature explicitly instead of letting libvirt
// pick a firmware build for the domain. Enabled is "yes" or "no".
type firmwareFeatureXML struct {
	Enabled string `xml:"enabled,attr"`
	Name    string `xml:"name,attr"`
}
type firmwareXML struct {
	Features []firmwareFeatureXML `xml:"feature"`
}

// nvramXML names the writable UEFI variable store.  It deliberately has no template
// attribute: libvirt's firmware auto-selection supplies the matching template while the
// explicit path keeps the guest state independent of the operator-visible domain name.
type nvramXML struct {
	Path string `xml:",chardata"`
}

// firmwareVariablesDir is package-scoped so focused tests can exercise the same ownership
// checks against a disposable directory. Production never changes it.
var firmwareVariablesDir = "/var/lib/libvirt/qemu/nvram"

func firmwareVariablesPath(id string) string {
	return filepath.Join(firmwareVariablesDir, id+"_VARS.fd")
}

// smmXML enables System Management Mode. OVMF stores the Secure Boot variables in SMRAM, so a
// Secure Boot domain that does not ask for SMM either fails to start or boots a firmware whose
// variables an unprivileged guest can rewrite.
type smmXML struct {
	State string `xml:"state,attr"`
}

type hypervStateXML struct {
	State string `xml:"state,attr"`
}
type hypervSpinlocksXML struct {
	State   string `xml:"state,attr"`
	Retries int    `xml:"retries,attr"`
}

// hypervXML carries the Hyper-V enlightenments a Windows guest looks for. Only the three that
// libvirt and virt-install have defaulted to for Windows for a decade are emitted: relaxed
// (do not panic on a long-stalled vCPU), vapic (MSR-based APIC access) and spinlocks (tell the
// guest how long to spin before yielding). vpindex, synic, stimer, reset, frequencies and
// tlbflush are deliberately absent: each is accepted by the QEMU and libvirt Nex Core ships
// (QEMU 10.0, libvirt 11.3) but each also asks KVM for a capability that a host or a nested
// hypervisor can lack, and a domain that then refuses to start is worse than a missing
// enlightenment. Add them only with evidence from a real host.
type hypervXML struct {
	Mode      string             `xml:"mode,attr,omitempty"`
	Relaxed   hypervStateXML     `xml:"relaxed"`
	VAPIC     hypervStateXML     `xml:"vapic"`
	Spinlocks hypervSpinlocksXML `xml:"spinlocks"`
}

// managedFeatures is the domain's <features> block. ACPI and APIC are required by every guest
// Nex defines; SMM and the Hyper-V enlightenments are conditional.
type managedFeatures struct {
	ACPI   struct{}   `xml:"acpi"`
	APIC   struct{}   `xml:"apic"`
	HyperV *hypervXML `xml:"hyperv,omitempty"`
	SMM    *smmXML    `xml:"smm,omitempty"`
}

// clockXML is emitted only for a Windows guest. Windows keeps local time in the RTC, and
// libvirt's default for an omitted <clock> is offset="utc", so a Windows guest without this
// reads the host's UTC as local time until it reaches a time server.
type clockXML struct {
	Offset string `xml:"offset,attr"`
}

// managedTPM is the emulated TPM. tpm-crb is the interface Windows 11 expects on a modern
// machine type; libvirt starts swtpm per domain and stores its state in
// /var/lib/libvirt/swtpm/<uuid>, including after restore.
type managedTPM struct {
	Model   string `xml:"model,attr"`
	Backend struct {
		Type    string            `xml:"type,attr"`
		Version string            `xml:"version,attr"`
		Source  *managedTPMSource `xml:"source,omitempty"`
	} `xml:"backend"`
}

type managedTPMSource struct {
	Type string `xml:"type,attr"`
	Path string `xml:"path,attr"`
}

func emulatedTPM() *managedTPM {
	t := &managedTPM{Model: "tpm-crb"}
	t.Backend.Type, t.Backend.Version = "emulator", "2.0"
	return t
}

func yesNo(on bool) string {
	if on {
		return "yes"
	}
	return "no"
}

// applyFirmware states Secure Boot explicitly rather than leaving libvirt to choose a firmware
// build for the domain. A UEFI definition that never named Secure Boot is Secure Boot on with
// the Microsoft keys enrolled, which is exactly what libvirt selected for it, so an installed
// guest keeps the firmware it was installed with. Turning it off selects the plain OVMF build
// and drops the enrolled keys so unsigned media and drivers boot.
func applyFirmware(d *managedDomain, boot protocol.VMBoot) {
	if boot.Firmware != "uefi" {
		return
	}
	d.OS.Firmware = "efi"
	d.OS.NVRAM = &nvramXML{Path: firmwareVariablesPath(d.UUID)}
	on := yesNo(boot.SecureBootEnabled())
	d.OS.FirmwareFeatures = &firmwareXML{Features: []firmwareFeatureXML{
		{Enabled: on, Name: "enrolled-keys"},
		{Enabled: on, Name: "secure-boot"},
	}}
	if boot.SecureBootEnabled() {
		d.Features.SMM = &smmXML{State: "on"}
	}
}

// applyGuestOSDefaults gives a Windows guest the RTC offset and enlightenments it expects.
// Every other guest OS value leaves the domain exactly as it was.
func applyGuestOSDefaults(d *managedDomain, guestOS string) {
	if guestOS != "windows" {
		return
	}
	d.Clock = &clockXML{Offset: "localtime"}
	d.Features.HyperV = &hypervXML{
		Mode:      "custom",
		Relaxed:   hypervStateXML{State: "on"},
		VAPIC:     hypervStateXML{State: "on"},
		Spinlocks: hypervSpinlocksXML{State: "on", Retries: 8191},
	}
}

// firmwareFeature reports whether a definition states the named firmware feature, and its
// value. A definition that does not mention the feature returns stated=false.
func firmwareFeature(os managedOS, name string) (enabled, stated bool) {
	if os.FirmwareFeatures == nil {
		return false, false
	}
	for _, f := range os.FirmwareFeatures.Features {
		if f.Name == name {
			return f.Enabled == "yes", true
		}
	}
	return false, false
}

// checkFirmwareAndTPM compares the security devices of an observed definition against the
// requested one. The TPM must be present exactly when it was asked for and carry the model that
// was asked for: a silently missing TPM is the defect W1 records, where Windows 11 Setup refuses
// the host with no indication of why. Secure Boot is enforced only where the observed definition
// states it, following how the balloon and MTU are treated: libvirt omitting a feature it
// resolved internally must not fail every VM, but libvirt stating the opposite of the request is
// a real difference.
func checkFirmwareAndTPM(got, want managedDomain) error {
	gotTPM, wantTPM := got.Devices.TPM, want.Devices.TPM
	if (gotTPM == nil) != (wantTPM == nil) {
		return errors.New("VM TPM differs from the requested configuration")
	}
	if wantTPM != nil && (gotTPM.Model != wantTPM.Model ||
		(wantTPM.Backend.Source != nil && (gotTPM.Backend.Source == nil || *gotTPM.Backend.Source != *wantTPM.Backend.Source))) {
		return errors.New("VM TPM differs from the requested configuration")
	}
	for _, name := range []string{"secure-boot", "enrolled-keys"} {
		wantOn, wantStated := firmwareFeature(want.OS, name)
		gotOn, gotStated := firmwareFeature(got.OS, name)
		if wantStated && gotStated && wantOn != gotOn {
			return errors.New("VM Secure Boot differs from the requested configuration")
		}
	}
	return nil
}

// hostTPMSupport reports whether this host can run an emulated TPM 2.0 on the machine type the
// capabilities describe. libvirt advertises the emulator backend only when swtpm is installed
// and usable, so this is the host's own answer rather than a guess from a package list. Core
// ships swtpm and swtpm-tools; a host that was upgraded from an image that did not, or where
// they were removed, must refuse the VM instead of defining a TPM that cannot start.
func hostTPMSupport(caps domainCapabilities) error {
	tpm := caps.Devices.TPM
	if tpm.Supported != "yes" {
		return errors.New("this host cannot provide a virtual TPM: install swtpm and swtpm-tools on Nex Core, then retry")
	}
	values := func(name string) []string {
		for _, e := range tpm.Enums {
			if e.Name == name {
				return e.Values
			}
		}
		return nil
	}
	if !slices.Contains(values("model"), "tpm-crb") {
		return errors.New("this host's libvirt does not offer the tpm-crb TPM model Windows 11 expects")
	}
	if !slices.Contains(values("backendModel"), "emulator") {
		return errors.New("this host has no emulated TPM backend: install swtpm and swtpm-tools on Nex Core, then retry")
	}
	// backendVersion is reported by the libvirt Core ships. An older libvirt that omits the
	// enum is not evidence that 2.0 is missing, so only a stated list is enforced.
	if v := values("backendVersion"); len(v) > 0 && !slices.Contains(v, "2.0") {
		return errors.New("this host's TPM emulator does not offer version 2.0")
	}
	return nil
}

// domainCapabilities is the part of virsh domcapabilities managedCapacity reads. The root
// element is deliberately not pinned: the fields below are what is checked, and an unexpected
// document fails on them rather than on its name.
// hostSecureBootSupport reports whether this host can build the firmware a Secure Boot VM asks
// for. Nex states secure-boot and enrolled-keys explicitly instead of letting libvirt choose
// (BUG-0199), so a host with no matching firmware build now fails the define with libvirt's own
// "Unable to find 'efi' firmware that is compatible with the current configuration" - true, but
// it never mentions Secure Boot, so the operator cannot tell which choice to change. libvirt
// publishes both answers in domcapabilities, so ask it, for the same reason the TPM check does:
// refuse in terms of the setting the operator picked (BUG-0215).
func hostSecureBootSupport(caps domainCapabilities, boot protocol.VMBoot) error {
	if boot.Firmware != "uefi" {
		return nil
	}
	// An older libvirt that reports no firmwareFeatures block is not evidence the firmware is
	// missing, so only a stated list is enforced, as with the TPM backendVersion enum above.
	if caps.OS.FirmwareFeatures.Supported != "yes" {
		return nil
	}
	values := func(name string) []string {
		for _, e := range caps.OS.FirmwareFeatures.Enums {
			if e.Name == name {
				return e.Values
			}
		}
		return nil
	}
	want := yesNo(boot.SecureBootEnabled())
	if v := values("secureBoot"); len(v) > 0 && !slices.Contains(v, want) {
		if boot.SecureBootEnabled() {
			return errors.New("this host has no UEFI firmware that can enforce Secure Boot: install the OVMF Secure Boot build on Nex Core, or turn Secure Boot off for this VM")
		}
		return errors.New("this host has no UEFI firmware build with Secure Boot disabled: leave Secure Boot on for this VM")
	}
	if v := values("enrolledKeys"); len(v) > 0 && boot.SecureBootEnabled() && !slices.Contains(v, "yes") {
		return errors.New("this host's UEFI firmware has no enrolled Secure Boot keys, so a signed installer such as Windows would not boot: install the enrolled-keys OVMF build on Nex Core, or turn Secure Boot off for this VM")
	}
	return nil
}

type domainCapabilities struct {
	Domain string `xml:"domain"`
	Arch   string `xml:"arch"`
	OS     struct {
		Supported        string           `xml:"supported,attr"`
		Enums            []capabilityEnum `xml:"enum"`
		FirmwareFeatures struct {
			Supported string           `xml:"supported,attr"`
			Enums     []capabilityEnum `xml:"enum"`
		} `xml:"firmwareFeatures"`
	} `xml:"os"`
	Devices struct {
		TPM struct {
			Supported string           `xml:"supported,attr"`
			Enums     []capabilityEnum `xml:"enum"`
		} `xml:"tpm"`
	} `xml:"devices"`
}
type capabilityEnum struct {
	Name   string   `xml:"name,attr"`
	Values []string `xml:"value"`
}
