//go:build !linux

package fsops

import "errors"

var errUnsupported = errors.New("xwebfm requires Linux")

// FS is unavailable on non-Linux platforms; it exists so the module builds.
type FS struct{ Owners *Owners }

func New(root string, owners *Owners) (*FS, error) { return nil, errUnsupported }
func (f *FS) Root() string                         { return "" }
func (f *FS) Close() error                         { return nil }
func (f *FS) List(p string) ([]Entry, error)       { return nil, errUnsupported }
func (f *FS) Stat(p string) (Entry, error)         { return Entry{}, errUnsupported }
func (f *FS) Chown(p string, o ChownOptions) error { return errUnsupported }
func (f *FS) Chmod(p string, o ChmodOptions) error { return errUnsupported }
func (f *FS) Copy(src, dst string, o CopyOptions) (CopyResult, error) {
	return CopyResult{}, errUnsupported
}
