package pubaddr

import "testing"

func TestHolder(t *testing.T) {
	h := New("https://panel.example.com")
	var seen []string
	h.Subscribe(func(u string) { seen = append(seen, u) })
	h.Set("https://panel.example.com") // the same: nobody is told
	h.Set("https://sub.example.com")
	h.Set("")
	if h.PublicURL() != "" || len(seen) != 2 || seen[0] != "https://sub.example.com" || seen[1] != "" {
		t.Fatalf("current %q, told %v", h.PublicURL(), seen)
	}
}
