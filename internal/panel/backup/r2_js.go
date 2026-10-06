//go:build js

package backup

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const r2BackupPrefix = "mistgate/backups/v1/"

var errEdgeBackupUnavailable = errors.New("off-site backup on the edge comes with the R2 binding (phase 2)")

type s3API interface{}

type backupObject struct {
	Key          string
	Size         int64
	LastModified time.Time
}

func s3Endpoint(string, string) (string, error) { return "", nil }

func newS3API(context.Context, store.PanelBackupSettings, func([]byte) ([]byte, error)) (s3API, error) {
	return nil, errEdgeBackupUnavailable
}

func checkBackupBucket(context.Context, s3API, string) error { return errEdgeBackupUnavailable }
func testStorage(context.Context, s3API, string) error       { return errEdgeBackupUnavailable }
func uploadBackup(context.Context, s3API, string, string, string) (int64, error) {
	return 0, errEdgeBackupUnavailable
}
func pruneBackups(context.Context, s3API, string, string, int, time.Time) error {
	return errEdgeBackupUnavailable
}
func recentBackups(context.Context, s3API, string, int) ([]backupObject, error) {
	return nil, errEdgeBackupUnavailable
}
func backupObjectKey(time.Time) (string, error) { return "", errEdgeBackupUnavailable }

func edgeRPCUnavailable() error {
	return connect.NewError(connect.CodeFailedPrecondition, errEdgeBackupUnavailable)
}
