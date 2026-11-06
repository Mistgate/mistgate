package release

import "testing"

func TestChannelOf(t *testing.T) {
	tests := []struct {
		tag     string
		channel Channel
		ok      bool
	}{
		{"v0.0.0", ChannelStable, true},
		{"v1.2.3", ChannelStable, true},
		{"v1.2.3-testing.1", ChannelTesting, true},
		{"v1.2.3-testing.20", ChannelTesting, true},
		{"v1.2.3-dev.1", ChannelDev, true},
		{"v1.2.3-dev.20", ChannelDev, true},
		{"1.2.3", "", false},
		{"v01.2.3", "", false},
		{"v1.02.3", "", false},
		{"v1.2.03", "", false},
		{"v1.2", "", false},
		{"v1.2.3-rc.1", "", false},
		{"v1.2.3-beta", "", false},
		{"v1.2.3-testing.0", "", false},
		{"v1.2.3-dev.0", "", false},
		{"v1.2.3-testing.01", "", false},
		{"v1.2.3-dev.01", "", false},
		{"v1.2.3+build.1", "", false},
		{"v1.2.3-dev.1+build.1", "", false},
		{"v1.2.3-testing", "", false},
		{"v1.2.3-dev", "", false},
	}
	for _, tc := range tests {
		t.Run(tc.tag, func(t *testing.T) {
			got, ok := ChannelOf(tc.tag)
			if got != tc.channel || ok != tc.ok {
				t.Fatalf("ChannelOf(%q) = %q, %v; want %q, %v", tc.tag, got, ok, tc.channel, tc.ok)
			}
			if ValidChannelTag(tc.tag) != tc.ok {
				t.Fatalf("ValidChannelTag(%q) = %v; want %v", tc.tag, ValidChannelTag(tc.tag), tc.ok)
			}
		})
	}
}

func TestChannelAllowsAndLatestTagForChannel(t *testing.T) {
	tests := []struct {
		channel Channel
		tags    []string
		want    string
	}{
		{ChannelStable, []string{"v1.2.3-dev.5", "v1.2.3-testing.2", "v1.2.3", "v1.2.2"}, "v1.2.3"},
		{ChannelTesting, []string{"v1.2.3-dev.5", "v1.2.3-testing.2", "v1.2.3", "v1.2.4-testing.1"}, "v1.2.4-testing.1"},
		{ChannelDev, []string{"v1.2.3-dev.5", "v1.2.3-testing.2", "v1.2.3", "v1.2.4-testing.1", "v1.2.4-dev.1"}, "v1.2.4-testing.1"},
		{ChannelDev, []string{"v1.2.3-dev.9", "v1.2.3-testing.1", "v1.2.3"}, "v1.2.3"},
		{ChannelStable, []string{"v1.2.3-dev.1", "v1.2.3-testing.1", "not-a-tag"}, ""},
		{Channel("unknown"), []string{"v1.2.3"}, ""},
	}
	for _, tc := range tests {
		got, ok := LatestTagForChannel(tc.tags, tc.channel)
		if wantOK := tc.want != ""; got != tc.want || ok != wantOK {
			t.Errorf("LatestTagForChannel(%v, %q) = %q, %v; want %q, %v", tc.tags, tc.channel, got, ok, tc.want, wantOK)
		}
	}
	if !ChannelDev.Allows(ChannelTesting) || !ChannelTesting.Allows(ChannelStable) || ChannelStable.Allows(ChannelTesting) || ChannelTesting.Allows(ChannelDev) {
		t.Fatal("channel ordering must be stable < testing < dev")
	}
}
