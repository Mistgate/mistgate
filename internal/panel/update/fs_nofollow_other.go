//go:build !unix && !js

package update

import "os"

func lchownPath(string, int, int) error      { return nil }
func chownOpenFile(*os.File, int, int) error { return nil }
