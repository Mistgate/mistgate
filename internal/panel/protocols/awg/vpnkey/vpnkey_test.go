package vpnkey

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata/qt_sample.key was produced by a real Qt 6.11.2 (PySide6: qCompress(json, 8) + toBase64 with
// Base64UrlEncoding|OmitTrailingEquals). The Go side decodes it here; the other direction (a Go key through
// qUncompress) was checked once by hand, and the end-to-end test repeats it.
func TestDecodeQtKey(t *testing.T) {
	raw, err := os.ReadFile("../testdata/qt_sample.key")
	if err != nil {
		t.Fatal(err)
	}
	js, err := Decode(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Containers []struct {
			Container string `json:"container"`
		} `json:"containers"`
		HostName string `json:"hostName"`
	}
	if err := json.Unmarshal(js, &doc); err != nil {
		t.Fatalf("decoded bytes are not JSON: %v\n%s", err, js)
	}
	if len(doc.Containers) == 0 || !strings.HasPrefix(doc.Containers[0].Container, "amnezia-awg") {
		t.Errorf("unexpected document: %s", js)
	}
	t.Logf("host %s, %d bytes of JSON", doc.HostName, len(js))
}

func TestRoundTrip(t *testing.T) {
	in := map[string]any{"description": "de1 · AWG 3.1", "hostName": "203.0.113.10", "n": 1}
	key, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, Prefix) || strings.ContainsAny(key[len(Prefix):], "=+/") {
		t.Fatalf("key is not base64url without padding: %q", key)
	}
	js, err := Decode(key)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(js, &out); err != nil || out["hostName"] != "203.0.113.10" {
		t.Fatalf("round trip: %v %s", err, js)
	}
	// The blob starts with the big-endian length of the uncompressed JSON (under 64 KiB: two zero bytes).
	if !strings.HasPrefix(key, Prefix+"AAA") {
		t.Errorf("key does not start with a 4-byte length prefix: %q", key[:12])
	}
}

func TestDecodeTolerance(t *testing.T) {
	key, _ := Encode(map[string]int{"a": 1})
	for _, k := range []string{key + "==", "  " + key + "\n", strings.TrimPrefix(key, Prefix)} {
		if js, err := Decode(k); err != nil || !bytes.Contains(js, []byte(`"a"`)) {
			t.Errorf("Decode(%q) = %s, %v", k, js, err)
		}
	}
	// Not a qCompress blob: the client takes the bytes as plain JSON.
	plain, err := Decode(Prefix + "eyJhIjoxfQ")
	if err != nil || string(plain) != `{"a":1}` {
		t.Errorf("plain JSON key = %q, %v", plain, err)
	}
	if _, err := Decode(Prefix + "!!!"); err == nil {
		t.Error("garbage accepted")
	}
}

func TestDecodeRefusesInflationBomb(t *testing.T) {
	// A qCompress header that claims more than maxDecoded is not inflated.
	blob := []byte{0x7f, 0xff, 0xff, 0xff, 0x78, 0xda, 0x03, 0x00}
	if out, err := qUncompress(blob); err == nil {
		t.Errorf("accepted a huge claim: %d bytes", len(out))
	}
}

func FuzzDecode(f *testing.F) {
	k, _ := Encode(map[string]int{"a": 1})
	f.Add(k)
	f.Add("vpn://")
	f.Fuzz(func(t *testing.T, s string) { _, _ = Decode(s) })
}
