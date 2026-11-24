package dm

import (
	"context"
	"net"
	"testing"
)

func TestOpenValidationAndProxyRejection(t *testing.T) {
	d := Driver{}
	if _, err := d.Open(context.Background(), "mysql://localhost:5236", nil); err == nil {
		t.Fatal("expected error for non-dm DSN")
	}
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		return nil, nil
	}
	if _, err := d.Open(context.Background(), "dm://SYSDBA:SYSDBA@localhost:5236", dial); err == nil {
		t.Fatal("expected error when dial is non-nil")
	}
}

func TestSplitTable(t *testing.T) {
	owner, name := splitTable(`"SYSDBA"."USERS"`)
	if owner != "SYSDBA" || name != "USERS" {
		t.Fatalf("splitTable = (%q, %q)", owner, name)
	}
}
