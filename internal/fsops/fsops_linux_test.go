//go:build linux

package fsops

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"golang.org/x/sys/unix"
)

var ctx = context.Background()

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

	must(t, f.Chmod(ctx, "/d", ChmodOptions{Mode: 0o750, Recursive: true}))
	for _, p := range []string{"d", "d/e", "d/e/f"} {
		st, err := os.Lstat(filepath.Join(dir, p))
		must(t, err)
		if st.Mode().Perm() != 0o750 {
			t.Errorf("%s mode = %v", p, st.Mode())
		}
	}
	if err := f.Chmod(ctx, "/d/link", ChmodOptions{Mode: 0o600}); !errors.Is(err, ErrIsSymlink) {
		t.Errorf("chmod symlink: %v", err)
	}
	if err := f.Chmod(ctx, "/d", ChmodOptions{Mode: 0o10000}); !errors.Is(err, ErrInvalidArgument) {
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

	must(t, f.Chown(ctx, "/d", ChownOptions{UID: 1000, GID: 50, Recursive: true}))
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
	must(t, f.Chown(ctx, "/d/e/f", ChownOptions{UID: -1, GID: 0}))
	if e, _ := f.Stat("/d/e/f"); e.UID != 1000 || e.GID != 0 {
		t.Errorf("gid-only chown: %+v", e)
	}
}

func TestCopyFile(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.WriteFile(filepath.Join(dir, "src"), []byte("data"), 0o640))

	res, err := f.Copy(ctx, "/src", "/dst", CopyOptions{Reflink: ReflinkNever})
	must(t, err)
	if res.Reflinked != 0 || res.Files != 1 || res.Bytes != 4 {
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

	if _, err := f.Copy(ctx, "/src", "/dst", CopyOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("copy without overwrite: %v", err)
	}
	if _, err := f.Copy(ctx, "/dst", "/dst", CopyOptions{Overwrite: true}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("copy onto itself: %v", err)
	}

	// Overwrite must replace the file, not write through a hard link to it.
	must(t, os.Link(filepath.Join(dir, "dst"), filepath.Join(dir, "hardlink")))
	must(t, os.WriteFile(filepath.Join(dir, "src"), []byte("new"), 0o640))
	_, err = f.Copy(ctx, "/src", "/dst", CopyOptions{Overwrite: true})
	must(t, err)
	if got, _ := os.ReadFile(filepath.Join(dir, "hardlink")); string(got) != "data" {
		t.Errorf("overwrite wrote through hard link: %q", got)
	}

	// Auto always succeeds; Always either reflinks or reports unsupported.
	res, err = f.Copy(ctx, "/src", "/auto", CopyOptions{Reflink: ReflinkAuto})
	must(t, err)
	t.Logf("auto reflinked=%d", res.Reflinked)
	res, err = f.Copy(ctx, "/src", "/always", CopyOptions{Reflink: ReflinkAlways})
	switch {
	case err == nil && res.Reflinked != 1:
		t.Error("ReflinkAlways succeeded without reflinking")
	case err != nil && !errors.Is(err, ErrReflinkUnsupported):
		t.Errorf("ReflinkAlways: %v", err)
	case err != nil:
		if _, serr := os.Lstat(filepath.Join(dir, "always")); !os.IsNotExist(serr) {
			t.Error("failed copy left destination behind")
		}
	}
}

func makeTree(t *testing.T, root string) {
	t.Helper()
	must(t, os.MkdirAll(filepath.Join(root, "sub/deeper"), 0o750))
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("aaa"), 0o640))
	must(t, os.WriteFile(filepath.Join(root, "sub/deeper/b.bin"), []byte("bb"), 0o755))
	must(t, unix.Chmod(filepath.Join(root, "sub/deeper/b.bin"), 0o4755))
	must(t, os.Symlink("../a.txt", filepath.Join(root, "sub/link")))
	must(t, unix.Mkfifo(filepath.Join(root, "fifo"), 0o600))
}

func TestCopyTree(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))

	res, err := f.Copy(ctx, "/src", "/dst", CopyOptions{})
	must(t, err)
	if res.Files != 4 || res.Dirs != 3 || res.Bytes != 5 {
		t.Errorf("result: %+v", res)
	}
	if target, _ := os.Readlink(filepath.Join(dir, "dst/sub/link")); target != "../a.txt" {
		t.Errorf("symlink target %q", target)
	}
	if st, _ := os.Lstat(filepath.Join(dir, "dst/fifo")); st == nil || st.Mode()&os.ModeNamedPipe == 0 {
		t.Errorf("fifo not copied: %v", st)
	}
	if st, _ := os.Stat(filepath.Join(dir, "dst/sub")); st.Mode().Perm() != 0o750 {
		t.Errorf("dir mode %v", st.Mode())
	}
	// Same owner, so setuid survives.
	if st, _ := os.Stat(filepath.Join(dir, "dst/sub/deeper/b.bin")); st.Mode()&os.ModeSetuid == 0 {
		t.Errorf("setuid lost: %v", st.Mode())
	}

	if _, err := f.Copy(ctx, "/src", "/src/sub/inside", CopyOptions{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("copy into itself: %v", err)
	}
	// Overwrite merges into the existing tree.
	must(t, os.WriteFile(filepath.Join(dir, "dst/extra"), nil, 0o644))
	_, err = f.Copy(ctx, "/src", "/dst", CopyOptions{Overwrite: true})
	must(t, err)
	if _, err := os.Lstat(filepath.Join(dir, "dst/extra")); err != nil {
		t.Errorf("merge removed extra file: %v", err)
	}
}

// Use case: share a tree with another user by copying it into their home,
// handing ownership to them.
func TestCopyTreeInheritOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown requires root")
	}
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "home/alice"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "home/bob"), 0o755))
	must(t, os.Chown(filepath.Join(dir, "home/bob"), 1001, 1001))
	makeTree(t, filepath.Join(dir, "home/alice/project"))
	must(t, f.Chown(ctx, "/home/alice/project", ChownOptions{UID: 1000, GID: 1000, Recursive: true}))

	_, err := f.Copy(ctx, "/home/alice/project", "/home/bob/project", CopyOptions{Ownership: Ownership{Owner: OwnerInherit}})
	must(t, err)
	for _, p := range []string{"project", "project/a.txt", "project/sub", "project/sub/link", "project/sub/deeper/b.bin"} {
		e, err := f.Stat("/home/bob/" + p)
		must(t, err)
		if e.UID != 1001 || e.GID != 1001 {
			t.Errorf("%s owned by %d:%d", p, e.UID, e.GID)
		}
	}
	// The setuid bit must not carry over to the new owner.
	if e, _ := f.Stat("/home/bob/project/sub/deeper/b.bin"); e.Mode&0o6000 != 0 || e.Mode&0o777 != 0o755 {
		t.Errorf("b.bin mode %o", e.Mode)
	}
	// Source untouched.
	if e, _ := f.Stat("/home/alice/project/a.txt"); e.UID != 1000 {
		t.Errorf("source re-owned: %+v", e)
	}

	_, err = f.Copy(ctx, "/home/alice/project/a.txt", "/home/bob/set.txt",
		CopyOptions{Ownership: Ownership{Owner: OwnerSet, UID: 1234, GID: -1}})
	must(t, err)
	if e, _ := f.Stat("/home/bob/set.txt"); e.UID != 1234 || e.GID != 1000 {
		t.Errorf("explicit owner: %+v", e)
	}
}

// Use case: move a directory between users' homes, re-owning it.
func TestMoveInheritOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("chown requires root")
	}
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "home/alice"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "home/bob"), 0o755))
	must(t, os.Chown(filepath.Join(dir, "home/bob"), 1001, 1001))
	makeTree(t, filepath.Join(dir, "home/alice/project"))
	must(t, f.Chown(ctx, "/home/alice/project", ChownOptions{UID: 1000, GID: 1000, Recursive: true}))

	res, err := f.Move(ctx, "/home/alice/project", "/home/bob/project", MoveOptions{Ownership: Ownership{Owner: OwnerInherit}})
	must(t, err)
	if res.Copied {
		t.Error("same-filesystem move fell back to copying")
	}
	if _, err := f.Stat("/home/alice/project"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("source still exists: %v", err)
	}
	for _, p := range []string{"project", "project/a.txt", "project/sub/link", "project/sub/deeper/b.bin"} {
		e, err := f.Stat("/home/bob/" + p)
		must(t, err)
		if e.UID != 1001 || e.GID != 1001 {
			t.Errorf("%s owned by %d:%d", p, e.UID, e.GID)
		}
	}
}

func TestMoveRenameDelete(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	must(t, os.WriteFile(filepath.Join(dir, "other"), nil, 0o644))

	if _, err := f.Move(ctx, "/src", "/src/sub/x", MoveOptions{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("move into itself: %v", err)
	}
	if _, err := f.Move(ctx, "/other", "/src/a.txt", MoveOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("move onto existing: %v", err)
	}
	_, err := f.Move(ctx, "/other", "/src/a.txt", MoveOptions{Overwrite: true})
	must(t, err)

	must(t, f.Rename("/src", "renamed"))
	if _, err := os.Stat(filepath.Join(dir, "renamed/sub/deeper/b.bin")); err != nil {
		t.Errorf("rename: %v", err)
	}
	if err := f.Rename("/renamed", "../escape"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("rename with slash: %v", err)
	}

	// Deleting a tree containing a symlink must not follow it.
	outside := filepath.Join(dir, "keep")
	must(t, os.WriteFile(outside, nil, 0o644))
	must(t, os.Symlink("/keep", filepath.Join(dir, "renamed/sub/abs")))
	must(t, f.Delete(ctx, "/renamed", DeleteOptions{}))
	if _, err := os.Lstat(filepath.Join(dir, "renamed")); !os.IsNotExist(err) {
		t.Errorf("delete: %v", err)
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Errorf("delete followed symlink: %v", err)
	}
	if err := f.Delete(ctx, "/", DeleteOptions{}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("delete root: %v", err)
	}
}

func TestMkdirAndWriteFile(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.Mkdir(filepath.Join(dir, "home"), 0o755))
	if os.Geteuid() == 0 {
		must(t, os.Chown(filepath.Join(dir, "home"), 1000, 50))
	}

	must(t, f.Mkdir("/home/new", MkdirOptions{Mode: 0o2770}))
	e, err := f.Stat("/home/new")
	must(t, err)
	if e.Type != TypeDir || e.Mode != 0o2770 {
		t.Errorf("mkdir: %+v", e)
	}
	if os.Geteuid() == 0 && (e.UID != 1000 || e.GID != 50) {
		t.Errorf("mkdir did not inherit owner: %+v", e)
	}

	e, err = f.WriteFile("/home", "up.txt", strings.NewReader("hello"), WriteOptions{})
	must(t, err)
	if e.Size != 5 || e.Mode != 0o644 {
		t.Errorf("write: %+v", e)
	}
	if _, err := f.WriteFile("/home", "up.txt", strings.NewReader("x"), WriteOptions{}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("write existing: %v", err)
	}
	_, err = f.WriteFile("/home", "up.txt", strings.NewReader("x"), WriteOptions{Overwrite: true})
	must(t, err)
	if got, _ := os.ReadFile(filepath.Join(dir, "home/up.txt")); string(got) != "x" {
		t.Errorf("overwrite contents %q", got)
	}
	entries, _ := f.List("/home")
	for _, e := range entries {
		if strings.HasPrefix(e.Name, ".xwebfm-upload-") {
			t.Errorf("temp file left behind: %s", e.Name)
		}
	}
}

func TestWriteTar(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	var buf bytes.Buffer
	must(t, f.WriteTar(ctx, "/src", &buf))
	gz, err := gzip.NewReader(&buf)
	must(t, err)
	tr := tar.NewReader(gz)
	got := map[string]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		must(t, err)
		got[hdr.Name] = hdr.Typeflag
	}
	want := map[string]byte{
		"src/": tar.TypeDir, "src/a.txt": tar.TypeReg, "src/sub/": tar.TypeDir,
		"src/sub/link": tar.TypeSymlink, "src/sub/deeper/": tar.TypeDir, "src/sub/deeper/b.bin": tar.TypeReg,
	}
	if len(got) != len(want) {
		t.Errorf("tar entries %v", got)
	}
	for name, typ := range want {
		if got[name] != typ {
			t.Errorf("%s: type %q, want %q", name, got[name], typ)
		}
	}
}

func TestMoveAcrossFilesystems(t *testing.T) {
	f, dir := newTestFS(t)
	mnt := filepath.Join(dir, "mnt")
	must(t, os.Mkdir(mnt, 0o755))
	if err := unix.Mount("tmpfs", mnt, "tmpfs", 0, "size=4m"); err != nil {
		t.Skipf("can't mount tmpfs: %v", err)
	}
	t.Cleanup(func() { unix.Unmount(mnt, unix.MNT_DETACH) })
	makeTree(t, filepath.Join(dir, "src"))

	res, err := f.Move(ctx, "/src", "/mnt/src", MoveOptions{})
	must(t, err)
	if !res.Copied || res.Copy == nil || res.Copy.Files != 4 {
		t.Errorf("result: %+v %+v", res, res.Copy)
	}
	if _, err := os.Lstat(filepath.Join(dir, "src")); !os.IsNotExist(err) {
		t.Errorf("source not removed: %v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(mnt, "src/a.txt")); string(got) != "aaa" {
		t.Errorf("moved contents %q", got)
	}
}

func TestCopyProgressAndUsage(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	big := bytes.Repeat([]byte("x"), copyChunk*2+123)
	must(t, os.WriteFile(filepath.Join(dir, "src/big"), big, 0o644))

	entries, size, err := f.Usage(ctx, "/src")
	must(t, err)
	// src, a.txt, sub, sub/link, sub/deeper, b.bin, fifo, big
	if entries != 8 || size != int64(len(big))+5 {
		t.Errorf("usage: %d entries, %d bytes", entries, size)
	}

	var prog Progress
	prog.AddTotal(entries, size)
	_, err = f.Copy(ctx, "/src", "/dst", CopyOptions{Reflink: ReflinkNever, Progress: &prog})
	must(t, err)
	got := prog.Snapshot()
	if got.Entries != entries || got.Bytes != size || got.TotalBytes != size {
		t.Errorf("progress %+v, want %d entries / %d bytes", got, entries, size)
	}
}

func TestCancel(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	if _, err := f.Copy(cancelled, "/src", "/dst", CopyOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("copy: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dst")); !os.IsNotExist(err) {
		t.Errorf("cancelled copy left destination: %v", err)
	}
	if err := f.Delete(cancelled, "/src", DeleteOptions{}); !errors.Is(err, context.Canceled) {
		t.Errorf("delete: %v", err)
	}
	if err := f.Chmod(cancelled, "/src", ChmodOptions{Mode: 0o700, Recursive: true}); !errors.Is(err, context.Canceled) {
		t.Errorf("chmod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/a.txt")); err != nil {
		t.Errorf("source damaged: %v", err)
	}
}

// cancelAfter reports cancellation once Err has been checked n times.
type cancelAfter struct {
	context.Context
	n atomic.Int32
}

func (c *cancelAfter) Err() error {
	if c.n.Add(-1) < 0 {
		return context.Canceled
	}
	return nil
}

// Cancelling while a large file is mid-copy stops between chunks.
func TestCancelMidFile(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.WriteFile(filepath.Join(dir, "big"), bytes.Repeat([]byte("y"), copyChunk*4), 0o644))
	// One check before the file, one per chunk: cancel after the first chunk.
	cctx := &cancelAfter{Context: ctx}
	cctx.n.Store(2)
	prog := &Progress{}
	_, err := f.Copy(cctx, "/big", "/copy", CopyOptions{Reflink: ReflinkNever, Progress: prog})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("copy: %v", err)
	}
	if got := prog.Snapshot().Bytes; got != copyChunk {
		t.Errorf("copied %d bytes before cancelling, want %d", got, copyChunk)
	}
	if _, err := os.Lstat(filepath.Join(dir, "copy")); !os.IsNotExist(err) {
		t.Errorf("partial file left behind: %v", err)
	}
}
