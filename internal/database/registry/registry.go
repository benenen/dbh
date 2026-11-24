// Package registry selects the implementation for a saved connection's driver.
package registry

import (
	"fmt"
	"strings"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/clickhouse"
	"github.com/benenen/dbh/internal/database/dm"
	"github.com/benenen/dbh/internal/database/elasticsearch"
	"github.com/benenen/dbh/internal/database/mongo"
	"github.com/benenen/dbh/internal/database/mssql"
	"github.com/benenen/dbh/internal/database/mysql"
	"github.com/benenen/dbh/internal/database/oracle"
	"github.com/benenen/dbh/internal/database/postgres"
	"github.com/benenen/dbh/internal/database/redis"
	"github.com/benenen/dbh/internal/database/sqlite"
	"github.com/benenen/dbh/internal/database/trino"
)

var drivers = []string{
	"sqlite",
	"postgres",
	"mysql",
	"mssql",
	"oracle",
	"trino",
	"dm",
	"mongo",
	"clickhouse",
	"redis",
	"elasticsearch",
}

// Drivers returns the supported driver names.
func Drivers() []string {
	out := make([]string, len(drivers))
	copy(out, drivers)
	return out
}

func Lookup(name string) (database.Driver, error) {
	switch name {
	case "clickhouse":
		return clickhouse.Driver{}, nil
	case "dm":
		return dm.Driver{}, nil
	case "elasticsearch":
		return elasticsearch.Driver{}, nil
	case "mongo":
		return mongo.Driver{}, nil
	case "mssql":
		return mssql.Driver{}, nil
	case "mysql":
		return mysql.Driver{}, nil
	case "oracle":
		return oracle.Driver{}, nil
	case "postgres":
		return postgres.Driver{}, nil
	case "redis":
		return redis.Driver{}, nil
	case "sqlite":
		return sqlite.Driver{}, nil
	case "trino":
		return trino.Driver{}, nil
	default:
		return nil, fmt.Errorf("unsupported driver %q (use %s)", name, strings.Join(drivers, ", "))
	}
}
