// SPDX-License-Identifier: AGPL-3.0-only
package vmhost

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"encoding/xml"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func testEngine(t *testing.T) *Engine {
	t.Helper()
	root := t.TempDir()
	e, err := New(Config{StateDir: filepath.Join(root, "state"), StorageDir: filepath.Join(root, "storage"), Bridge: "virbr0"})
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func validSpec(t *testing.T) DeploySpec {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "controller"}, NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return defaults(DeploySpec{RequestID: "request-1", Name: "nex-controller", Hostname: "nex-controller", AddressCIDR: "192.168.122.10/24", Gateway: "192.168.122.1", DNS: []string{"192.168.122.1"}, AdminPassword: "a-unique-test-password", TLSCertificate: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), TLSPrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))})
}
func fakeCommand(t *testing.T, name, body string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}
func TestValidation(t *testing.T) {
	s := validSpec(t)
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*DeploySpec)
	}{{"path", func(s *DeploySpec) { s.RequestID = "../escape" }}, {"broadcast", func(s *DeploySpec) { s.AddressCIDR = "192.168.122.255/24" }}, {"gateway broadcast", func(s *DeploySpec) { s.Gateway = "192.168.122.255" }}, {"gateway elsewhere", func(s *DeploySpec) { s.Gateway = "192.168.1.1" }}, {"tiny resources", func(s *DeploySpec) { s.MemoryMiB = 512 }}, {"bad key", func(s *DeploySpec) { s.TLSPrivateKey = "secret" }}, {"password line", func(s *DeploySpec) { s.AdminPassword = "long-password\ninjection" }}, {"DNS injection", func(s *DeploySpec) { s.DNS = []string{"1.1.1.1\nruncmd: bad"} }}} {
		t.Run(tc.name, func(t *testing.T) {
			copy := s
			tc.mutate(&copy)
			if Validate(copy) == nil {
				t.Fatal("invalid input accepted")
			}
		})
	}
}

func TestDeploymentAcceptsShortPasswords(t *testing.T) {
	s := validSpec(t)
	for _, password := range []string{"x", "é", " a "} {
		s.AdminPassword = password
		if err := Validate(s); err != nil {
			t.Fatalf("valid password rejected: %v", err)
		}
	}
	for _, password := range []string{"", "bad\x00", "bad\n", strings.Repeat("a", 1025)} {
		s.AdminPassword = password
		if err := Validate(s); err == nil {
			t.Fatal("invalid password accepted")
		}
	}
}

func TestDHCPNetworkValidationAndSeed(t *testing.T) {
	s := validSpec(t)
	s.NetworkMode = "dhcp"
	if Validate(s) == nil {
		t.Fatal("DHCP accepted static fields")
	}
	s.AddressCIDR, s.Gateway, s.DNS = "", "", nil
	if err := Validate(s); err != nil {
		t.Fatal(err)
	}
	if staticURL(s) != "" {
		t.Fatal("invented DHCP URL")
	}
	e := testEngine(t)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{ID: s.RequestID, VMID: id}, Disk: filepath.Join(e.config.StorageDir, id, "controller.qcow2")}
	if err := os.Mkdir(filepath.Dir(j.Disk), 0711); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "genisoimage", "exit 0")
	if err := e.seed(context.Background(), j, s); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(e.config.StateDir, id+"-seed", "network-config"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if !strings.Contains(text, "dhcp4: true") || !strings.Contains(text, mac(id)) || strings.Contains(text, "addresses:") || strings.Contains(text, "routes:") || strings.Contains(text, "nameservers:") {
		t.Fatalf("incorrect DHCP seed: %s", text)
	}
	s.NetworkMode = "unknown"
	if Validate(s) == nil {
		t.Fatal("unknown network mode accepted")
	}
}

func TestDHCPManagementAddress(t *testing.T) {
	const managementMAC = "52:54:00:11:22:33"
	for _, tc := range []struct{ name, data, want string }{
		{"right MAC", `{"return":[{"hardware-address":"52:54:00:11:22:33","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.20","prefix":24}]}]}`, "192.168.1.20"},
		{"wrong MAC", `{"return":[{"hardware-address":"52:54:00:99:22:33","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.20","prefix":24}]}]}`, ""},
		{"unavailable", `{"error":{"class":"GenericError"}}`, ""},
		{"malformed", `not-json`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := managementAddress([]byte(tc.data), managementMAC)
			if got != tc.want || (err == nil) != (tc.want != "") {
				t.Fatalf("got %q %v", got, err)
			}
		})
	}
	for _, ip := range []string{"127.0.0.1", "169.254.1.2", "0.0.0.0", "224.0.0.1", "192.168.1.0", "192.168.1.255"} {
		data := `{"return":[{"hardware-address":"` + managementMAC + `","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"` + ip + `","prefix":24}]}]}`
		if _, err := managementAddress([]byte(data), managementMAC); err == nil {
			t.Fatalf("unusable address accepted: %s", ip)
		}
	}
	data := `{"return":[{"hardware-address":"` + managementMAC + `","ip-addresses":[{"ip-address-type":"ipv4","ip-address":"192.168.1.20","prefix":24},{"ip-address-type":"ipv4","ip-address":"192.168.1.21","prefix":24}]}]}`
	if _, err := managementAddress([]byte(data), managementMAC); err == nil {
		t.Fatal("ambiguous address accepted")
	}
	e := testEngine(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if url, err := e.WaitControllerURL(ctx, "missing"); err == nil || url != "" {
		t.Fatalf("unavailable DHCP did not fail honestly: %q %v", url, err)
	}
}

func TestSignedImageAndUnsafeMetadata(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	dir := t.TempDir()
	e.config.ImagePath = filepath.Join(dir, "image.qcow2")
	e.config.ManifestPath = filepath.Join(dir, "manifest.json")
	e.config.SignaturePath = filepath.Join(dir, "manifest.sig")
	e.config.PublicKeyPath = filepath.Join(dir, "key.pub")
	image := []byte("simulated image bytes")
	sum := sha256.Sum256(image)
	m := manifest{FormatVersion: 1, ProtocolVersion: 1, Version: "test", Architecture: "amd64", Format: "qcow2", ImageSHA256: hex.EncodeToString(sum[:])}
	b, _ := json.Marshal(m)
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	for path, data := range map[string][]byte{e.config.ImagePath: image, e.config.ManifestPath: b, e.config.SignaturePath: ed25519.Sign(priv, b), e.config.PublicKeyPath: pub} {
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// Executable fakes simulate qemu-img metadata; they do not validate QEMU itself.
	fakeCommand(t, "qemu-img", `printf '%s\n' "$VMHOST_IMAGE_INFO"`)
	t.Setenv("VMHOST_IMAGE_INFO", `{"format":"qcow2","virtual-size":34359738368}`)
	if _, err := e.verifyImage(ctx); err != nil {
		t.Fatal(err)
	}
	for _, info := range []string{`{"format":"raw"}`, `{"format":"qcow2","backing-filename":"/other/disk"}`, `{"format":"qcow2","format-specific":{"data":{"data-file":"/other/disk"}}}`} {
		t.Setenv("VMHOST_IMAGE_INFO", info)
		if _, err := e.verifyImage(ctx); err == nil {
			t.Fatalf("unsafe metadata accepted: %s", info)
		}
	}
	t.Setenv("VMHOST_IMAGE_INFO", `{"format":"qcow2"}`)
	if err := os.WriteFile(e.config.ImagePath, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verifyManifest(); err != nil {
		t.Fatalf("cheap metadata check should not read image contents: %v", err)
	}
	if _, err := e.verifyImage(ctx); err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("tampering accepted: %v", err)
	}
	if err := os.WriteFile(e.config.ManifestPath, append(b, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.verifyImage(ctx); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Fatalf("invalid signature accepted: %v", err)
	}
}
func TestInterruptedRetryNeverRecreates(t *testing.T) {
	e := testEngine(t)
	s := validSpec(t)
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{ID: s.RequestID, VMID: id, Kind: "deploy", Status: "running", Stage: "copying_disk", ControllerURL: "https://192.168.122.10:8443"}, Digest: hex.EncodeToString(sum[:]), Name: s.Name, Disk: filepath.Join(e.config.StorageDir, id, "controller.qcow2"), Bridge: e.config.Bridge}
	if err := e.save(j); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "virsh", "exit 99")
	op, err := e.Deploy(context.Background(), s)
	if err != nil {
		t.Fatal(err)
	}
	if op.Status != "needs_attention" || op.VMID != id || op.ControllerURL == "" {
		t.Fatalf("bad reconciliation: %+v", op)
	}
	if _, err := os.Stat(filepath.Dir(j.Disk)); !os.IsNotExist(err) {
		t.Fatal("retry created VM disk directory")
	}
	s.MemoryMiB = 8192
	if _, err = e.Deploy(context.Background(), s); err == nil {
		t.Fatal("changed retry accepted")
	}
	ops, err := e.Operations(context.Background())
	if err != nil || len(ops) != 1 {
		t.Fatalf("operation duplication: %v %+v", err, ops)
	}
}
func TestSeedInjectsUpdateHelperWhenPresent(t *testing.T) {
	e := testEngine(t)
	bundle := t.TempDir()
	e.config.ImagePath = filepath.Join(bundle, "controller.qcow2")
	updateDir := filepath.Join(bundle, "update")
	if err := os.MkdirAll(updateDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"apply-update":                  "#!/usr/bin/python3\nprint('ok')\n",
		"appliance-firstboot":           "#!/usr/bin/python3\nprint('firstboot')\n",
		"nex-controller-update.path":    "[Path]\n",
		"nex-controller-update.service": "[Service]\n",
		"updates.conf":                  "repo_url=https://nexvirtualization.com/apt\n",
	} {
		if err := os.WriteFile(filepath.Join(updateDir, name), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	s := validSpec(t)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{ID: s.RequestID, VMID: id}, Name: s.Name, Disk: filepath.Join(e.config.StorageDir, id, "controller.qcow2"), Bridge: e.config.Bridge}
	if err := os.Mkdir(filepath.Dir(j.Disk), 0711); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "genisoimage", "exit 0")
	if err := e.seed(context.Background(), j, s); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(e.config.StateDir, id+"-seed", "user-data"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(b)
	for _, want := range []string{"/usr/local/lib/nex/apply-update", "/usr/local/lib/nex/appliance-firstboot", "/etc/nex/updates.conf", "nex-controller-update.path", "systemctl, enable, --now, nex-controller-update.path"} {
		if !strings.Contains(text, want) {
			t.Fatalf("seed missing %q in %s", want, text)
		}
	}
}

func TestSeedAndDomainContainUniquePrivateConfiguration(t *testing.T) {
	e := testEngine(t)
	s := validSpec(t)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{ID: s.RequestID, VMID: id}, Name: s.Name, Disk: filepath.Join(e.config.StorageDir, id, "controller.qcow2"), Bridge: e.config.Bridge}
	if err := os.Mkdir(filepath.Dir(j.Disk), 0711); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "genisoimage", "exit 0")
	if err := e.seed(context.Background(), j, s); err != nil {
		t.Fatal(err)
	}
	isoStat, isoErr := os.Stat(filepath.Join(filepath.Dir(j.Disk), "seed.iso"))
	if isoErr != nil || isoStat.Mode().Perm() != 0600 {
		t.Fatal("seed ISO permissions not private")
	}
	dir := filepath.Join(e.config.StateDir, id+"-seed")
	b, err := os.ReadFile(filepath.Join(dir, "user-data"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "permissions: '0600'") || strings.Contains(string(b), s.AdminPassword) {
		t.Fatal("seed lacks protected encoding")
	}
	st, err := os.Stat(filepath.Join(dir, "user-data"))
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("seed permissions not private")
	}
	b, _ = os.ReadFile(filepath.Join(dir, "network-config"))
	if !strings.Contains(string(b), mac(id)) || !strings.Contains(string(b), s.AddressCIDR) {
		t.Fatal("network identity missing")
	}
	d := makeDomain(j, s)
	b, err = xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	var decoded domainXML
	if err = xml.Unmarshal(b, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.UUID != id || decoded.Devices.Disks[0].Source.File != j.Disk || decoded.Devices.Graphics[0].Listen.Type != "socket" || decoded.CPU.Mode != "host-passthrough" || strings.Contains(string(b), s.AdminPassword) {
		t.Fatalf("invalid domain: %s", b)
	}
}
func TestLockCancellationAndInvalidPaths(t *testing.T) {
	e := testEngine(t)
	lock, err := e.lock(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = e.lock(ctx); err == nil {
		t.Fatal("concurrent mutation not blocked")
	}
	if _, err = e.Operation(context.Background(), "../outside"); err == nil {
		t.Fatal("path traversal accepted")
	}
	if _, err = e.Power(context.Background(), "request", "not-a-uuid", "start"); err == nil {
		t.Fatal("invalid VM accepted")
	}
}

func TestExclusiveDiskRejectsOtherDomainsAndHardlinks(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("fake image"), 0600); err != nil {
		t.Fatal(err)
	}
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	other := "6fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{VMID: id}, Disk: disk}
	t.Setenv("VMHOST_IDS", id+"\n"+other)
	t.Setenv("VMHOST_XML", `<domain><devices><disk><source file="`+disk+`"/></disk></devices></domain>`)
	fakeCommand(t, "virsh", `case "$3" in list) printf '%s\n' "$VMHOST_IDS";; dumpxml) printf '%s\n' "$VMHOST_XML";; *) exit 2;; esac`)
	if err := e.exclusiveDisk(context.Background(), j); err == nil || !strings.Contains(err.Error(), "another domain") {
		t.Fatalf("shared disk accepted: %v", err)
	}
	t.Setenv("VMHOST_IDS", id)
	if err := e.exclusiveDisk(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(disk, filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := e.exclusiveDisk(context.Background(), j); err == nil || !strings.Contains(err.Error(), "hardlink") {
		t.Fatalf("hardlink accepted: %v", err)
	}
}

// Regression BUG-0191: an unrelated domain whose disk lives on an unmounted
// repository must not block exclusive ownership of a disk on a healthy one.
func TestExclusiveDiskSkipsMissingOtherDomainSources(t *testing.T) {
	e := testEngine(t)
	dir := t.TempDir()
	disk := filepath.Join(dir, "disk.qcow2")
	if err := os.WriteFile(disk, []byte("fake image"), 0600); err != nil {
		t.Fatal(err)
	}
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	other := "6fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	j := journal{Operation: Operation{VMID: id}, Disk: disk}
	t.Setenv("VMHOST_IDS", id+"\n"+other)
	fakeCommand(t, "virsh", `case "$3" in list) printf '%s\n' "$VMHOST_IDS";; dumpxml) printf '%s\n' "$VMHOST_XML";; *) exit 2;; esac`)
	missing := `<source file="` + filepath.Join(dir, "missing", "disk-0.qcow2") + `"/>`
	for _, tc := range []struct{ name, sources, want string }{
		{"missing source", missing, ""},
		{"not a directory", `<source file="` + disk + `/child"/>`, "cannot be inspected"},
		{"missing then shared", missing + `<source file="` + disk + `"/>`, "another domain"},
	} {
		t.Setenv("VMHOST_XML", `<domain><devices><disk>`+tc.sources+`</disk></devices></domain>`)
		err := e.exclusiveDisk(context.Background(), j)
		if tc.want == "" && err != nil || tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
			t.Fatalf("%s: err=%v want %q", tc.name, err, tc.want)
		}
	}
}

func TestPowerStateChecksJobsOnlyForRunningGuests(t *testing.T) {
	e := testEngine(t)
	fakeCommand(t, "virsh", `case "$3" in domstate) printf '%s\n' "$VMHOST_STATE";; domjobinfo) if [ "$VMHOST_STATE" = 'shut off' ]; then exit 9; fi; printf '%s\n' "$VMHOST_JOB";; *) exit 2;; esac`)
	for _, tc := range []struct {
		state, job string
		ok         bool
	}{{"shut off", "", true}, {"running", "Job type: None", true}, {"running", "Job type: Unbounded", false}, {"paused", "Job type: None", false}, {"unknown", "", false}} {
		t.Setenv("VMHOST_STATE", tc.state)
		t.Setenv("VMHOST_JOB", tc.job)
		err := e.powerState(context.Background(), "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d")
		if (err == nil) != tc.ok {
			t.Fatalf("state=%s job=%s err=%v", tc.state, tc.job, err)
		}
	}
}

// Regression: QEMU cannot bind sockets inside the root-owned appliance disk
// directory. Libvirt must allocate its private runtime socket instead.
func TestConsoleSocketUsesLibvirtRuntimeDirectory(t *testing.T) {
	d := makeDomain(journal{Name: "nex-controller", Disk: "/var/lib/nex/appliances/vm/controller.qcow2", Bridge: "virbr0", Operation: Operation{VMID: "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"}}, DeploySpec{CPUs: 2, MemoryMiB: 4096})
	b, err := xml.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Devices.Graphics) != 1 || d.Devices.Graphics[0].Type != "vnc" || d.Devices.Graphics[0].Listen.Type != "socket" {
		t.Fatal("console must use private UNIX transport")
	}
	if d.Devices.Graphics[0].Socket != "" || d.Devices.Graphics[0].Listen.Socket != "" || strings.Contains(string(b), `socket="`) {
		t.Fatal("console socket path must be allocated by libvirt, not the root-owned disk directory")
	}
}

// The packaged service uses UMask=0077. Directory traversal must survive that
// mask without granting write access or exposing bootstrap files.
func TestApplianceDirectoryTraversableUnderPrivateUmask(t *testing.T) {
	previous := syscall.Umask(0077)
	defer syscall.Umask(previous)
	e := testEngine(t)
	s := validSpec(t)
	id := "5fd4c1fa-5c4c-43cf-91c2-8f42ef87db8d"
	dir := filepath.Join(e.config.StorageDir, id)
	if err := createDiskDir(dir); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || st.Mode().Perm() != 0711 {
		t.Fatalf("QEMU requires directory traversal under UMask=0077: mode=%v err=%v", st.Mode().Perm(), err)
	}
	disk := filepath.Join(dir, "controller.qcow2")
	if err := os.WriteFile(disk, []byte("simulated image"), 0600); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "genisoimage", "exit 0")
	j := journal{Operation: Operation{VMID: id}, Name: s.Name, Disk: disk, Bridge: e.config.Bridge}
	if err := e.seed(context.Background(), j, s); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{disk, filepath.Join(dir, "seed.iso"), filepath.Join(e.config.StateDir, id+"-seed", "user-data")} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatalf("private file %s has unsafe mode: %v", path, err)
		}
	}
	for _, path := range []string{e.config.StateDir, filepath.Join(e.config.StateDir, id+"-seed")} {
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0700 {
			t.Fatalf("secret directory %s has unsafe mode: %v", path, err)
		}
	}
}
