//go:build unix

package httpserver

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByCurrentUser reports whether the file belongs to the user running the
// process.
func ownedByCurrentUser(fi fs.FileInfo) bool {
	st, ok := fi.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// writableByOthers reports whether group or world can write to the directory.
func writableByOthers(fi fs.FileInfo) bool { return fi.Mode()&0o022 != 0 }
