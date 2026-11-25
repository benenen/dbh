//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestERDDatabases(t *testing.T) {
	for _, driver := range []string{"postgres", "mysql", "mssql", "oracle", "trino", "dm"} {
		t.Run(driver, func(t *testing.T) {
			env := "DBH_TEST_" + strings.ToUpper(driver) + "_DSN"
			if os.Getenv(env) == "" {
				t.Skip(env + " is not configured")
			}

			f := newFixture(t)
			repo := filepath.Join(f.root, "repo-"+driver)
			if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
			writeFile(t, filepath.Join(repo, ".git", "HEAD"), "ref: refs/heads/main\n")
			f.dir = repo

			f.run("new", "srv", "--driver", driver, "--dsn-env", env)

			switch driver {
			case "postgres":
				testERDPostgres(t, f, repo)
			case "mysql":
				testERDMySQL(t, f, repo)
			case "mssql":
				testERDMSSQL(t, f, repo)
			case "oracle":
				testERDOracle(t, f, repo)
			case "trino":
				testERDTrino(t, f, repo)
			case "dm":
				testERDDM(t, f, repo)
			}
		})
	}
}

func testERDPostgres(t *testing.T, f *fixture, repo string) {
	const schema = "dbh_erd_e2e"
	t.Cleanup(func() {
		f.run("exec", "srv", fmt.Sprintf("DROP SCHEMA IF EXISTS %s CASCADE;", schema), "--no-history")
	})
	f.run("exec", "srv", fmt.Sprintf(`
DROP SCHEMA IF EXISTS %s CASCADE;
CREATE SCHEMA %s;
CREATE TABLE %s.users (
  id SERIAL PRIMARY KEY,
  email TEXT UNIQUE NOT NULL
);
CREATE TABLE %s.orders (
  id SERIAL PRIMARY KEY,
  user_id INT NOT NULL REFERENCES %s.users(id) ON DELETE CASCADE,
  code TEXT
);
CREATE INDEX idx_orders_lower ON %s.orders (LOWER(code));
CREATE TABLE %s.events (
  id INT NOT NULL,
  created_date DATE NOT NULL
) PARTITION BY RANGE (created_date);
COMMENT ON TABLE %s.users IS 'PG users table';
COMMENT ON COLUMN %s.users.email IS 'User email';
`, schema, schema, schema, schema, schema, schema, schema, schema, schema), "--no-history")

	out := f.run("erd", "pull", "srv", "--schema", schema, "--save")
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `Table "dbh_erd_e2e"."users"`)
	contains(t, dbml, `Table "dbh_erd_e2e"."events"`)
	contains(t, dbml, `Note: 'PG users table'`)
	contains(t, dbml, `note: 'User email'`)
	contains(t, dbml, `[delete: cascade]`)
	contains(t, f.run("erd", "check"), "OK: 3 tables, 1 declared and 0 inferred relations")
	contains(t, f.run("erd", "relations", "--format", "tsv"), "dbh_erd_e2e.orders\tuser_id\t>\tdbh_erd_e2e.users\tid")
	contains(t, f.run("erd", "path", "dbh_erd_e2e.orders", "dbh_erd_e2e.users", "--format", "tsv"), "dbh_erd_e2e.orders\tuser_id\tdbh_erd_e2e.users\tid")
}

func testERDMySQL(t *testing.T, f *fixture, repo string) {
	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS dbh_erd_orders; DROP TABLE IF EXISTS dbh_erd_users;", "--no-history")
	})
	f.run("exec", "srv", `
DROP TABLE IF EXISTS dbh_erd_orders;
DROP TABLE IF EXISTS dbh_erd_users;
CREATE TABLE dbh_erd_users (
  id INT AUTO_INCREMENT PRIMARY KEY,
  email VARCHAR(100) NOT NULL UNIQUE COMMENT 'User email',
  status ENUM('open','in-progress','24h') NOT NULL DEFAULT 'open'
) COMMENT='MySQL users table';
CREATE TABLE dbh_erd_orders (
  id INT AUTO_INCREMENT PRIMARY KEY,
  user_id INT NOT NULL,
  code VARCHAR(32),
  CONSTRAINT fk_erd_orders_user FOREIGN KEY (user_id) REFERENCES dbh_erd_users(id) ON DELETE CASCADE
);
`, "--no-history")

	out := f.run("erd", "pull", "srv", "--include", "dbh_erd_*", "--save")
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `Table "dbh_erd_users"`)
	contains(t, dbml, `Note: 'MySQL users table'`)
	contains(t, dbml, `note: 'User email'`)
	contains(t, dbml, `'in-progress'`)
	contains(t, dbml, `Ref: "dbh_erd_orders"."user_id" > "dbh_erd_users"."id" [delete: cascade]`)
	contains(t, f.run("erd", "check"), "OK: 2 tables, 1 declared and 0 inferred relations")
	contains(t, f.run("erd", "relations", "--format", "tsv"), "dbh_erd_orders\tuser_id\t>\tdbh_erd_users\tid")
}

func testERDMSSQL(t *testing.T, f *fixture, repo string) {
	dbArgs := []string{}
	if dbName := os.Getenv("DBH_TEST_MSSQL_ALT_DATABASE"); dbName != "" {
		dbArgs = append(dbArgs, "--db", dbName)
	}
	execArgs := func(sqlText string) []string {
		args := append([]string{"exec", "srv"}, dbArgs...)
		return append(args, sqlText, "--no-history")
	}
	t.Cleanup(func() {
		f.run(execArgs("DROP TABLE IF EXISTS dbo.dbh_erd_orders; DROP TABLE IF EXISTS dbo.dbh_erd_users;")...)
	})
	f.run(execArgs(`
DROP TABLE IF EXISTS dbo.dbh_erd_orders;
DROP TABLE IF EXISTS dbo.dbh_erd_users;
CREATE TABLE dbo.dbh_erd_users (
  id INT IDENTITY(1,1) PRIMARY KEY,
  email NVARCHAR(100) UNIQUE NOT NULL,
  status VARCHAR(20) NOT NULL DEFAULT 'open',
  calc_col INT NOT NULL DEFAULT ((1)+(2))
);
CREATE TABLE dbo.dbh_erd_orders (
  id INT IDENTITY(1,1) PRIMARY KEY,
  user_id INT NOT NULL REFERENCES dbo.dbh_erd_users(id) ON DELETE CASCADE,
  code VARCHAR(32) NOT NULL
);
CREATE INDEX idx_erd_orders_code ON dbo.dbh_erd_orders (code);
EXEC sp_addextendedproperty @name=N'MS_Description', @value=N'MSSQL users table', @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'dbh_erd_users';
EXEC sp_addextendedproperty @name=N'MS_Description', @value=N'User email', @level0type=N'SCHEMA', @level0name=N'dbo', @level1type=N'TABLE', @level1name=N'dbh_erd_users', @level2type=N'COLUMN', @level2name=N'email';
`)...)

	pullArgs := append([]string{"erd", "pull", "srv"}, dbArgs...)
	pullArgs = append(pullArgs, "--schema", "dbo", "--include", "dbh_erd_*", "--save")
	out := f.run(pullArgs...)
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `Table "dbo"."dbh_erd_users"`)
	contains(t, dbml, `Note: 'MSSQL users table'`)
	contains(t, dbml, `note: 'User email'`)
	contains(t, dbml, `default: 'open'`)
	contains(t, dbml, `Ref: "dbo"."dbh_erd_orders"."user_id" > "dbo"."dbh_erd_users"."id" [delete: cascade]`)
	contains(t, f.run("erd", "check"), "OK: 2 tables, 1 declared and 0 inferred relations")
	contains(t, f.run("erd", "relations", "--format", "tsv"), "dbo.dbh_erd_orders\tuser_id\t>\tdbo.dbh_erd_users\tid")
}

func testERDOracle(t *testing.T, f *fixture, repo string) {
	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS DBH_ERD_ORDERS CASCADE CONSTRAINTS; DROP TABLE IF EXISTS DBH_ERD_USERS CASCADE CONSTRAINTS;", "--no-history")
	})
	f.run("exec", "srv", `CREATE TABLE DBH_ERD_USERS (
  id NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  email VARCHAR2(100) UNIQUE NOT NULL
);
CREATE TABLE DBH_ERD_ORDERS (
  id NUMBER GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY,
  user_id NUMBER NOT NULL REFERENCES DBH_ERD_USERS(id) ON DELETE CASCADE,
  code VARCHAR2(32)
);
CREATE INDEX idx_erd_orders_lower ON DBH_ERD_ORDERS (LOWER(code));
COMMENT ON TABLE DBH_ERD_USERS IS 'Oracle users table';
COMMENT ON COLUMN DBH_ERD_USERS.email IS 'User email';`, "--no-history")

	out := f.run("erd", "pull", "srv", "--include", "DBH_ERD_*", "--save")
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `DBH_ERD_USERS`)
	contains(t, dbml, `Note: 'Oracle users table'`)
	contains(t, dbml, `note: 'User email'`)
	contains(t, dbml, `[delete: cascade]`)
	contains(t, f.run("erd", "check"), "OK: 2 tables, 1 declared and 0 inferred relations")
}

func testERDTrino(t *testing.T, f *fixture, repo string) {
	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS memory.default.dbh_erd_users", "--no-history")
	})
	f.run("exec", "srv", "CREATE TABLE IF NOT EXISTS memory.default.dbh_erd_users (id BIGINT COMMENT 'user id', name VARCHAR)", "--no-history")

	out := f.run("erd", "pull", "srv", "--schema", "memory.default", "--include", "dbh_erd_*", "--save")
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `Table "default"."dbh_erd_users"`)
	contains(t, dbml, `note: 'user id'`)
	contains(t, f.run("erd", "check"), "OK: 1 tables, 0 declared and 0 inferred relations")

	tpchOut := filepath.Join(repo, "tpch.dbml")
	f.run("erd", "pull", "srv", "--schema", "tpch.tiny", "-o", tpchOut)
	contains(t, readFile(t, tpchOut), `Table "tiny"."customer"`)
}

func testERDDM(t *testing.T, f *fixture, repo string) {
	t.Cleanup(func() {
		f.run("exec", "srv", "DROP TABLE IF EXISTS DBH_ERD_ORDERS; DROP TABLE IF EXISTS DBH_ERD_USERS;", "--no-history")
	})
	f.run("exec", "srv", `
CREATE TABLE DBH_ERD_USERS (
  id INT IDENTITY(1,1) PRIMARY KEY,
  email VARCHAR(100) UNIQUE NOT NULL
);
CREATE TABLE DBH_ERD_ORDERS (
  id INT PRIMARY KEY,
  user_id INT NOT NULL REFERENCES DBH_ERD_USERS(id) ON DELETE CASCADE
);
COMMENT ON TABLE DBH_ERD_USERS IS 'DM users table';
`, "--no-history")

	out := f.run("erd", "pull", "srv", "--include", "DBH_ERD_*", "--save")
	contains(t, out, "Saved .dbh/schema.dbml")
	dbml := readFile(t, filepath.Join(repo, ".dbh", "schema.dbml"))
	contains(t, dbml, `DBH_ERD_USERS`)
	contains(t, dbml, `Note: 'DM users table'`)
	contains(t, f.run("erd", "check"), "OK: 2 tables, 1 declared and 0 inferred relations")
}
