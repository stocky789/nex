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
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
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

// Process-local proof expires; restart or a credential change rechecks it.
var s3MultipartProof sync.Map

// s3BackupManifestRecord is the abort tombstone; a delete record adds the keys.
type s3BackupManifestRecord struct {
	Repository  string          `json:"repository"`
	Pool        string          `json:"pool"`
	Point       string          `json:"point"`
	ManifestSHA string          `json:"manifest_sha256"`
	Manifest    json.RawMessage `json:"manifest"`
}

type s3BackupDeleteRecord struct {
	s3BackupManifestRecord
	Keys []string `json:"keys"`
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

// s3BackupReadTimeout is how long a connection may go without sending or
// receiving anything before it fails, so a stalled service cannot hold an
// operation forever while a slow upload keeps going.
var s3BackupReadTimeout = 2 * time.Minute

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
	// A "." prefix names the bucket root through a dot segment that services may normalise away.
	if credential.Username == "" || credential.Secret == "" || repo.S3Source.Prefix == "." {
		return nil, ErrBackupInvalid
	}
	region := repo.S3Source.Region
	if region == "" {
		region = "us-east-1"
	}
	client := newBackupS3Client(s3.Options{
		Region: region, BaseEndpoint: aws.String(repo.S3Source.Endpoint), UsePathStyle: true,
		HTTPClient: awshttp.NewBuildableClient().WithTransportOptions(func(t *http.Transport) {
			dial := t.DialContext
			t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := dial(ctx, network, address)
				if err != nil {
					return nil, err
				}
				return progressConn{conn, s3BackupReadTimeout}, nil
			}
		}),
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
	claimed, err := b.verifyOwner(ctx)
	if err != nil {
		return protocol.BackupRepository{}, err
	}
	identity := b.remoteIdentity()
	if repo.RemoteIdentity != nil && *repo.RemoteIdentity != identity {
		return protocol.BackupRepository{}, ErrBackupConflict
	}
	registering := repo.RemoteIdentity == nil
	repo.RemoteIdentity = &identity
	b.repo = repo
	// Registration must prove the current service profile before the repository
	// enters Core's durable inventory. Adoption reuses a fresh proof: member
	// dispatch prepares on every request and cannot repeat the multipart transfer.
	if registering {
		s3MultipartProof.Delete(b.multipartProofKey())
	}
	if err := b.Probe(ctx); err != nil {
		if claimed {
			// The retry carries a new repository ID, so a kept claim would refuse it.
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = errors.Join(err, b.deleteExactMarker(cleanup, b.key(s3BackupOwnerKey), b.ownerBytes()))
		}
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
	if _, err := b.verifyOwner(ctx); err != nil {
		return protocol.BackupRemoteIdentity{}, err
	}
	return b.remoteIdentity(), nil
}

func (b *s3BackupStorage) remoteIdentity() protocol.BackupRemoteIdentity {
	return protocol.BackupRemoteIdentity{Fingerprint: sha256Hex(b.ownerBytes())}
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

// verifyOwner checks the owner object, claiming an empty prefix on first
// registration. claimed reports that this call made the claim.
func (b *s3BackupStorage) verifyOwner(ctx context.Context) (claimed bool, err error) {
	key := b.key(s3BackupOwnerKey)
	owner := b.ownerBytes()
	defer func() {
		if !claimed || err == nil {
			return
		}
		// A failed create may still have committed. Reconcile and release only
		// this exact owner, even when the registration context has expired.
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, b.deleteExactMarker(cleanup, key, owner))
		claimed = false
	}()
	raw, err := b.read(ctx, key, 4096)
	if isS3Missing(err) && b.repo.RemoteIdentity == nil {
		if err := requirePoolAuthorityWritable(b.s); err != nil {
			return false, err
		}
		// An owner above this prefix means the new root sits inside another repository.
		parents := strings.Split(b.repo.S3Source.Prefix, "/")
		for i := range parents {
			_, ownerErr := b.read(ctx, strings.Join(append(parents[:i:i], s3BackupOwnerKey), "/"), 4096)
			if ownerErr == nil {
				return false, ErrBackupConflict
			}
			if !isS3Missing(ownerErr) {
				return false, ownerErr
			}
		}
		keys, listErr := b.list(ctx, b.root)
		if listErr != nil {
			return false, listErr
		}
		for _, found := range keys {
			// A probe from a refused registration may outlive its released claim.
			if !strings.HasPrefix(found, b.key(".nex-probe-")) {
				return false, ErrBackupConflict
			}
		}
		_, putErr := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
			Key: aws.String(key), Body: bytes.NewReader(owner), IfNoneMatch: aws.String("*")})
		// Only a precondition failure proves this attempt did not claim the prefix.
		claimed = !isS3Precondition(putErr)
		if putErr != nil && claimed {
			return claimed, putErr
		}
		raw, err = b.read(ctx, key, 4096)
	}
	if err != nil {
		return claimed, err
	}
	if !bytes.Equal(raw, owner) {
		return claimed, ErrBackupConflict
	}
	return claimed, nil
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
	defer func() {
		if err := b.removeProbe(key); err != nil && probeErr == nil {
			probeErr = err
		}
	}()
	// Probe writes carry the same checksum header as publication writes.
	if err := b.putExactMarker(ctx, key, payload); err != nil {
		return err
	}
	if _, err := b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(sha256Base64(payload))}); !isS3Precondition(err) {
		return errors.New("S3 conditional create is not proven")
	}
	raw, err := b.read(ctx, key, 128)
	if err != nil || !bytes.Equal(raw, payload) {
		return errors.New("S3 read after write is not proven")
	}
	// Cleanup relies on conditional deletion to avoid removing an object that
	// changed after its contents were verified.
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), IfMatch: aws.String("\"nex-wrong-etag\"")}); !isS3Precondition(err) {
		return errors.New("S3 conditional delete is not proven")
	}
	if got, err := b.read(ctx, key, 128); err != nil || !bytes.Equal(got, payload) {
		return errors.New("S3 conditional delete preserved no object")
	}
	keys, err := b.list(ctx, key)
	if err != nil {
		return err
	}
	if !slices.Contains(keys, key) {
		return errors.New("S3 listing after write is not proven")
	}
	return b.ensureMultipartProfile(ctx)
}

// removeProbe deletes a probe object on a fresh context, so a cancelled probe
// still cleans up, and checks the deletion is visible.
func (b *s3BackupStorage) removeProbe(key string) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.client.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)}); err != nil {
		return err
	}
	keys, err := b.list(cleanup, key)
	if err != nil {
		return err
	}
	if slices.Contains(keys, key) {
		return errors.New("S3 probe deletion is not visible")
	}
	return nil
}

func (b *s3BackupStorage) multipartProofKey() string {
	material := string(b.ownerBytes()) + "\x00" + b.repo.CredentialRef + "\x00" + b.s.State.HostID
	return sha256Hex([]byte(material))
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
		if err := b.removeProbe(key); err != nil && probeErr == nil {
			probeErr = err
		}
	}()
	input := &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: bytes.NewReader(payload), IfNoneMatch: aws.String("*")}
	if _, err := uploader.Upload(ctx, input); err != nil {
		return errors.Join(err, b.abortFailedMultipart(err, key))
	}
	payload[0] = 1
	input.Body = bytes.NewReader(payload)
	_, err := uploader.Upload(ctx, input)
	if abortErr := b.abortFailedMultipart(err, key); abortErr != nil {
		return abortErr
	}
	if !isS3Precondition(err) {
		return errors.New("S3 conditional multipart completion is not proven")
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

// requireAbsent fails with ErrBackupConflict when an object exists at key.
func (b *s3BackupStorage) requireAbsent(ctx context.Context, key string, limit int64) error {
	_, err := b.read(ctx, key, limit)
	if err == nil {
		return ErrBackupConflict
	}
	if !isS3Missing(err) {
		return err
	}
	return nil
}

func (b *s3BackupStorage) StatPoint(ctx context.Context, pointID string) (protocol.BackupManifest, string, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return protocol.BackupManifest{}, "", ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if err := b.requireAbsent(ctx, b.key(s3BackupAbortPrefix+pointID), 1<<20); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	if err := b.requireAbsent(ctx, b.pointKey(pointID, s3BackupDeleteKey), 16<<10); err != nil {
		return protocol.BackupManifest{}, "", err
	}
	raw, err := b.read(ctx, b.pointKey(pointID, "manifest.json"), 2<<20)
	if err != nil {
		if isS3Missing(err) {
			keys, listErr := b.list(ctx, b.pointKey(pointID, ""))
			if listErr != nil {
				return protocol.BackupManifest{}, "", listErr
			}
			incomplete, markerErr := b.hasIncompleteMarker(ctx, pointID)
			if markerErr != nil {
				return protocol.BackupManifest{}, "", markerErr
			}
			if len(keys) == 0 || incomplete {
				return protocol.BackupManifest{}, "", os.ErrNotExist
			}
			return protocol.BackupManifest{}, "", backupPointDamaged("recovery point " + pointID + " has no manifest on the destination")
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
		return protocol.BackupManifest{}, "", backupPointDamaged("the manifest of recovery point " + pointID + " is invalid or belongs to another repository")
	}
	for _, artifact := range manifestArtifacts(manifest) {
		if err := b.statArtifact(ctx, pointID, artifact.Filename, artifact.Size); isS3Missing(err) {
			return protocol.BackupManifest{}, "", backupPointDamaged(fmt.Sprintf("recovery point %s is missing its artifact %s on the destination", pointID, artifact.Filename))
		} else if errors.Is(err, ErrBackupConflict) {
			return protocol.BackupManifest{}, "", backupPointDamaged(fmt.Sprintf("recovery point %s has an artifact %s of the wrong size on the destination", pointID, artifact.Filename))
		} else if err != nil {
			return protocol.BackupManifest{}, "", err
		}
	}
	return manifest, sha256Hex(raw), nil
}

func (b *s3BackupStorage) abortTombstone(id string, manifestRaw []byte) []byte {
	raw, _ := json.Marshal(s3BackupManifestRecord{Repository: b.repo.ID, Pool: b.repo.PoolID,
		Point: id, ManifestSHA: sha256Hex(manifestRaw), Manifest: manifestRaw})
	return raw
}

func (b *s3BackupStorage) parseAbortTombstone(id string, raw []byte) (s3BackupManifestRecord, bool) {
	var tombstone s3BackupManifestRecord
	if json.Unmarshal(raw, &tombstone) != nil || tombstone.Repository != b.repo.ID ||
		tombstone.Pool != b.repo.PoolID || tombstone.Point != id || len(tombstone.ManifestSHA) != 64 {
		return s3BackupManifestRecord{}, false
	}
	if _, err := hex.DecodeString(tombstone.ManifestSHA); err != nil {
		return s3BackupManifestRecord{}, false
	}
	if sha256Hex(tombstone.Manifest) != tombstone.ManifestSHA {
		return s3BackupManifestRecord{}, false
	}
	if _, ok := b.recordedManifest(id, tombstone.Manifest); !ok {
		return s3BackupManifestRecord{}, false
	}
	canonical, _ := json.Marshal(tombstone)
	return tombstone, bytes.Equal(raw, canonical)
}

// recordedManifest decodes the manifest bound by an intent, abort tombstone or
// delete record. Those records are trusted by their own raw digest, so this
// skips manifest.Valid: it re-hashes the captured spec, and an upgraded spec
// schema would otherwise make every old record unrecognisable.
func (b *s3BackupStorage) recordedManifest(id string, raw []byte) (protocol.BackupManifest, bool) {
	var manifest protocol.BackupManifest
	if len(raw) > 1<<20 || json.Unmarshal(raw, &manifest) != nil || manifest.RecoveryPointID != id ||
		manifest.RepositoryID != b.repo.ID || manifest.PoolID != b.repo.PoolID {
		return protocol.BackupManifest{}, false
	}
	for _, disk := range manifest.Disks {
		if disk.Valid() != nil {
			return protocol.BackupManifest{}, false
		}
	}
	for _, fw := range manifest.Firmware {
		if fw.Valid() != nil {
			return protocol.BackupManifest{}, false
		}
	}
	return manifest, true
}

func (b *s3BackupStorage) artifactTombstone(id, name string, artifact protocol.BackupDiskArtifact) []byte {
	raw, _ := json.Marshal(s3BackupArtifactTombstone{Repository: b.repo.ID, Pool: b.repo.PoolID,
		Point: id, Name: name, Size: artifact.Size, SHA256: artifact.SHA256})
	return raw
}

func (b *s3BackupStorage) hasIncompleteMarker(ctx context.Context, id string) (bool, error) {
	for _, prefix := range []string{s3BackupIntentPrefix, s3BackupAbortPrefix} {
		_, err := b.read(ctx, b.key(prefix+id), 1<<20)
		if err == nil {
			return true, nil
		}
		if !isS3Missing(err) {
			return false, err
		}
	}
	return false, nil
}

func (b *s3BackupStorage) verifyArtifact(ctx context.Context, pointID, name string, size int64, digest string) error {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, name))})
	if err != nil {
		return err
	}
	defer out.Body.Close()
	n, sum, err := hashLimited(out.Body, size)
	if err != nil {
		return err
	}
	if n != size || sum != digest {
		return ErrBackupConflict
	}
	return nil
}

// backupPointDamaged says which recovery point the destination cannot fully serve and why.
// Callers still see ErrBackupConflict.
type backupPointDamaged string

func (e backupPointDamaged) Error() string      { return string(e) }
func (backupPointDamaged) Is(target error) bool { return target == ErrBackupConflict }

func (b *s3BackupStorage) statArtifact(ctx context.Context, pointID, name string, size int64) error {
	out, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, name))})
	if err != nil {
		return err
	}
	if out.ContentLength == nil || *out.ContentLength != size {
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
		_, digest, err := b.StatPoint(ctx, manifest.RecoveryPointID)
		return digest, err
	} else if !isS3Missing(err) {
		return "", err
	}
	expected := make(map[string]protocol.BackupDiskArtifact)
	for _, artifact := range manifestArtifacts(manifest) {
		expected[artifact.Filename] = artifact
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
	if err := b.requireAbsent(ctx, b.key(s3BackupAbortPrefix+manifest.RecoveryPointID), 1<<20); err != nil {
		return "", err
	}
	_, err = b.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(manifestKey),
		Body: bytes.NewReader(raw), IfNoneMatch: aws.String("*"), ChecksumSHA256: aws.String(sha256Base64(raw))})
	if err != nil && !isS3Precondition(err) {
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
	// putArtifact verified every artifact before the manifest could commit.
	_, digest, err := b.StatPoint(ctx, manifest.RecoveryPointID)
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
	if err != nil && !isS3Precondition(err) {
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

// deleteExact removes the object at key only if matches accepts its content
// and the ETag is unchanged. An absent object is already deleted.
func (b *s3BackupStorage) deleteExact(ctx context.Context, key string, matches func(io.Reader) (bool, error)) error {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)})
	if isS3Missing(err) {
		return nil
	}
	if err != nil {
		return err
	}
	ok, readErr := matches(out.Body)
	closeErr := out.Body.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if !ok || out.ETag == nil || *out.ETag == "" {
		return ErrBackupConflict
	}
	if _, err := b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket),
		Key: aws.String(key), IfMatch: out.ETag}); err != nil {
		return err
	}
	if _, err := b.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key)}); !isS3Missing(err) {
		if err != nil {
			return err
		}
		return ErrBackupConflict
	}
	return nil
}

func (b *s3BackupStorage) deleteExactMarker(ctx context.Context, key string, expected []byte) error {
	return b.deleteExact(ctx, key, func(r io.Reader) (bool, error) {
		raw, err := io.ReadAll(io.LimitReader(r, int64(len(expected))+1))
		return bytes.Equal(raw, expected), err
	})
}

func (b *s3BackupStorage) deleteExactArtifact(ctx context.Context, key string, size int64, digest string) error {
	return b.deleteExact(ctx, key, func(r io.Reader) (bool, error) {
		n, sum, err := hashLimited(r, size)
		return n == size && sum == digest, err
	})
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
	raw, intentPresent, err := b.claimAbort(ctx, id)
	if err != nil || raw == nil {
		return err
	}
	// A publisher that crashed mid-transfer leaves billed parts no other path lists.
	uploads := s3.NewListMultipartUploadsPaginator(b.client, &s3.ListMultipartUploadsInput{
		Bucket: aws.String(b.repo.S3Source.Bucket), Prefix: aws.String(b.pointKey(id, ""))})
	for uploads.HasMorePages() {
		page, err := uploads.NextPage(ctx)
		if err != nil {
			return err
		}
		for _, upload := range page.Uploads {
			_, err := b.client.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
				Bucket: aws.String(b.repo.S3Source.Bucket), Key: upload.Key, UploadId: upload.UploadId})
			if err != nil && !isS3Code(err, "NoSuchUpload") {
				return err
			}
		}
	}
	var manifest protocol.BackupManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	manifestKey := b.pointKey(id, "manifest.json")
	intentKey, abortKey := b.key(s3BackupIntentPrefix+id), b.key(s3BackupAbortPrefix+id)
	// The committed tombstone fences publication, so the exact-content artifact
	// reads below run without holding the authority transition lock.
	artifacts := make(map[string]protocol.BackupDiskArtifact)
	for _, artifact := range manifestArtifacts(manifest) {
		artifacts[b.pointKey(id, artifact.Filename)] = artifact
	}
	artifactKeys := slices.Sorted(maps.Keys(artifacts))
	for _, key := range artifactKeys {
		name := strings.TrimPrefix(key, b.pointKey(id, ""))
		artifact := artifacts[key]
		tombstone := b.artifactTombstone(id, name, artifact)
		stored, readErr := b.read(ctx, key, int64(len(tombstone)))
		if readErr == nil && bytes.Equal(stored, tombstone) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, ErrBackupInvalid) && !isS3Missing(readErr) {
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
	slices.Sort(expectedKeys)
	if remaining, err := b.list(ctx, b.pointKey(id, "")); err != nil {
		return err
	} else if !slices.Equal(remaining, expectedKeys) {
		return ErrBackupConflict
	}
	if intentPresent {
		if err := b.deleteExactMarker(ctx, intentKey, raw); err != nil {
			return err
		}
	}
	return b.deleteExactMarker(ctx, abortKey, raw)
}

// claimAbort commits the abort tombstone and marker under the authority lock.
// It returns the bound manifest, or nil when nothing remains to abort.
func (b *s3BackupStorage) claimAbort(ctx context.Context, id string) ([]byte, bool, error) {
	poolPeerMu.Lock()
	defer poolPeerMu.Unlock()
	state, err := loadPoolPeer(b.s)
	if err != nil {
		return nil, false, err
	}
	if err := poolAuthorityWritable(state); err != nil {
		return nil, false, err
	}
	if _, err := getCatalogPoint(b.s, b.repo.ID, id); err == nil {
		return nil, false, ErrBackupConflict
	} else if !errors.Is(err, ErrBackupNotFound) {
		return nil, false, err
	}
	pins, err := ActiveBackupRestorePins(b.s)
	if err != nil {
		return nil, false, err
	}
	if pins[id] {
		return nil, false, ErrBackupConflict
	}
	if protected, err := b.Protected(ctx, id); err != nil || protected {
		if err != nil {
			return nil, false, err
		}
		return nil, false, ErrBackupConflict
	}
	if _, deleting, err := b.readDeleteRecord(ctx, id); err != nil {
		return nil, false, err
	} else if deleting {
		return nil, false, ErrBackupConflict
	}
	manifestKey := b.pointKey(id, "manifest.json")
	remoteManifest, manifestErr := b.read(ctx, manifestKey, 2<<20)
	if manifestErr != nil && !isS3Missing(manifestErr) {
		return nil, false, manifestErr
	}
	intentKey, abortKey := b.key(s3BackupIntentPrefix+id), b.key(s3BackupAbortPrefix+id)
	intent, intentErr := b.read(ctx, intentKey, 1<<20)
	if intentErr != nil && !isS3Missing(intentErr) {
		return nil, false, intentErr
	}
	abort, abortErr := b.read(ctx, abortKey, 1<<20)
	if abortErr != nil && !isS3Missing(abortErr) {
		return nil, false, abortErr
	}
	keys, err := b.list(ctx, b.pointKey(id, ""))
	if err != nil {
		return nil, false, err
	}
	if intentErr != nil && abortErr != nil {
		if len(keys) == 0 && manifestErr != nil {
			return nil, false, nil
		}
		if tombstone, ok := b.parseAbortTombstone(id, remoteManifest); ok {
			intent = tombstone.Manifest
		} else {
			return nil, false, ErrBackupConflict
		}
	}
	raw := intent
	if intentErr != nil && abortErr == nil {
		raw = abort
	} else if abortErr == nil && !bytes.Equal(intent, abort) {
		return nil, false, ErrBackupConflict
	}
	manifest, ok := b.recordedManifest(id, raw)
	if !ok {
		return nil, false, ErrBackupConflict
	}
	tombstoneRaw := b.abortTombstone(id, raw)
	if manifestErr == nil && !bytes.Equal(remoteManifest, tombstoneRaw) {
		return nil, false, ErrBackupConflict
	}
	for _, key := range keys {
		if key == manifestKey && bytes.Equal(remoteManifest, tombstoneRaw) {
			continue
		}
		if key == manifestKey || !slices.Contains(b.manifestKeys(manifest), key) {
			return nil, false, ErrBackupConflict
		}
	}
	// The permanent same-key tombstone serializes against every delayed final
	// PutObject(If-None-Match:*). Do not remove any artifact before proving it won.
	if err := b.putExactMarker(ctx, manifestKey, tombstoneRaw); err != nil {
		return nil, false, err
	}
	if abortErr != nil {
		if err := b.putExactMarker(ctx, abortKey, raw); err != nil {
			return nil, false, err
		}
	}
	return raw, intentErr == nil, nil
}

func sha256Base64(raw []byte) string {
	sum := sha256.Sum256(raw)
	return base64.StdEncoding.EncodeToString(sum[:])
}

func (b *s3BackupStorage) putArtifact(ctx context.Context, pointID, name, path string, size int64, digest string) error {
	f, err := openNoFollow(path, syscall.O_RDONLY)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return ErrBackupInvalid
	}
	if _, sum, err := hashLimited(f, size); err != nil {
		return err
	} else if sum != digest {
		return ErrBackupConflict
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// A retry settles on an exact earlier upload, and any other object at the
	// key conflicts, before transferring the artifact again.
	if err := b.statArtifact(ctx, pointID, name, size); err == nil {
		return b.verifyArtifact(ctx, pointID, name, size, digest)
	} else if !isS3Missing(err) {
		return err
	}
	key := b.pointKey(pointID, name)
	uploader := manager.NewUploader(b.client)
	_, err = uploader.Upload(ctx, &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(key),
		Body: f, ContentLength: aws.Int64(size), IfNoneMatch: aws.String("*")})
	if err != nil {
		// The provider's refusal (for example an object over its size limit) is the run's failure reason.
		err = fmt.Errorf("upload %s to S3: %w", name, err)
		if abortErr := b.abortFailedMultipart(err, key); abortErr != nil {
			return errors.Join(err, abortErr)
		}
		if verifyErr := b.verifyArtifact(ctx, pointID, name, size, digest); verifyErr != nil {
			if errors.Is(verifyErr, ErrBackupConflict) {
				return ErrBackupConflict
			}
			return err
		}
		return nil
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
	// copyArtifact verifies each digest while copying.
	stored, _, err := b.StatPoint(ctx, pointID)
	if err != nil {
		return err
	}
	// Compare encodings: DeepEqual fails on time.Time after a JSON round trip.
	storedJSON, _ := json.Marshal(stored)
	manifestJSON, _ := json.Marshal(manifest)
	if !bytes.Equal(storedJSON, manifestJSON) {
		return ErrBackupConflict
	}
	if err := os.MkdirAll(localDir, 0700); err != nil {
		return err
	}
	for _, artifact := range manifestArtifacts(manifest) {
		if err := b.copyArtifact(ctx, pointID, artifact.Filename, localDir, artifact.Size, artifact.SHA256); err != nil {
			return err
		}
	}
	return nil
}

func (b *s3BackupStorage) copyArtifact(ctx context.Context, pointID, name, localDir string, size int64, digest string) error {
	if !filepath.IsAbs(localDir) {
		return ErrBackupInvalid
	}
	return copyVerifiedToLocal(func() (io.ReadCloser, error) {
		out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, name))})
		if err != nil {
			return nil, err
		}
		return out.Body, nil
	}, filepath.Join(localDir, name), size, digest)
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
			// Consoles create an empty "folder/" object; it holds no data.
			if obj.Key != nil && !(*obj.Key == prefix && strings.HasSuffix(prefix, "/") && aws.ToInt64(obj.Size) == 0) {
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
			if _, _, err := b.readDeleteRecord(ctx, id); err != nil {
				return nil, err
			}
			// A prune interrupted after removing every point object has finished
			// in effect; only an incomplete one must settle before rebuild.
			if slices.ContainsFunc(keys, func(other string) bool {
				return other != key && strings.HasPrefix(other, b.pointKey(id, ""))
			}) {
				return nil, ErrBackupConflict
			}
			continue
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
	return slices.Sorted(maps.Keys(seen)), nil
}

func (b *s3BackupStorage) Scan(ctx context.Context) ([]protocol.BackupRecoveryPoint, error) {
	names, err := b.Entries(ctx)
	if err != nil {
		return nil, err
	}
	points := make([]protocol.BackupRecoveryPoint, 0, len(names))
	for _, id := range names {
		manifest, digest, err := b.StatPoint(ctx, id)
		if err != nil {
			return nil, err
		}
		protected, err := b.Protected(ctx, id)
		if err != nil {
			return nil, err
		}
		points = append(points, protocol.BackupRecoveryPoint{ID: id, RepositoryID: b.repo.ID, PoolID: b.repo.PoolID,
			SourceVMID: manifest.SourceVMID, SourceVMName: manifest.SourceVMName, PolicyID: manifest.PolicyID, CapturedAt: manifest.CapturedAt,
			Consistency: manifest.Consistency, ManifestSHA256: digest, Protected: protected})
	}
	return points, nil
}

// readProtectMarker returns the marker's bytes and ETag, or nil when the point is unprotected.
func (b *s3BackupStorage) readProtectMarker(ctx context.Context, pointID string) ([]byte, *string, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: aws.String(b.pointKey(pointID, ".nex-protected"))})
	if isS3Missing(err) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	defer out.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(out.Body, 129))
	if err != nil {
		return nil, nil, err
	}
	if !bytes.HasPrefix(raw, []byte("protected\n"+pointID+"\n")) || len(raw) > 128 {
		return nil, nil, ErrBackupConflict
	}
	return raw, out.ETag, nil
}

func (b *s3BackupStorage) Protected(ctx context.Context, pointID string) (bool, error) {
	if !protocol.ValidSnapshotOperationID(pointID) {
		return false, ErrBackupInvalid
	}
	if err := b.Verify(ctx); err != nil {
		return false, err
	}
	raw, _, err := b.readProtectMarker(ctx, pointID)
	return raw != nil, err
}

// SetProtected replaces or removes only the marker it read, so a stalled request
// cannot undo a newer protect or unprotect.
func (b *s3BackupStorage) SetProtected(ctx context.Context, pointID string, protected bool) error {
	if _, _, err := b.StatPoint(ctx, pointID); err != nil {
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
	current, etag, err := b.readProtectMarker(ctx, pointID)
	if err != nil {
		return err
	}
	key := aws.String(b.pointKey(pointID, ".nex-protected"))
	if !protected {
		if current == nil {
			return nil
		}
		_, err = b.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: key, IfMatch: etag})
	} else {
		put := &s3.PutObjectInput{Bucket: aws.String(b.repo.S3Source.Bucket), Key: key, Body: bytes.NewReader(newBackupProtectMarker(pointID))}
		if current == nil {
			put.IfNoneMatch = aws.String("*")
		} else {
			put.IfMatch = etag
		}
		_, err = b.client.PutObject(ctx, put)
	}
	if isS3Precondition(err) {
		return ErrBackupConflict
	}
	return err
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
		manifest, digest, readErr := b.StatPoint(ctx, pointID)
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
		if sha256Hex(manifestRaw) != digest {
			return ErrBackupConflict
		}
		record = s3BackupDeleteRecord{s3BackupManifestRecord{Repository: b.repo.ID, Pool: b.repo.PoolID, Point: pointID,
			ManifestSHA: digest, Manifest: manifestRaw}, b.manifestKeys(manifest)}
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
		if putErr != nil && !isS3Precondition(putErr) {
			return putErr
		}
		stored, readErr := b.read(ctx, key, 2<<20)
		if readErr != nil || !bytes.Equal(stored, raw) {
			return ErrBackupConflict
		}
	}
	deleteKey := b.pointKey(pointID, s3BackupDeleteKey)
	keys, err := b.list(ctx, b.pointKey(pointID, ""))
	if err != nil {
		return err
	}
	for _, key := range keys {
		if key != deleteKey && !slices.Contains(record.Keys, key) {
			return ErrBackupConflict
		}
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
	} else if !isS3Missing(err) {
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
	if isS3Missing(err) {
		return s3BackupDeleteRecord{}, false, nil
	}
	if err != nil {
		return s3BackupDeleteRecord{}, false, err
	}
	var record s3BackupDeleteRecord
	if json.Unmarshal(raw, &record) != nil || record.Repository != b.repo.ID || record.Pool != b.repo.PoolID ||
		record.Point != pointID || len(record.ManifestSHA) != 64 || len(record.Keys) < 2 || len(record.Keys) > 35 ||
		!slices.IsSorted(record.Keys) {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	if sha256Hex(record.Manifest) != record.ManifestSHA {
		return s3BackupDeleteRecord{}, false, ErrBackupConflict
	}
	manifest, ok := b.recordedManifest(pointID, record.Manifest)
	if !ok || !slices.Equal(record.Keys, b.manifestKeys(manifest)) {
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
		if sha256Hex(manifestRaw) != record.ManifestSHA {
			return s3BackupDeleteRecord{}, false, ErrBackupConflict
		}
	} else if !isS3Missing(err) {
		return s3BackupDeleteRecord{}, false, err
	}
	return record, true, nil
}

func (b *s3BackupStorage) manifestKeys(manifest protocol.BackupManifest) []string {
	keys := []string{b.pointKey(manifest.RecoveryPointID, "manifest.json")}
	for _, artifact := range manifestArtifacts(manifest) {
		keys = append(keys, b.pointKey(manifest.RecoveryPointID, artifact.Filename))
	}
	slices.Sort(keys)
	return keys
}

func isS3Missing(err error) bool { return isS3Code(err, "NoSuchKey", "NotFound") }

func isS3Precondition(err error) bool {
	return isS3Code(err, "PreconditionFailed", "ConditionalRequestConflict")
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
