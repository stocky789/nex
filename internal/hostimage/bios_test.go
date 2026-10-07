// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// assertBootable checks what the menu relies on: every entry names a kernel directory whose kernel and initrd are
// there. A boot partition for which this fails could be left unbootable by a power cut.
func assertBootable(t *testing.T, root string) {
	t.Helper()
	slots, _, err := readSlots(root)
	if err != nil {
		t.Fatalf("the boot partition has an unreadable entry: %v", err)
	}
	for _, s := range slots {
		for _, f := range []string{"vmlinuz", "initrd"} {
			p := filepath.Join(root, filepath.FromSlash(payloadDir(s.Entry.Sequence)), f)
			if st, err := os.Stat(p); err != nil || st.Size() == 0 {
				t.Errorf("entry %s (sequence %d) names %s, which is not there: %v", s.Slot, s.Entry.Sequence, f, err)
			}
		}
	}
}

func readRelease(t *testing.T, label string, seq int) *Release {
	t.Helper()
	rel, err := ReadRelease(makeRelease(t, label, seq, int64(seq), 16))
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestGrubEnvRoundTrip(t *testing.T) {
	b, err := grubEnv{"nex_tries_b": "3", "other": "keep"}.render()
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != grubEnvSize || !bytes.HasPrefix(b, []byte("# GRUB Environment Block\n")) || b[grubEnvSize-1] != '#' {
		t.Fatalf("block is %d bytes, header %q, last byte %q", len(b), b[:26], b[grubEnvSize-1])
	}
	if !bytes.Contains(b, []byte("\nnex_tries_b=3\nother=keep\n####")) {
		t.Errorf("variables are not in name order, followed by padding: %q", b[:80])
	}
	got, err := parseGrubEnv(b)
	if err != nil || !reflect.DeepEqual(got, grubEnv{"nex_tries_b": "3", "other": "keep"}) {
		t.Errorf("round trip: %v %v", got, err)
	}
	empty, _ := grubEnv{}.render()
	if got, err = parseGrubEnv(empty); err != nil || len(got) != 0 {
		t.Errorf("empty block: %v %v", got, err)
	}
	for name, bad := range map[string][]byte{
		"short":      b[:grubEnvSize-1],
		"long":       append(append([]byte(nil), b...), '#'),
		"no header":  append([]byte("# something else\n"), b[len("# GRUB Environment Block\n"):]...),
		"no equals":  append([]byte("# GRUB Environment Block\nfoo\n"), bytes.Repeat([]byte{'#'}, grubEnvSize-30)...),
		"escape":     append([]byte("# GRUB Environment Block\nfoo=a\\nb\n"), bytes.Repeat([]byte{'#'}, grubEnvSize-34)...),
		"empty name": append([]byte("# GRUB Environment Block\n=1\n"), bytes.Repeat([]byte{'#'}, grubEnvSize-28)...),
	} {
		if _, err := parseGrubEnv(bad); err == nil {
			t.Errorf("%s block accepted", name)
		}
	}
	if _, err := (grubEnv{"a": strings.Repeat("x", grubEnvSize)}).render(); err == nil {
		t.Error("a block over the size was rendered")
	}
	for _, bad := range []grubEnv{{"a=b": "1"}, {"": "1"}, {"a": "x\ny"}, {"a": `x\y`}, {"#a": "1"}} {
		if _, err := bad.render(); err == nil {
			t.Errorf("%v rendered", bad)
		}
	}
}

func TestGrubEnvFileIsReplacedWholeAndKeepsItsSize(t *testing.T) {
	p := filepath.Join(t.TempDir(), "grub", "grubenv")
	if env, err := readGrubEnv(p); err != nil || len(env) != 0 {
		t.Fatalf("a missing block is an empty one: %v %v", env, err)
	}
	if err := writeGrubEnv(p, grubEnv{"nex_tries_a": "3"}); err != nil {
		t.Fatal(err)
	}
	if err := writeGrubEnv(p, grubEnv{"nex_tries_a": "2"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() != grubEnvSize {
		t.Fatalf("block on disk: %v %v", st, err)
	}
	if left, _ := filepath.Glob(p + "*"); len(left) != 1 {
		t.Errorf("temp file left behind: %v", left)
	}
	if env, err := readGrubEnv(p); err != nil || env["nex_tries_a"] != "2" {
		t.Errorf("read back: %v %v", env, err)
	}
	os.WriteFile(p, []byte("garbage"), 0o644)
	if _, err := readGrubEnv(p); err == nil {
		t.Error("a damaged block was read as if it were fine")
	}
}

func TestBootEntryRoundTripAndRefusals(t *testing.T) {
	e := bootEntry{Label: "2026.10.2-1~rc_1", Sequence: 12, Cmdline: "roothash=" + strings.Repeat("ab", 32) + " ro console=ttyS0,115200n8"}
	b, err := e.render()
	if err != nil {
		t.Fatal(err)
	}
	want := "set nex_seq=\"0000000012\"\nset nex_label=\"2026.10.2-1~rc_1\"\nset nex_dir=\"/nex/0000000012\"\nset nex_cmdline=\"" + e.Cmdline + "\"\n"
	if !strings.HasSuffix(string(b), want) {
		t.Errorf("entry text:\n%s", b)
	}
	if got, err := parseBootEntry(b); err != nil || got != e {
		t.Errorf("round trip: %+v %v", got, err)
	}
	for _, bad := range []bootEntry{
		{Label: "a b", Sequence: 1, Cmdline: "ro"}, {Label: "", Sequence: 1, Cmdline: "ro"}, {Label: "a", Sequence: 0, Cmdline: "ro"},
		{Label: "a", Sequence: 1, Cmdline: ""}, {Label: "a", Sequence: 1, Cmdline: `ro "x"`}, {Label: "a", Sequence: 1, Cmdline: "ro $root"},
		{Label: "a", Sequence: 1, Cmdline: "ro\nsource /evil"}, {Label: "a", Sequence: 1, Cmdline: "ro; reboot"},
	} {
		if _, err := bad.render(); err == nil {
			t.Errorf("%+v rendered", bad)
		}
	}
	for _, bad := range []string{
		"set nex_seq=\"12\"\nset nex_label=\"a\"\nset nex_dir=\"/nex/0000000012\"\nset nex_cmdline=\"ro\"\n",         // not 10 digits
		"set nex_seq=\"0000000012\"\nset nex_label=\"a\"\nset nex_dir=\"/nex/0000000013\"\nset nex_cmdline=\"ro\"\n", // dir of another sequence
		"set nex_seq=\"0000000012\"\nset nex_label=\"a\"\nset nex_cmdline=\"ro\"\n",                                  // no dir
		"set nex_seq=\"0000000012\"\nlinux /x\n",
	} {
		if _, err := parseBootEntry([]byte(bad)); err == nil {
			t.Errorf("entry accepted:\n%s", bad)
		}
	}
}

func TestUKIPartsComeOutOfTheSignedUKI(t *testing.T) {
	rel := readRelease(t, "r1", 1)
	p, err := rel.biosParts()
	if err != nil {
		t.Fatal(err)
	}
	// The kernel is the section's content, not the padding that fills its last block.
	if p.kernel.size != int64(len("kernel-r1-1-1")) || p.initrd.size != int64(len("initrd-r1-1-1")) {
		t.Errorf("sizes %d %d include padding", p.kernel.size, p.initrd.size)
	}
	if want := "roothash=" + rel.Manifest.RootHash + " ro console=tty0 panic=10"; p.cmdline != want {
		t.Errorf("cmdline %q, want %q", p.cmdline, want)
	}
	for name, want := range map[string]string{sectionLinux: "kernel-r1-1-1", sectionInitrd: "initrd-r1-1-1"} {
		rc, err := rel.openSection(name)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if string(got) != want {
			t.Errorf("%s streams %q, want %q", name, got, want)
		}
	}
	if p.kernel.sha == p.initrd.sha || len(p.kernel.sha) != 64 {
		t.Errorf("hashes %s %s", p.kernel.sha, p.initrd.sha)
	}
}

func TestUKIPartsRefuseWhatIsNotTheManifestsUKI(t *testing.T) {
	dir := t.TempDir()
	hash, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	os.WriteFile(filepath.Join(dir, "garbage.efi"), []byte("not a PE file at all"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.efi"), SyntheticUKI("x", 1, 1, other), 0o644)
	if _, err := (&Release{Dir: dir, UKI: "garbage.efi", Manifest: Manifest{RootHash: hash}}).biosParts(); err == nil {
		t.Error("a file that is not a PE image was taken for a UKI")
	}
	if _, err := (&Release{Dir: dir, UKI: "other.efi", Manifest: Manifest{RootHash: hash}}).biosParts(); err == nil || !strings.Contains(err.Error(), "manifest") {
		t.Errorf("a UKI whose command line names another root hash: %v", err)
	}
	if _, err := (&Release{Dir: dir, UKI: "missing.efi", Manifest: Manifest{RootHash: hash}}).biosParts(); err == nil {
		t.Error("a missing UKI was accepted")
	}
}

func TestGrubConfigTextAndSyntax(t *testing.T) {
	cfg := string(grubConfig())
	if !bytes.Equal(grubConfig(), grubConfig()) {
		t.Error("the menu is not deterministic")
	}
	for _, want := range []string{
		"set nex_base=($root)",
		"load_env --file $nex_env",
		"save_env --file $nex_env nex_tries_a",
		"save_env --file $nex_env nex_tries_b",
		"nex.bootfs=$nex_bootfs nex.slot=$1",
		"menuentry \"Nex Core $nex_label ($nex_seq), slot a\" --id nex-a {",
		"menuentry \"Nex Core $nex_label ($nex_seq), slot b\" --id nex-b {",
		"set default=nex-$nex_first",
		"set fallback=nex-$nex_s",
	} {
		if !strings.Contains(cfg, want) {
			t.Errorf("menu lacks %q", want)
		}
	}
	// GRUB has no arithmetic: every counter value is spelled out, from ArmTries down to 1.
	for n := ArmTries; n >= 1; n-- {
		if want := fmt.Sprintf("[ \"$nex_t\" = %d ]; then set nex_t=%d", n, n-1); !strings.Contains(cfg, want) {
			t.Errorf("menu cannot count %d down: lacks %q", n, want)
		}
	}
	if strings.Contains(cfg, fmt.Sprintf("= %d ]", ArmTries+1)) {
		t.Error("the menu counts above ArmTries")
	}
	if _, err := exec.LookPath("grub-script-check"); err != nil {
		t.Skip("grub-script-check not installed")
	}
	p := filepath.Join(t.TempDir(), "grub.cfg")
	os.WriteFile(p, []byte(cfg), 0o644)
	if out, err := exec.Command("grub-script-check", p).CombinedOutput(); err != nil {
		t.Errorf("grub-script-check: %v\n%s", err, out)
	}
}

// menuPartition writes a boot partition the menu reads: entries for the given slots (sequence and counter; -1 =
// blessed, no counter).
func menuPartition(t *testing.T, slots map[string][2]int) string {
	t.Helper()
	// A short path: grub-emu wraps its output at 80 columns.
	root, err := os.MkdirTemp("", "m")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	env := grubEnv{}
	for slot, sc := range slots {
		seq, tries := sc[0], sc[1]
		b, err := bootEntry{Label: fmt.Sprintf("r%d", seq), Sequence: seq, Cmdline: fmt.Sprintf("roothash=%064d ro", seq)}.render()
		if err != nil {
			t.Fatal(err)
		}
		writeFiles(t, root, map[string]string{slotCfgPath(slot): string(b), payloadDir(seq) + "/vmlinuz": "k", payloadDir(seq) + "/initrd": "i"})
		if tries >= 0 {
			env[triesVar(slot)] = strconv.Itoa(tries)
		}
	}
	if err := writeGrubEnv(filepath.Join(root, filepath.FromSlash(biosEnv)), env); err != nil {
		t.Fatal(err)
	}
	writeFiles(t, root, map[string]string{biosCfg: string(grubConfig())})
	return root
}

// runMenu runs the real menu script in GRUB's own interpreter (grub-emu), with linux and initrd replaced by
// functions that print their arguments, and returns what it printed. boot, if set, is the slot to boot.
func runMenu(t *testing.T, root, boot string) string {
	t.Helper()
	emu, err := exec.LookPath("grub-emu")
	if err != nil {
		t.Skip("grub-emu not installed")
	}
	modules := "/usr/lib/grub/x86_64-emu"
	if _, err = os.Stat(modules); err != nil {
		t.Skip("grub-emu modules not installed")
	}
	// grub-emu cannot save_env (its host file system has no block lists), and its own linux and initrd commands
	// cannot be replaced by functions: those three commands are rewritten to print what they would do.
	cfg := strings.Replace(string(grubConfig()), "set nex_base=($root)", "set nex_base=(host)"+root, 1)
	for from, to := range map[string]string{"save_env --file $nex_env ": "echo SAVE ", "  linux ": "  echo LINUX ", "  initrd ": "  echo INITRD "} {
		if !strings.Contains(cfg, from) {
			t.Fatalf("the menu has no %q to rewrite", from)
		}
		cfg = strings.ReplaceAll(cfg, from, to)
	}
	script := cfg + "\necho \"DEFAULT=$default FALLBACK=$fallback.\"\n"
	if boot != "" {
		script += "nex_boot " + boot + "\necho \"TRIES_A=$nex_tries_a TRIES_B=$nex_tries_b.\"\n"
	}
	script += "echo END\n"
	p := filepath.Join(t.TempDir(), "harness.cfg")
	os.WriteFile(p, []byte(script), 0o644)
	cmd := exec.Command(emu, "-d", modules)
	cmd.Stdin = strings.NewReader("source (host)" + p + "\nhalt\n")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "END") {
		t.Fatalf("the menu script did not run to its end:\n%s", out)
	}
	// The terminal wraps long lines and puts blank lines between them: compare the text without line breaks.
	return strings.NewReplacer("\r", "", "\n", "").Replace(string(out))
}

func TestGrubMenuPicksTheEntryTheEngineExpects(t *testing.T) {
	if _, err := exec.LookPath("grub-emu"); err != nil {
		t.Skip("grub-emu not installed")
	}
	for _, c := range []struct {
		name     string
		slots    map[string][2]int
		def, fbk string
	}{
		{"one blessed entry", map[string][2]int{"a": {1, -1}}, "nex-a", ""},
		{"armed newer entry beside the running one", map[string][2]int{"a": {1, -1}, "b": {2, 3}}, "nex-b", "nex-a"},
		{"last try left", map[string][2]int{"a": {1, -1}, "b": {2, 1}}, "nex-b", "nex-a"},
		{"no tries left: the old entry again", map[string][2]int{"a": {1, -1}, "b": {2, 0}}, "nex-a", "nex-b"},
		{"blessed newer entry", map[string][2]int{"a": {1, -1}, "b": {2, -1}}, "nex-b", "nex-a"},
		{"newer entry in slot a", map[string][2]int{"a": {9, -1}, "b": {8, -1}}, "nex-a", "nex-b"},
		{"rolled back: previous armed, running exhausted", map[string][2]int{"a": {1, 3}, "b": {2, 0}}, "nex-a", "nex-b"},
		{"both exhausted: the newer still boots", map[string][2]int{"a": {1, 0}, "b": {2, 0}}, "nex-b", "nex-a"},
		{"sequence 10 sorts after 9", map[string][2]int{"a": {9, -1}, "b": {10, -1}}, "nex-b", "nex-a"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out := runMenu(t, menuPartition(t, c.slots), "")
			if want := fmt.Sprintf("DEFAULT=%s FALLBACK=%s.", c.def, c.fbk); !strings.Contains(out, want) {
				t.Errorf("want %q in:\n%s", want, out)
			}
		})
	}
	t.Run("no entry", func(t *testing.T) {
		out := runMenu(t, menuPartition(t, nil), "")
		if !strings.Contains(out, "DEFAULT= FALLBACK=.") || !strings.Contains(out, "no boot entry found") {
			t.Errorf("an empty partition:\n%s", out)
		}
	})
}

func TestGrubMenuCountsTheTriesDownBeforeItBoots(t *testing.T) {
	if _, err := exec.LookPath("grub-emu"); err != nil {
		t.Skip("grub-emu not installed")
	}
	for tries, want := range map[int]string{3: "2", 2: "1", 1: "0", 0: "0"} {
		t.Run(fmt.Sprintf("%d tries", tries), func(t *testing.T) {
			root := menuPartition(t, map[string][2]int{"a": {1, -1}, "b": {2, tries}})
			out := runMenu(t, root, "b")
			if !strings.Contains(out, "TRIES_A= TRIES_B="+want+".") || !strings.Contains(out, "SAVE nex_tries_b") {
				t.Errorf("counter after the boot, want %s saved:\n%s", want, out)
			}
			wantLine := "LINUX (host)" + root + "/nex/0000000002/vmlinuz roothash=" + fmt.Sprintf("%064d", 2) + " ro nex.bootfs= nex.slot=b"
			if !strings.Contains(out, wantLine) || !strings.Contains(out, "INITRD (host)"+root+"/nex/0000000002/initrd") {
				t.Errorf("kernel and initrd not loaded as expected (want %q):\n%s", wantLine, out)
			}
		})
	}
	t.Run("a blessed entry is not counted", func(t *testing.T) {
		root := menuPartition(t, map[string][2]int{"a": {1, -1}})
		if out := runMenu(t, root, "a"); strings.Contains(out, "SAVE") || !strings.Contains(out, "TRIES_A= TRIES_B=.") {
			t.Errorf("a blessed entry was counted:\n%s", out)
		}
	})
}

func TestWriteTableKeepsTheMBRBootCode(t *testing.T) {
	path := sparseDisk(t, testDisk)
	d, err := OpenDisk(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	code := make([]byte, 446)
	copy(code, []byte{0xeb, 0x63, 0x90})
	copy(code[392:], "GRUB ")
	if _, err = d.File.WriteAt(code, 0); err != nil {
		t.Fatal(err)
	}
	// A fresh layout wipes whatever boot code the disk had.
	if err = LayDisk(context.Background(), &recorder{}, d, LayoutSingle); err != nil {
		t.Fatal(err)
	}
	if got := diskHead(t, path, 446); !bytes.Equal(got, make([]byte, 446)) {
		t.Fatal("a freshly laid disk kept the old boot code")
	}
	if _, err = d.File.WriteAt(code, 0); err != nil {
		t.Fatal(err)
	}
	// Every later rewrite of the table (each slot write changes partition GUIDs) keeps it.
	tb, _ := d.ReadTable()
	if err = d.SetPartitionIDs(map[int]GUID{2: NewGUID(), 3: NewGUID()}); err != nil {
		t.Fatal(err)
	}
	head := diskHead(t, path, 512)
	if !bytes.Equal(head[:446], code) || !hasGrubBootCode(head) || head[510] != 0x55 || head[511] != 0xaa || head[446+4] != 0xee {
		t.Errorf("MBR after a table rewrite: boot code kept=%v type=%x", bytes.Equal(head[:446], code), head[446+4])
	}
	if again, err := d.ReadTable(); err != nil || len(again.Parts) != len(tb.Parts) {
		t.Errorf("table unreadable after the rewrite: %v", err)
	}
	if hasGrubBootCode(make([]byte, 446)) || hasGrubBootCode(code[:100]) {
		t.Error("blank or short sectors were taken for GRUB")
	}
}

func installBIOS(t *testing.T, layout string, disks int, chroot string) (*recorder, []string, string) {
	t.Helper()
	sandbox(t)
	var paths []string
	for i := range disks {
		p := filepath.Join(t.TempDir(), fmt.Sprintf("disk%d.img", i))
		os.WriteFile(p, nil, 0o644)
		os.Truncate(p, testDisk+int64(i)*256*MiB)
		paths = append(paths, p)
	}
	rel := makeRelease(t, "r1", 1, 1, 16)
	rec := &recorder{}
	w := &Writer{Runner: rec, Boot: BIOS, GrubChroot: chroot}
	if err := w.Install(context.Background(), Options{Layout: layout, Disks: paths, ImageDir: rel}); err != nil {
		t.Fatal(err)
	}
	return rec, paths, rel
}

func TestInstallBIOSSingleDisk(t *testing.T) {
	rec, disks, relDir := installBIOS(t, LayoutSingle, 1, "")
	disk := disks[0]
	tb := readTable(t, disk)
	bios := part(t, tb, BIOSLabel)
	if bios.Index != BIOSIndex || bios.Type != mustGUID(typeBIOS) || tb.Size(bios) != BIOSSize || tb.Offset(bios) != MiB {
		t.Errorf("BIOS boot partition: %+v", bios)
	}
	esp := part(t, tb, ESPLabel)
	if esp.Index != ESPIndex || tb.Offset(esp) != 2*MiB {
		t.Errorf("ESP: %+v at %d", esp, tb.Offset(esp))
	}
	mount := ESPRoot(NodeName(disk, ESPIndex))
	mi, gi, ui := rec.index("mount -t vfat"), rec.index("grub-install"), rec.index("umount "+mount)
	if mi < 0 || gi < mi || ui < gi {
		t.Errorf("grub-install must run with the boot partition mounted: %v", rec.calls)
	}
	if want := "grub-install --target=i386-pc --boot-directory=" + mount + " --modules=part_gpt fat --no-floppy " + disk; rec.calls[gi] != want {
		t.Errorf("grub-install call:\n got %s\nwant %s", rec.calls[gi], want)
	}
	if rec.index("efibootmgr") >= 0 {
		t.Error("a BIOS install registered a firmware boot entry")
	}
	rel, _ := ReadRelease(relDir)
	tree := tree(t, mount)
	wantKeys := []string{"grub/grub.cfg", "grub/grubenv", "nex/0000000001/initrd", "nex/0000000001/vmlinuz", "nex/a.cfg"}
	var keys []string
	for k := range tree {
		keys = append(keys, k)
	}
	if !sameSet(keys, wantKeys) {
		t.Errorf("boot partition holds %v, want %v", keys, wantKeys)
	}
	if tree["grub/grub.cfg"] != string(grubConfig()) || tree["nex/0000000001/vmlinuz"] != "kernel-r1-1-1" || tree["nex/0000000001/initrd"] != "initrd-r1-1-1" {
		t.Errorf("menu, kernel or initrd wrong: %q %q %q", tree["grub/grub.cfg"][:20], tree["nex/0000000001/vmlinuz"], tree["nex/0000000001/initrd"])
	}
	e, err := parseBootEntry([]byte(tree["nex/a.cfg"]))
	if err != nil || e.Sequence != 1 || e.Label != "r1" || cmdlineRootHash(e.Cmdline) != rel.Manifest.RootHash {
		t.Errorf("entry: %+v %v", e, err)
	}
	if env, err := parseGrubEnv([]byte(tree["grub/grubenv"])); err != nil || len(env) != 0 {
		t.Errorf("a fresh install starts blessed, without counters: %v %v", env, err)
	}
	entries, _ := BIOS.entries(mount, "")
	if len(entries) != 1 || !entries[0].Blessed() || entries[0].Sequence != 1 {
		t.Errorf("entries: %+v", entries)
	}
	// The slot GUIDs still select slot A by the root hash the entry carries.
	rootID, _, _ := SlotIDs(rel.Manifest.RootHash)
	if a := part(t, tb, "nex-root-a"); a.ID != rootID {
		t.Error("slot A is not selected by the entry's root hash")
	}
}

func TestInstallBIOSOnEveryDiskThroughTheGrubChroot(t *testing.T) {
	chroot := t.TempDir()
	rec, disks, _ := installBIOS(t, LayoutRAID1, 2, chroot)
	var calls []string
	for _, c := range rec.calls {
		if strings.Contains(c, "grub-install") {
			calls = append(calls, c)
		}
	}
	if len(calls) != 2 {
		t.Fatalf("grub-install calls: %v", calls)
	}
	for i, d := range disks {
		mount := ESPRoot(NodeName(d, ESPIndex))
		bind := "mount --bind " + mount + " " + filepath.Join(chroot, mount)
		if rec.index(bind) < 0 || rec.index("umount "+filepath.Join(chroot, mount)) < 0 {
			t.Errorf("disk %d: the boot partition is not bound into the chroot and released again: %v", i, rec.calls)
		}
		if want := "chroot " + chroot + " grub-install --target=i386-pc --boot-directory=" + mount + " --modules=part_gpt fat --no-floppy " + d; calls[i] != want {
			t.Errorf("disk %d:\n got %s\nwant %s", i, calls[i], want)
		}
	}
	a, b := tree(t, ESPRoot(NodeName(disks[0], ESPIndex))), tree(t, ESPRoot(NodeName(disks[1], ESPIndex)))
	if !reflect.DeepEqual(a, b) || len(a) != 5 {
		t.Errorf("the two boot partitions differ: %v vs %v", a, b)
	}
	for _, d := range disks {
		if readTable(t, d); len(readTable(t, d).Parts) != 7 {
			t.Errorf("%s does not carry the 7-partition layout", d)
		}
	}
}

func TestBIOSInstallFailsWhenGrubInstallFails(t *testing.T) {
	sandbox(t)
	disk := sparseDisk(t, testDisk)
	rel := makeRelease(t, "r1", 1, 1, 16)
	rec := &recorder{fail: map[string]error{"grub-install": errors.New("grub-install: cannot find a BIOS boot partition")}}
	err := (&Writer{Runner: rec, Boot: BIOS}).Install(context.Background(), Options{Layout: LayoutSingle, Disks: []string{disk}, ImageDir: rel})
	if err == nil || !strings.Contains(err.Error(), "BIOS boot partition") {
		t.Fatalf("a disk that did not get its boot loader was reported installed: %v", err)
	}
}

func TestBIOSRepairRewritesTheBootPartitionAndTheLoader(t *testing.T) {
	rec, disks, _ := installBIOS(t, LayoutSingle, 1, "")
	rel2 := makeRelease(t, "r2", 2, 2, 16)
	rec2 := &recorder{}
	if err := (&Writer{Runner: rec2, Boot: BIOS}).Repair(context.Background(), Options{Layout: LayoutSingle, Disks: disks, ImageDir: rel2}); err != nil {
		t.Fatal(err)
	}
	_ = rec
	if rec2.index("grub-install") < 0 {
		t.Errorf("repair wiped the BIOS boot partition and did not reinstall GRUB: %v", rec2.calls)
	}
	tr := tree(t, ESPRoot(NodeName(disks[0], ESPIndex)))
	if tr["nex/0000000002/vmlinuz"] != "kernel-r2-2-2" || tr["nex/0000000001/vmlinuz"] != "" {
		t.Errorf("repaired boot partition: %v", tr)
	}
}

func sameSet(a, b []string) bool {
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sortStrings(x)
	sortStrings(y)
	return reflect.DeepEqual(x, y)
}

func sortStrings(s []string) {
	for i := range s {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

func TestBIOSArmCommitsTheEntryLast(t *testing.T) {
	for _, c := range []struct {
		point   string
		counted bool // the slot's counter is already set when the host stops
	}{{"arm.temp", false}, {"arm.counted", true}} {
		t.Run(c.point, func(t *testing.T) {
			r := newRig(t, BIOS, LayoutSingle, 1)
			ctx := context.Background()
			rel2 := r.release(2)
			if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
				t.Fatal(err)
			}
			withCrash(t, c.point, 1)
			if err := r.host.Arm(ctx, rel2); err == nil {
				t.Fatal("arm did not stop")
			}
			crashPoint = nil
			root := r.esp(0)
			// Kernel and initrd are there, the entry is not: the menu has nothing new to pick.
			for _, f := range []string{"vmlinuz", "initrd"} {
				if _, err := os.Stat(filepath.Join(root, "nex", "0000000002", f)); err != nil {
					t.Errorf("payload %s missing: %v", f, err)
				}
			}
			if _, err := os.Stat(filepath.Join(root, "nex", "b.cfg")); err == nil {
				t.Error("the entry exists before the arm finished")
			}
			if env, _ := readGrubEnv(filepath.Join(root, "grub", "grubenv")); (env["nex_tries_b"] == "3") != c.counted {
				t.Errorf("counter state %v", env)
			}
			r.wantDefaults(1)
			assertBootable(t, root)
			if err := r.host.Arm(ctx, rel2); err != nil {
				t.Fatal(err)
			}
			r.wantDefaults(2)
			if left := r.temps(0); len(left) != 0 {
				t.Errorf("leftover temp files %v", left)
			}
			if es := r.entries(0); len(es) != 2 {
				t.Errorf("entries after the retry: %+v", es)
			}
		})
	}
}

func TestBIOSStageRemovesTheKernelsOfTheSlotBeingRewritten(t *testing.T) {
	r := newRig(t, BIOS, LayoutSingle, 1)
	ctx := context.Background()
	rel2 := r.release(2)
	if _, err := r.host.StageSlot(ctx, rel2, r.rels[1].Manifest.RootHash, 1); err != nil {
		t.Fatal(err)
	}
	if err := r.host.Arm(ctx, rel2); err != nil {
		t.Fatal(err)
	}
	r.bless(0, 2)
	// Running is 2 (slot b); staging 3 keeps only 2.
	rel3 := r.release(3)
	if _, err := r.host.StageSlot(ctx, rel3, rel2.Manifest.RootHash, 2); err != nil {
		t.Fatal(err)
	}
	root := r.esp(0)
	entries, _ := os.ReadDir(filepath.Join(root, "nex"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !sameSet(names, []string{"0000000002", "b.cfg"}) {
		t.Errorf("nex/ holds %v after staging over slot a", names)
	}
	if env, _ := readGrubEnv(filepath.Join(root, "grub", "grubenv")); len(env) != 0 {
		t.Errorf("counters of removed entries remain: %v", env)
	}
	assertBootable(t, root)
}

// src builds a booted boot partition with sequence 2 (slot b) running and sequence 1 (slot a) retained.
func twoReleasePartition(t *testing.T) (root string, rel1, rel2 *Release) {
	t.Helper()
	sandbox(t)
	root = t.TempDir()
	rel1, rel2 = readRelease(t, "r1", 1), readRelease(t, "r2", 2)
	if err := BIOS.place(root, rel1, "a"); err != nil {
		t.Fatal(err)
	}
	if err := BIOS.arm(root, rel2, "b", ArmTries); err != nil {
		t.Fatal(err)
	}
	if err := Bless(root, 2); err != nil {
		t.Fatal(err)
	}
	return root, rel1, rel2
}

func TestConvergeBIOSCopiesTheBootPartitionAndLeavesTheModules(t *testing.T) {
	src, _, _ := twoReleasePartition(t)
	dst := t.TempDir()
	writeFiles(t, dst, map[string]string{"grub/i386-pc/normal.mod": "module", "nex/b.cfg": "stale", "nex/0000000009/vmlinuz": "stale kernel",
		"nex/old.txt": "x"})
	files, err := scanBIOS(src)
	if err != nil {
		t.Fatal(err)
	}
	if err = convergeBIOS(dst, files); err != nil {
		t.Fatal(err)
	}
	got, want := tree(t, dst), tree(t, src)
	want["grub/i386-pc/normal.mod"] = "module"
	if !reflect.DeepEqual(got, want) {
		t.Errorf("converged tree differs:\n got %v\nwant %v", keysOf(got), keysOf(want))
	}
	assertBootable(t, dst)
	// A second run changes nothing.
	before := tree(t, dst)
	if err = convergeBIOS(dst, files); err != nil || !reflect.DeepEqual(before, tree(t, dst)) {
		t.Errorf("not idempotent: %v", err)
	}
}

func keysOf(m map[string]string) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// A power cut between any two steps of a convergence leaves a boot partition whose every entry is complete, and a
// second run finishes the job.
func TestConvergeBIOSInterruptedAtEveryStepStaysBootable(t *testing.T) {
	for _, point := range []string{"converge.files", "converge.env", "converge.entries"} {
		for _, start := range []string{"blank", "older"} {
			t.Run(point+"/"+start, func(t *testing.T) {
				src, rel1, _ := twoReleasePartition(t)
				dst := t.TempDir()
				if start == "older" {
					if err := BIOS.place(dst, rel1, "a"); err != nil {
						t.Fatal(err)
					}
				}
				files, _ := scanBIOS(src)
				withCrash(t, point, 1)
				if err := convergeBIOS(dst, files); err == nil {
					t.Fatal("convergence did not stop")
				}
				crashPoint = nil
				assertBootable(t, dst)
				if err := convergeBIOS(dst, files); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(tree(t, dst), tree(t, src)) {
					t.Errorf("the second run did not finish the job: %v", keysOf(tree(t, dst)))
				}
				if left := leftoverTemps(dst); len(left) != 0 {
					t.Errorf("temp files remain: %v", left)
				}
			})
		}
	}
}

func leftoverTemps(root string) []string {
	var out []string
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.HasSuffix(p, tempSuffix) {
			out = append(out, p)
		}
		return nil
	})
	return out
}

func TestConvergeBIOSWithABadSourceChangesNothingThatBoots(t *testing.T) {
	src, rel1, _ := twoReleasePartition(t)
	dst := t.TempDir()
	if err := BIOS.place(dst, rel1, "a"); err != nil {
		t.Fatal(err)
	}
	before := tree(t, dst)
	files, _ := scanBIOS(src)
	for i := range files {
		if files[i].Rel == "nex/0000000002/initrd" {
			// The source changes between the scan and the copy (a failing disk): the bytes do not match the hash.
			files[i].open = func() (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("corrupt")), nil }
		}
	}
	if err := convergeBIOS(dst, files); err == nil || !strings.Contains(err.Error(), "nex/0000000002/initrd") {
		t.Fatalf("a corrupt source was accepted: %v", err)
	}
	if left := leftoverTemps(dst); len(left) != 0 {
		t.Errorf("a half-written file stays: %v", left)
	}
	assertBootable(t, dst)
	if got := tree(t, dst); got["nex/a.cfg"] != before["nex/a.cfg"] || got["nex/b.cfg"] != "" {
		t.Errorf("the new entry appeared although its initrd did not verify: %v", keysOf(got))
	}
	// An unreadable source fails the same way.
	files[0].open = func() (io.ReadCloser, error) { return nil, errors.New("I/O error") }
	if err := convergeBIOS(dst, files); err == nil {
		t.Error("an unreadable source was accepted")
	}
}

func TestBIOSRollbackAndDisarmOnTheEntries(t *testing.T) {
	root, _, _ := twoReleasePartition(t)
	changed, err := BIOS.rollback(root, 2, 1)
	if err != nil || !changed {
		t.Fatalf("rollback: %v %v", changed, err)
	}
	es, _ := BIOS.entries(root, "n")
	order := BootOrder(es)
	if order[0].Sequence != 1 || !order[0].Counted || order[0].Tries != ArmTries || !order[1].Bad() || order[1].Sequence != 2 || order[1].Done != ArmTries {
		t.Errorf("after rollback: %+v", order)
	}
	if changed, err = BIOS.rollback(root, 2, 1); err != nil || changed {
		t.Errorf("a repeated rollback changed %v (%v)", changed, err)
	}
	if err = BIOS.disarm(root, 2); err != nil {
		t.Fatal(err)
	}
	es, _ = BIOS.entries(root, "n")
	if len(es) != 1 || es[0].Sequence != 1 {
		t.Errorf("after disarm: %+v", es)
	}
	if _, err = os.Stat(filepath.Join(root, "nex", "0000000002")); err == nil {
		t.Error("the kernel of the disarmed release remains")
	}
	if env, _ := readGrubEnv(filepath.Join(root, "grub", "grubenv")); env[triesVar("b")] != "" {
		t.Errorf("counter of the disarmed slot remains: %v", env)
	}
	if err = BIOS.disarm(root, 2); err != nil {
		t.Errorf("disarm is not repeatable: %v", err)
	}
}

// fakeBIOSSystem builds what ESPSync and the bless step read for BIOS hosts: partitions by name with their disks in
// sysfs, mounts, the by-uuid links and the kernel command line of the booted one.
func fakeBIOSSystem(t *testing.T, booted, cmdlineExtra string, parts map[string]string) {
	t.Helper()
	fakeSystem(t, parts)
	oldCmd, oldUUID := procCmdline, devByUUID
	dir := t.TempDir()
	procCmdline, devByUUID = filepath.Join(dir, "cmdline"), filepath.Join(dir, "by-uuid")
	t.Cleanup(func() { procCmdline, devByUUID = oldCmd, oldUUID })
	os.MkdirAll(devByUUID, 0o755)
	i := 0
	for n := range parts {
		disk := strings.TrimSuffix(n, "1")
		real := filepath.Join(dir, "devices", disk, n)
		os.MkdirAll(real, 0o755)
		os.WriteFile(filepath.Join(real, "uevent"), []byte("DEVTYPE=partition\nPARTNAME=nex-esp\n"), 0o644)
		os.WriteFile(filepath.Join(real, "partition"), []byte("1\n"), 0o644)
		os.RemoveAll(filepath.Join(sysClassBlock, n))
		if err := os.Symlink(real, filepath.Join(sysClassBlock, n)); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(devDir, disk), nil, 0o644)
		uuid := fmt.Sprintf("AB12-CD3%d", i)
		i++
		os.Symlink(filepath.Join(devDir, n), filepath.Join(devByUUID, uuid))
		if n == booted {
			os.WriteFile(procCmdline, []byte("BOOT_IMAGE=/nex/x ro "+cmdlineExtra+" nex.bootfs="+strings.ToLower(uuid)+"\n"), 0o644)
		}
	}
}

func TestESPSyncPropagatesTheBlessedBIOSPartitionAndHealsAReplacedDisk(t *testing.T) {
	src, _, rel2 := twoReleasePartition(t)
	other := t.TempDir()
	fakeBIOSSystem(t, "vda1", "", map[string]string{"vda1": src, "vdb1": other})
	rec := &recorder{}
	w := &Writer{Runner: rec, Boot: BIOS}
	if err := w.ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree(t, src), tree(t, other)) {
		t.Errorf("boot partitions differ after the sync: %v vs %v", keysOf(tree(t, src)), keysOf(tree(t, other)))
	}
	// The replaced disk is blank: it gets its boot loader. The booted one is left alone.
	want := "grub-install --target=i386-pc --boot-directory=" + other + " --modules=part_gpt fat --no-floppy " + filepath.Join(devDir, "vdb")
	if rec.index("grub-install") < 0 || rec.calls[rec.index("grub-install")] != want {
		t.Errorf("heal did not install GRUB on the replaced disk:\n got %v\nwant %s", rec.calls, want)
	}
	if n := strings.Count(strings.Join(rec.calls, "\n"), "grub-install"); n != 1 {
		t.Errorf("grub-install ran %d times", n)
	}
	assertBootable(t, other)
	_ = rel2

	// A disk that has its boot code and its modules is not touched again.
	code := make([]byte, 512)
	copy(code, []byte{0xeb, 0x63, 0x90})
	copy(code[392:], "GRUB ")
	os.WriteFile(filepath.Join(devDir, "vdb"), code, 0o644)
	writeFiles(t, other, map[string]string{"grub/i386-pc/normal.mod": "m"})
	rec2 := &recorder{}
	if err := (&Writer{Runner: rec2, Boot: BIOS}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec2.index("grub-install") >= 0 {
		t.Errorf("a healthy disk got GRUB again: %v", rec2.calls)
	}
}

func TestESPSyncWaitsForTheBIOSEntryToBeBlessed(t *testing.T) {
	src, rel1, _ := twoReleasePartition(t)
	if _, err := BIOS.rollback(src, 2, 1); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	if err := BIOS.place(other, rel1, "a"); err != nil {
		t.Fatal(err)
	}
	before := tree(t, other)
	fakeBIOSSystem(t, "vda1", "", map[string]string{"vda1": src, "vdb1": other})
	if err := (&Writer{Runner: &recorder{}, Boot: BIOS}).ESPSync(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(tree(t, other), before) {
		t.Error("the other boot partition changed before the running entry was blessed")
	}
}

func TestESPSyncDoesNothingWhenNotStartedByTheNexMenu(t *testing.T) {
	sandbox(t)
	src := t.TempDir()
	fakeBIOSSystem(t, "", "", map[string]string{"vda1": src})
	os.WriteFile(procCmdline, []byte("ro quiet\n"), 0o644)
	rec := &recorder{}
	if err := (&Writer{Runner: rec, Boot: BIOS}).ESPSync(context.Background()); err != nil || len(rec.calls) != 0 {
		t.Errorf("%v %v", err, rec.calls)
	}
}

func TestBlessBootAndTriesOnBIOS(t *testing.T) {
	sandbox(t)
	root := t.TempDir()
	rel1, rel2 := readRelease(t, "r1", 1), readRelease(t, "r2", 2)
	if err := BIOS.place(root, rel1, "a"); err != nil {
		t.Fatal(err)
	}
	if err := BIOS.arm(root, rel2, "b", ArmTries); err != nil {
		t.Fatal(err)
	}
	if err := MarkTried(root, 2); err != nil {
		t.Fatal(err)
	}
	fakeBIOSSystem(t, "vda1", "roothash="+rel2.Manifest.RootHash+" nex.slot=b", map[string]string{"vda1": root})
	w := &Writer{Runner: &recorder{}, Boot: BIOS}
	ctx := context.Background()
	if left, counted, err := w.BootTries(ctx); err != nil || !counted || left != ArmTries-1 {
		t.Errorf("tries: %d %v %v", left, counted, err)
	}
	if err := w.BlessBoot(ctx, true); err != nil {
		t.Fatal(err)
	}
	if _, counted, err := w.BootTries(ctx); err != nil || counted {
		t.Errorf("blessed entry still counted: %v %v", counted, err)
	}
	if env, _ := readGrubEnv(filepath.Join(root, "grub", "grubenv")); len(env) != 0 {
		t.Errorf("counters after bless: %v", env)
	}
	if err := w.BlessBoot(ctx, true); err != nil {
		t.Errorf("blessing twice: %v", err)
	}
	// Bad: the tries are exhausted, so the next boot is the other entry.
	if err := w.BlessBoot(ctx, false); err != nil {
		t.Fatal(err)
	}
	es, _ := BIOS.entries(root, "")
	if o := BootOrder(es); o[0].Sequence != 1 || !o[1].Bad() {
		t.Errorf("after bad: %+v", o)
	}
	// The slot on the command line must hold the running root hash.
	os.WriteFile(procCmdline, []byte("roothash="+rel2.Manifest.RootHash+" nex.slot=a nex.bootfs=ab12-cd30\n"), 0o644)
	if err := w.BlessBoot(ctx, true); err == nil {
		t.Error("blessed a slot that does not hold the running root hash")
	}
	os.WriteFile(procCmdline, []byte("ro\n"), 0o644)
	if err := w.BlessBoot(ctx, true); !errors.Is(err, errNotBIOSBooted) {
		t.Errorf("bless without the menu's marker: %v", err)
	}
}

func TestBootMethodSelection(t *testing.T) {
	dir := t.TempDir()
	old := efiFirmwareDir
	t.Cleanup(func() { efiFirmwareDir = old })
	efiFirmwareDir = filepath.Join(dir, "missing")
	if DetectBoot() != BIOS || (&Writer{}).method() != BIOS {
		t.Error("a host without /sys/firmware/efi is a BIOS host")
	}
	efiFirmwareDir = dir
	if DetectBoot() != UEFI || (&Writer{}).method() != UEFI {
		t.Error("a host with /sys/firmware/efi is a UEFI host")
	}
	if (&Writer{Boot: BIOS}).method() != BIOS {
		t.Error("an explicit method is used")
	}
	for name, want := range map[string]BootMethod{"uefi": UEFI, "bios": BIOS, "auto": UEFI, "": UEFI} {
		if got, err := ParseBootMethod(name); err != nil || got != want {
			t.Errorf("%q: %v %v", name, got, err)
		}
	}
	if _, err := ParseBootMethod("grub"); err == nil {
		t.Error("unknown method accepted")
	}
}
