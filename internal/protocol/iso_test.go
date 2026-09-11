// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"net/netip"
	"testing"
)

func TestValidateISOURL(t *testing.T) {
	for _, raw := range []string{"https://example.org/disc.iso?token=example", "http://8.8.8.8/disc.iso", "https://[2606:4700:4700::1111]/disc.iso"} {
		if err := ValidateISOURL(raw); err != nil {
			t.Errorf("valid URL rejected: %v", err)
		}
	}
	for _, raw := range []string{"", "file:///disc.iso", "ftp://example.org/disc.iso", "https://user:secret@example.org/disc.iso", "https://example.org/disc.iso#fragment", "http://127.0.0.1/disc.iso", "http://[::ffff:127.0.0.1]/disc.iso", "http://[fe80::1%25eth0]/disc.iso", "https://example.org:65536/disc.iso"} {
		if ValidateISOURL(raw) == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	for _, addr := range []string{"0.0.0.0", "10.1.2.3", "100.100.100.200", "127.0.0.1", "169.254.169.254", "172.16.1.1", "192.168.1.1", "192.0.0.8", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "::", "::1", "::ffff:10.0.0.1", "fc00::1", "fe80::1", "ff00::1", "64:ff9b::7f00:1", "2001::1", "2001:db8::1", "2002:7f00:1::1", "3fff::1"} {
		if PublicISOAddress(netip.MustParseAddr(addr)) {
			t.Errorf("non-public IP accepted: %s", addr)
		}
	}
}
