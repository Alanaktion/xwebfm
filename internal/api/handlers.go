package api

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
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

// idFields accepts either numeric IDs or names for owner and group.
type idFields struct {
	UID   *uint32 `json:"uid"`
	User  string  `json:"user"`
	GID   *uint32 `json:"gid"`
	Group string  `json:"group"`
}

// resolveIDs returns the requested uid and gid, -1 for each one not given.
func (s *Server) resolveIDs(f idFields) (uid, gid int, err error) {
	uid, gid = -1, -1
	switch {
	case f.UID != nil:
		uid = int(*f.UID)
	case f.User != "":
		id, ok := s.FS.Owners.LookupUser(f.User)
		if !ok {
			return -1, -1, fmt.Errorf("%w: unknown user %q", fsops.ErrInvalidArgument, f.User)
		}
		uid = int(id)
	}
	switch {
	case f.GID != nil:
		gid = int(*f.GID)
	case f.Group != "":
		id, ok := s.FS.Owners.LookupGroup(f.Group)
		if !ok {
			return -1, -1, fmt.Errorf("%w: unknown group %q", fsops.ErrInvalidArgument, f.Group)
		}
		gid = int(id)
	}
	return uid, gid, nil
}

// ownerFields selects an owner policy for operations that create files;
// the IDs are used with policy "set".
type ownerFields struct {
	Owner fsops.OwnerPolicy `json:"owner"`
	idFields
}

func (s *Server) ownership(f ownerFields) (fsops.Ownership, error) {
	uid, gid, err := s.resolveIDs(f.idFields)
	return fsops.Ownership{Owner: f.Owner, UID: uid, GID: gid}, err
}

type chownRequest struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive"`
	idFields
}

func (s *Server) handleChown(w http.ResponseWriter, r *http.Request) {
	var req chownRequest
	if !decode(w, r, &req) {
		return
	}
	uid, gid, err := s.resolveIDs(req.idFields)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	if err := s.FS.Chown(req.Path, fsops.ChownOptions{UID: uid, GID: gid, Recursive: req.Recursive}); err != nil {
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
	Overwrite bool              `json:"overwrite"`
	ownerFields
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
	own, err := s.ownership(req.ownerFields)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	res, err := s.FS.Copy(req.Src, req.Dst, fsops.CopyOptions{
		Ownership: own,
		Reflink:   req.Reflink,
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

type moveRequest struct {
	Src       string `json:"src"`
	Dst       string `json:"dst"`
	Overwrite bool   `json:"overwrite"`
	ownerFields
}

type moveResponse struct {
	fsops.MoveResult
	Entry fsops.Entry `json:"entry"`
}

func (s *Server) handleMove(w http.ResponseWriter, r *http.Request) {
	var req moveRequest
	if !decode(w, r, &req) {
		return
	}
	own, err := s.ownership(req.ownerFields)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	res, err := s.FS.Move(req.Src, req.Dst, fsops.MoveOptions{Ownership: own, Overwrite: req.Overwrite})
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	e, err := s.FS.Stat(req.Dst)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, moveResponse{MoveResult: res, Entry: e})
}

type renameRequest struct {
	Path string `json:"path"`
	Name string `json:"name"`
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	var req renameRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.FS.Rename(req.Path, req.Name); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.respondStat(w, r, path.Join(path.Dir(fsops.CleanPath(req.Path)), req.Name))
}

type pathRequest struct {
	Path string `json:"path"`
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	var req pathRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.FS.Delete(req.Path); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type mkdirRequest struct {
	Path string `json:"path"`
	Mode string `json:"mode"` // octal; default 0755
	ownerFields
}

func (s *Server) handleMkdir(w http.ResponseWriter, r *http.Request) {
	var req mkdirRequest
	if !decode(w, r, &req) {
		return
	}
	var mode uint64
	if req.Mode != "" {
		var err error
		if mode, err = strconv.ParseUint(req.Mode, 8, 32); err != nil {
			writeError(w, http.StatusBadRequest, "invalid", fmt.Sprintf("invalid octal mode %q", req.Mode))
			return
		}
	}
	own, err := s.ownership(req.ownerFields)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	if err := s.FS.Mkdir(req.Path, fsops.MkdirOptions{Ownership: own, Mode: uint32(mode)}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.respondStat(w, r, req.Path)
}

// handleUpload streams multipart file parts into the directory given by the
// "dir" query parameter. Query parameters "overwrite", "owner", "user" and
// "group" mirror the JSON fields of other endpoints.
func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	own, err := s.ownership(ownerFields{
		Owner:    fsops.OwnerPolicy(q.Get("owner")),
		idFields: idFields{User: q.Get("user"), Group: q.Get("group")},
	})
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	opts := fsops.WriteOptions{Ownership: own, Overwrite: q.Get("overwrite") == "true"}
	mr, err := r.MultipartReader()
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "expected multipart/form-data")
		return
	}
	entries := []fsops.Entry{}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		if part.FileName() == "" {
			continue
		}
		e, err := s.FS.WriteFile(q.Get("dir"), part.FileName(), part, opts)
		if err != nil {
			s.writeFSError(w, r, err)
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleDownload serves a file (with range support) or a directory as a
// .tar.gz stream.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	p := fsops.CleanPath(r.URL.Query().Get("path"))
	e, err := s.FS.Stat(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	name := e.Name
	if p == "/" {
		name = "root"
	}
	if e.Type == fsops.TypeDir {
		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name + ".tar.gz"}))
		if err := s.FS.WriteTar(p, w); err != nil {
			// Headers are already sent; all we can do is abort the stream.
			s.Log.Error("tar download failed", "path", p, "err", err)
			panic(http.ErrAbortHandler)
		}
		return
	}
	file, e, err := s.FS.OpenFile(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	defer file.Close()
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	http.ServeContent(w, r, name, e.ModTime, file)
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
