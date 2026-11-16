package subsettings

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"google.golang.org/protobuf/proto"

	adminv1 "github.com/mistgate/mistgate/gen/mistgate/admin/v1"
	"github.com/mistgate/mistgate/internal/panel/subhdr"
)

// Limits of the owner's own response headers (SubscriptionSettings.response_headers). The total is the bytes of all names and
// values as they are sent, with the separators: what one answer can carry at most if every entry applies to it. A reverse proxy in
// front of the panel buffers a response's headers in 4 or 8 KiB, and the panel's own headers take about 1 KiB of that.
const (
	MaxResponseHeaders = 20
	MaxHeaderName      = 64
	MaxHeaderValue     = 1000
	MaxHeadersBytes    = 4096
	maxHeaderGroups    = 100
)

// headerFormats are the answer formats an own header can be limited to: what the subscription endpoint answers an app with.
var headerFormats = []adminv1.SubFormat{
	adminv1.SubFormat_SUB_FORMAT_BASE64_URIS, adminv1.SubFormat_SUB_FORMAT_MIHOMO_YAML,
	adminv1.SubFormat_SUB_FORMAT_XRAY_JSON, adminv1.SubFormat_SUB_FORMAT_SINGBOX_JSON,
}

// tchar is the character set of an HTTP token (RFC 9110 section 5.6.2).
func tchar(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0
}

func idChars(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// SentValue is the value as it goes out: the text itself, or "base64:" and its base64 (the way the panel sends the title).
func SentValue(value string, b64 bool) string {
	if !b64 {
		return value
	}
	return "base64:" + base64.StdEncoding.EncodeToString([]byte(value))
}

// validateResponseHeaders is the check of a save: it is the trust boundary, the document comes from the admin API and from MCP.
func validateResponseHeaders(list []*adminv1.ResponseHeader) error {
	if len(list) > MaxResponseHeaders {
		return invalid("response_headers: at most %d", MaxResponseHeaders)
	}
	total := 0
	for i, e := range list {
		n, err := checkHeader(fmt.Sprintf("response_headers[%d]", i), e)
		if err != nil {
			return err
		}
		total += n
	}
	if total > MaxHeadersBytes {
		return invalid("response_headers: %d bytes in all as sent, at most %d", total, MaxHeadersBytes)
	}
	return nil
}

// CheckResponseHeaders normalizes a copy of list and checks it as a save does (for the preview of an unsaved list); the returned
// list is the normalized copy.
func CheckResponseHeaders(list []*adminv1.ResponseHeader) ([]*adminv1.ResponseHeader, error) {
	out := make([]*adminv1.ResponseHeader, len(list))
	for i, e := range list {
		if e != nil {
			out[i] = proto.Clone(e).(*adminv1.ResponseHeader)
		}
	}
	normalizeResponseHeaders(out)
	return out, validateResponseHeaders(out)
}

// keepValidHeaders is what a stored document is read with: an entry a save would refuse now (a tampered document, or a name the
// panel has since started to set itself) is left out, within the limits of the list, so that a fetch is never served a header
// the panel would not accept, and never fails over one.
func keepValidHeaders(list []*adminv1.ResponseHeader) []*adminv1.ResponseHeader {
	var out []*adminv1.ResponseHeader
	total := 0
	for i, e := range list {
		n, err := checkHeader(fmt.Sprintf("response_headers[%d]", i), e)
		if err != nil || len(out) >= MaxResponseHeaders || total+n > MaxHeadersBytes {
			slog.Warn("subscription settings: an own response header is not served", "code", "subscription_response_header_dropped", "index", i)
			continue
		}
		out = append(out, e)
		total += n
	}
	return out
}

// checkHeader checks one entry; w names it in the error. It returns the bytes the entry takes in an answer.
func checkHeader(w string, e *adminv1.ResponseHeader) (int, error) {
	if e == nil {
		return 0, invalid("%s: missing", w)
	}
	if e.Name == "" || len(e.Name) > MaxHeaderName {
		return 0, invalid("%s.name: 1 to %d characters", w, MaxHeaderName)
	}
	for j := 0; j < len(e.Name); j++ {
		if !tchar(e.Name[j]) {
			return 0, invalid("%s.name: only letters, digits and !#$%%&'*+-.^_`|~ are allowed (an HTTP header name)", w)
		}
	}
	if subhdr.Reserved(e.Name) {
		return 0, invalid("%s.name: %q is a header the panel or the web server sets itself", w, e.Name)
	}
	if e.Value == "" || len(e.Value) > MaxHeaderValue || !utf8.ValidString(e.Value) {
		return 0, invalid("%s.value: 1 to %d bytes of text", w, MaxHeaderValue)
	}
	if strings.ContainsFunc(e.Value, unicode.IsControl) {
		return 0, invalid("%s.value: control characters (a line break, a tab) are not allowed", w)
	}
	if !e.Base64 && strings.ContainsFunc(e.Value, func(r rune) bool { return r > unicode.MaxASCII }) {
		return 0, invalid("%s.value: only ASCII is sent as is; turn base64 on for other text", w)
	}
	if strings.TrimSpace(e.Value) != e.Value {
		return 0, invalid("%s.value: no space at the start or the end", w)
	}
	seen := map[adminv1.SubFormat]bool{}
	for _, f := range e.Formats {
		if !slices.Contains(headerFormats, f) {
			return 0, invalid("%s.formats: a link list, Mihomo YAML, Xray JSON or sing-box JSON", w)
		}
		if seen[f] {
			return 0, invalid("%s.formats: repeated format", w)
		}
		seen[f] = true
	}
	if !e.OnlyGroups && len(e.GroupIds) > 0 {
		return 0, invalid("%s.group_ids: given without only_groups", w)
	}
	if len(e.GroupIds) > maxHeaderGroups {
		return 0, invalid("%s.group_ids: at most %d", w, maxHeaderGroups)
	}
	ids := map[string]bool{}
	for _, id := range e.GroupIds {
		if !idChars(id) || ids[id] {
			return 0, invalid("%s.group_ids: a repeated or malformed group id", w)
		}
		ids[id] = true
	}
	return len(e.Name) + len(SentValue(e.Value, e.Base64)) + 4, nil // ": " and the line end
}

// normalizeResponseHeaders trims the names and values and drops a format list that names every format (it means all).
func normalizeResponseHeaders(list []*adminv1.ResponseHeader) {
	for _, e := range list {
		if e == nil {
			continue
		}
		e.Name, e.Value = strings.TrimSpace(e.Name), strings.TrimSpace(e.Value)
		if len(e.Formats) == len(headerFormats) {
			all := true
			for _, f := range headerFormats {
				all = all && slices.Contains(e.Formats, f)
			}
			if all {
				e.Formats = nil
			}
		}
	}
}

// SentHeader is one own header as an answer carries it.
type SentHeader struct {
	Name, Value string
	Base64      bool
}

// ResponseHeadersFor are the own headers the answer of one format carries for a person of one group, in the order of the
// list; when two entries of one name apply, the first wins. The caller sends them with a successful subscription answer only.
func ResponseHeadersFor(s *adminv1.SubscriptionSettings, groupID string, format adminv1.SubFormat) []SentHeader {
	list := s.GetResponseHeaders()
	if len(list) == 0 {
		return nil
	}
	var out []SentHeader
	for _, e := range list {
		if len(e.GetFormats()) > 0 && !slices.Contains(e.Formats, format) {
			continue
		}
		if e.GetOnlyGroups() && !slices.Contains(e.GroupIds, groupID) {
			continue
		}
		if slices.ContainsFunc(out, func(h SentHeader) bool { return strings.EqualFold(h.Name, e.Name) }) {
			continue
		}
		out = append(out, SentHeader{Name: e.Name, Value: SentValue(e.Value, e.Base64), Base64: e.Base64})
	}
	return out
}
