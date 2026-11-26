// Package tdengine implements TDengine RESTful (/rest/sql) SQL execution and metadata.
package tdengine

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
	baseURL  *url.URL
	client   *http.Client
	user     string
	pass     string
	database string
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid tdengine connection URI")
	}

	q := parsed.Query()
	insecure := strings.EqualFold(q.Get("insecure"), "true") || q.Get("insecure") == "1"
	q.Del("insecure")
	parsed.RawQuery = q.Encode()

	var user, pass string
	if parsed.User != nil {
		user = parsed.User.Username()
		pass, _ = parsed.User.Password()
		parsed.User = nil
	}

	dbName := strings.Trim(parsed.Path, "/")
	if dbName != "" {
		if err := validateName(dbName); err != nil {
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
		baseURL:  parsed,
		client:   &http.Client{Transport: transport},
		user:     user,
		pass:     pass,
		database: dbName,
	}
	if err := conn.Exec(ctx, "SELECT SERVER_VERSION()"); err != nil {
		return nil, err
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{BackslashEscapes: true}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := tdConnection(conn)
	if err != nil {
		return nil, err
	}
	prev := c.database
	c.database = name
	if err := c.Exec(ctx, "SHOW TABLES"); err != nil {
		c.database = prev
		return nil, err
	}
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := tdConnection(conn)
	if err != nil {
		return "", err
	}
	return c.database, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	names, err := firstColumnValues(ctx, conn, "SHOW DATABASES")
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, q := range []string{"SHOW STABLES", "SHOW TABLES"} {
		names, err := firstColumnValues(ctx, conn, q)
		if err != nil {
			continue
		}
		for _, n := range names {
			if !seen[n] {
				seen[n] = true
				out = append(out, n)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	if err := validateName(table); err != nil {
		return nil, err
	}
	return firstColumnValues(ctx, conn, "DESCRIBE "+quoteIdent(table))
}

func firstColumnValues(ctx context.Context, conn database.Connection, sqlText string) ([]string, error) {
	rows, err := conn.Query(ctx, sqlText)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			return nil, err
		}
		if len(row.Values) > 0 && row.Values[0] != nil {
			if s := strings.TrimSpace(fmt.Sprint(row.Values[0])); s != "" {
				out = append(out, s)
			}
		}
	}
	return out, rows.Err()
}

func (Driver) ColumnDetails(_ context.Context, _ database.Connection, table string) (database.Query, error) {
	if err := validateName(table); err != nil {
		return database.Query{}, err
	}
	return database.Query{Text: "DESCRIBE " + quoteIdent(table), Heading: "Columns:"}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("tdengine REST commands do not accept SQL parameters")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("tdengine statement cannot be empty")
	}
	path := "/rest/sql"
	if c.database != "" {
		path = "/rest/sql/" + url.PathEscape(c.database)
	}
	status, respBody, err := c.do(ctx, http.MethodPost, path, []byte(trimmed))
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("tdengine returned status %d: %s", status, strings.TrimSpace(string(respBody)))
	}

	var resp struct {
		Code       int     `json:"code"`
		Desc       string  `json:"desc"`
		ColumnMeta [][]any `json:"column_meta"`
		Data       [][]any `json:"data"`
	}
	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("unexpected tdengine response: %w", err)
	}
	if resp.Code != 0 {
		return nil, fmt.Errorf("tdengine error %d: %s", resp.Code, resp.Desc)
	}
	cols := make([]string, len(resp.ColumnMeta))
	for i, meta := range resp.ColumnMeta {
		if len(meta) > 0 {
			cols[i] = fmt.Sprint(meta[0])
		}
	}
	return newStaticRows(cols, resp.Data), nil
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
	target.Path = path
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return 0, nil, err
	}
	if c.user != "" || c.pass != "" {
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

func quoteIdent(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/\\?#") {
		return fmt.Errorf("invalid tdengine identifier %q", name)
	}
	return nil
}

func tdConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a tdengine connection")
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
		row := make([]any, len(cols))
		for j := 0; j < len(cols) && j < len(v); j++ {
			row[j] = v[j]
		}
		rows[i] = database.NewRow(cols, row)
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
