package protocols

import "strings"

// NormalizePin returns the SHA-256 certificate pin a node reported in the form that may be put into a
// subscription: exactly 64 lowercase hex digits (colons, as in openssl output, are tolerated on the way in).
// Anything else is not a pin. A node is trusted to serve its own traffic, not to write into the subscription
// of every user of the inbound, so this is checked where the pin is stored and again where it is rendered.
func NormalizePin(s string) (string, bool) {
	s = strings.ToLower(strings.ReplaceAll(s, ":", ""))
	if len(s) != 64 {
		return "", false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return "", false
		}
	}
	return s, true
}
