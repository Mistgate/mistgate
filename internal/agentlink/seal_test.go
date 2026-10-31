package agentlink

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"google.golang.org/protobuf/proto"
)

func sealTestKeys(t *testing.T) (agentKeys, panelKeys []byte, panelNonce, agentNonce []byte) {
	t.Helper()
	node, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	panelNonce = bytes.Repeat([]byte{1}, 32)
	agentNonce, err := SealNonce()
	if err != nil {
		t.Fatal(err)
	}
	if agentKeys, err = SealKeys(node, &ca.PublicKey, "nod_a", "panel.example", panelNonce, agentNonce); err != nil {
		t.Fatal(err)
	}
	if panelKeys, err = SealKeys(ca, &node.PublicKey, "nod_a", "panel.example", panelNonce, agentNonce); err != nil {
		t.Fatal(err)
	}
	return agentKeys, panelKeys, panelNonce, agentNonce
}

func TestSealKeysAgreeAndBindTheSession(t *testing.T) {
	agentKeys, panelKeys, _, agentNonce := sealTestKeys(t)
	if !bytes.Equal(agentKeys, panelKeys) || len(agentKeys) != 64 || bytes.Equal(agentKeys[:32], agentKeys[32:]) {
		t.Fatal("the two ends derive different keys, or one key for both directions")
	}
	if !WantsSeal(agentNonce) || WantsSeal(bytes.Repeat([]byte{7}, 32)) {
		t.Error("WantsSeal does not tell a sealing agent's nonce from an older one's")
	}
	node, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	ca, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	base, _ := SealKeys(node, &ca.PublicKey, "nod_a", "panel.example", []byte("p"), []byte("a"))
	for name, other := range map[string]func() ([]byte, error){
		"node id": func() ([]byte, error) {
			return SealKeys(node, &ca.PublicKey, "nod_b", "panel.example", []byte("p"), []byte("a"))
		},
		"audience": func() ([]byte, error) {
			return SealKeys(node, &ca.PublicKey, "nod_a", "other.example", []byte("p"), []byte("a"))
		},
		"panel nonce": func() ([]byte, error) {
			return SealKeys(node, &ca.PublicKey, "nod_a", "panel.example", []byte("q"), []byte("a"))
		},
		"agent nonce": func() ([]byte, error) {
			return SealKeys(node, &ca.PublicKey, "nod_a", "panel.example", []byte("p"), []byte("b"))
		},
	} {
		if k, err := other(); err != nil || bytes.Equal(k, base) {
			t.Errorf("another %s gives the same keys (err %v)", name, err)
		}
	}
}

func TestSealedFramesRefuseChangeReplayReorderAndReflection(t *testing.T) {
	keys, _, _, _ := sealTestKeys(t)
	plain := []byte("desired state")
	sealed0, _ := SealFrame(keys, false, 0, plain)
	sealed1, _ := SealFrame(keys, false, 1, plain)
	if got, err := OpenFrame(keys, false, 0, sealed0); err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("round trip: %q, %v", got, err)
	}
	if bytes.Contains(sealed0, plain) || bytes.Equal(sealed0, sealed1) {
		t.Error("a sealed frame shows its plaintext, or two frames seal alike")
	}
	changed := bytes.Clone(sealed0)
	changed[0] ^= 1
	for name, open := range map[string]func() ([]byte, error){
		"changed":            func() ([]byte, error) { return OpenFrame(keys, false, 0, changed) },
		"replayed":           func() ([]byte, error) { return OpenFrame(keys, false, 1, sealed0) },
		"reordered":          func() ([]byte, error) { return OpenFrame(keys, false, 0, sealed1) },
		"sent back to panel": func() ([]byte, error) { return OpenFrame(keys, true, 0, sealed0) },
		"cut short":          func() ([]byte, error) { return OpenFrame(keys, false, 0, sealed0[:len(sealed0)-1]) },
	} {
		if _, err := open(); !errors.Is(err, ErrFrameAuth) {
			t.Errorf("%s frame: %v, want ErrFrameAuth", name, err)
		}
	}
	if _, err := OpenFrame(keys[:32], false, 0, sealed0); err == nil {
		t.Error("short keys opened a frame")
	}
}

func TestSealedPanelDigestIsItsOwnDomain(t *testing.T) {
	p, a := []byte("p"), []byte("a")
	if bytes.Equal(SealedPanelDigest("n", "h", p, a), PanelDigest("n", "h", p, a)) {
		t.Fatal("a sealed accept would verify as a plain one")
	}
}

// Link over a real WebSocket: messages cross sealed both ways, a plain Link talks to an older peer, and a sealed frame
// that was changed on the way is refused.
func TestLinkSealsBothWays(t *testing.T) {
	keys, _, _, _ := sealTestKeys(t)
	for _, tc := range []struct {
		name string
		keys []byte
	}{{"sealed", keys}, {"plain", nil}} {
		t.Run(tc.name, func(t *testing.T) {
			agent, panel, raw := linkPair(t, tc.keys)
			ctx := context.Background()
			if err := agent.Write(ctx, &pb.ConnectRequest{Seq: 7}); err != nil {
				t.Fatal(err)
			}
			if _, b, err := panel.Read(ctx); err != nil || readSeq(t, b) != 7 {
				t.Fatalf("panel read: %v", err)
			}
			for i := uint64(1); i <= 3; i++ {
				if err := panel.Write(ctx, &pb.ConnectRequest{Seq: i}); err != nil {
					t.Fatal(err)
				}
				if _, b, err := agent.Read(ctx); err != nil || readSeq(t, b) != i {
					t.Fatalf("agent read %d: %v", i, err)
				}
			}
			if agent.Sealed() != (tc.keys != nil) {
				t.Error("Sealed() is wrong")
			}
			// A man in the middle writes his own frame in the panel's place.
			forged, _ := proto.Marshal(&pb.ConnectRequest{Seq: 99})
			if err := raw.Write(ctx, websocket.MessageBinary, forged); err != nil {
				t.Fatal(err)
			}
			_, b, err := agent.Read(ctx)
			if tc.keys != nil && !errors.Is(err, ErrFrameAuth) {
				t.Errorf("a forged frame in a sealed session: %v, want ErrFrameAuth", err)
			}
			if tc.keys == nil && (err != nil || readSeq(t, b) != 99) {
				t.Errorf("a plain session reads frames as they come: %v", err)
			}
		})
	}
}

func readSeq(t *testing.T, b []byte) uint64 {
	t.Helper()
	var m pb.ConnectRequest
	if err := proto.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return m.Seq
}

// linkPair connects an agent Link to a panel Link; raw is the panel's socket under its Link.
func linkPair(t *testing.T, keys []byte) (agent, panel *Link, raw *websocket.Conn) {
	t.Helper()
	accepted := make(chan *websocket.Conn, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		accepted <- ws
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	ws, _, err := websocket.Dial(context.Background(), "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	raw = <-accepted
	t.Cleanup(func() { raw.CloseNow() })
	return NewLink(ws, keys, true), NewLink(raw, keys, false), raw
}
