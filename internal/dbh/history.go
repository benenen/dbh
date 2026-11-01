package dbh

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// rewriteHistory atomically replaces the history, preserving complete statements.
func rewriteHistory(s Store, name string, entries []string) error {
	dir := filepath.Dir(historyPath(s, name))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".history-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(f.Name()) }()
	encoder := json.NewEncoder(f)
	for _, entry := range entries {
		if err := encoder.Encode(entry); err != nil {
			_ = f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), historyPath(s, name))
}

// retainHistory keeps the latest occurrence of each complete statement.
func retainHistory(entries []string, limit int) []string {
	seen := make(map[string]bool)
	result := make([]string, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		q := entries[i]
		if seen[q] {
			continue
		}
		seen[q] = true
		result = append(result, q)
		if limit > 0 && len(result) == limit {
			break
		}
	}
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return result
}

func updateHistory(s Store, name string, change func([]string) []string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid connection name")
	}
	dir := filepath.Dir(historyPath(s, name))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(dir, "."+name+".lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()
	entries, err := readHistory(s, name)
	if err != nil {
		return err
	}
	return rewriteHistory(s, name, change(entries))
}

// seekHistoryTail avoids scanning an entire legacy history file at startup.
// Each JSONL record is one physical line, even for multi-line SQL.
func seekHistoryTail(f *os.File, limit int) error {
	if limit <= 0 {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	end := info.Size()
	buf := make([]byte, 64*1024)
	lines := 0
	for end > 0 {
		start := max(int64(0), end-int64(len(buf)))
		block := buf[:end-start]
		if _, err := f.ReadAt(block, start); err != nil {
			return err
		}
		for i := len(block) - 1; i >= 0; i-- {
			if block[i] != '\n' || start+int64(i) == info.Size()-1 {
				continue
			}
			lines++
			if lines == limit {
				_, err := f.Seek(start+int64(i)+1, 0)
				return err
			}
		}
		end = start
	}
	return nil
}
