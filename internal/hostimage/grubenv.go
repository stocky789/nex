// SPDX-License-Identifier: AGPL-3.0-only
package hostimage

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// grubEnv is a GRUB environment block: the file GRUB's load_env and save_env read and write. It is exactly
// grubEnvSize bytes (save_env rewrites the sectors of the file in place and cannot grow it), starts with a header
// line, holds name=value lines and is padded with '#'.
type grubEnv map[string]string

const (
	grubEnvSize   = 1024
	grubEnvHeader = "# GRUB Environment Block\n"
)

// parseGrubEnv decodes an environment block. Values are plain text without backslashes: Core only stores boot
// counters in it, and GRUB's escaping of other characters is not needed.
func parseGrubEnv(b []byte) (grubEnv, error) {
	if len(b) != grubEnvSize {
		return nil, fmt.Errorf("the GRUB environment block is %d bytes, not %d", len(b), grubEnvSize)
	}
	if !bytes.HasPrefix(b, []byte(grubEnvHeader)) {
		return nil, errors.New("the GRUB environment block has no header")
	}
	env := grubEnv{}
	for _, line := range strings.Split(string(b[len(grubEnvHeader):]), "\n") {
		if line == "" || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" || strings.Contains(v, `\`) {
			return nil, fmt.Errorf("the GRUB environment block has a line it cannot read: %q", line)
		}
		env[k] = v
	}
	return env, nil
}

// render encodes the block, variables in name order, padded to grubEnvSize with '#'.
func (e grubEnv) render() ([]byte, error) {
	var sb strings.Builder
	sb.WriteString(grubEnvHeader)
	names := make([]string, 0, len(e))
	for k := range e {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		v := e[k]
		if k == "" || strings.ContainsAny(k, "=\\\n#") || strings.ContainsAny(v, "\\\n") {
			return nil, fmt.Errorf("GRUB environment variable %q cannot be stored", k)
		}
		fmt.Fprintf(&sb, "%s=%s\n", k, v)
	}
	if sb.Len() > grubEnvSize {
		return nil, fmt.Errorf("the GRUB environment block is full (%d of %d bytes)", sb.Len(), grubEnvSize)
	}
	b := bytes.Repeat([]byte{'#'}, grubEnvSize)
	copy(b, sb.String())
	return b, nil
}

// readGrubEnv reads the block at path. A missing file is an empty environment: nothing has been counted yet.
func readGrubEnv(path string) (grubEnv, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return grubEnv{}, nil
	}
	if err != nil {
		return nil, err
	}
	return parseGrubEnv(b)
}

// writeGrubEnv replaces the block through a temp file, as grub-editenv does, so a power cut leaves the old or
// the new block, never a torn one.
func writeGrubEnv(path string, e grubEnv) error {
	b, err := e.render()
	if err != nil {
		return err
	}
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, b) {
		return nil
	}
	return writeFileAtomic(path, b)
}

// writeFileAtomic writes b to path through path+".new", flushes it and renames it into place.
func writeFileAtomic(path string, b []byte) error {
	tmp := path + tempSuffix
	if err := writeSynced(tmp, b); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// writeSynced writes b to path and flushes it.
func writeSynced(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}
