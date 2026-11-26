// Package neo4j implements Neo4j HTTP transactional Cypher commands and graph metadata.
package neo4j

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
		return nil, fmt.Errorf("invalid neo4j connection URI")
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
	if dbName == "" {
		dbName = "neo4j"
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
		baseURL:  parsed,
		client:   &http.Client{Transport: transport},
		user:     user,
		pass:     pass,
		database: dbName,
	}
	if err := conn.Exec(ctx, "RETURN 1 AS ok"); err != nil {
		return nil, err
	}
	return conn, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{
		BackslashEscapes: true,
		SlashComments:    true,
		HelpText:         "Neo4j accepts Cypher statements ending with ;. Press Enter when complete.\n",
		CompletionWords: strings.Fields(
			"MATCH OPTIONAL MATCH WHERE RETURN ORDER BY LIMIT SKIP CREATE MERGE DELETE DETACH DELETE SET REMOVE UNWIND CALL YIELD SHOW DATABASES SHOW INDEXES",
		),
	}
}

func (Driver) SwitchDatabase(ctx context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := neo4jConnection(conn)
	if err != nil {
		return nil, err
	}
	prev := c.database
	c.database = name
	if err := c.Exec(ctx, "RETURN 1 AS ok"); err != nil {
		c.database = prev
		return nil, err
	}
	return nil, nil
}

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := neo4jConnection(conn)
	if err != nil {
		return "", err
	}
	return c.database, nil
}

func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := neo4jConnection(conn)
	if err != nil {
		return nil, err
	}
	names, err := database.Names(ctx, conn, database.Query{Text: "SHOW DATABASES YIELD name RETURN name ORDER BY name"})
	if err != nil {
		return []string{c.database}, nil
	}
	return names, nil
}

func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	names, err := database.Names(ctx, conn, database.Query{Text: "CALL db.labels() YIELD label RETURN label ORDER BY label"})
	if err != nil {
		return nil, err
	}
	sort.Strings(names)
	return names, nil
}

func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	if err := validateName(table); err != nil {
		return nil, err
	}
	escaped := strings.ReplaceAll(table, "`", "``")
	return database.Names(ctx, conn, database.Query{
		Text: fmt.Sprintf("MATCH (n:`%s`) UNWIND keys(n) AS key RETURN DISTINCT key ORDER BY key LIMIT 100", escaped),
	})
}

func (Driver) ColumnDetails(_ context.Context, _ database.Connection, table string) (database.Query, error) {
	if err := validateName(table); err != nil {
		return database.Query{}, err
	}
	escaped := strings.ReplaceAll(table, "`", "``")
	return database.Query{
		Text:    fmt.Sprintf("MATCH (n:`%s`) UNWIND keys(n) AS property RETURN DISTINCT property ORDER BY property LIMIT 100", escaped),
		Heading: "Properties:",
	}, nil
}

func (Driver) Indexes(_ context.Context, _ database.Connection, _ string) (database.Query, error) {
	return database.Query{
		Text:    "SHOW INDEXES YIELD name, type, entityType, labelsOrTypes, properties, state RETURN name, type, entityType, labelsOrTypes, properties, state ORDER BY name",
		Heading: "Indexes:",
	}, nil
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("neo4j commands do not accept SQL parameters")
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return nil, fmt.Errorf("neo4j statement cannot be empty")
	}
	payload := map[string]any{
		"statements": []map[string]any{
			{"statement": trimmed},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/db/%s/tx/commit", url.PathEscape(c.database))
	status, respBody, err := c.do(ctx, http.MethodPost, path, body)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("neo4j returned status %d: %s", status, strings.TrimSpace(string(respBody)))
	}

	var resp struct {
		Results []struct {
			Columns []string `json:"columns"`
			Data    []struct {
				Row []any `json:"row"`
			} `json:"data"`
		} `json:"results"`
		Errors []struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	dec := json.NewDecoder(bytes.NewReader(respBody))
	dec.UseNumber()
	if err := dec.Decode(&resp); err != nil {
		return nil, fmt.Errorf("unexpected neo4j response: %w", err)
	}
	if len(resp.Errors) > 0 {
		e := resp.Errors[0]
		return nil, fmt.Errorf("neo4j %s: %s", e.Code, e.Message)
	}
	if len(resp.Results) == 0 {
		return newStaticRows(nil, nil), nil
	}
	res := resp.Results[0]
	values := make([][]any, len(res.Data))
	for i, d := range res.Data {
		row := make([]any, len(res.Columns))
		for j := 0; j < len(res.Columns) && j < len(d.Row); j++ {
			row[j] = normalizeCell(d.Row[j])
		}
		values[i] = row
	}
	return newStaticRows(res.Columns, values), nil
}

func normalizeCell(v any) any {
	switch val := v.(type) {
	case map[string]any, []any:
		b, err := json.Marshal(val)
		if err == nil {
			return string(b)
		}
	}
	return v
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
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

func validateName(name string) error {
	if name == "" || strings.ContainsAny(name, " \t\r\n/\\?#") {
		return fmt.Errorf("invalid neo4j identifier %q", name)
	}
	return nil
}

func neo4jConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a neo4j connection")
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
