// Package logview reads pages of lines from log files for the log file
// tool: the last lines before an offset (tail and "load earlier"), and the
// lines after an offset (follow), optionally filtered by text, regular
// expression or level.
//
// Offsets are byte offsets in the file. A page reports where it started
// and where its last complete line ended, so the next page continues
// exactly there. A final line without a newline is returned with Partial
// set but not counted in End, so following the file returns it again,
// complete, once the writer finishes it.
package logview

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
)

const (
	// DefaultLines and MaxLines bound the lines in a page.
	DefaultLines = 500
	MaxLines     = 5000
	// MaxLineBytes is the most of a line returned; longer lines are cut.
	MaxLineBytes = 16 << 10
	// ScanBudget is how much of the file one read examines at most, so a
	// filter that rarely matches doesn't scan a huge file in one request.
	ScanBudget = 32 << 20

	chunk = 64 << 10
)

// Levels, most severe first. A line's level is the first of these words
// (or a common spelling of one) found near its start.
var Levels = []string{"error", "warn", "info", "debug", "trace"}

var levelRe = regexp.MustCompile(`(?i)\b(fatal|panic|crit(?:ical)?|emerg(?:ency)?|alert|err(?:or)?|warn(?:ing)?|notice|info|debug|trace)\b`)

// levelScan is how far into a line the level is looked for.
const levelScan = 160

// Level returns the line's level ("error", "warn", …) or "".
func Level(line string) string {
	if len(line) > levelScan {
		line = line[:levelScan]
	}
	m := levelRe.FindStringSubmatch(line)
	if m == nil {
		return ""
	}
	switch w := strings.ToLower(m[1]); {
	case w == "fatal" || w == "panic" || strings.HasPrefix(w, "crit") || strings.HasPrefix(w, "emerg") || w == "alert" || strings.HasPrefix(w, "err"):
		return "error"
	case strings.HasPrefix(w, "warn"):
		return "warn"
	case w == "notice" || w == "info":
		return "info"
	default:
		return w
	}
}

func levelRank(l string) int {
	for i, v := range Levels {
		if v == l {
			return i
		}
	}
	return len(Levels)
}

// Filter selects lines. The zero Filter matches every line.
type Filter struct {
	Text  string `json:"text"`
	Regex bool   `json:"regex"`
	Case  bool   `json:"case"`  // case-sensitive
	Not   bool   `json:"not"`   // keep lines that don't match Text
	Level string `json:"level"` // keep lines at this level or more severe
}

// ErrBadFilter wraps an invalid filter (a bad regular expression or level).
var ErrBadFilter = errors.New("invalid filter")

type matcher struct {
	re    *regexp.Regexp
	text  string
	fold  bool
	not   bool
	level int // -1: any
}

func (f Filter) compile() (*matcher, error) {
	m := &matcher{not: f.Not, level: -1}
	if f.Level != "" {
		if m.level = levelRank(f.Level); m.level == len(Levels) {
			return nil, fmt.Errorf("%w: unknown level %q", ErrBadFilter, f.Level)
		}
	}
	switch {
	case f.Text == "":
	case f.Regex:
		src := f.Text
		if !f.Case {
			src = "(?i)" + src
		}
		re, err := regexp.Compile(src)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrBadFilter, err)
		}
		m.re = re
	case f.Case:
		m.text = f.Text
	default:
		m.text, m.fold = strings.ToLower(f.Text), true
	}
	return m, nil
}

func (m *matcher) match(l *Line) bool {
	if m.level >= 0 && (l.Level == "" || levelRank(l.Level) > m.level) {
		return false
	}
	var hit bool
	switch {
	case m.re != nil:
		hit = m.re.MatchString(l.Text)
	case m.text == "":
		return true
	case m.fold:
		hit = strings.Contains(strings.ToLower(l.Text), m.text)
	default:
		hit = strings.Contains(l.Text, m.text)
	}
	return hit != m.not
}

// Line is one line of the file, without its line ending.
type Line struct {
	Offset int64  `json:"o"`
	Text   string `json:"t"`
	Level  string `json:"l,omitempty"`
	// Cut is set when the line was longer than MaxLineBytes.
	Cut bool `json:"cut,omitempty"`
	// Partial is set on a last line that doesn't end with a newline yet.
	Partial bool `json:"partial,omitempty"`
}

// Page is a run of lines read from the file.
type Page struct {
	// Lines are the matching lines, in file order.
	Lines []Line `json:"lines"`
	// Start is the offset of the first line examined: pass it as Before to
	// read earlier lines. 0 means the start of the file was reached.
	Start int64 `json:"start"`
	// End is the offset just past the last complete line examined: pass it
	// as After to read later lines.
	End int64 `json:"end"`
	// More is set when a forward read stopped before the end of the file.
	More bool `json:"more,omitempty"`
}

func newLine(off int64, b []byte, partial bool) Line {
	b = bytes.TrimSuffix(b, []byte{'\r'})
	l := Line{Offset: off, Partial: partial}
	if len(b) > MaxLineBytes {
		b = b[:MaxLineBytes]
		l.Cut = true
	}
	l.Text = strings.ToValidUTF8(string(b), "�")
	l.Level = Level(l.Text)
	return l
}

func clampLines(n int) int {
	if n <= 0 {
		return DefaultLines
	}
	return min(n, MaxLines)
}

// Before reads up to n matching lines that end before offset before in r,
// whose size is size. before should be the start of a line (the Start of
// an earlier page) or the size of the file.
func Before(r io.ReaderAt, size, before int64, n int, f Filter) (*Page, error) {
	m, err := f.compile()
	if err != nil {
		return nil, err
	}
	n = clampLines(n)
	before = max(0, min(before, size))
	p := &Page{Start: before, End: before}
	if before == 0 {
		return p, nil
	}
	// end is where the line being assembled ends, before its newline.
	end := before - 1
	partial := false
	if before == size {
		var last [1]byte
		if _, err := r.ReadAt(last[:], size-1); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		if partial = last[0] != '\n'; partial {
			end = size
		}
	}
	var rev []Line // matches, last first
	buf := make([]byte, chunk)
	// tail is the part of the current line after pos, cut to its first
	// MaxLineBytes+4 bytes.
	var tail []byte
	pos := end
	emitted := false
	emit := func(start int64, b []byte) bool {
		l := newLine(start, b, partial)
		if partial {
			p.End = start
			partial = false
		}
		p.Start, emitted = start, true
		if m.match(&l) {
			rev = append(rev, l)
		}
		return len(rev) >= n
	}
	full := false
	for scanned := int64(0); pos > 0 && scanned < ScanBudget && !full; {
		k := min(int64(chunk), pos)
		data := buf[:k]
		if _, err := r.ReadAt(data, pos-k); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		pos -= k
		scanned += k
		for !full {
			i := bytes.LastIndexByte(data, '\n')
			if i < 0 {
				break
			}
			line := data[i+1:]
			if len(tail) > 0 {
				line = append(append([]byte{}, line...), tail...)
				tail = tail[:0]
			}
			full = emit(pos+int64(i)+1, line)
			data = data[:i]
		}
		if full {
			break
		}
		t := append(append([]byte{}, data...), tail...)
		tail = t[:min(len(t), MaxLineBytes+4)]
		if pos == 0 {
			emit(0, tail)
		}
	}
	if !emitted {
		// One enormous line used up the scan: continue from where it stopped.
		p.Start = pos
	}
	if partial {
		p.End = size
	}
	p.Lines = make([]Line, len(rev))
	for i, l := range rev {
		p.Lines[len(rev)-1-i] = l
	}
	return p, nil
}

// After reads up to n matching lines starting at offset after, which
// should be the start of a line.
func After(r io.ReaderAt, size, after int64, n int, f Filter) (*Page, error) {
	m, err := f.compile()
	if err != nil {
		return nil, err
	}
	n = clampLines(n)
	after = max(0, min(after, size))
	p := &Page{Lines: []Line{}, Start: after, End: after}
	sr := io.NewSectionReader(r, after, size-after)
	buf := make([]byte, chunk)
	var line []byte
	lineStart, pos := after, after
	scanned := int64(0)
	for len(p.Lines) < n {
		if scanned >= ScanBudget {
			p.More = true
			break
		}
		k, err := sr.Read(buf)
		data := buf[:k]
		for len(data) > 0 && len(p.Lines) < n {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				if len(line) <= MaxLineBytes+4 {
					line = append(line, data[:min(len(data), MaxLineBytes+4-len(line))]...)
				}
				pos += int64(len(data))
				break
			}
			if len(line) <= MaxLineBytes+4 {
				line = append(line, data[:min(i, MaxLineBytes+4-len(line))]...)
			}
			pos += int64(i) + 1
			data = data[i+1:]
			l := newLine(lineStart, line, false)
			if m.match(&l) {
				p.Lines = append(p.Lines, l)
			}
			line = line[:0]
			lineStart = pos
			p.End = pos
		}
		scanned += int64(k)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	if len(p.Lines) >= n && p.End < size {
		p.More = true
	} else if !p.More && lineStart < size && pos == size {
		l := newLine(lineStart, line, true)
		if m.match(&l) {
			p.Lines = append(p.Lines, l)
		}
	}
	return p, nil
}
