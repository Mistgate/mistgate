// Package instance holds the settings of this panel installation that are visible to
// its users: the brand (a two-part wordmark and an optional logo), the accent colour
// and the default language. They live in the setting table; the admin API and the
// sign-in page read them through here. Values are validated on the way in, so what is
// stored can be rendered as is.
package instance

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/panel/store"
)

// Setting keys in the setting table.
const (
	keyBrandHead = "brand_head"
	keyBrandTail = "brand_tail"
	keyAccent    = "accent"
	keyLanguage  = "language"
	keyLogo      = "logo_svg"
)

// SettingKeys lists the keys that make up Settings.
func SettingKeys() []string {
	return []string{keyBrandHead, keyBrandTail, keyAccent, keyLanguage, keyLogo}
}

// Defaults of a fresh installation: brand "Mistgate" (wordmark "mist" + "gate"), no
// custom logo, lavender accent, English.
const (
	DefaultBrandHead = "Mist"
	DefaultBrandTail = "gate"
	DefaultAccent    = "#b8acf2"
	DefaultLanguage  = "en"

	maxBrandPart = 24 // runes per wordmark part
)

// Languages are the UI languages the SPA ships.
var Languages = []string{"en", "ru"}

// ErrInvalid wraps every validation failure, so callers can map it to INVALID_ARGUMENT.
var ErrInvalid = errors.New("invalid instance setting")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Settings are the stored, already validated values.
type Settings struct {
	BrandHead string // first part of the wordmark, shown in the text colour
	BrandTail string // second part, shown in the accent colour; may be empty
	Accent    string // "#rrggbb", lower case
	Language  string // default UI language, one of Languages
	LogoSVG   string // sanitised SVG, "" = none (the UI shows its built-in mark)
}

// BrandName is the wordmark as one string ("Mistgate"), for page titles and the
// authenticator app's issuer label.
func (s Settings) BrandName() string { return s.BrandHead + s.BrandTail }

// Defaults returns the settings of a fresh installation.
func Defaults() Settings {
	return Settings{BrandHead: DefaultBrandHead, BrandTail: DefaultBrandTail, Accent: DefaultAccent, Language: DefaultLanguage}
}

// FromValues applies the stored values to the installation defaults. Missing keys keep their defaults.
func FromValues(values map[string]string) Settings {
	s := Defaults()
	for key, dst := range map[string]*string{
		keyBrandHead: &s.BrandHead, keyBrandTail: &s.BrandTail, keyAccent: &s.Accent,
		keyLanguage: &s.Language, keyLogo: &s.LogoSVG,
	} {
		if value, ok := values[key]; ok {
			*dst = value
		}
	}
	return s
}

// Load reads the settings, falling back to the default for every unset key.
func Load(ctx context.Context, st *store.Store) (Settings, error) {
	values, err := st.SettingValues(ctx, SettingKeys())
	if err != nil {
		return Defaults(), err
	}
	return FromValues(values), nil
}

// Patch is a partial update: a nil field is left unchanged. LogoSVG "" removes the logo.
type Patch struct {
	BrandHead, BrandTail, Accent, Language, LogoSVG *string
}

var accentRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Update validates the patch, applies it on top of the stored settings, saves the
// result and returns it. A rejected patch changes nothing.
func Update(ctx context.Context, st *store.Store, p Patch) (Settings, error) {
	s, err := Load(ctx, st)
	if err != nil {
		return s, err
	}
	if p.BrandHead != nil {
		s.BrandHead = strings.TrimSpace(*p.BrandHead)
	}
	if p.BrandTail != nil {
		s.BrandTail = strings.TrimSpace(*p.BrandTail)
	}
	if err := validBrand(s.BrandHead, s.BrandTail); err != nil {
		return s, err
	}
	if p.Accent != nil {
		if !accentRe.MatchString(*p.Accent) {
			return s, invalid("accent must look like #rrggbb")
		}
		s.Accent = strings.ToLower(*p.Accent)
	}
	if p.Language != nil {
		ok := false
		for _, l := range Languages {
			ok = ok || l == *p.Language
		}
		if !ok {
			return s, invalid("language must be one of %s", strings.Join(Languages, ", "))
		}
		s.Language = *p.Language
	}
	if p.LogoSVG != nil {
		if s.LogoSVG = ""; *p.LogoSVG != "" {
			if s.LogoSVG, err = SanitizeSVG(*p.LogoSVG); err != nil {
				return s, err
			}
		}
	}
	err = st.SetSettings(ctx, map[string]string{
		keyBrandHead: s.BrandHead, keyBrandTail: s.BrandTail, keyAccent: s.Accent,
		keyLanguage: s.Language, keyLogo: s.LogoSVG,
	})
	return s, err
}

// validBrand keeps the wordmark to letters, digits, spaces and - . _ : it ends up in
// page titles and the otpauth:// label, and there is no reason for more.
func validBrand(head, tail string) error {
	if head == "" {
		return invalid("brand name is empty")
	}
	for _, part := range []string{head, tail} {
		if utf8.RuneCountInString(part) > maxBrandPart {
			return invalid("each part of the brand name is at most %d characters", maxBrandPart)
		}
		for _, r := range part {
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != ' ' && r != '-' && r != '.' && r != '_' {
				return invalid("brand name may contain letters, digits, space and - . _ only")
			}
		}
	}
	return nil
}
