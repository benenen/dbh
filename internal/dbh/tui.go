package dbh

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
	"golang.org/x/term"
)

type tuiKeyKind int

const (
	keyRune tuiKeyKind = iota
	keyEnter
	keyEsc
	keyCtrlC
	keyCtrlN
	keyCtrlP
	keyCtrlU
	keyCtrlW
	keyBackspace
	keyUp
	keyDown
	keyLeft
	keyRight
	keyPgUp
	keyPgDown
	keyHome
	keyEnd
)

type tuiKey struct {
	kind tuiKeyKind
	r    rune
}

type tuiModel interface {
	Resize(width, height int)
	HandleKey(k tuiKey) (quit bool)
	Render() string
}

// runAltScreenTUI runs a minimal raw-mode alt-screen event loop using
// golang.org/x/term without sending terminal capability probes (such as OSC 11).
func runAltScreenTUI(m tuiModel) error {
	inFd := int(os.Stdin.Fd())
	outFd := int(os.Stdout.Fd())
	oldState, err := term.MakeRaw(inFd)
	if err != nil {
		return err
	}
	defer func() { _ = term.Restore(inFd, oldState) }()

	_, _ = io.WriteString(os.Stdout, "\x1b[?1049h\x1b[?25l")
	defer func() { _, _ = io.WriteString(os.Stdout, "\x1b[?25h\x1b[?1049l") }()

	var mu sync.Mutex
	draw := func() {
		mu.Lock()
		defer mu.Unlock()
		w, h, err := term.GetSize(outFd)
		if err != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		m.Resize(w, h)
		frame := strings.ReplaceAll(m.Render(), "\n", "\r\n")
		_, _ = io.WriteString(os.Stdout, "\x1b[H\x1b[2J"+frame)
	}

	draw()
	resizeCh, stopResize := notifyResize()
	defer stopResize()
	done := make(chan struct{})
	defer close(done)
	if resizeCh != nil {
		go func() {
			for {
				select {
				case <-done:
					return
				case <-resizeCh:
					draw()
				}
			}
		}()
	}

	buf := make([]byte, 64)
	for {
		n, err := os.Stdin.Read(buf)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		keys := parseTUIKeys(buf[:n])
		mu.Lock()
		quit := false
		for _, k := range keys {
			if m.HandleKey(k) {
				quit = true
				break
			}
		}
		mu.Unlock()
		if quit {
			return nil
		}
		draw()
	}
}

func parseTUIKeys(b []byte) []tuiKey {
	var keys []tuiKey
	for i := 0; i < len(b); {
		c := b[i]
		switch c {
		case 0x03:
			keys = append(keys, tuiKey{kind: keyCtrlC})
			i++
		case 0x0d, 0x0a:
			keys = append(keys, tuiKey{kind: keyEnter})
			i++
		case 0x0e:
			keys = append(keys, tuiKey{kind: keyCtrlN})
			i++
		case 0x10:
			keys = append(keys, tuiKey{kind: keyCtrlP})
			i++
		case 0x15:
			keys = append(keys, tuiKey{kind: keyCtrlU})
			i++
		case 0x17:
			keys = append(keys, tuiKey{kind: keyCtrlW})
			i++
		case 0x7f, 0x08:
			keys = append(keys, tuiKey{kind: keyBackspace})
			i++
		case 0x1b:
			if i+1 < len(b) && b[i+1] == '[' {
				j := i + 2
				for j < len(b) && b[j] >= 0x30 && b[j] <= 0x3f {
					j++
				}
				for j < len(b) && b[j] >= 0x20 && b[j] <= 0x2f {
					j++
				}
				if j < len(b) && b[j] >= 0x40 && b[j] <= 0x7e {
					params := string(b[i+2 : j])
					if k, ok := parseCSIKey(params, b[j]); ok {
						keys = append(keys, k)
					}
					i = j + 1
					continue
				}
			}
			if i+2 < len(b) && b[i+1] == 'O' {
				if k, ok := parseCSIKey("", b[i+2]); ok {
					keys = append(keys, k)
				}
				i += 3
				continue
			}
			keys = append(keys, tuiKey{kind: keyEsc})
			i++
		default:
			r, size := utf8.DecodeRune(b[i:])
			if r != utf8.RuneError && r >= 0x20 {
				keys = append(keys, tuiKey{kind: keyRune, r: r})
			}
			i += max(1, size)
		}
	}
	return keys
}

func parseCSIKey(params string, final byte) (tuiKey, bool) {
	switch final {
	case 'A':
		return tuiKey{kind: keyUp}, true
	case 'B':
		return tuiKey{kind: keyDown}, true
	case 'C':
		return tuiKey{kind: keyRight}, true
	case 'D':
		return tuiKey{kind: keyLeft}, true
	case 'H':
		return tuiKey{kind: keyHome}, true
	case 'F':
		return tuiKey{kind: keyEnd}, true
	case '~':
		code, _, _ := strings.Cut(params, ";")
		switch code {
		case "1", "7":
			return tuiKey{kind: keyHome}, true
		case "4", "8":
			return tuiKey{kind: keyEnd}, true
		case "5":
			return tuiKey{kind: keyPgUp}, true
		case "6":
			return tuiKey{kind: keyPgDown}, true
		}
	}
	return tuiKey{}, false
}

func deleteLastWordRunes(r []rune) []rune {
	i := len(r)
	for i > 0 && unicode.IsSpace(r[i-1]) {
		i--
	}
	for i > 0 && !unicode.IsSpace(r[i-1]) {
		i--
	}
	return r[:i]
}

// sliceColumns extracts a horizontal display-width window [offset, offset+width)
// from a line without splitting grapheme clusters.
func sliceColumns(line string, offset, width int) string {
	if width <= 0 {
		return ""
	}
	var b strings.Builder
	col := 0
	end := offset + width
	g := uniseg.NewGraphemes(line)
	for g.Next() {
		cluster := g.Str()
		cw := uniseg.StringWidth(cluster)
		if col+cw <= offset {
			col += cw
			continue
		}
		if col >= end {
			break
		}
		if col < offset {
			// A wide character starts before offset; pad the remaining columns.
			b.WriteString(strings.Repeat(" ", min(col+cw-offset, end-offset)))
			col += cw
			continue
		}
		if col+cw > end {
			break
		}
		b.WriteString(cluster)
		col += cw
	}
	return b.String()
}

func formatScrollPercent(yOffset, maxOffset int) string {
	if maxOffset <= 0 {
		return "100%"
	}
	return fmt.Sprintf("%3d%%", yOffset*100/maxOffset)
}
