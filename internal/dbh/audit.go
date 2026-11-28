package dbh

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
	"github.com/spf13/cobra"
)

type AuditEntry struct {
	Timestamp    string `json:"timestamp"`
	Connection   string `json:"connection"`
	Driver       string `json:"driver"`
	Env          string `json:"env,omitempty"`
	Database     string `json:"database,omitempty"`
	Query        string `json:"query"`
	RowsAffected *int64 `json:"rows_affected,omitempty"`
	ElapsedMs    int64  `json:"elapsed_ms"`
	Success      bool   `json:"success"`
	Error        string `json:"error,omitempty"`
}

func auditPath(s Store) string {
	return filepath.Join(s.Dir, "audit.jsonl")
}

func (s Store) appendAudit(entry AuditEntry) error {
	if err := s.prepare(); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(s.Dir, ".audit.lock"))
	if err := lock.Lock(); err != nil {
		return err
	}
	defer func() { _ = lock.Unlock() }()

	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(auditPath(s), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s Store) readAudit() ([]AuditEntry, error) {
	f, err := os.Open(auditPath(s))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var entries []AuditEntry
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), 4*1024*1024)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" {
			continue
		}
		var e AuditEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("invalid audit.jsonl: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, scan.Err()
}

func addAuditCommand(root *cobra.Command, store func() (Store, error), _ cobra.CompletionFunc) {
	var connFilter, envFilter, format string
	var failedOnly bool
	var limit int

	c := &cobra.Command{
		Use:               "audit",
		Short:             "Inspect production write audit log",
		Args:              cobra.NoArgs,
		ValidArgsFunction: cobra.NoFileCompletions,
	}
	c.Flags().StringVar(&connFilter, "connection", "", "Filter by connection name")
	c.Flags().StringVar(&envFilter, "env", "", "Filter by environment")
	c.Flags().BoolVar(&failedOnly, "failed", false, "Show only failed statements")
	c.Flags().IntVar(&limit, "limit", 50, "Show only the most recent N matching entries (0 for all)")
	c.Flags().StringVar(&format, "format", "table", "table, csv, json or tsv")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))

	c.RunE = func(cmd *cobra.Command, _ []string) error {
		if limit < 0 {
			return fmt.Errorf("limit must be nonnegative")
		}
		if !validFormat(format) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		st, err := store()
		if err != nil {
			return err
		}
		entries, err := st.readAudit()
		if err != nil {
			return err
		}
		var filtered []AuditEntry
		for _, e := range entries {
			if connFilter != "" && !strings.EqualFold(e.Connection, connFilter) {
				continue
			}
			if envFilter != "" && !strings.EqualFold(e.Env, envFilter) {
				continue
			}
			if failedOnly && e.Success {
				continue
			}
			filtered = append(filtered, e)
		}
		if limit > 0 && len(filtered) > limit {
			filtered = filtered[len(filtered)-limit:]
		}
		cols := []string{"timestamp", "connection", "driver", "env", "database", "success", "rows_affected", "elapsed_ms", "query", "error"}
		rows := make([][]any, len(filtered))
		for i, e := range filtered {
			var ra any = "-"
			if e.RowsAffected != nil {
				ra = *e.RowsAffected
			}
			rows[i] = []any{
				e.Timestamp,
				e.Connection,
				e.Driver,
				e.Env,
				e.Database,
				e.Success,
				ra,
				e.ElapsedMs,
				e.Query,
				e.Error,
			}
		}
		return writeRows(cmd.OutOrStdout(), cols, rows, format)
	}
	root.AddCommand(c)
}
