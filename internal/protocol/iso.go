// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import (
	"errors"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
)

const ISODownloadCapability = "iso-download-v1"

var nonPublicISOAddresses = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"), netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

// PublicISOAddress excludes local, special-use and transition addresses. IPv6
// must be in the currently allocated global unicast range, excluding tunnels.
func PublicISOAddress(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || (ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip)) {
		return false
	}
	for _, prefix := range nonPublicISOAddresses {
		if prefix.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateISOURL validates syntax and literal addresses; Core also checks every
// DNS result at dial time and connects to that exact address.
func ValidateISOURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" || strings.ContainsAny(u.Hostname(), "%\\") {
		return errors.New("ISO URL must be a public HTTP or HTTPS URL without credentials or a fragment, at most 4096 characters")
	}
	if u.Port() != "" {
		p, err := strconv.Atoi(u.Port())
		if err != nil || p < 1 || p > 65535 {
			return errors.New("invalid ISO URL port")
		}
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && !PublicISOAddress(ip) {
		return errors.New("ISO URL must use a public address")
	}
	return nil
}
