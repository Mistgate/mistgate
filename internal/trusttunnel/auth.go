package trusttunnel

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strings"
)

const (
	proxyAuthorizationPrefix = "Basic "

	// MaxProxyAuthorizationBytes bounds parsing and building Basic credentials.
	MaxProxyAuthorizationBytes = 8 * 1024
)

var (
	// ErrInvalidProxyAuthorization indicates malformed or empty Basic credentials.
	ErrInvalidProxyAuthorization = errors.New("invalid Proxy-Authorization")
	// ErrProxyAuthorizationTooLong indicates the value exceeds its package limit.
	ErrProxyAuthorizationTooLong = errors.New("Proxy-Authorization is too long")
)

// ParseProxyAuthorization parses a Proxy-Authorization value with the exact,
// case-sensitive "Basic " prefix. Passwords may contain colons; the first
// colon separates the username from the password.
func ParseProxyAuthorization(value string) (username, password string, err error) {
	if len(value) > MaxProxyAuthorizationBytes {
		return "", "", ErrProxyAuthorizationTooLong
	}
	if !strings.HasPrefix(value, proxyAuthorizationPrefix) {
		return "", "", ErrInvalidProxyAuthorization
	}

	encoded := value[len(proxyAuthorizationPrefix):]
	if encoded == "" {
		return "", "", ErrInvalidProxyAuthorization
	}
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if !(c >= 'A' && c <= 'Z' ||
			c >= 'a' && c <= 'z' ||
			c >= '0' && c <= '9' ||
			c == '+' || c == '/' || c == '=') {
			return "", "", ErrInvalidProxyAuthorization
		}
	}

	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(encoded)))
	n, decodeErr := base64.StdEncoding.Strict().Decode(decoded, []byte(encoded))
	if decodeErr != nil {
		return "", "", ErrInvalidProxyAuthorization
	}
	decoded = decoded[:n]
	colon := bytes.IndexByte(decoded, ':')
	if colon <= 0 || colon == len(decoded)-1 {
		return "", "", ErrInvalidProxyAuthorization
	}

	return string(decoded[:colon]), string(decoded[colon+1:]), nil
}

// BuildProxyAuthorization builds a complete Proxy-Authorization header value.
// A colon is not allowed in the username because it is the field separator.
func BuildProxyAuthorization(username, password string) (string, error) {
	if username == "" || password == "" || strings.Contains(username, ":") {
		return "", ErrInvalidProxyAuthorization
	}
	credentialBytes := len(username) + 1 + len(password)
	if credentialBytes > base64.StdEncoding.DecodedLen(MaxProxyAuthorizationBytes-len(proxyAuthorizationPrefix)) {
		return "", ErrProxyAuthorizationTooLong
	}
	credentials := make([]byte, 0, credentialBytes)
	credentials = append(credentials, username...)
	credentials = append(credentials, ':')
	credentials = append(credentials, password...)
	value := proxyAuthorizationPrefix + base64.StdEncoding.EncodeToString(credentials)
	if len(value) > MaxProxyAuthorizationBytes {
		return "", ErrProxyAuthorizationTooLong
	}
	return value, nil
}
