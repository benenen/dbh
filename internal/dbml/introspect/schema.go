// Package introspect reads database schemas via *sql.DB and renders DBML.
package introspect

// Engine identifies a supported database backend.
type Engine int

const (
	EngineMariaDB Engine = iota
	EnginePostgres
	EngineSQLite
	EngineMSSQL
	EngineOracle
	EngineDM
	EngineTrino
)

// Column represents a single column in a table.
type Column struct {
	Name         string
	Type         string
	Default      *string
	DefaultType  string // "number", "string", "boolean", "expression", "increment", "null"
	IsNullable   bool
	IsPrimaryKey bool
	IsUnique     bool
	IsIncrement  bool
	Comment      string
	Generated    string
}

// Index represents a non-constraint index on a table.
type Index struct {
	Name     string
	Columns  []string
	Exprs    []bool
	IsUnique bool
	Type     string
}

func (idx *Index) addKey(value string, expr bool) {
	idx.Columns = append(idx.Columns, value)
	idx.Exprs = append(idx.Exprs, expr)
}

// Constraint represents a PRIMARY KEY or UNIQUE constraint.
type Constraint struct {
	Name      string
	Columns   []string
	IsPrimary bool
}

// ForeignKey represents a FOREIGN KEY constraint.
type ForeignKey struct {
	Name       string
	TableName  string
	Schema     string
	Columns    []string
	RefSchema  string
	RefTable   string
	RefColumns []string
	OnDelete   string
	OnUpdate   string
}

// CheckConstraint represents a CHECK constraint.
type CheckConstraint struct {
	Name       string
	ColumnName string
	Expression string
}

// Table represents a database table.
type Table struct {
	Schema           string
	Name             string
	Comment          string
	Columns          []*Column
	Indexes          []*Index
	Constraints      []*Constraint
	CheckConstraints []*CheckConstraint
}

// Enum represents a named enum type or inline enum column.
type Enum struct {
	Schema string
	Name   string
	Values []string
}

// DBSchema holds the full introspected schema.
type DBSchema struct {
	Tables []*Table
	Enums  []*Enum
	FKs    []*ForeignKey
}
