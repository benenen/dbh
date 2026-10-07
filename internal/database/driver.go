// Package database defines the database-specific behavior used by a CLI session.
package database

import (
	"context"
	"fmt"
)

// Driver queries metadata through the session's existing connection. The caller
// supplies a bounded context and executes returned queries on that same connection.
type Driver interface {
	Open(context.Context, string) (Connection, error)
	Syntax() Syntax
	// A nil connection means the existing connection changed database in place. A new connection
	// must already be ready before the caller closes the old session.
	SwitchDatabase(context.Context, Connection, string, string) (Connection, error)
	Databases(context.Context, Connection) ([]string, error)
	Tables(context.Context, Connection) ([]string, error)
	Columns(context.Context, Connection, string) ([]string, error)
	ColumnDetails(context.Context, Connection, string) (Query, error)
	Indexes(context.Context, Connection, string) (Query, error)
}

// Syntax contains the lexical differences consumed by the shared SQL splitter.
type Syntax struct {
	BackslashEscapes        bool
	DashCommentNeedsSpace   bool
	HashComments            bool
	EscapeStringPrefix      bool
	BracketIdentifiers      bool
	DollarQuotes            bool
	JSONCommands            bool
	FoldUnquotedIdentifiers bool
	CompletionWords         []string
}

// Query keeps metadata values separate from the command text.
type Query struct {
	Text    string
	Args    []any
	Heading string
}

func Names(ctx context.Context, conn Connection, query Query) ([]string, error) {
	rows, err := conn.Query(ctx, query.Text, query.Args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	names := []string{}
	for rows.Next() {
		row, err := rows.Row()
		if err != nil {
			return nil, err
		}
		if len(row.Values) != 1 {
			return nil, fmt.Errorf("expected one metadata column")
		}
		name, ok := row.Values[0].(string)
		if !ok {
			return nil, fmt.Errorf("expected a metadata name, got %T", row.Values[0])
		}
		names = append(names, name)
	}
	return names, rows.Err()
}
