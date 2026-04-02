// SPDX-FileCopyrightText: Copyright 2025 Stacklok, Inc.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Spinner is a simple TTY-only spinner that shows animated progress.
// All methods are no-ops when stdout is not a terminal.
type Spinner struct {
	mu           sync.Mutex
	msg          string
	checkpointCh chan string // completed-step messages to print as ✓ lines
	stopCh       chan struct{}
	doneCh       chan struct{}
}

// spinnerFrames are braille-pattern animation frames.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// NewSpinner creates a new Spinner with the given message.
func NewSpinner(msg string) *Spinner {
	return &Spinner{
		msg:          msg,
		checkpointCh: make(chan string, 8),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start launches the spinner goroutine. Call Stop or Fail to end it.
func (s *Spinner) Start() {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	go func() {
		defer close(s.doneCh)
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()
		i := 0
		for {
			select {
			case <-s.stopCh:
				// Drain any pending checkpoints before exiting.
				for {
					select {
					case doneMsg := <-s.checkpointCh:
						printCheckpoint(doneMsg)
					default:
						return
					}
				}
			case doneMsg := <-s.checkpointCh:
				printCheckpoint(doneMsg)
			case <-ticker.C:
				frame := lipgloss.NewStyle().Foreground(ColorBlue).Render(spinnerFrames[i%len(spinnerFrames)])
				s.mu.Lock()
				label := lipgloss.NewStyle().Foreground(ColorDim2).Render(s.msg)
				s.mu.Unlock()
				fmt.Printf("\r\033[K  %s  %s", frame, label)
				i++
			}
		}
	}()
}

// printCheckpoint prints a completed step as a ✓ line (called from the goroutine).
func printCheckpoint(doneMsg string) {
	check := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render("✓")
	msg := lipgloss.NewStyle().Foreground(ColorDim2).Render(doneMsg)
	fmt.Printf("\r\033[K  %s  %s\n", check, msg)
}

// Checkpoint commits the current step as done (prints ✓ doneMsg) and keeps
// the spinner running. Safe to call from any goroutine.
func (s *Spinner) Checkpoint(doneMsg string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	s.checkpointCh <- doneMsg
}

// Update changes the spinner message while it is running.
func (s *Spinner) Update(msg string) {
	s.mu.Lock()
	s.msg = msg
	s.mu.Unlock()
}

// Stop halts the spinner and prints a final success line.
func (s *Spinner) Stop(successMsg string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	close(s.stopCh)
	<-s.doneCh
	check := lipgloss.NewStyle().Foreground(ColorGreen).Bold(true).Render("✓")
	msg := lipgloss.NewStyle().Foreground(ColorText).Bold(true).Render(successMsg)
	fmt.Printf("\r\033[K  %s  %s\n", check, msg)
}

// Fail halts the spinner and prints a final error line.
func (s *Spinner) Fail(errMsg string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return
	}
	close(s.stopCh)
	<-s.doneCh
	cross := lipgloss.NewStyle().Foreground(ColorRed).Bold(true).Render("✗")
	msg := lipgloss.NewStyle().Foreground(ColorRed).Render(errMsg)
	fmt.Printf("\r\033[K  %s  %s\n", cross, msg)
}
