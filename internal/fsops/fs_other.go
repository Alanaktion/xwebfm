//go:build !linux

package fsops

import (
	"context"
	"errors"
	"io"
	"os"
)

var errUnsupported = errors.New("xwebfm requires Linux")

// FS is unavailable on non-Linux platforms; it exists so the module builds.
type FS struct{ Owners *Owners }

func New(root string, owners *Owners) (*FS, error)                        { return nil, errUnsupported }
func (f *FS) Root() string                                                { return "" }
func (f *FS) Close() error                                                { return nil }
func (f *FS) List(p string) ([]Entry, error)                              { return nil, errUnsupported }
func (f *FS) Stat(p string) (Entry, error)                                { return Entry{}, errUnsupported }
func (f *FS) Chown(ctx context.Context, p string, o ChownOptions) error   { return errUnsupported }
func (f *FS) Chmod(ctx context.Context, p string, o ChmodOptions) error   { return errUnsupported }
func (f *FS) Rename(p, name string) error                                 { return errUnsupported }
func (f *FS) Delete(ctx context.Context, p string, o DeleteOptions) error { return errUnsupported }
func (f *FS) Mkdir(p string, o MkdirOptions) error                        { return errUnsupported }
func (f *FS) OpenFile(p string) (*os.File, Entry, error)                  { return nil, Entry{}, errUnsupported }
func (f *FS) WriteTar(ctx context.Context, p string, w io.Writer) error {
	return errUnsupported
}
func (f *FS) Usage(ctx context.Context, p string) (int64, int64, error) {
	return 0, 0, errUnsupported
}
func (f *FS) Copy(ctx context.Context, src, dst string, o CopyOptions) (CopyResult, error) {
	return CopyResult{}, errUnsupported
}
func (f *FS) Move(ctx context.Context, src, dst string, o MoveOptions) (MoveResult, error) {
	return MoveResult{}, errUnsupported
}
func (f *FS) WriteFile(dir, name string, r io.Reader, o WriteOptions) (Entry, error) {
	return Entry{}, errUnsupported
}
func (f *FS) Extract(ctx context.Context, archive, destDir string, o ExtractOptions) (ExtractResult, error) {
	return ExtractResult{}, errUnsupported
}
func (f *FS) CreateArchive(ctx context.Context, sources []string, dst string, o CreateArchiveOptions) (Entry, error) {
	return Entry{}, errUnsupported
}

// SevenZipAvailable reports whether .7z archives can be created.
func SevenZipAvailable() bool { return false }
