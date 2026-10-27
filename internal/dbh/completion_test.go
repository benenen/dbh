package dbh

import (
	"reflect"
	"testing"
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
	for _, driver := range []string{"sqlite", "postgres", "mysql", "clickhouse"} {
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
		{"redis", "GET #sel", true}, {"mongo", `{"find":"sel`, true},
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
		for _, driver := range []string{"sqlite", "postgres", "mysql", "clickhouse", "redis", "mongo"} {
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
