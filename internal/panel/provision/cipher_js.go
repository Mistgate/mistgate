//go:build js

package provision

import "golang.org/x/crypto/ssh"

// sshCiphers puts chacha20-poly1305 first: on js/wasm it moves the 30 MB agent upload 2.6 times faster than the
// library's default AES-GCM (no AES instructions in wasm). The other ciphers keep their library order after it.
func sshCiphers() []string {
	supported := ssh.SupportedAlgorithms().Ciphers
	const chacha = "chacha20-poly1305@openssh.com"
	out := make([]string, 0, len(supported))
	for _, c := range supported {
		if c == chacha {
			out = append([]string{c}, out...)
			continue
		}
		out = append(out, c)
	}
	return out
}
