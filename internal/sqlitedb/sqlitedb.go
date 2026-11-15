// Package sqlitedb browses and edits SQLite databases for the SQLite file
// tool. It has no knowledge of HTTP: callers pin the file with
// fsops.PinFile and open it here through the pinned directory.
//
// SQLite runs as WebAssembly (github.com/ncruces/go-sqlite3), so the binary
// stays free of cgo. Files are opened through a VFS that only reaches pinned
// directories, which also keeps ATTACH and VACUUM INTO inside the
// database's own directory.
package sqlitedb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/ncruces/go-sqlite3"
	_ "github.com/ncruces/go-sqlite3/embed" // the SQLite Wasm binary
)

// Display limits for values in pages and query results.
var display = limits{text: 64 << 10, blob: 64}

const (
	// DefaultPageSize is the number of rows Read returns by default.
	DefaultPageSize = 100
	// MaxPageSize caps ReadOptions.Limit.
	MaxPageSize = 1000
	// DefaultMaxRows caps the rows Exec returns by default.
	DefaultMaxRows = 1000
)

// ErrInvalid marks errors caused by the request rather than the database.
var ErrInvalid = errors.New("invalid request")

// DB is an open database connection. It is not safe for concurrent use.
type DB struct {
	c   *sqlite3.Conn
	dir string
}

// Open opens the database file name in dir, a /proc/self/fd directory link
// from fsops.PinFile, which must stay open until the DB is closed. A
// read-only connection never writes the file. The connection is
// interrupted when ctx ends.
func Open(ctx context.Context, dir, name string, readOnly bool) (*DB, error) {
	if strings.ContainsRune(name, '/') {
		return nil, fmt.Errorf("%w: bad file name %q", ErrInvalid, name)
	}
	allowDir(dir)
	u := url.URL{Scheme: "file", Path: dir + "/" + name}
	q := url.Values{"vfs": {vfsName}}
	flags := sqlite3.OPEN_URI | sqlite3.OPEN_NOFOLLOW | sqlite3.OPEN_READWRITE
	if readOnly {
		q.Set("mode", "ro")
		flags = sqlite3.OPEN_URI | sqlite3.OPEN_NOFOLLOW | sqlite3.OPEN_READONLY
	}
	u.RawQuery = q.Encode()
	c, err := sqlite3.OpenFlags(u.String(), flags)
	if err != nil {
		releaseDir(dir)
		return nil, wrap(err)
	}
	c.SetInterrupt(ctx)
	c.BusyTimeout(5 * time.Second)
	return &DB{c: c, dir: dir}, nil
}

// Close closes the connection, rolling back any open transaction.
func (d *DB) Close() error {
	err := d.c.Close()
	releaseDir(d.dir)
	return err
}

// Object is a table or view.
type Object struct {
	Name string `json:"name"`
	// Type is "table", "view", "virtual" or "shadow" (a virtual table's
	// storage).
	Type         string `json:"type"`
	Columns      int    `json:"columns"`
	WithoutRowid bool   `json:"withoutRowid,omitempty"`
	SQL          string `json:"sql"`
}

// Objects lists the tables and views of the main database, user objects
// first.
func (d *DB) Objects() ([]Object, error) {
	s, _, err := d.c.Prepare(`
		SELECT l.name, l.type, l.ncol, l.wr, coalesce(m.sql, '')
		FROM pragma_table_list AS l LEFT JOIN sqlite_schema AS m ON m.name = l.name
		WHERE l.schema = 'main'
		ORDER BY l.name LIKE 'sqlite\_%' ESCAPE '\', l.type = 'shadow', l.name COLLATE NOCASE`)
	if err != nil {
		return nil, wrap(err)
	}
	defer s.Close()
	objs := []Object{}
	for s.Step() {
		objs = append(objs, Object{
			Name:         s.ColumnText(0),
			Type:         s.ColumnText(1),
			Columns:      s.ColumnInt(2),
			WithoutRowid: s.ColumnBool(3),
			SQL:          s.ColumnText(4),
		})
	}
	return objs, wrap(s.Err())
}

// Column describes a table or view column.
type Column struct {
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	NotNull bool    `json:"notNull,omitempty"`
	Default *string `json:"default,omitempty"` // SQL expression
	// PK is the column's position in the primary key, or 0.
	PK        int  `json:"pk,omitempty"`
	Generated bool `json:"generated,omitempty"`
}

// table describes a table for reading and editing.
type table struct {
	name    string
	typ     string
	columns []Column
	// key lists the expressions identifying a row: a rowid alias or the
	// primary key columns of a WITHOUT ROWID table. Empty for read-only
	// objects (views, virtual tables).
	key   []string
	rowid bool // key is the rowid
}

func (t *table) column(name string) *Column {
	for i := range t.columns {
		if t.columns[i].Name == name {
			return &t.columns[i]
		}
	}
	return nil
}

func (d *DB) table(name string) (*table, error) {
	s, _, err := d.c.Prepare(`SELECT type, wr FROM pragma_table_list WHERE schema = 'main' AND name = ?`)
	if err != nil {
		return nil, wrap(err)
	}
	defer s.Close()
	s.BindText(1, name)
	if !s.Step() {
		if err := s.Err(); err != nil {
			return nil, wrap(err)
		}
		return nil, fmt.Errorf("%w: no such table: %s", ErrInvalid, name)
	}
	t := &table{name: name, typ: s.ColumnText(0)}
	withoutRowid := s.ColumnBool(1)
	s.Close()

	s, _, err = d.c.Prepare(`SELECT name, type, "notnull", dflt_value, pk, hidden FROM pragma_table_xinfo(?, 'main')`)
	if err != nil {
		return nil, wrap(err)
	}
	defer s.Close()
	s.BindText(1, name)
	var pk []string
	for s.Step() {
		hidden := s.ColumnInt(5)
		if hidden == 1 { // hidden column of a virtual table
			continue
		}
		c := Column{
			Name:      s.ColumnText(0),
			Type:      s.ColumnText(1),
			NotNull:   s.ColumnBool(2),
			PK:        s.ColumnInt(4),
			Generated: hidden >= 2,
		}
		if s.ColumnType(3) != sqlite3.NULL {
			def := s.ColumnText(3)
			c.Default = &def
		}
		t.columns = append(t.columns, c)
	}
	if err := s.Err(); err != nil {
		return nil, wrap(err)
	}
	for _, c := range t.columns {
		if c.PK > 0 {
			pk = append(pk, "")
		}
	}
	for _, c := range t.columns {
		if c.PK > 0 && c.PK <= len(pk) {
			pk[c.PK-1] = c.Name
		}
	}

	switch {
	case t.typ == "view" || t.typ == "virtual":
	case withoutRowid:
		t.key = pk
	default:
		// The rowid can be shadowed by ordinary columns with its names.
		for _, alias := range []string{"rowid", "_rowid_", "oid"} {
			shadowed := false
			for _, c := range t.columns {
				shadowed = shadowed || strings.EqualFold(c.Name, alias)
			}
			if !shadowed {
				t.key, t.rowid = []string{alias}, true
				break
			}
		}
	}
	return t, nil
}

// quote quotes an identifier.
func quote(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func (t *table) quoted() string { return "main." + quote(t.name) }

// where returns "k1 = ?n AND k2 = ?n+1 …" for the key.
func (t *table) where(first int) string {
	parts := make([]string, len(t.key))
	for i, k := range t.key {
		parts[i] = fmt.Sprintf("%s = ?%d", quote(k), first+i)
	}
	return strings.Join(parts, " AND ")
}

// ReadOptions selects a page of rows.
type ReadOptions struct {
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"` // default DefaultPageSize
	Order  string `json:"order"` // column name; default the key
	Desc   bool   `json:"desc"`
}

// Row is one row of a table: its key (absent for read-only objects) and
// column values.
type Row struct {
	Key    []Value `json:"key,omitempty"`
	Values []Value `json:"values"`
}

// Page is a window onto a table or view.
type Page struct {
	Table   string   `json:"table"`
	Type    string   `json:"type"`
	Columns []Column `json:"columns"`
	// Key names what identifies a row ("rowid" or the primary key
	// columns); empty when the rows can't be edited.
	Key    []string `json:"key"`
	Rows   []Row    `json:"rows"`
	Total  int64    `json:"total"`
	Offset int      `json:"offset"`
}

// Read returns a page of rows from a table or view. Long values are
// truncated for display.
func (d *DB) Read(name string, o ReadOptions) (*Page, error) {
	t, err := d.table(name)
	if err != nil {
		return nil, err
	}
	if o.Limit <= 0 {
		o.Limit = DefaultPageSize
	}
	o.Limit = min(o.Limit, MaxPageSize)
	o.Offset = max(o.Offset, 0)

	var order []string
	if o.Order != "" {
		if t.column(o.Order) == nil {
			return nil, fmt.Errorf("%w: no such column: %s", ErrInvalid, o.Order)
		}
		dir := " ASC"
		if o.Desc {
			dir = " DESC"
		}
		order = append(order, quote(o.Order)+dir)
	}
	for _, k := range t.key {
		order = append(order, quote(k))
	}
	sel := make([]string, 0, len(t.key)+len(t.columns))
	for _, k := range t.key {
		sel = append(sel, quote(k))
	}
	for _, c := range t.columns {
		sel = append(sel, quote(c.Name))
	}
	q := "SELECT " + strings.Join(sel, ", ") + " FROM " + t.quoted()
	if len(order) > 0 {
		q += " ORDER BY " + strings.Join(order, ", ")
	}
	q += " LIMIT ? OFFSET ?"

	p := &Page{Table: t.name, Type: t.typ, Columns: t.columns, Key: t.key, Rows: []Row{}, Offset: o.Offset}
	if p.Key == nil {
		p.Key = []string{}
	}
	s, _, err := d.c.Prepare(q)
	if err != nil {
		return nil, wrap(err)
	}
	defer s.Close()
	s.BindInt(1, o.Limit)
	s.BindInt(2, o.Offset)
	nk := len(t.key)
	for s.Step() {
		r := Row{Values: make([]Value, len(t.columns))}
		if nk > 0 {
			r.Key = make([]Value, nk)
			for i := range nk {
				r.Key[i] = column(s, i, limits{})
			}
		}
		for i := range t.columns {
			r.Values[i] = column(s, nk+i, display)
		}
		p.Rows = append(p.Rows, r)
	}
	if err := s.Err(); err != nil {
		return nil, wrap(err)
	}
	s.Close()

	cs, _, err := d.c.Prepare("SELECT count(*) FROM " + t.quoted())
	if err != nil {
		return nil, wrap(err)
	}
	defer cs.Close()
	if cs.Step() {
		p.Total = cs.ColumnInt64(0)
	}
	return p, wrap(cs.Err())
}

// Result is the outcome of Exec: the rows of the last statement that
// returned any, and how many rows all statements changed.
type Result struct {
	Columns    []string  `json:"columns"`
	Rows       [][]Value `json:"rows"`
	Truncated  bool      `json:"truncated"` // more rows than maxRows
	Statements int       `json:"statements"`
	Changes    int64     `json:"changes"`
	Note       string    `json:"note,omitempty"`
}

// Exec runs one or more SQL statements and returns the rows of the last one
// that produced columns, up to maxRows (DefaultMaxRows if 0). Statements run
// in autocommit mode unless the SQL begins a transaction itself; one left
// open is rolled back. On error, the statements before the failing one have
// already run.
func (d *DB) Exec(sql string, maxRows int) (*Result, error) {
	if maxRows <= 0 {
		maxRows = DefaultMaxRows
	}
	res := &Result{Columns: []string{}, Rows: [][]Value{}}
	before := d.c.TotalChanges()
	for {
		s, tail, err := d.c.Prepare(sql)
		if err != nil {
			return nil, execError(res.Statements+1, err)
		}
		if s == nil { // only whitespace or comments left
			break
		}
		sql = tail
		res.Statements++
		if n := s.ColumnCount(); n > 0 {
			res.Columns = make([]string, n)
			for i := range n {
				res.Columns[i] = s.ColumnName(i)
			}
			res.Rows, res.Truncated = [][]Value{}, false
			for s.Step() {
				if len(res.Rows) == maxRows {
					res.Truncated = true
					break
				}
				row := make([]Value, n)
				for i := range n {
					row[i] = column(s, i, display)
				}
				res.Rows = append(res.Rows, row)
			}
		} else {
			for s.Step() {
			}
		}
		err = s.Err()
		s.Close()
		if err != nil {
			return nil, execError(res.Statements, err)
		}
	}
	res.Changes = d.c.TotalChanges() - before
	if !d.c.GetAutocommit() {
		d.c.Exec("ROLLBACK")
		res.Changes = 0
		res.Note = "The query left a transaction open, so it was rolled back. End it with COMMIT."
	}
	return res, nil
}

// execError reports a failure in statement n (1-based).
func execError(n int, err error) error {
	err = wrap(err)
	if n > 1 {
		return fmt.Errorf("statement %d: %w (the statements before it ran)", n, err)
	}
	return err
}

// editable returns the table if its rows can be changed.
func (d *DB) editable(name string) (*table, error) {
	t, err := d.table(name)
	if err != nil {
		return nil, err
	}
	if len(t.key) == 0 {
		return nil, fmt.Errorf("%w: %s %s can't be edited here", ErrInvalid, t.typ, name)
	}
	return t, nil
}

func (t *table) checkKey(key []Value) error {
	if len(key) != len(t.key) {
		return fmt.Errorf("%w: expected a key of %d values", ErrInvalid, len(t.key))
	}
	return nil
}

// Update sets one column of the row identified by key.
func (d *DB) Update(name string, key []Value, col string, v Value) error {
	t, err := d.editable(name)
	if err != nil {
		return err
	}
	if err := t.checkKey(key); err != nil {
		return err
	}
	c := t.column(col)
	switch {
	case c == nil:
		return fmt.Errorf("%w: no such column: %s", ErrInvalid, col)
	case c.Generated:
		return fmt.Errorf("%w: %s is a generated column", ErrInvalid, col)
	}
	s, _, err := d.c.Prepare("UPDATE " + t.quoted() + " SET " + quote(col) + " = ?1 WHERE " + t.where(2))
	if err != nil {
		return wrap(err)
	}
	defer s.Close()
	if err := v.bind(s, 1); err != nil {
		return err
	}
	for i, k := range key {
		if err := k.bind(s, i+2); err != nil {
			return err
		}
	}
	if err := s.Exec(); err != nil {
		return wrap(err)
	}
	if d.c.Changes() == 0 {
		return fmt.Errorf("%w: the row no longer exists; reload the table", ErrInvalid)
	}
	return nil
}

// Insert adds a row; columns not in values get their defaults. It returns
// the new rowid (0 for WITHOUT ROWID tables).
func (d *DB) Insert(name string, values map[string]Value) (int64, error) {
	t, err := d.editable(name)
	if err != nil {
		return 0, err
	}
	var cols []string
	var args []Value
	for _, c := range t.columns {
		if v, ok := values[c.Name]; ok {
			if c.Generated {
				return 0, fmt.Errorf("%w: %s is a generated column", ErrInvalid, c.Name)
			}
			cols = append(cols, quote(c.Name))
			args = append(args, v)
		}
	}
	if len(cols) != len(values) {
		return 0, fmt.Errorf("%w: unknown column in new row", ErrInvalid)
	}
	q := "INSERT INTO " + t.quoted() + " DEFAULT VALUES"
	if len(cols) > 0 {
		q = "INSERT INTO " + t.quoted() + " (" + strings.Join(cols, ", ") + ") VALUES (?" +
			strings.Repeat(", ?", len(cols)-1) + ")"
	}
	s, _, err := d.c.Prepare(q)
	if err != nil {
		return 0, wrap(err)
	}
	defer s.Close()
	for i, v := range args {
		if err := v.bind(s, i+1); err != nil {
			return 0, err
		}
	}
	if err := s.Exec(); err != nil {
		return 0, wrap(err)
	}
	if t.rowid {
		return d.c.LastInsertRowID(), nil
	}
	return 0, nil
}

// Delete removes the rows with the given keys in one transaction and returns
// how many were deleted.
func (d *DB) Delete(name string, keys [][]Value) (int64, error) {
	t, err := d.editable(name)
	if err != nil {
		return 0, err
	}
	for _, k := range keys {
		if err := t.checkKey(k); err != nil {
			return 0, err
		}
	}
	if err := d.c.Exec("BEGIN IMMEDIATE"); err != nil {
		return 0, wrap(err)
	}
	n, err := d.deleteRows(t, keys)
	if err != nil {
		d.c.Exec("ROLLBACK")
		return 0, err
	}
	return n, wrap(d.c.Exec("COMMIT"))
}

func (d *DB) deleteRows(t *table, keys [][]Value) (int64, error) {
	s, _, err := d.c.Prepare("DELETE FROM " + t.quoted() + " WHERE " + t.where(1))
	if err != nil {
		return 0, wrap(err)
	}
	defer s.Close()
	var n int64
	for _, key := range keys {
		for i, k := range key {
			if err := k.bind(s, i+1); err != nil {
				return 0, err
			}
		}
		if err := s.Exec(); err != nil {
			return 0, wrap(err)
		}
		n += d.c.Changes()
	}
	return n, nil
}

// Error is an error reported by SQLite, without the driver's prefix.
type Error struct{ err error }

func (e *Error) Error() string { return strings.TrimPrefix(e.err.Error(), "sqlite3: ") }
func (e *Error) Unwrap() error { return e.err }

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var se *Error
	if errors.As(err, &se) {
		return err
	}
	return &Error{err}
}
