package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// isTTY reports whether f is connected to a terminal.
// When false, the spinner is suppressed so piped output stays clean.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

var spinFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner renders a braille animation with a message to an io.Writer.
// All methods are safe to call concurrently. When isTTY is false every
// method is a no-op so piped output stays clean.
type Spinner struct {
	out   io.Writer
	isTTY bool

	mu   sync.Mutex
	msg  string
	live bool
	done chan struct{}
}

func NewSpinner(out io.Writer, isTTY bool) *Spinner {
	return &Spinner{out: out, isTTY: isTTY}
}

// Start begins the animation with msg.
func (s *Spinner) Start(msg string) {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	s.msg = msg
	s.live = true
	s.done = make(chan struct{})
	s.mu.Unlock()
	go s.run()
}

func (s *Spinner) run() {
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for i := 0; ; i++ {
		select {
		case <-s.done:
			return
		case <-tick.C:
			s.mu.Lock()
			if s.live {
				fmt.Fprintf(s.out, "\r%s %s", spinFrames[i%len(spinFrames)], s.msg)
			}
			s.mu.Unlock()
		}
	}
}

// Pause stops the animation and clears the spinner line so other output
// can be written without interleaving. Call Resume to restart.
func (s *Spinner) Pause() {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	s.live = false
	s.mu.Unlock()
	s.clear()
}

// Resume restarts the animation with a new message.
func (s *Spinner) Resume(msg string) {
	if !s.isTTY {
		return
	}
	s.mu.Lock()
	s.msg = msg
	s.live = true
	s.mu.Unlock()
}

// Stop halts the animation and clears the spinner line.
func (s *Spinner) Stop() {
	if !s.isTTY {
		return
	}
	close(s.done)
	s.mu.Lock()
	s.live = false
	s.mu.Unlock()
	s.clear()
}

func (s *Spinner) clear() {
	fmt.Fprintf(s.out, "\r%s\r", strings.Repeat(" ", 60))
}
