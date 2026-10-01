package mimicry

import (
	"errors"
	"fmt"
	"strings"
)

// MaxDomainLen caps a host name: it lands in the packet several times (SIP repeats it in four headers) and a
// packet is limited to 1200 bytes. Real popular names are far shorter.
const MaxDomainLen = 100

// domainPool is where the host name of a packet comes from when the admin gives none: SNI of the QUIC Initial,
// the name of the DNS queries, the SIP domain. Clients are in Russia, so the list is what a user there opens all
// day: the big Russian services and the CDNs and vendors they sit behind. No adult, political or news sites, and
// nothing that is blocked there. Drawn uniformly: a weighted top (as in the tools this list comes from) makes the
// first few names a mark of every profile. Reachability is taken from the source lists, it was not measured.
var domainPool = []string{
	// Russian services
	"yandex.ru", "ya.ru", "dzen.ru", "mail.ru", "vk.com", "vk.ru", "ok.ru", "rutube.ru", "kinopoisk.ru",
	"ozon.ru", "wildberries.ru", "avito.ru", "2gis.ru", "sberbank.ru", "sber.ru", "tbank.ru", "vtb.ru",
	"alfabank.ru", "raiffeisen.ru", "gosuslugi.ru", "mos.ru", "hh.ru", "cian.ru", "drom.ru", "auto.ru",
	"dns-shop.ru", "mvideo.ru", "citilink.ru", "lamoda.ru", "sportmaster.ru", "detmir.ru", "aviasales.ru",
	"tutu.ru", "ivi.ru", "okko.tv", "kion.ru", "premier.one", "more.tv", "rustore.ru", "habr.com", "vc.ru",
	"gismeteo.ru", "rambler.ru", "sports.ru", "championat.com", "banki.ru", "consultant.ru", "1c.ru",
	"kaspersky.ru", "mts.ru", "beeline.ru", "megafon.ru", "tele2.ru", "rt.ru", "apteka.ru", "eapteka.ru",
	"megamarket.ru", "magnit.ru", "5ka.ru", "vkusvill.ru", "perekrestok.ru", "leroymerlin.ru", "hoff.ru",
	"goldapple.ru", "letu.ru", "samokat.ru", "ru.wikipedia.org",
	// their CDNs and hosting
	"yastatic.net", "yandex.net", "mycdn.me", "userapi.com", "imgsmail.ru", "cdn1.ozone.ru", "selectel.ru",
	"timeweb.cloud", "reg.ru", "nic.ru",
	// global CDNs, clouds, vendors
	"www.google.com", "www.microsoft.com", "www.apple.com", "www.cloudflare.com", "www.amazon.com",
	"aws.amazon.com", "cdnjs.cloudflare.com", "cdn.jsdelivr.net", "unpkg.com", "fonts.googleapis.com",
	"fonts.gstatic.com", "ajax.googleapis.com", "www.gstatic.com", "ssl.gstatic.com", "play.google.com",
	"github.com", "raw.githubusercontent.com", "gitlab.com", "stackoverflow.com", "www.wikipedia.org",
	"upload.wikimedia.org", "www.mozilla.org", "www.bing.com", "login.live.com", "login.microsoftonline.com",
	"www.office.com", "download.windowsupdate.com", "s3.amazonaws.com", "d1.awsstatic.com",
	"store.steampowered.com", "steamcdn-a.akamaihd.net", "registry.npmjs.org", "pypi.org", "www.python.org",
	"www.nvidia.com", "www.amd.com", "www.intel.com", "www.samsung.com", "www.mi.com", "www.huawei.com",
	"www.tp-link.com", "www.logitech.com", "www.asus.com", "www.dell.com", "www.lenovo.com", "www.adobe.com",
	"www.jetbrains.com", "www.oracle.com", "www.icloud.com", "itunes.apple.com", "is1-ssl.mzstatic.com",
}

// Domains lists the built-in pool, the names an empty Options.Domain is drawn from.
func Domains() []string { return append([]string(nil), domainPool...) }

// NormalizeDomain checks a host name typed by an admin and returns it as the packets will carry it: trimmed,
// lower case, no trailing dot. At least two labels, letters/digits/hyphens (Unicode names go in the xn-- form),
// labels of 1-63 characters, the last one not all digits (that is an IP address, not a name), at most
// MaxDomainLen characters.
func NormalizeDomain(s string) (string, error) {
	d := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if d == "" {
		return "", errors.New("domain is empty")
	}
	if len(d) > MaxDomainLen {
		return "", fmt.Errorf("domain is longer than %d characters", MaxDomainLen)
	}
	labels := strings.Split(d, ".")
	if len(labels) < 2 {
		return "", errors.New("domain needs at least two labels, like example.com")
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", errors.New("every label of a domain is 1-63 characters")
		}
		if l[0] == '-' || l[len(l)-1] == '-' {
			return "", errors.New("a label of a domain does not start or end with a hyphen")
		}
		for i := 0; i < len(l); i++ {
			if c := l[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return "", fmt.Errorf("domain has %q: only letters, digits and hyphens, write Unicode names as xn-- ", c)
			}
		}
		if len(l) >= 4 && l[2:4] == "--" && !strings.HasPrefix(l, "xn--") {
			return "", errors.New("a label with -- in the third and fourth place has to start with xn--")
		}
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 || strings.Trim(tld, "0123456789") == "" {
		return "", errors.New("the last label of a domain is two or more characters and not a number")
	}
	return d, nil
}
