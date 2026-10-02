//go:build !unix && !windows

package main

func diskFree(string) int64 { return -1 }
