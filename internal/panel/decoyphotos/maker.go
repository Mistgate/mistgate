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
	// MaxSearches and MaxFiles are the most requests one site costs its source.
	MaxSearches = 3
	MaxFiles    = sitegen.MaxPhotos

	// Budget is what the photos of one site may weigh together, so that the site stays well inside decoy.MaxBundleBytes.
	Budget = 1300 << 10

	perPage     = 30
	pages       = 3  // the page of results is drawn from the first three: later ones are often thin
	searchKeep  = 48 // search pages kept per source
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

// searchTTL is how long a source's search pages are kept, so that asking for another variant of a site costs no search. Pixabay's
// terms ask for 24 hours (the file addresses in an answer live as long); Pexels has no such rule, an hour is plenty.
var searchTTL = map[string]time.Duration{SourcePexels: time.Hour, SourcePixabay: 24 * time.Hour}

// Source is what the Maker needs of a photo service: a page of results, and the bytes of a file one of them names.
type Source interface {
	Search(ctx context.Context, key vault.Redacted, query string, portrait bool, page, perPage int) ([]Photo, error)
	Download(ctx context.Context, rawURL string) ([]byte, error)
}

// source is a Source with what the Maker remembers of it: its search pages, and the time until which it is left alone.
type source struct {
	Source
	ttl      time.Duration
	cache    map[string]cached
	cooldown time.Time
}

// Result is what Make brings back: photos by slot name, and why there are none (or fewer than asked).
type Result struct {
	Photos map[string][]byte
	Reason string
}

// Maker finds, downloads and prepares the photos of a site. It remembers the search pages of each source (searchTTL), and after a
// "too many requests" it leaves that source alone for a while.
type Maker struct {
	now func() time.Time

	mu   sync.Mutex // guards the cache and the cooldown of every source
	srcs map[string]*source
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
	mk := func(name string, s Source) *source {
		return &source{Source: s, ttl: searchTTL[name], cache: map[string]cached{}}
	}
	return &Maker{now: now, srcs: map[string]*source{SourcePexels: mk(SourcePexels, New(rt)), SourcePixabay: mk(SourcePixabay, NewPixabay(rt))}}
}

// people are words in the source's own description of a picture (Pexels' text, Pixabay's tags) that make it unfit: a site must not
// show a real person as its owner.
var people = regexp.MustCompile(`(?i)\b(man|men|woman|women|person|people|girl|boy|child|children|kid|kids|baby|lady|guy|couple|family|crowd|team|group|portrait|selfie|face|faces|smiling|customer|worker|workers|baker|barista|chef|waiter)\b`)

func (m *Maker) searchPage(ctx context.Context, src *source, key vault.Redacted, query string, portrait bool, page int, spent *int) ([]Photo, error) {
	ck := query + "|" + map[bool]string{false: "l", true: "p"}[portrait] + "|" + string(rune('0'+page))
	m.mu.Lock()
	if e, ok := src.cache[ck]; ok && m.now().Sub(e.at) < src.ttl {
		m.mu.Unlock()
		return e.photos, nil
	}
	if m.now().Before(src.cooldown) {
		m.mu.Unlock()
		return nil, &Error{Code: CodeLimit}
	}
	m.mu.Unlock()
	if *spent >= MaxSearches {
		return nil, nil
	}
	*spent++
	got, err := src.Search(ctx, key, query, portrait, page, perPage)
	m.mu.Lock()
	defer m.mu.Unlock()
	if err != nil {
		var e *Error
		if errors.As(err, &e) && e.Code == CodeLimit {
			wait := e.RetryAfter
			if wait <= 0 {
				wait = coolDefault
			}
			src.cooldown = m.now().Add(min(wait, coolMax))
		}
		return nil, err
	}
	kept := got[:0:0]
	for _, p := range got {
		if !people.MatchString(p.Text) {
			kept = append(kept, p)
		}
	}
	if len(src.cache) >= searchKeep {
		var oldest string
		for k, e := range src.cache {
			if oldest == "" || e.at.Before(src.cache[oldest].at) {
				oldest = k
			}
		}
		delete(src.cache, oldest)
	}
	src.cache[ck] = cached{at: m.now(), photos: kept}
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

// fileFor is the file of p to cut slot s from: the smaller one when it is sharp enough, else the bigger one, "" when neither is. A
// photo is never enlarged, so a file whose cut to the slot's shape is narrower than four fifths of the slot (at most MaxW) would
// show soft: the slot stays drawn. A file of unknown size is trusted.
func fileFor(p Photo, s sitegen.Slot) string {
	want := min(s.W, s.MaxW)
	sharp := func(w, h int) bool {
		return w <= 0 || h <= 0 || min(w, h*s.W/s.H)*5 >= want*4
	}
	if s.MaxW <= 640 && sharp(p.SmallW, p.SmallH) {
		return p.Small
	}
	if sharp(p.BigW, p.BigH) {
		return p.Big
	}
	return ""
}

// Make fetches a photo for every slot of the plan it can from one source ("pexels" or "pixabay"): at most MaxSearches searches
// and MaxFiles downloads, all to that source. The same seed and the same search results choose the same photos. A slot that got
// none is simply missing from the result; Reason says why when the result is empty ("used" otherwise).
func (m *Maker) Make(ctx context.Context, srcName string, key vault.Redacted, seed []byte, plan []sitegen.Slot) Result {
	src := m.srcs[srcName]
	if src == nil {
		return Result{Reason: ReasonUnavailable}
	}
	var groups []*group
	for _, s := range plan {
		portrait := s.H*10 > s.W*11
		var g *group
		for _, x := range groups {
			// one search per query, whatever the shapes of its slots: a picture is cropped to its slot anyway, and a kind
			// that shows one subject in an arch and in a wide tile must not spend two of the three searches on it
			if x.query == s.Query {
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
		url  string
	}
	var picks []pick
	spent := 0
	var firstErr error
	used := map[int64]bool{}
	for gi, g := range groups {
		page := 1 + int(draw(seed, "page:"+g.query)%pages)
		res, err := m.searchPage(ctx, src, key, g.query, g.portrait, page, &spent)
		// a thin page: the first one is the safe one, if the searches the other groups need are left
		if err == nil && len(res) < len(g.slots) && page != 1 && spent+len(groups)-gi-1 < MaxSearches {
			if first, err1 := m.searchPage(ctx, src, key, g.query, g.portrait, 1, &spent); err1 == nil && len(first) > len(res) {
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
		for _, s := range g.slots {
			for _, i := range order { // the first unused result that has a file sharp enough for the slot
				if p := res[i]; !used[p.ID] {
					if u := fileFor(p, s); u != "" {
						used[p.ID] = true
						picks = append(picks, pick{s, u})
						break
					}
				}
			}
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
			data, err := src.Download(ctx, p.url)
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
