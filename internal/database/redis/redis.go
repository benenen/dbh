// Package redis implements redis-cli style commands and key metadata.
package redis

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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

type connection struct {
	client *goredis.Client
	conn   *goredis.Conn
	db     int
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	return open(ctx, dsn, -1, dial)
}

// open connects to db, or to the URL's database when db is negative.
func open(ctx context.Context, dsn string, db int, dial database.Dial) (database.Connection, error) {
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
		config := opts.TLSConfig
		opts.Dialer = func(ctx context.Context, network, address string) (net.Conn, error) {
			conn, err := dial(ctx, network, address)
			if err != nil || config == nil {
				return conn, err
			}
			// A custom dialer replaces go-redis TLS, so negotiate it over the proxy here.
			secure := tls.Client(conn, config)
			if err := secure.HandshakeContext(ctx); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return secure, nil
		}
	}
	// One connection keeps SELECT and MULTI state; retries could repeat writes.
	opts.PoolSize = 1
	opts.MaxRetries = -1
	opts.ContextTimeoutEnabled = true
	opts.DisableIdentity = true
	opts.MaintNotificationsConfig = &maintnotifications.Config{Mode: maintnotifications.ModeDisabled}
	client := goredis.NewClient(opts)
	c := &connection{client: client, conn: client.Conn(), db: opts.DB}
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
func (c *connection) Close() error { return errors.Join(c.conn.Close(), c.client.Close()) }

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
