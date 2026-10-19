//go:build linux

package fsops

import (
	"fmt"
	"io/fs"
	"path"

	"golang.org/x/sys/unix"
)

// Move renames src to dst (the full destination path). Within one
// filesystem this is an atomic rename; across filesystems the tree is
// copied (reflinking where possible) and the source removed. Unless the
// owner policy is OwnerKeep, the moved tree is re-owned recursively.
func (f *FS) Move(src, dst string, o MoveOptions) (res MoveResult, err error) {
	if o.Ownership, err = o.Ownership.validate(OwnerKeep); err != nil {
		return res, err
	}
	src, dst = CleanPath(src), CleanPath(dst)
	if isWithin(dst, src) {
		return res, fmt.Errorf("%w: cannot move %s into itself", ErrInvalidArgument, src)
	}
	spfd, sname, err := f.openParent(src)
	if err != nil {
		return res, err
	}
	defer unix.Close(spfd)
	dpfd, dname, err := f.openParent(dst)
	if err != nil {
		return res, err
	}
	defer unix.Close(dpfd)
	uid, gid, err := ownerFor(o.Ownership, dpfd)
	if err != nil {
		return res, &fs.PathError{Op: "stat", Path: path.Dir(dst), Err: err}
	}

	flags := uint(unix.RENAME_NOREPLACE)
	if o.Overwrite {
		flags = 0
	}
	err = unix.Renameat2(spfd, sname, dpfd, dname, flags)
	if err == unix.EXDEV {
		return f.moveAcross(src, dst, o, spfd, sname)
	}
	if err != nil {
		return res, &fs.PathError{Op: "move", Path: src, Err: err}
	}

	if uid >= 0 || gid >= 0 {
		fd, err := unix.Openat(dpfd, dname, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return res, &fs.PathError{Op: "open", Path: dst, Err: err}
		}
		defer unix.Close(fd)
		err = walk(fd, dst, true, func(fd int, p string, _ *unix.Stat_t) error {
			if err := unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH); err != nil {
				return &fs.PathError{Op: "chown", Path: p, Err: err}
			}
			return nil
		})
	}
	return res, err
}

func (f *FS) moveAcross(src, dst string, o MoveOptions, spfd int, sname string) (MoveResult, error) {
	cr, err := f.Copy(src, dst, CopyOptions{Ownership: o.Ownership, Reflink: ReflinkAuto, Overwrite: o.Overwrite})
	res := MoveResult{Copied: true, Copy: &cr}
	if err != nil {
		return res, err
	}
	return res, removeAt(spfd, sname, src)
}

// Rename gives p a new name within the same directory.
func (f *FS) Rename(p, name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	p = CleanPath(p)
	_, err := f.Move(p, path.Join(path.Dir(p), name), MoveOptions{})
	return err
}

// Delete recursively removes p. Symlinks are removed, never followed.
func (f *FS) Delete(p string) error {
	p = CleanPath(p)
	dpfd, name, err := f.openParent(p)
	if err != nil {
		return err
	}
	defer unix.Close(dpfd)
	var st unix.Stat_t
	if err := unix.Fstatat(dpfd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return &fs.PathError{Op: "remove", Path: p, Err: err}
	}
	return removeAt(dpfd, name, p)
}

// Mkdir creates the directory p.
func (f *FS) Mkdir(p string, o MkdirOptions) (err error) {
	if o.Ownership, err = o.Ownership.validate(OwnerInherit); err != nil {
		return err
	}
	if o.Mode == 0 {
		o.Mode = 0o755
	}
	if o.Mode&^0o7777 != 0 {
		return fmt.Errorf("%w: mode %o out of range", ErrInvalidArgument, o.Mode)
	}
	p = CleanPath(p)
	dpfd, name, err := f.openParent(p)
	if err != nil {
		return err
	}
	defer unix.Close(dpfd)
	if err := ValidName(name); err != nil {
		return err
	}
	uid, gid, err := ownerFor(o.Ownership, dpfd)
	if err != nil {
		return &fs.PathError{Op: "stat", Path: path.Dir(p), Err: err}
	}
	if err := unix.Mkdirat(dpfd, name, 0o700); err != nil {
		return &fs.PathError{Op: "mkdir", Path: p, Err: err}
	}
	fd, err := unix.Openat(dpfd, name, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return &fs.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(fd)
	if err := unix.Fchownat(fd, "", uid, gid, unix.AT_EMPTY_PATH); err != nil {
		return &fs.PathError{Op: "chown", Path: p, Err: err}
	}
	if err := unix.Fchmodat(unix.AT_FDCWD, procPath(fd), o.Mode, 0); err != nil {
		return &fs.PathError{Op: "chmod", Path: p, Err: err}
	}
	return nil
}
