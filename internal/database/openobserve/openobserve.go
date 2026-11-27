package openobserve

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

	"github.com/benenen/dbh/internal/database"
)

const (
	defaultPort       = "5080"
	defaultOrg        = "default"
	defaultStreamType = "logs"
	defaultTimeout    = 30 * time.Second
)

// Driver implements database.Driver for OpenObserve REST SQL/Ingest API.
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

	c := &conn{
		baseURL:    cfg.baseURL,
		org:        cfg.org,
		streamType: cfg.streamType,
		authHeader: cfg.authHeader,
		client: &http.Client{
			Timeout:   cfg.timeout,
			Transport: transport,
		},
	}

	if _, _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/api/%s/streams?type=%s", url.PathEscape(c.org), url.QueryEscape(c.streamType)), nil); err != nil {
		return nil, fmt.Errorf("connect openobserve: %w", err)
	}

	return c, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		HelpText: "OpenObserve accepts SQL queries, SHOW STREAMS, SHOW SCHEMA <stream>, or INGEST <stream> <json> ending with ;.\n",
		CompletionWords: []string{
			"SELECT", "FROM", "WHERE", "GROUP", "BY", "ORDER", "LIMIT",
			"SHOW", "STREAMS", "SCHEMA", "INGEST",
		},
	}
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string, dial database.Dial) (database.Connection, error) {
	u, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("parse openobserve dsn: %w", err)
	}
	u.Path = "/" + strings.TrimPrefix(strings.TrimSpace(name), "/")
	return (Driver{}).Open(ctx, u.String(), dial)
}

func (Driver) Databases(ctx context.Context, c database.Connection) ([]string, error) {
	oc, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected openobserve connection type %T", c)
	}
	status, raw, err := oc.do(ctx, http.MethodGet, "/api/organizations", nil)
	if err != nil || status >= http.StatusBadRequest {
		return []string{oc.org}, nil
	}
	var orgs []struct {
		Identifier string `json:"identifier"`
		Name       string `json:"name"`
	}
	if err := json.Unmarshal(raw, &orgs); err != nil || len(orgs) == 0 {
		return []string{oc.org}, nil
	}
	out := make([]string, 0, len(orgs))
	for _, o := range orgs {
		if o.Identifier != "" {
			out = append(out, o.Identifier)
		} else if o.Name != "" {
			out = append(out, o.Name)
		}
	}
	if len(out) == 0 {
		return []string{oc.org}, nil
	}
	return out, nil
}

func (Driver) CurrentDatabase(_ context.Context, c database.Connection) (string, error) {
	oc, ok := c.(*conn)
	if !ok {
		return "", fmt.Errorf("unexpected openobserve connection type %T", c)
	}
	return oc.org, nil
}

func (Driver) Tables(ctx context.Context, c database.Connection) ([]string, error) {
	oc, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected openobserve connection type %T", c)
	}
	streams, err := oc.listStreams(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(streams))
	for _, s := range streams {
		if s.Name != "" {
			out = append(out, s.Name)
		}
	}
	return out, nil
}

func (Driver) Columns(ctx context.Context, c database.Connection, table string) ([]string, error) {
	oc, ok := c.(*conn)
	if !ok {
		return nil, fmt.Errorf("unexpected openobserve connection type %T", c)
	}
	fields, err := oc.streamSchema(ctx, table)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.Name)
	}
	return out, nil
}

func (Driver) ColumnDetails(_ context.Context, _ database.Connection, table string) (database.Query, error) {
	if strings.TrimSpace(table) == "" {
		return database.Query{Text: "SHOW STREAMS"}, nil
	}
	return database.Query{Text: "SHOW SCHEMA " + table}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

type config struct {
	baseURL    string
	org        string
	streamType string
	authHeader string
	timeout    time.Duration
}

func parseDSN(raw string) (config, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return config{}, fmt.Errorf("openobserve dsn is required")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return config{}, fmt.Errorf("parse openobserve dsn: %w", err)
	}

	var scheme string
	switch strings.ToLower(u.Scheme) {
	case "openobserve", "http":
		scheme = "http"
	case "https", "openobserves":
		scheme = "https"
	default:
		return config{}, fmt.Errorf("unsupported openobserve scheme %q", u.Scheme)
	}

	q := u.Query()
	if tlsVal := strings.TrimSpace(q.Get("tls")); tlsVal != "" {
		useTLS, err := strconv.ParseBool(tlsVal)
		if err != nil {
			return config{}, fmt.Errorf("invalid openobserve tls flag %q: %w", tlsVal, err)
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
			return config{}, fmt.Errorf("invalid openobserve timeout %q: %w", tVal, err)
		}
		timeout = d
	}

	org := strings.Trim(u.Path, "/")
	if org == "" {
		org = defaultOrg
	}

	streamType := strings.TrimSpace(q.Get("stream_type"))
	if streamType == "" {
		streamType = defaultStreamType
	}

	host := u.Host
	if host == "" {
		host = "127.0.0.1:" + defaultPort
	} else if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), defaultPort)
	}

	authHeader := ""
	if token := strings.TrimSpace(q.Get("token")); token != "" {
		authHeader = "Bearer " + token
	} else if u.User != nil {
		user := u.User.Username()
		pass, _ := u.User.Password()
		authHeader = "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
	}

	return config{
		baseURL:    scheme + "://" + host,
		org:        org,
		streamType: streamType,
		authHeader: authHeader,
		timeout:    timeout,
	}, nil
}

type conn struct {
	baseURL    string
	org        string
	streamType string
	authHeader string
	client     *http.Client
}

func (c *conn) Close() error {
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
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), ";"))
	if trimmed == "" {
		return newMemRows([]string{"result"}, nil), nil
	}

	upper := strings.ToUpper(trimmed)
	switch {
	case upper == "SHOW STREAMS":
		streams, err := c.listStreams(ctx)
		if err != nil {
			return nil, err
		}
		cols := []string{"name", "stream_type", "doc_num", "storage_size"}
		rows := make([][]any, 0, len(streams))
		for _, s := range streams {
			rows = append(rows, []any{s.Name, s.StreamType, s.Stats.DocNum, s.Stats.StorageSize})
		}
		return newMemRows(cols, rows), nil
	case strings.HasPrefix(upper, "SHOW SCHEMA "):
		table := strings.TrimSpace(trimmed[len("SHOW SCHEMA "):])
		fields, err := c.streamSchema(ctx, table)
		if err != nil {
			return nil, err
		}
		cols := []string{"name", "type"}
		rows := make([][]any, 0, len(fields))
		for _, f := range fields {
			rows = append(rows, []any{f.Name, f.Type})
		}
		return newMemRows(cols, rows), nil
	case strings.HasPrefix(upper, "INGEST "):
		rest := strings.TrimSpace(trimmed[len("INGEST "):])
		parts := strings.SplitN(rest, " ", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			return nil, fmt.Errorf("openobserve INGEST requires: INGEST <stream> <json>")
		}
		return c.ingest(ctx, strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1]))
	default:
		return c.searchSQL(ctx, trimmed)
	}
}

type streamInfo struct {
	Name       string `json:"name"`
	StreamType string `json:"stream_type"`
	Stats      struct {
		DocNum      int64   `json:"doc_num"`
		StorageSize float64 `json:"storage_size"`
	} `json:"stats"`
}

func (c *conn) listStreams(ctx context.Context) ([]streamInfo, error) {
	path := fmt.Sprintf("/api/%s/streams?type=%s", url.PathEscape(c.org), url.QueryEscape(c.streamType))
	_, raw, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		List []streamInfo `json:"list"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode openobserve streams: %w", err)
	}
	return resp.List, nil
}

type schemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

func (c *conn) streamSchema(ctx context.Context, table string) ([]schemaField, error) {
	path := fmt.Sprintf("/api/%s/streams/%s/schema?type=%s", url.PathEscape(c.org), url.PathEscape(table), url.QueryEscape(c.streamType))
	_, raw, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Schema []schemaField `json:"schema"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode openobserve schema: %w", err)
	}
	return resp.Schema, nil
}

func (c *conn) ingest(ctx context.Context, stream, payload string) (database.Rows, error) {
	body := []byte(payload)
	if strings.HasPrefix(payload, "{") {
		body = []byte("[" + payload + "]")
	}
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid openobserve ingest json")
	}
	path := fmt.Sprintf("/api/%s/%s/_json", url.PathEscape(c.org), url.PathEscape(stream))
	_, raw, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	var resp struct {
		Code   int `json:"code"`
		Status []struct {
			Name       string `json:"name"`
			Successful int    `json:"successful"`
			Failed     int    `json:"failed"`
		} `json:"status"`
	}
	if err := json.Unmarshal(raw, &resp); err == nil && len(resp.Status) > 0 {
		s := resp.Status[0]
		return newMemRows([]string{"stream", "successful", "failed"}, [][]any{{s.Name, s.Successful, s.Failed}}), nil
	}
	return newMemRows([]string{"stream", "status"}, [][]any{{stream, "OK"}}), nil
}

func (c *conn) searchSQL(ctx context.Context, sqlText string) (database.Rows, error) {
	now := time.Now().Add(24 * time.Hour).UnixMicro()
	// Newer OpenObserve rejects start_time 0 as an invalid time range; 1µs still covers all data.
	reqPayload := map[string]any{
		"query": map[string]any{
			"sql":        sqlText,
			"start_time": 1,
			"end_time":   now,
			"from":       0,
			"size":       100,
		},
	}
	body, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("marshal openobserve search: %w", err)
	}
	path := fmt.Sprintf("/api/%s/_search?type=%s", url.PathEscape(c.org), url.QueryEscape(c.streamType))
	_, raw, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}

	var resp struct {
		Hits []map[string]any `json:"hits"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode openobserve search response: %w", err)
	}
	if len(resp.Hits) == 0 {
		return newMemRows([]string{"result"}, nil), nil
	}

	colSet := make(map[string]struct{})
	for _, hit := range resp.Hits {
		for k := range hit {
			colSet[k] = struct{}{}
		}
	}
	cols := make([]string, 0, len(colSet))
	for k := range colSet {
		cols = append(cols, k)
	}
	sort.Strings(cols)

	rows := make([][]any, 0, len(resp.Hits))
	for _, hit := range resp.Hits {
		vals := make([]any, len(cols))
		for i, col := range cols {
			switch v := hit[col].(type) {
			case map[string]any, []any:
				b, _ := json.Marshal(v)
				vals[i] = string(b)
			default:
				vals[i] = v
			}
		}
		rows = append(rows, vals)
	}
	return newMemRows(cols, rows), nil
}

func (c *conn) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return 0, nil, fmt.Errorf("build openobserve request: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.authHeader != "" {
		req.Header.Set("Authorization", c.authHeader)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, fmt.Errorf("openobserve request %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("read openobserve response: %w", err)
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return resp.StatusCode, raw, fmt.Errorf("openobserve %s returned HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return resp.StatusCode, raw, nil
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
