package main

import (
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	shell32      = syscall.NewLazyDLL("shell32.dll")
	shellExecute = shell32.NewProc("ShellExecuteW")
)

// IsAdmin reports whether the current process is running with an elevated
// (administrator) token. It checks TokenElevation rather than group membership,
// so a standard UAC token of an admin user correctly reports false.
func (a *App) IsAdmin() bool {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return false
	}
	defer token.Close()

	var elevation uint32
	var size uint32
	err := windows.GetTokenInformation(
		token,
		uint32(windows.TokenElevation),
		(*byte)(unsafe.Pointer(&elevation)),
		uint32(unsafe.Sizeof(elevation)),
		&size,
	)
	if err != nil {
		return false
	}
	return elevation != 0
}

// RequestElevation relaunches the app with administrator privileges via a UAC
// prompt. The current process keeps running; the caller should exit afterwards.
// Returns true if the elevated process was successfully launched (i.e. the user
// accepted the UAC prompt).
func (a *App) RequestElevation() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}

	verb := syscall.StringToUTF16Ptr("runas")
	file := syscall.StringToUTF16Ptr(exe)
	params := syscall.StringToUTF16Ptr("--elevated")
	dir := syscall.StringToUTF16Ptr("")

	ret, _, _ := shellExecute.Call(
		uintptr(0), // hwnd: parent window, 0 = desktop
		uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(file)),
		uintptr(unsafe.Pointer(params)),
		uintptr(unsafe.Pointer(dir)),
		1, // nShowCmd: SW_SHOWNORMAL
	)
	// ShellExecute returns a value > 32 on success (HINSTANCE cast to int).
	return int(ret) > 32
}
