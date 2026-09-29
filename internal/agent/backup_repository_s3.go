// SPDX-License-Identifier: AGPL-3.0-only
package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"nex.local/nex/internal/protocol"
	"nex.local/nex/internal/vmhost"
)

const s3BackupOwnerKey = ".nex-owner.json"
const s3BackupDeleteKey = ".nex-delete.json"
const s3BackupIntentPrefix = ".nex-intent-"
const s3BackupAbortPrefix = ".nex-abort-"
const s3MultipartProofMaxAge = time.Hour

// Process-local proof expires; restart or credential/repository generation change rechecks it.
var s3MultipartProof sync.Map

type s3BackupDeleteRecord struct {
	Repository  string          `json:"repository"`
	Pool        string          `json:"pool"`
	Point       string          `json:"point"`
	ManifestSHA string          `json:"manifest_sha256"`
	Manifest    json.RawMessage `json:"manifest"`
	Keys        []string        `json:"keys"`
}

type s3BackupAbortTombstone struct {
	Repository  string          `json:"repository"`
	Pool        string          `json:"pool"`
	Point       string          `json:"point"`
	ManifestSHA string          `json:"manifest_sha256"`
	Manifest    json.RawMessage `json:"manifest"`
}

type s3BackupArtifactTombstone struct {
	Repository string `json:"repository"`
	Pool       string `json:"pool"`
	Point      string `json:"point"`
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	SHA256     string `json:"sha256"`
}

type s3BackupStorage struct {
	s      *Store
	repo   protocol.BackupRepository
	client *s3.Client
	root   string
}

type s3BackupOwner struct {
	Repository string `json:"repository"`
	Pool       string `json:"pool"`
	Endpoint   string `json:"endpoint"`
	Bucket     string `json:"bucket"`
	Prefix     string `json:"prefix"`
}

var newBackupS3Client = func(options s3.Options) *s3.Client { return s3.New(options) }

func newS3BackupRepositoryStorage(s *Store, repo protocol.BackupRepository) (backupRepositoryStorage, error) {
	credential, err := ResolveBackupCredential(s, repo)
	if err != nil {
		return nil, err
	}
	return newS3BackupStorageWithCredential(s, repo, credential)
}

func newS3BackupStorageWithCredential(s *Store, repo protocol.BackupRepository, credential protocol.StorageCredentials) (*s3BackupStorage, error) {
	if s == nil || repo.Kind != protocol.BackupRepoKindS3 || repo.S3Source == nil || repo.Valid() != nil || repo.CredentialRef == "" {
		return nil, ErrBackupInvalid
	}
	if credential.Username == "" || credential.Secret == "" {
		return nil, ErrBackupInvalid
	}
	client := newBackupS3Client(s3.Options{
		Region: "us-east-1", BaseEndpoint: aws.String(repo.S3Source.Endpoint), UsePathStyle: true,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: credential.Username, SecretAccessKey: credential.Secret}, nil
		}),
	})
	root := repo.S3Source.Prefix
	if root != "" {
		root += "/"
	}
	return &s3BackupStorage{s: s, repo: repo, client: client, root: root}, nil
}

func prepareS3BackupRepository(ctx context.Context, s *Store, repo protocol.BackupRepository, credential protocol.StorageCredentials) (protocol.BackupRepository, error) {
	b, err := newS3BackupStorageWithCredential(s, repo, credential)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	identity, err := b.Identity(ctx)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	if repo.RemoteIdentity != nil && *repo.RemoteIdentity != identity {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	repo.RemoteIdentity = &identity
	b.repo = repo
	// Registration and authority adoption must prove the current service profile
	// before the repository can enter Core's durable inventory.
	s3MultipartProof.Delete(b.multipartProofKey())
	if err := b.Probe(ctx); err != nil {
		return protocol.BackupRepository{}, err
	}
	return repo, nil
}

func (b *s3BackupStorage) key(name string) string { return b.root + name }
func (b *s3BackupStorage) pointKey(pointID, name string) string {
	return b.key(pointID + "/" + name)
}

func (b *s3BackupStorage) ownerBytes() []byte {
	raw, _ := json.Marshal(s3BackupOwner{Repository: b.repo.ID, Pool: b.repo.PoolID,
		Endpoint: b.repo.S3Source.Endpoint, Bucket: b.repo.S3Source.Bucket, Prefix: b.repo.S3Source.Prefix})
	return raw
}

// Identity binds the Core record to this exact TLS endpoint, bucket, prefix and owner.
func (b *s3BackupStorage) Identity(ctx context.Context) (protocol.BackupRemoteIdentity, error) {
	if err := b.verifyOwner(ctx); err != nil {
		return protocol.BackupRemoteIdentity{}, err
	}
	sum := sha256.Sum256(b.ownerBytes())
	return protocol.BackupRemoteIdentity{Fingerprint: hex.EncodeToString(sum[:])}, nil
}

func (b *s3BackupStorage) Verify(ctx context.Context) error {
	identity, err := b.Identity(ctx)
	if err != nil {
		return err
	}
	if b.repo.RemoteIdentity != nil && *b.repo.RemoteIdentity != identity {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) verifyOwner(ctx context.Context) error {
	key := b.key(s3BackupOwnerKey)
	raw, err := b.read(ctx, key, 4096)
	if isS3Code(err, "NoSuchKey", "NotFound") && b.repo.RemoteIdentity == nil {
		if err := requirePoolAuthorityWritable(b.s); err != nil {
			return err
		}
		keys, listErr := b.list(ctx, b.root)
		if listErr != nil {
			return listErr
		}
		if len(keys) != 0 {
			return ErrBackupConflict
		}
		_, putErr := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
			Key: aws.String(key), Body: bytes.NewReader(b.ownerBytes()), IfNoneMatch: aws.String("*")})
		if putErr != nil && !isS3Code(putErr, "PreconditionFailed", "ConditionalRequestConflict") {
			return putErr
		}
		raw, err = b.read(ctx, key, 4096)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, b.ownerBytes()) {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) Probe(ctx context.Context) (probeErr error) {
	defer func() {
		if probeErr != nil {
			s3MultipartProof.Delete(b.multipartProofKey())
		}
	}()
	if err := b.Verify(ctx); err != nil {
		return err
	}
	if err := requirePoolAuthorityWritable(b.s); err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	key := b.key(".nex-probe-" + hex.EncodeToString(nonce))
	payload := []byte("nex backup probe")
	if _, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*")}); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := b.client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
		if err != nil && probeErr == nil {
			probeErr = err
		}
		keys, err := b.list(cleanup, key)
		if err != nil && probeErr == nil {
			probeErr = err
		}
		for _, found := range keys {
			if found == key && probeErr == nil {
				probeErr = errors.New("S3 probe deletion is not visible")
			}
		}
	}()
	if _, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*")}); !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
		return errors.New("S3 conditional create is not proven")
	}
	raw, err := b.read(ctx, key, 128)
	if err != nil || !bytes.Equal(raw, payload) {
		return errors.New("S3 read after write is not proven")
	}
	// Cleanup relies on conditional deletion to avoid removing an object that
	// changed after its contents were verified.
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), IfMatch: aws.String("\"nex-wrong-etag\"")}); !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
		return errors.New("S3 conditional delete is not proven")
	}
	if got, err := b.read(ctx, key, 128); err != nil || !bytes.Equal(got, payload) {
		return errors.New("S3 conditional delete preserved no object")
	}
	keys, err := b.list(ctx, key)
	if err != nil {
		return err
	}
	found := false
	for _, listed := range keys {
		if listed == key {
			found = true
		}
	}
	if !found {
		return errors.New("S3 listing after write is not proven")
	}
	return b.ensureMultipartProfile(ctx)
}

func (b *s3BackupStorage) multipartProofKey() string {
	material := string(b.ownerBytes()) + "\x00" + b.repo.CredentialRef + "\x00" +
		strconv.FormatUint(b.repo.Generation, 10) + "\x00" + b.s.State.HostID
	sum := sha256.Sum256([]byte(material))
	return hex.EncodeToString(sum[:])
}

func (b *s3BackupStorage) ensureMultipartProfile(ctx context.Context) error {
	key := b.multipartProofKey()
	if proved, ok := s3MultipartProof.Load(key); ok && time.Since(proved.(time.Time)) < s3MultipartProofMaxAge {
		return nil
	}
	if err := requirePoolAuthorityWritable(b.s); err != nil {
		return err
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	if err := b.probeMultipart(ctx, b.key(".nex-probe-multipart-"+hex.EncodeToString(nonce))); err != nil {
		s3MultipartProof.Delete(key)
		return err
	}
	s3MultipartProof.Store(key, time.Now())
	return nil
}

func (b *s3BackupStorage) probeMultipart(ctx context.Context, key string) (probeErr error) {
	// The conditional single PUT above does not prove conditional multipart completion.
	payload := make([]byte, 5<<20+1)
	uploader := manager.NewUploader(b.client)
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, err := b.client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
		if err != nil && probeErr == nil {
			probeErr = err
		}
		keys, err := b.list(cleanup, key)
		if err != nil && probeErr == nil {
			probeErr = err
		}
		for _, listed := range keys {
			if listed == key && probeErr == nil {
				probeErr = errors.New("S3 multipart probe deletion is not visible")
			}
		}
	}()
	input := &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*")}
	if _, err := uploader.Upload(ctx, input); err != nil {
		return err
	}
	payload[0] = 1
	input.Body = bytes.NewReader(payload)
	if _, err := uploader.Upload(ctx, input); !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
		return errors.New("S3 conditional multipart completion is not proven")
	} else if abortErr := b.abortFailedMultipart(err, key); abortErr != nil {
		return abortErr
	}
	payload[0] = 0
	raw, err := b.read(ctx, key, int64(len(payload)))
	if err != nil || !bytes.Equal(raw, payload) {
		return errors.New("S3 multipart read after write is not proven")
	}
	return nil
}

func (b *s3BackupStorage) CaptureWorkDir(operationID string, online bool) string {
	if online {
		return vmhost.OnlineBackupWorkDir(operationID)
	}
	return filepath.Join(b.s.dir, "backup-staging", operationID)
}

func (b *s3BackupStorage) read(ctx context.Context, key string, limit int64) ([]byte, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(out.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, ErrBackupInvalid
	}
	return raw, nil
}

func (b *s3BackupStorage) ReadPoint(ctx context.Context, pointID string) (protocol.BackupManifest, string, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return protocol.BackupManifest{}, "", ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if _, err := b.read(ctx, b.key(s3BackupAbortPrefix+pointID), 1<<20); err == nil {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return protocol.BackupManifest{}, "", err
	}
	if _, err := b.read(ctx, b.pointKey(pointID, s3BackupDeleteKey), 16<<10); err == nil {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return protocol.BackupManifest{}, "", err
	}
	raw, err := b.read(ctx, b.pointKey(pointID, "manifest.json"), 2<<20)
	if err != nil {
		if isS3Code(err, "NoSuchKey", "NotFound") {
			keys, listErr := b.list(ctx, b.pointKey(pointID, ""))
			if listErr != nil {
				return protocol.BackupManifest{}, "", listErr
			}
			if len(keys) == 0 || b.hasIncompleteMarker(ctx, pointID) {
				return protocol.BackupManifest{}, "", os.ErrNotExist
			}
			return protocol.BackupManifest{}, "", ErrBackupConflict
		}
		return protocol.BackupManifest{}, "", err
	}
	if _, ok := b.parseAbortTombstone(pointID, raw); ok {
		return protocol.BackupManifest{}, "", os.ErrNotExist
	}
	if len(raw) > 1<<20 {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Valid() != nil || manifest.RecoveryPointID != pointID ||
		manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return protocol.BackupManifest{}, "", ErrBackupConflict
	}
	for _, disk := range manifest.Disks {
		if err := b.verifyArtifact(ctx, pointID, disk.Filename, disk.Size, disk.SHA256); err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	for _, fw := range manifest.Firmware {
		if err := b.verifyArtifact(ctx, pointID, fw.Filename, fw.Size, fw.SHA256); err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	sum := sha256.Sum256(raw)
	return manifest, hex.EncodeToString(sum[:]), nil
}

func (b *s3BackupStorage) abortTombstone(id string, manifestRaw []byte) []byte {
	sum := sha256.Sum256(manifestRaw)
	raw, _ := json.Marshal(s3BackupAbortTombstone{Repository: b.repo.ID, Pool: b.repo.PoolID,
		Point: id, ManifestSHA: hex.EncodeToString(sum[:]), Manifest: manifestRaw})
	return raw
}

func (b *s3BackupStorage) parseAbortTombstone(id string, raw []byte) (s3BackupAbortTombstone, bool) {
	var tombstone s3BackupAbortTombstone
	if json.Unmarshal(raw, &tombstone) != nil || tombstone.Repository != b.repo.ID ||
		tombstone.Pool != b.repo.PoolID || tombstone.Point != id || len(tombstone.ManifestSHA) != 64 {
		return s3BackupAbortTombstone{}, false
	}
	if _, err := hex.DecodeString(tombstone.ManifestSHA); err != nil {
		return s3BackupAbortTombstone{}, false
	}
	sum := sha256.Sum256(tombstone.Manifest)
	if hex.EncodeToString(sum[:]) != tombstone.ManifestSHA {
		return s3BackupAbortTombstone{}, false
	}
	var manifest protocol.BackupManifest
	if len(tombstone.Manifest) > 1<<20 || json.Unmarshal(tombstone.Manifest, &manifest) != nil || manifest.Valid() != nil ||
		manifest.RecoveryPointID != id || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return s3BackupAbortTombstone{}, false
	}
	canonical, _ := json.Marshal(tombstone)
	return tombstone, bytes.Equal(raw, canonical)
}

func (b *s3BackupStorage) artifactTombstone(id, name string, artifact protocol.BackupDiskArtifact) []byte {
	raw, _ := json.Marshal(s3BackupArtifactTombstone{Repository: b.repo.ID, Pool: b.repo.PoolID,
		Point: id, Name: name, Size: artifact.Size, SHA256: artifact.SHA256})
	return raw
}

func (b *s3BackupStorage) hasIncompleteMarker(ctx context.Context, id string) bool {
	for _, prefix := range []string{s3BackupIntentPrefix, s3BackupAbortPrefix} {
		if _, err := b.read(ctx, b.key(prefix+id), 1<<20); err == nil {
			return true
		}
	}
	return false
}

func (b *s3BackupStorage) verifyArtifact(ctx context.Context, pointID, name string, size int64, digest string) error {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, name))})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(out.Body, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) Publish(ctx context.Context, manifest protocol.BackupManifest, stageFiles map[string]string) (string, error) {
	if manifest.Valid() != nil || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return "", ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return "", err
	}
	if err := requirePoolAuthorityWritable(b.s); err != nil {
		return "", err
	}
	if err := b.ensureMultipartProfile(ctx); err != nil {
		return "", err
	}
	raw, err := json.Marshal(manifest)
	if err != nil || len(raw) > 1<<20 {
		return "", ErrBackupInvalid
	}
	manifestKey := b.pointKey(manifest.RecoveryPointID, "manifest.json")
	if old, err := b.read(ctx, manifestKey, 2<<20); err == nil {
		if !bytes.Equal(old, raw) {
			return "", ErrBackupConflict
		}
		_, digest, err := b.ReadPoint(ctx, manifest.RecoveryPointID)
		return digest, err
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return "", err
	}
	expected := make(map[string]protocol.BackupDiskArtifact)
	for _, disk := range manifest.Disks {
		expected[disk.Filename] = disk
	}
	for _, fw := range manifest.Firmware {
		expected[fw.Filename] = protocol.BackupDiskArtifact{Filename: fw.Filename, Size: fw.Size, SHA256: fw.SHA256}
	}
	if len(stageFiles) != len(expected) {
		return "", ErrBackupInvalid
	}
	if err := b.putExactMarker(ctx, b.key(s3BackupIntentPrefix+manifest.RecoveryPointID), raw); err != nil {
		return "", err
	}
	for name, path := range stageFiles {
		artifact, ok := expected[name]
		if !ok {
			return "", ErrBackupInvalid
		}
		if err := b.putArtifact(ctx, manifest.RecoveryPointID, name, path, artifact.Size, artifact.SHA256); err != nil {
			return "", err
		}
	}
	// Hold the authority transition lock across publication. Orphan artifact uploads
	// are harmless, but the final manifest may not cross a planned handoff.
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return "", err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return "", err
	}
	if _, deleting, err := b.readDeleteRecord(ctx, manifest.RecoveryPointID); err != nil {
		return "", err
	} else if deleting {
		return "", ErrBackupConflict
	}
	if _, err := b.read(ctx, b.key(s3BackupAbortPrefix+manifest.RecoveryPointID), 1<<20); err == nil {
		return "", ErrBackupConflict
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return "", err
	}
	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(manifestKey),
		Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(sha256Base64(raw))})
	if err != nil && !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
		// A lost reply is ambiguous: only the exact published bytes can settle it.
		old, readErr := b.read(ctx, manifestKey, 2<<20)
		if readErr != nil || !bytes.Equal(old, raw) {
			return "", err
		}
	}
	old, err := b.read(ctx, manifestKey, 2<<20)
	if err != nil || !bytes.Equal(old, raw) {
		return "", ErrBackupConflict
	}
	_, digest, err := b.ReadPoint(ctx, manifest.RecoveryPointID)
	if err == nil {
		// A lost cleanup reply leaves only this harmless exact intent. The
		// published manifest remains authoritative on an exact retry.
		_ = b.deleteExactMarker(ctx, b.key(s3BackupIntentPrefix+manifest.RecoveryPointID), raw)
	}
	return digest, err
}

func (b *s3BackupStorage) putExactMarker(ctx context.Context, key string, raw []byte) error {
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(sha256Base64(raw))})
	if err != nil && !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
		return err
	}
	stored, readErr := b.read(ctx, key, 2<<20)
	if readErr != nil {
		return readErr
	}
	if !bytes.Equal(stored, raw) {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) deleteExactMarker(ctx context.Context, key string, expected []byte) error {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
	if isS3Code(err, "NoSuchKey", "NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(out.Body, int64(len(expected))+1))
	closeErr := out.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !bytes.Equal(raw, expected) || out.ETag == nil || *out.ETag == "" {
		return ErrBackupConflict
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), IfMatch: out.ETag}); err != nil {
		return err
	}
	if _, err := b.read(ctx, key, int64(len(expected))+1); !isS3Code(err, "NoSuchKey", "NotFound") {
		if err != nil {
			return err
		}
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) deleteExactArtifact(ctx context.Context, key string, size int64, digest string) error {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
	if isS3Code(err, "NoSuchKey", "NotFound") {
		return nil
	}
	if err != nil {
		return err
	}
	h := sha256.New()
	n, readErr := io.Copy(h, io.LimitReader(out.Body, size+1))
	closeErr := out.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest || out.ETag == nil || *out.ETag == "" {
		return ErrBackupConflict
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), IfMatch: out.ETag}); err != nil {
		return err
	}
	if check, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)}); !isS3Code(err, "NoSuchKey", "NotFound") {
		if err != nil {
			return err
		}
		_ = check.Body.Close()
		return ErrBackupConflict
	}
	return nil
}

// AbortIncomplete removes only exact artifacts bound to this operation's
// durable intent. The abort marker keeps the decision across interrupted retries.
func (b *s3BackupStorage) AbortIncomplete(ctx context.Context, id string) error {
	if !protocol.ValidSnapshotOperationID(id) {
		return ErrBackupInvalid
	}
	// Reprove conditional deletion after restart or a service-profile change;
	// otherwise an If-Match-ignoring service could delete a replaced object.
	if err := b.Probe(ctx); err != nil {
		return err
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return err
	}
	if _, err := getCatalogPoint(b.s, b.repo.ID, id); err == nil {
		return ErrBackupConflict
	} else if !errors.Is(err, ErrBackupNotFound) {
		return err
	}
	pins, err := ActiveBackupRestorePins(b.s)
	if err != nil {
		return err
	}
	if pins[id] {
		return ErrBackupConflict
	}
	if protected, err := b.Protected(ctx, id); err != nil || protected {
		if err != nil {
			return err
		}
		return ErrBackupConflict
	}
	if _, deleting, err := b.readDeleteRecord(ctx, id); err != nil {
		return err
	} else if deleting {
		return ErrBackupConflict
	}
	manifestKey := b.pointKey(id, "manifest.json")
	remoteManifest, manifestErr := b.read(ctx, manifestKey, 2<<20)
	if manifestErr != nil && !isS3Code(manifestErr, "NoSuchKey", "NotFound") {
		return manifestErr
	}
	intentKey, abortKey := b.key(s3BackupIntentPrefix+id), b.key(s3BackupAbortPrefix+id)
	intent, intentErr := b.read(ctx, intentKey, 1<<20)
	if intentErr != nil && !isS3Code(intentErr, "NoSuchKey", "NotFound") {
		return intentErr
	}
	abort, abortErr := b.read(ctx, abortKey, 1<<20)
	if abortErr != nil && !isS3Code(abortErr, "NoSuchKey", "NotFound") {
		return abortErr
	}
	keys, err := b.list(ctx, b.pointKey(id, ""))
	if err != nil {
		return err
	}
	if intentErr != nil && abortErr != nil {
		if len(keys) == 0 && manifestErr != nil {
			return nil
		}
		if tombstone, ok := b.parseAbortTombstone(id, remoteManifest); ok {
			intent = tombstone.Manifest
		} else {
			return ErrBackupConflict
		}
	}
	raw := intent
	if intentErr != nil && abortErr == nil {
		raw = abort
	} else if abortErr == nil && !bytes.Equal(intent, abort) {
		return ErrBackupConflict
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Valid() != nil || manifest.RecoveryPointID != id ||
		manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return ErrBackupConflict
	}
	tombstoneRaw := b.abortTombstone(id, raw)
	if manifestErr == nil && !bytes.Equal(remoteManifest, tombstoneRaw) {
		return ErrBackupConflict
	}
	artifacts := make(map[string]protocol.BackupDiskArtifact)
	for _, disk := range manifest.Disks {
		artifacts[b.pointKey(id, disk.Filename)] = disk
	}
	for _, fw := range manifest.Firmware {
		artifacts[b.pointKey(id, fw.Filename)] = protocol.BackupDiskArtifact{Size: fw.Size, SHA256: fw.SHA256}
	}
	for _, key := range keys {
		if key == manifestKey && bytes.Equal(remoteManifest, tombstoneRaw) {
			continue
		}
		if _, ok := artifacts[key]; !ok {
			return ErrBackupConflict
		}
	}
	// The permanent same-key tombstone serializes against every delayed final
	// PutObject(If-None-Match:*). Do not remove any artifact before proving it won.
	if err := b.putExactMarker(ctx, manifestKey, tombstoneRaw); err != nil {
		return err
	}
	if abortErr != nil {
		if err := b.putExactMarker(ctx, abortKey, raw); err != nil {
			return err
		}
	}
	artifactKeys := make([]string, 0, len(artifacts))
	for key := range artifacts {
		artifactKeys = append(artifactKeys, key)
	}
	sort.Strings(artifactKeys)
	for _, key := range artifactKeys {
		name := strings.TrimPrefix(key, b.pointKey(id, ""))
		artifact := artifacts[key]
		tombstone := b.artifactTombstone(id, name, artifact)
		stored, readErr := b.read(ctx, key, int64(len(tombstone)))
		if readErr == nil && bytes.Equal(stored, tombstone) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, ErrBackupInvalid) && !isS3Code(readErr, "NoSuchKey", "NotFound") {
			return readErr
		}
		if err := b.deleteExactArtifact(ctx, key, artifact.Size, artifact.SHA256); err != nil {
			return err
		}
		if err := b.putExactMarker(ctx, key, tombstone); err != nil {
			return err
		}
	}
	expectedKeys := append(artifactKeys, manifestKey)
	sort.Strings(expectedKeys)
	if remaining, err := b.list(ctx, b.pointKey(id, "")); err != nil {
		return err
	} else if !slices.Equal(remaining, expectedKeys) {
		return ErrBackupConflict
	}
	if intentErr == nil {
		if err := b.deleteExactMarker(ctx, intentKey, raw); err != nil {
			return err
		}
	}
	return b.deleteExactMarker(ctx, abortKey, raw)
}

func sha256Base64(raw []byte) string {
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (b *s3BackupStorage) putArtifact(ctx context.Context, pointID, name, path string, size int64, digest string) error {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return ErrBackupInvalid
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrBackupConflict
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	key := b.pointKey(pointID, name)
	uploader := manager.NewUploader(b.client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: f, ContentLength: aws.Int64(size), IfNoneMatch: aws.String("*")})
	if err != nil {
		if abortErr := b.abortFailedMultipart(err, key); abortErr != nil {
			return errors.Join(err, abortErr)
		}
		if verifyErr := b.verifyArtifact(ctx, pointID, name, size, digest); verifyErr != nil {
			return err
		}
	}
	return b.verifyArtifact(ctx, pointID, name, size, digest)
}

func (b *s3BackupStorage) abortFailedMultipart(uploadErr error, key string) error {
	var multipart manager.MultiUploadFailure
	if !errors.As(uploadErr, &multipart) {
		return nil
	}
	cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := b.client.AbortMultipartUpload(cleanup, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key), UploadId: aws.String(multipart.UploadID())})
	if isS3Code(err, "NoSuchUpload") {
		return nil
	}
	return err
}

func (b *s3BackupStorage) CopyArtifacts(ctx context.Context, pointID, localDir string, manifest protocol.BackupManifest) error {
	stored, _, err := b.ReadPoint(ctx, pointID)
	if err != nil {
		return err
	}
	if !backupManifestArtifactsEqual(stored, manifest) {
		return ErrBackupConflict
	}
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	for _, disk := range manifest.Disks {
		if err := b.copyArtifact(ctx, pointID, disk.Filename, localDir, disk.Size, disk.SHA256); err != nil {
			return err
		}
	}
	for _, fw := range manifest.Firmware {
		if err := b.copyArtifact(ctx, pointID, fw.Filename, localDir, fw.Size, fw.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func backupManifestArtifactsEqual(a, b protocol.BackupManifest) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}

func (b *s3BackupStorage) copyArtifact(ctx context.Context, pointID, name, localDir string, size int64, digest string) (err error) {
	if !filepath.IsAbs(localDir) {
		return ErrBackupInvalid
	}
	path := filepath.Join(localDir, name)
	if info, statErr := os.Lstat(path); statErr == nil {
		if info.Mode().IsRegular() && info.Size() == size {
			local, openErr := os.Open(path)
			if openErr != nil {
				return openErr
			}
			h := sha256.New()
			_, readErr := io.Copy(h, local)
			closeErr := local.Close()
			if readErr == nil && closeErr == nil && hex.EncodeToString(h.Sum(nil)) == digest {
				return nil
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, name))})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
		if err != nil {
			_ = os.Remove(path)
		}
	}()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(out.Body, size+1))
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != digest {
		return ErrBackupConflict
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return f.Close()
}

func (b *s3BackupStorage) list(ctx context.Context, prefix string) ([]string, error) {
	var keys []string
	paginator := s3.NewListObjectsV2Paginator(b.client, &s3.ListObjectsV2Input{Bucket: aws.String(b.repo.S3Source.Bucket), Prefix: aws.String(prefix)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, err
		}
		for _, obj := range page.Contents {
			if obj.Key != nil {
				keys = append(keys, *obj.Key)
			}
		}
	}
	return keys, nil
}

func (b *s3BackupStorage) Entries(ctx context.Context) ([]string, error) {
	if err := b.Verify(ctx); err != nil {
		return nil, err
	}
	keys, err := b.list(ctx, b.root)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, key := range keys {
		rest := strings.TrimPrefix(key, b.root)
		if id, ok := strings.CutPrefix(rest, s3BackupAbortPrefix); ok && protocol.ValidSnapshotOperationID(id) {
			return nil, ErrBackupConflict // An incomplete abort must settle before rebuild.
		}
		if id, ok := strings.CutSuffix(rest, "/"+s3BackupDeleteKey); ok && protocol.ValidSnapshotOperationID(id) {
			return nil, ErrBackupConflict // An interrupted prune must settle before rebuild.
		}
		if !strings.HasSuffix(rest, "/manifest.json") {
			continue
		}
		pointID := strings.TrimSuffix(rest, "/manifest.json")
		if protocol.ValidSnapshotOperationID(pointID) {
			raw, readErr := b.read(ctx, key, 2<<20)
			if readErr != nil {
				return nil, readErr
			}
			if _, aborted := b.parseAbortTombstone(pointID, raw); aborted {
				continue
			}
			seen[pointID] = true
		}
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func (b *s3BackupStorage) Scan(ctx context.Context) ([]protocol.BackupRecoveryPoint, error) {
	names, err := b.Entries(ctx)
	if err != nil {
		return nil, err
	}
	points := make([]protocol.BackupRecoveryPoint, 0, len(names))
	for _, id := range names {
		manifest, digest, err := b.ReadPoint(ctx, id)
		if err != nil {
			return nil, err
		}
		protected, err := b.Protected(ctx, id)
		if err != nil {
			return nil, err
		}
		points = append(points, protocol.BackupRecoveryPoint{ID: id, RepositoryID: b.repo.ID, PoolID: b.repo.PoolID,
			SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName, CapturedAt: manifest.CapturedAt,
			Consistency: manifest.Consistency, ManifestSHA256: digest, Protected: protected})
	}
	return points, nil
}

func (b *s3BackupStorage) Protected(ctx context.Context, pointID string) (bool, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return false, ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return false, err
	}
	raw, err := b.read(ctx, b.pointKey(pointID, ".nex-protected"), 128)
	if isS3Code(err, "NoSuchKey", "NotFound") {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(raw) != pointID {
		return false, ErrBackupConflict
	}
	return true, nil
}

func (b *s3BackupStorage) SetProtected(ctx context.Context, pointID string, protected bool) error {
	if _, _, err := b.ReadPoint(ctx, pointID); err != nil {
		return err
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return err
	}
	key := b.pointKey(pointID, ".nex-protected")
	if protected {
		_, err = b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
			Body: strings.NewReader(pointID), IfNoneMatch: aws.String("*")})
		if err != nil && !isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict") {
			return err
		}
		ok, err := b.Protected(ctx, pointID)
		if err != nil || !ok {
			return ErrBackupConflict
		}
		return nil
	}
	if ok, err := b.Protected(ctx, pointID); err != nil || !ok {
		return err
	}
	_, err = b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
	if err != nil {
		return err
	}
	ok, err := b.Protected(ctx, pointID)
	if err != nil || ok {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) Delete(ctx context.Context, pointID string) error {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return err
	}
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return err
	}
	pins, err := ActiveBackupRestorePins(b.s)
	if err != nil {
		return err
	}
	if pins[pointID] {
		return ErrBackupConflict
	}
	record, exists, err := b.readDeleteRecord(ctx, pointID)
	if err != nil {
		return err
	}
	if exists {
		catalog, err := getCatalogPoint(b.s, b.repo.ID, pointID)
		if err != nil || catalog.PoolID != b.repo.PoolID || catalog.ManifestSHA256 != record.ManifestSHA || catalog.Protected {
			return ErrBackupConflict
		}
	}
	if !exists {
		manifest, digest, readErr := b.ReadPoint(ctx, pointID)
		if errors.Is(readErr, os.ErrNotExist) {
			remaining, listErr := b.list(ctx, b.pointKey(pointID, ""))
			if listErr != nil {
				return listErr
			}
			if len(remaining) == 0 {
				return nil
			}
			return ErrBackupConflict
		}
		if readErr != nil {
			return readErr
		}
		catalog, err := getCatalogPoint(b.s, b.repo.ID, pointID)
		if err != nil || catalog.PoolID != b.repo.PoolID || catalog.ManifestSHA256 != digest || catalog.Protected {
			return ErrBackupConflict
		}
		protected, protectErr := b.Protected(ctx, pointID)
		if protectErr != nil {
			return protectErr
		}
		if protected {
			return ErrBackupConflict
		}
		manifestRaw, err := b.read(ctx, b.pointKey(pointID, "manifest.json"), 1<<20)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(manifestRaw)
		if hex.EncodeToString(sum[:]) != digest {
			return ErrBackupConflict
		}
		record = s3BackupDeleteRecord{Repository: b.repo.ID, Pool: b.repo.PoolID, Point: pointID, ManifestSHA: digest,
			Manifest: manifestRaw, Keys: b.manifestKeys(manifest)}
		keys, listErr := b.list(ctx, b.pointKey(pointID, ""))
		if listErr != nil {
			return listErr
		}
		if !slices.Equal(keys, record.Keys) {
			return ErrBackupConflict
		}
		raw, _ := json.Marshal(record)
		key := b.pointKey(pointID, s3BackupDeleteKey)
		_, putErr := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
			Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(sha256Base64(raw))})
		if putErr != nil && !isS3Code(putErr, "PreconditionFailed", "ConditionalRequestConflict") {
			return putErr
		}
		stored, readErr := b.read(ctx, key, 2<<20)
		if readErr != nil || !bytes.Equal(stored, raw) {
			return ErrBackupConflict
		}
	}
	allowed := make(map[string]bool, len(record.Keys)+1)
	for _, key := range record.Keys {
		allowed[key] = true
	}
	deleteKey := b.pointKey(pointID, s3BackupDeleteKey)
	allowed[deleteKey] = true
	keys, err := b.list(ctx, b.pointKey(pointID, ""))
	if err != nil {
		return err
	}
	for _, key := range keys {
		if !allowed[key] {
			return ErrBackupConflict
		}
	}
	if !allowed[b.pointKey(pointID, "manifest.json")] {
		return ErrBackupConflict
	}
	// The durable deletion record permits exact retries after any lost reply.
	manifestKey := b.pointKey(pointID, "manifest.json")
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(manifestKey)}); err != nil {
		return err
	}
	for _, key := range record.Keys {
		if key == manifestKey {
			continue
		}
		if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)}); err != nil {
			return err
		}
	}
	remaining, err := b.list(ctx, b.pointKey(pointID, ""))
	if err != nil {
		return err
	}
	if len(remaining) != 1 || remaining[0] != deleteKey {
		return ErrBackupConflict
	}
	// A publication reply may have been lost after the final manifest committed.
	// Its exact intent can outlive the point and must be retired with deletion.
	intentKey := b.key(s3BackupIntentPrefix + pointID)
	if intent, err := b.read(ctx, intentKey, 1<<20); err == nil {
		if !bytes.Equal(intent, record.Manifest) {
			return ErrBackupConflict
		}
		if err := b.deleteExactMarker(ctx, intentKey, record.Manifest); err != nil {
			return err
		}
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return err
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(deleteKey)}); err != nil {
		return err
	}
	remaining, err = b.list(ctx, b.pointKey(pointID, ""))
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) readDeleteRecord(ctx context.Context, pointID string) (s3BackupDeleteRecord, bool, error) {
	raw, err := b.read(ctx, b.pointKey(pointID, s3BackupDeleteKey), 2<<20)
	if isS3Code(err, "NoSuchKey", "NotFound") {
		return s3BackupDeleteRecord{}, false, nil
	}
	if err != nil {
		return s3BackupDeleteRecord{}, false, err
	}
	var record s3BackupDeleteRecord
	if json.Unmarshal(raw, &record) != nil || record.Repository != b.repo.ID || record.Pool != b.repo.PoolID ||
		record.Point != pointID || len(record.ManifestSHA) != 64 || len(record.Keys) < 2 || len(record.Keys) > 35 ||
		!sort.StringsAreSorted(record.Keys) {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	sum := sha256.Sum256(record.Manifest)
	if hex.EncodeToString(sum[:]) != record.ManifestSHA {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	var manifest protocol.BackupManifest
	if json.Unmarshal(record.Manifest, &manifest) != nil || manifest.Valid() != nil ||
		manifest.RecoveryPointID != pointID || manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID ||
		!slices.Equal(record.Keys, b.manifestKeys(manifest)) {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	for i, key := range record.Keys {
		if i > 0 && record.Keys[i-1] == key {
			return s3BackupDeleteRecord{}, false, ErrBackupConflict
		}
		name, ok := strings.CutPrefix(key, b.pointKey(pointID, ""))
		if !ok || name == "" || strings.Contains(name, "/") || name == s3BackupDeleteKey || name == ".nex-protected" {
			return s3BackupDeleteRecord{}, false, ErrBackupConflict
		}
	}
	manifestKey := b.pointKey(pointID, "manifest.json")
	if !slices.Contains(record.Keys, manifestKey) {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	manifestRaw, err := b.read(ctx, manifestKey, 1<<20)
	if err == nil {
		sum := sha256.Sum256(manifestRaw)
		if hex.EncodeToString(sum[:]) != record.ManifestSHA {
			return s3BackupDeleteRecord{}, false, ErrBackupConflict
		}
	} else if !isS3Code(err, "NoSuchKey", "NotFound") {
		return s3BackupDeleteRecord{}, false, err
	}
	return record, true, nil
}

func (b *s3BackupStorage) manifestKeys(manifest protocol.BackupManifest) []string {
	keys := []string{b.pointKey(manifest.RecoveryPointID, "manifest.json")}
	for _, disk := range manifest.Disks {
		keys = append(keys, b.pointKey(manifest.RecoveryPointID, disk.Filename))
	}
	for _, fw := range manifest.Firmware {
		keys = append(keys, b.pointKey(manifest.RecoveryPointID, fw.Filename))
	}
	sort.Strings(keys)
	return keys
}

func isS3Code(err error, codes ...string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	for _, code := range codes {
		if apiErr.ErrorCode() == code {
			return true
		}
	}
	return false
}

var _ backupRepositoryStorage = (*s3BackupStorage)(nil)
