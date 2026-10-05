package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	r2BackupPrefix      = "mistgate/backups/v1/"
	r2ProbePrefix       = "mistgate/backups/.probe/"
	listPageSize        = 1000
	maxListedItems      = 100000
	r2SingleUploadLimit = int64(64 << 20)
	r2MultipartPartSize = int64(16 << 20)
	r2MaxMultipartParts = int64(10000)
)

type s3API interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
}

type backupObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

func s3Endpoint(accountID, jurisdiction string) (string, error) {
	if !accountIDPattern.MatchString(accountID) {
		return "", errors.New("backup: invalid R2 account id")
	}
	switch jurisdiction {
	case "", "default":
		return "https://" + accountID + ".r2.cloudflarestorage.com", nil
	case "eu", "us", "fedramp":
		return "https://" + accountID + "." + jurisdiction + ".r2.cloudflarestorage.com", nil
	default:
		return "", errors.New("backup: invalid R2 jurisdiction")
	}
}

func newS3API(ctx context.Context, cfg store.PanelBackupSettings, decryptSecret func([]byte) ([]byte, error)) (s3API, error) {
	if cfg.Bucket == "" || cfg.AccessKeyID == "" || len(cfg.SecretAccessKey) == 0 {
		return nil, errBackupNotConfigured
	}
	if !bucketPattern.MatchString(cfg.Bucket) {
		return nil, errBackupInvalidSettings
	}
	endpoint, err := s3Endpoint(cfg.AccountID, cfg.Jurisdiction)
	if err != nil {
		return nil, errBackupInvalidSettings
	}
	secret, err := decryptSecret(cfg.SecretAccessKey)
	if err != nil || len(secret) == 0 {
		return nil, errBackupInvalidSettings
	}
	defer clear(secret)
	awsConfig, err := config.LoadDefaultConfig(ctx,
		config.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, string(secret), "")),
		config.WithRegion("auto"),
	)
	if err != nil {
		return nil, errBackupStorageFailed
	}
	return s3.NewFromConfig(awsConfig, func(options *s3.Options) {
		options.BaseEndpoint = aws.String(endpoint)
		options.UsePathStyle = true
	}), nil
}

// testStorage checks what backups need of the bucket: it lists the backups, writes a probe object, reads it back (a
// restore downloads, so a token that can only write is not enough) and deletes it (retention deletes).
func testStorage(ctx context.Context, api s3API, bucket string) error {
	if _, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(r2BackupPrefix), MaxKeys: aws.Int32(1)}); err != nil {
		return errBackupStorageFailed
	}
	id, err := randomObjectID()
	if err != nil {
		return errBackupStorageFailed
	}
	const content = "mistgate r2 test\n"
	probe := r2ProbePrefix + id
	if _, err := api.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String(probe), Body: strings.NewReader(content), ContentType: aws.String("text/plain")}); err != nil {
		return errBackupStorageFailed
	}
	readErr := errBackupStorageFailed
	if out, err := api.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(probe)}); err == nil {
		got, err := io.ReadAll(io.LimitReader(out.Body, int64(len(content))+1))
		out.Body.Close()
		if err == nil && string(got) == content {
			readErr = nil
		}
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := api.DeleteObject(cleanupCtx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(probe)}); err != nil {
		return errBackupStorageDeleteFailed
	}
	return readErr
}

func uploadBackup(ctx context.Context, api s3API, bucket, key, filePath string) (int64, error) {
	return uploadBackupWithLimits(ctx, api, bucket, key, filePath, r2SingleUploadLimit, r2MultipartPartSize)
}

func uploadBackupWithLimits(ctx context.Context, api s3API, bucket, key, filePath string, singleLimit, partSize int64) (int64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return 0, errBackupArchiveFailed
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 {
		return 0, errBackupArchiveFailed
	}
	if info.Size() <= singleLimit {
		_, err = api.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: f,
			ContentLength: aws.Int64(info.Size()), ContentType: aws.String("application/octet-stream"),
		})
		if err != nil {
			return 0, errBackupStorageFailed
		}
		return info.Size(), nil
	}
	if partSize < 5<<20 || (info.Size()+partSize-1)/partSize > r2MaxMultipartParts {
		return 0, errBackupArchiveFailed
	}
	created, err := api.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), ContentType: aws.String("application/octet-stream"),
	})
	if err != nil || created == nil || created.UploadId == nil || *created.UploadId == "" {
		return 0, errBackupStorageFailed
	}
	uploadID := *created.UploadId
	parts := make([]s3types.CompletedPart, 0, (info.Size()+partSize-1)/partSize)
	for offset, number := int64(0), int32(1); offset < info.Size(); number++ {
		length := min(partSize, info.Size()-offset)
		body := io.NewSectionReader(f, offset, length)
		part, err := api.UploadPart(ctx, &s3.UploadPartInput{
			Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
			PartNumber: aws.Int32(number), Body: body, ContentLength: aws.Int64(length),
		})
		if err != nil || part == nil || part.ETag == nil || *part.ETag == "" {
			abortMultipart(api, bucket, key, uploadID)
			return 0, errBackupStorageFailed
		}
		parts = append(parts, s3types.CompletedPart{ETag: part.ETag, PartNumber: aws.Int32(number)})
		offset += length
	}
	completed := &s3types.CompletedMultipartUpload{Parts: parts}
	if _, err := api.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID), MultipartUpload: completed,
	}); err != nil {
		abortMultipart(api, bucket, key, uploadID)
		return 0, errBackupStorageFailed
	}
	return info.Size(), nil
}

func abortMultipart(api s3API, bucket, key, uploadID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _ = api.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket: aws.String(bucket), Key: aws.String(key), UploadId: aws.String(uploadID),
	})
}

func randomObjectID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func backupObjectKey(now time.Time) (string, error) {
	id, err := randomObjectID()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s%04d/%02d/%s-%s.tar.gz.age", r2BackupPrefix, now.UTC().Year(), now.UTC().Month(), now.UTC().Format("20060102T150405Z"), id), nil
}

func listBackupObjects(ctx context.Context, api s3API, bucket string) ([]backupObject, error) {
	var objects []backupObject
	var token *string
	for {
		out, err := api.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String(r2BackupPrefix), MaxKeys: aws.Int32(listPageSize), ContinuationToken: token})
		if err != nil {
			return nil, errBackupStorageFailed
		}
		for _, item := range out.Contents {
			if item.Key == nil || !strings.HasPrefix(*item.Key, r2BackupPrefix) || !strings.HasSuffix(*item.Key, ".tar.gz.age") {
				continue
			}
			object := backupObject{Key: *item.Key}
			if item.Size != nil {
				object.Size = *item.Size
			}
			if item.LastModified != nil {
				object.LastModified = item.LastModified.UTC()
			}
			objects = append(objects, object)
			if len(objects) > maxListedItems {
				return nil, errors.New("backup: object listing exceeds the safety limit")
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			break
		}
		if out.NextContinuationToken == nil || *out.NextContinuationToken == "" {
			return nil, errBackupStorageFailed
		}
		token = out.NextContinuationToken
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].LastModified.Equal(objects[j].LastModified) {
			return objects[i].Key > objects[j].Key
		}
		return objects[i].LastModified.After(objects[j].LastModified)
	})
	return objects, nil
}

func pruneBackups(ctx context.Context, api s3API, bucket, keepKey string, retentionDays int, now time.Time) error {
	if retentionDays == 0 {
		return nil
	}
	objects, err := listBackupObjects(ctx, api, bucket)
	if err != nil {
		return err
	}
	cutoff := now.Add(-time.Duration(retentionDays) * 24 * time.Hour)
	for _, item := range objects {
		if item.Key == keepKey || item.LastModified.IsZero() || !item.LastModified.Before(cutoff) {
			continue
		}
		if _, err := api.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String(item.Key)}); err != nil {
			return errBackupStorageDeleteFailed
		}
	}
	return nil
}

func recentBackups(ctx context.Context, api s3API, bucket string, limit int) ([]backupObject, error) {
	if limit < 1 {
		return nil, nil
	}
	objects, err := listBackupObjects(ctx, api, bucket)
	if err != nil {
		return nil, err
	}
	if len(objects) > limit {
		objects = objects[:limit]
	}
	return objects, nil
}
