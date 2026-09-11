// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"nex.local/nex/internal/protocol"
)

// Resolve and validate every address before dialing a numeric address: neither
// DNS rebinding, redirects nor environment proxies can reach a private service.
func dialPublicISO(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("invalid ISO download address")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("ISO download DNS lookup failed")
	}
	for _, ip := range addresses {
		if !protocol.PublicISOAddress(ip) {
			return nil, errors.New("ISO download destination is not public")
		}
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	for _, ip := range addresses {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("ISO download connection failed")
}

func publicISOClient() *http.Client {
	return &http.Client{
		Timeout:   2 * time.Hour,
		Transport: &http.Transport{DialContext: dialPublicISO, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > 5 {
				return errors.New("ISO download exceeded five redirects")
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return errors.New("ISO download refused HTTPS downgrade")
			}
			return protocol.ValidateISOURL(req.URL.String())
		},
	}
}

func fetchPublicISO(ctx context.Context, client *http.Client, source string, limit int64) (io.ReadCloser, error) {
	if err := protocol.ValidateISOURL(source); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return nil, errors.New("invalid ISO download request")
	}
	resp, err := client.Do(req)
	if err != nil {
		// Transport and redirect errors may include signed URLs; report no URL text.
		return nil, errors.New("ISO download failed: connection, redirect or timeout")
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("ISO server returned HTTP %d", resp.StatusCode)
	}
	if resp.ContentLength > limit {
		resp.Body.Close()
		return nil, errors.New("ISO exceeds available storage including reserve")
	}
	return resp.Body, nil
}

func (e *ManagedEngine) downloadISO(ctx context.Context, j *managedJournal, client *http.Client, limit int64) error {
	body, err := fetchPublicISO(ctx, client, j.Command.Media.URL, limit)
	if err != nil {
		return err
	}
	defer body.Close()
	return receiveISOContent(ctx, j.MediaPath, *j.Command.Media, body, limit, func(resolved protocol.Media) error {
		j.DownloadedMedia = &resolved
		return e.save(*j)
	})
}
