package vless

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	xlog "github.com/xtls/xray-core/common/log"

	"github.com/mistgate/mistgate/internal/logscrub"
)

var (
	xrayLogOnce       sync.Once
	xrayLogger        atomic.Pointer[slog.Logger]
	xraySessionPrefix = regexp.MustCompile(`^\[\d+\](?:\s|$)`)
	xrayUUID          = regexp.MustCompile(`(?i)\b[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}\b`)
	xrayIP            = regexp.MustCompile(`(?i)\[[0-9a-f:.%]+\]|(?:[0-9]{1,3}\.){3}[0-9]{1,3}|(?:[0-9a-f]{0,4}:){2,7}[0-9a-f]{0,4}`)

	// xrayDest is a destination as xray writes it (net.Destination): tcp:host:port, an IPv6 host in brackets.
	xrayDest = regexp.MustCompile(`\b(tcp|udp):(\[[0-9A-Fa-f:.%]+\]|[^\s:\[\](),;>]+):([0-9]{1,5})\b`)
	// Names that are not always a dotted host: REALITY's refusal reason, the fallback's real SNI and path, a sniffed domain, an
	// account name.
	xraySNI     = regexp.MustCompile(`(?i)\b(server name mismatch: |(?:realName|serverName|sni)\s*[=:]\s*)\S+`)
	xrayPath    = regexp.MustCompile(`(?i)\b(realPath\s*[=:]\s*)\S+`)
	xraySniffed = regexp.MustCompile(`(?i)\b(sniffed domain: )\S+`)
	xrayAccount = regexp.MustCompile(`(?i)\b(email[:=]\s*)\S+`)
)

type xrayLogHandler struct{}

func installXrayLogging(logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	// The source sits on the logger, not on each record: the agent answers the logger's Enabled(Debug) from its capture state,
	// by source.
	xrayLogger.Store(logger.With("source", "xray"))
	xrayLogOnce.Do(func() { xlog.RegisterHandler(xrayLogHandler{}) })
}

// capturing returns the logger while a debug capture asks for xray's connection lines, nil otherwise. A capture raises the
// logger's Debug level for what it wants (agent/debugcap.go); nothing else does, bar the node's own debug flag.
func capturing() *slog.Logger {
	if l := xrayLogger.Load(); l != nil && l.Enabled(context.Background(), slog.LevelDebug) {
		return l
	}
	return nil
}

// Severity (xlog.SeverityLogger) is what xray asks before it builds a message. Info is the least this node needs, because
// REALITY's verdict is an Info line; Debug is wanted only while a capture runs. Without it xray builds every Debug line just
// for Handle to drop it. The level changes on the fly: no listener is restarted.
func (xrayLogHandler) Severity() xlog.Severity {
	if capturing() != nil {
		return xlog.Severity_Debug
	}
	return xlog.Severity_Info
}

func (xrayLogHandler) Handle(message xlog.Message) {
	switch m := message.(type) {
	case *xlog.GeneralMessage:
		handleGeneral(m)
	case *xlog.AccessMessage:
		// A session's acceptance or refusal. The client's address and the account are left out of the line altogether.
		if l := capturing(); l != nil {
			l.Debug(redactXrayCapture(accessLine(m)))
		}
	}
}

func handleGeneral(general *xlog.GeneralMessage) {
	var level slog.Level
	switch general.Severity {
	case xlog.Severity_Error:
		level = slog.LevelError
	case xlog.Severity_Warning:
		level = slog.LevelWarn
	case xlog.Severity_Info:
		// REALITY's verdict on a connection it refused is an Info line (links.go)
		realityVerdict(fmt.Sprint(general.Content), time.Now())
		capture(general)
		return
	case xlog.Severity_Debug:
		capture(general)
		return
	default:
		return
	}
	content := strings.TrimSpace(fmt.Sprint(general.Content))
	if dropXrayNoise(content) {
		return
	}
	if xraySessionPrefix.MatchString(content) {
		capture(general)
		return
	}
	logger := xrayLogger.Load()
	if logger == nil {
		logger = slog.Default()
	}
	logger.Log(context.Background(), level, redactXrayIdentifiers(general.String()))
}

// capture keeps a line the node never logs (a connection's) while a capture asks for xray: cleaned, at Debug, so it reaches the
// capture's buffer only (the ring takes Info and above, the process log what its own level lets through).
func capture(general *xlog.GeneralMessage) {
	l := capturing()
	if l == nil || dropXrayNoise(strings.TrimSpace(fmt.Sprint(general.Content))) {
		return
	}
	l.Debug(redactXrayCapture(general.String()))
}

// accessLine is an AccessMessage without its From (the client) and Email (the account).
func accessLine(m *xlog.AccessMessage) string {
	parts := []string{"[Access]", string(m.Status), fmt.Sprint(m.To), "", fmt.Sprint(m.Reason)}
	if m.To == nil {
		parts[2] = ""
	}
	if m.Reason == nil {
		parts[4] = ""
	}
	if m.Detour != "" {
		parts[3] = "[" + m.Detour + "]"
	}
	out := parts[:0]
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " ")
}

// redactXrayCapture cleans a line for a debug capture: no client address, key, account, destination host or SNI. The session
// number stays (it links the lines of one connection), and so does a destination's port.
func redactXrayCapture(s string) string {
	s = xrayDest.ReplaceAllStringFunc(s, func(m string) string {
		parts := xrayDest.FindStringSubmatch(m)
		label := logscrub.Host
		if _, err := netip.ParseAddr(strings.Trim(parts[2], "[]")); err == nil {
			label = logscrub.IP
		}
		return parts[1] + ":" + label + ":" + parts[3]
	})
	s = xraySNI.ReplaceAllString(s, "${1}<sni>")
	s = xrayPath.ReplaceAllString(s, "${1}<path>")
	s = xraySniffed.ReplaceAllString(s, "${1}"+logscrub.Host)
	s = xrayAccount.ReplaceAllString(s, "${1}<user>")
	s = logscrub.Addrs(logscrub.Emails(s))
	return logscrub.Hosts(redactXrayIdentifiers(s))
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
