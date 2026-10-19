//go:build linux

package fsops

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDiskUsage(t *testing.T) {
	f, dir := newTestFS(t)
	d := filepath.Join(dir, "d")
	must(t, os.MkdirAll(filepath.Join(d, "sub"), 0o755))
	must(t, os.WriteFile(filepath.Join(d, "a"), []byte("abc"), 0o644))
	must(t, os.WriteFile(filepath.Join(d, "sub/b"), make([]byte, 100), 0o644))
	must(t, os.Link(filepath.Join(d, "a"), filepath.Join(d, "sub/hl")))
	if os.Geteuid() == 0 {
		must(t, os.Chown(filepath.Join(d, "sub/b"), 1000, 1000))
	}
	// A filesystem mounted inside is not counted.
	mnt := filepath.Join(d, "mnt")
	must(t, os.Mkdir(mnt, 0o755))
	mounted := unix.Mount("tmpfs", mnt, "tmpfs", 0, "size=1m") == nil
	if mounted {
		t.Cleanup(func() { unix.Unmount(mnt, unix.MNT_DETACH) })
		must(t, os.WriteFile(filepath.Join(mnt, "big"), make([]byte, 5000), 0o644))
	}
	// d, a, sub, sub/b, sub/hl; mnt counts only when nothing is mounted on it.
	wantEntries, wantChildren := int64(5), 2
	if !mounted {
		wantEntries, wantChildren = 6, 3
	}

	prog := &Progress{}
	rep, err := f.DiskUsage(ctx, "/d", prog)
	must(t, err)
	if rep.Size != 103 {
		t.Errorf("total size %d, want 103 (hard link counted once, mount excluded)", rep.Size)
	}
	if rep.Entries != wantEntries || prog.Snapshot().Entries != wantEntries {
		t.Errorf("entries %d (progress %d), want %d", rep.Entries, prog.Snapshot().Entries, wantEntries)
	}
	if len(rep.Children) != wantChildren || rep.Children[0].Name != "sub" || rep.Children[0].Type != TypeDir {
		t.Errorf("children %+v", rep.Children)
	}
	if os.Geteuid() == 0 {
		i := slices.IndexFunc(rep.ByOwner, func(o OwnerUsage) bool { return o.UID == 1000 })
		if i < 0 || rep.ByOwner[i].Size != 100 || rep.ByOwner[i].User != "alice" {
			t.Errorf("by owner %+v", rep.ByOwner)
		}
	}
}

func TestFind(t *testing.T) {
	f, dir := newTestFS(t)
	w := func(name string, size int, mode uint32) {
		p := filepath.Join(dir, "r", name)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, make([]byte, size), 0o644))
		must(t, unix.Chmod(p, mode))
	}
	w("notes.txt", 1, 0o644)
	w("sub/img.PNG", 1, 0o644)
	w("big.bin", 2000, 0o644)
	w("suid", 1, 0o4755)
	w("ww", 1, 0o666)
	w("foreign", 1, 0o644)
	w("orphan", 1, 0o644)
	if os.Geteuid() == 0 {
		must(t, os.Chown(filepath.Join(dir, "r/foreign"), 1000, 1000))
		must(t, os.Chown(filepath.Join(dir, "r/orphan"), 4242, 4242))
	}
	size := int64(1000)

	names := func(q FindQuery) []string {
		t.Helper()
		res, err := f.Find(ctx, "/r", q, nil)
		must(t, err)
		var out []string
		for _, e := range res.Entries {
			out = append(out, e.Name)
		}
		slices.Sort(out)
		return out
	}
	for _, tc := range []struct {
		q    FindQuery
		want []string
		root bool // needs root for chown
	}{
		{q: FindQuery{Name: "*.png"}, want: []string{"img.PNG"}},
		{q: FindQuery{Name: "NOTE"}, want: []string{"notes.txt"}},
		{q: FindQuery{MinSize: &size}, want: []string{"big.bin"}},
		{q: FindQuery{Setuid: true}, want: []string{"suid"}},
		{q: FindQuery{WorldWritable: true}, want: []string{"ww"}},
		{q: FindQuery{Type: TypeDir}, want: []string{"sub"}},
		{q: FindQuery{ForeignOwner: true}, want: []string{"foreign", "orphan"}, root: true},
		{q: FindQuery{Orphaned: true}, want: []string{"orphan"}, root: true},
	} {
		if tc.root && os.Geteuid() != 0 {
			continue
		}
		if got := names(tc.q); !slices.Equal(got, tc.want) {
			t.Errorf("%+v: got %v, want %v", tc.q, got, tc.want)
		}
	}

	res, err := f.Find(ctx, "/r", FindQuery{Limit: 2}, nil)
	must(t, err)
	if len(res.Entries) != 2 || !res.Truncated {
		t.Errorf("limit: %d entries, truncated=%v", len(res.Entries), res.Truncated)
	}
	if _, err := f.Find(ctx, "/r", FindQuery{Name: "["}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("bad pattern: %v", err)
	}
}

func TestStatFSAndListExtras(t *testing.T) {
	f, dir := newTestFS(t)
	info, err := f.StatFS("/")
	must(t, err)
	if info.Type == "" || info.Total == 0 || info.Free > info.Total {
		t.Errorf("statfs %+v", info)
	}
	must(t, os.WriteFile(filepath.Join(dir, "f"), make([]byte, 10000), 0o644))
	e, err := f.Stat("/f")
	must(t, err)
	if e.Disk < 8192 || e.ACL {
		t.Errorf("entry %+v", e)
	}
}
