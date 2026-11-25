// Command decoyfixtures writes real template sites as the panel's preview would send them (one self-contained HTML document
// each), for the dev-kit scenes of the "From a template" window (web/src/screens/dev-kit-fixtures/sites). Development only.
//
//	go run ./tools/decoyfixtures <out dir> "<countries>" "<kinds>" [variants]
//
// The seed of a site is "fixture-<country>-<kind>-<n>", so the same call writes the same sites.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mistgate/mistgate/internal/decoy"
	"github.com/mistgate/mistgate/internal/decoy/sitegen"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, `usage: decoyfixtures <out dir> "<countries>" "<kinds>" [variants]`)
		os.Exit(2)
	}
	variants := 2
	if len(os.Args) > 4 {
		variants, _ = strconv.Atoi(os.Args[4])
	}
	for _, cc := range strings.Fields(os.Args[2]) {
		for _, kind := range strings.Fields(os.Args[3]) {
			for n := 1; n <= variants; n++ {
				files, brand, err := sitegen.GenerateWith(kind, []byte(fmt.Sprintf("fixture-%s-%s-%d", cc, kind, n)), sitegen.Options{Country: cc})
				if err != nil {
					fatal(cc, kind, n, err)
				}
				b, err := decoy.FromFiles(files)
				if err != nil {
					fatal(cc, kind, n, err)
				}
				doc, err := decoy.PreviewDocument(b, "")
				if err != nil {
					fatal(cc, kind, n, err)
				}
				dir := filepath.Join(os.Args[1], cc)
				if err := os.MkdirAll(dir, 0o755); err != nil {
					fatal(cc, kind, n, err)
				}
				if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%s-%d.html", kind, n)), []byte(doc), 0o644); err != nil {
					fatal(cc, kind, n, err)
				}
				fmt.Printf("%s %s %d %q %d KB\n", cc, kind, n, brand, len(doc)/1024)
			}
		}
	}
}

func fatal(cc, kind string, n int, err error) {
	fmt.Fprintf(os.Stderr, "%s %s %d: %v\n", cc, kind, n, err)
	os.Exit(1)
}
