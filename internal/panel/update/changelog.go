package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"
)

// GitHubReleaseNotesURL lists the official repository's releases, newest first. The panel reads it on the schedule of the
// release check, with a conditional request, to show "What's new" on the Updates page.
const GitHubReleaseNotesURL = "https://api.github.com/repos/Mistgate/mistgate/releases?per_page=30"

const (
	notesMaxReleases = 30       // the page size above; also what is kept
	notesMaxBody     = 64 << 10 // per release description
	notesMaxResponse = 4 << 20  // the whole listing, assets and all
)

// ReleaseNote is one stable release's description, split by language. The text is INFORMATIONAL and not signed: unlike the
// manifests it is whatever the GitHub release page says, fetched over HTTPS from the official repository. Nothing is
// decided from it except a badge ("security"); the SPA renders it as text, never as HTML.
type ReleaseNote struct {
	Version       string
	URL           string
	PublishedUnix int64
	En, Ru, Zh    string
	Security      bool
}

type githubNotesRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	PublishedAt time.Time `json:"published_at"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	Body        string    `json:"body"`
}

// securityHeading matches a markdown heading of a security section in any of the three languages ("## Security",
// "### Security fixes", "## Безопасность", "## 安全修复"). Only headings count: a bullet that mentions security does not.
var securityHeading = regexp.MustCompile(`(?mi)^#{1,6}[ \t]*[*_]*[ \t]*(security|безопасност|安全)`)

// fullChangelogLine is the last line GitHub's generated notes add; the page links the release itself instead.
var fullChangelogLine = regexp.MustCompile(`(?i)^\*{0,2}full changelog\*{0,2}\s*:`)

var (
	codeSpan  = regexp.MustCompile("`[^`]*`")
	linkAndTL = regexp.MustCompile(`\]\([^)]*\)|https?://\S+`)
)

// parseReleaseBody splits a release description into its languages. The parts are separated by lines that are exactly
// "---"; each part is given the language of its script (Cyrillic ru, Han zh, otherwise en), not of its place, so a release
// that has only a Russian part or three parts in another order still lands right. Two parts of one language are joined.
func parseReleaseBody(body string) (n ReleaseNote) {
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	var parts []string
	var cur []string
	flush := func() {
		parts = append(parts, strings.Join(cur, "\n"))
		cur = nil
	}
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.TrimRight(line, " \t") == "---":
			flush()
		case fullChangelogLine.MatchString(strings.TrimSpace(line)):
		default:
			cur = append(cur, line)
		}
	}
	flush()
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		dst := &n.En
		switch scriptOf(p) {
		case "ru":
			dst = &n.Ru
		case "zh":
			dst = &n.Zh
		}
		if *dst != "" {
			*dst += "\n\n"
		}
		*dst += p
	}
	n.Security = securityHeading.MatchString(n.En) || securityHeading.MatchString(n.Ru) || securityHeading.MatchString(n.Zh)
	return n
}

// scriptOf names the language of a text by its dominant script, ignoring code, links and URLs. A Han character weighs four
// letters: a Chinese text with product names in Latin letters is still Chinese, an English text that mentions 简体中文 is not.
func scriptOf(s string) string {
	s = linkAndTL.ReplaceAllString(codeSpan.ReplaceAllString(s, ""), "")
	var han, cyr, lat int
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Han, r):
			han++
		case unicode.Is(unicode.Cyrillic, r):
			cyr++
		case unicode.Is(unicode.Latin, r):
			lat++
		}
	}
	switch {
	case han*4 > cyr && han*4 > lat:
		return "zh"
	case cyr > lat:
		return "ru"
	}
	return "en"
}

// capBody keeps at most notesMaxBody bytes of a description, cut on a character boundary.
func capBody(s string) string {
	if len(s) <= notesMaxBody {
		return s
	}
	s = s[:notesMaxBody]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// fetchReleaseNotes reads the release list. etag is the validator of the last answer ("" = none): GitHub answers 304
// to a request that carries it when nothing changed, and that answer does not count against the unauthenticated quota.
// notModified is true then, and notes is nil. Drafts, prereleases and tags that are not a plain vX.Y.Z are skipped.
func fetchReleaseNotes(ctx context.Context, client *http.Client, url, etag string) (notes []ReleaseNote, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "Mistgate-release-notes")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil, etag, true, nil
	case http.StatusOK:
	default:
		return nil, "", false, fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}
	var list []githubNotesRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, notesMaxResponse)).Decode(&list); err != nil {
		return nil, "", false, fmt.Errorf("decode GitHub release list: %w", err)
	}
	for _, r := range list {
		if r.Draft || r.Prerelease || !validReleaseTag(r.TagName) || !validReleasePage(r.HTMLURL, r.TagName) {
			continue
		}
		n := parseReleaseBody(capBody(r.Body))
		n.Version, n.URL = r.TagName, r.HTMLURL
		if !r.PublishedAt.IsZero() {
			n.PublishedUnix = r.PublishedAt.Unix()
		}
		notes = append(notes, n)
	}
	sort.SliceStable(notes, func(i, j int) bool { return semver.Compare(notes[i].Version, notes[j].Version) > 0 })
	if len(notes) > notesMaxReleases {
		notes = notes[:notesMaxReleases]
	}
	return notes, resp.Header.Get("ETag"), false, nil
}

// ReleaseNotes is the last list read from GitHub, newest first (empty until the first good read, and kept when a later
// read fails). It lives in memory only: the update check itself is not persisted either, and the next check (within
// minutes of a start) refills it.
func (u *GitHubPanelUpdater) ReleaseNotes() []ReleaseNote {
	u.notesMu.Lock()
	defer u.notesMu.Unlock()
	return u.notes
}

// refreshNotes reads the release list after a check. A failure leaves the notes already known: they are a nicety, never a
// reason to report the update check as failed.
func (u *GitHubPanelUpdater) refreshNotes(ctx context.Context) {
	if u.cfg.NotesURL == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	defer cancel()
	u.notesMu.Lock()
	etag := u.notesETag
	u.notesMu.Unlock()
	notes, newETag, same, err := fetchReleaseNotes(ctx, u.client, u.cfg.NotesURL, etag)
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			u.log.Debug("read GitHub release notes", "err", err)
		}
		return
	}
	if same {
		return
	}
	u.notesMu.Lock()
	u.notes, u.notesETag = notes, newETag
	u.notesMu.Unlock()
}

// versionCore is the vX.Y.Z of a version: a build from git describe ("v0.1.38-3-gabc1234") counts as the release it
// follows, and "0.1.38" as "v0.1.38". "" when it is not a version at all (a dev build says "dev").
func versionCore(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && v[0] != 'v' {
		v = "v" + v
	}
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	if !semver.IsValid(v) {
		return ""
	}
	return semver.Canonical(v)
}

// notesBetween picks the notes of the releases newer than after and not newer than upTo, newest first (all is already
// newest first). Either end that is not a version yields nothing: a panel that does not know where it stands cannot say
// what is new.
func notesBetween(all []ReleaseNote, after, upTo string) []ReleaseNote {
	after, upTo = versionCore(after), versionCore(upTo)
	if after == "" || upTo == "" {
		return nil
	}
	var out []ReleaseNote
	for _, n := range all {
		if semver.Compare(n.Version, after) > 0 && semver.Compare(n.Version, upTo) <= 0 {
			out = append(out, n)
		}
	}
	return out
}

// oldestVersion is the lowest version among the given ones that are versions; "" when none is.
func oldestVersion(versions []string) string {
	oldest := ""
	for _, v := range versions {
		c := versionCore(v)
		if c != "" && (oldest == "" || semver.Compare(c, oldest) < 0) {
			oldest = c
		}
	}
	return oldest
}
