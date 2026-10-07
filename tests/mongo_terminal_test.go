//go:build e2e && (linux || darwin)

package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMongoTerminalCommands(t *testing.T) {
	dsn := os.Getenv("DBH_TEST_MONGO_DSN")
	if dsn == "" {
		t.Skip("set DBH_TEST_MONGO_DSN to run")
	}
	f := newFixture(t)
	f.env["DBH_E2E_DSN"] = dsn
	f.run("new", "mongo", "--driver", "mongo", "--dsn-env", "DBH_E2E_DSN")
	collection := fmt.Sprintf("dbh_e2e_%d", time.Now().UnixNano())
	f.run("c", "mongo", "--sql", fmt.Sprintf(`{"insert":%q,"documents":[{"_id":1,"name":"hello"}]}`, collection), "--no-history")
	t.Cleanup(func() { f.run("c", "mongo", "--sql", fmt.Sprintf(`{"drop":%q}`, collection), "--no-history") })
	configBefore := readFile(t, filepath.Join(f.config, "connections.json"))
	var current struct {
		DB string `json:"db"`
	}
	if err := json.Unmarshal([]byte(f.run("c", "mongo", "--sql", `{"dbStats":1}`, "--format", "json", "--no-history")), &current); err != nil {
		t.Fatal(err)
	}
	term := f.openTerminal("mongo", "--no-history")
	term.exchange(`{"fi`, "find")
	term.exchange("\x03", "mongo> ")
	term.exchange("{\"ping\":\r", "...> ")
	term.exchange("1}\r", `"ok":1`)
	term.exchange(`{"dbh_invalid_command":1}`+"\r", "Command error:")
	term.exchange(`{"ping":1}`+"\r", `"ok":1`)
	term.exchange("\\database\r", current.DB)
	term.exchange("\\tables\r", collection)
	term.exchange("\\describe "+collection+"\r", `"name":"hello"`)
	term.exchange("\\indexes "+collection+"\r", `"name":"_id_"`)
	term.exchange("\\use dbh_e2e_switch\r", "Database changed to dbh_e2e_switch")
	term.exchange(`{"ping":1}`+"\r", `"ok":1`)
	quoted, err := json.Marshal(current.DB)
	if err != nil {
		t.Fatal(err)
	}
	term.exchange("USE "+string(quoted)+";\r", "mongo> ")
	term.exchange("\\tables\r", collection)
	term.exit("\\q\r")
	if readFile(t, filepath.Join(f.config, "connections.json")) != configBefore {
		t.Fatal("switching changed saved configuration")
	}
	absent(t, filepath.Join(f.config, "history", "mongo.jsonl"))
}
