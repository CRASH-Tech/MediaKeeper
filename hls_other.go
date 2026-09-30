//go:build !unix

package main

import "os"

// pauseProcess cannot suspend a process on this system: ffmpeg runs on, and
// a whole converted film may end up in the temporary folder.
func pauseProcess(p *os.Process, pause bool) bool { return false }
