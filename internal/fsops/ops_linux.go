//go:build linux

package fsops

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"

	"golang.org/x/sys/unix"
)

// Chown changes the owner and/or group of p. Symlinks are never followed:
// a symlink's own ownership is changed instead (like lchown / chown -h).
func (f *FS) Chown(p string, o ChownOptions) error {
	if o.UID < -1 || o.GID < -1 || (o.UID == -1 && o.GID == -1) {
		return fmt.Errorf("%w: uid and/or gid required", ErrInvalidArgument)
	}
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	return walk(fd, p, o.Recursive, func(fd int, p string, _ *unix.Stat_t) error {
		err := unix.Fchownat(fd, "", o.UID, o.GID, unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW)
		if err != nil {
			return &fs.PathError{Op: "chown", Path: p, Err: err}
		}
		return nil
	})
}

// Chmod sets the permission bits of p. A symlink target is rejected, and
// symlinks found during a recursive walk are skipped.
func (f *FS) Chmod(p string, o ChmodOptions) error {
	if o.Mode&^0o7777 != 0 {
		return fmt.Errorf("%w: mode %o out of range", ErrInvalidArgument, o.Mode)
	}
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	top := true
	return walk(fd, p, o.Recursive, func(fd int, p string, st *unix.Stat_t) error {
		if st.Mode&unix.S_IFMT == unix.S_IFLNK {
			if top {
				return &fs.PathError{Op: "chmod", Path: p, Err: ErrIsSymlink}
			}
			return nil
		}
		top = false
		if err := unix.Fchmodat(unix.AT_FDCWD, procPath(fd), o.Mode, 0); err != nil {
			return &fs.PathError{Op: "chmod", Path: p, Err: err}
		}
		return nil
	})
}

// walk calls fn for fd (an O_PATH|O_NOFOLLOW handle on p) and, if recursive
// and fd is a directory, for every descendant. Children are opened relative
// to their parent's fd with O_NOFOLLOW so the walk can't be redirected by a
// symlink swapped in concurrently.
func walk(fd int, p string, recursive bool, fn func(fd int, p string, st *unix.Stat_t) error) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	if err := fn(fd, p, &st); err != nil {
		return err
	}
	if !recursive || st.Mode&unix.S_IFMT != unix.S_IFDIR {
		return nil
	}

	dfd, err := unix.Openat(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	dir := os.NewFile(uintptr(dfd), p)
	defer dir.Close()
	names, err := dir.Readdirnames(-1)
	if err != nil {
		return &fs.PathError{Op: "readdir", Path: p, Err: err}
	}
	for _, name := range names {
		cfd, err := unix.Openat(dfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err == unix.ENOENT {
			continue
		}
		if err != nil {
			return &fs.PathError{Op: "open", Path: path.Join(p, name), Err: err}
		}
		err = walk(cfd, path.Join(p, name), true, fn)
		unix.Close(cfd)
		if err != nil {
			return err
		}
	}
	return nil
}

// Copy copies the regular file src to dst, using a reflink (FICLONE) when
// requested and supported. Without a reflink, data is copied with
// copy_file_range, which may still share extents on some filesystems.
func (f *FS) Copy(src, dst string, o CopyOptions) (res CopyResult, err error) {
	switch o.Reflink {
	case "":
		o.Reflink = ReflinkAuto
	case ReflinkAuto, ReflinkAlways, ReflinkNever:
	default:
		return res, fmt.Errorf("%w: unknown reflink mode %q", ErrInvalidArgument, o.Reflink)
	}
	src, dst = CleanPath(src), CleanPath(dst)
	if dst == "/" {
		return res, fmt.Errorf("%w: invalid destination", ErrInvalidArgument)
	}

	// Check the type through an O_PATH handle first so FIFOs and devices are
	// never opened for reading, then reopen the same inode for reading.
	spfd, err := f.open(src, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return res, err
	}
	defer unix.Close(spfd)
	var st unix.Stat_t
	if err := unix.Fstat(spfd, &st); err != nil {
		return res, &fs.PathError{Op: "stat", Path: src, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return res, &fs.PathError{Op: "copy", Path: src, Err: ErrNotRegular}
	}
	sfd, err := unix.Open(procPath(spfd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return res, &fs.PathError{Op: "open", Path: src, Err: err}
	}
	srcFile := os.NewFile(uintptr(sfd), src)
	defer srcFile.Close()

	pfd, err := f.open(path.Dir(dst), unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return res, err
	}
	defer unix.Close(pfd)
	name := path.Base(dst)

	flags := unix.O_WRONLY | unix.O_CREAT | unix.O_NOFOLLOW | unix.O_CLOEXEC
	if o.Overwrite {
		var dst2 unix.Stat_t
		if unix.Fstatat(pfd, name, &dst2, unix.AT_SYMLINK_NOFOLLOW) == nil &&
			dst2.Dev == st.Dev && dst2.Ino == st.Ino {
			return res, fmt.Errorf("%w: source and destination are the same file", ErrInvalidArgument)
		}
		flags |= unix.O_TRUNC
	} else {
		flags |= unix.O_EXCL
	}
	dfd, err := unix.Openat(pfd, name, flags, st.Mode&0o777)
	if err != nil {
		return res, &fs.PathError{Op: "create", Path: dst, Err: err}
	}
	dstFile := os.NewFile(uintptr(dfd), dst)
	defer func() {
		if cerr := dstFile.Close(); cerr != nil && err == nil {
			err = &fs.PathError{Op: "close", Path: dst, Err: cerr}
		}
		if err != nil && !o.Overwrite {
			unix.Unlinkat(pfd, name, 0)
		}
	}()

	if o.Reflink != ReflinkNever {
		cerr := unix.IoctlFileClone(dfd, sfd)
		switch {
		case cerr == nil:
			res.Reflinked, res.Bytes = true, st.Size
		case !reflinkUnsupported(cerr):
			return res, &fs.PathError{Op: "reflink", Path: dst, Err: cerr}
		case o.Reflink == ReflinkAlways:
			return res, &fs.PathError{Op: "reflink", Path: dst, Err: fmt.Errorf("%w (%v)", ErrReflinkUnsupported, cerr)}
		}
	}
	if !res.Reflinked {
		if res.Bytes, err = io.Copy(dstFile, srcFile); err != nil {
			return res, &fs.PathError{Op: "copy", Path: dst, Err: err}
		}
	}

	if o.Preserve {
		if err := unix.Fchown(dfd, int(st.Uid), int(st.Gid)); err != nil {
			return res, &fs.PathError{Op: "chown", Path: dst, Err: err}
		}
		// chmod after chown, which clears setuid/setgid.
		if err := unix.Fchmod(dfd, st.Mode&0o7777); err != nil {
			return res, &fs.PathError{Op: "chmod", Path: dst, Err: err}
		}
		ts := []unix.Timespec{st.Atim, st.Mtim}
		if err := unix.UtimesNanoAt(unix.AT_FDCWD, procPath(dfd), ts, 0); err != nil {
			return res, &fs.PathError{Op: "utimes", Path: dst, Err: err}
		}
	}
	return res, nil
}

// reflinkUnsupported reports whether a FICLONE error means "can't reflink
// here" (different filesystems, unsupported filesystem) rather than an I/O
// or permission failure.
func reflinkUnsupported(err error) bool {
	return errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EXDEV) ||
		errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTTY) ||
		errors.Is(err, unix.ENOSYS)
}
