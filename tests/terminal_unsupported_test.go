//go:build e2e && !linux && !darwin

package e2e

// Database CLI tests still run on platforms without the supported PTY harness.
type terminal struct{}

func (f *fixture) openTerminal(name string, args ...string) *terminal {
	f.t.Helper()
	f.t.Skip("PTY tests require Linux or macOS")
	return nil
}
func (*terminal) exchange(string, ...string) string { return "" }
func (*terminal) exit(string)                       {}
