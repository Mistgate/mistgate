// Mistgate patch: the key-share rule of REALITY before 2026-11. This file is not part of upstream REALITY; see MISTGATE.md.
//
// This Source Code Form is subject to the terms of the Mozilla Public License, v. 2.0. If a copy of the MPL was not
// distributed with this file, You can obtain one at https://mozilla.org/MPL/2.0/.

package reality

import (
	"crypto/mlkem"
	"sync/atomic"
)

// rejectClassical is the way to turn the compatibility off. The zero value is the default: on.
var rejectClassical atomic.Pointer[func(*Config) bool]

// SetRejectClassical installs the strict policy: f(config) true means that a ClientHello the current upstream rule turns away
// (no X25519MLKEM768 share before the X25519 share, a duplicate share) is turned away for this inbound too, as upstream does;
// false means the older rule applies. f == nil restores the default, where the older rule always applies. f is called once per
// hello that fails the upstream rule, from the connection's goroutine, and must be cheap and safe for concurrent use.
func SetRejectClassical(f func(config *Config) bool) {
	if f == nil {
		rejectClassical.Store(nil)
		return
	}
	rejectClassical.Store(&f)
}

// legacyPeerPub is the client's key share by the rule of xtls/reality 9234c772 (the library of Xray 26.3.27), which tls.go
// falls back on when the upstream rule found no usable X25519MLKEM768 share: the first 32-byte X25519 share anywhere in the
// list; if there is none, the X25519 half of the first X25519MLKEM768 share of the right size. nil when the strict policy
// says no, or when the hello has neither.
func legacyPeerPub(config *Config, shares []keyShare) []byte {
	if f := rejectClassical.Load(); f != nil && (*f)(config) {
		return nil
	}
	for _, share := range shares {
		if share.group == X25519 && len(share.data) == 32 {
			return share.data
		}
	}
	for _, share := range shares {
		if share.group == X25519MLKEM768 && len(share.data) == mlkem.EncapsulationKeySize768+32 {
			return share.data[mlkem.EncapsulationKeySize768:]
		}
	}
	return nil
}
