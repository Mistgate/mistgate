//go:build !js

package clientdebug

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"time"
)

var now = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

func ms(offset time.Duration) int64 { return now.Add(offset).UnixMilli() }

func rec(ts int64, lvl, src, msg, kv string) string {
	if kv != "" {
		kv = `,"kv":` + kv
	}
	return fmt.Sprintf(`{"ts":%d,"lvl":%q,"src":%q,"msg":%q%s}`, ts, lvl, src, msg, kv)
}

func parse(t *testing.T, lines ...string) (Bundle, error) {
	t.Helper()
	return Parse(strings.NewReader(strings.Join(lines, "\n")+"\n"), now, testCleaner())
}

func badLine(t *testing.T, err error, line int, field string) {
	t.Helper()
	var bad *BadLine
	if !errors.As(err, &bad) || bad.Line != line || bad.Field != field {
		t.Fatalf("error = %v, want line %d: %s", err, line, field)
	}
}

func TestParseTakesTheContractSample(t *testing.T) {
	b, err := parse(t,
		rec(ms(-10*time.Second), "info", "service", "connecting", `{"path":"pth_q1W2e3R4t5Y6u7I8","protocol":"hysteria2","attempt":"1"}`),
		rec(ms(-5*time.Second), "warn", "tunnel", "handshake timed out after 5000 ms", `{"path":"pth_q1W2e3R4t5Y6u7I8","code":"handshake_timeout"}`),
		`{"ts":`+fmt.Sprint(ms(-4*time.Second))+`,"lvl":"error","src":"service","msg":"cannot write C:\\Users\\SomeUser\\AppData\\Roaming\\Mistgate\\state.json","kv":{"code":"state_write_failed"}}`,
	)
	if err != nil || len(b.Lines) != 3 {
		t.Fatalf("Parse: %d lines, %v", len(b.Lines), err)
	}
	if l := b.Lines[0]; l.Level != 1 || l.Source != "service" || l.Message != "connecting" || l.Attrs["protocol"] != "hysteria2" || l.Attrs["path"] != "pth_q1W2e3R4t5Y6u7I8" || l.AtMs != ms(-10*time.Second) {
		t.Errorf("line 1: %+v", l)
	}
	if b.Lines[1].Level != 2 || b.Lines[2].Level != 3 {
		t.Errorf("levels: %d %d", b.Lines[1].Level, b.Lines[2].Level)
	}
	if got := b.Lines[2].Message; got != `cannot write C:\Users\<user>\AppData\Roaming\Mistgate\state.json` {
		t.Errorf("the path was not cleaned: %q", got)
	}
	if b.FirstMs != ms(-10*time.Second) || b.LastMs != ms(-4*time.Second) || b.Bytes <= 0 || b.DroppedKV != 0 {
		t.Errorf("bundle: first %d last %d bytes %d dropped %d", b.FirstMs, b.LastMs, b.Bytes, b.DroppedKV)
	}
}

func TestParseRefusesWhatBreaksTheSchema(t *testing.T) {
	ok := rec(ms(-time.Minute), "info", "service", "fine", "")
	for _, tc := range []struct {
		name, line string
		field      string
	}{
		{"unknown field", `{"ts":1,"lvl":"info","src":"a","msg":"x","host":"alice-liddell"}`, "unknown_field"},
		{"ts missing", `{"lvl":"info","src":"a","msg":"x"}`, "ts"},
		{"ts a string", `{"ts":"12","lvl":"info","src":"a","msg":"x"}`, "ts"},
		{"ts a fraction", fmt.Sprintf(`{"ts":%d.5,"lvl":"info","src":"a","msg":"x"}`, ms(0)), "ts"},
		{"ts older than 30 days", rec(ms(-30*24*time.Hour-time.Second), "info", "a", "x", ""), "ts"},
		{"ts more than a day ahead", rec(ms(24*time.Hour+time.Second), "info", "a", "x", ""), "ts"},
		{"lvl unknown", rec(ms(0), "trace", "a", "x", ""), "lvl"},
		{"lvl missing", fmt.Sprintf(`{"ts":%d,"src":"a","msg":"x"}`, ms(0)), "lvl"},
		{"lvl a number", fmt.Sprintf(`{"ts":%d,"lvl":2,"src":"a","msg":"x"}`, ms(0)), "lvl"},
		{"src with a capital", rec(ms(0), "info", "Service", "x", ""), "src"},
		{"src starts with a digit", rec(ms(0), "info", "1service", "x", ""), "src"},
		{"src too long", rec(ms(0), "info", "a"+strings.Repeat("b", 32), "x", ""), "src"},
		{"src missing", fmt.Sprintf(`{"ts":%d,"lvl":"info","msg":"x"}`, ms(0)), "src"},
		{"msg missing", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a"}`, ms(0)), "msg"},
		{"msg null", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a","msg":null}`, ms(0)), "msg"},
		{"msg a number", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a","msg":5}`, ms(0)), "msg"},
		{"kv a list", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a","msg":"x","kv":["a"]}`, ms(0)), "kv"},
		{"kv value a number", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a","msg":"x","kv":{"count":3}}`, ms(0)), "kv"},
		{"kv value null", fmt.Sprintf(`{"ts":%d,"lvl":"info","src":"a","msg":"x","kv":{"count":null}}`, ms(0)), "kv"},
		{"kv 17 keys", rec(ms(0), "info", "a", "x", manyKV(17)), "kv"},
		{"not json", `connecting to the server`, "json"},
		{"a list", `[1,2]`, "json"},
		{"text after the object", ok + ` {"x":1}`, "json"},
		{"cut off", `{"ts":1,"lvl":"in`, "json"},
		{"an empty line", ``, "empty"},
		{"a line over 8 KiB", rec(ms(0), "info", "a", strings.Repeat("x", MaxLine), ""), "size"},
	} {
		_, err := parse(t, ok, tc.line)
		var bad *BadLine
		if !errors.As(err, &bad) || bad.Line != 2 || bad.Field != tc.field {
			t.Errorf("%s: error = %v, want line 2: %s", tc.name, err, tc.field)
		}
		if tc.name == "unknown field" && strings.Contains(err.Error(), "alice-liddell") {
			t.Errorf("the message repeats a value: %v", err)
		}
	}
	// the line before the fault is not the one reported, and an empty delivery is a fault too
	_, err := parse(t, ok, ok, `{"ts":1}`)
	badLine(t, err, 3, "ts")
	_, err = Parse(strings.NewReader(""), now, testCleaner())
	badLine(t, err, 1, "empty")
	// a line of exactly 8 KiB (without the newline) is fine
	l := rec(ms(0), "info", "a", "", "")
	l = rec(ms(0), "info", "a", strings.Repeat("x", MaxLine-len(l)), "")
	if len(l) != MaxLine {
		t.Fatalf("test line has %d bytes", len(l))
	}
	if _, err := parse(t, l); err != nil {
		t.Errorf("a line of 8 KiB: %v", err)
	}
	// no newline at the end, and \r\n between the lines
	b, err := Parse(strings.NewReader(ok+"\r\n"+ok), now, testCleaner())
	if err != nil || len(b.Lines) != 2 {
		t.Errorf("CRLF delivery: %d lines, %v", len(b.Lines), err)
	}
}

func manyKV(n int) string {
	var parts []string
	for i := range n {
		parts = append(parts, fmt.Sprintf(`"k%d":"v"`, i))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestParseKeepsAllowListedAttributesCutAndCleaned(t *testing.T) {
	long := strings.Repeat("é", 400) // 800 bytes, 400 characters
	b, err := parse(t, rec(ms(0), "info", "a", "x", `{"path":"pth_x","err":"dial 203.0.113.45:443 failed","host":"alice-liddell-pc","ssid":"HomeNet","code":"`+long+`","version":"0.3.1"}`))
	if err != nil {
		t.Fatal(err)
	}
	a := b.Lines[0].Attrs
	if len(a) != 4 || a["path"] != "pth_x" || a["err"] != "dial <ip>:port failed" || a["version"] != "0.3.1" || a["host"] != "" || a["ssid"] != "" {
		t.Errorf("attrs: %v", a)
	}
	if got := []rune(a["code"]); len(got) != ValueRunes {
		t.Errorf("a long value has %d characters, want %d", len(got), ValueRunes)
	}
	if b.DroppedKV != 2 {
		t.Errorf("dropped = %d, want the 2 keys outside the list", b.DroppedKV)
	}
	// every key the contract names is on the list
	if len(KVKeys) != 20 {
		t.Errorf("the allow-list has %d keys, the contract names 20", len(KVKeys))
	}
	for _, key := range strings.Split("path server protocol via engine phase state reason code err mode version count ms bytes attempt net event status http", " ") {
		if !KVKeys[key] {
			t.Errorf("key %s is not allow-listed", key)
		}
	}
}

func TestParseCutsMessagesAfterCleaning(t *testing.T) {
	long := strings.Repeat("word ", 600) // 3000 bytes
	b, err := parse(t, rec(ms(0), "info", "a", long, ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(b.Lines[0].Message); got > MessageBytes || got < MessageBytes-100 {
		t.Errorf("message has %d bytes, want just under %d", got, MessageBytes)
	}
	// a key at the end of a long message is not left half there
	key := strings.Repeat("Ab1-", 10)
	b, err = parse(t, rec(ms(0), "info", "a", strings.Repeat("word ", 400)+key, ""))
	if err != nil || strings.Contains(b.Lines[0].Message, "Ab1-") {
		t.Errorf("part of a key survived: %v %v", err, b.Lines[0].Message[len(b.Lines[0].Message)-20:])
	}
	// multi-byte text is cut on a character
	b, err = parse(t, rec(ms(0), "info", "a", strings.Repeat("я", 1500), ""))
	if err != nil || len(b.Lines[0].Message) > MessageBytes || strings.ContainsRune(b.Lines[0].Message, '\uFFFD') {
		t.Errorf("multi-byte cut: %v %d", err, len(b.Lines[0].Message))
	}
}

func TestParseLimits(t *testing.T) {
	line := rec(ms(0), "info", "a", "x", "")
	// 20000 lines are the most
	if b, err := Parse(strings.NewReader(strings.Repeat(line+"\n", MaxLines)), now, testCleaner()); err != nil || len(b.Lines) != MaxLines {
		t.Fatalf("20000 lines: %d %v", len(b.Lines), err)
	}
	if _, err := Parse(strings.NewReader(strings.Repeat(line+"\n", MaxLines+1)), now, testCleaner()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("20001 lines: %v", err)
	}
	// 4 MiB of text are the most: the caller's limit reader hands over one byte more than that
	big := rec(ms(0), "info", "a", strings.Repeat("y", 4000), "")
	n := MaxText/(len(big)+1) + 2
	over := strings.Repeat(big+"\n", n)
	if len(over) <= MaxText {
		t.Fatalf("test body has %d bytes", len(over))
	}
	if _, err := Parse(io.LimitReader(strings.NewReader(over), MaxText+1), now, testCleaner()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("a body past 4 MiB: %v", err)
	}
	if _, err := Parse(io.LimitReader(strings.NewReader(over[:MaxText]), MaxText+1), now, testCleaner()); err != nil && !errors.Is(err, ErrTooLarge) {
		var bad *BadLine
		if !errors.As(err, &bad) || bad.Field != "json" { // the cut in the middle of the last line
			t.Errorf("a body of exactly 4 MiB: %v", err)
		}
	}
	// the wire's limit and a stream that does not unpack
	if _, err := Parse(iotest.ErrReader(&http.MaxBytesError{Limit: 1 << 20}), now, testCleaner()); !errors.Is(err, ErrTooLarge) {
		t.Errorf("wire limit: %v", err)
	}
	_, err := Parse(io.MultiReader(strings.NewReader(line+"\n"), iotest.ErrReader(errors.New("flate: corrupt"))), now, testCleaner())
	badLine(t, err, 2, "gzip")
}
