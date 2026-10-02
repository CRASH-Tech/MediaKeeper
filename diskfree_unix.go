//go:build unix

package main

import "syscall"

// diskFree is the space left to an ordinary user on the disk of path, or -1
// when it cannot be told.
func diskFree(path string) int64 {
	var st syscall.Statfs_t
	if syscall.Statfs(path, &st) != nil {
		return -1
	}
	return int64(uint64(st.Bavail) * uint64(st.Bsize))
}
