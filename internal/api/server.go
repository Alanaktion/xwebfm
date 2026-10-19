// Package api exposes fsops over a JSON HTTP API.
//
// The API performs no authentication: xwebfm is meant to run behind a
// reverse proxy or other layer that authenticates the single admin user.
package api

import (
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"syscall"

	"github.com/alanaktion/xwebfm/internal/fsops"
	"github.com/alanaktion/xwebfm/internal/jobs"
)

// Server holds the dependencies of the HTTP handlers.
type Server struct {
	FS     *fsops.FS
	Jobs   *jobs.Manager
	Log    *slog.Logger
	Static http.Handler // serves the SPA; may be nil
	// OriginPatterns lists extra origins (host patterns) allowed to open the
	// WebSocket, for proxies that don't preserve the Host header.
	OriginPatterns []string
}

// Handler returns the root HTTP handler, including middleware.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /api/v1/fs/list", s.handleList)
	mux.HandleFunc("GET /api/v1/fs/stat", s.handleStat)
	mux.HandleFunc("POST /api/v1/fs/chown", s.handleChown)
	mux.HandleFunc("POST /api/v1/fs/chmod", s.handleChmod)
	mux.HandleFunc("POST /api/v1/fs/copy", s.handleCopy)
	mux.HandleFunc("POST /api/v1/fs/move", s.handleMove)
	mux.HandleFunc("POST /api/v1/fs/rename", s.handleRename)
	mux.HandleFunc("POST /api/v1/fs/delete", s.handleDelete)
	mux.HandleFunc("POST /api/v1/fs/mkdir", s.handleMkdir)
	mux.HandleFunc("POST /api/v1/fs/upload", s.handleUpload)
	mux.HandleFunc("GET /api/v1/fs/download", s.handleDownload)
	mux.HandleFunc("GET /api/v1/fs/props", s.handleProps)
	mux.HandleFunc("POST /api/v1/fs/acl", s.handleACL)
	mux.HandleFunc("POST /api/v1/fs/flags", s.handleFlags)
	mux.HandleFunc("POST /api/v1/fs/link", s.handleLink)
	mux.HandleFunc("POST /api/v1/fs/write", s.handleWrite)
	mux.HandleFunc("GET /api/v1/jobs", s.handleListJobs)
	mux.HandleFunc("POST /api/v1/jobs", s.handleSubmitJob)
	mux.HandleFunc("GET /api/v1/jobs/{id}/result", s.handleJobResult)
	mux.HandleFunc("POST /api/v1/jobs/{id}/cancel", s.handleCancelJob)
	mux.HandleFunc("POST /api/v1/jobs/{id}/dismiss", s.handleDismissJob)
	mux.HandleFunc("POST /api/v1/jobs/clear", s.handleClearJobs)
	mux.HandleFunc("GET /api/v1/ws", s.handleWS)
	mux.HandleFunc("GET /api/v1/info", s.handleInfo)
	mux.HandleFunc("GET /api/v1/users", s.handleUsers)
	mux.HandleFunc("GET /api/v1/groups", s.handleGroups)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint")
	})
	if s.Static != nil {
		mux.Handle("/", s.Static)
	}
	return s.recoverer(s.logger(requireJSON(mux)))
}

type errorBody struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var body errorBody
	body.Error.Code, body.Error.Message = code, msg
	writeJSON(w, status, body)
}

// writeFSError maps filesystem errors onto HTTP statuses.
func (s *Server) writeFSError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := http.StatusInternalServerError, "internal"
	switch {
	case errors.Is(err, fs.ErrNotExist):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, fs.ErrExist), errors.Is(err, syscall.ENOTEMPTY):
		status, code = http.StatusConflict, "exists"
	case errors.Is(err, fs.ErrPermission):
		status, code = http.StatusForbidden, "permission_denied"
	case errors.Is(err, fsops.ErrReflinkUnsupported):
		status, code = http.StatusUnprocessableEntity, "reflink_unsupported"
	case errors.Is(err, fsops.ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, fsops.ErrInvalidArgument), errors.Is(err, fsops.ErrNotRegular),
		errors.Is(err, fsops.ErrIsSymlink), errors.Is(err, syscall.ENOTDIR),
		errors.Is(err, syscall.ELOOP), errors.Is(err, syscall.EISDIR):
		status, code = http.StatusBadRequest, "invalid"
	}
	if status == http.StatusInternalServerError {
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	writeError(w, status, code, err.Error())
}

// decode reads a JSON request body into v, rejecting unknown fields.
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	return decodeLimit(w, r, v, 1<<20)
}

// decodeLimit is decode with a custom body size limit.
func decodeLimit(w http.ResponseWriter, r *http.Request, v any, limit int64) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid JSON body: "+err.Error())
		return false
	}
	return true
}
