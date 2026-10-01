//go:build unix

package main

import (
	"os"
	"syscall"
)

// pauseProcess suspends or resumes a process and reports whether it is
// suspended now.
func pauseProcess(p *os.Process, pause bool) bool {
	sig := syscall.SIGCONT
	if pause {
		sig = syscall.SIGSTOP
	}
	return p.Signal(sig) == nil && pause
}
