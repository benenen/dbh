package bbolt

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/benenen/dbh/internal/database"
	bolt "go.etcd.io/bbolt"
)

const defaultTimeout = 5 * time.Second

// Driver implements database.Driver for embedded bbolt databases.
type Driver struct{}

func (Driver) Open(_ context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	if dial != nil {
		return nil, fmt.Errorf("bbolt does not support --proxy")
	}

	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	opts := &bolt.Options{
		Timeout:  cfg.timeout,
		ReadOnly: cfg.readOnly,
	}
	db, err := bolt.Open(cfg.path, 0o600, opts)
	if err != nil {
		return nil, fmt.Errorf("open bbolt %s: %w", cfg.path, err)
	}

	return &conn{db: db, path: cfg.path}, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		LineCommands: true,
		CompletionWords: []string{
			"BUCKETS", "KEYS", "GET", "PUT", "DEL", "DELETE", "CREATE BUCKET", "DELETE BUCKET", "STATS",
		},
	}
}

func (Driver) SwitchDatabase(_ context.Context, _ database.Connection, _, _ string, _ database.Dial) (database.Connection, error) {
	return nil, fmt.Errorf("bbolt does not support switching databases")
}

func (Driver) Databases(_ context.Context, c database.Connection) ([]string, error) {
	bc, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected bbolt connection type %T", c)
	}
	return []string{filepath.Base(bc.path)}, nil
}

func (Driver) CurrentDatabase(_ context.Context, c database.Connection) (string, error) {
	bc, ok := c.(*conn)
	if !ok {
		return "", fmt.Errorf("unexpected bbolt connection type %T", c)
	}
	return filepath.Base(bc.path), nil
}

func (Driver) Tables(_ context.Context, c database.Connection) ([]string, error) {
	bc, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected bbolt connection type %T", c)
	}
	var buckets []string
	err := bc.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bolt.Bucket) error {
			buckets = append(buckets, string(name))
			return nil
		})
	})
	return buckets, err
}

func (Driver) Columns(_ context.Context, _ database.Connection, _ string) ([]string, error) {
	return []string{"key", "value"}, nil
}

func (Driver) ColumnDetails(_ context.Context, _ database.Connection, table string) (database.Query, error) {
	if strings.TrimSpace(table) == "" {
		return database.Query{Text: "BUCKETS"}, nil
	}
	return database.Query{Text: "KEYS " + quoteToken(table)}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

type config struct {
	path     string
	readOnly bool
	timeout  time.Duration
}

func parseDSN(raw string) (config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config{}, fmt.Errorf("bbolt dsn is required")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return config{}, fmt.Errorf("parse bbolt dsn: %w", err)
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "bbolt" && scheme != "file" {
		return config{}, fmt.Errorf("unsupported bbolt scheme %q", u.Scheme)
	}

	q := u.Query()
	readOnly := false
	if ro := strings.TrimSpace(q.Get("readonly")); ro != "" {
		v, err := strconv.ParseBool(ro)
		if err != nil {
			return config{}, fmt.Errorf("invalid bbolt readonly flag %q: %w", ro, err)
		}
		readOnly = v
	}

	timeout := defaultTimeout
	if tVal := strings.TrimSpace(q.Get("timeout")); tVal != "" {
		d, err := time.ParseDuration(tVal)
		if err != nil {
			return config{}, fmt.Errorf("invalid bbolt timeout %q: %w", tVal, err)
		}
		timeout = d
	}

	path := u.Path
	if u.Host != "" {
		path = u.Host + path
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if path == "" {
		return config{}, fmt.Errorf("bbolt file path is required")
	}

	return config{
		path:     path,
		readOnly: readOnly,
		timeout:  timeout,
	}, nil
}

type conn struct {
	db   *bolt.DB
	path string
}

func (c *conn) Close() error {
	return c.db.Close()
}

func (c *conn) Exec(ctx context.Context, text string, _ ...any) error {
	rows, err := c.Query(ctx, text)
	if err != nil {
		return err
	}
	return rows.Close()
}

func (c *conn) Query(_ context.Context, text string, _ ...any) (database.Rows, error) {
	tokens, err := splitTokens(text)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return newMemRows([]string{"result"}, nil), nil
	}

	cmd := strings.ToUpper(tokens[0])
	switch cmd {
	case "BUCKETS":
		return c.listBuckets()
	case "KEYS":
		if len(tokens) < 2 || len(tokens) > 3 {
			return nil, fmt.Errorf("bbolt KEYS requires 1 or 2 arguments: KEYS <bucket> [prefix]")
		}
		prefix := ""
		if len(tokens) == 3 {
			prefix = tokens[2]
		}
		return c.listKeys(tokens[1], prefix)
	case "GET":
		if len(tokens) != 3 {
			return nil, fmt.Errorf("bbolt GET requires 2 arguments: GET <bucket> <key>")
		}
		return c.getKey(tokens[1], tokens[2])
	case "PUT":
		if len(tokens) < 4 {
			return nil, fmt.Errorf("bbolt PUT requires 3 arguments: PUT <bucket> <key> <value>")
		}
		val := strings.Join(tokens[3:], " ")
		return c.putKey(tokens[1], tokens[2], val)
	case "DEL", "DELETE":
		if len(tokens) == 3 && strings.EqualFold(tokens[1], "BUCKET") {
			return c.deleteBucket(tokens[2])
		}
		if len(tokens) != 3 {
			return nil, fmt.Errorf("bbolt %s requires 2 arguments: %s <bucket> <key>", cmd, cmd)
		}
		return c.deleteKey(tokens[1], tokens[2])
	case "CREATE":
		if len(tokens) != 3 || !strings.EqualFold(tokens[1], "BUCKET") {
			return nil, fmt.Errorf("bbolt CREATE requires: CREATE BUCKET <bucket>")
		}
		return c.createBucket(tokens[2])
	case "STATS":
		return c.stats()
	default:
		return nil, fmt.Errorf("unsupported bbolt command %q", tokens[0])
	}
}

func (c *conn) listBuckets() (database.Rows, error) {
	var rows [][]any
	err := c.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			st := b.Stats()
			rows = append(rows, []any{string(name), st.KeyN})
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"bucket", "keys"}, rows), nil
}

func (c *conn) listKeys(bucketName, prefix string) (database.Rows, error) {
	var rows [][]any
	err := c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return fmt.Errorf("bucket %q does not exist", bucketName)
		}
		cur := b.Cursor()
		if prefix == "" {
			for k, v := cur.First(); k != nil; k, v = cur.Next() {
				rows = append(rows, []any{string(k), string(v)})
			}
			return nil
		}
		pfx := []byte(prefix)
		for k, v := cur.Seek(pfx); k != nil && bytes.HasPrefix(k, pfx); k, v = cur.Next() {
			rows = append(rows, []any{string(k), string(v)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"key", "value"}, rows), nil
}

func (c *conn) getKey(bucketName, key string) (database.Rows, error) {
	var rows [][]any
	err := c.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return fmt.Errorf("bucket %q does not exist", bucketName)
		}
		v := b.Get([]byte(key))
		if v != nil {
			rows = append(rows, []any{key, string(v)})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"key", "value"}, rows), nil
}

func (c *conn) putKey(bucketName, key, val string) (database.Rows, error) {
	err := c.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		if err != nil {
			return err
		}
		return b.Put([]byte(key), []byte(val))
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"bucket", "key", "status"}, [][]any{{bucketName, key, "OK"}}), nil
}

func (c *conn) deleteKey(bucketName, key string) (database.Rows, error) {
	err := c.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte(bucketName))
		if b == nil {
			return fmt.Errorf("bucket %q does not exist", bucketName)
		}
		return b.Delete([]byte(key))
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"bucket", "key", "status"}, [][]any{{bucketName, key, "DELETED"}}), nil
}

func (c *conn) createBucket(bucketName string) (database.Rows, error) {
	err := c.db.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists([]byte(bucketName))
		return err
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"bucket", "status"}, [][]any{{bucketName, "CREATED"}}), nil
}

func (c *conn) deleteBucket(bucketName string) (database.Rows, error) {
	err := c.db.Update(func(tx *bolt.Tx) error {
		return tx.DeleteBucket([]byte(bucketName))
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"bucket", "status"}, [][]any{{bucketName, "DELETED"}}), nil
}

func (c *conn) stats() (database.Rows, error) {
	st := c.db.Stats()
	cols := []string{"free_pages", "pending_pages", "open_tx", "tx_count"}
	row := []any{st.FreePageN, st.PendingPageN, st.OpenTxN, st.TxN}
	return newMemRows(cols, [][]any{row}), nil
}

func splitTokens(input string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	var quote rune

	for _, r := range input {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			cur.WriteRune(r)
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			inToken = true
			continue
		}
		if unicode.IsSpace(r) {
			if inToken {
				tokens = append(tokens, cur.String())
				cur.Reset()
				inToken = false
			}
			continue
		}
		inToken = true
		cur.WriteRune(r)
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quoted string")
	}
	if inToken {
		tokens = append(tokens, cur.String())
	}
	return tokens, nil
}

func quoteToken(s string) string {
	if !strings.ContainsAny(s, " \t\n\"'") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

type memRows struct {
	cols []string
	rows [][]any
	idx  int
}

func newMemRows(cols []string, rows [][]any) *memRows {
	return &memRows{cols: cols, rows: rows, idx: -1}
}

func (r *memRows) Columns() ([]string, error) { return r.cols, nil }
func (r *memRows) Next() bool {
	r.idx++
	return r.idx < len(r.rows)
}
func (r *memRows) Row() (database.Row, error) {
	if r.idx < 0 || r.idx >= len(r.rows) {
		return database.Row{}, fmt.Errorf("row index out of bounds")
	}
	return database.NewRow(r.cols, r.rows[r.idx]), nil
}
func (r *memRows) Err() error          { return nil }
func (r *memRows) NextResultSet() bool { return false }
func (r *memRows) Close() error        { return nil }
