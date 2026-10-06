// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestNativeHostCPUKeepsNativeOverrides(t *testing.T) {
	got, err := nativeHostCPU(context.Background(), "q35", func(ctx context.Context, args ...string) ([]byte, error) {
		if slices.Contains(args, "--expand-cpu-features") {
			t.Fatal("unsupported option")
		}
		return []byte(`<domainCapabilities><cpu><mode name="host-model" supported="yes"><model fallback="forbid">FutureCPU-v9</model><vendor>Intel</vendor><feature policy="require" name="aes"/><feature policy="disable" name="avx"/></mode></cpu></domainCapabilities>`), nil
	})
	if err != nil || !strings.Contains(got, `<model>FutureCPU-v9</model>`) || !strings.Contains(got, `name="avx" policy="disable"`) {
		t.Fatalf("lost inherited override: %s %v", got, err)
	}
}

func TestNativeCPUBaselineUsesPrivateNativeInputAndCanonicalIdentity(t *testing.T) {
	var path string
	cpus := []string{`<cpu><model>FutureCPU-v9</model><vendor>Intel</vendor><feature name="avx" policy="disable"/></cpu>`, `<cpu><model>OtherCPU-v4</model><vendor>Intel</vendor><feature name="aes" policy="require"/></cpu>`}
	got, err := nativeCPUBaseline(context.Background(), cpus, "q35", func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "domcapabilities" {
			return []byte(`<domainCapabilities><cpu><mode name="custom"><model canonical="NativeCommon-v2">NativeCommon</model></mode></cpu></domainCapabilities>`), nil
		}
		if args[0] != "hypervisor-cpu-baseline" || !slices.Contains(args, "--features") || !slices.Contains(args, "--migratable") {
			t.Fatalf("wrong native call %q", args)
		}
		path = args[1]
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe CPU input permissions: %v %v", info, err)
		}
		data, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(data), `name="avx" policy="disable"`) || strings.Count(string(data), "<cpu>") != 2 {
			t.Fatalf("lost native baseline input: %s %v", data, err)
		}
		return []byte(`<cpu><model>NativeCommon</model><vendor>Intel</vendor><feature policy="require" name="aes"/><feature policy="disable" name="avx"/></cpu>`), nil
	})
	if err != nil || got.Model != "NativeCommon-v2" || got.Machine != "q35" || !reflect.DeepEqual(got.Features, []protocol.CPUFeature{{Name: "aes", Policy: "require"}, {Name: "avx", Policy: "disable"}}) {
		t.Fatalf("unexpected native result %+v %v", got, err)
	}
	if _, err = os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("CPU input retained: %v", err)
	}
}

func TestNativeCPUFailuresDoNotGuess(t *testing.T) {
	for _, raw := range []string{"", `<wrong/>`, `<cpu><model>host-model</model></cpu>`, `<cpu><model>FutureCPU-v1</model><feature name="aes" policy="optional"/></cpu>`, `<cpu><model>FutureCPU-v1</model><feature name="aes" policy="require"/><feature name="aes" policy="disable"/></cpu>`} {
		if _, err := parseBaselineCPU([]byte(raw)); err == nil {
			t.Fatalf("invalid CPU accepted: %s", raw)
		}
	}
	calls := 0
	query := func(context.Context, ...string) ([]byte, error) { calls++; return nil, errors.New("offline") }
	if _, err := nativeHostCPU(context.Background(), "q35", query); err == nil {
		t.Fatal("failed host discovery guessed CPU")
	}
	if _, err := nativeCPUBaseline(context.Background(), []string{`<cpu><model>FutureCPU-v1</model></cpu>`}, "q35", query); err == nil {
		t.Fatal("failed native baseline guessed CPU")
	}
	if err := nativeCPUCompare(context.Background(), "FutureCPU-v1", []protocol.CPUFeature{{Name: "aes", Policy: "require"}}, "q35", query); err == nil {
		t.Fatal("failed native comparison accepted CPU")
	}
	if calls != 3 {
		t.Fatalf("unexpected calls %d", calls)
	}
}

func TestNativeCPUCompareChecksWholePersistedCPU(t *testing.T) {
	features := []protocol.CPUFeature{{Name: "avx", Policy: "disable"}, {Name: "aes", Policy: "require"}}
	err := nativeCPUCompare(context.Background(), "ArbitraryCPU-v7", features, "pc", func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "hypervisor-cpu-compare" || !slices.Contains(args, "--error") || !slices.Contains(args, "pc") {
			t.Fatalf("wrong compare %q", args)
		}
		b, err := os.ReadFile(args[1])
		if err != nil {
			t.Fatal(err)
		}
		cpu, err := parseBaselineCPU(b)
		if err != nil || cpu.Model != "ArbitraryCPU-v7" || !sameCPUFeatures(cpu.Features, specCPUFeatures(features)) {
			t.Fatalf("different compared CPU: %s %v", b, err)
		}
		return []byte("compatible"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestNativeCPUComparePreservesContextDeadline(t *testing.T) {
	err := nativeCPUCompare(context.Background(), "ArbitraryCPU-v7", []protocol.CPUFeature{{Name: "aes", Policy: "require"}}, "q35", func(context.Context, ...string) ([]byte, error) {
		return nil, context.DeadlineExceeded
	})
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "interrupted") {
		t.Fatalf("deadline = %v", err)
	}
	if strings.Contains(err.Error(), "not verified compatible") {
		t.Fatalf("deadline reported as CPU incompatibility: %v", err)
	}
}

func TestManagedCPUFeaturesRenderedAndVerified(t *testing.T) {
	v := managedTestVM()
	v.CPU.Model = "ArbitraryCPU-v7"
	v.CPU.Features = []protocol.CPUFeature{{Name: "aes", Policy: "require"}, {Name: "avx", Policy: "disable"}}
	want, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk-0"}, "/iso")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(want), `name="avx" policy="disable"`) {
		t.Fatalf("features not rendered: %s", want)
	}
	a := `<feature name="aes" policy="require"></feature>`
	b := `<feature name="avx" policy="disable"></feature>`
	reordered := strings.Replace(string(want), a+b, b+a, 1)
	if err := validateManagedDefinition([]byte(reordered), want); err != nil {
		t.Fatalf("order changed meaning: %v", err)
	}
	for _, changed := range []string{strings.Replace(string(want), a, "", 1), strings.Replace(string(want), `name="avx" policy="disable"`, `name="avx" policy="require"`, 1), strings.Replace(string(want), a, a+a, 1)} {
		if err := validateManagedDefinition([]byte(changed), want); err == nil {
			t.Fatal("changed CPU features accepted")
		}
	}
}

func TestCPUFeaturesRejectUnsupportedStartAndPreserveFeaturelessStart(t *testing.T) {
	calls := startLibvirt(t)
	lowMemoryHost(t, 16384)
	t.Setenv("VMHOST_STATE", "shut off")
	v := managedTestVM()
	v.CPU.Model = "FutureCPU-v1"
	v.CPU.Features = []protocol.CPUFeature{{Name: "aes", Policy: "require"}}
	if err := powerVM(context.Background(), protocol.VMPowerRequest{VMID: v.ID, Action: "start"}, &v); err == nil || !strings.Contains(err.Error(), "CPU baseline") {
		t.Fatalf("unsupported CPU accepted: %v", err)
	}
	if got := calls(); strings.Contains(got, "\nstart ") {
		t.Fatalf("unsupported baseline reached start: %s", got)
	}
	t.Setenv("VMHOST_STATE", "running")
	if err := powerVM(context.Background(), protocol.VMPowerRequest{VMID: v.ID, Action: "start"}, &v); err != nil {
		t.Fatal(err)
	}
	if got := calls(); strings.Contains(got, "hypervisor-cpu-compare") {
		t.Fatal("running no-op compared or changed CPU")
	}
	t.Setenv("VMHOST_STATE", "shut off")
	v.CPU.Features = nil
	if err := powerVM(context.Background(), protocol.VMPowerRequest{VMID: v.ID, Action: "start"}, &v); err != nil {
		t.Fatalf("featureless start changed: %v", err)
	}
	if got := calls(); !strings.Contains(got, "\nstart ") {
		t.Fatalf("featureless start lost: %s", got)
	}
}

func TestCPUDefinitionWithoutExplicitFeaturesKeepsNativeNormalization(t *testing.T) {
	v := managedTestVM()
	want, err := testDomainXML(v, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/disk-0"}, "/iso")
	if err != nil {
		t.Fatal(err)
	}
	normalized := strings.Replace(string(want), "</cpu>", `<feature name="native-extra" policy="require"/></cpu>`, 1)
	if err := validateManagedDefinition([]byte(normalized), want); err != nil {
		t.Fatalf("native normalization changed: %v", err)
	}
}

func TestExplicitCPUFeaturesUseNativeComparisonInsteadOfUnmodifiedCatalogue(t *testing.T) {
	lowMemoryHost(t, 16384)
	fakeCommand(t, "virsh", `case "$3" in hypervisor-cpu-compare) exit 0;; domcapabilities) printf '%s\n' '<domainCapabilities><domain>kvm</domain><arch>x86_64</arch><os supported="yes"/><cpu><mode name="custom"><model usable="no">FutureCPU-v1</model></mode></cpu></domainCapabilities>';; *) exit 2;; esac`)
	v := managedTestVM()
	v.CPU.Count = 1
	v.CPU.Model = "FutureCPU-v1"
	v.CPU.Features = []protocol.CPUFeature{{Name: "avx", Policy: "disable"}}
	v.Boot = protocol.VMBoot{Firmware: "bios", Machine: "q35"}
	if err := managedCapacity(context.Background(), v, nil); err != nil {
		t.Fatalf("verified adjusted baseline rejected by bare model list: %v", err)
	}
	v.CPU.Features = nil
	if err := managedCapacity(context.Background(), v, nil); err == nil {
		t.Fatal("unmodified unavailable model accepted")
	}
}
