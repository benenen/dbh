package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestParse(t *testing.T) {
	for _, raw := range []string{"socks5://127.0.0.1:1080", "socks5h://u:p@proxy:1080", "ssh://jump", "ssh://u:p@jump:2222?identity=~/.ssh/key&known_hosts=/tmp/k"} {
		if _, err := Parse(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://proxy:8080", "socks5://proxy", "ssh://jump?port=1", "ssh:///", "jump:22", "ssh://jump/path"} {
		if _, err := Parse(raw); err == nil {
			t.Errorf("%s accepted", raw)
		}
	}
	if got := Redact("ssh://u:secret@jump:22"); strings.Contains(got, "secret") {
		t.Fatalf("password not redacted: %s", got)
	}
	if got := Display("ssh://u:secret@jump:22?identity=/k"); got != "ssh://u:xxxxx@jump:22" {
		t.Fatalf("display: %s", got)
	}
}

func TestChainThroughSSHAndSOCKS5(t *testing.T) {
	target := listen(t, func(conn net.Conn) { _, _ = io.Copy(conn, conn) })
	socks := listen(t, func(conn net.Conn) { serveSOCKS5(t, conn, "u", "p") })
	sshAddr, knownHosts := serveSSH(t, "secret")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "known_hosts"), []byte(knownHosts), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SSH_AUTH_SOCK", "")
	t.Setenv("HOME", dir)
	hops := []string{"ssh://dbh:secret@" + sshAddr + "?known_hosts=" + url.QueryEscape(filepath.Join(dir, "known_hosts")), "socks5://u:p@" + socks}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	chain, err := Open(ctx, hops)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = chain.Close() }()
	conn, err := chain.Dial()(ctx, "tcp", target)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 4)
	if _, err := io.ReadFull(conn, reply); err != nil || string(reply) != "ping" {
		t.Fatalf("reply %q, %v", reply, err)
	}
	// Drivers interrupt blocked reads with deadlines; SSH channels need the pipe wrapper for this.
	if err := conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var netErr net.Error
	if _, err := conn.Read(reply); !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("expected timeout, got %v", err)
	}
	if _, err := chain.Dial()(ctx, "unix", "/tmp/socket"); err == nil {
		t.Fatal("unix network accepted")
	}

	if _, err := Open(ctx, []string{"ssh://dbh:wrong@" + sshAddr + "?known_hosts=" + url.QueryEscape(filepath.Join(dir, "known_hosts"))}); err == nil {
		t.Fatal("wrong SSH password accepted")
	}
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(ctx, []string{"ssh://dbh:secret@" + sshAddr + "?known_hosts=" + url.QueryEscape(empty)}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown host key accepted: %v", err)
	}
}

func listen(t *testing.T, serve func(net.Conn)) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				serve(conn)
			}()
		}
	}()
	return l.Addr().String()
}

// serveSOCKS5 implements the username/password CONNECT subset used by the client.
func serveSOCKS5(t *testing.T, conn net.Conn, user, password string) {
	buf := make([]byte, 262)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if _, err := io.ReadFull(conn, buf[:buf[1]]); err != nil {
		return
	}
	_, _ = conn.Write([]byte{5, 2})
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	gotUser := make([]byte, buf[1])
	_, _ = io.ReadFull(conn, gotUser)
	_, _ = io.ReadFull(conn, buf[:1])
	gotPassword := make([]byte, buf[0])
	_, _ = io.ReadFull(conn, gotPassword)
	if string(gotUser) != user || string(gotPassword) != password {
		_, _ = conn.Write([]byte{1, 1})
		return
	}
	_, _ = conn.Write([]byte{1, 0})
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	var host string
	switch buf[3] {
	case 1:
		_, _ = io.ReadFull(conn, buf[:4])
		host = net.IP(buf[:4]).String()
	case 3:
		_, _ = io.ReadFull(conn, buf[:1])
		name := make([]byte, buf[0])
		_, _ = io.ReadFull(conn, name)
		host = string(name)
	default:
		t.Errorf("unexpected SOCKS5 address type %d", buf[3])
		return
	}
	_, _ = io.ReadFull(conn, buf[:2])
	target, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(buf[:2])))))
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer func() { _ = target.Close() }()
	_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
	go func() { _, _ = io.Copy(target, conn) }()
	_, _ = io.Copy(conn, target)
}

// serveSSH accepts password logins and forwards direct-tcpip channels.
func serveSSH(t *testing.T, password string) (string, string) {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
		if string(got) != password {
			return nil, errors.New("denied")
		}
		return nil, nil
	}}
	config.AddHostKey(signer)
	addr := listen(t, func(conn net.Conn) {
		_, channels, requests, err := ssh.NewServerConn(conn, config)
		if err != nil {
			return
		}
		go ssh.DiscardRequests(requests)
		for request := range channels {
			var target struct {
				Host       string
				Port       uint32
				OriginHost string
				OriginPort uint32
			}
			if request.ChannelType() != "direct-tcpip" || ssh.Unmarshal(request.ExtraData(), &target) != nil {
				_ = request.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			remote, err := net.Dial("tcp", net.JoinHostPort(target.Host, strconv.Itoa(int(target.Port))))
			if err != nil {
				_ = request.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			channel, reqs, err := request.Accept()
			if err != nil {
				_ = remote.Close()
				continue
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				_, _ = io.Copy(channel, remote)
				_ = channel.Close()
			}()
			go func() {
				_, _ = io.Copy(remote, channel)
				_ = remote.Close()
			}()
		}
	})
	return addr, knownhosts.Line([]string{knownhosts.Normalize(addr)}, signer.PublicKey()) + "\n"
}
