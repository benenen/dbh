// Package dbml implements DBML lexing, parsing, and AST types.
package dbml

import "fmt"

// Position is a 1-based source location.
type Position struct {
	Line   int
	Column int
}

// Error describes a lex or parse error at a source position.
type Error struct {
	Position Position
	Message  string
}

func (e Error) Error() string {
	return fmt.Sprintf("%d:%d: %s", e.Position.Line, e.Position.Column, e.Message)
}

// ParseOptions configures DBML parsing behavior.
type ParseOptions struct {
	RefsOnly bool
}

// Database holds the parsed top-level DBML declarations.
type Database struct {
	Tables []Table
	Refs   []Ref
	Enums  []Enum
}

// Table represents a DBML Table declaration.
type Table struct {
	SchemaName *string
	Name       string
	Alias      *string
	Note       *Note
	Fields     []Column
	Indexes    []Index
}

// Column represents a column inside a Table.
type Column struct {
	Name      string
	Type      ColumnType
	PK        bool
	Unique    bool
	NotNull   *bool
	Increment *bool
	DBDefault *DefaultValue
	Note      *Note
}

// ColumnType represents a column's type specification.
type ColumnType struct {
	SchemaName *string
	TypeName   string
	Args       string
	Suffix     string
}

// DefaultValue represents a column default value.
// Type is one of "string", "number", "boolean", "expression", "null".
type DefaultValue struct {
	Type  string
	Value any
}

// Note represents a table, column, index, or enum value note.
type Note struct {
	Value string
}

// Index represents an index entry inside a Table's indexes block.
type Index struct {
	Columns []IndexColumn
	Name    *string
	Type    *string
	PK      bool
	Unique  bool
	Note    *Note
}

// IndexColumn represents a column name or expression in an index.
// Type is "column" or "expression".
type IndexColumn struct {
	Type  string
	Value string
}

// Ref represents a relationship between two table endpoints.
type Ref struct {
	Name      *string
	Endpoints []RefEndpoint
	OnDelete  *string
	OnUpdate  *string
	Line      int
}

// RefEndpoint represents one side of a Ref.
// Relation is "*" or "1".
type RefEndpoint struct {
	SchemaName *string
	TableName  string
	FieldNames []string
	Relation   string
	Line       int
}

// Enum represents a DBML Enum declaration.
type Enum struct {
	SchemaName *string
	Name       string
	Values     []EnumValue
}

// EnumValue represents a single value inside an Enum.
type EnumValue struct {
	Name string
	Note *Note
}
