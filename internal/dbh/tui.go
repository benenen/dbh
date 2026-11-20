package dbh

import (
	"fmt"
	"io"
	"os"
	"strings"
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

	draw := func() {
		w, h, err := term.GetSize(outFd)
		if err != nil || w <= 0 || h <= 0 {
			w, h = 80, 24
		}
		m.Resize(w, h)
		frame := strings.ReplaceAll(m.Render(), "\n", "\r\n")
		_, _ = io.WriteString(os.Stdout, "\x1b[H\x1b[2J"+frame)
	}

	draw()
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
		for _, k := range keys {
			if m.HandleKey(k) {
				return nil
			}
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
		case 0x7f, 0x08:
			keys = append(keys, tuiKey{kind: keyBackspace})
			i++
		case 0x1b:
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
				switch b[i+2] {
				case 'A':
					keys = append(keys, tuiKey{kind: keyUp})
					i += 3
					continue
				case 'B':
					keys = append(keys, tuiKey{kind: keyDown})
					i += 3
					continue
				case 'C':
					keys = append(keys, tuiKey{kind: keyRight})
					i += 3
					continue
				case 'D':
					keys = append(keys, tuiKey{kind: keyLeft})
					i += 3
					continue
				case 'H':
					keys = append(keys, tuiKey{kind: keyHome})
					i += 3
					continue
				case 'F':
					keys = append(keys, tuiKey{kind: keyEnd})
					i += 3
					continue
				case '5', '6', '1', '4':
					if i+3 < len(b) && b[i+3] == '~' {
						switch b[i+2] {
						case '5':
							keys = append(keys, tuiKey{kind: keyPgUp})
						case '6':
							keys = append(keys, tuiKey{kind: keyPgDown})
						case '1':
							keys = append(keys, tuiKey{kind: keyHome})
						case '4':
							keys = append(keys, tuiKey{kind: keyEnd})
						}
						i += 4
						continue
					}
				}
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
