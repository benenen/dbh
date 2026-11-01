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

func addCheckCommand(root *cobra.Command, store func() (Store, error), names cobra.CompletionFunc) {
	var timeout time.Duration
	cmd := &cobra.Command{Use: "check NAME", Short: "Diagnose proxy, authentication and database connectivity", Args: cobra.ExactArgs(1), ValidArgsFunction: names}
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "Total diagnostic timeout")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if timeout <= 0 {
			return fmt.Errorf("timeout must be positive")
		}
		st, err := store()
		if err != nil {
			return err
		}
		p, err := st.Get(args[0])
		if err != nil {
			return err
		}
		return checkConnection(cmd, p, timeout)
	}
	root.AddCommand(cmd)
}

func checkConnection(cmd *cobra.Command, p Profile, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(cmd.Context(), timeout)
	defer cancel()
	var mu sync.Mutex
	var writeErr error
	report := func(stage string, elapsed time.Duration, err error) {
		mu.Lock()
		defer mu.Unlock()
		state := "OK"
		detail := ""
		if err != nil {
			state = "FAIL"
			detail = " (" + diagnosticReason(err) + ")"
		}
		if writeErr == nil {
			_, writeErr = fmt.Fprintf(cmd.OutOrStdout(), "%s: %s%s [%s]\n", stage, state, detail, elapsed.Round(time.Microsecond))
		}
	}
	started := time.Now()
	err := validate(p)
	report("Configuration", time.Since(started), err)
	if err != nil {
		return fmt.Errorf("connection check failed: invalid configuration")
	}
	backend, err := registry.Lookup(p.Driver)
	if err != nil {
		return err
	}
	var chain *proxy.Chain
	var dial database.Dial
	if len(p.Proxies) > 0 {
		chain, err = proxy.OpenTrace(ctx, p.Proxies, func(hop int, elapsed time.Duration, err error) {
			report(fmt.Sprintf("Proxy hop %d", hop), elapsed, err)
		})
		if err != nil {
			return fmt.Errorf("connection check failed at proxy: %s", diagnosticReason(err))
		}
		defer func() { _ = chain.Close() }()
		dial = chain.Dial()
	}
	started = time.Now()
	conn, err := backend.Open(ctx, p.DSN, dial)
	elapsed := time.Since(started)
	if err != nil {
		stage := diagnosticStage(err)
		report(stage, elapsed, err)
		return fmt.Errorf("connection check failed at %s: %s", strings.ToLower(stage), diagnosticReason(err))
	}
	defer func() { _ = conn.Close() }()
	// Open includes the driver's handshake and ping, so authentication and access
	// are verified rather than inferred from a successful TCP connection.
	report("Database connection and authentication", elapsed, nil)
	if current, ok := backend.(database.CurrentDatabase); ok {
		started = time.Now()
		_, err = current.CurrentDatabase(ctx, conn)
		report("Current database", time.Since(started), err)
		if err != nil {
			return fmt.Errorf("connection check failed at database: %s", diagnosticReason(err))
		}
	}
	mu.Lock()
	defer mu.Unlock()
	return writeErr
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
