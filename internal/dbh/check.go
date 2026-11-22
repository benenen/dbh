package dbh

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/benenen/dbh/internal/database"
	"github.com/benenen/dbh/internal/database/proxy"
	"github.com/benenen/dbh/internal/database/registry"
	"github.com/spf13/cobra"
)

type checkStageResult struct {
	Name    string
	Stage   string
	Status  string
	Detail  string
	Elapsed time.Duration
}

func addCheckCommand(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var timeout time.Duration
	var db, format string
	var checkAll bool
	cmd := &cobra.Command{
		Use:               "check [NAME]",
		Short:             "Diagnose proxy, authentication and database connectivity",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: names,
	}
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Total diagnostic timeout per connection")
	cmd.Flags().StringVar(&db, "db", "", "Server database to switch to and verify")
	cmd.Flags().BoolVar(&checkAll, "all", false, "Check all saved connections")
	cmd.Flags().StringVar(&format, "format", "", "Structured output: table, csv, json or tsv")
	_ = cmd.RegisterFlagCompletionFunc("format", cobra.FixedCompletions([]string{"table", "csv", "json", "tsv"}, cobra.ShellCompDirectiveNoFileComp))
	_ = cmd.RegisterFlagCompletionFunc("db", cobra.NoFileCompletions)
	_ = cmd.RegisterFlagCompletionFunc("timeout", cobra.NoFileCompletions)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		if format != "" && !validFormat(format) {
			return fmt.Errorf("format must be table, csv, json or tsv")
		}
		if checkAll && len(args) > 0 {
			return fmt.Errorf("--all cannot be combined with a connection NAME")
		}
		if !checkAll && len(args) == 0 {
			return fmt.Errorf("provide a connection NAME or --all")
		}
		if checkAll && cmd.Flags().Changed("db") {
			return fmt.Errorf("--db cannot be combined with --all")
		}
		if cmd.Flags().Changed("db") && strings.TrimSpace(db) == "" {
			return fmt.Errorf("database name must not be empty")
		}
		st, err := store()
		if err != nil {
			return err
		}
		if checkAll {
			profiles, err := st.List()
			if err != nil {
				return err
			}
			return checkAllConnections(cmd, profiles, timeout, format)
		}
		p, err := st.Get(args[0])
		if err != nil {
			return err
		}
		targetDB := ""
		if cmd.Flags().Changed("db") {
			targetDB = db
		}
		if format != "" {
			results, checkErr := runCheckStages(cmd.Context(), p, targetDB, timeout, nil)
			if writeErr := writeCheckRows(cmd, results, format); writeErr != nil {
				return writeErr
			}
			return checkErr
		}
		return checkConnection(cmd, p, targetDB, timeout)
	}
	root.AddCommand(cmd)
}

func checkAllConnections(cmd *cobra.Command, profiles []Profile, timeout time.Duration, format string) error {
	var allResults []checkStageResult
	failed := 0
	for _, p := range profiles {
		stopSpinner := startSpinner(cmd.ErrOrStderr(), "Checking "+p.Name)
		results, err := runCheckStages(cmd.Context(), p, "", timeout, nil)
		stopSpinner()
		if err != nil {
			failed++
		}
		if format != "" {
			allResults = append(allResults, results...)
			continue
		}
		last := checkStageResult{Stage: "Connection", Status: "OK"}
		if len(results) > 0 {
			last = results[len(results)-1]
		}
		detail := ""
		if last.Detail != "" {
			detail = " (" + last.Detail + ")"
		}
		if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "%s: %s - %s%s [%s]\n", p.Name, last.Status, last.Stage, detail, last.Elapsed.Round(time.Microsecond)); werr != nil {
			return werr
		}
	}
	if format != "" {
		if err := writeCheckRows(cmd, allResults, format); err != nil {
			return err
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d connection check(s) failed", failed, len(profiles))
	}
	return nil
}

func writeCheckRows(cmd *cobra.Command, results []checkStageResult, format string) error {
	values := make([][]any, len(results))
	for i, r := range results {
		values[i] = []any{r.Name, r.Stage, r.Status, r.Detail, r.Elapsed.Round(time.Microsecond).String()}
	}
	return writeRows(cmd.OutOrStdout(), []string{"name", "stage", "status", "detail", "elapsed"}, values, format)
}

func checkConnection(cmd *cobra.Command, p Profile, targetDB string, timeout time.Duration) error {
	var mu sync.Mutex
	var writeErr error
	stopSpinner := startSpinner(cmd.ErrOrStderr(), "Checking "+p.Name)
	defer stopSpinner()
	_, checkErr := runCheckStages(cmd.Context(), p, targetDB, timeout, func(res checkStageResult) {
		mu.Lock()
		defer mu.Unlock()
		stopSpinner()
		detail := ""
		if res.Detail != "" {
			detail = " (" + res.Detail + ")"
		}
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s%s [%s]\n", res.Stage, res.Status, detail, res.Elapsed.Round(time.Microsecond))
		}
	})
	stopSpinner()
	mu.Lock()
	defer mu.Unlock()
	if checkErr != nil {
		return checkErr
	}
	return writeErr
}

func runCheckStages(parent context.Context, p Profile, targetDB string, timeout time.Duration, onStage func(checkStageResult)) ([]checkStageResult, error) {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var mu sync.Mutex
	var results []checkStageResult
	record := func(stage string, elapsed time.Duration, err error) {
		res := checkStageResult{
			Name:    p.Name,
			Stage:   stage,
			Status:  "OK",
			Elapsed: elapsed,
		}
		if err != nil {
			res.Status = "FAIL"
			res.Detail = diagnosticReason(err)
		}
		mu.Lock()
		results = append(results, res)
		mu.Unlock()
		if onStage != nil {
			onStage(res)
		}
	}

	started := time.Now()
	err := validate(p)
	record("Configuration", time.Since(started), err)
	if err != nil {
		return results, fmt.Errorf("connection check failed: invalid configuration")
	}
	backend, err := registry.Lookup(p.Driver)
	if err != nil {
		return results, err
	}
	var chain *proxy.Chain
	var dial database.Dial
	if len(p.Proxies) > 0 {
		chain, err = proxy.OpenTrace(ctx, p.Proxies, func(hop int, elapsed time.Duration, err error) {
			record(fmt.Sprintf("Proxy hop %d", hop), elapsed, err)
		})
		if err != nil {
			return results, fmt.Errorf("connection check failed at proxy: %s", diagnosticReason(err))
		}
		defer func() { _ = chain.Close() }()
		dial = chain.Dial()
	}
	started = time.Now()
	conn, err := backend.Open(ctx, p.DSN, dial)
	elapsed := time.Since(started)
	if err != nil {
		stage := diagnosticStage(err)
		record(stage, elapsed, err)
		return results, fmt.Errorf("connection check failed at %s: %s", strings.ToLower(stage), diagnosticReason(err))
	}
	defer func() { _ = conn.Close() }()
	// Open includes the driver's handshake and ping, so authentication and access
	// are verified rather than inferred from a successful TCP connection.
	record("Database connection and authentication", elapsed, nil)

	if targetDB != "" {
		started = time.Now()
		switched, switchErr := backend.SwitchDatabase(ctx, conn, p.DSN, targetDB, dial)
		record("Database switch", time.Since(started), switchErr)
		if switchErr != nil {
			return results, fmt.Errorf("connection check failed at database switch: %s", diagnosticReason(switchErr))
		}
		if switched != nil {
			_ = conn.Close()
			conn = switched
		}
	}
	if current, ok := backend.(database.CurrentDatabase); ok {
		started = time.Now()
		_, err = current.CurrentDatabase(ctx, conn)
		record("Current database", time.Since(started), err)
		if err != nil {
			return results, fmt.Errorf("connection check failed at database: %s", diagnosticReason(err))
		}
	}
	return results, nil
}

func diagnosticStage(err error) string {
	var hop *proxy.HopError
	if errors.As(err, &hop) {
		return fmt.Sprintf("Proxy hop %d", hop.Hop)
	}
	if authenticationError(err) {
		return "Authentication"
	}
	var network *net.OpError
	var dns *net.DNSError
	if errors.As(err, &network) || errors.As(err, &dns) || errors.Is(err, context.DeadlineExceeded) {
		return "Network"
	}
	return "Database"
}

func authenticationError(err error) bool {
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"authentication failed", "authentication failure", "failed to authenticate", "password authentication", "access denied for user", "wrongpass", "noauth", "unable to authenticate", "authenticationfailed", "no supported methods remain", "no ssh credentials"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// Report categories, never raw driver errors: they can contain partial DSNs,
// credentials or server-provided text that simple whole-DSN replacement misses.
func diagnosticReason(err error) string {
	if authenticationError(err) {
		return "authentication rejected; check credentials and authentication settings"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout"
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "connection refused"):
		return "connection refused; check address and listener"
	case strings.Contains(text, "no such host"):
		return "hostname resolution failed"
	case strings.Contains(text, "host key"), strings.Contains(text, "known_hosts"):
		return "SSH host key verification failed; check known_hosts"
	case strings.Contains(text, "tls"), strings.Contains(text, "certificate"):
		return "TLS verification or handshake failed"
	case strings.Contains(text, "socks"):
		return "SOCKS5 connection or authentication failed"
	case strings.Contains(text, "invalid"):
		return "invalid connection settings"
	default:
		return "connection or database access failed; check server, database and permissions"
	}
}
