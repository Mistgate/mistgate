package awg

import (
	"math/bits"
	"net/netip"
	"slices"
	"strings"
	"sync"
)

// The "Russian services direct" key: the .conf and the vpn:// key of such a device carry AllowedIPs = the whole IPv4 world
// minus the networks below, so the traffic to those services leaves around the tunnel and they never see the node's address.
// AmneziaVPN overwrites its own split-tunnelling fields from the app's settings before every connect, so AllowedIPs is the
// one thing a key can carry. A key holds about 1 400 entries; phones start failing near 2 400 (amnezia-client #2976, #3207),
// which is why the list is networks of companies, not "all of Russia" (13 000 networks, 32 000 entries).
//
// RUDirectVersion is the date of the list. Change it with the list: the keys of the devices that hold another version are
// then called "needs a new key" (access.markDNSStale), the ordinary keys are not touched.
//
// How to rebuild the list (the sources are public routing data):
//  1. ranges of the big services' own sites: resolve their domains, take the range of the routing table that holds each answer
//     (the first study: 94 networks);
//  2. add every IPv4 prefix the RIPEstat call announced-prefixes returns for AS13238 (Yandex), AS47541 (VK), AS47764 (Mail.ru);
//  3. the checking host of Yandex, ipv4-internet.yandex.net (5.45.196.64), must be inside the result: with the first study
//     alone it was not, and the node's address showed on yandex.ru/internet.
//
// The networks are merged and complemented below (complement4); the property test pins what the result must and must not cover.
const RUDirectVersion = "2026-11-24"

// RouteRUDirect is the value of a device's route (store: device.awg_route) for this key; "" is the ordinary key.
const RouteRUDirect = "ru-direct"

// ruDirectMaxAllowed is the ceiling the test keeps the list under.
const ruDirectMaxAllowed = 1500

// ruDirectExcluded: the networks that stay outside the tunnel, grouped by whose they are. The grouping is for the reader, the
// result does not depend on it.
var ruDirectExcluded = [...]struct{ owner, nets string }{
	{"Yandex: the announced IPv4 prefixes of AS13238, and the ranges of ya.ru, yandex.ru, Passport, Market, Disk, Kinopoisk, Music, Taxi, static and avatar hosts", `
		5.45.192.0/18 5.255.192.0/18 37.9.64.0/18 37.140.128.0/18 77.88.0.0/18 84.252.160.0/19
		87.250.224.0/19 92.255.112.0/20 93.158.128.0/18 95.108.128.0/17 141.8.128.0/18 178.154.128.0/18
		185.32.187.0/24 213.180.192.0/19
	`},
	{"Yandex Cloud: hosts auto.ru, cian.ru and other sites", `
		51.250.0.0/17
	`},
	{"VK: AS47541 (vk.com, userapi, login.vk.com, the Max messenger), and the Mail.ru neighbours that merge with it", `
		79.137.139.0/24 79.137.164.0/24 79.137.180.0/24 79.137.183.0/24 87.240.128.0/18 91.231.132.0/22
		93.186.224.0/20 95.142.192.0/20 95.213.0.0/17 185.32.248.0/22 185.131.68.0/22 217.69.128.0/20
	`},
	{"Mail.ru group: AS47764 (mail.ru, e.mail.ru, ok.ru, dzen.ru) and the ranges that hold them", `
		5.61.16.0/21 5.61.232.0/21 5.101.40.0/22 5.181.60.0/22 5.188.140.0/22 31.177.104.0/22
		37.139.32.0/22 37.139.40.0/22 45.84.128.0/22 45.136.20.0/22 62.217.160.0/20 79.137.157.0/24
		79.137.174.0/23 79.137.240.0/21 83.166.232.0/21 83.166.248.0/21 83.217.216.0/22 83.222.28.0/22
		84.23.52.0/22 85.142.115.0/24 85.192.32.0/22 87.239.104.0/21 87.242.112.0/22 89.208.84.0/22
		89.208.196.0/22 89.208.208.0/22 89.208.216.0/21 89.208.228.0/22 89.221.228.0/22 89.221.232.0/21
		90.156.148.0/22 90.156.212.0/22 90.156.216.0/22 90.156.232.0/21 91.219.224.0/22 92.38.217.0/24
		94.100.176.0/20 94.139.244.0/22 95.163.32.0/19 95.163.133.0/24 95.163.180.0/22 95.163.208.0/21
		95.163.216.0/22 95.163.248.0/21 109.120.180.0/22 109.120.188.0/22 128.140.168.0/21 130.49.224.0/19
		138.16.192.0/20 138.16.240.0/20 146.185.208.0/22 146.185.240.0/22 154.90.70.0/23 155.212.192.0/20
		160.5.208.0/20 161.104.104.0/21 168.113.128.0/20 176.112.168.0/21 178.22.88.0/21 178.237.16.0/20
		185.5.136.0/22 185.16.148.0/22 185.16.244.0/22 185.86.144.0/22 185.100.104.0/22 185.130.112.0/22
		185.180.200.0/22 185.187.63.0/24 185.226.52.0/22 185.241.192.0/22 188.93.56.0/21 193.203.40.0/22
		194.186.63.0/24 195.211.20.0/22 195.218.190.0/23 212.111.84.0/22 212.233.72.0/21 212.233.88.0/21
		212.233.96.0/22 212.233.120.0/22 213.219.212.0/22 217.16.16.0/20 217.20.144.0/20 217.174.188.0/22
	`},
	{"Banks and payments: Sber, T-Bank, VTB, Alfa-Bank, Raiffeisen, PSB, Gazprombank, NSPK (the faster-payments QR)", `
		82.202.188.0/22 84.252.149.0/24 84.252.150.0/23 84.252.152.0/24 151.236.122.0/23 178.130.128.0/23
		185.179.144.0/22 193.28.44.0/24 193.200.10.0/24 194.190.0.0/24 195.242.82.0/23 217.12.96.0/21
		217.12.104.0/23 217.12.106.0/24
	`},
	{"State services and Rostelecom: Gosuslugi, the tax service (nalog.gov.ru), the pension fund, mos.ru, Russian Post, RZD", `
		94.79.51.0/24 195.161.52.0/23 195.208.64.0/21 195.208.72.0/22 195.208.76.0/23 212.164.0.0/16
		213.24.0.0/18 213.24.64.0/21 213.24.72.0/22 213.59.192.0/18
	`},
	{"Mobile operators and their sites: MTS, MegaFon, Beeline, Tele2", `
		37.220.160.0/22 46.235.184.0/21 78.41.104.0/21 93.93.88.0/22 93.93.92.0/23 93.93.94.0/24
		178.177.0.0/19 178.177.32.0/21 178.248.232.0/21 188.162.14.0/23 188.162.16.0/20 188.162.32.0/19
		195.209.64.0/21 212.193.152.0/22 212.193.156.0/23 217.118.81.0/24 217.118.82.0/23 217.118.84.0/22
		217.118.88.0/22
	`},
	{"Shops, media and others: Ozon, Wildberries, Avito, M.Video, DNS, Lamoda, 5ka, Magnit, hh.ru, 2GIS, ivi, Okko, Rutube, Lenta, RIA", `
		80.67.40.0/22 81.19.64.0/19 91.221.164.0/23 91.236.48.0/22 94.124.200.0/22 109.238.90.0/23
		155.212.234.0/23 176.114.120.0/21 185.62.200.0/23 185.62.202.0/24 185.65.148.0/22 185.71.64.0/22
		185.73.192.0/22 185.169.152.0/22 194.190.139.0/24
	`}}

var ruDirectAllowed = sync.OnceValue(func() []string {
	var excluded []netip.Prefix
	for _, g := range ruDirectExcluded {
		for _, f := range strings.Fields(g.nets) {
			excluded = append(excluded, netip.MustParsePrefix(f))
		}
	}
	return complement4(excluded)
})

// RUDirectAllowedIPs is the AllowedIPs of a "Russian services direct" key: every IPv4 address but those of the services,
// as the fewest networks (the list is the same for every key; the caller must not change it).
func RUDirectAllowedIPs() []string { return ruDirectAllowed() }

// complement4 returns the IPv4 networks that cover every address except those of ex, smallest set, in address order.
func complement4(ex []netip.Prefix) []string {
	type span struct{ lo, hi uint64 }
	spans := make([]span, 0, len(ex))
	for _, p := range ex {
		p = p.Masked()
		lo := uint64(addr4(p.Addr()))
		spans = append(spans, span{lo, lo + 1<<(32-p.Bits()) - 1})
	}
	slices.SortFunc(spans, func(a, b span) int { return int(int64(a.lo) - int64(b.lo)) })
	var out []string
	gap := func(lo, hi uint64) { // [lo, hi] holds no excluded address: cut it into the largest aligned blocks
		for lo <= hi {
			size := uint64(1) << 32
			if lo != 0 {
				size = lo & -lo
			}
			for size > hi-lo+1 {
				size >>= 1
			}
			ip := netip.AddrFrom4([4]byte{byte(lo >> 24), byte(lo >> 16), byte(lo >> 8), byte(lo)})
			out = append(out, netip.PrefixFrom(ip, 32-bits.TrailingZeros64(size)).String())
			lo += size
		}
	}
	next := uint64(0)
	for _, s := range spans {
		if s.lo > next {
			gap(next, s.lo-1)
		}
		next = max(next, s.hi+1)
	}
	if next <= 1<<32-1 {
		gap(next, 1<<32-1)
	}
	return out
}

func addr4(a netip.Addr) uint32 {
	b := a.As4()
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}
