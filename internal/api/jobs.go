package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strconv"

	"github.com/alanaktion/xwebfm/internal/fsops"
	"github.com/alanaktion/xwebfm/internal/jobs"
)

// jobRequest submits a background job. Which fields apply depends on Kind:
// copy, move and extract use Items (extract: src archive, dst directory);
// delete, chown, chmod and archive use Paths (archive also Dst and Format).
type jobRequest struct {
	Kind      string              `json:"kind"`
	Items     []jobs.Item         `json:"items"`
	Paths     []string            `json:"paths"`
	Dst       string              `json:"dst"`
	Reflink   fsops.ReflinkMode   `json:"reflink"`
	Overwrite bool                `json:"overwrite"`
	Recursive bool                `json:"recursive"`
	Mode      string              `json:"mode"`
	Format    fsops.ArchiveFormat `json:"format"`
	// Password for encrypted archives. It is only held by the job's closure,
	// never stored in the job's public state.
	Password string `json:"password"`
	// DeleteOriginals removes the archive (extract) or the sources
	// (archive) once the job has succeeded.
	DeleteOriginals bool `json:"deleteOriginals"`
	ownerFields
}

// stepFunc performs one item of a job.
type stepFunc func(ctx context.Context, p *fsops.Progress, i int) error

func (s *Server) handleSubmitJob(w http.ResponseWriter, r *http.Request) {
	var req jobRequest
	if !decode(w, r, &req) {
		return
	}
	spec, err := s.buildJob(req)
	if err != nil {
		s.writeFSError(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, s.Jobs.Submit(spec))
}

// buildJob validates a request fully, so that a bad request fails with 400
// rather than as a failed job, and returns a Spec whose Run performs it.
func (s *Server) buildJob(req jobRequest) (jobs.Spec, error) {
	invalid := func(format string, args ...any) (jobs.Spec, error) {
		return jobs.Spec{}, fmt.Errorf("%w: "+format, append([]any{fsops.ErrInvalidArgument}, args...)...)
	}
	for i := range req.Items {
		req.Items[i].Src = fsops.CleanPath(req.Items[i].Src)
		req.Items[i].Dst = fsops.CleanPath(req.Items[i].Dst)
	}
	for i := range req.Paths {
		req.Paths[i] = fsops.CleanPath(req.Paths[i])
	}
	var (
		spec  = jobs.Spec{Kind: req.Kind}
		dirs  []string
		scan  []string // paths whose size makes up the progress total
		count int
		step  stepFunc
	)

	switch req.Kind {
	case "copy", "move":
		if len(req.Items) == 0 {
			return invalid("no items")
		}
		own, err := s.ownership(req.ownerFields)
		if err != nil {
			return jobs.Spec{}, err
		}
		if err := checkOwnership(own); err != nil {
			return jobs.Spec{}, err
		}
		for _, it := range req.Items {
			spec.Items = append(spec.Items, it)
			dirs = append(dirs, path.Dir(it.Dst))
		}
		count = len(req.Items)
		if req.Kind == "copy" {
			switch req.Reflink {
			case "", fsops.ReflinkAuto, fsops.ReflinkAlways, fsops.ReflinkNever:
			default:
				return invalid("unknown reflink mode %q", req.Reflink)
			}
			for _, it := range req.Items {
				scan = append(scan, it.Src)
			}
			opts := fsops.CopyOptions{Ownership: own, Reflink: req.Reflink, Overwrite: req.Overwrite}
			step = func(ctx context.Context, p *fsops.Progress, i int) error {
				o := opts
				o.Progress = p
				_, err := s.FS.Copy(ctx, req.Items[i].Src, req.Items[i].Dst, o)
				return err
			}
		} else {
			for _, it := range req.Items {
				dirs = append(dirs, path.Dir(it.Src))
			}
			opts := fsops.MoveOptions{Ownership: own, Overwrite: req.Overwrite}
			step = func(ctx context.Context, p *fsops.Progress, i int) error {
				o := opts
				o.Progress = p
				_, err := s.FS.Move(ctx, req.Items[i].Src, req.Items[i].Dst, o)
				return err
			}
		}

	case "delete", "chown", "chmod":
		if len(req.Paths) == 0 {
			return invalid("no paths")
		}
		for _, p := range req.Paths {
			spec.Items = append(spec.Items, jobs.Item{Src: p})
			dirs = append(dirs, path.Dir(p), p)
		}
		count = len(req.Paths)
		if req.Kind == "delete" || req.Recursive {
			scan = req.Paths
		}
		switch req.Kind {
		case "delete":
			step = func(ctx context.Context, p *fsops.Progress, i int) error {
				return s.FS.Delete(ctx, req.Paths[i], fsops.DeleteOptions{Progress: p})
			}
		case "chown":
			uid, gid, err := s.resolveIDs(req.idFields)
			if err != nil {
				return jobs.Spec{}, err
			}
			if uid == -1 && gid == -1 {
				return invalid("uid and/or gid required")
			}
			step = func(ctx context.Context, p *fsops.Progress, i int) error {
				return s.FS.Chown(ctx, req.Paths[i], fsops.ChownOptions{UID: uid, GID: gid, Recursive: req.Recursive, Progress: p})
			}
		case "chmod":
			mode, err := strconv.ParseUint(req.Mode, 8, 32)
			if err != nil || mode > 0o7777 {
				return invalid("invalid octal mode %q", req.Mode)
			}
			step = func(ctx context.Context, p *fsops.Progress, i int) error {
				return s.FS.Chmod(ctx, req.Paths[i], fsops.ChmodOptions{Mode: uint32(mode), Recursive: req.Recursive, Progress: p})
			}
		}

	case "extract":
		own, err := s.ownership(req.ownerFields)
		if err != nil {
			return jobs.Spec{}, err
		}
		if err := checkOwnership(own); err != nil {
			return jobs.Spec{}, err
		}
		// Selecting several volumes of one set extracts it once.
		seen := map[string]bool{}
		for _, it := range req.Items {
			info, ok := fsops.DetectArchive(path.Base(it.Src))
			if !ok {
				return invalid("%s is not a recognised archive", it.Src)
			}
			key := path.Join(path.Dir(it.Src), info.First) + "\x00" + it.Dst
			if seen[key] {
				continue
			}
			seen[key] = true
			spec.Items = append(spec.Items, it)
			dirs = append(dirs, it.Dst)
			if req.DeleteOriginals {
				dirs = append(dirs, path.Dir(it.Src))
			}
		}
		if len(spec.Items) == 0 {
			return invalid("no items")
		}
		count = len(spec.Items)
		opts := fsops.ExtractOptions{Ownership: own, Password: req.Password, DeleteSource: req.DeleteOriginals}
		step = func(ctx context.Context, p *fsops.Progress, i int) error {
			o := opts
			o.Progress = p
			_, err := s.FS.Extract(ctx, spec.Items[i].Src, spec.Items[i].Dst, o)
			return err
		}

	case "archive":
		if len(req.Paths) == 0 {
			return invalid("no paths")
		}
		if req.Dst == "" {
			return invalid("no destination")
		}
		if req.Format == fsops.Format7z && !fsops.SevenZipAvailable() ||
			req.Format != fsops.Format7z && !slices.Contains(fsops.CreateFormats, req.Format) {
			return invalid("can't create %q archives", req.Format)
		}
		own, err := s.ownership(req.ownerFields)
		if err != nil {
			return jobs.Spec{}, err
		}
		if err := checkOwnership(own); err != nil {
			return jobs.Spec{}, err
		}
		dst := fsops.CleanPath(req.Dst)
		for _, p := range req.Paths {
			spec.Items = append(spec.Items, jobs.Item{Src: p, Dst: dst})
		}
		dirs = append(dirs, path.Dir(dst))
		if req.DeleteOriginals {
			dirs = append(dirs, path.Dir(req.Paths[0]))
		}
		count = 1
		opts := fsops.CreateArchiveOptions{Ownership: own, Format: req.Format, Overwrite: req.Overwrite, DeleteSources: req.DeleteOriginals}
		step = func(ctx context.Context, p *fsops.Progress, _ int) error {
			o := opts
			o.Progress = p
			_, err := s.FS.CreateArchive(ctx, slices.Clone(req.Paths), dst, o)
			return err
		}

	default:
		return invalid("unknown job kind %q", req.Kind)
	}

	slices.Sort(dirs)
	spec.Dirs = slices.Compact(dirs)
	spec.Title = jobTitle(req.Kind, spec.Items)
	spec.Run = func(ctx context.Context, p *fsops.Progress) []error {
		for _, sp := range scan {
			if entries, bytes, err := s.FS.Usage(ctx, sp); err == nil {
				p.AddTotal(entries, bytes)
			}
		}
		var errs []error
		for i := range count {
			if err := ctx.Err(); err != nil {
				return append(errs, err)
			}
			if err := step(ctx, p, i); err != nil {
				errs = append(errs, err)
			}
		}
		return errs
	}
	return spec, nil
}

// checkOwnership validates an owner policy up front; fsops would otherwise
// only reject it once the job runs.
func checkOwnership(o fsops.Ownership) error {
	switch o.Owner {
	case "", fsops.OwnerKeep, fsops.OwnerInherit:
		return nil
	case fsops.OwnerSet:
		if o.UID == -1 && o.GID == -1 {
			return fmt.Errorf("%w: owner policy \"set\" needs a user and/or group", fsops.ErrInvalidArgument)
		}
		return nil
	}
	return fmt.Errorf("%w: unknown owner policy %q", fsops.ErrInvalidArgument, o.Owner)
}

func jobTitle(kind string, items []jobs.Item) string {
	verb := map[string]string{
		"copy": "Copy", "move": "Move", "delete": "Delete", "chown": "Change owner of",
		"chmod": "Change mode of", "extract": "Extract", "archive": "Archive",
	}[kind]
	what := path.Base(items[0].Src)
	if items[0].Src == "/" {
		what = "/"
	}
	if len(items) > 1 {
		what = fmt.Sprintf("%d items", len(items))
	}
	switch kind {
	case "archive", "extract":
		return fmt.Sprintf("%s %s → %s", verb, what, items[0].Dst)
	case "copy", "move":
		dst := path.Dir(items[0].Dst)
		if len(items) == 1 {
			dst = items[0].Dst
		}
		return fmt.Sprintf("%s %s → %s", verb, what, dst)
	}
	return verb + " " + what
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Jobs.List())
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.Jobs.Cancel(r.PathValue("id"))
	if err != nil {
		writeJobError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func (s *Server) handleDismissJob(w http.ResponseWriter, r *http.Request) {
	if err := s.Jobs.Dismiss(r.PathValue("id")); err != nil {
		writeJobError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleClearJobs(w http.ResponseWriter, r *http.Request) {
	s.Jobs.ClearFinished()
	w.WriteHeader(http.StatusNoContent)
}

func writeJobError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, jobs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, jobs.ErrState):
		writeError(w, http.StatusConflict, "invalid_state", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}
