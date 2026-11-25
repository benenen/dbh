// Package elasticsearch implements Elasticsearch HTTP REST commands and index metadata.
package elasticsearch

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
	user    string
	pass    string
	apiKey  string
	index   string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid Elasticsearch connection URI")
	}

	q := parsed.Query()
	apiKey := q.Get("api_key")
	insecure := strings.EqualFold(q.Get("insecure"), "true") || q.Get("insecure") == "1"
	q.Del("api_key")
	q.Del("insecure")
	parsed.RawQuery = q.Encode()

	var user, pass string
	if parsed.User != nil {
		user = parsed.User.Username()
		pass, _ = parsed.User.Password()
		parsed.User = nil
	}

	index := strings.Trim(parsed.Path, "/")
	if index != "" {
		if err := validateIndex(index); err != nil {
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
		baseURL: parsed,
		client:  &http.Client{Transport: transport},
		user:    user,
		pass:    pass,
		apiKey:  apiKey,
		index:   index,
	}

	status, body, err := conn.do(ctx, http.MethodGet, "/", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatESError(status, body)
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		HTTPCommands:     true,
		BackslashEscapes: true,
		CompletionWords:  strings.Fields("GET POST PUT DELETE HEAD PATCH _search _doc _mapping _settings _cat/indices _cat/health _cat/nodes _count _sql _refresh _analyze"),
	}
}

func (Driver) SwitchDatabase(_ context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateIndex(name); err != nil {
		return nil, err
	}
	c, err := esConnection(conn)
	if err != nil {
		return nil, err
	}
	c.index = name
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := esConnection(conn)
	if err != nil {
		return "", err
	}
	return c.index, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	return listIndices(ctx, conn)
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	return listIndices(ctx, conn)
}

func listIndices(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := esConnection(conn)
	if err != nil {
		return nil, err
	}
	status, body, err := c.do(ctx, http.MethodGet, "/_cat/indices?format=json&h=index&s=index", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatESError(status, body)
	}
	var items []struct {
		Index string `json:"index"`
	}
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("unexpected Elasticsearch indices response: %w", err)
	}
	names := make([]string, 0, len(items))
	for _, item := range items {
		if item.Index != "" {
			names = append(names, item.Index)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	fields, err := readMappingFields(ctx, conn, table)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = f.name
	}
	return names, nil
}

func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	c, err := esConnection(conn)
	if err != nil {
		return database.Query{}, err
	}
	if err := validateIndex(table); err != nil {
		return database.Query{}, err
	}
	status, _, err := c.do(ctx, http.MethodHead, "/"+url.PathEscape(table), nil)
	if err != nil {
		return database.Query{}, err
	}
	if status == http.StatusNotFound {
		return database.Query{}, fmt.Errorf("index %q does not exist", table)
	}
	if status < 200 || status >= 300 {
		return database.Query{}, fmt.Errorf("elasticsearch returned status %d", status)
	}
	return database.Query{
		Text:    internalMappingPrefix + table,
		Heading: "Mapping:",
	}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

const internalMappingPrefix = "__dbh_es_mapping__:"

type mappingField struct {
	name string
	typ  string
}

func readMappingFields(ctx context.Context, conn database.Connection, table string) ([]mappingField, error) {
	c, err := esConnection(conn)
	if err != nil {
		return nil, err
	}
	if err := validateIndex(table); err != nil {
		return nil, err
	}
	status, body, err := c.do(ctx, http.MethodGet, "/"+url.PathEscape(table)+"/_mapping", nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		return nil, fmt.Errorf("index %q does not exist", table)
	}
	if status < 200 || status >= 300 {
		return nil, formatESError(status, body)
	}
	var raw map[string]struct {
		Mappings struct {
			Properties map[string]any `json:"properties"`
		} `json:"mappings"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("unexpected Elasticsearch mapping response: %w", err)
	}
	var fields []mappingField
	for _, entry := range raw {
		collectProperties("", entry.Mappings.Properties, &fields)
		break
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].name < fields[j].name })
	return fields, nil
}

func collectProperties(prefix string, props map[string]any, out *[]mappingField) {
	for name, val := range props {
		full := name
		if prefix != "" {
			full = prefix + "." + name
		}
		m, ok := val.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := m["type"].(string)
		if nested, ok := m["properties"].(map[string]any); ok {
			if typ == "" {
				typ = "object"
			}
			*out = append(*out, mappingField{name: full, typ: typ})
			collectProperties(full, nested, out)
			continue
		}
		if typ == "" {
			typ = "object"
		}
		*out = append(*out, mappingField{name: full, typ: typ})
	}
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("elasticsearch commands do not accept SQL parameters")
	}
	if strings.HasPrefix(text, internalMappingPrefix) {
		table := strings.TrimPrefix(text, internalMappingPrefix)
		fields, err := readMappingFields(ctx, c, table)
		if err != nil {
			return nil, err
		}
		rows := make([][]any, len(fields))
		for i, f := range fields {
			rows[i] = []any{f.name, f.typ}
		}
		return newStaticRows([]string{"field", "type"}, rows), nil
	}

	method, path, reqBody, err := parseCommand(text, c.index)
	if err != nil {
		return nil, err
	}
	status, respBody, err := c.do(ctx, method, path, reqBody)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatESError(status, respBody)
	}
	if method == http.MethodHead || len(bytes.TrimSpace(respBody)) == 0 {
		return newStaticRows(nil, nil), nil
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
		req.Header.Set("Authorization", "ApiKey "+c.apiKey)
	} else if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func parseCommand(text, currentIndex string) (method, path string, body []byte, err error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return "", "", nil, fmt.Errorf("elasticsearch command cannot be empty")
	}
	fields := strings.Fields(trimmed)
	if len(fields) < 2 {
		return "", "", nil, fmt.Errorf("expected HTTP method and path (e.g. GET /_cat/indices)")
	}
	method = strings.ToUpper(fields[0])
	switch method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead, http.MethodPatch:
	default:
		return "", "", nil, fmt.Errorf("unsupported HTTP method %q (use GET, POST, PUT, DELETE, HEAD or PATCH)", fields[0])
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
				return "", "", nil, fmt.Errorf("elasticsearch request body must be valid JSON")
			}
			body = []byte(rawBody)
		}
	}
	if !strings.HasPrefix(path, "/") {
		if currentIndex != "" {
			path = "/" + url.PathEscape(currentIndex) + "/" + path
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

	switch v := parsed.(type) {
	case []any:
		return rowsFromJSONArray(v)
	case map[string]any:
		// Search response: hits.hits
		if hitsObj, ok := v["hits"].(map[string]any); ok {
			if hitsArr, ok := hitsObj["hits"].([]any); ok {
				return rowsFromSearchHits(hitsArr)
			}
		}
		// ES SQL response: columns + rows
		if colsRaw, ok := v["columns"].([]any); ok {
			if rowsRaw, ok := v["rows"].([]any); ok {
				return rowsFromESSQL(colsRaw, rowsRaw)
			}
		}
		compact, _ := json.Marshal(v)
		return &staticRows{
			cols: []string{"document"},
			rows: []database.Row{{Values: []any{string(compact)}, Object: v}},
		}
	default:
		return newStaticRows([]string{"value"}, [][]any{{v}})
	}
}

func rowsFromSearchHits(hits []any) database.Rows {
	cols := []string{"_index", "_id", "_score", "_source"}
	rows := make([]database.Row, 0, len(hits))
	for _, h := range hits {
		m, ok := h.(map[string]any)
		if !ok {
			continue
		}
		srcBytes, _ := json.Marshal(m["_source"])
		rows = append(rows, database.Row{
			Values: []any{m["_index"], m["_id"], m["_score"], string(srcBytes)},
			Object: m,
		})
	}
	return &staticRows{cols: cols, rows: rows}
}

func rowsFromESSQL(colsRaw, rowsRaw []any) database.Rows {
	cols := make([]string, len(colsRaw))
	for i, c := range colsRaw {
		if m, ok := c.(map[string]any); ok {
			cols[i], _ = m["name"].(string)
		}
	}
	rows := make([]database.Row, 0, len(rowsRaw))
	for _, r := range rowsRaw {
		arr, ok := r.([]any)
		if !ok {
			continue
		}
		vals := make([]any, len(cols))
		for i := 0; i < len(cols) && i < len(arr); i++ {
			vals[i] = arr[i]
		}
		rows = append(rows, database.NewRow(cols, vals))
	}
	return &staticRows{cols: cols, rows: rows}
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

func formatESError(status int, body []byte) error {
	var errResp struct {
		Error struct {
			Type   string `json:"type"`
			Reason string `json:"reason"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &errResp) == nil && (errResp.Error.Reason != "" || errResp.Error.Type != "") {
		if errResp.Error.Type != "" && errResp.Error.Reason != "" {
			return fmt.Errorf("elasticsearch %d (%s): %s", status, errResp.Error.Type, errResp.Error.Reason)
		}
		return fmt.Errorf("elasticsearch %d: %s%s", status, errResp.Error.Type, errResp.Error.Reason)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("elasticsearch returned status %d: %s", status, msg)
	}
	return fmt.Errorf("elasticsearch returned status %d", status)
}

func validateIndex(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n?#\\") || strings.Contains(name, "..") {
		return fmt.Errorf("invalid Elasticsearch index name")
	}
	return nil
}

func esConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected an Elasticsearch connection")
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
