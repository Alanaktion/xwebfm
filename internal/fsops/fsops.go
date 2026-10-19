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
}

// ChownOptions controls Chown. A UID or GID of -1 leaves it unchanged.
type ChownOptions struct {
	UID       int
	GID       int
	Recursive bool
}

// ChmodOptions controls Chmod. Symlinks are skipped during recursive walks
// since Linux symlinks have no meaningful mode of their own.
type ChmodOptions struct {
	Mode      uint32
	Recursive bool
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

// CopyOptions controls Copy.
type CopyOptions struct {
	Reflink ReflinkMode
	// Preserve copies owner, group, full mode and timestamps from the source.
	// Without it the new file is owned by the server process (normally root)
	// and gets the source's permission bits without setuid/setgid/sticky.
	Preserve bool
	// Overwrite allows replacing an existing destination file.
	Overwrite bool
}

// CopyResult reports what Copy actually did.
type CopyResult struct {
	Reflinked bool  `json:"reflinked"`
	Bytes     int64 `json:"bytes"`
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
