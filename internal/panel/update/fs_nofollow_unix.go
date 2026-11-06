//go:build unix && !js

package update

import "os"

func lchownPath(path string, uid, gid int) error { return os.Lchown(path, uid, gid) }

func chownOpenFile(file *os.File, uid, gid int) error { return file.Chown(uid, gid) }
