//go:build !js

package clientdebug

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/logscrub"
	"github.com/mistgate/mistgate/internal/panel/store"
)

// The delivery: newline-delimited JSON, one record per line, oldest first (client contract "logs for support"). Unpacked it is
// at most MaxText bytes and MaxLines lines of at most MaxLine bytes each.
const (
	MaxText = 4 << 20
	MaxLine = 8 << 10
	// MaxLines is the most records of a delivery.
	MaxLines = 20000
	// MaxKV is the most attributes a record may carry on the way in (those not in the allow-list are dropped, those kept are cut).
	MaxKV = 16
	// MessageBytes is how much of a message is kept, after cleaning; ValueRunes how much of an attribute value.
	MessageBytes = 1024
	ValueRunes   = 160
	// How far from the panel's clock a record's time may be.
	pastWindow   = 30 * 24 * time.Hour
	futureWindow = 24 * time.Hour

	// rawMessageBytes and rawValueBytes bound what the cleaners see (see cut): about twice what is kept.
	rawMessageBytes = 2 * MessageBytes
	rawValueBytes   = 4 * ValueRunes
	recordOverhead  = 48
)

// KVKeys are the attribute keys that are kept; any other key of a record is dropped (and counted).
var KVKeys = map[string]bool{
	"path": true, "server": true, "protocol": true, "via": true, "engine": true, "phase": true, "state": true, "reason": true,
	"code": true, "err": true, "mode": true, "version": true, "count": true, "ms": true, "bytes": true, "attempt": true,
	"net": true, "event": true, "status": true, "http": true,
}

var (
	// ErrTooLarge: more than MaxText bytes or MaxLines lines, or more than the wire allows.
	ErrTooLarge = errors.New("clientdebug: the delivery is too large")

	sourceShape = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	levels      = map[string]int{"debug": 0, "info": 1, "warn": 2, "error": 3}
)

// Bundle is a delivery as it is kept: the cleaned records and what describes them.
type Bundle struct {
	Lines           []store.ClientDebugLine
	Bytes           int // what the records cost in the store (the node captures count the same way)
	DroppedKV       int // attributes not in the allow-list
	FirstMs, LastMs int64
}

// BadLine is a record that breaks the contract: Line counts from 1, Field names the part, never its value.
type BadLine struct {
	Line  int
	Field string
}

func (e *BadLine) Error() string { return "line " + strconv.Itoa(e.Line) + ": " + e.Field }

type rawLine struct {
	TS  *int64                     `json:"ts"`
	Lvl *string                    `json:"lvl"`
	Src *string                    `json:"src"`
	Msg *string                    `json:"msg"`
	KV  map[string]json.RawMessage `json:"kv"`
}

// Parse reads the unpacked body, which the caller has bounded (an io.LimitReader of MaxText+1 over the unpacked stream) and
// whose read errors it passes on: a *http.MaxBytesError is the wire's limit (ErrTooLarge), any other is a stream that does not
// unpack (a *BadLine of field "gzip"). The first fault ends the reading: a *BadLine, or ErrTooLarge for the size and the line
// count, in which case nothing is returned. Every text of a record is cleaned by c. now is the panel's clock.
func Parse(body io.Reader, now time.Time, c *Cleaner) (Bundle, error) {
	var b Bundle
	counted := &countingReader{r: body}
	sc := bufio.NewScanner(counted)
	sc.Buffer(make([]byte, 0, 4<<10), MaxLine+1)
	oldest, newest := now.Add(-pastWindow).UnixMilli(), now.Add(futureWindow).UnixMilli()
	line := 0
	for sc.Scan() {
		line++
		if line > MaxLines || counted.n > MaxText {
			return Bundle{}, ErrTooLarge
		}
		rec, field := decodeLine(sc.Bytes(), oldest, newest)
		if field != "" {
			return Bundle{}, &BadLine{Line: line, Field: field}
		}
		l, dropped, cost := clean(c, rec)
		b.DroppedKV += dropped
		b.Bytes += cost
		ts := l.AtMs
		if len(b.Lines) == 0 || ts < b.FirstMs {
			b.FirstMs = ts
		}
		if ts > b.LastMs {
			b.LastMs = ts
		}
		b.Lines = append(b.Lines, l)
	}
	if err := sc.Err(); err != nil {
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			return Bundle{}, ErrTooLarge
		case errors.Is(err, bufio.ErrTooLong):
			return Bundle{}, &BadLine{Line: line + 1, Field: "size"}
		}
		return Bundle{}, &BadLine{Line: line + 1, Field: "gzip"}
	}
	if counted.n > MaxText {
		return Bundle{}, ErrTooLarge
	}
	if len(b.Lines) == 0 {
		return Bundle{}, &BadLine{Line: 1, Field: "empty"}
	}
	return b, nil
}

type countingReader struct {
	r io.Reader
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// decodeLine checks one record against the schema; field is the first part that is wrong ("" = fine).
func decodeLine(raw []byte, oldest, newest int64) (rec rawLine, field string) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return rec, "empty"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		return rec, decodeFault(err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return rec, "json" // something after the object
	}
	switch {
	case rec.TS == nil || *rec.TS < oldest || *rec.TS > newest:
		return rec, "ts"
	case rec.Lvl == nil:
		return rec, "lvl"
	case !validLevel(*rec.Lvl):
		return rec, "lvl"
	case rec.Src == nil || !sourceShape.MatchString(*rec.Src):
		return rec, "src"
	case rec.Msg == nil:
		return rec, "msg"
	case len(rec.KV) > MaxKV:
		return rec, "kv"
	}
	for _, v := range rec.KV {
		if len(v) == 0 || v[0] != '"' {
			return rec, "kv"
		}
	}
	return rec, ""
}

func validLevel(s string) bool { _, ok := levels[s]; return ok }

// decodeFault names the part of a record that the decoder refused, without any of its content.
func decodeFault(err error) string {
	var typ *json.UnmarshalTypeError
	switch {
	case errors.As(err, &typ):
		if name, _, _ := strings.Cut(typ.Field, "."); name != "" {
			return name
		}
		return "json"
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return "unknown_field"
	}
	return "json"
}

// clean makes the stored record of a valid one. dropped counts the attributes not in the allow-list; cost is what the record is
// counted as in the bundle's size.
func clean(c *Cleaner, rec rawLine) (l store.ClientDebugLine, dropped, cost int) {
	l = store.ClientDebugLine{AtMs: *rec.TS, Level: levels[*rec.Lvl], Source: c.Secrets(*rec.Src)}
	l.Message = logscrub.Clip(c.Text(cut(*rec.Msg, rawMessageBytes)), MessageBytes)
	attrs := 0
	for key, v := range rec.KV {
		if !KVKeys[key] {
			dropped++
			continue
		}
		var value string
		if json.Unmarshal(v, &value) != nil { // cannot happen after decodeLine; a value that is no string is dropped
			dropped++
			continue
		}
		if l.Attrs == nil {
			l.Attrs = map[string]string{}
		}
		value = logscrub.ClipRunes(c.Text(cut(value, rawValueBytes)), ValueRunes)
		l.Attrs[key] = value
		attrs += len(key) + len(value) + 6
	}
	return l, dropped, len(l.Message) + len(l.Source) + attrs + 2 + recordOverhead
}
