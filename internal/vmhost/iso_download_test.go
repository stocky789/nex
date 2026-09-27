// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nex.local/nex/internal/protocol"
)

// Only the socket destination is substituted; real HTTP, redirect handling,
// bounded streaming, validation, atomic publication and journal recovery run.
func testISOClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client := publicISOClient()
	client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

// testISOTLSClient is testISOClient over real TLS, so an HTTPS source and the
// client's own redirect policy run exactly as they do against a public mirror.
// The standard test certificate covers example.com and its subdomains.
func testISOTLSClient(t *testing.T, handler http.HandlerFunc) *http.Client {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	client := publicISOClient()
	transport := client.Transport.(*http.Transport)
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	t.Cleanup(client.CloseIdleConnections)
	return client
}

// A download link can be signed, so its text is a credential: no failure may echo it.
func assertNoURLText(t *testing.T, err error, source string) {
	t.Helper()
	if err == nil {
		t.Fatal("download did not fail")
	}
	u, parseErr := url.Parse(source)
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	for _, secret := range []string{source, u.Host, u.Path, u.RawQuery, "://"} {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error echoed URL text %q: %v", secret, err)
		}
	}
}

// A refusal Core decided must not be reported as a network fault: the operator can act
// on "this server redirects HTTPS to plain HTTP" and cannot act on "connection, redirect
// or timeout". Every branch is still checked for URL text.
func TestPublicISODownloadReportsPolicyRefusalsDistinctly(t *testing.T) {
	data, _ := managedTestISO()
	const source = "https://iso.example.com/secret-path/disc.iso?token=private-token"
	const plain = "http://iso.example.org/secret-path/disc.iso?token=private-token"
	generic := "ISO download failed: connection, redirect or timeout"
	for _, tc := range []struct {
		name    string
		source  string
		want    string
		client  func(t *testing.T) *http.Client
		limit   int64
		partial bool
	}{
		// The official virtio-win "stable" link behaves exactly like this.
		{name: "https-downgrade-redirect", source: source, want: "ISO download refused this server's redirect from HTTPS to plain HTTP; use a direct HTTPS link to the ISO", client: func(t *testing.T) *http.Client {
			return testISOTLSClient(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://iso.example.com/secret-path/disc.iso?token=private-token", 302)
			})
		}},
		{name: "redirect-loop", source: plain, want: "ISO download exceeded five redirects", client: func(t *testing.T) *http.Client {
			return testISOClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain, 302) })
		}},
		{name: "redirect-to-private-address", source: plain, want: "ISO download refused the redirect target: ISO URL must use a public address", client: func(t *testing.T) *http.Client {
			return testISOClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.1/private", 302) })
		}},
		{name: "redirect-with-credentials", source: plain, want: "ISO download refused the redirect target: ISO URL must be a public HTTP or HTTPS URL without credentials or a fragment, at most 4096 characters", client: func(t *testing.T) *http.Client {
			return testISOClient(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://user:secret@iso.example.org/private", 302)
			})
		}},
		// The real dialer decides this one, so the wrapped dial error must survive too.
		{name: "non-public-destination", source: "http://localhost/secret-path/disc.iso?token=private-token", want: "ISO download destination is not public", client: func(t *testing.T) *http.Client { return publicISOClient() }},
		{name: "rejected-source-url", source: "http://127.0.0.1/secret-path/disc.iso?token=private-token", want: "ISO URL must use a public address", client: func(t *testing.T) *http.Client { return publicISOClient() }},
		{name: "server-error", source: plain, want: "ISO server returned HTTP 503", client: func(t *testing.T) *http.Client {
			return testISOClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) })
		}},
		{name: "declared-oversize", source: plain, want: "ISO exceeds available storage including reserve", client: func(t *testing.T) *http.Client {
			return testISOClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "999999")
				w.WriteHeader(200)
			})
		}, limit: 1000},
		// A genuine transport fault stays generic: it has no reason to report.
		{name: "connection-closed", source: plain, want: generic, client: func(t *testing.T) *http.Client {
			client := testISOClient(t, func(w http.ResponseWriter, r *http.Request) {})
			client.Transport.(*http.Transport).DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				return nil, errors.New("dial tcp " + plain + ": connection refused")
			}
			return client
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			if limit == 0 {
				limit = int64(len(data))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			body, err := fetchPublicISO(ctx, tc.client(t), tc.source, limit)
			if body != nil {
				body.Close()
			}
			assertNoURLText(t, err, tc.source)
			if err.Error() != tc.want {
				t.Fatalf("reason %q, want %q", err.Error(), tc.want)
			}
			if tc.want != generic && strings.Contains(err.Error(), generic) {
				t.Fatalf("policy refusal reported as a network fault: %v", err)
			}
		})
	}
}

func TestURLDownloadPublicationAndRecovery(t *testing.T) {
	data, expected := managedTestISO()
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted-after-publication", true: "completed"}[completed], func(t *testing.T) {
			e := NewManaged(t.TempDir())
			m := expected
			m.URL, m.SizeBytes, m.SHA256 = "https://example.org/disc.iso", 0, ""
			// The test server is plain HTTP.
			m.URL = "http://example.org/disc.iso"
			cmd := protocol.Command{OperationID: "download", Kind: "iso-import", Media: &m}
			j := managedJournal{Command: cmd, Result: protocol.Result{OperationID: "download", Status: "pending"}, Stage: "receiving", MediaPath: filepath.Join(t.TempDir(), "disc.iso")}
			calls := 0
			client := testISOClient(t, func(w http.ResponseWriter, r *http.Request) { calls++; w.Write(data) })
			if err := e.downloadISO(context.Background(), &j, client, int64(len(data))); err != nil {
				t.Fatal(err)
			}
			if completed {
				j.Result.Status = "succeeded"
				if err := e.save(j); err != nil {
					t.Fatal(err)
				}
			}
			reopened := NewManaged(e.stateDir)
			if !completed {
				// A fresh Controller has no original command to replay; heartbeat adoption
				// must promote the completed publication from Core's journal alone.
				applied, _ := reopened.Applied(context.Background())
				if len(applied.Media) != 1 || applied.Media[0].SHA256 != expected.SHA256 {
					t.Fatalf("fresh adoption: %+v", applied)
				}
			}
			result, err := reopened.Execute(context.Background(), cmd, func(context.Context, string) (io.ReadCloser, error) { t.Fatal("replayed transfer"); return nil, nil })
			if err != nil || result.Status != "succeeded" {
				t.Fatalf("%+v %v", result, err)
			}
			js, err := reopened.journals()
			if err != nil {
				t.Fatal(err)
			}
			if js[0].Command.Media.SizeBytes != 0 || js[0].Command.Media.URL != m.URL {
				t.Fatal("command mutated")
			}
			resolved, path, err := managedMedia(js, m.ID)
			if err != nil || resolved.SizeBytes != expected.SizeBytes || resolved.SHA256 != expected.SHA256 || resolved.URL != "" {
				t.Fatalf("metadata: %+v %v", resolved, err)
			}
			if err := verifyISO(path, resolved); err != nil {
				t.Fatal(err)
			}
			applied, _ := reopened.Applied(context.Background())
			if len(applied.Media) != 1 || applied.Media[0].SHA256 != expected.SHA256 {
				t.Fatalf("adoption lost metadata: %+v", applied)
			}
			if calls != 1 {
				t.Fatalf("downloads: %d", calls)
			}
		})
	}
}

func TestURLDownloadReportsLiveBytes(t *testing.T) {
	data, m := managedTestISO()
	m.URL, m.SizeBytes, m.SHA256 = "http://example.org/disc.iso", 0, ""
	for _, known := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown-length", true: "known-length"}[known], func(t *testing.T) {
			e := NewManaged(t.TempDir())
			j := managedJournal{Command: protocol.Command{OperationID: "download", Kind: "iso-import", Media: &m}, Result: protocol.Result{OperationID: "download", Status: "pending"}, Stage: "receiving", MediaPath: filepath.Join(t.TempDir(), "disc.iso")}
			if err := e.save(j); err != nil {
				t.Fatal(err)
			}
			first, resume := make(chan struct{}), make(chan struct{}, 1)
			defer close(resume)
			client := testISOClient(t, func(w http.ResponseWriter, r *http.Request) {
				if known {
					w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				}
				w.Write(data[:4096])
				w.(http.Flusher).Flush()
				close(first)
				<-resume
				w.Write(data[4096:])
			})
			inFlight.Lock()
			inFlight.ids[j.Command.OperationID] = true
			inFlight.Unlock()
			defer func() { inFlight.Lock(); delete(inFlight.ids, j.Command.OperationID); inFlight.Unlock() }()
			done := make(chan error, 1)
			go func() { done <- e.downloadISO(context.Background(), &j, client, int64(len(data))) }()
			<-first
			deadline := time.Now().Add(3 * time.Second)
			for {
				state, _ := e.Applied(context.Background())
				if len(state.ISODownloads) == 1 && state.ISODownloads[0].ReceivedBytes > 0 {
					want := int64(0)
					if known {
						want = int64(len(data))
					}
					if state.ISODownloads[0].TotalBytes != want {
						t.Fatalf("total: %+v", state.ISODownloads[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("missing live progress: %+v", state.ISODownloads)
				}
				time.Sleep(10 * time.Millisecond)
			}
			resume <- struct{}{}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			j.Result.Status = "succeeded"
			if err := e.save(j); err != nil {
				t.Fatal(err)
			}
			state, _ := e.Applied(context.Background())
			if len(state.ISODownloads) != 0 {
				t.Fatalf("terminal progress: %+v", state.ISODownloads)
			}
		})
	}
}

func TestURLDownloadFailuresNeverPublish(t *testing.T) {
	data, m := managedTestISO()
	expected := m
	m.URL, m.SizeBytes, m.SHA256 = "http://example.org/disc.iso", 0, ""
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		limit   int64
	}{
		{"not-ISO", func(w http.ResponseWriter, r *http.Request) { w.Write(bytes.Repeat([]byte{'x'}, len(data))) }, int64(len(data))},
		{"declared-oversize", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "999999")
			w.WriteHeader(200)
		}, int64(len(data))},
		{"chunked-oversize", func(w http.ResponseWriter, r *http.Request) { w.(http.Flusher).Flush(); w.Write(data) }, int64(len(data) - 1)},
		{"not-found", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, int64(len(data))},
		{"private-redirect", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://127.0.0.1/private", 302) }, int64(len(data))},
		{"credential-redirect", func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "http://user:secret@example.org/private", 302)
		}, int64(len(data))},
		{"redirect-loop", func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.org/again", 302) }, int64(len(data))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := NewManaged(t.TempDir())
			j := managedJournal{Command: protocol.Command{OperationID: "download", Kind: "iso-import", Media: &m}, MediaPath: filepath.Join(t.TempDir(), "disc.iso")}
			if err := e.downloadISO(context.Background(), &j, testISOClient(t, tc.handler), tc.limit); err == nil {
				t.Fatal("invalid download succeeded")
			}
			for _, p := range []string{j.MediaPath, j.MediaPath + ".partial"} {
				if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("left download artifact: %s %v", p, err)
				}
			}
		})
	}
	t.Run("cancellation-cleanup", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "disc.iso")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := receiveISOContent(ctx, path, m, bytes.NewReader(data), int64(len(data)), nil); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation: %v", err)
		}
		for _, p := range []string{path, path + ".partial"} {
			if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("cancellation left artifact")
			}
		}
	})
	t.Run("metadata-persistence", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "disc.iso")
		err := receiveISOContent(context.Background(), path, m, bytes.NewReader(data), int64(len(data)), func(protocol.Media) error { return errors.New("journal unavailable") })
		if err == nil {
			t.Fatal("published without durable metadata")
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("published despite journal failure")
		}
	})
	for _, metadataSaved := range []bool{false, true} {
		t.Run("interrupted-before-publication", func(t *testing.T) {
			e := NewManaged(t.TempDir())
			j := managedJournal{Command: protocol.Command{OperationID: "download", Kind: "iso-import", Media: &m}, Result: protocol.Result{OperationID: "download", Status: "pending"}, Stage: "receiving", MediaPath: filepath.Join(t.TempDir(), "disc.iso")}
			if metadataSaved {
				j.DownloadedMedia = &expected
			}
			if err := e.save(j); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(j.MediaPath+".partial", data, 0600); err != nil {
				t.Fatal(err)
			}
			applied, _ := e.Applied(context.Background())
			if len(applied.Media) != 0 || len(applied.ISODownloads) != 0 || len(applied.Outcomes) != 1 || applied.Outcomes[0].Status != "needs_attention" {
				t.Fatalf("incomplete adoption: %+v", applied)
			}
			if _, err := os.Stat(j.MediaPath + ".partial"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("adoption left interrupted partial")
			}
			r, err := e.Execute(context.Background(), j.Command, nil)
			if err != nil || r.Status != "needs_attention" {
				t.Fatalf("%+v %v", r, err)
			}
			if _, err := os.Stat(j.MediaPath + ".partial"); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("partial not removed")
			}
		})
	}
}

func TestPublicISOTransportRestrictions(t *testing.T) {
	client := publicISOClient()
	if client.Transport.(*http.Transport).Proxy != nil || client.Timeout != 2*time.Hour {
		t.Fatal("unsafe transport defaults")
	}
	for _, address := range []string{"127.0.0.1:80", "[::1]:80", "169.254.169.254:80", "localhost:80"} {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		conn, err := dialPublicISO(ctx, "tcp", address)
		cancel()
		if conn != nil {
			conn.Close()
		}
		if err == nil {
			t.Fatalf("non-public dial allowed: %s", address)
		}
	}
	c := testISOClient(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.org/next", 302) })
	c.Timeout = time.Nanosecond
	_, err := fetchPublicISO(context.Background(), c, "http://example.org/disc.iso?token=private-token", 100000)
	if err == nil || strings.Contains(err.Error(), "private-token") || strings.Contains(err.Error(), "example.org") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestPublicISORedirectSuccessAndDowngrade(t *testing.T) {
	data, _ := managedTestISO()
	calls := 0
	client := testISOClient(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "http://example.org/disc.iso", 302)
			return
		}
		if r.Referer() != "" {
			t.Error("redirect leaked source URL")
		}
		w.Write(data)
	})
	body, err := fetchPublicISO(context.Background(), client, "http://example.org/start?token=example", int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil || !bytes.Equal(got, data) || calls != 2 {
		t.Fatalf("redirect did not return ISO: calls=%d error=%v", calls, err)
	}
	next, _ := http.NewRequest(http.MethodGet, "http://example.org/disc.iso", nil)
	previous, _ := http.NewRequest(http.MethodGet, "https://example.org/disc.iso", nil)
	if publicISOClient().CheckRedirect(next, []*http.Request{previous}) == nil {
		t.Fatal("HTTPS downgrade accepted")
	}
}
