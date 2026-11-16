package logview

import (
	"fmt"
	"strings"
	"testing"
)

func texts(p *Page) []string {
	var out []string
	for _, l := range p.Lines {
		out = append(out, l.Text)
	}
	return out
}

func eq(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s: got %q, want %q", what, got, want)
	}
}

func TestBeforeAndAfter(t *testing.T) {
	src := "a\nINFO b\r\n\nERROR c\nd"
	r := strings.NewReader(src)
	size := int64(len(src))

	p, err := Before(r, size, size, 2, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "tail", texts(p), []string{"ERROR c", "d"})
	if !p.Lines[1].Partial || p.End != int64(strings.Index(src, "d")) || p.Start != int64(strings.Index(src, "ERROR")) {
		t.Errorf("tail page %+v", p)
	}
	if p.Lines[0].Level != "error" {
		t.Errorf("level %q", p.Lines[0].Level)
	}
	p2, _ := Before(r, size, p.Start, 10, Filter{})
	eq(t, "earlier", texts(p2), []string{"a", "INFO b", ""})
	if p2.Start != 0 || p2.Lines[1].Offset != 2 {
		t.Errorf("earlier page %+v", p2)
	}

	f, _ := After(r, size, 0, 10, Filter{})
	eq(t, "after", texts(f), []string{"a", "INFO b", "", "ERROR c", "d"})
	if f.End != p.End || f.More || !f.Lines[4].Partial {
		t.Errorf("after page %+v", f)
	}
	f, _ = After(r, size, 0, 2, Filter{})
	if !f.More || f.End != int64(strings.Index(src, "\n\n")+1) {
		t.Errorf("limited after %+v", f)
	}

	// Ends with a newline: nothing partial, End is the size.
	src2 := "x\ny\n"
	p, _ = Before(strings.NewReader(src2), 4, 4, 10, Filter{})
	eq(t, "newline end", texts(p), []string{"x", "y"})
	if p.End != 4 || p.Start != 0 {
		t.Errorf("newline end page %+v", p)
	}
	f, _ = After(strings.NewReader(src2), 4, 4, 10, Filter{})
	if len(f.Lines) != 0 || f.End != 4 {
		t.Errorf("after at end %+v", f)
	}
}

func TestFilter(t *testing.T) {
	var b strings.Builder
	for i := range 20000 {
		lvl := []string{"DEBUG", "INFO", "WARN", "ERROR"}[i%4]
		fmt.Fprintf(&b, "2026-01-01 %s request %d\n", lvl, i)
	}
	src := b.String()
	r := strings.NewReader(src)
	size := int64(len(src))
	p, err := Before(r, size, size, 3, Filter{Level: "warn"})
	if err != nil {
		t.Fatal(err)
	}
	eq(t, "warn", texts(p), []string{"2026-01-01 ERROR request 19995", "2026-01-01 WARN request 19998", "2026-01-01 ERROR request 19999"})
	p, _ = Before(r, size, size, 5, Filter{Text: `request 1\d$`, Regex: true})
	if len(p.Lines) != 5 || p.Lines[4].Text != "2026-01-01 ERROR request 19" {
		t.Errorf("regex %q", texts(p))
	}
	p, _ = After(r, size, 0, 2, Filter{Text: "error request 7", Case: false})
	eq(t, "fold", texts(p), []string{"2026-01-01 ERROR request 7", "2026-01-01 ERROR request 71"})
	p, _ = After(r, size, 0, 1, Filter{Text: "DEBUG", Not: true})
	eq(t, "not", texts(p), []string{"2026-01-01 INFO request 1"})
	if _, err := Before(r, size, size, 1, Filter{Text: "(", Regex: true}); err == nil {
		t.Error("bad regex accepted")
	}
	// Walking back page by page covers every line exactly once.
	n, before := 0, size
	for before > 0 {
		p, _ := Before(r, size, before, 777, Filter{})
		n += len(p.Lines)
		before = p.Start
	}
	if n != 20000 {
		t.Errorf("paged %d lines", n)
	}
}

func TestLongLine(t *testing.T) {
	src := "start\n" + strings.Repeat("x", 200000) + "\nend\n"
	r := strings.NewReader(src)
	size := int64(len(src))
	p, _ := Before(r, size, size, 10, Filter{})
	if len(p.Lines) != 3 || !p.Lines[1].Cut || len(p.Lines[1].Text) != MaxLineBytes || p.Lines[1].Offset != 6 {
		t.Errorf("long line before: %d lines", len(p.Lines))
	}
	f, _ := After(r, size, 0, 10, Filter{})
	if len(f.Lines) != 3 || !f.Lines[1].Cut || f.Lines[2].Text != "end" || f.End != size {
		t.Errorf("long line after: %+v", len(f.Lines))
	}
}

func TestLevel(t *testing.T) {
	for in, want := range map[string]string{
		"level=error msg=x":         "error",
		"[WARNING] disk":            "warn",
		"2026 FATAL boom":           "error",
		"I am fine":                 "",
		"Nov 16 sshd[1]: notice ok": "info",
		"terror":                    "",
		"  DEBUG x":                 "debug",
	} {
		if got := Level(in); got != want {
			t.Errorf("Level(%q) = %q, want %q", in, got, want)
		}
	}
}
