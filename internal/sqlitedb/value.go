package sqlitedb

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"

	"github.com/ncruces/go-sqlite3"
)

// Value is one SQLite value. In JSON it is null, a number or a string, or an
// object for what JSON can't hold exactly:
//
//	{"int": "9007199254740993"}           integers beyond ±2^53
//	{"real": "Infinity"}                  non-finite reals
//	{"blob": "<base64>", "size": n}       blobs
//	{"text": "…", "size": n, "truncated": true}
//
// Values read for display may be truncated (see Page); a truncated value
// can't be sent back.
type Value struct {
	V         any // nil, int64, float64, string or []byte
	Size      int // full length of a truncated text or blob
	Truncated bool
}

const maxSafeInt = 1<<53 - 1

func (v Value) MarshalJSON() ([]byte, error) {
	switch x := v.V.(type) {
	case nil:
		return []byte("null"), nil
	case int64:
		if x > maxSafeInt || x < -maxSafeInt {
			return json.Marshal(map[string]string{"int": strconv.FormatInt(x, 10)})
		}
		return strconv.AppendInt(nil, x, 10), nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) {
			return json.Marshal(map[string]string{"real": strconv.FormatFloat(x, 'g', -1, 64)})
		}
		b := strconv.AppendFloat(nil, x, 'g', -1, 64)
		if !bytes.ContainsAny(b, ".eEn") {
			b = append(b, ".0"...) // keep it a real when sent back
		}
		return b, nil
	case string:
		if v.Truncated {
			return json.Marshal(struct {
				Text      string `json:"text"`
				Size      int    `json:"size"`
				Truncated bool   `json:"truncated"`
			}{x, v.Size, true})
		}
		return json.Marshal(x)
	case []byte:
		size := len(x)
		if v.Truncated {
			size = v.Size
		}
		return json.Marshal(struct {
			Blob      string `json:"blob"`
			Size      int    `json:"size"`
			Truncated bool   `json:"truncated,omitempty"`
		}{base64.StdEncoding.EncodeToString(x), size, v.Truncated})
	}
	return nil, fmt.Errorf("sqlitedb: unsupported value %T", v.V)
}

func (v *Value) UnmarshalJSON(b []byte) error {
	*v = Value{}
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return fmt.Errorf("empty value")
	case string(b) == "null":
		return nil
	case b[0] == '"':
		var s string
		err := json.Unmarshal(b, &s)
		v.V = s
		return err
	case b[0] == '{':
		var o struct {
			Int       *string `json:"int"`
			Real      *string `json:"real"`
			Blob      *string `json:"blob"`
			Text      *string `json:"text"`
			Size      int     `json:"size"`
			Truncated bool    `json:"truncated"`
		}
		if err := json.Unmarshal(b, &o); err != nil {
			return err
		}
		switch {
		case o.Truncated:
			return fmt.Errorf("a truncated value can't be written back")
		case o.Int != nil:
			n, err := strconv.ParseInt(*o.Int, 10, 64)
			v.V = n
			return err
		case o.Real != nil:
			f, err := strconv.ParseFloat(*o.Real, 64)
			v.V = f
			return err
		case o.Blob != nil:
			data, err := base64.StdEncoding.DecodeString(*o.Blob)
			v.V = data
			return err
		case o.Text != nil:
			v.V = *o.Text
			return nil
		}
		return fmt.Errorf("unrecognised value %s", b)
	case string(b) == "true" || string(b) == "false":
		v.V = map[bool]int64{true: 1, false: 0}[string(b) == "true"]
		return nil
	default:
		if n, err := strconv.ParseInt(string(b), 10, 64); err == nil {
			v.V = n
			return nil
		}
		f, err := strconv.ParseFloat(string(b), 64)
		if err != nil {
			return fmt.Errorf("invalid number %s", b)
		}
		v.V = f
		return nil
	}
}

// bind binds v to parameter i (1-based) of s.
func (v Value) bind(s *sqlite3.Stmt, i int) error {
	switch x := v.V.(type) {
	case nil:
		return s.BindNull(i)
	case int64:
		return s.BindInt64(i, x)
	case float64:
		return s.BindFloat(i, x)
	case string:
		return s.BindText(i, x)
	case []byte:
		return s.BindBlob(i, x)
	}
	return fmt.Errorf("sqlitedb: unsupported value %T", v.V)
}

// limits caps the texts and blobs read for display; 0 reads them whole.
type limits struct{ text, blob int }

// column reads column i of the current row, cutting texts and blobs longer
// than the limits and marking them truncated.
func column(s *sqlite3.Stmt, i int, lim limits) Value {
	switch s.ColumnType(i) {
	case sqlite3.INTEGER:
		return Value{V: s.ColumnInt64(i)}
	case sqlite3.FLOAT:
		return Value{V: s.ColumnFloat(i)}
	case sqlite3.TEXT:
		raw := s.ColumnRawText(i)
		if lim.text > 0 && len(raw) > lim.text {
			cut := lim.text
			for cut > 0 && raw[cut]&0xC0 == 0x80 { // don't split a UTF-8 sequence
				cut--
			}
			return Value{V: string(raw[:cut]), Size: len(raw), Truncated: true}
		}
		return Value{V: string(raw)}
	case sqlite3.BLOB:
		raw := s.ColumnRawBlob(i)
		if lim.blob > 0 && len(raw) > lim.blob {
			return Value{V: bytes.Clone(raw[:lim.blob]), Size: len(raw), Truncated: true}
		}
		return Value{V: bytes.Clone(raw)}
	}
	return Value{}
}
