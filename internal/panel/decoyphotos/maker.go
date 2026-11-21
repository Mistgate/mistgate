//go:build !js

package decoyphotos

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/mistgate/mistgate/internal/decoy/sitegen"
	"github.com/mistgate/mistgate/internal/panel/vault"
)

const (
	// MaxSearches and MaxFiles are the most requests one site costs Pexels.
	MaxSearches = 3
	MaxFiles    = sitegen.MaxPhotos

	// Budget is what the photos of one site may weigh together, so that the site stays well inside decoy.MaxBundleBytes.
	Budget = 1300 << 10

	perPage     = 30
	pages       = 3 // the page of results is drawn from the first three: later ones are often thin
	searchTTL   = time.Hour
	searchKeep  = 48 // search pages kept
	coolDefault = 10 * time.Minute
	coolMax     = time.Hour
	fetchPar    = 4
)

// Reasons Result.Reason takes (the panel's API repeats them).
const (
	ReasonUsed        = "used"
	ReasonLimit       = "limit"
	ReasonBadKey      = "bad_key"
	ReasonUnavailable = "unavailable"
)

// Result is what Make brings back: photos by slot name, and why there are none (or fewer than asked).
type Result struct {
	Photos map[string][]byte
	Reason string
}

// Maker finds, downloads and prepares the photos of a site. It remembers the search pages for an hour, so that asking for another
// variant of a site costs no search, and after a "too many requests" it leaves Pexels alone for a while.
type Maker struct {
	c   *Client
	now func() time.Time

	mu       sync.Mutex
	cache    map[string]cached
	cooldown time.Time
}

type cached struct {
	at     time.Time
	photos []Photo
}

// NewMaker makes a Maker over rt (nil is the default transport). now may be nil.
func NewMaker(rt http.RoundTripper, now func() time.Time) *Maker {
	if now == nil {
		now = time.Now
	}
	return &Maker{c: New(rt), now: now, cache: map[string]cached{}}
}

// people are words in Pexels' own description of a picture that make it unfit: a site must not show a real person as its owner.
var people = regexp.MustCompile(`(?i)\b(man|men|woman|women|person|people|girl|boy|child|children|kid|kids|baby|lady|guy|couple|family|crowd|team|group|portrait|selfie|face|faces|smiling|customer|worker|workers|baker|barista|chef|waiter)\b`)

func (m *Maker) searchPage(ctx context.Context, key vault.Redacted, query string, portrait bool, page int, spent *int) ([]Photo, error) {
	ck := query + "|" + map[bool]string{false: "l", true: "p"}[portrait] + "|" + string(rune('0'+page))
	m.mu.Lock()
	if e, ok := m.cache[ck]; ok && m.now().Sub(e.at) < searchTTL {
		m.mu.Unlock()
		return e.photos, nil
	}
	if m.now().Before(m.cooldown) {
		m.mu.Unlock()
		return nil, &Error{Code: CodeLimit}
	}
	m.mu.Unlock()
	if *spent >= MaxSearches {
		return nil, nil
	}
	*spent++
	got, err := m.c.Search(ctx, key, query, portrait, page, perPage)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == CodeLimit {
			wait := e.RetryAfter
			if wait <= 0 {
				wait = coolDefault
			}
			m.cooldown = m.now().Add(min(wait, coolMax))
		}
		return nil, err
	}
	kept := got[:0:0]
	for _, p := range got {
		if !people.MatchString(p.Alt) {
			kept = append(kept, p)
		}
	}
	if len(m.cache) >= searchKeep {
		var oldest string
		for k, e := range m.cache {
			if oldest == "" || e.at.Before(m.cache[oldest].at) {
				oldest = k
			}
		}
		delete(m.cache, oldest)
	}
	m.cache[ck] = cached{at: m.now(), photos: kept}
	return kept, nil
}

// draw is a number from the seed and a label: the same inputs always give the same one.
func draw(seed []byte, label string) uint64 {
	h := sha256.New()
	h.Write(seed)
	h.Write([]byte{0})
	h.Write([]byte(label))
	return binary.BigEndian.Uint64(h.Sum(nil))
}

type group struct {
	query    string
	portrait bool
	slots    []sitegen.Slot
}

// Make fetches a photo for every slot of the plan it can: at most MaxSearches searches and MaxFiles downloads. The same seed
// and the same search results choose the same photos. A slot that got none is simply missing from the result; Reason says why
// when the result is empty ("used" otherwise).
func (m *Maker) Make(ctx context.Context, key vault.Redacted, seed []byte, plan []sitegen.Slot) Result {
	var groups []*group
	for _, s := range plan {
		portrait := s.H*10 > s.W*11
		var g *group
		for _, x := range groups {
			if x.query == s.Query && x.portrait == portrait {
				g = x
			}
		}
		if g == nil {
			if len(groups) == MaxSearches {
				continue
			}
			g = &group{query: s.Query, portrait: portrait}
			groups = append(groups, g)
		}
		if len(g.slots) < MaxFiles {
			g.slots = append(g.slots, s)
		}
	}

	type pick struct {
		slot sitegen.Slot
		ph   Photo
	}
	var picks []pick
	spent := 0
	var firstErr error
	used := map[int64]bool{}
	for gi, g := range groups {
		page := 1 + int(draw(seed, "page:"+g.query)%pages)
		res, err := m.searchPage(ctx, key, g.query, g.portrait, page, &spent)
		// a thin page: the first one is the safe one, if the searches the other groups need are left
		if err == nil && len(res) < len(g.slots) && page != 1 && spent+len(groups)-gi-1 < MaxSearches {
			if first, err1 := m.searchPage(ctx, key, g.query, g.portrait, 1, &spent); err1 == nil && len(first) > len(res) {
				res = first
			}
		}
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if ctx.Err() != nil {
				break
			}
			continue
		}
		// the results in an order that depends on the seed, each used once
		rank := make(map[int64]uint64, len(res))
		order := make([]int, len(res))
		for i, p := range res {
			order[i], rank[p.ID] = i, draw(seed, "pick:"+g.query+":"+strconv.FormatInt(p.ID, 10))
		}
		sort.Slice(order, func(a, b int) bool { return rank[res[order[a]].ID] < rank[res[order[b]].ID] })
		next := 0
		for _, s := range g.slots {
			for next < len(order) && used[res[order[next]].ID] {
				next++
			}
			if next == len(order) {
				break
			}
			used[res[order[next]].ID] = true
			picks = append(picks, pick{s, res[order[next]]})
		}
	}
	if len(picks) > MaxFiles {
		picks = picks[:MaxFiles]
	}

	scaled := make([]*Scaled, len(picks))
	var wg sync.WaitGroup
	sem := make(chan struct{}, fetchPar)
	var errMu sync.Mutex
	for i, p := range picks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			src := p.ph.Large
			if p.slot.MaxW > 640 {
				src = p.ph.Large2
			}
			data, err := m.c.Download(ctx, src)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			if s, err := Scale(data, p.slot.W, p.slot.H, p.slot.MaxW); err == nil {
				scaled[i] = s
			}
		}()
	}
	wg.Wait()

	var slots []sitegen.Slot // in the plan's order, so that the budget drops the last ones first
	byName := map[string]*Scaled{}
	for i, p := range picks {
		if scaled[i] != nil {
			byName[p.slot.Name] = scaled[i]
		}
	}
	for _, s := range plan {
		if byName[s.Name] != nil {
			slots = append(slots, s)
		}
	}
	out := Result{Photos: encodeWithin(byName, slots, Budget)}
	if len(out.Photos) > 0 {
		out.Reason = ReasonUsed
		return out
	}
	switch CodeOf(firstErr) {
	case CodeLimit:
		out.Reason = ReasonLimit
	case CodeKey:
		out.Reason = ReasonBadKey
	default:
		out.Reason = ReasonUnavailable
	}
	return out
}

// encodeWithin encodes the pictures at quality 75, then lower, until they weigh no more than budget together; when even the
// lowest does not fit, the last slots of the plan are left out one by one.
func encodeWithin(pics map[string]*Scaled, order []sitegen.Slot, budget int) map[string][]byte {
	names := make([]string, len(order))
	for i, s := range order {
		names[i] = s.Name
	}
	for len(names) > 0 {
		for _, q := range []int{75, 66, 56, 46} {
			out, total := make(map[string][]byte, len(names)), 0
			for _, n := range names {
				b, err := pics[n].JPEG(q)
				if err != nil {
					continue
				}
				out[n], total = b, total+len(b)
			}
			if total <= budget {
				return out
			}
		}
		names = names[:len(names)-1]
	}
	return map[string][]byte{}
}
