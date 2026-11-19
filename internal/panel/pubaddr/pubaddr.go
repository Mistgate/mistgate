// Package pubaddr holds the panel's public address, which can change while the panel runs: readers take the current value
// on every use, and subscribers are told when it changes.
package pubaddr

import (
	"sync"
	"sync/atomic"
)

// Holder is the current public URL ("https://panel.example.com"; "" = none). The zero value is not usable; call New.
type Holder struct {
	cur  atomic.Pointer[string]
	mu   sync.Mutex // serialises Set and Subscribe so subscribers see changes in order
	subs []func(string)
}

// New is a Holder that starts at publicURL.
func New(publicURL string) *Holder {
	h := &Holder{}
	h.cur.Store(&publicURL)
	return h
}

// PublicURL is the current public URL.
func (h *Holder) PublicURL() string { return *h.cur.Load() }

// Set changes the public URL. When it differs from the current one, every subscriber is called with it, synchronously, in
// the order they subscribed.
func (h *Holder) Set(publicURL string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if *h.cur.Load() == publicURL {
		return
	}
	h.cur.Store(&publicURL)
	for _, fn := range h.subs {
		fn(publicURL)
	}
}

// Subscribe calls fn on every later change of the public URL. fn must not call Set or Subscribe.
func (h *Holder) Subscribe(fn func(publicURL string)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.subs = append(h.subs, fn)
}
