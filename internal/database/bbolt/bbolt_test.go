package bbolt

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

func TestBBoltRejectsProxy(t *testing.T) {
	d := Driver{}
	_, err := d.Open(context.Background(), "bbolt://"+filepath.Join(t.TempDir(), "test.db"), func(ctx context.Context, network, addr string) (net.Conn, error) {
		return nil, nil
	})
	if err == nil {
		t.Fatal("expected error when --proxy dial is provided")
	}
}

func TestBBoltDriverOperations(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	d := Driver{}
	conn, err := d.Open(context.Background(), "bbolt://"+dbPath, nil)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.Exec(context.Background(), `CREATE BUCKET users`); err != nil {
		t.Fatalf("CREATE BUCKET failed: %v", err)
	}
	if err := conn.Exec(context.Background(), `PUT users u1 "alice"`); err != nil {
		t.Fatalf("PUT failed: %v", err)
	}

	rows, err := conn.Query(context.Background(), `GET users u1`)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	if !rows.Next() {
		t.Fatal("expected row for GET users u1")
	}
	row, err := rows.Row()
	if err != nil {
		t.Fatalf("Row failed: %v", err)
	}
	if row.Values[1] != "alice" {
		t.Fatalf("expected alice, got %v", row.Values[1])
	}
	_ = rows.Close()

	tables, err := d.Tables(context.Background(), conn)
	if err != nil {
		t.Fatalf("Tables failed: %v", err)
	}
	if len(tables) != 1 || tables[0] != "users" {
		t.Fatalf("unexpected tables: %v", tables)
	}

	if err := conn.Exec(context.Background(), `DEL users u1`); err != nil {
		t.Fatalf("DEL failed: %v", err)
	}
}
