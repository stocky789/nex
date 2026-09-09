// SPDX-License-Identifier: AGPL-3.0-only
package localcore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"nex.local/nex/internal/vmhost"
)

// PrepareRecovery creates only an empty, separate appliance. Restoring an original
// identity and establishing its exclusive ownership remain explicit recovery steps.
func PrepareRecovery(ctx context.Context, cfg Config, input string) (vmhost.Operation, error) {
	if os.Geteuid() != 0 {
		return vmhost.Operation{}, errors.New("recovery preparation requires root")
	}
	return prepareRecovery(ctx, cfg, input)
}
func recoveryRequest(path string) (DeployRequest, error) {
	var v DeployRequest
	st, err := os.Lstat(path)
	if err != nil {
		return v, err
	}
	if !st.Mode().IsRegular() || st.Mode().Perm() != 0600 {
		return v, errors.New("recovery request must be a regular 0600 file")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return v, err
	}
	defer f.Close()
	current, err := f.Stat()
	if err != nil || !os.SameFile(st, current) || !current.Mode().IsRegular() || current.Mode().Perm() != 0600 {
		return v, errors.New("recovery request changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, 32769))
	if err != nil {
		return v, err
	}
	if len(b) > 32768 {
		return v, errors.New("recovery request exceeds 32 KiB")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&v); err != nil {
		return v, errors.New("invalid recovery request JSON")
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return v, errors.New("one recovery request required")
	}
	if v.SiteName != "" {
		return v, errors.New("recovery preparation must not specify a Site name")
	}
	if v.CPUs == 0 {
		v.CPUs = 2
	}
	if v.MemoryMiB == 0 {
		v.MemoryMiB = 4096
	}
	if v.DiskGiB == 0 {
		v.DiskGiB = 32
	}
	return v, nil
}
func prepareRecovery(ctx context.Context, cfg Config, input string) (vmhost.Operation, error) {
	var empty vmhost.Operation
	v, err := recoveryRequest(input)
	if err != nil {
		return empty, err
	}
	if !validID.MatchString(v.RequestID) || v.Bridge == "" || v.Bridge != cfg.Engine.Bridge || v.StorageDir == "" || v.StorageDir != cfg.Engine.StorageDir {
		return empty, errors.New("select a valid request ID and exactly the configured bridge and storage")
	}
	if err = os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return empty, err
	}
	st, err := os.Lstat(cfg.DataDir)
	if err != nil || !st.IsDir() || st.Mode().Perm()&0077 != 0 {
		return empty, errors.New("local state directory must be private 0700")
	}
	fd, err := syscall.Open(filepath.Join(cfg.DataDir, "lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return empty, err
	}
	lock := os.NewFile(uintptr(fd), "lock")
	defer lock.Close()
	if err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return empty, errors.New("stop the local service before recovery preparation")
	}
	if hasAgentState(cfg.AgentStateDir) {
		return empty, errors.New("recovery preparation requires a clean replacement host without agent identity")
	}
	var ops map[string]deployment
	err = readPrivate(filepath.Join(cfg.DataDir, "deployments.json"), &ops)
	if err != nil && !os.IsNotExist(err) {
		return empty, err
	}
	if len(ops) > 0 {
		return empty, errors.New("host already has a normal Controller deployment")
	}
	data, _ := json.Marshal(v)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	path := filepath.Join(cfg.DataDir, "recovery-preparation.json")
	var d deployment
	err = readPrivate(path, &d)
	if err == nil {
		if d.Digest != digest {
			return empty, errors.New("recovery request differs from persisted preparation")
		}
		if d.Operation.Status == "recovery_prepared" {
			return d.Operation, nil
		}
	} else if os.IsNotExist(err) {
		cert, key, e := deploymentTLS(v)
		if e != nil {
			return empty, errors.New("invalid network settings")
		}
		if e = vmhost.Validate(specFor(v, cert, key)); e != nil {
			return empty, e
		}
		d = deployment{Request: v, Digest: digest, Certificate: cert, Key: key, Operation: vmhost.Operation{ID: v.RequestID, Kind: "deploy", Status: "running", Stage: "recovery_preparation"}}
		if err = savePrivate(path, d); err != nil {
			return empty, err
		}
	} else {
		return empty, err
	}
	engine, err := vmhost.New(cfg.Engine)
	if err != nil {
		return empty, err
	}
	op, err := engine.Deploy(ctx, specFor(d.Request, d.Certificate, d.Key))
	if err != nil {
		return op, err
	}
	if op.Status != "booted" {
		return op, errors.New("replacement VM is not confirmed booted; inspect the existing operation")
	}
	if v.NetworkMode == "dhcp" {
		op.ControllerURL, err = engine.WaitControllerURL(ctx, v.RequestID)
		if err != nil {
			return op, err
		}
	}
	d.Operation = op
	if err = savePrivate(path, d); err != nil {
		return op, err
	}
	h, err := newHandoffHTTP(op.ControllerURL, d.Certificate, controllerServerName(v))
	if err != nil {
		return op, err
	}
	defer h.client.CloseIdleConnections()
	if err = h.waitReady(ctx); err != nil {
		return op, err
	}
	if err = h.waitApplianceReady(ctx); err != nil {
		return op, err
	}
	if err = engine.RemoveSeed(ctx, op.VMID); err != nil {
		return op, err
	}
	op.Status = "recovery_prepared"
	op.Stage = "awaiting_manual_restore"
	op.Progress = 100
	d.Operation = op
	d.Key = ""
	d.Request.AdminPassword = ""
	if err = savePrivate(path, d); err != nil {
		return op, err
	}
	return op, nil
}

func hasAgentState(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, "state.json"))
	return !os.IsNotExist(err)
}
