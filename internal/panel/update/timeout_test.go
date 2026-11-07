package update

import (
	"testing"
	"time"

	"connectrpc.com/connect"
	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
)

func TestUpdateDownloadTimeoutSetting(t *testing.T) {
	e := newEnv(t)
	if got := e.s.downloadTimeoutOrDefault(e.ctx); got != defaultUpdateDownloadTimeout {
		t.Fatalf("default timeout = %s", got)
	}
	resp, err := (rpc{e.s}).SetUpdateDownloadTimeout(e.ctx, connect.NewRequest(&adminv1.SetUpdateDownloadTimeoutRequest{TimeoutSeconds: 3600}))
	if err != nil || resp.Msg.TimeoutSeconds != 3600 {
		t.Fatalf("set timeout = %+v, %v", resp, err)
	}
	if got := e.s.downloadTimeoutOrDefault(e.ctx); got != time.Hour {
		t.Fatalf("saved timeout = %s", got)
	}
	if _, err := (rpc{e.s}).SetUpdateDownloadTimeout(e.ctx, connect.NewRequest(&adminv1.SetUpdateDownloadTimeoutRequest{TimeoutSeconds: 1})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid timeout error = %v", err)
	}
}
