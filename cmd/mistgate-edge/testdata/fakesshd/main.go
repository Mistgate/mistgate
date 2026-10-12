// Command fakesshd is the SSH server of the edge js tests (scripts/test-edge-store.ps1): a password login and the three
// commands an installation runs over it, all in memory, so it runs the same on a developer's Windows machine and in CI.
//
//	uname -a            prints a fixed line
//	cat > <path>        stores stdin under <path>
//	sha256sum <path>    prints "<hex>  <path>" of what was stored
//
// It also opens a "silent" port that accepts connections and never says anything, for the tests of a connection that
// hangs. On start it prints one line, "READY <address> <silent address> <fingerprint> <user> <password>", with a
// password made for this run, and serves until it is killed. Anything else about the server (other host keys, other
// login methods) is covered by the provision package's own tests.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

const user = "root"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fakesshd:", err)
		os.Exit(1)
	}
}

func run() error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	password := hex.EncodeToString(raw)

	config := &ssh.ServerConfig{
		PasswordCallback: func(meta ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			if meta.User() == user && subtle.ConstantTimeCompare(got, []byte(password)) == 1 {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	config.AddHostKey(signer)

	files := &fileStore{files: map[string][]byte{}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	fmt.Printf("READY %s %s %s %s %s\n", listener.Addr(), silent.Addr(), ssh.FingerprintSHA256(signer.PublicKey()), user, password)

	go func() {
		for {
			conn, err := silent.Accept()
			if err != nil {
				return
			}
			_ = conn // held open until the process ends: a connection that never answers
		}
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go serve(conn, config, files)
	}
}

type fileStore struct {
	mu    sync.Mutex
	files map[string][]byte
}

func serve(conn net.Conn, config *ssh.ServerConfig, files *fileStore) {
	defer conn.Close()
	server, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "session channels only")
			continue
		}
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		go func() {
			for request := range channelRequests {
				if request.Type != "exec" {
					_ = request.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if ssh.Unmarshal(request.Payload, &payload) != nil {
					_ = request.Reply(false, nil)
					continue
				}
				_ = request.Reply(true, nil)
				code := exec(payload.Command, channel, files)
				_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{code}))
				_ = channel.Close()
				return
			}
		}()
	}
}

func exec(command string, channel ssh.Channel, files *fileStore) uint32 {
	switch {
	case command == "uname -a":
		_, _ = io.WriteString(channel, "Linux fakesshd 6.1.0 #1 SMP x86_64 GNU/Linux\n")
		return 0
	case strings.HasPrefix(command, "cat > "):
		data, err := io.ReadAll(channel)
		if err != nil {
			return 1
		}
		files.mu.Lock()
		files.files[strings.TrimPrefix(command, "cat > ")] = data
		files.mu.Unlock()
		return 0
	case strings.HasPrefix(command, "sha256sum "):
		path := strings.TrimPrefix(command, "sha256sum ")
		files.mu.Lock()
		data, ok := files.files[path]
		files.mu.Unlock()
		if !ok {
			_, _ = io.WriteString(channel.Stderr(), "sha256sum: "+path+": No such file or directory\n")
			return 1
		}
		sum := sha256.Sum256(data)
		_, _ = fmt.Fprintf(channel, "%s  %s\n", hex.EncodeToString(sum[:]), path)
		return 0
	}
	_, _ = io.WriteString(channel.Stderr(), "fakesshd: unknown command\n")
	return 127
}
