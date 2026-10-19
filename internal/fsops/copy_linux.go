//go:build linux

package fsops

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"

	"golang.org/x/sys/unix"
)

// Copy recursively copies src to dst (the full destination path, not its
// parent directory). Directories, regular files, symlinks, FIFOs and device
// nodes are copied; sockets are skipped. Regular files are reflinked when
// o.Reflink allows and the filesystem supports it. If ctx is cancelled the
// copy stops and a newly created destination is removed again.
func (f *FS) Copy(ctx context.Context, src, dst string, o CopyOptions) (res CopyResult, err error) {
	switch o.Reflink {
	case "":
		o.Reflink = ReflinkAuto
	case ReflinkAuto, ReflinkAlways, ReflinkNever:
	default:
		return res, fmt.Errorf("%w: unknown reflink mode %q", ErrInvalidArgument, o.Reflink)
	}
	if o.Ownership, err = o.Ownership.validate(OwnerKeep); err != nil {
		return res, err
	}
	src, dst = CleanPath(src), CleanPath(dst)
	if isWithin(dst, src) {
		return res, fmt.Errorf("%w: cannot copy %s into itself", ErrInvalidArgument, src)
	}

	spfd, err := f.open(src, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return res, err
	}
	defer unix.Close(spfd)
	var st unix.Stat_t
	if err := unix.Fstat(spfd, &st); err != nil {
		return res, &fs.PathError{Op: "stat", Path: src, Err: err}
	}

	dpfd, name, err := f.openParent(dst)
	if err != nil {
		return res, err
	}
	defer unix.Close(dpfd)
	var existing unix.Stat_t
	exists := unix.Fstatat(dpfd, name, &existing, unix.AT_SYMLINK_NOFOLLOW) == nil
	if exists && !o.Overwrite {
		return res, &fs.PathError{Op: "copy", Path: dst, Err: fs.ErrExist}
	}
	if exists && existing.Dev == st.Dev && existing.Ino == st.Ino {
		return res, fmt.Errorf("%w: source and destination are the same file", ErrInvalidArgument)
	}

	c := &copier{ctx: ctx, o: o}
	if c.uid, c.gid, err = ownerFor(o.Ownership, dpfd); err != nil {
		return res, &fs.PathError{Op: "stat", Path: path.Dir(dst), Err: err}
	}
	err = c.copy(spfd, &st, dpfd, name, dst)
	if err != nil && !exists {
		// Don't leave a partial copy behind (even when cancelled).
		removeAt(context.Background(), nil, dpfd, name, dst)
	}
	return c.res, err
}

// isWithin reports whether p is dir or inside it.
func isWithin(p, dir string) bool {
	return p == dir || dir == "/" || strings.HasPrefix(p, dir+"/")
}

type copier struct {
	ctx      context.Context
	o        CopyOptions
	uid, gid int // target owner; -1 keeps the source's
	res      CopyResult
	// The first directory created, so a copy into a subdirectory of the
	// source (only reachable via symlinks or bind mounts) can't recurse.
	rootDev, rootIno uint64
}

// owner returns the target uid/gid for a source file.
func (c *copier) owner(st *unix.Stat_t) (int, int) {
	uid, gid := c.uid, c.gid
	if uid < 0 {
		uid = int(st.Uid)
	}
	if gid < 0 {
		gid = int(st.Gid)
	}
	return uid, gid
}

// copy copies the file open as srcfd (O_PATH|O_NOFOLLOW) to name in dpfd.
func (c *copier) copy(srcfd int, st *unix.Stat_t, dpfd int, name, p string) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	c.o.Progress.setCurrent(p)
	isDir := st.Mode&unix.S_IFMT == unix.S_IFDIR
	if c.o.Overwrite {
		// Replace existing non-directories by unlinking them, so writing never
		// goes through a hard link or symlink into some other file.
		var ex unix.Stat_t
		if unix.Fstatat(dpfd, name, &ex, unix.AT_SYMLINK_NOFOLLOW) == nil {
			exDir := ex.Mode&unix.S_IFMT == unix.S_IFDIR
			switch {
			case exDir && !isDir:
				return &fs.PathError{Op: "copy", Path: p, Err: unix.EISDIR}
			case !exDir && isDir:
				return &fs.PathError{Op: "copy", Path: p, Err: unix.ENOTDIR}
			case !exDir:
				if err := unix.Unlinkat(dpfd, name, 0); err != nil && err != unix.ENOENT {
					return &fs.PathError{Op: "remove", Path: p, Err: err}
				}
			}
		}
	}

	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
		return c.copyFile(srcfd, st, dpfd, name, p)
	case unix.S_IFDIR:
		return c.copyDir(srcfd, st, dpfd, name, p)
	case unix.S_IFLNK:
		return c.copySymlink(srcfd, st, dpfd, name, p)
	case unix.S_IFIFO, unix.S_IFCHR, unix.S_IFBLK:
		return c.copyNode(st, dpfd, name, p)
	}
	c.o.Progress.entryDone()
	c.res.Skipped++
	return nil
}

func (c *copier) copyFile(srcfd int, st *unix.Stat_t, dpfd int, name, p string) (err error) {
	sfd, err := unix.Open(procPath(srcfd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	srcFile := os.NewFile(uintptr(sfd), p)
	defer srcFile.Close()

	dfd, err := unix.Openat(dpfd, name, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return &fs.PathError{Op: "create", Path: p, Err: err}
	}
	dstFile := os.NewFile(uintptr(dfd), p)
	defer func() {
		if cerr := dstFile.Close(); cerr != nil && err == nil {
			err = &fs.PathError{Op: "close", Path: p, Err: cerr}
		}
	}()

	reflinked := false
	if c.o.Reflink != ReflinkNever {
		cerr := unix.IoctlFileClone(dfd, sfd)
		switch {
		case cerr == nil:
			reflinked = true
		case !reflinkUnsupported(cerr):
			return &fs.PathError{Op: "reflink", Path: p, Err: cerr}
		case c.o.Reflink == ReflinkAlways:
			return &fs.PathError{Op: "reflink", Path: p, Err: fmt.Errorf("%w (%v)", ErrReflinkUnsupported, cerr)}
		}
	}
	n := st.Size
	if reflinked {
		c.res.Reflinked++
		c.o.Progress.addBytes(n)
	} else if n, err = c.copyData(dstFile, srcFile, p); err != nil {
		return err
	}
	if err := c.applyMeta(dfd, st, p); err != nil {
		return err
	}
	c.o.Progress.entryDone()
	c.res.Files++
	c.res.Bytes += n
	return nil
}

// copyChunk bounds each copy_file_range call so cancellation and progress
// stay responsive on large files.
const copyChunk = 8 << 20

func (c *copier) copyData(dst, src *os.File, p string) (int64, error) {
	var total int64
	for {
		if err := c.ctx.Err(); err != nil {
			return total, err
		}
		n, err := io.CopyN(dst, src, copyChunk)
		total += n
		c.o.Progress.addBytes(n)
		if err == io.EOF {
			return total, nil
		}
		if err != nil {
			return total, &fs.PathError{Op: "copy", Path: p, Err: err}
		}
	}
}

func (c *copier) copyDir(srcfd int, st *unix.Stat_t, dpfd int, name, p string) error {
	created := true
	if err := unix.Mkdirat(dpfd, name, 0o700); err != nil {
		if err != unix.EEXIST || !c.o.Overwrite {
			return &fs.PathError{Op: "mkdir", Path: p, Err: err}
		}
		created = false // merging into an existing directory
	}
	dfd, err := unix.Openat(dpfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(dfd)
	if c.rootIno == 0 {
		var dst unix.Stat_t
		if err := unix.Fstat(dfd, &dst); err == nil {
			c.rootDev, c.rootIno = dst.Dev, dst.Ino
		}
	}

	sdfd, err := unix.Openat(srcfd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	sdir := os.NewFile(uintptr(sdfd), p)
	defer sdir.Close()
	names, err := sdir.Readdirnames(-1)
	if err != nil {
		return &fs.PathError{Op: "readdir", Path: p, Err: err}
	}
	for _, n := range names {
		cfd, err := unix.Openat(sdfd, n, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "open", Path: path.Join(p, n), Err: err}
		}
		var cst unix.Stat_t
		err = unix.Fstat(cfd, &cst)
		if err == nil && !(cst.Dev == c.rootDev && cst.Ino == c.rootIno) {
			err = c.copy(cfd, &cst, dfd, n, path.Join(p, n))
		}
		unix.Close(cfd)
		if err != nil {
			return err
		}
	}

	c.o.Progress.entryDone()
	if created {
		// Applied last: the directory stays 0700 while being filled, and its
		// mtime isn't disturbed by creating children.
		if err := c.applyMeta(dfd, st, p); err != nil {
			return err
		}
	}
	c.res.Dirs++
	return nil
}

func (c *copier) copySymlink(srcfd int, st *unix.Stat_t, dpfd int, name, p string) error {
	buf := make([]byte, unix.PathMax)
	n, err := unix.Readlinkat(srcfd, "", buf)
	if err != nil {
		return &fs.PathError{Op: "readlink", Path: p, Err: err}
	}
	if err := unix.Symlinkat(string(buf[:n]), dpfd, name); err != nil {
		return &fs.PathError{Op: "symlink", Path: p, Err: err}
	}
	uid, gid := c.owner(st)
	if err := unix.Fchownat(dpfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "chown", Path: p, Err: err}
	}
	ts := []unix.Timespec{st.Atim, st.Mtim}
	if err := unix.UtimesNanoAt(dpfd, name, ts, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "utimes", Path: p, Err: err}
	}
	c.o.Progress.entryDone()
	c.res.Files++
	return nil
}

func (c *copier) copyNode(st *unix.Stat_t, dpfd int, name, p string) error {
	if err := unix.Mknodat(dpfd, name, st.Mode&unix.S_IFMT|0o600, int(st.Rdev)); err != nil {
		return &fs.PathError{Op: "mknod", Path: p, Err: err}
	}
	fd, err := unix.Openat(dpfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(fd)
	if err := c.applyMeta(fd, st, p); err != nil {
		return err
	}
	c.o.Progress.entryDone()
	c.res.Files++
	return nil
}

// applyMeta sets owner, mode and timestamps on the new file open as fd.
func (c *copier) applyMeta(fd int, st *unix.Stat_t, p string) error {
	uid, gid := c.owner(st)
	if err := unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH); err != nil {
		return &fs.PathError{Op: "chown", Path: p, Err: err}
	}
	mode := st.Mode & 0o7777
	if st.Mode&unix.S_IFMT == unix.S_IFREG && (uint32(uid) != st.Uid || uint32(gid) != st.Gid) {
		// Don't mint setuid/setgid executables for a different owner.
		mode &^= unix.S_ISUID | unix.S_ISGID
	}
	// chmod after chown, which clears setuid/setgid.
	if err := unix.Fchmodat(unix.AT_FDCWD, procPath(fd), mode, 0); err != nil {
		return &fs.PathError{Op: "chmod", Path: p, Err: err}
	}
	ts := []unix.Timespec{st.Atim, st.Mtim}
	if err := unix.UtimesNanoAt(unix.AT_FDCWD, procPath(fd), ts, 0); err != nil {
		return &fs.PathError{Op: "utimes", Path: p, Err: err}
	}
	return nil
}

// reflinkUnsupported reports whether a FICLONE error means "can't reflink
// here" (different filesystems, unsupported filesystem) rather than an I/O
// or permission failure.
func reflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTTY) ||
		errors.Is(err, unix.ENOSYS)
}
