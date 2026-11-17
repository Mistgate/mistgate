//go:build js

package httpserver

import "github.com/mistgate/mistgate/internal/panel/store"

// The edge keeps no library of decoy sites: its panel shows --decoy-dir or the built-in page.
func newLibrary(*store.Store) librarySource { return nil }
