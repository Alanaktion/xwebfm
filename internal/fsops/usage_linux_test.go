//go:build linux

package fsops

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
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

func TestFindContent(t *testing.T) {
	f, dir := newTestFS(t)
	w := func(name string, data []byte) {
		p := filepath.Join(dir, "r", name)
		must(t, os.MkdirAll(filepath.Dir(p), 0o755))
		must(t, os.WriteFile(p, data, 0o644))
	}
	w("a.txt", []byte("first line\n\tThe Quick brown fox\n"))
	w("sub/b.md", []byte("no match here\n"))
	w("sub/quick-name.txt", []byte("nothing\n"))
	w("bin.dat", append([]byte("quick brown"), 0, 1, 2))
	w("big.log", []byte(strings.Repeat("x", 100)+"quick brown\n"))
	w("long.txt", []byte(strings.Repeat("a", 500)+"QUICK BROWN"+strings.Repeat("b", 500)))
	must(t, os.Symlink("a.txt", filepath.Join(dir, "r", "link")))

	limit := int64(64)
	res, err := f.Find(ctx, "/r", FindQuery{Content: "quick BROWN", ContentMaxSize: &limit}, nil)
	must(t, err)
	var got []string
	for _, e := range res.Entries {
		got = append(got, e.Name)
	}
	slices.Sort(got)
	if want := []string{"a.txt"}; !slices.Equal(got, want) {
		t.Errorf("content: got %v, want %v", got, want)
	}
	if m := res.Matches["/r/a.txt"]; m.Line != 2 || m.Text != "The Quick brown fox" {
		t.Errorf("match %+v", m)
	}
	// bin.dat is binary; big.log and long.txt exceed the 64-byte limit.
	if res.ContentSkipped != 3 {
		t.Errorf("skipped %d, want 3", res.ContentSkipped)
	}

	res, err = f.Find(ctx, "/r", FindQuery{Content: "quick brown"}, nil)
	must(t, err)
	if len(res.Entries) != 3 || res.ContentSkipped != 1 {
		t.Errorf("default limit: %d entries, %d skipped", len(res.Entries), res.ContentSkipped)
	}
	if m := res.Matches["/r/long.txt"]; len(m.Text) > maxSnippet+8 || !strings.Contains(m.Text, "QUICK BROWN") ||
		!strings.HasPrefix(m.Text, "…") || !strings.HasSuffix(m.Text, "…") {
		t.Errorf("long snippet %q", m.Text)
	}

	// Name and content criteria combine.
	res, err = f.Find(ctx, "/r", FindQuery{Name: "*.txt", Content: "nothing"}, nil)
	must(t, err)
	if len(res.Entries) != 1 || res.Entries[0].Name != "quick-name.txt" {
		t.Errorf("name+content: %+v", res.Entries)
	}

	for _, q := range []FindQuery{
		{Content: "two\nlines"},
		{Content: strings.Repeat("x", 300)},
		{Content: "x", ContentMaxSize: new(int64)},
	} {
		if err := q.Validate(); !errors.Is(err, ErrInvalidArgument) {
			t.Errorf("%+v: %v", q, err)
		}
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
