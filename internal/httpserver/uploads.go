package httpserver

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

// S3: /convert uploads are staged under a per-user directory,
// <os.TempDir()>/gleann-plugin-sound-<user>/port-<port>/. Each listening port
// gets its own subdir, so a startup sweep can delete the leftovers of a
// previously SIGKILLed instance of this port without ever touching the
// in-flight uploads of an instance running on another port.
//
// The per-user root keeps another local user from pre-creating the shared
// parent. Before any use both the root and the port dir are Lstat-checked:
// each must be a real directory (not a symlink), owned by the current user and
// not writable by group or others. If a check fails the failure is logged, the
// startup sweep is skipped, and uploads are staged in a fresh private
// os.MkdirTemp directory (removed on shutdown) instead of the untrusted one.

// uploadRoot is the base of the staging area; a variable so tests point it
// at t.TempDir() and never touch the real temp dir.
var uploadRoot = filepath.Join(os.TempDir(), PluginName+"-"+userTag())

// uploadPrefix is the name prefix of every staged upload; the sweep deletes
// nothing else.
const uploadPrefix = "upload-"

// userTag names the current user in the staging root: the numeric uid, or the
// sanitised user name where there is no uid (Windows).
func userTag() string {
	if uid := os.Getuid(); uid >= 0 {
		return strconv.Itoa(uid)
	}
	name := "user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
}

// uploadDir is this server's intended staging directory.
func (s *Server) uploadDir() string {
	return filepath.Join(uploadRoot, fmt.Sprintf("port-%d", s.port))
}

// checkPrivateDir refuses a directory that is a symlink, is not a directory,
// belongs to someone else or is writable by group or others.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&os.ModeSymlink != 0:
		return fmt.Errorf("%s is a symbolic link", dir)
	case !fi.IsDir():
		return fmt.Errorf("%s is not a directory", dir)
	case !ownedByCurrentUser(fi):
		return fmt.Errorf("%s belongs to another user", dir)
	case writableByOthers(fi):
		return fmt.Errorf("%s is writable by other users", dir)
	}
	return nil
}

// ensurePrivateDir creates dir (mode 0700, never through a symlink) when it
// does not exist and then applies checkPrivateDir.
func ensurePrivateDir(dir string) error {
	if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	return checkPrivateDir(dir)
}

// ensureUploadDir returns the verified private staging dir, creating the root
// and the port dir when missing.
func (s *Server) ensureUploadDir() (string, error) {
	root := uploadRoot
	if err := os.MkdirAll(filepath.Dir(root), 0o700); err != nil {
		return "", fmt.Errorf("staging root: %w", err)
	}
	if err := ensurePrivateDir(root); err != nil {
		return "", fmt.Errorf("staging root: %w", err)
	}
	dir := s.uploadDir()
	if err := ensurePrivateDir(dir); err != nil {
		return "", fmt.Errorf("staging dir: %w", err)
	}
	return dir, nil
}

// stagingDir returns where the next upload goes: the verified per-user port
// dir, or, when that fails its checks, a private temp dir of its own.
func (s *Server) stagingDir() (string, error) {
	dir, err := s.ensureUploadDir()
	if err == nil {
		return dir, nil
	}
	s.fallbackMu.Lock()
	defer s.fallbackMu.Unlock()
	if s.fallbackDir == "" {
		d, merr := os.MkdirTemp("", PluginName+"-uploads-")
		if merr != nil {
			return "", fmt.Errorf("%v; private fallback: %w", err, merr)
		}
		log.Printf("[plugin-serve] upload staging unsafe (%v); using private %s", err, d)
		s.fallbackDir = d
	}
	return s.fallbackDir, nil
}

// removeFallback deletes the private fallback staging dir, if one was made.
func (s *Server) removeFallback() {
	s.fallbackMu.Lock()
	d := s.fallbackDir
	s.fallbackDir = ""
	s.fallbackMu.Unlock()
	if d != "" {
		_ = os.RemoveAll(d) // does not follow symlinks
	}
}

// sweepUploads deletes leftover upload-* regular files in this port's own
// staging directory. It runs exactly once per start, right after net.Listen
// has succeeded: the port is ours now, so nothing in that directory can belong
// to a live process. It refuses (and logs) when the root or the port dir fails
// checkPrivateDir, never follows symlinks, never recurses, and deletes only
// regular files named upload-*. Sibling port-<n> directories are never touched.
func (s *Server) sweepUploads() {
	for _, d := range []string{uploadRoot, s.uploadDir()} {
		if err := checkPrivateDir(d); err != nil {
			if !errors.Is(err, fs.ErrNotExist) {
				log.Printf("[plugin-serve] upload sweep skipped: %v", err)
			}
			return
		}
	}
	dir := s.uploadDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.Type().IsRegular() || !strings.HasPrefix(e.Name(), uploadPrefix) {
			continue // symlinks, dirs, devices and foreign names are left alone
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[plugin-serve] upload sweep: %v", err)
		}
	}
}

// uploadSet tracks the /convert temp files this server created and has not
// removed yet.
type uploadSet struct {
	mu sync.Mutex
	m  map[string]struct{}
}

func (u *uploadSet) add(path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil {
		u.m = make(map[string]struct{})
	}
	u.m[path] = struct{}{}
}

func (u *uploadSet) remove(path string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.m, path)
}

// drop removes everything still tracked (used by Shutdown/Close: a handler
// parked past the drain deadline would otherwise leak its upload until the
// next sweep).
func (u *uploadSet) drop() {
	u.mu.Lock()
	m := u.m
	u.m = nil
	u.mu.Unlock()
	for path := range m {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("[plugin-serve] upload cleanup: %v", err)
		}
	}
}
