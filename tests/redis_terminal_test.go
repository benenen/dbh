//go:build e2e && (linux || darwin)

package e2e

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRedisTerminalCommands(t *testing.T) {
	dsn := os.Getenv("DBH_TEST_REDIS_DSN")
	if dsn == "" {
		t.Skip("set DBH_TEST_REDIS_DSN to run")
	}
	f := newFixture(t)
	f.env["DBH_E2E_DSN"] = dsn
	f.run("new", "redis", "--driver", "redis", "--dsn-env", "DBH_E2E_DSN")
	key := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
	f.run("exec", "redis", fmt.Sprintf("RPUSH %s a b c", key), "--no-history")
	t.Cleanup(func() { f.run("exec", "redis", "DEL "+key+" "+key+":q", "--no-history") })
	term := f.openTerminal("redis", "--no-history")
	term.exchange("HGETA", "HGETALL")
	term.exchange("\x03", "redis> ")
	// Enter runs a complete line without a semicolon; an open quote continues it.
	term.exchange("SET "+key+":q \"a\r", "...> ")
	term.exchange("b\"\r", `{"value":"OK"}`)
	term.exchange("GET "+key+":q\r", `{"value":"a\nb"}`)
	term.exchange("DEL "+key+":q\r", `{"value":1}`)
	term.exchange("DBH_INVALID_COMMAND\r", "Command error:")
	term.exchange("\\tables\r", key)
	term.exchange("\\describe "+key[:len(key)-3], key)
	term.exchange("\x03", "redis> ")
	term.exchange("\\describe "+key+"\r", `"type":"list"`)
	term.exchange("\\indexes "+key+"\r", "no indexes")
	term.exchange("\\use 1\r", "Database changed to 1")
	term.exchange("\\database\r", "1\r\n")
	term.exchange("EXISTS "+key+"\r", `{"value":0}`)
	term.exchange("\\use 0\r", "Database changed to 0")
	term.exchange("LLEN "+key+"\r", `{"value":3}`)
	term.exit("\\q\r")
}
