package dbh

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStoreEncryptionAndLegacyMigration(t *testing.T) {
	dir := t.TempDir()
	s := Store{Dir: dir, HistoryLimit: 100}

	p := Profile{
		Name:     "secret-db",
		Driver:   "postgres",
		DSN:      "postgres://alice:super-secret@localhost:5432/app",
		ReadOnly: true,
		Env:      "prod",
		Tags:     []string{"prod", "core"},
	}
	if err := s.Put(p, true); err != nil {
		t.Fatalf("Put: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "connections.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") {
		t.Fatalf("connections.json should be encrypted, got plaintext: %s", raw)
	}
	if !strings.Contains(string(raw), `"encrypted": true`) {
		t.Fatalf("expected encrypted envelope: %s", raw)
	}

	keyInfo, err := os.Stat(filepath.Join(dir, ".store.key"))
	if err != nil {
		t.Fatal(err)
	}
	if keyInfo.Mode().Perm() != 0600 {
		t.Fatalf(".store.key mode = %o, want 0600", keyInfo.Mode().Perm())
	}

	got, err := s.Get("secret-db")
	if err != nil || got.DSN != p.DSN || !got.ReadOnly || got.Env != "prod" || len(got.Tags) != 2 {
		t.Fatalf("Get = %+v, err=%v", got, err)
	}

	// Legacy plaintext connections.json should load and migrate to encrypted on write.
	legacyDir := t.TempDir()
	legacyStore := Store{Dir: legacyDir}
	if err := os.WriteFile(filepath.Join(legacyDir, "connections.json"), []byte(`{"legacy":{"name":"legacy","driver":"sqlite","dsn":":memory:"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	leg, err := legacyStore.Get("legacy")
	if err != nil || leg.DSN != ":memory:" {
		t.Fatalf("legacy load = %+v, %v", leg, err)
	}
	if err := legacyStore.Put(Profile{Name: "second", Driver: "sqlite", DSN: ":memory:"}, true); err != nil {
		t.Fatal(err)
	}
	migrated, _ := os.ReadFile(filepath.Join(legacyDir, "connections.json"))
	if !strings.Contains(string(migrated), `"encrypted": true`) {
		t.Fatalf("expected migrated encrypted connections.json: %s", migrated)
	}
}

func TestStoreEnvKeyAndDSNCmd(t *testing.T) {
	t.Setenv("DBH_STORE_KEY", "test-passphrase-123")
	dir := t.TempDir()
	s := Store{Dir: dir}
	if err := s.Put(Profile{Name: "dyn", Driver: "sqlite", DSNCmd: "printf ':memory:'"}, true); err != nil {
		t.Fatalf("Put dsn-cmd: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".store.key")); !os.IsNotExist(err) {
		t.Fatalf("expected no .store.key when DBH_STORE_KEY is set")
	}

	session, err := openSession(context.Background(), Profile{Name: "dyn", Driver: "sqlite", DSNCmd: "printf ':memory:'"}, 5*time.Second)
	if err != nil {
		t.Fatalf("openSession with DSNCmd: %v", err)
	}
	session.Close()
}
