// Package fsops implements the privileged filesystem operations behind
// xwebfm. It has no knowledge of HTTP so it can be reused by other front ends.
//
// Every path accepted by this package is interpreted relative to the FS root
// and resolved with openat2(RESOLVE_IN_ROOT), so neither ".." nor symlinks can
// reach outside it. Operations act on the final path component itself and
// never follow it if it is a symlink; recursive operations walk the tree by
// file descriptor so a user swapping a directory for a symlink mid-walk can't
// redirect a privileged chown/chmod elsewhere.
package fsops

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

var (
	// ErrReflinkUnsupported is returned when a reflink copy was required but
	// the filesystem (or the pair of files) does not support it.
	ErrReflinkUnsupported = errors.New("reflink not supported for this source and destination")
	// ErrNotRegular is returned when an operation requires a regular file.
	ErrNotRegular = errors.New("not a regular file")
	// ErrIsSymlink is returned when an operation can't be applied to a symlink.
	ErrIsSymlink = errors.New("operation not supported on symlinks")
	// ErrInvalidArgument is returned for malformed options.
	ErrInvalidArgument = errors.New("invalid argument")
)

// FileType is a coarse classification of a directory entry.
type FileType string

const (
	TypeFile    FileType = "file"
	TypeDir     FileType = "dir"
	TypeSymlink FileType = "symlink"
	TypeFIFO    FileType = "fifo"
	TypeSocket  FileType = "socket"
	TypeBlock   FileType = "block"
	TypeChar    FileType = "char"
	TypeUnknown FileType = "unknown"
)

// Entry describes a single file as seen by lstat.
type Entry struct {
	Name       string    `json:"name"`
	Path       string    `json:"path"`
	Type       FileType  `json:"type"`
	Size       int64     `json:"size"`
	Mode       uint32    `json:"mode"` // permission bits incl. setuid/setgid/sticky (07777)
	UID        uint32    `json:"uid"`
	GID        uint32    `json:"gid"`
	User       string    `json:"user,omitempty"`
	Group      string    `json:"group,omitempty"`
	Nlink      uint64    `json:"nlink"`
	Inode      uint64    `json:"inode"`
	ModTime    time.Time `json:"mtime"`
	LinkTarget string    `json:"linkTarget,omitempty"`
	// Disk is the space allocated on disk (st_blocks), which differs from
	// Size for sparse, compressed or reflinked files.
	Disk int64 `json:"disk"`
	// ACL is set when the entry has an extended POSIX ACL (ls shows "+").
	ACL bool `json:"acl,omitempty"`
}

// FSInfo describes the filesystem holding a path.
type FSInfo struct {
	Type  string `json:"type"`
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
	Avail uint64 `json:"avail"` // available to unprivileged users
	// Reflink is set for filesystem types that support FICLONE (btrfs,
	// bcachefs, and xfs when created with reflink support).
	Reflink bool `json:"reflink"`
}

// ChownOptions controls Chown. A UID or GID of -1 leaves it unchanged.
type ChownOptions struct {
	UID       int
	GID       int
	Recursive bool
	Progress  *Progress
}

// ChmodOptions controls Chmod. Symlinks are skipped during recursive walks
// since Linux symlinks have no meaningful mode of their own.
type ChmodOptions struct {
	Mode uint32
	// DirMode and FileMode, when set, replace Mode for directories and for
	// everything else respectively (e.g. 755 for folders, 644 for files).
	DirMode   *uint32
	FileMode  *uint32
	Recursive bool
	Progress  *Progress
}

// ReflinkMode selects how Copy uses reflinks (FICLONE).
type ReflinkMode string

const (
	// ReflinkAuto tries a reflink and falls back to a regular copy.
	ReflinkAuto ReflinkMode = "auto"
	// ReflinkAlways fails with ErrReflinkUnsupported instead of copying data.
	ReflinkAlways ReflinkMode = "always"
	// ReflinkNever always copies data (still using copy_file_range).
	ReflinkNever ReflinkMode = "never"
)

// OwnerPolicy decides who owns files created by Copy, Move, Mkdir and
// WriteFile.
type OwnerPolicy string

const (
	// OwnerKeep keeps the source's owner and group (Copy, Move). For newly
	// created files (Mkdir, WriteFile) it means the server process (root).
	OwnerKeep OwnerPolicy = "keep"
	// OwnerInherit takes the owner and group of the destination directory,
	// e.g. handing files to the user whose home directory they land in.
	OwnerInherit OwnerPolicy = "inherit"
	// OwnerSet uses the explicit UID and/or GID in the options; an ID of -1
	// falls back to OwnerKeep for that ID.
	OwnerSet OwnerPolicy = "set"
)

// Ownership is embedded in options that create or relocate files.
type Ownership struct {
	Owner OwnerPolicy
	UID   int // used with OwnerSet; -1 for unchanged
	GID   int // used with OwnerSet; -1 for unchanged
}

// CopyOptions controls Copy. Copies are recursive and preserve mode and
// timestamps (like cp -a). When the owner changes, setuid/setgid bits are
// cleared from regular files.
type CopyOptions struct {
	Ownership
	Reflink ReflinkMode
	// Overwrite replaces existing files (unlinking them first, so hard links
	// elsewhere are untouched) and merges into existing directories.
	Overwrite bool
	Progress  *Progress
}

// CopyResult reports what Copy actually did.
type CopyResult struct {
	Files     int   `json:"files"`
	Dirs      int   `json:"dirs"`
	Reflinked int   `json:"reflinked"` // files whose data was shared via FICLONE
	Skipped   int   `json:"skipped"`   // sockets, which can't be copied
	Bytes     int64 `json:"bytes"`
}

// MoveOptions controls Move. With OwnerKeep (or empty) ownership is left
// untouched; any other policy re-owns the moved tree recursively.
type MoveOptions struct {
	Ownership
	Overwrite bool
	// Progress is updated when re-owning the moved tree, or by the copy when
	// moving across filesystems (which also fills in its totals).
	Progress *Progress
}

// DeleteOptions controls Delete.
type DeleteOptions struct {
	Progress *Progress
}

// MoveResult reports what Move did.
type MoveResult struct {
	// Copied is set when src and dst were on different filesystems, so the
	// tree was copied and the source removed.
	Copied bool        `json:"copied"`
	Copy   *CopyResult `json:"copy,omitempty"`
}

// MkdirOptions controls Mkdir. The default owner policy is OwnerInherit.
type MkdirOptions struct {
	Ownership
	Mode uint32 // default 0755
}

// WriteOptions controls WriteFile. The default owner policy is OwnerInherit.
type WriteOptions struct {
	Ownership
	Mode      uint32 // default 0644
	Overwrite bool
}

// CleanPath normalises a client-supplied path to an absolute, root-relative
// form such as "/home/alice". ".." can never climb above "/".
func CleanPath(p string) string {
	return path.Clean("/" + p)
}

// relPath converts a cleaned path to the form openat2 expects relative to the
// root directory fd.
func relPath(p string) string {
	rel := strings.TrimPrefix(CleanPath(p), "/")
	if rel == "" {
		return "."
	}
	return rel
}

// ValidName reports whether name is usable as a single path component.
func ValidName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%w: invalid name %q", ErrInvalidArgument, name)
	}
	return nil
}

func (o Ownership) validate(def OwnerPolicy) (Ownership, error) {
	switch o.Owner {
	case "":
		o.Owner = def
	case OwnerKeep, OwnerInherit:
	case OwnerSet:
		if o.UID < -1 || o.GID < -1 || (o.UID == -1 && o.GID == -1) {
			return o, fmt.Errorf("%w: owner policy \"set\" needs a uid and/or gid", ErrInvalidArgument)
		}
	default:
		return o, fmt.Errorf("%w: unknown owner policy %q", ErrInvalidArgument, o.Owner)
	}
	return o, nil
}
