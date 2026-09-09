// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestObserveGuest(t *testing.T) {
	const legacy = `<domain><devices><channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='disconnected'/></channel></devices></domain>`
	const full = `<domain><devices>
	<channel type='unix'><target type='virtio' name='org.qemu.guest_agent.0' state='connected'/></channel>
	<channel type='qemu-vdagent'><source><clipboard copypaste='yes'/></source><target type='virtio' name='com.redhat.spice.0' state='connected'/></channel>
	<video><model type='virtio'/></video></devices></domain>`
	fakeCommand(t, "virsh", `case "$3" in
dumpxml) printf '%s\n' "$GUEST_XML";;
qemu-agent-command) [ "$4" = --timeout ] || exit 3; [ -n "$GUEST_FAIL" ] && exit 1; printf '%s\n' "$5 $7" >> "$GUEST_LOG"
  case "$7" in
  *osinfo*) echo '{"return":{"pretty-name":"Debian GNU/Linux 13 (trixie)","name":"Debian GNU/Linux","version":"13"}}';;
  *host-name*) printf '{"return":{"host-name":"guest1%s"}}\n' "$GUEST_PAD";;
  *interfaces*) echo '{"return":[{"name":"lo","ip-addresses":[{"ip-address":"127.0.0.1","prefix":8}]},{"name":"enp1s0","ip-addresses":[{"ip-address":"192.168.122.10","prefix":24},{"ip-address":"fe80::1","prefix":64},{"ip-address":"2001:db8::10","prefix":64},{"ip-address":"bad","prefix":1}]}]}';;
  esac;;
*) exit 2;; esac`)
	log := t.TempDir() + "/log"
	t.Setenv("GUEST_LOG", log)
	id := "12345678-1234-4234-8234-123456789012"
	cases := []struct {
		name, xml, fail string
		facts           bool
		want            *protocol.GuestInfo
	}{
		{"legacy definition", legacy, "", true, &protocol.GuestInfo{Agent: "disconnected", Clipboard: "no-channel"}},
		{"channels waiting", strings.ReplaceAll(full, "state='connected'", "state='disconnected'"), "", true, &protocol.GuestInfo{Agent: "disconnected", Clipboard: "waiting", Resize: true}},
		{"attached without facts", full, "", false, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true}},
		{"agent not answering", full, "1", true, &protocol.GuestInfo{Agent: "unresponsive", Clipboard: "ready", Resize: true}},
		{"agent facts", full, "", true, &protocol.GuestInfo{Agent: "connected", OS: "Debian GNU/Linux 13 (trixie)", Hostname: "guest1", Clipboard: "ready", Resize: true,
			Addresses: []protocol.GuestAddress{{Interface: "enp1s0", Address: "192.168.122.10/24"}, {Interface: "enp1s0", Address: "2001:db8::10/64"}}}},
	}
	for _, c := range cases {
		t.Setenv("GUEST_XML", c.xml)
		t.Setenv("GUEST_FAIL", c.fail)
		got, err := observeGuest(context.Background(), managedVirsh, id)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if c.facts {
			guestFacts(context.Background(), managedVirsh, id, got)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %+v want %+v", c.name, got, c.want)
		}
	}
	if _, err := observeGuest(context.Background(), managedVirsh, "../etc"); err == nil {
		t.Fatal("invalid ID accepted")
	}
	t.Setenv("GUEST_XML", "<broken")
	if _, err := observeGuest(context.Background(), managedVirsh, id); err == nil {
		t.Fatal("invalid XML accepted")
	}
	// Guest strings are bounded before they leave the host.
	t.Setenv("GUEST_XML", full)
	t.Setenv("GUEST_PAD", strings.Repeat("x", 300))
	g, _ := observeGuest(context.Background(), managedVirsh, id)
	guestFacts(context.Background(), managedVirsh, id, g)
	if g.Agent != "connected" || len(g.Hostname) != guestHostnameLimit {
		t.Fatalf("hostname not bounded: %d", len(g.Hostname))
	}
}
