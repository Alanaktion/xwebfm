//go:build linux

package fsops

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"
)

const (
	xattrACLAccess  = "system.posix_acl_access"
	xattrACLDefault = "system.posix_acl_default"

	aclVersion   = 2
	tagUserObj   = 0x01
	tagUser      = 0x02
	tagGroupObj  = 0x04
	tagGroup     = 0x08
	tagMask      = 0x10
	tagOther     = 0x20
	aclUndefined = 0xFFFFFFFF
)

type rawACLEntry struct {
	tag  uint16
	perm uint16
	id   uint32
}

func decodeACL(b []byte) ([]rawACLEntry, error) {
	if len(b) < 4 || binary.LittleEndian.Uint32(b) != aclVersion || (len(b)-4)%8 != 0 {
		return nil, errors.New("malformed POSIX ACL")
	}
	var out []rawACLEntry
	for i := 4; i < len(b); i += 8 {
		out = append(out, rawACLEntry{
			tag:  binary.LittleEndian.Uint16(b[i:]),
			perm: binary.LittleEndian.Uint16(b[i+2:]),
			id:   binary.LittleEndian.Uint32(b[i+4:]),
		})
	}
	return out, nil
}

// buildACL encodes base entries taken from mode plus the named entries,
// with a mask covering the group class, in the order the kernel requires.
func buildACL(mode uint32, named []ACLEntry) []byte {
	entries := []rawACLEntry{
		{tagUserObj, uint16(mode>>6) & 7, aclUndefined},
		{tagGroupObj, uint16(mode>>3) & 7, aclUndefined},
		{tagOther, uint16(mode) & 7, aclUndefined},
	}
	mask := uint16(mode>>3) & 7
	for _, e := range named {
		tag := uint16(tagUser)
		if e.Kind == "group" {
			tag = tagGroup
		}
		entries = append(entries, rawACLEntry{tag, uint16(e.Perm), e.ID})
		mask |= uint16(e.Perm)
	}
	if len(named) > 0 {
		entries = append(entries, rawACLEntry{tagMask, mask, aclUndefined})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].tag != entries[j].tag {
			return entries[i].tag < entries[j].tag
		}
		return entries[i].id < entries[j].id
	})
	b := make([]byte, 4+8*len(entries))
	binary.LittleEndian.PutUint32(b, aclVersion)
	for i, e := range entries {
		o := 4 + 8*i
		binary.LittleEndian.PutUint16(b[o:], e.tag)
		binary.LittleEndian.PutUint16(b[o+2:], e.perm)
		binary.LittleEndian.PutUint32(b[o+4:], e.id)
	}
	return b
}

// readACLXattr returns the named entries and mask of one ACL xattr; ok is
// false if the attribute doesn't exist.
func (f *FS) readACLXattr(p, name string) (named []ACLEntry, mask *uint8, ok bool, err error) {
	sz, err := unix.Getxattr(p, name, nil)
	if err == unix.ENODATA || err == unix.EOPNOTSUPP {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, err
	}
	buf := make([]byte, sz)
	if sz, err = unix.Getxattr(p, name, buf); err != nil {
		return nil, nil, false, err
	}
	raw, err := decodeACL(buf[:sz])
	if err != nil {
		return nil, nil, false, err
	}
	named = []ACLEntry{}
	for _, e := range raw {
		switch e.tag {
		case tagUser:
			named = append(named, ACLEntry{Kind: "user", ID: e.id, Name: f.Owners.UserName(e.id), Perm: uint8(e.perm)})
		case tagGroup:
			named = append(named, ACLEntry{Kind: "group", ID: e.id, Name: f.Owners.GroupName(e.id), Perm: uint8(e.perm)})
		case tagMask:
			m := uint8(e.perm)
			mask = &m
		}
	}
	return named, mask, true, nil
}

// GetACL reads the POSIX ACL of p. Symlinks have no ACLs.
func (f *FS) GetACL(p string) (ACL, error) {
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return ACL{}, err
	}
	defer unix.Close(fd)
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return ACL{}, &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	if st.Mode&unix.S_IFMT == unix.S_IFLNK {
		return ACL{}, &fs.PathError{Op: "getacl", Path: p, Err: ErrIsSymlink}
	}
	acl := ACL{Named: []ACLEntry{}, Default: []ACLEntry{}}
	named, mask, _, err := f.readACLXattr(procPath(fd), xattrACLAccess)
	if err != nil {
		return acl, &fs.PathError{Op: "getacl", Path: p, Err: err}
	}
	if named != nil {
		acl.Named, acl.Mask = named, mask
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		def, _, ok, err := f.readACLXattr(procPath(fd), xattrACLDefault)
		if err != nil {
			return acl, &fs.PathError{Op: "getacl", Path: p, Err: err}
		}
		acl.HasDefault = ok
		if def != nil {
			acl.Default = def
		}
	}
	return acl, nil
}

// SetACL writes the named entries (and, for directories, the default ACL)
// to p and, with Recursive, everything beneath it.
func (f *FS) SetACL(ctx context.Context, p string, o SetACLOptions) error {
	if err := o.ACL.validate(); err != nil {
		return err
	}
	p = CleanPath(p)
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	top := true
	return walk(ctx, fd, p, o.Recursive, func(fd int, fp string, st *unix.Stat_t) error {
		o.Progress.setCurrent(fp)
		defer o.Progress.entryDone()
		kind := st.Mode & unix.S_IFMT
		if kind == unix.S_IFLNK {
			if top {
				return &fs.PathError{Op: "setacl", Path: fp, Err: ErrIsSymlink}
			}
			return nil
		}
		top = false
		named := o.Named
		if kind != unix.S_IFDIR && st.Mode&0o111 == 0 {
			// Capital-X semantics: don't make non-executables executable.
			named = make([]ACLEntry, len(o.Named))
			for i, e := range o.Named {
				e.Perm &^= 1
				named[i] = e
			}
		}
		pp := procPath(fd)
		if err := unix.Setxattr(pp, xattrACLAccess, buildACL(st.Mode, named), 0); err != nil {
			return &fs.PathError{Op: "setacl", Path: fp, Err: err}
		}
		if kind == unix.S_IFDIR {
			if o.HasDefault {
				err = unix.Setxattr(pp, xattrACLDefault, buildACL(st.Mode, o.Default), 0)
			} else if err = unix.Removexattr(pp, xattrACLDefault); err == unix.ENODATA {
				err = nil
			}
			if err != nil {
				return &fs.PathError{Op: "setacl", Path: fp, Err: err}
			}
		}
		return nil
	})
}

// openForFlags opens p for the flags ioctls. Only regular files and
// directories are opened, so device nodes and FIFOs are never touched.
func (f *FS) openForFlags(p string) (int, error) {
	pfd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return -1, err
	}
	defer unix.Close(pfd)
	var st unix.Stat_t
	if err := unix.Fstat(pfd, &st); err != nil {
		return -1, &fs.PathError{Op: "stat", Path: p, Err: err}
	}
	if k := st.Mode & unix.S_IFMT; k != unix.S_IFREG && k != unix.S_IFDIR {
		return -1, &fs.PathError{Op: "flags", Path: p, Err: fmt.Errorf("%w: attributes only apply to files and folders", ErrInvalidArgument)}
	}
	fd, err := unix.Open(procPath(pfd), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, &fs.PathError{Op: "open", Path: p, Err: err}
	}
	return fd, nil
}

// Inode flags from linux/fs.h (not exported by x/sys/unix).
const (
	fsImmutableFl = 0x00000010
	fsAppendFl    = 0x00000020
	fsNodumpFl    = 0x00000040
	fsNoatimeFl   = 0x00000080
	fsNocowFl     = 0x00800000
)

var flagBits = []struct {
	bit uint32
	get func(*Flags) *bool
}{
	{fsImmutableFl, func(f *Flags) *bool { return &f.Immutable }},
	{fsAppendFl, func(f *Flags) *bool { return &f.AppendOnly }},
	{fsNodumpFl, func(f *Flags) *bool { return &f.NoDump }},
	{fsNoatimeFl, func(f *Flags) *bool { return &f.NoAtime }},
	{fsNocowFl, func(f *Flags) *bool { return &f.NoCOW }},
}

// GetFlags reads the inode attributes of a file or directory.
func (f *FS) GetFlags(p string) (Flags, error) {
	p = CleanPath(p)
	fd, err := f.openForFlags(p)
	if err != nil {
		return Flags{}, err
	}
	defer unix.Close(fd)
	v, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return Flags{}, &fs.PathError{Op: "getflags", Path: p, Err: err}
	}
	var fl Flags
	for _, b := range flagBits {
		*b.get(&fl) = v&b.bit != 0
	}
	return fl, nil
}

// SetFlags sets the listed attributes, leaving other inode flags as they are.
func (f *FS) SetFlags(p string, fl Flags) error {
	p = CleanPath(p)
	fd, err := f.openForFlags(p)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	v, err := unix.IoctlGetUint32(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return &fs.PathError{Op: "getflags", Path: p, Err: err}
	}
	for _, b := range flagBits {
		if *b.get(&fl) {
			v |= b.bit
		} else {
			v &^= b.bit
		}
	}
	if err := unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, int(v)); err != nil {
		return &fs.PathError{Op: "setflags", Path: p, Err: err}
	}
	return nil
}

// Link creates a symlink or hard link at linkPath pointing to target.
func (f *FS) Link(target, linkPath string, o LinkOptions) (err error) {
	if o.Ownership, err = o.Ownership.validate(OwnerInherit); err != nil {
		return err
	}
	target, linkPath = CleanPath(target), CleanPath(linkPath)
	dpfd, name, err := f.openParent(linkPath)
	if err != nil {
		return err
	}
	defer unix.Close(dpfd)
	if err := ValidName(name); err != nil {
		return err
	}
	switch o.Kind {
	case Symlink:
		// Absolute targets are written as host paths so the link works
		// outside xwebfm too when -root isn't "/".
		text := path.Join(f.root, target)
		if o.Relative {
			if text, err = filepath.Rel(path.Dir(linkPath), target); err != nil {
				return err
			}
		}
		if err := unix.Symlinkat(text, dpfd, name); err != nil {
			return &fs.PathError{Op: "symlink", Path: linkPath, Err: err}
		}
		uid, gid, err := ownerFor(o.Ownership, dpfd)
		if err == nil {
			err = unix.Fchownat(dpfd, name, uid, gid, unix.AT_SYMLINK_NOFOLLOW)
		}
		if err != nil {
			return &fs.PathError{Op: "chown", Path: linkPath, Err: err}
		}
		return nil
	case Hardlink:
		tfd, err := f.open(target, unix.O_PATH|unix.O_NOFOLLOW)
		if err != nil {
			return err
		}
		defer unix.Close(tfd)
		var st unix.Stat_t
		if err := unix.Fstat(tfd, &st); err != nil {
			return &fs.PathError{Op: "stat", Path: target, Err: err}
		}
		if st.Mode&unix.S_IFMT == unix.S_IFDIR {
			return &fs.PathError{Op: "link", Path: target, Err: fmt.Errorf("%w: can't hard link a folder", ErrInvalidArgument)}
		}
		if err := unix.Linkat(tfd, "", dpfd, name, unix.AT_EMPTY_PATH); err != nil {
			return &fs.PathError{Op: "link", Path: linkPath, Err: err}
		}
		return nil
	}
	return fmt.Errorf("%w: unknown link kind %q", ErrInvalidArgument, o.Kind)
}
