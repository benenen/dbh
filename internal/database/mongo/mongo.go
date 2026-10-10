// Package mongo implements native MongoDB JSON commands and collection metadata.
package mongo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodb "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type Driver struct{}

var _ database.Driver = Driver{}

type connection struct {
	client *mongodb.Client
	db     *mongodb.Database
}

func (Driver) Open(ctx context.Context, dsn string, dial database.Dial) (database.Connection, error) {
	parsed, err := url.Parse(dsn)
	if err != nil || (parsed.Scheme != "mongodb" && parsed.Scheme != "mongodb+srv") {
		return nil, fmt.Errorf("invalid MongoDB connection URI")
	}
	name := strings.TrimPrefix(parsed.Path, "/")
	if name == "" {
		name = "test"
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	clientOptions := options.Client().ApplyURI(dsn)
	if dial != nil {
		clientOptions.SetDialer(dial)
	}
	client, err := mongodb.Connect(clientOptions)
	if err != nil {
		return nil, fmt.Errorf("invalid MongoDB connection URI")
	}
	conn := &connection{client: client, db: client.Database(name)}
	if err := client.Ping(ctx, readpref.Primary()); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
func (Driver) Syntax() database.Syntax {
	return database.Syntax{JSONCommands: true, BackslashEscapes: true,
		CompletionWords: strings.Fields("find filter insert documents update updates delete deletes aggregate pipeline count distinct listCollections listIndexes create drop ping")}
}
func (Driver) SwitchDatabase(_ context.Context, conn database.Connection, _ string, name string, _ database.Dial) (database.Connection, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	c, err := mongoConnection(conn)
	if err != nil {
		return nil, err
	}
	c.db = c.client.Database(name)
	return nil, nil
}
func validateName(name string) error {
	if name == "" || len(name) > 63 || strings.ContainsAny(name, "/\\. \"$\x00") {
		return fmt.Errorf("invalid MongoDB database name")
	}
	return nil
}
func mongoConnection(conn database.Connection) (*connection, error) {
	c, ok := conn.(*connection)
	if !ok {
		return nil, fmt.Errorf("expected a MongoDB connection")
	}
	return c, nil
}
func (Driver) Databases(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := mongoConnection(conn)
	if err != nil {
		return nil, err
	}
	names, err := c.client.ListDatabaseNames(ctx, bson.D{})
	sort.Strings(names)
	return names, err
}
func (Driver) Tables(ctx context.Context, conn database.Connection) ([]string, error) {
	c, err := mongoConnection(conn)
	if err != nil {
		return nil, err
	}
	names, err := c.db.ListCollectionNames(ctx, bson.D{})
	sort.Strings(names)
	return names, err
}
func (Driver) Columns(ctx context.Context, conn database.Connection, table string) ([]string, error) {
	c, err := mongoConnection(conn)
	if err != nil {
		return nil, err
	}
	raw, err := c.db.Collection(table).FindOne(ctx, bson.D{}).Raw()
	if errors.Is(err, mongodb.ErrNoDocuments) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	elements, err := raw.Elements()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(elements))
	for _, element := range elements {
		names = append(names, element.Key())
	}
	sort.Strings(names)
	return names, nil
}
func (Driver) ColumnDetails(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if err := resolveCollection(ctx, conn, table); err != nil {
		return database.Query{}, err
	}
	text, err := bson.MarshalExtJSON(bson.D{{Key: "find", Value: table}, {Key: "limit", Value: 1}}, false, false)
	return database.Query{Text: string(text), Heading: "Sample document:"}, err
}
func (Driver) Indexes(ctx context.Context, conn database.Connection, table string) (database.Query, error) {
	if err := resolveCollection(ctx, conn, table); err != nil {
		return database.Query{}, err
	}
	text, err := bson.MarshalExtJSON(bson.D{{Key: "listIndexes", Value: table}}, false, false)
	return database.Query{Text: string(text)}, err
}
func resolveCollection(ctx context.Context, conn database.Connection, table string) error {
	c, err := mongoConnection(conn)
	if err != nil {
		return err
	}
	names, err := c.db.ListCollectionNames(ctx, bson.D{{Key: "name", Value: table}})
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("collection %q does not exist", table)
	}
	return nil
}
func (c *connection) Query(ctx context.Context, text string, args ...any) (database.Rows, error) {
	if len(args) != 0 {
		return nil, fmt.Errorf("MongoDB commands do not accept SQL parameters")
	}
	var command bson.D
	if err := bson.UnmarshalExtJSON([]byte(text), false, &command); err != nil {
		return nil, fmt.Errorf("expected a MongoDB JSON command: %w", err)
	}
	if len(command) == 0 {
		return nil, fmt.Errorf("MongoDB command cannot be empty")
	}
	switch command[0].Key {
	case "find", "aggregate", "listCollections", "listIndexes", "getMore":
		cursor, err := c.db.RunCommandCursor(ctx, command)
		if err != nil {
			return nil, err
		}
		return &rows{ctx: ctx, cursor: cursor}, nil
	default:
		raw, err := c.db.RunCommand(ctx, command).Raw()
		if err != nil {
			return nil, err
		}
		if failures, ok := raw.Lookup("writeErrors").ArrayOK(); ok {
			values, err := failures.Values()
			if err != nil {
				return nil, err
			}
			if len(values) > 0 {
				detail, _ := values[0].DocumentOK()
				message, _ := detail.Lookup("errmsg").StringValueOK()
				return nil, fmt.Errorf("MongoDB write failed: %s", message)
			}
		}
		if failure, ok := raw.Lookup("writeConcernError").DocumentOK(); ok {
			message, _ := failure.Lookup("errmsg").StringValueOK()
			return nil, fmt.Errorf("MongoDB write concern failed: %s", message)
		}
		return &rows{ctx: ctx, single: raw}, nil
	}
}
func (c *connection) Exec(ctx context.Context, text string, args ...any) error {
	result, err := c.Query(ctx, text, args...)
	if err != nil {
		return err
	}
	defer func() { _ = result.Close() }()
	for result.Next() {
		if _, err := result.Row(); err != nil {
			return err
		}
	}
	return result.Err()
}
func (c *connection) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.client.Disconnect(ctx)
}

type rows struct {
	ctx      context.Context
	cursor   *mongodb.Cursor
	single   bson.Raw
	consumed bool
}

func (*rows) Columns() ([]string, error) { return []string{"document"}, nil }
func (r *rows) Next() bool {
	if r.cursor != nil {
		return r.cursor.Next(r.ctx)
	}
	if r.consumed {
		return false
	}
	r.consumed = true
	return len(r.single) > 0
}
func (r *rows) Row() (database.Row, error) {
	raw := r.single
	if r.cursor != nil {
		raw = r.cursor.Current
	}
	encoded, err := bson.MarshalExtJSON(raw, false, false)
	if err != nil {
		return database.Row{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return database.Row{}, err
	}
	return database.Row{Values: []any{string(encoded)}, Object: document}, nil
}
func (r *rows) Err() error {
	if r.cursor != nil {
		return r.cursor.Err()
	}
	return nil
}
func (*rows) NextResultSet() bool { return false }
func (r *rows) Close() error {
	if r.cursor == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return r.cursor.Close(ctx)
}
