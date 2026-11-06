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
	"unicode"
	"unicode/utf8"

	"golang.org/x/mod/semver"

	"github.com/mistgate/mistgate/internal/release"
)

// GitHubReleaseListURL lists the official repository's releases, newest first. Discovery and "What's new" share this
// conditionally fetched page.
const GitHubReleaseListURL = "https://api.github.com/repos/Mistgate/mistgate/releases?per_page=30"

// GitHubReleaseNotesURL remains the public name used by the application wiring.
const GitHubReleaseNotesURL = GitHubReleaseListURL

const (
	notesMaxReleases = 30       // the page size above; also what is kept
	notesMaxBody     = 64 << 10 // per release description
	notesMaxResponse = 4 << 20  // the whole listing, assets and all
)

type githubNotesRelease = githubRelease

// ReleaseNote is one channel-visible release's description, split by language. The text is INFORMATIONAL and not signed:
// unlike the manifests it is whatever the GitHub release page says, fetched over HTTPS from the official repository.
// Nothing is decided from it except a badge ("security"); the SPA renders it as text, never as HTML.
type ReleaseNote struct {
	Version       string
	Channel       release.Channel
	URL           string
	PublishedUnix int64
	En, Ru, Zh    string
	Security      bool
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
	for _, part := range []string{n.En, n.Ru, n.Zh} {
		n.Security = n.Security || securityHeading.MatchString(withoutFences(part))
	}
	return n
}

// withoutFences drops fenced code blocks (``` or ~~~), so a "## Security" in a code sample is no security section.
func withoutFences(s string) string {
	var b strings.Builder
	fence := ""
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case fence != "":
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
		case strings.HasPrefix(t, "```"), strings.HasPrefix(t, "~~~"):
			fence = t[:3]
		default:
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	return b.String()
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

// fetchGitHubReleaseList reads the first page of release metadata, conditionally when etag is available. The same list
// supplies both release discovery and notes, so an unchanged page costs one conditional request per checker.
func fetchGitHubReleaseList(ctx context.Context, client *http.Client, url, userAgent, etag string) (releases []githubRelease, newETag string, notModified bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
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
	case http.StatusNotFound:
		return nil, "", false, ErrNoPanelRelease
	case http.StatusOK:
	default:
		return nil, "", false, fmt.Errorf("GitHub releases API returned HTTP %d", resp.StatusCode)
	}
	list, err := decodeReleaseList(io.LimitReader(resp.Body, notesMaxResponse))
	if err != nil {
		return nil, "", false, fmt.Errorf("decode GitHub release list: %w", err)
	}
	return list, resp.Header.Get("ETag"), false, nil
}

func releaseNotesForChannel(list []githubRelease, channel release.Channel) []ReleaseNote {
	if !channel.Valid() {
		return nil
	}
	var notes []ReleaseNote
	for _, r := range list {
		if r.Draft || !release.ValidChannelTag(r.TagName) || !validReleasePage(r.HTMLURL, r.TagName) {
			continue
		}
		releaseChannel, ok := release.ChannelOf(r.TagName)
		if !ok || !channel.Allows(releaseChannel) {
			continue
		}
		n := parseReleaseBody(capBody(r.Body))
		n.Version, n.Channel, n.URL = r.TagName, releaseChannel, r.HTMLURL
		if !r.PublishedAt.IsZero() {
			n.PublishedUnix = r.PublishedAt.Unix()
		}
		notes = append(notes, n)
	}
	sort.SliceStable(notes, func(i, j int) bool { return semver.Compare(notes[i].Version, notes[j].Version) > 0 })
	if len(notes) > notesMaxReleases {
		notes = notes[:notesMaxReleases]
	}
	return notes
}

// fetchReleaseNotes is the stable-channel helper retained for callers that only need notes. Discovery uses the same
// underlying release-list fetch through fetchGitHubReleaseList.
func fetchReleaseNotes(ctx context.Context, client *http.Client, url, etag string) (notes []ReleaseNote, newETag string, notModified bool, err error) {
	list, newETag, notModified, err := fetchGitHubReleaseList(ctx, client, url, "Mistgate-release-notes", etag)
	if err != nil || notModified {
		return nil, newETag, notModified, err
	}
	return releaseNotesForChannel(list, release.ChannelStable), newETag, false, nil
}

func (u *GitHubPanelUpdater) fetchReleases(ctx context.Context, userAgent string) ([]githubRelease, error) {
	u.notesMu.Lock()
	etag, cached := u.releasesETag, u.releases
	u.notesMu.Unlock()

	list, newETag, notModified, err := fetchGitHubReleaseList(ctx, u.client, u.apiURL, userAgent, etag)
	if err != nil {
		return nil, err
	}
	if notModified {
		if len(cached) == 0 {
			list, newETag, _, err = fetchGitHubReleaseList(ctx, u.client, u.apiURL, userAgent, "")
			if err != nil {
				return nil, err
			}
		} else {
			list = cached
		}
	}
	notes := releaseNotesForChannel(list, u.channel())
	u.notesMu.Lock()
	u.releases, u.releasesETag = list, newETag
	u.notes = notes
	u.notesMu.Unlock()
	return list, nil
}

func latestGitHubRelease(list []githubRelease, channel release.Channel) (githubRelease, error) {
	tags := make([]string, 0, len(list))
	byTag := make(map[string]githubRelease, len(list))
	for _, r := range list {
		if r.Draft || !release.ValidChannelTag(r.TagName) {
			continue
		}
		tagChannel, ok := release.ChannelOf(r.TagName)
		if !ok || !channel.Allows(tagChannel) {
			continue
		}
		tags = append(tags, r.TagName)
		byTag[r.TagName] = r
	}
	tag, ok := release.LatestTagForChannel(tags, channel)
	if !ok {
		return githubRelease{}, ErrNoPanelRelease
	}
	r := byTag[tag]
	if !validReleasePage(r.HTMLURL, r.TagName) {
		return githubRelease{}, errors.New("GitHub returned a release with an invalid page URL")
	}
	return r, nil
}

// decodeReleaseList reads the first notesMaxReleases entries of a JSON array one by one and stops there: the page asks for
// that many, and an answer of many small entries (a hostile or broken one) must not be allocated whole before the cap.
func decodeReleaseList(r io.Reader) ([]githubRelease, error) {
	dec := json.NewDecoder(r)
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('[') {
		return nil, errors.New("not a JSON array")
	}
	var list []githubRelease
	for len(list) < notesMaxReleases && dec.More() {
		var rel githubRelease
		if err := dec.Decode(&rel); err != nil {
			return nil, err
		}
		list = append(list, rel)
	}
	return list, nil
}

// ReleaseNotes is the last list read from GitHub, newest first (empty until the first good read, and kept when a later
// read fails). It lives in memory only: the update check itself is not persisted either, and the next check (within
// minutes of a start) refills it.
func (u *GitHubPanelUpdater) ReleaseNotes() []ReleaseNote {
	u.notesMu.Lock()
	list := append([]githubRelease(nil), u.releases...)
	notes := append([]ReleaseNote(nil), u.notes...)
	u.notesMu.Unlock()
	if len(list) > 0 {
		return releaseNotesForChannel(list, u.channel())
	}
	return notes
}

// refreshNotes reads the release list after a check. A failure leaves the notes already known: they are a nicety, never a
// reason to report the update check as failed.
func (u *GitHubPanelUpdater) refreshNotes(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, panelUpdateCheckTimeout)
	defer cancel()
	if _, err := u.fetchReleases(ctx, "Mistgate-release-notes"); err != nil {
		if !errors.Is(err, context.Canceled) {
			u.log.Debug("read GitHub release notes", "err", err)
		}
	}
}

// gitDescribeSuffix is what git describe adds after a tag: commits since it and the hash, and "-dirty".
var gitDescribeSuffix = regexp.MustCompile(`(-[0-9]+-g[0-9a-f]+)?(-dirty)?$`)

// versionCore is the version a build stands at: a build from git describe ("v0.1.38-3-gabc1234") counts as the release it
// follows, and "0.1.38" as "v0.1.38". "" when it is not a version at all (a dev build says "dev").
func versionCore(v string) string {
	v = strings.TrimSpace(v)
	if v != "" && v[0] != 'v' {
		v = "v" + v
	}
	v = gitDescribeSuffix.ReplaceAllString(v, "") // a real prerelease ("-rc.1") stays one: it comes before its release
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
