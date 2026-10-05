package backup

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

type fakeS3 struct {
	putCalls     int
	listCalls    int
	getCalls     int
	deleted      []string
	partSizes    []int64
	completed    *s3types.CompletedMultipartUpload
	aborted      bool
	failPart     int32
	failComplete bool
	failGet      bool
	objects      []s3types.Object
	lastPut      []byte
	onPut        func() // called while the body of a PutObject is read
}

func (f *fakeS3) PutObject(_ context.Context, input *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.putCalls++
	if f.onPut != nil {
		f.onPut()
	}
	if input.Body != nil {
		b, err := io.ReadAll(input.Body)
		if err != nil {
			return nil, err
		}
		f.lastPut = b
	}
	return &s3.PutObjectOutput{}, nil
}

func (f *fakeS3) GetObject(_ context.Context, _ *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.getCalls++
	if f.failGet {
		return nil, errors.New("injected read failure")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(f.lastPut))}, nil
}

func (f *fakeS3) CreateMultipartUpload(_ context.Context, _ *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-1")}, nil
}

func (f *fakeS3) UploadPart(_ context.Context, input *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if input.PartNumber != nil && *input.PartNumber == f.failPart {
		return nil, errors.New("injected part failure")
	}
	n, err := io.Copy(io.Discard, input.Body)
	if err != nil {
		return nil, err
	}
	f.partSizes = append(f.partSizes, n)
	return &s3.UploadPartOutput{ETag: aws.String(fmt.Sprintf("\"part-%d\"", aws.ToInt32(input.PartNumber)))}, nil
}

func (f *fakeS3) CompleteMultipartUpload(_ context.Context, input *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.completed = input.MultipartUpload
	if f.failComplete {
		return nil, errors.New("injected completion failure")
	}
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeS3) AbortMultipartUpload(_ context.Context, _ *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	f.aborted = true
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (f *fakeS3) ListObjectsV2(_ context.Context, _ *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.listCalls++
	return &s3.ListObjectsV2Output{Contents: f.objects}, nil
}

func (f *fakeS3) DeleteObject(_ context.Context, input *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(input.Key))
	return &s3.DeleteObjectOutput{}, nil
}

func TestS3EndpointJurisdictions(t *testing.T) {
	const account = "0123456789abcdef0123456789abcdef"
	for _, tc := range []struct{ jurisdiction, want string }{
		{"", "https://" + account + ".r2.cloudflarestorage.com"},
		{"default", "https://" + account + ".r2.cloudflarestorage.com"},
		{"eu", "https://" + account + ".eu.r2.cloudflarestorage.com"},
		{"us", "https://" + account + ".us.r2.cloudflarestorage.com"},
		{"fedramp", "https://" + account + ".fedramp.r2.cloudflarestorage.com"},
	} {
		got, err := s3Endpoint(account, tc.jurisdiction)
		if err != nil || got != tc.want {
			t.Errorf("s3Endpoint(%q) = %q, %v; want %q", tc.jurisdiction, got, err, tc.want)
		}
	}
	if _, err := s3Endpoint("not-an-account", "default"); err == nil {
		t.Error("invalid account ID was accepted")
	}
	if _, err := s3Endpoint(account, "unknown"); err == nil {
		t.Error("unknown jurisdiction was accepted")
	}
}

// The storage test proves what a restore needs too: the probe it wrote reads back. A token that cannot read fails the
// test, and the probe is deleted either way.
func TestStorageTestReadsTheProbeBack(t *testing.T) {
	ok := &fakeS3{}
	if err := testStorage(context.Background(), ok, "bucket"); err != nil || ok.getCalls != 1 || len(ok.deleted) != 1 {
		t.Fatalf("test = %v, reads %d, deleted %v", err, ok.getCalls, ok.deleted)
	}
	noRead := &fakeS3{failGet: true}
	if err := testStorage(context.Background(), noRead, "bucket"); !errors.Is(err, errBackupStorageFailed) {
		t.Errorf("a bucket that cannot be read passed the test: %v", err)
	}
	if len(noRead.deleted) != 1 {
		t.Errorf("the probe was left behind: deleted %v", noRead.deleted)
	}
}

func TestUploadBackupUsesMultipartAndAbortsFailures(t *testing.T) {
	partSize := int64(5 << 20)
	archivePath := filepath.Join(t.TempDir(), "backup.age")
	if err := os.WriteFile(archivePath, make([]byte, 2*partSize+17), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &fakeS3{}
	size, err := uploadBackupWithLimits(context.Background(), api, "bucket", "backup-key", archivePath, partSize, partSize)
	if err != nil || size != 2*partSize+17 {
		t.Fatalf("multipart upload = %d, %v", size, err)
	}
	if api.putCalls != 0 || api.aborted || api.completed == nil || len(api.completed.Parts) != 3 {
		t.Fatalf("multipart lifecycle: puts=%d aborted=%v complete=%+v", api.putCalls, api.aborted, api.completed)
	}
	if !reflect.DeepEqual(api.partSizes, []int64{partSize, partSize, 17}) {
		t.Errorf("part sizes = %v", api.partSizes)
	}

	failing := &fakeS3{failPart: 2}
	if _, err := uploadBackupWithLimits(context.Background(), failing, "bucket", "failed-key", archivePath, partSize, partSize); !errors.Is(err, errBackupStorageFailed) {
		t.Errorf("part failure = %v", err)
	}
	if !failing.aborted || failing.completed != nil {
		t.Errorf("failed upload was not aborted: aborted=%v complete=%+v", failing.aborted, failing.completed)
	}

	badPartSize := &fakeS3{}
	if _, err := uploadBackupWithLimits(context.Background(), badPartSize, "bucket", "bad-key", archivePath, partSize, 1024); !errors.Is(err, errBackupArchiveFailed) {
		t.Errorf("invalid multipart size = %v", err)
	}
}

func TestUploadBackupUsesPutForSmallFile(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "backup.age")
	if err := os.WriteFile(archivePath, []byte("small archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := &fakeS3{}
	size, err := uploadBackupWithLimits(context.Background(), api, "bucket", "backup-key", archivePath, 1024, 5<<20)
	if err != nil || size != int64(len("small archive")) || api.putCalls != 1 || api.completed != nil {
		t.Fatalf("single upload = %d, %v, puts=%d complete=%+v", size, err, api.putCalls, api.completed)
	}
}

func TestBackupRetentionOnlyDeletesObjectsOutsidePolicy(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	old := now.Add(-8 * 24 * time.Hour)
	recent := now.Add(-6 * 24 * time.Hour)
	api := &fakeS3{objects: []s3types.Object{
		{Key: aws.String(r2BackupPrefix + "keep.tar.gz.age"), LastModified: aws.Time(old)},
		{Key: aws.String(r2BackupPrefix + "old.tar.gz.age"), LastModified: aws.Time(old)},
		{Key: aws.String(r2BackupPrefix + "recent.tar.gz.age"), LastModified: aws.Time(recent)},
		{Key: aws.String("unrelated/object.tar.gz.age"), LastModified: aws.Time(old)},
	}}
	if err := pruneBackups(context.Background(), api, "bucket", r2BackupPrefix+"keep.tar.gz.age", 7, now); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.deleted, []string{r2BackupPrefix + "old.tar.gz.age"}) {
		t.Fatalf("deleted objects = %v", api.deleted)
	}
	if err := pruneBackups(context.Background(), api, "bucket", "", 0, now); err != nil {
		t.Fatal(err)
	}
	if api.listCalls != 1 {
		t.Fatalf("retention=0 listed objects %d times, want no additional read", api.listCalls)
	}
}
