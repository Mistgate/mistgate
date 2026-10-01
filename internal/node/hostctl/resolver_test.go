package hostctl

import (
	"strings"
	"testing"
)

func TestCleanResolvers(t *testing.T) {
	tests := []struct {
		name       string
		in         []string
		resolvConf bool
		want       string
		wantErr    bool
	}{
		{"bare ips", []string{"1.1.1.1", "8.8.8.8"}, true, "1.1.1.1,8.8.8.8", false},
		{"port 53 is the default", []string{"1.1.1.1:53", "[2606:4700:4700::1111]:53"}, true, "1.1.1.1,2606:4700:4700::1111", false},
		{"resolv.conf cannot carry another port", []string{"10.0.0.1:5353", "9.9.9.9"}, true, "9.9.9.9", false},
		{"resolved can", []string{"10.0.0.1:5353", "[2001:db8::1]:5353", "9.9.9.9"}, false, "10.0.0.1:5353,[2001:db8::1]:5353,9.9.9.9", false},
		{"names are refused", []string{"dns.example.com", "1.1.1.1"}, true, "1.1.1.1", false},
		{"duplicates and blanks", []string{"1.1.1.1", " ", "1.1.1.1", "1.1.1.1:53"}, true, "1.1.1.1", false},
		{"at most three", []string{"1.1.1.1", "8.8.8.8", "9.9.9.9", "77.88.8.8"}, true, "1.1.1.1,8.8.8.8,9.9.9.9", false},
		{"unspecified and multicast", []string{"0.0.0.0", "224.0.0.1"}, true, "", true},
		{"mapped ipv4", []string{"::ffff:1.1.1.1"}, true, "1.1.1.1", false},
		{"nothing", nil, true, "", true},
		{"only names", []string{"dns.example.com"}, false, "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanResolvers(tc.in, tc.resolvConf)
			if (err != nil) != tc.wantErr || strings.Join(got, ",") != tc.want {
				t.Fatalf("got %v, %v; want %q (err %v)", got, err, tc.want, tc.wantErr)
			}
		})
	}
}

func TestParseNameserversAndResolvectl(t *testing.T) {
	conf := "# comment\nsearch example.com\nnameserver 10.0.0.1\nnameserver 2001:db8::1\noptions edns0\nnameserver\n"
	if got := strings.Join(parseNameservers(conf), ","); got != "10.0.0.1,2001:db8::1" {
		t.Errorf("nameservers = %s", got)
	}
	out := "Global: 1.1.1.1 8.8.8.8\nLink 2 (eth0): 203.0.113.1 fe80::1%eth0\nLink 3 (docker0):\n"
	if got := strings.Join(parseResolvectlDNS(out), ","); got != "1.1.1.1,8.8.8.8,203.0.113.1,fe80::1" {
		t.Errorf("resolvectl = %s", got)
	}
}

func TestRenderedResolverConfigs(t *testing.T) {
	if got := renderResolvConf([]string{"1.1.1.1", "8.8.8.8"}); got != "# Managed by mistgate-node.\nnameserver 1.1.1.1\nnameserver 8.8.8.8\n" {
		t.Errorf("resolv.conf = %q", got)
	}
	d := renderResolvedDropIn([]string{"77.88.8.8", "77.88.8.1"})
	for _, want := range []string{"[Resolve]\n", "DNS=77.88.8.8 77.88.8.1\n", "FallbackDNS=\n", "Domains=~.\n"} {
		if !strings.Contains(d, want) {
			t.Errorf("drop-in lacks %q:\n%s", want, d)
		}
	}
	if !strings.HasPrefix(d, resolverMarker) || !strings.HasPrefix(renderResolvConf([]string{"1.1.1.1"}), resolverMarker) {
		t.Error("both files must carry the marker Cleanup looks for")
	}
}

func TestBaselineBodiesAreExportedUnchanged(t *testing.T) {
	if SysctlFileBody != sysctlFileBody || JournaldFileBody != journaldFileBody {
		t.Fatal("the doctor would compare against something ApplyBaseline does not write")
	}
	if !strings.Contains(JournaldFileBody, "SystemMaxUse=200M") || JournalCapMB != 200 {
		t.Errorf("journal cap: %d in %q", JournalCapMB, JournaldFileBody)
	}
}
