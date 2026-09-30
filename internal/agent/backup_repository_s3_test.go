// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"nex.local/nex/internal/protocol"
)

// This fake exercises the Core client contract; it is not evidence for a real S3 service's durability or fencing.
type fakeS3 struct {
	mu                         sync.Mutex
	objects                    map[string][]byte
	ignoreConditional          bool
	failManifestDelete         bool
	uploads                    map[string]map[int][]byte
	aborts                     int
	failPart                   bool
	ignoreMultipartConditional bool
	multipartStarts            int
	denyManifestPut            bool
	rejectArtifactPut          bool
	loseManifestReply          bool
	failManifestGetOnce        bool
	failArtifactDeleteOnce     bool
	failFirmwareDeleteOnce     bool
	ignoreConditionalDelete    bool
	blockManifestPutOnce       atomic.Bool
	finalWinsTombstoneOnce     atomic.Bool
	manifestPutEntered         chan struct{}
	releaseManifestPut         chan struct{}
	finalCommitted             chan struct{}
	blockArtifactPutOnce       atomic.Bool
	blockArtifactCompleteOnce  atomic.Bool
	artifactPutEntered         chan struct{}
	releaseArtifactPut         chan struct{}
	onRequest                  func(*http.Request)
	denyGet                    string
	client                     *http.Client
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/backups/")
	if f.onRequest != nil {
		f.onRequest(r)
	}
	if r.Method == http.MethodPut && strings.HasSuffix(key, "/manifest.json") && f.blockManifestPutOnce.CompareAndSwap(true, false) {
		close(f.manifestPutEntered)
		<-f.releaseManifestPut
	}
	if r.Method == http.MethodPut && strings.HasSuffix(key, "/manifest.json") && f.finalWinsTombstoneOnce.CompareAndSwap(true, false) {
		close(f.releaseManifestPut)
		<-f.finalCommitted
	}
	if strings.HasSuffix(key, "/disk.qcow2") &&
		(r.Method == http.MethodPut && !r.URL.Query().Has("uploadId") && f.blockArtifactPutOnce.CompareAndSwap(true, false) ||
			r.Method == http.MethodPost && r.URL.Query().Has("uploadId") && f.blockArtifactCompleteOnce.CompareAndSwap(true, false)) {
		close(f.artifactPutEntered)
		<-f.releaseArtifactPut
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.URL.Query().Has("uploads") && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, "<ListMultipartUploadsResult><IsTruncated>false</IsTruncated>")
		for k := range f.uploads {
			if strings.HasPrefix(k, r.URL.Query().Get("prefix")) {
				_, _ = io.WriteString(w, "<Upload><Key>"+k+"</Key><UploadId>test-upload</UploadId></Upload>")
			}
		}
		_, _ = io.WriteString(w, "</ListMultipartUploadsResult>")
		return
	}
	if r.URL.Query().Has("uploads") && r.Method == http.MethodPost {
		f.multipartStarts++
		if f.uploads == nil {
			f.uploads = map[string]map[int][]byte{}
		}
		f.uploads[key] = map[int][]byte{}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, "<InitiateMultipartUploadResult><Bucket>backups</Bucket><Key>"+key+"</Key><UploadId>test-upload</UploadId></InitiateMultipartUploadResult>")
		return
	}
	if r.URL.Query().Has("uploadId") {
		switch r.Method {
		case http.MethodPut:
			if f.failPart {
				s3TestError(w, http.StatusBadRequest, "InvalidPart")
				return
			}
			part, _ := strconv.Atoi(r.URL.Query().Get("partNumber"))
			body, _ := fakeS3Body(r)
			f.uploads[key][part] = body
			w.Header().Set("ETag", "\"part\"")
		case http.MethodPost:
			if _, exists := f.objects[key]; exists && r.Header.Get("If-None-Match") == "*" && !f.ignoreMultipartConditional {
				s3TestError(w, http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
			parts := f.uploads[key]
			var numbers []int
			for n := range parts {
				numbers = append(numbers, n)
			}
			sort.Ints(numbers)
			var body []byte
			for _, n := range numbers {
				body = append(body, parts[n]...)
			}
			f.objects[key] = body
			delete(f.uploads, key)
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, "<CompleteMultipartUploadResult><Bucket>backups</Bucket><Key>"+key+"</Key><ETag>\"complete\"</ETag></CompleteMultipartUploadResult>")
		case http.MethodDelete:
			delete(f.uploads, key)
			f.aborts++
			w.WriteHeader(http.StatusNoContent)
		}
		return
	}
	if r.URL.Query().Has("list-type") {
		prefix := r.URL.Query().Get("prefix")
		var keys []string
		for k := range f.objects {
			if strings.HasPrefix(k, prefix) {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, "<ListBucketResult><IsTruncated>false</IsTruncated>")
		for _, k := range keys {
			_, _ = io.WriteString(w, "<Contents><Key>")
			_ = xml.EscapeText(w, []byte(k))
			_, _ = io.WriteString(w, "</Key></Contents>")
		}
		_, _ = io.WriteString(w, "</ListBucketResult>")
		return
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if key == f.denyGet {
			s3TestError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if strings.HasSuffix(key, "/manifest.json") && f.failManifestGetOnce {
			f.failManifestGetOnce = false
			s3TestError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if body, ok := f.objects[key]; ok {
			w.Header().Set("ETag", fakeS3ETag(body))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body)
		} else {
			s3TestError(w, http.StatusNotFound, "NoSuchKey")
		}
	case http.MethodPut:
		if strings.HasSuffix(key, "/manifest.json") && f.denyManifestPut {
			s3TestError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if f.rejectArtifactPut && strings.HasSuffix(key, "/disk.qcow2") {
			s3TestError(w, http.StatusRequestEntityTooLarge, "EntityTooLarge")
			return
		}
		if old, ok := f.objects[key]; ok && r.Header.Get("If-Match") != "" && r.Header.Get("If-Match") != fakeS3ETag(old) {
			s3TestError(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if _, ok := f.objects[key]; ok && r.Header.Get("If-None-Match") == "*" && !f.ignoreConditional {
			s3TestError(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		if strings.HasSuffix(key, "/manifest.json") && f.loseManifestReply {
			f.loseManifestReply = false
			f.failManifestGetOnce = true
			s3TestError(w, http.StatusBadRequest, "InvalidRequest")
			return
		}
	case http.MethodDelete:
		if body, ok := f.objects[key]; ok && r.Header.Get("If-Match") != "" &&
			r.Header.Get("If-Match") != fakeS3ETag(body) && !f.ignoreConditionalDelete {
			s3TestError(w, http.StatusPreconditionFailed, "PreconditionFailed")
			return
		}
		if strings.HasSuffix(key, "/disk.qcow2") && f.failArtifactDeleteOnce {
			f.failArtifactDeleteOnce = false
			s3TestError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		if strings.HasSuffix(key, "/firmware.bin") && f.failFirmwareDeleteOnce {
			f.failFirmwareDeleteOnce = false
			s3TestError(w, http.StatusForbidden, "AccessDenied")
			return
		}
		delete(f.objects, key)
		if f.failManifestDelete && strings.HasSuffix(key, "/manifest.json") {
			s3TestError(w, http.StatusInternalServerError, "InternalError")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestS3AbortFencesDelayedFinalManifest(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk before delayed final")
	manifest := publishDeniedS3Point(t, b, fake, payload)
	fake.manifestPutEntered = make(chan struct{})
	fake.releaseManifestPut = make(chan struct{})
	fake.blockManifestPutOnce.Store(true)
	raw, _ := json.Marshal(manifest)
	finalResult := make(chan error, 1)
	go func() {
		_, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
			Key: aws.String(b.pointKey(manifest.RecoveryPointID, "manifest.json")), Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*")})
		finalResult <- err
	}()
	<-fake.manifestPutEntered
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
		close(fake.releaseManifestPut)
		t.Fatal(err)
	}
	close(fake.releaseManifestPut)
	if err := <-finalResult; !isS3Precondition(err) {
		t.Fatalf("delayed final PUT escaped tombstone: %v", err)
	}
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("aborted point read: %v", err)
	}
	if names, err := b.Entries(ctx); err != nil || len(names) != 0 {
		t.Fatalf("tombstone appeared in catalog: %v %v", names, err)
	}
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("idempotent tombstone retry: %v", err)
	}
}

func TestS3PublishedFinalWinsAbortAfterAbsenceCheck(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk before final wins")
	manifest := publishDeniedS3Point(t, b, fake, payload)
	fake.manifestPutEntered = make(chan struct{})
	fake.releaseManifestPut = make(chan struct{})
	fake.finalCommitted = make(chan struct{})
	fake.blockManifestPutOnce.Store(true)
	raw, _ := json.Marshal(manifest)
	finalResult := make(chan error, 1)
	go func() {
		_, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
			Key: aws.String(b.pointKey(manifest.RecoveryPointID, "manifest.json")), Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*")})
		finalResult <- err
		close(fake.finalCommitted)
	}()
	<-fake.manifestPutEntered
	fake.finalWinsTombstoneOnce.Store(true)
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("published final did not stop abort: %v", err)
	}
	if err := <-finalResult; err != nil {
		t.Fatalf("published final lost key race: %v", err)
	}
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("published final was blocked by abort: %v", err)
	}
	if names, err := b.Entries(ctx); err != nil || !slices.Contains(names, manifest.RecoveryPointID) {
		t.Fatalf("published final cannot be adopted: %v %v", names, err)
	}
}

func TestS3AbortFencesDelayedArtifactWrites(t *testing.T) {
	for _, multipart := range []bool{false, true} {
		name := "single-put"
		if multipart {
			name = "multipart-completion"
		}
		t.Run(name, func(t *testing.T) {
			b, fake := testS3Storage(t)
			ctx := context.Background()
			payload := []byte("disk before delayed artifact")
			manifest := publishDeniedS3Point(t, b, fake, payload)
			fake.artifactPutEntered = make(chan struct{})
			fake.releaseArtifactPut = make(chan struct{})
			key := b.pointKey(manifest.RecoveryPointID, "disk.qcow2")
			lateResult := make(chan error, 1)
			if multipart {
				fake.mu.Lock()
				fake.uploads[key] = map[int][]byte{1: payload}
				fake.mu.Unlock()
				fake.blockArtifactCompleteOnce.Store(true)
				go func() {
					url := b.repo.S3Source.Endpoint + "/backups/" + key + "?uploadId=late"
					req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader("<CompleteMultipartUpload/>"))
					if err != nil {
						lateResult <- err
						return
					}
					req.Header.Set("If-None-Match", "*")
					resp, err := fake.client.Do(req)
					if err == nil {
						_ = resp.Body.Close()
						if resp.StatusCode != http.StatusPreconditionFailed {
							err = fmt.Errorf("multipart completion status %d", resp.StatusCode)
						}
					}
					lateResult <- err
				}()
			} else {
				fake.blockArtifactPutOnce.Store(true)
				go func() {
					_, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
						Key: aws.String(key), Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*")})
					if !isS3Precondition(err) {
						lateResult <- fmt.Errorf("single PUT escaped tombstone: %v", err)
						return
					}
					lateResult <- nil
				}()
			}
			<-fake.artifactPutEntered
			if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
				close(fake.releaseArtifactPut)
				t.Fatal(err)
			}
			close(fake.releaseArtifactPut)
			if err := <-lateResult; err != nil {
				t.Fatal(err)
			}
			if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
				t.Fatalf("exact fenced retry: %v", err)
			}
			if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("aborted point read: %v", err)
			}
		})
	}
}

func TestS3AbortResumesPartiallyFencedArtifacts(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	disk := []byte("disk")
	firmware := []byte("nvram")
	manifest := testS3Manifest(disk)
	sum := sha256.Sum256(firmware)
	manifest.Firmware = []protocol.BackupFirmwareArtifact{{Kind: "nvram", Filename: "firmware.bin",
		Size: int64(len(firmware)), SHA256: hex.EncodeToString(sum[:])}}
	stageDir := t.TempDir()
	diskPath := filepath.Join(stageDir, "disk.qcow2")
	fwPath := filepath.Join(stageDir, "firmware.bin")
	if err := os.WriteFile(diskPath, disk, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fwPath, firmware, 0600); err != nil {
		t.Fatal(err)
	}
	fake.denyManifestPut = true
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": diskPath, "firmware.bin": fwPath}); err == nil {
		t.Fatal("initial final denial missing")
	}
	fake.denyManifestPut = false
	fake.failFirmwareDeleteOnce = true
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("interrupted artifact cleanup settled")
	}
	fake.mu.Lock()
	fencedDisk := bytes.Clone(fake.objects[b.pointKey(manifest.RecoveryPointID, "disk.qcow2")])
	remainingFirmware := bytes.Clone(fake.objects[b.pointKey(manifest.RecoveryPointID, "firmware.bin")])
	fake.mu.Unlock()
	if !bytes.Equal(fencedDisk, b.artifactTombstone(manifest.RecoveryPointID, "disk.qcow2", manifest.Disks[0])) ||
		!bytes.Equal(remainingFirmware, firmware) {
		t.Fatal("interrupted cleanup did not retain exact mixed state")
	}
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("partial fence retry: %v", err)
	}
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("settled fence retry: %v", err)
	}
}

func fakeS3ETag(body []byte) string {
	sum := sha256.Sum256(body)
	return "\"" + hex.EncodeToString(sum[:]) + "\""
}

func fakeS3Body(r *http.Request) ([]byte, error) {
	if !strings.Contains(r.Header.Get("Content-Encoding"), "aws-chunked") {
		return io.ReadAll(r.Body)
	}
	reader := bufio.NewReader(r.Body)
	var body bytes.Buffer
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		sizeHex := strings.SplitN(strings.TrimSpace(line), ";", 2)[0]
		size, err := strconv.ParseInt(sizeHex, 16, 64)
		if err != nil {
			return nil, err
		}
		if size == 0 {
			return body.Bytes(), nil
		}
		if _, err := io.CopyN(&body, reader, size); err != nil {
			return nil, err
		}
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, err
		}
	}
}

func s3TestError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, "<Error><Code>"+code+"</Code></Error>")
}

func testS3Storage(t *testing.T) (*s3BackupStorage, *fakeS3) {
	t.Helper()
	fake := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewTLSServer(fake)
	fake.client = server.Client()
	t.Cleanup(server.Close)
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Test", Kind: protocol.BackupRepoKindS3,
		S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: "pool-1"}, CredentialRef: "key-1"}
	storage := &s3BackupStorage{s: seedBackupMaster(t), repo: repo, root: "pool-1/",
		client: s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(server.URL), UsePathStyle: true,
			HTTPClient: server.Client(), Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
				return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}, nil
			})})}
	return storage, fake
}

func TestS3CoreRegistrationAndTestUsePrivateReceipt(t *testing.T) {
	s := seedBackupMaster(t)
	fake := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	previous := newBackupS3Client
	newBackupS3Client = func(options s3.Options) *s3.Client {
		options.HTTPClient = server.Client()
		return s3.New(options)
	}
	t.Cleanup(func() { newBackupS3Client = previous })
	handoff := protocol.StorageCredentialHandoff{Offer: protocol.StorageCredentialOffer{
		RequestID: "credential-1", RegistrationRequestID: "backup-credential:s3:register-1",
		PoolID: s.State.PoolID, HostID: s.State.HostID, RepositoryID: "repo-1", ExpiresAt: time.Now().Add(time.Minute),
	}, Credentials: protocol.StorageCredentials{Username: "test-key", Secret: "test-secret"}}
	receipt, err := saveCredentialHandoff(s, handoff)
	if err != nil {
		t.Fatal(err)
	}
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Test", Kind: protocol.BackupRepoKindS3,
		S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: "pool-1"}, CredentialRef: receipt.Receipt}
	if _, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "wrong-operation"); err == nil {
		t.Fatal("wrong registration operation resolved private credential")
	}
	prepared, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1")
	if err != nil || prepared.RemoteIdentity == nil {
		t.Fatalf("prepare: %#v %v", prepared.RemoteIdentity, err)
	}
	if !preparedBackupRepositoryMatches(repo, prepared) {
		t.Fatal("prepared S3 identity rejected")
	}
	saved, err := upsertPoolBackupRepositoryPrepared(s, repo, prepared)
	if err != nil || saved.RemoteIdentity == nil || saved.MountPath != "" {
		t.Fatalf("save: %#v %v", saved, err)
	}
	backend, err := openBackupRepositoryStorage(s, saved)
	if err != nil || backend.Verify(context.Background()) != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := backend.StatPoint(context.Background(), "point-absent"); !os.IsNotExist(err) {
		t.Fatalf("missing point: %v", err)
	}
	if checked, err := TestPoolBackupRepositoryAccess(s, saved.ID); err != nil || checked.Access.Status != "ok" {
		t.Fatalf("test access: %#v %v", checked.Access, err)
	}
	replayed, err := prepareBackupRepositoryForRegistration(context.Background(), s, repo, "register-1")
	if err != nil || *replayed.RemoteIdentity != *saved.RemoteIdentity {
		t.Fatalf("exact replay: %#v %v", replayed.RemoteIdentity, err)
	}
	adopted, err := prepareBackupRepositoryForAdoption(context.Background(), s, saved)
	if err != nil || !preparedBackupRepositoryMatches(saved, adopted) {
		t.Fatalf("adoption: %#v %v", adopted, err)
	}
	wrong := saved
	wrong.RemoteIdentity = &protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("0", 64)}
	if _, err := prepareS3BackupRepository(context.Background(), s, wrong, handoff.Credentials); err == nil {
		t.Fatal("wrong durable identity accepted")
	}
}

func writeS3Stage(t *testing.T, payload []byte) string {
	t.Helper()
	stage := filepath.Join(t.TempDir(), "disk.qcow2")
	if err := os.WriteFile(stage, payload, 0600); err != nil {
		t.Fatal(err)
	}
	return stage
}

// publishDeniedS3Point uploads a point's artifacts and intent while the final
// manifest PUT is denied, then allows it again.
func publishDeniedS3Point(t *testing.T, b *s3BackupStorage, fake *fakeS3, payload []byte) protocol.BackupManifest {
	t.Helper()
	manifest := testS3Manifest(payload)
	fake.denyManifestPut = true
	if _, err := b.Publish(context.Background(), manifest, map[string]string{"disk.qcow2": writeS3Stage(t, payload)}); err == nil {
		t.Fatal("initial final denial missing")
	}
	fake.denyManifestPut = false
	return manifest
}

func testS3Manifest(payload []byte) protocol.BackupManifest {
	digest := sha256.Sum256(payload)
	return protocol.BackupManifest{Version: protocol.BackupManifestVersion, RecoveryPointID: "point-1", RepositoryID: "repo-1",
		PoolID: "pool-1", SourceVMID: "vm-1", CapturedAt: time.Now().UTC(), Consistency: protocol.BackupCaptureStopped,
		SpecSHA256: strings.Repeat("a", 64), DiskIdentitySHA256: strings.Repeat("b", 64), Complete: true, Success: 1,
		Disks: []protocol.BackupDiskArtifact{{Target: "vda", Filename: "disk.qcow2", Format: "qcow2", Size: int64(len(payload)), SHA256: hex.EncodeToString(digest[:])}}}
}

func catalogS3Point(t *testing.T, b *s3BackupStorage, manifest protocol.BackupManifest) {
	t.Helper()
	_, digest, err := b.StatPoint(context.Background(), manifest.RecoveryPointID)
	if err != nil {
		t.Fatal(err)
	}
	point := protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID,
		PoolID: manifest.PoolID, SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt,
		Consistency: manifest.Consistency, ManifestSHA256: digest}
	if err := savePoolBackupCatalogEntry(b.s, point); err != nil {
		t.Fatal(err)
	}
}

func TestS3BackupPublicationAndCorruption(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	if err := b.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	identity, err := b.Identity(ctx)
	if err != nil || identity.Fingerprint == "" {
		t.Fatalf("identity: %#v %v", identity, err)
	}
	b.repo.RemoteIdentity = &identity
	payload := []byte("standalone disk bytes")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	catalogS3Point(t, b, manifest)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatalf("exact retry: %v", err)
	}
	if got, err := b.Scan(ctx); err != nil || len(got) != 1 {
		t.Fatalf("scan: %#v %v", got, err)
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, true); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("protected point deleted")
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, false); err != nil {
		t.Fatal(err)
	}
	if err := b.CopyArtifacts(ctx, manifest.RecoveryPointID, t.TempDir(), manifest); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.objects[b.pointKey(manifest.RecoveryPointID, "disk.qcow2")] = []byte("corrupt")
	fake.mu.Unlock()
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("corrupt artifact accepted")
	}
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("corrupt existing point accepted")
	}
}

func TestS3CopyArtifactsCreatesRestoreDirectory(t *testing.T) {
	b, _ := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("restorable disk")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Join(t.TempDir(), "backup-restore", "restore-operation")
	if err := b.CopyArtifacts(ctx, manifest.RecoveryPointID, localDir, manifest); err != nil {
		t.Fatalf("copy into fresh restore directory: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(localDir, "disk.qcow2"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("restored artifact: %v", err)
	}
	if err := b.CopyArtifacts(ctx, manifest.RecoveryPointID, localDir, manifest); err != nil {
		t.Fatalf("exact restore copy retry: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "disk.qcow2"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := b.CopyArtifacts(ctx, manifest.RecoveryPointID, localDir, manifest); err != nil {
		t.Fatalf("corrupt local copy recovery: %v", err)
	}
	got, err = os.ReadFile(filepath.Join(localDir, "disk.qcow2"))
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("recovered artifact: %v", err)
	}
}

func TestS3BackupIncompleteCollisionAndIdentity(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	manifest := testS3Manifest([]byte("disk"))
	raw, _ := json.Marshal(manifest)
	fake.mu.Lock()
	fake.objects[b.pointKey(manifest.RecoveryPointID, "manifest.json")] = raw
	fake.mu.Unlock()
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("incomplete point accepted")
	}
	if _, err := b.Publish(ctx, manifest, nil); err == nil {
		t.Fatal("incomplete collision accepted")
	}
	wrong := protocol.BackupRemoteIdentity{Fingerprint: strings.Repeat("0", 64)}
	b.repo.RemoteIdentity = &wrong
	if err := b.Verify(ctx); err == nil {
		t.Fatal("wrong repository identity accepted")
	}
	if err := b.Probe(ctx); err == nil {
		t.Fatal("wrong identity probed")
	}
	fake.mu.Lock()
	fake.objects[b.key(s3BackupOwnerKey)] = []byte("wrong owner")
	fake.mu.Unlock()
	if err := b.Verify(ctx); err == nil {
		t.Fatal("wrong owner accepted")
	}
}

func TestS3ProbeRejectsMissingConditionalCreate(t *testing.T) {
	b, fake := testS3Storage(t)
	fake.ignoreConditional = true
	if err := b.Probe(context.Background()); err == nil {
		t.Fatal("unconditional overwrite accepted")
	}
}

func TestS3ProbeRejectsMissingConditionalDelete(t *testing.T) {
	b, fake := testS3Storage(t)
	fake.ignoreConditionalDelete = true
	if err := b.Probe(context.Background()); err == nil {
		t.Fatal("unconditional delete accepted")
	}
}

func TestS3AbortIncompletePublication(t *testing.T) {
	for _, scenario := range []string{"denied", "interrupted", "unknown", "mutated", "published", "lost-reply"} {
		t.Run(scenario, func(t *testing.T) {
			b, fake := testS3Storage(t)
			ctx := context.Background()
			payload := []byte("unpublished disk bytes")
			manifest := testS3Manifest(payload)
			stage := writeS3Stage(t, payload)
			fake.denyManifestPut = scenario != "published" && scenario != "lost-reply"
			fake.loseManifestReply = scenario == "lost-reply"
			if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil && scenario != "published" {
				t.Fatal("publication failure not reported")
			}
			fake.denyManifestPut = false // The operator restores write permission before cleanup.
			id := manifest.RecoveryPointID
			artifact := b.pointKey(id, "disk.qcow2")
			if scenario == "lost-reply" {
				if _, digest, err := b.StatPoint(ctx, id); err != nil || digest == "" {
					t.Fatalf("lost reply did not leave exact published point: %v", err)
				}
				if err := b.AbortIncomplete(ctx, id); err == nil {
					t.Fatal("published point aborted")
				}
				return
			}
			if scenario == "published" {
				catalogS3Point(t, b, manifest)
				if err := b.AbortIncomplete(ctx, id); err == nil {
					t.Fatal("cataloged point aborted")
				}
				return
			}
			fake.mu.Lock()
			switch scenario {
			case "unknown":
				fake.objects[b.pointKey(id, "foreign")] = []byte("foreign")
			case "mutated":
				fake.objects[artifact] = []byte("changed")
			case "interrupted":
				fake.failArtifactDeleteOnce = true
			}
			fake.mu.Unlock()
			if scenario == "unknown" || scenario == "mutated" || scenario == "interrupted" {
				if err := b.AbortIncomplete(ctx, id); err == nil {
					t.Fatal("unsafe or interrupted cleanup succeeded")
				}
				fake.mu.Lock()
				_, exists := fake.objects[artifact]
				fake.mu.Unlock()
				if !exists {
					t.Fatal("artifact removed before safe settlement")
				}
				if scenario != "interrupted" {
					return
				}
			}
			if err := b.AbortIncomplete(ctx, id); err != nil {
				t.Fatalf("exact abort retry: %v", err)
			}
			if err := b.AbortIncomplete(ctx, id); err != nil {
				t.Fatalf("idempotent abort: %v", err)
			}
			fake.mu.Lock()
			stored := bytes.Clone(fake.objects[artifact])
			fake.mu.Unlock()
			if !bytes.Equal(stored, b.artifactTombstone(id, "disk.qcow2", manifest.Disks[0])) {
				t.Fatal("artifact fence is absent")
			}
		})
	}
}

func TestS3ProbeRejectsMissingMultipartConditionalCreate(t *testing.T) {
	b, fake := testS3Storage(t)
	fake.ignoreMultipartConditional = true
	if err := b.Probe(context.Background()); err == nil {
		t.Fatal("unconditional multipart overwrite accepted")
	}
}

func TestS3ProbeCachesMultipartProofUntilExpiry(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	if err := b.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	first := fake.multipartStarts
	fake.mu.Unlock()
	if first != 2 {
		t.Fatalf("first probe started %d multipart uploads", first)
	}
	reopened := *b
	if err := reopened.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	second := fake.multipartStarts
	fake.mu.Unlock()
	if second != first {
		t.Fatalf("routine probe repeated multipart transfer: %d -> %d", first, second)
	}
	s3MultipartProof.Store(b.multipartProofKey(), time.Now().Add(-s3MultipartProofMaxAge-time.Second))
	if err := reopened.ensureMultipartProfile(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	third := fake.multipartStarts
	fake.mu.Unlock()
	if third != first+2 {
		t.Fatalf("expired proof was not revalidated: %d -> %d", first, third)
	}
	fake.mu.Lock()
	fake.ignoreConditional = true
	fake.mu.Unlock()
	if err := reopened.Probe(ctx); err == nil {
		t.Fatal("degraded routine probe passed")
	}
	if _, ok := s3MultipartProof.Load(b.multipartProofKey()); ok {
		t.Fatal("failed routine probe retained profile proof")
	}
	fake.mu.Lock()
	fake.ignoreConditional = false
	fake.mu.Unlock()
	if err := reopened.ensureMultipartProfile(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fourth := fake.multipartStarts
	fake.mu.Unlock()
	if fourth != third+2 {
		t.Fatalf("failed health probe did not force revalidation: %d -> %d", third, fourth)
	}
}

func TestS3BackupRefusesRestrictedAuthority(t *testing.T) {
	b, fake := testS3Storage(t)
	if err := b.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}
	poolPeerMu.Lock()
	state, err := loadPoolPeer(b.s)
	if err == nil {
		state.Restricted = &poolRestrictedRecord{Kind: poolRestrictedUnconfirmed, Reason: poolUnconfirmedReason, Since: time.Now().UTC()}
		err = savePoolPeer(b.s, state)
	}
	poolPeerMu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	stage := writeS3Stage(t, []byte("disk"))
	if _, err := b.Publish(context.Background(), testS3Manifest([]byte("disk")), map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("restricted writer published")
	}
	fake.mu.Lock()
	_, published := fake.objects[b.pointKey("point-1", "manifest.json")]
	fake.mu.Unlock()
	if published {
		t.Fatal("restricted writer left a published manifest")
	}
}

func TestS3BackupDeleteRequiresExactPoint(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	catalogS3Point(t, b, manifest)
	changed := manifest
	changed.SourceVMName = "different"
	if _, err := b.Publish(ctx, changed, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("manifest collision accepted")
	}
	if err := putBackupRestorePin(b.s, protocol.BackupRestorePin{RecoveryPointID: manifest.RecoveryPointID,
		OperationID: "restore-1", Kind: protocol.BackupPinKindNew, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("pinned point deleted")
	}
	if err := clearBackupRestorePin(b.s, manifest.RecoveryPointID, "restore-1"); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.objects[b.pointKey(manifest.RecoveryPointID, "unexpected")] = []byte("foreign")
	fake.mu.Unlock()
	if err := b.Delete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("unexpected object deleted")
	}
	fake.mu.Lock()
	delete(fake.objects, b.pointKey(manifest.RecoveryPointID, "unexpected"))
	fake.mu.Unlock()
	if err := b.Delete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	if keys, err := b.list(ctx, b.pointKey(manifest.RecoveryPointID, "")); err != nil || len(keys) != 0 {
		t.Fatalf("point remained: %#v %v", keys, err)
	}
}

func TestS3BackupDeleteResumesAfterLostManifestReply(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	catalogS3Point(t, b, manifest)
	fake.mu.Lock()
	fake.failManifestDelete = true
	fake.mu.Unlock()
	if err := b.Delete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("lost manifest reply reported success")
	} else {
		t.Logf("injected delete error: %v", err)
	}
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("deleting point remained restorable")
	}
	fake.mu.Lock()
	recordKey := b.pointKey(manifest.RecoveryPointID, s3BackupDeleteKey)
	originalRecord, record := fake.objects[recordKey]
	_, artifact := fake.objects[b.pointKey(manifest.RecoveryPointID, "disk.qcow2")]
	fake.failManifestDelete = false
	fake.mu.Unlock()
	if !record || !artifact {
		t.Fatalf("deletion lost its resumable evidence or artifact: record=%t artifact=%t", record, artifact)
	}
	if _, err := b.Entries(ctx); err == nil {
		t.Fatal("entries hid interrupted prune")
	}
	if _, err := b.Scan(ctx); err == nil {
		t.Fatal("scan hid interrupted prune")
	}
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("tombstoned point was republished")
	}
	var tampered s3BackupDeleteRecord
	if err := json.Unmarshal(originalRecord, &tampered); err != nil {
		t.Fatal(err)
	}
	foreignKey := b.pointKey(manifest.RecoveryPointID, "foreign")
	tampered.Keys = append(tampered.Keys, foreignKey)
	sort.Strings(tampered.Keys)
	tamperedRaw, _ := json.Marshal(tampered)
	fake.mu.Lock()
	fake.objects[recordKey] = tamperedRaw
	fake.objects[foreignKey] = []byte("foreign")
	fake.mu.Unlock()
	if err := b.Delete(ctx, manifest.RecoveryPointID); err == nil {
		t.Fatal("tampered deletion record accepted")
	}
	fake.mu.Lock()
	_, foreignSurvived := fake.objects[foreignKey]
	fake.objects[recordKey] = originalRecord
	delete(fake.objects, foreignKey)
	fake.mu.Unlock()
	if !foreignSurvived {
		t.Fatal("tampered record deleted a foreign object")
	}
	if err := b.Delete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if keys, err := b.list(ctx, b.pointKey(manifest.RecoveryPointID, "")); err != nil || len(keys) != 0 {
		t.Fatalf("point remained: %#v %v", keys, err)
	}
	if err := b.Delete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatalf("settled retry: %v", err)
	}
}

func TestS3MultipartFailureAbortsAndRetryPublishes(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := make([]byte, 5<<20+1)
	for i := range payload {
		payload[i] = byte(i)
	}
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	fake.mu.Lock()
	fake.failPart = true
	fake.mu.Unlock()
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("failed multipart published")
	}
	fake.mu.Lock()
	aborted, pending := fake.aborts, len(fake.uploads)
	_, published := fake.objects[b.pointKey(manifest.RecoveryPointID, "manifest.json")]
	fake.failPart = false
	fake.mu.Unlock()
	if aborted == 0 || pending != 0 || published {
		t.Fatalf("multipart failure not cleaned: aborts=%d pending=%d published=%t", aborted, pending, published)
	}
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		fake.mu.Lock()
		size := len(fake.objects[b.pointKey(manifest.RecoveryPointID, "disk.qcow2")])
		pending := len(fake.uploads)
		fake.mu.Unlock()
		t.Fatalf("multipart retry: %v size=%d pending=%d", err, size, pending)
	}
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatal(err)
	}
}

// Regression for BUG-0716: prune, protection, rebuild and publication must not
// re-download artifacts that publication already verified before commit.
func TestS3MetadataOperationsSkipArtifactDownloads(t *testing.T) {
	b, fake := testS3Storage(t)
	var downloads atomic.Int32
	fake.onRequest = func(r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/disk.qcow2") {
			downloads.Add(1)
		}
	}
	ctx := context.Background()
	payload := []byte("disk bytes")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	digest, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage})
	if err != nil {
		t.Fatal(err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("publication downloaded the artifact %d times", got)
	}
	if err := savePoolBackupCatalogEntry(b.s, protocol.BackupRecoveryPoint{ID: manifest.RecoveryPointID, RepositoryID: manifest.RepositoryID,
		PoolID: manifest.PoolID, SourceVMID: manifest.SourceVMID, CapturedAt: manifest.CapturedAt,
		Consistency: manifest.Consistency, ManifestSHA256: digest}); err != nil {
		t.Fatal(err)
	}
	if points, err := b.Scan(ctx); err != nil || len(points) != 1 || points[0].ManifestSHA256 != digest {
		t.Fatalf("scan: %#v %v", points, err)
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, false); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	if got := downloads.Load(); got != 1 {
		t.Fatalf("metadata operations downloaded the artifact %d more times", got-1)
	}
}

// Regression for BUG-0716: exact-content artifact reads during abort must not
// hold poolPeerMu, which every pool membership read waits on.
func TestS3AbortReadsArtifactsWithoutPoolPeerLock(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("unpublished disk bytes")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	fake.denyManifestPut = true
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("publication failure not reported")
	}
	fake.denyManifestPut = false
	var block atomic.Bool
	entered, release := make(chan struct{}), make(chan struct{})
	fake.onRequest = func(r *http.Request) {
		if r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/disk.qcow2") && block.CompareAndSwap(true, false) {
			close(entered)
			<-release // a stalled remote mid-artifact read
		}
	}
	block.Store(true)
	done := make(chan error, 1)
	go func() { done <- b.AbortIncomplete(ctx, manifest.RecoveryPointID) }()
	<-entered
	member := make(chan struct{})
	go func() { Membership(b.s); close(member) }()
	select {
	case <-member:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Membership blocked on poolPeerMu while abort read an artifact")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Regression for BUG-0716: a silent S3 service must fail a request, not hang it.
func TestS3ClientTimesOutStalledResponse(t *testing.T) {
	stalled := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-stalled }))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stalled) })
	oldTimeout, oldClient := s3BackupReadTimeout, newBackupS3Client
	s3BackupReadTimeout = 200 * time.Millisecond
	newBackupS3Client = func(options s3.Options) *s3.Client {
		trusted := server.Client().Transport.(*http.Transport).TLSClientConfig
		options.HTTPClient = options.HTTPClient.(*awshttp.BuildableClient).WithTransportOptions(func(t *http.Transport) { t.TLSClientConfig = trusted })
		options.RetryMaxAttempts = 1
		return s3.New(options)
	}
	t.Cleanup(func() { s3BackupReadTimeout, newBackupS3Client = oldTimeout, oldClient })
	repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Test", Kind: protocol.BackupRepoKindS3,
		S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: "pool-1"}, CredentialRef: "key-1"}
	b, err := newS3BackupStorageWithCredential(seedBackupMaster(t), repo, protocol.StorageCredentials{Username: "k", Secret: "s"})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := b.Verify(context.Background()); err == nil || time.Since(started) > 5*time.Second {
		t.Fatalf("stalled S3 response was not bounded: %v after %s", err, time.Since(started))
	}
}

// Regression for BUG-0740: a response read waiting behind a slow but
// progressing upload stays alive; a connection that stops moving fails.
func TestS3ConnectionFailsOnlyWithoutProgress(t *testing.T) {
	oldTimeout := s3BackupReadTimeout
	s3BackupReadTimeout = 100 * time.Millisecond
	t.Cleanup(func() { s3BackupReadTimeout = oldTimeout })
	local, remote := net.Pipe()
	defer local.Close()
	defer remote.Close()
	conn := progressConn{local, s3BackupReadTimeout}
	response := make(chan error, 1)
	go func() {
		_, err := conn.Read(make([]byte, 1))
		response <- err
	}()
	go io.Copy(io.Discard, remote)
	for until := time.Now().Add(500 * time.Millisecond); time.Now().Before(until); time.Sleep(30 * time.Millisecond) {
		if _, err := conn.Write([]byte("part")); err != nil {
			t.Fatal("progressing upload failed:", err)
		}
		select {
		case err := <-response:
			t.Fatal("response read timed out during a progressing upload:", err)
		default:
		}
	}
	select {
	case err := <-response:
		if err == nil {
			t.Fatal("silent connection returned data")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("silent connection was not bounded")
	}
}

func publishS3TestPoint(t *testing.T, b *s3BackupStorage, payload []byte) protocol.BackupManifest {
	t.Helper()
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(context.Background(), manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	return manifest
}

// Regression for BUG-0720: a prune interrupted after it removed the point
// must not block catalog rebuild, which cannot resume it without a catalog entry.
func TestS3EntriesAcceptsPruneThatRemovedThePoint(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	manifest := publishS3TestPoint(t, b, []byte("disk"))
	id := manifest.RecoveryPointID
	fake.mu.Lock()
	manifestRaw := fake.objects[b.pointKey(id, "manifest.json")]
	fake.mu.Unlock()
	record, _ := json.Marshal(s3BackupDeleteRecord{s3BackupManifestRecord{Repository: b.repo.ID, Pool: b.repo.PoolID, Point: id,
		ManifestSHA: sha256Hex(manifestRaw), Manifest: manifestRaw}, b.manifestKeys(manifest)})
	fake.mu.Lock()
	fake.objects[b.pointKey(id, s3BackupDeleteKey)] = record
	delete(fake.objects, b.pointKey(id, "manifest.json"))
	fake.mu.Unlock()
	if _, err := b.Entries(ctx); err == nil {
		t.Fatal("rebuild ignored a prune that still holds point objects")
	}
	fake.mu.Lock()
	delete(fake.objects, b.pointKey(id, "disk.qcow2"))
	fake.mu.Unlock()
	if names, err := b.Entries(ctx); err != nil || len(names) != 0 {
		t.Fatalf("finished prune blocked rebuild: %v %v", names, err)
	}
	if points, err := b.Scan(ctx); err != nil || len(points) != 0 {
		t.Fatalf("finished prune blocked scan: %v %v", points, err)
	}
}

// Regression for BUG-0720: abort and delete records are recognised by their own
// raw digest, not by re-validating the captured manifest after an upgrade.
func TestS3RecordsSurviveManifestValidationChange(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	manifest := testS3Manifest([]byte("disk"))
	manifest.SpecSHA256 = "digest-from-an-older-schema" // Valid() now rejects the bound manifest.
	raw, _ := json.Marshal(manifest)
	fake.mu.Lock()
	fake.objects[b.pointKey(manifest.RecoveryPointID, "manifest.json")] = b.abortTombstone(manifest.RecoveryPointID, raw)
	fake.mu.Unlock()
	if names, err := b.Entries(ctx); err != nil || len(names) != 0 {
		t.Fatalf("old abort tombstone blocked rebuild: %v %v", names, err)
	}
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old abort tombstone read as a point: %v", err)
	}
}

func testS3Registration(t *testing.T, prefix string) (*Store, *fakeS3, protocol.BackupRepository) {
	t.Helper()
	fake := &fakeS3{objects: map[string][]byte{}}
	server := httptest.NewTLSServer(fake)
	t.Cleanup(server.Close)
	previous := newBackupS3Client
	newBackupS3Client = func(options s3.Options) *s3.Client {
		options.HTTPClient = server.Client()
		return s3.New(options)
	}
	t.Cleanup(func() { newBackupS3Client = previous })
	s := seedBackupMaster(t)
	return s, fake, protocol.BackupRepository{ID: "repo-1", PoolID: s.State.PoolID, Name: "Test", Kind: protocol.BackupRepoKindS3,
		S3Source: &protocol.BackupS3Source{Endpoint: server.URL, Bucket: "backups", Prefix: prefix}, CredentialRef: "key-1"}
}

var testS3Credential = protocol.StorageCredentials{Username: "test-key", Secret: "test-secret"}

// Regression for BUG-0720: a registration refused by its probe releases the
// prefix, so the corrected retry (with a new repository ID) can claim it.
func TestS3RefusedRegistrationReleasesOwner(t *testing.T) {
	s, fake, repo := testS3Registration(t, "pool-1")
	ctx := context.Background()
	fake.objects["pool-1/"] = nil // A console-created folder holds no data.
	fake.ignoreConditionalDelete = true
	if _, err := prepareS3BackupRepository(ctx, s, repo, testS3Credential); err == nil {
		t.Fatal("service without conditional delete registered")
	}
	fake.mu.Lock()
	_, owned := fake.objects["pool-1/"+s3BackupOwnerKey]
	fake.objects["pool-1/.nex-probe-leftover"] = []byte("nex backup probe")
	fake.ignoreConditionalDelete = false
	fake.mu.Unlock()
	if owned {
		t.Fatal("refused registration kept its owner claim")
	}
	repo.ID = "repo-2"
	if _, err := prepareS3BackupRepository(ctx, s, repo, testS3Credential); err != nil {
		t.Fatalf("corrected registration refused: %v", err)
	}
}

// Regression for BUG-0720: a new repository may not sit inside another one's
// prefix, and "." may not alias the bucket root.
func TestS3RegistrationRefusesNestedPrefix(t *testing.T) {
	s, _, repo := testS3Registration(t, "pool-1")
	ctx := context.Background()
	if _, err := prepareS3BackupRepository(ctx, s, repo, testS3Credential); err != nil {
		t.Fatal(err)
	}
	nested := repo
	nested.ID, nested.S3Source = "repo-2", &protocol.BackupS3Source{Endpoint: repo.S3Source.Endpoint, Bucket: "backups", Prefix: "pool-1/child/deeper"}
	if _, err := prepareS3BackupRepository(ctx, s, nested, testS3Credential); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("nested prefix registered: %v", err)
	}
	dot := repo
	dot.ID, dot.S3Source = "repo-3", &protocol.BackupS3Source{Endpoint: repo.S3Source.Endpoint, Bucket: "backups", Prefix: "."}
	if _, err := prepareS3BackupRepository(ctx, s, dot, testS3Credential); !errors.Is(err, ErrBackupInvalid) {
		t.Fatalf("dot prefix registered: %v", err)
	}
}

// Regression for BUG-0720: member dispatch prepares an adopted repository on
// every request, so it must reuse the multipart proof instead of re-uploading.
func TestS3AdoptionReusesMultipartProof(t *testing.T) {
	s, fake, repo := testS3Registration(t, "pool-1")
	ctx := context.Background()
	prepared, err := prepareS3BackupRepository(ctx, s, repo, testS3Credential)
	if err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	registered := fake.multipartStarts
	fake.mu.Unlock()
	for range 2 {
		if _, err := prepareS3BackupRepository(ctx, s, prepared, testS3Credential); err != nil {
			t.Fatal(err)
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.multipartStarts != registered {
		t.Fatalf("adoption repeated the multipart probe: %d -> %d", registered, fake.multipartStarts)
	}
}

// Regression for BUG-0720: a probe cut off by its caller's deadline aborts its
// multipart upload with a fresh context instead of leaking billed parts.
func TestS3ProbeAbortsInterruptedMultipart(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fake.onRequest = func(r *http.Request) {
		if r.Method == http.MethodPut && r.URL.Query().Has("partNumber") {
			cancel()
		}
	}
	if err := b.Probe(ctx); err == nil {
		t.Fatal("interrupted probe passed")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.uploads) != 0 {
		t.Fatalf("interrupted probe leaked multipart uploads: %d", len(fake.uploads))
	}
}

// Regression for BUG-0720: reconciliation aborts multipart uploads a crashed
// publisher left under the point, and a foreign artifact is a conflict.
func TestS3AbortIncompleteAbortsOrphanMultipart(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	fake.denyManifestPut = true
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err == nil {
		t.Fatal("denied publication succeeded")
	}
	fake.mu.Lock()
	fake.denyManifestPut = false
	fake.uploads = map[string]map[int][]byte{b.pointKey(manifest.RecoveryPointID, "firmware.bin"): {1: []byte("part")}}
	fake.mu.Unlock()
	if err := b.AbortIncomplete(ctx, manifest.RecoveryPointID); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.uploads) != 0 {
		t.Fatal("orphan multipart upload survived reconciliation")
	}
}

// Regression for BUG-0720: a different object at an artifact key is a conflict,
// found before the upload, and a failed marker read is not read as absence.
func TestS3PublishReportsConflictsAndMarkerReadErrors(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	artifact := b.pointKey(manifest.RecoveryPointID, "disk.qcow2")
	var uploads atomic.Int32
	fake.onRequest = func(r *http.Request) {
		if r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/disk.qcow2") {
			uploads.Add(1)
		}
	}
	if err := b.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.objects[artifact] = []byte("othr")
	fake.mu.Unlock()
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("conflicting artifact: %v", err)
	}
	if uploads.Load() != 0 {
		t.Fatal("conflicting artifact was re-uploaded")
	}
	fake.mu.Lock()
	fake.denyGet = b.key(s3BackupIntentPrefix + manifest.RecoveryPointID)
	fake.mu.Unlock()
	if _, _, err := b.StatPoint(ctx, manifest.RecoveryPointID); err == nil || errors.Is(err, ErrBackupConflict) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unreadable intent was treated as settled: %v", err)
	}
}

// A stalled unprotect must not delete a marker written after it read the old one.
func TestS3StaleUnprotectKeepsNewerMarker(t *testing.T) {
	b, fake := testS3Storage(t)
	ctx := context.Background()
	payload := []byte("disk for protection race")
	manifest := testS3Manifest(payload)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, true); err != nil {
		t.Fatal(err)
	}
	key := b.pointKey(manifest.RecoveryPointID, ".nex-protected")
	newer := newBackupProtectMarker(manifest.RecoveryPointID)
	fake.onRequest = func(r *http.Request) {
		if r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/.nex-protected") {
			fake.mu.Lock()
			fake.objects[strings.TrimPrefix(r.URL.Path, "/backups/")] = newer
			fake.mu.Unlock()
		}
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, false); !errors.Is(err, ErrBackupConflict) {
		t.Fatal("stale unprotect was not refused:", err)
	}
	fake.mu.Lock()
	got := fake.objects[key]
	fake.mu.Unlock()
	if !bytes.Equal(got, newer) {
		t.Fatalf("newer marker was lost: %q", got)
	}
	fake.onRequest = nil
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, false); err != nil {
		t.Fatal(err)
	}
	if protected, err := b.Protected(ctx, manifest.RecoveryPointID); err != nil || protected {
		t.Fatal("unprotect did not remove the marker:", protected, err)
	}
}

func TestS3PublishReportsProviderRefusal(t *testing.T) {
	b, fake := testS3Storage(t)
	payload := []byte("disk the provider refuses")
	stage := writeS3Stage(t, payload)
	fake.rejectArtifactPut = true
	_, err := b.Publish(context.Background(), testS3Manifest(payload), map[string]string{"disk.qcow2": stage})
	if err == nil || !strings.Contains(err.Error(), "upload disk.qcow2 to S3") || !strings.Contains(err.Error(), "EntityTooLarge") {
		t.Fatalf("provider refusal is not in the error: %v", err)
	}
}

func TestS3ClientSignsWithConfiguredRegion(t *testing.T) {
	previous := newBackupS3Client
	t.Cleanup(func() { newBackupS3Client = previous })
	var regions []string
	newBackupS3Client = func(options s3.Options) *s3.Client {
		regions = append(regions, options.Region)
		return s3.New(options)
	}
	for _, region := range []string{"", "eu-west-2"} {
		repo := protocol.BackupRepository{ID: "repo-1", PoolID: "pool-1", Name: "Test", Kind: protocol.BackupRepoKindS3, CredentialRef: "key-1",
			S3Source: &protocol.BackupS3Source{Endpoint: "https://s3.example.test", Bucket: "backups", Region: region}}
		credential := protocol.StorageCredentials{Username: "key", Secret: "secret"}
		if _, err := newS3BackupStorageWithCredential(seedBackupMaster(t), repo, credential); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(regions, []string{"us-east-1", "eu-west-2"}) {
		t.Fatalf("signing regions %v", regions)
	}
}

// Regression for BUG-0751: every backend reads the marker of a maximum-length point ID.
func TestS3ProtectsMaximumLengthPointID(t *testing.T) {
	b, _ := testS3Storage(t)
	ctx := context.Background()
	if err := b.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	identity, err := b.Identity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b.repo.RemoteIdentity = &identity
	payload := []byte("disk")
	manifest := testS3Manifest(payload)
	manifest.RecoveryPointID = strings.Repeat("a", 64)
	stage := writeS3Stage(t, payload)
	if _, err := b.Publish(ctx, manifest, map[string]string{"disk.qcow2": stage}); err != nil {
		t.Fatal(err)
	}
	if err := b.SetProtected(ctx, manifest.RecoveryPointID, true); err != nil {
		t.Fatal(err)
	}
	if protected, err := b.Protected(ctx, manifest.RecoveryPointID); err != nil || !protected {
		t.Fatalf("maximum-length marker unreadable: %t %v", protected, err)
	}
}
