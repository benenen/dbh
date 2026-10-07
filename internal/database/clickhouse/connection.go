package clickhouse

import (
	"context"
	"fmt"
	"strings"
	"unicode"

	"github.com/benenen/dbh/internal/database"
)

// ClickHouse requires Exec for statements without a result block.
type connection struct{ database.Connection }

func (c *connection) Query(ctx context.Context, query string, args ...any) (database.Rows, error) {
	switch firstWord(query) {
	case "CREATE", "ALTER", "DROP", "TRUNCATE", "RENAME", "INSERT", "SET", "OPTIMIZE", "SYSTEM", "GRANT", "REVOKE", "ATTACH", "DETACH":
		if err := c.Exec(ctx, query, args...); err != nil {
			return nil, err
		}
		return emptyRows{}, nil
	default:
		return c.Connection.Query(ctx, query, args...)
	}
}
func firstWord(query string) string {
	for {
		query = strings.TrimSpace(query)
		if strings.HasPrefix(query, "--") || strings.HasPrefix(query, "#") {
			i := strings.IndexByte(query, '\n')
			if i < 0 {
				return ""
			}
			query = query[i+1:]
			continue
		}
		if strings.HasPrefix(query, "/*") {
			i := strings.Index(query[2:], "*/")
			if i < 0 {
				return ""
			}
			query = query[i+4:]
			continue
		}
		end := strings.IndexFunc(query, func(r rune) bool { return !unicode.IsLetter(r) })
		if end < 0 {
			end = len(query)
		}
		return strings.ToUpper(query[:end])
	}
}

type emptyRows struct{}

func (emptyRows) Columns() ([]string, error) { return nil, nil }
func (emptyRows) Next() bool                 { return false }
func (emptyRows) Row() (database.Row, error) { return database.Row{}, fmt.Errorf("no result row") }
func (emptyRows) Err() error                 { return nil }
func (emptyRows) NextResultSet() bool        { return false }
func (emptyRows) Close() error               { return nil }
