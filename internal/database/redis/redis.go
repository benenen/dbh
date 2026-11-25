// Package redis implements redis-cli style commands and key metadata.
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/benenen/dbh/internal/database"
	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

// maxKeys bounds the SCAN used by \tables and completion on large keyspaces.
const maxKeys = 1000

// describeCommand is only sent by ColumnDetails with the key as an argument;
// typed commands never carry arguments, so it cannot reach the server.
const describeCommand = "DESCRIBE"

type Driver struct{}

var (
	_ database.Driver      = Driver{}
	_ database.ColumnNames = Driver{}
)

type redisCmdable interface {
	Do(ctx context.Context, args ...any) *goredis.Cmd
	Ping(ctx context.Context) *goredis.StatusCmd
	Info(ctx context.Context, section ...string) *goredis.StringCmd
	Scan(ctx context.Context, cursor uint64, match string, count int64) *goredis.ScanCmd
	Type(ctx context.Context, key string) *goredis.StatusCmd
	ObjectEncoding(ctx context.Context, key string) *goredis.StringCmd
}

type connection struct {
	client goredis.UniversalClient
	conn   redisCmdable
	closer interface{ Close() error }
	db     int
	mode   string // "", "cluster", "sentinel"
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	return open(ctx, dsn, -1, dial)
}

func wrapProxyDialer(dial database.Dial, tlsCfg *tls.Config) func(ctx context.Context, network, address string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil || tlsCfg == nil {
			return conn, err
		}
		// A custom dialer replaces go-redis TLS, so negotiate it over the proxy here.
		secure := tls.Client(conn, tlsCfg)
		if err := secure.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		return secure, nil
	}
}

func parseClusterOrSentinel(dsn string, db int, dial database.Dial) (*connection, bool, error) {
	lower := strings.ToLower(dsn)
	var mode, rest string
	var useTLS bool
	switch {
	case strings.HasPrefix(lower, "redis+cluster://"):
		mode, rest = "cluster", dsn[len("redis+cluster://"):]
	case strings.HasPrefix(lower, "rediss+cluster://"):
		mode, useTLS, rest = "cluster", true, dsn[len("rediss+cluster://"):]
	case strings.HasPrefix(lower, "redis+sentinel://"):
		mode, rest = "sentinel", dsn[len("redis+sentinel://"):]
	case strings.HasPrefix(lower, "rediss+sentinel://"):
		mode, useTLS, rest = "sentinel", true, dsn[len("rediss+sentinel://"):]
	default:
		return nil, false, nil
	}

	authority, pathAndQuery := rest, ""
	if idx := strings.IndexAny(rest, "/?"); idx >= 0 {
		authority = rest[:idx]
		pathAndQuery = rest[idx:]
	}
	var user, pass string
	if at := strings.LastIndex(authority, "@"); at >= 0 {
		userInfo := authority[:at]
		authority = authority[at+1:]
		if u, p, ok := strings.Cut(userInfo, ":"); ok {
			user, pass = u, p
		} else {
			user = userInfo
		}
	}
	u, err := url.Parse("http://placeholder" + pathAndQuery)
	if err != nil {
		return nil, true, fmt.Errorf("invalid Redis %s URL: %w", mode, err)
	}
	addrs := strings.Split(authority, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
		if addrs[i] != "" && !strings.Contains(addrs[i], ":") {
			if mode == "sentinel" {
				addrs[i] += ":26379"
			} else {
				addrs[i] += ":6379"
			}
		}
	}
	if len(addrs) == 0 || addrs[0] == "" {
		return nil, true, fmt.Errorf("invalid Redis %s URL: missing host", mode)
	}
	q := u.Query()
	var tlsCfg *tls.Config
	if useTLS {
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	var dialer func(ctx context.Context, network, address string) (net.Conn, error)
	if dial != nil {
		dialer = wrapProxyDialer(dial, tlsCfg)
	}
	if mode == "cluster" {
		if db > 0 {
			return nil, true, fmt.Errorf("redis cluster only supports database 0")
		}
		cc := goredis.NewClusterClient(&goredis.ClusterOptions{
			Addrs:                    addrs,
			Username:                 user,
			Password:                 pass,
			TLSConfig:                tlsCfg,
			Dialer:                   dialer,
			MaxRetries:               -1,
			ContextTimeoutEnabled:    true,
			DisableIdentity:          true,
			MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeDisabled},
		})
		return &connection{client: cc, conn: cc, db: 0, mode: "cluster"}, true, nil
	}
	masterName := q.Get("master_name")
	if masterName == "" {
		masterName = "mymaster"
	}
	if db < 0 {
		if raw := strings.TrimPrefix(u.Path, "/"); raw != "" {
			if parsedDB, convErr := strconv.Atoi(raw); convErr == nil && parsedDB >= 0 {
				db = parsedDB
			}
		}
		if db < 0 {
			db = 0
		}
	}
	fc := goredis.NewFailoverClient(&goredis.FailoverOptions{
		MasterName:            masterName,
		SentinelAddrs:         addrs,
		SentinelUsername:      q.Get("sentinel_username"),
		SentinelPassword:      q.Get("sentinel_password"),
		Username:              user,
		Password:              pass,
		DB:                    db,
		TLSConfig:             tlsCfg,
		Dialer:                dialer,
		MaxRetries:            -1,
		ContextTimeoutEnabled: true,
		DisableIdentity:       true,
	})
	return &connection{client: fc, conn: fc, db: db, mode: "sentinel"}, true, nil
}

// open connects to db, or to the URL's database when db is negative.
func open(ctx context.Context, dsn string, db int, dial database.Dial) (database.Connection, error) {
	if c, handled, err := parseClusterOrSentinel(dsn, db, dial); handled {
		if err != nil {
			return nil, err
		}
		if err := c.conn.Ping(ctx).Err(); err != nil {
			_ = c.Close()
			return nil, err
		}
		return c, nil
	}
	opts, err := goredis.ParseURL(dsn)
	if err != nil {
		return nil, fmt.Errorf("invalid Redis connection URL")
	}
	if db >= 0 {
		opts.DB = db
	}
	if dial != nil {
		if opts.Network == "unix" {
			return nil, fmt.Errorf("redis Unix sockets do not support proxies")
		}
		opts.Dialer = wrapProxyDialer(dial, opts.TLSConfig)
	}
	// One connection keeps SELECT and MULTI state; retries could repeat writes.
	opts.PoolSize = 1
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	opts.DisableIdentity = true
	opts.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	client := goredis.NewClient(opts)
	pinned := client.Conn()
	c := &connection{client: client, conn: pinned, closer: pinned, db: opts.DB}
	if err := c.conn.Ping(ctx).Err(); err != nil {
		_ = c.Close()
		return nil, err
	}
	return c, nil
}

func (Driver) Syntax() database.Syntax {
	return database.Syntax{LineCommands: true, BackslashEscapes: true,
		CompletionWords: strings.Fields(`APPEND AUTH BITCOUNT BLPOP BRPOP CLIENT CONFIG COPY DBSIZE DECR DECRBY DEL DISCARD
EVAL EVALSHA EXEC EXISTS EXPIRE EXPIREAT FLUSHDB GET GETDEL GETEX GETRANGE GETSET HDEL HEXISTS HGET HGETALL
HINCRBY HKEYS HLEN HMGET HSCAN HSET HSETNX HVALS INCR INCRBY INCRBYFLOAT INFO KEYS LINDEX LINSERT LLEN LMOVE
LPOP LPUSH LRANGE LREM LSET LTRIM MEMORY MGET MSET MULTI OBJECT PERSIST PEXPIRE PING PTTL PUBLISH RENAME RPOP
RPUSH SADD SCAN SCARD SDIFF SELECT SET SETEX SETNX SINTER SISMEMBER SMEMBERS SPOP SREM SSCAN STRLEN SUNION
TTL TYPE UNLINK WATCH XADD XLEN XRANGE XREAD XREVRANGE ZADD ZCARD ZCOUNT ZINCRBY ZRANGE ZRANGEBYSCORE ZRANK
ZREM ZREVRANGE ZSCAN ZSCORE`)}
}

func (Driver) SwitchDatabase(ctx context.Context, _ database.Connection, dsn, name string, dial database.Dial) (database.Connection, error) {
	db, err := strconv.Atoi(name)
	if err != nil || db < 0 {
		return nil, fmt.Errorf("redis database must be a non-negative number")
	}
	return open(ctx, dsn, db, dial)
}

func redisConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a Redis connection")
	}
	return c, nil
}

// Databases lists databases that hold keys, plus the current one.
func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := redisConnection(conn)
	if err != nil {
		return nil, err
	}
	info, err := c.conn.Info(ctx, "keyspace").Result()
	if err != nil {
		return nil, err
	}
	numbers := map[int]bool{c.db: true}
	scanner := bufio.NewScanner(strings.NewReader(info))
	for scanner.Scan() {
		name, _, ok := strings.Cut(scanner.Text(), ":")
		if n, err := strconv.Atoi(strings.TrimPrefix(name, "db")); ok && strings.HasPrefix(name, "db") && err == nil {
			numbers[n] = true
		}
	}
	sorted := make([]int, 0, len(numbers))
	for n := range numbers {
		sorted = append(sorted, n)
	}
	sort.Ints(sorted)
	names := make([]string, len(sorted))
	for i, n := range sorted {
		names[i] = strconv.Itoa(n)
	}
	return names, nil
}

// Tables lists up to maxKeys keys of the current database.
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := redisConnection(conn)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var cursor uint64
	for {
		keys, next, err := c.conn.Scan(ctx, cursor, "", maxKeys).Result()
		if err != nil {
			return nil, err
		}
		for _, key := range keys {
			seen[key] = true
		}
		cursor = next
		if cursor == 0 || len(seen) >= maxKeys {
			break
		}
	}
	names := make([]string, 0, len(seen))
	for key := range seen {
		names = append(names, key)
	}
	sort.Strings(names)
	return names[:min(len(names), maxKeys)], nil
}

// Keys have no columns; hash fields are not read for completion.
func (Driver) Columns(context.Context, database.Connection, string) ([]string, error) {
	return []string{}, nil
}
func (Driver) ColumnNames(context.Context, database.Connection) ([]string, error) {
	return []string{}, nil
}
func (Driver) ColumnDetails(_ context.Context, _ database.Connection, key string) (database.Query, error) {
	return database.Query{Text: describeCommand, Args: []any{key}, Heading: "Key:"}, nil
}
func (Driver) Indexes(context.Context, database.Connection, string) (database.Query, error) {
	return database.Query{}, database.ErrNoIndexes
}

func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		key, ok := args[0].(string)
		if text != describeCommand || len(args) != 1 || !ok {
			return nil, fmt.Errorf("redis commands do not accept SQL parameters")
		}
		return c.describe(ctx, key)
	}
	fields, err := splitArgs(text)
	if err != nil {
		return nil, err
	}
	if len(fields) == 0 {
		return nil, fmt.Errorf("redis command cannot be empty")
	}
	command := make([]any, len(fields))
	for i, field := range fields {
		command[i] = field
	}
	reply, err := c.conn.Do(ctx, command...).Result()
	if errors.Is(err, goredis.Nil) {
		reply, err = nil, nil
	}
	if err != nil {
		return nil, err
	}
	// Keep \database accurate after a typed SELECT on this connection.
	if strings.EqualFold(fields[0], "SELECT") && len(fields) == 2 {
		if db, err := strconv.Atoi(fields[1]); err == nil {
			c.db = db
		}
	}
	return replyRows(reply), nil
}

// describe shows the key's type, TTL in seconds, encoding and length.
func (c *connection) describe(ctx context.Context, key string) (database.Rows, error) {
	kind, err := c.conn.Type(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	if kind == "none" {
		return nil, fmt.Errorf("key %q does not exist", key)
	}
	ttl, err := c.conn.Do(ctx, "TTL", key).Result()
	if err != nil {
		return nil, err
	}
	// OBJECT may be denied by ACLs; the other details are still useful.
	var encoding any
	if value, err := c.conn.ObjectEncoding(ctx, key).Result(); err == nil {
		encoding = value
	}
	var length any
	if command, ok := map[string]string{"string": "STRLEN", "list": "LLEN", "set": "SCARD", "zset": "ZCARD", "hash": "HLEN", "stream": "XLEN"}[kind]; ok {
		if length, err = c.conn.Do(ctx, command, key).Result(); err != nil {
			return nil, err
		}
	}
	columns := []string{"key", "type", "ttl", "encoding", "length"}
	return &rows{columns: columns, rows: []database.Row{database.NewRow(columns, []any{key, kind, ttl, encoding, length})}}, nil
}

func (c *connection) Exec(ctx context.Context, text string, args ...any) error {
	_, err := c.Query(ctx, text, args...)
	return err
}
func (c *connection) Close() error {
	if c.closer != nil {
		return errors.Join(c.closer.Close(), c.client.Close())
	}
	return c.client.Close()
}

// replyRows shows maps as field/value rows, arrays as one row per element and
// other replies as a single value.
func replyRows(reply any) *rows {
	switch reply := reply.(type) {
	case map[any]any:
		columns := []string{"field", "value"}
		fields := make([]string, 0, len(reply))
		values := make(map[string]any, len(reply))
		for field, value := range reply {
			name := fmt.Sprint(normalize(field))
			fields = append(fields, name)
			values[name] = value
		}
		sort.Strings(fields)
		r := &rows{columns: columns}
		for _, field := range fields {
			r.add(field, values[field])
		}
		return r
	case []any:
		r := &rows{columns: []string{"value"}}
		for _, value := range reply {
			r.add(value)
		}
		return r
	default:
		r := &rows{columns: []string{"value"}}
		r.add(reply)
		return r
	}
}

type rows struct {
	columns []string
	rows    []database.Row
	next    int
}

// add keeps nested replies as JSON values and shows them as JSON text in tables and CSV.
func (r *rows) add(values ...any) {
	objects := make([]any, len(values))
	texts := make([]any, len(values))
	for i, value := range values {
		objects[i] = normalize(value)
		texts[i] = objects[i]
		switch objects[i].(type) {
		case []any, map[string]any:
			texts[i] = jsonText(objects[i])
		}
	}
	row := database.NewRow(r.columns, objects)
	row.Values = texts
	r.rows = append(r.rows, row)
}

// normalize converts RESP3 maps and errors into values that encode as JSON.
func normalize(value any) any {
	switch value := value.(type) {
	case nil, string, int64, float64, bool:
		return value
	case []any:
		items := make([]any, len(value))
		for i, item := range value {
			items[i] = normalize(item)
		}
		return items
	case map[any]any:
		object := make(map[string]any, len(value))
		for key, item := range value {
			object[fmt.Sprint(normalize(key))] = normalize(item)
		}
		return object
	case map[string]any:
		object := make(map[string]any, len(value))
		for key, item := range value {
			object[key] = normalize(item)
		}
		return object
	case error:
		return value.Error()
	default:
		return fmt.Sprint(value)
	}
}

func (r *rows) Columns() ([]string, error) { return r.columns, nil }
func (r *rows) Next() bool {
	r.next++
	return r.next <= len(r.rows)
}
func (r *rows) Row() (database.Row, error) { return r.rows[r.next-1], nil }
func (*rows) Err() error                   { return nil }
func (*rows) NextResultSet() bool          { return false }
func (*rows) Close() error                 { return nil }

func (Driver) CurrentDatabase(_ context.Context, conn database.Connection) (string, error) {
	c, err := redisConnection(conn)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(c.db), nil
}
