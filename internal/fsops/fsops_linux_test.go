//go:build linux

package fsops

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func newTestFS(t *testing.T) (*FS, string) {
	t.Helper()
	dir := t.TempDir()
	etc := t.TempDir()
	passwd := filepath.Join(etc, "passwd")
	group := filepath.Join(etc, "group")
	must(t, os.WriteFile(passwd, []byte("root:x:0:0::/root:/bin/sh\nalice:x:1000:1000::/home/alice:/bin/sh\n"), 0o644))
	must(t, os.WriteFile(group, []byte("root:x:0:\nalice:x:1000:\nstaff:x:50:alice\n"), 0o644))
	f, err := New(dir, &Owners{PasswdPath: passwd, GroupPath: group})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	return f, dir
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestCleanPath(t *testing.T) {
	for in, want := range map[string]string{
		"":             "/",
		"a/b":          "/a/b",
		"/a/../../etc": "/etc",
		"../../../":    "/",
		"/a//b/./c/":   "/a/b/c",
	} {
		if got := CleanPath(in); got != want {
			t.Errorf("CleanPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListAndStat(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.Mkdir(filepath.Join(dir, "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "b.txt"), []byte("hello"), 0o640))
	must(t, os.Symlink("b.txt", filepath.Join(dir, "link")))

	entries, err := f.List("/")
	must(t, err)
	if len(entries) != 3 || entries[0].Name != "sub" || entries[0].Type != TypeDir {
		t.Fatalf("unexpected entries: %+v", entries)
	}
	byName := map[string]Entry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	if e := byName["b.txt"]; e.Size != 5 || e.Mode != 0o640 || e.Path != "/b.txt" {
		t.Errorf("b.txt: %+v", e)
	}
	if e := byName["link"]; e.Type != TypeSymlink || e.LinkTarget != "b.txt" {
		t.Errorf("link: %+v", e)
	}

	e, err := f.Stat("/link")
	must(t, err)
	if e.Type != TypeSymlink {
		t.Errorf("Stat followed symlink: %+v", e)
	}
	if _, err := f.Stat("/missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Stat missing: %v", err)
	}
}

func TestSymlinkCannotEscapeRoot(t *testing.T) {
	f, dir := newTestFS(t)
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("x"), 0o600))
	must(t, os.Symlink(outside, filepath.Join(dir, "escape")))
	must(t, os.Symlink("../../../../..", filepath.Join(dir, "up")))

	// An absolute symlink is resolved relative to the root, so it points at
	// a path that doesn't exist inside it.
	if _, err := f.List("/escape"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("List through absolute symlink: %v", err)
	}
	// A relative symlink climbing upwards is clamped to the root.
	entries, err := f.List("/up")
	must(t, err)
	if len(entries) != 2 {
		t.Errorf("List /up should show the root itself, got %+v", entries)
	}
}

func TestChmod(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "d/e"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "d/e/f"), nil, 0o644))
	must(t, os.Symlink("e/f", filepath.Join(dir, "d/link")))

	must(t, f.Chmod("/d", ChmodOptions{Mode: 0o750, Recursive: true}))
	for _, p := range []string{"d", "d/e", "d/e/f"} {
		st, err := os.Lstat(filepath.Join(dir, p))
		must(t, err)
		if st.Mode().Perm() != 0o750 {
			t.Errorf("%s mode = %v", p, st.Mode())
		}
	}
	if err := f.Chmod("/d/link", ChmodOptions{Mode: 0o600}); !errors.Is(err, ErrIsSymlink) {
		t.Errorf("chmod symlink: %v", err)
	}
	if err := f.Chmod("/d", ChmodOptions{Mode: 0o10000}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("chmod bad mode: %v", err)
	}
}

func TestChown(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown requires root")
	}
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "d/e"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "d/e/f"), nil, 0o644))
	target := filepath.Join(dir, "target")
	must(t, os.WriteFile(target, nil, 0o644))
	must(t, os.Symlink(target, filepath.Join(dir, "d/link")))

	must(t, f.Chown("/d", ChownOptions{UID: 1000, GID: 50, Recursive: true}))
	for _, p := range []string{"d", "d/e", "d/e/f", "d/link"} {
		e, err := f.Stat(p)
		must(t, err)
		if e.UID != 1000 || e.GID != 50 || e.User != "alice" || e.Group != "staff" {
			t.Errorf("%s: %+v", p, e)
		}
	}
	// The symlink's target must not have been touched.
	if e, _ := f.Stat("/target"); e.UID != 0 {
		t.Errorf("chown followed symlink: %+v", e)
	}
	must(t, f.Chown("/d/e/f", ChownOptions{UID: -1, GID: 0}))
	if e, _ := f.Stat("/d/e/f"); e.UID != 1000 || e.GID != 0 {
		t.Errorf("gid-only chown: %+v", e)
	}
}

func TestCopy(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.WriteFile(filepath.Join(dir, "src"), []byte("data"), 0o640))

	res, err := f.Copy("/src", "/dst", CopyOptions{Reflink: ReflinkNever, Preserve: true})
	must(t, err)
	if res.Reflinked || res.Bytes != 4 {
		t.Errorf("result: %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(dir, "dst"))
	must(t, err)
	if string(got) != "data" {
		t.Errorf("dst contents %q", got)
	}
	if st, _ := os.Stat(filepath.Join(dir, "dst")); st.Mode().Perm() != 0o640 {
		t.Errorf("dst mode %v", st.Mode())
	}

	if _, err := f.Copy("/src", "/dst", CopyOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("copy without overwrite: %v", err)
	}
	if _, err := f.Copy("/dst", "/dst", CopyOptions{Overwrite: true}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("copy onto itself: %v", err)
	}
	if _, err := f.Copy("/", "/x", CopyOptions{}); !errors.Is(err, ErrNotRegular) {
		t.Errorf("copy dir: %v", err)
	}

	// Auto always succeeds; Always either reflinks or reports unsupported.
	res, err = f.Copy("/src", "/auto", CopyOptions{Reflink: ReflinkAuto})
	must(t, err)
	t.Logf("auto reflinked=%v", res.Reflinked)
	res, err = f.Copy("/src", "/always", CopyOptions{Reflink: ReflinkAlways})
	switch {
	case err == nil && !res.Reflinked:
		t.Error("ReflinkAlways succeeded without reflinking")
	case err != nil && !errors.Is(err, ErrReflinkUnsupported):
		t.Errorf("ReflinkAlways: %v", err)
	case err != nil:
		if _, serr := os.Lstat(filepath.Join(dir, "always")); !os.IsNotExist(serr) {
			t.Error("failed copy left destination behind")
		}
	}
}
