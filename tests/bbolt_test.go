//go:build e2e

package e2e

import (
	"path/filepath"
	"testing"
)

func TestBBoltE2E(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	f := newFixture(t)

	f.run("new", "local-bolt", "--driver", "bbolt", "--dsn", "bbolt://"+dbPath)

	out := f.run("exec", "local-bolt", `CREATE BUCKET items`, "--format", "tsv")
	contains(t, out, "CREATED")

	out = f.run("exec", "local-bolt", `PUT items k1 "v1"`, "--format", "tsv")
	contains(t, out, "OK")

	f.run("check", "local-bolt")

	out = f.run("exec", "local-bolt", `GET items k1`, "--format", "tsv")
	contains(t, out, "v1")

	out = f.run("tables", "local-bolt")
	contains(t, out, "items")

	out = f.run("describe", "local-bolt", "items", "--format", "tsv")
	contains(t, out, "k1")

	out = f.run("exec", "local-bolt", `DEL items k1`, "--format", "tsv")
	contains(t, out, "DELETED")
}
