package erd

import (
	"strings"
	"testing"
)

func TestRenderMigrationDDL(t *testing.T) {
	oldModel := ParseDBML("old", "old.dbml", `
Table users {
  id int [pk]
  email varchar [not null]
  legacy text
}
Table temp_log {
  id int [pk]
}
`)
	newModel := ParseDBML("new", "new.dbml", `
Table users {
  id int [pk]
  email text [not null]
  role varchar [not null, default: 'member']
}
Table orders {
  id int [pk]
  user_id int [not null]
}
`)
	diff := DiffModels(oldModel, newModel)
	pgDDL := diff.RenderMigrationDDL(oldModel, newModel, "postgres")
	for _, want := range []string{
		`CREATE TABLE "orders"`,
		`DROP TABLE "temp_log";`,
		`ALTER TABLE "users" ADD COLUMN "role" varchar NOT NULL DEFAULT 'member';`,
		`ALTER TABLE "users" DROP COLUMN "legacy";`,
		`ALTER TABLE "users" ALTER COLUMN "email" TYPE text;`,
	} {
		if !strings.Contains(pgDDL, want) {
			t.Fatalf("postgres DDL missing %q:\n%s", want, pgDDL)
		}
	}

	mysqlDDL := diff.RenderMigrationDDL(oldModel, newModel, "mysql")
	if !strings.Contains(mysqlDDL, "ALTER TABLE `users` MODIFY COLUMN `email` text NOT NULL;") {
		t.Fatalf("mysql DDL missing MODIFY COLUMN:\n%s", mysqlDDL)
	}
}
