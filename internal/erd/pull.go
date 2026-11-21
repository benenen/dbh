package erd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/mevdschee/dbml-tools/introspect"
)

// Pull reads the schema through db and renders it as DBML with native types.
// For MySQL, schema is the database; for PostgreSQL, the schema (default
// public); SQLite ignores it.
func Pull(ctx context.Context, db *sql.DB, driver, schema string) (string, error) {
	var engine introspect.Engine
	var label string
	switch driver {
	case "sqlite":
		engine, label = introspect.EngineSQLite, "SQLite"
	case "postgres":
		engine, label = introspect.EnginePostgres, "PostgreSQL"
		if schema == "" {
			schema = "public"
		}
	case "mysql":
		engine, label = introspect.EngineMariaDB, "MySQL"
		if schema == "" {
			return "", errors.New("no database selected; use --db")
		}
		if err := checkMySQLVersion(ctx, db); err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("erd pull is not supported for %s", driver)
	}
	s, err := introspect.Introspect(ctx, db, engine, schema)
	if err != nil {
		return "", err
	}
	return introspect.GenerateDBML(s, label, false), nil
}

// checkMySQLVersion rejects MySQL before 8.0.16, which lacks
// information_schema.check_constraints. MariaDB 10.2.1+ has it.
func checkMySQLVersion(ctx context.Context, db *sql.DB) error {
	var version string
	if err := db.QueryRowContext(ctx, "SELECT VERSION()").Scan(&version); err != nil {
		return err
	}
	parts := strings.SplitN(strings.SplitN(version, "-", 2)[0], ".", 3)
	numbers := make([]int, 3)
	for i := range parts {
		n, err := strconv.Atoi(strings.TrimFunc(parts[i], func(r rune) bool { return r < '0' || r > '9' }))
		if err != nil {
			return nil // unknown format: let the catalog queries decide
		}
		numbers[i] = n
	}
	if strings.Contains(strings.ToLower(version), "mariadb") {
		if numbers[0] < 10 || (numbers[0] == 10 && (numbers[1] < 2 || (numbers[1] == 2 && numbers[2] < 1))) {
			return fmt.Errorf("erd pull requires MariaDB 10.2.1 or later (server is %s)", version)
		}
		return nil
	}
	if numbers[0] < 8 || (numbers[0] == 8 && numbers[1] == 0 && numbers[2] < 16) {
		return fmt.Errorf("erd pull requires MySQL 8.0.16 or later (server is %s)", version)
	}
	return nil
}

// Save atomically replaces path with content, creating its directory.
func Save(path, content string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".erd-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
