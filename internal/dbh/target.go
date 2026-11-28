package dbh

import (
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/benenen/dbh/internal/database/registry"
)

type adhocOptions struct {
	Driver   string
	DSN      string
	Proxies  []string
	ReadOnly bool
}

// resolveTarget resolves either a saved connection profile or an ad-hoc target
// (explicit --dsn or direct URL/file path). It never writes to the connection store.
func resolveTarget(st Store, target string, opts adhocOptions) (Profile, bool, error) {
	if opts.DSN != "" {
		if target != "" {
			return Profile{}, false, fmt.Errorf("--dsn cannot be combined with a connection NAME")
		}
		driver, normDSN, err := resolveAdhocDriver(opts.DSN, opts.Driver)
		if err != nil {
			return Profile{}, false, err
		}
		p := Profile{
			Name:     adhocDisplayName(driver, normDSN),
			Driver:   driver,
			DSN:      normDSN,
			Proxies:  opts.Proxies,
			ReadOnly: opts.ReadOnly,
		}
		if err := validate(p); err != nil {
			return Profile{}, false, err
		}
		return p, true, nil
	}

	if target == "" {
		return Profile{}, false, fmt.Errorf("provide a connection NAME, URL/file target, or --dsn")
	}

	if validName.MatchString(target) {
		if saved, err := st.Get(target); err == nil {
			if opts.Driver != "" || len(opts.Proxies) > 0 || opts.ReadOnly {
				return Profile{}, false, fmt.Errorf("--driver, --proxy and --read-only apply only to ad-hoc targets (--dsn or direct URL/file)")
			}
			return saved, false, nil
		}
	}

	driver, normDSN, ok, err := parseAdhocTarget(target, opts.Driver)
	if err != nil {
		return Profile{}, false, err
	}
	if !ok {
		return Profile{}, false, fmt.Errorf("connection %q does not exist", target)
	}

	p := Profile{
		Name:     adhocDisplayName(driver, normDSN),
		Driver:   driver,
		DSN:      normDSN,
		Proxies:  opts.Proxies,
		ReadOnly: opts.ReadOnly,
	}
	if err := validate(p); err != nil {
		return Profile{}, false, err
	}
	return p, true, nil
}

func resolveAdhocDriver(dsn, explicitDriver string) (string, string, error) {
	if explicitDriver != "" {
		if _, err := registry.Lookup(explicitDriver); err != nil {
			return "", "", err
		}
		// Normalize URL forms when the user passes e.g. --driver mysql --dsn mysql://...
		if driver, norm, ok, err := inferDriver(dsn); err == nil && ok && driver == explicitDriver {
			return explicitDriver, norm, nil
		}
		return explicitDriver, dsn, nil
	}
	driver, norm, ok, err := inferDriver(dsn)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", fmt.Errorf("cannot infer driver from --dsn; specify --driver")
	}
	return driver, norm, nil
}

func parseAdhocTarget(target, explicitDriver string) (driver, normDSN string, ok bool, err error) {
	if explicitDriver != "" {
		if _, err := registry.Lookup(explicitDriver); err != nil {
			return "", "", false, err
		}
		if inferred, norm, matched, inferErr := inferDriver(target); inferErr == nil && matched && inferred == explicitDriver {
			return explicitDriver, norm, true, nil
		}
		return explicitDriver, target, true, nil
	}
	return inferDriver(target)
}

func inferDriver(target string) (driver, normDSN string, ok bool, err error) {
	trimmed := strings.TrimSpace(target)
	if trimmed == "" {
		return "", "", false, nil
	}
	lower := strings.ToLower(trimmed)

	if trimmed == ":memory:" || strings.HasPrefix(lower, "file:") {
		return "sqlite", trimmed, true, nil
	}

	if idx := strings.Index(lower, "://"); idx > 0 {
		scheme := lower[:idx]
		rest := trimmed[idx+3:]
		switch scheme {
		case "postgres", "postgresql":
			return "postgres", trimmed, true, nil
		case "mysql":
			norm, err := mysqlURLToDSN(trimmed)
			if err != nil {
				return "", "", false, err
			}
			return "mysql", norm, true, nil
		case "sqlite", "sqlite3":
			if rest == "" {
				return "", "", false, fmt.Errorf("sqlite URL must include a database path or :memory:")
			}
			return "sqlite", rest, true, nil
		case "sqlserver":
			return "mssql", trimmed, true, nil
		case "mssql":
			return "mssql", "sqlserver://" + rest, true, nil
		case "oracle":
			return "oracle", trimmed, true, nil
		case "trino":
			return "trino", trimmed, true, nil
		case "dm":
			return "dm", trimmed, true, nil
		case "mongodb", "mongodb+srv":
			return "mongo", trimmed, true, nil
		case "clickhouse":
			return "clickhouse", trimmed, true, nil
		case "redis", "rediss", "redis+cluster", "rediss+cluster", "redis+sentinel", "rediss+sentinel":
			return "redis", trimmed, true, nil
		case "cassandra":
			return "cassandra", trimmed, true, nil
		case "scylla":
			return "cassandra", "cassandra://" + rest, true, nil
		case "etcd", "etcds":
			return "etcd", trimmed, true, nil
		case "bbolt":
			return "bbolt", trimmed, true, nil
		case "openobserve":
			return "openobserve", trimmed, true, nil
		case "http", "https":
			return "", "", false, fmt.Errorf("ambiguous http(s):// URL; specify --driver")
		default:
			return "", "", false, fmt.Errorf("unrecognized URL scheme %q; specify --driver", scheme)
		}
	}

	if strings.Contains(lower, "@tcp(") || strings.Contains(lower, "@unix(") {
		return "mysql", trimmed, true, nil
	}

	pathOnly := trimmed
	if q := strings.IndexByte(pathOnly, '?'); q >= 0 {
		pathOnly = pathOnly[:q]
	}
	lowerPath := strings.ToLower(pathOnly)
	switch {
	case strings.HasSuffix(lowerPath, ".db"),
		strings.HasSuffix(lowerPath, ".sqlite"),
		strings.HasSuffix(lowerPath, ".sqlite3"),
		strings.HasSuffix(lowerPath, ".db3"):
		return "sqlite", trimmed, true, nil
	case strings.HasSuffix(lowerPath, ".bolt"),
		strings.HasSuffix(lowerPath, ".bbolt"):
		return "bbolt", "bbolt://" + trimmed, true, nil
	}

	return "", "", false, nil
}

func mysqlURLToDSN(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", fmt.Errorf("invalid mysql:// URL")
	}
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "3306"
	}
	var auth string
	if u.User != nil {
		user := u.User.Username()
		pass, hasPass := u.User.Password()
		if hasPass {
			auth = user + ":" + pass + "@"
		} else if user != "" {
			auth = user + "@"
		}
	}
	db := strings.TrimPrefix(u.Path, "/")
	dsn := auth + "tcp(" + net.JoinHostPort(host, port) + ")/" + db
	if u.RawQuery != "" {
		dsn += "?" + u.RawQuery
	}
	return dsn, nil
}

// adhocDisplayName produces a safe display name without credentials or path separators
// that always satisfies validName.
func adhocDisplayName(driver, dsn string) string {
	var raw string
	switch driver {
	case "sqlite", "bbolt":
		pathPart := strings.TrimPrefix(dsn, "bbolt://")
		if q := strings.IndexByte(pathPart, '?'); q >= 0 {
			pathPart = pathPart[:q]
		}
		if pathPart == ":memory:" || strings.HasPrefix(strings.ToLower(pathPart), "file::memory:") {
			raw = "memory"
		} else {
			raw = filepath.Base(pathPart)
		}
	case "mysql":
		if idx := strings.Index(dsn, "@tcp("); idx >= 0 {
			rest := dsn[idx+len("@tcp("):]
			if end := strings.IndexByte(rest, ')'); end >= 0 {
				hostPort := rest[:end]
				if h, _, err := net.SplitHostPort(hostPort); err == nil && h != "" {
					raw = h
				} else {
					raw = hostPort
				}
			}
		}
	default:
		if u, err := url.Parse(dsn); err == nil && u.Hostname() != "" {
			raw = u.Hostname()
		}
	}

	sanitized := sanitizeNamePart(raw)
	if sanitized == "" {
		return driver
	}
	candidate := driver + "-" + sanitized
	if !validName.MatchString(candidate) {
		return driver
	}
	return candidate
}

func sanitizeNamePart(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		isValid := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' || r == '.' || r == '-'
		if isValid {
			b.WriteRune(r)
			prevDash = r == '-'
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), ".-_")
}
