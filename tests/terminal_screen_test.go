//go:build e2e && (linux || darwin)

package e2e

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// Model the ANSI controls used by the ASCII prompt layout regressions.
type terminalScreen struct {
	t    *testing.T
	x, y int
	rows [24][100]rune
}

var screenTokens = regexp.MustCompile("\x1b\\[[0-?]*[ -/]*[@-~]|(?s:.)")

func newScreen(t *testing.T, row int) *terminalScreen {
	s := &terminalScreen{t: t, y: row}
	for y := range s.rows {
		for x := range s.rows[y] {
			s.rows[y][x] = ' '
		}
	}
	return s
}
func (s *terminalScreen) newline() {
	if s.y == len(s.rows)-1 {
		copy(s.rows[:], s.rows[1:])
		for x := range s.rows[s.y] {
			s.rows[s.y][x] = ' '
		}
	} else {
		s.y++
	}
}
func (s *terminalScreen) feed(text string) {
	s.t.Helper()
	for _, token := range screenTokens.FindAllString(text, -1) {
		if strings.HasPrefix(token, "\x1b[") {
			s.control(token)
			continue
		}
		char := []rune(token)[0]
		switch char {
		case '\r':
			s.x = 0
		case '\n':
			s.newline()
		case '\a':
		default:
			if char < 32 {
				s.t.Fatalf("unsupported terminal character %q", token)
			}
			if s.x == len(s.rows[0]) {
				s.x = 0
				s.newline()
			}
			s.rows[s.y][s.x] = char
			s.x++
		}
	}
}
func (s *terminalScreen) control(token string) {
	s.t.Helper()
	command := token[len(token)-1]
	params := token[2 : len(token)-1]
	if strings.ContainsRune("mhlq", rune(command)) {
		return
	}
	value := 0
	if params != "" {
		var err error
		value, err = strconv.Atoi(params)
		if err != nil {
			s.t.Fatal(err)
		}
	}
	steps := value
	if steps == 0 {
		steps = 1
	}
	switch {
	case command == 'A':
		s.y = max(0, s.y-steps)
	case command == 'B':
		s.y = min(len(s.rows)-1, s.y+steps)
	case command == 'C':
		s.x = min(len(s.rows[0])-1, s.x+steps)
	case command == 'D':
		s.x = max(0, s.x-steps)
	case command == 'K' && value == 0:
		for x := s.x; x < len(s.rows[0]); x++ {
			s.rows[s.y][x] = ' '
		}
	case command == 'K' && value == 1:
		for x := 0; x <= s.x; x++ {
			s.rows[s.y][x] = ' '
		}
	case command == 'J' && value == 0:
		for y := s.y; y < len(s.rows); y++ {
			start := 0
			if y == s.y {
				start = s.x
			}
			for x := start; x < len(s.rows[0]); x++ {
				s.rows[y][x] = ' '
			}
		}
	default:
		s.t.Fatalf("unsupported terminal control %q", token)
	}
}
func (s *terminalScreen) line(row int) string { return strings.TrimRight(string(s.rows[row][:]), " ") }
