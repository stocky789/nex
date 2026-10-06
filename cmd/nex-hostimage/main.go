// SPDX-License-Identifier: AGPL-3.0-only

// Command nex-hostimage is the disk side of Nex Core's image: the installer and Core's
// slot writer run the same code (internal/hostimage).
//
//	nex-hostimage install --layout single|raid1 --disks /dev/a[,/dev/b] --image-dir DIR [--etc-seed DIR] [--boot uefi|bios] [--grub-chroot DIR]
//	nex-hostimage repair  --layout single|raid1 --disks /dev/a[,/dev/b] --image-dir DIR [--boot uefi|bios] [--grub-chroot DIR]
//	nex-hostimage esp-sync
//	nex-hostimage bless good|bad
//	nex-hostimage boot-tries
//	nex-hostimage etc-audit --lower DIR [--upper /var/lib/nex/etc/upper]
//	nex-hostimage verify DIR
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"nex.local/nex/internal/hostimage"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "nex-hostimage:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: nex-hostimage install|repair|esp-sync|bless|boot-tries|etc-audit|verify")
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	w := &hostimage.Writer{Runner: hostimage.ExecRunner{}, Logf: func(f string, a ...any) { fmt.Fprintf(stdout, f+"\n", a...) }}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "install", "repair":
		var o hostimage.Options
		boot := fs.String("boot", "auto", "boot method: uefi, bios, or auto for the running firmware")
		disks := fs.String("disks", "", "comma-separated whole-disk device nodes")
		fs.StringVar(&o.Layout, "layout", "", "single or raid1")
		fs.StringVar(&o.ImageDir, "image-dir", "", "release directory (manifest.json, root.erofs, ...)")
		fs.StringVar(&o.EtcSeed, "etc-seed", "", "install only: tree copied into the /etc overlay")
		fs.StringVar(&w.GrubChroot, "grub-chroot", "", "bios only: run grub-install chrooted into this directory")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *disks == "" || o.ImageDir == "" {
			return fmt.Errorf("--disks and --image-dir are required")
		}
		m, err := hostimage.ParseBootMethod(*boot)
		if err != nil {
			return err
		}
		w.Boot = m
		o.Disks = strings.Split(*disks, ",")
		if args[0] == "repair" {
			return w.Repair(ctx, o)
		}
		return w.Install(ctx, o)
	case "esp-sync":
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return w.ESPSync(ctx)
	case "bless":
		// A BIOS host's counterpart of systemd-bless-boot: good drops the running entry's boot counter, bad
		// exhausts its tries. UEFI hosts use systemd-bless-boot itself.
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 || (fs.Arg(0) != "good" && fs.Arg(0) != "bad") {
			return fmt.Errorf("usage: nex-hostimage bless good|bad")
		}
		return w.BlessBoot(ctx, fs.Arg(0) == "good")
	case "boot-tries":
		// Prints the boot tries the running entry has left; nothing when it is blessed. BIOS hosts only.
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
			return fmt.Errorf("usage: nex-hostimage boot-tries")
		}
		left, counted, err := w.BootTries(ctx)
		if counted {
			fmt.Fprintln(stdout, left)
		}
		return err
	case "etc-audit":
		// The image's /etc is hidden below the overlay and no copy of it ships, so the caller names it: a bind
		// mount of / (not recursive, so it does not carry the /etc overlay) shows it, e.g.
		// mount --bind / /mnt && nex-hostimage etc-audit --lower /mnt/etc
		lower := fs.String("lower", "", "image /etc (the lower layer; required, see the comment in main.go)")
		upper := fs.String("upper", "/var/lib/nex/etc/upper", "overlay upper layer")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *lower == "" {
			return fmt.Errorf("etc-audit: --lower DIR is required (the image /etc, e.g. /mnt/etc after mount --bind / /mnt)")
		}
		list, err := hostimage.AuditEtc(*lower, *upper)
		for _, s := range list {
			kind := "shadows image file"
			if s.Whiteout {
				kind = "deletes image file"
			}
			fmt.Fprintf(stdout, "/etc/%s: %s\n", s.Path, kind)
		}
		return err
	case "verify":
		if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 1 {
			return fmt.Errorf("usage: nex-hostimage verify DIR")
		}
		rel, err := hostimage.ReadRelease(fs.Arg(0))
		if err == nil {
			fmt.Fprintf(stdout, "release %s sequence %d roothash %s: ok\n", rel.Manifest.Label, rel.Manifest.Sequence, rel.Manifest.RootHash)
		}
		return err
	}
	return fmt.Errorf("unknown command %q", args[0])
}
