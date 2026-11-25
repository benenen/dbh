package erd

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/benenen/dbh/internal/dbml/introspect"
)

// PullOptions configures schema introspection for PullWithOptions.
type PullOptions struct {
	// Schemas lists PostgreSQL schemas to read (default: ["public"]).
	// For MySQL, the first entry is the database name. SQLite ignores it.
	Schemas []string
	// Include and Exclude are glob patterns (path.Match syntax) applied to
	// table names (or schema.table when the pattern contains a dot).
	Include []string
	Exclude []string
}

// Pull reads the schema through db and renders it as DBML with native types.
// For MySQL, schema is the database; for PostgreSQL, the schema (default
// public, comma-separated for multiple schemas); SQLite ignores it.
func Pull(ctx context.Context, db *sql.DB, driver, schema string) (string, error) {
	var schemas []string
	for _, part := range strings.Split(schema, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			schemas = append(schemas, trimmed)
		}
	}
	return PullWithOptions(ctx, db, driver, PullOptions{Schemas: schemas})
}

// PullWithOptions reads one or more schemas through db, applies optional table
// include/exclude filters, and renders DBML safe for re-parsing.
func PullWithOptions(ctx context.Context, db *sql.DB, driver string, opts PullOptions) (string, error) {
	if err := validateTablePatterns(opts.Include); err != nil {
		return "", fmt.Errorf("invalid --include pattern: %w", err)
	}
	if err := validateTablePatterns(opts.Exclude); err != nil {
		return "", fmt.Errorf("invalid --exclude pattern: %w", err)
	}

	var engine introspect.Engine
	var label string
	schemas := opts.Schemas
	switch driver {
	case "sqlite":
		engine, label = introspect.EngineSQLite, "SQLite"
		schemas = []string{""}
	case "postgres":
		engine, label = introspect.EnginePostgres, "PostgreSQL"
		if len(schemas) == 0 {
			schemas = []string{"public"}
		}
	case "mysql":
		engine, label = introspect.EngineMariaDB, "MySQL"
		if len(schemas) == 0 || schemas[0] == "" {
			return "", errors.New("no database selected; use --db")
		}
		schemas = schemas[:1]
		if err := checkMySQLVersion(ctx, db); err != nil {
			return "", err
		}
	case "mssql":
		engine, label = introspect.EngineMSSQL, "SQL Server"
		if len(schemas) == 0 {
			schemas = []string{""}
		}
	case "oracle":
		engine, label = introspect.EngineOracle, "Oracle"
		if len(schemas) == 0 {
			schemas = []string{""}
		}
	case "dm":
		engine, label = introspect.EngineDM, "Dameng"
		if len(schemas) == 0 {
			schemas = []string{""}
		}
	case "trino":
		engine, label = introspect.EngineTrino, "Trino"
		if len(schemas) == 0 {
			schemas = []string{""}
		}
	default:
		return "", fmt.Errorf("erd pull is not supported for %s", driver)
	}

	merged := &introspect.DBSchema{}
	seenTables := map[string]bool{}
	seenEnums := map[string]bool{}
	seenFKs := map[string]bool{}
	for _, sch := range schemas {
		s, err := introspect.Introspect(ctx, db, engine, sch)
		if err != nil {
			return "", err
		}
		for _, t := range s.Tables {
			key := schemaTableKey(t.Schema, t.Name)
			if seenTables[key] {
				continue
			}
			seenTables[key] = true
			merged.Tables = append(merged.Tables, t)
		}
		for _, e := range s.Enums {
			key := schemaTableKey(e.Schema, e.Name)
			if seenEnums[key] {
				continue
			}
			seenEnums[key] = true
			merged.Enums = append(merged.Enums, e)
		}
		for _, fk := range s.FKs {
			key := fkKey(fk)
			if seenFKs[key] {
				continue
			}
			seenFKs[key] = true
			merged.FKs = append(merged.FKs, fk)
		}
	}

	filterDBSchema(merged, opts.Include, opts.Exclude)
	return introspect.GenerateDBML(merged, label, false), nil
}

func schemaTableKey(schema, name string) string {
	if schema == "" || schema == "public" {
		return name
	}
	return schema + "." + name
}

func fkKey(fk *introspect.ForeignKey) string {
	return schemaTableKey(fk.Schema, fk.TableName) + "(" + strings.Join(fk.Columns, ",") + ")>" +
		schemaTableKey(fk.RefSchema, fk.RefTable) + "(" + strings.Join(fk.RefColumns, ",") + ")"
}

func validateTablePatterns(patterns []string) error {
	for _, p := range patterns {
		if _, err := path.Match(p, "table"); err != nil {
			return fmt.Errorf("%q: %w", p, err)
		}
	}
	return nil
}

func matchAnyTablePattern(patterns []string, schema, name string) bool {
	display := schemaTableKey(schema, name)
	for _, p := range patterns {
		target := name
		if strings.Contains(p, ".") {
			target = display
		}
		if ok, _ := path.Match(p, target); ok {
			return true
		}
		if ok, _ := path.Match(strings.ToLower(p), strings.ToLower(target)); ok {
			return true
		}
	}
	return false
}

func filterDBSchema(s *introspect.DBSchema, include, exclude []string) {
	if len(include) == 0 && len(exclude) == 0 {
		return
	}
	kept := make([]*introspect.Table, 0, len(s.Tables))
	keptSet := make(map[string]bool, len(s.Tables))
	for _, t := range s.Tables {
		if len(include) > 0 && !matchAnyTablePattern(include, t.Schema, t.Name) {
			continue
		}
		if len(exclude) > 0 && matchAnyTablePattern(exclude, t.Schema, t.Name) {
			continue
		}
		kept = append(kept, t)
		keptSet[schemaTableKey(t.Schema, t.Name)] = true
	}
	s.Tables = kept

	fks := make([]*introspect.ForeignKey, 0, len(s.FKs))
	for _, fk := range s.FKs {
		if keptSet[schemaTableKey(fk.Schema, fk.TableName)] && keptSet[schemaTableKey(fk.RefSchema, fk.RefTable)] {
			fks = append(fks, fk)
		}
	}
	s.FKs = fks
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
