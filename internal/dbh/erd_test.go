package dbh

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestERDPullSQLite(t *testing.T) {
	ctx := context.Background()
	p := Profile{Name: "test", Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "erd.db")}
	s, err := openSession(ctx, p, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Run(ctx, "CREATE TABLE users(id INTEGER PRIMARY KEY); CREATE TABLE orders(id INTEGER PRIMARY KEY, user_id INTEGER REFERENCES users(id));", "json", io.Discard)
	s.Close()
	if err != nil {
		t.Fatal(err)
	}
	dbml, err := pullDBML(ctx, p, "", false, "", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dbml, `Ref: "orders"."user_id" > "users"."id"`) {
		t.Fatalf("unexpected DBML:\n%s", dbml)
	}
}

// Opt-in: reads foreign keys from the databases supplied by the developer.
func TestExternalERDPull(t *testing.T) {
	for driver, env := range map[string]string{"postgres": "DBH_TEST_POSTGRES_DSN", "mysql": "DBH_TEST_MYSQL_DSN"} {
		t.Run(driver, func(t *testing.T) {
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip("set " + env + " to run")
			}
			ctx := context.Background()
			p := Profile{Name: "integration", Driver: driver, DSN: dsn}
			s, err := openSession(ctx, p, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			suffix := time.Now().UnixNano()
			parent, child := fmt.Sprintf("dbh_erd_parent_%d", suffix), fmt.Sprintf("dbh_erd_child_%d", suffix)
			// A composite key and a quoted default cover dbml-tools patches.
			ddl := fmt.Sprintf("CREATE TABLE %s (a INTEGER, b INTEGER, PRIMARY KEY (a, b)); CREATE TABLE %s (id INTEGER PRIMARY KEY, pa INTEGER, pb INTEGER, note VARCHAR(10) DEFAULT 'it''s', FOREIGN KEY (pa, pb) REFERENCES %s(a, b) ON DELETE CASCADE);", parent, child, parent)
			if err := s.Run(ctx, ddl, "json", io.Discard); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Run(ctx, "DROP TABLE "+child+"; DROP TABLE "+parent+";", "json", io.Discard); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			}()
			dbml, err := pullDBML(ctx, p, "", false, "", 30*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{
				fmt.Sprintf(`Ref: "%s".("pa", "pb") > "%s".("a", "b") [delete: cascade]`, child, parent),
				`"note" varchar(10) [default: 'it\'s']`,
			} {
				if !strings.Contains(dbml, want) {
					t.Fatalf("missing %s in:\n%s", want, dbml)
				}
			}
		})
	}
}
