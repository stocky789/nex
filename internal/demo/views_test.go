// SPDX-License-Identifier: AGPL-3.0-only
package demo

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"nex.local/nex/internal/protocol"
)

func TestStatsCPUStacksMatchResourceCounts(t *testing.T) {
	s := testServer(t)
	cookie, _ := login(t, s)
	for _, kind := range []string{"hosts", "vms"} {
		for _, item := range s.fixtures.lists[kind] {
			id := item["id"].(string)
			t.Run(kind+"/"+id, func(t *testing.T) {
				count := 0
				prefix := "cpu.core."
				running := true
				if kind == "hosts" {
					count = item["inventory"].(row)["cpu_count"].(int)
				} else {
					count = item["spec"].(row)["cpu"].(row)["count"].(int)
					prefix = "cpu.vcpu."
					running = item["state"] == "running"
				}
				w := request(s, "GET", "/api/v1/"+kind+"/"+id+"/stats?range=live", "", cookie, nil)
				if w.Code != 200 {
					t.Fatalf("stats: %d %s", w.Code, w.Body.String())
				}
				var history protocol.StatsHistory
				if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil {
					t.Fatal(err)
				}
				cpu := map[string]protocol.StatsSeries{}
				for _, series := range history.Series {
					if series.Group == "cpu" {
						if series.Unit != "percent" {
							t.Fatalf("CPU unit: %s", series.Unit)
						}
						cpu[series.Key] = series
					}
				}
				total, ok := cpu["cpu.total"]
				if !ok || len(cpu) != count+1 || len(total.Values) != 120 {
					t.Fatalf("CPU stack shape: count=%d series=%d samples=%d", count, len(cpu), len(total.Values))
				}
				for i, aggregate := range total.Values {
					sum := 0.0
					for core := range count {
						key := fmt.Sprintf("%s%d", prefix, core)
						series, exists := cpu[key]
						if !exists || len(series.Values) != len(total.Values) {
							t.Fatalf("missing or misaligned CPU series %s", key)
						}
						value := series.Values[i]
						if !running {
							if value != nil || aggregate != nil {
								t.Fatalf("stopped VM has CPU sample at %d", i)
							}
							continue
						}
						if value == nil || *value < 0 || *value > 100 {
							t.Fatalf("invalid per-CPU sample at %s[%d]: %v", key, i, value)
						}
						sum += *value
					}
					if running && (aggregate == nil || math.Abs(sum/float64(count)-*aggregate) > 1e-9) {
						t.Fatalf("aggregate is not normalized per-CPU sum at %d: sum=%v total=%v", i, sum, aggregate)
					}
				}
			})
		}
	}
}
