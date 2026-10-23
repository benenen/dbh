//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRedisCommands(t *testing.T) {
	dsn := os.Getenv("DBH_TEST_REDIS_DSN")
	if dsn == "" {
		t.Skip("set DBH_TEST_REDIS_DSN to run")
	}
	f := newFixture(t)
	f.env["DBH_E2E_DSN"] = dsn
	f.run("new", "redis", "--driver", "redis", "--dsn-env", "DBH_E2E_DSN")
	prefix := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
	command := func(text string, args ...string) string {
		return f.run(append([]string{"exec", "redis", text, "--no-history"}, args...)...)
	}
	t.Cleanup(func() { command(fmt.Sprintf("DEL %[1]s:s %[1]s:h %[1]s:l", prefix)) })
	equal(t, command(fmt.Sprintf(`SET %s:s "你好 dbh;"`, prefix), "--format", "json"), "{\"value\":\"OK\"}\n")
	equal(t, command(fmt.Sprintf("GET %s:s", prefix), "--format", "csv"), "value\n你好 dbh;\n")
	equal(t, command(fmt.Sprintf("GET %s:missing", prefix), "--format", "json"), "{\"value\":null}\n")
	// One command per line; quotes keep spaces and escapes.
	batch := fmt.Sprintf("HSET %[1]s:h b \"x\\ty\" a 1\nRPUSH %[1]s:l one 'two three'\nLRANGE %[1]s:l 0 -1\n", prefix)
	output := f.invoke(batch, true, "c", "redis", "--format", "json", "--no-history").stdout
	equal(t, output, "{\"value\":2}\n{\"value\":2}\n{\"value\":\"one\"}\n{\"value\":\"two three\"}\n")
	equal(t, command(fmt.Sprintf("HGETALL %s:h", prefix), "--format", "json"), "{\"field\":\"a\",\"value\":\"1\"}\n{\"field\":\"b\",\"value\":\"x\\ty\"}\n")
	contains(t, command(fmt.Sprintf("HGETALL %s:h", prefix)), "(2 rows)")
	file := filepath.Join(f.root, "redis.txt")
	writeFile(t, file, fmt.Sprintf("LLEN %s:l\n\nSTRLEN %s:s", prefix, prefix))
	equal(t, f.run("c", "redis", "--file", file, "--format", "csv", "--no-history"), "value\n2\nvalue\n11\n")
	// Errors stop the batch with a non-zero status.
	f.fail("exec", "redis", fmt.Sprintf("DBH_INVALID_COMMAND\nDEL %s:s", prefix), "--no-history")
	equal(t, command(fmt.Sprintf("EXISTS %s:s", prefix), "--format", "csv"), "value\n1\n")
	f.fail("exec", "redis", `GET "unterminated`, "--no-history")
	f.fail("exec", "redis", "--db", "not-a-number", "PING", "--no-history")
	command(fmt.Sprintf("SET %s:db1 x", prefix), "--db", "1")
	t.Cleanup(func() { command(fmt.Sprintf("DEL %s:db1", prefix), "--db", "1") })
	equal(t, command(fmt.Sprintf("EXISTS %s:db1", prefix), "--format", "csv"), "value\n0\n")
	equal(t, command(fmt.Sprintf("EXISTS %s:db1", prefix), "--db", "1", "--format", "csv"), "value\n1\n")
	f.run("exec", "redis", "PING")
	history := f.run("history", "redis")
	contains(t, history, "PING")
	if strings.Contains(history, "PING;") {
		t.Fatalf("Redis history must not add a SQL terminator:\n%s", history)
	}
}
