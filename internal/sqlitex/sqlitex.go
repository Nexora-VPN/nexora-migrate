// Package sqlitex is the thin read-only SQLite layer the file-backed readers
// share.
//
// Two things matter here and nothing else does. First, the file is opened
// **read-only and immutable**: the operator is handing us their live panel's
// database, sometimes the actual file the old panel still has open, and this
// tool must not be able to write to it, journal it or recover it. Second, every
// column is probed before it is selected — 3x-ui and s-ui add columns between
// releases, and a reader that names a column the operator's version does not
// have fails the whole migration instead of the one field.
package sqlitex

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure Go: no cgo, so Windows builds are one command
)

// DB is a read-only handle on a source panel's database.
type DB struct {
	sql *sql.DB
	// cols caches PRAGMA table_info per table.
	cols map[string]map[string]bool
}

// Open opens path read-only. It never creates a file and never writes one.
func Open(path string) (*DB, error) {
	if path == "" {
		return nil, fmt.Errorf("no database file given")
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a database file", path)
	}
	// immutable=1 tells SQLite the file cannot change underneath us, which also
	// stops it wanting a write lock or a journal beside a file we may not own.
	dsn := "file:" + url.PathEscape(path) + "?mode=ro&immutable=1&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s is not a readable SQLite database: %w", path, err)
	}
	return &DB{sql: db, cols: map[string]map[string]bool{}}, nil
}

// Close releases the handle.
func (d *DB) Close() error { return d.sql.Close() }

// HasTable reports whether a table exists.
func (d *DB) HasTable(name string) bool {
	var n int
	err := d.sql.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name).Scan(&n)
	return err == nil && n > 0
}

// Columns returns the column set of a table, cached.
func (d *DB) Columns(table string) map[string]bool {
	if c, ok := d.cols[table]; ok {
		return c
	}
	out := map[string]bool{}
	rows, err := d.sql.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name string
			if rows.Scan(&name) == nil {
				out[name] = true
			}
		}
	}
	d.cols[table] = out
	return out
}

// Row is one result row with every value as an any, keyed by column name.
type Row map[string]any

// Select reads every row of a table, asking only for the columns that exist.
// want lists the columns the caller would like; the ones the file does not have
// are simply absent from the returned rows, which is what lets one reader serve
// several upstream versions.
func (d *DB) Select(table string, want []string, orderBy string) ([]Row, error) {
	if !d.HasTable(table) {
		return nil, fmt.Errorf("this database has no %q table — it does not look like the panel you picked", table)
	}
	have := d.Columns(table)
	var cols []string
	for _, c := range want {
		if have[c] {
			cols = append(cols, `"`+c+`"`)
		}
	}
	if len(cols) == 0 {
		return nil, fmt.Errorf("the %q table has none of the columns this reader needs", table)
	}
	q := fmt.Sprintf(`SELECT %s FROM "%s"`, strings.Join(cols, ", "), table)
	if orderBy != "" && have[orderBy] {
		q += ` ORDER BY "` + orderBy + `"`
	}
	return d.Query(q)
}

// Query runs an arbitrary read and returns generic rows.
func (d *DB) Query(q string, args ...any) ([]Row, error) {
	rows, err := d.sql.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []Row
	for rows.Next() {
		vals := make([]any, len(names))
		ptrs := make([]any, len(names))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		r := Row{}
		for i, n := range names {
			r[n] = vals[i]
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Str reads a column as text. Missing or NULL is "".
func (r Row) Str(key string) string {
	switch v := r[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case []byte:
		return string(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		if v {
			return "true"
		}
		return "false"
	default:
		return fmt.Sprint(v)
	}
}

// Int reads a column as an int64. Anything unreadable is 0.
func (r Row) Int(key string) int64 {
	switch v := r[key].(type) {
	case int64:
		return v
	case float64:
		return int64(v)
	case bool:
		if v {
			return 1
		}
		return 0
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	case []byte:
		n, _ := strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		return n
	}
	return 0
}

// Bool reads a column as a boolean, accepting SQLite's several spellings.
func (r Row) Bool(key string) bool {
	switch v := r[key].(type) {
	case bool:
		return v
	case string:
		return v == "1" || strings.EqualFold(v, "true")
	case []byte:
		s := string(v)
		return s == "1" || strings.EqualFold(s, "true")
	}
	return r.Int(key) != 0
}

// JSON reads a column holding a JSON document. An empty or invalid value
// returns nil rather than an error: a single unparseable row must not stop a
// migration, and the reader turns a nil into a note on the item.
func (r Row) JSON(key string) json.RawMessage {
	s := strings.TrimSpace(r.Str(key))
	if s == "" || s == "null" {
		return nil
	}
	if !json.Valid([]byte(s)) {
		return nil
	}
	return json.RawMessage(s)
}

// Has reports whether a column was present and non-NULL.
func (r Row) Has(key string) bool {
	v, ok := r[key]
	return ok && v != nil
}
