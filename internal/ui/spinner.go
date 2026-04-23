package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// spinnerFrames is a small braille-dot animation that's readable in
// every modern monospace font and plays well with short operations.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// SpinnerStartDelay — quick operations finish faster than the first
// frame, so no spinner ever appears. This avoids a flash of animation
// that would immediately clear and just add terminal noise.
const SpinnerStartDelay = 150 * time.Millisecond

// spinnerTickInterval controls how fast the frame advances. ~12 Hz
// feels alive without being distracting.
const spinnerTickInterval = 80 * time.Millisecond

// StartSpinner draws a stderr-bound braille spinner until stop() is
// called. Non-TTY stderr (pipes, CI, `2>file`) makes it a no-op so
// piped output streams stay clean. The first frame is delayed so sub-
// SpinnerStartDelay operations never draw anything to clear.
//
// Usage:
//
//	stop := ui.StartSpinner("fetching origin...")
//	err := longOp()
//	stop()
//
// The returned stop func is idempotent-safe for single-call usage.
// Pair with defer at the call site for exception-safe cleanup.
func StartSpinner(msg string) (stop func()) {
	return startSpinnerTo(os.Stderr, msg)
}

// startSpinnerTo is the testable core — most callers should use
// StartSpinner, but tests can inject a bytes.Buffer to verify the
// clear sequence or compare frame output.
func startSpinnerTo(w io.Writer, msg string) (stop func()) {
	// If the target writer is os.Stderr but stderr is not a TTY, this
	// would paint garbage into log files. Skip silently in that case.
	if w == os.Stderr && !IsStderrTerminal() {
		return func() {}
	}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-done:
			return // operation finished before we ever drew
		case <-time.After(SpinnerStartDelay):
		}
		t := time.NewTicker(spinnerTickInterval)
		defer t.Stop()
		i := 0
		for {
			fmt.Fprintf(w, "\r%s %s", spinnerFrames[i], msg)
			select {
			case <-done:
				// Clear with BOTH strategies so legacy terminals that
				// don't parse `\x1b[2K` (serial consoles, some CI log
				// viewers, `script(1)` transcripts) still end clean:
				// overwrite with spaces first, then emit the erase.
				pad := strings.Repeat(" ", len(msg)+4)
				fmt.Fprint(w, "\r"+pad+"\r\x1b[2K")
				return
			case <-t.C:
				i = (i + 1) % len(spinnerFrames)
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}
