package sqlitedb

import (
	"strings"
	"sync"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/vfs"
)

// vfsName is a VFS that only opens files directly inside directories
// registered with allowDir (/proc/self/fd/N links from fsops.PinFile). The
// default VFS resolves symlinks in the database path and reopens it by name,
// which would undo the pinning; this one hands SQLite the /proc path as is.
// It also confines ATTACH and VACUUM INTO to the database's own directory.
const vfsName = "xwebfm-pinned"

var (
	allowedMu sync.Mutex
	allowed   = map[string]int{} // dir path -> open connections
)

func init() {
	os := vfs.Find("os")
	vfs.Register(vfsName, pinnedVFS{os: os, osf: os.(vfs.VFSFilename)})
}

func allowDir(dir string) {
	allowedMu.Lock()
	allowed[dir]++
	allowedMu.Unlock()
}

func releaseDir(dir string) {
	allowedMu.Lock()
	if allowed[dir]--; allowed[dir] <= 0 {
		delete(allowed, dir)
	}
	allowedMu.Unlock()
}

// permitted reports whether name is a single path component inside an
// allowed directory.
func permitted(name string) bool {
	i := strings.LastIndexByte(name, '/')
	if i < 0 {
		return false
	}
	base := name[i+1:]
	if base == "" || base == "." || base == ".." {
		return false
	}
	allowedMu.Lock()
	defer allowedMu.Unlock()
	return allowed[name[:i]] > 0
}

type pinnedVFS struct {
	os  vfs.VFS
	osf vfs.VFSFilename
}

func (v pinnedVFS) FullPathname(name string) (string, error) {
	if !permitted(name) {
		return "", sqlite3.CANTOPEN
	}
	return name, nil
}

func (v pinnedVFS) Open(name string, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	if name != "" && !permitted(name) {
		return nil, flags, sqlite3.CANTOPEN
	}
	return v.os.Open(name, flags)
}

func (v pinnedVFS) OpenFilename(name *vfs.Filename, flags vfs.OpenFlag) (vfs.File, vfs.OpenFlag, error) {
	// A nil name is a temporary file (VACUUM, large sorts).
	if name != nil && !permitted(name.String()) {
		return nil, flags, sqlite3.CANTOPEN
	}
	return v.osf.OpenFilename(name, flags)
}

func (v pinnedVFS) Delete(name string, syncDir bool) error {
	if !permitted(name) {
		return sqlite3.IOERR_DELETE
	}
	return v.os.Delete(name, syncDir)
}

func (v pinnedVFS) Access(name string, flags vfs.AccessFlag) (bool, error) {
	if !permitted(name) {
		return false, nil
	}
	return v.os.Access(name, flags)
}
