//go:build !unix

package httpserver

import "io/fs"

// Outside unix the file mode carries no owner or group/world permissions, and
// the per-user directory name (see userTag) keeps users apart; the symlink
// check still applies.
func ownedByCurrentUser(fs.FileInfo) bool { return true }

func writableByOthers(fs.FileInfo) bool { return false }
