//go:build linux

package fsops

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"golang.org/x/sys/unix"
)

// FS performs operations beneath a fixed root directory.
type FS struct {
	root   string
	rootFD int
	Owners *Owners
}

// New opens root and returns an FS confined to it. owners may be nil, in
// which case the system passwd/group files are used.
func New(root string, owners *Owners) (*FS, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fd, err := unix.Open(abs, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: abs, Err: err}
	}
	if owners == nil {
		owners = NewOwners()
	}
	f := &FS{root: abs, rootFD: fd, Owners: owners}

	probe, err := f.open("/", unix.O_PATH)
	if err != nil {
		unix.Close(fd)
		if errors.Is(err, unix.ENOSYS) {
			return nil, errors.New("openat2 is not available; Linux 5.6 or newer is required")
		}
		return nil, err
	}
	unix.Close(probe)
	return f, nil
}

// Root returns the absolute host path of the FS root.
func (f *FS) Root() string { return f.root }

// Close releases the root directory handle.
func (f *FS) Close() error { return unix.Close(f.rootFD) }

// open resolves p beneath the root. Symlinks in intermediate components are
// followed but can't escape the root; pass O_NOFOLLOW (usually with O_PATH)
// to get a handle on a final-component symlink itself.
func (f *FS) open(p string, flags int) (int, error) {
	how := unix.OpenHow{
		Flags:   uint64(flags | unix.O_CLOEXEC),
		Resolve: unix.RESOLVE_IN_ROOT | unix.RESOLVE_NO_MAGICLINKS,
	}
	var (
		fd  int
		err error
	)
	// openat2 returns EAGAIN if a concurrent rename may have affected
	// resolution; retry a few times before giving up.
	for range 8 {
		fd, err = unix.Openat2(f.rootFD, relPath(p), &how)
		if err != unix.EAGAIN && err != unix.EINTR {
			break
		}
	}
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: CleanPath(p), Err: err}
	}
	return fd, nil
}

// List returns the entries of the directory at p, directories first.
func (f *FS) List(p string) ([]Entry, error) {
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(fd), p)
	defer dir.Close()

	names, err := dir.Readdirnames(-1)
	if err != nil {
		return nil, &fs.PathError{Op: "readdir", Path: p, Err: err}
	}
	entries := make([]Entry, 0, len(names))
	for _, name := range names {
		e, err := f.entryAt(fd, name, path.Join(p, name))
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed while listing
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool {
		di, dj := entries[i].Type == TypeDir, entries[j].Type == TypeDir
		if di != dj {
			return di
		}
		return entries[i].Name < entries[j].Name
	})
	return entries, nil
}

// Stat returns information about p without following a final symlink.
func (f *FS) Stat(p string) (Entry, error) {
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return Entry{}, err
	}
	defer unix.Close(fd)
	e, err := f.entryAt(fd, "", p)
	if p == "/" {
		e.Name = "/"
	}
	return e, err
}

// entryAt builds an Entry for name relative to dirfd, or for dirfd itself if
// name is empty.
func (f *FS) entryAt(dirfd int, name, p string) (Entry, error) {
	var st unix.Stat_t
	if err := unix.Fstatat(dirfd, name, &st, unix.AT_SYMLINK_NOFOLLOW|unix.AT_EMPTY_PATH); err != nil {
		return Entry{}, &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	e := Entry{
		Name:    path.Base(p),
		Path:    p,
		Type:    fileType(st.Mode),
		Size:    st.Size,
		Mode:    st.Mode & 0o7777,
		UID:     st.Uid,
		GID:     st.Gid,
		User:    f.Owners.UserName(st.Uid),
		Group:   f.Owners.GroupName(st.Gid),
		Nlink:   uint64(st.Nlink),
		Inode:   st.Ino,
		ModTime: time.Unix(st.Mtim.Unix()),
		Disk:    st.Blocks * 512,
	}
	if e.Type == TypeSymlink {
		buf := make([]byte, unix.PathMax)
		if n, err := unix.Readlinkat(dirfd, name, buf); err == nil {
			e.LinkTarget = string(buf[:n])
		}
	} else {
		e.ACL = hasExtendedACL(dirfd, name)
	}
	return e, nil
}

// aclBaseSize is the xattr size of an ACL holding only the three base
// entries (owner, group, other), which the mode bits already show.
const aclBaseSize = 4 + 3*8

// hasExtendedACL reports whether the entry has named ACL entries or a
// default ACL. With an empty name dirfd is the entry itself (an O_PATH fd),
// read through its /proc magic link; otherwise the name is looked up
// relative to dirfd without following a final symlink.
func hasExtendedACL(dirfd int, name string) bool {
	get := unix.Lgetxattr
	p := procPath(dirfd) + "/" + name
	if name == "" {
		get, p = unix.Getxattr, procPath(dirfd)
	}
	if n, err := get(p, "system.posix_acl_access", nil); err == nil && n > aclBaseSize {
		return true
	}
	n, err := get(p, "system.posix_acl_default", nil)
	return err == nil && n > 0
}

var fsTypes = map[int64]string{
	0xEF53: "ext4", 0x9123683E: "btrfs", 0x58465342: "xfs", 0xCA451A4E: "bcachefs",
	0x2FC12FC1: "zfs", 0x01021994: "tmpfs", 0x6969: "nfs", 0xFF534D42: "cifs",
	0x794C7630: "overlay", 0xF2F52010: "f2fs", 0x4D44: "vfat", 0x2011BAB0: "exfat",
	0x65735546: "fuse", 0x5346544E: "ntfs", 0x3153464A: "jfs", 0x52654973: "reiserfs",
}

// StatFS reports the filesystem holding p.
func (f *FS) StatFS(p string) (FSInfo, error) {
	fd, err := f.open(p, unix.O_PATH)
	if err != nil {
		return FSInfo{}, err
	}
	defer unix.Close(fd)
	var st unix.Statfs_t
	if err := unix.Fstatfs(fd, &st); err != nil {
		return FSInfo{}, &fs.PathError{Op: "statfs", Path: CleanPath(p), Err: err}
	}
	bs := uint64(st.Bsize)
	typ, ok := fsTypes[int64(st.Type)]
	if !ok {
		typ = fmt.Sprintf("0x%x", st.Type)
	}
	return FSInfo{
		Type:    typ,
		Total:   st.Blocks * bs,
		Free:    st.Bfree * bs,
		Avail:   st.Bavail * bs,
		Reflink: typ == "btrfs" || typ == "xfs" || typ == "bcachefs",
	}, nil
}

func fileType(mode uint32) FileType {
	switch mode & unix.S_IFMT {
	case unix.S_IFREG:
		return TypeFile
	case unix.S_IFDIR:
		return TypeDir
	case unix.S_IFLNK:
		return TypeSymlink
	case unix.S_IFIFO:
		return TypeFIFO
	case unix.S_IFSOCK:
		return TypeSocket
	case unix.S_IFBLK:
		return TypeBlock
	case unix.S_IFCHR:
		return TypeChar
	}
	return TypeUnknown
}

// procPath returns a /proc magic-link path for fd, used to apply syscalls
// that have no fd-based variant usable with O_PATH descriptors.
func procPath(fd int) string {
	return fmt.Sprintf("/proc/self/fd/%d", fd)
}
