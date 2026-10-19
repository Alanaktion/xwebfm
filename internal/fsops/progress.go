package fsops

import "sync/atomic"

// Progress collects counters from a long-running operation so another
// goroutine can report them. All methods are safe on a nil *Progress, which
// disables tracking. Entries count every file, directory and symlink
// processed; bytes count regular-file data.
type Progress struct {
	totalEntries, totalBytes atomic.Int64
	entries, bytes           atomic.Int64
	current                  atomic.Pointer[string]
}

// ProgressSnapshot is a point-in-time copy of a Progress.
type ProgressSnapshot struct {
	TotalEntries int64  `json:"totalEntries"`
	TotalBytes   int64  `json:"totalBytes"`
	Entries      int64  `json:"entries"`
	Bytes        int64  `json:"bytes"`
	Current      string `json:"current,omitempty"`
}

// AddTotal adds to the expected totals, typically from Usage.
func (p *Progress) AddTotal(entries, bytes int64) {
	if p == nil {
		return
	}
	p.totalEntries.Add(entries)
	p.totalBytes.Add(bytes)
}

// Snapshot returns the current counters.
func (p *Progress) Snapshot() ProgressSnapshot {
	if p == nil {
		return ProgressSnapshot{}
	}
	s := ProgressSnapshot{
		TotalEntries: p.totalEntries.Load(),
		TotalBytes:   p.totalBytes.Load(),
		Entries:      p.entries.Load(),
		Bytes:        p.bytes.Load(),
	}
	if c := p.current.Load(); c != nil {
		s.Current = *c
	}
	return s
}

func (p *Progress) setCurrent(path string) {
	if p != nil {
		p.current.Store(&path)
	}
}

func (p *Progress) entryDone() {
	if p != nil {
		p.entries.Add(1)
	}
}

func (p *Progress) addBytes(n int64) {
	if p != nil {
		p.bytes.Add(n)
	}
}
