//go:build !censorsim

package countryprobe

// Production builds use only the fixed references declared in refs.go.
func censorSimReferences(string) (References, bool) { return References{}, false }
