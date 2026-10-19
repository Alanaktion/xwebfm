//go:build linux

package fsops

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"compress/bzip2"

	"github.com/bodgit/sevenzip"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"github.com/nwaples/rardecode/v2"
	"github.com/ulikunitz/xz"
	"golang.org/x/sys/unix"
)

const maxSkipped = 50

// Extract unpacks archive into destDir. Entries are written into a hidden
// temporary directory first; on success a single top-level entry is moved
// into destDir directly, otherwise the temporary directory is renamed after
// the archive ("photos.zip" → "photos", or "photos (2)" if taken). Failure
// or cancellation removes everything extracted so far.
//
// Archive contents are untrusted: names that are absolute or contain ".."
// are skipped, nothing is written through a symlink (each path component is
// opened with O_NOFOLLOW), hard links may only point inside the extraction,
// device nodes/FIFOs/sockets are skipped and setuid/setgid bits are cleared.
func (f *FS) Extract(ctx context.Context, archive, destDir string, o ExtractOptions) (res ExtractResult, err error) {
	if o.Ownership, err = o.Ownership.validate(OwnerInherit); err != nil {
		return res, err
	}
	archive, destDir = CleanPath(archive), CleanPath(destDir)
	info, ok := DetectArchive(path.Base(archive))
	if !ok {
		return res, fmt.Errorf("%w: %s is not a recognised archive", ErrInvalidArgument, path.Base(archive))
	}
	srcDir := path.Dir(archive)
	archive = path.Join(srcDir, info.First)

	dpfd, err := f.open(destDir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return res, err
	}
	defer unix.Close(dpfd)
	uid, gid, err := ownerFor(o.Ownership, dpfd)
	if err != nil {
		return res, &fs.PathError{Op: "stat", Path: destDir, Err: err}
	}

	tmp := ".xwebfm-extract-" + randomHex()
	if err := unix.Mkdirat(dpfd, tmp, 0o700); err != nil {
		return res, &fs.PathError{Op: "mkdir", Path: path.Join(destDir, tmp), Err: err}
	}
	tfd, err := unix.Openat(dpfd, tmp, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		unix.Unlinkat(dpfd, tmp, unix.AT_REMOVEDIR)
		return res, &fs.PathError{Op: "open", Path: path.Join(destDir, tmp), Err: err}
	}
	defer unix.Close(tfd)
	defer func() {
		if err != nil {
			removeAt(context.Background(), nil, dpfd, tmp, path.Join(destDir, tmp))
		}
	}()

	x := &extractor{
		ctx:     ctx,
		root:    tfd,
		uid:     uid,
		gid:     gid,
		keep:    o.Owner == OwnerKeep,
		prog:    o.Progress,
		dirs:    map[string]entryMeta{},
		display: archive,
	}
	vols, err := f.readArchive(ctx, archive, info, o.Password, x)
	if err != nil {
		return res, err
	}
	if err := x.finishDirs(); err != nil {
		return res, err
	}
	res.Entries, res.Skipped, res.Volumes = x.entries, x.skipped, vols

	if res.Path, err = f.placeExtracted(dpfd, tfd, tmp, destDir, info.Base, uid, gid); err != nil {
		return res, err
	}
	if o.DeleteSource {
		spfd, err := f.open(srcDir, unix.O_PATH|unix.O_DIRECTORY)
		if err != nil {
			return res, fmt.Errorf("extracted, but deleting the archive failed: %w", err)
		}
		defer unix.Close(spfd)
		for _, v := range vols {
			if err := unix.Unlinkat(spfd, v, 0); err != nil && err != unix.ENOENT {
				return res, fmt.Errorf("extracted, but deleting the archive failed: %w",
					&fs.PathError{Op: "remove", Path: path.Join(srcDir, v), Err: err})
			}
		}
	}
	return res, nil
}

// placeExtracted moves the extraction out of the temporary directory.
func (f *FS) placeExtracted(dpfd, tfd int, tmp, destDir, base string, uid, gid int) (string, error) {
	dfd, err := unix.Openat(tfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return "", &fs.PathError{Op: "open", Path: path.Join(destDir, tmp), Err: err}
	}
	dir := os.NewFile(uintptr(dfd), tmp)
	names, err := dir.Readdirnames(-1)
	dir.Close()
	if err != nil {
		return "", &fs.PathError{Op: "readdir", Path: path.Join(destDir, tmp), Err: err}
	}
	if len(names) == 1 {
		err := unix.Renameat2(tfd, names[0], dpfd, names[0], unix.RENAME_NOREPLACE)
		if err == nil {
			unix.Unlinkat(dpfd, tmp, unix.AT_REMOVEDIR)
			return path.Join(destDir, names[0]), nil
		}
		if err != unix.EEXIST {
			return "", &fs.PathError{Op: "rename", Path: path.Join(destDir, names[0]), Err: err}
		}
		// The name is taken: fall back to a directory named after the archive.
	}

	// The temp dir becomes the archive's directory: give it normal
	// permissions and the target owner.
	if err := unix.Fchownat(tfd, "", uid, gid, unix.AT_EMPTY_PATH); err != nil {
		return "", &fs.PathError{Op: "chown", Path: path.Join(destDir, tmp), Err: err}
	}
	if err := unix.Fchmod(tfd, 0o755); err != nil {
		return "", &fs.PathError{Op: "chmod", Path: path.Join(destDir, tmp), Err: err}
	}
	if base == "" || ValidName(base) != nil {
		base = "extracted"
	}
	for i := 1; i < 1000; i++ {
		name := base
		if i > 1 {
			name = fmt.Sprintf("%s (%d)", base, i)
		}
		err := unix.Renameat2(dpfd, tmp, dpfd, name, unix.RENAME_NOREPLACE)
		if err == nil {
			return path.Join(destDir, name), nil
		}
		if err != unix.EEXIST {
			return "", &fs.PathError{Op: "rename", Path: path.Join(destDir, name), Err: err}
		}
	}
	return "", &fs.PathError{Op: "rename", Path: path.Join(destDir, base), Err: fs.ErrExist}
}

// readArchive feeds every entry of the archive to x and returns the names
// of the volume files read.
func (f *FS) readArchive(ctx context.Context, archive string, info ArchiveInfo, password string, x *extractor) ([]string, error) {
	dir, first := path.Dir(archive), path.Base(archive)
	switch info.Format {
	case FormatRar:
		return f.readRar(dir, first, password, x)
	case FormatZip, Format7z:
		ra, size, vols, closeAll, err := f.openRandomAccess(dir, first, info.Split)
		if err != nil {
			return nil, err
		}
		defer closeAll()
		if info.Format == FormatZip {
			return vols, x.readZip(ra, size)
		}
		return vols, x.read7z(ra, size, password)
	}

	file, err := f.openRegular(archive)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	if st, err := file.Stat(); err == nil {
		x.prog.AddTotal(0, st.Size())
	}
	// Progress for streams counts compressed bytes consumed.
	in := &countingReader{r: file, prog: x.prog, ctx: ctx}
	x.countOutput = false

	var dec io.Reader
	tarFormat := info.Format
	switch info.Format {
	case FormatTar:
		dec = in
	case FormatTarGz, FormatGz:
		zr, err := gzip.NewReader(in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", archive, err)
		}
		defer zr.Close()
		dec = zr
	case FormatTarBz2, FormatBz2:
		dec = bzip2.NewReader(in)
	case FormatTarXz, FormatXz:
		zr, err := xz.NewReader(in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", archive, err)
		}
		dec = zr
	case FormatTarZst, FormatZst:
		zr, err := zstd.NewReader(in)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", archive, err)
		}
		defer zr.Close()
		dec = zr
	default:
		return nil, fmt.Errorf("%w: unsupported format %s", ErrInvalidArgument, info.Format)
	}

	vols := []string{first}
	switch tarFormat {
	case FormatGz, FormatBz2, FormatXz, FormatZst:
		// A bare compressed file: it may still be a tar (e.g. "backup.gz").
		br := bufio.NewReaderSize(dec, 4096)
		head, _ := br.Peek(512)
		if len(head) >= 262 && string(head[257:262]) == "ustar" {
			return vols, x.readTar(br)
		}
		st, _ := file.Stat()
		meta := entryMeta{mode: 0o644, mtime: time.Now(), uid: -1, gid: -1}
		if st != nil {
			meta.mtime = st.ModTime()
		}
		if err := x.file(info.Base, meta, br); err != nil {
			return nil, err
		}
		return vols, nil
	}
	return vols, x.readTar(dec)
}

// openRegular opens a regular file for reading without following a final
// symlink.
func (f *FS) openRegular(p string) (*os.File, error) {
	pfd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return nil, err
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return nil, &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, &fs.PathError{Op: "open", Path: p, Err: ErrNotRegular}
	}
	fd, err := unix.Open(procPath(pfd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	return os.NewFile(uintptr(fd), p), nil
}

// openRandomAccess opens a zip/7z archive, concatenating name.001, .002, …
// for split archives.
func (f *FS) openRandomAccess(dir, first string, split bool) (io.ReaderAt, int64, []string, func(), error) {
	var parts []*os.File
	closeAll := func() {
		for _, p := range parts {
			p.Close()
		}
	}
	names := []string{first}
	if split {
		stem := strings.TrimSuffix(first, "001")
		names = nil
		for i := 1; i < 1000; i++ {
			names = append(names, fmt.Sprintf("%s%03d", stem, i))
			if _, err := f.Stat(path.Join(dir, names[len(names)-1])); err != nil {
				names = names[:len(names)-1]
				break
			}
		}
	}
	var mr multiReaderAt
	for _, n := range names {
		file, err := f.openRegular(path.Join(dir, n))
		if err != nil {
			closeAll()
			return nil, 0, nil, nil, err
		}
		parts = append(parts, file)
		st, err := file.Stat()
		if err != nil {
			closeAll()
			return nil, 0, nil, nil, err
		}
		mr.parts = append(mr.parts, part{r: file, off: mr.size, size: st.Size()})
		mr.size += st.Size()
	}
	return &mr, mr.size, names, closeAll, nil
}

type part struct {
	r         io.ReaderAt
	off, size int64
}

// multiReaderAt presents consecutive files as one io.ReaderAt.
type multiReaderAt struct {
	parts []part
	size  int64
}

func (m *multiReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := 0
	for _, pt := range m.parts {
		if len(p) == 0 {
			break
		}
		if off >= pt.off+pt.size || off+int64(len(p)) <= pt.off {
			continue
		}
		rel := off - pt.off
		want := min(int64(len(p)), pt.size-rel)
		got, err := pt.r.ReadAt(p[:want], rel)
		n += got
		if err != nil && err != io.EOF {
			return n, err
		}
		if int64(got) < want {
			return n, io.ErrUnexpectedEOF
		}
		p, off = p[got:], off+int64(got)
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// volumeFS lets rardecode open further volumes, restricted to regular files
// directly inside one directory of the confined FS, and records which ones
// it opened.
type volumeFS struct {
	f   *FS
	dir string

	mu     sync.Mutex
	opened []string
}

func (v *volumeFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || strings.Contains(name, "/") {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	file, err := v.f.openRegular(path.Join(v.dir, name))
	if err != nil {
		return nil, err
	}
	v.mu.Lock()
	if !slices.Contains(v.opened, name) {
		v.opened = append(v.opened, name)
	}
	v.mu.Unlock()
	return file, nil
}

func (f *FS) readRar(dir, first, password string, x *extractor) ([]string, error) {
	vfs := &volumeFS{f: f, dir: dir}
	opts := []rardecode.Option{rardecode.FileSystem(vfs)}
	if password != "" {
		opts = append(opts, rardecode.Password(password))
	}
	// Headers first, for the progress total.
	if headers, err := rardecode.List(first, opts...); err == nil {
		var total int64
		for _, h := range headers {
			if !h.IsDir && h.LinkType == 0 {
				total += h.UnPackedSize
			}
		}
		x.prog.AddTotal(int64(len(headers)), total)
	}
	rc, err := rardecode.OpenReader(first, opts...)
	if err != nil {
		return nil, rarError(first, err)
	}
	defer rc.Close()
	x.countOutput = true
	for {
		if err := x.ctx.Err(); err != nil {
			return nil, err
		}
		h, err := rc.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, rarError(first, err)
		}
		meta := entryMeta{mode: uint32(h.Mode().Perm()), mtime: h.ModificationTime, uid: -1, gid: -1}
		if h.UnixOwnerUserID != nil {
			meta.uid = int(*h.UnixOwnerUserID)
		}
		if h.UnixOwnerGroupID != nil {
			meta.gid = int(*h.UnixOwnerGroupID)
		}
		switch {
		case h.IsDir:
			err = x.dir(h.Name, meta)
		case h.LinkType == rardecode.LinkTypeUnixSymlink || h.LinkType == rardecode.LinkTypeWindowsSymlink:
			err = x.symlink(h.Name, h.LinkTarget, meta)
		case h.LinkType == rardecode.LinkTypeHardLink || h.LinkType == rardecode.LinkTypeFileCopy:
			err = x.hardlink(h.Name, h.LinkTarget)
		case h.LinkType != 0:
			x.skip(h.Name, "unsupported link type")
		default:
			err = x.file(h.Name, meta, rc)
		}
		if err != nil {
			return nil, rarError(first, err)
		}
	}
	vfs.mu.Lock()
	defer vfs.mu.Unlock()
	return slices.Clone(vfs.opened), nil
}

func rarError(name string, err error) error {
	if errors.Is(err, rardecode.ErrArchiveEncrypted) || errors.Is(err, rardecode.ErrArchivedFileEncrypted) ||
		errors.Is(err, rardecode.ErrBadPassword) {
		return fmt.Errorf("%s: %w", name, ErrEncrypted)
	}
	return err
}

func (x *extractor) readZip(ra io.ReaderAt, size int64) error {
	zr, err := zip.NewReader(ra, size)
	if err != nil {
		return err
	}
	var total int64
	for _, zf := range zr.File {
		total += int64(zf.UncompressedSize64)
	}
	x.prog.AddTotal(int64(len(zr.File)), total)
	x.countOutput = true
	for _, zf := range zr.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		if zf.Flags&0x1 != 0 {
			return fmt.Errorf("%s: encrypted zip entries are not supported", zf.Name)
		}
		name := strings.ReplaceAll(zf.Name, `\`, "/")
		mode := zf.Mode()
		meta := entryMeta{mode: uint32(mode.Perm()), mtime: zf.Modified, uid: -1, gid: -1}
		switch {
		case mode&fs.ModeSymlink != 0:
			target, err := x.readSmall(zf.Open)
			if err != nil {
				return err
			}
			err = x.symlink(name, target, meta)
			if err != nil {
				return err
			}
		case mode.IsDir() || strings.HasSuffix(name, "/"):
			if err := x.dir(name, meta); err != nil {
				return err
			}
		case mode.Type() != 0:
			x.skip(name, "special file")
		default:
			rc, err := zf.Open()
			if err != nil {
				return err
			}
			err = x.file(name, meta, rc)
			rc.Close()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (x *extractor) read7z(ra io.ReaderAt, size int64, password string) error {
	// With an encrypted header, a wrong or missing password surfaces as an
	// arbitrary decoding error, so any failure with a password set (or one
	// mentioning encryption) is reported as a password problem.
	sz, err := sevenzip.NewReaderWithPassword(ra, size, password)
	if err != nil {
		if password != "" || strings.Contains(err.Error(), "password") || strings.Contains(err.Error(), "aes") {
			return fmt.Errorf("%w (%v)", ErrEncrypted, err)
		}
		return err
	}
	var total int64
	for _, sf := range sz.File {
		total += int64(sf.UncompressedSize)
	}
	x.prog.AddTotal(int64(len(sz.File)), total)
	x.countOutput = true
	for _, sf := range sz.File {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		name := strings.ReplaceAll(sf.Name, `\`, "/")
		mode := sf.Mode()
		meta := entryMeta{mode: uint32(mode.Perm()), mtime: sf.Modified, uid: -1, gid: -1}
		var err error
		switch {
		case mode&fs.ModeSymlink != 0:
			var target string
			if target, err = x.readSmall(sf.Open); err == nil {
				err = x.symlink(name, target, meta)
			}
		case mode.IsDir():
			err = x.dir(name, meta)
		case mode.Type() != 0:
			x.skip(name, "special file")
		default:
			var rc io.ReadCloser
			if rc, err = sf.Open(); err == nil {
				err = x.file(name, meta, rc)
				rc.Close()
			}
		}
		if err != nil {
			if password != "" && !errors.Is(err, context.Canceled) {
				return fmt.Errorf("%s: %w (%v)", name, ErrEncrypted, err)
			}
			return err
		}
	}
	return nil
}

func (x *extractor) readTar(r io.Reader) error {
	tr := tar.NewReader(r)
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		meta := entryMeta{mode: uint32(h.Mode) & 0o7777, mtime: h.ModTime, uid: h.Uid, gid: h.Gid}
		switch h.Typeflag {
		case tar.TypeReg, '\x00':
			err = x.file(h.Name, meta, tr)
		case tar.TypeDir:
			err = x.dir(h.Name, meta)
		case tar.TypeSymlink:
			err = x.symlink(h.Name, h.Linkname, meta)
		case tar.TypeLink:
			err = x.hardlink(h.Name, h.Linkname)
		case tar.TypeXGlobalHeader, tar.TypeXHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			// Metadata records, consumed by archive/tar.
		default:
			x.skip(h.Name, "special file")
		}
		if err != nil {
			return err
		}
	}
}

// readSmall reads a symlink target stored as entry content.
func (x *extractor) readSmall(open func() (io.ReadCloser, error)) (string, error) {
	rc, err := open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	b, err := io.ReadAll(io.LimitReader(rc, unix.PathMax))
	x.prog.addBytes(int64(len(b)))
	return string(b), err
}

type countingReader struct {
	r    io.Reader
	prog *Progress
	ctx  context.Context
}

func (c *countingReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := c.r.Read(p)
	c.prog.addBytes(int64(n))
	return n, err
}

type entryMeta struct {
	mode     uint32
	mtime    time.Time
	uid, gid int // -1 when the archive doesn't record them
}

// extractor writes archive entries beneath root (the temp dir fd).
type extractor struct {
	ctx         context.Context
	root        int
	uid, gid    int  // target owner from the policy; -1 = see keep
	keep        bool // use owners recorded in the archive
	prog        *Progress
	countOutput bool // progress counts written bytes (random-access formats)
	display     string

	dirs    map[string]entryMeta // every directory created, for finishDirs
	entries int
	skipped []string
}

// clean validates an entry name, returning "" if it must be skipped.
func (x *extractor) clean(name string) (string, bool) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", false
	}
	if strings.HasPrefix(name, "/") {
		return "", false
	}
	c := path.Clean(name)
	if c == "." {
		return "", true // the archive's own root entry ("./")
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

func (x *extractor) skip(name, reason string) {
	x.prog.entryDone()
	if len(x.skipped) < maxSkipped {
		x.skipped = append(x.skipped, name+": "+reason)
	}
}

// owner returns the uid/gid to apply to an entry (-1 leaves it as root).
func (x *extractor) owner(m entryMeta) (int, int) {
	if !x.keep {
		return x.uid, x.gid
	}
	return m.uid, m.gid
}

// parent returns an fd for the directory that will contain rel (creating
// missing directories when create is set) and rel's last component. No
// component is ever followed through a symlink. The fd must be closed
// unless it is x.root.
func (x *extractor) parent(rel string, create bool) (int, string, error) {
	comps := strings.Split(rel, "/")
	cur := x.root
	for i, c := range comps[:len(comps)-1] {
		if create {
			err := unix.Mkdirat(cur, c, 0o700)
			if err == nil {
				d := strings.Join(comps[:i+1], "/")
				if _, ok := x.dirs[d]; !ok {
					x.dirs[d] = entryMeta{mode: 0o755, mtime: time.Now(), uid: -1, gid: -1}
				}
			} else if err != unix.EEXIST {
				x.closeFD(cur)
				return -1, "", err
			}
		}
		next, err := unix.Openat(cur, c, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		x.closeFD(cur)
		if err != nil {
			if err == unix.ELOOP || err == unix.ENOTDIR {
				err = fmt.Errorf("path goes through a symlink or file (%s)", strings.Join(comps[:i+1], "/"))
			}
			return -1, "", err
		}
		cur = next
	}
	return cur, comps[len(comps)-1], nil
}

func (x *extractor) closeFD(fd int) {
	if fd != x.root {
		unix.Close(fd)
	}
}

func (x *extractor) file(name string, m entryMeta, r io.Reader) error {
	rel, ok := x.clean(name)
	if !ok || rel == "" {
		x.skip(name, "unsafe path")
		return nil
	}
	pfd, last, err := x.parent(rel, true)
	if err != nil {
		x.skip(name, err.Error())
		return nil
	}
	defer x.closeFD(pfd)
	unix.Unlinkat(pfd, last, 0) // a later duplicate entry replaces an earlier one
	fd, err := unix.Openat(pfd, last, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		x.skip(name, err.Error())
		return nil
	}
	file := os.NewFile(uintptr(fd), rel)
	defer file.Close()
	for {
		if err := x.ctx.Err(); err != nil {
			return err
		}
		n, err := io.CopyN(file, r, copyChunk)
		if x.countOutput {
			x.prog.addBytes(n)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	uid, gid := x.owner(m)
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return &fs.PathError{Op: "chown", Path: rel, Err: err}
	}
	// Never extract setuid/setgid files.
	if err := unix.Fchmod(fd, m.mode&0o1777); err != nil {
		return &fs.PathError{Op: "chmod", Path: rel, Err: err}
	}
	if !m.mtime.IsZero() {
		ts := []unix.Timespec{unix.NsecToTimespec(time.Now().UnixNano()), unix.NsecToTimespec(m.mtime.UnixNano())}
		unix.UtimesNanoAt(unix.AT_FDCWD, procPath(fd), ts, 0)
	}
	x.done()
	return file.Close()
}

func (x *extractor) dir(name string, m entryMeta) error {
	rel, ok := x.clean(name)
	if !ok {
		x.skip(name, "unsafe path")
		return nil
	}
	if rel == "" {
		return nil
	}
	pfd, last, err := x.parent(rel, true)
	if err != nil {
		x.skip(name, err.Error())
		return nil
	}
	defer x.closeFD(pfd)
	if err := unix.Mkdirat(pfd, last, 0o700); err != nil && err != unix.EEXIST {
		x.skip(name, err.Error())
		return nil
	}
	if m.mode == 0 {
		m.mode = 0o755
	}
	x.dirs[rel] = m
	x.done()
	return nil
}

func (x *extractor) symlink(name, target string, m entryMeta) error {
	rel, ok := x.clean(name)
	if !ok || rel == "" || target == "" {
		x.skip(name, "unsafe path")
		return nil
	}
	pfd, last, err := x.parent(rel, true)
	if err != nil {
		x.skip(name, err.Error())
		return nil
	}
	defer x.closeFD(pfd)
	unix.Unlinkat(pfd, last, 0)
	if err := unix.Symlinkat(target, pfd, last); err != nil {
		x.skip(name, err.Error())
		return nil
	}
	uid, gid := x.owner(m)
	unix.Fchownat(pfd, last, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
	if !m.mtime.IsZero() {
		ts := []unix.Timespec{unix.NsecToTimespec(time.Now().UnixNano()), unix.NsecToTimespec(m.mtime.UnixNano())}
		unix.UtimesNanoAt(pfd, last, ts, unix.AT_SYMLINK_NOFOLLOW)
	}
	x.done()
	return nil
}

// hardlink links rel to target, which must be an already extracted regular
// file inside this extraction.
func (x *extractor) hardlink(name, target string) error {
	rel, ok := x.clean(name)
	trel, tok := x.clean(target)
	if !ok || !tok || rel == "" || trel == "" {
		x.skip(name, "hard link outside the archive")
		return nil
	}
	tfd, tlast, err := x.parent(trel, false)
	if err != nil {
		x.skip(name, "hard link target missing")
		return nil
	}
	defer x.closeFD(tfd)
	var st unix.Stat_t
	if err := unix.Fstatat(tfd, tlast, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		x.skip(name, "hard link target missing")
		return nil
	}
	pfd, last, err := x.parent(rel, true)
	if err != nil {
		x.skip(name, err.Error())
		return nil
	}
	defer x.closeFD(pfd)
	unix.Unlinkat(pfd, last, 0)
	if err := unix.Linkat(tfd, tlast, pfd, last, 0); err != nil {
		x.skip(name, err.Error())
		return nil
	}
	x.done()
	return nil
}

func (x *extractor) done() {
	x.entries++
	x.prog.entryDone()
}

// finishDirs applies directory metadata once all entries exist, deepest
// first, so directories stay writable (0700) while being filled and their
// mtimes aren't disturbed afterwards.
func (x *extractor) finishDirs() error {
	rels := make([]string, 0, len(x.dirs))
	for r := range x.dirs {
		rels = append(rels, r)
	}
	sort.Slice(rels, func(i, j int) bool { return strings.Count(rels[i], "/") > strings.Count(rels[j], "/") })
	for _, rel := range rels {
		m := x.dirs[rel]
		pfd, last, err := x.parent(rel, false)
		if err != nil {
			continue
		}
		fd, err := unix.Openat(pfd, last, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		x.closeFD(pfd)
		if err != nil {
			continue
		}
		uid, gid := x.owner(m)
		unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH)
		unix.Fchmodat(unix.AT_FDCWD, procPath(fd), m.mode&0o1777, 0)
		if !m.mtime.IsZero() {
			ts := []unix.Timespec{unix.NsecToTimespec(time.Now().UnixNano()), unix.NsecToTimespec(m.mtime.UnixNano())}
			unix.UtimesNanoAt(unix.AT_FDCWD, procPath(fd), ts, 0)
		}
		unix.Close(fd)
	}
	return nil
}

func randomHex() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}
