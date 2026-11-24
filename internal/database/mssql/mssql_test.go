package mssql

import (
	"context"
	"testing"
)

func TestSyntaxAndSplit(t *testing.T) {
	d := Driver{}
	if !d.Syntax().BracketIdentifiers {
		t.Fatal("expected BracketIdentifiers to be true")
	}
	schema, name := splitTable("[dbo].[user_orders]")
	if schema != "dbo" || name != "user_orders" {
		t.Fatalf("splitTable = (%q, %q)", schema, name)
	}
	schema, name = splitTable("users")
	if schema != "" || name != "users" {
		t.Fatalf("splitTable = (%q, %q)", schema, name)
	}
}

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := (Driver{}).Open(context.Background(), "://bad", nil); err == nil {
		t.Fatal("expected error for invalid DSN")
	}
}
