// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

// Phase 2 gate F1: libvirt refuses to define an existing UUID under a new name. A stopped edit
// that renames the VM and toggles Secure Boot must rename before the Secure Boot define.
func TestStoppedRenameWithSecureBootToggleRenamesBeforeDefine(t *testing.T) {
	prev := managedTestVM()
	prev.Boot.SecureBoot = protocol.SecureBootOn
	next := prev
	next.Revision, next.Name, next.Boot.SecureBoot = 1, "renamed-guest", protocol.SecureBootOff
	e, j, state, calls := secureBootRenameFixture(t, prev, next, "secure=1\nsetup=0\npk=custom\nBoot0001=guest\nBootOrder=0001\n")
	target := j.Domain
	if err := e.applyStopped(context.Background(), &j, prev); err != nil {
		log, _ := os.ReadFile(calls)
		t.Fatalf("combined rename and Secure Boot edit: %v calls=%q", err, log)
	}
	got, _ := os.ReadFile(state)
	if validateManagedDefinition(got, target) != nil {
		t.Fatalf("definition %q stage=%q", got, j.Stage)
	}
	log, _ := os.ReadFile(calls)
	if strings.Count(string(log), "domrename") != 1 || strings.Index(string(log), "domrename") > strings.Index(string(log), "define") {
		t.Fatalf("rename must run once, before define: %q", log)
	}
	if vars, _ := os.ReadFile(firmwareVariablesPath(prev.ID)); !strings.Contains(string(vars), "secure=0") {
		t.Fatalf("Secure Boot variable not changed: %q", vars)
	}
}

// Review R-2: a Secure Boot refusal (no enrolled keys) in a combined rename edit must be
// decided before domrename, so the refused edit leaves libvirt's name untouched.
func TestStoppedRenameWithRefusedSecureBootDoesNotRename(t *testing.T) {
	prev := managedTestVM()
	prev.Boot.SecureBoot = protocol.SecureBootOff
	next := prev
	next.Revision, next.Name, next.Boot.SecureBoot = 1, "renamed-guest", protocol.SecureBootOn
	e, j, state, calls := secureBootRenameFixture(t, prev, next, "secure=0\nsetup=1\npk=\nBoot0001=guest\nBootOrder=0001\n")
	err := e.applyStopped(context.Background(), &j, prev)
	if err == nil || !strings.Contains(err.Error(), "no enrolled Secure Boot keys") {
		t.Fatalf("enabling Secure Boot without keys: %v", err)
	}
	if log, _ := os.ReadFile(calls); strings.Contains(string(log), "domrename") {
		t.Fatalf("refused edit renamed the domain: %q", log)
	}
	if got, _ := os.ReadFile(state); !strings.Contains(string(got), "<name>"+prev.Name+"</name>") {
		t.Fatalf("libvirt name changed: %q", got)
	}
}

func secureBootRenameFixture(t *testing.T, prev, next protocol.VMCreateSpec, vars string) (*ManagedEngine, managedJournal, string, string) {
	t.Helper()
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	t.Cleanup(func() { firmwareVariablesDir = oldVariablesDir })
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
	nets := []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}
	previous, err := testDomainXML(prev, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	target, err := testDomainXML(next, nets, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(firmwareVariablesPath(prev.ID), []byte(vars), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state, calls := filepath.Join(dir, "domain.xml"), filepath.Join(dir, "calls")
	if err = os.WriteFile(state, previous, 0600); err != nil {
		t.Fatal(err)
	}
	// Like libvirt: define of a known UUID under a different name is refused.
	fakeCommand(t, "virsh", `echo "$3" >> "$VMHOST_CALLS"
name() { sed -n "s:.*<name>\([^<]*\)</name>.*:\1:p" "$1" | head -n1; }
case "$3" in
dumpxml) cat "$VMHOST_STATE";;
domrename) sed -i "s:<name>[^<]*</name>:<name>$6</name>:" "$VMHOST_STATE";;
define) [ "$(name "$4")" = "$(name "$VMHOST_STATE")" ] || { echo "domain already defined with uuid" >&2; exit 1; }; cp "$4" "$VMHOST_STATE";;
*) exit 2;; esac`)
	t.Setenv("VMHOST_STATE", state)
	t.Setenv("VMHOST_CALLS", calls)
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "rename-secure-boot", Kind: "vm-update", VM: &next}, Domain: target, SecureBootPreviousDomain: previous, Stage: "allocating"}
	return e, j, state, calls
}

// Phase 2 gate VMEDIT-1: after UEFI-to-BIOS undefine lands, the journal records "defining".
// An interrupted or refused define there leaves no domain; recovery must prove it absent and
// define the journaled BIOS XML rather than strand the VM.
func TestReconcileFirmwareRemovalDefinesAfterInterruptedDefine(t *testing.T) {
	oldVariablesDir := firmwareVariablesDir
	firmwareVariablesDir = t.TempDir()
	defer func() { firmwareVariablesDir = oldVariablesDir }()
	next := managedTestVM()
	next.Revision, next.Boot.Firmware, next.Boot.SecureBoot = 1, "bios", ""
	bios, err := testDomainXML(next, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	state, calls := filepath.Join(dir, "domain.xml"), filepath.Join(dir, "calls")
	fakeCommand(t, "virsh", `echo "$*" >> "$VMHOST_CALLS"
case "$3" in
dumpxml) [ -f "$VMHOST_STATE" ] && cat "$VMHOST_STATE" || exit 1;;
list) ;;
undefine) rm -f "$VMHOST_STATE";;
define) [ -f "$VMHOST_REFUSE" ] && exit 1; cp "$4" "$VMHOST_STATE";;
*) exit 2;; esac`)
	refuse := filepath.Join(dir, "refuse")
	t.Setenv("VMHOST_STATE", state)
	t.Setenv("VMHOST_CALLS", calls)
	t.Setenv("VMHOST_REFUSE", refuse)
	e := NewManaged(t.TempDir())
	if err = os.MkdirAll(e.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	e.observe = func(context.Context, managedJournal, string) error {
		b, err := os.ReadFile(state)
		if err != nil {
			return err
		}
		return validateManagedDefinition(b, bios)
	}
	j := managedJournal{Command: protocol.Command{OperationID: "to-bios", Kind: "vm-update", VM: &next}, Result: protocol.Result{OperationID: "to-bios", Status: "pending"}, Domain: bios, Stage: "defining", NVRAMPath: firmwareVariablesPath(next.ID)}
	// libvirt refused the define: recovery stays needs_attention but retries next pass.
	if err = os.WriteFile(refuse, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.reconcile(context.Background(), j); got.Status == "succeeded" {
		t.Fatalf("refused define reported success: %+v", got)
	}
	os.Remove(refuse)
	got, _ := e.reconcile(context.Background(), j)
	if got.Status != "succeeded" {
		log, _ := os.ReadFile(calls)
		t.Fatalf("absent-domain recovery did not define BIOS XML: %+v calls=%q", got, log)
	}
	if b, err := os.ReadFile(state); err != nil || validateManagedDefinition(b, bios) != nil {
		t.Fatalf("BIOS definition %q err=%v", b, err)
	}
	if log, _ := os.ReadFile(calls); strings.Contains(string(log), "undefine") {
		t.Fatalf("recovery repeated undefine: %q", log)
	}
	// A UEFI domain appearing at "defining" was not left by this edit; never undefine it.
	uefi := managedTestVM()
	uefiXML, err := testDomainXML(uefi, []protocol.NetworkRegistration{{Kind: "bridge", Source: "br0"}}, []string{"/disk-0.qcow2"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(state, uefiXML, 0600); err != nil {
		t.Fatal(err)
	}
	os.Remove(calls)
	if got, _ := e.reconcile(context.Background(), j); got.Status == "succeeded" {
		t.Fatalf("foreign UEFI domain accepted: %+v", got)
	}
	if log, _ := os.ReadFile(calls); strings.Contains(string(log), "undefine") {
		t.Fatalf("recovery undefined a foreign UEFI domain: %q", log)
	}
}
