package oracle

import (
	"context"
	"testing"
)

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := (Driver{}).Open(context.Background(), "postgres://localhost/db", nil); err == nil {
		t.Fatal("expected error for non-oracle DSN")
	}
}

func TestSplitTable(t *testing.T) {
	owner, name := splitTable(`"HR"."EMPLOYEES"`)
	if owner != "HR" || name != "EMPLOYEES" {
		t.Fatalf("splitTable = (%q, %q)", owner, name)
	}
}
