package dbh

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbh/internal/database"
)

func TestSafetyWriteAndDangerousDetection(t *testing.T) {
	sqlSyn := database.Syntax{}
	if isWriteStatement("SELECT * FROM users;", sqlSyn) {
		t.Fatal("SELECT should not be flagged as write")
	}
	if !isWriteStatement("INSERT INTO users VALUES (1);", sqlSyn) {
		t.Fatal("INSERT should be flagged as write")
	}
	if _, dangerous := dangerousWriteReason("DELETE FROM users WHERE id = 1;", sqlSyn); dangerous {
		t.Fatal("DELETE with WHERE should not be dangerous")
	}
	if _, dangerous := dangerousWriteReason("DELETE FROM users;", sqlSyn); !dangerous {
		t.Fatal("DELETE without WHERE should be dangerous")
	}
	if _, dangerous := dangerousWriteReason("UPDATE users SET a = 1;", sqlSyn); !dangerous {
		t.Fatal("UPDATE without WHERE should be dangerous")
	}
	if _, dangerous := dangerousWriteReason("DROP TABLE users;", sqlSyn); !dangerous {
		t.Fatal("DROP TABLE should be dangerous")
	}

	if !isWriteStatement("SET k v", database.Syntax{LineCommands: true}) {
		t.Fatal("Redis SET should be write")
	}
	if isWriteStatement("GET k", database.Syntax{LineCommands: true}) {
		t.Fatal("Redis GET should not be write")
	}
	if !isWriteStatement(`{"insert":"users","documents":[{"a":1}]}`, database.Syntax{JSONCommands: true}) {
		t.Fatal("Mongo insert should be write")
	}
	if isWriteStatement(`POST /users/_search {"query":{"match_all":{}}}`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("ES _search should not be write")
	}
	if !isWriteStatement(`DELETE /users`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("ES DELETE should be write")
	}

	// etcd / bbolt line commands
	if !isWriteStatement("PUT /app/k v", database.Syntax{LineCommands: true}) {
		t.Fatal("etcd PUT should be write")
	}
	if !isWriteStatement("CREATE BUCKET users", database.Syntax{LineCommands: true}) {
		t.Fatal("bbolt CREATE BUCKET should be write")
	}
	if _, dangerous := dangerousWriteReason("DELETE BUCKET users", database.Syntax{LineCommands: true}); !dangerous {
		t.Fatal("bbolt DELETE BUCKET should be dangerous on prod")
	}

	// Milvus / Qdrant read-only POST endpoints
	if isWriteStatement(`POST collections/list {}`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("Milvus collections/list should not be write")
	}
	if isWriteStatement(`POST entities/query {"collectionName":"c"}`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("Milvus entities/query should not be write")
	}
	if isWriteStatement(`POST points/scroll {"limit":10}`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("Qdrant points/scroll should not be write")
	}
	if !isWriteStatement(`POST points/delete {"points":[1]}`, database.Syntax{HTTPCommands: true}) {
		t.Fatal("Qdrant points/delete should be write")
	}

	// InfluxDB / OpenObserve writes
	if !isWriteStatement(`WRITE cpu,host=a value=1;`, sqlSyn) {
		t.Fatal("InfluxDB WRITE should be write")
	}
	if !isWriteStatement(`INGEST app_logs {"msg":"hi"};`, sqlSyn) {
		t.Fatal("OpenObserve INGEST should be write")
	}

	// Neo4j Cypher
	cypherSyn := database.Syntax{SlashComments: true}
	if isWriteStatement(`MATCH (n) RETURN n;`, cypherSyn) {
		t.Fatal("Cypher MATCH RETURN should not be write")
	}
	if !isWriteStatement(`MATCH (n) DELETE n;`, cypherSyn) {
		t.Fatal("Cypher MATCH DELETE should be write")
	}
	if _, dangerous := dangerousWriteReason(`MATCH (n) DELETE n;`, cypherSyn); !dangerous {
		t.Fatal("Cypher DELETE without WHERE should be dangerous on prod")
	}
	if _, dangerous := dangerousWriteReason(`MATCH (n) WHERE n.id = 1 DELETE n;`, cypherSyn); dangerous {
		t.Fatal("Cypher DELETE with WHERE should not be dangerous")
	}
}

func TestSessionReadOnlyAndProdGuards(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "ro", Driver: "sqlite", DSN: ":memory:", ReadOnly: true}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.Execute(ctx, "CREATE TABLE t (id INT);", "table", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}
	s.AllowWrite = true
	if err := s.Execute(ctx, "CREATE TABLE t (id INT);", "table", &bytes.Buffer{}); err != nil {
		t.Fatalf("expected allow-write to succeed: %v", err)
	}

	s.ReadOnly = false
	s.Env = "prod"
	s.AllowWrite = false
	if err := s.Execute(ctx, "INSERT INTO t VALUES (1);", "table", &bytes.Buffer{}); err != nil {
		t.Fatalf("safe INSERT should be allowed on prod: %v", err)
	}
	if err := s.Execute(ctx, "DELETE FROM t;", "table", &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "without WHERE") {
		t.Fatalf("expected dangerous DELETE error on prod, got %v", err)
	}
	if err := s.Execute(ctx, "DELETE FROM t WHERE id = 1;", "table", &bytes.Buffer{}); err != nil {
		t.Fatalf("DELETE with WHERE should succeed on prod: %v", err)
	}
}
