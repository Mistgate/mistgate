// Package dnsdefaults defines the country-specific resolvers the doctor checks and offers
// for the server's own resolver when the node has none configured. Russian nodes use
// Yandex DNS, mainland Chinese nodes use AliDNS and DNSPod, and other nodes use
// Cloudflare and Google. An empty node list itself means the server's own resolver; the
// panel never substitutes these.
package dnsdefaults

import "strings"

var (
	russian = []string{"77.88.8.8", "77.88.8.1"}
	china   = []string{"223.5.5.5", "119.29.29.29"}
	global  = []string{"1.1.1.1", "8.8.8.8"}
)

// ForCountry returns the recommended DNS resolvers for a node. Russian nodes use
// Yandex so Russian services such as gosuslugi.ru resolve reliably; mainland Chinese
// nodes use AliDNS and DNSPod so Chinese services resolve reliably. Other nodes use
// Cloudflare and Google. The returned slice belongs to the caller.
func ForCountry(country string) []string {
	switch {
	case strings.EqualFold(country, "RU"):
		return append([]string(nil), russian...)
	case strings.EqualFold(country, "CN"):
		return append([]string(nil), china...)
	default:
		return append([]string(nil), global...)
	}
}
