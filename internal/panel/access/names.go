package access

import (
	"cmp"
	"context"
	"strconv"
	"strings"

	"golang.org/x/text/language"
	"golang.org/x/text/language/display"

	"github.com/mistgate/mistgate/internal/panel/instance"
	"github.com/mistgate/mistgate/internal/panel/store"
	"github.com/mistgate/mistgate/internal/panel/subsettings"
)

// What a friend's apps call the things they get: a country, never the panel's node names (de1, nl1) or profile names.

// CountryName is the localised name of a country code in lang ("ru", "en"); the code itself when unknown.
func CountryName(cc, lang string) string {
	cc = strings.ToUpper(strings.TrimSpace(cc))
	if cc == "" {
		return ""
	}
	r, err := language.ParseRegion(cc)
	if err != nil || !r.IsCountry() {
		return cc
	}
	if n := display.Regions(language.Make(lang)).Name(r); n != "" {
		return n
	}
	return cc
}

// keyNaming is what the AmneziaVPN keys of the instance are named after: the subscription title (the settings' title,
// else the brand), the brand (for file names) and the instance language (for the country). A read that fails leaves
// the defaults: a name is never worth failing a key over.
func (s *Service) keyNaming(ctx context.Context) (title, brand, lang string) {
	b, err := instance.Load(ctx, s.st)
	if err != nil {
		s.log.Warn("access: brand unreadable, keys are named after the defaults", "err", err)
	}
	title, brand, lang = b.BrandName(), b.BrandName(), b.Language
	if set, err := subsettings.Load(ctx, s.st); err == nil && set.GetTitle() != "" {
		title = set.GetTitle()
	}
	return title, brand, lang
}

// keyNames names the configs of one device, one per node in the given order: the connection AmneziaVPN lists
// ("Mistgate · Germany"; the node is added when the country has two: "Mistgate · Germany · de1") and the file
// ("mistgate-de.conf"; the second of a country is "mistgate-de-2.conf"). A node without a country goes by its name.
func keyNames(title, brand, lang string, nodes []store.AccessNode) (names, files []string) {
	perCountry := map[string]int{}
	for _, n := range nodes {
		perCountry[strings.ToUpper(n.CountryCode)]++
	}
	prefix := cmp.Or(slug(brand), "vpn")
	used := map[string]bool{}
	for _, n := range nodes {
		cc := strings.ToUpper(n.CountryCode)
		place, tag := CountryName(cc, lang), strings.ToLower(cc)
		if cc == "" {
			place, tag = n.Name, slug(n.Name)
		} else if perCountry[cc] > 1 {
			place += " · " + n.Name
		}
		name := clean(place)
		if t := clean(title); t != "" {
			name = t + " · " + name
		}
		names = append(names, name)
		base := prefix + "-" + cmp.Or(tag, "awg")
		file := base
		for k := 2; used[file]; k++ {
			file = base + "-" + strconv.Itoa(k)
		}
		used[file] = true
		files = append(files, file+".conf")
	}
	return names, files
}

// clean drops control characters and collapses runs of whitespace: a node or brand name cannot break a line.
func clean(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r <= ' ' || r == 0x7f || r == ' ' || r == ' ' }), " ")
}

// translit spells the Russian letters in Latin ones, so a Cyrillic brand still gives a readable file name.
var translit = strings.NewReplacer(
	"а", "a", "б", "b", "в", "v", "г", "g", "д", "d", "е", "e", "ё", "e", "ж", "zh", "з", "z", "и", "i", "й", "y",
	"к", "k", "л", "l", "м", "m", "н", "n", "о", "o", "п", "p", "р", "r", "с", "s", "т", "t", "у", "u", "ф", "f",
	"х", "h", "ц", "ts", "ч", "ch", "ш", "sh", "щ", "sch", "ъ", "", "ы", "y", "ь", "", "э", "e", "ю", "yu", "я", "ya",
)

// slug is a name reduced to lowercase ASCII letters, digits and single dashes (about 32 characters at most): a name is
// hostile input and must never reach a Content-Disposition header or a file system as it is.
func slug(s string) string {
	var b strings.Builder
	for _, r := range translit.Replace(strings.ToLower(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "-"):
			b.WriteByte('-')
		}
		if b.Len() >= 32 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
