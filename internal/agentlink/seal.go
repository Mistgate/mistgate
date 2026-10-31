package agentlink

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"sync"

	"github.com/coder/websocket"
	"google.golang.org/protobuf/proto"
)

// A sealed link encrypts and authenticates every session frame after the handshake, so a man in the middle that holds
// a certificate the agent trusts for the link host (the link runs over public-PKI TLS) can neither read nor change them.
//
// The agent asks for it with sealMarker at the start of its LinkAuth nonce. A panel that knows sealing answers with a
// SealedPanelDigest signature; an older one signs the nonce as it signs any (PanelDigest) and the session stays plain.
// The marker sits in the nonce, not in a field of its own, because the nonce is what every panel's signature covers: it
// cannot be stripped to make a new panel look old to a new agent.
//
// Keys: HKDF-SHA256 over the ECDH of the node key and the panel CA key (both P-256, already proven by the handshake
// signatures), salted with both nonces. Nobody without one of those private keys can derive them. Each direction has its
// own AES-256-GCM key; the nonce is the frame's sequence number, so a dropped, replayed or reordered frame fails.

var sealMarker = []byte("mgseal01")

const (
	sealedPanelDomain = "mistgate-panel-link-sealed/1\x00"
	sealInfo          = "mistgate-link-seal/1\x00"
	sealKeySize       = 32
	// SealOverhead is what sealing adds to a frame (the GCM tag).
	SealOverhead = 16
)

// SealNonce is a new agent's LinkAuth nonce: 32 bytes, the seal marker then random.
func SealNonce() ([]byte, error) {
	nonce := make([]byte, 32)
	copy(nonce, sealMarker)
	_, err := io.ReadFull(rand.Reader, nonce[len(sealMarker):])
	return nonce, err
}

// WantsSeal reports whether the agent that sent this nonce seals its link.
func WantsSeal(agentNonce []byte) bool { return bytes.HasPrefix(agentNonce, sealMarker) }

// SealedPanelDigest is PanelDigest for a sealed session, in a domain of its own: an older agent never accepts it, and a
// new agent that gets the plain one knows the panel does not seal.
func SealedPanelDigest(nodeID, audience string, panelNonce, agentNonce []byte) []byte {
	h := sha256.Sum256([]byte(sealedPanelDomain + nodeID + "\x00" + audience + "\x00" + string(panelNonce) + string(agentNonce)))
	return h[:]
}

// SealKeys derives a session's keys from one side's private key and the other's public key: the node key and the CA
// certificate's key on the agent, the CA key and the node certificate's key on the panel. The 64 bytes are the key of
// the agent's frames, then the key of the panel's.
func SealKeys(priv *ecdsa.PrivateKey, peer *ecdsa.PublicKey, nodeID, audience string, panelNonce, agentNonce []byte) ([]byte, error) {
	if priv == nil || peer == nil {
		return nil, errors.New("seal keys need both keys")
	}
	ep, err := priv.ECDH()
	if err != nil {
		return nil, err
	}
	pp, err := peer.ECDH()
	if err != nil {
		return nil, err
	}
	shared, err := ep.ECDH(pp)
	if err != nil {
		return nil, err
	}
	salt := append(append([]byte{}, panelNonce...), agentNonce...)
	return hkdf.Key(sha256.New, shared, salt, sealInfo+nodeID+"\x00"+audience, 2*sealKeySize)
}

// sealAEAD is the cipher of one direction of keys and the nonce of frame seq.
func sealAEAD(keys []byte, fromAgent bool, seq uint64) (cipher.AEAD, []byte, error) {
	if len(keys) != 2*sealKeySize {
		return nil, nil, errors.New("bad link seal keys")
	}
	key := keys[sealKeySize:]
	if fromAgent {
		key = keys[:sealKeySize]
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	binary.BigEndian.PutUint64(nonce[len(nonce)-8:], seq)
	return aead, nonce, nil
}

// SealFrame encrypts frame number seq of the agent's (fromAgent) or the panel's direction.
func SealFrame(keys []byte, fromAgent bool, seq uint64, plain []byte) ([]byte, error) {
	aead, nonce, err := sealAEAD(keys, fromAgent, seq)
	if err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plain, nil), nil
}

// ErrFrameAuth reports a sealed frame that did not open: changed, replayed, reordered or sealed with another key.
var ErrFrameAuth = errors.New("link frame failed authentication")

// OpenFrame decrypts frame number seq of the agent's (fromAgent) or the panel's direction.
func OpenFrame(keys []byte, fromAgent bool, seq uint64, sealed []byte) ([]byte, error) {
	aead, nonce, err := sealAEAD(keys, fromAgent, seq)
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ErrFrameAuth
	}
	return plain, nil
}

// Link is one end of a link session after the handshake: protobuf frames, sealed when both ends can. One goroutine reads.
type Link struct {
	ws     *websocket.Conn
	keys   []byte // nil = a plain session with an older peer
	agent  bool   // this end is the agent
	mu     sync.Mutex
	outSeq uint64
	inSeq  uint64
}

// NewLink wraps ws for the agent's end (agent) or the panel's; keys are SealKeys', nil for a plain session.
func NewLink(ws *websocket.Conn, keys []byte, agent bool) *Link {
	return &Link{ws: ws, keys: keys, agent: agent}
}

// Sealed reports whether the session's frames are sealed.
func (l *Link) Sealed() bool { return l.keys != nil }

// Write sends one message. Sealed writes are serialised so their sequence numbers go out in order.
func (l *Link) Write(ctx context.Context, message proto.Message) error {
	if l.keys == nil {
		return WriteFrame(ctx, l.ws, message)
	}
	b, err := proto.Marshal(message)
	if err != nil {
		return err
	}
	if len(b) > MaxFrameSize-SealOverhead {
		return ErrFrameTooLarge
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	sealed, err := SealFrame(l.keys, l.agent, l.outSeq, b)
	if err != nil {
		return err
	}
	l.outSeq++
	return l.ws.Write(ctx, websocket.MessageBinary, sealed)
}

// Read returns the next frame, opened when the session is sealed. A non-binary frame comes back as it is, for the caller
// to refuse.
func (l *Link) Read(ctx context.Context) (websocket.MessageType, []byte, error) {
	typ, b, err := ReadFrame(ctx, l.ws)
	if err != nil || l.keys == nil || typ != websocket.MessageBinary {
		return typ, b, err
	}
	plain, err := OpenFrame(l.keys, !l.agent, l.inSeq, b)
	if err != nil {
		return typ, nil, err
	}
	l.inSeq++
	return typ, plain, nil
}
