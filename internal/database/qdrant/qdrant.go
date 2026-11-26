// Package qdrant implements Qdrant HTTP REST commands and collection metadata.
package qdrant

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
)

type Driver struct{}

var _ database.Driver = Driver{}
var _ database.CurrentDatabase = Driver{}

type connection struct {
	baseURL    *url.URL
	client     *http.Client
	apiKey     string
	collection string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid qdrant connection URI")
	}

	q := parsed.Query()
	apiKey := strings.TrimSpace(q.Get("api_key"))
	insecure := strings.EqualFold(q.Get("insecure"), "true") || q.Get("insecure") == "1"
	q.Del("api_key")
	q.Del("insecure")
	parsed.RawQuery = q.Encode()
	parsed.User = nil

	collection := strings.Trim(parsed.Path, "/")
	if collection != "" {
		if err := validateName(collection); err != nil {
			return nil, err
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	if dial != nil {
		transport.DialContext = dial
		transport.Proxy = nil
	}
	if insecure {
		transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}

	conn := &connection{
		baseURL:    parsed,
		client:     &http.Client{Transport: transport},
		apiKey:     apiKey,
		collection: collection,
	}

	status, body, err := conn.do(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, body)
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		HTTPCommands:     true,
		BackslashEscapes: true,
		CompletionWords: strings.Fields(
			"GET POST PUT DELETE PATCH /collections points/scroll points/query points/search points/count points/delete index",
		),
	}
}

func (Driver) SwitchDatabase(_ context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := qdrantConnection(conn)
	if err != nil {
		return nil, err
	}
	c.collection = name
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := qdrantConnection(conn)
	if err != nil {
		return "", err
	}
	return c.collection, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return listCollections(ctx, conn)
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return listCollections(ctx, conn)
}

func listCollections(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := qdrantConnection(conn)
	if err != nil {
		return nil, err
	}
	status, body, err := c.do(ctx, http.MethodGet, "/collections", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, body)
	}
	var resp struct {
		Result struct {
			Collections []struct {
				Name string `json:"name"`
			} `json:"collections"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unexpected qdrant collections response: %w", err)
	}
	names := make([]string, 0, len(resp.Result.Collections))
	for _, col := range resp.Result.Collections {
		if col.Name != "" {
			names = append(names, col.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

const (
	internalDescribePrefix = "__dbh_qdrant_describe__:"
	internalIndexesPrefix  = "__dbh_qdrant_indexes__:"
)

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	rows, _, err := describeCollection(ctx, conn, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if name, ok := r[0].(string); ok {
			names = append(names, name)
		}
	}
	return names, nil
}

func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if _, err := qdrantConnection(conn); err != nil {
		return database.Query{}, err
	}
	if err := validateName(table); err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text:    internalDescribePrefix + table,
		Heading: "Fields:",
	}, nil
}

func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if _, err := qdrantConnection(conn); err != nil {
		return database.Query{}, err
	}
	if err := validateName(table); err != nil {
		return database.Query{}, err
	}
	return database.Query{
		Text:    internalIndexesPrefix + table,
		Heading: "Indexes:",
	}, nil
}

func describeCollection(ctx context.Context, conn database.Connection, table string) (fields [][]any, indexes [][]any, err error) {
	c, err := qdrantConnection(conn)
	if err != nil {
		return nil, nil, err
	}
	if err := validateName(table); err != nil {
		return nil, nil, err
	}
	status, body, err := c.do(ctx, http.MethodGet, "/collections/"+url.PathEscape(table), nil)
	if err != nil {
		return nil, nil, err
	}
	if status < 200 || status >= 300 {
		return nil, nil, formatHTTPError(status, body)
	}
	var resp struct {
		Result struct {
			Config struct {
				Params struct {
					Vectors json.RawMessage `json:"vectors"`
				} `json:"params"`
			} `json:"config"`
			PayloadSchema map[string]struct {
				DataType string `json:"data_type"`
				Points   int64  `json:"points"`
			} `json:"payload_schema"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, nil, fmt.Errorf("unexpected qdrant collection response: %w", err)
	}

	// Parse vector config (single unnamed vector or named vector map).
	if len(resp.Result.Config.Params.Vectors) > 0 {
		var single struct {
			Size     int    `json:"size"`
			Distance string `json:"distance"`
		}
		if json.Unmarshal(resp.Result.Config.Params.Vectors, &single) == nil && single.Size > 0 {
			detail := fmt.Sprintf("dim=%d, distance=%s", single.Size, single.Distance)
			fields = append(fields, []any{"vector", "vector", detail})
			indexes = append(indexes, []any{"vector", "hnsw", single.Distance})
		} else {
			var named map[string]struct {
				Size     int    `json:"size"`
				Distance string `json:"distance"`
			}
			if json.Unmarshal(resp.Result.Config.Params.Vectors, &named) == nil {
				names := make([]string, 0, len(named))
				for k := range named {
					names = append(names, k)
				}
				sort.Strings(names)
				for _, k := range names {
					v := named[k]
					detail := fmt.Sprintf("dim=%d, distance=%s", v.Size, v.Distance)
					fields = append(fields, []any{k, "vector", detail})
					indexes = append(indexes, []any{k, "hnsw", v.Distance})
				}
			}
		}
	}

	payloadKeys := make([]string, 0, len(resp.Result.PayloadSchema))
	for k := range resp.Result.PayloadSchema {
		payloadKeys = append(payloadKeys, k)
	}
	sort.Strings(payloadKeys)
	for _, k := range payloadKeys {
		info := resp.Result.PayloadSchema[k]
		fields = append(fields, []any{k, info.DataType, fmt.Sprintf("points=%d", info.Points)})
		indexes = append(indexes, []any{k, info.DataType, "payload"})
	}
	return fields, indexes, nil
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("qdrant commands do not accept SQL parameters")
	}
	if strings.HasPrefix(text, internalDescribePrefix) {
		table := strings.TrimPrefix(text, internalDescribePrefix)
		fields, _, err := describeCollection(ctx, c, table)
		if err != nil {
			return nil, err
		}
		return newStaticRows([]string{"field", "type", "details"}, fields), nil
	}
	if strings.HasPrefix(text, internalIndexesPrefix) {
		table := strings.TrimPrefix(text, internalIndexesPrefix)
		_, indexes, err := describeCollection(ctx, c, table)
		if err != nil {
			return nil, err
		}
		return newStaticRows([]string{"field", "type", "metric"}, indexes), nil
	}

	method, path, reqBody, err := parseCommand(text, c.collection)
	if err != nil {
		return nil, err
	}
	status, respBody, err := c.do(ctx, method, path, reqBody)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, respBody)
	}
	return parseResponseRows(respBody), nil
}

func (c *connection) Exec(ctx context.Context, text string, args ...any) error {
	rows, err := c.Query(ctx, text, args...)
	if err != nil {
		return err
	}
	return rows.Close()
}

func (c *connection) Close() error {
	c.client.CloseIdleConnections()
	return nil
}

func (c *connection) do(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	target := *c.baseURL
	if p, q, found := strings.Cut(path, "?"); found {
		target.Path = p
		target.RawQuery = q
	} else {
		target.Path = path
		target.RawQuery = ""
	}
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.apiKey != "" {
		req.Header.Set("api-key", c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func parseCommand(text, currentCollection string) (method, path string, body []byte, err error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", nil, fmt.Errorf("qdrant command cannot be empty")
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return "", "", nil, fmt.Errorf("expected HTTP method and path (e.g. GET /collections)")
	}
	method = strings.ToUpper(fields[0])
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch:
	default:
		return "", "", nil, fmt.Errorf("unsupported HTTP method %q (use GET, POST, PUT, DELETE or PATCH)", fields[0])
	}
	rest := strings.TrimSpace(trimmed[len(fields[0]):])
	idx := strings.IndexAny(rest, " \t\r\n")
	if idx < 0 {
		path = rest
	} else {
		path = rest[:idx]
		rawBody := strings.TrimSpace(rest[idx:])
		if rawBody != "" {
			if !json.Valid([]byte(rawBody)) {
				return "", "", nil, fmt.Errorf("qdrant request body must be valid JSON")
			}
			body = []byte(rawBody)
		}
	}
	if !strings.HasPrefix(path, "/") {
		if currentCollection != "" {
			path = "/collections/" + url.PathEscape(currentCollection) + "/" + path
		} else {
			path = "/" + path
		}
	}
	return method, path, body, nil
}

func parseResponseRows(body []byte) database.Rows {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var parsed any
	if err := dec.Decode(&parsed); err != nil {
		return newStaticRows([]string{"response"}, [][]any{{strings.TrimRight(string(body), "\r\n")}})
	}
	if m, ok := parsed.(map[string]any); ok {
		if res, hasRes := m["result"]; hasRes && res != nil {
			if resMap, ok := res.(map[string]any); ok {
				if pts, ok := resMap["points"].([]any); ok {
					return rowsFromJSONArray(pts)
				}
				if cols, ok := resMap["collections"].([]any); ok {
					return rowsFromJSONArray(cols)
				}
			}
			return rowsFromValue(res)
		}
	}
	return rowsFromValue(parsed)
}

func rowsFromValue(val any) database.Rows {
	switch v := val.(type) {
	case []any:
		return rowsFromJSONArray(v)
	case map[string]any:
		compact, _ := json.Marshal(v)
		return &staticRows{
			cols: []string{"document"},
			rows: []database.Row{{Values: []any{string(compact)}, Object: v}},
		}
	default:
		return newStaticRows([]string{"value"}, [][]any{{v}})
	}
}

func rowsFromJSONArray(items []any) database.Rows {
	if len(items) == 0 {
		return newStaticRows(nil, nil)
	}
	keySet := map[string]bool{}
	var objs []map[string]any
	for _, item := range items {
		m, ok := item.(map[string]any)
		if !ok {
			rows := make([][]any, len(items))
			for i, it := range items {
				rows[i] = []any{it}
			}
			return newStaticRows([]string{"value"}, rows)
		}
		objs = append(objs, m)
		for k := range m {
			keySet[k] = true
		}
	}
	cols := make([]string, 0, len(keySet))
	for k := range keySet {
		cols = append(cols, k)
	}
	sort.Strings(cols)
	rows := make([]database.Row, len(objs))
	for i, m := range objs {
		vals := make([]any, len(cols))
		for j, col := range cols {
			vals[j] = m[col]
		}
		rows[i] = database.Row{Values: vals, Object: m}
	}
	return &staticRows{cols: cols, rows: rows}
}

func formatHTTPError(status int, body []byte) error {
	var errResp struct {
		Status struct {
			Error string `json:"error"`
		} `json:"status"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Status.Error != "" {
		return fmt.Errorf("qdrant returned status %d: %s", status, errResp.Status.Error)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("qdrant returned status %d: %s", status, msg)
	}
	return fmt.Errorf("qdrant returned status %d", status)
}

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/\\?#") {
		return fmt.Errorf("invalid qdrant collection %q", name)
	}
	return nil
}

func qdrantConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a qdrant connection")
	}
	return c, nil
}

type staticRows struct {
	cols []string
	rows []database.Row
	pos  int
}

func newStaticRows(cols []string, values [][]any) *staticRows {
	rows := make([]database.Row, len(values))
	for i, v := range values {
		rows[i] = database.NewRow(cols, v)
	}
	return &staticRows{cols: cols, rows: rows}
}

func (r *staticRows) Columns() ([]string, error) { return r.cols, nil }
func (r *staticRows) Next() bool {
	if r.pos >= len(r.rows) {
		return false
	}
	r.pos++
	return true
}
func (r *staticRows) Row() (database.Row, error) {
	if r.pos == 0 || r.pos > len(r.rows) {
		return database.Row{}, fmt.Errorf("no current row")
	}
	return r.rows[r.pos-1], nil
}
func (r *staticRows) Err() error          { return nil }
func (r *staticRows) NextResultSet() bool { return false }
func (r *staticRows) Close() error        { return nil }
