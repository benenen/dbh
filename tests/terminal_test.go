//go:build e2e && (linux || darwin)

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

func TestTerminalBracketedPasteIsEditableBeforeExecution(t *testing.T) {
	f := newFixture(t)
	f.seed()
	term := f.terminal()
	contains(t, term.transcript, "\x1b[?2004h")
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		query := "SELECT\n    '你好;粘贴' AS value;\n"
		output := term.exchange("\x1b[200~" + strings.ReplaceAll(query, "\n", newline) + "\x1b[201~")
		excludes(t, output, "{\"value\":")
		absent(t, f.historyPath())
		term.exchange("\x03", "demo> ")
	}
	query := "INSERT INTO users(name)\n    VALUES ('粘贴');\nSELECT name\n    FROM users WHERE id = 2;\n"
	term.exchange("\x1b[200~" + query + "\x1b[201~")
	equal(t, f.sql("SELECT COUNT(*) AS count FROM users", "--format", "json", "--no-history"), "{\"count\":1}\n")
	term.exchange("\x1b[A\x05")
	term.exchange("\x7f")
	term.exchange("\x7f")
	term.exchange("3;\x1b[B")
	term.exchange("\r", "demo> ")
	equal(t, f.sql("SELECT name FROM users ORDER BY id", "--format", "json", "--no-history"), "{\"name\":\"Alice\"}\n{\"name\":\"粘贴\"}\n")
	equal(t, f.history(), []string{"INSERT INTO users(name)\n    VALUES ('粘贴');", "SELECT name\n    FROM users WHERE id = 3;"})
	term.exit("\\q\r")
}

func TestTerminalPasteLineEndingsAndLiteralCommands(t *testing.T) {
	f := newFixture(t)
	term := f.terminal()
	query := "SELECT\n    '你好;\n\\q\n缩进' AS value;\n"
	for _, newline := range []string{"\n", "\r\n", "\r"} {
		term.exchange("\x1b[200~" + strings.ReplaceAll(query, "\n", newline) + "\x1b[201~")
		term.exchange("\r", "{\"value\":\"你好;\\n\\\\q\\n缩进\"}")
	}
	term.exit("\\q\r")
	equal(t, f.history(), []string{strings.TrimSpace(query), strings.TrimSpace(query), strings.TrimSpace(query)})
}

func TestTerminalCursorReportDoesNotBlockInput(t *testing.T) {
	f := newFixture(t)
	f.env["TERM"] = "xterm-ghostty"
	f.env["TERM_PROGRAM"] = "ghostty"
	term := f.terminal()
	if _, err := term.master.Write([]byte("\x1b[1;7R")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	term.exchange("\x1b[1;7RSELECT 42 AS value;\r", "{\"value\":42}")
	term.exchange("\x03", "demo> ")
	term.exit("\\q\r")
}

func TestTerminalPromptAtBottomKeepsCursorAndInputAligned(t *testing.T) {
	f := newFixture(t)
	term := f.terminal("--no-history")
	check := func(suffix, prefix string) {
		t.Helper()
		for _, start := range []int{0, 22, 23} {
			screen := newScreen(t, start)
			screen.feed(term.transcript)
			equal(t, screen.line(screen.y), prefix+suffix)
			equal(t, screen.x, len("demo> ")+len(strings.TrimSpace(suffix)))
			connected := false
			for row := range screen.rows {
				connected = connected || strings.Contains(screen.line(row), "Connected to demo")
			}
			if !connected {
				t.Fatalf("connection banner missing at start row %d", start)
			}
		}
	}
	check("", "demo>")
	term.exchange("se", "SELECT")
	check(" se", "demo>")
	term.exchange("\x03", "demo> ")
	check("", "demo>")
	term.exchange("SELECT 42 AS value;\r", "{\"value\":42}")
	check("", "demo>")
	term.exchange("SELECT\r", "...> ")
	check("", "...>")
	term.exchange("43 AS value;\r", "{\"value\":43}")
	check("", "demo>")
	term.exit("\\q\r")
}

func TestTerminalPromptColorsAndCommandsDuringInput(t *testing.T) {
	f := newFixture(t)
	f.seed()
	term := f.terminal()
	contains(t, term.transcript, "\x1b[36m")
	term.exchange("SELECT\r", "...> ")
	term.exchange("\\tables\r", "users")
	term.exchange("name FROM users;\r", "{\"name\":\"Alice\"}")
	term.exchange("SELECT\r", "...> ")
	term.exchange("\\clear\r", "demo> ")
	term.exchange("SELECT 42 AS value;\r", "{\"value\":42}")
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT\nname FROM users;", "SELECT 42 AS value;"})
	f.env["NO_COLOR"] = "1"
	term = f.terminal()
	excludes(t, term.transcript, "\x1b[36m")
	term.exit("\\q\r")
}

func TestTerminalManualNewlineKeepsWholeBuffer(t *testing.T) {
	f := newFixture(t)
	term := f.terminal()
	term.exchange("SELECT 6 AS value;\x1b\r", "...> ")
	absent(t, f.historyPath())
	term.exchange("SELECT 8 AS value;\r", "{\"value\":8}")
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT 6 AS value;", "SELECT 8 AS value;"})
}

func TestTerminalLiveCompletionAndInterrupt(t *testing.T) {
	f := newFixture(t)
	f.seed()
	term := f.terminal()
	term.exchange("sel", "SELECT") // Suggestions must appear while typing, before Tab.
	term.exchange("\t ")
	term.exchange("na", "name")
	term.exchange("\t FROM ")
	term.exchange("us", "users")
	term.exchange("\t;\r", "{\"name\":\"Alice\"}")
	term.exchange("se\t")
	term.exchange("\x03", "demo> ")
	term.exchange("SELECT 41 AS value;\r", "{\"value\":41}")
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT name FROM users;", "SELECT 41 AS value;"})
}

func TestTerminalSubstringCompletion(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE apple(id INTEGER); CREATE TABLE platform(id INTEGER); CREATE TABLE sample(value TEXT); CREATE INDEX sample_lookup ON sample(value); INSERT INTO sample VALUES ('substring result');", "--no-history")
	term := f.terminal()
	for _, command := range []string{`\describe `, `\indexes `, "SELECT * FROM "} {
		term.exchange(command)
		term.exchange("p")
		output := term.exchange("l", "sample") // Type characters without Tab to verify live suggestions.
		contains(t, output, "apple")
		contains(t, output, "platform")
		screen := newScreen(t, 0)
		screen.feed(term.transcript)
		equal(t, screen.line(screen.y), "demo> "+command+"pl")
		term.exchange("\x03", "demo> ")
	}
	for _, command := range []string{`\describe `, `\indexes `} {
		term.exchange(command+"AMP", "sample")
		output := term.exchange("\t\r", `"name":"sample_lookup"`)
		excludes(t, output, "does not exist")
	}
	term.exchange("SELECT * FROM AMP", "sample")
	term.exchange("\t;\r", `{"value":"substring result"}`)
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT * FROM sample;"})
}

func TestTerminalMenuSelectionKeys(t *testing.T) {
	f := newFixture(t)
	f.sql("CREATE TABLE mail(address TEXT, addressee TEXT, addresseeID INTEGER); INSERT INTO mail VALUES ('a', 'b', 3);", "--no-history")
	term := f.terminal()
	// Down enters the visible menu; arrows move and Enter only confirms the candidate.
	term.exchange("SELECT ad", "addresseeID")
	term.exchange("\x1b[B")
	term.exchange("\x1b[C")
	output := term.exchange("\r")
	excludes(t, output, "...>")
	term.exchange(" FROM mail;\r", `{"addressee":"b"}`)
	// Tab then Up returns to the first candidate.
	term.exchange("SELECT ad\t\x1b[B\x1b[A")
	term.exchange("\r")
	term.exchange(" FROM mail;\r", `{"address":"a"}`)
	// A complete command is not executed by the Enter that confirms a candidate.
	term.exchange(`\describe ma`, "mail")
	excludes(t, term.exchange("\t\r"), "addresseeID")
	term.exchange("\r", `"name":"addresseeID"`)
	// Above the last line, Down moves within multi-line input even when the
	// cursor lands on a word with candidates.
	term.exchange("SELECT 1 AS x,\x1b\r2 AS y")
	term.exchange("\x1b[A\x1b[B;\r", `{"x":1,"y":2}`)
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT addressee FROM mail;", "SELECT address FROM mail;", "SELECT 1 AS x,\n2 AS y;"})
}

func TestTerminalLongCandidateListKeepsInputVisible(t *testing.T) {
	f := newFixture(t)
	var ddl strings.Builder
	for i := range 150 {
		fmt.Fprintf(&ddl, "CREATE TABLE report_table_%03d(id INTEGER);", i)
	}
	f.sql(ddl.String(), "--no-history")
	term := f.terminal()
	term.exchange(`\describe re`, "more completion rows")
	screen := newScreen(t, 0)
	screen.feed(term.transcript)
	equal(t, screen.line(screen.y), `demo> \describe re`)
	rows := 0
	for y := range screen.rows {
		if strings.Contains(screen.line(y), "report_table_") {
			rows++
		}
	}
	if rows == 0 || rows > len(screen.rows)/2 {
		t.Fatalf("candidate rows = %d, want 1..%d", rows, len(screen.rows)/2)
	}
	term.exchange("\x03", "demo> ")
	term.exit("\\q\r")
}

func TestTerminalPersistedHistoryAndSearch(t *testing.T) {
	f := newFixture(t)
	f.seed()
	term := f.terminal()
	term.exchange("SELECT name FROM users;\r", "{\"name\":\"Alice\"}")
	term.exit("\\q\r")
	term = f.terminal()
	ghost := term.exchange("SELECT na", "FROM users;")
	contains(t, ghost, "\x1b[2m")
	term.exchange("\x1b[C\r", "{\"name\":\"Alice\"}")
	term.exchange("\x12name", "SQL history")
	term.exchange("\x03", "demo> ")
	term.exchange("SELECT 41 AS value;\r", "{\"value\":41}")
	term.exchange("\x1b[A\r", "{\"value\":41}")
	term.exit("\\q\r")
	before := readFile(t, f.historyPath())
	term = f.terminal("--no-history")
	excludes(t, term.exchange("SELECT na", "SELECT na"), "FROM users;")
	term.exchange("\x03", "demo> ")
	term.exchange("SELECT 99 AS value;\r", "{\"value\":99}")
	term.exit("\\q\r")
	equal(t, readFile(t, f.historyPath()), before)
}

func TestTerminalMultilineErrorRecoveryAndEOF(t *testing.T) {
	f := newFixture(t)
	f.seed()
	term := f.terminal()
	term.exchange("SELECT\r", "...> ")
	term.exchange("name FROM users;\r", "{\"name\":\"Alice\"}")
	term.exchange("SELECT * FROM missing;\r", "SQL error:")
	term.exchange("SELECT\r", "...> ")
	term.exchange("\x03", "demo> ")
	term.exchange("SELECT 9 AS value;\r", "{\"value\":9}")
	term.exchange("\\tables\r", "users")
	term.exit("\x04")
	equal(t, f.history(), []string{"SELECT\nname FROM users;", "SELECT * FROM missing;", "SELECT 9 AS value;"})
}

func TestTerminalColumnDetailsAndIndexes(t *testing.T) {
	f := newFixture(t)
	f.seed()
	f.sql("CREATE UNIQUE INDEX users_name ON users(name)", "--no-history")
	term := f.terminal()
	output := term.exchange("\\describe users\r", "users_name")
	for _, want := range []string{"\"primary_key_position\":1", "\"type\":\"INTEGER\"", "\"comment\":null", "\"nullable\":\"NO\""} {
		contains(t, output, want)
	}
	output = term.exchange("\\indexes users\r", "users_name")
	contains(t, output, "\"is_unique\":1")
	excludes(t, output, "\"primary_key_position\"")
	term.exchange("\\describe missing\r", "does not exist")
	term.exchange("\\format table\r", "demo> ")
	output = term.exchange("\\describe users\r", "users_name")
	contains(t, output, "Columns:")
	contains(t, output, "Indexes:")
	term.exchange("\\indexes\r", "Usage: \\indexes TABLE")
	term.exit("\\q\r")
	absent(t, f.historyPath())
}

func TestTerminalDatabaseLists(t *testing.T) {
	for _, driver := range []string{"mysql", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_MYSQL_DSN"
			currentSQL := "SELECT DATABASE() AS name"
			systemDB := "information_schema"
			if driver == "postgres" {
				env = "DBH_TEST_POSTGRES_DSN"
				currentSQL = "SELECT current_database() AS name"
				systemDB = "template1"
			}
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip("set " + env + " to run")
			}
			f := newFixture(t)
			f.env["DBH_E2E_DSN"] = dsn
			f.run("new", driver, "--driver", driver, "--dsn-env", "DBH_E2E_DSN")
			var current struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(f.run("connect", driver, "--sql", currentSQL, "--format", "json", "--no-history")), &current); err != nil {
				t.Fatal(err)
			}
			term := f.openTerminal(driver)
			term.exchange("\\dat", "\\database") // Command suggestions must appear without Tab.
			term.exchange("\x03", driver+"> ")
			output := term.exchange("\\database\r", systemDB)
			contains(t, output, current.Name+"\r\n")
			excludes(t, output, "Unknown command")
			term.exchange("\\database extra\r", "Usage: \\database")
			output = term.exchange("\\help\r", "List databases")
			contains(t, output, "\\database")
			term.exit("\\q\r")
			absent(t, filepath.Join(f.config, "history", driver+".jsonl"))
		})
	}
}

func TestTerminalDatabaseListUnsupportedDriver(t *testing.T) {
	f := newFixture(t)
	term := f.terminal()
	term.exchange("\\database\r", "database listing is not supported for SQLite")
	term.exchange("SELECT 1 AS value;\r", "{\"value\":1}")
	term.exit("\\q\r")
	equal(t, f.history(), []string{"SELECT 1 AS value;"})
}

func TestTerminalDatabaseSwitch(t *testing.T) {
	for _, driver := range []string{"mysql", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_MYSQL_DSN"
			altEnv := "DBH_TEST_MYSQL_ALT_DATABASE"
			currentSQL := "SELECT DATABASE() AS name"
			quote := "`"
			if driver == "postgres" {
				env = "DBH_TEST_POSTGRES_DSN"
				altEnv = "DBH_TEST_POSTGRES_ALT_DATABASE"
				currentSQL = "SELECT current_database() AS name"
				quote = "\""
			}
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip("set " + env + " to run")
			}
			f := newFixture(t)
			f.env["DBH_E2E_DSN"] = dsn
			f.run("new", driver, "--driver", driver, "--dsn-env", "DBH_E2E_DSN")
			var current struct {
				Name string `json:"name"`
			}
			if err := json.Unmarshal([]byte(f.run("c", driver, "--sql", currentSQL, "--format", "json", "--no-history")), &current); err != nil {
				t.Fatal(err)
			}
			identifier := func(name string) string { return quote + strings.ReplaceAll(name, quote, quote+quote) + quote }
			expectedJSON := func(name string) string {
				encoded, err := json.Marshal(map[string]string{"name": name})
				if err != nil {
					t.Fatal(err)
				}
				return string(encoded)
			}
			useCurrent := "USE " + identifier(current.Name) + ";"
			equal(t, f.run("c", driver, "--sql", useCurrent+currentSQL, "--format", "json", "--no-history"), expectedJSON(current.Name)+"\n")
			configBefore := readFile(t, filepath.Join(f.config, "connections.json"))
			term := f.openTerminal(driver, "--no-history")
			term.exchange("\\us", "\\use")
			term.exchange("\x03", driver+"> ")
			term.exchange("\\use "+identifier(current.Name)+"\r", "Database changed to "+current.Name)
			term.exchange(currentSQL+";\r", expectedJSON(current.Name))
			term.exchange(useCurrent+currentSQL+";\r", expectedJSON(current.Name))
			missing := fmt.Sprintf("dbh_missing_%d", time.Now().UnixNano())
			term.exchange("\\use "+missing+"\r", "cannot switch database")
			term.exchange(currentSQL+";\r", expectedJSON(current.Name))
			term.exchange("USE "+missing+";\r", "cannot switch database")
			term.exchange(currentSQL+";\r", expectedJSON(current.Name))
			term.exchange("\\use\r", "Usage: \\use DATABASE")
			term.exit("\\q\r")
			if readFile(t, filepath.Join(f.config, "connections.json")) != configBefore {
				t.Fatal("database switching changed the saved connection configuration")
			}
			f.fail("c", driver, "--sql", "USE "+missing+"; SELECT 1;", "--format", "json", "--no-history")
			if driver == "mysql" {
				// Native MySQL USE must preserve the connection and its temporary tables.
				equal(t, f.run("c", driver, "--sql", "CREATE TEMPORARY TABLE switch_session(value INTEGER); INSERT INTO switch_session VALUES(7); "+useCurrent+"SELECT value FROM switch_session;", "--format", "json", "--no-history"), "{\"value\":7}\n")
			} else {
				output := f.run("c", driver, "--sql", "SELECT pg_backend_pid() AS pid; "+useCurrent+"SELECT pg_backend_pid() AS pid;", "--format", "json", "--no-history")
				var pids []float64
				for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
					var row map[string]float64
					if err := json.Unmarshal([]byte(line), &row); err != nil {
						t.Fatal(err)
					}
					pids = append(pids, row["pid"])
				}
				if len(pids) != 2 || pids[0] == pids[1] {
					t.Fatalf("PostgreSQL did not reconnect: %v", pids)
				}
			}
			t.Run("different_database", func(t *testing.T) {
				alt := os.Getenv(altEnv)
				if alt == "" {
					t.Skip("set " + altEnv + " to test switching between different databases")
				}
				if alt == current.Name {
					t.Fatal(altEnv + " must name a different test database")
				}
				original := fmt.Sprintf("dbh_switch_original_%d", time.Now().UnixNano())
				target := fmt.Sprintf("dbh_switch_target_%d", time.Now().UnixNano())
				child := *f
				child.t = t
				f := &child
				f.run("c", driver, "--sql", "CREATE TABLE "+original+" (original_marker INTEGER);", "--no-history")
				t.Cleanup(func() { f.run("c", driver, "--sql", "DROP TABLE "+original+";", "--no-history") })
				useAlt := "USE " + identifier(alt) + ";"
				f.run("c", driver, "--sql", useAlt+"CREATE TABLE "+target+" (target_column INTEGER);", "--no-history")
				t.Cleanup(func() { f.run("c", driver, "--sql", useAlt+"DROP TABLE "+target+";", "--no-history") })
				term := f.openTerminal(driver, "--no-history")
				term.exchange("\\use "+identifier(alt)+"\r", "Database changed to "+alt)
				term.exchange(currentSQL+";\r", expectedJSON(alt))
				output := term.exchange("\\tables\r", target)
				excludes(t, output, original)
				term.exchange("SELECT tar", "target_column")
				term.exchange("\x03", driver+"> ")
				term.exchange(useCurrent+currentSQL+";\r", expectedJSON(current.Name))
				term.exchange("SELECT orig", "original_marker")
				term.exchange("\x03", driver+"> ")
				output = term.exchange("\\tables\r", original)
				excludes(t, output, target)
				term.exit("\\q\r")
				if readFile(t, filepath.Join(f.config, "connections.json")) != configBefore {
					t.Fatal("database switching changed the saved connection configuration")
				}
			})
		})
	}
}
