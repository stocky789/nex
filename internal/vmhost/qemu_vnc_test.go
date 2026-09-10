// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQEMUVNCLossyWrapperAppendsLossy(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "qemu-vnc-lossy")
	fake := filepath.Join(dir, "qemu-system-x86_64")
	out := filepath.Join(dir, "args.txt")
	if err := os.WriteFile(fake, []byte("#!/bin/bash\nprintf '%s\\n' \"$@\" >\""+out+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(qemuVNCLossyScript, "qemu=/usr/bin/qemu-system-x86_64", "qemu="+fake, 1)
	if err := os.WriteFile(wrapper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(wrapper, "-display", "none", "-vnc", "unix:"+filepath.Join(dir, "vnc.sock")+",audiodev=audio0", "-name", "t")
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	found := false
	for i, line := range lines {
		if line == "-vnc" && i+1 < len(lines) {
			if !strings.Contains(lines[i+1], "lossy=on") {
				t.Fatalf("lossy not appended: %q", lines[i+1])
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("no -vnc in args: %s", b)
	}
}

func TestQEMUVNCLossyWrapperSkipsExistingLossy(t *testing.T) {
	dir := t.TempDir()
	wrapper := filepath.Join(dir, "qemu-vnc-lossy")
	fake := filepath.Join(dir, "qemu-system-x86_64")
	out := filepath.Join(dir, "args.txt")
	if err := os.WriteFile(fake, []byte("#!/bin/bash\nprintf '%s\\n' \"$@\" >\""+out+"\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	script := strings.Replace(qemuVNCLossyScript, "qemu=/usr/bin/qemu-system-x86_64", "qemu="+fake, 1)
	if err := os.WriteFile(wrapper, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	vnc := "unix:" + filepath.Join(dir, "vnc.sock") + ",lossy=on"
	if err := exec.Command(wrapper, "-vnc", vnc).Run(); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(b), "lossy=on") != 1 {
		t.Fatalf("duplicated lossy: %s", b)
	}
}

func TestQEMUEmulatorInstallsWrapper(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qemu-vnc-lossy")
	old := qemuVNCLossyPath
	qemuVNCLossyPath = path
	defer func() { qemuVNCLossyPath = old }()
	got := qemuEmulator()
	if got != path {
		t.Fatalf("got %q", got)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != qemuVNCLossyScript {
		t.Fatalf("wrapper not installed: %v %q", err, b)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode()&0111 == 0 {
		t.Fatalf("wrapper not executable: %v %v", err, info.Mode())
	}
}
