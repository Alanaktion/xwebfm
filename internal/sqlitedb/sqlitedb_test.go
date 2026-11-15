//go:build linux

package sqlitedb

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alanaktion/xwebfm/internal/fsops"
)

// open creates name in a fresh root from setup SQL and opens it through a
// pinned directory, as the API does.
func open(t *testing.T, setup string, readOnly bool) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	f, err := fsops.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	os.WriteFile(filepath.Join(dir, "test.db"), nil, 0o644)
	if setup != "" {
		db := pin(t, f, "/test.db", false)
		if _, err := db.Exec(setup, 0); err != nil {
			t.Fatal(err)
		}
		db.Close()
	}
	return pin(t, f, "/test.db", readOnly), dir
}

func pin(t *testing.T, f *fsops.FS, p string, readOnly bool) *DB {
	t.Helper()
	pf, err := f.PinFile(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pf.Close() })
	db, err := Open(context.Background(), pf.DirPath, pf.Name, readOnly)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

const schema = `
CREATE TABLE people (id INTEGER PRIMARY KEY, name TEXT NOT NULL, age INT DEFAULT 30, photo BLOB);
INSERT INTO people (name, age, photo) VALUES ('Ann', 41, x'00ff'), ('Bob', NULL, NULL), ('Cy', 9007199254740993, NULL);
CREATE TABLE kv (k TEXT, n INT, v, PRIMARY KEY (n, k)) WITHOUT ROWID;
INSERT INTO kv VALUES ('a', 1, 'one'), ('b', 2, 'two');
CREATE TABLE odd (rowid TEXT, _rowid_ TEXT, oid TEXT);
CREATE VIEW adults AS SELECT name FROM people WHERE age >= 18;
`

func TestObjectsAndRead(t *testing.T) {
	db, _ := open(t, schema, true)
	objs, err := db.Objects()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, o := range objs {
		names = append(names, o.Name+":"+o.Type)
	}
	if got := strings.Join(names, ","); !strings.HasPrefix(got, "adults:view,kv:table,odd:table,people:table,") {
		t.Errorf("objects %s", got)
	}

	p, err := db.Read("people", ReadOptions{Order: "name", Desc: true, Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || len(p.Rows) != 2 || len(p.Key) != 1 || p.Key[0] != "rowid" {
		t.Fatalf("page %+v", p)
	}
	b, _ := json.Marshal(p.Rows)
	want := `[{"key":[3],"values":[3,"Cy",{"int":"9007199254740993"},null]},{"key":[2],"values":[2,"Bob",null,null]}]`
	if string(b) != want {
		t.Errorf("rows\n got %s\nwant %s", b, want)
	}

	p, err = db.Read("kv", ReadOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Key, ",") != "n,k" {
		t.Errorf("WITHOUT ROWID key %v", p.Key)
	}
	if p, _ := db.Read("odd", ReadOptions{}); len(p.Key) != 0 {
		t.Errorf("shadowed rowid should be read-only, key %v", p.Key)
	}
	if p, _ := db.Read("adults", ReadOptions{}); len(p.Key) != 0 || len(p.Rows) != 2 {
		t.Errorf("view %+v", p)
	}
	if _, err := db.Read("nope", ReadOptions{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing table: %v", err)
	}
	if _, err := db.Read("people", ReadOptions{Order: "x; DROP TABLE people"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("bad order column: %v", err)
	}
	// Read-only connections can't write.
	if _, err := db.Exec("DELETE FROM people", 0); err == nil {
		t.Error("read-only connection wrote")
	}
}

func TestEdit(t *testing.T) {
	db, _ := open(t, schema, false)
	var key []Value
	json.Unmarshal([]byte(`[2]`), &key)
	if err := db.Update("people", key, "age", Value{V: "52"}); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec("SELECT age, typeof(age) FROM people WHERE id = 2", 0)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res.Rows); string(b) != `[[52,"integer"]]` {
		t.Errorf("after update %s", b)
	}
	if err := db.Update("people", []Value{{V: int64(99)}}, "age", Value{}); !errors.Is(err, ErrInvalid) {
		t.Errorf("missing row: %v", err)
	}
	if err := db.Update("adults", []Value{{V: int64(1)}}, "name", Value{V: "x"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("view update: %v", err)
	}
	var kvKey []Value
	json.Unmarshal([]byte(`[1, "a"]`), &kvKey)
	if err := db.Update("kv", kvKey, "v", Value{V: []byte{1, 2}}); err != nil {
		t.Fatal(err)
	}

	id, err := db.Insert("people", map[string]Value{"name": {V: "Dee"}})
	if err != nil || id != 4 {
		t.Fatalf("insert: %d %v", id, err)
	}
	res, _ = db.Exec("SELECT age FROM people WHERE id = 4", 0)
	if b, _ := json.Marshal(res.Rows); string(b) != `[[30]]` {
		t.Errorf("default not applied: %s", b)
	}
	if _, err := db.Insert("people", map[string]Value{"age": {V: int64(1)}}); err == nil {
		t.Error("NOT NULL constraint not enforced")
	} else if strings.HasPrefix(err.Error(), "sqlite3:") {
		t.Errorf("error keeps driver prefix: %v", err)
	}

	n, err := db.Delete("people", [][]Value{{{V: int64(1)}}, {{V: int64(4)}}, {{V: int64(77)}}})
	if err != nil || n != 2 {
		t.Fatalf("delete: %d %v", n, err)
	}
}

func TestExec(t *testing.T) {
	db, dir := open(t, schema, false)
	res, err := db.Exec(`
		-- comments and several statements
		UPDATE people SET age = age + 1 WHERE age IS NOT NULL;
		SELECT name FROM people ORDER BY id;
		SELECT 1 AS one; -- trailing comment`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Statements != 3 || res.Changes != 2 || strings.Join(res.Columns, ",") != "one" || len(res.Rows) != 1 {
		t.Errorf("result %+v", res)
	}
	res, err = db.Exec("SELECT value FROM generate_series(1, 50)", 10)
	if err != nil || len(res.Rows) != 10 || !res.Truncated {
		t.Errorf("truncation: %+v %v", res, err)
	}
	if _, err := db.Exec("SELECT 1; SELECT nope", 0); err == nil || !strings.Contains(err.Error(), "statement 2") {
		t.Errorf("error in second statement: %v", err)
	}
	res, err = db.Exec("BEGIN; DELETE FROM people", 0)
	if err != nil || res.Note == "" {
		t.Errorf("open transaction: %+v %v", res, err)
	}
	if res, _ := db.Exec("SELECT count(*) FROM people", 0); res.Rows[0][0].V != int64(3) {
		t.Errorf("open transaction wasn't rolled back")
	}
	if _, err := db.Exec("VACUUM", 0); err != nil {
		t.Errorf("vacuum: %v", err)
	}

	// Nothing outside the database's directory can be attached or written.
	outside := t.TempDir()
	for _, q := range []string{
		"ATTACH '" + filepath.Join(outside, "x.db") + "' AS x",
		"ATTACH 'x.db' AS x",
		"VACUUM INTO '" + filepath.Join(outside, "copy.db") + "'",
		"ATTACH '/proc/self/fd/0' AS x",
	} {
		if _, err := db.Exec(q, 0); err == nil {
			t.Errorf("%s: allowed", q)
		}
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Errorf("files created outside: %v", ents)
	}
	// Journals are created beside the database and cleaned up.
	ents, _ := os.ReadDir(dir)
	if len(ents) != 1 {
		t.Errorf("leftover files: %v", ents)
	}
}

func TestSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	f, err := fsops.New(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	os.WriteFile(filepath.Join(dir, "real.db"), nil, 0o644)
	os.Symlink("real.db", filepath.Join(dir, "link.db"))
	if _, err := f.PinFile("/link.db"); !errors.Is(err, fsops.ErrNotRegular) {
		t.Errorf("pinning a symlink: %v", err)
	}
	// A file swapped for a symlink after pinning isn't followed.
	pf, err := f.PinFile("/real.db")
	if err != nil {
		t.Fatal(err)
	}
	defer pf.Close()
	os.Rename(filepath.Join(dir, "link.db"), filepath.Join(dir, "real.db"))
	if db, err := Open(context.Background(), pf.DirPath, pf.Name, true); err == nil {
		_, err = db.Objects()
		db.Close()
		if err == nil {
			t.Error("opened a symlink")
		}
	}
}

func TestValueJSON(t *testing.T) {
	for _, s := range []string{`null`, `1`, `-2.5`, `2.0`, `"x"`, `{"int":"9007199254740993"}`, `{"real":"+Inf"}`, `{"blob":"AP8=","size":2}`} {
		var v Value
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Errorf("%s: %v", s, err)
			continue
		}
		b, _ := json.Marshal(v)
		if string(b) != s {
			t.Errorf("round trip %s -> %s", s, b)
		}
	}
	var v Value
	if err := json.Unmarshal([]byte(`{"text":"ab","size":9,"truncated":true}`), &v); err == nil {
		t.Error("accepted a truncated value")
	}
}

func TestWAL(t *testing.T) {
	db, dir := open(t, "PRAGMA journal_mode = WAL; CREATE TABLE t (x); INSERT INTO t VALUES (1)", false)
	if _, err := db.Insert("t", map[string]Value{"x": {V: int64(2)}}); err != nil {
		t.Fatal(err)
	}
	f, _ := fsops.New(dir, nil)
	defer f.Close()
	ro := pin(t, f, "/test.db", true)
	p, err := ro.Read("t", ReadOptions{})
	if err != nil || p.Total != 2 {
		t.Fatalf("read WAL database: %+v %v", p, err)
	}
}
