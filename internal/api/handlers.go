package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

type listResponse struct {
	Path    string        `json:"path"`
	Entries []fsops.Entry `json:"entries"`
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	p := fsops.CleanPath(r.URL.Query().Get("path"))
	entries, err := s.FS.List(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, listResponse{Path: p, Entries: entries})
}

func (s *Server) handleStat(w http.ResponseWriter, r *http.Request) {
	e, err := s.FS.Stat(r.URL.Query().Get("path"))
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// chownRequest accepts either numeric IDs or names for owner and group.
// Omitted fields are left unchanged.
type chownRequest struct {
	Path      string  `json:"path"`
	UID       *uint32 `json:"uid"`
	User      string  `json:"user"`
	GID       *uint32 `json:"gid"`
	Group     string  `json:"group"`
	Recursive bool    `json:"recursive"`
}

func (s *Server) handleChown(w http.ResponseWriter, r *http.Request) {
	var req chownRequest
	if !decode(w, r, &req) {
		return
	}
	opts := fsops.ChownOptions{UID: -1, GID: -1, Recursive: req.Recursive}
	switch {
	case req.UID != nil:
		opts.UID = int(*req.UID)
	case req.User != "":
		id, ok := s.FS.Owners.LookupUser(req.User)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("unknown user %q", req.User))
			return
		}
		opts.UID = int(id)
	}
	switch {
	case req.GID != nil:
		opts.GID = int(*req.GID)
	case req.Group != "":
		id, ok := s.FS.Owners.LookupGroup(req.Group)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("unknown group %q", req.Group))
			return
		}
		opts.GID = int(id)
	}
	if err := s.FS.Chown(req.Path, opts); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.respondStat(w, r, req.Path)
}

type chmodRequest struct {
	Path      string `json:"path"`
	Mode      string `json:"mode"` // octal, e.g. "0755" or "2775"
	Recursive bool   `json:"recursive"`
}

func (s *Server) handleChmod(w http.ResponseWriter, r *http.Request) {
	var req chmodRequest
	if !decode(w, r, &req) {
		return
	}
	mode, err := strconv.ParseUint(req.Mode, 8, 32)
	if err != nil || mode > 0o7777 {
		writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("invalid octal mode %q", req.Mode))
		return
	}
	if err := s.FS.Chmod(req.Path, fsops.ChmodOptions{Mode: uint32(mode), Recursive: req.Recursive}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.respondStat(w, r, req.Path)
}

type copyRequest struct {
	Src       string            `json:"src"`
	Dst       string            `json:"dst"`
	Reflink   fsops.ReflinkMode `json:"reflink"`
	Preserve  bool              `json:"preserve"`
	Overwrite bool              `json:"overwrite"`
}

type copyResponse struct {
	fsops.CopyResult
	Entry fsops.Entry `json:"entry"`
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	var req copyRequest
	if !decode(w, r, &req) {
		return
	}
	res, err := s.FS.Copy(req.Src, req.Dst, fsops.CopyOptions{
		Reflink:   req.Reflink,
		Preserve:  req.Preserve,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	e, err := s.FS.Stat(req.Dst)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, copyResponse{CopyResult: res, Entry: e})
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.FS.Owners.Users())
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.FS.Owners.Groups())
}

// respondStat replies with the updated entry after a modifying operation.
func (s *Server) respondStat(w http.ResponseWriter, r *http.Request, p string) {
	e, err := s.FS.Stat(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}
