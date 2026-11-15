package api

import (
	"context"
	"errors"

	"github.com/alanaktion/xwebfm/internal/sqlitedb"
)

// sqliteTool browses and edits SQLite databases (see package sqlitedb).
var sqliteTool = &FileTool{
	ID:         "sqlite",
	Name:       "SQLite",
	Extensions: []string{"sqlite", "sqlite3", "db", "db3", "s3db", "sl3"},
	Actions: map[string]ToolAction{
		// → [{name, type, columns, withoutRowid, sql}]
		"objects": {Run: sqliteRead(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			return db.Objects()
		})},
		// {table, offset, limit, order, desc} → page of rows
		"rows": {Run: sqliteRead(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			var args struct {
				Table string `json:"table"`
				sqlitedb.ReadOptions
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			return db.Read(args.Table, args.ReadOptions)
		})},
		// {sql, maxRows} → {columns, rows, truncated, statements, changes}
		"query": {Writes: true, Run: sqliteWrite(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			var args struct {
				SQL     string `json:"sql"`
				MaxRows int    `json:"maxRows"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			return db.Exec(args.SQL, args.MaxRows)
		})},
		// {table, key, column, value}
		"update": {Writes: true, Run: sqliteWrite(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			var args struct {
				Table  string           `json:"table"`
				Key    []sqlitedb.Value `json:"key"`
				Column string           `json:"column"`
				Value  sqlitedb.Value   `json:"value"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			return struct{}{}, db.Update(args.Table, args.Key, args.Column, args.Value)
		})},
		// {table, values: {column: value}} → {rowid}
		"insert": {Writes: true, Run: sqliteWrite(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			var args struct {
				Table  string                    `json:"table"`
				Values map[string]sqlitedb.Value `json:"values"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			id, err := db.Insert(args.Table, args.Values)
			return map[string]int64{"rowid": id}, err
		})},
		// {table, keys: [key, …]} → {deleted}
		"delete": {Writes: true, Run: sqliteWrite(func(db *sqlitedb.DB, req *ToolRequest) (any, error) {
			var args struct {
				Table string             `json:"table"`
				Keys  [][]sqlitedb.Value `json:"keys"`
			}
			if err := req.Args(&args); err != nil {
				return nil, err
			}
			n, err := db.Delete(args.Table, args.Keys)
			return map[string]int64{"deleted": n}, err
		})},
	},
}

func sqliteRead(fn func(*sqlitedb.DB, *ToolRequest) (any, error)) func(context.Context, *ToolRequest) (any, error) {
	return withSQLite(true, fn)
}

func sqliteWrite(fn func(*sqlitedb.DB, *ToolRequest) (any, error)) func(context.Context, *ToolRequest) (any, error) {
	return withSQLite(false, fn)
}

// withSQLite opens the request's file for the duration of fn. Database
// errors (bad SQL, constraints, "not a database") are the request's fault.
func withSQLite(readOnly bool, fn func(*sqlitedb.DB, *ToolRequest) (any, error)) func(context.Context, *ToolRequest) (any, error) {
	return func(ctx context.Context, req *ToolRequest) (any, error) {
		pf, err := req.FS.PinFile(req.Path)
		if err != nil {
			return nil, err
		}
		defer pf.Close()
		db, err := sqlitedb.Open(ctx, pf.DirPath, pf.Name, readOnly)
		if err != nil {
			return nil, &ToolError{err}
		}
		defer db.Close()
		res, err := fn(db, req)
		var te *ToolError
		var se *sqlitedb.Error
		if errors.Is(err, sqlitedb.ErrInvalid) || errors.As(err, &se) && !errors.As(err, &te) {
			err = &ToolError{err}
		}
		return res, err
	}
}
