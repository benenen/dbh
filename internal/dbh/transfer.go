package dbh

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/spf13/cobra"
)

func addTransferCommands(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	root.AddCommand(exportCommand(store, names))
	root.AddCommand(importCommand(store, names))
	root.AddCommand(copyCommand(store, names))
}

func exportCommand(store func() (Store, error), names cobra.CompletionFunc) *cobra.Command {
	var table, query, db, format, outputPath string
	var useGzip bool
	var maxRows int
	var timeout time.Duration

	c := &cobra.Command{
		Use:               "export NAME [QUERY]",
		Short:             "Export table or query results to CSV, TSV, JSON or SQL INSERT",
		Args:              cobra.RangeArgs(1, 2),
		ValidArgsFunction: names,
	}
	c.Flags().StringVar(&table, "table", "", "Table name to export (SELECT * FROM <table>)")
	c.Flags().StringVarP(&query, "sql", "e", "", "SQL query to export")
	c.Flags().StringVar(&db, "db", "", "Server database to use")
	c.Flags().StringVar(&format, "format", "csv", "Export format: csv, tsv, json or sql")
	c.Flags().StringVarP(&outputPath, "output", "o", "", "Output file path (default stdout)")
	c.Flags().BoolVar(&useGzip, "gzip", false, "Compress output with gzip")
	c.Flags().IntVar(&maxRows, "max-rows", 0, "Maximum rows to export (0 for unlimited)")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Query timeout")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"csv", "tsv", "json", "sql"}, cobra.ShellCompDirectiveNoFileComp))
	c.MarkFlagsMutuallyExclusive("table", "sql")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		if len(args) == 2 {
			if cmd.Flags().Changed("sql") || cmd.Flags().Changed("table") {
				return fmt.Errorf("positional query cannot be combined with --sql or --table")
			}
			query = args[1]
		}
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		if maxRows < 0 {
			return fmt.Errorf("max-rows must be nonnegative")
		}
		switch format {
		case "csv", "tsv", "json", "sql":
		default:
			return fmt.Errorf("format must be csv, tsv, json or sql")
		}
		st, err := store()
		if err != nil {
			return err
		}
		p, _, err := resolveTarget(st, args[0], adhocOptions{})
		if err != nil {
			return err
		}
		if query == "" && table != "" {
			query = "SELECT * FROM " + quoteSQLIdent(p.Driver, table)
		}
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("provide --table, --sql or positional QUERY")
		}
		if format == "sql" && table == "" {
			table = "exported_rows"
		}

		session, err := openSession(cmd.Context(), p, timeout)
		if err != nil {
			return err
		}
		defer session.Close()
		session.MaxRows = maxRows
		session.Notice = cmd.ErrOrStderr()
		if cmd.Flags().Changed("db") {
			if err := session.UseDatabase(cmd.Context(), db); err != nil {
				return err
			}
		}

		out := cmd.OutOrStdout()
		var f *os.File
		var tmpPath string
		if outputPath != "" {
			if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
				return err
			}
			f, err = os.CreateTemp(filepath.Dir(outputPath), ".dbh-export-*.tmp")
			if err != nil {
				return err
			}
			tmpPath = f.Name()
			defer func() {
				if tmpPath != "" {
					_ = os.Remove(tmpPath)
				}
			}()
			out = f
			if strings.HasSuffix(strings.ToLower(outputPath), ".gz") {
				useGzip = true
			}
		}

		var gz *gzip.Writer
		if useGzip {
			gz = gzip.NewWriter(out)
			out = gz
		}

		var exportErr error
		if format == "sql" {
			exportErr = exportSQLInserts(cmd.Context(), session, query, table, p.Driver, out)
		} else {
			exportErr = session.Execute(cmd.Context(), query, format, out)
		}
		if exportErr != nil {
			if gz != nil {
				_ = gz.Close()
			}
			if f != nil {
				_ = f.Close()
			}
			return exportErr
		}
		if gz != nil {
			if err := gz.Close(); err != nil {
				if f != nil {
					_ = f.Close()
				}
				return err
			}
		}
		if f != nil {
			if err := f.Chmod(0o644); err != nil {
				_ = f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
			if err := os.Rename(tmpPath, outputPath); err != nil {
				return err
			}
			tmpPath = ""
		}
		return nil
	}
	return c
}

func exportSQLInserts(ctx context.Context, s *Session, query, table, driver string, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, s.Timeout)
	defer cancel()
	rows, err := s.Conn.Query(ctx, query)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteSQLIdent(driver, c)
	}
	prefix := fmt.Sprintf("INSERT INTO %s (%s) VALUES (", quoteSQLIdent(driver, table), strings.Join(quotedCols, ", "))

	count := 0
	for rows.Next() {
		if s.MaxRows > 0 && count >= s.MaxRows {
			break
		}
		row, err := rows.Row()
		if err != nil {
			return err
		}
		vals := make([]string, len(row.Values))
		for i, v := range row.Values {
			vals[i] = sqlLiteral(v)
		}
		if _, err := fmt.Fprintln(out, prefix+strings.Join(vals, ", ")+");"); err != nil {
			return err
		}
		count++
	}
	return rows.Err()
}

func sqlLiteral(v any) string {
	switch val := v.(type) {
	case nil:
		return "NULL"
	case bool:
		if val {
			return "TRUE"
		}
		return "FALSE"
	case int, int64, int32, float64, float32:
		return fmt.Sprint(val)
	case database.Binary:
		return fmt.Sprintf("X'%x'", []byte(val))
	default:
		s := fmt.Sprint(val)
		return "'" + strings.ReplaceAll(s, "'", "''") + "'"
	}
}

func quoteSQLIdent(driver, ident string) string {
	parts := database.SplitIdentifier(ident)
	quoted := make([]string, len(parts))
	for i, p := range parts {
		switch driver {
		case "mysql", "clickhouse":
			quoted[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
		case "mssql":
			quoted[i] = "[" + strings.ReplaceAll(p, "]", "]]") + "]"
		default:
			quoted[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
		}
	}
	return strings.Join(quoted, ".")
}

func importCommand(store func() (Store, error), names cobra.CompletionFunc) *cobra.Command {
	var table, file, format, db string
	var batchSize int
	var timeout time.Duration
	var allowWrite bool

	c := &cobra.Command{
		Use:               "import NAME",
		Short:             "Import CSV or JSON (NDJSON) rows into a table in batched transactions",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: names,
	}
	c.Flags().StringVar(&table, "table", "", "Destination table name (required)")
	c.Flags().StringVarP(&file, "file", "f", "-", "Input file (- for stdin; .gz auto-decompressed)")
	c.Flags().StringVar(&format, "format", "", "Input format: csv or json (auto-detected from filename, default csv)")
	c.Flags().StringVar(&db, "db", "", "Server database to use")
	c.Flags().IntVar(&batchSize, "batch-size", 500, "Rows per transaction batch")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Operation timeout")
	c.Flags().BoolVar(&allowWrite, "allow-write", false, "Allow import on read-only/prod connections")
	_ = c.MarkFlagRequired("table")
	_ = c.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"csv", "json"}, cobra.ShellCompDirectiveNoFileComp))

	c.RunE = func(cmd *cobra.Command, args []string) error {
		if batchSize <= 0 {
			return fmt.Errorf("batch-size must be positive")
		}
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		if format == "" {
			lower := strings.ToLower(strings.TrimSuffix(file, ".gz"))
			if strings.HasSuffix(lower, ".json") || strings.HasSuffix(lower, ".ndjson") {
				format = "json"
			} else {
				format = "csv"
			}
		}
		if format != "csv" && format != "json" {
			return fmt.Errorf("format must be csv or json")
		}

		st, err := store()
		if err != nil {
			return err
		}
		p, _, err := resolveTarget(st, args[0], adhocOptions{})
		if err != nil {
			return err
		}
		if p.ReadOnly && !allowWrite {
			return fmt.Errorf("import blocked: connection is marked read-only (use --allow-write to override)")
		}

		reader := cmd.InOrStdin()
		var f *os.File
		if file != "-" {
			f, err = os.Open(file)
			if err != nil {
				return err
			}
			defer func() { _ = f.Close() }()
			reader = f
		}
		if strings.HasSuffix(strings.ToLower(file), ".gz") {
			gz, gzErr := gzip.NewReader(reader)
			if gzErr != nil {
				return gzErr
			}
			defer func() { _ = gz.Close() }()
			reader = gz
		}

		var cols []string
		var rows [][]any
		if format == "csv" {
			cols, rows, err = readCSVImport(reader)
		} else {
			cols, rows, err = readJSONImport(reader)
		}
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			return fmt.Errorf("no columns found in input")
		}

		session, err := openSession(cmd.Context(), p, timeout)
		if err != nil {
			return err
		}
		defer session.Close()
		session.AllowWrite = allowWrite
		if cmd.Flags().Changed("db") {
			if err := session.UseDatabase(cmd.Context(), db); err != nil {
				return err
			}
		}

		imported, err := runBatchedImport(cmd.Context(), session, p.Driver, table, cols, rows, batchSize)
		if strings.EqualFold(p.Env, "prod") {
			affected := int64(imported)
			_ = st.appendAudit(AuditEntry{
				Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
				Connection:   p.Name,
				Driver:       p.Driver,
				Env:          p.Env,
				Database:     session.CurrentDatabase,
				Query:        fmt.Sprintf("IMPORT INTO %s (%d rows)", table, imported),
				RowsAffected: &affected,
				Success:      err == nil,
				Error:        redactErrorString(err, session),
			})
		}
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Imported %d rows into %s\n", imported, table)
		return err
	}
	return c
}

func copyCommand(store func() (Store, error), names cobra.CompletionFunc) *cobra.Command {
	var table, targetTable, query, srcDB, dstDB string
	var batchSize, maxRows int
	var timeout time.Duration
	var allowWrite bool

	c := &cobra.Command{
		Use:               "copy SRC DST",
		Short:             "Stream table or query results directly from SRC to DST",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: names,
	}
	c.Flags().StringVar(&table, "table", "", "Source table name (also default destination table)")
	c.Flags().StringVar(&targetTable, "target-table", "", "Destination table name (defaults to --table)")
	c.Flags().StringVarP(&query, "sql", "e", "", "Source SQL query to stream")
	c.Flags().StringVar(&srcDB, "src-db", "", "Source server database to use")
	c.Flags().StringVar(&dstDB, "dst-db", "", "Destination server database to use")
	c.Flags().IntVar(&batchSize, "batch-size", 500, "Rows per destination transaction batch")
	c.Flags().IntVar(&maxRows, "max-rows", 0, "Maximum rows to copy (0 for unlimited)")
	c.Flags().DurationVar(&timeout, "timeout", 60*time.Second, "Operation timeout")
	c.Flags().BoolVar(&allowWrite, "allow-write", false, "Allow copy into read-only/prod destination")
	c.MarkFlagsMutuallyExclusive("table", "sql")

	c.RunE = func(cmd *cobra.Command, args []string) error {
		if batchSize <= 0 {
			return fmt.Errorf("batch-size must be positive")
		}
		if maxRows < 0 {
			return fmt.Errorf("max-rows must be nonnegative")
		}
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		st, err := store()
		if err != nil {
			return err
		}
		srcProfile, _, err := resolveTarget(st, args[0], adhocOptions{})
		if err != nil {
			return err
		}
		dstProfile, _, err := resolveTarget(st, args[1], adhocOptions{})
		if err != nil {
			return err
		}
		if dstProfile.ReadOnly && !allowWrite {
			return fmt.Errorf("copy blocked: destination connection is marked read-only (use --allow-write to override)")
		}

		destTable := targetTable
		if destTable == "" {
			destTable = table
		}
		if query == "" && table != "" {
			query = "SELECT * FROM " + quoteSQLIdent(srcProfile.Driver, table)
		}
		if strings.TrimSpace(query) == "" {
			return fmt.Errorf("provide --table or --sql")
		}
		if strings.TrimSpace(destTable) == "" {
			return fmt.Errorf("provide --target-table when using --sql")
		}

		srcSession, err := openSession(cmd.Context(), srcProfile, timeout)
		if err != nil {
			return err
		}
		defer srcSession.Close()
		if cmd.Flags().Changed("src-db") {
			if err := srcSession.UseDatabase(cmd.Context(), srcDB); err != nil {
				return err
			}
		}

		dstSession, err := openSession(cmd.Context(), dstProfile, timeout)
		if err != nil {
			return err
		}
		defer dstSession.Close()
		dstSession.AllowWrite = allowWrite
		if cmd.Flags().Changed("dst-db") {
			if err := dstSession.UseDatabase(cmd.Context(), dstDB); err != nil {
				return err
			}
		}

		started := time.Now()
		queryCtx, cancel := context.WithTimeout(cmd.Context(), timeout)
		defer cancel()
		rows, err := srcSession.Conn.Query(queryCtx, query)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()

		cols, err := rows.Columns()
		if err != nil {
			return err
		}
		if len(cols) == 0 {
			return fmt.Errorf("source query returned no columns")
		}

		var batch [][]any
		copied := 0
		var copyErr error
		for rows.Next() {
			if maxRows > 0 && copied+len(batch) >= maxRows {
				break
			}
			row, rowErr := rows.Row()
			if rowErr != nil {
				copyErr = rowErr
				break
			}
			batch = append(batch, row.Values)
			if len(batch) >= batchSize {
				n, batchErr := runBatchedImport(cmd.Context(), dstSession, dstProfile.Driver, destTable, cols, batch, batchSize)
				copied += n
				batch = batch[:0]
				if batchErr != nil {
					copyErr = batchErr
					break
				}
			}
		}
		if copyErr == nil {
			if err := rows.Err(); err != nil {
				copyErr = err
			}
		}
		if copyErr == nil && len(batch) > 0 {
			n, batchErr := runBatchedImport(cmd.Context(), dstSession, dstProfile.Driver, destTable, cols, batch, batchSize)
			copied += n
			if batchErr != nil {
				copyErr = batchErr
			}
		}

		if strings.EqualFold(dstProfile.Env, "prod") {
			affected := int64(copied)
			_ = st.appendAudit(AuditEntry{
				Timestamp:    time.Now().UTC().Format(time.RFC3339Nano),
				Connection:   dstProfile.Name,
				Driver:       dstProfile.Driver,
				Env:          dstProfile.Env,
				Database:     dstSession.CurrentDatabase,
				Query:        fmt.Sprintf("COPY %s -> %s (%d rows)", srcProfile.Name, destTable, copied),
				RowsAffected: &affected,
				ElapsedMs:    time.Since(started).Milliseconds(),
				Success:      copyErr == nil,
				Error:        redactErrorString(copyErr, dstSession),
			})
		}
		if copyErr != nil {
			return copyErr
		}
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Copied %d rows from %s to %s.%s\n", copied, srcProfile.Name, dstProfile.Name, destTable)
		return err
	}
	return c
}

func redactErrorString(err error, s *Session) string {
	if err == nil {
		return ""
	}
	if s == nil {
		return err.Error()
	}
	return redact(err.Error(), s.dsn, s.proxies)
}

func readCSVImport(r io.Reader) ([]string, [][]any, error) {
	cr := csv.NewReader(r)
	header, err := cr.Read()
	if err != nil {
		return nil, nil, fmt.Errorf("read CSV header: %w", err)
	}
	var rows [][]any
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		vals := make([]any, len(header))
		for i := range header {
			if i < len(rec) && rec[i] != "NULL" && rec[i] != `\N` {
				vals[i] = rec[i]
			}
		}
		rows = append(rows, vals)
	}
	return header, rows, nil
}

func readJSONImport(r io.Reader) ([]string, [][]any, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 16*1024*1024)
	var docs []map[string]any
	colSet := map[string]bool{}
	var cols []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var doc map[string]any
		if err := dec.Decode(&doc); err != nil {
			return nil, nil, fmt.Errorf("invalid JSON line: %w", err)
		}
		for k := range doc {
			if !colSet[k] {
				colSet[k] = true
				cols = append(cols, k)
			}
		}
		docs = append(docs, doc)
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	rows := make([][]any, len(docs))
	for i, doc := range docs {
		vals := make([]any, len(cols))
		for j, col := range cols {
			v := doc[col]
			if num, ok := v.(json.Number); ok {
				if iv, err := num.Int64(); err == nil {
					v = iv
				} else if fv, err := num.Float64(); err == nil {
					v = fv
				}
			}
			vals[j] = v
		}
		rows[i] = vals
	}
	return cols, rows, nil
}

func runBatchedImport(ctx context.Context, s *Session, driver, table string, cols []string, rows [][]any, batchSize int) (int, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteSQLIdent(driver, c)
	}
	placeholders := make([]string, len(cols))
	for i := range cols {
		switch driver {
		case "postgres":
			placeholders[i] = fmt.Sprintf("$%d", i+1)
		case "mssql":
			placeholders[i] = fmt.Sprintf("@p%d", i+1)
		case "oracle":
			placeholders[i] = fmt.Sprintf(":%d", i+1)
		default:
			placeholders[i] = "?"
		}
	}
	stmt := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quoteSQLIdent(driver, table), strings.Join(quotedCols, ", "), strings.Join(placeholders, ", "))

	supportsTx := driver == "sqlite" || driver == "postgres" || driver == "mysql" || driver == "mssql" || driver == "dm"
	total := 0
	for start := 0; start < len(rows); start += batchSize {
		end := min(start+batchSize, len(rows))
		batchCtx, cancel := context.WithTimeout(ctx, s.Timeout)
		if supportsTx {
			if err := s.Conn.Exec(batchCtx, "BEGIN"); err != nil {
				cancel()
				return total, err
			}
		}
		for _, row := range rows[start:end] {
			if err := s.Conn.Exec(batchCtx, stmt, row...); err != nil {
				if supportsTx {
					_ = s.Conn.Exec(batchCtx, "ROLLBACK")
				}
				cancel()
				return total, err
			}
			total++
		}
		if supportsTx {
			if err := s.Conn.Exec(batchCtx, "COMMIT"); err != nil {
				_ = s.Conn.Exec(batchCtx, "ROLLBACK")
				cancel()
				return total, err
			}
		}
		cancel()
	}
	return total, nil
}
