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

	linkCodeTTL = 10 * time.Minute
	maxPending  = 200
	minSeverity = 2 // warning and up; info-level alerts are never sent
	maxMessage  = 3500
	ipSeenFor   = 30 * 24 * time.Hour
	edgePollMax = 25 // seconds; leave time in the cron job for processing and offset persistence

	nodeNameTimeout = 5 * time.Second // the node-name read of an alert message (D1 on the edge)
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
	// Edge is the Cloudflare edition: work is triggered by EdgeTick and bounded after-response tasks, not process loops.
	Edge bool
	// AfterResponse keeps short request producers alive until a post-response flush has finished. The runner is bounded by
	// its caller; nil leaves delivery to the VPS background loop.
	AfterResponse func(func())

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

	// Wait replaces the timers in tests: it waits d, or until ctx ends (false) or wake fires (wake is nil where a bot
	// change must not cut the wait short).
	Wait func(ctx context.Context, d time.Duration, wake <-chan struct{}) bool
}

// Service is the Telegram module.
type Service struct {
	st    *store.Store
	vault *vault.Vault
	api   *apiClient
	cfg   Config
	log   *slog.Logger
	now   func() time.Time

	mu         sync.Mutex
	pending    []item
	announcing map[string]struct{}  // watcher notices queued or being delivered
	open       map[string]bool      // health alerts announced as open and not yet resolved
	seenIP     map[string]time.Time // admin id + address -> last sign-in seen from it
	lastTest   map[string]time.Time // admin id -> last test message
	botErr     string               // how the last conversation with Telegram went ("" = fine)
	sources    Sources
	pollStop   context.CancelFunc // ends the poll in flight when the bot changes
	// pollFrom0: the next poll starts from the beginning (another bot: update ids are per bot).
	pollFrom0 bool
	notify    chan struct{} // an item was queued
	wake      chan struct{} // the bot changed
}

// item is a line of news waiting for the next message: who gets it and how it reads in a language.
type item struct {
	to            func(store.TelegramLinkRow) bool
	text          func(l L, brand string) string
	noticeKey     string
	noticeVersion string
}

func (it item) noticeID() string {
	if it.noticeKey == "" {
		return ""
	}
	return it.noticeKey + "\x00" + it.noticeVersion
}

// Audiences. What a role receives: health alerts reach every admin (every role may read them in the admin); everything
// else is the owner's business.
func everyone(store.TelegramLinkRow) bool    { return true }
func ownerOnly(l store.TelegramLinkRow) bool { return l.Role == store.RoleOwner }
func ownerAnd(id string) func(store.TelegramLinkRow) bool {
	return func(l store.TelegramLinkRow) bool { return l.Role == store.RoleOwner || (id != "" && l.AdminID == id) }
}

// New builds the module. VPS starts Run; the edge invokes the bounded polling, watcher, and flush methods from EdgeTick.
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
		if cfg.Edge {
			cfg.PollTimeout = edgePollMax
		}
	}
	if cfg.Edge && cfg.PollTimeout > edgePollMax {
		cfg.PollTimeout = edgePollMax
	}
	if cfg.RetryMax <= 0 {
		cfg.RetryMax = 6
	}
	return &Service{
		st: cfg.Store, vault: cfg.Vault, api: &apiClient{base: strings.TrimRight(cfg.APIBase, "/"), hc: cfg.HTTPClient}, cfg: cfg, log: cfg.Log, now: cfg.Now,
		announcing: map[string]struct{}{}, open: map[string]bool{}, seenIP: map[string]time.Time{}, lastTest: map[string]time.Time{},
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

// Run starts the VPS polling, sending, and watcher loops until ctx ends, then briefly flushes queued messages. No goroutine
// outlives it; the edge runs bounded methods from EdgeTick instead.
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
	token, _, ok := s.tokenAndBotID(ctx)
	return token, ok
}

func (s *Service) tokenAndBotID(ctx context.Context) (vault.Redacted, int64, bool) {
	b, err := s.st.TelegramBot(ctx)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.log.Warn("telegram: read bot", "err", err)
		}
		return "", 0, false
	}
	pt, err := s.vault.Open(b.Token, tokenRecordID)
	if err != nil {
		s.log.Error("telegram: the stored bot token cannot be opened", "err", err)
		return "", 0, false
	}
	return vault.Redacted(pt), b.BotID, true
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
	return s.wait(ctx, d, s.wake)
}

func (s *Service) wait(ctx context.Context, d time.Duration, wake <-chan struct{}) bool {
	if s.cfg.Wait != nil {
		return s.cfg.Wait(ctx, d, wake)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-wake:
		return ctx.Err() == nil
	case <-t.C:
		return true
	}
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
				wait = retryAfterWait(ae.RetryAfter)
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

// PollPendingLinkCode is one edge tick's bounded Telegram poll. It checks D1 before taking the cross-isolate lease, so the
// edge does not long-poll when there is no link code to hear.
func (s *Service) PollPendingLinkCode(ctx context.Context) error {
	pending, err := s.st.HasPendingTelegramLinkCode(ctx, s.now())
	if err != nil {
		return err
	}
	if !pending {
		return nil
	}
	holder := store.NewID("lease_")
	taken, err := s.st.TakeLease(ctx, "telegram-poll", holder, 45*time.Second, s.now())
	if err != nil || !taken {
		return err
	}
	defer func() {
		if err := s.st.ReleaseLease(ctx, "telegram-poll", holder); err != nil && ctx.Err() == nil {
			s.log.Warn("telegram: release poll lease", "err", err)
		}
	}()

	tok, botID, ok := s.tokenAndBotID(ctx)
	if !ok {
		return nil
	}
	offset := int64(0)
	if value, err := s.st.Setting(ctx, offsetKey); err == nil {
		offset, _ = strconv.ParseInt(value, 10, 64)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	s.mu.Lock()
	if s.pollFrom0 {
		offset, s.pollFrom0 = 0, false
	}
	pctx, cancel := context.WithTimeout(ctx, time.Duration(s.cfg.PollTimeout+15)*time.Second)
	s.pollStop = cancel
	s.mu.Unlock()
	updates, err := s.api.getUpdates(pctx, tok, offset, s.cfg.PollTimeout)
	pollErr := pctx.Err()
	cancel()
	s.mu.Lock()
	s.pollStop = nil
	s.mu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if pollErr != nil && !errors.Is(pollErr, context.DeadlineExceeded) {
			return nil // the bot changed during the poll
		}
		s.setBotErr(errCode(err))
		s.log.Warn("telegram: getUpdates", "code", errCode(err))
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	// Another isolate may have replaced the bot while this poll was in flight. Its updates and offset belong to the old bot.
	current, err := s.st.TelegramBot(ctx)
	if errors.Is(err, store.ErrNotFound) || (err == nil && current.BotID != botID) {
		return nil
	}
	if err != nil {
		return err
	}
	s.setBotErr("")
	for _, update := range updates {
		offset = max(offset, update.UpdateID+1)
		if update.Message != nil {
			s.handleMessage(ctx, tok, *update.Message)
		}
	}
	if len(updates) > 0 {
		return s.st.SetSettings(ctx, map[string]string{offsetKey: strconv.FormatInt(offset, 10)})
	}
	return nil
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
		if adminID, ok := s.takeCode(ctx, fields[1]); ok {
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
func (s *Service) newCode(ctx context.Context, adminID string) (code string, expires time.Time, err error) {
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", time.Time{}, err
	}
	code = strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw[:]))[:12]
	expires = s.now().Add(linkCodeTTL)
	if err := s.st.SetTelegramLinkCode(ctx, codeHash(code), adminID, expires); err != nil {
		return "", time.Time{}, err
	}
	return code, expires, nil
}

// takeCode consumes a code: it works once, and only before it expires.
func (s *Service) takeCode(ctx context.Context, code string) (adminID string, ok bool) {
	adminID, err := s.st.TakeTelegramLinkCode(ctx, codeHash(code), s.now())
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && ctx.Err() == nil {
			s.log.Warn("telegram: consume link code", "err", err)
		}
		return "", false
	}
	return adminID, true
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
	ctx, cancel := context.WithTimeout(ctx, nodeNameTimeout) // the callers pass Background: a hung read must not hold them for ever
	defer cancel()
	if n, err := s.st.Node(ctx, id); err == nil {
		return n.Name
	}
	return ""
}

func (s *Service) enqueue(it item) {
	s.mu.Lock()
	if len(s.pending) >= maxPending {
		if id := s.pending[0].noticeID(); id != "" {
			delete(s.announcing, id)
		}
		s.pending = s.pending[1:]
	}
	s.pending = append(s.pending, it)
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
	s.flushAfterResponse() // the edge has no send loop; a no-op on the VPS
}

func (s *Service) enqueueNotice(key, version string, it item) {
	it.noticeKey, it.noticeVersion = key, version
	id := it.noticeID()
	s.mu.Lock()
	if _, exists := s.announcing[id]; exists {
		s.mu.Unlock()
		return
	}
	s.announcing[id] = struct{}{}
	s.mu.Unlock()
	s.enqueue(it)
}

func (s *Service) noticeDelivered(ctx context.Context, it item) {
	if id := it.noticeID(); id != "" {
		if err := s.st.SetSettings(context.WithoutCancel(ctx), map[string]string{it.noticeKey: it.noticeVersion}); err != nil {
			s.log.Warn("telegram: remember announced version", "err", err)
		}
		s.mu.Lock()
		delete(s.announcing, id)
		s.mu.Unlock()
	}
}

func (s *Service) finishNotices(items []item, retained map[string]struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range items {
		id := it.noticeID()
		if id == "" {
			continue
		}
		if _, queued := retained[id]; !queued {
			delete(s.announcing, id)
		}
	}
}

// AlertTransition is health.Config.OnTransition: an alert opened (or re-opened) or ended. An alert already announced as open
// is not announced again; one that ended because a worse one replaced it, was accepted as normal or lost its node says
// nothing; one the admin muted ends and re-opens quietly.
func (s *Service) AlertTransition(a store.HealthAlert, resolved bool) {
	if a.Severity < minSeverity {
		return
	}
	if !s.cfg.Edge { // on the edge the alert row and the health-eval lease decide; a per-isolate map would drop a re-open
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
	}
	if resolved {
		switch a.Resolution {
		case "superseded", "accepted", "node_retired":
			return
		}
		if a.Kind == "torrent" || a.Kind == "device_limit" {
			return // a person is told once a day: the end of a quiet day is not a second message
		}
		if a.MutedUntil.After(s.now()) {
			return
		}
	}
	if !resolved && a.MutedUntil.After(s.now()) {
		return // a muted alert that flaps back is the news the admin silenced
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
	return s.wait(ctx, d, nil)
}

// canSend is whether a message for this audience would reach anybody (a bot and an enabled chat). The watcher checks this
// before it reads the update sources.
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
	// A watcher with no owner chat has no useful work; in particular, avoid the update-source reads on every edge isolate
	// when Telegram has not been configured.
	if !s.canSend(ctx, ownerOnly) {
		return
	}
	s.mu.Lock()
	src := s.sources
	s.mu.Unlock()
	adminURL := s.cfg.AdminURL
	told := func(key, version string) bool {
		v, err := s.st.Setting(ctx, key)
		return err == nil && v == version
	}
	if src.PanelRelease != nil {
		if v, ok := src.PanelRelease(); ok && v != "" && !told(notifiedRel, v) {
			s.enqueueNotice(notifiedRel, v, item{to: ownerOnly, text: func(l L, _ string) string { return releaseText(l, v, adminURL) }})
		}
	}
	if src.OutdatedAgents != nil {
		if v, nodes := src.OutdatedAgents(ctx); v != "" && len(nodes) > 0 && !told(notifiedAgent, v) {
			s.enqueueNotice(notifiedAgent, v, item{to: ownerOnly, text: func(l L, _ string) string { return agentsOutdatedText(l, v, nodes, adminURL) }})
		}
	}
}

// WatchTick runs the release and agent watcher every fifth scheduled minute on the edge.
func (s *Service) WatchTick(ctx context.Context, now time.Time) {
	if now.UTC().Minute()%5 == 0 {
		s.watch(ctx)
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

// FlushNow replaces the VPS burst window for bounded edge jobs and request producers.
func (s *Service) FlushNow(ctx context.Context) {
	s.flush(ctx)
}

func (s *Service) flushAfterResponse() {
	if !s.cfg.Edge || s.cfg.AfterResponse == nil {
		return
	}
	s.cfg.AfterResponse(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second) // ends before Cloudflare's 30 s waitUntil limit
		defer cancel()
		s.FlushNow(ctx)
	})
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
		var retained map[string]struct{}
		if ctx.Err() != nil {
			retained = s.requeueUndelivered(items, nil, nil)
		}
		s.finishNotices(items, retained)
		return
	}
	links, err := s.st.TelegramLinks(ctx)
	if err != nil {
		s.log.Warn("telegram: read links", "err", err)
		var retained map[string]struct{}
		if ctx.Err() != nil {
			retained = s.requeueUndelivered(items, nil, nil)
		}
		s.finishNotices(items, retained)
		return
	}
	l, brand := s.panel(ctx)
	finished := make([]map[string]struct{}, len(items))
	markFinished := func(itemIndex int, adminID string) {
		if finished[itemIndex] == nil {
			finished[itemIndex] = map[string]struct{}{}
		}
		finished[itemIndex][adminID] = struct{}{}
	}
	for _, link := range links {
		if ctx.Err() != nil {
			retained := s.requeueUndelivered(items, links, finished)
			s.finishNotices(items, retained)
			return
		}
		if !link.Enabled {
			continue
		}
		var lines []string
		lineItems := make([][]int, 0, len(items))
		lineIndexes := make(map[string]int, len(items))
		for itemIndex, it := range items {
			if !it.to(link) {
				continue
			}
			t := it.text(l, brand)
			if t == "" {
				markFinished(itemIndex, link.AdminID)
				continue
			}
			if lineIndex, found := lineIndexes[t]; found {
				lineItems[lineIndex] = append(lineItems[lineIndex], itemIndex)
				continue
			}
			lineIndexes[t] = len(lines)
			lines = append(lines, t)
			lineItems = append(lineItems, []int{itemIndex})
		}
		for _, msg := range chunk(lines) {
			if ctx.Err() != nil {
				retained := s.requeueUndelivered(items, links, finished)
				s.finishNotices(items, retained)
				return
			}
			result := s.deliver(ctx, tok, link.ChatID, msg.text)
			switch result {
			case deliveryCanceled:
				retained := s.requeueUndelivered(items, links, finished)
				s.finishNotices(items, retained)
				return
			case deliveryGaveUp:
				if s.cfg.Edge {
					continue // the next tick tries again; a VPS drops it
				}
			}
			for _, lineIndex := range msg.lineIndexes {
				for _, itemIndex := range lineItems[lineIndex] {
					if result == deliverySent {
						s.noticeDelivered(ctx, items[itemIndex])
					}
					markFinished(itemIndex, link.AdminID)
				}
			}
		}
	}
	var retained map[string]struct{}
	if s.cfg.Edge {
		retained = s.requeueUndelivered(items, links, finished)
	}
	s.finishNotices(items, retained)
}

func (s *Service) requeueUndelivered(items []item, links []store.TelegramLinkRow, finished []map[string]struct{}) map[string]struct{} {
	retained := map[string]struct{}{}
	var remaining []item
	for i, it := range items {
		if links == nil {
			remaining = append(remaining, it)
			continue
		}
		admins := make(map[string]struct{})
		for _, link := range links {
			if !link.Enabled || !it.to(link) {
				continue
			}
			if i < len(finished) {
				if _, done := finished[i][link.AdminID]; done {
					continue
				}
			}
			admins[link.AdminID] = struct{}{}
		}
		if len(admins) == 0 {
			continue
		}
		to := it.to
		it.to = func(link store.TelegramLinkRow) bool {
			_, wanted := admins[link.AdminID]
			return wanted && to(link)
		}
		remaining = append(remaining, it)
	}
	if len(remaining) == 0 {
		return retained
	}
	s.mu.Lock()
	pending := make([]item, 0, len(remaining)+len(s.pending))
	pending = append(pending, remaining...)
	pending = append(pending, s.pending...)
	s.pending = pending
	s.mu.Unlock()
	for _, it := range remaining {
		if id := it.noticeID(); id != "" {
			retained[id] = struct{}{}
		}
	}
	return retained
}

// chunk joins lines into messages that fit Telegram's limit (a line is never split; one too long is cut).
func chunk(lines []string) []messageChunk {
	var out []messageChunk
	var cur string
	var lineIndexes []int
	for i, ln := range lines {
		if len([]rune(ln)) > maxMessage {
			ln = string([]rune(ln)[:maxMessage])
		}
		if cur != "" && len([]rune(cur))+2+len([]rune(ln)) > maxMessage {
			out = append(out, messageChunk{text: cur, lineIndexes: lineIndexes})
			cur = ""
			lineIndexes = nil
		}
		if cur != "" {
			cur += "\n\n"
		}
		cur += ln
		lineIndexes = append(lineIndexes, i)
	}
	if cur != "" {
		out = append(out, messageChunk{text: cur, lineIndexes: lineIndexes})
	}
	return out
}

type messageChunk struct {
	text        string
	lineIndexes []int
}

type deliveryResult uint8

const (
	deliverySent    deliveryResult = iota
	deliveryRefused                // a 4xx or a refused token: retrying cannot help, dropped on both editions
	deliveryGaveUp                 // retries used up: dropped on the VPS, requeued on the edge
	deliveryCanceled
)

// deliver sends one message, trying again after a network failure or a server error with a doubling wait, and after 429 for
// as long as Telegram asks. A chat that cannot be written to (the user blocked the bot) or a refused token ends it at once
// (deliveryRefused); when the retries are used up the edge keeps the message for the next tick (deliveryGaveUp).
func (s *Service) deliver(ctx context.Context, tok vault.Redacted, chat int64, text string) deliveryResult {
	wait := s.cfg.RetryBase
	attempt := 1
retry:
	for {
		if ctx.Err() != nil {
			return deliveryCanceled
		}
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.api.sendMessage(sctx, tok, chat, text)
		cancel()
		if err == nil {
			s.setBotErr("")
			return deliverySent
		}
		if ctx.Err() != nil {
			return deliveryCanceled
		}
		var ae *apiError
		errors.As(err, &ae)
		switch {
		case ae != nil && ae.Status == http.StatusTooManyRequests:
			d := retryAfterWait(ae.RetryAfter)
			s.log.Warn("telegram: rate limited", "retry_in", d.String())
			if !s.sleepPlain(ctx, d) {
				return deliveryCanceled
			}
			continue
		case ae != nil && ae.Status == http.StatusUnauthorized:
			s.setBotErr("unauthorized")
			s.log.Warn("telegram: the bot token is refused")
			return deliveryRefused
		case ae != nil && !ae.Transport && ae.Status >= 400 && ae.Status < 500:
			s.log.Warn("telegram: message refused", "status", ae.Status, "desc", clip(ae.Desc, 80)) // a blocked bot, a deleted chat
			return deliveryRefused
		default:
			s.setBotErr("unreachable")
			if attempt >= s.cfg.RetryMax {
				s.log.Warn("telegram: send failed, retry limit reached", "code", errCode(err), "attempt", attempt)
				break retry
			}
			s.log.Warn("telegram: send failed, will retry", "code", errCode(err), "attempt", attempt)
			if !s.sleepPlain(ctx, wait) {
				return deliveryCanceled
			}
			wait *= 2
			attempt++
		}
	}
	s.log.Warn("telegram: message dropped after retries")
	return deliveryGaveUp
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
