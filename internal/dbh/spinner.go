package dbh

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/term"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	spinnerDelay    = 120 * time.Millisecond
	spinnerInterval = 80 * time.Millisecond
)

func isTerminalWriter(w io.Writer) bool {
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	return term.IsTerminal(int(f.Fd()))
}

// startSpinner shows an inline ANSI spinner with elapsed time on w while an
// operation is running. It is a no-op when w is not an interactive terminal.
func startSpinner(w io.Writer, label string) func() {
	if !isTerminalWriter(w) {
		return func() {}
	}
	return startSpinnerWithDelay(w, label, spinnerDelay, spinnerInterval)
}

func startSpinnerWithDelay(w io.Writer, label string, delay, interval time.Duration) func() {
	started := time.Now()
	done := make(chan struct{})
	finished := make(chan struct{})

	go func() {
		defer close(finished)
		drawn := false
		if delay > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-done:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		frame := 0
		render := func() {
			elapsed := time.Since(started).Truncate(100 * time.Millisecond)
			_, _ = fmt.Fprintf(w, "\r\x1b[2K%s %s... %.1fs", spinnerFrames[frame%len(spinnerFrames)], label, elapsed.Seconds())
			frame++
			drawn = true
		}
		render()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				if drawn {
					_, _ = io.WriteString(w, "\r\x1b[2K")
				}
				return
			case <-ticker.C:
				render()
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			<-finished
		})
	}
}
