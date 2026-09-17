// SPDX-License-Identifier: AGPL-3.0-only
package protocol

import "time"

// UsageHistoryCapability is offered by a Core that keeps usage history and answers stats
// channels. A Controller delivers StatsChannelRequest only to a host that offered it.
const UsageHistoryCapability = "usage-history-v1"

// StatsChannelRequest asks the agent to dial POST /api/v1/agent/stats/{Token} with
// "Upgrade: nex-stats". After 101 Switching Protocols the Controller writes HTTP/1.1
// requests on the connection and the agent answers each in order:
//
//	GET /stats?object=vm&id=<uuid>&range=24h
//	GET /stats?object=host&range=10m
//
// A 200 carries a StatsHistory; any other status carries {"error": "..."}. Either side
// closes the connection when it is idle, on restart, or on any protocol error.
type StatsChannelRequest struct {
	Token string `json:"token"`
}

// StatsRanges maps each queryable range to the resolution tier that serves it. "live" is not a
// stored tier: a query for it starts, or renews for StatsLiveLease, one-second sampling of that
// object into a memory-only buffer on the host, and returns what the buffer holds so far.
var StatsRanges = map[string]StatsTier{
	"live": {Step: time.Second, Points: 120},
	"10m":  {Step: 5 * time.Second, Points: 120},
	"24h":  {Step: time.Minute, Points: 1440},
	"30d":  {Step: time.Hour, Points: 720},
	"1y":   {Step: 24 * time.Hour, Points: 365},
}

// StatsLiveLease is how long one-second sampling continues after the last live query. The host
// owns the lease; nothing is scheduled or remembered by the Controller.
const StatsLiveLease = 2 * time.Minute

// StatsLiveObjects caps how many objects one host samples every second at once. A live query
// beyond the cap is refused with 429 rather than slowing every other sample.
const StatsLiveObjects = 8

type StatsTier struct {
	Step   time.Duration
	Points int
}

// StatsHistory is one object's history for one range. Values has exactly one entry per
// step from Start; nil is unknown or not running, never zero. Series and points are
// bounded by the host (128 series, 1440 points).
type StatsHistory struct {
	Object      string        `json:"object"` // "vm" or "host"
	ID          string        `json:"id"`
	HostID      string        `json:"host_id,omitempty"` // set by the Controller relay
	Range       string        `json:"range"`
	StepSeconds int           `json:"step_seconds"`
	Start       time.Time     `json:"start"`
	Series      []StatsSeries `json:"series"`
}

type StatsSeries struct {
	Key    string     `json:"key"`   // stable, e.g. "cpu.vcpu.0", "net.vnet3.rx", "disk.vda.read_bytes"
	Group  string     `json:"group"` // cpu, memory, network, disk, load
	Label  string     `json:"label"` // operator-facing, e.g. "vCPU 0", "vnet3 rx"
	Unit   string     `json:"unit"`  // percent, bytes, bytes_per_second, iops, load
	Values []*float64 `json:"values"`
}
