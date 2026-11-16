//go:build linux

package api

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestSQLiteTool(t *testing.T) {
	ts, dir := newTestServer(t)
	os.WriteFile(filepath.Join(dir, "app.db"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("not a database, just text that is long enough to have a header"), 0o644)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	call := func(action, body string, out any) int {
		return doJSON(t, "POST", ts.URL+"/api/v1/tools/sqlite/"+action, body, out)
	}

	var info infoResponse
	doJSON(t, "GET", ts.URL+"/api/v1/info", "", &info)
	if len(info.Tools) == 0 || info.Tools[0].ID != "sqlite" || len(info.Tools[0].Extensions) == 0 {
		t.Errorf("info tools %+v", info.Tools)
	}

	var res struct {
		Changes int64 `json:"changes"`
	}
	if code := call("query", `{"path": "/app.db", "args": {"sql": "CREATE TABLE t (id INTEGER PRIMARY KEY, name TEXT); INSERT INTO t (name) VALUES ('a'), ('b')"}}`, &res); code != 200 || res.Changes != 2 {
		t.Fatalf("query: %d %+v", code, res)
	}
	var objs []map[string]any
	if code := call("objects", `{"path": "/app.db"}`, &objs); code != 200 || len(objs) == 0 || objs[0]["name"] != "t" {
		t.Errorf("objects: %d %v", code, objs)
	}
	if code := call("update", `{"path": "/app.db", "args": {"table": "t", "key": [2], "column": "name", "value": "B"}}`, nil); code != 200 {
		t.Errorf("update: %d", code)
	}
	var ins map[string]int64
	if code := call("insert", `{"path": "/app.db", "args": {"table": "t", "values": {"name": "c"}}}`, &ins); code != 200 || ins["rowid"] != 3 {
		t.Errorf("insert: %d %v", code, ins)
	}
	var del map[string]int64
	if code := call("delete", `{"path": "/app.db", "args": {"table": "t", "keys": [[1]]}}`, &del); code != 200 || del["deleted"] != 1 {
		t.Errorf("delete: %d %v", code, del)
	}
	var page struct {
		Rows []struct {
			Values []json.RawMessage `json:"values"`
		} `json:"rows"`
		Total int64 `json:"total"`
	}
	if code := call("rows", `{"path": "/app.db", "args": {"table": "t", "order": "name"}}`, &page); code != 200 || page.Total != 2 || string(page.Rows[0].Values[1]) != `"B"` {
		t.Errorf("rows: %d %+v", code, page)
	}

	var eb errorBody
	for _, c := range []struct {
		action, body string
		status       int
		code         string
	}{
		{"query", `{"path": "/app.db", "args": {"sql": "SELEC 1"}}`, 400, "tool_error"},
		{"rows", `{"path": "/app.db", "args": {"table": "nope"}}`, 400, "tool_error"},
		{"rows", `{"path": "/app.db", "args": {"bogus": 1}}`, 400, "tool_error"},
		{"objects", `{"path": "/notes.txt"}`, 400, "tool_error"},
		{"objects", `{"path": "/missing.db"}`, 404, "not_found"},
		{"objects", `{"path": "/sub"}`, 400, "invalid"},
		{"explode", `{"path": "/app.db"}`, 404, "not_found"},
	} {
		eb = errorBody{}
		if code := call(c.action, c.body, &eb); code != c.status || eb.Error.Code != c.code {
			t.Errorf("%s %s: %d %+v", c.action, c.body, code, eb)
		}
	}
	if code := doJSON(t, "POST", ts.URL+"/api/v1/tools/nope/x", `{"path": "/app.db"}`, &eb); code != 404 {
		t.Errorf("unknown tool: %d", code)
	}
}

func TestLogTool(t *testing.T) {
	ts, dir := newTestServer(t)
	p := filepath.Join(dir, "app.log")
	os.WriteFile(p, []byte("one\nERROR two\nthree\n"), 0o644)
	call := func(action, body string, out any) int {
		return doJSON(t, "POST", ts.URL+"/api/v1/tools/log/"+action, body, out)
	}
	type page struct {
		Lines []struct {
			O int64  `json:"o"`
			T string `json:"t"`
			L string `json:"l"`
		} `json:"lines"`
		Start, End, Size int64
		Inode            uint64
		Reset            bool
	}
	var pg page
	if code := call("tail", `{"path": "/app.log", "args": {"lines": 2}}`, &pg); code != 200 || len(pg.Lines) != 2 || pg.Lines[0].T != "ERROR two" || pg.Lines[0].L != "error" || pg.End != 20 || pg.Start != 4 || pg.Inode == 0 {
		t.Fatalf("tail: %d %+v", code, pg)
	}
	inode := pg.Inode
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("four\nfi")
	f.Close()
	pg = page{}
	body := fmt.Sprintf(`{"path": "/app.log", "args": {"after": 20, "inode": %d}}`, inode)
	if code := call("follow", body, &pg); code != 200 || len(pg.Lines) != 2 || pg.Lines[1].T != "fi" || pg.End != 25 || pg.Reset {
		t.Errorf("follow: %d %+v", code, pg)
	}
	// Truncated: the client must start over.
	os.WriteFile(p, []byte("new\n"), 0o644)
	pg = page{}
	if code := call("follow", body, &pg); code != 200 || !pg.Reset {
		t.Errorf("follow after truncate: %d %+v", code, pg)
	}
	// Rotated: a new file under the same name.
	os.Rename(p, p+".1")
	os.WriteFile(p, []byte("x\n"), 0o644)
	pg = page{}
	if code := call("follow", fmt.Sprintf(`{"path": "/app.log", "args": {"after": 0, "inode": %d}}`, inode), &pg); code != 200 || !pg.Reset {
		t.Errorf("follow after rotate: %d %+v", code, pg)
	}
	var eb errorBody
	if code := call("tail", `{"path": "/app.log", "args": {"filter": {"text": "(", "regex": true}}}`, &eb); code != 400 || eb.Error.Code != "tool_error" {
		t.Errorf("bad regex: %d %+v", code, eb)
	}
}
