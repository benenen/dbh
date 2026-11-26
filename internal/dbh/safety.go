package dbh

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"unicode"

	"github.com/benenen/dbh/internal/database"
)

func resolveRuntimeProfile(ctx context.Context, p Profile) (Profile, error) {
	if p.DSN != "" || p.DSNCmd == "" {
		return p, nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", p.DSNCmd)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", p.DSNCmd)
	}
	out, err := cmd.Output()
	if err != nil {
		return p, fmt.Errorf("dsn-cmd failed: %w", err)
	}
	dsn := strings.TrimSpace(string(out))
	if dsn == "" {
		return p, fmt.Errorf("dsn-cmd returned empty DSN")
	}
	p.DSN = dsn
	return p, nil
}

func isWriteStatement(query string, syntax database.Syntax) bool {
	if syntax.JSONCommands {
		var doc map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(query)), &doc) == nil {
			for k := range doc {
				switch strings.ToLower(k) {
				case "insert", "update", "delete", "drop", "create", "findandmodify", "dropdatabase", "dropindexes", "createindexes":
					return true
				}
			}
		}
		return false
	}
	if syntax.LineCommands {
		fields := strings.Fields(query)
		if len(fields) == 0 {
			return false
		}
		switch strings.ToUpper(fields[0]) {
		case "SET", "SETEX", "SETNX", "MSET", "DEL", "UNLINK", "FLUSHDB", "FLUSHALL",
			"HSET", "HDEL", "LPUSH", "RPUSH", "LPOP", "RPOP", "LSET", "LTRIM",
			"SADD", "SREM", "SPOP", "ZADD", "ZREM", "INCR", "DECR", "INCRBY", "DECRBY",
			"EXPIRE", "PEXPIRE", "PERSIST", "RENAME", "COPY", "XADD", "APPEND", "MULTI", "EXEC",
			"PUT", "COMPACT", "CREATE", "DELETE", "POST":
			return true
		}
		return false
	}
	if syntax.HTTPCommands {
		fields := strings.Fields(query)
		if len(fields) < 2 {
			return false
		}
		method := strings.ToUpper(fields[0])
		path := strings.ToLower(fields[1])
		switch method {
		case "PUT", "DELETE", "PATCH":
			return true
		case "POST":
			if isHTTPReadPath(path) {
				return false
			}
			return true
		}
		return false
	}
	if syntax.SlashComments {
		for _, t := range contextTokens(query, syntax) {
			if t.depth != 0 || t.quoted {
				continue
			}
			switch strings.ToUpper(t.text) {
			case "CREATE", "MERGE", "DELETE", "DETACH", "SET", "REMOVE":
				return true
			}
		}
		return false
	}
	verb := leadingSQLKeyword(query, syntax)
	switch verb {
	case "INSERT", "UPDATE", "DELETE", "DROP", "TRUNCATE", "ALTER", "CREATE",
		"REPLACE", "MERGE", "GRANT", "REVOKE", "VACUUM", "ATTACH", "DETACH", "RENAME",
		"WRITE", "INGEST":
		return true
	default:
		return false
	}
}

func isHTTPReadPath(path string) bool {
	for _, marker := range []string{
		"_search", "_count", "_sql", "_analyze", "_msearch", "_explain",
		"collections/list", "collections/describe", "collections/has",
		"collections/get_load_state", "collections/get_stats",
		"entities/query", "entities/search", "entities/get",
		"indexes/list", "indexes/describe",
		"partitions/list", "partitions/has",
		"databases/list", "databases/describe",
		"points/scroll", "points/query", "points/search",
		"points/count", "points/recommend", "points/discover",
	} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

func dangerousWriteReason(query string, syntax database.Syntax) (string, bool) {
	if syntax.LineCommands {
		fields := strings.Fields(query)
		if len(fields) > 0 {
			switch strings.ToUpper(fields[0]) {
			case "FLUSHDB", "FLUSHALL":
				return strings.ToUpper(fields[0]) + " is blocked on production connections", true
			case "DELETE":
				if len(fields) >= 2 && strings.EqualFold(fields[1], "BUCKET") {
					return "DELETE BUCKET is blocked on production connections", true
				}
			}
		}
		return "", false
	}
	if syntax.JSONCommands {
		var doc map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(query)), &doc) == nil {
			for k := range doc {
				switch strings.ToLower(k) {
				case "drop", "dropdatabase":
					return k + " is blocked on production connections", true
				}
			}
		}
		return "", false
	}
	if syntax.HTTPCommands {
		fields := strings.Fields(query)
		if len(fields) >= 2 && strings.EqualFold(fields[0], "DELETE") {
			return "DELETE is blocked on production connections", true
		}
		return "", false
	}
	if syntax.SlashComments {
		hasDelete, hasWhere := false, false
		for _, t := range contextTokens(query, syntax) {
			if t.depth != 0 || t.quoted {
				continue
			}
			switch strings.ToUpper(t.text) {
			case "DELETE":
				hasDelete = true
			case "WHERE":
				hasWhere = true
			}
		}
		if hasDelete && !hasWhere {
			return "DELETE without WHERE is blocked on production connections", true
		}
		return "", false
	}
	verb := leadingSQLKeyword(query, syntax)
	switch verb {
	case "DROP", "TRUNCATE":
		return verb + " is blocked on production connections", true
	case "DELETE", "UPDATE":
		if !hasTopLevelWhere(stripCommentsSyntax(query, syntax), syntax) {
			return verb + " without WHERE is blocked on production connections", true
		}
	}
	return "", false
}

func leadingSQLKeyword(query string, syntax database.Syntax) string {
	s := stripCommentsSyntax(query, syntax)
	end := strings.IndexFunc(s, func(r rune) bool { return !unicode.IsLetter(r) })
	if end < 0 {
		end = len(s)
	}
	return strings.ToUpper(s[:end])
}

func hasTopLevelWhere(s string, syntax database.Syntax) bool {
	depth := 0
	quote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if quote != 0 {
			if c == '\\' && syntax.BackslashEscapes {
				i++
				continue
			}
			if c == quote {
				if i+1 < len(s) && s[i+1] == quote {
					i++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch c {
		case '\'', '"', '`':
			quote = c
			continue
		case '[':
			if syntax.BracketIdentifiers {
				quote = ']'
				continue
			}
		case '(':
			depth++
			continue
		case ')':
			if depth > 0 {
				depth--
			}
			continue
		}
		if depth == 0 && (c == 'W' || c == 'w') && i+5 <= len(s) && strings.EqualFold(s[i:i+5], "WHERE") {
			prevOK := i == 0 || !isIdentifier(rune(s[i-1]))
			nextOK := i+5 == len(s) || !isIdentifier(rune(s[i+5]))
			if prevOK && nextOK {
				return true
			}
		}
	}
	return false
}
