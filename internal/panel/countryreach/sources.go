package countryreach

// SourceKlick is the kl!ck reports a node's "Check now" reads again. It takes no part in the verdict: it has a place in Sources
// for the list of that round only.
const SourceKlick = "klick"

// NowMode is what the "Check now" round of one node (health.RunCountryNow) does with a source. The order of the modes is the order
// of the round's answer.
type NowMode int

const (
	// NowNone: the source takes no part in the round.
	NowNone NowMode = iota
	// NowRound: measured by the country round itself, from what is left of the source's limit.
	NowRound
	// NowPlan: asked for this node through its own plan (health.Config.CountryNow).
	NowPlan
	// NowSchedule: paid, runs on its schedule only; the round says so.
	NowSchedule
	// NowReread: read again from what the panel holds.
	NowReread
)

// SourceInfo is what the panel knows of a source of the country check before it has seen one row of it.
type SourceInfo struct {
	ID string
	// Path: the source shows the path past the TSPU but cannot prove the node answered (Live.Path).
	Path bool
	// Eyeball: every observation of the source comes from home or mobile networks (Live.Eyeball). The own probes are the exception:
	// there it is the kind of the point.
	Eyeball bool
	// Broad: the source sees the whole country, so its full block may open the full alert; a source that picks regions or single
	// networks raises only the partial one.
	Broad bool
	Now   NowMode
}

// Sources is every source of the country check in the fixed order in which a verdict, an alert and a screen name them.
var Sources = []SourceInfo{
	{ID: SourceGlobalping, Broad: true, Now: NowRound},
	{ID: SourceTSPU, Broad: true, Now: NowPlan},
	{ID: SourceCheburcheck, Path: true, Eyeball: true, Broad: true, Now: NowPlan},
	{ID: SourceCheburcheckOwn, Path: true, Eyeball: true, Now: NowPlan},
	{ID: SourceBsbord, Eyeball: true, Now: NowSchedule},
	{ID: SourceBsbordTunnel, Eyeball: true},
	{ID: SourceBsbordGeo, Eyeball: true},
	{ID: SourceLists, Now: NowReread},
	{ID: SourceProbes, Now: NowPlan},
	{ID: SourceKlick, Now: NowReread},
}

var (
	sourceIndex = func() map[string]int {
		m := make(map[string]int, len(Sources))
		for i, s := range Sources {
			m[s.ID] = i
		}
		return m
	}()
	nowSources = func() []SourceInfo {
		var out []SourceInfo
		for mode := NowRound; mode <= NowReread; mode++ { // no sort: the generic one costs the edge Workers 13 KB
			for _, s := range Sources {
				if s.Now == mode {
					out = append(out, s)
				}
			}
		}
		return out
	}()
)

// Info is what is known of a source by its id; false for a name the panel does not know.
func Info(id string) (SourceInfo, bool) {
	i, ok := sourceIndex[id]
	if !ok {
		return SourceInfo{}, false
	}
	return Sources[i], true
}

// NowSources are the sources a "Check now" round answers for, in the order of its answer: by mode, then in the order of Sources.
// The caller must not change the slice.
func NowSources() []SourceInfo { return nowSources }

// sourceRank is the place of a source in the fixed order; a name the panel does not know comes after every known one.
func sourceRank(id string) int {
	if i, ok := sourceIndex[id]; ok {
		return i
	}
	return len(Sources)
}
