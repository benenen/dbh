//go:build e2e

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMongoDatabaseConnection(t *testing.T) {
	dsn := os.Getenv("DBH_TEST_MONGO_DSN")
	if dsn == "" {
		t.Skip("set DBH_TEST_MONGO_DSN to run")
	}
	f := newFixture(t)
	f.env["DBH_E2E_DSN"] = dsn
	f.run("new", "mongo", "--driver", "mongo", "--dsn-env", "DBH_E2E_DSN")
	contains(t, f.run("ls"), "mongo")
	collection := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
	command := func(text string, args ...string) string {
		return f.run(append([]string{"connect", "mongo", "--sql", text, "--no-history"}, args...)...)
	}
	command(fmt.Sprintf(`{"create":%q}`, collection), "--format", "json")
	t.Cleanup(func() { command(fmt.Sprintf(`{"drop":%q}`, collection), "--format", "json") })
	output := command(fmt.Sprintf(`{"insert":%q,"documents":[{"_id":1,"name":"你好;dbh","note":null,"nested":{"tags":["a","b"]}},{"_id":2,"name":"second","extra":{"$numberLong":"9007199254740993"}},{"_id":3,"name":"third"}]}`, collection), "--format", "json")
	contains(t, output, `"n":3`)
	f.fail("c", "mongo", "--sql", fmt.Sprintf(`{"insert":%q,"documents":[{"_id":1}]};{"ping":1};`, collection), "--format", "json", "--no-history")
	query := fmt.Sprintf(`{"find":%q,"filter":{},"sort":{"_id":1},"batchSize":1}`, collection)
	output = command(query, "--format", "json")
	lines := strings.Split(strings.TrimSpace(output), "\n")
	equal(t, len(lines), 3)
	var first map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &first); err != nil {
		t.Fatal(err)
	}
	equal(t, first, map[string]any{"_id": float64(1), "name": "你好;dbh", "note": nil, "nested": map[string]any{"tags": []any{"a", "b"}}})
	contains(t, lines[1], `"extra":9007199254740993`)
	contains(t, command(query), "(3 rows)")
	contains(t, command(query), "┌")
	equal(t, f.run("c", "mongo", "--sql", query, "--format", "json", "--no-history"), output)
	file := filepath.Join(f.root, "mongo.json")
	writeFile(t, file, query)
	equal(t, f.run("c", "mongo", "--file", file, "--format", "json", "--no-history"), output)
	equal(t, f.invoke(query, true, "c", "mongo", "--format", "json", "--no-history").stdout, output)
	csv := command(query, "--format", "csv")
	contains(t, csv, "document\n")
	contains(t, csv, "你好;dbh")
	contains(t, command(fmt.Sprintf(`{"update":%q,"updates":[{"q":{"_id":2},"u":{"$set":{"name":"updated"}}}]}`, collection), "--format", "json"), `"n":1`)
	contains(t, command(fmt.Sprintf(`{"delete":%q,"deletes":[{"q":{"_id":3},"limit":1}]}`, collection), "--format", "json"), `"n":1`)
	contains(t, command(fmt.Sprintf(`{"count":%q}`, collection), "--format", "json"), `"n":2`)
	f.fail("c", "mongo", "--sql", `{"dbh_invalid_command":1};{"ping":1};`, "--format", "json", "--no-history")
	f.fail("c", "mongo", "--sql", `not a JSON command`, "--format", "json", "--no-history")
	f.run("remove", "mongo")
	f.fail("c", "mongo", "--sql", `{"ping":1}`)
	f.run("n", "mongo", "--driver", "mongo", "--dsn-env", "DBH_E2E_DSN")
	contains(t, command(fmt.Sprintf(`{"count":%q}`, collection), "--format", "json"), `"n":2`)
	absent(t, filepath.Join(f.config, "history", "mongo.jsonl"))
}
