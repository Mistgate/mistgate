package health

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// history is CountryHistory over the last week, one line per lane: "node source verdict@hours-ago,... until=hours-ago".
func (c *countryEnv) history() (sources []string, lanes []string) {
	c.t.Helper()
	now := c.clock.Now()
	sources, nodes, err := c.s.CountryHistory(c.ctx, now.Add(-7*24*time.Hour))
	if err != nil {
		c.t.Fatal(err)
	}
	ago := func(unix int64) int64 { return (now.Unix() - unix) / 3600 }
	for _, n := range nodes {
		for _, l := range n.Lanes {
			var pts []string
			for _, p := range l.Points {
				pts = append(pts, fmt.Sprintf("%s@%d", strings.ToLower(strings.TrimPrefix(p.Verdict.String(), "COUNTRY_VERDICT_")), ago(p.AtUnix)))
			}
			lanes = append(lanes, fmt.Sprintf("%s %s %s until=%d", n.NodeId, l.Source, strings.Join(pts, ","), ago(l.UntilUnix)))
		}
	}
	return sources, lanes
}

// The stored week: Globalping saw nl1 TCP-cut on day 4 and back the day after, with a Kazakh round in between; the lists hold
// se1 from the second check on; the probe node saw nl1 blocked, then a partial run with an open port (ok), then nothing for a day.
func (c *countryEnv) storeWeek() {
	c.t.Helper()
	now := c.clock.Now()
	h := func(n int) time.Time { return now.Add(-time.Duration(n) * time.Hour) }
	var gp []store.CountryCheck
	for _, r := range []struct {
		node, country, verdict string
		ago                    int
	}{
		{"nl1", "RU", "blocked", 200}, // older than the week
		{"nl1", "RU", "ok", 160}, {"nl1", "RU", "ok", 120}, {"nl1", "RU", "tcp_blocked", 96}, {"nl1", "KZ", "blocked", 90},
		{"nl1", "RU", "tcp_blocked", 80}, {"nl1", "RU", "ok", 72}, {"nl1", "RU", "unknown", 10}, {"nl1", "RU", "ok", 9},
		{"de1", "RU", "ok", 150}, {"de1", "RU", "ok", 1},
	} {
		gp = append(gp, store.CountryCheck{NodeID: r.node, Country: r.country, Verdict: r.verdict, At: h(r.ago)})
	}
	if err := c.st.InsertCountryChecks(c.ctx, gp); err != nil {
		c.t.Fatal(err)
	}
	var lists []store.BlocklistCheck
	for _, r := range []struct {
		node   string
		listed bool
		ago    int
	}{{"se1", false, 150}, {"se1", true, 144}, {"se1", true, 138}, {"de1", false, 150}, {"de1", false, 6}} {
		row := store.BlocklistCheck{NodeID: r.node, At: h(r.ago)}
		if r.listed {
			row.InRKN, row.ListedBy = true, "antifilter_ipsum"
		}
		lists = append(lists, row)
	}
	if err := c.st.InsertBlocklistChecks(c.ctx, lists); err != nil {
		c.t.Fatal(err)
	}
	if err := c.st.InsertTSPUChecks(c.ctx, []store.TSPUCheck{
		{NodeID: "nl1", Vantage: "probe1", At: h(50), Verdict: "blocked", DetailJSON: `[{"port":443,"verdict":"blocked"}]`},
		{NodeID: "nl1", Vantage: "probe1", At: h(48), Verdict: "blocked", DetailJSON: `[{"port":443,"verdict":"blocked"}]`},
		{NodeID: "nl1", Vantage: "probe1", At: h(30), Verdict: "partial", DetailJSON: `[{"port":443,"verdict":"blocked"},{"port":8443,"verdict":"open"}]`},
		{NodeID: "de1", Vantage: "probe9", At: h(2), Verdict: "blocked"}, // another probe node: not the one in use
	}); err != nil {
		c.t.Fatal(err)
	}
}

// Every source that is off has no lane, as GetChecks shows nothing of it; with all three off there is nothing at all.
func TestCountryHistoryHidesTheSourcesThatAreOff(t *testing.T) {
	c := newCountryEnv(t)
	c.storeWeek()
	c.cfg.Enabled = false
	if sources, lanes := c.history(); len(sources) != 0 || len(lanes) != 0 {
		t.Fatalf("every source off: %v %v", sources, lanes)
	}

	c.lists.Store(true)
	sources, lanes := c.history()
	if want := []string{"de1 lists ok@150 until=0", "se1 lists ok@150,listed@144 until=0"}; !slices.Equal(sources, []string{"lists"}) || !slices.Equal(lanes, want) {
		t.Fatalf("the lists alone: %v\n%q\nwant %q", sources, lanes, want)
	}
}

// Consecutive equal verdicts are one point: per source, in the order globalping, lists, tspu. Globalping keeps the selected
// country's rounds only; the probe node's lane holds the current probe node's runs mapped as Combine does, and ends three
// intervals after its newest run; a source with nothing on a node has no lane there.
func TestCountryHistoryCompactsEachSource(t *testing.T) {
	c := newCountryEnv(t)
	c.storeWeek()
	c.lists.Store(true)
	c.tspuOn() // probe1, rows count for 6 hours
	sources, lanes := c.history()
	if !slices.Equal(sources, []string{"globalping", "lists", "tspu"}) {
		t.Fatalf("sources = %v", sources)
	}
	want := []string{
		"de1 globalping ok@150 until=0",
		"de1 lists ok@150 until=0",
		"nl1 globalping ok@160,tcp_blocked@96,ok@72,unknown@10,ok@9 until=0",
		"nl1 tspu tcp_blocked@50,ok@30 until=24",
		"se1 lists ok@150,listed@144 until=0",
	}
	if !slices.Equal(lanes, want) {
		t.Fatalf("lanes:\n%q\nwant\n%q", lanes, want)
	}

	// another country: Globalping's lane starts over (nothing yet), the lists and the probe stay
	c.cfg.Country = "KZ"
	if _, lanes := c.history(); !slices.Contains(lanes, "nl1 globalping blocked@90 until=0") || slices.Contains(lanes, "de1 globalping ok@150 until=0") {
		t.Fatalf("KZ lanes: %q", lanes)
	}
}
