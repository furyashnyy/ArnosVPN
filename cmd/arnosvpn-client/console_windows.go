package main

import (
	"os"
	"syscall"
)

// attachParentConsole reattaches stdout/stderr to the parent terminal's console
// when the app is launched with CLI arguments. The Windows binary is linked as a
// GUI app (-H windowsgui) so a double-click opens the native window with no
// console flash; this restores visible output for `arnosvpn-client <cmd>` run
// from cmd.exe or PowerShell.
func attachParentConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1 = ATTACH_PARENT_PROCESS
	r, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("AttachConsole").Call(attachParentProcess)
	if r == 0 {
		return // launched without a parent console (double-click) — keep it clean
	}
	if h, err := syscall.Open("CONOUT$", syscall.O_RDWR, 0); err == nil {
		os.Stdout = os.NewFile(uintptr(h), "CONOUT$")
		os.Stderr = os.NewFile(uintptr(h), "CONOUT$")
	}
}
