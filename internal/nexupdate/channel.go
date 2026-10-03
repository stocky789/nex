// SPDX-License-Identifier: AGPL-3.0-only
package nexupdate

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// The package archive keeps its development channel in the dev subdirectory of the stable archive, so
// a package-edition host's channel is the repo_url it reads: the archive root, or the root plus /dev.
// The image edition's channel is a separate setting (ImageSettings) because its update tree has one
// directory per channel.
const devRepoPath = "/dev"

// RepoChannel is the channel a package archive URL names: stable, or dev.
func RepoChannel(repoURL string) string {
	if strings.HasSuffix(strings.TrimRight(repoURL, "/"), devRepoPath) {
		return "dev"
	}
	return "stable"
}

// ChannelRepoURL moves an archive URL to channel: the archive root for stable, its dev subdirectory for dev.
func ChannelRepoURL(repoURL, channel string) string {
	root := strings.TrimSuffix(strings.TrimRight(repoURL, "/"), devRepoPath)
	if channel == "dev" {
		return root + devRepoPath
	}
	return root
}

// SaveClassicChannel points repo_url in /etc/nex/updates.conf at channel, keeping every other line, and
// forgets the package-index stamp so the next check reads the new archive instead of the index of the
// old one. The new source reaches apt when that check rewrites it.
func SaveClassicChannel(channel string) error {
	if channel != "stable" && channel != "dev" {
		return refuse(400, "invalid_channel", "the package edition offers the stable and dev channels")
	}
	if os.Getenv("NEX_UPDATE_REPO") != "" {
		return refuse(409, "settings_overridden", "the update archive is fixed by the service environment and cannot be changed here")
	}
	kept := []string{"repo_url=" + ChannelRepoURL(DefaultRepoURL(), channel)}
	if b, err := os.ReadFile(UpdatesConfig); err == nil {
		for _, line := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
			if t := strings.TrimSpace(line); t != "" && !strings.HasPrefix(t, "repo_url=") {
				kept = append(kept, line)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(UpdatesConfig), 0o755); err != nil {
		return err
	}
	if err := writeFileAtomic(UpdatesConfig, []byte(strings.Join(kept, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	_ = os.Remove(IndexStampPath)
	return nil
}
