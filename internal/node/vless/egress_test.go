package vless

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/xtls/xray-core/common/buf"

	"github.com/mistgate/mistgate/internal/node/ratelimit"
)

func TestUploadDoesNotConsumeDownloadRateLimit(t *testing.T) {
	limit := ratelimit.New(8)
	cred := &credState{}
	cred.limit.Store(limit)

	writer := countWriter{Writer: buf.NewWriter(io.Discard), cred: cred, up: true, ctx: context.Background()}
	if err := writer.WriteMultiBuffer(buf.MultiBuffer{buf.FromBytes([]byte{1})}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if !limit.WaitDown(ctx, 64*1024) {
		t.Fatal("upload consumed tokens from the download bucket")
	}
}
