// Package influxdb implements InfluxDB v2 Flux queries, Line Protocol writes, and bucket metadata.
package influxdb

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/csv"
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
	org     string
	token   string
	bucket  string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid influxdb connection URI")
	}

	q := parsed.Query()
	org := strings.TrimSpace(q.Get("org"))
	token := strings.TrimSpace(q.Get("token"))
	insecure := strings.EqualFold(q.Get("insecure"), "true") || q.Get("insecure") == "1"
	q.Del("org")
	q.Del("token")
	q.Del("insecure")
	parsed.RawQuery = q.Encode()
	parsed.User = nil

	bucket := strings.Trim(parsed.Path, "/")
	if bucket != "" {
		if err := validateName(bucket); err != nil {
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
		org:     org,
		token:   token,
		bucket:  bucket,
	}

	status, body, err := conn.do(ctx, http.MethodGet, "/health", "", "", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, body)
	}
	if token != "" {
		path := "/api/v2/buckets"
		if org != "" {
			path += "?org=" + url.QueryEscape(org)
		}
		st, b, err := conn.do(ctx, http.MethodGet, path, "", "", nil)
		if err != nil {
			return nil, err
		}
		if st < 200 || st >= 300 {
			return nil, formatHTTPError(st, b)
		}
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		BackslashEscapes: true,
		CompletionWords: strings.Fields(
			`from range filter map group count mean sum sort limit yield schema.measurements schema.measurementFieldKeys schema.measurementTagKeys WRITE`,
		),
	}
}

func (Driver) SwitchDatabase(_ context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := influxConnection(conn)
	if err != nil {
		return nil, err
	}
	c.bucket = name
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := influxConnection(conn)
	if err != nil {
		return "", err
	}
	return c.bucket, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := influxConnection(conn)
	if err != nil {
		return nil, err
	}
	path := "/api/v2/buckets"
	if c.org != "" {
		path += "?org=" + url.QueryEscape(c.org)
	}
	status, body, err := c.do(ctx, http.MethodGet, path, "", "", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, body)
	}
	var resp struct {
		Buckets []struct {
			Name string `json:"name"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("unexpected influxdb buckets response: %w", err)
	}
	names := make([]string, 0, len(resp.Buckets))
	for _, b := range resp.Buckets {
		if b.Name != "" && !strings.HasPrefix(b.Name, "_") {
			names = append(names, b.Name)
		}
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := influxConnection(conn)
	if err != nil {
		return nil, err
	}
	if c.bucket == "" {
		return nil, fmt.Errorf("no bucket selected; pass bucket in DSN path or use --db")
	}
	flux := fmt.Sprintf("import \"influxdata/influxdb/schema\"\nschema.measurements(bucket: %q)", c.bucket)
	names, err := fluxColumnValues(ctx, conn, flux, "_value")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	c, err := influxConnection(conn)
	if err != nil {
		return nil, err
	}
	if c.bucket == "" {
		return nil, fmt.Errorf("no bucket selected; pass bucket in DSN path or use --db")
	}
	flux := fmt.Sprintf("import \"influxdata/influxdb/schema\"\nschema.measurementFieldKeys(bucket: %q, measurement: %q)", c.bucket, table)
	return fluxColumnValues(ctx, conn, flux, "_value")
}

func fluxColumnValues(ctx context.Context, conn database.Connection, flux, colName string) ([]string, error) {
	rows, err := conn.Query(ctx, flux)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(cols) == 0 {
		return nil, nil
	}
	targetIdx := len(cols) - 1
	for i, c := range cols {
		if c == colName {
			targetIdx = i
			break
		}
	}
	var out []string
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			return nil, err
		}
		if targetIdx < len(row.Values) && row.Values[targetIdx] != nil {
			if s := strings.TrimSpace(fmt.Sprint(row.Values[targetIdx])); s != "" {
				out = append(out, s)
			}
		}
	}
	return out, rows.Err()
}

func (Driver) ColumnDetails(_ context.Context, conn database.Connection, table string) (database.Query, error) {
	c, err := influxConnection(conn)
	if err != nil {
		return database.Query{}, err
	}
	if c.bucket == "" {
		return database.Query{}, fmt.Errorf("no bucket selected; pass bucket in DSN path or use --db")
	}
	flux := fmt.Sprintf("import \"influxdata/influxdb/schema\"\nschema.measurementFieldKeys(bucket: %q, measurement: %q)", c.bucket, table)
	return database.Query{Text: flux, Heading: "Fields:"}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("influxdb commands do not accept SQL parameters")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("influxdb command cannot be empty")
	}
	if len(trimmed) >= 6 && strings.EqualFold(trimmed[:6], "WRITE ") {
		if c.bucket == "" {
			return nil, fmt.Errorf("no bucket selected; pass bucket in DSN path or use --db")
		}
		lineProto := strings.TrimSpace(trimmed[6:])
		q := url.Values{}
		if c.org != "" {
			q.Set("org", c.org)
		}
		q.Set("bucket", c.bucket)
		path := "/api/v2/write?" + q.Encode()
		status, body, err := c.do(ctx, http.MethodPost, path, "text/plain; charset=utf-8", "application/json", []byte(lineProto))
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, formatHTTPError(status, body)
		}
		return newStaticRows([]string{"status"}, [][]any{{"ok"}}), nil
	}

	path := "/api/v2/query"
	if c.org != "" {
		path += "?org=" + url.QueryEscape(c.org)
	}
	status, body, err := c.do(ctx, http.MethodPost, path, "application/vnd.flux", "application/csv", []byte(trimmed))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, formatHTTPError(status, body)
	}
	return parseAnnotatedCSV(body)
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

func (c *connection) do(ctx context.Context, method, path, contentType, accept string, body []byte) (int, []byte, error) {
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
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Token "+c.token)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	return resp.StatusCode, data, err
}

func parseAnnotatedCSV(body []byte) (database.Rows, error) {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return newStaticRows(nil, nil), nil
	}
	r := csv.NewReader(strings.NewReader(trimmed))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("unexpected influxdb csv response: %w", err)
	}

	var cols []string
	var values [][]any
	for _, rec := range records {
		if len(rec) == 0 || strings.HasPrefix(rec[0], "#") {
			continue
		}
		if len(rec) == 1 && rec[0] == "" {
			continue
		}
		// Skip leading empty annotation column and "result"/"table" metadata if repeating header.
		start := 0
		if rec[0] == "" {
			start = 1
		}
		row := rec[start:]
		if cols == nil {
			cols = row
			continue
		}
		// Repeated header in multi-table Flux output
		if len(row) == len(cols) && strings.Join(row, "\x00") == strings.Join(cols, "\x00") {
			continue
		}
		vals := make([]any, len(cols))
		for i := 0; i < len(cols) && i < len(row); i++ {
			vals[i] = row[i]
		}
		values = append(values, vals)
	}
	return newStaticRows(cols, values), nil
}

func formatHTTPError(status int, body []byte) error {
	var errResp struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(body, &errResp) == nil && errResp.Message != "" {
		return fmt.Errorf("influxdb returned status %d: %s", status, errResp.Message)
	}
	msg := strings.TrimSpace(string(body))
	if msg != "" {
		return fmt.Errorf("influxdb returned status %d: %s", status, msg)
	}
	return fmt.Errorf("influxdb returned status %d", status)
}

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/\\?#") {
		return fmt.Errorf("invalid influxdb bucket %q", name)
	}
	return nil
}

func influxConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected an influxdb connection")
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
