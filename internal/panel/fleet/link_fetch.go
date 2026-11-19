package fleet

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/plugin"
)

// LINK FETCH (agent.proto): a VPS panel gives a link session of an agent that lists link-fetch/1 a token in its HelloAck;
// with it the agent GETs update files and the network table at <link prefix>/fetch/<node id>/<file name>, the bytes that
// FetchUpdate streams over mTLS. The token is a secret: only its SHA-256 is kept, in memory, and it ends linkFetchGrace after
// its session (a restart of the panel ends every token; the agent gets a new one with its next session).

const linkFetchGrace = 10 * time.Minute

type linkFetchGrant struct {
	nodeID string
	live   bool      // its session runs
	until  time.Time // when !live: the end of the grace
}

type linkFetchTokens struct {
	mu sync.Mutex
	m  map[[32]byte]linkFetchGrant
}

// issue returns a new token for nodeID's running session.
func (t *linkFetchTokens) issue(nodeID string, now time.Time) (string, bool) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", false
	}
	token := base64.RawURLEncoding.EncodeToString(b[:])
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.m == nil {
		t.m = map[[32]byte]linkFetchGrant{}
	}
	for k, g := range t.m { // keeps the map to the live sessions and the graces that have not run out
		if !g.live && !now.Before(g.until) {
			delete(t.m, k)
		}
	}
	t.m[sha256.Sum256([]byte(token))] = linkFetchGrant{nodeID: nodeID, live: true}
	return token, true
}

// end starts the grace of a session's token.
func (t *linkFetchTokens) end(token string, now time.Time) {
	if token == "" {
		return
	}
	key := sha256.Sum256([]byte(token))
	t.mu.Lock()
	defer t.mu.Unlock()
	if g, ok := t.m[key]; ok {
		g.live, g.until = false, now.Add(linkFetchGrace)
		t.m[key] = g
	}
}

// check says token is nodeID's and still valid. The map is keyed by the token's hash, so the lookup takes the same time
// whatever the token.
func (t *linkFetchTokens) check(token, nodeID string, now time.Time) bool {
	if token == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	g, ok := t.m[sha256.Sum256([]byte(token))]
	return ok && g.nodeID == nodeID && (g.live || now.Before(g.until))
}

// fetchSessionStream is a link session's stream on a VPS panel: it remembers whether the agent's Hello lists link-fetch/1
// and puts a fetch token into the HelloAck it is sent. The token's grace starts when the session ends (done).
type fetchSessionStream struct {
	agentSessionStream
	f       *Fleet
	nodeID  string
	capable bool
	token   string
	now     func() time.Time // the fleet's clock, taken when the session started: done runs as the handler unwinds
}

func (s *fetchSessionStream) Receive() (*agentv1.ConnectRequest, error) {
	m, err := s.agentSessionStream.Receive()
	if h := m.GetHello(); h != nil {
		s.capable = slices.Contains(h.Capabilities, plugin.CapLinkFetch)
	}
	return m, err
}

func (s *fetchSessionStream) Send(m *agentv1.ConnectResponse) error {
	if ack := m.GetHelloAck(); ack != nil && s.capable && s.f.cfg.Remote == nil {
		if s.token == "" {
			s.token, _ = s.f.fetchTokens.issue(s.nodeID, s.f.now())
		}
		if s.token != "" {
			ack.FetchToken = s.token
		}
	}
	return s.agentSessionStream.Send(m)
}

func (s *fetchSessionStream) done() { s.f.fetchTokens.end(s.token, s.now()) }

// linkFetch serves GET /fetch/<node id>/<file name> with Range: bytes=<offset>- (200 from the start, 206 from an offset).
// Anything wrong is a 404, as for a path nobody serves; a busy bundle is a 503.
func (f *Fleet) linkFetch(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/fetch/"), "/")
	if f.cfg.Remote != nil || r.Method != http.MethodGet || len(parts) != 2 || !validLinkNodeID(parts[0]) || parts[1] == "" {
		http.NotFound(w, r)
		return
	}
	nodeID, name := parts[0], parts[1]
	credentials := strings.Fields(r.Header.Get("Authorization"))
	if len(credentials) != 2 || !strings.EqualFold(credentials[0], "Bearer") || !f.fetchTokens.check(credentials[1], nodeID, f.now()) {
		http.NotFound(w, r)
		return
	}
	if n, err := f.st.Node(r.Context(), nodeID); err != nil || n.State == "retired" {
		http.NotFound(w, r)
		return
	}
	offset, ok := rangeOffset(r.Header.Get("Range"))
	if !ok {
		http.Error(w, "bad range", http.StatusRequestedRangeNotSatisfiable)
		return
	}
	updates := f.updates()
	if updates == nil {
		http.NotFound(w, r)
		return
	}
	rc := http.NewResponseController(w)
	started := false
	err := updates.ServeWithWriteDeadline(r.Context(), nodeID, name, offset, rc.SetWriteDeadline, func(chunk []byte, total uint64) error {
		if !started {
			started = true
			w.Header().Set("Content-Type", "application/octet-stream")
			if offset > 0 {
				w.Header().Set("Content-Range", "bytes "+strconv.FormatUint(offset, 10)+"-"+strconv.FormatUint(total-1, 10)+"/"+strconv.FormatUint(total, 10))
				w.Header().Set("Content-Length", strconv.FormatUint(total-offset, 10))
				w.WriteHeader(http.StatusPartialContent)
			} else {
				w.Header().Set("Content-Length", strconv.FormatUint(total, 10))
				w.WriteHeader(http.StatusOK)
			}
		}
		_, err := w.Write(chunk)
		return err
	})
	if started {
		return // the body was cut: the agent resumes from what it got
	}
	switch connect.CodeOf(err) {
	case connect.CodeResourceExhausted, connect.CodeUnavailable:
		http.Error(w, "busy", http.StatusServiceUnavailable)
	default:
		if err == nil { // an empty file
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}
}

// rangeOffset reads "bytes=<n>-" ("" = 0); anything else is refused.
func rangeOffset(v string) (uint64, bool) {
	if v == "" {
		return 0, true
	}
	rest, ok := strings.CutPrefix(v, "bytes=")
	if !ok {
		return 0, false
	}
	start, ok := strings.CutSuffix(rest, "-")
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(start, 10, 64)
	return n, err == nil
}
