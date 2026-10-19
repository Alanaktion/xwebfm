//go:build linux

package fsops

import (
	"fmt"
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

// ownerFor returns the uid and gid to give files placed in the directory
// dpfd under policy o; -1 means "keep the source's" (or "unchanged").
func ownerFor(o Ownership, dpfd int) (uid, gid int, err error) {
	switch o.Owner {
	case OwnerInherit:
		var st unix.Stat_t
		if err := unix.Fstat(dpfd, &st); err != nil {
			return -1, -1, err
		}
		return int(st.Uid), int(st.Gid), nil
	case OwnerSet:
		return o.UID, o.GID, nil
	}
	return -1, -1, nil
}

// removeAt recursively removes name within dpfd without following symlinks.
func removeAt(dpfd int, name, p string) error {
	err := unix.Unlinkat(dpfd, name, 0)
	if err == nil || err == unix.ENOENT {
		return nil
	}
	if err != unix.EISDIR {
		return &fs.PathError{Op: "remove", Path: p, Err: err}
	}
	dfd, err := unix.Openat(dpfd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	dir := os.NewFile(uintptr(dfd), p)
	names, err := dir.Readdirnames(-1)
	if err != nil {
		dir.Close()
		return &fs.PathError{Op: "readdir", Path: p, Err: err}
	}
	for _, n := range names {
		if err := removeAt(dfd, n, path.Join(p, n)); err != nil {
			dir.Close()
			return err
		}
	}
	dir.Close()
	if err := unix.Unlinkat(dpfd, name, unix.AT_REMOVEDIR); err != nil && err != unix.ENOENT {
		return &fs.PathError{Op: "remove", Path: p, Err: err}
	}
	return nil
}

// openParent opens the directory containing p and returns it with p's base
// name. p must not be the root.
func (f *FS) openParent(p string) (int, string, error) {
	if p == "/" {
		return -1, "", fmt.Errorf("%w: operation not allowed on the root", ErrInvalidArgument)
	}
	fd, err := f.open(path.Dir(p), unix.O_PATH|unix.O_DIRECTORY)
	return fd, path.Base(p), err
}
