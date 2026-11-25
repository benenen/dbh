//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedConnectionsStoreE2E(t *testing.T) {
	f := newFixture(t)
	f.run("new", "secret", "--driver", "postgres", "--dsn", "postgres://alice:super-secret@localhost:5432/app", "--env", "prod", "--read-only", "--tag", "core")

	connFile := filepath.Join(f.config, "connections.json")
	raw := readFile(t, connFile)
	if strings.Contains(raw, "super-secret") {
		t.Fatalf("connections.json leaked plaintext secret: %s", raw)
	}
	contains(t, raw, `"encrypted": true`)
	permission(t, connFile, 0600)
	permission(t, filepath.Join(f.config, ".store.key"), 0600)

	// DBH_STORE_KEY works without creating .store.key.
	f2 := newFixture(t)
	f2.config = filepath.Join(f2.root, "custom-cfg")
	f2.env["DBH_CONFIG_DIR"] = f2.config
	f2.env["DBH_STORE_KEY"] = "e2e-passphrase"
	f2.run("new", "k", "--driver", "sqlite", "--dsn", filepath.Join(f2.root, "k.db"))
	if _, err := os.Stat(filepath.Join(f2.config, ".store.key")); !os.IsNotExist(err) {
		t.Fatalf("expected no .store.key when DBH_STORE_KEY is set")
	}
	contains(t, f2.run("exec", "k", "SELECT 42 AS n;", "--format", "tsv"), "42")

	// Legacy plaintext connections.json migrates transparently on next write.
	f3 := newFixture(t)
	legacyCfg := filepath.Join(f3.root, "legacy-cfg")
	if err := os.MkdirAll(legacyCfg, 0700); err != nil {
		t.Fatal(err)
	}
	legacyDB := filepath.Join(f3.root, "legacy.db")
	writeFile(t, filepath.Join(legacyCfg, "connections.json"), `{"old":{"name":"old","driver":"sqlite","dsn":"`+legacyDB+`"}}`)
	f3.env["DBH_CONFIG_DIR"] = legacyCfg
	contains(t, f3.run("exec", "old", "SELECT 7 AS v;", "--format", "tsv"), "7")
	f3.run("new", "second", "--driver", "sqlite", "--dsn", ":memory:")
	migrated := readFile(t, filepath.Join(legacyCfg, "connections.json"))
	contains(t, migrated, `"encrypted": true`)
}

func TestDSNCmdAndTagsE2E(t *testing.T) {
	f := newFixture(t)
	dynDB := filepath.Join(f.root, "dyn.db")
	f.run("new", "dyn", "--driver", "sqlite", "--dsn-cmd", "printf '%s' '"+dynDB+"'", "--tag", "analytics", "--tag", "local")
	f.run("new", "other", "--driver", "sqlite", "--dsn", filepath.Join(f.root, "other.db"), "--tag", "staging")

	contains(t, f.run("check", "dyn"), "Credential command: OK")
	contains(t, f.run("exec", "dyn", "SELECT 1 AS ok;", "--format", "tsv"), "1")

	listed := f.run("ls", "--tag", "analytics")
	contains(t, listed, "ENV")
	contains(t, listed, "MODE")
	contains(t, listed, "TAGS")
	contains(t, listed, "dyn")
	contains(t, listed, "analytics,local")
	if strings.Contains(listed, "other") {
		t.Fatalf("unexpected connection in --tag analytics list: %s", listed)
	}

	checkOut := f.run("check", "--all", "--tag", "analytics")
	contains(t, checkOut, "dyn: OK")
	if strings.Contains(checkOut, "other:") {
		t.Fatalf("unexpected connection in --tag analytics check: %s", checkOut)
	}

	lsJSON := f.run("ls", "--tag", "analytics", "--format", "json")
	contains(t, lsJSON, `"name":"dyn"`)
	contains(t, lsJSON, `"env":""`)
	contains(t, lsJSON, `"read_only":false`)
	contains(t, lsJSON, `"tags":"analytics,local"`)
	contains(t, f.run("ls", "--tag", "analytics", "--format", "tsv"), "dyn\t")

	f.run("edit", "dyn", "--no-tags")
	excludes(t, f.run("ls", "--tag", "analytics"), "dyn")

	f.run("edit", "other", "--env", "prod", "--read-only")
	lsProd := f.run("ls")
	contains(t, lsProd, "prod")
	contains(t, lsProd, "ro")
	contains(t, f.fail("exec", "other", "CREATE TABLE x (id INT);"), "read-only")
	f.run("edit", "other", "--no-read-only", "--env", "")
	f.run("exec", "other", "CREATE TABLE x (id INT);")

	otherDynDB := filepath.Join(f.root, "other_dyn.db")
	f.run("edit", "other", "--dsn-cmd", "printf '%s' '"+otherDynDB+"'")
	contains(t, f.run("check", "other"), "Credential command: OK")
}

func TestReadOnlyAndProdGuardsE2E(t *testing.T) {
	f := newFixture(t)
	roDB := filepath.Join(f.root, "ro.db")
	f.run("new", "ro", "--driver", "sqlite", "--dsn", roDB, "--read-only")
	contains(t, f.fail("exec", "ro", "CREATE TABLE t (id INT);"), "read-only")
	f.run("exec", "ro", "--allow-write", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1);")
	contains(t, f.run("exec", "ro", "SELECT id FROM t;", "--format", "tsv"), "1")

	prodDB := filepath.Join(f.root, "prod.db")
	f.run("new", "prod", "--driver", "sqlite", "--dsn", prodDB, "--env", "prod")
	f.run("exec", "prod", "CREATE TABLE t (id INT); INSERT INTO t VALUES (1), (2);")
	contains(t, f.fail("exec", "prod", "DELETE FROM t;"), "without WHERE")
	contains(t, f.fail("exec", "prod", "DROP TABLE t;"), "blocked on production")
	f.run("exec", "prod", "DELETE FROM t WHERE id = 1;")
	f.run("exec", "prod", "--allow-write", "DROP TABLE t;")
}
