package ownprobe

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/panel/auth"
	"github.com/mistgate/mistgate/internal/panel/countryreach"
	"github.com/mistgate/mistgate/internal/panel/httpserver"
	"github.com/mistgate/mistgate/internal/panel/securitylimit"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The failures of a stranger are counted per source, apart from every other limit of the panel: a wrong enrolment token and a
// wrong secret. Past the limit the answer is the 429 of the decoy site, so the limit does not tell the endpoint from any other
// path under a secret prefix.
var (
	enrollFailWindow = securitylimit.Window{Name: "own-probe-enroll-failure", Limit: 10, Span: time.Minute}
	authFailWindow   = securitylimit.Window{Name: "own-probe-auth-failure", Limit: 30, Span: time.Minute}
)

// maxEnrollBody is generous for a 56-character token.
const maxEnrollBody = 128

// publicHandler is what the probes reach under probe_prefix: GET probe.sh, POST enroll, POST sync. Everything else, and every
// request of a stranger (unknown path, unknown token, unknown or revoked secret), gets the same 404 as any other path of the site.
type publicHandler struct {
	service *Service
	decoy   http.Handler
	tooMany func(http.ResponseWriter, *http.Request, time.Duration)
}

func (h publicHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/probe.sh":
		h.script(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/enroll":
		h.enroll(w, r)
	case r.Method == http.MethodPost && r.URL.Path == "/sync":
		h.sync(w, r)
	default:
		h.decoy.ServeHTTP(w, r)
	}
}

func (h publicHandler) script(w http.ResponseWriter, r *http.Request) {
	etag := scriptETag()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "no-cache")
	for _, value := range strings.Split(r.Header.Get("If-None-Match"), ",") {
		if strings.TrimSpace(value) == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(probeScript)))
	_, _ = w.Write(probeScript)
}

func (h publicHandler) enroll(w http.ResponseWriter, r *http.Request) {
	s := h.service
	ctx := r.Context()
	key := limitKey(ipFor(r))
	if d, err := s.cfg.Limiter.Peek(ctx, enrollFailWindow, key); err != nil || !d.Allowed {
		h.tooMany(w, r, d.RetryAfter)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEnrollBody+1))
	token := strings.TrimSuffix(string(body), "\n")
	if err != nil || len(body) > maxEnrollBody || !validProbeToken(token) {
		h.failed(w, r, enrollFailWindow, key)
		return
	}
	asn, operator, ok := s.network(ctx, ipFor(r))
	if !ok {
		s.cfg.Log.Warn("own probe enrol: the network of the request is not in the ASN table", "code", "own_probe_asn_unknown")
		h.failed(w, r, enrollFailWindow, key)
		return
	}
	secret, secretHash, err := tokenSecret()
	if err != nil {
		s.cfg.Log.Error("generate own probe secret", "code", "own_probe_enroll_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	probe, err := s.cfg.Store.EnrollOwnProbe(ctx, hashCredential(token), secretHash, asn, operator, operator, ScriptVersion(), s.cfg.Now().UTC())
	if errors.Is(err, store.ErrNotFound) {
		h.failed(w, r, enrollFailWindow, key)
		return
	}
	if err != nil {
		s.cfg.Log.Error("enroll own probe", "code", "own_probe_enroll_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, "mgp 1\nid %s\nsecret %s\nend\n", probe.ID, secret)
}

// failed counts one failure of the source and answers like any path of the site: the decoy's 404, or its 429 past the limit.
func (h publicHandler) failed(w http.ResponseWriter, r *http.Request, window securitylimit.Window, key string) {
	d, err := h.service.cfg.Limiter.Record(r.Context(), window, key)
	if err != nil || !d.Allowed {
		h.tooMany(w, r, d.RetryAfter)
		return
	}
	h.decoy.ServeHTTP(w, r)
}

func (h publicHandler) sync(w http.ResponseWriter, r *http.Request) {
	s := h.service
	ctx := r.Context()
	key := limitKey(ipFor(r))
	if d, err := s.cfg.Limiter.Peek(ctx, authFailWindow, key); err != nil || !d.Allowed {
		h.tooMany(w, r, d.RetryAfter)
		return
	}
	secret := bearerSecret(r.Header.Get("Authorization"))
	if secret == "" {
		h.failed(w, r, authFailWindow, key)
		return
	}
	secretHash := hashCredential(secret)
	probe, err := s.cfg.Store.OwnProbeBySecretHash(ctx, secretHash)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.cfg.Log.Error("read own probe for sync", "code", "own_probe_sync_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	// The lookup is by the hash; the comparison that decides is constant-time on the hashes, so "no such probe" and "wrong
	// secret" cost the same and give the same answer.
	if err != nil || subtle.ConstantTimeCompare(probe.SecretHash, secretHash) != 1 {
		h.failed(w, r, authFailWindow, key)
		return
	}
	if d, err := s.cfg.Limiter.Take(ctx, securitylimit.Bucket{Name: probeSyncBucket, Burst: float64(s.cfg.SyncBurst), Refill: probeSyncRefill}, "p:"+probe.ID); err != nil || !d.Allowed {
		h.tooMany(w, r, d.RetryAfter)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxSyncBody+1))
	if err != nil || len(body) > maxSyncBody {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	report, err := ParseSync(body)
	if err != nil {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	asn, operator, ok := s.network(ctx, ipFor(r))
	if !ok {
		s.cfg.Log.Warn("own probe sync: the network of the request is not in the ASN table", "code", "own_probe_asn_unknown", "probe", probe.ID)
		w.Header().Set("Retry-After", "900")
		http.Error(w, "probe paused", http.StatusServiceUnavailable)
		return
	}
	wasActive := probe.State == "active"
	probe, err = s.cfg.Store.ObserveOwnProbeNetwork(ctx, probe.ID, secretHash, asn, operator, report.Version, s.cfg.Now().UTC())
	if errors.Is(err, store.ErrNotFound) { // revoked a moment ago
		h.decoy.ServeHTTP(w, r)
		return
	}
	if err != nil {
		s.cfg.Log.Error("observe own probe network", "code", "own_probe_sync_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if wasActive && probe.State == "network_changed" {
		// The point of a probe that moved to another network leaves the rows at once.
		s.mu.Lock()
		foldErr := s.refold(ctx)
		s.mu.Unlock()
		if foldErr != nil {
			s.cfg.Log.Error("refold after own probe network change", "code", "own_probe_fold_failed", "err", foldErr)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		s.changed(ctx)
	}
	applied, err := s.applyReport(ctx, probe.ID, report)
	if errors.Is(err, errBadSync) {
		http.Error(w, "invalid report", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.cfg.Log.Error("fold own probe report", "code", "own_probe_fold_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if applied {
		s.changed(ctx)
	}
	response, err := s.syncResponse(ctx, probe.ID)
	if errors.Is(err, errProbeReferencesUnavailable) {
		w.Header().Set("Retry-After", "900")
		http.Error(w, "probe paused", http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		s.cfg.Log.Error("build own probe sync response", "code", "own_probe_sync_failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = io.WriteString(w, response)
}

// applyReport folds a report into the probe's points when it answers the job the panel has outstanding for this probe, and does
// nothing with any other report (a stale job id, a replay, a job the owner cleared, a probe that is not active, the switch off).
// Results are matched to the saved job by their index; the report carries no address. It runs under the service lock: two
// reports of one probe, or a report and a revocation, are applied one at a time, and a job is consumed once.
func (s *Service) applyReport(ctx context.Context, id string, report SyncReport) (bool, error) {
	if report.JobID == noJobID {
		return false, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.cfg.Store.OwnProbe(ctx, id)
	if err != nil {
		return false, err
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return false, err
	}
	if current.State != "active" || !settings.Enabled || current.JobID == "" || current.JobID != report.JobID {
		return false, nil
	}
	targets := parseJob(current.JobJSON)
	if len(targets) == 0 || !resultsFit(report.Results, targets) {
		return false, errBadSync
	}
	now := s.cfg.Now().UTC()
	interval := time.Duration(settings.IntervalMin) * time.Minute
	if err := s.foldReport(ctx, current, report, targets, now, interval); err != nil {
		return false, err
	}
	if err := s.cfg.Store.CompleteOwnProbeJob(ctx, current.ID, report.JobID, report.Version, report.Reference, now, interval, len(report.Results)); err != nil {
		return false, err
	}
	return true, nil
}

// resultsFit says that every result points into the saved job and has the shape its target's kind can give.
func resultsFit(results []SyncResult, targets []Target) bool {
	for _, result := range results {
		if result.Index < 1 || result.Index > len(targets) || !result.fits(targets[result.Index-1].Kind) {
			return false
		}
	}
	return true
}

var errProbeReferencesUnavailable = errors.New("probe references unavailable")

// syncResponse is the answer to a sync: the control and reference addresses of the country and, for an active probe whose job is
// due, the targets. It reads the probe again under the lock, so a revocation or a network change that came a moment ago wins.
func (s *Service) syncResponse(ctx context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	probe, err := s.cfg.Store.OwnProbe(ctx, id)
	if err != nil {
		return "", err
	}
	settings, err := s.settings(ctx)
	if err != nil {
		return "", err
	}
	references, ok := s.references(s.cfg.Country(ctx))
	if !ok {
		return "", errProbeReferencesUnavailable
	}
	now := s.cfg.Now().UTC()
	state, jobID := "paused", noJobID
	var targets []Target
	if settings.Enabled && probe.State == "active" {
		state = "active"
		jobID, targets = probe.JobID, parseJob(probe.JobJSON)
		// An outstanding job the probe did not answer by its due time, or one it cannot run (a version without a kind), is dropped.
		if jobID != "" && (!now.Before(probe.JobDue) || !targetsFit(probe.Version, targets)) {
			jobID, targets = "", nil
			if err := s.cfg.Store.DueOwnProbe(ctx, probe.ID, now); err != nil {
				return "", err
			}
			probe.JobDue = now
		}
		if jobID == "" && !now.Before(probe.JobDue) {
			if jobID, targets, err = s.issueJob(ctx, probe, settings, now); err != nil {
				return "", err
			}
		}
		if jobID == "" {
			jobID = noJobID
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mgp 1\nnext %d\nstate %s\njob %s\nctl %s 443 %s\nref %s 443\n", pollSeconds(), state, jobID, references.ControlIP, references.ControlSNI, references.BlockedIP)
	for i, target := range targets {
		b.WriteString(formatTargetLine(i+1, target))
		b.WriteByte('\n')
	}
	b.WriteString("end\n")
	return b.String(), nil
}

// issueJob picks the targets of a probe's next job, among the kinds its version runs, and saves it with its due time.
func (s *Service) issueJob(ctx context.Context, probe store.OwnProbe, settings Settings, now time.Time) (string, []Target, error) {
	selected, err := s.chooseTargets(ctx, probe.Version)
	if err != nil || len(selected) == 0 {
		return "", nil, err
	}
	jobID, err := randomID(8)
	if err != nil {
		return "", nil, err
	}
	due := now.Add(time.Duration(settings.IntervalMin) * time.Minute)
	if err := s.cfg.Store.SetOwnProbeJob(ctx, probe.ID, jobID, encodeJob(selected), due); err != nil {
		return "", nil, err
	}
	return jobID, selected, nil
}

// references are the control and the blocked address of the country, with what the probe will put in a command line checked: an
// address that is not a public IPv4 or a name that is not a host name is no reference, and the probe is paused.
func (s *Service) references(country string) (References, bool) {
	if s.cfg.References == nil {
		return References{}, false
	}
	references, ok := s.cfg.References(country)
	return references, ok && countryreach.ProbeIPv4(references.ControlIP) && countryreach.ProbeIPv4(references.BlockedIP) && countryreach.ProbeSNI(references.ControlSNI)
}

// network is the ASN and the operator of the address of a request. The address goes no further than this function.
func (s *Service) network(ctx context.Context, ip netip.Addr) (uint32, string, bool) {
	if !ip.IsValid() || s.cfg.ASN == nil {
		return 0, "", false
	}
	asn := s.cfg.ASN(ip)
	if asn == 0 {
		return 0, "", false
	}
	operator := ""
	if s.cfg.Operator != nil {
		operator = s.cfg.Operator(s.cfg.Country(ctx), asn)
	}
	if operator == "" {
		operator = "AS" + strconv.FormatUint(uint64(asn), 10)
	}
	return asn, operator, true
}

// filterTargets keeps what can go into a line of the answer: a public IPv4 in canonical form, a port, a known kind, a host name
// and a path of the strict shapes. A target with anything else in it is dropped, never repaired. At most two per node.
func filterTargets(all []Target, version string) []Target {
	out := make([]Target, 0, min(len(all), maxTargets))
	perNode := map[string]int{}
	for _, target := range all {
		if !validTarget(target, version) || perNode[target.NodeID] >= 2 {
			continue
		}
		perNode[target.NodeID]++
		out = append(out, target)
	}
	return out
}

func validTarget(target Target, version string) bool {
	if target.NodeID == "" || !countryreach.ProbeIPv4(target.IP) || target.Port == 0 || !kindAllowed(version, target.Kind) {
		return false
	}
	if target.Kind == "tcp" {
		return target.SNI == "" && target.Path == ""
	}
	return countryreach.ProbeSNI(target.SNI) && (target.Path == "" || countryreach.ProbePath(target.Path))
}

func targetsFit(version string, targets []Target) bool {
	for _, target := range targets {
		if !validTarget(target, version) {
			return false
		}
	}
	return true
}

func bearerSecret(value string) string {
	secret, ok := strings.CutPrefix(value, "Bearer ")
	if !ok || secret == "" || len(secret) > 256 || strings.ContainsAny(secret, " \t\r\n\x00") {
		return ""
	}
	return secret
}

// pollSeconds is the next poll the answer asks for: 900 s, spread by 20 %, inside the 600..3600 the script accepts.
func pollSeconds() int {
	var raw [2]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return defaultPollSeconds
	}
	return 720 + (int(raw[0])<<8|int(raw[1]))%361
}

// limitKey is the source a failure is counted against: the IPv4 address or the IPv6 /64, hashed, so no address is kept even by
// the limiter.
func limitKey(ip netip.Addr) string {
	h := sha256.Sum256([]byte(auth.SourceKey(ip)))
	return hex.EncodeToString(h[:8])
}

// PublicHandler is the handler of the probes' endpoint, to mount under probe_prefix; decoyDir is the decoy site's directory.
func (s *Service) PublicHandler(decoyDir string) http.Handler {
	return publicHandler{service: s, decoy: httpserver.NewDecoyFor(decoyDir, s.cfg.Store), tooMany: httpserver.NewTooManyFor(decoyDir, s.cfg.Store)}
}
