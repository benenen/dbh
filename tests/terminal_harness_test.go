//go:build e2e && (linux || darwin)

package e2e

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

type terminal struct {
	t          *testing.T
	master     *os.File
	output     chan string
	done       chan struct{}
	stop       chan struct{}
	readerDone chan struct{}
	waitErr    error
	transcript string
}

func (f *fixture) openTerminal(name string, args ...string) *terminal {
	f.t.Helper()
	command := exec.Command(binary, append([]string{"c", name, "--format", "json"}, args...)...)
	command.Env = f.environment()
	master, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		f.t.Fatal(err)
	}
	term := &terminal{t: f.t, master: master, output: make(chan string, 128), done: make(chan struct{}), stop: make(chan struct{}), readerDone: make(chan struct{})}
	go func() { term.waitErr = command.Wait(); close(term.done) }()
	go func() {
		defer close(term.readerDone)
		defer close(term.output)
		buffer := make([]byte, 65536)
		for {
			n, err := master.Read(buffer)
			if n > 0 {
				select {
				case term.output <- string(buffer[:n]):
				case <-term.stop:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	f.t.Cleanup(func() {
		select {
		case <-term.done:
		default:
			if err := command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				f.t.Errorf("kill terminal: %v", err)
			}
			select {
			case <-term.done:
			case <-time.After(5 * time.Second):
				f.t.Error("terminal did not exit after kill")
			}
		}
		close(term.stop)
		if err := master.Close(); err != nil {
			f.t.Errorf("close terminal: %v", err)
		}
		select {
		case <-term.readerDone:
		case <-time.After(5 * time.Second):
			f.t.Error("terminal reader did not stop")
		}
	})
	term.exchange("", name+"> ")
	return term
}

func (f *fixture) terminal(args ...string) *terminal {
	f.t.Helper()
	return f.openTerminal("demo", args...)
}

func (term *terminal) exchange(input string, expected ...string) string {
	term.t.Helper()
	if input != "" {
		if _, err := io.WriteString(term.master, input); err != nil {
			term.t.Fatal(err)
		}
	}
	var result strings.Builder
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	quiet := time.NewTimer(150 * time.Millisecond)
	defer quiet.Stop()
	matches := func() bool { return len(expected) == 0 || strings.Contains(result.String(), expected[0]) }
	finish := func() string {
		term.transcript += result.String()
		if !matches() {
			term.t.Fatalf("terminal did not show %q after %q:\n%q", expected[0], input, term.transcript)
		}
		return result.String()
	}
	for {
		select {
		case chunk, ok := <-term.output:
			if !ok {
				return finish()
			}
			result.WriteString(chunk)
			quiet.Reset(150 * time.Millisecond)
		case <-quiet.C:
			if result.Len() > 0 && matches() {
				return finish()
			}
			quiet.Reset(150 * time.Millisecond)
		case <-deadline.C:
			return finish()
		}
	}
}

func (term *terminal) exit(input string) {
	term.t.Helper()
	term.exchange(input)
	select {
	case <-term.done:
		if term.waitErr != nil {
			term.t.Fatalf("terminal exited: %v\n%q", term.waitErr, term.transcript)
		}
	case <-time.After(5 * time.Second):
		term.t.Fatal("terminal exit timed out")
	}
}
