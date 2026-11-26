package dbh

import (
	"reflect"
	"testing"

	"github.com/benenen/dbh/internal/database"
)

func TestCompletionRanking(t *testing.T) {
	words := []string{"apple", "platform", "PLan", "sample", "用户名称", "名称"}
	for _, tt := range []struct {
		text string
		want []string
	}{
		{"pl", []string{"platform", "PLan", "apple", "sample"}},
		{"PL", []string{"platform", "PLan", "apple", "sample"}},
		{"名称", []string{"名称", "用户名称"}},
		{"", words},
		{"missing", []string{}},
	} {
		t.Run(tt.text, func(t *testing.T) {
			before := append([]string(nil), words...)
			if got := matching(words, tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
			if !reflect.DeepEqual(words, before) {
				t.Fatal("ranking mutated the schema")
			}
		})
	}
}

func TestCompletionCommandArgumentsAndCursor(t *testing.T) {
	c := &completer{words: []string{"SELECT", "table_column", "名称"}, tables: []string{"users"}}
	for _, tt := range []struct {
		input  string
		pos    int
		prefix string
		want   []string
	}{
		{`\format `, 8, "", []string{"table", "csv", "json", "tsv"}},
		{`\format T`, 9, "T", []string{"table", "tsv"}},
		{`\format SV`, 10, "SV", []string{"csv", "tsv"}},
		{"SELECT 1\n  \\format\tj", 20, "j", []string{"json"}},
		{`\format csv SELECT`, 17, "", nil},
		{`\describe users SE`, 17, "", nil},
		{`\help SE`, 8, "", nil},
		{"SELECT 名", 8, "名", []string{"名称"}},
		{"SELECT na", 2, "SE", []string{"SELECT"}},
		{"SELECT", -1, "", nil},
		{"SELECT", 7, "", nil},
		{"", 0, "", nil},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, prefix := c.candidates([]rune(tt.input), tt.pos)
			if !reflect.DeepEqual(got, tt.want) || prefix != tt.prefix {
				t.Fatalf("got %q / %q, want %q / %q", got, prefix, tt.want, tt.prefix)
			}
		})
	}
}

func TestCompletionLexicalContext(t *testing.T) {
	for _, driver := range []string{"sqlite", "postgres", "mysql", "clickhouse", "mssql", "oracle", "trino", "dm"} {
		for _, tt := range []struct {
			input string
			want  bool
		}{
			{"sel", true}, {"SELECT 1; sel", true}, {"SELECT '--' AS sel", true},
			{"-- sel", false}, {"SELECT 1; -- sel", false}, {"-- sel\nsel", true},
			{"SELECT 'sel", false}, {"SELECT 'it''s sel", false},
			{"SELECT /* sel", false}, {"SELECT /* sel */ sel", true},
			{"SELECT \"sel", false}, {"SELECT `sel", false},
			{`\format j`, true},
		} {
			t.Run(driver+"/"+tt.input, func(t *testing.T) {
				if got := completionAllowed(tt.input, driver); got != tt.want {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			})
		}
	}
	for _, tt := range []struct {
		driver, input string
		want          bool
	}{
		{"mysql", "SELECT 1 # sel", false}, {"mysql", "# sel\nsel", true},
		{"mysql", "SELECT 1--sel", true}, {"postgres", "SELECT 1--sel", false},
		{"postgres", "SELECT $$ sel", false}, {"postgres", "SELECT $tag$ sel", false},
		{"postgres", "SELECT $$x$$ AS sel", true},
		{"postgres", "/* outer /* inner */ sel", false},
		{"sqlite", "SELECT [sel", false},
		{"redis", "GET sel", true}, {"redis", "GET 'sel", false},
		{"redis", "GET #sel", true}, {"mongo", `{"find":"sel`, true}, {"elasticsearch", `GET /_cat/sel`, true}, {"milvus", `POST collections/sel`, true}, {"qdrant", `GET /collections/sel`, true},
		{"etcd", "GET sel", true}, {"bbolt", "BUCKETS sel", true}, {"openobserve", "SELECT sel", true},
		{"mssql", "SELECT [sel", false},
		{"missing", "sel", false},
	} {
		t.Run(tt.driver+"/"+tt.input, func(t *testing.T) {
			if got := completionAllowed(tt.input, tt.driver); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func FuzzCompletionCandidates(f *testing.F) {
	for _, input := range []string{"SELECT 名", `\format j`, "-- sel", "", "SELECT 'x"} {
		f.Add(input, 2)
	}
	c := &completer{words: []string{"SELECT", "名称"}}
	f.Fuzz(func(t *testing.T, input string, pos int) {
		line := []rune(input)
		_, prefix := c.candidates(line, pos)
		if (pos < 0 || pos > len(line)) && prefix != "" {
			t.Fatal("invalid cursor returned a prefix")
		}
		for _, driver := range []string{"sqlite", "postgres", "mysql", "clickhouse", "redis", "mongo", "mssql", "oracle", "trino", "dm", "elasticsearch", "milvus", "qdrant", "neo4j", "influxdb", "tdengine", "cassandra", "etcd", "bbolt", "openobserve"} {
			completionAllowed(input, driver)
		}
	})
}

func BenchmarkCompletionMatching(b *testing.B) {
	words := make([]string, 10000)
	for i := range words {
		words[i] = "customer_order_history"
	}
	b.ReportAllocs()
	for b.Loop() {
		matching(words, "order")
	}
}

func TestSQLCompletionContext(t *testing.T) {
	c := &completer{words: []string{"SELECT", "UPDATE", "users", "name"}, tables: []string{"orders", "users"}, columns: map[string][]string{"users": {"id", "name"}, "orders": {"order_id"}}}
	for _, tt := range []struct {
		sql  string
		pos  int
		want []string
	}{
		{"SELECT * FROM ", -1, []string{"orders", "users"}},
		{"SELECT * FROM us", -1, []string{"users"}},
		{"SELECT * FROM users u JOIN ", -1, []string{"orders", "users"}},
		{"SELECT * FROM users, ", -1, []string{"orders", "users"}},
		{"SELECT u. FROM users AS u", 9, []string{"u.id", "u.name"}},
		{"SELECT u.na FROM users u", 11, []string{"u.name"}},
		{"SELECT * FROM users u JOIN orders o ON o.", -1, []string{"o.order_id"}},
		{"SELECT 'FROM orders o' AS text FROM users u WHERE o.", -1, []string{}},
		{"SELECT * FROM users u /* JOIN orders o */ WHERE u.", -1, []string{"u.id", "u.name"}},
		{"SELECT * FROM users WHERE id IN (SELECT o. FROM orders o)", len("SELECT * FROM users WHERE id IN (SELECT o."), []string{"o.order_id"}},
	} {
		pos := tt.pos
		if pos < 0 {
			pos = len([]rune(tt.sql))
		}
		got, _ := c.candidates([]rune(tt.sql), pos)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q at %d: %q, want %q", tt.sql, pos, got, tt.want)
		}
	}
}

func TestCompletionQuotesSpecialIdentifiers(t *testing.T) {
	c := &completer{tables: []string{"order items", "order"}}
	got, _ := c.candidates([]rune("SELECT * FROM ord"), len("SELECT * FROM ord"))
	if !reflect.DeepEqual(got, []string{`"order items"`, `"order"`}) {
		t.Fatalf("unsafe identifiers: %q", got)
	}
}

func TestCompletionPathsAndSnippets(t *testing.T) {
	httpCompleter := &completer{
		words:  []string{"GET", "POST", "/collections", "points/scroll", "_cat/indices"},
		syntax: database.Syntax{HTTPCommands: true},
	}
	if got, prefix := httpCompleter.candidates([]rune("GET /col"), len("GET /col")); !reflect.DeepEqual(got, []string{"/collections"}) || prefix != "/col" {
		t.Fatalf("http path completion: got=%v prefix=%q", got, prefix)
	}
	if got, prefix := httpCompleter.candidates([]rune("POST points/"), len("POST points/")); !reflect.DeepEqual(got, []string{"points/scroll"}) || prefix != "points/" {
		t.Fatalf("http subpath completion: got=%v prefix=%q", got, prefix)
	}

	lineCompleter := &completer{
		words:    []string{"GET", "PUT", "/app/name"},
		snippets: []string{"daily_report", "user_by_id"},
		syntax:   database.Syntax{LineCommands: true},
	}
	if got, prefix := lineCompleter.candidates([]rune("GET /app/"), len("GET /app/")); !reflect.DeepEqual(got, []string{"/app/name"}) || prefix != "/app/" {
		t.Fatalf("line path completion: got=%v prefix=%q", got, prefix)
	}
	if got, prefix := lineCompleter.candidates([]rune(`\run user`), len(`\run user`)); !reflect.DeepEqual(got, []string{"user_by_id"}) || prefix != "user" {
		t.Fatalf("snippet completion: got=%v prefix=%q", got, prefix)
	}
	if got, prefix := lineCompleter.candidates([]rune(`\limit 5`), len(`\limit 5`)); !reflect.DeepEqual(got, []string{"500", "5000"}) || prefix != "5" {
		t.Fatalf("limit completion: got=%v prefix=%q", got, prefix)
	}
}

func TestMatchFilterAndListFiltering(t *testing.T) {
	items := []string{
		"resource_asset_current",
		"resource_asset_history",
		"users",
		"dbo.order[1]",
		"用户表",
	}
	if got := filterList(items, ""); !reflect.DeepEqual(got, items) {
		t.Fatalf("empty filter = %v, want %v", got, items)
	}
	if got := filterList(items, "ASSET"); !reflect.DeepEqual(got, []string{"resource_asset_current", "resource_asset_history"}) {
		t.Fatalf("substring filter = %v", got)
	}
	if got := filterList(items, "resource_*_current"); !reflect.DeepEqual(got, []string{"resource_asset_current"}) {
		t.Fatalf("wildcard * filter = %v", got)
	}
	if got := filterList(items, "user?"); !reflect.DeepEqual(got, []string{"users"}) {
		t.Fatalf("wildcard ? filter = %v", got)
	}
	if !matchFilter("dbo.order[1]", "*order[1]") {
		t.Fatal("expected wildcard match with brackets and dots")
	}
	if !matchFilter("SELECT *\nFROM users;", "*from*users*") {
		t.Fatal("expected wildcard match across newlines")
	}
	if !matchFilter("用户表", "用户") {
		t.Fatal("expected Unicode substring match")
	}

	c := completer{
		databases: []string{"analytics", "app_db"},
		tables:    []string{"resource_asset_current", "users"},
	}
	if got, prefix := c.candidates([]rune(`\tables res`), len(`\tables res`)); prefix != "res" || !reflect.DeepEqual(got, []string{"resource_asset_current"}) {
		t.Fatalf("\\tables candidates = (%v, %q)", got, prefix)
	}
	if got, prefix := c.candidates([]rune(`\database app`), len(`\database app`)); prefix != "app" || !reflect.DeepEqual(got, []string{"app_db"}) {
		t.Fatalf("\\database candidates = (%v, %q)", got, prefix)
	}
}
