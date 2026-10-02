// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// controllerProbe is the readiness check of a swapped (or restored) Controller: the request deployment
// itself uses, an HTTPS GET of its UI at the management URL, verified against the deployment's pinned
// certificate. Core reads only the Controller's answer; it keeps nothing of the Controller's.
func (s *Service) controllerProbe(ctx context.Context, url string) error {
	s.mu.Lock()
	var trust, serverName string
	for _, d := range s.operations {
		if d.Operation.Status == "ready" && d.Certificate != "" {
			trust, serverName = d.Certificate, controllerServerName(d.Request)
		}
	}
	s.mu.Unlock()
	pool := x509.NewCertPool()
	if trust == "" || !pool.AppendCertsFromPEM([]byte(trust)) {
		return errors.New("this host has no deployed Controller identity to verify against")
	}
	client := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}}}
	defer client.CloseIdleConnections()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the Controller answered %d", resp.StatusCode)
	}
	return nil
}
