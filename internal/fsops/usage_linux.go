//go:build linux

package fsops

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// DiskUsage scans p and reports usage per immediate child and per owner.
// It stays on p's filesystem (like du -x) and counts hard-linked files once.
func (f *FS) DiskUsage(ctx context.Context, p string, prog *Progress) (UsageReport, error) {
	p = CleanPath(p)
	rep := UsageReport{Path: p}
	fd, err := f.open(p, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return rep, err
	}
	defer unix.Close(fd)
	var root unix.Stat_t
	if err := unix.Fstat(fd, &root); err != nil {
		return rep, err
	}

	children := map[string]*UsageItem{}
	owners := map[uint32]*OwnerUsage{}
	seen := map[[2]uint64]bool{}
	err = walk(ctx, fd, p, true, func(_ int, fp string, st *unix.Stat_t) error {
		isDir := st.Mode&unix.S_IFMT == unix.S_IFDIR
		if fp != p && st.Dev != root.Dev {
			return errSkipDir // another filesystem mounted here
		}
		prog.entryDone()
		var size, disk int64
		if !isDir && st.Nlink > 1 && seen[[2]uint64{st.Dev, st.Ino}] {
			// Already counted through another hard link.
		} else {
			if !isDir && st.Nlink > 1 {
				seen[[2]uint64{st.Dev, st.Ino}] = true
			}
			if st.Mode&unix.S_IFMT == unix.S_IFREG {
				size = st.Size
			}
			disk = st.Blocks * 512
		}
		prog.addBytes(size)
		rep.Size += size
		rep.Disk += disk
		rep.Entries++

		o := owners[st.Uid]
		if o == nil {
			o = &OwnerUsage{UID: st.Uid, User: f.Owners.UserName(st.Uid)}
			owners[st.Uid] = o
		}
		o.Size += size
		o.Disk += disk
		o.Entries++

		if fp == p {
			return nil
		}
		rel := strings.TrimPrefix(strings.TrimPrefix(fp, p), "/")
		name, _, _ := strings.Cut(rel, "/")
		c := children[name]
		if c == nil {
			c = &UsageItem{Name: name, Path: path.Join(p, name)}
			children[name] = c
		}
		if rel == name {
			c.Type = fileType(st.Mode)
			prog.setCurrent(fp)
		}
		c.Size += size
		c.Disk += disk
		c.Entries++
		return nil
	})
	if err != nil {
		return rep, err
	}
	for _, c := range children {
		rep.Children = append(rep.Children, *c)
	}
	sort.Slice(rep.Children, func(i, j int) bool { return rep.Children[i].Disk > rep.Children[j].Disk })
	for _, o := range owners {
		rep.ByOwner = append(rep.ByOwner, *o)
	}
	sort.Slice(rep.ByOwner, func(i, j int) bool { return rep.ByOwner[i].Disk > rep.ByOwner[j].Disk })
	return rep, nil
}

var errStopWalk = errors.New("stop walk")

// Find walks root (not following symlinks, and by default not leaving its
// filesystem) and returns entries matching q.
func (f *FS) Find(ctx context.Context, root string, q FindQuery, prog *Progress) (FindResult, error) {
	root = CleanPath(root)
	res := FindResult{Root: root, Entries: []Entry{}}
	if err := q.Validate(); err != nil {
		return res, err
	}
	if q.Limit <= 0 || q.Limit > MaxFindResults {
		q.Limit = MaxFindResults
	}
	pattern := strings.ToLower(q.Name)
	substring := pattern != "" && !strings.ContainsAny(pattern, "*?[")

	fd, err := f.open(root, unix.O_PATH|unix.O_NOFOLLOW)
	if err != nil {
		return res, err
	}
	defer unix.Close(fd)
	var rst unix.Stat_t
	if err := unix.Fstat(fd, &rst); err != nil {
		return res, err
	}

	match := func(fp string, st *unix.Stat_t) bool {
		mode := st.Mode & unix.S_IFMT
		if pattern != "" {
			base := strings.ToLower(path.Base(fp))
			if substring && !strings.Contains(base, pattern) {
				return false
			}
			if !substring {
				if ok, _ := path.Match(pattern, base); !ok {
					return false
				}
			}
		}
		if q.Type != "" && fileType(st.Mode) != q.Type {
			return false
		}
		if q.UID != nil && st.Uid != *q.UID || q.GID != nil && st.Gid != *q.GID {
			return false
		}
		if q.MinSize != nil || q.MaxSize != nil {
			// Size filters apply to regular files only.
			if mode != unix.S_IFREG || q.MinSize != nil && st.Size < *q.MinSize || q.MaxSize != nil && st.Size > *q.MaxSize {
				return false
			}
		}
		mtime := time.Unix(st.Mtim.Unix())
		if q.ModifiedAfter != nil && !mtime.After(*q.ModifiedAfter) ||
			q.ModifiedBefore != nil && !mtime.Before(*q.ModifiedBefore) {
			return false
		}
		if q.Orphaned && f.Owners.UserName(st.Uid) != "" && f.Owners.GroupName(st.Gid) != "" {
			return false
		}
		if q.ForeignOwner && st.Uid == rst.Uid {
			return false
		}
		if q.WorldWritable && (mode == unix.S_IFLNK || st.Mode&0o002 == 0) {
			return false
		}
		if q.Setuid && st.Mode&(unix.S_ISUID|unix.S_ISGID) == 0 {
			return false
		}
		return true
	}

	err = walk(ctx, fd, root, true, func(efd int, fp string, st *unix.Stat_t) error {
		if fp == root {
			return nil
		}
		res.Scanned++
		prog.entryDone()
		if res.Scanned%256 == 0 {
			prog.setCurrent(fp)
		}
		if match(fp, st) {
			if len(res.Entries) >= q.Limit {
				res.Truncated = true
				return errStopWalk
			}
			e, err := f.entryAt(efd, "", fp)
			if err == nil {
				res.Entries = append(res.Entries, e)
			}
		}
		if !q.CrossMounts && st.Mode&unix.S_IFMT == unix.S_IFDIR && st.Dev != rst.Dev {
			return errSkipDir
		}
		return nil
	})
	if err == errStopWalk {
		err = nil
	}
	return res, err
}
