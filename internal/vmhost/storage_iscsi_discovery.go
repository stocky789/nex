// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"nex.local/nex/internal/protocol"
)

var (
	iscsiInspectHelper = "/usr/lib/nex/nex-iscsi-inspect"
	iscsiInitiatorPath = "/etc/iscsi/initiatorname.iscsi"
	iscsiInspectRun    = runInspectHelper
)

// runInspectHelper gives the native helper, which holds a CHAP secret, nothing but LC_ALL=C:
// no inherited proxy, LD_*, or other Core environment.
func runInspectHelper(ctx context.Context, stdin []byte, name string, args ...string) ([]byte, error) {
	return runCommandEnv(ctx, "", []string{"LC_ALL=C"}, stdin, name, args...)
}

func discoveryInitiator() (string, error) {
	file, err := os.Open(iscsiInitiatorPath)
	if err != nil {
		return "", errors.New("iSCSI initiator identity is unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(data) > 4096 {
		return "", errors.New("iSCSI initiator identity is unavailable")
	}
	var initiator string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if name != "InitiatorName" || !ok {
			continue
		}
		if initiator != "" || !protocol.ValidISCSITarget(value) {
			return "", errors.New("iSCSI initiator identity is invalid")
		}
		initiator = value
	}
	if initiator == "" {
		return "", errors.New("iSCSI initiator identity is unavailable")
	}
	return initiator, nil
}

// ISCSIInspectionAvailable requires both the actual host initiator and a working native
// helper/library adapter. A missing runtime never advertises selected-target inspection.
func ISCSIInspectionAvailable(ctx context.Context) bool {
	if _, err := discoveryInitiator(); err != nil {
		return false
	}
	work, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	output, err := iscsiInspectRun(work, nil, iscsiInspectHelper, "--probe")
	return err == nil && strings.TrimSpace(string(output)) == protocol.ISCSIInspectCapability
}

func inspectISCSILUNs(ctx context.Context, request protocol.StorageDiscoveryRequest) ([]protocol.ISCSILUNDiscovery, error) {
	initiator, err := discoveryInitiator()
	if err != nil {
		return nil, err
	}
	portal, err := canonicalISCSIPortal(request.Server)
	if err != nil {
		return nil, err
	}
	var username, secret string
	if request.Credentials != nil {
		username, secret = request.Credentials.Username, request.Credentials.Secret
	}
	// Names and credentials have already rejected CR/LF/NUL. Stdin goes directly to
	// one bounded child, never to argv, environment, a temporary file or a journal.
	input := make([]byte, 0, len(initiator)+len(portal)+len(request.TargetIQN)+len(username)+len(secret)+5)
	for _, field := range []string{initiator, portal, request.TargetIQN, username, secret} {
		input = append(input, field...)
		input = append(input, '\n')
	}
	defer clear(input)
	output, err := iscsiInspectRun(ctx, input, iscsiInspectHelper)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, errors.New("read-only iSCSI inspection is unavailable")
	}
	if len(output) > 64<<10 {
		return nil, errors.New("iSCSI inspection exceeded its result limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	decoder.DisallowUnknownFields()
	var observations []struct {
		LUN            *uint64 `json:"lun"`
		WWID           string  `json:"wwid,omitempty"`
		FilesystemUUID string  `json:"filesystem_uuid,omitempty"`
		FilesystemType string  `json:"filesystem_type,omitempty"`
		SizeBytes      uint64  `json:"size_bytes,omitempty"`
		Error          string  `json:"error,omitempty"`
	}
	if err := decoder.Decode(&observations); err != nil || len(observations) > protocol.MaxISCSILUNDiscoveries || bytes.Equal(bytes.TrimSpace(output), []byte("null")) {
		return nil, errors.New("iSCSI inspection returned invalid metadata")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("iSCSI inspection returned invalid metadata")
	}
	luns := make([]protocol.ISCSILUNDiscovery, 0, len(observations))
	seen := make(map[uint64]bool, len(observations))
	for _, observed := range observations {
		if observed.LUN == nil {
			return nil, errors.New("iSCSI inspection returned invalid metadata")
		}
		lun := protocol.ISCSILUNDiscovery{LUN: *observed.LUN, WWID: observed.WWID, FilesystemUUID: observed.FilesystemUUID, FilesystemType: observed.FilesystemType, SizeBytes: observed.SizeBytes, Error: observed.Error}
		// One odd filesystem identity must not hide the target's other LUNs.
		if lun.FilesystemUUID != "" && !iscsiUUID.MatchString(lun.FilesystemUUID) {
			lun.FilesystemUUID, lun.Error = "", "Filesystem UUID is not in a supported form; enter values manually"
		}
		if seen[lun.LUN] || (lun.WWID != "" && (!iscsiWWID.MatchString(lun.WWID) || len(lun.WWID) > 255)) ||
			!discoveryMetadata(lun.FilesystemUUID, 255) || !discoveryMetadata(lun.FilesystemType, 63) || !discoveryMetadata(lun.Error, 511) {
			return nil, errors.New("iSCSI inspection returned invalid metadata")
		}
		seen[lun.LUN] = true
		luns = append(luns, lun)
	}
	sort.Slice(luns, func(i, j int) bool { return luns[i].LUN < luns[j].LUN })
	return luns, nil
}

func discoveryMetadata(value string, limit int) bool {
	if len(value) > limit {
		return false
	}
	for _, c := range value {
		if c < ' ' || c > '~' {
			return false
		}
	}
	return true
}
