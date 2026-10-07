// Package registry selects the implementation for a saved connection's driver.
package registry

import (
	"fmt"
	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/clickhouse"
	"github.com/benenen/dbh/internal/database/mongo"
	"github.com/benenen/dbh/internal/database/mysql"
	"github.com/benenen/dbh/internal/database/postgres"
	"github.com/benenen/dbh/internal/database/sqlite"
)

func Lookup(name string) (database.Driver, error) {
	switch name {
	case "clickhouse":
		return clickhouse.Driver{}, nil
	case "mongo":
		return mongo.Driver{}, nil
	case "mysql":
		return mysql.Driver{}, nil
	case "postgres":
		return postgres.Driver{}, nil
	case "sqlite":
		return sqlite.Driver{}, nil
	default:
		return nil, fmt.Errorf("unsupported driver %q (use sqlite, postgres, mysql, mongo or clickhouse)", name)
	}
}
