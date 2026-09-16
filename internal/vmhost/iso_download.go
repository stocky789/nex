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

// isoPolicyError is a refusal Core decided, not a fault it observed: an unsafe
// destination, an HTTPS downgrade or too many redirects. The HTTP client wraps
// dial and redirect errors in a *url.Error whose text repeats the request URL,
// and ISO URLs can be signed, so only this reason is ever reported to callers.
type isoPolicyError struct{ reason string }

func (e isoPolicyError) Error() string { return e.reason }

func isoPolicy(reason string) error { return isoPolicyError{reason: reason} }

// Resolve and validate every address before dialing a numeric address: neither
// DNS rebinding, redirects nor environment proxies can reach a private service.
func dialPublicISO(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, isoPolicy("invalid ISO download address")
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, isoPolicy("ISO download DNS lookup failed")
	}
	for _, ip := range addresses {
		if !protocol.PublicISOAddress(ip) {
			return nil, isoPolicy("ISO download destination is not public")
		}
	}
	d := net.Dialer{Timeout: 15 * time.Second}
	for _, ip := range addresses {
		conn, err := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, isoPolicy("ISO download connection failed")
}

func publicISOClient() *http.Client {
	return &http.Client{
		Timeout:   2 * time.Hour,
		Transport: &http.Transport{DialContext: dialPublicISO, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 15 * time.Second, DisableCompression: true},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			req.Header.Del("Referer")
			if len(via) > 5 {
				return isoPolicy("ISO download exceeded five redirects")
			}
			if via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return isoPolicy("ISO download refused this server's redirect from HTTPS to plain HTTP; use a direct HTTPS link to the ISO")
			}
			if err := protocol.ValidateISOURL(req.URL.String()); err != nil {
				return isoPolicy("ISO download refused the redirect target: " + err.Error())
			}
			return nil
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
		// A refusal Core decided is not a network fault, so report that reason itself:
		// an operator cannot act on "connection, redirect or timeout" when the real
		// answer is that the server redirected an HTTPS link to plain HTTP.
		var policy isoPolicyError
		if errors.As(err, &policy) {
			return nil, policy
		}
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
