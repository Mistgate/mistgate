// Package telegram sends the panel's alerts to the admins who linked a Telegram chat, and serves the admin TelegramService
// that sets the bot up. One bot (the owner's), long polling only to hear "/start <code>" (the panel sits behind a decoy
// site and takes no inbound webhook), sendMessage for everything else.
//
// What goes out is decided by the callers (cmd/mistgate wires them): health alert transitions, a new panel release, agents
// behind a release, a failed backup, an MCP plan waiting for the owner, an AWG kernel build that failed, and sign-in
// security events. This package owns the how: who receives it by role, never the same open alert twice, bursts coalesced
// into one message, retries with backoff and Telegram's retry_after, and a clean stop.
package telegram

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	"github.com/mistgate/mistgate/gen/mistgate/admin/v1/adminv1connect"
	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	tokenRecordID = "telegram_bot.token"
	offsetKey     = "telegram.offset"
	notifiedRel   = "telegram.notified.release"
	notifiedAgent = "telegram.notified.agents"

	linkCodeTTL  = 10 * time.Minute
	maxPending   = 200
	minSeverity  = 2 // warning and up; info-level alerts are never sent
	maxMessage   = 3500
	maxLinkCodes = 64
	ipSeenFor    = 30 * 24 * time.Hour
)

// Sources are what the periodic watcher asks other modules (wired after they exist; nil = not watched).
type Sources struct {
	// PanelRelease: the newest signed panel release that is newer than this build, if any.
	PanelRelease func() (version string, available bool)
	// OutdatedAgents: the version of the release the nodes are compared with and the names of the connected nodes behind it.
	OutdatedAgents func(ctx context.Context) (version string, nodes []string)
}

// Config configures Service. Store and Vault are required.
type Config struct {
	Store *store.Store
	Vault *vault.Vault
	// StepUp is auth.Service.RequireStepUp: setting the bot and linking a chat ask for it. Required.
	StepUp func(context.Context) error
	// AdminURL is the public address of the admin ("" = unknown: messages carry no link).
	AdminURL string
	Log      *slog.Logger

	// Test seams. The zero value is production.
	APIBase     string       // https://api.telegram.org
	HTTPClient  *http.Client // default: a client with no timeout of its own (every call has its context's)
	Now         func() time.Time
	Window      time.Duration // how long a burst is gathered into one message; default 30 s
	PollTimeout int           // getUpdates long-poll seconds; default 30
	RetryBase   time.Duration // first wait after a failed send, doubled each time; default 2 s
	RetryMax    int           // sends tried before a message is dropped; default 6
	WatchEvery  time.Duration // release / agent check period; default 5 min
	WatchFirst  time.Duration // first check after Run starts; default 1 min
}

// Service is the Telegram module.
type Service struct {
	st    *store.Store
	vault *vault.Vault
	api   *apiClient
	cfg   Config
	log   *slog.Logger
	now   func() time.Time

	mu       sync.Mutex
	pending  []item
	open     map[string]bool      // health alerts announced as open and not yet resolved
	codes    map[string]linkCode  // sha256 of a link code -> whom it is for
	seenIP   map[string]time.Time // admin id + address -> last sign-in seen from it
	lastTest map[string]time.Time // admin id -> last test message
	botErr   string               // how the last conversation with Telegram went ("" = fine)
	sources  Sources
	pollStop context.CancelFunc // ends the poll in flight when the bot changes
	// pollFrom0: the next poll starts from the beginning (another bot: update ids are per bot).
	pollFrom0 bool
	notify    chan struct{} // an item was queued
	wake      chan struct{} // the bot changed
}

type linkCode struct {
	adminID string
	expires time.Time
}

// item is a line of news waiting for the next message: who gets it and how it reads in a language.
type item struct {
	to   func(store.TelegramLinkRow) bool
	text func(l L, brand string) string
}

// Audiences. What a role receives: health alerts reach every admin (every role may read them in the admin); everything
// else is the owner's business.
func everyone(store.TelegramLinkRow) bool    { return true }
func ownerOnly(l store.TelegramLinkRow) bool { return l.Role == store.RoleOwner }
func ownerAnd(id string) func(store.TelegramLinkRow) bool {
	return func(l store.TelegramLinkRow) bool { return l.Role == store.RoleOwner || (id != "" && l.AdminID == id) }
}

// New builds the module. Call Run to start polling and sending.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil || cfg.Vault == nil || cfg.StepUp == nil {
		return nil, errors.New("telegram: store, vault and step-up are required")
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.APIBase == "" {
		cfg.APIBase = defaultAPIBase
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{}
	}
	for p, d := range map[*time.Duration]time.Duration{&cfg.Window: 30 * time.Second, &cfg.RetryBase: 2 * time.Second, &cfg.WatchEvery: 5 * time.Minute, &cfg.WatchFirst: time.Minute} {
		if *p <= 0 {
			*p = d
		}
	}
	if cfg.PollTimeout <= 0 {
		cfg.PollTimeout = 30
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 6
	}
	return &Service{
		st: cfg.Store, vault: cfg.Vault, api: &apiClient{base: strings.TrimRight(cfg.APIBase, "/"), hc: cfg.HTTPClient}, cfg: cfg, log: cfg.Log, now: cfg.Now,
		open: map[string]bool{}, codes: map[string]linkCode{}, seenIP: map[string]time.Time{}, lastTest: map[string]time.Time{},
		notify: make(chan struct{}, 1), wake: make(chan struct{}, 1),
	}, nil
}

// Handler is the admin TelegramService (mounted behind the session like the others).
func (s *Service) Handler(opts ...connect.HandlerOption) (string, http.Handler) {
	return adminv1connect.NewTelegramServiceHandler(rpc{s}, append([]connect.HandlerOption{connect.WithReadMaxBytes(16 << 10)}, opts...)...)
}

// SetSources connects the watcher to the update module (they are built after this one).
func (s *Service) SetSources(src Sources) {
	s.mu.Lock()
	s.sources = src
	s.mu.Unlock()
}

// Run polls, sends and watches until ctx ends, then flushes what is queued (briefly) and returns. No goroutine outlives it.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, f := range []func(context.Context){s.pollLoop, s.sendLoop, s.watchLoop} {
		wg.Add(1)
		go func() { defer wg.Done(); f(ctx) }()
	}
	wg.Wait()
}

// token is the bot token, opened from the vault for one use. ok is false when no bot is set.
func (s *Service) token(ctx context.Context) (vault.Redacted, bool) {
	b, err := s.st.TelegramBot(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.log.Warn("telegram: read bot", "err", err)
		}
		return "", false
	}
	pt, err := s.vault.Open(b.Token, tokenRecordID)
	if err != nil {
		s.log.Error("telegram: the stored bot token cannot be opened", "err", err)
		return "", false
	}
	return vault.Redacted(pt), true
}

func (s *Service) setBotErr(code string) {
	s.mu.Lock()
	s.botErr = code
	s.mu.Unlock()
}

func (s *Service) botError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.botErr
}

// botChanged ends the poll in flight and makes the loops look at the new bot at once. newBot: it is another bot than before
// (or none), so the position in the old bot's updates means nothing to it.
func (s *Service) botChanged(newBot bool) {
	s.mu.Lock()
	s.botErr = ""
	s.pollFrom0 = s.pollFrom0 || newBot
	stop := s.pollStop
	s.mu.Unlock()
	if stop != nil {
		stop()
	}
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// sleep waits d, or until ctx ends (false) or the bot changed.
func (s *Service) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-s.wake:
	case <-t.C:
	}
	return ctx.Err() == nil
}

// ---- polling: only to hear "/start <code>"

func (s *Service) pollLoop(ctx context.Context) {
	backoff := time.Second
	var offset int64
	if v, err := s.st.Setting(ctx, offsetKey); err == nil {
		offset, _ = strconv.ParseInt(v, 10, 64)
	}
	for ctx.Err() == nil {
		tok, ok := s.token(ctx)
		if !ok {
			s.sleep(ctx, 15*time.Second)
			continue
		}
		pctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.PollTimeout+15)*time.Second)
		s.mu.Lock()
		if s.pollFrom0 {
			offset, s.pollFrom0 = 0, false
		}
		s.pollStop = cancel
		s.mu.Unlock()
		ups, err := s.api.getUpdates(pctx, tok, offset, s.cfg.PollTimeout)
		perr := pctx.Err() // before cancel: set only when the poll was cut short
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if perr != nil && !errors.Is(perr, context.DeadlineExceeded) {
				continue // the bot changed under the poll: start over with the new one
			}
			wait := backoff
			var ae *apiError
			if errors.As(err, &ae) && ae.RetryAfter > 0 {
				wait = min(ae.RetryAfter, 5*time.Minute)
			} else {
				backoff = min(backoff*2, 5*time.Minute)
			}
			s.setBotErr(errCode(err))
			s.log.Warn("telegram: getUpdates", "code", errCode(err), "retry_in", wait.String())
			s.sleep(ctx, wait)
			continue
		}
		backoff = time.Second
		s.setBotErr("")
		for _, u := range ups {
			offset = max(offset, u.UpdateID+1)
			if u.Message != nil {
				s.handleMessage(ctx, tok, *u.Message)
			}
		}
		if len(ups) > 0 {
			if err := s.st.SetSettings(ctx, map[string]string{offsetKey: strconv.FormatInt(offset, 10)}); err != nil && ctx.Err() == nil {
				s.log.Warn("telegram: save offset", "err", err)
			}
		}
	}
}

// resetOffset forgets the position in the updates of the old bot.
func (s *Service) resetOffset(ctx context.Context) {
	_ = s.st.SetSettings(ctx, map[string]string{offsetKey: "0"})
}

// handleMessage answers a private chat that sends a valid link code (and a linked chat that says /start); every other chat
// gets no reply at all, so a stranger who finds the bot learns nothing from it.
func (s *Service) handleMessage(ctx context.Context, tok vault.Redacted, m tgMessage) {
	if m.Chat.Type != "private" {
		return
	}
	fields := strings.Fields(m.Text)
	if len(fields) == 0 {
		return
	}
	cmd, _, _ := strings.Cut(fields[0], "@")
	if cmd != "/start" {
		return
	}
	if len(fields) >= 2 {
		if adminID, ok := s.takeCode(fields[1]); ok {
			l, brand := s.panel(ctx)
			if err := s.st.BindTelegramChat(ctx, adminID, m.Chat.ID, s.now()); err != nil {
				if !errors.Is(err, store.ErrNotFound) {
					s.log.Warn("telegram: bind chat", "err", err)
				}
				return
			}
			s.auditAs(ctx, adminID, "telegram_link", nil, store.AuditBot)
			name := adminID
			if a, err := s.st.Admin(ctx, adminID); err == nil && a.DisplayName != "" {
				name = a.DisplayName
			}
			s.reply(ctx, tok, m.Chat.ID, linkedText(l, name, brand))
			return
		}
	}
	// no valid code: only an already linked chat is answered
	if link, err := s.st.TelegramLinkByChat(ctx, m.Chat.ID); err == nil {
		l, brand := s.panel(ctx)
		s.reply(ctx, tok, m.Chat.ID, alreadyLinkedText(l, link.AdminName, brand))
	}
}

func (s *Service) reply(ctx context.Context, tok vault.Redacted, chat int64, text string) {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := s.api.sendMessage(sctx, tok, chat, text); err != nil && ctx.Err() == nil {
		s.log.Warn("telegram: reply", "code", errCode(err))
	}
}

// ---- link codes

func codeHash(code string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(code)))
	return hex.EncodeToString(h[:])
}

// newCode makes the one-time code of an admin (12 characters of base32: 60 bits) that lives linkCodeTTL. A new code
// replaces the admin's earlier one.
func (s *Service) newCode(adminID string) (code string, expires time.Time) {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	code = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))[:12]
	expires = s.now().Add(linkCodeTTL)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for h, c := range s.codes {
		if c.adminID == adminID || !now.Before(c.expires) {
			delete(s.codes, h)
		}
	}
	for len(s.codes) >= maxLinkCodes { // never more than a handful of admins link at once
		for h := range s.codes {
			delete(s.codes, h)
			break
		}
	}
	s.codes[codeHash(code)] = linkCode{adminID: adminID, expires: expires}
	return code, expires
}

// takeCode consumes a code: it works once, and only before it expires.
func (s *Service) takeCode(code string) (adminID string, ok bool) {
	h := codeHash(code)
	s.mu.Lock()
	defer s.mu.Unlock()
	c, found := s.codes[h]
	if !found {
		return "", false
	}
	delete(s.codes, h)
	if !s.now().Before(c.expires) {
		return "", false
	}
	return c.adminID, true
}

// ---- what is announced

// panel is the panel language and the brand name.
func (s *Service) panel(ctx context.Context) (L, string) {
	in, err := instance.Load(ctx, s.st)
	if err != nil {
		return "en", "Mistgate"
	}
	return L(in.Language), in.BrandName()
}

func (s *Service) nodeName(ctx context.Context, id string) string {
	if id == "" {
		return ""
	}
	if n, err := s.st.Node(ctx, id); err == nil {
		return n.Name
	}
	return ""
}

func (s *Service) enqueue(it item) {
	s.mu.Lock()
	if len(s.pending) >= maxPending {
		s.pending = s.pending[1:]
	}
	s.pending = append(s.pending, it)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// AlertTransition is health.Config.OnTransition: an alert opened (or re-opened) or ended. An alert already announced as open
// is not announced again; one that ended because a worse one replaced it, was accepted as normal or lost its node says
// nothing; one the admin muted ends quietly.
func (s *Service) AlertTransition(a store.HealthAlert, resolved bool) {
	if a.Severity < minSeverity {
		return
	}
	key := a.Kind + "/" + a.NodeID + "/" + a.Subject
	s.mu.Lock()
	if resolved {
		delete(s.open, key)
	} else if s.open[key] {
		s.mu.Unlock()
		return
	} else {
		s.open[key] = true
	}
	s.mu.Unlock()
	if resolved {
		switch a.Resolution {
		case "superseded", "accepted", "node_retired":
			return
		}
		if a.MutedUntil.After(s.now()) {
			return
		}
	}
	node := s.nodeName(context.Background(), a.NodeID)
	adminURL := s.cfg.AdminURL
	s.enqueue(item{to: everyone, text: func(l L, _ string) string {
		if resolved {
			return alertResolved(l, a, node)
		}
		return alertOpened(l, a, node, adminURL)
	}})
}

// BackupFailed announces a failed backup once; the next one that works resolves it. The scheduler retries with a growing
// wait, and every retry that fails again would otherwise be a message.
func (s *Service) BackupFailed(code string) {
	s.mu.Lock()
	again := s.open["backup"]
	s.open["backup"] = true
	s.mu.Unlock()
	if again {
		return
	}
	adminURL := s.cfg.AdminURL
	s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return backupFailedText(l, code, adminURL) }})
}

// BackupOK resolves a failure that was announced.
func (s *Service) BackupOK() {
	s.mu.Lock()
	was := s.open["backup"]
	delete(s.open, "backup")
	s.mu.Unlock()
	if was {
		s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return backupRecoveredText(l) }})
	}
}

// PlanWaiting announces an MCP plan that waits for the owner's approval: the owner only, with a link to the screen where the
// decision is made (never an approval from the chat).
func (s *Service) PlanWaiting(tool, tokenName string) {
	adminURL := s.cfg.AdminURL
	s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return planWaitingText(l, tool, tokenName, adminURL) }})
}

// AwgPrepareFailed announces a failed build of the AmneziaWG kernel module on a node (the same failure is told once).
func (s *Service) AwgPrepareFailed(nodeID, code string, at int64) {
	key := "awgprep/" + nodeID + "/" + strconv.FormatInt(at, 10)
	s.mu.Lock()
	again := s.open[key]
	s.open[key] = true
	s.mu.Unlock()
	if again {
		return
	}
	node := s.nodeName(context.Background(), nodeID)
	adminURL := s.cfg.AdminURL
	s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return awgPrepareFailedText(l, node, code, adminURL) }})
}

// ---- release and agent watcher

func (s *Service) watchLoop(ctx context.Context) {
	if !s.sleepPlain(ctx, s.cfg.WatchFirst) {
		return
	}
	for {
		s.watch(ctx)
		if !s.sleepPlain(ctx, s.cfg.WatchEvery) {
			return
		}
	}
}

func (s *Service) sleepPlain(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// canSend is whether a message for this audience would reach anybody (a bot and an enabled chat). The watcher marks a
// version as told only when it could be.
func (s *Service) canSend(ctx context.Context, to func(store.TelegramLinkRow) bool) bool {
	if _, err := s.st.TelegramBot(ctx); err != nil {
		return false
	}
	links, err := s.st.TelegramLinks(ctx)
	if err != nil {
		return false
	}
	for _, l := range links {
		if l.Enabled && to(l) {
			return true
		}
	}
	return false
}

// watch announces a signed panel release and agents behind a release, each once per version.
func (s *Service) watch(ctx context.Context) {
	s.mu.Lock()
	src := s.sources
	s.mu.Unlock()
	adminURL := s.cfg.AdminURL
	told := func(key, version string) bool {
		v, err := s.st.Setting(ctx, key)
		return err == nil && v == version
	}
	mark := func(key, version string) {
		if err := s.st.SetSettings(ctx, map[string]string{key: version}); err != nil && ctx.Err() == nil {
			s.log.Warn("telegram: remember announced version", "err", err)
		}
	}
	if src.PanelRelease != nil {
		if v, ok := src.PanelRelease(); ok && v != "" && !told(notifiedRel, v) && s.canSend(ctx, ownerOnly) {
			s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return releaseText(l, v, adminURL) }})
			mark(notifiedRel, v)
		}
	}
	if src.OutdatedAgents != nil {
		if v, nodes := src.OutdatedAgents(ctx); v != "" && len(nodes) > 0 && !told(notifiedAgent, v) && s.canSend(ctx, ownerOnly) {
			s.enqueue(item{to: ownerOnly, text: func(l L, _ string) string { return agentsOutdatedText(l, v, nodes, adminURL) }})
			mark(notifiedAgent, v)
		}
	}
}

// ---- sending

// sendLoop gathers a burst (the first queued item opens a Window) and sends it as one message per chat.
func (s *Service) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			s.finalFlush(ctx)
			return
		case <-s.notify:
		}
		if !s.sleepPlain(ctx, s.cfg.Window) {
			s.finalFlush(ctx)
			return
		}
		s.flush(ctx)
	}
}

// finalFlush sends what is queued when the panel stops (an update restarts it), but never holds the stop for long.
func (s *Service) finalFlush(ctx context.Context) {
	fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.flush(fctx)
}

func (s *Service) flush(ctx context.Context) {
	s.mu.Lock()
	items := s.pending
	s.pending = nil
	s.mu.Unlock()
	if len(items) == 0 {
		return
	}
	tok, ok := s.token(ctx)
	if !ok {
		return
	}
	links, err := s.st.TelegramLinks(ctx)
	if err != nil {
		s.log.Warn("telegram: read links", "err", err)
		return
	}
	l, brand := s.panel(ctx)
	for _, link := range links {
		if !link.Enabled {
			continue
		}
		var lines []string
		for _, it := range items {
			if !it.to(link) {
				continue
			}
			t := it.text(l, brand)
			if t == "" || containsString(lines, t) {
				continue
			}
			lines = append(lines, t)
		}
		for _, msg := range chunk(lines) {
			if ctx.Err() != nil {
				return
			}
			s.deliver(ctx, tok, link.ChatID, msg)
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// chunk joins lines into messages that fit Telegram's limit (a line is never split; one too long is cut).
func chunk(lines []string) []string {
	var out []string
	var cur string
	for _, ln := range lines {
		if len([]rune(ln)) > maxMessage {
			ln = string([]rune(ln)[:maxMessage])
		}
		if cur != "" && len([]rune(cur))+2+len([]rune(ln)) > maxMessage {
			out = append(out, cur)
			cur = ""
		}
		if cur != "" {
			cur += "\n\n"
		}
		cur += ln
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// deliver sends one message, trying again after a network failure or a server error with a doubling wait, and after 429 for
// as long as Telegram asks. A chat that cannot be written to (the user blocked the bot) or a refused token ends it at once.
func (s *Service) deliver(ctx context.Context, tok vault.Redacted, chat int64, text string) {
	wait := s.cfg.RetryBase
	for attempt := 1; attempt <= s.cfg.RetryMax; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.api.sendMessage(sctx, tok, chat, text)
		cancel()
		if err == nil {
			s.setBotErr("")
			return
		}
		if ctx.Err() != nil {
			return
		}
		var ae *apiError
		errors.As(err, &ae)
		switch {
		case ae != nil && ae.Status == http.StatusTooManyRequests:
			d := min(max(ae.RetryAfter, time.Second), 2*time.Minute)
			s.log.Warn("telegram: rate limited", "retry_in", d.String())
			if !s.sleepPlain(ctx, d) {
				return
			}
		case ae != nil && ae.Status == http.StatusUnauthorized:
			s.setBotErr("unauthorized")
			s.log.Warn("telegram: the bot token is refused")
			return
		case ae != nil && !ae.Transport && ae.Status >= 400 && ae.Status < 500:
			s.log.Warn("telegram: message refused", "status", ae.Status, "desc", clip(ae.Desc, 80)) // a blocked bot, a deleted chat
			return
		default:
			s.setBotErr("unreachable")
			s.log.Warn("telegram: send failed, will retry", "code", errCode(err), "attempt", attempt)
			if !s.sleepPlain(ctx, wait) {
				return
			}
			wait *= 2
		}
	}
	s.log.Warn("telegram: message dropped after retries")
}

// ---- audit

func (s *Service) auditAs(ctx context.Context, actor, action string, params map[string]any, source string) {
	if params == nil {
		params = map[string]any{}
	}
	b, err := json.Marshal(params)
	if err != nil {
		return
	}
	if err := s.st.Audit(ctx, s.now(), store.AuditEntry{Actor: actor, Action: action, Params: string(b), Result: "ok", Source: source}); err != nil {
		s.log.Warn("telegram: audit", "action", action, "err", err)
	}
}
