package api

import (
	"context"
	"errors"
	"io"

	"github.com/alanaktion/xwebfm/internal/logview"
)

// logTool pages through log files: the last lines (tail), earlier lines on
// demand, and new lines as the file grows (follow), with optional
// filtering (see package logview).
var logTool = &FileTool{
	ID:         "log",
	Name:       "Log viewer",
	Extensions: []string{"log", "out", "err"},
	Actions: map[string]ToolAction{
		// {before?, lines?, filter?} → page of lines ending before `before`
		// (default: the end of the file).
		"tail": {Run: func(ctx context.Context, req *ToolRequest) (any, error) {
			var args struct {
				Before *int64         `json:"before"`
				Lines  int            `json:"lines"`
				Filter logview.Filter `json:"filter"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			return logRead(req, func(lf *logFile) (*logview.Page, error) {
				before := lf.Size
				if args.Before != nil {
					before = *args.Before
				}
				return logview.Before(lf.f, lf.Size, before, args.Lines, args.Filter)
			})
		}},
		// {after, inode, lines?, filter?} → page of lines from `after`, or
		// {reset: true} if the file was truncated or replaced (rotated) since
		// the client read it as inode.
		"follow": {Run: func(ctx context.Context, req *ToolRequest) (any, error) {
			var args struct {
				After  int64          `json:"after"`
				Inode  uint64         `json:"inode"`
				Lines  int            `json:"lines"`
				Filter logview.Filter `json:"filter"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			return logRead(req, func(lf *logFile) (*logview.Page, error) {
				if lf.Inode != args.Inode || lf.Size < args.After {
					lf.Reset = true
					return &logview.Page{Lines: []logview.Line{}, Start: 0, End: 0}, nil
				}
				return logview.After(lf.f, lf.Size, args.After, args.Lines, args.Filter)
			})
		}},
	},
}

type logFile struct {
	f     io.ReaderAt
	Size  int64
	Inode uint64
	Reset bool
}

type logPage struct {
	*logview.Page
	Size  int64  `json:"size"`
	Inode uint64 `json:"inode"`
	// Reset means the file was truncated or replaced: read it again from
	// the end.
	Reset bool `json:"reset,omitempty"`
}

func logRead(req *ToolRequest, read func(*logFile) (*logview.Page, error)) (any, error) {
	f, e, err := req.FS.OpenFile(req.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Stat the open file for its current size.
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	lf := &logFile{f: f, Size: st.Size(), Inode: e.Inode}
	page, err := read(lf)
	if errors.Is(err, logview.ErrBadFilter) {
		return nil, &ToolError{err}
	}
	if err != nil {
		return nil, err
	}
	return logPage{Page: page, Size: lf.Size, Inode: lf.Inode, Reset: lf.Reset}, nil
}
