package dbh

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Opt-in tests run only against databases explicitly supplied by the developer.
func TestExternalDrivers(t *testing.T) {
	for driver, env := range map[string]string{"postgres": "DBH_TEST_POSTGRES_DSN", "mysql": "DBH_TEST_MYSQL_DSN"} {
		t.Run(driver, func(t *testing.T) {
			dsn := os.Getenv(env)
			if dsn == "" {
				t.Skip("set " + env + " to run")
			}
			ctx := context.Background()
			s, err := openSession(ctx, Profile{Name: "integration", Driver: driver, DSN: dsn}, 10*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			name := fmt.Sprintf("dbh_test_%d", time.Now().UnixNano())
			var out bytes.Buffer
			if err := s.Run(ctx, "CREATE TABLE "+name+" (id INTEGER, name VARCHAR(50))", "table", &out); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := s.Run(ctx, "DROP TABLE "+name, "table", &out); err != nil {
					t.Errorf("cleanup: %v", err)
				}
			}()
			sql := fmt.Sprintf("INSERT INTO %s VALUES(1,'one'); BEGIN; INSERT INTO %s VALUES(2,'two'); ROLLBACK; SELECT * FROM %s ORDER BY id;", name, name, name)
			out.Reset()
			if err := s.Run(ctx, sql, "json", &out); err != nil {
				t.Fatal(err)
			}
			if out.String() != "{\"id\":1,\"name\":\"one\"}\n" {
				t.Fatalf("unexpected output: %s", out.String())
			}
			tables, err := s.Tables(ctx)
			if err != nil {
				t.Fatal(err)
			}
			found := ""
			for _, table := range tables {
				if table == name || strings.HasSuffix(table, "."+name) {
					found = table
					break
				}
			}
			if found == "" {
				t.Fatal("table absent from metadata")
			}
			cols, err := s.Columns(ctx, found)
			if err != nil || len(cols) != 2 {
				t.Fatalf("%v %v", cols, err)
			}
		})
	}
}
