package app

import "testing"

func TestProvisionLinkURLIsEdgeOnly(t *testing.T) {
	in := InstanceConfig{PublicURL: "https://panel.example.com", LinkPrefix: "/0123456789abcdef0123/"}
	if got, want := provisionLinkURL(true, in), "wss://panel.example.com/0123456789abcdef0123/"; got != want {
		t.Fatalf("edge link = %q, want %q", got, want)
	}
	if got := provisionLinkURL(false, in); got != "" {
		t.Fatalf("the VPS edition got a link %q: its installs enrol over mTLS", got)
	}
	if got := provisionLinkURL(true, InstanceConfig{PublicURL: "https://panel.example.com"}); got != "" {
		t.Fatalf("an edge panel without a link prefix got %q", got)
	}
}
