// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// The BIOS boot partition holds, besides what grub-install puts under grub/i386-pc:
//
//	grub/grub.cfg      the boot menu below; the same bytes on every disk and every release
//	grub/grubenv       the boot counters (a GRUB environment block)
//	nex/a.cfg, b.cfg   the entry of each slot: sequence, label, kernel directory and command line
//	nex/<seq>/vmlinuz  the kernel and initrd of a release, taken out of its UKI
//	nex/<seq>/initrd
//
// An entry that names files that are not all there never exists: payload first, entry last (the commit point).
const (
	biosCfg     = "grub/grub.cfg"
	biosEnv     = "grub/grubenv"
	biosModules = "grub/i386-pc"
	biosDir     = "nex"

	// bootFSArg and bootSlotArg are the words the menu appends to the kernel command line, so that the running system
	// finds the boot partition it came from and the slot it runs (what LoaderDevicePartUUID does on UEFI).
	bootFSArg   = "nex.bootfs"
	bootSlotArg = "nex.slot"
)

// biosSlots are the entries the menu knows; the release dir names the slot partitions the same letters.
var biosSlots = []string{"a", "b"}

func slotCfgPath(slot string) string { return biosDir + "/" + slot + ".cfg" }
func payloadDir(seq int) string      { return fmt.Sprintf("%s/%010d", biosDir, seq) }
func triesVar(slot string) string    { return "nex_tries_" + slot }

var (
	slotCfgRE    = regexp.MustCompile(`^nex/([ab])\.cfg$`)
	payloadDirRE = regexp.MustCompile(`^nex/([0-9]{10})$`)
)

// isSlotCfg reports whether an ESP-relative path is a slot entry.
func isSlotCfg(rel string) bool { return slotCfgRE.MatchString(rel) }

// bootEntry is the content of a slot entry.
type bootEntry struct {
	Label    string
	Sequence int
	Cmdline  string
}

// bareLabelRE is what a release label may hold; the manifest reader accepts the same characters in the UKI name.
var bareLabelRE = regexp.MustCompile(`^[A-Za-z0-9.~_-]+$`)

// render writes the entry as a GRUB script the menu sources: assignments only, each value in double quotes. A
// command line that GRUB would expand or unquote is refused instead of escaped.
func (e bootEntry) render() ([]byte, error) {
	if !bareLabelRE.MatchString(e.Label) {
		return nil, fmt.Errorf("label %q cannot be written into a boot entry", e.Label)
	}
	if e.Sequence < 1 {
		return nil, fmt.Errorf("sequence %d cannot be written into a boot entry", e.Sequence)
	}
	if e.Cmdline == "" || strings.ContainsAny(e.Cmdline, "\"$\\`;{}()|&<>\n\r\x00") {
		return nil, fmt.Errorf("the kernel command line %q cannot be written into a boot entry", e.Cmdline)
	}
	return []byte(fmt.Sprintf("# Nex Core boot entry, written by nex-hostimage.\nset nex_seq=\"%010d\"\nset nex_label=\"%s\"\nset nex_dir=\"/%s\"\nset nex_cmdline=\"%s\"\n",
		e.Sequence, e.Label, payloadDir(e.Sequence), e.Cmdline)), nil
}

var entryLineRE = regexp.MustCompile(`^set (nex_[a-z]+)="([^"]*)"$`)

// parseBootEntry decodes render's output.
func parseBootEntry(b []byte) (bootEntry, error) {
	vars := map[string]string{}
	for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "#") {
			continue
		}
		m := entryLineRE.FindStringSubmatch(line)
		if m == nil {
			return bootEntry{}, fmt.Errorf("boot entry line %q is not one this version wrote", line)
		}
		vars[m[1]] = m[2]
	}
	seq, err := strconv.Atoi(vars["nex_seq"])
	if err != nil || len(vars["nex_seq"]) != 10 {
		return bootEntry{}, fmt.Errorf("boot entry sequence %q is not a 10-digit number", vars["nex_seq"])
	}
	e := bootEntry{Label: vars["nex_label"], Sequence: seq, Cmdline: vars["nex_cmdline"]}
	if vars["nex_dir"] != "/"+payloadDir(seq) || e.Label == "" || e.Cmdline == "" {
		return bootEntry{}, fmt.Errorf("boot entry is incomplete or inconsistent: %v", vars)
	}
	return e, nil
}

// decrement is GRUB script that counts the boot counter in $nex_t down by one: GRUB has no arithmetic, so every
// value up to ArmTries is spelled out. 0 and unset stay as they are.
func decrement() string {
	var sb strings.Builder
	for n := ArmTries; n >= 1; n-- {
		kw := "elif"
		if n == ArmTries {
			kw = "if"
		}
		fmt.Fprintf(&sb, "    %s [ \"$nex_t\" = %d ]; then set nex_t=%d\n", kw, n, n-1)
	}
	sb.WriteString("    fi\n")
	return sb.String()
}

// grubConfig is the boot menu: it picks the entry with the highest sequence that still has tries (or has no
// counter), falls back to the other one, and counts the counter of an entry down and saves it before it boots that
// entry, so that an entry that never reaches the bless step runs out of tries. When no entry has tries left the
// highest sequence still boots: a host with only exhausted entries is better off trying than stopping.
func grubConfig() []byte {
	var sb strings.Builder
	w := func(format string, a ...any) { fmt.Fprintf(&sb, format+"\n", a...) }
	w("# Nex Core boot menu, written by nex-hostimage. The same bytes sit on every disk; do not edit.")
	w("#")
	w("# nex/a.cfg and nex/b.cfg are the entries of the two slots; an entry appearing is what commits a release.")
	w("# grub/grubenv holds the boot counters: nex_tries_<slot> unset = blessed, N = tries left, 0 = exhausted.")
	w("# Core sets, clears and reads them; this file only counts down before it boots an entry.")
	w("set nex_base=($root)")
	w("set nex_env=$nex_base/grub/grubenv")
	w("set timeout=3")
	w("")
	w("if [ \"$grub_platform\" = pc ]; then")
	w("  insmod serial")
	w("  if serial --unit=0 --speed=115200; then")
	w("    terminal_input console serial")
	w("    terminal_output console serial")
	w("  fi")
	w("fi")
	w("insmod probe")
	w("probe --set=nex_bootfs --fs-uuid $nex_base")
	w("load_env --file $nex_env")
	w("")
	w("# nex_load SLOT: read the entry and the counter of a slot into nex_* (nex_t empty when blessed).")
	w("function nex_load {")
	w("  source $nex_base/nex/$1.cfg")
	for i, s := range biosSlots {
		kw := "elif"
		if i == 0 {
			kw = "if"
		}
		w("  %s [ \"$1\" = %s ]; then set nex_t=\"$%s\"", kw, s, triesVar(s))
	}
	w("  fi")
	w("}")
	w("")
	w("# nex_boot SLOT: count the counter down, save it, then load the slot's kernel and initrd.")
	w("function nex_boot {")
	w("  nex_load $1")
	w("  if [ -n \"$nex_t\" ]; then")
	sb.WriteString(strings.ReplaceAll(decrement(), "    ", "    "))
	for i, s := range biosSlots {
		kw := "elif"
		if i == 0 {
			kw = "if"
		}
		w("    %s [ \"$1\" = %s ]; then set %s=$nex_t; save_env --file $nex_env %s", kw, s, triesVar(s), triesVar(s))
	}
	w("    fi")
	w("  fi")
	w("  echo \"Loading Nex Core $nex_label ($nex_seq), slot $1\"")
	w("  linux $nex_base$nex_dir/vmlinuz $nex_cmdline %s=$nex_bootfs %s=$1", bootFSArg, bootSlotArg)
	w("  initrd $nex_base$nex_dir/initrd")
	w("}")
	w("")
	w("# The default is the highest sequence with tries left; nex_any is the highest sequence at all.")
	w("set nex_best=")
	w("set nex_best_seq=")
	w("set nex_any=")
	w("set nex_any_seq=")
	w("for nex_s in %s; do", strings.Join(biosSlots, " "))
	w("  if [ -f $nex_base/nex/$nex_s.cfg ]; then")
	w("    nex_load $nex_s")
	w("    if [ -z \"$nex_any\" ]; then set nex_any=$nex_s; set nex_any_seq=$nex_seq; fi")
	w("    if [ \"$nex_seq\" '>' \"$nex_any_seq\" ]; then set nex_any=$nex_s; set nex_any_seq=$nex_seq; fi")
	w("    if [ \"$nex_t\" != 0 ]; then")
	w("      if [ -z \"$nex_best\" ]; then set nex_best=$nex_s; set nex_best_seq=$nex_seq; fi")
	w("      if [ \"$nex_seq\" '>' \"$nex_best_seq\" ]; then set nex_best=$nex_s; set nex_best_seq=$nex_seq; fi")
	w("    fi")
	w("  fi")
	w("done")
	w("if [ -n \"$nex_best\" ]; then set nex_first=$nex_best; else set nex_first=$nex_any; fi")
	w("if [ -z \"$nex_first\" ]; then")
	w("  echo \"Nex Core: no boot entry found on $nex_base.\"")
	w("fi")
	w("")
	w("# One menu entry per slot that has an entry; the other slot is the fallback of the default one.")
	for _, s := range biosSlots {
		w("if [ -f $nex_base/nex/%s.cfg ]; then", s)
		w("  nex_load %s", s)
		w("  menuentry \"Nex Core $nex_label ($nex_seq), slot %s\" --id nex-%s {", s, s)
		w("    nex_boot %s", s)
		w("  }")
		w("fi")
	}
	w("if [ -n \"$nex_first\" ]; then")
	w("  set default=nex-$nex_first")
	w("  for nex_s in %s; do", strings.Join(biosSlots, " "))
	w("    if [ \"$nex_s\" != \"$nex_first\" ]; then")
	w("      if [ -f $nex_base/nex/$nex_s.cfg ]; then set fallback=nex-$nex_s; fi")
	w("    fi")
	w("  done")
	w("fi")
	return []byte(sb.String())
}
