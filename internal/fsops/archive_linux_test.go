//go:build linux

package fsops

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestDetectArchive(t *testing.T) {
	for name, want := range map[string]ArchiveInfo{
		"a.zip":           {Format: FormatZip, Base: "a", First: "a.zip"},
		"A.TAR.GZ":        {Format: FormatTarGz, Base: "A", First: "A.TAR.GZ"},
		"b.tgz":           {Format: FormatTarGz, Base: "b", First: "b.tgz"},
		"c.tar.zst":       {Format: FormatTarZst, Base: "c", First: "c.tar.zst"},
		"notes.txt.bz2":   {Format: FormatBz2, Base: "notes.txt", First: "notes.txt.bz2"},
		"m.part2.rar":     {Format: FormatRar, Base: "m", First: "m.part1.rar"},
		"m.part03.rar":    {Format: FormatRar, Base: "m", First: "m.part01.rar"},
		"old.r01":         {Format: FormatRar, Base: "old", First: "old.rar"},
		"v.7z.002":        {Format: Format7z, Base: "v", First: "v.7z.001", Split: true},
		"photos 2024.rar": {Format: FormatRar, Base: "photos 2024", First: "photos 2024.rar"},
	} {
		got, ok := DetectArchive(name)
		if !ok || got != want {
			t.Errorf("DetectArchive(%q) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	for _, name := range []string{"readme.txt", ".zip", "archive"} {
		if _, ok := DetectArchive(name); ok {
			t.Errorf("DetectArchive(%q) recognised a non-archive", name)
		}
	}
}

// checkTree verifies an extracted copy of makeTree's tree (FIFO excluded).
func checkTree(t *testing.T, dir string) {
	t.Helper()
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "aaa" {
		t.Errorf("a.txt: %q %v", got, err)
	}
	if target, err := os.Readlink(filepath.Join(dir, "sub/link")); err != nil || target != "../a.txt" {
		t.Errorf("sub/link: %q %v", target, err)
	}
	st, err := os.Lstat(filepath.Join(dir, "sub/deeper/b.bin"))
	if err != nil {
		t.Fatalf("b.bin: %v", err)
	}
	if st.Mode()&os.ModeSetuid != 0 || st.Mode().Perm() != 0o755 {
		t.Errorf("b.bin mode %v (setuid must be stripped)", st.Mode())
	}
	if _, err := os.Lstat(filepath.Join(dir, "fifo")); !os.IsNotExist(err) {
		t.Errorf("fifo extracted: %v", err)
	}
}

func TestArchiveRoundTrip(t *testing.T) {
	formats := slices.Clone(CreateFormats)
	if SevenZipAvailable() {
		formats = append(formats, Format7z)
	}
	for _, format := range formats {
		t.Run(string(format), func(t *testing.T) {
			f, dir := newTestFS(t)
			makeTree(t, filepath.Join(dir, "src"))
			must(t, os.Mkdir(filepath.Join(dir, "out"), 0o755))
			arch := "/archive" + format.Extension()
			prog := &Progress{}
			e, err := f.CreateArchive(ctx, []string{"/src"}, arch, CreateArchiveOptions{Format: format, Progress: prog})
			must(t, err)
			if e.Size == 0 || e.Mode != 0o644 {
				t.Errorf("archive entry %+v", e)
			}
			if p := prog.Snapshot(); p.Bytes != p.TotalBytes {
				t.Errorf("create progress %+v", p)
			}

			res, err := f.Extract(ctx, arch, "/out", ExtractOptions{})
			must(t, err)
			// A single top-level directory is extracted as is.
			if res.Path != "/out/src" {
				t.Errorf("extracted to %s", res.Path)
			}
			checkTree(t, filepath.Join(dir, "out/src"))
			assertNoTemp(t, filepath.Join(dir, "out"))
		})
	}
}

func assertNoTemp(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".xwebfm-") {
			t.Errorf("temporary %s left in %s", e.Name(), dir)
		}
	}
}

func TestExtractSubdirectoryRules(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	must(t, os.Mkdir(filepath.Join(dir, "out"), 0o755))
	_, err := f.CreateArchive(ctx, []string{"/src/a.txt", "/src/sub"}, "/two.zip", CreateArchiveOptions{Format: FormatZip})
	must(t, err)

	// Several top-level entries: a directory named after the archive.
	res, err := f.Extract(ctx, "/two.zip", "/out", ExtractOptions{})
	must(t, err)
	if res.Path != "/out/two" || res.Entries != 5 {
		t.Errorf("result %+v", res)
	}
	if st, err := os.Stat(filepath.Join(dir, "out/two")); err != nil || st.Mode().Perm() != 0o755 {
		t.Errorf("subdir: %v %v", st, err)
	}
	// Taken name: "two (2)".
	res, err = f.Extract(ctx, "/two.zip", "/out", ExtractOptions{})
	must(t, err)
	if res.Path != "/out/two (2)" {
		t.Errorf("second extraction to %s", res.Path)
	}

	// A single top-level entry whose name is taken also goes into a subdir.
	_, err = f.CreateArchive(ctx, []string{"/src/a.txt"}, "/one.tar.gz", CreateArchiveOptions{Format: FormatTarGz})
	must(t, err)
	res, err = f.Extract(ctx, "/one.tar.gz", "/src", ExtractOptions{})
	must(t, err)
	if res.Path != "/src/one" {
		t.Errorf("single entry with taken name extracted to %s", res.Path)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "src/one/a.txt")); string(got) != "aaa" {
		t.Errorf("one/a.txt = %q", got)
	}
}

func copyFixture(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join("testdata", n))
		if err != nil {
			t.Skipf("fixture %s missing: %v", n, err)
		}
		must(t, os.WriteFile(filepath.Join(dir, n), b, 0o644))
	}
}

// The fixtures hold dir/{a.txt,sub/b.txt,link→a.txt,random.bin} and readme.txt.
func checkFixture(t *testing.T, root string) {
	t.Helper()
	for p, want := range map[string]string{"dir/a.txt": "alpha\n", "dir/sub/b.txt": "bravo\n", "readme.txt": "readme\n"} {
		if got, err := os.ReadFile(filepath.Join(root, p)); err != nil || string(got) != want {
			t.Errorf("%s: %q %v", p, got, err)
		}
	}
	if st, err := os.Stat(filepath.Join(root, "dir/random.bin")); err != nil || st.Size() != 40000 {
		t.Errorf("random.bin: %v %v", st, err)
	}
	if target, err := os.Readlink(filepath.Join(root, "dir/link")); err != nil || target != "a.txt" {
		t.Errorf("dir/link: %q %v", target, err)
	}
}

func TestExtractFixtures(t *testing.T) {
	for _, tc := range []struct {
		files    []string
		open     string // the file selected for extraction
		password string
	}{
		{files: []string{"multi.7z"}, open: "multi.7z"},
		{files: []string{"multi.rar"}, open: "multi.rar"},
		{files: []string{"enc.7z"}, open: "enc.7z", password: "secret"},
		{files: []string{"enc.rar"}, open: "enc.rar", password: "secret"},
		{files: []string{"vol.7z.001", "vol.7z.002"}, open: "vol.7z.002"},
		{files: []string{"vol.part1.rar", "vol.part2.rar"}, open: "vol.part2.rar"},
	} {
		t.Run(tc.open, func(t *testing.T) {
			f, dir := newTestFS(t)
			copyFixture(t, dir, tc.files...)
			must(t, os.Mkdir(filepath.Join(dir, "out"), 0o755))
			if tc.password != "" {
				_, err := f.Extract(ctx, "/"+tc.open, "/out", ExtractOptions{Password: "wrong"})
				if !errors.Is(err, ErrEncrypted) {
					t.Errorf("wrong password: %v", err)
				}
				assertNoTemp(t, filepath.Join(dir, "out"))
			}
			prog := &Progress{}
			res, err := f.Extract(ctx, "/"+tc.open, "/out", ExtractOptions{Password: tc.password, DeleteSource: true, Progress: prog})
			must(t, err)
			info, _ := DetectArchive(tc.open)
			if res.Path != "/out/"+info.Base {
				t.Errorf("extracted to %s", res.Path)
			}
			checkFixture(t, filepath.Join(dir, "out", info.Base))
			if len(res.Volumes) != len(tc.files) {
				t.Errorf("volumes %v, want %v", res.Volumes, tc.files)
			}
			for _, n := range tc.files {
				if _, err := os.Stat(filepath.Join(dir, n)); !os.IsNotExist(err) {
					t.Errorf("%s not deleted: %v", n, err)
				}
			}
			if p := prog.Snapshot(); p.TotalBytes == 0 || p.Bytes != p.TotalBytes {
				t.Errorf("progress %+v", p)
			}
		})
	}
}

func TestExtractHostileArchive(t *testing.T) {
	f, dir := newTestFS(t)
	outside := t.TempDir()
	must(t, os.Mkdir(filepath.Join(dir, "out"), 0o755))

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		must(t, tw.WriteHeader(h))
		tw.Write([]byte(body))
	}
	add(&tar.Header{Name: "../evil", Typeflag: tar.TypeReg}, "x")
	add(&tar.Header{Name: "/abs", Typeflag: tar.TypeReg}, "x")
	add(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: outside}, "")
	add(&tar.Header{Name: "link/pwned", Typeflag: tar.TypeReg}, "x")
	add(&tar.Header{Name: "hard", Typeflag: tar.TypeLink, Linkname: "/etc/passwd"}, "")
	add(&tar.Header{Name: "hard2", Typeflag: tar.TypeLink, Linkname: "../../x"}, "")
	add(&tar.Header{Name: "dev", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}, "")
	add(&tar.Header{Name: "suid", Typeflag: tar.TypeReg, Mode: 0o4755}, "#!/bin/sh\n")
	add(&tar.Header{Name: "ok", Typeflag: tar.TypeReg}, "fine")
	add(&tar.Header{Name: "okhard", Typeflag: tar.TypeLink, Linkname: "ok"}, "")
	must(t, tw.Close())
	must(t, os.WriteFile(filepath.Join(dir, "bad.tar"), buf.Bytes(), 0o644))

	res, err := f.Extract(ctx, "/bad.tar", "/out", ExtractOptions{})
	must(t, err)
	if res.Path != "/out/bad" {
		t.Fatalf("extracted to %s", res.Path)
	}
	if _, err := os.Stat(filepath.Join(outside, "pwned")); !os.IsNotExist(err) {
		t.Error("wrote through a symlink outside the extraction")
	}
	for _, p := range []string{"evil", "out/evil", "abs", "out/bad/abs", "out/bad/dev", "out/bad/hard", "out/bad/hard2"} {
		if _, err := os.Lstat(filepath.Join(dir, p)); !os.IsNotExist(err) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
	st, err := os.Stat(filepath.Join(dir, "out/bad/suid"))
	if err != nil || st.Mode()&os.ModeSetuid != 0 {
		t.Errorf("suid: %v %v", st, err)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, "out/bad/okhard")); string(got) != "fine" {
		t.Errorf("internal hard link: %q", got)
	}
	if len(res.Skipped) != 6 { // the symlink itself is allowed; writing through it is not
		t.Errorf("skipped %d: %v", len(res.Skipped), res.Skipped)
	}

	// Backslash traversal in zip names.
	buf.Reset()
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create(`..\..\zipevil`)
	w.Write([]byte("x"))
	w, _ = zw.Create("fine.txt")
	w.Write([]byte("x"))
	must(t, zw.Close())
	must(t, os.WriteFile(filepath.Join(dir, "bad.zip"), buf.Bytes(), 0o644))
	res, err = f.Extract(ctx, "/bad.zip", "/out", ExtractOptions{})
	must(t, err)
	if res.Path != "/out/fine.txt" || len(res.Skipped) != 1 {
		t.Errorf("zip result %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "zipevil")); !os.IsNotExist(err) {
		t.Error("zip backslash traversal escaped")
	}
}

func TestExtractCancelAndOwner(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	must(t, os.Mkdir(filepath.Join(dir, "out"), 0o755))
	_, err := f.CreateArchive(ctx, []string{"/src"}, "/a.tar.zst", CreateArchiveOptions{Format: FormatTarZst})
	must(t, err)

	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.Extract(cancelled, "/a.tar.zst", "/out", ExtractOptions{DeleteSource: true}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled extract: %v", err)
	}
	assertNoTemp(t, filepath.Join(dir, "out"))
	if _, err := os.Stat(filepath.Join(dir, "a.tar.zst")); err != nil {
		t.Errorf("archive deleted after a failed extraction: %v", err)
	}

	if os.Geteuid() != 0 {
		return
	}
	must(t, os.Chown(filepath.Join(dir, "out"), 1000, 50))
	res, err := f.Extract(ctx, "/a.tar.zst", "/out", ExtractOptions{})
	must(t, err)
	for _, p := range []string{"", "a.txt", "sub", "sub/link"} {
		e, err := f.Stat(filepath.Join(res.Path, p))
		must(t, err)
		if e.UID != 1000 || e.GID != 50 {
			t.Errorf("%s owned by %d:%d, want inherited 1000:50", p, e.UID, e.GID)
		}
	}
}

func TestCreateArchiveSafety(t *testing.T) {
	f, dir := newTestFS(t)
	makeTree(t, filepath.Join(dir, "src"))
	must(t, os.WriteFile(filepath.Join(dir, "taken.zip"), nil, 0o644))

	if _, err := f.CreateArchive(ctx, []string{"/src"}, "/taken.zip", CreateArchiveOptions{Format: FormatZip, DeleteSources: true}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("existing destination: %v", err)
	}
	if _, err := f.CreateArchive(ctx, []string{"/src"}, "/src/inside.zip", CreateArchiveOptions{Format: FormatZip}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("archive inside source: %v", err)
	}
	if _, err := f.CreateArchive(ctx, []string{"/src/a.txt", "/src/sub/deeper"}, "/x.zip", CreateArchiveOptions{Format: FormatZip}); !errors.Is(err, ErrInvalidArgument) {
		t.Errorf("sources in different directories: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "src/a.txt")); err != nil {
		t.Fatalf("source removed after a failure: %v", err)
	}

	_, err := f.CreateArchive(ctx, []string{"/src"}, "/new.tar.xz", CreateArchiveOptions{Format: FormatTarXz, DeleteSources: true})
	must(t, err)
	if _, err := os.Stat(filepath.Join(dir, "src")); !os.IsNotExist(err) {
		t.Errorf("sources not deleted: %v", err)
	}
	assertNoTemp(t, dir)
}
