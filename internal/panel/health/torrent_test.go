package health

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func addTorrentAttempt(e *env, nodeID, userID string, at time.Time, evidence, port string) {
	e.t.Helper()
	params := fmt.Sprintf(`{"protocol":"udp","torrent_protocol":"bittorrent_tracker","user_id":%q`, userID)
	if evidence != "" {
		params += fmt.Sprintf(`,"evidence":%q`, evidence)
	}
	if port != "" {
		params += fmt.Sprintf(`,"dst_port":%q`, port)
	}
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 2, 'torrent_attempt', 'agent', ?, ?)`,
		at.Unix(), nodeID, params+"}")
}

// One alert per person: it opens on the first attempt, stays the same alert while attempts keep coming (one Telegram
// message, not one per evaluation or per refresh), closes when the last attempt is a day old, and the next day's
// attempt is a new episode.
func TestTorrentAlertOpensStaysAndClosesAfterAQuietDay(t *testing.T) {
	var seen []Transition
	e := newEnv(t, func(c *Config) { c.OnTransition = func(tr Transition) { seen = append(seen, tr) } })
	e.node("node1", "provider", true)
	user := addHealthUser(e, "alice", "active", time.Time{}, time.Time{}, time.Time{})
	badge := func() uint32 { n, _ := e.s.AlertCounts(e.ctx); return n }

	start := e.clock.Now()
	addTorrentAttempt(e, "node1", user, start.Add(-time.Minute), "tracker_connect", "6969")
	e.evaluate()
	alert, ok := e.active()["torrent//"+user]
	if !ok {
		t.Fatalf("torrent alert did not open: %+v", e.active())
	}
	if alert.Severity != sevWarning || alert.WhyKey != "health.alert.torrent.why" || alert.TitleKey != "health.alert.torrent.title" || alert.NodeID != "" {
		t.Fatalf("alert = %+v", alert)
	}
	want := map[string]string{"user_id": user, "user_name": "alice", "nodes": "node1", "count": "1",
		"last_unix": strconv.FormatInt(start.Add(-time.Minute).Unix(), 10), "evidence": "tracker_connect", "ports": "6969"}
	for k, v := range want {
		if alert.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, alert.Params[k], v)
		}
	}
	if len(alert.Params) != len(want) {
		t.Errorf("params = %v", alert.Params)
	}
	if badge() != 0 {
		t.Errorf("a torrent alert is counted in the badge: %d", badge())
	}

	// A day and a half of attempts every ten minutes, evaluated every ten seconds' worth of passes: still the one alert.
	for i := 0; i < 36*6; i++ {
		e.clock.Advance(10 * time.Minute)
		addTorrentAttempt(e, "node1", user, e.clock.Now(), "dht_query", "6881")
		e.evaluate()
		e.evaluate()
		if got := e.active()["torrent//"+user]; got.ID != alert.ID {
			t.Fatalf("after %d attempts the alert is %+v, want the same id %s", i+1, got, alert.ID)
		}
	}
	if len(seen) != 1 || seen[0].Resolved {
		t.Fatalf("transitions while attempts continue = %+v, want a single open", seen)
	}
	got := e.active()["torrent//"+user]
	if got.Params["evidence"] != "dht_query" || got.Params["ports"] != "6881" {
		t.Errorf("params were not refreshed: %v", got.Params)
	}
	if n, _ := strconv.Atoi(got.Params["count"]); n < 100 {
		t.Errorf("count = %q, want attempts of the last 24 hours", got.Params["count"])
	}
	if len(e.history()) != 0 {
		t.Fatalf("history while attempts continue = %+v", e.history())
	}

	// Quiet: it is still open until the last attempt is a day old, then it closes.
	e.clock.Advance(24*time.Hour - time.Minute)
	e.evaluate()
	if _, ok := e.active()["torrent//"+user]; !ok {
		t.Fatal("alert closed before a quiet day")
	}
	e.clock.Advance(2 * time.Minute)
	e.evaluate()
	if _, ok := e.active()["torrent//"+user]; ok {
		t.Fatal("alert stayed open after a quiet day")
	}
	if len(seen) != 2 || !seen[1].Resolved || seen[1].Alert.Resolution != "cleared" {
		t.Fatalf("transitions after the quiet day = %+v", seen)
	}

	// The next day: a new episode (the re-open window is an hour, long past), announced again.
	e.clock.Advance(2 * time.Hour)
	addTorrentAttempt(e, "node1", user, e.clock.Now(), "utp_syn", "51413")
	e.evaluate()
	next, ok := e.active()["torrent//"+user]
	if !ok || next.ID == alert.ID {
		t.Fatalf("next day's alert = %+v (ok %v), want a new episode", next, ok)
	}
	if next.Params["count"] != "1" || next.Params["evidence"] != "utp_syn" || next.Params["ports"] != "51413" {
		t.Errorf("new episode params = %v", next.Params)
	}
	opens := 0
	for _, tr := range seen {
		if !tr.Resolved {
			opens++
		}
	}
	if opens != 2 {
		t.Fatalf("open transitions over two days = %d, want 2 (one per episode)", opens)
	}
}

func TestTorrentAlertNamesNodesAndIgnoresWhoCannotBeAlerted(t *testing.T) {
	e := newEnv(t)
	for _, id := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
		e.node(id, "provider", true)
	}
	user := addHealthUser(e, "alice", "active", time.Time{}, time.Time{}, time.Time{})
	disabled := addHealthUser(e, "carol", "disabled", time.Time{}, time.Time{}, time.Time{})
	now := e.clock.Now()
	for i, id := range []string{"n1", "n2", "n3", "n4", "n5", "n6", "n7"} {
		for j := 0; j <= i; j++ { // n7 the most attempts, n1 the fewest
			addTorrentAttempt(e, id, user, now.Add(-time.Duration(j+1)*time.Minute), "tcp_handshake", strconv.Itoa(6881+i))
		}
	}
	addTorrentAttempt(e, "n1", disabled, now.Add(-time.Minute), "dht_query", "6881")                                                                                 // not an active user
	addTorrentAttempt(e, "n1", "usr_gone", now.Add(-time.Minute), "dht_query", "6881")                                                                               // no such user
	addTorrentAttempt(e, "gone", user, now.Add(-time.Minute), "dht_query", "9999")                                                                                   // a node that is not there
	addTorrentAttempt(e, "n1", user, now.Add(-25*time.Hour), "dht_query", "7777")                                                                                    // older than a day
	e.exec(`INSERT INTO event (ts, severity, code, source, node_id, params_json) VALUES (?, 2, 'torrent_attempt', 'agent', 'n1', '{"protocol":"tcp"}')`, now.Unix()) // unknown user
	e.evaluate()

	active := e.active()
	if len(active) != 1 {
		t.Fatalf("alerts = %+v, want only alice's", active)
	}
	got := active["torrent//"+user]
	if got.Params["nodes"] != "n7, n6, n5, n4, n3, +2" || got.Params["count"] != "28" {
		t.Errorf("nodes = %q, count = %q", got.Params["nodes"], got.Params["count"])
	}
	if got.Params["ports"] != "6881, 6882, 6883, 6884, 6885" {
		t.Errorf("ports = %q, want at most five, smallest first", got.Params["ports"])
	}
	if actions := alertMsg(got, map[string]string{}, func(store.HealthAlert) string { return "" }).Actions; len(actions) != 2 || actions[0] != "open_user" || actions[1] != "mute" {
		t.Errorf("actions = %v, want open_user and mute", actions)
	}
}

// An agent that predates the evidence still raises the alert, without the two parameters.
func TestTorrentAlertWithoutEvidenceFromAnOlderAgent(t *testing.T) {
	e := newEnv(t)
	e.node("node1", "provider", true)
	user := addHealthUser(e, "alice", "active", time.Time{}, time.Time{}, time.Time{})
	addTorrentAttempt(e, "node1", user, e.clock.Now().Add(-time.Hour), "", "")
	e.evaluate()
	got := e.active()["torrent//"+user]
	if got.ID == "" {
		t.Fatalf("no alert: %+v", e.active())
	}
	if _, ok := got.Params["evidence"]; ok {
		t.Errorf("params = %v", got.Params)
	}
	if _, ok := got.Params["ports"]; ok {
		t.Errorf("params = %v", got.Params)
	}
}
