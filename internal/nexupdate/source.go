// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Source is where release metadata and blobs come from: the update server, a mounted bundle or the
// pool master's peer cache. Every byte it returns is untrusted; the engine authenticates it.
type Source interface {
	// Fetch returns a small metadata file, refusing more than limit bytes.
	Fetch(ctx context.Context, name string, limit int64) ([]byte, error)
	// Blob opens a blob at offset. ranged reports whether the source honoured the offset: when it did
	// not, the reader starts at byte 0. validator is the ETag to send as If-Range on a later resume.
	Blob(ctx context.Context, name string, offset int64, validator string) (body io.ReadCloser, ranged bool, newValidator string, err error)
}

const maxMetadata = 1 << 20

func unreachable(err error) *Error {
	return refuse(503, "source_unreachable", "the update source cannot be reached")
}

// HTTPSource serves a base URL. It carries no private-network rewrite: signatures give integrity, so
// plain HTTP to a mirror is acceptable.
type HTTPSource struct {
	Base   string
	Client *http.Client
}

func (s HTTPSource) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	return &http.Client{Timeout: 0}
}

func (s HTTPSource) url(name string) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.Base, "/") + "/")
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return "", refuse(503, "source_unconfigured", "the update base URL is not a valid http or https URL")
	}
	ref, err := url.Parse(name)
	if err != nil {
		return "", refuse(502, "metadata_invalid", "an update URL is malformed")
	}
	return base.ResolveReference(ref).String(), nil
}

func (s HTTPSource) Fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	u, err := s.url(name)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, refuse(502, "metadata_missing", "the update source does not have the requested metadata")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, unreachable(nil)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, unreachable(err)
	}
	if int64(len(b)) > limit {
		return nil, refuse(502, "metadata_invalid", "metadata is larger than allowed")
	}
	return b, nil
}

func (s HTTPSource) Blob(ctx context.Context, name string, offset int64, validator string) (io.ReadCloser, bool, string, error) {
	u, err := s.url(name)
	if err != nil {
		return nil, false, "", err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if offset > 0 {
		req.Header.Set("Range", "bytes="+strconv.FormatInt(offset, 10)+"-")
		if validator != "" {
			req.Header.Set("If-Range", validator)
		}
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return nil, false, "", unreachable(err)
	}
	etag := resp.Header.Get("ETag")
	switch {
	case resp.StatusCode == http.StatusPartialContent && offset > 0 && strings.HasPrefix(resp.Header.Get("Content-Range"), "bytes "+strconv.FormatInt(offset, 10)+"-"):
		return resp.Body, true, etag, nil
	case resp.StatusCode == http.StatusOK:
		// No Range support, or the validator no longer matches: the whole file follows.
		return resp.Body, false, etag, nil
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, false, "", refuse(502, "metadata_missing", "the update source does not have the requested blob")
	}
	return nil, false, "", unreachable(nil)
}

// DirSource serves a directory in the online layout: a mounted NEXUPDATE bundle, an inbox or a test.
type DirSource struct{ Root string }

// openRegular opens name inside root for reading. It never follows a link out of root and never blocks:
// a FIFO on the media would otherwise hold the update lock until someone wrote to it. Anything that is
// not a regular file is refused.
func openRegular(root *os.Root, name string) (*os.File, error) {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() {
		f.Close()
		return nil, errors.New("not a regular file")
	}
	return f, nil
}

func (s DirSource) open(name string) (*os.File, error) {
	if strings.Contains(name, "://") {
		return nil, refuse(502, "metadata_invalid", "an update URL is not relative to the bundle")
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return nil, unreachable(err)
	}
	defer root.Close()
	f, err := openRegular(root, strings.TrimPrefix(path.Clean("/"+name), "/"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil, refuse(502, "metadata_missing", "the update source does not have the requested file")
	case err != nil:
		return nil, refuse(502, "metadata_invalid", "the update source file is not a regular file inside the bundle")
	}
	return f, nil
}

func (s DirSource) Fetch(_ context.Context, name string, limit int64) ([]byte, error) {
	f, err := s.open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, refuse(502, "metadata_invalid", "metadata is unreadable or larger than allowed")
	}
	return b, nil
}

func (s DirSource) Blob(_ context.Context, name string, offset int64, _ string) (io.ReadCloser, bool, string, error) {
	f, err := s.open(name)
	if err != nil {
		return nil, false, "", err
	}
	if offset > 0 {
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			f.Close()
			return nil, false, "", unreachable(err)
		}
	}
	return f, offset > 0, "", nil
}

// progressFn reports bytes done of total.
type progressFn func(done, total int64)

// fetchBlob downloads a blob into dir/<sha256> and returns that path, resuming a .part file: the part
// is re-hashed and continued at its length; a source that ignores Range restarts the file; the final
// hash and size must match or the part is discarded (a resumed part gets one clean retry first, so a
// corrupt prefix does not strand the update). A finished blob that still hashes right is reused.
func fetchBlob(ctx context.Context, src Source, name string, c Component, dir string, progress progressFn) (string, error) {
	final := filepath.Join(dir, c.SHA256)
	if sum, err := fileSHA256(final); err == nil && sum == c.SHA256 {
		return final, nil
	}
	_ = os.Remove(final)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	part := final + ".part"
	for attempt := 0; ; attempt++ {
		resumed, err := fetchPart(ctx, src, name, c, part, progress)
		if err == nil {
			if err = os.Rename(part, final); err != nil {
				return "", err
			}
			if d, derr := os.Open(dir); derr == nil {
				_ = d.Sync()
				d.Close()
			}
			return final, nil
		}
		var re *Error
		if errors.As(err, &re) && re.Code == "blob_mismatch" {
			_ = os.Remove(part)
			_ = os.Remove(part + ".etag")
			if resumed && attempt == 0 {
				continue
			}
		}
		return "", err
	}
}

func fetchPart(ctx context.Context, src Source, name string, c Component, part string, progress progressFn) (resumed bool, err error) {
	h := sha256.New()
	f, err := os.OpenFile(part, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return false, err
	}
	defer f.Close()
	have, err := io.Copy(h, io.LimitReader(f, c.Bytes+1))
	if err != nil {
		return false, err
	}
	if have > c.Bytes {
		have = 0
		h.Reset()
	}
	etagBytes, _ := os.ReadFile(part + ".etag")
	if have < c.Bytes {
		body, ranged, etag, err := src.Blob(ctx, name, have, strings.TrimSpace(string(etagBytes)))
		if err != nil {
			return false, err
		}
		defer body.Close()
		if !ranged {
			have = 0
			h.Reset()
		}
		resumed = have > 0
		if etag != "" {
			_ = os.WriteFile(part+".etag", []byte(etag), 0o600)
		}
		if err = f.Truncate(have); err != nil {
			return resumed, err
		}
		if _, err = f.Seek(have, io.SeekStart); err != nil {
			return resumed, err
		}
		// One byte past the announced size is read so an over-long blob is a mismatch, not a truncation.
		buf := make([]byte, 1<<20)
		limit := c.Bytes - have + 1
		for limit > 0 {
			if err = ctx.Err(); err != nil {
				return resumed, err
			}
			n, rerr := body.Read(buf[:min(int64(len(buf)), limit)])
			if n > 0 {
				if _, err = f.Write(buf[:n]); err != nil {
					return resumed, err
				}
				h.Write(buf[:n])
				have += int64(n)
				limit -= int64(n)
				if progress != nil {
					progress(min(have, c.Bytes), c.Bytes)
				}
			}
			if rerr == io.EOF {
				break
			}
			if rerr != nil {
				_ = f.Sync()
				return resumed, unreachable(rerr)
			}
		}
		if err = f.Sync(); err != nil {
			return resumed, err
		}
	}
	if have != c.Bytes {
		if have < c.Bytes {
			// Connection ended early: keep the part and report it, so the next attempt continues.
			return resumed, unreachable(nil)
		}
		return resumed, refuse(502, "blob_mismatch", "a downloaded blob does not match its manifest")
	}
	if hex.EncodeToString(h.Sum(nil)) != c.SHA256 {
		return resumed, refuse(502, "blob_mismatch", "a downloaded blob does not match its manifest")
	}
	_ = os.Remove(part + ".etag")
	return resumed, nil
}

func fileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// PeerFirstSource reads blobs from a pool peer's cache when it has them and falls back to the
// origin otherwise. Metadata always comes from the origin, and the caller still verifies every
// blob against the signed manifest, so the peer is trusted for nothing but bytes it may not have.
type PeerFirstSource struct{ Peer, Origin Source }

func (s PeerFirstSource) Fetch(ctx context.Context, name string, limit int64) ([]byte, error) {
	return s.Origin.Fetch(ctx, name, limit)
}

func (s PeerFirstSource) Blob(ctx context.Context, name string, offset int64, validator string) (io.ReadCloser, bool, string, error) {
	if body, ranged, newValidator, err := s.Peer.Blob(ctx, name, offset, validator); err == nil {
		return body, ranged, newValidator, nil
	}
	return s.Origin.Blob(ctx, name, offset, validator)
}
