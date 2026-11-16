package fsops

import (
	"bytes"
	"io"
	"strings"
	"unicode/utf8"
)

// binarySniff is how much of a file is checked for NUL bytes, like git.
const binarySniff = 8000

// maxSnippet caps the length of ContentMatch.Text.
const maxSnippet = 160

// contentSearcher looks for one piece of text in files, reusing its buffer.
type contentSearcher struct {
	needle []byte // lower-cased
	buf    []byte
}

func newContentSearcher(text string) *contentSearcher {
	return &contentSearcher{needle: bytes.ToLower([]byte(text))}
}

// search reads up to size bytes of r and returns the first line containing
// the needle (case-insensitive). binary reports a file that looks like
// binary data (a NUL byte near the start), which isn't searched.
func (c *contentSearcher) search(r io.Reader, size int64) (m ContentMatch, found, binary bool, err error) {
	if int64(cap(c.buf)) < size {
		c.buf = make([]byte, size)
	}
	n, err := io.ReadFull(r, c.buf[:size])
	if err == io.ErrUnexpectedEOF || err == io.EOF {
		err = nil // the file shrank since it was stat'ed
	}
	if err != nil {
		return m, false, false, err
	}
	data := c.buf[:n]
	if bytes.IndexByte(data[:min(n, binarySniff)], 0) >= 0 {
		return m, false, true, nil
	}
	for line := 1; len(data) > 0; line++ {
		end := bytes.IndexByte(data, '\n')
		if end < 0 {
			end = len(data)
		}
		raw := data[:end]
		data = data[min(end+1, len(data)):]
		lower := bytes.ToLower(raw)
		i := bytes.Index(lower, c.needle)
		if i < 0 {
			continue
		}
		if len(lower) != len(raw) {
			i = 0 // case folding changed byte offsets; show the line start
		}
		return ContentMatch{Line: line, Text: snippet(raw, i, len(c.needle))}, true, false, nil
	}
	return m, false, false, nil
}

// snippet shortens line to about maxSnippet bytes around the match at
// [i, i+n), on rune boundaries, as valid UTF-8.
func snippet(line []byte, i, n int) string {
	line = bytes.TrimRight(line, "\r")
	start, end := 0, len(line)
	if end > maxSnippet {
		start = max(0, i-(maxSnippet-n)/3)
		end = min(len(line), start+maxSnippet)
		start = max(0, min(start, end-maxSnippet))
		for start > 0 && start < len(line) && !utf8.RuneStart(line[start]) {
			start--
		}
		for end < len(line) && !utf8.RuneStart(line[end]) {
			end++
		}
	}
	s := strings.ToValidUTF8(string(line[start:end]), "�")
	s = strings.TrimSpace(strings.ReplaceAll(s, "\t", " "))
	if start > 0 {
		s = "…" + s
	}
	if end < len(line) {
		s += "…"
	}
	return s
}
