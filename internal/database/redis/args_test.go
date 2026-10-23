package redis

import (
	"reflect"
	"testing"
)

func TestSplitArgs(t *testing.T) {
	for _, tt := range []struct {
		line string
		want []string
	}{
		{"", nil},
		{"  GET   key ", []string{"GET", "key"}},
		{`SET k "a b\n\x41\"" 'it\'s \n'`, []string{"SET", "k", "a b\nA\"", `it's \n`}},
		{`SET k ""`, []string{"SET", "k", ""}},
		{`SET k a"b`, []string{"SET", "k", `a"b`}},
		{"SET k 你好", []string{"SET", "k", "你好"}},
	} {
		got, err := splitArgs(tt.line)
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("splitArgs(%q) = %q, %v; want %q", tt.line, got, err, tt.want)
		}
	}
	for _, line := range []string{`GET "open`, `GET 'open`, `GET "a"b`} {
		if _, err := splitArgs(line); err == nil {
			t.Errorf("splitArgs(%q) succeeded", line)
		}
	}
}

func TestReplyRows(t *testing.T) {
	r := replyRows(map[any]any{"b": []any{"x", int64(1)}, "a": nil})
	var got [][]any
	for r.Next() {
		row, _ := r.Row()
		got = append(got, row.Values)
	}
	want := [][]any{{"a", nil}, {"b", `["x",1]`}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows = %q; want %q", got, want)
	}
}
