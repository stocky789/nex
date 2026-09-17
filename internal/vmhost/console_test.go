// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestConsoleSocketRequiresRunningLocalVNCSocket(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "vnc.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	fakeCommand(t, "virsh", `case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; dumpxml) printf '%s\n' "$VMHOST_XML";; *) exit 2;; esac`)
	id := "11111111-2222-4333-a444-555555555555"
	local := `<domain type='kvm'><name>g</name><uuid>` + id + `</uuid><devices><graphics type='vnc' autoport='no'><listen type='socket' socket='` + socket + `'/></graphics></devices></domain>`
	e := &Engine{config: Config{VirshURI: "test:///default"}}
	for _, tc := range []struct{ name, state, xml, want string }{
		{"running local socket", "running", local, ""},
		{"stopped", "shut off", local, "not running"},
		{"asleep", "pmsuspended", local, "asleep; use Start to wake it"},
		{"network display", "running", strings.Replace(local, "type='socket' socket='"+socket+"'", "type='address' address='0.0.0.0'", 1), "not a local VNC socket"},
		{"missing socket file", "running", strings.Replace(local, socket, socket+".gone", 1), "unavailable"},
		{"no display", "running", `<domain><devices/></domain>`, "no single console display"},
	} {
		t.Setenv("VMHOST_STATE", tc.state)
		t.Setenv("VMHOST_XML", tc.xml)
		got, err := e.ConsoleSocket(context.Background(), id)
		if tc.want == "" {
			if err != nil || got != socket {
				t.Fatalf("%s: %v %q", tc.name, err, got)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: expected %q, got %v (%q)", tc.name, tc.want, err, got)
		}
	}
	if _, err := e.ConsoleSocket(context.Background(), "../etc"); err == nil {
		t.Fatal("invalid ID accepted")
	}
}
