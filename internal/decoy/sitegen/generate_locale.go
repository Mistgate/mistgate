package sitegen

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Countries and languages. A country (templates/shared/country.<cc>.json) is how a place writes dates, money and addresses and
// what its cities, streets and names are; a language (templates/shared/text.<lang>.json, and a text file in every kind) is the
// words. Several countries may share a language (de, at, ch) and a country has exactly one.

// DefaultCountry is the country a site is made for when the caller names none.
const DefaultCountry = "us"

var (
	// ErrUnknownCountry is the refusal of a country that has no data file.
	ErrUnknownCountry = errors.New("decoy: unknown country")
	// ErrNotInCountry is the refusal of a kind that has no text in the language of the country.
	ErrNotInCountry = errors.New("decoy: the kind has no texts for this country")
)

type cityDef struct {
	Name  string   `json:"name"`
	In    string   `json:"in"`    // the name after "in" where the language bends it (Russian); default is the name
	State string   `json:"state"` // the state or region, for the formats that print one
	Zip   []string `json:"zip"`   // the beginnings of the city's postcodes, one is drawn
	// Phone is the pattern of the city's numbers, X for a digit; "" means the city has none. Missing means the country's Phone.
	Phone *string `json:"phone"`
}

type moneyDef struct {
	Fmt   string  `json:"fmt"`   // "{n} €", "£{n}"
	Group string  `json:"group"` // the separator of thousands
	Scale float64 `json:"scale"` // a price in the kind's data is in dollars; this many of the country's units
	Round int     `json:"round"` // prices of 100 and more are rounded to a multiple of this
}

// countryDef is one data file of templates/shared.
type countryDef struct {
	Code  string `json:"-"`
	Order int    `json:"order"`
	Lang  string `json:"lang"`      // which text.<lang>.json the country reads
	HTML  string `json:"html_lang"` // the lang attribute of its pages
	// Date holds the formats of a date, one is drawn per site: {d} {dd} {m} (month of a date, as in "21 November") {mn} (month alone)
	// {mm} {y} {wd}.
	Date  []string `json:"date"`
	Clock string   `json:"clock"` // "24h" or "12h"
	Money moneyDef `json:"money"`
	// Address is the order of a postal address: {street} {no} {zip} {city} {state}.
	Address string `json:"address"`
	// ZipTail is what follows a city's postcode beginning: X is a digit, A a letter of ZipLetters, anything else stays.
	ZipTail    string `json:"zip_tail"`
	ZipLetters string `json:"zip_letters"`
	// Phone is the pattern of numbers for cities that have no pattern of their own. Only ranges set aside for made-up numbers
	// belong here; a country with none has no phone at all and its sites ask for a message in a form instead.
	Phone   string            `json:"phone"`
	Cities  []cityDef         `json:"cities"`
	Streets []string          `json:"streets"`
	First   []string          `json:"first"` // "Anna:f" is a woman's name; the others are a man's or neutral
	Last    []string          `json:"last"`  // "Ivanov/Ivanova": the man's form, the woman's form
	Replace map[string]string `json:"replace"`
	rep     *strings.Replacer
}

type locales struct {
	countries map[string]*countryDef
	order     []string
	shared    map[string]map[string]any // by language
}

var loadLocales = sync.OnceValues(func() (*locales, error) {
	files, err := fs.ReadDir(templateFS, "templates/shared")
	if err != nil {
		return nil, err
	}
	l := &locales{countries: map[string]*countryDef{}, shared: map[string]map[string]any{}}
	for _, f := range files {
		name := f.Name()
		if cc, ok := strings.CutPrefix(name, "country."); ok {
			cc, ok = strings.CutSuffix(cc, ".json")
			if !ok || cc == "" {
				continue
			}
			raw, err := fs.ReadFile(templateFS, "templates/shared/"+name)
			if err != nil {
				return nil, err
			}
			c := &countryDef{}
			if err := json.Unmarshal(raw, c); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			c.Code = cc
			if c.Lang == "" || c.HTML == "" || len(c.Date) == 0 || c.Address == "" || len(c.Cities) == 0 || len(c.Streets) == 0 || len(c.First) == 0 || len(c.Last) == 0 {
				return nil, fmt.Errorf("%s is incomplete", name)
			}
			if c.Clock != "12h" && c.Clock != "24h" {
				return nil, fmt.Errorf("%s: clock is 12h or 24h", name)
			}
			if c.Money.Fmt == "" || !strings.Contains(c.Money.Fmt, "{n}") {
				return nil, fmt.Errorf("%s: money.fmt needs {n}", name)
			}
			var pairs []string
			for k, v := range c.Replace {
				pairs = append(pairs, k, v)
			}
			if len(pairs) > 0 {
				c.rep = strings.NewReplacer(pairs...)
			}
			l.countries[cc] = c
			continue
		}
		if lang, ok := textLang(name); ok {
			raw, err := fs.ReadFile(templateFS, "templates/shared/"+name)
			if err != nil {
				return nil, err
			}
			m := map[string]any{}
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			l.shared[lang] = m
		}
	}
	for cc, c := range l.countries {
		if l.shared[c.Lang] == nil {
			return nil, fmt.Errorf("country %s: there is no shared text for the language %q", cc, c.Lang)
		}
		l.order = append(l.order, cc)
	}
	sort.Slice(l.order, func(i, j int) bool {
		a, b := l.countries[l.order[i]], l.countries[l.order[j]]
		if a.Order != b.Order {
			return a.Order < b.Order
		}
		return a.Code < b.Code
	})
	return l, nil
})

// CountryInfo is a country the generator knows.
type CountryInfo struct {
	Code string
	Lang string
	// Kinds are the kinds that have texts in the country's language.
	Kinds []string
}

// Countries lists the countries in the order of their "order" field, then by code.
func Countries() []CountryInfo {
	l, err := loadLocales()
	if err != nil {
		return nil
	}
	kinds, err := loadKinds()
	if err != nil {
		return nil
	}
	out := make([]CountryInfo, 0, len(l.order))
	for _, cc := range l.order {
		c := l.countries[cc]
		info := CountryInfo{Code: cc, Lang: c.Lang}
		for id, d := range kinds {
			if d.available(c.Lang) {
				info.Kinds = append(info.Kinds, id)
			}
		}
		sort.Strings(info.Kinds)
		out = append(out, info)
	}
	return out
}

// available is whether the kind has at least one variant written in the language.
func (d *kindDef) available(lang string) bool {
	lt := d.texts[lang]
	return lt != nil && len(lt.Bundles) > 0
}

func (l *locales) country(cc string) (*countryDef, error) {
	if cc == "" {
		cc = DefaultCountry
	}
	c := l.countries[cc]
	if c == nil {
		return nil, fmt.Errorf("%w %q", ErrUnknownCountry, cc)
	}
	return c, nil
}

// ---- merging the layers of a variant's texts

// copyValue is a deep copy of decoded JSON.
func copyValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = copyValue(e)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = copyValue(e)
		}
		return out
	}
	return v
}

// mergeText lays src over dst: objects are merged key by key, and two lists of objects of the same length are joined item by
// item (the prices of a list of services, their texts); anything else in src replaces what is in dst.
func mergeText(dst, src map[string]any) error {
	for k, v := range src {
		switch sv := v.(type) {
		case map[string]any:
			if dm, ok := dst[k].(map[string]any); ok {
				if err := mergeText(dm, sv); err != nil {
					return fmt.Errorf("%s: %w", k, err)
				}
				continue
			}
		case []any:
			if da, ok := dst[k].([]any); ok && objects(da) && objects(sv) {
				if len(da) != len(sv) {
					return fmt.Errorf("%s: the fixed data has %d items and the texts %d", k, len(da), len(sv))
				}
				for i := range sv {
					if err := mergeText(da[i].(map[string]any), sv[i].(map[string]any)); err != nil {
						return fmt.Errorf("%s[%d]: %w", k, i, err)
					}
				}
				continue
			}
		}
		dst[k] = copyValue(v)
	}
	return nil
}

func objects(l []any) bool {
	if len(l) == 0 {
		return false
	}
	for _, e := range l {
		if _, ok := e.(map[string]any); !ok {
			return false
		}
	}
	return true
}

// replaceText applies a country's text replacements (Swiss German writes "ss" for "ß") to every string.
func replaceText(rep *strings.Replacer, v any) any {
	if rep == nil {
		return v
	}
	switch x := v.(type) {
	case string:
		return rep.Replace(x)
	case []any:
		for i, e := range x {
			x[i] = replaceText(rep, e)
		}
	case map[string]any:
		for k, e := range x {
			x[k] = replaceText(rep, e)
		}
	}
	return v
}

// ---- what a country gives a site

// geo draws the place of a site from its country: a city, a street, the address, the phone and a name. The keys are the
// variables of the texts.
func (c *countryDef) geo(r *rng) map[string]string {
	city := c.Cities[r.sub("city").n(len(c.Cities))]
	street := c.Streets[r.sub("street").n(len(c.Streets))]
	no := fmt.Sprint(r.sub("no").between(1, 120))
	zip := ""
	if len(city.Zip) > 0 {
		zr := r.sub("zip")
		zip = city.Zip[zr.n(len(city.Zip))] + fillPattern(zr, c.ZipTail, c.ZipLetters)
	}
	in := city.In
	if in == "" {
		in = city.Name
	}
	phone := c.Phone
	if city.Phone != nil {
		phone = *city.Phone
	}
	first, last := c.person(r.sub("person"))
	v := map[string]string{
		"city": city.Name, "city_in": in, "state": city.State, "street": street, "no": no, "zip": zip,
		"phone": fillPattern(r.sub("phone"), phone, ""), "first": first, "last": last, "name": first + " " + last,
	}
	v["address"] = strings.NewReplacer("{street}", street, "{no}", no, "{zip}", zip, "{city}", city.Name, "{state}", city.State).Replace(c.Address)
	return v
}

// person is a first and a last name, the last in the form of the first name's sex.
func (c *countryDef) person(r *rng) (string, string) {
	first := c.First[r.sub("first").n(len(c.First))]
	woman := strings.HasSuffix(first, ":f")
	first = strings.TrimSuffix(strings.TrimSuffix(first, ":f"), ":m")
	last := c.Last[r.sub("last").n(len(c.Last))]
	if i := strings.IndexByte(last, '/'); i >= 0 {
		if woman {
			last = last[i+1:]
		} else {
			last = last[:i]
		}
	}
	return first, last
}

// fillPattern draws the X (a digit) and A (a letter of letters, A to Z when none) of a pattern.
func fillPattern(r *rng, pat, letters string) string {
	if letters == "" {
		letters = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	}
	var b strings.Builder
	for _, ch := range pat {
		switch ch {
		case 'X':
			b.WriteByte(byte('0' + r.n(10)))
		case 'A':
			b.WriteByte(letters[r.n(len(letters))])
		default:
			b.WriteRune(ch)
		}
	}
	return b.String()
}

// money is a price in the country's money: the dollars of the kind's data times the country's scale, rounded, with the
// country's separators and sign.
func (c *countryDef) money(base float64) string {
	scale := c.Money.Scale
	if scale == 0 {
		scale = 1
	}
	x := base * scale
	if step := float64(c.Money.Round); x >= 100 && step > 1 {
		x = math.Round(x/step) * step
	} else {
		x = math.Round(x)
	}
	n := strconv.Itoa(int(x))
	if g := c.Money.Group; g != "" {
		for i := len(n) - 3; i > 0; i -= 3 {
			n = n[:i] + g + n[i:]
		}
	}
	return strings.Replace(c.Money.Fmt, "{n}", n, 1)
}

// price is money for a shop's or a service's price list: a small sum (under 30 of the country's units) keeps its pence or cents,
// to the nearest 5, as "£4.80" or "4,80 €" (a whole sum has none), because a bakery's price rounded to a whole unit looks made up; a bigger one is money.
func (c *countryDef) price(base float64) string {
	scale := c.Money.Scale
	if scale == 0 {
		scale = 1
	}
	x := base * scale
	if x >= 30 || x < 0 {
		return c.money(base)
	}
	cents := int(math.Round(x*100/5)) * 5
	mark := ","
	if strings.HasPrefix(c.Lang, "en") || c.Code == "ch" {
		mark = "."
	}
	n := fmt.Sprintf("%d%s%02d", cents/100, mark, cents%100)
	if cents%100 == 0 { // "£24", not "£24.00"
		n = strconv.Itoa(cents / 100)
	}
	return strings.Replace(c.Money.Fmt, "{n}", n, 1)
}

// clock is a time of day, "08:30", the way the country writes it: 8:30, or 8:30 am.
func (c *countryDef) clock(hhmm string, am, pm string) string {
	var h, m int
	if _, err := fmt.Sscanf(hhmm, "%d:%d", &h, &m); err != nil {
		return hhmm
	}
	if c.Clock == "12h" {
		suffix := am
		if h >= 12 {
			suffix = pm
		}
		h12 := h % 12
		if h12 == 0 {
			h12 = 12
		}
		if m == 0 {
			return fmt.Sprintf("%d %s", h12, suffix)
		}
		return fmt.Sprintf("%d:%02d %s", h12, m, suffix)
	}
	return fmt.Sprintf("%d:%02d", h, m)
}

// shapeOf describes the keys of decoded JSON for the test that every language has the same ones: a list is described by its first
// item, and an error says when its items differ.
func shapeOf(v any) (string, error) {
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var b strings.Builder
		b.WriteByte('{')
		for _, k := range keys {
			if k == "sep" { // the separator of a joined name is optional
				continue
			}
			s, err := shapeOf(x[k])
			if err != nil {
				return "", fmt.Errorf("%s: %w", k, err)
			}
			b.WriteString(k + ":" + s + ",")
		}
		b.WriteByte('}')
		return b.String(), nil
	case []any:
		if len(x) == 0 {
			return "", errors.New("an empty list")
		}
		first, err := shapeOf(x[0])
		if err != nil {
			return "", err
		}
		for i, e := range x[1:] {
			s, err := shapeOf(e)
			if err != nil {
				return "", err
			}
			if s != first {
				return "", fmt.Errorf("item %d has other keys than item 0", i+1)
			}
		}
		return "[" + first + "]", nil
	}
	return "s", nil
}
