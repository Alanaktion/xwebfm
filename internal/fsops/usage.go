package fsops

import (
	"fmt"
	"path"
	"strings"
	"time"
)

// UsageItem is the usage of one immediate child of the scanned directory.
type UsageItem struct {
	Name    string   `json:"name"`
	Path    string   `json:"path"`
	Type    FileType `json:"type"`
	Size    int64    `json:"size"` // apparent size of regular files
	Disk    int64    `json:"disk"` // allocated blocks
	Entries int64    `json:"entries"`
}

// OwnerUsage is the usage attributed to one user within the scanned tree.
type OwnerUsage struct {
	UID     uint32 `json:"uid"`
	User    string `json:"user,omitempty"`
	Size    int64  `json:"size"`
	Disk    int64  `json:"disk"`
	Entries int64  `json:"entries"`
}

// UsageReport summarises a directory tree, like du/ncdu.
type UsageReport struct {
	Path     string       `json:"path"`
	Size     int64        `json:"size"`
	Disk     int64        `json:"disk"`
	Entries  int64        `json:"entries"`
	Children []UsageItem  `json:"children"`
	ByOwner  []OwnerUsage `json:"byOwner"`
}

// FindQuery filters Find results; all set criteria must match.
type FindQuery struct {
	// Name is a case-insensitive glob matched against the base name; text
	// without wildcards matches as a substring.
	Name           string     `json:"name,omitempty"`
	Type           FileType   `json:"type,omitempty"`
	UID            *uint32    `json:"uid,omitempty"`
	GID            *uint32    `json:"gid,omitempty"`
	MinSize        *int64     `json:"minSize,omitempty"` // size filters match regular files only
	MaxSize        *int64     `json:"maxSize,omitempty"`
	ModifiedAfter  *time.Time `json:"modifiedAfter,omitempty"`
	ModifiedBefore *time.Time `json:"modifiedBefore,omitempty"`
	// Orphaned matches owners or groups with no passwd/group entry.
	Orphaned bool `json:"orphaned,omitempty"`
	// ForeignOwner matches entries not owned by the owner of the search root.
	ForeignOwner bool `json:"foreignOwner,omitempty"`
	// WorldWritable matches non-symlinks writable by others.
	WorldWritable bool `json:"worldWritable,omitempty"`
	// Setuid matches files with setuid or setgid set.
	Setuid bool `json:"setuid,omitempty"`
	// CrossMounts descends into other filesystems mounted below the root.
	CrossMounts bool `json:"crossMounts,omitempty"`
	// Content, when set, also requires a regular text file containing this
	// text (case-insensitive). Off by default since it reads every
	// candidate file; binary files and files over ContentMaxSize are
	// skipped and counted in FindResult.ContentSkipped.
	Content        string `json:"content,omitempty"`
	ContentMaxSize *int64 `json:"contentMaxSize,omitempty"` // default 4 MiB, at most 64 MiB
	Limit          int    `json:"limit,omitempty"`          // default and maximum 5000
}

// ContentMatch is the first line of a file that matched a content search.
type ContentMatch struct {
	Line int    `json:"line"` // 1-based
	Text string `json:"text"` // the line, shortened around the match
}

// FindResult holds the matches of a Find.
type FindResult struct {
	Root      string  `json:"root"`
	Entries   []Entry `json:"entries"`
	Truncated bool    `json:"truncated"`
	Scanned   int64   `json:"scanned"`
	// Matches holds the first matching line per path for content searches.
	Matches map[string]ContentMatch `json:"matches,omitempty"`
	// ContentSkipped counts files that met the other criteria but weren't
	// searched: too large, binary or unreadable.
	ContentSkipped int64 `json:"contentSkipped,omitempty"`
}

const (
	// MaxFindResults caps the matches Find returns.
	MaxFindResults = 5000
	// DefaultContentMaxSize is the largest file a content search reads
	// unless the query says otherwise; MaxContentMaxSize is the ceiling.
	DefaultContentMaxSize = 4 << 20
	MaxContentMaxSize     = 64 << 20
	maxContentQuery       = 256
)

// Validate checks a query before it is run.
func (q FindQuery) Validate() error {
	if q.Name != "" {
		if _, err := path.Match(q.Name, ""); err != nil {
			return fmt.Errorf("%w: bad name pattern %q", ErrInvalidArgument, q.Name)
		}
	}
	switch q.Type {
	case "", TypeFile, TypeDir, TypeSymlink, TypeFIFO, TypeSocket, TypeBlock, TypeChar:
	default:
		return fmt.Errorf("%w: unknown type %q", ErrInvalidArgument, q.Type)
	}
	if len(q.Content) > maxContentQuery {
		return fmt.Errorf("%w: content search text is longer than %d bytes", ErrInvalidArgument, maxContentQuery)
	}
	if strings.ContainsAny(q.Content, "\r\n") {
		return fmt.Errorf("%w: content search text must be a single line", ErrInvalidArgument)
	}
	if m := q.ContentMaxSize; m != nil && (*m <= 0 || *m > MaxContentMaxSize) {
		return fmt.Errorf("%w: content size limit must be between 1 byte and %d MiB", ErrInvalidArgument, MaxContentMaxSize>>20)
	}
	return nil
}
