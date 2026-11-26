package cassandra

import (
	"context"
	"testing"
)

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := (Driver{}).Open(context.Background(), "mysql://localhost:9042/ks", nil); err == nil {
		t.Fatal("expected error for non-cassandra DSN")
	}
}

func TestSplitTable(t *testing.T) {
	ks, tbl := splitTable("app", "events.logs")
	if ks != "events" || tbl != "logs" {
		t.Fatalf("splitTable = (%q, %q)", ks, tbl)
	}
	ks, tbl = splitTable("app", "users")
	if ks != "app" || tbl != "users" {
		t.Fatalf("splitTable = (%q, %q)", ks, tbl)
	}
}
