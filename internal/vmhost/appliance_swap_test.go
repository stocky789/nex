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
	"syscall"
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
if [ -e "$S/stall_$3" ]; then touch "$S/stalled"; exec sleep 30; fi
case "$3" in
domstate) cat "$S/state"; if [ -e "$S/settle" ]; then rm "$S/settle"; echo "shut off" > "$S/state"; fi;;
domjobinfo) echo "Job type: None";;
shutdown) [ -e "$S/fail_shutdown" ] && { echo "error: guest agent not responding" >&2; exit 1; }
  [ -e "$S/hang_shutdown" ] || echo "shut off" > "$S/state";;
destroy) echo "shut off" > "$S/state";;
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
	// The temporary directories are not on a known local filesystem: the host reports ext4 with room to spare.
	e.statfs = func(_ string, st *syscall.Statfs_t) error {
		st.Type, st.Bsize, st.Bavail = 0xef53, 4096, 1<<30
		return nil
	}
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
	// The data disk is a plain qcow2: no backing file, no external data file.
	fakeCommand(t, "qemu-img", `case "$*" in *--backing-chain*) echo '[{"filename":"'"$DATA"'","format":"qcow2"}]';; *) echo '{"format":"qcow2"}';; esac`)
	t.Setenv("LAB", l.state)
	t.Setenv("NEWSHA", l.newSHA)
	t.Setenv("DATA", l.data)
	t.Setenv("LABID", l.id)
	t.Setenv("MAC", mac(l.id))
	l.req = SwapRequest{RequestID: "swap-request-1", Sequence: 7, SHA256: l.newSHA, Source: l.source, Deadline: readyDeadline, Probe: l.probe}
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

// readyDeadline bounds the readiness probe when the new image comes up; a run that fails only pays
// notReadyDeadline. Both are far above one fake virsh call so a loaded runner cannot expire the first.
const (
	readyDeadline    = 5 * time.Second
	notReadyDeadline = 500 * time.Millisecond
)

// setNewReady chooses whether the new image boots, and the probe budget that fits the outcome.
func (l *swapLab) setNewReady(ok bool) {
	l.ready[l.newSHA] = ok
	l.req.Deadline = readyDeadline
	if !ok {
		l.req.Deadline = notReadyDeadline
	}
}

func (l *swapLab) swap(hook func(stage string) error) (Operation, error) {
	return l.swapWith(context.Background(), hook)
}

func (l *swapLab) swapWith(ctx context.Context, hook func(stage string) error) (Operation, error) {
	l.e.swapHook = hook
	return l.e.SwapAppliance(ctx, l.req)
}

// cancelOnStall returns a context that is cancelled the moment a command stalls, the way the service's
// context ends when it stops in the middle of a step. A fake command stalls when it sees its stall flag.
func (l *swapLab) cancelOnStall() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	l.t.Cleanup(func() { close(done); cancel() })
	go func() {
		for {
			select {
			case <-done:
				return
			case <-time.After(time.Millisecond):
			}
			if _, err := os.Stat(filepath.Join(l.state, "stalled")); err == nil {
				cancel()
				return
			}
		}
	}()
	return ctx
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
	l.setNewReady(false)
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
	l.setNewReady(false)
	l.ready[l.oldSHA] = false
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
	l.setNewReady(false)
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
			l.setNewReady(false)
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

func TestSwapRollbackRestoresDataAfterFailedImageAutostartsAgain(t *testing.T) {
	for _, stage := range []string{swapRestore, swapRedefine} {
		for _, mode := range []string{"running", "already stopped", "no guest agent"} {
			t.Run(stage+"/"+mode, func(t *testing.T) {
				l := newSwapLab(t)
				l.setNewReady(false)
				if _, err := l.swap(crashAt(stage)); err == nil {
					t.Fatal("expected the simulated crash")
				}
				l.setDomain(l.newPath())
				l.put("booted", "system-"+l.newSHA+".img\n")
				l.put("state", "running\n")
				if mode == "already stopped" {
					l.put("state", "shut off\n")
				} else if mode == "no guest agent" {
					l.put("fail_shutdown", "")
				}
				if err := os.WriteFile(l.data, []byte("controller state\nwritten-after-power-cut\n"), 0o600); err != nil {
					t.Fatal(err)
				}
				op, err := l.swap(nil)
				if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || l.dataContent() != "controller state\n" {
					t.Fatalf("failed-image writes survived rollback: %+v %v data %q", op, err, l.dataContent())
				}
				if l.read("state") != "running" || !strings.Contains(l.read("booted"), l.oldSHA) || l.deployment().System != l.oldPath() {
					t.Fatalf("the old Controller was not restarted: %+v", l.deployment())
				}
				if mode == "no guest agent" && l.count("destroy") != 1 || mode != "no guest agent" && l.count("destroy") != 0 {
					t.Fatalf("only the failed image without an agent may be forced:\n%s", l.read("log"))
				}
				if err := checkDomain([]byte(l.read("inactive.xml")), l.deployment()); err != nil {
					t.Fatalf("rollback changed appliance identity or data ownership: %v", err)
				}
			})
		}
	}
}

func TestSwapRollbackPreservesOldImageWritesAfterRedefinition(t *testing.T) {
	const late = "controller state\nwritten-by-restored-old-image\n"
	for _, stage := range []string{swapRedefine, swapRestart, swapReprobe} {
		t.Run(stage, func(t *testing.T) {
			l := newSwapLab(t)
			l.setNewReady(false)
			if _, err := l.swap(crashAt(stage)); err == nil {
				t.Fatal("expected the simulated crash")
			}
			// At swap_redefine, libvirt completed the define before the journal could advance.
			l.setDomain(l.oldPath())
			l.put("state", "running\n")
			l.put("booted", "system-"+l.oldSHA+".img\n")
			if err := os.WriteFile(l.data, []byte(late), 0o600); err != nil {
				t.Fatal(err)
			}
			op, err := l.swap(nil)
			if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || l.dataContent() != late {
				t.Fatalf("resuming rollback lost the restored image's writes: %+v %v data %q", op, err, l.dataContent())
			}
			if l.read("state") != "running" || l.count("destroy") != 0 {
				t.Fatalf("the restored old image was stopped or forced:\n%s", l.read("log"))
			}
			if cp, err := os.ReadFile(l.data + ".pre-7"); err != nil || string(cp) != "controller state\n" {
				t.Fatalf("rollback changed its checkpoint: %q %v", cp, err)
			}
		})
	}
}

func TestSwapRollbackRechecksOwnedImageBeforeRepeatingRestore(t *testing.T) {
	for _, changed := range []string{"system", "data", "MAC", "inactive"} {
		t.Run(changed, func(t *testing.T) {
			l := newSwapLab(t)
			l.setNewReady(false)
			if _, err := l.swap(crashAt(swapRedefine)); err == nil {
				t.Fatal("expected the simulated crash")
			}
			l.setDomain(l.newPath())
			l.put("state", "running\n")
			switch changed {
			case "system":
				l.put("live.xml", strings.ReplaceAll(l.read("live.xml"), l.newPath(), l.newPath()+".foreign"))
			case "data":
				l.put("live.xml", strings.ReplaceAll(l.read("live.xml"), l.data, l.data+".foreign"))
			case "MAC":
				l.put("live.xml", strings.ReplaceAll(l.read("live.xml"), mac(l.id), "52:54:00:00:00:01"))
			case "inactive":
				l.put("inactive.xml", strings.ReplaceAll(l.read("inactive.xml"), l.newPath(), l.newPath()+".foreign"))
			}
			const changedData = "do-not-restore-unverified-guest\n"
			if err := os.WriteFile(l.data, []byte(changedData), 0o600); err != nil {
				t.Fatal(err)
			}
			shutdowns := l.count("shutdown")
			op, err := l.swap(nil)
			if codeOf(err) != SwapNeedsAttention || op.Status != "needs_attention" || l.count("destroy") != 0 || l.dataContent() != changedData {
				t.Fatalf("an unverified guest was forced or its disk replaced: %+v %v data %q", op, err, l.dataContent())
			}
			if changed != "inactive" && l.count("shutdown") != shutdowns {
				t.Fatalf("an unverified guest was shut down:\n%s", l.read("log"))
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
		l.setNewReady(false)
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
	l.req.Deadline = notReadyDeadline
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
		t.Fatalf("an answer from the old image must not commit the swap: %+v %v", op, err)
	}
	if dep := l.deployment(); dep.System != l.oldPath() {
		t.Fatalf("deployment must keep the old image: %+v", dep)
	}
}

// Power is lost after the domain was redefined and before the journal recorded a start. The Controller is
// autostarted from the new definition, so the new image runs on the data disk while the journal still
// says swap_define: the rollback must restore the checkpoint, not hand the old image a migrated disk.
func TestSwapRestoresDataWhenLibvirtAutostartedTheNewImage(t *testing.T) {
	l := newSwapLab(t)
	if _, err := l.swap(crashAt(swapDefine)); err == nil {
		t.Fatal("expected the simulated crash")
	}
	l.setDomain(l.newPath())
	l.put("state", "running\n")
	l.put("booted", "system-"+l.newSHA+".img\n")
	if err := os.WriteFile(l.data, []byte("controller state\nwritten-by-new-image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
		t.Fatalf("expected a rollback: %+v %v", op, err)
	}
	if got := l.dataContent(); got != "controller state\n" {
		t.Fatalf("the old image was given the data the new image wrote: %q", got)
	}
	if l.deployment().System != l.oldPath() || !strings.Contains(l.read("booted"), l.oldSHA) {
		t.Fatalf("the old image is not running again: %s", l.read("booted"))
	}
}

func TestSwapKeepsCheckpointWhenAlreadyDefinedNewImageStoppedAfterWriting(t *testing.T) {
	l := newSwapLab(t)
	l.setNewReady(false)
	if _, err := l.swap(crashAt(swapDefine)); err == nil {
		t.Fatal("expected the simulated crash")
	}
	l.setDomain(l.newPath())
	l.put("state", "shut off\n")
	if err := os.WriteFile(l.data, []byte("controller state\nwritten-by-new-image\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	op, err := l.swap(nil)
	if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || l.dataContent() != "controller state\n" {
		t.Fatalf("the new image overwrote the old checkpoint: %+v %v data %q", op, err, l.dataContent())
	}
}

// A service that stops while a step runs is an interruption, not a failure of the step: the journal
// keeps the step, no rollback starts, no terminal status is written, and repeating the request finishes.
func TestSwapCancelledContextInterruptsEveryStep(t *testing.T) {
	real, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, stage, stall, copyEnd string
		restore                     bool
	}{
		{"checkpoint copy", swapCheckpoint, "cp", "controller-data.qcow2.pre-7.part", false},
		{"define", swapDefine, "define", "", false},
		{"start", swapStart, "start", "", false},
		{"restore copy", swapRestore, "cp", "controller-data.qcow2.part", true},
		{"redefine", swapRedefine, "define", "", true},
		{"restart", swapRestart, "start", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newSwapLab(t)
			l.setNewReady(!tc.restore)
			t.Setenv("STALLCP", tc.copyEnd)
			fakeCommand(t, "cp", `case "$*" in *"$STALLCP") if [ -e "$LAB/stall_cp" ]; then touch "$LAB/stalled"; exec sleep 30; fi;; esac
exec `+real+` "$@"`)
			stall := func(stage string) error {
				if stage == tc.stage {
					l.put("stall_"+tc.stall, "")
				}
				return nil
			}
			op, err := l.swapWith(l.cancelOnStall(), stall)
			if !errors.Is(err, context.Canceled) || op.Status != "running" || op.Stage != tc.stage {
				t.Fatalf("a cancelled context must leave the journal at %s: %+v %v", tc.stage, op, err)
			}
			if j, err := l.e.load(l.req.RequestID); err != nil || j.Status != "running" || j.Stage != tc.stage {
				t.Fatalf("journal after the interruption: %+v %v", j, err)
			}
			for _, name := range []string{"stall_" + tc.stall, "stalled"} {
				if err := os.Remove(filepath.Join(l.state, name)); err != nil {
					t.Fatal(err)
				}
			}
			op, err = l.swap(nil)
			if tc.restore {
				if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || l.dataContent() != "controller state\n" || l.deployment().System != l.oldPath() {
					t.Fatalf("the resumed rollback did not finish: %+v %v data %q", op, err, l.dataContent())
				}
			} else if err != nil || op.Status != "completed" || l.deployment().System != l.newPath() {
				t.Fatalf("the resumed swap did not finish: %+v %v", op, err)
			}
		})
	}
}

// A graceful shutdown already under way (the request was repeated while the guest was still stopping) is
// waited for. Failing here would end the swap with the Controller about to power off and nothing to start it.
func TestSwapWaitsForAShutdownAlreadyUnderWay(t *testing.T) {
	l := newSwapLab(t)
	if _, err := l.swap(crashAt(swapShutdown)); err == nil {
		t.Fatal("expected the simulated crash")
	}
	l.put("state", "in shutdown\n")
	l.put("settle", "")
	op, err := l.swap(nil)
	if err != nil || op.Status != "completed" {
		t.Fatalf("resume during a shutdown: %+v %v", op, err)
	}
	if l.count("shutdown") != 0 || l.count("destroy") != 0 {
		t.Fatalf("a shutdown already under way must be waited for, not requested or forced again:\n%s", l.read("log"))
	}
}

// A new image that never booted far enough to run the guest agent cannot be shut down gracefully. It is
// the only writer of a data disk that the checkpoint replaces, so the rollback forces it off, and only it.
func TestSwapRollbackForcesOnlyANewImageThatNeverBecameReady(t *testing.T) {
	for name, file := range map[string]string{"no guest agent": "fail_shutdown", "guest ignores the shutdown": "hang_shutdown"} {
		t.Run(name, func(t *testing.T) {
			l := newSwapLab(t)
			l.setNewReady(false)
			op, err := l.swap(func(stage string) error {
				if stage == swapProbe {
					l.put(file, "")
				}
				return nil
			})
			if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" {
				t.Fatalf("expected a rollback: %+v %v", op, err)
			}
			if l.count("destroy") != 1 || l.dataContent() != "controller state\n" || l.deployment().System != l.oldPath() || l.read("state") != "running" || !strings.Contains(l.read("booted"), l.oldSHA) {
				t.Fatalf("the old image was not restored after the forced stop (data %q):\n%s", l.dataContent(), l.read("log"))
			}
		})
	}
	t.Run("never any other guest", func(t *testing.T) {
		l := newSwapLab(t)
		l.setNewReady(false)
		if _, err := l.swap(crashAt(swapRestore)); err == nil {
			t.Fatal("expected the simulated crash")
		}
		// The definition names the new image but the running domain is not it, and its agent does not answer.
		inactive := l.read("inactive.xml")
		l.setDomain(l.oldPath())
		l.put("inactive.xml", inactive)
		l.put("state", "running\n")
		l.put("fail_shutdown", "")
		op, err := l.swap(nil)
		if codeOf(err) != SwapNeedsAttention || op.Status != "needs_attention" || l.count("destroy") != 0 {
			t.Fatalf("a guest that is not the failed new image must never be forced: %+v %v\n%s", op, err, l.read("log"))
		}
	})
}

// A replay of a refused swap answers with the code the first call returned.
func TestSwapReplayAnswersWithTheCodeOfTheFirstRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		break_     func(*testing.T, *swapLab)
	}{
		{"digest", SwapDigestMismatch, func(t *testing.T, l *swapLab) {
			if err := os.WriteFile(l.source, []byte("tampered"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"staging", SwapStageFailed, func(t *testing.T, l *swapLab) { fakeCommand(t, "cp", "exit 1") }},
		{"shutdown", SwapShutdownFailed, func(t *testing.T, l *swapLab) { l.put("fail_shutdown", "") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := newSwapLab(t)
			tc.break_(t, l)
			if _, err := l.swap(nil); codeOf(err) != tc.code {
				t.Fatalf("first answer %v, want %s", err, tc.code)
			}
			if op, err := l.swap(nil); codeOf(err) != tc.code || op.Status != "failed" {
				t.Fatalf("replay answered %+v %v, want %s", op, err, tc.code)
			}
		})
	}
}

// The guards every other Controller action runs also stand before a swap, so a Controller whose disk or
// storage is not what it deployed is refused before it is stopped.
func TestSwapAdmissionRunsTheOwnershipGuards(t *testing.T) {
	for name, setup := range map[string]func(*testing.T, *swapLab){
		"data disk replaced by a symlink": func(t *testing.T, l *swapLab) {
			if err := os.Rename(l.data, l.data+".real"); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(l.data+".real", l.data); err != nil {
				t.Fatal(err)
			}
		},
		"storage not local": func(t *testing.T, l *swapLab) {
			l.e.statfs = func(_ string, st *syscall.Statfs_t) error { st.Type = 0x6969; return nil } // NFS
		},
		"data disk with a backing file": func(t *testing.T, l *swapLab) {
			fakeCommand(t, "qemu-img", `echo '{"format":"qcow2","backing-filename":"base.qcow2"}'`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			l := newSwapLab(t)
			setup(t, l)
			if op, err := l.swap(nil); err == nil || op.Status == "completed" {
				t.Fatalf("the swap was admitted: %+v %v", op, err)
			}
			if l.count("shutdown") != 0 || l.read("state") != "running" {
				t.Fatalf("the Controller was stopped for a swap that must be refused:\n%s", l.read("log"))
			}
			if _, err := l.e.load(l.req.RequestID); err == nil {
				t.Fatal("a refused swap left a journal")
			}
		})
	}
}

// Without extent sharing (ext4) the checkpoint and a restore each copy the data disk. That has to fit
// before the Controller is stopped, not be found out by a failed copy after it was.
func TestSwapRefusesBeforeShutdownWhenTheCheckpointCannotFit(t *testing.T) {
	real, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	room := func(l *swapLab, free uint64) {
		l.e.statfs = func(_ string, st *syscall.Statfs_t) error {
			st.Type, st.Bsize, st.Bavail = 0xef53, 4096, free
			return nil
		}
	}
	t.Run("no reflink and no room", func(t *testing.T) {
		l := newSwapLab(t)
		if err := os.WriteFile(l.data, []byte(strings.Repeat("x", 1<<20)), 0o600); err != nil {
			t.Fatal(err)
		}
		fakeCommand(t, "cp", `case "$*" in *--reflink=always*) echo "cp: failed to clone: Operation not supported" >&2; exit 1;; esac
exec `+real+` "$@"`)
		room(l, 1)
		if op, err := l.swap(nil); codeOf(err) != SwapNoRoom || op.Status == "completed" {
			t.Fatalf("expected a refusal for lack of room: %+v %v", op, err)
		}
		if l.count("shutdown") != 0 || l.read("state") != "running" {
			t.Fatalf("the Controller was stopped for a checkpoint that cannot fit:\n%s", l.read("log"))
		}
		room(l, 1<<30)
		if op, err := l.swap(nil); err != nil || op.Status != "completed" {
			t.Fatalf("the same swap with room must go ahead: %+v %v", op, err)
		}
	})
	t.Run("reflink needs no room", func(t *testing.T) {
		l := newSwapLab(t)
		fakeCommand(t, "cp", `case "$*" in *--reflink=always*) exit 0;; esac
exec `+real+` "$@"`)
		room(l, 0)
		if op, err := l.swap(nil); err != nil || op.Status != "completed" {
			t.Fatalf("a filesystem that shares extents needs no free space for the checkpoint: %+v %v", op, err)
		}
	})
}

// Power is lost after the swap shut the Controller down and before the definition changed; libvirt
// autostarts the old image on reboot and it writes to the data disk. The swap stops it gracefully, takes
// the checkpoint again from the quiescent disk, and goes on: a checkpoint older than that guest's writes
// would lose them on a rollback.
func TestSwapContinuesWhenLibvirtAutostartedTheOldImage(t *testing.T) {
	const late = "controller state\nwritten-by-old-image\n"
	for _, stage := range []string{swapCheckpoint, swapDefine} {
		for _, ready := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s ready=%v", stage, ready), func(t *testing.T) {
				l := newSwapLab(t)
				l.setNewReady(ready)
				if _, err := l.swap(crashAt(stage)); err == nil {
					t.Fatal("expected the simulated crash")
				}
				// After the reboot the old image runs again from the unchanged definition and writes.
				l.put("state", "running\n")
				if err := os.WriteFile(l.data, []byte(late), 0o600); err != nil {
					t.Fatal(err)
				}
				op, err := l.swap(nil)
				if cp, _ := os.ReadFile(l.data + ".pre-7"); string(cp) != late {
					t.Fatalf("the checkpoint lost what the old image wrote: %q (%+v %v)", cp, op, err)
				}
				// The first shutdown, the restarted old image's, and on a rollback the failed new image's.
				shutdowns := 2
				if !ready {
					shutdowns = 3
				}
				if l.count("destroy") != 0 || l.count("shutdown") != shutdowns {
					t.Fatalf("the restarted old image must be stopped once, gracefully:\n%s", l.read("log"))
				}
				if ready {
					if err != nil || op.Status != "completed" || l.deployment().System != l.newPath() || l.dataContent() != late+"written-by-new-image\n" {
						t.Fatalf("the swap did not go on: %+v %v data %q", op, err, l.dataContent())
					}
				} else if codeOf(err) != SwapRolledBack || l.dataContent() != late || l.deployment().System != l.oldPath() {
					t.Fatalf("the rollback lost the old image's writes: %+v %v data %q", op, err, l.dataContent())
				}
			})
		}
	}
}

func TestSwapRefreshesCheckpointWhenRestartedOldImageAlreadyStopped(t *testing.T) {
	const late = "controller state\nwritten-by-old-image-before-stopping\n"
	for _, ready := range []bool{true, false} {
		t.Run(fmt.Sprintf("ready=%v", ready), func(t *testing.T) {
			l := newSwapLab(t)
			l.setNewReady(ready)
			if _, err := l.swap(crashAt(swapDefine)); err == nil {
				t.Fatal("expected the simulated crash")
			}
			// The old image autostarted, wrote, then shut down before the service resumed.
			l.put("state", "shut off\n")
			if err := os.WriteFile(l.data, []byte(late), 0o600); err != nil {
				t.Fatal(err)
			}
			op, err := l.swap(nil)
			if cp, err := os.ReadFile(l.data + ".pre-7"); err != nil || string(cp) != late {
				t.Fatalf("the checkpoint lost the old image's writes: %q %v", cp, err)
			}
			if ready {
				if err != nil || op.Status != "completed" || l.dataContent() != late+"written-by-new-image\n" {
					t.Fatalf("the resumed swap did not finish: %+v %v data %q", op, err, l.dataContent())
				}
			} else if codeOf(err) != SwapRolledBack || op.Status != "rolled_back" || l.dataContent() != late {
				t.Fatalf("rollback lost committed old-image writes: %+v %v data %q", op, err, l.dataContent())
			}
			if err := checkDomain([]byte(l.read("inactive.xml")), l.deployment()); err != nil {
				t.Fatalf("swap changed appliance identity or data ownership: %v", err)
			}
		})
	}
}

// A restarted old image that will not stop is not forced: the swap ends having changed nothing, and the
// Controller keeps serving its image. A replay answers the same.
func TestSwapEndsWithoutChangesWhenARestartedOldImageWillNotStop(t *testing.T) {
	for _, stage := range []string{swapCheckpoint, swapDefine} {
		t.Run(stage, func(t *testing.T) {
			l := newSwapLab(t)
			if _, err := l.swap(crashAt(stage)); err == nil {
				t.Fatal("expected the simulated crash")
			}
			l.put("state", "running\n")
			l.put("fail_shutdown", "")
			for i := 0; i < 2; i++ {
				op, err := l.swap(nil)
				if codeOf(err) != SwapShutdownFailed || op.Status != "failed" {
					t.Fatalf("attempt %d: %+v %v", i, op, err)
				}
			}
			if l.count("destroy") != 0 || l.read("state") != "running" || l.deployment().System != l.oldPath() || !strings.Contains(l.read("inactive.xml"), l.oldPath()) {
				t.Fatalf("the swap must change nothing:\n%s", l.read("log"))
			}
			if _, err := os.Stat(l.data + ".pre-7"); !os.IsNotExist(err) {
				t.Fatalf("a stale checkpoint was left behind: %v", err)
			}
		})
	}
}

// The staged image is on disk before the journal moves past staging: its data is synced before the rename
// can expose it under its final name, and the directory entry after the rename.
func TestSwapSyncsTheStagedImageBeforeTheJournalAdvances(t *testing.T) {
	for _, placed := range []bool{false, true} {
		t.Run(fmt.Sprintf("placed by an earlier run=%v", placed), func(t *testing.T) {
			l := newSwapLab(t)
			if placed {
				if err := os.WriteFile(l.newPath(), []byte("new system image"), 0o444); err != nil {
					t.Fatal(err)
				}
			}
			part := l.newPath() + ".part"
			exists := func(p string) bool { _, err := os.Stat(p); return err == nil }
			var events []string
			l.e.syncPath = func(p string) error {
				switch p {
				case part:
					if placed || exists(l.newPath()) || !exists(part) {
						t.Errorf("the temporary file must be synced before the rename, while the final name is absent")
					}
					if sum, _, err := fileSHA256(part); err != nil || sum != l.newSHA {
						t.Errorf("the temporary file was synced before it was complete: %v", err)
					}
					events = append(events, "file")
				case l.newPath():
					if !placed {
						t.Errorf("a copied image is synced under its temporary name")
					}
					events = append(events, "file")
				case filepath.Dir(l.newPath()):
					if !exists(l.newPath()) || exists(part) {
						t.Errorf("the directory must be synced after the rename")
					}
					events = append(events, "dir")
				default:
					t.Errorf("unexpected sync of %s", p)
				}
				return nil
			}
			seen := false
			op, err := l.swap(func(stage string) error {
				if stage == swapShutdown {
					seen = true
					if got := strings.Join(events, ","); got != "file,dir" {
						t.Errorf("the journal advanced after syncs %q, want file,dir", got)
					}
				}
				return nil
			})
			if err != nil || op.Status != "completed" || !seen {
				t.Fatalf("swap: %+v %v (hook seen %v)", op, err, seen)
			}
		})
	}
}

// A journal written before the code was recorded still replays with the code the swap then answered with.
func TestSwapReplaysAJournalWithoutACode(t *testing.T) {
	l := newSwapLab(t)
	l.put("fail_shutdown", "")
	if _, err := l.swap(nil); codeOf(err) != SwapShutdownFailed {
		t.Fatalf("first answer %v", err)
	}
	j, err := l.e.load(l.req.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	j.Swap.Code = ""
	if err = l.e.save(j); err != nil {
		t.Fatal(err)
	}
	if op, err := l.swap(nil); codeOf(err) != SwapShutdownFailed || op.Status != "failed" {
		t.Fatalf("replay answered %+v %v", op, err)
	}
}

// A copy that fails halfway leaves no temporary file behind, and a temporary file a crash left is pruned
// when the next swap commits.
func TestSwapLeavesNoPartialImages(t *testing.T) {
	t.Run("failed copy", func(t *testing.T) {
		l := newSwapLab(t)
		fakeCommand(t, "cp", `for a; do d="$a"; done; echo partial > "$d"; exit 1`)
		if _, err := l.swap(nil); codeOf(err) != SwapStageFailed {
			t.Fatalf("expected %s: %v", SwapStageFailed, err)
		}
		if _, err := os.Stat(l.newPath() + ".part"); !os.IsNotExist(err) {
			t.Fatalf("a partial image was left: %v", err)
		}
	})
	t.Run("crash leftover", func(t *testing.T) {
		l := newSwapLab(t)
		stale := filepath.Join(filepath.Dir(l.data), "system-"+strings.Repeat("c", 64)+".img.part")
		if err := os.WriteFile(stale, []byte("cut"), 0o444); err != nil {
			t.Fatal(err)
		}
		if op, err := l.swap(nil); err != nil || op.Status != "completed" {
			t.Fatalf("swap: %+v %v", op, err)
		}
		if _, err := os.Stat(stale); !os.IsNotExist(err) {
			t.Fatalf("a stale partial image outlived the commit: %v", err)
		}
	})
}

// A service that stops during the admission probe is an interruption, not a storage verdict: no refusal
// is published and no journal exists.
func TestSwapAdmissionInterruptedByCancelIsNotAStorageRefusal(t *testing.T) {
	real, err := exec.LookPath("cp")
	if err != nil {
		t.Fatal(err)
	}
	l := newSwapLab(t)
	fakeCommand(t, "cp", `case "$*" in *reflink-probe) touch "$LAB/stalled"; exec sleep 30;; esac
exec `+real+` "$@"`)
	l.e.statfs = func(_ string, st *syscall.Statfs_t) error {
		st.Type, st.Bsize, st.Bavail = 0xef53, 4096, 0
		return nil
	}
	_, err = l.swapWith(l.cancelOnStall(), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected the cancellation, got %v", err)
	}
	if _, err = l.e.load(l.req.RequestID); err == nil {
		t.Fatal("an interrupted admission must not leave a journal")
	}
}
