// Package web embeds the built admin SPA (web/dist) into the panel binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// Dist returns the SPA build output rooted at dist/. It may hold only .gitkeep
// when the SPA has not been built; callers must cope with a missing index.html.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // the embed directive guarantees dist exists
	}
	return sub
}
