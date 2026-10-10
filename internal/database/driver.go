// Package database defines the database-specific behavior used by a CLI session.
package database

import (
	"context"
	"fmt"
	"net"
	"strings"
)

// Dial opens a network connection to the database server. A nil Dial connects directly.
type Dial func(ctx context.Context, network, address string) (net.Conn, error)

func (d Dial) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d(ctx, network, address)
}
func (d Dial) Dial(network, address string) (net.Conn, error) {
	return d(context.Background(), network, address)
}

// Driver queries metadata through the session's existing connection. The caller
// supplies a bounded context and executes returned queries on that same connection.
type Driver interface {
	Open(context.Context, string, Dial) (Connection, error)
	Syntax() Syntax
	// A nil connection means the existing connection changed database in place. A new connection
	// must already be ready before the caller closes the old session.
	SwitchDatabase(context.Context, Connection, string, string, Dial) (Connection, error)
	Databases(context.Context, Connection) ([]string, error)
	Tables(context.Context, Connection) ([]string, error)
	Columns(context.Context, Connection, string) ([]string, error)
	ColumnDetails(context.Context, Connection, string) (Query, error)
	Indexes(context.Context, Connection, string) (Query, error)
}

// ColumnNames is implemented by drivers that can list the column names of every
// table in the current database with one query instead of one query per table.
type ColumnNames interface {
	ColumnNames(context.Context, Connection) ([]string, error)
}

// Syntax contains the lexical differences consumed by the shared SQL splitter.
type Syntax struct {
	BackslashEscapes        bool
	BacktickEscapes         bool
	NestedComments          bool
	DashCommentNeedsSpace   bool
	HashComments            bool
	ExecutableComments      bool
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

// SplitIdentifier splits a dotted name outside double-quote, backtick and bracket
// quoting, then unquotes each part.
func SplitIdentifier(name string) []string {
	var parts []string
	start, quote := 0, byte(0)
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '`':
			quote = c
		case c == '[':
			quote = ']'
		case c == '.':
			parts = append(parts, unquote(name[start:i]))
			start = i + 1
		}
	}
	return append(parts, unquote(name[start:]))
}

func unquote(name string) string {
	if len(name) < 2 {
		return name
	}
	switch open, end := name[0], name[len(name)-1]; {
	case open == '"' && end == '"', open == '`' && end == '`':
		return strings.ReplaceAll(name[1:len(name)-1], string(open)+string(open), string(open))
	case open == '[' && end == ']':
		return name[1 : len(name)-1]
	}
	return name
}
