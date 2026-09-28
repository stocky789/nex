// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"nex.local/nex/internal/protocol"
)

type cpuFeatureXML struct {
	Name   string `xml:"name,attr"`
	Policy string `xml:"policy,attr"`
}
type baselineCPUXML struct {
	XMLName  xml.Name        `xml:"cpu"`
	Model    string          `xml:"model"`
	Vendor   string          `xml:"vendor,omitempty"`
	Features []cpuFeatureXML `xml:"feature"`
}

func normalizeCPUFeatures(features []cpuFeatureXML) ([]cpuFeatureXML, error) {
	if len(features) > 512 {
		return nil, errors.New("CPU feature limit exceeded")
	}
	out := append([]cpuFeatureXML{}, features...)
	seen := map[string]bool{}
	for _, f := range out {
		if !protocol.ValidCPUModel(f.Name) || (f.Policy != "require" && f.Policy != "disable") || seen[f.Name] {
			return nil, errors.New("invalid or duplicate CPU feature policy")
		}
		seen[f.Name] = true
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func specCPUFeatures(features []protocol.CPUFeature) []cpuFeatureXML {
	out := make([]cpuFeatureXML, len(features))
	for i, f := range features {
		out[i] = cpuFeatureXML{Name: f.Name, Policy: f.Policy}
	}
	return out
}
func sameCPUFeatures(a, b []cpuFeatureXML) bool {
	left, err := normalizeCPUFeatures(a)
	if err != nil {
		return false
	}
	right, err := normalizeCPUFeatures(b)
	return err == nil && reflect.DeepEqual(left, right)
}
func parseBaselineCPU(b []byte) (baselineCPUXML, error) {
	var cpu baselineCPUXML
	if len(b) > 1<<20 || xml.Unmarshal(b, &cpu) != nil {
		return cpu, errors.New("invalid native CPU XML")
	}
	cpu.Model = strings.TrimSpace(cpu.Model)
	cpu.Vendor = strings.TrimSpace(cpu.Vendor)
	if !protocol.ValidCPUModel(cpu.Model) || cpu.Model == "host-model" || cpu.Model == "host-passthrough" || cpu.Model == "max" {
		return cpu, errors.New("native CPU did not provide a named baseline")
	}
	if cpu.Vendor != "" && !protocol.ValidCPUModel(cpu.Vendor) {
		return cpu, errors.New("invalid CPU vendor")
	}
	var err error
	cpu.Features, err = normalizeCPUFeatures(cpu.Features)
	return cpu, err
}
func cpuMachine(machine string) (string, error) {
	if machine == "" {
		machine = "q35"
	}
	if !protocol.ValidCPUModel(machine) {
		return "", errors.New("invalid CPU machine type")
	}
	return machine, nil
}

// NativeHostCPU reports the host-model baseline and its explicit native feature
// overrides. It does not confuse the host's feature list with a guest CPU model.
func NativeHostCPU(ctx context.Context, machine string) (string, error) {
	return nativeHostCPU(ctx, machine, managedVirsh)
}
func nativeHostCPU(ctx context.Context, machine string, query func(context.Context, ...string) ([]byte, error)) (string, error) {
	machine, err := cpuMachine(machine)
	if err != nil {
		return "", err
	}
	b, err := query(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", machine)
	if err != nil {
		return "", errors.New("cannot observe native host CPU capabilities")
	}
	var caps struct {
		XMLName xml.Name `xml:"domainCapabilities"`
		Modes   []struct {
			Name      string          `xml:"name,attr"`
			Supported string          `xml:"supported,attr"`
			Model     string          `xml:"model"`
			Vendor    string          `xml:"vendor"`
			Features  []cpuFeatureXML `xml:"feature"`
		} `xml:"cpu>mode"`
	}
	if len(b) > 1<<20 || xml.Unmarshal(b, &caps) != nil {
		return "", errors.New("invalid domain CPU capabilities")
	}
	for _, mode := range caps.Modes {
		if mode.Name != "host-model" || mode.Supported != "yes" {
			continue
		}
		raw, err := xml.Marshal(baselineCPUXML{Model: mode.Model, Vendor: mode.Vendor, Features: mode.Features})
		if err != nil {
			return "", err
		}
		cpu, err := parseBaselineCPU(raw)
		if err != nil {
			return "", err
		}
		raw, err = xml.Marshal(cpu)
		return string(raw), err
	}
	return "", errors.New("native host CPU baseline is unavailable")
}

// withCPUFile gives virsh a private, bounded native CPU document and removes it
// on every return, including cancellation and command failure.
func withCPUFile(data []byte, call func(string) ([]byte, error)) ([]byte, error) {
	f, err := os.CreateTemp("", "nex-cpu-*.xml")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	defer os.Remove(path)
	if _, err = f.Write(data); err != nil {
		f.Close()
		return nil, err
	}
	if err = f.Close(); err != nil {
		return nil, err
	}
	return call(path)
}

// NativeCPUBaseline delegates feature intersection and inherited feature handling
// to libvirt. The result is a candidate; every intended host must compare it.
func NativeCPUBaseline(ctx context.Context, cpus []string, machine string) (protocol.CPUBaseline, error) {
	return nativeCPUBaseline(ctx, cpus, machine, managedVirsh)
}
func nativeCPUBaseline(ctx context.Context, cpus []string, machine string, query func(context.Context, ...string) ([]byte, error)) (protocol.CPUBaseline, error) {
	out := protocol.CPUBaseline{}
	machine, err := cpuMachine(machine)
	if err != nil {
		return out, err
	}
	if len(cpus) == 0 || len(cpus) > 256 {
		return out, errors.New("CPU baseline requires between 1 and 256 host observations")
	}
	var doc strings.Builder
	for _, raw := range cpus {
		cpu, err := parseBaselineCPU([]byte(raw))
		if err != nil {
			return out, err
		}
		b, err := xml.Marshal(cpu)
		if err != nil {
			return out, err
		}
		if doc.Len()+len(b) > 16<<20 {
			return out, errors.New("CPU baseline observation size limit exceeded")
		}
		doc.Write(b)
		doc.WriteByte('\n')
	}
	b, err := withCPUFile([]byte(doc.String()), func(path string) ([]byte, error) {
		return query(ctx, "hypervisor-cpu-baseline", path, "--features", "--migratable", "--virttype", "kvm", "--arch", "x86_64", "--machine", machine)
	})
	if err != nil {
		return out, errors.New("native CPU baseline could not be established")
	}
	cpu, err := parseBaselineCPU(b)
	if err != nil {
		return out, err
	}
	caps, err := query(ctx, "domcapabilities", "--virttype", "kvm", "--arch", "x86_64", "--machine", machine)
	if err != nil {
		return out, errors.New("cannot resolve native CPU baseline identity")
	}
	var aliases struct {
		XMLName xml.Name `xml:"domainCapabilities"`
		Models  []struct {
			Name      string `xml:",chardata"`
			Canonical string `xml:"canonical,attr"`
		} `xml:"cpu>mode>model"`
	}
	if xml.Unmarshal(caps, &aliases) != nil {
		return out, errors.New("invalid native CPU model identities")
	}
	for _, m := range aliases.Models {
		if strings.TrimSpace(m.Name) == cpu.Model && m.Canonical != "" {
			if !protocol.ValidCPUModel(m.Canonical) {
				return out, errors.New("invalid canonical CPU model")
			}
			cpu.Model = m.Canonical
			break
		}
	}
	out.Model = cpu.Model
	out.Machine = machine
	out.Features = make([]protocol.CPUFeature, len(cpu.Features))
	for i, f := range cpu.Features {
		out.Features[i] = protocol.CPUFeature{Name: f.Name, Policy: f.Policy}
	}
	return out, nil
}

// NativeCPUCompare checks the full persisted guest CPU against this host's actual
// KVM runtime; a matching model name alone is not sufficient.
func NativeCPUCompare(ctx context.Context, model string, features []protocol.CPUFeature, machine string) error {
	return nativeCPUCompare(ctx, model, features, machine, managedVirsh)
}
func nativeCPUCompare(ctx context.Context, model string, features []protocol.CPUFeature, machine string, query func(context.Context, ...string) ([]byte, error)) error {
	machine, err := cpuMachine(machine)
	if err != nil {
		return err
	}
	raw, err := xml.Marshal(baselineCPUXML{Model: model, Features: specCPUFeatures(features)})
	if err != nil {
		return err
	}
	cpu, err := parseBaselineCPU(raw)
	if err != nil {
		return err
	}
	raw, err = xml.Marshal(cpu)
	if err != nil {
		return err
	}
	_, err = withCPUFile(raw, func(path string) ([]byte, error) {
		return query(ctx, "hypervisor-cpu-compare", path, "--error", "--virttype", "kvm", "--arch", "x86_64", "--machine", machine)
	})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return fmt.Errorf("CPU baseline comparison interrupted: %w", err)
		}
		return fmt.Errorf("CPU baseline %s is not verified compatible with this host's KVM runtime", model)
	}
	return nil
}
