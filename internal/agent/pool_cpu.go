// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const poolCPUPath = "/api/v1/pool/cpu/"
const poolCPULimit = 128 * 1024

var poolHostCPU = vmhost.NativeHostCPU
var poolNativeBaseline = vmhost.NativeCPUBaseline
var poolNativeCompare = vmhost.NativeCPUCompare

type poolCPUObservation struct {
	Generation uint64 `json:"generation"`
	Machine    string `json:"machine"`
	CPU        string `json:"cpu"`
}
type poolCPUComparison struct {
	Generation uint64                `json:"generation"`
	Machine    string                `json:"machine"`
	Model      string                `json:"model"`
	Features   []protocol.CPUFeature `json:"features"`
}

func poolCPUMachine(machine string) bool { return machine == "q35" || machine == "pc" }

type poolCPUCache struct {
	mu         sync.Mutex
	running    chan struct{}
	membership protocol.PoolMembership
	baseline   protocol.CPUBaseline
}

// PoolCPUProbe reports an observation, never a durable pool policy. Selecting it
// freezes its model/features in the normal Core-owned VM definition.
func PoolCPUProbe(s *Store) Probe {
	return func(ctx context.Context) (any, error) {
		result, err := cachedPoolCPUBaseline(ctx, s, "q35")
		if err != nil {
			return protocol.CPUBaseline{Machine: "q35", Error: "Pool CPU baseline unavailable: " + err.Error()}, nil
		}
		return result, nil
	}
}

// The monitor and peer handler share one bounded cache, so each member's monitor
// does not repeat the entire pool discovery. Waiting callers remain cancellable.
func cachedPoolCPUBaseline(ctx context.Context, s *Store, machine string) (protocol.CPUBaseline, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cache := &s.cpuBaseline
	for {
		if err := ctx.Err(); err != nil {
			return protocol.CPUBaseline{}, err
		}
		membership, err := Membership(s)
		if err != nil {
			return protocol.CPUBaseline{}, err
		}
		cache.mu.Lock()
		if reflect.DeepEqual(cache.membership, membership) && cache.baseline.Machine == machine && !cache.baseline.ObservedAt.IsZero() && time.Since(cache.baseline.ObservedAt) < 30*time.Second {
			result := cache.baseline
			result.Features = append([]protocol.CPUFeature(nil), result.Features...)
			cache.mu.Unlock()
			return result, nil
		}
		if cache.running != nil {
			done := cache.running
			cache.mu.Unlock()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return protocol.CPUBaseline{}, ctx.Err()
			}
		}
		cache.running = make(chan struct{})
		cache.mu.Unlock()
		result, err := discoverPoolCPUBaseline(ctx, s, membership, machine)
		if err == nil {
			err = ctx.Err()
		}
		cache.mu.Lock()
		if err == nil {
			cache.membership, cache.baseline = membership, result
		}
		close(cache.running)
		cache.running = nil
		cache.mu.Unlock()
		result.Features = append([]protocol.CPUFeature(nil), result.Features...)
		return result, err
	}
}

func validPoolCPUDefinition(b protocol.CPUBaseline) bool {
	return protocol.ValidCPUModel(b.Model) && b.Model != "host-model" && b.Model != "host-passthrough" && b.Model != "max" && len(b.Features) > 0 && protocol.ValidateCPUFeatures(b.Model, b.Features) == nil
}

func discoverPoolCPUBaseline(ctx context.Context, s *Store, membership protocol.PoolMembership, machine string) (protocol.CPUBaseline, error) {
	var result protocol.CPUBaseline
	var err error
	if membership.MasterHostID == s.State.HostID {
		result, err = poolCPUBaseline(ctx, s, machine)
	} else {
		var endpoint string
		endpoint, err = PoolMasterURL(s)
		if err == nil {
			err = poolCPURequest(ctx, s, endpoint, "", http.MethodGet, "baseline?machine="+machine+"&generation="+strconv.FormatUint(membership.Generation, 10), nil, &result)
		}
		if err == nil && (result.Error != "" || result.Generation != membership.Generation || !validPoolCPUDefinition(result) || result.Machine != machine || result.MemberCount < 1 || result.MemberCount > 64 || result.ObservedAt.IsZero() || time.Since(result.ObservedAt) > 30*time.Second || result.ObservedAt.After(time.Now().Add(5*time.Second))) {
			err = errors.New("master returned an unavailable or stale CPU baseline")
		}
	}
	current, currentErr := Membership(s)
	if err == nil && (currentErr != nil || !reflect.DeepEqual(membership, current)) {
		err = errors.New("pool membership changed during CPU discovery")
	}
	return result, err
}

func poolCPURequest(ctx context.Context, s *Store, endpoint, memberID, method, operation string, body, result any) error {
	var client *http.Client
	var err error
	if memberID == "" {
		client, err = PoolPeerClient(s, endpoint)
	} else {
		client, err = PoolMemberPeerClient(s, endpoint, memberID)
	}
	if err != nil {
		return err
	}
	defer client.CloseIdleConnections()
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint+poolCPUPath+operation, bytes.NewReader(data))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("CPU peer is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("CPU peer refused %s (HTTP %d)", strings.Split(operation, "?")[0], response.StatusCode)
	}
	if result == nil {
		return nil
	}
	data, err = io.ReadAll(io.LimitReader(response.Body, poolCPULimit+1))
	if err != nil || len(data) > poolCPULimit {
		return errors.New("invalid CPU peer response size")
	}
	if err = json.Unmarshal(data, result); err != nil {
		return errors.New("invalid CPU peer response")
	}
	return nil
}

func poolCPUVendor(cpu string) (string, error) {
	var definition struct {
		XMLName xml.Name `xml:"cpu"`
		Vendor  string   `xml:"vendor"`
	}
	if len(cpu) > 65536 || xml.Unmarshal([]byte(cpu), &definition) != nil || strings.TrimSpace(definition.Vendor) == "" {
		return "", errors.New("member CPU vendor could not be established")
	}
	return strings.TrimSpace(definition.Vendor), nil
}

func poolCPUBaseline(ctx context.Context, s *Store, machine string) (protocol.CPUBaseline, error) {
	membership, err := Membership(s)
	if err != nil {
		return protocol.CPUBaseline{}, err
	}
	if membership.MasterHostID != s.State.HostID {
		return protocol.CPUBaseline{}, errors.New("CPU baseline belongs to the pool master")
	}
	if !poolCPUMachine(machine) || len(membership.Members) > 64 {
		return protocol.CPUBaseline{}, errors.New("unsupported CPU baseline request")
	}
	var cpus []string
	var members []protocol.PoolMember
	vendor := ""
	for _, member := range membership.Members {
		if member.Status == "ejected" {
			continue
		}
		if member.Status != "ready" {
			return protocol.CPUBaseline{}, errors.New("every pool member must be ready for CPU discovery")
		}
		observation := poolCPUObservation{Generation: membership.Generation, Machine: machine}
		if member.HostID == s.State.HostID {
			observation.CPU, err = poolHostCPU(ctx, machine)
		} else if member.PeerOrigin == "" {
			err = errors.New("member CPU endpoint is unknown")
		} else {
			err = poolCPURequest(ctx, s, member.PeerOrigin, member.HostID, http.MethodGet, "capabilities?machine="+machine+"&generation="+strconv.FormatUint(membership.Generation, 10), nil, &observation)
		}
		if err != nil {
			return protocol.CPUBaseline{}, err
		}
		if observation.Generation != membership.Generation || observation.Machine != machine {
			return protocol.CPUBaseline{}, errors.New("member CPU observation has a different generation or machine")
		}
		currentVendor, e := poolCPUVendor(observation.CPU)
		if e != nil {
			return protocol.CPUBaseline{}, e
		}
		if vendor != "" && vendor != currentVendor {
			return protocol.CPUBaseline{}, errors.New("pool CPU vendors differ")
		}
		vendor = currentVendor
		cpus = append(cpus, observation.CPU)
		members = append(members, member)
	}
	baseline, err := poolNativeBaseline(ctx, cpus, machine)
	if err != nil {
		return protocol.CPUBaseline{}, err
	}
	if !validPoolCPUDefinition(baseline) {
		return protocol.CPUBaseline{}, errors.New("native CPU baseline is invalid")
	}
	for _, member := range members {
		if member.HostID == s.State.HostID {
			err = poolNativeCompare(ctx, baseline.Model, baseline.Features, machine)
		} else {
			request := poolCPUComparison{Generation: membership.Generation, Machine: machine, Model: baseline.Model, Features: baseline.Features}
			var confirmed poolCPUComparison
			err = poolCPURequest(ctx, s, member.PeerOrigin, member.HostID, http.MethodPost, "compare", request, &confirmed)
			if err == nil && !reflect.DeepEqual(request, confirmed) {
				err = errors.New("member did not confirm the exact CPU definition")
			}
		}
		if err != nil {
			return protocol.CPUBaseline{}, err
		}
	}
	current, err := Membership(s)
	if err != nil || !reflect.DeepEqual(current, membership) {
		return protocol.CPUBaseline{}, errors.New("pool membership changed during CPU discovery")
	}
	baseline.Machine, baseline.Generation, baseline.MemberCount, baseline.ObservedAt = machine, membership.Generation, len(members), time.Now().UTC()
	return baseline, nil
}

// PoolCPUHandler is a read-only authenticated Core peer API. Only the master may
// ask a member to inspect its CPU; only admitted peers may ask their master for a baseline.
func PoolCPUHandler(s *Store) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		operation := strings.TrimPrefix(r.URL.Path, poolCPUPath)
		var err error
		if operation == "baseline" {
			_, err = AuthenticatePoolPeer(s, r)
		} else {
			_, err = AuthenticatePoolMaster(s, r)
		}
		if err != nil {
			http.Error(w, "CPU peer rejected", http.StatusUnauthorized)
			return
		}
		membership, err := Membership(s)
		if err != nil {
			http.Error(w, "membership unavailable", http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		var result any
		switch operation {
		case "capabilities", "baseline":
			if r.Method != http.MethodGet {
				w.Header().Set("Allow", http.MethodGet)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			machine := r.URL.Query().Get("machine")
			generation, e := strconv.ParseUint(r.URL.Query().Get("generation"), 10, 64)
			if e != nil || !poolCPUMachine(machine) {
				http.Error(w, "invalid CPU request", http.StatusBadRequest)
				return
			}
			if generation != membership.Generation {
				http.Error(w, "membership generation changed", http.StatusConflict)
				return
			}
			if operation == "baseline" {
				result, err = cachedPoolCPUBaseline(ctx, s, machine)
			} else {
				var cpu string
				cpu, err = poolHostCPU(ctx, machine)
				result = poolCPUObservation{Generation: generation, Machine: machine, CPU: cpu}
			}
		case "compare":
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			var request poolCPUComparison
			decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, poolCPULimit))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&request) != nil || decoder.Decode(new(any)) != io.EOF || !poolCPUMachine(request.Machine) || !protocol.ValidCPUModel(request.Model) || protocol.ValidateCPUFeatures(request.Model, request.Features) != nil {
				http.Error(w, "invalid CPU comparison", http.StatusBadRequest)
				return
			}
			if request.Generation != membership.Generation {
				http.Error(w, "membership generation changed", http.StatusConflict)
				return
			}
			err = poolNativeCompare(ctx, request.Model, request.Features, request.Machine)
			result = request
		default:
			http.NotFound(w, r)
			return
		}
		current, currentErr := Membership(s)
		if err == nil && (currentErr != nil || !reflect.DeepEqual(current, membership)) {
			err = errors.New("pool membership changed during CPU discovery")
		}
		if err != nil {
			http.Error(w, "CPU compatibility could not be verified", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
}
