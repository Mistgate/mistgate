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
	b, err := wholeDecoySite(a.BundleBase64, a.Files, "bundle_base64 or files is required for own mode")
	if err != nil {
		return nil, nil, err
	}
	return b, b.Archive(), nil
}

// wholeDecoySite is the site a call gives as an archive (base64) or as a list of files; missing is the refusal for neither.
func wholeDecoySite(bundleBase64 string, files []decoyFileArg, missing string) (*decoy.Bundle, error) {
	switch {
	case bundleBase64 != "" && len(files) > 0:
		return nil, errors.New("give either bundle_base64 or files, not both")
	case bundleBase64 == "" && len(files) == 0:
		return nil, errors.New(missing)
	case len(files) > 0:
		return decoyFromFiles(files)
	}
	archive, err := base64.StdEncoding.Strict().DecodeString(bundleBase64)
	if err != nil {
		return nil, errors.New("bundle_base64 must encode a ZIP or TAR archive")
	}
	bundle, err := decoy.ParseArchive(archive)
	if err != nil {
		return nil, errors.New(decoy.ClientMessage(err))
	}
	return bundle, nil
}

// decoyFromFiles checks a list of files and builds its bundle. The archive is canonical (sorted paths, fixed times and
// modes), so the same files always give the same SHA-256 and a node that has the site is not sent it again.
func decoyFromFiles(in []decoyFileArg) (*decoy.Bundle, error) {
	files, err := decodeDecoyFiles(in, "files")
	if err != nil {
		return nil, err
	}
	return bundleOfDecoyFiles(files)
}

// decodeDecoyFiles checks the paths and sizes of a list as given and decodes the contents; label names the list in a refusal.
func decodeDecoyFiles(in []decoyFileArg, label string) ([]decoy.File, error) {
	if len(in) > maxDecoyFiles {
		return nil, fmt.Errorf("%s: at most %d files, got %d", label, maxDecoyFiles, len(in))
	}
	files := make([]decoy.File, 0, len(in))
	total := 0
	for i, f := range in {
		if err := checkDecoyPath(f.Path); err != nil {
			return nil, fmt.Errorf("%s[%d] %q: %w", label, i, clean(f.Path, 60), err)
		}
		if (f.Content == "") == (f.ContentBase64 == "") {
			return nil, fmt.Errorf("%s[%d] %q: give exactly one of content and content_base64, not empty", label, i, clean(f.Path, 60))
		}
		// the size is checked before anything is decoded, so a huge list costs no memory
		size := len(f.Content)
		if f.ContentBase64 != "" {
			size = base64.StdEncoding.DecodedLen(len(f.ContentBase64))
		}
		if total += size; total > decoy.MaxBundleBytes {
			return nil, fmt.Errorf("%s: more than %d bytes in total", label, decoy.MaxBundleBytes)
		}
		content := []byte(f.Content)
		if f.ContentBase64 != "" {
			var err error
			if content, err = base64.StdEncoding.Strict().DecodeString(f.ContentBase64); err != nil {
				return nil, fmt.Errorf("%s[%d] %q: content_base64 is not valid base64", label, i, clean(f.Path, 60))
			}
		}
		files = append(files, decoy.File{Path: f.Path, Content: content})
	}
	return files, nil
}

// bundleOfDecoyFiles builds the bundle of a whole site's files: the paths must not repeat (ignoring case) or ask a file to be a
// folder, then the bundle rules of the decoy package apply.
func bundleOfDecoyFiles(files []decoy.File) (*decoy.Bundle, error) {
	lower := make(map[string]string, len(files))
	for _, f := range files {
		k := strings.ToLower(f.Path)
		if other, dup := lower[k]; dup {
			return nil, fmt.Errorf("%q repeats %q: paths are compared ignoring case", clean(f.Path, 60), clean(other, 60))
		}
		lower[k] = f.Path
	}
	// a file where another needs a folder ("a" and "a/b"), also across case
	for _, f := range files {
		for dir := strings.ToLower(f.Path); ; {
			k := strings.LastIndexByte(dir, '/')
			if k < 0 {
				break
			}
			dir = dir[:k]
			if file, clash := lower[dir]; clash {
				return nil, fmt.Errorf("%q is a file, but %q needs it to be a folder", clean(file, 60), clean(f.Path, 60))
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
