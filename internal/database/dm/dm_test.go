package dm

import (
	"context"
	"io"
	"net"
	"sync/atomic"
	"testing"
)

func TestOpenValidationAndProxyBridge(t *testing.T) {
	d := Driver{}
	if _, err := d.Open(context.Background(), "mysql://localhost:5236", nil); err == nil {
		t.Fatal("expected error for non-dm DSN")
	}

	// Start a dummy TCP echo/close server to verify proxyBridge invokes dial.
	srv, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go func() {
		for {
			c, err := srv.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	var dialed atomic.Bool
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		dialed.Store(true)
		return (&net.Dialer{}).DialContext(ctx, network, srv.Addr().String())
	}

	bridge, localAddr, err := startProxyBridge("10.0.0.1:5236", dial)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()

	conn, err := net.Dial("tcp", localAddr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.ReadAll(conn)
	_ = conn.Close()

	if !dialed.Load() {
		t.Fatal("expected proxyBridge to invoke custom dialer")
	}
}

func TestSplitTable(t *testing.T) {
	owner, name := splitTable(`"SYSDBA"."USERS"`)
	if owner != "SYSDBA" || name != "USERS" {
		t.Fatalf("splitTable = (%q, %q)", owner, name)
	}
}
