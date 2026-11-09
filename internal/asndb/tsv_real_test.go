package asndb

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"
	"time"
)

// What the real iptoasn files hold and the fixtures did not: "None" and "Unknown" for a country, and IPv6 /64s that two
// ASNs share (a transit link's /127 inside another network's /64, an unrouted hole).

func TestAppendTSVTakesNoneAndUnknownCountries(t *testing.T) {
	data := "192.0.2.0\t192.0.2.255\t0\tNone\tNot routed\n198.51.100.0\t198.51.100.255\t64500\tUnknown\tExample Telecom\n"
	var table Table
	if err := AppendTSV(strings.NewReader(data), FamilyIPv4, &table); err != nil {
		t.Fatal(err)
	}
	if len(table.ASNs) != 1 || table.ASNs[0].Country != "" {
		t.Fatalf("operators %+v, want AS64500 with no country", table.ASNs)
	}
}

func TestSharedIPv6Slash64(t *testing.T) {
	v4 := "192.0.2.0\t192.0.2.255\t64500\tDE\tExample Telecom\n"
	cases := []struct {
		name string
		v6   string
		want map[string]uint32
	}{
		{
			// a /127 of AS64501 at the start of a /64 that AS64500 holds otherwise: AS64500 keeps the /64
			name: "the larger share wins",
			v6: "2001:db8::\t2001:db8:0:ffff:ffff:ffff:ffff:ffff\t64500\tDE\tExample Telecom\n" +
				"2001:db8:1::\t2001:db8:1::1\t64501\tUS\tExample Transit\n" +
				"2001:db8:1::2\t2001:db8:2:ffff:ffff:ffff:ffff:ffff\t64500\tDE\tExample Telecom\n",
			want: map[string]uint32{"2001:db8::1": 64500, "2001:db8:1::": 64500, "2001:db8:1::9": 64500, "2001:db8:2::1": 64500},
		},
		{
			// the first line holds a few addresses of the /64, the next one the rest and the following /64s
			name: "a range that runs on takes the /64 it starts in",
			v6: "2001:db8:5::\t2001:db8:5::ff\t64501\tUS\tExample Transit\n" +
				"2001:db8:5::100\t2001:db8:6:ffff:ffff:ffff:ffff:ffff\t64502\tNL\tExample Network\n",
			want: map[string]uint32{"2001:db8:5::1": 64502, "2001:db8:6::1": 64502},
		},
		{
			// unrouted space never wins, however much of the /64 it covers
			name: "ASN 0 never wins",
			v6: "2001:db8:7::\t2001:db8:7::f\t64503\tGB\tExample Hoster\n" +
				"2001:db8:7::10\t2001:db8:8:ffff:ffff:ffff:ffff:ffff\t0\tNone\tNot routed\n",
			want: map[string]uint32{"2001:db8:7::1": 64503, "2001:db8:7::ffff": 64503, "2001:db8:8::1": 0},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			table := Table{Built: time.Unix(1_800_000_000, 0)}
			if err := AppendTSV(strings.NewReader(v4), FamilyIPv4, &table); err != nil {
				t.Fatal(err)
			}
			if err := AppendTSV(strings.NewReader(c.v6), FamilyIPv6, &table); err != nil {
				t.Fatal(err)
			}
			var buf bytes.Buffer
			if err := Write(&buf, table); err != nil { // an operator whose only range lost its /64 is left out, not an error
				t.Fatal(err)
			}
			db, err := Parse(&buf)
			if err != nil {
				t.Fatal(err)
			}
			for addr, want := range c.want {
				if got := db.Lookup(netip.MustParseAddr(addr)); got != want {
					t.Errorf("%s: AS%d, want AS%d", addr, got, want)
				}
			}
		})
	}
}
