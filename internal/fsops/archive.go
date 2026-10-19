package fsops

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ArchiveFormat identifies an archive or compression format.
type ArchiveFormat string

const (
	FormatZip    ArchiveFormat = "zip"
	Format7z     ArchiveFormat = "7z"
	FormatRar    ArchiveFormat = "rar"
	FormatTar    ArchiveFormat = "tar"
	FormatTarGz  ArchiveFormat = "tar.gz"
	FormatTarBz2 ArchiveFormat = "tar.bz2"
	FormatTarXz  ArchiveFormat = "tar.xz"
	FormatTarZst ArchiveFormat = "tar.zst"
	// Single compressed files; extraction checks whether they hold a tar.
	FormatGz  ArchiveFormat = "gz"
	FormatBz2 ArchiveFormat = "bz2"
	FormatXz  ArchiveFormat = "xz"
	FormatZst ArchiveFormat = "zst"
)

// CreateFormats are the formats CreateArchive writes natively; 7z is
// additionally available when SevenZipAvailable reports true.
var CreateFormats = []ArchiveFormat{FormatZip, FormatTar, FormatTarGz, FormatTarBz2, FormatTarXz, FormatTarZst}

// Extension returns the file extension for a format, including the dot.
func (f ArchiveFormat) Extension() string { return "." + string(f) }

// ArchiveInfo describes a file name recognised as an archive.
type ArchiveInfo struct {
	Format ArchiveFormat `json:"format"`
	// Base is the name without archive or volume suffixes, used for the
	// extraction directory ("photos.part1.rar" → "photos").
	Base string `json:"base"`
	// First is the name of the first volume of a multi-volume set (the name
	// itself for single-file archives).
	First string `json:"first"`
	// Split is set for byte-split volumes (name.7z.001, name.zip.001), which
	// are concatenated before reading.
	Split bool `json:"split,omitempty"`
}

var (
	rarPartRe = regexp.MustCompile(`(?i)^(.+)\.part(\d+)\.rar$`)
	rarOldRe  = regexp.MustCompile(`(?i)^(.+)\.r\d\d$`)
	splitRe   = regexp.MustCompile(`(?i)^(.+)\.(7z|zip)\.(\d{3})$`)
	// Longest suffixes first so ".tar.gz" wins over ".gz".
	suffixes = []struct {
		ext    string
		format ArchiveFormat
	}{
		{".tar.gz", FormatTarGz}, {".tgz", FormatTarGz},
		{".tar.bz2", FormatTarBz2}, {".tbz2", FormatTarBz2}, {".tbz", FormatTarBz2},
		{".tar.xz", FormatTarXz}, {".txz", FormatTarXz},
		{".tar.zst", FormatTarZst}, {".tzst", FormatTarZst},
		{".tar", FormatTar}, {".zip", FormatZip}, {".7z", Format7z}, {".rar", FormatRar},
		{".gz", FormatGz}, {".bz2", FormatBz2}, {".xz", FormatXz}, {".zst", FormatZst},
	}
)

// DetectArchive recognises archives by file name.
func DetectArchive(name string) (ArchiveInfo, bool) {
	if m := rarPartRe.FindStringSubmatch(name); m != nil {
		// Keep the volume number's zero padding: x.part01.rar → x.part01.rar.
		first := fmt.Sprintf("%s.part%0*d.rar", m[1], len(m[2]), 1)
		return ArchiveInfo{Format: FormatRar, Base: m[1], First: first}, true
	}
	if m := rarOldRe.FindStringSubmatch(name); m != nil {
		return ArchiveInfo{Format: FormatRar, Base: m[1], First: m[1] + ".rar"}, true
	}
	if m := splitRe.FindStringSubmatch(name); m != nil {
		return ArchiveInfo{
			Format: ArchiveFormat(strings.ToLower(m[2])),
			Base:   m[1],
			First:  m[1] + "." + m[2] + ".001",
			Split:  true,
		}, true
	}
	lower := strings.ToLower(name)
	for _, s := range suffixes {
		if strings.HasSuffix(lower, s.ext) && len(name) > len(s.ext) {
			return ArchiveInfo{Format: s.format, Base: name[:len(name)-len(s.ext)], First: name}, true
		}
	}
	return ArchiveInfo{}, false
}

// ExtractOptions controls Extract. The default owner policy is
// OwnerInherit; OwnerKeep uses the owner recorded in tar headers and root
// for formats that don't record one.
type ExtractOptions struct {
	Ownership
	Password string // for encrypted 7z and rar archives
	// DeleteSource removes the archive (every volume of a multi-volume set)
	// once extraction has succeeded.
	DeleteSource bool
	Progress     *Progress
}

// ExtractResult reports where an archive was extracted to.
type ExtractResult struct {
	// Path is the single top-level entry, or the directory created to hold
	// several.
	Path    string   `json:"path"`
	Entries int      `json:"entries"`
	Skipped []string `json:"skipped,omitempty"` // entry names and reasons, capped
	Volumes []string `json:"volumes"`
}

// ErrEncrypted is returned when an archive needs a (different) password.
var ErrEncrypted = errors.New("archive is encrypted; a correct password is required")

// CreateArchiveOptions controls CreateArchive. The default owner policy
// for the new archive file is OwnerInherit.
type CreateArchiveOptions struct {
	Ownership
	Format    ArchiveFormat
	Overwrite bool
	// DeleteSources removes the sources once the archive is complete.
	DeleteSources bool
	Progress      *Progress
}
