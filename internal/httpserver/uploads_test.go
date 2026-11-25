package httpserver

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/tevfik/gleann-plugin-sound/internal/core"
)

// S3: /convert uploads are staged under <TempDir>/gleann-plugin-sound/
// port-<port>/, and a stale file left in this port's own subdir by a killed
// instance is swept once at startup. Sibling port dirs belong to instances
// running on other ports and are never touched.

func TestSweepUploadsOwnDirOnly(t *testing.T) {
	root := t.TempDir()
	withUploadRoot(t, root)

	own := filepath.Join(root, "port-18765")
	sibling := filepath.Join(root, "port-18766")
	for _, d := range []string{own, sibling} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	stale := []string{"upload-0001.wav", "upload-0002.mp3"}
	foreign := []string{"gleann-plugin-sound-old.wav", "stranger.txt", "notes-upload-1.txt"}
	for _, name := range append(append([]string{}, stale...), foreign...) {
		if err := os.WriteFile(filepath.Join(own, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(sibling, "upload-live.wav")
	if err := os.WriteFile(keep, []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := New(nil, "tr", 18765)
	s.sweepUploads()

	// Only upload-* files are ours; foreign-named files survive.
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(own, name)); !os.IsNotExist(err) {
			t.Errorf("stale %s was not swept (err=%v)", name, err)
		}
	}
	for _, name := range foreign {
		if _, err := os.Stat(filepath.Join(own, name)); err != nil {
			t.Errorf("foreign-named %s was deleted: %v", name, err)
		}
	}
	// The sibling port's dir must survive untouched.
	if _, err := os.Stat(keep); err != nil {
		t.Errorf("sibling port dir damaged: %v", err)
	}
}

func TestSweepUploadsMissingDir(t *testing.T) {
	root := t.TempDir()
	old := uploadRoot
	uploadRoot = filepath.Join(root, "absent")
	defer func() { uploadRoot = old }()
	s := New(nil, "tr", 18767)
	s.sweepUploads() // must not panic, must not create junk
	if _, err := os.Stat(filepath.Join(root, "absent")); !os.IsNotExist(err) {
		t.Errorf("sweep created %s although it found nothing to clean", filepath.Join(root, "absent"))
	}
}

// The upload must land in the per-port staging dir (where the sweep can find
// it if the process dies) and be removed again when the request ends.
func TestUploadStagedInPortDir(t *testing.T) {
	root := t.TempDir()
	withUploadRoot(t, root)

	var got string
	s := New(func() (core.Transcriber, error) {
		return &fakePathEngine{segs: []core.Segment{{End: time.Second, Text: "x"}}, onPath: func(p string) { got = p }}, nil
	}, "tr", 18790)
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()

	resp := upload(t, ts.URL)
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	want := filepath.Join(root, "port-18790")
	if got == "" {
		t.Fatal("engine never saw an upload path")
	}
	if filepath.Dir(got) != want {
		t.Errorf("upload staged at %q, want inside %q", got, want)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Errorf("upload file not removed after the request: %v", err)
	}
}

type fakePathEngine struct {
	segs   []core.Segment
	onPath func(string)
}

func (e *fakePathEngine) TranscribeStream(context.Context, []int16) (string, error) { return "", nil }
func (e *fakePathEngine) TranscribeStreamSegments(context.Context, []int16) ([]core.Segment, error) {
	return e.segs, nil
}
func (e *fakePathEngine) TranscribeFile(_ context.Context, path string) ([]core.Segment, error) {
	e.onPath(path)
	return e.segs, nil
}
func (e *fakePathEngine) SetLanguage(string) {}
func (e *fakePathEngine) Close() error       { return nil }

// victimDir makes a directory with files that must never be deleted.
func victimDir(t *testing.T) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	var files []string
	for _, n := range []string{"upload-1.wav", "thesis.docx", "id_ed25519"} {
		p := filepath.Join(dir, n)
		if err := os.WriteFile(p, []byte("precious"), 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, p)
	}
	return dir, files
}

func assertAllExist(t *testing.T, files []string) {
	t.Helper()
	for _, f := range files {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s was deleted: %v", f, err)
		}
	}
}

func withUploadRoot(t *testing.T, root string) {
	t.Helper()
	// t.TempDir honours the umask (002 gives group-write); a real root is 0700.
	if err := os.Chmod(root, 0o700); err != nil && !os.IsNotExist(err) && runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	old := uploadRoot
	uploadRoot = root
	t.Cleanup(func() { uploadRoot = old })
}

// B1: a port-<n> that is a symlink to someone's directory must not be swept.
func TestSweepIgnoresSymlinkedPortDir(t *testing.T) {
	victim, files := victimDir(t)
	root := t.TempDir()
	withUploadRoot(t, root)
	if err := os.Symlink(victim, filepath.Join(root, "port-18801")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := New(nil, "tr", 18801)
	s.sweepUploads()
	assertAllExist(t, files)
}

// B1: a root that is a symlink (planted in a shared temp dir) must not be swept.
func TestSweepIgnoresSymlinkedRoot(t *testing.T) {
	victim, files := victimDir(t)
	if err := os.MkdirAll(filepath.Join(victim, "port-18802"), 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(victim, "port-18802", "upload-9.wav")
	if err := os.WriteFile(inner, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	files = append(files, inner)
	link := filepath.Join(t.TempDir(), "root")
	if err := os.Symlink(victim, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	withUploadRoot(t, link)
	s := New(nil, "tr", 18802)
	s.sweepUploads()
	assertAllExist(t, files)
}

// B1: a root other users can write to is refused.
func TestSweepRefusesGroupWritableRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix modes")
	}
	root := t.TempDir()
	withUploadRoot(t, root)
	port := filepath.Join(root, "port-18803")
	if err := os.Mkdir(port, 0o700); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(port, "upload-1.wav")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o770); err != nil {
		t.Fatal(err)
	}
	New(nil, "tr", 18803).sweepUploads()
	assertAllExist(t, []string{f})
	// a group-writable port dir is refused too
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(port, 0o770); err != nil {
		t.Fatal(err)
	}
	New(nil, "tr", 18803).sweepUploads()
	assertAllExist(t, []string{f})
}

// Symlinks and directories named upload-* inside a good port dir are not followed.
func TestSweepSkipsSymlinkAndDirEntries(t *testing.T) {
	victim, files := victimDir(t)
	root := t.TempDir()
	withUploadRoot(t, root)
	port := filepath.Join(root, "port-18804")
	if err := os.Mkdir(port, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(victim, "thesis.docx"), filepath.Join(port, "upload-link.wav")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	sub := filepath.Join(port, "upload-dir")
	if err := os.Mkdir(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(sub, "keep.txt")
	if err := os.WriteFile(inner, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	New(nil, "tr", 18804).sweepUploads()
	assertAllExist(t, append(files, inner))
}

// When the staging dir fails its checks, uploads go to a private fresh dir,
// never into the untrusted one.
func TestUploadFallsBackToPrivateDirWhenStagingUnsafe(t *testing.T) {
	victim, _ := victimDir(t)
	root := t.TempDir()
	withUploadRoot(t, root)
	if err := os.Symlink(victim, filepath.Join(root, "port-18805")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	var got string
	s := New(func() (core.Transcriber, error) {
		return &fakePathEngine{segs: []core.Segment{{End: time.Second, Text: "x"}}, onPath: func(p string) { got = p }}, nil
	}, "tr", 18805)
	defer s.Close()
	ts := httptest.NewServer(s.Handler())
	defer ts.Close()
	resp := upload(t, ts.URL)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d, want 200", resp.StatusCode)
	}
	if got == "" {
		t.Fatal("engine never saw an upload path")
	}
	if d := filepath.Dir(got); d == victim || d == filepath.Join(root, "port-18805") {
		t.Fatalf("upload staged inside the untrusted dir %s", d)
	}
	if ents, _ := os.ReadDir(victim); len(ents) != 3 {
		t.Errorf("victim dir changed: %d entries", len(ents))
	}
}
