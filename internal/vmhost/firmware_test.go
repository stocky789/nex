// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// parseDomain unmarshals a definition this package produced.
func parseDomain(t *testing.T, b []byte) managedDomain {
	t.Helper()
	var d managedDomain
	if err := xml.Unmarshal(b, &d); err != nil {
		t.Fatalf("definition does not parse: %v\n%s", err, b)
	}
	return d
}

func TestFirmwareVariablesStayWithVMIdentity(t *testing.T) {
	v := managedTestVM()
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	v.Name = "before-rename"
	before, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	v.Name, v.Revision = "after-rename", 1
	after, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := firmwareVariablesPath(v.ID)
	if got := parseDomain(t, before).OS.NVRAM; got == nil || got.Path != want {
		t.Fatalf("new UEFI VM variable store %+v, want %q", got, want)
	}
	if got := parseDomain(t, after).OS.NVRAM; got == nil || got.Path != want {
		t.Fatalf("rename changed firmware variable store to %+v, want %q", got, want)
	}
	// A definition from before explicit paths retains libvirt's resolved path during a stopped
	// rename, rather than replacing its installed guest state with a blank UUID store.
	legacy := parseDomain(t, before)
	legacy.OS.NVRAM.Path = filepath.Join(firmwareVariablesDir, "before-rename_VARS.fd")
	legacyXML, err := xml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	carried, err := carryFirmwareVariables(after, legacyXML)
	if err != nil {
		t.Fatal(err)
	}
	if got := parseDomain(t, carried).OS.NVRAM.Path; got != legacy.OS.NVRAM.Path {
		t.Fatalf("legacy variable store changed to %q", got)
	}
	legacy.OS.NVRAM = nil
	legacyXML, err = xml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = carryFirmwareVariables(after, legacyXML); err == nil {
		t.Fatal("existing UEFI VM without an observed variable store was reset to a blank store")
	}
}

func TestSecureBootVariablesPreserveGuestStateAndRecover(t *testing.T) {
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	// The fake exposes exactly the Debian tool's JSON contract and copies all unrelated
	// variables. Boot#### and PK stand in for guest boot entries and custom enrolled keys.
	fakeCommand(t, "virt-fw-vars", `
input=""
output=""
json=""
mode=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --input) input="$2"; shift 2;;
    --output) output="$2"; shift 2;;
    --output-json) json="$2"; shift 2;;
	--secure-boot) mode=on; shift;;
	--set-false) mode=off; shift 2;;
    *) shift;;
  esac
done
if [ -n "$json" ]; then
  secure=$(sed -n 's/^secure=//p' "$input")
  setup=$(sed -n 's/^setup=//p' "$input")
  pk=$(sed -n 's/^pk=//p' "$input")
  boot=$(sed -n 's/^Boot0001=//p' "$input")
  order=$(sed -n 's/^BootOrder=//p' "$input")
  [ "$secure" = 1 ] && secure=01 || secure=00
  [ "$setup" = 0 ] && setup=00 || setup=01
  [ -n "$pk" ] && pk=aa
  printf '{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"%s"},{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"%s"},{"name":"PK","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"%s"},{"name":"Boot0001","data":"%s"},{"name":"BootOrder","data":"%s"}]}' "$secure" "$setup" "$pk" "$boot" "$order"
  exit 0
fi
cp "$input" "$output"
case "$mode" in
  on) sed -i 's/^secure=.*/secure=1/' "$output";;
  off) sed -i 's/^secure=.*/secure=0/' "$output";;
esac`)
	vm := managedTestVM()
	vm.Boot.SecureBoot = protocol.SecureBootOff
	domain, err := testDomainXML(vm, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	nvram := firmwareVariablesPath(vm.ID)
	if err = os.WriteFile(nvram, []byte("secure=1\nsetup=0\npk=custom\nBoot0001=guest\nBootOrder=0001\n"), 0640); err != nil {
		t.Fatal(err)
	}
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "secure-boot-off", Kind: "vm-update", VM: &vm}, Domain: domain, SecureBootPreviousDomain: domain, Stage: secureBootVariablesStage, SecureBootEnabled: false}
	if err = e.prepareSecureBootVariables(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if err = e.finishSecureBootVariables(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(nvram)
	if err != nil || !strings.Contains(string(got), "secure=0") || !strings.Contains(string(got), "Boot0001=guest") || !strings.Contains(string(got), "pk=custom") || j.Stage != secureBootDefiningStage {
		t.Fatalf("Secure Boot update lost guest state: %q err=%v stage=%q", got, err, j.Stage)
	}
	if info, err := os.Stat(nvram); err != nil || info.Mode().Perm() != 0640 {
		t.Fatalf("Secure Boot update changed NVRAM mode: %v %v", info, err)
	}
	// A crash after rename but before the final journal save observes the desired store and
	// advances without rerunning the native tool or replacing the guest's variables.
	j.Stage = secureBootVariablesStage
	if err = e.finishSecureBootVariables(context.Background(), &j); err != nil || j.Stage != secureBootDefiningStage {
		t.Fatalf("post-rename recovery: %v stage=%q", err, j.Stage)
	}
}

func TestSecureBootEnableRefusesUnenrolledVariables(t *testing.T) {
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	fakeCommand(t, "virt-fw-vars", `printf '{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"00"},{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"01"}]}'`)
	vm := managedTestVM()
	vm.Boot.SecureBoot = protocol.SecureBootOn
	domain, err := testDomainXML(vm, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	nvram := firmwareVariablesPath(vm.ID)
	if err = os.WriteFile(nvram, []byte("secure=0\nsetup=1\nBoot0001=guest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "secure-boot-on", Kind: "vm-update", VM: &vm}, Domain: domain, SecureBootPreviousDomain: domain, Stage: secureBootVariablesStage, SecureBootEnabled: true}
	if err = e.prepareSecureBootVariables(context.Background(), &j); err == nil || !strings.Contains(err.Error(), "no enrolled Secure Boot keys") {
		t.Fatalf("unenrolled firmware variables accepted: %v", err)
	}
	got, _ := os.ReadFile(nvram)
	if !strings.Contains(string(got), "Boot0001=guest") {
		t.Fatalf("refusal changed guest state: %q", got)
	}
}

func TestSecureBootTemporaryRejectsUnsafePathAndPreservesXattr(t *testing.T) {
	old := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = old }()
	source := firmwareVariablesPath("vm")
	if err := os.WriteFile(source, []byte("vars"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Setxattr(source, "user.nex-test", []byte("kept"), 0); err == nil {
		temp, err := secureBootTemporary(source, 0600, uint32(os.Getuid()), uint32(os.Getgid()))
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 16)
		n, err := syscall.Getxattr(temp, "user.nex-test", buf)
		if err != nil || string(buf[:n]) != "kept" {
			t.Fatalf("xattr not preserved: %q %v", buf[:n], err)
		}
		if err = removeSecureBootTemporary(temp); err != nil {
			t.Fatal(err)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(firmwareVariablesDir, "bad")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := removeSecureBootTemporary(link); err == nil {
		t.Fatal("symlink temporary accepted")
	}
	if got, _ := os.ReadFile(outside); string(got) != "keep" {
		t.Fatalf("unsafe unlink touched outside file: %q", got)
	}
	if err := removeSecureBootTemporary(outside); err == nil {
		t.Fatal("outside temporary accepted")
	}
}

func TestFirmwareVariableStateRejectsSpoofedOrDuplicateCriticalVariables(t *testing.T) {
	for _, output := range []string{
		`{"variables":[{"name":"SecureBootEnable","guid":"00000000-0000-0000-0000-000000000000","data":"01"}]}`,
		`{"variables":[{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"00"},{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"01"}]}`,
		`{"variables":[{"name":"Boot0001","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"one"},{"name":"Boot0001","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"two"}]}`,
	} {
		t.Run("reject", func(t *testing.T) {
			fakeCommand(t, "virt-fw-vars", "printf '%s' '"+output+"'")
			if _, _, err := firmwareVariablesState(context.Background(), "/unused"); err == nil {
				t.Fatal("ambiguous firmware JSON accepted")
			}
		})
	}
}

func TestFirmwareVariableStateAcceptsEnrolledOfflineOVMFStore(t *testing.T) {
	// OVMF's offline VARS store contains SecureBootEnable at EDK2's vendor GUID and PK at
	// the global-variable GUID. SetupMode is runtime-derived and absent from this store.
	fakeCommand(t, "virt-fw-vars", `printf '%s' '{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"00"},{"name":"PK","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"a1b2"},{"name":"Boot0001","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"guest"}]}'`)
	enabled, enrolled, err := firmwareVariablesState(context.Background(), "/unused")
	if err != nil || enabled || !enrolled {
		t.Fatalf("offline OVMF state enabled=%t enrolled=%t err=%v", enabled, enrolled, err)
	}
}

func TestFirmwareVariableStateRejectsUnenrolledEvidence(t *testing.T) {
	for _, output := range []string{
		`{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"00"},{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"00"}]}`,
		`{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"00"},{"name":"SetupMode","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"01"},{"name":"PK","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"a1b2"}]}`,
	} {
		t.Run("reject", func(t *testing.T) {
			fakeCommand(t, "virt-fw-vars", "printf '%s' '"+output+"'")
			_, enrolled, err := firmwareVariablesState(context.Background(), "/unused")
			if err != nil || enrolled {
				t.Fatalf("unenrolled firmware state accepted: enrolled=%t err=%v", enrolled, err)
			}
		})
	}
}

func TestSecureBootVariablesPreservedRejectsWrongGUIDControlVariable(t *testing.T) {
	fakeCommand(t, "virt-fw-vars", `
input=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --input) input="$2"; shift 2;;
    *) shift;;
  esac
done
cat "$input"`)
	dir := t.TempDir()
	before := filepath.Join(dir, "before.json")
	after := filepath.Join(dir, "after.json")
	if err := os.WriteFile(before, []byte(`{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"01"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	// The native tool may add its legitimate vendor-GUID control variable, but it must never
	// add a same-named value under another GUID.
	if err := os.WriteFile(after, []byte(`{"variables":[{"name":"SecureBootEnable","guid":"f0a30bc7-af08-4556-99c4-001009c93a44","data":"00"},{"name":"SecureBootEnable","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"00"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := secureBootVariablesPreserved(context.Background(), before, after, false); err == nil {
		t.Fatal("wrong-GUID SecureBootEnable addition was accepted")
	}
	if err := os.WriteFile(before, []byte(`{"variables":[{"name":"SecureBootEnable","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"01"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(after, []byte(`{"variables":[{"name":"SecureBootEnable","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"00"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := secureBootVariablesPreserved(context.Background(), before, after, false); err == nil {
		t.Fatal("wrong-GUID SecureBootEnable change was accepted")
	}
}

func TestSecureBootVariablesPreservedAcceptsOnlyImageSecurityDatabaseDBX(t *testing.T) {
	fakeCommand(t, "virt-fw-vars", `
input=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    --input) input="$2"; shift 2;;
    *) shift;;
  esac
done
cat "$input"`)
	dir := t.TempDir()
	before := filepath.Join(dir, "before.json")
	after := filepath.Join(dir, "after.json")
	if err := os.WriteFile(before, []byte(`{"variables":[{"name":"dbx","guid":"d719b2cb-3d3a-4596-a3bc-dad00e67656f","data":"old"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(after, []byte(`{"variables":[{"name":"dbx","guid":"d719b2cb-3d3a-4596-a3bc-dad00e67656f","data":"new"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := secureBootVariablesPreserved(context.Background(), before, after, true); err != nil {
		t.Fatalf("legitimate dbx update rejected: %v", err)
	}
	if err := os.WriteFile(after, []byte(`{"variables":[{"name":"dbx","guid":"8be4df61-93ca-11d2-aa0d-00e098032b8c","data":"new"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := secureBootVariablesPreserved(context.Background(), before, after, true); err == nil {
		t.Fatal("global-GUID dbx replacement was accepted")
	}
}

func TestSecureBootDefinitionRecoveryDefinesOnlyProvenPredecessor(t *testing.T) {
	vm := managedTestVM()
	old := vm
	vm.Name, vm.Boot.SecureBoot = "renamed-guest", protocol.SecureBootOff
	target, err := testDomainXML(vm, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	previous, err := testDomainXML(old, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "domain.xml")
	if err = os.WriteFile(state, previous, 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `case "$3" in
dumpxml) cat "$VMHOST_STATE";;
define) cp "$4" "$VMHOST_STATE";;
*) exit 2;; esac`)
	t.Setenv("VMHOST_STATE", state)
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "secure-boot-define", Kind: "vm-update", VM: &vm}, Domain: target, SecureBootPreviousDomain: previous, Stage: secureBootDefiningStage, SecureBootEnabled: false}
	// This is the crash window after the atomic NVRAM rename and before XML define: only the
	// exact predecessor permits the pending define.
	if err = e.finishSecureBootDefinition(context.Background(), &j); err != nil || j.Stage != secureBootDefiningStage {
		t.Fatalf("predecessor recovery: %v stage=%q", err, j.Stage)
	}
	got, err := os.ReadFile(state)
	if err != nil || validateManagedDefinition(got, target) != nil {
		t.Fatalf("recovery did not define requested XML: %q err=%v", got, err)
	}
	// This is the crash window after virsh define: observation sees the target and accepts it
	// without another define.
	if err = e.finishSecureBootDefinition(context.Background(), &j); err != nil || j.Stage != "defined" {
		t.Fatalf("post-define recovery: %v stage=%q", err, j.Stage)
	}
	// A field Core does not model is still part of the recorded definition. Recovery must not
	// accept it and overwrite it just because the resource projection happens to match.
	changed := strings.Replace(string(previous), "</domain>", "<description>operator change</description></domain>", 1)
	if err = os.WriteFile(state, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	j.Stage = secureBootDefiningStage
	if err = e.finishSecureBootDefinition(context.Background(), &j); err == nil {
		t.Fatal("non-firmware XML change was redefined")
	}
	if err = os.WriteFile(state, []byte("<domain><name>other</name></domain>"), 0600); err != nil {
		t.Fatal(err)
	}
	j.Stage = secureBootDefiningStage
	if err = e.finishSecureBootDefinition(context.Background(), &j); err == nil {
		t.Fatal("unrecognised persistent definition was redefined")
	}
}

func TestRemoteSecureBootRecoveryValidatesBeforeChangingVariables(t *testing.T) {
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	e, _, next, _, _ := remoteUpdateEngine(t, secureBootVariablesStage, 2*time.Minute)
	nvram := firmwareVariablesPath(next.ID)
	if err := os.WriteFile(nvram, []byte("secure=1\nsetup=0\npk=custom\nBoot0001=guest\n"), 0600); err != nil {
		t.Fatal(err)
	}
	toolCalls := filepath.Join(t.TempDir(), "tool-calls")
	fakeCommand(t, "virt-fw-vars", `echo called >> "$VMHOST_TOOL_CALLS"; exit 2`)
	t.Setenv("VMHOST_TOOL_CALLS", toolCalls)
	j, err := e.readJournal("remote-update.json")
	if err != nil {
		t.Fatal(err)
	}
	j.SecureBootEnabled = false
	j.SecureBootPreviousDomain = j.Domain
	if err = e.prepareSecureBootVariables(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if err = e.ReconcileRemoteVMUpdate(context.Background(), func(context.Context, protocol.VMCreateSpec) error {
		return errors.New("pool execution permit lost")
	}); err != nil {
		t.Fatal(err)
	}
	got, readErr := os.ReadFile(nvram)
	if readErr != nil || !strings.Contains(string(got), "secure=1") {
		t.Fatalf("remote validation changed variables: %q err=%v", got, readErr)
	}
	if calls, _ := os.ReadFile(toolCalls); len(calls) != 0 {
		t.Fatalf("remote validation invoked native tool: %q", calls)
	}
}

// BUG-0214: libvirt only deletes NVRAM while its persistent definition still names UEFI.
// The cleanup is journaled before undefine and can resume once an interrupted undefine is
// observed to have removed the old domain.
func TestFinishFirmwareRemovalCleansNVRAMAndRetriesSafely(t *testing.T) {
	state, calls := filepath.Join(t.TempDir(), "domain.xml"), filepath.Join(t.TempDir(), "virsh.log")
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	old := managedTestVM()
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	old.Name = "uefi-guest"
	uefi, err := testDomainXML(old, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(state, uefi, 0600); err != nil {
		t.Fatal(err)
	}
	nvram := firmwareVariablesPath(old.ID)
	if err = os.WriteFile(nvram, []byte("firmware variables"), 0600); err != nil {
		t.Fatal(err)
	}
	next := old
	next.Revision, next.Name, next.Boot.Firmware, next.Boot.SecureBoot = 1, "bios-guest", "bios", ""
	bios, err := testDomainXML(next, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_CALLS"
case "$3" in
dumpxml) [ -f "$VMHOST_STATE" ] && cat "$VMHOST_STATE" || exit 1;;
list) ;;
undefine) [ "$5" = "--nvram" ] || exit 2; rm -f "$VMHOST_STATE";;
define) cp "$4" "$VMHOST_STATE";;
*) exit 2;; esac`)
	t.Setenv("VMHOST_STATE", state)
	t.Setenv("VMHOST_CALLS", calls)
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "firmware-to-bios", Kind: "vm-update", VM: &next}, Domain: bios, Stage: "cleaning-nvram"}
	if err = e.finishFirmwareRemoval(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(state)
	if err != nil || validateManagedDefinition(got, bios) != nil || j.Stage != "defining" {
		t.Fatalf("BIOS definition after cleanup: %q %v stage=%q", got, err, j.Stage)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "undefine "+old.ID+" --nvram") || !strings.Contains(string(log), "define ") {
		t.Fatalf("cleanup calls %q", log)
	}
	if _, err = os.Stat(nvram); !os.IsNotExist(err) {
		t.Fatalf("NVRAM left after libvirt undefine: %v", err)
	}
	// A crash after the BIOS definition lands still removes the recorded variable store before
	// accepting that observed definition.
	if err = os.WriteFile(nvram, []byte("left by libvirt"), 0600); err != nil {
		t.Fatal(err)
	}
	j.Stage = "cleaning-nvram"
	if err = e.finishFirmwareRemoval(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(nvram); !os.IsNotExist(err) || j.Stage != "defined" {
		t.Fatalf("BIOS-observed cleanup left NVRAM: %v stage=%q", err, j.Stage)
	}
	// The crash window after undefine has no domain. Observation proves that state, then
	// defining the journaled BIOS XML is safe and never repeats the destructive call.
	if err = os.Remove(state); err != nil {
		t.Fatal(err)
	}
	os.Remove(calls)
	j.Command.OperationID = "firmware-to-bios-retry"
	j.Stage = "cleaning-nvram"
	if err = e.finishFirmwareRemoval(context.Background(), &j); err != nil {
		t.Fatal(err)
	}
	log, _ = os.ReadFile(calls)
	if strings.Contains(string(log), "undefine") || !strings.Contains(string(log), "define ") {
		t.Fatalf("absent-domain retry calls %q", log)
	}
}

func TestReconcileRemoteVMUpdateFinishesFirmwareCleanupAfterValidation(t *testing.T) {
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	e, previous, next, previousDomain, _ := remoteUpdateEngine(t, "cleaning-nvram", 2*time.Minute)
	next.Name, next.Boot.Firmware, next.Boot.SecureBoot = "bios-remote-guest", "bios", ""
	bios, err := testDomainXML(next, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/remote/nex-vms/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	nvram := firmwareVariablesPath(previous.ID)
	if err = os.WriteFile(nvram, []byte("firmware variables"), 0600); err != nil {
		t.Fatal(err)
	}
	j, err := e.readJournal("remote-update.json")
	if err != nil {
		t.Fatal(err)
	}
	j.Command.VM, j.Domain, j.NVRAMPath = &next, bios, nvram
	if err = e.save(j); err != nil {
		t.Fatal(err)
	}
	state, calls := filepath.Join(t.TempDir(), "domain.xml"), filepath.Join(t.TempDir(), "calls")
	if err = os.WriteFile(state, previousDomain, 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_CALLS"
case "$3" in
dumpxml) [ -f "$VMHOST_STATE" ] && cat "$VMHOST_STATE" || exit 1;;
list) ;;
undefine) rm -f "$VMHOST_STATE";;
define) cp "$4" "$VMHOST_STATE";;
*) exit 2;; esac`)
	t.Setenv("VMHOST_STATE", state)
	t.Setenv("VMHOST_CALLS", calls)
	observed := 0
	e.observe = func(_ context.Context, got managedJournal, state string) error {
		observed++
		if got.Stage != "defining" || state != "shut off" {
			t.Fatalf("remote cleanup observation %+v state=%q", got, state)
		}
		return nil
	}
	validated := 0
	if err = e.ReconcileRemoteVMUpdate(context.Background(), func(_ context.Context, got protocol.VMCreateSpec) error {
		validated++
		if got.ID != next.ID {
			t.Fatalf("validated wrong VM %+v", got)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := e.readJournal("remote-update.json"); err != nil || got.Result.Status != "succeeded" || got.Stage != "defined" || observed != 1 || validated != 1 {
		t.Fatalf("remote cleanup journal %+v err=%v observed=%d validated=%d", got, err, observed, validated)
	}
	if _, err = os.Stat(nvram); !os.IsNotExist(err) {
		t.Fatalf("remote cleanup left NVRAM: %v", err)
	}
	log, _ := os.ReadFile(calls)
	if !strings.Contains(string(log), "undefine "+previous.ID+" --nvram") || !strings.Contains(string(log), "define ") {
		t.Fatalf("remote cleanup calls %q", log)
	}

	// Validation runs before the cleanup's native effects. A lost pool execution permit leaves
	// both the definition and variable store untouched for a later eligible reconciliation.
	e2, previous2, next2, previousDomain2, _ := remoteUpdateEngine(t, "cleaning-nvram", 2*time.Minute)
	next2.Boot.Firmware, next2.Boot.SecureBoot = "bios", ""
	bios2, err := testDomainXML(next2, []protocol.NetworkRegistration{{Kind: "network", Source: "default"}}, []string{"/remote/nex-vms/disk"}, "")
	if err != nil {
		t.Fatal(err)
	}
	nvram2 := firmwareVariablesPath(previous2.ID)
	if err = os.WriteFile(nvram2, []byte("firmware variables"), 0600); err != nil {
		t.Fatal(err)
	}
	j2, _ := e2.readJournal("remote-update.json")
	j2.Command.VM, j2.Domain, j2.NVRAMPath = &next2, bios2, nvram2
	if err = e2.save(j2); err != nil {
		t.Fatal(err)
	}
	state2, calls2 := filepath.Join(t.TempDir(), "domain.xml"), filepath.Join(t.TempDir(), "calls")
	if err = os.WriteFile(state2, previousDomain2, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VMHOST_STATE", state2)
	t.Setenv("VMHOST_CALLS", calls2)
	if err = e2.ReconcileRemoteVMUpdate(context.Background(), func(context.Context, protocol.VMCreateSpec) error {
		return errors.New("remote VM storage is not currently eligible")
	}); err != nil {
		t.Fatal(err)
	}
	if log, _ := os.ReadFile(calls2); len(log) != 0 {
		t.Fatalf("ineligible remote cleanup issued native calls: %q", log)
	}
	if _, err = os.Stat(nvram2); err != nil {
		t.Fatalf("ineligible remote cleanup removed NVRAM: %v", err)
	}
}

// W1/BUG-0196: Windows 11 refuses to install without TPM 2.0. A VM asking for one gets an
// emulated tpm-crb device; every other VM keeps exactly the devices it had, because a TPM that
// appears on an existing guest changes its hardware identity and can invalidate anything sealed
// to the platform.
func TestManagedDomainXMLTPM(t *testing.T) {
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	for _, tc := range []struct {
		name, tpm string
		want      bool
	}{
		{"unset", "", false},
		{"none", protocol.TPMNone, false},
		{"2.0", protocol.TPM20, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := managedTestVM()
			v.Boot.TPM = tc.tpm
			b, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
			if err != nil {
				t.Fatal(err)
			}
			d := parseDomain(t, b)
			if (d.Devices.TPM != nil) != tc.want {
				t.Fatalf("TPM presence %v, want %v: %s", d.Devices.TPM != nil, tc.want, b)
			}
			if !tc.want {
				if strings.Contains(string(b), "<tpm") {
					t.Fatalf("no TPM was requested: %s", b)
				}
				return
			}
			if d.Devices.TPM.Model != "tpm-crb" || d.Devices.TPM.Backend.Type != "emulator" || d.Devices.TPM.Backend.Version != "2.0" {
				t.Fatalf("emulated TPM 2.0 expected: %+v", d.Devices.TPM)
			}
		})
	}
}

// W4/BUG-0199: Secure Boot is stated explicitly so libvirt no longer picks a firmware build
// from the rest of the domain. The compatibility case is the important one: a UEFI VM whose
// journaled specification never named Secure Boot must keep the Secure Boot, enrolled keys and
// SMM libvirt had already chosen for it, or a VM with Windows installed stops booting.
func TestManagedDomainXMLSecureBoot(t *testing.T) {
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	feature := func(t *testing.T, d managedDomain, name string) string {
		t.Helper()
		if d.OS.FirmwareFeatures == nil {
			t.Fatalf("UEFI definition must state its firmware features: %+v", d.OS)
		}
		for _, f := range d.OS.FirmwareFeatures.Features {
			if f.Name == name {
				return f.Enabled
			}
		}
		t.Fatalf("firmware feature %s is not stated: %+v", name, d.OS.FirmwareFeatures)
		return ""
	}
	for _, tc := range []struct {
		name, value, enabled string
		smm                  bool
	}{
		{"default-keeps-secure-boot", protocol.SecureBootDefault, "yes", true},
		{"on", protocol.SecureBootOn, "yes", true},
		{"off", protocol.SecureBootOff, "no", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := managedTestVM()
			v.Boot.SecureBoot = tc.value
			b, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
			if err != nil {
				t.Fatal(err)
			}
			d := parseDomain(t, b)
			if d.OS.Firmware != "efi" {
				t.Fatalf("UEFI definition: %s", b)
			}
			if got := feature(t, d, "secure-boot"); got != tc.enabled {
				t.Fatalf("secure-boot=%q, want %q: %s", got, tc.enabled, b)
			}
			// The enrolled Microsoft keys are what make Secure Boot able to verify a stock
			// Windows or shim boot loader; turning Secure Boot off must also drop them so
			// unsigned media boots.
			if got := feature(t, d, "enrolled-keys"); got != tc.enabled {
				t.Fatalf("enrolled-keys=%q, want %q: %s", got, tc.enabled, b)
			}
			// OVMF keeps the Secure Boot variables in SMRAM; without SMM the domain either
			// refuses to start or exposes them to the guest.
			if (d.Features.SMM != nil) != tc.smm {
				t.Fatalf("SMM presence %v, want %v: %s", d.Features.SMM != nil, tc.smm, b)
			}
			if tc.smm && d.Features.SMM.State != "on" {
				t.Fatalf("SMM must be on: %s", b)
			}
		})
	}
	// A BIOS VM has neither firmware features nor SMM, and asking for Secure Boot on BIOS is
	// refused by the shared specification rather than silently ignored here.
	v := managedTestVM()
	v.Boot.Firmware = "bios"
	b, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	d := parseDomain(t, b)
	if d.OS.Firmware != "" || d.OS.FirmwareFeatures != nil || d.Features.SMM != nil {
		t.Fatalf("BIOS VM must not carry UEFI firmware features: %s", b)
	}
	v.Boot.SecureBoot = protocol.SecureBootOn
	if protocol.ValidateVMSpec(v) == nil {
		t.Fatal("Secure Boot on BIOS firmware accepted")
	}
}

// W15/BUG-0210: Guest OS = Windows must change devices. Windows keeps local time in the RTC and
// looks for the Hyper-V enlightenments; every other guest OS keeps the definition it had.
func TestManagedDomainXMLWindowsDefaults(t *testing.T) {
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	v := managedTestVM()
	v.GuestOS = "windows"
	b, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	d := parseDomain(t, b)
	if d.Clock == nil || d.Clock.Offset != "localtime" {
		t.Fatalf("a Windows guest keeps local time in the RTC: %s", b)
	}
	if d.Features.HyperV == nil {
		t.Fatalf("a Windows guest needs the Hyper-V enlightenments: %s", b)
	}
	hv := d.Features.HyperV
	if hv.Relaxed.State != "on" || hv.VAPIC.State != "on" || hv.Spinlocks.State != "on" || hv.Spinlocks.Retries != 8191 {
		t.Fatalf("hyperv %+v", hv)
	}

	for _, guestOS := range []string{"linux", "other", ""} {
		v.GuestOS = guestOS
		if b, err = testDomainXML(v, nets, []string{"/disk-0.qcow2"}, ""); err != nil {
			t.Fatal(err)
		}
		d = parseDomain(t, b)
		if d.Clock != nil || d.Features.HyperV != nil {
			t.Fatalf("guest OS %q must keep the definition it had: %s", guestOS, b)
		}
		if strings.Contains(string(b), "<clock") || strings.Contains(string(b), "hyperv") {
			t.Fatalf("guest OS %q must keep the definition it had: %s", guestOS, b)
		}
	}
}

// W3/BUG-0198: a driver ISO has to sit beside the install ISO, so every VM carries two CD
// drives. The aliases are the contract a media request resolves a drive by; the target letters
// are libvirt's and must not be depended on.
func TestManagedDomainXMLTwoCDDrives(t *testing.T) {
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	v := managedTestVM()
	b, err := testDomainXMLWithDrivers(v, nets, []string{"/disk-0.qcow2"}, "/iso/windows.iso", "/iso/virtio-win.iso")
	if err != nil {
		t.Fatal(err)
	}
	d := parseDomain(t, b)
	drives := cdromDrives(d)
	install, drivers := drives[protocol.InstallDrive], drives[protocol.DriversDrive]
	if install == nil || drivers == nil {
		t.Fatalf("both CD drives must exist: %s", b)
	}
	if install.Alias == nil || install.Alias.Name != "ua-nex-cdrom-install" || drivers.Alias == nil || drivers.Alias.Name != "ua-nex-cdrom-drivers" {
		t.Fatalf("CD drives must carry their user aliases: %s", b)
	}
	if diskFile(*install) != "/iso/windows.iso" || diskFile(*drivers) != "/iso/virtio-win.iso" {
		t.Fatalf("install %q drivers %q", diskFile(*install), diskFile(*drivers))
	}
	if install.Target.Dev == drivers.Target.Dev || install.Target.Bus != "sata" || drivers.Target.Bus != "sata" {
		t.Fatalf("CD drives need distinct SATA targets: %+v %+v", install.Target, drivers.Target)
	}
	if install.Readonly == nil || drivers.Readonly == nil {
		t.Fatal("both CD drives must be read-only")
	}
	// The driver disc never gets a boot order: it holds drivers an installer loads from inside
	// Setup, and a bootable entry for it would change what the VM boots.
	if bootOrder(install.Boot) != v.Boot.MediaBootOrder || drivers.Boot != nil {
		t.Fatalf("install boot order %d, drivers boot %+v", bootOrder(install.Boot), drivers.Boot)
	}
	// An empty driver disc still leaves the drive: SATA cannot hot-plug, so a disc can only be
	// inserted later if the tray already exists.
	if b, err = testDomainXMLWithDrivers(v, nets, []string{"/disk-0.qcow2"}, "/iso/windows.iso", ""); err != nil {
		t.Fatal(err)
	}
	drives = cdromDrives(parseDomain(t, b))
	if drives[protocol.DriversDrive] == nil || drives[protocol.DriversDrive].Source != nil {
		t.Fatalf("an empty driver disc still leaves its drive: %s", b)
	}
	// The two drives must never collide, and a definition that cannot name both is refused
	// rather than silently defining one.
	targets, media, drivers2 := managedTargets(v)
	if media == drivers2 {
		t.Fatalf("CD targets collide: %q", media)
	}
	if _, err = managedDomainXML(v, nets, []string{"/d0"}, targets, media, "", media, ""); err == nil {
		t.Fatal("a definition reusing one target for both CD drives was accepted")
	}
	if _, err = managedDomainXML(v, nets, []string{"/d0"}, targets, media, "", "", ""); err == nil {
		t.Fatal("a definition without a driver disc target was accepted")
	}
}

// The drive a media request names is resolved from the domain libvirt actually holds. A domain
// defined by an older Core has no aliases and one cdrom: that one is always the install drive,
// and its driver disc drive is reported absent rather than guessed, because inserting a disc
// into the wrong tray of a running guest cannot be undone from the outside.
func TestCDROMDriveResolution(t *testing.T) {
	parse := func(body string) managedDomain {
		var d managedDomain
		if err := xml.Unmarshal([]byte(body), &d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	legacy := parse(`<domain><devices>` +
		`<disk device="disk" type="file"><target dev="vda" bus="virtio"/></disk>` +
		`<disk device="cdrom" type="file"><source file="/iso/win.iso"/><target dev="sda" bus="sata"/><readonly/></disk>` +
		`</devices></domain>`)
	drives := cdromDrives(legacy)
	if drives[protocol.InstallDrive] == nil || drives[protocol.InstallDrive].Target.Dev != "sda" {
		t.Fatalf("the single unaliased cdrom is the install drive: %+v", drives)
	}
	if drives[protocol.DriversDrive] != nil {
		t.Fatalf("an older domain has no driver disc drive: %+v", drives[protocol.DriversDrive])
	}

	// libvirt echoes its own aliases on every device; only the Nex user aliases name a drive.
	libvirtAliases := parse(`<domain><devices>` +
		`<disk device="cdrom" type="file"><target dev="sda" bus="sata"/><alias name="sata0-0-0"/></disk>` +
		`</devices></domain>`)
	if got := cdromDrives(libvirtAliases)[protocol.InstallDrive]; got == nil || got.Target.Dev != "sda" {
		t.Fatalf("a libvirt-assigned alias must not hide the install drive: %+v", got)
	}

	// Aliased drives are resolved by alias whatever order or target letters libvirt reports.
	both := parse(`<domain><devices>` +
		`<disk device="cdrom" type="file"><target dev="sdd" bus="sata"/><alias name="ua-nex-cdrom-drivers"/></disk>` +
		`<disk device="cdrom" type="file"><source file="/iso/win.iso"/><target dev="sdc" bus="sata"/><alias name="ua-nex-cdrom-install"/></disk>` +
		`</devices></domain>`)
	drives = cdromDrives(both)
	if drives[protocol.InstallDrive] == nil || drives[protocol.InstallDrive].Target.Dev != "sdc" {
		t.Fatalf("install drive resolved by alias: %+v", drives[protocol.InstallDrive])
	}
	if drives[protocol.DriversDrive] == nil || drives[protocol.DriversDrive].Target.Dev != "sdd" {
		t.Fatalf("drivers drive resolved by alias: %+v", drives[protocol.DriversDrive])
	}
	if cdromDrives(parse(`<domain><devices/></domain>`))[protocol.InstallDrive] != nil {
		t.Fatal("a domain with no cdrom has no install drive")
	}
	if _, err := driveAlias("nonsense"); err == nil {
		t.Fatal("unknown drive accepted")
	}
}

// managedCurrent places the driver disc drive beside the targets an older journal entry already
// holds, so adding the drive never renames a device the guest has.
func TestDriversTargetAvoidsExistingDevices(t *testing.T) {
	if got := driversTarget([]string{"vda"}, "sda"); got != "sdb" {
		t.Fatalf("drivers target %q", got)
	}
	// A SATA data disk at sdb pushes the driver disc drive past it rather than colliding.
	if got := driversTarget([]string{"vda", "sdb"}, "sda"); got != "sdc" {
		t.Fatalf("drivers target %q", got)
	}
	e := NewManaged(t.TempDir())
	if err := os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	v := managedTestVM()
	entry := managedJournal{
		Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &v},
		Result:  protocol.Result{OperationID: "create", Status: "succeeded"},
		Disks:   []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda",
	}
	if err := e.save(entry); err != nil {
		t.Fatal(err)
	}
	js, err := e.journals()
	if err != nil {
		t.Fatal(err)
	}
	cur, err := managedCurrent(js, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.Targets[0] != "vda" || cur.MediaTarget != "sda" || cur.DriversTarget != "sdb" {
		t.Fatalf("existing targets kept, driver disc added: %+v", cur)
	}
}

// W1/BUG-0196: swtpm keeps a VM's TPM state under /var/lib/libvirt/swtpm/<uuid>, including its
// endorsement key and anything the guest sealed to it. Undefining without --tpm leaves it behind,
// which leaks those secrets and makes a later VM with the same UUID fail to start.
func TestManagedDeleteRemovesTPMState(t *testing.T) {
	for _, tc := range []struct {
		name string
		tpm  string
		want bool
	}{{"with-tpm", protocol.TPM20, true}, {"without-tpm", "", false}} {
		t.Run(tc.name, func(t *testing.T) {
			vm := managedTestVM()
			vm.MediaID, vm.Boot.TPM = "", tc.tpm
			disk := filepath.Join(t.TempDir(), "disk-0.qcow2")
			if err := os.WriteFile(disk, []byte("guest-data"), 0600); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(t.TempDir(), "virsh.log")
			fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"; case "$3" in
domstate) echo 'shut off';;
list) ;;
undefine) exit 0;;
*) exit 2;; esac`)
			t.Setenv("VMHOST_LOG", log)
			e := NewManaged(filepath.Join(t.TempDir(), "state"))
			if err := os.MkdirAll(e.stateDir, 0700); err != nil {
				t.Fatal(err)
			}
			create := protocol.Command{OperationID: "create-1", Kind: "vm-create", VM: &vm}
			if err := e.save(managedJournal{
				Command: create,
				Result:  protocol.Result{OperationID: create.OperationID, Status: "succeeded"},
				Stage:   "defined",
				Disks:   []string{disk},
				Targets: []string{"vda"}, MediaTarget: "sda", DriversTarget: "sdb",
			}); err != nil {
				t.Fatal(err)
			}
			del := protocol.Command{OperationID: "del-1", Kind: "vm-delete", Delete: &protocol.VMDeleteRequest{VMID: vm.ID, PoolID: vm.PoolID, HostID: vm.HostID}}
			r, err := e.Execute(context.Background(), del, nil)
			if err != nil || r.Status != "succeeded" {
				t.Fatalf("delete: %+v %v", r, err)
			}
			calls, _ := os.ReadFile(log)
			// UEFI keeps its variables in an NVRAM file; both are per-VM state the undefine
			// must remove, and neither flag may be dropped by adding the other.
			if !strings.Contains(string(calls), "undefine "+vm.ID+" --nvram") {
				t.Fatalf("UEFI VM must undefine its NVRAM: %q", calls)
			}
			if got := strings.Contains(string(calls), "--tpm"); got != tc.want {
				t.Fatalf("undefine --tpm present=%v, want %v: %q", got, tc.want, calls)
			}
		})
	}
}

// A TPM the host cannot run is worse than no TPM: libvirt accepts the definition and every
// start then fails. managedCapacity refuses first, naming the missing host support. libvirt
// advertises the emulator backend only when swtpm is installed, so this is the host's own
// answer rather than a guess from a package list.
func TestManagedCapacityRefusesTPMWithoutSwtpm(t *testing.T) {
	const withTPM = `<tpm supported="yes"><enum name="model"><value>tpm-tis</value><value>tpm-crb</value></enum>` +
		`<enum name="backendModel"><value>passthrough</value><value>emulator</value></enum>` +
		`<enum name="backendVersion"><value>2.0</value></enum></tpm>`
	// A host without swtpm: libvirt still reports the TPM device, with passthrough only.
	const withoutSwtpm = `<tpm supported="yes"><enum name="model"><value>tpm-tis</value><value>tpm-crb</value></enum>` +
		`<enum name="backendModel"><value>passthrough</value></enum></tpm>`
	for _, tc := range []struct {
		name, devices, refusal string
	}{
		{"supported", withTPM, ""},
		{"no-swtpm", withoutSwtpm, "install swtpm and swtpm-tools"},
		{"no-tpm-device", `<tpm supported="no"/>`, "install swtpm and swtpm-tools"},
		{"absent", ``, "install swtpm and swtpm-tools"},
		{"no-crb-model", `<tpm supported="yes"><enum name="model"><value>tpm-tis</value></enum><enum name="backendModel"><value>emulator</value></enum></tpm>`, "tpm-crb"},
		{"no-2.0", `<tpm supported="yes"><enum name="model"><value>tpm-crb</value></enum><enum name="backendModel"><value>emulator</value></enum><enum name="backendVersion"><value>1.2</value></enum></tpm>`, "version 2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lowMemoryHost(t, 65536)
			fakeCommand(t, "virsh", `printf '%s\n' '<domainCapabilities><domain>kvm</domain><arch>x86_64</arch>`+
				`<os supported="yes"><enum name="firmware"><value>efi</value></enum></os>`+
				`<devices>`+tc.devices+`</devices></domainCapabilities>'`)
			v := managedTestVM()
			v.CPU.Count = 1
			v.Boot.TPM = protocol.TPM20
			err := managedCapacity(context.Background(), v, nil)
			if tc.refusal == "" {
				if err != nil {
					t.Fatalf("a host with swtpm must accept a TPM: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.refusal) {
				t.Fatalf("refusal naming %q, got %v", tc.refusal, err)
			}
			// The same host must still accept a VM that asks for no TPM.
			v.Boot.TPM = ""
			if err = managedCapacity(context.Background(), v, nil); err != nil {
				t.Fatalf("a VM without a TPM must not be refused: %v", err)
			}
		})
	}
}

// Observation must catch a host that dropped the TPM or inverted Secure Boot, and must tolerate
// a libvirt that resolved a firmware feature without echoing it, the way the balloon attribute
// and the adapter MTU are treated.
func TestValidateManagedDefinitionFirmwareAndTPM(t *testing.T) {
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	v := managedTestVM()
	v.Boot.TPM = protocol.TPM20
	want, err := testDomainXML(v, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = validateManagedDefinition(want, want); err != nil {
		t.Fatalf("a definition must match itself: %v", err)
	}
	dropped := strings.Replace(string(want), `<tpm model="tpm-crb">`, `<ignored model="tpm-crb">`, 1)
	dropped = strings.Replace(dropped, "</tpm>", "</ignored>", 1)
	if err = validateManagedDefinition([]byte(dropped), want); err == nil || !strings.Contains(err.Error(), "TPM") {
		t.Fatalf("a silently missing TPM must be refused: %v", err)
	}
	inverted := strings.Replace(string(want), `<feature enabled="yes" name="secure-boot">`, `<feature enabled="no" name="secure-boot">`, 1)
	if inverted == string(want) {
		t.Fatalf("fixture did not mutate: %s", want)
	}
	if err = validateManagedDefinition([]byte(inverted), want); err == nil || !strings.Contains(err.Error(), "Secure Boot") {
		t.Fatalf("an inverted Secure Boot state must be refused: %v", err)
	}
	// libvirt resolving the firmware without echoing the features back is not a difference.
	silent := strings.Replace(string(want), `<feature enabled="yes" name="secure-boot"></feature>`, "", 1)
	silent = strings.Replace(silent, `<feature enabled="yes" name="enrolled-keys"></feature>`, "", 1)
	if err = validateManagedDefinition([]byte(silent), want); err != nil {
		t.Fatalf("libvirt omitting the firmware features must not fail the VM: %v", err)
	}
}

// W3/BUG-0198: a media request names a drive. Inserting the driver disc must reach the drivers
// tray, record into drivers_media_id and leave the install media and the boot order alone
// (BUG-0188: an eject that rewrote the boot order made every eject of a running install-media-
// first VM demand a stop).
func TestManagedMediaDriversDrive(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "virsh.log")
	isoDir := t.TempDir()
	installPath := filepath.Join(isoDir, "media-1.iso")
	driversPath := filepath.Join(isoDir, "media-2.iso")
	body, install := managedTestISO()
	drivers := install
	drivers.ID, drivers.Name = "media-2", "virtio-win.iso"
	if err := os.WriteFile(installPath, body, 0444); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driversPath, body, 0444); err != nil {
		t.Fatal(err)
	}
	vm := "11111111-2222-4333-a444-555555555555"
	domain := `<domain><devices>` +
		`<disk device="disk" type="file"><target dev="vda" bus="virtio"/></disk>` +
		`<disk device="cdrom" type="file"><source file="` + installPath + `"/><target dev="sda" bus="sata"/><readonly/><boot order="2"/><alias name="ua-nex-cdrom-install"/></disk>` +
		`<disk device="cdrom" type="file"><target dev="sdb" bus="sata"/><readonly/><alias name="ua-nex-cdrom-drivers"/></disk>` +
		`</devices></domain>`
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_LOG"
case "$3" in
domstate) printf '%s\n' "$VMHOST_STATE";;
dumpxml) printf '%s\n' "$VMHOST_XML";;
attach-device) cat "$5" >> "$VMHOST_LOG"; printf '\n' >> "$VMHOST_LOG"; exit 0;;
change-media) exit 0;;
*) exit 2;;
esac`)
	t.Setenv("VMHOST_LOG", log)
	t.Setenv("VMHOST_STATE", "running")
	t.Setenv("VMHOST_XML", domain)
	e := NewManaged(dir)
	spec := protocol.VMCreateSpec{
		ID: vm, PoolID: "pool1", HostID: "host1", Name: "guest", GuestOS: "windows", MediaID: "media-1",
		CPU: protocol.VMCPU{Count: 1, Model: "host-passthrough"}, Memory: protocol.VMMemory{Mode: "dedicated", SizeMiB: 512},
		Disks: []protocol.VMDisk{{StorageID: "storage-1", Name: "system", SizeGiB: 10, Bus: "virtio", BootOrder: 1}},
		Boot:  protocol.VMBoot{Firmware: "uefi", MediaBootOrder: 2},
	}
	for _, j := range []managedJournal{
		{Command: protocol.Command{OperationID: "st", Kind: "storage-register", Storage: &protocol.StorageRegistration{ID: "storage-1", PoolID: "pool1", HostID: "host1", Name: "local", Path: isoDir}}, Result: protocol.Result{OperationID: "st", Status: "succeeded"}},
		{Command: protocol.Command{OperationID: "iso1", Kind: "iso-import", Media: &install}, Result: protocol.Result{OperationID: "iso1", Status: "succeeded"}, MediaPath: installPath},
		{Command: protocol.Command{OperationID: "iso2", Kind: "iso-import", Media: &drivers}, Result: protocol.Result{OperationID: "iso2", Status: "succeeded"}, MediaPath: driversPath},
		{Command: protocol.Command{OperationID: "create", Kind: "vm-create", VM: &spec}, Result: protocol.Result{OperationID: "create", Status: "succeeded"}, MediaPath: installPath, Disks: []string{"/d0"}, Targets: []string{"vda"}, MediaTarget: "sda", DriversTarget: "sdb"},
	} {
		b, _ := json.Marshal(j)
		if err := os.WriteFile(filepath.Join(dir, j.Command.OperationID+".json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	calls := func() string { b, _ := os.ReadFile(log); os.Remove(log); return string(b) }
	command := func(op, action, drive, mediaID string) protocol.Command {
		return protocol.Command{OperationID: op, Kind: "vm-media", VMMedia: &protocol.VMMediaRequest{VMID: vm, PoolID: "pool1", HostID: "host1", Action: action, Drive: drive, MediaID: mediaID}}
	}

	if r, err := e.Execute(context.Background(), command("d1", "insert", protocol.DriversDrive, "media-2"), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("insert into the driver disc drive: %v %+v", err, r)
	}
	if got := calls(); !strings.Contains(got, "change-media "+vm+" sdb --force --live --config "+driversPath+" --update") {
		t.Fatalf("driver disc must reach sdb: %q", got)
	}
	applied, _ := e.Applied(context.Background())
	if len(applied.VMs) != 1 || applied.VMs[0].DriversMediaID != "media-2" {
		t.Fatalf("driver disc must be recorded in drivers_media_id: %+v", applied.VMs)
	}
	// The install drive and the boot order are untouched by a drivers-drive request.
	if applied.VMs[0].MediaID != "media-1" || applied.VMs[0].Boot.MediaBootOrder != 2 {
		t.Fatalf("install media and boot order must be untouched: %+v", applied.VMs[0])
	}

	// An eject of the drivers drive clears only that record. The fixture domain's drivers tray
	// is empty, so nothing is issued to libvirt and the empty drive stays in place.
	if r, err := e.Execute(context.Background(), command("d2", "eject", protocol.DriversDrive, ""), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("eject the driver disc: %v %+v", err, r)
	}
	applied, _ = e.Applied(context.Background())
	if applied.VMs[0].DriversMediaID != "" || applied.VMs[0].MediaID != "media-1" || applied.VMs[0].Boot.MediaBootOrder != 2 {
		t.Fatalf("eject must clear only the driver disc: %+v", applied.VMs[0])
	}

	// An install-drive request still reaches the install tray, by alias rather than by position.
	calls()
	if r, err := e.Execute(context.Background(), command("d3", "eject", protocol.InstallDrive, ""), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("eject the install media: %v %+v", err, r)
	}
	if got := calls(); !strings.Contains(got, "change-media "+vm+" sda --force --live --config --eject") {
		t.Fatalf("install eject must reach sda: %q", got)
	}

	// A drivers-drive request against a VM defined before the second drive existed must say so
	// instead of inserting the disc into the install tray.
	t.Setenv("VMHOST_XML", `<domain><devices><disk device="cdrom" type="file"><target dev="sda" bus="sata"/><readonly/></disk></devices></domain>`)
	r, _ := e.Execute(context.Background(), command("d4", "insert", protocol.DriversDrive, "media-2"), nil)
	if r.Status != "failed" || !strings.Contains(r.Error, "no driver disc drive while running") {
		t.Fatalf("older domain must refuse a drivers insert while running: %+v", r)
	}
	// Stopped, the drive is added to the persistent definition and the disc goes into it.
	t.Setenv("VMHOST_STATE", "shut off")
	calls()
	if r, err := e.Execute(context.Background(), command("d5", "insert", protocol.DriversDrive, "media-2"), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("stopped drivers insert: %v %+v", err, r)
	}
	got := calls()
	if !strings.Contains(got, "attach-device "+vm) || !strings.Contains(got, "change-media "+vm+" sdb --force --config "+driversPath+" --update") {
		t.Fatalf("stopped drivers insert argv %q", got)
	}
	if !strings.Contains(got, "--config") || strings.Contains(got, "--live") {
		t.Fatalf("a stopped VM takes config-only changes: %q", got)
	}
	// The tray it attaches carries the drivers alias, so the next request resolves it.
	if !strings.Contains(got, `name="ua-nex-cdrom-drivers"`) {
		t.Fatalf("an attached tray must carry its drive alias: %q", got)
	}

	// An unknown drive is refused before anything is issued.
	bad := command("d6", "insert", "floppy", "media-2")
	if r, _ = e.Execute(context.Background(), bad, nil); r.Status != "failed" || !strings.Contains(r.Error, "CD drive") {
		t.Fatalf("unknown drive must be refused: %+v", r)
	}

	// BUG-0235: Core refuses an insert of the ISO its own record holds in the other drive, so no
	// Controller can make it record a definition ValidateVMSpec rejects. Nothing reaches libvirt.
	calls()
	if r, _ = e.Execute(context.Background(), command("d7", "insert", protocol.InstallDrive, "media-2"), nil); r.Status != "failed" || !strings.Contains(r.Error, "already in the driver disc drive") {
		t.Fatalf("install insert of the driver disc ISO must be refused: %+v", r)
	}
	if got := calls(); got != "" {
		t.Fatalf("a refused insert must not call virsh: %q", got)
	}
	// Re-inserting the same ISO into its own drive stays allowed.
	if r, err := e.Execute(context.Background(), command("d8", "insert", protocol.DriversDrive, "media-2"), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("re-insert into the same drive: %v %+v", err, r)
	}
	if r, err := e.Execute(context.Background(), command("d9", "insert", protocol.InstallDrive, "media-1"), nil); err != nil || r.Status != "succeeded" {
		t.Fatalf("install insert of a different ISO: %v %+v", err, r)
	}
	calls()
	if r, _ = e.Execute(context.Background(), command("d10", "insert", "", "media-1"), nil); r.Status != "succeeded" {
		t.Fatalf("re-insert into the install drive (legacy empty drive name): %+v", r)
	}
	calls()
	if r, _ = e.Execute(context.Background(), command("d11", "insert", protocol.DriversDrive, "media-1"), nil); r.Status != "failed" || !strings.Contains(r.Error, "already in the install drive") {
		t.Fatalf("drivers insert of the install ISO must be refused: %+v", r)
	}
	if got := calls(); got != "" {
		t.Fatalf("a refused insert must not call virsh: %q", got)
	}
	applied, _ = e.Applied(context.Background())
	if applied.VMs[0].MediaID != "media-1" || applied.VMs[0].DriversMediaID != "media-2" {
		t.Fatalf("a refused insert must leave the record unchanged: %+v", applied.VMs[0])
	}
}

// A VM defined before the driver disc drive existed is edited while running: SATA cannot
// hot-plug, so the drive is added to the persistent definition alone and appears at the next
// start. Without this the expected definition could never match and every live edit of such a
// VM would fail its observation.
func TestApplyLiveAddsDriversDriveToOlderDomain(t *testing.T) {
	calls := filepath.Join(t.TempDir(), "calls")
	t.Setenv("MANAGED_CALLS", calls)
	v := managedTestVM()
	v.NICs = nil // Only the CD drive differs, so every attach below is the drive itself.
	// The persistent definition an older Core left: one unaliased cdrom, no drivers drive.
	legacy := `<domain type="kvm"><name>guest</name><uuid>` + v.ID + `</uuid><devices>` +
		`<disk device="disk" type="file"><source file="/vm/disk-0.qcow2"/><target dev="vda" bus="virtio"/></disk>` +
		`<disk device="cdrom" type="file"><target dev="sda" bus="sata"/><readonly/></disk>` +
		`</devices></domain>`
	legacyPath := filepath.Join(t.TempDir(), "legacy.xml")
	if err := os.WriteFile(legacyPath, []byte(legacy), 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", `echo "$@" >> "$MANAGED_CALLS"
case "$3" in
dumpxml) cat "${MANAGED_XML:-`+legacyPath+`}";;
attach-device) cat "$5" >> "$MANAGED_CALLS"; printf '\n' >> "$MANAGED_CALLS";;
esac`)
	expected, err := managedDomainXML(v, nil, []string{"/vm/disk-0.qcow2"}, []string{"vda"}, "sda", "", "sdb", "")
	if err != nil {
		t.Fatal(err)
	}
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = e.applyLive(context.Background(), v.ID, expected, "", ""); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(calls)
	if !strings.Contains(string(b), "attach-device "+v.ID) || !strings.Contains(string(b), `name="ua-nex-cdrom-drivers"`) {
		t.Fatalf("the driver disc drive must be added: %s", b)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		// SATA cannot hot-plug: the drive is a persistent-definition change only.
		if strings.Contains(line, "attach-device") && (strings.Contains(line, "--persistent") || strings.Contains(line, "--live")) {
			t.Fatalf("the driver disc drive must be added config-only: %q", line)
		}
	}
	// The install tray is empty in both definitions, so no media change is issued: adding the
	// drive must not disturb what the guest already has.
	if strings.Contains(string(b), "change-media") {
		t.Fatalf("adding the drive must not change media: %s", b)
	}
	fragment := filepath.Join(e.stateDir, v.ID+".device.xml")
	if _, err = os.Stat(fragment); !os.IsNotExist(err) {
		t.Fatalf("the device fragment must not be left behind: %v", err)
	}
}

// Lab-derived: this data is the shape real libvirt reports. Before the enrolled-keys OVMF build
// was installed, an Arch host advertised secureBoot yes/no but enrolledKeys "no" only, and the
// define failed with libvirt's "Unable to find 'efi' firmware that is compatible with the
// current configuration" - true, but it never names Secure Boot, so the operator cannot tell
// which choice to change. Nex asks the host first and refuses in terms of the setting (BUG-0215).
func TestHostSecureBootSupportRefusesWhatTheHostCannotBuild(t *testing.T) {
	parse := func(t *testing.T, x string) domainCapabilities {
		t.Helper()
		var c domainCapabilities
		if err := xml.Unmarshal([]byte(x), &c); err != nil {
			t.Fatalf("parse capabilities: %v", err)
		}
		return c
	}
	const noEnrolled = `<domainCapabilities><os supported='yes'>
	  <firmwareFeatures supported='yes'>
	    <enum name='secureBoot'><value>yes</value><value>no</value></enum>
	    <enum name='enrolledKeys'><value>no</value></enum>
	  </firmwareFeatures></os></domainCapabilities>`
	const withEnrolled = `<domainCapabilities><os supported='yes'>
	  <firmwareFeatures supported='yes'>
	    <enum name='secureBoot'><value>yes</value><value>no</value></enum>
	    <enum name='enrolledKeys'><value>yes</value><value>no</value></enum>
	  </firmwareFeatures></os></domainCapabilities>`
	// An older libvirt says nothing about firmware features; silence is not a refusal.
	const silent = `<domainCapabilities><os supported='yes'/></domainCapabilities>`

	uefiOn := protocol.VMBoot{Firmware: "uefi"}
	uefiOff := protocol.VMBoot{Firmware: "uefi", SecureBoot: protocol.SecureBootOff}
	bios := protocol.VMBoot{Firmware: "bios"}

	if err := hostSecureBootSupport(parse(t, noEnrolled), uefiOn); err == nil {
		t.Fatal("a host with no enrolled-keys firmware must refuse a Secure Boot VM, not hand libvirt an unsatisfiable request")
	} else if !strings.Contains(err.Error(), "enrolled") {
		t.Fatalf("the refusal must name what is missing, got %q", err)
	}
	if err := hostSecureBootSupport(parse(t, noEnrolled), uefiOff); err != nil {
		t.Fatalf("Secure Boot off needs no enrolled keys: %v", err)
	}
	if err := hostSecureBootSupport(parse(t, withEnrolled), uefiOn); err != nil {
		t.Fatalf("a host that advertises enrolled keys must be accepted: %v", err)
	}
	if err := hostSecureBootSupport(parse(t, silent), uefiOn); err != nil {
		t.Fatalf("an older libvirt that reports no firmwareFeatures is not evidence of a missing build: %v", err)
	}
	if err := hostSecureBootSupport(parse(t, noEnrolled), bios); err != nil {
		t.Fatalf("BIOS has no Secure Boot to check: %v", err)
	}
}
