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

// What a friend's apps and page call the things they get: a country, never the panel's node names (de1, nl1) or profile names.

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
	keys := append(instance.SettingKeys(), subsettings.Key)
	values, err := s.st.SettingValues(ctx, keys)
	if err != nil {
		s.log.Warn("access: brand unreadable, keys are named after the defaults", "err", err)
	}
	b := instance.Defaults()
	if err == nil {
		b = instance.FromValues(values)
	}
	title, brand, lang = b.BrandName(), b.BrandName(), b.Language
	if err == nil {
		if raw, ok := values[subsettings.Key]; ok {
			if set, parseErr := subsettings.Parse(raw); parseErr == nil && set.GetTitle() != "" {
				title = set.GetTitle()
			}
		}
	} else if set, loadErr := subsettings.Load(ctx, s.st); loadErr == nil && set.GetTitle() != "" {
		title = set.GetTitle()
	}
	return title, brand, lang
}

// ServerLabeler names servers for the people who use them, one call per node in order: the country and the location
// ("Germany · Frankfurt"), "Server" for a node with neither, and a number where a name repeats ("Germany 2"). Never
// the panel's node name. The user page's channel card and the AmneziaVPN keys name servers this way.
func ServerLabeler(lang string) func(cc, location string) string {
	taken := map[string]bool{}
	return func(cc, location string) string {
		name := CountryName(cc, lang)
		if place := clean(location); place != "" && place != name {
			name = strings.TrimPrefix(name+" · "+place, " · ")
		}
		if name == "" {
			name = "Server"
			if lang == "ru" {
				name = "Сервер"
			}
		}
		unique := name
		for k := 2; taken[unique]; k++ {
			unique = name + " " + strconv.Itoa(k)
		}
		taken[unique] = true
		return unique
	}
}

// keyNames names the configs of one device, one per node in the given order: the server (ServerLabeler: "Germany",
// "Germany · Frankfurt", "Germany 2"), the connection AmneziaVPN lists ("Mistgate · Germany") and the file
// ("mistgate-de.conf"; the second of a country is "mistgate-de-2.conf"; a node without a country is named by its
// location, else "awg"). The panel's node name is in none of them: the user page shows these.
func keyNames(title, brand, lang string, nodes []store.AccessNode) (servers, names, files []string) {
	label := ServerLabeler(lang)
	prefix := cmp.Or(slug(brand), "vpn")
	used := map[string]bool{}
	for _, n := range nodes {
		server := label(n.CountryCode, n.Location)
		servers = append(servers, server)
		name := server
		if t := clean(title); t != "" {
			name = t + " · " + name
		}
		names = append(names, name)
		base := prefix + "-" + cmp.Or(strings.ToLower(strings.TrimSpace(n.CountryCode)), slug(n.Location), "awg")
		file := base
		for k := 2; used[file]; k++ {
			file = base + "-" + strconv.Itoa(k)
		}
		used[file] = true
		files = append(files, file+".conf")
	}
	return servers, names, files
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
