package fsops

import "fmt"

// ACLEntry is a named POSIX ACL entry: a specific user or group and its
// permissions (bits 4=r, 2=w, 1=x).
type ACLEntry struct {
	Kind string `json:"kind"` // "user" or "group"
	ID   uint32 `json:"id"`
	Name string `json:"name,omitempty"` // filled in when reading
	Perm uint8  `json:"perm"`
}

// ACL is the part of a POSIX ACL that isn't already expressed by the mode
// bits: named entries, the mask, and the default ACL of a directory.
type ACL struct {
	Named []ACLEntry `json:"named"`
	// Mask limits named entries and the owning group; it is recomputed
	// from them whenever an ACL is written.
	Mask *uint8 `json:"mask,omitempty"`
	// Default is inherited by new files in a directory. HasDefault
	// distinguishes "no default ACL" from one with only base entries.
	Default    []ACLEntry `json:"default"`
	HasDefault bool       `json:"hasDefault"`
}

// SetACLOptions controls SetACL. The owner/group/other entries always come
// from each file's own mode bits (change them with Chmod), so applying an
// ACL recursively only syncs the named entries, like setfacl -R -m. On
// files without any execute bit, execute is dropped from named entries
// (capital-X semantics).
type SetACLOptions struct {
	ACL
	Recursive bool
	Progress  *Progress
}

// Flags are inode attributes as shown by lsattr.
type Flags struct {
	Immutable  bool `json:"immutable"`
	AppendOnly bool `json:"appendOnly"`
	NoDump     bool `json:"noDump"`
	NoAtime    bool `json:"noAtime"`
	// NoCOW disables copy-on-write on btrfs; it only takes effect on empty
	// files or for files created later in a directory.
	NoCOW bool `json:"noCow"`
}

// LinkKind is the kind of link created by Link.
type LinkKind string

const (
	Symlink  LinkKind = "symlink"
	Hardlink LinkKind = "hard"
)

// LinkOptions controls Link. The default owner policy (for symlinks) is
// OwnerInherit; hard links share their target's inode and owner.
type LinkOptions struct {
	Ownership
	Kind LinkKind
	// Relative makes a symlink's target relative to the link's directory.
	Relative bool
}

func (a ACL) validate() error {
	for _, list := range [][]ACLEntry{a.Named, a.Default} {
		for _, e := range list {
			if e.Kind != "user" && e.Kind != "group" {
				return fmt.Errorf("%w: ACL entry kind %q", ErrInvalidArgument, e.Kind)
			}
			if e.Perm > 7 {
				return fmt.Errorf("%w: ACL permission %d", ErrInvalidArgument, e.Perm)
			}
		}
	}
	return nil
}
