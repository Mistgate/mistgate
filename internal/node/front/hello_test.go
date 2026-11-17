package front

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"
)

// bytesConn is a connection that has only the bytes of data to read, then EOF.
type bytesConn struct{ r io.Reader }

func (c bytesConn) Read(p []byte) (int, error)     { return c.r.Read(p) }
func (bytesConn) Write(p []byte) (int, error)      { return len(p), nil }
func (bytesConn) Close() error                     { return nil }
func (bytesConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (bytesConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (bytesConn) SetDeadline(time.Time) error      { return nil }
func (bytesConn) SetReadDeadline(time.Time) error  { return nil }
func (bytesConn) SetWriteDeadline(time.Time) error { return nil }

func FuzzReadHello(f *testing.F) {
	hello := helloOf(f, "b.example.com")
	f.Add(hello)
	f.Add(split(hello, 3))
	f.Add(withExt(hello, 0xfe0d, echOuter()))
	f.Add(hello[:len(hello)/2])
	f.Add(hello[:7])
	f.Add([]byte("GET / HTTP/1.1\r\nHost: b.example.com\r\n\r\n"))
	f.Add([]byte{0x80, 0x2e, 1, 3, 1, 0, 0x15, 0, 0, 0, 0x10})
	f.Add(record([]byte{1, 0, 0, 4, 1, 2, 3, 4}))
	f.Add(split(withExt(hello, 21, make([]byte, 20000)), 3))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		name, buf, v := readHello(bytesConn{bytes.NewReader(data)})
		if len(buf) > maxHello || !bytes.HasPrefix(data, buf) {
			t.Fatalf("kept %d bytes that are not the start of the %d sent", len(buf), len(data))
		}
		if v == helloTooLarge && len(buf) != maxHello {
			t.Fatalf("too large after %d bytes", len(buf))
		}
		if v != helloOK && name != "" {
			t.Fatalf("name %q without a hello", name)
		}
		// the same bytes read one at a time give the same answer (how much was read before it differs)
		name2, buf2, v2 := readHello(bytesConn{iotestOneByte{bytes.NewReader(data)}})
		if v2 != v || name2 != name || !bytes.HasPrefix(data, buf2) {
			t.Fatalf("one byte at a time: %v %q, whole: %v %q", v2, name2, v, name)
		}
	})
}

type iotestOneByte struct{ r io.Reader }

func (o iotestOneByte) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return o.r.Read(p[:1])
}
