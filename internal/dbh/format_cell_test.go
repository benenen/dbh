package dbh

import (
	"bytes"
	"context"
	"encoding/hex"
	"strings"
	"testing"
	"time"

	"github.com/benenen/dbh/internal/database"
)

func TestFormatCellValueBinaryAndVector(t *testing.T) {
	shortBin := database.Binary{0xde, 0xad, 0xbe, 0xef}
	text, jv := formatCellValue(shortBin, 200)
	if text != "0xdeadbeef" {
		t.Fatalf("short binary text = %q", text)
	}
	if _, ok := jv.(database.Binary); !ok {
		t.Fatalf("expected database.Binary jsonVal, got %T", jv)
	}

	largeBin := make(database.Binary, 64)
	for i := range largeBin {
		largeBin[i] = byte(i)
	}
	text, _ = formatCellValue(largeBin, 200)
	if !strings.HasPrefix(text, "<binary 64 B, 0x0001020304050607...>") {
		t.Fatalf("large binary summary = %q", text)
	}
	fullText, _ := formatCellValue(largeBin, 0)
	if fullText != "0x"+hex.EncodeToString(largeBin) {
		t.Fatalf("untruncated binary = %q", fullText)
	}

	vec := "[0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9, 1.0]"
	vText, _ := formatCellValue(vec, 200)
	if vText != "<vector[10]: [0.1, 0.2, 0.3, ...]>" {
		t.Fatalf("vector summary = %q", vText)
	}
	if shortVec, _ := formatCellValue("[1, 2, 3]", 200); shortVec != "[1, 2, 3]" {
		t.Fatalf("short array should not summarize: %q", shortVec)
	}

	longText := strings.Repeat("a", 250)
	tText, _ := formatCellValue(longText, 200)
	if !strings.HasSuffix(tText, "... (250 chars)") || len([]rune(tText)) > 220 {
		t.Fatalf("truncated text = %q", tText)
	}
}

func TestSummarizeJSONValue(t *testing.T) {
	obj := map[string]any{
		"id":  1,
		"vec": []any{0.1, 0.2, 0.3, 0.4, 0.5, 0.6, 0.7, 0.8, 0.9},
		"msg": strings.Repeat("x", 210),
	}
	summarized := summarizeJSONValue(obj, 200).(map[string]any)
	if summarized["vec"] != "<vector[9]: [0.1, 0.2, 0.3, ...]>" {
		t.Fatalf("summarized vec = %v", summarized["vec"])
	}
	if !strings.HasSuffix(summarized["msg"].(string), "... (210 chars)") {
		t.Fatalf("summarized msg = %v", summarized["msg"])
	}
}

func TestSQLiteBlobVectorAndTruncationFormats(t *testing.T) {
	ctx := context.Background()
	s, err := openSession(ctx, Profile{Name: "test", Driver: "sqlite", DSN: ":memory:"}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	vec := "[0.01, 0.02, 0.03, 0.04, 0.05, 0.06, 0.07, 0.08, 0.09, 0.10]"
	longNote := strings.Repeat("中", 220)
	setup := `
CREATE TABLE items (id INTEGER PRIMARY KEY, payload BLOB, embedding TEXT, note TEXT);
INSERT INTO items VALUES (1, X'89504E470D0A1A0A0000000D4948445200000001000000010802000000907753DE00000000', '` + vec + `', '` + longNote + `');
`
	if err := s.Run(ctx, setup, "table", &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	var tableOut bytes.Buffer
	if err := s.Execute(ctx, "SELECT id, payload, embedding, note FROM items;", "table", &tableOut); err != nil {
		t.Fatal(err)
	}
	tbl := tableOut.String()
	if !strings.Contains(tbl, "<binary 37 B,") || !strings.Contains(tbl, "0x89504e470d0a") {
		t.Fatalf("table missing binary summary: %s", tbl)
	}
	if !strings.Contains(tbl, "<vector[10]:") || !strings.Contains(tbl, "0.01, 0.02,") {
		t.Fatalf("table missing vector summary: %s", tbl)
	}
	if !strings.Contains(tbl, "... (220 chars)") {
		t.Fatalf("table missing truncated note: %s", tbl)
	}

	// TSV and JSON remain lossless by default.
	var tsvOut bytes.Buffer
	if err := s.Execute(ctx, "SELECT payload, embedding, note FROM items;", "tsv", &tsvOut); err != nil {
		t.Fatal(err)
	}
	tsv := tsvOut.String()
	if !strings.Contains(tsv, "0x89504e47") || !strings.Contains(tsv, vec) || !strings.Contains(tsv, longNote) {
		t.Fatalf("tsv should be lossless by default: %s", tsv)
	}

	var jsonOut bytes.Buffer
	if err := s.Execute(ctx, "SELECT payload, embedding FROM items;", "json", &jsonOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut.String(), `"payload":"0x89504e47`) || !strings.Contains(jsonOut.String(), vec) {
		t.Fatalf("json should preserve hex binary and full vector by default: %s", jsonOut.String())
	}

	// Explicit --max-cell-width applies to json/tsv too.
	s.MaxCellWidth = 50
	s.MaxCellWidthSet = true
	jsonOut.Reset()
	if err := s.Execute(ctx, "SELECT payload, embedding FROM items;", "json", &jsonOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jsonOut.String(), "<binary 37 B") || !strings.Contains(jsonOut.String(), "<vector[10]:") {
		t.Fatalf("explicit max-cell-width should summarize json: %s", jsonOut.String())
	}
}
