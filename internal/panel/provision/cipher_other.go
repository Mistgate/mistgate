//go:build !js

package provision

// sshCiphers is the cipher order offered to the server; nil keeps the library's default order.
func sshCiphers() []string { return nil }
