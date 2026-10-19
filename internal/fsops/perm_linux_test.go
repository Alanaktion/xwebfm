//go:build linux

package fsops

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestACL(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "d/sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "d/sub/data"), nil, 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "d/run.sh"), nil, 0o755))
	must(t, os.Symlink("run.sh", filepath.Join(dir, "d/link")))

	err := f.SetACL(ctx, "/d", SetACLOptions{
		ACL: ACL{
			Named:      []ACLEntry{{Kind: "user", ID: 1000, Perm: 7}},
			Default:    []ACLEntry{{Kind: "group", ID: 50, Perm: 5}},
			HasDefault: true,
		},
		Recursive: true,
	})
	if errors.Is(err, unix.EOPNOTSUPP) {
		t.Skip("filesystem without ACL support")
	}
	must(t, err)

	acl, err := f.GetACL("/d")
	must(t, err)
	if len(acl.Named) != 1 || acl.Named[0].Name != "alice" || acl.Named[0].Perm != 7 || acl.Mask == nil || *acl.Mask != 7 {
		t.Errorf("dir ACL %+v", acl)
	}
	if !acl.HasDefault || len(acl.Default) != 1 || acl.Default[0].Name != "staff" || acl.Default[0].Perm != 5 {
		t.Errorf("default ACL %+v", acl)
	}
	// Non-executable files don't gain execute; executables keep it.
	if a, _ := f.GetACL("/d/sub/data"); len(a.Named) != 1 || a.Named[0].Perm != 6 {
		t.Errorf("data ACL %+v", a)
	}
	if a, _ := f.GetACL("/d/run.sh"); len(a.Named) != 1 || a.Named[0].Perm != 7 {
		t.Errorf("run.sh ACL %+v", a)
	}
	if e, _ := f.Stat("/d/sub/data"); !e.ACL {
		t.Error("entry doesn't report its ACL")
	}
	// Cross-check the encoding with getfacl when available.
	if out, err := exec.Command("getfacl", "-n", filepath.Join(dir, "d/sub/data")).Output(); err == nil {
		if !strings.Contains(string(out), "user:1000:rw-") {
			t.Errorf("getfacl output:\n%s", out)
		}
	}
	if _, err := f.GetACL("/d/link"); !errors.Is(err, ErrIsSymlink) {
		t.Errorf("symlink ACL: %v", err)
	}

	// Removing the named entries and the default ACL clears the "+".
	must(t, f.SetACL(ctx, "/d", SetACLOptions{Recursive: true}))
	for _, p := range []string{"/d", "/d/sub/data"} {
		if e, _ := f.Stat(p); e.ACL {
			t.Errorf("%s still has an ACL", p)
		}
	}
	if err := f.SetACL(ctx, "/d", SetACLOptions{ACL: ACL{Named: []ACLEntry{{Kind: "other", Perm: 1}}}}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad entry kind: %v", err)
	}
}

func TestFlags(t *testing.T) {
	f, dir := newTestFS(t)
	p := filepath.Join(dir, "f")
	must(t, os.WriteFile(p, []byte("x"), 0o644))
	if _, err := f.GetFlags("/f"); err != nil {
		t.Skipf("inode flags unsupported here: %v", err)
	}
	if err := f.SetFlags("/f", Flags{Immutable: true, NoDump: true}); errors.Is(err, unix.EPERM) {
		t.Skip("setting the immutable flag needs CAP_LINUX_IMMUTABLE")
	} else {
		must(t, err)
	}
	t.Cleanup(func() { f.SetFlags("/f", Flags{}) })
	fl, err := f.GetFlags("/f")
	must(t, err)
	if !fl.Immutable || !fl.NoDump || fl.AppendOnly {
		t.Errorf("flags %+v", fl)
	}
	if err := os.WriteFile(p, []byte("y"), 0o644); err == nil {
		t.Error("wrote to an immutable file")
	}
	must(t, f.SetFlags("/f", Flags{}))
	must(t, os.WriteFile(p, []byte("y"), 0o644))

	must(t, unix.Mkfifo(filepath.Join(dir, "fifo"), 0o600))
	if _, err := f.GetFlags("/fifo"); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("fifo flags: %v", err)
	}
}

func TestChmodDirFileModes(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "d/sub"), 0o700))
	must(t, os.WriteFile(filepath.Join(dir, "d/sub/x"), nil, 0o600))
	dm, fm := uint32(0o2775), uint32(0o664)
	must(t, f.Chmod(ctx, "/d", ChmodOptions{DirMode: &dm, FileMode: &fm, Recursive: true}))
	for p, want := range map[string]uint32{"/d": 0o2775, "/d/sub": 0o2775, "/d/sub/x": 0o664} {
		if e, _ := f.Stat(p); e.Mode != want {
			t.Errorf("%s mode %o, want %o", p, e.Mode, want)
		}
	}
}

func TestLink(t *testing.T) {
	f, dir := newTestFS(t)
	must(t, os.MkdirAll(filepath.Join(dir, "a"), 0o755))
	must(t, os.MkdirAll(filepath.Join(dir, "b"), 0o755))
	must(t, os.WriteFile(filepath.Join(dir, "a/target"), []byte("t"), 0o644))
	if os.Geteuid() == 0 {
		must(t, os.Chown(filepath.Join(dir, "b"), 1000, 1000))
	}

	must(t, f.Link("/a/target", "/b/rel", LinkOptions{Kind: Symlink, Relative: true}))
	if got, _ := os.Readlink(filepath.Join(dir, "b/rel")); got != "../a/target" {
		t.Errorf("relative link %q", got)
	}
	must(t, f.Link("/a/target", "/b/abs", LinkOptions{Kind: Symlink}))
	if got, _ := os.Readlink(filepath.Join(dir, "b/abs")); got != filepath.Join(dir, "a/target") {
		t.Errorf("absolute link %q (must be a host path)", got)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "b/abs")); err != nil || string(b) != "t" {
		t.Errorf("absolute link doesn't resolve: %v", err)
	}
	if e, _ := f.Stat("/b/rel"); os.Geteuid() == 0 && e.UID != 1000 {
		t.Errorf("symlink owner %d, want inherited 1000", e.UID)
	}

	must(t, f.Link("/a/target", "/b/hard", LinkOptions{Kind: Hardlink}))
	a, _ := f.Stat("/a/target")
	h, _ := f.Stat("/b/hard")
	if a.Inode != h.Inode || h.Nlink != 2 {
		t.Errorf("hard link: %+v vs %+v", a, h)
	}
	if err := f.Link("/a", "/b/dirlink", LinkOptions{Kind: Hardlink}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("hard link to dir: %v", err)
	}
}

func TestReplaceFile(t *testing.T) {
	f, dir := newTestFS(t)
	p := filepath.Join(dir, "conf")
	must(t, os.WriteFile(p, []byte("old"), 0o640))
	if os.Geteuid() == 0 {
		must(t, os.Chown(p, 1000, 50))
	}
	f.SetACL(ctx, "/conf", SetACLOptions{ACL: ACL{Named: []ACLEntry{{Kind: "user", ID: 1234, Perm: 4}}}})
	e, err := f.Stat("/conf")
	must(t, err)

	if _, err := f.ReplaceFile("/conf", strings.NewReader("new"), e.Size+1, e.ModTime); !errors.Is(err, ErrConflict) {
		t.Errorf("stale size: %v", err)
	}
	ne, err := f.ReplaceFile("/conf", strings.NewReader("new contents"), e.Size, e.ModTime)
	must(t, err)
	if got, _ := os.ReadFile(p); string(got) != "new contents" {
		t.Errorf("contents %q", got)
	}
	if ne.Mode&0o777 != e.Mode&0o777 || ne.UID != e.UID || ne.GID != e.GID || ne.ACL != e.ACL {
		t.Errorf("metadata not kept: before %+v after %+v", e, ne)
	}
	entries, _ := f.List("/")
	for _, x := range entries {
		if strings.HasPrefix(x.Name, ".xwebfm-") {
			t.Errorf("temp file %s left", x.Name)
		}
	}
	must(t, os.Symlink("conf", filepath.Join(dir, "lnk")))
	if _, err := f.ReplaceFile("/lnk", strings.NewReader("x"), 0, e.ModTime); !errors.Is(err, ErrNotRegular) {
		t.Errorf("symlink: %v", err)
	}
}
