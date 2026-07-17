//go:build !windows

package main

// attachParentConsole is a no-op off Windows (other platforms already inherit
// the terminal's stdout/stderr).
func attachParentConsole() {}
