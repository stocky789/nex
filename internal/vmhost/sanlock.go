// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strings"
)

// sanlock's Debian Python binding is a thin typed wrapper over libsanlock. Keep this
// bridge limited to that API call; all policy and proof decisions stay in Go.
const sanlockHostQuery = `
import json, sys, sanlock
r = json.load(sys.stdin)
try:
    hosts = sanlock.get_hosts(r["lockspace"].encode("ascii"), r["host_id"])
except Exception:
    sys.exit(1)
if len(hosts) != 1 or hosts[0].get("host_id") != r["host_id"]:
    sys.exit(2)
h = hosts[0]
keys = ("host_id", "generation", "timestamp", "io_timeout", "flags")
if any(type(h.get(k)) is not int or h[k] < 0 for k in keys):
    sys.exit(3)
states = {sanlock.HOST_UNKNOWN: "HOST_UNKNOWN", sanlock.HOST_FREE: "HOST_FREE",
          sanlock.HOST_LIVE: "HOST_LIVE", sanlock.HOST_FAIL: "HOST_FAIL",
          sanlock.HOST_DEAD: "HOST_DEAD"}
if h["flags"] not in states:
    sys.exit(4)
result = {k: h[k] for k in keys if k != "flags"}
result["host_state"] = states[h["flags"]]
sys.stdout.write(json.dumps(result, separators=(",", ":")))
`

const sanlockHostDead = "HOST_DEAD"

type sanlockHostStatus struct {
	HostID     uint64 `json:"host_id"`
	Generation uint64 `json:"generation"`
	Timestamp  uint64 `json:"timestamp"`
	IOTimeout  uint32 `json:"io_timeout"`
	HostState  string `json:"host_state"`
}

type sanlockOutput struct{ bytes.Buffer }

func (b *sanlockOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 4096 {
		return 0, errors.New("sanlock response is too large")
	}
	return b.Buffer.Write(p)
}

func decodeSanlockHostStatus(data []byte, hostID uint64) (sanlockHostStatus, error) {
	var status sanlockHostStatus
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&status) != nil || status.HostID != hostID || status.Generation == 0 || status.IOTimeout == 0 ||
		status.HostState != "HOST_UNKNOWN" && status.HostState != "HOST_FREE" &&
			status.HostState != "HOST_LIVE" && status.HostState != "HOST_FAIL" && status.HostState != sanlockHostDead {
		return sanlockHostStatus{}, errors.New("sanlock host status is invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return sanlockHostStatus{}, errors.New("sanlock host status is invalid")
	}
	return status, nil
}

// ReadSanlockHostStatus returns one status record from sanlock's API for this exact ID.
// It never infers exclusion from a missing, free, failing, or malformed host record.
func ReadSanlockHostStatus(ctx context.Context, lockspace string, hostID uint64) (sanlockHostStatus, error) {
	var status sanlockHostStatus
	if lockspace == "" || len(lockspace) > 48 || strings.ContainsAny(lockspace, "\x00\r\n") || hostID == 0 {
		return status, errors.New("invalid sanlock host query")
	}
	for _, r := range lockspace {
		if r > 0x7f {
			return status, errors.New("invalid sanlock host query")
		}
	}
	request, err := json.Marshal(struct {
		Lockspace string `json:"lockspace"`
		HostID    uint64 `json:"host_id"`
	}{lockspace, hostID})
	if err != nil {
		return status, errors.New("invalid sanlock host query")
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/python3", "-c", sanlockHostQuery)
	cmd.Stdin = bytes.NewReader(request)
	var output sanlockOutput
	cmd.Stdout = &output
	if err := cmd.Run(); err != nil {
		return status, errors.New("sanlock host status is unavailable")
	}
	return decodeSanlockHostStatus(output.Bytes(), hostID)
}
