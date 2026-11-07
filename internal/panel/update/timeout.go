package update

import (
	"context"
	"errors"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/mistgate/mistgate/internal/panel/store"
)

const (
	updateDownloadTimeoutKey     = "update_download_timeout_seconds"
	defaultUpdateDownloadTimeout = 30 * time.Minute
	minUpdateDownloadTimeout     = 5 * time.Minute
	maxUpdateDownloadTimeout     = 2 * time.Hour
)

func validUpdateDownloadTimeout(d time.Duration) bool {
	return d >= minUpdateDownloadTimeout && d <= maxUpdateDownloadTimeout
}

func (s *Service) updateDownloadTimeout(ctx context.Context) (time.Duration, error) {
	raw, err := s.st.Setting(ctx, updateDownloadTimeoutKey)
	if errors.Is(err, store.ErrNotFound) {
		return defaultUpdateDownloadTimeout, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return defaultUpdateDownloadTimeout, nil
	}
	d := time.Duration(n) * time.Second
	if !validUpdateDownloadTimeout(d) {
		return defaultUpdateDownloadTimeout, nil
	}
	return d, nil
}

func (s *Service) downloadTimeoutOrDefault(ctx context.Context) time.Duration {
	d, err := s.updateDownloadTimeout(ctx)
	if err != nil {
		s.log.Warn("load update download timeout", "err", err)
		return defaultUpdateDownloadTimeout
	}
	return d
}

func (s *Service) setUpdateDownloadTimeout(ctx context.Context, seconds uint32) error {
	d := time.Duration(seconds) * time.Second
	if !validUpdateDownloadTimeout(d) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("download timeout must be between 5 minutes and 2 hours"))
	}
	if err := s.st.SetSettings(ctx, map[string]string{updateDownloadTimeoutKey: strconv.FormatInt(int64(seconds), 10)}); err != nil {
		return s.internal("save update download timeout", err)
	}
	s.audit(ctx, "update_download_timeout", map[string]string{"seconds": strconv.FormatUint(uint64(seconds), 10)})
	return nil
}
