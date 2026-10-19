//go:build linux

package fsops

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dsnet/compress/bzip2"
	"github.com/klauspost/compress/flate"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/ulikunitz/xz"
	"golang.org/x/sys/unix"
)

// sevenZip is the 7-Zip binary used to create .7z archives, if installed.
var sevenZip = sync.OnceValue(func() string {
	for _, name := range []string{"7zz", "7z", "7za"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
})

// SevenZipAvailable reports whether .7z archives can be created.
func SevenZipAvailable() bool { return sevenZip() != "" }

// CreateArchive writes the sources, which must share a parent directory, to
// a new archive at dst. Entries are named after each source's base name.
// The archive is written to a temporary file and renamed into place, so a
// failed or cancelled run leaves nothing behind; sources are deleted (with
// DeleteSources) only after that rename.
func (f *FS) CreateArchive(ctx context.Context, sources []string, dst string, o CreateArchiveOptions) (e Entry, err error) {
	if o.Ownership, err = o.Ownership.validate(OwnerInherit); err != nil {
		return e, err
	}
	switch {
	case o.Format == Format7z && !SevenZipAvailable():
		return e, fmt.Errorf("%w: creating 7z archives needs the 7z binary installed", ErrInvalidArgument)
	case o.Format != Format7z && !containsFormat(CreateFormats, o.Format):
		return e, fmt.Errorf("%w: can't create %q archives", ErrInvalidArgument, o.Format)
	case len(sources) == 0:
		return e, fmt.Errorf("%w: nothing to archive", ErrInvalidArgument)
	}
	dst = CleanPath(dst)
	srcDir := path.Dir(CleanPath(sources[0]))
	names := make([]string, len(sources))
	for i, s := range sources {
		s = CleanPath(s)
		if s == "/" || path.Dir(s) != srcDir {
			return e, fmt.Errorf("%w: archive sources must be in the same directory", ErrInvalidArgument)
		}
		if isWithin(dst, s) {
			return e, fmt.Errorf("%w: can't write the archive inside %s", ErrInvalidArgument, s)
		}
		sources[i], names[i] = s, path.Base(s)
	}

	dpfd, name, err := f.openParent(dst)
	if err != nil {
		return e, err
	}
	defer unix.Close(dpfd)
	if err := ValidName(name); err != nil {
		return e, err
	}
	var st unix.Stat_t
	if !o.Overwrite && unix.Fstatat(dpfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
		return e, &fs.PathError{Op: "create", Path: dst, Err: fs.ErrExist}
	}
	uid, gid, err := ownerFor(o.Ownership, dpfd)
	if err != nil {
		return e, &fs.PathError{Op: "stat", Path: path.Dir(dst), Err: err}
	}
	if o.Progress != nil {
		for _, s := range sources {
			if n, b, err := f.Usage(ctx, s); err == nil {
				o.Progress.AddTotal(n, b)
			}
		}
	}

	tmp := ".xwebfm-archive-" + randomHex() + o.Format.Extension()
	defer func() {
		if err != nil {
			unix.Unlinkat(dpfd, tmp, 0)
		}
	}()
	if o.Format == Format7z {
		err = f.create7z(ctx, srcDir, names, dpfd, tmp, o.Progress)
	} else {
		err = f.createNative(ctx, sources, dpfd, tmp, o)
	}
	if err != nil {
		return e, err
	}

	fd, err := unix.Openat(dpfd, tmp, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return e, &fs.PathError{Op: "open", Path: dst, Err: err}
	}
	err = unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH)
	if err == nil {
		err = unix.Fchmodat(unix.AT_FDCWD, procPath(fd), 0o644, 0)
	}
	unix.Close(fd)
	if err != nil {
		return e, &fs.PathError{Op: "chown", Path: dst, Err: err}
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if o.Overwrite {
		flags = 0
	}
	if err = unix.Renameat2(dpfd, tmp, dpfd, name, flags); err != nil {
		return e, &fs.PathError{Op: "rename", Path: dst, Err: err}
	}

	if o.DeleteSources {
		for _, s := range sources {
			// Not cancellable: the archive is complete, so finish the job.
			if derr := f.Delete(context.Background(), s, DeleteOptions{}); derr != nil {
				e, _ = f.Stat(dst)
				return e, fmt.Errorf("archive created, but deleting the originals failed: %w", derr)
			}
		}
	}
	return f.Stat(dst)
}

func containsFormat(list []ArchiveFormat, f ArchiveFormat) bool {
	for _, l := range list {
		if l == f {
			return true
		}
	}
	return false
}

func (f *FS) createNative(ctx context.Context, sources []string, dpfd int, tmp string, o CreateArchiveOptions) error {
	fd, err := unix.Openat(dpfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &fs.PathError{Op: "create", Path: tmp, Err: err}
	}
	file := os.NewFile(uintptr(fd), tmp)
	defer file.Close()
	bw := bufio.NewWriterSize(file, 1<<20)
	aw, err := f.newArchiveWriter(ctx, o.Format, bw, o.Progress)
	if err != nil {
		return err
	}
	for _, s := range sources {
		if err := f.addTree(ctx, aw, s); err != nil {
			return err
		}
	}
	if err := aw.Close(); err != nil {
		return err
	}
	if err := bw.Flush(); err != nil {
		return err
	}
	return file.Close()
}

// archiveWriter adds entries to a tar- or zip-based archive.
type archiveWriter interface {
	// add writes the entry open as fd (O_PATH|O_NOFOLLOW).
	add(fd int, st *unix.Stat_t, name, p string) error
	Close() error
}

func (f *FS) newArchiveWriter(ctx context.Context, format ArchiveFormat, w io.Writer, prog *Progress) (archiveWriter, error) {
	b := entryBase{ctx: ctx, f: f, prog: prog}
	if format == FormatZip {
		zw := zip.NewWriter(w)
		zw.RegisterCompressor(zip.Deflate, func(w io.Writer) (io.WriteCloser, error) {
			return flate.NewWriter(w, flate.DefaultCompression)
		})
		return &zipWriter{entryBase: b, zw: zw}, nil
	}
	var (
		comp io.WriteCloser
		err  error
	)
	switch format {
	case FormatTar:
		comp = nopCloser{w}
	case FormatTarGz:
		comp = gzip.NewWriter(w)
	case FormatTarBz2:
		comp, err = bzip2.NewWriter(w, &bzip2.WriterConfig{Level: bzip2.DefaultCompression})
	case FormatTarXz:
		comp, err = xz.NewWriter(w)
	case FormatTarZst:
		comp, err = zstd.NewWriter(w)
	default:
		return nil, fmt.Errorf("%w: can't create %q archives", ErrInvalidArgument, format)
	}
	if err != nil {
		return nil, err
	}
	return &tarWriter{entryBase: b, comp: comp, tw: tar.NewWriter(comp)}, nil
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// addTree adds p and, for directories, everything beneath it, named
// relative to p's parent.
func (f *FS) addTree(ctx context.Context, aw archiveWriter, p string) error {
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	base := path.Base(p)
	if p == "/" {
		base = "root"
	}
	return walk(ctx, fd, p, true, func(fd int, fp string, st *unix.Stat_t) error {
		name := base
		if fp != p {
			name = path.Join(base, fp[len(p):])
		}
		return aw.add(fd, st, name, fp)
	})
}

type entryBase struct {
	ctx  context.Context
	f    *FS
	prog *Progress
}

// copyFile copies the regular file open as fd (O_PATH) to w, checking for
// cancellation between chunks.
func (b *entryBase) copyFile(w io.Writer, fd int, size int64, p string) error {
	rfd, err := unix.Open(procPath(fd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	file := os.NewFile(uintptr(rfd), p)
	defer file.Close()
	var done int64
	for done < size {
		if err := b.ctx.Err(); err != nil {
			return err
		}
		n, err := io.CopyN(w, file, min(copyChunk, size-done))
		done += n
		b.prog.addBytes(n)
		if err != nil {
			// The header promised size bytes; a file that shrank while being
			// archived can't be represented, so fail rather than corrupt it.
			return &fs.PathError{Op: "read", Path: p, Err: err}
		}
	}
	return nil
}

func readlink(fd int, p string) (string, error) {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(fd, "", buf)
	if err != nil {
		return "", &fs.PathError{Op: "readlink", Path: p, Err: err}
	}
	return string(buf[:n]), nil
}

type tarWriter struct {
	entryBase
	comp io.WriteCloser
	tw   *tar.Writer
}

func (t *tarWriter) add(fd int, st *unix.Stat_t, name, p string) error {
	t.prog.setCurrent(p)
	defer t.prog.entryDone()
	hdr := &tar.Header{
		Name:    name,
		Mode:    int64(st.Mode & 0o7777),
		Uid:     int(st.Uid),
		Gid:     int(st.Gid),
		Uname:   t.f.Owners.UserName(st.Uid),
		Gname:   t.f.Owners.GroupName(st.Gid),
		ModTime: time.Unix(st.Mtim.Unix()),
		Format:  tar.FormatPAX,
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		hdr.Typeflag, hdr.Name = tar.TypeDir, name+"/"
		return t.tw.WriteHeader(hdr)
	case unix.S_IFLNK:
		target, err := readlink(fd, p)
		if err != nil {
			return err
		}
		hdr.Typeflag, hdr.Linkname = tar.TypeSymlink, target
		return t.tw.WriteHeader(hdr)
	case unix.S_IFREG:
		hdr.Typeflag, hdr.Size = tar.TypeReg, st.Size
		if err := t.tw.WriteHeader(hdr); err != nil {
			return err
		}
		return t.copyFile(t.tw, fd, st.Size, p)
	}
	return nil // FIFOs, devices and sockets are skipped
}

func (t *tarWriter) Close() error {
	if err := t.tw.Close(); err != nil {
		return err
	}
	return t.comp.Close()
}

type zipWriter struct {
	entryBase
	zw *zip.Writer
}

func (z *zipWriter) add(fd int, st *unix.Stat_t, name, p string) error {
	z.prog.setCurrent(p)
	defer z.prog.entryDone()
	hdr := &zip.FileHeader{Name: name, Modified: time.Unix(st.Mtim.Unix()), Method: zip.Store}
	mode := fs.FileMode(st.Mode & 0o777)
	if st.Mode&unix.S_ISUID != 0 {
		mode |= fs.ModeSetuid
	}
	if st.Mode&unix.S_ISGID != 0 {
		mode |= fs.ModeSetgid
	}
	if st.Mode&unix.S_ISVTX != 0 {
		mode |= fs.ModeSticky
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFDIR:
		hdr.Name += "/"
		hdr.SetMode(mode | fs.ModeDir)
		_, err := z.zw.CreateHeader(hdr)
		return err
	case unix.S_IFLNK:
		target, err := readlink(fd, p)
		if err != nil {
			return err
		}
		hdr.SetMode(mode | fs.ModeSymlink)
		w, err := z.zw.CreateHeader(hdr)
		if err == nil {
			_, err = io.WriteString(w, target)
		}
		return err
	case unix.S_IFREG:
		hdr.SetMode(mode)
		hdr.Method = zip.Deflate
		hdr.UncompressedSize64 = uint64(st.Size) // lets zip decide on zip64 up front
		w, err := z.zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		return z.copyFile(w, fd, st.Size, p)
	}
	return nil
}

func (z *zipWriter) Close() error { return z.zw.Close() }

var percentRe = regexp.MustCompile(`(\d+)%`)

// create7z runs the 7-Zip binary. Both the working directory (the sources'
// parent) and the output directory are passed as /proc/self/fd paths of
// descriptors opened through the confined FS, so neither can be redirected
// by a symlink; -snl stores symlinks as links instead of following them.
// 7-Zip walks subdirectories itself, by path.
func (f *FS) create7z(ctx context.Context, srcDir string, names []string, dpfd int, tmp string, prog *Progress) error {
	sfd, err := f.open(srcDir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	defer unix.Close(sfd)
	for _, n := range names {
		if err := ValidName(n); err != nil {
			return err
		}
	}
	// The destination directory is passed to the child as fd 3 (a dup, so
	// closing the *os.File doesn't close the caller's descriptor).
	dup, err := unix.FcntlInt(uintptr(dpfd), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return err
	}
	destDir := os.NewFile(uintptr(dup), "dest")
	defer destDir.Close()
	args := append([]string{"a", "-t7z", "-snl", "-bsp1", "-bso0", "-bse2", "-y", "--", "/proc/self/fd/3/" + tmp}, names...)
	cmd := exec.CommandContext(ctx, sevenZip(), args...)
	cmd.Dir = procPath(sfd)
	cmd.ExtraFiles = []*os.File{destDir}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	total := prog.Snapshot().TotalBytes
	sc := bufio.NewScanner(stdout)
	sc.Split(splitProgress)
	for sc.Scan() {
		prog.setCurrent(srcDir)
		if m := percentRe.FindSubmatch(sc.Bytes()); m != nil {
			pct, _ := strconv.Atoi(string(m[1]))
			prog.setBytes(total * int64(pct) / 100)
		}
	}
	err = cmd.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return fmt.Errorf("7z failed: %v: %s", err, msg)
	}
	// 7-Zip doesn't report per-entry progress; mark everything done.
	p := prog.Snapshot()
	prog.setBytes(p.TotalBytes)
	prog.addEntries(p.TotalEntries - p.Entries)
	return nil
}

// splitProgress splits 7-Zip's progress output, which redraws one line
// using backspaces, into tokens.
func splitProgress(data []byte, atEOF bool) (int, []byte, error) {
	for i, b := range data {
		if b == '\b' || b == '\r' || b == '\n' {
			return i + 1, data[:i], nil
		}
	}
	if atEOF && len(data) > 0 {
		return len(data), data, nil
	}
	return 0, nil, nil
}
