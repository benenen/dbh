// Package milvus implements Milvus RESTful API v2 commands and collection metadata.
package milvus

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
	baseURL *url.URL
	client  *http.Client
	token   string
	dbName  string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid milvus connection URI")
	}

	q := parsed.Query()
	token := strings.TrimSpace(q.Get("token"))
	insecure := strings.EqualFold(q.Get("insecure"), "true") || q.Get("insecure") == "1"
	q.Del("token")
	q.Del("insecure")
	parsed.RawQuery = q.Encode()

	if token == "" && parsed.User != nil {
		user := parsed.User.Username()
		pass, _ := parsed.User.Password()
		if user != "" || pass != "" {
			token = user + ":" + pass
		}
	}
	parsed.User = nil

	dbName := strings.Trim(parsed.Path, "/")
	if dbName == "" {
		dbName = "default"
	}
	if err := validateName(dbName); err != nil {
		return nil, err
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
		baseURL: parsed,
		client:  &http.Client{Transport: transport},
		token:   token,
		dbName:  dbName,
	}

	if _, err := conn.callV2(ctx, http.MethodPost, "/v2/vectordb/collections/list", map[string]any{"dbName": dbName}); err != nil {
		return nil, err
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		HTTPCommands:     true,
		BackslashEscapes: true,
		HelpText:         "Milvus accepts HTTP REST v2 commands (e.g. POST collections/list {}, POST entities/query {...}).\nPress Enter when complete; use ; between commands in batch input.\n",
		CompletionWords: strings.Fields(
			"GET POST PUT DELETE PATCH " +
				"databases/list databases/create databases/drop " +
				"collections/list collections/create collections/describe collections/drop collections/load collections/release " +
				"entities/insert entities/upsert entities/delete entities/get entities/query entities/search " +
				"indexes/list indexes/create indexes/describe indexes/drop " +
				"partitions/list partitions/create partitions/drop",
		),
	}
}

func (Driver) SwitchDatabase(_ context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := milvusConnection(conn)
	if err != nil {
		return nil, err
	}
	c.dbName = name
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := milvusConnection(conn)
	if err != nil {
		return "", err
	}
	return c.dbName, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := milvusConnection(conn)
	if err != nil {
		return nil, err
	}
	data, err := c.callV2(ctx, http.MethodPost, "/v2/vectordb/databases/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	return decodeSortedStringList(data, "databases")
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := milvusConnection(conn)
	if err != nil {
		return nil, err
	}
	data, err := c.callV2(ctx, http.MethodPost, "/v2/vectordb/collections/list", map[string]any{"dbName": c.dbName})
	if err != nil {
		return nil, err
	}
	return decodeSortedStringList(data, "collections")
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	fields, err := describeFields(ctx, conn, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.Name
	}
	return names, nil
}

const (
	internalDescribePrefix = "__dbh_milvus_describe__:"
	internalIndexesPrefix  = "__dbh_milvus_indexes__:"
)

func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if _, err := milvusConnection(conn); err != nil {
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
	if _, err := milvusConnection(conn); err != nil {
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

type collectionField struct {
	Name        string
	Type        string
	PrimaryKey  bool
	AutoID      bool
	Nullable    bool
	Params      string
	Description string
}

func describeFields(ctx context.Context, conn database.Connection, table string) ([]collectionField, error) {
	c, err := milvusConnection(conn)
	if err != nil {
		return nil, err
	}
	if err := validateName(table); err != nil {
		return nil, err
	}
	data, err := c.callV2(ctx, http.MethodPost, "/v2/vectordb/collections/describe", map[string]any{
		"dbName":         c.dbName,
		"collectionName": table,
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Fields []struct {
			Name             string `json:"name"`
			Type             string `json:"type"`
			PrimaryKey       bool   `json:"primaryKey"`
			AutoID           bool   `json:"autoId"`
			Nullable         bool   `json:"nullable"`
			Description      string `json:"description"`
			ElementTypeParms []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"params"`
		} `json:"fields"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("unexpected milvus describe response: %w", err)
	}
	fields := make([]collectionField, 0, len(resp.Fields))
	for _, f := range resp.Fields {
		var params []string
		for _, p := range f.ElementTypeParms {
			if p.Key != "" {
				params = append(params, p.Key+"="+p.Value)
			}
		}
		fields = append(fields, collectionField{
			Name:        f.Name,
			Type:        f.Type,
			PrimaryKey:  f.PrimaryKey,
			AutoID:      f.AutoID,
			Nullable:    f.Nullable,
			Params:      strings.Join(params, ","),
			Description: f.Description,
		})
	}
	return fields, nil
}

func (c *connection) describeIndexes(ctx context.Context, table string) ([][]any, error) {
	if err := validateName(table); err != nil {
		return nil, err
	}
	listData, err := c.callV2(ctx, http.MethodPost, "/v2/vectordb/indexes/list", map[string]any{
		"dbName":         c.dbName,
		"collectionName": table,
	})
	if err != nil {
		return nil, err
	}
	names, err := decodeSortedStringList(listData, "indexes")
	if err != nil {
		return nil, err
	}
	rows := make([][]any, 0, len(names))
	for _, name := range names {
		descData, err := c.callV2(ctx, http.MethodPost, "/v2/vectordb/indexes/describe", map[string]any{
			"dbName":         c.dbName,
			"collectionName": table,
			"indexName":      name,
		})
		if err != nil {
			return nil, err
		}
		var items []struct {
			IndexName  string `json:"indexName"`
			FieldName  string `json:"fieldName"`
			IndexType  string `json:"indexType"`
			MetricType string `json:"metricType"`
		}
		if err := json.Unmarshal(descData, &items); err != nil {
			var single struct {
				IndexName  string `json:"indexName"`
				FieldName  string `json:"fieldName"`
				IndexType  string `json:"indexType"`
				MetricType string `json:"metricType"`
			}
			if err2 := json.Unmarshal(descData, &single); err2 != nil {
				return nil, fmt.Errorf("unexpected milvus index describe response: %w", err)
			}
			if single.IndexName != "" || single.FieldName != "" {
				items = append(items, single)
			}
		}
		if len(items) == 0 {
			rows = append(rows, []any{name, "", "", ""})
			continue
		}
		for _, it := range items {
			idxName := it.IndexName
			if idxName == "" {
				idxName = name
			}
			rows = append(rows, []any{idxName, it.FieldName, it.IndexType, it.MetricType})
		}
	}
	return rows, nil
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("milvus commands do not accept SQL parameters")
	}
	if strings.HasPrefix(text, internalDescribePrefix) {
		table := strings.TrimPrefix(text, internalDescribePrefix)
		fields, err := describeFields(ctx, c, table)
		if err != nil {
			return nil, err
		}
		rows := make([][]any, len(fields))
		for i, f := range fields {
			rows[i] = []any{f.Name, f.Type, f.PrimaryKey, f.AutoID, f.Nullable, f.Params, f.Description}
		}
		return newStaticRows([]string{"field", "type", "primary", "auto_id", "nullable", "params", "description"}, rows), nil
	}
	if strings.HasPrefix(text, internalIndexesPrefix) {
		table := strings.TrimPrefix(text, internalIndexesPrefix)
		rows, err := c.describeIndexes(ctx, table)
		if err != nil {
			return nil, err
		}
		return newStaticRows([]string{"index", "field", "type", "metric"}, rows), nil
	}

	method, path, reqBody, err := parseCommand(text)
	if err != nil {
		return nil, err
	}
	if len(reqBody) > 0 && c.dbName != "" && c.dbName != "default" && !strings.HasPrefix(path, "/v2/vectordb/databases/") {
		reqBody = injectDBName(reqBody, c.dbName)
	}
	status, respBody, err := c.do(ctx, method, path, reqBody)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, respBody)
	}
	return parseResponseRows(respBody)
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

func (c *connection) callV2(ctx context.Context, method, path string, payload any) (json.RawMessage, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	status, respBody, err := c.do(ctx, method, path, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, respBody)
	}
	var env struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &env); err != nil {
		return nil, fmt.Errorf("unexpected milvus response: %w", err)
	}
	if env.Code != 0 && env.Code != 200 {
		if env.Message != "" {
			return nil, fmt.Errorf("milvus error %d: %s", env.Code, env.Message)
		}
		return nil, fmt.Errorf("milvus error %d", env.Code)
	}
	return env.Data, nil
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
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func parseCommand(text string) (method, path string, body []byte, err error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", nil, fmt.Errorf("milvus command cannot be empty")
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return "", "", nil, fmt.Errorf("expected HTTP method and path (e.g. POST collections/list {})")
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
				return "", "", nil, fmt.Errorf("milvus request body must be valid JSON")
			}
			body = []byte(rawBody)
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/v2/vectordb/" + path
	}
	return method, path, body, nil
}

func injectDBName(body []byte, dbName string) []byte {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil || obj == nil {
		return body
	}
	if _, exists := obj["dbName"]; exists {
		return body
	}
	obj["dbName"] = dbName
	out, err := json.Marshal(obj)
	if err != nil {
		return body
	}
	return out
}

func parseResponseRows(body []byte) (database.Rows, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var env map[string]any
	if err := dec.Decode(&env); err != nil {
		return newStaticRows([]string{"response"}, [][]any{{strings.TrimRight(string(body), "\r\n")}}), nil
	}
	if codeRaw, hasCode := env["code"]; hasCode {
		if n, ok := codeRaw.(json.Number); ok {
			if code, err := n.Int64(); err == nil && code != 0 && code != 200 {
				msg, _ := env["message"].(string)
				if msg != "" {
					return nil, fmt.Errorf("milvus error %d: %s", code, msg)
				}
				return nil, fmt.Errorf("milvus error %d", code)
			}
		}
		if data, hasData := env["data"]; hasData && data != nil {
			return rowsFromValue(data), nil
		}
	}
	return rowsFromValue(env), nil
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

func decodeSortedStringList(data json.RawMessage, kind string) ([]string, error) {
	if len(data) == 0 || string(data) == "null" {
		return nil, nil
	}
	var names []string
	if err := json.Unmarshal(data, &names); err != nil {
		return nil, fmt.Errorf("unexpected milvus %s response: %w", kind, err)
	}
	sort.Strings(names)
	return names, nil
}

func formatHTTPError(status int, body []byte) error {
	var errResp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
		return fmt.Errorf("milvus returned status %d (%d): %s", status, errResp.Code, errResp.Message)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("milvus returned status %d: %s", status, msg)
	}
	return fmt.Errorf("milvus returned status %d", status)
}

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/\\?#") {
		return fmt.Errorf("invalid milvus identifier %q", name)
	}
	return nil
}

func milvusConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a milvus connection")
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
