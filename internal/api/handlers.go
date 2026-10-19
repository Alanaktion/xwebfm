package api

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

type listResponse struct {
	Path    string        `json:"path"`
	Dir     *fsops.Entry  `json:"dir,omitempty"` // the directory itself
	FS      *fsops.FSInfo `json:"fs,omitempty"`
	Entries []fsops.Entry `json:"entries"`
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	p := fsops.CleanPath(r.URL.Query().Get("path"))
	entries, err := s.FS.List(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	res := listResponse{Path: p, Entries: entries}
	if dir, err := s.FS.Stat(p); err == nil {
		res.Dir = &dir
	}
	if info, err := s.FS.StatFS(p); err == nil {
		res.FS = &info
	}
	writeJSON(w, http.StatusOK, res)
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
	if err := s.FS.Chown(r.Context(), req.Path, fsops.ChownOptions{UID: uid, GID: gid, Recursive: req.Recursive}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, true)
	s.respondStat(w, r, req.Path)
}

type chmodRequest struct {
	Path string `json:"path"`
	modeFields
	Recursive bool `json:"recursive"`
}

// modeFields gives either one octal mode for everything, or separate modes
// for directories and for files (e.g. "2775" and "664").
type modeFields struct {
	Mode     string `json:"mode"`
	DirMode  string `json:"dirMode"`
	FileMode string `json:"fileMode"`
}

func parseMode(s string) (*uint32, error) {
	if s == "" {
		return nil, nil
	}
	m, err := strconv.ParseUint(s, 8, 32)
	if err != nil || m > 0o7777 {
		return nil, fmt.Errorf("%w: invalid octal mode %q", fsops.ErrInvalidArgument, s)
	}
	v := uint32(m)
	return &v, nil
}

func (f modeFields) options() (fsops.ChmodOptions, error) {
	var o fsops.ChmodOptions
	mode, err := parseMode(f.Mode)
	if err != nil {
		return o, err
	}
	if o.DirMode, err = parseMode(f.DirMode); err != nil {
		return o, err
	}
	if o.FileMode, err = parseMode(f.FileMode); err != nil {
		return o, err
	}
	switch {
	case mode != nil:
		o.Mode = *mode
	case o.DirMode == nil || o.FileMode == nil:
		return o, fmt.Errorf("%w: give mode, or both dirMode and fileMode", fsops.ErrInvalidArgument)
	}
	return o, nil
}

func (s *Server) handleChmod(w http.ResponseWriter, r *http.Request) {
	var req chmodRequest
	if !decode(w, r, &req) {
		return
	}
	opts, err := req.modeFields.options()
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	opts.Recursive = req.Recursive
	if err := s.FS.Chmod(r.Context(), req.Path, opts); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, true)
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
	res, err := s.FS.Copy(r.Context(), req.Src, req.Dst, fsops.CopyOptions{
		Ownership: own,
		Reflink:   req.Reflink,
		Overwrite: req.Overwrite,
	})
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Dst, false)
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
	res, err := s.FS.Move(r.Context(), req.Src, req.Dst, fsops.MoveOptions{Ownership: own, Overwrite: req.Overwrite})
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Src, false)
	s.notify(req.Dst, false)
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
	s.notify(req.Path, false)
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
	if err := s.FS.Delete(r.Context(), req.Path, fsops.DeleteOptions{}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, false)
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
	s.notify(req.Path, false)
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
	s.notify(fsops.CleanPath(q.Get("dir")), true)
	writeJSON(w, http.StatusOK, entries)
}

// handleDownload serves a file (with range support) or a directory as a
// .tar.gz stream.
// inlineTypes are the media types served inline for viewing. Everything
// else is served inline as text/plain, so user-controlled HTML or SVG can
// never run in the app's origin.
var inlineTypes = []string{"image/png", "image/jpeg", "image/gif", "image/webp", "image/avif", "image/bmp", "audio/", "video/"}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	// Defence in depth for anything the browser renders from here.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	p := fsops.CleanPath(r.URL.Query().Get("path"))
	inline := r.URL.Query().Get("inline") == "1"
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
		if err := s.FS.WriteTar(r.Context(), p, w); err != nil {
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
	disposition, ctype := "attachment", "application/octet-stream"
	if inline {
		disposition, ctype = "inline", "text/plain; charset=utf-8"
		if t, _, _ := mime.ParseMediaType(mime.TypeByExtension(path.Ext(name))); t != "" {
			for _, ok := range inlineTypes {
				if t == ok || strings.HasSuffix(ok, "/") && strings.HasPrefix(t, ok) {
					ctype = t
				}
			}
		}
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	http.ServeContent(w, r, name, e.ModTime, file)
}

type writeRequest struct {
	Path        string    `json:"path"`
	Content     string    `json:"content"`
	ExpectSize  int64     `json:"expectSize"`
	ExpectMtime time.Time `json:"expectMtime"`
}

// handleWrite saves edited text over an existing file (see
// fsops.ReplaceFile); 409 means it changed since it was loaded.
func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	var req writeRequest
	if !decodeLimit(w, r, &req, 2*fsops.MaxEditSize) {
		return
	}
	e, err := s.FS.ReplaceFile(req.Path, strings.NewReader(req.Content), req.ExpectSize, req.ExpectMtime)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, false)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.FS.Owners.Users())
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.FS.Owners.Groups())
}

type infoResponse struct {
	Archive struct {
		Create []fsops.ArchiveFormat `json:"create"`
	} `json:"archive"`
}

// handleInfo reports server capabilities the UI adapts to.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	var res infoResponse
	res.Archive.Create = slices.Clone(fsops.CreateFormats)
	if fsops.SevenZipAvailable() {
		res.Archive.Create = append(res.Archive.Create, fsops.Format7z)
	}
	writeJSON(w, http.StatusOK, res)
}

type propsResponse struct {
	Entry fsops.Entry  `json:"entry"`
	ACL   *fsops.ACL   `json:"acl"`   // null for symlinks or without ACL support
	Flags *fsops.Flags `json:"flags"` // null for special files or without support
}

// handleProps returns an entry with its ACL and inode attributes.
func (s *Server) handleProps(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Query().Get("path")
	e, err := s.FS.Stat(p)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	res := propsResponse{Entry: e}
	if acl, err := s.FS.GetACL(p); err == nil {
		res.ACL = &acl
	}
	if fl, err := s.FS.GetFlags(p); err == nil {
		res.Flags = &fl
	}
	writeJSON(w, http.StatusOK, res)
}

type aclRequest struct {
	Path string    `json:"path"`
	ACL  fsops.ACL `json:"acl"`
}

// handleACL sets the ACL of one path; recursive changes go through the
// "acl" job kind.
func (s *Server) handleACL(w http.ResponseWriter, r *http.Request) {
	var req aclRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.FS.SetACL(r.Context(), req.Path, fsops.SetACLOptions{ACL: req.ACL}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, false)
	s.handlePropsFor(w, r, req.Path)
}

type flagsRequest struct {
	Path  string      `json:"path"`
	Flags fsops.Flags `json:"flags"`
}

func (s *Server) handleFlags(w http.ResponseWriter, r *http.Request) {
	var req flagsRequest
	if !decode(w, r, &req) {
		return
	}
	if err := s.FS.SetFlags(req.Path, req.Flags); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.handlePropsFor(w, r, req.Path)
}

func (s *Server) handlePropsFor(w http.ResponseWriter, r *http.Request, p string) {
	r2 := r.Clone(r.Context())
	r2.URL.RawQuery = "path=" + url.QueryEscape(p)
	s.handleProps(w, r2)
}

type linkRequest struct {
	Target   string         `json:"target"`
	Path     string         `json:"path"`
	Kind     fsops.LinkKind `json:"kind"`
	Relative bool           `json:"relative"`
	ownerFields
}

func (s *Server) handleLink(w http.ResponseWriter, r *http.Request) {
	var req linkRequest
	if !decode(w, r, &req) {
		return
	}
	own, err := s.ownership(req.ownerFields)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	if err := s.FS.Link(req.Target, req.Path, fsops.LinkOptions{Ownership: own, Kind: req.Kind, Relative: req.Relative}); err != nil {
		s.writeFSError(w, r, err)
		return
	}
	s.notify(req.Path, false)
	s.respondStat(w, r, req.Path)
}

// notify tells WebSocket clients that p's parent directory changed (and p
// itself, if self is set, e.g. after chmod of a directory's contents).
func (s *Server) notify(p string, self bool) {
	if s.Jobs == nil {
		return
	}
	p = fsops.CleanPath(p)
	if self {
		s.Jobs.Notify(path.Dir(p), p)
	} else {
		s.Jobs.Notify(path.Dir(p))
	}
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
