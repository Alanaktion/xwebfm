//go:build linux

package fsops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// OpenFile opens the regular file p for reading. The final component is
// not followed if it is a symlink.
func (f *FS) OpenFile(p string) (*os.File, Entry, error) {
	p = CleanPath(p)
	pfd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return nil, Entry{}, err
	}
	defer unix.Close(pfd)
	e, err := f.entryAt(pfd, "", p)
	if err != nil {
		return nil, Entry{}, err
	}
	if e.Type != TypeFile {
		return nil, Entry{}, &fs.PathError{Op: "open", Path: p, Err: ErrNotRegular}
	}
	fd, err := unix.Open(procPath(pfd), unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, Entry{}, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	return os.NewFile(uintptr(fd), p), e, nil
}

// WriteFile creates name in directory dir from r. Data is written to a
// temporary file first and renamed into place, so a failed upload never
// leaves a truncated file and an overwrite is atomic.
func (f *FS) WriteFile(dir, name string, r io.Reader, o WriteOptions) (e Entry, err error) {
	if err := ValidName(name); err != nil {
		return e, err
	}
	if o.Ownership, err = o.Ownership.validate(OwnerInherit); err != nil {
		return e, err
	}
	if o.Mode == 0 {
		o.Mode = 0o644
	}
	if o.Mode&^0o7777 != 0 {
		return e, fmt.Errorf("%w: mode %o out of range", ErrInvalidArgument, o.Mode)
	}
	dir = CleanPath(dir)
	p := path.Join(dir, name)
	dpfd, err := f.open(dir, unix.O_PATH|unix.O_DIRECTORY)
	if err != nil {
		return e, err
	}
	defer unix.Close(dpfd)
	uid, gid, err := ownerFor(o.Ownership, dpfd)
	if err != nil {
		return e, &fs.PathError{Op: "stat", Path: dir, Err: err}
	}
	if !o.Overwrite {
		// Fail early rather than after receiving the whole upload.
		var st unix.Stat_t
		if unix.Fstatat(dpfd, name, &st, unix.AT_SYMLINK_NOFOLLOW) == nil {
			return e, &fs.PathError{Op: "create", Path: p, Err: fs.ErrExist}
		}
	}

	rnd := make([]byte, 6)
	rand.Read(rnd)
	tmp := ".xwebfm-upload-" + hex.EncodeToString(rnd)
	fd, err := unix.Openat(dpfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return e, &fs.PathError{Op: "create", Path: p, Err: err}
	}
	file := os.NewFile(uintptr(fd), p)
	defer func() {
		if file != nil {
			file.Close()
		}
		if err != nil {
			unix.Unlinkat(dpfd, tmp, 0)
		}
	}()
	if _, err := io.Copy(file, r); err != nil {
		return e, &fs.PathError{Op: "write", Path: p, Err: err}
	}
	if err := unix.Fchown(fd, uid, gid); err != nil {
		return e, &fs.PathError{Op: "chown", Path: p, Err: err}
	}
	if err := unix.Fchmod(fd, o.Mode); err != nil {
		return e, &fs.PathError{Op: "chmod", Path: p, Err: err}
	}
	cerr := file.Close()
	file = nil
	if cerr != nil {
		return e, &fs.PathError{Op: "close", Path: p, Err: cerr}
	}
	flags := uint(unix.RENAME_NOREPLACE)
	if o.Overwrite {
		flags = 0
	}
	if err := unix.Renameat2(dpfd, tmp, dpfd, name, flags); err != nil {
		return e, &fs.PathError{Op: "rename", Path: p, Err: err}
	}
	return f.Stat(p)
}

// WriteTar writes p (a file or directory tree) to w as a gzipped tar
// archive. Symlinks are stored as links; FIFOs, devices and sockets are
// skipped. The archive is abandoned if ctx is cancelled.
func (f *FS) WriteTar(ctx context.Context, p string, w io.Writer) error {
	aw, err := f.newArchiveWriter(ctx, FormatTarGz, w, nil)
	if err != nil {
		return err
	}
	if err := f.addTree(ctx, aw, CleanPath(p)); err != nil {
		return err
	}
	return aw.Close()
}

// ReplaceFile atomically replaces the contents of the existing regular file
// p, as editors do: the data is written to a temporary file in the same
// directory, which gets the original's owner, mode and extended
// attributes (including ACLs), and is then renamed over it. The file
// therefore gets a new inode, so other hard links keep the old contents.
// If expectSize/expectMtime don't match the current file (it was changed
// by someone else after being read) ErrConflict is returned.
func (f *FS) ReplaceFile(p string, r io.Reader, expectSize int64, expectMtime time.Time) (e Entry, err error) {
	p = CleanPath(p)
	dpfd, name, err := f.openParent(p)
	if err != nil {
		return e, err
	}
	defer unix.Close(dpfd)
	ofd, err := unix.Openat(dpfd, name, unix.O_PATH|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return e, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	defer unix.Close(ofd)
	var st unix.Stat_t
	if err := unix.Fstat(ofd, &st); err != nil {
		return e, &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		return e, &fs.PathError{Op: "write", Path: p, Err: ErrNotRegular}
	}
	if st.Size != expectSize || st.Mtim.Sec != expectMtime.Unix() {
		return e, &fs.PathError{Op: "write", Path: p, Err: ErrConflict}
	}

	tmp := ".xwebfm-edit-" + randomHex()
	fd, err := unix.Openat(dpfd, tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return e, &fs.PathError{Op: "create", Path: p, Err: err}
	}
	file := os.NewFile(uintptr(fd), p)
	defer func() {
		if file != nil {
			file.Close()
		}
		if err != nil {
			unix.Unlinkat(dpfd, tmp, 0)
		}
	}()
	n, err := io.Copy(file, io.LimitReader(r, MaxEditSize+1))
	if err != nil {
		return e, &fs.PathError{Op: "write", Path: p, Err: err}
	}
	if n > MaxEditSize {
		return e, fmt.Errorf("%w: file too large to edit", ErrInvalidArgument)
	}
	copyXattrs(procPath(ofd), fd)
	if err := unix.Fchown(fd, int(st.Uid), int(st.Gid)); err != nil {
		return e, &fs.PathError{Op: "chown", Path: p, Err: err}
	}
	if err := unix.Fchmod(fd, st.Mode&0o7777); err != nil {
		return e, &fs.PathError{Op: "chmod", Path: p, Err: err}
	}
	if err := unix.Fsync(fd); err != nil {
		return e, &fs.PathError{Op: "sync", Path: p, Err: err}
	}
	cerr := file.Close()
	file = nil
	if cerr != nil {
		return e, &fs.PathError{Op: "close", Path: p, Err: cerr}
	}
	if err := unix.Renameat(dpfd, tmp, dpfd, name); err != nil {
		return e, &fs.PathError{Op: "rename", Path: p, Err: err}
	}
	return f.Stat(p)
}

// copyXattrs copies extended attributes (ACLs, user.*, security labels)
// from the file at src to fd, ignoring ones that can't be set.
func copyXattrs(src string, fd int) {
	sz, err := unix.Listxattr(src, nil)
	if err != nil || sz == 0 {
		return
	}
	buf := make([]byte, sz)
	if sz, err = unix.Listxattr(src, buf); err != nil {
		return
	}
	for _, name := range strings.Split(strings.TrimRight(string(buf[:sz]), "\x00"), "\x00") {
		vsz, err := unix.Getxattr(src, name, nil)
		if err != nil {
			continue
		}
		val := make([]byte, vsz)
		if vsz, err = unix.Getxattr(src, name, val); err == nil {
			unix.Fsetxattr(fd, name, val[:vsz], 0)
		}
	}
}
