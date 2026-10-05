// Package dnsdefaults defines the resolvers the doctor checks and offers for the server's
// own resolver when the node has none configured. An empty node list itself means the
// server's own resolver; the panel never substitutes these.
package dnsdefaults

import "strings"

var (
	russian = []string{"77.88.8.8", "77.88.8.1"}
	global  = []string{"1.1.1.1", "8.8.8.8"}
)

// ForCountry returns the recommended DNS resolvers for a node. Russian nodes use
// Yandex so Russian services such as gosuslugi.ru resolve reliably; other nodes use
// Cloudflare and Google. The returned slice belongs to the caller.
func ForCountry(country string) []string {
	if strings.EqualFold(country, "RU") {
		return append([]string(nil), russian...)
	}
	return append([]string(nil), global...)
}
