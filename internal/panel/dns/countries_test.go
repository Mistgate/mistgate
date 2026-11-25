package dns

import (
	"context"
	"slices"
	"testing"
)

// The five country built-ins (00158): Cloudflare through the tunnel, the country's sites direct through Google, and the main and the
// split resolver share no address (the direct rule of Xray JSON picks resolvers by address).
func TestCountryBuiltins(t *testing.T) {
	e := newEnv(t)
	rows, err := e.st.DNS().List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	wantSuffixes := map[string][]string{
		"dns_builtin_tr_split": {".tr"},
		"dns_builtin_kz_split": {".kz", ".xn--80ao21a"},
		"dns_builtin_by_split": {".by", ".xn--90ais"},
		"dns_builtin_uz_split": {".uz"},
		"dns_builtin_ir_split": nil,
	}
	seen := 0
	for _, row := range rows {
		want, ok := wantSuffixes[row.ID]
		if !ok {
			continue
		}
		seen++
		p, err := fromRow(row)
		if err != nil {
			t.Fatal(err)
		}
		if !row.Builtin || !p.SplitDirect || len(p.Servers) != 1 || p.Servers[0].Variant != "cloudflare/standard" || len(p.Split) != 1 || p.Split[0].Servers[0].Variant != "google/standard" {
			t.Errorf("%s = %+v", row.ID, p)
		}
		if !slices.Equal(p.Split[0].Suffixes, want) || (want == nil) != slices.Equal(p.Split[0].Geo, []string{"ir"}) {
			t.Errorf("%s: suffixes %v geo %v", row.ID, p.Split[0].Suffixes, p.Split[0].Geo)
		}
		x, err := p.XrayDNS()
		if err != nil {
			t.Fatalf("%s: %v", row.ID, err)
		}
		if !slices.Contains(x.DirectIPs, "8.8.8.8") || slices.Contains(x.DirectIPs, "1.1.1.1") || slices.Contains(x.DirectIPs, "1.0.0.1") {
			t.Errorf("%s: direct resolvers %v", row.ID, x.DirectIPs)
		}
		if _, _, _, _, err := Clean(p.Name, p.Description, p.Servers, p.Split); err != nil {
			t.Errorf("%s does not validate: %v", row.ID, err)
		}
	}
	if seen != 5 {
		t.Errorf("%d of the 5 country presets", seen)
	}
	// the Cyrillic endings of the two presets are the punycode ones the migration stores
	for in, want := range map[string]string{".қаз": ".xn--80ao21a", ".бел": ".xn--90ais"} {
		if got, err := NormalizeSuffix(in); err != nil || got != want {
			t.Errorf("NormalizeSuffix(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
