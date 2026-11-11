package access

import (
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// What the nodes saw next to what the table says: the measured counts of every device (one statement for all of them), the class,
// the disagreement, the summary over all devices, the filter by class, and the nodes that do not count.

const (
	actNone    = adminv1.ClientCompatActual_CLIENT_COMPAT_ACTUAL_NONE
	actPQ      = adminv1.ClientCompatActual_CLIENT_COMPAT_ACTUAL_PQ
	actClassic = adminv1.ClientCompatActual_CLIENT_COMPAT_ACTUAL_CLASSIC
	actMixed   = adminv1.ClientCompatActual_CLIENT_COMPAT_ACTUAL_MIXED
)

// hello puts hello counts on the credential of the device that fetched with this app string, in the hour that is `ago` before now.
func (c *compatEnv) hello(inbound, user, ua string, ago time.Duration, pq, classic, unknown uint32) {
	c.t.Helper()
	var cred string
	if err := c.st.R.QueryRowContext(c.ctx, `SELECT c.id FROM device_credential c JOIN device d ON d.id = c.device_id
		WHERE d.user_id = ? AND d.client = ? AND c.protocol = 'vless'`, c.vless[user], ua).Scan(&cred); err != nil {
		c.t.Fatalf("the credential of %s (%s): %v", user, ua, err)
	}
	hour := c.clock.Add(-ago).Unix() / 3600 * 3600
	c.seq++
	out, err := c.st.IngestStats(c.ctx, store.FleetStatsIn{NodeID: "nod_de1", Instance: "act", Seq: c.seq, Now: c.clock, HourStart: hour,
		Links: []store.FleetLink{{CredID: cred, InboundID: inbound, HelloPQ: pq, HelloClassic: classic, HelloUnknown: unknown}}})
	if err != nil || out.Duplicate || out.Skipped != 0 {
		c.t.Fatalf("IngestStats = %+v, %v", out, err)
	}
}

func actualOf(r *adminv1.ListOutdatedDevicesResponse, a adminv1.ClientCompatActual) (devices, people uint32) {
	for _, c := range r.ActualCounts {
		if c.Actual == a {
			return c.Devices, c.People
		}
	}
	return 0, 0
}

// actualEnv: eight devices of seven people on a REALITY server of de1 (its agent counts hellos); nl1 has another one (its agent does not).
func actualEnv(t *testing.T) (*compatEnv, string) {
	c := newCompatEnv(t)
	c.sql(`UPDATE node SET agent_caps = 'vless-hello-kex/1' WHERE id = 'nod_de1'`)
	reality := c.realityProfile("reality", "")
	in := c.inbound(reality, "nod_de1")
	c.inbound(reality, "nod_nl1")
	g := c.group("reality", reality)
	for _, p := range []struct{ name, hwid, os, ua string }{
		{"alice", "aaaaaaaaaa01", "Android", "Happ/3.4.2"},     // table ok
		{"bob", "bbbbbbbbbb01", "Android", "Happ/2.7.1"},       // table: below minimum
		{"carol", "cccccccccc01", "Android", "SFA/1.14.3"},     // table: unsupported
		{"dave", "dddddddddd01", "iOS", "Shadowrocket/2086"},   // table: unknown
		{"erin", "eeeeeeeeee01", "Windows", "SuperVPN/1.0"},    // table: unknown app, never measured
		{"frank", "ffffffffff01", "Android", "Happ/3.4.2"},     // table ok, only hellos that could not be read
		{"grace", "gggggggggg01", "Android", "Happ/3.4.2"},     // table ok, classic hellos older than the window
		{"alice", "aaaaaaaaaa02", "Windows", "mihomo/1.19.33"}, // table ok, a second device of alice
	} {
		if c.vless[p.name] == "" {
			c.person(p.name, g)
		}
		c.fetchWith(p.name, p.hwid, p.os, p.ua)
	}
	return c, in.Id
}

func TestOutdatedDevicesCarryWhatTheNodesSaw(t *testing.T) {
	c, inb := actualEnv(t)
	c.hello(inb, "alice", "Happ/3.4.2", 3*time.Hour, 20, 0, 0)
	c.hello(inb, "alice", "mihomo/1.19.33", 2*time.Hour, 0, 5, 0) // the table trusts it, the node saw classic hellos: worse
	c.hello(inb, "bob", "Happ/2.7.1", 5*time.Hour, 25, 0, 0)
	c.hello(inb, "bob", "Happ/2.7.1", 30*time.Hour, 16, 0, 1)     // another hour: the counts add up
	c.hello(inb, "carol", "SFA/1.14.3", time.Hour, 0, 12, 1)      // the table and the node agree
	c.hello(inb, "dave", "Shadowrocket/2086", time.Hour, 3, 2, 0) // both kinds
	c.hello(inb, "frank", "Happ/3.4.2", time.Hour, 0, 0, 4)
	c.hello(inb, "grace", "Happ/3.4.2", 9*24*time.Hour, 0, 9, 0) // older than the 168 hours: not counted

	all := c.list(&adminv1.ListOutdatedDevicesRequest{})
	byUser := map[string]*adminv1.OutdatedDevice{}
	for _, d := range all.Devices {
		byUser[d.UserName] = d
	}
	// the default list is what needs a look: bob, carol, dave, erin
	if got := namesOf(all); len(got) != 4 {
		t.Fatalf("default list = %v", got)
	}
	if d := byUser["bob"]; d.Actual != actPQ || d.HelloPq != 41 || d.HelloClassic != 0 || d.HelloUnknown != 1 || d.TableMismatch != "better" {
		t.Errorf("bob (below minimum, post-quantum in fact) = %+v", d)
	}
	if d := byUser["carol"]; d.Actual != actClassic || d.HelloClassic != 12 || d.TableMismatch != "" {
		t.Errorf("carol (unsupported, classic in fact) = %+v", d)
	}
	if d := byUser["dave"]; d.Actual != actMixed || d.HelloPq != 3 || d.HelloClassic != 2 || d.TableMismatch != "" {
		t.Errorf("dave (unknown, both kinds) = %+v", d)
	}
	if d := byUser["erin"]; d.Actual != actNone || d.HelloPq+d.HelloClassic+d.HelloUnknown != 0 || d.TableMismatch != "" {
		t.Errorf("erin (never measured) = %+v", d)
	}

	// over all devices, whatever the filters: erin, frank (unreadable only), grace (outside the window) are none; alice's Happ and bob are pq
	if n, p := actualOf(all, actNone); n != 3 || p != 3 {
		t.Errorf("none: %d devices, %d people", n, p)
	}
	if n, p := actualOf(all, actPQ); n != 2 || p != 2 {
		t.Errorf("pq: %d devices, %d people", n, p)
	}
	if n, p := actualOf(all, actClassic); n != 2 || p != 2 { // alice's Mihomo, carol
		t.Errorf("classic: %d devices, %d people", n, p)
	}
	if n, p := actualOf(all, actMixed); n != 1 || p != 1 {
		t.Errorf("mixed: %d devices, %d people", n, p)
	}
	if len(all.ActualCounts) != 4 || all.ActualCounts[0].Actual != actNone || all.ActualCounts[3].Actual != actMixed {
		t.Errorf("classes in order: %v", all.ActualCounts)
	}
	if all.MismatchWorse != 1 || all.MismatchBetter != 1 || all.HelloWindowHours != 168 {
		t.Errorf("worse %d, better %d, window %d h", all.MismatchWorse, all.MismatchBetter, all.HelloWindowHours)
	}
	if !slices.Equal(all.HelloKexNodesWithout, []string{"nl1"}) {
		t.Errorf("nodes that do not count = %v", all.HelloKexNodesWithout)
	}

	// the class asked for takes in every state, ok included: alice's Mihomo is the table's mistake; worst state first
	classic := c.list(&adminv1.ListOutdatedDevicesRequest{Actual: actClassic})
	if got := namesOf(classic); !slices.Equal(got, []string{"carol:UNSUPPORTED", "alice:OK"}) || classic.Total != 2 {
		t.Fatalf("classic devices = %v", got)
	}
	if d := classic.Devices[1]; d.TableMismatch != "worse" || d.Client != "mihomo/1.19.33" || d.HelloClassic != 5 {
		t.Errorf("alice's Mihomo = %+v", d)
	}
	if n, _ := actualOf(classic, actPQ); n != 2 { // the summary does not follow the filter
		t.Errorf("summary follows the filter: %v", classic.ActualCounts)
	}
	// together with a state
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{Actual: actPQ, State: adminv1.ClientCompatState_CLIENT_COMPAT_STATE_BELOW_MINIMUM}); r.Total != 1 || r.Devices[0].UserName != "bob" {
		t.Errorf("pq and below minimum = %v", namesOf(r))
	}
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{Actual: actPQ, State: adminv1.ClientCompatState_CLIENT_COMPAT_STATE_UNKNOWN}); r.Total != 0 {
		t.Errorf("pq and unknown = %v", namesOf(r))
	}
	// the devices nobody measured
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{Actual: actNone}); r.Total != 3 {
		t.Errorf("none = %v", namesOf(r))
	}
	// a page of the filtered list
	if p2 := c.list(&adminv1.ListOutdatedDevicesRequest{Actual: actClassic, PageSize: 1, Offset: 1}); len(p2.Devices) != 1 || p2.Total != 2 || p2.Devices[0].UserName != "alice" {
		t.Errorf("page 2 of classic = %v", namesOf(p2))
	}
	_, err := c.s.ListOutdatedDevices(c.ctx, req(&adminv1.ListOutdatedDevicesRequest{Actual: 99}))
	wantCode(t, err, connect.CodeInvalidArgument)

	// the agent of nl1 is updated: no node is left that does not count
	c.sql(`UPDATE node SET agent_caps = 'vless-hello-kex/1 links/3' WHERE id = 'nod_nl1'`)
	if r := c.list(&adminv1.ListOutdatedDevicesRequest{}); len(r.HelloKexNodesWithout) != 0 {
		t.Errorf("nodes that do not count after the update = %v", r.HelloKexNodesWithout)
	}
}

// With no hello counted anywhere every device is none and nothing disagrees; a REALITY server on a node that is not live does not
// make its node "not counting".
func TestOutdatedDevicesWithoutAnyMeasurement(t *testing.T) {
	c, _ := actualEnv(t)
	c.sql(`UPDATE node SET state = 'pending' WHERE id = 'nod_nl1'`)
	r := c.list(&adminv1.ListOutdatedDevicesRequest{})
	if n, _ := actualOf(r, actNone); n != 8 {
		t.Errorf("none = %d, want all 8 devices", n)
	}
	for _, a := range []adminv1.ClientCompatActual{actPQ, actClassic, actMixed} {
		if n, _ := actualOf(r, a); n != 0 {
			t.Errorf("class %v has %d devices", a, n)
		}
	}
	if r.MismatchWorse != 0 || r.MismatchBetter != 0 || len(r.HelloKexNodesWithout) != 0 {
		t.Errorf("mismatches %d/%d, nodes %v", r.MismatchWorse, r.MismatchBetter, r.HelloKexNodesWithout)
	}
	for _, d := range r.Devices {
		if d.Actual != actNone || d.TableMismatch != "" {
			t.Errorf("%s: %v %q", d.UserName, d.Actual, d.TableMismatch)
		}
	}
}

func TestTableMismatchAndHelloClass(t *testing.T) {
	for _, tc := range []struct {
		state string
		a     adminv1.ClientCompatActual
		want  string
	}{
		{ClientOK, actClassic, "worse"}, {ClientOK, actMixed, "worse"}, {ClientOK, actPQ, ""}, {ClientOK, actNone, ""},
		{ClientBelow, actPQ, "better"}, {ClientUnsupported, actPQ, "better"}, {ClientUnknown, actPQ, "better"},
		{ClientBelow, actClassic, ""}, {ClientUnknown, actMixed, ""}, {ClientUnknown, actNone, ""},
	} {
		if got := tableMismatch(tc.state, tc.a); got != tc.want {
			t.Errorf("tableMismatch(%s, %v) = %q, want %q", tc.state, tc.a, got, tc.want)
		}
	}
	for _, tc := range []struct {
		h    store.HelloKexSums
		want adminv1.ClientCompatActual
	}{{store.HelloKexSums{}, actNone}, {store.HelloKexSums{Unknown: 9}, actNone}, {store.HelloKexSums{PQ: 1}, actPQ}, {store.HelloKexSums{Classic: 1, Unknown: 1}, actClassic}, {store.HelloKexSums{PQ: 1, Classic: 1}, actMixed}} {
		if got := helloActual(tc.h); got != tc.want {
			t.Errorf("helloActual(%+v) = %v, want %v", tc.h, got, tc.want)
		}
	}
}
