// Package proxy dials database connections through an ordered chain of SOCKS5 and SSH hops.
package proxy

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/benenen/dbh/internal/database"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	xproxy "golang.org/x/net/proxy"
)

// Chain owns the SSH clients opened for its hops; Close releases them.
type Chain struct {
	dial    database.Dial
	closers []io.Closer
}

// Parse validates one hop: socks5://[user:pass@]host:port or
// ssh://[user[:pass]@]host[:port][?identity=FILE&known_hosts=FILE].
func Parse(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return nil, fmt.Errorf("invalid proxy %q (use socks5://host:port or ssh://user@host[:port])", Redact(raw))
	}
	switch u.Scheme {
	case "socks5", "socks5h":
		if u.Port() == "" {
			return nil, fmt.Errorf("SOCKS5 proxy %s requires a port", u.Host)
		}
	case "ssh":
		for key := range u.Query() {
			if key != "identity" && key != "known_hosts" {
				return nil, fmt.Errorf("unsupported SSH proxy option %q", key)
			}
		}
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use socks5 or ssh)", u.Scheme)
	}
	if u.Path != "" && u.Path != "/" {
		return nil, fmt.Errorf("proxy %s must not contain a path", u.Host)
	}
	return u, nil
}

// Redact hides the password of a proxy URL for display and error messages.
func Redact(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		if i := strings.LastIndex(raw, "@"); i >= 0 {
			return "[redacted]" + raw[i:]
		}
		return raw
	}
	return u.Redacted()
}

// Display shows a hop without its password or options.
func Display(raw string) string {
	u, err := Parse(raw)
	if err != nil {
		return Redact(raw)
	}
	u.RawQuery = ""
	return u.Redacted()
}

// Open connects the SSH hops in order; the first hop is dialed directly.
func Open(ctx context.Context, hops []string) (*Chain, error) {
	var direct net.Dialer
	c := &Chain{dial: direct.DialContext}
	for i, raw := range hops {
		u, err := Parse(raw)
		if err != nil {
			_ = c.Close()
			return nil, err
		}
		if u.Scheme == "ssh" {
			client, err := dialSSH(ctx, c.dial, u)
			if err != nil {
				_ = c.Close()
				return nil, fmt.Errorf("proxy hop %d (%s): %w", i+1, u.Host, err)
			}
			c.closers = append(c.closers, client)
			c.dial = sshDial(client)
			continue
		}
		var auth *xproxy.Auth
		if u.User != nil {
			password, _ := u.User.Password()
			auth = &xproxy.Auth{User: u.User.Username(), Password: password}
		}
		// Hostnames are sent unresolved, so internal names resolve at the proxy.
		d, err := xproxy.SOCKS5("tcp", u.Host, auth, c.dial)
		if err != nil {
			_ = c.Close()
			return nil, fmt.Errorf("proxy hop %d (%s): %w", i+1, u.Host, err)
		}
		c.dial = d.(xproxy.ContextDialer).DialContext
	}
	return c, nil
}

// Dial returns the chain's dial function for driver configuration.
func (c *Chain) Dial() database.Dial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		if network != "tcp" && network != "tcp4" && network != "tcp6" {
			return nil, fmt.Errorf("proxies support only TCP connections, not %s", network)
		}
		return c.dial(ctx, "tcp", address)
	}
}

func (c *Chain) Close() error {
	var errs []error
	for i := len(c.closers) - 1; i >= 0; i-- {
		errs = append(errs, c.closers[i].Close())
	}
	c.closers = nil
	return errors.Join(errs...)
}

func dialSSH(ctx context.Context, dial database.Dial, u *url.URL) (*ssh.Client, error) {
	address := u.Host
	if u.Port() == "" {
		address = net.JoinHostPort(u.Hostname(), "22")
	}
	name := u.User.Username()
	if name == "" {
		current, err := user.Current()
		if err != nil {
			return nil, fmt.Errorf("SSH user is required: %w", err)
		}
		name = current.Username
	}
	home, _ := os.UserHomeDir()
	knownHosts := u.Query().Get("known_hosts")
	if knownHosts == "" {
		knownHosts = filepath.Join(home, ".ssh", "known_hosts")
	}
	hostKey, err := knownhosts.New(expandHome(knownHosts, home))
	if err != nil {
		return nil, fmt.Errorf("cannot read known_hosts: %w", err)
	}
	methods, closeAgent, err := authMethods(u, home)
	if err != nil {
		return nil, err
	}
	defer closeAgent()
	config := &ssh.ClientConfig{User: name, Auth: methods, HostKeyCallback: hostKey,
		HostKeyAlgorithms: hostKeyAlgorithms(hostKey, address)}
	conn, err := dial(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if !stop() || err != nil {
		_ = conn.Close()
		if err == nil {
			err = ctx.Err()
		}
		var keyErr *knownhosts.KeyError
		if errors.As(err, &keyErr) && len(keyErr.Want) == 0 {
			err = fmt.Errorf("host key of %s is not in %s; verify it and add it (for example with ssh-keyscan): %w", address, knownHosts, err)
		}
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return ssh.NewClient(clientConn, channels, requests), nil
}

// authMethods tries the URL password, then identity files, the SSH agent and default keys.
func authMethods(u *url.URL, home string) ([]ssh.AuthMethod, func(), error) {
	var methods []ssh.AuthMethod
	closeAgent := func() {}
	if password, ok := u.User.Password(); ok {
		methods = append(methods, ssh.Password(password))
	}
	var signers []ssh.Signer
	for _, file := range u.Query()["identity"] {
		signer, err := readKey(expandHome(file, home))
		if err != nil {
			return nil, nil, fmt.Errorf("cannot use SSH identity %s: %w", file, err)
		}
		signers = append(signers, signer)
	}
	if socket := os.Getenv("SSH_AUTH_SOCK"); socket != "" {
		if conn, err := net.Dial("unix", socket); err == nil {
			closeAgent = func() { _ = conn.Close() }
			if agentSigners, err := agent.NewClient(conn).Signers(); err == nil {
				signers = append(signers, agentSigners...)
			}
		}
	}
	if len(u.Query()["identity"]) == 0 {
		for _, name := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
			// Missing or passphrase-protected default keys are skipped; use the agent for those.
			if signer, err := readKey(filepath.Join(home, ".ssh", name)); err == nil {
				signers = append(signers, signer)
			}
		}
	}
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	if len(methods) == 0 {
		closeAgent()
		return nil, nil, errors.New("no SSH credentials (set a password, identity, SSH agent or default key)")
	}
	return methods, closeAgent, nil
}

func readKey(file string) (ssh.Signer, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	return ssh.ParsePrivateKey(data)
}

func expandHome(path, home string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[1:])
	}
	return path
}

// hostKeyAlgorithms prefers the key types recorded in known_hosts, so the server
// does not present a different type that would fail verification.
func hostKeyAlgorithms(callback ssh.HostKeyCallback, address string) []string {
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil
	}
	probe, err := ssh.NewPublicKey(public)
	if err != nil {
		return nil
	}
	var keyErr *knownhosts.KeyError
	if !errors.As(callback(address, &net.TCPAddr{}, probe), &keyErr) {
		return nil
	}
	var algorithms []string
	for _, known := range keyErr.Want {
		if known.Key.Type() == ssh.KeyAlgoRSA {
			algorithms = append(algorithms, ssh.KeyAlgoRSASHA512, ssh.KeyAlgoRSASHA256)
		}
		algorithms = append(algorithms, known.Key.Type())
	}
	return algorithms
}

// sshDial wraps SSH channels in a pipe because drivers rely on deadlines,
// which SSH channels do not support.
func sshDial(client *ssh.Client) database.Dial {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		remote, err := client.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		pipe, bridge := net.Pipe()
		local := addrConn{Conn: pipe, local: remote.LocalAddr(), remote: remote.RemoteAddr()}
		go func() {
			_, _ = io.Copy(remote, bridge)
			_ = remote.Close()
		}()
		go func() {
			_, _ = io.Copy(bridge, remote)
			_ = bridge.Close()
		}()
		return local, nil
	}
}

// addrConn reports the SSH channel's TCP addresses; drivers send them to the server.
type addrConn struct {
	net.Conn
	local, remote net.Addr
}

func (c addrConn) LocalAddr() net.Addr  { return c.local }
func (c addrConn) RemoteAddr() net.Addr { return c.remote }
