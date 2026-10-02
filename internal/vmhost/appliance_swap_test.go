// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// swapLab is a Controller deployment in the two-disk profile with a stateful fake libvirt: `virsh`
// keeps the domain state, the persistent definition and the booted system image under dir, and
// `start` of the new image mutates the data disk the way a booted guest would.
type swapLab struct {
	t      *testing.T
	e      *Engine
	state  string // fake libvirt state directory
	id     string
	data   string
	oldSHA string
	newSHA string
	source string
	// ready lists the system images whose Controller answers the readiness probe.
	ready map[string]bool
	req   SwapRequest
}

const labVirsh = `S="$LAB"
echo "$*" >> "$S/log"
case "$3" in
domstate) cat "$S/state";;
domjobinfo) echo "Job type: None";;
shutdown) [ -e "$S/fail_shutdown" ] && { echo "error: guest agent not responding" >&2; exit 1; }
  [ -e "$S/hang_shutdown" ] || echo "shut off" > "$S/state";;
start) [ -e "$S/fail_start" ] && { echo "error: cannot start" >&2; exit 1; }
  XML="$S/inactive.xml"; [ -e "$S/stale_live" ] && XML="$S/live.xml"
  grep -o 'system-[0-9a-f]*\.img' "$XML" | head -1 > "$S/booted"
  if grep -q "$NEWSHA" "$S/booted"; then echo "written-by-new-image" >> "$DATA"; fi
  [ -e "$S/stale_live" ] || cp "$S/inactive.xml" "$S/live.xml"
  echo running > "$S/state";;
list) [ -e "$S/others" ] && cat "$S/others"; exit 0;;
dumpxml) if [ "$4" != "$LABID" ]; then cat "$S/other.xml"
  elif [ "$5" != "--inactive" ] && [ -e "$S/live.xml" ]; then cat "$S/live.xml"
  else cat "$S/inactive.xml"; fi;;
define) [ -e "$S/fail_define" ] && { echo "error: define failed" >&2; exit 1; }
  cp "$4" "$S/inactive.xml";;
qemu-agent-command) echo '{"return":[{"hardware-address":"'"$MAC"'","ip-addresses":[{"ip-address":"192.168.122.10","ip-address-type":"ipv4","prefix":24}]}]}';;
*) exit 99;;
esac`

func newSwapLab(t *testing.T) *swapLab {
	t.Helper()
	e := testEngine(t)
	e.swapPoll = time.Millisecond
	e.shutdownWait = 50 * time.Millisecond
	l := &swapLab{t: t, e: e, id: "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d", state: t.TempDir(), ready: map[string]bool{}}
	dir := filepath.Join(e.config.StorageDir, l.id)
	if err := os.MkdirAll(dir, 0o711); err != nil {
		t.Fatal(err)
	}
	l.oldSHA = l.write(filepath.Join(dir, "system-%s.img"), "old system image")
	l.data = filepath.Join(dir, "controller-data.qcow2")
	if err := os.WriteFile(l.data, []byte("controller state\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(t.TempDir(), "payload")
	l.source = filepath.Join(payload, "controller-system.img")
	if err := os.MkdirAll(payload, 0o755); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("new system image"))
	l.newSHA = hex.EncodeToString(sum[:])
	if err := os.WriteFile(l.source, []byte("new system image"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldPath := filepath.Join(dir, "system-"+l.oldSHA+".img")
	dep := journal{Operation: Operation{ID: "deploy-1", VMID: l.id, Kind: "deploy", Status: "ready", Stage: "ready", ControllerURL: "https://192.168.122.10:8443"},
		Name: "nex-controller", Disk: l.data, Bridge: e.config.Bridge, System: oldPath}
	if err := e.save(dep); err != nil {
		t.Fatal(err)
	}
	l.setDomain(oldPath)
	l.put("state", "running\n")
	l.put("booted", "system-"+l.oldSHA+".img\n")
	l.ready[l.oldSHA] = true
	l.ready[l.newSHA] = true
	fakeCommand(t, "virsh", labVirsh)
	t.Setenv("LAB", l.state)
	t.Setenv("NEWSHA", l.newSHA)
	t.Setenv("DATA", l.data)
	t.Setenv("LABID", l.id)
	t.Setenv("MAC", mac(l.id))
	l.req = SwapRequest{RequestID: "swap-request-1", Sequence: 7, SHA256: l.newSHA, Source: l.source, Deadline: 100 * time.Millisecond, Probe: l.probe}
	return l
}

// write stores content under a path template that takes the content's SHA-256 and returns the digest.
func (l *swapLab) write(template, content string) string {
	sum := sha256.Sum256([]byte(content))
	h := hex.EncodeToString(sum[:])
	if err := os.WriteFile(fmt.Sprintf(template, h), []byte(content), 0o444); err != nil {
		l.t.Fatal(err)
	}
	return h
}

func (l *swapLab) put(name, content string) {
	l.t.Helper()
	if err := os.WriteFile(filepath.Join(l.state, name), []byte(content), 0o600); err != nil {
		l.t.Fatal(err)
	}
}

func (l *swapLab) read(name string) string {
	b, _ := os.ReadFile(filepath.Join(l.state, name))
	return strings.TrimSpace(string(b))
}

func (l *swapLab) setDomain(system string) {
	xml := fmt.Sprintf(`<domain type='kvm'><name>nex-controller</name><uuid>%s</uuid><os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type></os><devices>
<disk type='file' device='disk'><driver name='qemu' type='raw'/><source file='%s'/><target dev='vda' bus='virtio'/><readonly/></disk>
<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='vdb' bus='virtio'/></disk>
<disk type='file' device='cdrom'><driver name='qemu' type='raw'/><target dev='sda' bus='sata'/><readonly/></disk>
<interface type='bridge'><mac address='%s'/><source bridge='virbr0'/><model type='virtio'/></interface></devices></domain>`, l.id, system, l.data, mac(l.id))
	l.put("inactive.xml", xml)
	l.put("live.xml", xml)
}

func (l *swapLab) probe(ctx context.Context, url string) error {
	if url != "https://192.168.122.10:8443" {
		return fmt.Errorf("unexpected URL %s", url)
	}
	booted := strings.TrimSuffix(strings.TrimPrefix(l.read("booted"), "system-"), ".img")
	if l.read("state") != "running" || !l.ready[booted] {
		return errors.New("Controller not ready")
	}
	return nil
}

func (l *swapLab) swap(hook func(stage string) error) (Operation, error) {
	l.e.swapHook = hook
	return l.e.SwapAppliance(context.Background(), l.req)
}

func (l *swapLab) newPath() string {
	return filepath.Join(filepath.Dir(l.data), "system-"+l.newSHA+".img")
}
func (l *swapLab) oldPath() string {
	return filepath.Join(filepath.Dir(l.data), "system-"+l.oldSHA+".img")
}

func (l *swapLab) dataContent() string {
	b, _ := os.ReadFile(l.data)
	return string(b)
}

func (l *swapLab) count(command string) int {
	return strings.Count(l.read("log"), " "+command+" ")
}

func (l *swapLab) deployment() journal {
	j, err := l.e.deployment(l.id)
	if err != nil {
		l.t.Fatal(err)
	}
	return j
}

// crashAt returns a hook that fails once, after the journal recorded stage, like a killed process.
func crashAt(stage string) func(string) error {
	fired := false
	return func(s string) error {
		if s == stage && !fired {
			fired = true
			return errors.New("simulated crash")
		}
		return nil
	}
}

func codeOf(err error) string {
	var se *SwapError
	if errors.As(err, &se) {
		return se.Code
	}
	return ""
}

func TestSwapSuccessKeepsIdentityAndOldImage(t *testing.T) {
	l := newSwapLab(t)
	op, err := l.swap(nil)
	if err != nil || op.Status != "completed" || op.Stage != swapCommitted {
		t.Fatalf("swap: %+v %v", op, err)
	}
	dep := l.deployment()
	if dep.System != l.newPath() || dep.PreviousSystem != l.oldPath() {
		t.Fatalf("deployment not committed: %+v", dep)
	}
	for _, p := range []string{l.newPath(), l.oldPath(), l.data + ".pre-7"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s kept until the next update: %v", p, err)
		}
	}
	if got := l.dataContent(); got != "controller state\nwritten-by-new-image\n" {
		t.Fatalf("data disk was rewritten by the swap: %q", got)
	}
	if cp, _ := os.ReadFile(l.data + ".pre-7"); string(cp) != "controller state\n" {
		t.Fatalf("checkpoint is not the pre-swap data: %q", cp)
	}
	xml := l.read("inactive.xml")
	if !strings.Contains(xml, l.newPath()) || strings.Contains(xml, l.oldPath()) || !strings.Contains(xml, l.id) || !strings.Contains(xml, mac(l.id)) {
		t.Fatalf("definition lost its identity or kept the old image: %s", xml)
	}
	if l.count("destroy") != 0 || l.count("shutdown") != 1 {
		t.Fatalf("shutdown must be one graceful request, never forced:\n%s", l.read("log"))
	}
	if st, _ := os.Stat(l.newPath()); st.Mode().Perm() != 0o444 {
		t.Fatalf("system image must be read-only, mode %v", st.Mode().Perm())
	}
}

func TestSwapNextUpdatePrunesImagesOlderThanPrevious(t *testing.T) {
	l := newSwapLab(t)
	if _, err := l.swap(nil); err != nil {
		t.Fatal(err)
	}
	// A third release replaces the first image; the second stays as the rollback target.
	third := "third system image"
	sum := sha256.Sum256([]byte(third))
	l.req = SwapRequest{RequestID: "swap-request-2", Sequence: 8, SHA256: hex.EncodeToString(sum[:]), Source: l.source, Deadline: l.req.Deadline, Probe: l.probe}
	if err := os.WriteFile(l.source, []byte(third), 0o644); err != nil {
		t.Fatal(err)
	}
	l.ready[l.req.SHA256] = true
	t.Setenv("NEWSHA", l.req.SHA256)
	if op, err := l.swap(nil); err != nil || op.Status != "completed" {
		t.Fatalf("second swap: %+v %v", op, err)
	}
	if _, err := os.Stat(l.oldPath()); !os.IsNotExist(err) {
		t.Fatalf("the image before the previous one was kept: %v", err)
	}
	if _, err := os.Stat(l.newPath()); err != nil {
		t.Fatalf("the previous image was pruned: %v", err)
	}
	if _, err := os.Stat(l.data + ".pre-7"); !os.IsNotExist(err) {
		t.Fatal("an old checkpoint outlived the next update")
	}
}

func TestSwapReadinessFailureRestoresDataAndOldImage(t *testing.T) {
	l := newSwapLab(t)
	l.ready[l.newSHA] = false
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || op.Stage != swapRolledBack {
		t.Fatalf("expected a rollback: %+v %v", op, err)
	}
	if got := l.dataContent(); got != "controller state\n" {
		t.Fatalf("data not restored to the checkpoint: %q", got)
	}
	dep := l.deployment()
	if dep.System != l.oldPath() || dep.PreviousSystem != "" {
		t.Fatalf("deployment must keep the old image: %+v", dep)
	}
	if !strings.Contains(l.read("inactive.xml"), l.oldPath()) || l.read("state") != "running" || !strings.Contains(l.read("booted"), l.oldSHA) {
		t.Fatalf("old image not running again: %s / %s", l.read("state"), l.read("booted"))
	}
	if l.count("destroy") != 0 {
		t.Fatal("rollback must never force a guest off")
	}
	// A replay returns the same result and touches nothing.
	before := l.count("start")
	if op2, err2 := l.swap(nil); codeOf(err2) != SwapRolledBack || op2.Status != "rolled_back" || l.count("start") != before {
		t.Fatalf("replay changed the outcome: %+v %v", op2, err2)
	}
}

func TestSwapDoubleFailureLeavesControllerStoppedNeedsAttention(t *testing.T) {
	l := newSwapLab(t)
	l.ready[l.newSHA], l.ready[l.oldSHA] = false, false
	op, err := l.swap(nil)
	if codeOf(err) != SwapNeedsAttention || op.Status != "needs_attention" {
		t.Fatalf("expected needs_attention: %+v %v", op, err)
	}
	if l.read("state") != "shut off" {
		t.Fatalf("the Controller must stay stopped, state %q", l.read("state"))
	}
	if l.dataContent() != "controller state\n" {
		t.Fatal("data must still be the checkpoint")
	}
	if l.count("destroy") != 0 {
		t.Fatal("never forced")
	}
	// A repeat reports the stored result without starting it again.
	starts := l.count("start")
	if _, err = l.swap(nil); codeOf(err) != SwapNeedsAttention || l.count("start") != starts {
		t.Fatalf("replay re-ran effects: %v", err)
	}
}

func TestSwapRestoreFailureAlsoNeedsAttention(t *testing.T) {
	l := newSwapLab(t)
	l.ready[l.newSHA] = false
	// The checkpoint disappears after the new image booted: the data cannot be restored.
	hook := func(stage string) error {
		if stage == swapRestore {
			return os.Remove(l.data + ".pre-7")
		}
		return nil
	}
	if op, err := l.swap(hook); codeOf(err) != SwapNeedsAttention || op.Status != "needs_attention" {
		t.Fatalf("expected needs_attention: %+v %v", op, err)
	}
}

func TestSwapInterruptedAtEveryStepResumes(t *testing.T) {
	for _, stage := range []string{swapStage, swapShutdown, swapCheckpoint, swapDefine, swapStart, swapProbe, swapCommit} {
		t.Run(stage, func(t *testing.T) {
			l := newSwapLab(t)
			op, err := l.swap(crashAt(stage))
			if err == nil || op.Status != "running" || op.Stage != stage {
				t.Fatalf("interruption must leave the journal at %s: %+v %v", stage, op, err)
			}
			op, err = l.swap(nil)
			if err != nil || op.Status != "completed" {
				t.Fatalf("resume: %+v %v", op, err)
			}
			if l.deployment().System != l.newPath() || l.read("state") != "running" || !strings.Contains(l.read("booted"), l.newSHA) {
				t.Fatalf("resumed swap did not finish: %+v", l.deployment())
			}
			if l.count("shutdown") > 1 || l.count("destroy") != 0 {
				t.Fatalf("a resume must not repeat shutdown or force anything:\n%s", l.read("log"))
			}
			if cp, _ := os.ReadFile(l.data + ".pre-7"); string(cp) != "controller state\n" {
				t.Fatalf("checkpoint taken again after the new image ran: %q", cp)
			}
		})
	}
}

func TestSwapRestoreInterruptedAtEveryStepResumes(t *testing.T) {
	for _, stage := range []string{swapRestore, swapRedefine, swapRestart, swapReprobe} {
		t.Run(stage, func(t *testing.T) {
			l := newSwapLab(t)
			l.ready[l.newSHA] = false
			op, err := l.swap(crashAt(stage))
			if err == nil || op.Status != "running" || op.Stage != stage {
				t.Fatalf("interruption must leave the journal at %s: %+v %v", stage, op, err)
			}
			op, err = l.swap(nil)
			if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
				t.Fatalf("resume: %+v %v", op, err)
			}
			if l.dataContent() != "controller state\n" || l.deployment().System != l.oldPath() || !strings.Contains(l.read("booted"), l.oldSHA) || l.read("state") != "running" {
				t.Fatalf("the old Controller was not restored: data %q", l.dataContent())
			}
		})
	}
}

func TestSwapReplayAndConflicts(t *testing.T) {
	l := newSwapLab(t)
	if _, err := l.swap(nil); err != nil {
		t.Fatal(err)
	}
	log := l.read("log")
	op, err := l.swap(nil)
	if err != nil || op.Status != "completed" || l.read("log") != log {
		t.Fatalf("exact replay must return the original result without effects: %+v %v", op, err)
	}
	changed := l.req
	changed.Sequence = 9
	if _, err = l.e.SwapAppliance(context.Background(), changed); err == nil || !strings.Contains(err.Error(), "different input") {
		t.Fatalf("a changed body must conflict: %v", err)
	}
	// The same release under a new request ID is already running: no second checkpoint, no restart.
	again := l.req
	again.RequestID = "swap-request-2"
	op, err = l.e.SwapAppliance(context.Background(), again)
	if err != nil || op.Status != "completed" || l.read("log") != log {
		t.Fatalf("an already running release must be a no-op: %+v %v", op, err)
	}
}

func TestSwapRefusesStagedImageThatIsNotTheManifestDigest(t *testing.T) {
	l := newSwapLab(t)
	if err := os.WriteFile(l.source, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	op, err := l.swap(nil)
	if codeOf(err) != SwapDigestMismatch || op.Status != "failed" {
		t.Fatalf("a wrong digest must be refused: %+v %v", op, err)
	}
	if l.count("shutdown") != 0 || l.read("state") != "running" {
		t.Fatal("the Controller must not be touched when the image fails verification")
	}
	if _, err = os.Stat(l.newPath()); err == nil {
		t.Fatal("an unverified image was placed beside the VM")
	}
}

func TestSwapShutdownIsNeverForced(t *testing.T) {
	for name, file := range map[string]string{"agent refuses": "fail_shutdown", "guest ignores": "hang_shutdown"} {
		t.Run(name, func(t *testing.T) {
			l := newSwapLab(t)
			l.put(file, "1")
			op, err := l.swap(nil)
			if op.Status != "failed" || err == nil {
				t.Fatalf("expected a failed swap: %+v %v", op, err)
			}
			if l.count("destroy") != 0 || strings.Contains(l.read("log"), "define") {
				t.Fatalf("a failed graceful shutdown must change nothing:\n%s", l.read("log"))
			}
			if l.deployment().System != l.oldPath() {
				t.Fatal("deployment changed")
			}
		})
	}
}

func TestSwapRefusesWithoutProfileOrAppliance(t *testing.T) {
	e := testEngine(t)
	req := SwapRequest{RequestID: "swap-request-1", Sequence: 1, SHA256: strings.Repeat("a", 64), Source: "/nonexistent", Probe: func(context.Context, string) error { return nil }}
	if _, err := e.SwapAppliance(context.Background(), req); codeOf(err) != SwapNoAppliance {
		t.Fatalf("no deployment: %v", err)
	}
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	if err := e.save(journal{Operation: Operation{ID: "deploy-1", VMID: id, Kind: "deploy", Status: "ready"}, Name: "c", Disk: filepath.Join(e.config.StorageDir, id, "controller.qcow2"), Bridge: e.config.Bridge}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.SwapAppliance(context.Background(), req); codeOf(err) != SwapNotProfile {
		t.Fatalf("single-disk deployment: %v", err)
	}
}

func TestProfileDomainHasReadOnlySystemAndPersistentData(t *testing.T) {
	e := testEngine(t)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	dir := filepath.Join(e.config.StorageDir, id)
	j := journal{Operation: Operation{ID: "deploy-1", VMID: id}, Name: "nex-controller", Disk: filepath.Join(dir, "controller-data.qcow2"), System: filepath.Join(dir, "system-"+strings.Repeat("b", 64)+".img"), Bridge: e.config.Bridge}
	b, err := xml.Marshal(makeDomain(j, validSpec(t)))
	if err != nil {
		t.Fatal(err)
	}
	if err = checkDomain(b, j); err != nil {
		t.Fatalf("profile domain fails the ownership check: %v\n%s", err, b)
	}
	text := string(b)
	// The unsigned system image needs a UEFI build without Secure Boot; libvirt must not be left to pick one.
	if !strings.Contains(text, `<feature enabled="no" name="secure-boot">`) || !strings.Contains(text, `<feature enabled="no" name="enrolled-keys">`) {
		t.Fatalf("profile domain does not pin UEFI Secure Boot off: %s", text)
	}
	if !strings.Contains(text, `firmware="efi"`) || !strings.Contains(text, mac(id)) || !strings.Contains(text, id) {
		t.Fatalf("profile domain lacks UEFI, MAC or UUID: %s", text)
	}
	// The same domain is not accepted for a deployment without the system disk, nor with a writable one.
	if checkDomain(b, journal{Operation: j.Operation, Name: j.Name, Disk: filepath.Join(dir, "controller.qcow2"), Bridge: j.Bridge}) == nil {
		t.Fatal("a profile domain passed as a single-disk appliance")
	}
	if checkDomain([]byte(strings.Replace(text, "<readonly></readonly>", "", 1)), j) == nil {
		t.Fatal("a writable system disk was accepted")
	}
}

// A swap must never take the ID of an operation that already exists, least of all the deployment's own:
// its journal is the ownership record and would be overwritten.
func TestSwapRequestIDCannotReuseAnotherOperation(t *testing.T) {
	l := newSwapLab(t)
	l.req.RequestID = "deploy-1"
	if _, err := l.swap(nil); err == nil || !strings.Contains(err.Error(), "different input") {
		t.Fatalf("a swap reusing the deployment's ID must be refused: %v", err)
	}
	if dep := l.deployment(); dep.Kind != "deploy" || dep.System != l.oldPath() {
		t.Fatalf("the deployment journal was overwritten: %+v", dep)
	}
}

// Two different swaps never run at once: an unfinished one must be resumed or finished first.
func TestSwapRefusesASecondUnfinishedSwap(t *testing.T) {
	l := newSwapLab(t)
	if _, err := l.swap(crashAt(swapShutdown)); err == nil {
		t.Fatal("expected the simulated crash")
	}
	other := l.req
	other.RequestID, other.Sequence = "swap-request-2", 8
	if _, err := l.e.SwapAppliance(context.Background(), other); codeOf(err) != SwapBusy {
		t.Fatalf("a second swap while one is unfinished: %v", err)
	}
}

// otherDomain defines a second domain that references path as one of its disks.
func (l *swapLab) otherDomain(path string) {
	l.put("others", "11111111-2222-4333-8444-555555555555\n")
	l.put("other.xml", fmt.Sprintf(`<domain type='kvm'><name>other</name><uuid>11111111-2222-4333-8444-555555555555</uuid><devices>
<disk type='file' device='disk'><driver name='qemu' type='qcow2'/><source file='%s'/><target dev='vda' bus='virtio'/></disk></devices></domain>`, path))
}

func TestSwapRefusesADataDiskAnotherDomainReferences(t *testing.T) {
	l := newSwapLab(t)
	l.otherDomain(l.data)
	if op, err := l.swap(nil); err == nil || op.Status == "completed" {
		t.Fatalf("a shared data disk must be refused: %+v %v", op, err)
	}
	if l.count("shutdown") != 0 || l.read("state") != "running" || l.dataContent() != "controller state\n" {
		t.Fatalf("nothing may change when the disk is shared:\n%s", l.read("log"))
	}
	if _, err := os.Stat(l.data + ".pre-7"); !os.IsNotExist(err) {
		t.Fatal("a checkpoint was taken of a shared disk")
	}
}

func TestSwapRechecksExclusivityBeforeCheckpointAndRestore(t *testing.T) {
	t.Run("checkpoint", func(t *testing.T) {
		l := newSwapLab(t)
		// Another domain is defined on the disk while the Controller shuts down.
		hook := func(stage string) error {
			if stage == swapCheckpoint {
				l.otherDomain(l.data)
			}
			return nil
		}
		op, err := l.swap(hook)
		if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
			t.Fatalf("expected a rollback: %+v %v", op, err)
		}
		if _, err := os.Stat(l.data + ".pre-7"); !os.IsNotExist(err) || strings.Contains(l.dataContent(), "written-by-new-image") {
			t.Fatal("the new image must not start on a disk another domain references")
		}
	})
	t.Run("restore", func(t *testing.T) {
		l := newSwapLab(t)
		l.ready[l.newSHA] = false
		// Another domain claims the disk while the new image runs: replacing the file would split them.
		hook := func(stage string) error {
			if stage == swapRestore {
				l.otherDomain(l.data)
			}
			return nil
		}
		if op, err := l.swap(hook); codeOf(err) != SwapNeedsAttention || op.Status != "needs_attention" {
			t.Fatalf("expected needs_attention: %+v %v", op, err)
		}
		if !strings.Contains(l.dataContent(), "written-by-new-image") {
			t.Fatal("the disk was replaced under another domain")
		}
	})
}

func TestSwapCheckpointIsVerifiedAgainstTheDisk(t *testing.T) {
	l := newSwapLab(t)
	// A copy tool that silently writes the wrong bytes for the checkpoint.
	real, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "cp", `case "$*" in *.pre-7.part) echo corrupt > "$5";; *) exec `+real+` "$@";; esac`)
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
		t.Fatalf("a corrupt checkpoint must stop the swap before the new image starts: %+v %v", op, err)
	}
	if strings.Contains(l.dataContent(), "written-by-new-image") {
		t.Fatal("the new image started without a verified checkpoint")
	}
	for _, p := range []string{l.data + ".pre-7", l.data + ".pre-7.part"} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("leftover %s", p)
		}
	}
}

func TestSwapReadinessNeedsTheExpectedImageToRun(t *testing.T) {
	l := newSwapLab(t)
	// The running domain never picked up the new definition and the old Controller keeps answering.
	l.put("stale_live", "")
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
		t.Fatalf("an answer from the old image must not commit the swap: %+v %v", op, err)
	}
	if dep := l.deployment(); dep.System != l.oldPath() {
		t.Fatalf("deployment must keep the old image: %+v", dep)
	}
}
