package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/awg"
)

const (
	kernelCaptureSource = "kernel"
	kernelLineMaxBytes  = 1 << 20
	// kernelHandshakesMax bounds the handshake events an interface keeps for its unanswered count (about 3 MB at most on a
	// node with 1 GB): a flood of initiations must not grow the agent's memory for 15 minutes. Past it the count is partial
	// and the summary says so.
	kernelHandshakesMax = 100_000
)

type kernelJournalProcess interface {
	io.ReadCloser
	Wait() error
}

type kernelJournalRunner func(context.Context, string, ...string) (kernelJournalProcess, error)

type kernelDebugOptions struct {
	journalctlPath    string
	dynamicDebugPaths []string
	messageCostPath   string
	startJournal      kernelJournalRunner
}

type kernelDebugEnvironment struct {
	opts kernelDebugOptions
}

type kernelDebugLease struct {
	debugOn bool
	begin   []error
	closeFn func() []error
}

func (l *kernelDebugLease) close() []error {
	if l == nil || l.closeFn == nil {
		return nil
	}
	return l.closeFn()
}

type awgKernelDebugEngine interface {
	KernelDebugInfo() (bool, []awg.KernelDebugInterface)
	WatchKernelUnknownPeers(func(string)) (bool, func())
}

type parsedKernelLine struct {
	at       time.Time
	message  string
	priority int
}

func parseKernelJournalLine(line []byte) (parsedKernelLine, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(line, &fields); err != nil {
		return parsedKernelLine{}, fmt.Errorf("invalid journal JSON: %w", err)
	}
	message, err := parseJournalMessage(fields["MESSAGE"])
	if err != nil {
		return parsedKernelLine{}, err
	}
	realtime, err := parseJournalInt(fields["__REALTIME_TIMESTAMP"])
	if err != nil || realtime <= 0 {
		return parsedKernelLine{}, errors.New("journal entry has an invalid realtime timestamp")
	}
	priority, err := parseJournalInt(fields["PRIORITY"])
	if err != nil || priority < 0 || priority > 7 {
		return parsedKernelLine{}, errors.New("journal entry has an invalid priority")
	}
	return parsedKernelLine{at: time.UnixMicro(realtime), message: message, priority: int(priority)}, nil
}

func parseJournalMessage(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("journal entry has no message")
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var values []json.RawMessage
	if err := json.Unmarshal(raw, &values); err != nil {
		return "", errors.New("journal message is neither text nor a byte array")
	}
	bytes := make([]byte, len(values))
	for i, value := range values {
		n, err := parseJournalInt(value)
		if err != nil || n < 0 || n > 255 {
			return "", errors.New("journal message has an invalid byte array")
		}
		bytes[i] = byte(n)
	}
	if !utf8.Valid(bytes) { // Keep non-UTF-8 bytes visible without placing invalid UTF-8 in a protobuf string.
		return strconv.QuoteToASCII(string(bytes)), nil
	}
	return string(bytes), nil
}

func parseJournalInt(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, errors.New("missing integer")
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return strconv.ParseInt(text, 10, 64)
	}
	var number json.Number
	if json.Unmarshal(raw, &number) == nil {
		return number.Int64()
	}
	return 0, errors.New("invalid integer")
}

func kernelLevel(priority int) slog.Level {
	switch {
	case priority <= 3:
		return slog.LevelError
	case priority == 4:
		return slog.LevelWarn
	case priority <= 6:
		return slog.LevelInfo
	default:
		return slog.LevelDebug
	}
}

func captureSeverity(level slog.Level) pb.Severity {
	switch {
	case level >= slog.LevelError:
		return pb.Severity_SEVERITY_ERROR
	case level >= slog.LevelWarn:
		return pb.Severity_SEVERITY_WARNING
	case level <= slog.LevelDebug:
		return pb.Severity_SEVERITY_DEBUG
	default:
		return pb.Severity_SEVERITY_INFO
	}
}

func firewallPacketLog(message string) bool {
	return strings.Contains(message, " SRC=") && strings.Contains(message, " DST=") && strings.Contains(message, " PROTO=")
}

var (
	kernelPeerNumber = regexp.MustCompile(`\bpeer ([0-9]+)\b`)
	kernelSuppressed = regexp.MustCompile(`net_ratelimit: ([0-9]+) callbacks suppressed`)
)

var awgKernelEvents = []string{
	"initiation_in", "response_out", "initiation_out", "response_in", "invalid_initiation", "invalid_response",
	"cookie_in", "cookie_out", "keepalive_in", "keepalive_out", "handshake_retry", "unallowed_src",
	"keypair_created", "keypair_destroyed", "unknown_peer", "other",
}

func classifyAWGKernelLine(message string) (event, peer string) {
	switch {
	case strings.Contains(message, "Invalid handshake initiation from"):
		event = "invalid_initiation"
	case strings.Contains(message, "Invalid handshake response from"):
		event = "invalid_response"
	case strings.Contains(message, "Receiving handshake initiation from peer "):
		event = "initiation_in"
	case strings.Contains(message, "Sending handshake response to peer "):
		event = "response_out"
	case strings.Contains(message, "Sending handshake initiation to peer "):
		event = "initiation_out"
	case strings.Contains(message, "Receiving handshake response from peer "):
		event = "response_in"
	case strings.Contains(message, "Receiving cookie response from"):
		event = "cookie_in"
	case strings.Contains(message, "Sending cookie response"):
		event = "cookie_out"
	case strings.Contains(message, "Receiving keepalive packet from peer "):
		event = "keepalive_in"
	case strings.Contains(message, "Sending keepalive packet to peer "):
		event = "keepalive_out"
	case strings.Contains(message, "Handshake for peer ") && strings.Contains(message, " did not complete after "):
		event = "handshake_retry"
	case strings.Contains(message, "Packet has unallowed src IP"):
		event = "unallowed_src"
	case strings.Contains(message, "Keypair ") && strings.Contains(message, " created for peer "):
		event = "keypair_created"
	case strings.Contains(message, "Keypair ") && strings.Contains(message, " destroyed for peer "):
		event = "keypair_destroyed"
	default:
		event = "other"
	}
	if match := kernelPeerNumber.FindStringSubmatch(message); len(match) == 2 {
		peer = match[1]
	}
	return event, peer
}

func kernelInterfaceMessage(message string, interfaces map[string]string) (ifname, inboundID, body string, ok bool) {
	for name, id := range interfaces {
		prefix := name + ":"
		if strings.HasPrefix(message, prefix) {
			return name, id, strings.TrimSpace(strings.TrimPrefix(message, prefix)), true
		}
	}
	return "", "", "", false
}

type kernelHandshakeEvent struct {
	at    time.Time
	peer  string
	event string
}

func unansweredKernelInitiations(events []kernelHandshakeEvent) uint64 {
	events = append([]kernelHandshakeEvent(nil), events...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].at.Before(events[j].at) })
	pending := map[string][]time.Time{}
	var unanswered uint64
	for _, event := range events {
		if event.peer == "" {
			continue
		}
		queue := pending[event.peer]
		for len(queue) > 0 && event.at.Sub(queue[0]) > 5*time.Second {
			unanswered++
			queue = queue[1:]
		}
		switch event.event {
		case "initiation_in":
			queue = append(queue, event.at)
		case "response_out":
			if len(queue) > 0 && !event.at.Before(queue[0]) && event.at.Sub(queue[0]) <= 5*time.Second {
				queue = queue[1:]
			}
		}
		pending[event.peer] = queue
	}
	for _, queue := range pending {
		unanswered += uint64(len(queue))
	}
	return unanswered
}

type kernelInterfaceStats struct {
	inbound    string
	counts     map[string]uint64
	handshakes []kernelHandshakeEvent
	cut        bool // kernelHandshakesMax was reached: initiations_unanswered counts only the first ones
}

type kernelCaptureCollector struct {
	d            *debugState
	c            *debugCapture
	env          *kernelDebugEnvironment
	ctx          context.Context
	cancel       context.CancelFunc
	kernelWanted bool
	awgWanted    bool
	interfaces   map[string]*kernelInterfaceStats
	debugOn      bool
	lease        *kernelDebugLease
	stopUnknown  func()
	process      kernelJournalProcess
	readerDone   chan struct{}
	warnings     map[string]kernelCaptureWarning
	mu           sync.Mutex
	kept         uint64
	firewall     uint64
	dropped      uint64
	suppressed   uint64
	closed       bool
}

type kernelCaptureWarning struct {
	source string
	reason string
}

func sourceWanted(sources map[string]bool, source string) bool {
	return sources == nil || sources[source]
}

func (a *Agent) awgKernelSnapshot() (awgKernelDebugEngine, bool, []awg.KernelDebugInterface) {
	e, ok := a.engines[awg.Protocol].(awgKernelDebugEngine)
	if !ok {
		return nil, false, nil
	}
	kernel, interfaces := e.KernelDebugInfo()
	return e, kernel, interfaces
}

func (a *Agent) resetKernelDebugAtStart() {
	_, kernel, _ := a.awgKernelSnapshot()
	if !kernel {
		return
	}
	if errs := a.kernelDebug.resetAtStart(); len(errs) != 0 {
		a.log.Warn("AWG kernel debug reset could not be completed", "code", "awg_kernel_debug_reset", "reason", errors.Join(errs...).Error())
	}
}

func (a *Agent) prepareKernelCapture(c *debugCapture) {
	kernelWanted := sourceWanted(c.sources, kernelCaptureSource)
	awgWanted := sourceWanted(c.sources, "awg")
	e, kernelBackend, activeInterfaces := a.awgKernelSnapshot()
	needAWGReader := awgWanted && kernelBackend && len(activeInterfaces) > 0
	if !kernelWanted && !needAWGReader {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	collector := &kernelCaptureCollector{d: a.debug, c: c, env: a.kernelDebug, ctx: ctx, cancel: cancel,
		kernelWanted: kernelWanted, awgWanted: awgWanted, interfaces: map[string]*kernelInterfaceStats{}, warnings: map[string]kernelCaptureWarning{}}
	for _, in := range activeInterfaces {
		collector.interfaces[in.Name] = &kernelInterfaceStats{inbound: in.InboundID, counts: make(map[string]uint64, len(awgKernelEvents))}
	}
	finalRecords := 2 // The journal and AWG control each have one warning record.
	if awgWanted {
		finalRecords += len(collector.interfaces)
	}
	if kernelWanted {
		finalRecords++
	}
	c.reservedRecords = finalRecords
	c.reservedBytes = min(debugMaxBytes, finalRecords*2048)
	c.onEnd = collector.finish

	if needAWGReader {
		if _, stop := e.WatchKernelUnknownPeers(collector.unknownPeer); stop != nil {
			collector.stopUnknown = stop
		}
		if c.level <= slog.LevelDebug {
			collector.lease = a.kernelDebug.enable()
			collector.debugOn = collector.lease.debugOn
			for _, err := range collector.lease.begin {
				collector.warn("awg_kernel_debug_unavailable", "awg", err)
			}
		}
	}

	process, err := a.kernelDebug.startJournal(ctx, c.started.Add(-30*time.Minute))
	if err != nil {
		collector.warn("kernel_log_unavailable", collector.unavailableSource(), err)
		return
	}
	collector.process = process
	collector.readerDone = make(chan struct{})
	go collector.readJournal()
}

func (k *kernelCaptureCollector) unavailableSource() string {
	if k.kernelWanted {
		return kernelCaptureSource
	}
	return "awg"
}

func (k *kernelCaptureCollector) warn(code, source string, err error) {
	if err == nil {
		return
	}
	k.mu.Lock()
	if _, exists := k.warnings[code]; !exists {
		k.warnings[code] = kernelCaptureWarning{source: source, reason: err.Error()}
	}
	k.mu.Unlock()
}

func (k *kernelCaptureCollector) readJournal() {
	defer close(k.readerDone)
	scanner := bufio.NewScanner(k.process)
	scanner.Buffer(make([]byte, 4096), kernelLineMaxBytes)
	for scanner.Scan() {
		line, err := parseKernelJournalLine(scanner.Bytes())
		if err != nil {
			k.warn("kernel_log_unavailable", k.unavailableSource(), fmt.Errorf("parse journalctl output: %w", err))
			continue
		}
		k.recordJournalLine(line)
	}
	scanErr := scanner.Err()
	waitErr := k.process.Wait()
	if k.ctx.Err() == nil {
		if scanErr != nil {
			k.warn("kernel_log_unavailable", k.unavailableSource(), scanErr)
		} else if waitErr != nil {
			k.warn("kernel_log_unavailable", k.unavailableSource(), waitErr)
		} else {
			k.warn("kernel_log_unavailable", k.unavailableSource(), errors.New("journalctl exited while the capture was running"))
		}
	}
}

func (k *kernelCaptureCollector) recordJournalLine(line parsedKernelLine) {
	k.mu.Lock()
	closed := k.closed
	k.mu.Unlock()
	if closed {
		return
	}
	if match := kernelSuppressed.FindStringSubmatch(line.message); len(match) == 2 {
		if n, err := strconv.ParseUint(match[1], 10, 64); err == nil {
			k.mu.Lock()
			k.suppressed += n
			k.mu.Unlock()
		}
	}
	if firewallPacketLog(line.message) {
		k.mu.Lock()
		k.firewall++
		k.mu.Unlock()
		return
	}
	level := kernelLevel(line.priority)
	if level < k.c.level {
		return
	}
	if ifname, inbound, body, ok := kernelInterfaceMessage(line.message, k.interfaceMap()); ok {
		if !k.awgWanted {
			return
		}
		event, peer := classifyAWGKernelLine(body)
		k.mu.Lock()
		stats := k.interfaces[ifname]
		if stats != nil {
			stats.counts[event]++
			if event == "initiation_in" || event == "response_out" {
				if len(stats.handshakes) < kernelHandshakesMax {
					stats.handshakes = append(stats.handshakes, kernelHandshakeEvent{at: line.at, peer: peer, event: event})
				} else {
					stats.cut = true
				}
			}
			inbound = stats.inbound
		}
		k.mu.Unlock()
		attrs := map[string]string{"event": event}
		if inbound != "" {
			attrs["inbound"] = inbound
		}
		if peer != "" {
			attrs["peer"] = peer
		}
		k.d.add(level, logRecord{at: line.at, level: captureSeverity(level), source: "awg", msg: body, attrs: attrs})
		return
	}
	if !k.kernelWanted {
		return
	}
	result := k.d.add(level, logRecord{at: line.at, level: captureSeverity(level), source: kernelCaptureSource, msg: line.message})
	k.mu.Lock()
	switch result {
	case debugAddStored:
		k.kept++
	case debugAddCapped:
		k.dropped++
	}
	k.mu.Unlock()
}

func (k *kernelCaptureCollector) interfaceMap() map[string]string {
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make(map[string]string, len(k.interfaces))
	for name, stats := range k.interfaces {
		out[name] = stats.inbound
	}
	return out
}

func (k *kernelCaptureCollector) unknownPeer(ifname string) {
	if !k.awgWanted {
		return
	}
	k.mu.Lock()
	if k.closed {
		k.mu.Unlock()
		return
	}
	stats := k.interfaces[ifname]
	if stats == nil {
		k.mu.Unlock()
		return
	}
	stats.counts["unknown_peer"]++
	inbound := stats.inbound
	k.mu.Unlock()
	k.d.add(slog.LevelInfo, logRecord{at: time.Now(), level: pb.Severity_SEVERITY_INFO, source: "awg", msg: "unknown peer handshake",
		attrs: map[string]string{"event": "unknown_peer", "inbound": inbound}})
}

func (k *kernelCaptureCollector) finish(c *debugCapture, _ string) {
	k.mu.Lock()
	k.closed = true
	k.mu.Unlock()
	k.cancel()
	if k.stopUnknown != nil {
		k.stopUnknown()
	}
	if k.process != nil {
		_ = k.process.Close()
		<-k.readerDone
	}
	if errs := k.lease.close(); len(errs) != 0 {
		k.warn("awg_kernel_debug_unavailable", "awg", errors.Join(errs...))
	}

	k.mu.Lock()
	warnings := make([]logRecord, 0, len(k.warnings))
	warningCodes := make([]string, 0, len(k.warnings))
	for code := range k.warnings {
		warningCodes = append(warningCodes, code)
	}
	sort.Strings(warningCodes)
	for _, code := range warningCodes {
		warning := k.warnings[code]
		warnings = append(warnings, logRecord{at: time.Now(), level: pb.Severity_SEVERITY_WARNING, source: warning.source,
			msg: warning.reason, attrs: map[string]string{"code": code}})
	}
	if k.awgWanted {
		for _, name := range sortedKernelInterfaces(k.interfaces) {
			stats := k.interfaces[name]
			counts := make(map[string]uint64, len(awgKernelEvents))
			for _, event := range awgKernelEvents {
				counts[event] = stats.counts[event]
			}
			data, _ := json.Marshal(counts)
			message := fmt.Sprintf("awg kernel summary: event_counts=%s initiations_unanswered=%d suppressed=%d debug_on=%t",
				data, unansweredKernelInitiations(stats.handshakes), k.suppressed, k.debugOn)
			if stats.cut {
				message += fmt.Sprintf(" handshakes_cut=%d (the unanswered count covers the first %d handshake lines only)", kernelHandshakesMax, kernelHandshakesMax)
			}
			warnings = append(warnings, logRecord{at: time.Now(), level: pb.Severity_SEVERITY_INFO, source: "awg", msg: message,
				attrs: map[string]string{"event": "summary", "inbound": stats.inbound}})
		}
	}
	if k.kernelWanted {
		message := fmt.Sprintf("kernel log summary: kept=%d firewall_lines=%d dropped_by_caps=%d", k.kept, k.firewall, k.dropped)
		warnings = append(warnings, logRecord{at: time.Now(), level: pb.Severity_SEVERITY_INFO, source: kernelCaptureSource, msg: message})
	}
	k.mu.Unlock()
	k.d.addFinal(c, warnings...)
}

func sortedKernelInterfaces(in map[string]*kernelInterfaceStats) []string {
	out := make([]string, 0, len(in))
	for name := range in {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
