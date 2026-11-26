package etcd

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/benenen/dbh/internal/database"
)

const (
	defaultPort    = "2379"
	defaultTimeout = 10 * time.Second
	maxKeysListed  = 200
)

// Driver implements database.Driver for etcd v3 HTTP/JSON gateway.
type Driver struct{}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	cfg, err := parseDSN(dsn)
	if err != nil {
		return nil, err
	}

	transport := http.DefaultTransport.(*http.Transport).Clone()
	if dial != nil {
		transport.DialContext = dial
	}

	client := &http.Client{
		Timeout:   cfg.timeout,
		Transport: transport,
	}

	c := &conn{
		baseURL: cfg.baseURL,
		user:    cfg.user,
		pass:    cfg.pass,
		client:  client,
	}

	if cfg.user != "" {
		if err := c.authenticate(ctx); err != nil {
			return nil, err
		}
	}

	if _, err := c.doJSON(ctx, "/v3/maintenance/status", map[string]any{}); err != nil {
		return nil, fmt.Errorf("connect etcd: %w", err)
	}

	return c, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		LineCommands: true,
		CompletionWords: []string{
			"GET", "PUT", "DEL", "DELETE", "PREFIX", "KEYS", "STATUS", "COMPACT", "POST",
		},
	}
}

func (Driver) SwitchDatabase(_ context.Context, _ database.Connection, _, _ string, _ database.Dial) (database.Connection, error) {
	return nil, fmt.Errorf("etcd does not support switching databases")
}

func (Driver) Databases(_ context.Context, _ database.Connection) ([]string, error) {
	return []string{"default"}, nil
}

func (Driver) Tables(ctx context.Context, c database.Connection) ([]string, error) {
	ec, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected etcd connection type %T", c)
	}
	rows, err := ec.rangeQuery(ctx, "", true, maxKeysListed)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var keys []string
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			return nil, err
		}
		if len(row.Values) > 0 {
			if s, ok := row.Values[0].(string); ok && s != "" {
				keys = append(keys, s)
			}
		}
	}
	return keys, rows.Err()
}

func (Driver) Columns(_ context.Context, _ database.Connection, _ string) ([]string, error) {
	return []string{"key", "value", "version", "create_revision", "mod_revision"}, nil
}

func (Driver) ColumnDetails(_ context.Context, _ database.Connection, table string) (database.Query, error) {
	if strings.TrimSpace(table) == "" {
		return database.Query{Text: "KEYS"}, nil
	}
	return database.Query{Text: "GET " + quoteToken(table)}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

func (Driver) CurrentDatabase(_ context.Context, _ database.Connection) (string, error) {
	return "default", nil
}

type config struct {
	baseURL string
	user    string
	pass    string
	timeout time.Duration
}

func parseDSN(raw string) (config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config{}, fmt.Errorf("etcd dsn is required")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return config{}, fmt.Errorf("parse etcd dsn: %w", err)
	}

	var scheme string
	switch strings.ToLower(u.Scheme) {
	case "etcd", "http":
		scheme = "http"
	case "https", "etcds":
		scheme = "https"
	default:
		return config{}, fmt.Errorf("unsupported etcd scheme %q", u.Scheme)
	}

	q := u.Query()
	if tlsVal := strings.TrimSpace(q.Get("tls")); tlsVal != "" {
		useTLS, err := strconv.ParseBool(tlsVal)
		if err != nil {
			return config{}, fmt.Errorf("invalid etcd tls flag %q: %w", tlsVal, err)
		}
		if useTLS {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}

	timeout := defaultTimeout
	if tVal := strings.TrimSpace(q.Get("timeout")); tVal != "" {
		d, err := time.ParseDuration(tVal)
		if err != nil {
			return config{}, fmt.Errorf("invalid etcd timeout %q: %w", tVal, err)
		}
		timeout = d
	}

	host := u.Host
	if host == "" {
		host = "127.0.0.1:" + defaultPort
	} else if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), defaultPort)
	}

	var user, pass string
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}

	return config{
		baseURL: scheme + "://" + host,
		user:    user,
		pass:    pass,
		timeout: timeout,
	}, nil
}

type conn struct {
	baseURL string
	user    string
	pass    string
	token   string
	client  *http.Client
}

func (c *conn) Close() error {
	return nil
}

func (c *conn) authenticate(ctx context.Context) error {
	resp, err := c.doJSON(ctx, "/v3/auth/authenticate", map[string]any{
		"name":     c.user,
		"password": c.pass,
	})
	if err != nil {
		return fmt.Errorf("etcd authenticate: %w", err)
	}
	token, _ := resp["token"].(string)
	if token == "" {
		return fmt.Errorf("etcd authenticate: empty token in response")
	}
	c.token = token
	return nil
}

func (c *conn) Exec(ctx context.Context, text string, _ ...any) error {
	rows, err := c.Query(ctx, text)
	if err != nil {
		return err
	}
	return rows.Close()
}

func (c *conn) Query(ctx context.Context, text string, _ ...any) (database.Rows, error) {
	tokens, err := splitTokens(text)
	if err != nil {
		return nil, err
	}
	if len(tokens) == 0 {
		return newMemRows([]string{"result"}, nil), nil
	}

	cmd := strings.ToUpper(tokens[0])
	switch cmd {
	case "GET":
		if len(tokens) != 2 {
			return nil, fmt.Errorf("etcd GET requires 1 argument: GET <key>")
		}
		return c.getKey(ctx, tokens[1])
	case "PREFIX":
		if len(tokens) != 2 {
			return nil, fmt.Errorf("etcd PREFIX requires 1 argument: PREFIX <prefix>")
		}
		return c.rangeQuery(ctx, tokens[1], false, 0)
	case "KEYS":
		prefix := ""
		if len(tokens) == 2 {
			prefix = tokens[1]
		} else if len(tokens) > 2 {
			return nil, fmt.Errorf("etcd KEYS accepts at most 1 argument: KEYS [prefix]")
		}
		return c.rangeQuery(ctx, prefix, true, 0)
	case "PUT":
		if len(tokens) < 3 {
			return nil, fmt.Errorf("etcd PUT requires 2 arguments: PUT <key> <value>")
		}
		val := strings.Join(tokens[2:], " ")
		return c.putKey(ctx, tokens[1], val)
	case "DEL", "DELETE":
		if len(tokens) != 2 {
			return nil, fmt.Errorf("etcd %s requires 1 argument: %s <key>", cmd, cmd)
		}
		return c.deleteKey(ctx, tokens[1])
	case "STATUS":
		return c.status(ctx)
	case "COMPACT":
		if len(tokens) != 2 {
			return nil, fmt.Errorf("etcd COMPACT requires 1 argument: COMPACT <revision>")
		}
		rev, err := strconv.ParseInt(tokens[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid revision %q: %w", tokens[1], err)
		}
		if _, err := c.doJSON(ctx, "/v3/kv/compaction", map[string]any{"revision": rev}); err != nil {
			return nil, err
		}
		return newMemRows([]string{"compacted_revision"}, [][]any{{rev}}), nil
	case "POST":
		if len(tokens) < 2 {
			return nil, fmt.Errorf("etcd POST requires a path: POST <path> [json]")
		}
		path := tokens[1]
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		payload := map[string]any{}
		if len(tokens) > 2 {
			rawBody := strings.Join(tokens[2:], " ")
			if err := json.Unmarshal([]byte(rawBody), &payload); err != nil {
				return nil, fmt.Errorf("parse etcd POST json: %w", err)
			}
		}
		resp, err := c.doJSON(ctx, path, payload)
		if err != nil {
			return nil, err
		}
		return mapToRows(resp), nil
	default:
		return nil, fmt.Errorf("unsupported etcd command %q", tokens[0])
	}
}

func (c *conn) getKey(ctx context.Context, key string) (database.Rows, error) {
	resp, err := c.doJSON(ctx, "/v3/kv/range", map[string]any{
		"key": base64.StdEncoding.EncodeToString([]byte(key)),
	})
	if err != nil {
		return nil, err
	}
	return kvResponseToRows(resp, false)
}

func (c *conn) rangeQuery(ctx context.Context, prefix string, keysOnly bool, limit int) (database.Rows, error) {
	var startKey, endKey []byte
	if prefix == "" {
		startKey = []byte{0}
		endKey = []byte{0}
	} else {
		startKey = []byte(prefix)
		endKey = prefixEnd(startKey)
	}

	req := map[string]any{
		"key":       base64.StdEncoding.EncodeToString(startKey),
		"range_end": base64.StdEncoding.EncodeToString(endKey),
	}
	if keysOnly {
		req["keys_only"] = true
	}
	if limit > 0 {
		req["limit"] = limit
	}

	resp, err := c.doJSON(ctx, "/v3/kv/range", req)
	if err != nil {
		return nil, err
	}
	return kvResponseToRows(resp, keysOnly)
}

func (c *conn) putKey(ctx context.Context, key, value string) (database.Rows, error) {
	_, err := c.doJSON(ctx, "/v3/kv/put", map[string]any{
		"key":   base64.StdEncoding.EncodeToString([]byte(key)),
		"value": base64.StdEncoding.EncodeToString([]byte(value)),
	})
	if err != nil {
		return nil, err
	}
	return newMemRows([]string{"key", "status"}, [][]any{{key, "OK"}}), nil
}

func (c *conn) deleteKey(ctx context.Context, key string) (database.Rows, error) {
	resp, err := c.doJSON(ctx, "/v3/kv/deleterange", map[string]any{
		"key": base64.StdEncoding.EncodeToString([]byte(key)),
	})
	if err != nil {
		return nil, err
	}
	deleted := any(0)
	if v, ok := resp["deleted"]; ok {
		deleted = v
	}
	return newMemRows([]string{"key", "deleted"}, [][]any{{key, deleted}}), nil
}

func (c *conn) status(ctx context.Context) (database.Rows, error) {
	resp, err := c.doJSON(ctx, "/v3/maintenance/status", map[string]any{})
	if err != nil {
		return nil, err
	}
	cols := []string{"version", "dbSize", "leader", "raftIndex", "raftTerm"}
	row := make([]any, len(cols))
	for i, col := range cols {
		row[i] = resp[col]
	}
	return newMemRows(cols, [][]any{row}), nil
}

func (c *conn) doJSON(ctx context.Context, path string, payload map[string]any) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal etcd request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build etcd request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		req.Header.Set("Authorization", c.token)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("etcd request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read etcd response: %w", err)
	}

	var parsed map[string]any
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &parsed); err != nil {
			if resp.StatusCode >= http.StatusBadRequest {
				return nil, fmt.Errorf("etcd %s returned %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
			}
			return nil, fmt.Errorf("decode etcd response: %w", err)
		}
	}

	if resp.StatusCode >= http.StatusBadRequest {
		if msg, ok := parsed["error"].(string); ok && msg != "" {
			return nil, fmt.Errorf("etcd %s: %s", path, msg)
		}
		if msg, ok := parsed["message"].(string); ok && msg != "" {
			return nil, fmt.Errorf("etcd %s: %s", path, msg)
		}
		return nil, fmt.Errorf("etcd %s returned HTTP %d", path, resp.StatusCode)
	}

	return parsed, nil
}

func kvResponseToRows(resp map[string]any, keysOnly bool) (database.Rows, error) {
	rawKVs, _ := resp["kvs"].([]any)
	if keysOnly {
		rows := make([][]any, 0, len(rawKVs))
		for _, item := range rawKVs {
			m, ok := item.(map[string]any)
			if !ok {
				continue
			}
			k, err := decodeB64(m["key"])
			if err != nil {
				return nil, err
			}
			rows = append(rows, []any{k})
		}
		return newMemRows([]string{"key"}, rows), nil
	}

	cols := []string{"key", "value", "version", "create_revision", "mod_revision"}
	rows := make([][]any, 0, len(rawKVs))
	for _, item := range rawKVs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		k, err := decodeB64(m["key"])
		if err != nil {
			return nil, err
		}
		v, err := decodeB64(m["value"])
		if err != nil {
			return nil, err
		}
		rows = append(rows, []any{k, v, m["version"], m["create_revision"], m["mod_revision"]})
	}
	return newMemRows(cols, rows), nil
}

func decodeB64(v any) (string, error) {
	s, _ := v.(string)
	if s == "" {
		return "", nil
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", fmt.Errorf("decode base64 value: %w", err)
	}
	return string(b), nil
}

func prefixEnd(prefix []byte) []byte {
	end := make([]byte, len(prefix))
	copy(end, prefix)
	for i := len(end) - 1; i >= 0; i-- {
		if end[i] < 0xff {
			end[i]++
			return end[:i+1]
		}
	}
	return []byte{0}
}

func mapToRows(m map[string]any) database.Rows {
	if len(m) == 0 {
		return newMemRows([]string{"result"}, nil)
	}
	cols := make([]string, 0, len(m))
	for k := range m {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	vals := make([]any, len(cols))
	for i, k := range cols {
		switch v := m[k].(type) {
		case map[string]any, []any:
			b, _ := json.Marshal(v)
			vals[i] = string(b)
		default:
			vals[i] = v
		}
	}
	return newMemRows(cols, [][]any{vals})
}

func splitTokens(input string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inToken := false
	var quote rune

	for i, r := range input {
		_ = i
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
