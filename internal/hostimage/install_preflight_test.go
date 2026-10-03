// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

type blockFixtureInfo struct {
	os.FileInfo
	dev uint64
}

func (s blockFixtureInfo) Mode() os.FileMode { return os.ModeDevice | 0o600 }
func (s blockFixtureInfo) Sys() any {
	st := *s.FileInfo.Sys().(*syscall.Stat_t)
	st.Rdev = s.dev
	return &st
}

type countedDiskFile struct {
	File
	writes *int
}

func (f countedDiskFile) WriteAt(b []byte, off int64) (int, error) {
	(*f.writes)++
	return f.File.WriteAt(b, off)
}

// All apparent block devices are sparse regular files. Only stat/geometry observations are
// substituted; OpenDisk, sysfs traversal, mount/swap parsing and installer validation run normally.
type diskPreflightFixture struct {
	t       *testing.T
	root    string
	devices map[string]uint64
	writes  int
}

func preflightFixture(t *testing.T) *diskPreflightFixture {
	t.Helper()
	f := &diskPreflightFixture{t: t, root: sandbox(t), devices: map[string]uint64{}}
	oldSys, oldDev, oldMounts, oldSwaps := sysBlockDir, blockDevDir, mountInfoPath, swapsPath
	oldStat, oldGeometry, oldOpen := diskFileInfo, blockDiskGeometry, installDisk
	sysBlockDir = filepath.Join(f.root, "sys", "dev", "block")
	blockDevDir = filepath.Join(f.root, "dev")
	mountInfoPath, swapsPath = filepath.Join(f.root, "mountinfo"), filepath.Join(f.root, "swaps")
	f.mkdir(sysBlockDir)
	f.mkdir(blockDevDir)
	f.write(mountInfoPath, "24 1 0:99 / /run rw - tmpfs unrelated rw\n")
	f.write(swapsPath, "Filename Type Size Used Priority\n")
	diskFileInfo = func(file *os.File) (os.FileInfo, error) {
		st, err := file.Stat()
		if err != nil {
			return nil, err
		}
		path, err := filepath.EvalSymlinks(file.Name())
		if err != nil {
			return nil, err
		}
		if dev, ok := f.devices[path]; ok {
			return blockFixtureInfo{FileInfo: st, dev: dev}, nil
		}
		return st, nil
	}
	blockDiskGeometry = func(file *os.File) (int64, int, error) {
		st, err := file.Stat()
		if err != nil {
			return 0, 0, err
		}
		return st.Size(), 512, nil
	}
	installDisk = func(path string, write bool) (*Disk, error) {
		d, err := OpenDisk(path, write)
		if err == nil {
			d.File = countedDiskFile{File: d.File, writes: &f.writes}
		}
		return d, err
	}
	t.Cleanup(func() {
		sysBlockDir, blockDevDir, mountInfoPath, swapsPath = oldSys, oldDev, oldMounts, oldSwaps
		diskFileInfo, blockDiskGeometry, installDisk = oldStat, oldGeometry, oldOpen
	})
	return f
}

func (f *diskPreflightFixture) mkdir(path string) {
	f.t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		f.t.Fatal(err)
	}
}

func (f *diskPreflightFixture) write(path, content string) {
	f.t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *diskPreflightFixture) link(target, path string) string {
	f.t.Helper()
	f.mkdir(filepath.Dir(path))
	if err := os.Symlink(target, path); err != nil {
		f.t.Fatal(err)
	}
	return path
}

func (f *diskPreflightFixture) node(name string, major, minor uint32, parent string, layout string) (string, string) {
	f.t.Helper()
	path := filepath.Join(blockDevDir, name)
	file, err := os.Create(path)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := file.Truncate(testDisk); err != nil {
		f.t.Fatal(err)
	}
	// Seed a valid repair layout before this regular file is observed as a block node.
	plan, err := Plan(testDisk, layout)
	if err != nil {
		f.t.Fatal(err)
	}
	d := &Disk{Path: path, File: file, Size: testDisk, SectorSize: 512}
	if err := d.WriteTable(tableFor(plan, 512)); err != nil {
		f.t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		f.t.Fatal(err)
	}
	sys := filepath.Join(f.root, "sys", "devices", name)
	if parent != "" {
		sys = filepath.Join(parent, name)
	}
	f.mkdir(filepath.Join(sys, "holders"))
	f.write(filepath.Join(sys, "dev"), fmt.Sprintf("%d:%d\n", major, minor))
	if parent != "" {
		f.write(filepath.Join(sys, "partition"), "1\n")
	}
	f.link(sys, filepath.Join(sysBlockDir, fmt.Sprintf("%d:%d", major, minor)))
	f.devices[path] = unix.Mkdev(major, minor)
	return path, sys
}

func assertPreflightRefusal(t *testing.T, f *diskPreflightFixture, operation, want string, o Options) {
	t.Helper()
	// If preflight regresses, stop after the first tool request instead of waiting for fake
	// kernel nodes. Writes and all runner calls must still be zero on the actual refusal path.
	rec := &recorder{fail: map[string]error{"udevadm": errors.New("unexpected destructive phase")}}
	w := &Writer{Runner: rec}
	var err error
	if operation == "install" {
		err = w.Install(context.Background(), o)
	} else {
		err = w.Repair(context.Background(), o)
	}
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("refusal = %v, want %q", err, want)
	}
	if f.writes != 0 || len(rec.calls) != 0 {
		t.Fatalf("refused operation wrote %d times and ran %v", f.writes, rec.calls)
	}
}

func TestInstallAndRepairRefuseInUseDisksBeforeWriting(t *testing.T) {
	for _, operation := range []string{"install", "repair"} {
		for _, use := range []string{"disk mount", "partition mount", "disk holder", "verity holder", "md holder", "disk swap", "partition swap", "missing mount observations", "missing holder observations"} {
			t.Run(operation+"/"+use, func(t *testing.T) {
				f := preflightFixture(t)
				disk, sys := f.node("nvme0n1", 259, 0, "", LayoutSingle)
				partition, psys := f.node("nvme0n1p1", 259, 1, sys, LayoutSingle)
				alias := f.link(disk, filepath.Join(blockDevDir, "disk", "by-id", "selected-disk"))
				want := "in use"
				switch use {
				case "disk mount":
					f.write(mountInfoPath, "24 1 259:0 / /mnt rw - ext4 /dev/root rw\n")
				case "partition mount":
					f.write(mountInfoPath, "24 1 259:1 / /var rw - ext4 UUID=fixture rw\n")
				case "disk holder":
					f.link(psys, filepath.Join(sys, "holders", "dm-2"))
				case "verity holder":
					f.link(sys, filepath.Join(psys, "holders", "dm-verity-root"))
				case "md holder":
					f.link(sys, filepath.Join(psys, "holders", "md127"))
				case "disk swap", "partition swap":
					swap := alias
					if use == "partition swap" {
						swap = f.link(partition, filepath.Join(blockDevDir, "swap-alias"))
					}
					f.write(swapsPath, "Filename Type Size Used Priority\n"+swap+" partition 1024 0 -2\n")
				case "missing mount observations":
					mountInfoPath = filepath.Join(f.root, "missing")
					want = "inspect mounts"
				case "missing holder observations":
					if err := os.Remove(filepath.Join(psys, "holders")); err != nil {
						t.Fatal(err)
					}
					want = "inspect holders"
				}
				assertPreflightRefusal(t, f, operation, want, Options{Layout: LayoutSingle, Disks: []string{alias}, ImageDir: makeRelease(t, "r1", 1, 1, 4)})
			})
		}
	}
}

func TestInstallAndRepairRefusePartitionAliasesBeforeWriting(t *testing.T) {
	for _, operation := range []string{"install", "repair"} {
		t.Run(operation, func(t *testing.T) {
			f := preflightFixture(t)
			_, sys := f.node("sda", 8, 0, "", LayoutSingle)
			partition, _ := f.node("sda1", 8, 1, sys, LayoutSingle)
			alias := f.link(partition, filepath.Join(blockDevDir, "disk", "by-id", "partition"))
			assertPreflightRefusal(t, f, operation, "not whole disks", Options{Layout: LayoutSingle, Disks: []string{alias}, ImageDir: makeRelease(t, "r1", 1, 1, 4)})
		})
	}
}

func TestInstallAndRepairDeduplicateDeviceIdentityBeforeWriting(t *testing.T) {
	for _, operation := range []string{"install", "repair"} {
		for _, kind := range []string{"block alias", "block alternate node", "file symlink", "file hardlink"} {
			t.Run(operation+"/"+kind, func(t *testing.T) {
				f := preflightFixture(t)
				var disk, alias string
				if strings.HasPrefix(kind, "block") {
					disk, _ = f.node("sda", 8, 0, "", LayoutRAID1)
					alias = filepath.Join(blockDevDir, "disk", "by-id", "same-disk")
					if kind == "block alias" {
						f.link(disk, alias)
					} else {
						// A second node with the same rdev is the same disk even without a symlink.
						f.mkdir(filepath.Dir(alias))
						f.write(alias, "")
						file, err := os.OpenFile(alias, os.O_RDWR, 0)
						if err != nil {
							t.Fatal(err)
						}
						if err := file.Truncate(testDisk); err != nil {
							t.Fatal(err)
						}
						plan, err := Plan(testDisk, LayoutRAID1)
						if err != nil {
							t.Fatal(err)
						}
						d := &Disk{Path: alias, File: file, Size: testDisk, SectorSize: 512}
						if err := d.WriteTable(tableFor(plan, 512)); err != nil {
							t.Fatal(err)
						}
						file.Close()
						f.devices[alias] = unix.Mkdev(8, 0)
					}
				} else {
					disk = sparseDisk(t, testDisk)
					d, err := OpenDisk(disk, true)
					if err != nil {
						t.Fatal(err)
					}
					plan, err := Plan(testDisk, LayoutRAID1)
					if err != nil {
						t.Fatal(err)
					}
					if err := d.WriteTable(tableFor(plan, 512)); err != nil {
						t.Fatal(err)
					}
					d.Close()
					alias = filepath.Join(f.root, "same-file")
					if kind == "file symlink" {
						f.link(disk, alias)
					} else if err := os.Link(disk, alias); err != nil {
						t.Fatal(err)
					}
				}
				assertPreflightRefusal(t, f, operation, "listed twice", Options{Layout: LayoutRAID1, Disks: []string{disk, alias}, ImageDir: makeRelease(t, "r1", 1, 1, 4)})
			})
		}
	}
}

func TestOfflineDiskAliasesUseKernelPartitionNodes(t *testing.T) {
	for _, name := range []string{"sda", "nvme0n1", "mmcblk0", "dm-0"} {
		t.Run(name, func(t *testing.T) {
			f := preflightFixture(t)
			disk, sys := f.node(name, 240, 0, "", LayoutSingle)
			f.node(NodeName(name, 1), 240, 1, sys, LayoutSingle)
			alias := f.link(disk, filepath.Join(blockDevDir, "disk", "by-id", "offline-selected"))
			rec := &recorder{}
			w := &Writer{Runner: rec}
			_, disks, err := w.open(Options{Layout: LayoutSingle, Disks: []string{alias}, ImageDir: makeRelease(t, "r1", 1, 1, 4)})
			if err != nil {
				t.Fatal(err)
			}
			defer closeAll(disks)
			if disks[0].Path != disk {
				t.Fatalf("resolved disk = %s, want %s", disks[0].Path, disk)
			}
			if err := w.makeData(context.Background(), disks, LayoutSingle); err != nil {
				t.Fatal(err)
			}
			if rec.index("mkfs.ext4 -F -q -L nex-data "+NodeName(disk, DataIndex)) < 0 {
				t.Fatalf("partition command uses an alias instead of a kernel node: %v", rec.calls)
			}
			if f.writes != 0 {
				t.Fatal("opening the offline disk wrote bytes")
			}
		})
	}
}

func TestInstallAndRepairCheckReleaseCapacityBeforeWriting(t *testing.T) {
	for _, operation := range []string{"install", "repair"} {
		for _, artifact := range []string{"root.erofs", "root.verity"} {
			t.Run(operation+"/"+artifact, func(t *testing.T) {
				f := preflightFixture(t)
				disk, _ := f.node("sda", 8, 0, "", LayoutSingle)
				oldRead := installRelease
				// Model the verified metadata of an oversized release without creating and hashing
				// multi-GiB artifacts. All disk/runner behavior remains the real consumer flow.
				installRelease = func(dir string) (*Release, error) {
					r, err := ReadRelease(dir)
					if err != nil {
						return nil, err
					}
					entry := r.Manifest.Files[artifact]
					entry.Size = SlotSize + 1
					if artifact == "root.verity" {
						entry.Size = VeritySize + 1
					}
					r.Manifest.Files[artifact] = entry
					return r, nil
				}
				t.Cleanup(func() { installRelease = oldRead })
				assertPreflightRefusal(t, f, operation, "does not fit slot a", Options{Layout: LayoutSingle, Disks: []string{disk}, ImageDir: makeRelease(t, "r1", 1, 1, 4)})
			})
		}
	}
}

func TestInstallAndRepairValidateAllDisksBeforeWriting(t *testing.T) {
	for _, operation := range []string{"install", "repair"} {
		t.Run(operation, func(t *testing.T) {
			f := preflightFixture(t)
			first, _ := f.node("sda", 8, 0, "", LayoutRAID1)
			second, sys := f.node("sdb", 8, 16, "", LayoutRAID1)
			f.node("sdb6", 8, 22, sys, LayoutRAID1)
			f.write(mountInfoPath, "24 1 8:22 / /var rw - ext4 /dev/disk/by-uuid/fixture rw\n")
			assertPreflightRefusal(t, f, operation, "in use", Options{
				Layout: LayoutRAID1, Disks: []string{first, second}, ImageDir: makeRelease(t, "r1", 1, 1, 4),
			})
		})
	}
}

func TestInstallPreflightAcceptsReleaseAtSlotCapacity(t *testing.T) {
	f := preflightFixture(t)
	disk, _ := f.node("sda", 8, 0, "", LayoutSingle)
	oldRead := installRelease
	installRelease = func(dir string) (*Release, error) {
		r, err := ReadRelease(dir)
		if err != nil {
			return nil, err
		}
		for name, size := range map[string]int64{"root.erofs": SlotSize, "root.verity": VeritySize} {
			entry := r.Manifest.Files[name]
			entry.Size = size
			r.Manifest.Files[name] = entry
		}
		return r, nil
	}
	t.Cleanup(func() { installRelease = oldRead })
	_, disks, err := (&Writer{Runner: &recorder{}}).open(Options{
		Layout: LayoutSingle, Disks: []string{disk}, ImageDir: makeRelease(t, "r1", 1, 1, 4),
	})
	if err != nil {
		t.Fatal(err)
	}
	closeAll(disks)
	if f.writes != 0 {
		t.Fatal("capacity validation wrote bytes")
	}
}
