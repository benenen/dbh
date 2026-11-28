package dbh

import (
	"regexp"
	"strings"
	"testing"

	"github.com/benenen/dbh/internal/database"
)

var ansiCSI = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestHighlightSQLPreservesText(t *testing.T) {
	syntax := database.Syntax{DollarQuotes: true, EscapeStringPrefix: true, NestedComments: true}
	samples := []string{
		"SELECT id, name FROM users WHERE age >= 18 AND active = true;",
		"SELECT 'it''s /* not comment */' AS s, 3.14 AS n -- trailing comment\nFROM t;",
		"SELECT $$hello$$ /* block /* nested */ */ FROM tbl;",
		`  \ddl users`,
		"SELECT 名称 FROM 用户表 WHERE id = 42;",
	}
	for _, sample := range samples {
		highlighted := highlightSQL([]rune(sample), syntax)
		stripped := ansiCSI.ReplaceAllString(highlighted, "")
		if stripped != sample {
			t.Fatalf("stripped mismatch:\ngot:  %q\nwant: %q", stripped, sample)
		}
		if highlighted == sample && !strings.Contains(sample, `"`) {
			t.Fatalf("expected ANSI highlighting for %q", sample)
		}
	}
}

func TestSplitSQLDelimiterAndCompoundTrigger(t *testing.T) {
	triggerSQL := `
CREATE TRIGGER trg_audit AFTER INSERT ON items
BEGIN
  INSERT INTO log VALUES (NEW.id);
  UPDATE stats SET cnt = cnt + 1;
END;
SELECT 1;`
	stmts, rest, delim, err := splitSQLWithDelimiter(triggerSQL, "sqlite", ";")
	if err != nil {
		t.Fatalf("splitSQLWithDelimiter trigger: %v", err)
	}
	if delim != ";" || rest != "" || len(stmts) != 2 {
		t.Fatalf("got len=%d rest=%q delim=%q stmts=%q", len(stmts), rest, delim, stmts)
	}
	if !strings.Contains(stmts[0], "END") || stmts[1] != "SELECT 1" {
		t.Fatalf("unexpected trigger split: %q", stmts)
	}

	mysqlProc := `DELIMITER //
CREATE PROCEDURE p()
BEGIN
  SELECT 1;
  SELECT 2;
END//
DELIMITER ;
SELECT 3;`
	stmts, rest, delim, err = splitSQLWithDelimiter(mysqlProc, "mysql", ";")
	if err != nil {
		t.Fatalf("splitSQLWithDelimiter DELIMITER: %v", err)
	}
	if delim != ";" || rest != "" || len(stmts) != 2 {
		t.Fatalf("got len=%d rest=%q delim=%q stmts=%q", len(stmts), rest, delim, stmts)
	}
	if !strings.Contains(stmts[0], "SELECT 2;") || stmts[1] != "SELECT 3" {
		t.Fatalf("unexpected DELIMITER split: %q", stmts)
	}
}
