package health

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/mistgate/mistgate/internal/panel/store"
)

func TestCountryOperatorTable(t *testing.T) {
	operators := countryOperators["RU"]
	wantNames := []string{"MTS", "Beeline", "MegaFon", "Rostelecom", "T2", "ER-Telecom"}
	wantCounts := []int{96, 35, 40, 121, 9, 132}
	wantASNs := map[string][]uint32{
		"MTS":        {8359},
		"Beeline":    {3216, 8402, 16345},
		"MegaFon":    {12714, 25159, 31133, 31163, 31195, 31205, 31208, 31213, 31224, 31261},
		"Rostelecom": {12389},
		"T2":         {15378, 41330, 48190},
		"ER-Telecom": {9049, 25446, 41754, 41843, 42116, 43478, 51570},
	}
	if len(operators) != len(wantNames) {
		t.Fatalf("operators = %d, want %d", len(operators), len(wantNames))
	}
	seen := map[uint32]string{}
	for i, operator := range operators {
		if operator.Name != wantNames[i] {
			t.Errorf("operator %d = %q, want %q", i, operator.Name, wantNames[i])
		}
		if len(operator.ASNs) != wantCounts[i] {
			t.Errorf("%s ASN count = %d, want %d", operator.Name, len(operator.ASNs), wantCounts[i])
		}
		for _, asn := range operator.ASNs {
			if previous, ok := seen[asn]; ok {
				t.Errorf("AS%d belongs to both %s and %s", asn, previous, operator.Name)
			}
			seen[asn] = operator.Name
		}
		for _, asn := range wantASNs[operator.Name] {
			if !slices.Contains(operator.ASNs, asn) {
				t.Errorf("%s is missing AS%d", operator.Name, asn)
			}
		}
	}
	if _, ok := countryOperators["DE"]; ok {
		t.Fatal("DE has an operator table; the plan only names RU operators")
	}
}

func TestCountryWantsNeverAskedThenOldest(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	row := func(country string, at time.Time, asns ...uint32) store.CountryCheck {
		detail := countryDetail{}
		for _, asn := range asns {
			detail.Probes = append(detail.Probes, countryProbeDetail{ASN: asn})
		}
		b, err := json.Marshal(detail)
		if err != nil {
			t.Fatal(err)
		}
		return store.CountryCheck{Country: country, At: at, Detail: string(b)}
	}
	rows := []store.CountryCheck{
		row("RU", base.Add(20*time.Minute), 8359),
		row("RU", base.Add(15*time.Minute), 3216),
		row("RU", base.Add(10*time.Minute), 12389),
		row("DE", base.Add(time.Minute), 31133), // other-country probes do not count
	}
	wants := countryWants("RU", rows)
	var got []string
	for _, want := range wants {
		got = append(got, want.Name)
	}
	want := []string{"MegaFon", "T2", "ER-Telecom", "Rostelecom", "Beeline", "MTS"}
	if !slices.Equal(got, want) {
		t.Fatalf("want order = %v, want %v", got, want)
	}
	if got := countryWants("DE", rows); len(got) != 0 {
		t.Fatalf("DE wants = %+v, want none", got)
	}
}

func TestCountryRoundWantsStoreErrorUsesTableOrder(t *testing.T) {
	c := newCountryEnv(t)
	ctx, cancel := context.WithCancel(c.ctx)
	cancel()
	wants := c.s.countryRoundWants(ctx, "RU")
	var got []string
	for _, want := range wants {
		got = append(got, want.Name)
	}
	if want := []string{"MTS", "Beeline", "MegaFon", "Rostelecom", "T2", "ER-Telecom"}; !slices.Equal(got, want) {
		t.Fatalf("want order on store error = %v, want %v", got, want)
	}
}
