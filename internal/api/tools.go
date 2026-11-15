package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

// A FileTool is a viewer or editor for a kind of file, such as SQLite
// databases. The UI offers it as an action on files with matching
// extensions and calls its actions as
//
//	POST /api/v1/tools/{tool}/{action}  {"path": "/file", "args": {...}}
//
// The path must name a regular file (a final symlink isn't followed). The
// server doesn't check the extension, so a misnamed file can still be
// opened with a tool. To add a tool, implement its actions and list it in
// fileTools; the UI side lives in web/src/lib/tools.
type FileTool struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Extensions are lower case, without the dot.
	Extensions []string              `json:"extensions"`
	Actions    map[string]ToolAction `json:"-"`
}

// ToolAction is one operation of a FileTool.
type ToolAction struct {
	// Writes marks actions that may modify the file; clients watching its
	// folder are told it changed.
	Writes bool
	Run    func(ctx context.Context, req *ToolRequest) (any, error)
}

// ToolRequest is a call to a ToolAction.
type ToolRequest struct {
	FS   *fsops.FS
	Path string // cleaned
	args json.RawMessage
}

// Args decodes the action's arguments into v, rejecting unknown fields.
// Missing arguments decode as an empty object.
func (r *ToolRequest) Args(v any) error {
	if len(r.args) == 0 || string(r.args) == "null" {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(r.args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return &ToolError{fmt.Errorf("invalid arguments: %w", err)}
	}
	return nil
}

// ToolError is a failure caused by the request (bad SQL, a missing table…)
// rather than by the server. It's reported as 400 with the error's message.
type ToolError struct{ Err error }

func (e *ToolError) Error() string { return e.Err.Error() }
func (e *ToolError) Unwrap() error { return e.Err }

// fileTools are the registered tools, in the order the UI lists them.
var fileTools = []*FileTool{sqliteTool}

func findTool(id string) *FileTool {
	for _, t := range fileTools {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// maxToolBody limits tool request bodies (cell edits, SQL scripts).
const maxToolBody = 16 << 20

type toolRequest struct {
	Path string          `json:"path"`
	Args json.RawMessage `json:"args"`
}

func (s *Server) handleTool(w http.ResponseWriter, r *http.Request) {
	tool := findTool(r.PathValue("tool"))
	if tool == nil {
		writeError(w, http.StatusNotFound, "not_found", "unknown tool")
		return
	}
	action, ok := tool.Actions[r.PathValue("action")]
	if !ok {
		writeError(w, http.StatusNotFound, "not_found", fmt.Sprintf("unknown %s action", tool.Name))
		return
	}
	var body toolRequest
	if !decodeLimit(w, r, &body, maxToolBody) {
		return
	}
	p := fsops.CleanPath(body.Path)
	e, err := s.FS.Stat(p)
	if err == nil && e.Type != fsops.TypeFile {
		err = &fs.PathError{Op: tool.ID, Path: p, Err: fsops.ErrNotRegular}
	}
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	res, err := action.Run(r.Context(), &ToolRequest{FS: s.FS, Path: p, args: body.Args})
	if action.Writes {
		s.notify(p, false) // even a failed script may have changed something
	}
	var te *ToolError
	switch {
	case errors.As(err, &te):
		writeError(w, http.StatusBadRequest, "tool_error", te.Error())
	case err != nil:
		s.writeFSError(w, r, err)
	default:
		writeJSON(w, http.StatusOK, res)
	}
}
