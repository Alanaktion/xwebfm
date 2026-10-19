package fsops

import (
	"bufio"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Account is a user or group from the system databases.
type Account struct {
	ID   uint32 `json:"id"`
	Name string `json:"name"`
	Home string `json:"home,omitempty"` // users only
}

// Owners resolves UIDs and GIDs to names by reading passwd and group files
// directly (no cgo / NSS), reloading them whenever their mtime changes.
type Owners struct {
	PasswdPath string
	GroupPath  string

	mu     sync.Mutex
	users  table
	groups table
}

type table struct {
	mtime  time.Time
	byID   map[uint32]string
	byName map[string]uint32
	homes  map[uint32]string
}

// NewOwners returns an Owners reading the standard /etc files.
func NewOwners() *Owners {
	return &Owners{PasswdPath: "/etc/passwd", GroupPath: "/etc/group"}
}

// UserName returns the name for uid, or "" if unknown.
func (o *Owners) UserName(uid uint32) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.load(&o.users, o.PasswdPath).byID[uid]
}

// GroupName returns the name for gid, or "" if unknown.
func (o *Owners) GroupName(gid uint32) string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.load(&o.groups, o.GroupPath).byID[gid]
}

// LookupUser returns the UID for a user name.
func (o *Owners) LookupUser(name string) (uint32, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id, ok := o.load(&o.users, o.PasswdPath).byName[name]
	return id, ok
}

// LookupGroup returns the GID for a group name.
func (o *Owners) LookupGroup(name string) (uint32, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	id, ok := o.load(&o.groups, o.GroupPath).byName[name]
	return id, ok
}

// Users lists all known users sorted by ID.
func (o *Owners) Users() []Account {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.load(&o.users, o.PasswdPath).list()
}

// Groups lists all known groups sorted by ID.
func (o *Owners) Groups() []Account {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.load(&o.groups, o.GroupPath).list()
}

func (o *Owners) load(t *table, file string) *table {
	st, err := os.Stat(file)
	if err != nil {
		if t.byID == nil {
			t.byID, t.byName, t.homes = map[uint32]string{}, map[string]uint32{}, map[uint32]string{}
		}
		return t
	}
	if t.byID != nil && st.ModTime().Equal(t.mtime) {
		return t
	}
	byID, byName, homes := parseIDFile(file)
	t.mtime, t.byID, t.byName, t.homes = st.ModTime(), byID, byName, homes
	return t
}

func (t *table) list() []Account {
	out := make([]Account, 0, len(t.byID))
	for id, name := range t.byID {
		out = append(out, Account{ID: id, Name: name, Home: t.homes[id]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// parseIDFile parses passwd/group style files: name:x:id:...; for passwd
// it also returns home directories (field 6).
func parseIDFile(file string) (map[uint32]string, map[string]uint32, map[uint32]string) {
	byID, byName, homes := map[uint32]string{}, map[string]uint32{}, map[uint32]string{}
	f, err := os.Open(file)
	if err != nil {
		return byID, byName, homes
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, ":", 7)
		if len(fields) < 3 {
			continue
		}
		id, err := strconv.ParseUint(fields[2], 10, 32)
		if err != nil {
			continue
		}
		// First entry wins for duplicate IDs, matching getpwuid behaviour.
		if _, ok := byID[uint32(id)]; !ok {
			byID[uint32(id)] = fields[0]
			if len(fields) == 7 && fields[5] != "" {
				homes[uint32(id)] = fields[5]
			}
		}
		byName[fields[0]] = uint32(id)
	}
	return byID, byName, homes
}
