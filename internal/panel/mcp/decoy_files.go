//go:build !js

package mcp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/mistgate/mistgate/internal/decoy"
)

// node_decoy takes an own site either as an archive (bundle_base64) or as a list of files. The list is turned into the
// archive the first form carries, then goes through the same decoy.ParseArchive, store and node check: the bundle
// rules are the decoy package's alone. What this file adds is only the checks a list of files from a model needs first.

const (
	maxDecoyFiles   = 64
	maxDecoyPathLen = 200
)

// decoyFileArg is one file of a site given as a list.
type decoyFileArg struct {
	Path          string `json:"path" jsonschema:"path from the site root, for example index.html or about/index.html: no leading slash, no .., no backslash, no empty part, at most 200 characters"`
	Content       string `json:"content,omitempty" jsonschema:"the file's text (UTF-8); give this or content_base64"`
	ContentBase64 string `json:"content_base64,omitempty" jsonschema:"for a small image or icon: the file's bytes in base64; give this or content"`
}

// ownBundle is the bundle an own-mode call carries and its archive. Exactly one of bundle_base64 and files must be given.
func (a nodeDecoyArgs) ownBundle() (*decoy.Bundle, []byte, error) {
	switch {
	case a.BundleBase64 != "" && len(a.Files) > 0:
		return nil, nil, errors.New("give either bundle_base64 or files, not both")
	case a.BundleBase64 == "" && len(a.Files) == 0:
		return nil, nil, errors.New("bundle_base64 or files is required for own mode")
	case len(a.Files) > 0:
		built, err := decoyFromFiles(a.Files)
		if err != nil {
			return nil, nil, err
		}
		archive := built.Archive()
		bundle, err := decoy.ParseArchive(archive)
		if err != nil {
			return nil, nil, errors.New(decoy.ClientMessage(err))
		}
		return bundle, archive, nil
	}
	archive, err := base64.StdEncoding.Strict().DecodeString(a.BundleBase64)
	if err != nil {
		return nil, nil, errors.New("bundle_base64 must encode a ZIP or TAR archive")
	}
	bundle, err := decoy.ParseArchive(archive)
	if err != nil {
		return nil, nil, errors.New(decoy.ClientMessage(err))
	}
	return bundle, archive, nil
}

// decoyFromFiles checks a list of files and builds its bundle. The archive is canonical (sorted paths, fixed times and
// modes), so the same files always give the same SHA-256 and a node that has the site is not sent it again.
func decoyFromFiles(in []decoyFileArg) (*decoy.Bundle, error) {
	if len(in) > maxDecoyFiles {
		return nil, fmt.Errorf("files: at most %d files, got %d", maxDecoyFiles, len(in))
	}
	files := make([]decoy.File, 0, len(in))
	lower := make(map[string]int, len(in))
	total := 0
	for i, f := range in {
		if err := checkDecoyPath(f.Path); err != nil {
			return nil, fmt.Errorf("files[%d] %q: %w", i, clean(f.Path, 60), err)
		}
		if j, dup := lower[strings.ToLower(f.Path)]; dup {
			return nil, fmt.Errorf("files[%d] %q repeats files[%d]: paths are compared ignoring case", i, clean(f.Path, 60), j)
		}
		lower[strings.ToLower(f.Path)] = i
		if (f.Content == "") == (f.ContentBase64 == "") {
			return nil, fmt.Errorf("files[%d] %q: give exactly one of content and content_base64, not empty", i, clean(f.Path, 60))
		}
		// the size is checked before anything is decoded, so a huge list costs no memory
		size := len(f.Content)
		if f.ContentBase64 != "" {
			size = base64.StdEncoding.DecodedLen(len(f.ContentBase64))
		}
		if total += size; total > decoy.MaxBundleBytes {
			return nil, fmt.Errorf("files: more than %d bytes in total", decoy.MaxBundleBytes)
		}
		content := []byte(f.Content)
		if f.ContentBase64 != "" {
			var err error
			if content, err = base64.StdEncoding.Strict().DecodeString(f.ContentBase64); err != nil {
				return nil, fmt.Errorf("files[%d] %q: content_base64 is not valid base64", i, clean(f.Path, 60))
			}
		}
		files = append(files, decoy.File{Path: f.Path, Content: content})
	}
	// a file where another needs a folder ("a" and "a/b"), also across case
	for i, f := range in {
		for dir := strings.ToLower(f.Path); ; {
			k := strings.LastIndexByte(dir, '/')
			if k < 0 {
				break
			}
			dir = dir[:k]
			if j, clash := lower[dir]; clash {
				return nil, fmt.Errorf("files[%d] is a file, but files[%d] needs it to be a folder", j, i)
			}
		}
	}
	b, err := decoy.FromFiles(files)
	if err != nil {
		return nil, errors.New(decoy.ClientMessage(err))
	}
	return b, nil
}

func checkDecoyPath(p string) error {
	switch {
	case p == "":
		return errors.New("the path is empty")
	case len(p) > maxDecoyPathLen:
		return fmt.Errorf("the path is longer than %d bytes", maxDecoyPathLen)
	case !utf8.ValidString(p):
		return errors.New("the path is not valid UTF-8")
	case strings.HasPrefix(p, "/"):
		return errors.New("the path must be relative to the site root: no leading slash")
	case strings.Contains(p, `\`):
		return errors.New("the path must use / and no backslash")
	}
	for _, r := range p {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Co, r) {
			return errors.New("the path has a control or invisible character")
		}
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "":
			return errors.New("the path has an empty part (a doubled or trailing slash)")
		case ".", "..":
			return errors.New("the path must not contain . or .. parts")
		}
	}
	if scrub(p) != p {
		return errors.New("the path looks like a key or a token; give the file a plain name")
	}
	return nil
}

// decoyFileFacts lists the files of a bundle, one line each, for the owner's approval card: paths and sizes, never contents.
func decoyFileFacts(b *decoy.Bundle) ([]Fact, error) {
	var list strings.Builder
	for _, f := range b.Files() {
		fmt.Fprintf(&list, "%s: %d bytes\n", f.Path, len(f.Content))
	}
	return approvalTextFacts("file_list", strings.TrimRight(list.String(), "\n"))
}

// spacedHash writes a SHA-256 in groups of eight, so the panel's scrubber does not take it for a token.
func spacedHash(h string) string {
	var b strings.Builder
	for i := 0; i < len(h); i += 8 {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(h[i:min(i+8, len(h))])
	}
	return b.String()
}
