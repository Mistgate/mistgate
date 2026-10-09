package vless

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"

	xlog "github.com/xtls/xray-core/common/log"
)

var (
	xrayLogOnce       sync.Once
	xrayLogger        atomic.Pointer[slog.Logger]
	xraySessionPrefix = regexp.MustCompile(`^\[\d+\](?:\s|$)`)
	xrayUUID          = regexp.MustCompile(`(?i)\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b`)
	xrayIP            = regexp.MustCompile(`(?i)\[[0-9a-f:.%]+\]|(?:[0-9]{1,3}\.){3}[0-9]{1,3}|(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}`)
)

type xrayLogHandler struct{}

func installXrayLogging(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	xrayLogger.Store(logger)
	xrayLogOnce.Do(func() { xlog.RegisterHandler(xrayLogHandler{}) })
}

func (xrayLogHandler) Handle(message xlog.Message) {
	general, ok := message.(*xlog.GeneralMessage)
	if !ok {
		return
	}
	var level slog.Level
	switch general.Severity {
	case xlog.Severity_Error:
		level = slog.LevelError
	case xlog.Severity_Warning:
		level = slog.LevelWarn
	default:
		return
	}
	content := strings.TrimSpace(fmt.Sprint(general.Content))
	if xraySessionPrefix.MatchString(content) || dropXrayNoise(content) {
		return
	}
	logger := xrayLogger.Load()
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, redactXrayIdentifiers(general.String()), "source", "xray")
}

func dropXrayNoise(content string) bool {
	if strings.HasPrefix(content, "core: Xray ") && strings.HasSuffix(content, " started") {
		return true
	}
	return strings.Contains(content, "failed to serve HTTP for XHTTP") && strings.Contains(content, "use of closed network connection")
}

func redactXrayIdentifiers(content string) string {
	content = xrayUUID.ReplaceAllString(content, "[redacted]")
	return xrayIP.ReplaceAllStringFunc(content, func(candidate string) string {
		ip := strings.Trim(candidate, "[]")
		if net.ParseIP(ip) != nil {
			return "[redacted]"
		}
		return candidate
	})
}
